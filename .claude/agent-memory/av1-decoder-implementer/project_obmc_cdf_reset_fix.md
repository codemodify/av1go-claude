---
name: OBMC CDF counter reset bug fix
description: ResetAllCounts used resetMulti for OBMC CDFs but OBMC uses boolean CDF layout (counter at index 1)
type: project
---

OBMC CDF adaptation counters were not being reset between frames because ResetAllCounts() used resetMulti (resets cdf[len-1]) instead of resetBool (resets cdf[1]) for the OBMC arrays.

**Why:** Boolean CDFs (read via ReadSymbolBoolInt) store their adaptation counter at index 1, while multi-symbol CDFs store it at the last index. OBMC is read as a boolean but was categorized with multi-symbol CDFs in ResetAllCounts().

**How to apply:** When adding new CDFs to ResetAllCounts(), always verify whether the CDF is read via ReadSymbolBool/ReadSymbolBoolInt (use resetBool) or ReadSymbol (use resetMulti). The counter position differs.

Impact: Fixed CityHall (1.59M diffs -> 0) and BeachDrone (287K diffs -> 0) inter-frame decoding. The bug only manifested when multiple hidden frames in a GOP used different allow_warped_motion settings, causing OBMC CDFs to accumulate stale counters across frame boundaries.
