// Package bitstream provides bit-level I/O primitives for AV1 bitstream encoding and decoding.
//
// The Writer type writes individual bits, multi-bit integers, and AV1-specific
// coded integer formats (leb128, uvlc, ns, su) as defined in the AV1 specification
// Section 4 ("Bitstream Syntax Description").
package bitstream

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
)

// Writer writes individual bits and AV1-coded integers to an underlying byte buffer.
// Bits are written MSB-first within each byte (big-endian bit order), which matches
// the AV1 bitstream convention where f(1) reads the most significant unread bit.
type Writer struct {
	buf      []byte
	bitPos   int // number of bits written total
	bytePos  int // current byte index in buf
	bitsLeft int // bits remaining in current byte (8 when starting a new byte)
}

// NewWriter creates a new Writer with an initial buffer capacity.
func NewWriter(initialCap int) *Writer {
	if initialCap <= 0 {
		initialCap = 256
	}
	return &Writer{
		buf:      make([]byte, 0, initialCap),
		bitsLeft: 0,
	}
}

// WriteBit writes a single bit (0 or 1).
// AV1 spec: f(1)
func (w *Writer) WriteBit(bit uint8) {
	if bit > 1 {
		bit = 1
	}
	if w.bitsLeft == 0 {
		// Start a new byte.
		w.buf = append(w.buf, 0)
		w.bytePos = len(w.buf) - 1
		w.bitsLeft = 8
	}
	w.bitsLeft--
	if bit == 1 {
		w.buf[w.bytePos] |= 1 << uint(w.bitsLeft)
	}
	w.bitPos++
}

// WriteBits writes the bottom n bits of val, MSB first.
// AV1 spec: f(n)
func (w *Writer) WriteBits(val uint64, n int) {
	if n <= 0 || n > 64 {
		return
	}
	for i := n - 1; i >= 0; i-- {
		w.WriteBit(uint8((val >> uint(i)) & 1))
	}
}

// WriteBool writes a single bit: 1 for true, 0 for false.
func (w *Writer) WriteBool(b bool) {
	if b {
		w.WriteBit(1)
	} else {
		w.WriteBit(0)
	}
}

// WriteLiteral writes n bits from val (unsigned). Alias for WriteBits for spec alignment.
// AV1 spec: literal(n)
func (w *Writer) WriteLiteral(val uint64, n int) {
	w.WriteBits(val, n)
}

// WriteLeb128 encodes val as a leb128-coded unsigned integer.
// AV1 spec Section 4.10.5: leb128()
// Each output byte carries 7 payload bits in the low 7 bits and a continuation
// flag in bit 7. The encoding is little-endian: least significant group first.
func (w *Writer) WriteLeb128(val uint64) {
	for {
		b := uint8(val & 0x7f)
		val >>= 7
		if val != 0 {
			b |= 0x80 // set continuation bit
		}
		w.WriteBits(uint64(b), 8)
		if val == 0 {
			break
		}
	}
}

// WriteUvlc encodes val as a universal variable-length code.
// AV1 spec Section 4.10.3: uvlc()
//
// Encoding: for value v, let leadingZeros = floor(log2(v+1)).
// Write leadingZeros zero bits, then a 1 bit, then the leadingZeros low bits
// of (v+1) minus the leading 1.
// Special: v=0 produces a single "1" bit with zero leading zeros.
func (w *Writer) WriteUvlc(val uint32) {
	if val == 0 {
		// v+1 = 1, leadingZeros = 0, just write the '1' bit.
		w.WriteBit(1)
		return
	}
	vPlus1 := uint64(val) + 1
	leadingZeros := bits.Len64(vPlus1) - 1 // floor(log2(v+1))

	// Write leadingZeros zero bits.
	for i := 0; i < leadingZeros; i++ {
		w.WriteBit(0)
	}
	// Write the 1 bit (the leading 1 of v+1).
	w.WriteBit(1)
	// Write the remaining leadingZeros bits of v+1 (excluding the leading 1).
	if leadingZeros > 0 {
		remainder := vPlus1 - (1 << uint(leadingZeros))
		w.WriteBits(remainder, leadingZeros)
	}
}

// WriteNs writes val using non-symmetric coding for range [0, n).
// AV1 spec Section 4.10.7: ns(n)
//
// For n values, let w = floor(log2(n)) + 1. Let m = (1 << w) - n.
// If val < m, write (w-1) bits of val.
// Otherwise, write w bits of (val + m).
func (w *Writer) WriteNs(val uint32, n uint32) error {
	if n <= 0 {
		return errors.New("bitstream: ns(n) requires n > 0")
	}
	if val >= n {
		return fmt.Errorf("bitstream: ns value %d out of range [0, %d)", val, n)
	}
	if n == 1 {
		// Only one possible value; zero bits needed.
		return nil
	}
	width := bits.Len32(n) // floor(log2(n)) + 1 for n > 0
	m := uint32((1 << uint(width)) - uint(n))
	if val < m {
		w.WriteBits(uint64(val), width-1)
	} else {
		w.WriteBits(uint64(val+m), width)
	}
	return nil
}

