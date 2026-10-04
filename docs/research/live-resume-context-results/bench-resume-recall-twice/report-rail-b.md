# Voice bench (tier 2), 2026-10-04 21:21Z

- brain: vertex gemini-3.5-flash (failover: vertex)
- budget_usd: 3
- judge: vertex gemini-3.1-flash-lite
- openings: pinned by testdata/bench/openings.json
- parallel: 2
- ports: 7790,7791,7792
- scenarios: testdata/bench/scenarios/*.json (filter resume-recall-twice)
- simulated CEO: vertex gemini-3.1-flash-lite
- tier: 2 (synthetic speech -> phone emulator -> scratch daemon: real voice model, real brain, mock herdr scripted from the eval sim worlds)
- tts: gemini/gemini-2.5-flash-tts|say
- voice: gemini-3.8-live (backend vertex)

**2 runs (1 scenarios × 2): pass 2, fail 0, errored 0, not run 0 — pass rate 100%** (errored and not-run runs excluded). 1 min wall. Estimated spend $0.14 (brain $0.032, judge $0.006, live $0.106, live audio 0.4 min in / 1.1 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| reconnect | 2 | 2 | 0 | 0 | 100% |  |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| resume-recall-twice | 2 | 2 | 0 | 0 | 100% |  | 104 | 0.145 |

## Failure taxonomy

Primary label per failed or errored run: none. Every label of every run: none.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 7 | 0 | 6 | 6 |
| relay | 7 | 0 | 0 | 0 |
| queue | 7 | 0 | 0 | 0 |
| brain | 7 | 3789 | 4843 | 4843 |
| model | 7 | 3786 | 4841 | 4841 |
| tool | 7 | 0 | 1 | 1 |
| delivery | 7 | 1 | 2 | 2 |
| first_audio | 7 | 696 | 840 | 840 |
| total | 7 | 4442 | 5551 | 5551 |

Outcomes: played 7.

## Voice layer

- CEO lines: 8, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.025; by condition: clean 0.025.
- Daemon flushes 0, phone barge-in cuts 0, interrupted 0, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 0, held (no listener) 0. Brain failovers 0.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 0.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 8.

## Failed runs

None.
