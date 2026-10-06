# North star: A vs B

- A: t1 (segments) (35 runs, 35 graded)
- B: t2 (segments) (21 runs, 21 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 9 / 35 (26 %, CI 14–42 %) | 4 / 21 (19 %, CI 8–40 %) | -7 pp | -27 to +17 pp |
| Done | 30 / 35 (86 %, CI 71–94 %) | 21 / 21 (100 %, CI 85–100 %) | +14 pp | -3 to +29 pp |
| Low effort | 12 / 35 (34 %, CI 21–51 %) | 3 / 21 (14 %, CI 5–35 %) | -20 pp | -39 to +4 pp |
| Safe | 25 / 35 (71 %, CI 55–84 %) | 10 / 21 (48 %, CI 28–68 %) | -24 pp | -47 to +2 pp |
| Understood (segments, cluster bootstrap) | 344 / 416 (83 %, CI 79–87 %) | 182 / 250 (73 %, CI 68–77 %) | -10 pp | -16 to -4 pp |
| Turns to goal | 12.0 (95 % CI 11.7–12.3, n 18) | 11.8 (95 % CI 11.3–12.3, n 6) | -0.17 | -0.78 to +0.44 |
| Time to goal | 124.2 s (95 % CI 104.9–149.7, n 18) | 431.1 s (95 % CI 374.2–497.6, n 6) | +306.93 | +242.60 to +374.91 |
| Unmatched claims (heard) | 5 / 95 (5 %, CI 2–12 %) | 0 / 47 (0 %, CI 0–8 %) | -5 pp | -12 to +3 pp |
| Actions never told | 8 / 89 (9 %, CI 5–17 %) | 14 / 56 (25 %, CI 16–38 %) | +16 pp | +4 to +29 pp |
| Missed news | 96 / 239 (40 %, CI 34–46 %) | 84 / 141 (60 %, CI 51–67 %) | +19 pp | +9 to +29 pp |

- Arm A is tier 1 and arm B tier 2: the difference includes the tier's.

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