// WriteSu writes a signed integer val using n bits.
// AV1 spec Section 4.10.6: su(n)
//
// The value is encoded as an unsigned n-bit integer where negative values
// use the offset: if val >= 0, write val; if val < 0, write val + (1 << n).
func (w *Writer) WriteSu(val int32, n int) error {
	if n <= 0 || n > 32 {
		return fmt.Errorf("bitstream: su(n) requires 1 <= n <= 32, got %d", n)
	}
	maxPos := int32(1<<(n-1)) - 1
	minNeg := -int32(1 << (n - 1))
	if val > maxPos || val < minNeg {
		return fmt.Errorf("bitstream: su value %d out of range [%d, %d] for %d bits", val, minNeg, maxPos, n)
	}
	var uval uint64
	if val >= 0 {
		uval = uint64(val)
	} else {
		uval = uint64(val + (1 << uint(n)))
	}
	w.WriteBits(uval, n)
	return nil
}

// WriteByteAlignment pads with zero bits until the writer is byte-aligned.
// AV1 spec: byte_alignment()
func (w *Writer) WriteByteAlignment() {
	if w.bitsLeft > 0 && w.bitsLeft < 8 {
		for w.bitsLeft > 0 {
			w.WriteBit(0)
		}
	}
}

// WriteTrailingBits writes a 1 bit followed by zero-padding to byte alignment.
// AV1 spec: trailing_bits(nbBits) — used at the end of OBU payloads.
func (w *Writer) WriteTrailingBits() {
	w.WriteBit(1)
	w.WriteByteAlignment()
}

// WriteBytes writes raw bytes directly. The writer must be byte-aligned.
func (w *Writer) WriteBytes(data []byte) error {
	if w.bitsLeft != 0 {
		return errors.New("bitstream: WriteBytes requires byte-aligned position")
	}
	w.buf = append(w.buf, data...)
	w.bytePos = len(w.buf) - 1
	w.bitPos += len(data) * 8
	return nil
}

// WriteUint8 writes an 8-bit unsigned integer.
func (w *Writer) WriteUint8(val uint8) {
	w.WriteBits(uint64(val), 8)
}

// WriteUint16 writes a 16-bit unsigned integer in big-endian order.
func (w *Writer) WriteUint16(val uint16) {
	w.WriteBits(uint64(val), 16)
}

// WriteUint32 writes a 32-bit unsigned integer in big-endian order.
func (w *Writer) WriteUint32(val uint32) {
	w.WriteBits(uint64(val), 32)
}

// Bytes returns the written data as a byte slice. If the stream is not
// byte-aligned, the final byte is zero-padded in the low bits.
func (w *Writer) Bytes() []byte {
	return w.buf
}

// BitPosition returns the total number of bits written.
func (w *Writer) BitPosition() int {
	return w.bitPos
}

// ByteLength returns the number of full or partial bytes written.
func (w *Writer) ByteLength() int {
	return len(w.buf)
}

// Reset clears the writer for reuse.
func (w *Writer) Reset() {
	w.buf = w.buf[:0]
	w.bitPos = 0
	w.bytePos = 0
	w.bitsLeft = 0
}

// Leb128Size returns the number of bytes needed to encode val as leb128.
func Leb128Size(val uint64) int {
	if val == 0 {
		return 1
	}
	size := 0
	for val > 0 {
		val >>= 7
		size++
	}
	return size
}

// PutLeb128 encodes val as leb128 into buf and returns the number of bytes written.
// buf must be large enough (use Leb128Size to determine the required size).
func PutLeb128(buf []byte, val uint64) int {
	i := 0
	for {
		b := uint8(val & 0x7f)
		val >>= 7
		if val != 0 {
			b |= 0x80
		}
		buf[i] = b
		i++
		if val == 0 {
			break
		}
	}
	return i
}

// ReadLeb128 decodes a leb128-coded unsigned integer from buf, returning
// the value and the number of bytes consumed. This is provided as a utility
// complement to the Writer for use in tests and internal tools.
func ReadLeb128(buf []byte) (uint64, int, error) {
	var val uint64
	for i := 0; i < len(buf) && i < 8; i++ {
		b := buf[i]
		val |= uint64(b&0x7f) << uint(i*7)
		if b&0x80 == 0 {
			return val, i + 1, nil
		}
	}
	return 0, 0, errors.New("bitstream: leb128 not terminated within 8 bytes")
}

// AppendUint16BE appends a big-endian uint16 to a byte slice.
func AppendUint16BE(b []byte, v uint16) []byte {
	return binary.BigEndian.AppendUint16(b, v)
}

// AppendUint32BE appends a big-endian uint32 to a byte slice.
func AppendUint32BE(b []byte, v uint32) []byte {
	return binary.BigEndian.AppendUint32(b, v)
}
