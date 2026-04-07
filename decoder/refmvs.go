// Package decoder implements AV1 bitstream decoding.
//
// This file implements the reference MV (refmvs) subsystem: temporal MV
// storage/projection and the complete MV candidate stack builder matching
// dav1d's refmvs.c dav1d_refmvs_find().
package decoder

import (
	"av1go/obu"
)

// TemporalMV stores a motion vector at 8x8 resolution for temporal prediction.
// Matches dav1d refmvs_temporal_block.
type TemporalMV struct {
	MV  MV
	Ref int8 // 0 = invalid, 1-7 = reference index (1-based, like dav1d)
}

// MiGrid is a frame-level 2D ModeInfo grid at 4x4 resolution.
// Used for multi-row MV scanning and temporal MV saving.
type MiGrid struct {
	Info   []ModeInfo
	Stride int // = MiCols
	Rows   int // = MiRows
}

// NewMiGrid allocates a frame-level ModeInfo grid.
func NewMiGrid(miRows, miCols int) *MiGrid {
	g := &MiGrid{
		Info:   make([]ModeInfo, miRows*miCols),
		Stride: miCols,
		Rows:   miRows,
	}
	// Initialize with intra defaults (matching dav1d: RefFrame={-1,-1}).
	for i := range g.Info {
		g.Info[i].IsIntra = true
		g.Info[i].RefFrame = [2]int8{-1, -1}
	}
	return g
}

// Get returns ModeInfo at (miRow, miCol), or an intra default if out of bounds.
func (g *MiGrid) Get(miRow, miCol int) ModeInfo {
	if miRow < 0 || miRow >= g.Rows || miCol < 0 || miCol >= g.Stride {
		return ModeInfo{IsIntra: true, RefFrame: [2]int8{-1, -1}}
	}
	return g.Info[miRow*g.Stride+miCol]
}

// Set stores ModeInfo for every 4x4 position in the block at (miRow, miCol).
func (g *MiGrid) Set(miRow, miCol, bW, bH int, info ModeInfo) {
	for r := miRow; r < miRow+bH && r < g.Rows; r++ {
		for c := miCol; c < miCol+bW && c < g.Stride; c++ {
			g.Info[r*g.Stride+c] = info
		}
	}
}

// divMultLUT is the lookup table for MV projection: divMultLUT[d] = floor(2^14 / d).
// Matches dav1d div_mult in refmvs.c exactly.
var divMultLUT = [32]int32{
	0, 16384, 8192, 5461, 4096, 3276, 2730, 2340,
	2048, 1820, 1638, 1489, 1365, 1260, 1170, 1092,
	1024, 963, 910, 862, 819, 780, 744, 712,
	682, 655, 630, 606, 585, 564, 546, 528,
}

// mvProjection projects a single MV component from one frame to another.
// num and den are signed POC diffs; den > 0 and den < 32 per dav1d assert.
// Matches dav1d mv_projection() exactly.
func mvProjection(mv int32, num, den int) int32 {
	// dav1d: assert(den > 0 && den < 32); assert(num > -32 && num < 32);
	// frac = num * div_mult[den]
	frac := int32(num) * int32(divMultLUT[den])
	val := int64(mv) * int64(frac)
	// dav1d: (y + 8192 + (y >> 31)) >> 14  — rounds toward zero
	val = (val + 8192 + (val >> 63)) >> 14
	// Clamp to [-0x3fff, 0x3fff] per AV1 spec 7.9.3
	if val < -0x3fff {
		val = -0x3fff
	}
	if val > 0x3fff {
		val = 0x3fff
	}
	return int32(val)
}

// mvProjectionMV projects a full MV struct using mvProjection per component.
func mvProjectionMV(mv MV, num, den int) MV {
	return MV{
		Row: mvProjection(mv.Row, num, den),
		Col: mvProjection(mv.Col, num, den),
	}
}

