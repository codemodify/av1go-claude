// Command decode is a CLI tool that parses AV1 bitstreams from various
// container formats and prints a summary of the OBU structure.
//
// Supported input formats (auto-detected):
//   - MP4/ISOBMFF (.mp4, .m4v) — starts with "ftyp" box
//   - IVF (.ivf) — starts with "DKIF" signature
//   - Low-overhead OBU stream (.obu) — concatenated OBUs with has_size=1
//   - Annex B (.annexb) — leb128-delimited temporal units
//
// Usage:
//
//	go run ./cmd/decode <file>
package main

import (
	"av1go/annexb"
	"av1go/bitstream"
	"av1go/decoder"
	"av1go/ivf"
	"av1go/mp4"
	"av1go/obu"
	"fmt"
	"os"
	"strings"
)

// inputFormat represents the detected container format.
type inputFormat int

const (
	formatUnknown inputFormat = iota
	formatMP4
	formatIVF
	formatOBUStream
	formatAnnexB
)

func (f inputFormat) String() string {
	switch f {
	case formatMP4:
		return "MP4"
	case formatIVF:
		return "IVF"
	case formatOBUStream:
		return "OBU Stream (low-overhead)"
	case formatAnnexB:
		return "Annex B"
	default:
		return "Unknown"
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s [--decode] [--output FILE] <file>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nSupported formats: MP4, IVF, OBU stream, Annex B\n")
		fmt.Fprintf(os.Stderr, "\nFlags:\n")
		fmt.Fprintf(os.Stderr, "  --decode        Decode pixels (output Y4M or PPM)\n")
		fmt.Fprintf(os.Stderr, "  --output FILE   Output file (default: output.y4m or output.ppm)\n")
		os.Exit(1)
	}

	doDecode := false
	outputPath := ""
	maxFrames := 0
	outputAll := false
	cdfTrace := false
	noFilters := false
	var path string

	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--decode":
			doDecode = true
		case "--output":
			if i+1 < len(os.Args) {
				i++
				outputPath = os.Args[i]
			}
		case "--limit":
			if i+1 < len(os.Args) {
				i++
				fmt.Sscanf(os.Args[i], "%d", &maxFrames)
			}
		case "--all":
			outputAll = true
		case "--cdftrace":
			cdfTrace = true
		case "--nofilters":
			noFilters = true
		default:
			path = os.Args[i]
		}
	}

	if path == "" {
		fmt.Fprintf(os.Stderr, "error: no input file specified\n")
		os.Exit(1)
	}

	if doDecode {
		if outputPath == "" {
			outputPath = "output.ppm"
		}
		if err := runDecode(path, outputPath, maxFrames, outputAll, cdfTrace, noFilters); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(path); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	format := detectFormat(data)
	fmt.Printf("File: %s\n", path)
	fmt.Printf("Size: %d bytes\n", len(data))
	fmt.Printf("Format: %s\n\n", format)

	switch format {
	case formatMP4:
		return runMP4(path)
	case formatIVF:
		return runIVF(data)
	case formatOBUStream:
		return runOBUStream(data)
	case formatAnnexB:
		return runAnnexB(data)
	default:
		return fmt.Errorf("unrecognized input format.\n\nThe file does not match any supported format:\n"+
			"  - MP4: must start with an ISOBMFF box (ftyp, moov, mdat, free, skip)\n"+
			"  - IVF: must start with \"DKIF\" signature\n"+
			"  - OBU stream: must start with a valid OBU header with has_size=1\n"+
			"  - Annex B: must start with a valid leb128 temporal_unit_size\n\n"+
			"File starts with: %02x", data[:min(16, len(data))])
	}
}

