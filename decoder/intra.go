// Package decoder implements an AV1 bitstream decoder.
//
// This file implements the 13 AV1 intra prediction modes as specified
// in Section 7.11.2 of the AV1 specification.
package decoder


// Intra prediction mode constants. AV1 spec Section 6.4.2, Table 3.
const (
	DC_PRED      = 0  // Average of above and left neighbors
	V_PRED       = 1  // Vertical: copy above row
	H_PRED       = 2  // Horizontal: copy left column
	D45_PRED     = 3  // Diagonal 45 degrees (up-right)
	D135_PRED    = 4  // Diagonal 135 degrees (up-left)
	D113_PRED    = 5  // Diagonal 113 degrees
	D157_PRED    = 6  // Diagonal 157 degrees
	D203_PRED    = 7  // Diagonal 203 degrees
	D67_PRED     = 8  // Diagonal 67 degrees
	SMOOTH_PRED  = 9  // Weighted average smooth prediction
	SMOOTH_V     = 10 // Vertically-weighted smooth
	SMOOTH_H     = 11 // Horizontally-weighted smooth
	PAETH_PRED   = 12 // Paeth predictor
	FILTER_PRED  = 13 // Filter intra prediction (up to 32x32)
	NumIntraModes = 13
)

// ctz returns the number of trailing zero bits in v (count trailing zeros).
// For v == 0, returns 0 (undefined in practice, but safe).
func ctz(v int) int {
	if v == 0 {
		return 0
	}
	n := 0
	for v&1 == 0 {
		v >>= 1
		n++
	}
	return n
}

// modeToAngle maps each intra prediction mode to its nominal angle in degrees.
// Non-directional modes (DC, SMOOTH, PAETH) have angle 0.
// AV1 spec Section 7.11.2.4.
var modeToAngle = [NumIntraModes]int{
	0,   // DC_PRED
	90,  // V_PRED
	180, // H_PRED
	45,  // D45_PRED
	135, // D135_PRED
	113, // D113_PRED
	157, // D157_PRED
	203, // D203_PRED
	67,  // D67_PRED
	0,   // SMOOTH_PRED
	0,   // SMOOTH_V
	0,   // SMOOTH_H
	0,   // PAETH_PRED
}

// smoothWeights contains the weight arrays used for SMOOTH prediction modes.
// Indexed by block dimension (4, 8, 16, 32, 64).
// AV1 spec Section 7.11.2.8, Table 2.
var smoothWeights = map[int][]uint8{
	4: {255, 149, 85, 64},
	8: {255, 197, 146, 105, 73, 50, 37, 32},
	16: {255, 225, 196, 170, 145, 123, 102, 84,
		68, 54, 43, 33, 26, 20, 17, 16},
	32: {255, 240, 225, 210, 196, 182, 169, 157,
		145, 133, 122, 111, 101, 92, 83, 74,
		66, 59, 52, 45, 39, 34, 29, 25,
		21, 17, 14, 12, 10, 9, 8, 8},
	64: {255, 248, 240, 233, 225, 218, 210, 203,
		196, 189, 182, 176, 169, 163, 156, 150,
		144, 138, 133, 127, 121, 116, 111, 106,
		101, 96, 91, 86, 82, 77, 73, 69,
		65, 61, 57, 54, 50, 47, 44, 41,
		38, 35, 32, 29, 27, 25, 22, 20,
		18, 16, 15, 13, 12, 10, 9, 8,
		7, 6, 6, 5, 5, 4, 4, 4},
}

// dr_intra_derivative maps from angle index to the derivative used for
// directional intra prediction. The derivative approximates 256 * tan(angle)
// or 256 * cot(angle) depending on the angle quadrant.
// drIntraDerivative is the AV1 directional prediction derivative table.
// Indexed by angle >> 1. Matches dav1d's dav1d_dr_intra_derivative[44].
// Uses 6 fractional bits: derivative = 64 * tan(angle) approximately.
// Entries that are 0 correspond to unused angle/2 indices.
var drIntraDerivative = [44]int{
	//        Angles:
	0,        //
	1023, 0,  //  3,  93, 183
	547,      //  6,  96, 186
	372, 0, 0, //  9,  99, 189
	273,      // 14, 104, 194
	215, 0,   // 17, 107, 197
	178,      // 20, 110, 200
	151, 0,   // 23, 113, 203
	132,      // 26, 116, 206
	116, 0,   // 29, 119, 209
	102, 0,   // 32, 122, 212
	90,       // 36, 126, 216
	80, 0,    // 39, 129, 219
	71,       // 42, 132, 222
	64, 0,    // 45, 135, 225
	57,       // 48, 138, 228
	51, 0,    // 51, 141, 231
	45, 0,    // 54, 144, 234
	40,       // 58, 148, 238
	35, 0,    // 61, 151, 241
	31,       // 64, 154, 244
	27, 0,    // 67, 157, 247
	23,       // 70, 160, 250
	19, 0,    // 73, 163, 253
	15, 0,    // 76, 166, 256
	11, 0,    // 81, 171, 261
	7,        // 84, 174, 264
	3,        // 87, 177, 267
}

// clip constrains val to the range [lo, hi].
func clip(val, lo, hi int) int {
	if val < lo {
		return lo
	}
	if val > hi {
		return hi
	}
	return val
}

// abs returns the absolute value of x.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// iclip constrains i to [from, to-1]. Matches dav1d's iclip().
func iclip(i, from, to int) int {
	if i < from {
		return from
	}
	if i >= to {
		return to - 1
	}
	return i
}

// Intra edge filter kernels. Matches dav1d's filter_edge kernels.
var edgeFilterKernels = [3][5]int{
	{0, 4, 8, 4, 0},
	{0, 5, 6, 5, 0},
	{2, 4, 4, 4, 2},
}

// getFilterStrength returns the intra edge filter strength (0-3) based on
// block size (wh = width+height), angle offset, and whether neighbors use
// smooth mode. Matches dav1d's get_filter_strength().
func getFilterStrength(wh, angle int, isSm bool) int {
	if isSm {
		if wh <= 8 {
			if angle >= 64 {
				return 2
			}
			if angle >= 40 {
				return 1
			}
		} else if wh <= 16 {
			if angle >= 48 {
				return 2
			}
			if angle >= 20 {
				return 1
			}
		} else if wh <= 24 {
			if angle >= 4 {
				return 3
			}
		} else {
			return 3
		}
	} else {
		if wh <= 8 {
			if angle >= 56 {
				return 1
			}
		} else if wh <= 16 {
			if angle >= 40 {
				return 1
			}
		} else if wh <= 24 {
			if angle >= 32 {
				return 3
			}
			if angle >= 16 {
				return 2
			}
			if angle >= 8 {
				return 1
			}
		} else if wh <= 32 {
			if angle >= 32 {
				return 3
			}
			if angle >= 4 {
				return 2
			}
			return 1
		} else {
			return 3
		}
	}
	return 0
}

// getUpsample returns whether upsampling should be applied.
// Matches dav1d's get_upsample().
func getUpsample(wh, angle int, isSm bool) bool {
	shift := 0
	if isSm {
		shift = 1
	}
	return angle < 40 && wh <= (16>>shift)
}

