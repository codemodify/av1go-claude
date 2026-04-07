package obu

import (
	"av1go/bitstream"
	"testing"
)

func TestEncodeHeaderBasic(t *testing.T) {
	// Sequence header, no extension, with size.
	h := Header{
		Type:    TypeSequenceHeader,
		HasSize: true,
	}
	got, err := EncodeHeader(h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 byte header, got %d", len(got))
	}
	// obu_type = 1 (sequence header) -> bits 6:3 = 0001
	// obu_extension_flag = 0 -> bit 2 = 0
	// obu_has_size_flag = 1 -> bit 1 = 1
	// Expected: 0b0_0001_0_1_0 = 0x0A
	if got[0] != 0x0A {
		t.Fatalf("expected 0x0A, got 0x%02X", got[0])
	}
}

func TestEncodeHeaderWithExtension(t *testing.T) {
	h := Header{
		Type:         TypeFrame,
		HasExtension: true,
		HasSize:      true,
		Extension: &ExtensionHeader{
			TemporalID: 2,
			SpatialID:  1,
		},
	}
	got, err := EncodeHeader(h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 byte header, got %d", len(got))
	}
	// Byte 0: obu_type = 6 (frame) -> bits 6:3 = 0110
	//   extension = 1, has_size = 1
	//   0b0_0110_1_1_0 = 0x36
	if got[0] != 0x36 {
		t.Fatalf("byte 0: expected 0x36, got 0x%02X", got[0])
	}
	// Byte 1: temporal_id = 2 -> bits 7:5 = 010
	//   spatial_id = 1 -> bits 4:3 = 01
	//   reserved = 000
	//   0b010_01_000 = 0x48
	if got[1] != 0x48 {
		t.Fatalf("byte 1: expected 0x48, got 0x%02X", got[1])
	}
}

func TestEncodeHeaderNoSize(t *testing.T) {
	h := Header{
		Type:    TypeTileGroup,
		HasSize: false,
	}
	got, err := EncodeHeader(h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// obu_type = 4 -> bits 6:3 = 0100
	// has_size = 0
	// 0b0_0100_0_0_0 = 0x20
	if got[0] != 0x20 {
		t.Fatalf("expected 0x20, got 0x%02X", got[0])
	}
}

func TestEncodeHeaderInvalidType(t *testing.T) {
	h := Header{Type: 0}
	_, err := EncodeHeader(h)
	if err == nil {
		t.Fatal("expected error for invalid OBU type")
	}
}

func TestEncodeHeaderExtensionNil(t *testing.T) {
	h := Header{
		Type:         TypeFrame,
		HasExtension: true,
		HasSize:      true,
		Extension:    nil,
	}
	_, err := EncodeHeader(h)
	if err == nil {
		t.Fatal("expected error when HasExtension=true but Extension=nil")
	}
}

func TestWriteOBU(t *testing.T) {
	w := NewWriter()
	payload := []byte{0x01, 0x02, 0x03}
	err := w.WriteOBU(Header{
		Type:    TypeSequenceHeader,
		HasSize: true,
	}, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := w.Bytes()

	// Header: 1 byte (0x0A)
	// Size: leb128(3) = 1 byte (0x03)
	// Payload: 3 bytes
	// Total: 5 bytes
	if len(got) != 5 {
		t.Fatalf("expected 5 bytes, got %d: %#v", len(got), got)
	}
	if got[0] != 0x0A {
		t.Fatalf("header byte: expected 0x0A, got 0x%02X", got[0])
	}
	if got[1] != 0x03 {
		t.Fatalf("size byte: expected 0x03, got 0x%02X", got[1])
	}
	if got[2] != 0x01 || got[3] != 0x02 || got[4] != 0x03 {
		t.Fatalf("payload mismatch: %#v", got[2:5])
	}
}

func TestWriteOBUEmptyPayload(t *testing.T) {
	w := NewWriter()
	err := w.WriteOBU(Header{
		Type:    TypeTemporalDelimiter,
		HasSize: true,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := w.Bytes()
	// Header: 1 byte, Size: leb128(0) = 1 byte (0x00)
	if len(got) != 2 {
		t.Fatalf("expected 2 bytes, got %d: %#v", len(got), got)
	}
}

func TestWriteTemporalDelimiter(t *testing.T) {
	w := NewWriter()
	err := w.WriteTemporalDelimiter()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := w.Bytes()
	// TD OBU: header=0x12 (type=2, has_size=1), size=0x00
	if len(got) != 2 {
		t.Fatalf("expected 2 bytes, got %d", len(got))
	}
	if got[0] != 0x12 {
		t.Fatalf("header: expected 0x12, got 0x%02X", got[0])
	}
	if got[1] != 0x00 {
		t.Fatalf("size: expected 0x00, got 0x%02X", got[1])
	}
}

func TestWriteOBULargePayload(t *testing.T) {
	// Test with payload > 127 bytes to exercise multi-byte leb128 size.
	w := NewWriter()
	payload := make([]byte, 300)
	for i := range payload {
		payload[i] = byte(i & 0xFF)
	}
	err := w.WriteOBU(Header{
		Type:    TypeTileGroup,
		HasSize: true,
	}, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := w.Bytes()

	// Verify size field.
	// leb128(300) = 2 bytes: 0xAC, 0x02
	sizeBytes := got[1:3] // After 1 header byte
	val, n, err := bitstream.ReadLeb128(sizeBytes)
	if err != nil {
		t.Fatalf("failed to decode size: %v", err)
	}
	if val != 300 || n != 2 {
		t.Fatalf("size: expected (300, 2), got (%d, %d)", val, n)
	}

	// Total: 1 (header) + 2 (size) + 300 (payload) = 303
	if len(got) != 303 {
		t.Fatalf("expected 303 bytes, got %d", len(got))
	}
}

func TestMakeOBU(t *testing.T) {
	payload := []byte{0xAA, 0xBB}
	got, err := MakeOBU(Header{
		Type: TypeMetadata,
	}, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Header: type=5 (metadata), has_size=1
	// 0b0_0101_0_1_0 = 0x2A
	if got[0] != 0x2A {
		t.Fatalf("header: expected 0x2A, got 0x%02X", got[0])
	}
	if got[1] != 0x02 {
		t.Fatalf("size: expected 0x02, got 0x%02X", got[1])
	}
}

func TestWriterReset(t *testing.T) {
	w := NewWriter()
	_ = w.WriteTemporalDelimiter()
	if w.Len() == 0 {
		t.Fatal("expected non-zero length after write")
	}
	w.Reset()
	if w.Len() != 0 {
		t.Fatalf("expected 0 length after reset, got %d", w.Len())
	}
}

func TestWriteMultipleOBUs(t *testing.T) {
	w := NewWriter()

	// Write TD.
	err := w.WriteTemporalDelimiter()
	if err != nil {
		t.Fatalf("TD write error: %v", err)
	}

	// Write sequence header with dummy payload.
	err = w.WriteOBU(Header{
		Type:    TypeSequenceHeader,
		HasSize: true,
	}, []byte{0x10, 0x20})
	if err != nil {
		t.Fatalf("SH write error: %v", err)
	}

	got := w.Bytes()
	// TD: 2 bytes + SH: 1 header + 1 size + 2 payload = 6 bytes total
	if len(got) != 6 {
		t.Fatalf("expected 6 bytes, got %d: %#v", len(got), got)
	}
}
