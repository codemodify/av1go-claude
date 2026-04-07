package obu

import (
	"av1go/bitstream"
	"fmt"
	"math/bits"
)

// FrameHeaderParams contains all parameters needed to write an
// uncompressed_header(). AV1 spec Section 5.9.2.
//
// For keyframe encoding, many fields are ignored or derived automatically.
type FrameHeaderParams struct {
	FrameType          FrameType
	ShowFrame          bool
	ShowableFrame      bool // only written when ShowFrame is false
	ErrorResilientMode bool // forced for KEY_FRAME+ShowFrame and SWITCH
	DisableCDFUpdate   bool

	AllowScreenContentTools bool
	ForceIntegerMV          bool

	// Frame size. If FrameSizeOverride is false, max dimensions from
	// the sequence header are used.
	FrameSizeOverride bool
	FrameWidth        uint32 // only written if FrameSizeOverride
	FrameHeight       uint32 // only written if FrameSizeOverride

	UseSuperRes         bool
	SuperResDenomMinus9 uint8 // SUPERRES_DENOM_BITS (3 bits), only if UseSuperRes

	RenderSizeDifferent bool
	RenderWidth         uint32 // only if RenderSizeDifferent
	RenderHeight        uint32 // only if RenderSizeDifferent

	OrderHint uint32

	// For inter frames: refresh_frame_flags.
	// For KEY_FRAME + ShowFrame this is derived as 0xFF.
	RefreshFrameFlags uint8

	// Reference frame indices (inter only).
	RefFrameIdx [RefsPerFrame]uint8

	AllowIntraBC bool

	// Quantization parameters.
	BaseQIndex uint8 // 0-255
	DeltaQYDC  int8
	DeltaQUDC  int8
	DeltaQUAC  int8
	DeltaQVDC  int8
	DeltaQVAC  int8
	UsingQMatrix bool
	QMY          uint8
	QMU          uint8
	QMV          uint8

	// Segmentation (only enabled flag for now).
	SegmentationEnabled bool

	// Delta Q.
	DeltaQPresent bool
	DeltaQRes     uint8

	// Delta LF.
	DeltaLFPresent bool
	DeltaLFRes     uint8
	DeltaLFMulti   bool

	// Loop filter.
	LoopFilterLevel        [4]uint8 // Y_vert, Y_horiz, U, V; each 0-63
	LoopFilterSharpness    uint8    // 0-7
	LoopFilterDeltaEnabled bool
	LoopFilterDeltaUpdate  bool
	RefDeltas              [NumRefFrames]int8
	ModeDeltas             [2]int8

	// CDEF.
	CDEFDampingMinus3 uint8 // 0-3
	CDEFBits          uint8 // 0-3
	CDEFYPriStrength  []uint8
	CDEFYSecStrength  []uint8
	CDEFUVPriStrength []uint8
	CDEFUVSecStrength []uint8

	// Loop restoration. Per-plane type:
	//   0 = RESTORE_NONE
	//   1 = RESTORE_SWITCHABLE
	//   2 = RESTORE_WIENER
	//   3 = RESTORE_SGRPROJ
	LRType      [3]uint8
	LRUnitShift uint8 // 0-2 (added to base)
	LRUVShift   bool

	// TX mode. true = TX_MODE_SELECT, false = TX_MODE_LARGEST.
	TxModeSelect bool

	// Reduced TX set.
	ReducedTxSet bool

	// Skip mode (inter only).
	SkipModePresent bool

	// Warped motion (inter only).
	AllowWarpedMotion bool

	// Tile info.
	TileInfo TileInfoParams
}

// TileInfoParams describes tile partitioning for the frame.
// AV1 spec Section 5.9.15.
type TileInfoParams struct {
	UniformSpacing bool
	TileColsLog2   int // log2 of number of tile columns
	TileRowsLog2   int // log2 of number of tile rows
}

