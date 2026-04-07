package encoder

import (
	"av1go/bitstream"
	"av1go/obu"
	"encoding/binary"
)

// TileEncoder encodes tile groups for AV1 frames.
//
// For the initial implementation, this produces minimal valid tile data
// where every superblock has skip=1 (all zero coefficients) and uses
// DC_PRED for intra prediction, resulting in a flat mid-gray frame.
type TileEncoder struct {
	seqHdr    *obu.SequenceHeader
	fhParams  *obu.FrameHeaderParams
	config    *Config
}

// NewTileEncoder creates a tile encoder for the given frame parameters.
func NewTileEncoder(sh *obu.SequenceHeader, fhp *obu.FrameHeaderParams, cfg *Config) *TileEncoder {
	return &TileEncoder{
		seqHdr:   sh,
		fhParams: fhp,
		config:   cfg,
	}
}

// AV1 partition type constants.
const (
	partitionNone       = 0
	partitionHorz       = 1
	partitionVert       = 2
	partitionSplit      = 3
)

// AV1 intra prediction mode constants.
const (
	dcPred    = 0
	vPred     = 1
	hPred     = 2
	uvModeDC  = 0
)

// Block sizes.
const (
	block128x128 = 0
	block64x64   = 1
)

// EncodeFrame produces a complete Frame OBU (frame header + tile group)
// for the given frame.
func (te *TileEncoder) EncodeFrame(frame *Frame, isKey bool) ([]byte, error) {
	// Write the frame header.
	fhBytes, err := obu.WriteFrameHeader(te.fhParams, te.seqHdr)
	if err != nil {
		return nil, err
	}

	// Write the tile group data.
	tileData := te.encodeTileGroup()

	// A Frame OBU combines frame header + tile group in one payload.
	// The payload is: frame_header_bytes + tile_group_data
	payload := make([]byte, 0, len(fhBytes)+len(tileData))
	payload = append(payload, fhBytes...)
	payload = append(payload, tileData...)

	return payload, nil
}

// encodeTileGroup produces the tile_group_obu() data.
// AV1 spec Section 5.11.1.
func (te *TileEncoder) encodeTileGroup() []byte {
	numTiles := te.fhParams.NumTiles()

	// Start with a bitstream writer for tile group header.
	w := bitstream.NewWriter(64)

	if numTiles > 1 {
		tileBits := te.fhParams.TileInfo.TileColsLog2 + te.fhParams.TileInfo.TileRowsLog2
		// tg_start f(tileBits)
		w.WriteBits(0, tileBits)
		// tg_end f(tileBits)
		w.WriteBits(uint64(numTiles-1), tileBits)
	}

	// byte_alignment() — pad to byte boundary before tile data.
	w.WriteByteAlignment()
	header := w.Bytes()

	// Encode each tile.
	tileDataParts := make([][]byte, numTiles)
	for i := 0; i < numTiles; i++ {
		tileDataParts[i] = te.encodeTile(i)
	}

	// Assemble: header + tile sizes + tile data.
	tileSizeBytes := te.fhParams.TileSizeBytes()
	totalSize := len(header)
	for i, td := range tileDataParts {
		if i < numTiles-1 && tileSizeBytes > 0 {
			totalSize += tileSizeBytes
		}
		totalSize += len(td)
	}

	result := make([]byte, 0, totalSize)
	result = append(result, header...)

	for i, td := range tileDataParts {
		// Write tile_size_minus_1 in LE for all but the last tile.
		if i < numTiles-1 && tileSizeBytes > 0 {
			sizeLE := make([]byte, 4)
			binary.LittleEndian.PutUint32(sizeLE, uint32(len(td)-1))
			result = append(result, sizeLE[:tileSizeBytes]...)
		}
		result = append(result, td...)
	}

	return result
}

// encodeTile encodes a single tile's worth of data.
// For the minimal implementation, this produces valid AV1 syntax where
// every superblock uses skip=1 and DC_PRED.
func (te *TileEncoder) encodeTile(tileIdx int) []byte {
	bc := NewBoolWriter(1024)

	// Compute tile dimensions in superblocks.
	width := te.seqHdr.MaxFrameWidthMinus1 + 1
	height := te.seqHdr.MaxFrameHeightMinus1 + 1

	miCols := 2 * ((width + 7) >> 3)
	miRows := 2 * ((height + 7) >> 3)

	var sbSize uint32
	if te.seqHdr.Use128x128Superblock {
		sbSize = 32 // 128 pixels / 4 = 32 MI units
	} else {
		sbSize = 16 // 64 pixels / 4 = 16 MI units
	}

	sbCols := (miCols + sbSize - 1) / sbSize
	sbRows := (miRows + sbSize - 1) / sbSize

	// For single tile, encode all superblocks.
	// For multi-tile, we'd compute the tile's SB range.
	for sbRow := uint32(0); sbRow < sbRows; sbRow++ {
		for sbCol := uint32(0); sbCol < sbCols; sbCol++ {
			te.encodeSuperblock(bc, sbRow, sbCol, sbSize)
		}
	}

	return bc.Finalize()
}

// encodeSuperblock encodes a single superblock with skip=1 (all zero).
//
// The encoding must produce valid AV1 syntax. For a keyframe with skip=1:
// 1. Partition: PARTITION_NONE (no splitting)
// 2. Skip flag: 1 (skip all coefficients)
// 3. Segment ID: not written (segmentation disabled)
// 4. Intra mode: DC_PRED (predicted from neighbors)
// 5. No transform coefficients (skip=1)
func (te *TileEncoder) encodeSuperblock(bc *BoolWriter, sbRow, sbCol, sbSize uint32) {
	// Partition tree: PARTITION_NONE at the top level.
	// In AV1, partition is signaled using CDF-based symbol coding.
	// For PARTITION_NONE, the symbol is 0.
	//
	// Use a flat CDF for partition type (4 symbols for non-edge blocks).
	partCDF := InitCDF(4)
	bc.WriteSymbol(partitionNone, partCDF, 4)

	// For the block at this superblock level:
	// skip f(1) — using CDF
	skipCDF := InitCDF(2)
	bc.WriteSymbol(1, skipCDF, 2) // skip = 1

	// intra_frame_y_mode — DC_PRED (symbol 0)
	// CDF for 13 intra modes.
	yModeCDF := InitCDF(13)
	bc.WriteSymbol(dcPred, yModeCDF, 13)

	// uv_mode — DC_PRED (symbol 0)
	// CDF for UV modes (13 without CfL, 14 with CfL).
	// For skip blocks, UV mode is still coded.
	uvModeCDF := InitCDF(13)
	bc.WriteSymbol(uvModeDC, uvModeCDF, 13)

	// With skip=1, no transform coefficients are written.
	// No delta_q, no CDEF index per-block either (those are at SB level
	// and only written when delta_q is present).
}
