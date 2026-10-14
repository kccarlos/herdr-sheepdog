# Simulated-user campaign

Started 2026-10-14T07:10:51Z, finished 2026-10-14T07:13:02Z (2m11s). Repeat 3.

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
| **Done, claim part (phrases)** | 1 / 6 | 17 % | 3–56 % | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 18 / 18 | 100 % | 82–100 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 18 / 18 | 100 % | 82–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 3.7 (95 % CI 3.1–4.2, n 18). Time to goal: 26.6 s (95 % CI 19.9–32.8, n 18).

- Done: 18 runs had nothing to get done (no required action, no end-state check and no action) and are left out.
- Missed news: explain-en-invoice-job r3: api-claude result at t+25s.

### Programmatic checks (diagnostics)

- **Said vs done, orchestrator**: 10 claims — 2 matched in their turn, 0 matched by an earlier action, unmatched 8 / 10 (80 %, CI 49–94 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
  - explain-en-invoice-job r1: said "the first hundred went out and the remaining four thousand one hundred were refused" (declined) — no declined action in the turn or before
  - explain-zh-input-rules r2: said "麻烦在于整句话里只要出现「好的继续」，就直接批准了，后面那半句「先别动数据库」会被吞掉" (approved) — no approved action in the turn or before
  - explain-zh-input-rules r2: said "先跑测试」，结果直接批准了，先跑测试那句压根没送到模型那里" (approved) — no approved action in the turn or before
  - explain-en-invoice-job r2: said "api-claude found the nightly invoice job failed because it sent the invoices one at a time" (sent) — no sent action in the turn or before
  - explain-zh-input-rules r3: said "先跑测试」，系统只当你说同意，就把命令放行了，那句「先跑测试」它没看见，也没提醒你" (approved) — no approved action in the turn or before
  - explain-zh-input-rules r3: said "系统一眼看到前面的「好的继续」，立刻把那一步放行了，你说的「先跑测试」它根本没看，也没回头问你一句" (approved) — no approved action in the turn or before
  - explain-en-invoice-job r3: said "almost every call after the first hundred was refused" (declined) — no declined action in the turn or before
  - explain-en-invoice-job r3: said "everything after the first hundred was refused" (declined) — no declined action in the turn or before
- **Missed news**: 1 items (finished, blocked, result, spawn failed); missed 1 / 1 (100 %, CI 21–100 %); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 66 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 3.7 (95 % CI 3.1–4.2, n 18); estimated cost per run 0.0178 $ (95 % CI 0.0138–0.0217, n 18); per-turn latency p50 6174 ms, p90 9959 ms (n 66).

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| explain-zh-input-rules | 3 | 3 | 0 | 0 | 0 | 100% | – | 45.5 | 0.085 |
| explain-en-flaky-test | 3 | 3 | 0 | 0 | 0 | 100% | – | 39.4 | 0.074 |
| explain-zh-notes-ledger | 3 | 3 | 0 | 0 | 0 | 100% | – | 30.4 | 0.053 |
| explain-en-invoice-job | 3 | 3 | 0 | 0 | 0 | 100% | – | 34.2 | 0.066 |
| explain-control-en-done | 3 | 3 | 0 | 0 | 0 | 100% | – | 9.0 | 0.020 |
| explain-control-zh-busy | 3 | 3 | 0 | 0 | 0 | 100% | – | 10.4 | 0.023 |
| **TOTAL** | 18 | 18 | 0 | 0 | 0 | 100% | – | 28.1 | 0.321 |

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

Explain (18 runs): orchestrator coverage 1.00, comprehension 1.00, words per answer en 175 / zh 228.

| Side | Coverage | Of orchestrator | Comprehension | Words/answer en | zh | Simple en | zh | Follow-up depth | No padding |
|---|---|---|---|---|---|---|---|---|---|
| orchestrator | 1.00 | – | 1.00 | 175 | 228 | 48 | 43 | 42/42 | 12/12 |

Coverage: share of the scenario's key points stated anywhere in the call. Of orchestrator: of the points the orchestrator gave the voice, the share the voice said. Comprehension: share of what / why / what-can-be-done a listener who cannot see the screen could restate. Words count Han characters as words. 0 of 66 questions (0 of 42 follow-ups) were answered without asking the orchestrator.

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | deepseek-flash | 113 | 919226 | 45808 | 29473 | 0.321 |
| **total** | | | | | | **0.321** |
