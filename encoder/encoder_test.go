package encoder

import (
	"av1go/obu"
	"errors"
	"testing"
)

func TestNewEncoder(t *testing.T) {
	cfg := DefaultConfig(1920, 1080)
	enc, err := New(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enc.State() != StateReady {
		t.Fatalf("expected StateReady, got %d", enc.State())
	}
}

func TestNewEncoderInvalidConfig(t *testing.T) {
	cfg := DefaultConfig(0, 0) // Invalid dimensions.
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
}

func TestEncoderConfig(t *testing.T) {
	cfg := DefaultConfig(1920, 1080)
	enc, _ := New(cfg)
	got := enc.Config()
	if got.Width != 1920 || got.Height != 1080 {
		t.Fatalf("config mismatch: %dx%d", got.Width, got.Height)
	}
}

func TestEncoderSequenceHeader(t *testing.T) {
	cfg := DefaultConfig(1920, 1080)
	enc, _ := New(cfg)
	sh := enc.SequenceHeader()
	if sh == nil {
		t.Fatal("expected non-nil sequence header")
	}
	if sh.MaxFrameWidth() != 1920 {
		t.Fatalf("expected width 1920, got %d", sh.MaxFrameWidth())
	}
}

func TestWriteSequenceHeaderOBU(t *testing.T) {
	cfg := DefaultConfig(1920, 1080)
	enc, _ := New(cfg)
	data, err := enc.WriteSequenceHeaderOBU()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty OBU data")
	}
	// First byte should be sequence header OBU header.
	if data[0] != 0x0A {
		t.Fatalf("expected OBU header 0x0A, got 0x%02X", data[0])
	}
}

func TestSendFrameAndReceivePacket(t *testing.T) {
	cfg := DefaultConfig(4, 2) // Small frame for testing.
	enc, _ := New(cfg)

	// Create a minimal test frame.
	frame := &Frame{
		Y:       make([]byte, 4*2),
		U:       make([]byte, 2*1),
		V:       make([]byte, 2*1),
		StrideY: 4,
		StrideU: 2,
		StrideV: 2,
		Width:   4,
		Height:  2,
		PTS:     0,
	}

	err := enc.SendFrame(frame)
	if err != nil {
		t.Fatalf("SendFrame error: %v", err)
	}
	if enc.State() != StateEncoding {
		t.Fatalf("expected StateEncoding, got %d", enc.State())
	}

	pkt := enc.ReceivePacket()
	if pkt == nil {
		t.Fatal("expected a packet")
	}
	if !pkt.IsKeyFrame {
		t.Fatal("first frame should be a keyframe")
	}
	if pkt.FrameType != FrameTypeKey {
		t.Fatalf("expected FrameTypeKey, got %d", pkt.FrameType)
	}
	if pkt.Size == 0 {
		t.Fatal("expected non-zero packet size")
	}
	if len(pkt.Data) != pkt.Size {
		t.Fatalf("Data length %d != Size %d", len(pkt.Data), pkt.Size)
	}
}

func TestSendMultipleFrames(t *testing.T) {
	cfg := DefaultConfig(4, 2)
	enc, _ := New(cfg)

	for i := 0; i < 5; i++ {
		frame := &Frame{
			Y:       make([]byte, 4*2),
			U:       make([]byte, 2*1),
			V:       make([]byte, 2*1),
			StrideY: 4,
			StrideU: 2,
			StrideV: 2,
			Width:   4,
			Height:  2,
			PTS:     int64(i),
		}
		err := enc.SendFrame(frame)
		if err != nil {
			t.Fatalf("SendFrame(%d) error: %v", i, err)
		}

		pkt := enc.ReceivePacket()
		if pkt == nil {
			t.Fatalf("expected packet for frame %d", i)
		}
		if i == 0 && !pkt.IsKeyFrame {
			t.Fatal("first frame should be keyframe")
		}
		if pkt.PTS != int64(i) {
			t.Fatalf("expected PTS %d, got %d", i, pkt.PTS)
		}
	}
}

func TestSendFrameDimensionMismatch(t *testing.T) {
	cfg := DefaultConfig(4, 2)
	enc, _ := New(cfg)

	frame := &Frame{
		Y:       make([]byte, 8*4),
		U:       make([]byte, 4*2),
		V:       make([]byte, 4*2),
		StrideY: 8,
		StrideU: 4,
		StrideV: 4,
		Width:   8, // Mismatch!
		Height:  4,
		PTS:     0,
	}

	err := enc.SendFrame(frame)
	if err == nil {
		t.Fatal("expected error for dimension mismatch")
	}
}

func TestSendNilFrame(t *testing.T) {
	cfg := DefaultConfig(4, 2)
	enc, _ := New(cfg)

	err := enc.SendFrame(nil)
	if err == nil {
		t.Fatal("expected error for nil frame")
	}
}

