// Package decoder implements AV1 bitstream decoding.
//
// This file implements full frame header parsing per AV1 spec Section 5.9.2
// (uncompressed_header). The existing ParseBasicFrameHeader in obu/parser.go
// only extracts the first few fields; this parser extracts ALL fields needed
// for tile decoding including quantization, loop filter, CDEF, restoration,
// tile layout, and more.
package decoder

import (
	"av1go/bitstream"
	"av1go/obu"
	"fmt"
)

// TX mode constants. AV1 spec Section 6.8.2.
const (
	TxModeOnly4x4 = 0 // ONLY_4X4: lossless only
	TxModeLargest  = 1 // TX_MODE_LARGEST: always use largest
	TxModeSelect_  = 2 // TX_MODE_SELECT: per-block TX size
)

// Interpolation filter constants. AV1 spec Section 6.8.13.
const (
	InterpFilterEighttapRegular = 0
	InterpFilterEighttapSmooth  = 1
	InterpFilterEighttapSharp   = 2
	InterpFilterBilinear        = 3
	InterpFilterSwitchable      = 4
)

// Frame restoration type constants (matching obu/frame_header_writer.go).
const (
	FrameRestoreNone       = 0
	FrameRestoreSwitchable = 1
	FrameRestoreWiener     = 2
	FrameRestoreSGRProj    = 3
)

// Spec constants for frame header parsing.
const (
	superresDenomMin  = 9  // SUPERRES_DENOM_MIN
	superresDenomBits = 3  // SUPERRES_DENOM_BITS
	superresNum       = 8  // SUPERRES_NUM
	maxTileWidth      = 4096
	maxTileArea       = 4096 * 2304
	maxTileRows_      = 64
	maxTileCols_      = 64
	maxSegments       = 8
	segLvlMax         = 8
	primaryRefNone    = 7
)

// Remap table for lr_type. AV1 spec Section 5.9.20.
// Coded values map to frame restoration type.
var frameRestoreTypeRemap = [4]int{
	FrameRestoreNone,
	FrameRestoreSwitchable,
	FrameRestoreWiener,
	FrameRestoreSGRProj,
}

// DecodedFrameHeader contains all parsed fields from uncompressed_header()
// needed for tile decoding. AV1 spec Section 5.9.2.
type DecodedFrameHeader struct {
	// Basic identification fields.
	ShowExistingFrame  bool
	FrameToShowMapIdx  uint8
	FrameType          obu.FrameType
	ShowFrame          bool
	ShowableFrame      bool
	ErrorResilientMode bool

	DisableCDFUpdate           bool
	DisableFrameEndUpdateCDF   bool // disable_frame_end_update_cdf
	AllowScreenContentTools    bool
	ForceIntegerMV          bool

	FrameSizeOverride bool
	OrderHint         uint32
	PrimaryRefFrame   uint8
	RefreshFrameFlags uint8

	// Frame dimensions. AV1 spec Section 5.9.5.
	FrameWidth    uint32
	FrameHeight   uint32
	RenderWidth   uint32
	RenderHeight  uint32
	UseSuperRes   bool
	SuperResDenom uint32
	UpscaledWidth uint32
	MiCols        uint32
	MiRows        uint32

	AllowIntraBC bool

	// Interpolation filter. AV1 spec Section 5.9.13.
	InterpFilter       int
	IsFilterSwitchable bool

	// Quantization parameters. AV1 spec Section 5.9.12.
	BaseQIndex   uint8
	DeltaQYDC    int
	DeltaQUDC    int
	DeltaQUAC    int
	DeltaQVDC    int
	DeltaQVAC    int
	UsingQMatrix bool
	QMY          uint8
	QMU          uint8
	QMV          uint8

	// Segmentation. AV1 spec Section 5.9.14.
	SegmentationEnabled        bool
	SegmentationUpdateMap      bool
	SegmentationTemporalUpdate bool
	SegmentationUpdateData     bool
	SegFeatureActive           [maxSegments][segLvlMax]bool
	SegFeatureData             [maxSegments][segLvlMax]int

	// Delta Q. AV1 spec Section 5.9.17.
	DeltaQPresent bool
	DeltaQRes     uint8

	// Delta LF. AV1 spec Section 5.9.18.
	DeltaLFPresent bool
	DeltaLFRes     uint8
	DeltaLFMulti   bool

	// Loop filter. AV1 spec Section 5.9.11.
	LoopFilterLevel        [4]uint8
	LoopFilterSharpness    uint8
	LoopFilterDeltaEnabled bool
	LoopFilterDeltaUpdate  bool
	RefDeltas              [obu.NumRefFrames]int8
	ModeDeltas             [2]int8

	// CDEF. AV1 spec Section 5.9.19.
	CDEFDamping       int
	CDEFBits          int
	CDEFYPriStrength  [8]int
	CDEFYSecStrength  [8]int
	CDEFUVPriStrength [8]int
	CDEFUVSecStrength [8]int

	// Loop restoration. AV1 spec Section 5.9.20.
	LRType      [3]int // per plane: 0=none, 1=switchable, 2=wiener, 3=sgrproj
	LRUnitShift int
	LRUVShift   bool

	// TX mode. AV1 spec Section 5.9.21.
	TxMode int

	// Reference frames (inter). AV1 spec Section 5.9.2.
	ReferenceSelect        bool
	SkipModePresent        bool
	SkipModeFrame          [2]int8 // skip_mode reference indices (0-6), derived by skip_mode_params()
	AllowHighPrecisionMV   bool
	AllowWarpedMotion      bool
	ReducedTxSet           bool
	IsMotionModeSwitchable bool
	UseRefFrameMVs         bool

	RefFrameIdx [obu.RefsPerFrame]uint8

	// Global motion parameters.
	GmType   [obu.RefsPerFrame]int      // 0=IDENTITY, 1=TRANSLATION, 2=ROTZOOM, 3=AFFINE
	GmParams [obu.RefsPerFrame][6]int32 // up to 6 params per ref

	// Tile info. AV1 spec Section 5.9.15.
	TileCols            int
	TileRows            int
	TileColsLog2        int
	TileRowsLog2        int
	TileColStarts       []int // MI column start for each tile column + 1 sentinel
	TileRowStarts       []int // MI row start for each tile row + 1 sentinel
	TileSizeBytes       int   // bytes used for tile size coding (1-4)
	ContextUpdateTileID int

	// Derived values.
	CodedLossless bool
	AllLossless   bool
	NumPlanes     int
}

