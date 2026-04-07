// Package decoder implements AV1 bitstream decoding.
//
// This file implements AV1 inverse transforms per spec Section 7.13.
// AV1 uses separable 2D transforms where each dimension can independently
// use DCT, ADST, identity, or flipped ADST.
package decoder

// No external imports needed.

// TxType identifies the 2D transform type used for a block.
// The row and column transforms can each be one of DCT, ADST, FLIPADST,
// or IDENTITY, giving 16 combinations.
// AV1 spec Section 6.8.4 (Table 6-3).
type TxType int

const (
	DCT_DCT           TxType = iota // 0
	ADST_DCT                        // 1
	DCT_ADST                        // 2
	ADST_ADST                       // 3
	FLIPADST_DCT                    // 4
	DCT_FLIPADST                    // 5
	FLIPADST_FLIPADST               // 6
	ADST_FLIPADST                   // 7
	FLIPADST_ADST                   // 8
	IDTX                            // 9 (identity)
	V_DCT                           // 10
	H_DCT                           // 11
	V_ADST                          // 12
	H_ADST                          // 13
	V_FLIPADST                      // 14
	H_FLIPADST                      // 15
)

// TxSize identifies the transform block size.
// AV1 spec Section 6.4.1 (Table 6-1).
type TxSize int

const (
	TX_4X4   TxSize = iota // 0
	TX_8X8                 // 1
	TX_16X16               // 2
	TX_32X32               // 3
	TX_64X64               // 4
	TX_4X8                 // 5
	TX_8X4                 // 6
	TX_8X16                // 7
	TX_16X8                // 8
	TX_16X32               // 9
	TX_32X16               // 10
	TX_32X64               // 11
	TX_64X32               // 12
	TX_4X16                // 13
	TX_16X4                // 14
	TX_8X32                // 15
	TX_32X8                // 16
	TX_16X64               // 17
	TX_64X16               // 18
)

// TxSizeDimensions returns (width, height) in pixels for a TxSize.
func TxSizeDimensions(txSz TxSize) (int, int) {
	switch txSz {
	case TX_4X4:
		return 4, 4
	case TX_8X8:
		return 8, 8
	case TX_16X16:
		return 16, 16
	case TX_32X32:
		return 32, 32
	case TX_64X64:
		return 64, 64
	case TX_4X8:
		return 4, 8
	case TX_8X4:
		return 8, 4
	case TX_8X16:
		return 8, 16
	case TX_16X8:
		return 16, 8
	case TX_16X32:
		return 16, 32
	case TX_32X16:
		return 32, 16
	case TX_32X64:
		return 32, 64
	case TX_64X32:
		return 64, 32
	case TX_4X16:
		return 4, 16
	case TX_16X4:
		return 16, 4
	case TX_8X32:
		return 8, 32
	case TX_32X8:
		return 32, 8
	case TX_16X64:
		return 16, 64
	case TX_64X16:
		return 64, 16
	default:
		return 0, 0
	}
}

// txSizeLog2WH returns (lw, lh) = (log2(width/4), log2(height/4)) for a TxSize.
// Matches dav1d's dav1d_txfm_dimensions[].lw/.lh.
func txSizeLog2WH(txSz TxSize) (int, int) {
	w, h := TxSizeDimensions(txSz)
	lw := floorLog2(w) - 2
	lh := floorLog2(h) - 2
	if lw < 0 {
		lw = 0
	}
	if lh < 0 {
		lh = 0
	}
	return lw, lh
}

// getUVInterTxType derives the UV transform type from the luma txType and
// the UV transform size. Matches dav1d's get_uv_inter_txtp().
func getUVInterTxType(uvTxSz TxSize, yTxType TxType) TxType {
	uvW, uvH := TxSizeDimensions(uvTxSz)
	// Compute max/min of log2(dim/4) — these are dav1d's t_dim->max/min.
	lwi := floorLog2(uvW) - 2
	lhi := floorLog2(uvH) - 2
	if lwi < 0 {
		lwi = 0
	}
	if lhi < 0 {
		lhi = 0
	}
	maxDim := lwi
	if lhi > maxDim {
		maxDim = lhi
	}
	minDim := lwi
	if lhi < minDim {
		minDim = lhi
	}

	// dav1d: if max == TX_32X32 (3), only IDTX passes through; all else → DCT_DCT.
	if maxDim == 3 {
		if yTxType == IDTX {
			return IDTX
		}
		return DCT_DCT
	}
	// dav1d: if min == TX_16X16 (2) and ytxtp is H/V FLIPADST or H/V ADST → DCT_DCT.
	if minDim == 2 {
		switch yTxType {
		case H_FLIPADST, V_FLIPADST, H_ADST, V_ADST:
			return DCT_DCT
		}
	}
	return yTxType
}

// 1D transform type constants used internally.
const (
	tx1DDCT      = 0
	tx1DADST     = 1
	tx1DFLIPADST = 2
	tx1DIDENTITY = 3
)

// txTypeToRowCol decomposes a 2D TxType into the 1D transform type
// for the row (horizontal) and column (vertical) dimensions.
// Returns (rowType, colType) where each is one of:
//
//	0 = DCT, 1 = ADST, 2 = FLIPADST, 3 = IDENTITY
//
// txTypeToRowCol decomposes a 2D TxType into the 1D transform types
// for row (horizontal) and column (vertical) dimensions.
// Returns (rowType, colType).
func txTypeToRowCol(txType TxType) (row1DType, col1DType int) {
	// Our row type = dav1d txtps[1], col type = dav1d txtps[0].
	// The swap accounts for our coefficient layout vs dav1d's column-major.
	switch txType {
	case DCT_DCT:
		return tx1DDCT, tx1DDCT
	case ADST_DCT:
		return tx1DDCT, tx1DADST
	case DCT_ADST:
		return tx1DADST, tx1DDCT
	case ADST_ADST:
		return tx1DADST, tx1DADST
	case FLIPADST_DCT:
		return tx1DDCT, tx1DFLIPADST
	case DCT_FLIPADST:
		return tx1DFLIPADST, tx1DDCT
	case FLIPADST_FLIPADST:
		return tx1DFLIPADST, tx1DFLIPADST
	case ADST_FLIPADST:
		return tx1DFLIPADST, tx1DADST
	case FLIPADST_ADST:
		return tx1DADST, tx1DFLIPADST
	case IDTX:
		return tx1DIDENTITY, tx1DIDENTITY
	case V_DCT:
		return tx1DIDENTITY, tx1DDCT
	case H_DCT:
		return tx1DDCT, tx1DIDENTITY
	case V_ADST:
		return tx1DIDENTITY, tx1DADST
	case H_ADST:
		return tx1DADST, tx1DIDENTITY
	case V_FLIPADST:
		return tx1DIDENTITY, tx1DFLIPADST
	case H_FLIPADST:
		return tx1DFLIPADST, tx1DIDENTITY
	default:
		return tx1DDCT, tx1DDCT
	}
}

