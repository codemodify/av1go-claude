// Package decoder implements AV1 bitstream decoding.
//
// This file implements block-level decoding: superblock partition parsing,
// intra mode decoding, coefficient decoding, prediction, and reconstruction.
// AV1 spec Sections 5.11.3 (decode_partition) and 5.11.4 (decode_block).
package decoder

import (
	"av1go/obu"
	"fmt"
)

// Partition type constants.
// AV1 spec Section 6.4.2, Table 4.
const (
	PartitionNone  = 0
	PartitionHorz  = 1
	PartitionVert  = 2
	PartitionSplit = 3
	// Extended partition types (available for blocks >= 16x16 MI).
	PartitionHorzA = 4
	PartitionHorzB = 5
	PartitionVertA = 6
	PartitionVertB = 7
	PartitionHorz4 = 8
	PartitionVert4 = 9
)

// Intra edge flags matching dav1d's intra_edge.h EdgeFlags.
// Different subsampling formats track separate flags because chroma blocks
// at 4:2:0/4:2:2 have different spatial neighbor relationships than luma.
const (
	EdgeI444TopHasRight   uint8 = 1 << 0
	EdgeI422TopHasRight   uint8 = 1 << 1
	EdgeI420TopHasRight   uint8 = 1 << 2
	EdgeI444LeftHasBottom uint8 = 1 << 3
	EdgeI422LeftHasBottom uint8 = 1 << 4
	EdgeI420LeftHasBottom uint8 = 1 << 5
	EdgeAllTopHasRight    uint8 = EdgeI444TopHasRight | EdgeI422TopHasRight | EdgeI420TopHasRight
	EdgeAllLeftHasBottom  uint8 = EdgeI444LeftHasBottom | EdgeI422LeftHasBottom | EdgeI420LeftHasBottom
	EdgeAllTRAndBL        uint8 = EdgeAllTopHasRight | EdgeAllLeftHasBottom
)

// gatherTopPartitionProb computes the probability of SPLIT (vs HORZ) when
// rows don't fit but columns do. Derived from the partition CDF.
// Matches dav1d env.h gather_top_partition_prob.
// pc is the partition CDF in ICDF format. bSize is in MI units.
func gatherTopPartitionProb(pc []uint16, bSize int) uint16 {
	// Matches dav1d env.h gather_top_partition_prob exactly.
	// in[V-1] - in[T_TOP_SPLIT] = P(V) + P(SPLIT) + P(T_TOP_SPLIT)
	out := pc[PartitionVert-1] - pc[PartitionHorzA]
	// in[T_LEFT_SPLIT-1] = accumulates T_BOTTOM_SPLIT probability
	out += pc[PartitionVertA-1]
	if bSize < 32 { // not 128x128
		// in[V4-1] - in[T_RIGHT_SPLIT] = P(T_RIGHT_SPLIT) + P(H4) + P(V4)
		out += pc[PartitionVert4-1] - pc[PartitionVertB]
	}
	return out
}

// gatherLeftPartitionProb computes the probability of SPLIT (vs VERT) when
// columns don't fit but rows do. Derived from the partition CDF.
// Matches dav1d env.h gather_left_partition_prob.
func gatherLeftPartitionProb(pc []uint16, bSize int) uint16 {
	// Sum probabilities of partition types that need horizontal splitting:
	// H(1), SPLIT(3), T_TOP_SPLIT(4), T_BOTTOM_SPLIT(5), T_LEFT_SPLIT(6), H4(8)
	// P(H) = in[H-1] - in[H] = in[0] - in[1]
	out := pc[PartitionHorz-1] - pc[PartitionHorz]
	// P(SPLIT) + P(T_TOP_SPLIT) + P(T_BOTTOM_SPLIT) + P(T_LEFT_SPLIT)
	// = in[SPLIT-1] - in[T_LEFT_SPLIT]
	out += pc[PartitionSplit-1] - pc[PartitionVertA]
	if bSize < 32 { // not 128x128
		// P(H4) = in[H4-1] - in[H4]
		out += pc[PartitionHorz4-1] - pc[PartitionHorz4]
	}
	return out
}

// decodeSuperblock decodes a single superblock starting at (miRow, miCol).
// sbSize is the superblock size in MI units (16 for 64x64, 32 for 128x128).
// AV1 spec Section 5.11.2.
func (td *TileDecoder) decodeSuperblock(bc *BoolReader, miRow, miCol, sbSize int) error {
	// Reset per-SB delta Q tracking.
	td.sbDeltaQDone = false

	// Read loop restoration parameters before decoding the partition tree.
	// This matches dav1d's behavior where read_restoration_info is called
	// per-superblock before decode_sb (src/decode.c lines 2662-2714).
	if err := td.readLoopRestorationForSuperblock(bc, miRow, miCol, sbSize); err != nil {
		return err
	}

	// Root of partition tree always has topHasRight=true, leftHasBottom=false
	// (matches dav1d: init_mode_node called with top_has_right=1, left_has_bottom=0).
	return td.decodePartition(bc, miRow, miCol, sbSize, true, false)
}

// boolsToEdgeFlags converts topHasRight/leftHasBottom booleans into the packed
// edge flags expected by decodeBlock. Sets all subsampling format bits (I444,
// I422, I420) uniformly. Correct for block levels > BL_8X8.
func boolsToEdgeFlags(topHasRight, leftHasBottom bool) uint8 {
	var flags uint8
	if topHasRight {
		flags |= EdgeAllTopHasRight
	}
	if leftHasBottom {
		flags |= EdgeAllLeftHasBottom
	}
	return flags
}

// edgeFlagsFromParent constructs parent edge_flags from booleans.
// This is the uniform form used as input to init_edges logic.
func edgeFlagsFromParent(topHasRight, leftHasBottom bool) uint8 {
	var ef uint8
	if topHasRight {
		ef |= EdgeAllTopHasRight
	}
	if leftHasBottom {
		ef |= EdgeAllLeftHasBottom
	}
	return ef
}

// bl8x8SplitEdgeFlags computes edge flags for the 4 children of a BL_8X8
// SPLIT partition, matching dav1d's init_edges for bl==BL_8X8.
// Returns [TL, TR, BL, BR] edge flags.
// dav1d ref: src/intra_edge.c init_edges(), lines 63-77.
func bl8x8SplitEdgeFlags(parentTHR, parentLHB bool) [4]uint8 {
	ef := edgeFlagsFromParent(parentTHR, parentLHB)
	var flags [4]uint8

	// TL: always EDGE_ALL_TR_AND_BL (dav1d decode.c line 2189)
	flags[0] = EdgeAllTRAndBL

	// TR: split[0] = (ef & ALL_THR) | I422_LHB
	flags[1] = (ef & EdgeAllTopHasRight) | EdgeI422LeftHasBottom

	// BL: split[1] = ef | I444_THR
	flags[2] = ef | EdgeI444TopHasRight

	// BR: split[2] = ef & (I420_THR | I420_LHB | I422_LHB)
	flags[3] = ef & (EdgeI420TopHasRight | EdgeI420LeftHasBottom | EdgeI422LeftHasBottom)

	return flags
}

// bl8x8HorzEdgeFlags computes edge flags for the 2 children of a BL_8X8
// HORZ partition, matching dav1d's init_edges h[0] and h[1] for bl==BL_8X8.
func bl8x8HorzEdgeFlags(parentTHR, parentLHB bool) [2]uint8 {
	ef := edgeFlagsFromParent(parentTHR, parentLHB)
	var flags [2]uint8
	// h[0] = ef | ALL_LHB
	flags[0] = ef | EdgeAllLeftHasBottom
	// h[1] = ef & (ALL_LHB | I420_THR)
	flags[1] = ef & (EdgeAllLeftHasBottom | EdgeI420TopHasRight)
	return flags
}

// bl8x8VertEdgeFlags computes edge flags for the 2 children of a BL_8X8
// VERT partition, matching dav1d's init_edges v[0] and v[1] for bl==BL_8X8.
func bl8x8VertEdgeFlags(parentTHR, parentLHB bool) [2]uint8 {
	ef := edgeFlagsFromParent(parentTHR, parentLHB)
	var flags [2]uint8
	// v[0] = ef | ALL_THR
	flags[0] = ef | EdgeAllTopHasRight
	// v[1] = ef & (ALL_THR | I420_LHB | I422_LHB)
	flags[1] = ef & (EdgeAllTopHasRight | EdgeI420LeftHasBottom | EdgeI422LeftHasBottom)
	return flags
}

// bl16x16H4EdgeFlags computes the h4 edge flags for the middle strips of a
// BL_16X16 HORZ4 partition, matching dav1d's init_edges nwc->h4 for bl==BL_16X16.
func bl16x16H4EdgeFlags(parentTHR bool) uint8 {
	// h4 base = ALL_LHB; at BL_16X16, h4 |= ef & I420_THR
	flags := EdgeAllLeftHasBottom
	if parentTHR {
		flags |= EdgeI420TopHasRight
	}
	return flags
}

// bl16x16V4EdgeFlags computes the v4 edge flags for the middle strips of a
// BL_16X16 VERT4 partition, matching dav1d's init_edges nwc->v4 for bl==BL_16X16.
func bl16x16V4EdgeFlags(parentLHB bool) uint8 {
	// v4 base = ALL_THR; at BL_16X16, v4 |= ef & (I420_LHB | I422_LHB)
	flags := EdgeAllTopHasRight
	if parentLHB {
		flags |= EdgeI420LeftHasBottom | EdgeI422LeftHasBottom
	}
	return flags
}

// decodePartition recursively decodes the partition tree for a block
// of size bSize MI units starting at (miRow, miCol).
// AV1 spec Section 5.11.4 (partition).
func (td *TileDecoder) decodePartition(bc *BoolReader, miRow, miCol, bSize int, topHasRight, leftHasBottom bool) error {
	// Out of frame or tile bounds: nothing to decode.
	if miRow >= int(td.fh.MiRows) || miCol >= int(td.fh.MiCols) {
		return nil
	}
	if miRow >= td.tileRowEnd || miCol >= td.tileColEnd {
		return nil
	}

	halfSize := bSize >> 1
	hasRows := (miRow + halfSize) < int(td.fh.MiRows)
	hasCols := (miCol + halfSize) < int(td.fh.MiCols)

	// Determine partition context.
	ctx := td.getPartitionContext(miRow, miCol, bSize)

	var partition int

	if bSize <= 1 {
		partition = PartitionNone
	} else if hasRows && hasCols {
		nsyms := 10
		if bSize >= 32 {
			nsyms = 8
		} else if bSize <= 2 {
			nsyms = 4
		}

		cdf := td.cdf.Partition[ctx]
		sym, err := bc.ReadSymbol(cdf, nsyms)
		if err != nil {
			return fmt.Errorf("partition at (%d,%d) bSize=%d: %w", miRow, miCol, bSize, err)
		}
		partition = sym
	} else if hasCols {
		pc := td.cdf.Partition[ctx]
		prob := gatherTopPartitionProb(pc, bSize)
		isSplit, err := bc.ReadBool(prob)
		if err != nil {
			return err
		}
		if isSplit {
			partition = PartitionSplit
		} else {
			partition = PartitionHorz
		}
	} else if hasRows {
		pc := td.cdf.Partition[ctx]
		prob := gatherLeftPartitionProb(pc, bSize)
		isSplit, err := bc.ReadBool(prob)
		if err != nil {
			return err
		}
		if isSplit {
			partition = PartitionSplit
		} else {
			partition = PartitionVert
		}
	} else {
		partition = PartitionSplit
	}

	// Decode sub-blocks according to the partition type.
	switch partition {
	case PartitionNone:
		return td.decodeBlock(bc, miRow, miCol, bSize, bSize, boolsToEdgeFlags(topHasRight, leftHasBottom))

	case PartitionHorz:
		if bSize == 2 {
			// BL_8X8 HORZ: I420/I422 flags differ from I444.
			// dav1d: h[0] = ef | ALL_LHB, h[1] = ef & (ALL_LHB | I420_THR)
			hf := bl8x8HorzEdgeFlags(topHasRight, leftHasBottom)
			if err := td.decodeBlock(bc, miRow, miCol, bSize, halfSize, hf[0]); err != nil {
				return err
			}
			if miRow+halfSize < int(td.fh.MiRows) {
				return td.decodeBlock(bc, miRow+halfSize, miCol, bSize, halfSize, hf[1])
			}
			return nil
		}
		// node->h[0]: top always has LHB (bottom half is sibling), keeps parent THR.
		if err := td.decodeBlock(bc, miRow, miCol, bSize, halfSize, boolsToEdgeFlags(topHasRight, true)); err != nil {
			return err
		}
		// node->h[1]: bottom loses THR, keeps parent LHB.
		if miRow+halfSize < int(td.fh.MiRows) {
			return td.decodeBlock(bc, miRow+halfSize, miCol, bSize, halfSize, boolsToEdgeFlags(false, leftHasBottom))
		}
		return nil

	case PartitionVert:
		if bSize == 2 {
			// BL_8X8 VERT: I420/I422 flags differ from I444.
			// dav1d: v[0] = ef | ALL_THR, v[1] = ef & (ALL_THR | I420_LHB | I422_LHB)
			vf := bl8x8VertEdgeFlags(topHasRight, leftHasBottom)
			if err := td.decodeBlock(bc, miRow, miCol, halfSize, bSize, vf[0]); err != nil {
				return err
			}
			if miCol+halfSize < int(td.fh.MiCols) {
				return td.decodeBlock(bc, miRow, miCol+halfSize, halfSize, bSize, vf[1])
			}
			return nil
		}
		// node->v[0]: left always has THR (right half is sibling), keeps parent LHB.
		if err := td.decodeBlock(bc, miRow, miCol, halfSize, bSize, boolsToEdgeFlags(true, leftHasBottom)); err != nil {
			return err
		}
		// node->v[1]: right loses LHB, keeps parent THR.
		if miCol+halfSize < int(td.fh.MiCols) {
			return td.decodeBlock(bc, miRow, miCol+halfSize, halfSize, bSize, boolsToEdgeFlags(topHasRight, false))
		}
		return nil

	case PartitionSplit:
		if bSize == 2 {
			// BL_8X8 SPLIT into 4x4: I420/I422 edge flags differ from I444.
			// dav1d: TL=ALL_TR_AND_BL, TR=split[0], BL=split[1], BR=split[2]
			sf := bl8x8SplitEdgeFlags(topHasRight, leftHasBottom)
			// TL
			if err := td.decodeBlock(bc, miRow, miCol, 1, 1, sf[0]); err != nil {
				return err
			}
			// TR
			if miCol+1 < int(td.fh.MiCols) {
				if err := td.decodeBlock(bc, miRow, miCol+1, 1, 1, sf[1]); err != nil {
					return err
				}
			}
			// BL
			if miRow+1 < int(td.fh.MiRows) {
				if err := td.decodeBlock(bc, miRow+1, miCol, 1, 1, sf[2]); err != nil {
					return err
				}
			}
			// BR
			if miRow+1 < int(td.fh.MiRows) && miCol+1 < int(td.fh.MiCols) {
				if err := td.decodeBlock(bc, miRow+1, miCol+1, 1, 1, sf[3]); err != nil {
					return err
				}
			}
			return nil
		}
		// Z-order: TL, TR, BL, BR with edge flags from dav1d init_mode_node.
		// TL: THR=true (TR sibling exists), LHB=true (BL sibling exists)
		if err := td.decodePartition(bc, miRow, miCol, halfSize, true, true); err != nil {
			return err
		}
		// TR: THR=parent.THR, LHB=false
		if err := td.decodePartition(bc, miRow, miCol+halfSize, halfSize, topHasRight, false); err != nil {
			return err
		}
		// BL: THR=true (BR sibling exists), LHB=parent.LHB
		if err := td.decodePartition(bc, miRow+halfSize, miCol, halfSize, true, leftHasBottom); err != nil {
			return err
		}
		// BR: THR=false, LHB=false
		return td.decodePartition(bc, miRow+halfSize, miCol+halfSize, halfSize, false, false)

	case PartitionHorzA:
		// T_TOP_SPLIT: partition context from dav1d_al_part_ctx table.
		// All sub-blocks in the same T-split share the parent's partition context.
		aCtx, lCtx := tSplitPartCtx(bSize, PartitionHorzA)
		td.partCtxAbove = aCtx
		td.partCtxLeft = lCtx
		if err := td.decodeBlock(bc, miRow, miCol, halfSize, halfSize, boolsToEdgeFlags(true, true)); err != nil {
			return err
		}
		td.partCtxAbove = aCtx
		td.partCtxLeft = lCtx
		if err := td.decodeBlock(bc, miRow, miCol+halfSize, halfSize, halfSize, boolsToEdgeFlags(topHasRight, false)); err != nil {
			return err
		}
		td.partCtxAbove = aCtx
		td.partCtxLeft = lCtx
		return td.decodeBlock(bc, miRow+halfSize, miCol, bSize, halfSize, boolsToEdgeFlags(false, leftHasBottom))

	case PartitionHorzB:
		// T_BOTTOM_SPLIT
		aCtx, lCtx := tSplitPartCtx(bSize, PartitionHorzB)
		td.partCtxAbove = aCtx
		td.partCtxLeft = lCtx
		if err := td.decodeBlock(bc, miRow, miCol, bSize, halfSize, boolsToEdgeFlags(topHasRight, true)); err != nil {
			return err
		}
		td.partCtxAbove = aCtx
		td.partCtxLeft = lCtx
		if err := td.decodeBlock(bc, miRow+halfSize, miCol, halfSize, halfSize, boolsToEdgeFlags(true, leftHasBottom)); err != nil {
			return err
		}
		td.partCtxAbove = aCtx
		td.partCtxLeft = lCtx
		return td.decodeBlock(bc, miRow+halfSize, miCol+halfSize, halfSize, halfSize, boolsToEdgeFlags(false, false))

	case PartitionVertA:
		// T_LEFT_SPLIT
		aCtx, lCtx := tSplitPartCtx(bSize, PartitionVertA)
		td.partCtxAbove = aCtx
		td.partCtxLeft = lCtx
		if err := td.decodeBlock(bc, miRow, miCol, halfSize, halfSize, boolsToEdgeFlags(true, true)); err != nil {
			return err
		}
		td.partCtxAbove = aCtx
		td.partCtxLeft = lCtx
		if err := td.decodeBlock(bc, miRow+halfSize, miCol, halfSize, halfSize, boolsToEdgeFlags(false, leftHasBottom)); err != nil {
			return err
		}
		td.partCtxAbove = aCtx
		td.partCtxLeft = lCtx
		return td.decodeBlock(bc, miRow, miCol+halfSize, halfSize, bSize, boolsToEdgeFlags(topHasRight, false))

	case PartitionVertB:
		// T_RIGHT_SPLIT
		aCtx, lCtx := tSplitPartCtx(bSize, PartitionVertB)
		td.partCtxAbove = aCtx
		td.partCtxLeft = lCtx
		if err := td.decodeBlock(bc, miRow, miCol, halfSize, bSize, boolsToEdgeFlags(true, leftHasBottom)); err != nil {
			return err
		}
		td.partCtxAbove = aCtx
		td.partCtxLeft = lCtx
		if err := td.decodeBlock(bc, miRow, miCol+halfSize, halfSize, halfSize, boolsToEdgeFlags(topHasRight, true)); err != nil {
			return err
		}
		td.partCtxAbove = aCtx
		td.partCtxLeft = lCtx
		return td.decodeBlock(bc, miRow+halfSize, miCol+halfSize, halfSize, halfSize, boolsToEdgeFlags(false, false))

	case PartitionHorz4:
		// Four horizontal strips. Matches dav1d: h[0], h4, ALL_LHB, h[1].
		quarterSize := bSize >> 2
		// Compute edge flags for each strip. At BL_16X16 (bSize==4), h4 strips
		// need special I420 handling: h4 = ALL_LHB | (parentTHR ? I420_THR : 0).
		var h4EdgeFlags [4]uint8
		h4EdgeFlags[0] = boolsToEdgeFlags(topHasRight, true)           // h[0]
		h4EdgeFlags[3] = boolsToEdgeFlags(false, leftHasBottom)        // h[1]
		if bSize == 4 {
			// BL_16X16: h4 has I420 THR from parent
			h4ef := bl16x16H4EdgeFlags(topHasRight)
			h4EdgeFlags[1] = h4ef                                      // h4
			h4EdgeFlags[2] = EdgeAllLeftHasBottom                      // ALL_LHB
		} else {
			h4EdgeFlags[1] = boolsToEdgeFlags(false, true)             // h4 uniform
			h4EdgeFlags[2] = boolsToEdgeFlags(false, true)             // ALL_LHB
		}
		for i := 0; i < 4; i++ {
			row := miRow + i*quarterSize
			if row >= int(td.fh.MiRows) {
				break
			}
			if err := td.decodeBlock(bc, row, miCol, bSize, quarterSize, h4EdgeFlags[i]); err != nil {
				return err
			}
		}
		return nil

	case PartitionVert4:
		// Four vertical strips. Matches dav1d: v[0], v4, ALL_THR, v[1].
		quarterSize := bSize >> 2
		// Compute edge flags for each strip. At BL_16X16 (bSize==4), v4 strips
		// need special I420 handling: v4 = ALL_THR | (parentLHB ? I420_LHB|I422_LHB : 0).
		var v4EdgeFlags [4]uint8
		v4EdgeFlags[0] = boolsToEdgeFlags(true, leftHasBottom)         // v[0]
		v4EdgeFlags[3] = boolsToEdgeFlags(topHasRight, false)          // v[1]
		if bSize == 4 {
			// BL_16X16: v4 has I420/I422 LHB from parent
			v4ef := bl16x16V4EdgeFlags(leftHasBottom)
			v4EdgeFlags[1] = v4ef                                      // v4
			v4EdgeFlags[2] = EdgeAllTopHasRight                        // ALL_THR
		} else {
			v4EdgeFlags[1] = boolsToEdgeFlags(true, false)             // v4 uniform
			v4EdgeFlags[2] = boolsToEdgeFlags(true, false)             // ALL_THR
		}
		for i := 0; i < 4; i++ {
			col := miCol + i*quarterSize
			if col >= int(td.fh.MiCols) {
				break
			}
			if err := td.decodeBlock(bc, miRow, col, quarterSize, bSize, v4EdgeFlags[i]); err != nil {
				return err
			}
		}
		return nil
	}

	return fmt.Errorf("unknown partition type %d at (%d,%d)", partition, miRow, miCol)
}

