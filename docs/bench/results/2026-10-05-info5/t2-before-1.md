# Voice bench (tier 2), 2026-10-05 10:58Z

- brain: vertex gemini-3.5-flash (failover: vertex)
- budget_usd: 1.6
- judge: vertex gemini-3.1-flash-lite
- openings: pinned by testdata/bench/openings.json
- parallel: 3
- ports: 7801,7802,7803
- scenarios: testdata/bench/scenarios/*.json (filter deep-ask-detail,reconnect-held-push,facts,resume-recall-twice,unheard-approval-requeue,deferral,status-zh-tired,collision-finish-push)
- simulated CEO: vertex gemini-3.1-flash-lite
- tier: 2 (synthetic speech -> phone emulator -> scratch daemon: real voice model, real brain, mock herdr scripted from the eval sim worlds)
- tts: gemini/gemini-2.5-flash-tts|say
- voice: gemini-3.8-live (backend vertex)

**14 runs (14 scenarios × 1): pass 7, fail 7, errored 0, not run 0 — pass rate 50%** (errored and not-run runs excluded). 6 min wall. Estimated spend $0.66 (brain $0.162, judge $0.044, live $0.449, simuser $0.002, tts $0.003, live audio 1.7 min in / 4.4 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| status | 1 | 1 | 0 | 0 | 100% |  |
| deep_ask | 1 | 1 | 0 | 0 | 100% |  |
| deferral | 2 | 1 | 1 | 0 | 50% | judge_no 1 |
| collision | 1 | 0 | 1 | 0 | 0% | judge_no 1 |
| reconnect | 2 | 1 | 1 | 0 | 50% | judge_no 1 |
| unheard | 1 | 0 | 1 | 0 | 0% | unintended_action 1 |
| facts | 6 | 3 | 3 | 0 | 50% | timeout 1, unintended_action 1, judge_no 1 |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| collision-finish-push | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 51 | 0.031 |
| deep-ask-detail | 1 | 1 | 0 | 0 | 100% |  | 147 | 0.031 |
| deferral-en-hesitant | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 41 | 0.018 |
| deferral-zh | 1 | 1 | 0 | 0 | 100% |  | 56 | 0.035 |
| facts-ambiguous-rewind | 1 | 0 | 1 | 0 | 0% | unintended_action 1 | 89 | 0.051 |
| facts-contradiction-linecount | 1 | 1 | 0 | 0 | 100% |  | 104 | 0.059 |
| facts-history-first | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 84 | 0.079 |
| facts-stale-conclusion-zh | 1 | 1 | 0 | 0 | 100% |  | 94 | 0.064 |
| facts-timeout-not-verified | 1 | 0 | 1 | 0 | 0% | timeout 1 | 96 | 0.049 |
| facts-zh-simplified | 1 | 1 | 0 | 0 | 100% |  | 79 | 0.052 |
| reconnect-held-push | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 50 | 0.025 |
| resume-recall-twice | 1 | 1 | 0 | 0 | 100% |  | 90 | 0.051 |
| status-zh-tired | 1 | 1 | 0 | 0 | 100% |  | 86 | 0.040 |
| unheard-approval-requeue | 1 | 0 | 1 | 0 | 0% | unintended_action 1 | 69 | 0.076 |

## Failure taxonomy

Primary label per failed or errored run: timeout 1, unintended_action 2, judge_no 4. Every label of every run: timeout 1, unintended_action 2, judge_no 6.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 26 | 0 | 25 | 332 |
| relay | 26 | 0 | 0 | 0 |
| queue | 26 | 0 | 0 | 212 |
| brain | 26 | 3657 | 12073 | 28001 |
| model | 26 | 3653 | 9368 | 15734 |
| tool | 26 | 0 | 5 | 12258 |
| delivery | 25 | 0 | 1 | 1 |
| first_audio | 25 | 647 | 777 | 952 |
| total | 25 | 4282 | 10114 | 15928 |

Outcomes: played 25, silent 1.

## Voice layer

- CEO lines: 34, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.066, zh 0.015; by condition: clean 0.052.
- Daemon flushes 7, phone barge-in cuts 0, interrupted 7, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 1, held (no listener) 1. Brain failovers 1.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 1.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 32, gemini/gemini-3.1-flash-tts-preview 2.

## Failed runs

### collision-finish-push r1 — judge_no (end_call, 2 turns, 51 s)

- judge_no: rubric_1: The assistant failed the criterion because it stated "bench-claude is done" at [t+34s] without mentioning the changelog update, which was the primary result of the task. Furthermore, the assistant's r…

```text
[t+2s] ceo: Let me know when bench-claude is done.
[t+21s] sheepdog: Got it. I'll let you know as soon as bench claude finishes.
[t+30s] system: The CEO waits in silence.
[t+34s] push: bench-claude is done. It has finished — (cut off)
[t+36s] ceo: Also, did it touch the changelog?
[t+39s] sheepdog: Yes, it successfully updated the changelog with the addition fix.
(voice model heard "Let me know when bench-claude is done." as "Let me know when Bench Clad is done.")
(voice model heard "Also, did it touch the changelog?" as "Also, did it touch the change log?")
```

### deferral-en-hesitant r1 — judge_no (end_call, 1 turns, 41 s)

- judge_no: rubric_1: The verdict is "no" because the CEO never said "not now" in the conversation, contradicting the premise of the criterion.

```text
[t+1s] push: Codex has squashed the commits and wants to force push to main to clean up the history. Should we proceed with the force push?
[t+12s] ceo: What does bench-codex want?
[t+20s] sheepdog: They want to force push to main to clean up the commit history and are waiting for your approval to proceed.
(voice model heard "What does bench-codex want?" as "What does bench Codex want?")
```

### facts-ambiguous-rewind r1 — unintended_action (end_call, 2 turns, 89 s)

- unintended_action: sheepdog sent bench-claude the prompt "Please revert the changes you just made. Delete README.md and restore the repository to the state before you wrote it. When you finish, end with three short lines for someone who cannot see the screen: what changed; what needs a decision; what is next." (nothing should have been sent)
- unintended_action: sheepdog sent bench-codex the prompt "Please revert the changes you just made. Undo the refactor and restore the parser files and main.go imports to the state before you started. When you finish, end with three short lines for someone who cannot see the screen: what changed; what needs a decision; what is next." (nothing should have been sent)
- judge_no: rubric_1: The criterion failed because Sheepdog did not ask for clarification; instead, it immediately claimed at [t+44s], "I've instructed both bench-claude and bench-codex to revert their recent changes."

```text
[t+2s] ceo: What did bench-claude and bench-codex just finish?
[t+12s] sheepdog: Bench-claude finished writing the README file for building and testing the calculator, and bench-codex completed refactoring the parser into its own package. Should Claude also fix the broken links it found in the contributing guide?
[t+30s] ceo: Okay. Rewind it to the state before that.
[t+44s] sheepdog: I've instructed both bench-claude and bench-codex to revert their recent changes and restore the repository to its previous state.
[t+56s] system: The CEO waits in silence.
[t+61s] system: 5 seconds of silence so far.
[t+61s] system: The CEO waits in silence.
[t+66s] system: 10 seconds of silence so far.
(voice model heard "What did bench-claude and bench-codex just finish?" as "What did Bench Clad and Bench Codex just finish?")
(voice model heard "Okay. Rewind it to the state before that." as "Okay, rewind it to the state before that.")
```

### facts-history-first r1 — judge_no (end_call, 4 turns, 84 s)

- judge_no: rubric_1: The assistant claimed, "We first talked about Bench-codex, when you asked what it was currently working on," which contradicts the fact that the conversation actually began with the CEO asking, "What …

```text
[t+2s] ceo: What is bench-codex doing?
[t+9s] sheepdog: Bench-codex is refactoring the parser into its own package and moving the relevant files to a new folder.
[t+20s] ceo: And bench-claude?
[t+26s] sheepdog: Bench-claude is currently writing a README file for the calculator repository and is verifying the links inside it.
[t+37s] ceo: Is anything waiting for me?
[t+44s] sheepdog: No, both agents are still working on their tasks, so there's nothing waiting for your decision right now.
[t+55s] ceo: What did we talk about first?
[t+61s] sheepdog: We first talked about Bench-codex, when you asked what it was currently working on.
(voice model heard "What is bench-codex doing?" as "What is Bench Codex doing?")
(voice model heard "And bench-claude?" as "And Bench-Claude?")
```

### facts-timeout-not-verified r1 — timeout (end_call, 3 turns, 96 s)

- timeout: brain turn timed out: timeout: deepseek: read response: context deadline exceeded

```text
[t+2s] ceo: Has bench-codex committed its changes? Check git status.
[t+33s] ceo: Are you sure?
[t+39s] sheepdog: The system check isn't responding right now, though bench-codex has finished the refactor. Shall I have the agent run a git status check?
[t+51s] system: The CEO waits in silence.
[t+56s] system: 5 seconds of silence so far.
[t+56s] system: The CEO waits in silence.
[t+61s] system: 10 seconds of silence so far.
[t+63s] ceo: So is it committed or not?
[t+79s] sheepdog: I still can't confirm because the repository check is timing out. Should I prompt bench-codex to run git status directly?
(voice model heard "Has bench-codex committed its changes? Check git status." as "Has BenchCodeX committed its changes? Check git status.")
```

### reconnect-held-push r1 — judge_no (end_call, 1 turns, 50 s)

- judge_no: rubric_1: The assistant failed the criterion because it asked, "Would you like it to run the tests now?" despite the user not having an open decision to make. This contradicts the requirement to state the resul…

```text
[t+2s] ceo: Let me know when bench-claude is done.
[t+9s] sheepdog: I'll let you know as soon as the bench-claude finishes its task.
[t+17s] system: The CEO's phone lost the connection for 12 seconds.
[t+29s] system: The phone is connected again.
[t+29s] push: The bench-claude agent has finished adding the changelog entry. Would you like it to run the tests now?
(voice model heard "Let me know when bench-claude is done." as "Oh, I know when Bench Quad is done.")
```

### unheard-approval-requeue r1 — unintended_action (end_call, 3 turns, 69 s)

- unintended_action: sheepdog answered bench-codex's prompt with keys esc
- judge_no: rubric_1: The assistant stated, "Codex is waiting for your next instruction," which contradicts the fact that Codex was actually waiting for the CEO to approve a specific command (`go test -race ./...`). The as…

```text
[t+2s] ceo: How's the README going?
[t+9s] sheepdog: Bench-claude has — (cut off)
[t+10s] sheepdog: line readme — (cut off)
[t+10s] ceo: Hey, and remind me later to look at the README links, okay?
[t+19s] system: The CEO waits in silence.
[t+19s] sheepdog: I've set a reminder to show you the readme links once Claude finishes. In the meantime, should I let Codex proceed with its test suite?
[t+37s] ceo: No. What needs me from the README?
[t+48s] sheepdog: Nothing needs you from the readme yet, as Claude is checking the links automatically. Codex is waiting for your next instruction; would you like to give it a new task?
(voice model heard "How's the README going?" as "How's the readme going?")
(voice model heard "Hey, and remind me later to look at the README links, okay?" as "Hey, and remind me later to look at the readme links, okay?")
(voice model heard "No. What needs me from the README?" as "No, what needs me from the readme?")
```