// Loop restoration type constants.
const (
	RestoreNone       = 0
	RestoreSwitchable = 1
	RestoreWiener     = 2
	RestoreSGRProj    = 3
)

// Remap maps coded lr_type to FrameRestorationType.
var lrTypeRemap = [4]uint8{RestoreNone, RestoreSwitchable, RestoreWiener, RestoreSGRProj}

// DefaultKeyFrameParams returns FrameHeaderParams with sensible defaults for
// a keyframe at the given QP. Frame dimensions come from the sequence header.
func DefaultKeyFrameParams(qp uint8) *FrameHeaderParams {
	return &FrameHeaderParams{
		FrameType:               FrameTypeKey,
		ShowFrame:               true,
		ErrorResilientMode:      true, // forced for KEY_FRAME + show_frame
		DisableCDFUpdate:        false,
		AllowScreenContentTools: false,
		ForceIntegerMV:          false,
		FrameSizeOverride:       false,
		UseSuperRes:             false,
		RenderSizeDifferent:     false,
		AllowIntraBC:            false,

		BaseQIndex: qp,

		LoopFilterLevel:        [4]uint8{0, 0, 0, 0},
		LoopFilterSharpness:    0,
		LoopFilterDeltaEnabled: true,
		LoopFilterDeltaUpdate:  true,
		RefDeltas:              [NumRefFrames]int8{1, 0, 0, 0, 0, -1, -1, -1},
		ModeDeltas:             [2]int8{0, 0},

		CDEFDampingMinus3: 0,
		CDEFBits:          0,
		CDEFYPriStrength:  []uint8{0},
		CDEFYSecStrength:  []uint8{0},
		CDEFUVPriStrength: []uint8{0},
		CDEFUVSecStrength: []uint8{0},

		LRType: [3]uint8{RestoreNone, RestoreNone, RestoreNone},

		TxModeSelect: false,
		ReducedTxSet: false,

		TileInfo: TileInfoParams{
			UniformSpacing: true,
			TileColsLog2:   0,
			TileRowsLog2:   0,
		},
	}
}

// DefaultInterFrameParams returns FrameHeaderParams defaults for an inter frame.
func DefaultInterFrameParams(qp uint8, orderHint uint32) *FrameHeaderParams {
	p := DefaultKeyFrameParams(qp)
	p.FrameType = FrameTypeInter
	p.ErrorResilientMode = true // keep error resilient for simplicity
	p.OrderHint = orderHint
	p.RefreshFrameFlags = 1 // refresh slot 0
	return p
}

