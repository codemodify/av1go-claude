---
name: IBC chroma bilinear interpolation fix
description: IntraBC chroma prediction must use bilinear interp for sub-pixel MVs in 4:2:0, not simple copy
type: project
---

IntraBC chroma prediction required bilinear interpolation, not simple pixel copy.

**Root cause:** IBC luma MVs are integer-pel (multiples of 8 in 1/8-pel units), but when downsampled to chroma for 4:2:0, odd pixel offsets become half-pixel offsets (mx=8 or my=8 in 1/16-pel). The original `ibcCopyBlock` did integer-pel copy only, missing the sub-pixel interpolation that dav1d's `mc[FILTER_2D_BILINEAR]` performs.

**Fix:** Added `ibcBilinearBlock` function implementing separable bilinear filter (H then V, matching dav1d's two-pass approach). Fractional components derived as: `mx = mvx & (15 >> !ss_hor)`, scaled by `mx <<= !ss_hor`.

**Why:** dav1d's `mc()` always applies the bilinear filter for IntraBC chroma, even though luma is integer-pel. The chroma MV derivation (`mvx >> (3+ss_hor)`) produces integer positions, but the fractional remainder (`mvx & 15`) can be non-zero in 4:2:0/4:2:2.

**How to apply:** Any future IBC or MC code changes must account for chroma sub-pixel offsets. The bilinear path is in `decodeIntraBCBlock` in block.go.

**Result:** Sintel_360_10MB 110/110 ALL PERFECT (was 9 chroma diffs on F95). No regressions on spbtv, CityHall, BeachYoga, s1, s5, s10, bbb.

**Note on remaining diffs:** BigBuckBunny, BeachDrone, Sintel_720_20MB have NO IntraBC frames at all — they are all codedLossless=true inter frames. Their diffs are pre-existing lossless inter bugs, not IBC issues. Sintel_360_2MB F95 (IBC, QP=2) has 5750 remaining diffs from intra prediction blocks on the IBC keyframe, not from IBC blocks themselves.