// ParseFullFrameHeader parses all fields of uncompressed_header() from the
// given payload bytes, using the active sequence header for context.
// refOrderHints contains the order hint stored for each of the 8 reference
// frame buffer slots, used by set_frame_refs() when frame_refs_short_signaling
// is enabled.
//
// Returns the parsed header and the byte offset where tile group data begins
// (i.e., the position after the frame header including byte alignment).
//
// AV1 spec Section 5.9.2.
func ParseFullFrameHeader(payload []byte, sh *obu.SequenceHeader, refOrderHints [8]uint32, savedGmParams [8][obu.RefsPerFrame][6]int32) (*DecodedFrameHeader, int, error) {
	r := bitstream.NewReader(payload)
	fh := &DecodedFrameHeader{}

	numPlanes := 3
	if sh.ColorConfig.MonoChrome {
		numPlanes = 1
	}
	fh.NumPlanes = numPlanes

	var err error

	// --- Reduced still picture header shortcut ---
	// AV1 spec Section 5.9.2: if reduced_still_picture_header, many fields
	// are forced to their key-frame defaults.
	if sh.ReducedStillPictureHeader {
		fh.ShowExistingFrame = false
		fh.FrameType = obu.FrameTypeKey
		fh.ShowFrame = true
		fh.ShowableFrame = false
		fh.ErrorResilientMode = true
		fh.PrimaryRefFrame = primaryRefNone
		fh.RefreshFrameFlags = 0xFF

		// Frame size defaults to sequence max.
		fh.FrameWidth = sh.MaxFrameWidthMinus1 + 1
		fh.FrameHeight = sh.MaxFrameHeightMinus1 + 1
		fh.UpscaledWidth = fh.FrameWidth
		fh.RenderWidth = fh.FrameWidth
		fh.RenderHeight = fh.FrameHeight
		fh.SuperResDenom = superresNum

		fh.MiCols = 2 * ((fh.FrameWidth + 7) >> 3)
		fh.MiRows = 2 * ((fh.FrameHeight + 7) >> 3)

		// Parse sections that still apply for reduced still picture header.
		if err := parseTileInfo(r, fh, sh); err != nil {
			return nil, 0, fmt.Errorf("decoder: tile_info: %w", err)
		}

		if err := parseQuantizationParams(r, fh, sh); err != nil {
			return nil, 0, fmt.Errorf("decoder: quantization_params: %w", err)
		}
		fh.DeltaQPresent = false
		fh.CodedLossless = isCodedLossless(fh)
		fh.AllLossless = fh.CodedLossless
		if fh.CodedLossless {
			fh.TxMode = TxModeOnly4x4
		} else {
			fh.TxMode = TxModeLargest
		}

		r.ByteAlign()
		return fh, (r.BitsRead() + 7) / 8, nil
	}

	// --- Normal (non-reduced) frame header ---

	// show_existing_frame f(1)
	fh.ShowExistingFrame, err = r.ReadFlag()
	if err != nil {
		return nil, 0, wrapErr("show_existing_frame", err)
	}

	if fh.ShowExistingFrame {
		// frame_to_show_map_idx f(3)
		fh.FrameToShowMapIdx, err = r.ReadUint8(3)
		if err != nil {
			return nil, 0, wrapErr("frame_to_show_map_idx", err)
		}
		// For show_existing_frame, the header is done after this point
		// (plus any temporal_point_info and decoder_model_info, which
		// we skip for now). The caller should display the referenced frame.
		r.ByteAlign()
		return fh, (r.BitsRead() + 7) / 8, nil
	}

	// frame_type f(2)
	ft, err := r.ReadUint8(2)
	if err != nil {
		return nil, 0, wrapErr("frame_type", err)
	}
	fh.FrameType = obu.FrameType(ft)

	// show_frame f(1)
	fh.ShowFrame, err = r.ReadFlag()
	if err != nil {
		return nil, 0, wrapErr("show_frame", err)
	}

	if fh.ShowFrame {
		// temporal_point_info() if decoder model present.
		if sh.DecoderModelInfoPresent && sh.TimingInfo != nil && !sh.TimingInfo.EqualPictureInterval {
			n := int(sh.DecoderModelInfo.FramePresentationTimeLengthMinus1) + 1
			if _, err := r.ReadBits(n); err != nil {
				return nil, 0, wrapErr("frame_presentation_time", err)
			}
		}
		fh.ShowableFrame = fh.FrameType != obu.FrameTypeKey
	} else {
		// showable_frame f(1)
		fh.ShowableFrame, err = r.ReadFlag()
		if err != nil {
			return nil, 0, wrapErr("showable_frame", err)
		}
	}

	// error_resilient_mode
	frameIsIntra := fh.FrameType == obu.FrameTypeKey || fh.FrameType == obu.FrameTypeIntraOnly
	if fh.FrameType == obu.FrameTypeSwitch ||
		(fh.FrameType == obu.FrameTypeKey && fh.ShowFrame) {
		fh.ErrorResilientMode = true
	} else {
		fh.ErrorResilientMode, err = r.ReadFlag()
		if err != nil {
			return nil, 0, wrapErr("error_resilient_mode", err)
		}
	}

	// disable_cdf_update f(1)
	fh.DisableCDFUpdate, err = r.ReadFlag()
	if err != nil {
		return nil, 0, wrapErr("disable_cdf_update", err)
	}

	// allow_screen_content_tools
	if sh.SeqForceScreenContentTools == obu.SelectScreenContentTools {
		fh.AllowScreenContentTools, err = r.ReadFlag()
		if err != nil {
			return nil, 0, wrapErr("allow_screen_content_tools", err)
		}
	} else {
		fh.AllowScreenContentTools = sh.SeqForceScreenContentTools == 1
	}

	// force_integer_mv
	if fh.AllowScreenContentTools {
		if sh.SeqForceIntegerMV == obu.SelectIntegerMV {
			fh.ForceIntegerMV, err = r.ReadFlag()
			if err != nil {
				return nil, 0, wrapErr("force_integer_mv", err)
			}
		} else {
			fh.ForceIntegerMV = sh.SeqForceIntegerMV == 1
		}
	}

	// current_frame_id
	if sh.FrameIDNumbersPresent {
		idLen := int(sh.DeltaFrameIDLengthMinus2) + 2 + int(sh.AdditionalFrameIDLengthMinus1) + 1
		if _, err := r.ReadBits(idLen); err != nil {
			return nil, 0, wrapErr("current_frame_id", err)
		}
	}

	// frame_size_override_flag
	if fh.FrameType == obu.FrameTypeSwitch {
		fh.FrameSizeOverride = true
	} else {
		fh.FrameSizeOverride, err = r.ReadFlag()
		if err != nil {
			return nil, 0, wrapErr("frame_size_override_flag", err)
		}
	}

	// order_hint
	if sh.EnableOrderHint {
		orderHintBits := int(sh.OrderHintBitsMinus1) + 1
		oh, err := r.ReadUint32(orderHintBits)
		if err != nil {
			return nil, 0, wrapErr("order_hint", err)
		}
		fh.OrderHint = oh
	}

	// primary_ref_frame
	if frameIsIntra || fh.ErrorResilientMode {
		fh.PrimaryRefFrame = primaryRefNone
	} else {
		fh.PrimaryRefFrame, err = r.ReadUint8(3)
		if err != nil {
			return nil, 0, wrapErr("primary_ref_frame", err)
		}
	}

	// decoder_model_info - skip buffer_removal_time for each operating point.
	if sh.DecoderModelInfoPresent {
		for i := 0; i <= int(sh.OperatingPointsCntMinus1); i++ {
			if sh.OperatingPoints[i].DecoderModelPresentForOP {
				n := int(sh.DecoderModelInfo.BufferRemovalTimeLengthMinus1) + 1
				if _, err := r.ReadBits(n); err != nil {
					return nil, 0, wrapErr("buffer_removal_time", err)
				}
			}
		}
	}

	// refresh_frame_flags
	if fh.FrameType == obu.FrameTypeSwitch ||
		(fh.FrameType == obu.FrameTypeKey && fh.ShowFrame) {
		fh.RefreshFrameFlags = 0xFF
	} else {
		fh.RefreshFrameFlags, err = r.ReadUint8(8)
		if err != nil {
			return nil, 0, wrapErr("refresh_frame_flags", err)
		}
	}

	// For inter frames: ref_order_hint. AV1 spec Section 5.9.2.
	if !frameIsIntra {
		if fh.ErrorResilientMode && sh.EnableOrderHint {
			orderHintBits := int(sh.OrderHintBitsMinus1) + 1
			for i := 0; i < obu.NumRefFrames; i++ {
				if _, err := r.ReadBits(orderHintBits); err != nil {
					return nil, 0, wrapErr("ref_order_hint", err)
				}
			}
		}
	}

	if frameIsIntra {
		// Intra path: frame_size → render_size → allow_intrabc.
		if err := parseFrameSize(r, fh, sh, frameIsIntra); err != nil {
			return nil, 0, fmt.Errorf("decoder: frame_size: %w", err)
		}
		if err := parseRenderSize(r, fh); err != nil {
			return nil, 0, fmt.Errorf("decoder: render_size: %w", err)
		}
		if fh.AllowScreenContentTools && !fh.UseSuperRes {
			fh.AllowIntraBC, err = r.ReadFlag()
			if err != nil {
				return nil, 0, wrapErr("allow_intrabc", err)
			}
		}
	} else {
		// Inter path: ref_frame_idx → frame_size_with_refs/frame_size → render_size
		// → allow_high_precision_mv → interp_filter → motion fields.
		// AV1 spec Section 5.9.2.

		// frame_refs_short_signaling - AV1 spec Section 5.9.2.
		// When enable_order_hint is true, a flag indicates whether short
		// ref signaling is used (derives refs from last_frame_idx/gold_frame_idx).
		frameRefsShortSignaling := false
		if sh.EnableOrderHint {
			frameRefsShortSignaling, err = r.ReadFlag()
			if err != nil {
				return nil, 0, wrapErr("frame_refs_short_signaling", err)
			}
			if frameRefsShortSignaling {
				// last_frame_idx f(3) and gold_frame_idx f(3)
				lastFrameIdx, err := r.ReadUint8(3)
				if err != nil {
					return nil, 0, wrapErr("last_frame_idx", err)
				}
				goldFrameIdx, err := r.ReadUint8(3)
				if err != nil {
					return nil, 0, wrapErr("gold_frame_idx", err)
				}
				// Derive all 7 reference indices from last and golden.
				// AV1 spec Section 7.8.
				setFrameRefs(fh, sh, refOrderHints, lastFrameIdx, goldFrameIdx)
			}
		}

		// ref_frame_idx[i] f(3) for i in 0..6
		for i := 0; i < obu.RefsPerFrame; i++ {
			if !frameRefsShortSignaling {
				fh.RefFrameIdx[i], err = r.ReadUint8(3)
				if err != nil {
					return nil, 0, wrapErr("ref_frame_idx", err)
				}
			}
		}

		// frame_size_with_refs() or frame_size() + render_size().
		// AV1 spec Section 5.9.7.
		if fh.FrameSizeOverride && !fh.ErrorResilientMode {
			if err := parseFrameSizeWithRefs(r, fh, sh); err != nil {
				return nil, 0, fmt.Errorf("decoder: frame_size_with_refs: %w", err)
			}
		} else {
			if err := parseFrameSize(r, fh, sh, frameIsIntra); err != nil {
				return nil, 0, fmt.Errorf("decoder: frame_size: %w", err)
			}
			if err := parseRenderSize(r, fh); err != nil {
				return nil, 0, fmt.Errorf("decoder: render_size: %w", err)
			}
		}

		// allow_high_precision_mv f(1)
		if !fh.ForceIntegerMV {
			fh.AllowHighPrecisionMV, err = r.ReadFlag()
			if err != nil {
				return nil, 0, wrapErr("allow_high_precision_mv", err)
			}
		}

		// read_interpolation_filter() - AV1 spec Section 5.9.13
		if err := parseInterpFilter(r, fh); err != nil {
			return nil, 0, fmt.Errorf("decoder: interp_filter: %w", err)
		}

		// is_motion_mode_switchable f(1)
		fh.IsMotionModeSwitchable, err = r.ReadFlag()
		if err != nil {
			return nil, 0, wrapErr("is_motion_mode_switchable", err)
		}

		// use_ref_frame_mvs
		if !fh.ErrorResilientMode && sh.EnableRefFrameMVS {
			fh.UseRefFrameMVs, err = r.ReadFlag()
			if err != nil {
				return nil, 0, wrapErr("use_ref_frame_mvs", err)
			}
		}
	}

	// disable_frame_end_update_cdf - AV1 spec Section 5.9.2
	// This field controls whether CDF probabilities are updated at
	// the end of the frame. Only present when !reduced_still_picture_header
	// and !disable_cdf_update.
	if !sh.ReducedStillPictureHeader && !fh.DisableCDFUpdate {
		disableFrameEndUpdateCDF, err := r.ReadFlag()
		if err != nil {
			return nil, 0, wrapErr("disable_frame_end_update_cdf", err)
		}
		fh.DisableFrameEndUpdateCDF = disableFrameEndUpdateCDF
	}

	// Compute MI dimensions.
	fh.MiCols = 2 * ((fh.FrameWidth + 7) >> 3)
	fh.MiRows = 2 * ((fh.FrameHeight + 7) >> 3)

	// tile_info() - AV1 spec Section 5.9.15
	if err := parseTileInfo(r, fh, sh); err != nil {
		return nil, 0, fmt.Errorf("decoder: tile_info: %w", err)
	}

	// quantization_params() - AV1 spec Section 5.9.12
	if err := parseQuantizationParams(r, fh, sh); err != nil {
		return nil, 0, fmt.Errorf("decoder: quantization_params: %w", err)
	}

	// segmentation_params() - AV1 spec Section 5.9.14
	if err := parseSegmentationParams(r, fh); err != nil {
		return nil, 0, fmt.Errorf("decoder: segmentation_params: %w", err)
	}

	// delta_q_params() - AV1 spec Section 5.9.17
	if err := parseDeltaQParams(r, fh); err != nil {
		return nil, 0, fmt.Errorf("decoder: delta_q_params: %w", err)
	}

	// delta_lf_params() - AV1 spec Section 5.9.18
	if err := parseDeltaLFParams(r, fh); err != nil {
		return nil, 0, fmt.Errorf("decoder: delta_lf_params: %w", err)
	}

	// Derive lossless state.
	fh.CodedLossless = isCodedLossless(fh)
	fh.AllLossless = fh.CodedLossless // Simplified: without segmentation QP deltas.

	// loop_filter_params() - AV1 spec Section 5.9.11
	if !fh.CodedLossless && !fh.AllowIntraBC {
		if err := parseLoopFilterParams(r, fh, numPlanes); err != nil {
			return nil, 0, fmt.Errorf("decoder: loop_filter_params: %w", err)
		}
	}

	// cdef_params() - AV1 spec Section 5.9.19
	if !fh.CodedLossless && !fh.AllowIntraBC && sh.EnableCDEF {
		if err := parseCDEFParams(r, fh, numPlanes); err != nil {
			return nil, 0, fmt.Errorf("decoder: cdef_params: %w", err)
		}
	}

	// lr_params() - AV1 spec Section 5.9.20
	if !fh.CodedLossless && !fh.AllowIntraBC && sh.EnableRestoration {
		if err := parseLRParams(r, fh, sh, numPlanes); err != nil {
			return nil, 0, fmt.Errorf("decoder: lr_params: %w", err)
		}
	}

	// read_tx_mode() - AV1 spec Section 5.9.21
	if fh.CodedLossless {
		fh.TxMode = TxModeOnly4x4
	} else {
		txModeSelect, err := r.ReadFlag()
		if err != nil {
			return nil, 0, wrapErr("tx_mode_select", err)
		}
		if txModeSelect {
			fh.TxMode = TxModeSelect_
		} else {
			fh.TxMode = TxModeLargest
		}
	}

	// frame_reference_mode() - AV1 spec Section 5.9.23
	if frameIsIntra {
		fh.ReferenceSelect = false
	} else {
		fh.ReferenceSelect, err = r.ReadFlag()
		if err != nil {
			return nil, 0, wrapErr("reference_select", err)
		}
	}

	// skip_mode_params() - AV1 spec Section 5.9.22
	// Must check skipModeAllowed by finding valid forward AND backward references.
	fh.SkipModePresent = false
	if !frameIsIntra && fh.ReferenceSelect && sh.EnableOrderHint {
		// Derive skip mode reference frames.
		// Need both a forward ref (orderHint < current) and backward ref (orderHint > current).
		orderHintBits := int(sh.OrderHintBitsMinus1) + 1
		curOrderHint := fh.OrderHint
		forwardIdx := -1
		backwardIdx := -1
		var forwardHint, backwardHint uint32
		for i := 0; i < obu.RefsPerFrame; i++ {
			slot := fh.RefFrameIdx[i]
			refHint := refOrderHints[slot]
			if getRelativeDist(refHint, curOrderHint, orderHintBits) < 0 {
				// Forward reference (in the past).
				if forwardIdx < 0 || getRelativeDist(refHint, forwardHint, orderHintBits) > 0 {
					forwardIdx = i
					forwardHint = refHint
				}
			} else if getRelativeDist(refHint, curOrderHint, orderHintBits) > 0 {
				// Backward reference (in the future).
				if backwardIdx < 0 || getRelativeDist(refHint, backwardHint, orderHintBits) < 0 {
					backwardIdx = i
					backwardHint = refHint
				}
			}
		}
		skipModeAllowed := forwardIdx >= 0 && backwardIdx >= 0
		if skipModeAllowed {
			// AV1 spec: SkipModeFrame[0] = nearest forward, SkipModeFrame[1] = nearest backward.
			fh.SkipModeFrame[0] = int8(forwardIdx)
			fh.SkipModeFrame[1] = int8(backwardIdx)
		}
		if !skipModeAllowed && forwardIdx >= 0 {
			// Try to find two forward references with different order hints.
			// AV1 spec: if no backward ref, look for second-closest forward ref.
			secondForwardIdx := -1
			var secondForwardHint uint32
			for i := 0; i < obu.RefsPerFrame; i++ {
				slot := fh.RefFrameIdx[i]
				refHint := refOrderHints[slot]
				if getRelativeDist(refHint, curOrderHint, orderHintBits) < 0 {
					if refHint != forwardHint {
						if secondForwardIdx < 0 || getRelativeDist(refHint, secondForwardHint, orderHintBits) < 0 {
							secondForwardIdx = i
							secondForwardHint = refHint
						}
					}
				}
			}
			skipModeAllowed = secondForwardIdx >= 0
			if skipModeAllowed {
				// AV1 spec: both forward refs, order by POC distance.
				// SkipModeFrame[0] = farther (secondForwardIdx), [1] = closer (forwardIdx).
				fh.SkipModeFrame[0] = int8(secondForwardIdx)
				fh.SkipModeFrame[1] = int8(forwardIdx)
			}
		}
		if skipModeAllowed {
			fh.SkipModePresent, err = r.ReadFlag()
			if err != nil {
				return nil, 0, wrapErr("skip_mode_present", err)
			}
		}
	}

	// allow_warped_motion
	if !frameIsIntra && !fh.ErrorResilientMode && sh.EnableWarpedMotion {
		fh.AllowWarpedMotion, err = r.ReadFlag()
		if err != nil {
			return nil, 0, wrapErr("allow_warped_motion", err)
		}
	}

	// reduced_tx_set f(1)
	fh.ReducedTxSet, err = r.ReadFlag()
	if err != nil {
		return nil, 0, wrapErr("reduced_tx_set", err)
	}

	// global_motion_params() - AV1 spec Section 5.9.24
	// Derive PrevGmParams from primary_ref_frame's saved GM params.
	var prevGmParams [obu.RefsPerFrame][6]int32
	if fh.PrimaryRefFrame == primaryRefNone {
		// Identity for all refs.
		for ref := 0; ref < obu.RefsPerFrame; ref++ {
			prevGmParams[ref] = [6]int32{0, 0, 1 << warpedModelPrecBits, 0, 0, 1 << warpedModelPrecBits}
		}
	} else {
		prevSlot := fh.RefFrameIdx[fh.PrimaryRefFrame]
		prevGmParams = savedGmParams[prevSlot]
	}
	if !frameIsIntra {
		if err := parseGlobalMotionParamsWithPrev(r, fh, prevGmParams); err != nil {
			return nil, 0, fmt.Errorf("decoder: global_motion_params: %w", err)
		}
	}

	// film_grain_params() - skip for now.
	// Would be parsed here if sh.FilmGrainParamsPresent.

	// AV1 spec Section 5.11 (frame_obu): after frame_header_obu(),
	// use byte_alignment() (NOT trailing_bits). trailing_bits is only
	// for standalone OBU_FRAME_HEADER (type 3), not for OBU_FRAME (type 6).
	// byte_alignment() pads with zero bits to the next byte boundary.
	r.ByteAlign()
	byteOffset := (r.BitsRead() + 7) / 8

	return fh, byteOffset, nil
}

