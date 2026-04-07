// Package decoder implements AV1 bitstream decoding.
//
// This file implements sub-pixel motion compensation interpolation filters
// for inter prediction. Uses dav1d-compatible 64-sum filter coefficients and
// rounding for bit-exact matching with dav1d reference implementation.
// AV1 spec Section 7.11.3.4 (Motion Vector Projection) and Section 7.11.3.1
// (Inter Sample Filtering).
package decoder

// Sub-pixel interpolation filter coefficients from dav1d (tables.c).
// 64-sum coefficients, 15 sub-pixel positions (1/16 pel increments, positions 1-15).
// Position 0 (integer position) is handled by returning nil (no filter needed).
// [filter_type][subpel_position_minus_1][tap]
// filter_type: 0=EIGHTTAP_REGULAR, 1=EIGHTTAP_SMOOTH, 2=EIGHTTAP_SHARP

var interpFilter8Tap = [3][15][8]int8{
	// EIGHTTAP_REGULAR (dav1d DAV1D_FILTER_8TAP_REGULAR)
	{
		{0, 1, -3, 63, 4, -1, 0, 0},
		{0, 1, -5, 61, 9, -2, 0, 0},
		{0, 1, -6, 58, 14, -4, 1, 0},
		{0, 1, -7, 55, 19, -5, 1, 0},
		{0, 1, -7, 51, 24, -6, 1, 0},
		{0, 1, -8, 47, 29, -6, 1, 0},
		{0, 1, -7, 42, 33, -6, 1, 0},
		{0, 1, -7, 38, 38, -7, 1, 0},
		{0, 1, -6, 33, 42, -7, 1, 0},
		{0, 1, -6, 29, 47, -8, 1, 0},
		{0, 1, -6, 24, 51, -7, 1, 0},
		{0, 1, -5, 19, 55, -7, 1, 0},
		{0, 1, -4, 14, 58, -6, 1, 0},
		{0, 0, -2, 9, 61, -5, 1, 0},
		{0, 0, -1, 4, 63, -3, 1, 0},
	},
	// EIGHTTAP_SMOOTH (dav1d DAV1D_FILTER_8TAP_SMOOTH)
	{
		{0, 1, 14, 31, 17, 1, 0, 0},
		{0, 0, 13, 31, 18, 2, 0, 0},
		{0, 0, 11, 31, 20, 2, 0, 0},
		{0, 0, 10, 30, 21, 3, 0, 0},
		{0, 0, 9, 29, 22, 4, 0, 0},
		{0, 0, 8, 28, 23, 5, 0, 0},
		{0, -1, 8, 27, 24, 6, 0, 0},
		{0, -1, 7, 26, 26, 7, -1, 0},
		{0, 0, 6, 24, 27, 8, -1, 0},
		{0, 0, 5, 23, 28, 8, 0, 0},
		{0, 0, 4, 22, 29, 9, 0, 0},
		{0, 0, 3, 21, 30, 10, 0, 0},
		{0, 0, 2, 20, 31, 11, 0, 0},
		{0, 0, 2, 18, 31, 13, 0, 0},
		{0, 0, 1, 17, 31, 14, 1, 0},
	},
	// EIGHTTAP_SHARP (dav1d DAV1D_FILTER_8TAP_SHARP)
	{
		{-1, 1, -3, 63, 4, -1, 1, 0},
		{-1, 3, -6, 62, 8, -3, 2, -1},
		{-1, 4, -9, 60, 13, -5, 3, -1},
		{-2, 5, -11, 58, 19, -7, 3, -1},
		{-2, 5, -11, 54, 24, -9, 4, -1},
		{-2, 5, -12, 50, 30, -10, 4, -1},
		{-2, 5, -12, 45, 35, -11, 5, -1},
		{-2, 6, -12, 40, 40, -12, 6, -2},
		{-1, 5, -11, 35, 45, -12, 5, -2},
		{-1, 4, -10, 30, 50, -12, 5, -2},
		{-1, 4, -9, 24, 54, -11, 5, -2},
		{-1, 3, -7, 19, 58, -11, 5, -2},
		{-1, 3, -5, 13, 60, -9, 4, -1},
		{-1, 2, -3, 8, 62, -6, 3, -1},
		{0, 1, -1, 4, 63, -3, 1, -1},
	},
}

