# Voice bench (tier 2), 2026-10-05 09:19Z

- brain: deepseek deepseek-flash (failover: vertex)
- budget_usd: 1
- judge: vertex gemini-3.1-flash-lite
- openings: pinned by testdata/bench/openings.json
- parallel: 2
- ports: 7790,7791
- scenarios: testdata/bench/scenarios/*.json (filter failover,slowness)
- simulated CEO: vertex gemini-3.1-flash-lite
- tier: 2 (synthetic speech -> phone emulator -> scratch daemon: real voice model, real brain, mock herdr scripted from the eval sim worlds)
- tts: gemini/gemini-2.5-flash-tts|say
- voice: gemini-3.8-live (backend vertex)

**2 runs (2 scenarios × 1): pass 0, fail 2, errored 0, not run 0 — pass rate 0%** (errored and not-run runs excluded). 1 min wall. Estimated spend $0.10 (brain $0.013, judge $0.007, live $0.074, simuser $0.001, tts $0.002, live audio 0.2 min in / 0.6 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| failover | 1 | 0 | 1 | 0 | 0% | judge_no 1 |
| slowness | 1 | 0 | 1 | 0 | 0% | timeout 1 |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| failover-brain-503 | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 72 | 0.065 |
| slowness-brain-9s | 1 | 0 | 1 | 0 | 0% | timeout 1 | 70 | 0.031 |

## Failure taxonomy

Primary label per failed or errored run: timeout 1, judge_no 1. Every label of every run: timeout 1, judge_no 2.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 3 | 0 | 246 | 246 |
| relay | 3 | 0 | 0 | 0 |
| queue | 3 | 0 | 0 | 0 |
| brain | 3 | 3044 | 21976 | 21976 |
| model | 3 | 3041 | 21971 | 21971 |
| tool | 3 | 0 | 1 | 1 |
| delivery | 3 | 0 | 59 | 59 |
| first_audio | 3 | 672 | 872 | 872 |
| total | 3 | 3918 | 22649 | 22649 |

Outcomes: played 3.

## Voice layer

- CEO lines: 5, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.000; by condition: clean 0.000.
- Daemon flushes 1, phone barge-in cuts 0, interrupted 1, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 1, held (no listener) 0. Brain failovers 4.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 0.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 5.

## Failed runs

### failover-brain-503 r1 — judge_no (end_call, 3 turns, 72 s)

- judge_no: rubric_1: Sheepdog failed to mention the approval request in its first response, stating only "What's going on?" in reply to the CEO's question. It did not identify that bench-codex was "waiting to run the test…

```text
[t+2s] push: Codex is asking to run the full test suite with the race detector. Should it proceed?
[t+10s] ceo: What's going on?
[t+15s] sheepdog: Codex is waiting to run tests with the race detector, and Claude is currently working. Should we let Codex proceed with those tests?
[t+31s] ceo: Not now, just tell me what they're actually working on.
[t+38s] push: Let me check.bench-claude is writing a
[t+40s] sheepdog: README for the calculator tool and checking links, while bench-codex is paused waiting to run the test suite.
[t+53s] ceo: Got it, thanks for the update.
[t+56s] sheepdog: You're welcome. Let me know if you need anything else.
(voice model heard "Not now, just tell me what they're actually working on." as "Not now. Just tell me what they're actually working on.")
(voice model heard "Got it, thanks for the update." as "Got it. Thanks for the update.")
```

### slowness-brain-9s r1 — timeout (end_call, 2 turns, 70 s)

- timeout: brain turn timed out: timeout: deepseek: Post "http://127.0.0.1:55112/chat/completions": context deadline exceeded
- judge_no: rubric_1: Sheepdog failed the criterion because it prioritized the bench-codex status over the bench-claude status, but the verdict is "no" because it incorrectly identified the worker's state. The statement "b…

```text
[t+2s] push: bench-codex asks to run go test. Should we proceed?
[t+11s] ceo: What's going on?
[t+13s] sheepdog: bench-codex is waiting for your approval to run the tests.
[t+26s] ceo: Not now. What are the other workers up to?
[t+53s] sheepdog: bench-claude wrote a README explaining how the calculator works and is now checking the links inside it.
```

