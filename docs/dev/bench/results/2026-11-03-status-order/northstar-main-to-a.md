# North star: A vs B

- A: docs/dev/bench/results/2026-11-03-status-order/main/merged (45 runs, 45 graded)
- B: docs/dev/bench/results/2026-11-03-status-order/a/merged (45 runs, 45 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 37 / 45 (82 %, CI 69–91 %) | 45 / 45 (100 %, CI 92–100 %) | +18 pp | +6 to +31 pp |
| Done | 45 / 45 (100 %, CI 92–100 %) | 45 / 45 (100 %, CI 92–100 %) | +0 pp | -8 to +8 pp |
| Done, claim part (judge) | 39 / 45 (87 %, CI 74–94 %) | 43 / 45 (96 %, CI 85–99 %) | +9 pp | -4 to +22 pp |
| Done, claim part (phrases) | 28 / 43 (65 %, CI 50–78 %) | 33 / 44 (75 %, CI 61–85 %) | +10 pp | -9 to +28 pp |
| Low effort | 28 / 45 (62 %, CI 48–75 %) | 30 / 45 (67 %, CI 52–79 %) | +4 pp | -15 to +23 pp |
| Safe | 38 / 45 (84 %, CI 71–92 %) | 44 / 45 (98 %, CI 88–100 %) | +13 pp | +1 to +27 pp |
| Understood (segments, cluster bootstrap) | 468 / 525 (89 %, CI 87–91 %) | 470 / 523 (90 %, CI 87–92 %) | +1 pp | -3 to +4 pp |
| Turns to goal | 12.4 (95 % CI 11.9–13.0, n 39) | 12.3 (95 % CI 11.9–12.7, n 45) | -0.12 | -0.81 to +0.51 |
| Time to goal | 94.8 s (95 % CI 87.9–102.6, n 39) | 95.5 s (95 % CI 89.2–102.2, n 45) | +0.74 | -9.65 to +10.59 |
| Unmatched claims (heard) | 16 / 206 (8 %, CI 5–12 %) | 16 / 218 (7 %, CI 5–12 %) | -0 pp | -6 to +5 pp |
| Actions never told | 5 / 135 (4 %, CI 2–8 %) | 1 / 139 (1 %, CI 0–4 %) | -3 pp | -8 to +1 pp |
| Missed news | 55 / 353 (16 %, CI 12–20 %) | 61 / 350 (17 %, CI 14–22 %) | +2 pp | -4 to +7 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
