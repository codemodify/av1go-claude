package decoder

import (
	"errors"
	"fmt"
	"io"
	"math/bits"
)

// ErrBoolReaderExhausted is returned when the BoolReader runs out of data
// unexpectedly during a read operation.
var ErrBoolReaderExhausted = errors.New("decoder: bool reader exhausted")


// Constants for the AV1 multi-symbol arithmetic decoder.
// These match the values used in dav1d (src/msac.c) and libaom (aom_dsp/entdec.c).
const (
	// ecWinSize is the window size in bits. We use 64-bit windows to match
	// dav1d on 64-bit platforms (EC_WIN_SIZE = sizeof(ec_win) * CHAR_BIT).
	ecWinSize = 64

	// ecProbShift is the number of bits to right-shift CDF values before
	// multiplying by the scaled range.
	ecProbShift = 6

	// ecMinProb is the minimum probability assigned to each symbol to prevent
	// any symbol from having zero probability.
	ecMinProb = 4
)

// BoolReader implements the AV1 multi-symbol arithmetic decoder as used in
// dav1d (src/msac.c) and libaom (aom_dsp/entdec.c).
//
// This is the Daala-derived entropy coder specified in AV1. It uses a
// complement-based "dif" register instead of a raw "value" register.
// The CDF values are in ascending format (matching AOM_CDF macro arguments).
type BoolReader struct {
	data           []byte // source bitstream
	pos            int    // current byte read position in data
	dif            uint64 // difference register (complement of coded value)
	rng            uint32 // current range, always in [0x8000, 0xFFFF] after normalization
	cnt            int    // number of available bits before refill needed
	allowUpdateCDF bool   // when false, skip CDF adaptation (disable_cdf_update)
}

// DebugState returns the MSAC state for debugging.
func (br *BoolReader) DebugState() (dif uint64, rng uint32, pos int, cnt int) {
	return br.dif, br.rng, br.pos, br.cnt
}

// NewBoolReader creates a new arithmetic decoder over the given byte slice.
//
// Initialization matches dav1d's dav1d_msac_init (src/msac.c):
//   - dif = (1 << 31) - 1  (all ones except MSB)
//   - rng = 0x8000          (32768)
//   - cnt = -15             (need to load 15 bits)
//   - Then refill to load initial bytes from the stream.
func NewBoolReader(data []byte) *BoolReader {
	return NewBoolReaderWithCDFUpdate(data, true)
}

// NewBoolReaderWithCDFUpdate creates an arithmetic decoder with explicit CDF update control.
// When allowUpdateCDF is false, CDF adaptation is skipped (matching dav1d's disable_cdf_update).
func NewBoolReaderWithCDFUpdate(data []byte, allowUpdateCDF bool) *BoolReader {
	br := &BoolReader{
		data:           data,
		allowUpdateCDF: allowUpdateCDF,
		dif:            0,
		rng:  0x8000,
		cnt:  -15,
	}
	br.refill()
	return br
}

// refill loads bytes from the stream into the dif register.
// Matches dav1d's ctx_refill (src/msac.c) exactly.
//
// Stream bytes are complemented (XOR 0xFF) and OR'd into the dif register
// at descending bit positions. This is the Daala entropy coder's convention:
// the "dif" register holds the complement of the coded value.
func (br *BoolReader) refill() {
	c := ecWinSize - 24 - br.cnt // bit position for next byte
	for {
		if c < 0 {
			break
		}
		if br.pos >= len(br.data) {
			// End of stream: set remaining low bits to 1 (matches dav1d).
			// Do NOT decrement c here — dav1d breaks without decrementing.
			br.dif |= ^(^uint64(0xFF) << uint(c))
			break
		}
		br.dif |= uint64(br.data[br.pos]^0xFF) << uint(c)
		br.pos++
		c -= 8
	}
	br.cnt = ecWinSize - 24 - c
}

