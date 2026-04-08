---
name: BigBuckBunny F127 investigation
description: BBB F127 (516K diffs) at order hint wrap-around boundary; investigated SaveTMVs/LoadTMVs sign convention, not yet fixed
type: project
---

BigBuckBunny-AV1 132 frames: F0-F126 ALL PERFECT, F127+ broken (516K diffs cascading).

F127 is the first shown frame after the 7-bit order hint wraps from 112 to 0 (= 128 mod 128).
The bitstream uses a scene-change structure with 4 hidden frames (#135-#138) before the shown frame (#139).

**Root cause analysis so far:**
- All hidden frames are lossless inter (BaseQIndex=0, CodedLossless=true)
- Hidden frame #135 (OH=0) gets res[0]=3 at pixel (0,0), output=63. The correct value may be 60 (matching the pre-wrap frame).
- The error cascades through subsequent hidden frames and to the shown frame
- The refSigns sign convention in SaveTMVs was verified CORRECT (matches dav1d: sign=true for past refs)
- Temporal MV projection (LoadTMVs) structure is identical between the working batch (OH=96) and broken batch (OH=0)
- getPocDiff handles wrap-around correctly

**Ruled out:**
- refSigns inversion (tested, made things worse)
- getPocDiff wrap-around bug
- LoadTMVs MFMV selection
- Skip mode frame derivation
- CDF initialization from wrong slot
- Loop filter issues (lossless = no filters)

**What still needs investigation:**
- The actual CDF state at frame #135 needs to be compared with dav1d (no easy way found yet)
- Could be a subtle SaveTMVs bug where the saved MV or Ref field is wrong for compound blocks near the OH wrap
- Could be a bug in the MV candidate stack that only manifests with wrapped order hints

**Critical linter bug found and fixed:**
- `show_existing_frame` keyframe check was being changed by linter from `d.refFrameType[idx]` to `fh.FrameType`, which is always 0 (KEY_FRAME) for show_existing_frame OBUs since frame_type isn't parsed. This wipes ALL reference slots on every show_existing_frame. Added detailed comment to prevent recurrence.

**How to apply:** The F127 bug is specific to streams with order hint wrap-around (7-bit hints, >128 frames). The linter `show_existing_frame` bug is a regression risk that must be watched for in every session.
