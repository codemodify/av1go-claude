---
name: BeachDrone warp rounding bug investigation
description: BeachDrone 18/345 PERFECT; 1-pixel cascade from OH=32 warp MC at miR=46 miC=18; NOT a CDF/coefficient bug
type: project
---

BeachDrone-AV1 has 18/345 perfect frames (0-17, where 0-16 are first GOP). Error starts at display frame 17.

**Root cause chain:** OH=32 (warp MC, miR=46 miC=18, 8x8 block) → OH=24 (weighted avg) → OH=20 (diffwtd SEG) → OH=18 (V-only prep filter reads 8 taps from OH=20) → OH=17 (compound avg of OH=16 [PERFECT] + OH=18 [off by 1]).

The specific error: warpAffine8x8 at OH=32 predicts 73 at block position (7,6); dav1d likely produces 74. With residual -13, our pixel=60, dav1d=61. This cascades through multiple compound prediction stages.

**Eliminated causes:**
- CDF counter positions: all verified correct for every CDF type
- H-only MC rounding (+34): confirmed correct, reverting to +32 breaks many frames
- V-only prep rounding (+2 >> 2): matches dav1d
- Compound avg formula ((prep0+prep1+16)>>5): matches dav1d
- SEG/diffwtd mask shift (>>8): confirmed correct, spec-derived from pixel-precision >>4
- getMultShiftDiag/Ndiag: clamp ranges encode identity+deviation correctly
- Warp filter table: all 193 entries match dav1d
- Warp my=delta*4 formula: matches dav1d's source code

**Still to investigate:** The warpAffine8x8 intermediate values for the specific warp params (alpha=2176, beta=2176, gamma=-512, delta=-512). May need bit-exact comparison of the 15x8 intermediate buffer.

BigBuckBunny: 563/839 PERFECT with separate catastrophic desync at frame 127 (entropy bug).
BeachYoga: 93/93 ALL PERFECT. spbtv: 375/375 ALL PERFECT.
