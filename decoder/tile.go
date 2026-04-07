// Package decoder implements AV1 bitstream decoding.
//
// This file implements tile-level decoding: tile group parsing, per-tile
// entropy decoder initialization, and superblock iteration within tiles.
// AV1 spec Sections 5.11.1 (General Tile Group OBU) and 5.11.2.
package decoder

import (
	"av1go/bitstream"
	"av1go/obu"
	"encoding/binary"
	"fmt"
)

// TileDecoder decodes individual tiles within a frame.
// Each tile has its own entropy decoder state (BoolReader) and may
// optionally have its own CDF context (reset from defaults or copied
// from a reference tile).
type TileDecoder struct {
	fh         *DecodedFrameHeader
	sh         *obu.SequenceHeader
	cdf        *CDFContext
	frame *FrameBuffer // output pixel buffer

	// Tile boundaries in MI units. Used for intra reference availability:
	// dav1d checks t->by > ts->tiling.row_start / t->bx > ts->tiling.col_start
	// instead of frame boundaries.
	tileRowStart int // MI row of tile start
	tileColStart int // MI col of tile start
	tileRowEnd   int // MI row of tile end (exclusive)
	tileColEnd   int // MI col of tile end (exclusive)

	// Above context: stores intra mode per 4x4 block column.
	// Used for context modeling during block decoding.
	aboveModes []int

	// Left context: stores intra mode per 4x4 block row.
	// Reset at the start of each superblock row within the tile.
	leftModes []int

	// UV mode context: stores UV intra mode per MI column/row.
	// Used for chroma smooth-mode flag in intra edge filtering.
	// Matches dav1d's BlockContext.uvmode[].
	aboveUVModes []int
	leftUVModes  []int

	// Partition context: packed bitmask per MI column (above) and row (left).
	// Stores dav1d-style partition context values for bit-shift extraction.
	// Matches dav1d BlockContext.partition[].
	abovePartCtx []uint8
	leftPartCtx  []uint8

	// partCtxAbove/partCtxLeft: overrides for partition context written by
	// setModeInfo. Set by the partition decoder for T-split partitions where
	// the context depends on the parent block level, not the sub-block size.
	// -1 means use the default bW/bH-derived value.
	partCtxAbove int
	partCtxLeft  int

	// Skip context: stores skip flag per MI column (above) and row (left).
	aboveSkip []bool
	leftSkip  []bool

	// Palette size context: stores palette size per MI column (above) and row (left).
	// 0 means no palette. Used for palette_y/palette_uv context derivation.
	abovePalSz []uint8
	leftPalSz  []uint8

	// UV palette size context: separate tracking for UV plane palette neighbor cache.
	// dav1d: pal_sz_uv[0][bx4] (above), pal_sz_uv[1][by4] (left).
	abovePalSzUV []uint8
	leftPalSzUV  []uint8

	// Palette color cache: stores up to 8 sorted palette colors per MI position.
	// [plane][miCol or miRow] = [8]uint8 (colors are sorted ascending).
	// Used to build the palette neighbor cache for delta-coding optimization.
	abovePalColors [3][][8]uint8 // indexed by [plane][miCol]
	leftPalColors  [3][][8]uint8 // indexed by [plane][miRow]

	// Coefficient context per plane: encodes cumulative level (bits [5:0])
	// and DC sign (bits [7:6]) per 4x4 block position.
	// Default/initial value is 0x40 (neutral DC sign, zero level).
	// [3] = Y, U, V planes.
	aboveCoeffCtx [3][]uint8
	leftCoeffCtx  [3][]uint8

	// TX intra context (dav1d's tx_intra): stores log2(dim/4) values.
	// For intra blocks: TX dim log2. For inter blocks: block dim log2.
	// Used in getTxSizeContext (get_tx_ctx). Init: -1.
	aboveTxW []int
	leftTxH  []int

	// TX var context (dav1d's tx): stores log2(dim/4) values.
	// Updated by readVarTxSize leaves and non-vartx inter blocks.
	// Used in readVarTxSize split context. Init: 4 (TX_64X64).
	aboveTx []int
	leftTx  []int

	// CDEF tracking: which 64x64 regions have had their CDEF index read.
	// Key is (cdefRow/16)*256 + (cdefCol/16).
	cdefRead map[uint32]bool

	// Loop restoration reference state per plane.
	// Stores the most recently decoded filter parameters for subexp coding.
	// [3] = Y, U, V planes.
	lrRef [3]lrRefState

	// Inter prediction context: ModeInfo per 4x4 block for above/left neighbors.
	aboveModeInfo []ModeInfo
	leftModeInfo  []ModeInfo

	// Frame-level ModeInfo grid at 4x4 resolution (shared across tiles).
	// Used for multi-row MV scanning and temporal MV saving.
	miGrid *MiGrid

	// Projected temporal MVs at 8x8 resolution (for use_ref_frame_mvs).
	projTMVs   []TemporalMV
	projStride int

	// Reference frame buffers for inter prediction (pointer to Decoder's refFrames).
	refFrames      [8]*FrameBuffer
	refOrderHints  [8]uint32 // Order hints per reference frame slot
	jntWeights     [7][7]int // Distance-weighted compound prediction weights

	// Frame-level filter data (shared across tiles, filled during block decoding).
	deblockInfo [][]DeblockInfo    // [miRow][miCol]
	cdefIndices map[uint32]int     // key=(cdefRow/16)*256+(cdefCol/16), value=cdef index

	// Loop restoration parameters (shared across tiles, filled during LR reading).
	// [plane][unitIdx] — allocated in decoder.go, populated in lr.go.
	lrParams [3][]LRUnitParams

	// Delta Q/LF state per tile. AV1 spec Section 5.11.2/5.11.3.
	// lastQIdx tracks the cumulative quantizer index (initialized to BaseQIndex).
	// lastDeltaLF tracks cumulative loop filter deltas (up to 4 for multi).
	// sbDeltaQDone is reset per-SB; delta_q is read once at the first non-skip block.
	lastQIdx      int
	lastDeltaLF   [4]int
	sbDeltaQDone  bool

	// Luma transform type map: stores the decoded Y txType at each MI position
	// within the current coding block. UV inter blocks derive their txType from
	// the corresponding luma block's txType via get_uv_inter_txtp.
	// Indexed by [miRow-blockMiRow][miCol-blockMiCol]. Reset per coding block.
	txtpMap      [32][32]TxType
	txtpMapMiRow int // MI row origin of current block
	txtpMapMiCol int // MI col origin of current block

}

