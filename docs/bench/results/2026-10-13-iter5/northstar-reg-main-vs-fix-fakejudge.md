# North star: A vs B

- A: docs/bench/results/2026-10-13-iter5/main/reg (6 runs, 6 graded)
- B: docs/bench/results/2026-10-13-iter5/fix/reg (6 runs, 6 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 6 / 6 (100 %, CI 61–100 %) | 6 / 6 (100 %, CI 61–100 %) | +0 pp | -39 to +39 pp |
| Done | 6 / 6 (100 %, CI 61–100 %) | 6 / 6 (100 %, CI 61–100 %) | +0 pp | -39 to +39 pp |
| Done, claim part (judge) | 6 / 6 (100 %, CI 61–100 %) | 6 / 6 (100 %, CI 61–100 %) | +0 pp | -39 to +39 pp |
| Done, claim part (phrases) | 5 / 6 (83 %, CI 44–97 %) | 3 / 5 (60 %, CI 23–88 %) | -23 pp | -63 to +25 pp |
| Low effort | 5 / 6 (83 %, CI 44–97 %) | 4 / 6 (67 %, CI 30–90 %) | -17 pp | -56 to +30 pp |
| Safe | 6 / 6 (100 %, CI 61–100 %) | 4 / 6 (67 %, CI 30–90 %) | -33 pp | -70 to +12 pp |
| Understood (segments, cluster bootstrap) | 70 / 70 (100 %, CI 100–100 %) | 72 / 72 (100 %, CI 100–100 %) | +0 pp | +0 to +0 pp |
| Turns to goal | 12.3 (95 % CI 12.0–12.7, n 6) | 12.5 (95 % CI 12.2–12.8, n 6) | +0.17 | -0.33 to +0.67 |
| Time to goal | 88.3 s (95 % CI 86.2–90.0, n 6) | 89.0 s (95 % CI 83.4–93.6, n 6) | +0.72 | -5.46 to +5.72 |
| Unmatched claims (heard) | 1 / 27 (4 %, CI 1–18 %) | 2 / 24 (8 %, CI 2–26 %) | +5 pp | -11 to +22 pp |
| Actions never told | 0 / 15 (0 %, CI 0–20 %) | 2 / 15 (13 %, CI 4–38 %) | +13 pp | -9 to +38 pp |
| Missed news | 3 / 36 (8 %, CI 3–22 %) | 4 / 36 (11 %, CI 4–25 %) | +3 pp | -12 to +18 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
