package obu

import (
	"av1go/bitstream"
	"os"
	"path/filepath"
	"testing"
)

func TestParseHeaderBasic(t *testing.T) {
	// Temporal delimiter: type=2, no extension, has_size=1
	// 0b0_0010_0_1_0 = 0x12
	h, n, err := ParseHeader([]byte{0x12})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("consumed %d bytes, want 1", n)
	}
	if h.Type != TypeTemporalDelimiter {
		t.Errorf("type = %d, want %d (TD)", h.Type, TypeTemporalDelimiter)
	}
	if h.HasExtension {
		t.Error("unexpected extension flag")
	}
	if !h.HasSize {
		t.Error("expected has_size flag")
	}
}

func TestParseHeaderWithExtension(t *testing.T) {
	// Frame OBU with extension: type=6, ext=1, has_size=1
	// Byte 0: 0b0_0110_1_1_0 = 0x36
	// Byte 1: temporal_id=2, spatial_id=1, reserved=0
	//         0b010_01_000 = 0x48
	h, n, err := ParseHeader([]byte{0x36, 0x48})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("consumed %d bytes, want 2", n)
	}
	if h.Type != TypeFrame {
		t.Errorf("type = %d, want %d (Frame)", h.Type, TypeFrame)
	}
	if !h.HasExtension {
		t.Error("expected extension flag")
	}
	if h.Extension == nil {
		t.Fatal("extension is nil")
	}
	if h.Extension.TemporalID != 2 {
		t.Errorf("temporal_id = %d, want 2", h.Extension.TemporalID)
	}
	if h.Extension.SpatialID != 1 {
		t.Errorf("spatial_id = %d, want 1", h.Extension.SpatialID)
	}
}

func TestParseHeaderForbiddenBit(t *testing.T) {
	// Forbidden bit set (bit 7 = 1)
	_, _, err := ParseHeader([]byte{0x92})
	if err == nil {
		t.Error("expected error for forbidden bit")
	}
}

func TestParseUnit(t *testing.T) {
	// Build a temporal delimiter OBU: header=0x12, size=0x00
	data := []byte{0x12, 0x00}
	u, n, err := ParseUnit(data)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("consumed %d bytes, want 2", n)
	}
	if u.Header.Type != TypeTemporalDelimiter {
		t.Errorf("type = %v, want TD", u.Header.Type)
	}
	if u.Size != 0 {
		t.Errorf("size = %d, want 0", u.Size)
	}
	if len(u.Payload) != 0 {
		t.Errorf("payload len = %d, want 0", len(u.Payload))
	}
}

func TestParseUnitWithPayload(t *testing.T) {
	// Sequence header OBU with 3-byte payload
	// Header: type=1, has_size=1 -> 0b0_0001_0_1_0 = 0x0A
	// Size: leb128(3) = 0x03
	// Payload: 0xAA, 0xBB, 0xCC
	data := []byte{0x0A, 0x03, 0xAA, 0xBB, 0xCC}
	u, n, err := ParseUnit(data)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("consumed %d bytes, want 5", n)
	}
	if u.Header.Type != TypeSequenceHeader {
		t.Errorf("type = %v, want SEQ_HEADER", u.Header.Type)
	}
	if u.Size != 3 {
		t.Errorf("size = %d, want 3", u.Size)
	}
	if len(u.Payload) != 3 {
		t.Errorf("payload len = %d, want 3", len(u.Payload))
	}
}

func TestParseUnits(t *testing.T) {
	// Two OBUs: TD + Sequence Header with dummy payload
	data := []byte{
		0x12, 0x00, // TD
		0x0A, 0x02, 0xAA, 0xBB, // SH with 2-byte payload
	}
	units, err := ParseUnits(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2", len(units))
	}
	if units[0].Header.Type != TypeTemporalDelimiter {
		t.Errorf("unit[0] type = %v, want TD", units[0].Header.Type)
	}
	if units[1].Header.Type != TypeSequenceHeader {
		t.Errorf("unit[1] type = %v, want SEQ_HEADER", units[1].Header.Type)
	}
}

