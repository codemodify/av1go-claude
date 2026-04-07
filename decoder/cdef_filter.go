// Package decoder implements AV1 bitstream decoding.
//
// This file implements the AV1 Constrained Directional Enhancement Filter
// (CDEF), the second in-loop filter. CDEF reduces ringing artifacts by
// applying a directional filter that follows local edge structure.
//
// The algorithm has three stages per 8x8 luma block:
//   1. Direction finding: select one of 8 directions with maximum variance.
//   2. Primary filtering: tap pixels along the detected direction.
//   3. Secondary filtering: tap pixels at +/-45 degrees from primary direction.
//
// The filter is applied to 8x8 luma blocks and corresponding chroma blocks
// (either 8x8 or 4x4 depending on subsampling). Filter strengths and damping
// are signaled per 64x64 block via a CDEF index.
//
// AV1 spec Section 7.15; dav1d src/cdef_tmpl.c, src/cdef_apply_tmpl.c.
package decoder

import (
	"av1go/obu"
)

// ApplyCDEF applies the Constrained Directional Enhancement Filter to the
// reconstructed frame in-place.
//
// cdefIndices maps 64x64-block keys to CDEF parameter set indices [0..7].
// The key for a 64x64 block at MI position (miRow, miCol) is:
//
//	(miRow/16)*256 + (miCol/16)
//
// This matches the key scheme used in TileDecoder.cdefRead.
//
// AV1 spec Section 7.15.
func ApplyCDEF(frame *FrameBuffer, fh *DecodedFrameHeader, sh *obu.SequenceHeader,
	cdefIndices map[uint32]int, deblockInfo [][]DeblockInfo) {

	if fh.CodedLossless || !sh.EnableCDEF || fh.AllowIntraBC {
		return
	}
	if fh.CDEFBits == 0 && fh.CDEFYPriStrength[0] == 0 && fh.CDEFYSecStrength[0] == 0 &&
		fh.CDEFUVPriStrength[0] == 0 && fh.CDEFUVSecStrength[0] == 0 {
		return
	}

	subX := int(sh.ColorConfig.SubsamplingX)
	subY := int(sh.ColorConfig.SubsamplingY)
	damping := fh.CDEFDamping

	// Work on a copy of the frame to read original (pre-filter) samples while
	// writing filtered output. CDEF spec requires reading from the pre-filtered
	// frame (cdefFrame) for each block.
	origY := make([]byte, len(frame.Y))
	copy(origY, frame.Y)
	origU := make([]byte, len(frame.U))
	copy(origU, frame.U)
	origV := make([]byte, len(frame.V))
	copy(origV, frame.V)

	cdefStep := 16 // 64x64 in MI units

	// Iterate over 64x64 superblocks.
	miRows := int(fh.MiRows)
	miCols := int(fh.MiCols)

	// Clip to actual frame height in MI units (matches dav1d's f->bh).
	effectiveMiRows := (frame.Height + 3) / 4
	if miRows > effectiveMiRows {
		miRows = effectiveMiRows
	}

	for sbRow := 0; sbRow*cdefStep < miRows; sbRow++ {
		for sbCol := 0; sbCol*cdefStep < miCols; sbCol++ {
			key := uint32(sbRow)*256 + uint32(sbCol)
			cdefIdx := cdefIndices[key] // default 0 if missing (matches dav1d zero-init)

			priStrY := fh.CDEFYPriStrength[cdefIdx]
			secStrY := fh.CDEFYSecStrength[cdefIdx]
			priStrUV := fh.CDEFUVPriStrength[cdefIdx]
			secStrUV := fh.CDEFUVSecStrength[cdefIdx]

			yStrength := (priStrY << 2) | secStrY
			uvStrength := (priStrUV << 2) | secStrUV
			if yStrength == 0 && uvStrength == 0 {
				continue
			}

			// Process each 8x8 luma block within this 64x64 region.
			miRowStart := sbRow * cdefStep
			miColStart := sbCol * cdefStep
			miRowEnd := filterMin(miRowStart+cdefStep, miRows)
			miColEnd := filterMin(miColStart+cdefStep, miCols)

			for miR := miRowStart; miR < miRowEnd; miR += 2 {
				for miC := miColStart; miC < miColEnd; miC += 2 {
					py := miR * 4
					px := miC * 4
					if py >= frame.Height || px >= frame.Width {
						continue
					}
					bw := 8
					bh := 8

					// Per-8x8 skip check: if all 4x4 blocks in this 8x8
					// region have skip=true, skip CDEF.
					if deblockInfo != nil {
						allSkip := true
						for dr := 0; dr < 2 && allSkip; dr++ {
							for dc := 0; dc < 2 && allSkip; dc++ {
								mr := miR + dr
								mc := miC + dc
								if mr < len(deblockInfo) && mc < len(deblockInfo[mr]) {
									if !deblockInfo[mr][mc].Skip {
										allSkip = false
									}
								}
							}
						}
						if allSkip {
							continue
						}
					}

					// Find direction for this 8x8 luma block.
					dir, variance := cdefFindDirection(origY, frame.StrideY, px, py)

					// Adjust luma primary strength based on direction variance.
					adjPriY := cdefAdjustStrength(priStrY, variance)

					// Apply luma CDEF.
					lumaDir := dir
					if priStrY == 0 {
						lumaDir = 0
					}
					if adjPriY > 0 || secStrY > 0 {
						cdefFilterBlock(
							frame.Y, origY, frame.StrideY,
							px, py, bw, bh,
							adjPriY, secStrY, lumaDir, damping,
							frame.Width, frame.Height,
						)
					}

					// Apply chroma CDEF using the same direction from luma.
					chromaDir := dir
					if priStrUV == 0 {
						chromaDir = 0
					}
					cpx := px >> subX
					cpy := py >> subY
					if cpy >= (frame.Height+subY)>>subY || cpx >= (frame.Width+subX)>>subX {
						continue
					}
					cbw := 8 >> subX
					cbh := 8 >> subY
					chromaW := (frame.Width + subX) >> subX
					chromaH := (frame.Height + subY) >> subY

					if priStrUV > 0 || secStrUV > 0 {
						cdefFilterBlock(
							frame.U, origU, frame.StrideU,
							cpx, cpy, cbw, cbh,
							priStrUV, secStrUV, chromaDir, damping-1,
							chromaW, chromaH,
						)
						cdefFilterBlock(
							frame.V, origV, frame.StrideV,
							cpx, cpy, cbw, cbh,
							priStrUV, secStrUV, chromaDir, damping-1,
							chromaW, chromaH,
						)
					}
				}
			}
		}
	}
}

