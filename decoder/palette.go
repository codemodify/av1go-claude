// Package decoder implements AV1 bitstream decoding.
//
// This file implements palette mode bitstream parsing:
// palette size, palette colors, and palette indices (color map).
// Palette colors and indices are stored for pixel reconstruction.
//
// AV1 spec Sections 5.11.42-5.11.44 and dav1d recon_tmpl.c/decode.c.
package decoder

import "fmt"

// buildPaletteCache builds the palette neighbor cache by merge-sorting
// the sorted left and above neighbor palettes, deduplicating.
// Returns the cache entries and the cache size.
//
// Matches dav1d_read_pal_plane cache construction (recon_tmpl.c lines 2213-2251).
func buildPaletteCache(leftColors [8]uint8, leftSz int, aboveColors [8]uint8, aboveSz int) ([]uint8, int) {
	var cache [16]uint8
	nCache := 0

	li := 0
	ai := 0
	for li < leftSz && ai < aboveSz {
		lv := leftColors[li]
		av := aboveColors[ai]
		if lv < av {
			if nCache == 0 || cache[nCache-1] != lv {
				cache[nCache] = lv
				nCache++
			}
			li++
		} else {
			if av == lv {
				li++
			}
			if nCache == 0 || cache[nCache-1] != av {
				cache[nCache] = av
				nCache++
			}
			ai++
		}
	}
	for li < leftSz {
		lv := leftColors[li]
		if nCache == 0 || cache[nCache-1] != lv {
			cache[nCache] = lv
			nCache++
		}
		li++
	}
	for ai < aboveSz {
		av := aboveColors[ai]
		if nCache == 0 || cache[nCache-1] != av {
			cache[nCache] = av
			nCache++
		}
		ai++
	}
	return cache[:nCache], nCache
}

