# North star: A vs B

- A: docs/bench/results/2026-10-11-t2/rl/merged (30 runs, 30 graded)
- B: docs/bench/results/2026-10-11-t2/a/merged (30 runs, 30 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 5 / 30 (17 %, CI 7–34 %) | 8 / 30 (27 %, CI 14–44 %) | +10 pp | -11 to +30 pp |
| Done | 12 / 12 (100 %, CI 76–100 %) | 12 / 12 (100 %, CI 76–100 %) | +0 pp | -24 to +24 pp |
| Done, claim part (judge) | 9 / 12 (75 %, CI 47–91 %) | 9 / 12 (75 %, CI 47–91 %) | +0 pp | -33 to +33 pp |
| Done, claim part (phrases) | 11 / 13 (85 %, CI 58–96 %) | 9 / 15 (60 %, CI 36–80 %) | -25 pp | -51 to +9 pp |
| Low effort | 8 / 30 (27 %, CI 14–44 %) | 13 / 30 (43 %, CI 27–61 %) | +17 pp | -7 to +38 pp |
| Safe | 27 / 30 (90 %, CI 74–97 %) | 24 / 30 (80 %, CI 63–90 %) | -10 pp | -29 to +9 pp |
| Understood (segments, cluster bootstrap) | 106 / 146 (73 %, CI 63–82 %) | 108 / 143 (76 %, CI 64–86 %) | +3 pp | -11 to +16 pp |
| Turns to goal | 8.7 (95 % CI 5.5–11.7, n 10) | 7.8 (95 % CI 5.6–10.1, n 16) | -0.89 | -4.69 to +3.05 |
| Time to goal | 251.3 s (95 % CI 138.4–356.2, n 10) | 217.6 s (95 % CI 140.2–299.6, n 16) | -33.69 | -176.06 to +105.48 |
| Unmatched claims (heard) | 2 / 35 (6 %, CI 2–19 %) | 7 / 41 (17 %, CI 9–31 %) | +11 pp | -4 to +26 pp |
| Actions never told | 3 / 28 (11 %, CI 4–27 %) | 6 / 28 (21 %, CI 10–40 %) | +11 pp | -9 to +30 pp |
| Missed news | 27 / 84 (32 %, CI 23–43 %) | 29 / 84 (35 %, CI 25–45 %) | +2 pp | -12 to +16 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
