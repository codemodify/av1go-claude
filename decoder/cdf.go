// Package decoder implements AV1 bitstream decoding.
//
// This file provides the CDFContext type, which holds all cumulative distribution
// function tables needed for AV1 entropy decoding.
//
// Multi-symbol CDF layout (for N symbols):
//   Array has N+1 entries: [N-1 ICDF values, 0 sentinel, counter].
//   ReadSymbol(cdf, N) decodes using cdf[0..N-2], then calls
//   UpdateCDF(cdf, N, val) which updates cdf[0..N-2] and stores
//   the adaptation counter at cdf[N].
//
// Boolean CDF layout (for 2 symbols via ReadSymbolBool):
//   Array has 2 entries: [ICDF value, counter].
//   Counter at cdf[1].
//
// AV1 spec Section 9.3 (Symbol Decoding Process) and Section 9.4 (Default CDF Tables).
package decoder

import "fmt"

// CDFContext holds all CDF tables needed for AV1 entropy decoding.
//
// CDF conventions (ascending, matching libaom/dav1d):
//   - cdf[0..nsyms-2] are cumulative probabilities in ascending order
//   - cdf[nsyms-1] is implicitly 32768 (not stored; the slot holds 0)
//   - cdf[nsyms] is an adaptation counter (starts at 0, saturates at 32)
//   - Probabilities are in [0, 32768)
//
// The tables are initialized with the AV1 specification default CDF values
// from Section 9.4 (see cdf_defaults.go). These empirically-tuned values
// are required for conformant decoding of AV1 bitstreams.
type CDFContext struct {
	// Partition type CDFs indexed by [partition_context].
	// AV1 spec Section 9.3, partition(): context depends on block size and
	// above/left neighbor availability. There are 4 contexts per block size
	// category, with the number of symbols varying by block size:
	//   - 128x128 (ctx 0-3): 10 symbols (all partition types)
	//   - 8x8 (ctx 20-23): 4 symbols (NONE, HORZ, VERT, SPLIT only)
	//   - Other sizes (ctx 4-19): 10 symbols
	Partition [24][]uint16

	// Split-or-horizontal partition CDFs for boundary blocks.
	// AV1 spec Section 5.11.4: used when only rows (top half) fit.
	// [partition_context], 2 symbols (0=HORZ, 1=SPLIT).
	SplitOrHorz [24][]uint16

	// Split-or-vertical partition CDFs for boundary blocks.
	// AV1 spec Section 5.11.4: used when only cols (left half) fit.
	// [partition_context], 2 symbols (0=VERT, 1=SPLIT).
	SplitOrVert [24][]uint16

	// Skip flag CDF indexed by [ctx].
	// AV1 spec Section 5.11.5. 3 contexts based on above/left skip flags.
	Skip [3][]uint16 // 2 symbols each

	// IntraBC CDF.
	// AV1 spec Section 5.11.4 (use_intrabc). 2 symbols.
	IntraBC []uint16

	// Intra frame Y mode CDFs indexed by [above_ctx][left_ctx].
	// AV1 spec Section 5.11.7 (intra_frame_y_mode).
	// 5x5 contexts (mapped via intraModeCtx), each with 13 symbols.
	IntraFrameYMode [5][5][]uint16

	// Non-intra-frame Y mode CDFs indexed by [size_group].
	// AV1 spec Section 5.11.7 (y_mode).
	// 4 size groups, 13 symbols.
	YMode [4][]uint16

	// UV mode CDFs indexed by [cfl_allowed][y_mode].
	// AV1 spec Section 5.11.8 (uv_mode).
	// When cfl_allowed=0: 13 symbols (no CFL mode).
	// When cfl_allowed=1: 14 symbols (includes CFL mode).
	UVMode [2][13][]uint16

	// TX size CDFs for different max tx size categories.
	// AV1 spec Section 5.11.16 (tx_size).
	// [max_tx_size_cat][tx_size_ctx], variable nsyms (2..5 depending on category).
	TxSize [4][3][]uint16

	// TX split CDF for inter variable TX tree.
	// AV1 spec Section 5.11.38 (read_var_tx_size): txfm_split.
	// TX split (txfm_split) CDF for inter variable TX tree.
	// Indexed by [cat][ctx], 2 symbols (split or not).
	// cat = 2*(4-max) - depth, where max is the square-up TX category (0-4)
	// and depth is the recursion depth (0-1). Range: 0-6.
	// ctx: 0-2 derived from above + left neighbor TX sizes.
	// dav1d: ts->cdf.m.txpart[cat][a+l].
	TxSplit [7][3][]uint16

	// All-zero (txb_skip) flag: whether a TX block has any non-zero coefficients.
	// AV1 spec Section 5.11.35 (all_zero / txb_skip).
	// Indexed by [tx_size_sqr_up][txb_ctx], 2 symbols.
	// tx_size_sqr_up is in [0, 4] (TX_4X4 to TX_64X64).
	// txb_ctx is in [0, 12] derived from neighbor zero flags.
	AllZero [5][13][]uint16

	// EOB (end of block position) CDFs per transform size.
	// AV1 spec Section 5.11.36.
	// [plane_type][eob_cdf_idx] with variable number of symbols.
	// eob_cdf_idx = min(txSzCtx, 1): 0 for TX_4X4, 1 for larger.
	EobPt16   [2][2][]uint16 // 5 symbols
	EobPt32   [2][2][]uint16 // 6 symbols
	EobPt64   [2][2][]uint16 // 7 symbols
	EobPt128  [2][2][]uint16 // 8 symbols
	EobPt256  [2][2][]uint16 // 9 symbols
	EobPt512  [2][2][]uint16 // 10 symbols
	EobPt1024 [2][2][]uint16 // 11 symbols

	// EOB extra bit CDFs.
	// AV1 spec Section 5.11.36. [txSzCtx][planeType][eobPt-2], 2 symbols.
	EobExtra [5][2][9][]uint16

	// Coefficient base level CDFs.
	// AV1 spec Section 5.11.39. [txSzCtx][planeType][coeffCtx], 4 symbols (0,1,2,3+).
	CoeffBase [5][2][42][]uint16

	// Coefficient base EOB CDFs.
	// AV1 spec Section 5.11.39. [txSzCtx][planeType][coeffCtx], 3 symbols.
	// 4 contexts: c==0, c in [1,3], c in [4,9], c >= 10.
	CoeffBaseEob [5][2][4][]uint16

	// DC sign CDF.
	// AV1 spec Section 5.11.38. [plane_type][dc_sign_ctx], 2 symbols.
	DcSign [2][3][]uint16

	// Coefficient base range (BR) CDFs.
	// AV1 spec Section 5.11.40. [txSzCtx][planeType][brCtx], 4 symbols.
	CoeffBR [5][2][21][]uint16

	// Segment ID CDFs.
	// AV1 spec Section 5.11.4 (segment_id).
	// [spatial_prediction_ctx], MAX_SEGMENTS (8) symbols.
	SegmentID [3][]uint16

	// Prediction of segment ID CDFs.
	// AV1 spec Section 5.11.4 (seg_id_predicted).
	// [prediction_ctx], 2 symbols.
	SegIDPredicted [3][]uint16

	// Inter vs intra mode CDF.
	// AV1 spec Section 5.11.6 (is_inter). [ctx], 2 symbols.
	IsInter [4][]uint16

	// Compound forward reference CDFs.
	// AV1 spec Section 5.11.10 (comp_ref). [level][ctx], 2 symbols.
	CompRef [3][6][]uint16

	// Compound backward reference CDFs. [level][ctx], 2 symbols.
	CompBwdRef [2][3][]uint16

	// Compound unidir reference CDFs. [level][ctx], 2 symbols.
	CompUniRef [3][3][]uint16

	// Single reference CDFs.
	// AV1 spec Section 5.11.11 (single_ref). [level][ctx], 2 symbols.
	SingleRef [6][3][]uint16

	// Compound mode CDFs.
	// AV1 spec Section 5.11.12 (compound_mode). [ctx], 8 symbols.
	CompoundMode [8][]uint16

	// New MV CDFs.
	// AV1 spec Section 5.11.12 (new_mv). [ctx], 2 symbols.
	NewMV [6][]uint16

	// Zero MV CDFs.
	// AV1 spec Section 5.11.12 (zero_mv). [ctx], 2 symbols.
	ZeroMV [2][]uint16

	// Ref MV CDFs.
	// AV1 spec Section 5.11.12 (ref_mv). [ctx], 2 symbols.
	RefMV [6][]uint16

	// DRL (dynamic reference list) index CDFs.
	// AV1 spec Section 5.11.12 (drl_mode). [ctx], 2 symbols.
	DrlMode [3][]uint16

	// Delta Q CDFs.
	// AV1 spec Section 5.11.2 (delta_q_abs). 4 symbols.
	DeltaQ []uint16

	// Delta LF CDFs.
	// AV1 spec Section 5.11.3 (delta_lf_abs). 4 symbols.
	DeltaLF []uint16

	// Multi delta LF CDFs.
	// [frame_lf_count], 4 symbols.
	DeltaLFMulti [4][]uint16

	// Intra angle delta CDF.
	// AV1 spec Section 5.11.9. [mode_ctx], 7 symbols.
	AngleDelta [8][]uint16

	// Filter intra mode CDF.
	// AV1 spec Section 5.11.21 (filter_intra_mode). 5 symbols.
	FilterIntraMode []uint16

	// Use filter intra CDF.
	// AV1 spec Section 5.11.21 (use_filter_intra). [block_size], 2 symbols.
	UseFilterIntra [22][]uint16

	// Palette mode CDFs.
	// AV1 spec Section 5.11.22. [bsize_ctx][palette_ctx], 2 symbols.
	PaletteYMode  [7][3][]uint16
	PaletteUVMode [2][]uint16

	// Palette size CDFs. [plane][sz_ctx], 7 symbols (sizes 2-8).
	PaletteSz [2][7][]uint16

	// Color map CDFs. [plane][pal_sz-2][ctx], up to 8 symbols.
	ColorMap [2][7][5][]uint16

	// CFL (chroma-from-luma) CDFs.
	// AV1 spec Section 5.11.44 (read_cfl_alphas).
	CflSign []uint16    // 8 symbols (joint sign for U and V)
	CflAlpha [6][]uint16 // [ctx], 16 symbols each

	// CDEF index CDF.
	// AV1 spec Section 5.11.45. Variable symbols (1 << cdef_bits).
	CdefIdx []uint16

	// Restoration type CDF.
	// AV1 spec Section 5.11.47. [plane_type], 3 or 4 symbols.
	RestorationType []uint16

	// Use Wiener CDF.
	UseWiener []uint16 // 2 symbols
	// Use SGR CDF.
	UseSGRProj []uint16 // 2 symbols

	// Inter TX type CDFs.
	// AV1 spec Section 5.11.17 (inter_tx_type). [tx_set][tx_size_sqr], variable symbols.
	InterTxType [4][4][]uint16

	// Intra TX type CDFs.
	// AV1 spec Section 5.11.17 (intra_tx_type). [tx_set][tx_size_sqr][intra_mode], variable symbols.
	IntraTxType [3][4][13][]uint16

	// TX type CDFs for intra prediction, dav1d-style indexing.
	// AV1 spec Section 5.11.37 (transform_type).
	// TxTypeIntra1: full set (non-reduced, min_dim < TX_16X16).
	//   [min_dim_log2][intra_mode], 7 symbols (IDTX, DCT_DCT, V_DCT, H_DCT, ADST_ADST, ADST_DCT, DCT_ADST).
	//   min_dim_log2: 0 = TX_4X4, 1 = TX_8X8.
	TxTypeIntra1 [2][13][]uint16

	// TxTypeIntra2: reduced set (or min_dim >= TX_16X16).
	//   [min_dim_log2][intra_mode], 5 symbols (IDTX, DCT_DCT, ADST_ADST, ADST_DCT, DCT_ADST).
	//   min_dim_log2: 0 = TX_4X4, 1 = TX_8X8, 2 = TX_16X16.
	TxTypeIntra2 [3][13][]uint16

	// Reference mode CDF (single vs compound).
	// AV1 spec Section 5.11.9 (reference_mode). [ctx], 2 symbols.
	ReferenceMode [5][]uint16

	// Skip mode CDF.
	// AV1 spec Section 5.11.5 (skip_mode). [ctx], 2 symbols.
	SkipMode [3][]uint16

	// Motion mode CDFs (SIMPLE, OBMC, WARPED).
	// AV1 spec Section 5.11.26 (motion_mode). [block_size], 3 symbols.
	MotionMode [22][]uint16

	// OBMC CDFs (used when warp is not allowed, boolean).
	// Separate from MotionMode per dav1d's obmc[] table. [block_size], 2 symbols.
	OBMC [22][]uint16

	// Is compound CDF.
	// AV1 spec Section 5.11.9 (comp_reference_type). [ctx], 2 symbols.
	CompReferenceType [5][]uint16

	// Inter-intra mode CDFs.
	// AV1 spec Section 5.11.25 (interintra). [bsize_group], 2 symbols.
	InterIntra [4][]uint16

	// Inter-intra mode type CDFs.
	// AV1 spec Section 5.11.25 (interintra_mode). [bsize_group], 4 symbols.
	InterIntraMode [4][]uint16

	// Wedge inter-intra CDF.
	// AV1 spec Section 5.11.25 (wedge_interintra). [bsize], 2 symbols.
	WedgeInterIntra [22][]uint16

	// Compound type CDFs.
	// AV1 spec Section 5.11.27 (compound_type). [bsize], 2 symbols.
	CompoundType [22][]uint16

	// Wedge index CDFs.
	// AV1 spec Section 5.11.27 (wedge_index). [bsize], 16 symbols.
	WedgeIndex [22][]uint16

	// Compound group index CDF.
	// AV1 spec Section 5.11.27 (comp_group_idx). [ctx], 2 symbols.
	CompGroupIdx [6][]uint16

	// Compound index CDF.
	// AV1 spec Section 5.11.27 (compound_idx). [ctx], 2 symbols.
	CompoundIdx [6][]uint16

	// MV joint CDFs.
	// AV1 spec Section 5.11.32 (mv_joint). 4 symbols.
	MVJoint []uint16

	// MV sign CDFs.
	// AV1 spec Section 5.11.33 (mv_sign). [comp], 2 symbols.
	MVSign [2][]uint16

	// MV class CDFs.
	// AV1 spec Section 5.11.33 (mv_class). [comp], 11 symbols.
	MVClass [2][]uint16

	// MV class 0 bit CDFs.
	// AV1 spec Section 5.11.33 (mv_class0_bit). [comp], 2 symbols.
	MVClass0Bit [2][]uint16

	// MV class 0 FR CDFs.
	// AV1 spec Section 5.11.34 (mv_class0_fr). [comp][class0_bit], 4 symbols.
	MVClass0FR [2][2][]uint16

	// MV class 0 HP CDFs.
	// AV1 spec Section 5.11.35 (mv_class0_hp). [comp], 2 symbols.
	MVClass0HP [2][]uint16

	// MV bits CDFs.
	// AV1 spec Section 5.11.33 (mv_bit). [comp][bit_pos], 2 symbols.
	MVBit [2][10][]uint16

	// MV FR CDFs.
	// AV1 spec Section 5.11.34 (mv_fr). [comp], 4 symbols.
	MVFR [2][]uint16

	// MV HP CDFs.
	// AV1 spec Section 5.11.35 (mv_hp). [comp], 2 symbols.
	MVHP [2][]uint16

	// Switchable interpolation filter CDFs.
	// AV1 spec Section 5.11.13. [dim][ctx], 3 symbols.
	SwitchableFilter [2][8][]uint16
}

