package obu

import (
	"av1go/bitstream"
	"fmt"
	"io"
)

// Unit represents a single parsed OBU with its header and payload.
type Unit struct {
	Header  Header
	Size    uint64 // payload size in bytes (from leb128 size field)
	Payload []byte // raw OBU payload (excluding header and size field)
}

// ParsedSequenceHeader is a Unit whose SequenceHeader field has been populated.
type ParsedSequenceHeader struct {
	Unit
	SeqHdr SequenceHeader
}

// ParsedFrameHeader contains the basic frame header fields we parse.
type ParsedFrameHeader struct {
	Unit
	FrameHdr FrameHeader
}

// FrameHeader represents a partially parsed uncompressed_header().
// AV1 spec Section 5.9.2. Only the fields needed for basic frame
// identification are parsed here; full parsing comes later.
type FrameHeader struct {
	ShowExistingFrame bool
	FrameToShowMapIdx uint8 // only if ShowExistingFrame
	FrameType         FrameType
	ShowFrame         bool
	ShowableFrame     bool
	ErrorResilientMode bool
	OrderHint         uint32
}

// ParseHeader parses an OBU header from the given byte.
// AV1 spec Section 5.3.2: obu_header()
func ParseHeader(data []byte) (Header, int, error) {
	if len(data) < 1 {
		return Header{}, 0, io.ErrUnexpectedEOF
	}
	b0 := data[0]

	// obu_forbidden_bit (bit 7) must be 0
	if b0&0x80 != 0 {
		return Header{}, 0, fmt.Errorf("obu: forbidden bit is set")
	}

	h := Header{
		Type:         Type((b0 >> 3) & 0x0F),
		HasExtension: (b0 & 0x04) != 0,
		HasSize:      (b0 & 0x02) != 0,
	}

	consumed := 1
	if h.HasExtension {
		if len(data) < 2 {
			return Header{}, 0, io.ErrUnexpectedEOF
		}
		b1 := data[1]
		h.Extension = &ExtensionHeader{
			TemporalID: (b1 >> 5) & 0x07,
			SpatialID:  (b1 >> 3) & 0x03,
		}
		consumed = 2
	}
	return h, consumed, nil
}

// ParseUnit parses a single OBU from a byte slice at the given offset.
// It reads the header, optional size field, and returns a Unit with the
// raw payload. Returns the Unit and the total number of bytes consumed.
//
// If the OBU header has obu_has_size_flag=0, the remaining bytes after
// the header are treated as the payload (used in Annex B framing).
func ParseUnit(data []byte) (Unit, int, error) {
	h, hdrSize, err := ParseHeader(data)
	if err != nil {
		return Unit{}, 0, err
	}

	pos := hdrSize
	var payloadSize uint64
	if h.HasSize {
		size, n, err := bitstream.ReadLeb128(data[pos:])
		if err != nil {
			return Unit{}, 0, fmt.Errorf("obu: reading size: %w", err)
		}
		payloadSize = size
		pos += n
	} else {
		payloadSize = uint64(len(data) - pos)
	}

	end := pos + int(payloadSize)
	if end > len(data) {
		return Unit{}, 0, fmt.Errorf("obu: payload size %d exceeds available data %d", payloadSize, len(data)-pos)
	}

	u := Unit{
		Header:  h,
		Size:    payloadSize,
		Payload: data[pos:end],
	}
	return u, end, nil
}

// ParseUnits parses all OBUs from a byte slice (e.g., an AV1 sample
// in low-overhead bitstream format). Returns the list of parsed units.
func ParseUnits(data []byte) ([]Unit, error) {
	var units []Unit
	pos := 0
	for pos < len(data) {
		u, n, err := ParseUnit(data[pos:])
		if err != nil {
			return units, fmt.Errorf("obu: at offset %d: %w", pos, err)
		}
		if n == 0 {
			break
		}
		units = append(units, u)
		pos += n
	}
	return units, nil
}

