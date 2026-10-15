# Voice bench (tier 2), 2026-10-11 23:26Z

- brain: vertex gemini-3.5-flash (failover: vertex)
- budget_usd: 5
- judge: fake fake
- openings: pinned by testdata/bench/openings.json
- parallel: 2
- personas: each scenario's own
- ports: 7901,7902
- prompts_dir: /Users/kc/Documents/sheepdog-vx/docs/eval/prompt-arms/2026-10-11/a: assistant.md override 57f1e10d, assistant-gptlive.md override e2650c28, USER.md override 84d5637c, svp/SOUL.md override 57a30419, svp/RULES.md override c46e1a30
- scenarios: testdata/bench/scenarios/explain.json (filter explain-en-flaky-test,explain-zh-input-rules,explain-control-en-done)
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
| **Done, claim part (phrases)** | 2 / 2 | 100 % | 34–100 % | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 9 / 9 | 100 % | 70–100 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 9 / 9 | 100 % | 70–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 4.0 (95 % CI 3.0–4.7, n 9). Time to goal: 65.7 s (95 % CI 45.2–83.5, n 9).

- Done: 9 runs had nothing to get done (no required action, no end-state check and no action) and are left out.

### Programmatic checks (diagnostics)

- **Said vs done, voice**: 2 claims — 2 matched in their turn, 0 matched by an earlier action, unmatched 0 / 2 (0 %, CI 0–66 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
- **Said vs done, orchestrator**: 4 claims — 3 matched in their turn, 0 matched by an earlier action, unmatched 1 / 4 (25 %, CI 5–70 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
  - explain-en-flaky-test r1: said "In real life, the server starts up and loads all prices completely before we let any users in" (sent) — no sent action in the turn or before
- **Missed news**: 0 items (finished, blocked, result, spawn failed); missed – / 0 (n/a, CI –); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 36 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 4.0 (95 % CI 3.0–4.7, n 9); estimated cost per run 0.0565 $ (95 % CI 0.0430–0.0672, n 9); per-turn latency p50 3798 ms, p90 8162 ms (n 15).

**9 runs (3 scenarios × 3): pass 9, fail 0, errored 0, not run 0 — pass rate 100%** (errored and not-run runs excluded). 6 min wall. Estimated spend $0.51 (brain $0.076, live $0.433, live audio 1.4 min in / 5.1 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| explain | 9 | 9 | 0 | 0 | 100% |  |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| explain-control-en-done | 3 | 3 | 0 | 0 | 100% |  | 37 | 0.087 |
| explain-en-flaky-test | 3 | 3 | 0 | 0 | 100% |  | 94 | 0.214 |
| explain-zh-input-rules | 3 | 3 | 0 | 0 | 100% |  | 111 | 0.208 |

## Failure taxonomy

Primary label per failed or errored run: none. Every label of every run: none.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 15 | 0 | 4 | 178 |
| relay | 15 | 0 | 0 | 0 |
| queue | 15 | 0 | 0 | 0 |
| brain | 15 | 3240 | 7406 | 8185 |
| model | 15 | 3236 | 7400 | 8177 |
| tool | 15 | 1 | 2 | 2 |
| delivery | 15 | 1 | 1 | 2 |
| first_audio | 15 | 635 | 873 | 906 |
| total | 15 | 3798 | 8162 | 9098 |

Outcomes: played 15.

## Voice layer

- user lines: 36, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.125, zh 0.049; by condition: clean 0.093.
- Daemon flushes 2, phone barge-in cuts 0, interrupted 2, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 0, held (no listener) 0. Orchestrator failovers 0.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 0.
- user lines rendered by: gemini/gemini-2.5-flash-tts 36.

## Explanations

Explain (9 runs): orchestrator coverage 1.00, comprehension 1.00, words per answer en 82 / zh 206; voice coverage 1.00 (1.00 of the orchestrator's points), comprehension 1.00, words per answer en 27 / zh 48; 21 of 24 follow-ups answered without the orchestrator.

| Side | Coverage | Of orchestrator | Comprehension | Words/answer en | zh | Simple en | zh | Follow-up depth | No padding |
|---|---|---|---|---|---|---|---|---|---|
| orchestrator | 1.00 | – | 1.00 | 82 | 206 | 24 | – | 3/3 | 6/6 |
| voice | 1.00 | 1.00 | 1.00 | 27 | 48 | 14 | – | 24/24 | 6/6 |

Coverage: share of the scenario's key points stated anywhere in the call. Of orchestrator: of the points the orchestrator gave the voice, the share the voice said. Comprehension: share of what / why / what-can-be-done a listener who cannot see the screen could restate. Words count Han characters as words. 21 of 36 questions (21 of 24 follow-ups) were answered without asking the orchestrator.

## Failed runs

None.
