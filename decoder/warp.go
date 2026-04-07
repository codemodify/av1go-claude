// Package decoder implements AV1 bitstream decoding.
//
// This file implements warped motion prediction for inter frames.
// AV1 spec Section 7.11.3.5 (Warp Estimation) and Section 7.11.3.6
// (Block Warp Process). Includes the warp filter table, affine fitting,
// shear parameter extraction, and per-8x8 warp motion compensation.
package decoder

// warpFilter contains the 8-tap warp filter kernels.
// [193][8] — 193 entries covering fractional positions in [-1, 2) at 1/64 resolution.
// Index 64 = integer position (identity). Coefficients sum to 128.
// From dav1d tables.c dav1d_mc_warp_filter.
var warpFilter = [193][8]int16{
	// [-1, 0) — indices 0..63
	{0, 0, 127, 1, 0, 0, 0, 0},
	{0, -1, 127, 2, 0, 0, 0, 0},
	{1, -3, 127, 4, -1, 0, 0, 0},
	{1, -4, 126, 6, -2, 1, 0, 0},
	{1, -5, 126, 8, -3, 1, 0, 0},
	{1, -6, 125, 11, -4, 1, 0, 0},
	{1, -7, 124, 13, -4, 1, 0, 0},
	{2, -8, 123, 15, -5, 1, 0, 0},
	{2, -9, 122, 18, -6, 1, 0, 0},
	{2, -10, 121, 20, -6, 1, 0, 0},
	{2, -11, 120, 22, -7, 2, 0, 0},
	{2, -12, 119, 25, -8, 2, 0, 0},
	{3, -13, 117, 27, -8, 2, 0, 0},
	{3, -13, 116, 29, -9, 2, 0, 0},
	{3, -14, 114, 32, -10, 3, 0, 0},
	{3, -15, 113, 35, -10, 2, 0, 0},
	{3, -15, 111, 37, -11, 3, 0, 0},
	{3, -16, 109, 40, -11, 3, 0, 0},
	{3, -16, 108, 42, -12, 3, 0, 0},
	{4, -17, 106, 45, -13, 3, 0, 0},
	{4, -17, 104, 47, -13, 3, 0, 0},
	{4, -17, 102, 50, -14, 3, 0, 0},
	{4, -17, 100, 52, -14, 3, 0, 0},
	{4, -18, 98, 55, -15, 4, 0, 0},
	{4, -18, 96, 58, -15, 3, 0, 0},
	{4, -18, 94, 60, -16, 4, 0, 0},
	{4, -18, 91, 63, -16, 4, 0, 0},
	{4, -18, 89, 65, -16, 4, 0, 0},
	{4, -18, 87, 68, -17, 4, 0, 0},
	{4, -18, 85, 70, -17, 4, 0, 0},
	{4, -18, 82, 73, -17, 4, 0, 0},
	{4, -18, 80, 75, -17, 4, 0, 0},
	{4, -18, 78, 78, -18, 4, 0, 0},
	{4, -17, 75, 80, -18, 4, 0, 0},
	{4, -17, 73, 82, -18, 4, 0, 0},
	{4, -17, 70, 85, -18, 4, 0, 0},
	{4, -17, 68, 87, -18, 4, 0, 0},
	{4, -16, 65, 89, -18, 4, 0, 0},
	{4, -16, 63, 91, -18, 4, 0, 0},
	{4, -16, 60, 94, -18, 4, 0, 0},
	{3, -15, 58, 96, -18, 4, 0, 0},
	{4, -15, 55, 98, -18, 4, 0, 0},
	{3, -14, 52, 100, -17, 4, 0, 0},
	{3, -14, 50, 102, -17, 4, 0, 0},
	{3, -13, 47, 104, -17, 4, 0, 0},
	{3, -13, 45, 106, -17, 4, 0, 0},
	{3, -12, 42, 108, -16, 3, 0, 0},
	{3, -11, 40, 109, -16, 3, 0, 0},
	{3, -11, 37, 111, -15, 3, 0, 0},
	{2, -10, 35, 113, -15, 3, 0, 0},
	{3, -10, 32, 114, -14, 3, 0, 0},
	{2, -9, 29, 116, -13, 3, 0, 0},
	{2, -8, 27, 117, -13, 3, 0, 0},
	{2, -8, 25, 119, -12, 2, 0, 0},
	{2, -7, 22, 120, -11, 2, 0, 0},
	{1, -6, 20, 121, -10, 2, 0, 0},
	{1, -6, 18, 122, -9, 2, 0, 0},
	{1, -5, 15, 123, -8, 2, 0, 0},
	{1, -4, 13, 124, -7, 1, 0, 0},
	{1, -4, 11, 125, -6, 1, 0, 0},
	{1, -3, 8, 126, -5, 1, 0, 0},
	{1, -2, 6, 126, -4, 1, 0, 0},
	{0, -1, 4, 127, -3, 1, 0, 0},
	{0, 0, 2, 127, -1, 0, 0, 0},
	// [0, 1) — indices 64..127
	{0, 0, 0, 127, 1, 0, 0, 0},
	{0, 0, -1, 127, 2, 0, 0, 0},
	{0, 1, -3, 127, 4, -2, 1, 0},
	{0, 1, -5, 127, 6, -2, 1, 0},
	{0, 2, -6, 126, 8, -3, 1, 0},
	{-1, 2, -7, 126, 11, -4, 2, -1},
	{-1, 3, -8, 125, 13, -5, 2, -1},
	{-1, 3, -10, 124, 16, -6, 3, -1},
	{-1, 4, -11, 123, 18, -7, 3, -1},
	{-1, 4, -12, 122, 20, -7, 3, -1},
	{-1, 4, -13, 121, 23, -8, 3, -1},
	{-2, 5, -14, 120, 25, -9, 4, -1},
	{-1, 5, -15, 119, 27, -10, 4, -1},
	{-1, 5, -16, 118, 30, -11, 4, -1},
	{-2, 6, -17, 116, 33, -12, 5, -1},
	{-2, 6, -17, 114, 35, -12, 5, -1},
	{-2, 6, -18, 113, 38, -13, 5, -1},
	{-2, 7, -19, 111, 41, -14, 6, -2},
	{-2, 7, -19, 110, 43, -15, 6, -2},
	{-2, 7, -20, 108, 46, -15, 6, -2},
	{-2, 7, -20, 106, 49, -16, 6, -2},
	{-2, 7, -21, 104, 51, -16, 7, -2},
	{-2, 7, -21, 102, 54, -17, 7, -2},
	{-2, 8, -21, 100, 56, -18, 7, -2},
	{-2, 8, -22, 98, 59, -18, 7, -2},
	{-2, 8, -22, 96, 62, -19, 7, -2},
	{-2, 8, -22, 94, 64, -19, 7, -2},
	{-2, 8, -22, 91, 67, -20, 8, -2},
	{-2, 8, -22, 89, 69, -20, 8, -2},
	{-2, 8, -22, 87, 72, -21, 8, -2},
	{-2, 8, -21, 84, 74, -21, 8, -2},
	{-2, 8, -22, 82, 77, -21, 8, -2},
	{-2, 8, -21, 79, 79, -21, 8, -2},
	{-2, 8, -21, 77, 82, -22, 8, -2},
	{-2, 8, -21, 74, 84, -21, 8, -2},
	{-2, 8, -21, 72, 87, -22, 8, -2},
	{-2, 8, -20, 69, 89, -22, 8, -2},
	{-2, 8, -20, 67, 91, -22, 8, -2},
	{-2, 7, -19, 64, 94, -22, 8, -2},
	{-2, 7, -19, 62, 96, -22, 8, -2},
	{-2, 7, -18, 59, 98, -22, 8, -2},
	{-2, 7, -18, 56, 100, -21, 8, -2},
	{-2, 7, -17, 54, 102, -21, 7, -2},
	{-2, 7, -16, 51, 104, -21, 7, -2},
	{-2, 6, -16, 49, 106, -20, 7, -2},
	{-2, 6, -15, 46, 108, -20, 7, -2},
	{-2, 6, -15, 43, 110, -19, 7, -2},
	{-2, 6, -14, 41, 111, -19, 7, -2},
	{-1, 5, -13, 38, 113, -18, 6, -2},
	{-1, 5, -12, 35, 114, -17, 6, -2},
	{-1, 5, -12, 33, 116, -17, 6, -2},
	{-1, 4, -11, 30, 118, -16, 5, -1},
	{-1, 4, -10, 27, 119, -15, 5, -1},
	{-1, 4, -9, 25, 120, -14, 5, -2},
	{-1, 3, -8, 23, 121, -13, 4, -1},
	{-1, 3, -7, 20, 122, -12, 4, -1},
	{-1, 3, -7, 18, 123, -11, 4, -1},
	{-1, 3, -6, 16, 124, -10, 3, -1},
	{-1, 2, -5, 13, 125, -8, 3, -1},
	{-1, 2, -4, 11, 126, -7, 2, -1},
	{0, 1, -3, 8, 126, -6, 2, 0},
	{0, 1, -2, 6, 127, -5, 1, 0},
	{0, 1, -2, 4, 127, -3, 1, 0},
	{0, 0, 0, 2, 127, -1, 0, 0},
	// [1, 2) — indices 128..191
	{0, 0, 0, 1, 127, 0, 0, 0},
	{0, 0, 0, -1, 127, 2, 0, 0},
	{0, 0, 1, -3, 127, 4, -1, 0},
	{0, 0, 1, -4, 126, 6, -2, 1},
	{0, 0, 1, -5, 126, 8, -3, 1},
	{0, 0, 1, -6, 125, 11, -4, 1},
	{0, 0, 1, -7, 124, 13, -4, 1},
	{0, 0, 2, -8, 123, 15, -5, 1},
	{0, 0, 2, -9, 122, 18, -6, 1},
	{0, 0, 2, -10, 121, 20, -6, 1},
	{0, 0, 2, -11, 120, 22, -7, 2},
	{0, 0, 2, -12, 119, 25, -8, 2},
	{0, 0, 3, -13, 117, 27, -8, 2},
	{0, 0, 3, -13, 116, 29, -9, 2},
	{0, 0, 3, -14, 114, 32, -10, 3},
	{0, 0, 3, -15, 113, 35, -10, 2},
	{0, 0, 3, -15, 111, 37, -11, 3},
	{0, 0, 3, -16, 109, 40, -11, 3},
	{0, 0, 3, -16, 108, 42, -12, 3},
	{0, 0, 4, -17, 106, 45, -13, 3},
	{0, 0, 4, -17, 104, 47, -13, 3},
	{0, 0, 4, -17, 102, 50, -14, 3},
	{0, 0, 4, -17, 100, 52, -14, 3},
	{0, 0, 4, -18, 98, 55, -15, 4},
	{0, 0, 4, -18, 96, 58, -15, 3},
	{0, 0, 4, -18, 94, 60, -16, 4},
	{0, 0, 4, -18, 91, 63, -16, 4},
	{0, 0, 4, -18, 89, 65, -16, 4},
	{0, 0, 4, -18, 87, 68, -17, 4},
	{0, 0, 4, -18, 85, 70, -17, 4},
	{0, 0, 4, -18, 82, 73, -17, 4},
	{0, 0, 4, -18, 80, 75, -17, 4},
	{0, 0, 4, -18, 78, 78, -18, 4},
	{0, 0, 4, -17, 75, 80, -18, 4},
	{0, 0, 4, -17, 73, 82, -18, 4},
	{0, 0, 4, -17, 70, 85, -18, 4},
	{0, 0, 4, -17, 68, 87, -18, 4},
	{0, 0, 4, -16, 65, 89, -18, 4},
	{0, 0, 4, -16, 63, 91, -18, 4},
	{0, 0, 4, -16, 60, 94, -18, 4},
	{0, 0, 3, -15, 58, 96, -18, 4},
	{0, 0, 4, -15, 55, 98, -18, 4},
	{0, 0, 3, -14, 52, 100, -17, 4},
	{0, 0, 3, -14, 50, 102, -17, 4},
	{0, 0, 3, -13, 47, 104, -17, 4},
	{0, 0, 3, -13, 45, 106, -17, 4},
	{0, 0, 3, -12, 42, 108, -16, 3},
	{0, 0, 3, -11, 40, 109, -16, 3},
	{0, 0, 3, -11, 37, 111, -15, 3},
	{0, 0, 2, -10, 35, 113, -15, 3},
	{0, 0, 3, -10, 32, 114, -14, 3},
	{0, 0, 2, -9, 29, 116, -13, 3},
	{0, 0, 2, -8, 27, 117, -13, 3},
	{0, 0, 2, -8, 25, 119, -12, 2},
	{0, 0, 2, -7, 22, 120, -11, 2},
	{0, 0, 1, -6, 20, 121, -10, 2},
	{0, 0, 1, -6, 18, 122, -9, 2},
	{0, 0, 1, -5, 15, 123, -8, 2},
	{0, 0, 1, -4, 13, 124, -7, 1},
	{0, 0, 1, -4, 11, 125, -6, 1},
	{0, 0, 1, -3, 8, 126, -5, 1},
	{0, 0, 1, -2, 6, 126, -4, 1},
	{0, 0, 0, -1, 4, 127, -3, 1},
	{0, 0, 0, 0, 2, 127, -1, 0},
	// dummy entry [192] = copy of [191]
	{0, 0, 0, 0, 2, 127, -1, 0},
}

