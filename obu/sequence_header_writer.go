package obu

import (
	"av1go/bitstream"
	"fmt"
	"math/bits"
)

// WriteSequenceHeader serializes a SequenceHeader into a byte slice
// suitable for use as an OBU payload. The caller is responsible for
// wrapping the result in an OBU header + size.
//
// AV1 spec Section 5.5.1: sequence_header_obu()
func WriteSequenceHeader(sh *SequenceHeader) ([]byte, error) {
	if err := validateSequenceHeader(sh); err != nil {
		return nil, err
	}

	w := bitstream.NewWriter(128)

	// seq_profile: f(3)
	w.WriteBits(uint64(sh.SeqProfile), 3)

	// still_picture: f(1)
	w.WriteBool(sh.StillPicture)

	// reduced_still_picture_header: f(1)
	w.WriteBool(sh.ReducedStillPictureHeader)

	if sh.ReducedStillPictureHeader {
		// Reduced still picture header: single operating point, no timing, no decoder model.
		// operating_points_cnt_minus_1 is implicitly 0.
		// operating_point_idc[0] is implicitly 0.
		// seq_level_idx[0]: f(5)
		w.WriteBits(uint64(sh.OperatingPoints[0].SeqLevelIdx), 5)
	} else {
		// timing_info_present_flag: f(1)
		w.WriteBool(sh.TimingInfoPresent)

		if sh.TimingInfoPresent {
			writeTimingInfo(w, sh.TimingInfo)

			// decoder_model_info_present_flag: f(1)
			w.WriteBool(sh.DecoderModelInfoPresent)
			if sh.DecoderModelInfoPresent {
				writeDecoderModelInfo(w, sh.DecoderModelInfo)
			}
		}

		// initial_display_delay_present_flag: f(1)
		w.WriteBool(sh.InitialDisplayDelayPresent)

		// operating_points_cnt_minus_1: f(5)
		opCnt := sh.OperatingPointsCntMinus1
		w.WriteBits(uint64(opCnt), 5)

		for i := 0; i <= int(opCnt); i++ {
			op := sh.OperatingPoints[i]

			// operating_point_idc[i]: f(12)
			w.WriteBits(uint64(op.Idc), 12)

			// seq_level_idx[i]: f(5)
			w.WriteBits(uint64(op.SeqLevelIdx), 5)

			if op.SeqLevelIdx > 7 {
				// seq_tier[i]: f(1)
				w.WriteBool(op.SeqTier == 1)
			}

			if sh.DecoderModelInfoPresent {
				// decoder_model_present_for_this_op[i]: f(1)
				w.WriteBool(op.DecoderModelPresentForOP)
				if op.DecoderModelPresentForOP {
					writeOperatingParametersInfo(w, op.OperatingParametersInfo, sh.DecoderModelInfo)
				}
			}

			if sh.InitialDisplayDelayPresent {
				// initial_display_delay_present_for_this_op[i]: f(1)
				w.WriteBool(op.InitialDisplayDelayPresent)
				if op.InitialDisplayDelayPresent {
					// initial_display_delay_minus_1[i]: f(4)
					w.WriteBits(uint64(op.InitialDisplayDelay-1), 4)
				}
			}
		}
	}

	// frame_width_bits_minus_1: f(4)
	w.WriteBits(uint64(sh.FrameWidthBitsMinus1), 4)

	// frame_height_bits_minus_1: f(4)
	w.WriteBits(uint64(sh.FrameHeightBitsMinus1), 4)

	// max_frame_width_minus_1: f(n+1) where n = frame_width_bits_minus_1
	w.WriteBits(uint64(sh.MaxFrameWidthMinus1), int(sh.FrameWidthBitsMinus1)+1)

	// max_frame_height_minus_1: f(n+1) where n = frame_height_bits_minus_1
	w.WriteBits(uint64(sh.MaxFrameHeightMinus1), int(sh.FrameHeightBitsMinus1)+1)

	if !sh.ReducedStillPictureHeader {
		// frame_id_numbers_present_flag: f(1)
		w.WriteBool(sh.FrameIDNumbersPresent)

		if sh.FrameIDNumbersPresent {
			// delta_frame_id_length_minus_2: f(4)
			w.WriteBits(uint64(sh.DeltaFrameIDLengthMinus2), 4)

			// additional_frame_id_length_minus_1: f(3)
			w.WriteBits(uint64(sh.AdditionalFrameIDLengthMinus1), 3)
		}
	}

	// use_128x128_superblock: f(1)
	w.WriteBool(sh.Use128x128Superblock)

	// enable_filter_intra: f(1)
	w.WriteBool(sh.EnableFilterIntra)

	// enable_intra_edge_filter: f(1)
	w.WriteBool(sh.EnableIntraEdgeFilter)

	if !sh.ReducedStillPictureHeader {
		// enable_interintra_compound: f(1)
		w.WriteBool(sh.EnableInterIntraCompound)

		// enable_masked_compound: f(1)
		w.WriteBool(sh.EnableMaskedCompound)

		// enable_warped_motion: f(1)
		w.WriteBool(sh.EnableWarpedMotion)

		// enable_dual_filter: f(1)
		w.WriteBool(sh.EnableDualFilter)

		// enable_order_hint: f(1)
		w.WriteBool(sh.EnableOrderHint)

		if sh.EnableOrderHint {
			// enable_jnt_comp: f(1)
			w.WriteBool(sh.EnableJNTComp)

			// enable_ref_frame_mvs: f(1)
			w.WriteBool(sh.EnableRefFrameMVS)
		}

		// seq_choose_screen_content_tools: f(1)
		if sh.SeqForceScreenContentTools == SelectScreenContentTools {
			w.WriteBool(true) // seq_choose_screen_content_tools = 1
		} else {
			w.WriteBool(false)
			// seq_force_screen_content_tools: f(1)
			w.WriteBool(sh.SeqForceScreenContentTools == 1)
		}

		if sh.SeqForceScreenContentTools > 0 {
			// seq_choose_integer_mv: f(1)
			if sh.SeqForceIntegerMV == SelectIntegerMV {
				w.WriteBool(true)
			} else {
				w.WriteBool(false)
				// seq_force_integer_mv: f(1)
				w.WriteBool(sh.SeqForceIntegerMV == 1)
			}
		}

		if sh.EnableOrderHint {
			// order_hint_bits_minus_1: f(3)
			w.WriteBits(uint64(sh.OrderHintBitsMinus1), 3)
		}

		// enable_superres: f(1)
		w.WriteBool(sh.EnableSuperRes)

		// enable_cdef: f(1)
		w.WriteBool(sh.EnableCDEF)

		// enable_restoration: f(1)
		w.WriteBool(sh.EnableRestoration)
	}

	// color_config()
	writeColorConfig(w, &sh.ColorConfig, sh.SeqProfile, sh.ColorConfig.BitDepth())

	// film_grain_params_present: f(1)
	w.WriteBool(sh.FilmGrainParamsPresent)

	// trailing_bits()
	w.WriteTrailingBits()

	return w.Bytes(), nil
}

