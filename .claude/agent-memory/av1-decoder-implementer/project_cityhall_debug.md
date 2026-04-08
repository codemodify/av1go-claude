---
name: CityHall inter-frame reconstruction bug
description: CityHall 1.65M Y diffs -- MSAC perfect, F1 reconstruction perfect without filters, F2 diverges at [1216,128]; NOT a filter bug, NOT a CDF bug
type: project
---

CityHall inter-frame decoding bug is in inter prediction reconstruction, not CDF/MSAC/filters.

**Why:** Exhaustive debugging confirmed:
1. MSAC entropy state matches dav1d bit-for-bit (first 30 symbols of F1 verified)
2. CDF propagation/ResetAllCounts is correct (F1 hidden frame is PERFECT)
3. Disabling deblock/CDEF/LR individually all produce the SAME 1.65M Y diff count -- filters are not the root cause
4. Without ANY filters AND outputting all frames, F1 is PERFECT but F2 diverges at Y[1216,128] with ours=112 ref=111
5. F2 references the keyframe (slot 2) at that location with MV=(0,2), skip=true -- flat reference region where interpolation should trivially produce the same result
6. Sintel 3/3 and spbtv 5/5 remain PERFECT -- CityHall-specific

**How to apply:** The bug is in how hidden frame F2 (OrderHint=16) performs inter prediction from the keyframe. Since F1 is perfect and the entropy matches, the issue is specific to F2's reconstruction. The first error is at pixel (128, 1216) which is MI row 32, MI col 304 -- an 8x8 skip inter block referencing LAST_FRAME(slot 2) with small horizontal MV. Possible causes: wrong reference frame slot mapping for F2's ref indices, wrong MV derivation at a specific location, or a compound prediction path error.

Key test commands:
- Decode all frames without filters and compare: F1 should be PERFECT, F2 diverges
- dav1d comparison requires --outputinvisible 1 --inloopfilters none
- dav1d source built at /home/user/Projects/av1/dav1d/build with MSAC traces
