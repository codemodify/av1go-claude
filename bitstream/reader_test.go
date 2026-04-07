package bitstream

import (
	"io"
	"testing"
)

func TestReadBit(t *testing.T) {
	// 0b10110010 = 0xB2
	r := NewReader([]byte{0xB2})
	expected := []uint8{1, 0, 1, 1, 0, 0, 1, 0}
	for i, want := range expected {
		got, err := r.ReadBit()
		if err != nil {
			t.Fatalf("ReadBit[%d]: %v", i, err)
		}
		if got != want {
			t.Errorf("ReadBit[%d] = %d, want %d", i, got, want)
		}
	}
	_, err := r.ReadBit()
	if err != io.ErrUnexpectedEOF {
		t.Errorf("expected ErrUnexpectedEOF after all bits, got %v", err)
	}
}

func TestReadBits(t *testing.T) {
	// 0xFF 0x00 = 1111_1111 0000_0000
	r := NewReader([]byte{0xFF, 0x00})

	v, err := r.ReadBits(4)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0xF {
		t.Errorf("got %d, want 15", v)
	}

	v, err = r.ReadBits(8)
	if err != nil {
		t.Fatal(err)
	}
	// bits: 1111_0000 -> 0xF0 = 240
	if v != 0xF0 {
		t.Errorf("got %d, want 240", v)
	}

	v, err = r.ReadBits(4)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0x0 {
		t.Errorf("got %d, want 0", v)
	}
}

func TestReadBitsZero(t *testing.T) {
	r := NewReader([]byte{0xAB})
	v, err := r.ReadBits(0)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0 {
		t.Errorf("ReadBits(0) = %d, want 0", v)
	}
	if r.BitsRead() != 0 {
		t.Errorf("BitsRead() = %d, want 0", r.BitsRead())
	}
}

func TestReadFlag(t *testing.T) {
	r := NewReader([]byte{0x80}) // 1000_0000
	f, err := r.ReadFlag()
	if err != nil {
		t.Fatal(err)
	}
	if !f {
		t.Error("expected true")
	}
	f, err = r.ReadFlag()
	if err != nil {
		t.Fatal(err)
	}
	if f {
		t.Error("expected false")
	}
}

func TestByteAlign(t *testing.T) {
	r := NewReader([]byte{0xAB, 0xCD})
	r.ReadBits(3)
	r.ByteAlign()
	if r.BitsRead() != 8 {
		t.Errorf("BitsRead after align = %d, want 8", r.BitsRead())
	}

	// Already aligned: should not advance
	r.ByteAlign()
	if r.BitsRead() != 8 {
		t.Errorf("BitsRead after double align = %d, want 8", r.BitsRead())
	}
}

func TestReaderReadLeb128(t *testing.T) {
	tests := []struct {
		name  string
		data  []byte
		want  uint64
		bytes int
	}{
		{"zero", []byte{0x00}, 0, 1},
		{"one", []byte{0x01}, 1, 1},
		{"127", []byte{0x7F}, 127, 1},
		{"128", []byte{0x80, 0x01}, 128, 2},
		{"300", []byte{0xAC, 0x02}, 300, 2},
		{"624485", []byte{0xE5, 0x8E, 0x26}, 624485, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReader(tc.data)
			got, n, err := r.ReadLeb128()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("value = %d, want %d", got, tc.want)
			}
			if n != tc.bytes {
				t.Errorf("bytes = %d, want %d", n, tc.bytes)
			}
		})
	}
}

