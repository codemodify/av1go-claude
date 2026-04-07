// Package mp4 provides a minimal ISOBMFF (ISO 14496-12) demuxer for
// extracting AV1 video samples from MP4 files. It reads the moov/trak
// structure to locate sample entries of type "av01" and extracts sample
// data from the mdat box.
//
// This is not a general-purpose MP4 parser. It supports only the subset
// of boxes needed for basic AV1 sample extraction from unfragmented MP4
// files with a single AV1 video track.
package mp4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// BoxHeader represents the header of an ISOBMFF box.
type BoxHeader struct {
	Type   [4]byte
	Size   int64  // total box size including header
	Offset int64  // file offset where the box starts
}

// TypeString returns the 4CC box type as a string.
func (h BoxHeader) TypeString() string {
	return string(h.Type[:])
}

// Track holds extracted information about a single track.
type Track struct {
	TrackID     uint32
	HandlerType string // "vide", "soun", etc.
	Codec       string // "av01", "mp4a", etc.
	Timescale   uint32

	// AV1-specific: AV1CodecConfigurationRecord from av1C box.
	AV1Config    []byte // raw bytes of AV1CodecConfigurationRecord
	AV1ConfigOBUs []byte // configOBUs portion (OBU data embedded in av1C)

	// Sample table data
	sampleSizes   []uint32 // from stsz
	chunkOffsets  []int64  // from stco/co64
	sampleToChunk []sampleToChunkEntry // from stsc

	// Sync samples (random access points) from stss; nil means all are sync.
	syncSamples map[uint32]bool

	// Composition time offsets from ctts
	cttsEntries []cttsEntry
}

type sampleToChunkEntry struct {
	FirstChunk             uint32
	SamplesPerChunk        uint32
	SampleDescriptionIndex uint32
}

type cttsEntry struct {
	SampleCount  uint32
	SampleOffset int32
}

// Sample represents one coded sample (frame or audio AU).
type Sample struct {
	Index      int
	Offset     int64
	Size       uint32
	IsSync     bool
	CTSOffset  int32
}

// File represents a parsed MP4 file.
type File struct {
	Tracks []*Track
	r      io.ReaderAt
	size   int64
}

// Open opens and parses an MP4 file.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	mp4 := &File{r: f, size: info.Size()}
	if err := mp4.parse(); err != nil {
		f.Close()
		return nil, err
	}
	return mp4, nil
}

// OpenData parses MP4 from an in-memory byte slice.
func OpenData(data []byte) (*File, error) {
	ra := &bytesReaderAt{data: data}
	mp4 := &File{r: ra, size: int64(len(data))}
	if err := mp4.parse(); err != nil {
		return nil, err
	}
	return mp4, nil
}

type bytesReaderAt struct{ data []byte }

