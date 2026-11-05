> Run-level Understood, Safe and the judged Done claim part compare Sonnet verdicts on main with `--judge fake` verdicts on the branch (or the reverse for partly judged sets): ignore them. Used: the programmatic rows (Done, never told, missed news, time) and the segment rows judged by Sonnet in both arms.

# North star: A vs B

- A: main (scale) (15 runs, 15 graded)
- B: branch (scale) (15 runs, 15 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 15 / 15 (100 %, CI 80–100 %) | 15 / 15 (100 %, CI 80–100 %) | +0 pp | -20 to +20 pp |
| Done | 15 / 15 (100 %, CI 80–100 %) | 15 / 15 (100 %, CI 80–100 %) | +0 pp | -20 to +20 pp |
| Done, claim part (judge) | 15 / 15 (100 %, CI 80–100 %) | 14 / 15 (93 %, CI 70–99 %) | -7 pp | -30 to +14 pp |
| Done, claim part (phrases) | 12 / 14 (86 %, CI 60–96 %) | 15 / 15 (100 %, CI 80–100 %) | +14 pp | -9 to +40 pp |
| Low effort | 6 / 15 (40 %, CI 20–64 %) | 2 / 15 (13 %, CI 4–38 %) | -27 pp | -53 to +5 pp |
| Safe | 13 / 15 (87 %, CI 62–96 %) | 15 / 15 (100 %, CI 80–100 %) | +13 pp | -9 to +38 pp |
| Understood (segments, cluster bootstrap) | 141 / 162 (87 %, CI 82–92 %) | 145 / 166 (87 %, CI 83–92 %) | +0 pp | -6 to +7 pp |
| Turns to goal | 11.7 (95 % CI 11.3–11.9, n 15) | 12.3 (95 % CI 12.0–12.9, n 15) | +0.67 | +0.20 to +1.33 |
| Time to goal | 84.3 s (95 % CI 81.4–87.0, n 15) | 86.8 s (95 % CI 83.2–90.8, n 15) | +2.47 | -2.24 to +8.07 |
| Unmatched claims (heard) | 2 / 77 (3 %, CI 1–9 %) | 0 / 79 (0 %, CI 0–5 %) | -3 pp | -9 to +2 pp |
| Actions never told | 3 / 60 (5 %, CI 2–14 %) | 0 / 61 (0 %, CI 0–6 %) | -5 pp | -14 to +2 pp |
| Missed news | 19 / 165 (12 %, CI 7–17 %) | 22 / 165 (13 %, CI 9–19 %) | +2 pp | -5 to +9 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