// readPalettePlane reads a palette for one plane from the bitstream.
// pl: 0 for Y, 1 for U. Returns the palette colors (length = palette size, 2-8).
//
// Matches dav1d_read_pal_plane (recon_tmpl.c lines 2172-2276).
func (td *TileDecoder) readPalettePlane(bc *BoolReader, pl int, szCtx int, miRow, miCol, bW, bH int) ([]uint8, error) {
	// 1. Palette size: 7-symbol CDF, result + 2.
	szSym, err := bc.ReadSymbol(td.cdf.PaletteSz[pl][szCtx], 7)
	if err != nil {
		return nil, fmt.Errorf("pal_sz plane %d: %w", pl, err)
	}
	palSz := szSym + 2

	pal := make([]uint8, palSz)

	// 2. Build palette neighbor cache from left and above.
	// dav1d: l_cache = pl ? t->pal_sz_uv[1][by4] : t->l.pal_sz[by4]
	//         a_cache = by4 & 15 ? (pl ? t->pal_sz_uv[0][bx4] : t->a->pal_sz[bx4]) : 0
	// Note: above cache is zeroed at SB64 (16 MI) boundaries.
	var leftSz, aboveSz int
	var leftColors, aboveColors [8]uint8

	// Left neighbor palette.
	if pl == 0 {
		if miRow < len(td.leftPalSz) {
			leftSz = int(td.leftPalSz[miRow])
		}
	} else {
		// UV palette uses separate size tracking (dav1d: pal_sz_uv[1][by4]).
		if miRow < len(td.leftPalSzUV) {
			leftSz = int(td.leftPalSzUV[miRow])
		}
	}
	if leftSz > 0 && pl < len(td.leftPalColors) && miRow < len(td.leftPalColors[pl]) {
		leftColors = td.leftPalColors[pl][miRow]
	} else {
		leftSz = 0
	}

	// Above neighbor palette — zeroed at SB64 boundary.
	// dav1d: by4 & 15 means relative position within the SB is nonzero.
	// In our code, miRow is absolute. SB size in MI is 16 (64px) or 32 (128px).
	// dav1d uses SB64 boundary always for palette cache: by4 & 15.
	// Since by4 is the position within the SB row, and SB rows are 16 MI tall (64px),
	// the check by4 & 15 is true when miRow is not at the start of a 16-MI boundary.
	atSB64Boundary := (miRow % 16) == 0
	if !atSB64Boundary {
		if pl == 0 {
			if miCol < len(td.abovePalSz) {
				aboveSz = int(td.abovePalSz[miCol])
			}
		} else {
			// UV palette uses separate size tracking (dav1d: pal_sz_uv[0][bx4]).
			if miCol < len(td.abovePalSzUV) {
				aboveSz = int(td.abovePalSzUV[miCol])
			}
		}
		if aboveSz > 0 && pl < len(td.abovePalColors) && miCol < len(td.abovePalColors[pl]) {
			aboveColors = td.abovePalColors[pl][miCol]
		} else {
			aboveSz = 0
		}
	}

	cache, nCache := buildPaletteCache(leftColors, leftSz, aboveColors, aboveSz)

	// Read equi-prob bools for each cache entry.
	i := 0
	for n := 0; n < nCache && i < palSz; n++ {
		used, err := bc.readBoolEqui()
		if err != nil {
			return nil, fmt.Errorf("pal cache[%d]: %w", n, err)
		}
		if used {
			pal[i] = cache[n]
			i++
		}
	}
	nUsedCache := i

	// 3. New palette entries: literal bits.
	if i < palSz {
		bpc := 8 // 8-bit content
		maxVal := (1 << bpc) - 1

		// First new entry: bpc literal bits.
		val0, err := bc.ReadLiteral(bpc)
		if err != nil {
			return nil, fmt.Errorf("pal new[0]: %w", err)
		}
		prev := int(val0)
		if prev > maxVal {
			prev = maxVal
		}
		pal[i] = uint8(prev)
		i++

		if i < palSz {
			// Delta encoding bits: 2 literal bits.
			deltaBitsSym, err := bc.ReadLiteral(2)
			if err != nil {
				return nil, fmt.Errorf("pal delta_bits: %w", err)
			}
			bits := bpc - 3 + int(deltaBitsSym)

			offset := 0
			if pl == 0 {
				offset = 1
			}

			for i < palSz {
				delta, err := bc.ReadLiteral(bits)
				if err != nil {
					return nil, fmt.Errorf("pal delta[%d]: %w", i-nUsedCache, err)
				}
				prev = prev + int(delta) + offset
				if prev > maxVal {
					prev = maxVal
				}
				pal[i] = uint8(prev)
				i++
				if prev+offset >= maxVal {
					// Fill remaining with maxVal.
					for i < palSz {
						pal[i] = uint8(maxVal)
						i++
					}
					break
				}
				remaining := maxVal - prev - offset
				if remaining > 0 {
					newBits := 1 + floorLog2(remaining)
					if newBits < bits {
						bits = newBits
					}
				} else {
					for i < palSz {
						pal[i] = uint8(maxVal)
						i++
					}
					break
				}
			}
		}
	}

	// Merge cached and new entries: sort the final palette.
	// dav1d merges them in sorted order in-place.
	// Since cached entries are already sorted and new entries are delta-coded (ascending),
	// we need to merge the two sorted sequences.
	if nUsedCache > 0 && nUsedCache < palSz {
		// Merge-sort in place: cached entries are pal[0:nUsedCache] (sorted),
		// new entries are pal[nUsedCache:palSz] (sorted ascending).
		merged := make([]uint8, palSz)
		ci, ni, mi := 0, nUsedCache, 0
		for ci < nUsedCache && ni < palSz {
			if pal[ci] <= pal[ni] {
				merged[mi] = pal[ci]
				ci++
			} else {
				merged[mi] = pal[ni]
				ni++
			}
			mi++
		}
		for ci < nUsedCache {
			merged[mi] = pal[ci]
			ci++
			mi++
		}
		for ni < palSz {
			merged[mi] = pal[ni]
			ni++
			mi++
		}
		copy(pal, merged)
	}

	return pal, nil
}

