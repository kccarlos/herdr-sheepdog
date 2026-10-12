# Iteration 3 tier-1 runs (2026-10-12)

- fix-v1/c1: fix 1 alone (budget at handover, lock wait bounded by the turn's own 9 s); diagnostic only, not in the fix arm.
- fix/{c2,c3}, main/{c1,c2}: workday-scale-ten x4 each, interleaved (main c1, fix c2, main c2, fix c3).
- fix/{r1,r2}, main/{r1,r2}: workday-morning-standup x3, workday-interruptions x3 per arm.
- At the planned n = 8 scale-ten runs per arm the total brain fallbacks per run were 0.75 (main) vs 0.25 (fix), difference -0.50, bootstrap 95 % CI -1.13 to +0.13: not beyond the CI.
- **Extension decided after seeing n = 8** (23:50): 8 more scale-ten runs per arm (main c3, fix c4, main c4, fix c5, interleaved), the merge rule applied at n = 16; the n = 8 and n = 16 results are reported separately.