// detectFormat examines the first bytes of data to determine the container format.
func detectFormat(data []byte) inputFormat {
	if len(data) < 4 {
		return formatUnknown
	}

	// IVF: starts with "DKIF"
	if data[0] == 'D' && data[1] == 'K' && data[2] == 'I' && data[3] == 'F' {
		return formatIVF
	}

	// MP4: look for ISOBMFF box types at offset 4.
	// A valid MP4 starts with a box whose type is typically "ftyp", but could
	// also be "moov", "mdat", "free", "skip", or "wide".
	if len(data) >= 8 {
		boxType := string(data[4:8])
		switch boxType {
		case "ftyp", "moov", "mdat", "free", "skip", "wide", "pnot":
			return formatMP4
		}
	}

	// OBU stream: first byte should be a valid OBU header with has_size=1.
	// OBU header: forbidden_bit(1) must be 0, obu_type(4) in valid range,
	// obu_has_size_field(1) must be 1.
	if isValidOBUStreamStart(data) {
		return formatOBUStream
	}

	// Annex B: starts with a leb128 temporal_unit_size.
	// Try to parse: the leb128 value should be reasonable and the data
	// following should contain valid frame units.
	if isValidAnnexBStart(data) {
		return formatAnnexB
	}

	return formatUnknown
}

// isValidOBUStreamStart checks if data starts with a valid OBU with has_size=1.
func isValidOBUStreamStart(data []byte) bool {
	if len(data) < 2 {
		return false
	}
	b0 := data[0]

	// forbidden bit must be 0
	if b0&0x80 != 0 {
		return false
	}

	// obu_has_size_field must be 1
	if b0&0x02 == 0 {
		return false
	}

	// obu_type must be a known type
	obuType := obu.Type((b0 >> 3) & 0x0F)
	switch obuType {
	case obu.TypeSequenceHeader, obu.TypeTemporalDelimiter, obu.TypeFrameHeader,
		obu.TypeTileGroup, obu.TypeMetadata, obu.TypeFrame,
		obu.TypeRedundantFrameHeader, obu.TypeTileList, obu.TypePadding:
		// valid type
	default:
		return false
	}

	// Try to parse the full first OBU to ensure it's well-formed.
	_, n, err := obu.ParseUnit(data)
	return err == nil && n > 0
}

// isValidAnnexBStart checks if data starts with a plausible Annex B stream.
func isValidAnnexBStart(data []byte) bool {
	if len(data) < 2 {
		return false
	}

	// Try to read leb128 temporal_unit_size.
	tuSize, tuSizeLen, err := bitstream.ReadLeb128(data)
	if err != nil {
		return false
	}

	// The size must be reasonable: > 0 and <= remaining data.
	if tuSize == 0 || int(tuSize) > len(data)-tuSizeLen {
		return false
	}

	// Within the temporal unit, try to read a frame_unit_size.
	tuData := data[tuSizeLen:]
	fuSize, fuSizeLen, err := bitstream.ReadLeb128(tuData)
	if err != nil {
		return false
	}

	// frame_unit_size must fit within temporal_unit_size.
	if fuSize == 0 || fuSizeLen+int(fuSize) > int(tuSize) {
		return false
	}

	// Within the frame unit, try to read an obu_length and check the OBU header.
	fuData := tuData[fuSizeLen:]
	obuLen, obuLenSize, err := bitstream.ReadLeb128(fuData)
	if err != nil {
		return false
	}
	if obuLen == 0 || obuLenSize+int(obuLen) > int(fuSize) {
		return false
	}

	// The OBU should have forbidden_bit=0 and has_size=0 (Annex B convention).
	obuStart := fuData[obuLenSize:]
	if len(obuStart) < 1 {
		return false
	}
	b0 := obuStart[0]
	if b0&0x80 != 0 { // forbidden bit
		return false
	}
	// In Annex B, OBUs should have has_size_field=0.
	if b0&0x02 != 0 {
		return false
	}

	return true
}

// --- MP4 path (existing) ---

