# North star: A vs B

- A: docs/bench/results/2026-10-13-iter6/main/wd (35 runs, 34 graded)
- B: docs/bench/results/2026-10-13-iter6/fix/wd (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 34 / 34 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 34 / 34 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done, claim part (judge) | 34 / 34 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done, claim part (phrases) | 21 / 34 (62 %, CI 45–76 %) | 20 / 34 (59 %, CI 42–74 %) | -3 pp | -25 to +19 pp |
| Low effort | 23 / 34 (68 %, CI 51–81 %) | 28 / 35 (80 %, CI 64–90 %) | +12 pp | -8 to +32 pp |
| Safe | 32 / 34 (94 %, CI 81–98 %) | 32 / 35 (91 %, CI 78–97 %) | -3 pp | -17 to +12 pp |
| Understood (segments, cluster bootstrap) | 413 / 413 (100 %, CI 100–100 %) | 413 / 413 (100 %, CI 100–100 %) | +0 pp | +0 to +0 pp |
| Turns to goal | 12.8 (95 % CI 12.4–13.2, n 34) | 12.3 (95 % CI 11.9–12.6, n 35) | -0.54 | -1.14 to +0.01 |
| Time to goal | 103.9 s (95 % CI 98.9–108.6, n 34) | 108.6 s (95 % CI 101.1–116.6, n 35) | +4.64 | -4.20 to +14.41 |
| Unmatched claims (heard) | 21 / 164 (13 %, CI 9–19 %) | 17 / 165 (10 %, CI 7–16 %) | -3 pp | -10 to +5 pp |
| Actions never told | 1 / 92 (1 %, CI 0–6 %) | 3 / 90 (3 %, CI 1–9 %) | +2 pp | -3 to +8 pp |
| Missed news | 35 / 233 (15 %, CI 11–20 %) | 39 / 243 (16 %, CI 12–21 %) | +1 pp | -6 to +8 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