// ApplyCDEFSBRow applies CDEF for a single SB row. Reads from the frame
// directly (not a copy), so previous SB rows' fully-filtered data is visible.
// This matches dav1d's interleaved per-SB-row CDEF processing.
//
// sby is a superblock row index. For 128x128 superblocks, each SB row
// contains two 64-pixel CDEF rows; this function iterates over all CDEF
// rows within the SB row.
func ApplyCDEFSBRow(frame *FrameBuffer, fh *DecodedFrameHeader, sh *obu.SequenceHeader,
	cdefIndices map[uint32]int, deblockInfo [][]DeblockInfo, sby int) {

	if fh.CodedLossless || !sh.EnableCDEF || fh.AllowIntraBC {
		return
	}
	if fh.CDEFBits == 0 && fh.CDEFYPriStrength[0] == 0 && fh.CDEFYSecStrength[0] == 0 &&
		fh.CDEFUVPriStrength[0] == 0 && fh.CDEFUVSecStrength[0] == 0 {
		return
	}

	subX := int(sh.ColorConfig.SubsamplingX)
	subY := int(sh.ColorConfig.SubsamplingY)
	damping := fh.CDEFDamping
	cdefStep := 16 // 64x64 in MI units
	miRows := int(fh.MiRows)
	miCols := int(fh.MiCols)

	// Clip to actual frame height in MI units (matches dav1d's f->bh).
	effectiveMiRows := (frame.Height + 3) / 4
	if miRows > effectiveMiRows {
		miRows = effectiveMiRows
	}

	// For 128x128 SBs, each SB row has 2 CDEF rows (64px each).
	cdefRowsPerSB := 1
	if sh.Use128x128Superblock {
		cdefRowsPerSB = 2
	}

	for cr := 0; cr < cdefRowsPerSB; cr++ {
		sbRow := sby*cdefRowsPerSB + cr
		if sbRow*cdefStep >= miRows {
			return
		}

		for sbCol := 0; sbCol*cdefStep < miCols; sbCol++ {
			key := uint32(sbRow)*256 + uint32(sbCol)
			cdefIdx := cdefIndices[key] // default 0 if missing (matches dav1d zero-init)

			priStrY := fh.CDEFYPriStrength[cdefIdx]
			secStrY := fh.CDEFYSecStrength[cdefIdx]
			priStrUV := fh.CDEFUVPriStrength[cdefIdx]
			secStrUV := fh.CDEFUVSecStrength[cdefIdx]

			yStrength := (priStrY << 2) | secStrY
			uvStrength := (priStrUV << 2) | secStrUV
			if yStrength == 0 && uvStrength == 0 {
				continue
			}

			miRowStart := sbRow * cdefStep
			miColStart := sbCol * cdefStep
			miRowEnd := filterMin(miRowStart+cdefStep, miRows)
			miColEnd := filterMin(miColStart+cdefStep, miCols)

			for miR := miRowStart; miR < miRowEnd; miR += 2 {
				for miC := miColStart; miC < miColEnd; miC += 2 {
					py := miR * 4
					px := miC * 4
					if py >= frame.Height || px >= frame.Width {
						continue
					}
					bw := 8
					bh := 8

					if deblockInfo != nil {
						allSkip := true
						for dr := 0; dr < 2 && allSkip; dr++ {
							for dc := 0; dc < 2 && allSkip; dc++ {
								mr := miR + dr
								mc := miC + dc
								if mr < len(deblockInfo) && mc < len(deblockInfo[mr]) {
									if !deblockInfo[mr][mc].Skip {
										allSkip = false
									}
								}
							}
						}
						if allSkip {
							continue
						}
					}

					// Read source from frame directly (interleaved: prev SB rows are fully filtered).
					dir, variance := cdefFindDirection(frame.Y, frame.StrideY, px, py)
					adjPriY := cdefAdjustStrength(priStrY, variance)

					lumaDir := dir
					if priStrY == 0 {
						lumaDir = 0
					}
					if adjPriY > 0 || secStrY > 0 {
						cdefFilterBlock(
							frame.Y, frame.Y, frame.StrideY,
							px, py, bw, bh,
							adjPriY, secStrY, lumaDir, damping,
							frame.Width, frame.Height,
						)
					}

					chromaDir := dir
					if priStrUV == 0 {
						chromaDir = 0
					}
					cpx := px >> subX
					cpy := py >> subY
					if cpy >= (frame.Height+subY)>>subY || cpx >= (frame.Width+subX)>>subX {
						continue
					}
					cbw := 8 >> subX
					cbh := 8 >> subY
					chromaW := (frame.Width + subX) >> subX
					chromaH := (frame.Height + subY) >> subY

					if priStrUV > 0 || secStrUV > 0 {
						cdefFilterBlock(
							frame.U, frame.U, frame.StrideU,
							cpx, cpy, cbw, cbh,
							priStrUV, secStrUV, chromaDir, damping-1,
							chromaW, chromaH,
						)
						cdefFilterBlock(
							frame.V, frame.V, frame.StrideV,
							cpx, cpy, cbw, cbh,
							priStrUV, secStrUV, chromaDir, damping-1,
							chromaW, chromaH,
						)
					}
				}
			}
		}
	}
}

