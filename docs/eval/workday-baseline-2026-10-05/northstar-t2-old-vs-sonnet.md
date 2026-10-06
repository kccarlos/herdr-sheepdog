# North star: A vs B

- A: t2 judged by gemini-3.1-flash-lite (original) (21 runs, 21 graded)
- B: t2 rejudged by Claude Sonnet (21 runs, 21 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 13 / 21 (62 %, CI 41–79 %) | 4 / 21 (19 %, CI 8–40 %) | -43 pp | -64 to -13 pp |
| Done | 21 / 21 (100 %, CI 85–100 %) | 21 / 21 (100 %, CI 85–100 %) | +0 pp | -15 to +15 pp |
| Low effort | 9 / 21 (43 %, CI 24–63 %) | 3 / 21 (14 %, CI 5–35 %) | -29 pp | -51 to -1 pp |
| Safe | 10 / 21 (48 %, CI 28–68 %) | 10 / 21 (48 %, CI 28–68 %) | +0 pp | -28 to +28 pp |
| Turns to goal | 12.4 (95 % CI 11.9–12.8, n 18) | 11.8 (95 % CI 11.3–12.3, n 6) | -0.56 | -1.28 to +0.11 |
| Time to goal | 457.1 s (95 % CI 413.0–505.9, n 18) | 431.1 s (95 % CI 374.2–497.6, n 6) | -25.96 | -104.59 to +56.12 |
| Unmatched claims (heard) | 0 / 47 (0 %, CI 0–8 %) | 0 / 47 (0 %, CI 0–8 %) | +0 pp | -8 to +8 pp |
| Actions never told | 14 / 56 (25 %, CI 16–38 %) | 14 / 56 (25 %, CI 16–38 %) | +0 pp | -16 to +16 pp |
| Missed news | 84 / 141 (60 %, CI 51–67 %) | 84 / 141 (60 %, CI 51–67 %) | +0 pp | -11 to +11 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
