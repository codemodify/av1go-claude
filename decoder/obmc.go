// Package decoder implements AV1 bitstream decoding.
//
// This file implements OBMC (Overlapped Block Motion Compensation) for
// inter frames. OBMC blends the current block's prediction with predictions
// from overlapping above and left neighbors to smooth prediction boundaries.
// AV1 spec Section 7.11.3.3 (Overlap Block MC).
package decoder

// obmcMasks contains pre-computed OBMC weight masks for each block size.
// Indexed by block size in pixels: obmcMasks[size] gives the weights
// starting at that position. From dav1d tables.c dav1d_obmc_masks[64].
var obmcMasks = [64]uint8{
	// Unused (sizes 0-1)
	0, 0,
	// Size 2
	19, 0,
	// Size 4
	25, 14, 5, 0,
	// Size 8
	28, 22, 16, 11, 7, 3, 0, 0,
	// Size 16
	30, 27, 24, 21, 18, 15, 12, 10, 8, 6, 4, 3, 0, 0, 0, 0,
	// Size 32
	31, 29, 28, 26, 24, 23, 21, 20, 19, 17, 16, 14, 13, 12, 11, 9,
	8, 7, 6, 5, 4, 4, 3, 2, 0, 0, 0, 0, 0, 0, 0, 0,
}

// blockDimOverlap returns the max OBMC overlap count for a given block size.
// b_dim[2] (top overlap count) and b_dim[3] (left overlap count) from dav1d.
func blockDimOverlap(bW4, bH4 int) (topOverlap, leftOverlap int) {
	// From dav1d_block_dimensions: overlap = floor(log2(dim4))
	topOverlap = ilog2(bW4)
	leftOverlap = ilog2(bH4)
	return topOverlap, leftOverlap
}

// blendPx blends two pixel values using a mask. mask ranges from 0 to 64.
// Result = (a * (64 - mask) + b * mask + 32) >> 6
func blendPx(a, b byte, mask uint8) byte {
	return byte((int(a)*(64-int(mask)) + int(b)*int(mask) + 32) >> 6)
}

// blendH blends neighbor prediction from above into dst (horizontal OBMC).
// dav1d blend_h_c: mask indexed by block height, applies (h*3/4) rows.
// Each row uses a different mask value; all pixels in the row use the same mask.
func blendH(dst []byte, dstStride int, tmp []byte, tmpW int, w, h int) {
	mask := obmcMasks[h:]
	rows := (h * 3) >> 2
	for y := 0; y < rows; y++ {
		m := mask[y]
		for x := 0; x < w; x++ {
			dst[y*dstStride+x] = blendPx(dst[y*dstStride+x], tmp[y*tmpW+x], m)
		}
	}
}

// blendV blends neighbor prediction from left into dst (vertical OBMC).
// dav1d blend_v_c: mask indexed by block width, applies (w*3/4) columns.
// All rows use the same per-column mask pattern.
func blendV(dst []byte, dstStride int, tmp []byte, tmpW int, w, h int) {
	mask := obmcMasks[w:]
	cols := (w * 3) >> 2
	for y := 0; y < h; y++ {
		for x := 0; x < cols; x++ {
			dst[y*dstStride+x] = blendPx(dst[y*dstStride+x], tmp[y*tmpW+x], mask[x])
		}
	}
}

