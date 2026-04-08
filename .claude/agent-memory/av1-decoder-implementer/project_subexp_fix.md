---
name: decodeSignedSubexpWithRef fix
description: Global motion subexp decoder had wrong branch formulas causing GM param corruption after OH wrap; BBB 160/160 PERFECT
type: project
---

BBB F143 break root cause: `decodeSignedSubexpWithRef` in header.go had incorrect formulas for both branches.

**Bug:** The function used a simplified `if ref < 0` / `else` split with wrong recenter computations:
- "if" branch used `inverseRecenter(ref+high, v)` instead of `inverseRecenter(ref-low, v)` (off by 1 since high = mx+1 and -low = mx)
- "else" branch used the "if" formula instead of the spec's reflected formula `high-1-inverseRecenter(high-1-ref+low, v)+low`

**Fix:** Rewrote to match AV1 spec Section 5.9.26 and dav1d getbits.c exactly:
- Map to unsigned: `refU = ref - low`, `nU = high - low - 1`
- Branch on `refU*2 <= nU` (equivalent to spec's `(r<<1) <= high+low`)
- "if": `inverseRecenter(refU, v) + low`
- "else": `nU - inverseRecenter(nU-refU, v) + low`

**Impact:** Only affected GM params where the previous frame's refVal > 0 (positive matrix deltas). This only occurred after order hint wrapped (OH 127->0), because that's when the primary reference frame had significantly different GM params. Frames 0-142 were unaffected because their GM refs were all <= 0.

**Why:** The prior code was adapted from a simplified spec interpretation that didn't account for the asymmetric recenter mapping when ref is on the "far side" of the unsigned range.

**How to apply:** Any future subexp-related decoding should use the dav1d-matched unsigned mapping approach, not simplified signed formulas.