func runMP4(path string) error {
	f, err := mp4.Open(path)
	if err != nil {
		return fmt.Errorf("open MP4: %w", err)
	}

	fmt.Printf("Tracks: %d\n\n", len(f.Tracks))

	vt := f.VideoTrack()
	if vt == nil {
		return fmt.Errorf("no AV1 video track found")
	}

	fmt.Printf("Video Track:\n")
	fmt.Printf("  Codec:     %s\n", vt.Codec)
	fmt.Printf("  Timescale: %d\n", vt.Timescale)

	var seqHdr *obu.SequenceHeader
	if len(vt.AV1ConfigOBUs) > 0 {
		fmt.Printf("\nav1C configOBUs (%d bytes):\n", len(vt.AV1ConfigOBUs))
		configUnits, err := obu.ParseUnits(vt.AV1ConfigOBUs)
		if err != nil {
			return fmt.Errorf("parse configOBUs: %w", err)
		}
		for _, u := range configUnits {
			fmt.Printf("  %s (size=%d)\n", u.Header.Type, u.Size)
			if u.Header.Type == obu.TypeSequenceHeader {
				seqHdr, err = obu.ParseSequenceHeader(u.Payload)
				if err != nil {
					return fmt.Errorf("parse sequence header: %w", err)
				}
				printSequenceHeader(seqHdr)
			}
		}
	}

	if len(vt.AV1Config) >= 4 {
		seqProfile := (vt.AV1Config[1] >> 5) & 7
		seqLevelIdx := vt.AV1Config[1] & 0x1f
		fmt.Printf("\nav1C Record:\n")
		fmt.Printf("  Profile: %d\n", seqProfile)
		fmt.Printf("  Level:   %d\n", seqLevelIdx)
	}

	samples := vt.Samples()
	fmt.Printf("\nSamples: %d\n", len(samples))

	maxDetailed := 10
	syncCount := 0
	frameTypes := make(map[obu.FrameType]int)

	for i, s := range samples {
		data, err := f.ReadSample(vt, i)
		if err != nil {
			return fmt.Errorf("read sample %d: %w", i, err)
		}
		units, err := obu.ParseUnits(data)
		if err != nil {
			fmt.Printf("  [sample %d] OBU parse error: %v\n", i, err)
			continue
		}
		if s.IsSync {
			syncCount++
		}
		for _, u := range units {
			if u.Header.Type == obu.TypeFrame || u.Header.Type == obu.TypeFrameHeader {
				if seqHdr != nil {
					fh, err := obu.ParseBasicFrameHeader(u.Payload, seqHdr)
					if err == nil {
						frameTypes[fh.FrameType]++
					}
				}
			}
		}
		if i < maxDetailed {
			syncStr := ""
			if s.IsSync {
				syncStr = " [SYNC]"
			}
			fmt.Printf("\n  Sample %d: %d bytes%s\n", i, s.Size, syncStr)
			for _, u := range units {
				extStr := ""
				if u.Header.HasExtension && u.Header.Extension != nil {
					extStr = fmt.Sprintf(" (tid=%d, sid=%d)",
						u.Header.Extension.TemporalID, u.Header.Extension.SpatialID)
				}
				fmt.Printf("    %s size=%d%s\n", u.Header.Type, u.Size, extStr)
				if u.Header.Type == obu.TypeSequenceHeader {
					sh, err := obu.ParseSequenceHeader(u.Payload)
					if err == nil {
						seqHdr = sh
					}
				}
				if (u.Header.Type == obu.TypeFrame || u.Header.Type == obu.TypeFrameHeader) && seqHdr != nil {
					fh, err := obu.ParseBasicFrameHeader(u.Payload, seqHdr)
					if err == nil {
						showStr := ""
						if fh.ShowExistingFrame {
							showStr = fmt.Sprintf("show_existing_frame (idx=%d)", fh.FrameToShowMapIdx)
						} else {
							showStr = fmt.Sprintf("type=%s show=%v", fh.FrameType, fh.ShowFrame)
						}
						fmt.Printf("      Frame: %s\n", showStr)
					}
				}
			}
		}
	}

	printSummary(seqHdr, len(samples), syncCount, frameTypes)
	return nil
}

// --- IVF path ---

