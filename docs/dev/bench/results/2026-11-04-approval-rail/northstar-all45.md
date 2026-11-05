> Run-level Understood, Safe and the judged Done claim part compare Sonnet verdicts on main with `--judge fake` verdicts on the branch (or the reverse for partly judged sets): ignore them. Used: the programmatic rows (Done, never told, missed news, time) and the segment rows judged by Sonnet in both arms.

# North star: A vs B

- A: main (2026-11-03 baseline, 45 runs) (45 runs, 45 graded)
- B: branch (45 runs) (45 runs, 45 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 37 / 45 (82 %, CI 69–91 %) | 45 / 45 (100 %, CI 92–100 %) | +18 pp | +6 to +31 pp |
| Done | 45 / 45 (100 %, CI 92–100 %) | 45 / 45 (100 %, CI 92–100 %) | +0 pp | -8 to +8 pp |
| Done, claim part (judge) | 39 / 45 (87 %, CI 74–94 %) | 45 / 45 (100 %, CI 92–100 %) | +13 pp | +3 to +26 pp |
| Done, claim part (phrases) | 28 / 43 (65 %, CI 50–78 %) | 38 / 44 (86 %, CI 73–94 %) | +21 pp | +3 to +38 pp |
| Low effort | 28 / 45 (62 %, CI 48–75 %) | 29 / 45 (64 %, CI 50–77 %) | +2 pp | -17 to +21 pp |
| Safe | 38 / 45 (84 %, CI 71–92 %) | 41 / 45 (91 %, CI 79–96 %) | +7 pp | -8 to +21 pp |
| Understood (segments, cluster bootstrap) | 468 / 525 (89 %, CI 87–91 %) | 521 / 521 (100 %, CI 100–100 %) | +11 pp | +8 to +13 pp |
| Turns to goal | 12.4 (95 % CI 11.9–13.0, n 39) | 12.4 (95 % CI 12.0–12.7, n 45) | -0.05 | -0.68 to +0.53 |
| Time to goal | 94.8 s (95 % CI 87.9–102.6, n 39) | 96.0 s (95 % CI 91.0–101.1, n 45) | +1.26 | -8.10 to +9.69 |
| Unmatched claims (heard) | 16 / 206 (8 %, CI 5–12 %) | 7 / 208 (3 %, CI 2–7 %) | -4 pp | -9 to +0 pp |
| Actions never told | 5 / 135 (4 %, CI 2–8 %) | 4 / 132 (3 %, CI 1–8 %) | -1 pp | -6 to +4 pp |
| Missed news | 55 / 353 (16 %, CI 12–20 %) | 47 / 347 (14 %, CI 10–18 %) | -2 pp | -7 to +3 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
