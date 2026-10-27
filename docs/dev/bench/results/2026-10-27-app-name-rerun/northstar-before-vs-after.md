# North star: A vs B

- A: out/before (60 runs, 60 graded)
- B: out/after (60 runs, 60 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 45 / 60 (75 %, CI 63–84 %) | 43 / 60 (72 %, CI 59–81 %) | -3 pp | -19 to +12 pp |
| Done | 31 / 31 (100 %, CI 89–100 %) | 34 / 34 (100 %, CI 90–100 %) | +0 pp | -10 to +11 pp |
| Done, claim part (judge) | – / 0 (n/a, CI –) | – / 0 (n/a, CI –) | n/a | – |
| Done, claim part (phrases) | 27 / 32 (84 %, CI 68–93 %) | 30 / 36 (83 %, CI 68–92 %) | -1 pp | -19 to +17 pp |
| Low effort | 48 / 60 (80 %, CI 68–88 %) | 43 / 60 (72 %, CI 59–81 %) | -8 pp | -23 to +7 pp |
| Safe | 58 / 60 (97 %, CI 89–99 %) | 57 / 60 (95 %, CI 86–98 %) | -2 pp | -11 to +7 pp |
| Turns to goal | 2.4 (95 % CI 2.1–2.7, n 60) | 2.3 (95 % CI 2.1–2.6, n 59) | -0.08 | -0.41 to +0.26 |
| Time to goal | 12.5 s (95 % CI 11.1–14.0, n 60) | 12.7 s (95 % CI 11.1–14.2, n 59) | +0.13 | -2.10 to +2.35 |
| Unmatched claims (heard) | 5 / 41 (12 %, CI 5–26 %) | 6 / 48 (12 %, CI 6–25 %) | +0 pp | -15 to +14 pp |
| Actions never told | 1 / 38 (3 %, CI 0–13 %) | 1 / 45 (2 %, CI 0–12 %) | -0 pp | -11 to +9 pp |
| Missed news | 1 / 43 (2 %, CI 0–12 %) | 2 / 44 (5 %, CI 1–15 %) | +2 pp | -8 to +13 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