// decodeBlock decodes a single coding block of size bW x bH MI units.
// AV1 spec Section 5.11.5 (mode_info) and 5.11.7 (intra_frame_mode_info).
//
// Read order per AV1 spec:
//  1. skip
//  2. CDEF index (literal bits, per 64x64 region)
//  3. read_tx_size (TX_MODE_SELECT, non-skip, non-lossless)
//  4. Y intra mode
//  5. Y angle delta (directional modes, block >= 8x8 pixels)
//  6. UV mode with CfL (if HasChroma)
//  7. CfL alpha (if UV_CFL_PRED)
//  8. UV angle delta (directional UV, block >= 8x8 pixels)
//  9. palette_mode_info (Y and UV flags)
//  10. filter_intra_mode_info
//  11. Coefficients (residual)
func (td *TileDecoder) decodeBlock(bc *BoolReader, miRow, miCol, bW, bH int, edgeFlags uint8) error {
	frameIsIntra := td.fh.FrameType == obu.FrameTypeKey || td.fh.FrameType == obu.FrameTypeIntraOnly

	if !frameIsIntra {
		return td.decodeInterBlock(bc, miRow, miCol, bW, bH, edgeFlags)
	}

	// Nominal (unclamped) block dimensions in pixels.
	// Used for CDF context selection and feature eligibility checks.
	nomPixW := bW * 4
	nomPixH := bH * 4

	// --- 1. skip flag ---
	skipCtx := td.getSkipContext(miRow, miCol)
	skipSym, err := bc.ReadSymbolBoolInt(td.cdf.Skip[skipCtx])
	if err != nil {
		return fmt.Errorf("skip at (%d,%d): %w", miRow, miCol, err)
	}
	skip := skipSym == 1
	// --- 2. CDEF index: literal bits, per 64x64 region ---
	// AV1 spec Section 5.11.15 (read_cdef): read ONE cdef_idx per block call,
	// for the 64x64 region containing the block's (miRow, miCol).
	// For blocks larger than 64x64, the single value is replicated to all
	// covered 64x64 regions (matching dav1d decode.c lines 942-957).
	if !skip && !td.fh.CodedLossless && td.sh.EnableCDEF && !td.fh.AllowIntraBC && td.fh.CDEFBits > 0 {
		cdefStep := 16 // 64x64 = 16 MI units
		cdefRow := miRow & ^(cdefStep - 1)
		cdefCol := miCol & ^(cdefStep - 1)
		cdefKey := uint32(cdefRow/cdefStep)*256 + uint32(cdefCol/cdefStep)
		if !td.cdefRead[cdefKey] {
			cdefIdx, err := bc.ReadLiteral(td.fh.CDEFBits)
			if err != nil {
				return fmt.Errorf("cdef_idx at (%d,%d): %w", miRow, miCol, err)
			}
			td.cdefRead[cdefKey] = true
			if td.cdefIndices != nil {
				td.cdefIndices[cdefKey] = int(cdefIdx)
			}
			if bW > 16 {
				td.cdefRead[cdefKey+1] = true
				if td.cdefIndices != nil {
					td.cdefIndices[cdefKey+1] = int(cdefIdx)
				}
			}
			if bH > 16 {
				td.cdefRead[cdefKey+256] = true
				if td.cdefIndices != nil {
					td.cdefIndices[cdefKey+256] = int(cdefIdx)
				}
			}
			if bW > 16 && bH > 16 {
				td.cdefRead[cdefKey+257] = true
				if td.cdefIndices != nil {
					td.cdefIndices[cdefKey+257] = int(cdefIdx)
				}
			}
		}
	}

	// --- 3b. delta_q / delta_lf ---
	// For intra frames, DeltaQPresent is typically false, so this is a no-op.
	if err := td.readSBDeltaQLF(bc, bW, bH, skip); err != nil {
		return fmt.Errorf("delta_q/lf at (%d,%d): %w", miRow, miCol, err)
	}
	// Blocks within the MI grid (miRow < MiRows, miCol < MiCols) must always
	// be fully reconstructed using their nominal (unclamped) dimensions, even
	// when the block extends past FrameHeight/FrameWidth. dav1d reconstructs
	// these blocks into the frame buffer's padding area so that directional
	// intra prediction and bottom-left/top-right extensions of neighboring
	// blocks read valid reference pixels. The frame buffer is allocated with
	// extra rows beyond FrameHeight for this purpose.
	//
	// The decodePartition guard (miRow >= MiRows → return) already prevents
	// blocks fully outside the MI grid from reaching here, so all blocks that
	// arrive here have valid content to reconstruct.

	// --- 3c. use_intrabc ---
	// AV1 spec Section 5.11.4: when AllowIntraBC, read use_intrabc flag.
	// If set, the block uses IntraBC (block copy from the same frame).
	useIntraBC := false
	if td.fh.AllowIntraBC {
		ibcSym, err := bc.ReadSymbolBoolInt(td.cdf.IntraBC)
		if err != nil {
			return fmt.Errorf("use_intrabc at (%d,%d): %w", miRow, miCol, err)
		}
		useIntraBC = ibcSym == 1
	}

	if useIntraBC {
		return td.decodeIntraBCBlock(bc, miRow, miCol, bW, bH, skip, edgeFlags)
	}

	// --- 4. Y intra mode ---
	aboveMode := td.getAboveIntraMode(miRow, miCol)
	leftMode := td.getLeftIntraMode(miRow, miCol)
	yModeCdf := td.cdf.IntraFrameYMode[IntraModeCtx[aboveMode]][IntraModeCtx[leftMode]]
	yMode, err := bc.ReadSymbol(yModeCdf, NumIntraModes)
	if err != nil {
		return fmt.Errorf("y_mode at (%d,%d): %w", miRow, miCol, err)
	}
	// --- 5. Y angle delta ---
	// AV1 spec: directional modes V_PRED(1)..D67_PRED(8), CDF index = mode - V_PRED.
	// Condition: block area >= 4 MI units (bW*bH >= 4), matching dav1d b_dim[2]+b_dim[3] >= 2.
	angleDelta := 0
	if yMode >= V_PRED && yMode <= D67_PRED && bW*bH >= 4 {
		angleDeltaCdf := td.cdf.AngleDelta[yMode-V_PRED]
		sym, err := bc.ReadSymbol(angleDeltaCdf, 7)
		if err != nil {
			return fmt.Errorf("angle_delta_y at (%d,%d): %w", miRow, miCol, err)
		}
		angleDelta = sym - 3
	}
	// --- HasChroma check ---
	// AV1 spec Section 6.4.3.
	subX := int(td.sh.ColorConfig.SubsamplingX)
	subY := int(td.sh.ColorConfig.SubsamplingY)
	hasChroma := true
	if subX == 1 && !(miCol&1 == 1 || bW >= 2) {
		hasChroma = false
	}
	if subY == 1 && !(miRow&1 == 1 || bH >= 2) {
		hasChroma = false
	}
	// (trace moved below after uvMode is set)

	// --- 6-8. UV mode + CfL alpha + UV angle delta ---
	uvMode := DC_PRED
	uvAngleDelta := 0
	cflAlphaU := 0
	cflAlphaV := 0
	if hasChroma {
		// CfL allowed check. Matches dav1d decode.c:
		//   lossless ? (cbw4 == 1 && cbh4 == 1) : !!(cfl_allowed_mask & (1 << bs))
		// In lossless mode, CFL is allowed only for 4x4 chroma blocks (1x1 MI).
		// In non-lossless mode, CFL is allowed for blocks up to 32x32 pixels.
		cflAllowed := 0
		if td.fh.CodedLossless {
			// Chroma MI dims: (bW + subX) >> subX, (bH + subY) >> subY
			cbw4 := (bW + subX) >> subX
			cbh4 := (bH + subY) >> subY
			if cbw4 == 1 && cbh4 == 1 {
				cflAllowed = 1
			}
		} else if nomPixW <= 32 && nomPixH <= 32 {
			cflAllowed = 1
		}
		uvNsyms := 13
		if cflAllowed == 1 {
			uvNsyms = 14
		}
		uvModeCdf := td.cdf.UVMode[cflAllowed][yMode]
		uvMode, err = bc.ReadSymbol(uvModeCdf, uvNsyms)
		if err != nil {
			return fmt.Errorf("uv_mode at (%d,%d): %w", miRow, miCol, err)
		}
		// 7. CfL alpha (UV_CFL_PRED = mode 13).
		if uvMode == 13 {
			jointSign, err := bc.ReadSymbol(td.cdf.CflSign, 8)
			if err != nil {
				return fmt.Errorf("cfl_sign at (%d,%d): %w", miRow, miCol, err)
			}
			signU := (jointSign + 1) / 3
			signV := (jointSign + 1) % 3
			if signU != 0 {
				alphaCtxU := signV
				if signU == 2 {
					alphaCtxU += 3
				}
				alphaIdx, err := bc.ReadSymbol(td.cdf.CflAlpha[alphaCtxU], 16)
				if err != nil {
					return fmt.Errorf("cfl_alpha_u at (%d,%d): %w", miRow, miCol, err)
				}
				cflAlphaU = alphaIdx + 1
				if signU == 1 {
					cflAlphaU = -cflAlphaU
				}
			}
			if signV != 0 {
				alphaCtxV := signU
				if signV == 2 {
					alphaCtxV += 3
				}
				alphaIdx, err := bc.ReadSymbol(td.cdf.CflAlpha[alphaCtxV], 16)
				if err != nil {
					return fmt.Errorf("cfl_alpha_v at (%d,%d): %w", miRow, miCol, err)
				}
				cflAlphaV = alphaIdx + 1
				if signV == 1 {
					cflAlphaV = -cflAlphaV
				}
			}
		}

		// 8. UV angle delta (same conditions as Y).
		if uvMode >= V_PRED && uvMode <= D67_PRED && bW*bH >= 4 {
			sym, err := bc.ReadSymbol(td.cdf.AngleDelta[uvMode-V_PRED], 7)
			if err != nil {
				return fmt.Errorf("angle_delta_uv at (%d,%d): %w", miRow, miCol, err)
			}
			uvAngleDelta = sym - 3
		}
	}

	// --- 9. palette_mode_info ---
	// AV1 spec Section 5.11.42.
	paletteSizeY := 0
	var paletteY []uint8 // Y palette colors
	paletteAllowed := td.fh.AllowScreenContentTools && bW+bH >= 4 && nomPixW <= 64 && nomPixH <= 64
	paletteSizeUV := 0
	var paletteU, paletteV []uint8 // chroma palette colors
	if paletteAllowed {
		bsCtx := paletteBsizeCtx(nomPixW, nomPixH)
		if bsCtx < 7 {
			if yMode == DC_PRED {
				paletteCtx := td.getPaletteContext(miRow, miCol)
				palYFlag, err := bc.ReadSymbolBoolInt(td.cdf.PaletteYMode[bsCtx][paletteCtx])
				if err != nil {
					return fmt.Errorf("palette_y at (%d,%d): %w", miRow, miCol, err)
				}
				if palYFlag == 1 {
					pal, err := td.readPalettePlane(bc, 0, bsCtx, miRow, miCol, bW, bH)
					if err != nil {
						return fmt.Errorf("palette_y data at (%d,%d): %w", miRow, miCol, err)
					}
					paletteY = pal
					paletteSizeY = len(pal)
				}
			}

			// Palette UV flag: read if hasChroma and UV mode is DC_PRED.
			if hasChroma && uvMode == DC_PRED {
				uvCtx := 0
				if paletteSizeY > 0 {
					uvCtx = 1
				}
				palUVFlag, err := bc.ReadSymbolBoolInt(td.cdf.PaletteUVMode[uvCtx])
				if err != nil {
					return fmt.Errorf("palette_uv at (%d,%d): %w", miRow, miCol, err)
				}
				if palUVFlag == 1 {
					pU, pV, err := td.readPaletteUV(bc, bsCtx, paletteSizeY, miRow, miCol, bW, bH)
					if err != nil {
						return fmt.Errorf("palette_uv data at (%d,%d): %w", miRow, miCol, err)
					}
					paletteU = pU
					paletteV = pV
					paletteSizeUV = len(pU)
				}
			}
		}
	}

	// --- 10. filter_intra_mode_info ---
	// AV1 spec Section 5.11.24.
	// Conditions: EnableFilterIntra, yMode==DC_PRED, paletteSizeY==0,
	// max TX width ≤ 32 and height ≤ 32.
	//
	// When filter_intra is active, dav1d maps the filter_intra mode to a
	// substitute y_mode for txType CDF selection via filter_mode_to_y_mode[].
	// We track this as yModeForTx (defaults to yMode).
	yModeForTx := yMode
	filterIntraMode := -1 // -1 means not active
	if td.sh.EnableFilterIntra && yMode == DC_PRED && paletteSizeY == 0 && nomPixW <= 32 && nomPixH <= 32 {
		bsCtx := blockSizeEnum(nomPixW, nomPixH)
		if bsCtx < 22 {
			useFlag, err := bc.ReadSymbolBoolInt(td.cdf.UseFilterIntra[bsCtx])
			if err != nil {
				return fmt.Errorf("use_filter_intra at (%d,%d): %w", miRow, miCol, err)
			}
			if useFlag == 1 {
				fiMode, err := bc.ReadSymbol(td.cdf.FilterIntraMode, 5)
				if err != nil {
					return fmt.Errorf("filter_intra_mode at (%d,%d): %w", miRow, miCol, err)
				}
				filterIntraMode = fiMode
				// dav1d: filter_mode_to_y_mode = {DC_PRED, VERT_PRED, HOR_PRED, HOR_DOWN_PRED, DC_PRED}
				filterModeToYMode := [5]int{DC_PRED, V_PRED, H_PRED, D157_PRED, DC_PRED}
				yModeForTx = filterModeToYMode[fiMode]
			}
		}
	}


	// --- 10b. palette indices ---
	// dav1d reads palette indices after filter_intra but before tx_size.
	var palIdxY []uint8  // Y palette index grid (w*h)
	var palIdxUV []uint8 // UV palette index grid (cw*ch)
	if paletteSizeY > 0 {
		// Clamp scan dimensions to visible frame (matching dav1d's f->bh/bw).
		scanW4 := bW
		scanH4 := bH
		if miCol+scanW4 > int(td.fh.MiCols) {
			scanW4 = int(td.fh.MiCols) - miCol
		}
		if miRow+scanH4 > int(td.fh.MiRows) {
			scanH4 = int(td.fh.MiRows) - miRow
		}
		var err error
		palIdxY, err = td.readPaletteIndices(bc, 0, paletteSizeY, scanW4, scanH4, bW, bH)
		if err != nil {
			return fmt.Errorf("pal_idx_y at (%d,%d): %w", miRow, miCol, err)
		}
	}
	if paletteSizeUV > 0 && hasChroma {
		outCW4 := (bW + subX) >> subX
		outCH4 := (bH + subY) >> subY
		scanCW4 := outCW4
		scanCH4 := outCH4
		// Clamp scan dimensions to visible frame (chroma coordinates).
		chromaMiColsEnd := (int(td.fh.MiCols) + subX) >> subX
		chromaMiRowsEnd := (int(td.fh.MiRows) + subY) >> subY
		cMiCol := miCol >> subX
		cMiRow := miRow >> subY
		if cMiCol+scanCW4 > chromaMiColsEnd {
			scanCW4 = chromaMiColsEnd - cMiCol
		}
		if cMiRow+scanCH4 > chromaMiRowsEnd {
			scanCH4 = chromaMiRowsEnd - cMiRow
		}
		var err error
		palIdxUV, err = td.readPaletteIndices(bc, 1, paletteSizeUV, scanCW4, scanCH4, outCW4, outCH4)
		if err != nil {
			return fmt.Errorf("pal_idx_uv at (%d,%d): %w", miRow, miCol, err)
		}
	}

	// --- 11. read_tx_size (TX_MODE_SELECT) ---
	// dav1d reads TX size AFTER all mode info (ymode, uvmode, palette, filter_intra).
	// AV1 spec Section 5.11.16: read TX depth for non-skip, non-lossless blocks > 4x4.
	// In lossless mode, TX size is always TX_4X4.
	maxRectTx := blockSizeToTxSize(nomPixW, nomPixH)
	lumaTxSz := maxRectTx
	if td.fh.CodedLossless {
		lumaTxSz = TX_4X4
	}
	if td.fh.TxMode == 2 && !td.fh.CodedLossless && !(bW == 1 && bH == 1) {
		cat, nsyms := txSizeCat(maxRectTx)
		if cat >= 0 && nsyms >= 2 {
			txCtx := td.getTxSizeContext(miRow, miCol, maxRectTx)
			txDepth, err := bc.ReadSymbol(td.cdf.TxSize[cat][txCtx], nsyms)
			if err != nil {
				return fmt.Errorf("tx_size at (%d,%d): %w", miRow, miCol, err)
			}
			lumaTxSz = maxRectTx
			for d := 0; d < txDepth; d++ {
				lumaTxSz = splitTxSize(lumaTxSz)
			}
		}
	}

	// Record palette info, and TX size for neighbor context.
	// NOTE: setModeInfo is deferred until AFTER reconstruction so that
	// neighborIsSm (called during prediction) reads the NEIGHBOR's mode,
	// not the current block's mode. This matches dav1d's ordering.
	palColorsMap := map[int][]uint8{}
	if paletteSizeY > 0 {
		palColorsMap[0] = paletteY
	}
	if paletteSizeUV > 0 {
		palColorsMap[1] = paletteU
		palColorsMap[2] = paletteV
	}
	td.setPaletteInfo(miRow, miCol, bW, bH, paletteSizeY, paletteSizeUV, palColorsMap)
	td.setTxSizeCtx(miRow, miCol, bW, bH, lumaTxSz)

	// --- Coefficient decoding and reconstruction ---
	// Use NOMINAL dimensions for coefficient decoding (AV1 encodes full block
	// even at frame boundaries). Use clamped dimensions for pixel reconstruction.
	nomChromaW := nomPixW >> subX
	nomChromaH := nomPixH >> subY
	if nomChromaW < 4 {
		nomChromaW = 4
	}
	if nomChromaH < 4 {
		nomChromaH = 4
	}
	chromaTxSz := adjustUVTxSize(blockSizeToTxSize(nomChromaW, nomChromaH))
	if td.fh.CodedLossless {
		chromaTxSz = TX_4X4
	}

	chromaMiRow := miRow >> subY
	chromaMiCol := miCol >> subX


	hasNonZero := false // tracks whether any decoded coefficient is non-zero (for CDEF skip)

	if skip {
		// Skip: predict per TX block with no residual, write to frame buffer.
		td.setSkipCoeffCtx(miRow, miCol, bW, bH, hasChroma, subX, subY)
		if paletteSizeY > 0 {
			td.reconstructPalette(miRow, miCol, nomPixW, nomPixH, 0, paletteY, palIdxY, nil)
		} else {
			td.reconstructPlane(miRow, miCol, nomPixW, nomPixH, 0, lumaTxSz, yMode, angleDelta, filterIntraMode, nil, edgeFlags)
		}
		if hasChroma {
			if paletteSizeUV > 0 {
				td.reconstructPalette(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 1, paletteU, palIdxUV, nil)
				td.reconstructPalette(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 2, paletteV, palIdxUV, nil)
			} else if uvMode == 13 {
				td.reconstructCFL(miRow, miCol, chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, chromaTxSz, cflAlphaU, cflAlphaV, nil, nil, subX, subY)
			} else {
				td.reconstructPlane(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 1, chromaTxSz, uvMode, uvAngleDelta, -1, nil, edgeFlags)
				td.reconstructPlane(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 2, chromaTxSz, uvMode, uvAngleDelta, -1, nil, edgeFlags)
			}
		}
	} else {
		// Parse coefficients with Y/UV interleaving in 16x16 MI sub-regions.
		// Matches dav1d read_coef_blocks: for blocks > 64x64 pixels, iteration
		// processes Y then UV within each 64x64 luma pixel sub-region.
		var yCoeffs, uCoeffs, vCoeffs []int32
		yCoeffs, uCoeffs, vCoeffs, err = td.decodeCoeffsInterleaved(bc,
			nomPixW, nomPixH, lumaTxSz, yModeForTx, miRow, miCol,
			nomChromaW, nomChromaH, chromaTxSz, uvMode, chromaMiRow, chromaMiCol,
			hasChroma, subX, subY, false)
		if err != nil {
			yCoeffs = nil
			uCoeffs = nil
			vCoeffs = nil
		}

		hasNonZero = sliceHasNonZero(yCoeffs) || sliceHasNonZero(uCoeffs) || sliceHasNonZero(vCoeffs)

		// Reconstruct: palette prediction or intra prediction + residual.
		if paletteSizeY > 0 {
			td.reconstructPalette(miRow, miCol, nomPixW, nomPixH, 0, paletteY, palIdxY, yCoeffs)
		} else {
			td.reconstructPlane(miRow, miCol, nomPixW, nomPixH, 0, lumaTxSz, yMode, angleDelta, filterIntraMode, yCoeffs, edgeFlags)
		}
		if hasChroma {
			if paletteSizeUV > 0 {
				td.reconstructPalette(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 1, paletteU, palIdxUV, uCoeffs)
				td.reconstructPalette(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 2, paletteV, palIdxUV, vCoeffs)
			} else if uvMode == 13 {
				td.reconstructCFL(miRow, miCol, chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, chromaTxSz, cflAlphaU, cflAlphaV, uCoeffs, vCoeffs, subX, subY)
			} else {
				td.reconstructPlane(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 1, chromaTxSz, uvMode, uvAngleDelta, -1, uCoeffs, edgeFlags)
				td.reconstructPlane(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 2, chromaTxSz, uvMode, uvAngleDelta, -1, vCoeffs, edgeFlags)
			}
		}
	}

	// Record mode info AFTER reconstruction so that neighborIsSm during
	// prediction reads the neighbor's mode, not the current block's.
	td.setModeInfo(miRow, miCol, bW, bH, yMode, uvMode, skip, hasChroma)

	// Populate deblockInfo for in-loop filtering.
	if td.deblockInfo != nil {
		txW, txH := TxSizeDimensions(lumaTxSz)
		if txW == 0 {
			txW = nomPixW
		}
		if txH == 0 {
			txH = nomPixH
		}
		uvTxW, uvTxH := TxSizeDimensions(chromaTxSz)
		if uvTxW == 0 {
			uvTxW = nomChromaW
		}
		if uvTxH == 0 {
			uvTxH = nomChromaH
		}
		info := DeblockInfo{
			IsInter:        false,
			RefFrame:       -1,
			Mode:           uint8(yMode),
			Skip:           skip,
			HasNonZero:     hasNonZero,
			TxW:            txW,
			TxH:            txH,
			UvTxW:          uvTxW,
			UvTxH:          uvTxH,
			DeltaLF:        td.lastDeltaLF,
			CodingBlockCol: miCol,
			CodingBlockRow: miRow,
		}
		for r := miRow; r < miRow+bH && r < len(td.deblockInfo); r++ {
			for c := miCol; c < miCol+bW && c < len(td.deblockInfo[r]); c++ {
				td.deblockInfo[r][c] = info
			}
		}
	}

	return nil
}

