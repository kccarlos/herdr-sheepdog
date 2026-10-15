# North star: A vs B

- A: docs/bench/results/2026-10-12-iter3/main/merged16 (22 runs, 22 graded)
- B: docs/bench/results/2026-10-12-iter3/fix/merged16 (22 runs, 22 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 22 / 22 (100 %, CI 85–100 %) | 22 / 22 (100 %, CI 85–100 %) | +0 pp | -15 to +15 pp |
| Done | 22 / 22 (100 %, CI 85–100 %) | 22 / 22 (100 %, CI 85–100 %) | +0 pp | -15 to +15 pp |
| Done, claim part (judge) | 21 / 22 (95 %, CI 78–99 %) | 22 / 22 (100 %, CI 85–100 %) | +5 pp | -11 to +22 pp |
| Done, claim part (phrases) | 21 / 22 (95 %, CI 78–99 %) | 18 / 20 (90 %, CI 70–97 %) | -5 pp | -26 to +13 pp |
| Low effort | 10 / 22 (45 %, CI 27–65 %) | 11 / 22 (50 %, CI 31–69 %) | +5 pp | -23 to +31 pp |
| Safe | 14 / 22 (64 %, CI 43–80 %) | 17 / 22 (77 %, CI 57–90 %) | +14 pp | -13 to +38 pp |
| Understood (segments, cluster bootstrap) | 215 / 249 (86 %, CI 82–91 %) | 212 / 248 (85 %, CI 81–89 %) | -1 pp | -7 to +5 pp |
| Turns to goal | 12.1 (95 % CI 12.0–12.2, n 22) | 12.0 (95 % CI 12.0–12.0, n 22) | -0.09 | -0.23 to +0.00 |
| Time to goal | 114.4 s (95 % CI 108.6–119.8, n 22) | 113.7 s (95 % CI 108.6–119.0, n 22) | -0.73 | -8.03 to +7.22 |
| Unmatched claims (heard) | 1 / 96 (1 %, CI 0–6 %) | 2 / 111 (2 %, CI 0–6 %) | +1 pp | -4 to +5 pp |
| Actions never told | 12 / 80 (15 %, CI 9–24 %) | 7 / 79 (9 %, CI 4–17 %) | -6 pp | -17 to +4 pp |
| Missed news | 30 / 212 (14 %, CI 10–19 %) | 38 / 211 (18 %, CI 13–24 %) | +4 pp | -3 to +11 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
