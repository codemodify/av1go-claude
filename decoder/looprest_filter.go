// Package decoder implements AV1 bitstream decoding.
//
// This file implements the AV1 loop restoration filter (in-loop filter #3),
// the last filter in the AV1 decoding pipeline (applied after deblocking and
// CDEF).
//
// Loop restoration operates on restoration units (RUs) -- rectangular regions
// whose size is signaled in the frame header. Each RU can independently choose
// between three filter modes:
//   - RESTORE_NONE:   no filtering
//   - RESTORE_WIENER: symmetric 7-tap separable FIR filter
//   - RESTORE_SGRPROJ: self-guided restoration using box filters at two scales
//
// AV1 spec Section 7.17; dav1d src/looprestoration_tmpl.c.
package decoder

import (
	"av1go/obu"
)

// LRUnitParams stores the decoded loop restoration parameters for one
// restoration unit on one plane. These parameters are read from the
// bitstream in lr.go (readRestorationInfo) and supplied here for
// application.
type LRUnitParams struct {
	Type       int    // 0=NONE, 2=WIENER, 3=SGRPROJ (matching FrameRestore* constants)
	WienerH    [3]int // horizontal filter coefficients (symmetric, 3 of 7)
	WienerV    [3]int // vertical filter coefficients (symmetric, 3 of 7)
	SGRWeights [2]int // projection weights w0, w1
	SGRIdx     int    // SGR parameter set index [0..15]
}

