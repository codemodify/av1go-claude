---
name: 2MB final fix - hasMatchingRef top-right guard
description: find_matching_ref top-right corner check missing max(bW,bH)<32 guard — caused wrong CDF for motion mode on 128x128 blocks
type: project
---

The last bug (Sintel_1080_10s_2MB) was in `hasMatchingRef()` in inter_block.go. The function checked the top-right corner neighbor without the `max(bW, bH) < 32` guard that dav1d uses in `find_matching_ref()`.

**Root cause:** For 128x128 blocks (bW=32, bH=32), dav1d's `find_matching_ref` sets `have_topright = imax(bw4, bh4) < 32` which is false. Our code didn't have this guard, so it checked the top-right corner MI(miRow-1, miCol+bW), found a matching-ref neighbor, and incorrectly set `allowWarp=true`. This caused reading from the 3-symbol `MotionMode` CDF instead of the 2-symbol `OBMC` CDF, desynchronizing the entropy coder for all subsequent blocks.

**Fix:** Added `max(bW, bH) < 32 &&` guard to the top-right corner check in `hasMatchingRef()`.

**Why:** dav1d ref: `find_matching_ref()` in src/decode.c line ~203: `int have_topright = imax(bw4, bh4) < 32 && ...`

**How to apply:** When implementing refmvs/neighbor scanning functions, always cross-reference dav1d's boundary guards. Large blocks (128x128) often have special cases.

**Result:** 22/22 ALL PERFECT.
