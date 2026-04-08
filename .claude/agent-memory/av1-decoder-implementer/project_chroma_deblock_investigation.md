---
name: Chroma deblock level cache fix
description: Fixed chroma deblock filter level lookup for 4:2:0 to match dav1d's last-writer-wins level cache; 20/22 videos ALL PERFECT
type: project
---

## Status (2026-04-02)
Fixed the primary chroma deblock issue. Results:
- **20/22 videos ALL PERFECT** (up from 18/22)
- Sintel_720_2MB and Sintel_720_5MB: FIXED (all 10 frames PERFECT)
- Sintel_1080_2MB: Pre-existing inter-frame cascade at F5+ (Y diffs max=120+, NOT a deblock issue)
- Sintel_1080_5MB: Reduced from ~26 diffs to ~6 diffs per frame (F7-F9 only, chroma-only, max=1)

## Root cause and fix
In 4:2:0, dav1d precomputes a level cache at chroma MI positions `(by>>1, bx>>1)`. When multiple sub-8x8 luma blocks map to the same chroma cell, the last block in raster order (bottom-right MI of the 2x2 group) wins. Our code was reading blockInfo from the even MI position (top-left), producing wrong filter levels.

**Fix in `decoder/deblock.go`**: For chroma filter levels only (not edge detection), adjust the MI lookup to `(miR+1, miC+1)` when subY/subX=1 and within bounds. Edge detection continues using the original cur/prev positions, matching dav1d's mask_edges_chroma which operates per-block.

## Remaining issues
- Sintel_1080_2MB F5+: Large cascading inter-frame diffs, unrelated to deblock
- Sintel_1080_5MB F7-F9: 4 chroma diffs per plane per frame (magnitude 1), possibly CDEF or residual deblock edge detection difference

**Why:** Videos with LoopFilterLevel[2,3] > 0 (2MB, 5MB bitrates) exercise chroma deblocking. Other bitrates have LF[2]=LF[3]=0 so chroma deblock is skipped entirely.

**How to apply:** The fix is in `deblockEdges()` in decoder/deblock.go, in the chroma filtering section. It separates edge detection (uses original luma MI positions) from level computation (uses adjusted bottom-right MI positions for sub-8x8 chroma cells).
