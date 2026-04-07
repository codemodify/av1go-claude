// Package obu provides types and serialization for AV1 Open Bitstream Units.
//
// An OBU is the fundamental framing unit of an AV1 bitstream. Each OBU has a
// 1-2 byte header followed by an optional size field and payload.
// See AV1 spec Section 5.3 ("OBU Header Syntax").
package obu

// Type identifies the type of an OBU, as defined in AV1 spec Table 6.2.
type Type uint8

const (
	// TypeSequenceHeader (1) contains sequence-level coding parameters.
	TypeSequenceHeader Type = 1
	// TypeTemporalDelimiter (2) signals a temporal unit boundary.
	TypeTemporalDelimiter Type = 2
	// TypeFrameHeader (3) contains frame-level coding parameters.
	TypeFrameHeader Type = 3
	// TypeTileGroup (4) contains tile data.
	TypeTileGroup Type = 4
	// TypeMetadata (5) contains metadata.
	TypeMetadata Type = 5
	// TypeFrame (6) combines frame header and a single tile group.
	TypeFrame Type = 6
	// TypeRedundantFrameHeader (7) is a redundant copy of the frame header.
	TypeRedundantFrameHeader Type = 7
	// TypeTileList (8) contains a list of tiles for large-scale tile decoding.
	TypeTileList Type = 8
	// TypePadding (15) is ignored by decoders.
	TypePadding Type = 15
)

// String returns the spec name of the OBU type.
func (t Type) String() string {
	switch t {
	case TypeSequenceHeader:
		return "OBU_SEQUENCE_HEADER"
	case TypeTemporalDelimiter:
		return "OBU_TEMPORAL_DELIMITER"
	case TypeFrameHeader:
		return "OBU_FRAME_HEADER"
	case TypeTileGroup:
		return "OBU_TILE_GROUP"
	case TypeMetadata:
		return "OBU_METADATA"
	case TypeFrame:
		return "OBU_FRAME"
	case TypeRedundantFrameHeader:
		return "OBU_REDUNDANT_FRAME_HEADER"
	case TypeTileList:
		return "OBU_TILE_LIST"
	case TypePadding:
		return "OBU_PADDING"
	default:
		return "OBU_UNKNOWN"
	}
}

// ExtensionHeader contains the fields of an OBU extension header.
// Present when obu_extension_flag is set in the OBU header.
// AV1 spec Section 5.3.3.
type ExtensionHeader struct {
	TemporalID uint8 // temporal_id: 3 bits [0,7]
	SpatialID  uint8 // spatial_id: 2 bits [0,3]
}

// Header represents a parsed or to-be-written OBU header.
type Header struct {
	Type         Type             // obu_type: 4 bits
	HasExtension bool             // obu_extension_flag
	HasSize      bool             // obu_has_size_flag
	Extension    *ExtensionHeader // present if HasExtension is true
}

// ColorPrimaries as defined in AV1 spec Section 6.4.2 (Table 6.3).
type ColorPrimaries uint8

const (
	ColorPrimariesBT709        ColorPrimaries = 1
	ColorPrimariesUnspecified   ColorPrimaries = 2
	ColorPrimariesBT470M       ColorPrimaries = 4
	ColorPrimariesBT470BG      ColorPrimaries = 5
	ColorPrimariesBT601        ColorPrimaries = 6
	ColorPrimariesSMPTE240     ColorPrimaries = 7
	ColorPrimariesGenericFilm  ColorPrimaries = 8
	ColorPrimariesBT2020       ColorPrimaries = 9
	ColorPrimariesXYZ          ColorPrimaries = 10
	ColorPrimariesSMPTE431     ColorPrimaries = 11
	ColorPrimariesSMPTE432     ColorPrimaries = 12
	ColorPrimariesEBU3213      ColorPrimaries = 22
)

// TransferCharacteristics as defined in AV1 spec Section 6.4.2 (Table 6.4).
type TransferCharacteristics uint8

