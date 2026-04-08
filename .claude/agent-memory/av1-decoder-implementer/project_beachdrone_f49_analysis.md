---
name: BeachDrone F49 CDF divergence analysis
description: Deep analysis of BeachDrone frame 49 bug - CDF adaptation diverges in hidden frame OH=56 SB(32,160)
type: project
---

BeachDrone F49 (434K diffs) root cause analysis:

**Chain of error**: OH=64,56 correct → OH=52 CDF diverged → OH=50 wrong → OH=49 wrong (displayed F49)

**Exact divergence location**: Hidden frame OH=56, SB at miRow=32 miCol=160.
- Arith states match between our decoder and dav1d at SB boundaries through miRow=32.
- Within SB(32,160), block (44,164) 4x2 starts with matching rng=33800 but ends with different states.
- Our decoder has 43 blocks in this SB, dav1d has 42 (partition tree differs at (40,172)).

**Key findings**:
1. Block (32,160) 2x4 is decoded as intra by BOTH decoders (isInter=false). Arith matches after this block.
2. The CDF for IsInter[0] diverges massively by end of OH=56: ours=32705 vs dav1d=19418.
3. The IsInter CDF at start of OH=56 matches: both=31509.
4. Some neighbor context set by intra-in-inter blocks differs from dav1d, causing a subsequent inter block at (44,164) to read different symbols.

**Suspected root cause**: A missing or incorrect context update in `decodeIntraInInterBlock` or `setModeInfo` that propagates to a context read by a later inter block. The Filter field was fixed to [3,3] for intra-in-inter ModeInfo (matching dav1d), but this alone didn't resolve the issue. The remaining difference may be in coefficient context, TX context storage, or some other neighbor-dependent field.

**Why:** The error only manifests at 4th GOP boundary because it takes 3+ GOPs for the CDF drift to produce visible pixel differences. The CDF is saved to reference slots and inherited across GOPs.

**How to apply:** Focus on comparing the FULL set of context arrays (tx, tx_intra, coeff, pal_sz, seg_pred, etc.) between our `decodeIntraInInterBlock`/`setModeInfo` and dav1d's intra-in-inter context update (decode.c lines 1236-1262).
