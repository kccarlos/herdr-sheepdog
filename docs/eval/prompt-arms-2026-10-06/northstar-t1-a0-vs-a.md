# North star: A vs B

- A: docs/bench/results/2026-10-06-arms/t1-a0/workday (35 runs, 35 graded)
- B: docs/bench/results/2026-10-06-arms/t1-a/workday (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 28 / 35 (80 %, CI 64–90 %) | 24 / 35 (69 %, CI 52–81 %) | -11 pp | -31 to +9 pp |
| Low effort | 25 / 35 (71 %, CI 55–84 %) | 26 / 35 (74 %, CI 58–86 %) | +3 pp | -18 to +23 pp |
| Safe | 28 / 35 (80 %, CI 64–90 %) | 28 / 35 (80 %, CI 64–90 %) | +0 pp | -19 to +19 pp |
| Understood (segments, cluster bootstrap) | 379 / 426 (89 %, CI 86–92 %) | 374 / 421 (89 %, CI 86–92 %) | -0 pp | -4 to +4 pp |
| Turns to goal | 12.7 (95 % CI 12.3–13.1, n 35) | 12.4 (95 % CI 12.1–12.7, n 35) | -0.29 | -0.77 to +0.20 |
| Time to goal | 114.1 s (95 % CI 104.8–123.5, n 35) | 109.9 s (95 % CI 99.7–120.3, n 35) | -4.13 | -17.47 to +9.44 |
| Unmatched claims (heard) | 11 / 131 (8 %, CI 5–14 %) | 13 / 139 (9 %, CI 6–15 %) | +1 pp | -6 to +8 pp |
| Actions never told | 11 / 93 (12 %, CI 7–20 %) | 8 / 94 (9 %, CI 4–16 %) | -3 pp | -12 to +6 pp |
| Missed news | 81 / 235 (34 %, CI 29–41 %) | 85 / 239 (36 %, CI 30–42 %) | +1 pp | -7 to +10 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
