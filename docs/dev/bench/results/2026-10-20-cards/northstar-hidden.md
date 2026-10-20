# North star: A vs B

- A: docs/dev/bench/results/2026-10-20-cards/subset/main-hidden (8 runs, 8 graded)
- B: docs/dev/bench/results/2026-10-20-cards/subset/cards-hidden (8 runs, 8 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 3 / 8 (38 %, CI 14–69 %) | 2 / 8 (25 %, CI 7–59 %) | -12 pp | -49 to +29 pp |
| Done | 8 / 8 (100 %, CI 68–100 %) | 8 / 8 (100 %, CI 68–100 %) | +0 pp | -32 to +32 pp |
| Done, claim part (judge) | 7 / 8 (88 %, CI 53–98 %) | 7 / 8 (88 %, CI 53–98 %) | +0 pp | -36 to +36 pp |
| Done, claim part (phrases) | 5 / 8 (62 %, CI 31–86 %) | 6 / 8 (75 %, CI 41–93 %) | +12 pp | -29 to +49 pp |
| Low effort | 4 / 8 (50 %, CI 22–78 %) | 6 / 8 (75 %, CI 41–93 %) | +25 pp | -19 to +59 pp |
| Safe | 5 / 8 (62 %, CI 31–86 %) | 7 / 8 (88 %, CI 53–98 %) | +25 pp | -17 to +59 pp |
| Understood (segments, cluster bootstrap) | 88 / 98 (90 %, CI 86–94 %) | 89 / 99 (90 %, CI 85–95 %) | +0 pp | -6 to +7 pp |
| Turns to goal | 13.0 (95 % CI 11.8–14.0, n 6) | 13.1 (95 % CI 12.3–14.1, n 7) | +0.14 | -1.24 to +1.67 |
| Time to goal | 93.7 s (95 % CI 83.3–103.4, n 6) | 97.8 s (95 % CI 88.1–107.3, n 7) | +4.12 | -9.54 to +19.17 |
| Unmatched claims (heard) | 4 / 30 (13 %, CI 5–30 %) | 3 / 31 (10 %, CI 3–25 %) | -4 pp | -21 to +14 pp |
| Actions never told | 2 / 20 (10 %, CI 3–30 %) | 1 / 18 (6 %, CI 1–26 %) | -4 pp | -25 to +17 pp |
| Missed news | 9 / 56 (16 %, CI 9–28 %) | 11 / 57 (19 %, CI 11–31 %) | +3 pp | -11 to +17 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