func TestParseSequenceHeaderFromConfigOBUs(t *testing.T) {
	// Use the known configOBUs from the sample MP4.
	// These bytes were extracted from the av1C box.
	// First OBU in configOBUs is a sequence header.
	configOBUs := []byte{0x0a, 0x0b, 0x00, 0x00, 0x00, 0x24, 0xcf, 0x7f, 0x0d, 0xbf, 0xff, 0x30, 0x08}

	units, err := ParseUnits(configOBUs)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) == 0 {
		t.Fatal("no OBUs found in configOBUs")
	}
	if units[0].Header.Type != TypeSequenceHeader {
		t.Fatalf("first OBU type = %v, want SEQ_HEADER", units[0].Header.Type)
	}

	sh, err := ParseSequenceHeader(units[0].Payload)
	if err != nil {
		t.Fatalf("ParseSequenceHeader: %v", err)
	}

	// Verify known values for this sample.
	if sh.SeqProfile != 0 {
		t.Errorf("seq_profile = %d, want 0 (Main)", sh.SeqProfile)
	}

	t.Logf("max frame size: %dx%d", sh.MaxFrameWidth(), sh.MaxFrameHeight())
	if sh.MaxFrameWidth() != 960 {
		t.Errorf("max_frame_width = %d, want 960", sh.MaxFrameWidth())
	}
	if sh.MaxFrameHeight() != 540 {
		t.Errorf("max_frame_height = %d, want 540", sh.MaxFrameHeight())
	}

	t.Logf("bit depth: %d", sh.ColorConfig.BitDepth())
	if sh.ColorConfig.BitDepth() != 8 {
		t.Errorf("bit_depth = %d, want 8", sh.ColorConfig.BitDepth())
	}

	// Profile 0 (Main) -> 4:2:0
	if sh.ColorConfig.SubsamplingX != 1 || sh.ColorConfig.SubsamplingY != 1 {
		t.Errorf("subsampling = %d:%d, want 1:1 (4:2:0)",
			sh.ColorConfig.SubsamplingX, sh.ColorConfig.SubsamplingY)
	}

	t.Logf("use_128x128_superblock: %v", sh.Use128x128Superblock)
	t.Logf("enable_cdef: %v, enable_restoration: %v", sh.EnableCDEF, sh.EnableRestoration)
	t.Logf("film_grain_params_present: %v", sh.FilmGrainParamsPresent)
}

func TestParseSequenceHeaderRoundTrip(t *testing.T) {
	// Write a sequence header using the bitstream writer, then parse it back.
	w := bitstream.NewWriter(64)

	// seq_profile = 0 (Main)
	w.WriteBits(0, 3)
	// still_picture = 0
	w.WriteBool(false)
	// reduced_still_picture_header = 1 (simplified)
	w.WriteBool(true)
	// seq_level_idx = 4
	w.WriteBits(4, 5)
	// frame_width_bits_minus_1 = 9 (10 bits for width)
	w.WriteBits(9, 4)
	// frame_height_bits_minus_1 = 8 (9 bits for height)
	w.WriteBits(8, 4)
	// max_frame_width_minus_1 = 319 (width=320)
	w.WriteBits(319, 10)
	// max_frame_height_minus_1 = 239 (height=240)
	w.WriteBits(239, 9)
	// use_128x128_superblock = 0
	w.WriteBool(false)
	// enable_filter_intra = 0
	w.WriteBool(false)
	// enable_intra_edge_filter = 0
	w.WriteBool(false)
	// enable_superres = 0
	w.WriteBool(false)
	// enable_cdef = 1
	w.WriteBool(true)
	// enable_restoration = 0
	w.WriteBool(false)
	// color_config: high_bitdepth = 0
	w.WriteBool(false)
	// mono_chrome = 0 (profile != 1, so mono_chrome is read)
	w.WriteBool(false)
	// color_description_present = 0
	w.WriteBool(false)
	// color_range = 0 (studio range)
	w.WriteBool(false)
	// chroma_sample_position = 0 (unknown) -- 2 bits, since subsampling=1,1 for profile 0
	w.WriteBits(0, 2)
	// separate_uv_delta_q = 0
	w.WriteBool(false)
	// film_grain_params_present = 0
	w.WriteBool(false)

	payload := w.Bytes()

	sh, err := ParseSequenceHeader(payload)
	if err != nil {
		t.Fatalf("ParseSequenceHeader: %v", err)
	}

	if sh.SeqProfile != 0 {
		t.Errorf("seq_profile = %d, want 0", sh.SeqProfile)
	}
	if !sh.ReducedStillPictureHeader {
		t.Error("expected reduced_still_picture_header")
	}
	if sh.MaxFrameWidth() != 320 {
		t.Errorf("width = %d, want 320", sh.MaxFrameWidth())
	}
	if sh.MaxFrameHeight() != 240 {
		t.Errorf("height = %d, want 240", sh.MaxFrameHeight())
	}
	if sh.EnableCDEF != true {
		t.Error("expected enable_cdef = true")
	}
	if sh.ColorConfig.BitDepth() != 8 {
		t.Errorf("bit_depth = %d, want 8", sh.ColorConfig.BitDepth())
	}
}