// SaveTMVs converts a 4x4 ModeInfo grid to 8x8 temporal MV blocks.
// refSigns[i] is true if reference i is a backward reference (from the POV of
// temporal MV selection — dav1d uses sign(getRelativeDist(refOH, curOH))).
// Returns a slice of TemporalMV at 8x8 resolution with stride tmvStride.
func SaveTMVs(grid *MiGrid, miRows, miCols int, refSigns [7]bool) ([]TemporalMV, int) {
	h8 := (miRows + 1) >> 1
	w8 := (miCols + 1) >> 1
	tmvs := make([]TemporalMV, h8*w8)

	for y := 0; y < h8; y++ {
		// Match dav1d's save_tmvs_c: step by block width (bw8).
		mi4Row := y*2 + 1
		for x := 0; x < w8; {
			mi4Col := x*2 + 1
			if mi4Col >= miCols {
				mi4Col = miCols - 1
			}
			info := grid.Get(mi4Row, mi4Col)

			// Compute bw8: (bw4 + 1) >> 1, matching dav1d's block_dimensions[bs][0].
			bw4 := int(info.BW4)
			if bw4 < 1 {
				bw4 = 1
			}
			bw8 := (bw4 + 1) >> 1
			if bw8 < 1 {
				bw8 = 1
			}
			tmv := TemporalMV{Ref: 0} // invalid by default
			if !info.IsIntra {
				// Prefer backward ref (ref[1]) if it's a backward ref and MV is small.
				if info.RefFrame[1] >= 0 && int(info.RefFrame[1]) < 7 &&
					refSigns[info.RefFrame[1]] &&
					mvMagnitude(info.MV[1]) < 4096 {
					tmv.MV = info.MV[1]
					tmv.Ref = info.RefFrame[1] + 1 // 1-based
				} else if info.RefFrame[0] >= 0 && int(info.RefFrame[0]) < 7 &&
					refSigns[info.RefFrame[0]] &&
					mvMagnitude(info.MV[0]) < 4096 {
					tmv.MV = info.MV[0]
					tmv.Ref = info.RefFrame[0] + 1 // 1-based
				}
			}

			// Fill bw8 positions with the same TMV.
			for n := 0; n < bw8 && x < w8; n++ {
				tmvs[y*w8+x] = tmv
				x++
			}
		}
	}
	return tmvs, w8
}

// mvMagnitude returns (abs(row) | abs(col)) for MV magnitude check.
// Matches dav1d's (abs(mv.y) | abs(mv.x)) check in save_tmvs_c.
func mvMagnitude(mv MV) int32 {
	r := mv.Row
	if r < 0 {
		r = -r
	}
	c := mv.Col
	if c < 0 {
		c = -c
	}
	return r | c
}

