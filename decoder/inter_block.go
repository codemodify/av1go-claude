// Package decoder implements AV1 bitstream decoding.
//
// This file implements inter block decoding: the full decode_b() path for
// inter frames, including reference frame selection, inter mode parsing,
// motion vector decoding, motion compensation, and residual reconstruction.
// AV1 spec Sections 5.11.5-5.11.12, 5.11.32, 6.10, 7.11.3.
package decoder

import (
	"fmt"
)

// readDeltaQOrLF reads a delta_q_abs or delta_lf_abs value from the bitstream.
// AV1 spec Section 5.11.2/5.11.3: CDF-coded symbol (4 values) with extension.
// cdf should be a 4-symbol CDF (DeltaQ or DeltaLF).
// resLog2 is the resolution shift (DeltaQRes or DeltaLFRes).
func (td *TileDecoder) readDeltaQOrLF(bc *BoolReader, cdf []uint16, resLog2 uint8) (int, error) {
	sym, err := bc.ReadSymbol(cdf, 4)
	if err != nil {
		return 0, fmt.Errorf("delta_q/lf symbol: %w", err)
	}
	if sym == 3 {
		// Extension coding: read 3 bits for n_bits, then n_bits+1 more bits for value.
		nBitsRaw, err := bc.ReadLiteral(3)
		if err != nil {
			return 0, fmt.Errorf("delta_q/lf n_bits: %w", err)
		}
		nBits := 1 + int(nBitsRaw)
		valRaw, err := bc.ReadLiteral(nBits)
		if err != nil {
			return 0, fmt.Errorf("delta_q/lf ext_val: %w", err)
		}
		sym = int(valRaw) + 1 + (1 << nBits)
	}
	if sym != 0 {
		signBit, err := bc.readBoolEqui()
		if err != nil {
			return 0, fmt.Errorf("delta_q/lf sign: %w", err)
		}
		if signBit {
			sym = -sym
		}
		sym *= 1 << resLog2
	}
	return sym, nil
}

// readSBDeltaQLF reads delta_q and delta_lf for the current SB.
// Called once per SB at the first non-skip block.
// AV1 spec Section 5.11.2 (delta_q) and 5.11.3 (delta_lf).
// Matches dav1d decode.c lines 1006-1060.
func (td *TileDecoder) readSBDeltaQLF(bc *BoolReader, bW, bH int, skip bool) error {
	if !td.fh.DeltaQPresent || td.sbDeltaQDone {
		return nil
	}

	// Check: full SB block that's skip → don't read delta_q.
	sbSize := 32
	if !td.sh.Use128x128Superblock {
		sbSize = 16
	}
	isFullSB := (bW == sbSize && bH == sbSize)
	haveDeltaQ := !isFullSB || !skip

	if haveDeltaQ {
		// Read delta_q_abs.
		_ = td.lastQIdx
		deltaQ, err := td.readDeltaQOrLF(bc, td.cdf.DeltaQ, td.fh.DeltaQRes)
		if err != nil {
			return fmt.Errorf("delta_q: %w", err)
		}
		td.lastQIdx = clamp(td.lastQIdx+deltaQ, 1, 255)

		// Read delta_lf if present.
		if td.fh.DeltaLFPresent {
			nLFs := 1
			if td.fh.DeltaLFMulti {
				nLFs = 4
				if td.sh.ColorConfig.MonoChrome {
					nLFs = 2
				}
			}
			for i := 0; i < nLFs; i++ {
				var cdf []uint16
				if td.fh.DeltaLFMulti {
					cdf = td.cdf.DeltaLFMulti[i]
				} else {
					cdf = td.cdf.DeltaLF
				}
				deltaLF, err := td.readDeltaQOrLF(bc, cdf, td.fh.DeltaLFRes)
				if err != nil {
					return fmt.Errorf("delta_lf[%d]: %w", i, err)
				}
				td.lastDeltaLF[i] = clamp(td.lastDeltaLF[i]+deltaLF, -63, 63)
			}
		}
	}

	td.sbDeltaQDone = true
	return nil
}