// getQCtx returns the quantizer context (0-3) for coefficient CDF selection.
// AV1 spec Section 9.4: default CDFs are conditioned on base_q_idx.
// Matches dav1d's get_qcat_idx macro.
func getQCtx(baseQIndex int) int {
	if baseQIndex <= 20 {
		return 0
	}
	if baseQIndex <= 60 {
		return 1
	}
	if baseQIndex <= 120 {
		return 2
	}
	return 3
}

// qCtxTables holds pointers to the Q-context-specific CDF tables.
type qCtxTables struct {
	allZero      *[5][13]uint16
	eobExtra     *[5][2][9]uint16
	eobMulti16   *[2][2][4]uint16
	eobMulti32   *[2][2][5]uint16
	eobMulti64   *[2][2][6]uint16
	eobMulti128  *[2][2][7]uint16
	eobMulti256  *[2][2][8]uint16
	eobMulti512  *[2][2][9]uint16
	eobMulti1024 *[2][2][10]uint16
	dcSign       *[2][3]uint16
	coeffBase    *[5][2][42][3]uint16
	coeffBaseEob *[5][2][4][2]uint16
	coeffBR      *[5][2][21][3]uint16
}

func getQCtxTables(qctx int) qCtxTables {
	switch qctx {
	case 0:
		return qCtxTables{
			&defaultAllZeroCDF_Q0, &defaultEobExtraCDF_Q0,
			&defaultEobMulti16CDF_Q0, &defaultEobMulti32CDF_Q0,
			&defaultEobMulti64CDF_Q0, &defaultEobMulti128CDF_Q0,
			&defaultEobMulti256CDF_Q0, &defaultEobMulti512CDF_Q0,
			&defaultEobMulti1024CDF_Q0, &defaultDcSignCDF_Q0,
			&defaultCoeffBaseCDF_Q0, &defaultCoeffBaseEobCDF_Q0,
			&defaultCoeffBRCDF_Q0,
		}
	case 1:
		return qCtxTables{
			&defaultAllZeroCDF_Q1, &defaultEobExtraCDF_Q1,
			&defaultEobMulti16CDF_Q1, &defaultEobMulti32CDF_Q1,
			&defaultEobMulti64CDF_Q1, &defaultEobMulti128CDF_Q1,
			&defaultEobMulti256CDF_Q1, &defaultEobMulti512CDF_Q1,
			&defaultEobMulti1024CDF_Q1, &defaultDcSignCDF_Q1,
			&defaultCoeffBaseCDF_Q1, &defaultCoeffBaseEobCDF_Q1,
			&defaultCoeffBRCDF_Q1,
		}
	case 3:
		return qCtxTables{
			&defaultAllZeroCDF_Q3, &defaultEobExtraCDF_Q3,
			&defaultEobMulti16CDF_Q3, &defaultEobMulti32CDF_Q3,
			&defaultEobMulti64CDF_Q3, &defaultEobMulti128CDF_Q3,
			&defaultEobMulti256CDF_Q3, &defaultEobMulti512CDF_Q3,
			&defaultEobMulti1024CDF_Q3, &defaultDcSignCDF_Q3,
			&defaultCoeffBaseCDF_Q3, &defaultCoeffBaseEobCDF_Q3,
			&defaultCoeffBRCDF_Q3,
		}
	default: // qctx == 2
		return qCtxTables{
			&defaultAllZeroCDF_Q2, &defaultEobExtraCDF_Q2,
			&defaultEobMulti16CDF_Q2, &defaultEobMulti32CDF_Q2,
			&defaultEobMulti64CDF_Q2, &defaultEobMulti128CDF_Q2,
			&defaultEobMulti256CDF_Q2, &defaultEobMulti512CDF_Q2,
			&defaultEobMulti1024CDF_Q2, &defaultDcSignCDF_Q2,
			&defaultCoeffBaseCDF_Q2, &defaultCoeffBaseEobCDF_Q2,
			&defaultCoeffBRCDF_Q2,
		}
	}
}

// NewDefaultCDFContext creates a CDFContext with all tables initialized to
// the AV1 specification default CDF values from Section 9.4.
// These values are empirically tuned and are required for conformant decoding.
// The default tables are defined in cdf_defaults.go, sourced from the libaom
// and dav1d reference implementations.
func NewDefaultCDFContext() *CDFContext {
	return NewDefaultCDFContextForQP(80) // Q_CTX=2 for backward compat
}

