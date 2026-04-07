package obu

import (
	"testing"
)

func TestWriteSequenceHeaderMinimal(t *testing.T) {
	// Reduced still picture header — the simplest possible sequence header.
	sh := &SequenceHeader{
		SeqProfile:                ProfileMain,
		StillPicture:              true,
		ReducedStillPictureHeader: true,

		OperatingPoints: []OperatingPoint{
			{SeqLevelIdx: 0},
		},

		FrameWidthBitsMinus1:  9,  // 10 bits -> max 1024
		FrameHeightBitsMinus1: 9,  // 10 bits -> max 1024
		MaxFrameWidthMinus1:   639, // 640 pixels
		MaxFrameHeightMinus1:  479, // 480 pixels

		ColorConfig: ColorConfig{
			HighBitDepth:            false,
			ColorDescriptionPresent: true,
			ColorPrimaries:          ColorPrimariesBT709,
			TransferCharacteristics: TransferCharacteristicsBT709,
			MatrixCoefficients:      MatrixCoefficientsBT709,
			ColorRange:              false,
			SubsamplingX:            1,
			SubsamplingY:            1,
			ChromaSamplePosition:    ChromaSamplePositionUnknown,
		},
	}

	data, err := WriteSequenceHeader(sh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty sequence header")
	}

	// Verify first byte contains profile and still_picture fields.
	// Profile=0 (3 bits: 000), still_picture=1, reduced=1
	// First 5 bits: 00011
	firstByte := data[0]
	profile := (firstByte >> 5) & 0x07
	if profile != 0 {
		t.Fatalf("expected profile 0, got %d (first byte: 0x%02X)", profile, firstByte)
	}
	stillPic := (firstByte >> 4) & 0x01
	if stillPic != 1 {
		t.Fatalf("expected still_picture=1, got %d", stillPic)
	}
	reduced := (firstByte >> 3) & 0x01
	if reduced != 1 {
		t.Fatalf("expected reduced_still_picture_header=1, got %d", reduced)
	}
}

func TestWriteSequenceHeaderFull(t *testing.T) {
	sh := &SequenceHeader{
		SeqProfile: ProfileMain,

		OperatingPointsCntMinus1: 0,
		OperatingPoints: []OperatingPoint{
			{
				Idc:         0,
				SeqLevelIdx: 5, // Level 3.1
			},
		},

		FrameWidthBitsMinus1:  10, // 11 bits
		FrameHeightBitsMinus1: 10,
		MaxFrameWidthMinus1:   1919, // 1920
		MaxFrameHeightMinus1:  1079, // 1080

		Use128x128Superblock:  true,
		EnableFilterIntra:     true,
		EnableIntraEdgeFilter: true,

		EnableInterIntraCompound: true,
		EnableMaskedCompound:     true,
		EnableWarpedMotion:       true,
		EnableDualFilter:         true,
		EnableOrderHint:          true,
		EnableJNTComp:            true,
		EnableRefFrameMVS:        true,
		OrderHintBitsMinus1:      6,

		SeqForceScreenContentTools: SelectScreenContentTools,
		SeqForceIntegerMV:          SelectIntegerMV,

		EnableSuperRes:    false,
		EnableCDEF:        true,
		EnableRestoration: true,

		ColorConfig: ColorConfig{
			HighBitDepth:            false,
			ColorDescriptionPresent: true,
			ColorPrimaries:          ColorPrimariesBT709,
			TransferCharacteristics: TransferCharacteristicsBT709,
			MatrixCoefficients:      MatrixCoefficientsBT709,
			ColorRange:              false,
			SubsamplingX:            1,
			SubsamplingY:            1,
			ChromaSamplePosition:    ChromaSamplePositionUnknown,
		},
	}

	data, err := WriteSequenceHeader(sh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty sequence header")
	}

	// Basic structural checks.
	firstByte := data[0]
	profile := (firstByte >> 5) & 0x07
	if profile != 0 {
		t.Fatalf("expected profile 0, got %d", profile)
	}
	stillPic := (firstByte >> 4) & 0x01
	if stillPic != 0 {
		t.Fatalf("expected still_picture=0, got %d", stillPic)
	}

	// The last byte should end with trailing bits (1 followed by zero padding).
	lastByte := data[len(data)-1]
	if lastByte == 0 {
		t.Fatal("last byte should not be 0 (trailing bits should set at least one bit)")
	}
}

