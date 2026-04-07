// Package decoder implements an AV1 video decoder.
//
// This decoder parses AV1 bitstreams (from MP4, IVF, or raw OBU streams),
// performs entropy decoding, intra/inter prediction, inverse transforms,
// and produces decoded YUV frames.
//
// Current status: keyframe decoding with intra prediction. Inter-frame
// prediction (motion compensation) is not yet implemented.
package decoder

import (
	"av1go/obu"
	"fmt"
	"io"
	"os"
)


// Decoder is an AV1 video decoder.
type Decoder struct {
	seqHdr *obu.SequenceHeader

	// Reference frame buffers (for inter prediction).
	refFrames [8]*FrameBuffer

	// Order hints for each reference frame buffer slot.
	// Updated when frames are stored via refresh_frame_flags.
	// Used by set_frame_refs() to derive reference indices.
	refOrderHints [8]uint32

	// Saved CDF contexts per reference frame slot.
	// Used by primary_ref_frame to initialize CDFs for new frames.
	refCDFs [8]*CDFContext

	// Temporal MV storage per reference buffer slot (8x8 resolution).
	refTMVs       [8][]TemporalMV
	refTMVStrides [8]int

	// Each reference frame slot's own reference POCs (for temporal MV projection).
	// refRefPOC[slot][i] = the order hint of reference i as seen by the frame in slot.
	// Needed by LoadTMVs to compute ref2ref distances (dav1d's ref_ref_poc).
	refRefPOC [8][7]uint32

	// Saved global motion parameters per reference frame slot.
	// Used by PrevGmParams when primary_ref_frame references a saved frame.
	refGmParams [8][obu.RefsPerFrame][6]int32

	// Frame type per reference slot, used for show_existing_frame keyframe detection.
	refFrameType [8]obu.FrameType

	// Statistics.
	FramesDecoded   int
	shownFrameCount int

	// ForceOutputHidden: when true, return hidden frames from decodeFrame.
	ForceOutputHidden bool

	// CDFTrace: when true, dump per-component CDF checksums at frame boundaries.
	CDFTrace bool

	// NoLoopFilters: when true, skip all in-loop filters (deblock, CDEF, LR).
	NoLoopFilters bool
}

// New creates a new AV1 decoder.
func New() *Decoder {
	return &Decoder{}
}

// GetRefFrame returns the reference frame at the given slot (for testing).
func (d *Decoder) GetRefFrame(slot int) *FrameBuffer {
	if slot < 0 || slot >= 8 {
		return nil
	}
	return d.refFrames[slot]
}

// GetRefCDFs returns the saved CDF context for a reference frame slot (for testing).
func (d *Decoder) GetRefCDFs(slot int) *CDFContext {
	if slot < 0 || slot >= 8 {
		return nil
	}
	return d.refCDFs[slot]
}

// DecodeOBUs decodes a sequence of OBUs (a temporal unit) and returns
// the decoded frame, or nil if no displayable frame is produced.
func (d *Decoder) DecodeOBUs(units []obu.Unit) (*FrameBuffer, error) {
	var frame *FrameBuffer

	for _, u := range units {
		switch u.Header.Type {
		case obu.TypeTemporalDelimiter:
			// Nothing to do.

		case obu.TypeSequenceHeader:
			sh, err := obu.ParseSequenceHeader(u.Payload)
			if err != nil {
				return nil, fmt.Errorf("decoder: sequence header: %w", err)
			}
			d.seqHdr = sh

		case obu.TypeFrame:
			if d.seqHdr == nil {
				return nil, fmt.Errorf("decoder: frame OBU before sequence header")
			}
			f, err := d.decodeFrame(u.Payload)
			if err != nil {
				return nil, fmt.Errorf("decoder: frame: %w", err)
			}
			frame = f

		case obu.TypeFrameHeader:
			// A standalone OBU_FRAME_HEADER is used for show_existing_frame
			// (no tile data needed). Parse it the same way as TypeFrame.
			if d.seqHdr == nil {
				return nil, fmt.Errorf("decoder: frame header OBU before sequence header")
			}
			f, err := d.decodeFrame(u.Payload)
			if err != nil {
				return nil, fmt.Errorf("decoder: frame header: %w", err)
			}
			frame = f

		case obu.TypeTileGroup:
			// Handled as part of TypeFrame above.

		default:
			// Skip metadata, padding, etc.
		}
	}

	return frame, nil
}