// WriteFrameHeader serializes an uncompressed_header() into bytes.
// The result is suitable as the payload of a TypeFrameHeader or TypeFrame OBU.
//
// AV1 spec Section 5.9.2.
func WriteFrameHeader(p *FrameHeaderParams, sh *SequenceHeader) ([]byte, error) {
	w := bitstream.NewWriter(256)

	frameIsIntra := p.FrameType == FrameTypeKey || p.FrameType == FrameTypeIntraOnly
	numPlanes := 3
	if sh.ColorConfig.MonoChrome {
		numPlanes = 1
	}

	if !sh.ReducedStillPictureHeader {
		// show_existing_frame f(1) — always 0 for encoding new frames
		w.WriteBool(false)

		// frame_type f(2)
		w.WriteBits(uint64(p.FrameType), 2)

		// show_frame f(1)
		w.WriteBool(p.ShowFrame)

		if p.ShowFrame {
			// showable_frame is derived
		} else {
			// showable_frame f(1)
			w.WriteBool(p.ShowableFrame)
		}

		// error_resilient_mode
		if p.FrameType == FrameTypeSwitch || (p.FrameType == FrameTypeKey && p.ShowFrame) {
			// Forced to 1, not written.
		} else {
			w.WriteBool(p.ErrorResilientMode)
		}
	}

	// disable_cdf_update f(1)
	w.WriteBool(p.DisableCDFUpdate)

	// allow_screen_content_tools
	allowSCT := p.AllowScreenContentTools
	if sh.SeqForceScreenContentTools == SelectScreenContentTools {
		w.WriteBool(allowSCT)
	} else {
		allowSCT = sh.SeqForceScreenContentTools == 1
	}

	// force_integer_mv
	forceIntMV := p.ForceIntegerMV
	if allowSCT {
		if sh.SeqForceIntegerMV == SelectIntegerMV {
			w.WriteBool(forceIntMV)
		} else {
			forceIntMV = sh.SeqForceIntegerMV == 1
		}
	} else {
		forceIntMV = false
	}

	// current_frame_id
	if sh.FrameIDNumbersPresent {
		idLen := int(sh.DeltaFrameIDLengthMinus2) + 2 + int(sh.AdditionalFrameIDLengthMinus1) + 1
		w.WriteBits(0, idLen) // frame ID 0
	}

	// frame_size_override_flag
	if p.FrameType == FrameTypeSwitch {
		// Forced to 1, not written.
	} else if !sh.ReducedStillPictureHeader {
		w.WriteBool(p.FrameSizeOverride)
	}

	// order_hint
	if sh.EnableOrderHint {
		orderHintBits := int(sh.OrderHintBitsMinus1) + 1
		w.WriteBits(uint64(p.OrderHint), orderHintBits)
	}

	// primary_ref_frame — for KEY_FRAME it's PrimaryRefNone (derived).
	// For non-KEY non-INTRA_ONLY non-SWITCH frames, we'd need to write it.
	if !frameIsIntra && p.FrameType != FrameTypeSwitch {
		w.WriteBits(uint64(PrimaryRefNone), 3) // primary_ref_frame f(3)
	}

	// refresh_frame_flags
	if p.FrameType == FrameTypeSwitch || (p.FrameType == FrameTypeKey && p.ShowFrame) {
		// Derived as 0xFF, not written.
	} else {
		w.WriteBits(uint64(p.RefreshFrameFlags), 8)
	}

	// For inter frames: reference frame signaling
	if !frameIsIntra {
		if p.ErrorResilientMode && sh.EnableOrderHint {
			// ref_order_hint[i] for each ref frame
			orderHintBits := int(sh.OrderHintBitsMinus1) + 1
			for i := 0; i < NumRefFrames; i++ {
				w.WriteBits(0, orderHintBits) // ref_order_hint placeholder
			}
		}
	}

	// frame_size()
	writeFrameSize(w, p, sh)

	// render_size()
	w.WriteBool(p.RenderSizeDifferent)
	if p.RenderSizeDifferent {
		w.WriteBits(uint64(p.RenderWidth-1), 16)
		w.WriteBits(uint64(p.RenderHeight-1), 16)
	}

	// allow_intrabc
	if allowSCT && !p.UseSuperRes {
		if frameIsIntra {
			w.WriteBool(p.AllowIntraBC)
		}
	}

	// For inter frames: ref_frame_idx, global motion, etc.
	if !frameIsIntra {
		for i := 0; i < RefsPerFrame; i++ {
			w.WriteBits(uint64(p.RefFrameIdx[i]), 3) // ref_frame_idx f(3)
		}
		// allow_high_precision_mv f(1)
		if !forceIntMV {
			w.WriteBool(false) // allow_high_precision_mv
		}
		// interpolation_filter
		writeInterpFilter(w)
		// is_motion_mode_switchable f(1)
		w.WriteBool(false)
	}

	// Derive lossless state.
	codedLossless := p.BaseQIndex == 0 && p.DeltaQYDC == 0 &&
		p.DeltaQUAC == 0 && p.DeltaQUDC == 0 &&
		p.DeltaQVAC == 0 && p.DeltaQVDC == 0

	// tile_info()
	if err := writeTileInfo(w, p, sh); err != nil {
		return nil, fmt.Errorf("obu: tile_info: %w", err)
	}

	// quantization_params()
	writeQuantizationParams(w, p, sh, numPlanes)

	// segmentation_params()
	w.WriteBool(p.SegmentationEnabled) // segmentation_enabled f(1)
	// If enabled, would write segment data here.

	// delta_q_params()
	writeDeltaQParams(w, p)

	// delta_lf_params()
	writeDeltaLFParams(w, p)

	// loop_filter_params()
	if !codedLossless && !p.AllowIntraBC {
		writeLoopFilterParams(w, p, numPlanes)
	}

	// cdef_params()
	if !codedLossless && !p.AllowIntraBC && sh.EnableCDEF {
		writeCDEFParams(w, p, numPlanes)
	}

	// lr_params()
	if !codedLossless && !p.AllowIntraBC && sh.EnableRestoration {
		writeLRParams(w, p, sh, numPlanes)
	}

	// read_tx_mode()
	if !codedLossless {
		// tx_mode_select f(1)
		w.WriteBool(p.TxModeSelect)
	}
	// else TxMode = ONLY_4X4 (derived)

	// frame_reference_mode() — for intra, reference_select = 0 (derived)
	if !frameIsIntra {
		w.WriteBool(false) // reference_select f(1) — single reference
	}

	// skip_mode_params() — not allowed for intra
	if !frameIsIntra {
		w.WriteBool(p.SkipModePresent) // skip_mode_present f(1)
	}

	// allow_warped_motion — 0 for intra, error_resilient, or !enable_warped_motion
	if !frameIsIntra && !p.ErrorResilientMode && sh.EnableWarpedMotion {
		w.WriteBool(p.AllowWarpedMotion)
	}

	// reduced_tx_set f(1)
	w.WriteBool(p.ReducedTxSet)

	// global_motion_params() — identity for intra (nothing written)
	if !frameIsIntra {
		writeGlobalMotionParams(w, p)
	}

	// film_grain_params() — skip for now (not present if !FilmGrainParamsPresent)

	return w.Bytes(), nil
}

