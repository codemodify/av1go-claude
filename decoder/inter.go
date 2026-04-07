// Package decoder implements AV1 bitstream decoding.
//
// This file implements inter-frame prediction: reference frame selection,
// inter mode parsing, MV candidate list construction, MV decoding, and
// context derivation for inter blocks.
// AV1 spec Sections 5.11.6, 5.11.9-5.11.12, 5.11.32-5.11.35, 6.10.
package decoder

import (
	"fmt"
)

// Inter prediction mode constants. AV1 spec Section 6.10.
// Single reference modes.
const (
	NEARESTMV = 0
	NEARMV    = 1
	GLOBALMV  = 2
	NEWMV     = 3
)

// Compound reference modes (pairs). AV1 spec Section 6.10.
const (
	NEAREST_NEARESTMV = 0
	NEAR_NEARMV       = 1
	NEAREST_NEWMV     = 2
	NEW_NEARESTMV     = 3
	NEAR_NEWMV        = 4
	NEW_NEARMV        = 5
	GLOBALMV_GLOBALMV = 6
	NEWMV_NEWMV       = 7
)

// DRL index constants matching dav1d.
const (
	NEAREST_DRL  = 0
	NEARER_DRL   = 1
	NEAR_DRL     = 2
	NEARISH_DRL  = 3
)

// Compound type constants.
const (
	COMP_INTER_NONE         = 0
	COMP_INTER_WEIGHTED_AVG = 1
	COMP_INTER_AVG          = 2
	COMP_INTER_SEG          = 3
	COMP_INTER_WEDGE        = 4
)

// Global motion type constants.
const (
	GM_IDENTITY    = 0
	GM_TRANSLATION = 1
	GM_ROTZOOM     = 2
	GM_AFFINE      = 3
)

// Motion mode constants.
const (
	MM_TRANSLATION = 0
	MM_OBMC        = 1
	MM_WARP        = 2
)

// ModeInfo stores per-4x4 block information needed for MV prediction
// and inter context derivation.
type ModeInfo struct {
	IsIntra    bool
	RefFrame   [2]int8   // 0-6 = LAST..ALTREF (dav1d convention), -1 = none
	MV         [2]MV     // motion vectors in 1/8 pel
	Mode       uint8     // inter mode (NEARESTMV, NEARMV, GLOBALMV, NEWMV)
	CompType   uint8     // COMP_INTER_NONE, COMP_INTER_AVG, etc.
	Skip       bool
	SkipMode   bool
	Filter     [2]uint8  // interpolation filter [h, v]
	BW4        uint8     // block width in 4-pixel MI units
	BH4        uint8     // block height in 4-pixel MI units
	MotionMode uint8     // 0=SIMPLE, 1=OBMC, 2=WARP
	IIType     uint8     // 0=none, 1=smooth interintra, 2=wedge interintra
	// MF packs two flags matching dav1d's refmvs_block.mf:
	//   bit 0: GLOBALMV substitution flag (1 if block uses GLOBALMV/GLOBALMV_GLOBALMV
	//          AND min(bw4,bh4)>=2 for single, always for compound GLOBALMV_GLOBALMV)
	//   bit 1: has-NEWMV flag (1 if mode is NEWMV or compound NEW variant)
	MF uint8
}

// initModeInfoArray creates a ModeInfo slice with dav1d-matching defaults:
// RefFrame={-1,-1} (no reference), Filter={3,3} (DAV1D_N_SWITCHABLE_FILTERS).
func initModeInfoArray(n int) []ModeInfo {
	arr := make([]ModeInfo, n)
	for i := range arr {
		arr[i].RefFrame = [2]int8{-1, -1}
		arr[i].Filter = [2]uint8{3, 3}
	}
	return arr
}

// MV represents a motion vector in 1/8 pel units.
type MV struct {
	Row int32
	Col int32
}

// RefMVCandidate is an entry in the MV candidate stack.
type RefMVCandidate struct {
	MV     [2]MV // motion vectors for [ref0, ref1]
	Weight int32 // weight for DRL context
}

// compInterPredModes maps compound mode index to pair of single modes.
// Matches dav1d dav1d_comp_inter_pred_modes.
var compInterPredModes = [8][2]int{
	{NEARESTMV, NEARESTMV}, // NEAREST_NEARESTMV
	{NEARMV, NEARMV},       // NEAR_NEARMV
	{NEARESTMV, NEWMV},     // NEAREST_NEWMV
	{NEWMV, NEARESTMV},     // NEW_NEARESTMV
	{NEARMV, NEWMV},        // NEAR_NEWMV
	{NEWMV, NEARMV},        // NEW_NEARMV
	{GLOBALMV, GLOBALMV},   // GLOBALMV_GLOBALMV
	{NEWMV, NEWMV},         // NEWMV_NEWMV
}

// readIsInter reads the is_inter flag. AV1 spec Section 5.11.6.
func (td *TileDecoder) readIsInter(bc *BoolReader, miRow, miCol int) (bool, error) {
	ctx := td.getIsInterContext(miRow, miCol)
	sym, err := bc.ReadSymbolBoolInt(td.cdf.IsInter[ctx])
	if err != nil {
		return false, fmt.Errorf("is_inter at (%d,%d): %w", miRow, miCol, err)
	}
	return sym == 1, nil
}

// getIsInterContext derives the context for is_inter CDF.
// Matches dav1d get_intra_ctx (intra=0 means inter=1).
func (td *TileDecoder) getIsInterContext(miRow, miCol int) int {
	haveTop := miRow > td.tileRowStart
	haveLeft := miCol > td.tileColStart

	if haveTop && haveLeft {
		aboveIntra := td.aboveIsIntra(miCol)
		leftIntra := td.leftIsIntra(miRow)
		if aboveIntra && leftIntra {
			return 3
		}
		if aboveIntra || leftIntra {
			return 1
		}
		return 0
	}
	if haveTop || haveLeft {
		if haveTop {
			if td.aboveIsIntra(miCol) {
				return 2
			}
			return 0
		}
		if td.leftIsIntra(miRow) {
			return 2
		}
		return 0
	}
	return 0
}

// aboveIsIntra returns whether the above neighbor at miCol is intra.
func (td *TileDecoder) aboveIsIntra(miCol int) bool {
	if miCol < len(td.aboveModeInfo) {
		return td.aboveModeInfo[miCol].IsIntra
	}
	return true
}

// leftIsIntra returns whether the left neighbor at miRow is intra.
func (td *TileDecoder) leftIsIntra(miRow int) bool {
	if miRow < len(td.leftModeInfo) {
		return td.leftModeInfo[miRow].IsIntra
	}
	return true
}

// readSingleRefFrames reads reference frame for single-ref inter mode.
// Returns the 0-based reference frame index (0=LAST, 1=LAST2, ..., 6=ALTREF).
// AV1 spec Section 5.11.11.
func (td *TileDecoder) readSingleRefFrames(bc *BoolReader, miRow, miCol int) (int8, error) {
	haveTop := miRow > td.tileRowStart
	haveLeft := miCol > td.tileColStart

	// Binary tree of decisions matching dav1d decode.c lines 1757-1789.
	// Level 0: bwd (>=4) vs fwd (<4)
	ctx1 := td.getRefCtx(haveTop, haveLeft, miRow, miCol)
	bit1, err := bc.ReadSymbolBoolInt(td.cdf.SingleRef[0][ctx1])
	if err != nil {
		return -1, fmt.Errorf("single_ref[0] at (%d,%d): %w", miRow, miCol, err)
	}
	if bit1 == 1 {
		// Backward references (4, 5, 6) = (BWDREF, ALTREF2, ALTREF)
		ctx2 := td.getBwdRefCtx(haveTop, haveLeft, miRow, miCol)
		bit2, err := bc.ReadSymbolBoolInt(td.cdf.SingleRef[1][ctx2])
		if err != nil {
			return -1, fmt.Errorf("single_ref[1] at (%d,%d): %w", miRow, miCol, err)
		}
		if bit2 == 1 {
			return 6, nil // ALTREF
		}
		// BWDREF or ALTREF2
		ctx3 := td.getBwdRef1Ctx(haveTop, haveLeft, miRow, miCol)
		bit3, err := bc.ReadSymbolBoolInt(td.cdf.SingleRef[5][ctx3])
		if err != nil {
			return -1, fmt.Errorf("single_ref[5] at (%d,%d): %w", miRow, miCol, err)
		}
		return int8(4 + bit3), nil // 4=BWDREF, 5=ALTREF2
	}

	// Forward references (0, 1, 2, 3) = (LAST, LAST2, LAST3, GOLDEN)
	ctx2 := td.getFwdRefCtx(haveTop, haveLeft, miRow, miCol)
	bit2, err := bc.ReadSymbolBoolInt(td.cdf.SingleRef[2][ctx2])
	if err != nil {
		return -1, fmt.Errorf("single_ref[2] at (%d,%d): %w", miRow, miCol, err)
	}
	if bit2 == 1 {
		// LAST3 or GOLDEN
		ctx3 := td.getFwdRef2Ctx(haveTop, haveLeft, miRow, miCol)
		bit3, err := bc.ReadSymbolBoolInt(td.cdf.SingleRef[4][ctx3])
		if err != nil {
			return -1, fmt.Errorf("single_ref[4] at (%d,%d): %w", miRow, miCol, err)
		}
		return int8(2 + bit3), nil // 2=LAST3, 3=GOLDEN
	}

	// LAST or LAST2
	ctx3 := td.getFwdRef1Ctx(haveTop, haveLeft, miRow, miCol)
	bit3, err := bc.ReadSymbolBoolInt(td.cdf.SingleRef[3][ctx3])
	if err != nil {
		return -1, fmt.Errorf("single_ref[3] at (%d,%d): %w", miRow, miCol, err)
	}
	return int8(bit3), nil // 0=LAST, 1=LAST2
}

