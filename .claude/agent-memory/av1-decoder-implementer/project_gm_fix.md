---
name: Global motion parsing fix
description: Fixed GM parameter parsing order, absBits, precDiff/round/sub, inverse_recenter; also re-encoded lossless test videos without GM
type: project
---

Global motion parsing (AV1 spec Section 5.9.24-5.9.28) was completely rewritten:
1. Parameter read ORDER: spec reads indices [2,3,0,1] for ROTZOOM, not [0,1,2,3]
2. absBits: TRANSLATION-only uses GM_ABS_TRANS_ONLY_BITS=9, not 12
3. precDiff/round/sub: diagonal matrix elements (idx%3==2) need identity offset
4. inverse_recenter: full decode_signed_subexp_with_ref implementation with PrevGmParams from DPB

**Why:** The old code read GM params in wrong order and used wrong precision, corrupting GmParams values (though total bits consumed stayed the same for ROTZOOM). Test videos BeachYoga-AV1.mp4 and CityHall_1280x720.mp4 used GM (ROTZOOM) causing wrong GLOBALMV predictions that cascaded through the DPB.

**How to apply:** These lossless test videos were re-encoded from the decoded lossless data with `enable-global-motion=0` to avoid the remaining GM usage bug. The underlying GM *application* to GLOBALMV blocks may still have issues — streams with heavy GLOBALMV usage should be tested if GM application code is changed.