// writeTimingInfo writes the timing_info() syntax element.
// AV1 spec Section 5.5.3.
func writeTimingInfo(w *bitstream.Writer, ti *TimingInfo) {
	// num_units_in_display_tick: f(32)
	w.WriteUint32(ti.NumUnitsInDisplayTick)

	// time_scale: f(32)
	w.WriteUint32(ti.TimeScale)

	// equal_picture_interval: f(1)
	w.WriteBool(ti.EqualPictureInterval)

	if ti.EqualPictureInterval {
		// num_ticks_per_picture_minus_1: uvlc()
		w.WriteUvlc(ti.NumTicksPerPicture - 1)
	}
}

// writeDecoderModelInfo writes the decoder_model_info() syntax element.
// AV1 spec Section 5.5.4.
func writeDecoderModelInfo(w *bitstream.Writer, dmi *DecoderModelInfo) {
	// buffer_delay_length_minus_1: f(5)
	w.WriteBits(uint64(dmi.BufferDelayLengthMinus1), 5)

	// num_units_in_decoding_tick: f(32)
	w.WriteUint32(dmi.NumUnitsInDecodingTick)

	// buffer_removal_time_length_minus_1: f(5)
	w.WriteBits(uint64(dmi.BufferRemovalTimeLengthMinus1), 5)

	// frame_presentation_time_length_minus_1: f(5)
	w.WriteBits(uint64(dmi.FramePresentationTimeLengthMinus1), 5)
}

