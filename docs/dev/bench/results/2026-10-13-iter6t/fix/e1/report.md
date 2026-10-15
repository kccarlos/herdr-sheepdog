# Simulated-user campaign

Started 2026-10-14T02:37:32Z, finished 2026-10-14T02:39:38Z (2m6s). Repeat 3.

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
| **Done, claim part (phrases)** | 2 / 8 | 25 % | 7–59 % | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 18 / 18 | 100 % | 82–100 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 18 / 18 | 100 % | 82–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 3.7 (95 % CI 3.1–4.2, n 18). Time to goal: 25.6 s (95 % CI 18.8–32.0, n 18).

- Done: 18 runs had nothing to get done (no required action, no end-state check and no action) and are left out.

### Programmatic checks (diagnostics)

- **Said vs done, orchestrator**: 12 claims — 2 matched in their turn, 0 matched by an earlier action, unmatched 10 / 12 (83 %, CI 55–95 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
  - explain-zh-input-rules r1: said "先别动数据库」，它看到前半句就放行了，后半句没人听见" (approved) — no approved action in the turn or before
  - explain-en-invoice-job r1: said "Then every call after that was refused" (declined) — no declined action in the turn or before
  - explain-en-invoice-job r1: said "the first hundred went through and every one of the remaining four thousand one hundred was refused" (declined) — no declined action in the turn or before
  - explain-zh-input-rules r2: said "先别动数据库」，它只看前半句像同意，就直接批准了，后半句那个「别动数据库」被丢掉" (approved) — no approved action in the turn or before
  - explain-en-invoice-job r2: said "after the first hundred every call was rejected as too many requests" (declined) — no declined action in the turn or before
  - explain-zh-input-rules r3: said "先别动数据库\"，它只看到前半句就放行了，后半句\"先别动数据库\"就丢了" (approved) — no approved action in the turn or before
  - explain-zh-input-rules r3: said "先别动数据库\"，它看到前面的\"好的\"就放行了，后面那句嘱咐根本没人读" (approved) — no approved action in the turn or before
  - explain-zh-input-rules r3: said "那张表只看到前面的\"好的继续\"，四十毫秒就把同意发出去了" (sent) — no sent action in the turn or before
  - explain-en-invoice-job r3: said "after the first batch every request was refused and almost all of last night's invoices never went out" (declined) — no declined action in the turn or before
  - explain-en-invoice-job r3: said "the job hit the limit on its first batch and everything after it was refused" (declined) — no declined action in the turn or before
- **Missed news**: 1 items (finished, blocked, result, spawn failed); missed 0 / 1 (0 %, CI 0–79 %); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 66 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 3.7 (95 % CI 3.1–4.2, n 18); estimated cost per run 0.0171 $ (95 % CI 0.0131–0.0210, n 18); per-turn latency p50 5765 ms, p90 9256 ms (n 66).

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| explain-zh-input-rules | 3 | 3 | 0 | 0 | 0 | 100% | – | 48.2 | 0.087 |
| explain-en-flaky-test | 3 | 3 | 0 | 0 | 0 | 100% | – | 39.0 | 0.079 |
| explain-zh-notes-ledger | 3 | 3 | 0 | 0 | 0 | 100% | – | 30.1 | 0.053 |
| explain-en-invoice-job | 3 | 3 | 0 | 0 | 0 | 100% | – | 27.4 | 0.049 |
| explain-control-en-done | 3 | 3 | 0 | 0 | 0 | 100% | – | 8.7 | 0.020 |
| explain-control-zh-busy | 3 | 3 | 0 | 0 | 0 | 100% | – | 9.2 | 0.020 |
| **TOTAL** | 18 | 18 | 0 | 0 | 0 | 100% | – | 27.1 | 0.309 |

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

Explain (18 runs): orchestrator coverage 1.00, comprehension 1.00, words per answer en 173 / zh 234.

| Side | Coverage | Of orchestrator | Comprehension | Words/answer en | zh | Simple en | zh | Follow-up depth | No padding |
|---|---|---|---|---|---|---|---|---|---|
| orchestrator | 1.00 | – | 1.00 | 173 | 234 | 37 | 56 | 42/42 | 12/12 |

Coverage: share of the scenario's key points stated anywhere in the call. Of orchestrator: of the points the orchestrator gave the voice, the share the voice said. Comprehension: share of what / why / what-can-be-done a listener who cannot see the screen could restate. Words count Han characters as words. 0 of 66 questions (0 of 42 follow-ups) were answered without asking the orchestrator.

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | deepseek-flash | 109 | 882021 | 44879 | 28441 | 0.309 |
| **total** | | | | | | **0.309** |
