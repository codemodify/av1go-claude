package annexb

import (
	"av1go/bitstream"
	"av1go/obu"
	"testing"
)

// buildLeb128 encodes val as leb128 and returns the bytes.
func buildLeb128(val uint64) []byte {
	buf := make([]byte, 10)
	n := bitstream.PutLeb128(buf, val)
	return buf[:n]
}

// buildOBU creates a raw OBU with has_size=0 (for Annex B framing).
// Returns the OBU bytes (header + payload, no size field).
func buildOBU(obuType obu.Type, hasExtension bool, payload []byte) []byte {
	// obu_header byte: forbidden(1) | obu_type(4) | extension_flag(1) | has_size_field(1) | reserved(1)
	b0 := byte(obuType) << 3
	if hasExtension {
		b0 |= 0x04
	}
	// has_size_field = 0 for Annex B
	result := []byte{b0}
	if hasExtension {
		// extension byte: temporal_id(3) | spatial_id(2) | reserved(3)
		result = append(result, 0x00)
	}
	result = append(result, payload...)
	return result
}

func TestReadAll_SingleTU_SingleFU_SingleOBU(t *testing.T) {
	// Build a temporal delimiter OBU (empty payload).
	obuData := buildOBU(obu.TypeTemporalDelimiter, false, nil)

	// Frame unit: obu_length + obu
	var fuData []byte
	fuData = append(fuData, buildLeb128(uint64(len(obuData)))...)
	fuData = append(fuData, obuData...)

	// Temporal unit: frame_unit_size + frame_unit
	var tuData []byte
	tuData = append(tuData, buildLeb128(uint64(len(fuData)))...)
	tuData = append(tuData, fuData...)

	// Full stream: temporal_unit_size + temporal_unit
	var stream []byte
	stream = append(stream, buildLeb128(uint64(len(tuData)))...)
	stream = append(stream, tuData...)

	r := NewReader(stream)
	units, err := r.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("got %d temporal units, want 1", len(units))
	}
	tu := units[0]
	if len(tu.FrameUnits) != 1 {
		t.Fatalf("got %d frame units, want 1", len(tu.FrameUnits))
	}
	fu := tu.FrameUnits[0]
	if len(fu.OBUs) != 1 {
		t.Fatalf("got %d OBUs, want 1", len(fu.OBUs))
	}
	if fu.OBUs[0].Header.Type != obu.TypeTemporalDelimiter {
		t.Errorf("OBU type: got %s, want %s", fu.OBUs[0].Header.Type, obu.TypeTemporalDelimiter)
	}
}

func TestReadAll_MultipleTUs(t *testing.T) {
	var stream []byte

	// Build two temporal units, each with one frame unit containing a sequence header OBU.
	for i := 0; i < 2; i++ {
		payload := []byte{0x00, 0x00, 0x00} // dummy payload
		obuData := buildOBU(obu.TypeSequenceHeader, false, payload)

		var fuData []byte
		fuData = append(fuData, buildLeb128(uint64(len(obuData)))...)
		fuData = append(fuData, obuData...)

		var tuData []byte
		tuData = append(tuData, buildLeb128(uint64(len(fuData)))...)
		tuData = append(tuData, fuData...)

		stream = append(stream, buildLeb128(uint64(len(tuData)))...)
		stream = append(stream, tuData...)
	}

	r := NewReader(stream)
	units, err := r.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(units) != 2 {
		t.Fatalf("got %d temporal units, want 2", len(units))
	}
	for i, tu := range units {
		if tu.Index != i {
			t.Errorf("TU %d: index=%d", i, tu.Index)
		}
	}
}

func TestReadAll_MultipleOBUsInFrameUnit(t *testing.T) {
	// Two OBUs in one frame unit: a temporal delimiter + a sequence header.
	tdOBU := buildOBU(obu.TypeTemporalDelimiter, false, nil)
	shOBU := buildOBU(obu.TypeSequenceHeader, false, []byte{0x10, 0x20})

	var fuData []byte
	fuData = append(fuData, buildLeb128(uint64(len(tdOBU)))...)
	fuData = append(fuData, tdOBU...)
	fuData = append(fuData, buildLeb128(uint64(len(shOBU)))...)
	fuData = append(fuData, shOBU...)

	var tuData []byte
	tuData = append(tuData, buildLeb128(uint64(len(fuData)))...)
	tuData = append(tuData, fuData...)

	var stream []byte
	stream = append(stream, buildLeb128(uint64(len(tuData)))...)
	stream = append(stream, tuData...)

	r := NewReader(stream)
	units, err := r.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("got %d temporal units, want 1", len(units))
	}
	fu := units[0].FrameUnits[0]
	if len(fu.OBUs) != 2 {
		t.Fatalf("got %d OBUs, want 2", len(fu.OBUs))
	}
	if fu.OBUs[0].Header.Type != obu.TypeTemporalDelimiter {
		t.Errorf("OBU[0] type: got %s, want %s", fu.OBUs[0].Header.Type, obu.TypeTemporalDelimiter)
	}
	if fu.OBUs[1].Header.Type != obu.TypeSequenceHeader {
		t.Errorf("OBU[1] type: got %s, want %s", fu.OBUs[1].Header.Type, obu.TypeSequenceHeader)
	}
}

func TestAllOBUs(t *testing.T) {
	units := []TemporalUnit{
		{
			FrameUnits: []FrameUnit{
				{OBUs: []obu.Unit{{Header: obu.Header{Type: obu.TypeTemporalDelimiter}}}},
				{OBUs: []obu.Unit{{Header: obu.Header{Type: obu.TypeSequenceHeader}}}},
			},
		},
		{
			FrameUnits: []FrameUnit{
				{OBUs: []obu.Unit{{Header: obu.Header{Type: obu.TypeFrame}}}},
			},
		},
	}
	all := AllOBUs(units)
	if len(all) != 3 {
		t.Fatalf("AllOBUs: got %d, want 3", len(all))
	}
}

func TestReadAll_EmptyStream(t *testing.T) {
	r := NewReader(nil)
	units, err := r.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(units) != 0 {
		t.Errorf("got %d units, want 0", len(units))
	}
}

func TestReadAll_OBUWithExtension(t *testing.T) {
	obuData := buildOBU(obu.TypeFrame, true, []byte{0xAA, 0xBB})

	var fuData []byte
	fuData = append(fuData, buildLeb128(uint64(len(obuData)))...)
	fuData = append(fuData, obuData...)

	var tuData []byte
	tuData = append(tuData, buildLeb128(uint64(len(fuData)))...)
	tuData = append(tuData, fuData...)

	var stream []byte
	stream = append(stream, buildLeb128(uint64(len(tuData)))...)
	stream = append(stream, tuData...)

	r := NewReader(stream)
	units, err := r.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("got %d TUs, want 1", len(units))
	}
	u := units[0].FrameUnits[0].OBUs[0]
	if u.Header.Type != obu.TypeFrame {
		t.Errorf("OBU type: got %s, want %s", u.Header.Type, obu.TypeFrame)
	}
	if !u.Header.HasExtension {
		t.Error("expected OBU to have extension")
	}
}
