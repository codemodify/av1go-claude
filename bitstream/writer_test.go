package bitstream

import (
	"testing"
)

func TestWriteBit(t *testing.T) {
	w := NewWriter(16)
	w.WriteBit(1)
	w.WriteBit(0)
	w.WriteBit(1)
	w.WriteBit(1)
	w.WriteBit(0)
	w.WriteBit(0)
	w.WriteBit(1)
	w.WriteBit(0)
	// Should produce 0b10110010 = 0xB2
	got := w.Bytes()
	if len(got) != 1 || got[0] != 0xB2 {
		t.Fatalf("expected [0xB2], got %#v", got)
	}
	if w.BitPosition() != 8 {
		t.Fatalf("expected 8 bits, got %d", w.BitPosition())
	}
}

func TestWriteBits(t *testing.T) {
	w := NewWriter(16)
	// Write 5 bits: 0b10101 = 21
	w.WriteBits(21, 5)
	// Write 3 bits: 0b110 = 6
	w.WriteBits(6, 3)
	// Total: 0b10101110 = 0xAE
	got := w.Bytes()
	if len(got) != 1 || got[0] != 0xAE {
		t.Fatalf("expected [0xAE], got %#v", got)
	}
}

func TestWriteBitsCrossBytes(t *testing.T) {
	w := NewWriter(16)
	// Write 12 bits: 0b101010101010 = 0xAAA
	w.WriteBits(0xAAA, 12)
	got := w.Bytes()
	if len(got) != 2 {
		t.Fatalf("expected 2 bytes, got %d", len(got))
	}
	// First byte: 0b10101010 = 0xAA
	// Second byte: 0b10100000 = 0xA0 (4 bits written + 4 zero padding)
	if got[0] != 0xAA || got[1] != 0xA0 {
		t.Fatalf("expected [0xAA, 0xA0], got [0x%02X, 0x%02X]", got[0], got[1])
	}
}

func TestWriteBool(t *testing.T) {
	w := NewWriter(16)
	w.WriteBool(true)
	w.WriteBool(false)
	w.WriteBool(true)
	w.WriteByteAlignment()
	got := w.Bytes()
	// 0b10100000 = 0xA0
	if len(got) != 1 || got[0] != 0xA0 {
		t.Fatalf("expected [0xA0], got %#v", got)
	}
}

func TestWriteLeb128(t *testing.T) {
	tests := []struct {
		val      uint64
		expected []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{127, []byte{0x7F}},
		{128, []byte{0x80, 0x01}},
		{300, []byte{0xAC, 0x02}},       // 300 = 0b100101100 -> 0101100 | 0000010
		{16384, []byte{0x80, 0x80, 0x01}}, // 16384 = 2^14
	}
	for _, tt := range tests {
		w := NewWriter(16)
		w.WriteLeb128(tt.val)
		got := w.Bytes()
		if len(got) != len(tt.expected) {
			t.Errorf("leb128(%d): expected %d bytes, got %d", tt.val, len(tt.expected), len(got))
			continue
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("leb128(%d): byte %d: expected 0x%02X, got 0x%02X", tt.val, i, tt.expected[i], got[i])
			}
		}
	}
}

func TestWriteUvlc(t *testing.T) {
	tests := []struct {
		val         uint32
		expectedLen int // expected bit count
	}{
		{0, 1},  // just "1"
		{1, 3},  // "010"
		{2, 3},  // "011"
		{3, 5},  // "00100"
		{6, 5},  // "00111"
		{7, 7},  // "0001000"
	}
	for _, tt := range tests {
		w := NewWriter(16)
		w.WriteUvlc(tt.val)
		if w.BitPosition() != tt.expectedLen {
			t.Errorf("uvlc(%d): expected %d bits, got %d", tt.val, tt.expectedLen, w.BitPosition())
		}
	}
}

func TestUvlcRoundTrip(t *testing.T) {
	// Verify uvlc encoding can be decoded back.
	// uvlc decoding: read leading zeros until a 1, then read that many more bits.
	for _, val := range []uint32{0, 1, 2, 3, 7, 15, 31, 100, 1000} {
		w := NewWriter(16)
		w.WriteUvlc(val)
		data := w.Bytes()
		decoded, err := decodeUvlc(data, w.BitPosition())
		if err != nil {
			t.Errorf("uvlc roundtrip(%d): decode error: %v", val, err)
			continue
		}
		if decoded != val {
			t.Errorf("uvlc roundtrip: wrote %d, decoded %d", val, decoded)
		}
	}
}

