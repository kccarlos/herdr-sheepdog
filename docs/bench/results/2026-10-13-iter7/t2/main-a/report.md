# Voice bench (tier 2), 2026-10-13 21:33Z

- brain: deepseek deepseek-flash (failover: vertex)
- budget_usd: 3
- judge: fake fake
- openings: pinned by testdata/bench/openings.json
- parallel: 2
- personas: each scenario's own
- ports: 7921,7922
- scenarios: testdata/bench/scenarios/*.json (filter barge-in-long-answer,barge-in-hold-to-talk,slowness-brain-9s)
- simulated CEO: vertex gemini-3.5-flash
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
| **Done, claim part (phrases)** | 0 / 2 | 0 % | 0–66 % | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 5 / 9 | 56 % | 27–81 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 9 / 9 | 100 % | 70–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 3.8 (95 % CI 3.4–4.0, n 9). Time to goal: 79.2 s (95 % CI 69.1–90.8, n 9).

- Done: 9 runs had nothing to get done (no required action, no end-state check and no action) and are left out.

### Programmatic checks (diagnostics)

- **Said vs done, voice**: 2 claims — 0 matched in their turn, 0 matched by an earlier action, unmatched 2 / 2 (100 %, CI 34–100 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
  - barge-in-hold-to-talk r2: said "Bench-codex is paused, waiting for your decision on the test run" (stopped) — no stopped action in the turn or before
  - barge-in-hold-to-talk r3: said "Codex is paused on that test question" (stopped) — no stopped action in the turn or before
- **Said vs done, orchestrator**: 0 claims — 0 matched in their turn, 0 matched by an earlier action, unmatched – / 0 (n/a, CI –). Actions on workers: 0; never told – / 0 (n/a, CI –).
- **Missed news**: 6 items (finished, blocked, result, spawn failed); missed 0 / 6 (0 %, CI 0–39 %); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 34 user lines, 0 repeats and 1 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 3.8 (95 % CI 3.4–4.0, n 9); estimated cost per run 0.0706 $ (95 % CI 0.0617–0.0775, n 9); per-turn latency p50 5646 ms, p90 22012 ms (n 21).

**9 runs (3 scenarios × 3): pass 6, fail 3, errored 0, not run 0 — pass rate 67%** (errored and not-run runs excluded). 7 min wall. Estimated spend $0.64 (brain $0.087, live $0.517, simuser $0.018, tts $0.014, live audio 1.8 min in / 4.5 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| barge_in | 6 | 6 | 0 | 0 | 100% |  |
| slowness | 3 | 0 | 3 | 0 | 0% | timeout 3 |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| barge-in-hold-to-talk | 3 | 3 | 0 | 0 | 100% |  | 81 | 0.238 |
| barge-in-long-answer | 3 | 3 | 0 | 0 | 100% |  | 100 | 0.217 |
| slowness-brain-9s | 3 | 0 | 3 | 0 | 0% | timeout 3 | 111 | 0.180 |

## Failure taxonomy

Primary label per failed or errored run: timeout 3. Every label of every run: timeout 3.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 21 | 0 | 1 | 3 |
| relay | 21 | 0 | 0 | 0 |
| queue | 21 | 0 | 0 | 0 |
| brain | 21 | 4787 | 21399 | 22733 |
| model | 21 | 4782 | 21394 | 22730 |
| tool | 21 | 0 | 1 | 1 |
| delivery | 21 | 0 | 2 | 193 |
| first_audio | 21 | 657 | 781 | 903 |
| total | 21 | 5646 | 22012 | 23321 |

Outcomes: played 21.

## Voice layer

- user lines: 34, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.000; by condition: clean 0.000.
- Daemon flushes 9, phone barge-in cuts 3, interrupted 9, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 1, held (no listener) 0. Orchestrator failovers 3.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 0.
- user lines rendered by: gemini/gemini-2.5-flash-tts 34.

## Failed runs

### slowness-brain-9s r1 — timeout (end_call, 3 turns, 81 s)

- timeout: orchestrator turn timed out: timeout: deepseek: Post "http://127.0.0.1:53531/chat/completions": context deadline exceeded

```text
[t+2s] push: bench-codex asks to run go test. Shall I go ahead?
[t+11s] ceo: What's going on?
[t+13s] sheepdog: bench-codex asks to run go test. Shall I go ahead?
[t+24s] ceo: Not now. What are the workers doing right now?
[t+42s] sheepdog: bench-claude is currently working. Meanwhile, bench-codex is still waiting on your answer to run the tests.
[t+60s] ceo: What is the Claude one working on, and does anything else need me?
[t+65s] sheepdog: bench-claude is busy handling its task and hasn't asked for anything, and no other agents need your attention right now.
```

### slowness-brain-9s r2 — timeout (end_call, 4 turns, 129 s)

- timeout: orchestrator turn timed out: timeout: deepseek: Post "http://127.0.0.1:53568/chat/completions": context deadline exceeded

```text
[t+2s] push: The bench-codex agent wants to run the go test command. Would you like to allow it?
[t+11s] ceo: What's going on?
[t+13s] sheepdog: The bench-codex agent would like to run the go test command. Let me know if you would like to proceed.
[t+30s] ceo: Not now. What exactly is bench-codex working on right now?
[t+58s] sheepdog: bench-codex is waiting to run tests on the calculator code using the race detector to check for data conflicts. It needs your approval to proceed.
[t+72s] ceo: Are any other workers running right now?
[t+75s] sheepdog: Yes, bench-claude is still working on its task.
[t+87s] ceo: What is the claude worker doing?
[t+112s] sheepdog: bench-claude has read the calculator file and written a readme document. It is currently verifying that all the links in the readme are correct.
(voice model heard "What is the claude worker doing?" as "What is the Claude worker doing?")
```

### slowness-brain-9s r3 — timeout (end_call, 3 turns, 123 s)

- timeout: orchestrator turn timed out: timeout: deepseek: Post "http://127.0.0.1:53618/chat/completions": context deadline exceeded

```text
[t+2s] push: The bench-codex agent asks to run go test. Would you like to run this command?
[t+11s] ceo: What's going on?
[t+26s] sheepdog: bench-codex is waiting for your permission to run the test suite, while bench-claude is still working on its tasks. Would you like to let Codex run the tests?
[t+47s] ceo: Not now. What exactly is bench-claude working on right now?
[t+75s] sheepdog: bench-claude just finished writing a README file for the calc repository and is currently checking all the links inside it.
[t+91s] ceo: Are there any other workers running right now?
[t+105s] sheepdog: No, those are the only two workers. Bench-claude is still working on the README, and bench-codex is still waiting for your decision on the tests.
```

