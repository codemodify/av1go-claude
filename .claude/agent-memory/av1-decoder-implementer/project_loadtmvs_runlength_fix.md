---
name: LoadTMVs run-length fix
description: Fixed temporal MV projection run-length optimization in LoadTMVs, BeachDrone 49/55 PERFECT
type: project
---

LoadTMVs inner loop was missing dav1d's run-length optimization: when consecutive source TMV blocks at 8x8 resolution have the same ref and MV, dav1d increments both source position (x) and target position (posX) together in a for(;;) loop, writing the same projected MV to consecutive target positions. Our code recomputed posX independently for each source position, causing blocks with identical MVs to write to the SAME target position.

**Why:** Large blocks (16x16+) in the source frame produce multiple consecutive 8x8 TMV entries with identical ref/MV. Without the run-length grouping, these all project to the same target posX (since offset is identical), effectively collapsing a run of N entries into a single write. With the run-length grouping, each source position increments posX by 1, filling N consecutive target positions.

**How to apply:**
- `/home/user/Projects/av1/av1go/decoder/refmvs.go` LoadTMVs inner loop now matches dav1d exactly
- BeachDrone improved from ~18/55 to 49/55 PERFECT (frames 0-48 all PERFECT)
- Remaining BeachDrone F49 issue is a CDF adaptation drift (separate bug)
- No regressions on any other test video (spbtv, bbb, s1-s10, Sintel, BeachYoga all PERFECT)