// writeFrameSize writes frame_size() per spec Section 5.9.5.
func writeFrameSize(w *bitstream.Writer, p *FrameHeaderParams, sh *SequenceHeader) {
	if p.FrameSizeOverride {
		widthBits := int(sh.FrameWidthBitsMinus1) + 1
		heightBits := int(sh.FrameHeightBitsMinus1) + 1
		w.WriteBits(uint64(p.FrameWidth-1), widthBits)
		w.WriteBits(uint64(p.FrameHeight-1), heightBits)
	}

	// superres_params()
	if sh.EnableSuperRes {
		w.WriteBool(p.UseSuperRes) // use_superres f(1)
		if p.UseSuperRes {
			w.WriteBits(uint64(p.SuperResDenomMinus9), 3) // coded_denom f(3)
		}
	}
}

// writeInterpFilter writes read_interpolation_filter() per spec Section 5.9.13.
func writeInterpFilter(w *bitstream.Writer) {
	// is_filter_switchable f(1) = 1 means switchable
	w.WriteBool(true) // switchable filter
}

// writeTileInfo writes tile_info() per spec Section 5.9.15.
func writeTileInfo(w *bitstream.Writer, p *FrameHeaderParams, sh *SequenceHeader) error {
	// Compute superblock dimensions.
	miCols := 2 * ((sh.MaxFrameWidthMinus1 + 1 + 7) >> 3)  // MiCols
	miRows := 2 * ((sh.MaxFrameHeightMinus1 + 1 + 7) >> 3) // MiRows

	var sbCols, sbRows uint32
	if sh.Use128x128Superblock {
		sbCols = (miCols + 31) >> 5
		sbRows = (miRows + 31) >> 5
	} else {
		sbCols = (miCols + 15) >> 4
		sbRows = (miRows + 15) >> 4
	}

	// Compute min/max log2 tile cols.
	maxTileWidthSB := uint32(4096 >> 2) // MAX_TILE_WIDTH_SB in MI units / SB
	if sh.Use128x128Superblock {
		maxTileWidthSB = (4096 + 127) >> 7
	} else {
		maxTileWidthSB = (4096 + 63) >> 6
	}

	minLog2TileCols := tileLog2(maxTileWidthSB, sbCols)
	maxLog2TileCols := tileLog2(1, sbCols)
	if maxLog2TileCols > 6 {
		maxLog2TileCols = 6
	}

	minLog2TileRows := 0
	maxLog2TileRows := tileLog2(1, sbRows)
	if maxLog2TileRows > 6 {
		maxLog2TileRows = 6
	}

	// Compute min tiles for area constraint.
	maxTileAreaSB := uint32(4096 * 2304 / (128 * 128)) // MAX_TILE_AREA_SB
	if !sh.Use128x128Superblock {
		maxTileAreaSB = 4096 * 2304 / (64 * 64)
	}
	minLog2Tiles := tileLog2(maxTileAreaSB, uint32(sbCols)*uint32(sbRows))
	if minLog2Tiles < int(minLog2TileCols) {
		minLog2Tiles = int(minLog2TileCols)
	}

	// uniform_tile_spacing_flag f(1)
	w.WriteBool(p.TileInfo.UniformSpacing)

	if p.TileInfo.UniformSpacing {
		// Write increment bits for tile columns.
		tileColsLog2 := int(minLog2TileCols)
		targetColsLog2 := p.TileInfo.TileColsLog2
		if targetColsLog2 < tileColsLog2 {
			targetColsLog2 = tileColsLog2
		}
		for tileColsLog2 < int(maxLog2TileCols) {
			if tileColsLog2 >= targetColsLog2 {
				w.WriteBool(false) // don't increment
				break
			}
			w.WriteBool(true) // increment
			tileColsLog2++
		}

		// Write increment bits for tile rows.
		if tileColsLog2 > int(minLog2Tiles) {
			minLog2TileRows = 0
		} else {
			minLog2TileRows = minLog2Tiles - tileColsLog2
		}

		tileRowsLog2 := minLog2TileRows
		targetRowsLog2 := p.TileInfo.TileRowsLog2
		if targetRowsLog2 < tileRowsLog2 {
			targetRowsLog2 = tileRowsLog2
		}
		for tileRowsLog2 < int(maxLog2TileRows) {
			if tileRowsLog2 >= targetRowsLog2 {
				w.WriteBool(false) // don't increment
				break
			}
			w.WriteBool(true) // increment
			tileRowsLog2++
		}

		numTiles := (1 << uint(tileColsLog2)) * (1 << uint(tileRowsLog2))

		if numTiles > 1 {
			tileBits := tileColsLog2 + tileRowsLog2
			// context_update_tile_id f(tileBits)
			w.WriteBits(0, tileBits)
			// tile_size_bytes_minus_1 f(2)
			w.WriteBits(uint64(3), 2) // 4 bytes for tile sizes
		}
	} else {
		return fmt.Errorf("non-uniform tile spacing not yet implemented")
	}

	_ = minLog2TileRows

	return nil
}

