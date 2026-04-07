package bitstream

import (
	"errors"
	"fmt"
	"io"
)

// ErrOverflow is returned when a coded integer exceeds the representable range.
var ErrOverflow = errors.New("bitstream: integer overflow")

// Reader reads bits from an underlying byte slice. Bits are read from
// MSB to LSB within each byte, matching the AV1 spec convention.
type Reader struct {
	buf []byte
	pos int // bit position within buf
}

// NewReader creates a Reader over the given byte slice. The slice is not copied.
func NewReader(data []byte) *Reader {
	return &Reader{buf: data}
}

// BitsRead returns the number of bits consumed so far.
func (r *Reader) BitsRead() int {
	return r.pos
}

// BitsRemaining returns the number of bits remaining.
func (r *Reader) BitsRemaining() int {
	return len(r.buf)*8 - r.pos
}

// ByteAlign advances the bit position to the next byte boundary.
// AV1 spec: byte_alignment() (Section 5.3.5)
func (r *Reader) ByteAlign() {
	if rem := r.pos % 8; rem != 0 {
		r.pos += 8 - rem
	}
}

// ReadBit reads a single bit and returns it as a uint8 (0 or 1).
func (r *Reader) ReadBit() (uint8, error) {
	if r.pos >= len(r.buf)*8 {
		return 0, io.ErrUnexpectedEOF
	}
	byteIdx := r.pos / 8
	bitIdx := 7 - (r.pos % 8) // MSB first
	bit := (r.buf[byteIdx] >> bitIdx) & 1
	r.pos++
	return bit, nil
}

// ReadBits reads n bits (0 <= n <= 64) and returns them as a uint64.
// AV1 spec: f(n) — unsigned n-bit number (Section 4.10.2)
func (r *Reader) ReadBits(n int) (uint64, error) {
	if n < 0 || n > 64 {
		return 0, fmt.Errorf("bitstream: invalid bit count %d", n)
	}
	if n == 0 {
		return 0, nil
	}
	if r.pos+n > len(r.buf)*8 {
		return 0, io.ErrUnexpectedEOF
	}
	var val uint64
	for i := 0; i < n; i++ {
		byteIdx := r.pos / 8
		bitIdx := 7 - (r.pos % 8)
		bit := uint64((r.buf[byteIdx] >> bitIdx) & 1)
		val = (val << 1) | bit
		r.pos++
	}
	return val, nil
}

// ReadFlag reads a single bit as a boolean.
func (r *Reader) ReadFlag() (bool, error) {
	b, err := r.ReadBit()
	return b == 1, err
}

// ReadUint8 reads n bits and returns as uint8.
func (r *Reader) ReadUint8(n int) (uint8, error) {
	v, err := r.ReadBits(n)
	return uint8(v), err
}

// ReadUint16 reads n bits and returns as uint16.
func (r *Reader) ReadUint16(n int) (uint16, error) {
	v, err := r.ReadBits(n)
	return uint16(v), err
}

// ReadUint32 reads n bits and returns as uint32.
func (r *Reader) ReadUint32(n int) (uint32, error) {
	v, err := r.ReadBits(n)
	return uint32(v), err
}

// ReadLeb128 reads a LEB128-coded unsigned integer (up to 8 bytes)
// from the bit reader. The reader is byte-aligned before reading.
// Returns the decoded value and the number of bytes consumed.
// AV1 spec: leb128() (Section 4.10.5)
func (r *Reader) ReadLeb128() (uint64, int, error) {
	r.ByteAlign()
	var value uint64
	var bytesRead int
	for i := 0; i < 8; i++ {
		b, err := r.ReadBits(8)
		if err != nil {
			return 0, bytesRead, err
		}
		bytesRead++
		value |= (b & 0x7F) << (i * 7)
		if (b & 0x80) == 0 {
			break
		}
	}
	return value, bytesRead, nil
}

// ReadUvlc reads a universal variable-length coded integer.
// AV1 spec: uvlc() (Section 4.10.3)
func (r *Reader) ReadUvlc() (uint32, error) {
	leadingZeros := 0
	for {
		done, err := r.ReadFlag()
		if err != nil {
			return 0, err
		}
		if done {
			break
		}
		leadingZeros++
		if leadingZeros >= 32 {
			return 0, ErrOverflow
		}
	}
	if leadingZeros >= 32 {
		return (1 << 32) - 1, nil
	}
	value, err := r.ReadBits(leadingZeros)
	if err != nil {
		return 0, err
	}
	return uint32(value + (1 << leadingZeros) - 1), nil
}

// ReadSu reads a signed integer using su(n) coding.
// AV1 spec: su(n) (Section 4.10.6)
func (r *Reader) ReadSu(n int) (int32, error) {
	v, err := r.ReadBits(n)
	if err != nil {
		return 0, err
	}
	signMask := uint64(1) << (n - 1)
	if (v & signMask) != 0 {
		return int32(v) - int32(1<<n), nil
	}
	return int32(v), nil
}

// ReadNs reads a non-symmetric unsigned integer coded value in the range [0, n).
// AV1 spec: ns(n) (Section 4.10.7)
func (r *Reader) ReadNs(n int) (uint32, error) {
	if n <= 0 {
		return 0, fmt.Errorf("bitstream: ns() n must be > 0, got %d", n)
	}
	if n == 1 {
		return 0, nil
	}
	w := floorLog2(n-1) + 1
	m := (1 << w) - n
	v, err := r.ReadBits(w - 1)
	if err != nil {
		return 0, err
	}
	if v < uint64(m) {
		return uint32(v), nil
	}
	extraBit, err := r.ReadBit()
	if err != nil {
		return 0, err
	}
	return uint32((v << 1) - uint64(m) + uint64(extraBit)), nil
}

// ReadBytes reads n whole bytes, returning a new slice.
// The reader must be byte-aligned.
func (r *Reader) ReadBytes(n int) ([]byte, error) {
	if r.pos%8 != 0 {
		return nil, fmt.Errorf("bitstream: ReadBytes called on non-byte-aligned reader")
	}
	bytePos := r.pos / 8
	if bytePos+n > len(r.buf) {
		return nil, io.ErrUnexpectedEOF
	}
	out := make([]byte, n)
	copy(out, r.buf[bytePos:bytePos+n])
	r.pos += n * 8
	return out, nil
}

// SubReader returns a new Reader over the next n bytes, advancing the
// current reader past them. The current reader must be byte-aligned.
func (r *Reader) SubReader(n int) (*Reader, error) {
	data, err := r.ReadBytes(n)
	if err != nil {
		return nil, err
	}
	return NewReader(data), nil
}

// HasMoreData returns true if there are unread bits remaining.
func (r *Reader) HasMoreData() bool {
	return r.pos < len(r.buf)*8
}

// floorLog2 returns floor(log2(x)) for x > 0.
func floorLog2(x int) int {
	s := 0
	for x > 1 {
		x >>= 1
		s++
	}
	return s
}
