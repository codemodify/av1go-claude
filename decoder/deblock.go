// Package decoder implements AV1 bitstream decoding.
//
// This file implements the AV1 deblocking filter (in-loop filter #1).
// The deblocking filter reduces blocking artifacts at block-edge boundaries
// produced by the block-based transform coding in AV1.
//
// AV1 spec Section 7.14; dav1d src/loopfilter_tmpl.c, src/lf_apply_tmpl.c.
package decoder

import (
	"av1go/obu"
)

// DeblockInfo holds per-4x4-block information needed for deblocking filter
// decisions. The slice is indexed as [miRow][miCol] in MI units.
type DeblockInfo struct {
	IsInter       bool  // true for inter-predicted blocks
	RefFrame      int8  // -1 = intra, 0-6 = reference frame index
	Mode          uint8 // prediction mode
	IsGlobalMV    bool  // true for GLOBALMV (single) or GLOBALMV_GLOBALMV (compound)
	Skip          bool  // skip flag (no residual)
	HasNonZero    bool  // true if any non-zero coefficients in this block
	TxW           int   // luma transform width in pixels
	TxH           int   // luma transform height in pixels
	UvTxW         int   // chroma transform width in pixels
	UvTxH         int   // chroma transform height in pixels
	DeltaLF       [4]int // per-SB cumulative delta_lf (Y-vert, Y-horiz, U, V)
	CodingBlockCol int  // MI column of the coding block this 4x4 belongs to
	CodingBlockRow int  // MI row of the coding block this 4x4 belongs to
}

// ApplyDeblocking applies the AV1 deblocking filter to the reconstructed
// frame in-place. Processing order: vertical edges first (left-to-right,
// top-to-bottom), then horizontal edges.
//
// blockInfo is indexed as [miRow][miCol], where MI = 4 pixels.
// AV1 spec Section 7.14.
func ApplyDeblocking(frame *FrameBuffer, fh *DecodedFrameHeader, sh *obu.SequenceHeader,
	blockInfo [][]DeblockInfo) {

	if len(blockInfo) == 0 {
		return
	}
	miRows := len(blockInfo)
	miCols := len(blockInfo[0])

	// Clip MI rows to actual frame height (see ApplyDeblockingSBRow comment).
	effectiveMiRows := (frame.Height + 3) / 4
	if miRows > effectiveMiRows {
		miRows = effectiveMiRows
	}

	sharp := int(fh.LoopFilterSharpness)
	lut := buildDeblockLUT(sharp)

	subX := int(sh.ColorConfig.SubsamplingX)
	subY := int(sh.ColorConfig.SubsamplingY)

	// --- Vertical edge filtering (for each horizontal edge position) ---
	deblockEdges(frame, blockInfo, miRows, miCols, &lut,
		fh, subX, subY, true, 0, miRows)


	// --- Horizontal edge filtering ---
	deblockEdges(frame, blockInfo, miRows, miCols, &lut,
		fh, subX, subY, false, 0, miRows)

}

// ApplyDeblockingSBRow applies deblocking for a single SB row.
// Matches dav1d's filter_sbrow_deblock_cols + filter_sbrow_deblock_rows.
func ApplyDeblockingSBRow(frame *FrameBuffer, fh *DecodedFrameHeader, sh *obu.SequenceHeader,
	blockInfo [][]DeblockInfo, lut *deblockLUT, sby int) {

	if len(blockInfo) == 0 {
		return
	}
	// dav1d dav1d_calc_lf_values: if both luma levels are 0, all filter
	// levels are zeroed -- skip deblocking entirely (no ref_delta applied).
	if fh.LoopFilterLevel[0] == 0 && fh.LoopFilterLevel[1] == 0 &&
		fh.LoopFilterLevel[2] == 0 && fh.LoopFilterLevel[3] == 0 {
		return
	}
	miRows := len(blockInfo)
	miCols := len(blockInfo[0])
	subX := int(sh.ColorConfig.SubsamplingX)
	subY := int(sh.ColorConfig.SubsamplingY)

	// Clip MI row range to the actual frame height, matching dav1d's f->bh.
	// The AV1 spec's MiRows rounds up to the 8-pixel SB column boundary,
	// which can be 1 larger than (frameHeight+3)/4 when the frame height is
	// not a multiple of 8. Processing the phantom extra MI row would deblock
	// buffer padding rows that CDEF later reads, causing pixel diffs.
	effectiveMiRows := (frame.Height + 3) / 4
	if miRows > effectiveMiRows {
		miRows = effectiveMiRows
	}

	sbSize := 64
	if sh.Use128x128Superblock {
		sbSize = 128
	}
	miPerSB := sbSize / 4
	miRowStart := sby * miPerSB
	miRowEnd := miRowStart + miPerSB
	if miRowEnd > miRows {
		miRowEnd = miRows
	}

	// Vertical edges (cols), then horizontal edges (rows) — matching dav1d.
	deblockEdges(frame, blockInfo, miRows, miCols, lut,
		fh, subX, subY, true, miRowStart, miRowEnd)
	deblockEdges(frame, blockInfo, miRows, miCols, lut,
		fh, subX, subY, false, miRowStart, miRowEnd)
}