// --- Sub-parsers for frame header sections ---

// parseFrameSize parses frame_size(). AV1 spec Section 5.9.5.
func parseFrameSize(r *bitstream.Reader, fh *DecodedFrameHeader, sh *obu.SequenceHeader, frameIsIntra bool) error {
	if fh.FrameSizeOverride {
		widthBits := int(sh.FrameWidthBitsMinus1) + 1
		heightBits := int(sh.FrameHeightBitsMinus1) + 1

		w, err := r.ReadUint32(widthBits)
		if err != nil {
			return wrapErr("frame_width_minus_1", err)
		}
		fh.FrameWidth = w + 1

		h, err := r.ReadUint32(heightBits)
		if err != nil {
			return wrapErr("frame_height_minus_1", err)
		}
		fh.FrameHeight = h + 1
	} else {
		fh.FrameWidth = sh.MaxFrameWidthMinus1 + 1
		fh.FrameHeight = sh.MaxFrameHeightMinus1 + 1
	}

	// superres_params() - AV1 spec Section 5.9.8
	if err := parseSuperResParams(r, fh, sh); err != nil {
		return err
	}

	return nil
}

// parseSuperResParams parses superres_params(). AV1 spec Section 5.9.8.
func parseSuperResParams(r *bitstream.Reader, fh *DecodedFrameHeader, sh *obu.SequenceHeader) error {
	var err error
	if sh.EnableSuperRes {
		fh.UseSuperRes, err = r.ReadFlag()
		if err != nil {
			return wrapErr("use_superres", err)
		}
	}

	if fh.UseSuperRes {
		codedDenom, err := r.ReadUint8(superresDenomBits)
		if err != nil {
			return wrapErr("coded_denom", err)
		}
		fh.SuperResDenom = uint32(codedDenom) + superresDenomMin
	} else {
		fh.SuperResDenom = superresNum
	}

	// Compute upscaled width and actual frame width.
	// AV1 spec Section 5.9.8:
	// UpscaledWidth = FrameWidth
	// FrameWidth = (UpscaledWidth * SUPERRES_NUM + (SuperResDenom / 2)) / SuperResDenom
	fh.UpscaledWidth = fh.FrameWidth
	if fh.UseSuperRes {
		fh.FrameWidth = (fh.UpscaledWidth*superresNum + (fh.SuperResDenom / 2)) / fh.SuperResDenom
	}

	return nil
}

