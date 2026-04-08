---
name: BBB/BeachDrone MV decode bug at frame boundaries
description: MV differs by (1,1) for NEARMV blocks near right frame edge, causing CDF drift in BBB F127 and BeachDrone F49
type: project
---

Root cause identified 2026-04-02:

BBB F127 and BeachDrone F49 CDF drift traced to MV decoding bug in specific inter blocks near frame right edge. The original hypothesis (filter ModeInfo storage) was incorrect -- dav1d stores DAV1D_FILTER_8TAP_REGULAR (0) not N_SWITCHABLE_FILTERS (3) for !hasSubpelFilter.

**Exact divergence point:** BBB OH=0 inter frame (d=135 in Go, d=270 in dav1d), block at MI (14,210) 8x8 NEARMV:
- dav1d: MV=(24,-30), rng=62304
- Go: MV=(25,-29), rng=60640
- All earlier syntax (skipMode, skip, isInter, ref0=3, ref1=-1, mode=1, comp=false) matches exactly

The MV differs by (1,1) -- likely a sub-pixel rounding or MV stack selection issue. mode=1 is NEARMV which uses the second candidate from the reference MV list. The block is at miCol=210 in a frame with MiCols=214.

**Debug approach:** The difference is NOT in CDF state or filter storage but in the MV stack (findMVStack/refMVList). Need to compare the MV candidate list for this block between decoders.

**Why:** The MV error causes different MSAC bits consumed, leading to cumulative CDF adaptation drift over the remaining ~100 blocks in the SB, which cascades into subsequent frames.

**How to apply:** Investigate findMVStack at MI (14,210) for ref0=3, compare near MV candidates with dav1d.
