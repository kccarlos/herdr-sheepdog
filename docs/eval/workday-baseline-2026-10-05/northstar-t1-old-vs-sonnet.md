# North star: A vs B

- A: t1 judged by gemini-3.1-flash-lite (original) (35 runs, 35 graded)
- B: t1 rejudged by Claude Sonnet (2026-10-06) (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 23 / 35 (66 %, CI 49–79 %) | 9 / 35 (26 %, CI 14–42 %) | -40 pp | -58 to -17 pp |
| Done | 30 / 35 (86 %, CI 71–94 %) | 30 / 35 (86 %, CI 71–94 %) | +0 pp | -17 to +17 pp |
| Low effort | 21 / 35 (60 %, CI 44–74 %) | 12 / 35 (34 %, CI 21–51 %) | -26 pp | -45 to -2 pp |
| Safe | 26 / 35 (74 %, CI 58–86 %) | 25 / 35 (71 %, CI 55–84 %) | -3 pp | -23 to +18 pp |
| Turns to goal | 12.2 (95 % CI 11.9–12.5, n 31) | 12.0 (95 % CI 11.7–12.3, n 18) | -0.23 | -0.68 to +0.22 |
| Time to goal | 128.5 s (95 % CI 115.3–144.6, n 31) | 124.2 s (95 % CI 104.9–149.7, n 18) | -4.37 | -29.35 to +24.88 |
| Unmatched claims (heard) | 5 / 95 (5 %, CI 2–12 %) | 5 / 95 (5 %, CI 2–12 %) | +0 pp | -7 to +7 pp |
| Actions never told | 8 / 89 (9 %, CI 5–17 %) | 8 / 89 (9 %, CI 5–17 %) | +0 pp | -9 to +9 pp |
| Missed news | 96 / 239 (40 %, CI 34–46 %) | 96 / 239 (40 %, CI 34–46 %) | +0 pp | -9 to +9 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