func (b *bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b.data)) {
		return 0, io.EOF
	}
	n := copy(p, b.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// VideoTrack returns the first AV1 video track, or nil if none found.
func (f *File) VideoTrack() *Track {
	for _, t := range f.Tracks {
		if t.HandlerType == "vide" && t.Codec == "av01" {
			return t
		}
	}
	return nil
}

// SampleCount returns the number of samples in this track.
func (t *Track) SampleCount() int {
	return len(t.sampleSizes)
}

// Samples returns metadata for all samples in the track.
func (t *Track) Samples() []Sample {
	offsets := t.computeSampleOffsets()
	samples := make([]Sample, len(t.sampleSizes))
	ctsIdx := 0
	ctsRemaining := uint32(0)
	ctsOffset := int32(0)
	if len(t.cttsEntries) > 0 {
		ctsRemaining = t.cttsEntries[0].SampleCount
		ctsOffset = t.cttsEntries[0].SampleOffset
	}
	for i := range samples {
		isSync := true
		if t.syncSamples != nil {
			_, isSync = t.syncSamples[uint32(i+1)] // stss uses 1-based
		}
		samples[i] = Sample{
			Index:     i,
			Offset:    offsets[i],
			Size:      t.sampleSizes[i],
			IsSync:    isSync,
			CTSOffset: ctsOffset,
		}
		if len(t.cttsEntries) > 0 {
			ctsRemaining--
			if ctsRemaining == 0 {
				ctsIdx++
				if ctsIdx < len(t.cttsEntries) {
					ctsRemaining = t.cttsEntries[ctsIdx].SampleCount
					ctsOffset = t.cttsEntries[ctsIdx].SampleOffset
				}
			}
		}
	}
	return samples
}

// ReadSample reads the raw data for sample at the given index (0-based).
func (f *File) ReadSample(t *Track, index int) ([]byte, error) {
	if index < 0 || index >= len(t.sampleSizes) {
		return nil, fmt.Errorf("mp4: sample index %d out of range [0, %d)", index, len(t.sampleSizes))
	}
	offsets := t.computeSampleOffsets()
	size := t.sampleSizes[index]
	buf := make([]byte, size)
	_, err := f.r.ReadAt(buf, offsets[index])
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf, nil
}

// ReadAllSamples reads all raw sample data for the given track.
func (f *File) ReadAllSamples(t *Track) ([][]byte, error) {
	samples := make([][]byte, len(t.sampleSizes))
	for i := range t.sampleSizes {
		data, err := f.ReadSample(t, i)
		if err != nil {
			return nil, err
		}
		samples[i] = data
	}
	return samples, nil
}

func (t *Track) computeSampleOffsets() []int64 {
	nSamples := len(t.sampleSizes)
	offsets := make([]int64, nSamples)

	// Build a mapping from chunk index (0-based) to (samplesPerChunk, firstSampleIndex)
	type chunkInfo struct {
		offset         int64
		samplesPerChunk uint32
	}

	nChunks := len(t.chunkOffsets)
	chunks := make([]chunkInfo, nChunks)
	for i := range chunks {
		chunks[i].offset = t.chunkOffsets[i]
	}

	// Apply stsc to determine samplesPerChunk for each chunk.
	// stsc entries use 1-based chunk numbers.
	for i, entry := range t.sampleToChunk {
		startChunk := int(entry.FirstChunk) - 1 // convert to 0-based
		var endChunk int
		if i+1 < len(t.sampleToChunk) {
			endChunk = int(t.sampleToChunk[i+1].FirstChunk) - 1
		} else {
			endChunk = nChunks
		}
		for c := startChunk; c < endChunk && c < nChunks; c++ {
			chunks[c].samplesPerChunk = entry.SamplesPerChunk
		}
	}

	// Now compute per-sample file offsets.
	sampleIdx := 0
	for chunkIdx := 0; chunkIdx < nChunks && sampleIdx < nSamples; chunkIdx++ {
		chunkOffset := chunks[chunkIdx].offset
		for s := uint32(0); s < chunks[chunkIdx].samplesPerChunk && sampleIdx < nSamples; s++ {
			offsets[sampleIdx] = chunkOffset
			chunkOffset += int64(t.sampleSizes[sampleIdx])
			sampleIdx++
		}
	}
	return offsets
}

// parse reads top-level boxes and extracts track information.
func (f *File) parse() error {
	return f.parseBoxes(0, f.size, 0)
}

func (f *File) readBoxHeader(offset int64) (BoxHeader, error) {
	var buf [8]byte
	if _, err := f.r.ReadAt(buf[:], offset); err != nil {
		return BoxHeader{}, err
	}
	h := BoxHeader{
		Size:   int64(binary.BigEndian.Uint32(buf[0:4])),
		Offset: offset,
	}
	copy(h.Type[:], buf[4:8])

	if h.Size == 1 {
		// 64-bit extended size
		var buf8 [8]byte
		if _, err := f.r.ReadAt(buf8[:], offset+8); err != nil {
			return BoxHeader{}, err
		}
		h.Size = int64(binary.BigEndian.Uint64(buf8[:]))
	} else if h.Size == 0 {
		// box extends to end of file
		h.Size = f.size - offset
	}
	return h, nil
}

func (f *File) headerSize(h BoxHeader) int64 {
	raw := make([]byte, 4)
	f.r.ReadAt(raw, h.Offset)
	if binary.BigEndian.Uint32(raw) == 1 {
		return 16
	}
	return 8
}

func (f *File) readBoxData(h BoxHeader) ([]byte, error) {
	hs := f.headerSize(h)
	dataSize := h.Size - hs
	if dataSize <= 0 {
		return nil, nil
	}
	data := make([]byte, dataSize)
	_, err := f.r.ReadAt(data, h.Offset+hs)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return data, nil
}

func (f *File) parseBoxes(offset, end int64, depth int) error {
	pos := offset
	for pos < end {
		h, err := f.readBoxHeader(pos)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		if h.Size < 8 {
			break
		}
		typ := h.TypeString()
		hs := f.headerSize(h)
		contentStart := pos + hs
		contentEnd := pos + h.Size

		switch typ {
		case "moov", "mdia", "minf", "dinf", "edts", "mvex":
			if err := f.parseBoxes(contentStart, contentEnd, depth+1); err != nil {
				return err
			}
		case "trak":
			if err := f.parseTrak(contentStart, contentEnd); err != nil {
				return err
			}
		case "stbl":
			// stbl is parsed by parseTrak -> parseMdia -> parseMinf -> parseStbl
			// when reached from top-level we skip
		}
		pos += h.Size
	}
	return nil
}

func (f *File) parseTrak(offset, end int64) error {
	track := &Track{}
	f.Tracks = append(f.Tracks, track)

	pos := offset
	for pos < end {
		h, err := f.readBoxHeader(pos)
		if err != nil {
			break
		}
		if h.Size < 8 {
			break
		}
		hs := f.headerSize(h)
		contentStart := pos + hs

		switch h.TypeString() {
		case "tkhd":
			if err := f.parseTkhd(track, h); err != nil {
				return err
			}
		case "mdia":
			if err := f.parseMdia(track, contentStart, pos+h.Size); err != nil {
				return err
			}
		}
		pos += h.Size
	}
	return nil
}

func (f *File) parseTkhd(track *Track, h BoxHeader) error {
	data, err := f.readBoxData(h)
	if err != nil {
		return err
	}
	version := data[0]
	if version == 1 {
		track.TrackID = binary.BigEndian.Uint32(data[20:24])
	} else {
		track.TrackID = binary.BigEndian.Uint32(data[12:16])
	}
	return nil
}

func (f *File) parseMdia(track *Track, offset, end int64) error {
	pos := offset
	for pos < end {
		h, err := f.readBoxHeader(pos)
		if err != nil {
			break
		}
		if h.Size < 8 {
			break
		}
		hs := f.headerSize(h)
		contentStart := pos + hs

		switch h.TypeString() {
		case "mdhd":
			if err := f.parseMdhd(track, h); err != nil {
				return err
			}
		case "hdlr":
			if err := f.parseHdlr(track, h); err != nil {
				return err
			}
		case "minf":
			if err := f.parseMinf(track, contentStart, pos+h.Size); err != nil {
				return err
			}
		}
		pos += h.Size
	}
	return nil
}

func (f *File) parseMdhd(track *Track, h BoxHeader) error {
	data, err := f.readBoxData(h)
	if err != nil {
		return err
	}
	version := data[0]
	if version == 1 {
		track.Timescale = binary.BigEndian.Uint32(data[20:24])
	} else {
		track.Timescale = binary.BigEndian.Uint32(data[12:16])
	}
	return nil
}

func (f *File) parseHdlr(track *Track, h BoxHeader) error {
	data, err := f.readBoxData(h)
	if err != nil {
		return err
	}
	// version(1) + flags(3) + pre_defined(4) + handler_type(4)
	if len(data) < 12 {
		return fmt.Errorf("mp4: hdlr box too short")
	}
	track.HandlerType = string(data[8:12])
	return nil
}

func (f *File) parseMinf(track *Track, offset, end int64) error {
	pos := offset
	for pos < end {
		h, err := f.readBoxHeader(pos)
		if err != nil {
			break
		}
		if h.Size < 8 {
			break
		}
		hs := f.headerSize(h)
		if h.TypeString() == "stbl" {
			if err := f.parseStbl(track, pos+hs, pos+h.Size); err != nil {
				return err
			}
		}
		pos += h.Size
	}
	return nil
}

func (f *File) parseStbl(track *Track, offset, end int64) error {
	pos := offset
	for pos < end {
		h, err := f.readBoxHeader(pos)
		if err != nil {
			break
		}
		if h.Size < 8 {
			break
		}

		switch h.TypeString() {
		case "stsd":
			if err := f.parseStsd(track, h); err != nil {
				return err
			}
		case "stsz":
			if err := f.parseStsz(track, h); err != nil {
				return err
			}
		case "stco":
			if err := f.parseStco(track, h); err != nil {
				return err
			}
		case "co64":
			if err := f.parseCo64(track, h); err != nil {
				return err
			}
		case "stsc":
			if err := f.parseStsc(track, h); err != nil {
				return err
			}
		case "stss":
			if err := f.parseStss(track, h); err != nil {
				return err
			}
		case "ctts":
			if err := f.parseCtts(track, h); err != nil {
				return err
			}
		}
		pos += h.Size
	}
	return nil
}

func (f *File) parseStsd(track *Track, h BoxHeader) error {
	data, err := f.readBoxData(h)
	if err != nil {
		return err
	}
	// version(1) + flags(3) + entry_count(4) = 8
	if len(data) < 8 {
		return fmt.Errorf("mp4: stsd too short")
	}
	entryCount := binary.BigEndian.Uint32(data[4:8])
	pos := 8
	for i := uint32(0); i < entryCount && pos+8 <= len(data); i++ {
		entrySize := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		entryType := string(data[pos+4 : pos+8])
		track.Codec = entryType

		if entryType == "av01" && pos+entrySize <= len(data) {
			f.parseAV01Entry(track, data[pos:pos+entrySize])
		}
		pos += entrySize
	}
	return nil
}

func (f *File) parseAV01Entry(track *Track, data []byte) {
	// VisualSampleEntry: 8 (box header) + 6 (reserved) + 2 (data_ref_idx)
	// + 2 (pre_defined) + 2 (reserved) + 12 (pre_defined)
	// + 2 (width) + 2 (height) + 4 (horiz_res) + 4 (vert_res)
	// + 4 (reserved) + 2 (frame_count) + 32 (compressor_name)
	// + 2 (depth) + 2 (pre_defined) = 86 bytes total
	// After that come sub-boxes.
	if len(data) < 86 {
		return
	}

	// Parse sub-boxes starting at offset 86
	pos := 86
	for pos+8 <= len(data) {
		boxSize := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		boxType := string(data[pos+4 : pos+8])
		if boxSize < 8 || pos+boxSize > len(data) {
			break
		}
		if boxType == "av1C" {
			configData := data[pos+8 : pos+boxSize]
			track.AV1Config = make([]byte, len(configData))
			copy(track.AV1Config, configData)
			// configOBUs start at byte 4 of AV1CodecConfigurationRecord
			if len(configData) > 4 {
				track.AV1ConfigOBUs = make([]byte, len(configData)-4)
				copy(track.AV1ConfigOBUs, configData[4:])
			}
		}
		pos += boxSize
	}
}

func (f *File) parseStsz(track *Track, h BoxHeader) error {
	data, err := f.readBoxData(h)
	if err != nil {
		return err
	}
	// version(1) + flags(3) + sample_size(4) + sample_count(4) = 12
	if len(data) < 12 {
		return fmt.Errorf("mp4: stsz too short")
	}
	sampleSize := binary.BigEndian.Uint32(data[4:8])
	sampleCount := binary.BigEndian.Uint32(data[8:12])
	track.sampleSizes = make([]uint32, sampleCount)

	if sampleSize != 0 {
		// Constant sample size
		for i := range track.sampleSizes {
			track.sampleSizes[i] = sampleSize
		}
	} else {
		// Variable sample sizes
		for i := uint32(0); i < sampleCount; i++ {
			off := 12 + int(i)*4
			if off+4 > len(data) {
				break
			}
			track.sampleSizes[i] = binary.BigEndian.Uint32(data[off : off+4])
		}
	}
	return nil
}

func (f *File) parseStco(track *Track, h BoxHeader) error {
	data, err := f.readBoxData(h)
	if err != nil {
		return err
	}
	if len(data) < 8 {
		return fmt.Errorf("mp4: stco too short")
	}
	entryCount := binary.BigEndian.Uint32(data[4:8])
	track.chunkOffsets = make([]int64, entryCount)
	for i := uint32(0); i < entryCount; i++ {
		off := 8 + int(i)*4
		if off+4 > len(data) {
			break
		}
		track.chunkOffsets[i] = int64(binary.BigEndian.Uint32(data[off : off+4]))
	}
	return nil
}

func (f *File) parseCo64(track *Track, h BoxHeader) error {
	data, err := f.readBoxData(h)
	if err != nil {
		return err
	}
	if len(data) < 8 {
		return fmt.Errorf("mp4: co64 too short")
	}
	entryCount := binary.BigEndian.Uint32(data[4:8])
	track.chunkOffsets = make([]int64, entryCount)
	for i := uint32(0); i < entryCount; i++ {
		off := 8 + int(i)*8
		if off+8 > len(data) {
			break
		}
		track.chunkOffsets[i] = int64(binary.BigEndian.Uint64(data[off : off+8]))
	}
	return nil
}

func (f *File) parseStsc(track *Track, h BoxHeader) error {
	data, err := f.readBoxData(h)
	if err != nil {
		return err
	}
	if len(data) < 8 {
		return fmt.Errorf("mp4: stsc too short")
	}
	entryCount := binary.BigEndian.Uint32(data[4:8])
	track.sampleToChunk = make([]sampleToChunkEntry, entryCount)
	for i := uint32(0); i < entryCount; i++ {
		off := 8 + int(i)*12
		if off+12 > len(data) {
			break
		}
		track.sampleToChunk[i] = sampleToChunkEntry{
			FirstChunk:             binary.BigEndian.Uint32(data[off : off+4]),
			SamplesPerChunk:        binary.BigEndian.Uint32(data[off+4 : off+8]),
			SampleDescriptionIndex: binary.BigEndian.Uint32(data[off+8 : off+12]),
		}
	}
	return nil
}

func (f *File) parseStss(track *Track, h BoxHeader) error {
	data, err := f.readBoxData(h)
	if err != nil {
		return err
	}
	if len(data) < 8 {
		return fmt.Errorf("mp4: stss too short")
	}
	entryCount := binary.BigEndian.Uint32(data[4:8])
	track.syncSamples = make(map[uint32]bool, entryCount)
	for i := uint32(0); i < entryCount; i++ {
		off := 8 + int(i)*4
		if off+4 > len(data) {
			break
		}
		sn := binary.BigEndian.Uint32(data[off : off+4])
		track.syncSamples[sn] = true
	}
	return nil
}

func (f *File) parseCtts(track *Track, h BoxHeader) error {
	data, err := f.readBoxData(h)
	if err != nil {
		return err
	}
	if len(data) < 8 {
		return fmt.Errorf("mp4: ctts too short")
	}
	version := data[0]
	entryCount := binary.BigEndian.Uint32(data[4:8])
	track.cttsEntries = make([]cttsEntry, entryCount)
	for i := uint32(0); i < entryCount; i++ {
		off := 8 + int(i)*8
		if off+8 > len(data) {
			break
		}
		track.cttsEntries[i].SampleCount = binary.BigEndian.Uint32(data[off : off+4])
		if version == 0 {
			track.cttsEntries[i].SampleOffset = int32(binary.BigEndian.Uint32(data[off+4 : off+8]))
		} else {
			track.cttsEntries[i].SampleOffset = int32(binary.BigEndian.Uint32(data[off+4 : off+8]))
		}
	}
	return nil
}