// AV1 fixed-point trig constants (14-bit precision).
// These are round(cos(k*pi/64) * (1 << 14)), matching the AV1 spec
// Section 7.13.2.
const (
	cospi1  = 16364 // cos(1*pi/64)
	cospi2  = 16305 // cos(2*pi/64)
	cospi3  = 16207 // cos(3*pi/64)
	cospi4  = 16069 // cos(4*pi/64)
	cospi5  = 15893 // cos(5*pi/64)
	cospi6  = 15679 // cos(6*pi/64)
	cospi7  = 15426 // cos(7*pi/64)
	cospi8  = 15137 // cos(8*pi/64) = cos(pi/8)
	cospi9  = 14811 // cos(9*pi/64)
	cospi10 = 14449 // cos(10*pi/64)
	cospi11 = 14053 // cos(11*pi/64)
	cospi12 = 13623 // cos(12*pi/64) = cos(3*pi/16)
	cospi13 = 13160 // cos(13*pi/64)
	cospi14 = 12665 // cos(14*pi/64)
	cospi15 = 12140 // cos(15*pi/64)
	cospi16 = 11585 // cos(16*pi/64) = cos(pi/4) = sqrt(2)/2
	cospi17 = 11003 // cos(17*pi/64)
	cospi18 = 10394 // cos(18*pi/64)
	cospi19 = 9760  // cos(19*pi/64)
	cospi20 = 9102  // cos(20*pi/64) = cos(5*pi/16)
	cospi21 = 8423  // cos(21*pi/64)
	cospi22 = 7723  // cos(22*pi/64)
	cospi23 = 7005  // cos(23*pi/64)
	cospi24 = 6270  // cos(24*pi/64) = cos(3*pi/8)
	cospi25 = 5520  // cos(25*pi/64)
	cospi26 = 4756  // cos(26*pi/64)
	cospi27 = 3981  // cos(27*pi/64)
	cospi28 = 3196  // cos(28*pi/64) = cos(7*pi/16)
	cospi29 = 2404  // cos(29*pi/64)
	cospi30 = 1606  // cos(30*pi/64)
	cospi31 = 804   // cos(31*pi/64)
	cospi32 = 0     // cos(32*pi/64) = cos(pi/2) = 0

)

// roundShift performs a fixed-point rounding right shift.
// This is the ROUND2 operation from AV1 spec Section 7.1.2:
//
//	ROUND2(x, n) = (x + (1 << (n-1))) >> n
func roundShift(val int64, shift int) int32 {
	if shift == 0 {
		return int32(val)
	}
	return int32((val + (1 << (shift - 1))) >> shift)
}

// clip16 clips a value to the INT16 range [-32768, 32767].
// This matches dav1d's CLIP macro for 8-bit content.
func clip16(v int32) int32 {
	if v < -32768 {
		return -32768
	}
	if v > 32767 {
		return 32767
	}
	return v
}

// --- 4-point inverse DCT ---
// Port of dav1d's inv_dct4_1d_internal_c (non-tx64 path).
// Uses dav1d's exact constants and overflow-safe formulations.
func idct4(input []int32) {
	in0, in1, in2, in3 := input[0], input[1], input[2], input[3]

	t0 := ((in0 + in2) * 181 + 128) >> 8
	t1 := ((in0 - in2) * 181 + 128) >> 8
	t2 := ((in1*1567 - in3*(3784-4096) + 2048) >> 12) - in3
	t3 := ((in1*(3784-4096) + in3*1567 + 2048) >> 12) + in1

	input[0] = clip16(t0 + t3)
	input[1] = clip16(t1 + t2)
	input[2] = clip16(t1 - t2)
	input[3] = clip16(t0 - t3)
}

// --- 8-point inverse DCT ---
// Port of dav1d's inv_dct8_1d_internal_c (non-tx64 path).
func idct8(input []int32) {
	// Even half: apply 4-point IDCT to even-indexed inputs.
	even := [4]int32{input[0], input[2], input[4], input[6]}
	idct4(even[:])

	// Odd half: initial rotations using dav1d's exact constants.
	in1, in3, in5, in7 := input[1], input[3], input[5], input[7]

	t4a := ((in1*799 - in7*(4017-4096) + 2048) >> 12) - in7
	t5a := (in5*1703 - in3*1138 + 1024) >> 11
	t6a := (in5*1138 + in3*1703 + 1024) >> 11
	t7a := ((in1*(4017-4096) + in7*799 + 2048) >> 12) + in1

	// Butterfly with clip.
	t4 := clip16(t4a + t5a)
	t5a = clip16(t4a - t5a)
	t7 := clip16(t7a + t6a)
	t6a = clip16(t7a - t6a)

	// cospi16 rotation using 181>>8.
	t5 := ((t6a - t5a) * 181 + 128) >> 8
	t6 := ((t6a + t5a) * 181 + 128) >> 8

	// Read back even results (after idct4 modified them in place).
	t0 := even[0]
	t1 := even[1]
	t2 := even[2]
	t3 := even[3]

	// Final combination with clip.
	input[0] = clip16(t0 + t7)
	input[1] = clip16(t1 + t6)
	input[2] = clip16(t2 + t5)
	input[3] = clip16(t3 + t4)
	input[4] = clip16(t3 - t4)
	input[5] = clip16(t2 - t5)
	input[6] = clip16(t1 - t6)
	input[7] = clip16(t0 - t7)
}

// --- 16-point inverse DCT ---
// Port of dav1d's inv_dct16_1d_internal_c (non-tx64 path).
func idct16(input []int32) {
	// Even half: 8-point IDCT on even-indexed inputs.
	even := [8]int32{input[0], input[2], input[4], input[6],
		input[8], input[10], input[12], input[14]}
	idct8(even[:])

	// Odd half: initial rotations using dav1d's exact constants.
	in1, in3, in5, in7 := input[1], input[3], input[5], input[7]
	in9, in11, in13, in15 := input[9], input[11], input[13], input[15]

	t8a := ((in1*401 - in15*(4076-4096) + 2048) >> 12) - in15
	t9a := (in9*1583 - in7*1299 + 1024) >> 11
	t10a := ((in5*1931 - in11*(3612-4096) + 2048) >> 12) - in11
	t11a := ((in13*(3920-4096) - in3*1189 + 2048) >> 12) + in13
	t12a := ((in13*1189 + in3*(3920-4096) + 2048) >> 12) + in3
	t13a := ((in5*(3612-4096) + in11*1931 + 2048) >> 12) + in5
	t14a := (in9*1299 + in7*1583 + 1024) >> 11
	t15a := ((in1*(4076-4096) + in15*401 + 2048) >> 12) + in1

	// Stage 2 butterfly with clip.
	t8 := clip16(t8a + t9a)
	t9 := clip16(t8a - t9a)
	t10 := clip16(t11a - t10a)
	t11 := clip16(t11a + t10a)
	t12 := clip16(t12a + t13a)
	t13 := clip16(t12a - t13a)
	t14 := clip16(t15a - t14a)
	t15 := clip16(t15a + t14a)

	// Stage 3 rotations (cospi8/cospi24 equivalent in dav1d form).
	t9a = ((t14*1567 - t9*(3784-4096) + 2048) >> 12) - t9
	t14a = ((t14*(3784-4096) + t9*1567 + 2048) >> 12) + t14
	t10a = ((-(t13*(3784-4096) + t10*1567) + 2048) >> 12) - t13
	t13a = ((t13*1567 - t10*(3784-4096) + 2048) >> 12) - t10

	// Stage 3 butterfly with clip.
	t8a = clip16(t8 + t11)
	t9 = clip16(t9a + t10a)
	t10 = clip16(t9a - t10a)
	t11a = clip16(t8 - t11)
	t12a = clip16(t15 - t12)
	t13 = clip16(t14a - t13a)
	t14 = clip16(t14a + t13a)
	t15a = clip16(t15 + t12)

	// cospi16 rotations using 181>>8.
	t10a = ((t13 - t10) * 181 + 128) >> 8
	t13a = ((t13 + t10) * 181 + 128) >> 8
	t11 = ((t12a - t11a) * 181 + 128) >> 8
	t12 = ((t12a + t11a) * 181 + 128) >> 8

	// Read back even results.
	t0 := even[0]
	t1 := even[1]
	t2 := even[2]
	t3 := even[3]
	t4 := even[4]
	t5 := even[5]
	t6 := even[6]
	t7 := even[7]

	// Final combination with clip.
	input[0] = clip16(t0 + t15a)
	input[1] = clip16(t1 + t14)
	input[2] = clip16(t2 + t13a)
	input[3] = clip16(t3 + t12)
	input[4] = clip16(t4 + t11)
	input[5] = clip16(t5 + t10a)
	input[6] = clip16(t6 + t9)
	input[7] = clip16(t7 + t8a)
	input[8] = clip16(t7 - t8a)
	input[9] = clip16(t6 - t9)
	input[10] = clip16(t5 - t10a)
	input[11] = clip16(t4 - t11)
	input[12] = clip16(t3 - t12)
	input[13] = clip16(t2 - t13a)
	input[14] = clip16(t1 - t14)
	input[15] = clip16(t0 - t15a)
}