// interpFilter8TapSmall contains narrower filters for small blocks (dim <= 4).
// [filter_type][subpel_position_minus_1][tap]
// filter_type: 0=REGULAR_SMALL (dav1d [3+REGULAR]), 1=SMOOTH_SMALL (dav1d [3+SMOOTH])
var interpFilter8TapSmall = [2][15][8]int8{
	// REGULAR small (dav1d table [3 + DAV1D_FILTER_8TAP_REGULAR])
	{
		{0, 0, -2, 63, 4, -1, 0, 0},
		{0, 0, -4, 61, 9, -2, 0, 0},
		{0, 0, -5, 58, 14, -3, 0, 0},
		{0, 0, -6, 55, 19, -4, 0, 0},
		{0, 0, -6, 51, 24, -5, 0, 0},
		{0, 0, -7, 47, 29, -5, 0, 0},
		{0, 0, -6, 42, 33, -5, 0, 0},
		{0, 0, -6, 38, 38, -6, 0, 0},
		{0, 0, -5, 33, 42, -6, 0, 0},
		{0, 0, -5, 29, 47, -7, 0, 0},
		{0, 0, -5, 24, 51, -6, 0, 0},
		{0, 0, -4, 19, 55, -6, 0, 0},
		{0, 0, -3, 14, 58, -5, 0, 0},
		{0, 0, -2, 9, 61, -4, 0, 0},
		{0, 0, -1, 4, 63, -2, 0, 0},
	},
	// SMOOTH small (dav1d table [3 + DAV1D_FILTER_8TAP_SMOOTH])
	{
		{0, 0, 15, 31, 17, 1, 0, 0},
		{0, 0, 13, 31, 18, 2, 0, 0},
		{0, 0, 11, 31, 20, 2, 0, 0},
		{0, 0, 10, 30, 21, 3, 0, 0},
		{0, 0, 9, 29, 22, 4, 0, 0},
		{0, 0, 8, 28, 23, 5, 0, 0},
		{0, 0, 7, 27, 24, 6, 0, 0},
		{0, 0, 6, 26, 26, 6, 0, 0},
		{0, 0, 6, 24, 27, 7, 0, 0},
		{0, 0, 5, 23, 28, 8, 0, 0},
		{0, 0, 4, 22, 29, 9, 0, 0},
		{0, 0, 3, 21, 30, 10, 0, 0},
		{0, 0, 2, 20, 31, 11, 0, 0},
		{0, 0, 2, 18, 31, 13, 0, 0},
		{0, 0, 1, 17, 31, 15, 0, 0},
	},
}

// interpFilterBilinear contains the 2-tap bilinear filter (dav1d [5], 64-sum, 15 entries).
var interpFilterBilinear = [15][8]int8{
	{0, 0, 0, 60, 4, 0, 0, 0},
	{0, 0, 0, 56, 8, 0, 0, 0},
	{0, 0, 0, 52, 12, 0, 0, 0},
	{0, 0, 0, 48, 16, 0, 0, 0},
	{0, 0, 0, 44, 20, 0, 0, 0},
	{0, 0, 0, 40, 24, 0, 0, 0},
	{0, 0, 0, 36, 28, 0, 0, 0},
	{0, 0, 0, 32, 32, 0, 0, 0},
	{0, 0, 0, 28, 36, 0, 0, 0},
	{0, 0, 0, 24, 40, 0, 0, 0},
	{0, 0, 0, 20, 44, 0, 0, 0},
	{0, 0, 0, 16, 48, 0, 0, 0},
	{0, 0, 0, 12, 52, 0, 0, 0},
	{0, 0, 0, 8, 56, 0, 0, 0},
	{0, 0, 0, 4, 60, 0, 0, 0},
}

// getFilterKernel returns the 8-tap filter kernel for a given filter type, sub-pixel position,
// and block dimension (in pixels). Returns nil if subpel==0 (integer position, no filtering needed).
// For blocks with dim <= 4, uses the narrower small-block filter.
// Matches dav1d GET_H_FILTER/GET_V_FILTER: subpel is 1-15, table indexed by subpel-1.
func getFilterKernel(filterType int, subpel int, dim int) *[8]int8 {
	subpel &= 15
	if subpel == 0 {
		return nil // integer position
	}
	if filterType == InterpFilterBilinear {
		return &interpFilterBilinear[subpel-1]
	}
	if filterType < 0 || filterType > 2 {
		filterType = 0 // default to regular
	}
	if dim <= 4 {
		// Small block: use narrower filter. SHARP maps to REGULAR for small blocks.
		// dav1d: 3 + (filter_type & 1) -> 0=REGULAR->small[0], 1=SMOOTH->small[1], 2=SHARP->small[0]
		return &interpFilter8TapSmall[filterType&1][subpel-1]
	}
	return &interpFilter8Tap[filterType][subpel-1]
}