func runIVF(data []byte) error {
	d, err := ivf.OpenData(data)
	if err != nil {
		return fmt.Errorf("open IVF: %w", err)
	}

	fmt.Printf("IVF Header:\n")
	fmt.Printf("  Codec:      %s\n", d.Header.CodecString())
	fmt.Printf("  Dimensions: %dx%d\n", d.Header.Width, d.Header.Height)
	fmt.Printf("  Timebase:   %d/%d\n", d.Header.TimebaseNum, d.Header.TimebaseDen)
	fmt.Printf("  Frames:     %d (header)\n", d.Header.FrameCount)

	if !d.Header.IsAV1() {
		fmt.Printf("\nWarning: codec is %q, not AV1. OBU parsing may fail.\n", d.Header.CodecString())
	}

	count, err := d.FrameCount()
	if err != nil {
		return fmt.Errorf("counting frames: %w", err)
	}
	fmt.Printf("  Frames:     %d (scanned)\n", count)

	var seqHdr *obu.SequenceHeader
	maxDetailed := 10
	frameTypes := make(map[obu.FrameType]int)
	totalOBUs := 0

	for i := 0; i < count; i++ {
		frame, err := d.ReadFrame(i)
		if err != nil {
			return fmt.Errorf("read frame %d: %w", i, err)
		}

		units, err := obu.ParseUnits(frame.Data)
		if err != nil {
			fmt.Printf("  [frame %d] OBU parse error: %v\n", i, err)
			continue
		}
		totalOBUs += len(units)

		for _, u := range units {
			if u.Header.Type == obu.TypeSequenceHeader && seqHdr == nil {
				seqHdr, _ = obu.ParseSequenceHeader(u.Payload)
			}
			if (u.Header.Type == obu.TypeFrame || u.Header.Type == obu.TypeFrameHeader) && seqHdr != nil {
				fh, err := obu.ParseBasicFrameHeader(u.Payload, seqHdr)
				if err == nil {
					frameTypes[fh.FrameType]++
				}
			}
		}

		if i < maxDetailed {
			fmt.Printf("\n  Frame %d: %d bytes, ts=%d\n", i, frame.Size, frame.Timestamp)
			for _, u := range units {
				fmt.Printf("    %s size=%d\n", u.Header.Type, u.Size)
				if u.Header.Type == obu.TypeSequenceHeader {
					sh, err := obu.ParseSequenceHeader(u.Payload)
					if err == nil {
						printSequenceHeader(sh)
					}
				}
				if (u.Header.Type == obu.TypeFrame || u.Header.Type == obu.TypeFrameHeader) && seqHdr != nil {
					fh, err := obu.ParseBasicFrameHeader(u.Payload, seqHdr)
					if err == nil {
						printFrameInfo(fh)
					}
				}
			}
		}
	}

	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Total frames: %d\n", count)
	fmt.Printf("Total OBUs:   %d\n", totalOBUs)
	if seqHdr != nil {
		printSeqHdrSummary(seqHdr)
	}
	printFrameTypeSummary(frameTypes)
	return nil
}

// --- OBU stream path ---

