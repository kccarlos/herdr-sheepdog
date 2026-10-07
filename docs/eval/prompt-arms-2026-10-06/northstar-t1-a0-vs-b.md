# North star: A vs B

- A: docs/bench/results/2026-10-06-arms/t1-a0/workday (35 runs, 35 graded)
- B: docs/bench/results/2026-10-06-arms/t1-b/workday (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 28 / 35 (80 %, CI 64–90 %) | 23 / 35 (66 %, CI 49–79 %) | -14 pp | -34 to +7 pp |
| Low effort | 25 / 35 (71 %, CI 55–84 %) | 25 / 35 (71 %, CI 55–84 %) | +0 pp | -21 to +21 pp |
| Safe | 28 / 35 (80 %, CI 64–90 %) | 27 / 35 (77 %, CI 61–88 %) | -3 pp | -22 to +16 pp |
| Understood (segments, cluster bootstrap) | 379 / 426 (89 %, CI 86–92 %) | 354 / 421 (84 %, CI 81–87 %) | -5 pp | -9 to -0 pp |
| Turns to goal | 12.7 (95 % CI 12.3–13.1, n 35) | 12.7 (95 % CI 12.3–13.1, n 35) | -0.03 | -0.57 to +0.51 |
| Time to goal | 114.1 s (95 % CI 104.8–123.5, n 35) | 121.6 s (95 % CI 109.7–133.9, n 35) | +7.50 | -7.41 to +22.86 |
| Unmatched claims (heard) | 11 / 131 (8 %, CI 5–14 %) | 16 / 145 (11 %, CI 7–17 %) | +3 pp | -5 to +10 pp |
| Actions never told | 11 / 93 (12 %, CI 7–20 %) | 7 / 91 (8 %, CI 4–15 %) | -4 pp | -13 to +5 pp |
| Missed news | 81 / 235 (34 %, CI 29–41 %) | 57 / 234 (24 %, CI 19–30 %) | -10 pp | -18 to -2 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
