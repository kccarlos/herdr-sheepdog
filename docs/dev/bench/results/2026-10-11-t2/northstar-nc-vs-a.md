# North star: A vs B

- A: docs/bench/results/2026-10-11-t2/nc/merged (30 runs, 30 graded)
- B: docs/bench/results/2026-10-11-t2/a/merged (30 runs, 30 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 9 / 30 (30 %, CI 17–48 %) | 8 / 30 (27 %, CI 14–44 %) | -3 pp | -25 to +19 pp |
| Done | 12 / 12 (100 %, CI 76–100 %) | 12 / 12 (100 %, CI 76–100 %) | +0 pp | -24 to +24 pp |
| Done, claim part (judge) | 8 / 12 (67 %, CI 39–86 %) | 9 / 12 (75 %, CI 47–91 %) | +8 pp | -26 to +40 pp |
| Done, claim part (phrases) | 13 / 16 (81 %, CI 57–93 %) | 9 / 15 (60 %, CI 36–80 %) | -21 pp | -48 to +10 pp |
| Low effort | 12 / 30 (40 %, CI 25–58 %) | 13 / 30 (43 %, CI 27–61 %) | +3 pp | -20 to +27 pp |
| Safe | 27 / 30 (90 %, CI 74–97 %) | 24 / 30 (80 %, CI 63–90 %) | -10 pp | -29 to +9 pp |
| Understood (segments, cluster bootstrap) | 112 / 144 (78 %, CI 71–85 %) | 108 / 143 (76 %, CI 64–86 %) | -2 pp | -15 to +10 pp |
| Turns to goal | 7.4 (95 % CI 5.2–9.8, n 16) | 7.8 (95 % CI 5.6–10.1, n 16) | +0.38 | -2.94 to +3.69 |
| Time to goal | 201.0 s (95 % CI 125.4–284.6, n 16) | 217.6 s (95 % CI 140.2–299.6, n 16) | +16.62 | -101.76 to +132.63 |
| Unmatched claims (heard) | 3 / 47 (6 %, CI 2–17 %) | 7 / 41 (17 %, CI 9–31 %) | +11 pp | -3 to +25 pp |
| Actions never told | 2 / 29 (7 %, CI 2–22 %) | 6 / 28 (21 %, CI 10–40 %) | +15 pp | -4 to +33 pp |
| Missed news | 27 / 84 (32 %, CI 23–43 %) | 29 / 84 (35 %, CI 25–45 %) | +2 pp | -12 to +16 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