// decodeInterBlock decodes one block within an inter frame.
// This handles both intra-in-inter and true inter blocks.
// AV1 spec Section 5.11.5 (mode_info) and 5.11.8 (inter_frame_mode_info).
func (td *TileDecoder) decodeInterBlock(bc *BoolReader, miRow, miCol, bW, bH int, edgeFlags uint8) error {
	nomPixW := bW * 4
	nomPixH := bH * 4

	// --- 1. skip_mode ---
	// AV1 spec Section 5.11.5: skip_mode before skip.
	skipMode := false
	if td.fh.SkipModePresent && min(bW, bH) >= 2 {
		skipModeCtx := td.getSkipModeContext(miRow, miCol)
		sym, err := bc.ReadSymbolBoolInt(td.cdf.SkipMode[skipModeCtx])
		if err != nil {
			return fmt.Errorf("skip_mode at (%d,%d): %w", miRow, miCol, err)
		}
		skipMode = sym == 1
	}
	// --- 2. skip flag ---
	skip := false
	if skipMode {
		skip = true
	} else {
		skipCtx := td.getSkipContext(miRow, miCol)
		skipSym, err := bc.ReadSymbolBoolInt(td.cdf.Skip[skipCtx])
		if err != nil {
			return fmt.Errorf("skip at (%d,%d): %w", miRow, miCol, err)
		}
		skip = skipSym == 1
	}
	// --- 3. CDEF index ---
	if !skip && !td.fh.CodedLossless && td.sh.EnableCDEF && !td.fh.AllowIntraBC && td.fh.CDEFBits > 0 {
		cdefStep := 16
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
	// --- 4. delta_q / delta_lf ---
	// AV1 spec Section 5.11.2/5.11.3: read once per SB at the first block.
	// Matches dav1d decode.c lines 1006-1060.
	if err := td.readSBDeltaQLF(bc, bW, bH, skip); err != nil {
		return fmt.Errorf("delta_q/lf at (%d,%d): %w", miRow, miCol, err)
	}

	// Clamped pixel dimensions for reconstruction writes.
	// Entropy decoding and context updates proceed for the full block
	// within MiRows/MiCols even when pixels extend past the visible frame.
	pixelW := nomPixW
	pixelH := nomPixH
	if miCol*4+pixelW > int(td.fh.FrameWidth) {
		pixelW = int(td.fh.FrameWidth) - miCol*4
	}
	if miRow*4+pixelH > int(td.fh.FrameHeight) {
		pixelH = int(td.fh.FrameHeight) - miRow*4
	}

	subX := int(td.sh.ColorConfig.SubsamplingX)
	subY := int(td.sh.ColorConfig.SubsamplingY)

	// --- 4. is_inter ---
	isInter := false
	if skipMode {
		isInter = true
	} else {
		var err error
		isInter, err = td.readIsInter(bc, miRow, miCol)
		if err != nil {
			return err
		}
	}
	if !isInter {
		// Intra block within inter frame.
		return td.decodeIntraInInterBlock(bc, miRow, miCol, bW, bH, nomPixW, nomPixH, pixelW, pixelH, skip, edgeFlags)
	}

	// --- Inter block ---
	var ref0, ref1 int8
	isComp := false
	var interMode int
	var drlIdx int
	var mv0, mv1 MV
	compType := uint8(COMP_INTER_NONE)
	motionMode := 0 // 0=simple, 1=OBMC, 2=WARP
	iiType := 0     // 0=none, 1=smooth interintra, 2=wedge interintra
	iiMode := 0     // interintra prediction mode (0-3)
	wedgeIdx := 0   // wedge index (0-15)
	maskSign := 0   // mask_sign for wedge/seg compound
	var warpMasks [2]uint64

	if skipMode {
		// skip_mode: use predetermined references and NEARESTMV.
		ref0 = td.fh.SkipModeFrame[0]
		ref1 = td.fh.SkipModeFrame[1]
		isComp = true
		compType = uint8(COMP_INTER_AVG)
		interMode = NEAREST_NEARESTMV

		// Find MV candidates.
		mvstack, _, _ := td.findMVStack(miRow, miCol, bW, bH, ref0, ref1, edgeFlags)
		mv0 = mvstack[0].MV[0]
		mv1 = mvstack[0].MV[1]
		fixMVPrecision(td.fh, &mv0)
		fixMVPrecision(td.fh, &mv1)
	} else {
		// Determine single vs compound reference.
		if td.fh.ReferenceSelect && min(bW, bH) >= 2 {
			compCtx := td.getCompCtx(miRow, miCol)
			compSym, err := bc.ReadSymbolBoolInt(td.cdf.ReferenceMode[compCtx])
			if err != nil {
				return fmt.Errorf("reference_mode at (%d,%d): %w", miRow, miCol, err)
			}
			isComp = compSym == 1
		}
		if isComp {
			// Compound reference frames.
			var err error
			ref0, ref1, err = td.readCompRefFrames(bc, miRow, miCol)
			if err != nil {
				return err
			}

			// Find MV candidates.
			mvstack, nMVs, ctx := td.findMVStack(miRow, miCol, bW, bH, ref0, ref1, edgeFlags)

			// Read compound mode — context is low 3 bits of packed mode context.
			compModeCtx := ctx & 7
			compMode, err := td.readCompoundMode(bc, compModeCtx)
			if err != nil {
				return err
			}
			interMode = compMode
			im := compInterPredModes[compMode]

			// DRL index for compound.
			drlIdx = NEAREST_DRL
			if compMode == NEWMV_NEWMV {
				if nMVs > 1 {
					drlCtx := getDRLContext(mvstack, 0)
					drlBit, err := bc.ReadSymbolBoolInt(td.cdf.DrlMode[drlCtx])
					if err != nil {
						return fmt.Errorf("drl_mode at (%d,%d): %w", miRow, miCol, err)
					}
					drlIdx += drlBit
					if drlIdx == NEARER_DRL && nMVs > 2 {
						drlCtx2 := getDRLContext(mvstack, 1)
						drlBit2, err := bc.ReadSymbolBoolInt(td.cdf.DrlMode[drlCtx2])
						if err != nil {
							return fmt.Errorf("drl_mode at (%d,%d): %w", miRow, miCol, err)
						}
						drlIdx += drlBit2
					}
				}
			} else if im[0] == NEARMV || im[1] == NEARMV {
				drlIdx = NEARER_DRL
				if nMVs > 2 {
					drlCtx := getDRLContext(mvstack, 1)
					drlBit, err := bc.ReadSymbolBoolInt(td.cdf.DrlMode[drlCtx])
					if err != nil {
						return fmt.Errorf("drl_mode at (%d,%d): %w", miRow, miCol, err)
					}
					drlIdx += drlBit
					if drlIdx == NEAR_DRL && nMVs > 3 {
						drlCtx2 := getDRLContext(mvstack, 2)
						drlBit2, err := bc.ReadSymbolBoolInt(td.cdf.DrlMode[drlCtx2])
						if err != nil {
							return fmt.Errorf("drl_mode at (%d,%d): %w", miRow, miCol, err)
						}
						drlIdx += drlBit2
					}
				}
			}


			// Derive MVs for each reference.
			mvPrec := 0
			if td.fh.ForceIntegerMV {
				mvPrec = -1
			} else if td.fh.AllowHighPrecisionMV {
				mvPrec = 1
			}
			for idx := 0; idx < 2; idx++ {
				switch im[idx] {
				case NEARESTMV, NEARMV:
					if idx == 0 {
						mv0 = mvstack[drlIdx].MV[0]
					} else {
						mv1 = mvstack[drlIdx].MV[1]
					}
					if drlIdx < NEAR_DRL {
						if idx == 0 {
							fixMVPrecision(td.fh, &mv0)
						} else {
							fixMVPrecision(td.fh, &mv1)
						}
					}
				case GLOBALMV:
					if idx == 0 {
						mv0 = td.getGlobalMV(ref0, miRow, miCol, bW, bH)
					} else {
						mv1 = td.getGlobalMV(ref1, miRow, miCol, bW, bH)
					}
				case NEWMV:
					if idx == 0 {
						mv0 = mvstack[drlIdx].MV[0]
						if err := td.readMVResidual(bc, &mv0, mvPrec); err != nil {
							return err
						}
					} else {
						mv1 = mvstack[drlIdx].MV[1]
						if err := td.readMVResidual(bc, &mv1, mvPrec); err != nil {
							return err
						}
					}
				}
			}


			// Compound type: simplified — read symbols but use average blend.
			compType = uint8(COMP_INTER_AVG)
			if td.sh.EnableMaskedCompound {
				maskCtx := td.getMaskCompCtx(miRow, miCol)
				isSegWedge, err := bc.ReadSymbolBoolInt(td.cdf.CompGroupIdx[maskCtx])
				if err != nil {
					return fmt.Errorf("comp_group_idx at (%d,%d): %w", miRow, miCol, err)
				}
				if isSegWedge == 0 {
					// jnt_comp or average
					if td.sh.EnableJNTComp {
						jntCtx := td.getJntCompCtx(miRow, miCol, ref0, ref1)
						jntBit, err := bc.ReadSymbolBoolInt(td.cdf.CompoundIdx[jntCtx])
						if err != nil {
							return fmt.Errorf("compound_idx at (%d,%d): %w", miRow, miCol, err)
						}
						// jntBit=0 → COMP_INTER_WEIGHTED_AVG, jntBit=1 → COMP_INTER_AVG
						compType = uint8(COMP_INTER_WEIGHTED_AVG) + uint8(jntBit)
					} else {
						compType = uint8(COMP_INTER_AVG)
					}
					// compType already set above
				} else {
					// Wedge or seg compound.
					bsz := blockSizeEnum(nomPixW, nomPixH)
					if bsz < 22 && isWedgeAllowed(bsz) {
						wedgeBit, err := bc.ReadSymbolBoolInt(td.cdf.CompoundType[bsz])
						if err != nil {
							return fmt.Errorf("compound_type at (%d,%d): %w", miRow, miCol, err)
						}
						if wedgeBit == 0 {
							// Wedge compound: read index.
							compType = uint8(COMP_INTER_WEDGE)
							idx, err := bc.ReadSymbol(td.cdf.WedgeIndex[bsz], 16)
							if err != nil {
								return fmt.Errorf("wedge_index at (%d,%d): %w", miRow, miCol, err)
							}
							wedgeIdx = idx
						} else {
							compType = uint8(COMP_INTER_SEG)
						}
					} else {
						// Wedge not allowed: force SEG (diffwtd).
						compType = uint8(COMP_INTER_SEG)
					}
					// mask_sign (1 bit equi-prob)
					ms, err := bc.ReadLiteral(1)
					if err != nil {
						return fmt.Errorf("mask_sign at (%d,%d): %w", miRow, miCol, err)
					}
					maskSign = int(ms)
				}
			}
		} else {
			// Single reference frame.
			ref1 = -1
			var err error
			ref0, err = td.readSingleRefFrames(bc, miRow, miCol)
			if err != nil {
				return err
			}
			// Find MV candidates.
			mvstack, nMVs, ctx := td.findMVStack(miRow, miCol, bW, bH, ref0, -1, edgeFlags)

			// Read inter mode.
			interMode, drlIdx, err = td.readInterMode(bc, ctx, nMVs, mvstack)
			if err != nil {
				return err
			}
			// Derive MV.
			mvPrec := 0
			if td.fh.ForceIntegerMV {
				mvPrec = -1
			} else if td.fh.AllowHighPrecisionMV {
				mvPrec = 1
			}

			switch interMode {
			case GLOBALMV:
				mv0 = td.getGlobalMV(ref0, miRow, miCol, bW, bH)
			case NEARESTMV:
				mv0 = mvstack[drlIdx].MV[0]
				fixMVPrecision(td.fh, &mv0)
			case NEARMV:
				mv0 = mvstack[drlIdx].MV[0]
				if drlIdx < NEAR_DRL {
					fixMVPrecision(td.fh, &mv0)
				}
			case NEWMV:
				if nMVs > 1 {
					mv0 = mvstack[drlIdx].MV[0]
				} else {
					mv0 = mvstack[0].MV[0]
					fixMVPrecision(td.fh, &mv0)
				}
				if err := td.readMVResidual(bc, &mv0, mvPrec); err != nil {
					return err
				}
			}
			// Inter-intra: read symbols to keep entropy state correct.
			iiAllowed := td.sh.EnableInterIntraCompound && isInterIntraAllowed(nomPixW, nomPixH)
			iiType = 0 // INTER_INTRA_NONE
			if iiAllowed {
				iiGrp := yModeSizeGroup(bW, bH)
				iiBit, err := bc.ReadSymbolBoolInt(td.cdf.InterIntra[iiGrp])
				if err != nil {
					return fmt.Errorf("interintra at (%d,%d): %w", miRow, miCol, err)
				}
				if iiBit == 1 {
					iiType = 1
					// Read interintra mode.
					iiModeVal, err := bc.ReadSymbol(td.cdf.InterIntraMode[iiGrp], 4)
					if err != nil {
						return fmt.Errorf("interintra_mode at (%d,%d): %w", miRow, miCol, err)
					}
					iiMode = iiModeVal
					// Read wedge.
					bsz := blockSizeEnum(nomPixW, nomPixH)
					if bsz < 22 {
						wedgeBit, err := bc.ReadSymbolBoolInt(td.cdf.WedgeInterIntra[bsz])
						if err != nil {
							return fmt.Errorf("wedge_interintra at (%d,%d): %w", miRow, miCol, err)
						}
						if wedgeBit == 1 {
							iiType = 2
							wedgeIdxVal, err := bc.ReadSymbol(td.cdf.WedgeIndex[bsz], 16)
							if err != nil {
								return fmt.Errorf("wedge_idx at (%d,%d): %w", miRow, miCol, err)
							}
							wedgeIdx = wedgeIdxVal
						}
					}
				}
			}

			// Motion mode: read motion_mode or obmc symbol.
			// MM_WARP = 2 suppresses subpel filter reading (dav1d decode.c line 1998).
			if iiType == 0 && td.fh.IsMotionModeSwitchable && bW >= 2 && bH >= 2 &&
				!(interMode == GLOBALMV && !td.fh.ForceIntegerMV &&
					td.fh.GmType[ref0] > GM_TRANSLATION) {
				// Check for overlappable (inter) neighbors using findoddzero equivalent.
				hasOverlap := td.hasOverlappableNeighbors(miRow, miCol, bW, bH, edgeFlags)
				if hasOverlap {
					bsz := blockSizeEnum(nomPixW, nomPixH)
					if bsz < 22 {
						// Check if warp is allowed.
						// dav1d's dav1d_find_matching_ref() checks for matching refs
						// AND derives warp model with get_shear_params() validation.
						// Warp is allowed only if both checks pass.
						// Check if warp is allowed.
						// dav1d: use 3-symbol MotionMode CDF when any matching-ref
						// neighbor exists (dav1d_find_matching_ref > 0).
						allowWarp := !td.fh.ForceIntegerMV && td.fh.AllowWarpedMotion &&
							td.hasMatchingRef(miRow, miCol, bW, bH, ref0, edgeFlags)
						if allowWarp {
							mmVal, err := bc.ReadSymbol(td.cdf.MotionMode[bsz], 3)
							if err != nil {
								return fmt.Errorf("motion_mode at (%d,%d): %w", miRow, miCol, err)
							}
							motionMode = mmVal
						} else {
							// Just OBMC flag — uses separate OBMC CDF (not MotionMode).
							obmcVal, err := bc.ReadSymbolBoolInt(td.cdf.OBMC[bsz])
							if err != nil {
								return fmt.Errorf("obmc at (%d,%d): %w", miRow, miCol, err)
							}
							motionMode = obmcVal
						}
					}
				}
			}
		}
	}


	// --- Interpolation filter ---
	filterTypeH := td.fh.InterpFilter // horizontal filter
	filterTypeV := td.fh.InterpFilter // vertical filter (may differ when dual filter enabled)
	if td.fh.IsFilterSwitchable {
		hasSubpelFilter := true
		// skip_mode blocks do NOT read the interpolation filter.
		// dav1d decode.c: has_subpel_filter = 0 in the skip_mode path.
		if skipMode {
			hasSubpelFilter = false
		}
		if !isComp && interMode == GLOBALMV && bW >= 2 && bH >= 2 {
			hasSubpelFilter = (td.fh.GmType[ref0] == GM_TRANSLATION)
		}
		if isComp && interMode == GLOBALMV_GLOBALMV {
			hasSubpelFilter = (bW < 2 || bH < 2)
		}
		// MM_WARP (motion_mode == 2) suppresses subpel filter reading.
		// dav1d decode.c line 1998: if (b->motion_mode == MM_WARP) has_subpel_filter = 0;
		if motionMode == 2 {
			hasSubpelFilter = false
		}
		if hasSubpelFilter {
			// Read filter for dimension 0 (horizontal).
			fCtx0 := td.getFilterCtx(miRow, miCol, isComp, 0, ref0)
			f0, err := bc.ReadSymbol(td.cdf.SwitchableFilter[0][fCtx0], 3)
			if err != nil {
				return fmt.Errorf("interp_filter[0] at (%d,%d): %w", miRow, miCol, err)
			}
			// dav1d: filter[0] = vertical, filter[1] = horizontal
			// (from dav1d_filter_2d[filter[1]/*h*/][filter[0]/*v*/])
			filterTypeV = f0
			filterTypeH = f0
			if td.sh.EnableDualFilter {
				// Read filter for dimension 1 (horizontal).
				fCtx1 := td.getFilterCtx(miRow, miCol, isComp, 1, ref0)
				f1, err := bc.ReadSymbol(td.cdf.SwitchableFilter[1][fCtx1], 3)
				if err != nil {
					return fmt.Errorf("interp_filter[1] at (%d,%d): %w", miRow, miCol, err)
				}
				filterTypeH = f1
			}
		} else {
			filterTypeH = InterpFilterEighttapRegular
			filterTypeV = InterpFilterEighttapRegular
		}
	}


	// --- TX size (variable TX tree for inter blocks) ---
	// AV1 spec Section 5.11.38: inter blocks use read_var_tx_size() which
	// recursively reads binary txfm_split flags, NOT the multi-symbol
	// tx_depth CDF used by intra blocks.
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
					return fmt.Errorf("var_tx_size at (%d,%d): %w", miRow+y4, miCol+x4, err)
				}
				varBlocks = append(varBlocks, blocks...)
			}
		}
		// Per-leaf tx[] context is set inside readVarTxSize.
		// Also set tx_intra[] to block dim log2 (dav1d line 1964).
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
		// dav1d stores BLOCK dims in tx_intra (line 1389).
		td.setTxSizeCtxBlock(miRow, miCol, bW, bH)
		// dav1d stores to tx[] for non-vartx switchable mode (lines 461-467).
		if skip {
			// Skip: store block dim log2 in tx[].
			td.setVarTxCtxBlock(miRow, miCol, bW, bH)
		} else {
			// Lossless or TX_4X4: store TX_4X4 (=0) in tx[].
			td.setVarTxCtxVal(miRow, miCol, bW, bH, 0)
		}
	}

	// Clear palette info for inter blocks.
	td.setPaletteInfo(miRow, miCol, bW, bH, 0, 0, nil)

	// --- Chroma TX size ---
	nomChromaW := nomPixW >> subX
	nomChromaH := nomPixH >> subY
	if nomChromaW < 4 {
		nomChromaW = 4
	}
	if nomChromaH < 4 {
		nomChromaH = 4
	}
	chromaTxSz := adjustUVTxSize(blockSizeToTxSize(nomChromaW, nomChromaH))
	// AV1 spec: in lossless mode, all TX sizes are TX_4X4.
	if td.fh.CodedLossless {
		chromaTxSz = TX_4X4
	}
	chromaMiRow := miRow >> subY
	chromaMiCol := miCol >> subX

	// HasChroma check.
	hasChroma := true
	if subX == 1 && !(miCol&1 == 1 || bW >= 2) {
		hasChroma = false
	}
	if subY == 1 && !(miRow&1 == 1 || bH >= 2) {
		hasChroma = false
	}

	// --- Motion compensation ---
	// Get reference frame buffer.
	refIdx := td.fh.RefFrameIdx[ref0]
	refFrame := td.refFrames[refIdx]
	// Allocate prediction buffers.
	predY := make([]byte, nomPixW*nomPixH)
	var predU, predV []byte
	if hasChroma {
		predU = make([]byte, nomChromaW*nomChromaH)
		predV = make([]byte, nomChromaW*nomChromaH)
	}

	if refFrame != nil {
		chromaRefW := (refFrame.Width + subX) >> subX
		chromaRefH := (refFrame.Height + subY) >> subY

		// Check global motion warp eligibility per reference frame.
		// dav1d: gmv_warp_allowed[ref] = gm_type > TRANSLATION && !force_integer_mv
		//        && getShearParams succeeds && not scaled.
		// We don't support scaling, so skip that check.
		gmvWarpAllowed := func(refType int8) bool {
			if refType < 0 || int(refType) >= len(td.fh.GmType) {
				return false
			}
			if td.fh.GmType[refType] <= GM_TRANSLATION {
				return false
			}
			if td.fh.ForceIntegerMV {
				return false
			}
			// Check shear params. Copy GM params into a WarpParams and validate.
			var gm WarpParams
			gm.Mat = td.fh.GmParams[refType]
			return getShearParams(&gm)
		}

		// Decide between warp MC and regular MC.
		useWarp := false
		var warpParams WarpParams
		if motionMode == 2 {
			warpMasks = td.findMatchingRefMasks(miRow, miCol, bW, bH, ref0, edgeFlags)
			warpParams = td.deriveWarpMV(miRow, miCol, bW, bH, warpMasks, mv0)
			useWarp = warpParams.Valid
		}
		// Single-ref GLOBALMV warp: dav1d uses warp_affine for non-compound
		// GLOBALMV when gmv_warp_allowed and min(bw4,bh4) > 1.
		if !isComp && !useWarp && interMode == GLOBALMV && bW >= 2 && bH >= 2 && gmvWarpAllowed(ref0) {
			var gm WarpParams
			gm.Mat = td.fh.GmParams[ref0]
			getShearParams(&gm)
			warpParams = gm
			useWarp = true
		}

		if useWarp {
			// Warped motion compensation for luma.
			applyWarpedMotion(predY, nomPixW, nomPixH,
				refFrame.Y, refFrame.StrideY, refFrame.Width, refFrame.Height,
				miCol, miRow, &warpParams,
				nil, nil, 0, 0, nil, 0, nil, 0, 0, 0, 0, 0)
			// Chroma: dav1d only uses warp for chroma when min(cbw4, cbh4) > 1.
			// Otherwise, falls back to regular translation MC.
			cbw4 := (bW + subX) >> subX
			cbh4 := (bH + subY) >> subY
			if hasChroma {
				if cbw4 > 1 && cbh4 > 1 {
					applyWarpedMotion(nil, 0, 0,
						nil, 0, 0, 0,
						miCol, miRow, &warpParams,
						predU, predV, nomChromaW, nomChromaH,
						refFrame.U, refFrame.StrideU,
						refFrame.V, refFrame.StrideV,
						chromaRefW, chromaRefH, subX, subY)
				} else {
					td.chromaMC(predU, predV, nomChromaW, nomChromaH,
						miRow, miCol, bW, bH, mv0, ref0,
						refFrame, chromaRefW, chromaRefH,
						filterTypeH, filterTypeV, subX, subY)
				}
			}
		} else if isComp && ref1 >= 0 {
			// Compound prediction: use intermediate-precision MC (prep) for both refs,
			// blend in int16 space, then clip to byte. Matches dav1d's prep_8tap + avg/w_avg.
			// dav1d: for GLOBALMV_GLOBALMV, each ref independently uses warp_affine
			// if gmv_warp_allowed, otherwise regular prep MC.
			ref1Idx := td.fh.RefFrameIdx[ref1]
			ref1Frame := td.refFrames[ref1Idx]
			if ref1Frame != nil {
				// Prep MC for ref0 (int16 at 16x scale).
				prep0Y := make([]int16, nomPixW*nomPixH)
				if interMode == GLOBALMV_GLOBALMV && gmvWarpAllowed(ref0) {
					var gm WarpParams
					gm.Mat = td.fh.GmParams[ref0]
					getShearParams(&gm)
					applyWarpedMotionPrep(prep0Y, nomPixW, nomPixH,
						refFrame.Y, refFrame.StrideY, refFrame.Width, refFrame.Height,
						miCol, miRow, &gm)
				} else {
					motionCompensationPrep(prep0Y, nomPixW, nomPixH,
						refFrame.Y, refFrame.StrideY, refFrame.Width, refFrame.Height,
						miCol*4, miRow*4, mv0, filterTypeH, filterTypeV)
				}

				// Prep MC for ref1.
				prep1Y := make([]int16, nomPixW*nomPixH)
				if interMode == GLOBALMV_GLOBALMV && gmvWarpAllowed(ref1) {
					var gm WarpParams
					gm.Mat = td.fh.GmParams[ref1]
					getShearParams(&gm)
					applyWarpedMotionPrep(prep1Y, nomPixW, nomPixH,
						ref1Frame.Y, ref1Frame.StrideY, ref1Frame.Width, ref1Frame.Height,
						miCol, miRow, &gm)
				} else {
					motionCompensationPrep(prep1Y, nomPixW, nomPixH,
						ref1Frame.Y, ref1Frame.StrideY, ref1Frame.Width, ref1Frame.Height,
						miCol*4, miRow*4, mv1, filterTypeH, filterTypeV)
				}

				// Blend in int16 space, then clip to byte.
				// dav1d 8-bit: intermediate_bits=4, PREP_BIAS=0.
				// For mask-based blending (WEDGE/SEG), mask_sign swaps which prep is tmp1/tmp2.
				var segMask []uint8 // for COMP_INTER_SEG: stores chroma mask
				switch compType {
				case uint8(COMP_INTER_WEIGHTED_AVG):
					// w_avg: (tmp0*w + tmp1*(16-w) + 128) >> 8
					w := td.jntWeights[ref0][ref1]
					for i := range predY {
						predY[i] = clipU8(int32(int(prep0Y[i])*w+int(prep1Y[i])*(16-w)+128) >> 8)
					}
				case uint8(COMP_INTER_WEDGE):
					// Wedge: use precomputed mask. mask_sign swaps tmp1/tmp2.
					mask := getWedgeMask(nomPixW, nomPixH, wedgeIdx)
					t0, t1 := prep0Y, prep1Y
					if maskSign != 0 {
						t0, t1 = prep1Y, prep0Y
					}
					for i := range predY {
						m := int(mask[i])
						predY[i] = clipU8(int32(int(t0[i])*m+int(t1[i])*(64-m)+512) >> 10)
					}
				case uint8(COMP_INTER_SEG):
					// Diffwtd: compute mask from prediction difference, blend.
					// dav1d w_mask: tmp[mask_sign] is tmp1, tmp[!mask_sign] is tmp2.
					// 8-bit: intermediate_bits=4, bitdepth=8.
					//   mask_sh = bitdepth + intermediate_bits - 4 = 8
					//   mask_rnd = 1 << (mask_sh - 5) = 8
					//   Blend: (tmpdiff*m + tmp2*64 + 512) >> 10
					t0, t1 := prep0Y, prep1Y
					if maskSign != 0 {
						t0, t1 = prep1Y, prep0Y
					}
					lumaMask := make([]uint8, nomPixW*nomPixH)
					for i := range predY {
						diff := int(t0[i]) - int(t1[i])
						if diff < 0 {
							diff = -diff
						}
						m := 38 + ((diff + 8) >> 8)
						if m > 64 {
							m = 64
						}
						lumaMask[i] = uint8(m)
						predY[i] = clipU8(int32((int(t0[i])-int(t1[i]))*m+int(t1[i])*64+512) >> 10)
					}
					// Downsample luma mask to 420 chroma for later use.
					segMask = make([]uint8, nomChromaW*nomChromaH)
					di := 0
					for y := 0; y < nomPixH; y += 2 {
						for x := 0; x < nomPixW; x += 2 {
							sum := int(lumaMask[y*nomPixW+x]) + int(lumaMask[y*nomPixW+x+1]) +
								int(lumaMask[(y+1)*nomPixW+x]) + int(lumaMask[(y+1)*nomPixW+x+1]) + 2
							segMask[di] = uint8((sum - maskSign) >> 2)
							di++
						}
					}
				default:
					// avg: (tmp0 + tmp1 + 16) >> 5
					for i := range predY {
						predY[i] = clipU8(int32(prep0Y[i]+prep1Y[i]+16) >> 5)
					}
				}

				if hasChroma {
					chromaRef1W := (ref1Frame.Width + subX) >> subX
					chromaRef1H := (ref1Frame.Height + subY) >> subY

					chromaMV0Col := int32(mv0.Col)
					chromaMV0Row := int32(mv0.Row)
					chromaMV1Col := int32(mv1.Col)
					chromaMV1Row := int32(mv1.Row)

					prep0U := make([]int16, nomChromaW*nomChromaH)
					prep0V := make([]int16, nomChromaW*nomChromaH)
					prep1U := make([]int16, nomChromaW*nomChromaH)
					prep1V := make([]int16, nomChromaW*nomChromaH)

					motionCompensationChromaPrep(prep0U, nomChromaW, nomChromaH,
						refFrame.U, refFrame.StrideU, chromaRefW, chromaRefH,
						chromaMiCol*4, chromaMiRow*4, chromaMV0Col, chromaMV0Row, filterTypeH, filterTypeV,
						subX, subY)
					motionCompensationChromaPrep(prep0V, nomChromaW, nomChromaH,
						refFrame.V, refFrame.StrideV, chromaRefW, chromaRefH,
						chromaMiCol*4, chromaMiRow*4, chromaMV0Col, chromaMV0Row, filterTypeH, filterTypeV,
						subX, subY)
					motionCompensationChromaPrep(prep1U, nomChromaW, nomChromaH,
						ref1Frame.U, ref1Frame.StrideU, chromaRef1W, chromaRef1H,
						chromaMiCol*4, chromaMiRow*4, chromaMV1Col, chromaMV1Row, filterTypeH, filterTypeV,
						subX, subY)
					motionCompensationChromaPrep(prep1V, nomChromaW, nomChromaH,
						ref1Frame.V, ref1Frame.StrideV, chromaRef1W, chromaRef1H,
						chromaMiCol*4, chromaMiRow*4, chromaMV1Col, chromaMV1Row, filterTypeH, filterTypeV,
						subX, subY)

					switch compType {
					case uint8(COMP_INTER_WEIGHTED_AVG):
						w := td.jntWeights[ref0][ref1]
						for i := range predU {
							predU[i] = clipU8(int32(int(prep0U[i])*w+int(prep1U[i])*(16-w)+128) >> 8)
						}
						for i := range predV {
							predV[i] = clipU8(int32(int(prep0V[i])*w+int(prep1V[i])*(16-w)+128) >> 8)
						}
					case uint8(COMP_INTER_WEDGE):
						cmask := getWedgeMaskChroma420(nomPixW, nomPixH, wedgeIdx, maskSign)
						t0U, t1U := prep0U, prep1U
						t0V, t1V := prep0V, prep1V
						if maskSign != 0 {
							t0U, t1U = prep1U, prep0U
							t0V, t1V = prep1V, prep0V
						}
						for i := range predU {
							m := int(cmask[i])
							predU[i] = clipU8(int32(int(t0U[i])*m+int(t1U[i])*(64-m)+512) >> 10)
						}
						for i := range predV {
							m := int(cmask[i])
							predV[i] = clipU8(int32(int(t0V[i])*m+int(t1V[i])*(64-m)+512) >> 10)
						}
					case uint8(COMP_INTER_SEG):
						// Use the segMask computed during luma blending.
						t0U, t1U := prep0U, prep1U
						t0V, t1V := prep0V, prep1V
						if maskSign != 0 {
							t0U, t1U = prep1U, prep0U
							t0V, t1V = prep1V, prep0V
						}
						for i := range predU {
							m := int(segMask[i])
							predU[i] = clipU8(int32(int(t0U[i])*m+int(t1U[i])*(64-m)+512) >> 10)
						}
						for i := range predV {
							m := int(segMask[i])
							predV[i] = clipU8(int32(int(t0V[i])*m+int(t1V[i])*(64-m)+512) >> 10)
						}
					default:
						for i := range predU {
							predU[i] = clipU8(int32(prep0U[i]+prep1U[i]+16) >> 5)
						}
						for i := range predV {
							predV[i] = clipU8(int32(prep0V[i]+prep1V[i]+16) >> 5)
						}
					}
				}
			} else {
				// ref1Frame is nil, fall back to single-ref put MC for ref0.
				motionCompensation(predY, nomPixW, nomPixH,
					refFrame.Y, refFrame.StrideY, refFrame.Width, refFrame.Height,
					miCol*4, miRow*4, mv0, filterTypeH, filterTypeV)
				if hasChroma {
					chromaMVCol := int32(mv0.Col)
					chromaMVRow := int32(mv0.Row)
					motionCompensationChroma(predU, nomChromaW, nomChromaH,
						refFrame.U, refFrame.StrideU, chromaRefW, chromaRefH,
						chromaMiCol*4, chromaMiRow*4, chromaMVCol, chromaMVRow, filterTypeH, filterTypeV,
						subX, subY)
					motionCompensationChroma(predV, nomChromaW, nomChromaH,
						refFrame.V, refFrame.StrideV, chromaRefW, chromaRefH,
						chromaMiCol*4, chromaMiRow*4, chromaMVCol, chromaMVRow, filterTypeH, filterTypeV,
						subX, subY)
				}
			}
		} else {
			// Single-reference: regular translation MC to byte output.
			motionCompensation(predY, nomPixW, nomPixH,
				refFrame.Y, refFrame.StrideY, refFrame.Width, refFrame.Height,
				miCol*4, miRow*4, mv0, filterTypeH, filterTypeV)

				if hasChroma {
				td.chromaMC(predU, predV, nomChromaW, nomChromaH,
					miRow, miCol, bW, bH, mv0, ref0,
					refFrame, chromaRefW, chromaRefH,
					filterTypeH, filterTypeV, subX, subY)
			}

			// OBMC: blend overlapping neighbor predictions after regular MC.
			if motionMode == 1 {
				td.applyOBMC(predY, nomPixW, nomPixH,
					predU, predV, nomChromaW, nomChromaH,
					miRow, miCol, bW, bH, filterTypeH, filterTypeV,
					hasChroma, subX, subY)
			}

		}

		// Inter-Intra: blend MC prediction with intra prediction.
		if iiType != 0 {
			td.applyInterIntra(predY, nomPixW, nomPixH,
				predU, predV, nomChromaW, nomChromaH,
				miRow, miCol, bW, bH,
				iiType, iiMode, wedgeIdx,
				hasChroma, subX, subY)
		}
	}


	// --- Coefficient decoding and reconstruction ---
	interHasNonZero := false
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
		interHasNonZero = sliceHasNonZero(yRes) || sliceHasNonZero(uRes) || sliceHasNonZero(vRes)

		td.reconstructInterPlaneSpatial(miRow, miCol, nomPixW, nomPixH, 0, predY, yRes)
		if hasChroma {
			td.reconstructInterPlaneSpatial(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 1, predU, uRes)
			td.reconstructInterPlaneSpatial(chromaMiRow, chromaMiCol, nomChromaW, nomChromaH, 2, predV, vRes)

		}
	}




	// --- Update context ---
	modeVal := uint8(interMode)
	// Compute MF flags matching dav1d's refmvs_block.mf:
	//   bit 0: GLOBALMV substitution (replace neighbor MV with current block's GMV)
	//   bit 1: has-NEWMV flag
	var mfVal uint8
	if !isComp {
		// Single: mf = (mode==GLOBALMV && min(bw4,bh4)>=2) | ((mode==NEWMV) * 2)
		minDim := bW
		if bH < minDim {
			minDim = bH
		}
		if interMode == GLOBALMV && minDim >= 2 {
			mfVal |= 1
		}
		if interMode == NEWMV {
			mfVal |= 2
		}
	} else {
		// Compound: mf = (mode==GLOBALMV_GLOBALMV) | !!((1<<mode) & 0xbc) * 2
		if interMode == GLOBALMV_GLOBALMV {
			mfVal |= 1
		}
		if (1<<interMode)&0xbc != 0 {
			mfVal |= 2
		}
	}
	info := ModeInfo{
		IsIntra:    false,
		RefFrame:   [2]int8{ref0, ref1},
		MV:         [2]MV{mv0, mv1},
		Mode:       modeVal,
		CompType:   compType,
		Skip:       skip,
		SkipMode:   skipMode,
		Filter:     [2]uint8{uint8(filterTypeV), uint8(filterTypeH)},
		BW4:        uint8(bW),
		BH4:        uint8(bH),
		MotionMode: uint8(motionMode),
		IIType:     uint8(iiType),
		MF:         mfVal,
	}
	td.setInterModeInfo(miRow, miCol, bW, bH, info)

	// Populate deblockInfo for in-loop filtering using per-leaf TX sizes.
	// Chroma TX size is block-level (not per-leaf), matching dav1d which
	// passes b->uvtx to mask_edges_chroma for the entire coding block.
	if td.deblockInfo != nil {
		blockUvTxW, blockUvTxH := TxSizeDimensions(chromaTxSz)
		if blockUvTxW < 4 {
			blockUvTxW = 4
		}
		if blockUvTxH < 4 {
			blockUvTxH = 4
		}
		for _, blk := range varBlocks {
			txW, txH := TxSizeDimensions(blk.txSz)
			if txW <= 0 {
				txW = 4
			}
			if txH <= 0 {
				txH = 4
			}
			blkW4 := max(txW>>2, 1)
			blkH4 := max(txH>>2, 1)
			dInfo := DeblockInfo{
				IsInter:        true,
				RefFrame:       ref0,
				Mode:           modeVal,
				IsGlobalMV:     (!isComp && interMode == GLOBALMV) || (isComp && interMode == GLOBALMV_GLOBALMV),
				Skip:           skip,
				HasNonZero:     interHasNonZero,
				TxW:            txW,
				TxH:            txH,
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

	// Set UV mode to DC_PRED for inter blocks (matches dav1d lines 2074-2076).
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

	// Also update legacy intra-mode context (used for partition context).
	// Use T-split partition context overrides if set.
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
		td.aboveModes[c] = int(modeVal)
		if c < len(td.abovePartCtx) {
			td.abovePartCtx[c] = aboveCtx
		}
		if c < len(td.aboveSkip) {
			td.aboveSkip[c] = skip
		}
	}
	for r := miRow; r < miRow+bH && r < len(td.leftModes); r++ {
		td.leftModes[r] = int(modeVal)
		if r < len(td.leftPartCtx) {
			td.leftPartCtx[r] = leftCtx
		}
		if r < len(td.leftSkip) {
			td.leftSkip[r] = skip
		}
	}

	return nil
}

// decodeIntraInInterBlock handles an intra-coded block within an inter frame.
// The mode syntax is different from keyframe (uses YMode CDF instead of IntraFrameYMode).
func (td *TileDecoder) decodeIntraInInterBlock(bc *BoolReader, miRow, miCol, bW, bH, nomPixW, nomPixH, pixelW, pixelH int, skip bool, edgeFlags uint8) error {
	subX := int(td.sh.ColorConfig.SubsamplingX)
	subY := int(td.sh.ColorConfig.SubsamplingY)

	// Y mode: use YMode CDF (size group based), not IntraFrameYMode.
	szGrp := yModeSizeGroup(bW, bH)
	yMode, err := bc.ReadSymbol(td.cdf.YMode[szGrp], NumIntraModes)
	if err != nil {
		return fmt.Errorf("y_mode at (%d,%d): %w", miRow, miCol, err)
	}

	// Y angle delta.
	angleDelta := 0
	if yMode >= V_PRED && yMode <= D67_PRED && bW*bH >= 4 {
		sym, err := bc.ReadSymbol(td.cdf.AngleDelta[yMode-V_PRED], 7)
		if err != nil {
			return fmt.Errorf("angle_delta_y at (%d,%d): %w", miRow, miCol, err)
		}
		angleDelta = sym - 3
	}
	// HasChroma check.
	hasChroma := true
	if subX == 1 && !(miCol&1 == 1 || bW >= 2) {
		hasChroma = false
	}
	if subY == 1 && !(miRow&1 == 1 || bH >= 2) {
		hasChroma = false
	}

	// UV mode.
	uvMode := DC_PRED
	uvAngleDelta := 0
	cflAlphaU := 0
	cflAlphaV := 0
	if hasChroma {
		cflAllowed := 0
		if td.fh.CodedLossless {
			// In lossless mode, CFL is allowed only for 4x4 chroma blocks (1x1 MI).
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

		// CfL alpha.
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

		// UV angle delta.
		if uvMode >= V_PRED && uvMode <= D67_PRED && bW*bH >= 4 {
			sym, err := bc.ReadSymbol(td.cdf.AngleDelta[uvMode-V_PRED], 7)
			if err != nil {
				return fmt.Errorf("angle_delta_uv at (%d,%d): %w", miRow, miCol, err)
			}
			uvAngleDelta = sym - 3
		}
	}



	// --- palette_mode_info ---
	// AV1 spec Section 5.11.42. Same as keyframe path.
	paletteSizeY := 0
	var paletteY []uint8
	paletteAllowed := td.fh.AllowScreenContentTools && bW+bH >= 4 && nomPixW <= 64 && nomPixH <= 64
	paletteSizeUV := 0
	var paletteU, paletteV []uint8
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

	// --- filter_intra_mode_info ---
	yModeForTx := yMode
	filterIntraMode := -1
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
				filterModeToYMode := [5]int{DC_PRED, V_PRED, H_PRED, D157_PRED, DC_PRED}
				yModeForTx = filterModeToYMode[fiMode]
			}
		}
	}
	_ = filterIntraMode

	// --- palette indices ---
	var palIdxY []uint8
	var palIdxUV []uint8
	if paletteSizeY > 0 {
		scanW4, scanH4 := bW, bH
		if miCol+scanW4 > int(td.fh.MiCols) {
			scanW4 = int(td.fh.MiCols) - miCol
		}
		if miRow+scanH4 > int(td.fh.MiRows) {
			scanH4 = int(td.fh.MiRows) - miRow
		}
		palIdxY, err = td.readPaletteIndices(bc, 0, paletteSizeY, scanW4, scanH4, bW, bH)
		if err != nil {
			return fmt.Errorf("pal_idx_y at (%d,%d): %w", miRow, miCol, err)
		}
	}
	if paletteSizeUV > 0 && hasChroma {
		outCW4 := (bW + subX) >> subX
		outCH4 := (bH + subY) >> subY
		scanCW4, scanCH4 := outCW4, outCH4
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
		palIdxUV, err = td.readPaletteIndices(bc, 1, paletteSizeUV, scanCW4, scanCH4, outCW4, outCH4)
		if err != nil {
			return fmt.Errorf("pal_idx_uv at (%d,%d): %w", miRow, miCol, err)
		}
	}
	// TX size.
	maxRectTx := blockSizeToTxSize(nomPixW, nomPixH)
	lumaTxSz := maxRectTx
	// AV1 spec: in lossless mode, TX size is always TX_4X4.
	if td.fh.CodedLossless {
		lumaTxSz = TX_4X4
	}
	if td.fh.TxMode == TxModeSelect_ && !td.fh.CodedLossless && !(bW == 1 && bH == 1) {
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

	td.setTxSizeCtx(miRow, miCol, bW, bH, lumaTxSz)
	palColorsMap := map[int][]uint8{}
	if paletteSizeY > 0 {
		palColorsMap[0] = paletteY
	}
	if paletteSizeUV > 0 {
		palColorsMap[1] = paletteU
		palColorsMap[2] = paletteV
	}
	td.setPaletteInfo(miRow, miCol, bW, bH, paletteSizeY, paletteSizeUV, palColorsMap)

	nomChromaW := nomPixW >> subX
	nomChromaH := nomPixH >> subY
	if nomChromaW < 4 {
		nomChromaW = 4
	}
	if nomChromaH < 4 {
		nomChromaH = 4
	}
	chromaTxSz := adjustUVTxSize(blockSizeToTxSize(nomChromaW, nomChromaH))
	// AV1 spec: in lossless mode, all TX sizes are TX_4X4.
	if td.fh.CodedLossless {
		chromaTxSz = TX_4X4
	}
	chromaMiRow := miRow >> subY
	chromaMiCol := miCol >> subX

	if skip {
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
		yCoeffs, uCoeffs, vCoeffs, err := td.decodeCoeffsInterleaved(bc,
			nomPixW, nomPixH, lumaTxSz, yModeForTx, miRow, miCol,
			nomChromaW, nomChromaH, chromaTxSz, uvMode, chromaMiRow, chromaMiCol,
			hasChroma, subX, subY, false)
		if err != nil {
			yCoeffs = nil
			uCoeffs = nil
			vCoeffs = nil
		}
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


	// Update context as intra.
	td.setModeInfo(miRow, miCol, bW, bH, yMode, uvMode, skip, hasChroma)

	// Populate deblockInfo for in-loop filtering.
	if td.deblockInfo != nil {
		txW, txH := TxSizeDimensions(lumaTxSz)
		if txW <= 0 {
			txW = 4
		}
		if txH <= 0 {
			txH = 4
		}
		uvTxW, uvTxH := TxSizeDimensions(chromaTxSz)
		if uvTxW == 0 {
			uvTxW = nomChromaW
		}
		if uvTxH == 0 {
			uvTxH = nomChromaH
		}
		dInfo := DeblockInfo{
			IsInter:        false,
			RefFrame:       -1,
			Mode:           uint8(yMode),
			Skip:           skip,
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
				td.deblockInfo[r][c] = dInfo
			}
		}
	}

	return nil
}

