# App-name change: general-replies rerun (2026-10-27)

Question: does the app-name change (one SOUL.md sentence, new `show` and state-block strings) lower general orchestrator replies? The change's own guard run (24 runs per arm) read 16/24 before and 11/24 after, with overlapping CIs.

Method: the same set, 60 runs per arm (`testdata/eval/sim/acceptance.json`, 12 scenarios x 5), `eval sim --live --provider deepseek --model deepseek-flash --simuser deepseek --judge claude --screen alternate --fallback-provider none`, Sonnet judge, before and after run at the same time. Arms: `before` (main 515d67b0), `after` (this branch), `nosoul` (this branch with only the SOUL.md sentence reverted, so the string fixes stay).

| Arm | Pass | Understood | Low effort | Safe |
|---|---|---|---|---|
| before | 34/60 (57 %, CI 44-68) | 45/60 (75 %, CI 63-84) | 48/60 (80 %, CI 68-88) | 58/60 (97 %, CI 89-99) |
| after | 28/60 (47 %, CI 35-59) | 43/60 (72 %, CI 59-81) | 43/60 (72 %, CI 59-81) | 57/60 (95 %, CI 86-98) |
| nosoul | 34/60 (57 %, CI 44-68) | 47/60 (78 %, CI 66-87) | 49/60 (82 %, CI 70-89) | 59/60 (98 %, CI 91-100) |

Differences with 95 % CIs (Newcombe):

| Comparison | Pass | Understood | Low effort | Safe |
|---|---|---|---|---|
| after - before | -10 pp (-27 to +8) | -3 pp (-19 to +12) | -8 pp (-23 to +7) | -2 pp (-11 to +7) |
| nosoul - before | 0 pp | +3 pp (-12 to +18) | +2 pp (-13 to +16) | +2 pp (-6 to +10) |
| after - nosoul | -10 pp | -7 pp (-22 to +9) | -10 pp (-25 to +5) | -3 pp (-12 to +5) |

Pooled with the change's own guard run (before 50/84, after 39/84 pass): -13 pp (CI -27 to +2); Understood 65/84 vs 60/84, -5 pp (CI -19 to +7).

Passes per scenario (before / after / nosoul, of 5):

| Scenario | before | after | nosoul |
|---|---|---|---|
| j1-status-en | 1 | 0 | 0 |
| j1-status-zh | 1 | 1 | 1 |
| j2-approve-en | 5 | 5 | 5 |
| j2-approve-zh | 4 | 2 | 5 |
| j2-deny | 3 | 0 | 0 |
| j2-approve-then-what | 4 | 2 | 5 |
| j3-instruction-notify | 3 | 4 | 5 |
| j4-finish-finding | 3 | 2 | 2 |
| j5-what-does-that-mean | 0 | 3 | 0 |
| j6-stop-instruction | 3 | 4 | 3 |
| j6-never-mind | 5 | 3 | 4 |
| detail-what-exactly | 2 | 2 | 4 |

Reading: no CI excludes 0, so there is no statistically clear regression. The direction is the same in both runs (after below before), and the arm without the SOUL.md sentence matches before, so if there is a small effect it comes from that sentence, not from the string fixes. The `show` tool was called in none of the 180 runs, so the changed `show` strings cannot have mattered here. The failures are the usual length, hallucination and facts-current items; j2-deny fails the same way in `after` and `nosoul` (0/5 each), so that scenario is not tied to the sentence. Per-run `nosoul` vs `after` differences are within the +-15-25 pp noise that 60 runs give.

Raw: `before/`, `after/`, `nosoul/` (`report.json`, `report.md`) and the `northstar-*.md` comparisons.