// readCompRefFrames reads compound reference frames.
// Returns two 0-based reference frame indices.
// AV1 spec Section 5.11.10.
func (td *TileDecoder) readCompRefFrames(bc *BoolReader, miRow, miCol int) (int8, int8, error) {
	haveTop := miRow > td.tileRowStart
	haveLeft := miCol > td.tileColStart

	// Reference type: bidir vs unidir
	dirCtx := td.getCompDirCtx(haveTop, haveLeft, miRow, miCol)
	isBidir, err := bc.ReadSymbolBoolInt(td.cdf.CompReferenceType[dirCtx])
	if err != nil {
		return -1, -1, fmt.Errorf("comp_reference_type at (%d,%d): %w", miRow, miCol, err)
	}

	if isBidir == 1 {
		// Bidir compound: forward ref + backward ref
		ctx1 := td.getFwdRefCtx(haveTop, haveLeft, miRow, miCol)
		bit1, err := bc.ReadSymbolBoolInt(td.cdf.CompRef[0][ctx1])
		if err != nil {
			return -1, -1, fmt.Errorf("comp_ref[0] at (%d,%d): %w", miRow, miCol, err)
		}
		var ref0 int8
		if bit1 == 1 {
			// LAST3 or GOLDEN
			ctx2 := td.getFwdRef2Ctx(haveTop, haveLeft, miRow, miCol)
			bit2, err := bc.ReadSymbolBoolInt(td.cdf.CompRef[2][ctx2])
			if err != nil {
				return -1, -1, fmt.Errorf("comp_ref[2] at (%d,%d): %w", miRow, miCol, err)
			}
			ref0 = int8(2 + bit2)
		} else {
			// LAST or LAST2
			ctx2 := td.getFwdRef1Ctx(haveTop, haveLeft, miRow, miCol)
			bit2, err := bc.ReadSymbolBoolInt(td.cdf.CompRef[1][ctx2])
			if err != nil {
				return -1, -1, fmt.Errorf("comp_ref[1] at (%d,%d): %w", miRow, miCol, err)
			}
			ref0 = int8(bit2)
		}

		// Backward ref — uses CompBwdRef (separate from SingleRef)
		ctx3 := td.getBwdRefCtx(haveTop, haveLeft, miRow, miCol)
		bit3, err := bc.ReadSymbolBoolInt(td.cdf.CompBwdRef[0][ctx3])
		if err != nil {
			return -1, -1, fmt.Errorf("comp_bwd_ref[0] at (%d,%d): %w", miRow, miCol, err)
		}
		var ref1 int8
		if bit3 == 1 {
			ref1 = 6 // ALTREF
		} else {
			ctx4 := td.getBwdRef1Ctx(haveTop, haveLeft, miRow, miCol)
			bit4, err := bc.ReadSymbolBoolInt(td.cdf.CompBwdRef[1][ctx4])
			if err != nil {
				return -1, -1, fmt.Errorf("comp_bwd_ref[1] at (%d,%d): %w", miRow, miCol, err)
			}
			ref1 = int8(4 + bit4)
		}
		return ref0, ref1, nil
	}

	// Unidir compound — uses CompUniRef (separate from SingleRef)
	uCtxP := td.getRefCtx(haveTop, haveLeft, miRow, miCol)
	uBit, err := bc.ReadSymbolBoolInt(td.cdf.CompUniRef[0][uCtxP])
	if err != nil {
		return -1, -1, fmt.Errorf("comp_uni_ref[0] at (%d,%d): %w", miRow, miCol, err)
	}
	if uBit == 1 {
		return 4, 6, nil // BWDREF + ALTREF
	}
	uCtxP1 := td.getUniP1Ctx(haveTop, haveLeft, miRow, miCol)
	uBit1, err := bc.ReadSymbolBoolInt(td.cdf.CompUniRef[1][uCtxP1])
	if err != nil {
		return -1, -1, fmt.Errorf("comp_uni_ref[1] at (%d,%d): %w", miRow, miCol, err)
	}
	ref1 := int8(1 + uBit1)
	if ref1 == 2 {
		uCtxP2 := td.getFwdRef2Ctx(haveTop, haveLeft, miRow, miCol)
		uBit2, err := bc.ReadSymbolBoolInt(td.cdf.CompUniRef[2][uCtxP2])
		if err != nil {
			return -1, -1, fmt.Errorf("comp_uni_ref[2] at (%d,%d): %w", miRow, miCol, err)
		}
		ref1 += int8(uBit2)
	}
	return 0, ref1, nil
}

// readCompoundMode reads the compound inter mode. AV1 spec Section 5.11.12.
func (td *TileDecoder) readCompoundMode(bc *BoolReader, ctx int) (int, error) {
	mode, err := bc.ReadSymbol(td.cdf.CompoundMode[ctx], 8)
	if err != nil {
		return 0, fmt.Errorf("compound_mode: %w", err)
	}
	return mode, nil
}

// readInterMode reads the single-reference inter mode using the mode tree.
// Returns mode and DRL index. AV1 spec Section 5.11.12.
func (td *TileDecoder) readInterMode(bc *BoolReader, ctx int, nMVs int, mvstack []RefMVCandidate) (int, int, error) {
	// NewMV flag
	newMVBit, err := bc.ReadSymbolBoolInt(td.cdf.NewMV[ctx&7])
	if err != nil {
		return 0, 0, fmt.Errorf("newmv_mode: %w", err)
	}

	if newMVBit == 1 {
		// Not NEWMV — check GLOBALMV
		globMVBit, err := bc.ReadSymbolBoolInt(td.cdf.ZeroMV[(ctx>>3)&1])
		if err != nil {
			return 0, 0, fmt.Errorf("globalmv_mode: %w", err)
		}
		if globMVBit == 0 {
			return GLOBALMV, 0, nil
		}
		// Not GLOBALMV — check REFMV
		refMVBit, err := bc.ReadSymbolBoolInt(td.cdf.RefMV[(ctx>>4)&15])
		if err != nil {
			return 0, 0, fmt.Errorf("refmv_mode: %w", err)
		}
		if refMVBit == 1 {
			// NEARMV with DRL
			drlIdx := NEARER_DRL
			if nMVs > 2 {
				drlCtx := getDRLContext(mvstack, 1)
				drlBit, err := bc.ReadSymbolBoolInt(td.cdf.DrlMode[drlCtx])
				if err != nil {
					return 0, 0, fmt.Errorf("drl_mode: %w", err)
				}
				drlIdx += drlBit
				if drlIdx == NEAR_DRL && nMVs > 3 {
					drlCtx2 := getDRLContext(mvstack, 2)
					drlBit2, err := bc.ReadSymbolBoolInt(td.cdf.DrlMode[drlCtx2])
					if err != nil {
						return 0, 0, fmt.Errorf("drl_mode: %w", err)
					}
					drlIdx += drlBit2
				}
			}
			return NEARMV, drlIdx, nil
		}
		// NEARESTMV
		return NEARESTMV, NEAREST_DRL, nil
	}

	// NEWMV with DRL
	drlIdx := NEAREST_DRL
	if nMVs > 1 {
		drlCtx := getDRLContext(mvstack, 0)
		drlBit, err := bc.ReadSymbolBoolInt(td.cdf.DrlMode[drlCtx])
		if err != nil {
			return 0, 0, fmt.Errorf("drl_mode: %w", err)
		}
		drlIdx += drlBit
		if drlIdx == NEARER_DRL && nMVs > 2 {
			drlCtx2 := getDRLContext(mvstack, 1)
			drlBit2, err := bc.ReadSymbolBoolInt(td.cdf.DrlMode[drlCtx2])
			if err != nil {
				return 0, 0, fmt.Errorf("drl_mode: %w", err)
			}
			drlIdx += drlBit2
		}
	}
	return NEWMV, drlIdx, nil
}

// getDRLContext returns the DRL context for the given stack index.
// Matches dav1d env.h get_drl_context.
func getDRLContext(mvstack []RefMVCandidate, refIdx int) int {
	if refIdx+1 >= len(mvstack) {
		return 2
	}
	if mvstack[refIdx].Weight >= 640 {
		if mvstack[refIdx+1].Weight < 640 {
			return 1
		}
		return 0
	}
	if mvstack[refIdx+1].Weight < 640 {
		return 2
	}
	return 0
}

// readMVResidual reads the MV residual (delta) and adds it to the reference MV.
// AV1 spec Section 5.11.32.
func (td *TileDecoder) readMVResidual(bc *BoolReader, refMV *MV, mvPrec int) error {
	// Read MV joint
	joint, err := bc.ReadSymbol(td.cdf.MVJoint, 4)
	if err != nil {
		return fmt.Errorf("mv_joint: %w", err)
	}

	// dav1d levels.h: MV_JOINT_ZERO=0, MV_JOINT_H=1, MV_JOINT_V=2, MV_JOINT_HV=3
	// bit 1 (value 1) = H (x/col) component present
	// bit 2 (value 2) = V (y/row) component present
	// Row (y) is read first when both are present (comp[0]), matching dav1d.
	if joint&2 != 0 { // MV_JOINT_V: has row (y) component
		diff, err := td.readMVComponent(bc, 0, mvPrec) // comp=0 for row/y
		if err != nil {
			return fmt.Errorf("mv_row: %w", err)
		}
		refMV.Row += int32(diff)
	}
	if joint&1 != 0 { // MV_JOINT_H: has col (x) component
		diff, err := td.readMVComponent(bc, 1, mvPrec) // comp=1 for col/x
		if err != nil {
			return fmt.Errorf("mv_col: %w", err)
		}
		refMV.Col += int32(diff)
	}
	return nil
}

// readMVComponent reads one MV component (row or col).
// comp: 0=row, 1=col. mvPrec: -1=force_integer, 0=normal, 1=hp.
// AV1 spec Section 5.11.33-5.11.35.
func (td *TileDecoder) readMVComponent(bc *BoolReader, comp int, mvPrec int) (int, error) {
	sign, err := bc.ReadSymbolBoolInt(td.cdf.MVSign[comp])
	if err != nil {
		return 0, fmt.Errorf("mv_sign: %w", err)
	}

	cl, err := bc.ReadSymbol(td.cdf.MVClass[comp], 11)
	if err != nil {
		return 0, fmt.Errorf("mv_class: %w", err)
	}
	var up, fp, hp int
	fp = 3
	hp = 1

	if cl == 0 {
		// Class 0: read class0_bit
		c0bit, err := bc.ReadSymbolBoolInt(td.cdf.MVClass0Bit[comp])
		if err != nil {
			return 0, fmt.Errorf("mv_class0_bit: %w", err)
		}
		up = c0bit
		if mvPrec >= 0 { // !force_integer_mv
			fpSym, err := bc.ReadSymbol(td.cdf.MVClass0FR[comp][up], 4)
			if err != nil {
				return 0, fmt.Errorf("mv_class0_fr: %w", err)
			}
			fp = fpSym
			if mvPrec > 0 { // allow_high_precision_mv
				hpSym, err := bc.ReadSymbolBoolInt(td.cdf.MVClass0HP[comp])
				if err != nil {
					return 0, fmt.Errorf("mv_class0_hp: %w", err)
				}
				hp = hpSym
			}
		}
	} else {
		// Class > 0: read magnitude bits
		up = 1 << uint(cl)
		for n := 0; n < cl; n++ {
			bit, err := bc.ReadSymbolBoolInt(td.cdf.MVBit[comp][n])
			if err != nil {
				return 0, fmt.Errorf("mv_bit[%d]: %w", n, err)
			}
			up |= bit << uint(n)
		}
		if mvPrec >= 0 { // !force_integer_mv
			fpSym, err := bc.ReadSymbol(td.cdf.MVFR[comp], 4)
			if err != nil {
				return 0, fmt.Errorf("mv_fr: %w", err)
			}
			fp = fpSym
			if mvPrec > 0 { // allow_high_precision_mv
				hpSym, err := bc.ReadSymbolBoolInt(td.cdf.MVHP[comp])
				if err != nil {
					return 0, fmt.Errorf("mv_hp: %w", err)
				}
				hp = hpSym
			}
		}
	}

	diff := ((up << 3) | (fp << 1) | hp) + 1
	if sign == 1 {
		diff = -diff
	}
	return diff, nil
}

