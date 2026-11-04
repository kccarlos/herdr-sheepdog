# North star: A vs B

- A: docs/dev/bench/results/2026-11-03-status-order/main/merged (45 runs, 45 graded)
- B: docs/dev/bench/results/2026-11-03-status-order/b/merged (45 runs, 45 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 37 / 45 (82 %, CI 69–91 %) | 45 / 45 (100 %, CI 92–100 %) | +18 pp | +6 to +31 pp |
| Done | 45 / 45 (100 %, CI 92–100 %) | 45 / 45 (100 %, CI 92–100 %) | +0 pp | -8 to +8 pp |
| Done, claim part (judge) | 39 / 45 (87 %, CI 74–94 %) | 41 / 45 (91 %, CI 79–96 %) | +4 pp | -9 to +18 pp |
| Done, claim part (phrases) | 28 / 43 (65 %, CI 50–78 %) | 28 / 44 (64 %, CI 49–76 %) | -1 pp | -21 to +18 pp |
| Low effort | 28 / 45 (62 %, CI 48–75 %) | 30 / 45 (67 %, CI 52–79 %) | +4 pp | -15 to +23 pp |
| Safe | 38 / 45 (84 %, CI 71–92 %) | 45 / 45 (100 %, CI 92–100 %) | +16 pp | +4 to +29 pp |
| Understood (segments, cluster bootstrap) | 468 / 525 (89 %, CI 87–91 %) | 470 / 521 (90 %, CI 88–93 %) | +1 pp | -2 to +4 pp |
| Turns to goal | 12.4 (95 % CI 11.9–13.0, n 39) | 12.5 (95 % CI 12.2–12.8, n 45) | +0.08 | -0.56 to +0.67 |
| Time to goal | 94.8 s (95 % CI 87.9–102.6, n 39) | 92.9 s (95 % CI 87.9–99.2, n 45) | -1.88 | -11.28 to +7.40 |
| Unmatched claims (heard) | 16 / 206 (8 %, CI 5–12 %) | 22 / 203 (11 %, CI 7–16 %) | +3 pp | -3 to +9 pp |
| Actions never told | 5 / 135 (4 %, CI 2–8 %) | 0 / 130 (0 %, CI 0–3 %) | -4 pp | -8 to -0 pp |
| Missed news | 55 / 353 (16 %, CI 12–20 %) | 56 / 345 (16 %, CI 13–20 %) | +1 pp | -5 to +6 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