// writeQuantizationParams writes quantization_params() per spec Section 5.9.12.
func writeQuantizationParams(w *bitstream.Writer, p *FrameHeaderParams, sh *SequenceHeader, numPlanes int) {
	// base_q_idx f(8)
	w.WriteBits(uint64(p.BaseQIndex), 8)

	// DeltaQYDc: read_delta_q()
	writeDeltaQ(w, p.DeltaQYDC)

	if numPlanes > 1 {
		diffUVDelta := p.DeltaQUDC != p.DeltaQVDC || p.DeltaQUAC != p.DeltaQVAC
		if sh.ColorConfig.SeparateUVDeltaQ {
			w.WriteBool(diffUVDelta)
		}

		// DeltaQUDc
		writeDeltaQ(w, p.DeltaQUDC)
		// DeltaQUAc
		writeDeltaQ(w, p.DeltaQUAC)

		if diffUVDelta && sh.ColorConfig.SeparateUVDeltaQ {
			// DeltaQVDc
			writeDeltaQ(w, p.DeltaQVDC)
			// DeltaQVAc
			writeDeltaQ(w, p.DeltaQVAC)
		}
	}

	// using_qmatrix f(1)
	w.WriteBool(p.UsingQMatrix)
	if p.UsingQMatrix {
		w.WriteBits(uint64(p.QMY), 4)
		w.WriteBits(uint64(p.QMU), 4)
		if sh.ColorConfig.SeparateUVDeltaQ {
			w.WriteBits(uint64(p.QMV), 4)
		}
	}
}

