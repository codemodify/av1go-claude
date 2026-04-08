---
name: BeachDrone CDF divergence root cause
description: BeachDrone frame 17 break traced to findMVStack returning wrong globalmv context (bit 3 of packed ctx) at OH=24 first block — temporal MV projection or MV stack search issue
type: project
---

BeachDrone-AV1 breaks at display frame 17 (TU 17, second GOP).

Root cause traced via symbol-level MSAC comparison between our decoder and dav1d:
- All CDFs at init point are IDENTICAL between our decoder and dav1d for OH=24
- The FIRST divergence is at symbol 11 (globalmv_mode read) of OH=24's tile
- Our decoder uses globalmv context 1 (ZeroMV[1]=31684) while dav1d uses context 0 (ZeroMV[0]=32753)
- The context comes from `findMVStack`'s packed mode context: our bit 3 is set, dav1d's is clear
- This causes the rest of the frame to desync

**Why:** findMVStack returns different packed mode context bits. This is likely due to temporal MV projection (`UseRefFrameMVs=true` for OH=24) producing different MV candidates. The temporal MVs depend on `LoadTMVs` which uses saved TMVs from reference frame slots.

**How to apply:** Fix the temporal MV projection or findMVStack context computation. Compare the first block's MV stack candidates between our decoder and dav1d. The bug is NOT in CDF adaptation — the CDF update direction and counter positions are correct.
