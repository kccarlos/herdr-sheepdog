> Run-level Understood, Safe and the judged Done claim part compare Sonnet verdicts on main with `--judge fake` verdicts on the branch (or the reverse for partly judged sets): ignore them. Used: the programmatic rows (Done, never told, missed news, time) and the segment rows judged by Sonnet in both arms.

# North star: A vs B

- A: main (sample) (27 runs, 27 graded)
- B: branch (sample) (27 runs, 27 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 24 / 27 (89 %, CI 72–96 %) | 27 / 27 (100 %, CI 88–100 %) | +11 pp | -3 to +28 pp |
| Done | 27 / 27 (100 %, CI 88–100 %) | 27 / 27 (100 %, CI 88–100 %) | +0 pp | -12 to +12 pp |
| Done, claim part (judge) | 25 / 27 (93 %, CI 77–98 %) | 25 / 27 (93 %, CI 77–98 %) | +0 pp | -17 to +17 pp |
| Done, claim part (phrases) | 18 / 26 (69 %, CI 50–83 %) | 23 / 26 (88 %, CI 71–96 %) | +19 pp | -3 to +40 pp |
| Low effort | 15 / 27 (56 %, CI 37–72 %) | 12 / 27 (44 %, CI 28–63 %) | -11 pp | -35 to +15 pp |
| Safe | 23 / 27 (85 %, CI 68–94 %) | 24 / 27 (89 %, CI 72–96 %) | +4 pp | -15 to +23 pp |
| Understood (segments, cluster bootstrap) | 276 / 308 (90 %, CI 86–93 %) | 275 / 311 (88 %, CI 85–92 %) | -1 pp | -6 to +4 pp |
| Turns to goal | 12.2 (95 % CI 11.7–12.8, n 25) | 12.4 (95 % CI 12.0–12.8, n 27) | +0.21 | -0.53 to +0.92 |
| Time to goal | 89.8 s (95 % CI 83.5–98.3, n 25) | 92.6 s (95 % CI 86.6–99.4, n 27) | +2.75 | -7.28 to +12.54 |
| Unmatched claims (heard) | 9 / 129 (7 %, CI 4–13 %) | 4 / 125 (3 %, CI 1–8 %) | -4 pp | -10 to +2 pp |
| Actions never told | 3 / 90 (3 %, CI 1–9 %) | 3 / 88 (3 %, CI 1–10 %) | +0 pp | -6 to +7 pp |
| Missed news | 35 / 241 (15 %, CI 11–20 %) | 33 / 236 (14 %, CI 10–19 %) | -1 pp | -7 to +6 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