// decodeUvlc is a test helper that decodes uvlc from a bit buffer.
func decodeUvlc(data []byte, totalBits int) (uint32, error) {
	pos := 0
	readBit := func() (uint8, bool) {
		if pos >= totalBits {
			return 0, false
		}
		byteIdx := pos / 8
		bitIdx := 7 - (pos % 8)
		pos++
		return (data[byteIdx] >> uint(bitIdx)) & 1, true
	}

	leadingZeros := 0
	for {
		bit, ok := readBit()
		if !ok {
			return 0, nil
		}
		if bit == 1 {
			break
		}
		leadingZeros++
	}

	if leadingZeros == 0 {
		return 0, nil
	}

	var remainder uint32
	for i := leadingZeros - 1; i >= 0; i-- {
		bit, ok := readBit()
		if !ok {
			break
		}
		remainder |= uint32(bit) << uint(i)
	}
	return (1 << uint(leadingZeros)) + remainder - 1, nil
}

func TestWriteNs(t *testing.T) {
	// ns(n) with n=5:
	// w = 3 (bits.Len(5) = 3), m = 8 - 5 = 3
	// val 0: write 2 bits of 0 -> "00"
	// val 1: write 2 bits of 1 -> "01"
	// val 2: write 2 bits of 2 -> "10"
	// val 3: write 3 bits of 3+3=6 -> "110"
	// val 4: write 3 bits of 4+3=7 -> "111"
	tests := []struct {
		val uint32
		n   uint32
		len int
	}{
		{0, 5, 2},
		{1, 5, 2},
		{2, 5, 2},
		{3, 5, 3},
		{4, 5, 3},
		{0, 1, 0}, // n=1, zero bits
	}
	for _, tt := range tests {
		w := NewWriter(16)
		err := w.WriteNs(tt.val, tt.n)
		if err != nil {
			t.Errorf("ns(%d, %d): unexpected error: %v", tt.val, tt.n, err)
			continue
		}
		if w.BitPosition() != tt.len {
			t.Errorf("ns(%d, %d): expected %d bits, got %d", tt.val, tt.n, tt.len, w.BitPosition())
		}
	}
}

func TestWriteNsOutOfRange(t *testing.T) {
	w := NewWriter(16)
	err := w.WriteNs(5, 5) // val >= n
	if err == nil {
		t.Fatal("expected error for out-of-range ns value")
	}
}

func TestWriteSu(t *testing.T) {
	tests := []struct {
		val int32
		n   int
	}{
		{0, 4},
		{7, 4},
		{-8, 4},
		{-1, 4},
		{1, 8},
		{-128, 8},
	}
	for _, tt := range tests {
		w := NewWriter(16)
		err := w.WriteSu(tt.val, tt.n)
		if err != nil {
			t.Errorf("su(%d, %d): unexpected error: %v", tt.val, tt.n, err)
			continue
		}
		if w.BitPosition() != tt.n {
			t.Errorf("su(%d, %d): expected %d bits, got %d", tt.val, tt.n, tt.n, w.BitPosition())
		}
	}
}

func TestWriteSuOutOfRange(t *testing.T) {
	w := NewWriter(16)
	err := w.WriteSu(8, 4) // max for 4 bits is 7
	if err == nil {
		t.Fatal("expected error for out-of-range su value")
	}
}

func TestWriteByteAlignment(t *testing.T) {
	w := NewWriter(16)
	w.WriteBits(0b101, 3)
	w.WriteByteAlignment()
	got := w.Bytes()
	// 0b10100000 = 0xA0
	if len(got) != 1 || got[0] != 0xA0 {
		t.Fatalf("expected [0xA0], got %#v", got)
	}
	if w.BitPosition() != 8 {
		t.Fatalf("expected 8 bits after alignment, got %d", w.BitPosition())
	}
}

func TestWriteByteAlignmentAlreadyAligned(t *testing.T) {
	w := NewWriter(16)
	w.WriteBits(0xFF, 8)
	w.WriteByteAlignment() // Should be a no-op.
	if w.BitPosition() != 8 {
		t.Fatalf("expected 8 bits, got %d", w.BitPosition())
	}
}

