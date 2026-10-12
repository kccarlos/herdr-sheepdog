# North star: A vs B

- A: docs/bench/results/2026-10-11-t2/a/merged (30 runs, 30 graded)
- B: docs/bench/results/2026-10-11-t2/nc/merged (30 runs, 30 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 8 / 30 (27 %, CI 14–44 %) | 9 / 30 (30 %, CI 17–48 %) | +3 pp | -19 to +25 pp |
| Done | 12 / 12 (100 %, CI 76–100 %) | 12 / 12 (100 %, CI 76–100 %) | +0 pp | -24 to +24 pp |
| Done, claim part (judge) | 9 / 12 (75 %, CI 47–91 %) | 8 / 12 (67 %, CI 39–86 %) | -8 pp | -40 to +26 pp |
| Done, claim part (phrases) | 9 / 15 (60 %, CI 36–80 %) | 13 / 16 (81 %, CI 57–93 %) | +21 pp | -10 to +48 pp |
| Low effort | 13 / 30 (43 %, CI 27–61 %) | 12 / 30 (40 %, CI 25–58 %) | -3 pp | -27 to +20 pp |
| Safe | 24 / 30 (80 %, CI 63–90 %) | 27 / 30 (90 %, CI 74–97 %) | +10 pp | -9 to +29 pp |
| Understood (segments, cluster bootstrap) | 108 / 143 (76 %, CI 64–86 %) | 112 / 144 (78 %, CI 71–85 %) | +2 pp | -10 to +15 pp |
| Turns to goal | 7.8 (95 % CI 5.6–10.1, n 16) | 7.4 (95 % CI 5.2–9.8, n 16) | -0.38 | -3.62 to +2.81 |
| Time to goal | 217.6 s (95 % CI 140.2–299.6, n 16) | 201.0 s (95 % CI 125.4–284.6, n 16) | -16.62 | -128.64 to +98.27 |
| Unmatched claims (heard) | 7 / 41 (17 %, CI 9–31 %) | 3 / 47 (6 %, CI 2–17 %) | -11 pp | -25 to +3 pp |
| Actions never told | 6 / 28 (21 %, CI 10–40 %) | 2 / 29 (7 %, CI 2–22 %) | -15 pp | -33 to +4 pp |
| Missed news | 29 / 84 (35 %, CI 25–45 %) | 27 / 84 (32 %, CI 23–43 %) | -2 pp | -16 to +12 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
