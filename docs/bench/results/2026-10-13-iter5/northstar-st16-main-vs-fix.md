# North star: A vs B

- A: docs/bench/results/2026-10-13-iter5/main/st16 (16 runs, 16 graded)
- B: docs/bench/results/2026-10-13-iter5/fix/st16 (16 runs, 16 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 16 / 16 (100 %, CI 81–100 %) | 16 / 16 (100 %, CI 81–100 %) | +0 pp | -19 to +19 pp |
| Done | 16 / 16 (100 %, CI 81–100 %) | 16 / 16 (100 %, CI 81–100 %) | +0 pp | -19 to +19 pp |
| Done, claim part (judge) | 15 / 16 (94 %, CI 72–99 %) | 16 / 16 (100 %, CI 81–100 %) | +6 pp | -14 to +28 pp |
| Done, claim part (phrases) | 15 / 15 (100 %, CI 80–100 %) | 16 / 16 (100 %, CI 81–100 %) | +0 pp | -19 to +20 pp |
| Low effort | 4 / 16 (25 %, CI 10–49 %) | 5 / 16 (31 %, CI 14–56 %) | +6 pp | -24 to +35 pp |
| Safe | 15 / 16 (94 %, CI 72–99 %) | 15 / 16 (94 %, CI 72–99 %) | +0 pp | -23 to +23 pp |
| Understood (segments, cluster bootstrap) | 154 / 176 (88 %, CI 82–93 %) | 161 / 179 (90 %, CI 85–94 %) | +2 pp | -5 to +10 pp |
| Turns to goal | 12.2 (95 % CI 12.0–12.4, n 16) | 12.1 (95 % CI 11.8–12.5, n 16) | -0.06 | -0.44 to +0.38 |
| Time to goal | 101.3 s (95 % CI 96.4–106.8, n 16) | 99.6 s (95 % CI 93.6–108.7, n 16) | -1.65 | -9.94 to +8.62 |
| Unmatched claims (heard) | 0 / 73 (0 %, CI 0–5 %) | 0 / 79 (0 %, CI 0–5 %) | +0 pp | -5 to +5 pp |
| Actions never told | 0 / 65 (0 %, CI 0–6 %) | 0 / 65 (0 %, CI 0–6 %) | +0 pp | -6 to +6 pp |
| Missed news | 35 / 175 (20 %, CI 15–27 %) | 30 / 176 (17 %, CI 12–23 %) | -3 pp | -11 to +5 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