// FrameBuffer holds decoded pixel data for a frame.
// Planes are stored in planar layout (separate Y, U, V arrays).
type FrameBuffer struct {
	Y       []byte // Luma plane
	U       []byte // Chroma U plane
	V       []byte // Chroma V plane
	StrideY int
	StrideU int
	StrideV int
	Width   int
	Height  int
}

// Clone returns a deep copy of the FrameBuffer.
func (fb *FrameBuffer) Clone() *FrameBuffer {
	c := &FrameBuffer{
		StrideY: fb.StrideY, StrideU: fb.StrideU, StrideV: fb.StrideV,
		Width: fb.Width, Height: fb.Height,
		Y: make([]byte, len(fb.Y)),
		U: make([]byte, len(fb.U)),
		V: make([]byte, len(fb.V)),
	}
	copy(c.Y, fb.Y)
	copy(c.U, fb.U)
	copy(c.V, fb.V)
	return c
}

// NewFrameBuffer allocates a FrameBuffer for the given dimensions and
// chroma subsampling. subsamplingX and subsamplingY are 0 or 1.
// For 4:2:0, both are 1. For 4:4:4, both are 0.
func NewFrameBuffer(width, height int, subsamplingX, subsamplingY int) *FrameBuffer {
	chromaW := (width + subsamplingX) >> subsamplingX
	chromaH := (height + subsamplingY) >> subsamplingY
	// Allocate extra rows beyond the visible frame to match dav1d's
	// behavior. dav1d's frame buffers extend to the full SB grid, so
	// reconstructed pixels past the visible frame boundary are preserved.
	// CDEF direction finding reads these rows, and CFL (chroma-from-luma)
	// AC computation reads luma pixels for the full coding block even when
	// it extends past the visible frame. Use 128 extra rows to cover the
	// maximum 128x128 superblock overshoot, matching dav1d's 128-pixel
	// border allocation.
	extraY := 128
	extraUV := (extraY + subsamplingY) >> subsamplingY
	// Pad strides to the MI grid boundary (multiple of 4 pixels) so that
	// intra prediction reference pixel reads beyond the visible frame width
	// (up to tileColEnd*4) stay within the allocated buffer. Without this
	// padding, reads at the right frame edge wrap into the next row.
	strideY := ((width + 3) >> 2) << 2 // ceil to 4
	strideU := ((chromaW + 3) >> 2) << 2
	strideV := strideU
	return &FrameBuffer{
		Y:       make([]byte, strideY*(height+extraY)),
		U:       make([]byte, strideU*(chromaH+extraUV)),
		V:       make([]byte, strideV*(chromaH+extraUV)),
		StrideY: strideY,
		StrideU: strideU,
		StrideV: strideV,
		Width:   width,
		Height:  height,
	}
}