// filterEdge applies a 5-tap smoothing filter to reference edge samples.
// out[0..sz-1]: output
// limFrom, limTo: filtering range within [0, sz); outside copied with clamp
// in: input array, accessed via in[offs+i] for logical index i
// offs: offset into in[] for logical index 0
// minIdx, maxIdx: absolute min/max valid indices in in[]
// strength: 1-3
// Matches dav1d's filter_edge() using offset to avoid negative slice indices.
func filterEdge(out []byte, sz, limFrom, limTo int, in []byte, offs, minIdx, maxIdx, strength int) {
	kernel := edgeFilterKernels[strength-1]
	clampIn := func(idx int) byte {
		if idx < minIdx {
			idx = minIdx
		}
		if idx > maxIdx {
			idx = maxIdx
		}
		return in[idx]
	}
	i := 0
	for ; i < sz && i < limFrom; i++ {
		out[i] = clampIn(offs + i)
	}
	for ; i < limTo && i < sz; i++ {
		s := 0
		for j := 0; j < 5; j++ {
			s += int(clampIn(offs+i-2+j)) * kernel[j]
		}
		out[i] = byte(clip((s+8)>>4, 0, 255))
	}
	for ; i < sz; i++ {
		out[i] = clampIn(offs + i)
	}
}

// upsampleEdge upsamples edge samples by 2x using a 4-tap filter.
// out[0..2*hsz-1]: output (even = original, odd = interpolated)
// in: input array, accessed via in[offs+i] for logical index i
// offs: offset into in[] for logical index 0
// minIdx, maxIdx: absolute min/max valid indices in in[]
// Matches dav1d's upsample_edge() using offset to avoid negative slice indices.
func upsampleEdge(out []byte, hsz int, in []byte, offs, minIdx, maxIdx int) {
	kernel := [4]int{-1, 9, 9, -1}
	clampIn := func(idx int) byte {
		if idx < minIdx {
			idx = minIdx
		}
		if idx > maxIdx {
			idx = maxIdx
		}
		return in[idx]
	}
	i := 0
	for ; i < hsz-1; i++ {
		out[i*2] = clampIn(offs + i)
		s := 0
		for j := 0; j < 4; j++ {
			s += int(clampIn(offs+i+j-1)) * kernel[j]
		}
		out[i*2+1] = byte(clip((s+8)>>4, 0, 255))
	}
	out[i*2] = clampIn(offs + i)
}

// PredictIntra generates an intra-predicted block for 8-bit content.
//
// Parameters:
//   - mode: AV1 intra mode (0-12)
//   - dst: output prediction buffer, row-major
//   - stride: row stride of dst in bytes
//   - w, h: block width and height (4, 8, 16, 32, or 64)
//   - above: reference pixels above the block; above[0] is the pixel directly
//     above the top-left corner. Must contain at least w pixels; for directional
//     modes, 2*w pixels are preferred. above[-1] (index convention: the caller
//     may prepend the top-left pixel at index 0 and shift the rest, or pass it
//     via left) is the top-left corner pixel, encoded as left[0] when the left
//     array starts one row above.
//   - left: reference pixels to the left; left[0] is the pixel to the left of
//     row 0. Must contain at least h pixels; 2*h preferred for directional modes.
//   - bitDepth: 8, 10, or 12
//
// The top-left corner pixel (above[-1] in spec notation) is expected at
// left[-1] or equivalently as the last element before above[0] in the caller's
// reference buffer. For this function, the caller should pass topLeft explicitly
// via the element left[0] being the pixel to the left of row 0, and above[0]
// being the pixel above column 0. The top-left pixel is inferred as above[-1]
// which the caller should place at a position accessible by the prediction.
// For simplicity, many callers will pass topLeft separately -- we accept it
// as the convention that left has at least h entries and above has at least w
// entries, and the top-left pixel (needed by PAETH and D135 etc.) is passed
// as above index -1. Since Go slices cannot have negative indices, we adopt
// the convention:
//
//	above: [topLeft, above0, above1, ..., aboveN]  (length >= w+1)
//	left:  [topLeft, left0,  left1,  ..., leftN]   (length >= h+1)
//
// where above[0] == left[0] == topLeft.
// above[1] is the pixel above column 0, above[2] above column 1, etc.
// left[1] is the pixel to the left of row 0, left[2] to the left of row 1, etc.
//
// For directional modes that reference extended above/left, the caller should
// provide up to 2*w+1 entries in above and 2*h+1 entries in left.
//
// AV1 spec Section 7.11.2.
func PredictIntra(mode int, dst []byte, stride int, w, h int, above []byte, left []byte, hasAbove, hasLeft bool, bitDepth int) {
	maxVal := (1 << bitDepth) - 1

	switch mode {
	case DC_PRED:
		predictDC8(dst, stride, w, h, above, left, hasAbove, hasLeft, bitDepth)
	case V_PRED:
		predictV8(dst, stride, w, h, above)
	case H_PRED:
		predictH8(dst, stride, w, h, left)
	case D45_PRED:
		predictDirectional8(dst, stride, w, h, above, left, 45, maxVal, false, false)
	case D135_PRED:
		predictDirectional8(dst, stride, w, h, above, left, 135, maxVal, false, false)
	case D113_PRED:
		predictDirectional8(dst, stride, w, h, above, left, 113, maxVal, false, false)
	case D157_PRED:
		predictDirectional8(dst, stride, w, h, above, left, 157, maxVal, false, false)
	case D203_PRED:
		predictDirectional8(dst, stride, w, h, above, left, 203, maxVal, false, false)
	case D67_PRED:
		predictDirectional8(dst, stride, w, h, above, left, 67, maxVal, false, false)
	case SMOOTH_PRED:
		predictSmooth8(dst, stride, w, h, above, left, maxVal)
	case SMOOTH_V:
		predictSmoothV8(dst, stride, w, h, above, left, maxVal)
	case SMOOTH_H:
		predictSmoothH8(dst, stride, w, h, above, left, maxVal)
	case PAETH_PRED:
		// dav1d mode conversion: PAETH falls back when references unavailable.
		switch {
		case !hasAbove && !hasLeft:
			predictDC8(dst, stride, w, h, above, left, false, false, bitDepth)
		case hasAbove && !hasLeft:
			predictV8(dst, stride, w, h, above)
		case !hasAbove && hasLeft:
			predictH8(dst, stride, w, h, left)
		default:
			predictPaeth8(dst, stride, w, h, above, left, maxVal)
		}
	case 13: // UV_CFL_PRED: base is DC prediction; full CFL adds alpha * AC_luma
		predictDC8(dst, stride, w, h, above, left, hasAbove, hasLeft, bitDepth)
	}
}

// PredictIntraWithDelta generates an intra-predicted block with an angle delta
// applied to directional modes. angleDelta is in the range [-3, +3] and each
// unit represents 3 degrees.
// AV1 spec Section 7.11.2.4.
func PredictIntraWithDelta(mode int, angleDelta int, dst []byte, stride int, w, h int, above []byte, left []byte, bitDepth int, enableEdgeFilter bool, isSm bool) {
	if mode < V_PRED || mode > D67_PRED {
		// Non-directional mode; ignore delta.
		PredictIntra(mode, dst, stride, w, h, above, left, true, true, bitDepth)
		return
	}
	maxVal := (1 << bitDepth) - 1
	angle := modeToAngle[mode] + angleDelta*3
	predictDirectional8(dst, stride, w, h, above, left, angle, maxVal, enableEdgeFilter, isSm)
}

