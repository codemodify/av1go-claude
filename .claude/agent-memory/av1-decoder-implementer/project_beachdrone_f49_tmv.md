---
name: BeachDrone F49 TMV bug investigation
description: F49 434K diffs traced to temporal MV divergence in OH=56, ref2ref abs fix applied but root cause remains
type: project
---

BeachDrone F49: 434K diffs starting at 4th GOP boundary. 49 frames before are ALL PERFECT.

**Investigation findings (2026-04-02):**
- OH=64 (key-dist) is PIXEL PERFECT vs dav1d
- OH=56 has first Y diff at (704,128) with d=1 — classic temporal MV divergence
- CDF propagation and ResetAllCounts are VERIFIED correct
- Reference frame pixels are VERIFIED correct at frame start
- show_existing_frame keyframe handling uses `d.refFrameType[idx]` correctly (fh.FrameType is always 0 for show_existing_frame since frame_type isn't parsed)

**Fix applied:** ref2ref in LoadTMVs now uses abs(diff2) instead of rejecting negative diffs.
This matches dav1d's `abs(poc_diff) <= 31` check. No regression on spbtv/CityHall.
Did NOT fix F49 bug — root cause is elsewhere in temporal MV projection or MV stack.

**Linter corruption found:** The external linter/IDE tool silently modifies files after edits. It injected a Z2 topLeft smoothing filter into intra.go that broke ALL keyframe decoding (spbtv frame 0 regressed). Restored intra.go from git HEAD to fix.

**Why:** Understanding exactly where MSAC diverges in this lossless ROTZOOM video
**How to apply:** The temporal MV code needs careful comparison with dav1d load_tmvs_c for the specific OH=56 reference configuration