// DecodeTileGroup parses tile group data and decodes all tiles within it.
// tileData is the raw bytes of the tile group OBU payload (after the
// tile group header has been partially parsed, or the full payload if
// it is a single tile group covering all tiles).
//
// AV1 spec Section 5.11.1 (tile_group_obu).
func DecodeTileGroup(tileData []byte, fh *DecodedFrameHeader, sh *obu.SequenceHeader, frame *FrameBuffer, refFrames [8]*FrameBuffer, refOrderHints [8]uint32, deblockInfo [][]DeblockInfo, cdefIndices map[uint32]int, lrParams [3][]LRUnitParams, initCDF *CDFContext, miGrid *MiGrid, projTMVs []TemporalMV, projStride int) (*CDFContext, error) {
	numTiles := fh.TileCols * fh.TileRows

	// Parse tile group header per AV1 spec Section 5.11.1.
	r := bitstream.NewReader(tileData)

	tgStart := 0
	tgEnd := numTiles - 1

	if numTiles > 1 {
		// tile_start_and_end_present_flag f(1)
		flag, err := r.ReadFlag()
		if err != nil {
			return nil, fmt.Errorf("tile_start_and_end_present_flag: %w", err)
		}
		if flag {
			tileBits := fh.TileColsLog2 + fh.TileRowsLog2
			start, err := r.ReadUint32(tileBits)
			if err != nil {
				return nil, fmt.Errorf("tg_start: %w", err)
			}
			end, err := r.ReadUint32(tileBits)
			if err != nil {
				return nil, fmt.Errorf("tg_end: %w", err)
			}
			tgStart = int(start)
			tgEnd = int(end)
		}
	}

	// byte_alignment() before tile data.
	r.ByteAlign()
	offset := (r.BitsRead() + 7) / 8

	// Decode each tile in the tile group.
	// Track the CDF from the context_update_tile for forward CDF update.
	var savedCDF *CDFContext
	for tileIdx := tgStart; tileIdx <= tgEnd; tileIdx++ {
		var tileSize int
		if tileIdx < tgEnd {
			// Read tile_size_minus_1 in little-endian format.
			// AV1 spec Section 5.11.1: le(TileSizeBytes).
			if fh.TileSizeBytes <= 0 {
				return nil, fmt.Errorf("tile %d: invalid TileSizeBytes=%d", tileIdx, fh.TileSizeBytes)
			}
			if offset+fh.TileSizeBytes > len(tileData) {
				return nil, fmt.Errorf("tile %d: not enough data for tile size", tileIdx)
			}
			sizeBytes := tileData[offset : offset+fh.TileSizeBytes]
			tileSize = int(readLE(sizeBytes, fh.TileSizeBytes)) + 1
			offset += fh.TileSizeBytes
		} else {
			// Last tile consumes the remaining data.
			tileSize = len(tileData) - offset
		}

		if tileSize <= 0 {
			return nil, fmt.Errorf("tile %d: invalid tile size %d", tileIdx, tileSize)
		}
		if offset+tileSize > len(tileData) {
			return nil, fmt.Errorf("tile %d: tile size %d exceeds remaining data %d", tileIdx, tileSize, len(tileData)-offset)
		}

		tileBuf := tileData[offset : offset+tileSize]
		offset += tileSize

		td := newTileDecoder(fh, sh, frame, refFrames, refOrderHints, deblockInfo, cdefIndices, lrParams, miGrid, projTMVs, projStride)
		// Initialize CDF from the provided initial context.
		if initCDF != nil {
			td.cdf = initCDF.Clone()
		}
		if err := td.decodeTile(tileBuf, tileIdx); err != nil {
			return nil, fmt.Errorf("tile %d: %w", tileIdx, err)
		}
		// Save CDF from the context_update_tile (matches dav1d's refresh_context).
		// Only save when disable_frame_end_update_cdf is false.
		// Reset adaptation counts (matches dav1d's dav1d_cdf_thread_update).
		if tileIdx == fh.ContextUpdateTileID && !fh.DisableFrameEndUpdateCDF {
			savedCDF = td.cdf
			savedCDF.ResetAllCounts()
			// For KEY_FRAME/INTRA_ONLY frames, reset inter-only and MV CDFs
			// to defaults. This matches dav1d's dav1d_cdf_thread_update which
			// returns early for IS_KEY_OR_INTRA without copying inter/MV CDFs.
			// Without this, IntraBC adaptation pollutes the MV CDFs saved to
			// reference slots, causing CDF drift in subsequent inter frames.
			frameIsIntra := fh.FrameType == obu.FrameTypeKey || fh.FrameType == obu.FrameTypeIntraOnly
			if frameIsIntra {
				savedCDF.ResetInterMVCDFs()
			}
		}
	}

	return savedCDF, nil
}