func runOBUStream(data []byte) error {
	units, err := obu.ReadOBUStream(data)
	if err != nil {
		return fmt.Errorf("parse OBU stream: %w", err)
	}

	fmt.Printf("OBU count: %d\n", len(units))

	var seqHdr *obu.SequenceHeader
	maxDetailed := 20
	frameTypes := make(map[obu.FrameType]int)
	typeCounts := make(map[obu.Type]int)

	for i, u := range units {
		typeCounts[u.Header.Type]++

		if u.Header.Type == obu.TypeSequenceHeader && seqHdr == nil {
			seqHdr, _ = obu.ParseSequenceHeader(u.Payload)
		}
		if (u.Header.Type == obu.TypeFrame || u.Header.Type == obu.TypeFrameHeader) && seqHdr != nil {
			fh, err := obu.ParseBasicFrameHeader(u.Payload, seqHdr)
			if err == nil {
				frameTypes[fh.FrameType]++
			}
		}

		if i < maxDetailed {
			extStr := ""
			if u.Header.HasExtension && u.Header.Extension != nil {
				extStr = fmt.Sprintf(" (tid=%d, sid=%d)",
					u.Header.Extension.TemporalID, u.Header.Extension.SpatialID)
			}
			fmt.Printf("  [%d] %s size=%d%s\n", i, u.Header.Type, u.Size, extStr)
			if u.Header.Type == obu.TypeSequenceHeader {
				sh, err := obu.ParseSequenceHeader(u.Payload)
				if err == nil {
					printSequenceHeader(sh)
				}
			}
			if (u.Header.Type == obu.TypeFrame || u.Header.Type == obu.TypeFrameHeader) && seqHdr != nil {
				fh, err := obu.ParseBasicFrameHeader(u.Payload, seqHdr)
				if err == nil {
					printFrameInfo(fh)
				}
			}
		}
	}

	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Total OBUs: %d\n", len(units))
	fmt.Printf("OBU types:\n")
	for t, c := range typeCounts {
		fmt.Printf("  %s: %d\n", t, c)
	}
	if seqHdr != nil {
		printSeqHdrSummary(seqHdr)
	}
	printFrameTypeSummary(frameTypes)
	return nil
}

// --- Annex B path ---

func runAnnexB(data []byte) error {
	r := annexb.NewReader(data)
	tus, err := r.ReadAll()
	if err != nil {
		return fmt.Errorf("parse Annex B: %w", err)
	}

	fmt.Printf("Temporal units: %d\n", len(tus))

	var seqHdr *obu.SequenceHeader
	maxDetailed := 10
	totalFUs := 0
	totalOBUs := 0
	frameTypes := make(map[obu.FrameType]int)

	for i, tu := range tus {
		totalFUs += len(tu.FrameUnits)
		for _, fu := range tu.FrameUnits {
			totalOBUs += len(fu.OBUs)
		}

		allOBUs := annexb.AllOBUs([]annexb.TemporalUnit{tu})
		for _, u := range allOBUs {
			if u.Header.Type == obu.TypeSequenceHeader && seqHdr == nil {
				seqHdr, _ = obu.ParseSequenceHeader(u.Payload)
			}
			if (u.Header.Type == obu.TypeFrame || u.Header.Type == obu.TypeFrameHeader) && seqHdr != nil {
				fh, err := obu.ParseBasicFrameHeader(u.Payload, seqHdr)
				if err == nil {
					frameTypes[fh.FrameType]++
				}
			}
		}

		if i < maxDetailed {
			fmt.Printf("\n  TU %d: size=%d, frame_units=%d\n", i, tu.Size, len(tu.FrameUnits))
			for j, fu := range tu.FrameUnits {
				fmt.Printf("    FU %d: size=%d, obus=%d\n", j, fu.Size, len(fu.OBUs))
				for k, u := range fu.OBUs {
					fmt.Printf("      [%d] %s size=%d\n", k, u.Header.Type, u.Size)
					if u.Header.Type == obu.TypeSequenceHeader {
						sh, err := obu.ParseSequenceHeader(u.Payload)
						if err == nil {
							printSequenceHeader(sh)
						}
					}
					if (u.Header.Type == obu.TypeFrame || u.Header.Type == obu.TypeFrameHeader) && seqHdr != nil {
						fh, err := obu.ParseBasicFrameHeader(u.Payload, seqHdr)
						if err == nil {
							printFrameInfo(fh)
						}
					}
				}
			}
		}
	}

	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Temporal units: %d\n", len(tus))
	fmt.Printf("Frame units:    %d\n", totalFUs)
	fmt.Printf("Total OBUs:     %d\n", totalOBUs)
	if seqHdr != nil {
		printSeqHdrSummary(seqHdr)
	}
	printFrameTypeSummary(frameTypes)
	return nil
}

// --- Shared printing helpers ---

