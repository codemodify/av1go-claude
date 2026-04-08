---
name: BeachDrone real bug location
description: BeachDrone error is NOT warp - it is inverse transform rounding in OH=24 frame, block miRow=48 miCol=18
type: project
---

BeachDrone 1-pixel error at (79,190) starting frame 17: root cause is NOT in warp.

Verified: warp function output matches dav1d exactly (all 64 pixels of 8x8 block at miRow=46,miCol=18 in OH=32 match).

Real bug: inverse transform or coefficient decode in non-displayed frame OH=24, regular MC block at miRow=48 miCol=18 (8x8, motionMode=0, MV=(0,10), ref=GOLDEN). 
- Our residual at pixel (76,192) position (4,0) in block = 1, should be 0
- Prediction=108 is correct (matches H-only filter computation)
- val=109 (ours) vs val=108 (dav1d) -> 1-pixel error propagates through reference chain

**Why:** The user originally attributed this to warp rounding. Extensive verification proves warp is correct. The error originates in a DIFFERENT block in a DIFFERENT frame (OH=24, not OH=32).

**How to apply:** Focus investigation on InverseTransform2D or decodeTxBlock for 8x8 inter blocks in OH=24. Check intermediate rounding in apply1DTransform, particularly the column transform or final >>4 shift.