func TestParseBasicFrameHeader(t *testing.T) {
	// Parse frame headers from the sample MP4 if available.
	path := filepath.Join("..", "spbtv_sample_bipbop_av1_960x540_25fps.mp4")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("sample MP4 not found: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Parse the configOBUs to get the sequence header.
	configOBUs := []byte{0x0a, 0x0b, 0x00, 0x00, 0x00, 0x24, 0xcf, 0x7f, 0x0d, 0xbf, 0xff, 0x30, 0x08}
	units, err := ParseUnits(configOBUs)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := ParseSequenceHeader(units[0].Payload)
	if err != nil {
		t.Fatal(err)
	}

	// Find the first mdat box and parse its first sample's OBUs.
	// mdat is at offset 0x28+8=0x30, but we need to use mp4 package for proper offsets.
	// Instead, use the known first sample data position.
	// From earlier analysis: mdat data starts at 0x30, first sample starts there.
	mdatStart := 0x30

	// Parse OBUs from the first few hundred bytes (first sample).
	sampleData := data[mdatStart : mdatStart+7653]
	sampleUnits, err := ParseUnits(sampleData)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("first sample has %d OBUs", len(sampleUnits))
	for i, u := range sampleUnits {
		t.Logf("  OBU[%d]: %v, size=%d", i, u.Header.Type, u.Size)
	}

	// Find the frame or frame_header OBU.
	for _, u := range sampleUnits {
		if u.Header.Type == TypeFrame || u.Header.Type == TypeFrameHeader {
			fh, err := ParseBasicFrameHeader(u.Payload, sh)
			if err != nil {
				t.Fatalf("ParseBasicFrameHeader: %v", err)
			}
			t.Logf("frame_type=%v, show_frame=%v, show_existing_frame=%v",
				fh.FrameType, fh.ShowFrame, fh.ShowExistingFrame)

			// First frame should be a key frame.
			if fh.FrameType != FrameTypeKey {
				t.Errorf("first frame type = %v, want KEY_FRAME", fh.FrameType)
			}
			if !fh.ShowFrame {
				t.Error("first frame should have show_frame=true")
			}
			break
		}
	}
}

func TestFrameTypeString(t *testing.T) {
	tests := []struct {
		ft   FrameType
		want string
	}{
		{FrameTypeKey, "KEY_FRAME"},
		{FrameTypeInter, "INTER_FRAME"},
		{FrameTypeIntraOnly, "INTRA_ONLY_FRAME"},
		{FrameTypeSwitch, "SWITCH_FRAME"},
	}
	for _, tc := range tests {
		if got := tc.ft.String(); got != tc.want {
			t.Errorf("%d.String() = %q, want %q", tc.ft, got, tc.want)
		}
	}
}

func TestOBUTypeString(t *testing.T) {
	tests := []struct {
		typ  Type
		want string
	}{
		{TypeSequenceHeader, "OBU_SEQUENCE_HEADER"},
		{TypeTemporalDelimiter, "OBU_TEMPORAL_DELIMITER"},
		{TypeFrame, "OBU_FRAME"},
		{TypePadding, "OBU_PADDING"},
	}
	for _, tc := range tests {
		if got := tc.typ.String(); got != tc.want {
			t.Errorf("Type(%d).String() = %q, want %q", tc.typ, got, tc.want)
		}
	}
}