// writeDeltaQ writes read_delta_q() per spec: delta_coded f(1), then su(7) if coded.
func writeDeltaQ(w *bitstream.Writer, val int8) {
	if val != 0 {
		w.WriteBool(true) // delta_coded = 1
		w.WriteSu(int32(val), 7)
	} else {
		w.WriteBool(false) // delta_coded = 0
	}
}

// writeDeltaQParams writes delta_q_params() per spec Section 5.9.17.
func writeDeltaQParams(w *bitstream.Writer, p *FrameHeaderParams) {
	if p.BaseQIndex > 0 {
		w.WriteBool(p.DeltaQPresent)
	}
	if p.DeltaQPresent {
		w.WriteBits(uint64(p.DeltaQRes), 2)
	}
}

// writeDeltaLFParams writes delta_lf_params() per spec Section 5.9.18.
func writeDeltaLFParams(w *bitstream.Writer, p *FrameHeaderParams) {
	if p.DeltaQPresent {
		if !p.AllowIntraBC {
			w.WriteBool(p.DeltaLFPresent)
		}
		if p.DeltaLFPresent {
			w.WriteBits(uint64(p.DeltaLFRes), 2)
			w.WriteBool(p.DeltaLFMulti)
		}
	}
}

// writeLoopFilterParams writes loop_filter_params() per spec Section 5.9.11.
func writeLoopFilterParams(w *bitstream.Writer, p *FrameHeaderParams, numPlanes int) {
	// loop_filter_level[0] f(6)
	w.WriteBits(uint64(p.LoopFilterLevel[0]), 6)
	// loop_filter_level[1] f(6)
	w.WriteBits(uint64(p.LoopFilterLevel[1]), 6)

	if numPlanes > 1 && (p.LoopFilterLevel[0] != 0 || p.LoopFilterLevel[1] != 0) {
		// loop_filter_level[2] f(6)
		w.WriteBits(uint64(p.LoopFilterLevel[2]), 6)
		// loop_filter_level[3] f(6)
		w.WriteBits(uint64(p.LoopFilterLevel[3]), 6)
	}

	// loop_filter_sharpness f(3)
	w.WriteBits(uint64(p.LoopFilterSharpness), 3)

	// loop_filter_delta_enabled f(1)
	w.WriteBool(p.LoopFilterDeltaEnabled)

	if p.LoopFilterDeltaEnabled {
		// loop_filter_delta_update f(1)
		w.WriteBool(p.LoopFilterDeltaUpdate)

		if p.LoopFilterDeltaUpdate {
			// ref deltas
			for i := 0; i < NumRefFrames; i++ {
				if p.RefDeltas[i] != 0 {
					w.WriteBool(true) // update_ref_delta
					w.WriteSu(int32(p.RefDeltas[i]), 7)
				} else {
					w.WriteBool(false)
				}
			}
			// mode deltas
			for i := 0; i < 2; i++ {
				if p.ModeDeltas[i] != 0 {
					w.WriteBool(true) // update_mode_delta
					w.WriteSu(int32(p.ModeDeltas[i]), 7)
				} else {
					w.WriteBool(false)
				}
			}
		}
	}
}

