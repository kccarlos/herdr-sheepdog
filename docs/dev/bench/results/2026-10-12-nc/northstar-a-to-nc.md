# North star: A vs B

- A: docs/bench/results/2026-10-12-nc/a/merged (42 runs, 42 graded)
- B: docs/bench/results/2026-10-12-nc/nc/merged (42 runs, 42 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 8 / 42 (19 %, CI 10–33 %) | 6 / 42 (14 %, CI 7–28 %) | -5 pp | -21 to +12 pp |
| Done | 11 / 12 (92 %, CI 65–99 %) | 12 / 12 (100 %, CI 76–100 %) | +8 pp | -17 to +35 pp |
| Done, claim part (judge) | 9 / 11 (82 %, CI 52–95 %) | 11 / 11 (100 %, CI 74–100 %) | +18 pp | -11 to +48 pp |
| Done, claim part (phrases) | 13 / 18 (72 %, CI 49–88 %) | 12 / 22 (55 %, CI 35–73 %) | -18 pp | -43 to +12 pp |
| Low effort | 23 / 42 (55 %, CI 40–69 %) | 24 / 42 (57 %, CI 42–71 %) | +2 pp | -18 to +23 pp |
| Safe | 38 / 42 (90 %, CI 78–96 %) | 38 / 42 (90 %, CI 78–96 %) | +0 pp | -14 to +14 pp |
| Understood (segments, cluster bootstrap) | 88 / 134 (66 %, CI 59–73 %) | 95 / 134 (71 %, CI 64–78 %) | +5 pp | -5 to +15 pp |
| Turns to goal | 4.8 (95 % CI 3.6–6.3, n 25) | 5.7 (95 % CI 4.2–7.4, n 28) | +0.84 | -1.37 to +2.98 |
| Time to goal | 121.0 s (95 % CI 74.9–174.9, n 25) | 178.2 s (95 % CI 106.5–257.0, n 28) | +57.21 | -33.14 to +146.38 |
| Unmatched claims (heard) | 5 / 36 (14 %, CI 6–29 %) | 12 / 46 (26 %, CI 16–40 %) | +12 pp | -6 to +28 pp |
| Actions never told | 3 / 27 (11 %, CI 4–28 %) | 7 / 30 (23 %, CI 12–41 %) | +12 pp | -8 to +31 pp |
| Missed news | 25 / 80 (31 %, CI 22–42 %) | 22 / 81 (27 %, CI 19–38 %) | -4 pp | -18 to +10 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
