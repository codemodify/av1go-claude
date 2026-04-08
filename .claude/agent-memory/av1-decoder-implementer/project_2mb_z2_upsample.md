---
name: 2MB z2 upsample investigation
description: 2MB IBC keyframe F95 has 5206 Y diffs; z2 upsampled above reference diverges from dav1d but fixing globally causes regressions
type: project
---

2MB Sintel IBC keyframe F95 has 5206 Y diffs. First diff at px(579,128) = MI(32,144), mode=D135_PRED, angleDelta=-2, angle=129, V_DCT txType, BaseQIdx=2, 8x8 block with upsampled above edge reference.

**Verified correct**: ITXFM output matches C reference, reference pixels match between decoders, edge filter parameters match. The prediction output differs by the exact same pattern as the output diffs.

**Root cause**: The upsampled above reference in z2 (predictDirAboveLeft8) includes TL at topRef[0], shifting all upsampled positions by 2 relative to dav1d. The xpos formula `(baseIncX<<6)` compensates for this shift in most cases, but at the 219->218 pixel value boundary, the shifted interpolation produces 219 instead of 218.

**Critical finding**: Fixing the offset (offs=1 in upsampleEdge + (1<<6) in xpos) makes ALL videos regress massively. The current code is correct for non-IBC frames but wrong for this specific IBC block. This suggests the z2 code is accidentally correct due to compensating errors in the common case.

**Status**: Unresolved. Need deeper analysis of why the z2 offset convention works for regular keyframes but fails for IBC keyframes. 22/22 videos PERFECT except 2MB F95 (5206 Y diffs, 269 U, 275 V).

**How to apply**: Do NOT modify predictDirAboveLeft8 without testing ALL 22 videos. The fix must be isolated to the IBC keyframe or found in a different component.