// writeInterPredToFrame writes a prediction buffer to the frame buffer.
// Writes to the full buffer (including padding), matching dav1d.
func (td *TileDecoder) writeInterPredToFrame(miRow, miCol, w, h, plane int, pred []byte) {
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

	bufH := len(buf) / stride
	bufW := stride

	baseX := miCol * 4
	baseY := miRow * 4

	for dy := 0; dy < h; dy++ {
		py := baseY + dy
		if py >= bufH {
			break
		}
		for dx := 0; dx < w; dx++ {
			px := baseX + dx
			if px >= bufW {
				break
			}
			buf[py*stride+px] = pred[dy*w+dx]
		}
	}
}

// reconstructInterPlane writes prediction + residual to the frame buffer.
// The residual is added per-TX block and inverse-transformed.
// Writes to the full buffer (including padding), matching dav1d.
func (td *TileDecoder) reconstructInterPlane(miRow, miCol, nomW, nomH, plane int, txSz TxSize, pred []byte, residual []int32) {
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

	bufH := len(buf) / stride
	bufW := stride

	baseX := miCol * 4
	baseY := miRow * 4
	txW, txH := TxSizeDimensions(txSz)
	if txW == 0 || txH == 0 {
		// Fallback: just write prediction.
		td.writeInterPredToFrame(miRow, miCol, nomW, nomH, plane, pred)
		return
	}

	resOff := 0
	for tyOff := 0; tyOff < nomH; tyOff += txH {
		for txOff := 0; txOff < nomW; txOff += txW {
			for dy := 0; dy < txH; dy++ {
				py := baseY + tyOff + dy
				if py >= bufH {
					resOff += txW
					continue
				}
				for dx := 0; dx < txW; dx++ {
					px := baseX + txOff + dx
					if px >= bufW {
						resOff++
						continue
					}
					predVal := int32(pred[(tyOff+dy)*nomW+(txOff+dx)])
					var resVal int32
					if residual != nil && resOff < len(residual) {
						resVal = residual[resOff]
					}
					val := predVal + resVal
					if val < 0 {
						val = 0
					}
					if val > 255 {
						val = 255
					}
					buf[py*stride+px] = byte(val)
					resOff++
				}
			}
		}
	}
}