// predictDC8 implements DC_PRED for 8-bit. AV1 spec Section 7.11.2.1.
func predictDC8(dst []byte, stride, w, h int, above, left []byte, haveAbove, haveLeft bool, bitDepth int) {

	var avg int

	switch {
	case haveAbove && haveLeft:
		sum := 0
		for i := 1; i <= w && i < len(above); i++ {
			sum += int(above[i])
		}
		for j := 1; j <= h && j < len(left); j++ {
			sum += int(left[j])
		}
		// Match dav1d dc_gen: dc >>= ctz(w+h), then multiplier for non-square.
		count := w + h
		dc := (count >> 1) + sum
		dc >>= uint(ctz(count))
		if w != h {
			mult := 0x5556 // MULTIPLIER_1x2
			if w > h*2 || h > w*2 {
				mult = 0x3334 // MULTIPLIER_1x4
			}
			dc = (dc * mult) >> 16
		}
		avg = dc

	case haveAbove:
		sum := 0
		for i := 1; i <= w && i < len(above); i++ {
			sum += int(above[i])
		}
		avg = (sum + w/2) / w

	case haveLeft:
		sum := 0
		for j := 1; j <= h && j < len(left); j++ {
			sum += int(left[j])
		}
		avg = (sum + h/2) / h

	default:
		avg = 1 << (bitDepth - 1)
	}

	if avg < 0 {
		avg = 0
	} else if avg > 255 {
		avg = 255
	}
	val := byte(avg)

	for y := 0; y < h; y++ {
		off := y * stride
		for x := 0; x < w; x++ {
			dst[off+x] = val
		}
	}
}

// predictV8 implements V_PRED for 8-bit. AV1 spec Section 7.11.2.2.
func predictV8(dst []byte, stride, w, h int, above []byte) {
	for y := 0; y < h; y++ {
		off := y * stride
		for x := 0; x < w; x++ {
			dst[off+x] = above[1+x]
		}
	}
}

// predictH8 implements H_PRED for 8-bit. AV1 spec Section 7.11.2.3.
func predictH8(dst []byte, stride, w, h int, left []byte) {
	for y := 0; y < h; y++ {
		off := y * stride
		val := left[1+y]
		for x := 0; x < w; x++ {
			dst[off+x] = val
		}
	}
}

// getDx returns the dx derivative for directional intra prediction.
// Indexed by angle >> 1 into drIntraDerivative. Matches dav1d convention.
// For angle < 90 (zone 1): dx = derivative[angle >> 1]
// For 90 < angle < 180 (zone 2): dx = derivative[(180-angle) >> 1]
func getDx(angle int) int {
	if angle > 0 && angle < 90 {
		idx := angle >> 1
		if idx < len(drIntraDerivative) {
			return drIntraDerivative[idx]
		}
	} else if angle > 90 && angle < 180 {
		idx := (180 - angle) >> 1
		if idx < len(drIntraDerivative) {
			return drIntraDerivative[idx]
		}
	}
	return 0
}

// getDy returns the dy derivative for directional intra prediction.
// For 90 < angle < 180 (zone 2): dy = derivative[(angle-90) >> 1]
// For 180 < angle < 270 (zone 3): dy = derivative[(270-angle) >> 1]
func getDy(angle int) int {
	if angle > 90 && angle < 180 {
		idx := (angle - 90) >> 1
		if idx < len(drIntraDerivative) {
			return drIntraDerivative[idx]
		}
	} else if angle > 180 && angle < 270 {
		idx := (270 - angle) >> 1
		if idx < len(drIntraDerivative) {
			return drIntraDerivative[idx]
		}
	}
	return 0
}

// predictDirectional8 implements directional prediction for 8-bit at an
// arbitrary angle. This covers D45, D67, D113, D135, D157, D203 and all
// angle-delta variants.
//
// The algorithm works differently depending on the angle quadrant:
//   - angle < 90: reference is above; project upward and right
//   - angle == 90: pure vertical (V_PRED)
//   - 90 < angle < 180: reference is above+left; project from above, shifting left
//   - angle == 180: pure horizontal (H_PRED)
//   - 180 < angle < 270: reference is left; project leftward and down
//
// AV1 spec Section 7.11.2.4.
func predictDirectional8(dst []byte, stride, w, h int, above, left []byte, angle, maxVal int, enableEdgeFilter bool, isSm bool) {
	dx := getDx(angle)
	dy := getDy(angle)

	switch {
	case angle > 0 && angle < 90:
		predictDirAbove8(dst, stride, w, h, above, dx, maxVal, angle, enableEdgeFilter, isSm)

	case angle == 90:
		predictV8(dst, stride, w, h, above)

	case angle > 90 && angle < 180:
		predictDirAboveLeft8(dst, stride, w, h, above, left, dx, dy, maxVal, angle, enableEdgeFilter, isSm)

	case angle == 180:
		predictH8(dst, stride, w, h, left)

	case angle > 180 && angle < 270:
		predictDirLeft8(dst, stride, w, h, above, left, dy, maxVal, angle, enableEdgeFilter, isSm)
	}
}

// predictDirAbove8 handles directional prediction for angles in (0, 90).
// Matches dav1d's ipred_z1_c. Uses 6 fractional bits.
// above[0]=topLeft, above[1..]=above pixels (extended for directional).
func predictDirAbove8(dst []byte, stride, w, h int, above []byte, dx, maxVal int, angle int, enableEdgeFilter, isSm bool) {
	wh := w + h
	maxSrc := w + min(w, h)
	if maxSrc > len(above)-1 {
		maxSrc = len(above) - 1
	}

	// Prepare top reference. dav1d: top = &topleft_in[1], using above[0] as topleft.
	// In our convention: above[0]=topLeft, above[1+i] = i-th above pixel.
	// upsampleEdge/filterEdge use offs=1 to start at above[1] with above[0] accessible.
	upsampleAbove := enableEdgeFilter && getUpsample(wh, 90-angle, isSm)
	var top []byte
	var maxBaseX int

	if upsampleAbove {
		topBuf := make([]byte, 2*wh)
		upsampleEdge(topBuf, wh, above, 1, 0, maxSrc)
		top = topBuf
		maxBaseX = 2*wh - 2
		dx <<= 1
	} else {
		strength := 0
		if enableEdgeFilter {
			strength = getFilterStrength(wh, 90-angle, isSm)
		}
		if strength > 0 {
			topBuf := make([]byte, wh)
			filterEdge(topBuf, wh, 0, wh, above, 1, 0, maxSrc, strength)
			top = topBuf
			maxBaseX = wh - 1
		} else {
			top = above[1:]
			maxBaseX = maxSrc - 1
		}
	}
	if maxBaseX < 0 {
		maxBaseX = 0
	}

	baseInc := 1
	if upsampleAbove {
		baseInc = 2
	}

	for y := 0; y < h; y++ {
		off := y * stride
		xpos := (y + 1) * dx
		frac := xpos & 0x3E
		for x, base := 0, xpos>>6; x < w; x, base = x+1, base+baseInc {
			if base < maxBaseX {
				v := int(top[base])*(64-frac) + int(top[base+1])*frac
				dst[off+x] = byte(clip((v+32)>>6, 0, maxVal))
			} else {
				idx := maxBaseX
				if idx >= len(top) {
					idx = len(top) - 1
				}
				for xx := x; xx < w; xx++ {
					dst[off+xx] = top[idx]
				}
				break
			}
		}
	}
}