// apply8TapFilter computes the 8-tap dot product of filter coefficients and source samples.
// This is the core FILTER_8TAP operation matching dav1d's macro.
func apply8TapFilter(f *[8]int8, s0, s1, s2, s3, s4, s5, s6, s7 int32) int32 {
	return int32(f[0])*s0 + int32(f[1])*s1 + int32(f[2])*s2 + int32(f[3])*s3 +
		int32(f[4])*s4 + int32(f[5])*s5 + int32(f[6])*s6 + int32(f[7])*s7
}

// motionCompensation performs motion compensation for one reference frame (put_8tap).
// Uses dav1d-compatible 64-sum filter coefficients and rounding constants.
//
// For 8-bit, intermediate_bits = 4:
//   - H-only: clip((sum + 34) >> 6)  [intermediate_rnd = 32 + ((1<<(6-4))>>1) = 34]
//   - V-only: clip((sum + 32) >> 6)
//   - 2D H:  (sum + 2) >> 2          [sh = 6 - intermediate_bits = 2]
//   - 2D V:  clip((sum + 512) >> 10)  [sh = 6 + intermediate_bits = 10]
func motionCompensation(dst []byte, dstW, dstH int,
	refBuf []byte, refStride, refW, refH int,
	baseX, baseY int, mv MV, filterTypeH, filterTypeV int) {

	// Use buffer dimensions for clamping, not visible frame dimensions.
	// The buffer includes padding rows with valid reconstructed data beyond
	// the visible frame. Using buffer bounds matches dav1d which reads from
	// the padded reference frame buffer.
	// Source position in 1/8 pel.
	srcX8 := baseX*8 + int(mv.Col)
	srcY8 := baseY*8 + int(mv.Row)

	// Integer and fractional parts.
	intX := srcX8 >> 3
	intY := srcY8 >> 3
	fracX := srcX8 & 7
	fracY := srcY8 & 7

	// Convert 1/8 pel fraction to filter position (0-15 range for 1/16 pel).
	subX := fracX * 2
	subY := fracY * 2

	// Get filter kernels (nil means integer position, no filtering).
	fh := getFilterKernel(filterTypeH, subX, dstW)
	fv := getFilterKernel(filterTypeV, subY, dstH)

	if fh == nil && fv == nil {
		// Integer position: just copy with clamping.
		for dy := 0; dy < dstH; dy++ {
			sy := clampInt(intY+dy, 0, refH-1)
			for dx := 0; dx < dstW; dx++ {
				sx := clampInt(intX+dx, 0, refW-1)
				dst[dy*dstW+dx] = refBuf[sy*refStride+sx]
			}
		}
		return
	}

	if fh != nil && fv != nil {
		// 2D separable filtering.
		// dav1d put_8tap 8-bit: intermediate_bits=4
		// H pass: (sum + 2) >> 2  [sh = 6-4 = 2, rnd = (1<<2)>>1 = 2]
		// V pass: clip((sum + 512) >> 10)  [sh = 6+4 = 10, rnd = (1<<10)>>1 = 512]
		tmpH := dstH + 7
		tmp := make([]int16, dstW*tmpH)

		for dy := 0; dy < tmpH; dy++ {
			sy := clampInt(intY+dy-3, 0, refH-1)
			for dx := 0; dx < dstW; dx++ {
				var sum int32
				for t := 0; t < 8; t++ {
					sx := clampInt(intX+dx+t-3, 0, refW-1)
					sum += int32(fh[t]) * int32(refBuf[sy*refStride+sx])
				}
				tmp[dy*dstW+dx] = int16((sum + 2) >> 2)
			}
		}

		for dy := 0; dy < dstH; dy++ {
			for dx := 0; dx < dstW; dx++ {
				var sum int32
				for t := 0; t < 8; t++ {
					sum += int32(fv[t]) * int32(tmp[(dy+t)*dstW+dx])
				}
				dst[dy*dstW+dx] = clipU8((sum + 512) >> 10)
			}
		}
		return
	}

	if fh != nil {
		// Horizontal-only filtering.
		// dav1d put_8tap H-only 8-bit: clip((sum + intermediate_rnd) >> 6)
		// intermediate_rnd = 32 + ((1 << (6-4)) >> 1) = 32 + 2 = 34
		for dy := 0; dy < dstH; dy++ {
			sy := clampInt(intY+dy, 0, refH-1)
			for dx := 0; dx < dstW; dx++ {
				var sum int32
				for t := 0; t < 8; t++ {
					sx := clampInt(intX+dx+t-3, 0, refW-1)
					sum += int32(fh[t]) * int32(refBuf[sy*refStride+sx])
				}
				dst[dy*dstW+dx] = clipU8((sum + 34) >> 6)
			}
		}
		return
	}

	// Vertical-only filtering.
	// dav1d put_8tap V-only 8-bit: clip((sum + 32) >> 6)
	for dy := 0; dy < dstH; dy++ {
		for dx := 0; dx < dstW; dx++ {
			sx := clampInt(intX+dx, 0, refW-1)
			var sum int32
			for t := 0; t < 8; t++ {
				sy := clampInt(intY+dy+t-3, 0, refH-1)
				sum += int32(fv[t]) * int32(refBuf[sy*refStride+sx])
			}
			dst[dy*dstW+dx] = clipU8((sum + 32) >> 6)
		}
	}
}