// fixMVPrecision reduces MV precision to match frame settings.
// Matches dav1d fix_mv_precision.
func fixMVPrecision(fh *DecodedFrameHeader, mv *MV) {
	if fh.ForceIntegerMV {
		// Matches dav1d fix_int_mv_precision:
		//   mv->x = (mv->x - (mv->x >> 15) + 3) & ~7;
		// This rounds toward zero to integer pel.
		mv.Row = (mv.Row - (mv.Row >> 31) + 3) & ^int32(7)
		mv.Col = (mv.Col - (mv.Col >> 31) + 3) & ^int32(7)
	} else if !fh.AllowHighPrecisionMV {
		// Matches dav1d fix_mv_precision for !hp:
		//   mv->x = (mv->x - (mv->x >> 15)) & ~1;
		// This rounds toward zero to 1/4 pel.
		mv.Row = (mv.Row - (mv.Row >> 31)) & ^int32(1)
		mv.Col = (mv.Col - (mv.Col >> 31)) & ^int32(1)
	}
}

// findMVStack builds the MV candidate list from spatial neighbors.
// Matches dav1d's dav1d_refmvs_find() algorithm from refmvs.c with
// block-size-dependent weights, scan_row/scan_col block-stepping,
// +640 nearest boost, top-left corner, secondary row/col scans,
// single-ref extended candidates, sorting, and MV clamping.
// Returns the candidate stack, number of candidates, and the mode context.
// AV1 spec Section 6.10.7 and dav1d refmvs.c dav1d_refmvs_find.
func (td *TileDecoder) findMVStack(miRow, miCol, bW, bH int, ref0, ref1 int8, edgeFlags uint8) ([]RefMVCandidate, int, int) {
	mvstack := make([]RefMVCandidate, 8)
	nMVs := 0
	isComp := ref1 >= 0

	// Clamp effective scan dimensions to tile boundaries, max 16.
	w4 := bW
	if w4 > 16 {
		w4 = 16
	}
	if miCol+w4 > td.tileColEnd {
		w4 = td.tileColEnd - miCol
	}
	h4 := bH
	if h4 > 16 {
		h4 = 16
	}
	if miRow+h4 > td.tileRowEnd {
		h4 = td.tileRowEnd - miRow
	}

	// Compute tgmv (global MV used to fill empty slots) and gmv (for GLOBALMV substitution).
	// dav1d: gmv[i] = frm_hdr->gmv[ref-1].type > TRANSLATION ? tgmv[i] : INVALID_MV
	// INVALID_MV means "no substitution".
	tgmv0 := td.getGlobalMV(ref0, miRow, miCol, bW, bH)
	var tgmv1 MV
	if isComp {
		tgmv1 = td.getGlobalMV(ref1, miRow, miCol, bW, bH)
	}
	// gmv0/gmv1: used for GLOBALMV substitution in add_spatial_candidate.
	// Valid (non-nil) only when the reference uses ROTZOOM or AFFINE global motion.
	var gmv0Valid, gmv1Valid bool
	var gmv0, gmv1 MV
	if ref0 >= 0 && int(ref0) < len(td.fh.GmType) && td.fh.GmType[ref0] > 1 {
		gmv0Valid = true
		gmv0 = tgmv0
	}
	if isComp && ref1 >= 0 && int(ref1) < len(td.fh.GmType) && td.fh.GmType[ref1] > 1 {
		gmv1Valid = true
		gmv1 = tgmv1
	}

	// ---------------------------------------------------------------
	// Helper: add/merge a spatial candidate into mvstack.
	// Sets have_refmv_match and have_newmv_match flags.
	// Implements dav1d's GLOBALMV substitution: when neighbor uses GLOBALMV
	// (info.MF & 1) and the reference's global motion is ROTZOOM/AFFINE,
	// the candidate MV is replaced with the current block's global MV.
	// ---------------------------------------------------------------
	addSpatialCandidate := func(info ModeInfo, weight int32, haveNewMV, haveRefMVMatch *int) {
		if info.IsIntra {
			return
		}
		if !isComp {
			// Single reference.
			for n := 0; n < 2; n++ {
				if info.RefFrame[n] == ref0 {
					// GLOBALMV substitution: if neighbor uses GLOBALMV (MF bit 0)
					// and current ref has ROTZOOM/AFFINE, use current block's GMV.
					candMV := info.MV[n]
					if (info.MF&1) != 0 && gmv0Valid {
						candMV = gmv0
					}
					*haveRefMVMatch = 1
					if info.MF&2 != 0 {
						*haveNewMV = 1
					}
					// Check for duplicate.
					for m := 0; m < nMVs; m++ {
						if mvstack[m].MV[0] == candMV {
							mvstack[m].Weight += weight
							return
						}
					}
					if nMVs < 8 {
						mvstack[nMVs].MV[0] = candMV
						mvstack[nMVs].Weight = weight
						nMVs++
					}
					return
				}
			}
		} else {
			// Compound reference.
			if info.RefFrame[0] == ref0 && info.RefFrame[1] == ref1 {
				candMV0 := info.MV[0]
				candMV1 := info.MV[1]
				// GLOBALMV substitution for compound.
				if (info.MF & 1) != 0 {
					if gmv0Valid {
						candMV0 = gmv0
					}
					if gmv1Valid {
						candMV1 = gmv1
					}
				}
				*haveRefMVMatch = 1
				if info.MF&2 != 0 {
					*haveNewMV = 1
				}
				for m := 0; m < nMVs; m++ {
					if mvstack[m].MV[0] == candMV0 && mvstack[m].MV[1] == candMV1 {
						mvstack[m].Weight += weight
						return
					}
				}
				if nMVs < 8 {
					mvstack[nMVs].MV[0] = candMV0
					mvstack[nMVs].MV[1] = candMV1
					mvstack[nMVs].Weight = weight
					nMVs++
				}
			}
		}
	}

	// ---------------------------------------------------------------
	// scan_row: scan the above row with block-stepping.
	// Returns n_rows contribution (weight_factor >> 1 or 1).
	// ---------------------------------------------------------------
	scanRow := func(rowMI int, maxRows, step int, useAboveCtx bool, haveNewMV, haveRowMVs *int) int {
		// Get the first candidate block.
		var firstInfo ModeInfo
		if useAboveCtx {
			firstInfo = td.getAboveModeInfo(miCol)
		} else if td.miGrid != nil {
			firstInfo = td.miGrid.Get(rowMI, miCol)
		} else {
			return 1
		}

		candBW4 := int(firstInfo.BW4)
		if candBW4 < 1 {
			candBW4 = 1
		}
		candBH4 := int(firstInfo.BH4)
		if candBH4 < 1 {
			candBH4 = 1
		}

		// len = max(step, min(bw4, cand_bw4))
		minBWCand := bW
		if candBW4 < minBWCand {
			minBWCand = candBW4
		}
		lenVal := step
		if minBWCand > lenVal {
			lenVal = minBWCand
		}

		if bW <= candBW4 {
			// Single call with computed weight factor.
			var weightFactor int
			if bW == 1 {
				weightFactor = 2
			} else {
				wf := 2 * maxRows
				if candBH4 < wf {
					wf = candBH4
				}
				if wf < 2 {
					wf = 2
				}
				weightFactor = wf
			}
			addSpatialCandidate(firstInfo, int32(lenVal*weightFactor), haveNewMV, haveRowMVs)
			return weightFactor >> 1
		}

		// bW > candBW4: step through sub-blocks.
		addSpatialCandidate(firstInfo, int32(lenVal*2), haveNewMV, haveRowMVs)
		for x := lenVal; x < w4; {
			var info ModeInfo
			col := miCol + x
			if useAboveCtx {
				info = td.getAboveModeInfo(col)
			} else if td.miGrid != nil {
				info = td.miGrid.Get(rowMI, col)
			} else {
				break
			}
			candBW4 = int(info.BW4)
			if candBW4 < 1 {
				candBW4 = 1
			}
			lenVal = step
			if candBW4 > lenVal {
				lenVal = candBW4
			}
			addSpatialCandidate(info, int32(lenVal*2), haveNewMV, haveRowMVs)
			x += lenVal
		}
		return 1
	}

	// ---------------------------------------------------------------
	// scan_col: scan the left column with block-stepping.
	// Returns n_cols contribution (weight_factor >> 1 or 1).
	// ---------------------------------------------------------------
	scanCol := func(colMI int, maxCols, step int, useLeftCtx bool, haveNewMV, haveColMVs *int) int {
		// Get the first candidate block.
		var firstInfo ModeInfo
		if useLeftCtx {
			firstInfo = td.getLeftModeInfo(miRow)
		} else if td.miGrid != nil {
			firstInfo = td.miGrid.Get(miRow, colMI)
		} else {
			return 1
		}

		candBH4 := int(firstInfo.BH4)
		if candBH4 < 1 {
			candBH4 = 1
		}
		candBW4 := int(firstInfo.BW4)
		if candBW4 < 1 {
			candBW4 = 1
		}

		// len = max(step, min(bh4, cand_bh4))
		minBHCand := bH
		if candBH4 < minBHCand {
			minBHCand = candBH4
		}
		lenVal := step
		if minBHCand > lenVal {
			lenVal = minBHCand
		}

		if bH <= candBH4 {
			// Single call with computed weight factor.
			// Note: dav1d uses cand_bw4 (width) for column scan weight.
			var weightFactor int
			if bH == 1 {
				weightFactor = 2
			} else {
				wf := 2 * maxCols
				if candBW4 < wf {
					wf = candBW4
				}
				if wf < 2 {
					wf = 2
				}
				weightFactor = wf
			}
			addSpatialCandidate(firstInfo, int32(lenVal*weightFactor), haveNewMV, haveColMVs)
			return weightFactor >> 1
		}

		// bH > candBH4: step through sub-blocks.
		addSpatialCandidate(firstInfo, int32(lenVal*2), haveNewMV, haveColMVs)
		for y := lenVal; y < h4; {
			var info ModeInfo
			row := miRow + y
			if useLeftCtx {
				info = td.getLeftModeInfo(row)
			} else if td.miGrid != nil {
				info = td.miGrid.Get(row, colMI)
			} else {
				break
			}
			candBH4 = int(info.BH4)
			if candBH4 < 1 {
				candBH4 = 1
			}
			lenVal = step
			if candBH4 > lenVal {
				lenVal = candBH4
			}
			addSpatialCandidate(info, int32(lenVal*2), haveNewMV, haveColMVs)
			y += lenVal
		}
		return 1
	}

	// ---------------------------------------------------------------
	// Pass 1: Nearest above row (scan_row)
	// ---------------------------------------------------------------
	haveNewMV := 0
	haveRowMVs := 0
	haveColMVs := 0

	var maxRows int
	nRows := ^uint(0) // ~0U sentinel meaning "no above row scanned"
	if miRow > td.tileRowStart {
		maxRows = (miRow - td.tileRowStart + 1) >> 1
		maxLimit := 2
		if bH > 1 {
			maxLimit = 3
		}
		if maxRows > maxLimit {
			maxRows = maxLimit
		}
		rowStep := 1
		if bW >= 16 {
			rowStep = 4
		}
		nRows = uint(scanRow(miRow-1, maxRows, rowStep, true, &haveNewMV, &haveRowMVs))
	}

	// ---------------------------------------------------------------
	// Pass 2: Nearest left column (scan_col)
	// ---------------------------------------------------------------
	var maxCols int
	nCols := ^uint(0) // ~0U sentinel meaning "no left col scanned"
	if miCol > td.tileColStart {
		maxCols = (miCol - td.tileColStart + 1) >> 1
		maxLimit := 2
		if bW > 1 {
			maxLimit = 3
		}
		if maxCols > maxLimit {
			maxCols = maxLimit
		}
		colStep := 1
		if bH >= 16 {
			colStep = 4
		}
		nCols = uint(scanCol(miCol-1, maxCols, colStep, true, &haveNewMV, &haveColMVs))
	}

	// ---------------------------------------------------------------
	// Pass 3: Top-right corner
	// ---------------------------------------------------------------
	if nRows != ^uint(0) && (edgeFlags&EdgeI444TopHasRight) != 0 {
		maxDim := bW
		if bH > maxDim {
			maxDim = bH
		}
		if maxDim <= 16 && miCol+bW < td.tileColEnd {
			var info ModeInfo
			if td.miGrid != nil {
				info = td.miGrid.Get(miRow-1, miCol+bW)
			} else {
				info = td.getAboveModeInfo(miCol + bW)
			}
			addSpatialCandidate(info, 4, &haveNewMV, &haveRowMVs)
		}
	}

	// ---------------------------------------------------------------
	// +640 nearest boost
	// ---------------------------------------------------------------
	nearestMatch := haveColMVs + haveRowMVs
	nearestCnt := nMVs
	for n := 0; n < nearestCnt; n++ {
		mvstack[n].Weight += 640
	}

	// ---------------------------------------------------------------
	// Temporal candidates
	// dav1d refmvs.c: scan projected temporal MVs at 8x8 resolution.
	// The first valid temporal candidate overrides globalmv_ctx.
	// Temporal candidates also add MVs to the stack, but that requires
	// correct MV projection through POC differences (mv_projection).
	// ---------------------------------------------------------------
	globMVCtx := 0
	if td.fh.UseRefFrameMVs {
		globMVCtx = 1
	}
	if td.projTMVs != nil && td.projStride > 0 && td.fh.UseRefFrameMVs {
		by8 := miRow >> 1
		bx8 := miCol >> 1
		w8 := (bW + 1) >> 1
		h8 := (bH + 1) >> 1
		if w8 > 8 {
			w8 = 8
		}
		if h8 > 8 {
			h8 = 8
		}
		stepH := 1
		if bW >= 16 {
			stepH = 2
		}
		stepV := 1
		if bH >= 16 {
			stepV = 2
		}
		maxTMV := len(td.projTMVs) / td.projStride

		// Compute POC differences for MV projection.
		// pocdiff[i] = clamped distance from current frame to reference i.
		// Matches dav1d rf->pocdiff[i] = clamp(get_poc_diff(poc, ref_poc[i]), -31, 31).
		orderHintBits := 0
		if td.sh.EnableOrderHint {
			orderHintBits = int(td.sh.OrderHintBitsMinus1) + 1
		}
		var pocdiff [7]int
		for i := 0; i < 7; i++ {
			slot := int(td.fh.RefFrameIdx[i])
			refOH := int(td.refOrderHints[slot])
			// dav1d: pocdiff[i] = iclip(get_poc_diff(poc, ref_poc[i]), -31, 31)
			// = current_poc - ref_poc, so past refs give negative, future positive.
			pd := getPocDiff(orderHintBits, int(td.fh.OrderHint), refOH)
			if pd < -31 {
				pd = -31
			} else if pd > 31 {
				pd = 31
			}
			pocdiff[i] = pd
		}

		for y := 0; y < h8; y += stepV {
			for x := 0; x < w8; x += stepH {
				ty := by8 + y
				tx := bx8 + x
				if ty < 0 || tx < 0 || ty >= maxTMV || tx >= td.projStride {
					continue
				}
				tmv := td.projTMVs[ty*td.projStride+tx]
				if tmv.Ref == 0 {
					continue
				}

				// Project temporal MV: mv_projection(mv, pocdiff[ref0], ref2ref)
				// tmv.Ref stores ref2ref (POC diff denominator from LoadTMVs).
				ref2ref := int(tmv.Ref)
				projMV := mvProjectionMV(tmv.MV, pocdiff[ref0], ref2ref)
				fixMVPrecision(td.fh, &projMV)

				// globalmv_ctx: only the first temporal *position* (x==0, y==0)
				// sets this, matching dav1d's !(x | y) NULL-pointer guard.
				// If that position is invalid (tmv.Ref==0), globmv_ctx keeps
				// its initial value — it is NOT overridden by a later valid TMV.
				if x == 0 && y == 0 && !isComp {
					diffRow := projMV.Row - tgmv0.Row
					diffCol := projMV.Col - tgmv0.Col
					if diffRow < 0 {
						diffRow = -diffRow
					}
					if diffCol < 0 {
						diffCol = -diffCol
					}
					if diffRow|diffCol >= 16 {
						globMVCtx = 1
					} else {
						globMVCtx = 0
					}
				}

				// Add temporal candidate to stack with weight=2.
				if !isComp {
					dup := false
					for m := 0; m < nMVs; m++ {
						if mvstack[m].MV[0] == projMV {
							mvstack[m].Weight += 2
							dup = true
							break
						}
					}
					if !dup && nMVs < 8 {
						mvstack[nMVs].MV[0] = projMV
						mvstack[nMVs].Weight = 2
						nMVs++
					}
				} else {
					// For compound, project to both refs.
					projMV1 := mvProjectionMV(tmv.MV, pocdiff[ref1], ref2ref)
					fixMVPrecision(td.fh, &projMV1)
					dup := false
					for m := 0; m < nMVs; m++ {
						if mvstack[m].MV[0] == projMV && mvstack[m].MV[1] == projMV1 {
							mvstack[m].Weight += 2
							dup = true
							break
						}
					}
					if !dup && nMVs < 8 {
						mvstack[nMVs].MV[0] = projMV
						mvstack[nMVs].MV[1] = projMV1
						mvstack[nMVs].Weight = 2
						nMVs++
					}
				}
			}
		}

		// Secondary temporal scans (corners) for small blocks.
		// dav1d: if min(bw4,bh4) >= 2 && max(bw4,bh4) < 16
		minDim := bW
		if bH < minDim {
			minDim = bH
		}
		maxDim := bW
		if bH > maxDim {
			maxDim = bH
		}
		if minDim >= 2 && maxDim < 16 {
			bh8 := (bH + 1) >> 1
			bw8 := (bW + 1) >> 1
			tileRowEnd8 := td.tileRowEnd >> 1
			sbRow8 := (by8 & ^7)
			maxRow8 := tileRowEnd8
			if sbRow8+8 < maxRow8 {
				maxRow8 = sbRow8 + 8
			}
			tileColEnd8 := td.tileColEnd >> 1
			sbCol8 := (bx8 & ^7)
			maxCol8 := tileColEnd8
			if sbCol8+8 < maxCol8 {
				maxCol8 = sbCol8 + 8
			}
			tileColStart8 := td.tileColStart >> 1

			hasBottom := by8+bh8 < maxRow8

			addTemporalCorner := func(ty, tx int) {
				if ty < 0 || tx < 0 || ty >= maxTMV || tx >= td.projStride {
					return
				}
				tmv := td.projTMVs[ty*td.projStride+tx]
				if tmv.Ref == 0 {
					return
				}
				r2r := int(tmv.Ref)
				pMV := mvProjectionMV(tmv.MV, pocdiff[ref0], r2r)
				fixMVPrecision(td.fh, &pMV)
				if !isComp {
					for m := 0; m < nMVs; m++ {
						if mvstack[m].MV[0] == pMV {
							mvstack[m].Weight += 2
							return
						}
					}
					if nMVs < 8 {
						mvstack[nMVs].MV[0] = pMV
						mvstack[nMVs].Weight = 2
						nMVs++
					}
				} else {
					pMV1 := mvProjectionMV(tmv.MV, pocdiff[ref1], r2r)
					fixMVPrecision(td.fh, &pMV1)
					for m := 0; m < nMVs; m++ {
						if mvstack[m].MV[0] == pMV && mvstack[m].MV[1] == pMV1 {
							mvstack[m].Weight += 2
							return
						}
					}
					if nMVs < 8 {
						mvstack[nMVs].MV[0] = pMV
						mvstack[nMVs].MV[1] = pMV1
						mvstack[nMVs].Weight = 2
						nMVs++
					}
				}
			}

			// Bottom-left
			if hasBottom && bx8-1 >= tileColStart8 && bx8-1 >= sbCol8 {
				addTemporalCorner(by8+bh8, bx8-1)
			}
			// Bottom-right
			if bx8+bw8 < maxCol8 {
				if hasBottom {
					addTemporalCorner(by8+bh8, bx8+bw8)
				}
				if by8+bh8-1 < maxRow8 {
					addTemporalCorner(by8+bh8-1, bx8+bw8)
				}
			}
		}
	}

	// ---------------------------------------------------------------
	// Pass 4: Top-left corner
	// ---------------------------------------------------------------
	if nRows != ^uint(0) || nCols != ^uint(0) {
		// b_top[-1] = position (miRow-1, miCol-1)
		if miRow > td.tileRowStart && miCol > td.tileColStart {
			var info ModeInfo
			if td.miGrid != nil {
				info = td.miGrid.Get(miRow-1, miCol-1)
			} else {
				// Use above context at miCol-1 as approximation.
				info = td.getAboveModeInfo(miCol - 1)
			}
			haveDummyNewMV := 0
			addSpatialCandidate(info, 4, &haveDummyNewMV, &haveRowMVs)
		}
	}

	// ---------------------------------------------------------------
	// Pass 5: Secondary rows/cols (n=2,3)
	// At 8x8 resolution: scan at odd MI positions with step=2.
	// ---------------------------------------------------------------
	if td.miGrid != nil {
		for n := 2; n <= 3; n++ {
			// Secondary above rows.
			if uint(n) > nRows && uint(n) <= uint(maxRows) {
				scanRowMI := ((miRow - 2*n + 1) | 1)
				if scanRowMI >= td.tileRowStart {
					rowStep := 2
					if bW >= 16 {
						rowStep = 4
					}
					scanColStart := miCol | 1
					// Use miGrid for secondary rows (not the above context array).
					// We scan starting from scanColStart.
					haveDummyNewMV := 0
					nr := td.scanRowSecondary(mvstack[:], &nMVs, isComp, ref0, ref1,
						scanRowMI, scanColStart, bW, w4, 1+maxRows-n, rowStep,
						&haveDummyNewMV, &haveRowMVs,
						gmv0Valid, gmv1Valid, gmv0, gmv1)
					nRows += uint(nr)
				}
			}

			// Secondary left cols.
			if uint(n) > nCols && uint(n) <= uint(maxCols) {
				scanColMI := ((miCol - 2*n + 1) | 1)
				if scanColMI >= td.tileColStart {
					colStep := 2
					if bH >= 16 {
						colStep = 4
					}
					scanRowStart := miRow | 1
					haveDummyNewMV := 0
					nc := td.scanColSecondary(mvstack[:], &nMVs, isComp, ref0, ref1,
						scanColMI, scanRowStart, bH, h4, 1+maxCols-n, colStep,
						&haveDummyNewMV, &haveColMVs,
						gmv0Valid, gmv1Valid, gmv0, gmv1)
					nCols += uint(nc)
				}
			}
		}
	}

	// ---------------------------------------------------------------
	// Sort: nearest candidates by weight (descending), then secondary.
	// Uses dav1d's "last exchange index" optimization.
	// ---------------------------------------------------------------
	// Sort nearest portion.
	sortLen := nearestCnt
	for sortLen > 0 {
		last := 0
		for n := 1; n < sortLen; n++ {
			if mvstack[n-1].Weight < mvstack[n].Weight {
				mvstack[n-1], mvstack[n] = mvstack[n], mvstack[n-1]
				last = n
			}
		}
		sortLen = last
	}
	// Sort secondary portion.
	sortLen = nMVs
	for sortLen > nearestCnt {
		last := nearestCnt
		for n := nearestCnt + 1; n < sortLen; n++ {
			if mvstack[n-1].Weight < mvstack[n].Weight {
				mvstack[n-1], mvstack[n] = mvstack[n], mvstack[n-1]
				last = n
			}
		}
		sortLen = last
	}


	// ---------------------------------------------------------------
	// Compound extended candidates.
	// When cnt < 2 for compound blocks, synthesize compound MVs from
	// single-ref neighbors by collecting "same" (matching ref) and
	// "diff" (sign-flipped) MVs, then merging them.
	// Matches dav1d refmvs.c lines 526-581.
	// ---------------------------------------------------------------
	if isComp && nMVs < 2 {
		signBias := td.computeSignBias()
		sign0 := signBias[ref0]
		sign1 := signBias[ref1]

		sz4 := w4
		if h4 < sz4 {
			sz4 = h4
		}

		// same[0..1] holds MVs for ref0 (same_count[0]) and ref1 (same_count[1]).
		// diff[0..1] holds sign-flipped MVs for ref0 (diff_count[0]) and ref1 (diff_count[1]).
		var sameMV [2][2]MV     // sameMV[refIdx][candidateIdx]
		var diffMV [2][2]MV     // diffMV[refIdx][candidateIdx]
		var sameCount [2]int
		var diffCount [2]int

		addCompoundExtended := func(info ModeInfo) {
			if info.IsIntra {
				return
			}
			for n := 0; n < 2; n++ {
				candRef := info.RefFrame[n]
				if candRef < 0 {
					break
				}
				candMV := info.MV[n]
				candRefIdx := int(candRef)

				if candRef == ref0 {
					if sameCount[0] < 2 {
						sameMV[0][sameCount[0]] = candMV
						sameCount[0]++
					}
					if diffCount[1] < 2 {
						dmv := candMV
						if candRefIdx < len(signBias) && (sign1^signBias[candRefIdx]) != 0 {
							dmv.Row = -dmv.Row
							dmv.Col = -dmv.Col
						}
						diffMV[1][diffCount[1]] = dmv
						diffCount[1]++
					}
				} else if candRef == ref1 {
					if sameCount[1] < 2 {
						sameMV[1][sameCount[1]] = candMV
						sameCount[1]++
					}
					if diffCount[0] < 2 {
						dmv := candMV
						if candRefIdx < len(signBias) && (sign0^signBias[candRefIdx]) != 0 {
							dmv.Row = -dmv.Row
							dmv.Col = -dmv.Col
						}
						diffMV[0][diffCount[0]] = dmv
						diffCount[0]++
					}
				} else {
					// Neither ref matches: use sign-flipped as diff for both.
					iMV := MV{Row: -candMV.Row, Col: -candMV.Col}
					if diffCount[0] < 2 {
						if candRefIdx < len(signBias) && (sign0^signBias[candRefIdx]) != 0 {
							diffMV[0][diffCount[0]] = iMV
						} else {
							diffMV[0][diffCount[0]] = candMV
						}
						diffCount[0]++
					}
					if diffCount[1] < 2 {
						if candRefIdx < len(signBias) && (sign1^signBias[candRefIdx]) != 0 {
							diffMV[1][diffCount[1]] = iMV
						} else {
							diffMV[1][diffCount[1]] = candMV
						}
						diffCount[1]++
					}
				}
			}
		}

		// Scan top row.
		if nRows != ^uint(0) {
			for x := 0; x < sz4; {
				info := td.getAboveModeInfo(miCol + x)
				addCompoundExtended(info)
				step := int(info.BW4)
				if step < 1 {
					step = 1
				}
				x += step
			}
		}

		// Scan left column.
		if nCols != ^uint(0) {
			for y := 0; y < sz4; {
				info := td.getLeftModeInfo(miRow + y)
				addCompoundExtended(info)
				step := int(info.BH4)
				if step < 1 {
					step = 1
				}
				y += step
			}
		}

		// Merge: for each ref component, fill from same then diff then tgmv.
		// same[n] holds candidates in mvstack[nMVs..nMVs+1].MV[n].
		// Matches dav1d lines 554-571.
		var assembled [2]RefMVCandidate
		for n := 0; n < 2; n++ {
			m := sameCount[n]
			if m >= 2 {
				// Already have 2 same candidates for this ref component.
				if n == 0 {
					assembled[0].MV[0] = sameMV[0][0]
					assembled[1].MV[0] = sameMV[0][1]
				} else {
					assembled[0].MV[1] = sameMV[1][0]
					assembled[1].MV[1] = sameMV[1][1]
				}
				continue
			}
			// Copy same candidates.
			for i := 0; i < m; i++ {
				if n == 0 {
					assembled[i].MV[0] = sameMV[0][i]
				} else {
					assembled[i].MV[1] = sameMV[1][i]
				}
			}
			// Fill from diff candidates.
			l := diffCount[n]
			if l > 0 && m < 2 {
				if n == 0 {
					assembled[m].MV[0] = diffMV[0][0]
				} else {
					assembled[m].MV[1] = diffMV[1][0]
				}
				m++
				if m < 2 && l == 2 {
					if n == 0 {
						assembled[1].MV[0] = diffMV[0][1]
					} else {
						assembled[1].MV[1] = diffMV[1][1]
					}
					m = 2
				}
			}
			// Fill remaining from global MV.
			for m < 2 {
				if n == 0 {
					assembled[m].MV[0] = tgmv0
				} else {
					assembled[m].MV[1] = tgmv1
				}
				m++
			}
		}

		// If the first extended was the same as the non-extended one,
		// replace it with the second extended one.
		// Matches dav1d lines 575-577.
		n := nMVs
		if n == 1 && mvstack[0].MV[0] == assembled[0].MV[0] && mvstack[0].MV[1] == assembled[0].MV[1] {
			mvstack[1].MV = assembled[1].MV
		} else {
			for i := n; i < 2; i++ {
				mvstack[i].MV = assembled[i-n].MV
			}
		}
		for i := n; i < 2; i++ {
			mvstack[i].Weight = 2
		}
		nMVs = 2
	}

	// ---------------------------------------------------------------
	// Single-ref extended candidates (sign-flip from non-matching refs).
	// Fill up to 2 candidates if cnt < 2.
	// ---------------------------------------------------------------
	if !isComp && ref0 >= 0 && nMVs < 2 {
		// Compute sign bias for each reference.
		signBias := td.computeSignBias()
		sign := signBias[ref0]

		sz4 := w4
		if h4 < sz4 {
			sz4 = h4
		}

		// Scan above row for non-self references.
		if nRows != ^uint(0) {
			for x := 0; x < sz4 && nMVs < 2; {
				var info ModeInfo
				info = td.getAboveModeInfo(miCol + x)
				td.addSingleExtendedCandidate(mvstack[:], &nMVs, info, sign, signBias[:])
				step := int(info.BW4)
				if step < 1 {
					step = 1
				}
				x += step
			}
		}

		// Scan left column for non-self references.
		if nCols != ^uint(0) {
			for y := 0; y < sz4 && nMVs < 2; {
				var info ModeInfo
				info = td.getLeftModeInfo(miRow + y)
				td.addSingleExtendedCandidate(mvstack[:], &nMVs, info, sign, signBias[:])
				step := int(info.BH4)
				if step < 1 {
					step = 1
				}
				y += step
			}
		}
	}

	// ---------------------------------------------------------------
	// MV clamping
	// ---------------------------------------------------------------
	if nMVs > 0 {
		iw4 := int(td.fh.MiCols)
		ih4 := int(td.fh.MiRows)
		left := -(miCol + bW + 4) * 4 * 8
		right := (iw4 - miCol + 4) * 4 * 8
		top := -(miRow + bH + 4) * 4 * 8
		bottom := (ih4 - miRow + 4) * 4 * 8
		for n := 0; n < nMVs; n++ {
			mvstack[n].MV[0].Col = clampInt32(mvstack[n].MV[0].Col, int32(left), int32(right))
			mvstack[n].MV[0].Row = clampInt32(mvstack[n].MV[0].Row, int32(top), int32(bottom))
			if isComp {
				mvstack[n].MV[1].Col = clampInt32(mvstack[n].MV[1].Col, int32(left), int32(right))
				mvstack[n].MV[1].Row = clampInt32(mvstack[n].MV[1].Row, int32(top), int32(bottom))
			}
		}
	}

	// Fill remaining slots (up to 2) with global MV.
	for n := nMVs; n < 2; n++ {
		mvstack[n].MV[0] = tgmv0
		if isComp {
			mvstack[n].MV[1] = tgmv1
		}
	}

	// ---------------------------------------------------------------
	// Context derivation matching dav1d.
	// ---------------------------------------------------------------
	refMatchCount := haveRowMVs + haveColMVs

	var newMVCtx, refMVCtx int
	switch nearestMatch {
	case 0:
		if refMatchCount > 0 {
			newMVCtx = 1
		}
		refMVCtx = refMatchCount
		if refMVCtx > 2 {
			refMVCtx = 2
		}
	case 1:
		newMVCtx = 3 - haveNewMV
		refMVCtx = refMatchCount * 3
		if refMVCtx > 4 {
			refMVCtx = 4
		}
	case 2:
		newMVCtx = 5 - haveNewMV
		refMVCtx = 5
	}

	// Context packing depends on single-ref vs compound.
	// For single-ref: matches dav1d refmvs.c line 658:
	//   ctx = (refmv_ctx << 4) | (globalmv_ctx << 3) | newmv_ctx
	// For compound: dav1d computes a compact 0-7 index from
	//   (newMVCtx, refMVCtx) via refmvs.c lines 607-617.
	if isComp {
		var compCtx int
		switch refMVCtx >> 1 {
		case 0:
			compCtx = min(newMVCtx, 1)
		case 1:
			compCtx = 1 + min(newMVCtx, 3)
		case 2:
			compCtx = clamp(3+newMVCtx, 4, 7)
		}
		return mvstack[:8], nMVs, compCtx
	}
	ctx := (refMVCtx << 4) | (globMVCtx << 3) | newMVCtx
	return mvstack[:8], nMVs, ctx
}