// parseRenderSize parses render_size(). AV1 spec Section 5.9.6.
func parseRenderSize(r *bitstream.Reader, fh *DecodedFrameHeader) error {
	renderSizeDiff, err := r.ReadFlag()
	if err != nil {
		return wrapErr("render_and_frame_size_different", err)
	}

	if renderSizeDiff {
		rw, err := r.ReadUint32(16)
		if err != nil {
			return wrapErr("render_width_minus_1", err)
		}
		fh.RenderWidth = rw + 1

		rh, err := r.ReadUint32(16)
		if err != nil {
			return wrapErr("render_height_minus_1", err)
		}
		fh.RenderHeight = rh + 1
	} else {
		fh.RenderWidth = fh.UpscaledWidth
		fh.RenderHeight = fh.FrameHeight
	}

	return nil
}

// parseFrameSizeWithRefs parses frame_size_with_refs(). AV1 spec Section 5.9.7.
// For inter frames with frame_size_override && !error_resilient, tries to
// reuse a reference frame's dimensions before falling back to frame_size().
func parseFrameSizeWithRefs(r *bitstream.Reader, fh *DecodedFrameHeader, sh *obu.SequenceHeader) error {
	foundRef := false
	for i := 0; i < obu.RefsPerFrame; i++ {
		found, err := r.ReadFlag()
		if err != nil {
			return wrapErr("found_ref", err)
		}
		if found {
			// Use reference frame dimensions (we don't track ref dimensions yet,
			// so fall back to sequence header defaults).
			fh.FrameWidth = sh.MaxFrameWidthMinus1 + 1
			fh.FrameHeight = sh.MaxFrameHeightMinus1 + 1
			foundRef = true
			break
		}
	}
	if !foundRef {
		// No reference matched: read explicit frame_size().
		if err := parseFrameSize(r, fh, sh, false); err != nil {
			return err
		}
	} else {
		// superres_params() still needs to be parsed.
		if err := parseSuperResParams(r, fh, sh); err != nil {
			return err
		}
	}
	// render_size() is always parsed after frame_size_with_refs.
	if err := parseRenderSize(r, fh); err != nil {
		return err
	}
	return nil
}

// parseInterpFilter parses read_interpolation_filter(). AV1 spec Section 5.9.13.
func parseInterpFilter(r *bitstream.Reader, fh *DecodedFrameHeader) error {
	isFilterSwitchable, err := r.ReadFlag()
	if err != nil {
		return wrapErr("is_filter_switchable", err)
	}
	fh.IsFilterSwitchable = isFilterSwitchable

	if isFilterSwitchable {
		fh.InterpFilter = InterpFilterSwitchable
	} else {
		f, err := r.ReadUint8(2)
		if err != nil {
			return wrapErr("interpolation_filter", err)
		}
		fh.InterpFilter = int(f)
	}

	return nil
}