// BuildDeblockLUT exposes the LUT builder for interleaved processing.
func BuildDeblockLUT(sharp int) deblockLUT {
	return buildDeblockLUT(sharp)
}

// deblockEdges filters all edges in one direction (vertical or horizontal).
// miRowStart..miRowEnd restrict the MI row range (0..miRows for full frame).
func deblockEdges(frame *FrameBuffer, blockInfo [][]DeblockInfo,
	miRows, miCols int, lut *deblockLUT,
	fh *DecodedFrameHeader, subX, subY int, vertical bool,
	miRowStart, miRowEnd int) {

	// lvlIdx for luma: 0=vertical, 1=horizontal
	yLvlIdx := 0
	if !vertical {
		yLvlIdx = 1
	}

	for miR := miRowStart; miR < miRowEnd; miR++ {
		for miC := 0; miC < miCols; miC++ {
			if vertical && miC == 0 {
				continue
			}
			if !vertical && miR == 0 {
				continue
			}

			cur := blockInfo[miR][miC]

			var prev DeblockInfo
			if vertical {
				prev = blockInfo[miR][miC-1]
			} else {
				prev = blockInfo[miR-1][miC]
			}

			// Determine filter level for luma.
			yLevel := deblockLevelForEdge(fh, cur, prev, yLvlIdx)
			// TX size perpendicular to edge.
			var curTx, prevTx int
			if vertical {
				curTx = cur.TxW
				prevTx = prev.TxW
			} else {
				curTx = cur.TxH
				prevTx = prev.TxH
			}
			if curTx <= 0 {
				curTx = 4
			}
			if prevTx <= 0 {
				prevTx = 4
			}

			// Transform edge detection.
			// dav1d lf_mask.c: block boundary edges are always present;
			// internal TX edges within a block are suppressed for skip inter blocks.
			px := miC * 4
			py := miR * 4
			var isBlockBoundary bool
			if vertical {
				isBlockBoundary = cur.CodingBlockCol == miC
			} else {
				isBlockBoundary = cur.CodingBlockRow == miR
			}
			var isEdge bool
			if isBlockBoundary {
				// Block boundary: always an edge (checked against BOTH sides' TX grids).
				isEdge = true
			} else {
				// Internal position: check TX grid alignment.
				if vertical {
					isEdge = (px%curTx == 0) || (px%prevTx == 0)
				} else {
					isEdge = (py%curTx == 0) || (py%prevTx == 0)
				}
				// Skip inter blocks suppress internal TX edges (dav1d lf_mask.c line 110).
				if isEdge && cur.Skip && cur.IsInter {
					isEdge = false
				}
			}
			if !isEdge {
				yLevel = 0
			}

			filterWidth := deblockFilterWidth(curTx, prevTx)

			// --- Luma filtering ---
			if yLevel > 0 {
				E := lut.e[yLevel]
				I := lut.i[yLevel]
				H := yLevel >> 4
				if vertical {
					loopFilterVertical(frame.Y, frame.StrideY, px, py, E, I, H, filterWidth)
				} else {
					loopFilterHorizontal(frame.Y, frame.StrideY, px, py, E, I, H, filterWidth)
				}
			}

			// --- Chroma filtering ---
			// Chroma edges are detected independently from luma, using the
			// chroma TX grid. Only process at subsampling-aligned MI positions.
			// For 4:2:0: skip odd miC AND odd miR. Each call filters 4 rows
			// (vertical) or 4 cols (horizontal), covering both the even and
			// odd MI pair.
			//
			// Edge detection (TX grid, block boundaries, skip flag) uses the
			// original cur/prev from the luma MI position — matching dav1d's
			// mask_edges_chroma which is computed per-block.
			//
			// Filter LEVEL uses adjusted MI positions to match dav1d's
			// precomputed level cache, where multiple sub-8x8 blocks mapping
			// to the same chroma cell result in last-writer-wins (the
			// bottom-right MI of the 2x2 group in raster order).
			chromaEdge := true
			if subX == 1 && (miC%2) != 0 {
				chromaEdge = false
			}
			if subY == 1 && (miR%2) != 0 {
				chromaEdge = false
			}

			if chromaEdge {
				var chromaCurTx, chromaPrevTx int
				if vertical {
					chromaCurTx = cur.UvTxW
					chromaPrevTx = prev.UvTxW
				} else {
					chromaCurTx = cur.UvTxH
					chromaPrevTx = prev.UvTxH
				}
				if chromaCurTx < 4 {
					chromaCurTx = 4
				}
				if chromaPrevTx < 4 {
					chromaPrevTx = 4
				}
				cpx := px >> subX
				cpy := py >> subY
				// Re-check edge using chroma TX grid.
				if vertical {
					chromaEdge = (cpx%chromaCurTx == 0) || (cpx%chromaPrevTx == 0)
				} else {
					chromaEdge = (cpy%chromaCurTx == 0) || (cpy%chromaPrevTx == 0)
				}
				// Suppress internal chroma TX edges for skip-inter blocks,
				// matching dav1d mask_edges_chroma which gates inner edges
				// on !skip_inter. Block boundary edges are never suppressed.
				if chromaEdge && !isBlockBoundary && cur.Skip && cur.IsInter {
					chromaEdge = false
				}
				if !chromaEdge {
					continue
				}
				chromaFilterWidth := deblockFilterWidth(chromaCurTx, chromaPrevTx)
				// Cap chroma filter width at 6 per spec.
				if chromaFilterWidth > 6 {
					chromaFilterWidth = 6
				}

				// For filter levels, use the "winning" MI position from
				// dav1d's last-writer-wins level cache. For 4:2:0 (subX=1,
				// subY=1), the winning position is the bottom-right MI of
				// the 2x2 chroma cell group.
				lvlCur := cur
				lvlPrev := prev
				if subX != 0 || subY != 0 {
					// Cur side: bottom-right of the chroma cell
					lr := miR
					lc := miC
					if subY == 1 && lr+1 < miRows {
						lr++
					}
					if subX == 1 && lc+1 < miCols {
						lc++
					}
					lvlCur = blockInfo[lr][lc]

					// Prev side: depends on edge direction
					if vertical {
						// Left cell's bottom-right: (miR+subY, miC-1)
						pr := miR
						if subY == 1 && pr+1 < miRows {
							pr++
						}
						lvlPrev = blockInfo[pr][miC-1]
					} else {
						// Above cell's bottom-right: (miR-1, miC+subX)
						pc := miC
						if subX == 1 && pc+1 < miCols {
							pc++
						}
						lvlPrev = blockInfo[miR-1][pc]
					}
				}

				uLevel := deblockLevelForEdge(fh, lvlCur, lvlPrev, 2)
				if uLevel > 0 && cpx < frame.StrideU && cpy < (len(frame.U)/frame.StrideU) {
					EU := lut.e[uLevel]
					IU := lut.i[uLevel]
					HU := uLevel >> 4
					if vertical {
						if cpx > 0 {
							loopFilterVertical(frame.U, frame.StrideU, cpx, cpy, EU, IU, HU, chromaFilterWidth)
						}
					} else {
						if cpy > 0 {
							loopFilterHorizontal(frame.U, frame.StrideU, cpx, cpy, EU, IU, HU, chromaFilterWidth)
						}
					}
				}
				vLevel := deblockLevelForEdge(fh, lvlCur, lvlPrev, 3)
				if vLevel > 0 && cpx < frame.StrideV && cpy < (len(frame.V)/frame.StrideV) {
					EV := lut.e[vLevel]
					IV := lut.i[vLevel]
					HV := vLevel >> 4
					if vertical {
						if cpx > 0 {
							loopFilterVertical(frame.V, frame.StrideV, cpx, cpy, EV, IV, HV, chromaFilterWidth)
						}
					} else {
						if cpy > 0 {
							loopFilterHorizontal(frame.V, frame.StrideV, cpx, cpy, EV, IV, HV, chromaFilterWidth)
						}
					}
				}
			}
		}
	}
}

