---
name: Deblock MI row clipping + sub-8x8 chroma neighbor fix
description: Two fixes achieving ALL PERFECT on all 9 test videos: deblock/CDEF MI rows clipped to frame height, sub-8x8 chroma uses miGrid for above-row neighbors
type: project
---

Two bugs fixed on 2026-04-02 achieving pixel-perfect output on all 9 test videos (10 frames each):

**Bug 1: Deblock/CDEF processing phantom MI row at non-8-aligned frame heights**
- AV1 spec MiRows = `2 * ceil(height/8)`, but dav1d uses `f->bh = (height+3)/4`
- When height is not a multiple of 8, our MiRows is 1 larger (e.g., 138 vs 137 for height=545)
- This caused deblocking to filter edges at MI rows beyond the actual frame, modifying buffer padding rows that CDEF direction finding later reads
- Fix: clip MI row range to `(frameHeight + 3) / 4` in both ApplyDeblocking/ApplyDeblockingSBRow and all CDEF functions
- Affected: Sintel_720 (1280x545), Sintel_1080 (1920x818) — any video with height % 8 != 0

**Bug 2: Sub-8x8 chroma MC reading stale aboveModeInfo for above-row neighbors**
- In sub-8x8 chroma composition, neighbor MVs come from above-left, above, and left blocks
- `aboveModeInfo[miCol-1]` is overwritten by same-row blocks in raster order (block at miCol-1 on same row runs before block at miCol)
- Resulted in reading current-row MV instead of above-row MV for above-left and above neighbors
- dav1d uses `r[-1][bx-1]` which always reads the above row's refmvs data
- Fix: use `td.miGrid.Get(miRow-1, col)` for above-row neighbor lookups in sub-8x8 chroma
- Affected: all videos with sub-8x8 inter blocks — CityHall_1280x720, BeachDrone, Sintel_720

**Why:** Both caused chroma-only diffs that cascaded through CDF adaptation into larger errors at later frames.

**How to apply:** When adding new code that reads neighbor ModeInfo for above-row blocks, always use miGrid instead of aboveModeInfo to avoid stale context data.