// reconstructPlane performs per-TX-block intra prediction and reconstruction
// for a single plane. For each TX block within the coding block:
//  1. Fill reference pixels from the frame buffer (already-reconstructed)
//  2. Predict the TX-sized region
//  3. Add residual (if non-nil)
//  4. Write reconstructed pixels to the frame buffer
//
// This per-TX ordering is critical because later TX blocks use reconstructed
// pixels from earlier TX blocks as prediction references.
// AV1 spec Section 7.11 (Intra Prediction) and Section 7.12 (Reconstruction).
func (td *TileDecoder) reconstructPlane(miRow, miCol, nomW, nomH, plane int, txSz TxSize, mode, angleDelta, filterIntraMode int, residual []int32, edgeFlags uint8) {
	txW, txH := TxSizeDimensions(txSz)
	if txW == 0 || txH == 0 {
		return
	}

	subX := int(td.sh.ColorConfig.SubsamplingX)
	subY := int(td.sh.ColorConfig.SubsamplingY)

	// Determine frame buffer and dimensions for this plane.
	var buf []byte
	var stride, frameW, frameH int
	if plane == 0 {
		buf = td.frame.Y
		stride = td.frame.StrideY
		frameW = td.frame.Width
		frameH = td.frame.Height
	} else {
		frameW = (td.frame.Width + subX) >> subX
		frameH = (td.frame.Height + subY) >> subY
		if plane == 1 {
			buf = td.frame.U
			stride = td.frame.StrideU
		} else {
			buf = td.frame.V
			stride = td.frame.StrideV
		}
	}

	basePixelX := miCol * 4
	basePixelY := miRow * 4

	// Extract plane-appropriate edge flags. Luma uses I444 bits; chroma uses
	// the bits matching the subsampling format (I420/I422/I444).
	// Matches dav1d: luma checks EDGE_I444_*, chroma checks EDGE_I420_* >> (layout-1).
	var edgeTHR, edgeLHB bool
	if plane == 0 {
		edgeTHR = (edgeFlags & EdgeI444TopHasRight) != 0
		edgeLHB = (edgeFlags & EdgeI444LeftHasBottom) != 0
	} else if subX == 1 && subY == 1 { // 4:2:0
		edgeTHR = (edgeFlags & EdgeI420TopHasRight) != 0
		edgeLHB = (edgeFlags & EdgeI420LeftHasBottom) != 0
	} else if subX == 1 { // 4:2:2
		edgeTHR = (edgeFlags & EdgeI422TopHasRight) != 0
		edgeLHB = (edgeFlags & EdgeI422LeftHasBottom) != 0
	} else { // 4:4:4
		edgeTHR = (edgeFlags & EdgeI444TopHasRight) != 0
		edgeLHB = (edgeFlags & EdgeI444LeftHasBottom) != 0
	}

	// Sub-block iteration matching dav1d's 16-MI (64 luma pixel) sub-block processing.
	// For blocks > 64 pixels, dav1d processes each 64-pixel sub-block fully before
	// moving to the next, which affects which pixels are available as references.
	// See dav1d recon_tmpl.c lines 1411-1463 (luma) and 1742-1774 (chroma).
	subSizeX := 64
	subSizeY := 64
	if plane > 0 {
		subSizeX = 64 >> subX
		subSizeY = 64 >> subY
	}

	for initY := 0; initY < nomH; initY += subSizeY {
		subH := nomH
		if initY+subSizeY < subH {
			subH = initY + subSizeY
		}
		for initX := 0; initX < nomW; initX += subSizeX {
			subW := nomW
			if initX+subSizeX < subW {
				subW = initX + subSizeX
			}

			// Sub-block level edge flags (dav1d lines 1441-1444).
			// sb_has_tr: is there a sub-block to the right?
			var sbHasTR bool
			if initX+subSizeX < nomW {
				sbHasTR = true // sub-block to the right
			} else if initY > 0 {
				sbHasTR = false // rightmost column, not first row
			} else {
				sbHasTR = edgeTHR // rightmost column, first row: use block flag
			}
			// sb_has_bl: is there a sub-block below?
			var sbHasBL bool
			if initX > 0 {
				sbHasBL = false // not leftmost column
			} else if initY+subSizeY < nomH {
				sbHasBL = true // sub-block below exists
			} else {
				sbHasBL = edgeLHB // bottommost row, leftmost column: use block flag
			}

			for tyOff := initY; tyOff < subH; tyOff += txH {
				for txOff := initX; txOff < subW; txOff += txW {
			pxX := basePixelX + txOff
			pxY := basePixelY + tyOff

			// Clamp to buffer boundary (not visible frame boundary).
			// Allow writes past the visible frame into the extended
			// buffer so CDEF can read reconstructed pixels there.
			bufH := len(buf) / stride
			bufW := stride
			curW := txW
			curH := txH
			if pxX+curW > bufW {
				curW = bufW - pxX
			}
			if pxY+curH > bufH {
				curH = bufH - pxY
			}
			if curW <= 0 || curH <= 0 {
				continue
			}

			// Fill reference pixels from already-reconstructed frame buffer.
			// Directional modes need extended references (top-right/bottom-left).
			isDirectional := (mode >= V_PRED && mode <= D67_PRED)
			aboveLen := txW + 1
			leftLen := txH + 1
			if isDirectional {
				aboveLen = 2*txW + 1 // top-right extension
				leftLen = 2*txH + 1  // bottom-left extension
			}
			above := make([]byte, aboveLen)
			left := make([]byte, leftLen)
			// Per-TU edge flag computation matching dav1d lines 1459-1463.
			// has_top_right: TU not at right edge of sub-block, or at first row with sb_has_tr.
			hasTopRight := (txOff + txW) < subW
			if !hasTopRight {
				hasTopRight = tyOff == initY && sbHasTR
			}
			// has_bottom_left: only possible when TU is at left edge of sub-block.
			// If txOff > initX, the left column is inside the block and pixels below
			// haven't been decoded yet (raster order within sub-block).
			var hasBottomLeft bool
			if txOff > initX {
				hasBottomLeft = false
			} else if (tyOff + txH) < subH {
				hasBottomLeft = true
			} else {
				hasBottomLeft = sbHasBL
			}
			if plane == 0 {
				td.fillIntraRef(pxX, pxY, txW, txH, above, left, isDirectional, hasTopRight, hasBottomLeft)
			} else {
				td.fillIntraRefChroma(pxX, pxY, txW, txH, above, left, buf, stride, frameW, frameH, isDirectional, hasTopRight, hasBottomLeft)
			}
			// Use tile boundaries for availability, matching dav1d.
			var hasAbove, hasLeft bool
			if plane == 0 {
				hasAbove = pxY > td.tileRowStart*4
				hasLeft = pxX > td.tileColStart*4
			} else {
				subXv := int(td.sh.ColorConfig.SubsamplingX)
				subYv := int(td.sh.ColorConfig.SubsamplingY)
				hasAbove = pxY > (td.tileRowStart*4)>>subYv
				hasLeft = pxX > (td.tileColStart*4)>>subXv
			}

			// Determine edge filter flags for directional prediction.
			enableEdgeFilter := td.sh.EnableIntraEdgeFilter && isDirectional
			isSm := false
			if enableEdgeFilter {
				// Check if above/left neighbor uses smooth mode.
				isSm = td.neighborIsSm(miRow, miCol, plane)
			}
			// Predict using TX block size.
			pred := make([]byte, txW*txH)
			if filterIntraMode >= 0 && plane == 0 {
				PredictFilterIntra(pred, txW, txW, txH, above, left, filterIntraMode)
			} else if isDirectional {
				PredictIntraWithDelta(mode, angleDelta, pred, txW, txW, txH, above, left, 8, enableEdgeFilter, isSm)
			} else {
				PredictIntra(mode, pred, txW, txW, txH, above, left, hasAbove, hasLeft, 8)
			}
			// Add residual and write to frame buffer.
			for r := 0; r < curH; r++ {
				for c := 0; c < curW; c++ {
					val := int(pred[r*txW+c])
					if residual != nil {
						idx := (tyOff+r)*nomW + (txOff + c)
						if idx < len(residual) {
							val += int(residual[idx])
						}
					}
					if val < 0 {
						val = 0
					}
					if val > 255 {
						val = 255
					}
					buf[(pxY+r)*stride+pxX+c] = byte(val)
				}
			}
		}
	}
		}
	}
}

// reconstructCFL implements Chroma-from-Luma prediction for both chroma planes.
// It reads the already-reconstructed luma pixels from the frame buffer,
// downsamples them to chroma resolution, subtracts the mean (AC signal),
// and predicts chroma as DC + round(alpha * AC / 64).
// Matches dav1d cfl_ac_c + cfl_pred (ipred_tmpl.c).
func (td *TileDecoder) reconstructCFL(lumaMiRow, lumaMiCol, chromaMiRow, chromaMiCol, chromaW, chromaH int, chromaTxSz TxSize, alphaU, alphaV int, uResidual, vResidual []int32, subX, subY int) {
	frameW := (td.frame.Width + subX) >> subX
	frameH := (td.frame.Height + subY) >> subY

	// Compute AC luma signal at chroma resolution.
	// Matches dav1d cfl_ac_c: downsample luma, then subtract mean.
	// Use chromaMiCol/chromaMiRow to derive luma origin, since for 4:2:0
	// the chroma covers an area starting from the even-aligned MI position.
	//
	// dav1d cfl_ac_c reads luma pixels directly from the frame buffer
	// without clamping at the frame boundary. When the block extends
	// past the visible frame, it reads reconstructed data from the
	// buffer padding area (allocated with extra rows/cols for this
	// purpose). We replicate this by reading directly from the buffer,
	// clamping only at the buffer boundary (not the visible frame).
	lumaPixX := chromaMiCol * (4 << subX)
	lumaPixY := chromaMiRow * (4 << subY)
	lumaBufH := len(td.frame.Y) / td.frame.StrideY
	lumaBufW := td.frame.StrideY
	ac := make([]int16, chromaW*chromaH)
	for cy := 0; cy < chromaH; cy++ {
		for cx := 0; cx < chromaW; cx++ {
			ly := lumaPixY + (cy << subY)
			lx := lumaPixX + (cx << subX)
			// Clamp to buffer boundary (not visible frame boundary).
			// dav1d reads from the frame buffer which extends past
			// the visible frame; reconstructed pixels are valid there.
			if ly >= lumaBufH {
				ly = lumaBufH - 1
			}
			if lx >= lumaBufW {
				lx = lumaBufW - 1
			}
			sum := int(td.frame.Y[ly*td.frame.StrideY+lx])
			if subX == 1 {
				lx1 := lx + 1
				if lx1 >= lumaBufW {
					lx1 = lumaBufW - 1
				}
				sum += int(td.frame.Y[ly*td.frame.StrideY+lx1])
			}
			if subY == 1 {
				ly1 := ly + 1
				if ly1 >= lumaBufH {
					ly1 = lumaBufH - 1
				}
				sum += int(td.frame.Y[ly1*td.frame.StrideY+lx])
				if subX == 1 {
					lx1 := lx + 1
					if lx1 >= lumaBufW {
						lx1 = lumaBufW - 1
					}
					sum += int(td.frame.Y[ly1*td.frame.StrideY+lx1])
				}
			}
			// dav1d: ac[x] = ac_sum << (1 + !ss_ver + !ss_hor)
			shift := 1
			if subY == 0 {
				shift++
			}
			if subX == 0 {
				shift++
			}
			ac[cy*chromaW+cx] = int16(sum << shift)
		}
	}

	// Subtract mean (DC component) from AC signal.
	log2sz := 0
	for v := chromaW; v > 1; v >>= 1 {
		log2sz++
	}
	for v := chromaH; v > 1; v >>= 1 {
		log2sz++
	}
	avg := (1 << log2sz) >> 1
	for i := range ac {
		avg += int(ac[i])
	}
	avg >>= log2sz
	for i := range ac {
		ac[i] -= int16(avg)
	}
	// Reconstruct each chroma plane using CFL prediction.
	txW, txH := TxSizeDimensions(chromaTxSz)
	if txW == 0 || txH == 0 {
		return
	}

	// Reconstruct each chroma plane using CFL prediction.
	for pl := 1; pl <= 2; pl++ {
		alpha := alphaU
		residual := uResidual
		var buf []byte
		var stride int
		if pl == 1 {
			buf = td.frame.U
			stride = td.frame.StrideU
		} else {
			alpha = alphaV
			residual = vResidual
			buf = td.frame.V
			stride = td.frame.StrideV
		}

		if alpha == 0 {
			// No CFL for this plane — use plain DC prediction.
			td.reconstructPlane(chromaMiRow, chromaMiCol, chromaW, chromaH, pl, chromaTxSz, DC_PRED, 0, -1, residual, 0)
			continue
		}

		basePixelX := chromaMiCol * 4
		basePixelY := chromaMiRow * 4

		for tyOff := 0; tyOff < chromaH; tyOff += txH {
			for txOff := 0; txOff < chromaW; txOff += txW {
				pxX := basePixelX + txOff
				pxY := basePixelY + tyOff

				curW := txW
				curH := txH
				bufH := len(buf) / stride
				bufW := stride
				if pxX+curW > bufW {
					curW = bufW - pxX
				}
				if pxY+curH > bufH {
					curH = bufH - pxY
				}
				if curW <= 0 || curH <= 0 {
					continue
				}

				// Fill reference pixels and compute DC prediction.
				above := make([]byte, txW+1)
				left := make([]byte, txH+1)
				td.fillIntraRefChroma(pxX, pxY, txW, txH, above, left, buf, stride, frameW, frameH, false, false, false)
				// Use tile boundaries for availability, matching dav1d.
				hasAbove := pxY > (td.tileRowStart*4)>>subY
				hasLeft := pxX > (td.tileColStart*4)>>subX

				// DC prediction as the base.
				pred := make([]byte, txW*txH)
				PredictIntra(DC_PRED, pred, txW, txW, txH, above, left, hasAbove, hasLeft, 8)

				// Apply CFL: dst = clip(dc + apply_sign((abs(alpha*ac)+32)>>6, alpha*ac))
				// dav1d clips prediction to [0,255] BEFORE adding residual.
				for r := 0; r < curH; r++ {
					for c := 0; c < curW; c++ {
						dc := int(pred[r*txW+c])
						acVal := int(ac[(tyOff+r)*chromaW+(txOff+c)])
						diff := alpha * acVal
						// dav1d: apply_sign((abs(diff) + 32) >> 6, diff)
						adj := diff
						if adj < 0 {
							adj = -adj
						}
						adj = (adj + 32) >> 6
						if diff < 0 {
							adj = -adj
						}
						val := dc + adj
						// Clip prediction before adding residual (matches dav1d iclip_pixel).
						if val < 0 {
							val = 0
						}
						if val > 255 {
							val = 255
						}
						if residual != nil {
							idx := (tyOff+r)*chromaW + (txOff + c)
							if idx < len(residual) {
								val += int(residual[idx])
							}
						}
						// Clip final value after residual.
						if val < 0 {
							val = 0
						}
						if val > 255 {
							val = 255
						}
						buf[(pxY+r)*stride+pxX+c] = byte(val)
					}
				}
			}
		}
	}
}

// reconstructPalette writes palette-predicted pixels to the frame buffer.
// For each pixel, the prediction is palette[indexGrid[y*w+x]].
// If residual is non-nil, it is added to the prediction.
func (td *TileDecoder) reconstructPalette(miRow, miCol, nomW, nomH, plane int, palette []uint8, indexGrid []uint8, residual []int32) {
	var buf []byte
	var stride int
	if plane == 0 {
		buf = td.frame.Y
		stride = td.frame.StrideY
	} else {
		if plane == 1 {
			buf = td.frame.U
			stride = td.frame.StrideU
		} else {
			buf = td.frame.V
			stride = td.frame.StrideV
		}
	}

	basePixelX := miCol * 4
	basePixelY := miRow * 4

	// Clamp to buffer boundary (allow writes past visible frame).
	bufH := len(buf) / stride
	bufW := stride
	w := nomW
	h := nomH
	if basePixelX+w > bufW {
		w = bufW - basePixelX
	}
	if basePixelY+h > bufH {
		h = bufH - basePixelY
	}
	if w <= 0 || h <= 0 {
		return
	}

	idxStride := nomW // index grid stride is the nominal (unclamped) width

	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			idx := indexGrid[r*idxStride+c]
			val := int(palette[idx])
			if residual != nil {
				ri := r*nomW + c
				if ri < len(residual) {
					val += int(residual[ri])
				}
			}
			if val < 0 {
				val = 0
			}
			if val > 255 {
				val = 255
			}
			buf[(basePixelY+r)*stride+basePixelX+c] = byte(val)
		}
	}
}