// getSkipModeContext returns the context for skip_mode CDF.
func (td *TileDecoder) getSkipModeContext(miRow, miCol int) int {
	ctx := 0
	if miRow > td.tileRowStart && miCol < len(td.aboveModeInfo) {
		if td.aboveModeInfo[miCol].SkipMode {
			ctx++
		}
	}
	if miCol > td.tileColStart && miRow < len(td.leftModeInfo) {
		if td.leftModeInfo[miRow].SkipMode {
			ctx++
		}
	}
	return ctx
}

// getMaskCompCtx returns the context for comp_group_idx CDF.
// Simplified version of dav1d get_mask_comp_ctx.
func (td *TileDecoder) getMaskCompCtx(miRow, miCol int) int {
	aCtx := 0
	if miCol < len(td.aboveModeInfo) {
		info := td.aboveModeInfo[miCol]
		if info.CompType >= uint8(COMP_INTER_SEG) {
			aCtx = 1
		} else if !info.IsIntra && info.RefFrame[0] == 6 {
			aCtx = 3
		}
	}
	lCtx := 0
	if miRow < len(td.leftModeInfo) {
		info := td.leftModeInfo[miRow]
		if info.CompType >= uint8(COMP_INTER_SEG) {
			lCtx = 1
		} else if !info.IsIntra && info.RefFrame[0] == 6 {
			lCtx = 3
		}
	}
	ctx := aCtx + lCtx
	if ctx > 5 {
		ctx = 5
	}
	return ctx
}

