package obu

import (
	"av1go/bitstream"
	"io"
	"testing"
)

// buildSizedOBU creates a raw OBU with has_size=1 (low-overhead format).
// Returns the complete OBU bytes (header + leb128 size + payload).
func buildSizedOBU(obuType Type, payload []byte) []byte {
	// obu_header: forbidden(1=0) | obu_type(4) | extension_flag(1=0) | has_size_field(1=1) | reserved(1=0)
	b0 := byte(obuType)<<3 | 0x02 // has_size_field = 1
	result := []byte{b0}

	// leb128 size of payload
	sizeBuf := make([]byte, 10)
	n := bitstream.PutLeb128(sizeBuf, uint64(len(payload)))
	result = append(result, sizeBuf[:n]...)
	result = append(result, payload...)
	return result
}

func TestStreamReader_ReadAll(t *testing.T) {
	// Build a stream with 3 OBUs.
	var stream []byte
	stream = append(stream, buildSizedOBU(TypeTemporalDelimiter, nil)...)
	stream = append(stream, buildSizedOBU(TypeSequenceHeader, []byte{0x01, 0x02, 0x03})...)
	stream = append(stream, buildSizedOBU(TypeFrame, []byte{0xAA, 0xBB})...)

	sr := NewStreamReader(stream)
	units, err := sr.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(units) != 3 {
		t.Fatalf("got %d units, want 3", len(units))
	}

	wantTypes := []Type{TypeTemporalDelimiter, TypeSequenceHeader, TypeFrame}
	for i, u := range units {
		if u.Header.Type != wantTypes[i] {
			t.Errorf("unit %d: type=%s, want %s", i, u.Header.Type, wantTypes[i])
		}
	}
}

func TestStreamReader_Next(t *testing.T) {
	var stream []byte
	stream = append(stream, buildSizedOBU(TypeTemporalDelimiter, nil)...)
	stream = append(stream, buildSizedOBU(TypeFrame, []byte{0x10})...)

	sr := NewStreamReader(stream)

	u1, err := sr.Next()
	if err != nil {
		t.Fatalf("Next 1: %v", err)
	}
	if u1.Header.Type != TypeTemporalDelimiter {
		t.Errorf("unit 1: type=%s, want %s", u1.Header.Type, TypeTemporalDelimiter)
	}

	u2, err := sr.Next()
	if err != nil {
		t.Fatalf("Next 2: %v", err)
	}
	if u2.Header.Type != TypeFrame {
		t.Errorf("unit 2: type=%s, want %s", u2.Header.Type, TypeFrame)
	}

	_, err = sr.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestStreamReader_Reset(t *testing.T) {
	stream := buildSizedOBU(TypeTemporalDelimiter, nil)

	sr := NewStreamReader(stream)
	_, err := sr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if sr.Remaining() != 0 {
		t.Errorf("remaining: %d, want 0", sr.Remaining())
	}

	sr.Reset()
	if sr.Position() != 0 {
		t.Errorf("position after reset: %d, want 0", sr.Position())
	}
	if sr.Remaining() != len(stream) {
		t.Errorf("remaining after reset: %d, want %d", sr.Remaining(), len(stream))
	}
}

func TestStreamReader_Empty(t *testing.T) {
	sr := NewStreamReader(nil)
	units, err := sr.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(units) != 0 {
		t.Errorf("got %d units, want 0", len(units))
	}
}

func TestReadOBUStream(t *testing.T) {
	stream := buildSizedOBU(TypePadding, []byte{0x00, 0x00})

	units, err := ReadOBUStream(stream)
	if err != nil {
		t.Fatalf("ReadOBUStream: %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("got %d units, want 1", len(units))
	}
	if units[0].Header.Type != TypePadding {
		t.Errorf("type: got %s, want %s", units[0].Header.Type, TypePadding)
	}
}