// fillIntraRef fills the above and left reference buffers for intra prediction.
// Uses reconstructed pixels from the frame buffer where available, falling
// back to 128 (mid-gray for 8-bit) at frame boundaries.
// AV1 spec Section 7.11.2.1.
func (td *TileDecoder) fillIntraRef(pixelX, pixelY, w, h int, above, left []byte, extended bool, hasTopRight, hasBottomLeft bool) {
	// Use tile boundaries, not frame boundaries, for reference availability.
	// Matches dav1d: have_top = t->by > ts->tiling.row_start,
	//               have_left = t->bx > ts->tiling.col_start.
	haveTop := pixelY > td.tileRowStart*4
	haveLeft := pixelX > td.tileColStart*4
	stride := td.frame.StrideY

	// Tile pixel boundaries. dav1d uses ts->tiling.col_end / row_end
	// to clamp reference pixel reads. Pixels beyond the tile boundary
	// have not been decoded yet (tiles are independent) and must not
	// be read. AV1 spec Section 7.11.2.
	tilePixRight := td.tileColEnd * 4
	tilePixBottom := td.tileRowEnd * 4

	// Top-left pixel.
	// Matches dav1d ipred_prepare_tmpl.c lines 192-196.
	if haveLeft {
		if haveTop {
			above[0] = td.frame.Y[(pixelY-1)*stride+pixelX-1]
		} else {
			above[0] = td.frame.Y[pixelY*stride+pixelX-1]
		}
	} else {
		if haveTop {
			above[0] = td.frame.Y[(pixelY-1)*stride+pixelX]
		} else {
			above[0] = 128
		}
	}
	left[0] = above[0]

	// Above pixels (top row).
	// Matches dav1d ipred_prepare_tmpl.c lines 162-173.
	// Clamp available pixels to tile/frame boundary.
	if haveTop {
		pxHave := w
		if tileAvail := tilePixRight - pixelX; tileAvail < pxHave {
			pxHave = tileAvail
		}
		if pxHave < 0 {
			pxHave = 0
		}
		for x := 0; x < pxHave; x++ {
			above[x+1] = td.frame.Y[(pixelY-1)*stride+pixelX+x]
		}
		if pxHave < w {
			fillVal := above[pxHave]
			for x := pxHave; x < w; x++ {
				above[x+1] = fillVal
			}
		}
	} else {
		var fillVal byte
		if haveLeft {
			fillVal = td.frame.Y[pixelY*stride+pixelX-1]
		} else {
			fillVal = 127
		}
		for x := 0; x < w; x++ {
			above[x+1] = fillVal
		}
	}

	// Left pixels (left column).
	// Matches dav1d ipred_prepare_tmpl.c lines 130-143.
	// Clamp available pixels to tile/frame boundary.
	if haveLeft {
		pxHave := h
		if tileAvail := tilePixBottom - pixelY; tileAvail < pxHave {
			pxHave = tileAvail
		}
		if pxHave < 0 {
			pxHave = 0
		}
		for y := 0; y < pxHave; y++ {
			left[y+1] = td.frame.Y[(pixelY+y)*stride+pixelX-1]
		}
		if pxHave < h {
			fillVal := left[pxHave]
			for y := pxHave; y < h; y++ {
				left[y+1] = fillVal
			}
		}
	} else {
		var fillVal byte
		if haveTop {
			fillVal = td.frame.Y[(pixelY-1)*stride+pixelX]
		} else {
			fillVal = 129
		}
		for y := 0; y < h; y++ {
			left[y+1] = fillVal
		}
	}

	// Extended references for directional prediction (top-right and bottom-left).
	// Matches dav1d ipred_prepare_tmpl.c lines 175-189 (top-right) and 145-159 (bottom-left).
	if extended {
		// Top-right: unavailable when TX right edge reaches the tile boundary.
		// dav1d: have_topright = (!have_top || x + tw >= w) ? 0 : edge_flags
		// where w = ts->tiling.col_end.
		if pixelX+w >= tilePixRight {
			hasTopRight = false
		}

		// Top-right extension.
		if haveTop && hasTopRight {
			pxHave := w
			if tileAvail := tilePixRight - pixelX - w; tileAvail < pxHave {
				pxHave = tileAvail
			}
			if pxHave < 0 {
				pxHave = 0
			}
			lastAbove := above[w]
			for x := 0; x < pxHave; x++ {
				above[w+x+1] = td.frame.Y[(pixelY-1)*stride+pixelX+w+x]
				lastAbove = above[w+x+1]
			}
			if pxHave < w {
				fillVal := lastAbove
				for x := pxHave; x < w; x++ {
					above[w+x+1] = fillVal
				}
			}
		} else {
			// No top-right available: extend with last above pixel.
			fillVal := above[w]
			for x := w; x < 2*w; x++ {
				above[x+1] = fillVal
			}
		}

		// Bottom-left: unavailable when TX bottom edge reaches the tile boundary.
		// dav1d: have_bottomleft = (!have_left || y + th >= h) ? 0 : edge_flags
		// where h = ts->tiling.row_end.
		if pixelY+h >= tilePixBottom {
			hasBottomLeft = false
		}

		// Bottom-left extension.
		if haveLeft && hasBottomLeft {
			pxHave := h
			if tileAvail := tilePixBottom - pixelY - h; tileAvail < pxHave {
				pxHave = tileAvail
			}
			if pxHave < 0 {
				pxHave = 0
			}
			lastLeft := left[h]
			for y := 0; y < pxHave; y++ {
				left[h+y+1] = td.frame.Y[(pixelY+h+y)*stride+pixelX-1]
				lastLeft = left[h+y+1]
			}
			if pxHave < h {
				fillVal := lastLeft
				for y := pxHave; y < h; y++ {
					left[h+y+1] = fillVal
				}
			}
		} else {
			// No bottom-left available: extend with last left pixel.
			fillVal := left[h]
			for y := h; y < 2*h; y++ {
				left[y+1] = fillVal
			}
		}
	}
}

// fillIntraRefChroma fills reference buffers for chroma intra prediction.
// Same logic as fillIntraRef but operates on the given chroma plane buffer.
func (td *TileDecoder) fillIntraRefChroma(pixelX, pixelY, w, h int, above, left []byte, buf []byte, stride, frameW, frameH int, extended bool, hasTopRight, hasBottomLeft bool) {
	// Use tile boundaries for reference availability (not frame boundaries).
	// Matches dav1d: checks against tile row/col start.
	subX := int(td.sh.ColorConfig.SubsamplingX)
	subY := int(td.sh.ColorConfig.SubsamplingY)
	haveTop := pixelY > (td.tileRowStart*4)>>subY
	haveLeft := pixelX > (td.tileColStart*4)>>subX

	// Tile pixel boundaries in chroma coordinates.
	tilePixRight := (td.tileColEnd * 4) >> subX
	tilePixBottom := (td.tileRowEnd * 4) >> subY

	// Top-left pixel. Matches dav1d ipred_prepare_tmpl.c lines 192-196.
	if haveLeft {
		if haveTop {
			above[0] = buf[(pixelY-1)*stride+pixelX-1]
		} else {
			above[0] = buf[pixelY*stride+pixelX-1]
		}
	} else {
		if haveTop {
			above[0] = buf[(pixelY-1)*stride+pixelX]
		} else {
			above[0] = 128
		}
	}
	left[0] = above[0]

	// Above pixels. Clamp to tile/frame boundary.
	if haveTop {
		pxHave := w
		if tileAvail := tilePixRight - pixelX; tileAvail < pxHave {
			pxHave = tileAvail
		}
		if pxHave < 0 {
			pxHave = 0
		}
		for x := 0; x < pxHave; x++ {
			above[x+1] = buf[(pixelY-1)*stride+pixelX+x]
		}
		if pxHave < w {
			fillVal := above[pxHave]
			for x := pxHave; x < w; x++ {
				above[x+1] = fillVal
			}
		}
	} else {
		var fillVal byte
		if haveLeft {
			fillVal = buf[pixelY*stride+pixelX-1]
		} else {
			fillVal = 127
		}
		for x := 0; x < w; x++ {
			above[x+1] = fillVal
		}
	}

	// Left pixels. Clamp to tile/frame boundary.
	if haveLeft {
		pxHave := h
		if tileAvail := tilePixBottom - pixelY; tileAvail < pxHave {
			pxHave = tileAvail
		}
		if pxHave < 0 {
			pxHave = 0
		}
		for y := 0; y < pxHave; y++ {
			left[y+1] = buf[(pixelY+y)*stride+pixelX-1]
		}
		if pxHave < h {
			fillVal := left[pxHave]
			for y := pxHave; y < h; y++ {
				left[y+1] = fillVal
			}
		}
	} else {
		var fillVal byte
		if haveTop {
			fillVal = buf[(pixelY-1)*stride+pixelX]
		} else {
			fillVal = 129
		}
		for y := 0; y < h; y++ {
			left[y+1] = fillVal
		}
	}

	// Extended references for directional prediction.
	if extended {
		// Clamp top-right at tile column boundary.
		if pixelX+w >= tilePixRight {
			hasTopRight = false
		}

		if haveTop && hasTopRight {
			pxHave := w
			if tileAvail := tilePixRight - pixelX - w; tileAvail < pxHave {
				pxHave = tileAvail
			}
			if pxHave < 0 {
				pxHave = 0
			}
			lastAbove := above[w]
			for x := 0; x < pxHave; x++ {
				above[w+x+1] = buf[(pixelY-1)*stride+pixelX+w+x]
				lastAbove = above[w+x+1]
			}
			if pxHave < w {
				fillVal := lastAbove
				for x := pxHave; x < w; x++ {
					above[w+x+1] = fillVal
				}
			}
		} else {
			fillVal := above[w]
			for x := w; x < 2*w; x++ {
				above[x+1] = fillVal
			}
		}

		// Clamp bottom-left at tile row boundary.
		if pixelY+h >= tilePixBottom {
			hasBottomLeft = false
		}

		if haveLeft && hasBottomLeft {
			pxHave := h
			if tileAvail := tilePixBottom - pixelY - h; tileAvail < pxHave {
				pxHave = tileAvail
			}
			if pxHave < 0 {
				pxHave = 0
			}
			lastLeft := left[h]
			for y := 0; y < pxHave; y++ {
				left[h+y+1] = buf[(pixelY+h+y)*stride+pixelX-1]
				lastLeft = left[h+y+1]
			}
			if pxHave < h {
				fillVal := lastLeft
				for y := pxHave; y < h; y++ {
					left[h+y+1] = fillVal
				}
			}
		} else {
			fillVal := left[h]
			for y := h; y < 2*h; y++ {
				left[y+1] = fillVal
			}
		}
	}
}

// blockSizeToMaxTxSize returns the largest transform size that fits within
// a block of the given pixel dimensions.
// AV1 spec Section 5.11.15 / Table 6-2: block sizes map to max TX sizes.
// Note: AV1 caps maximum TX at 32x32 (1024 coefficients).
func blockSizeToMaxTxSize(pixelW, pixelH int) TxSize {
	// Determine the largest square TX that fits, capped at TX_32X32.
	minDim := pixelW
	if pixelH < minDim {
		minDim = pixelH
	}

	switch {
	case minDim >= 32:
		return TX_32X32 // AV1: max usable TX is 32x32
	case minDim >= 16:
		return TX_16X16
	case minDim >= 8:
		return TX_8X8
	default:
		return TX_4X4
	}
}

// txSizeCat returns the TxSize CDF category for a given max TX size.
// AV1 spec Section 5.11.16: category = tx_size_sqr_up[maxTx] - TX_8X8.
// Returns (cat, nsyms). cat=-1 means no TX size read needed.
func txSizeCat(maxTx TxSize) (int, int) {
	// Compute square-up: the smallest square TX >= maxTx.
	w, h := TxSizeDimensions(maxTx)
	maxDim := w
	if h > maxDim {
		maxDim = h
	}
	switch {
	case maxDim >= 64:
		return 3, 3 // cat=3, 3 symbols (depth 0,1,2)
	case maxDim >= 32:
		return 2, 3 // cat=2, 3 symbols
	case maxDim >= 16:
		return 1, 3 // cat=1, 3 symbols
	case maxDim >= 8:
		return 0, 2 // cat=0, 2 symbols (depth 0,1)
	default:
		return -1, 0 // TX_4X4: no read needed
	}
}

// splitTxSize reduces a TX size by one level (halves each dimension).
// AV1 spec Table: split_tx_size[txSz].
func splitTxSize(txSz TxSize) TxSize {
	// Matches dav1d's dav1d_txfm_dimensions[].sub field exactly.
	switch txSz {
	case TX_64X64:
		return TX_32X32
	case TX_64X32:
		return TX_32X32
	case TX_32X64:
		return TX_32X32
	case TX_64X16:
		return TX_32X16 // 4:1 → 2:1
	case TX_16X64:
		return TX_16X32 // 1:4 → 1:2
	case TX_32X32:
		return TX_16X16
	case TX_32X16:
		return TX_16X16
	case TX_16X32:
		return TX_16X16
	case TX_32X8:
		return TX_16X8 // 4:1 → 2:1
	case TX_8X32:
		return TX_8X16 // 1:4 → 1:2
	case TX_16X16:
		return TX_8X8
	case TX_16X8:
		return TX_8X8
	case TX_8X16:
		return TX_8X8
	case TX_16X4:
		return TX_8X4 // 4:1 → 2:1
	case TX_4X16:
		return TX_4X8 // 1:4 → 1:2
	case TX_8X8:
		return TX_4X4
	case TX_8X4:
		return TX_4X4
	case TX_4X8:
		return TX_4X4
	default:
		return TX_4X4
	}
}

// blockSizeToTxSize returns a TX size that fits the given block dimensions,
// including rectangular transforms when appropriate.
// AV1 spec Section 5.11.16: maps block size to transform size.
func blockSizeToTxSize(pixelW, pixelH int) TxSize {
	switch {
	case pixelW == 4 && pixelH == 4:
		return TX_4X4
	case pixelW == 8 && pixelH == 8:
		return TX_8X8
	case pixelW == 16 && pixelH == 16:
		return TX_16X16
	case pixelW == 32 && pixelH == 32:
		return TX_32X32
	case pixelW >= 64 && pixelH >= 64:
		return TX_64X64
	case pixelW == 4 && pixelH == 8:
		return TX_4X8
	case pixelW == 8 && pixelH == 4:
		return TX_8X4
	case pixelW == 8 && pixelH == 16:
		return TX_8X16
	case pixelW == 16 && pixelH == 8:
		return TX_16X8
	case pixelW == 16 && pixelH == 32:
		return TX_16X32
	case pixelW == 32 && pixelH == 16:
		return TX_32X16
	case pixelW == 32 && pixelH == 64:
		return TX_32X64
	case pixelW == 64 && pixelH == 32:
		return TX_64X32
	case pixelW == 4 && pixelH == 16:
		return TX_4X16
	case pixelW == 16 && pixelH == 4:
		return TX_16X4
	case pixelW == 8 && pixelH == 32:
		return TX_8X32
	case pixelW == 32 && pixelH == 8:
		return TX_32X8
	case pixelW == 16 && pixelH == 64:
		return TX_16X64
	case pixelW == 64 && pixelH == 16:
		return TX_64X16
	default:
		// Fall back to largest fitting square TX.
		return blockSizeToMaxTxSize(pixelW, pixelH)
	}
}

// adjustUVTxSize caps UV transform sizes per AV1 spec.
// Matches libaom's av1_get_adjusted_tx_size: UV planes cannot use
// transforms wider or taller than 32 pixels.
func adjustUVTxSize(txSz TxSize) TxSize {
	switch txSz {
	case TX_64X64, TX_64X32, TX_32X64:
		return TX_32X32
	case TX_64X16:
		return TX_32X16
	case TX_16X64:
		return TX_16X32
	default:
		return txSz
	}
}

// txSizeCtx returns the txs_ctx for coefficient CDF lookup.
// Matches dav1d's t_dim->ctx = (lw + lh + 1) >> 1.
func txSizeCtx(txSz TxSize) int {
	w, h := TxSizeDimensions(txSz)
	if w == 0 || h == 0 {
		return 0
	}
	lw := floorLog2(w) - 2
	lh := floorLog2(h) - 2
	if lw < 0 {
		lw = 0
	}
	if lh < 0 {
		lh = 0
	}
	return (lw + lh + 1) >> 1
}

// eobMultiSize returns the EOB multi-symbol size class index for the given
// number of coefficients. This selects which EobPt CDF to use.
// AV1 spec Section 5.11.36: eob_multi_size mapping.
//
// dav1d computes tx2dszctx = imin(lw, TX_32X32) + imin(lh, TX_32X32) and
// passes n_symbols = 4 + tx2dszctx to msac_decode_symbol_adapt. In dav1d,
// n_symbols is the number of CDF boundary values (= our nsyms - 1). So
// our nsyms = 5 + tx2dszctx. The mapping by numCoeffs gives the same result.
func eobMultiSize(numCoeffs int) int {
	switch {
	case numCoeffs <= 16:
		return 0 // EobPt16: 5 symbols (dav1d n_symbols=4)
	case numCoeffs <= 32:
		return 1 // EobPt32: 6 symbols (dav1d n_symbols=5)
	case numCoeffs <= 64:
		return 2 // EobPt64: 7 symbols (dav1d n_symbols=6)
	case numCoeffs <= 128:
		return 3 // EobPt128: 8 symbols (dav1d n_symbols=7)
	case numCoeffs <= 256:
		return 4 // EobPt256: 9 symbols (dav1d n_symbols=8)
	case numCoeffs <= 512:
		return 5 // EobPt512: 10 symbols (dav1d n_symbols=9)
	default:
		return 6 // EobPt1024: 11 symbols (dav1d n_symbols=10)
	}
}

// readEobPt reads the EOB position class symbol from the appropriate CDF.
// AV1 spec Section 5.11.36. eobCdfIdx = min(txSzCtx, 1).
func (td *TileDecoder) readEobPt(bc *BoolReader, eobMultiSz, planeType, eobCdfIdx int) (int, error) {
	switch eobMultiSz {
	case 0:
		return bc.ReadSymbol(td.cdf.EobPt16[planeType][eobCdfIdx], 5)
	case 1:
		return bc.ReadSymbol(td.cdf.EobPt32[planeType][eobCdfIdx], 6)
	case 2:
		return bc.ReadSymbol(td.cdf.EobPt64[planeType][eobCdfIdx], 7)
	case 3:
		return bc.ReadSymbol(td.cdf.EobPt128[planeType][eobCdfIdx], 8)
	case 4:
		return bc.ReadSymbol(td.cdf.EobPt256[planeType][eobCdfIdx], 9)
	case 5:
		return bc.ReadSymbol(td.cdf.EobPt512[planeType][eobCdfIdx], 10)
	default:
		return bc.ReadSymbol(td.cdf.EobPt1024[planeType][eobCdfIdx], 11)
	}
}

// eobPtToEob returns the EOB offset and number of extra bits for a given
// EOB position class. eob = offset + eob_extra (no +1 needed).
// AV1 spec Section 5.11.36, Table 9-38.
func eobPtToEob(eobPt int) (int, int) {
	// AV1 spec Table 9-38:
	// eob_pt:  0   1   2   3   4   5   6    7    8     9     10
	// offset:  1   2   3   5   9  17  33   65  129   257   513
	// k:       0   0   1   2   3   4   5    6    7     8     9
	switch eobPt {
	case 0:
		return 1, 0
	case 1:
		return 2, 0
	case 2:
		return 3, 1
	case 3:
		return 5, 2
	case 4:
		return 9, 3
	case 5:
		return 17, 4
	case 6:
		return 33, 5
	case 7:
		return 65, 6
	case 8:
		return 129, 7
	case 9:
		return 257, 8
	default:
		return 513, 9
	}
}

// TxClass identifies the transform class for scan order and context.
type TxClass int

const (
	TX_CLASS_2D TxClass = iota // diagonal scan
	TX_CLASS_H                 // horizontal — column-first scan
	TX_CLASS_V                 // vertical — row-first (linear) scan
)

// txClassOf returns the TX class for a given TX type.
func txClassOf(t TxType) TxClass {
	switch t {
	case V_DCT, V_ADST, V_FLIPADST:
		return TX_CLASS_V
	case H_DCT, H_ADST, H_FLIPADST:
		return TX_CLASS_H
	default:
		return TX_CLASS_2D
	}
}

// generateScanOrder generates the scan order for a transform block.
// Returns positions in ROW-MAJOR layout (pos = row*txW + col).
//
// TX_CLASS_2D: uses dav1d zig-zag scan tables (column-major encoded),
//   converted to row-major. dav1d encodes: rc = col*coeffH + row.
// TX_CLASS_H (AV1 spec): pos = c (linear / row-major).
// TX_CLASS_V (AV1 spec): pos = (c % H) * W + (c / H) (column-major).
func generateScanOrder(txW, txH int, txClass TxClass, txSz TxSize) []int {
	coeffW := txW
	if coeffW > 32 {
		coeffW = 32
	}
	coeffH := txH
	if coeffH > 32 {
		coeffH = 32
	}
	total := coeffW * coeffH
	if total > 1024 {
		total = 1024
	}

	scan := make([]int, total)
	switch txClass {
	case TX_CLASS_2D:
		// dav1d scan tables encode positions as col*coeffH + row (column-major).
		// Convert to row-major (row*coeffW + col) for our levels array.
		scanTbl := getScan2D(txSz)
		if scanTbl != nil && len(scanTbl) >= total {
			for i := 0; i < total; i++ {
				rc := int(scanTbl[i])
				col := rc / coeffH
				row := rc % coeffH
				scan[i] = row*coeffW + col
			}
		} else {
			for i := 0; i < total; i++ {
				scan[i] = i
			}
		}
	case TX_CLASS_H:
		// AV1 spec: pos = (c / W) * W + (c % W) = c.
		// Linear / row-major order (row by row).
		for i := 0; i < total; i++ {
			scan[i] = i
		}
	case TX_CLASS_V:
		// dav1d: scan index i decomposed as (col = i%txW, row = i/txW),
		// stored in column-major cf[col*txH + row]. Our buffer is row-major,
		// so scan[i] = i maps to (row = i/txW, col = i%txW) — same spatial
		// position, just row-major layout. Linear scan is correct.
		for i := 0; i < total; i++ {
			scan[i] = i
		}
	}
	return scan
}

// getCoeffBaseCtx computes the context for CoeffBase CDFs.
// loCtxOffsets matches dav1d_lo_ctx_offsets[3][5][5] from dav1d/src/tables.c.
// Indexed by [shape][min(row,4)][min(col,4)] where shape is:
//   0 = square (w == h), 1 = wide (w > h), 2 = tall (w < h)

var loCtxOffsets = [3][5][5]int{
	{ // square (w == h)
		{0, 1, 6, 6, 21},
		{1, 6, 6, 21, 21},
		{6, 6, 21, 21, 21},
		{6, 21, 21, 21, 21},
		{21, 21, 21, 21, 21},
	},
	{ // wide (w > h)
		{0, 16, 6, 6, 21},
		{16, 16, 6, 21, 21},
		{16, 16, 21, 21, 21},
		{16, 16, 21, 21, 21},
		{16, 16, 21, 21, 21},
	},
	{ // tall (w < h)
		{0, 11, 11, 11, 11},
		{11, 11, 11, 11, 11},
		{6, 6, 21, 21, 21},
		{6, 21, 21, 21, 21},
		{21, 21, 21, 21, 21},
	},
}