// NewDefaultCDFContextForQP creates a CDFContext with Q-context-specific
// coefficient CDF tables selected based on baseQIndex.
func NewDefaultCDFContextForQP(baseQIndex int) *CDFContext {
	ctx := &CDFContext{}

	// clone converts a CDF slice from ascending probability format (as in
	// the AV1 spec / AOM_CDF macros) to the ICDF format (32768 - prob)
	// used by our complement-based arithmetic decoder. CDF entries at
	// indices 0..len-3 are converted; the last two entries (implicit
	// ceiling and adaptation counter, both 0) are left unchanged.
	clone := func(src []uint16) []uint16 {
		dst := make([]uint16, len(src))
		for i := 0; i < len(src)-2; i++ {
			dst[i] = 32768 - src[i]
		}
		// Last two entries stay as-is (ceiling=0, counter=0)
		dst[len(src)-2] = src[len(src)-2]
		dst[len(src)-1] = src[len(src)-1]
		return dst
	}

	// icdf converts a single ascending probability value to ICDF.
	icdf := func(v uint16) uint16 { return 32768 - v }

	// Partition CDFs. AV1 spec Section 9.4.
	for i := range defaultPartitionCDF {
		ctx.Partition[i] = clone(defaultPartitionCDF[i])
	}

	// SplitOrHorz and SplitOrVert CDFs for boundary partition decisions.
	// Use uniform 2-symbol CDFs (no spec defaults available separately).
	for i := 0; i < 24; i++ {
		ctx.SplitOrHorz[i] = InitCDF(2)
		ctx.SplitOrVert[i] = InitCDF(2)
	}

	// Skip (txb_skip) CDFs. AV1 spec Section 9.4.
	for i := range defaultSkipCDF {
		ctx.Skip[i] = clone(defaultSkipCDF[i])
	}

	// IntraBC CDF. AV1 spec Section 9.4.
	// Default: AOM_CDF2(30531) -> ascending {30531, 0} -> ICDF {32768-30531, 0} = {2237, 0}
	ctx.IntraBC = []uint16{icdf(30531), 0}

	// IntraFrameYMode CDFs. AV1 spec Section 9.4.
	// 5x5 contexts indexed by intra_mode_context mapping.
	for i := 0; i < 5; i++ {
		for j := 0; j < 5; j++ {
			ctx.IntraFrameYMode[i][j] = clone(defaultKfYModeCDF[i][j])
		}
	}

	// YMode (non-keyframe) CDFs. AV1 spec Section 9.4.
	for i := range defaultYModeCDF {
		ctx.YMode[i] = clone(defaultYModeCDF[i])
	}

	// UVMode CDFs. AV1 spec Section 9.4.
	for i := 0; i < 2; i++ {
		for j := 0; j < 13; j++ {
			ctx.UVMode[i][j] = clone(defaultUVModeCDF[i][j])
		}
	}

	// TxSize CDFs. AV1 spec Section 9.4.
	for cat := 0; cat < 4; cat++ {
		for txCtx := 0; txCtx < 3; txCtx++ {
			ctx.TxSize[cat][txCtx] = clone(defaultTxSizeCDF[cat][txCtx])
		}
	}

	// TxSplit CDFs for inter variable TX tree.
	// Default values from AV1 spec / dav1d txpart table.
	// cat = 2*(4-max)-depth: 0=64x depth0, 1=32x depth1, 2=32x depth0,
	// 3=16x depth1, 4=16x depth0, 5=8x depth1, 6=8x depth0.
	defaultTxSplit := [7][3]uint16{
		{28581, 23846, 20847}, // cat 0
		{24315, 18196, 12133}, // cat 1
		{18791, 10887, 11005}, // cat 2
		{27179, 20004, 11281}, // cat 3
		{26549, 19308, 14224}, // cat 4
		{28015, 21546, 14400}, // cat 5
		{28165, 22401, 16088}, // cat 6
	}
	for cat := 0; cat < 7; cat++ {
		for c := 0; c < 3; c++ {
			ctx.TxSplit[cat][c] = clone([]uint16{defaultTxSplit[cat][c], 0, 0})
		}
	}

	// Select Q-context-specific CDF tables based on baseQIndex.
	qt := getQCtxTables(getQCtx(baseQIndex))

	// AllZero (txb_skip) CDFs.
	for txSz := 0; txSz < 5; txSz++ {
		for txbCtx := 0; txbCtx < 13; txbCtx++ {
			v := qt.allZero[txSz][txbCtx]
			ctx.AllZero[txSz][txbCtx] = []uint16{icdf(v), 0, 0}
		}
	}

	// EOB CDFs per transform size, both eob_cdf_idx contexts.
	for pt := 0; pt < 2; pt++ {
		for ei := 0; ei < 2; ei++ {
			v16 := qt.eobMulti16[pt][ei]
			ctx.EobPt16[pt][ei] = []uint16{icdf(v16[0]), icdf(v16[1]), icdf(v16[2]), icdf(v16[3]), 0, 0}
			v32 := qt.eobMulti32[pt][ei]
			ctx.EobPt32[pt][ei] = []uint16{icdf(v32[0]), icdf(v32[1]), icdf(v32[2]), icdf(v32[3]), icdf(v32[4]), 0, 0}
			v64 := qt.eobMulti64[pt][ei]
			ctx.EobPt64[pt][ei] = []uint16{icdf(v64[0]), icdf(v64[1]), icdf(v64[2]), icdf(v64[3]), icdf(v64[4]), icdf(v64[5]), 0, 0}
			v128 := qt.eobMulti128[pt][ei]
			ctx.EobPt128[pt][ei] = []uint16{icdf(v128[0]), icdf(v128[1]), icdf(v128[2]), icdf(v128[3]), icdf(v128[4]), icdf(v128[5]), icdf(v128[6]), 0, 0}
			v256 := qt.eobMulti256[pt][ei]
			ctx.EobPt256[pt][ei] = []uint16{icdf(v256[0]), icdf(v256[1]), icdf(v256[2]), icdf(v256[3]), icdf(v256[4]), icdf(v256[5]), icdf(v256[6]), icdf(v256[7]), 0, 0}
			v512 := qt.eobMulti512[pt][ei]
			ctx.EobPt512[pt][ei] = []uint16{icdf(v512[0]), icdf(v512[1]), icdf(v512[2]), icdf(v512[3]), icdf(v512[4]), icdf(v512[5]), icdf(v512[6]), icdf(v512[7]), icdf(v512[8]), 0, 0}
			v1024 := qt.eobMulti1024[pt][ei]
			ctx.EobPt1024[pt][ei] = []uint16{icdf(v1024[0]), icdf(v1024[1]), icdf(v1024[2]), icdf(v1024[3]), icdf(v1024[4]), icdf(v1024[5]), icdf(v1024[6]), icdf(v1024[7]), icdf(v1024[8]), icdf(v1024[9]), 0, 0}
		}
	}

	// EobExtra CDFs.
	for i := 0; i < 5; i++ {
		for j := 0; j < 2; j++ {
			for k := 0; k < 9; k++ {
				ctx.EobExtra[i][j][k] = []uint16{icdf(qt.eobExtra[i][j][k]), 0, 0}
			}
		}
	}

	// CoeffBase CDFs.
	for i := 0; i < 5; i++ {
		for j := 0; j < 2; j++ {
			for k := 0; k < 42; k++ {
				v := qt.coeffBase[i][j][k]
				ctx.CoeffBase[i][j][k] = []uint16{icdf(v[0]), icdf(v[1]), icdf(v[2]), 0, 0}
			}
		}
	}

	// CoeffBaseEob CDFs.
	for i := 0; i < 5; i++ {
		for j := 0; j < 2; j++ {
			for k := 0; k < 4; k++ {
				v := qt.coeffBaseEob[i][j][k]
				ctx.CoeffBaseEob[i][j][k] = []uint16{icdf(v[0]), icdf(v[1]), 0, 0}
			}
		}
	}

	// DcSign CDFs.
	for i := 0; i < 2; i++ {
		for j := 0; j < 3; j++ {
			ctx.DcSign[i][j] = []uint16{icdf(qt.dcSign[i][j]), 0, 0}
		}
	}

	// CoeffBR CDFs.
	for i := 0; i < 5; i++ {
		for j := 0; j < 2; j++ {
			for k := 0; k < 21; k++ {
				v := qt.coeffBR[i][j][k]
				ctx.CoeffBR[i][j][k] = []uint16{icdf(v[0]), icdf(v[1]), icdf(v[2]), 0, 0}
			}
		}
	}

	// SegmentID CDFs. AV1 spec Section 9.4.
	for i := range defaultSegmentIDCDF {
		ctx.SegmentID[i] = clone(defaultSegmentIDCDF[i])
	}

	// SegIDPredicted CDFs. AV1 spec Section 9.4.
	for i := range defaultSegIDPredictedCDF {
		ctx.SegIDPredicted[i] = clone(defaultSegIDPredictedCDF[i])
	}

	// IsInter CDFs. AV1 spec Section 9.4.
	for i := range defaultIsInterCDF {
		ctx.IsInter[i] = clone(defaultIsInterCDF[i])
	}

	// CompRef CDFs. AV1 spec Section 9.4.
	for i := 0; i < 3; i++ {
		for j := 0; j < 6; j++ {
			ctx.CompRef[i][j] = clone(defaultCompRefCDF[i][j])
		}
	}

	// CompBwdRef CDFs.
	for i := 0; i < 2; i++ {
		for j := 0; j < 3; j++ {
			ctx.CompBwdRef[i][j] = clone(defaultCompBwdRefCDF[i][j])
		}
	}

	// CompUniRef CDFs.
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			ctx.CompUniRef[i][j] = clone(defaultCompUniRefCDF[i][j])
		}
	}

	// SingleRef CDFs. AV1 spec Section 9.4. [level][ctx].
	for i := 0; i < 6; i++ {
		for j := 0; j < 3; j++ {
			ctx.SingleRef[i][j] = clone(defaultSingleRefCDF[i][j])
		}
	}

	// CompoundMode CDFs. AV1 spec Section 9.4.
	for i := range defaultCompoundModeCDF {
		ctx.CompoundMode[i] = clone(defaultCompoundModeCDF[i])
	}

	// NewMV CDFs. AV1 spec Section 9.4.
	for i := range defaultNewMVCDF {
		ctx.NewMV[i] = clone(defaultNewMVCDF[i])
	}

	// ZeroMV CDFs. AV1 spec Section 9.4.
	for i := range defaultZeroMVCDF {
		ctx.ZeroMV[i] = clone(defaultZeroMVCDF[i])
	}

	// RefMV CDFs. AV1 spec Section 9.4.
	for i := range defaultRefMVCDF {
		ctx.RefMV[i] = clone(defaultRefMVCDF[i])
	}

	// DrlMode CDFs. AV1 spec Section 9.4.
	for i := range defaultDrlModeCDF {
		ctx.DrlMode[i] = clone(defaultDrlModeCDF[i])
	}

	// DeltaQ CDF. AV1 spec Section 9.4.
	ctx.DeltaQ = clone(defaultDeltaQCDF)

	// DeltaLF CDF. AV1 spec Section 9.4.
	ctx.DeltaLF = clone(defaultDeltaLFCDF)

	// DeltaLFMulti CDFs. AV1 spec Section 9.4.
	for i := range defaultDeltaLFMultiCDF {
		ctx.DeltaLFMulti[i] = clone(defaultDeltaLFMultiCDF[i])
	}

	// AngleDelta CDFs. AV1 spec Section 9.4.
	for i := range defaultAngleDeltaCDF {
		ctx.AngleDelta[i] = clone(defaultAngleDeltaCDF[i])
	}

	// FilterIntraMode CDF. AV1 spec Section 9.4.
	ctx.FilterIntraMode = clone(defaultFilterIntraModeCDF)

	// UseFilterIntra CDFs. AV1 spec Section 9.4.
	for i := range defaultUseFilterIntraCDF {
		ctx.UseFilterIntra[i] = clone(defaultUseFilterIntraCDF[i])
	}

	// PaletteYMode CDFs. AV1 spec Section 9.4.
	for i := 0; i < 7; i++ {
		for j := 0; j < 3; j++ {
			ctx.PaletteYMode[i][j] = clone(defaultPaletteYModeCDF[i][j])
		}
	}

	// PaletteUVMode CDFs. AV1 spec Section 9.4.
	for i := range defaultPaletteUVModeCDF {
		ctx.PaletteUVMode[i] = clone(defaultPaletteUVModeCDF[i])
	}

	// PaletteSz CDFs.
	for pl := 0; pl < 2; pl++ {
		for sc := 0; sc < 7; sc++ {
			ctx.PaletteSz[pl][sc] = clone(defaultPaletteSzCDF[pl][sc])
		}
	}

	// ColorMap CDFs.
	for pl := 0; pl < 2; pl++ {
		for ps := 0; ps < 7; ps++ {
			for c := 0; c < 5; c++ {
				ctx.ColorMap[pl][ps][c] = clone(defaultColorMapCDF[pl][ps][c])
			}
		}
	}

	// CFL CDFs. AV1 spec Section 9.4.
	ctx.CflSign = clone(defaultCflSignCDF)
	for i := range defaultCflAlphaCDF {
		ctx.CflAlpha[i] = clone(defaultCflAlphaCDF[i])
	}

	// CdefIdx: default to 8 symbols (max 1 << 3). Uniform is correct here
	// since the actual CDF depends on cdef_bits which varies per frame.
	ctx.CdefIdx = InitCDF(8)

	// RestorationType CDF. AV1 spec Section 9.4.
	ctx.RestorationType = clone(defaultRestorationTypeCDF)

	// UseWiener CDF. AV1 spec Section 9.4.
	ctx.UseWiener = clone(defaultUseWienerCDF)

	// UseSGRProj CDF. AV1 spec Section 9.4.
	ctx.UseSGRProj = clone(defaultUseSGRProjCDF)

	// InterTxType CDFs. AV1 spec Section 9.4. dav1d convention:
	//   set 0: unused
	//   set 1: 2 symbols (IDTX, DCT_DCT) — for reduced tx set or min_dim=3 (32x32)
	//   set 2: 12 symbols — for min_dim=2 (16x16)
	//   set 3: 16 symbols — for min_dim=0,1 (4x4, 8x8)
	// Default CDF values from dav1d cdf.c (ICDF = 32768 - raw_value).
	// set 0: unused, initialize as 2-symbol uniform
	for sz := 0; sz < 4; sz++ {
		ctx.InterTxType[0][sz] = InitCDF(2)
	}
	// set 1 (inter3): boolean, 4 contexts by min_dim (0..3)
	ctx.InterTxType[1][0] = []uint16{32768 - 16384, 0, 0}
	ctx.InterTxType[1][1] = []uint16{32768 - 4167, 0, 0}
	ctx.InterTxType[1][2] = []uint16{32768 - 1998, 0, 0}
	ctx.InterTxType[1][3] = []uint16{32768 - 748, 0, 0}
	// set 2 (inter2): 12 symbols, no context dimension (same for all min_dim)
	inter2CDF := func() []uint16 {
		// CDF11(770, 2421, 5225, 12907, 15819, 18927, 21561, 24089, 26595, 28526, 30529)
		return []uint16{
			32768 - 770, 32768 - 2421, 32768 - 5225, 32768 - 12907,
			32768 - 15819, 32768 - 18927, 32768 - 21561, 32768 - 24089,
			32768 - 26595, 32768 - 28526, 32768 - 30529, 0, 0,
		}
	}
	for sz := 0; sz < 4; sz++ {
		ctx.InterTxType[2][sz] = inter2CDF()
	}
	// set 3 (inter1): 16 symbols, 2 contexts (min_dim=0 and min_dim=1)
	// CDF15(4458, 5560, 7695, 9709, 13330, 14789, 17537, 20266, 21504, 22848, 23934, 25474, 27727, 28915, 30631)
	ctx.InterTxType[3][0] = []uint16{
		32768 - 4458, 32768 - 5560, 32768 - 7695, 32768 - 9709,
		32768 - 13330, 32768 - 14789, 32768 - 17537, 32768 - 20266,
		32768 - 21504, 32768 - 22848, 32768 - 23934, 32768 - 25474,
		32768 - 27727, 32768 - 28915, 32768 - 30631, 0, 0,
	}
	// CDF15(1645, 2573, 4778, 5711, 7807, 8622, 10522, 15357, 17674, 20408, 22517, 25010, 27116, 28856, 30749)
	ctx.InterTxType[3][1] = []uint16{
		32768 - 1645, 32768 - 2573, 32768 - 4778, 32768 - 5711,
		32768 - 7807, 32768 - 8622, 32768 - 10522, 32768 - 15357,
		32768 - 17674, 32768 - 20408, 32768 - 22517, 32768 - 25010,
		32768 - 27116, 32768 - 28856, 32768 - 30749, 0, 0,
	}
	// min_dim=2 and min_dim=3 for set 3 shouldn't be used (set 2 handles min_dim=2,
	// set 1 handles min_dim=3), but initialize with uniform just in case.
	ctx.InterTxType[3][2] = InitCDF(16)
	ctx.InterTxType[3][3] = InitCDF(16)
	// txSetSyms used for intra below.
	txSetSyms := [4]int{1, 7, 12, 2}

	// IntraTxType CDFs. AV1 spec Section 9.4.
	for set := 0; set < 3; set++ {
		nsyms := txSetSyms[set+1]
		if nsyms < 2 {
			nsyms = 2
		}
		for sz := 0; sz < 4; sz++ {
			for mode := 0; mode < 13; mode++ {
				ctx.IntraTxType[set][sz][mode] = InitCDF(nsyms)
			}
		}
	}

	// TxTypeIntra1 CDFs (dav1d-style). Full set, 7 symbols.
	// Default CDF values from dav1d cdf.c (default_coef_cdf, txtp_intra1).
	// Ascending format CDF6 values; convert to ICDF via icdf().
	// [2][13]: [min_dim_log2][intra_mode]
	{
		// min_dim_log2 = 0 (TX_4X4)
		type cdf6 = [6]uint16
		txtp1Ascending := [2][13]cdf6{
			{ // min_dim=0 (TX_4X4)
				{1535, 8035, 9461, 12751, 23467, 27825},   // DC_PRED
				{564, 3335, 9709, 10870, 18143, 28094},    // V_PRED
				{672, 3247, 3676, 11982, 19415, 23127},    // H_PRED
				{5279, 13885, 15487, 18044, 23527, 30252}, // D45_PRED
				{4423, 6074, 7985, 10416, 25693, 29298},   // D135_PRED
				{1486, 4241, 9460, 10662, 16456, 27694},   // D113_PRED
				{439, 2838, 3522, 6737, 18058, 23754},     // D157_PRED
				{1190, 4233, 4855, 11670, 20281, 24377},   // D203_PRED
				{1045, 4312, 8647, 10159, 18644, 29335},   // D67_PRED
				{202, 3734, 4747, 7298, 17127, 24016},     // SMOOTH
				{447, 4312, 6819, 8884, 16010, 23858},     // SMOOTH_V
				{277, 4369, 5255, 8905, 16465, 22271},     // SMOOTH_H
				{3409, 5436, 10599, 15599, 19687, 24040}, // PAETH
			},
			{ // min_dim=1 (TX_8X8)
				{1870, 13742, 14530, 16498, 23770, 27698},
				{326, 8796, 14632, 15079, 19272, 27486},
				{484, 7576, 7712, 14443, 19159, 22591},
				{1126, 15340, 15895, 17023, 20896, 30279},
				{655, 4854, 5249, 5913, 22099, 27138},
				{1299, 6458, 8885, 9290, 14851, 25497},
				{311, 5295, 5552, 6885, 16107, 22672},
				{883, 8059, 8270, 11258, 17289, 21549},
				{741, 7580, 9318, 10345, 16688, 29046},
				{110, 7406, 7915, 9195, 16041, 23329},
				{363, 7974, 9357, 10673, 15629, 24474},
				{153, 7647, 8112, 9936, 15307, 19996},
				{3511, 6332, 11165, 15335, 19323, 23594},
			},
		}
		for dim := 0; dim < 2; dim++ {
			for mode := 0; mode < 13; mode++ {
				cdf := make([]uint16, 8) // 6 ICDF values + sentinel 0 + counter 0
				for k := 0; k < 6; k++ {
					cdf[k] = icdf(txtp1Ascending[dim][mode][k])
				}
				// cdf[6] = 0 (sentinel), cdf[7] = 0 (counter)
				ctx.TxTypeIntra1[dim][mode] = cdf
			}
		}
	}

	// TxTypeIntra2 CDFs (dav1d-style). Reduced set, 5 symbols.
	// [3][13]: [min_dim_log2][intra_mode]
	// dim=0 and dim=1 are uniform; dim=2 has non-uniform defaults from dav1d cdf.c.
	{
		txtp2Dim2 := [13][4]uint16{
			{1127, 12814, 22772, 27483}, // DC_PRED
			{145, 6761, 11980, 26667},   // V_PRED
			{362, 5887, 11678, 16725},   // H_PRED
			{385, 15213, 18587, 30693},  // D45_PRED
			{25, 2914, 23134, 27903},    // D135_PRED
			{60, 4470, 11749, 23991},    // D113_PRED
			{37, 3332, 14511, 21448},    // D157_PRED
			{157, 6320, 13036, 17439},   // D203_PRED
			{119, 6719, 12906, 29396},   // D67_PRED
			{47, 5537, 12576, 21499},    // SMOOTH
			{269, 6076, 11258, 23115},   // SMOOTH_V
			{83, 5615, 12001, 17228},    // SMOOTH_H
			{1968, 5556, 12023, 18547},  // PAETH
		}
		for dim := 0; dim < 3; dim++ {
			for mode := 0; mode < 13; mode++ {
				cdf := make([]uint16, 6) // 4 ICDF values + sentinel 0 + counter 0
				if dim < 2 {
					cdf[0] = icdf(6554)
					cdf[1] = icdf(13107)
					cdf[2] = icdf(19661)
					cdf[3] = icdf(26214)
				} else {
					cdf[0] = icdf(txtp2Dim2[mode][0])
					cdf[1] = icdf(txtp2Dim2[mode][1])
					cdf[2] = icdf(txtp2Dim2[mode][2])
					cdf[3] = icdf(txtp2Dim2[mode][3])
				}
				// cdf[4] = 0 (sentinel), cdf[5] = 0 (counter)
				ctx.TxTypeIntra2[dim][mode] = cdf
			}
		}
	}

	// ReferenceMode CDFs. AV1 spec Section 9.4.
	for i := range defaultReferenceModeCDF {
		ctx.ReferenceMode[i] = clone(defaultReferenceModeCDF[i])
	}

	// SkipMode CDFs. AV1 spec Section 9.4.
	for i := range defaultSkipModeCDF {
		ctx.SkipMode[i] = clone(defaultSkipModeCDF[i])
	}

	// MotionMode CDFs. AV1 spec Section 9.4.
	for i := range defaultMotionModeCDF {
		ctx.MotionMode[i] = clone(defaultMotionModeCDF[i])
	}

	// OBMC CDFs (separate from MotionMode).
	for i := range defaultOBMCCDF {
		ctx.OBMC[i] = clone(defaultOBMCCDF[i])
	}

	// CompReferenceType CDFs. AV1 spec Section 9.4.
	for i := range defaultCompReferenceTypeCDF {
		ctx.CompReferenceType[i] = clone(defaultCompReferenceTypeCDF[i])
	}

	// InterIntra CDFs. AV1 spec Section 9.4.
	for i := range defaultInterIntraCDF {
		ctx.InterIntra[i] = clone(defaultInterIntraCDF[i])
	}

	// InterIntraMode CDFs. AV1 spec Section 9.4.
	for i := range defaultInterIntraModeCDF {
		ctx.InterIntraMode[i] = clone(defaultInterIntraModeCDF[i])
	}

	// WedgeInterIntra CDFs. AV1 spec Section 9.4.
	for i := range defaultWedgeInterIntraCDF {
		ctx.WedgeInterIntra[i] = clone(defaultWedgeInterIntraCDF[i])
	}

	// CompoundType CDFs. AV1 spec Section 9.4.
	for i := range defaultCompoundTypeCDF {
		ctx.CompoundType[i] = clone(defaultCompoundTypeCDF[i])
	}

	// WedgeIndex CDFs. AV1 spec Section 9.4.
	for i := range defaultWedgeIndexCDF {
		ctx.WedgeIndex[i] = clone(defaultWedgeIndexCDF[i])
	}

	// CompGroupIdx CDFs. AV1 spec Section 9.4.
	for i := range defaultCompGroupIdxCDF {
		ctx.CompGroupIdx[i] = clone(defaultCompGroupIdxCDF[i])
	}

	// CompoundIdx CDFs. AV1 spec Section 9.4.
	for i := range defaultCompoundIdxCDF {
		ctx.CompoundIdx[i] = clone(defaultCompoundIdxCDF[i])
	}

	// MV joint CDF. AV1 spec Section 9.4.
	ctx.MVJoint = clone(defaultMVJointCDF)

	// MV component CDFs. AV1 spec Section 9.4.
	for comp := 0; comp < 2; comp++ {
		ctx.MVSign[comp] = clone(defaultMVSignCDF[comp])
		ctx.MVClass[comp] = clone(defaultMVClassCDF[comp])
		ctx.MVClass0Bit[comp] = clone(defaultMVClass0BitCDF[comp])
		for c0 := 0; c0 < 2; c0++ {
			ctx.MVClass0FR[comp][c0] = clone(defaultMVClass0FRCDF[comp][c0])
		}
		ctx.MVClass0HP[comp] = clone(defaultMVClass0HPCDF[comp])
		for bit := 0; bit < 10; bit++ {
			ctx.MVBit[comp][bit] = clone(defaultMVBitCDF[comp][bit])
		}
		ctx.MVFR[comp] = clone(defaultMVFRCDF[comp])
		ctx.MVHP[comp] = clone(defaultMVHPCDF[comp])
	}

	// SwitchableFilter CDFs. AV1 spec Section 9.4.
	for dim := 0; dim < 2; dim++ {
		for c := 0; c < 8; c++ {
			ctx.SwitchableFilter[dim][c] = clone(defaultSwitchableFilterCDF[dim][c])
		}
	}

	return ctx
}

