package mp4

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// testMP4Path returns the path to the sample MP4 file.
// Tests that need this file are skipped if it doesn't exist.
func testMP4Path(t *testing.T) string {
	t.Helper()
	// Walk up from the test directory to find the project root.
	path := filepath.Join("..", "spbtv_sample_bipbop_av1_960x540_25fps.mp4")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("sample MP4 not found at %s: %v", path, err)
	}
	return path
}

func TestOpenAndVideoTrack(t *testing.T) {
	path := testMP4Path(t)
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if len(f.Tracks) < 2 {
		t.Fatalf("expected >= 2 tracks, got %d", len(f.Tracks))
	}

	vt := f.VideoTrack()
	if vt == nil {
		t.Fatal("no AV1 video track found")
	}
	if vt.Codec != "av01" {
		t.Errorf("codec = %q, want av01", vt.Codec)
	}
	if vt.HandlerType != "vide" {
		t.Errorf("handler = %q, want vide", vt.HandlerType)
	}
}

func TestAV1Config(t *testing.T) {
	path := testMP4Path(t)
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	vt := f.VideoTrack()
	if vt == nil {
		t.Fatal("no video track")
	}

	if len(vt.AV1Config) < 4 {
		t.Fatalf("AV1Config too short: %d bytes", len(vt.AV1Config))
	}

	// Check marker and version
	marker := (vt.AV1Config[0] >> 7) & 1
	version := vt.AV1Config[0] & 0x7F
	if marker != 1 {
		t.Errorf("marker = %d, want 1", marker)
	}
	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}

	// Profile should be 0 for this sample
	seqProfile := (vt.AV1Config[1] >> 5) & 7
	if seqProfile != 0 {
		t.Errorf("seq_profile = %d, want 0", seqProfile)
	}

	if len(vt.AV1ConfigOBUs) == 0 {
		t.Error("no configOBUs found")
	}
	t.Logf("AV1Config: %d bytes, configOBUs: %d bytes", len(vt.AV1Config), len(vt.AV1ConfigOBUs))
}

func TestSampleTable(t *testing.T) {
	path := testMP4Path(t)
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	vt := f.VideoTrack()
	if vt == nil {
		t.Fatal("no video track")
	}

	samples := vt.Samples()
	if len(samples) == 0 {
		t.Fatal("no samples found")
	}
	t.Logf("found %d video samples", len(samples))

	// First sample should be a sync sample
	if !samples[0].IsSync {
		t.Error("first sample should be a sync sample")
	}

	// All samples should have positive sizes
	for i, s := range samples {
		if s.Size == 0 {
			t.Errorf("sample %d has zero size", i)
		}
	}
}

func TestReadSample(t *testing.T) {
	path := testMP4Path(t)
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	vt := f.VideoTrack()
	if vt == nil {
		t.Fatal("no video track")
	}

	// Read first sample
	data, err := f.ReadSample(vt, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty sample data")
	}

	// First bytes should be valid OBU headers.
	// In AV1 low-overhead format, samples start with OBU data.
	// The first OBU is typically a temporal delimiter (type=2).
	obuType := (data[0] >> 3) & 0xF
	t.Logf("first sample: %d bytes, first OBU type=%d", len(data), obuType)
	if obuType != 2 {
		t.Logf("note: first OBU type is %d (expected temporal delimiter=2)", obuType)
	}
}

func TestOpenData(t *testing.T) {
	// Build a minimal MP4 in memory with ftyp + moov (empty)
	data := buildMinimalMP4()
	f, err := OpenData(data)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("nil file")
	}
}

// buildMinimalMP4 creates a tiny valid MP4 with just ftyp and moov boxes.
func buildMinimalMP4() []byte {
	var buf []byte

	// ftyp box
	ftyp := []byte("isom")
	ftypBox := makeBox("ftyp", ftyp)
	buf = append(buf, ftypBox...)

	// moov box (empty)
	moovBox := makeBox("moov", nil)
	buf = append(buf, moovBox...)

	return buf
}

func makeBox(typ string, content []byte) []byte {
	size := uint32(8 + len(content))
	buf := make([]byte, size)
	binary.BigEndian.PutUint32(buf[0:4], size)
	copy(buf[4:8], typ)
	if len(content) > 0 {
		copy(buf[8:], content)
	}
	return buf
}
