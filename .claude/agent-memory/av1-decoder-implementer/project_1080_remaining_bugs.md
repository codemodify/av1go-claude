---
name: 1080p remaining bugs investigation
description: Analysis of the 2 remaining non-perfect 1080p Sintel videos (2MB and 5MB) out of 22 total
type: project
---

Status as of 2026-04-02: 20/22 videos are ALL PERFECT. Remaining:

## Sintel_1080_10s_5MB (8 diffs at worst)
- Frames 0-6: PERFECT. Frames 7-9: 4U+4V chroma diffs, all off by exactly 1
- Diffs are at the BOTTOM EDGE of the frame (chroma rows 404-408, chromaH=409)
- Root cause traced to deblock and CDEF filters modifying bottom-edge chroma pixels
- Deblock: vertical edge at U[592,404] applies 6-tap filter changing 131->130
  - The filter level is non-zero and the flat mask passes, so filter is applied
  - dav1d appears to NOT filter this edge (keeps 131)
  - Most likely cause: chroma deblock level differs between our code and dav1d
  - The chroma level uses bottom-right MI of 2x2 group; might be picking wrong neighbor near bottom edge
- CDEF: V[595,408] changed from 127->128
  - At the very last chroma row (408 of 409)
  - CDEF temp buffer correctly uses bufH (includes padding) not planeH
  - The CDEF change was verified NOT caused by using planeH (that made things worse)

## Sintel_1080_10s_2MB (146K diffs at worst, Frame 9)
- Frames 0-4: PERFECT. Frame 5: 113K Y diffs starting at y=632
- First diffs at Y[896,632] = MI(224,158), block mi(128,224) 128x128 NEWMV
- Pre-deblock value is already wrong: pred=46, res=-3, sum=43, deblock->44, ref expects 45
- The MC and reference frames were verified correct (Frame 4 is PERFECT)
- **Ruled out**: effectiveMiRows vs MiRows for TX clamping (reverting made things far worse)
- The 2-pixel prediction error is likely in the residual (inverse transform), not MC
- Cascading happens because wrong reference frame propagates to later frames

## Key findings
- The AV1 spec's MiRows must be used (not effective MI rows) for TX coefficient clamping
- Both bugs only appear in low-bitrate 1080p Sintel encodes (2MB and 5MB)
- Higher bitrate (10MB+) works perfectly at the same resolution
- All debug traces have been cleaned up from the code

**Why:** Understanding these bugs to achieve ALL 22 videos PERFECT.
**How to apply:** Focus next on chroma deblock level computation for 5MB, and residual/ITX verification for 2MB.