// applyOBMC blends overlapping neighbor predictions into the current block.
// dav1d calls obmc() separately per plane. For luma h_mul=v_mul=4, for
// chroma h_mul=4>>ss_hor, v_mul=4>>ss_ver.
func (td *TileDecoder) applyOBMC(predY []byte, nomPixW, nomPixH int,
	predU, predV []byte, nomChromaW, nomChromaH int,
	miRow, miCol, bW, bH int, filterTypeH, filterTypeV int,
	hasChroma bool, subX, subY int) {

	topOverlap, leftOverlap := blockDimOverlap(bW, bH)
	hMul := 4 >> subX // chroma horizontal multiplier
	vMul := 4 >> subY // chroma vertical multiplier

	w4 := bW
	if miCol+bW > td.tileColEnd {
		w4 = td.tileColEnd - miCol
	}
	h4 := bH
	if miRow+bH > td.tileRowEnd {
		h4 = td.tileRowEnd - miRow
	}

	// Process top neighbors (vertical overlap — blended with blend_h).
	if miRow > td.tileRowStart {
		nProcessed := 0
		for x := 0; x < w4 && nProcessed < min(topOverlap, 4); {
			idx := miCol + x + 1
			if idx >= len(td.aboveModeInfo) {
				break
			}
			info := td.aboveModeInfo[idx]
			step4 := clamp(int(info.BW4), 2, 16)

			if info.RefFrame[0] >= 0 && !info.IsIntra {
				ow4 := min(step4, bW)
				oh4 := min(bH, 16) >> 1
				// Clip blend width to available block space (see left-neighbor comment).
				blendOw4 := min(ow4, bW-x)

				refIdx := td.fh.RefFrameIdx[info.RefFrame[0]]
				refFrame := td.refFrames[refIdx]
				if refFrame != nil {
					nFilterH := int(info.Filter[1])
					nFilterV := int(info.Filter[0])

					// Luma: h_mul=4, v_mul=4
					owPxY := ow4 * 4
					ohPxY := oh4 * 4
					blendWY := blendOw4 * 4
					lapY := make([]byte, owPxY*ohPxY)
					motionCompensation(lapY, owPxY, ohPxY,
						refFrame.Y, refFrame.StrideY, refFrame.Width, refFrame.Height,
						(miCol+x)*4, miRow*4, info.MV[0], nFilterH, nFilterV)
					blendH(predY[x*4:], nomPixW, lapY, owPxY, blendWY, ohPxY)

					// Chroma: h_mul=hMul, v_mul=vMul
					if hasChroma && bW*hMul+bH*vMul >= 16 {
						owPxC := ow4 * hMul
						blendWC := blendOw4 * hMul
						ohBlendC := oh4 * vMul
						if owPxC > 0 && ohBlendC > 0 {
							chromaRefW := (refFrame.Width + subX) >> subX
							chromaRefH := (refFrame.Height + subY) >> subY
							chromaMVCol := int32(info.MV[0].Col)
							chromaMVRow := int32(info.MV[0].Row)
							chromaBaseX := (miCol + x) * hMul
							chromaBaseY := miRow * vMul

							lapU := make([]byte, owPxC*ohBlendC)
							lapV := make([]byte, owPxC*ohBlendC)
							motionCompensationChroma(lapU, owPxC, ohBlendC,
								refFrame.U, refFrame.StrideU, chromaRefW, chromaRefH,
								chromaBaseX, chromaBaseY, chromaMVCol, chromaMVRow, nFilterH, nFilterV, subX, subY)
							motionCompensationChroma(lapV, owPxC, ohBlendC,
								refFrame.V, refFrame.StrideV, chromaRefW, chromaRefH,
								chromaBaseX, chromaBaseY, chromaMVCol, chromaMVRow, nFilterH, nFilterV, subX, subY)
							chromaX := x * hMul
							blendH(predU[chromaX:], nomChromaW, lapU, owPxC, blendWC, ohBlendC)
							blendH(predV[chromaX:], nomChromaW, lapV, owPxC, blendWC, ohBlendC)
						}
					}
				}
				nProcessed++
			}
			x += step4
		}
	}

	// Process left neighbors (horizontal overlap — blended with blend_v).
	if miCol > td.tileColStart {
		nProcessed := 0
		for y := 0; y < h4 && nProcessed < min(leftOverlap, 4); {
			idx := miRow + y + 1
			if idx >= len(td.leftModeInfo) {
				break
			}
			info := td.leftModeInfo[idx]
			step4 := clamp(int(info.BH4), 2, 16)

			if info.RefFrame[0] >= 0 && !info.IsIntra {
				ow4 := min(bW, 16) >> 1
				oh4 := min(step4, bH)
				// dav1d writes directly to the frame buffer so the blend can extend
				// past the block boundary (overwritten by subsequent blocks).
				// We use an isolated prediction buffer, so clip to available space.
				// MC uses the full oh4 but blend is clipped.
				blendOh4 := min(oh4, bH-y)

				refIdx := td.fh.RefFrameIdx[info.RefFrame[0]]
				refFrame := td.refFrames[refIdx]
				if refFrame != nil {
					nFilterH := int(info.Filter[1])
					nFilterV := int(info.Filter[0])

					// Luma: h_mul=4, v_mul=4
					owPxY := ow4 * 4
					ohPxY := oh4 * 4
					blendHY := blendOh4 * 4
					lapY := make([]byte, owPxY*ohPxY)
					motionCompensation(lapY, owPxY, ohPxY,
						refFrame.Y, refFrame.StrideY, refFrame.Width, refFrame.Height,
						miCol*4, (miRow+y)*4, info.MV[0], nFilterH, nFilterV)
					blendV(predY[y*4*nomPixW:], nomPixW, lapY, owPxY, owPxY, blendHY)

					// Chroma
					if hasChroma {
						owPxC := ow4 * hMul
						ohPxC := oh4 * vMul
						blendHC := blendOh4 * vMul
						if owPxC > 0 && ohPxC > 0 {
							chromaRefW := (refFrame.Width + subX) >> subX
							chromaRefH := (refFrame.Height + subY) >> subY
							chromaMVCol := int32(info.MV[0].Col)
							chromaMVRow := int32(info.MV[0].Row)
							chromaBaseX := miCol * hMul
							chromaBaseY := (miRow + y) * vMul

							lapU := make([]byte, owPxC*ohPxC)
							lapV := make([]byte, owPxC*ohPxC)
							motionCompensationChroma(lapU, owPxC, ohPxC,
								refFrame.U, refFrame.StrideU, chromaRefW, chromaRefH,
								chromaBaseX, chromaBaseY, chromaMVCol, chromaMVRow, nFilterH, nFilterV, subX, subY)
							motionCompensationChroma(lapV, owPxC, ohPxC,
								refFrame.V, refFrame.StrideV, chromaRefW, chromaRefH,
								chromaBaseX, chromaBaseY, chromaMVCol, chromaMVRow, nFilterH, nFilterV, subX, subY)
							chromaY := y * vMul
							blendV(predU[chromaY*nomChromaW:], nomChromaW, lapU, owPxC, owPxC, blendHC)
							blendV(predV[chromaY*nomChromaW:], nomChromaW, lapV, owPxC, owPxC, blendHC)
						}
					}
				}
				nProcessed++
			}
			y += step4
		}
	}
}