// norm renormalizes the decoder state after decoding a symbol.
// Matches dav1d's ctx_norm (src/msac.c).
//
// Shifts rng and dif left by the number of bits needed to restore
// rng >= 0x8000, then refills if needed.
func (br *BoolReader) norm() {
	// d = number of bits to shift = 15 - floor(log2(rng))
	// Since rng < 0x8000 when called, floor(log2(rng)) ∈ [0, 14], so d ∈ [1, 15].
	d := 15 - (31 - bits.LeadingZeros32(br.rng))
	oldCnt := br.cnt
	br.cnt -= d
	br.dif <<= uint(d)
	br.rng <<= uint(d)
	// dav1d uses unsigned compare: (unsigned)cnt < (unsigned)d
	// This avoids redundant refills when cnt is already negative (end of stream).
	if uint32(oldCnt) < uint32(d) {
		br.refill()
	}
}

// ReadSymbol reads a multi-symbol value using a cumulative distribution function.
//
// Matches dav1d's msac_decode_symbol_adapt_c (src/msac.c).
//
// CDF format (ICDF / descending, matching dav1d):
//   - cdf[0..nsyms-2] hold ICDF values (32768 - cumulative_prob) in descending order
//   - cdf[nsyms-1] is 0 (last symbol, implicitly probability up to 32768)
//   - cdf[nsyms] = adaptation counter
//
// The boundary for each symbol is computed from the CDF value.
// Higher CDF values produce higher boundaries, meaning the symbol
// occupies a smaller portion of the upper range (lower probability).
//
// Algorithm:
//  1. c = top 16 bits of dif (complement-coded value)
//  2. For each symbol val, compute boundary v from cdf[val]
//  3. Find first val where c >= v (complement comparison)
//  4. Update dif, rng, normalize, adapt CDF
func (br *BoolReader) ReadSymbol(cdf []uint16, nsyms int) (int, error) {
	if nsyms < 2 {
		return 0, fmt.Errorf("decoder: ReadSymbol nsyms=%d must be >= 2", nsyms)
	}
	if len(cdf) < nsyms {
		return 0, fmt.Errorf("decoder: CDF length %d too short for nsyms=%d (need %d)", len(cdf), nsyms, nsyms)
	}

	// n = number of CDF boundary entries = nsyms - 1 = dav1d's n_symbols.
	// CDF layout: cdf[0..n-1] = ICDF boundary values, cdf[n] = 0 sentinel,
	// cdf[n+1] = cdf[nsyms] = adaptation counter.
	n := nsyms - 1

	c := br.dif >> (ecWinSize - 16) // top 16 bits of dif
	r := br.rng >> 8                // scaled range [128, 255]

	var u uint32
	v := br.rng
	val := -1

	for {
		val++
		if val >= nsyms {
			// Safety: force last symbol if c is below all boundaries.
			val = n
			v = 0
			break
		}
		u = v
		v = r * (uint32(cdf[val]) >> ecProbShift)
		v >>= 7 - ecProbShift
		v += ecMinProb * uint32(n-val)
		if c >= uint64(v) {
			break
		}
	}

	br.dif -= uint64(v) << (ecWinSize - 16)
	br.rng = u - v

	if br.allowUpdateCDF {
		UpdateCDF(cdf, nsyms, val)
	}

	if br.rng < 0x8000 {
		br.norm()
	}

	return val, nil
}