// parseTileInfo parses tile_info(). AV1 spec Section 5.9.15.
func parseTileInfo(r *bitstream.Reader, fh *DecodedFrameHeader, sh *obu.SequenceHeader) error {
	miCols := int(fh.MiCols)
	miRows := int(fh.MiRows)

	// Compute superblock size in MI units.
	var sbMISize int
	if sh.Use128x128Superblock {
		sbMISize = 32 // 128/4
	} else {
		sbMISize = 16 // 64/4
	}

	sbCols := (miCols + sbMISize - 1) / sbMISize
	sbRows := (miRows + sbMISize - 1) / sbMISize

	// Compute superblock size in pixels for tile width limit.
	sbSizePixels := sbMISize * 4 // MI is 4x4 pixels

	// Max tile width in superblocks.
	maxTileWidthSB := maxTileWidth / sbSizePixels

	// Tile area constraint in superblocks.
	maxTileAreaSB := maxTileArea / (sbSizePixels * sbSizePixels)

	// Compute min/max log2 tile columns.
	minLog2TileCols := tileLog2Calc(maxTileWidthSB, sbCols)
	maxLog2TileCols := tileLog2Calc(1, sbCols)
	if maxLog2TileCols > 6 {
		maxLog2TileCols = 6
	}

	maxLog2TileRows := tileLog2Calc(1, sbRows)
	if maxLog2TileRows > 6 {
		maxLog2TileRows = 6
	}

	minLog2Tiles := tileLog2Calc(maxTileAreaSB, sbCols*sbRows)
	if minLog2Tiles < minLog2TileCols {
		minLog2Tiles = minLog2TileCols
	}

	// uniform_tile_spacing_flag f(1)
	uniformSpacing, err := r.ReadFlag()
	if err != nil {
		return wrapErr("uniform_tile_spacing_flag", err)
	}

	if uniformSpacing {
		// Parse tile columns log2.
		tileColsLog2 := minLog2TileCols
		for tileColsLog2 < maxLog2TileCols {
			increment, err := r.ReadFlag()
			if err != nil {
				return wrapErr("increment_tile_cols_log2", err)
			}
			if increment {
				tileColsLog2++
			} else {
				break
			}
		}
		fh.TileColsLog2 = tileColsLog2

		// Compute tile column starts.
		tileWidthSB := (sbCols + (1 << tileColsLog2) - 1) >> tileColsLog2
		tileCols := 0
		fh.TileColStarts = []int{}
		for startSB := 0; startSB < sbCols; startSB += tileWidthSB {
			fh.TileColStarts = append(fh.TileColStarts, startSB*sbMISize)
			tileCols++
		}
		fh.TileColStarts = append(fh.TileColStarts, miCols) // sentinel
		fh.TileCols = tileCols

		// Compute minimum log2 tile rows.
		minLog2TileRows := 0
		if minLog2Tiles > tileColsLog2 {
			minLog2TileRows = minLog2Tiles - tileColsLog2
		}

		// Parse tile rows log2.
		tileRowsLog2 := minLog2TileRows
		for tileRowsLog2 < maxLog2TileRows {
			increment, err := r.ReadFlag()
			if err != nil {
				return wrapErr("increment_tile_rows_log2", err)
			}
			if increment {
				tileRowsLog2++
			} else {
				break
			}
		}
		fh.TileRowsLog2 = tileRowsLog2

		// Compute tile row starts.
		tileHeightSB := (sbRows + (1 << tileRowsLog2) - 1) >> tileRowsLog2
		tileRows := 0
		fh.TileRowStarts = []int{}
		for startSB := 0; startSB < sbRows; startSB += tileHeightSB {
			fh.TileRowStarts = append(fh.TileRowStarts, startSB*sbMISize)
			tileRows++
		}
		fh.TileRowStarts = append(fh.TileRowStarts, miRows) // sentinel
		fh.TileRows = tileRows
	} else {
		// Non-uniform (explicit) tile spacing.
		// Parse tile column widths in superblocks.
		fh.TileColStarts = []int{}
		widestTileSB := 0
		startSB := 0
		tileCols := 0
		for startSB < sbCols {
			fh.TileColStarts = append(fh.TileColStarts, startSB*sbMISize)
			maxWidth := sbCols - startSB
			if maxWidth > maxTileWidthSB {
				maxWidth = maxTileWidthSB
			}
			widthSB, err := r.ReadNs(maxWidth)
			if err != nil {
				return wrapErr("tile_width_in_sbs_minus_1", err)
			}
			sizeSB := int(widthSB) + 1
			if sizeSB > widestTileSB {
				widestTileSB = sizeSB
			}
			startSB += sizeSB
			tileCols++
		}
		fh.TileColStarts = append(fh.TileColStarts, miCols)
		fh.TileCols = tileCols
		fh.TileColsLog2 = tileLog2Calc(1, tileCols)

		// Max tile height in superblocks based on widest tile.
		maxTileHeightSB := 1
		if widestTileSB > 0 {
			maxTileHeightSB = maxTileAreaSB / widestTileSB
			if maxTileHeightSB < 1 {
				maxTileHeightSB = 1
			}
		}

		// Parse tile row heights in superblocks.
		fh.TileRowStarts = []int{}
		startSB = 0
		tileRows := 0
		for startSB < sbRows {
			fh.TileRowStarts = append(fh.TileRowStarts, startSB*sbMISize)
			maxHeight := sbRows - startSB
			if maxHeight > maxTileHeightSB {
				maxHeight = maxTileHeightSB
			}
			heightSB, err := r.ReadNs(maxHeight)
			if err != nil {
				return wrapErr("tile_height_in_sbs_minus_1", err)
			}
			startSB += int(heightSB) + 1
			tileRows++
		}
		fh.TileRowStarts = append(fh.TileRowStarts, miRows)
		fh.TileRows = tileRows
		fh.TileRowsLog2 = tileLog2Calc(1, tileRows)
	}

	numTiles := fh.TileCols * fh.TileRows

	// context_update_tile_id and tile_size_bytes.
	if numTiles > 1 {
		tileBits := fh.TileColsLog2 + fh.TileRowsLog2
		ctxID, err := r.ReadUint32(tileBits)
		if err != nil {
			return wrapErr("context_update_tile_id", err)
		}
		fh.ContextUpdateTileID = int(ctxID)

		tsb, err := r.ReadUint8(2)
		if err != nil {
			return wrapErr("tile_size_bytes_minus_1", err)
		}
		fh.TileSizeBytes = int(tsb) + 1
	} else {
		fh.ContextUpdateTileID = 0
		fh.TileSizeBytes = 0
	}

	return nil
}

// parseQuantizationParams parses quantization_params(). AV1 spec Section 5.9.12.
func parseQuantizationParams(r *bitstream.Reader, fh *DecodedFrameHeader, sh *obu.SequenceHeader) error {
	// base_q_idx f(8)
	bqi, err := r.ReadUint8(8)
	if err != nil {
		return wrapErr("base_q_idx", err)
	}
	fh.BaseQIndex = bqi

	// DeltaQYDc
	dq, err := readDeltaQ(r)
	if err != nil {
		return wrapErr("DeltaQYDc", err)
	}
	fh.DeltaQYDC = dq

	if fh.NumPlanes > 1 {
		diffUVDelta := false
		if sh.ColorConfig.SeparateUVDeltaQ {
			diffUVDelta, err = r.ReadFlag()
			if err != nil {
				return wrapErr("diff_uv_delta", err)
			}
		}

		// DeltaQUDc
		fh.DeltaQUDC, err = readDeltaQ(r)
		if err != nil {
			return wrapErr("DeltaQUDc", err)
		}

		// DeltaQUAc
		fh.DeltaQUAC, err = readDeltaQ(r)
		if err != nil {
			return wrapErr("DeltaQUAc", err)
		}

		if diffUVDelta {
			// DeltaQVDc
			fh.DeltaQVDC, err = readDeltaQ(r)
			if err != nil {
				return wrapErr("DeltaQVDc", err)
			}

			// DeltaQVAc
			fh.DeltaQVAC, err = readDeltaQ(r)
			if err != nil {
				return wrapErr("DeltaQVAc", err)
			}
		} else {
			fh.DeltaQVDC = fh.DeltaQUDC
			fh.DeltaQVAC = fh.DeltaQUAC
		}
	}

	// using_qmatrix f(1)
	fh.UsingQMatrix, err = r.ReadFlag()
	if err != nil {
		return wrapErr("using_qmatrix", err)
	}

	if fh.UsingQMatrix {
		fh.QMY, err = r.ReadUint8(4)
		if err != nil {
			return wrapErr("qm_y", err)
		}
		fh.QMU, err = r.ReadUint8(4)
		if err != nil {
			return wrapErr("qm_u", err)
		}
		if sh.ColorConfig.SeparateUVDeltaQ {
			fh.QMV, err = r.ReadUint8(4)
			if err != nil {
				return wrapErr("qm_v", err)
			}
		} else {
			fh.QMV = fh.QMU
		}
	}

	return nil
}