// getJntCompCtx returns the context for compound_idx CDF.
// Matches dav1d's get_jnt_comp_ctx (src/env.h).
func (td *TileDecoder) getJntCompCtx(miRow, miCol int, ref0, ref1 int8) int {
	// Compute POC distance offset: offset=1 if distances are equal.
	orderHintBits := int(td.sh.OrderHintBitsMinus1) + 1
	poc := int(td.fh.OrderHint)
	ref0Idx := td.fh.RefFrameIdx[ref0]
	ref1Idx := td.fh.RefFrameIdx[ref1]
	ref0poc := int(td.refOrderHints[ref0Idx])
	ref1poc := int(td.refOrderHints[ref1Idx])
	d0 := abs(getPocDiff(orderHintBits, ref0poc, poc))
	d1 := abs(getPocDiff(orderHintBits, poc, ref1poc))
	offset := 0
	if d0 == d1 {
		offset = 1
	}

	aCtx := 0
	if miCol < len(td.aboveModeInfo) {
		info := td.aboveModeInfo[miCol]
		if info.CompType >= uint8(COMP_INTER_AVG) || (!info.IsIntra && info.RefFrame[0] == 6) {
			aCtx = 1
		}
	}
	lCtx := 0
	if miRow < len(td.leftModeInfo) {
		info := td.leftModeInfo[miRow]
		if info.CompType >= uint8(COMP_INTER_AVG) || (!info.IsIntra && info.RefFrame[0] == 6) {
			lCtx = 1
		}
	}
	return 3*offset + aCtx + lCtx
}