// divLut is the precomputed reciprocal table for affine fitting.
// From dav1d warpmv.c div_lut[257].
var divLut = [257]uint16{
	16384, 16320, 16257, 16194, 16132, 16070, 16009, 15948, 15888, 15828, 15768,
	15709, 15650, 15592, 15534, 15477, 15420, 15364, 15308, 15252, 15197, 15142,
	15087, 15033, 14980, 14926, 14873, 14821, 14769, 14717, 14665, 14614, 14564,
	14513, 14463, 14413, 14364, 14315, 14266, 14218, 14170, 14122, 14075, 14028,
	13981, 13935, 13888, 13843, 13797, 13752, 13707, 13662, 13618, 13574, 13530,
	13487, 13443, 13400, 13358, 13315, 13273, 13231, 13190, 13148, 13107, 13066,
	13026, 12985, 12945, 12906, 12866, 12827, 12788, 12749, 12710, 12672, 12633,
	12596, 12558, 12520, 12483, 12446, 12409, 12373, 12336, 12300, 12264, 12228,
	12193, 12157, 12122, 12087, 12053, 12018, 11984, 11950, 11916, 11882, 11848,
	11815, 11782, 11749, 11716, 11683, 11651, 11619, 11586, 11555, 11523, 11491,
	11460, 11429, 11398, 11367, 11336, 11305, 11275, 11245, 11215, 11185, 11155,
	11125, 11096, 11067, 11038, 11009, 10980, 10951, 10923, 10894, 10866, 10838,
	10810, 10782, 10755, 10727, 10700, 10673, 10645, 10618, 10592, 10565, 10538,
	10512, 10486, 10460, 10434, 10408, 10382, 10356, 10331, 10305, 10280, 10255,
	10230, 10205, 10180, 10156, 10131, 10107, 10082, 10058, 10034, 10010, 9986,
	9963, 9939, 9916, 9892, 9869, 9846, 9823, 9800, 9777, 9754, 9732,
	9709, 9687, 9664, 9642, 9620, 9598, 9576, 9554, 9533, 9511, 9489,
	9468, 9447, 9425, 9404, 9383, 9362, 9341, 9321, 9300, 9279, 9259,
	9239, 9218, 9198, 9178, 9158, 9138, 9118, 9098, 9079, 9059, 9039,
	9020, 9001, 8981, 8962, 8943, 8924, 8905, 8886, 8867, 8849, 8830,
	8812, 8793, 8775, 8756, 8738, 8720, 8702, 8684, 8666, 8648, 8630,
	8613, 8595, 8577, 8560, 8542, 8525, 8508, 8490, 8473, 8456, 8439,
	8422, 8405, 8389, 8372, 8355, 8339, 8322, 8306, 8289, 8273, 8257,
	8240, 8224, 8208, 8192,
}

