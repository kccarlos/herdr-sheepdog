# North star: A vs B

- A: docs/dev/bench/results/2026-11-03-status-order/subset/main-scale (15 runs, 15 graded)
- B: docs/dev/bench/results/2026-11-03-status-order/subset/b-scale (15 runs, 15 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 15 / 15 (100 %, CI 80–100 %) | 15 / 15 (100 %, CI 80–100 %) | +0 pp | -20 to +20 pp |
| Done | 15 / 15 (100 %, CI 80–100 %) | 15 / 15 (100 %, CI 80–100 %) | +0 pp | -20 to +20 pp |
| Done, claim part (judge) | 15 / 15 (100 %, CI 80–100 %) | 13 / 15 (87 %, CI 62–96 %) | -13 pp | -38 to +9 pp |
| Done, claim part (phrases) | 12 / 14 (86 %, CI 60–96 %) | 13 / 15 (87 %, CI 62–96 %) | +1 pp | -26 to +28 pp |
| Low effort | 6 / 15 (40 %, CI 20–64 %) | 5 / 15 (33 %, CI 15–58 %) | -7 pp | -37 to +25 pp |
| Safe | 13 / 15 (87 %, CI 62–96 %) | 15 / 15 (100 %, CI 80–100 %) | +13 pp | -9 to +38 pp |
| Understood (segments, cluster bootstrap) | 141 / 162 (87 %, CI 82–92 %) | 140 / 163 (86 %, CI 82–90 %) | -1 pp | -7 to +6 pp |
| Turns to goal | 11.7 (95 % CI 11.3–11.9, n 15) | 12.3 (95 % CI 11.9–12.7, n 15) | +0.60 | +0.07 to +1.13 |
| Time to goal | 84.3 s (95 % CI 81.4–87.0, n 15) | 87.0 s (95 % CI 83.6–90.5, n 15) | +2.70 | -1.69 to +7.34 |
| Unmatched claims (heard) | 2 / 77 (3 %, CI 1–9 %) | 4 / 77 (5 %, CI 2–13 %) | +3 pp | -5 to +10 pp |
| Actions never told | 3 / 60 (5 %, CI 2–14 %) | 0 / 59 (0 %, CI 0–6 %) | -5 pp | -14 to +2 pp |
| Missed news | 19 / 165 (12 %, CI 7–17 %) | 24 / 160 (15 %, CI 10–21 %) | +3 pp | -4 to +11 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
