---
name: IntraBC implementation rewrite
description: IntraBC block copy rewritten to match dav1d exactly: findMVStack, no drl_mode, MV clipping, varTx tree, proper chroma
type: project
---

## IntraBC rewrite (2026-04-02)

Rewrote `decodeIntraBCBlock` in block.go to match dav1d decode.c lines 1265-1379.

Key changes from original implementation:
- MV search: uses `findMVStack(ref0=-1, ref1=-1)` instead of custom scanning
- MV selection: NO drl_mode read; pick first non-zero from mvstack, then special default
- MV clipping: clips source to decoded tile regions (lines 1290-1343 in dav1d)
- VarTx tree: uses `readVarTxSize` + `decodeInterCoeffsVarTx` (matching inter blocks)
- Chroma source: aligned position `(miCol &^ subX) * hMul + mv >> (3+subX)`
- `ibcCopyBlock` uses reference bounds (MiCols*4, MiRows*4) not buffer dims
- Context: tx_intra set to block dim log2, matching dav1d set_ctx

Results: spbtv + BigBuckBunny ALL PERFECT (no regression). Sintel 360 2MB has remaining F95 diffs (5206 Y, down from 37K) but dav1d comparison unreliable due to film grain in stream.

**Why:** Original IntraBC had custom MV search, read drl_mode (wrong), used TX_MODE_LARGEST (wrong), and had simple chroma copy.

**How to apply:** Remaining F95 diffs are likely in MV search ordering or pixel copy. Coefficient decode is confirmed correct (same result with both old and new coeff paths). Film grain in Sintel 360 2MB makes dav1d comparison unreliable with dav1d 1.5.3 --filmgrain 0 flag.
