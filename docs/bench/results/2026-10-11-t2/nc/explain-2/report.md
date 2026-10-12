# Voice bench (tier 2), 2026-10-12 00:04Z

- brain: vertex gemini-3.5-flash (failover: vertex)
- budget_usd: 5
- judge: fake fake
- openings: pinned by testdata/bench/openings.json
- parallel: 2
- personas: each scenario's own
- ports: 7901,7902
- prompts_dir: /Users/kc/Documents/sheepdog-vx/docs/eval/prompt-arms/2026-10-11/nc: assistant.md override 2b654572, assistant-gptlive.md override e2650c28, USER.md override 84d5637c, svp/SOUL.md override 57a30419, svp/RULES.md override c46e1a30
- scenarios: testdata/bench/scenarios/explain.json (filter explain-zh-notes-ledger,explain-en-invoice-job,explain-control-zh-busy)
- simulated CEO: deepseek deepseek-flash
- tier: 2 (synthetic speech -> phone emulator -> scratch daemon: real voice model, real orchestrator, mock herdr scripted from the eval sim worlds)
- tts: gemini/gemini-2.5-flash-tts|say
- voice: gemini-3.8-live (backend vertex)

## North star

9 runs, 9 graded (9 with a full record, 9 with the event log). Each rate is the share of the graded runs it applies to, with a Wilson 95 % CI; means have a bootstrap 95 % CI (2000 resamples). Everything after this block is diagnostics.

| North star | Pass / runs | Rate | 95 % CI | A run passes when |
|---|---|---|---|---|
| **Understood** | 9 / 9 | 100 % | 70–100 % | the judge's `answered` is yes and every depth or length item is yes |
| **Done** | – / 0 | n/a | – | no required action missing or sent to the wrong agent, and every declared end-state check passes (the claim part below is not in the gate) |
| **Done, claim part (judge)** | – / 0 | n/a | – | no segment's `said_vs_done` judge item is no (runs with a claim the judge looked at); not gated until calibrated |
| **Done, claim part (phrases)** | – / 0 | n/a | – | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 9 / 9 | 100 % | 70–100 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 9 / 9 | 100 % | 70–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 3.3 (95 % CI 2.7–3.8, n 9). Time to goal: 65.3 s (95 % CI 48.8–80.1, n 9).

- Done: 9 runs had nothing to get done (no required action, no end-state check and no action) and are left out.

### Programmatic checks (diagnostics)

- **Said vs done, voice**: 0 claims — 0 matched in their turn, 0 matched by an earlier action, unmatched – / 0 (n/a, CI –). Actions on workers: 0; never told – / 0 (n/a, CI –).
- **Said vs done, orchestrator**: 2 claims — 1 matched in their turn, 0 matched by an earlier action, unmatched 1 / 2 (50 %, CI 9–91 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
  - explain-en-invoice-job r3: said "Last night, it sent the first hundred successfully" (sent) — no sent action in the turn or before
- **Missed news**: 0 items (finished, blocked, result, spawn failed); missed – / 0 (n/a, CI –); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 30 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 3.3 (95 % CI 2.7–3.8, n 9); estimated cost per run 0.0554 $ (95 % CI 0.0399–0.0715, n 9); per-turn latency p50 4792 ms, p90 8538 ms (n 17).

**9 runs (3 scenarios × 3): pass 9, fail 0, errored 0, not run 0 — pass rate 100%** (errored and not-run runs excluded). 6 min wall. Estimated spend $0.50 (brain $0.079, live $0.419, live audio 1.2 min in / 5.8 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| explain | 9 | 9 | 0 | 0 | 100% |  |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| explain-control-zh-busy | 3 | 3 | 0 | 0 | 100% |  | 44 | 0.089 |
| explain-en-invoice-job | 3 | 3 | 0 | 0 | 100% |  | 110 | 0.261 |
| explain-zh-notes-ledger | 3 | 3 | 0 | 0 | 100% |  | 93 | 0.148 |

## Failure taxonomy

Primary label per failed or errored run: none. Every label of every run: none.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 17 | 0 | 3 | 74 |
| relay | 17 | 0 | 0 | 0 |
| queue | 17 | 0 | 0 | 0 |
| brain | 17 | 4017 | 7806 | 8594 |
| model | 17 | 4012 | 7801 | 8586 |
| tool | 17 | 1 | 1 | 2 |
| delivery | 17 | 1 | 2 | 2 |
| first_audio | 17 | 712 | 864 | 938 |
| total | 17 | 4792 | 8538 | 9284 |

Outcomes: played 17.

## Voice layer

- user lines: 30, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.028, zh 0.126; by condition: clean 0.087.
- Daemon flushes 0, phone barge-in cuts 0, interrupted 0, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 0, held (no listener) 0. Orchestrator failovers 1.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 0.
- user lines rendered by: gemini/gemini-2.5-flash-tts 30.

## Explanations

Explain (9 runs): orchestrator coverage 1.00, comprehension 1.00, words per answer en 99 / zh 154; voice coverage 1.00 (1.00 of the orchestrator's points), comprehension 1.00, words per answer en 48 / zh 54; 12 of 18 follow-ups answered without the orchestrator.

| Side | Coverage | Of orchestrator | Comprehension | Words/answer en | zh | Simple en | zh | Follow-up depth | No padding |
|---|---|---|---|---|---|---|---|---|---|
| orchestrator | 1.00 | – | 1.00 | 99 | 154 | – | 54 | 6/6 | 5/5 |
| voice | 1.00 | 1.00 | 1.00 | 48 | 54 | – | 31 | 18/18 | 6/6 |

Coverage: share of the scenario's key points stated anywhere in the call. Of orchestrator: of the points the orchestrator gave the voice, the share the voice said. Comprehension: share of what / why / what-can-be-done a listener who cannot see the screen could restate. Words count Han characters as words. 13 of 30 questions (12 of 18 follow-ups) were answered without asking the orchestrator.

## Failed runs

None.