// clampInt32 clamps v to [lo, hi].
func clampInt32(v, lo, hi int32) int32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// computeSignBias returns sign_bias[0..6] for references LAST..ALTREF.
// sign_bias[i] is 1 if reference i+1 has order_hint > current frame's order_hint.
func (td *TileDecoder) computeSignBias() [7]int {
	var sb [7]int
	orderHintBits := int(td.sh.OrderHintBitsMinus1) + 1
	if orderHintBits == 0 {
		return sb
	}
	curOH := int(td.fh.OrderHint)
	for i := 0; i < 7; i++ {
		slot := int(td.fh.RefFrameIdx[i])
		refOH := int(td.refOrderHints[slot])
		dist := getPocDiff(orderHintBits, refOH, curOH)
		if dist > 0 {
			sb[i] = 1
		}
	}
	return sb
}

// addSingleExtendedCandidate adds a single-ref extended candidate from a
// non-matching reference neighbor, applying sign-flip if needed.
// Matches dav1d's add_single_extended_candidate().
func (td *TileDecoder) addSingleExtendedCandidate(mvstack []RefMVCandidate, cnt *int, info ModeInfo, sign int, signBias []int) {
	if info.IsIntra {
		return
	}
	for n := 0; n < 2; n++ {
		candRef := info.RefFrame[n]
		if candRef < 0 {
			break
		}
		candMV := info.MV[n]
		candRefIdx := int(candRef)
		if candRefIdx < len(signBias) && (sign^signBias[candRefIdx]) != 0 {
			candMV.Row = -candMV.Row
			candMV.Col = -candMV.Col
		}
		// Check for duplicate.
		last := *cnt
		found := false
		for m := 0; m < last; m++ {
			if candMV == mvstack[m].MV[0] {
				found = true
				break
			}
		}
		if !found && last < 8 {
			mvstack[last].MV[0] = candMV
			mvstack[last].Weight = 2
			*cnt = last + 1
		}
	}
}

