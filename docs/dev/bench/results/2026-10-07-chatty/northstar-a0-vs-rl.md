# North star: A vs B

- A: docs/bench/results/2026-10-07-chatty/a0 (35 runs, 35 graded)
- B: docs/bench/results/2026-10-07-chatty/rl (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 35 / 35 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 22 / 35 (63 %, CI 46–77 %) | 27 / 35 (77 %, CI 61–88 %) | +14 pp | -7 to +34 pp |
| Low effort | 26 / 35 (74 %, CI 58–86 %) | 29 / 35 (83 %, CI 67–92 %) | +9 pp | -11 to +27 pp |
| Safe | 27 / 35 (77 %, CI 61–88 %) | 24 / 35 (69 %, CI 52–81 %) | -9 pp | -28 to +12 pp |
| Understood (segments, cluster bootstrap) | 387 / 421 (92 %, CI 89–95 %) | 370 / 421 (88 %, CI 84–91 %) | -4 pp | -8 to +0 pp |
| Turns to goal | 12.5 (95 % CI 12.1–12.9, n 35) | 12.5 (95 % CI 12.1–12.8, n 35) | -0.03 | -0.49 to +0.43 |
| Time to goal | 108.7 s (95 % CI 96.6–125.1, n 35) | 113.4 s (95 % CI 103.0–124.8, n 35) | +4.70 | -14.40 to +20.97 |
| Unmatched claims (heard) | 19 / 145 (13 %, CI 9–20 %) | 13 / 141 (9 %, CI 5–15 %) | -4 pp | -11 to +4 pp |
| Actions never told | 9 / 90 (10 %, CI 5–18 %) | 12 / 91 (13 %, CI 8–22 %) | +3 pp | -6 to +13 pp |
| Missed news | 45 / 240 (19 %, CI 14–24 %) | 43 / 234 (18 %, CI 14–24 %) | -0 pp | -7 to +7 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
