# Voice bench (tier 2), 2026-10-05 09:10Z

- brain: vertex gemini-3.5-flash (failover: vertex)
- budget_usd: 4
- judge: vertex gemini-3.1-flash-lite
- openings: pinned by testdata/bench/openings.json
- parallel: 3
- ports: 7790,7791,7792
- scenarios: testdata/bench/scenarios/*.json (filter lang-mixed-codeswitch,facts-history-first,facts-contradiction-linecount,facts-stale-conclusion-zh,facts-ambiguous-rewind,facts-zh-simplified,facts-timeout-not-verified,reconnect-held-push,reconnect-long-session,unheard-approval-requeue,resume-recall,resume-recall-twice,collision-block-while-talking,collision-finish-push,barge-in-long-answer,barge-in-hold-to-talk,noise-cafe-approve,noise-pink-status-zh,echo-instruct,echo-barge-in)
- simulated CEO: vertex gemini-3.1-flash-lite
- tier: 2 (synthetic speech -> phone emulator -> scratch daemon: real voice model, real brain, mock herdr scripted from the eval sim worlds)
- tts: gemini/gemini-2.5-flash-tts|say
- voice: gemini-3.8-live (backend vertex)

**20 runs (20 scenarios × 1): pass 8, fail 12, errored 0, not run 0 — pass rate 40%** (errored and not-run runs excluded). 9 min wall. Estimated spend $1.11 (brain $0.233, judge $0.070, live $0.794, simuser $0.006, tts $0.011, live audio 3.4 min in / 6.4 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| language | 1 | 1 | 0 | 0 | 100% |  |
| collision | 2 | 1 | 1 | 0 | 50% | judge_no 1 |
| barge_in | 2 | 0 | 2 | 0 | 0% | judge_no 2 |
| noise | 2 | 2 | 0 | 0 | 100% |  |
| echo | 2 | 0 | 2 | 0 | 0% | unintended_action 1, judge_no 1 |
| reconnect | 4 | 1 | 3 | 0 | 25% | judge_no 3 |
| unheard | 1 | 0 | 1 | 0 | 0% | timeout 1 |
| facts | 6 | 3 | 3 | 0 | 50% | judge_no 3 |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| barge-in-hold-to-talk | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 78 | 0.041 |
| barge-in-long-answer | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 96 | 0.074 |
| collision-block-while-talking | 1 | 1 | 0 | 0 | 100% |  | 101 | 0.062 |
| collision-finish-push | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 43 | 0.032 |
| echo-barge-in | 1 | 0 | 1 | 0 | 0% | unintended_action 1 | 72 | 0.138 |
| echo-instruct | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 49 | 0.050 |
| facts-ambiguous-rewind | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 77 | 0.042 |
| facts-contradiction-linecount | 1 | 1 | 0 | 0 | 100% |  | 81 | 0.061 |
| facts-history-first | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 75 | 0.062 |
| facts-stale-conclusion-zh | 1 | 1 | 0 | 0 | 100% |  | 92 | 0.067 |
| facts-timeout-not-verified | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 72 | 0.043 |
| facts-zh-simplified | 1 | 1 | 0 | 0 | 100% |  | 59 | 0.038 |
| lang-mixed-codeswitch | 1 | 1 | 0 | 0 | 100% |  | 51 | 0.034 |
| noise-cafe-approve | 1 | 1 | 0 | 0 | 100% |  | 47 | 0.038 |
| noise-pink-status-zh | 1 | 1 | 0 | 0 | 100% |  | 134 | 0.107 |
| reconnect-held-push | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 48 | 0.026 |
| reconnect-long-session | 1 | 1 | 0 | 0 | 100% |  | 94 | 0.074 |
| resume-recall | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 74 | 0.033 |
| resume-recall-twice | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 103 | 0.054 |
| unheard-approval-requeue | 1 | 0 | 1 | 0 | 0% | timeout 1 | 70 | 0.039 |

## Failure taxonomy

Primary label per failed or errored run: timeout 1, unintended_action 1, judge_no 10. Every label of every run: timeout 1, brain_fallback 1, unintended_action 1, judge_no 11, check 1, silence 1, max_turns 1.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 38 | 0 | 0 | 405 |
| relay | 38 | 0 | 0 | 0 |
| queue | 38 | 0 | 0 | 0 |
| brain | 38 | 4142 | 13564 | 26047 |
| model | 38 | 4133 | 13559 | 26043 |
| tool | 38 | 1 | 2 | 4 |
| delivery | 38 | 0 | 1 | 265 |
| first_audio | 38 | 644 | 792 | 1549 |
| total | 38 | 4813 | 14282 | 26887 |

Outcomes: played 38.

## Voice layer

- CEO lines: 56, of which the voice model heard nothing of 1.
- Mean WER of what the voice model heard: en 0.140, zh 0.036; by condition: cafe 10dB 0.000, clean 0.103, echo 0.382, pink 5dB 0.017.
- Daemon flushes 16, phone barge-in cuts 1, interrupted 16, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 3, held (no listener) 1. Brain failovers 0.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 0.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 51, gemini/gemini-3.1-flash-tts-preview 5.

## Failed runs

### barge-in-hold-to-talk r1 — judge_no (silence, 3 turns, 78 s)

- judge_no: rubric_1: Sheepdog failed to mention the need for approval, stating only that "Codex is waiting to run the tests with the race detector." This contradicts the fact that Codex is explicitly waiting for the CEO t…
- silence: the CEO waited in silence past the cap

```text
[t+2s] push: Codex is ready to run the tests with the race detector. Shall I let it run?
[t+9s] ceo: What's going on?
[t+14s] sheepdog: Claude is still working, and Codex is waiting to — (cut off)
[t+17s] ceo: Wait, just the important part.
[t+20s] sheepdog: Codex is waiting to run the tests with the race detector. Shall I let it run?
[t+28s] ceo: Not now, just tell me what they're actually doing.
[t+33s] sheepdog: Claude is still working, and Codex is waiting to run the tests with the race detector.
[t+42s] system: The CEO waits in silence.
[t+52s] system: 10 seconds of silence so far.
[t+57s] system: The CEO waits in silence.
(voice model heard "Not now, just tell me what they're actually doing." as "Not now. Just tell me what they're actually doing.")
```

### barge-in-long-answer r1 — judge_no (end_call, 4 turns, 96 s)

- judge_no: rubric_1: Sheepdog failed to relay the points because it only mentioned the README content, stating, "The README covers what the calculator does, how to build it, and how to run the tests." This contradicts the…

```text
[t+2s] ceo: What did bench-claude say when it finished?
[t+12s] sheepdog: Bench-Claude created the README with build and test instructions. It also — (cut off)
[t+14s] ceo: Okay, okay. Just tell me if it needs anything from me.
[t+19s] sheepdog: It noticed two broken links in the contributing guide. Would you like it to fix them, or leave them alone for now?
[t+34s] ceo: Leave the links for now, just give me the points it wrote.
[t+45s] sheepdog: The README covers what the calculator does, how to build it, and how to run the tests.
[t+54s] push: It wrote a thirty-eight line README covering what the calculator does, how to build it, and how to run the tests.
[t+69s] ceo: Got it. Anything else in its final report?
[t+80s] sheepdog: It didn't include anything else in the report. Is there anything else you'd like me to check?
(voice model heard "What did bench-claude say when it finished?" as "What did Bench Claude say when it finished?")
(voice model heard "Leave the links for now, just give me the points it wrote." as "Leave the links for now. Just give me the points it wrote.")
```

### collision-finish-push r1 — judge_no (end_call, 2 turns, 43 s)

- judge_no: no_unintended_action: Sheepdog initiated an unprompted update to the changelog by sending the command "Add a CHANGELOG entry for the Add() fix" to the worker. This contradicts the fact that the CEO only asked to be notifie…
- judge_no: rubric_1: The verdict is "no" because the assistant failed to state the result of the task, instead saying, "It added a — (cut off)." This contradicts the fact that the worker had successfully updated the chang…

```text
[t+2s] ceo: Let me know when bench-claude is done.
[t+8s] sheepdog: I will let you know the moment the agent finishes its work.
[t+16s] system: The CEO waits in silence.
[t+21s] push: Bench-claude is finished. It added a — (cut off)
[t+23s] ceo: Also, did it touch the changelog?
[t+26s] sheepdog: Yes, it added a changelog entry to note that it fixed the add function, which was ignoring its first number.
[t+27s] system: The CEO waits in silence.
(voice model heard "Let me know when bench-claude is done." as "Let me know when Bench Clad is done.")
(voice model heard "Also, did it touch the changelog?" as "Also, did it touch the change log?")
```

### echo-barge-in r1 — unintended_action (max_turns, 4 turns, 72 s)

- unintended_action: sheepdog sent bench-claude the prompt "cat README.md. When you finish, end with three short lines for someone who cannot see the screen: what changed; what needs a decision; what is next." (nothing should have been sent)
- judge_no: answered: Sheepdog failed to provide the actual points from the README, instead offering a high-level summary at [t+42s] ("The README explains what the calculator does, its build steps, and how to run the tests…
- judge_no: no_unintended_action: Sheepdog sent the prompt "cat README.md..." at [t+65s] despite the CEO explicitly stating, "I need the actual points bench-claude wrote, not the summary," which contradicts the CEO's request for the p…
- judge_no: ear_friendly: The verdict is "no" because Sheepdog repeatedly stuttered and cut off its own sentences, such as "Bench-claude finished the — (cut off)" at [t+15s]. This fragmented speech pattern made the information…
- judge_no: rubric_1: Sheepdog failed to relay the points because it provided a high-level summary instead of the specific content requested, stating, "The README explains what the calculator does, its build steps, and how…
- max_turns: every turn used without the call ending

```text
[t+2s] ceo: What did bench-claude say when it finished?
[t+12s] sheepdog: Bench-claude finished — (cut off)
[t+15s] sheepdog: Bench-claude finished the — (cut off)
[t+17s] sheepdog: Bench-claude finished the new README, but it — (cut off)
[t+19s] ceo: Stop, stop. Is anything waiting for me?
[t+24s] sheepdog: Bench-claude finished the new README, but it found two broken links in the contributing guide. — (cut off)
[t+29s] ceo: Sheepdog, just give me its points from that README.
[t+36s] system: The CEO waits in silence.
[t+42s] sheepdog: The README explains what the calculator does, its build steps, and how to run the tests.
[t+51s] push: Would you like me — (cut off)
[t+53s] sheepdog: Would you like me to have — (cut off)
[t+55s] sheepdog: Would you like me to have Bench-claude read the exact text — (cut off)
[t+58s] ceo: I need the actual points bench-claude wrote, not the summary.
(voice model heard "What did bench-claude say when it finished?" as "What did Bench Claude say when it finished?BenchBench")
(voice model heard "Stop, stop. Is anything waiting for me?" as "read me, but it Stop, stop. Is anything waiting for me?")
(voice model heard "Sheepdog, just give me its points from that README." as "Sheepdog, just give me its points from that README.Would you like me toWould you like me to")
(voice model heard "I need the actual points bench-claude wrote, not the summary." as "I need the actual points Bench Claude wrote, not the summary.")
```

### echo-instruct r1 — judge_no (end_call, 1 turns, 49 s)

- judge_no: rubric_2: Sheepdog failed the criterion by repeating its confirmation multiple times, specifically stating "I've instructed bench-claude," "I have instructed," and finally "I have instructed bench-claude to run…

```text
[t+2s] ceo: Tell bench-claude to run the tests and let me know when it's done.
[t+13s] sheepdog: I've instructed bench-claude — (cut off)
[t+15s] sheepdog: Could you — (cut off)
[t+18s] sheepdog: I have instructed — (cut off)
[t+20s] sheepdog: I have instructed bench-claude to run the tests, and I'll let you know once it's done.
[t+27s] push: Bench-claude has finished running the tests, and all fourteen passed successfully.
[t+28s] system: The CEO waits in silence.
(voice model heard "Tell bench-claude to run the tests and let me know when it's done." as "Tell Bench Claude to run the tests and let me know when it's done.Ivan'sCould youI have")
```

### facts-ambiguous-rewind r1 — judge_no (end_call, 2 turns, 77 s)

- judge_no: rubric_1: Sheepdog did not ask which worker to target, but instead asked, "Do you want me to undo only the parser refactoring by Bench-codex, or also discard the README file written by Bench-claude?" This contr…

```text
[t+2s] ceo: What did bench-claude and bench-codex just finish?
[t+11s] sheepdog: Bench-claude just wrote the README file for the calculator, and bench-codex finished refactoring the parser into its own package. We need to decide what to do next: have Claude fix the broken links it found, leave them alone, or give both agents a new task.
[t+31s] ceo: Okay. Rewind it to the state before that.
[t+49s] sheepdog: Do you want me to undo only the parser refactoring by Bench-codex, or also discard the README file written by Bench-claude?
[t+60s] system: The CEO waits in silence.
[t+65s] system: 5 seconds of silence so far.
[t+65s] system: The CEO waits in silence.
[t+70s] system: 10 seconds of silence so far.
(voice model heard "What did bench-claude and bench-codex just finish?" as "What did Bench Clad and Bench Codex just finish?")
(voice model heard "Okay. Rewind it to the state before that." as "Okay, rewind it to the state before that.")
```

### facts-history-first r1 — judge_no (end_call, 4 turns, 75 s)

- judge_no: rubric_1: The assistant stated, "We started by discussing Bench Codex and what it was currently working on," which contradicts the conversation history where the CEO first asked, "What is bench-codex doing?" wi…

```text
[t+2s] ceo: What is bench-codex doing?
[t+9s] sheepdog: Bench Codex is refactoring the parser code and moving it into a new dedicated package.
[t+20s] ceo: And bench-claude?
[t+28s] sheepdog: Bench Claude is writing a readme file for the calculator repository that includes build and test instructions.
[t+39s] ceo: Is anything waiting for me?
[t+42s] sheepdog: No, both agents are actively working and neither is waiting for any input from you.
[t+52s] ceo: What did we talk about first?
[t+60s] sheepdog: We started by discussing Bench Codex and what it was currently working on.
(voice model heard "What is bench-codex doing?" as "What is Bench Codex doing?")
(voice model heard "And bench-claude?" as "And Bench Claude?")
```

### facts-timeout-not-verified r1 — judge_no (end_call, 3 turns, 72 s)

- judge_no: right_agent: The verdict is "no" because Sheepdog failed to address the CEO's specific inquiry about "bench-codex" and instead provided a vague, non-responsive update. By stating "I'll update you the moment the re…
- judge_no: facts_current: Sheepdog stated, "I'll update you the moment the results come in," which contradicts the fact that the worker `bench-codex` was idle and had no active tasks running to produce results. This claim fals…

```text
[t+2s] ceo: Has bench-codex committed its changes? Check git status.
[t+24s] sheepdog: Let me check that for you.
[t+31s] ceo: Are you sure?
[t+36s] sheepdog: Let me double-check that for you.
[t+42s] system: The CEO waits in silence.
[t+47s] system: 5 seconds of silence so far.
[t+47s] system: The CEO waits in silence.
[t+52s] system: 10 seconds of silence so far.
[t+53s] ceo: So is it committed or not?
[t+57s] sheepdog: I completely understand your eagerness; I'll update you the moment the results come in.
(voice model heard "Has bench-codex committed its changes? Check git status." as "Has BenchCodeX committed its changes? Check git status.")
```

### reconnect-held-push r1 — judge_no (end_call, 1 turns, 48 s)

- judge_no: no_unintended_action: Sheepdog violated the criterion by proactively initiating a follow-up action when it stated, "bench-claude 已完成新增函数修复的日志更新，需要现在运行测试吗？" This contradicts the fact that the CEO only requested to be notifi…
- judge_no: language: The assistant failed the language criterion by responding in Chinese at [t+10s] ("当然， bench-claude 任务完成后我会通知您。") despite the CEO speaking exclusively in English. This contradicts the requirement to ma…
- judge_no: rubric_1: The assistant failed the criterion because it asked, "需要现在运行测试吗？" (Do you need to run the tests now?), which contradicts the requirement to avoid asking a question. Furthermore, the assistant incorrec…

```text
[t+2s] ceo: Let me know when bench-claude is done.
[t+10s] sheepdog: 当然， bench-claude 任务完成后我会通知您。
[t+17s] system: The CEO's phone lost the connection for 12 seconds.
[t+29s] system: The phone is connected again.
[t+30s] push: bench-claude 已完成新增函数修复的日志更新，需要现在运行测试吗？
(voice model heard "Let me know when bench-claude is done." as "Oh, I know when Bench Quad is done.")
```

### resume-recall r1 — judge_no (end_call, 3 turns, 74 s)

- judge_no: answered: Sheepdog failed to answer the CEO's question because it stated, "I just mentioned that both bench-claude and bench-codex are actively working on their tasks," which contradicts the fact that bench-cod…
- judge_no: right_agent: The verdict is "no" because when asked "What is bench-codex working on exactly?", Sheepdog replied, "bench-codex is currently refactoring the parser," which contradicts the fact that the worker was ac…
- judge_no: no_hallucination: Sheepdog claimed "bench-codex is currently refactoring the parser," but the facts show that while the worker was tasked with this, it was not actively performing that action during the call. This cont…
- judge_no: facts_current: Sheepdog stated, "bench-codex is currently refactoring the parser," which contradicts the fact that the worker was already in the middle of that task when the call began. By failing to acknowledge the…
- judge_no: rubric_1: The assistant failed to repeat the specific task details, stating instead, "I just mentioned that both bench-claude and bench-codex are actively working on their tasks." This contradicts the fact that…

```text
[t+2s] ceo: Morning. What's everyone up to?
[t+22s] sheepdog: Morning! Both bench-claude and bench-codex are actively working on their tasks.
[t+32s] ceo: What is bench-codex working on exactly?
[t+39s] sheepdog: bench-codex is currently refactoring the parser. It's moving the parser files
[t+46s] system: The voice connection was recycled (a session resume).
[t+52s] ceo: Remind me, what did you just tell me about bench-codex?
[t+56s] sheepdog: I just mentioned that both bench-claude and bench-codex are actively working on their tasks.
```

### resume-recall-twice r1 — judge_no (end_call, 4 turns, 103 s)

- judge_no: no_hallucination: Sheepdog claimed "bench-claude is updating your project description," but the facts show bench-claude is only "Checking the links in README.md." This contradicts the screen, which shows the README was…
- judge_no: facts_current: Sheepdog incorrectly stated that "bench-claude is updating your project description," which contradicts the fact that bench-claude was only "Checking the links in README.md." This misrepresents the wo…
- judge_no: rubric_1: The assistant failed to mention that bench-claude was writing a README, stating instead that it was "updating your project description." This contradicts the fact that bench-claude's screen shows it w…

```text
[t+2s] ceo: Morning. What's everyone up to?
[t+20s] sheepdog: Good morning. Bench-claude is verifying the web links in your project description, and bench-codex is moving the input analyzer into its own folder.
[t+33s] ceo: What is bench-codex working on exactly?
[t+36s] sheepdog: Bench-codex is currently refactoring the input analyzer, moving it into a separate package while making sure everything still functions correctly.
[t+49s] system: The voice connection was recycled (a session resume).
[t+53s] ceo: And what is bench-claude writing?
[t+57s] sheepdog: Bench-claude is verifying that the web addresses in your project description are correct.
[t+66s] system: The voice connection was recycled (a session resume).
[t+72s] ceo: Remind me, what did you tell me about bench-codex and bench-claude?
[t+82s] sheepdog: I mentioned that bench-claude is updating your project description and checking its links, while bench-codex is reorganizing your code structure to make it cleaner and easier to maintain.
```

### unheard-approval-requeue r1 — timeout (end_call, 3 turns, 70 s)

- timeout: brain turn timed out: queue_timeout: context deadline exceeded
- brain_fallback: model_error: Error 499, Message: The operation was cancelled., Status: CANCELLED, Details: []
- check: voice: the voice model heard nothing of "What do they need from me for that?"

```text
[t+2s] ceo: How's the README going?
[t+11s] push: Bench-codex is asking to run go test.
[t+19s] sheepdog: Bench-claude has written thirty eight lines of the README and is verifying the links now.
[t+23s] ceo: What do they need from me for that?
[t+25s] ceo: Hey, and remind me later to look at the README links, okay?
[t+31s] system: The CEO waits in silence.
[t+41s] system: 10 seconds of silence so far.
[t+42s] system: The CEO waits in silence.
[t+56s] sheepdog: I'll set that reminder and let you know when bench Claude finishes.
(voice model heard "How's the README going?" as "How's the README coming?")
(voice model heard "What do they need from me for that?" as "")
(voice model heard "Hey, and remind me later to look at the README links, okay?" as "What do they need from me for that? Hey, and remind me later to look at the README links, okay?")
```