// writeCDEFParams writes cdef_params() per spec Section 5.9.19.
func writeCDEFParams(w *bitstream.Writer, p *FrameHeaderParams, numPlanes int) {
	// cdef_damping_minus_3 f(2)
	w.WriteBits(uint64(p.CDEFDampingMinus3), 2)

	// cdef_bits f(2)
	w.WriteBits(uint64(p.CDEFBits), 2)

	numFilters := 1 << p.CDEFBits
	for i := 0; i < numFilters; i++ {
		yPri := uint8(0)
		if i < len(p.CDEFYPriStrength) {
			yPri = p.CDEFYPriStrength[i]
		}
		ySec := uint8(0)
		if i < len(p.CDEFYSecStrength) {
			ySec = p.CDEFYSecStrength[i]
		}

		w.WriteBits(uint64(yPri), 4) // cdef_y_pri_strength f(4)
		w.WriteBits(uint64(ySec), 2) // cdef_y_sec_strength f(2)

		if numPlanes > 1 {
			uvPri := uint8(0)
			if i < len(p.CDEFUVPriStrength) {
				uvPri = p.CDEFUVPriStrength[i]
			}
			uvSec := uint8(0)
			if i < len(p.CDEFUVSecStrength) {
				uvSec = p.CDEFUVSecStrength[i]
			}
			w.WriteBits(uint64(uvPri), 4) // cdef_uv_pri_strength f(4)
			w.WriteBits(uint64(uvSec), 2) // cdef_uv_sec_strength f(2)
		}
	}
}

// writeLRParams writes lr_params() per spec Section 5.9.20.
func writeLRParams(w *bitstream.Writer, p *FrameHeaderParams, sh *SequenceHeader, numPlanes int) {
	usesLR := false
	usesChromaLR := false

	for i := 0; i < numPlanes; i++ {
		// lr_type f(2)
		lrType := p.LRType[i]
		w.WriteBits(uint64(lrType), 2)
		if lrTypeRemap[lrType] != RestoreNone {
			usesLR = true
			if i > 0 {
				usesChromaLR = true
			}
		}
	}

	if usesLR {
		if sh.Use128x128Superblock {
			// lr_unit_shift: for 128x128 SB, base is 1 (meaning 128).
			// Write 1 bit for extra shift.
			w.WriteBool(p.LRUnitShift > 0)
		} else {
			// lr_unit_shift f(1)
			w.WriteBool(p.LRUnitShift > 0)
			if p.LRUnitShift > 0 {
				// lr_unit_extra_shift f(1)
				w.WriteBool(p.LRUnitShift > 1)
			}
		}

		if sh.ColorConfig.SubsamplingX == 1 && sh.ColorConfig.SubsamplingY == 1 && usesChromaLR {
			w.WriteBool(p.LRUVShift)
		}
	}
}

// writeGlobalMotionParams writes global_motion_params() for inter frames.
// Currently writes identity transforms for all reference frames.
func writeGlobalMotionParams(w *bitstream.Writer, p *FrameHeaderParams) {
	// For each reference frame: is_global f(1) = 0 (identity)
	for i := 0; i < RefsPerFrame; i++ {
		w.WriteBool(false) // is_global = 0 => IDENTITY
	}
}

// tileLog2 computes the smallest k such that (1 << k) >= target/blkSize
// This is used in tile_info() to compute min/max tile log2 values.
func tileLog2(blkSize, target uint32) int {
	if blkSize == 0 {
		return 0
	}
	k := 0
	for (uint32(1) << uint(k)) * blkSize < target {
		k++
	}
	return k
}

// NumTiles returns the number of tiles for the given params.
func (p *FrameHeaderParams) NumTiles() int {
	cols := 1 << uint(p.TileInfo.TileColsLog2)
	rows := 1 << uint(p.TileInfo.TileRowsLog2)
	return cols * rows
}

// TileSizeBytes returns the number of bytes used for tile size coding.
// For a single tile, no size is written. For multiple tiles, returns 4.
func (p *FrameHeaderParams) TileSizeBytes() int {
	if p.NumTiles() <= 1 {
		return 0
	}
	return 4
}

// helper to suppress unused import
var _ = bits.Len32