// scanRowSecondary scans a secondary above row using miGrid with block-stepping.
// This is used for the n=2,3 secondary row scans at 8x8 resolution.
// Returns the n_rows contribution from this scan.
func (td *TileDecoder) scanRowSecondary(
	mvstack []RefMVCandidate, nMVs *int,
	isComp bool, ref0, ref1 int8,
	rowMI, startCol, blockW, w4scan, maxRows, step int,
	haveNewMV, haveRowMVs *int,
	gmv0Valid, gmv1Valid bool, gmv0, gmv1 MV,
) int {
	if td.miGrid == nil {
		return 1
	}

	firstInfo := td.miGrid.Get(rowMI, startCol)
	candBW4 := int(firstInfo.BW4)
	if candBW4 < 1 {
		candBW4 = 1
	}
	candBH4 := int(firstInfo.BH4)
	if candBH4 < 1 {
		candBH4 = 1
	}
	bW := blockW // original block width for weight comparison

	// len = max(step, min(bw4, cand_bw4))
	minBWCand := bW
	if candBW4 < minBWCand {
		minBWCand = candBW4
	}
	lenVal := step
	if minBWCand > lenVal {
		lenVal = minBWCand
	}

	// add_spatial_candidate helper with GLOBALMV substitution.
	addSC := func(info ModeInfo, weight int32) {
		if info.IsIntra {
			return
		}
		if !isComp {
			for n := 0; n < 2; n++ {
				if info.RefFrame[n] == ref0 {
					candMV := info.MV[n]
					if (info.MF&1) != 0 && gmv0Valid {
						candMV = gmv0
					}
					*haveRowMVs = 1
					if info.MF&2 != 0 {
						*haveNewMV = 1
					}
					for m := 0; m < *nMVs; m++ {
						if mvstack[m].MV[0] == candMV {
							mvstack[m].Weight += weight
							return
						}
					}
					if *nMVs < 8 {
						mvstack[*nMVs].MV[0] = candMV
						mvstack[*nMVs].Weight = weight
						*nMVs++
					}
					return
				}
			}
		} else {
			if info.RefFrame[0] == ref0 && info.RefFrame[1] == ref1 {
				candMV0 := info.MV[0]
				candMV1 := info.MV[1]
				if (info.MF & 1) != 0 {
					if gmv0Valid {
						candMV0 = gmv0
					}
					if gmv1Valid {
						candMV1 = gmv1
					}
				}
				*haveRowMVs = 1
				if info.MF&2 != 0 {
					*haveNewMV = 1
				}
				for m := 0; m < *nMVs; m++ {
					if mvstack[m].MV[0] == candMV0 && mvstack[m].MV[1] == candMV1 {
						mvstack[m].Weight += weight
						return
					}
				}
				if *nMVs < 8 {
					mvstack[*nMVs].MV[0] = candMV0
					mvstack[*nMVs].MV[1] = candMV1
					mvstack[*nMVs].Weight = weight
					*nMVs++
				}
			}
		}
	}

	if bW <= candBW4 {
		var weightFactor int
		if bW == 1 {
			weightFactor = 2
		} else {
			wf := 2 * maxRows
			if candBH4 < wf {
				wf = candBH4
			}
			if wf < 2 {
				wf = 2
			}
			weightFactor = wf
		}
		addSC(firstInfo, int32(lenVal*weightFactor))
		return weightFactor >> 1
	}

	// bW > candBW4: step through sub-blocks.
	addSC(firstInfo, int32(lenVal*2))
	for x := lenVal; x < w4scan; {
		info := td.miGrid.Get(rowMI, startCol+x)
		candBW4 = int(info.BW4)
		if candBW4 < 1 {
			candBW4 = 1
		}
		lenVal = step
		if candBW4 > lenVal {
			lenVal = candBW4
		}
		addSC(info, int32(lenVal*2))
		x += lenVal
	}
	return 1
}