func TestWriteSequenceHeaderWithTimingInfo(t *testing.T) {
	sh := &SequenceHeader{
		SeqProfile: ProfileMain,

		TimingInfoPresent: true,
		TimingInfo: &TimingInfo{
			NumUnitsInDisplayTick: 1,
			TimeScale:             30,
			EqualPictureInterval:  true,
			NumTicksPerPicture:    1,
		},

		OperatingPointsCntMinus1: 0,
		OperatingPoints: []OperatingPoint{
			{SeqLevelIdx: 5},
		},

		FrameWidthBitsMinus1:  10,
		FrameHeightBitsMinus1: 10,
		MaxFrameWidthMinus1:   1919,
		MaxFrameHeightMinus1:  1079,

		EnableOrderHint:     true,
		OrderHintBitsMinus1: 6,

		SeqForceScreenContentTools: SelectScreenContentTools,
		SeqForceIntegerMV:          SelectIntegerMV,

		EnableCDEF: true,

		ColorConfig: ColorConfig{
			SubsamplingX: 1,
			SubsamplingY: 1,
		},
	}

	data, err := WriteSequenceHeader(sh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty sequence header")
	}
	// Timing info adds 32+32+1 = 65 bits minimum; ensure payload is larger.
	if len(data) < 10 {
		t.Fatalf("expected at least 10 bytes with timing info, got %d", len(data))
	}
}

func TestWriteSequenceHeaderHighProfile10Bit(t *testing.T) {
	sh := &SequenceHeader{
		SeqProfile: ProfileHigh,

		OperatingPointsCntMinus1: 0,
		OperatingPoints: []OperatingPoint{
			{SeqLevelIdx: 5},
		},

		FrameWidthBitsMinus1:  10,
		FrameHeightBitsMinus1: 10,
		MaxFrameWidthMinus1:   1919,
		MaxFrameHeightMinus1:  1079,

		EnableOrderHint:     true,
		OrderHintBitsMinus1: 6,

		SeqForceScreenContentTools: SelectScreenContentTools,
		SeqForceIntegerMV:          SelectIntegerMV,

		EnableCDEF: true,

		ColorConfig: ColorConfig{
			HighBitDepth:            true,
			ColorDescriptionPresent: true,
			ColorPrimaries:          ColorPrimariesBT709,
			TransferCharacteristics: TransferCharacteristicsBT709,
			MatrixCoefficients:      MatrixCoefficientsBT709,
			ColorRange:              true, // Full range
			// Profile 1 doesn't write mono_chrome flag.
			// Profile 1 implies 4:4:4 (SubsamplingX=0, SubsamplingY=0).
		},
	}

	data, err := WriteSequenceHeader(sh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty sequence header")
	}
}

func TestWriteSequenceHeaderMonochrome(t *testing.T) {
	sh := &SequenceHeader{
		SeqProfile: ProfileMain,

		OperatingPointsCntMinus1: 0,
		OperatingPoints: []OperatingPoint{
			{SeqLevelIdx: 5},
		},

		FrameWidthBitsMinus1:  10,
		FrameHeightBitsMinus1: 10,
		MaxFrameWidthMinus1:   1919,
		MaxFrameHeightMinus1:  1079,

		EnableOrderHint:     true,
		OrderHintBitsMinus1: 6,

		SeqForceScreenContentTools: SelectScreenContentTools,
		SeqForceIntegerMV:          SelectIntegerMV,

		EnableCDEF: true,

		ColorConfig: ColorConfig{
			MonoChrome: true,
			ColorRange: true,
			// Subsampling is implicit for mono.
			SubsamplingX: 1,
			SubsamplingY: 1,
		},
	}

	data, err := WriteSequenceHeader(sh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty sequence header")
	}
}