// readDeltaQ reads a read_delta_q() element. AV1 spec Section 5.9.16.
// Returns the delta value (0 if not coded).
func readDeltaQ(r *bitstream.Reader) (int, error) {
	deltaCoded, err := r.ReadFlag()
	if err != nil {
		return 0, err
	}
	if !deltaCoded {
		return 0, nil
	}
	val, err := r.ReadSu(7)
	if err != nil {
		return 0, err
	}
	return int(val), nil
}

// parseSegmentationParams parses segmentation_params(). AV1 spec Section 5.9.14.
func parseSegmentationParams(r *bitstream.Reader, fh *DecodedFrameHeader) error {
	var err error
	fh.SegmentationEnabled, err = r.ReadFlag()
	if err != nil {
		return wrapErr("segmentation_enabled", err)
	}

	if !fh.SegmentationEnabled {
		return nil
	}

	// segmentation_update_map / segmentation_update_data
	if fh.PrimaryRefFrame == primaryRefNone {
		// For key frames / intra-only, update is implicit.
		fh.SegmentationUpdateMap = true
		fh.SegmentationTemporalUpdate = false
		fh.SegmentationUpdateData = true
	} else {
		fh.SegmentationUpdateMap, err = r.ReadFlag()
		if err != nil {
			return wrapErr("segmentation_update_map", err)
		}
		if fh.SegmentationUpdateMap {
			fh.SegmentationTemporalUpdate, err = r.ReadFlag()
			if err != nil {
				return wrapErr("segmentation_temporal_update", err)
			}
		}
		fh.SegmentationUpdateData, err = r.ReadFlag()
		if err != nil {
			return wrapErr("segmentation_update_data", err)
		}
	}

	if fh.SegmentationUpdateData {
		// Segmentation feature data bits and sign per feature ID.
		// AV1 spec Table 3 (Segmentation_Feature_Bits / Segmentation_Feature_Signed).
		segFeatureBits := [segLvlMax]int{8, 6, 6, 6, 6, 3, 0, 0}
		segFeatureSigned := [segLvlMax]bool{true, true, true, true, true, false, false, false}

		for seg := 0; seg < maxSegments; seg++ {
			for feat := 0; feat < segLvlMax; feat++ {
				featureEnabled, err := r.ReadFlag()
				if err != nil {
					return wrapErr("feature_enabled", err)
				}
				fh.SegFeatureActive[seg][feat] = featureEnabled

				if featureEnabled {
					bitsToRead := segFeatureBits[feat]
					if bitsToRead > 0 {
						if segFeatureSigned[feat] {
							val, err := r.ReadSu(bitsToRead + 1)
							if err != nil {
								return wrapErr("feature_value", err)
							}
							fh.SegFeatureData[seg][feat] = int(val)
						} else {
							val, err := r.ReadBits(bitsToRead)
							if err != nil {
								return wrapErr("feature_value", err)
							}
							fh.SegFeatureData[seg][feat] = int(val)
						}
					}
				}
			}
		}
	}

	return nil
}

// parseDeltaQParams parses delta_q_params(). AV1 spec Section 5.9.17.
func parseDeltaQParams(r *bitstream.Reader, fh *DecodedFrameHeader) error {
	fh.DeltaQPresent = false
	fh.DeltaQRes = 0

	if fh.BaseQIndex > 0 {
		var err error
		fh.DeltaQPresent, err = r.ReadFlag()
		if err != nil {
			return wrapErr("delta_q_present", err)
		}
	}

	if fh.DeltaQPresent {
		var err error
		fh.DeltaQRes, err = r.ReadUint8(2)
		if err != nil {
			return wrapErr("delta_q_res", err)
		}
	}

	return nil
}

// parseDeltaLFParams parses delta_lf_params(). AV1 spec Section 5.9.18.
func parseDeltaLFParams(r *bitstream.Reader, fh *DecodedFrameHeader) error {
	fh.DeltaLFPresent = false
	fh.DeltaLFRes = 0
	fh.DeltaLFMulti = false

	if fh.DeltaQPresent {
		if !fh.AllowIntraBC {
			var err error
			fh.DeltaLFPresent, err = r.ReadFlag()
			if err != nil {
				return wrapErr("delta_lf_present", err)
			}
		}
		if fh.DeltaLFPresent {
			var err error
			fh.DeltaLFRes, err = r.ReadUint8(2)
			if err != nil {
				return wrapErr("delta_lf_res", err)
			}
			fh.DeltaLFMulti, err = r.ReadFlag()
			if err != nil {
				return wrapErr("delta_lf_multi", err)
			}
		}
	}

	return nil
}

// parseLoopFilterParams parses loop_filter_params(). AV1 spec Section 5.9.11.
func parseLoopFilterParams(r *bitstream.Reader, fh *DecodedFrameHeader, numPlanes int) error {
	var err error

	// loop_filter_level[0] f(6)
	fh.LoopFilterLevel[0], err = r.ReadUint8(6)
	if err != nil {
		return wrapErr("loop_filter_level[0]", err)
	}

	// loop_filter_level[1] f(6)
	fh.LoopFilterLevel[1], err = r.ReadUint8(6)
	if err != nil {
		return wrapErr("loop_filter_level[1]", err)
	}

	if numPlanes > 1 && (fh.LoopFilterLevel[0] != 0 || fh.LoopFilterLevel[1] != 0) {
		// loop_filter_level[2] f(6)
		fh.LoopFilterLevel[2], err = r.ReadUint8(6)
		if err != nil {
			return wrapErr("loop_filter_level[2]", err)
		}

		// loop_filter_level[3] f(6)
		fh.LoopFilterLevel[3], err = r.ReadUint8(6)
		if err != nil {
			return wrapErr("loop_filter_level[3]", err)
		}
	}

	// loop_filter_sharpness f(3)
	fh.LoopFilterSharpness, err = r.ReadUint8(3)
	if err != nil {
		return wrapErr("loop_filter_sharpness", err)
	}

	// AV1 spec setup_past_independence(): default loop filter ref deltas.
	// These are the defaults for keyframes and are used when deltas are
	// not explicitly signaled. Per spec Section 6.8.2 / dav1d setup_past_independence.
	fh.RefDeltas = [obu.NumRefFrames]int8{1, 0, 0, 0, -1, 0, -1, -1}
	fh.ModeDeltas = [2]int8{0, 0}

	// loop_filter_delta_enabled f(1)
	fh.LoopFilterDeltaEnabled, err = r.ReadFlag()
	if err != nil {
		return wrapErr("loop_filter_delta_enabled", err)
	}

	if fh.LoopFilterDeltaEnabled {
		// loop_filter_delta_update f(1)
		fh.LoopFilterDeltaUpdate, err = r.ReadFlag()
		if err != nil {
			return wrapErr("loop_filter_delta_update", err)
		}

		if fh.LoopFilterDeltaUpdate {
			// Reference frame loop filter deltas.
			for i := 0; i < obu.NumRefFrames; i++ {
				updateRefDelta, err := r.ReadFlag()
				if err != nil {
					return wrapErr("update_ref_delta", err)
				}
				if updateRefDelta {
					val, err := r.ReadSu(7)
					if err != nil {
						return wrapErr("ref_delta", err)
					}
					fh.RefDeltas[i] = int8(val)
				}
			}

			// Mode loop filter deltas.
			for i := 0; i < 2; i++ {
				updateModeDelta, err := r.ReadFlag()
				if err != nil {
					return wrapErr("update_mode_delta", err)
				}
				if updateModeDelta {
					val, err := r.ReadSu(7)
					if err != nil {
						return wrapErr("mode_delta", err)
					}
					fh.ModeDeltas[i] = int8(val)
				}
			}
		}
	}

	return nil
}

