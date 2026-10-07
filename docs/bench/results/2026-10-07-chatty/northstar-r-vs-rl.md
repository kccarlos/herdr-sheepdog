# North star: A vs B

- A: docs/bench/results/2026-10-07-chatty/r (35 runs, 35 graded)
- B: docs/bench/results/2026-10-07-chatty/rl (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 24 / 35 (69 %, CI 52–81 %) | 27 / 35 (77 %, CI 61–88 %) | +9 pp | -12 to +28 pp |
| Low effort | 25 / 35 (71 %, CI 55–84 %) | 29 / 35 (83 %, CI 67–92 %) | +11 pp | -8 to +30 pp |
| Safe | 29 / 35 (83 %, CI 67–92 %) | 24 / 35 (69 %, CI 52–81 %) | -14 pp | -33 to +6 pp |
| Understood (segments, cluster bootstrap) | 371 / 419 (89 %, CI 85–92 %) | 370 / 421 (88 %, CI 84–91 %) | -1 pp | -6 to +4 pp |
| Turns to goal | 12.7 (95 % CI 12.2–13.1, n 35) | 12.5 (95 % CI 12.1–12.8, n 35) | -0.20 | -0.74 to +0.31 |
| Time to goal | 108.3 s (95 % CI 98.5–118.6, n 35) | 113.4 s (95 % CI 103.0–124.8, n 35) | +5.02 | -9.95 to +19.74 |
| Unmatched claims (heard) | 12 / 138 (9 %, CI 5–15 %) | 13 / 141 (9 %, CI 5–15 %) | +1 pp | -6 to +7 pp |
| Actions never told | 9 / 92 (10 %, CI 5–18 %) | 12 / 91 (13 %, CI 8–22 %) | +3 pp | -6 to +13 pp |
| Missed news | 44 / 238 (18 %, CI 14–24 %) | 43 / 234 (18 %, CI 14–24 %) | -0 pp | -7 to +7 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
