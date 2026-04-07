// Package ivf provides a demuxer for the IVF (Indeo Video Format) container,
// a simple container format commonly used for AV1, VP8, and VP9 test vectors.
//
// IVF file structure:
//   - 32-byte file header: signature "DKIF", version, header length, codec fourcc,
//     width, height, timebase numerator/denominator, frame count, unused.
//   - Sequence of frame packets, each with a 12-byte header: frame size (4 bytes LE),
//     timestamp (8 bytes LE), followed by frame_size bytes of coded data.
//
// See https://wiki.multimedia.cx/index.php/IVF for the format specification.
package ivf

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// FileHeader represents the 32-byte IVF file header.
type FileHeader struct {
	Signature       [4]byte // "DKIF"
	Version         uint16  // should be 0
	HeaderLength    uint16  // length of this header (32)
	Codec           [4]byte // fourcc: "AV01", "VP80", "VP90", etc.
	Width           uint16
	Height          uint16
	TimebaseNum     uint32 // timebase numerator (e.g., 1)
	TimebaseDen     uint32 // timebase denominator (e.g., 30 for 30fps)
	FrameCount      uint32 // number of frames (may be 0 if unknown)
	Unused          uint32
}

// CodecString returns the codec fourcc as a string.
func (h *FileHeader) CodecString() string {
	return string(h.Codec[:])
}

// IsAV1 returns true if the codec fourcc indicates AV1.
func (h *FileHeader) IsAV1() bool {
	return h.Codec == [4]byte{'A', 'V', '0', '1'}
}

// Frame represents a single IVF frame packet.
type Frame struct {
	Index     int
	Size      uint32
	Timestamp uint64
	Data      []byte // raw coded data (OBU stream for AV1)
}

// Demuxer reads IVF files and provides frame-by-frame access.
type Demuxer struct {
	Header FileHeader
	r      io.ReaderAt
	size   int64

	// Cached frame offsets for random access.
	frameOffsets []int64
	frameSizes   []uint32
	frameTS      []uint64
	indexed      bool
}

const (
	fileHeaderSize  = 32
	frameHeaderSize = 12
)

// Open opens an IVF file and parses its file header.
func Open(path string) (*Demuxer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	d := &Demuxer{r: f, size: info.Size()}
	if err := d.parseFileHeader(); err != nil {
		f.Close()
		return nil, err
	}
	return d, nil
}