// getCoeffBaseCtx returns the coefficient base context.
// For TX_CLASS_2D: uses loCtxOffsets table.
// For TX_CLASS_H/V: uses offset formula 26 + (freq > 1 ? 10 : freq * 5).
func getCoeffBaseCtx(levels []int32, pos, txW, txH, origTxW, origTxH int, txClass TxClass) int {
	row := pos / txW
	col := pos % txW

	mag := int32(0)

	clamp3 := func(v int32) int32 {
		if v < 0 {
			v = -v
		}
		if v > 3 {
			return 3
		}
		return v
	}

	switch txClass {
	case TX_CLASS_2D:
		// 5 neighbors: right, below, below-right, right+2, below+2.
		if col+1 < txW {
			mag += clamp3(levels[pos+1])
		}
		if row+1 < txH {
			mag += clamp3(levels[pos+txW])
		}
		if col+1 < txW && row+1 < txH {
			mag += clamp3(levels[pos+txW+1])
		}
		if col+2 < txW {
			mag += clamp3(levels[pos+2])
		}
		if row+2 < txH {
			mag += clamp3(levels[pos+2*txW])
		}
	case TX_CLASS_H:
		// H class: x = pos % txH (cross), y = pos / txH (frequency).
		// dav1d: 1 cross neighbor (x+1) + 4 frequency neighbors (y+1..y+4).
		hx := pos % txH
		hy := pos / txH
		hGroups := len(levels) / txH
		if hx+1 < txH {
			mag += clamp3(levels[pos+1]) // cross+1
		}
		if hy+1 < hGroups {
			mag += clamp3(levels[pos+txH]) // freq+1
		}
		if hy+2 < hGroups {
			mag += clamp3(levels[pos+2*txH]) // freq+2
		}
		if hy+3 < hGroups {
			mag += clamp3(levels[pos+3*txH]) // freq+3
		}
		if hy+4 < hGroups {
			mag += clamp3(levels[pos+4*txH]) // freq+4
		}
	case TX_CLASS_V:
		// V class: row-major layout. row = pos/txW (freq), col = pos%txW (cross).
		// Cross neighbor: same row, next col → +1.
		// Freq neighbors: same col, next rows → +txW.
		if col+1 < txW {
			mag += clamp3(levels[pos+1]) // cross+1
		}
		if row+1 < txH {
			mag += clamp3(levels[pos+txW]) // freq+1
		}
		if row+2 < txH {
			mag += clamp3(levels[pos+2*txW]) // freq+2
		}
		if row+3 < txH {
			mag += clamp3(levels[pos+3*txW]) // freq+3
		}
		if row+4 < txH {
			mag += clamp3(levels[pos+4*txW]) // freq+4
		}
	}

	ctx := int((mag + 1) >> 1)
	if ctx > 4 {
		ctx = 4
	}

	// Position-based offset.
	switch txClass {
	case TX_CLASS_2D:
		var shape int
		if origTxW == origTxH {
			shape = 0
		} else if origTxW > origTxH {
			shape = 1
		} else {
			shape = 2
		}
		r := row
		if r > 4 {
			r = 4
		}
		c := col
		if c > 4 {
			c = 4
		}
		ctx += loCtxOffsets[shape][r][c]
	case TX_CLASS_H:
		// Frequency axis = y = pos / txH.
		hFreq := pos / txH
		if hFreq > 1 {
			ctx += 36 // 26 + 10
		} else {
			ctx += 26 + hFreq*5
		}
	case TX_CLASS_V:
		// Frequency axis = row = pos / txW (row-major layout).
		vFreq := pos / txW
		if vFreq > 1 {
			ctx += 36 // 26 + 10
		} else {
			ctx += 26 + vFreq*5
		}
	}

	return ctx
}

// getCoeffBaseEobCtx returns a CoeffBaseEob context in [0,3].
// Matches dav1d: ctx = 1 + (eob > numCoeffs/8) + (eob > numCoeffs/4).
func getCoeffBaseEobCtx(c, numCoeffs int) int {
	if c == 0 {
		return 0
	}
	if c <= numCoeffs/8 {
		return 1
	}
	if c <= numCoeffs/4 {
		return 2
	}
	return 3
}

// getCoeffBRCtx returns the context for CoeffBR (base range refinement) CDFs.
// For TX_CLASS_2D: 3 neighbors (right, below, diagonal), offset by position.
// For TX_CLASS_H/V: 3 neighbors (freq+1, cross, freq+2), offset by freq.
func getCoeffBRCtx(levels []int32, pos, txW, txH int, txClass TxClass) int {
	row := pos / txW
	col := pos % txW

	clampBR := func(v int32) int32 {
		if v < 0 {
			v = -v
		}
		if v > 15 {
			return 15
		}
		return v
	}

	mag := int32(0)

	switch txClass {
	case TX_CLASS_2D:
		// 3 neighbors: right, below, below-right diagonal.
		if col+1 < txW {
			mag += clampBR(levels[pos+1])
		}
		if row+1 < txH {
			mag += clampBR(levels[pos+txW])
		}
		if col+1 < txW && row+1 < txH {
			mag += clampBR(levels[pos+txW+1])
		}
	case TX_CLASS_H:
		// H class: x = pos % txH (cross), y = pos / txH (frequency).
		// dav1d: 1 cross neighbor (x+1) + 2 frequency neighbors (y+1, y+2).
		hx := pos % txH
		hy := pos / txH
		hGroups := len(levels) / txH
		if hx+1 < txH {
			mag += clampBR(levels[pos+1]) // cross+1
		}
		if hy+1 < hGroups {
			mag += clampBR(levels[pos+txH]) // freq+1
		}
		if hy+2 < hGroups {
			mag += clampBR(levels[pos+2*txH]) // freq+2
		}
	case TX_CLASS_V:
		// V class: row-major. Cross (next col) → +1. Freq (next row) → +txW.
		if col+1 < txW {
			mag += clampBR(levels[pos+1]) // cross+1
		}
		if row+1 < txH {
			mag += clampBR(levels[pos+txW]) // freq+1
		}
		if row+2 < txH {
			mag += clampBR(levels[pos+2*txW]) // freq+2
		}
	}

	ctx := int((mag + 1) >> 1)
	if ctx > 6 {
		ctx = 6
	}

	// Position-based offset.
	if pos == 0 {
		// DC: contexts 0..6 (all classes)
	} else {
		switch txClass {
		case TX_CLASS_2D:
			// (row|col) > 1 → offset 14, else offset 7
			if row > 1 || col > 1 {
				ctx += 14
			} else {
				ctx += 7
			}
		case TX_CLASS_H:
			// freq = pos / txH; freq > 0 → offset 14, else offset 7
			if pos/txH > 0 {
				ctx += 14
			} else {
				ctx += 7
			}
		case TX_CLASS_V:
			// freq = pos / txW (row-major row index); freq > 0 → offset 14, else offset 7
			if pos/txW > 0 {
				ctx += 14
			} else {
				ctx += 7
			}
		}
	}

	if ctx > 20 {
		ctx = 20
	}

	return ctx
}

