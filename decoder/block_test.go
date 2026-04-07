package decoder

import (
	"testing"
)

// --- Scan order tests ---

func TestGenerateScanOrder4x4(t *testing.T) {
	scan := generateScanOrder(4, 4, TX_CLASS_2D, TX_4X4)
	if len(scan) != 16 {
		t.Fatalf("expected 16 entries, got %d", len(scan))
	}
	// First entry must be DC (position 0).
	if scan[0] != 0 {
		t.Errorf("scan[0] = %d, want 0 (DC)", scan[0])
	}
	// Verify all positions appear exactly once.
	seen := make(map[int]bool)
	for i, pos := range scan {
		if pos < 0 || pos >= 16 {
			t.Errorf("scan[%d] = %d, out of range [0,15]", i, pos)
		}
		if seen[pos] {
			t.Errorf("scan[%d] = %d, duplicate position", i, pos)
		}
		seen[pos] = true
	}
}

func TestGenerateScanOrder8x8(t *testing.T) {
	scan := generateScanOrder(8, 8, TX_CLASS_2D, TX_8X8)
	if len(scan) != 64 {
		t.Fatalf("expected 64 entries, got %d", len(scan))
	}
	// First entry must be DC (position 0).
	if scan[0] != 0 {
		t.Errorf("scan[0] = %d, want 0 (DC)", scan[0])
	}
	// Second entry should be position 1 (row 0, col 1).
	if scan[1] != 1 {
		t.Errorf("scan[1] = %d, want 1", scan[1])
	}
	// Third entry should be position 8 (row 1, col 0) for zig-zag.
	if scan[2] != 8 {
		t.Errorf("scan[2] = %d, want 8", scan[2])
	}
	// Verify all positions appear exactly once.
	seen := make(map[int]bool)
	for i, pos := range scan {
		if pos < 0 || pos >= 64 {
			t.Errorf("scan[%d] = %d, out of range [0,63]", i, pos)
		}
		if seen[pos] {
			t.Errorf("scan[%d] = %d, duplicate position", i, pos)
		}
		seen[pos] = true
	}
	if len(seen) != 64 {
		t.Errorf("only %d unique positions, want 64", len(seen))
	}
}

func TestGenerateScanOrder4x8(t *testing.T) {
	scan := generateScanOrder(4, 8, TX_CLASS_2D, TX_4X8)
	if len(scan) != 32 {
		t.Fatalf("expected 32 entries, got %d", len(scan))
	}
	// Verify all positions appear exactly once.
	seen := make(map[int]bool)
	for _, pos := range scan {
		if pos < 0 || pos >= 32 {
			t.Errorf("position %d out of range [0,31]", pos)
		}
		seen[pos] = true
	}
	if len(seen) != 32 {
		t.Errorf("only %d unique positions, want 32", len(seen))
	}
}

// --- TX size mapping tests ---

func TestBlockSizeToTxSize(t *testing.T) {
	tests := []struct {
		w, h   int
		expect TxSize
	}{
		{4, 4, TX_4X4},
		{8, 8, TX_8X8},
		{16, 16, TX_16X16},
		{32, 32, TX_32X32},
		{64, 64, TX_64X64},
		{4, 8, TX_4X8},
		{8, 4, TX_8X4},
		{8, 16, TX_8X16},
		{16, 8, TX_16X8},
		{16, 32, TX_16X32},
		{32, 16, TX_32X16},
	}
	for _, tc := range tests {
		txSz := blockSizeToTxSize(tc.w, tc.h)
		if txSz != tc.expect {
			t.Errorf("blockSizeToTxSize(%d,%d) = %d, want %d", tc.w, tc.h, txSz, tc.expect)
		}
	}
}

// --- EOB mapping tests ---