// readPaletteUV reads U and V palette entries.
// Returns U palette, V palette, and the palette size.
// Matches dav1d dav1d_read_pal_uv (recon_tmpl.c lines 2278-2310).
func (td *TileDecoder) readPaletteUV(bc *BoolReader, szCtx int, palSzY int, miRow, miCol, bW, bH int) ([]uint8, []uint8, error) {
	// U palette: same as readPalettePlane with pl=1.
	palU, err := td.readPalettePlane(bc, 1, szCtx, miRow, miCol, bW, bH)
	if err != nil {
		return nil, nil, fmt.Errorf("pal_uv U: %w", err)
	}
	palSzU := len(palU)

	bpc := 8
	maxVal := (1 << bpc) - 1
	palV := make([]uint8, palSzU)

	// V palette: delta-coded or direct.
	isDelta, err := bc.readBoolEqui()
	if err != nil {
		return nil, nil, fmt.Errorf("pal_uv V delta flag: %w", err)
	}

	if isDelta {
		// Delta-coded V palette.
		deltaBitsSym, err := bc.ReadLiteral(2)
		if err != nil {
			return nil, nil, fmt.Errorf("pal_uv V delta_bits: %w", err)
		}
		bits := bpc - 4 + int(deltaBitsSym)

		// First value.
		v0, err := bc.ReadLiteral(bpc)
		if err != nil {
			return nil, nil, fmt.Errorf("pal_uv V val[0]: %w", err)
		}
		prev := int(v0)
		palV[0] = uint8(prev)

		for i := 1; i < palSzU; i++ {
			delta, err := bc.ReadLiteral(bits)
			if err != nil {
				return nil, nil, fmt.Errorf("pal_uv V delta[%d]: %w", i, err)
			}
			d := int(delta)
			if d != 0 {
				sign, err := bc.readBoolEqui()
				if err != nil {
					return nil, nil, fmt.Errorf("pal_uv V sign[%d]: %w", i, err)
				}
				if sign {
					d = -d
				}
			}
			prev = (prev + d) & maxVal
			palV[i] = uint8(prev)
		}
	} else {
		// Direct V palette: each entry is bpc literal bits.
		for i := 0; i < palSzU; i++ {
			v, err := bc.ReadLiteral(bpc)
			if err != nil {
				return nil, nil, fmt.Errorf("pal_uv V direct[%d]: %w", i, err)
			}
			palV[i] = uint8(v)
		}
	}

	return palU, palV, nil
}