func TestFlush(t *testing.T) {
	cfg := DefaultConfig(4, 2)
	enc, _ := New(cfg)

	frame := &Frame{
		Y:       make([]byte, 4*2),
		U:       make([]byte, 2*1),
		V:       make([]byte, 2*1),
		StrideY: 4,
		StrideU: 2,
		StrideV: 2,
		Width:   4,
		Height:  2,
		PTS:     0,
	}
	_ = enc.SendFrame(frame)
	_ = enc.ReceivePacket()

	err := enc.Flush()
	if err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	if enc.State() != StateDone {
		t.Fatalf("expected StateDone, got %d", enc.State())
	}
}

func TestSendAfterFlush(t *testing.T) {
	cfg := DefaultConfig(4, 2)
	enc, _ := New(cfg)

	frame := &Frame{
		Y:       make([]byte, 4*2),
		U:       make([]byte, 2*1),
		V:       make([]byte, 2*1),
		StrideY: 4,
		StrideU: 2,
		StrideV: 2,
		Width:   4,
		Height:  2,
		PTS:     0,
	}
	_ = enc.SendFrame(frame)
	_ = enc.ReceivePacket()
	_ = enc.Flush()

	err := enc.SendFrame(frame)
	if !errors.Is(err, ErrEncoderFlushed) {
		t.Fatalf("expected ErrEncoderFlushed, got %v", err)
	}
}

func TestReceivePacketEmpty(t *testing.T) {
	cfg := DefaultConfig(4, 2)
	enc, _ := New(cfg)

	pkt := enc.ReceivePacket()
	if pkt != nil {
		t.Fatal("expected nil packet when no frames sent")
	}
}

func TestForceKeyFrame(t *testing.T) {
	cfg := DefaultConfig(4, 2)
	enc, _ := New(cfg)

	// Send first frame (auto-keyframe).
	frame := &Frame{
		Y: make([]byte, 4*2), U: make([]byte, 2), V: make([]byte, 2),
		StrideY: 4, StrideU: 2, StrideV: 2,
		Width: 4, Height: 2, PTS: 0,
	}
	_ = enc.SendFrame(frame)
	_ = enc.ReceivePacket()

	// Send second frame (inter).
	frame.PTS = 1
	frame.ForceKeyFrame = false
	_ = enc.SendFrame(frame)
	pkt := enc.ReceivePacket()
	if pkt.IsKeyFrame {
		t.Fatal("second frame should not be a keyframe")
	}

	// Send third frame with forced keyframe.
	frame.PTS = 2
	frame.ForceKeyFrame = true
	_ = enc.SendFrame(frame)
	pkt = enc.ReceivePacket()
	if !pkt.IsKeyFrame {
		t.Fatal("forced keyframe should be a keyframe")
	}
}

func TestClose(t *testing.T) {
	cfg := DefaultConfig(4, 2)
	enc, _ := New(cfg)
	enc.Close()
	if enc.State() != StateDone {
		t.Fatalf("expected StateDone after Close, got %d", enc.State())
	}
}

func TestKeyFrameInterval(t *testing.T) {
	cfg := DefaultConfig(4, 2)
	cfg.KeyFrameInterval = 3
	enc, _ := New(cfg)

	for i := 0; i < 7; i++ {
		frame := &Frame{
			Y: make([]byte, 4*2), U: make([]byte, 2), V: make([]byte, 2),
			StrideY: 4, StrideU: 2, StrideV: 2,
			Width: 4, Height: 2, PTS: int64(i),
		}
		_ = enc.SendFrame(frame)
		pkt := enc.ReceivePacket()
		expectKey := (i % 3) == 0
		if pkt.IsKeyFrame != expectKey {
			t.Fatalf("frame %d: expected keyframe=%v, got %v", i, expectKey, pkt.IsKeyFrame)
		}
	}
}

