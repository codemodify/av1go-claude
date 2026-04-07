package ivf

import (
	"encoding/binary"
	"testing"
)

// buildTestIVF creates a minimal IVF file in memory with the given parameters.
func buildTestIVF(codec [4]byte, width, height uint16, frames [][]byte) []byte {
	// File header: 32 bytes
	hdr := make([]byte, fileHeaderSize)
	copy(hdr[0:4], "DKIF")
	binary.LittleEndian.PutUint16(hdr[4:6], 0)  // version
	binary.LittleEndian.PutUint16(hdr[6:8], 32)  // header length
	copy(hdr[8:12], codec[:])
	binary.LittleEndian.PutUint16(hdr[12:14], width)
	binary.LittleEndian.PutUint16(hdr[14:16], height)
	binary.LittleEndian.PutUint32(hdr[16:20], 1)                  // timebase num
	binary.LittleEndian.PutUint32(hdr[20:24], 30)                 // timebase den
	binary.LittleEndian.PutUint32(hdr[24:28], uint32(len(frames))) // frame count
	binary.LittleEndian.PutUint32(hdr[28:32], 0)                  // unused

	buf := make([]byte, 0, 512)
	buf = append(buf, hdr...)

	for i, frame := range frames {
		// Frame header: 12 bytes
		fhdr := make([]byte, frameHeaderSize)
		binary.LittleEndian.PutUint32(fhdr[0:4], uint32(len(frame)))
		binary.LittleEndian.PutUint64(fhdr[4:12], uint64(i))
		buf = append(buf, fhdr...)
		buf = append(buf, frame...)
	}
	return buf
}

func TestOpenData_ValidIVF(t *testing.T) {
	frames := [][]byte{
		{0x01, 0x02, 0x03},
		{0x04, 0x05},
		{0x06, 0x07, 0x08, 0x09},
	}
	data := buildTestIVF([4]byte{'A', 'V', '0', '1'}, 320, 240, frames)

	d, err := OpenData(data)
	if err != nil {
		t.Fatalf("OpenData: %v", err)
	}

	// Check file header.
	if !d.Header.IsAV1() {
		t.Errorf("expected AV1 codec, got %q", d.Header.CodecString())
	}
	if d.Header.Width != 320 || d.Header.Height != 240 {
		t.Errorf("dimensions: got %dx%d, want 320x240", d.Header.Width, d.Header.Height)
	}
	if d.Header.TimebaseNum != 1 || d.Header.TimebaseDen != 30 {
		t.Errorf("timebase: got %d/%d, want 1/30", d.Header.TimebaseNum, d.Header.TimebaseDen)
	}
	if d.Header.FrameCount != 3 {
		t.Errorf("frame count in header: got %d, want 3", d.Header.FrameCount)
	}

	// Check frame count from scanning.
	count, err := d.FrameCount()
	if err != nil {
		t.Fatalf("FrameCount: %v", err)
	}
	if count != 3 {
		t.Fatalf("FrameCount: got %d, want 3", count)
	}

	// Read each frame.
	for i, want := range frames {
		f, err := d.ReadFrame(i)
		if err != nil {
			t.Fatalf("ReadFrame(%d): %v", i, err)
		}
		if f.Index != i {
			t.Errorf("frame %d: index=%d", i, f.Index)
		}
		if f.Size != uint32(len(want)) {
			t.Errorf("frame %d: size=%d, want %d", i, f.Size, len(want))
		}
		if f.Timestamp != uint64(i) {
			t.Errorf("frame %d: timestamp=%d, want %d", i, f.Timestamp, i)
		}
		for j := range want {
			if f.Data[j] != want[j] {
				t.Errorf("frame %d: data[%d]=%#02x, want %#02x", i, j, f.Data[j], want[j])
			}
		}
	}
}

func TestOpenData_ReadAllFrames(t *testing.T) {
	frames := [][]byte{
		{0xAA},
		{0xBB, 0xCC},
	}
	data := buildTestIVF([4]byte{'V', 'P', '9', '0'}, 1920, 1080, frames)

	d, err := OpenData(data)
	if err != nil {
		t.Fatalf("OpenData: %v", err)
	}

	allFrames, err := d.ReadAllFrames()
	if err != nil {
		t.Fatalf("ReadAllFrames: %v", err)
	}
	if len(allFrames) != 2 {
		t.Fatalf("got %d frames, want 2", len(allFrames))
	}
}

func TestFrameReader(t *testing.T) {
	frames := [][]byte{
		{0x10, 0x20},
		{0x30},
		{0x40, 0x50, 0x60},
	}
	data := buildTestIVF([4]byte{'A', 'V', '0', '1'}, 64, 64, frames)

	d, err := OpenData(data)
	if err != nil {
		t.Fatalf("OpenData: %v", err)
	}

	fr := d.NewFrameReader()
	var got []*Frame
	for fr.Next() {
		f := fr.Frame()
		got = append(got, f)
	}
	if fr.Err() != nil {
		t.Fatalf("FrameReader error: %v", fr.Err())
	}
	if len(got) != 3 {
		t.Fatalf("got %d frames, want 3", len(got))
	}
	for i, f := range got {
		if f.Index != i {
			t.Errorf("frame %d: index=%d", i, f.Index)
		}
		if int(f.Size) != len(frames[i]) {
			t.Errorf("frame %d: size=%d, want %d", i, f.Size, len(frames[i]))
		}
	}
}

func TestOpenData_InvalidSignature(t *testing.T) {
	data := make([]byte, 32)
	copy(data[0:4], "XXXX")
	_, err := OpenData(data)
	if err == nil {
		t.Fatal("expected error for invalid signature")
	}
}

func TestOpenData_TooShort(t *testing.T) {
	data := []byte("DKIF")
	_, err := OpenData(data)
	if err == nil {
		t.Fatal("expected error for short data")
	}
}

func TestOpenData_EmptyFrames(t *testing.T) {
	data := buildTestIVF([4]byte{'A', 'V', '0', '1'}, 16, 16, nil)
	d, err := OpenData(data)
	if err != nil {
		t.Fatalf("OpenData: %v", err)
	}
	count, err := d.FrameCount()
	if err != nil {
		t.Fatalf("FrameCount: %v", err)
	}
	if count != 0 {
		t.Errorf("got %d frames, want 0", count)
	}
}

func TestReadFrame_OutOfRange(t *testing.T) {
	data := buildTestIVF([4]byte{'A', 'V', '0', '1'}, 16, 16, [][]byte{{0x01}})
	d, err := OpenData(data)
	if err != nil {
		t.Fatalf("OpenData: %v", err)
	}
	_, err = d.ReadFrame(5)
	if err == nil {
		t.Fatal("expected error for out-of-range index")
	}
	_, err = d.ReadFrame(-1)
	if err == nil {
		t.Fatal("expected error for negative index")
	}
}
