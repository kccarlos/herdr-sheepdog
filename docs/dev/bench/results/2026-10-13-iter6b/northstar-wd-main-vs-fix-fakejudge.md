# North star: A vs B

- A: docs/bench/results/2026-10-13-iter6b/main/wd (35 runs, 35 graded)
- B: docs/bench/results/2026-10-13-iter6b/fix/wd (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done, claim part (judge) | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done, claim part (phrases) | 22 / 35 (63 %, CI 46–77 %) | 25 / 35 (71 %, CI 55–84 %) | +9 pp | -13 to +29 pp |
| Low effort | 23 / 35 (66 %, CI 49–79 %) | 18 / 35 (51 %, CI 36–67 %) | -14 pp | -35 to +8 pp |
| Safe | 32 / 35 (91 %, CI 78–97 %) | 32 / 35 (91 %, CI 78–97 %) | +0 pp | -15 to +15 pp |
| Understood (segments, cluster bootstrap) | 425 / 425 (100 %, CI 100–100 %) | 426 / 426 (100 %, CI 100–100 %) | +0 pp | +0 to +0 pp |
| Turns to goal | 12.7 (95 % CI 12.4–13.1, n 35) | 12.8 (95 % CI 12.3–13.4, n 35) | +0.09 | -0.51 to +0.71 |
| Time to goal | 113.6 s (95 % CI 103.6–127.7, n 35) | 112.2 s (95 % CI 106.3–118.8, n 35) | -1.39 | -16.44 to +10.33 |
| Unmatched claims (heard) | 20 / 163 (12 %, CI 8–18 %) | 13 / 165 (8 %, CI 5–13 %) | -4 pp | -11 to +2 pp |
| Actions never told | 5 / 97 (5 %, CI 2–12 %) | 3 / 97 (3 %, CI 1–9 %) | -2 pp | -9 to +4 pp |
| Missed news | 37 / 237 (16 %, CI 12–21 %) | 30 / 242 (12 %, CI 9–17 %) | -3 pp | -9 to +3 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
