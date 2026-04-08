---
name: CFL buffer padding fix
description: Frame buffer extra rows increased from 8 to 128 to fix CFL AC computation at bottom edge for non-aligned frame heights
type: project
---

Frame buffer `extraY` increased from 8 to 128 in `NewFrameBuffer` (tile.go). This fixed CFL (Chroma-from-Luma) prediction producing wrong chroma values when a CFL coding block straddles the visible frame boundary.

**Root cause:** CFL AC computation reads downsampled luma from the frame buffer for the entire coding block. When the block extends beyond the visible frame (e.g., a 32x32 luma block at row 800 in a 818-pixel-tall frame extends to row 831), the AC reads from buffer rows beyond the visible frame. With only 8 extra rows (buffer height 826), rows 826-831 were never written by luma reconstruction (clamped to buffer size), so they contained zeros instead of actual reconstructed data. dav1d's 128-pixel border allocation ensures all block data is present.

**Why:** This affected only 1080p Sintel (height=818, non-8-aligned) because other test videos have 8-aligned heights where no blocks straddle the edge with CFL.

**How to apply:** The fix is in `/home/user/Projects/av1/av1go/decoder/tile.go` line ~182. Any future buffer size optimization must ensure extraY >= max SB dimension (128 for 128x128 SBs).

**Status as of 2026-04-02:** 21/22 test videos ALL PERFECT. Only Sintel_1080_10s_2MB remains with a separate bug (1-pixel MC/transform error at Y[896,632] in frame 5, not a boundary issue).
