# North star: A vs B

- A: t1 (35 runs, 35 graded)
- B: t2 (21 runs, 21 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 23 / 35 (66 %, CI 49–79 %) | 13 / 21 (62 %, CI 41–79 %) | -4 pp | -29 to +20 pp |
| Done | 30 / 35 (86 %, CI 71–94 %) | 21 / 21 (100 %, CI 85–100 %) | +14 pp | -3 to +29 pp |
| Low effort | 21 / 35 (60 %, CI 44–74 %) | 9 / 21 (43 %, CI 24–63 %) | -17 pp | -41 to +9 pp |
| Safe | 26 / 35 (74 %, CI 58–86 %) | 10 / 21 (48 %, CI 28–68 %) | -27 pp | -49 to -1 pp |
| Turns to goal | 12.2 (95 % CI 11.9–12.5, n 31) | 12.4 (95 % CI 11.9–12.8, n 18) | +0.16 | -0.37 to +0.68 |
| Time to goal | 128.5 s (95 % CI 115.3–144.6, n 31) | 457.1 s (95 % CI 413.0–505.9, n 18) | +328.51 | +278.94 to +378.28 |
| Unmatched claims (heard) | 5 / 95 (5 %, CI 2–12 %) | 0 / 47 (0 %, CI 0–8 %) | -5 pp | -12 to +3 pp |
| Actions never told | 8 / 89 (9 %, CI 5–17 %) | 14 / 56 (25 %, CI 16–38 %) | +16 pp | +4 to +29 pp |
| Missed news | 96 / 239 (40 %, CI 34–46 %) | 84 / 141 (60 %, CI 51–67 %) | +19 pp | +9 to +29 pp |

- Arm A is tier 1 and arm B tier 2: the difference includes the tier's.

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
