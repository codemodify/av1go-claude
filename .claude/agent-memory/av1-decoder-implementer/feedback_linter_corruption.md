---
name: External linter silently corrupts source files
description: IDE/linter tool modifies files after edits - must verify no unwanted changes, can corrupt intra.go, decoder.go, inter_block.go
type: feedback
---

An external linter/IDE tool silently modifies source files after Edit operations. Observed behaviors:
1. Injected Z2 topLeft smoothing filter into intra.go (broke ALL keyframe decoding)
2. Added dbg113 debug variable to inter_block.go (caused build failure)
3. Added TMV zeroing experiment to decoder.go (changed decoding behavior)
4. Changed trace ranges (48-56 -> 134-142) and added reference frame dump traces
5. Reverted intentional bug fixes with comments justifying the revert
6. Changed `fh.FrameType` to `d.refFrameType[idx]` in show_existing_frame check

**Why:** Caused severe regression where spbtv frame 0 failed due to corrupted intra prediction.

**How to apply:** After every Edit:
1. Re-read modified lines to verify no corruption
2. Before tests, run `git diff -- decoder/intra.go` to check for unwanted changes
3. If a previously-passing test fails, restore from git: `git show HEAD:decoder/intra.go > /tmp/orig.go && cp /tmp/orig.go decoder/intra.go`
4. For show_existing_frame: must use `d.refFrameType[idx]` NOT `fh.FrameType` (fh.FrameType defaults to 0 for all show_existing_frame OBUs)
