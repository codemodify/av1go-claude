package decoder

// interintra.go implements Inter-Intra compound prediction blending.
// When enabled, the MC (inter) prediction is blended with an intra prediction
// using either smooth II masks or wedge masks.
// AV1 spec Section 7.11.3.2; dav1d recon_tmpl.c lines 1637-1660 (luma)
// and 1793-1831 (chroma).

// Inter-Intra prediction modes (InterIntraPredMode in dav1d).
const (
	iiDCPred     = 0
	iiVertPred   = 1
	iiHorPred    = 2
	iiSmoothPred = 3
)

// iiWeights1d contains the 1D inter-intra blend weights.
// From dav1d wedge.c ii_weights_1d[32].
var iiWeights1d = [32]uint8{
	60, 52, 45, 39, 34, 30, 26, 22, 19, 17, 15, 13, 11, 10, 8, 7,
	6, 6, 5, 4, 4, 3, 3, 2, 2, 2, 2, 1, 1, 1, 1, 1,
}

// generateIIMask generates a smooth inter-intra blend mask for the given
// pixel dimensions and II mode. Higher mask values mean more intra weight.
func generateIIMask(w, h, iiMode int) []uint8 {
	mask := make([]uint8, w*h)
	if iiMode == iiDCPred {
		for i := range mask {
			mask[i] = 32
		}
		return mask
	}
	// dav1d build_nondc_ii_masks: step = 32 / max(w, h).
	// Weight index = min(pixel_idx * step, 31).
	// For 8x8: step=4, so y=0→0, y=1→4, y=2→8, etc.
	maxDim := w
	if h > maxDim {
		maxDim = h
	}
	if maxDim < 1 {
		maxDim = 1
	}
	step := 32 / maxDim
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var idx int
			switch iiMode {
			case iiVertPred:
				idx = y
			case iiHorPred:
				idx = x
			case iiSmoothPred:
				idx = x
				if y < x {
					idx = y
				}
			}
			wi := idx * step
			if wi > 31 {
				wi = 31
			}
			mask[y*w+x] = iiWeights1d[wi]
		}
	}
	return mask
}

// --- Wedge mask generation ---
// Ported from dav1d wedge.c.

const (
	wedgeHorizontal = 0
	wedgeVertical   = 1
	wedgeOblique27  = 2
	wedgeOblique63  = 3
	wedgeOblique117 = 4
	wedgeOblique153 = 5
)

type wedgeCodeEntry struct {
	direction int
	xOffset   int
	yOffset   int
}

var wedgeCodebookHgtw = [16]wedgeCodeEntry{
	{wedgeOblique27, 4, 4}, {wedgeOblique63, 4, 4},
	{wedgeOblique117, 4, 4}, {wedgeOblique153, 4, 4},
	{wedgeHorizontal, 4, 2}, {wedgeHorizontal, 4, 4},
	{wedgeHorizontal, 4, 6}, {wedgeVertical, 4, 4},
	{wedgeOblique27, 4, 2}, {wedgeOblique27, 4, 6},
	{wedgeOblique153, 4, 2}, {wedgeOblique153, 4, 6},
	{wedgeOblique63, 2, 4}, {wedgeOblique63, 6, 4},
	{wedgeOblique117, 2, 4}, {wedgeOblique117, 6, 4},
}

var wedgeCodebookHltw = [16]wedgeCodeEntry{
	{wedgeOblique27, 4, 4}, {wedgeOblique63, 4, 4},
	{wedgeOblique117, 4, 4}, {wedgeOblique153, 4, 4},
	{wedgeVertical, 2, 4}, {wedgeVertical, 4, 4},
	{wedgeVertical, 6, 4}, {wedgeHorizontal, 4, 4},
	{wedgeOblique27, 4, 2}, {wedgeOblique27, 4, 6},
	{wedgeOblique153, 4, 2}, {wedgeOblique153, 4, 6},
	{wedgeOblique63, 2, 4}, {wedgeOblique63, 6, 4},
	{wedgeOblique117, 2, 4}, {wedgeOblique117, 6, 4},
}

