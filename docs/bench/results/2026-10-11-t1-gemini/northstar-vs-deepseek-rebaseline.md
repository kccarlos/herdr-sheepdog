# North star: A vs B

- A: docs/bench/results/2026-10-11-t1-gemini/merged (30 runs, 30 graded)
- B: docs/bench/results/2026-10-08-rebaseline/merged (35 runs, 34 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 8 / 30 (27 %, CI 14–44 %) | 34 / 34 (100 %, CI 90–100 %) | +73 pp | +53 to +86 pp |
| Done | 30 / 30 (100 %, CI 89–100 %) | 34 / 34 (100 %, CI 90–100 %) | +0 pp | -10 to +11 pp |
| Done, claim part (judge) | 27 / 30 (90 %, CI 74–97 %) | 33 / 34 (97 %, CI 85–99 %) | +7 pp | -7 to +23 pp |
| Done, claim part (phrases) | 23 / 30 (77 %, CI 59–88 %) | 22 / 32 (69 %, CI 51–82 %) | -8 pp | -29 to +14 pp |
| Low effort | 13 / 30 (43 %, CI 27–61 %) | 26 / 34 (76 %, CI 60–88 %) | +33 pp | +9 to +53 pp |
| Safe | 24 / 30 (80 %, CI 63–90 %) | 28 / 34 (82 %, CI 66–92 %) | +2 pp | -17 to +22 pp |
| Understood (segments, cluster bootstrap) | 290 / 359 (81 %, CI 77–85 %) | 365 / 402 (91 %, CI 87–94 %) | +10 pp | +5 to +15 pp |
| Turns to goal | 12.4 (95 % CI 12.0–12.8, n 19) | 12.4 (95 % CI 12.1–12.8, n 34) | +0.02 | -0.48 to +0.53 |
| Time to goal | 91.1 s (95 % CI 83.6–99.3, n 19) | 102.0 s (95 % CI 96.0–108.2, n 34) | +10.92 | +0.82 to +20.36 |
| Unmatched claims (heard) | 12 / 114 (11 %, CI 6–18 %) | 12 / 144 (8 %, CI 5–14 %) | -2 pp | -10 to +5 pp |
| Actions never told | 5 / 72 (7 %, CI 3–15 %) | 5 / 84 (6 %, CI 3–13 %) | -1 pp | -10 to +7 pp |
| Missed news | 32 / 212 (15 %, CI 11–21 %) | 40 / 222 (18 %, CI 14–24 %) | +3 pp | -4 to +10 pp |

- The arms ran different scenarios (only in A: []; only in B: [workday-adversarial-afternoon]): a difference may come from the scenario mix.

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
