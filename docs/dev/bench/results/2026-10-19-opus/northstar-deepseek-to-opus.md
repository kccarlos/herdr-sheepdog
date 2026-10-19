# North star: A vs B

- A: docs/dev/bench/results/2026-10-19-opus/deepseek/merged (18 runs, 18 graded)
- B: docs/dev/bench/results/2026-10-19-opus/opus-medium/merged (18 runs, 18 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 5 / 18 (28 %, CI 12–51 %) | 5 / 18 (28 %, CI 12–51 %) | +0 pp | -28 to +28 pp |
| Done | 18 / 18 (100 %, CI 82–100 %) | 18 / 18 (100 %, CI 82–100 %) | +0 pp | -18 to +18 pp |
| Done, claim part (judge) | 17 / 18 (94 %, CI 74–99 %) | 18 / 18 (100 %, CI 82–100 %) | +6 pp | -13 to +26 pp |
| Done, claim part (phrases) | 11 / 17 (65 %, CI 41–83 %) | 10 / 18 (56 %, CI 34–75 %) | -9 pp | -37 to +22 pp |
| Low effort | 10 / 18 (56 %, CI 34–75 %) | 12 / 18 (67 %, CI 44–84 %) | +11 pp | -19 to +39 pp |
| Safe | 13 / 18 (72 %, CI 49–88 %) | 16 / 18 (89 %, CI 67–97 %) | +17 pp | -10 to +41 pp |
| Understood (segments, cluster bootstrap) | 198 / 216 (92 %, CI 89–95 %) | 199 / 216 (92 %, CI 87–96 %) | +0 pp | -5 to +6 pp |
| Turns to goal | 12.8 (95 % CI 12.1–13.7, n 13) | 12.7 (95 % CI 12.1–13.3, n 15) | -0.10 | -1.19 to +0.86 |
| Time to goal | 93.8 s (95 % CI 82.4–108.3, n 13) | 169.7 s (95 % CI 146.5–194.5, n 15) | +75.98 | +48.40 to +105.80 |
| Unmatched claims (heard) | 8 / 75 (11 %, CI 6–20 %) | 16 / 127 (13 %, CI 8–19 %) | +2 pp | -8 to +11 pp |
| Actions never told | 3 / 48 (6 %, CI 2–17 %) | 2 / 47 (4 %, CI 1–14 %) | -2 pp | -13 to +9 pp |
| Missed news | 20 / 127 (16 %, CI 10–23 %) | 19 / 127 (15 %, CI 10–22 %) | -1 pp | -10 to +8 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
