# North star: A vs B

- A: docs/bench/results/2026-10-06-arms/t1-a0/workday (35 runs, 35 graded)
- B: docs/bench/results/2026-10-07-iter1/workday (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 28 / 35 (80 %, CI 64–90 %) | 24 / 35 (69 %, CI 52–81 %) | -11 pp | -31 to +9 pp |
| Low effort | 25 / 35 (71 %, CI 55–84 %) | 25 / 35 (71 %, CI 55–84 %) | +0 pp | -21 to +21 pp |
| Safe | 28 / 35 (80 %, CI 64–90 %) | 29 / 35 (83 %, CI 67–92 %) | +3 pp | -16 to +21 pp |
| Understood (segments, cluster bootstrap) | 379 / 426 (89 %, CI 86–92 %) | 355 / 417 (85 %, CI 80–89 %) | -4 pp | -9 to +2 pp |
| Turns to goal | 12.7 (95 % CI 12.3–13.1, n 35) | 12.4 (95 % CI 12.0–12.8, n 35) | -0.29 | -0.83 to +0.26 |
| Time to goal | 114.1 s (95 % CI 104.8–123.5, n 35) | 120.5 s (95 % CI 108.7–132.3, n 35) | +6.45 | -8.01 to +20.75 |
| Unmatched claims (heard) | 11 / 131 (8 %, CI 5–14 %) | 12 / 147 (8 %, CI 5–14 %) | -0 pp | -7 to +6 pp |
| Actions never told | 11 / 93 (12 %, CI 7–20 %) | 7 / 94 (7 %, CI 4–15 %) | -4 pp | -13 to +4 pp |
| Missed news | 81 / 235 (34 %, CI 29–41 %) | 74 / 237 (31 %, CI 26–37 %) | -3 pp | -12 to +5 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
