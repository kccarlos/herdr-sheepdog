# Simulated-user campaign

Started 2026-10-13T22:47:31Z, finished 2026-10-13T22:48:20Z (49s). Repeat 1.

- brain: deepseek deepseek-flash
- budget_usd: 2
- failover providers: none
- judge: fake fake
- mode: live
- parallel: 4
- personas: rotate
- scenarios: testdata/eval/sim/explain.json (filter explain-)
- simulated user: deepseek deepseek-flash
- utterance_timeout: 25s (0s = the orchestrator default, 25s)

## North star

6 runs, 6 graded (6 with a full record, 6 with the event log). Each rate is the share of the graded runs it applies to, with a Wilson 95 % CI; means have a bootstrap 95 % CI (2000 resamples). Everything after this block is diagnostics.

| North star | Pass / runs | Rate | 95 % CI | A run passes when |
|---|---|---|---|---|
| **Understood** | 6 / 6 | 100 % | 61–100 % | the judge's `answered` is yes and every depth or length item is yes |
| **Done** | – / 0 | n/a | – | no required action missing or sent to the wrong agent, and every declared end-state check passes (the claim part below is not in the gate) |
| **Done, claim part (judge)** | – / 0 | n/a | – | no segment's `said_vs_done` judge item is no (runs with a claim the judge looked at); not gated until calibrated |
| **Done, claim part (phrases)** | 1 / 3 | 33 % | 6–79 % | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 6 / 6 | 100 % | 61–100 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 6 / 6 | 100 % | 61–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 3.7 (95 % CI 2.7–4.7, n 6). Time to goal: 26.7 s (95 % CI 17.9–36.0, n 6).

- Done: 6 runs had nothing to get done (no required action, no end-state check and no action) and are left out.

### Programmatic checks (diagnostics)

- **Said vs done, orchestrator**: 5 claims — 1 matched in their turn, 0 matched by an earlier action, unmatched 4 / 5 (80 %, CI 38–96 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
  - explain-zh-input-rules r1: said "先别动数据库」，它看到前半句就放行了，后半句那个条件你说了等于没说" (approved) — no approved action in the turn or before
  - explain-zh-input-rules r1: said "你觉得自己交代了「先别动数据库」，它那边却当你已经放行，照原样做下去" (approved) — no approved action in the turn or before
  - explain-zh-input-rules r1: said "你觉得自己交代了「先别动数据库」，它那边却当你已经放行，照原样做下去" (sent) — no sent action in the turn or before
  - explain-en-invoice-job r1: said "after the first hundred every ask was refused and 4,100 invoices went unsent" (declined) — no declined action in the turn or before
- **Missed news**: 0 items (finished, blocked, result, spawn failed); missed – / 0 (n/a, CI –); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 22 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 3.7 (95 % CI 2.7–4.7, n 6); estimated cost per run 0.0176 $ (95 % CI 0.0124–0.0228, n 6); per-turn latency p50 4898 ms, p90 11527 ms (n 22).

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| explain-zh-input-rules | 1 | 1 | 0 | 0 | 0 | 100% | – | 40.8 | 0.025 |
| explain-en-flaky-test | 1 | 1 | 0 | 0 | 0 | 100% | – | 36.4 | 0.023 |
| explain-zh-notes-ledger | 1 | 1 | 0 | 0 | 0 | 100% | – | 37.5 | 0.021 |
| explain-en-invoice-job | 1 | 1 | 0 | 0 | 0 | 100% | – | 30.1 | 0.020 |
| explain-control-en-done | 1 | 1 | 0 | 0 | 0 | 100% | – | 11.9 | 0.009 |
| explain-control-zh-busy | 1 | 1 | 0 | 0 | 0 | 100% | – | 12.7 | 0.007 |
| **TOTAL** | 6 | 6 | 0 | 0 | 0 | 100% | – | 28.2 | 0.106 |

## Brain failover

Failovers = brain.failover events (a step retried on another provider); answered by = the provider of each run's last answered step.

| Scenario | Runs | Runs with failover | Failovers | Answered by |
|---|---|---|---|---|
| explain-zh-input-rules | 1 | 0 | 0 | deepseek ×1 |
| explain-en-flaky-test | 1 | 0 | 0 | deepseek ×1 |
| explain-zh-notes-ledger | 1 | 0 | 0 | deepseek ×1 |
| explain-en-invoice-job | 1 | 0 | 0 | deepseek ×1 |
| explain-control-en-done | 1 | 0 | 0 | deepseek ×1 |
| explain-control-zh-busy | 1 | 0 | 0 | deepseek ×1 |
| **TOTAL** | 6 | 0 | 0 | deepseek ×6 |

## Failure taxonomy

| Label | Runs (primary) | Runs (any) |
|---|---|---|

## Judge items

| Item | Yes | Asked | Yes rate |
|---|---|---|---|
| answered | 6 | 6 | 100% |
| no_unintended_action | 6 | 6 | 100% |
| right_agent | 6 | 6 | 100% |
| no_hallucination | 6 | 6 | 100% |
| facts_current | 6 | 6 | 100% |
| length_fits | 6 | 6 | 100% |
| ear_friendly | 6 | 6 | 100% |
| language | 6 | 6 | 100% |

## Explanations

Explain (6 runs): orchestrator coverage 1.00, comprehension 1.00, words per answer en 148 / zh 196.

| Side | Coverage | Of orchestrator | Comprehension | Words/answer en | zh | Simple en | zh | Follow-up depth | No padding |
|---|---|---|---|---|---|---|---|---|---|
| orchestrator | 1.00 | – | 1.00 | 148 | 196 | 42 | 84 | 14/14 | 4/4 |

Coverage: share of the scenario's key points stated anywhere in the call. Of orchestrator: of the points the orchestrator gave the voice, the share the voice said. Comprehension: share of what / why / what-can-be-done a listener who cannot see the screen could restate. Words count Han characters as words. 0 of 22 questions (0 of 14 follow-ups) were answered without asking the orchestrator.

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | deepseek-flash | 37 | 298146 | 16040 | 10950 | 0.106 |
| **total** | | | | | | **0.106** |