// computeBlockLevel computes the filter level for a single block at a given
// level index, incorporating per-SB delta_lf.
// Matches dav1d's calc_lf_value / calc_lf_value_chroma in lf_mask.c:
//
//	base = clamp(clamp(base_lvl + lf_delta, 0, 63) + seg_delta, 0, 63)
//	level = clamp(base + ref_delta + mode_delta scaled by shift, 0, 63)
//
// For chroma (lvlIdx >= 2): if the base level (from frame header) is 0,
// ALL filter levels are 0 — ref/mode deltas are NOT applied.
// This matches dav1d's calc_lf_value_chroma which zeroes everything
// when base_lvl == 0.
func computeBlockLevel(fh *DecodedFrameHeader, deltaLF [4]int, ref int, isGlobalMV bool, lvlIdx int) int {
	baseLvl := int(fh.LoopFilterLevel[lvlIdx])

	// dav1d calc_lf_value_chroma: if chroma base level is 0, return 0
	// without applying any ref/mode deltas.
	if lvlIdx >= 2 && baseLvl == 0 {
		return 0
	}

	// Per-SB delta_lf: index depends on DeltaLFMulti.
	lfDeltaIdx := 0
	if fh.DeltaLFMulti {
		lfDeltaIdx = lvlIdx
	}
	lfDelta := deltaLF[lfDeltaIdx]

	// dav1d: base = iclip(iclip(base_lvl + lf_delta, 0, 63) + seg_delta, 0, 63)
	// We don't support segmentation yet, so seg_delta = 0.
	base := filterClamp(baseLvl+lfDelta, 0, 63)

	if !fh.LoopFilterDeltaEnabled {
		return base
	}

	// ref is stored as -1=intra, 0-6=reference. Map to 0-7 index.
	refIdx := ref + 1
	if refIdx < 0 || refIdx >= 8 {
		refIdx = 0
	}

	sh := 0
	if base >= 32 {
		sh = 1
	}
	delta := int(fh.RefDeltas[refIdx])
	if refIdx > 0 {
		mode := 1
		if isGlobalMV {
			mode = 0
		}
		delta += int(fh.ModeDeltas[mode])
	}
	return filterClamp(base+(delta<<sh), 0, 63)
}

