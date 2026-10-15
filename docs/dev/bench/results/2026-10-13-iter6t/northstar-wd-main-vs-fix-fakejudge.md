# North star: A vs B

- A: docs/bench/results/2026-10-13-iter6t/main/wd (35 runs, 35 graded)
- B: docs/bench/results/2026-10-13-iter6t/fix/wd (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done, claim part (judge) | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done, claim part (phrases) | 23 / 33 (70 %, CI 53–83 %) | 25 / 34 (74 %, CI 57–85 %) | +4 pp | -17 to +25 pp |
| Low effort | 28 / 35 (80 %, CI 64–90 %) | 25 / 35 (71 %, CI 55–84 %) | -9 pp | -28 to +11 pp |
| Safe | 32 / 35 (91 %, CI 78–97 %) | 33 / 35 (94 %, CI 81–98 %) | +3 pp | -11 to +17 pp |
| Understood (segments, cluster bootstrap) | 417 / 417 (100 %, CI 100–100 %) | 418 / 418 (100 %, CI 100–100 %) | +0 pp | +0 to +0 pp |
| Turns to goal | 12.6 (95 % CI 12.2–13.0, n 35) | 12.7 (95 % CI 12.3–13.2, n 35) | +0.17 | -0.40 to +0.74 |
| Time to goal | 107.9 s (95 % CI 101.1–116.6, n 35) | 118.9 s (95 % CI 110.5–128.5, n 35) | +11.01 | -0.86 to +22.79 |
| Unmatched claims (heard) | 17 / 166 (10 %, CI 6–16 %) | 10 / 127 (8 %, CI 4–14 %) | -2 pp | -9 to +5 pp |
| Actions never told | 7 / 95 (7 %, CI 4–14 %) | 2 / 91 (2 %, CI 1–8 %) | -5 pp | -12 to +1 pp |
| Missed news | 42 / 236 (18 %, CI 13–23 %) | 39 / 248 (16 %, CI 12–21 %) | -2 pp | -9 to +5 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
