package encoder

// BoolWriter implements the AV1 boolean entropy coder (range encoder).
//
// This is a range coder variant used for entropy coding in AV1. It encodes
// binary and multi-symbol decisions using cumulative distribution functions
// (CDFs). The output is a byte stream consumed by the bool decoder.
//
// AV1 spec Section 8.2 (Arithmetic coding) and 8.3 (CDF updates).
type BoolWriter struct {
	buf   []byte
	low   uint32
	rng   uint32 // range, always in [1, 65535]
	cnt   int    // number of bits buffered in low
}

// NewBoolWriter creates a new boolean entropy coder.
func NewBoolWriter(capacity int) *BoolWriter {
	if capacity <= 0 {
		capacity = 256
	}
	return &BoolWriter{
		buf: make([]byte, 0, capacity),
		rng: 65535, // initial range = 0xFFFF
		cnt: -24,   // initial bit count
	}
}

// WriteBool writes a boolean symbol with given probability.
// prob is the probability of the symbol being 0, expressed as a value
// in [1, 65535] where 65535 means ~100% chance of 0.
//
// This implements the core of the AV1 range coder:
//   split = ((range * prob) >> 16) + 1  (biased to avoid zero)
//   if symbol == 0: range = split
//   if symbol == 1: low += split; range = range - split
func (bw *BoolWriter) WriteBool(bit bool, prob uint16) {
	split := ((bw.rng * uint32(prob)) >> 16) + 1

	if !bit {
		// Symbol 0: upper sub-range.
		bw.rng = split
	} else {
		// Symbol 1: lower sub-range.
		bw.low += split
		bw.rng -= split
	}

	// Renormalize.
	bw.renorm()
}

// WriteLiteral writes n raw bits (uniform probability, bypass mode).
func (bw *BoolWriter) WriteLiteral(val uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		bit := (val >> uint(i)) & 1
		bw.WriteBool(bit == 1, 1<<15) // prob=32768 = 50%
	}
}

// WriteSymbol writes a multi-symbol value using a CDF (cumulative distribution).
// The CDF is an array where cdf[i] represents the cumulative probability
// up to symbol i, in the range [0, 32768].
//
// nsyms is the number of symbols in the alphabet.
// The cdf array must have nsyms entries where cdf[nsyms-1] = 0.
func (bw *BoolWriter) WriteSymbol(symbol int, cdf []uint16, nsyms int) {
	// AV1 spec: the CDF is stored in reverse order, with cdf[nsyms-1] = 0.
	// For encoding: we compute the sub-range for the given symbol.
	//
	// The range is split according to CDF values:
	// For symbol s, the sub-range is [cdf[s], cdf[s-1]) mapped to [0, range).

	cur := bw.rng >> 8 // scale factor

	// Compute bounds.
	var lo, hi uint32
	if symbol > 0 {
		lo = uint32(cdf[symbol-1])
	} else {
		lo = uint32(1<<15) - 1 // cdf[-1] = 32767
	}
	hi = uint32(cdf[symbol])

	// Map to actual range.
	rngLo := cur * (uint32(1<<15) - 1 - lo) >> 15
	rngHi := cur*(uint32(1<<15)-1-hi)>>15 + 1

	bw.low += rngLo
	bw.rng = rngHi - rngLo

	bw.renorm()
}

// WriteSymbolBool writes a binary symbol using a CDF with 2 entries.
// Equivalent to WriteSymbol with nsyms=2 but optimized.
func (bw *BoolWriter) WriteSymbolBool(bit bool, cdf []uint16) {
	prob := cdf[0]
	split := ((bw.rng >> 8) * (uint32(1<<15) - 1 - uint32(prob)) >> 15) + 1

	if !bit {
		bw.rng = split
	} else {
		bw.low += split
		bw.rng -= split
	}

	bw.renorm()
}

// renorm renormalizes the range coder state, outputting bytes as needed.
func (bw *BoolWriter) renorm() {
	// Count leading zeros in range to determine shift.
	shift := 0
	rng := bw.rng
	for rng < 0x8000 {
		rng <<= 1
		shift++
	}
	bw.rng = rng
	bw.low <<= uint(shift)
	bw.cnt += shift

	// Output complete bytes.
	for bw.cnt >= 0 {
		bw.buf = append(bw.buf, byte(bw.low>>24))
		bw.low = (bw.low << 8) & 0xFFFFFFFF
		bw.cnt -= 8
	}
}

// Finalize finalizes the bool coder and returns the encoded bytes.
// Must be called after all symbols have been written.
func (bw *BoolWriter) Finalize() []byte {
	// Flush remaining bits.
	// We need to output enough bytes to uniquely identify the final state.
	for i := 0; i < 4; i++ {
		bw.buf = append(bw.buf, byte(bw.low>>24))
		bw.low <<= 8
	}

	// Trim trailing zeros (but keep at least 1 byte).
	end := len(bw.buf)
	for end > 1 && bw.buf[end-1] == 0 {
		end--
	}

	return bw.buf[:end]
}

// Reset resets the bool coder for reuse.
func (bw *BoolWriter) Reset() {
	bw.buf = bw.buf[:0]
	bw.low = 0
	bw.rng = 65535
	bw.cnt = -24
}

// Len returns the current number of output bytes.
func (bw *BoolWriter) Len() int {
	return len(bw.buf)
}

// --- CDF utilities ---

// InitCDF initializes a uniform CDF for the given number of symbols.
// Returns a CDF array suitable for use with WriteSymbol.
func InitCDF(nsyms int) []uint16 {
	cdf := make([]uint16, nsyms)
	for i := 0; i < nsyms; i++ {
		cdf[i] = uint16(((1 << 15) * (nsyms - 1 - i)) / nsyms)
	}
	return cdf
}
