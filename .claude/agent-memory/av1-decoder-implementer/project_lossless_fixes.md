---
name: Lossless mode fixes
description: Lossless inter/intra-in-inter fixes applied; remaining F1 diffs are pre-existing inter bug (CityHall/Sintel 10MB)
type: project
---

## Lossless keyframe fixes (previously applied in block.go)

1. CFL allowed for lossless 4x4 chroma blocks
2. txType forced to DCT_DCT (== WHT_WHT)
3. Frame buffer stride padding to MI boundaries

## Lossless inter-frame fixes (2026-04-02, applied in inter_block.go)

Four additional fixes for inter frames with CodedLossless=true:

1. **Inter chroma TX_4X4** (~line 658): `chromaTxSz = TX_4X4` in `decodeInterBlock`
2. **IntraInInter CFL allowed** (~line 1109): CFL for lossless 4x4 chroma (cbw4==1 && cbh4==1) in `decodeIntraInInterBlock`
3. **IntraInInter luma TX_4X4** (~line 1288): `lumaTxSz = TX_4X4` in `decodeIntraInInterBlock`
4. **IntraInInter chroma TX_4X4** (~line 1327): `chromaTxSz = TX_4X4` in `decodeIntraInInterBlock`

**Impact:** BeachDrone temporal sub-frame OH=16 went from 383K Y diffs to 0 Y diffs. Remaining F1 diffs (350K) are from a pre-existing general inter parsing bug.

**Pre-existing inter bug:** Also visible in non-lossless CityHall F1 (847K diffs), Sintel 360 10MB F4 (100K diffs). spbtv and Sintel 360 1MB unaffected. Bug likely in compound prediction, OBMC, warped motion, or inter-intra modes used in high-quality encodes. CityHall first diff at pixel (6,0), Sintel 10MB at pixel (129,0).