// ReadSymbolBool is an optimized 2-symbol (boolean) CDF decoder.
//
// Matches dav1d's msac_decode_bool_adapt (src/msac.c) exactly:
//   - Boundary: v = r*(cdf[0]>>6)>>1 + EC_MIN_PROB (not *2)
//   - CDF layout: [cdf_value, counter] — counter at cdf[1], not cdf[2]
//   - Update rate: 4 + (count >> 4) (no +1 for nsyms>2)
//
// Returns true for symbol 1, false for symbol 0.
func (br *BoolReader) ReadSymbolBool(cdf []uint16) (bool, error) {
	if len(cdf) < 2 {
		return false, fmt.Errorf("decoder: ReadSymbolBool requires CDF with >= 2 entries, got %d", len(cdf))
	}

	c := br.dif >> (ecWinSize - 16)
	r := br.rng >> 8

	// Boundary: matches dav1d's + EC_MIN_PROB (not * nsyms).
	v := r * (uint32(cdf[0]) >> ecProbShift)
	v >>= 7 - ecProbShift
	v += ecMinProb

	var symbol int
	if c >= uint64(v) {
		// Symbol 0: c is above the boundary.
		symbol = 0
		br.dif -= uint64(v) << (ecWinSize - 16)
		br.rng -= v
	} else {
		// Symbol 1: c is below the boundary.
		symbol = 1
		br.rng = v
	}

	if br.rng < 0x8000 {
		br.norm()
	}

	// CDF update matching dav1d's bool CDF layout: counter at cdf[1].
	if br.allowUpdateCDF {
		count := cdf[1]
		rate := uint16(4) + (count >> 4)
		if symbol == 1 {
			cdf[0] += (32768 - cdf[0]) >> rate
		} else {
			cdf[0] -= cdf[0] >> rate
		}
		if count < 32 {
			cdf[1] = count + 1
		}
	}

	return symbol == 1, nil
}

// ReadSymbolBoolInt is like ReadSymbolBool but returns 0 or 1 as int.
// Use this for 2-symbol CDF reads that need an integer result.
func (br *BoolReader) ReadSymbolBoolInt(cdf []uint16) (int, error) {
	b, err := br.ReadSymbolBool(cdf)
	if err != nil {
		return 0, err
	}
	if b {
		return 1, nil
	}
	return 0, nil
}

// readBoolEqui decodes a single equiprobable (50/50) bit.
// Matches dav1d's msac_decode_bool_equi (src/msac.c).
//
// In dav1d: ret = (dif >= vw); return !ret;
// So dif >= v means bit=0 (false), dif < v means bit=1 (true).
func (br *BoolReader) readBoolEqui() (bool, error) {
	c := br.dif >> (ecWinSize - 16)
	v := ((br.rng >> 8) << 7) + ecMinProb

	if c >= uint64(v) {
		// dif >= v: subtract boundary from dif, narrow range to upper part.
		// dav1d returns !ret = !1 = 0 here, so bit = false.
		br.dif -= uint64(v) << (ecWinSize - 16)
		br.rng -= v
		if br.rng < 0x8000 {
			br.norm()
		}

		return false, nil
	}

	// dif < v: range becomes the boundary (lower part).
	// dav1d returns !ret = !0 = 1 here, so bit = true.
	br.rng = v
	if br.rng < 0x8000 {
		br.norm()
	}

	return true, nil
}

// ReadLiteral reads n bits as a raw unsigned value, MSB first.
// Each bit is decoded using readBoolEqui (equal probability).
//
// AV1 spec Section 4.10.5 (L(n)): literal bits from the arithmetic coder.
func (br *BoolReader) ReadLiteral(n int) (uint32, error) {
	if n < 0 || n > 32 {
		return 0, fmt.Errorf("decoder: ReadLiteral n=%d out of range [0,32]", n)
	}

	var result uint32
	for i := 0; i < n; i++ {
		bit, err := br.readBoolEqui()
		if err != nil {
			return 0, err
		}
		result = (result << 1)
		if bit {
			result |= 1
		}
	}
	return result, nil
}