// newTileDecoder creates a TileDecoder for one tile.
func newTileDecoder(fh *DecodedFrameHeader, sh *obu.SequenceHeader, frame *FrameBuffer, refFrames [8]*FrameBuffer, refOrderHints [8]uint32, deblockInfo [][]DeblockInfo, cdefIndices map[uint32]int, lrParams [3][]LRUnitParams, miGrid *MiGrid, projTMVs []TemporalMV, projStride int) *TileDecoder {
	// Context arrays must extend to the SB grid boundary, not just MiRows/MiCols.
	// Blocks at frame edges extend beyond MiRows/MiCols within their SB, and
	// coefficient context reads/writes at those positions must succeed (not
	// fall back to defaults). dav1d allocates context to the aligned SB grid.
	sbMiSize := 16 // 64x64 SB
	if sh.Use128x128Superblock {
		sbMiSize = 32
	}
	alignedMiRows := ((int(fh.MiRows) + sbMiSize - 1) / sbMiSize) * sbMiSize

	td := &TileDecoder{
		fh:           fh,
		sh:           sh,
		cdf:          NewDefaultCDFContext(),
		frame:        frame,
		aboveModes:   make([]int, fh.MiCols),
		leftModes:    make([]int, alignedMiRows),
		aboveUVModes: make([]int, fh.MiCols),
		leftUVModes:  make([]int, alignedMiRows),
		abovePartCtx: make([]uint8, fh.MiCols),
		leftPartCtx:  make([]uint8, alignedMiRows),
		aboveSkip:    make([]bool, fh.MiCols),
		leftSkip:     make([]bool, alignedMiRows),
		abovePalSz:   make([]uint8, fh.MiCols),
		leftPalSz:    make([]uint8, alignedMiRows),
		abovePalSzUV: make([]uint8, fh.MiCols),
		leftPalSzUV:  make([]uint8, alignedMiRows),
		aboveTxW:      make([]int, fh.MiCols),
		leftTxH:       make([]int, alignedMiRows),
		aboveTx:       make([]int, fh.MiCols),
		leftTx:        make([]int, alignedMiRows),
		cdefRead:      make(map[uint32]bool),
		aboveModeInfo: initModeInfoArray(int(fh.MiCols)),
		leftModeInfo:  initModeInfoArray(alignedMiRows),
		miGrid:        miGrid,
		projTMVs:      projTMVs,
		projStride:    projStride,
		refFrames:     refFrames,
		refOrderHints: refOrderHints,
		deblockInfo:   deblockInfo,
		cdefIndices:   cdefIndices,
		lrParams:      lrParams,
		partCtxAbove:  -1,
		partCtxLeft:   -1,
	}
	// Initialize palette color cache arrays (3 planes).
	for p := 0; p < 3; p++ {
		td.abovePalColors[p] = make([][8]uint8, fh.MiCols)
		td.leftPalColors[p] = make([][8]uint8, alignedMiRows)
	}
	// Initialize coefficient context arrays (3 planes) with default value 0x40.
	for p := 0; p < 3; p++ {
		td.aboveCoeffCtx[p] = make([]uint8, fh.MiCols)
		td.leftCoeffCtx[p] = make([]uint8, alignedMiRows)
		for i := range td.aboveCoeffCtx[p] {
			td.aboveCoeffCtx[p][i] = 0x40
		}
		for i := range td.leftCoeffCtx[p] {
			td.leftCoeffCtx[p][i] = 0x40
		}
	}
	// Initialize TX context arrays.
	// tx_intra (aboveTxW): init to -1 (dav1d: memset(ctx->tx_intra, -1, ...))
	// tx (aboveTx): init to 4 = TX_64X64 log2 (dav1d: memset(ctx->tx, TX_64X64, ...))
	for i := range td.aboveTxW {
		td.aboveTxW[i] = -1
	}
	for i := range td.aboveTx {
		td.aboveTx[i] = 4
	}
	// Initialize delta Q state: start at frame-level BaseQIndex.
	td.lastQIdx = int(fh.BaseQIndex)

	// Initialize loop restoration reference state with dav1d defaults.
	// Matches dav1d decode.c lines 2495-2502.
	for p := 0; p < 3; p++ {
		td.lrRef[p] = lrRefState{
			filterV:    [3]int{3, -7, 15},
			filterH:    [3]int{3, -7, 15},
			sgrWeights: [2]int{-32, 31},
		}
	}
	// Compute jnt_comp distance-weighted blending weights per reference pair.
	// Matches dav1d decode.c setup_jnt_comp.
	if sh.EnableJNTComp {
		orderHintBits := int(sh.OrderHintBitsMinus1) + 1
		curOH := int(fh.OrderHint)
		quantDistWeight := [3][2]int{{2, 3}, {2, 5}, {2, 7}}
		quantDistLUT := [4][2]int{{9, 7}, {11, 5}, {12, 4}, {13, 3}}
		for i := 0; i < 7; i++ {
			refI := fh.RefFrameIdx[i]
			ref0poc := int(refOrderHints[refI])
			for j := i + 1; j < 7; j++ {
				refJ := fh.RefFrameIdx[j]
				ref1poc := int(refOrderHints[refJ])
				d1 := abs(getPocDiff(orderHintBits, ref0poc, curOH))
				if d1 > 31 {
					d1 = 31
				}
				d0 := abs(getPocDiff(orderHintBits, ref1poc, curOH))
				if d0 > 31 {
					d0 = 31
				}
				order := 0
				if d0 <= d1 {
					order = 1
				}
				k := 0
				for k = 0; k < 3; k++ {
					c0 := quantDistWeight[k][order]
					c1 := quantDistWeight[k][1-order]
					d0c0 := d0 * c0
					d1c1 := d1 * c1
					if (d0 > d1 && d0c0 < d1c1) || (d0 <= d1 && d0c0 > d1c1) {
						break
					}
				}
				td.jntWeights[i][j] = quantDistLUT[k][order]
				td.jntWeights[j][i] = quantDistLUT[k][order]
			}
		}
	}
	return td
}