// predictDirLeft8 handles directional prediction for angles in (180, 270).
// Matches dav1d's ipred_z3_c. Uses 6 fractional bits.
// In dav1d, z3 uses a combined reference: left[0]=first left pixel, left[1]=topLeft,
// left[2]=first above pixel, etc. (left = &topleft_in[-1]).
// Our convention: above[0]=left[0]=topLeft, above[1..]=above, left[1..]=left going down.
func predictDirLeft8(dst []byte, stride, w, h int, above, left []byte, dy, maxVal int, angle int, enableEdgeFilter, isSm bool) {
	// Build reference strip matching dav1d's z3 convention.
	// dav1d: left = &topleft_in[-1], accesses left[-base] going DOWN the left column:
	//   left[0]  = topleft_in[-1]  = first left pixel (our left[1])
	//   left[-1] = topleft_in[-2]  = second left pixel (our left[2])
	//   left[-2] = topleft_in[-3]  = third left pixel (our left[3])
	//   ...continuing down through left column then bottom-left extension.
	// Our ref[base] maps to dav1d's left[-base]:
	//   ref[0] = left[1], ref[1] = left[2], ..., ref[i] = left[i+1]
	maxBaseY := h + min(w, h) - 1
	refLen := maxBaseY + 2
	ref := make([]byte, refLen)

	for i := 0; i < refLen; i++ {
		idx := i + 1 // left[i+1] in our convention
		if idx < len(left) {
			ref[i] = left[idx]
		} else if i > 0 {
			ref[i] = ref[i-1] // extend with last available
		}
	}

	// Apply edge filtering/upsampling if enabled.
	// For z3, dav1d filters/upsamples the combined strip stored bottom-to-top.
	// dav1d: filter_edge(left_out, w+h, 0, w+h, &topleft_in[-(w+h)], max(w-h,0), w+h+1, strength)
	// Then left = &left_out[w+h-1], giving left[0] = first left pixel, accessing backwards.
	// For simplicity, we build the reversed strip, filter it, then reverse back.
	wh := w + h
	upsampleLeft := enableEdgeFilter && getUpsample(wh, angle-180, isSm)

	// Build reversed strip for filter/upsample: matches dav1d's &topleft_in[-(w+h)].
	// Goes from left[wh] (bottom) up to left[0] (topLeft): wh+1 elements.
	buildRevStrip := func() []byte {
		revLen := wh + 1
		rev := make([]byte, revLen)
		for i := 0; i < revLen; i++ {
			leftIdx := wh - i // left[wh], left[wh-1], ..., left[0]
			if leftIdx >= 0 && leftIdx < len(left) {
				rev[i] = left[leftIdx]
			} else if i > 0 {
				rev[i] = rev[i-1]
			}
		}
		return rev
	}

	if upsampleLeft {
		rev := buildRevStrip()
		upBuf := make([]byte, 2*wh)
		upsampleEdge(upBuf, wh, rev, 0, max(w-h, 0), wh)
		// dav1d: left = &left_out[2*(w+h)-2], accesses left[-base] going backward
		// Map: ref2[base] = upBuf[2*(wh)-2 - base]
		ref2 := make([]byte, 2*wh)
		for i := 0; i < 2*wh-1; i++ {
			ref2[i] = upBuf[2*wh-2-i]
		}
		ref2[2*wh-1] = ref2[2*wh-2] // extend last
		ref = ref2
		maxBaseY = 2*wh - 2
		dy <<= 1
	} else {
		strength := 0
		if enableEdgeFilter {
			strength = getFilterStrength(wh, angle-180, isSm)
		}
		if strength > 0 {
			rev := buildRevStrip()
			filtBuf := make([]byte, wh)
			filterEdge(filtBuf, wh, 0, wh, rev, 0, max(w-h, 0), wh, strength)
			// dav1d: left = &left_out[w+h-1], accesses left[-base]
			// Map: ref2[base] = filtBuf[wh-1-base]
			ref2 := make([]byte, wh+2)
			for i := 0; i < wh; i++ {
				ref2[i] = filtBuf[wh-1-i]
			}
			ref = ref2
			maxBaseY = wh - 1
		}
		// else: ref already set up correctly
	}
	if maxBaseY < 0 {
		maxBaseY = 0
	}

	baseInc := 1
	if upsampleLeft {
		baseInc = 2
	}

	for x := 0; x < w; x++ {
		ypos := (x + 1) * dy
		frac := ypos & 0x3E
		base := ypos >> 6
		for y := 0; y < h; y++ {
			if base < maxBaseY && base+1 < len(ref) {
				v := int(ref[base])*(64-frac) + int(ref[base+1])*frac
				dst[y*stride+x] = byte(clip((v+32)>>6, 0, maxVal))
			} else {
				idx := maxBaseY
				if idx >= len(ref) {
					idx = len(ref) - 1
				}
				for yy := y; yy < h; yy++ {
					dst[yy*stride+x] = ref[idx]
				}
				break
			}
			base += baseInc
		}
	}
}