var wedgeCodebookHeqw = [16]wedgeCodeEntry{
	{wedgeOblique27, 4, 4}, {wedgeOblique63, 4, 4},
	{wedgeOblique117, 4, 4}, {wedgeOblique153, 4, 4},
	{wedgeHorizontal, 4, 2}, {wedgeHorizontal, 4, 6},
	{wedgeVertical, 2, 4}, {wedgeVertical, 6, 4},
	{wedgeOblique27, 4, 2}, {wedgeOblique27, 4, 6},
	{wedgeOblique153, 4, 2}, {wedgeOblique153, 4, 6},
	{wedgeOblique63, 2, 4}, {wedgeOblique63, 6, 4},
	{wedgeOblique117, 2, 4}, {wedgeOblique117, 6, 4},
}

var wedgeMasterBorderOdd = [8]uint8{1, 2, 6, 18, 37, 53, 60, 63}
var wedgeMasterBorderEven = [8]uint8{1, 4, 11, 27, 46, 58, 62, 63}
var wedgeMasterBorderVert = [8]uint8{0, 2, 7, 21, 43, 57, 62, 64}

var wedgeMasters [6][64 * 64]uint8

type wedgeConfig struct {
	w, h     int
	codebook *[16]wedgeCodeEntry
	signs    uint16
}

var wedgeConfigs = []wedgeConfig{
	{32, 32, &wedgeCodebookHeqw, 0x7bfb},
	{32, 16, &wedgeCodebookHltw, 0x7beb},
	{32, 8, &wedgeCodebookHltw, 0x6beb},
	{16, 32, &wedgeCodebookHgtw, 0x7beb},
	{16, 16, &wedgeCodebookHeqw, 0x7bfb},
	{16, 8, &wedgeCodebookHltw, 0x7beb},
	{8, 32, &wedgeCodebookHgtw, 0x7aeb},
	{8, 16, &wedgeCodebookHgtw, 0x7beb},
	{8, 8, &wedgeCodebookHeqw, 0x7bfb},
}

// Pre-computed wedge masks indexed by [w, h, idx].
var wedgeMask444 map[[3]int][]uint8
var wedgeMask420 map[[3]int][]uint8

// Sign-aware 420 chroma masks for compound prediction: [w, h, idx, sign].
var wedgeMask420Sign map[[4]int][]uint8

func init() {
	initWedgeMasters()
	initWedgeMasks()
}

func insertBorder(dst []uint8, src [8]uint8, ctr int) {
	for i := 0; i < 64; i++ {
		j := i - ctr + 4
		if j < 0 {
			dst[i] = 0
		} else if j >= 8 {
			dst[i] = 64
		} else {
			dst[i] = src[j]
		}
	}
}

func initWedgeMasters() {
	// VERTICAL: each row identical, border centered at 32.
	for y := 0; y < 64; y++ {
		insertBorder(wedgeMasters[wedgeVertical][y*64:], wedgeMasterBorderVert, 32)
	}

	// OBLIQUE63: alternating even/odd lines, center from 48 decreasing.
	for y, ctr := 0, 48; y < 64; y, ctr = y+2, ctr-1 {
		insertBorder(wedgeMasters[wedgeOblique63][y*64:], wedgeMasterBorderEven, ctr)
		insertBorder(wedgeMasters[wedgeOblique63][(y+1)*64:], wedgeMasterBorderOdd, ctr-1)
	}

	// OBLIQUE27 = transpose of OBLIQUE63.
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			wedgeMasters[wedgeOblique27][x*64+y] = wedgeMasters[wedgeOblique63][y*64+x]
		}
	}

	// HORIZONTAL = transpose of VERTICAL.
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			wedgeMasters[wedgeHorizontal][x*64+y] = wedgeMasters[wedgeVertical][y*64+x]
		}
	}

	// OBLIQUE117 = hflip of OBLIQUE63.
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			wedgeMasters[wedgeOblique117][y*64+63-x] = wedgeMasters[wedgeOblique63][y*64+x]
		}
	}

	// OBLIQUE153 = hflip of OBLIQUE27.
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			wedgeMasters[wedgeOblique153][y*64+63-x] = wedgeMasters[wedgeOblique27][y*64+x]
		}
	}
}