// --- 32-point inverse DCT ---
// Port of dav1d's inv_dct32_1d_internal_c (non-tx64 path).
func idct32(input []int32) {
	// Even half: 16-point IDCT on even-indexed inputs.
	even := [16]int32{
		input[0], input[2], input[4], input[6],
		input[8], input[10], input[12], input[14],
		input[16], input[18], input[20], input[22],
		input[24], input[26], input[28], input[30],
	}
	idct16(even[:])

	// Odd half: initial rotations using dav1d's exact constants.
	in1, in3, in5, in7 := input[1], input[3], input[5], input[7]
	in9, in11, in13, in15 := input[9], input[11], input[13], input[15]
	in17, in19, in21, in23 := input[17], input[19], input[21], input[23]
	in25, in27, in29, in31 := input[25], input[27], input[29], input[31]

	t16a := ((in1*201 - in31*(4091-4096) + 2048) >> 12) - in31
	t17a := ((in17*(3035-4096) - in15*2751 + 2048) >> 12) + in17
	t18a := ((in9*1751 - in23*(3703-4096) + 2048) >> 12) - in23
	t19a := ((in25*(3857-4096) - in7*1380 + 2048) >> 12) + in25
	t20a := ((in5*995 - in27*(3973-4096) + 2048) >> 12) - in27
	t21a := ((in21*(3513-4096) - in11*2106 + 2048) >> 12) + in21
	t22a := (in13*1220 - in19*1645 + 1024) >> 11
	t23a := ((in29*(4052-4096) - in3*601 + 2048) >> 12) + in29
	t24a := ((in29*601 + in3*(4052-4096) + 2048) >> 12) + in3
	t25a := (in13*1645 + in19*1220 + 1024) >> 11
	t26a := ((in21*2106 + in11*(3513-4096) + 2048) >> 12) + in11
	t27a := ((in5*(3973-4096) + in27*995 + 2048) >> 12) + in5
	t28a := ((in25*1380 + in7*(3857-4096) + 2048) >> 12) + in7
	t29a := ((in9*(3703-4096) + in23*1751 + 2048) >> 12) + in9
	t30a := ((in17*2751 + in15*(3035-4096) + 2048) >> 12) + in15
	t31a := ((in1*(4091-4096) + in31*201 + 2048) >> 12) + in1

	// Stage 2 butterfly with clip.
	t16 := clip16(t16a + t17a)
	t17 := clip16(t16a - t17a)
	t18 := clip16(t19a - t18a)
	t19 := clip16(t19a + t18a)
	t20 := clip16(t20a + t21a)
	t21 := clip16(t20a - t21a)
	t22 := clip16(t23a - t22a)
	t23 := clip16(t23a + t22a)
	t24 := clip16(t24a + t25a)
	t25 := clip16(t24a - t25a)
	t26 := clip16(t27a - t26a)
	t27 := clip16(t27a + t26a)
	t28 := clip16(t28a + t29a)
	t29 := clip16(t28a - t29a)
	t30 := clip16(t31a - t30a)
	t31 := clip16(t31a + t30a)

	// Stage 2 rotations (cospi4/cospi28 equivalent).
	t17a = ((t30*799 - t17*(4017-4096) + 2048) >> 12) - t17
	t30a = ((t30*(4017-4096) + t17*799 + 2048) >> 12) + t30
	t18a = ((-(t29*(4017-4096) + t18*799) + 2048) >> 12) - t29
	t29a = ((t29*799 - t18*(4017-4096) + 2048) >> 12) - t18
	t21a = (t26*1703 - t21*1138 + 1024) >> 11
	t26a = (t26*1138 + t21*1703 + 1024) >> 11
	t22a = (-(t25*1138 + t22*1703) + 1024) >> 11
	t25a = (t25*1703 - t22*1138 + 1024) >> 11

	// Stage 3 butterfly with clip.
	t16a = clip16(t16 + t19)
	t17 = clip16(t17a + t18a)
	t18 = clip16(t17a - t18a)
	t19a = clip16(t16 - t19)
	t20a = clip16(t23 - t20)
	t21 = clip16(t22a - t21a)
	t22 = clip16(t22a + t21a)
	t23a = clip16(t23 + t20)
	t24a = clip16(t24 + t27)
	t25 = clip16(t25a + t26a)
	t26 = clip16(t25a - t26a)
	t27a = clip16(t24 - t27)
	t28a = clip16(t31 - t28)
	t29 = clip16(t30a - t29a)
	t30 = clip16(t30a + t29a)
	t31a = clip16(t31 + t28)

	// Stage 3 rotations (cospi8/cospi24 equivalent).
	t18a = ((t29*1567 - t18*(3784-4096) + 2048) >> 12) - t18
	t29a = ((t29*(3784-4096) + t18*1567 + 2048) >> 12) + t29
	t19 = ((t28a*1567 - t19a*(3784-4096) + 2048) >> 12) - t19a
	t28 = ((t28a*(3784-4096) + t19a*1567 + 2048) >> 12) + t28a
	t20 = ((-(t27a*(3784-4096) + t20a*1567) + 2048) >> 12) - t27a
	t27 = ((t27a*1567 - t20a*(3784-4096) + 2048) >> 12) - t20a
	t21a = ((-(t26*(3784-4096) + t21*1567) + 2048) >> 12) - t26
	t26a = ((t26*1567 - t21*(3784-4096) + 2048) >> 12) - t21

	// Stage 4 butterfly with clip.
	t16 = clip16(t16a + t23a)
	t17a = clip16(t17 + t22)
	t18 = clip16(t18a + t21a)
	t19a = clip16(t19 + t20)
	t20a = clip16(t19 - t20)
	t21 = clip16(t18a - t21a)
	t22a = clip16(t17 - t22)
	t23 = clip16(t16a - t23a)
	t24 = clip16(t31a - t24a)
	t25a = clip16(t30 - t25)
	t26 = clip16(t29a - t26a)
	t27a = clip16(t28 - t27)
	t28a = clip16(t28 + t27)
	t29 = clip16(t29a + t26a)
	t30a = clip16(t30 + t25)
	t31 = clip16(t31a + t24a)

	// cospi16 rotations using 181>>8.
	t20 = ((t27a - t20a) * 181 + 128) >> 8
	t27 = ((t27a + t20a) * 181 + 128) >> 8
	t21a = ((t26 - t21) * 181 + 128) >> 8
	t26a = ((t26 + t21) * 181 + 128) >> 8
	t22 = ((t25a - t22a) * 181 + 128) >> 8
	t25 = ((t25a + t22a) * 181 + 128) >> 8
	t23a = ((t24 - t23) * 181 + 128) >> 8
	t24a = ((t24 + t23) * 181 + 128) >> 8

	// Read back even results.
	t0 := even[0]
	t1 := even[1]
	t2 := even[2]
	t3 := even[3]
	t4 := even[4]
	t5 := even[5]
	t6 := even[6]
	t7 := even[7]
	t8 := even[8]
	t9 := even[9]
	t10 := even[10]
	t11 := even[11]
	t12 := even[12]
	t13 := even[13]
	t14 := even[14]
	t15 := even[15]

	// Final combination with clip.
	input[0] = clip16(t0 + t31)
	input[1] = clip16(t1 + t30a)
	input[2] = clip16(t2 + t29)
	input[3] = clip16(t3 + t28a)
	input[4] = clip16(t4 + t27)
	input[5] = clip16(t5 + t26a)
	input[6] = clip16(t6 + t25)
	input[7] = clip16(t7 + t24a)
	input[8] = clip16(t8 + t23a)
	input[9] = clip16(t9 + t22)
	input[10] = clip16(t10 + t21a)
	input[11] = clip16(t11 + t20)
	input[12] = clip16(t12 + t19a)
	input[13] = clip16(t13 + t18)
	input[14] = clip16(t14 + t17a)
	input[15] = clip16(t15 + t16)
	input[16] = clip16(t15 - t16)
	input[17] = clip16(t14 - t17a)
	input[18] = clip16(t13 - t18)
	input[19] = clip16(t12 - t19a)
	input[20] = clip16(t11 - t20)
	input[21] = clip16(t10 - t21a)
	input[22] = clip16(t9 - t22)
	input[23] = clip16(t8 - t23a)
	input[24] = clip16(t7 - t24a)
	input[25] = clip16(t6 - t25)
	input[26] = clip16(t5 - t26a)
	input[27] = clip16(t4 - t27)
	input[28] = clip16(t3 - t28a)
	input[29] = clip16(t2 - t29)
	input[30] = clip16(t1 - t30a)
	input[31] = clip16(t0 - t31)
}

