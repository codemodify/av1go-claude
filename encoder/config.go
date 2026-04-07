// Package encoder provides AV1 encoding functionality.
//
// This package implements a pure Go AV1 encoder. The current implementation
// provides configuration types and encoder scaffolding; frame-level encoding
// will be added incrementally.
package encoder

import (
	"av1go/obu"
	"fmt"
)

// Profile identifies the AV1 profile. Profiles constrain the set of
// coding tools and bit depths available.
// AV1 spec Section 6.4.1.
type Profile = uint8

const (
	// ProfileMain supports 8 and 10-bit 4:2:0.
	ProfileMain Profile = obu.ProfileMain
	// ProfileHigh supports 8 and 10-bit 4:4:4.
	ProfileHigh Profile = obu.ProfileHigh
	// ProfileProfessional supports 8, 10, and 12-bit in all chroma subsamplings.
	ProfileProfessional Profile = obu.ProfileProfessional
)

// RateControlMode specifies the rate control strategy.
type RateControlMode int

const (
	// RateControlCQ uses constant quality (CRF/CQ) mode.
	// The encoder targets a fixed quality level specified by QP.
	RateControlCQ RateControlMode = iota
	// RateControlCBR uses constant bitrate mode.
	RateControlCBR
	// RateControlVBR uses variable bitrate mode.
	RateControlVBR
)

// String returns the name of the rate control mode.
func (m RateControlMode) String() string {
	switch m {
	case RateControlCQ:
		return "CQ"
	case RateControlCBR:
		return "CBR"
	case RateControlVBR:
		return "VBR"
	default:
		return "Unknown"
	}
}

// FrameType is an alias for obu.FrameType, representing the AV1 frame_type syntax element.
type FrameType = obu.FrameType

// Frame type constants re-exported from obu package for convenience.
const (
	FrameTypeKey       = obu.FrameTypeKey
	FrameTypeInter     = obu.FrameTypeInter
	FrameTypeIntraOnly = obu.FrameTypeIntraOnly
	FrameTypeSwitch    = obu.FrameTypeSwitch
)

// PixelFormat describes the chroma subsampling of input frames.
type PixelFormat int

const (
	// PixelFormatYUV420 is 4:2:0 chroma subsampling (most common for video).
	PixelFormatYUV420 PixelFormat = iota
	// PixelFormatYUV422 is 4:2:2 chroma subsampling.
	PixelFormatYUV422
	// PixelFormatYUV444 is 4:4:4 (no chroma subsampling).
	PixelFormatYUV444
	// PixelFormatMonochrome is grayscale (no chroma planes).
	PixelFormatMonochrome
)

// String returns the name of the pixel format.
func (f PixelFormat) String() string {
	switch f {
	case PixelFormatYUV420:
		return "YUV420"
	case PixelFormatYUV422:
		return "YUV422"
	case PixelFormatYUV444:
		return "YUV444"
	case PixelFormatMonochrome:
		return "Monochrome"
	default:
		return "Unknown"
	}
}

// Config holds all encoder configuration parameters.
type Config struct {
	// Dimensions.
	Width  uint32 // Frame width in pixels.
	Height uint32 // Frame height in pixels.

	// Profile and bit depth.
	Profile  Profile // AV1 profile (Main, High, Professional).
	BitDepth int     // Bit depth: 8, 10, or 12.

	// Frame rate.
	FrameRateNum uint32 // Numerator of frame rate (e.g., 30000 for 29.97fps).
	FrameRateDen uint32 // Denominator of frame rate (e.g., 1001 for 29.97fps).

	// Pixel format.
	PixelFormat PixelFormat // Input chroma subsampling.

	// Rate control.
	RateControl    RateControlMode // Rate control strategy.
	QP             int             // Quantization parameter for CQ mode (0-63).
	TargetBitrate  int             // Target bitrate in kbps for CBR/VBR modes.

	// Encoding speed/quality tradeoff.
	// 0 = slowest/best quality, 6 = fastest.
	SpeedLevel int

	// Keyframe interval.
	KeyFrameInterval int  // Maximum distance between keyframes (0 = auto).
	ForceKeyFrame    bool // Force keyframe on first frame.

	// Threading.
	Threads    int // Number of encoding threads (0 = auto-detect).
	TileColumns int // log2 of tile columns (0-6).
	TileRows    int // log2 of tile rows (0-6).

	// Lookahead.
	LagInFrames int // Number of frames to buffer for lookahead (0 = low-latency).

	// AV1-specific coding tools.
	EnableCDEF        bool // Enable Constrained Directional Enhancement Filter.
	EnableRestoration bool // Enable Loop Restoration Filter.
	EnableSuperRes    bool // Enable super-resolution.
	UseSuperblock128  bool // Use 128x128 superblocks (vs 64x64).

	// Color properties.
	ColorPrimaries          obu.ColorPrimaries
	TransferCharacteristics obu.TransferCharacteristics
	MatrixCoefficients      obu.MatrixCoefficients
	FullColorRange          bool // true = full range, false = studio/limited range.

	// Film grain.
	FilmGrainDenoise bool // Enable film grain synthesis.

	// Still picture mode.
	StillPicture bool // Encode a single still image (AVIF).
}