// predictDirAboveLeft8 handles directional prediction for angles in (90, 180).
// Matches dav1d's ipred_z2_c. Uses 6 fractional bits.
// above[0]=topLeft, above[1..]=above pixels.
// left[0]=topLeft, left[1..]=left pixels.
func predictDirAboveLeft8(dst []byte, stride, w, h int, above, left []byte, dx, dy, maxVal int, angle int, enableEdgeFilter, isSm bool) {
	wh := w + h

	upsampleAbove := enableEdgeFilter && getUpsample(wh, angle-90, isSm)
	upsampleLeft := enableEdgeFilter && getUpsample(wh, 180-angle, isSm)

	// Build above reference: topRef[0]=topLeft, topRef[1..]=above pixels.
	var topRef []byte
	if upsampleAbove {
		topRef = make([]byte, 2*(w+1))
		maxAbove := w
		if maxAbove >= len(above) {
			maxAbove = len(above) - 1
		}
		upsampleEdge(topRef, w+1, above, 0, 0, maxAbove)
		dx <<= 1
	} else {
		strength := 0
		if enableEdgeFilter {
			strength = getFilterStrength(wh, angle-90, isSm)
		}
		topRef = make([]byte, w+1)
		if strength > 0 {
			filtBuf := make([]byte, w)
			maxW := w
			if maxW >= len(above) {
				maxW = len(above) - 1
			}
			filterEdge(filtBuf, w, 0, w, above, 1, 0, maxW, strength)
			topRef[0] = above[0]
			copy(topRef[1:], filtBuf)
		} else {
			maxCopy := w + 1
			if maxCopy > len(above) {
				maxCopy = len(above)
			}
			copy(topRef, above[:maxCopy])
		}
	}

	// Build left reference buffer.
	// leftBuf[k] corresponds to topleft[-k] in dav1d convention:
	//   leftBuf[0] = topLeft, leftBuf[1] = first left pixel, etc.
	var leftBuf []byte
	leftBufUps := 0
	if upsampleLeft {
		leftBufUps = 1
	}

	if upsampleLeft {
		// dav1d: upsample_edge(&topleft[-h*2], h+1, &topleft_in[-h], 0, h+1)
		// Source in dav1d order: topleft_in[-h], ..., topleft_in[0]
		// = our left[h], left[h-1], ..., left[0]
		srcLen := h + 1
		src := make([]byte, srcLen)
		for i := 0; i < srcLen; i++ {
			idx := h - i
			if idx < len(left) {
				src[i] = left[idx]
			} else {
				src[i] = left[len(left)-1]
			}
		}

		upBuf := make([]byte, 2*srcLen)
		upsampleEdge(upBuf, srcLen, src, 0, 0, srcLen-1)

		// Map upBuf to leftBuf: upBuf[2*h-k] = topleft[-k] = leftBuf[k]
		leftBuf = make([]byte, 2*h+1)
		for k := 0; k <= 2*h; k++ {
			leftBuf[k] = upBuf[2*h-k]
		}
		dy <<= 1
	} else {
		strength := 0
		if enableEdgeFilter {
			strength = getFilterStrength(wh, 180-angle, isSm)
		}

		if strength > 0 {
			// Source in dav1d order: topleft_in[-h..-0]
			srcLen := h + 1
			src := make([]byte, srcLen)
			for i := 0; i < srcLen; i++ {
				idx := h - i
				if idx < len(left) {
					src[i] = left[idx]
				} else {
					src[i] = left[len(left)-1]
				}
			}
			filtBuf := make([]byte, h)
			filterEdge(filtBuf, h, 0, h, src, 0, 0, srcLen-1, strength)

			// filtBuf[0]=topleft_in[-h], filtBuf[h-1]=topleft_in[-1]
			// leftBuf[k] = topleft[-k] = filtBuf[h-k] for k=1..h
			leftBuf = make([]byte, h+1)
			leftBuf[0] = left[0] // topLeft
			for k := 1; k <= h; k++ {
				leftBuf[k] = filtBuf[h-k]
			}
		} else {
			leftBuf = make([]byte, h+1)
			for k := 0; k <= h; k++ {
				if k < len(left) {
					leftBuf[k] = left[k]
				} else {
					leftBuf[k] = left[len(left)-1]
				}
			}
		}
	}

	// Set topLeft after both references built (dav1d: *topleft = *topleft_in).
	if !upsampleAbove {
		topRef[0] = above[0]
	}

	// Z2 topLeft filter: dav1d applies a 3-tap smoothing filter to the topLeft
	// pixel for Z2 prediction when the block is large enough and edge filtering
	// is enabled. This uses the raw (unfiltered) above[1] and left[1] pixels.
	// dav1d: ipred_prepare_tmpl.c line 198-200.
	// Condition: tw + th >= 6 where tw/th are in 4-pixel units.
	tw4 := w >> 2
	th4 := h >> 2
	if enableEdgeFilter && tw4+th4 >= 6 {
		// topleft_out[-1] = left[1] (first left pixel, raw)
		// topleft_out[1] = above[1] (first above pixel, raw)
		// topleft_out[0] = topLeft
		l1 := int(left[1])
		a1 := int(above[1])
		tl := int(above[0])
		filtered := ((l1+a1)*5 + tl*6 + 8) >> 4
		if filtered < 0 {
			filtered = 0
		}
		if filtered > maxVal {
			filtered = maxVal
		}
		topRef[0] = byte(filtered)
		leftBuf[0] = byte(filtered)
	}

	baseIncX := 1
	if upsampleAbove {
		baseIncX = 2
	}

	yShift := 6
	if upsampleLeft {
		yShift = 7
	}

	for y := 0; y < h; y++ {
		off := y * stride
		xpos := (baseIncX << 6) - (y+1)*dx
		baseX := xpos >> 6
		fracX := xpos & 0x3E

		for x := 0; x < w; x++ {
			var v int
			if baseX >= 0 {
				idx0 := baseX
				idx1 := baseX + 1
				if idx0 >= len(topRef) {
					idx0 = len(topRef) - 1
				}
				if idx1 >= len(topRef) {
					idx1 = len(topRef) - 1
				}
				v = int(topRef[idx0])*(64-fracX) + int(topRef[idx1])*fracX
			} else {
				ypos := (y << yShift) - (x+1)*dy
				baseY := ypos >> 6
				fracY := ypos & 0x3E
				// dav1d: left[-base_y] = leftBuf[(1+ups)+baseY]
				//        left[-(base_y+1)] = leftBuf[(2+ups)+baseY]
				idx0 := (1 + leftBufUps) + baseY
				idx1 := (2 + leftBufUps) + baseY
				if idx0 < 0 {
					idx0 = 0
				}
				if idx1 < 0 {
					idx1 = 0
				}
				if idx0 >= len(leftBuf) {
					idx0 = len(leftBuf) - 1
				}
				if idx1 >= len(leftBuf) {
					idx1 = len(leftBuf) - 1
				}
				v = int(leftBuf[idx0])*(64-fracY) + int(leftBuf[idx1])*fracY
			}
			dst[off+x] = byte(clip((v+32)>>6, 0, maxVal))
			baseX += baseIncX
		}
	}
}

// predictSmooth8 implements SMOOTH_PRED for 8-bit.
// Uses both horizontal and vertical smooth weight arrays.
// AV1 spec Section 7.11.2.8.
func predictSmooth8(dst []byte, stride, w, h int, above, left []byte, maxVal int) {
	wWeights := smoothWeights[w]
	hWeights := smoothWeights[h]

	if wWeights == nil || hWeights == nil {
		// Fallback: fill with DC if weights not available.
		return
	}

	// bottomLeft = left[h], the pixel at the bottom of the left column.
	// topRight = above[w], the pixel at the right end of the above row.
	bottomLeft := int(left[h])
	topRight := int(above[w])

	for y := 0; y < h; y++ {
		off := y * stride
		wH := int(hWeights[y])
		leftVal := int(left[1+y])

		for x := 0; x < w; x++ {
			wW := int(wWeights[x])
			aboveVal := int(above[1+x])

			// AV1 spec Section 7.11.2.8:
			// pred = (above[x] * smoothWeightH[y] +
			//         bottomLeft * (256 - smoothWeightH[y]) +
			//         left[y] * smoothWeightW[x] +
			//         topRight * (256 - smoothWeightW[x]) + 256) >> 9
			pred := (aboveVal*wH + bottomLeft*(256-wH) +
				leftVal*wW + topRight*(256-wW) + 256) >> 9
			dst[off+x] = byte(clip(pred, 0, maxVal))
		}
	}
}

