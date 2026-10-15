# North star: A vs B

- A: docs/bench/results/2026-10-12-it2/main/merged (12 runs, 12 graded)
- B: docs/bench/results/2026-10-12-it2/fix/merged (12 runs, 12 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 2 / 12 (17 %, CI 5–45 %) | 3 / 12 (25 %, CI 9–53 %) | +8 pp | -24 to +39 pp |
| Done | 12 / 12 (100 %, CI 76–100 %) | 12 / 12 (100 %, CI 76–100 %) | +0 pp | -24 to +24 pp |
| Done, claim part (judge) | 8 / 12 (67 %, CI 39–86 %) | 7 / 11 (64 %, CI 35–85 %) | -3 pp | -37 to +32 pp |
| Done, claim part (phrases) | 6 / 10 (60 %, CI 31–83 %) | 7 / 10 (70 %, CI 40–89 %) | +10 pp | -28 to +45 pp |
| Low effort | 2 / 12 (17 %, CI 5–45 %) | 4 / 12 (33 %, CI 14–61 %) | +17 pp | -18 to +47 pp |
| Safe | 5 / 12 (42 %, CI 19–68 %) | 5 / 12 (42 %, CI 19–68 %) | +0 pp | -35 to +35 pp |
| Understood (segments, cluster bootstrap) | 100 / 145 (69 %, CI 57–79 %) | 113 / 146 (77 %, CI 69–85 %) | +8 pp | -6 to +23 pp |
| Turns to goal | 13.0 (95 % CI 12.2–13.8, n 5) | 13.3 (95 % CI 12.6–13.9, n 7) | +0.29 | -0.74 to +1.31 |
| Time to goal | 468.7 s (95 % CI 393.0–544.7, n 5) | 474.7 s (95 % CI 402.2–540.8, n 7) | +6.06 | -105.16 to +110.59 |
| Unmatched claims (heard) | 6 / 29 (21 %, CI 10–38 %) | 4 / 28 (14 %, CI 6–31 %) | -6 pp | -26 to +14 pp |
| Actions never told | 8 / 29 (28 %, CI 15–46 %) | 7 / 30 (23 %, CI 12–41 %) | -4 pp | -26 to +18 pp |
| Missed news | 26 / 82 (32 %, CI 23–42 %) | 23 / 84 (27 %, CI 19–38 %) | -4 pp | -18 to +9 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