// --- 4-point inverse ADST ---
// AV1 spec Section 7.13.2.6.
//
// The 4-point ADST uses sin(k*pi/9) constants rather than the cosine
// constants used by DCT.
func iadst4(input []int32) {
	// Port of dav1d's inv_adst4_1d_internal_c.
	// Uses non-standard constants 1321, 3803, 2482, 3344, 209.
	in0, in1, in2, in3 := input[0], input[1], input[2], input[3]

	input[0] = int32(((1321*int64(in0) + (3803-4096)*int64(in2) +
		(2482-4096)*int64(in3) + (3344-4096)*int64(in1) + 2048) >> 12) +
		int64(in2) + int64(in3) + int64(in1))
	input[1] = int32((((2482-4096)*int64(in0) - 1321*int64(in2) -
		(3803-4096)*int64(in3) + (3344-4096)*int64(in1) + 2048) >> 12) +
		int64(in0) - int64(in3) + int64(in1))
	input[2] = int32((209*int64(in0-in2+in3) + 128) >> 8)
	input[3] = int32((((3803-4096)*int64(in0) + (2482-4096)*int64(in2) -
		1321*int64(in3) - (3344-4096)*int64(in1) + 2048) >> 12) +
		int64(in0) + int64(in2) - int64(in1))
}

// --- 8-point inverse ADST ---
// Port of dav1d's inv_adst8_1d_internal_c.
func iadst8(input []int32) {
	in0, in1, in2, in3 := input[0], input[1], input[2], input[3]
	in4, in5, in6, in7 := input[4], input[5], input[6], input[7]

	// Stage 1: rotations with overflow-safe dav1d constants.
	t0a := int32(((4076-4096)*int64(in7) + 401*int64(in0) + 2048) >> 12) + in7
	t1a := int32((401*int64(in7) - (4076-4096)*int64(in0) + 2048) >> 12) - in0
	t2a := int32(((3612-4096)*int64(in5) + 1931*int64(in2) + 2048) >> 12) + in5
	t3a := int32((1931*int64(in5) - (3612-4096)*int64(in2) + 2048) >> 12) - in2
	t4a := int32((1299*int64(in3) + 1583*int64(in4) + 1024) >> 11)
	t5a := int32((1583*int64(in3) - 1299*int64(in4) + 1024) >> 11)
	t6a := int32((1189*int64(in1) + (3920-4096)*int64(in6) + 2048) >> 12) + in6
	t7a := int32(((3920-4096)*int64(in1) - 1189*int64(in6) + 2048) >> 12) + in1

	// Stage 2: butterfly with clip.
	t0 := clip16(t0a + t4a)
	t1 := clip16(t1a + t5a)
	t2 := clip16(t2a + t6a)
	t3 := clip16(t3a + t7a)
	t4 := clip16(t0a - t4a)
	t5 := clip16(t1a - t5a)
	t6 := clip16(t2a - t6a)
	t7 := clip16(t3a - t7a)

	// Stage 3: rotations by (3784, 1567) with overflow-safe formulas.
	t4a = int32(((3784-4096)*int64(t4) + 1567*int64(t5) + 2048) >> 12) + t4
	t5a = int32((1567*int64(t4) - (3784-4096)*int64(t5) + 2048) >> 12) - t5
	t6a = int32(((3784-4096)*int64(t7) - 1567*int64(t6) + 2048) >> 12) + t7
	t7a = int32((1567*int64(t7) + (3784-4096)*int64(t6) + 2048) >> 12) + t6

	// Stage 4: final output with negations and cospi16=181 rotations.
	input[0] = clip16(t0 + t2)
	input[7] = -clip16(t1 + t3)
	t2 = clip16(t0 - t2)
	t3 = clip16(t1 - t3)
	input[1] = -clip16(t4a + t6a)
	input[6] = clip16(t5a + t7a)
	t6 = clip16(t4a - t6a)
	t7 = clip16(t5a - t7a)

	input[3] = -int32((int64(t2+t3)*181 + 128) >> 8)
	input[4] = int32((int64(t2-t3)*181 + 128) >> 8)
	input[2] = int32((int64(t6+t7)*181 + 128) >> 8)
	input[5] = -int32((int64(t6-t7)*181 + 128) >> 8)
}