// DefaultConfig returns a Config with sensible defaults for encoding
// video at the given dimensions.
func DefaultConfig(width, height uint32) Config {
	return Config{
		Width:  width,
		Height: height,

		Profile:  ProfileMain,
		BitDepth: 8,

		FrameRateNum: 30,
		FrameRateDen: 1,

		PixelFormat: PixelFormatYUV420,

		RateControl: RateControlCQ,
		QP:          32,

		SpeedLevel: 4,

		KeyFrameInterval: 240,
		ForceKeyFrame:    true,

		Threads:    0,
		TileColumns: 0,
		TileRows:    0,

		LagInFrames: 19,

		EnableCDEF:        true,
		EnableRestoration: true,
		UseSuperblock128:  true,

		ColorPrimaries:          obu.ColorPrimariesBT709,
		TransferCharacteristics: obu.TransferCharacteristicsBT709,
		MatrixCoefficients:      obu.MatrixCoefficientsBT709,
		FullColorRange:          false,
	}
}

// Validate checks the configuration for consistency and returns an error
// if any fields are invalid.
func (c *Config) Validate() error {
	if c.Width == 0 || c.Height == 0 {
		return fmt.Errorf("encoder: width and height must be > 0")
	}
	if c.Width > 65536 || c.Height > 65536 {
		return fmt.Errorf("encoder: maximum dimension is 65536 (got %dx%d)", c.Width, c.Height)
	}

	switch c.BitDepth {
	case 8, 10, 12:
		// Valid.
	default:
		return fmt.Errorf("encoder: invalid bit depth %d (must be 8, 10, or 12)", c.BitDepth)
	}

	if c.Profile > 2 {
		return fmt.Errorf("encoder: invalid profile %d (must be 0-2)", c.Profile)
	}

	// Profile constraints.
	switch c.Profile {
	case ProfileMain:
		if c.BitDepth == 12 {
			return fmt.Errorf("encoder: profile Main does not support 12-bit")
		}
		if c.PixelFormat != PixelFormatYUV420 && c.PixelFormat != PixelFormatMonochrome {
			return fmt.Errorf("encoder: profile Main only supports YUV420 and Monochrome")
		}
	case ProfileHigh:
		if c.BitDepth == 12 {
			return fmt.Errorf("encoder: profile High does not support 12-bit")
		}
		if c.PixelFormat != PixelFormatYUV444 {
			return fmt.Errorf("encoder: profile High only supports YUV444")
		}
	case ProfileProfessional:
		// All bit depths and chroma subsamplings are allowed.
	}

	if c.FrameRateNum == 0 || c.FrameRateDen == 0 {
		return fmt.Errorf("encoder: frame rate numerator and denominator must be > 0")
	}

	if c.QP < 0 || c.QP > 63 {
		return fmt.Errorf("encoder: QP must be in range [0, 63], got %d", c.QP)
	}

	if c.SpeedLevel < 0 || c.SpeedLevel > 6 {
		return fmt.Errorf("encoder: speed level must be in range [0, 6], got %d", c.SpeedLevel)
	}

	if c.TileColumns < 0 || c.TileColumns > 6 {
		return fmt.Errorf("encoder: tile_columns must be in range [0, 6], got %d", c.TileColumns)
	}
	if c.TileRows < 0 || c.TileRows > 6 {
		return fmt.Errorf("encoder: tile_rows must be in range [0, 6], got %d", c.TileRows)
	}

	if c.LagInFrames < 0 {
		return fmt.Errorf("encoder: lag_in_frames must be >= 0")
	}

	if c.TargetBitrate < 0 {
		return fmt.Errorf("encoder: target_bitrate must be >= 0")
	}
	if c.RateControl != RateControlCQ && c.TargetBitrate == 0 {
		return fmt.Errorf("encoder: CBR/VBR modes require target_bitrate > 0")
	}

	return nil
}

// SequenceHeader generates an obu.SequenceHeader from this encoder configuration.
func (c *Config) SequenceHeader() *obu.SequenceHeader {
	sh := obu.DefaultSequenceHeader(c.Width, c.Height, c.Profile)

	sh.StillPicture = c.StillPicture
	if c.StillPicture {
		sh.ReducedStillPictureHeader = true
	}

	sh.EnableCDEF = c.EnableCDEF
	sh.EnableRestoration = c.EnableRestoration
	sh.EnableSuperRes = c.EnableSuperRes
	sh.Use128x128Superblock = c.UseSuperblock128

	sh.ColorConfig.HighBitDepth = c.BitDepth >= 10
	sh.ColorConfig.TwelveBit = c.BitDepth == 12
	sh.ColorConfig.ColorDescriptionPresent = true
	sh.ColorConfig.ColorPrimaries = c.ColorPrimaries
	sh.ColorConfig.TransferCharacteristics = c.TransferCharacteristics
	sh.ColorConfig.MatrixCoefficients = c.MatrixCoefficients
	sh.ColorConfig.ColorRange = c.FullColorRange
	sh.ColorConfig.MonoChrome = c.PixelFormat == PixelFormatMonochrome

	switch c.PixelFormat {
	case PixelFormatYUV420:
		sh.ColorConfig.SubsamplingX = 1
		sh.ColorConfig.SubsamplingY = 1
	case PixelFormatYUV422:
		sh.ColorConfig.SubsamplingX = 1
		sh.ColorConfig.SubsamplingY = 0
	case PixelFormatYUV444:
		sh.ColorConfig.SubsamplingX = 0
		sh.ColorConfig.SubsamplingY = 0
	case PixelFormatMonochrome:
		sh.ColorConfig.SubsamplingX = 1
		sh.ColorConfig.SubsamplingY = 1
	}

	sh.FilmGrainParamsPresent = c.FilmGrainDenoise

	if !c.StillPicture {
		sh.TimingInfoPresent = true
		sh.TimingInfo = &obu.TimingInfo{
			NumUnitsInDisplayTick: c.FrameRateDen,
			TimeScale:             c.FrameRateNum,
			EqualPictureInterval:  true,
			NumTicksPerPicture:    1,
		}
	}

	return sh
}
