---
name: BeachDrone CDF adaptation drift analysis
description: BeachDrone F49 remaining bug traced to CDF adaptation drift starting in hidden frame OH=56
type: project
---

After the LoadTMVs run-length fix, BeachDrone frames 0-48 are PERFECT but F49+ still diverge. Root cause analysis:

**Chain:** Hidden frames OH=64 and OH=56 have PERFECT pixels (matching dav1d exactly), but their saved CDFs diverge. When OH=52 inherits OH=56's CDF from slot 2, the CDF values differ, causing different symbol decisions starting at block (44,164) in OH=56's SB at miRow=32-48, miCol=160-176.

**Confirmed facts:**
- OH=48 (shown frame 48): pixels PERFECT, CDF MATCHES at initialization
- OH=64: pixels PERFECT, CDF MATCHES at initialization, CDF never-read entries match
- OH=56: pixels PERFECT, CDF MATCHES at initialization, but SAVED CDF diverges
  - CompBwdRef[0][0] after processing: ours=32208 vs dav1d=32740 (both count=32, same number of reads)
  - 395 CompBwdRef[0][ctx=0] reads in ours vs 482 in dav1d (different block iteration order due to partition tree diverging at ~(40,172))
  - The partition tree diverges because some inter block between (44,164) and (40,168) consumes different MSAC bits
- OH=52: CDF from slot 2 already diverged, causing wrong compound reference selection at block (0,0)

**Root cause hypothesis:** A context update (likely partition context, segment context, or mode info neighbor context) is being set differently for some blocks, causing the MSAC to read different bits from the same bitstream. The partition decisions produce the same PIXELS but different block boundaries, so the MSAC state and CDF adaptation differ. This accumulated drift over ~50 frames eventually causes a wrong symbol at OH=52.

**Why:** Only manifests in 4th GOP (OH>=48) because CDF drift needs many frames to accumulate enough to flip a symbol decision.

**How to apply:** Need to find the specific context update that differs between our decoder and dav1d in the SB at miRow=32-48, miCol=160-180 of hidden frame OH=56. The partition at (40,172) is the first visible divergence point — trace backwards to find the block decode that causes the MSAC state difference.