func initWedgeMasks() {
	wedgeMask444 = make(map[[3]int][]uint8)
	wedgeMask420 = make(map[[3]int][]uint8)
	wedgeMask420Sign = make(map[[4]int][]uint8)

	for _, cfg := range wedgeConfigs {
		for n := 0; n < 16; n++ {
			sign := (cfg.signs >> n) & 1
			cb := cfg.codebook[n]
			xOff := 32 - (cfg.w*cb.xOffset)>>3
			yOff := 32 - (cfg.h*cb.yOffset)>>3

			mask444 := make([]uint8, cfg.w*cfg.h)
			master := wedgeMasters[cb.direction][:]
			for y := 0; y < cfg.h; y++ {
				for x := 0; x < cfg.w; x++ {
					v := master[(yOff+y)*64+xOff+x]
					if sign == 1 {
						v = 64 - v
					}
					mask444[y*cfg.w+x] = v
				}
			}
			wedgeMask444[[3]int{cfg.w, cfg.h, n}] = mask444

			// 420 chroma: average 2x2 luma blocks (sign=0 rounding, for interintra).
			cw := cfg.w >> 1
			ch := cfg.h >> 1
			mask420 := make([]uint8, cw*ch)
			for cy := 0; cy < ch; cy++ {
				for cx := 0; cx < cw; cx++ {
					sum := int(mask444[(cy*2)*cfg.w+cx*2]) +
						int(mask444[(cy*2)*cfg.w+cx*2+1]) +
						int(mask444[(cy*2+1)*cfg.w+cx*2]) +
						int(mask444[(cy*2+1)*cfg.w+cx*2+1]) + 2
					mask420[cy*cw+cx] = uint8(sum >> 2)
				}
			}
			wedgeMask420[[3]int{cfg.w, cfg.h, n}] = mask420

			// Sign-aware 420 chroma masks for compound wedge prediction.
			// Per dav1d: wedge[0][n] = init_chroma(sign*stride, luma, 0, ...),
			//            wedge[1][n] = init_chroma(!sign*stride, luma, 1, ...).
			for s := 0; s < 2; s++ {
				chromaSign := s
				cm := make([]uint8, cw*ch)
				for cy := 0; cy < ch; cy++ {
					for cx := 0; cx < cw; cx++ {
						sum := int(mask444[(cy*2)*cfg.w+cx*2]) +
							int(mask444[(cy*2)*cfg.w+cx*2+1]) +
							int(mask444[(cy*2+1)*cfg.w+cx*2]) +
							int(mask444[(cy*2+1)*cfg.w+cx*2+1]) + 2
						cm[cy*cw+cx] = uint8((sum - chromaSign) >> 2)
					}
				}
				// Map lookup sign to storage sign per dav1d convention.
				lookupSign := s
				if sign == 0 {
					lookupSign = s
				} else {
					lookupSign = 1 - s
				}
				wedgeMask420Sign[[4]int{cfg.w, cfg.h, n, lookupSign}] = cm
			}
		}
	}
}

// iiBlend blends inter prediction (dst) with intra prediction (src) using mask.
// mask[i] ranges 0..64; higher values → more intra.
// Result: (inter*(64-mask) + intra*mask + 32) >> 6
func iiBlend(dst []byte, src []byte, mask []uint8, n int) {
	for i := 0; i < n; i++ {
		m := int(mask[i])
		dst[i] = byte((int(dst[i])*(64-m) + int(src[i])*m + 32) >> 6)
	}
}