// getPocDiff computes signed POC difference with wrap-around.
func getPocDiff(orderHintBits int, pocA, pocB int) int {
	if orderHintBits == 0 {
		return 0
	}
	diff := pocA - pocB
	m := 1 << (orderHintBits - 1)
	return (diff & (m - 1)) - (diff & m)
}

// getFilterCtx returns the context for switchable interpolation filter CDF.
// Simplified version.
func (td *TileDecoder) getFilterCtx(miRow, miCol int, isComp bool, dim int, ref0 int8) int {
	// Matches dav1d get_filter_ctx (src/env.h).
	// Always reads from context arrays; initial values (ref=-1, filter=3)
	// ensure the default "no match" filter is N_SWITCHABLE_FILTERS (3).
	const nSwitchable = 3 // DAV1D_N_SWITCHABLE_FILTERS

	aFilter := nSwitchable
	if miCol < len(td.aboveModeInfo) {
		info := td.aboveModeInfo[miCol]
		if info.RefFrame[0] == ref0 || info.RefFrame[1] == ref0 {
			aFilter = int(info.Filter[dim])
		}
	}

	lFilter := nSwitchable
	if miRow < len(td.leftModeInfo) {
		info := td.leftModeInfo[miRow]
		if info.RefFrame[0] == ref0 || info.RefFrame[1] == ref0 {
			lFilter = int(info.Filter[dim])
		}
	}

	comp := 0
	if isComp {
		comp = 1
	}

	if aFilter == lFilter {
		return comp*4 + aFilter
	} else if aFilter == nSwitchable {
		return comp*4 + lFilter
	} else if lFilter == nSwitchable {
		return comp*4 + aFilter
	}
	return comp*4 + nSwitchable
}

// isWedgeAllowed returns whether wedge compound is allowed for the block size.
// Matches dav1d wedge_allowed_mask.
func isWedgeAllowed(bsz int) bool {
	// Matches dav1d wedge_allowed_mask in tables.h:
	// BS_8x8(3), BS_8x16(4), BS_16x8(5), BS_16x16(6), BS_16x32(7),
	// BS_32x16(8), BS_32x32(9), BS_8x32(18), BS_32x8(19).
	switch bsz {
	case 3, 4, 5, 6, 7, 8, 9, 18, 19:
		return true
	default:
		return false
	}
}


// isInterIntraAllowed returns whether inter-intra is allowed for the block size.
// Must match dav1d's interintra_allowed_mask in tables.h exactly:
// allowed sizes: 32x32, 32x16, 16x32, 16x16, 16x8, 8x16, 8x8.
// That is: MiSize >= BLOCK_8X8 (3) && MiSize <= BLOCK_32X32 (9).
func isInterIntraAllowed(nomPixW, nomPixH int) bool {
	bsz := blockSizeEnum(nomPixW, nomPixH)
	return bsz >= 3 && bsz <= 9
}

// hasOverlappableNeighbors implements dav1d's findoddzero check for OBMC/motion mode.
// Returns true if any top or left neighbor (at odd 4x4 positions) is an inter block.
func (td *TileDecoder) hasOverlappableNeighbors(miRow, miCol, bW, bH int, edgeFlags uint8) bool {
	haveTop := miRow > td.tileRowStart
	haveLeft := miCol > td.tileColStart

	// Clip to tile boundary (dav1d: w4 = imin(bw4, col_end - bx))
	w4 := bW
	if miCol+bW > td.tileColEnd {
		w4 = td.tileColEnd - miCol
	}
	h4 := bH
	if miRow+bH > td.tileRowEnd {
		h4 = td.tileRowEnd - miRow
	}

	if haveTop {
		for n := 0; n < w4>>1; n++ {
			c := miCol + 1 + n*2
			if c < len(td.aboveModeInfo) && !td.aboveModeInfo[c].IsIntra {
				return true
			}
		}
	}
	if haveLeft {
		for n := 0; n < h4>>1; n++ {
			r := miRow + 1 + n*2
			if r < len(td.leftModeInfo) && !td.leftModeInfo[r].IsIntra {
				return true
			}
		}
	}
	return false
}

// hasMatchingRef checks if any top/left/corner neighbor uses the same single
// reference as the current block. Simplified equivalent of dav1d's find_matching_ref.
// Uses context arrays for top/left edge scans and miGrid for corner checks
// (top-left, top-right) which can be overwritten by same-row blocks in context arrays.
func (td *TileDecoder) hasMatchingRef(miRow, miCol, bW, bH int, ref0 int8, edgeFlags uint8) bool {
	haveTop := miRow > td.tileRowStart
	haveLeft := miCol > td.tileColStart

	w4 := bW
	if miCol+bW > td.tileColEnd {
		w4 = td.tileColEnd - miCol
	}
	h4 := bH
	if miRow+bH > td.tileRowEnd {
		h4 = td.tileRowEnd - miRow
	}


	// matchesRef: dav1d matches(rp): rp->ref.ref[0] == ref+1 && rp->ref.ref[1] == -1.
	// Interintra blocks store ref[1]=0 in dav1d (not -1), excluded via IIType.
	matchesRef := func(info ModeInfo) bool {
		return info.RefFrame[0] == ref0 && info.RefFrame[1] < 0 && info.IIType == 0
	}

	// Check top edge neighbors using context array.
	if haveTop {
		for c := miCol; c < miCol+w4 && c < len(td.aboveModeInfo); c++ {
			if matchesRef(td.aboveModeInfo[c]) {
				return true
			}
		}
	}
	// Check left edge neighbors using context array.
	if haveLeft {
		for r := miRow; r < miRow+h4 && r < len(td.leftModeInfo); r++ {
			if matchesRef(td.leftModeInfo[r]) {
				return true
			}
		}
	}
	// Check top-left corner using miGrid (avoids stale context array data
	// when same-row blocks overwrite aboveModeInfo[miCol-1]).
	if haveTop && haveLeft && td.miGrid != nil {
		info := td.miGrid.Get(miRow-1, miCol-1)
		if matchesRef(info) {
			return true
		}
	}
	// Check top-right corner using miGrid.
	// dav1d: have_topright = imax(bw4, bh4) < 32 && have_top && bx+bw4 < col_end && (edge & TOP_HAS_RIGHT)
	// 128x128 blocks (bW=32 or bH=32) do NOT check top-right.
	if max(bW, bH) < 32 && haveTop && miCol+bW < td.tileColEnd && (edgeFlags&EdgeI444TopHasRight) != 0 && td.miGrid != nil {
		if matchesRef(td.miGrid.Get(miRow-1, miCol+bW)) {
			return true
		}
	}
	return false
}

// varTxBlock represents a leaf node in the variable TX size tree.
type varTxBlock struct {
	miRow, miCol int
	txSz         TxSize
}

