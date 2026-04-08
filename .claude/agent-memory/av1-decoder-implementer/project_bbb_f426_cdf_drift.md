---
name: BBB F426 CDF drift investigation  
description: MV CDF divergence at shown F426 (d=448); initial CDFs match but MV residual reads diverge, first NEWMV at MI(0,128)
type: project
---

BBB F426 (shown frame 426, FramesDecoded=448) has 475K pixel diffs starting at x=512 (MI col 128).

**Root cause trace:**
- All partition-level MSAC ranges match perfectly between Go and dav1d for the entire frame
- The MSAC diverges at the first MV residual read at block MI(0,128)
- Block (0,128) is the FIRST NEWMV block in the frame; no MV CDF adaptation before it
- Initial MVJoint CDF from primary_ref_frame matches dav1d: [25804,21220,17946]
- Primary_ref_frame=4 → RefFrameIdx[4]=0 → slot 0 (F414 lossless keyframe CDF)
- All CDFs match at frame boundaries through d=445+ 
- The CDF at frame start is correct; MSAC state matches; but MV joint/class reads produce different symbols

**How to apply:** The MV read divergence despite matching initial state suggests either a subtle MSAC implementation difference or a CDF initialization order issue. Verify by adding a targeted check: print MSAC dif/rng/cnt right before the mv_joint read for block MI(0,128) in both decoders and compare exactly.

**Three BBB clusters:** F426-428 (475K diffs), F573/575/577 (15-162 diffs), F661-663 (528K diffs). All near scene changes. Same root cause likely.