// motionCompensationChroma performs motion compensation for a chroma plane (put_8tap).
// MVs are in 1/16 pel for chroma in 4:2:0.
// Same rounding as luma motionCompensation.
func motionCompensationChroma(dst []byte, dstW, dstH int,
	refBuf []byte, refStride, refW, refH int,
	baseX, baseY int, mvCol, mvRow int32, filterTypeH, filterTypeV int,
	subX, subY int) {

	// Use buffer dimensions for clamping (see motionCompensation comment).
	// For 4:2:0 chroma, MV is in 1/16 pel.
	srcX16 := baseX*16 + int(mvCol)
	srcY16 := baseY*16 + int(mvRow)

	intCX := srcX16 >> 4
	intCY := srcY16 >> 4
	fracCX := srcX16 & 15
	fracCY := srcY16 & 15

	fh := getFilterKernel(filterTypeH, fracCX, dstW)
	fv := getFilterKernel(filterTypeV, fracCY, dstH)

	if fh == nil && fv == nil {
		// Integer position.
		for dy := 0; dy < dstH; dy++ {
			sy := clampInt(intCY+dy, 0, refH-1)
			for dx := 0; dx < dstW; dx++ {
				sx := clampInt(intCX+dx, 0, refW-1)
				dst[dy*dstW+dx] = refBuf[sy*refStride+sx]
			}
		}
		return
	}

	if fh != nil && fv != nil {
		// 2D separable: H (sum+2)>>2, V clip((sum+512)>>10)
		tmpH := dstH + 7
		tmp := make([]int16, dstW*tmpH)

		for dy := 0; dy < tmpH; dy++ {
			sy := clampInt(intCY+dy-3, 0, refH-1)
			for dx := 0; dx < dstW; dx++ {
				var sum int32
				for t := 0; t < 8; t++ {
					sx := clampInt(intCX+dx+t-3, 0, refW-1)
					sum += int32(fh[t]) * int32(refBuf[sy*refStride+sx])
				}
				tmp[dy*dstW+dx] = int16((sum + 2) >> 2)
			}
		}

		for dy := 0; dy < dstH; dy++ {
			for dx := 0; dx < dstW; dx++ {
				var sum int32
				for t := 0; t < 8; t++ {
					sum += int32(fv[t]) * int32(tmp[(dy+t)*dstW+dx])
				}
				dst[dy*dstW+dx] = clipU8((sum + 512) >> 10)
			}
		}
		return
	}

	if fh != nil {
		// Horizontal-only: clip((sum + 34) >> 6)
		for dy := 0; dy < dstH; dy++ {
			sy := clampInt(intCY+dy, 0, refH-1)
			for dx := 0; dx < dstW; dx++ {
				var sum int32
				for t := 0; t < 8; t++ {
					sx := clampInt(intCX+dx+t-3, 0, refW-1)
					sum += int32(fh[t]) * int32(refBuf[sy*refStride+sx])
				}
				dst[dy*dstW+dx] = clipU8((sum + 34) >> 6)
			}
		}
		return
	}

	// Vertical-only: clip((sum + 32) >> 6)
	for dy := 0; dy < dstH; dy++ {
		for dx := 0; dx < dstW; dx++ {
			sx := clampInt(intCX+dx, 0, refW-1)
			var sum int32
			for t := 0; t < 8; t++ {
				sy := clampInt(intCY+dy+t-3, 0, refH-1)
				sum += int32(fv[t]) * int32(refBuf[sy*refStride+sx])
			}
			dst[dy*dstW+dx] = clipU8((sum + 32) >> 6)
		}
	}
}

