---
name: CityHall 1080p pre-existing ITXFM diffs
description: CityHall 1920x1080 lossy encoding has 313K Y diffs (max=48) that are NOT a regression — they predate the GM rewrite and are a separate ITXFM/coefficient cascading bug
type: project
---

CityHall 1920x1080 (lossy, BaseQIndex=28) has 313K Y diffs, 53K U diffs, 360 V diffs per frame (max Y=48).

**Key finding**: This was NEVER PERFECT. The "CityHall F0: PERFECT" in prior memories referred to the 1280x720 LOSSLESS encoding (BaseQIndex=0) which remains PERFECT. The 1080p test file (`test_city_fresh.ivf`) was created on 2026-04-06 and was never previously tested.

**Root cause analysis**:
- Prediction (z2 directional, angle=144) verified IDENTICAL to dav1d
- Neighbor pixels (above row, left col, topLeft) verified IDENTICAL
- Dequantization verified correct
- ITXFM (IDTX 8x16) produces same output as dav1d for same coefficients
- First diff at (952,32) MI(8,238) with IDTX txType → the residual differs by 1
- Diffs cascade from row 32 downward through intra reference usage
- Suspected: CDF adaptation drift causing wrong coefficient levels from bitstream

**Status**: Open bug, NOT a regression from the GM parsing rewrite. All previously-PERFECT videos (spbtv, BeachYoga, CityHall 720p, Sintel variants) remain PERFECT.

**Why:** The 1080p encoding exercises lossy code paths (quantization, non-trivial transforms like IDTX) that the lossless 720p encoding bypasses entirely.

**How to apply:** Do not conflate CityHall 1080p results with the previously-tested 720p results. Fixing this requires comparing CDF state between our decoder and dav1d at the first-diff block.