// applyInterIntra blends inter-intra prediction into the MC prediction buffers.
// iiType: 1 = smooth interintra, 2 = wedge interintra.
// iiMode: 0-3 (DC, V, H, SMOOTH).
// wedgeIdx: 0-15 (only used when iiType==2).
func (td *TileDecoder) applyInterIntra(
	predY []byte, nomPixW, nomPixH int,
	predU, predV []byte, nomChromaW, nomChromaH int,
	miRow, miCol, bW, bH int,
	iiType, iiMode, wedgeIdx int,
	hasChroma bool, subX, subY int,
) {
	if iiType == 0 {
		return
	}

	// Map II mode to intra prediction mode.
	// II_SMOOTH_PRED → SMOOTH_PRED; others map directly (DC=0, V=1, H=2).
	intraMode := iiMode
	if iiMode == iiSmoothPred {
		intraMode = SMOOTH_PRED
	}

	// --- Luma ---
	pxX := miCol * 4
	pxY := miRow * 4
	above := make([]byte, nomPixW+1)
	left := make([]byte, nomPixH+1)
	td.fillIntraRef(pxX, pxY, nomPixW, nomPixH, above, left, false, false, false)

	hasAbove := miRow > td.tileRowStart
	hasLeft := miCol > td.tileColStart

	intraTmp := make([]byte, nomPixW*nomPixH)
	PredictIntra(intraMode, intraTmp, nomPixW, nomPixW, nomPixH, above, left, hasAbove, hasLeft, 8)

	var lumaMask []uint8
	if iiType == 1 {
		lumaMask = generateIIMask(nomPixW, nomPixH, iiMode)
	} else {
		lumaMask = wedgeMask444[[3]int{nomPixW, nomPixH, wedgeIdx}]
	}
	iiBlend(predY, intraTmp, lumaMask, nomPixW*nomPixH)

	// --- Chroma ---
	if !hasChroma || nomChromaW == 0 || nomChromaH == 0 {
		return
	}

	chromaPxX := pxX >> subX
	chromaPxY := pxY >> subY
	chromaRefW := (td.frame.Width + subX) >> subX
	chromaRefH := (td.frame.Height + subY) >> subY
	hasAboveC := chromaPxY > (td.tileRowStart*4)>>subY
	hasLeftC := chromaPxX > (td.tileColStart*4)>>subX

	var chromaMask []uint8
	if iiType == 1 {
		chromaMask = generateIIMask(nomChromaW, nomChromaH, iiMode)
	} else {
		chromaMask = wedgeMask420[[3]int{nomPixW, nomPixH, wedgeIdx}]
	}

	for pl := 0; pl < 2; pl++ {
		var buf []byte
		var stride int
		var pred []byte
		if pl == 0 {
			buf = td.frame.U
			stride = td.frame.StrideU
			pred = predU
		} else {
			buf = td.frame.V
			stride = td.frame.StrideV
			pred = predV
		}

		aboveC := make([]byte, nomChromaW+1)
		leftC := make([]byte, nomChromaH+1)
		td.fillIntraRefChroma(chromaPxX, chromaPxY, nomChromaW, nomChromaH,
			aboveC, leftC, buf, stride, chromaRefW, chromaRefH, false, false, false)

		intraTmpC := make([]byte, nomChromaW*nomChromaH)
		PredictIntra(intraMode, intraTmpC, nomChromaW, nomChromaW, nomChromaH,
			aboveC, leftC, hasAboveC, hasLeftC, 8)
		iiBlend(pred, intraTmpC, chromaMask, nomChromaW*nomChromaH)
	}
}

// getWedgeMask returns the 444 luma wedge mask for compound prediction.
func getWedgeMask(w, h, wedgeIdx int) []uint8 {
	return wedgeMask444[[3]int{w, h, wedgeIdx}]
}

// getWedgeMaskChroma420 returns the 420 chroma wedge mask for compound prediction.
// maskSign is the per-block mask_sign from the bitstream (0 or 1).
func getWedgeMaskChroma420(w, h, wedgeIdx, maskSign int) []uint8 {
	return wedgeMask420Sign[[4]int{w, h, wedgeIdx, maskSign}]
}
