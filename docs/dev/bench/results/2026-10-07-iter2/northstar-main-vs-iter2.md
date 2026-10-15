# North star: A vs B

- A: docs/bench/results/2026-10-06-arms/t1-a0/workday (35 runs, 35 graded)
- B: docs/bench/results/2026-10-07-iter2/workday (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 28 / 35 (80 %, CI 64–90 %) | 30 / 35 (86 %, CI 71–94 %) | +6 pp | -12 to +24 pp |
| Low effort | 25 / 35 (71 %, CI 55–84 %) | 27 / 35 (77 %, CI 61–88 %) | +6 pp | -15 to +25 pp |
| Safe | 28 / 35 (80 %, CI 64–90 %) | 29 / 35 (83 %, CI 67–92 %) | +3 pp | -16 to +21 pp |
| Understood (segments, cluster bootstrap) | 379 / 426 (89 %, CI 86–92 %) | 387 / 421 (92 %, CI 90–94 %) | +3 pp | -1 to +7 pp |
| Turns to goal | 12.7 (95 % CI 12.3–13.1, n 35) | 12.5 (95 % CI 12.2–12.9, n 35) | -0.17 | -0.66 to +0.31 |
| Time to goal | 114.1 s (95 % CI 104.8–123.5, n 35) | 121.7 s (95 % CI 109.1–134.9, n 35) | +7.61 | -8.07 to +23.38 |
| Unmatched claims (heard) | 11 / 131 (8 %, CI 5–14 %) | 5 / 141 (4 %, CI 2–8 %) | -5 pp | -11 to +1 pp |
| Actions never told | 11 / 93 (12 %, CI 7–20 %) | 4 / 95 (4 %, CI 2–10 %) | -8 pp | -16 to +0 pp |
| Missed news | 81 / 235 (34 %, CI 29–41 %) | 88 / 234 (38 %, CI 32–44 %) | +3 pp | -6 to +12 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