// readVarTxSize implements AV1 spec Section 5.11.38 read_var_tx_size().
// It recursively reads the variable TX size tree for inter blocks and
// returns the leaf blocks with their positions and sizes.
// MAX_VARTX_DEPTH = 2: at most 2 levels of splitting.
func (td *TileDecoder) readVarTxSize(bc *BoolReader, miRow, miCol int, txSz TxSize, depth int) ([]varTxBlock, error) {
	if miRow >= int(td.fh.MiRows) || miCol >= int(td.fh.MiCols) {
		return nil, nil
	}

	txW, txH := TxSizeDimensions(txSz)
	w4 := max(txW>>2, 1)
	h4 := max(txH>>2, 1)

	const maxVarTxDepth = 2
	split := false

	if txSz > TX_4X4 && depth < maxVarTxDepth {
		// txSzSqrUp: square-up size class (1..4), matching dav1d's t_dim->max.
		maxDim := txW
		if txH > maxDim {
			maxDim = txH
		}
		txSzSqrUp := floorLog2(maxDim) - 2
		if txSzSqrUp < 1 {
			txSzSqrUp = 1
		}
		if txSzSqrUp > 4 {
			txSzSqrUp = 4
		}

		// CDF category: matches dav1d's cat = 2*(TX_64X64 - t_dim->max) - depth.
		cat := 2*(4-txSzSqrUp) - depth

		// Context: compare above/left neighbor TX log2 sizes with current TX log2 dimensions.
		// dav1d uses tx[] (not tx_intra[]) for var TX split context:
		// a = a->tx[bx4] < t_dim->lw, l = l->tx[by4] < t_dim->lh.
		txLw, txLh := txSizeLog2WH(txSz)
		above := 0
		if miRow > td.tileRowStart && miCol < len(td.aboveTx) && td.aboveTx[miCol] < txLw {
			above = 1
		}
		left := 0
		if miCol > td.tileColStart && miRow < len(td.leftTx) && td.leftTx[miRow] < txLh {
			left = 1
		}
		ctx := above + left

		splitVal, err := bc.ReadSymbol(td.cdf.TxSplit[cat][ctx], 2)
		if err != nil {
			return nil, fmt.Errorf("txfm_split at (%d,%d) depth=%d cat=%d: %w", miRow, miCol, depth, cat, err)
		}
		split = (splitVal == 1)
	}

	if split {
		subTxSz := splitTxSize(txSz)
		subW, subH := TxSizeDimensions(subTxSz)
		subW4 := max(subW>>2, 1)
		subH4 := max(subH>>2, 1)

		var blocks []varTxBlock
		if txW == txH {
			// Square: 4 quadrants.
			positions := [4][2]int{
				{miRow, miCol},
				{miRow, miCol + subW4},
				{miRow + subH4, miCol},
				{miRow + subH4, miCol + subW4},
			}
			for _, p := range positions {
				sub, err := td.readVarTxSize(bc, p[0], p[1], subTxSz, depth+1)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, sub...)
			}
		} else if txW > txH {
			// Wide: 2 horizontal halves.
			sub1, err := td.readVarTxSize(bc, miRow, miCol, subTxSz, depth+1)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, sub1...)
			sub2, err := td.readVarTxSize(bc, miRow, miCol+subW4, subTxSz, depth+1)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, sub2...)
		} else {
			// Tall: 2 vertical halves.
			sub1, err := td.readVarTxSize(bc, miRow, miCol, subTxSz, depth+1)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, sub1...)
			sub2, err := td.readVarTxSize(bc, miRow+subH4, miCol, subTxSz, depth+1)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, sub2...)
		}
		return blocks, nil
	}

	// Leaf: record TX size log2 in tx[] context (dav1d's a->tx / l->tx).
	leafLw, leafLh := txSizeLog2WH(txSz)
	for c := miCol; c < miCol+w4 && c < len(td.aboveTx); c++ {
		td.aboveTx[c] = leafLw
	}
	for r := miRow; r < miRow+h4 && r < len(td.leftTx); r++ {
		td.leftTx[r] = leafLh
	}
	return []varTxBlock{{miRow: miRow, miCol: miCol, txSz: txSz}}, nil
}

// chromaMC performs chroma motion compensation for single-reference inter blocks,
// implementing the sub-8x8 chroma derivation from AV1 spec / dav1d recon_tmpl.c.
// When a luma block dimension equals the subsampling factor (e.g., bH=1 in 4:2:0),
// chroma is composed from multiple neighboring blocks' MVs.
func (td *TileDecoder) chromaMC(predU, predV []byte, nomChromaW, nomChromaH int,
	miRow, miCol, bW, bH int, mv0 MV, ref0 int8,
	refFrame *FrameBuffer, chromaRefW, chromaRefH int,
	filterTypeH, filterTypeV, subX, subY int) {

	// Determine if sub-8x8 chroma derivation is needed.
	// dav1d: is_sub8x8 = bw4 == ss_hor || bh4 == ss_ver
	isSub8x8 := (bW == 1 && subX == 1) || (bH == 1 && subY == 1)

	if isSub8x8 {
		// Check if neighbors are inter (required for sub-8x8 composition).
		if bW == 1 && subX == 1 {
			// Left neighbor must be inter.
			if miCol > 0 {
				info := td.leftModeInfo[miRow]
				if info.IsIntra || info.RefFrame[0] < 0 {
					isSub8x8 = false
				}
			} else {
				isSub8x8 = false
			}
		}
		if bH == 1 && subY == 1 {
			// Above neighbor must be inter.
			// Use miGrid to read the above row's data (aboveModeInfo may be
			// overwritten by same-row blocks to the left).
			if miRow > 0 && td.miGrid != nil {
				info := td.miGrid.Get(miRow-1, miCol)
				if info.IsIntra || info.RefFrame[0] < 0 {
					isSub8x8 = false
				}
			} else {
				isSub8x8 = false
			}
		}
		if bW == 1 && bH == 1 && subX == 1 && subY == 1 {
			// Above-left neighbor must be inter too.
			// Use miGrid to get stable above-row data (dav1d uses r[-1][bx-1]).
			if miRow > 0 && miCol > 0 && td.miGrid != nil {
				info := td.miGrid.Get(miRow-1, miCol-1)
				if info.IsIntra || info.RefFrame[0] < 0 {
					isSub8x8 = false
				}
			} else {
				isSub8x8 = false
			}
		}
	}

	if isSub8x8 {
		// Sub-8x8 chroma composition: build chroma prediction from
		// multiple neighboring blocks' MVs.
		// Each quadrant/half uses a different block's MV.
		hOff := 0 // horizontal pixel offset into predU/predV
		vOff := 0 // vertical pixel offset into predU/predV

		halfW := nomChromaW // width of each sub-MC
		halfH := nomChromaH // height of each sub-MC
		if bW == 1 && subX == 1 {
			halfW = nomChromaW / 2
		}
		if bH == 1 && subY == 1 {
			halfH = nomChromaH / 2
		}

		// Helper to MC one sub-block into predU/predV at (dstXOff, dstYOff).
		// fH, fV are the filter types for this sub-block (neighbor's filter for neighbor quadrants).
		mcSub := func(srcMiCol, srcMiRow int, smv MV, sref int8, w, h, dstXOff, dstYOff, fH, fV int) {
			if w <= 0 || h <= 0 {
				return
			}
			refIdx := td.fh.RefFrameIdx[sref]
			rf := td.refFrames[refIdx]
			if rf == nil {
				return
			}
			crw := (rf.Width + subX) >> subX
			crh := (rf.Height + subY) >> subY

			tmpU := make([]byte, w*h)
			tmpV := make([]byte, w*h)
			motionCompensationChroma(tmpU, w, h,
				rf.U, rf.StrideU, crw, crh,
				srcMiCol*(4>>subX), srcMiRow*(4>>subY),
				int32(smv.Col), int32(smv.Row),
				fH, fV, subX, subY)
			motionCompensationChroma(tmpV, w, h,
				rf.V, rf.StrideV, crw, crh,
				srcMiCol*(4>>subX), srcMiRow*(4>>subY),
				int32(smv.Col), int32(smv.Row),
				fH, fV, subX, subY)
			for r := 0; r < h; r++ {
				for c := 0; c < w; c++ {
					predU[(dstYOff+r)*nomChromaW+(dstXOff+c)] = tmpU[r*w+c]
					predV[(dstYOff+r)*nomChromaW+(dstXOff+c)] = tmpV[r*w+c]
				}
			}
		}

		if bW == 1 && bH == 1 && subX == 1 && subY == 1 {
			// 4x4 luma block: 4 quadrants from above-left, above, left, current.
			// dav1d: each neighbor quadrant uses that neighbor's filter type.
			// Use miGrid for above-row neighbors to avoid stale aboveModeInfo
			// (overwritten by same-row blocks processed earlier in raster order).

			// Above-left quadrant — dav1d: r[-1][bx-1]
			aboveLeftInfo := td.miGrid.Get(miRow-1, miCol-1)
			alFH, alFV := int(aboveLeftInfo.Filter[1]), int(aboveLeftInfo.Filter[0])
			mcSub(miCol-1, miRow-1, aboveLeftInfo.MV[0], aboveLeftInfo.RefFrame[0], halfW, halfH, 0, 0, alFH, alFV)
			hOff = halfW

			// Above quadrant — dav1d: r[-1][bx]
			aboveInfo := td.miGrid.Get(miRow-1, miCol)
			aFH, aFV := int(aboveInfo.Filter[1]), int(aboveInfo.Filter[0])
			mcSub(miCol, miRow-1, aboveInfo.MV[0], aboveInfo.RefFrame[0], halfW, halfH, hOff, 0, aFH, aFV)
			vOff = halfH
			hOff = 0

			// Left quadrant — dav1d: r[0][bx-1] (same row, OK to use leftModeInfo)
			leftInfo := td.leftModeInfo[miRow]
			lFH, lFV := int(leftInfo.Filter[1]), int(leftInfo.Filter[0])
			mcSub(miCol-1, miRow, leftInfo.MV[0], leftInfo.RefFrame[0], halfW, halfH, 0, vOff, lFH, lFV)
			hOff = halfW

			// Current quadrant
			mcSub(miCol, miRow, mv0, ref0, halfW, halfH, hOff, vOff, filterTypeH, filterTypeV)
		} else if bW == 1 && subX == 1 {
			// Narrow block (4 pixels wide): left half from left neighbor, right half from current.
			// dav1d: left half uses left neighbor's filter type.
			leftInfo := td.leftModeInfo[miRow]
			lFH, lFV := int(leftInfo.Filter[1]), int(leftInfo.Filter[0])
			mcSub(miCol-1, miRow, leftInfo.MV[0], leftInfo.RefFrame[0], halfW, nomChromaH, 0, 0, lFH, lFV)
			hOff = halfW
			mcSub(miCol, miRow, mv0, ref0, halfW, nomChromaH, hOff, 0, filterTypeH, filterTypeV)
		} else if bH == 1 && subY == 1 {
			// Short block (4 pixels tall): top half from above neighbor, bottom half from current.
			// dav1d: top half uses above neighbor's filter type (r[-1][bx]).
			aboveInfo := td.miGrid.Get(miRow-1, miCol)
			aFH, aFV := int(aboveInfo.Filter[1]), int(aboveInfo.Filter[0])
			mcSub(miCol, miRow-1, aboveInfo.MV[0], aboveInfo.RefFrame[0], nomChromaW, halfH, 0, 0, aFH, aFV)
			vOff = halfH
			mcSub(miCol, miRow, mv0, ref0, nomChromaW, halfH, 0, vOff, filterTypeH, filterTypeV)
		}
	} else {
		// Normal chroma MC path (non-sub8x8).
		// Use adjusted parameters per dav1d:
		//   width:  bW << (bW == ss_hor)  → doubles if bW == 1 in 4:2:0
		//   height: bH << (bH == ss_ver)  → doubles if bH == 1 in 4:2:0
		//   position: miCol & ~subX, miRow & ~subY  → round to even MI
		adjMiCol := miCol &^ subX
		adjMiRow := miRow &^ subY
		// The mc() function in dav1d uses bx * h_mul where h_mul = 4 >> ss_hor.
		// For chroma: baseX = adjMiCol * (4 >> subX), baseY = adjMiRow * (4 >> subY)
		baseX := adjMiCol * (4 >> subX)
		baseY := adjMiRow * (4 >> subY)

		motionCompensationChroma(predU, nomChromaW, nomChromaH,
			refFrame.U, refFrame.StrideU, chromaRefW, chromaRefH,
			baseX, baseY, int32(mv0.Col), int32(mv0.Row),
			filterTypeH, filterTypeV, subX, subY)
		motionCompensationChroma(predV, nomChromaW, nomChromaH,
			refFrame.V, refFrame.StrideV, chromaRefW, chromaRefH,
			baseX, baseY, int32(mv0.Col), int32(mv0.Row),
			filterTypeH, filterTypeV, subX, subY)
	}
}