// Clone creates a deep copy of the CDFContext. This is needed because each
// tile in AV1 starts with a copy of the CDFs (from the reference frame
// designated by primary_ref_frame), and adapts them independently during
// decoding. After decoding, the CDFs from context_update_tile_id become
// the forward-updated CDFs for subsequent frames.
//
// AV1 spec Section 7.20 (CDF update process).
func (c *CDFContext) Clone() *CDFContext {
	dst := &CDFContext{}

	// Helper to clone a single CDF slice.
	cloneCDF := func(src []uint16) []uint16 {
		if src == nil {
			return nil
		}
		out := make([]uint16, len(src))
		copy(out, src)
		return out
	}

	// Partition.
	for i := range c.Partition {
		dst.Partition[i] = cloneCDF(c.Partition[i])
	}

	// SplitOrHorz/SplitOrVert.
	for i := range c.SplitOrHorz {
		dst.SplitOrHorz[i] = cloneCDF(c.SplitOrHorz[i])
	}
	for i := range c.SplitOrVert {
		dst.SplitOrVert[i] = cloneCDF(c.SplitOrVert[i])
	}

	// Skip.
	for i := range c.Skip {
		dst.Skip[i] = cloneCDF(c.Skip[i])
	}

	// IntraBC.
	dst.IntraBC = cloneCDF(c.IntraBC)

	// IntraFrameYMode.
	for i := 0; i < 5; i++ {
		for j := 0; j < 5; j++ {
			dst.IntraFrameYMode[i][j] = cloneCDF(c.IntraFrameYMode[i][j])
		}
	}

	// YMode.
	for i := range c.YMode {
		dst.YMode[i] = cloneCDF(c.YMode[i])
	}

	// UVMode.
	for i := 0; i < 2; i++ {
		for j := 0; j < 13; j++ {
			dst.UVMode[i][j] = cloneCDF(c.UVMode[i][j])
		}
	}

	// TxSize.
	for i := 0; i < 4; i++ {
		for j := 0; j < 3; j++ {
			dst.TxSize[i][j] = cloneCDF(c.TxSize[i][j])
		}
	}

	// TxSplit.
	for i := 0; i < 7; i++ {
		for j := 0; j < 3; j++ {
			dst.TxSplit[i][j] = cloneCDF(c.TxSplit[i][j])
		}
	}

	// AllZero.
	for i := 0; i < 5; i++ {
		for j := 0; j < 13; j++ {
			dst.AllZero[i][j] = cloneCDF(c.AllZero[i][j])
		}
	}

	// EOB CDFs.
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			dst.EobPt16[i][j] = cloneCDF(c.EobPt16[i][j])
			dst.EobPt32[i][j] = cloneCDF(c.EobPt32[i][j])
			dst.EobPt64[i][j] = cloneCDF(c.EobPt64[i][j])
			dst.EobPt128[i][j] = cloneCDF(c.EobPt128[i][j])
			dst.EobPt256[i][j] = cloneCDF(c.EobPt256[i][j])
			dst.EobPt512[i][j] = cloneCDF(c.EobPt512[i][j])
			dst.EobPt1024[i][j] = cloneCDF(c.EobPt1024[i][j])
		}
	}

	// EobExtra.
	for i := 0; i < 5; i++ {
		for j := 0; j < 2; j++ {
			for k := 0; k < 9; k++ {
				dst.EobExtra[i][j][k] = cloneCDF(c.EobExtra[i][j][k])
			}
		}
	}

	// CoeffBase.
	for i := 0; i < 5; i++ {
		for j := 0; j < 2; j++ {
			for k := 0; k < 42; k++ {
				dst.CoeffBase[i][j][k] = cloneCDF(c.CoeffBase[i][j][k])
			}
		}
	}

	// CoeffBaseEob.
	for i := 0; i < 5; i++ {
		for j := 0; j < 2; j++ {
			for k := 0; k < 4; k++ {
				dst.CoeffBaseEob[i][j][k] = cloneCDF(c.CoeffBaseEob[i][j][k])
			}
		}
	}

	// DcSign.
	for i := 0; i < 2; i++ {
		for j := 0; j < 3; j++ {
			dst.DcSign[i][j] = cloneCDF(c.DcSign[i][j])
		}
	}

	// CoeffBR.
	for i := 0; i < 5; i++ {
		for j := 0; j < 2; j++ {
			for k := 0; k < 21; k++ {
				dst.CoeffBR[i][j][k] = cloneCDF(c.CoeffBR[i][j][k])
			}
		}
	}

	// SegmentID.
	for i := range c.SegmentID {
		dst.SegmentID[i] = cloneCDF(c.SegmentID[i])
	}

	// SegIDPredicted.
	for i := range c.SegIDPredicted {
		dst.SegIDPredicted[i] = cloneCDF(c.SegIDPredicted[i])
	}

	// IsInter.
	for i := range c.IsInter {
		dst.IsInter[i] = cloneCDF(c.IsInter[i])
	}

	// CompRef.
	for i := 0; i < 3; i++ {
		for j := 0; j < 6; j++ {
			dst.CompRef[i][j] = cloneCDF(c.CompRef[i][j])
		}
	}

	// CompBwdRef.
	for i := 0; i < 2; i++ {
		for j := 0; j < 3; j++ {
			dst.CompBwdRef[i][j] = cloneCDF(c.CompBwdRef[i][j])
		}
	}

	// CompUniRef.
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			dst.CompUniRef[i][j] = cloneCDF(c.CompUniRef[i][j])
		}
	}

	// SingleRef.
	for i := 0; i < 6; i++ {
		for j := 0; j < 3; j++ {
			dst.SingleRef[i][j] = cloneCDF(c.SingleRef[i][j])
		}
	}

	// CompoundMode.
	for i := range c.CompoundMode {
		dst.CompoundMode[i] = cloneCDF(c.CompoundMode[i])
	}

	// NewMV, ZeroMV, RefMV, DrlMode.
	for i := range c.NewMV {
		dst.NewMV[i] = cloneCDF(c.NewMV[i])
	}
	for i := range c.ZeroMV {
		dst.ZeroMV[i] = cloneCDF(c.ZeroMV[i])
	}
	for i := range c.RefMV {
		dst.RefMV[i] = cloneCDF(c.RefMV[i])
	}
	for i := range c.DrlMode {
		dst.DrlMode[i] = cloneCDF(c.DrlMode[i])
	}

	// Delta Q/LF.
	dst.DeltaQ = cloneCDF(c.DeltaQ)
	dst.DeltaLF = cloneCDF(c.DeltaLF)
	for i := range c.DeltaLFMulti {
		dst.DeltaLFMulti[i] = cloneCDF(c.DeltaLFMulti[i])
	}

	// AngleDelta.
	for i := range c.AngleDelta {
		dst.AngleDelta[i] = cloneCDF(c.AngleDelta[i])
	}

	// FilterIntraMode.
	dst.FilterIntraMode = cloneCDF(c.FilterIntraMode)

	// UseFilterIntra.
	for i := range c.UseFilterIntra {
		dst.UseFilterIntra[i] = cloneCDF(c.UseFilterIntra[i])
	}

	// Palette.
	for i := 0; i < 7; i++ {
		for j := 0; j < 3; j++ {
			dst.PaletteYMode[i][j] = cloneCDF(c.PaletteYMode[i][j])
		}
	}
	for i := range c.PaletteUVMode {
		dst.PaletteUVMode[i] = cloneCDF(c.PaletteUVMode[i])
	}
	for pl := 0; pl < 2; pl++ {
		for sc := 0; sc < 7; sc++ {
			dst.PaletteSz[pl][sc] = cloneCDF(c.PaletteSz[pl][sc])
		}
	}
	for pl := 0; pl < 2; pl++ {
		for ps := 0; ps < 7; ps++ {
			for ct := 0; ct < 5; ct++ {
				dst.ColorMap[pl][ps][ct] = cloneCDF(c.ColorMap[pl][ps][ct])
			}
		}
	}

	// CFL.
	dst.CflSign = cloneCDF(c.CflSign)
	for i := range c.CflAlpha {
		dst.CflAlpha[i] = cloneCDF(c.CflAlpha[i])
	}

	// CDEF.
	dst.CdefIdx = cloneCDF(c.CdefIdx)

	// Restoration.
	dst.RestorationType = cloneCDF(c.RestorationType)
	dst.UseWiener = cloneCDF(c.UseWiener)
	dst.UseSGRProj = cloneCDF(c.UseSGRProj)

	// InterTxType.
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			dst.InterTxType[i][j] = cloneCDF(c.InterTxType[i][j])
		}
	}

	// IntraTxType.
	for i := 0; i < 3; i++ {
		for j := 0; j < 4; j++ {
			for k := 0; k < 13; k++ {
				dst.IntraTxType[i][j][k] = cloneCDF(c.IntraTxType[i][j][k])
			}
		}
	}

	// TxTypeIntra1.
	for i := 0; i < 2; i++ {
		for j := 0; j < 13; j++ {
			dst.TxTypeIntra1[i][j] = cloneCDF(c.TxTypeIntra1[i][j])
		}
	}

	// TxTypeIntra2.
	for i := 0; i < 3; i++ {
		for j := 0; j < 13; j++ {
			dst.TxTypeIntra2[i][j] = cloneCDF(c.TxTypeIntra2[i][j])
		}
	}

	// ReferenceMode.
	for i := range c.ReferenceMode {
		dst.ReferenceMode[i] = cloneCDF(c.ReferenceMode[i])
	}

	// SkipMode.
	for i := range c.SkipMode {
		dst.SkipMode[i] = cloneCDF(c.SkipMode[i])
	}

	// MotionMode.
	for i := range c.MotionMode {
		dst.MotionMode[i] = cloneCDF(c.MotionMode[i])
	}

	// OBMC.
	for i := range c.OBMC {
		dst.OBMC[i] = cloneCDF(c.OBMC[i])
	}

	// CompReferenceType.
	for i := range c.CompReferenceType {
		dst.CompReferenceType[i] = cloneCDF(c.CompReferenceType[i])
	}

	// InterIntra.
	for i := range c.InterIntra {
		dst.InterIntra[i] = cloneCDF(c.InterIntra[i])
	}

	// InterIntraMode.
	for i := range c.InterIntraMode {
		dst.InterIntraMode[i] = cloneCDF(c.InterIntraMode[i])
	}

	// WedgeInterIntra.
	for i := range c.WedgeInterIntra {
		dst.WedgeInterIntra[i] = cloneCDF(c.WedgeInterIntra[i])
	}

	// CompoundType.
	for i := range c.CompoundType {
		dst.CompoundType[i] = cloneCDF(c.CompoundType[i])
	}

	// WedgeIndex.
	for i := range c.WedgeIndex {
		dst.WedgeIndex[i] = cloneCDF(c.WedgeIndex[i])
	}

	// CompGroupIdx.
	for i := range c.CompGroupIdx {
		dst.CompGroupIdx[i] = cloneCDF(c.CompGroupIdx[i])
	}

	// CompoundIdx.
	for i := range c.CompoundIdx {
		dst.CompoundIdx[i] = cloneCDF(c.CompoundIdx[i])
	}

	// MV.
	dst.MVJoint = cloneCDF(c.MVJoint)
	for comp := 0; comp < 2; comp++ {
		dst.MVSign[comp] = cloneCDF(c.MVSign[comp])
		dst.MVClass[comp] = cloneCDF(c.MVClass[comp])
		dst.MVClass0Bit[comp] = cloneCDF(c.MVClass0Bit[comp])
		for c0 := 0; c0 < 2; c0++ {
			dst.MVClass0FR[comp][c0] = cloneCDF(c.MVClass0FR[comp][c0])
		}
		dst.MVClass0HP[comp] = cloneCDF(c.MVClass0HP[comp])
		for bit := 0; bit < 10; bit++ {
			dst.MVBit[comp][bit] = cloneCDF(c.MVBit[comp][bit])
		}
		dst.MVFR[comp] = cloneCDF(c.MVFR[comp])
		dst.MVHP[comp] = cloneCDF(c.MVHP[comp])
	}

	// SwitchableFilter.
	for dim := 0; dim < 2; dim++ {
		for ctx := 0; ctx < 8; ctx++ {
			dst.SwitchableFilter[dim][ctx] = cloneCDF(c.SwitchableFilter[dim][ctx])
		}
	}

	return dst
}

