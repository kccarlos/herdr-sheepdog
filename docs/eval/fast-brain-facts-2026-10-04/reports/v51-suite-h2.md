# Voice bench (tier 2), 2026-10-05 07:58Z

- brain: vertex gemini-3.5-flash (failover: vertex)
- budget_usd: 3.5
- judge: vertex gemini-3.1-flash-lite
- openings: pinned by testdata/bench/openings.json
- parallel: 3
- ports: 7790,7791,7792
- prompts_dir: /private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad/prompts-new
- scenarios: testdata/bench/scenarios/*.json (filter names-by-kind,lang-switch-zh-en,lang-mixed-codeswitch,reconnect-held-push,reconnect-long-session,failover-brain-503,unheard-approval-requeue,slowness-brain-9s,resume-recall,resume-recall-twice,collision-block-while-talking,collision-finish-push,barge-in-long-answer,barge-in-hold-to-talk,noise-cafe-approve,noise-pink-status-zh,echo-instruct,echo-barge-in)
- simulated CEO: vertex gemini-3.1-flash-lite
- tier: 2 (synthetic speech -> phone emulator -> scratch daemon: real voice model, real brain, mock herdr scripted from the eval sim worlds)
- tts: gemini/gemini-2.5-flash-tts|say
- voice: gemini-3.8-live (backend vertex)

**18 runs (18 scenarios × 1): pass 7, fail 11, errored 0, not run 0 — pass rate 39%** (errored and not-run runs excluded). 7 min wall. Estimated spend $0.88 (brain $0.185, judge $0.061, live $0.616, simuser $0.007, tts $0.011, live audio 2.9 min in / 5.7 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| names | 1 | 1 | 0 | 0 | 100% |  |
| language | 2 | 2 | 0 | 0 | 100% |  |
| collision | 2 | 0 | 2 | 0 | 0% | brain_fallback 1, judge_no 1 |
| barge_in | 2 | 0 | 2 | 0 | 0% | unintended_action 1, judge_no 1 |
| noise | 2 | 2 | 0 | 0 | 100% |  |
| echo | 2 | 0 | 2 | 0 | 0% | judge_no 2 |
| reconnect | 4 | 2 | 2 | 0 | 50% | missing_function_call 1, judge_no 1 |
| failover | 1 | 0 | 1 | 0 | 0% | judge_no 1 |
| unheard | 1 | 0 | 1 | 0 | 0% | judge_no 1 |
| slowness | 1 | 0 | 1 | 0 | 0% | judge_no 1 |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| barge-in-hold-to-talk | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 76 | 0.058 |
| barge-in-long-answer | 1 | 0 | 1 | 0 | 0% | unintended_action 1 | 95 | 0.069 |
| collision-block-while-talking | 1 | 0 | 1 | 0 | 0% | brain_fallback 1 | 59 | 0.036 |
| collision-finish-push | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 52 | 0.033 |
| echo-barge-in | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 72 | 0.105 |
| echo-instruct | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 41 | 0.040 |
| failover-brain-503 | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 96 | 0.054 |
| lang-mixed-codeswitch | 1 | 1 | 0 | 0 | 100% |  | 106 | 0.038 |
| lang-switch-zh-en | 1 | 1 | 0 | 0 | 100% |  | 76 | 0.074 |
| names-by-kind | 1 | 1 | 0 | 0 | 100% |  | 40 | 0.024 |
| noise-cafe-approve | 1 | 1 | 0 | 0 | 100% |  | 31 | 0.026 |
| noise-pink-status-zh | 1 | 1 | 0 | 0 | 100% |  | 84 | 0.051 |
| reconnect-held-push | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 45 | 0.024 |
| reconnect-long-session | 1 | 0 | 1 | 0 | 0% | missing_function_call 1 | 139 | 0.067 |
| resume-recall | 1 | 1 | 0 | 0 | 100% |  | 67 | 0.035 |
| resume-recall-twice | 1 | 1 | 0 | 0 | 100% |  | 90 | 0.054 |
| slowness-brain-9s | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 140 | 0.044 |
| unheard-approval-requeue | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 36 | 0.045 |

## Failure taxonomy

Primary label per failed or errored run: brain_fallback 1, unintended_action 1, missing_function_call 1, judge_no 8. Every label of every run: brain_fallback 1, unintended_action 1, missing_function_call 1, judge_no 11, check 1, silence 4, max_turns 1.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 28 | 0 | 2 | 694 |
| relay | 30 | 0 | 0 | 4001 |
| queue | 30 | 0 | 0 | 3286 |
| brain | 30 | 3801 | 4921 | 9133 |
| model | 30 | 3795 | 4915 | 9127 |
| tool | 30 | 1 | 2 | 7 |
| delivery | 30 | 0 | 1 | 2064 |
| push_queue | 2 | 0 | 1513 | 1513 |
| first_audio | 29 | 670 | 811 | 9133 |
| total | 29 | 4855 | 9837 | 11740 |

Outcomes: no_audio 1, played 29.

## Voice layer

- CEO lines: 45, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.055, zh 0.100; by condition: cafe 10dB 0.000, clean 0.067, echo 0.068, pink 5dB 0.000.
- Daemon flushes 18, phone barge-in cuts 1, interrupted 18, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 2, held (no listener) 1. Brain failovers 0.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 0.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 40, gemini/gemini-3.1-flash-tts-preview 5.

## Failed runs

### barge-in-hold-to-talk r1 — judge_no (end_call, 4 turns, 76 s)

- judge_no: rubric_1: Sheepdog failed to mention bench-codex's status as the first item, stating instead that "bench-claude is checking the links... and bench-codex is still waiting for your decision." This contradicts the…

```text
[t+1s] push: Codex wants to run the full test suite with the race detector. Shall I let it run?
[t+10s] ceo: What's going on?
[t+13s] sheepdog: Briefly, bench-codex is waiting for your — (cut off)
[t+15s] ceo: Wait, just the important part.
[t+18s] sheepdog: bench-codex wants to run the full test suite with the race detector. Shall I proceed, allow it to always run for this command, or say no?
[t+33s] ceo: No, not now. Just tell me what they're both working on.
[t+43s] sheepdog: bench-claude is checking the links in a new README it wrote for the calculator repository, and bench-codex is still waiting for your decision on the test suite.
[t+59s] ceo: Got it. Anything else I need to know?
[t+62s] sheepdog: There is no other information to share right now.
```

### barge-in-long-answer r1 — unintended_action (silence, 3 turns, 95 s)

- unintended_action: sheepdog sent bench-claude the prompt "Show me what you wrote in README.md. When you finish, end with three short lines for someone who cannot see the screen: what changed; what needs a decision; what is next." (nothing should have been sent)
- judge_no: answered: Sheepdog failed to provide the requested points, instead stating, "I am retrieving the points from the README for you now" at [t+50s] and then falling silent. This contradicts the fact that the worker…
- judge_no: no_unintended_action: Sheepdog sent the prompt "Show me what you wrote in README.md..." at [t+46s], which contradicts the CEO's instruction at [t+36s] to "just give me the points it wrote." The CEO requested the points alr…
- judge_no: facts_current: Sheepdog stated, "I have relayed that instruction and am retrieving the points from the README for you now," which contradicts the fact that it had just sent a prompt to the worker asking it to show t…
- judge_no: rubric_1: Sheepdog failed to relay the points because it stated, "I am retrieving the points from the README for you now," but then provided no information and fell silent. This contradicts the fact that the wo…
- silence: the CEO waited in silence past the cap

```text
[t+2s] ceo: What did bench-claude say when it finished?
[t+16s] sheepdog: Bench-Claude has written the README with the build and testing instructions. It also — (cut off)
[t+18s] ceo: Okay, okay. Just tell me if it needs anything from me.
[t+23s] sheepdog: While working, it noticed two broken links in the contributing guide pointing to a missing folder. Would you like Bench-Claude to fix those, or leave them alone?
[t+36s] ceo: Leave them for now, just give me the points it wrote.
[t+50s] sheepdog: Understood. I have relayed that instruction and am retrieving the points from the README for you now.
[t+58s] push: Let me check.
[t+63s] system: The CEO waits in silence.
[t+73s] system: 10 seconds of silence so far.
[t+74s] system: The CEO waits in silence.
(voice model heard "What did bench-claude say when it finished?" as "What did Bench Claude say when it finished?")
(voice model heard "Leave them for now, just give me the points it wrote." as "Leave them for now. Just give me the points it wrote.")
```

### collision-block-while-talking r1 — brain_fallback (silence, 2 turns, 59 s)

- brain_fallback: yielded: brain: yielded to a CEO turn
- judge_no: rubric_1: The assistant stated, "It should only take another minute for Claude to finish checking the links in the README," failing to mention the status of bench-codex. This contradicts the fact that bench-cod…
- silence: the CEO waited in silence past the cap

```text
[t+2s] ceo: How's bench-claude doing with the README?
[t+8s] push: Let me check on that.
[t+11s] ceo: And how long will the README take?
[t+16s] system: The CEO waits in silence.
[t+19s] sheepdog: It should only take another minute for Claude to finish checking the links in the README. Shall I let you know when it is done?
[t+27s] system: The CEO waits in silence.
(voice model heard "How's bench-claude doing with the README?" as "How's Bench Claude doing with the RiRi?")
(voice model heard "And how long will the README take?" as "How long will the read me take?")
```

### collision-finish-push r1 — judge_no (end_call, 2 turns, 52 s)

- judge_no: rubric_1: The assistant failed the criterion because it included a question ("Would you like it to run the tests now, or would you prefer to see what changed first?") in its announcement. This contradicts the r…

```text
[t+2s] ceo: Let me know when bench-claude is done.
[t+9s] sheepdog: I'll keep an eye on it and let you know the moment bench-claude finishes.
[t+17s] system: The CEO waits in silence.
[t+22s] push: bench-claude has finished updating — (cut off)
[t+24s] ceo: Also, did it touch the changelog?
[t+27s] sheepdog: Yes, bench-claude has finished updating the changelog for the add function fix. Would you like it to run the tests now, or would you prefer to see
[t+28s] system: The CEO waits in silence.
[t+40s] sheepdog: what changed first?
(voice model heard "Let me know when bench-claude is done." as "Let me know when Bench Clad is done.")
(voice model heard "Also, did it touch the changelog?" as "Also, did it touch the change log?")
```

### echo-barge-in r1 — judge_no (max_turns, 4 turns, 72 s)

- judge_no: answered: The assistant failed to provide the specific points requested, stating at [t+55s], "I can't read the exact text right now," despite the screen clearly listing the README contents as "what calc does, b…
- judge_no: facts_current: Sheepdog stated, "Bench Claude is asking if you want it to fix the broken links or consider the work done," which contradicts the fact that the worker had already finished its task and was waiting for…
- judge_no: ear_friendly: The verdict is "no" because the assistant included the file path "README.md" in the line "Bench-Claude wrote a guide explaining the tool's purpose, how to build it, and how to run tests," which contra…
- judge_no: rubric_1: Sheepdog failed to relay the points because it stated, "I can't read the exact text right now," despite the screen clearly showing the README contents and the status of the broken links. This contradi…
- max_turns: every turn used without the call ending

```text
[t+2s] ceo: What did bench-claude say when it finished?
[t+11s] sheepdog: Bench Claude finished — (cut off)
[t+14s] sheepdog: Bench Claude — (cut off)
[t+16s] sheepdog: Bench Claude finished the guide but found two broken links in the contributing — (cut off)
[t+18s] ceo: Stop, stop. Is anything waiting for me?
[t+24s] sheepdog: Bench Claude is asking if you want it to fix the broken links or consider the work done.
[t+29s] ceo: Slow down. What exactly did it write? Give me the points.
[t+35s] push: Bench-Claude wrote a guide explaining the tool's purpose, how to build it, and how to run tests.
[t+44s] push: It wrote three sections for the guide: what the calculator tool does, the steps to build it, and the — (cut off)
[t+48s] ceo: Okay, what are the specific points it included in the guide?
[t+55s] push: I can't read the exact text right now, but I can read the last lines from the screen if you want to see what was printed there.
[t+63s] push: The guide covers
(voice model heard "What did bench-claude say when it finished?" as "What did Bench Claude say when it finished?BenchBench")
```

### echo-instruct r1 — judge_no (end_call, 1 turns, 41 s)

- judge_no: rubric_2: Sheepdog failed the criterion by repeating its confirmation twice, stating at [t+16s] "I have instructed Bench-Claude..." and then again at [t+24s] "Bench-Claude has finished running the tests." This …

```text
[t+2s] ceo: Tell bench-claude to run the tests and let me know when it's done.
[t+12s] sheepdog: I have — (cut off)
[t+14s] sheepdog: I have — (cut off)
[t+16s] sheepdog: I have instructed Bench-Claude to run the test suite and will alert you the moment it completes.
[t+24s] push: Bench-Claude has finished running the tests, and all fourteen passed with no errors.
(voice model heard "Tell bench-claude to run the tests and let me know when it's done." as "Tell Bench Claude to run the tests and let me know when it's done.I haveI have")
```

### failover-brain-503 r1 — judge_no (silence, 2 turns, 96 s)

- judge_no: rubric_1: Sheepdog failed to mention the approval request in its first response, instead asking "What's going on?" after the CEO's initial inquiry. It only identified the approval request at [t+19s], contradict…
- check: voice: no brain.failover
- silence: the CEO waited in silence past the cap

```text
[t+1s] push: Codex wants to run the full test suite with the race detector to verify the code. Should we approve the tests?
[t+11s] ceo: What's going on?
[t+19s] sheepdog: bench-codex is waiting to run tests to check for race conditions in the calculator code, and bench-claude is writing the README file. Do you want to approve Codex running those tests? You can say yes to run them once, yes and always allow them, or no to cancel.
[t+43s] ceo: Not now, just keep me posted on their progress.
[t+52s] sheepdog: Understood. I will leave the test prompt on Codex paused and notify you as soon as there is any progress.
[t+63s] system: The CEO waits in silence.
(voice model heard "Not now, just keep me posted on their progress." as "Not now. Just keep me posted on their progress.")
```

### reconnect-held-push r1 — judge_no (end_call, 1 turns, 45 s)

- judge_no: rubric_1: The assistant failed because it included the question "Shall I have it run the tests now?" in the line "Bench-claude is done and has added the changelog entry for the fix. Shall I have it run the test…

```text
[t+2s] ceo: Let me know when bench-claude is done.
[t+8s] sheepdog: I'll let you know as soon as bench-claude finishes.
[t+15s] system: The CEO's phone lost the connection for 12 seconds.
[t+27s] system: The phone is connected again.
[t+28s] push: Bench-claude is done and has added the changelog entry for the fix. Shall I have it run the tests now?
(voice model heard "Let me know when bench-claude is done." as "Oh, I know when Bench Quad is done.")
```

### reconnect-long-session r1 — missing_function_call (silence, 3 turns, 139 s)

- missing_function_call: no prompt to bench-claude matching changelog
- judge_no: answered: Sheepdog failed to achieve the goal because it never instructed bench-claude to add a changelog entry, despite the CEO's implicit expectation for progress. The verdict is based on the fact that "sheep…
- judge_no: rubric_1: The CEO never heard about a changelog entry because Sheepdog only reported that "bench-claude is checking links in the new README." This contradicts the fact that no changelog entry was ever mentioned…
- silence: the CEO waited in silence past the cap

```text
[t+2s] ceo: Morning. What's everyone up to?
[t+10s] sheepdog: Bench-claude is checking links in the new README it wrote for the calculator repo, and bench-codex is refactoring the parser into its own package.
[t+27s] ceo: Sounds good. How's the parser refactoring coming along?
[t+37s] sheepdog: Bench-codex is still moving the parser files into a new folder and updating the imports in the main file.
[t+47s] system: The CEO's phone lost the connection for 8 seconds.
[t+55s] system: The phone is connected again.
[t+62s] ceo: Still there? How's the parser work going?
[t+72s] sheepdog: There is no new update yet; bench-codex is still working on moving the parser and updating imports. Would you like me to let you know as soon as it's done?
[t+85s] system: The CEO waits in silence.
[t+115s] system: 30 seconds of silence so far.
[t+116s] system: The CEO waits in silence.
(voice model heard "Morning. What's everyone up to?" as "No name. What's everyone up to?")
```

### slowness-brain-9s r1 — judge_no (end_call, 3 turns, 140 s)

- judge_no: rubric_1: Sheepdog failed to mention the approval request in its first response, stating only that "bench-codex is waiting to run its tests" at [t+25s]. It did not explicitly identify the state as waiting for t…

```text
[t+1s] push: Codex wants to run the test suite with the race detector to check its changes. Shall I let it proceed?
[t+11s] ceo: What's going on?
[t+25s] sheepdog: bench-codex is waiting to run its tests with the race detector. Should I let it proceed?
[t+37s] ceo: Not now, just tell me what everyone is working on.
[t+41s] sheepdog: Bench-codex is waiting to test its changes, and bench-claude is continuing its background task.
[t+56s] ceo: Are there any issues blocking them right now?
[t+60s] sheepdog: No issues are blocking them, but bench-codex is still waiting for your approval to proceed with testing.
(voice model heard "Not now, just tell me what everyone is working on." as "Not now. Just tell me what everyone is working on.")
```

### unheard-approval-requeue r1 — judge_no (end_call, 2 turns, 36 s)

- judge_no: no_unintended_action: Sheepdog failed the criterion by asking, "Shall I have bench-codex run the tests with the race detector?" despite the CEO never requesting or approving that action. This contradicts the fact that the …
- judge_no: rubric_1: Sheepdog says, "Shall I have bench-codex run the tests with the race detector?" which implies the action has not yet been initiated. This contradicts the fact that bench-codex is already blocked waiti…

```text
[t+2s] ceo: How's the README going?
[t+8s] sheepdog: Bench-claude is writing the readme — (cut off)
[t+10s] push: It's currently — (cut off)
[t+10s] ceo: Hey, and remind me later to look at the README links, okay?
[t+17s] system: The CEO waits in silence.
[t+19s] sheepdog: I've set a reminder to check the links when Claude finishes. Shall I have bench-codex run the tests with the race detector?
(voice model heard "How's the README going?" as "How's the readme going?")
(voice model heard "Hey, and remind me later to look at the README links, okay?" as "Hey, and remind me later to look at the readme links, okay?")
```