// predictSmoothV8 implements SMOOTH_V_PRED for 8-bit.
// Uses only vertical smooth weights.
// AV1 spec Section 7.11.2.9.
func predictSmoothV8(dst []byte, stride, w, h int, above, left []byte, maxVal int) {
	hWeights := smoothWeights[h]
	if hWeights == nil {
		return
	}

	// bottomLeft = left[h], the pixel at the bottom of the left column.
	bottomLeft := int(left[h])

	for y := 0; y < h; y++ {
		off := y * stride
		wH := int(hWeights[y])

		for x := 0; x < w; x++ {
			aboveVal := int(above[1+x])
			pred := (aboveVal*wH + bottomLeft*(256-wH) + 128) >> 8
			dst[off+x] = byte(clip(pred, 0, maxVal))
		}
	}
}

// predictSmoothH8 implements SMOOTH_H_PRED for 8-bit.
// Uses only horizontal smooth weights.
// AV1 spec Section 7.11.2.10.
func predictSmoothH8(dst []byte, stride, w, h int, above, left []byte, maxVal int) {
	wWeights := smoothWeights[w]
	if wWeights == nil {
		return
	}

	// topRight = above[w], the pixel at the right end of the above row.
	topRight := int(above[w])

	for y := 0; y < h; y++ {
		off := y * stride
		leftVal := int(left[1+y])

		for x := 0; x < w; x++ {
			wW := int(wWeights[x])
			pred := (leftVal*wW + topRight*(256-wW) + 128) >> 8
			dst[off+x] = byte(clip(pred, 0, maxVal))
		}
	}
}

// predictPaeth8 implements PAETH_PRED for 8-bit.
// For each pixel, picks the neighbor (above, left, or top-left) whose value
// is closest to the linear combination: above[x] + left[y] - topLeft.
// AV1 spec Section 7.11.2.7.
func predictPaeth8(dst []byte, stride, w, h int, above, left []byte, maxVal int) {
	// topLeft is at above[0] == left[0].
	topLeft := int(above[0])

	for y := 0; y < h; y++ {
		off := y * stride
		leftVal := int(left[1+y])

		for x := 0; x < w; x++ {
			aboveVal := int(above[1+x])
			base := aboveVal + leftVal - topLeft

			dAbove := abs(base - aboveVal)
			dLeft := abs(base - leftVal)
			dTopLeft := abs(base - topLeft)

			var pred int
			if dAbove <= dLeft && dAbove <= dTopLeft {
				pred = aboveVal
			} else if dLeft <= dTopLeft {
				pred = leftVal
			} else {
				pred = topLeft
			}
			dst[off+x] = byte(clip(pred, 0, maxVal))
		}
	}
}

// ---------------------------------------------------------------------------
// 16-bit (high bit depth) variants
// ---------------------------------------------------------------------------

// PredictIntra16 generates an intra-predicted block for high bit depth content
// (10-bit or 12-bit), using uint16 buffers.
//
// The calling conventions for above/left are the same as PredictIntra:
//
//	above: [topLeft, above0, above1, ..., aboveN]
//	left:  [topLeft, left0,  left1,  ..., leftN]
//
// AV1 spec Section 7.11.2.
func PredictIntra16(mode int, dst []uint16, stride int, w, h int, above []uint16, left []uint16, bitDepth int) {
	maxVal := (1 << bitDepth) - 1

	switch mode {
	case DC_PRED:
		predictDC16(dst, stride, w, h, above, left, bitDepth)
	case V_PRED:
		predictV16(dst, stride, w, h, above)
	case H_PRED:
		predictH16(dst, stride, w, h, left)
	case D45_PRED:
		predictDirectional16(dst, stride, w, h, above, left, 45, maxVal)
	case D135_PRED:
		predictDirectional16(dst, stride, w, h, above, left, 135, maxVal)
	case D113_PRED:
		predictDirectional16(dst, stride, w, h, above, left, 113, maxVal)
	case D157_PRED:
		predictDirectional16(dst, stride, w, h, above, left, 157, maxVal)
	case D203_PRED:
		predictDirectional16(dst, stride, w, h, above, left, 203, maxVal)
	case D67_PRED:
		predictDirectional16(dst, stride, w, h, above, left, 67, maxVal)
	case SMOOTH_PRED:
		predictSmooth16(dst, stride, w, h, above, left, maxVal)
	case SMOOTH_V:
		predictSmoothV16(dst, stride, w, h, above, left, maxVal)
	case SMOOTH_H:
		predictSmoothH16(dst, stride, w, h, above, left, maxVal)
	case PAETH_PRED:
		predictPaeth16(dst, stride, w, h, above, left, maxVal)
	}
}

// PredictIntra16WithDelta generates an intra-predicted block with an angle
// delta for high bit depth content.
func PredictIntra16WithDelta(mode int, angleDelta int, dst []uint16, stride int, w, h int, above []uint16, left []uint16, bitDepth int) {
	if mode < D45_PRED || mode > D67_PRED {
		PredictIntra16(mode, dst, stride, w, h, above, left, bitDepth)
		return
	}
	maxVal := (1 << bitDepth) - 1
	angle := modeToAngle[mode] + angleDelta*3
	predictDirectional16(dst, stride, w, h, above, left, angle, maxVal)
}

// predictDC16 implements DC_PRED for 16-bit. AV1 spec Section 7.11.2.1.
func predictDC16(dst []uint16, stride, w, h int, above, left []uint16, bitDepth int) {
	haveAbove := len(above) > 1
	haveLeft := len(left) > 1

	var avg int

	switch {
	case haveAbove && haveLeft:
		sum := 0
		for i := 1; i <= w && i < len(above); i++ {
			sum += int(above[i])
		}
		for j := 1; j <= h && j < len(left); j++ {
			sum += int(left[j])
		}
		// Match dav1d dc_gen: dc >>= ctz(w+h), then multiplier for non-square.
		count := w + h
		dc := (count >> 1) + sum
		dc >>= uint(ctz(count))
		if w != h {
			// 16-bit: MULTIPLIER_1x2=0xAAAB, MULTIPLIER_1x4=0x6667, BASE_SHIFT=17
			mult := 0xAAAB
			if w > h*2 || h > w*2 {
				mult = 0x6667
			}
			dc = (dc * mult) >> 17
		}
		avg = dc

	case haveAbove:
		sum := 0
		for i := 1; i <= w && i < len(above); i++ {
			sum += int(above[i])
		}
		avg = (sum + w/2) / w

	case haveLeft:
		sum := 0
		for j := 1; j <= h && j < len(left); j++ {
			sum += int(left[j])
		}
		avg = (sum + h/2) / h

	default:
		avg = 1 << (bitDepth - 1)
	}

	maxVal := (1 << bitDepth) - 1
	avg = clip(avg, 0, maxVal)
	val := uint16(avg)

	for y := 0; y < h; y++ {
		off := y * stride
		for x := 0; x < w; x++ {
			dst[off+x] = val
		}
	}
}

// predictV16 implements V_PRED for 16-bit.
func predictV16(dst []uint16, stride, w, h int, above []uint16) {
	for y := 0; y < h; y++ {
		off := y * stride
		for x := 0; x < w; x++ {
			dst[off+x] = above[1+x]
		}
	}
}