// ResetAllCounts resets all CDF adaptation counters to 0.
// This matches dav1d's dav1d_cdf_thread_update() which resets counts
// before saving CDFs to reference frame slots. Without this, subsequent
// frames inherit saturated counts and adapt too slowly, causing decode errors.
//
// Boolean CDFs (used via ReadSymbolBool): counter at index 1.
// Multi-symbol CDFs (used via ReadSymbol): counter at last index.
func (c *CDFContext) ResetAllCounts() {
	// Helper for boolean CDFs (counter at cdf[1]).
	resetBool := func(cdf []uint16) {
		if len(cdf) >= 2 {
			cdf[1] = 0
		}
	}
	// Helper for multi-symbol CDFs (counter at cdf[nsyms] = cdf[len-1]).
	resetMulti := func(cdf []uint16) {
		if l := len(cdf); l > 0 {
			cdf[l-1] = 0
		}
	}

	// --- Multi-symbol CDFs ---

	// Partition CDFs: nsyms varies by block level, so the counter position
	// is NOT at cdf[len-1] for all entries. Reset the correct position.
	// BL_128X128 (ctx 0-3): nsyms=8, counter at cdf[8]
	// BL_64X64..BL_16X16 (ctx 4-15): nsyms=10, counter at cdf[10]
	// BL_8X8 (ctx 16-19): nsyms=4, counter at cdf[4]
	// ctx 20-23: unused but reset for safety
	for i := 0; i < 4; i++ {
		if len(c.Partition[i]) > 8 {
			c.Partition[i][8] = 0
		}
	}
	for i := 4; i < 16; i++ {
		resetMulti(c.Partition[i])
	}
	for i := 16; i < 20; i++ {
		if len(c.Partition[i]) > 4 {
			c.Partition[i][4] = 0
		}
	}
	for i := 20; i < 24; i++ {
		resetMulti(c.Partition[i])
	}
	for i := range c.IntraFrameYMode {
		for j := range c.IntraFrameYMode[i] {
			resetMulti(c.IntraFrameYMode[i][j])
		}
	}
	for i := range c.YMode {
		resetMulti(c.YMode[i])
	}
	for i := range c.UVMode {
		for j := range c.UVMode[i] {
			resetMulti(c.UVMode[i][j])
		}
	}
	for i := range c.TxSize {
		for j := range c.TxSize[i] {
			resetMulti(c.TxSize[i][j])
		}
	}
	for i := range c.EobPt16 {
		for j := range c.EobPt16[i] {
			resetMulti(c.EobPt16[i][j])
		}
	}
	for i := range c.EobPt32 {
		for j := range c.EobPt32[i] {
			resetMulti(c.EobPt32[i][j])
		}
	}
	for i := range c.EobPt64 {
		for j := range c.EobPt64[i] {
			resetMulti(c.EobPt64[i][j])
		}
	}
	for i := range c.EobPt128 {
		for j := range c.EobPt128[i] {
			resetMulti(c.EobPt128[i][j])
		}
	}
	for i := range c.EobPt256 {
		for j := range c.EobPt256[i] {
			resetMulti(c.EobPt256[i][j])
		}
	}
	for i := range c.EobPt512 {
		for j := range c.EobPt512[i] {
			resetMulti(c.EobPt512[i][j])
		}
	}
	for i := range c.EobPt1024 {
		for j := range c.EobPt1024[i] {
			resetMulti(c.EobPt1024[i][j])
		}
	}
	for i := range c.CoeffBase {
		for j := range c.CoeffBase[i] {
			for k := range c.CoeffBase[i][j] {
				resetMulti(c.CoeffBase[i][j][k])
			}
		}
	}
	for i := range c.CoeffBaseEob {
		for j := range c.CoeffBaseEob[i] {
			for k := range c.CoeffBaseEob[i][j] {
				resetMulti(c.CoeffBaseEob[i][j][k])
			}
		}
	}
	for i := range c.CoeffBR {
		for j := range c.CoeffBR[i] {
			for k := range c.CoeffBR[i][j] {
				resetMulti(c.CoeffBR[i][j][k])
			}
		}
	}
	for i := range c.SegmentID {
		resetMulti(c.SegmentID[i])
	}
	for i := range c.CompoundMode {
		resetMulti(c.CompoundMode[i])
	}
	resetMulti(c.DeltaQ)
	resetMulti(c.DeltaLF)
	for i := range c.DeltaLFMulti {
		resetMulti(c.DeltaLFMulti[i])
	}
	for i := range c.AngleDelta {
		resetMulti(c.AngleDelta[i])
	}
	resetMulti(c.FilterIntraMode)
	for i := range c.PaletteSz {
		for j := range c.PaletteSz[i] {
			resetMulti(c.PaletteSz[i][j])
		}
	}
	for i := range c.ColorMap {
		for j := range c.ColorMap[i] {
			for k := range c.ColorMap[i][j] {
				resetMulti(c.ColorMap[i][j][k])
			}
		}
	}
	resetMulti(c.CflSign)
	for i := range c.CflAlpha {
		resetMulti(c.CflAlpha[i])
	}
	resetMulti(c.CdefIdx)
	resetMulti(c.RestorationType)
	for i := range c.InterTxType {
		for j := range c.InterTxType[i] {
			resetMulti(c.InterTxType[i][j])
		}
	}
	for i := range c.IntraTxType {
		for j := range c.IntraTxType[i] {
			for k := range c.IntraTxType[i][j] {
				resetMulti(c.IntraTxType[i][j][k])
			}
		}
	}
	for i := range c.TxTypeIntra1 {
		for j := range c.TxTypeIntra1[i] {
			resetMulti(c.TxTypeIntra1[i][j])
		}
	}
	for i := range c.TxTypeIntra2 {
		for j := range c.TxTypeIntra2[i] {
			resetMulti(c.TxTypeIntra2[i][j])
		}
	}
	for i := range c.MotionMode {
		resetMulti(c.MotionMode[i])
	}
	for i := range c.OBMC {
		resetBool(c.OBMC[i])
	}
	for i := range c.InterIntraMode {
		resetMulti(c.InterIntraMode[i])
	}
	for i := range c.WedgeIndex {
		resetMulti(c.WedgeIndex[i])
	}
	resetMulti(c.MVJoint)
	for comp := range c.MVClass {
		resetMulti(c.MVClass[comp])
	}
	for comp := range c.MVClass0FR {
		for j := range c.MVClass0FR[comp] {
			resetMulti(c.MVClass0FR[comp][j])
		}
	}
	for comp := range c.MVFR {
		resetMulti(c.MVFR[comp])
	}
	for dim := range c.SwitchableFilter {
		for ctx := range c.SwitchableFilter[dim] {
			resetMulti(c.SwitchableFilter[dim][ctx])
		}
	}

	// --- Boolean CDFs (counter at index 1) ---

	for i := range c.Skip {
		resetBool(c.Skip[i])
	}
	resetBool(c.IntraBC)
	for i := range c.SplitOrHorz {
		resetBool(c.SplitOrHorz[i])
	}
	for i := range c.SplitOrVert {
		resetBool(c.SplitOrVert[i])
	}
	for i := range c.SkipMode {
		resetBool(c.SkipMode[i])
	}
	for i := range c.SegIDPredicted {
		resetBool(c.SegIDPredicted[i])
	}
	for i := range c.IsInter {
		resetBool(c.IsInter[i])
	}
	for i := range c.CompRef {
		for j := range c.CompRef[i] {
			resetBool(c.CompRef[i][j])
		}
	}
	for i := range c.CompBwdRef {
		for j := range c.CompBwdRef[i] {
			resetBool(c.CompBwdRef[i][j])
		}
	}
	for i := range c.CompUniRef {
		for j := range c.CompUniRef[i] {
			resetBool(c.CompUniRef[i][j])
		}
	}
	for i := range c.SingleRef {
		for j := range c.SingleRef[i] {
			resetBool(c.SingleRef[i][j])
		}
	}
	for i := range c.NewMV {
		resetBool(c.NewMV[i])
	}
	for i := range c.ZeroMV {
		resetBool(c.ZeroMV[i])
	}
	for i := range c.RefMV {
		resetBool(c.RefMV[i])
	}
	for i := range c.DrlMode {
		resetBool(c.DrlMode[i])
	}
	for i := range c.UseFilterIntra {
		resetBool(c.UseFilterIntra[i])
	}
	for i := range c.PaletteYMode {
		for j := range c.PaletteYMode[i] {
			resetBool(c.PaletteYMode[i][j])
		}
	}
	for i := range c.PaletteUVMode {
		resetBool(c.PaletteUVMode[i])
	}
	for i := range c.ReferenceMode {
		resetBool(c.ReferenceMode[i])
	}
	for i := range c.CompReferenceType {
		resetBool(c.CompReferenceType[i])
	}
	for i := range c.InterIntra {
		resetBool(c.InterIntra[i])
	}
	for i := range c.WedgeInterIntra {
		resetBool(c.WedgeInterIntra[i])
	}
	for i := range c.CompoundType {
		resetBool(c.CompoundType[i])
	}
	for i := range c.CompGroupIdx {
		resetBool(c.CompGroupIdx[i])
	}
	for i := range c.CompoundIdx {
		resetBool(c.CompoundIdx[i])
	}
	for comp := range c.MVSign {
		resetBool(c.MVSign[comp])
	}
	for comp := range c.MVClass0Bit {
		resetBool(c.MVClass0Bit[comp])
	}
	for comp := range c.MVClass0HP {
		resetBool(c.MVClass0HP[comp])
	}
	for comp := range c.MVBit {
		for j := range c.MVBit[comp] {
			resetBool(c.MVBit[comp][j])
		}
	}
	for comp := range c.MVHP {
		resetBool(c.MVHP[comp])
	}
	for i := range c.AllZero {
		for j := range c.AllZero[i] {
			resetBool(c.AllZero[i][j])
		}
	}
	for i := range c.EobExtra {
		for j := range c.EobExtra[i] {
			for k := range c.EobExtra[i][j] {
				resetBool(c.EobExtra[i][j][k])
			}
		}
	}
	for i := range c.DcSign {
		for j := range c.DcSign[i] {
			resetBool(c.DcSign[i][j])
		}
	}
	resetBool(c.UseWiener)
	resetBool(c.UseSGRProj)

	// TxSplit uses ReadSymbol (not ReadSymbolBool), so count is at last index.
	for i := range c.TxSplit {
		for j := range c.TxSplit[i] {
			resetMulti(c.TxSplit[i][j])
		}
	}
}