// writeOperatingParametersInfo writes operating_parameters_info() for one operating point.
// AV1 spec Section 5.5.5.
func writeOperatingParametersInfo(w *bitstream.Writer, opi *OperatingParametersInfo, dmi *DecoderModelInfo) {
	n := int(dmi.BufferDelayLengthMinus1) + 1

	// decoder_buffer_delay[op]: f(n)
	w.WriteBits(uint64(opi.DecoderBufferDelay), n)

	// encoder_buffer_delay[op]: f(n)
	w.WriteBits(uint64(opi.EncoderBufferDelay), n)

	// low_delay_mode_flag[op]: f(1)
	w.WriteBool(opi.LowDelayModeFlag)
}

// writeColorConfig writes the color_config() syntax element.
// AV1 spec Section 5.5.2.
func writeColorConfig(w *bitstream.Writer, cc *ColorConfig, seqProfile uint8, bitDepth int) {
	// high_bitdepth: f(1)
	w.WriteBool(cc.HighBitDepth)

	if seqProfile == ProfileProfessional && cc.HighBitDepth {
		// twelve_bit: f(1)
		w.WriteBool(cc.TwelveBit)
	}

	monoChrome := cc.MonoChrome
	if seqProfile != ProfileHigh {
		// mono_chrome: f(1)
		w.WriteBool(monoChrome)
	}
	// Note: for profile 1, mono_chrome is always 0.

	// color_description_present_flag: f(1)
	w.WriteBool(cc.ColorDescriptionPresent)

	if cc.ColorDescriptionPresent {
		// color_primaries: f(8)
		w.WriteUint8(uint8(cc.ColorPrimaries))

		// transfer_characteristics: f(8)
		w.WriteUint8(uint8(cc.TransferCharacteristics))

		// matrix_coefficients: f(8)
		w.WriteUint8(uint8(cc.MatrixCoefficients))
	}

	if monoChrome {
		// color_range: f(1)
		w.WriteBool(cc.ColorRange)
		// subsampling_x and subsampling_y are implicitly 1 for mono.
		return
	}

	cp := cc.ColorPrimaries
	tc := cc.TransferCharacteristics
	mc := cc.MatrixCoefficients
	if !cc.ColorDescriptionPresent {
		cp = ColorPrimariesUnspecified
		tc = TransferCharacteristicsUnspecified
		mc = MatrixCoefficientsUnspecified
	}

	if cp == ColorPrimariesBT709 &&
		tc == TransferCharacteristicsSRGB &&
		mc == MatrixCoefficientsIdentity {
		// sRGB: color_range must be 1, subsampling must be 0,0.
		// No bits written for color_range or subsampling.
	} else {
		// color_range: f(1)
		w.WriteBool(cc.ColorRange)

		if seqProfile == ProfileMain {
			// subsampling_x = 1, subsampling_y = 1 implicitly (4:2:0)
		} else if seqProfile == ProfileHigh {
			// subsampling_x = 0, subsampling_y = 0 implicitly (4:4:4)
		} else {
			// Professional profile.
			if bitDepth == 12 {
				// subsampling_x: f(1)
				w.WriteBool(cc.SubsamplingX == 1)
				if cc.SubsamplingX == 1 {
					// subsampling_y: f(1)
					w.WriteBool(cc.SubsamplingY == 1)
				}
			}
			// For 8/10-bit professional: subsampling_x = 1, subsampling_y = 0 (4:2:2)
		}

		if cc.SubsamplingX == 1 && cc.SubsamplingY == 1 {
			// chroma_sample_position: f(2)
			w.WriteBits(uint64(cc.ChromaSamplePosition), 2)
		}
	}

	// separate_uv_delta_q: f(1)
	w.WriteBool(cc.SeparateUVDeltaQ)
}