// decodeTile decodes a single tile identified by tileIdx.
// The data slice contains the tile's entropy-coded bitstream.
//
// AV1 spec Section 5.11.2 (decode_tile).
func (td *TileDecoder) decodeTile(data []byte, tileIdx int) error {
	bc := NewBoolReaderWithCDFUpdate(data, !td.fh.DisableCDFUpdate)

	tileCol := tileIdx % td.fh.TileCols
	tileRow := tileIdx / td.fh.TileCols

	miColStart := td.fh.TileColStarts[tileCol]
	miColEnd := td.fh.TileColStarts[tileCol+1]
	miRowStart := td.fh.TileRowStarts[tileRow]
	miRowEnd := td.fh.TileRowStarts[tileRow+1]
	// Store tile boundaries for intra reference availability checks.
	td.tileRowStart = miRowStart
	td.tileColStart = miColStart
	td.tileRowEnd = miRowEnd
	td.tileColEnd = miColEnd

	// Superblock size in MI units.
	// AV1 spec: 128x128 superblock = 32 MI units, 64x64 = 16 MI units.
	sbSize := 16 // MI units for 64x64
	if td.sh.Use128x128Superblock {
		sbSize = 32 // MI units for 128x128
	}

	// Iterate over superblocks in raster order within the tile.
	// AV1 spec Section 5.11.2: for each SB row, then each SB column.
	for miRow := miRowStart; miRow < miRowEnd; miRow += sbSize {
		// Reset left context at the start of each superblock row.
		for r := miRow; r < miRow+sbSize && r < len(td.leftModes); r++ {
			td.leftModes[r] = DC_PRED
			if r < len(td.leftUVModes) {
				td.leftUVModes[r] = DC_PRED
			}
			if r < len(td.leftPartCtx) {
				td.leftPartCtx[r] = 0
			}
			if r < len(td.leftSkip) {
				td.leftSkip[r] = false
			}
			if r < len(td.leftTxH) {
				td.leftTxH[r] = -1 // dav1d: memset(ctx->tx_intra, -1, ...)
			}
			if r < len(td.leftTx) {
				td.leftTx[r] = 4 // dav1d: memset(ctx->tx, TX_64X64, ...)
			}
			if r < len(td.leftPalSz) {
				td.leftPalSz[r] = 0
			}
			if r < len(td.leftPalSzUV) {
				td.leftPalSzUV[r] = 0
			}
			for p := 0; p < 3; p++ {
				if r < len(td.leftPalColors[p]) {
					td.leftPalColors[p][r] = [8]uint8{}
				}
				if r < len(td.leftCoeffCtx[p]) {
					td.leftCoeffCtx[p][r] = 0x40
				}
			}
		}

		for miCol := miColStart; miCol < miColEnd; miCol += sbSize {
			if err := td.decodeSuperblock(bc, miRow, miCol, sbSize); err != nil {
				return err
			}
		}
	}

	return nil
}

// readLE reads a little-endian unsigned integer of n bytes (1..4).
// AV1 spec Section 4.10.3 (le(n)).
func readLE(data []byte, n int) uint32 {
	switch n {
	case 1:
		return uint32(data[0])
	case 2:
		return uint32(binary.LittleEndian.Uint16(data))
	case 3:
		return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
	case 4:
		return binary.LittleEndian.Uint32(data)
	}
	return 0
}
