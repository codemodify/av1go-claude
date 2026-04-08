---
name: 2MB Root Cause Discovery
description: Sintel 2MB bug is NOT CDEF deferred boundary - it's an MC/reconstruction error starting at SB row 5 of frame 5 in tile (row=1, col=0)
type: project
---

The original hypothesis (CDEF deferred row boundary at SB row 4/5) was WRONG.

**Proof**: Running deblock/CDEF/LR as completely separate full-frame passes produces the EXACT SAME diffs. Running with `--inloopfilters none` in dav1d and `NO_LOOP_ALL` in ours shows the reconstruction itself differs starting at pixel row 640 (MI row 160, SB row 5 start, tile row 1's 2nd SB row).

**Key facts**:
- Frame 5 is first affected. Frames 0-4 are PERFECT at all stages.
- Error is ONLY in tile col 0 (x=0-1023). Tile col 1 (x=1024-1919) is PERFECT.
- Tile layout: 2 cols (MI 0/256), 2 rows (MI 0/128). SBSize=128.
- First reconstruction diff: pixel (207,640) = MI(160,51). Value diffs up to 108.
- SB row 4 (MI 128-159, in tile row 1) is PERFECT.
- SB row 5 (MI 160+) starts with correct blocks (MI cols 0-50) then goes wrong.
- This pattern (correct start, then drift) suggests CDF/parsing drift after a bad symbol decode somewhere around MI(160, ~50).

**Why:** Resolving the real bug (MC/reconstruction) will make the CDEF deferred boundary issue moot since the input data will finally be correct.

**How to apply:** Focus debugging on the inter prediction parsing for tile(row=1,col=0) SB row 5 of frame 5. Look for CDF drift or missing/extra symbol reads.