// decodeInterCoeffsVarTx decodes coefficients for an inter block with
// variable TX sizes for luma and uniform TX for chroma.
// Returns residuals in 2D spatial format (indexed by [y*width+x]).
func (td *TileDecoder) decodeInterCoeffsVarTx(bc *BoolReader,
	miRow, miCol, bW, bH int,
	varBlocks []varTxBlock,
	nomPixW, nomPixH int,
	chromaTxSz TxSize,
	nomChromaW, nomChromaH int,
	chromaMiRow, chromaMiCol int,
	hasChroma bool, subX, subY int,
) (yResidual, uResidual, vResidual []int32, err error) {

	yRes := make([]int32, nomPixW*nomPixH)
	yAnyNonZero := false

	var uvTxW, uvTxH, uvTxW4, uvTxH4 int
	var uvBlockMatchesTx bool
	var uRes, vRes []int32
	cw4, ch4 := 0, 0
	if hasChroma && nomChromaW > 0 && nomChromaH > 0 {
		uvTxW, uvTxH = TxSizeDimensions(chromaTxSz)
		uvTxW4 = max(uvTxW>>2, 1)
		uvTxH4 = max(uvTxH>>2, 1)
		uvBlockMatchesTx = (nomChromaW == uvTxW && nomChromaH == uvTxH)
		uRes = make([]int32, nomChromaW*nomChromaH)
		vRes = make([]int32, nomChromaW*nomChromaH)
		// NOTE: cw4/ch4 are overridden below after h4/w4 are clamped.
		cw4 = nomChromaW >> 2
		ch4 = nomChromaH >> 2
	}

	uAnyNonZero := false
	vAnyNonZero := false

	// Clamp iteration to the visible frame boundary.
	// Matches dav1d: w4 = imin(bw4, f->bw - t->bx), h4 = imin(bh4, f->bh - t->by).
	// Use effective MI bounds (f->bh/bw), not spec MiRows/MiCols.
	// TX blocks beyond the visible frame are NOT decoded from the bitstream.
	h4 := bH
	w4 := bW
	if visW := int(td.fh.MiCols) - miCol; visW < w4 {
		w4 = visW
	}
	if visH := int(td.fh.MiRows) - miRow; visH < h4 {
		h4 = visH
	}

	// Override chroma iteration bounds to use clamped luma dimensions.
	// Matches dav1d: cw4 = (w4 + ss_hor) >> ss_hor, ch4 = (h4 + ss_ver) >> ss_ver.
	if hasChroma && nomChromaW > 0 && nomChromaH > 0 {
		cw4 = (w4 + subX) >> subX
		ch4 = (h4 + subY) >> subY
	}

	// Group var TX blocks by 64x64 (16 MI) sub-region for correct
	// interleaving with chroma per AV1 spec residual() ordering.
	// Only include blocks within the clamped visible area.
	type subKey [2]int
	subYBlocks := map[subKey][]varTxBlock{}
	for _, blk := range varBlocks {
		relY := blk.miRow - miRow
		relX := blk.miCol - miCol
		if relY >= h4 || relX >= w4 {
			continue
		}
		key := subKey{(relY / 16) * 16, (relX / 16) * 16}
		subYBlocks[key] = append(subYBlocks[key], blk)
	}

	// Set txtpMap origin for UV inter txType derivation.
	td.txtpMapMiRow = miRow
	td.txtpMapMiCol = miCol

	for initY := 0; initY < h4; initY += 16 {
		for initX := 0; initX < w4; initX += 16 {
			// --- Y luma TX blocks in this sub-region ---
			key := subKey{initY, initX}
			for _, blk := range subYBlocks[key] {
				txW, txH := TxSizeDimensions(blk.txSz)
				txW4 := max(txW>>2, 1)
				txH4 := max(txH>>2, 1)
				blockMatchesTx := (nomPixW == txW && nomPixH == txH)

				txOff := (blk.miCol - miCol) * 4
				tyOff := (blk.miRow - miRow) * 4

				coeffs, cerr := td.decodeTxBlock(bc, blk.txSz, 0, 0, DC_PRED, blk.miRow, blk.miCol, txW4, txH4, blockMatchesTx, true, -1)
				if cerr != nil {
					return nil, nil, nil, fmt.Errorf("Y varTx at (%d,%d): %w", blk.miRow, blk.miCol, cerr)
				}

				if coeffs != nil {
					yAnyNonZero = true
					curTxW := min(txW, nomPixW-txOff)
					curTxH := min(txH, nomPixH-tyOff)
					for r := 0; r < curTxH; r++ {
						for c := 0; c < curTxW; c++ {
							if r*txW+c < len(coeffs) {
								yRes[(tyOff+r)*nomPixW+(txOff+c)] = coeffs[r*txW+c]
							}
						}
					}
				}
			}

			// --- UV chroma TX blocks in this sub-region ---
			if hasChroma && nomChromaW > 0 && nomChromaH > 0 && uvTxW > 0 && uvTxH > 0 {
				subCH4 := min(ch4, (initY+16)>>subY)
				subCW4 := min(cw4, (initX+16)>>subX)

				for pl := 1; pl <= 2; pl++ {
					for cy4 := initY >> subY; cy4 < subCH4; cy4 += uvTxH4 {
						for cx4 := initX >> subX; cx4 < subCW4; cx4 += uvTxW4 {
							txOff := cx4 * 4
							tyOff := cy4 * 4
							txMiRow := chromaMiRow + cy4
							txMiCol := chromaMiCol + cx4

							// Look up luma txType at the corresponding luma MI position.
						lumaY4 := cy4 << subY
						lumaX4 := cx4 << subX
						yTxType := DCT_DCT
						if lumaY4 < 32 && lumaX4 < 32 {
							yTxType = td.txtpMap[lumaY4][lumaX4]
						}
						uvTxType := getUVInterTxType(chromaTxSz, yTxType)
						coeffs, cerr := td.decodeTxBlock(bc, chromaTxSz, 1, pl, DC_PRED, txMiRow, txMiCol, uvTxW4, uvTxH4, uvBlockMatchesTx, true, uvTxType)
							if cerr != nil {
								return nil, nil, nil, fmt.Errorf("UV pl=%d at (%d,%d): %w", pl, txOff, tyOff, cerr)
							}

							if coeffs != nil {
								curTxW := min(uvTxW, nomChromaW-txOff)
								curTxH := min(uvTxH, nomChromaH-tyOff)
								res := uRes
								if pl == 2 {
									res = vRes
								}
								if pl == 1 {
									uAnyNonZero = true
								} else {
									vAnyNonZero = true
								}
								for r := 0; r < curTxH; r++ {
									for c := 0; c < curTxW; c++ {
										if r*uvTxW+c < len(coeffs) {
											res[(tyOff+r)*nomChromaW+(txOff+c)] = coeffs[r*uvTxW+c]
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
		yRes = nil
	}
	if !uAnyNonZero {
		uRes = nil
	}
	if !vAnyNonZero {
		vRes = nil
	}
	return yRes, uRes, vRes, nil
}

// reconstructInterPlaneSpatial reconstructs an inter plane by adding
// spatial-format residuals to the prediction buffer and writing to the frame.
// Writes to the full buffer (including padding rows past the visible frame),
// matching dav1d's behavior. This ensures intra reference reads in subsequent
// blocks find valid pixel data in the padding area.
func (td *TileDecoder) reconstructInterPlaneSpatial(miRow, miCol, nomW, nomH, plane int, pred []byte, residual []int32) {
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

	// Use buffer dimensions (including padding) instead of frame dimensions,
	// so padding rows get valid reconstructed data for intra reference reads.
	bufH := len(buf) / stride
	bufW := stride

	baseX := miCol * 4
	baseY := miRow * 4

	for dy := 0; dy < nomH; dy++ {
		py := baseY + dy
		if py >= bufH {
			continue
		}
		for dx := 0; dx < nomW; dx++ {
			px := baseX + dx
			if px >= bufW {
				continue
			}
			predVal := int32(pred[dy*nomW+dx])
			var resVal int32
			if residual != nil {
				idx := dy*nomW + dx
				if idx < len(residual) {
					resVal = residual[idx]
				}
			}
			val := predVal + resVal
			if val < 0 {
				val = 0
			} else if val > 255 {
				val = 255
			}
			if py*stride+px < len(buf) {
				buf[py*stride+px] = byte(val)
			}
		}
	}
}