// scanColSecondary scans a secondary left column using miGrid with block-stepping.
// This is used for the n=2,3 secondary column scans at 8x8 resolution.
// Returns the n_cols contribution from this scan.
func (td *TileDecoder) scanColSecondary(
	mvstack []RefMVCandidate, nMVs *int,
	isComp bool, ref0, ref1 int8,
	colMI, startRow, blockH, h4scan, maxCols, step int,
	haveNewMV, haveColMVs *int,
	gmv0Valid, gmv1Valid bool, gmv0, gmv1 MV,
) int {
	if td.miGrid == nil {
		return 1
	}

	firstInfo := td.miGrid.Get(startRow, colMI)
	candBH4 := int(firstInfo.BH4)
	if candBH4 < 1 {
		candBH4 = 1
	}
	candBW4 := int(firstInfo.BW4)
	if candBW4 < 1 {
		candBW4 = 1
	}
	bH := blockH // original block height for weight comparison

	// len = max(step, min(bh4, cand_bh4))
	minBHCand := bH
	if candBH4 < minBHCand {
		minBHCand = candBH4
	}
	lenVal := step
	if minBHCand > lenVal {
		lenVal = minBHCand
	}

	addSC := func(info ModeInfo, weight int32) {
		if info.IsIntra {
			return
		}
		if !isComp {
			for n := 0; n < 2; n++ {
				if info.RefFrame[n] == ref0 {
					candMV := info.MV[n]
					if (info.MF&1) != 0 && gmv0Valid {
						candMV = gmv0
					}
					*haveColMVs = 1
					if info.MF&2 != 0 {
						*haveNewMV = 1
					}
					for m := 0; m < *nMVs; m++ {
						if mvstack[m].MV[0] == candMV {
							mvstack[m].Weight += weight
							return
						}
					}
					if *nMVs < 8 {
						mvstack[*nMVs].MV[0] = candMV
						mvstack[*nMVs].Weight = weight
						*nMVs++
					}
					return
				}
			}
		} else {
			if info.RefFrame[0] == ref0 && info.RefFrame[1] == ref1 {
				candMV0 := info.MV[0]
				candMV1 := info.MV[1]
				if (info.MF & 1) != 0 {
					if gmv0Valid {
						candMV0 = gmv0
					}
					if gmv1Valid {
						candMV1 = gmv1
					}
				}
				*haveColMVs = 1
				if info.MF&2 != 0 {
					*haveNewMV = 1
				}
				for m := 0; m < *nMVs; m++ {
					if mvstack[m].MV[0] == candMV0 && mvstack[m].MV[1] == candMV1 {
						mvstack[m].Weight += weight
						return
					}
				}
				if *nMVs < 8 {
					mvstack[*nMVs].MV[0] = candMV0
					mvstack[*nMVs].MV[1] = candMV1
					mvstack[*nMVs].Weight = weight
					*nMVs++
				}
			}
		}
	}

	if bH <= candBH4 {
		var weightFactor int
		if bH == 1 {
			weightFactor = 2
		} else {
			// dav1d uses cand_bw4 for column scan weight factor.
			wf := 2 * maxCols
			if candBW4 < wf {
				wf = candBW4
			}
			if wf < 2 {
				wf = 2
			}
			weightFactor = wf
		}
		addSC(firstInfo, int32(lenVal*weightFactor))
		return weightFactor >> 1
	}

	// bH > candBH4: step through sub-blocks.
	addSC(firstInfo, int32(lenVal*2))
	for y := lenVal; y < h4scan; {
		info := td.miGrid.Get(startRow+y, colMI)
		candBH4 = int(info.BH4)
		if candBH4 < 1 {
			candBH4 = 1
		}
		lenVal = step
		if candBH4 > lenVal {
			lenVal = candBH4
		}
		addSC(info, int32(lenVal*2))
		y += lenVal
	}
	return 1
}

