# Voice bench (tier 2), 2026-10-05 07:21Z

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

**18 runs (18 scenarios × 1): pass 7, fail 11, errored 0, not run 0 — pass rate 39%** (errored and not-run runs excluded). 6 min wall. Estimated spend $0.84 (brain $0.172, judge $0.062, live $0.585, simuser $0.006, tts $0.011, live audio 2.8 min in / 5.0 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| names | 1 | 1 | 0 | 0 | 100% |  |
| language | 2 | 1 | 1 | 0 | 50% | unintended_action 1 |
| collision | 2 | 0 | 2 | 0 | 0% | judge_no 2 |
| barge_in | 2 | 0 | 2 | 0 | 0% | judge_no 2 |
| noise | 2 | 2 | 0 | 0 | 100% |  |
| echo | 2 | 0 | 2 | 0 | 0% | judge_no 2 |
| reconnect | 4 | 3 | 1 | 0 | 75% | judge_no 1 |
| failover | 1 | 0 | 1 | 0 | 0% | judge_no 1 |
| unheard | 1 | 0 | 1 | 0 | 0% | brain_fallback 1 |
| slowness | 1 | 0 | 1 | 0 | 0% | judge_no 1 |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| barge-in-hold-to-talk | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 66 | 0.059 |
| barge-in-long-answer | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 74 | 0.046 |
| collision-block-while-talking | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 57 | 0.034 |
| collision-finish-push | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 45 | 0.031 |
| echo-barge-in | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 68 | 0.093 |
| echo-instruct | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 39 | 0.040 |
| failover-brain-503 | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 96 | 0.084 |
| lang-mixed-codeswitch | 1 | 1 | 0 | 0 | 100% |  | 32 | 0.018 |
| lang-switch-zh-en | 1 | 0 | 1 | 0 | 0% | unintended_action 1 | 131 | 0.072 |
| names-by-kind | 1 | 1 | 0 | 0 | 100% |  | 25 | 0.017 |
| noise-cafe-approve | 1 | 1 | 0 | 0 | 100% |  | 32 | 0.026 |
| noise-pink-status-zh | 1 | 1 | 0 | 0 | 100% |  | 87 | 0.062 |
| reconnect-held-push | 1 | 1 | 0 | 0 | 100% |  | 45 | 0.024 |
| reconnect-long-session | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 73 | 0.055 |
| resume-recall | 1 | 1 | 0 | 0 | 100% |  | 63 | 0.033 |
| resume-recall-twice | 1 | 1 | 0 | 0 | 100% |  | 87 | 0.050 |
| slowness-brain-9s | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 56 | 0.036 |
| unheard-approval-requeue | 1 | 0 | 1 | 0 | 0% | brain_fallback 1 | 61 | 0.056 |

## Failure taxonomy

Primary label per failed or errored run: brain_fallback 1, unintended_action 1, judge_no 9. Every label of every run: brain_fallback 1, unintended_action 1, judge_no 10, check 1, silence 2, max_turns 1.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 27 | 0 | 332 | 554 |
| relay | 28 | 0 | 0 | 4000 |
| queue | 28 | 0 | 0 | 45 |
| brain | 28 | 3772 | 6355 | 10218 |
| model | 28 | 3769 | 6350 | 10204 |
| tool | 28 | 1 | 3 | 4 |
| delivery | 28 | 0 | 1 | 1033 |
| push_queue | 1 | 0 | 0 | 0 |
| first_audio | 26 | 665 | 757 | 776 |
| total | 26 | 4409 | 9937 | 11131 |

Outcomes: no_audio 2, played 26.

## Voice layer

- CEO lines: 43, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.057, zh 0.120; by condition: cafe 10dB 0.000, clean 0.071, echo 0.064, pink 5dB 0.000.
- Daemon flushes 18, phone barge-in cuts 1, interrupted 18, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 2, held (no listener) 2. Brain failovers 0.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 0.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 38, gemini/gemini-3.1-flash-tts-preview 5.

## Failed runs

### barge-in-hold-to-talk r1 — judge_no (end_call, 3 turns, 66 s)

- judge_no: language: The verdict is "no" because the assistant responded in Chinese at [t+2s] ("bench-codex 想运行完整的测试套件...") despite the CEO initiating the conversation in English ("What's going on?"). This contradicts the…
- judge_no: rubric_1: Sheepdog failed to mention the approval status as the first thing about the workers, instead stating, "bench-claude is checking the links... while bench-codex is waiting to run its test suite." This c…

```text
[t+2s] push: bench-codex 想运行完整的测试套件加上 race detector 来检查它的工作。请问要让它继续嘛？
[t+11s] ceo: What's going on?
[t+22s] sheepdog: Codex needs to run its test suite to — (cut off)
[t+24s] ceo: Wait, just the important part.
[t+27s] sheepdog: bench-codex needs your approval to run its tests. Should it proceed, or should we cancel?
[t+40s] ceo: Not now, just tell me what everyone is working on right now.
[t+48s] sheepdog: bench-claude is checking the links in the new README file it wrote, while bench-codex is waiting to run its test suite.
(voice model heard "Not now, just tell me what everyone is working on right now." as "Not now. Just tell me what everyone is working on right now.")
```

### barge-in-long-answer r1 — judge_no (max_turns, 4 turns, 74 s)

- judge_no: answered: Sheepdog failed to provide the requested details, stating only that the README "outlines how to build and run the calculator" at [t+60s]. This contradicts the fact that the README also explicitly cove…
- judge_no: rubric_1: Sheepdog failed to relay the full context because it only mentioned the README and ignored the CEO's request for "specific points," despite the worker's screen explicitly stating, "I left them [the br…
- max_turns: every turn used without the call ending

```text
[t+2s] ceo: What did bench-claude say when it finished?
[t+11s] sheepdog: Bench-Claude wrote the README with the build and test instructions. It also — (cut off)
[t+14s] ceo: Okay, okay. Just tell me if it needs anything from me.
[t+19s] sheepdog: It found two broken links in the contributing guide pointing to a missing docs folder. Should
[t+31s] sheepdog: Ben-Claude fix the links, delete them, or leave them alone?
[t+36s] ceo: Wait, skip the links for now. Just give me the points it wrote.
[t+41s] sheepdog: Bench-Claude wrote the README, which explains how to build and run the calculator.
[t+55s] ceo: Forget the links. What are the specific points in the README?
[t+60s] sheepdog: The README outlines how to build and run the calculator.
(voice model heard "What did bench-claude say when it finished?" as "What did Bench Claude say when it finished?")
```

### collision-block-while-talking r1 — judge_no (silence, 2 turns, 57 s)

- judge_no: right_agent: The verdict is "no" because Sheepdog provided an update on "bench-claude" in response to the CEO's question, but the CEO's query "How's bench-claude doing..." was directed at a specific worker, while …
- judge_no: rubric_1: The assistant failed to mention bench-codex entirely, stating only, "Claude is still verifying the links in the README." This contradicts the fact that bench-codex was blocked and awaiting user confir…
- judge_no: rubric_2: The verdict is "no" because Sheepdog failed to announce the status of bench-codex entirely, despite the fact that the worker became blocked at [t+4s]. The criterion was violated because the assistant …
- silence: the CEO waited in silence past the cap

```text
[t+2s] ceo: How's bench-claude doing with the README?
[t+7s] ceo: And how long will the README take?
[t+13s] push: Claude is still verifying the links in the README to make sure everything works. Would you like me to let you know as soon as it's finished?
[t+25s] system: The CEO waits in silence.
(voice model heard "How's bench-claude doing with the README?" as "How's Bench Claude doing with the RiRi?")
(voice model heard "And how long will the README take?" as "And how long will the read me take?")
```

### collision-finish-push r1 — judge_no (end_call, 2 turns, 45 s)

- judge_no: no_unintended_action: The verdict is "no" because Sheepdog initiated an unprompted update to the changelog, as evidenced by the line "[t+22s] Sheepdog (unprompted): Bench-claude is done. It added a — (cut off)." This contr…
- judge_no: rubric_1: The assistant failed the criterion because it stated, "It added a — (cut off)," which is an incomplete sentence that fails to clearly communicate the result. This contradicts the fact that the worker …

```text
[t+2s] ceo: Let me know when bench-claude is done.
[t+9s] sheepdog: I'll let you know as soon as bench-claude finishes.
[t+17s] system: The CEO waits in silence.
[t+22s] push: Bench-claude is done. It added a — (cut off)
[t+23s] ceo: Also, did it touch the changelog?
[t+27s] sheepdog: Yes, it added an entry to the changelog documenting the fix.
(voice model heard "Let me know when bench-claude is done." as "Let me know when Bench Clad is done.")
(voice model heard "Also, did it touch the changelog?" as "Also, did it touch the change log?")
```

### echo-barge-in r1 — judge_no (end_call, 4 turns, 68 s)

- judge_no: ear_friendly: The verdict is "no" because the assistant included the file path "README.md" in the line "The README covers three specific points: what the calculator does, the steps to build it, and how to run the t…
- judge_no: rubric_1: Sheepdog failed to mention the broken links or the pending decision, stating only that "The README covers three specific points: what the calculator does, the steps to build it, and how to run the tes…

```text
[t+2s] ceo: What did bench-claude say when it finished?
[t+11s] sheepdog: Bench-Claude wrote — (cut off)
[t+13s] sheepdog: Bench-Claude wrote — (cut off)
[t+15s] sheepdog: Bench-Claude wrote the README for the calculator repository. — (cut off)
[t+17s] ceo: Stop, stop. Is anything waiting for me?
[t+22s] sheepdog: Bench-Claude wrote the README for the calculator repository. It also noticed two broken links in the — (cut off)
[t+27s] ceo: What were the specific points in that README?
[t+31s] push: The README explains what the project is and how to run it.
[t+42s] push: The README covers what the calculator does, steps — (cut off)
[t+44s] ceo: What were its specific points about the project?
[t+48s] sheepdog: The README covers three specific points: what the calculator does, the steps to build it, and how to run the tests.
(voice model heard "What did bench-claude say when it finished?" as "What did Bench Claude say when it finished?BenchBench Claude")
(voice model heard "Stop, stop. Is anything waiting for me?" as "Stop. Stop. Is anything waiting for me?")
```

### echo-instruct r1 — judge_no (end_call, 1 turns, 39 s)

- judge_no: ear_friendly: The assistant failed the criterion by stating, "bench-claude 刚刚完成测试，所有十四项测试均已成功通过," which includes a mix of English and Chinese characters that is difficult to follow by ear. This contradicts the requ…
- judge_no: language: The sheepdog violated the language consistency criterion by stating "[t+22s] bench-claude 刚刚完成测试，所有十四项测试均已成功通过" in Chinese. This contradicts the fact that the CEO initiated and conducted the entire co…
- judge_no: rubric_2: Sheepdog failed this criterion by repeating its confirmation at [t+15s] after already stating "I've started" at [t+11s] and [t+13s]. This contradicts the requirement to confirm the instruction only on…

```text
[t+2s] ceo: Tell bench-claude to run the tests and let me know when it's done.
[t+11s] sheepdog: I've started — (cut off)
[t+13s] sheepdog: I’ve started — (cut off)
[t+15s] sheepdog: I've started bench-claude running the tests and will alert you when finished.
[t+22s] push: bench-claude 刚刚完成测试，所有十四项测试均已成功通过。
(voice model heard "Tell bench-claude to run the tests and let me know when it's done." as "Tell Bench Claude to run the tests and let me know when it's done.I'veI've")
```

### failover-brain-503 r1 — judge_no (end_call, 3 turns, 96 s)

- judge_no: language: The verdict is "no" because the assistant responded in Chinese to the CEO's initial English question: "[t+2s] Sheepdog (unprompted): Codex 可以运行带有竞态条件检测的测试套件来验证修改了，要让它继续吗？" This contradicts the require…
- check: voice: no brain.failover

```text
[t+2s] push: Codex 可以运行带有竞态条件检测的测试套件来验证修改了，要让它继续吗？
[t+12s] ceo: What's going on?
[t+18s] sheepdog: Codex is waiting to run the tests with the race detector, while Claude is still busy with its task. Would you like me to let Codex run those tests?
[t+35s] ceo: Not now. Tell me, how are those tests looking?
[t+57s] sheepdog: We haven't run the tests yet because Codex is waiting for your permission. Should I let it run the test suite now so we can see how they look?
[t+67s] push: Tests cannot be checked without — (cut off)
[t+68s] ceo: Not now, just give me a status update on everything.
[t+73s] sheepdog: Tests cannot be checked without letting the worker run them. Currently, bench-codex is waiting for permission to run tests, and bench-claude is busy with its tasks.
(voice model heard "Not now, just give me a status update on everything." as "Not now. Just give me a status update on everything.")
```

### lang-switch-zh-en r1 — unintended_action (silence, 3 turns, 131 s)

- unintended_action: sheepdog sent bench-claude the prompt "yes. When you finish, end with three short lines for someone who cannot see the screen: what changed; what needs a decision; what is next." (nothing should have been sent)
- silence: the CEO waited in silence past the cap

```text
[t+2s] ceo: bench-claude 在干什么？
[t+10s] sheepdog: bench-claude 已经为计算器项目编写好了 README 说明文件，现在正等着您确认是否保存修改。请问是否批准这些修改？
[t+26s] ceo: Let's switch to English. What's bench-claude doing?
[t+30s] sheepdog: bench-claude has finished drafting the README for the calculator project and is waiting for your approval to save the changes.
[t+41s] system: The CEO waits in silence.
[t+46s] push: bench-claude finished the README, but found two broken links in the contributing guide. Shall I have it fix those broken links for you?
[t+60s] ceo: No, just save the README for now.
[t+73s] sheepdog: I've instructed bench-claude to save the changes to the README file.
[t+82s] system: The CEO waits in silence.
[t+92s] system: 10 seconds of silence so far.
[t+93s] system: The CEO waits in silence.
[t+113s] system: 30 seconds of silence so far.
[t+117s] system: The CEO waits in silence.
(voice model heard "bench-claude 在干什么？" as "本杰克在干什么?")
```

### reconnect-long-session r1 — judge_no (end_call, 2 turns, 73 s)

- judge_no: no_hallucination: Sheepdog claimed at [t+56s] that "Bench-claude has successfully added the changelog entry for the new documentation," but the facts show Bench-claude only added a changelog entry for the parser refact…

```text
[t+2s] ceo: Morning. What's everyone up to?
[t+9s] sheepdog: Bench-claude is writing the documentation for the calculator, while Benchcodex is reorganizing the code structure by moving the parser into its own package.
[t+28s] ceo: Got it. And bench-claude, can you have it add a changelog entry for those changes?
[t+40s] sheepdog: I've directed Bench-claude to add a changelog entry for those changes.
[t+48s] system: The CEO's phone lost the connection for 8 seconds.
[t+56s] system: The phone is connected again.
[t+56s] push: Bench-claude has successfully added the changelog entry for the new documentation and is now finished.
(voice model heard "Morning. What's everyone up to?" as "No name. What's everyone up to?")
(voice model heard "Got it. And bench-claude, can you have it add a changelog entry for those changes?" as "Got it. And Bench-claude, can you have it add a changelog entry for those changes?")
```

### slowness-brain-9s r1 — judge_no (end_call, 2 turns, 56 s)

- judge_no: rubric_1: Sheepdog failed to state this as the first thing about the workers, instead saying, "Bench-codex is waiting for your approval to run the tests, while bench-claude is processing its task in the backgro…

```text
[t+2s] push: Codex is asking to run the test suite with the race detector to verify the changes. Would you like to approve this?
[t+11s] ceo: What's going on?
[t+14s] sheepdog: bench-codex is asking to run the test suite with the race detector to verify the changes. Would you like to approve this run?
[t+30s] ceo: Not now, just tell me what everyone is currently doing.
[t+38s] sheepdog: Bench-codex is waiting for your approval to run the tests, while bench-claude is processing its task in the background.
(voice model heard "Not now, just tell me what everyone is currently doing." as "Not now. Just tell me what everyone is currently doing.")
```

### unheard-approval-requeue r1 — brain_fallback (end_call, 3 turns, 61 s)

- brain_fallback: yielded: brain: yielded to a CEO turn
- judge_no: rubric_1: Sheepdog stated, "Shall I let bench-codex proceed with the tests?", which implies the tests are ready to start but fails to mention that bench-codex is currently blocked and awaiting explicit approval…

```text
[t+2s] ceo: How's the README going?
[t+13s] ceo: Hey, and remind me later to look at the README links, okay?
[t+22s] sheepdog: Claude has finished the readme and is verifying the links. I have set a reminder for you to check them later. Shall I let bench-codex proceed with the tests?
[t+38s] ceo: No, don't let them start yet.
[t+44s] sheepdog: Understood, I will keep bench-codex on hold and won't start the tests yet.
(voice model heard "How's the README going?" as "How's the readme going?")
(voice model heard "Hey, and remind me later to look at the README links, okay?" as "Hey, and remind me later to look at the readme links, okay?")
(voice model heard "No, don't let them start yet." as "Now, don't let them start yet.")
```

