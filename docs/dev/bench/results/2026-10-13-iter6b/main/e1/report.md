# Simulated-user campaign

Started 2026-10-13T22:52:14Z, finished 2026-10-13T22:54:03Z (1m49s). Repeat 3.

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

18 runs, 18 graded (18 with a full record, 18 with the event log). Each rate is the share of the graded runs it applies to, with a Wilson 95 % CI; means have a bootstrap 95 % CI (2000 resamples). Everything after this block is diagnostics.

| North star | Pass / runs | Rate | 95 % CI | A run passes when |
|---|---|---|---|---|
| **Understood** | 18 / 18 | 100 % | 82–100 % | the judge's `answered` is yes and every depth or length item is yes |
| **Done** | – / 0 | n/a | – | no required action missing or sent to the wrong agent, and every declared end-state check passes (the claim part below is not in the gate) |
| **Done, claim part (judge)** | – / 0 | n/a | – | no segment's `said_vs_done` judge item is no (runs with a claim the judge looked at); not gated until calibrated |
| **Done, claim part (phrases)** | 0 / 7 | 0 % | 0–35 % | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 18 / 18 | 100 % | 82–100 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 18 / 18 | 100 % | 82–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 3.7 (95 % CI 3.1–4.2, n 18). Time to goal: 22.2 s (95 % CI 16.4–27.5, n 18).

- Done: 18 runs had nothing to get done (no required action, no end-state check and no action) and are left out.

### Programmatic checks (diagnostics)

- **Said vs done, orchestrator**: 13 claims — 1 matched in their turn, 0 matched by an earlier action, unmatched 12 / 13 (92 %, CI 67–99 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
  - explain-en-invoice-job r1: said "after the first hundred every request was refused" (declined) — no declined action in the turn or before
  - explain-en-invoice-job r1: said "the job hit the limit and every request after the first hundred was refused" (declined) — no declined action in the turn or before
  - explain-control-en-done r1: said "Nothing has changed since I just told you" (sent) — no sent action in the turn or before
  - explain-zh-input-rules r2: said "先别动数据库」，它只看到前半截就批准了，后面那句「先别动数据库」等于没说" (approved) — no approved action in the turn or before
  - explain-zh-input-rules r2: said "先别动数据库」，它只看到「同意」两个字就放行了，你后面那句「先别动」被丢掉" (approved) — no approved action in the turn or before
  - explain-en-invoice-job r2: said "most were refused as too many requests" (declined) — no declined action in the turn or before
  - explain-en-invoice-job r2: said "On the fifth it sent the first hundred fine, then invoice number one hundred and one came back refused for being sent to…" (sent) — no sent action in the turn or before
  - explain-en-invoice-job r2: said "the first hundred went out and every one after that was refused" (declined) — no declined action in the turn or before
  - explain-zh-input-rules r3: said "先跑测试」，程序听到「好的继续」就秒同意了，后面「先跑测试」这个要求根本没被读进去" (approved) — no approved action in the turn or before
  - explain-en-invoice-job r3: said "after the first hundred every one was refused" (declined) — no declined action in the turn or before
- **Missed news**: 0 items (finished, blocked, result, spawn failed); missed – / 0 (n/a, CI –); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 66 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 3.7 (95 % CI 3.1–4.2, n 18); estimated cost per run 0.0143 $ (95 % CI 0.0114–0.0169, n 18); per-turn latency p50 4163 ms, p90 8706 ms (n 66).

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| explain-zh-input-rules | 3 | 3 | 0 | 0 | 0 | 100% | – | 40.1 | 0.064 |
| explain-en-flaky-test | 3 | 3 | 0 | 0 | 0 | 100% | – | 32.9 | 0.061 |
| explain-zh-notes-ledger | 3 | 3 | 0 | 0 | 0 | 100% | – | 23.8 | 0.039 |
| explain-en-invoice-job | 3 | 3 | 0 | 0 | 0 | 100% | – | 29.2 | 0.053 |
| explain-control-en-done | 3 | 3 | 0 | 0 | 0 | 100% | – | 8.1 | 0.020 |
| explain-control-zh-busy | 3 | 3 | 0 | 0 | 0 | 100% | – | 8.3 | 0.020 |
| **TOTAL** | 18 | 18 | 0 | 0 | 0 | 100% | – | 23.7 | 0.257 |

## Brain failover

Failovers = brain.failover events (a step retried on another provider); answered by = the provider of each run's last answered step.

| Scenario | Runs | Runs with failover | Failovers | Answered by |
|---|---|---|---|---|
| explain-zh-input-rules | 3 | 0 | 0 | deepseek ×3 |
| explain-en-flaky-test | 3 | 0 | 0 | deepseek ×3 |
| explain-zh-notes-ledger | 3 | 0 | 0 | deepseek ×3 |
| explain-en-invoice-job | 3 | 0 | 0 | deepseek ×3 |
| explain-control-en-done | 3 | 0 | 0 | deepseek ×3 |
| explain-control-zh-busy | 3 | 0 | 0 | deepseek ×3 |
| **TOTAL** | 18 | 0 | 0 | deepseek ×18 |

## Failure taxonomy

| Label | Runs (primary) | Runs (any) |
|---|---|---|

## Judge items

| Item | Yes | Asked | Yes rate |
|---|---|---|---|
| answered | 18 | 18 | 100% |
| no_unintended_action | 18 | 18 | 100% |
| right_agent | 18 | 18 | 100% |
| no_hallucination | 18 | 18 | 100% |
| facts_current | 18 | 18 | 100% |
| length_fits | 18 | 18 | 100% |
| ear_friendly | 18 | 18 | 100% |
| language | 18 | 18 | 100% |

## Explanations

Explain (18 runs): orchestrator coverage 1.00, comprehension 1.00, words per answer en 163 / zh 218.

| Side | Coverage | Of orchestrator | Comprehension | Words/answer en | zh | Simple en | zh | Follow-up depth | No padding |
|---|---|---|---|---|---|---|---|---|---|
| orchestrator | 1.00 | – | 1.00 | 163 | 218 | 41 | 57 | 42/42 | 12/12 |

Coverage: share of the scenario's key points stated anywhere in the call. Of orchestrator: of the points the orchestrator gave the voice, the share the voice said. Comprehension: share of what / why / what-can-be-done a listener who cannot see the screen could restate. Words count Han characters as words. 0 of 66 questions (0 of 42 follow-ups) were answered without asking the orchestrator.

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | deepseek-flash | 95 | 730500 | 38815 | 24201 | 0.257 |
| **total** | | | | | | **0.257** |
