# North star: A vs B

- A: docs/bench/results/2026-10-11-t2/a/merged (30 runs, 30 graded)
- B: docs/bench/results/2026-10-11-t2/rl/merged (30 runs, 30 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 8 / 30 (27 %, CI 14–44 %) | 5 / 30 (17 %, CI 7–34 %) | -10 pp | -30 to +11 pp |
| Done | 12 / 12 (100 %, CI 76–100 %) | 12 / 12 (100 %, CI 76–100 %) | +0 pp | -24 to +24 pp |
| Done, claim part (judge) | 9 / 12 (75 %, CI 47–91 %) | 9 / 12 (75 %, CI 47–91 %) | +0 pp | -33 to +33 pp |
| Done, claim part (phrases) | 9 / 15 (60 %, CI 36–80 %) | 11 / 13 (85 %, CI 58–96 %) | +25 pp | -9 to +51 pp |
| Low effort | 13 / 30 (43 %, CI 27–61 %) | 8 / 30 (27 %, CI 14–44 %) | -17 pp | -38 to +7 pp |
| Safe | 24 / 30 (80 %, CI 63–90 %) | 27 / 30 (90 %, CI 74–97 %) | +10 pp | -9 to +29 pp |
| Understood (segments, cluster bootstrap) | 108 / 143 (76 %, CI 64–86 %) | 106 / 146 (73 %, CI 63–82 %) | -3 pp | -16 to +11 pp |
| Turns to goal | 7.8 (95 % CI 5.6–10.1, n 16) | 8.7 (95 % CI 5.5–11.7, n 10) | +0.89 | -3.00 to +4.74 |
| Time to goal | 217.6 s (95 % CI 140.2–299.6, n 16) | 251.3 s (95 % CI 138.4–356.2, n 10) | +33.69 | -110.91 to +175.04 |
| Unmatched claims (heard) | 7 / 41 (17 %, CI 9–31 %) | 2 / 35 (6 %, CI 2–19 %) | -11 pp | -26 to +4 pp |
| Actions never told | 6 / 28 (21 %, CI 10–40 %) | 3 / 28 (11 %, CI 4–27 %) | -11 pp | -30 to +9 pp |
| Missed news | 29 / 84 (35 %, CI 25–45 %) | 27 / 84 (32 %, CI 23–43 %) | -2 pp | -16 to +12 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
