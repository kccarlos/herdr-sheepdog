# North star: A vs B

- A: docs/bench/results/2026-10-13-iter7/main/wd3-sonnet (21 runs, 21 graded)
- B: docs/bench/results/2026-10-13-iter7/fix/wd3-sonnet (21 runs, 21 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 21 / 21 (100 %, CI 85–100 %) | 21 / 21 (100 %, CI 85–100 %) | +0 pp | -15 to +15 pp |
| Done | 20 / 21 (95 %, CI 77–99 %) | 21 / 21 (100 %, CI 85–100 %) | +5 pp | -11 to +23 pp |
| Done, claim part (judge) | 21 / 21 (100 %, CI 85–100 %) | 20 / 21 (95 %, CI 77–99 %) | -5 pp | -23 to +11 pp |
| Done, claim part (phrases) | 15 / 20 (75 %, CI 53–89 %) | 15 / 21 (71 %, CI 50–86 %) | -4 pp | -29 to +23 pp |
| Low effort | 18 / 21 (86 %, CI 65–95 %) | 15 / 21 (71 %, CI 50–86 %) | -14 pp | -38 to +11 pp |
| Safe | 18 / 21 (86 %, CI 65–95 %) | 19 / 21 (90 %, CI 71–97 %) | +5 pp | -17 to +26 pp |
| Understood (segments, cluster bootstrap) | 229 / 249 (92 %, CI 88–95 %) | 232 / 253 (92 %, CI 87–95 %) | -0 pp | -5 to +5 pp |
| Turns to goal | 12.8 (95 % CI 12.0–13.8, n 21) | 12.7 (95 % CI 12.2–13.2, n 21) | -0.05 | -1.19 to +0.81 |
| Time to goal | 108.6 s (95 % CI 94.1–126.4, n 21) | 101.0 s (95 % CI 94.6–108.2, n 21) | -7.61 | -26.29 to +8.23 |
| Unmatched claims (heard) | 8 / 83 (10 %, CI 5–18 %) | 7 / 83 (8 %, CI 4–16 %) | -1 pp | -10 to +8 pp |
| Actions never told | 5 / 56 (9 %, CI 4–19 %) | 1 / 55 (2 %, CI 0–10 %) | -7 pp | -18 to +2 pp |
| Missed news | 23 / 140 (16 %, CI 11–23 %) | 17 / 143 (12 %, CI 8–18 %) | -5 pp | -13 to +4 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
