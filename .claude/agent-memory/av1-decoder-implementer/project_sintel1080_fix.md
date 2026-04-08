---
name: Sintel 1080 keyframe boundary reconstruction fix
description: Two-phase fix for Sintel 1080 bottom-edge diffs — early return desync + skipReconstruction reference pixel starvation
type: project
---

Two bugs fixed for Sintel 1080 (1920x818, 128x128 SBs, MiRows=206, FrameHeight=818):

**Phase 1:** Blocks at miRow=205 (where `miRow*4=820 > FrameHeight=818`) returned early,
desynchronizing the MSAC entropy decoder. Fix: parse all bitstream symbols but gate
pixel writes with a `skipReconstruction` flag. Dropped diffs from 51K to 4.7K.

**Phase 2 (this fix):** Removed `skipReconstruction` entirely. Blocks within the MI grid
(miRow < MiRows) must always be fully reconstructed — even when extending past FrameHeight —
because neighboring blocks read their pixels as intra prediction references (bottom-left
extensions, left column for DC/directional modes). The `reconstructPlane` curH clamp
against the buffer height already prevents out-of-bounds writes. Without reconstruction,
padding-area pixels stay zero, poisoning directional predictions (e.g., D203_PRED) that
cascade errors diagonally across the frame bottom.

**Why:** MiRows=206 but FrameHeight=818, so `MiRows*4=824 > FrameHeight`. Blocks at
miRow=205 cover pixels 820-823 (beyond 818) but are within the MI grid. dav1d reconstructs
them fully into the frame buffer padding. Our `skipReconstruction` flag prevented this,
leaving zero-filled pixels that corrupted bottom-left reference extensions for blocks
further right and below, producing cascading DC offset errors of up to -92.

**Result:** Sintel 1080 F0 now Y=PERFECT, U=PERFECT, V=3 off-by-1 diffs (pre-existing
chroma rounding). All regressions pass: spbtv PERFECT, Sintel 360 3/3 PERFECT.