// ParseSequenceHeader parses a sequence_header_obu() from the OBU payload.
// AV1 spec Section 5.5.1.
func ParseSequenceHeader(payload []byte) (*SequenceHeader, error) {
	r := bitstream.NewReader(payload)
	sh := &SequenceHeader{}

	var err error

	// seq_profile f(3)
	sh.SeqProfile, err = r.ReadUint8(3)
	if err != nil {
		return nil, fmt.Errorf("obu: seq_profile: %w", err)
	}

	// still_picture f(1)
	sh.StillPicture, err = r.ReadFlag()
	if err != nil {
		return nil, fmt.Errorf("obu: still_picture: %w", err)
	}

	// reduced_still_picture_header f(1)
	sh.ReducedStillPictureHeader, err = r.ReadFlag()
	if err != nil {
		return nil, fmt.Errorf("obu: reduced_still_picture_header: %w", err)
	}

	if sh.ReducedStillPictureHeader {
		// Simplified header for still pictures.
		sh.OperatingPointsCntMinus1 = 0
		sh.OperatingPoints = []OperatingPoint{{}}
		sh.OperatingPoints[0].SeqLevelIdx, err = r.ReadUint8(5)
		if err != nil {
			return nil, fmt.Errorf("obu: seq_level_idx: %w", err)
		}
		sh.OperatingPoints[0].SeqTier = 0
	} else {
		// timing_info_present_flag f(1)
		sh.TimingInfoPresent, err = r.ReadFlag()
		if err != nil {
			return nil, err
		}

		if sh.TimingInfoPresent {
			sh.TimingInfo, err = parseTimingInfo(r)
			if err != nil {
				return nil, err
			}

			// decoder_model_info_present_flag f(1)
			sh.DecoderModelInfoPresent, err = r.ReadFlag()
			if err != nil {
				return nil, err
			}
			if sh.DecoderModelInfoPresent {
				sh.DecoderModelInfo, err = parseDecoderModelInfo(r)
				if err != nil {
					return nil, err
				}
			}
		}

		// initial_display_delay_present_flag f(1)
		sh.InitialDisplayDelayPresent, err = r.ReadFlag()
		if err != nil {
			return nil, err
		}

		// operating_points_cnt_minus_1 f(5)
		sh.OperatingPointsCntMinus1, err = r.ReadUint8(5)
		if err != nil {
			return nil, err
		}
		numOPs := int(sh.OperatingPointsCntMinus1) + 1
		sh.OperatingPoints = make([]OperatingPoint, numOPs)

		for i := 0; i < numOPs; i++ {
			op := &sh.OperatingPoints[i]

			// operating_point_idc f(12)
			op.Idc, err = r.ReadUint16(12)
			if err != nil {
				return nil, err
			}

			// seq_level_idx f(5)
			op.SeqLevelIdx, err = r.ReadUint8(5)
			if err != nil {
				return nil, err
			}

			if op.SeqLevelIdx > 7 {
				// seq_tier f(1)
				op.SeqTier, err = r.ReadUint8(1)
				if err != nil {
					return nil, err
				}
			}

			if sh.DecoderModelInfoPresent {
				op.DecoderModelPresentForOP, err = r.ReadFlag()
				if err != nil {
					return nil, err
				}
				if op.DecoderModelPresentForOP {
					op.OperatingParametersInfo, err = parseOperatingParametersInfo(r, sh.DecoderModelInfo)
					if err != nil {
						return nil, err
					}
				}
			}

			if sh.InitialDisplayDelayPresent {
				op.InitialDisplayDelayPresent, err = r.ReadFlag()
				if err != nil {
					return nil, err
				}
				if op.InitialDisplayDelayPresent {
					v, err := r.ReadUint8(4)
					if err != nil {
						return nil, err
					}
					op.InitialDisplayDelay = v + 1
				}
			}
		}
	}

	// frame_width_bits_minus_1 f(4)
	sh.FrameWidthBitsMinus1, err = r.ReadUint8(4)
	if err != nil {
		return nil, err
	}

	// frame_height_bits_minus_1 f(4)
	sh.FrameHeightBitsMinus1, err = r.ReadUint8(4)
	if err != nil {
		return nil, err
	}

	// max_frame_width_minus_1 f(n+1)
	sh.MaxFrameWidthMinus1, err = r.ReadUint32(int(sh.FrameWidthBitsMinus1) + 1)
	if err != nil {
		return nil, err
	}

	// max_frame_height_minus_1 f(n+1)
	sh.MaxFrameHeightMinus1, err = r.ReadUint32(int(sh.FrameHeightBitsMinus1) + 1)
	if err != nil {
		return nil, err
	}

	if !sh.ReducedStillPictureHeader {
		// frame_id_numbers_present_flag f(1)
		sh.FrameIDNumbersPresent, err = r.ReadFlag()
		if err != nil {
			return nil, err
		}
	}

	if sh.FrameIDNumbersPresent {
		// delta_frame_id_length_minus_2 f(4)
		sh.DeltaFrameIDLengthMinus2, err = r.ReadUint8(4)
		if err != nil {
			return nil, err
		}
		// additional_frame_id_length_minus_1 f(3)
		sh.AdditionalFrameIDLengthMinus1, err = r.ReadUint8(3)
		if err != nil {
			return nil, err
		}
	}

	// use_128x128_superblock f(1)
	sh.Use128x128Superblock, err = r.ReadFlag()
	if err != nil {
		return nil, err
	}

	// enable_filter_intra f(1)
	sh.EnableFilterIntra, err = r.ReadFlag()
	if err != nil {
		return nil, err
	}

	// enable_intra_edge_filter f(1)
	sh.EnableIntraEdgeFilter, err = r.ReadFlag()
	if err != nil {
		return nil, err
	}

	if !sh.ReducedStillPictureHeader {
		// enable_interintra_compound f(1)
		sh.EnableInterIntraCompound, err = r.ReadFlag()
		if err != nil {
			return nil, err
		}
		// enable_masked_compound f(1)
		sh.EnableMaskedCompound, err = r.ReadFlag()
		if err != nil {
			return nil, err
		}
		// enable_warped_motion f(1)
		sh.EnableWarpedMotion, err = r.ReadFlag()
		if err != nil {
			return nil, err
		}
		// enable_dual_filter f(1)
		sh.EnableDualFilter, err = r.ReadFlag()
		if err != nil {
			return nil, err
		}
		// enable_order_hint f(1)
		sh.EnableOrderHint, err = r.ReadFlag()
		if err != nil {
			return nil, err
		}

		if sh.EnableOrderHint {
			// enable_jnt_comp f(1)
			sh.EnableJNTComp, err = r.ReadFlag()
			if err != nil {
				return nil, err
			}
			// enable_ref_frame_mvs f(1)
			sh.EnableRefFrameMVS, err = r.ReadFlag()
			if err != nil {
				return nil, err
			}
		}

		// seq_choose_screen_content_tools f(1)
		seqChooseSCT, err := r.ReadFlag()
		if err != nil {
			return nil, err
		}
		if seqChooseSCT {
			sh.SeqForceScreenContentTools = SelectScreenContentTools
		} else {
			v, err := r.ReadUint8(1)
			if err != nil {
				return nil, err
			}
			sh.SeqForceScreenContentTools = v
		}

		if sh.SeqForceScreenContentTools > 0 {
			// seq_choose_integer_mv f(1)
			seqChooseIMV, err := r.ReadFlag()
			if err != nil {
				return nil, err
			}
			if seqChooseIMV {
				sh.SeqForceIntegerMV = SelectIntegerMV
			} else {
				v, err := r.ReadUint8(1)
				if err != nil {
					return nil, err
				}
				sh.SeqForceIntegerMV = v
			}
		}

		if sh.EnableOrderHint {
			// order_hint_bits_minus_1 f(3)
			sh.OrderHintBitsMinus1, err = r.ReadUint8(3)
			if err != nil {
				return nil, err
			}
		}
	}

	// enable_superres f(1)
	sh.EnableSuperRes, err = r.ReadFlag()
	if err != nil {
		return nil, err
	}

	// enable_cdef f(1)
	sh.EnableCDEF, err = r.ReadFlag()
	if err != nil {
		return nil, err
	}

	// enable_restoration f(1)
	sh.EnableRestoration, err = r.ReadFlag()
	if err != nil {
		return nil, err
	}

	// color_config()
	if err := parseColorConfig(r, sh); err != nil {
		return nil, fmt.Errorf("obu: color_config: %w", err)
	}

	// film_grain_params_present f(1)
	sh.FilmGrainParamsPresent, err = r.ReadFlag()
	if err != nil {
		return nil, err
	}

	return sh, nil
}