// ApplyCDEFSBRowFromSrc applies CDEF for a single SB row, reading source
// pixels from srcFrame and writing filtered output to frame.
func ApplyCDEFSBRowFromSrc(frame, srcFrame *FrameBuffer, fh *DecodedFrameHeader, sh *obu.SequenceHeader,
	cdefIndices map[uint32]int, deblockInfo [][]DeblockInfo, sby int) {

	sbMISize := 16
	if sh.Use128x128Superblock {
		sbMISize = 32
	}
	miRows := int(fh.MiRows)
	miStart := sby * sbMISize
	miEnd := miStart + sbMISize
	if miEnd > miRows {
		miEnd = miRows
	}
	ApplyCDEFMIRange(frame, srcFrame, fh, sh, cdefIndices, deblockInfo, miStart, miEnd)
}

// ApplyCDEFMIRange applies CDEF to all 8x8 blocks whose MI rows fall within
// [miRowStart, miRowEnd). Reads source (pre-CDEF) pixels from srcFrame and
// writes filtered output to frame. Processes blocks left-to-right within each
// MI row pair across the full frame width, matching dav1d's processing order.
func ApplyCDEFMIRange(frame, srcFrame *FrameBuffer, fh *DecodedFrameHeader, sh *obu.SequenceHeader,
	cdefIndices map[uint32]int, deblockInfo [][]DeblockInfo, miRowStart, miRowEnd int) {

	if fh.CodedLossless || !sh.EnableCDEF || fh.AllowIntraBC {
		return
	}
	if fh.CDEFBits == 0 && fh.CDEFYPriStrength[0] == 0 && fh.CDEFYSecStrength[0] == 0 &&
		fh.CDEFUVPriStrength[0] == 0 && fh.CDEFUVSecStrength[0] == 0 {
		return
	}

	subX := int(sh.ColorConfig.SubsamplingX)
	subY := int(sh.ColorConfig.SubsamplingY)
	damping := fh.CDEFDamping
	cdefStep := 16 // 64x64 in MI units
	miRows := int(fh.MiRows)
	miCols := int(fh.MiCols)

	// Clip to actual frame height in MI units (matches dav1d's f->bh).
	effectiveMiRows := (frame.Height + 3) / 4
	if miRows > effectiveMiRows {
		miRows = effectiveMiRows
	}
	if miRowEnd > miRows {
		miRowEnd = miRows
	}

	chromaW := (frame.Width + subX) >> subX
	chromaH := (frame.Height + subY) >> subY

	// Process each MI row pair in [miRowStart, miRowEnd), left to right
	// across the full frame width (matching dav1d's processing order).
	for miR := miRowStart &^ 1; miR < miRowEnd; miR += 2 {
		if miR < miRowStart || miR >= miRows {
			continue
		}
		py := miR * 4
		if py >= frame.Height {
			continue
		}

		sbRow := miR / cdefStep

		for miC := 0; miC < miCols; miC += 2 {
			px := miC * 4
			if px >= frame.Width {
				break
			}

			// Look up CDEF parameters for this 64x64 block.
			sbCol := miC / cdefStep
			key := uint32(sbRow)*256 + uint32(sbCol)
			cdefIdx := cdefIndices[key]

			priStrY := fh.CDEFYPriStrength[cdefIdx]
			secStrY := fh.CDEFYSecStrength[cdefIdx]
			priStrUV := fh.CDEFUVPriStrength[cdefIdx]
			secStrUV := fh.CDEFUVSecStrength[cdefIdx]

			yStrength := (priStrY << 2) | secStrY
			uvStrength := (priStrUV << 2) | secStrUV

			if yStrength == 0 && uvStrength == 0 {
				continue
			}

			// Per-8x8 skip check.
			allSkip := true
			if deblockInfo != nil {
				for dr := 0; dr < 2 && allSkip; dr++ {
					for dc := 0; dc < 2 && allSkip; dc++ {
						mr := miR + dr
						mc := miC + dc
						if mr < len(deblockInfo) && mc < len(deblockInfo[mr]) {
							if !deblockInfo[mr][mc].Skip {
								allSkip = false
							}
						}
					}
				}
			} else {
				allSkip = false
			}
			if allSkip {
				continue
			}

			// Direction finding from srcFrame (pre-CDEF snapshot).
			dir, variance := cdefFindDirection(srcFrame.Y, srcFrame.StrideY, px, py)
			adjPriY := cdefAdjustStrength(priStrY, variance)

			lumaDir := dir
			if priStrY == 0 {
				lumaDir = 0
			}

			if adjPriY > 0 || secStrY > 0 {
				cdefFilterBlock(
					frame.Y, srcFrame.Y, frame.StrideY,
					px, py, 8, 8,
					adjPriY, secStrY, lumaDir, damping,
					frame.Width, frame.Height,
				)
			}

			chromaDir := dir
			if priStrUV == 0 {
				chromaDir = 0
			}
			cpx := px >> subX
			cpy := py >> subY
			cbw := 8 >> subX
			cbh := 8 >> subY

			if cpy < chromaH && cpx < chromaW && (priStrUV > 0 || secStrUV > 0) {
				cdefFilterBlock(
					frame.U, srcFrame.U, frame.StrideU,
					cpx, cpy, cbw, cbh,
					priStrUV, secStrUV, chromaDir, damping-1,
					chromaW, chromaH,
				)
				cdefFilterBlock(
					frame.V, srcFrame.V, frame.StrideV,
					cpx, cpy, cbw, cbh,
					priStrUV, secStrUV, chromaDir, damping-1,
					chromaW, chromaH,
				)
			}

		}
	}
}