// decodeCoeffsInterleaved decodes Y and UV coefficients in the interleaved
// sub-region order used by dav1d/AV1 for intra blocks. For blocks larger than
// 64x64 luma pixels, the iteration processes 16x16 MI (64x64 pixel) sub-regions,
// decoding Y then U then V within each sub-region before moving to the next.
// For blocks <= 64x64, this is equivalent to all-Y then all-U then all-V.
//
// Matches dav1d read_coef_blocks (recon_tmpl.c).
func (td *TileDecoder) decodeCoeffsInterleaved(bc *BoolReader,
	yW, yH int, yTxSz TxSize, yMode int, yMiRow, yMiCol int,
	uvW, uvH int, uvTxSz TxSize, uvMode int, uvMiRow, uvMiCol int,
	hasChroma bool, subX, subY int, isInter bool,
) (yCoeffs, uCoeffs, vCoeffs []int32, err error) {

	yTxW, yTxH := TxSizeDimensions(yTxSz)
	if yTxW == 0 || yTxH == 0 {
		return nil, nil, nil, nil
	}
	yTxW4 := max(yTxW>>2, 1)
	yTxH4 := max(yTxH>>2, 1)
	yBlockMatchesTx := (yW == yTxW && yH == yTxH)

	// Luma block dimensions in MI units, clamped to the visible frame boundary.
	// Matches dav1d: w4 = imin(bw4, f->bw - t->bx), h4 = imin(bh4, f->bh - t->by).
	// TX blocks beyond the visible frame are NOT decoded from the bitstream.
	w4 := yW >> 2
	h4 := yH >> 2
	if visW := int(td.fh.MiCols) - yMiCol; visW < w4 {
		w4 = visW
	}
	if visH := int(td.fh.MiRows) - yMiRow; visH < h4 {
		h4 = visH
	}
	yResidual := make([]int32, yW*yH)
	yAnyNonZero := false

	var uvTxW, uvTxH, uvTxW4, uvTxH4 int
	var uvBlockMatchesTx bool
	var uResidual, vResidual []int32
	// Chroma dimensions in MI units, derived from clamped luma dimensions.
	// Matches dav1d: cw4 = (w4 + ss_hor) >> ss_hor, ch4 = (h4 + ss_ver) >> ss_ver.
	cw4, ch4 := 0, 0
	if hasChroma && uvW > 0 && uvH > 0 {
		uvTxW, uvTxH = TxSizeDimensions(uvTxSz)
		uvTxW4 = max(uvTxW>>2, 1)
		uvTxH4 = max(uvTxH>>2, 1)
		uvBlockMatchesTx = (uvW == uvTxW && uvH == uvTxH)
		uResidual = make([]int32, uvW*uvH)
		vResidual = make([]int32, uvW*uvH)
		cw4 = (w4 + subX) >> subX
		ch4 = (h4 + subY) >> subY
	}

	uAnyNonZero := false
	vAnyNonZero := false

	txIdx := 0

	// Iterate in 16x16 MI sub-regions (64x64 luma pixel chunks).
	// Set txtpMap origin for UV inter txType derivation.
	if isInter {
		td.txtpMapMiRow = yMiRow
		td.txtpMapMiCol = yMiCol
	}

	for initY := 0; initY < h4; initY += 16 {
		subH4 := min(h4, initY+16)
		for initX := 0; initX < w4; initX += 16 {
			subW4 := min(w4, initX+16)

			// --- Y luma TX blocks in this sub-region ---
			for y4 := initY; y4 < subH4; y4 += yTxH4 {
				for x4 := initX; x4 < subW4; x4 += yTxW4 {
					txOff := x4 * 4 // pixel offset
					tyOff := y4 * 4
					txMiRow := yMiRow + y4
					txMiCol := yMiCol + x4

					coeffs, cerr := td.decodeTxBlock(bc, yTxSz, 0, 0, yMode, txMiRow, txMiCol, yTxW4, yTxH4, yBlockMatchesTx, isInter, -1)
					if cerr != nil {
						return nil, nil, nil, fmt.Errorf("Y decodeTxBlock at (%d,%d): %w", txOff, tyOff, cerr)
					}
					txIdx++

					if coeffs != nil {
						yAnyNonZero = true
						curTxW := min(yTxW, yW-txOff)
						curTxH := min(yTxH, yH-tyOff)
						for r := 0; r < curTxH; r++ {
							for c := 0; c < curTxW; c++ {
								if r*yTxW+c < len(coeffs) {
									yResidual[(tyOff+r)*yW+(txOff+c)] = coeffs[r*yTxW+c]
								}
							}
						}
					}
				}
			}

			// --- UV chroma TX blocks in this sub-region ---
			if hasChroma && uvW > 0 && uvH > 0 && uvTxW > 0 && uvTxH > 0 {
				subCH4 := min(ch4, (initY+16)>>subY)
				subCW4 := min(cw4, (initX+16)>>subX)

				for pl := 1; pl <= 2; pl++ {
					planeType := 1
					for cy4 := initY >> subY; cy4 < subCH4; cy4 += uvTxH4 {
						for cx4 := initX >> subX; cx4 < subCW4; cx4 += uvTxW4 {
							txOff := cx4 * 4
							tyOff := cy4 * 4
							txMiRow := uvMiRow + cy4
							txMiCol := uvMiCol + cx4

							// For UV inter blocks, derive txType from luma.
						var uvOverride TxType = -1
						if isInter {
							lumaY4 := cy4 << subY
							lumaX4 := cx4 << subX
							yTxType := DCT_DCT
							if lumaY4 < 32 && lumaX4 < 32 {
								yTxType = td.txtpMap[lumaY4][lumaX4]
							}
							uvOverride = getUVInterTxType(uvTxSz, yTxType)
						}
						coeffs, cerr := td.decodeTxBlock(bc, uvTxSz, planeType, pl, uvMode, txMiRow, txMiCol, uvTxW4, uvTxH4, uvBlockMatchesTx, isInter, uvOverride)
							if cerr != nil {
								return nil, nil, nil, fmt.Errorf("UV pl=%d decodeTxBlock at (%d,%d): %w", pl, txOff, tyOff, cerr)
							}
							txIdx++

							if coeffs != nil {
								curTxW := min(uvTxW, uvW-txOff)
								curTxH := min(uvTxH, uvH-tyOff)
								res := uResidual
								if pl == 2 {
									res = vResidual
								}
								if pl == 1 {
									uAnyNonZero = true
								} else {
									vAnyNonZero = true
								}
								for r := 0; r < curTxH; r++ {
									for c := 0; c < curTxW; c++ {
										if r*uvTxW+c < len(coeffs) {
											res[(tyOff+r)*uvW+(txOff+c)] = coeffs[r*uvTxW+c]
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	if !yAnyNonZero {
		yResidual = nil
	}
	if !uAnyNonZero {
		uResidual = nil
	}
	if !vAnyNonZero {
		vResidual = nil
	}
	return yResidual, uResidual, vResidual, nil
}

// decodeCoeffsWithTx decodes all transform coefficients for a single plane.
// Used only for non-interleaved iteration (kept for reference/fallback).
func (td *TileDecoder) decodeCoeffsWithTx(bc *BoolReader, w, h, plane int, txSz TxSize, intraMode int, miRow, miCol int) ([]int32, error) {
	planeType := 0
	if plane > 0 {
		planeType = 1
	}

	txW, txH := TxSizeDimensions(txSz)

	if txW == 0 || txH == 0 {
		return nil, nil
	}

	// MI units per TX block.
	txW4 := txW >> 2
	txH4 := txH >> 2
	if txW4 < 1 {
		txW4 = 1
	}
	if txH4 < 1 {
		txH4 = 1
	}

	// Whether the block size matches the TX size (for txb_ctx).
	blockMatchesTx := (w == txW && h == txH)

	// Allocate the full-block residual in pixel domain.
	residual := make([]int32, w*h)
	anyNonZero := false

	// Iterate over transform blocks within the coding block.
	txIdx := 0
	for tyOff := 0; tyOff < h; tyOff += txH {
		for txOff := 0; txOff < w; txOff += txW {
			// Actual TX dimensions (may be clipped at block edge).
			curTxW := txW
			curTxH := txH
			if txOff+curTxW > w {
				curTxW = w - txOff
			}
			if tyOff+curTxH > h {
				curTxH = h - tyOff
			}

			// MI position of this TX block.
			txMiRow := miRow + (tyOff >> 2)
			txMiCol := miCol + (txOff >> 2)

			// Use the full TX size for parsing even if clipped.
			coeffs, err := td.decodeTxBlock(bc, txSz, planeType, plane, intraMode, txMiRow, txMiCol, txW4, txH4, blockMatchesTx, false, -1)
			if err != nil {
				return nil, fmt.Errorf("decodeTxBlock at offset (%d,%d): %w", txOff, tyOff, err)
			}
			txIdx++

			if coeffs != nil {
				anyNonZero = true
				// Copy TX block residual into the coding block residual.
				for r := 0; r < curTxH; r++ {
					for c := 0; c < curTxW; c++ {
						if r*txW+c < len(coeffs) {
							residual[(tyOff+r)*w+(txOff+c)] = coeffs[r*txW+c]
						}
					}
				}
			}
		}
	}

	if !anyNonZero {
		return nil, nil
	}

	return residual, nil
}

// decodeTxBlock decodes a single transform block's coefficients.
// AV1 spec Section 5.11.39 (coeffs).
//
// Returns the dequantized, inverse-transformed residual for one TX block,
// or nil if the block is all zeros.
func (td *TileDecoder) decodeTxBlock(bc *BoolReader, txSz TxSize, planeType, plane, intraMode int, txMiRow, txMiCol, txW4, txH4 int, blockMatchesTx bool, isInter bool, uvInterTxType TxType) ([]int32, error) {
	txW, txH := TxSizeDimensions(txSz)

	// AV1 caps coefficients at 32 per dimension for large transforms.
	// Compute the actual coded coefficient area first.
	coeffW := txW
	if coeffW > 32 {
		coeffW = 32
	}
	coeffH := txH
	if coeffH > 32 {
		coeffH = 32
	}
	numCoeffs := coeffW * coeffH

	// Read all_zero flag (txb_skip).
	// AV1 spec Section 5.11.35.
	txsCtx := txSizeCtx(txSz)
	if txsCtx > 4 {
		txsCtx = 4
	}
	txbCtx := td.getTxbCtx(plane, txMiRow, txMiCol, txW4, txH4, blockMatchesTx)
	allZeroCdf := td.cdf.AllZero[txsCtx][txbCtx]
	allZero, err := bc.ReadSymbolBoolInt(allZeroCdf)
	if err != nil {
		return nil, err
	}
	if allZero == 1 {
		td.updateCoeffCtx(plane, txMiRow, txMiCol, txW4, txH4, nil, 0, true)
		// Store DCT_DCT in txtpMap for all-zero inter luma blocks.
		// dav1d always writes txtp to txtp_map (even for skip), and sets
		// txtp = DCT_DCT when all_skip (decode_coefs line 347).
		// Without this, subsequent UV inter blocks read stale txtp values.
		// Fill the full TX block area (txW4 x txH4) to match dav1d.
		if isInter && plane == 0 {
			r0 := txMiRow - td.txtpMapMiRow
			c0 := txMiCol - td.txtpMapMiCol
			for ry := r0; ry < r0+txH4 && ry < 32; ry++ {
				if ry < 0 {
					continue
				}
				for cx := c0; cx < c0+txW4 && cx < 32; cx++ {
					if cx < 0 {
						continue
					}
					td.txtpMap[ry][cx] = DCT_DCT
				}
			}
		}
		return nil, nil
	}

	// --- Read TX type from bitstream ---
	// AV1 spec Section 5.11.37: transform_type().
	// dav1d: read when t_dim->max + intra < TX_64X64 (4).
	//   intra: max + 1 < 4 → max < 3 (reads for 4x4..16x16)
	//   inter: max + 0 < 4 → max < 4 (reads for 4x4..32x32)
	txType := DCT_DCT // default for inter
	if !isInter {
		txType = intraModeToTxType(intraMode, txSz)
	}

	// For UV inter blocks, the TX type is derived from the luma txType,
	// not decoded from the bitstream. dav1d: get_uv_inter_txtp().
	if uvInterTxType >= 0 {
		txType = uvInterTxType
	}

	// AV1 spec Section 5.11.37: In lossless mode, transform type is always
	// WHT_WHT (== DCT_DCT == 0). No TX type syntax is read from the bitstream.
	// This overrides the intraModeToTxType and UV derivation above.
	// Matches dav1d decode_coefs: if (lossless) { *txtp = WHT_WHT; }
	if td.fh.CodedLossless {
		txType = DCT_DCT
	}

	if !td.fh.CodedLossless && plane == 0 {
		lwi := floorLog2(txW) - 2 // log2 of width in 4-pixel units
		lhi := floorLog2(txH) - 2
		if lwi < 0 {
			lwi = 0
		}
		if lhi < 0 {
			lhi = 0
		}
		maxDimLog2 := lwi
		if lhi > maxDimLog2 {
			maxDimLog2 = lhi
		}
		minDimLog2 := lwi
		if lhi < minDimLog2 {
			minDimLog2 = lhi
		}

		// Threshold: max < 3 for intra, max < 4 for inter.
		threshold := 3
		if isInter {
			threshold = 4
		}

		if maxDimLog2 < threshold {
			if isInter {
				// Inter TX type: read from InterTxType CDFs.
				// dav1d set selection: reduced or max==TX_32X32 → set 1 (2 sym),
				// min==TX_16X16 → set 2 (12 sym), else → set 3 (16 sym).
				set := 3
				nsyms := 16
				if td.fh.ReducedTxSet || maxDimLog2 == 3 {
					set = 1
					nsyms = 2
				} else if minDimLog2 == 2 {
					set = 2
					nsyms = 12
				}
				idx, err := bc.ReadSymbol(td.cdf.InterTxType[set][minDimLog2], nsyms)
				if err != nil {
					return nil, fmt.Errorf("txtp_inter: %w", err)
				}
				if set == 1 {
					txType = txTypesInterSet1[idx]
				} else if set == 2 {
					txType = txTypesInterSet2[idx]
				} else {
					txType = txTypesInterSet3[idx]
				}
			} else {
				// Intra TX type: read from intra CDFs.
				if td.fh.ReducedTxSet || minDimLog2 == 2 {
					idx, err := bc.ReadSymbol(td.cdf.TxTypeIntra2[minDimLog2][intraMode], 5)
					if err != nil {
						return nil, fmt.Errorf("txtp_intra2: %w", err)
					}
					txType = txTypesIntra2[idx]
				} else {
					idx, err := bc.ReadSymbol(td.cdf.TxTypeIntra1[minDimLog2][intraMode], 7)
					if err != nil {
						return nil, fmt.Errorf("txtp_intra1: %w", err)
					}
					txType = txTypesIntra1[idx]
				}
			}
		}
	}

	// Store luma txType in txtpMap for UV inter derivation.
	// dav1d fills the full TX block area (txw4 x txh4 in MI units).
	if isInter && plane == 0 {
		r0 := txMiRow - td.txtpMapMiRow
		c0 := txMiCol - td.txtpMapMiCol
		for ry := r0; ry < r0+txH4 && ry < 32; ry++ {
			if ry < 0 {
				continue
			}
			for cx := c0; cx < c0+txW4 && cx < 32; cx++ {
				if cx < 0 {
					continue
				}
				td.txtpMap[ry][cx] = txType
			}
		}
	}

	// TX size entropy context for coefficient CDFs (0..4).
	txSzCtxVal := txSizeCtx(txSz)
	if txSzCtxVal > 4 {
		txSzCtxVal = 4
	}

	// TX class determines scan order and context computation.
	txClass := txClassOf(txType)

	// --- Parse EOB position ---
	// AV1 spec Section 5.11.36.
	eobMultiSz := eobMultiSize(numCoeffs)

	// dav1d: eob_bin CDFs indexed by [chroma][is_1d] where is_1d = (txClass != TX_CLASS_2D).
	// For eobMultiSz >= 5 (512+), dav1d has no is_1d dimension, always use 0.
	eobCdfIdx := 0
	if txClass != TX_CLASS_2D && eobMultiSz < 5 {
		eobCdfIdx = 1
	}
	eobPt, err := td.readEobPt(bc, eobMultiSz, planeType, eobCdfIdx)
	if err != nil {
		return nil, fmt.Errorf("eob_pt: %w", err)
	}

	eobBase, eobExtraBits := eobPtToEob(eobPt)

	// Read EOB extra bits to refine the position.
	eobExtra := 0
	for i := eobExtraBits - 1; i >= 0; i-- {
		// Use EobExtra CDF for the MSB, then literal bits for remaining.
		if i == eobExtraBits-1 && eobExtraBits > 0 {
			// The most significant extra bit uses an entropy-coded CDF.
			// AV1 spec: indexed by [txSzCtx][planeType][eobPt-2].
			eobExtraIdx := eobPt - 2
			if eobExtraIdx < 0 {
				eobExtraIdx = 0
			}
			if eobExtraIdx > 8 {
				eobExtraIdx = 8
			}
			sym, err := bc.ReadSymbolBoolInt(td.cdf.EobExtra[txSzCtxVal][planeType][eobExtraIdx])
			if err != nil {
				return nil, fmt.Errorf("eob_extra: %w", err)
			}
			eobExtra |= sym << uint(i)
		} else {
			// Remaining bits are literal.
			bit, err := bc.ReadLiteral(1)
			if err != nil {
				return nil, fmt.Errorf("eob_extra_literal: %w", err)
			}
			eobExtra |= int(bit) << uint(i)
		}
	}

	eob := eobBase + eobExtra // AV1 spec: eob = eob_offset + eob_extra
	if eob > numCoeffs {
		eob = numCoeffs
	}
	if eob <= 0 {
		return nil, nil
	}

	// --- Generate scan order ---
	// coeffW and coeffH were computed above (capped at 32 per dimension).
	// Scan returns positions in row-major format (pos = row*coeffW + col).
	scan := generateScanOrder(coeffW, coeffH, txClass, txSz)

	// --- Parse coefficient levels ---
	// Levels are read in REVERSE scan order from eob-1 down to 0.
	// AV1 spec Section 5.11.39.

	// levels stores the absolute coefficient levels indexed by position
	// in the coefficient block (raster order with stride=coeffW).
	levels := make([]int32, numCoeffs)

	// For BR CDFs, libaom caps at TX_32X32 (index 3).
	brTxSzCtx := txSzCtxVal
	if brTxSzCtx > 3 {
		brTxSzCtx = 3
	}

	for c := eob - 1; c >= 0; c-- {
		pos := scan[c]
		var level int

		if c == eob-1 {
			// Last non-zero position: use CoeffBaseEob CDF (3 symbols: 0,1,2+).
			eobCtx := getCoeffBaseEobCtx(c, numCoeffs)
			cdfSlice := td.cdf.CoeffBaseEob[txSzCtxVal][planeType][eobCtx]
			sym, err := bc.ReadSymbol(cdfSlice, 3)
			if err != nil {
				return nil, fmt.Errorf("coeff_base_eob at scan %d: %w", c, err)
			}
			level = sym + 1 // CoeffBaseEob codes (1,2,3+), so sym 0->level 1, etc.
		} else {
			// Other positions: use CoeffBase CDF (4 symbols: 0,1,2,3+).
			// DC (pos=0) always uses ctx=0 for TX_CLASS_2D (matches dav1d).
			// For non-2D classes, DC uses the normal context computation.
			baseCtx := 0
			if c > 0 || txClass != TX_CLASS_2D {
				baseCtx = getCoeffBaseCtx(levels, pos, coeffW, coeffH, txW, txH, txClass)
			}
			sym, err := bc.ReadSymbol(td.cdf.CoeffBase[txSzCtxVal][planeType][baseCtx], 4)
			if err != nil {
				return nil, fmt.Errorf("coeff_base at scan %d: %w", c, err)
			}
			level = sym // 0,1,2,3+
		}

		// If level indicates "3 or more", read BR (base range) refinement.
		// AV1 spec Section 5.11.40.
		if level >= 3 {
			// Read up to 4 CoeffBR symbols (each adds 0,1,2,3+ to the level).
			brCtx := getCoeffBRCtx(levels, pos, coeffW, coeffH, txClass)
			for k := 0; k < 4; k++ {
				sym, err := bc.ReadSymbol(td.cdf.CoeffBR[brTxSzCtx][planeType][brCtx], 4)
				if err != nil {
					return nil, fmt.Errorf("coeff_br at scan %d, k=%d: %w", c, k, err)
				}
				level += sym
				if sym < 3 {
					break // If symbol < 3, no more BR tokens needed.
				}
			}
		}

		// Level is capped at 15 here (base 3 + 4*3 max from BR).
		// Golomb extension for levels >= 15 is deferred to after sign reads,
		// matching dav1d's read order: levels → DC sign → {AC sign, Golomb} pairs.
		levels[pos] = int32(level)
	}

	// --- Read signs and Golomb extensions ---
	// dav1d reads: DC sign (adaptive), DC Golomb (if level==15),
	// then for each non-zero AC: sign (equi) + Golomb (if level==15).
	// AV1 spec Sections 5.11.38-5.11.40.
	signs := make([]int, numCoeffs)

	// DC sign uses DcSign CDF.
	if levels[0] != 0 {
		dcSignCtx := td.getDcSignCtx(plane, txMiRow, txMiCol, txW4, txH4)
		dcSign, err := bc.ReadSymbolBoolInt(td.cdf.DcSign[planeType][dcSignCtx])
		if err != nil {
			return nil, fmt.Errorf("dc_sign: %w", err)
		}
		signs[0] = dcSign // 0=positive, 1=negative

		// DC Golomb extension: read AFTER dc_sign, matching dav1d order.
		if levels[0] == 15 {
			golomb, err := readGolomb(bc)
			if err != nil {
				return nil, fmt.Errorf("dc golomb: %w", err)
			}
			levels[0] += int32(golomb)
		}
	}

	// AC signs with interleaved Golomb reads.
	// AV1 spec reads signs in forward scan order (c = 1 to eob-1).
	for c := 1; c < eob; c++ {
		pos := scan[c]
		if levels[pos] != 0 {
			signBit, err := bc.ReadLiteral(1)
			if err != nil {
				return nil, fmt.Errorf("sign at scan %d: %w", c, err)
			}
			signs[pos] = int(signBit)
			// Golomb extension for levels that hit the cap (15).
			if levels[pos] == 15 {
				golomb, err := readGolomb(bc)
				if err != nil {
					return nil, fmt.Errorf("golomb at scan %d: %w", c, err)
				}
				levels[pos] += int32(golomb)
			}
		}
	}

	// Apply signs to levels to get signed coefficients.
	// For 64-point transforms, AV1 only uses 32 coefficients per dimension,
	// but InverseTransform2D needs a full txW*txH array.
	fullSize := txW * txH
	coeffs := make([]int32, fullSize)
	// Place decoded coefficients into the upper-left portion.
	// coeffW/coeffH already computed above (capped at 32).
	//
	// dav1d's inv_txfm_add_c always reads coefficients with coeff[y + x*sh]
	// (column-major), which transposes whatever is in the cf buffer. So the
	// effective coefficient grid seen by the transform is the TRANSPOSE of
	// what's stored in the buffer.
	//
	// For TX_CLASS_2D: dav1d stores cf in column-major (via scan table).
	//   Column-major store + column-major read = row-major in transform. OK.
	//   Our code stores in row-major (scan converted to row-major positions).
	//   Row-major store + row-major read in our transform = same result. OK.
	//
	// For TX_CLASS_H: dav1d stores cf linearly (row-major via rc_i=i).
	//   Row-major store + column-major read = transposed in transform.
	//   Our code stores in column-major (below). Our transform reads row-major.
	//   Column-major store + row-major read = transposed. Same result. OK.
	for i := 0; i < numCoeffs; i++ {
		var r, c int
		if txClass == TX_CLASS_H {
			c = i / coeffH
			r = i % coeffH
		} else {
			// Row-major for TX_CLASS_2D and TX_CLASS_V.
			r = i / coeffW
			c = i % coeffW
		}
		if r < coeffH && c < coeffW {
			val := levels[i]
			if signs[i] == 1 {
				val = -val
			}
			coeffs[r*txW+c] = val
		}
	}

	// Update coefficient context for neighbor tracking.
	td.updateCoeffCtx(plane, txMiRow, txMiCol, txW4, txH4, levels, signs[0], false)

	// --- Dequantization ---
	// AV1 spec Section 7.12.2.
	// Use the per-SB quantizer index (BaseQIndex + delta_q), not the raw BaseQIndex.
	// Then apply per-plane DC/AC delta offsets from the frame header.
	baseQ := td.lastQIdx
	dcDelta := 0
	acDelta := 0
	switch plane {
	case 0:
		dcDelta = td.fh.DeltaQYDC
	case 1:
		dcDelta = td.fh.DeltaQUDC
		acDelta = td.fh.DeltaQUAC
	case 2:
		dcDelta = td.fh.DeltaQVDC
		acDelta = td.fh.DeltaQVAC
	}
	qIndexDC := GetQIndex(baseQ, dcDelta)
	qIndexAC := GetQIndex(baseQ, acDelta)
	isLossless := td.fh.CodedLossless

	DequantSeparate(coeffs, fullSize, qIndexDC, qIndexAC, isLossless, txSz)


	// --- Inverse transform ---
	// In lossless mode (4x4 only), use WHT (Walsh-Hadamard Transform).
	// Otherwise use the standard 2D inverse transform.
	if isLossless && txW == 4 && txH == 4 {
		InverseWHT4x4(coeffs)
	} else {
		InverseTransform2D(coeffs, txW, txH, txType)
	}

	return coeffs, nil
}


// readGolomb reads a Golomb-coded residual from the arithmetic decoder.
// Matches dav1d's read_golomb (src/recon_tmpl.c).
// AV1 spec Section 5.9.29: prefix counts 0-bits until a 1-bit is found.
// Returns the decoded value (>= 0).
func readGolomb(bc *BoolReader) (int, error) {
	length := 0
	for {
		bit, err := bc.ReadLiteral(1)
		if err != nil {
			return 0, fmt.Errorf("golomb prefix: %w", err)
		}
		if bit != 0 {
			break
		}
		length++
		if length >= 32 {
			break
		}
	}
	if length == 0 {
		return 0, nil
	}
	suffix, err := bc.ReadLiteral(length)
	if err != nil {
		return 0, fmt.Errorf("golomb suffix: %w", err)
	}
	return (1 << uint(length)) - 1 + int(suffix), nil
}

// sliceHasNonZero returns true if any element in s is non-zero.
func sliceHasNonZero(s []int32) bool {
	for _, v := range s {
		if v != 0 {
			return true
		}
	}
	return false
}

// --- Context helpers ---

// intraModeToTxType derives the transform type from the intra prediction mode.
// AV1 spec Section 6.8.4 (compute_tx_type), Table 7-56.
// For TX sizes where t_dim->max + intra >= TX_64X64 (max dim >= 32 for intra),
// only DCT_DCT is allowed. dav1d: t_dim->max + 1 >= 4 → max(lw,lh) >= 3.
func intraModeToTxType(intraMode int, txSz TxSize) TxType {
	w, h := TxSizeDimensions(txSz)
	maxDim := w
	if h > maxDim {
		maxDim = h
	}
	if maxDim >= 32 {
		return DCT_DCT
	}

	// AV1 spec Table 7-56: intra mode to TX type mapping.
	switch intraMode {
	case DC_PRED: // 0
		return DCT_DCT
	case V_PRED: // 1
		return ADST_DCT
	case H_PRED: // 2
		return DCT_ADST
	case D45_PRED: // 3
		return DCT_DCT
	case D135_PRED: // 4
		return ADST_ADST
	case D113_PRED: // 5
		return ADST_DCT
	case D157_PRED: // 6
		return DCT_ADST
	case D203_PRED: // 7
		return DCT_ADST
	case D67_PRED: // 8
		return ADST_DCT
	case SMOOTH_PRED: // 9
		return ADST_ADST
	case SMOOTH_V: // 10
		return ADST_DCT
	case SMOOTH_H: // 11
		return DCT_ADST
	case PAETH_PRED: // 12
		return ADST_ADST
	default:
		return DCT_DCT
	}
}

// txTypesIntra1 maps full-set TX type indices (7 symbols) to TxType values.
// dav1d order: IDTX, DCT_DCT, V_DCT, H_DCT, ADST_ADST, ADST_DCT, DCT_ADST.
var txTypesIntra1 = [7]TxType{IDTX, DCT_DCT, V_DCT, H_DCT, ADST_ADST, ADST_DCT, DCT_ADST}

// txTypesIntra2 maps reduced-set TX type indices (5 symbols) to TxType values.
// dav1d order: IDTX, DCT_DCT, ADST_ADST, ADST_DCT, DCT_ADST.
var txTypesIntra2 = [5]TxType{IDTX, DCT_DCT, ADST_ADST, ADST_DCT, DCT_ADST}

// Inter TX type mapping tables (dav1d convention).
// Matches dav1d_tx_types_per_set[] in tables.c.

// set 1: IDTX, DCT_DCT (2 symbols, for reduced or 32x32).
// dav1d: idx=0 → IDTX, idx=1 → DCT_DCT (via (idx-1) & IDTX trick).
var txTypesInterSet1 = [2]TxType{IDTX, DCT_DCT}

// set 2: 12 types (for 16x16). dav1d_tx_types_per_set[12..23].
// dav1d n_symbols=11 → 12 possible return values (0..11).
var txTypesInterSet2 = [12]TxType{
	IDTX, V_DCT, H_DCT, DCT_DCT, ADST_DCT, DCT_ADST, FLIPADST_DCT,
	DCT_FLIPADST, ADST_ADST, FLIPADST_FLIPADST, ADST_FLIPADST, FLIPADST_ADST,
}

// set 3: 16 types (for 4x4/8x8). dav1d_tx_types_per_set[24..39].
// dav1d n_symbols=15 → 16 possible return values (0..15).
var txTypesInterSet3 = [16]TxType{
	IDTX, V_DCT, H_DCT, V_ADST, H_ADST, V_FLIPADST, H_FLIPADST,
	DCT_DCT, ADST_DCT, DCT_ADST, FLIPADST_DCT, DCT_FLIPADST,
	ADST_ADST, FLIPADST_FLIPADST, ADST_FLIPADST, FLIPADST_ADST,
}

// getPartitionContext computes the partition CDF context for a block.
// Uses dav1d's bit-shift approach: extracts bit (log2(bSize)-1) from
// the stored partition context bitmask.
// Returns ctx in [0, 23]. Matches dav1d env.h get_partition_ctx.
func (td *TileDecoder) getPartitionContext(miRow, miCol, bSize int) int {
	// Block level: bl = 5 - log2(bSize)
	// Bit position: 4 - bl = log2(bSize) - 1
	bitPos := uint(ilog2(bSize) - 1)

	// Offset: 4 contexts per block level.
	// bl=0 (128x128) → offset 0, bl=1 (64x64) → offset 4, etc.
	bl := 5 - ilog2(bSize)
	offset := bl * 4

	above := 0
	if miCol < len(td.abovePartCtx) {
		above = int((td.abovePartCtx[miCol] >> bitPos) & 1)
	}

	left := 0
	if miRow < len(td.leftPartCtx) {
		left = int((td.leftPartCtx[miRow] >> bitPos) & 1)
	}

	return offset + left*2 + above
}

// ilog2 returns floor(log2(v)) for v > 0.
func ilog2(v int) int {
	n := 0
	for v > 1 {
		v >>= 1
		n++
	}
	return n
}

// dav1dAlPartCtx matches dav1d's dav1d_al_part_ctx[above/left][bl][partition].
// above=0, left=1. bl: 0=BL_128, 1=BL_64, 2=BL_32, 3=BL_16, 4=BL_8.
// partition: 0=none, 1=h, 2=v, 3=split, 4=tts, 5=tbs, 6=tls, 7=trs, 8=h4, 9=v4.
// Only T-split entries (4-7) differ from the bW/bH-derived default.
var dav1dAlPartCtx = [2][5][10]int{
	{ // above
		{0x00, 0x00, 0x10, -1, 0x00, 0x10, 0x10, 0x10, -1, -1},
		{0x10, 0x10, 0x18, -1, 0x10, 0x18, 0x18, 0x18, 0x10, 0x1c},
		{0x18, 0x18, 0x1c, -1, 0x18, 0x1c, 0x1c, 0x1c, 0x18, 0x1e},
		{0x1c, 0x1c, 0x1e, -1, 0x1c, 0x1e, 0x1e, 0x1e, 0x1c, 0x1f},
		{0x1e, 0x1e, 0x1f, 0x1f, -1, -1, -1, -1, -1, -1},
	},
	{ // left
		{0x00, 0x10, 0x00, -1, 0x10, 0x10, 0x00, 0x10, -1, -1},
		{0x10, 0x18, 0x10, -1, 0x18, 0x18, 0x10, 0x18, 0x1c, 0x10},
		{0x18, 0x1c, 0x18, -1, 0x1c, 0x1c, 0x18, 0x1c, 0x1e, 0x18},
		{0x1c, 0x1e, 0x1c, -1, 0x1e, 0x1e, 0x1c, 0x1e, 0x1f, 0x1c},
		{0x1e, 0x1f, 0x1e, 0x1f, -1, -1, -1, -1, -1, -1},
	},
}

// tSplitPartCtx returns (aboveCtx, leftCtx) partition context values for
// T-split partitions. bSize is in MI units, partition is the partition type.
// These values match dav1d's dav1d_al_part_ctx table and differ from the
// simple bW/bH-derived values for T-split sub-blocks.
func tSplitPartCtx(bSize, partition int) (int, int) {
	// Map bSize (MI units) to block level: 32->0, 16->1, 8->2, 4->3, 2->4
	bl := 0
	switch bSize {
	case 32:
		bl = 0
	case 16:
		bl = 1
	case 8:
		bl = 2
	case 4:
		bl = 3
	case 2:
		bl = 4
	}
	return dav1dAlPartCtx[0][bl][partition], dav1dAlPartCtx[1][bl][partition]
}

// getSkipContext computes the skip flag CDF context.
// AV1 spec Section 5.11.5: context = above_skip + left_skip (0, 1, or 2).
func (td *TileDecoder) getSkipContext(miRow, miCol int) int {
	ctx := 0
	if miRow > 0 && miCol < len(td.aboveSkip) && td.aboveSkip[miCol] {
		ctx++
	}
	if miCol > 0 && miRow < len(td.leftSkip) && td.leftSkip[miRow] {
		ctx++
	}
	return ctx
}

// getAboveIntraMode returns the intra mode of the block above (miRow, miCol).
// Returns DC_PRED if no above neighbor is available.
func (td *TileDecoder) getAboveIntraMode(miRow, miCol int) int {
	if miRow <= td.tileRowStart || miCol >= len(td.aboveModes) {
		return DC_PRED
	}
	return td.aboveModes[miCol]
}

// getLeftIntraMode returns the intra mode of the block to the left of (miRow, miCol).
// Returns DC_PRED if no left neighbor is available.
func (td *TileDecoder) getLeftIntraMode(miRow, miCol int) int {
	if miCol <= td.tileColStart || miRow >= len(td.leftModes) {
		return DC_PRED
	}
	return td.leftModes[miRow]
}

// isSmooth returns true if a mode is SMOOTH_PRED, SMOOTH_V, or SMOOTH_H.
func isSmooth(mode int) bool {
	return mode == SMOOTH_PRED || mode == SMOOTH_V || mode == SMOOTH_H
}

// neighborIsSm checks if above or left neighbor uses a smooth prediction mode.
// Matches dav1d's sm_flag lookup packed into angle bits 9-10.
// For plane > 0, checks using chroma MI coordinates.
func (td *TileDecoder) neighborIsSm(miRow, miCol, plane int) bool {
	aboveSm := false
	leftSm := false
	if plane > 0 {
		// Chroma: check UV modes at chroma MI position.
		// Matches dav1d: sm_uv_flag(t->a, cbx4) where cbx4 = bx4 >> ss_hor.
		// The UV mode arrays are stored at luma MI indices, but dav1d reads
		// at the chroma-subsampled index, so we use miRow/miCol directly
		// (they are already in chroma MI coordinates for chroma planes).
		subX := int(td.sh.ColorConfig.SubsamplingX)
		subY := int(td.sh.ColorConfig.SubsamplingY)
		tileRowStartC := td.tileRowStart >> subY
		tileColStartC := td.tileColStart >> subX
		if miRow > tileRowStartC && miCol < len(td.aboveUVModes) {
			aboveSm = isSmooth(td.aboveUVModes[miCol])
		}
		if miCol > tileColStartC && miRow < len(td.leftUVModes) {
			leftSm = isSmooth(td.leftUVModes[miRow])
		}
	} else {
		// Luma: check Y modes. Matches dav1d's sm_flag().
		if miRow > td.tileRowStart && miCol < len(td.aboveModes) {
			aboveSm = isSmooth(td.aboveModes[miCol])
		}
		if miCol > td.tileColStart && miRow < len(td.leftModes) {
			leftSm = isSmooth(td.leftModes[miRow])
		}
	}
	return aboveSm || leftSm
}

// blockSizeEnum maps pixel dimensions to the AV1 block size enum (0-21).
// AV1 spec Section 6.4.2, Table 4.
// Returns 22 for unrecognized sizes (signals "not a valid block size").
func blockSizeEnum(pixelW, pixelH int) int {
	switch {
	case pixelW == 4 && pixelH == 4:
		return 0 // BLOCK_4X4
	case pixelW == 4 && pixelH == 8:
		return 1 // BLOCK_4X8
	case pixelW == 8 && pixelH == 4:
		return 2 // BLOCK_8X4
	case pixelW == 8 && pixelH == 8:
		return 3 // BLOCK_8X8
	case pixelW == 8 && pixelH == 16:
		return 4 // BLOCK_8X16
	case pixelW == 16 && pixelH == 8:
		return 5 // BLOCK_16X8
	case pixelW == 16 && pixelH == 16:
		return 6 // BLOCK_16X16
	case pixelW == 16 && pixelH == 32:
		return 7 // BLOCK_16X32
	case pixelW == 32 && pixelH == 16:
		return 8 // BLOCK_32X16
	case pixelW == 32 && pixelH == 32:
		return 9 // BLOCK_32X32
	case pixelW == 32 && pixelH == 64:
		return 10 // BLOCK_32X64
	case pixelW == 64 && pixelH == 32:
		return 11 // BLOCK_64X32
	case pixelW == 64 && pixelH == 64:
		return 12 // BLOCK_64X64
	case pixelW == 64 && pixelH == 128:
		return 13 // BLOCK_64X128
	case pixelW == 128 && pixelH == 64:
		return 14 // BLOCK_128X64
	case pixelW == 128 && pixelH == 128:
		return 15 // BLOCK_128X128
	case pixelW == 4 && pixelH == 16:
		return 16 // BLOCK_4X16
	case pixelW == 16 && pixelH == 4:
		return 17 // BLOCK_16X4
	case pixelW == 8 && pixelH == 32:
		return 18 // BLOCK_8X32
	case pixelW == 32 && pixelH == 8:
		return 19 // BLOCK_32X8
	case pixelW == 16 && pixelH == 64:
		return 20 // BLOCK_16X64
	case pixelW == 64 && pixelH == 16:
		return 21 // BLOCK_64X16
	default:
		return 22
	}
}

// paletteBsizeCtx returns the palette block size context (0-6).
// AV1 spec Section 5.11.41: based on block area.
func paletteBsizeCtx(pixelW, pixelH int) int {
	area := pixelW * pixelH
	switch {
	case area <= 64:
		return 0 // 8x8
	case area <= 128:
		return 1 // 8x16, 16x8
	case area <= 256:
		return 2 // 16x16
	case area <= 512:
		return 3 // 16x32, 32x16
	case area <= 1024:
		return 4 // 32x32
	case area <= 2048:
		return 5 // 32x64, 64x32
	case area <= 4096:
		return 6 // 64x64
	default:
		return 7 // too large, palette not allowed
	}
}

// filterIntraBsizeCtx returns the block size index for UseFilterIntra CDF.
// AV1 spec Section 5.11.24: indexed by block_size enum.
func filterIntraBsizeCtx(pixelW, pixelH int) int {
	// Map to AV1 block size enum values used for UseFilterIntra[].
	// We only need to handle sizes where filter intra is allowed (<=32x32).
	switch {
	case pixelW == 4 && pixelH == 4:
		return 0
	case pixelW == 4 && pixelH == 8:
		return 1
	case pixelW == 8 && pixelH == 4:
		return 2
	case pixelW == 8 && pixelH == 8:
		return 3
	case pixelW == 8 && pixelH == 16:
		return 4
	case pixelW == 16 && pixelH == 8:
		return 5
	case pixelW == 16 && pixelH == 16:
		return 6
	case pixelW == 16 && pixelH == 32:
		return 7
	case pixelW == 32 && pixelH == 16:
		return 8
	case pixelW == 32 && pixelH == 32:
		return 9
	default:
		return 0
	}
}

// setModeInfo records the intra mode, UV mode, partition context, skip flag, and palette size
// for the block at (miRow, miCol) of size bW x bH.
func (td *TileDecoder) setModeInfo(miRow, miCol, bW, bH, mode, uvMode int, skip, hasChroma bool) {
	// Compute partition context values. Normally derived from block dimensions,
	// but T-split partitions override with values from dav1d_al_part_ctx table
	// (the context depends on the parent block level, not sub-block dimensions).
	aboveCtx := uint8((0x1F << uint(ilog2(bW))) & 0x1F)
	leftCtx := uint8((0x1F << uint(ilog2(bH))) & 0x1F)
	if td.partCtxAbove >= 0 {
		aboveCtx = uint8(td.partCtxAbove)
		td.partCtxAbove = -1 // consume override
	}
	if td.partCtxLeft >= 0 {
		leftCtx = uint8(td.partCtxLeft)
		td.partCtxLeft = -1 // consume override
	}

	// Set above context for all MI columns spanned by this block.
	for c := miCol; c < miCol+bW && c < len(td.aboveModes); c++ {
		td.aboveModes[c] = mode
		if c < len(td.abovePartCtx) {
			td.abovePartCtx[c] = aboveCtx
		}
		if c < len(td.aboveSkip) {
			td.aboveSkip[c] = skip
		}
	}
	// Set left context for all MI rows spanned by this block.
	for r := miRow; r < miRow+bH && r < len(td.leftModes); r++ {
		td.leftModes[r] = mode
		if r < len(td.leftPartCtx) {
			td.leftPartCtx[r] = leftCtx
		}
		if r < len(td.leftSkip) {
			td.leftSkip[r] = skip
		}
	}
	// Update frame-level ModeInfo grid and neighbor context arrays.
	// Intra blocks must also update aboveModeInfo/leftModeInfo so that
	// neighbor queries (is_inter context, MV stack, etc.) return correct values.
	// Matches dav1d: intra blocks in inter frames store comp_type=NONE, ref={-1,-1},
	// filter={N_SWITCHABLE_FILTERS, N_SWITCHABLE_FILTERS} (decode.c lines 1254-1258).
	intraInfo := ModeInfo{
		IsIntra:  true,
		RefFrame: [2]int8{-1, -1},
		Filter:   [2]uint8{3, 3},
		BW4:      uint8(bW),
		BH4:      uint8(bH),
	}
	for c := miCol; c < miCol+bW && c < len(td.aboveModeInfo); c++ {
		td.aboveModeInfo[c] = intraInfo
	}
	for r := miRow; r < miRow+bH && r < len(td.leftModeInfo); r++ {
		td.leftModeInfo[r] = intraInfo
	}
	if td.miGrid != nil {
		td.miGrid.Set(miRow, miCol, bW, bH, intraInfo)
	}
	// UV mode context: stored at chroma MI coordinates (matching dav1d).
	// Only blocks with chroma write UV mode context.
	if hasChroma {
		subX := int(td.sh.ColorConfig.SubsamplingX)
		subY := int(td.sh.ColorConfig.SubsamplingY)
		cbx4 := miCol >> subX
		cby4 := miRow >> subY
		cbw4 := (bW + subX) >> subX
		cbh4 := (bH + subY) >> subY
		for c := cbx4; c < cbx4+cbw4 && c < len(td.aboveUVModes); c++ {
			td.aboveUVModes[c] = uvMode
		}
		for r := cby4; r < cby4+cbh4 && r < len(td.leftUVModes); r++ {
			td.leftUVModes[r] = uvMode
		}
	}
}

// setPaletteInfo records the palette size and colors for the block at (miRow, miCol).
// palColors maps plane -> sorted palette color array. Pass nil for planes without palette.
func (td *TileDecoder) setPaletteInfo(miRow, miCol, bW, bH, palSzY, palSzUV int, palColors map[int][]uint8) {
	psz := uint8(palSzY)
	for c := miCol; c < miCol+bW && c < len(td.abovePalSz); c++ {
		td.abovePalSz[c] = psz
	}
	for r := miRow; r < miRow+bH && r < len(td.leftPalSz); r++ {
		td.leftPalSz[r] = psz
	}
	// UV palette size tracked separately for UV palette neighbor cache.
	uvpsz := uint8(palSzUV)
	for c := miCol; c < miCol+bW && c < len(td.abovePalSzUV); c++ {
		td.abovePalSzUV[c] = uvpsz
	}
	for r := miRow; r < miRow+bH && r < len(td.leftPalSzUV); r++ {
		td.leftPalSzUV[r] = uvpsz
	}
	// Store palette colors for each plane.
	for pl, colors := range palColors {
		var packed [8]uint8
		for i := 0; i < len(colors) && i < 8; i++ {
			packed[i] = colors[i]
		}
		for c := miCol; c < miCol+bW && c < len(td.abovePalColors[pl]); c++ {
			td.abovePalColors[pl][c] = packed
		}
		for r := miRow; r < miRow+bH && r < len(td.leftPalColors[pl]); r++ {
			td.leftPalColors[pl][r] = packed
		}
	}
}

// getPaletteContext returns the palette CDF context (0, 1, or 2) based on
// whether above and left neighbors use palette.
func (td *TileDecoder) getPaletteContext(miRow, miCol int) int {
	ctx := 0
	if miCol < len(td.abovePalSz) && td.abovePalSz[miCol] > 0 {
		ctx++
	}
	if miRow < len(td.leftPalSz) && td.leftPalSz[miRow] > 0 {
		ctx++
	}
	return ctx
}

// getTxSizeContext computes the TX size CDF context from above/left neighbor TX log2 sizes.
// Reads from tx_intra arrays (aboveTxW/leftTxH), initialized to -1.
// Matches dav1d's get_tx_ctx: (left_log2 >= max_lh) + (above_log2 >= max_lw).
// Returns 0, 1, or 2.
func (td *TileDecoder) getTxSizeContext(miRow, miCol int, maxTx TxSize) int {
	maxLw, maxLh := txSizeLog2WH(maxTx)
	ctx := 0
	aboveVal := -1
	if miCol < len(td.aboveTxW) {
		aboveVal = td.aboveTxW[miCol]
	}
	leftVal := -1
	if miRow < len(td.leftTxH) {
		leftVal = td.leftTxH[miRow]
	}
	// -1 (init value) is always < any maxLw/maxLh >= 0, so contributes 0.
	if aboveVal >= maxLw {
		ctx++
	}
	if leftVal >= maxLh {
		ctx++
	}
	return ctx
}

// setTxSizeCtx records the chosen TX size (log2 of dim in MI) for above/left context arrays.
// For intra blocks, stores TX dimension log2. Matches dav1d's t_dim->lw/lh.
func (td *TileDecoder) setTxSizeCtx(miRow, miCol, bW, bH int, txSz TxSize) {
	lw, lh := txSizeLog2WH(txSz)
	for c := miCol; c < miCol+bW && c < len(td.aboveTxW); c++ {
		td.aboveTxW[c] = lw
	}
	for r := miRow; r < miRow+bH && r < len(td.leftTxH); r++ {
		td.leftTxH[r] = lh
	}
	// Also update var TX context arrays (tx[]). dav1d sets both tx_intra[]
	// and tx[] to the same value for intra blocks in inter frames
	// (decode.c line 1265-1266: edge->tx_intra and edge->tx).
	for c := miCol; c < miCol+bW && c < len(td.aboveTx); c++ {
		td.aboveTx[c] = lw
	}
	for r := miRow; r < miRow+bH && r < len(td.leftTx); r++ {
		td.leftTx[r] = lh
	}
}

// setTxIntraCtxBlock records block dim log2 in the tx_intra[] context arrays only.
// Used for inter var-TX blocks where tx[] is already set by readVarTxSize leaves,
// but tx_intra[] still needs block dims (dav1d decode.c line 1964).
func (td *TileDecoder) setTxIntraCtxBlock(miRow, miCol, bW, bH int) {
	lwBlock := floorLog2(bW)
	lhBlock := floorLog2(bH)
	for c := miCol; c < miCol+bW && c < len(td.aboveTxW); c++ {
		td.aboveTxW[c] = lwBlock
	}
	for r := miRow; r < miRow+bH && r < len(td.leftTxH); r++ {
		td.leftTxH[r] = lhBlock
	}
}

// setTxSizeCtxBlock records block dimensions (log2 of MI dims) for above/left context arrays.
// For inter blocks, stores block dimension log2. Matches dav1d's b_dim[2]/b_dim[3].
func (td *TileDecoder) setTxSizeCtxBlock(miRow, miCol, bW, bH int) {
	lwBlock := floorLog2(bW)
	lhBlock := floorLog2(bH)
	for c := miCol; c < miCol+bW && c < len(td.aboveTxW); c++ {
		td.aboveTxW[c] = lwBlock
	}
	for r := miRow; r < miRow+bH && r < len(td.leftTxH); r++ {
		td.leftTxH[r] = lhBlock
	}
}

// setTxIntraCtxTx sets tx_intra[] to TX size log2 and tx[] to BLOCK size log2.
// Used for intra-in-inter blocks. Matches dav1d decode.c lines 1261-1266:
//   tx_intra[]: t_dim->lw / t_dim->lh (TX dim log2)
//   tx[]:       b_dim[2] / b_dim[3]   (BLOCK dim log2)
func (td *TileDecoder) setTxIntraCtxTx(miRow, miCol, bW, bH int, txSz TxSize) {
	lw, lh := txSizeLog2WH(txSz)
	lwBlock := floorLog2(bW)
	lhBlock := floorLog2(bH)
	for c := miCol; c < miCol+bW && c < len(td.aboveTxW); c++ {
		td.aboveTxW[c] = lw
	}
	for r := miRow; r < miRow+bH && r < len(td.leftTxH); r++ {
		td.leftTxH[r] = lh
	}
	for c := miCol; c < miCol+bW && c < len(td.aboveTx); c++ {
		td.aboveTx[c] = lwBlock
	}
	for r := miRow; r < miRow+bH && r < len(td.leftTx); r++ {
		td.leftTx[r] = lhBlock
	}
}

// setVarTxCtxBlock stores block dim log2 in the tx[] (var TX) context arrays.
// Used for inter skip blocks. Matches dav1d lines 466-467.
func (td *TileDecoder) setVarTxCtxBlock(miRow, miCol, bW, bH int) {
	lwBlock := floorLog2(bW)
	lhBlock := floorLog2(bH)
	for c := miCol; c < miCol+bW && c < len(td.aboveTx); c++ {
		td.aboveTx[c] = lwBlock
	}
	for r := miRow; r < miRow+bH && r < len(td.leftTx); r++ {
		td.leftTx[r] = lhBlock
	}
}

// setVarTxCtxVal stores a fixed value in the tx[] (var TX) context arrays.
// Used for inter lossless/TX_4X4 blocks. Matches dav1d lines 461-462.
func (td *TileDecoder) setVarTxCtxVal(miRow, miCol, bW, bH, val int) {
	for c := miCol; c < miCol+bW && c < len(td.aboveTx); c++ {
		td.aboveTx[c] = val
	}
	for r := miRow; r < miRow+bH && r < len(td.leftTx); r++ {
		td.leftTx[r] = val
	}
}

// skipContexts is the lookup table for luma txb_ctx when block size != TX size.
// Indexed by [min(topLevel,4)][min(leftLevel,4)].
var skipContexts = [5][5]int{
	{1, 2, 2, 2, 3},
	{2, 4, 4, 4, 5},
	{2, 4, 4, 4, 5},
	{2, 4, 4, 4, 5},
	{3, 5, 5, 5, 6},
}

// getTxbCtx computes the context index for the all_zero (txb_skip) CDF.
// AV1 spec Section 5.11.39 / get_txb_ctx.
func (td *TileDecoder) getTxbCtx(plane, txMiRow, txMiCol, txW4, txH4 int, blockMatchesTx bool) int {
	if plane == 0 { // Luma
		if blockMatchesTx {
			return 0
		}
		// OR together above context bytes, extract level bits.
		var top uint8
		for c := 0; c < txW4; c++ {
			col := txMiCol + c
			if col < len(td.aboveCoeffCtx[0]) {
				top |= td.aboveCoeffCtx[0][col]
			}
		}
		topLevel := int(top & 0x3F)
		if topLevel > 4 {
			topLevel = 4
		}

		var left uint8
		for r := 0; r < txH4; r++ {
			row := txMiRow + r
			if row < len(td.leftCoeffCtx[0]) {
				left |= td.leftCoeffCtx[0][row]
			}
		}
		leftLevel := int(left & 0x3F)
		if leftLevel > 4 {
			leftLevel = 4
		}

		return skipContexts[topLevel][leftLevel]
	}

	// Chroma planes.
	aboveNonZero := false
	for c := 0; c < txW4; c++ {
		col := txMiCol + c
		if col < len(td.aboveCoeffCtx[plane]) && td.aboveCoeffCtx[plane][col] != 0x40 {
			aboveNonZero = true
			break
		}
	}
	leftNonZero := false
	for r := 0; r < txH4; r++ {
		row := txMiRow + r
		if row < len(td.leftCoeffCtx[plane]) && td.leftCoeffCtx[plane][row] != 0x40 {
			leftNonZero = true
			break
		}
	}

	ctxBase := 0
	if aboveNonZero {
		ctxBase++
	}
	if leftNonZero {
		ctxBase++
	}

	if !blockMatchesTx {
		return ctxBase + 10
	}
	return ctxBase + 7
}

// getDcSignCtx computes the DC sign context from neighbor context bytes.
// Returns 0 (balanced), 1 (more negative), or 2 (more positive).
func (td *TileDecoder) getDcSignCtx(plane, txMiRow, txMiCol, txW4, txH4 int) int {
	dcSign := 0
	for c := 0; c < txW4; c++ {
		col := txMiCol + c
		if col < len(td.aboveCoeffCtx[plane]) {
			signBits := td.aboveCoeffCtx[plane][col] >> 6
			// 0 = negative (-1), 1 = neutral (0), 2 = positive (+1)
			dcSign += int(signBits) - 1
		}
	}
	for r := 0; r < txH4; r++ {
		row := txMiRow + r
		if row < len(td.leftCoeffCtx[plane]) {
			signBits := td.leftCoeffCtx[plane][row] >> 6
			dcSign += int(signBits) - 1
		}
	}

	if dcSign < 0 {
		return 1
	} else if dcSign > 0 {
		return 2
	}
	return 0
}

// setSkipCoeffCtx sets coefficient context to 0x40 (skip) for an entire block
// area. Called for blocks with skip=true, matching dav1d's read_coef_blocks
// which memsets lcoef/ccoef to 0x40 for skipped blocks.
func (td *TileDecoder) setSkipCoeffCtx(miRow, miCol, bW4, bH4 int, hasChroma bool, subX, subY int) {
	// Luma
	for c := 0; c < bW4; c++ {
		col := miCol + c
		if col < len(td.aboveCoeffCtx[0]) {
			td.aboveCoeffCtx[0][col] = 0x40
		}
	}
	for r := 0; r < bH4; r++ {
		row := miRow + r
		if row < len(td.leftCoeffCtx[0]) {
			td.leftCoeffCtx[0][row] = 0x40
		}
	}
	// Chroma
	if hasChroma {
		cbW4 := (bW4 + subX) >> subX
		cbH4 := (bH4 + subY) >> subY
		chromaMiCol := miCol >> subX
		chromaMiRow := miRow >> subY
		for pl := 1; pl <= 2; pl++ {
			for c := 0; c < cbW4; c++ {
				col := chromaMiCol + c
				if col < len(td.aboveCoeffCtx[pl]) {
					td.aboveCoeffCtx[pl][col] = 0x40
				}
			}
			for r := 0; r < cbH4; r++ {
				row := chromaMiRow + r
				if row < len(td.leftCoeffCtx[pl]) {
					td.leftCoeffCtx[pl][row] = 0x40
				}
			}
		}
	}
}

// updateCoeffCtx updates the above/left coefficient context arrays after
// decoding a transform block.
func (td *TileDecoder) updateCoeffCtx(plane, txMiRow, txMiCol, txW4, txH4 int, levels []int32, dcSign int, wasAllZero bool) {
	var ctx uint8
	if wasAllZero {
		ctx = 0x40 // neutral DC sign, zero level
	} else {
		// Compute cumulative level.
		var culLevel int32
		for _, l := range levels {
			if l < 0 {
				culLevel -= l
			} else {
				culLevel += l
			}
		}
		if culLevel > 63 {
			culLevel = 63
		}

		// Encode DC sign: 0=negative (0x00), 1=neutral (0x40), 2=positive (0x80).
		// When DC level is 0, sign is neutral regardless of the dcSign parameter
		// (which defaults to 0 when no sign was read from the bitstream).
		var dcSignBits uint8
		if levels != nil && len(levels) > 0 && levels[0] != 0 {
			if dcSign == 0 { // positive
				dcSignBits = 0x80
			} else { // negative
				dcSignBits = 0x00
			}
		} else {
			dcSignBits = 0x40 // neutral: DC is zero
		}
		ctx = uint8(culLevel) | dcSignBits
	}

	// Clamp context writes to the visible frame boundary.
	// Matches dav1d: imin(t_dim->w, f->bw - t->bx) for luma,
	// imin(uv_t_dim->w, (f->bw - t->bx + ss_hor) >> ss_hor) for chroma.
	ctxW := txW4
	ctxH := txH4
	if plane == 0 {
		if rem := int(td.fh.MiCols) - txMiCol; rem < ctxW {
			ctxW = rem
		}
		if rem := int(td.fh.MiRows) - txMiRow; rem < ctxH {
			ctxH = rem
		}
	} else {
		// Chroma: txMiCol/txMiRow are in chroma MI coords.
		// dav1d: imin(txW, (f->bw - lumaBx + ss_hor) >> ss_hor).
		// lumaBx = txMiCol << subX, so: (effMiCols - txMiCol*2 + 1) >> 1 for 4:2:0.
		subX := int(td.sh.ColorConfig.SubsamplingX)
		subY := int(td.sh.ColorConfig.SubsamplingY)
		lumaBx := txMiCol << subX
		lumaBy := txMiRow << subY
		chromaRemW := (int(td.fh.MiCols) - lumaBx + subX) >> subX
		chromaRemH := (int(td.fh.MiRows) - lumaBy + subY) >> subY
		if chromaRemW < ctxW {
			ctxW = chromaRemW
		}
		if chromaRemH < ctxH {
			ctxH = chromaRemH
		}
	}

	for c := 0; c < ctxW; c++ {
		col := txMiCol + c
		if col < len(td.aboveCoeffCtx[plane]) {
			td.aboveCoeffCtx[plane][col] = ctx
		}
	}
	for r := 0; r < ctxH; r++ {
		row := txMiRow + r
		if row < len(td.leftCoeffCtx[plane]) {
			td.leftCoeffCtx[plane][row] = ctx
		}
	}
}

// decodeIntraBCBlock decodes an IntraBC block within an intra frame.
// IntraBC (Intra Block Copy) copies pixels from an already-decoded region
// of the same frame, then adds a residual. It uses integer-pel MVs and
// is only available when AllowIntraBC is true.
// Matches dav1d decode.c lines 1265-1379 and recon_tmpl.c lines 1599-1611.
func (td *TileDecoder) decodeIntraBCBlock(bc *BoolReader, miRow, miCol, bW, bH int, skip bool, edgeFlags uint8) error {
	nomPixW := bW * 4
	nomPixH := bH * 4
	subX := int(td.sh.ColorConfig.SubsamplingX)
	subY := int(td.sh.ColorConfig.SubsamplingY)
	ibcRef := int8(-1) // IntraBC ref sentinel in our 0-based ref system

	// --- IntraBC MV stack search ---
	// Use the full findMVStack with ref0=-1 (our IntraBC sentinel).
	// Matches dav1d: dav1d_refmvs_find(&t->rt, mvstack, &n_mvs, &ctx,
	//   (union refmvs_refpair){.ref={0,-1}}, bs, edge_flags, t->by, t->bx)
	// In dav1d, ref.ref[0]=0 means self-frame. In our system, ref0=-1 is the
	// IntraBC sentinel. findMVStack will match neighbor blocks that have
	// RefFrame[0]==-1 && !IsIntra (i.e., other IntraBC blocks).
	mvstack, _, _ := td.findMVStack(miRow, miCol, bW, bH, ibcRef, -1, edgeFlags)

	// --- MV selection (no drl_mode for IntraBC) ---
	// dav1d decode.c lines 1273-1284: pick first non-zero MV from stack,
	// else use a special default based on SB position.
	var mv MV
	if mvstack[0].MV[0].Row != 0 || mvstack[0].MV[0].Col != 0 {
		mv = mvstack[0].MV[0]
	} else if mvstack[1].MV[0].Row != 0 || mvstack[1].MV[0].Col != 0 {
		mv = mvstack[1].MV[0]
	} else {
		// Special default MV: depends on position relative to SB row start.
		// dav1d: if (t->by - (16 << sb128) < ts->tiling.row_start)
		//            mv = {0, -(512 << sb128) - 2048}
		//        else mv = {-(512 << sb128), 0}
		sb128 := 0
		if td.sh.Use128x128Superblock {
			sb128 = 1
		}
		if miRow-(16<<sb128) < td.tileRowStart {
			mv.Row = 0
			mv.Col = int32(-(512 << sb128) - 2048)
		} else {
			mv.Row = int32(-(512 << sb128))
			mv.Col = 0
		}
	}

	// Read MV residual with force_integer_mv precision (mvPrec = -1).
	if err := td.readMVResidual(bc, &mv, -1); err != nil {
		return fmt.Errorf("intrabc mv at (%d,%d): %w", miRow, miCol, err)
	}

	// --- Clip IntraBC MV to decoded parts of current tile ---
	// Matches dav1d decode.c lines 1290-1343.
	{
		borderLeft := td.tileColStart * 4
		borderTop := td.tileRowStart * 4
		hasChromaForClip := subX != 0 || subY != 0
		if hasChromaForClip {
			if bW < 2 && subX == 1 {
				borderLeft += 4
			}
			if bH < 2 && subY == 1 {
				borderTop += 4
			}
		}
		srcLeft := miCol*4 + int(mv.Col)/8
		srcTop := miRow*4 + int(mv.Row)/8
		srcRight := srcLeft + bW*4
		srcBottom := srcTop + bH*4
		borderRight := ((td.tileColEnd + (bW - 1)) &^ (bW - 1)) * 4

		// Clamp to tile boundaries.
		if srcLeft < borderLeft {
			d := borderLeft - srcLeft
			srcLeft += d
			srcRight += d
		} else if srcRight > borderRight {
			d := srcRight - borderRight
			srcLeft -= d
			srcRight -= d
		}
		if srcTop < borderTop {
			d := borderTop - srcTop
			srcTop += d
			srcBottom += d
		}

		// Check for overlap with current superblock.
		sb128 := 0
		if td.sh.Use128x128Superblock {
			sb128 = 1
		}
		sbx := (miCol >> (4 + sb128)) << (6 + sb128)
		sby := (miRow >> (4 + sb128)) << (6 + sb128)
		sbSize := 1 << (6 + sb128)

		if srcBottom > sby && srcRight > sbx {
			if srcTop-borderTop >= srcBottom-sby {
				d := srcBottom - sby
				srcTop -= d
				srcBottom -= d
			} else if srcLeft-borderLeft >= srcRight-sbx {
				d := srcRight - sbx
				srcLeft -= d
				srcRight -= d
			}
		}
		if srcBottom > sby+sbSize {
			d := srcBottom - (sby + sbSize)
			srcTop -= d
			srcBottom -= d
		}
		_ = srcBottom
		_ = srcRight

		// Write clipped MV back (in 1/8-pel units).
		mv.Col = int32((srcLeft - miCol*4) * 8)
		mv.Row = int32((srcTop - miRow*4) * 8)
	}

	// --- Variable TX tree (matches dav1d read_vartx_tree) ---
	// IntraBC uses the inter-block vartx tree logic, NOT just TX_MODE_LARGEST.
	// dav1d decode.c line 1350: read_vartx_tree(t, b, bs, bx4, by4).
	maxRectTx := blockSizeToTxSize(nomPixW, nomPixH)
	var varBlocks []varTxBlock

	if td.fh.TxMode == TxModeSelect_ && !skip && !td.fh.CodedLossless {
		// Parse variable TX size tree per AV1 spec Section 5.11.38.
		txW, txH := TxSizeDimensions(maxRectTx)
		txW4 := max(txW>>2, 1)
		txH4 := max(txH>>2, 1)
		for y4 := 0; y4 < bH; y4 += txH4 {
			for x4 := 0; x4 < bW; x4 += txW4 {
				blocks, err := td.readVarTxSize(bc, miRow+y4, miCol+x4, maxRectTx, 0)
				if err != nil {
					return fmt.Errorf("intrabc var_tx_size at (%d,%d): %w", miRow+y4, miCol+x4, err)
				}
				varBlocks = append(varBlocks, blocks...)
			}
		}
		// tx[] is set by readVarTxSize leaves; set tx_intra to block dims.
		td.setTxIntraCtxBlock(miRow, miCol, bW, bH)
	} else {
		// No variable TX: use maxRectTx uniformly.
		lumaTxSz := maxRectTx
		if td.fh.CodedLossless {
			lumaTxSz = TX_4X4
		}
		txW, txH := TxSizeDimensions(lumaTxSz)
		txW4 := max(txW>>2, 1)
		txH4 := max(txH>>2, 1)
		for y4 := 0; y4 < bH; y4 += txH4 {
			for x4 := 0; x4 < bW; x4 += txW4 {
				varBlocks = append(varBlocks, varTxBlock{
					miRow: miRow + y4, miCol: miCol + x4, txSz: lumaTxSz,
				})
			}
		}
		td.setTxSizeCtxBlock(miRow, miCol, bW, bH)
		if skip {
			td.setVarTxCtxBlock(miRow, miCol, bW, bH)
		} else {
			td.setVarTxCtxVal(miRow, miCol, bW, bH, 0)
		}
	}

	// Clear palette info.
	td.setPaletteInfo(miRow, miCol, bW, bH, 0, 0, nil)

	// --- Chroma ---
	nomChromaW := nomPixW >> subX
	nomChromaH := nomPixH >> subY
	if nomChromaW < 4 {
		nomChromaW = 4
	}
	if nomChromaH < 4 {
		nomChromaH = 4
	}
	chromaTxSz := adjustUVTxSize(blockSizeToTxSize(nomChromaW, nomChromaH))
	if td.fh.CodedLossless {
		chromaTxSz = TX_4X4
	}
	chromaMiRow := miRow >> subY
	chromaMiCol := miCol >> subX

	hasChroma := true
	if subX == 1 && !(miCol&1 == 1 || bW >= 2) {
		hasChroma = false
	}
	if subY == 1 && !(miRow&1 == 1 || bH >= 2) {
		hasChroma = false
	}

	// --- Block copy prediction ---
	// IntraBC uses mc() with FILTER_2D_BILINEAR and the current frame as reference.
	// Luma MVs are integer-pel so luma is a simple copy. Chroma may have sub-pixel
	// offsets in 4:2:0/4:2:2 requiring bilinear interpolation.
	// AV1 spec + dav1d recon_tmpl.c lines 1600-1611.
	mvx := int(mv.Col)
	mvy := int(mv.Row)

	// Luma: integer-pel copy.
	srcX := miCol*4 + mvx/8
	srcY := miRow*4 + mvy/8

	// dav1d mc() for IntraBC uses f->bw*4 and f->bh*4 as reference bounds.
	refW := int(td.fh.MiCols) * 4
	refH := int(td.fh.MiRows) * 4

	predY := make([]byte, nomPixW*nomPixH)
	ibcCopyBlock(predY, nomPixW, nomPixH, td.frame.Y, td.frame.StrideY, srcX, srcY, refW, refH)

	var predU, predV []byte
	if hasChroma {
		predU = make([]byte, nomChromaW*nomChromaH)
		predV = make([]byte, nomChromaW*nomChromaH)
		// Chroma source: matches dav1d mc() with bx = t->bx & ~ss_hor, etc.
		chromaBx := miCol &^ subX
		chromaBy := miRow &^ subY
		hMul := 4 >> subX
		vMul := 4 >> subY
		chromaSrcX := chromaBx*hMul + (mvx >> (3 + subX))
		chromaSrcY := chromaBy*vMul + (mvy >> (3 + subY))
		chromaRefW := refW >> subX
		chromaRefH := refH >> subY

		// Fractional chroma MV components (1/16-pel for 4:2:0).
		// dav1d: mx = mvx & (15 >> !ss_hor), my = mvy & (15 >> !ss_ver)
		// then scaled: mx << !ss_hor, my << !ss_ver when calling mc.
		// For 4:2:0: !ss_hor=0, so mx = mvx & 15, my = mvy & 15.
		// For 4:4:4: !ss_hor=1, so mx = mvx & 7, my = mvy & 7 (but shifted <<1 later).
		// For bilinear, the filter is just: out = ((16-f)*p0 + f*p1 + 8) >> 4.
		notSsHor := 0
		if subX == 0 {
			notSsHor = 1
		}
		notSsVer := 0
		if subY == 0 {
			notSsVer = 1
		}
		mx := mvx & (15 >> notSsHor)
		my := mvy & (15 >> notSsVer)
		// Scale fractional part: dav1d passes mx << !ss_hor to the filter kernel.
		mx <<= notSsHor
		my <<= notSsVer

		if mx == 0 && my == 0 {
			// Integer-pel: simple copy.
			ibcCopyBlock(predU, nomChromaW, nomChromaH, td.frame.U, td.frame.StrideU, chromaSrcX, chromaSrcY, chromaRefW, chromaRefH)
			ibcCopyBlock(predV, nomChromaW, nomChromaH, td.frame.V, td.frame.StrideV, chromaSrcX, chromaSrcY, chromaRefW, chromaRefH)
		} else {
			// Sub-pixel: bilinear interpolation.
			ibcBilinearBlock(predU, nomChromaW, nomChromaH, td.frame.U, td.frame.StrideU, chromaSrcX, chromaSrcY, chromaRefW, chromaRefH, mx, my)
			ibcBilinearBlock(predV, nomChromaW, nomChromaH, td.frame.V, td.frame.StrideV, chromaSrcX, chromaSrcY, chromaRefW, chromaRefH, mx, my)
		}
	}

	// --- Coefficient decoding and reconstruction ---
	// IntraBC uses the same coefficient decoding as inter blocks (varTx tree).
	// Matches dav1d recon_b_inter() for IS_KEY_OR_INTRA case.
	if skip {
		td.setSkipCoeffCtx(miRow, miCol, bW, bH, hasChroma, subX, subY)
		td.writeInterPredToFrame(miRow, miCol, nomPixW, nomPixH, 0, predY)
		if hasChroma {
			td.writeInterPredToFrame(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 1, predU)
			td.writeInterPredToFrame(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 2, predV)
		}
	} else {
		yRes, uRes, vRes, cerr := td.decodeInterCoeffsVarTx(bc,
			miRow, miCol, bW, bH,
			varBlocks,
			nomPixW, nomPixH,
			chromaTxSz,
			nomChromaW, nomChromaH,
			chromaMiRow, chromaMiCol,
			hasChroma, subX, subY)
		if cerr != nil {
			yRes = nil
			uRes = nil
			vRes = nil
		}
		td.reconstructInterPlaneSpatial(miRow, miCol, nomPixW, nomPixH, 0, predY, yRes)
		if hasChroma {
			td.reconstructInterPlaneSpatial(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 1, predU, uRes)
			td.reconstructInterPlaneSpatial(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 2, predV, vRes)
		}
	}

	// --- Update context ---
	// IntraBC blocks are stored as non-intra with ref=-1 in the ModeInfo grid.
	// Matches dav1d splat_intrabc_mv: ref.ref={0,-1}, mv=actual_mv.
	info := ModeInfo{
		IsIntra:  false,
		RefFrame: [2]int8{ibcRef, -1},
		MV:       [2]MV{mv, {}},
		Mode:     NEWMV,
		Skip:     skip,
		Filter:   [2]uint8{3, 3},
		BW4:      uint8(bW),
		BH4:      uint8(bH),
	}
	td.setInterModeInfo(miRow, miCol, bW, bH, info)

	// Deblock info: IntraBC is treated like inter for deblocking.
	if td.deblockInfo != nil {
		blockUvTxW, blockUvTxH := TxSizeDimensions(chromaTxSz)
		if blockUvTxW < 4 {
			blockUvTxW = 4
		}
		if blockUvTxH < 4 {
			blockUvTxH = 4
		}
		for _, blk := range varBlocks {
			blkTxW, blkTxH := TxSizeDimensions(blk.txSz)
			if blkTxW <= 0 {
				blkTxW = 4
			}
			if blkTxH <= 0 {
				blkTxH = 4
			}
			blkW4 := max(blkTxW>>2, 1)
			blkH4 := max(blkTxH>>2, 1)
			dInfo := DeblockInfo{
				IsInter:        true,
				RefFrame:       ibcRef,
				Skip:           skip,
				TxW:            blkTxW,
				TxH:            blkTxH,
				UvTxW:          blockUvTxW,
				UvTxH:          blockUvTxH,
				DeltaLF:        td.lastDeltaLF,
				CodingBlockCol: miCol,
				CodingBlockRow: miRow,
			}
			for r := blk.miRow; r < blk.miRow+blkH4 && r < len(td.deblockInfo); r++ {
				for c := blk.miCol; c < blk.miCol+blkW4 && c < len(td.deblockInfo[r]); c++ {
					td.deblockInfo[r][c] = dInfo
				}
			}
		}
	}

	// Context updates matching dav1d decode.c lines 1361-1379.
	// tx_intra: block dim log2 (set via set_ctx in dav1d).
	// mode: DC_PRED, pal_sz: 0, skip_mode: 0, intra: 0 (not intra), skip: b->skip.
	// Note: ref[], filter[], comp_type are NOT set for IntraBC in key frames.

	// Update UV modes to DC_PRED for IntraBC.
	if hasChroma {
		cbx4 := miCol >> subX
		cby4 := miRow >> subY
		cbw4 := (bW + subX) >> subX
		cbh4 := (bH + subY) >> subY
		for c := cbx4; c < cbx4+cbw4 && c < len(td.aboveUVModes); c++ {
			td.aboveUVModes[c] = DC_PRED
		}
		for r := cby4; r < cby4+cbh4 && r < len(td.leftUVModes); r++ {
			td.leftUVModes[r] = DC_PRED
		}
	}

	// Update block context: mode=DC_PRED, intra=false, skip, pal_sz=0, skip_mode=0.
	// tx_intra is set to block dim log2 (dav1d: b_dim[2+i]).
	// Partition context must also be updated, matching setModeInfo for regular blocks.
	lwBlock := floorLog2(bW)
	lhBlock := floorLog2(bH)
	aboveCtx := uint8((0x1F << uint(ilog2(bW))) & 0x1F)
	leftCtx := uint8((0x1F << uint(ilog2(bH))) & 0x1F)
	if td.partCtxAbove >= 0 {
		aboveCtx = uint8(td.partCtxAbove)
		td.partCtxAbove = -1
	}
	if td.partCtxLeft >= 0 {
		leftCtx = uint8(td.partCtxLeft)
		td.partCtxLeft = -1
	}
	for c := miCol; c < miCol+bW && c < len(td.aboveModes); c++ {
		td.aboveModes[c] = DC_PRED
		if c < len(td.aboveTxW) {
			td.aboveTxW[c] = lwBlock
		}
		if c < len(td.aboveSkip) {
			td.aboveSkip[c] = skip
		}
		if c < len(td.abovePartCtx) {
			td.abovePartCtx[c] = aboveCtx
		}
	}
	for r := miRow; r < miRow+bH && r < len(td.leftModes); r++ {
		td.leftModes[r] = DC_PRED
		if r < len(td.leftTxH) {
			td.leftTxH[r] = lhBlock
		}
		if r < len(td.leftSkip) {
			td.leftSkip[r] = skip
		}
		if r < len(td.leftPartCtx) {
			td.leftPartCtx[r] = leftCtx
		}
	}

	return nil
}

// ibcCopyBlock copies a block of pixels from a source buffer (integer-pel).
// refW and refH are the reference bounds (dav1d uses f->bw*4/f->bh*4 for IntraBC,
// not the buffer dimensions). Clamps to [0, refW-1] x [0, refH-1].
func ibcCopyBlock(dst []byte, w, h int, src []byte, stride, srcX, srcY, refW, refH int) {
	for dy := 0; dy < h; dy++ {
		sy := srcY + dy
		if sy < 0 {
			sy = 0
		} else if sy >= refH {
			sy = refH - 1
		}
		for dx := 0; dx < w; dx++ {
			sx := srcX + dx
			if sx < 0 {
				sx = 0
			} else if sx >= refW {
				sx = refW - 1
			}
			dst[dy*w+dx] = src[sy*stride+sx]
		}
	}
}

// ibcBilinearBlock performs bilinear sub-pixel interpolation for IntraBC chroma.
// mx/my are fractional components in 1/16-pel units (0..15).
// Matches dav1d mc[FILTER_2D_BILINEAR] kernel: ((16-f)*p0 + f*p1 + 8) >> 4.
// For 2D bilinear: first interpolate horizontally, then vertically.
func ibcBilinearBlock(dst []byte, w, h int, src []byte, stride, srcX, srcY, refW, refH, mx, my int) {
	// Helper to clamp-read a source pixel.
	getSrc := func(x, y int) int {
		if x < 0 {
			x = 0
		} else if x >= refW {
			x = refW - 1
		}
		if y < 0 {
			y = 0
		} else if y >= refH {
			y = refH - 1
		}
		if y*stride+x >= len(src) {
			return 0
		}
		return int(src[y*stride+x])
	}

	if mx != 0 && my != 0 {
		// 2D bilinear: H then V.
		// Intermediate buffer: (w) x (h+1) after horizontal filtering.
		tmp := make([]int, w*(h+1))
		for ty := 0; ty < h+1; ty++ {
			for tx := 0; tx < w; tx++ {
				p0 := getSrc(srcX+tx, srcY+ty)
				p1 := getSrc(srcX+tx+1, srcY+ty)
				tmp[ty*w+tx] = (16-mx)*p0 + mx*p1
			}
		}
		// Vertical pass on intermediate.
		for dy := 0; dy < h; dy++ {
			for dx := 0; dx < w; dx++ {
				t0 := tmp[dy*w+dx]
				t1 := tmp[(dy+1)*w+dx]
				val := ((16-my)*t0 + my*t1 + 128) >> 8
				if val < 0 {
					val = 0
				} else if val > 255 {
					val = 255
				}
				dst[dy*w+dx] = byte(val)
			}
		}
	} else if mx != 0 {
		// Horizontal-only bilinear.
		for dy := 0; dy < h; dy++ {
			for dx := 0; dx < w; dx++ {
				p0 := getSrc(srcX+dx, srcY+dy)
				p1 := getSrc(srcX+dx+1, srcY+dy)
				val := ((16-mx)*p0 + mx*p1 + 8) >> 4
				if val < 0 {
					val = 0
				} else if val > 255 {
					val = 255
				}
				dst[dy*w+dx] = byte(val)
			}
		}
	} else {
		// Vertical-only bilinear.
		for dy := 0; dy < h; dy++ {
			for dx := 0; dx < w; dx++ {
				p0 := getSrc(srcX+dx, srcY+dy)
				p1 := getSrc(srcX+dx, srcY+dy+1)
				val := ((16-my)*p0 + my*p1 + 8) >> 4
				if val < 0 {
					val = 0
				} else if val > 255 {
					val = 255
				}
				dst[dy*w+dx] = byte(val)
			}
		}
	}
}