// parseTimingInfo parses timing_info().
// AV1 spec Section 5.5.3.
func parseTimingInfo(r *bitstream.Reader) (*TimingInfo, error) {
	ti := &TimingInfo{}
	var err error

	// num_units_in_display_tick f(32)
	ti.NumUnitsInDisplayTick, err = r.ReadUint32(32)
	if err != nil {
		return nil, err
	}

	// time_scale f(32)
	ti.TimeScale, err = r.ReadUint32(32)
	if err != nil {
		return nil, err
	}

	// equal_picture_interval f(1)
	ti.EqualPictureInterval, err = r.ReadFlag()
	if err != nil {
		return nil, err
	}

	if ti.EqualPictureInterval {
		// num_ticks_per_picture_minus_1 uvlc()
		v, err := r.ReadUvlc()
		if err != nil {
			return nil, err
		}
		ti.NumTicksPerPicture = v + 1
	}

	return ti, nil
}

// parseDecoderModelInfo parses decoder_model_info().
// AV1 spec Section 5.5.4.
func parseDecoderModelInfo(r *bitstream.Reader) (*DecoderModelInfo, error) {
	dmi := &DecoderModelInfo{}
	var err error

	// buffer_delay_length_minus_1 f(5)
	dmi.BufferDelayLengthMinus1, err = r.ReadUint8(5)
	if err != nil {
		return nil, err
	}

	// num_units_in_decoding_tick f(32)
	dmi.NumUnitsInDecodingTick, err = r.ReadUint32(32)
	if err != nil {
		return nil, err
	}

	// buffer_removal_time_length_minus_1 f(5)
	dmi.BufferRemovalTimeLengthMinus1, err = r.ReadUint8(5)
	if err != nil {
		return nil, err
	}

	// frame_presentation_time_length_minus_1 f(5)
	dmi.FramePresentationTimeLengthMinus1, err = r.ReadUint8(5)
	if err != nil {
		return nil, err
	}

	return dmi, nil
}

