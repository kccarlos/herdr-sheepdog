# North star: A vs B

- A: docs/dev/bench/results/2026-10-20-cards/main/merged (18 runs, 18 graded)
- B: docs/dev/bench/results/2026-10-20-cards/cards/merged (18 runs, 17 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 3 / 18 (17 %, CI 6–39 %) | 7 / 17 (41 %, CI 22–64 %) | +25 pp | -5 to +50 pp |
| Done | 18 / 18 (100 %, CI 82–100 %) | 17 / 17 (100 %, CI 82–100 %) | +0 pp | -18 to +18 pp |
| Done, claim part (judge) | 17 / 18 (94 %, CI 74–99 %) | 15 / 17 (88 %, CI 66–97 %) | -6 pp | -29 to +16 pp |
| Done, claim part (phrases) | 14 / 17 (82 %, CI 59–94 %) | 12 / 16 (75 %, CI 51–90 %) | -7 pp | -34 to +20 pp |
| Low effort | 8 / 18 (44 %, CI 25–66 %) | 12 / 17 (71 %, CI 47–87 %) | +26 pp | -6 to +52 pp |
| Safe | 14 / 18 (78 %, CI 55–91 %) | 13 / 17 (76 %, CI 53–90 %) | -1 pp | -28 to +26 pp |
| Understood (segments, cluster bootstrap) | 189 / 216 (88 %, CI 83–91 %) | 188 / 206 (91 %, CI 88–94 %) | +4 pp | -1 to +9 pp |
| Turns to goal | 12.6 (95 % CI 12.0–13.1, n 14) | 12.6 (95 % CI 11.9–13.3, n 14) | +0.00 | -0.86 to +0.86 |
| Time to goal | 88.8 s (95 % CI 82.5–94.7, n 14) | 94.9 s (95 % CI 86.2–104.0, n 14) | +6.11 | -4.87 to +17.47 |
| Unmatched claims (heard) | 4 / 70 (6 %, CI 2–14 %) | 5 / 66 (8 %, CI 3–17 %) | +2 pp | -7 to +11 pp |
| Actions never told | 2 / 44 (5 %, CI 1–15 %) | 2 / 41 (5 %, CI 1–16 %) | +0 pp | -11 to +12 pp |
| Missed news | 15 / 125 (12 %, CI 7–19 %) | 19 / 118 (16 %, CI 11–24 %) | +4 pp | -5 to +13 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