// WarpParams holds the affine warp parameters for a block.
type WarpParams struct {
	Mat   [6]int32 // affine matrix: [0],[1]=translation, [2]-[5]=affine coefficients
	Alpha int      // shear parameter alpha (horizontal per-pixel increment)
	Beta  int      // shear parameter beta (horizontal per-row increment)
	Gamma int      // shear parameter gamma (vertical per-pixel increment)
	Delta int      // shear parameter delta (vertical per-row increment)
	Valid      bool // true if affine was successfully derived
	NumSamples int  // number of valid warp sample points after filtering
}

// iclipWMP clamps to int16 range and rounds to nearest multiple of 64.
func iclipWMP(v int) int {
	cv := clamp(v, -32768, 32767)
	sign := 1
	if cv < 0 {
		sign = -1
		cv = -cv
	}
	return sign * (((cv + 32) >> 6) << 6)
}

// applySign64 returns |v| with the sign of s (int64 version).
func applySign64(v int, s int64) int {
	if s < 0 {
		return -v
	}
	return v
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// ulog2 returns floor(log2(v)) for uint32.
func ulog2(v uint32) int {
	n := 0
	for v > 1 {
		v >>= 1
		n++
	}
	return n
}

// u64log2 returns floor(log2(v)) for uint64.
func u64log2(v uint64) int {
	n := 0
	for v > 1 {
		v >>= 1
		n++
	}
	return n
}

// resolveDivisor32 computes a fixed-point reciprocal of d.
func resolveDivisor32(d uint32) (idet int, shift int) {
	shift = ulog2(d)
	e := int(d) - (1 << shift)
	var f int
	if shift > 8 {
		f = (e + (1 << (shift - 9))) >> (shift - 8)
	} else {
		f = e << (8 - shift)
	}
	if f > 256 {
		f = 256
	}
	shift += 14
	return int(divLut[f]), shift
}

// resolveDivisor64 computes a fixed-point reciprocal of d (64-bit version).
func resolveDivisor64(d uint64) (idet int, shift int) {
	shift = u64log2(d)
	e := int64(d) - (1 << shift)
	var f int64
	if shift > 8 {
		f = (e + (1 << (shift - 9))) >> (shift - 8)
	} else {
		f = e << (8 - shift)
	}
	if f > 256 {
		f = 256
	}
	shift += 14
	return int(divLut[f]), shift
}

func getMultShiftNdiag(px int64, idet int, shift int) int {
	v1 := px * int64(idet)
	rnd := int64(1<<shift) >> 1
	v2 := applySign64(int((abs64(v1)+rnd)>>shift), v1)
	return clamp(v2, -0x1fff, 0x1fff)
}

func getMultShiftDiag(px int64, idet int, shift int) int {
	v1 := px * int64(idet)
	rnd := int64(1<<shift) >> 1
	v2 := applySign64(int((abs64(v1)+rnd)>>shift), v1)
	return clamp(v2, 0xe001, 0x11fff)
}

// findAffineInt solves a least-squares affine fit from up to 8 sample points.
// pts[i][0] = source (dx, dy), pts[i][1] = destination (sx, sy).
// Returns false on failure.
func findAffineInt(pts [][2][2]int, np int, bw4, bh4 int, mv MV, bx4, by4 int, wm *WarpParams) bool {
	rsuy := 2*bh4 - 1
	rsux := 2*bw4 - 1
	suy := rsuy * 8
	sux := rsux * 8
	duy := suy + int(mv.Row)
	dux := sux + int(mv.Col)
	isuy := by4*4 + rsuy
	isux := bx4*4 + rsux

	var a [2][2]int
	var bx, by [2]int

	for i := 0; i < np; i++ {
		dx := pts[i][1][0] - dux
		dy := pts[i][1][1] - duy
		sx := pts[i][0][0] - sux
		sy := pts[i][0][1] - suy
		if absInt(sx-dx) < 256 && absInt(sy-dy) < 256 {
			a[0][0] += ((sx * sx) >> 2) + sx*2 + 8
			a[0][1] += ((sx * sy) >> 2) + sx + sy + 4
			a[1][1] += ((sy * sy) >> 2) + sy*2 + 8
			bx[0] += ((sx * dx) >> 2) + sx + dx + 8
			bx[1] += ((sy * dx) >> 2) + sy + dx + 4
			by[0] += ((sx * dy) >> 2) + sx + dy + 4
			by[1] += ((sy * dy) >> 2) + sy + dy + 8
		}
	}

	det := int64(a[0][0])*int64(a[1][1]) - int64(a[0][1])*int64(a[0][1])
	if det == 0 {
		return false
	}

	idet, shift := resolveDivisor64(uint64(abs64(det)))
	if det < 0 {
		idet = -idet
	}
	shift -= 16
	if shift < 0 {
		idet <<= (-shift)
		shift = 0
	}

	wm.Mat[2] = int32(getMultShiftDiag(int64(a[1][1])*int64(bx[0])-int64(a[0][1])*int64(bx[1]), idet, shift))
	wm.Mat[3] = int32(getMultShiftNdiag(int64(a[0][0])*int64(bx[1])-int64(a[0][1])*int64(bx[0]), idet, shift))
	wm.Mat[4] = int32(getMultShiftNdiag(int64(a[1][1])*int64(by[0])-int64(a[0][1])*int64(by[1]), idet, shift))
	wm.Mat[5] = int32(getMultShiftDiag(int64(a[0][0])*int64(by[1])-int64(a[0][1])*int64(by[0]), idet, shift))

	wm.Mat[0] = int32(clamp(int(mv.Col)*0x2000-(isux*(int(wm.Mat[2])-0x10000)+isuy*int(wm.Mat[3])), -0x800000, 0x7fffff))
	wm.Mat[1] = int32(clamp(int(mv.Row)*0x2000-(isux*int(wm.Mat[4])+isuy*(int(wm.Mat[5])-0x10000)), -0x800000, 0x7fffff))

	return true
}

// getShearParams validates the affine matrix and extracts alpha/beta/gamma/delta.
// Returns false if shear params are invalid (block should fall back to identity).
func getShearParams(wm *WarpParams) bool {
	mat := wm.Mat

	if mat[2] <= 0 {
		return false
	}

	wm.Alpha = iclipWMP(int(mat[2]) - 0x10000)
	wm.Beta = iclipWMP(int(mat[3]))

	idet, shift := resolveDivisor32(uint32(absInt(int(mat[2]))))
	y := applySign(idet, int(mat[2]))
	v1 := int64(mat[4]) * 0x10000 * int64(y)
	rnd := int64(1<<shift) >> 1
	wm.Gamma = iclipWMP(applySign64(int((abs64(v1)+rnd)>>shift), v1))
	v2 := int64(mat[3]) * int64(mat[4]) * int64(y)
	wm.Delta = iclipWMP(int(mat[5]) - applySign64(int((abs64(v2)+rnd)>>shift), v2) - 0x10000)

	if 4*absInt(wm.Alpha)+7*absInt(wm.Beta) >= 0x10000 {
		return false
	}
	if 4*absInt(wm.Gamma)+4*absInt(wm.Delta) >= 0x10000 {
		return false
	}
	return true
}

// findMatchingRefMasks scans neighbors to find matching-reference blocks.
// Returns masks[0] (top edge bitmask), masks[1] (left edge, top-left in bit 32, etc.)
// Matches dav1d find_matching_ref().
func (td *TileDecoder) findMatchingRefMasks(miRow, miCol, bW, bH int, ref0 int8, edgeFlags uint8) [2]uint64 {
	var masks [2]uint64
	count := 0
	haveTop := miRow > td.tileRowStart
	haveLeft := miCol > td.tileColStart
	haveTopLeft := haveTop && haveLeft
	haveTopRight := max(bW, bH) < 32 && haveTop && miCol+bW < td.tileColEnd &&
		(edgeFlags&EdgeI444TopHasRight) != 0

	w4 := bW
	if miCol+bW > td.tileColEnd {
		w4 = td.tileColEnd - miCol
	}
	h4 := bH
	if miRow+bH > td.tileRowEnd {
		h4 = td.tileRowEnd - miRow
	}

	// dav1d matches(rp): rp->ref.ref[0] == ref+1 && rp->ref.ref[1] == -1.
	// Interintra blocks store ref[1]=0 in dav1d's refmvs (not -1), so they
	// are excluded. We replicate this by also checking IIType == 0.
	matches := func(info ModeInfo) bool {
		return info.RefFrame[0] == ref0 && info.RefFrame[1] == -1 && info.IIType == 0
	}

	// Top edge
	if haveTop {
		c := miCol
		if c < len(td.aboveModeInfo) {
			info := td.aboveModeInfo[c]
			if matches(info) {
				masks[0] |= 1
				count++
			}
			aw4 := int(info.BW4)
			if aw4 < 1 {
				aw4 = 1
			}
			if aw4 >= bW {
				off := miCol & (aw4 - 1)
				if off != 0 {
					haveTopLeft = false
				}
				if aw4-off > bW {
					haveTopRight = false
				}
			} else {
				mask := uint64(1) << aw4
				for x := aw4; x < w4 && count < 8; x += aw4 {
					idx := c + x
					if idx >= len(td.aboveModeInfo) {
						break
					}
					info = td.aboveModeInfo[idx]
					if matches(info) {
						masks[0] |= mask
						count++
					}
					aw4 = int(info.BW4)
					if aw4 < 1 {
						aw4 = 1
					}
					mask <<= aw4
				}
			}
		}
	}

	// Left edge
	if haveLeft && count < 8 {
		r := miRow
		if r < len(td.leftModeInfo) {
			info := td.leftModeInfo[r]
			if matches(info) {
				masks[1] |= 1
				count++
			}
			lh4 := int(info.BH4)
			if lh4 < 1 {
				lh4 = 1
			}
			if lh4 >= bH {
				if miRow&(lh4-1) != 0 {
					haveTopLeft = false
				}
			} else if count < 8 {
				mask := uint64(1) << lh4
				for y := lh4; y < h4 && count < 8; y += lh4 {
					idx := r + y
					if idx >= len(td.leftModeInfo) {
						break
					}
					info = td.leftModeInfo[idx]
					if matches(info) {
						masks[1] |= mask
						count++
					}
					lh4 = int(info.BH4)
					if lh4 < 1 {
						lh4 = 1
					}
					mask <<= lh4
				}
			}
		}
	}

	// Top-left corner: use miGrid to avoid stale context array data
	// (aboveModeInfo[miCol-1] can be overwritten by same-row blocks).
	if haveTopLeft && count < 8 && td.miGrid != nil {
		if matches(td.miGrid.Get(miRow-1, miCol-1)) {
			masks[1] |= 1 << 32
			count++
		}
	}

	// Top-right corner: use miGrid for same reason.
	if haveTopRight && count < 8 && td.miGrid != nil {
		if matches(td.miGrid.Get(miRow-1, miCol+bW)) {
			masks[0] |= 1 << 32
		}
	}

	return masks
}

// deriveWarpMV collects warp samples from neighbors and derives affine parameters.
// Matches dav1d derive_warpmv().
func (td *TileDecoder) deriveWarpMV(miRow, miCol, bW, bH int, masks [2]uint64, mv MV) WarpParams {
	var pts [][2][2]int // [i][0]=source, [1]=dest, each [x,y]

	// Helper: add a sample point from a neighbor block.
	addSample := func(dx, dy, sx, sy int, info ModeInfo) {
		nw := int(info.BW4)
		nh := int(info.BH4)
		if nw < 1 {
			nw = 1
		}
		if nh < 1 {
			nh = 1
		}
		srcX := 16*(2*dx+sx*nw) - 8
		srcY := 16*(2*dy+sy*nh) - 8
		dstX := srcX + int(info.MV[0].Col)
		dstY := srcY + int(info.MV[0].Row)
		pts = append(pts, [2][2]int{{srcX, srcY}, {dstX, dstY}})
	}

	// Use the 2D miGrid for sample point lookups, since the 1D above/left
	// context arrays can be overwritten by blocks in the current SB row.
	gridGet := func(r, c int) ModeInfo {
		if td.miGrid != nil {
			return td.miGrid.Get(r, c)
		}
		return ModeInfo{IsIntra: true, RefFrame: [2]int8{-1, -1}}
	}

	// Collect from top edge
	if uint32(masks[0]) == 1 && (masks[1]>>32) == 0 {
		// Single top block covers entire width
		info := gridGet(miRow-1, miCol)
		nw := int(info.BW4)
		if nw < 1 {
			nw = 1
		}
		off := miCol & (nw - 1)
		addSample(-off, 0, 1, -1, info)
	} else {
		off := 0
		xmask := uint32(masks[0])
		for len(pts) < 8 && xmask != 0 {
			tz := ctz32(xmask)
			off += tz
			xmask >>= tz
			addSample(off, 0, 1, -1, gridGet(miRow-1, miCol+off))
			xmask &= ^uint32(1)
		}
	}

	// Collect from left edge
	if len(pts) < 8 && masks[1] == 1 {
		// Single left block covers entire height
		info := gridGet(miRow, miCol-1)
		nh := int(info.BH4)
		if nh < 1 {
			nh = 1
		}
		off := miRow & (nh - 1)
		addSample(0, -off, -1, 1, gridGet(miRow-off, miCol-1))
	} else {
		off := 0
		ymask := uint32(masks[1])
		for len(pts) < 8 && ymask != 0 {
			tz := ctz32(ymask)
			off += tz
			ymask >>= tz
			addSample(0, off, -1, 1, gridGet(miRow+off, miCol-1))
			ymask &= ^uint32(1)
		}
	}

	// Top-left corner (bit 32 of masks[1])
	if len(pts) < 8 && (masks[1]>>32) != 0 {
		addSample(0, 0, -1, -1, gridGet(miRow-1, miCol-1))
	}

	// Top-right corner (bit 32 of masks[0])
	if len(pts) < 8 && (masks[0]>>32) != 0 {
		addSample(bW, 0, 1, -1, gridGet(miRow-1, miCol+bW))
	}

	if len(pts) == 0 {
		return WarpParams{}
	}

	// Threshold-filter outliers
	thresh := 4 * clamp(max(bW, bH), 4, 28)
	np := len(pts)
	mvd := make([]int, np)
	ret := 0
	for i := 0; i < np; i++ {
		mvd[i] = absInt(pts[i][1][0]-pts[i][0][0]-int(mv.Col)) +
			absInt(pts[i][1][1]-pts[i][0][1]-int(mv.Row))
		if mvd[i] > thresh {
			mvd[i] = -1
		} else {
			ret++
		}
	}

	if ret == 0 {
		ret = 1
	} else {
		// Compact: move valid samples to the front
		i, j := 0, np-1
		for k := 0; k < np-ret; k++ {
			for i < np && mvd[i] != -1 {
				i++
			}
			for j >= 0 && mvd[j] == -1 {
				j--
			}
			if i >= j {
				break
			}
			mvd[i] = mvd[j]
			pts[i] = pts[j]
			i++
			j--
		}
	}

	var wm WarpParams
	wm.NumSamples = ret
	if findAffineInt(pts, ret, bW, bH, mv, miCol, miRow, &wm) && getShearParams(&wm) {
		wm.Valid = true
	}
	return wm
}

// ctz32 returns count of trailing zeros in a uint32.
func ctz32(v uint32) int {
	if v == 0 {
		return 32
	}
	n := 0
	for v&1 == 0 {
		v >>= 1
		n++
	}
	return n
}

// warpAffine8x8 performs warped motion compensation for one 8x8 block.
// dst is 8x8 output, refBuf is the reference plane, alpha/beta/gamma/delta
// are the shear parameters. mx/my are the initial fractional positions.
// dx/dy are the integer reference positions.
func warpAffine8x8(dst []byte, dstStride int,
	refBuf []byte, refStride, refW, refH int,
	dx, dy, mx, my, alpha, beta, gamma, delta int) {

	// Build edge-padded 15x15 source block (centered at dx-3, dy-3).
	var src [15 * 15]int16
	for sy := 0; sy < 15; sy++ {
		ry := clampInt(dy-3+sy, 0, refH-1)
		for sx := 0; sx < 15; sx++ {
			rx := clampInt(dx-3+sx, 0, refW-1)
			src[sy*15+sx] = int16(refBuf[ry*refStride+rx])
		}
	}

	// Horizontal pass: 15 rows x 8 columns, using warp filter.
	// Shift = 3 (for 8-bit: 7 - intermediate_bits = 7 - 4 = 3).
	var mid [15 * 8]int16
	tmx := mx
	for y := 0; y < 15; y++ {
		rowTmx := tmx
		for x := 0; x < 8; x++ {
			// Filter index: 64 + ((tmx + 512) >> 10), clamped to [0, 192].
			idx := 64 + ((rowTmx + 512) >> 10)
			if idx < 0 {
				idx = 0
			} else if idx > 192 {
				idx = 192
			}
			f := &warpFilter[idx]
			// 8-tap filter applied horizontally. Source is at src[y*15 + (x+3) + tap_offset].
			// The center of the filter (tap 3) maps to src position (x+3).
			var sum int32
			for t := 0; t < 8; t++ {
				sum += int32(f[t]) * int32(src[y*15+(x+t)])
			}
			mid[y*8+x] = int16((sum + 4) >> 3)
			rowTmx += alpha
		}
		tmx += beta
	}

	// Vertical pass: 8 rows x 8 columns, using warp filter.
	// Shift = 11 (for 8-bit: 7 + intermediate_bits = 7 + 4 = 11).
	tmy := my
	for y := 0; y < 8; y++ {
		rowTmy := tmy
		for x := 0; x < 8; x++ {
			idx := 64 + ((rowTmy + 512) >> 10)
			if idx < 0 {
				idx = 0
			} else if idx > 192 {
				idx = 192
			}
			f := &warpFilter[idx]
			// Vertical filter: center is at mid[(y+3)*8 + x].
			var sum int32
			for t := 0; t < 8; t++ {
				sum += int32(f[t]) * int32(mid[(y+t)*8+x])
			}
			dst[y*dstStride+x] = clipU8((sum + 1024) >> 11)
			rowTmy += gamma
		}
		tmy += delta
	}

}

// warpAffine8x8Prep is the compound-prediction variant of warpAffine8x8.
// Outputs intermediate-precision int16 values (like motionCompensationPrep)
// instead of clipped bytes. Matches dav1d's warp_affine_8x8t_c.
//
// For 8-bit: H pass shift = 3 (same as put), V pass shift = 7 (vs 11 for put).
// V pass rnd = 64 (1 << 6). No PREP_BIAS for 8-bit.
func warpAffine8x8Prep(dst []int16, dstStride int,
	refBuf []byte, refStride, refW, refH int,
	dx, dy, mx, my, alpha, beta, gamma, delta int) {

	// Build edge-padded 15x15 source block.
	var src [15 * 15]int16
	for sy := 0; sy < 15; sy++ {
		ry := clampInt(dy-3+sy, 0, refH-1)
		for sx := 0; sx < 15; sx++ {
			rx := clampInt(dx-3+sx, 0, refW-1)
			src[sy*15+sx] = int16(refBuf[ry*refStride+rx])
		}
	}

	// Horizontal pass: same as put variant.
	var mid [15 * 8]int16
	tmx := mx
	for y := 0; y < 15; y++ {
		rowTmx := tmx
		for x := 0; x < 8; x++ {
			idx := 64 + ((rowTmx + 512) >> 10)
			if idx < 0 {
				idx = 0
			} else if idx > 192 {
				idx = 192
			}
			f := &warpFilter[idx]
			var sum int32
			for t := 0; t < 8; t++ {
				sum += int32(f[t]) * int32(src[y*15+(x+t)])
			}
			mid[y*8+x] = int16((sum + 4) >> 3)
			rowTmx += alpha
		}
		tmx += beta
	}

	// Vertical pass: prep variant uses shift=7, rnd=64.
	tmy := my
	for y := 0; y < 8; y++ {
		rowTmy := tmy
		for x := 0; x < 8; x++ {
			idx := 64 + ((rowTmy + 512) >> 10)
			if idx < 0 {
				idx = 0
			} else if idx > 192 {
				idx = 192
			}
			f := &warpFilter[idx]
			var sum int32
			for t := 0; t < 8; t++ {
				sum += int32(f[t]) * int32(mid[(y+t)*8+x])
			}
			dst[y*dstStride+x] = int16((sum + 64) >> 7)
			rowTmy += gamma
		}
		tmy += delta
	}
}

// applyWarpedMotionPrep outputs intermediate-precision int16 values
// for compound prediction. Used when GLOBALMV_GLOBALMV with global motion warp.
// Matches dav1d's warp_affine path for compound (dst16 != NULL).
func applyWarpedMotionPrep(dst []int16, nomPixW, nomPixH int,
	refBuf []byte, refStride, refW, refH int,
	miCol, miRow int, wm *WarpParams) {

	mat := wm.Mat
	alpha := wm.Alpha
	beta := wm.Beta
	gamma := wm.Gamma
	delta := wm.Delta

	for y := 0; y < nomPixH; y += 8 {
		srcY := miRow*4 + (y + 4)
		mat3y := int64(mat[3])*int64(srcY) + int64(mat[0])
		for x := 0; x < nomPixW; x += 8 {
			srcX := miCol*4 + (x + 4)
			mvx := int64(mat[2])*int64(srcX) + mat3y
			mvy := int64(mat[4])*int64(srcX) + int64(mat[5])*int64(srcY) + int64(mat[1])

			dx := int(mvx>>16) - 4
			mx := (int(mvx&0xffff) - alpha*4 - beta*7) & ^0x3f
			dy := int(mvy>>16) - 4
			my := (int(mvy&0xffff) - gamma*4 - delta*4) & ^0x3f

			warpAffine8x8Prep(dst[y*nomPixW+x:], nomPixW,
				refBuf, refStride, refW, refH,
				dx, dy, mx, my, alpha, beta, gamma, delta)
		}
	}
}

// applyWarpedMotion replaces the prediction buffer with warped motion compensation.
// Called when motionMode==2 and warp type is AFFINE.
func applyWarpedMotion(predY []byte, nomPixW, nomPixH int,
	refBuf []byte, refStride, refW, refH int,
	miCol, miRow int, wm *WarpParams,
	predU, predV []byte, nomChromaW, nomChromaH int,
	refU []byte, refStrideU int,
	refV []byte, refStrideV int,
	chromaRefW, chromaRefH, subX, subY int) {

	mat := wm.Mat
	alpha := wm.Alpha
	beta := wm.Beta
	gamma := wm.Gamma
	delta := wm.Delta

	// Luma: process in 8x8 blocks
	for y := 0; y < nomPixH; y += 8 {
		srcY := miRow*4 + (y + 4) // ss_hor=0, ss_ver=0 for luma
		mat3y := int64(mat[3])*int64(srcY) + int64(mat[0])
		for x := 0; x < nomPixW; x += 8 {
			srcX := miCol*4 + (x + 4)
			mvx := int64(mat[2])*int64(srcX) + mat3y
			mvy := int64(mat[4])*int64(srcX) + int64(mat[5])*int64(srcY) + int64(mat[1])

			dx := int(mvx>>16) - 4
			mx := (int(mvx&0xffff) - alpha*4 - beta*7) & ^0x3f
			dy := int(mvy>>16) - 4
			my := (int(mvy&0xffff) - gamma*4 - delta*4) & ^0x3f

			warpAffine8x8(predY[y*nomPixW+x:], nomPixW,
				refBuf, refStride, refW, refH,
				dx, dy, mx, my, alpha, beta, gamma, delta)
		}
	}

	// Chroma: process in 8x8 blocks (in chroma pixel coordinates)
	if predU == nil || predV == nil {
		return
	}

	for y := 0; y < nomChromaH; y += 8 {
		srcY := miRow*4 + ((y+4)<<subY)
		mat3y := int64(mat[3])*int64(srcY) + int64(mat[0])
		mat5y := int64(mat[5])*int64(srcY) + int64(mat[1])
		for x := 0; x < nomChromaW; x += 8 {
			srcX := miCol*4 + ((x+4)<<subX)
			mvx := (int64(mat[2])*int64(srcX) + mat3y) >> subX
			mvy := (int64(mat[4])*int64(srcX) + mat5y) >> subY

			dx := int(mvx>>16) - 4
			mx := (int(mvx&0xffff) - alpha*4 - beta*7) & ^0x3f
			dy := int(mvy>>16) - 4
			my := (int(mvy&0xffff) - gamma*4 - delta*4) & ^0x3f

			// Handle blocks that may be < 8 pixels wide/tall at frame edges
			outW := 8
			if x+8 > nomChromaW {
				outW = nomChromaW - x
			}
			outH := 8
			if y+8 > nomChromaH {
				outH = nomChromaH - y
			}

			if outW == 8 && outH == 8 {
				warpAffine8x8(predU[y*nomChromaW+x:], nomChromaW,
					refU, refStrideU, chromaRefW, chromaRefH,
					dx, dy, mx, my, alpha, beta, gamma, delta)
				warpAffine8x8(predV[y*nomChromaW+x:], nomChromaW,
					refV, refStrideV, chromaRefW, chromaRefH,
					dx, dy, mx, my, alpha, beta, gamma, delta)
			} else {
				// Partial 8x8 block: warp into temp then copy
				var tmpU, tmpV [64]byte
				warpAffine8x8(tmpU[:], 8,
					refU, refStrideU, chromaRefW, chromaRefH,
					dx, dy, mx, my, alpha, beta, gamma, delta)
				warpAffine8x8(tmpV[:], 8,
					refV, refStrideV, chromaRefW, chromaRefH,
					dx, dy, mx, my, alpha, beta, gamma, delta)
				for r := 0; r < outH; r++ {
					for c := 0; c < outW; c++ {
						predU[(y+r)*nomChromaW+(x+c)] = tmpU[r*8+c]
						predV[(y+r)*nomChromaW+(x+c)] = tmpV[r*8+c]
					}
				}
			}

		}
	}
}
