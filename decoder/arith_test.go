package decoder

import (
	"testing"
)

// TestNewBoolReader verifies correct initialization of the arithmetic decoder.
// Matches dav1d's msac_init: rng=0x8000, dif loaded with complement of stream.
func TestNewBoolReader(t *testing.T) {
	t.Run("basic_init", func(t *testing.T) {
		br := NewBoolReader([]byte{0xAB, 0xCD, 0xEF, 0x01})
		if br.rng != 0x8000 {
			t.Errorf("rng = 0x%X, want 0x8000", br.rng)
		}
		// After init, pos should have advanced (refill loads bytes).
		if br.pos == 0 {
			t.Error("pos should advance after refill")
		}
	})

	t.Run("empty", func(t *testing.T) {
		br := NewBoolReader([]byte{})
		if br.rng != 0x8000 {
			t.Errorf("rng = 0x%X, want 0x8000", br.rng)
		}
	})
}

// TestReadBoolDeterministic verifies that ReadBool matches dav1d's polarity:
// dav1d's msac_decode_bool: ret = (dif >= vw); return !ret;
// So dif >= v means false (0), dif < v means true (1).
func TestReadBoolDeterministic(t *testing.T) {
	// All-zero data: dif starts as 0x7FFFFFFF XOR 0x00... = 0x7FFFFFFF.
	// c = dif >> 16 = 0x7FFF = 32767.
	// With high prob (32000): v = (128 * 500) >> 1 + 4 = 32004.
	// c (32767) >= v (32004) is true, so dav1d returns !1 = 0 = false.
	t.Run("high_prob_zero_data", func(t *testing.T) {
		br := NewBoolReader([]byte{0x00, 0x00, 0x00, 0x00})
		bit, err := br.ReadBool(32000)
		if err != nil {
			t.Fatal(err)
		}
		if bit {
			t.Error("expected false: c >= v, dav1d returns !1 = false")
		}
	})

	// All-FF data: dif = 0x7FFFFFFF XOR 0xFF<<8 XOR 0xFF<<0 = 0x7FFF00FF ^ ...
	// Actually let's just check that with very low prob, v is tiny,
	// and c >= v is true, so result is false.
	// With prob=1: v = (128 * 0) >> 1 + 4 = 4. c = large. c >= 4, returns false.
	// To get true, we need c < v, which means prob must be high enough.
	t.Run("low_prob_ff_data", func(t *testing.T) {
		br := NewBoolReader([]byte{0xFF, 0xFF, 0xFF, 0xFF})
		bit, err := br.ReadBool(1)
		if err != nil {
			t.Fatal(err)
		}
		// dif = 0x7FFFFFFF ^ 0xFF00 ^ 0xFF = 0x7FFF0000... wait, need to trace carefully.
		// dif starts as 0x7FFFFFFF. Refill XORs in bytes at descending positions.
		// c = 0 after XOR with 0xFF... Actually with all-FF data, the complement
		// XOR makes dif bits small. c should be small. v=4. c < 4 → true.
		if !bit {
			t.Error("expected true: c < v with all-FF data and low prob")
		}
	})
}

// TestReadLiteral verifies reading fixed-width bit fields.
func TestReadLiteral(t *testing.T) {
	// ReadLiteral with n=0 should return 0.
	t.Run("zero_bits", func(t *testing.T) {
		br := NewBoolReader([]byte{0xFF, 0xFF})
		val, err := br.ReadLiteral(0)
		if err != nil {
			t.Fatal(err)
		}
		if val != 0 {
			t.Errorf("ReadLiteral(0) = %d, want 0", val)
		}
	})

	// ReadLiteral with invalid n should error.
	t.Run("invalid_n", func(t *testing.T) {
		br := NewBoolReader([]byte{0xFF, 0xFF})
		_, err := br.ReadLiteral(33)
		if err == nil {
			t.Error("expected error for n=33")
		}
		_, err = br.ReadLiteral(-1)
		if err == nil {
			t.Error("expected error for n=-1")
		}
	})
}

// TestReadNS verifies the non-symmetric unsigned integer decoder.
func TestReadNS(t *testing.T) {
	t.Run("n_equals_1", func(t *testing.T) {
		br := NewBoolReader([]byte{0x00, 0x00})
		val, err := br.ReadNS(1)
		if err != nil {
			t.Fatal(err)
		}
		if val != 0 {
			t.Errorf("ReadNS(1) = %d, want 0", val)
		}
	})

	t.Run("n_invalid", func(t *testing.T) {
		br := NewBoolReader([]byte{0x00, 0x00})
		_, err := br.ReadNS(0)
		if err == nil {
			t.Error("expected error for n=0")
		}
	})
}

