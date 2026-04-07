// Package decoder implements AV1 bitstream decoding.
//
// This file implements per-superblock loop restoration parameter reading.
// Before decoding each superblock's partition tree, the decoder reads
// restoration filter parameters from the tile MSAC for each plane that
// has loop restoration enabled and whose superblock position aligns to
// a restoration unit boundary.
//
// AV1 spec Section 5.11.47 and dav1d src/decode.c (read_restoration_info).
package decoder

import "fmt"


// lrRefState holds the most recently decoded loop restoration filter
// parameters for one plane. These are used as reference values for
// subexp-coded filter coefficients in subsequent restoration units.
type lrRefState struct {
	filterV    [3]int // Wiener vertical filter coefficients
	filterH    [3]int // Wiener horizontal filter coefficients
	sgrWeights [2]int // SGR projection weights
}

// sgrParamsTable maps the 4-bit sgrproj index to two parameters.
// Each entry [i][0] and [i][1] correspond to the two SGR filter strengths.
// A zero value means that filter tap is disabled.
// Matches dav1d's dav1d_sgr_params (src/tables.c).
var sgrParamsTable = [16][2]uint16{
	{140, 3236}, {112, 2158}, {93, 1618}, {80, 1438},
	{70, 1295}, {58, 1177}, {47, 1079}, {37, 996},
	{30, 925}, {25, 863}, {0, 2589}, {0, 1618},
	{0, 1177}, {0, 925}, {56, 0}, {22, 0},
}

// readLoopRestorationForSuperblock reads loop restoration parameters from
// the tile MSAC for all planes before decoding the superblock's partition tree.
//
// This matches dav1d's per-superblock restoration reading in decode.c lines 2662-2713.
// The decoded parameters are stored in td.lrParams for later application by
// ApplyLoopRestoration.
func (td *TileDecoder) readLoopRestorationForSuperblock(bc *BoolReader, miRow, miCol, sbSize int) error {
	for p := 0; p < 3; p++ {
		if td.fh.LRType[p] == FrameRestoreNone {
			continue
		}

		// Compute subsampling for chroma planes.
		ssVer := 0
		ssHor := 0
		if p > 0 {
			ssVer = int(td.sh.ColorConfig.SubsamplingY)
			ssHor = int(td.sh.ColorConfig.SubsamplingX)
		}

		// Compute the LR unit size in pixels (log2).
		// dav1d: unit_size[0] = 6 + sb128 + shift bits
		// Our LRUnitShift already encodes the total additional shift.
		// For 64x64 SB: base log2 = 6. For 128x128 SB: base log2 = 7.
		unitSizeLog2 := 6 + td.fh.LRUnitShift
		if td.sh.Use128x128Superblock {
			unitSizeLog2 = 7 + td.fh.LRUnitShift
		}

		// Chroma planes may have a smaller unit size.
		if p > 0 && td.fh.LRUVShift {
			unitSizeLog2--
		}

		unitSize := 1 << unitSizeLog2
		mask := unitSize - 1
		halfUnit := unitSize >> 1


		// Check vertical alignment: y must be aligned to unit boundary.
		y := miRow * 4 >> ssVer
		if y&mask != 0 {
			continue
		}

		// Frame height for this plane.
		h := (int(td.fh.FrameHeight) + ssVer) >> ssVer

		// Skip if this is not the first unit row and we're past the
		// point where a full unit fits (round half up at frame boundary,
		// but only if there's more than one restoration unit).
		if y != 0 && y+halfUnit > h {
			continue
		}

		// Check horizontal alignment: x must be aligned to unit boundary.
		x := miCol * 4 >> ssHor
		if x&mask != 0 {
			continue
		}

		// Frame width for this plane.
		w := (int(td.fh.FrameWidth) + ssHor) >> ssHor

		// Same boundary check for horizontal.
		if x != 0 && x+halfUnit > w {
			continue
		}

		// Compute the restoration unit index for this position.
		// Must match ApplyLoopRestoration's grid: round-half-up, not ceiling.
		unitCols := 1
		if w > unitSize {
			unitCols = (w + halfUnit) / unitSize
		}
		unitRow := y / unitSize
		unitCol := x / unitSize
		unitIdx := unitRow*unitCols + unitCol

		if err := td.readRestorationInfo(bc, p, td.fh.LRType[p], unitIdx); err != nil {
			return fmt.Errorf("lr plane %d at (%d,%d): %w", p, miRow, miCol, err)
		}
	}
	return nil
}