// parseCDEFParams parses cdef_params(). AV1 spec Section 5.9.19.
func parseCDEFParams(r *bitstream.Reader, fh *DecodedFrameHeader, numPlanes int) error {
	// cdef_damping_minus_3 f(2)
	dampingMinus3, err := r.ReadUint8(2)
	if err != nil {
		return wrapErr("cdef_damping_minus_3", err)
	}
	fh.CDEFDamping = int(dampingMinus3) + 3

	// cdef_bits f(2)
	cdefBits, err := r.ReadUint8(2)
	if err != nil {
		return wrapErr("cdef_bits", err)
	}
	fh.CDEFBits = int(cdefBits)

	numFilters := 1 << fh.CDEFBits
	for i := 0; i < numFilters; i++ {
		// cdef_y_pri_strength f(4)
		yPri, err := r.ReadUint8(4)
		if err != nil {
			return wrapErr("cdef_y_pri_strength", err)
		}
		fh.CDEFYPriStrength[i] = int(yPri)

		// cdef_y_sec_strength f(2)
		ySec, err := r.ReadUint8(2)
		if err != nil {
			return wrapErr("cdef_y_sec_strength", err)
		}
		secY := int(ySec)
		if secY == 3 {
			secY = 4
		}
		fh.CDEFYSecStrength[i] = secY

		if numPlanes > 1 {
			// cdef_uv_pri_strength f(4)
			uvPri, err := r.ReadUint8(4)
			if err != nil {
				return wrapErr("cdef_uv_pri_strength", err)
			}
			fh.CDEFUVPriStrength[i] = int(uvPri)

			// cdef_uv_sec_strength f(2)
			uvSec, err := r.ReadUint8(2)
			if err != nil {
				return wrapErr("cdef_uv_sec_strength", err)
			}
			secUV := int(uvSec)
			if secUV == 3 {
				secUV = 4
			}
			fh.CDEFUVSecStrength[i] = secUV
		}
	}

	return nil
}

// parseLRParams parses lr_params(). AV1 spec Section 5.9.20.
func parseLRParams(r *bitstream.Reader, fh *DecodedFrameHeader, sh *obu.SequenceHeader, numPlanes int) error {
	usesLR := false
	usesChromaLR := false

	for i := 0; i < numPlanes; i++ {
		// lr_type f(2)
		lrType, err := r.ReadUint8(2)
		if err != nil {
			return wrapErr("lr_type", err)
		}
		fh.LRType[i] = frameRestoreTypeRemap[lrType]
		if fh.LRType[i] != FrameRestoreNone {
			usesLR = true
			if i > 0 {
				usesChromaLR = true
			}
		}
	}

	if usesLR {
		if sh.Use128x128Superblock {
			// lr_unit_shift: 1 bit for extra shift.
			// dav1d: unit_size[0] = 6 + sb128 (=7), then +1 if this bit is set.
			// So LRUnitShift is 0 or 1 (the additional shift beyond base).
			shift, err := r.ReadFlag()
			if err != nil {
				return wrapErr("lr_unit_shift", err)
			}
			if shift {
				fh.LRUnitShift = 1
			} else {
				fh.LRUnitShift = 0
			}
		} else {
			// lr_unit_shift f(1)
			shift, err := r.ReadFlag()
			if err != nil {
				return wrapErr("lr_unit_shift", err)
			}
			if shift {
				// lr_unit_extra_shift f(1)
				extraShift, err := r.ReadFlag()
				if err != nil {
					return wrapErr("lr_unit_extra_shift", err)
				}
				if extraShift {
					fh.LRUnitShift = 2
				} else {
					fh.LRUnitShift = 1
				}
			} else {
				fh.LRUnitShift = 0
			}
		}

		if sh.ColorConfig.SubsamplingX == 1 && sh.ColorConfig.SubsamplingY == 1 && usesChromaLR {
			var err error
			fh.LRUVShift, err = r.ReadFlag()
			if err != nil {
				return wrapErr("lr_uv_shift", err)
			}
		}
	}

	return nil
}

// AV1 global motion constants. AV1 spec Section 5.9.25.
const (
	warpedModelPrecBits   = 16
	gmAbsAlphaBits        = 12
	gmAlphaPrecBits       = 15
	gmAbsTransBits        = 12
	gmTransPrecBits       = 6
	gmAbsTransOnlyBits    = 9
	gmTransOnlyPrecBits   = 3
)

// parseGlobalMotionParams parses global_motion_params(). AV1 spec Section 5.9.24.
// Reads the is_global flag for each reference frame and parses global
// motion parameters using decode_signed_subexp_with_ref.
//
// PrevGmParams is derived from the primary reference frame's saved GM params.
// For primary_ref_frame == PRIMARY_REF_NONE, PrevGmParams = identity.
func parseGlobalMotionParamsWithPrev(r *bitstream.Reader, fh *DecodedFrameHeader, prevGmParams [obu.RefsPerFrame][6]int32) error {
	for ref := 0; ref < obu.RefsPerFrame; ref++ {
		// Initialize to identity.
		fh.GmParams[ref] = [6]int32{0, 0, 1 << warpedModelPrecBits, 0, 0, 1 << warpedModelPrecBits}

		isGlobal, err := r.ReadFlag()
		if err != nil {
			return wrapErr("is_global", err)
		}

		if !isGlobal {
			fh.GmType[ref] = 0 // IDENTITY
			continue
		}

		isROTZoom, err := r.ReadFlag()
		if err != nil {
			return wrapErr("is_rot_zoom", err)
		}

		if isROTZoom {
			fh.GmType[ref] = 2 // ROTZOOM
		} else {
			isTrans, err := r.ReadFlag()
			if err != nil {
				return wrapErr("is_translation", err)
			}
			if isTrans {
				fh.GmType[ref] = 1 // TRANSLATION
			} else {
				fh.GmType[ref] = 3 // AFFINE
			}
		}

		// Parse global motion parameters per type.
		// AV1 spec Section 5.9.24: parameters are read in specific index order.
		// Matrix params (indices 2,3 and optionally 4,5) are read before
		// translation params (indices 0,1).
		gmType := fh.GmType[ref]
		if gmType >= 2 { // ROTZOOM or AFFINE
			// Read matrix parameters: indices 2, 3.
			for _, idx := range []int{2, 3} {
				val, err := readGlobalParam(r, gmType, idx, prevGmParams[ref][idx], fh.AllowHighPrecisionMV)
				if err != nil {
					return fmt.Errorf("decoder: global_motion_param[%d][%d]: %w", ref, idx, err)
				}
				fh.GmParams[ref][idx] = val
			}
			if gmType == 3 { // AFFINE: also read indices 4, 5.
				for _, idx := range []int{4, 5} {
					val, err := readGlobalParam(r, gmType, idx, prevGmParams[ref][idx], fh.AllowHighPrecisionMV)
					if err != nil {
						return fmt.Errorf("decoder: global_motion_param[%d][%d]: %w", ref, idx, err)
					}
					fh.GmParams[ref][idx] = val
				}
			} else {
				// ROTZOOM: derive indices 4,5 from 2,3.
				fh.GmParams[ref][4] = -fh.GmParams[ref][3]
				fh.GmParams[ref][5] = fh.GmParams[ref][2]
			}
		}
		if gmType >= 1 { // TRANSLATION, ROTZOOM, or AFFINE
			// Read translation parameters: indices 0, 1.
			for _, idx := range []int{0, 1} {
				val, err := readGlobalParam(r, gmType, idx, prevGmParams[ref][idx], fh.AllowHighPrecisionMV)
				if err != nil {
					return fmt.Errorf("decoder: global_motion_param[%d][%d]: %w", ref, idx, err)
				}
				fh.GmParams[ref][idx] = val
			}
		}
	}

	return nil
}

// readGlobalParam reads one global motion parameter per AV1 spec Section 5.9.25.
// idx: parameter index (0-5), gmType: global motion type, prevParam: PrevGmParams[ref][idx],
// allowHP: allow_high_precision_mv flag.
func readGlobalParam(r *bitstream.Reader, gmType int, idx int, prevParam int32, allowHP bool) (int32, error) {
	var absBits, precBits int

	if idx < 2 {
		// Translation parameters (idx 0, 1).
		if gmType == 1 { // TRANSLATION only
			absBits = gmAbsTransOnlyBits
			precBits = gmTransOnlyPrecBits
			if !allowHP {
				absBits--
				precBits--
			}
		} else {
			absBits = gmAbsTransBits
			precBits = gmTransPrecBits
		}
	} else {
		// Matrix parameters (idx 2-5).
		absBits = gmAbsAlphaBits
		precBits = gmAlphaPrecBits
	}

	precDiff := warpedModelPrecBits - precBits
	round := int32(0)
	sub := int32(0)
	if idx%3 == 2 {
		round = 1 << warpedModelPrecBits
		sub = 1 << precBits
	}

	mx := 1 << absBits
	refVal := (prevParam >> uint(precDiff)) - sub

	val, err := decodeSignedSubexpWithRef(r, refVal, -int32(mx), int32(mx)+1)
	if err != nil {
		return 0, err
	}

	return (val << uint(precDiff)) + round, nil
}

