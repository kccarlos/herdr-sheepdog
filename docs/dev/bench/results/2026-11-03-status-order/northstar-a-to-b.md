# North star: A vs B

- A: docs/dev/bench/results/2026-11-03-status-order/a/merged (45 runs, 45 graded)
- B: docs/dev/bench/results/2026-11-03-status-order/b/merged (45 runs, 45 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 45 / 45 (100 %, CI 92–100 %) | 45 / 45 (100 %, CI 92–100 %) | +0 pp | -8 to +8 pp |
| Done | 45 / 45 (100 %, CI 92–100 %) | 45 / 45 (100 %, CI 92–100 %) | +0 pp | -8 to +8 pp |
| Done, claim part (judge) | 43 / 45 (96 %, CI 85–99 %) | 41 / 45 (91 %, CI 79–96 %) | -4 pp | -17 to +7 pp |
| Done, claim part (phrases) | 33 / 44 (75 %, CI 61–85 %) | 28 / 44 (64 %, CI 49–76 %) | -11 pp | -29 to +8 pp |
| Low effort | 30 / 45 (67 %, CI 52–79 %) | 30 / 45 (67 %, CI 52–79 %) | +0 pp | -19 to +19 pp |
| Safe | 44 / 45 (98 %, CI 88–100 %) | 45 / 45 (100 %, CI 92–100 %) | +2 pp | -6 to +12 pp |
| Understood (segments, cluster bootstrap) | 470 / 523 (90 %, CI 87–92 %) | 470 / 521 (90 %, CI 88–93 %) | +0 pp | -3 to +4 pp |
| Turns to goal | 12.3 (95 % CI 11.9–12.7, n 45) | 12.5 (95 % CI 12.2–12.8, n 45) | +0.20 | -0.33 to +0.73 |
| Time to goal | 95.5 s (95 % CI 89.2–102.2, n 45) | 92.9 s (95 % CI 87.9–99.2, n 45) | -2.61 | -11.51 to +6.28 |
| Unmatched claims (heard) | 16 / 218 (7 %, CI 5–12 %) | 22 / 203 (11 %, CI 7–16 %) | +3 pp | -2 to +9 pp |
| Actions never told | 1 / 139 (1 %, CI 0–4 %) | 0 / 130 (0 %, CI 0–3 %) | -1 pp | -4 to +2 pp |
| Missed news | 61 / 350 (17 %, CI 14–22 %) | 56 / 345 (16 %, CI 13–20 %) | -1 pp | -7 to +4 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