// readRestorationInfo reads restoration filter parameters for one restoration
// unit on the given plane.
//
// Matches dav1d's read_restoration_info (src/decode.c lines 2511-2573).
// The frame_type parameter is the per-plane LRType from the frame header:
//
//	FrameRestoreSwitchable (1): read 3-symbol CDF to pick none/wiener/sgrproj
//	FrameRestoreWiener (2): read bool CDF to decide wiener vs none
//	FrameRestoreSGRProj (3): read bool CDF to decide sgrproj vs none
func (td *TileDecoder) readRestorationInfo(bc *BoolReader, p int, frameType int, unitIdx int) error {
	var lrType int // 0=none, 2=wiener, 3=sgrproj (matching FrameRestore* constants)

	if frameType == FrameRestoreSwitchable {
		// Read 3-symbol CDF: 0=none, 1=wiener, 2=sgrproj
		filter, err := bc.ReadSymbol(td.cdf.RestorationType, 3)
		if err != nil {
			return fmt.Errorf("restore_switchable: %w", err)
		}
		// dav1d: lr->type = filter + !!filter (maps 0->0, 1->2, 2->3)
		if filter == 0 {
			lrType = FrameRestoreNone
		} else if filter == 1 {
			lrType = FrameRestoreWiener
		} else {
			lrType = FrameRestoreSGRProj
		}
	} else if frameType == FrameRestoreWiener {
		useWiener, err := bc.ReadSymbolBool(td.cdf.UseWiener)
		if err != nil {
			return fmt.Errorf("restore_wiener: %w", err)
		}
		if useWiener {
			lrType = FrameRestoreWiener
		} else {
			lrType = FrameRestoreNone
		}
	} else if frameType == FrameRestoreSGRProj {
		useSgr, err := bc.ReadSymbolBool(td.cdf.UseSGRProj)
		if err != nil {
			return fmt.Errorf("restore_sgrproj: %w", err)
		}
		if useSgr {
			lrType = FrameRestoreSGRProj
		} else {
			lrType = FrameRestoreNone
		}
	}

	// Initialize the stored params for this unit (default NONE).
	var params LRUnitParams
	params.Type = lrType

	if lrType == FrameRestoreWiener {
		// Read 6 Wiener filter coefficients via subexp coding.
		// Vertical coefficients: 3 values.
		// For chroma planes (p > 0), the first vertical and horizontal
		// coefficients are always 0 (not read from bitstream).
		if p == 0 {
			// filter_v[0]: ref + 5, range 16, k=1
			v0, err := bc.DecodeSubexp(td.lrRef[p].filterV[0]+5, 16, 1)
			if err != nil {
				return fmt.Errorf("wiener filter_v[0]: %w", err)
			}
			td.lrRef[p].filterV[0] = v0 - 5
		}

		// filter_v[1]: ref + 23, range 32, k=2
		v1, err := bc.DecodeSubexp(td.lrRef[p].filterV[1]+23, 32, 2)
		if err != nil {
			return fmt.Errorf("wiener filter_v[1]: %w", err)
		}
		td.lrRef[p].filterV[1] = v1 - 23

		// filter_v[2]: ref + 17, range 64, k=3
		v2, err := bc.DecodeSubexp(td.lrRef[p].filterV[2]+17, 64, 3)
		if err != nil {
			return fmt.Errorf("wiener filter_v[2]: %w", err)
		}
		td.lrRef[p].filterV[2] = v2 - 17

		// Horizontal coefficients: 3 values (same structure).
		if p == 0 {
			// filter_h[0]: ref + 5, range 16, k=1
			h0, err := bc.DecodeSubexp(td.lrRef[p].filterH[0]+5, 16, 1)
			if err != nil {
				return fmt.Errorf("wiener filter_h[0]: %w", err)
			}
			td.lrRef[p].filterH[0] = h0 - 5
		}

		// filter_h[1]: ref + 23, range 32, k=2
		h1, err := bc.DecodeSubexp(td.lrRef[p].filterH[1]+23, 32, 2)
		if err != nil {
			return fmt.Errorf("wiener filter_h[1]: %w", err)
		}
		td.lrRef[p].filterH[1] = h1 - 23

		// filter_h[2]: ref + 17, range 64, k=3
		h2, err := bc.DecodeSubexp(td.lrRef[p].filterH[2]+17, 64, 3)
		if err != nil {
			return fmt.Errorf("wiener filter_h[2]: %w", err)
		}
		td.lrRef[p].filterH[2] = h2 - 17

		// Store decoded Wiener coefficients.
		params.WienerV = td.lrRef[p].filterV
		params.WienerH = td.lrRef[p].filterH

	} else if lrType == FrameRestoreSGRProj {
		// Read 4 literal bits for the sgrproj index.
		idx, err := bc.ReadLiteral(4)
		if err != nil {
			return fmt.Errorf("sgrproj idx: %w", err)
		}

		sgrEntry := sgrParamsTable[idx]

		// Read up to 2 subexp-coded weights, depending on whether
		// each filter tap is enabled (non-zero parameter).
		if sgrEntry[0] != 0 {
			// sgr_weights[0]: ref + 96, range 128, k=4
			w0, err := bc.DecodeSubexp(td.lrRef[p].sgrWeights[0]+96, 128, 4)
			if err != nil {
				return fmt.Errorf("sgrproj weight[0]: %w", err)
			}
			td.lrRef[p].sgrWeights[0] = w0 - 96
		} else {
			td.lrRef[p].sgrWeights[0] = 0
		}

		if sgrEntry[1] != 0 {
			// sgr_weights[1]: ref + 32, range 128, k=4
			w1, err := bc.DecodeSubexp(td.lrRef[p].sgrWeights[1]+32, 128, 4)
			if err != nil {
				return fmt.Errorf("sgrproj weight[1]: %w", err)
			}
			td.lrRef[p].sgrWeights[1] = w1 - 32
		} else {
			td.lrRef[p].sgrWeights[1] = 95
		}

		// Store decoded SGR parameters.
		params.SGRIdx = int(idx)
		params.SGRWeights = td.lrRef[p].sgrWeights
	}

	// Store the decoded params in the shared LR params array.
	if td.lrParams[p] != nil && unitIdx < len(td.lrParams[p]) {
		td.lrParams[p][unitIdx] = params
	}

	return nil
}