// --- 16-point inverse ADST ---
// Port of dav1d's inv_adst16_1d_internal_c.
func iadst16(input []int32) {
	in0, in1, in2, in3 := input[0], input[1], input[2], input[3]
	in4, in5, in6, in7 := input[4], input[5], input[6], input[7]
	in8, in9, in10, in11 := input[8], input[9], input[10], input[11]
	in12, in13, in14, in15 := input[12], input[13], input[14], input[15]

	// Stage 1: initial rotations with overflow-safe dav1d constants.
	t0 := int32(((4091-4096)*int64(in15) + 201*int64(in0) + 2048) >> 12) + in15
	t1 := int32((201*int64(in15) - (4091-4096)*int64(in0) + 2048) >> 12) - in0
	t2 := int32(((3973-4096)*int64(in13) + 995*int64(in2) + 2048) >> 12) + in13
	t3 := int32((995*int64(in13) - (3973-4096)*int64(in2) + 2048) >> 12) - in2
	t4 := int32(((3703-4096)*int64(in11) + 1751*int64(in4) + 2048) >> 12) + in11
	t5 := int32((1751*int64(in11) - (3703-4096)*int64(in4) + 2048) >> 12) - in4
	t6 := int32((1645*int64(in9) + 1220*int64(in6) + 1024) >> 11)
	t7 := int32((1220*int64(in9) - 1645*int64(in6) + 1024) >> 11)
	t8 := int32((2751*int64(in7) + (3035-4096)*int64(in8) + 2048) >> 12) + in8
	t9 := int32(((3035-4096)*int64(in7) - 2751*int64(in8) + 2048) >> 12) + in7
	t10 := int32((2106*int64(in5) + (3513-4096)*int64(in10) + 2048) >> 12) + in10
	t11 := int32(((3513-4096)*int64(in5) - 2106*int64(in10) + 2048) >> 12) + in5
	t12 := int32((1380*int64(in3) + (3857-4096)*int64(in12) + 2048) >> 12) + in12
	t13 := int32(((3857-4096)*int64(in3) - 1380*int64(in12) + 2048) >> 12) + in3
	t14 := int32((601*int64(in1) + (4052-4096)*int64(in14) + 2048) >> 12) + in14
	t15 := int32(((4052-4096)*int64(in1) - 601*int64(in14) + 2048) >> 12) + in1

	// Stage 2: butterfly with clip.
	t0a := clip16(t0 + t8)
	t1a := clip16(t1 + t9)
	t2a := clip16(t2 + t10)
	t3a := clip16(t3 + t11)
	t4a := clip16(t4 + t12)
	t5a := clip16(t5 + t13)
	t6a := clip16(t6 + t14)
	t7a := clip16(t7 + t15)
	t8a := clip16(t0 - t8)
	t9a := clip16(t1 - t9)
	t10a := clip16(t2 - t10)
	t11a := clip16(t3 - t11)
	t12a := clip16(t4 - t12)
	t13a := clip16(t5 - t13)
	t14a := clip16(t6 - t14)
	t15a := clip16(t7 - t15)

	// Stage 3: rotations on diff pairs.
	t8 = int32(((4017-4096)*int64(t8a) + 799*int64(t9a) + 2048) >> 12) + t8a
	t9 = int32((799*int64(t8a) - (4017-4096)*int64(t9a) + 2048) >> 12) - t9a
	t10 = int32((2276*int64(t10a) + (3406-4096)*int64(t11a) + 2048) >> 12) + t11a
	t11 = int32(((3406-4096)*int64(t10a) - 2276*int64(t11a) + 2048) >> 12) + t10a
	t12 = int32(((4017-4096)*int64(t13a) - 799*int64(t12a) + 2048) >> 12) + t13a
	t13 = int32((799*int64(t13a) + (4017-4096)*int64(t12a) + 2048) >> 12) + t12a
	t14 = int32((2276*int64(t15a) - (3406-4096)*int64(t14a) + 2048) >> 12) - t14a
	t15 = int32(((3406-4096)*int64(t15a) + 2276*int64(t14a) + 2048) >> 12) + t15a

	// Stage 4: butterfly with clip.
	t0 = clip16(t0a + t4a)
	t1 = clip16(t1a + t5a)
	t2 = clip16(t2a + t6a)
	t3 = clip16(t3a + t7a)
	t4 = clip16(t0a - t4a)
	t5 = clip16(t1a - t5a)
	t6 = clip16(t2a - t6a)
	t7 = clip16(t3a - t7a)
	t8a = clip16(t8 + t12)
	t9a = clip16(t9 + t13)
	t10a = clip16(t10 + t14)
	t11a = clip16(t11 + t15)
	t12a = clip16(t8 - t12)
	t13a = clip16(t9 - t13)
	t14a = clip16(t10 - t14)
	t15a = clip16(t11 - t15)

	// Stage 5: cospi16 rotations with (3784, 1567).
	t4a = int32(((3784-4096)*int64(t4) + 1567*int64(t5) + 2048) >> 12) + t4
	t5a = int32((1567*int64(t4) - (3784-4096)*int64(t5) + 2048) >> 12) - t5
	t6a = int32(((3784-4096)*int64(t7) - 1567*int64(t6) + 2048) >> 12) + t7
	t7a = int32((1567*int64(t7) + (3784-4096)*int64(t6) + 2048) >> 12) + t6
	t12 = int32(((3784-4096)*int64(t12a) + 1567*int64(t13a) + 2048) >> 12) + t12a
	t13 = int32((1567*int64(t12a) - (3784-4096)*int64(t13a) + 2048) >> 12) - t13a
	t14 = int32(((3784-4096)*int64(t15a) - 1567*int64(t14a) + 2048) >> 12) + t15a
	t15 = int32((1567*int64(t15a) + (3784-4096)*int64(t14a) + 2048) >> 12) + t14a

	// Stage 6: final output with negations and cospi16=181 rotations.
	input[0] = clip16(t0 + t2)
	input[15] = -clip16(t1 + t3)
	t2a = clip16(t0 - t2)
	t3a = clip16(t1 - t3)
	input[3] = -clip16(t4a + t6a)
	input[12] = clip16(t5a + t7a)
	t6 = clip16(t4a - t6a)
	t7 = clip16(t5a - t7a)
	input[1] = -clip16(t8a + t10a)
	input[14] = clip16(t9a + t11a)
	t10 = clip16(t8a - t10a)
	t11 = clip16(t9a - t11a)
	input[2] = clip16(t12 + t14)
	input[13] = -clip16(t13 + t15)
	t14a = clip16(t12 - t14)
	t15a = clip16(t13 - t15)

	input[7] = -int32((int64(t2a+t3a)*181 + 128) >> 8)
	input[8] = int32((int64(t2a-t3a)*181 + 128) >> 8)
	input[4] = int32((int64(t6+t7)*181 + 128) >> 8)
	input[11] = -int32((int64(t6-t7)*181 + 128) >> 8)
	input[6] = int32((int64(t10+t11)*181 + 128) >> 8)
	input[9] = -int32((int64(t10-t11)*181 + 128) >> 8)
	input[5] = -int32((int64(t14a+t15a)*181 + 128) >> 8)
	input[10] = int32((int64(t14a-t15a)*181 + 128) >> 8)
}