// ResetInterMVCDFs resets inter-only and MV CDFs to defaults.
//
// This matches dav1d's dav1d_cdf_thread_update behavior for KEY_FRAME/INTRA_ONLY:
// dav1d only copies coefficient and intra-mode CDFs from the tile decoder to the
// saved context. Inter-mode CDFs (y_mode through obmc) and MV CDFs (joint, sign,
// class, etc.) are NOT copied for keyframes — they remain at their default values.
//
// Without this, IntraBC keyframes would pollute the MV CDFs with IntraBC-specific
// adaptation, causing MV CDF divergence in subsequent inter frames that inherit
// the saved context.
func (c *CDFContext) ResetInterMVCDFs() {
	// clone converts from ascending CDF to ICDF format, matching the
	// clone() function in NewDefaultCDFContextForQP.
	clone := func(src []uint16) []uint16 {
		dst := make([]uint16, len(src))
		n := len(src) - 2 // number of CDF entries to convert
		for i := 0; i < n; i++ {
			dst[i] = 32768 - src[i]
		}
		// Last two entries (sentinel and counter) stay 0.
		return dst
	}

	// Reset MV CDFs to defaults.
	c.MVJoint = clone(defaultMVJointCDF)
	for comp := 0; comp < 2; comp++ {
		c.MVSign[comp] = clone(defaultMVSignCDF[comp])
		c.MVClass[comp] = clone(defaultMVClassCDF[comp])
		c.MVClass0Bit[comp] = clone(defaultMVClass0BitCDF[comp])
		for c0 := 0; c0 < 2; c0++ {
			c.MVClass0FR[comp][c0] = clone(defaultMVClass0FRCDF[comp][c0])
		}
		c.MVClass0HP[comp] = clone(defaultMVClass0HPCDF[comp])
		for bit := 0; bit < 10; bit++ {
			c.MVBit[comp][bit] = clone(defaultMVBitCDF[comp][bit])
		}
		c.MVFR[comp] = clone(defaultMVFRCDF[comp])
		c.MVHP[comp] = clone(defaultMVHPCDF[comp])
	}

	// Reset inter-only mode CDFs to defaults.
	// These are the CDFs in dav1d's CdfModeContext from y_mode onwards
	// (after m.intrabc) that are only copied for inter/switch frames.
	for i := range defaultYModeCDF {
		c.YMode[i] = clone(defaultYModeCDF[i])
	}
	for i := range defaultCompoundModeCDF {
		c.CompoundMode[i] = clone(defaultCompoundModeCDF[i])
	}
	for i := range defaultWedgeIndexCDF {
		c.WedgeIndex[i] = clone(defaultWedgeIndexCDF[i])
	}
	for dim := 0; dim < 2; dim++ {
		for ctx := 0; ctx < 8; ctx++ {
			c.SwitchableFilter[dim][ctx] = clone(defaultSwitchableFilterCDF[dim][ctx])
		}
	}
	for i := range defaultInterIntraModeCDF {
		c.InterIntraMode[i] = clone(defaultInterIntraModeCDF[i])
	}
	for i := range defaultMotionModeCDF {
		c.MotionMode[i] = clone(defaultMotionModeCDF[i])
	}
	for i := range defaultSkipModeCDF {
		c.SkipMode[i] = clone(defaultSkipModeCDF[i])
	}
	for i := range defaultNewMVCDF {
		c.NewMV[i] = clone(defaultNewMVCDF[i])
	}
	for i := range defaultZeroMVCDF {
		c.ZeroMV[i] = clone(defaultZeroMVCDF[i])
	}
	for i := range defaultRefMVCDF {
		c.RefMV[i] = clone(defaultRefMVCDF[i])
	}
	for i := range defaultDrlModeCDF {
		c.DrlMode[i] = clone(defaultDrlModeCDF[i])
	}
	for i := range defaultIsInterCDF {
		c.IsInter[i] = clone(defaultIsInterCDF[i])
	}
	for i := range defaultReferenceModeCDF {
		c.ReferenceMode[i] = clone(defaultReferenceModeCDF[i])
	}
	for i := range defaultCompRefCDF {
		for j := range defaultCompRefCDF[i] {
			c.CompRef[i][j] = clone(defaultCompRefCDF[i][j])
		}
	}
	for i := range defaultCompBwdRefCDF {
		for j := range defaultCompBwdRefCDF[i] {
			c.CompBwdRef[i][j] = clone(defaultCompBwdRefCDF[i][j])
		}
	}
	for i := range defaultSingleRefCDF {
		for j := range defaultSingleRefCDF[i] {
			c.SingleRef[i][j] = clone(defaultSingleRefCDF[i][j])
		}
	}
	for i := range defaultCompGroupIdxCDF {
		c.CompGroupIdx[i] = clone(defaultCompGroupIdxCDF[i])
	}
	for i := range defaultCompoundIdxCDF {
		c.CompoundIdx[i] = clone(defaultCompoundIdxCDF[i])
	}
	for i := range defaultInterIntraCDF {
		c.InterIntra[i] = clone(defaultInterIntraCDF[i])
	}
	for i := range defaultWedgeInterIntraCDF {
		c.WedgeInterIntra[i] = clone(defaultWedgeInterIntraCDF[i])
	}
	for i := range defaultOBMCCDF {
		c.OBMC[i] = clone(defaultOBMCCDF[i])
	}
	for i := range defaultCompoundTypeCDF {
		c.CompoundType[i] = clone(defaultCompoundTypeCDF[i])
	}
	for i := range defaultCompReferenceTypeCDF {
		c.CompReferenceType[i] = clone(defaultCompReferenceTypeCDF[i])
	}
	for i := range defaultCompUniRefCDF {
		for j := range defaultCompUniRefCDF[i] {
			c.CompUniRef[i][j] = clone(defaultCompUniRefCDF[i][j])
		}
	}
	// IntraBC CDF is at the boundary but it IS copied for keyframes in dav1d
	// (it's before the return in dav1d_cdf_thread_update). So don't reset it.
}

// CDFChecksum computes a simple hash of all CDF values for debugging.
func (c *CDFContext) CDFChecksum() uint64 {
	var h uint64
	addSlice := func(cdf []uint16) {
		for _, v := range cdf {
			h = h*31 + uint64(v)
		}
	}
	for i := range c.Partition {
		addSlice(c.Partition[i])
	}
	for i := range c.SplitOrHorz {
		addSlice(c.SplitOrHorz[i])
	}
	for i := range c.SplitOrVert {
		addSlice(c.SplitOrVert[i])
	}
	for i := range c.Skip {
		addSlice(c.Skip[i])
	}
	addSlice(c.IntraBC)
	for i := range c.IntraFrameYMode {
		for j := range c.IntraFrameYMode[i] {
			addSlice(c.IntraFrameYMode[i][j])
		}
	}
	for i := range c.YMode {
		addSlice(c.YMode[i])
	}
	for i := range c.UVMode {
		for j := range c.UVMode[i] {
			addSlice(c.UVMode[i][j])
		}
	}
	for i := range c.TxSize {
		for j := range c.TxSize[i] {
			addSlice(c.TxSize[i][j])
		}
	}
	for i := range c.TxSplit {
		for j := range c.TxSplit[i] {
			addSlice(c.TxSplit[i][j])
		}
	}
	for i := range c.AllZero {
		for j := range c.AllZero[i] {
			addSlice(c.AllZero[i][j])
		}
	}
	for i := range c.EobPt16 {
		for j := range c.EobPt16[i] {
			addSlice(c.EobPt16[i][j])
		}
	}
	for i := range c.EobPt32 {
		for j := range c.EobPt32[i] {
			addSlice(c.EobPt32[i][j])
		}
	}
	for i := range c.EobPt64 {
		for j := range c.EobPt64[i] {
			addSlice(c.EobPt64[i][j])
		}
	}
	for i := range c.EobPt128 {
		for j := range c.EobPt128[i] {
			addSlice(c.EobPt128[i][j])
		}
	}
	for i := range c.EobPt256 {
		for j := range c.EobPt256[i] {
			addSlice(c.EobPt256[i][j])
		}
	}
	for i := range c.EobPt512 {
		for j := range c.EobPt512[i] {
			addSlice(c.EobPt512[i][j])
		}
	}
	for i := range c.EobPt1024 {
		for j := range c.EobPt1024[i] {
			addSlice(c.EobPt1024[i][j])
		}
	}
	for i := range c.EobExtra {
		for j := range c.EobExtra[i] {
			for k := range c.EobExtra[i][j] {
				addSlice(c.EobExtra[i][j][k])
			}
		}
	}
	for i := range c.CoeffBase {
		for j := range c.CoeffBase[i] {
			for k := range c.CoeffBase[i][j] {
				addSlice(c.CoeffBase[i][j][k])
			}
		}
	}
	for i := range c.CoeffBaseEob {
		for j := range c.CoeffBaseEob[i] {
			for k := range c.CoeffBaseEob[i][j] {
				addSlice(c.CoeffBaseEob[i][j][k])
			}
		}
	}
	for i := range c.CoeffBR {
		for j := range c.CoeffBR[i] {
			for k := range c.CoeffBR[i][j] {
				addSlice(c.CoeffBR[i][j][k])
			}
		}
	}
	for i := range c.DcSign {
		for j := range c.DcSign[i] {
			addSlice(c.DcSign[i][j])
		}
	}
	for i := range c.SegmentID {
		addSlice(c.SegmentID[i])
	}
	for i := range c.SegIDPredicted {
		addSlice(c.SegIDPredicted[i])
	}
	for i := range c.IsInter {
		addSlice(c.IsInter[i])
	}
	for i := range c.CompRef {
		for j := range c.CompRef[i] {
			addSlice(c.CompRef[i][j])
		}
	}
	for i := range c.CompBwdRef {
		for j := range c.CompBwdRef[i] {
			addSlice(c.CompBwdRef[i][j])
		}
	}
	for i := range c.CompUniRef {
		for j := range c.CompUniRef[i] {
			addSlice(c.CompUniRef[i][j])
		}
	}
	for i := range c.SingleRef {
		for j := range c.SingleRef[i] {
			addSlice(c.SingleRef[i][j])
		}
	}
	for i := range c.CompoundMode {
		addSlice(c.CompoundMode[i])
	}
	for i := range c.NewMV {
		addSlice(c.NewMV[i])
	}
	for i := range c.ZeroMV {
		addSlice(c.ZeroMV[i])
	}
	for i := range c.RefMV {
		addSlice(c.RefMV[i])
	}
	for i := range c.DrlMode {
		addSlice(c.DrlMode[i])
	}
	addSlice(c.DeltaQ)
	addSlice(c.DeltaLF)
	for i := range c.DeltaLFMulti {
		addSlice(c.DeltaLFMulti[i])
	}
	for i := range c.AngleDelta {
		addSlice(c.AngleDelta[i])
	}
	addSlice(c.FilterIntraMode)
	for i := range c.UseFilterIntra {
		addSlice(c.UseFilterIntra[i])
	}
	for i := range c.PaletteYMode {
		for j := range c.PaletteYMode[i] {
			addSlice(c.PaletteYMode[i][j])
		}
	}
	for i := range c.PaletteUVMode {
		addSlice(c.PaletteUVMode[i])
	}
	for i := range c.PaletteSz {
		for j := range c.PaletteSz[i] {
			addSlice(c.PaletteSz[i][j])
		}
	}
	for i := range c.ColorMap {
		for j := range c.ColorMap[i] {
			for k := range c.ColorMap[i][j] {
				addSlice(c.ColorMap[i][j][k])
			}
		}
	}
	addSlice(c.CflSign)
	for i := range c.CflAlpha {
		addSlice(c.CflAlpha[i])
	}
	addSlice(c.CdefIdx)
	addSlice(c.RestorationType)
	addSlice(c.UseWiener)
	addSlice(c.UseSGRProj)
	for i := range c.InterTxType {
		for j := range c.InterTxType[i] {
			addSlice(c.InterTxType[i][j])
		}
	}
	for i := range c.IntraTxType {
		for j := range c.IntraTxType[i] {
			for k := range c.IntraTxType[i][j] {
				addSlice(c.IntraTxType[i][j][k])
			}
		}
	}
	for i := range c.TxTypeIntra1 {
		for j := range c.TxTypeIntra1[i] {
			addSlice(c.TxTypeIntra1[i][j])
		}
	}
	for i := range c.TxTypeIntra2 {
		for j := range c.TxTypeIntra2[i] {
			addSlice(c.TxTypeIntra2[i][j])
		}
	}
	for i := range c.ReferenceMode {
		addSlice(c.ReferenceMode[i])
	}
	for i := range c.SkipMode {
		addSlice(c.SkipMode[i])
	}
	for i := range c.MotionMode {
		addSlice(c.MotionMode[i])
	}
	for i := range c.OBMC {
		addSlice(c.OBMC[i])
	}
	for i := range c.CompReferenceType {
		addSlice(c.CompReferenceType[i])
	}
	for i := range c.InterIntra {
		addSlice(c.InterIntra[i])
	}
	for i := range c.InterIntraMode {
		addSlice(c.InterIntraMode[i])
	}
	for i := range c.WedgeInterIntra {
		addSlice(c.WedgeInterIntra[i])
	}
	for i := range c.CompoundType {
		addSlice(c.CompoundType[i])
	}
	for i := range c.WedgeIndex {
		addSlice(c.WedgeIndex[i])
	}
	for i := range c.CompGroupIdx {
		addSlice(c.CompGroupIdx[i])
	}
	for i := range c.CompoundIdx {
		addSlice(c.CompoundIdx[i])
	}
	addSlice(c.MVJoint)
	for comp := range c.MVSign {
		addSlice(c.MVSign[comp])
	}
	for comp := range c.MVClass {
		addSlice(c.MVClass[comp])
	}
	for comp := range c.MVClass0Bit {
		addSlice(c.MVClass0Bit[comp])
	}
	for comp := range c.MVClass0FR {
		for j := range c.MVClass0FR[comp] {
			addSlice(c.MVClass0FR[comp][j])
		}
	}
	for comp := range c.MVClass0HP {
		addSlice(c.MVClass0HP[comp])
	}
	for comp := range c.MVBit {
		for j := range c.MVBit[comp] {
			addSlice(c.MVBit[comp][j])
		}
	}
	for comp := range c.MVFR {
		addSlice(c.MVFR[comp])
	}
	for comp := range c.MVHP {
		addSlice(c.MVHP[comp])
	}
	for dim := range c.SwitchableFilter {
		for ctx := range c.SwitchableFilter[dim] {
			addSlice(c.SwitchableFilter[dim][ctx])
		}
	}
	return h
}