// ApplyLoopRestoration applies loop restoration filtering to the
// reconstructed frame in-place. lrParams is indexed as [plane][unitIdx],
// where plane is 0=Y, 1=U, 2=V, and unitIdx is computed from the
// restoration unit grid for that plane.
//
// postDeblock is the post-deblocking frame (before CDEF). Per AV1 spec
// Section 7.17.6, cross-stripe boundary reads use post-deblock pixels
// instead of post-CDEF pixels. If nil, post-CDEF pixels are used for all
// reads (less accurate at stripe boundaries).
//
// AV1 spec Section 7.17.
func ApplyLoopRestoration(frame *FrameBuffer, fh *DecodedFrameHeader, sh *obu.SequenceHeader,
	lrParams [3][]LRUnitParams, postDeblock *FrameBuffer) {

	subX := int(sh.ColorConfig.SubsamplingX)
	subY := int(sh.ColorConfig.SubsamplingY)

	for p := 0; p < 3; p++ {
		if fh.LRType[p] == FrameRestoreNone {
			continue
		}
		if len(lrParams[p]) == 0 {
			continue
		}

		// Determine plane dimensions and buffer.
		var plane []byte
		var stride, planeW, planeH int
		var lpfPlane []byte // post-deblock plane for cross-stripe reads
		planeSubY := 0
		switch p {
		case 0:
			plane = frame.Y
			stride = frame.StrideY
			planeW = frame.Width
			planeH = frame.Height
			if postDeblock != nil {
				lpfPlane = postDeblock.Y
			}
		case 1:
			plane = frame.U
			stride = frame.StrideU
			planeW = (frame.Width + subX) >> subX
			planeH = (frame.Height + subY) >> subY
			planeSubY = subY
			if postDeblock != nil {
				lpfPlane = postDeblock.U
			}
		case 2:
			plane = frame.V
			stride = frame.StrideV
			planeW = (frame.Width + subX) >> subX
			planeH = (frame.Height + subY) >> subY
			planeSubY = subY
			if postDeblock != nil {
				lpfPlane = postDeblock.V
			}
		}

		// Compute restoration unit size for this plane.
		unitSizeLog2 := 6 + fh.LRUnitShift
		if sh.Use128x128Superblock {
			unitSizeLog2 = 7 + fh.LRUnitShift
		}
		if p > 0 && fh.LRUVShift {
			unitSizeLog2--
		}
		unitSize := 1 << unitSizeLog2

		// Number of RU columns and rows.
		// Per AV1 spec: the last RU is merged with the previous if it would be
		// smaller than half a unit. This gives round-half-up instead of ceiling.
		halfUnit := unitSize >> 1
		unitCols := 1
		if planeW > unitSize {
			unitCols = (planeW + halfUnit) / unitSize
		}
		unitRows := 1
		if planeH > unitSize {
			unitRows = (planeH + halfUnit) / unitSize
		}

		// Make a copy of the plane for reading source pixels while writing
		// filtered output. The spec requires filtering against the pre-LR
		// (post-CDEF) reconstructed frame.
		origPlane := make([]byte, len(plane))
		copy(origPlane, plane)

		// If no post-deblock data, use post-CDEF for all reads.
		if lpfPlane == nil {
			lpfPlane = origPlane
		}

		// Process per SB row, matching dav1d's lr_sbrow processing order.
		// Each SB row determines its RU params via aligned_unit_pos, which
		// accounts for the 8-pixel LR stripe offset. This means the effective
		// RU vertical boundary is at (SB_boundary - 8), not at the unit grid.
		sbSize := 64
		if sh.Use128x128Superblock {
			sbSize = 128
		}
		planeSBSize := sbSize >> uint(planeSubY)
		sbRows := (planeH + planeSBSize - 1) / planeSBSize

		for sby := 0; sby < sbRows; sby++ {
			// Compute the vertical range for this SB row's LR processing.
			offsetY := 0
			if sby > 0 {
				offsetY = 8 >> uint(planeSubY)
			}
			yStripe := sby*planeSBSize - offsetY

			notLast := 0
			if sby+1 < sbRows {
				notLast = 1
			}
			rowH := (sby+1)*planeSBSize - (8>>uint(planeSubY))*notLast
			if rowH > planeH {
				rowH = planeH
			}
			if yStripe >= rowH {
				continue
			}

			// Determine the RU row via aligned_unit_pos (matches dav1d).
			rowY := yStripe + offsetY // = sby*planeSBSize (the actual SB position)
			alignedPos := rowY & ^(unitSize - 1)
			if alignedPos > 0 && alignedPos+halfUnit > planeH {
				alignedPos -= unitSize
			}
			unitRow := alignedPos / unitSize
			if unitRow >= unitRows {
				unitRow = unitRows - 1
			}

			// Process each RU column within this SB row's vertical range.
			for uCol := 0; uCol < unitCols; uCol++ {
				idx := unitRow*unitCols + uCol
				if idx >= len(lrParams[p]) {
					continue
				}
				params := lrParams[p][idx]
				if params.Type == FrameRestoreNone {
					continue
				}

				// Horizontal range for this RU column.
				ruX0 := uCol * unitSize
				ruW := unitSize
				if uCol == unitCols-1 {
					ruW = planeW - ruX0
				}
				if ruW <= 0 {
					continue
				}

				// Split the vertical range into per-stripe segments.
				segY := yStripe
				for segY < rowH {
					stripeStart, stripeEnd := lrStripeRange(segY, planeH, planeSubY)
					segEnd := rowH
					if segEnd > stripeEnd {
						segEnd = stripeEnd
					}
					segH := segEnd - segY
					if segH <= 0 {
						break
					}

					switch params.Type {
					case FrameRestoreWiener:
						lrWienerFilter(plane, origPlane, lpfPlane, stride, ruX0, segY, ruW, segH,
							params.WienerH, params.WienerV, planeW, planeH,
							stripeStart, stripeEnd)
					case FrameRestoreSGRProj:
						lrSGRFilter(plane, origPlane, lpfPlane, stride, ruX0, segY, ruW, segH,
							params.SGRIdx, params.SGRWeights, planeW, planeH,
							stripeStart, stripeEnd)
					}

					segY = segEnd
				}
			}
		}
	}
}

