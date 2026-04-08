---
name: IntraBC partition context fix
description: IntraBC blocks missing abovePartCtx/leftPartCtx updates caused partition CDF drift on lossless keyframes with IntraBC
type: project
---

IntraBC blocks in `decodeIntraBCBlock` did not update `abovePartCtx`/`leftPartCtx` arrays, causing partition CDF context errors in subsequent blocks. The regular intra path (`setModeInfo`) and the inter path (`decodeInterBlock`) both updated these arrays, but the IntraBC path only updated `aboveModes`, `leftModes`, `aboveTxW`, `leftTxH`, `aboveSkip`, `leftSkip`.

**Why:** Without partition context updates, neighboring blocks read stale partition context bits, producing wrong partition CDF indices. This caused MSAC bitstream divergence that manifested as incorrect IntraBC vs intra decisions for subsequent blocks (e.g., reading `use_intrabc=true` where dav1d reads `false`). The bug only triggered on lossless keyframes with IntraBC blocks (AllowScreenContentTools=true, QP=0), which is common in BBB but not in simpler test videos.

**How to apply:** Fixed in `decodeIntraBCBlock` by adding `abovePartCtx`/`leftPartCtx` updates with T-split override support, matching `setModeInfo`'s logic. BBB F414-F425 went from 12+ broken frames to ALL PERFECT. Remaining F426-428 and F661-663 diffs are a separate pre-existing inter-frame bug, not IntraBC-related.