func TestEobPtToEob(t *testing.T) {
	tests := []struct {
		eobPt        int
		expectBase   int
		expectExBits int
	}{
		{0, 0, 0},
		{1, 1, 0},
		{2, 2, 0},
		{3, 3, 1},
		{4, 5, 1},
		{5, 9, 2},
		{6, 17, 3},
		{7, 33, 4},
		{8, 65, 5},
		{9, 129, 6},
		{10, 257, 7},
	}
	for _, tc := range tests {
		base, bits := eobPtToEob(tc.eobPt)
		if base != tc.expectBase || bits != tc.expectExBits {
			t.Errorf("eobPtToEob(%d) = (%d, %d), want (%d, %d)",
				tc.eobPt, base, bits, tc.expectBase, tc.expectExBits)
		}
	}
}

func TestEobMultiSize(t *testing.T) {
	tests := []struct {
		numCoeffs int
		expect    int
	}{
		{16, 0},
		{32, 1},
		{64, 2},
		{128, 3},
		{256, 4},
		{512, 5},
		{1024, 6},
	}
	for _, tc := range tests {
		result := eobMultiSize(tc.numCoeffs)
		if result != tc.expect {
			t.Errorf("eobMultiSize(%d) = %d, want %d", tc.numCoeffs, result, tc.expect)
		}
	}
}

// --- Context derivation tests ---

func TestGetCoeffBaseCtx_DC(t *testing.T) {
	levels := make([]int32, 16)
	// DC position with no neighbors set should give context 0.
	ctx := getCoeffBaseCtx(levels, 0, 4, 4, 4, 4, 0)
	if ctx != 0 {
		t.Errorf("DC with zero neighbors: ctx = %d, want 0", ctx)
	}
}

func TestGetCoeffBaseCtx_WithNeighbors(t *testing.T) {
	levels := make([]int32, 16)
	// Set some neighbor levels near DC.
	levels[1] = 2 // right of DC
	levels[4] = 3 // below DC
	ctx := getCoeffBaseCtx(levels, 0, 4, 4, 4, 4, 0)
	// DC position: mag = min(2,3) + min(3,3) + min(diag,3) = 2+3+0 = 5, capped at 4.
	// DC offset = 0, so ctx = 4.
	if ctx != 4 {
		t.Errorf("DC with neighbors: ctx = %d, want 4", ctx)
	}
}

func TestGetCoeffBaseEobCtx(t *testing.T) {
	if getCoeffBaseEobCtx(0, 1) != 0 {
		t.Error("eob=1 should give ctx=0")
	}
	if getCoeffBaseEobCtx(2, 3) != 1 {
		t.Error("eob=3 should give ctx=1")
	}
	if getCoeffBaseEobCtx(10, 15) != 2 {
		t.Error("eob=15 should give ctx=2")
	}
}

func TestGetCoeffBRCtx_DC(t *testing.T) {
	levels := make([]int32, 16)
	ctx := getCoeffBRCtx(levels, 0, 4, 4, TX_CLASS_2D)
	if ctx != 0 {
		t.Errorf("DC with zero neighbors: BR ctx = %d, want 0", ctx)
	}
}

func TestGetCoeffBRCtx_Clamped(t *testing.T) {
	levels := make([]int32, 16)
	levels[1] = 20  // right of DC, high level
	levels[4] = 100 // below DC, very high level
	ctx := getCoeffBRCtx(levels, 0, 4, 4, TX_CLASS_2D)
	// mag = min(20,14) + min(100,14) = 14+14 = 28, capped at 6.
	// DC offset = 0, so ctx = 6.
	if ctx != 6 {
		t.Errorf("DC with high neighbors: BR ctx = %d, want 6", ctx)
	}
}

func TestTxSizeCtx(t *testing.T) {
	tests := []struct {
		txSz   TxSize
		expect int
	}{
		{TX_4X4, 0},
		{TX_8X8, 1},
		{TX_16X16, 2},
		{TX_32X32, 3},
		{TX_64X64, 4},
		{TX_4X8, 1},
		{TX_8X16, 2},
	}
	for _, tc := range tests {
		result := txSizeCtx(tc.txSz)
		if result != tc.expect {
			t.Errorf("txSizeCtx(%d) = %d, want %d", tc.txSz, result, tc.expect)
		}
	}
}
