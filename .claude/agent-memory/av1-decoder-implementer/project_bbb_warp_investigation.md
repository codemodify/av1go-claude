---
name: BBB frame 143 warp investigation
description: BBB breaks at frame 143 (d=152 OH=16); motion mode CDF was NOT the cause; hasMatchingRef is correct per spec; deriveWarpMV has separate bug
type: project
---

BBB 160-frame test: 143/160 PERFECT, breaks at shown frame 143 = decoded frame d=152 (OH=16).

**Investigation results:**
- First pixel diff: Y(0,664) MI(0,166) in d=152 (no-filter comparison confirms reconstruction error, not loop filter)
- All reference frames verified identical between dav1d and Go through frame 210 (both filtered and no-filter)
- hasMatchingRef for motion mode CDF (2-symbol OBMC vs 3-symbol MotionMode) matches dav1d behavior
- Tried numSamples>1 check per AV1 spec 5.11.14: regressed to 2/160, confirming spec interpretation differs from dav1d
- numSamples>=1 (= hasMatchingRef) gives 143/160, matching original
- deriveWarpMV has a separate pre-existing bug: returns invalid warp at MI(112,152) OH=10 where dav1d succeeds (threshold filtering removes all samples when neighbor MV differs by 512 from block MV, then single-fallback-point fails |sx-dx|<256 check in findAffineInt)

**Root cause still open.** The CDF/MSAC divergence at d=152 must be from a different source. Possible causes:
1. CDF adaptation bug that only manifests with specific reference distances (OH wrapping from 127 to 0)
2. findMVStack bug with large OH distances affecting MV candidate weights or order
3. jnt_comp weight computation bug with distances > 31 (clamped differently)
4. Subtle frame header parsing difference (unlikely since frames 0-142 are PERFECT)

**How to apply:** Next debugging session should try binary-search the MSAC state within d=152 to find the exact block where CDF diverges, possibly by comparing per-SB MSAC state against a dav1d build with tracing.