// ReadBool reads a single boolean symbol with the given probability.
// prob is a 15-bit probability in [1, 32767].
//
// Matches dav1d's msac_decode_bool (src/msac.c).
// In dav1d: ret = (dif >= vw); return !ret;
// So dif >= v means false (0), dif < v means true (1).
func (br *BoolReader) ReadBool(prob uint16) (bool, error) {
	c := br.dif >> (ecWinSize - 16)
	r := br.rng >> 8

	v := r * (uint32(prob) >> ecProbShift)
	v >>= 7 - ecProbShift
	v += ecMinProb

	if c >= uint64(v) {
		// dif >= v: dav1d returns !1 = 0 (false).
		br.dif -= uint64(v) << (ecWinSize - 16)
		br.rng -= v
		if br.rng < 0x8000 {
			br.norm()
		}

		return false, nil
	}

	// dif < v: dav1d returns !0 = 1 (true).
	br.rng = v
	if br.rng < 0x8000 {
		br.norm()
	}

	return true, nil
}

// ReadNS reads a non-symmetric unsigned integer in the range [0, n).
//
// AV1 spec Section 4.10.7 (ns(n)):
// This encoding uses floor(log2(n-1))+1 bits for some values and one fewer
// bit for others, distributing the "extra" values evenly.
func (br *BoolReader) ReadNS(n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("decoder: ReadNS n must be > 0, got %d", n)
	}
	if n == 1 {
		return 0, nil
	}

	w := floorLog2(n-1) + 1
	m := (1 << w) - n

	v, err := br.ReadLiteral(w - 1)
	if err != nil {
		return 0, fmt.Errorf("decoder: ReadNS: %w", err)
	}

	if int(v) < m {
		return int(v), nil
	}

	extraBit, err := br.ReadLiteral(1)
	if err != nil {
		return 0, fmt.Errorf("decoder: ReadNS: %w", err)
	}

	return int((v << 1)) - m + int(extraBit), nil
}

// DecodeUniform decodes a value uniformly distributed in [0, n).
// Matches dav1d_msac_decode_uniform: reads ceil(log2(n))+1 bits using equiprob bools.
func (br *BoolReader) DecodeUniform(n int) (int, error) {
	if n <= 1 {
		return 0, nil
	}
	l := floorLog2(n) + 1 // matches dav1d's ulog2(n) + 1
	m := (1 << l) - n
	v, err := br.ReadLiteral(l - 1)
	if err != nil {
		return 0, fmt.Errorf("DecodeUniform: %w", err)
	}
	if int(v) < m {
		return int(v), nil
	}
	extra, err := br.readBoolEqui()
	if err != nil {
		return 0, fmt.Errorf("DecodeUniform: %w", err)
	}
	result := int(v)<<1 - m
	if extra {
		result++
	}
	return result, nil
}

// ExitPos returns the current byte position in the underlying data.
func (br *BoolReader) ExitPos() int {
	return br.pos
}