// DecodeAllOBUs decodes a sequence of OBUs and returns ALL decoded frames
// (including invisible/hidden ones), not just the last shown frame.
func (d *Decoder) DecodeAllOBUs(units []obu.Unit) ([]*FrameBuffer, error) {
	var frames []*FrameBuffer

	for _, u := range units {
		switch u.Header.Type {
		case obu.TypeTemporalDelimiter:
			// Nothing to do.

		case obu.TypeSequenceHeader:
			sh, err := obu.ParseSequenceHeader(u.Payload)
			if err != nil {
				return nil, fmt.Errorf("decoder: sequence header: %w", err)
			}
			d.seqHdr = sh

		case obu.TypeFrame, obu.TypeFrameHeader:
			if d.seqHdr == nil {
				return nil, fmt.Errorf("decoder: frame OBU before sequence header")
			}
			f, err := d.decodeFrame(u.Payload)
			if err != nil {
				return nil, fmt.Errorf("decoder: frame: %w", err)
			}
			if f != nil {
				frames = append(frames, f)
			}

		default:
			// Skip other OBU types.
		}
	}

	return frames, nil
}

// decodeFrame decodes a Frame OBU payload (frame header + tile group).
// Returns the decoded frame buffer if displayable, or nil for hidden frames.
// Hidden frames (ShowFrame=false) are fully decoded and stored in reference
// buffers but nil is returned so the caller does not output them.
func (d *Decoder) decodeFrame(payload []byte) (*FrameBuffer, error) {
	// Parse the complete frame header.
	fh, headerBytes, err := ParseFullFrameHeader(payload, d.seqHdr, d.refOrderHints, d.refGmParams)
	if err != nil {
		return nil, fmt.Errorf("frame header: %w", err)
	}

	if fh.ShowExistingFrame {
		// Show a previously decoded frame from the reference buffer.
		idx := fh.FrameToShowMapIdx
		if d.refFrames[idx] == nil {
			return nil, fmt.Errorf("show_existing_frame: ref %d is nil", idx)
		}
		shownFrame := d.refFrames[idx]
		if d.CDFTrace {
			fmt.Fprintf(os.Stderr, "SHOW_EXISTING d=%d idx=%d OH_ref=%d\n",
				d.FramesDecoded, idx, d.refOrderHints[idx])
		}

		// AV1 spec Section 7.21: when show_existing_frame shows a KEY_FRAME,
		// all 8 reference slots must be refreshed with the shown frame's state.
		// IMPORTANT: must check the STORED frame type (d.refFrameType[idx]),
		// NOT fh.FrameType, because show_existing_frame OBUs don't parse frame_type
		// from the bitstream — fh.FrameType defaults to 0 (KEY_FRAME) for all of them.
		if d.refFrameType[idx] == obu.FrameTypeKey {
			for i := 0; i < 8; i++ {
				d.refFrames[i] = shownFrame
				d.refOrderHints[i] = d.refOrderHints[idx]
				d.refCDFs[i] = d.refCDFs[idx]
				d.refTMVs[i] = nil
				d.refTMVStrides[i] = 0
				d.refRefPOC[i] = [7]uint32{}
				d.refGmParams[i] = [obu.RefsPerFrame][6]int32{}
				d.refFrameType[i] = obu.FrameTypeKey
			}
		}

		d.shownFrameCount++
		return shownFrame, nil
	}



	// Temporary trace for frame structure analysis.
	if d.CDFTrace {
		fmt.Fprintf(os.Stderr, "FRAME d=%d OH=%d type=%d show=%v refresh=0x%02x\n",
			d.FramesDecoded, fh.OrderHint, fh.FrameType, fh.ShowFrame, fh.RefreshFrameFlags)
	}

	// Allocate the output frame buffer.
	w := int(fh.FrameWidth)
	h := int(fh.FrameHeight)
	subX := int(d.seqHdr.ColorConfig.SubsamplingX)
	subY := int(d.seqHdr.ColorConfig.SubsamplingY)
	frame := NewFrameBuffer(w, h, subX, subY)

	// The tile group data starts after the frame header.
	tileData := payload[headerBytes:]
	if d.CDFTrace {
		fmt.Fprintf(os.Stderr, "FRAME_DATA d=%d payload=%d hdrBytes=%d tileData=%d tileCols=%d tileRows=%d MiCols=%d MiRows=%d\n",
			d.FramesDecoded, len(payload), headerBytes, len(tileData),
			fh.TileCols, fh.TileRows, fh.MiCols, fh.MiRows)
	}

	// Allocate per-block filter data for in-loop filters.
	miRows := int(fh.MiRows)
	miCols := int(fh.MiCols)
	deblockInfo := make([][]DeblockInfo, miRows)
	for r := 0; r < miRows; r++ {
		deblockInfo[r] = make([]DeblockInfo, miCols)
	}
	cdefIndices := make(map[uint32]int)

	// Allocate loop restoration parameter arrays for each plane.
	var lrParams [3][]LRUnitParams
	for p := 0; p < 3; p++ {
		if fh.LRType[p] != FrameRestoreNone {
			// Compute restoration unit grid dimensions for this plane.
			unitSizeLog2 := 6 + fh.LRUnitShift
			if d.seqHdr.Use128x128Superblock {
				unitSizeLog2 = 7 + fh.LRUnitShift
			}
			if p > 0 && fh.LRUVShift {
				unitSizeLog2--
			}
			unitSize := 1 << unitSizeLog2

			planeW := w
			planeH := h
			if p > 0 {
				planeW = (w + subX) >> subX
				planeH = (h + subY) >> subY
			}
			halfUnit := unitSize >> 1
			unitCols := 1
			if planeW > unitSize {
				unitCols = (planeW + halfUnit) / unitSize
			}
			unitRows := 1
			if planeH > unitSize {
				unitRows = (planeH + halfUnit) / unitSize
			}
			lrParams[p] = make([]LRUnitParams, unitCols*unitRows)
		}
	}

	// Initialize CDF context based on primary_ref_frame.
	var initCDF *CDFContext
	if fh.PrimaryRefFrame == primaryRefNone {
		initCDF = NewDefaultCDFContextForQP(int(fh.BaseQIndex))
	} else {
		refSlot := fh.RefFrameIdx[fh.PrimaryRefFrame]
		if d.refCDFs[refSlot] != nil {
			initCDF = d.refCDFs[refSlot]
		} else {
			initCDF = NewDefaultCDFContext()
		}
	}
	if d.CDFTrace {
		fmt.Fprintf(os.Stderr, "GO_INIT d=%d OH=%d prf=%d\n", d.FramesDecoded, fh.OrderHint, fh.PrimaryRefFrame)
		fmt.Fprintf(os.Stderr, "  init_filter:")
		for a := 0; a < 2; a++ {
			for b := 0; b < 8; b++ {
				cdf := initCDF.SwitchableFilter[a][b]
				fmt.Fprintf(os.Stderr, " %d,%d,%d/%d", cdf[0], cdf[1], cdf[2], cdf[3])
			}
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Allocate frame-level ModeInfo grid for multi-row MV scanning.
	miGrid := NewMiGrid(miRows, miCols)

	// Load projected temporal MVs if use_ref_frame_mvs is enabled.
	var projTMVs []TemporalMV
	var projStride int
	if fh.UseRefFrameMVs {
		projTMVs, projStride = LoadTMVs(d.refTMVs, d.refTMVStrides, fh, d.seqHdr, d.refOrderHints, d.refRefPOC)
	}
	// Decode all tiles.
	savedCDF, err := DecodeTileGroup(tileData, fh, d.seqHdr, frame, d.refFrames, d.refOrderHints, deblockInfo, cdefIndices, lrParams, initCDF, miGrid, projTMVs, projStride)
	if err != nil {
		return nil, fmt.Errorf("tile group: %w", err)
	}


	// Apply in-loop filters (deblock -> CDEF -> LR, interleaved per SB row).
	if !d.NoLoopFilters {
		applyLoopFiltersInterleaved(frame, fh, d.seqHdr, deblockInfo, cdefIndices, lrParams)
	}

	// Save temporal MVs from the ModeInfo grid for future frames.
	// Only inter frames (not keyframes or intra-only) have temporal MVs.
	// Matches dav1d: IS_INTER_OR_SWITCH check before save_tmvs, and
	// mvs_ref=NULL for non-inter frames (no allocation at line 3641).
	var frameTMVs []TemporalMV
	var frameTMVStride int
	isInterOrSwitch := fh.FrameType == obu.FrameTypeInter || fh.FrameType == obu.FrameTypeSwitch
	if d.seqHdr.EnableOrderHint && isInterOrSwitch {
		orderHintBits := int(d.seqHdr.OrderHintBitsMinus1) + 1
		var refSigns [7]bool
		for i := 0; i < 7; i++ {
			slot := int(fh.RefFrameIdx[i])
			refOH := int(d.refOrderHints[slot])
			curOH := int(fh.OrderHint)
			dist := getPocDiff(orderHintBits, refOH, curOH)
			refSigns[i] = dist < 0
		}
		frameTMVs, frameTMVStride = SaveTMVs(miGrid, miRows, miCols, refSigns)
	}

	// Update reference frame buffers, order hints, saved CDFs, and temporal MVs.
	refreshFlags := fh.RefreshFrameFlags
	if fh.FrameType == obu.FrameTypeKey && fh.ShowFrame {
		refreshFlags = 0xFF // all slots
	}
	var curRefPOC [7]uint32
	for i := 0; i < 7; i++ {
		curRefPOC[i] = d.refOrderHints[fh.RefFrameIdx[i]]
	}

	for i := 0; i < 8; i++ {
		if refreshFlags&(1<<uint(i)) != 0 {
			d.refFrames[i] = frame
			d.refOrderHints[i] = fh.OrderHint
			if savedCDF != nil {
				d.refCDFs[i] = savedCDF
			} else {
				d.refCDFs[i] = initCDF
			}
			d.refTMVs[i] = frameTMVs
			d.refTMVStrides[i] = frameTMVStride
			d.refRefPOC[i] = curRefPOC
			d.refGmParams[i] = fh.GmParams
			d.refFrameType[i] = fh.FrameType
		}
	}

	// CDF trace: dump raw CDF values for comparison with dav1d
	if savedCDF != nil && d.CDFTrace {
		fmt.Fprintf(os.Stderr, "GO_CDF d=%d OH=%d\n", d.FramesDecoded, fh.OrderHint)
		// comp_bwd_ref: [2][3] x {val,cnt}
		fmt.Fprintf(os.Stderr, "  bwdRef:")
		for a := 0; a < 2; a++ {
			for b := 0; b < 3; b++ {
				fmt.Fprintf(os.Stderr, " %d/%d", savedCDF.CompBwdRef[a][b][0], savedCDF.CompBwdRef[a][b][1])
			}
		}
		fmt.Fprintf(os.Stderr, "\n")
		// filter: [2][8] x {v0,v1,v2,cnt}
		fmt.Fprintf(os.Stderr, "  filter:")
		for a := 0; a < 2; a++ {
			for b := 0; b < 8; b++ {
				cdf := savedCDF.SwitchableFilter[a][b]
				fmt.Fprintf(os.Stderr, " %d,%d,%d/%d", cdf[0], cdf[1], cdf[2], cdf[3])
			}
		}
		fmt.Fprintf(os.Stderr, "\n")
		// comp_fwd_ref: [3][3] x {val,cnt}
		fmt.Fprintf(os.Stderr, "  fwdRef:")
		for a := 0; a < 3; a++ {
			for b := 0; b < 3; b++ {
				fmt.Fprintf(os.Stderr, " %d/%d", savedCDF.CompRef[a][b][0], savedCDF.CompRef[a][b][1])
			}
		}
		fmt.Fprintf(os.Stderr, "\n")
		// skip: [3] x {val,cnt}
		fmt.Fprintf(os.Stderr, "  skip:")
		for a := 0; a < 3; a++ {
			fmt.Fprintf(os.Stderr, " %d/%d", savedCDF.Skip[a][0], savedCDF.Skip[a][1])
		}
		fmt.Fprintf(os.Stderr, "\n")
		// partition first context: [levels][0]
		fmt.Fprintf(os.Stderr, "  part0:")
		for a := 0; a < len(savedCDF.Partition); a += 4 {
			cdf := savedCDF.Partition[a]
			fmt.Fprintf(os.Stderr, " {")
			for c := 0; c < len(cdf); c++ {
				if c > 0 {
					fmt.Fprintf(os.Stderr, ",")
				}
				fmt.Fprintf(os.Stderr, "%d", cdf[c])
			}
			fmt.Fprintf(os.Stderr, "}")
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	d.FramesDecoded++
	if fh.ShowFrame {
		d.shownFrameCount++
		return frame, nil
	}

	// Hidden frame: fully decoded and references updated, but not displayed.
	if d.ForceOutputHidden {
		return frame, nil
	}
	return nil, nil
}

// WriteY4M writes a decoded frame to a Y4M file.
// Y4M is a simple uncompressed video format that can be viewed with
// ffplay, mpv, or converted with ffmpeg.
func WriteY4M(w io.Writer, frame *FrameBuffer, frameIdx int) error {
	if frameIdx == 0 {
		// Write Y4M file header.
		header := fmt.Sprintf("YUV4MPEG2 W%d H%d F25:1 Ip A1:1 C420\n",
			frame.Width, frame.Height)
		if _, err := w.Write([]byte(header)); err != nil {
			return err
		}
	}

	// Write frame header.
	if _, err := w.Write([]byte("FRAME\n")); err != nil {
		return err
	}

	// Write Y plane.
	for y := 0; y < frame.Height; y++ {
		row := frame.Y[y*frame.StrideY : y*frame.StrideY+frame.Width]
		if _, err := w.Write(row); err != nil {
			return err
		}
	}

	// Write U plane.
	chromaW := (frame.Width + 1) >> 1
	chromaH := (frame.Height + 1) >> 1
	for y := 0; y < chromaH; y++ {
		row := frame.U[y*frame.StrideU : y*frame.StrideU+chromaW]
		if _, err := w.Write(row); err != nil {
			return err
		}
	}

	// Write V plane.
	for y := 0; y < chromaH; y++ {
		row := frame.V[y*frame.StrideV : y*frame.StrideV+chromaW]
		if _, err := w.Write(row); err != nil {
			return err
		}
	}

	return nil
}

// WriteRawYUVTo writes a single frame as raw planar YUV420 to an io.Writer.
func WriteRawYUVTo(w io.Writer, frame *FrameBuffer) error {
	// Y plane
	for y := 0; y < frame.Height; y++ {
		row := frame.Y[y*frame.StrideY : y*frame.StrideY+frame.Width]
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	// U plane
	chromaW := (frame.Width + 1) >> 1
	chromaH := (frame.Height + 1) >> 1
	for y := 0; y < chromaH; y++ {
		row := frame.U[y*frame.StrideU : y*frame.StrideU+chromaW]
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	// V plane
	for y := 0; y < chromaH; y++ {
		row := frame.V[y*frame.StrideV : y*frame.StrideV+chromaW]
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

// applyLoopFiltersInterleaved applies deblock → CDEF → LR per SB row,
// matching dav1d's single-threaded interleaved pipeline.
//
// Key: dav1d defers the CDEF of the last 2 MI rows (8 pixels) of each SB row
// to the start of the next SB row. This ensures CDEF reads post-deblock data
// at the bottom boundary (from the next SB row, which has been deblocked by
// that point). The LR range per SB row is shifted by 8 pixels to align with
// the deferred CDEF processing.
func applyLoopFiltersInterleaved(frame *FrameBuffer, fh *DecodedFrameHeader,
	sh *obu.SequenceHeader, deblockInfo [][]DeblockInfo,
	cdefIndices map[uint32]int, lrParams [3][]LRUnitParams) {

	sbSize := 64
	if sh.Use128x128Superblock {
		sbSize = 128
	}
	sbRows := (frame.Height + sbSize - 1) / sbSize

	// MI units per SB row.
	sbMISize := sbSize / 4
	miRows := int(fh.MiRows)

	// Build deblock LUT once (shared across SB rows).
	lut := BuildDeblockLUT(int(fh.LoopFilterSharpness))

	subX := int(sh.ColorConfig.SubsamplingX)
	subY := int(sh.ColorConfig.SubsamplingY)

	// LPF frame: stores post-deblock data for LR cross-stripe boundary reads.
	lpfFrame := NewFrameBuffer(frame.Width, frame.Height, subX, subY)
	copy(lpfFrame.Y, frame.Y)
	copy(lpfFrame.U, frame.U)
	copy(lpfFrame.V, frame.V)

	// Pre-LR snapshot: stores post-CDEF data for within-stripe LR reads.
	preLR := NewFrameBuffer(frame.Width, frame.Height, subX, subY)
	copy(preLR.Y, frame.Y)
	copy(preLR.U, frame.U)
	copy(preLR.V, frame.V)

	// CDEF source snapshot: provides pre-CDEF data for direction finding
	// and top/bottom boundary taps. The left boundary for center rows uses
	// lr_bak (in-place backup) per dav1d's approach.
	cdefSrc := NewFrameBuffer(frame.Width, frame.Height, subX, subY)
	copy(cdefSrc.Y, frame.Y)
	copy(cdefSrc.U, frame.U)
	copy(cdefSrc.V, frame.V)

	chromaH := (frame.Height + subY) >> subY

	for sby := 0; sby < sbRows; sby++ {
		// 1. Deblock this SB row.
		ApplyDeblockingSBRow(frame, fh, sh, deblockInfo, &lut, sby)

		// 2. Copy post-deblock rows to lpfFrame.
		// Horizontal deblocking modifies pixels above the SB boundary (up to
		// 7 rows for wide filters), so re-copy those overlap rows too.
		y0 := sby * sbSize
		y1 := y0 + sbSize
		if y1 > frame.Height {
			y1 = frame.Height
		}
		if sby > 0 {
			overlap := y0 - 8
			if overlap < 0 {
				overlap = 0
			}
			y0 = overlap
		}
		// Also copy extended buffer rows beyond the visible frame. Deblock
		// can modify pixels up to 3 rows past the last MI boundary, which
		// may extend into the buffer padding area. CDEF direction finding
		// and the temp buffer padding read from these extended rows, so
		// cdefSrc must reflect post-deblock data there too.
		bufHY := len(frame.Y) / frame.StrideY
		y1ext := y1
		if y1 == frame.Height && bufHY > frame.Height {
			y1ext = bufHY
		}
		copyPixelRows(lpfFrame.Y, frame.Y, frame.StrideY, y0, y1)
		copyPixelRows(cdefSrc.Y, frame.Y, frame.StrideY, y0, y1ext)
		cy0 := y0 >> subY
		// Use (y1 + subY) >> subY to ensure the last chroma row is included
		// when luma height is odd. Plain y1 >> subY truncates, missing the
		// final chroma row that maps to the last luma row pair.
		cy1 := (y1 + subY) >> subY
		if cy1 > chromaH {
			cy1 = chromaH
		}
		bufHUV := len(frame.U) / frame.StrideU
		cy1ext := cy1
		if cy1 == chromaH && bufHUV > chromaH {
			cy1ext = bufHUV
		}
		copyPixelRows(lpfFrame.U, frame.U, frame.StrideU, cy0, cy1)
		copyPixelRows(cdefSrc.U, frame.U, frame.StrideU, cy0, cy1ext)
		copyPixelRows(lpfFrame.V, frame.V, frame.StrideV, cy0, cy1)
		copyPixelRows(cdefSrc.V, frame.V, frame.StrideV, cy0, cy1ext)

		// 4. CDEF with deferred row handling (matching dav1d).
		sbMIStart := sby * sbMISize
		sbMIEnd := sbMIStart + sbMISize
		if sbMIEnd > miRows {
			sbMIEnd = miRows
		}
		isLastSBRow := sby+1 >= sbRows

		// 4a. CDEF deferred rows from previous SB row (now that cdefSrc
		// has post-deblock data from this SB row for boundary reads).
		if sby > 0 {
			prevMIEnd := sbMIStart
			deferredStart := prevMIEnd - 2
			if deferredStart < 0 {
				deferredStart = 0
			}
			ApplyCDEFMIRange(frame, cdefSrc, fh, sh, cdefIndices, deblockInfo,
				deferredStart, prevMIEnd)

			// Copy post-CDEF deferred rows to preLR.
			deferredPixStart := deferredStart * 4
			deferredPixEnd := prevMIEnd * 4
			if deferredPixEnd > frame.Height {
				deferredPixEnd = frame.Height
			}
			copyPixelRows(preLR.Y, frame.Y, frame.StrideY, deferredPixStart, deferredPixEnd)
			chromaDeferStart := deferredPixStart >> subY
			chromaDeferEnd := (deferredPixEnd + subY) >> subY
			if chromaDeferEnd > chromaH {
				chromaDeferEnd = chromaH
			}
			copyPixelRows(preLR.U, frame.U, frame.StrideU, chromaDeferStart, chromaDeferEnd)
			copyPixelRows(preLR.V, frame.V, frame.StrideV, chromaDeferStart, chromaDeferEnd)
		}

		// 4b. CDEF main rows of this SB row (skip last 2 MI rows if not last).
		mainMIEnd := sbMIEnd
		if !isLastSBRow {
			mainMIEnd = sbMIEnd - 2
		}
		ApplyCDEFMIRange(frame, cdefSrc, fh, sh, cdefIndices, deblockInfo,
			sbMIStart, mainMIEnd)

		// 5. Copy post-CDEF main rows to preLR.
		mainPixStart := sbMIStart * 4
		mainPixEnd := mainMIEnd * 4
		if mainPixEnd > frame.Height {
			mainPixEnd = frame.Height
		}
		copyPixelRows(preLR.Y, frame.Y, frame.StrideY, mainPixStart, mainPixEnd)
		chromaMainStart := mainPixStart >> subY
		chromaMainEnd := (mainPixEnd + subY) >> subY
		if chromaMainEnd > chromaH {
			chromaMainEnd = chromaH
		}
		copyPixelRows(preLR.U, frame.U, frame.StrideU, chromaMainStart, chromaMainEnd)
		copyPixelRows(preLR.V, frame.V, frame.StrideV, chromaMainStart, chromaMainEnd)

		// 6. Loop restoration for this SB row's range.
		ApplyLRSBRow(frame, fh, sh, lrParams, preLR, lpfFrame, sby)

	}
}

// copyPixelRows copies pixel rows [y0, y1) from src to dst plane.
func copyPixelRows(dst, src []byte, stride, y0, y1 int) {
	for y := y0; y < y1; y++ {
		off := y * stride
		copy(dst[off:off+stride], src[off:off+stride])
	}
}

// copyPlaneRows copies pixel rows for a single SB row from src to dst plane.
func copyPlaneRows(dst, src []byte, stride, sby, sbSize, planeH int) {
	y0 := sby * sbSize
	y1 := y0 + sbSize
	if y1 > planeH {
		y1 = planeH
	}
	if y0 >= planeH {
		return
	}
	for y := y0; y < y1; y++ {
		off := y * stride
		copy(dst[off:off+stride], src[off:off+stride])
	}
}

func WriteRawYUV(path string, frame *FrameBuffer) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return WriteRawYUVTo(f, frame)
}

// WriteY4MFile writes decoded frames to a Y4M file at the given path.
func WriteY4MFile(path string, frames []*FrameBuffer) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	for i, frame := range frames {
		if err := WriteY4M(f, frame, i); err != nil {
			return err
		}
	}

	return nil
}

// WritePPM writes a single frame as a PPM (P6) image file.
// This converts YUV to RGB for display.
func WritePPM(path string, frame *FrameBuffer) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := frame.Width
	h := frame.Height

	// PPM header.
	header := fmt.Sprintf("P6\n%d %d\n255\n", w, h)
	if _, err := f.Write([]byte(header)); err != nil {
		return err
	}

	// Convert YUV420 to RGB.
	rgb := make([]byte, 3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			yVal := int(frame.Y[y*frame.StrideY+x])
			cx := x >> 1
			cy := y >> 1
			uVal := int(frame.U[cy*frame.StrideU+cx]) - 128
			vVal := int(frame.V[cy*frame.StrideV+cx]) - 128

			// BT.601 YUV to RGB conversion (studio range).
			r := yVal + ((91881*vVal + 32768) >> 16)
			g := yVal - ((22554*uVal + 46802*vVal + 32768) >> 16)
			b := yVal + ((116130*uVal + 32768) >> 16)

			rgb[0] = clipByte(r)
			rgb[1] = clipByte(g)
			rgb[2] = clipByte(b)
			if _, err := f.Write(rgb); err != nil {
				return err
			}
		}
	}

	return nil
}

func clipByte(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}