func TestEncodeParseRoundtrip(t *testing.T) {
	cfg := DefaultConfig(64, 64)
	enc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	frame := &Frame{
		Y:       make([]byte, 64*64),
		U:       make([]byte, 32*32),
		V:       make([]byte, 32*32),
		StrideY: 64,
		StrideU: 32,
		StrideV: 32,
		Width:   64,
		Height:  64,
		PTS:     0,
	}

	if err := enc.SendFrame(frame); err != nil {
		t.Fatalf("SendFrame: %v", err)
	}

	pkt := enc.ReceivePacket()
	if pkt == nil {
		t.Fatal("expected packet")
	}

	// Parse the OBUs from the encoded packet.
	units, err := obu.ParseUnits(pkt.Data)
	if err != nil {
		t.Fatalf("ParseUnits: %v", err)
	}

	if len(units) < 3 {
		t.Fatalf("expected at least 3 OBUs (TD + SH + Frame), got %d", len(units))
	}

	// Verify OBU types.
	if units[0].Header.Type != obu.TypeTemporalDelimiter {
		t.Fatalf("OBU[0]: expected TD, got %s", units[0].Header.Type)
	}
	if units[1].Header.Type != obu.TypeSequenceHeader {
		t.Fatalf("OBU[1]: expected SH, got %s", units[1].Header.Type)
	}
	if units[2].Header.Type != obu.TypeFrame {
		t.Fatalf("OBU[2]: expected Frame, got %s", units[2].Header.Type)
	}

	// Parse the sequence header.
	sh, err := obu.ParseSequenceHeader(units[1].Payload)
	if err != nil {
		t.Fatalf("ParseSequenceHeader: %v", err)
	}
	if sh.MaxFrameWidth() != 64 || sh.MaxFrameHeight() != 64 {
		t.Fatalf("sequence header dimensions: %dx%d", sh.MaxFrameWidth(), sh.MaxFrameHeight())
	}

	// Parse the frame header from the Frame OBU payload.
	fh, err := obu.ParseBasicFrameHeader(units[2].Payload, sh)
	if err != nil {
		t.Fatalf("ParseBasicFrameHeader: %v", err)
	}
	if fh.FrameType != obu.FrameTypeKey {
		t.Fatalf("expected KEY_FRAME, got %s", fh.FrameType)
	}
	if !fh.ShowFrame {
		t.Fatal("expected show_frame=true")
	}

	t.Logf("Roundtrip OK: %d OBUs, %d bytes total, frame type=%s",
		len(units), len(pkt.Data), fh.FrameType)
}

func TestEncodeMultipleFramesRoundtrip(t *testing.T) {
	cfg := DefaultConfig(32, 32)
	cfg.KeyFrameInterval = 3
	enc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 0; i < 5; i++ {
		frame := &Frame{
			Y:       make([]byte, 32*32),
			U:       make([]byte, 16*16),
			V:       make([]byte, 16*16),
			StrideY: 32,
			StrideU: 16,
			StrideV: 16,
			Width:   32,
			Height:  32,
			PTS:     int64(i),
		}
		if err := enc.SendFrame(frame); err != nil {
			t.Fatalf("SendFrame(%d): %v", i, err)
		}

		pkt := enc.ReceivePacket()
		if pkt == nil {
			t.Fatalf("frame %d: expected packet", i)
		}

		units, err := obu.ParseUnits(pkt.Data)
		if err != nil {
			t.Fatalf("frame %d: ParseUnits: %v", i, err)
		}

		// Every frame should have TD.
		if units[0].Header.Type != obu.TypeTemporalDelimiter {
			t.Fatalf("frame %d: first OBU should be TD", i)
		}

		// Keyframes should include SH.
		isKey := (i % 3) == 0
		if isKey {
			if len(units) < 3 {
				t.Fatalf("frame %d: keyframe should have >= 3 OBUs", i)
			}
			if units[1].Header.Type != obu.TypeSequenceHeader {
				t.Fatalf("frame %d: keyframe should have SH as OBU[1]", i)
			}
		}

		// Find the Frame OBU.
		var frameOBU *obu.Unit
		for j := range units {
			if units[j].Header.Type == obu.TypeFrame {
				frameOBU = &units[j]
				break
			}
		}
		if frameOBU == nil {
			t.Fatalf("frame %d: no Frame OBU found", i)
		}

		t.Logf("frame %d: %d OBUs, %d bytes, keyframe=%v",
			i, len(units), len(pkt.Data), isKey)
	}
}

func TestPacketDataContainsTD(t *testing.T) {
	cfg := DefaultConfig(4, 2)
	enc, _ := New(cfg)

	frame := &Frame{
		Y: make([]byte, 4*2), U: make([]byte, 2), V: make([]byte, 2),
		StrideY: 4, StrideU: 2, StrideV: 2,
		Width: 4, Height: 2, PTS: 0,
	}
	_ = enc.SendFrame(frame)
	pkt := enc.ReceivePacket()

	// First 2 bytes should be the Temporal Delimiter OBU.
	// TD header: type=2, has_size=1 -> 0x12
	// TD size: leb128(0) -> 0x00
	if len(pkt.Data) < 2 {
		t.Fatal("packet too short")
	}
	if pkt.Data[0] != 0x12 {
		t.Fatalf("expected TD header 0x12, got 0x%02X", pkt.Data[0])
	}
	if pkt.Data[1] != 0x00 {
		t.Fatalf("expected TD size 0x00, got 0x%02X", pkt.Data[1])
	}
}
