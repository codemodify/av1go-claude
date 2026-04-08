---
name: GLOBALMV substitution fix in findMVStack
description: add_spatial_candidate must substitute neighbor MVs with current block GMV for ROTZOOM/AFFINE refs, matching dav1d mf&1 logic
type: project
---

Root cause of BBB F127 and BeachDrone F49 CDF drift fixed 2026-04-02.

dav1d's `add_spatial_candidate` substitutes the neighbor's stored MV with the current block's global MV (`gmv[0]`) when:
1. The neighbor uses GLOBALMV mode (dav1d's `b->mf & 1` flag)
2. The current reference's global motion type is ROTZOOM or AFFINE (type > TRANSLATION)

For ROTZOOM/AFFINE, the global MV varies by block position. Without substitution, a GLOBALMV neighbor's stored MV (computed at its own position) differs slightly from the GMV at the current block's position, producing wrong MV candidates that cascade into CDF drift.

**Fix applied:**
1. Added `MF uint8` field to `ModeInfo` struct (bit 0 = GLOBALMV subst flag, bit 1 = NEWMV flag)
2. Set `MF` in `inter_block.go` when constructing ModeInfo, matching dav1d's logic
3. In `addSpatialCandidate` (and secondary scan helpers), check `info.MF&1` + `gmv0Valid` to substitute
4. Also use `info.MF&2` for `have_newmv_match` instead of the old `isNewMVMode()` function

**Why:** The bug only manifested when a reference used ROTZOOM global motion (e.g., BBB's GOLDEN ref at gmType=2). Without substitution, spatial candidates carried position-dependent MVs that don't match dav1d's output.

**How to apply:** Any future refmvs/findMVStack changes must preserve the GMV substitution logic. The `MF` field must be set correctly for all inter blocks.
