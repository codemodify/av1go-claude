---
name: CityHall skip_mode subpel filter fix
description: skip_mode blocks must not read subpel interpolation filter - found by MSAC range comparison with dav1d
type: project
---

CityHall inter-frame decoding had two bugs found on 2026-04-02:

1. **show_existing_frame not handled for OBU_FRAME_HEADER**: standalone frame headers (used for show_existing_frame) were silently ignored. Fixed by routing TypeFrameHeader through decodeFrame in both DecodeOBUs and DecodeAllOBUs.

2. **skip_mode blocks incorrectly read interpolation filter**: when skip_mode=1, dav1d sets has_subpel_filter=0, meaning no filter symbol is read from the bitstream. Our decoder was reading the filter for these blocks, causing MSAC state divergence that cascades through CDF adaptation to all subsequent frames.

**Why:** The AV1 spec requires that skip_mode blocks use predetermined reference frames and MVs without reading additional syntax. The interpolation filter is part of the "additional syntax" that should be skipped.

**How to apply:** When adding new syntax element reads to the inter block path, always check if they should be gated by `!skipMode`.

**Remaining:** Still ~1.58M Y diffs on CityHall shown frames. The skip_mode fix reduced diffs by ~67K. There is at least one more inter-frame parsing bug causing CDF state divergence.