// OpenData creates a Demuxer from an in-memory byte slice.
func OpenData(data []byte) (*Demuxer, error) {
	d := &Demuxer{
		r:    &bytesReaderAt{data: data},
		size: int64(len(data)),
	}
	if err := d.parseFileHeader(); err != nil {
		return nil, err
	}
	return d, nil
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

func (d *Demuxer) parseFileHeader() error {
	if d.size < fileHeaderSize {
		return fmt.Errorf("ivf: file too short for header (%d bytes)", d.size)
	}
	var buf [fileHeaderSize]byte
	if _, err := d.r.ReadAt(buf[:], 0); err != nil {
		return fmt.Errorf("ivf: reading file header: %w", err)
	}

	copy(d.Header.Signature[:], buf[0:4])
	if d.Header.Signature != [4]byte{'D', 'K', 'I', 'F'} {
		return fmt.Errorf("ivf: invalid signature %q, expected \"DKIF\"", d.Header.Signature)
	}

	d.Header.Version = binary.LittleEndian.Uint16(buf[4:6])
	d.Header.HeaderLength = binary.LittleEndian.Uint16(buf[6:8])
	copy(d.Header.Codec[:], buf[8:12])
	d.Header.Width = binary.LittleEndian.Uint16(buf[12:14])
	d.Header.Height = binary.LittleEndian.Uint16(buf[14:16])
	d.Header.TimebaseNum = binary.LittleEndian.Uint32(buf[16:20])
	d.Header.TimebaseDen = binary.LittleEndian.Uint32(buf[20:24])
	d.Header.FrameCount = binary.LittleEndian.Uint32(buf[24:28])
	d.Header.Unused = binary.LittleEndian.Uint32(buf[28:32])

	return nil
}

// buildIndex scans all frame headers to build a frame offset index.
func (d *Demuxer) buildIndex() error {
	if d.indexed {
		return nil
	}
	pos := int64(fileHeaderSize)
	for pos+frameHeaderSize <= d.size {
		var buf [frameHeaderSize]byte
		if _, err := d.r.ReadAt(buf[:], pos); err != nil {
			break
		}
		frameSize := binary.LittleEndian.Uint32(buf[0:4])
		timestamp := binary.LittleEndian.Uint64(buf[4:12])

		dataStart := pos + frameHeaderSize
		if dataStart+int64(frameSize) > d.size {
			break
		}

		d.frameOffsets = append(d.frameOffsets, dataStart)
		d.frameSizes = append(d.frameSizes, frameSize)
		d.frameTS = append(d.frameTS, timestamp)

		pos = dataStart + int64(frameSize)
	}
	d.indexed = true
	return nil
}

// FrameCount returns the number of frames in the file.
// This scans the file on first call to get an accurate count.
func (d *Demuxer) FrameCount() (int, error) {
	if err := d.buildIndex(); err != nil {
		return 0, err
	}
	return len(d.frameOffsets), nil
}

// ReadFrame reads the frame at the given index (0-based).
func (d *Demuxer) ReadFrame(index int) (*Frame, error) {
	if err := d.buildIndex(); err != nil {
		return nil, err
	}
	if index < 0 || index >= len(d.frameOffsets) {
		return nil, fmt.Errorf("ivf: frame index %d out of range [0, %d)", index, len(d.frameOffsets))
	}
	data := make([]byte, d.frameSizes[index])
	if _, err := d.r.ReadAt(data, d.frameOffsets[index]); err != nil {
		return nil, fmt.Errorf("ivf: reading frame %d data: %w", index, err)
	}
	return &Frame{
		Index:     index,
		Size:      d.frameSizes[index],
		Timestamp: d.frameTS[index],
		Data:      data,
	}, nil
}

// ReadAllFrames reads all frames from the IVF file.
func (d *Demuxer) ReadAllFrames() ([]*Frame, error) {
	count, err := d.FrameCount()
	if err != nil {
		return nil, err
	}
	frames := make([]*Frame, count)
	for i := 0; i < count; i++ {
		frames[i], err = d.ReadFrame(i)
		if err != nil {
			return nil, err
		}
	}
	return frames, nil
}

// Frames returns an iterator-style reader that yields frames sequentially.
// Call Next() to advance and Frame() to get the current frame.
type FrameReader struct {
	d       *Demuxer
	pos     int64
	current *Frame
	index   int
	err     error
}

// NewFrameReader creates a FrameReader that reads frames sequentially.
func (d *Demuxer) NewFrameReader() *FrameReader {
	return &FrameReader{
		d:   d,
		pos: fileHeaderSize,
	}
}

// Next advances to the next frame. Returns false when no more frames are
// available or an error occurred. Check Err() after the loop.
func (fr *FrameReader) Next() bool {
	if fr.pos+frameHeaderSize > fr.d.size {
		return false
	}
	var buf [frameHeaderSize]byte
	if _, err := fr.d.r.ReadAt(buf[:], fr.pos); err != nil {
		fr.err = err
		return false
	}
	frameSize := binary.LittleEndian.Uint32(buf[0:4])
	timestamp := binary.LittleEndian.Uint64(buf[4:12])

	dataStart := fr.pos + frameHeaderSize
	if dataStart+int64(frameSize) > fr.d.size {
		fr.err = fmt.Errorf("ivf: frame %d extends beyond file", fr.index)
		return false
	}

	data := make([]byte, frameSize)
	if _, err := fr.d.r.ReadAt(data, dataStart); err != nil {
		fr.err = err
		return false
	}

	fr.current = &Frame{
		Index:     fr.index,
		Size:      frameSize,
		Timestamp: timestamp,
		Data:      data,
	}
	fr.index++
	fr.pos = dataStart + int64(frameSize)
	return true
}

// Frame returns the current frame. Only valid after a successful Next() call.
func (fr *FrameReader) Frame() *Frame {
	return fr.current
}

// Err returns any error encountered during iteration.
func (fr *FrameReader) Err() error {
	return fr.err
}
