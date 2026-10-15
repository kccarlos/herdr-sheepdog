# Simulated-user campaign

Started 2026-10-13T19:41:39Z, finished 2026-10-13T19:42:48Z (1m9s). Repeat 2.

- brain: deepseek deepseek-flash
- budget_usd: 1.5
- failover providers: none
- judge: fake fake
- mode: live
- parallel: 4
- personas: rotate
- scenarios: testdata/eval/sim/explain.json (filter explain)
- simulated user: deepseek deepseek-flash
- utterance_timeout: 25s (0s = the orchestrator default, 25s)

## North star

12 runs, 12 graded (12 with a full record, 12 with the event log). Each rate is the share of the graded runs it applies to, with a Wilson 95 % CI; means have a bootstrap 95 % CI (2000 resamples). Everything after this block is diagnostics.

| North star | Pass / runs | Rate | 95 % CI | A run passes when |
|---|---|---|---|---|
| **Understood** | 12 / 12 | 100 % | 76–100 % | the judge's `answered` is yes and every depth or length item is yes |
| **Done** | – / 0 | n/a | – | no required action missing or sent to the wrong agent, and every declared end-state check passes (the claim part below is not in the gate) |
| **Done, claim part (judge)** | – / 0 | n/a | – | no segment's `said_vs_done` judge item is no (runs with a claim the judge looked at); not gated until calibrated |
| **Done, claim part (phrases)** | 0 / 1 | 0 % | 0–79 % | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 12 / 12 | 100 % | 76–100 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 12 / 12 | 100 % | 76–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 3.7 (95 % CI 2.9–4.3, n 12). Time to goal: 20.5 s (95 % CI 14.6–26.2, n 12).

- Done: 12 runs had nothing to get done (no required action, no end-state check and no action) and are left out.

### Programmatic checks (diagnostics)

- **Said vs done, orchestrator**: 1 claims — 0 matched in their turn, 0 matched by an earlier action, unmatched 1 / 1 (100 %, CI 21–100 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
  - explain-control-en-done r1: said "nothing has changed since I just told you" (sent) — no sent action in the turn or before
- **Missed news**: 1 items (finished, blocked, result, spawn failed); missed – / 0 (n/a, CI –); 1 ungraded (the call ended before the 30 s window closed).
- **User effort**: 44 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 3.7 (95 % CI 2.9–4.3, n 12); estimated cost per run 0.0142 $ (95 % CI 0.0109–0.0174, n 12); per-turn latency p50 3978 ms, p90 8037 ms (n 44).

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| explain-zh-input-rules | 2 | 2 | 0 | 0 | 0 | 100% | – | 34.1 | 0.038 |
| explain-en-flaky-test | 2 | 2 | 0 | 0 | 0 | 100% | – | 31.9 | 0.042 |
| explain-zh-notes-ledger | 2 | 2 | 0 | 0 | 0 | 100% | – | 22.5 | 0.029 |
| explain-en-invoice-job | 2 | 2 | 0 | 0 | 0 | 100% | – | 27.1 | 0.036 |
| explain-control-en-done | 2 | 2 | 0 | 0 | 0 | 100% | – | 8.3 | 0.013 |
| explain-control-zh-busy | 2 | 2 | 0 | 0 | 0 | 100% | – | 8.3 | 0.013 |
| **TOTAL** | 12 | 12 | 0 | 0 | 0 | 100% | – | 22.1 | 0.171 |

## Brain failover

Failovers = brain.failover events (a step retried on another provider); answered by = the provider of each run's last answered step.

| Scenario | Runs | Runs with failover | Failovers | Answered by |
|---|---|---|---|---|
| explain-zh-input-rules | 2 | 0 | 0 | deepseek ×2 |
| explain-en-flaky-test | 2 | 0 | 0 | deepseek ×2 |
| explain-zh-notes-ledger | 2 | 0 | 0 | deepseek ×2 |
| explain-en-invoice-job | 2 | 0 | 0 | deepseek ×2 |
| explain-control-en-done | 2 | 0 | 0 | deepseek ×2 |
| explain-control-zh-busy | 2 | 0 | 0 | deepseek ×2 |
| **TOTAL** | 12 | 0 | 0 | deepseek ×12 |

## Failure taxonomy

| Label | Runs (primary) | Runs (any) |
|---|---|---|

## Judge items

| Item | Yes | Asked | Yes rate |
|---|---|---|---|
| answered | 12 | 12 | 100% |
| no_unintended_action | 12 | 12 | 100% |
| right_agent | 12 | 12 | 100% |
| no_hallucination | 12 | 12 | 100% |
| facts_current | 12 | 12 | 100% |
| length_fits | 12 | 12 | 100% |
| ear_friendly | 12 | 12 | 100% |
| language | 12 | 12 | 100% |

## Explanations

Explain (12 runs): orchestrator coverage 1.00, comprehension 1.00, words per answer en 149 / zh 212.

| Side | Coverage | Of orchestrator | Comprehension | Words/answer en | zh | Simple en | zh | Follow-up depth | No padding |
|---|---|---|---|---|---|---|---|---|---|
| orchestrator | 1.00 | – | 1.00 | 149 | 212 | 42 | 57 | 28/28 | 8/8 |

Coverage: share of the scenario's key points stated anywhere in the call. Of orchestrator: of the points the orchestrator gave the voice, the share the voice said. Comprehension: share of what / why / what-can-be-done a listener who cannot see the screen could restate. Words count Han characters as words. 0 of 44 questions (0 of 28 follow-ups) were answered without asking the orchestrator.

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | deepseek-flash | 65 | 486457 | 25431 | 15946 | 0.171 |
| **total** | | | | | | **0.171** |
