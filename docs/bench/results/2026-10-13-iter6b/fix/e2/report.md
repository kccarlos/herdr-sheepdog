# Simulated-user campaign

Started 2026-10-13T22:56:36Z, finished 2026-10-13T22:57:55Z (1m19s). Repeat 2.

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

12 runs, 12 graded (12 with a full record, 12 with the event log). Each rate is the share of the graded runs it applies to, with a Wilson 95 % CI; means have a bootstrap 95 % CI (2000 resamples). Everything after this block is diagnostics.

| North star | Pass / runs | Rate | 95 % CI | A run passes when |
|---|---|---|---|---|
| **Understood** | 12 / 12 | 100 % | 76–100 % | the judge's `answered` is yes and every depth or length item is yes |
| **Done** | – / 0 | n/a | – | no required action missing or sent to the wrong agent, and every declared end-state check passes (the claim part below is not in the gate) |
| **Done, claim part (judge)** | – / 0 | n/a | – | no segment's `said_vs_done` judge item is no (runs with a claim the judge looked at); not gated until calibrated |
| **Done, claim part (phrases)** | 2 / 8 | 25 % | 7–59 % | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 12 / 12 | 100 % | 76–100 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 12 / 12 | 100 % | 76–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 3.7 (95 % CI 2.9–4.3, n 12). Time to goal: 24.1 s (95 % CI 16.8–31.1, n 12).

- Done: 12 runs had nothing to get done (no required action, no end-state check and no action) and are left out.

### Programmatic checks (diagnostics)

- **Said vs done, orchestrator**: 11 claims — 2 matched in their turn, 0 matched by an earlier action, unmatched 9 / 11 (82 %, CI 52–95 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
  - explain-zh-input-rules r1: said "先跑测试\"，代码只把\"好的继续\"当成同意发出去了，后半句\"先跑测试\"压根没送到模型面前，所以那个测试一直没跑" (sent) — no sent action in the turn or before
  - explain-en-flaky-test r1: said "I have asked for the reason from the code itself, since Codex only told us what the race is, not why it is possible" (sent) — no sent action in the turn or before
  - explain-en-invoice-job r1: said "almost all of them were refused" (declined) — no declined action in the turn or before
  - explain-en-invoice-job r1: said "Look at last night's run: the first hundred invoices went out fine, then every call after that was refused" (declined) — no declined action in the turn or before
  - explain-zh-input-rules r2: said "先别动数据库」这样的整句，只对上前面半句就批准了，后半句的叮嘱没人看见" (approved) — no approved action in the turn or before
  - explain-zh-input-rules r2: said "所以你会有那种感觉：明明交代了，它却像没听见" (sent) — no sent action in the turn or before
  - explain-en-flaky-test r2: said "Nothing has happened since I told you" (sent) — no sent action in the turn or before
  - explain-en-invoice-job r2: said "everything past the first hundred was refused" (declined) — no declined action in the turn or before
  - explain-en-invoice-job r2: said "because the job never waits and tries again, every bill after that was refused too" (declined) — no declined action in the turn or before
- **Missed news**: 1 items (finished, blocked, result, spawn failed); missed 0 / 1 (0 %, CI 0–79 %); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 44 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 3.7 (95 % CI 2.9–4.3, n 12); estimated cost per run 0.0165 $ (95 % CI 0.0122–0.0207, n 12); per-turn latency p50 4730 ms, p90 9535 ms (n 44).

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| explain-zh-input-rules | 2 | 2 | 0 | 0 | 0 | 100% | – | 40.2 | 0.052 |
| explain-en-flaky-test | 2 | 2 | 0 | 0 | 0 | 100% | – | 34.8 | 0.045 |
| explain-zh-notes-ledger | 2 | 2 | 0 | 0 | 0 | 100% | – | 32.1 | 0.040 |
| explain-en-invoice-job | 2 | 2 | 0 | 0 | 0 | 100% | – | 28.8 | 0.034 |
| explain-control-en-done | 2 | 2 | 0 | 0 | 0 | 100% | – | 9.3 | 0.014 |
| explain-control-zh-busy | 2 | 2 | 0 | 0 | 0 | 100% | – | 8.1 | 0.013 |
| **TOTAL** | 12 | 12 | 0 | 0 | 0 | 100% | – | 25.6 | 0.198 |

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

Explain (12 runs): orchestrator coverage 1.00, comprehension 1.00, words per answer en 166 / zh 228.

| Side | Coverage | Of orchestrator | Comprehension | Words/answer en | zh | Simple en | zh | Follow-up depth | No padding |
|---|---|---|---|---|---|---|---|---|---|
| orchestrator | 1.00 | – | 1.00 | 166 | 228 | 37 | 56 | 28/28 | 8/8 |

Coverage: share of the scenario's key points stated anywhere in the call. Of orchestrator: of the points the orchestrator gave the voice, the share the voice said. Comprehension: share of what / why / what-can-be-done a listener who cannot see the screen could restate. Words count Han characters as words. 0 of 44 questions (0 of 28 follow-ups) were answered without asking the orchestrator.

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | deepseek-flash | 71 | 566084 | 28977 | 18564 | 0.198 |
| **total** | | | | | | **0.198** |