// cdefFindDirection computes the dominant direction for an 8x8 block of luma
// samples starting at pixel position (px, py). Returns direction index [0..7]
// and the direction variance (used to adjust primary strength).
//
// Matches dav1d cdef_find_dir_c (cdef_tmpl.c).
func cdefFindDirection(plane []byte, stride, px, py int) (int, int) {
	var partialSumHV [2][8]int
	var partialSumDiag [2][15]int
	var partialSumAlt [4][11]int

	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			// Read directly from the buffer, which extends past
			// the visible frame. Matches dav1d reading from its
			// padded frame buffer (contains reconstructed pixels).
			idx := (py+y)*stride + (px + x)
			val := int(plane[idx]) - 128

			partialSumDiag[0][y+x] += val
			partialSumAlt[0][y+(x>>1)] += val
			partialSumHV[0][y] += val
			partialSumAlt[1][3+y-(x>>1)] += val
			partialSumDiag[1][7+y-x] += val
			partialSumAlt[2][3-(y>>1)+x] += val
			partialSumHV[1][x] += val
			partialSumAlt[3][(y>>1)+x] += val
		}
	}

	var cost [8]uint64

	for n := 0; n < 8; n++ {
		cost[2] += uint64(partialSumHV[0][n] * partialSumHV[0][n])
		cost[6] += uint64(partialSumHV[1][n] * partialSumHV[1][n])
	}
	cost[2] *= 105
	cost[6] *= 105

	for n := 0; n < 7; n++ {
		d := uint64(cdefDivTable[n])
		cost[0] += uint64(partialSumDiag[0][n]*partialSumDiag[0][n]+
			partialSumDiag[0][14-n]*partialSumDiag[0][14-n]) * d
		cost[4] += uint64(partialSumDiag[1][n]*partialSumDiag[1][n]+
			partialSumDiag[1][14-n]*partialSumDiag[1][14-n]) * d
	}
	cost[0] += uint64(partialSumDiag[0][7]*partialSumDiag[0][7]) * 105
	cost[4] += uint64(partialSumDiag[1][7]*partialSumDiag[1][7]) * 105

	for n := 0; n < 4; n++ {
		costPtr := &cost[n*2+1]
		for m := 0; m < 5; m++ {
			*costPtr += uint64(partialSumAlt[n][3+m] * partialSumAlt[n][3+m])
		}
		*costPtr *= 105
		for m := 0; m < 3; m++ {
			d := uint64(cdefDivTable[2*m+1])
			*costPtr += uint64(partialSumAlt[n][m]*partialSumAlt[n][m]+
				partialSumAlt[n][10-m]*partialSumAlt[n][10-m]) * d
		}
	}

	bestDir := 0
	bestCost := cost[0]
	for n := 1; n < 8; n++ {
		if cost[n] > bestCost {
			bestCost = cost[n]
			bestDir = n
		}
	}
	variance := int(bestCost-cost[bestDir^4]) >> 10
	return bestDir, variance
}

