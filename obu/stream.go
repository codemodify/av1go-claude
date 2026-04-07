package obu

import (
	"fmt"
	"io"
)

// StreamReader reads a low-overhead OBU bitstream (AV1 spec Section 5.2).
// In this format, each OBU has obu_has_size_field=1, and OBUs are simply
// concatenated one after another. This is the format used inside MP4/WebM
// samples and IVF frame payloads.
//
// This differs from Annex B, where OBUs have has_size=0 and are framed
// by external leb128 length fields.
type StreamReader struct {
	data []byte
	pos  int
}

// NewStreamReader creates a reader over a low-overhead OBU stream.
// The data should contain concatenated OBUs with obu_has_size_field=1.
func NewStreamReader(data []byte) *StreamReader {
	return &StreamReader{data: data}
}

// ReadAll reads all OBUs from the stream. Each OBU must have
// obu_has_size_field=1. Returns the parsed units or an error.
func (sr *StreamReader) ReadAll() ([]Unit, error) {
	return ParseUnits(sr.data)
}

// Next reads the next OBU from the stream. Returns io.EOF when no more
// OBUs are available.
func (sr *StreamReader) Next() (Unit, error) {
	if sr.pos >= len(sr.data) {
		return Unit{}, io.EOF
	}
	u, n, err := ParseUnit(sr.data[sr.pos:])
	if err != nil {
		return Unit{}, fmt.Errorf("obu stream: at offset %d: %w", sr.pos, err)
	}
	if n == 0 {
		return Unit{}, io.EOF
	}
	sr.pos += n
	return u, nil
}

// Reset resets the reader to the beginning of the stream.
func (sr *StreamReader) Reset() {
	sr.pos = 0
}

// Position returns the current byte offset in the stream.
func (sr *StreamReader) Position() int {
	return sr.pos
}

// Remaining returns the number of bytes remaining.
func (sr *StreamReader) Remaining() int {
	return len(sr.data) - sr.pos
}

// ReadOBUStream reads all OBUs from a low-overhead OBU stream byte slice.
// This is a convenience function equivalent to NewStreamReader(data).ReadAll().
func ReadOBUStream(data []byte) ([]Unit, error) {
	return ParseUnits(data)
}
