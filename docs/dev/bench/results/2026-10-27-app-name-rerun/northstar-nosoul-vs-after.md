# North star: A vs B

- A: nosoul (60 runs, 60 graded)
- B: after (60 runs, 60 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 47 / 60 (78 %, CI 66–87 %) | 43 / 60 (72 %, CI 59–81 %) | -7 pp | -22 to +9 pp |
| Done | 32 / 32 (100 %, CI 89–100 %) | 34 / 34 (100 %, CI 90–100 %) | +0 pp | -10 to +11 pp |
| Done, claim part (judge) | – / 0 (n/a, CI –) | – / 0 (n/a, CI –) | n/a | – |
| Done, claim part (phrases) | 30 / 35 (86 %, CI 71–94 %) | 30 / 36 (83 %, CI 68–92 %) | -2 pp | -20 to +15 pp |
| Low effort | 49 / 60 (82 %, CI 70–89 %) | 43 / 60 (72 %, CI 59–81 %) | -10 pp | -25 to +5 pp |
| Safe | 59 / 60 (98 %, CI 91–100 %) | 57 / 60 (95 %, CI 86–98 %) | -3 pp | -12 to +5 pp |
| Turns to goal | 2.4 (95 % CI 2.2–2.6, n 59) | 2.3 (95 % CI 2.1–2.6, n 59) | -0.07 | -0.37 to +0.25 |
| Time to goal | 13.0 s (95 % CI 11.4–14.5, n 59) | 12.7 s (95 % CI 11.1–14.2, n 59) | -0.32 | -2.53 to +1.94 |
| Unmatched claims (heard) | 5 / 52 (10 %, CI 4–21 %) | 6 / 48 (12 %, CI 6–25 %) | +3 pp | -10 to +16 pp |
| Actions never told | 0 / 42 (0 %, CI 0–8 %) | 1 / 45 (2 %, CI 0–12 %) | +2 pp | -6 to +12 pp |
| Missed news | 1 / 10 (10 %, CI 2–40 %) | 2 / 9 (22 %, CI 6–55 %) | +12 pp | -22 to +46 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
