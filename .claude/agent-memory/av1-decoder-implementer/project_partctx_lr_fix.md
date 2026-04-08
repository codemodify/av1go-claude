---
name: Partition context + LR allocation fixes
description: T-split partition context table fix and LR unit grid round-half-up allocation; both BBB/BeachDrone still have residual CDF drift
type: project
---

Two fixes applied 2026-04-02:

1. **T-split partition context** (`block.go`, `inter_block.go`, `tile.go`): T-split sub-blocks (PartitionHorzA/B, VertA/B) wrote partition context derived from sub-block bW/bH, but dav1d uses a fixed table `dav1d_al_part_ctx[above/left][bl][partition]` that depends on the PARENT block level and partition type. Added the full table and set overrides via `partCtxAbove/partCtxLeft` fields on TileDecoder. Also fixed `decodeIntraInInterBlock` which had its own inline context update that also needed the override.

2. **LR unit grid allocation** (`decoder.go`): Changed from ceiling `(planeW+unitSize-1)/unitSize` to round-half-up matching lr.go reader and looprest_filter.go filter. Harmless (extra unused entries) but correct.

**Status:** spbtv 50/50, CityHall 30/30 PERFECT. BBB 127/132, BeachDrone 49/55 unchanged — both still have CDF drift from a DIFFERENT context update bug (not partition context). BeachDrone drift starts at MI(40,172) in OH=56 where CompBwdRef CDF diverges.

**Why:** The partition context table fix is spec-correct but doesn't fix the specific CDF drift path in these two videos. The remaining drift likely comes from another context array (possibly tx context, mode context, or neighbor context) that differs subtly from dav1d.

**How to apply:** The remaining CDF drift is extremely subtle — pixels match but CDF adaptation counts differ, accumulating over ~50 frames until a symbol decision flips. Need to find the next context update discrepancy between our decoder and dav1d.