const (
	TransferCharacteristicsBT709        TransferCharacteristics = 1
	TransferCharacteristicsUnspecified   TransferCharacteristics = 2
	TransferCharacteristicsBT470M       TransferCharacteristics = 4
	TransferCharacteristicsBT470BG      TransferCharacteristics = 5
	TransferCharacteristicsBT601        TransferCharacteristics = 6
	TransferCharacteristicsSMPTE240     TransferCharacteristics = 7
	TransferCharacteristicsLinear       TransferCharacteristics = 8
	TransferCharacteristicsLog100       TransferCharacteristics = 9
	TransferCharacteristicsLog100Sqrt10 TransferCharacteristics = 10
	TransferCharacteristicsIEC61966     TransferCharacteristics = 11
	TransferCharacteristicsBT1361       TransferCharacteristics = 12
	TransferCharacteristicsSRGB         TransferCharacteristics = 13
	TransferCharacteristicsBT2020_10    TransferCharacteristics = 14
	TransferCharacteristicsBT2020_12    TransferCharacteristics = 15
	TransferCharacteristicsSMPTE2084    TransferCharacteristics = 16 // PQ / HDR10
	TransferCharacteristicsSMPTE428     TransferCharacteristics = 17
	TransferCharacteristicsHLG          TransferCharacteristics = 18
)

// MatrixCoefficients as defined in AV1 spec Section 6.4.2 (Table 6.5).
type MatrixCoefficients uint8

const (
	MatrixCoefficientsIdentity    MatrixCoefficients = 0
	MatrixCoefficientsBT709      MatrixCoefficients = 1
	MatrixCoefficientsUnspecified MatrixCoefficients = 2
	MatrixCoefficientsFCC        MatrixCoefficients = 4
	MatrixCoefficientsBT470BG    MatrixCoefficients = 5
	MatrixCoefficientsBT601      MatrixCoefficients = 6
	MatrixCoefficientsSMPTE240   MatrixCoefficients = 7
	MatrixCoefficientsYCgCo      MatrixCoefficients = 8
	MatrixCoefficientsBT2020NCL  MatrixCoefficients = 9
	MatrixCoefficientsBT2020CL   MatrixCoefficients = 10
	MatrixCoefficientsSMPTE2085  MatrixCoefficients = 11
	MatrixCoefficientsChromatNCL MatrixCoefficients = 12
	MatrixCoefficientsChromatCL  MatrixCoefficients = 13
	MatrixCoefficientsICTCP      MatrixCoefficients = 14
)

// ChromaSamplePosition as defined in AV1 spec Section 6.4.2.
type ChromaSamplePosition uint8

const (
	ChromaSamplePositionUnknown   ChromaSamplePosition = 0
	ChromaSamplePositionVertical  ChromaSamplePosition = 1 // MPEG-2 style: co-located vertically
	ChromaSamplePositionColocated ChromaSamplePosition = 2 // MPEG-4/H.264 style: co-located
)

// ColorConfig represents the color_config() syntax element.
// AV1 spec Section 5.5.2.
type ColorConfig struct {
	HighBitDepth         bool
	TwelveBit            bool
	MonoChrome           bool
	ColorDescriptionPresent bool
	ColorPrimaries       ColorPrimaries
	TransferCharacteristics TransferCharacteristics
	MatrixCoefficients   MatrixCoefficients
	ColorRange           bool   // false=studio/limited, true=full
	SubsamplingX         uint8  // 0 or 1
	SubsamplingY         uint8  // 0 or 1
	ChromaSamplePosition ChromaSamplePosition
	SeparateUVDeltaQ     bool
}

// BitDepth returns the bit depth implied by the color config flags.
func (cc *ColorConfig) BitDepth() int {
	if !cc.HighBitDepth {
		return 8
	}
	if cc.TwelveBit {
		return 12
	}
	return 10
}

// TimingInfo represents the timing_info() syntax element.
// AV1 spec Section 5.5.3.
type TimingInfo struct {
	NumUnitsInDisplayTick uint32 // num_units_in_display_tick
	TimeScale             uint32 // time_scale
	EqualPictureInterval  bool   // equal_picture_interval
	NumTicksPerPicture    uint32 // num_ticks_per_picture_minus_1 + 1 (only if EqualPictureInterval)
}

// DecoderModelInfo represents decoder_model_info() syntax.
// AV1 spec Section 5.5.4.
type DecoderModelInfo struct {
	BufferDelayLengthMinus1          uint8  // 5 bits
	NumUnitsInDecodingTick           uint32 // 32 bits
	BufferRemovalTimeLengthMinus1    uint8  // 5 bits
	FramePresentationTimeLengthMinus1 uint8 // 5 bits
}

// OperatingParametersInfo represents operating_parameters_info() per operating point.
// AV1 spec Section 5.5.5.
type OperatingParametersInfo struct {
	DecoderBufferDelay uint32 // n bits, where n = buffer_delay_length
	EncoderBufferDelay uint32 // n bits
	LowDelayModeFlag   bool
}

