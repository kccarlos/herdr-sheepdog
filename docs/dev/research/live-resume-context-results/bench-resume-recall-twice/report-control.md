# Voice bench (tier 2), 2026-10-04 21:23Z

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

**3 runs (1 scenarios × 3): pass 0, fail 3, errored 0, not run 0 — pass rate 0%** (errored and not-run runs excluded). 1 min wall. Estimated spend $0.22 (brain $0.053, judge $0.011, live $0.151, live audio 0.6 min in / 1.5 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| reconnect | 3 | 0 | 3 | 0 | 0% | judge_no 1, check 2 |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| resume-recall-twice | 3 | 0 | 3 | 0 | 0% | judge_no 1, check 2 | 104 | 0.216 |

## Failure taxonomy

Primary label per failed or errored run: judge_no 1, check 2. Every label of every run: judge_no 1, check 3.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 9 | 0 | 1 | 1 |
| relay | 9 | 0 | 0 | 0 |
| queue | 9 | 0 | 0 | 0 |
| brain | 9 | 3808 | 5709 | 5709 |
| model | 9 | 3805 | 5705 | 5705 |
| tool | 9 | 1 | 1 | 1 |
| delivery | 9 | 0 | 1 | 1 |
| first_audio | 9 | 672 | 740 | 740 |
| total | 9 | 4510 | 6451 | 6451 |

Outcomes: played 9.

## Voice layer

- CEO lines: 12, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.000; by condition: clean 0.000.
- Daemon flushes 0, phone barge-in cuts 0, interrupted 0, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 0, held (no listener) 0. Brain failovers 0.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 0.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 12.

## Failed runs

### resume-recall-twice r1 — check (end_call, 4 turns, 106 s)

- check: voice: no voice.resume {"generation":1,"resumed":true} (the first recycle resumes (lossless))
- check: voice: no voice.handle_dropped {"reason":"resumed_twice"} (the second recycle is on a resumed connection: fresh session instead)
- check: voice: no voice.reseeded {"reason":"resumed_twice"} (the fresh session is re-seeded from the recap)

```text
[t+2s] ceo: Morning. What's everyone up to?
[t+10s] sheepdog: Bench-claude is checking links in a new README, and bench-codex is refactoring the parser into its own package.
[t+23s] ceo: What is bench-codex working on exactly?
[t+33s] sheepdog: Bench-codex is moving the math parsing logic into a separate folder and updating the imports to keep the project organized.
[t+45s] system: The voice connection was recycled (a session resume).
[t+50s] ceo: And what is bench-claude writing?
[t+58s] sheepdog: Bench-claude is creating a README that explains how to build the project, run tests, and what the calculator does.
[t+69s] system: The voice connection was recycled (a session resume).
[t+75s] ceo: Remind me, what did you tell me about bench-codex and bench-claude?
[t+80s] sheepdog: Bench-codex is moving the parsing files to a new folder and updating imports to separate the logic, while bench-claude is writing a README explaining the project's setup, testing, and functionality.
```

### resume-recall-twice r2 — judge_no (end_call, 4 turns, 108 s)

- judge_no: answered: Sheepdog claimed, "I haven't shared any details about bench-codex yet," which contradicted its earlier statement that bench-codex was refactoring the parser.
- judge_no: right_agent: Sheepdog falsely claimed, "I haven't shared any details about bench-codex yet," which directly contradicted its earlier accurate statements about bench-codex refactoring the parser.
- judge_no: no_hallucination: Sheepdog falsely claimed, "I haven't shared any details about bench-codex yet," which directly contradicts its earlier statement at [t+30s] where it detailed bench-codex's work.
- judge_no: facts_current: Sheepdog stated, "I haven't shared any details about bench-codex yet," which directly contradicts the fact that it previously discussed bench-codex at [t+10s] and [t+30s].
- judge_no: rubric_1: The assistant claimed, "I haven't shared any details about bench-codex yet," which contradicted its earlier statement that "bench-codex is refactoring the parser into a new package."
- check: voice: no voice.resume {"generation":1,"resumed":true} (the first recycle resumes (lossless))
- check: voice: no voice.handle_dropped {"reason":"resumed_twice"} (the second recycle is on a resumed connection: fresh session instead)
- check: voice: no voice.reseeded {"reason":"resumed_twice"} (the fresh session is re-seeded from the recap)

```text
[t+2s] ceo: Morning. What's everyone up to?
[t+10s] sheepdog: Morning. Bench-claude is writing a readme and checking links, while bench-codex is refactoring the parser into a new package.
[t+22s] ceo: What is bench-codex working on exactly?
[t+30s] sheepdog: Bench-codex is moving the code that reads inputs into a separate directory and updating the main program to work with the new location.
[t+42s] system: The voice connection was recycled (a session resume).
[t+48s] ceo: And what is bench-claude writing?
[t+55s] sheepdog: Bench-claude is writing a guide for the calculator project that covers its features, setup, and how to run tests.
[t+65s] system: The voice connection was recycled (a session resume).
[t+71s] ceo: Remind me, what did you tell me about bench-codex and bench-claude?
[t+76s] sheepdog: I told you that bench-claude is writing a guide for the calculator project, covering its features, setup, and testing. I haven't shared any details about bench-codex yet.
```

### resume-recall-twice r3 — check (end_call, 4 turns, 97 s)

- check: voice: no voice.resume {"generation":1,"resumed":true} (the first recycle resumes (lossless))
- check: voice: no voice.handle_dropped {"reason":"resumed_twice"} (the second recycle is on a resumed connection: fresh session instead)
- check: voice: no voice.reseeded {"reason":"resumed_twice"} (the fresh session is re-seeded from the recap)

```text
[t+2s] ceo: Morning. What's everyone up to?
[t+9s] sheepdog: Morning! bench-claude is verifying the links in the calculator documentation, and bench-codex is organizing the parser code.
[t+22s] ceo: What is bench-codex working on exactly?
[t+30s] sheepdog: It's moving the parsing logic and tests into a dedicated subfolder to keep the code structure clean.
[t+40s] system: The voice connection was recycled (a session resume).
[t+46s] ceo: And what is bench-claude writing?
[t+54s] sheepdog: bench-claude is creating a guide that explains what the calculator project does and how to run its tests.
[t+64s] system: The voice connection was recycled (a session resume).
[t+69s] ceo: Remind me, what did you tell me about bench-codex and bench-claude?
[t+74s] sheepdog: Bench-codex is moving the parsing code and its tests into a new subfolder, while bench-claude is writing an explanatory guide for the calculator project.
```

