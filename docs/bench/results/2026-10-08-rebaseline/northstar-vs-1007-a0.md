# North star: A vs B

- A: docs/bench/results/2026-10-08-rebaseline/merged (35 runs, 34 graded)
- B: docs/bench/results/2026-10-07-chatty/a0 (35 runs, 35 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 34 / 34 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done | 34 / 34 (100 %, CI 90–100 %) | 35 / 35 (100 %, CI 90–100 %) | +0 pp | -10 to +10 pp |
| Done, claim part (judge) | 33 / 34 (97 %, CI 85–99 %) | – / 0 (n/a, CI –) | n/a | – |
| Done, claim part (phrases) | 22 / 32 (69 %, CI 51–82 %) | 19 / 32 (59 %, CI 42–74 %) | -9 pp | -31 to +14 pp |
| Low effort | 26 / 34 (76 %, CI 60–88 %) | 26 / 35 (74 %, CI 58–86 %) | -2 pp | -22 to +18 pp |
| Safe | 28 / 34 (82 %, CI 66–92 %) | 27 / 35 (77 %, CI 61–88 %) | -5 pp | -24 to +14 pp |
| Understood (segments, cluster bootstrap) | 365 / 402 (91 %, CI 87–94 %) | 387 / 421 (92 %, CI 89–95 %) | +1 pp | -3 to +6 pp |
| Turns to goal | 12.4 (95 % CI 12.1–12.8, n 34) | 12.5 (95 % CI 12.1–12.9, n 35) | +0.04 | -0.45 to +0.54 |
| Time to goal | 102.0 s (95 % CI 96.0–108.2, n 34) | 108.7 s (95 % CI 96.6–125.1, n 35) | +6.63 | -7.31 to +24.56 |
| Unmatched claims (heard) | 12 / 144 (8 %, CI 5–14 %) | 19 / 145 (13 %, CI 9–20 %) | +5 pp | -2 to +12 pp |
| Actions never told | 5 / 84 (6 %, CI 3–13 %) | 9 / 90 (10 %, CI 5–18 %) | +4 pp | -5 to +13 pp |
| Missed news | 40 / 222 (18 %, CI 14–24 %) | 45 / 240 (19 %, CI 14–24 %) | +1 pp | -6 to +8 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
