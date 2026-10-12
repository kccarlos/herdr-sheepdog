# North star: A vs B

- A: docs/bench/results/2026-10-12-iter3/main/merged (14 runs, 14 graded)
- B: docs/bench/results/2026-10-12-iter3/fix/merged (14 runs, 14 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 14 / 14 (100 %, CI 78–100 %) | 14 / 14 (100 %, CI 78–100 %) | +0 pp | -22 to +22 pp |
| Done | 14 / 14 (100 %, CI 78–100 %) | 14 / 14 (100 %, CI 78–100 %) | +0 pp | -22 to +22 pp |
| Done, claim part (judge) | 13 / 14 (93 %, CI 69–99 %) | 14 / 14 (100 %, CI 78–100 %) | +7 pp | -15 to +31 pp |
| Done, claim part (phrases) | 13 / 14 (93 %, CI 69–99 %) | 11 / 13 (85 %, CI 58–96 %) | -8 pp | -36 to +18 pp |
| Low effort | 7 / 14 (50 %, CI 27–73 %) | 8 / 14 (57 %, CI 33–79 %) | +7 pp | -27 to +39 pp |
| Safe | 8 / 14 (57 %, CI 33–79 %) | 12 / 14 (86 %, CI 60–96 %) | +29 pp | -5 to +55 pp |
| Understood (segments, cluster bootstrap) | 136 / 161 (84 %, CI 78–90 %) | 143 / 160 (89 %, CI 84–94 %) | +5 pp | -3 to +13 pp |
| Turns to goal | 12.1 (95 % CI 12.0–12.2, n 14) | 12.0 (95 % CI 12.0–12.0, n 14) | -0.07 | -0.21 to +0.00 |
| Time to goal | 114.3 s (95 % CI 107.0–122.2, n 14) | 117.4 s (95 % CI 111.3–123.7, n 14) | +3.11 | -6.60 to +13.00 |
| Unmatched claims (heard) | 1 / 55 (2 %, CI 0–10 %) | 2 / 71 (3 %, CI 1–10 %) | +1 pp | -7 to +8 pp |
| Actions never told | 9 / 48 (19 %, CI 10–32 %) | 3 / 47 (6 %, CI 2–17 %) | -12 pp | -26 to +1 pp |
| Missed news | 16 / 124 (13 %, CI 8–20 %) | 21 / 124 (17 %, CI 11–25 %) | +4 pp | -5 to +13 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