func printSequenceHeader(sh *obu.SequenceHeader) {
	fmt.Printf("    Sequence Header:\n")
	fmt.Printf("      Profile:          %d\n", sh.SeqProfile)
	fmt.Printf("      Max frame size:   %dx%d\n", sh.MaxFrameWidth(), sh.MaxFrameHeight())
	fmt.Printf("      Bit depth:        %d\n", sh.ColorConfig.BitDepth())
	fmt.Printf("      Mono chrome:      %v\n", sh.ColorConfig.MonoChrome)
	fmt.Printf("      Color range:      ")
	if sh.ColorConfig.ColorRange {
		fmt.Println("full")
	} else {
		fmt.Println("studio")
	}
	fmt.Printf("      Subsampling:      %d:%d\n", sh.ColorConfig.SubsamplingX, sh.ColorConfig.SubsamplingY)
	fmt.Printf("      128x128 SB:       %v\n", sh.Use128x128Superblock)
	fmt.Printf("      Filter intra:     %v\n", sh.EnableFilterIntra)
	fmt.Printf("      CDEF:             %v\n", sh.EnableCDEF)
	fmt.Printf("      Restoration:      %v\n", sh.EnableRestoration)
	fmt.Printf("      Film grain:       %v\n", sh.FilmGrainParamsPresent)
	if !sh.ReducedStillPictureHeader {
		fmt.Printf("      Order hint:       %v\n", sh.EnableOrderHint)
		fmt.Printf("      Warped motion:    %v\n", sh.EnableWarpedMotion)
		fmt.Printf("      Dual filter:      %v\n", sh.EnableDualFilter)
	}
	if len(sh.OperatingPoints) > 0 {
		for i, op := range sh.OperatingPoints {
			fmt.Printf("      OP[%d]: idc=%d level=%d tier=%d\n",
				i, op.Idc, op.SeqLevelIdx, op.SeqTier)
		}
	}
}

func printFrameInfo(fh *obu.FrameHeader) {
	if fh.ShowExistingFrame {
		fmt.Printf("      Frame: show_existing_frame (idx=%d)\n", fh.FrameToShowMapIdx)
	} else {
		fmt.Printf("      Frame: type=%s show=%v OrderHint=%d\n", fh.FrameType, fh.ShowFrame, fh.OrderHint)
	}
}

func printSeqHdrSummary(sh *obu.SequenceHeader) {
	fmt.Printf("Resolution:     %dx%d\n", sh.MaxFrameWidth(), sh.MaxFrameHeight())
	fmt.Printf("Bit depth:      %d\n", sh.ColorConfig.BitDepth())
	fmt.Printf("Profile:        %d\n", sh.SeqProfile)
	subsampling := "4:4:4"
	if sh.ColorConfig.MonoChrome {
		subsampling = "monochrome"
	} else if sh.ColorConfig.SubsamplingX == 1 && sh.ColorConfig.SubsamplingY == 1 {
		subsampling = "4:2:0"
	} else if sh.ColorConfig.SubsamplingX == 1 {
		subsampling = "4:2:2"
	}
	fmt.Printf("Chroma:         %s\n", subsampling)
}

func printFrameTypeSummary(frameTypes map[obu.FrameType]int) {
	if len(frameTypes) > 0 {
		fmt.Printf("Frame types:\n")
		for ft, count := range frameTypes {
			fmt.Printf("  %s: %d\n", ft, count)
		}
	}
}

func printSummary(seqHdr *obu.SequenceHeader, totalSamples, syncCount int, frameTypes map[obu.FrameType]int) {
	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Total samples:  %d\n", totalSamples)
	fmt.Printf("Sync samples:   %d\n", syncCount)
	if seqHdr != nil {
		printSeqHdrSummary(seqHdr)
	}
	printFrameTypeSummary(frameTypes)
}

// --- Pixel decode path ---