// cdefAdjustStrength adjusts primary strength based on direction variance.
func cdefAdjustStrength(strength, variance int) int {
	if variance == 0 {
		return 0
	}
	i := 0
	if variance>>6 != 0 {
		i = filterIlog2(variance >> 6)
		if i > 12 {
			i = 12
		}
	}
	return (strength*(4+i) + 8) >> 4
}

// cdefConstrain applies the CDEF constrain function.
func cdefConstrain(diff, strength, shift int) int {
	if strength == 0 {
		return 0
	}
	adiff := filterAbs(diff)
	dampedStr := filterMin(adiff, filterMax(0, strength-(adiff>>shift)))
	return applySign(dampedStr, diff)
}

// cdefFilterBlock applies the CDEF filter to a single block of pixels.
// dst is the output buffer, src is the pre-filter (original) pixel data.
// Both share the same stride and coordinate space.
func cdefFilterBlock(dst, src []byte, stride, px, py, bw, bh,
	priStrength, secStrength, dir, damping, planeW, planeH int) {

	const tmpStride = 12
	const cdefLarge = 16384 // matches dav1d CDEF_VERY_LARGE

	tmpBuf := make([]int16, (bh+4)*tmpStride)
	for i := range tmpBuf {
		tmpBuf[i] = int16(cdefLarge)
	}

	tmpOff := 2*tmpStride + 2

	// Fill tmpBuf matching dav1d's padding() function.
	// Determine CDEF_HAVE_TOP/BOTTOM: these control whether the 2 padding
	// rows above/below the block are filled from the source buffer or left
	// as sentinel. Matches dav1d: CDEF_HAVE_TOP = (by_start > 0),
	// CDEF_HAVE_BOTTOM = (by + 2 < f->bh), equivalently (py + bh < planeH).
	haveTop := py > 0
	haveBottom := py+bh < planeH
	yStart := -2
	yEnd := bh + 2
	if !haveTop {
		yStart = 0
	}
	if !haveBottom {
		yEnd = bh
	}

	bufH := len(src) / stride
	for ty := yStart; ty < yEnd; ty++ {
		for tx := -2; tx < bw+2; tx++ {
			sy := py + ty
			sx := px + tx

			if sx < 0 || sx >= planeW {
				continue // keep cdefLarge
			}

			if sy < 0 || sy >= bufH {
				continue // keep cdefLarge
			}

			idx := sy*stride + sx
			if idx >= 0 && idx < len(src) {
				tmpBuf[tmpOff+ty*tmpStride+tx] = int16(src[idx])
			}
		}
	}

	priShift := 0
	if priStrength > 0 {
		priShift = filterMax(0, damping-filterIlog2(priStrength))
	}
	secShift := 0
	if secStrength > 0 {
		secShift = filterMax(0, damping-filterIlog2(secStrength))
	}

	priTap0 := 4 - (priStrength & 1)

	// dav1d separates into three cases:
	//   1. Both pri+sec: track min/max from all taps, clamp result
	//   2. Pri only:     no min/max tracking, no clamping
	//   3. Sec only:     no min/max tracking, no clamping
	haveBoth := priStrength > 0 && secStrength > 0

	for y := 0; y < bh; y++ {
		// Skip rows outside the frame (don't write output).
		if py+y >= planeH {
			continue
		}
		for x := 0; x < bw; x++ {
			// Skip columns outside the frame.
			if px+x >= planeW {
				continue
			}
			tIdx := tmpOff + y*tmpStride + x
			pxVal := int(tmpBuf[tIdx])
			if pxVal == cdefLarge {
				continue
			}

			sum := 0
			maxVal := pxVal
			minVal := pxVal

			priTapK := priTap0
			for k := 0; k < 2; k++ {
				if priStrength > 0 {
					off1 := cdefDirectionOffsets[dir+2][k]
					p0 := getFromTmpBuf(tmpBuf, tIdx+off1)
					p1 := getFromTmpBuf(tmpBuf, tIdx-off1)

					sum += priTapK * cdefConstrain(int(p0)-pxVal, priStrength, priShift)
					sum += priTapK * cdefConstrain(int(p1)-pxVal, priStrength, priShift)

					if haveBoth {
						// Skip padding sentinel values when tracking min/max.
						// dav1d uses INT16_MIN and unsigned min (umin) to achieve
						// this automatically; we must gate explicitly.
						if int(p0) != cdefLarge {
							if int(p0) < minVal {
								minVal = int(p0)
							}
							if int(p0) > maxVal {
								maxVal = int(p0)
							}
						}
						if int(p1) != cdefLarge {
							if int(p1) < minVal {
								minVal = int(p1)
							}
							if int(p1) > maxVal {
								maxVal = int(p1)
							}
						}
					}
				}
				priTapK = (priTapK & 3) | 2

				if secStrength > 0 {
					off2 := cdefDirectionOffsets[dir+4][k]
					off3 := cdefDirectionOffsets[dir+0][k]
					secTap := 2 - k

					s0 := getFromTmpBuf(tmpBuf, tIdx+off2)
					s1 := getFromTmpBuf(tmpBuf, tIdx-off2)
					s2 := getFromTmpBuf(tmpBuf, tIdx+off3)
					s3 := getFromTmpBuf(tmpBuf, tIdx-off3)

					sum += secTap * cdefConstrain(int(s0)-pxVal, secStrength, secShift)
					sum += secTap * cdefConstrain(int(s1)-pxVal, secStrength, secShift)
					sum += secTap * cdefConstrain(int(s2)-pxVal, secStrength, secShift)
					sum += secTap * cdefConstrain(int(s3)-pxVal, secStrength, secShift)

					if haveBoth {
						for _, sv := range []int16{s0, s1, s2, s3} {
							if int(sv) != cdefLarge {
								if int(sv) < minVal {
									minVal = int(sv)
								}
								if int(sv) > maxVal {
									maxVal = int(sv)
								}
							}
						}
					}
				}
			}

			adj := 0
			if sum < 0 {
				adj = -1
			}
			filtered := pxVal + ((sum + adj + 8) >> 4)

			// Only clamp when both pri and sec are active (matches dav1d).
			if haveBoth {
				filtered = filterClamp(filtered, minVal, maxVal)
			}

			dstIdx := (py+y)*stride + (px + x)
			if dstIdx >= 0 && dstIdx < len(dst) {
				dst[dstIdx] = byte(filtered)
			}
		}
	}
}

func getFromTmpBuf(buf []int16, idx int) int16 {
	if idx < 0 || idx >= len(buf) {
		return 16384 // CDEF_VERY_LARGE
	}
	return buf[idx]
}