// parseOperatingParametersInfo parses operating_parameters_info().
// AV1 spec Section 5.5.5.
func parseOperatingParametersInfo(r *bitstream.Reader, dmi *DecoderModelInfo) (*OperatingParametersInfo, error) {
	opi := &OperatingParametersInfo{}
	n := int(dmi.BufferDelayLengthMinus1) + 1
	var err error

	// decoder_buffer_delay f(n)
	opi.DecoderBufferDelay, err = r.ReadUint32(n)
	if err != nil {
		return nil, err
	}

	// encoder_buffer_delay f(n)
	opi.EncoderBufferDelay, err = r.ReadUint32(n)
	if err != nil {
		return nil, err
	}

	// low_delay_mode_flag f(1)
	opi.LowDelayModeFlag, err = r.ReadFlag()
	if err != nil {
		return nil, err
	}

	return opi, nil
}

// parseColorConfig parses color_config().
// AV1 spec Section 5.5.2.
func parseColorConfig(r *bitstream.Reader, sh *SequenceHeader) error {
	cc := &sh.ColorConfig
	var err error

	// high_bitdepth f(1)
	cc.HighBitDepth, err = r.ReadFlag()
	if err != nil {
		return err
	}

	if sh.SeqProfile == ProfileProfessional && cc.HighBitDepth {
		// twelve_bit f(1)
		cc.TwelveBit, err = r.ReadFlag()
		if err != nil {
			return err
		}
	}

	bitDepth := cc.BitDepth()

	if sh.SeqProfile == ProfileHigh {
		cc.MonoChrome = false
	} else {
		// mono_chrome f(1)
		cc.MonoChrome, err = r.ReadFlag()
		if err != nil {
			return err
		}
	}

	// color_description_present_flag f(1)
	cc.ColorDescriptionPresent, err = r.ReadFlag()
	if err != nil {
		return err
	}

	if cc.ColorDescriptionPresent {
		// color_primaries f(8)
		v, err := r.ReadUint8(8)
		if err != nil {
			return err
		}
		cc.ColorPrimaries = ColorPrimaries(v)

		// transfer_characteristics f(8)
		v, err = r.ReadUint8(8)
		if err != nil {
			return err
		}
		cc.TransferCharacteristics = TransferCharacteristics(v)

		// matrix_coefficients f(8)
		v, err = r.ReadUint8(8)
		if err != nil {
			return err
		}
		cc.MatrixCoefficients = MatrixCoefficients(v)
	} else {
		cc.ColorPrimaries = ColorPrimariesUnspecified
		cc.TransferCharacteristics = TransferCharacteristicsUnspecified
		cc.MatrixCoefficients = MatrixCoefficientsUnspecified
	}

	if cc.MonoChrome {
		// color_range f(1)
		cc.ColorRange, err = r.ReadFlag()
		if err != nil {
			return err
		}
		cc.SubsamplingX = 1
		cc.SubsamplingY = 1
		return nil
	}

	if cc.ColorPrimaries == ColorPrimariesBT709 &&
		cc.TransferCharacteristics == TransferCharacteristicsSRGB &&
		cc.MatrixCoefficients == MatrixCoefficientsIdentity {
		// sRGB: full range, 4:4:4
		cc.ColorRange = true
		cc.SubsamplingX = 0
		cc.SubsamplingY = 0
	} else {
		// color_range f(1)
		cc.ColorRange, err = r.ReadFlag()
		if err != nil {
			return err
		}

		if sh.SeqProfile == ProfileMain {
			cc.SubsamplingX = 1
			cc.SubsamplingY = 1
		} else if sh.SeqProfile == ProfileHigh {
			cc.SubsamplingX = 0
			cc.SubsamplingY = 0
		} else {
			// Professional profile
			if bitDepth == 12 {
				// subsampling_x f(1)
				sx, err := r.ReadFlag()
				if err != nil {
					return err
				}
				if sx {
					cc.SubsamplingX = 1
				}
				if cc.SubsamplingX == 1 {
					// subsampling_y f(1)
					sy, err := r.ReadFlag()
					if err != nil {
						return err
					}
					if sy {
						cc.SubsamplingY = 1
					}
				}
			} else {
				cc.SubsamplingX = 1
				cc.SubsamplingY = 0
			}
		}

		if cc.SubsamplingX == 1 && cc.SubsamplingY == 1 {
			// chroma_sample_position f(2)
			v, err := r.ReadUint8(2)
			if err != nil {
				return err
			}
			cc.ChromaSamplePosition = ChromaSamplePosition(v)
		}
	}

	// separate_uv_delta_q f(1)
	if !cc.MonoChrome {
		cc.SeparateUVDeltaQ, err = r.ReadFlag()
		if err != nil {
			return err
		}
	}

	return nil
}

