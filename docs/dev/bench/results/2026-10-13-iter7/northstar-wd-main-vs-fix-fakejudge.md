# North star: A vs B

- A: docs/bench/results/2026-10-13-iter7/main/wd (35 runs, 35 graded)
- B: docs/bench/results/2026-10-13-iter7/fix/wd (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 34 / 35 (97 %, CI 85–99 %) | 35 / 35 (100 %, CI 90–100 %) | +3 pp | -7 to +15 pp |
| Done, claim part (judge) | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done, claim part (phrases) | 25 / 34 (74 %, CI 57–85 %) | 24 / 34 (71 %, CI 54–83 %) | -3 pp | -23 to +18 pp |
| Low effort | 30 / 35 (86 %, CI 71–94 %) | 25 / 35 (71 %, CI 55–84 %) | -14 pp | -33 to +5 pp |
| Safe | 31 / 35 (89 %, CI 74–95 %) | 32 / 35 (91 %, CI 78–97 %) | +3 pp | -13 to +18 pp |
| Understood (segments, cluster bootstrap) | 415 / 415 (100 %, CI 100–100 %) | 420 / 420 (100 %, CI 100–100 %) | +0 pp | +0 to +0 pp |
| Turns to goal | 12.6 (95 % CI 12.1–13.2, n 35) | 12.5 (95 % CI 12.2–12.9, n 35) | -0.06 | -0.74 to +0.57 |
| Time to goal | 106.3 s (95 % CI 96.6–118.1, n 35) | 100.9 s (95 % CI 96.5–105.6, n 35) | -5.38 | -17.04 to +5.79 |
| Unmatched claims (heard) | 12 / 141 (9 %, CI 5–14 %) | 13 / 154 (8 %, CI 5–14 %) | -0 pp | -7 to +6 pp |
| Actions never told | 6 / 91 (7 %, CI 3–14 %) | 2 / 89 (2 %, CI 1–8 %) | -4 pp | -12 to +2 pp |
| Missed news | 41 / 235 (17 %, CI 13–23 %) | 33 / 238 (14 %, CI 10–19 %) | -4 pp | -10 to +3 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
