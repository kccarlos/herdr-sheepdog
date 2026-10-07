# North star: A vs B

- A: docs/bench/results/2026-10-06-arms/t1-a0/workday (35 runs, 35 graded)
- B: docs/bench/results/2026-10-07-chatty/a0 (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 28 / 35 (80 %, CI 64–90 %) | 22 / 35 (63 %, CI 46–77 %) | -17 pp | -36 to +4 pp |
| Low effort | 25 / 35 (71 %, CI 55–84 %) | 26 / 35 (74 %, CI 58–86 %) | +3 pp | -18 to +23 pp |
| Safe | 28 / 35 (80 %, CI 64–90 %) | 27 / 35 (77 %, CI 61–88 %) | -3 pp | -22 to +16 pp |
| Understood (segments, cluster bootstrap) | 379 / 426 (89 %, CI 86–92 %) | 387 / 421 (92 %, CI 89–95 %) | +3 pp | -1 to +7 pp |
| Turns to goal | 12.7 (95 % CI 12.3–13.1, n 35) | 12.5 (95 % CI 12.1–12.9, n 35) | -0.20 | -0.71 to +0.31 |
| Time to goal | 114.1 s (95 % CI 104.8–123.5, n 35) | 108.7 s (95 % CI 96.6–125.1, n 35) | -5.41 | -21.46 to +11.92 |
| Unmatched claims (heard) | 11 / 131 (8 %, CI 5–14 %) | 19 / 145 (13 %, CI 9–20 %) | +5 pp | -3 to +12 pp |
| Actions never told | 11 / 93 (12 %, CI 7–20 %) | 9 / 90 (10 %, CI 5–18 %) | -2 pp | -11 to +8 pp |
| Missed news | 81 / 235 (34 %, CI 29–41 %) | 45 / 240 (19 %, CI 14–24 %) | -16 pp | -23 to -8 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
