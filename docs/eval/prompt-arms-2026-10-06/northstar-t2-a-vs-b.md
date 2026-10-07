# North star: A vs B

- A: docs/bench/results/2026-10-06-arms/t2-a (12 runs, 12 graded)
- B: docs/bench/results/2026-10-06-arms/t2-b (12 runs, 12 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 12 / 12 (100 %, CI 76–100 %) | 12 / 12 (100 %, CI 76–100 %) | +0 pp | -24 to +24 pp |
| Done | 9 / 12 (75 %, CI 47–91 %) | 7 / 12 (58 %, CI 32–81 %) | -17 pp | -48 to +19 pp |
| Low effort | 2 / 12 (17 %, CI 5–45 %) | 1 / 12 (8 %, CI 1–35 %) | -8 pp | -37 to +21 pp |
| Safe | 4 / 12 (33 %, CI 14–61 %) | 8 / 12 (67 %, CI 39–86 %) | +33 pp | -6 to +61 pp |
| Understood (segments, cluster bootstrap) | 86 / 149 (58 %, CI 49–67 %) | 69 / 147 (47 %, CI 35–61 %) | -11 pp | -26 to +5 pp |
| Turns to goal | 14.2 (95 % CI 13.2–15.2, n 12) | 15.1 (95 % CI 13.3–16.9, n 12) | +0.92 | -1.08 to +3.00 |
| Time to goal | 444.5 s (95 % CI 381.0–527.9, n 12) | 509.9 s (95 % CI 426.7–602.6, n 12) | +65.43 | -50.90 to +182.32 |
| Unmatched claims (heard) | 1 / 18 (6 %, CI 1–26 %) | 3 / 23 (13 %, CI 5–32 %) | +7 pp | -14 to +27 pp |
| Actions never told | 8 / 28 (29 %, CI 15–47 %) | 5 / 23 (22 %, CI 10–42 %) | -7 pp | -29 to +17 pp |
| Missed news | 55 / 79 (70 %, CI 59–79 %) | 55 / 82 (67 %, CI 56–76 %) | -3 pp | -17 to +12 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