// predictH16 implements H_PRED for 16-bit.
func predictH16(dst []uint16, stride, w, h int, left []uint16) {
	for y := 0; y < h; y++ {
		off := y * stride
		val := left[1+y]
		for x := 0; x < w; x++ {
			dst[off+x] = val
		}
	}
}

// predictDirectional16 implements directional prediction for 16-bit.
func predictDirectional16(dst []uint16, stride, w, h int, above, left []uint16, angle, maxVal int) {
	dx := getDx(angle)
	dy := getDy(angle)

	switch {
	case angle > 0 && angle < 90:
		predictDirAbove16(dst, stride, w, h, above, dx, maxVal)
	case angle == 90:
		predictV16(dst, stride, w, h, above)
	case angle > 90 && angle < 180:
		predictDirAboveLeft16(dst, stride, w, h, above, left, dx, dy, maxVal)
	case angle == 180:
		predictH16(dst, stride, w, h, left)
	case angle > 180 && angle < 270:
		predictDirLeft16(dst, stride, w, h, left, dy, maxVal)
	}
}

// predictDirAbove16 handles directional prediction for angles in (0, 90), 16-bit.
func predictDirAbove16(dst []uint16, stride, w, h int, above []uint16, dx, maxVal int) {
	for y := 0; y < h; y++ {
		off := y * stride
		yOffset := (y + 1) * dx
		for x := 0; x < w; x++ {
			posFixed := (x << 8) + yOffset
			base := posFixed >> 8
			shift := ((posFixed) >> 1) & 0x7F

			idx := 1 + base
			if idx < 1 {
				idx = 1
			}

			if shift == 0 {
				abIdx := idx
				if abIdx >= len(above) {
					abIdx = len(above) - 1
				}
				dst[off+x] = above[abIdx]
			} else {
				abIdx0 := idx
				abIdx1 := idx + 1
				if abIdx0 >= len(above) {
					abIdx0 = len(above) - 1
				}
				if abIdx1 >= len(above) {
					abIdx1 = len(above) - 1
				}
				if abIdx0 < 0 {
					abIdx0 = 0
				}
				if abIdx1 < 0 {
					abIdx1 = 0
				}
				val := (int(above[abIdx0])*(128-shift) + int(above[abIdx1])*shift + 64) >> 7
				dst[off+x] = uint16(clip(val, 0, maxVal))
			}
		}
	}
}

// predictDirLeft16 handles directional prediction for angles in (180, 270), 16-bit.
func predictDirLeft16(dst []uint16, stride, w, h int, left []uint16, dy, maxVal int) {
	for y := 0; y < h; y++ {
		off := y * stride
		for x := 0; x < w; x++ {
			posFixed := (y << 8) + (x+1)*dy
			base := posFixed >> 8
			shift := ((posFixed) >> 1) & 0x7F

			idx := 1 + base
			if idx < 1 {
				idx = 1
			}

			if shift == 0 {
				lIdx := idx
				if lIdx >= len(left) {
					lIdx = len(left) - 1
				}
				dst[off+x] = left[lIdx]
			} else {
				lIdx0 := idx
				lIdx1 := idx + 1
				if lIdx0 >= len(left) {
					lIdx0 = len(left) - 1
				}
				if lIdx1 >= len(left) {
					lIdx1 = len(left) - 1
				}
				if lIdx0 < 0 {
					lIdx0 = 0
				}
				if lIdx1 < 0 {
					lIdx1 = 0
				}
				val := (int(left[lIdx0])*(128-shift) + int(left[lIdx1])*shift + 64) >> 7
				dst[off+x] = uint16(clip(val, 0, maxVal))
			}
		}
	}
}

// predictDirAboveLeft16 handles directional prediction for angles in (90, 180), 16-bit.
func predictDirAboveLeft16(dst []uint16, stride, w, h int, above, left []uint16, dx, dy, maxVal int) {
	for y := 0; y < h; y++ {
		off := y * stride
		for x := 0; x < w; x++ {
			abovePosFixed := (x << 8) + (y+1)*dx
			aboveBase := abovePosFixed >> 8

			if aboveBase >= 0 {
				shift := ((abovePosFixed) >> 1) & 0x7F
				idx := 1 + aboveBase

				if shift == 0 {
					abIdx := idx
					if abIdx >= len(above) {
						abIdx = len(above) - 1
					}
					if abIdx < 0 {
						abIdx = 0
					}
					dst[off+x] = above[abIdx]
				} else {
					abIdx0 := idx
					abIdx1 := idx + 1
					if abIdx0 >= len(above) {
						abIdx0 = len(above) - 1
					}
					if abIdx1 >= len(above) {
						abIdx1 = len(above) - 1
					}
					if abIdx0 < 0 {
						abIdx0 = 0
					}
					if abIdx1 < 0 {
						abIdx1 = 0
					}
					val := (int(above[abIdx0])*(128-shift) + int(above[abIdx1])*shift + 64) >> 7
					dst[off+x] = uint16(clip(val, 0, maxVal))
				}
			} else {
				leftPosFixed := (y << 8) + (x+1)*dy
				leftBase := leftPosFixed >> 8
				shift := ((leftPosFixed) >> 1) & 0x7F
				idx := 1 + leftBase

				if shift == 0 {
					lIdx := idx
					if lIdx >= len(left) {
						lIdx = len(left) - 1
					}
					if lIdx < 0 {
						lIdx = 0
					}
					dst[off+x] = left[lIdx]
				} else {
					lIdx0 := idx
					lIdx1 := idx + 1
					if lIdx0 >= len(left) {
						lIdx0 = len(left) - 1
					}
					if lIdx1 >= len(left) {
						lIdx1 = len(left) - 1
					}
					if lIdx0 < 0 {
						lIdx0 = 0
					}
					if lIdx1 < 0 {
						lIdx1 = 0
					}
					val := (int(left[lIdx0])*(128-shift) + int(left[lIdx1])*shift + 64) >> 7
					dst[off+x] = uint16(clip(val, 0, maxVal))
				}
			}
		}
	}
}

// predictSmooth16 implements SMOOTH_PRED for 16-bit.
func predictSmooth16(dst []uint16, stride, w, h int, above, left []uint16, maxVal int) {
	wWeights := smoothWeights[w]
	hWeights := smoothWeights[h]
	if wWeights == nil || hWeights == nil {
		return
	}

	bottomLeft := int(left[h])
	topRight := int(above[w])

	for y := 0; y < h; y++ {
		off := y * stride
		wH := int(hWeights[y])
		leftVal := int(left[1+y])

		for x := 0; x < w; x++ {
			wW := int(wWeights[x])
			aboveVal := int(above[1+x])

			pred := (aboveVal*wH + bottomLeft*(256-wH) +
				leftVal*wW + topRight*(256-wW) + 256) >> 9
			dst[off+x] = uint16(clip(pred, 0, maxVal))
		}
	}
}

