---
name: Decoding bugs status 2026-04-03
description: Partition CDF counter reset bug fixed, Sintel 360 F0-F2 ALL PERFECT, spbtv ALL PERFECT
type: project
---

## Partition CDF counter reset fix (2026-04-03, applied)

Fixed 73K Y diffs in Sintel 360 F2 by correcting `ResetAllCounts()` for partition CDFs.

**Root cause**: Partition CDFs use variable `nsyms` depending on block level (BL_128X128=8, BL_64X64..BL_16X16=10, BL_8X8=4), but all share a fixed 11-entry array. `UpdateCDF(cdf, nsyms, val)` correctly stores the adaptation counter at `cdf[nsyms]` (cdf[4] for BL_8X8), but `ResetAllCounts()` used `resetMulti()` which resets `cdf[len-1]` = cdf[10]. For BL_8X8 partitions, the real counter at cdf[4] was NEVER reset, causing stale adaptation counts to carry over between frames.

**Impact**: When frame N's saved CDFs (via `context_update_tile_id`) were used by frame N+1, BL_8X8 and BL_128X128 partition CDFs had incorrect adaptation rates, causing symbol misparse and cascade.

**Fix**: In `cdf.go ResetAllCounts()`, reset partition CDF counters at the correct positions per block level: cdf[8] for BL_128X128, cdf[10] for BL_64X64..BL_16X16, cdf[4] for BL_8X8.

**Why:** The generalization principle: any CDF whose nsyms differs from (array_length - 1) will have its counter at the wrong position for `resetMulti`. Currently only partition CDFs have this issue because they share a uniform 11-entry array across all block levels.

**How to apply:** When adding new variable-nsyms CDFs, ensure `ResetAllCounts` resets the counter at `cdf[nsyms]`, not `cdf[len-1]`.

## Chroma deblock TX size fix (2026-04-02, applied)

Fixed 103 V-plane diffs in Sintel 360 F1 by using the block-level chroma TX size (`chromaTxSz`) in DeblockInfo instead of deriving it from per-leaf luma TX sizes.

## Chroma I420 intra edge flags fix (2026-04-02, applied)

Fixed 6791 U diffs (Sintel F0) to 0 by implementing per-format intra edge flags at BL_8X8 partition.

## CDEF boundary fixes (2026-04-02, applied)

1. Added haveTop/haveBottom padding in cdefFilterBlock()
2. Fixed chroma row coverage truncation in cdefSrc copy
3. Added extended buffer row copy for cdefSrc

## Status summary (2026-04-03)

- **Sintel 360 F0-F2: ALL PERFECT**
- **spbtv F0-F4: ALL PERFECT**
- **CityHall F0: PERFECT** (F1+ has separate issues)
- **Sintel 720 F0: 4 Y diffs max=1** (near-perfect, edge rounding)
- **Sintel 1080: pre-existing diffs** (likely loop restoration or similar)
