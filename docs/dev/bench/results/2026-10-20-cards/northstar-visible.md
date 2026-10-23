# North star: A vs B

- A: docs/dev/bench/results/2026-10-20-cards/subset/main-visible (10 runs, 10 graded)
- B: docs/dev/bench/results/2026-10-20-cards/subset/cards-visible (10 runs, 9 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 0 / 10 (0 %, CI 0–28 %) | 5 / 9 (56 %, CI 27–81 %) | +56 pp | +15 to +81 pp |
| Done | 10 / 10 (100 %, CI 72–100 %) | 9 / 9 (100 %, CI 70–100 %) | +0 pp | -30 to +28 pp |
| Done, claim part (judge) | 10 / 10 (100 %, CI 72–100 %) | 8 / 9 (89 %, CI 57–98 %) | -11 pp | -43 to +18 pp |
| Done, claim part (phrases) | 9 / 9 (100 %, CI 70–100 %) | 6 / 8 (75 %, CI 41–93 %) | -25 pp | -59 to +10 pp |
| Low effort | 4 / 10 (40 %, CI 17–69 %) | 6 / 9 (67 %, CI 35–88 %) | +27 pp | -16 to +58 pp |
| Safe | 9 / 10 (90 %, CI 60–98 %) | 6 / 9 (67 %, CI 35–88 %) | -23 pp | -56 to +14 pp |
| Understood (segments, cluster bootstrap) | 101 / 118 (86 %, CI 79–92 %) | 99 / 107 (93 %, CI 89–96 %) | +7 pp | -1 to +14 pp |
| Turns to goal | 12.2 (95 % CI 12.0–12.8, n 8) | 12.0 (95 % CI 11.4–12.7, n 7) | -0.25 | -1.07 to +0.61 |
| Time to goal | 85.1 s (95 % CI 78.6–93.1, n 8) | 91.9 s (95 % CI 78.8–106.6, n 7) | +6.86 | -8.96 to +25.19 |
| Unmatched claims (heard) | 0 / 40 (0 %, CI 0–9 %) | 2 / 35 (6 %, CI 2–19 %) | +6 pp | -4 to +19 pp |
| Actions never told | 0 / 24 (0 %, CI 0–14 %) | 1 / 23 (4 %, CI 1–21 %) | +4 pp | -10 to +21 pp |
| Missed news | 6 / 69 (9 %, CI 4–18 %) | 8 / 61 (13 %, CI 7–24 %) | +4 pp | -7 to +16 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
