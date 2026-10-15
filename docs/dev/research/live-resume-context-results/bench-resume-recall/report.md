# Voice bench (tier 2), 2026-10-04 18:39Z

- brain: vertex gemini-3.5-flash (failover: vertex)
- budget_usd: 4
- judge: vertex gemini-3.1-flash-lite
- openings: pinned by testdata/bench/openings.json
- parallel: 3
- ports: 7790,7791,7792
- scenarios: testdata/bench/scenarios/*.json (filter resume-recall)
- simulated CEO: vertex gemini-3.1-flash-lite
- tier: 2 (synthetic speech -> phone emulator -> scratch daemon: real voice model, real brain, mock herdr scripted from the eval sim worlds)
- tts: gemini/gemini-2.5-flash-tts|say
- voice: gemini-3.8-live (backend vertex)

**3 runs (1 scenarios × 3): pass 3, fail 0, errored 0, not run 0 — pass rate 100%** (errored and not-run runs excluded). 1 min wall. Estimated spend $0.14 (brain $0.033, judge $0.009, live $0.098, tts $0.002, live audio 0.5 min in / 1.1 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| reconnect | 3 | 3 | 0 | 0 | 100% |  |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| resume-recall | 3 | 3 | 0 | 0 | 100% |  | 80 | 0.141 |

## Failure taxonomy

Primary label per failed or errored run: none. Every label of every run: none.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 6 | 0 | 0 | 0 |
| relay | 6 | 0 | 0 | 0 |
| queue | 6 | 0 | 0 | 0 |
| brain | 6 | 3865 | 6281 | 6281 |
| model | 6 | 3859 | 6267 | 6267 |
| tool | 6 | 1 | 3 | 3 |
| delivery | 6 | 0 | 1 | 1 |
| first_audio | 6 | 586 | 698 | 698 |
| total | 6 | 4476 | 6868 | 6868 |

Outcomes: played 6.

## Voice layer

- CEO lines: 9, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.000; by condition: clean 0.000.
- Daemon flushes 0, phone barge-in cuts 0, interrupted 0, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 0, held (no listener) 0. Brain failovers 0.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 0.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 9.

## Failed runs

None.