// deblockLevelForEdge computes the effective filter level at a block edge.
// dav1d: L = l[0][0] ? l[0][0] : l[-1][0] (use current if non-zero, else previous).
func deblockLevelForEdge(fh *DecodedFrameHeader, cur, prev DeblockInfo, lvlIdx int) int {
	lCur := computeBlockLevel(fh, cur.DeltaLF, int(cur.RefFrame), cur.IsGlobalMV, lvlIdx)
	lPrev := computeBlockLevel(fh, prev.DeltaLF, int(prev.RefFrame), prev.IsGlobalMV, lvlIdx)
	if lCur != 0 {
		return lCur
	}
	return lPrev
}

// deblockFilterWidth determines the luma filter kernel width from transform
// sizes perpendicular to the edge. Returns 4, 8, or 16.
//
// In dav1d, luma filter widths are 4 << idx where idx = min(min(2, log2(cur)),
// min(2, log2(prev))). This means wd=6 never occurs for luma.
func deblockFilterWidth(txSizeCur, txSizePrev int) int {
	minTx := txSizeCur
	if txSizePrev < minTx {
		minTx = txSizePrev
	}

	if minTx >= 16 {
		return 16
	}
	if minTx >= 8 {
		return 8
	}
	return 4
}

// ---------------------------------------------------------------------------
// Core loop filter kernel
// ---------------------------------------------------------------------------

// loopFilterVertical applies the deblocking filter to a vertical edge at
// position (px, py) in a plane buffer. The edge is between columns px-1 and px.
// 'wd' is the filter width (4, 6, 8, or 14). 'count' is the number of rows to
// filter (4 for luma, 4>>subY for chroma in 4:2:0).
//
// This directly translates dav1d's loop_filter() with stridea=stride, strideb=1
// for vertical edges. AV1 spec Section 7.14.5.
func loopFilterVertical(plane []byte, stride, px, py, E, I, H, wd int) {
	loopFilterVerticalN(plane, stride, px, py, E, I, H, wd, 4)
}

