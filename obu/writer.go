package obu

import (
	"av1go/bitstream"
	"errors"
	"fmt"
)

// Writer serializes OBUs (Open Bitstream Units) to a byte stream.
// Each OBU consists of a header, an optional size field, and a payload.
//
// AV1 spec Section 5.3: obu_header() and open_bitstream_unit()
type Writer struct {
	buf []byte
}

// NewWriter creates a new OBU writer.
func NewWriter() *Writer {
	return &Writer{
		buf: make([]byte, 0, 4096),
	}
}

// WriteOBU writes a complete OBU: header + leb128 size + payload.
// This is the primary method for writing OBUs with size fields, which is
// the format used within Temporal Units and inside containers (MP4, WebM).
//
// AV1 spec Section 5.3.1-5.3.4.
func (w *Writer) WriteOBU(header Header, payload []byte) error {
	if err := validateHeader(header); err != nil {
		return err
	}

	// Write header byte(s).
	headerBytes := encodeHeader(header)
	w.buf = append(w.buf, headerBytes...)

	// Write payload size as leb128.
	sizeBuf := make([]byte, 10)
	n := bitstream.PutLeb128(sizeBuf, uint64(len(payload)))
	w.buf = append(w.buf, sizeBuf[:n]...)

	// Write payload.
	w.buf = append(w.buf, payload...)

	return nil
}

// WriteOBUNoSize writes an OBU without a size field (obu_has_size_flag = 0).
// This format is used in Annex B (length-delimited) bitstreams.
func (w *Writer) WriteOBUNoSize(header Header, payload []byte) error {
	header.HasSize = false
	if err := validateHeader(header); err != nil {
		return err
	}

	headerBytes := encodeHeader(header)
	w.buf = append(w.buf, headerBytes...)
	w.buf = append(w.buf, payload...)

	return nil
}

// WriteTemporalDelimiter writes a Temporal Delimiter OBU.
// The TD OBU has an empty payload.
// AV1 spec Section 5.6.
func (w *Writer) WriteTemporalDelimiter() error {
	return w.WriteOBU(Header{
		Type:    TypeTemporalDelimiter,
		HasSize: true,
	}, nil)
}

// Bytes returns the accumulated bytes and resets the internal buffer.
func (w *Writer) Bytes() []byte {
	out := make([]byte, len(w.buf))
	copy(out, w.buf)
	w.buf = w.buf[:0]
	return out
}

// Len returns the current length of accumulated bytes.
func (w *Writer) Len() int {
	return len(w.buf)
}

// Reset clears the internal buffer.
func (w *Writer) Reset() {
	w.buf = w.buf[:0]
}

// encodeHeader serializes an OBU header to 1 or 2 bytes.
// AV1 spec Section 5.3.2: obu_header()
//
// Byte 0 layout (MSB first):
//   obu_forbidden_bit (1 bit, always 0)
//   obu_type (4 bits)
//   obu_extension_flag (1 bit)
//   obu_has_size_flag (1 bit)
//   obu_reserved_1bit (1 bit, always 0)
//
// If obu_extension_flag is set, Byte 1:
//   temporal_id (3 bits)
//   spatial_id (2 bits)
//   extension_header_reserved_3bits (3 bits, always 0)
func encodeHeader(h Header) []byte {
	b0 := uint8(h.Type&0x0F) << 3
	if h.HasExtension {
		b0 |= 0x04
	}
	if h.HasSize {
		b0 |= 0x02
	}
	// obu_forbidden_bit = 0 (bit 7), obu_reserved_1bit = 0 (bit 0)

	if !h.HasExtension {
		return []byte{b0}
	}

	ext := h.Extension
	if ext == nil {
		ext = &ExtensionHeader{}
	}
	b1 := (ext.TemporalID & 0x07) << 5
	b1 |= (ext.SpatialID & 0x03) << 3
	// reserved 3 bits = 0

	return []byte{b0, b1}
}

// validateHeader checks that the header fields are valid.
func validateHeader(h Header) error {
	if h.Type == 0 || (h.Type > TypeTileList && h.Type != TypePadding) {
		if h.Type != 0 {
			// Type 0 is reserved; types 9-14 are reserved.
			// We allow known types and padding.
		}
	}
	switch h.Type {
	case TypeSequenceHeader, TypeTemporalDelimiter, TypeFrameHeader,
		TypeTileGroup, TypeMetadata, TypeFrame, TypeRedundantFrameHeader,
		TypeTileList, TypePadding:
		// Valid.
	default:
		return fmt.Errorf("obu: invalid OBU type %d", h.Type)
	}
	if h.HasExtension && h.Extension == nil {
		return errors.New("obu: HasExtension is true but Extension is nil")
	}
	if h.HasExtension && h.Extension != nil {
		if h.Extension.TemporalID > 7 {
			return fmt.Errorf("obu: temporal_id %d exceeds 3-bit maximum", h.Extension.TemporalID)
		}
		if h.Extension.SpatialID > 3 {
			return fmt.Errorf("obu: spatial_id %d exceeds 2-bit maximum", h.Extension.SpatialID)
		}
	}
	return nil
}

// EncodeHeader is an exported wrapper for encoding an OBU header to bytes.
// Returns the encoded header bytes (1 or 2 bytes).
func EncodeHeader(h Header) ([]byte, error) {
	if err := validateHeader(h); err != nil {
		return nil, err
	}
	return encodeHeader(h), nil
}

// MakeOBU creates a complete OBU byte slice from a header and payload.
// This is a convenience function that returns the OBU bytes directly
// without using a Writer.
func MakeOBU(header Header, payload []byte) ([]byte, error) {
	header.HasSize = true
	if err := validateHeader(header); err != nil {
		return nil, err
	}

	headerBytes := encodeHeader(header)
	sizeBuf := make([]byte, 10)
	n := bitstream.PutLeb128(sizeBuf, uint64(len(payload)))

	result := make([]byte, 0, len(headerBytes)+n+len(payload))
	result = append(result, headerBytes...)
	result = append(result, sizeBuf[:n]...)
	result = append(result, payload...)
	return result, nil
}