// predictSmoothV16 implements SMOOTH_V_PRED for 16-bit.
func predictSmoothV16(dst []uint16, stride, w, h int, above, left []uint16, maxVal int) {
	hWeights := smoothWeights[h]
	if hWeights == nil {
		return
	}

	bottomLeft := int(left[h])

	for y := 0; y < h; y++ {
		off := y * stride
		wH := int(hWeights[y])

		for x := 0; x < w; x++ {
			aboveVal := int(above[1+x])
			pred := (aboveVal*wH + bottomLeft*(256-wH) + 128) >> 8
			dst[off+x] = uint16(clip(pred, 0, maxVal))
		}
	}
}

// predictSmoothH16 implements SMOOTH_H_PRED for 16-bit.
func predictSmoothH16(dst []uint16, stride, w, h int, above, left []uint16, maxVal int) {
	wWeights := smoothWeights[w]
	if wWeights == nil {
		return
	}

	topRight := int(above[w])

	for y := 0; y < h; y++ {
		off := y * stride
		leftVal := int(left[1+y])

		for x := 0; x < w; x++ {
			wW := int(wWeights[x])
			pred := (leftVal*wW + topRight*(256-wW) + 128) >> 8
			dst[off+x] = uint16(clip(pred, 0, maxVal))
		}
	}
}

// predictPaeth16 implements PAETH_PRED for 16-bit.
func predictPaeth16(dst []uint16, stride, w, h int, above, left []uint16, maxVal int) {
	topLeft := int(above[0])

	for y := 0; y < h; y++ {
		off := y * stride
		leftVal := int(left[1+y])

		for x := 0; x < w; x++ {
			aboveVal := int(above[1+x])
			base := aboveVal + leftVal - topLeft

			dAbove := abs(base - aboveVal)
			dLeft := abs(base - leftVal)
			dTopLeft := abs(base - topLeft)

			var pred int
			if dAbove <= dLeft && dAbove <= dTopLeft {
				pred = aboveVal
			} else if dLeft <= dTopLeft {
				pred = leftVal
			} else {
				pred = topLeft
			}
			dst[off+x] = uint16(clip(pred, 0, maxVal))
		}
	}
}

// filterIntraTaps contains the 5 filter_intra modes' 7-tap filter coefficients.
// Each mode has 8 output positions (4 columns x 2 rows within a 4x2 sub-block).
// For each position, 7 taps weight: p0(topLeft), p1-p4(above), p5-p6(left).
// AV1 spec Section 7.11.2.6, matching dav1d tables.c dav1d_filter_intra_taps.
var filterIntraTaps = [5][8][7]int8{
	{{-6, 10, 0, 0, 0, 12, 0}, {-5, 2, 10, 0, 0, 9, 0}, {-3, 1, 1, 10, 0, 7, 0}, {-3, 1, 1, 2, 10, 5, 0},
		{-4, 6, 0, 0, 0, 2, 12}, {-3, 2, 6, 0, 0, 2, 9}, {-3, 2, 2, 6, 0, 2, 7}, {-3, 1, 2, 2, 6, 3, 5}},
	{{-10, 16, 0, 0, 0, 10, 0}, {-6, 0, 16, 0, 0, 6, 0}, {-4, 0, 0, 16, 0, 4, 0}, {-2, 0, 0, 0, 16, 2, 0},
		{-10, 16, 0, 0, 0, 0, 10}, {-6, 0, 16, 0, 0, 0, 6}, {-4, 0, 0, 16, 0, 0, 4}, {-2, 0, 0, 0, 16, 0, 2}},
	{{-8, 8, 0, 0, 0, 16, 0}, {-8, 0, 8, 0, 0, 16, 0}, {-8, 0, 0, 8, 0, 16, 0}, {-8, 0, 0, 0, 8, 16, 0},
		{-4, 4, 0, 0, 0, 0, 16}, {-4, 0, 4, 0, 0, 0, 16}, {-4, 0, 0, 4, 0, 0, 16}, {-4, 0, 0, 0, 4, 0, 16}},
	{{-2, 8, 0, 0, 0, 10, 0}, {-1, 3, 8, 0, 0, 6, 0}, {-1, 2, 3, 8, 0, 4, 0}, {0, 1, 2, 3, 8, 2, 0},
		{-1, 4, 0, 0, 0, 3, 10}, {-1, 3, 4, 0, 0, 4, 6}, {-1, 2, 3, 4, 0, 4, 4}, {-1, 2, 2, 3, 4, 3, 3}},
	{{-12, 14, 0, 0, 0, 14, 0}, {-10, 0, 14, 0, 0, 12, 0}, {-9, 0, 0, 14, 0, 11, 0}, {-8, 0, 0, 0, 14, 10, 0},
		{-10, 12, 0, 0, 0, 0, 14}, {-9, 1, 12, 0, 0, 0, 12}, {-8, 0, 0, 12, 0, 1, 11}, {-7, 0, 0, 1, 12, 1, 9}},
}

// PredictFilterIntra implements filter_intra prediction for 8-bit.
// Processes the block in 4x2 sub-blocks. Matches dav1d ipred_filter_c.
// above[0]=topLeft, above[1..]=above pixels; left[0]=topLeft, left[1..]=left pixels.
func PredictFilterIntra(dst []byte, stride, w, h int, above, left []byte, filtIdx int) {
	taps := &filterIntraTaps[filtIdx]
	for y := 0; y < h; y += 2 {
		for x := 0; x < w; x += 4 {
			var p0, p1, p2, p3, p4, p5, p6 int
			if y == 0 {
				if x == 0 {
					p0 = int(above[0])
				} else {
					p0 = int(above[x])
				}
				p1 = int(above[x+1])
				p2 = int(above[x+2])
				if x+3 < len(above) {
					p3 = int(above[x+3])
				} else {
					p3 = p2
				}
				if x+4 < len(above) {
					p4 = int(above[x+4])
				} else {
					p4 = p3
				}
			} else {
				prevRow := (y - 1) * stride
				if x == 0 {
					if y < len(left) {
						p0 = int(left[y])
					} else {
						p0 = int(left[len(left)-1])
					}
				} else {
					p0 = int(dst[prevRow+x-1])
				}
				p1 = int(dst[prevRow+x])
				p2 = int(dst[prevRow+x+1])
				p3 = int(dst[prevRow+x+2])
				p4 = int(dst[prevRow+x+3])
			}
			if x == 0 {
				if y+1 < len(left) {
					p5 = int(left[y+1])
				} else {
					p5 = int(left[len(left)-1])
				}
				if y+2 < len(left) {
					p6 = int(left[y+2])
				} else {
					p6 = int(left[len(left)-1])
				}
			} else {
				p5 = int(dst[y*stride+x-1])
				p6 = int(dst[(y+1)*stride+x-1])
			}
			for yy := 0; yy < 2; yy++ {
				for xx := 0; xx < 4; xx++ {
					t := taps[yy*4+xx]
					acc := int(t[0])*p0 + int(t[1])*p1 + int(t[2])*p2 + int(t[3])*p3 +
						int(t[4])*p4 + int(t[5])*p5 + int(t[6])*p6
					val := (acc + 8) >> 4
					if val < 0 {
						val = 0
					}
					if val > 255 {
						val = 255
					}
					dst[(y+yy)*stride+x+xx] = byte(val)
				}
			}
		}
	}
}