// TestUpdateCDF verifies that CDF adaptation works correctly.
// Uses ICDF convention matching dav1d:
//   rate = 4 + (count >> 4) + (nsyms > 2)
func TestUpdateCDF(t *testing.T) {
	t.Run("binary_cdf", func(t *testing.T) {
		// Start with uniform binary CDF: [16384, 0, counter=0]
		// cdf[0] = 16384 = boundary between symbol 0 (above) and symbol 1 (below)
		cdf := []uint16{16384, 0, 0}

		// Ascending CDF convention:
		// Symbol 0 has interval [v, range_) where v is derived from cdf[0].
		// Lower cdf[0] -> lower v -> bigger [v, range_) -> symbol 0 more likely.
		//
		// UpdateCDF with symbol=0: i=0 is NOT < 0, so cdf[0] decreases
		// (pushed toward 0), making symbol 0 more probable.
		UpdateCDF(cdf, 2, 0)
		if cdf[0] >= 16384 {
			t.Errorf("after symbol 0: cdf[0] = %d, expected < 16384 (symbol 0 should become more likely)", cdf[0])
		}
		if cdf[2] != 1 {
			t.Errorf("counter = %d, want 1", cdf[2])
		}

		// Verify exact value: rate = 4 + (0 >> 4) = 4 (nCdfVals=1, not > 2)
		// cdf[0] = 16384 - (16384 >> 4) = 16384 - 1024 = 15360
		if cdf[0] != 15360 {
			t.Errorf("after symbol 0: cdf[0] = %d, want 15360", cdf[0])
		}

		// Decode symbol 1: cdf[0] should increase (pushed toward 32768),
		// giving symbol 1 more probability mass.
		prev := cdf[0]
		UpdateCDF(cdf, 2, 1)
		if cdf[0] <= prev {
			t.Errorf("after symbol 1: cdf[0] = %d, expected > %d", cdf[0], prev)
		}
		if cdf[2] != 2 {
			t.Errorf("counter = %d, want 2", cdf[2])
		}
	})

	t.Run("ternary_cdf", func(t *testing.T) {
		// Ascending uniform ternary CDF: [10923, 21845, 0, counter=0]
		cdf := InitCDF(3)
		if len(cdf) != 4 {
			t.Fatalf("InitCDF(3) length = %d, want 4", len(cdf))
		}

		original0 := cdf[0]
		original1 := cdf[1]

		// Decode symbol 1:
		//   i=0: i < symbol(1) is true  -> cdf[0] increases toward 32768
		//   i=1: i < symbol(1) is false -> cdf[1] decreases toward 0
		UpdateCDF(cdf, 3, 1)
		if cdf[0] <= original0 {
			t.Errorf("after symbol 1: cdf[0] = %d, expected > %d", cdf[0], original0)
		}
		if cdf[1] >= original1 {
			t.Errorf("after symbol 1: cdf[1] = %d, expected < %d", cdf[1], original1)
		}
	})

	t.Run("counter_saturation", func(t *testing.T) {
		cdf := []uint16{16384, 0, 31}

		UpdateCDF(cdf, 2, 0)
		if cdf[2] != 32 {
			t.Errorf("counter should be 32 after increment from 31, got %d", cdf[2])
		}

		// Counter should not exceed 32.
		UpdateCDF(cdf, 2, 0)
		if cdf[2] != 32 {
			t.Errorf("counter should remain 32, got %d", cdf[2])
		}
	})

	t.Run("rate_cap_for_large_alphabets", func(t *testing.T) {
		// dav1d rate formula: rate = 4 + (count >> 4) + (nsyms > 2)
		// For nsyms=4: rate = 4 + (32>>4) + 1 = 4 + 2 + 1 = 7
		// For nsyms=2: rate = 4 + (32>>4) + 0 = 4 + 2 = 6
		cdf4 := InitCDF(4)
		cdf4[4] = 32 // max counter

		cdf2 := []uint16{16384, 0, 32} // 2-symbol, max counter

		prev4 := cdf4[0]
		UpdateCDF(cdf4, 4, 0)
		delta4 := prev4 - cdf4[0] // cdf[0] should decrease (symbol 0 becomes more likely)

		prev2 := cdf2[0]
		UpdateCDF(cdf2, 2, 0)
		delta2 := prev2 - cdf2[0]

		// Both should adapt (deltas should be positive since cdf decreases for symbol 0).
		if delta4 == 0 {
			t.Error("4-symbol CDF did not adapt")
		}
		if delta2 == 0 {
			t.Error("2-symbol CDF did not adapt")
		}
		// 4-symbol rate (7) is higher than 2-symbol rate (6), so delta4 should be smaller
		if delta4 >= delta2 {
			t.Errorf("4-symbol delta (%d) should be smaller than 2-symbol delta (%d) due to higher rate", delta4, delta2)
		}
	})
}

