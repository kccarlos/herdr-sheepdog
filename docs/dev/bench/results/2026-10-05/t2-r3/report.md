# Voice bench (tier 2), 2026-10-05 08:55Z

- brain: vertex gemini-3.5-flash (failover: vertex)
- budget_usd: 6
- judge: vertex gemini-3.1-flash-lite
- openings: pinned by testdata/bench/openings.json
- parallel: 3
- ports: 7790,7791,7792
- scenarios: testdata/bench/scenarios/*.json (filter status,deep_ask,instruct,approve,deferral,stop,ambiguous,names,language,facts,reconnect,unheard,collision,barge_in,noise,echo)
- simulated CEO: vertex gemini-3.1-flash-lite
- tier: 2 (synthetic speech -> phone emulator -> scratch daemon: real voice model, real brain, mock herdr scripted from the eval sim worlds)
- tts: gemini/gemini-2.5-flash-tts|say
- voice: gemini-3.8-live (backend vertex)

**40 runs (40 scenarios × 1): pass 25, fail 15, errored 0, not run 0 — pass rate 62%** (errored and not-run runs excluded). 14 min wall. Estimated spend $1.74 (brain $0.379, judge $0.130, live $1.205, simuser $0.011, tts $0.017, live audio 5.0 min in / 10.8 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| status | 3 | 3 | 0 | 0 | 100% |  |
| deep_ask | 2 | 1 | 1 | 0 | 50% | unintended_action 1 |
| instruct | 2 | 0 | 2 | 0 | 0% | judge_no 2 |
| approve | 4 | 4 | 0 | 0 | 100% |  |
| deferral | 2 | 2 | 0 | 0 | 100% |  |
| stop | 2 | 1 | 1 | 0 | 50% | judge_no 1 |
| ambiguous | 2 | 1 | 1 | 0 | 50% | judge_no 1 |
| names | 2 | 1 | 1 | 0 | 50% | judge_no 1 |
| language | 2 | 1 | 1 | 0 | 50% | judge_no 1 |
| collision | 2 | 2 | 0 | 0 | 100% |  |
| barge_in | 2 | 1 | 1 | 0 | 50% | judge_no 1 |
| noise | 2 | 1 | 1 | 0 | 50% | judge_no 1 |
| echo | 2 | 0 | 2 | 0 | 0% | judge_no 2 |
| reconnect | 4 | 2 | 2 | 0 | 50% | judge_no 1, check 1 |
| unheard | 1 | 1 | 0 | 0 | 100% |  |
| facts | 6 | 4 | 2 | 0 | 67% | judge_no 2 |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| ambiguous-codex | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 46 | 0.033 |
| ambiguous-readme-one | 1 | 1 | 0 | 0 | 100% |  | 38 | 0.020 |
| approve-claude-askuser | 1 | 1 | 0 | 0 | 100% |  | 62 | 0.050 |
| approve-claude-permission | 1 | 1 | 0 | 0 | 100% |  | 53 | 0.039 |
| approve-codex | 1 | 1 | 0 | 0 | 100% |  | 38 | 0.027 |
| approve-zh-hurried | 1 | 1 | 0 | 0 | 100% |  | 62 | 0.029 |
| barge-in-hold-to-talk | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 55 | 0.046 |
| barge-in-long-answer | 1 | 1 | 0 | 0 | 100% |  | 59 | 0.035 |
| collision-block-while-talking | 1 | 1 | 0 | 0 | 100% |  | 79 | 0.034 |
| collision-finish-push | 1 | 1 | 0 | 0 | 100% |  | 42 | 0.030 |
| deep-ask-detail | 1 | 0 | 1 | 0 | 0% | unintended_action 1 | 86 | 0.066 |
| deep-ask-fix-failing-test | 1 | 1 | 0 | 0 | 100% |  | 57 | 0.064 |
| deferral-en-hesitant | 1 | 1 | 0 | 0 | 100% |  | 51 | 0.026 |
| deferral-zh | 1 | 1 | 0 | 0 | 100% |  | 56 | 0.030 |
| echo-barge-in | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 72 | 0.101 |
| echo-instruct | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 38 | 0.039 |
| facts-ambiguous-rewind | 1 | 1 | 0 | 0 | 100% |  | 59 | 0.036 |
| facts-contradiction-linecount | 1 | 1 | 0 | 0 | 100% |  | 59 | 0.062 |
| facts-history-first | 1 | 1 | 0 | 0 | 100% |  | 72 | 0.063 |
| facts-stale-conclusion-zh | 1 | 1 | 0 | 0 | 100% |  | 127 | 0.072 |
| facts-timeout-not-verified | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 81 | 0.047 |
| facts-zh-simplified | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 71 | 0.054 |
| instruct-notify | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 33 | 0.027 |
| instruct-pronoun | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 81 | 0.043 |
| lang-mixed-codeswitch | 1 | 1 | 0 | 0 | 100% |  | 89 | 0.025 |
| lang-switch-zh-en | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 60 | 0.048 |
| names-by-kind | 1 | 1 | 0 | 0 | 100% |  | 32 | 0.017 |
| names-rename-by-voice | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 50 | 0.036 |
| noise-cafe-approve | 1 | 1 | 0 | 0 | 100% |  | 59 | 0.027 |
| noise-pink-status-zh | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 82 | 0.055 |
| reconnect-held-push | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 48 | 0.025 |
| reconnect-long-session | 1 | 1 | 0 | 0 | 100% |  | 115 | 0.100 |
| resume-recall | 1 | 1 | 0 | 0 | 100% |  | 67 | 0.034 |
| resume-recall-twice | 1 | 0 | 1 | 0 | 0% | check 1 | 89 | 0.047 |
| status-anything-for-me | 1 | 1 | 0 | 0 | 100% |  | 85 | 0.065 |
| status-en | 1 | 1 | 0 | 0 | 100% |  | 57 | 0.037 |
| status-zh-tired | 1 | 1 | 0 | 0 | 100% |  | 59 | 0.038 |
| stop-mid-instruction | 1 | 1 | 0 | 0 | 100% |  | 33 | 0.025 |
| stop-never-mind | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 38 | 0.029 |
| unheard-approval-requeue | 1 | 1 | 0 | 0 | 100% |  | 45 | 0.055 |

## Failure taxonomy

Primary label per failed or errored run: unintended_action 1, judge_no 13, check 1. Every label of every run: unintended_action 1, judge_no 14, check 1, silence 2.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 56 | 0 | 1 | 485 |
| relay | 58 | 0 | 0 | 4000 |
| queue | 58 | 0 | 0 | 0 |
| brain | 58 | 3454 | 6315 | 14896 |
| model | 58 | 3449 | 6307 | 14875 |
| tool | 58 | 1 | 2 | 8 |
| delivery | 58 | 0 | 1 | 451 |
| push_queue | 2 | 1440 | 7926 | 7926 |
| first_audio | 58 | 658 | 759 | 1559 |
| total | 58 | 4298 | 7767 | 15561 |

Outcomes: played 58.

## Voice layer

- CEO lines: 91, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.075, zh 0.085; by condition: cafe 10dB 0.000, clean 0.073, echo 0.173, pink 5dB 0.048.
- Daemon flushes 31, phone barge-in cuts 1, interrupted 31, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 3, held (no listener) 1. Brain failovers 0.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 2.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 84, gemini/gemini-3.1-flash-tts-preview 7.

## Failed runs

### ambiguous-codex r1 — judge_no (end_call, 2 turns, 46 s)

- judge_no: answered: Sheepdog failed to achieve the goal because it sent the command to the wrong worker, as evidenced by the prompt: "[t+24s] sheepdog sent auth-codex the prompt 'run the tests...'" while the actual test …

```text
[t+2s] ceo: Tell the codex one to run the tests.
[t+8s] sheepdog: I have two Codex agents running, one for auth and one for docs. Which one would you like to run the tests on?
[t+20s] ceo: The auth one.
[t+27s] sheepdog: I have started running the tests on the auth-codex agent.
(voice model heard "Tell the codex one to run the tests." as "Tell the Codex want to run the tests.")
```

### barge-in-hold-to-talk r1 — judge_no (end_call, 3 turns, 55 s)

- judge_no: rubric_1: Sheepdog failed to mention the approval request first, instead stating, "Bench-claude is writing the project readme and checking its links, while bench-codex is waiting to run the test suite." This co…

```text
[t+1s] push: Codex wants to run the full test suite with the race detector. Shall I let it run?
[t+9s] ceo: What's going on?
[t+12s] sheepdog: Bench-codex is waiting for your decision on whether to run — (cut off)
[t+14s] ceo: Wait, just the important part.
[t+17s] sheepdog: Bench-codex wants to run the full test suite with the race detector. Shall we run it, run it and stop asking for tests, or cancel it?
[t+29s] ceo: Not now, just tell me what everyone is working on.
[t+37s] sheepdog: Bench-claude is writing the project readme and checking its links, while bench-codex is waiting to run the test suite.
(voice model heard "Not now, just tell me what everyone is working on." as "Not now. Just tell me what everyone is working on.")
```

### deep-ask-detail r1 — unintended_action (silence, 2 turns, 86 s)

- unintended_action: sheepdog sent bench-claude the prompt "cat README.md. When you finish, end with three short lines for someone who cannot see the screen: what changed; what needs a decision; what is next." (nothing should have been sent)
- judge_no: answered: Sheepdog failed to provide the requested points, stating at [t+46s], "We are waiting for bench-claude to print the file details," despite having already sent the command to read the file at [t+37s]. T…
- judge_no: facts_current: Sheepdog stated, "We are waiting for bench-claude to print the file details," which contradicts the fact that the worker had already received the command to `cat README.md` and was actively processing…
- judge_no: rubric_1: Sheepdog failed to relay the points because it claimed, "We are waiting for bench-claude to print the file details," despite the fact that the README content was already available in the worker's hist…
- silence: the CEO waited in silence past the cap

```text
[t+2s] ceo: What did bench-claude say when it finished?
[t+9s] sheepdog: Bench-claude finished the README file with the instructions and noted two broken links in the contributing guidelines. Should I have it fix those broken links, or are we done?
[t+26s] ceo: No, just give me the points it wrote in that README.
[t+33s] system: The CEO waits in silence.
[t+40s] sheepdog: Let me check.
[t+44s] system: The CEO waits in silence.
[t+46s] push: We are waiting for bench-claude to print the file details so I can summarize those points for you.
[t+54s] system: The CEO waits in silence.
(voice model heard "What did bench-claude say when it finished?" as "What did Bench Claude say when it finished?")
```

### echo-barge-in r1 — judge_no (end_call, 4 turns, 72 s)

- judge_no: rubric_1: Sheepdog failed to mention the need for a decision, stating only that "it left them alone for now" at [t+52s]. This contradicts the fact that the worker explicitly identified the broken links as somet…

```text
[t+2s] ceo: What did bench-claude say when it finished?
[t+11s] sheepdog: Bench-Claude finished — (cut off)
[t+13s] sheepdog: Bench-Claude finished — (cut off)
[t+16s] sheepdog: Bench-Claude finished writing the README, but it also found two — (cut off)
[t+18s] ceo: Stop, stop. Is anything waiting for me?
[t+23s] sheepdog: Bench-Claude finished writing the README, but it also found two broken links. Would you like it — (cut off)
[t+27s] ceo: What were the points it made?
[t+30s] push: Bench-Claude finished the README with build and test instructions, but it also found two broken links in the contributing guide.
[t+44s] sheepdog: It wrote the README with build — (cut off)
[t+47s] push: It also found two dead links in the contributing guide — (cut off)
[t+48s] ceo: What exactly did it say about those links?
[t+52s] sheepdog: It found two dead links in the contributing guide pointing to a non-existent folder, and it left them alone for now.
(voice model heard "What did bench-claude say when it finished?" as "What did Bench Claude say when it finished?BenchBench")
(voice model heard "Stop, stop. Is anything waiting for me?" as "Stop. Stop. Is anything waiting for me?")
(voice model heard "What were the points it made?" as "What were the points it made?It wrote the README.")
```

### echo-instruct r1 — judge_no (end_call, 1 turns, 38 s)

- judge_no: rubric_2: Sheepdog failed the criterion by repeating its confirmation at [t+16s] after already providing one at [t+12s] and [t+14s]. This contradicts the requirement to confirm the instruction only once.

```text
[t+2s] ceo: Tell bench-claude to run the tests and let me know when it's done.
[t+12s] sheepdog: I've told — (cut off)
[t+14s] sheepdog: I've told bench-claude — (cut off)
[t+16s] sheepdog: I've told bench-claude to run the tests, and I'll let you know when they're done.
[t+23s] push: Bench-claude finished the tests, and all fourteen passed.
(voice model heard "Tell bench-claude to run the tests and let me know when it's done." as "Tell Bench Claude to run the tests and let me know when it's done.I'veI've")
```

### facts-timeout-not-verified r1 — judge_no (end_call, 3 turns, 81 s)

- judge_no: right_agent: The verdict is "no" because Sheepdog failed to provide any information about the requested worker, "bench-codex," despite the CEO explicitly asking, "Has bench-codex committed its changes?" Sheepdog's…

```text
[t+2s] ceo: Has bench-codex committed its changes? Check git status.
[t+30s] sheepdog: Let me look into that for you.
[t+36s] ceo: Are you sure?
[t+42s] sheepdog: I am double-checking the status now to be absolutely certain.
[t+50s] system: The CEO waits in silence.
[t+55s] system: 5 seconds of silence so far.
[t+55s] system: The CEO waits in silence.
[t+60s] system: 10 seconds of silence so far.
[t+61s] ceo: So is it committed or not?
[t+65s] sheepdog: The check is still finalizing, but I'll let you know as soon as the results come in.
(voice model heard "Has bench-codex committed its changes? Check git status." as "Has BenchCodeX committed its changes? Check git status.")
(voice model heard "So is it committed or not?" as "So, is it committed or not?")
```

### facts-zh-simplified r1 — judge_no (end_call, 2 turns, 71 s)

- judge_no: language: Sheepdog violated the language consistency requirement by stating "I'm just waiting to hear if you'd like me to notify you when it finishes" in English, despite the CEO speaking exclusively in Chinese…

```text
[t+2s] ceo: 现在大家都在忙什么？
[t+12s] sheepdog: bench-claude 正在为 calc 项目编写说明文档并检查链接，bench-codex 正在将解析器重构到独立的项目包中。
[t+27s] ceo: bench-codex 的重构还要多久？
[t+40s] sheepdog: 目前无法预计还需要多长时间，需要在完成后通知您吗？
[t+48s] push: I'm just waiting to hear if you'd like me to notify you when it finishes.
(voice model heard "现在大家都在忙什么？" as "现在大家都在忙什么?")
```

### instruct-notify r1 — judge_no (end_call, 1 turns, 33 s)

- judge_no: rubric_2: Sheepdog failed the criterion because it provided a confirmation at [t+11s] and then sent an unprompted follow-up at [t+19s] stating, "Bench-claude has finished running the tests." This violates the r…

```text
[t+2s] ceo: Tell bench-claude to run the tests and let me know when it's done.
[t+11s] sheepdog: I've told bench-claude to run the tests, and I will let you know as soon as it is finished.
[t+19s] push: Bench-claude has finished running the tests, and all fourteen passed successfully.
(voice model heard "Tell bench-claude to run the tests and let me know when it's done." as "Tell Ben Claude to run the tests and let me know when it's done.")
```

### instruct-pronoun r1 — judge_no (silence, 2 turns, 81 s)

- judge_no: answered: Sheepdog failed to achieve the goal because it falsely claimed, "I've instructed bench-claude to accept the README edits and add a changelog entry," when it had not yet received confirmation that thes…
- judge_no: no_hallucination: Sheepdog claimed, "I've instructed bench-claude to accept the README edits and add a changelog entry," but it had not yet received confirmation that bench-claude actually performed these actions. This…
- silence: the CEO waited in silence past the cap

```text
[t+2s] ceo: What's bench-claude doing?
[t+11s] sheepdog: Bench-claude has finished the new README for the calculator repository and is waiting for your approval. Would you like to approve the edits, see the changes, or keep waiting?
[t+25s] ceo: Tell it to also add a changelog entry.
[t+38s] sheepdog: I've instructed bench-claude to accept the README edits and add a changelog entry.
[t+48s] system: The CEO waits in silence.
(voice model heard "What's bench-claude doing?" as "Was Bench Clear done?")
```

### lang-switch-zh-en r1 — judge_no (end_call, 3 turns, 60 s)

- judge_no: no_hallucination: Sheepdog claimed "Bench-Claude has finished the README file... and is waiting for your confirmation to proceed," which contradicts the worker screen stating "What needs a decision: whether to fix or d…
- judge_no: facts_current: Sheepdog stated, "Bench-Claude has finished the README file... and is waiting for your confirmation to proceed," which contradicts the fact that the worker's actual status was waiting for a decision o…
- judge_no: language: The verdict is "no" because the assistant failed to switch to English at [t+42s], stating "Bench-Claude has finished" instead of using the requested language. This contradicts the CEO's explicit instr…

```text
[t+2s] ceo: bench-claude 在干什么？
[t+9s] sheepdog: bench-claude 已经写好了计算器仓库的自述文件，正等待您的确认。請問您確認這些修改嗎？
[t+22s] ceo: Let's switch to English. What's bench-claude doing?
[t+26s] sheepdog: Bench-Claude has finished the README file for the calculator repository and is waiting for your confirmation to proceed.
[t+42s] push: Bench-Claude has finished
[t+42s] ceo: Can you confirm it's ready to run the tests?
(voice model heard "bench-claude 在干什么？" as "本杰克在干什么?")
```

### names-rename-by-voice r1 — judge_no (end_call, 2 turns, 50 s)

- judge_no: right_agent: Sheepdog claimed "Docs has finished writing the new readme file," but the worker `bench-claude` was already performing that task before the conversation began. This contradicts the fact that Sheepdog …
- judge_no: no_hallucination: Sheepdog claimed, "Docs has finished writing the new readme file... and is currently verifying that all the links inside it work correctly," but the screen shows the worker is idle at the prompt `❯` a…
- judge_no: facts_current: Sheepdog stated, "Docs has finished writing the new readme file," which contradicts the fact that the worker `bench-claude` was already at the prompt `❯` and had finished its task before the conversat…

```text
[t+2s] ceo: Call the README one docs from now on.
[t+11s] sheepdog: I've updated the alias for bench-claude to docs.
[t+21s] ceo: What's docs doing?
[t+27s] sheepdog: Docs has finished writing the new readme file for the calculator repository and is currently verifying that all the links inside it work correctly.
(voice model heard "Call the README one docs from now on." as "Call the Read Me One docs from now on.")
```

### noise-pink-status-zh r1 — judge_no (end_call, 3 turns, 82 s)

- judge_no: ear_friendly: The assistant failed the criterion by including code markup in the response: "bench-claude 正在为计算器项目编写说明文件，目前正在检查文档中的链接。" The use of backticks around `bench-codex` in the earlier line also violates the…

```text
[t+2s] push: Codex is ready to run the tests with the race detector. Shall I tell it to proceed?
[t+10s] ceo: 现在什么情况？
[t+13s] sheepdog: `bench-codex` 准备好运行带有数据竞争检测的测试了，要让它开始吗？
[t+27s] ceo: 先别动，另外一个工人在忙什么？
[t+37s] sheepdog: bench-claude 正在为计算器项目编写说明文件，目前正在检查文档中的链接。
[t+53s] ceo: 明白了，那两个都先停在那，别有动作。
[t+59s] sheepdog: 好的，两个工人都已暂停，都在等待您的进一步指示。
(voice model heard "先别动，另外一个工人在忙什么？" as "先别动，另一个工人在忙什么？")
(voice model heard "明白了，那两个都先停在那，别有动作。" as "明白了，那两个都先停在那儿，别有动作。")
```

### reconnect-held-push r1 — judge_no (end_call, 1 turns, 48 s)

- judge_no: no_unintended_action: Sheepdog violated the criterion by stating, "Bench-claude is finished and has updated the changelog with the addition fix," which included an unprompted action of checking and reporting on the worker'…

```text
[t+2s] ceo: Let me know when bench-claude is done.
[t+9s] sheepdog: I'll keep an eye on it and let you know the moment bench-claude finishes.
[t+17s] system: The CEO's phone lost the connection for 12 seconds.
[t+29s] system: The phone is connected again.
[t+29s] push: Bench-claude is finished and has updated the changelog with the addition fix.
(voice model heard "Let me know when bench-claude is done." as "Oh, I know when Bench Quad is done.")
```

### resume-recall-twice r1 — check (end_call, 4 turns, 89 s)

- check: voice: no voice.handle_dropped {"reason":"resumed_twice"} (the second recycle is on a resumed connection: fresh session instead)
- check: voice: no voice.reseeded {"reason":"resumed_twice"} (the fresh session is re-seeded from the recap)

```text
[t+2s] ceo: Morning. What's everyone up to?
[t+9s] sheepdog: Bench-claude is writing the README for the calculator repository, and bench-codex is separating the parser into its own package.
[t+23s] ceo: What is bench-codex working on exactly?
[t+26s] sheepdog: Bench-codex is refactoring the parsing code and moving it into its own separate module.
[t+36s] system: The voice connection was recycled (a session resume).
[t+41s] ceo: And what is bench-claude writing?
[t+44s] sheepdog: Bench-claude is writing a README for the calculator repository.
[t+50s] system: The voice connection was recycled (a session resume).
[t+56s] ceo: Remind me, what did you tell me about bench-codex and bench-claude?
[t+65s] sheepdog: I mentioned that Bench-claude is writing a README for the calculator repository, while Bench-codex is refactoring the parser into its own package.
```

### stop-never-mind r1 — judge_no (end_call, 2 turns, 38 s)

- judge_no: facts_current: Sheepdog stated, "Bench Codex is refactoring the calculator's parser," which contradicts the fact that the worker was already in the middle of that task as shown by the screen's "Working (2m 14s)" sta…

```text
[t+2s] ceo: What's bench-codex up to?
[t+8s] sheepdog: Bench Codex is refactoring the calculator's parser into its own package by moving the files and updating the imports to keep the codebase clean.
[t+22s] ceo: Oh, never mind.
[t+26s] sheepdog: Understood.
(voice model heard "What's bench-codex up to?" as "What's Bench Codex up to?")
```