// getAboveModeInfo returns the ModeInfo for the above neighbor at miCol.
func (td *TileDecoder) getAboveModeInfo(miCol int) ModeInfo {
	if miCol < len(td.aboveModeInfo) {
		return td.aboveModeInfo[miCol]
	}
	return ModeInfo{IsIntra: true}
}

// getLeftModeInfo returns the ModeInfo for the left neighbor at miRow.
func (td *TileDecoder) getLeftModeInfo(miRow int) ModeInfo {
	if miRow < len(td.leftModeInfo) {
		return td.leftModeInfo[miRow]
	}
	return ModeInfo{IsIntra: true}
}

// getGlobalMV returns the global motion vector for the given reference frame
// projected to the block position. For TRANSLATION type, this is just the
// translation component. For IDENTITY, returns zero.
// getGlobalMV computes the global motion vector for a block.
// Matches dav1d get_gmv_2d() in env.h exactly.
// GmParams are stored in 1/(1<<16) pel units (warpedModelPrecBits=16).
// The result is in 1/8 pel units (standard MV precision).
func (td *TileDecoder) getGlobalMV(ref int8, miRow, miCol, bW, bH int) MV {
	if ref < 0 || int(ref) >= len(td.fh.GmType) {
		return MV{}
	}
	gmType := td.fh.GmType[ref]
	switch gmType {
	case GM_IDENTITY:
		return MV{}
	case GM_TRANSLATION:
		// Translation params stored at warpedModelPrecBits (16) precision.
		// Convert to 1/8 pel: >> 13 (= 16 - 3).
		// dav1d: res.y = gmv->matrix[0] >> 13; res.x = gmv->matrix[1] >> 13;
		res := MV{
			Row: td.fh.GmParams[ref][0] >> 13,
			Col: td.fh.GmParams[ref][1] >> 13,
		}
		if td.fh.ForceIntegerMV {
			fixIntMVPrecision(&res)
		}
		return res
	default:
		// ROTZOOM or AFFINE: project block center through the warp matrix.
		// dav1d env.h get_gmv_2d():
		//   x = bx4*4 + bw4*2 - 1
		//   y = by4*4 + bh4*2 - 1
		//   xc = (matrix[2] - (1<<16)) * x + matrix[3] * y + matrix[0]
		//   yc = (matrix[5] - (1<<16)) * y + matrix[4] * x + matrix[1]
		//   shift = 16 - (3 - !hp)
		//   round = (1 << shift) >> 1
		//   res.y = apply_sign(((abs(yc) + round) >> shift) << !hp, yc)
		//   res.x = apply_sign(((abs(xc) + round) >> shift) << !hp, xc)
		bx4 := miCol
		by4 := miRow
		bw4 := bW
		bh4 := bH
		x := int64(bx4*4 + bw4*2 - 1)
		y := int64(by4*4 + bh4*2 - 1)
		m := td.fh.GmParams[ref]
		xc := (int64(m[2]) - (1 << 16)) * x + int64(m[3])*y + int64(m[0])
		yc := (int64(m[5]) - (1 << 16)) * y + int64(m[4])*x + int64(m[1])

		hp := 0
		if !td.fh.AllowHighPrecisionMV {
			hp = 1
		}
		shift := uint(16 - (3 - hp))
		round := int64(1<<shift) >> 1

		absXc := xc
		if absXc < 0 {
			absXc = -absXc
		}
		absYc := yc
		if absYc < 0 {
			absYc = -absYc
		}
		resCol := int32(((absXc + round) >> shift) << uint(hp))
		if xc < 0 {
			resCol = -resCol
		}
		resRow := int32(((absYc + round) >> shift) << uint(hp))
		if yc < 0 {
			resRow = -resRow
		}
		res := MV{Row: resRow, Col: resCol}
		if td.fh.ForceIntegerMV {
			fixIntMVPrecision(&res)
		}
		return res
	}
}

// fixIntMVPrecision rounds an MV to integer-pel precision.
// Matches dav1d fix_int_mv_precision() in env.h.
// dav1d uses >> 15 on int16_t; we use >> 31 on int32 for the sign bit.
func fixIntMVPrecision(mv *MV) {
	mv.Col = (mv.Col - (mv.Col >> 31) + 3) & ^int32(7)
	mv.Row = (mv.Row - (mv.Row >> 31) + 3) & ^int32(7)
}

// --- Reference frame context derivation ---
// These match dav1d env.h exactly.

func (td *TileDecoder) getRefCtx(haveTop, haveLeft bool, miRow, miCol int) int {
	cnt := [2]int{}
	if haveTop && miCol < len(td.aboveModeInfo) && !td.aboveModeInfo[miCol].IsIntra {
		info := td.aboveModeInfo[miCol]
		if info.RefFrame[0] >= 4 {
			cnt[1]++
		} else {
			cnt[0]++
		}
		if info.CompType != COMP_INTER_NONE && info.RefFrame[1] >= 0 {
			if info.RefFrame[1] >= 4 {
				cnt[1]++
			} else {
				cnt[0]++
			}
		}
	}
	if haveLeft && miRow < len(td.leftModeInfo) && !td.leftModeInfo[miRow].IsIntra {
		info := td.leftModeInfo[miRow]
		if info.RefFrame[0] >= 4 {
			cnt[1]++
		} else {
			cnt[0]++
		}
		if info.CompType != COMP_INTER_NONE && info.RefFrame[1] >= 0 {
			if info.RefFrame[1] >= 4 {
				cnt[1]++
			} else {
				cnt[0]++
			}
		}
	}
	if cnt[0] == cnt[1] {
		return 1
	}
	if cnt[0] < cnt[1] {
		return 0
	}
	return 2
}

func (td *TileDecoder) getFwdRefCtx(haveTop, haveLeft bool, miRow, miCol int) int {
	cnt := [4]int{}
	if haveTop && miCol < len(td.aboveModeInfo) && !td.aboveModeInfo[miCol].IsIntra {
		info := td.aboveModeInfo[miCol]
		if info.RefFrame[0] >= 0 && info.RefFrame[0] < 4 {
			cnt[info.RefFrame[0]]++
		}
		if info.CompType != COMP_INTER_NONE && info.RefFrame[1] >= 0 && info.RefFrame[1] < 4 {
			cnt[info.RefFrame[1]]++
		}
	}
	if haveLeft && miRow < len(td.leftModeInfo) && !td.leftModeInfo[miRow].IsIntra {
		info := td.leftModeInfo[miRow]
		if info.RefFrame[0] >= 0 && info.RefFrame[0] < 4 {
			cnt[info.RefFrame[0]]++
		}
		if info.CompType != COMP_INTER_NONE && info.RefFrame[1] >= 0 && info.RefFrame[1] < 4 {
			cnt[info.RefFrame[1]]++
		}
	}
	cnt[0] += cnt[1]
	cnt[2] += cnt[3]
	if cnt[0] == cnt[2] {
		return 1
	}
	if cnt[0] < cnt[2] {
		return 0
	}
	return 2
}

func (td *TileDecoder) getFwdRef1Ctx(haveTop, haveLeft bool, miRow, miCol int) int {
	cnt := [2]int{}
	if haveTop && miCol < len(td.aboveModeInfo) && !td.aboveModeInfo[miCol].IsIntra {
		info := td.aboveModeInfo[miCol]
		if info.RefFrame[0] >= 0 && info.RefFrame[0] < 2 {
			cnt[info.RefFrame[0]]++
		}
		if info.CompType != COMP_INTER_NONE && info.RefFrame[1] >= 0 && info.RefFrame[1] < 2 {
			cnt[info.RefFrame[1]]++
		}
	}
	if haveLeft && miRow < len(td.leftModeInfo) && !td.leftModeInfo[miRow].IsIntra {
		info := td.leftModeInfo[miRow]
		if info.RefFrame[0] >= 0 && info.RefFrame[0] < 2 {
			cnt[info.RefFrame[0]]++
		}
		if info.CompType != COMP_INTER_NONE && info.RefFrame[1] >= 0 && info.RefFrame[1] < 2 {
			cnt[info.RefFrame[1]]++
		}
	}
	if cnt[0] == cnt[1] {
		return 1
	}
	if cnt[0] < cnt[1] {
		return 0
	}
	return 2
}