func TestWriteSequenceHeaderValidation(t *testing.T) {
	tests := []struct {
		name string
		sh   *SequenceHeader
	}{
		{
			name: "invalid profile",
			sh:   &SequenceHeader{SeqProfile: 3},
		},
		{
			name: "reduced without still_picture",
			sh: &SequenceHeader{
				SeqProfile:                ProfileMain,
				ReducedStillPictureHeader: true,
				StillPicture:              false,
				OperatingPoints:           []OperatingPoint{{SeqLevelIdx: 0}},
			},
		},
		{
			name: "no operating points",
			sh: &SequenceHeader{
				SeqProfile:  ProfileMain,
				StillPicture: true,
				ReducedStillPictureHeader: true,
			},
		},
		{
			name: "timing info present but nil",
			sh: &SequenceHeader{
				SeqProfile:        ProfileMain,
				TimingInfoPresent: true,
				TimingInfo:        nil,
				OperatingPoints:   []OperatingPoint{{SeqLevelIdx: 0}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := WriteSequenceHeader(tt.sh)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDefaultSequenceHeader(t *testing.T) {
	sh := DefaultSequenceHeader(1920, 1080, ProfileMain)

	if sh.SeqProfile != ProfileMain {
		t.Fatalf("expected profile Main, got %d", sh.SeqProfile)
	}
	if sh.MaxFrameWidth() != 1920 {
		t.Fatalf("expected max width 1920, got %d", sh.MaxFrameWidth())
	}
	if sh.MaxFrameHeight() != 1080 {
		t.Fatalf("expected max height 1080, got %d", sh.MaxFrameHeight())
	}

	// Should serialize without error.
	data, err := WriteSequenceHeader(sh)
	if err != nil {
		t.Fatalf("failed to serialize default sequence header: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty data")
	}
}

func TestDefaultSequenceHeaderSmallDimensions(t *testing.T) {
	sh := DefaultSequenceHeader(320, 240, ProfileMain)
	if sh.MaxFrameWidth() != 320 {
		t.Fatalf("expected max width 320, got %d", sh.MaxFrameWidth())
	}
	data, err := WriteSequenceHeader(sh)
	if err != nil {
		t.Fatalf("failed to serialize: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty data")
	}
}

func TestWriteSequenceHeaderFullOBU(t *testing.T) {
	// Integration test: write a complete sequence header OBU.
	sh := DefaultSequenceHeader(1920, 1080, ProfileMain)
	payload, err := WriteSequenceHeader(sh)
	if err != nil {
		t.Fatalf("failed to write sequence header: %v", err)
	}

	obuData, err := MakeOBU(Header{
		Type:    TypeSequenceHeader,
		HasSize: true,
	}, payload)
	if err != nil {
		t.Fatalf("failed to make OBU: %v", err)
	}

	// Verify OBU header byte.
	if obuData[0] != 0x0A {
		t.Fatalf("expected OBU header 0x0A, got 0x%02X", obuData[0])
	}

	// Verify the total structure is sensible.
	if len(obuData) < 5 {
		t.Fatalf("OBU too small: %d bytes", len(obuData))
	}
}

func TestWriteSequenceHeaderMultipleOperatingPoints(t *testing.T) {
	sh := &SequenceHeader{
		SeqProfile: ProfileMain,

		OperatingPointsCntMinus1: 1, // 2 operating points
		OperatingPoints: []OperatingPoint{
			{Idc: 0x001, SeqLevelIdx: 5},
			{Idc: 0x101, SeqLevelIdx: 8, SeqTier: 1},
		},

		FrameWidthBitsMinus1:  10,
		FrameHeightBitsMinus1: 10,
		MaxFrameWidthMinus1:   1919,
		MaxFrameHeightMinus1:  1079,

		EnableOrderHint:     true,
		OrderHintBitsMinus1: 6,

		SeqForceScreenContentTools: SelectScreenContentTools,
		SeqForceIntegerMV:          SelectIntegerMV,

		EnableCDEF: true,

		ColorConfig: ColorConfig{
			SubsamplingX: 1,
			SubsamplingY: 1,
		},
	}

	data, err := WriteSequenceHeader(sh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty data")
	}
}