// ComponentChecksums returns a string of per-component CDF checksums for debugging drift.
func (c *CDFContext) ComponentChecksums() string {
	h := func(cdfs ...[]uint16) uint64 {
		var v uint64
		for _, cdf := range cdfs {
			for _, x := range cdf {
				v = v*31 + uint64(x)
			}
		}
		return v
	}
	h2d := func(arr [][]uint16) uint64 {
		var v uint64
		for _, cdf := range arr {
			v = v*31 + h(cdf)
		}
		return v
	}
	_ = h2d

	var partH uint64
	for i := range c.Partition {
		partH = partH*31 + h(c.Partition[i])
	}
	var yModeH uint64
	for i := range c.YMode {
		yModeH = yModeH*31 + h(c.YMode[i])
	}
	var txSzH uint64
	for i := range c.TxSize {
		for j := range c.TxSize[i] {
			txSzH = txSzH*31 + h(c.TxSize[i][j])
		}
	}
	var txSplitH uint64
	for i := range c.TxSplit {
		for j := range c.TxSplit[i] {
			txSplitH = txSplitH*31 + h(c.TxSplit[i][j])
		}
	}
	var coeffBaseH uint64
	for i := range c.CoeffBase {
		for j := range c.CoeffBase[i] {
			for k := range c.CoeffBase[i][j] {
				coeffBaseH = coeffBaseH*31 + h(c.CoeffBase[i][j][k])
			}
		}
	}
	var coeffBRH uint64
	for i := range c.CoeffBR {
		for j := range c.CoeffBR[i] {
			for k := range c.CoeffBR[i][j] {
				coeffBRH = coeffBRH*31 + h(c.CoeffBR[i][j][k])
			}
		}
	}
	var skipH uint64
	for i := range c.Skip {
		skipH = skipH*31 + h(c.Skip[i])
	}
	var isInterH uint64
	for i := range c.IsInter {
		isInterH = isInterH*31 + h(c.IsInter[i])
	}
	var singleRefH uint64
	for i := range c.SingleRef {
		for j := range c.SingleRef[i] {
			singleRefH = singleRefH*31 + h(c.SingleRef[i][j])
		}
	}
	var compModeH uint64
	for i := range c.CompoundMode {
		compModeH = compModeH*31 + h(c.CompoundMode[i])
	}
	var newMVH uint64
	for i := range c.NewMV {
		newMVH = newMVH*31 + h(c.NewMV[i])
	}
	var zeroMVH uint64
	for i := range c.ZeroMV {
		zeroMVH = zeroMVH*31 + h(c.ZeroMV[i])
	}
	var refMVH uint64
	for i := range c.RefMV {
		refMVH = refMVH*31 + h(c.RefMV[i])
	}
	var drlH uint64
	for i := range c.DrlMode {
		drlH = drlH*31 + h(c.DrlMode[i])
	}
	var interTxH uint64
	for i := range c.InterTxType {
		for j := range c.InterTxType[i] {
			interTxH = interTxH*31 + h(c.InterTxType[i][j])
		}
	}
	var mvJointH uint64
	mvJointH = h(c.MVJoint)
	var mvClassH uint64
	for i := range c.MVClass {
		mvClassH = mvClassH*31 + h(c.MVClass[i])
	}
	var uvModeH uint64
	for i := range c.UVMode {
		for j := range c.UVMode[i] {
			uvModeH = uvModeH*31 + h(c.UVMode[i][j])
		}
	}
	var allZeroH uint64
	for i := range c.AllZero {
		for j := range c.AllZero[i] {
			allZeroH = allZeroH*31 + h(c.AllZero[i][j])
		}
	}
	var eobH uint64
	for i := range c.EobPt16 {
		for j := range c.EobPt16[i] {
			eobH = eobH*31 + h(c.EobPt16[i][j])
		}
	}
	for i := range c.EobPt32 {
		for j := range c.EobPt32[i] {
			eobH = eobH*31 + h(c.EobPt32[i][j])
		}
	}
	for i := range c.EobPt64 {
		for j := range c.EobPt64[i] {
			eobH = eobH*31 + h(c.EobPt64[i][j])
		}
	}
	for i := range c.EobPt128 {
		for j := range c.EobPt128[i] {
			eobH = eobH*31 + h(c.EobPt128[i][j])
		}
	}
	for i := range c.EobPt256 {
		for j := range c.EobPt256[i] {
			eobH = eobH*31 + h(c.EobPt256[i][j])
		}
	}
	for i := range c.EobPt512 {
		for j := range c.EobPt512[i] {
			eobH = eobH*31 + h(c.EobPt512[i][j])
		}
	}
	for i := range c.EobPt1024 {
		for j := range c.EobPt1024[i] {
			eobH = eobH*31 + h(c.EobPt1024[i][j])
		}
	}
	var coeffBaseEobH uint64
	for i := range c.CoeffBaseEob {
		for j := range c.CoeffBaseEob[i] {
			for k := range c.CoeffBaseEob[i][j] {
				coeffBaseEobH = coeffBaseEobH*31 + h(c.CoeffBaseEob[i][j][k])
			}
		}
	}
	var dcSignH uint64
	for i := range c.DcSign {
		for j := range c.DcSign[i] {
			dcSignH = dcSignH*31 + h(c.DcSign[i][j])
		}
	}
	var refModeH uint64
	for i := range c.ReferenceMode {
		refModeH = refModeH*31 + h(c.ReferenceMode[i])
	}
	var skipModeH uint64
	for i := range c.SkipMode {
		skipModeH = skipModeH*31 + h(c.SkipMode[i])
	}
	var motionModeH uint64
	for i := range c.MotionMode {
		motionModeH = motionModeH*31 + h(c.MotionMode[i])
	}
	var obmcH uint64
	for i := range c.OBMC {
		obmcH = obmcH*31 + h(c.OBMC[i])
	}
	var filterH uint64
	for dim := range c.SwitchableFilter {
		for ctx := range c.SwitchableFilter[dim] {
			filterH = filterH*31 + h(c.SwitchableFilter[dim][ctx])
		}
	}
	var compRefH uint64
	for i := range c.CompRef {
		for j := range c.CompRef[i] {
			compRefH = compRefH*31 + h(c.CompRef[i][j])
		}
	}
	var compBwdRefH uint64
	for i := range c.CompBwdRef {
		for j := range c.CompBwdRef[i] {
			compBwdRefH = compBwdRefH*31 + h(c.CompBwdRef[i][j])
		}
	}
	var eobExtraH uint64
	for i := range c.EobExtra {
		for j := range c.EobExtra[i] {
			for k := range c.EobExtra[i][j] {
				eobExtraH = eobExtraH*31 + h(c.EobExtra[i][j][k])
			}
		}
	}
	var intraFYH uint64
	for i := range c.IntraFrameYMode {
		for j := range c.IntraFrameYMode[i] {
			intraFYH = intraFYH*31 + h(c.IntraFrameYMode[i][j])
		}
	}

	return fmt.Sprintf("part=%x yMode=%x txSz=%x txSplit=%x coeffBase=%x coeffBR=%x skip=%x isInter=%x singleRef=%x compMode=%x newMV=%x zeroMV=%x refMV=%x drl=%x interTx=%x mvJoint=%x mvClass=%x uvMode=%x allZero=%x eob=%x coeffBaseEob=%x dcSign=%x refMode=%x skipMode=%x motionMode=%x obmc=%x filter=%x compRef=%x compBwdRef=%x eobExtra=%x intraFY=%x",
		partH, yModeH, txSzH, txSplitH, coeffBaseH, coeffBRH, skipH, isInterH, singleRefH, compModeH, newMVH, zeroMVH, refMVH, drlH, interTxH, mvJointH, mvClassH, uvModeH, allZeroH, eobH, coeffBaseEobH, dcSignH, refModeH, skipModeH, motionModeH, obmcH, filterH, compRefH, compBwdRefH, eobExtraH, intraFYH)
}

