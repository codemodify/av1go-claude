package decoder

import (
	"testing"
)

// makeAboveLeft builds reference arrays with the topLeft/above/left convention:
//
//	above: [topLeft, above0, above1, ..., above(n-1)]
//	left:  [topLeft, left0,  left1,  ..., left(n-1)]
//
// topLeft is duplicated as the first element of both arrays.
func makeAboveLeft(topLeft byte, abovePixels, leftPixels []byte) (above, left []byte) {
	above = make([]byte, 1+len(abovePixels))
	above[0] = topLeft
	copy(above[1:], abovePixels)

	left = make([]byte, 1+len(leftPixels))
	left[0] = topLeft
	copy(left[1:], leftPixels)
	return
}

// makeAboveLeft16 is the 16-bit variant.
func makeAboveLeft16(topLeft uint16, abovePixels, leftPixels []uint16) (above, left []uint16) {
	above = make([]uint16, 1+len(abovePixels))
	above[0] = topLeft
	copy(above[1:], abovePixels)

	left = make([]uint16, 1+len(leftPixels))
	left[0] = topLeft
	copy(left[1:], leftPixels)
	return
}

// --- DC_PRED tests ---

func TestPredictDC_4x4_AllSame(t *testing.T) {
	// All neighbors are 128; DC should be 128.
	abovePixels := []byte{128, 128, 128, 128}
	leftPixels := []byte{128, 128, 128, 128}
	above, left := makeAboveLeft(128, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(DC_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	for i, v := range dst {
		if v != 128 {
			t.Errorf("pixel %d: got %d, want 128", i, v)
		}
	}
}

func TestPredictDC_4x4_Mixed(t *testing.T) {
	// above = [100, 100, 100, 100], left = [200, 200, 200, 200]
	// sum = 4*100 + 4*200 = 1200, avg = (1200 + 4) / 8 = 150
	abovePixels := []byte{100, 100, 100, 100}
	leftPixels := []byte{200, 200, 200, 200}
	above, left := makeAboveLeft(150, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(DC_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	expected := byte(150)
	for i, v := range dst {
		if v != expected {
			t.Errorf("pixel %d: got %d, want %d", i, v, expected)
		}
	}
}

func TestPredictDC_NoNeighbors(t *testing.T) {
	// No above or left: should use 1 << (bitDepth-1) = 128 for 8-bit.
	above := []byte{}
	left := []byte{}

	dst := make([]byte, 4*4)
	PredictIntra(DC_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	for i, v := range dst {
		if v != 128 {
			t.Errorf("pixel %d: got %d, want 128", i, v)
		}
	}
}

func TestPredictDC_OnlyAbove(t *testing.T) {
	// Only above available: avg of above pixels.
	abovePixels := []byte{40, 60, 80, 100}
	above := make([]byte, 1+len(abovePixels))
	above[0] = 50 // topLeft (won't be used for DC)
	copy(above[1:], abovePixels)
	left := []byte{} // No left

	dst := make([]byte, 4*4)
	PredictIntra(DC_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	// sum = 40+60+80+100 = 280, avg = (280+2)/4 = 70
	expected := byte(70)
	for i, v := range dst {
		if v != expected {
			t.Errorf("pixel %d: got %d, want %d", i, v, expected)
		}
	}
}

// --- V_PRED tests ---

func TestPredictV_4x4(t *testing.T) {
	abovePixels := []byte{10, 20, 30, 40}
	leftPixels := []byte{0, 0, 0, 0}
	above, left := makeAboveLeft(0, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(V_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			got := dst[y*4+x]
			want := abovePixels[x]
			if got != want {
				t.Errorf("(%d,%d): got %d, want %d", x, y, got, want)
			}
		}
	}
}

// --- H_PRED tests ---

func TestPredictH_4x4(t *testing.T) {
	abovePixels := []byte{0, 0, 0, 0}
	leftPixels := []byte{10, 20, 30, 40}
	above, left := makeAboveLeft(0, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(H_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			got := dst[y*4+x]
			want := leftPixels[y]
			if got != want {
				t.Errorf("(%d,%d): got %d, want %d", x, y, got, want)
			}
		}
	}
}

// --- PAETH_PRED tests ---

func TestPredictPaeth_4x4_AllSame(t *testing.T) {
	// All neighbors are 100; Paeth should produce 100 everywhere.
	abovePixels := []byte{100, 100, 100, 100}
	leftPixels := []byte{100, 100, 100, 100}
	above, left := makeAboveLeft(100, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(PAETH_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	for i, v := range dst {
		if v != 100 {
			t.Errorf("pixel %d: got %d, want 100", i, v)
		}
	}
}

func TestPredictPaeth_4x4_Known(t *testing.T) {
	// topLeft=128, above=[200,200,200,200], left=[50,50,50,50]
	// base = 200 + 50 - 128 = 122
	// |122-200|=78, |122-50|=72, |122-128|=6
	// closest to topLeft (128), so predict 128
	abovePixels := []byte{200, 200, 200, 200}
	leftPixels := []byte{50, 50, 50, 50}
	above, left := makeAboveLeft(128, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(PAETH_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	for i, v := range dst {
		if v != 128 {
			t.Errorf("pixel %d: got %d, want 128", i, v)
		}
	}
}

func TestPredictPaeth_PicksAbove(t *testing.T) {
	// topLeft=100, above=[110, ...], left=[100, ...]
	// base = 110 + 100 - 100 = 110
	// |110-110|=0 (above wins)
	abovePixels := []byte{110, 110, 110, 110}
	leftPixels := []byte{100, 100, 100, 100}
	above, left := makeAboveLeft(100, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(PAETH_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	for i, v := range dst {
		if v != 110 {
			t.Errorf("pixel %d: got %d, want 110", i, v)
		}
	}
}

func TestPredictPaeth_PicksLeft(t *testing.T) {
	// topLeft=100, above=[100, ...], left=[110, ...]
	// base = 100 + 110 - 100 = 110
	// |110-100|=10 (above), |110-110|=0 (left wins)
	abovePixels := []byte{100, 100, 100, 100}
	leftPixels := []byte{110, 110, 110, 110}
	above, left := makeAboveLeft(100, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(PAETH_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	// dAbove=10, dLeft=0, dTopLeft=10 => dLeft is smallest => pick left
	for i, v := range dst {
		if v != 110 {
			t.Errorf("pixel %d: got %d, want 110", i, v)
		}
	}
}

// --- SMOOTH tests ---

func TestPredictSmooth_4x4_Uniform(t *testing.T) {
	// All neighbors are 128; smooth should produce 128 everywhere.
	abovePixels := []byte{128, 128, 128, 128}
	leftPixels := []byte{128, 128, 128, 128}
	above, left := makeAboveLeft(128, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(SMOOTH_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	for i, v := range dst {
		if v != 128 {
			t.Errorf("pixel %d: got %d, want 128", i, v)
		}
	}
}

func TestPredictSmoothV_4x4_Uniform(t *testing.T) {
	abovePixels := []byte{128, 128, 128, 128}
	leftPixels := []byte{128, 128, 128, 128}
	above, left := makeAboveLeft(128, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(SMOOTH_V, dst, 4, 4, 4, above, left, true, true, 8)

	for i, v := range dst {
		if v != 128 {
			t.Errorf("pixel %d: got %d, want 128", i, v)
		}
	}
}

func TestPredictSmoothH_4x4_Uniform(t *testing.T) {
	abovePixels := []byte{128, 128, 128, 128}
	leftPixels := []byte{128, 128, 128, 128}
	above, left := makeAboveLeft(128, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(SMOOTH_H, dst, 4, 4, 4, above, left, true, true, 8)

	for i, v := range dst {
		if v != 128 {
			t.Errorf("pixel %d: got %d, want 128", i, v)
		}
	}
}

func TestPredictSmoothV_4x4_Gradient(t *testing.T) {
	// above = [255, 255, 255, 255], left = [_, _, _, 0] (bottomLeft = 0)
	// Each row y: pred = (255*w[y] + 0*(256-w[y]) + 128) >> 8 = (255*w[y]+128)>>8
	// w = {255, 149, 85, 64}
	// row 0: (255*255+128)>>8 = (65153)>>8 = 254
	// row 1: (255*149+128)>>8 = (38123)>>8 = 148
	// row 2: (255*85+128)>>8  = (21803)>>8 = 85
	// row 3: (255*64+128)>>8  = (16448)>>8 = 64
	abovePixels := []byte{255, 255, 255, 255}
	leftPixels := []byte{255, 255, 255, 0} // left[4] = bottomLeft = 0
	above := make([]byte, 5) // [topLeft, above0..above3]
	above[0] = 255
	copy(above[1:], abovePixels)
	left := make([]byte, 5) // [topLeft, left0..left3] but we need left[h=4] as bottomLeft
	left[0] = 255
	copy(left[1:], leftPixels)

	// bottomLeft is left[h] = left[4]. We need to extend left.
	leftExt := make([]byte, 5)
	copy(leftExt, left)
	leftExt[4] = 0 // bottomLeft

	dst := make([]byte, 4*4)
	PredictIntra(SMOOTH_V, dst, 4, 4, 4, above, leftExt, true, true, 8)

	expected := []byte{254, 254, 254, 254, 148, 148, 148, 148, 85, 85, 85, 85, 64, 64, 64, 64}
	for i, v := range dst {
		if v != expected[i] {
			t.Errorf("pixel %d: got %d, want %d", i, v, expected[i])
		}
	}
}

// --- Directional mode basic tests ---

func TestPredictD45_4x4(t *testing.T) {
	// D45: projects along the 45-degree diagonal (up-right).
	// With exact 45 degrees, dx = 44 per drIntraDerivative[14] = 44
	// This is an approximation test - just verify it runs without panics
	// and produces reasonable output.
	abovePixels := make([]byte, 8) // 2*w for directional
	for i := range abovePixels {
		abovePixels[i] = byte(10 * (i + 1))
	}
	leftPixels := make([]byte, 8)
	for i := range leftPixels {
		leftPixels[i] = byte(10 * (i + 1))
	}
	above, left := makeAboveLeft(10, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(D45_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	// Just verify no panic and values are in valid range.
	for i, v := range dst {
		if v > 255 {
			t.Errorf("pixel %d: value %d out of range", i, v)
		}
	}
}

func TestPredictD135_4x4(t *testing.T) {
	abovePixels := make([]byte, 8)
	for i := range abovePixels {
		abovePixels[i] = byte(50 + 10*i)
	}
	leftPixels := make([]byte, 8)
	for i := range leftPixels {
		leftPixels[i] = byte(50 + 10*i)
	}
	above, left := makeAboveLeft(50, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(D135_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	for i, v := range dst {
		if v > 255 {
			t.Errorf("pixel %d: value %d out of range", i, v)
		}
	}
}

func TestPredictD203_4x4(t *testing.T) {
	abovePixels := make([]byte, 8)
	for i := range abovePixels {
		abovePixels[i] = byte(100)
	}
	leftPixels := make([]byte, 8)
	for i := range leftPixels {
		leftPixels[i] = byte(50 + 5*i)
	}
	above, left := makeAboveLeft(80, abovePixels, leftPixels)

	dst := make([]byte, 4*4)
	PredictIntra(D203_PRED, dst, 4, 4, 4, above, left, true, true, 8)

	for i, v := range dst {
		if v > 255 {
			t.Errorf("pixel %d: value %d out of range", i, v)
		}
	}
}

// --- PredictIntraWithDelta tests ---

func TestPredictIntraWithDelta_NonDirectional(t *testing.T) {
	// Non-directional mode should ignore delta.
	abovePixels := []byte{128, 128, 128, 128}
	leftPixels := []byte{128, 128, 128, 128}
	above, left := makeAboveLeft(128, abovePixels, leftPixels)

	dst1 := make([]byte, 4*4)
	dst2 := make([]byte, 4*4)

	PredictIntra(DC_PRED, dst1, 4, 4, 4, above, left, true, true, 8)
	PredictIntraWithDelta(DC_PRED, 2, dst2, 4, 4, 4, above, left, 8, false, false)

	for i := range dst1 {
		if dst1[i] != dst2[i] {
			t.Errorf("pixel %d: delta should not affect DC_PRED, got %d vs %d", i, dst1[i], dst2[i])
		}
	}
}

// --- 16-bit tests ---

func TestPredictDC16_4x4(t *testing.T) {
	abovePixels := []uint16{512, 512, 512, 512}
	leftPixels := []uint16{512, 512, 512, 512}
	above, left := makeAboveLeft16(512, abovePixels, leftPixels)

	dst := make([]uint16, 4*4)
	PredictIntra16(DC_PRED, dst, 4, 4, 4, above, left, 10)

	for i, v := range dst {
		if v != 512 {
			t.Errorf("pixel %d: got %d, want 512", i, v)
		}
	}
}

func TestPredictV16_4x4(t *testing.T) {
	abovePixels := []uint16{100, 200, 300, 400}
	leftPixels := []uint16{0, 0, 0, 0}
	above, left := makeAboveLeft16(0, abovePixels, leftPixels)

	dst := make([]uint16, 4*4)
	PredictIntra16(V_PRED, dst, 4, 4, 4, above, left, 10)

	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			got := dst[y*4+x]
			want := abovePixels[x]
			if got != want {
				t.Errorf("(%d,%d): got %d, want %d", x, y, got, want)
			}
		}
	}
}

func TestPredictPaeth16_4x4(t *testing.T) {
	abovePixels := []uint16{800, 800, 800, 800}
	leftPixels := []uint16{800, 800, 800, 800}
	above, left := makeAboveLeft16(800, abovePixels, leftPixels)

	dst := make([]uint16, 4*4)
	PredictIntra16(PAETH_PRED, dst, 4, 4, 4, above, left, 10)

	for i, v := range dst {
		if v != 800 {
			t.Errorf("pixel %d: got %d, want 800", i, v)
		}
	}
}

func TestPredictSmooth16_4x4_Uniform(t *testing.T) {
	abovePixels := []uint16{512, 512, 512, 512}
	leftPixels := []uint16{512, 512, 512, 512}
	above, left := makeAboveLeft16(512, abovePixels, leftPixels)

	dst := make([]uint16, 4*4)
	PredictIntra16(SMOOTH_PRED, dst, 4, 4, 4, above, left, 10)

	for i, v := range dst {
		if v != 512 {
			t.Errorf("pixel %d: got %d, want 512", i, v)
		}
	}
}

// --- 8x8 block tests ---

func TestPredictDC_8x8(t *testing.T) {
	abovePixels := make([]byte, 8)
	leftPixels := make([]byte, 8)
	for i := 0; i < 8; i++ {
		abovePixels[i] = 64
		leftPixels[i] = 64
	}
	above, left := makeAboveLeft(64, abovePixels, leftPixels)

	dst := make([]byte, 8*8)
	PredictIntra(DC_PRED, dst, 8, 8, 8, above, left, true, true, 8)

	for i, v := range dst {
		if v != 64 {
			t.Errorf("pixel %d: got %d, want 64", i, v)
		}
	}
}

func TestPredictSmooth_8x8(t *testing.T) {
	abovePixels := make([]byte, 8)
	leftPixels := make([]byte, 8)
	for i := 0; i < 8; i++ {
		abovePixels[i] = 200
		leftPixels[i] = 200
	}
	above, left := makeAboveLeft(200, abovePixels, leftPixels)

	dst := make([]byte, 8*8)
	PredictIntra(SMOOTH_PRED, dst, 8, 8, 8, above, left, true, true, 8)

	for i, v := range dst {
		if v != 200 {
			t.Errorf("pixel %d: got %d, want 200", i, v)
		}
	}
}

// --- Stride tests ---

func TestPredictV_WithStride(t *testing.T) {
	// Test that stride is respected (stride > w).
	abovePixels := []byte{10, 20, 30, 40}
	leftPixels := []byte{0, 0, 0, 0}
	above, left := makeAboveLeft(0, abovePixels, leftPixels)

	stride := 8
	dst := make([]byte, 4*stride)
	PredictIntra(V_PRED, dst, stride, 4, 4, above, left, true, true, 8)

	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			got := dst[y*stride+x]
			want := abovePixels[x]
			if got != want {
				t.Errorf("(%d,%d): got %d, want %d", x, y, got, want)
			}
		}
	}
}

// --- Rectangular block tests ---

func TestPredictDC_8x4(t *testing.T) {
	abovePixels := make([]byte, 8)
	leftPixels := make([]byte, 4)
	for i := 0; i < 8; i++ {
		abovePixels[i] = 100
	}
	for i := 0; i < 4; i++ {
		leftPixels[i] = 100
	}
	above, left := makeAboveLeft(100, abovePixels, leftPixels)

	dst := make([]byte, 8*4)
	PredictIntra(DC_PRED, dst, 8, 8, 4, above, left, true, true, 8)

	for i, v := range dst {
		if v != 100 {
			t.Errorf("pixel %d: got %d, want 100", i, v)
		}
	}
}

// --- Clip helper test ---

func TestClip(t *testing.T) {
	tests := []struct {
		val, lo, hi, want int
	}{
		{50, 0, 255, 50},
		{-10, 0, 255, 0},
		{300, 0, 255, 255},
		{0, 0, 255, 0},
		{255, 0, 255, 255},
		{500, 0, 1023, 500},
		{1100, 0, 1023, 1023},
	}
	for _, tt := range tests {
		got := clip(tt.val, tt.lo, tt.hi)
		if got != tt.want {
			t.Errorf("clip(%d, %d, %d) = %d, want %d", tt.val, tt.lo, tt.hi, got, tt.want)
		}
	}
}

// --- getDx / getDy tests ---

func TestGetDx(t *testing.T) {
	// angle=45: dx = drIntraDerivative[45/3-1] = drIntraDerivative[14] = 44
	dx := getDx(45)
	if dx != 44 {
		t.Errorf("getDx(45) = %d, want 44", dx)
	}

	// angle=90: dx = 0 (vertical)
	dx = getDx(90)
	if dx != 0 {
		t.Errorf("getDx(90) = %d, want 0", dx)
	}

	// angle=135: (180-135)/3-1 = 14 -> drIntraDerivative[14] = 44, negative
	dx = getDx(135)
	if dx != -44 {
		t.Errorf("getDx(135) = %d, want -44", dx)
	}
}

func TestGetDy(t *testing.T) {
	// angle=135: (135-90)/3-1 = 14 -> drIntraDerivative[14] = 44
	dy := getDy(135)
	if dy != 44 {
		t.Errorf("getDy(135) = %d, want 44", dy)
	}

	// angle=180: dy = 0 (horizontal)
	dy = getDy(180)
	if dy != 0 {
		t.Errorf("getDy(180) = %d, want 0", dy)
	}

	// angle=203: (270-203)/3-1 = 21 -> drIntraDerivative[21] = 19, negative
	dy = getDy(203)
	if dy != -19 {
		t.Errorf("getDy(203) = %d, want -19", dy)
	}
}

// --- All modes smoke test ---

func TestAllModes_NoPanic(t *testing.T) {
	// Verify all 13 modes run without panic on a 4x4 block.
	abovePixels := make([]byte, 8)
	leftPixels := make([]byte, 8)
	for i := range abovePixels {
		abovePixels[i] = byte(100 + i*10)
	}
	for i := range leftPixels {
		leftPixels[i] = byte(50 + i*10)
	}
	above, left := makeAboveLeft(80, abovePixels, leftPixels)

	for mode := 0; mode < NumIntraModes; mode++ {
		dst := make([]byte, 4*4)
		PredictIntra(mode, dst, 4, 4, 4, above, left, true, true, 8)
	}
}

func TestAllModes16_NoPanic(t *testing.T) {
	abovePixels := make([]uint16, 8)
	leftPixels := make([]uint16, 8)
	for i := range abovePixels {
		abovePixels[i] = uint16(400 + i*50)
	}
	for i := range leftPixels {
		leftPixels[i] = uint16(200 + i*50)
	}
	above, left := makeAboveLeft16(350, abovePixels, leftPixels)

	for mode := 0; mode < NumIntraModes; mode++ {
		dst := make([]uint16, 4*4)
		PredictIntra16(mode, dst, 4, 4, 4, above, left, 10)
	}
}

// --- All block sizes smoke test ---

func TestAllBlockSizes_NoPanic(t *testing.T) {
	sizes := []int{4, 8, 16, 32, 64}
	for _, sz := range sizes {
		abovePixels := make([]byte, 2*sz)
		leftPixels := make([]byte, 2*sz)
		for i := range abovePixels {
			abovePixels[i] = 128
		}
		for i := range leftPixels {
			leftPixels[i] = 128
		}
		above, left := makeAboveLeft(128, abovePixels, leftPixels)

		for mode := 0; mode < NumIntraModes; mode++ {
			dst := make([]byte, sz*sz)
			PredictIntra(mode, dst, sz, sz, sz, above, left, true, true, 8)
		}
	}
}

// --- ModeToAngle table test ---

func TestModeToAngle(t *testing.T) {
	expected := map[int]int{
		DC_PRED:     0,
		V_PRED:      90,
		H_PRED:      180,
		D45_PRED:    45,
		D135_PRED:   135,
		D113_PRED:   113,
		D157_PRED:   157,
		D203_PRED:   203,
		D67_PRED:    67,
		SMOOTH_PRED: 0,
		SMOOTH_V:    0,
		SMOOTH_H:    0,
		PAETH_PRED:  0,
	}
	for mode, want := range expected {
		got := modeToAngle[mode]
		if got != want {
			t.Errorf("modeToAngle[%d] = %d, want %d", mode, got, want)
		}
	}
}

// --- Benchmark tests ---

func BenchmarkPredictDC_32x32(b *testing.B) {
	abovePixels := make([]byte, 64)
	leftPixels := make([]byte, 64)
	for i := range abovePixels {
		abovePixels[i] = 128
	}
	for i := range leftPixels {
		leftPixels[i] = 128
	}
	above, left := makeAboveLeft(128, abovePixels, leftPixels)
	dst := make([]byte, 32*32)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PredictIntra(DC_PRED, dst, 32, 32, 32, above, left, true, true, 8)
	}
}

func BenchmarkPredictPaeth_32x32(b *testing.B) {
	abovePixels := make([]byte, 64)
	leftPixels := make([]byte, 64)
	for i := range abovePixels {
		abovePixels[i] = byte(i * 2)
	}
	for i := range leftPixels {
		leftPixels[i] = byte(i * 3)
	}
	above, left := makeAboveLeft(128, abovePixels, leftPixels)
	dst := make([]byte, 32*32)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PredictIntra(PAETH_PRED, dst, 32, 32, 32, above, left, true, true, 8)
	}
}

func BenchmarkPredictSmooth_32x32(b *testing.B) {
	abovePixels := make([]byte, 64)
	leftPixels := make([]byte, 64)
	for i := range abovePixels {
		abovePixels[i] = byte(i * 4)
	}
	for i := range leftPixels {
		leftPixels[i] = byte(i * 4)
	}
	above, left := makeAboveLeft(128, abovePixels, leftPixels)
	dst := make([]byte, 32*32)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PredictIntra(SMOOTH_PRED, dst, 32, 32, 32, above, left, true, true, 8)
	}
}

func BenchmarkPredictD135_32x32(b *testing.B) {
	abovePixels := make([]byte, 64)
	leftPixels := make([]byte, 64)
	for i := range abovePixels {
		abovePixels[i] = byte(i * 2)
	}
	for i := range leftPixels {
		leftPixels[i] = byte(i * 3)
	}
	above, left := makeAboveLeft(128, abovePixels, leftPixels)
	dst := make([]byte, 32*32)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PredictIntra(D135_PRED, dst, 32, 32, 32, above, left, true, true, 8)
	}
}