func (td *TileDecoder) getFwdRef2Ctx(haveTop, haveLeft bool, miRow, miCol int) int {
	cnt := [2]int{}
	if haveTop && miCol < len(td.aboveModeInfo) && !td.aboveModeInfo[miCol].IsIntra {
		info := td.aboveModeInfo[miCol]
		r := info.RefFrame[0]
		if r >= 2 && r < 4 {
			cnt[r-2]++
		}
		if info.CompType != COMP_INTER_NONE {
			r = info.RefFrame[1]
			if r >= 2 && r < 4 {
				cnt[r-2]++
			}
		}
	}
	if haveLeft && miRow < len(td.leftModeInfo) && !td.leftModeInfo[miRow].IsIntra {
		info := td.leftModeInfo[miRow]
		r := info.RefFrame[0]
		if r >= 2 && r < 4 {
			cnt[r-2]++
		}
		if info.CompType != COMP_INTER_NONE {
			r = info.RefFrame[1]
			if r >= 2 && r < 4 {
				cnt[r-2]++
			}
		}
	}
	if cnt[0] == cnt[1] {
		return 1
	}
	if cnt[0] < cnt[1] {
		return 0
	}
	return 2
}

func (td *TileDecoder) getBwdRefCtx(haveTop, haveLeft bool, miRow, miCol int) int {
	cnt := [3]int{}
	if haveTop && miCol < len(td.aboveModeInfo) && !td.aboveModeInfo[miCol].IsIntra {
		info := td.aboveModeInfo[miCol]
		if info.RefFrame[0] >= 4 {
			cnt[info.RefFrame[0]-4]++
		}
		if info.CompType != COMP_INTER_NONE && info.RefFrame[1] >= 4 {
			cnt[info.RefFrame[1]-4]++
		}
	}
	if haveLeft && miRow < len(td.leftModeInfo) && !td.leftModeInfo[miRow].IsIntra {
		info := td.leftModeInfo[miRow]
		if info.RefFrame[0] >= 4 {
			cnt[info.RefFrame[0]-4]++
		}
		if info.CompType != COMP_INTER_NONE && info.RefFrame[1] >= 4 {
			cnt[info.RefFrame[1]-4]++
		}
	}
	cnt[1] += cnt[0]
	if cnt[2] == cnt[1] {
		return 1
	}
	if cnt[1] < cnt[2] {
		return 0
	}
	return 2
}

func (td *TileDecoder) getBwdRef1Ctx(haveTop, haveLeft bool, miRow, miCol int) int {
	cnt := [3]int{}
	if haveTop && miCol < len(td.aboveModeInfo) && !td.aboveModeInfo[miCol].IsIntra {
		info := td.aboveModeInfo[miCol]
		if info.RefFrame[0] >= 4 {
			cnt[info.RefFrame[0]-4]++
		}
		if info.CompType != COMP_INTER_NONE && info.RefFrame[1] >= 4 {
			cnt[info.RefFrame[1]-4]++
		}
	}
	if haveLeft && miRow < len(td.leftModeInfo) && !td.leftModeInfo[miRow].IsIntra {
		info := td.leftModeInfo[miRow]
		if info.RefFrame[0] >= 4 {
			cnt[info.RefFrame[0]-4]++
		}
		if info.CompType != COMP_INTER_NONE && info.RefFrame[1] >= 4 {
			cnt[info.RefFrame[1]-4]++
		}
	}
	if cnt[0] == cnt[1] {
		return 1
	}
	if cnt[0] < cnt[1] {
		return 0
	}
	return 2
}

func (td *TileDecoder) getCompDirCtx(haveTop, haveLeft bool, miRow, miCol int) int {
	if haveTop && haveLeft {
		aIntra := td.aboveIsIntra(miCol)
		lIntra := td.leftIsIntra(miRow)
		if aIntra && lIntra {
			return 2
		}
		if aIntra || lIntra {
			var info ModeInfo
			if aIntra {
				info = td.getLeftModeInfo(miRow)
			} else {
				info = td.getAboveModeInfo(miCol)
			}
			if info.CompType == COMP_INTER_NONE {
				return 2
			}
			hasUni := (info.RefFrame[0] < 4) == (info.RefFrame[1] < 4)
			if hasUni {
				return 3
			}
			return 1
		}
		aInfo := td.getAboveModeInfo(miCol)
		lInfo := td.getLeftModeInfo(miRow)
		aComp := aInfo.CompType != COMP_INTER_NONE
		lComp := lInfo.CompType != COMP_INTER_NONE
		if !aComp && !lComp {
			if (aInfo.RefFrame[0] >= 4) == (lInfo.RefFrame[0] >= 4) {
				return 3
			}
			return 1
		}
		if !aComp || !lComp {
			var compInfo ModeInfo
			if aComp {
				compInfo = aInfo
			} else {
				compInfo = lInfo
			}
			hasUni := (compInfo.RefFrame[0] < 4) == (compInfo.RefFrame[1] < 4)
			if !hasUni {
				return 1
			}
			if (aInfo.RefFrame[0] >= 4) == (lInfo.RefFrame[0] >= 4) {
				return 4
			}
			return 3
		}
		aUni := (aInfo.RefFrame[0] < 4) == (aInfo.RefFrame[1] < 4)
		lUni := (lInfo.RefFrame[0] < 4) == (lInfo.RefFrame[1] < 4)
		if !aUni && !lUni {
			return 0
		}
		if !aUni || !lUni {
			return 2
		}
		if (aInfo.RefFrame[0] == 4) == (lInfo.RefFrame[0] == 4) {
			return 4
		}
		return 3
	}
	if haveTop || haveLeft {
		var info ModeInfo
		if haveLeft {
			info = td.getLeftModeInfo(miRow)
		} else {
			info = td.getAboveModeInfo(miCol)
		}
		if info.IsIntra {
			return 2
		}
		if info.CompType == COMP_INTER_NONE {
			return 2
		}
		hasUni := (info.RefFrame[0] < 4) == (info.RefFrame[1] < 4)
		if hasUni {
			return 4
		}
		return 0
	}
	return 2
}

func (td *TileDecoder) getUniP1Ctx(haveTop, haveLeft bool, miRow, miCol int) int {
	cnt := [3]int{}
	if haveTop && miCol < len(td.aboveModeInfo) && !td.aboveModeInfo[miCol].IsIntra {
		info := td.aboveModeInfo[miCol]
		r := info.RefFrame[0]
		if r >= 1 && r <= 3 {
			cnt[r-1]++
		}
		if info.CompType != COMP_INTER_NONE {
			r = info.RefFrame[1]
			if r >= 1 && r <= 3 {
				cnt[r-1]++
			}
		}
	}
	if haveLeft && miRow < len(td.leftModeInfo) && !td.leftModeInfo[miRow].IsIntra {
		info := td.leftModeInfo[miRow]
		r := info.RefFrame[0]
		if r >= 1 && r <= 3 {
			cnt[r-1]++
		}
		if info.CompType != COMP_INTER_NONE {
			r = info.RefFrame[1]
			if r >= 1 && r <= 3 {
				cnt[r-1]++
			}
		}
	}
	cnt[1] += cnt[2]
	if cnt[0] == cnt[1] {
		return 1
	}
	if cnt[0] < cnt[1] {
		return 0
	}
	return 2
}

// getCompCtx returns the context for the reference mode CDF (single vs compound).
// Matches dav1d env.h get_comp_ctx.
func (td *TileDecoder) getCompCtx(miRow, miCol int) int {
	haveTop := miRow > td.tileRowStart
	haveLeft := miCol > td.tileColStart

	if haveTop {
		if haveLeft {
			aInfo := td.getAboveModeInfo(miCol)
			lInfo := td.getLeftModeInfo(miRow)
			if aInfo.CompType != COMP_INTER_NONE {
				if lInfo.CompType != COMP_INTER_NONE {
					return 4
				}
				if lInfo.IsIntra || lInfo.RefFrame[0] >= 4 {
					return 3
				}
				return 2
			}
			if lInfo.CompType != COMP_INTER_NONE {
				if aInfo.IsIntra || aInfo.RefFrame[0] >= 4 {
					return 3
				}
				return 2
			}
			aboveBwd := !aInfo.IsIntra && aInfo.RefFrame[0] >= 4
			leftBwd := !lInfo.IsIntra && lInfo.RefFrame[0] >= 4
			if aboveBwd != leftBwd {
				return 1
			}
			return 0
		}
		aInfo := td.getAboveModeInfo(miCol)
		if aInfo.CompType != COMP_INTER_NONE {
			return 3
		}
		if !aInfo.IsIntra && aInfo.RefFrame[0] >= 4 {
			return 1
		}
		return 0
	}
	if haveLeft {
		lInfo := td.getLeftModeInfo(miRow)
		if lInfo.CompType != COMP_INTER_NONE {
			return 3
		}
		if !lInfo.IsIntra && lInfo.RefFrame[0] >= 4 {
			return 1
		}
		return 0
	}
	return 1
}

// setInterModeInfo records the inter mode info for neighbor context and miGrid.
func (td *TileDecoder) setInterModeInfo(miRow, miCol, bW, bH int, info ModeInfo) {
	// Set above context.
	for c := miCol; c < miCol+bW && c < len(td.aboveModeInfo); c++ {
		td.aboveModeInfo[c] = info
	}
	// Set left context.
	for r := miRow; r < miRow+bH && r < len(td.leftModeInfo); r++ {
		td.leftModeInfo[r] = info
	}
	// Set frame-level ModeInfo grid.
	if td.miGrid != nil {
		td.miGrid.Set(miRow, miCol, bW, bH, info)
	}
}

// yModeSizeGroup returns the size group for Y mode CDF selection.
// Matches dav1d dav1d_ymode_size_context.
// yModeSizeGroup returns the Y mode size context for inter-intra.
// Must match dav1d's dav1d_ymode_size_context lookup table exactly.
// The table is indexed by block size enum, not by area.
func yModeSizeGroup(bW, bH int) int {
	pixW := bW * 4
	pixH := bH * 4
	// Use exact lookup matching dav1d tables.c dav1d_ymode_size_context[].
	switch {
	case pixW >= 64 || pixH >= 64:
		// 128x128, 128x64, 64x128, 64x64, 64x32, 32x64 = 3
		if pixW >= 64 && pixH >= 32 {
			return 3
		}
		if pixH >= 64 && pixW >= 32 {
			return 3
		}
		// 64x16 = 2, 16x64 = 2
		return 2
	case pixW == 32 && pixH == 32:
		return 3
	case pixW == 32 && pixH == 16:
		return 2
	case pixW == 32 && pixH == 8:
		return 1
	case pixW == 16 && pixH == 32:
		return 2
	case pixW == 16 && pixH == 16:
		return 2
	case pixW == 16 && pixH == 8:
		return 1
	case pixW == 16 && pixH == 4:
		return 0
	case pixW == 8 && pixH == 32:
		return 1
	case pixW == 8 && pixH == 16:
		return 1
	case pixW == 8 && pixH == 8:
		return 1
	case pixW == 8 && pixH == 4:
		return 0
	case pixW == 4:
		return 0
	default:
		return 0
	}
}