// --- 64-point inverse DCT ---
// AV1 spec Section 7.13.2.3 (Inverse DCT array process, N=64).
//
// AV1 constrains 64-point transforms to have at most 32 non-zero
// coefficients (input[0..31]; input[32..63] = 0).
// Butterfly decomposition matching dav1d inv_dct64_1d_c exactly.
func idct64(input []int32) {
	// Even half: 32-point IDCT on even-indexed inputs.
	even := [32]int32{
		input[0], input[2], input[4], input[6],
		input[8], input[10], input[12], input[14],
		input[16], input[18], input[20], input[22],
		input[24], input[26], input[28], input[30],
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0,
	}
	idct32(even[:])

	// Odd half: initial rotations.
	in1, in3, in5, in7 := input[1], input[3], input[5], input[7]
	in9, in11, in13, in15 := input[9], input[11], input[13], input[15]
	in17, in19, in21, in23 := input[17], input[19], input[21], input[23]
	in25, in27, in29, in31 := input[25], input[27], input[29], input[31]

	t32a := (in1*101 + 2048) >> 12
	t33a := (in31*-2824 + 2048) >> 12
	t34a := (in17*1660 + 2048) >> 12
	t35a := (in15*-1474 + 2048) >> 12
	t36a := (in9*897 + 2048) >> 12
	t37a := (in23*-2191 + 2048) >> 12
	t38a := (in25*2359 + 2048) >> 12
	t39a := (in7*-700 + 2048) >> 12
	t40a := (in5*501 + 2048) >> 12
	t41a := (in27*-2520 + 2048) >> 12
	t42a := (in21*2019 + 2048) >> 12
	t43a := (in11*-1092 + 2048) >> 12
	t44a := (in13*1285 + 2048) >> 12
	t45a := (in19*-1842 + 2048) >> 12
	t46a := (in29*2675 + 2048) >> 12
	t47a := (in3*-301 + 2048) >> 12
	t48a := (in3*4085 + 2048) >> 12
	t49a := (in29*3102 + 2048) >> 12
	t50a := (in19*3659 + 2048) >> 12
	t51a := (in13*3889 + 2048) >> 12
	t52a := (in11*3948 + 2048) >> 12
	t53a := (in21*3564 + 2048) >> 12
	t54a := (in27*3229 + 2048) >> 12
	t55a := (in5*4065 + 2048) >> 12
	t56a := (in7*4036 + 2048) >> 12
	t57a := (in25*3349 + 2048) >> 12
	t58a := (in23*3461 + 2048) >> 12
	t59a := (in9*3996 + 2048) >> 12
	t60a := (in15*3822 + 2048) >> 12
	t61a := (in17*3745 + 2048) >> 12
	t62a := (in31*2967 + 2048) >> 12
	t63a := (in1*4095 + 2048) >> 12

	t32 := clip16(t32a + t33a)
	t33 := clip16(t32a - t33a)
	t34 := clip16(t35a - t34a)
	t35 := clip16(t35a + t34a)
	t36 := clip16(t36a + t37a)
	t37 := clip16(t36a - t37a)
	t38 := clip16(t39a - t38a)
	t39 := clip16(t39a + t38a)
	t40 := clip16(t40a + t41a)
	t41 := clip16(t40a - t41a)
	t42 := clip16(t43a - t42a)
	t43 := clip16(t43a + t42a)
	t44 := clip16(t44a + t45a)
	t45 := clip16(t44a - t45a)
	t46 := clip16(t47a - t46a)
	t47 := clip16(t47a + t46a)
	t48 := clip16(t48a + t49a)
	t49 := clip16(t48a - t49a)
	t50 := clip16(t51a - t50a)
	t51 := clip16(t51a + t50a)
	t52 := clip16(t52a + t53a)
	t53 := clip16(t52a - t53a)
	t54 := clip16(t55a - t54a)
	t55 := clip16(t55a + t54a)
	t56 := clip16(t56a + t57a)
	t57 := clip16(t56a - t57a)
	t58 := clip16(t59a - t58a)
	t59 := clip16(t59a + t58a)
	t60 := clip16(t60a + t61a)
	t61 := clip16(t60a - t61a)
	t62 := clip16(t63a - t62a)
	t63 := clip16(t63a + t62a)

	// Stage 2 rotations.
	t33a = ((t33*(4096-4076) + t62*401 + 2048) >> 12) - t33
	t34a = ((t34*-401 + t61*(4096-4076) + 2048) >> 12) - t61
	t37a = (t37*-1299 + t58*1583 + 1024) >> 11
	t38a = (t38*-1583 + t57*-1299 + 1024) >> 11
	t41a = ((t41*(4096-3612) + t54*1931 + 2048) >> 12) - t41
	t42a = ((t42*-1931 + t53*(4096-3612) + 2048) >> 12) - t53
	t45a = ((t45*-1189 + t50*(3920-4096) + 2048) >> 12) + t50
	t46a = ((t46*(4096-3920) + t49*-1189 + 2048) >> 12) - t46
	t49a = ((t46*-1189 + t49*(3920-4096) + 2048) >> 12) + t49
	t50a = ((t45*(3920-4096) + t50*1189 + 2048) >> 12) + t45
	t53a = ((t42*(4096-3612) + t53*1931 + 2048) >> 12) - t42
	t54a = ((t41*1931 + t54*(3612-4096) + 2048) >> 12) + t54
	t57a = (t38*-1299 + t57*1583 + 1024) >> 11
	t58a = (t37*1583 + t58*1299 + 1024) >> 11
	t61a = ((t34*(4096-4076) + t61*401 + 2048) >> 12) - t34
	t62a = ((t33*401 + t62*(4076-4096) + 2048) >> 12) + t62

	t32a = clip16(t32 + t35)
	t33 = clip16(t33a + t34a)
	t34 = clip16(t33a - t34a)
	t35a = clip16(t32 - t35)
	t36a = clip16(t39 - t36)
	t37 = clip16(t38a - t37a)
	t38 = clip16(t38a + t37a)
	t39a = clip16(t39 + t36)
	t40a = clip16(t40 + t43)
	t41 = clip16(t41a + t42a)
	t42 = clip16(t41a - t42a)
	t43a = clip16(t40 - t43)
	t44a = clip16(t47 - t44)
	t45 = clip16(t46a - t45a)
	t46 = clip16(t46a + t45a)
	t47a = clip16(t47 + t44)
	t48a = clip16(t48 + t51)
	t49 = clip16(t49a + t50a)
	t50 = clip16(t49a - t50a)
	t51a = clip16(t48 - t51)
	t52a = clip16(t55 - t52)
	t53 = clip16(t54a - t53a)
	t54 = clip16(t54a + t53a)
	t55a = clip16(t55 + t52)
	t56a = clip16(t56 + t59)
	t57 = clip16(t57a + t58a)
	t58 = clip16(t57a - t58a)
	t59a = clip16(t56 - t59)
	t60a = clip16(t63 - t60)
	t61 = clip16(t62a - t61a)
	t62 = clip16(t62a + t61a)
	t63a = clip16(t63 + t60)

	// Stage 3 rotations.
	t34a = ((t34*(4096-4017) + t61*799 + 2048) >> 12) - t34
	t35 = ((t35a*(4096-4017) + t60a*799 + 2048) >> 12) - t35a
	t36 = ((t36a*-799 + t59a*(4096-4017) + 2048) >> 12) - t59a
	t37a = ((t37*-799 + t58*(4096-4017) + 2048) >> 12) - t58
	t42a = (t42*-1138 + t53*1703 + 1024) >> 11
	t43 = (t43a*-1138 + t52a*1703 + 1024) >> 11
	t44 = (t44a*-1703 + t51a*-1138 + 1024) >> 11
	t45a = (t45*-1703 + t50*-1138 + 1024) >> 11
	t50a = (t45*-1138 + t50*1703 + 1024) >> 11
	t51 = (t44a*-1138 + t51a*1703 + 1024) >> 11
	t52 = (t43a*1703 + t52a*1138 + 1024) >> 11
	t53a = (t42*1703 + t53*1138 + 1024) >> 11
	t58a = ((t37*(4096-4017) + t58*799 + 2048) >> 12) - t37
	t59 = ((t36a*(4096-4017) + t59a*799 + 2048) >> 12) - t36a
	t60 = ((t35a*799 + t60a*(4017-4096) + 2048) >> 12) + t60a
	t61a = ((t34*799 + t61*(4017-4096) + 2048) >> 12) + t61

	t32 = clip16(t32a + t39a)
	t33a = clip16(t33 + t38)
	t34 = clip16(t34a + t37a)
	t35a = clip16(t35 + t36)
	t36a = clip16(t35 - t36)
	t37 = clip16(t34a - t37a)
	t38a = clip16(t33 - t38)
	t39 = clip16(t32a - t39a)
	t40 = clip16(t47a - t40a)
	t41a = clip16(t46 - t41)
	t42 = clip16(t45a - t42a)
	t43a = clip16(t44 - t43)
	t44a = clip16(t44 + t43)
	t45 = clip16(t45a + t42a)
	t46a = clip16(t46 + t41)
	t47 = clip16(t47a + t40a)
	t48 = clip16(t48a + t55a)
	t49a = clip16(t49 + t54)
	t50 = clip16(t50a + t53a)
	t51a = clip16(t51 + t52)
	t52a = clip16(t51 - t52)
	t53 = clip16(t50a - t53a)
	t54a = clip16(t49 - t54)
	t55 = clip16(t48a - t55a)
	t56 = clip16(t63a - t56a)
	t57a = clip16(t62 - t57)
	t58 = clip16(t61a - t58a)
	t59a = clip16(t60 - t59)
	t60a = clip16(t60 + t59)
	t61 = clip16(t61a + t58a)
	t62a = clip16(t62 + t57)
	t63 = clip16(t63a + t56a)

	// Stage 4 rotations (cospi8/cospi24).
	t36 = ((t36a*(4096-3784) + t59a*1567 + 2048) >> 12) - t36a
	t37a = ((t37*(4096-3784) + t58*1567 + 2048) >> 12) - t37
	t38 = ((t38a*(4096-3784) + t57a*1567 + 2048) >> 12) - t38a
	t39a = ((t39*(4096-3784) + t56*1567 + 2048) >> 12) - t39
	t40a = ((t40*-1567 + t55*(4096-3784) + 2048) >> 12) - t55
	t41 = ((t41a*-1567 + t54a*(4096-3784) + 2048) >> 12) - t54a
	t42a = ((t42*-1567 + t53*(4096-3784) + 2048) >> 12) - t53
	t43 = ((t43a*-1567 + t52a*(4096-3784) + 2048) >> 12) - t52a
	t52 = ((t43a*(4096-3784) + t52a*1567 + 2048) >> 12) - t43a
	t53a = ((t42*(4096-3784) + t53*1567 + 2048) >> 12) - t42
	t54 = ((t41a*(4096-3784) + t54a*1567 + 2048) >> 12) - t41a
	t55a = ((t40*(4096-3784) + t55*1567 + 2048) >> 12) - t40
	t56a = ((t39*1567 + t56*(3784-4096) + 2048) >> 12) + t56
	t57 = ((t38a*1567 + t57a*(3784-4096) + 2048) >> 12) + t57a
	t58a = ((t37*1567 + t58*(3784-4096) + 2048) >> 12) + t58
	t59 = ((t36a*1567 + t59a*(3784-4096) + 2048) >> 12) + t59a

	// Stage 5 butterfly.
	t32a = clip16(t32 + t47)
	t33 = clip16(t33a + t46a)
	t34a = clip16(t34 + t45)
	t35 = clip16(t35a + t44a)
	t36a = clip16(t36 + t43)
	t37 = clip16(t37a + t42a)
	t38a = clip16(t38 + t41)
	t39 = clip16(t39a + t40a)
	t40 = clip16(t39a - t40a)
	t41a = clip16(t38 - t41)
	t42 = clip16(t37a - t42a)
	t43a = clip16(t36 - t43)
	t44 = clip16(t35a - t44a)
	t45a = clip16(t34 - t45)
	t46 = clip16(t33a - t46a)
	t47a = clip16(t32 - t47)
	t48a = clip16(t63 - t48)
	t49 = clip16(t62a - t49a)
	t50a = clip16(t61 - t50)
	t51 = clip16(t60a - t51a)
	t52a = clip16(t59 - t52)
	t53 = clip16(t58a - t53a)
	t54a = clip16(t57 - t54)
	t55 = clip16(t56a - t55a)
	t56 = clip16(t56a + t55a)
	t57a = clip16(t57 + t54)
	t58 = clip16(t58a + t53a)
	t59a = clip16(t59 + t52)
	t60 = clip16(t60a + t51a)
	t61a = clip16(t61 + t50)
	t62 = clip16(t62a + t49a)
	t63a = clip16(t63 + t48)

	// Stage 5 cospi16 rotations.
	t40a = ((t55 - t40) * 181 + 128) >> 8
	t41 = ((t54a - t41a) * 181 + 128) >> 8
	t42a = ((t53 - t42) * 181 + 128) >> 8
	t43 = ((t52a - t43a) * 181 + 128) >> 8
	t44a = ((t51 - t44) * 181 + 128) >> 8
	t45 = ((t50a - t45a) * 181 + 128) >> 8
	t46a = ((t49 - t46) * 181 + 128) >> 8
	t47 = ((t48a - t47a) * 181 + 128) >> 8
	t48 = ((t47a + t48a) * 181 + 128) >> 8
	t49a = ((t46 + t49) * 181 + 128) >> 8
	t50 = ((t45a + t50a) * 181 + 128) >> 8
	t51a = ((t44 + t51) * 181 + 128) >> 8
	t52 = ((t43a + t52a) * 181 + 128) >> 8
	t53a = ((t42 + t53) * 181 + 128) >> 8
	t54 = ((t41a + t54a) * 181 + 128) >> 8
	t55a = ((t40 + t55) * 181 + 128) >> 8

	// Final combination: even[i] +/- odd[i].
	input[0] = clip16(even[0] + t63a)
	input[1] = clip16(even[1] + t62)
	input[2] = clip16(even[2] + t61a)
	input[3] = clip16(even[3] + t60)
	input[4] = clip16(even[4] + t59a)
	input[5] = clip16(even[5] + t58)
	input[6] = clip16(even[6] + t57a)
	input[7] = clip16(even[7] + t56)
	input[8] = clip16(even[8] + t55a)
	input[9] = clip16(even[9] + t54)
	input[10] = clip16(even[10] + t53a)
	input[11] = clip16(even[11] + t52)
	input[12] = clip16(even[12] + t51a)
	input[13] = clip16(even[13] + t50)
	input[14] = clip16(even[14] + t49a)
	input[15] = clip16(even[15] + t48)
	input[16] = clip16(even[16] + t47)
	input[17] = clip16(even[17] + t46a)
	input[18] = clip16(even[18] + t45)
	input[19] = clip16(even[19] + t44a)
	input[20] = clip16(even[20] + t43)
	input[21] = clip16(even[21] + t42a)
	input[22] = clip16(even[22] + t41)
	input[23] = clip16(even[23] + t40a)
	input[24] = clip16(even[24] + t39)
	input[25] = clip16(even[25] + t38a)
	input[26] = clip16(even[26] + t37)
	input[27] = clip16(even[27] + t36a)
	input[28] = clip16(even[28] + t35)
	input[29] = clip16(even[29] + t34a)
	input[30] = clip16(even[30] + t33)
	input[31] = clip16(even[31] + t32a)
	input[32] = clip16(even[31] - t32a)
	input[33] = clip16(even[30] - t33)
	input[34] = clip16(even[29] - t34a)
	input[35] = clip16(even[28] - t35)
	input[36] = clip16(even[27] - t36a)
	input[37] = clip16(even[26] - t37)
	input[38] = clip16(even[25] - t38a)
	input[39] = clip16(even[24] - t39)
	input[40] = clip16(even[23] - t40a)
	input[41] = clip16(even[22] - t41)
	input[42] = clip16(even[21] - t42a)
	input[43] = clip16(even[20] - t43)
	input[44] = clip16(even[19] - t44a)
	input[45] = clip16(even[18] - t45)
	input[46] = clip16(even[17] - t46a)
	input[47] = clip16(even[16] - t47)
	input[48] = clip16(even[15] - t48)
	input[49] = clip16(even[14] - t49a)
	input[50] = clip16(even[13] - t50)
	input[51] = clip16(even[12] - t51a)
	input[52] = clip16(even[11] - t52)
	input[53] = clip16(even[10] - t53a)
	input[54] = clip16(even[9] - t54)
	input[55] = clip16(even[8] - t55a)
	input[56] = clip16(even[7] - t56)
	input[57] = clip16(even[6] - t57a)
	input[58] = clip16(even[5] - t58)
	input[59] = clip16(even[4] - t59a)
	input[60] = clip16(even[3] - t60)
	input[61] = clip16(even[2] - t61a)
	input[62] = clip16(even[1] - t62)
	input[63] = clip16(even[0] - t63a)
}

