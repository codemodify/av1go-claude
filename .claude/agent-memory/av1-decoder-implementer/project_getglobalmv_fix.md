---
name: getGlobalMV fix for BeachDrone
description: getGlobalMV had wrong shift (>>3 instead of >>13) for TRANSLATION and missing ROTZOOM/AFFINE block-center projection; fixes BeachDrone frame 17+ desync
type: project
---

The `getGlobalMV` function in `decoder/inter.go` had two bugs:

1. **TRANSLATION mode**: Used `>> 3` instead of `>> 13` to convert GmParams from warpedModelPrecBits (1/(1<<16) pel) to MV precision (1/8 pel). The correct shift is 13 = 16 - 3.

2. **ROTZOOM/AFFINE mode**: Was incorrectly using just the translation component (`GmParams[ref][0] >> 3`). Must instead compute the full affine projection at the block center, matching dav1d's `get_gmv_2d()` in env.h.

**Root cause chain:** BeachDrone OH=24 has ROTZOOM global motion. The wrong getGlobalMV produced tgmv0={15872,16512} instead of {14,14}. This caused the globalmv_ctx to be 1 instead of 0 in findMVStack, which used the wrong CDF bin for the first MSAC symbol, desyncing the entire frame.

**Why:** The temporal MV projection was actually correct. The real bug was in how the global MV was computed for the globalmv_ctx comparison.

**How to apply:** The getGlobalMV function is used for both MV stack context and GLOBALMV mode MC. Both paths now use the correct implementation.

Remaining: 1-pixel warp rounding diffs at a specific area (~y=75,x=190) in BeachDrone frames 17+. This is a separate pre-existing bug in the warp affine filter, not related to getGlobalMV.
