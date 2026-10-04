# Voice bench (tier 2), 2026-10-04 21:18Z

- brain: vertex gemini-3.5-flash (failover: vertex)
- budget_usd: 3
- judge: vertex gemini-3.1-flash-lite
- openings: pinned by testdata/bench/openings.json
- parallel: 3
- ports: 7790,7791,7792
- scenarios: testdata/bench/scenarios/*.json (filter resume-recall-twice)
- simulated CEO: vertex gemini-3.1-flash-lite
- tier: 2 (synthetic speech -> phone emulator -> scratch daemon: real voice model, real brain, mock herdr scripted from the eval sim worlds)
- tts: gemini/gemini-2.5-flash-tts|say
- voice: gemini-3.8-live (backend vertex)

**3 runs (1 scenarios × 3): pass 2, fail 1, errored 0, not run 0 — pass rate 67%** (errored and not-run runs excluded). 1 min wall. Estimated spend $0.23 (brain $0.054, judge $0.010, live $0.159, tts $0.002, live audio 0.6 min in / 1.8 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| reconnect | 3 | 2 | 1 | 0 | 67% | brain_fallback 1 |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| resume-recall-twice | 3 | 2 | 1 | 0 | 67% | brain_fallback 1 | 114 | 0.226 |

## Failure taxonomy

Primary label per failed or errored run: brain_fallback 1. Every label of every run: brain_fallback 1.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 8 | 0 | 2 | 2 |
| relay | 8 | 0 | 0 | 0 |
| queue | 8 | 0 | 0 | 0 |
| brain | 8 | 3761 | 12466 | 12466 |
| model | 8 | 3756 | 12460 | 12460 |
| tool | 8 | 1 | 3 | 3 |
| delivery | 8 | 0 | 0 | 0 |
| first_audio | 8 | 695 | 815 | 815 |
| total | 8 | 4462 | 13162 | 13162 |

Outcomes: played 8.

## Voice layer

- CEO lines: 12, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.050; by condition: clean 0.050.
- Daemon flushes 0, phone barge-in cuts 0, interrupted 0, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 0, held (no listener) 0. Brain failovers 1.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 0.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 12.

## Failed runs

### resume-recall-twice r1 — brain_fallback (end_call, 4 turns, 109 s)

- brain_fallback: model_error: deepseek: HTTP 402: Insufficient Balance (request_id: 19f00636-bf80-421e-8f8b-892db34069a7)

```text
[t+2s] ceo: Morning. What's everyone up to?
[t+11s] sheepdog: Morning! bench-claude is working on the documentation for the calculator, while bench-codex is tidying up the code by moving the parser into its own module.
[t+26s] ceo: What is bench-codex working on exactly?
[t+43s] sheepdog: I am checking on that for you right now.
[t+49s] system: The voice connection was recycled (a session resume).
[t+57s] ceo: And what is bench-claude writing?
[t+60s] sheepdog: bench-claude is creating documentation that explains how to use and test the calculator app.
[t+70s] system: The voice connection was recycled (a session resume).
[t+80s] ceo: Remind me, what did you tell me about bench-codex and bench-claude?
[t+85s] sheepdog: I mentioned that bench-claude is writing documentation for the calculator app, including how to use and test it. bench-codex is tidying up the code by moving the parser into its own module.
(voice model heard "Morning. What's everyone up to?" as "Morgen, what's everyone up to?")
```