// motionCompensationPrep performs intermediate-precision MC for compound prediction (prep_8tap).
// Output is int16 at intermediate precision.
//
// For 8-bit, intermediate_bits=4, PREP_BIAS=0:
//   - Integer: pixel << 4
//   - H-only: (sum + 2) >> 2  [sh = 6-4 = 2, PREP_BIAS=0]
//   - V-only: (sum + 2) >> 2  [sh = 6-4 = 2, PREP_BIAS=0]
//   - 2D H:   (sum + 2) >> 2  [same as put 2D H]
//   - 2D V:   (sum + 32) >> 6  [sh = 6, rnd = 32, PREP_BIAS=0]
func motionCompensationPrep(dst []int16, dstW, dstH int,
	refBuf []byte, refStride, refW, refH int,
	baseX, baseY int, mv MV, filterTypeH, filterTypeV int) {

	srcX8 := baseX*8 + int(mv.Col)
	srcY8 := baseY*8 + int(mv.Row)

	intX := srcX8 >> 3
	intY := srcY8 >> 3
	fracX := srcX8 & 7
	fracY := srcY8 & 7

	subX := fracX * 2
	subY := fracY * 2

	fh := getFilterKernel(filterTypeH, subX, dstW)
	fv := getFilterKernel(filterTypeV, subY, dstH)

	if fh == nil && fv == nil {
		// Integer position: shift to intermediate scale.
		for dy := 0; dy < dstH; dy++ {
			sy := clampInt(intY+dy, 0, refH-1)
			for dx := 0; dx < dstW; dx++ {
				sx := clampInt(intX+dx, 0, refW-1)
				dst[dy*dstW+dx] = int16(refBuf[sy*refStride+sx]) << 4
			}
		}
		return
	}

	if fh != nil && fv != nil {
		// 2D separable prep.
		// H pass: (sum + 2) >> 2  [sh = 6-4 = 2]
		// V pass: (sum + 32) >> 6  [sh = 6, rnd = 32] - PREP_BIAS(=0)
		tmpH := dstH + 7
		tmp := make([]int16, dstW*tmpH)

		for dy := 0; dy < tmpH; dy++ {
			sy := clampInt(intY+dy-3, 0, refH-1)
			for dx := 0; dx < dstW; dx++ {
				var sum int32
				for t := 0; t < 8; t++ {
					sx := clampInt(intX+dx+t-3, 0, refW-1)
					sum += int32(fh[t]) * int32(refBuf[sy*refStride+sx])
				}
				tmp[dy*dstW+dx] = int16((sum + 2) >> 2)
			}
		}

		for dy := 0; dy < dstH; dy++ {
			for dx := 0; dx < dstW; dx++ {
				var sum int32
				for t := 0; t < 8; t++ {
					sum += int32(fv[t]) * int32(tmp[(dy+t)*dstW+dx])
				}
				dst[dy*dstW+dx] = int16((sum + 32) >> 6)
			}
		}
		return
	}

	if fh != nil {
		// H-only prep: (sum + 2) >> 2 - PREP_BIAS(=0)
		// dav1d: DAV1D_FILTER_8TAP_RND(src, x, fh, 1, 6 - intermediate_bits) - PREP_BIAS
		// sh = 6 - 4 = 2, rnd = (1<<2)>>1 = 2

		for dy := 0; dy < dstH; dy++ {
			sy := clampInt(intY+dy, 0, refH-1)
			for dx := 0; dx < dstW; dx++ {
				var sum int32
				for t := 0; t < 8; t++ {
					sx := clampInt(intX+dx+t-3, 0, refW-1)
					sum += int32(fh[t]) * int32(refBuf[sy*refStride+sx])
				}
				dst[dy*dstW+dx] = int16((sum + 2) >> 2)
			}
		}
		return
	}

	// V-only prep: (sum + 2) >> 2 - PREP_BIAS(=0)
	// dav1d: DAV1D_FILTER_8TAP_RND(src, x, fv, src_stride, 6 - intermediate_bits) - PREP_BIAS
	// sh = 6 - 4 = 2, rnd = (1<<2)>>1 = 2
	for dy := 0; dy < dstH; dy++ {
		for dx := 0; dx < dstW; dx++ {
			sx := clampInt(intX+dx, 0, refW-1)
			var sum int32
			for t := 0; t < 8; t++ {
				sy := clampInt(intY+dy+t-3, 0, refH-1)
				sum += int32(fv[t]) * int32(refBuf[sy*refStride+sx])
			}
			dst[dy*dstW+dx] = int16((sum + 2) >> 2)
		}
	}
}