// ParseBasicFrameHeader parses the first few fields of uncompressed_header()
// sufficient to determine frame type and display properties.
// AV1 spec Section 5.9.2.
//
// The seqHdr must be the active sequence header for context.
// If the OBU type is TypeFrame, the payload starts with the frame header
// followed by tile group data.
func ParseBasicFrameHeader(payload []byte, seqHdr *SequenceHeader) (*FrameHeader, error) {
	r := bitstream.NewReader(payload)
	fh := &FrameHeader{}

	var err error

	if seqHdr.ReducedStillPictureHeader {
		fh.ShowExistingFrame = false
		fh.FrameType = FrameTypeKey
		fh.ShowFrame = true
		fh.ShowableFrame = false
		return fh, nil
	}

	// show_existing_frame f(1)
	fh.ShowExistingFrame, err = r.ReadFlag()
	if err != nil {
		return nil, err
	}

	if fh.ShowExistingFrame {
		// frame_to_show_map_idx f(3)
		fh.FrameToShowMapIdx, err = r.ReadUint8(3)
		if err != nil {
			return nil, err
		}
		return fh, nil
	}

	// frame_type f(2)
	ft, err := r.ReadUint8(2)
	if err != nil {
		return nil, err
	}
	fh.FrameType = FrameType(ft)

	// show_frame f(1)
	fh.ShowFrame, err = r.ReadFlag()
	if err != nil {
		return nil, err
	}

	if fh.ShowFrame {
		if seqHdr.DecoderModelInfoPresent && !seqHdr.TimingInfo.EqualPictureInterval {
			// temporal_point_info() - skip for basic parsing
		}
		fh.ShowableFrame = fh.FrameType != FrameTypeKey
	} else {
		// showable_frame f(1)
		fh.ShowableFrame, err = r.ReadFlag()
		if err != nil {
			return nil, err
		}
	}

	// error_resilient_mode
	if fh.FrameType == FrameTypeSwitch ||
		(fh.FrameType == FrameTypeKey && fh.ShowFrame) {
		fh.ErrorResilientMode = true
	} else {
		fh.ErrorResilientMode, err = r.ReadFlag()
		if err != nil {
			return nil, err
		}
	}

	return fh, nil
}