func loopFilterVerticalN(plane []byte, stride, px, py, E, I, H, wd, count int) {
	planeH := len(plane) / stride
	for i := 0; i < count; i++ {
		row := py + i
		if row < 0 || row >= planeH {
			continue
		}
		loopFilterSample(plane, stride, row*stride+px, 1, E, I, H, wd)
	}
}

// loopFilterHorizontal applies the deblocking filter to a horizontal edge at
// position (px, py). The edge is between rows py-1 and py.
// 'count' is the number of columns to filter (4 for luma, 4>>subX for chroma).
//
// stridea=1, strideb=stride for horizontal edges.
func loopFilterHorizontal(plane []byte, stride, px, py, E, I, H, wd int) {
	loopFilterHorizontalN(plane, stride, px, py, E, I, H, wd, 4)
}

func loopFilterHorizontalN(plane []byte, stride, px, py, E, I, H, wd, count int) {
	planeW := stride
	for i := 0; i < count; i++ {
		col := px + i
		if col < 0 || col >= planeW {
			continue
		}
		loopFilterSample(plane, stride, py*stride+col, stride, E, I, H, wd)
	}
}

// loopFilterSample filters a single 1D line of samples perpendicular to the
// edge. 'pos' is the position of q0 in the plane buffer, and 'step' is the
// byte stride between successive samples along the filter direction (1 for
// vertical edges, stride for horizontal edges).
//
// The filter reads samples p[n] at pos-step*(n+1) and q[n] at pos+step*n.
// Matches dav1d loopfilter_tmpl.c loop_filter().
func loopFilterSample(plane []byte, stride, pos, step, E, I, H, wd int) {
	planeLen := len(plane)
	// Bounds check helper
	safeGet := func(idx int) int {
		if idx < 0 || idx >= planeLen {
			return 0
		}
		return int(plane[idx])
	}
	safeSet := func(idx int, v int) {
		if idx >= 0 && idx < planeLen {
			plane[idx] = clipPixel8(v)
		}
	}

	p1 := safeGet(pos - 2*step)
	p0 := safeGet(pos - 1*step)
	q0 := safeGet(pos + 0*step)
	q1 := safeGet(pos + 1*step)

	// Filter mask (fm): determines if filtering should be applied at all.
	fm := filterAbs(p1-p0) <= I && filterAbs(q1-q0) <= I &&
		filterAbs(p0-q0)*2+(filterAbs(p1-q1)>>1) <= E

	var p2, q2, p3, q3 int
	if wd > 4 {
		p2 = safeGet(pos - 3*step)
		q2 = safeGet(pos + 2*step)
		fm = fm && filterAbs(p2-p1) <= I && filterAbs(q2-q1) <= I
		if wd > 6 {
			p3 = safeGet(pos - 4*step)
			q3 = safeGet(pos + 3*step)
			fm = fm && filterAbs(p3-p2) <= I && filterAbs(q3-q2) <= I
		}
	}

	if !fm {
		return
	}

	// Flat detection for wide filters.
	var flat8in, flat8out bool

	if wd >= 6 {
		flat8in = filterAbs(p2-p0) <= 1 && filterAbs(p1-p0) <= 1 &&
			filterAbs(q1-q0) <= 1 && filterAbs(q2-q0) <= 1
	}
	if wd >= 8 && flat8in {
		flat8in = flat8in && filterAbs(p3-p0) <= 1 && filterAbs(q3-q0) <= 1
	}

	var p4, p5, p6, q4, q5, q6 int
	if wd >= 16 {
		p6 = safeGet(pos - 7*step)
		p5 = safeGet(pos - 6*step)
		p4 = safeGet(pos - 5*step)
		q4 = safeGet(pos + 4*step)
		q5 = safeGet(pos + 5*step)
		q6 = safeGet(pos + 6*step)

		flat8out = filterAbs(p6-p0) <= 1 && filterAbs(p5-p0) <= 1 &&
			filterAbs(p4-p0) <= 1 && filterAbs(q4-q0) <= 1 &&
			filterAbs(q5-q0) <= 1 && filterAbs(q6-q0) <= 1
	}

	if wd >= 16 && flat8out && flat8in {
		// Wide 14-tap filter. AV1 spec Section 7.14.5 (filter14).
		safeSet(pos-6*step, (p6+p6+p6+p6+p6+p6*2+p5*2+p4*2+p3+p2+p1+p0+q0+8)>>4)
		safeSet(pos-5*step, (p6+p6+p6+p6+p6+p5*2+p4*2+p3*2+p2+p1+p0+q0+q1+8)>>4)
		safeSet(pos-4*step, (p6+p6+p6+p6+p5+p4*2+p3*2+p2*2+p1+p0+q0+q1+q2+8)>>4)
		safeSet(pos-3*step, (p6+p6+p6+p5+p4+p3*2+p2*2+p1*2+p0+q0+q1+q2+q3+8)>>4)
		safeSet(pos-2*step, (p6+p6+p5+p4+p3+p2*2+p1*2+p0*2+q0+q1+q2+q3+q4+8)>>4)
		safeSet(pos-1*step, (p6+p5+p4+p3+p2+p1*2+p0*2+q0*2+q1+q2+q3+q4+q5+8)>>4)
		safeSet(pos+0*step, (p5+p4+p3+p2+p1+p0*2+q0*2+q1*2+q2+q3+q4+q5+q6+8)>>4)
		safeSet(pos+1*step, (p4+p3+p2+p1+p0+q0*2+q1*2+q2*2+q3+q4+q5+q6+q6+8)>>4)
		safeSet(pos+2*step, (p3+p2+p1+p0+q0+q1*2+q2*2+q3*2+q4+q5+q6+q6+q6+8)>>4)
		safeSet(pos+3*step, (p2+p1+p0+q0+q1+q2*2+q3*2+q4*2+q5+q6+q6+q6+q6+8)>>4)
		safeSet(pos+4*step, (p1+p0+q0+q1+q2+q3*2+q4*2+q5*2+q6+q6+q6+q6+q6+8)>>4)
		safeSet(pos+5*step, (p0+q0+q1+q2+q3+q4*2+q5*2+q6*2+q6+q6+q6+q6+q6+8)>>4)
	} else if wd >= 8 && flat8in {
		// 8-tap filter.
		safeSet(pos-3*step, (p3+p3+p3+2*p2+p1+p0+q0+4)>>3)
		safeSet(pos-2*step, (p3+p3+p2+2*p1+p0+q0+q1+4)>>3)
		safeSet(pos-1*step, (p3+p2+p1+2*p0+q0+q1+q2+4)>>3)
		safeSet(pos+0*step, (p2+p1+p0+2*q0+q1+q2+q3+4)>>3)
		safeSet(pos+1*step, (p1+p0+q0+2*q1+q2+q3+q3+4)>>3)
		safeSet(pos+2*step, (p0+q0+q1+2*q2+q3+q3+q3+4)>>3)
	} else if wd == 6 && flat8in {
		// 6-tap filter.
		safeSet(pos-2*step, (p2+2*p2+2*p1+2*p0+q0+4)>>3)
		safeSet(pos-1*step, (p2+2*p1+2*p0+2*q0+q1+4)>>3)
		safeSet(pos+0*step, (p1+2*p0+2*q0+2*q1+q2+4)>>3)
		safeSet(pos+1*step, (p0+2*q0+2*q1+2*q2+q2+4)>>3)
	} else {
		// Narrow 4-tap filter (with HEV detection).
		hev := filterAbs(p1-p0) > H || filterAbs(q1-q0) > H

		if hev {
			f := iclipDiff(3*(q0-p0) + iclipDiff(p1-q1))

			f1 := filterMin(f+4, 127) >> 3
			f2 := filterMin(f+3, 127) >> 3

			safeSet(pos-1*step, p0+f2)
			safeSet(pos+0*step, q0-f1)
		} else {
			f := iclipDiff(3 * (q0 - p0))

			f1 := filterMin(f+4, 127) >> 3
			f2 := filterMin(f+3, 127) >> 3

			safeSet(pos-1*step, p0+f2)
			safeSet(pos+0*step, q0-f1)

			f3 := (f1 + 1) >> 1
			safeSet(pos-2*step, p1+f3)
			safeSet(pos+1*step, q1-f3)
		}
	}
}

// iclipDiff clamps v to [-128, 127] for 8-bit processing.
// Matches dav1d's iclip_diff for bitdepth_min_8 = 0.
func iclipDiff(v int) int {
	if v < -128 {
		return -128
	}
	if v > 127 {
		return 127
	}
	return v
}