// VerifyAllCountsZero checks every CDF counter position and returns
// a list of CDF names where the counter is non-zero. This is used to
// debug ResetAllCounts correctness.
func (c *CDFContext) VerifyAllCountsZero() []string {
	var bad []string
	checkBool := func(name string, cdf []uint16) {
		if len(cdf) >= 2 && cdf[1] != 0 {
			bad = append(bad, fmt.Sprintf("%s: cdf[1]=%d", name, cdf[1]))
		}
	}
	checkMulti := func(name string, cdf []uint16, nsyms int) {
		if len(cdf) > nsyms && cdf[nsyms] != 0 {
			bad = append(bad, fmt.Sprintf("%s: cdf[%d]=%d (nsyms=%d, len=%d)", name, nsyms, cdf[nsyms], nsyms, len(cdf)))
		}
	}

	// Partition: ctx 0-3 nsyms=8, ctx 4-15 nsyms=10, ctx 16-19 nsyms=4, ctx 20-23 nsyms=4
	for i := 0; i < 4; i++ {
		checkMulti(fmt.Sprintf("Partition[%d]", i), c.Partition[i], 8)
	}
	for i := 4; i < 16; i++ {
		checkMulti(fmt.Sprintf("Partition[%d]", i), c.Partition[i], 10)
	}
	for i := 16; i < 20; i++ {
		checkMulti(fmt.Sprintf("Partition[%d]", i), c.Partition[i], 4)
	}
	for i := 20; i < 24; i++ {
		checkMulti(fmt.Sprintf("Partition[%d]", i), c.Partition[i], 4)
	}
	for i := range c.SplitOrHorz {
		checkBool(fmt.Sprintf("SplitOrHorz[%d]", i), c.SplitOrHorz[i])
	}
	for i := range c.SplitOrVert {
		checkBool(fmt.Sprintf("SplitOrVert[%d]", i), c.SplitOrVert[i])
	}
	for i := range c.Skip {
		checkBool(fmt.Sprintf("Skip[%d]", i), c.Skip[i])
	}
	checkBool("IntraBC", c.IntraBC)
	for i := range c.IntraFrameYMode {
		for j := range c.IntraFrameYMode[i] {
			checkMulti(fmt.Sprintf("IntraFrameYMode[%d][%d]", i, j), c.IntraFrameYMode[i][j], 13)
		}
	}
	for i := range c.YMode {
		checkMulti(fmt.Sprintf("YMode[%d]", i), c.YMode[i], 13)
	}
	for i := range c.UVMode {
		for j := range c.UVMode[i] {
			nsyms := 13
			if i == 1 {
				nsyms = 14
			}
			checkMulti(fmt.Sprintf("UVMode[%d][%d]", i, j), c.UVMode[i][j], nsyms)
		}
	}
	for i := range c.TxSize {
		for j := range c.TxSize[i] {
			// nsyms varies: cat0=2, cat1=3, cat2=4, cat3=5
			nsyms := i + 2
			checkMulti(fmt.Sprintf("TxSize[%d][%d]", i, j), c.TxSize[i][j], nsyms)
		}
	}
	for i := range c.TxSplit {
		for j := range c.TxSplit[i] {
			checkMulti(fmt.Sprintf("TxSplit[%d][%d]", i, j), c.TxSplit[i][j], 2)
		}
	}
	for i := range c.AllZero {
		for j := range c.AllZero[i] {
			checkBool(fmt.Sprintf("AllZero[%d][%d]", i, j), c.AllZero[i][j])
		}
	}
	for i := range c.EobPt16 {
		for j := range c.EobPt16[i] {
			checkMulti(fmt.Sprintf("EobPt16[%d][%d]", i, j), c.EobPt16[i][j], 5)
		}
	}
	for i := range c.EobPt32 {
		for j := range c.EobPt32[i] {
			checkMulti(fmt.Sprintf("EobPt32[%d][%d]", i, j), c.EobPt32[i][j], 6)
		}
	}
	for i := range c.EobPt64 {
		for j := range c.EobPt64[i] {
			checkMulti(fmt.Sprintf("EobPt64[%d][%d]", i, j), c.EobPt64[i][j], 7)
		}
	}
	for i := range c.EobPt128 {
		for j := range c.EobPt128[i] {
			checkMulti(fmt.Sprintf("EobPt128[%d][%d]", i, j), c.EobPt128[i][j], 8)
		}
	}
	for i := range c.EobPt256 {
		for j := range c.EobPt256[i] {
			checkMulti(fmt.Sprintf("EobPt256[%d][%d]", i, j), c.EobPt256[i][j], 9)
		}
	}
	for i := range c.EobPt512 {
		for j := range c.EobPt512[i] {
			checkMulti(fmt.Sprintf("EobPt512[%d][%d]", i, j), c.EobPt512[i][j], 10)
		}
	}
	for i := range c.EobPt1024 {
		for j := range c.EobPt1024[i] {
			checkMulti(fmt.Sprintf("EobPt1024[%d][%d]", i, j), c.EobPt1024[i][j], 11)
		}
	}
	for i := range c.EobExtra {
		for j := range c.EobExtra[i] {
			for k := range c.EobExtra[i][j] {
				checkBool(fmt.Sprintf("EobExtra[%d][%d][%d]", i, j, k), c.EobExtra[i][j][k])
			}
		}
	}
	for i := range c.CoeffBase {
		for j := range c.CoeffBase[i] {
			for k := range c.CoeffBase[i][j] {
				checkMulti(fmt.Sprintf("CoeffBase[%d][%d][%d]", i, j, k), c.CoeffBase[i][j][k], 4)
			}
		}
	}
	for i := range c.CoeffBaseEob {
		for j := range c.CoeffBaseEob[i] {
			for k := range c.CoeffBaseEob[i][j] {
				checkMulti(fmt.Sprintf("CoeffBaseEob[%d][%d][%d]", i, j, k), c.CoeffBaseEob[i][j][k], 3)
			}
		}
	}
	for i := range c.CoeffBR {
		for j := range c.CoeffBR[i] {
			for k := range c.CoeffBR[i][j] {
				checkMulti(fmt.Sprintf("CoeffBR[%d][%d][%d]", i, j, k), c.CoeffBR[i][j][k], 4)
			}
		}
	}
	for i := range c.DcSign {
		for j := range c.DcSign[i] {
			checkBool(fmt.Sprintf("DcSign[%d][%d]", i, j), c.DcSign[i][j])
		}
	}
	for i := range c.SegmentID {
		checkMulti(fmt.Sprintf("SegmentID[%d]", i), c.SegmentID[i], 8)
	}
	for i := range c.SegIDPredicted {
		checkBool(fmt.Sprintf("SegIDPredicted[%d]", i), c.SegIDPredicted[i])
	}
	for i := range c.IsInter {
		checkBool(fmt.Sprintf("IsInter[%d]", i), c.IsInter[i])
	}
	for i := range c.CompRef {
		for j := range c.CompRef[i] {
			checkBool(fmt.Sprintf("CompRef[%d][%d]", i, j), c.CompRef[i][j])
		}
	}
	for i := range c.CompBwdRef {
		for j := range c.CompBwdRef[i] {
			checkBool(fmt.Sprintf("CompBwdRef[%d][%d]", i, j), c.CompBwdRef[i][j])
		}
	}
	for i := range c.CompUniRef {
		for j := range c.CompUniRef[i] {
			checkBool(fmt.Sprintf("CompUniRef[%d][%d]", i, j), c.CompUniRef[i][j])
		}
	}
	for i := range c.SingleRef {
		for j := range c.SingleRef[i] {
			checkBool(fmt.Sprintf("SingleRef[%d][%d]", i, j), c.SingleRef[i][j])
		}
	}
	for i := range c.CompoundMode {
		checkMulti(fmt.Sprintf("CompoundMode[%d]", i), c.CompoundMode[i], 8)
	}
	for i := range c.NewMV {
		checkBool(fmt.Sprintf("NewMV[%d]", i), c.NewMV[i])
	}
	for i := range c.ZeroMV {
		checkBool(fmt.Sprintf("ZeroMV[%d]", i), c.ZeroMV[i])
	}
	for i := range c.RefMV {
		checkBool(fmt.Sprintf("RefMV[%d]", i), c.RefMV[i])
	}
	for i := range c.DrlMode {
		checkBool(fmt.Sprintf("DrlMode[%d]", i), c.DrlMode[i])
	}
	checkMulti("DeltaQ", c.DeltaQ, 4)
	checkMulti("DeltaLF", c.DeltaLF, 4)
	for i := range c.DeltaLFMulti {
		checkMulti(fmt.Sprintf("DeltaLFMulti[%d]", i), c.DeltaLFMulti[i], 4)
	}
	for i := range c.AngleDelta {
		checkMulti(fmt.Sprintf("AngleDelta[%d]", i), c.AngleDelta[i], 7)
	}
	checkMulti("FilterIntraMode", c.FilterIntraMode, 5)
	for i := range c.UseFilterIntra {
		checkBool(fmt.Sprintf("UseFilterIntra[%d]", i), c.UseFilterIntra[i])
	}
	for i := range c.PaletteYMode {
		for j := range c.PaletteYMode[i] {
			checkBool(fmt.Sprintf("PaletteYMode[%d][%d]", i, j), c.PaletteYMode[i][j])
		}
	}
	for i := range c.PaletteUVMode {
		checkBool(fmt.Sprintf("PaletteUVMode[%d]", i), c.PaletteUVMode[i])
	}
	for i := range c.PaletteSz {
		for j := range c.PaletteSz[i] {
			checkMulti(fmt.Sprintf("PaletteSz[%d][%d]", i, j), c.PaletteSz[i][j], 7)
		}
	}
	for i := range c.ColorMap {
		for j := range c.ColorMap[i] {
			for k := range c.ColorMap[i][j] {
				nsyms := j + 2 // palette size 2..8 maps to nsyms 2..8
				checkMulti(fmt.Sprintf("ColorMap[%d][%d][%d]", i, j, k), c.ColorMap[i][j][k], nsyms)
			}
		}
	}
	checkMulti("CflSign", c.CflSign, 8)
	for i := range c.CflAlpha {
		checkMulti(fmt.Sprintf("CflAlpha[%d]", i), c.CflAlpha[i], 16)
	}
	// CdefIdx has variable nsyms, check cdf[len-1]
	if l := len(c.CdefIdx); l > 0 && c.CdefIdx[l-1] != 0 {
		bad = append(bad, fmt.Sprintf("CdefIdx: cdf[%d]=%d", l-1, c.CdefIdx[l-1]))
	}
	checkMulti("RestorationType", c.RestorationType, 3)
	checkBool("UseWiener", c.UseWiener)
	checkBool("UseSGRProj", c.UseSGRProj)
	for i := range c.InterTxType {
		for j := range c.InterTxType[i] {
			// nsyms varies: set 1→7, set 2→5, set 3→12, set 4→2
			// But the arrays may not all be initialized. Check last element.
			if l := len(c.InterTxType[i][j]); l > 0 && c.InterTxType[i][j][l-1] != 0 {
				bad = append(bad, fmt.Sprintf("InterTxType[%d][%d]: cdf[%d]=%d", i, j, l-1, c.InterTxType[i][j][l-1]))
			}
		}
	}
	for i := range c.IntraTxType {
		for j := range c.IntraTxType[i] {
			for k := range c.IntraTxType[i][j] {
				if l := len(c.IntraTxType[i][j][k]); l > 0 && c.IntraTxType[i][j][k][l-1] != 0 {
					bad = append(bad, fmt.Sprintf("IntraTxType[%d][%d][%d]: cdf[%d]=%d", i, j, k, l-1, c.IntraTxType[i][j][k][l-1]))
				}
			}
		}
	}
	for i := range c.TxTypeIntra1 {
		for j := range c.TxTypeIntra1[i] {
			checkMulti(fmt.Sprintf("TxTypeIntra1[%d][%d]", i, j), c.TxTypeIntra1[i][j], 7)
		}
	}
	for i := range c.TxTypeIntra2 {
		for j := range c.TxTypeIntra2[i] {
			checkMulti(fmt.Sprintf("TxTypeIntra2[%d][%d]", i, j), c.TxTypeIntra2[i][j], 5)
		}
	}
	for i := range c.ReferenceMode {
		checkBool(fmt.Sprintf("ReferenceMode[%d]", i), c.ReferenceMode[i])
	}
	for i := range c.SkipMode {
		checkBool(fmt.Sprintf("SkipMode[%d]", i), c.SkipMode[i])
	}
	for i := range c.MotionMode {
		checkMulti(fmt.Sprintf("MotionMode[%d]", i), c.MotionMode[i], 3)
	}
	for i := range c.OBMC {
		checkBool(fmt.Sprintf("OBMC[%d]", i), c.OBMC[i])
	}
	for i := range c.CompReferenceType {
		checkBool(fmt.Sprintf("CompReferenceType[%d]", i), c.CompReferenceType[i])
	}
	for i := range c.InterIntra {
		checkBool(fmt.Sprintf("InterIntra[%d]", i), c.InterIntra[i])
	}
	for i := range c.InterIntraMode {
		checkMulti(fmt.Sprintf("InterIntraMode[%d]", i), c.InterIntraMode[i], 4)
	}
	for i := range c.WedgeInterIntra {
		checkBool(fmt.Sprintf("WedgeInterIntra[%d]", i), c.WedgeInterIntra[i])
	}
	for i := range c.CompoundType {
		checkBool(fmt.Sprintf("CompoundType[%d]", i), c.CompoundType[i])
	}
	for i := range c.WedgeIndex {
		checkMulti(fmt.Sprintf("WedgeIndex[%d]", i), c.WedgeIndex[i], 16)
	}
	for i := range c.CompGroupIdx {
		checkBool(fmt.Sprintf("CompGroupIdx[%d]", i), c.CompGroupIdx[i])
	}
	for i := range c.CompoundIdx {
		checkBool(fmt.Sprintf("CompoundIdx[%d]", i), c.CompoundIdx[i])
	}
	checkMulti("MVJoint", c.MVJoint, 4)
	for comp := range c.MVSign {
		checkBool(fmt.Sprintf("MVSign[%d]", comp), c.MVSign[comp])
	}
	for comp := range c.MVClass {
		checkMulti(fmt.Sprintf("MVClass[%d]", comp), c.MVClass[comp], 11)
	}
	for comp := range c.MVClass0Bit {
		checkBool(fmt.Sprintf("MVClass0Bit[%d]", comp), c.MVClass0Bit[comp])
	}
	for comp := range c.MVClass0FR {
		for j := range c.MVClass0FR[comp] {
			checkMulti(fmt.Sprintf("MVClass0FR[%d][%d]", comp, j), c.MVClass0FR[comp][j], 4)
		}
	}
	for comp := range c.MVClass0HP {
		checkBool(fmt.Sprintf("MVClass0HP[%d]", comp), c.MVClass0HP[comp])
	}
	for comp := range c.MVBit {
		for j := range c.MVBit[comp] {
			checkBool(fmt.Sprintf("MVBit[%d][%d]", comp, j), c.MVBit[comp][j])
		}
	}
	for comp := range c.MVFR {
		checkMulti(fmt.Sprintf("MVFR[%d]", comp), c.MVFR[comp], 4)
	}
	for comp := range c.MVHP {
		checkBool(fmt.Sprintf("MVHP[%d]", comp), c.MVHP[comp])
	}
	for dim := range c.SwitchableFilter {
		for ctx := range c.SwitchableFilter[dim] {
			checkMulti(fmt.Sprintf("SwitchableFilter[%d][%d]", dim, ctx), c.SwitchableFilter[dim][ctx], 3)
		}
	}
	return bad
}

// ResetCDEF reconfigures the CDEF index CDF for the given number of
// CDEF bits. This must be called after parsing cdef_bits from the frame
// header, since the number of symbols is (1 << cdef_bits).
func (c *CDFContext) ResetCDEF(cdefBits int) {
	nsyms := 1 << cdefBits
	if nsyms < 2 {
		nsyms = 2
	}
	c.CdefIdx = InitCDF(nsyms)
}