// readPaletteIndices reads the color map indices for a palette block.
// Returns the index grid (w4*4 x h4*4) as a flat array in raster order.
// Matches dav1d read_pal_indices (decode.c lines 414-443) and
// order_palette (decode.c lines 353-412).
//
// Uses wavefront diagonal order (top-right to bottom-left matching dav1d).
func (td *TileDecoder) readPaletteIndices(bc *BoolReader, pl int, palSz int, w4, h4, outW4, outH4 int) ([]uint8, error) {
	w := w4 * 4   // scan width (clamped to frame)
	h := h4 * 4   // scan height (clamped to frame)
	outW := outW4 * 4 // output grid width (nominal block)
	outH := outH4 * 4 // output grid height (nominal block)
	stride := outW

	if palSz < 2 {
		return make([]uint8, stride*outH), nil
	}

	// Allocate the full output grid (nominal size).
	grid := make([]uint8, stride*outH)

	// First index: uniform in [0, palSz).
	v0, err := bc.DecodeUniform(palSz)
	if err != nil {
		return nil, fmt.Errorf("pal_idx[0]: %w", err)
	}
	grid[0] = uint8(v0)

	// Color map CDF: color_map[pl][palSz-2][ctx], palSz symbols.
	cmapCDF := td.cdf.ColorMap[pl][palSz-2]

	// Wavefront diagonal order, matching dav1d exactly:
	// i = diagonal index (row + col), from 1 to 4*(w4+h4)-2
	// For each diagonal, iterate from top-right (first=high col) to bottom-left (last=low col).
	for i := 1; i < 4*(w4+h4)-1; i++ {
		first := i
		if first > w-1 {
			first = w - 1
		}
		last := i - h + 1
		if last < 0 {
			last = 0
		}

		// order_palette: compute context and order for each position on diagonal.
		haveTop := i > first

		for j, n := first, 0; j >= last; j, n = j-1, n+1 {
			row := i - j
			col := j
			haveLeft := col > 0

			// Compute context (ctx) matching dav1d order_palette.
			var ctx int
			var firstIdx uint8 // the most-likely palette index (order[0])

			if !haveLeft {
				ctx = 0
				firstIdx = grid[(row-1)*stride+col] // top
			} else if !haveTop {
				ctx = 0
				firstIdx = grid[row*stride+col-1] // left
			} else {
				l := grid[row*stride+col-1]          // left
				t := grid[(row-1)*stride+col]         // top
				tl := grid[(row-1)*stride+col-1]      // top-left
				sameAll := t == l && t == tl

				if sameAll {
					ctx = 4
					firstIdx = t
				} else if t == l {
					ctx = 3
					firstIdx = t
				} else if t == tl || l == tl {
					ctx = 2
					firstIdx = tl
				} else {
					ctx = 1
					if t < l {
						firstIdx = t
					} else {
						firstIdx = l
					}
				}
			}

			// Read symbol from context-dependent CDF.
			sym, err := bc.ReadSymbol(cmapCDF[ctx], palSz)
			if err != nil {
				return nil, fmt.Errorf("pal_idx[%d,%d]: %w", row, col, err)
			}

			// Map symbol through order array.
			grid[row*stride+col] = mapPaletteSymbol(sym, palSz, firstIdx, ctx, func() (uint8, uint8, uint8) {
				var l, t, tl uint8
				if haveLeft {
					l = grid[row*stride+col-1]
				}
				if haveTop || (haveLeft && i > first) {
					if row > 0 {
						t = grid[(row-1)*stride+col]
					}
				}
				if haveLeft && row > 0 {
					tl = grid[(row-1)*stride+col-1]
				}
				return l, t, tl
			})

			haveTop = true
		}
	}

	return grid, nil
}

// mapPaletteSymbol maps a decoded color_map symbol to a palette index,
// matching dav1d's order_palette reordering.
//
// The order array is: first the "interesting" neighbors (determined by context),
// then all remaining indices 0..palSz-1 not yet included.
func mapPaletteSymbol(sym int, palSz int, firstIdx uint8, ctx int, getNeighbors func() (uint8, uint8, uint8)) uint8 {
	// Build the order array exactly as dav1d does.
	var order [8]uint8
	var mask uint32
	oIdx := 0

	addVal := func(v uint8) {
		order[oIdx] = v
		oIdx++
		mask |= 1 << v
	}

	l, t, tl := getNeighbors()

	switch ctx {
	case 0:
		addVal(firstIdx)
	case 4:
		addVal(t)
	case 3:
		addVal(t)
		addVal(tl)
	case 2:
		addVal(tl)
		if t == tl {
			addVal(l)
		} else {
			addVal(t)
		}
	case 1:
		if t < l {
			addVal(t)
			addVal(l)
		} else {
			addVal(l)
			addVal(t)
		}
		addVal(tl)
	}

	// Fill remaining indices.
	for bit := uint8(0); bit < 8 && oIdx < 8; bit++ {
		if mask&(1<<bit) == 0 {
			order[oIdx] = bit
			oIdx++
		}
	}

	return order[sym]
}
