---
name: BeachDrone F49 deep analysis
description: Deep analysis of BeachDrone F49 MV stack divergence - cascading temporal MV projection issue
type: project
---

BeachDrone F49 (OH=49, shown frame 49) breaks with 308K diffs. Root cause is in hidden frame OH=56.

**Confirmed facts:**
- MSAC state (entropy decoder) matches PERFECTLY between our decoder and dav1d for every block in OH=64 and OH=56
- Partition trees are identical (verified by comparing partition dumps)
- All bitstream decisions (mode, drl, residual) are the same
- The MV stack (findMVStack) diverges at block (46,48) in OH=56: entry[1] has mv=(2,24) in ours vs (2,22) in dav1d
- This 2-unit MV difference cascades through spatial neighbors, contaminating all subsequent blocks

**Root cause hypothesis:**
- The divergence cascades from a temporal MV projection difference in LoadTMVs
- OH=56 references slot 1 (OH=64) for temporal MVs, POC diff = -8, which is fine (not clamped)
- The raw TMV data and ref2ref values appear correct
- The bug is likely a subtle difference in how projected temporal MVs are stored or indexed, causing a single entry at an 8x8 position to differ by 2 units

**Key files:**
- `/home/user/Projects/av1/av1go/decoder/refmvs.go` - LoadTMVs, mvProjection, SaveTMVs
- `/home/user/Projects/av1/av1go/decoder/inter.go` - findMVStack

**Why:** BeachDrone uses orderHintBits=7 and ROTZOOM global motion. The 4th GOP (OH=64+) is the first where temporal MV distances become large enough (POC diff up to 48) to trigger the issue.

**How to apply:** Need to compare the full projected TMV grid between our decoder and dav1d at the start of OH=56. The difference is in a single or small number of TMV entries. Possible areas: position calculation in LoadTMVs (posX/posY computation with applySign), or the run-length optimization skip in dav1d that we don't implement.
