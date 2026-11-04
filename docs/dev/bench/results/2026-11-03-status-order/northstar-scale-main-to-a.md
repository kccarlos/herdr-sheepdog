# North star: A vs B

- A: docs/dev/bench/results/2026-11-03-status-order/subset/main-scale (15 runs, 15 graded)
- B: docs/dev/bench/results/2026-11-03-status-order/subset/a-scale (15 runs, 15 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 15 / 15 (100 %, CI 80–100 %) | 15 / 15 (100 %, CI 80–100 %) | +0 pp | -20 to +20 pp |
| Done | 15 / 15 (100 %, CI 80–100 %) | 15 / 15 (100 %, CI 80–100 %) | +0 pp | -20 to +20 pp |
| Done, claim part (judge) | 15 / 15 (100 %, CI 80–100 %) | 15 / 15 (100 %, CI 80–100 %) | +0 pp | -20 to +20 pp |
| Done, claim part (phrases) | 12 / 14 (86 %, CI 60–96 %) | 15 / 15 (100 %, CI 80–100 %) | +14 pp | -9 to +40 pp |
| Low effort | 6 / 15 (40 %, CI 20–64 %) | 5 / 15 (33 %, CI 15–58 %) | -7 pp | -37 to +25 pp |
| Safe | 13 / 15 (87 %, CI 62–96 %) | 15 / 15 (100 %, CI 80–100 %) | +13 pp | -9 to +38 pp |
| Understood (segments, cluster bootstrap) | 141 / 162 (87 %, CI 82–92 %) | 142 / 164 (87 %, CI 83–90 %) | -0 pp | -6 to +6 pp |
| Turns to goal | 11.7 (95 % CI 11.3–11.9, n 15) | 11.8 (95 % CI 11.4–12.3, n 15) | +0.13 | -0.33 to +0.73 |
| Time to goal | 84.3 s (95 % CI 81.4–87.0, n 15) | 85.3 s (95 % CI 82.1–88.7, n 15) | +0.99 | -3.30 to +5.41 |
| Unmatched claims (heard) | 2 / 77 (3 %, CI 1–9 %) | 0 / 83 (0 %, CI 0–4 %) | -3 pp | -9 to +2 pp |
| Actions never told | 3 / 60 (5 %, CI 2–14 %) | 0 / 61 (0 %, CI 0–6 %) | -5 pp | -14 to +2 pp |
| Missed news | 19 / 165 (12 %, CI 7–17 %) | 34 / 166 (20 %, CI 15–27 %) | +9 pp | +1 to +17 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
