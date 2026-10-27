# North star: A vs B

- A: before (60 runs, 60 graded)
- B: nosoul (60 runs, 60 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 45 / 60 (75 %, CI 63–84 %) | 47 / 60 (78 %, CI 66–87 %) | +3 pp | -12 to +18 pp |
| Done | 31 / 31 (100 %, CI 89–100 %) | 32 / 32 (100 %, CI 89–100 %) | +0 pp | -11 to +11 pp |
| Done, claim part (judge) | – / 0 (n/a, CI –) | – / 0 (n/a, CI –) | n/a | – |
| Done, claim part (phrases) | 27 / 32 (84 %, CI 68–93 %) | 30 / 35 (86 %, CI 71–94 %) | +1 pp | -16 to +19 pp |
| Low effort | 48 / 60 (80 %, CI 68–88 %) | 49 / 60 (82 %, CI 70–89 %) | +2 pp | -13 to +16 pp |
| Safe | 58 / 60 (97 %, CI 89–99 %) | 59 / 60 (98 %, CI 91–100 %) | +2 pp | -6 to +10 pp |
| Turns to goal | 2.4 (95 % CI 2.1–2.7, n 60) | 2.4 (95 % CI 2.2–2.6, n 59) | -0.01 | -0.36 to +0.36 |
| Time to goal | 12.5 s (95 % CI 11.1–14.0, n 60) | 13.0 s (95 % CI 11.4–14.5, n 59) | +0.45 | -1.81 to +2.67 |
| Unmatched claims (heard) | 5 / 41 (12 %, CI 5–26 %) | 5 / 52 (10 %, CI 4–21 %) | -3 pp | -17 to +10 pp |
| Actions never told | 1 / 38 (3 %, CI 0–13 %) | 0 / 42 (0 %, CI 0–8 %) | -3 pp | -13 to +6 pp |
| Missed news | 0 / 9 (0 %, CI 0–30 %) | 1 / 10 (10 %, CI 2–40 %) | +10 pp | -21 to +40 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