// ApplyLRSBRow applies loop restoration for a single SB row across all planes.
// preLR holds the post-CDEF snapshot (for within-stripe reads).
// lpfFrame holds the post-deblock snapshot (for cross-stripe boundary reads).
func ApplyLRSBRow(frame *FrameBuffer, fh *DecodedFrameHeader, sh *obu.SequenceHeader,
	lrParams [3][]LRUnitParams, preLR, lpfFrame *FrameBuffer, sby int) {

	subX := int(sh.ColorConfig.SubsamplingX)
	subY := int(sh.ColorConfig.SubsamplingY)

	for p := 0; p < 3; p++ {
		if fh.LRType[p] == FrameRestoreNone {
			continue
		}
		if len(lrParams[p]) == 0 {
			continue
		}

		var plane, origPlane, lpfPlane []byte
		var stride, planeW, planeH int
		planeSubY := 0
		switch p {
		case 0:
			plane = frame.Y
			origPlane = preLR.Y
			lpfPlane = lpfFrame.Y
			stride = frame.StrideY
			planeW = frame.Width
			planeH = frame.Height
		case 1:
			plane = frame.U
			origPlane = preLR.U
			lpfPlane = lpfFrame.U
			stride = frame.StrideU
			planeW = (frame.Width + subX) >> subX
			planeH = (frame.Height + subY) >> subY
			planeSubY = subY
		case 2:
			plane = frame.V
			origPlane = preLR.V
			lpfPlane = lpfFrame.V
			stride = frame.StrideV
			planeW = (frame.Width + subX) >> subX
			planeH = (frame.Height + subY) >> subY
			planeSubY = subY
		}

		unitSizeLog2 := 6 + fh.LRUnitShift
		if sh.Use128x128Superblock {
			unitSizeLog2 = 7 + fh.LRUnitShift
		}
		if p > 0 && fh.LRUVShift {
			unitSizeLog2--
		}
		unitSize := 1 << unitSizeLog2
		halfUnit := unitSize >> 1
		unitCols := 1
		if planeW > unitSize {
			unitCols = (planeW + halfUnit) / unitSize
		}
		unitRows := 1
		if planeH > unitSize {
			unitRows = (planeH + halfUnit) / unitSize
		}

		sbSize := 64
		if sh.Use128x128Superblock {
			sbSize = 128
		}
		planeSBSize := sbSize >> uint(planeSubY)
		sbRows := (planeH + planeSBSize - 1) / planeSBSize
		if sby >= sbRows {
			continue
		}

		// Compute vertical range for this SB row's LR processing.
		offsetY := 0
		if sby > 0 {
			offsetY = 8 >> uint(planeSubY)
		}
		yStripe := sby*planeSBSize - offsetY

		notLast := 0
		if sby+1 < sbRows {
			notLast = 1
		}
		rowH := (sby+1)*planeSBSize - (8>>uint(planeSubY))*notLast
		if rowH > planeH {
			rowH = planeH
		}
		if yStripe >= rowH {
			continue
		}

		// Determine the RU row via aligned_unit_pos.
		rowY := yStripe + offsetY
		alignedPos := rowY & ^(unitSize - 1)
		if alignedPos > 0 && alignedPos+halfUnit > planeH {
			alignedPos -= unitSize
		}
		unitRow := alignedPos / unitSize
		if unitRow >= unitRows {
			unitRow = unitRows - 1
		}

		for uCol := 0; uCol < unitCols; uCol++ {
			idx := unitRow*unitCols + uCol
			if idx >= len(lrParams[p]) {
				continue
			}
			params := lrParams[p][idx]
			if params.Type == FrameRestoreNone {
				continue
			}

			ruX0 := uCol * unitSize
			ruW := unitSize
			if uCol == unitCols-1 {
				ruW = planeW - ruX0
			}
			if ruW <= 0 {
				continue
			}

			segY := yStripe
			for segY < rowH {
				stripeStart, stripeEnd := lrStripeRange(segY, planeH, planeSubY)
				segEnd := rowH
				if segEnd > stripeEnd {
					segEnd = stripeEnd
				}
				segH := segEnd - segY
				if segH <= 0 {
					break
				}

				switch params.Type {
				case FrameRestoreWiener:
					lrWienerFilter(plane, origPlane, lpfPlane, stride, ruX0, segY, ruW, segH,
						params.WienerH, params.WienerV, planeW, planeH,
						stripeStart, stripeEnd)
				case FrameRestoreSGRProj:
					lrSGRFilter(plane, origPlane, lpfPlane, stride, ruX0, segY, ruW, segH,
						params.SGRIdx, params.SGRWeights, planeW, planeH,
						stripeStart, stripeEnd)
				}

				segY = segEnd
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Wiener filter
// ---------------------------------------------------------------------------

// lrWienerFilter applies a symmetric 7-tap separable Wiener filter to a
// rectangular region of a plane.
//
// The filter is separable: first horizontal, then vertical. Coefficients are
// symmetric: coefH[3] (and coefV[3]) is derived so the 7 taps sum to 128.
//
// h[0], h[1], h[2] are the three stored coefficients; h[3] is computed as
// 128 - 2*(h[0]+h[1]+h[2]). The full 7-tap kernel is [h[0], h[1], h[2], h[3],
// h[2], h[1], h[0]].
//
// AV1 spec Section 7.17.3 (Wiener filter process).
// dav1d src/looprestoration_tmpl.c wiener_c().
func lrWienerFilter(dst, src, lpfSrc []byte, stride, x0, y0, w, h int,
	coeffH, coeffV [3]int, planeW, planeH int,
	stripeStart, stripeEnd int) {

	// Build 7-tap symmetric kernels matching dav1d's 8-bit approach.
	// Horizontal: center tap does NOT include +128 (handled via bias).
	// Vertical: center tap includes +128 (full filter, taps sum to 128).
	var fh, fv [7]int
	fh[0] = coeffH[0]
	fh[1] = coeffH[1]
	fh[2] = coeffH[2]
	fh[3] = -(coeffH[0] + coeffH[1] + coeffH[2]) * 2 // taps sum to 0
	fh[4] = coeffH[2]
	fh[5] = coeffH[1]
	fh[6] = coeffH[0]

	fv[0] = coeffV[0]
	fv[1] = coeffV[1]
	fv[2] = coeffV[2]
	fv[3] = 128 - (coeffV[0]+coeffV[1]+coeffV[2])*2 // taps sum to 128
	fv[4] = coeffV[2]
	fv[5] = coeffV[1]
	fv[6] = coeffV[0]

	// AV1 spec Section 7.17.3 / dav1d wiener_c (8-bit).
	//
	// Horizontal pass: add bias (1<<14) + src[x]*128 so intermediates stay
	// non-negative, then shift by 3.  Result is uint16 in [0, 8191].
	//
	// Vertical pass: subtract round_offset = (1<<18) to cancel the bias
	// accumulated through the vertical filter (2048 * 128 = 262144).
	// Then shift by 11 and clip to [0, 255].
	const bitdepth = 8
	const roundBitsH = 3
	const roundBitsV = 11
	const roundOffH = 1 << (roundBitsH - 1)                // 4
	const roundOffV = 1 << (roundBitsV - 1)                 // 1024
	const clipLimitH = 1 << (bitdepth + 1 + 7 - roundBitsH) // 8192
	const hBias = 1 << (bitdepth + 6)                        // 16384
	const roundOffset = 1 << (bitdepth + roundBitsV - 1)     // 262144

	// Intermediate buffer for horizontally-filtered rows.
	totalRows := h + 6
	hor := make([][]int, totalRows)
	for i := range hor {
		hor[i] = make([]int, w)
	}

	// Horizontal pass with bias (matching dav1d 8-bit wiener_filter_h).
	for iy := -3; iy < h+3; iy++ {
		sy := lrClampRow(y0+iy, stripeStart, stripeEnd, planeH)

		rowSrc := src
		if sy < stripeStart || sy >= stripeEnd {
			rowSrc = lpfSrc
		}

		rowIdx := iy + 3
		for ix := 0; ix < w; ix++ {
			sx := x0 + ix
			centerPx := int(lrGetPixel(rowSrc, stride, sx, sy, planeW, planeH))
			sum := hBias + centerPx*128 // bias + identity
			for j := 0; j < 7; j++ {
				nx := sx + j - 3
				if nx < 0 {
					nx = 0
				} else if nx >= planeW {
					nx = planeW - 1
				}
				sum += int(lrGetPixel(rowSrc, stride, nx, sy, planeW, planeH)) * fh[j]
			}
			hor[rowIdx][ix] = filterClamp((sum+roundOffH)>>roundBitsH, 0, clipLimitH-1)
		}
	}

	// Vertical pass with round_offset subtraction (matching dav1d wiener_filter_hv).
	for iy := 0; iy < h; iy++ {
		for ix := 0; ix < w; ix++ {
			sum := -roundOffset
			for j := 0; j < 7; j++ {
				sum += hor[iy+j][ix] * fv[j]
			}
			result := filterClamp((sum+roundOffV)>>roundBitsV, 0, 255)
			dstIdx := (y0+iy)*stride + (x0 + ix)

			if dstIdx >= 0 && dstIdx < len(dst) {
				dst[dstIdx] = byte(result)
			}
		}
	}
}

// lrGetPixel reads a pixel from the source plane with clamped coordinates.
func lrGetPixel(plane []byte, stride, x, y, w, h int) byte {
	if x < 0 {
		x = 0
	} else if x >= w {
		x = w - 1
	}
	if y < 0 {
		y = 0
	} else if y >= h {
		y = h - 1
	}
	idx := y*stride + x
	if idx < 0 || idx >= len(plane) {
		return 0
	}
	return plane[idx]
}

// lrStripeRange computes the half-open range [stripeStart, stripeEnd) of the
// loop restoration stripe that contains plane row y.
// subY is the vertical subsampling shift (0 for luma, 1 for 4:2:0 chroma).
// AV1 spec Section 7.17.6.
func lrStripeRange(y, planeH, subY int) (stripeStart, stripeEnd int) {
	lumaY := y << uint(subY)
	stripeNum := (lumaY + 8) >> 6
	lumaStart := stripeNum*64 - 8
	lumaEnd := (stripeNum+1)*64 - 8

	stripeStart = lumaStart >> uint(subY)
	stripeEnd = lumaEnd >> uint(subY)

	if stripeStart < 0 {
		stripeStart = 0
	}
	if stripeEnd > planeH {
		stripeEnd = planeH
	}
	return
}

// lrClampRow clamps a vertical read coordinate to the stripe boundary ±2,
// then to frame boundaries. stripeEnd is exclusive.
// AV1 spec Section 7.17.6 (get_source_sample).
func lrClampRow(sy, stripeStart, stripeEnd, planeH int) int {
	// Stripe boundary clamping per AV1 spec:
	// The filter may read up to 2 rows beyond the stripe boundary.
	// Reads further out are clamped to ±2.
	lo := stripeStart - 2
	hi := stripeEnd + 2 - 1 // inclusive upper bound (2 rows past last stripe row)
	if sy < lo {
		sy = lo
	} else if sy > hi {
		sy = hi
	}
	// Frame boundary clamping.
	if sy < 0 {
		sy = 0
	} else if sy >= planeH {
		sy = planeH - 1
	}
	return sy
}

// lrClampRowFrame clamps a vertical read to frame boundaries only (no stripe).
func lrClampRowFrame(sy, planeH int) int {
	if sy < 0 {
		sy = 0
	} else if sy >= planeH {
		sy = planeH - 1
	}
	return sy
}

// ---------------------------------------------------------------------------
// Self-Guided Restoration (SGR) filter
// ---------------------------------------------------------------------------

// lrSGRFilter applies the self-guided restoration filter to a rectangular
// region. SGR uses box filters at one or two scales to produce guided
// images, which are then linearly combined with the source to produce
// the output.
//
// AV1 spec Section 7.17.4 (Self-guided restoration process).
// dav1d src/looprestoration_tmpl.c sgr_3x3_c(), sgr_5x5_c(), sgr_mix_c().
func lrSGRFilter(dst, src, lpfSrc []byte, stride, x0, y0, w, h int,
	sgrIdx int, weights [2]int, planeW, planeH int,
	stripeStart, stripeEnd int) {

	eps0 := sgrprojFilterParams[sgrIdx][0]
	eps1 := sgrprojFilterParams[sgrIdx][1]
	r0, r1 := sgrprojRadii(sgrIdx)
	w0 := weights[0]
	w1 := weights[1]

	// AV1 spec Section 7.17.4: When only one pass is active, the weight
	// for the active pass is transformed to (128 - w) per the xqd derivation.
	// When both are active, raw weights w0, w1 are used directly.

	// Compute guided filter output for each active pass.
	var flt0, flt1 []int // filter correction per pixel (row-major, w*h)

	if r0 > 0 && eps0 > 0 {
		flt0 = sgrBoxFilter(src, lpfSrc, stride, x0, y0, w, h, planeW, planeH, 5, eps0, stripeStart, stripeEnd)
	}
	if r1 > 0 && eps1 > 0 {
		flt1 = sgrBoxFilter(src, lpfSrc, stride, x0, y0, w, h, planeW, planeH, 3, eps1, stripeStart, stripeEnd)
	}

	// Apply weighted combination.
	// AV1 spec: w0 = sgrXqd[0], w1 = 128 - sgrXqd[0] - sgrXqd[1].
	// The implicit identity weight is sgrXqd[1]; the correction is additive.
	w0Eff := w0
	w1Eff := 128 - w0 - w1
	for iy := 0; iy < h; iy++ {
		for ix := 0; ix < w; ix++ {
			pidx := iy*w + ix
			srcVal := int(lrGetPixel(src, stride, x0+ix, y0+iy, planeW, planeH))

			v := 0
			if flt0 != nil && flt1 != nil {
				v = w0Eff*flt0[pidx] + w1Eff*flt1[pidx]
			} else if flt0 != nil {
				v = w0Eff * flt0[pidx]
			} else if flt1 != nil {
				v = w1Eff * flt1[pidx]
			} else {
				continue // no filtering
			}

			result := filterClamp(srcVal+((v+(1<<10))>>11), 0, 255)

			dstIdx := (y0+iy)*stride + (x0 + ix)
			if dstIdx >= 0 && dstIdx < len(dst) {
				dst[dstIdx] = byte(result)
			}
		}
	}
}

// sgrBoxFilter computes the SGR guided filter output for one pass.
// boxSize is 3 or 5 (corresponding to radius 1 or 2). eps is the
// strength parameter from the sgrprojFilterParams table.
//
// Returns a correction image: flt[y*w+x] = (B - A * src) >> shift.
// For box3: n=9, one_by_x=455, neighbors use EIGHT_NEIGHBORS (3x3).
// For box5: n=25, one_by_x=164, neighbors use SIX_NEIGHBORS (5x5).
//
// dav1d src/looprestoration_tmpl.c sgr_box{3,5}_row_h, sgr_calc_row_ab,
// sgr_finish_filter_row1, sgr_finish_filter2.
func sgrBoxFilter(src, lpfSrc []byte, stride, x0, y0, w, h, planeW, planeH int,
	boxSize int, eps int, stripeStart, stripeEnd int) []int {

	radius := boxSize >> 1 // 1 for box3, 2 for box5
	n := boxSize * boxSize // 9 or 25
	oneByX := 0
	if boxSize == 3 {
		oneByX = 455 // round(1/9 * (1<<12))
	} else {
		oneByX = 164 // round(1/25 * (1<<12))
	}

	// Extended dimensions for the box filter (+1 on each side for horizontal
	// summation output).
	ew := w + 2

	// We need h+2 AB rows (centered at y0-1 through y0+h) to support
	// the neighbor patterns. This requires h + 2*radius + 2 source rows.
	totalSrcRows := h + 2*radius + 2

	// Step 1: Compute horizontal box sums (sum and sumsq) for each
	// source row needed.
	type rowSums struct {
		sum   []int   // length ew, indexed [0..ew-1] corresponding to x=-1..w
		sumsq []int32 // length ew
	}
	hRows := make([]rowSums, totalSrcRows)

	for i := 0; i < totalSrcRows; i++ {
		hRows[i].sum = make([]int, ew)
		hRows[i].sumsq = make([]int32, ew)

		// Source row: shifted by -(radius+1) so AB[k] is centered at y0+k-1
		sy := lrClampRow(y0+i-radius-1, stripeStart, stripeEnd, planeH)

		// Cross-stripe reads use post-deblock (pre-CDEF) pixels.
		rowSrc := src
		if sy < stripeStart || sy >= stripeEnd {
			rowSrc = lpfSrc
		}

		sgrBoxRowH(hRows[i].sum, hRows[i].sumsq, rowSrc, stride, x0, sy, w, planeW, planeH, radius)

	}

	// Step 2: Vertical summation of horizontal row sums, then compute
	// A (filtered sumsq) and B (filtered sum) per pixel.
	// AB[k] is centered at source row y0+k-1.
	// We produce h+2 AB rows (k=0..h+1).
	type abRow struct {
		A []int32 // length ew
		B []int   // length ew
	}
	numAB := h + 2
	abRows := make([]abRow, numAB)

	for iy := 0; iy < numAB; iy++ {
		ab := abRow{
			A: make([]int32, ew),
			B: make([]int, ew),
		}
		for j := 0; j < boxSize; j++ {
			rIdx := iy + j
			if rIdx >= totalSrcRows {
				rIdx = totalSrcRows - 1
			}
			for x := 0; x < ew; x++ {
				ab.B[x] += hRows[rIdx].sum[x]
				ab.A[x] += hRows[rIdx].sumsq[x]
			}
		}

		// Compute the guided filter A and B values via sgr_calc_row_ab.
		sgrCalcRowAB(ab.A, ab.B, ew, eps, n, oneByX)
		abRows[iy] = ab
	}

	// Step 3: Weighted neighbor summation and final filter output.
	// AB[k] is centered at y0+k-1, so for output row iy (position y0+iy):
	//   box3: above=AB[iy] (y0+iy-1), center=AB[iy+1] (y0+iy), below=AB[iy+2] (y0+iy+1)
	//   box5 even: above=AB[iy] (y0+iy-1), below=AB[iy+2] (y0+iy+1)
	//   box5 odd:  center=AB[iy+1] (y0+iy)
	flt := make([]int, w*h)

	if boxSize == 3 {
		// EIGHT_NEIGHBORS: 3x3 pattern with weights [3,4,3 / 4,4,4 / 3,4,3].
		// Total weight = 32, shift = 9. Normalization: 32/512 = 1/16.
		// AB[iy] centered at y0+iy-1 (above), AB[iy+1] at y0+iy (center),
		// AB[iy+2] at y0+iy+1 (below).
		for iy := 0; iy < h; iy++ {
			ra := iy     // above: centered at y0+iy-1
			rc := iy + 1 // center: centered at y0+iy
			rb := iy + 2 // below: centered at y0+iy+1

			for ix := 0; ix < w; ix++ {
				ci := ix + 1

				// B values (x values after sgrCalcRowAB).
				a := abRows[rc].B[ci]*4 +
					(abRows[rc].B[ci-1]+abRows[rc].B[ci+1]+abRows[ra].B[ci]+abRows[rb].B[ci])*4 +
					(abRows[ra].B[ci-1]+abRows[rb].B[ci-1]+abRows[ra].B[ci+1]+abRows[rb].B[ci+1])*3

				// A values (restored means after sgrCalcRowAB).
				b := int(abRows[rc].A[ci])*4 +
					(int(abRows[rc].A[ci-1])+int(abRows[rc].A[ci+1])+int(abRows[ra].A[ci])+int(abRows[rb].A[ci]))*4 +
					(int(abRows[ra].A[ci-1])+int(abRows[rb].A[ci-1])+int(abRows[ra].A[ci+1])+int(abRows[rb].A[ci+1]))*3

				srcVal := int(lrGetPixel(src, stride, x0+ix, y0+iy, planeW, planeH))
				flt[iy*w+ix] = (b - a*srcVal + (1 << 8)) >> 9

			}
		}
	} else {
		// Box5 (r=2): AB computed at step=2 in the spec, but we have dense
		// AB rows. Even output rows use SIX_NEIGHBORS (2 AB rows, >>9),
		// odd output rows use horizontal 3-tap (1 AB row, >>8).
		// AB[iy] centered at y0+iy-1, AB[iy+1] at y0+iy, AB[iy+2] at y0+iy+1.
		// Even row iy: SIX_NEIGHBORS with AB[iy] (above) and AB[iy+2] (below)
		// Odd row iy: 3-tap horizontal with AB[iy+1] (center)
		for iy := 0; iy < h; iy++ {
			for ix := 0; ix < w; ix++ {
				ci := ix + 1
				srcVal := int(lrGetPixel(src, stride, x0+ix, y0+iy, planeW, planeH))

				if iy%2 == 0 {
					// Even row: SIX_NEIGHBORS with above (AB[iy]) and below (AB[iy+2])
					r0 := iy     // above: centered at y0+iy-1
					r1 := iy + 2 // below: centered at y0+iy+1

					a := (abRows[r0].B[ci]+abRows[r1].B[ci])*6 +
						(abRows[r0].B[ci-1]+abRows[r1].B[ci-1]+
							abRows[r0].B[ci+1]+abRows[r1].B[ci+1])*5

					b := (int(abRows[r0].A[ci])+int(abRows[r1].A[ci]))*6 +
						(int(abRows[r0].A[ci-1])+int(abRows[r1].A[ci-1])+
							int(abRows[r0].A[ci+1])+int(abRows[r1].A[ci+1]))*5

					flt[iy*w+ix] = (b - a*srcVal + (1 << 8)) >> 9
				} else {
					// Odd row: horizontal 3-tap with center (AB[iy+1])
					rc := iy + 1 // center: centered at y0+iy

					a := abRows[rc].B[ci]*6 +
						(abRows[rc].B[ci-1]+abRows[rc].B[ci+1])*5

					b := int(abRows[rc].A[ci])*6 +
						(int(abRows[rc].A[ci-1])+int(abRows[rc].A[ci+1]))*5

					flt[iy*w+ix] = (b - a*srcVal + (1 << 7)) >> 8
				}
			}
		}
	}

	return flt
}

// sgrBoxRowH computes horizontal box sums for one source row.
// Produces ew = w+2 values of sum and sumsq.
// For radius 1 (box3): sums of 3 horizontally adjacent pixels.
// For radius 2 (box5): sums of 5 horizontally adjacent pixels.
//
// dav1d src/looprestoration_tmpl.c sgr_box3_row_h, sgr_box5_row_h.
func sgrBoxRowH(sum []int, sumsq []int32, src []byte, stride, x0, sy, w, planeW, planeH, radius int) {
	getSrc := func(sx int) int {
		cx := sx
		if cx < 0 {
			cx = 0
		} else if cx >= planeW {
			cx = planeW - 1
		}
		cy := sy
		if cy < 0 {
			cy = 0
		} else if cy >= planeH {
			cy = planeH - 1
		}
		idx := cy*stride + cx
		if idx < 0 || idx >= len(src) {
			return 0
		}
		return int(src[idx])
	}

	if radius == 1 {
		// Box3: sum of 3 pixels centered on x.
		for x := -1; x <= w; x++ {
			sx := x0 + x
			a := getSrc(sx - 1)
			b := getSrc(sx)
			c := getSrc(sx + 1)
			sum[x+1] = a + b + c
			sumsq[x+1] = int32(a*a + b*b + c*c)
		}
	} else {
		// Box5: sum of 5 pixels centered on x.
		for x := -1; x <= w; x++ {
			sx := x0 + x
			a := getSrc(sx - 2)
			b := getSrc(sx - 1)
			c := getSrc(sx)
			d := getSrc(sx + 1)
			e := getSrc(sx + 2)
			sum[x+1] = a + b + c + d + e
			sumsq[x+1] = int32(a*a + b*b + c*c + d*d + e*e)
		}
	}
}

// sgrCalcRowAB transforms the vertically-summed box filter values into
// guided filter A and B values using the sgr_x_by_x lookup table.
//
// After this call:
//   A[i] = x * B_old[i] * one_by_x / 4096  (the "restored mean")
//   B[i] = x (the lookup value)
//
// where x = sgr_x_by_x[min(z, 255)] and z = (p * s + (1<<19)) >> 20
// with p = max(a*n - b*b, 0).
//
// dav1d src/looprestoration_tmpl.c sgr_calc_row_ab.
func sgrCalcRowAB(A []int32, B []int, ew, s, n, oneByX int) {
	for i := 0; i < ew; i++ {
		// For 8-bit: bitdepth_min_8 = 0, so no shifting of a and b.
		a := int(A[i])
		b := B[i]

		p := a*n - b*b
		if p < 0 {
			p = 0
		}
		z := (p*s + (1 << 19)) >> 20
		if z > 255 {
			z = 255
		}
		x := int(sgrprojXByX[z])

		// Invert: A becomes the weighted sum, B becomes x.
		A[i] = int32((x * b * oneByX + (1 << 11)) >> 12)
		B[i] = x
	}
}
