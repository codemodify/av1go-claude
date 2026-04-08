---
name: CFL luma boundary fix
description: CFL AC luma downsampling must read from buffer (not clamp to frame boundary) to match dav1d
type: project
---

CFL (Chroma From Luma) AC computation must read luma pixels from the frame buffer directly, not clamp to the visible frame boundary. dav1d's cfl_ac_c reads from the buffer without frame-boundary checks; pixels beyond the visible frame come from reconstruction of blocks that straddle the frame edge.

**Why:** When the visible frame height doesn't align to the chroma block grid (e.g., 818px -> chromaH=409), CFL blocks near the bottom read luma rows beyond the visible frame. Clamping to lumaH repeated the last visible row; dav1d reads the actual reconstructed data in the buffer padding area. This caused max=1 diffs in V chroma that cascaded into larger errors on inter frames.

**How to apply:** In `reconstructCFL` (block.go), use `lumaBufH = len(Y)/strideY` and `lumaBufW = strideY` as the clamp boundaries instead of `frame.Width`/`frame.Height`. The frame buffer is allocated with extra rows beyond the visible frame specifically for this purpose.