// --- Identity transform ---
// AV1 spec Section 7.13.2.9.
//
// The identity transform multiplies by a scale factor that depends on
// the transform size:
//
//	N=4:  sqrt(2) => (x * 2 * 11585 + (1<<14)) >> 15, approximated below
//	N=8:  2
//	N=16: 2*sqrt(2) => (x * 2 * 11585 + (1<<13)) >> 14, approximated below
//	N=32: 4
//	N=64: 4*sqrt(2)
func iidentity(input []int32, n int) {
	switch n {
	case 4:
		// sqrt(2) ~= 1.41421356..., fixed-point: (x * 5793 + (1<<11)) >> 12
		// Using NewSqrt2 = 5793, NewSqrt2Bits = 12.
		for i := 0; i < n; i++ {
			input[i] = roundShift(int64(input[i])*5793, 12)
		}
	case 8:
		for i := 0; i < n; i++ {
			input[i] *= 2
		}
	case 16:
		// 2*sqrt(2) = 2 * 5793/4096 ~= 2.828...
		for i := 0; i < n; i++ {
			input[i] = roundShift(int64(input[i])*2*5793, 12)
		}
	case 32:
		for i := 0; i < n; i++ {
			input[i] *= 4
		}
	case 64:
		for i := 0; i < n; i++ {
			input[i] = roundShift(int64(input[i])*4*5793, 12)
		}
	}
}