// LoadTMVs projects temporal MVs from reference frames to the current frame's
// coordinate space. This implements dav1d's dav1d_refmvs_init_frame() mfmv
// selection + load_tmvs_c() projection.
//
// Parameters:
//   - refTMVs: temporal MV grids per reference buffer slot [8]
//   - refTMVStrides: strides per reference buffer slot [8]
//   - fh: current frame header
//   - sh: sequence header
//   - refOrderHints: order hints per buffer slot [8]
//   - refRefPOC: each buffer slot's own 7 reference POCs [8][7]
//
// Returns projected temporal MVs at 8x8 resolution for the current frame.
func LoadTMVs(
	refTMVs [8][]TemporalMV,
	refTMVStrides [8]int,
	fh *DecodedFrameHeader,
	sh *obu.SequenceHeader,
	refOrderHints [8]uint32,
	refRefPOC [8][7]uint32,
) ([]TemporalMV, int) {
	miRows := int(fh.MiRows)
	miCols := int(fh.MiCols)
	h8 := (miRows + 1) >> 1
	w8 := (miCols + 1) >> 1
	proj := make([]TemporalMV, h8*w8)

	if !sh.EnableOrderHint {
		return proj, w8
	}

	orderHintBits := int(sh.OrderHintBitsMinus1) + 1
	curOH := int(fh.OrderHint)

	// Compute ref_poc for each of the 7 reference types of the current frame.
	var refPOC [7]int
	for i := 0; i < 7; i++ {
		slot := int(fh.RefFrameIdx[i])
		refPOC[i] = int(refOrderHints[slot])
	}

	// Helper: check if ref type i has valid temporal MVs.
	refHasTMVs := func(refIdx int) bool {
		slot := int(fh.RefFrameIdx[refIdx])
		return refTMVs[slot] != nil && len(refTMVs[slot]) > 0
	}

	// --- MFMV reference selection (matches dav1d_refmvs_init_frame) ---
	// dav1d selects mfmv references in a specific order:
	//   1. LAST(0) if available and alt-of-last != golden
	//   2. BWD(4) if future
	//   3. ALTREF2(5) if future
	//   4. ALTREF(6) if future and total not met
	//   5. LAST2(1) if total not met
	type mfmvRef struct {
		refIdx  int // 0-6 reference type index
		ref2cur int // signed POC diff (always positive for valid refs)
	}

	var mfmvRefs []mfmvRef
	nMFMVs := 0
	total := 2

	// Step 1: LAST (ref idx 0), conditionally
	lastSlot := int(fh.RefFrameIdx[0])
	if refHasTMVs(0) {
		altOfLast := int(refRefPOC[lastSlot][6])
		goldenPOC := refPOC[3]
		if altOfLast != goldenPOC {
			mfmvRefs = append(mfmvRefs, mfmvRef{refIdx: 0})
			nMFMVs++
			total = 3
		}
	}

	// Step 2: BWD (ref idx 4) if it's a future reference
	if refHasTMVs(4) && getPocDiff(orderHintBits, refPOC[4], curOH) > 0 {
		mfmvRefs = append(mfmvRefs, mfmvRef{refIdx: 4})
		nMFMVs++
	}

	// Step 3: ALTREF2 (ref idx 5) if future
	if refHasTMVs(5) && getPocDiff(orderHintBits, refPOC[5], curOH) > 0 {
		mfmvRefs = append(mfmvRefs, mfmvRef{refIdx: 5})
		nMFMVs++
	}

	// Step 4: ALTREF (ref idx 6) if future and total not met
	if nMFMVs < total && refHasTMVs(6) && getPocDiff(orderHintBits, refPOC[6], curOH) > 0 {
		mfmvRefs = append(mfmvRefs, mfmvRef{refIdx: 6})
		nMFMVs++
	}

	// Step 5: LAST2 (ref idx 1) if total not met
	if nMFMVs < total && refHasTMVs(1) {
		mfmvRefs = append(mfmvRefs, mfmvRef{refIdx: 1})
		nMFMVs++
	}

	// Compute ref2cur for each selected mfmv reference.
	// dav1d: diff1 = get_poc_diff(rpoc, cur_poc);
	//        ref2cur = mfmv_ref < 4 ? -diff1 : diff1;
	// For past refs: diff1 < 0, so -diff1 > 0.
	// For future refs: diff1 > 0, stays positive.
	// ref2cur is always positive for valid entries.
	for i := range mfmvRefs {
		rpoc := refPOC[mfmvRefs[i].refIdx]
		diff1 := getPocDiff(orderHintBits, rpoc, curOH)
		absDiff1 := diff1
		if absDiff1 < 0 {
			absDiff1 = -absDiff1
		}
		if absDiff1 > 31 {
			mfmvRefs[i].ref2cur = 0 // invalid, will be skipped
		} else if mfmvRefs[i].refIdx < 4 {
			mfmvRefs[i].ref2cur = -diff1
		} else {
			mfmvRefs[i].ref2cur = diff1
		}
	}

	// apply_sign helper (matches dav1d: return s < 0 ? -v : v).
	applySign := func(v int, s int) int {
		if s < 0 {
			return -v
		}
		return v
	}

	// --- Project temporal MVs per SB row per tile column (matching dav1d load_tmvs_c) ---
	// dav1d calls load_tmvs once per SB row per tile column. Each call:
	//  1. Clears the destination range to invalid (Ref=0 in our convention)
	//  2. Projects source TMVs with run-length optimization
	// We emulate this by iterating over SB rows and tile columns, clearing
	// and projecting each chunk independently. This ensures identical results
	// because the run-length skip and the per-chunk clearing interact.
	sbSize8 := 8 // 64x64 SB = 8 8x8 rows
	if sh.Use128x128Superblock {
		sbSize8 = 16
	}

	// Precompute per-mfmv data.
	type mfmvData struct {
		refSign int
		ref2ref [7]int
		slot    int
	}

	var mfData []mfmvData
	for _, mf := range mfmvRefs {
		if mf.ref2cur < -31 || mf.ref2cur > 31 || mf.ref2cur == 0 {
			continue
		}
		slot := int(fh.RefFrameIdx[mf.refIdx])
		if refTMVStrides[slot] == 0 || len(refTMVs[slot]) == 0 {
			continue
		}
		d := mfmvData{
			refSign: mf.refIdx - 4,
			slot:    slot,
		}
		rpoc := refPOC[mf.refIdx]
		for m := 0; m < 7; m++ {
			rrpoc := int(refRefPOC[slot][m])
			diff2 := getPocDiff(orderHintBits, rpoc, rrpoc)
			if diff2 < 0 || diff2 > 31 {
				d.ref2ref[m] = 0
			} else {
				d.ref2ref[m] = diff2
			}
		}
		mfData = append(mfData, d)
	}


	// Tile column starts in 8x8 units.
	nTileCols := fh.TileCols
	tileCol8Starts := make([]int, nTileCols+1)
	for tc := 0; tc <= nTileCols; tc++ {
		tileCol8Starts[tc] = fh.TileColStarts[tc] >> 1
	}

	for rowStart8 := 0; rowStart8 < h8; rowStart8 += sbSize8 {
		rowEnd8 := rowStart8 + sbSize8
		if rowEnd8 > h8 {
			rowEnd8 = h8
		}

		for tc := 0; tc < nTileCols; tc++ {
			colStart8 := tileCol8Starts[tc]
			colEnd8 := tileCol8Starts[tc+1]

			// 1. Clear destination range to invalid (Ref=0).
			for y := rowStart8; y < rowEnd8; y++ {
				for x := colStart8; x < colEnd8; x++ {
					proj[y*w8+x] = TemporalMV{}
				}
			}

			colStart8i := colStart8 - 8
			if colStart8i < 0 {
				colStart8i = 0
			}
			colEnd8i := colEnd8 + 8
			if colEnd8i > w8 {
				colEnd8i = w8
			}

			// 2. Project from each MFMV reference.
			mdIdx := 0
			for _, mf := range mfmvRefs {
				if mf.ref2cur < -31 || mf.ref2cur > 31 || mf.ref2cur == 0 {
					continue
				}
				if mdIdx >= len(mfData) {
					break
				}
				md := mfData[mdIdx]
				mdIdx++
				srcTMVs := refTMVs[md.slot]
				srcStride := refTMVStrides[md.slot]
				srcH8 := len(srcTMVs) / srcStride

				for y := rowStart8; y < rowEnd8 && y < srcH8; y++ {
					ySBAlign := y & ^7
					yProjStart := ySBAlign
					if yProjStart < rowStart8 {
						yProjStart = rowStart8
					}
					yProjEnd := ySBAlign + 8
					if yProjEnd > rowEnd8 {
						yProjEnd = rowEnd8
					}

					rOff := y * srcStride
					for x := colStart8i; x < colEnd8i; x++ {
						if rOff+x >= len(srcTMVs) {
							break
						}
						rb := srcTMVs[rOff+x]
						if rb.Ref == 0 {
							continue
						}
						bRef := int(rb.Ref)
						if bRef < 1 || bRef > 7 {
							continue
						}
						r2r := md.ref2ref[bRef-1]
						if r2r == 0 {
							continue
						}

						bMV := rb.MV
						bMVn := (int64(bMV.Row) << 32) | int64(uint32(bMV.Col))
						offset := mvProjectionMV(bMV, mf.ref2cur, r2r)
						posX := x + applySign(absInt(int(offset.Col))>>6, int(offset.Col)^md.refSign)
						posY := y + applySign(absInt(int(offset.Row))>>6, int(offset.Row)^md.refSign)

						if posY >= yProjStart && posY < yProjEnd {
							pos := posY * w8
							// Run-length loop: advance x and posX together while
							// consecutive source blocks have the same ref and MV.
							// Matches dav1d load_tmvs_c exactly.
							for {
								xSBAlign := x & ^7
								xLo := xSBAlign - 8
								if xLo < colStart8 {
									xLo = colStart8
								}
								xHi := xSBAlign + 16
								if xHi > colEnd8 {
									xHi = colEnd8
								}
								if posX >= xLo && posX < xHi {
									idx := pos + posX
									if idx >= 0 && idx < len(proj) {
										proj[idx] = TemporalMV{
											MV:  bMV,
											Ref: int8(r2r),
										}
									}
								}
								x++
								if x >= colEnd8i {
									break
								}
								if rOff+x >= len(srcTMVs) {
									break
								}
								nrb := srcTMVs[rOff+x]
								nMVn := (int64(nrb.MV.Row) << 32) | int64(uint32(nrb.MV.Col))
								if int(nrb.Ref) != bRef || nMVn != bMVn {
									break
								}
								posX++
							}
						} else {
							// posY out of range — still do run-length skip.
							for {
								x++
								if x >= colEnd8i {
									break
								}
								if rOff+x >= len(srcTMVs) {
									break
								}
								nrb := srcTMVs[rOff+x]
								nMVn := (int64(nrb.MV.Row) << 32) | int64(uint32(nrb.MV.Col))
								if int(nrb.Ref) != bRef || nMVn != bMVn {
									break
								}
							}
						}
						x-- // compensate for outer loop's x++
					}
				}
			}
		}
	}



	return proj, w8
}