// decodeSignedSubexpWithRef decodes a signed subexponential value with a
// reference center. AV1 spec Section 5.9.26 (decode_signed_subexp_with_ref).
// Matches dav1d getbits.c get_bits_subexp_u / dav1d_get_bits_subexp.
func decodeSignedSubexpWithRef(r *bitstream.Reader, ref, low, high int32) (int32, error) {
	v, err := decodeUnsignedSubexp(r, int(high-low))
	if err != nil {
		return 0, err
	}
	// Map signed ref to unsigned: ref_u = ref - low (in range [0, high-low]).
	// dav1d: ref_u * 2 <= n_u  <==>  (ref-low)*2 <= (high-low-1)
	//        <==>  ref*2 <= high+low-1  <==>  (ref<<1) <= high+low-1
	// For GM params where high+low=1, this is ref <= 0.
	refU := int(ref - low)
	nU := int(high - low - 1)
	if refU*2 <= nU {
		return int32(inverseRecenter(refU, v)) + low, nil
	}
	return int32(nU-inverseRecenter(nU-refU, v)) + low, nil
}

// decodeUnsignedSubexp decodes an unsigned subexponential value.
// AV1 spec Section 5.9.28 (decode_subexp).
func decodeUnsignedSubexp(r *bitstream.Reader, numSyms int) (int, error) {
	i := 0
	mk := 0
	k := 3
	for {
		b2 := k
		if i > 0 {
			b2 = k + i - 1
		}
		a := 1 << b2
		if numSyms <= mk+3*a {
			v, err := r.ReadNs(numSyms - mk)
			if err != nil {
				return 0, err
			}
			return mk + int(v), nil
		}
		more, err := r.ReadFlag()
		if err != nil {
			return 0, err
		}
		if more {
			mk += a
			i++
		} else {
			v, err := r.ReadBits(b2)
			if err != nil {
				return 0, err
			}
			return mk + int(v), nil
		}
	}
}

// inverseRecenter maps an unsigned subexp value back to a signed value
// centered around r. AV1 spec Section 5.9.27.
func inverseRecenter(r, v int) int {
	if v > 2*r {
		return v
	}
	if v&1 != 0 {
		return r - ((v + 1) >> 1)
	}
	return r + (v >> 1)
}

// --- Utility functions ---

// isCodedLossless returns true if the frame is coded lossless.
// AV1 spec Section 5.9.2: CodedLossless = (base_q_idx == 0 && all delta_q == 0).
func isCodedLossless(fh *DecodedFrameHeader) bool {
	return fh.BaseQIndex == 0 &&
		fh.DeltaQYDC == 0 &&
		fh.DeltaQUDC == 0 && fh.DeltaQUAC == 0 &&
		fh.DeltaQVDC == 0 && fh.DeltaQVAC == 0
}

// tileLog2Calc computes the smallest k such that (1 << k) * blkSize >= target.
// Used in tile_info() to compute min/max tile log2 values.
// AV1 spec Section 5.9.15.
func tileLog2Calc(blkSize, target int) int {
	if blkSize <= 0 {
		return 0
	}
	k := 0
	for (1<<k)*blkSize < target {
		k++
	}
	return k
}

// getRelativeDist computes the signed distance between two order hints,
// accounting for wrap-around. AV1 spec Section 7.1 (get_relative_dist).
func getRelativeDist(a, b uint32, orderHintBits int) int {
	if orderHintBits == 0 {
		return 0
	}
	diff := int(a) - int(b)
	m := 1 << (orderHintBits - 1)
	diff = (diff & (m - 1)) - (diff & m)
	return diff
}

// setFrameRefs derives all 7 reference frame indices from last_frame_idx
// and gold_frame_idx when frame_refs_short_signaling is enabled.
// AV1 spec Section 7.8 (set_frame_refs).
func setFrameRefs(fh *DecodedFrameHeader, sh *obu.SequenceHeader, refOrderHints [8]uint32, lastFrameIdx, goldFrameIdx uint8) {
	orderHintBits := int(sh.OrderHintBitsMinus1) + 1

	// Initialize all ref indices to -1 (unset).
	var refFrameIdx [obu.RefsPerFrame]int
	for i := range refFrameIdx {
		refFrameIdx[i] = -1
	}

	// LAST_FRAME = index 0, GOLDEN_FRAME = index 3.
	refFrameIdx[0] = int(lastFrameIdx)
	refFrameIdx[3] = int(goldFrameIdx)

	var usedFrame [obu.NumRefFrames]bool
	usedFrame[lastFrameIdx] = true
	usedFrame[goldFrameIdx] = true

	// Compute shifted order hints for all 8 reference buffer slots.
	curFrameHint := 1 << (orderHintBits - 1)
	var shiftedOrderHints [obu.NumRefFrames]int
	for i := 0; i < obu.NumRefFrames; i++ {
		shiftedOrderHints[i] = curFrameHint + getRelativeDist(refOrderHints[i], fh.OrderHint, orderHintBits)
	}

	// --- ALTREF_FRAME (index 6): latest backward reference ---
	ref := -1
	latestOrderHint := 0
	for i := 0; i < obu.NumRefFrames; i++ {
		hint := shiftedOrderHints[i]
		if !usedFrame[i] && hint >= curFrameHint &&
			(ref < 0 || hint >= latestOrderHint) {
			ref = i
			latestOrderHint = hint
		}
	}
	if ref >= 0 {
		refFrameIdx[6] = ref // ALTREF_FRAME
		usedFrame[ref] = true
	}

	// --- BWDREF_FRAME (index 4): earliest backward reference ---
	ref = -1
	earliestOrderHint := 0
	for i := 0; i < obu.NumRefFrames; i++ {
		hint := shiftedOrderHints[i]
		if !usedFrame[i] && hint >= curFrameHint &&
			(ref < 0 || hint < earliestOrderHint) {
			ref = i
			earliestOrderHint = hint
		}
	}
	if ref >= 0 {
		refFrameIdx[4] = ref // BWDREF_FRAME
		usedFrame[ref] = true
	}

	// --- ALTREF2_FRAME (index 5): next earliest backward reference ---
	ref = -1
	earliestOrderHint = 0
	for i := 0; i < obu.NumRefFrames; i++ {
		hint := shiftedOrderHints[i]
		if !usedFrame[i] && hint >= curFrameHint &&
			(ref < 0 || hint < earliestOrderHint) {
			ref = i
			earliestOrderHint = hint
		}
	}
	if ref >= 0 {
		refFrameIdx[5] = ref // ALTREF2_FRAME
		usedFrame[ref] = true
	}

	// --- Fill remaining forward references ---
	// Order: LAST2 (1), LAST3 (2), BWDREF (4), ALTREF2 (5), ALTREF (6).
	refFrameList := [5]int{1, 2, 4, 5, 6}
	for _, refFrame := range refFrameList {
		if refFrameIdx[refFrame] >= 0 {
			continue
		}
		ref = -1
		latestOrderHint = 0
		for j := 0; j < obu.NumRefFrames; j++ {
			hint := shiftedOrderHints[j]
			if !usedFrame[j] && hint < curFrameHint &&
				(ref < 0 || hint >= latestOrderHint) {
				ref = j
				latestOrderHint = hint
			}
		}
		if ref >= 0 {
			refFrameIdx[refFrame] = ref
			usedFrame[ref] = true
		}
	}

	// --- Fill any still-unset slots with the earliest available reference ---
	ref = -1
	earliestOrderHint = 0
	for i := 0; i < obu.NumRefFrames; i++ {
		hint := shiftedOrderHints[i]
		if ref < 0 || hint < earliestOrderHint {
			ref = i
			earliestOrderHint = hint
		}
	}
	for i := 0; i < obu.RefsPerFrame; i++ {
		if refFrameIdx[i] < 0 {
			refFrameIdx[i] = ref
		}
	}

	// Write results to the frame header.
	for i := 0; i < obu.RefsPerFrame; i++ {
		fh.RefFrameIdx[i] = uint8(refFrameIdx[i])
	}
}

// wrapErr wraps an error with a field name for context.
func wrapErr(field string, err error) error {
	return fmt.Errorf("decoder: %s: %w", field, err)
}