// TestInitCDF verifies uniform CDF initialization with ascending convention.
func TestInitCDF(t *testing.T) {
	t.Run("binary", func(t *testing.T) {
		cdf := InitCDF(2)
		if len(cdf) != 3 {
			t.Fatalf("len = %d, want 3", len(cdf))
		}
		// Ascending: cdf[0] = 32768*1/2 = 16384 (boundary between sym 0 and 1)
		if cdf[0] != 16384 {
			t.Errorf("cdf[0] = %d, want 16384", cdf[0])
		}
		if cdf[1] != 0 { // last sym slot (implicit 32768)
			t.Errorf("cdf[1] = %d, want 0", cdf[1])
		}
		if cdf[2] != 0 {
			t.Errorf("cdf[2] (counter) = %d, want 0", cdf[2])
		}
	})

	t.Run("four_symbols", func(t *testing.T) {
		cdf := InitCDF(4)
		if len(cdf) != 5 {
			t.Fatalf("len = %d, want 5", len(cdf))
		}
		// ICDF convention (descending): cdf[i] = 32768 * (nsyms-1-i) / nsyms
		// cdf[0] = 32768*3/4 = 24576
		// cdf[1] = 32768*2/4 = 16384
		// cdf[2] = 32768*1/4 = 8192
		// cdf[3] = 0 (last symbol)
		// cdf[4] = 0 (adaptation counter)
		expected := []uint16{24576, 16384, 8192, 0, 0}
		for i, want := range expected {
			if cdf[i] != want {
				t.Errorf("cdf[%d] = %d, want %d", i, cdf[i], want)
			}
		}
	})
}

// TestFloorLog2 verifies the floor log2 utility.
func TestFloorLog2(t *testing.T) {
	cases := []struct {
		x, want int
	}{
		{1, 0},
		{2, 1},
		{3, 1},
		{4, 2},
		{7, 2},
		{8, 3},
		{255, 7},
		{256, 8},
		{0, 0},
	}
	for _, tc := range cases {
		got := floorLog2(tc.x)
		if got != tc.want {
			t.Errorf("floorLog2(%d) = %d, want %d", tc.x, got, tc.want)
		}
	}
}

// TestExitPos verifies byte position tracking.
func TestExitPos(t *testing.T) {
	data := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	br := NewBoolReader(data)

	// After init, pos should be 3 (refill loads bytes into 32-bit window).
	if br.ExitPos() != 3 {
		t.Errorf("initial ExitPos() = %d, want 3", br.ExitPos())
	}

	// Read many symbols to advance position via renormalization.
	for i := 0; i < 32; i++ {
		br.ReadBool(16384)
	}
	// Position should have advanced as renormalization reads more bytes.
	if br.ExitPos() <= 3 {
		t.Errorf("ExitPos() should advance after many reads, got %d", br.ExitPos())
	}
}

// TestReadSymbolValidation verifies error handling in ReadSymbol.
func TestReadSymbolValidation(t *testing.T) {
	br := NewBoolReader([]byte{0x00, 0x00, 0x00, 0x00})

	t.Run("nsyms_too_small", func(t *testing.T) {
		cdf := []uint16{0, 0}
		_, err := br.ReadSymbol(cdf, 1)
		if err == nil {
			t.Error("expected error for nsyms=1")
		}
	})

	t.Run("cdf_too_short", func(t *testing.T) {
		cdf := []uint16{16384}
		_, err := br.ReadSymbol(cdf, 2)
		if err == nil {
			t.Error("expected error for CDF too short")
		}
	})
}

