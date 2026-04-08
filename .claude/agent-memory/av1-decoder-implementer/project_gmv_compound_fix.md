---
name: Global motion warp for compound prediction fix
description: Compound GLOBALMV_GLOBALMV blocks must use warp_affine prep (not regular MC prep) when gmv_warp_allowed; also added single-ref GLOBALMV warp path
type: project
---

Compound prediction was missing global motion warp dispatch. In dav1d, the compound MC loop checks `b->inter_mode == GLOBALMV_GLOBALMV && f->gmv_warp_allowed[b->ref[i]]` for each reference independently. When true, it uses `warp_affine` (prep variant outputting int16) instead of regular `prep_8tap`. Our decoder always used `motionCompensationPrep` for compound references, missing the warp case.

**Why:** The prep values from regular sub-pixel MC vs warp_affine differ significantly (10-30 per pixel) because warp uses per-pixel adaptive filter positions based on affine parameters, while regular MC uses a fixed sub-pixel position for the entire block. This caused ~21 pixel diffs (all ±1) in the first compound-GLOBALMV frame, which then propagated through reference chains.

**How to apply:** 
- Added `warpAffine8x8Prep` (V pass shift=7, rnd=64 vs put's shift=11, rnd=1024) and `applyWarpedMotionPrep` in warp.go
- Added `gmvWarpAllowed` helper checking GmType > TRANSLATION, !ForceIntegerMV, and getShearParams validity
- Added single-ref GLOBALMV warp path (was also missing but not triggered in test videos)
- Compound prep now dispatches per-reference to warp or regular prep

Files: `/home/user/Projects/av1/av1go/decoder/warp.go`, `/home/user/Projects/av1/av1go/decoder/inter_block.go`