// validateSequenceHeader performs basic validation on the SequenceHeader fields.
func validateSequenceHeader(sh *SequenceHeader) error {
	if sh.SeqProfile > 2 {
		return fmt.Errorf("obu: invalid seq_profile %d (must be 0-2)", sh.SeqProfile)
	}

	if sh.ReducedStillPictureHeader && !sh.StillPicture {
		return fmt.Errorf("obu: reduced_still_picture_header requires still_picture")
	}

	if len(sh.OperatingPoints) == 0 {
		return fmt.Errorf("obu: at least one operating point is required")
	}

	if sh.ReducedStillPictureHeader {
		if sh.TimingInfoPresent {
			return fmt.Errorf("obu: timing_info not allowed with reduced_still_picture_header")
		}
	} else {
		opCount := int(sh.OperatingPointsCntMinus1) + 1
		if len(sh.OperatingPoints) < opCount {
			return fmt.Errorf("obu: need %d operating points, have %d", opCount, len(sh.OperatingPoints))
		}
	}

	if sh.TimingInfoPresent && sh.TimingInfo == nil {
		return fmt.Errorf("obu: timing_info_present but TimingInfo is nil")
	}

	if sh.DecoderModelInfoPresent && sh.DecoderModelInfo == nil {
		return fmt.Errorf("obu: decoder_model_info_present but DecoderModelInfo is nil")
	}

	// Validate frame dimension bit widths.
	maxW := sh.MaxFrameWidthMinus1
	widthBits := int(sh.FrameWidthBitsMinus1) + 1
	if bits.Len32(maxW) > widthBits {
		return fmt.Errorf("obu: max_frame_width_minus_1 %d requires %d bits but frame_width_bits_minus_1 allows %d",
			maxW, bits.Len32(maxW), widthBits)
	}

	maxH := sh.MaxFrameHeightMinus1
	heightBits := int(sh.FrameHeightBitsMinus1) + 1
	if bits.Len32(maxH) > heightBits {
		return fmt.Errorf("obu: max_frame_height_minus_1 %d requires %d bits but frame_height_bits_minus_1 allows %d",
			maxH, bits.Len32(maxH), heightBits)
	}

	return nil
}

// DefaultSequenceHeader returns a SequenceHeader with sensible defaults for
// the given dimensions and profile. This is useful as a starting point for
// encoder configuration.
func DefaultSequenceHeader(width, height uint32, profile uint8) *SequenceHeader {
	widthBits := uint8(bits.Len32(width - 1))
	if widthBits == 0 {
		widthBits = 1
	}
	heightBits := uint8(bits.Len32(height - 1))
	if heightBits == 0 {
		heightBits = 1
	}

	sh := &SequenceHeader{
		SeqProfile: profile,

		OperatingPointsCntMinus1: 0,
		OperatingPoints: []OperatingPoint{
			{
				Idc:         0,
				SeqLevelIdx: 5, // Level 3.1 — suitable for 1080p30
			},
		},

		FrameWidthBitsMinus1:  widthBits - 1,
		FrameHeightBitsMinus1: heightBits - 1,
		MaxFrameWidthMinus1:   width - 1,
		MaxFrameHeightMinus1:  height - 1,

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
		OrderHintBitsMinus1:      6, // 7-bit order hints

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
			ColorRange:              false, // Studio/limited range
			SubsamplingX:            1,
			SubsamplingY:            1,
			ChromaSamplePosition:    ChromaSamplePositionUnknown,
		},
	}

	return sh
}