func TestReadUvlc(t *testing.T) {
	tests := []struct {
		name string
		bits string
		want uint32
	}{
		// uvlc encoding: leading zeros, then 1, then value bits
		// 0 -> "1" (0 leading zeros, value = 0 + (1<<0) - 1 = 0)
		{"zero", "1", 0},
		// 1 -> "010" (1 leading zero, 1 bit value=0, result = 0 + (1<<1) - 1 = 1)
		{"one", "010", 1},
		// 2 -> "011" (1 leading zero, 1 bit value=1, result = 1 + (1<<1) - 1 = 2)
		{"two", "011", 2},
		// 3 -> "00100" (2 leading zeros, 2 bit value=00, result = 0 + (1<<2) - 1 = 3)
		{"three", "00100", 3},
		// 6 -> "00111" (2 leading zeros, 2 bit value=11, result = 3 + (1<<2) - 1 = 6)
		{"six", "00111", 6},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := bitsToBytes(tc.bits)
			r := NewReader(data)
			got, err := r.ReadUvlc()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("uvlc = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReadSu(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		n    int
		want int32
	}{
		// su(8): 0b00000011 = 3
		{"positive", []byte{0x03}, 8, 3},
		// su(8): 0b11111101 = 253, sign bit set -> 253 - 256 = -3
		{"negative", []byte{0xFD}, 8, -3},
		// su(4): 0b0010_xxxx = 2 (read from MSB, bits=0010)
		{"four_bit_pos", []byte{0x20}, 4, 2},
		// su(4): 0b1110_xxxx = 14, but read as 4-bit: 1110 = 14, sign -> 14 - 16 = -2
		{"four_bit_neg", []byte{0xE0}, 4, -2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReader(tc.data)
			got, err := r.ReadSu(tc.n)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("su(%d) = %d, want %d", tc.n, got, tc.want)
			}
		})
	}
}

func TestReadNs(t *testing.T) {
	tests := []struct {
		n    int
		bits string
		want uint32
	}{
		// ns(5): w=3, m=(1<<3)-5=3
		// values 0,1,2 coded in 2 bits: 00,01,10
		// values 3,4 coded in 3 bits: (v<<1)-m+extra = 110,111
		{5, "00", 0},
		{5, "01", 1},
		{5, "10", 2},
		{5, "110", 3},
		{5, "111", 4},
		// ns(1) always returns 0
	}
	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			data := bitsToBytes(tc.bits)
			r := NewReader(data)
			got, err := r.ReadNs(tc.n)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("ns(%d) with bits %q = %d, want %d", tc.n, tc.bits, got, tc.want)
			}
		})
	}

	// ns(1) should always return 0 without reading any bits
	r := NewReader([]byte{})
	got, err := r.ReadNs(1)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("ns(1) = %d, want 0", got)
	}
}

func TestReadBytes(t *testing.T) {
	r := NewReader([]byte{0x01, 0x02, 0x03, 0x04})
	b, err := r.ReadBytes(2)
	if err != nil {
		t.Fatal(err)
	}
	if b[0] != 0x01 || b[1] != 0x02 {
		t.Errorf("got %v, want [1 2]", b)
	}
	b, err = r.ReadBytes(2)
	if err != nil {
		t.Fatal(err)
	}
	if b[0] != 0x03 || b[1] != 0x04 {
		t.Errorf("got %v, want [3 4]", b)
	}
}

func TestSubReader(t *testing.T) {
	r := NewReader([]byte{0xAA, 0xBB, 0xCC, 0xDD})
	sub, err := r.SubReader(2)
	if err != nil {
		t.Fatal(err)
	}
	if sub.BitsRemaining() != 16 {
		t.Errorf("sub has %d bits, want 16", sub.BitsRemaining())
	}
	v, _ := sub.ReadBits(8)
	if v != 0xAA {
		t.Errorf("sub byte 0 = %#x, want 0xAA", v)
	}
	if r.BitsRead() != 16 {
		t.Errorf("parent BitsRead = %d, want 16", r.BitsRead())
	}
}

func TestBitsRemaining(t *testing.T) {
	r := NewReader([]byte{0x00, 0x00})
	if r.BitsRemaining() != 16 {
		t.Errorf("got %d, want 16", r.BitsRemaining())
	}
	r.ReadBits(5)
	if r.BitsRemaining() != 11 {
		t.Errorf("got %d, want 11", r.BitsRemaining())
	}
}

// bitsToBytes converts a string of '0' and '1' characters to a byte slice,
// padding with zeros to the right to fill the last byte.
func bitsToBytes(bits string) []byte {
	n := len(bits)
	nBytes := (n + 7) / 8
	out := make([]byte, nBytes)
	for i, c := range bits {
		if c == '1' {
			out[i/8] |= 1 << (7 - (i % 8))
		}
	}
	return out
}
