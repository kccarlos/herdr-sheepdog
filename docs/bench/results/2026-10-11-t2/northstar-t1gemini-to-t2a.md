# North star: A vs B

- A: docs/bench/results/2026-10-11-t1-gemini/merged (30 runs, 30 graded)
- B: docs/bench/results/2026-10-11-t2/a/merged (30 runs, 30 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 8 / 30 (27 %, CI 14–44 %) | 8 / 30 (27 %, CI 14–44 %) | +0 pp | -22 to +22 pp |
| Done | 30 / 30 (100 %, CI 89–100 %) | 12 / 12 (100 %, CI 76–100 %) | +0 pp | -24 to +11 pp |
| Done, claim part (judge) | 27 / 30 (90 %, CI 74–97 %) | 9 / 12 (75 %, CI 47–91 %) | -15 pp | -44 to +7 pp |
| Done, claim part (phrases) | 23 / 30 (77 %, CI 59–88 %) | 9 / 15 (60 %, CI 36–80 %) | -17 pp | -44 to +10 pp |
| Low effort | 13 / 30 (43 %, CI 27–61 %) | 13 / 30 (43 %, CI 27–61 %) | +0 pp | -24 to +24 pp |
| Safe | 24 / 30 (80 %, CI 63–90 %) | 24 / 30 (80 %, CI 63–90 %) | +0 pp | -20 to +20 pp |
| Understood (segments, cluster bootstrap) | 290 / 359 (81 %, CI 77–85 %) | 108 / 143 (76 %, CI 64–86 %) | -5 pp | -17 to +5 pp |
| Turns to goal | 12.4 (95 % CI 12.0–12.8, n 19) | 7.8 (95 % CI 5.6–10.1, n 16) | -4.61 | -7.08 to -2.35 |
| Time to goal | 91.1 s (95 % CI 83.6–99.3, n 19) | 217.6 s (95 % CI 140.2–299.6, n 16) | +126.48 | +39.91 to +212.76 |
| Unmatched claims (heard) | 12 / 114 (11 %, CI 6–18 %) | 7 / 41 (17 %, CI 9–31 %) | +7 pp | -4 to +21 pp |
| Actions never told | 5 / 72 (7 %, CI 3–15 %) | 6 / 28 (21 %, CI 10–40 %) | +14 pp | +1 to +33 pp |
| Missed news | 32 / 212 (15 %, CI 11–21 %) | 29 / 84 (35 %, CI 25–45 %) | +19 pp | +9 to +31 pp |

- The arms ran different scenarios (only in A: []; only in B: [explain-control-en-done explain-control-zh-busy explain-en-flaky-test explain-en-invoice-job explain-zh-input-rules explain-zh-notes-ledger]): a difference may come from the scenario mix.

- Arm A is tier 1 and arm B tier 2: the difference includes the tier's.

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
