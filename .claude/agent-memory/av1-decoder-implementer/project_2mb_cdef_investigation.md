---
name: Sintel 1080 2MB CDEF investigation
description: Deep investigation of the last remaining non-perfect video — root cause is CDEF rounding edge case at deferred row boundary
type: project
---

Sintel_1080_10s_2MB is the only non-PERFECT video out of 22. Frames 0-4 are PERFECT. Frame 5 has 113K Y diffs starting at Y[896,632].

**Root cause investigation (2026-04-02):**
- MC prediction: verified bit-exact with dav1d (pred=46 at error pixel)
- Inverse transform 64x64 DCT: verified bit-exact with dav1d (residual=-3)
- Deblocking filter: verified bit-exact with dav1d using standalone C test (43→44)
- CDEF: the error starts exactly at the CDEF deferred row boundary (MI 158-159 = pixel rows 632-639)
- CDEF pri-only filter with adjPriY=4, dir=5 produces sum=6, which gives (6+0+8)>>4=0 (no change). dav1d somehow produces +1 at this pixel.
- The difference is a rounding edge case: sum=6 rounds down to 0, but dav1d appears to get a slightly different sum (>=8) which rounds to +1.

**Why:** The root cause is likely in how CDEF reads the center pixel value. dav1d reads `px = dst[x]` (output frame, post-deblock) while neighbors come from `tmp` (pre-CDEF snapshot). Our code reads both center and neighbors from the pre-CDEF snapshot. In most cases this produces the same result, but at the deferred row boundary there may be a subtle difference.

**How to apply:** This is the only remaining non-perfect video. All other 21 videos are ALL PERFECT across all tested frames. Any future CDEF refactoring should check this specific video.