// motionCompensationChromaPrep performs intermediate-precision chroma MC for compound prediction.
// Same rounding as motionCompensationPrep.
func motionCompensationChromaPrep(dst []int16, dstW, dstH int,
	refBuf []byte, refStride, refW, refH int,
	baseX, baseY int, mvCol, mvRow int32, filterTypeH, filterTypeV int,
	chromaSubX, chromaSubY int) {

	srcX16 := baseX*16 + int(mvCol)
	srcY16 := baseY*16 + int(mvRow)

	intCX := srcX16 >> 4
	intCY := srcY16 >> 4
	fracCX := srcX16 & 15
	fracCY := srcY16 & 15

	fh := getFilterKernel(filterTypeH, fracCX, dstW)
	fv := getFilterKernel(filterTypeV, fracCY, dstH)

	if fh == nil && fv == nil {
		for dy := 0; dy < dstH; dy++ {
			sy := clampInt(intCY+dy, 0, refH-1)
			for dx := 0; dx < dstW; dx++ {
				sx := clampInt(intCX+dx, 0, refW-1)
				dst[dy*dstW+dx] = int16(refBuf[sy*refStride+sx]) << 4
			}
		}
		return
	}

	if fh != nil && fv != nil {
		// 2D chroma prep: H (sum+2)>>2, V (sum+32)>>6
		tmpH := dstH + 7
		tmp := make([]int16, dstW*tmpH)

		for dy := 0; dy < tmpH; dy++ {
			sy := clampInt(intCY+dy-3, 0, refH-1)
			for dx := 0; dx < dstW; dx++ {
				var sum int32
				for t := 0; t < 8; t++ {
					sx := clampInt(intCX+dx+t-3, 0, refW-1)
					sum += int32(fh[t]) * int32(refBuf[sy*refStride+sx])
				}
				tmp[dy*dstW+dx] = int16((sum + 2) >> 2)
			}
		}

		for dy := 0; dy < dstH; dy++ {
			for dx := 0; dx < dstW; dx++ {
				var sum int32
				for t := 0; t < 8; t++ {
					sum += int32(fv[t]) * int32(tmp[(dy+t)*dstW+dx])
				}
				dst[dy*dstW+dx] = int16((sum + 32) >> 6)
			}
		}
		return
	}

	if fh != nil {
		// H-only chroma prep: (sum + 2) >> 2
		for dy := 0; dy < dstH; dy++ {
			sy := clampInt(intCY+dy, 0, refH-1)
			for dx := 0; dx < dstW; dx++ {
				var sum int32
				for t := 0; t < 8; t++ {
					sx := clampInt(intCX+dx+t-3, 0, refW-1)
					sum += int32(fh[t]) * int32(refBuf[sy*refStride+sx])
				}
				dst[dy*dstW+dx] = int16((sum + 2) >> 2)
			}
		}
		return
	}

	// V-only chroma prep: (sum + 2) >> 2
	for dy := 0; dy < dstH; dy++ {
		for dx := 0; dx < dstW; dx++ {
			sx := clampInt(intCX+dx, 0, refW-1)
			var sum int32
			for t := 0; t < 8; t++ {
				sy := clampInt(intCY+dy+t-3, 0, refH-1)
				sum += int32(fv[t]) * int32(refBuf[sy*refStride+sx])
			}
			dst[dy*dstW+dx] = int16((sum + 2) >> 2)
		}
	}
}

// clampInt clamps v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clipU8 clips an integer to [0, 255] and returns it as byte.
func clipU8(v int32) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}