// HasError checks if the decoder is in a valid state.
func (br *BoolReader) HasError() error {
	if br.pos > len(br.data)+1 {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// DataRemaining returns the number of unconsumed bytes in the source data.
func (br *BoolReader) DataRemaining() int {
	remaining := len(br.data) - br.pos
	if remaining < 0 {
		return 0
	}
	return remaining
}

// --- CDF Management ---

// UpdateCDF updates the cumulative distribution function after decoding a symbol.
//
// Matches dav1d's msac_update_cdf (src/msac.c).
//
// Called from ReadSymbol with nsyms = number of symbols (including implicit last).
// CDF layout (our convention with explicit zero sentinel):
//
//	cdf[0..nsyms-2] are ICDF boundary values
//	cdf[nsyms-1]    is 0 (sentinel for last symbol)
//	cdf[nsyms]      is adaptation counter (saturates at 32)
//
// Only cdf[0..nsyms-2] are updated (the nsyms-1 real ICDF values).
// This matches dav1d's n_symbols = nsyms-1 loop bound.
//
// After decoding symbol val:
//   - Entries BEFORE val: push UP toward 32768 (increases boundary,
//     making symbols before val LESS probable)
//   - Entries AT or AFTER val: push DOWN toward 0 (decreases boundary,
//     making the decoded symbol MORE probable)
func UpdateCDF(cdf []uint16, nsyms int, symbol int) {
	count := cdf[nsyms]

	// dav1d: rate = ((count >> 4) | 4) + (n_symbols > 2)
	// n_symbols in dav1d = nsyms - 1 here, so the condition is nsyms - 1 > 2 => nsyms > 3.
	rate := uint16(4) + (count >> 4)
	if nsyms > 3 {
		rate++
	}

	// Update the nsyms-1 real ICDF entries.
	// Matches dav1d's loop: for (i = 0; i < n_symbols; i++) where n_symbols = nsyms-1.
	for i := 0; i < nsyms-1; i++ {
		if i < symbol {
			cdf[i] += (32768 - cdf[i]) >> rate
		} else {
			cdf[i] -= cdf[i] >> rate
		}
	}

	if count < 32 {
		cdf[nsyms] = count + 1
	}
}

// InitCDF creates a uniform CDF in ICDF format for the given number of symbols.
//
// ICDF values are 32768 - ascending_probability. For example, InitCDF(4) produces:
//
//	[24576, 16384, 8192, 0, 0]
//
// which represents equal probability for each of 4 symbols.
func InitCDF(nsyms int) []uint16 {
	cdf := make([]uint16, nsyms+1)
	for i := 0; i < nsyms-1; i++ {
		// Ascending probability: (32768 * (i+1)) / nsyms
		// ICDF = 32768 - ascending = 32768 * (nsyms - 1 - i) / nsyms
		cdf[i] = uint16((32768 * (nsyms - 1 - i)) / nsyms)
	}
	return cdf
}

// DecodeSubexp reads a subexponentially-coded value from the bitstream.
//
// Matches dav1d's dav1d_msac_decode_subexp (src/msac.c).
// Parameters:
//   - ref: the reference/predicted value
//   - n: the range of allowed values
//   - k: the initial number of bits per group
//
// The algorithm reads an exponential-Golomb-like prefix to determine
// the group, then reads k bits within that group, and applies
// inverse recentering around the reference value.
func (br *BoolReader) DecodeSubexp(ref, n int, k uint) (int, error) {
	// assert(n >> k == 8) in dav1d
	var a uint32
	var b2, b3 bool
	b1, err := br.readBoolEqui()
	if err != nil {
		return 0, fmt.Errorf("decoder: DecodeSubexp: %w", err)
	}
	if b1 {
		b2, err = br.readBoolEqui()
		if err != nil {
			return 0, fmt.Errorf("decoder: DecodeSubexp: %w", err)
		}
		if b2 {
			b3, err = br.readBoolEqui()
			if err != nil {
				return 0, fmt.Errorf("decoder: DecodeSubexp: %w", err)
			}
			if b3 {
				k += 2
			} else {
				k += 1
			}
		}
		a = 1 << k
	}
	bits, err := br.ReadLiteral(int(k))
	if err != nil {
		return 0, fmt.Errorf("decoder: DecodeSubexp: %w", err)
	}
	v := bits + a
	if ref*2 <= n {
		result := invRecenter(ref, int(v))
		return result, nil
	}
	result := n - 1 - invRecenter(n-1-ref, int(v))
	return result, nil
}

// invRecenter computes the inverse recentering of a value around a reference.
// Matches dav1d's inv_recenter (include/common/intops.h).
func invRecenter(r, v int) int {
	if v > r*2 {
		return v
	} else if v&1 == 0 {
		return (v >> 1) + r
	}
	return r - ((v + 1) >> 1)
}

// --- Utility ---

// floorLog2 returns floor(log2(x)) for x > 0. Returns 0 for x <= 0.
func floorLog2(x int) int {
	if x <= 0 {
		return 0
	}
	s := 0
	for x > 1 {
		x >>= 1
		s++
	}
	return s
}
