# North star: A vs B

- A: docs/bench/results/2026-10-14-iter6u/main/wd (35 runs, 35 graded)
- B: docs/bench/results/2026-10-14-iter6u/fix/wd (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done, claim part (judge) | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done, claim part (phrases) | 32 / 35 (91 %, CI 78–97 %) | 27 / 35 (77 %, CI 61–88 %) | -14 pp | -31 to +3 pp |
| Low effort | 23 / 35 (66 %, CI 49–79 %) | 23 / 35 (66 %, CI 49–79 %) | +0 pp | -21 to +21 pp |
| Safe | 34 / 35 (97 %, CI 85–99 %) | 32 / 35 (91 %, CI 78–97 %) | -6 pp | -20 to +7 pp |
| Understood (segments, cluster bootstrap) | 419 / 419 (100 %, CI 100–100 %) | 421 / 421 (100 %, CI 100–100 %) | +0 pp | +0 to +0 pp |
| Turns to goal | 12.6 (95 % CI 12.3–13.0, n 35) | 12.5 (95 % CI 12.2–12.9, n 35) | -0.09 | -0.60 to +0.43 |
| Time to goal | 112.2 s (95 % CI 107.2–117.2, n 35) | 119.7 s (95 % CI 111.5–129.9, n 35) | +7.47 | -2.17 to +18.63 |
| Unmatched claims (heard) | 5 / 156 (3 %, CI 1–7 %) | 10 / 146 (7 %, CI 4–12 %) | +4 pp | -1 to +9 pp |
| Actions never told | 0 / 89 (0 %, CI 0–4 %) | 3 / 89 (3 %, CI 1–9 %) | +3 pp | -1 to +9 pp |
| Missed news | 27 / 237 (11 %, CI 8–16 %) | 39 / 245 (16 %, CI 12–21 %) | +5 pp | -2 to +11 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