// apply1DTransform dispatches to the correct 1D inverse transform
// based on transform type and size.
func apply1DTransform(data []int32, n int, txType1D int) {
	switch txType1D {
	case tx1DDCT:
		switch n {
		case 4:
			idct4(data[:4])
		case 8:
			idct8(data[:8])
		case 16:
			idct16(data[:16])
		case 32:
			idct32(data[:32])
		case 64:
			idct64(data[:64])
		}
	case tx1DADST:
		switch n {
		case 4:
			iadst4(data[:4])
		case 8:
			iadst8(data[:8])
		case 16:
			iadst16(data[:16])
		default:
			// ADST is not defined for sizes > 16 in AV1; fall back to DCT.
			apply1DTransform(data, n, tx1DDCT)
		}
	case tx1DFLIPADST:
		// FLIPADST = ADST followed by output reversal.
		switch n {
		case 4:
			iadst4(data[:4])
		case 8:
			iadst8(data[:8])
		case 16:
			iadst16(data[:16])
		default:
			apply1DTransform(data, n, tx1DDCT)
		}
		// Reverse the output.
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			data[i], data[j] = data[j], data[i]
		}
	case tx1DIDENTITY:
		iidentity(data[:n], n)
	}
}

// InverseTransform2D performs an in-place 2D inverse transform on the
// invTxfmShift returns the intermediate round shift between the two
// 1D transform passes. Matches dav1d inv_txfm_shift / libaom inv_shift tables.
func invTxfmShift(w, h int) int {
	// Matches dav1d inv_txfm_add_c shift parameter per TX size.
	switch {
	case w == 4 && h == 4:
		return 0
	case w == 4 && h == 8:
		return 0
	case w == 8 && h == 4:
		return 0
	case w == 4 && h == 16:
		return 1
	case w == 16 && h == 4:
		return 1
	case w == 8 && h == 8:
		return 1
	case w == 8 && h == 16:
		return 1
	case w == 16 && h == 8:
		return 1
	case w == 8 && h == 32:
		return 2
	case w == 32 && h == 8:
		return 2
	case w == 16 && h == 16:
		return 2
	case w == 16 && h == 32:
		return 1
	case w == 32 && h == 16:
		return 1
	case w == 16 && h == 64:
		return 2
	case w == 64 && h == 16:
		return 2
	case w == 32 && h == 32:
		return 2
	case w == 32 && h == 64:
		return 1
	case w == 64 && h == 32:
		return 1
	case w == 64 && h == 64:
		return 2
	default:
		return 1
	}
}

// coefficient block. The coefficients are stored in row-major order
// with stride w: coeffs[row*w + col].
//
// Pipeline matches dav1d's inv_txfm_add_c (row-first order):
//  1. For each row: read coefficients (with rect scaling if 2:1 ratio),
//     apply row transform (rowType = txtps[0])
//  2. Apply intermediate shift + INT16 clipping to all elements
//  3. For each column: apply column transform (colType = txtps[1])
//  4. Final >>4 round-shift
//
// For FLIPADST variants, the output is reversed in the corresponding
// dimension (handled inside apply1DTransform).
func InverseTransform2D(coeffs []int32, w, h int, txType TxType) {
	rowType, colType := txTypeToRowCol(txType)

	// For 64-wide or 64-tall transforms, only the lower 32 coefficients
	// are non-zero. AV1 spec Section 7.13.3.1.
	sw := w
	if sw > 32 {
		sw = 32
	}
	sh := h
	if sh > 32 {
		sh = 32
	}

	// Intermediate round shift between passes.
	shift := invTxfmShift(w, h)
	rnd := int32(0)
	if shift > 0 {
		rnd = int32(1) << (shift - 1)
	}

	// Rectangular scaling for 2:1 ratio transforms.
	isRect2 := (w*2 == h || h*2 == w)

	tmp := make([]int32, w*h)

	// Step 1: Row transforms.
	row := make([]int32, w)
	for y := 0; y < sh; y++ {
		if isRect2 {
			for x := 0; x < sw; x++ {
				row[x] = int32((int64(coeffs[y*w+x])*181 + 128) >> 8)
			}
		} else {
			for x := 0; x < sw; x++ {
				row[x] = coeffs[y*w+x]
			}
		}
		for x := sw; x < w; x++ {
			row[x] = 0
		}
		apply1DTransform(row, w, rowType)
		copy(tmp[y*w:y*w+w], row)
	}
	// Zero remaining rows.
	for y := sh; y < h; y++ {
		for x := 0; x < w; x++ {
			tmp[y*w+x] = 0
		}
	}

	// Step 2: Intermediate shift + INT16 clipping.
	for i := 0; i < w*sh; i++ {
		v := (tmp[i] + rnd) >> shift
		if v < -32768 {
			v = -32768
		} else if v > 32767 {
			v = 32767
		}
		tmp[i] = v
	}

	// Step 3: Column transforms.
	col := make([]int32, h)
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			if y < sh {
				col[y] = tmp[y*w+x]
			} else {
				col[y] = 0
			}
		}
		apply1DTransform(col, h, colType)
		for y := 0; y < h; y++ {
			tmp[y*w+x] = col[y]
		}
	}

	// Step 4: Final >>4 round-shift + write back.
	for i := 0; i < w*h; i++ {
		coeffs[i] = (tmp[i] + 8) >> 4
	}
}

// InverseWHT4x4 performs the inverse Walsh-Hadamard Transform for lossless 4x4 blocks.
// Matches dav1d's inv_txfm_add_wht_wht_4x4_c (itx_tmpl.c).
//
// dav1d's WHT reads coeff[y + x*4] (column-major) from a column-major cf buffer.
// Our coefficient buffer is row-major (coeffs[row*4 + col]), so we read
// coeffs[y*4 + x] (row-major) to get the same spatial coefficient at (y, x).
//
// Output is spatial-domain residual in row-major order (coeffs[row*4 + col]).
func InverseWHT4x4(coeffs []int32) {
	if len(coeffs) < 16 {
		return
	}
	var tmp [16]int32

	// Row pass: read row-major, apply 1D WHT to each row.
	// dav1d reads c[x] = coeff[y + x*4] >> 2 from column-major buffer.
	// We read coeffs[y*4 + x] >> 2 from row-major buffer (same spatial position).
	for y := 0; y < 4; y++ {
		in0 := coeffs[y*4+0] >> 2
		in1 := coeffs[y*4+1] >> 2
		in2 := coeffs[y*4+2] >> 2
		in3 := coeffs[y*4+3] >> 2

		t0 := in0 + in1
		t2 := in2 - in3
		t4 := (t0 - t2) >> 1
		t3 := t4 - in3
		t1 := t4 - in1

		tmp[y*4+0] = t0 - t3
		tmp[y*4+1] = t3
		tmp[y*4+2] = t1
		tmp[y*4+3] = t2 + t1
	}

	// Column pass: apply 1D WHT to each column.
	for x := 0; x < 4; x++ {
		in0 := tmp[0*4+x]
		in1 := tmp[1*4+x]
		in2 := tmp[2*4+x]
		in3 := tmp[3*4+x]

		t0 := in0 + in1
		t2 := in2 - in3
		t4 := (t0 - t2) >> 1
		t3 := t4 - in3
		t1 := t4 - in1

		tmp[0*4+x] = t0 - t3
		tmp[1*4+x] = t3
		tmp[2*4+x] = t1
		tmp[3*4+x] = t2 + t1
	}

	// Copy to output (row-major).
	copy(coeffs[:16], tmp[:])
}

// InverseTransform2DSize is a convenience wrapper that determines
// width and height from a TxSize value.
func InverseTransform2DSize(coeffs []int32, txSz TxSize, txType TxType) {
	w, h := TxSizeDimensions(txSz)
	if w == 0 || h == 0 {
		return
	}
	InverseTransform2D(coeffs, w, h, txType)
}