func runDecode(inputPath, outputPath string, maxFrames int, outputAll bool, cdfTrace bool, noFilters bool) error {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	format := detectFormat(data)
	fmt.Printf("File: %s\n", inputPath)
	fmt.Printf("Format: %s\n", format)

	// Extract OBU samples from the container.
	var samples [][]byte
	switch format {
	case formatMP4:
		samples, err = extractMP4Samples(inputPath)
	case formatIVF:
		samples, err = extractIVFSamples(data)
	case formatOBUStream:
		samples, err = extractOBUStreamSamples(data)
	default:
		return fmt.Errorf("pixel decoding not supported for format %s", format)
	}
	if err != nil {
		return err
	}

	fmt.Printf("Samples: %d\n", len(samples))

	dec := decoder.New()
	dec.ForceOutputHidden = outputAll
	dec.CDFTrace = cdfTrace
	dec.NoLoopFilters = noFilters
	var frames []*decoder.FrameBuffer

	for i, sample := range samples {
		units, err := obu.ParseUnits(sample)
		if err != nil {
			fmt.Printf("  sample %d: parse error: %v (skipping)\n", i, err)
			continue
		}

		if outputAll {
			allFrames, err := dec.DecodeAllOBUs(units)
			if err != nil {
				fmt.Printf("  sample %d: decode error: %v (stopping)\n", i, err)
				break
			}
			for _, f := range allFrames {
				frames = append(frames, f)
				fmt.Printf("  Decoded frame %d: %dx%d\n", len(frames)-1, f.Width, f.Height)
			}
		} else {
			frame, err := dec.DecodeOBUs(units)
			if err != nil {
				fmt.Printf("  sample %d: decode error: %v (stopping)\n", i, err)
				break
			}
			if frame != nil {
				frames = append(frames, frame)
				fmt.Printf("  Decoded frame %d: %dx%d\n", len(frames)-1, frame.Width, frame.Height)
			}
		}

		// Limit to --limit frames if specified, otherwise decode all.
		if maxFrames > 0 && len(frames) >= maxFrames {
			break
		}
	}

	if len(frames) == 0 {
		return fmt.Errorf("no frames decoded")
	}

	// Write output.
	if strings.HasSuffix(outputPath, ".y4m") {
		if err := decoder.WriteY4MFile(outputPath, frames); err != nil {
			return fmt.Errorf("writing Y4M: %w", err)
		}
	} else if strings.HasSuffix(outputPath, ".yuv") {
		// Write all frames as raw YUV
		f, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("creating YUV file: %w", err)
		}
		defer f.Close()
		for _, fr := range frames {
			if err := decoder.WriteRawYUVTo(f, fr); err != nil {
				return fmt.Errorf("writing raw YUV: %w", err)
			}
		}
	} else {
		// Default: write first frame as PPM.
		if err := decoder.WritePPM(outputPath, frames[0]); err != nil {
			return fmt.Errorf("writing PPM: %w", err)
		}
	}

	fmt.Printf("\nOutput: %s (%d frame(s))\n", outputPath, len(frames))
	return nil
}

func extractMP4Samples(path string) ([][]byte, error) {
	f, err := mp4.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open MP4: %w", err)
	}

	vt := f.VideoTrack()
	if vt == nil {
		return nil, fmt.Errorf("no AV1 video track found")
	}

	sampleList := vt.Samples()
	var samples [][]byte
	for i := range sampleList {
		data, err := f.ReadSample(vt, i)
		if err != nil {
			return nil, fmt.Errorf("read sample %d: %w", i, err)
		}
		samples = append(samples, data)
	}
	return samples, nil
}

func extractIVFSamples(data []byte) ([][]byte, error) {
	d, err := ivf.OpenData(data)
	if err != nil {
		return nil, fmt.Errorf("open IVF: %w", err)
	}

	count, err := d.FrameCount()
	if err != nil {
		return nil, fmt.Errorf("counting frames: %w", err)
	}

	var samples [][]byte
	for i := 0; i < count; i++ {
		frame, err := d.ReadFrame(i)
		if err != nil {
			return nil, fmt.Errorf("read frame %d: %w", i, err)
		}
		samples = append(samples, frame.Data)
	}
	return samples, nil
}

func extractOBUStreamSamples(data []byte) ([][]byte, error) {
	// For OBU stream, treat the entire stream as one "sample".
	return [][]byte{data}, nil
}