// OperatingPoint represents a single operating point in the sequence header.
type OperatingPoint struct {
	Idc                    uint16 // operating_point_idc: 12 bits
	SeqLevelIdx            uint8  // seq_level_idx: 5 bits
	SeqTier                uint8  // seq_tier: 1 bit (only if level > 7)
	DecoderModelPresentForOP bool
	OperatingParametersInfo *OperatingParametersInfo // present if decoder model is active
	InitialDisplayDelayPresent bool
	InitialDisplayDelay        uint8 // initial_display_delay_minus_1 + 1
}

// SequenceHeader represents the sequence_header_obu() syntax element.
// AV1 spec Section 5.5.1.
type SequenceHeader struct {
	SeqProfile                  uint8 // seq_profile: 3 values (0=Main, 1=High, 2=Professional)
	StillPicture                bool
	ReducedStillPictureHeader   bool
	TimingInfoPresent           bool
	TimingInfo                  *TimingInfo
	DecoderModelInfoPresent     bool
	DecoderModelInfo            *DecoderModelInfo
	InitialDisplayDelayPresent  bool
	OperatingPointsCntMinus1    uint8 // 0-31
	OperatingPoints             []OperatingPoint

	FrameWidthBitsMinus1        uint8  // 4 bits -> max frame width bits
	FrameHeightBitsMinus1       uint8  // 4 bits -> max frame height bits
	MaxFrameWidthMinus1         uint32 // n+1 bits
	MaxFrameHeightMinus1        uint32 // n+1 bits

	FrameIDNumbersPresent       bool
	DeltaFrameIDLengthMinus2    uint8 // 4 bits
	AdditionalFrameIDLengthMinus1 uint8 // 3 bits

	Use128x128Superblock        bool
	EnableFilterIntra           bool
	EnableIntraEdgeFilter       bool

	// The following are only present if ReducedStillPictureHeader is false.
	EnableInterIntraCompound    bool
	EnableMaskedCompound        bool
	EnableWarpedMotion          bool
	EnableDualFilter            bool
	EnableOrderHint             bool
	EnableJNTComp               bool   // joint compound (requires order hint)
	EnableRefFrameMVS           bool   // ref frame motion vectors (requires order hint)
	SeqForceScreenContentTools  uint8  // SELECT_SCREEN_CONTENT_TOOLS (2) or forced 0/1
	SeqForceIntegerMV           uint8  // SELECT_INTEGER_MV (2) or forced 0/1
	OrderHintBitsMinus1         uint8  // only if EnableOrderHint

	EnableSuperRes              bool
	EnableCDEF                  bool
	EnableRestoration           bool

	ColorConfig                 ColorConfig

	FilmGrainParamsPresent      bool
}

// MaxFrameWidth returns the maximum frame width.
func (sh *SequenceHeader) MaxFrameWidth() uint32 {
	return sh.MaxFrameWidthMinus1 + 1
}

// MaxFrameHeight returns the maximum frame height.
func (sh *SequenceHeader) MaxFrameHeight() uint32 {
	return sh.MaxFrameHeightMinus1 + 1
}

// Spec constants.
const (
	SelectScreenContentTools uint8 = 2 // SELECT_SCREEN_CONTENT_TOOLS
	SelectIntegerMV          uint8 = 2 // SELECT_INTEGER_MV

	MaxOperatingPoints = 32 // MAX_NUM_OPERATING_POINTS
)

// SeqProfile constants matching AV1 spec profiles.
const (
	ProfileMain         uint8 = 0
	ProfileHigh         uint8 = 1
	ProfileProfessional uint8 = 2
)

// FrameType represents the frame_type syntax element.
// AV1 spec Section 6.8.2.
type FrameType uint8

const (
	FrameTypeKey       FrameType = 0 // KEY_FRAME
	FrameTypeInter     FrameType = 1 // INTER_FRAME
	FrameTypeIntraOnly FrameType = 2 // INTRA_ONLY_FRAME
	FrameTypeSwitch    FrameType = 3 // SWITCH_FRAME
)

// String returns the spec name of the frame type.
func (ft FrameType) String() string {
	switch ft {
	case FrameTypeKey:
		return "KEY_FRAME"
	case FrameTypeInter:
		return "INTER_FRAME"
	case FrameTypeIntraOnly:
		return "INTRA_ONLY_FRAME"
	case FrameTypeSwitch:
		return "SWITCH_FRAME"
	default:
		return "UNKNOWN_FRAME_TYPE"
	}
}

// Reference frame constants.
const (
	NumRefFrames   = 8
	RefsPerFrame   = 7
	PrimaryRefNone = 7
)
