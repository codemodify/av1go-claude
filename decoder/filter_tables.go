// Package decoder implements AV1 bitstream decoding.
//
// This file provides shared constants, lookup tables, and utility functions
// used by the three AV1 in-loop filters: deblocking, CDEF, and loop
// restoration. Tables are derived from the AV1 specification and dav1d
// reference implementation.
//
// AV1 spec Sections 7.14 (Deblocking), 7.15 (CDEF), 7.17 (Loop Restoration).
package decoder

// ---------------------------------------------------------------------------
// Deblocking filter lookup tables
// ---------------------------------------------------------------------------

// deblockLUT holds precomputed E, I, H threshold values indexed by filter
// level [0..63]. Built at init time from the sharpness parameter.
// Matches dav1d Av1FilterLUT (lf_mask.c dav1d_calc_eih).
type deblockLUT struct {
	e [64]int // outer threshold (E)
	i [64]int // inner threshold (I)
}

// buildDeblockLUT builds E/I tables for a given sharpness value.
// H (hev threshold) is derived on-the-fly as level >> 4.
// AV1 spec Section 7.14.3; dav1d lf_mask.c:dav1d_calc_eih.
func buildDeblockLUT(sharpness int) deblockLUT {
	var lut deblockLUT
	for level := 0; level < 64; level++ {
		limit := level
		if sharpness > 0 {
			limit >>= (sharpness + 3) >> 2
			limit = filterMin(limit, 9-sharpness)
		}
		limit = filterMax(limit, 1)
		lut.i[level] = limit
		lut.e[level] = 2*(level+2) + limit
	}
	return lut
}

// ---------------------------------------------------------------------------
// CDEF direction table
// ---------------------------------------------------------------------------

// cdefDirections encodes the pixel offset for each of the 8 CDEF directions
// at two tap distances (k=0, k=1). Offsets are relative to a padded 12-wide
// temporary buffer (tmp_stride = 12).
//
// The array is [12] entries = 2 prefix + 8 directions + 2 suffix to allow
// wrapping when accessing dir-2 and dir+2. Matches dav1d_cdef_directions
// (tables.c).
var cdefDirectionOffsets = [12][2]int{
	{1*12 + 0, 2*12 + 0},   // dir 6 (prefix)
	{1*12 + 0, 2*12 - 1},   // dir 7 (prefix)
	{-1*12 + 1, -2*12 + 2}, // dir 0: 45 deg
	{0*12 + 1, -1*12 + 2},  // dir 1: ~63 deg
	{0*12 + 1, 0*12 + 2},   // dir 2: horizontal
	{0*12 + 1, 1*12 + 2},   // dir 3: ~117 deg
	{1*12 + 1, 2*12 + 2},   // dir 4: 135 deg
	{1*12 + 0, 2*12 + 1},   // dir 5: ~153 deg
	{1*12 + 0, 2*12 + 0},   // dir 6: vertical
	{1*12 + 0, 2*12 - 1},   // dir 7: ~207 deg
	{-1*12 + 1, -2*12 + 2}, // dir 0 (suffix)
	{0*12 + 1, -1*12 + 2},  // dir 1 (suffix)
}

// cdefDivTable is used in direction finding cost computation.
// Matches dav1d cdef_find_dir_c's div_table.
var cdefDivTable = [7]int{840, 420, 280, 210, 168, 140, 120}

// ---------------------------------------------------------------------------
// SGR (Self-Guided Restoration) tables
// ---------------------------------------------------------------------------

// sgrprojXByX maps z in [0..255] to x for the SGR guided filter.
// The mapping is NOT simple round(256/(z+1)); it uses a specific formula
// from the AV1 spec. Values match dav1d_sgr_x_by_x (tables.c).
var sgrprojXByX = [256]uint8{
	255, 128, 85, 64, 51, 43, 37, 32, 28, 26, 23, 21, 20, 18, 17,
	16, 15, 14, 13, 13, 12, 12, 11, 11, 10, 10, 9, 9, 9, 9,
	8, 8, 8, 8, 7, 7, 7, 7, 7, 6, 6, 6, 6, 6, 6,
	6, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 4, 4, 4, 4,
	4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 3, 3,
	3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3,
	3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 2, 2, 2,
	2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
	2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
	2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
	2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
	2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	0,
}

// sgrprojFilterParams holds [eps0, eps1] for each of the 16 SGR parameter
// sets. A zero eps means that filter pass is disabled.
// Matches dav1d_sgr_params (tables.c).
var sgrprojFilterParams = [16][2]int{
	{140, 3236}, {112, 2158}, {93, 1618}, {80, 1438},
	{70, 1295}, {58, 1177}, {47, 1079}, {37, 996},
	{30, 925}, {25, 863}, {0, 2589}, {0, 1618},
	{0, 1177}, {0, 925}, {56, 0}, {22, 0},
}

// sgrprojRadii maps SGR parameter index to [radius0, radius1].
// radius0=2 when eps0 != 0, radius1=1 when eps1 != 0.
func sgrprojRadii(idx int) (r0, r1 int) {
	if sgrprojFilterParams[idx][0] != 0 {
		r0 = 2
	}
	if sgrprojFilterParams[idx][1] != 0 {
		r1 = 1
	}
	return
}

// ---------------------------------------------------------------------------
// Utility functions (private to this file's scope, avoid redeclaration
// conflicts with existing helpers in the package).
// ---------------------------------------------------------------------------

// filterClamp restricts val to [lo, hi]. Named to avoid conflict with
// the existing clamp() in quant.go.
func filterClamp(val, lo, hi int) int {
	if val < lo {
		return lo
	}
	if val > hi {
		return hi
	}
	return val
}

// filterAbs returns |x|. Named to avoid conflict with abs() in intra.go.
func filterAbs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// filterMin returns the smaller of a and b.
func filterMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// filterMax returns the larger of a and b.
func filterMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// filterIlog2 returns floor(log2(v)) for v > 0. Returns 0 for v <= 1.
func filterIlog2(v int) int {
	n := 0
	for v > 1 {
		v >>= 1
		n++
	}
	return n
}

// clipPixel8 clamps v to [0, 255] for 8-bit.
func clipPixel8(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// applySign returns abs(v) with the sign of s applied:
// if s >= 0 then v, else -v. Used by CDEF constrain.
func applySign(v, s int) int {
	if s < 0 {
		return -v
	}
	return v
}
