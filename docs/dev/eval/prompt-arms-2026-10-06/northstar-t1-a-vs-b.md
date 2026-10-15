# North star: A vs B

- A: docs/dev/bench/results/2026-10-06-arms/t1-a/workday (35 runs, 35 graded)
- B: docs/dev/bench/results/2026-10-06-arms/t1-b/workday (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 24 / 35 (69 %, CI 52–81 %) | 23 / 35 (66 %, CI 49–79 %) | -3 pp | -24 to +18 pp |
| Low effort | 26 / 35 (74 %, CI 58–86 %) | 25 / 35 (71 %, CI 55–84 %) | -3 pp | -23 to +18 pp |
| Safe | 28 / 35 (80 %, CI 64–90 %) | 27 / 35 (77 %, CI 61–88 %) | -3 pp | -22 to +16 pp |
| Understood (segments, cluster bootstrap) | 374 / 421 (89 %, CI 86–92 %) | 354 / 421 (84 %, CI 81–87 %) | -5 pp | -9 to -1 pp |
| Turns to goal | 12.4 (95 % CI 12.1–12.7, n 35) | 12.7 (95 % CI 12.3–13.1, n 35) | +0.26 | -0.26 to +0.77 |
| Time to goal | 109.9 s (95 % CI 99.7–120.3, n 35) | 121.6 s (95 % CI 109.7–133.9, n 35) | +11.63 | -4.08 to +27.95 |
| Unmatched claims (heard) | 13 / 139 (9 %, CI 6–15 %) | 16 / 145 (11 %, CI 7–17 %) | +2 pp | -6 to +9 pp |
| Actions never told | 8 / 94 (9 %, CI 4–16 %) | 7 / 91 (8 %, CI 4–15 %) | -1 pp | -9 to +8 pp |
| Missed news | 85 / 239 (36 %, CI 30–42 %) | 57 / 234 (24 %, CI 19–30 %) | -11 pp | -19 to -3 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