func TestWriteTrailingBits(t *testing.T) {
	w := NewWriter(16)
	w.WriteBits(0b101, 3)
	w.WriteTrailingBits()
	got := w.Bytes()
	// 3 bits data + 1 trailing bit + 4 padding zeros = 0b10110000 = 0xB0
	if len(got) != 1 || got[0] != 0xB0 {
		t.Fatalf("expected [0xB0], got %#v", got)
	}
}

func TestWriteBytes(t *testing.T) {
	w := NewWriter(16)
	w.WriteBits(0xFF, 8) // byte-align first
	err := w.WriteBytes([]byte{0x01, 0x02, 0x03})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := w.Bytes()
	expected := []byte{0xFF, 0x01, 0x02, 0x03}
	if len(got) != len(expected) {
		t.Fatalf("expected %d bytes, got %d", len(expected), len(got))
	}
	for i := range got {
		if got[i] != expected[i] {
			t.Fatalf("byte %d: expected 0x%02X, got 0x%02X", i, expected[i], got[i])
		}
	}
}

func TestWriteBytesUnaligned(t *testing.T) {
	w := NewWriter(16)
	w.WriteBit(1) // Not byte-aligned.
	err := w.WriteBytes([]byte{0x01})
	if err == nil {
		t.Fatal("expected error for unaligned WriteBytes")
	}
}

func TestWriteUint16(t *testing.T) {
	w := NewWriter(16)
	w.WriteUint16(0x1234)
	got := w.Bytes()
	if len(got) != 2 || got[0] != 0x12 || got[1] != 0x34 {
		t.Fatalf("expected [0x12, 0x34], got %#v", got)
	}
}

func TestLeb128Size(t *testing.T) {
	tests := []struct {
		val  uint64
		size int
	}{
		{0, 1},
		{127, 1},
		{128, 2},
		{16383, 2},
		{16384, 3},
	}
	for _, tt := range tests {
		got := Leb128Size(tt.val)
		if got != tt.size {
			t.Errorf("Leb128Size(%d): expected %d, got %d", tt.val, tt.size, got)
		}
	}
}

func TestPutLeb128(t *testing.T) {
	buf := make([]byte, 10)
	n := PutLeb128(buf, 300)
	if n != 2 || buf[0] != 0xAC || buf[1] != 0x02 {
		t.Fatalf("PutLeb128(300): expected [0xAC, 0x02] (2 bytes), got %#v (%d bytes)", buf[:n], n)
	}
}

func TestReadLeb128(t *testing.T) {
	tests := []struct {
		data     []byte
		expected uint64
		size     int
	}{
		{[]byte{0x00}, 0, 1},
		{[]byte{0x01}, 1, 1},
		{[]byte{0x7F}, 127, 1},
		{[]byte{0x80, 0x01}, 128, 2},
		{[]byte{0xAC, 0x02}, 300, 2},
	}
	for _, tt := range tests {
		val, n, err := ReadLeb128(tt.data)
		if err != nil {
			t.Errorf("ReadLeb128(%#v): unexpected error: %v", tt.data, err)
			continue
		}
		if val != tt.expected || n != tt.size {
			t.Errorf("ReadLeb128(%#v): expected (%d, %d), got (%d, %d)", tt.data, tt.expected, tt.size, val, n)
		}
	}
}

func TestLeb128RoundTrip(t *testing.T) {
	for _, val := range []uint64{0, 1, 42, 127, 128, 255, 300, 16383, 16384, 1 << 20, 1 << 40} {
		buf := make([]byte, 10)
		n := PutLeb128(buf, val)
		decoded, m, err := ReadLeb128(buf[:n])
		if err != nil {
			t.Errorf("leb128 roundtrip(%d): decode error: %v", val, err)
			continue
		}
		if decoded != val || m != n {
			t.Errorf("leb128 roundtrip(%d): expected (%d, %d), got (%d, %d)", val, val, n, decoded, m)
		}
	}
}

func TestReset(t *testing.T) {
	w := NewWriter(16)
	w.WriteBits(0xFF, 8)
	w.Reset()
	if w.BitPosition() != 0 {
		t.Fatalf("expected 0 bits after reset, got %d", w.BitPosition())
	}
	if len(w.Bytes()) != 0 {
		t.Fatalf("expected 0 bytes after reset, got %d", len(w.Bytes()))
	}
	// Writer should be usable after reset.
	w.WriteBits(0xAB, 8)
	got := w.Bytes()
	if len(got) != 1 || got[0] != 0xAB {
		t.Fatalf("after reset, expected [0xAB], got %#v", got)
	}
}