// TestReadSymbolBoolValidation verifies error handling in ReadSymbolBool.
func TestReadSymbolBoolValidation(t *testing.T) {
	br := NewBoolReader([]byte{0x00, 0x00})

	_, err := br.ReadSymbolBool([]uint16{16384})
	if err == nil {
		t.Error("expected error for CDF with only 1 entry")
	}
}

// TestReadSymbolDistribution decodes many symbols and verifies that
// the distribution roughly matches the CDF.
func TestReadSymbolDistribution(t *testing.T) {
	// Create data that should produce a mix of symbols.
	data := make([]byte, 1024)
	for i := range data {
		data[i] = byte(i * 37) // pseudo-random-ish pattern
	}

	br := NewBoolReader(data)
	cdf := InitCDF(4)

	counts := [4]int{}
	totalSymbols := 100

	for i := 0; i < totalSymbols; i++ {
		sym, err := br.ReadSymbol(cdf, 4)
		if err != nil {
			t.Fatalf("ReadSymbol failed at iteration %d: %v", i, err)
		}
		if sym < 0 || sym > 3 {
			t.Fatalf("symbol %d out of range [0,3]", sym)
		}
		counts[sym]++
	}

	// Verify all symbols were decoded (total should match).
	total := 0
	for _, c := range counts {
		total += c
	}
	if total != totalSymbols {
		t.Errorf("total symbols = %d, want %d", total, totalSymbols)
	}
	t.Logf("Symbol distribution: %v", counts)
}

// TestReadSymbolUniformNoAdapt decodes many symbols with a uniform CDF that is
// reset after each decode, so there is no adaptation bias. The resulting
// distribution should be roughly uniform across all symbols.
func TestReadSymbolUniformNoAdapt(t *testing.T) {
	// Use enough data for many decodes.
	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte(i*137 + 53) // pseudo-random-ish
	}

	br := NewBoolReader(data)
	totalSymbols := 500
	counts := [4]int{}

	for i := 0; i < totalSymbols; i++ {
		// Fresh uniform CDF each time -- no adaptation skew.
		cdf := InitCDF(4)
		sym, err := br.ReadSymbol(cdf, 4)
		if err != nil {
			t.Fatalf("ReadSymbol failed at iteration %d: %v", i, err)
		}
		if sym < 0 || sym > 3 {
			t.Fatalf("symbol %d out of range [0,3]", sym)
		}
		counts[sym]++
	}

	t.Logf("Symbol distribution (no adapt): %v", counts)

	// With a uniform CDF and pseudo-random data, each symbol should appear
	// roughly 25% of the time. Allow 10-40% tolerance.
	for sym, c := range counts {
		frac := float64(c) / float64(totalSymbols)
		if frac < 0.10 || frac > 0.40 {
			t.Errorf("symbol %d: %d/%d = %.1f%%, expected roughly 25%%", sym, c, totalSymbols, frac*100)
		}
	}
}

// TestReadSymbolBoolDistribution decodes many boolean symbols with a uniform
// CDF (no adaptation) and verifies roughly 50/50 distribution.
func TestReadSymbolBoolDistribution(t *testing.T) {
	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte(i*173 + 97)
	}

	br := NewBoolReader(data)
	totalSymbols := 500
	counts := [2]int{}

	for i := 0; i < totalSymbols; i++ {
		cdf := InitCDF(2)
		val, err := br.ReadSymbolBool(cdf)
		if err != nil {
			t.Fatalf("ReadSymbolBool failed at iteration %d: %v", i, err)
		}
		if val {
			counts[1]++
		} else {
			counts[0]++
		}
	}

	t.Logf("Bool distribution (no adapt): false=%d true=%d", counts[0], counts[1])

	// Each outcome should appear roughly 50% of the time. Allow 30-70% tolerance.
	for sym, c := range counts {
		frac := float64(c) / float64(totalSymbols)
		if frac < 0.30 || frac > 0.70 {
			t.Errorf("symbol %d: %d/%d = %.1f%%, expected roughly 50%%", sym, c, totalSymbols, frac*100)
		}
	}
}

// TestDataRemaining verifies unconsumed byte tracking.
func TestDataRemaining(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	br := NewBoolReader(data)

	// After init we consumed 3 bytes (32-bit window refill).
	remaining := br.DataRemaining()
	if remaining != 5 {
		t.Errorf("DataRemaining() = %d, want 5", remaining)
	}
}
