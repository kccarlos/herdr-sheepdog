# North star: A vs B

- A: docs/bench/results/2026-10-07-chatty/a0 (35 runs, 35 graded)
- B: docs/bench/results/2026-10-07-chatty/r (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 22 / 35 (63 %, CI 46–77 %) | 24 / 35 (69 %, CI 52–81 %) | +6 pp | -16 to +27 pp |
| Low effort | 26 / 35 (74 %, CI 58–86 %) | 25 / 35 (71 %, CI 55–84 %) | -3 pp | -23 to +18 pp |
| Safe | 27 / 35 (77 %, CI 61–88 %) | 29 / 35 (83 %, CI 67–92 %) | +6 pp | -13 to +24 pp |
| Understood (segments, cluster bootstrap) | 387 / 421 (92 %, CI 89–95 %) | 371 / 419 (89 %, CI 85–92 %) | -3 pp | -8 to +1 pp |
| Turns to goal | 12.5 (95 % CI 12.1–12.9, n 35) | 12.7 (95 % CI 12.2–13.1, n 35) | +0.17 | -0.34 to +0.74 |
| Time to goal | 108.7 s (95 % CI 96.6–125.1, n 35) | 108.3 s (95 % CI 98.5–118.6, n 35) | -0.32 | -18.58 to +15.27 |
| Unmatched claims (heard) | 19 / 145 (13 %, CI 9–20 %) | 12 / 138 (9 %, CI 5–15 %) | -4 pp | -12 to +3 pp |
| Actions never told | 9 / 90 (10 %, CI 5–18 %) | 9 / 92 (10 %, CI 5–18 %) | -0 pp | -9 to +9 pp |
| Missed news | 45 / 240 (19 %, CI 14–24 %) | 44 / 238 (18 %, CI 14–24 %) | -0 pp | -7 to +7 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
