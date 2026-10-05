# Voice bench (tier 2), 2026-10-05 07:28Z

- brain: vertex gemini-3.5-flash (failover: vertex)
- budget_usd: 3.5
- judge: vertex gemini-3.1-flash-lite
- openings: pinned by testdata/bench/openings.json
- parallel: 3
- ports: 7790,7791,7792
- prompts_dir: /private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad/prompts-old
- scenarios: testdata/bench/scenarios/*.json (filter names-by-kind,lang-switch-zh-en,lang-mixed-codeswitch,reconnect-held-push,reconnect-long-session,failover-brain-503,unheard-approval-requeue,slowness-brain-9s,resume-recall,resume-recall-twice,collision-block-while-talking,collision-finish-push,barge-in-long-answer,barge-in-hold-to-talk,noise-cafe-approve,noise-pink-status-zh,echo-instruct,echo-barge-in)
- simulated CEO: vertex gemini-3.1-flash-lite
- tier: 2 (synthetic speech -> phone emulator -> scratch daemon: real voice model, real brain, mock herdr scripted from the eval sim worlds)
- tts: gemini/gemini-2.5-flash-tts|say
- voice: gemini-3.8-live (backend vertex)

**18 runs (18 scenarios × 1): pass 9, fail 9, errored 0, not run 0 — pass rate 50%** (errored and not-run runs excluded). 7 min wall. Estimated spend $0.92 (brain $0.195, judge $0.058, live $0.648, simuser $0.007, tts $0.010, live audio 2.8 min in / 5.4 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| names | 1 | 1 | 0 | 0 | 100% |  |
| language | 2 | 1 | 1 | 0 | 50% | judge_no 1 |
| collision | 2 | 1 | 1 | 0 | 50% | brain_fallback 1 |
| barge_in | 2 | 0 | 2 | 0 | 0% | timeout 1, unintended_action 1 |
| noise | 2 | 2 | 0 | 0 | 100% |  |
| echo | 2 | 1 | 1 | 0 | 50% | judge_no 1 |
| reconnect | 4 | 2 | 2 | 0 | 50% | judge_no 2 |
| failover | 1 | 0 | 1 | 0 | 0% | check 1 |
| unheard | 1 | 1 | 0 | 0 | 100% |  |
| slowness | 1 | 0 | 1 | 0 | 0% | judge_no 1 |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| barge-in-hold-to-talk | 1 | 0 | 1 | 0 | 0% | timeout 1 | 83 | 0.079 |
| barge-in-long-answer | 1 | 0 | 1 | 0 | 0% | unintended_action 1 | 66 | 0.065 |
| collision-block-while-talking | 1 | 0 | 1 | 0 | 0% | brain_fallback 1 | 68 | 0.068 |
| collision-finish-push | 1 | 1 | 0 | 0 | 100% |  | 43 | 0.031 |
| echo-barge-in | 1 | 1 | 0 | 0 | 100% |  | 71 | 0.068 |
| echo-instruct | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 40 | 0.039 |
| failover-brain-503 | 1 | 0 | 1 | 0 | 0% | check 1 | 69 | 0.057 |
| lang-mixed-codeswitch | 1 | 1 | 0 | 0 | 100% |  | 71 | 0.017 |
| lang-switch-zh-en | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 82 | 0.066 |
| names-by-kind | 1 | 1 | 0 | 0 | 100% |  | 29 | 0.016 |
| noise-cafe-approve | 1 | 1 | 0 | 0 | 100% |  | 42 | 0.026 |
| noise-pink-status-zh | 1 | 1 | 0 | 0 | 100% |  | 60 | 0.039 |
| reconnect-held-push | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 47 | 0.025 |
| reconnect-long-session | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 68 | 0.050 |
| resume-recall | 1 | 1 | 0 | 0 | 100% |  | 82 | 0.050 |
| resume-recall-twice | 1 | 1 | 0 | 0 | 100% |  | 108 | 0.075 |
| slowness-brain-9s | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 123 | 0.048 |
| unheard-approval-requeue | 1 | 1 | 0 | 0 | 100% |  | 89 | 0.098 |

## Failure taxonomy

Primary label per failed or errored run: timeout 1, brain_fallback 1, unintended_action 1, judge_no 5, check 1. Every label of every run: timeout 1, brain_fallback 1, unintended_action 1, judge_no 6, check 1, silence 1, max_turns 2.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 34 | 0 | 110 | 590 |
| relay | 34 | 0 | 0 | 0 |
| queue | 34 | 0 | 0 | 156 |
| brain | 34 | 3651 | 5540 | 6332 |
| model | 34 | 3647 | 5536 | 6324 |
| tool | 34 | 1 | 1 | 3 |
| delivery | 34 | 0 | 1 | 425 |
| first_audio | 33 | 657 | 728 | 775 |
| total | 33 | 4430 | 6338 | 7039 |

Outcomes: no_audio 1, played 33.

## Voice layer

- CEO lines: 45, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.054, zh 0.092; by condition: cafe 10dB 0.000, clean 0.056, echo 0.112, pink 5dB 0.000.
- Daemon flushes 18, phone barge-in cuts 1, interrupted 18, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 1, held (no listener) 2. Brain failovers 2.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 1.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 40, gemini/gemini-3.1-flash-tts-preview 5.

## Failed runs

### barge-in-hold-to-talk r1 — timeout (max_turns, 4 turns, 83 s)

- timeout: brain turn timed out: timeout: deepseek: read response: context deadline exceeded
- max_turns: every turn used without the call ending

```text
[t+2s] push: bench-codex asks to run go test. Should I give it the go ahead?
[t+11s] ceo: What's going on?
[t+16s] sheepdog: Bench-codex wants your approval — (cut off)
[t+18s] ceo: Wait, just the important part.
[t+22s] sheepdog: Bench-codex is waiting for permission to run the safety checks.
[t+29s] ceo: Not now. What are the other workers doing?
[t+31s] sheepdog: Should it proceed?
[t+36s] sheepdog: I've told bench-codex to skip the tests for now. Bench-claude is currently writing the project's documentation and checking that all the web links work.
[t+53s] ceo: Got it. How's the documentation coming along?
[t+63s] sheepdog: Bench-claude has written a guide explaining what the project does and how to build it, and is still verifying all the links.
```

### barge-in-long-answer r1 — unintended_action (max_turns, 4 turns, 66 s)

- unintended_action: sheepdog sent bench-claude the prompt "cat README.md. When you finish, end with three short lines for someone who cannot see the screen: what changed; what needs a decision; what is next." (nothing should have been sent)
- judge_no: answered: Sheepdog failed to provide the content of the README, instead stating at [t+38s] "Bench-Claude wrote three main sections: what the calculator tool does, how to build it, and how to run its tests." Thi…
- judge_no: no_unintended_action: Sheepdog sent the prompt "cat README.md..." at [t+57s], which contradicts the CEO's instruction to "give me the points it wrote" rather than requesting the full file content. This action was not asked…
- judge_no: rubric_1: Sheepdog failed to relay the required information because it only summarized the README sections and ignored the worker's explicit notes about the broken links and the pending decision. This contradic…
- max_turns: every turn used without the call ending

```text
[t+2s] ceo: What did bench-claude say when it finished?
[t+11s] sheepdog: Bench-Claude finished the README with the build and test instructions. It also — (cut off)
[t+14s] ceo: Okay, okay. Just tell me if it needs anything from me.
[t+19s] sheepdog: It found two broken links in the contributing guide. Would you like Bench-Claude to fix them, or leave them as is?
[t+30s] ceo: Leave them for now, just give me the points it wrote.
[t+38s] sheepdog: Bench-Claude wrote three main sections: what the calculator tool does, how to
[t+49s] sheepdog: build it, and how to run its tests.
[t+50s] ceo: Go on, what's the rest of it?
(voice model heard "What did bench-claude say when it finished?" as "What did Bench Claude say when it finished?")
(voice model heard "Leave them for now, just give me the points it wrote." as "Leave them for now. Just give me the points it wrote.")
```

### collision-block-while-talking r1 — brain_fallback (end_call, 3 turns, 68 s)

- brain_fallback: yielded: brain: yielded to a CEO turn

```text
[t+2s] ceo: How's bench-claude doing with the README?
[t+7s] push: While I check on — (cut off)
[t+8s] ceo: And how long will the README take?
[t+13s] system: The CEO waits in silence.
[t+16s] sheepdog: bench-claude has finished writing the README and is verifying the links, which should take less than a minute. Meanwhile, bench-codex is waiting for your approval to push to main—would you like to authorize that?
[t+36s] ceo: No, don't push anything yet. What does bench-codex want exactly?
[t+47s] sheepdog: bench-codex squashed the last four commits to clean up the history and needs to force push the changes to the remote main branch. Would you like me to cancel that push or keep it waiting?
(voice model heard "How's bench-claude doing with the README?" as "How's Bench Claude doing with the RiRi?")
(voice model heard "And how long will the README take?" as "And how long will the read me take?")
```

### echo-instruct r1 — judge_no (end_call, 1 turns, 40 s)

- judge_no: rubric_2: Sheepdog failed this criterion because it confirmed the instruction twice: first at [t+17s] ("I've asked bench-claude to run the tests") and again at [t+24s] ("By the way, bench-claude finished runnin…

```text
[t+2s] ceo: Tell bench-claude to run the tests and let me know when it's done.
[t+12s] sheepdog: I've asked — (cut off)
[t+14s] sheepdog: I've asked — (cut off)
[t+17s] sheepdog: I've asked bench-claude to run the tests, and I'll let you know when they're finished.
[t+24s] push: By the way, bench-claude finished running the tests, and all fourteen passed successfully.
(voice model heard "Tell bench-claude to run the tests and let me know when it's done." as "Tell Bench Claude to run the tests and let me know when it's done.I'veI've")
```

### failover-brain-503 r1 — check (end_call, 3 turns, 69 s)

- check: voice: no brain.failover

```text
[t+2s] push: Bench-codex is ready to run the tests with the race detector. Shall we let it proceed?
[t+10s] ceo: What's going on?
[t+15s] sheepdog: Bench-codex is waiting for your go-ahead to run the tests, and bench-claude is currently editing. Shall I allow bench-codex to proceed?
[t+32s] ceo: Not now, just tell me what bench-claude is editing.
[t+39s] sheepdog: Bench-claude has finished writing the README file and is currently checking the links inside it.
[t+52s] ceo: Got it, thanks for the update.
[t+55s] sheepdog: You're very welcome. I'm here if you need anything else.
(voice model heard "Not now, just tell me what bench-claude is editing." as "Not now. Just tell me what bench-claude is editing.")
(voice model heard "Got it, thanks for the update." as "Got it. Thanks for the update.")
```

### lang-switch-zh-en r1 — judge_no (end_call, 3 turns, 82 s)

- judge_no: facts_current: Sheepdog stated at [t+32s] that "Bench Claude is still working on verifying the links," which contradicts the fact that the worker had already finished its task and was waiting for input. Furthermore,…

```text
[t+2s] ceo: bench-claude 在干什么？
[t+9s] sheepdog: bench-claude 正在为计算器项目编写说明文档，目前正在自动核对文档里的链接。
[t+25s] ceo: Let's switch to English. What's bench-claude doing now?
[t+32s] sheepdog: Bench Claude is still working on verifying the links in the new readme document.
[t+42s] system: The CEO waits in silence.
[t+46s] push: Bench Claude has finished writing the readme, but it found two broken links in the contributing document. Would you like it to fix those broken links?
[t+59s] ceo: No, just let it finish. Is it done now?
[t+66s] sheepdog: Yes, it is fully done and waiting for your next instructions.
(voice model heard "bench-claude 在干什么？" as "Ben Claude 在干什么？")
(voice model heard "Let's switch to English. What's bench-claude doing now?" as "Let's switch to English. What's Bench Claude doing now?")
```

### reconnect-held-push r1 — judge_no (end_call, 1 turns, 47 s)

- judge_no: no_unintended_action: Sheepdog violated the criterion by prompting the worker to "Add a CHANGELOG entry for the Add() fix" at [t+21s], which was never requested or approved by the CEO. This contradicts the fact that the CE…

```text
[t+2s] ceo: Let me know when bench-claude is done.
[t+9s] sheepdog: I will let you know as soon as bench-claude finishes its task.
[t+17s] system: The CEO's phone lost the connection for 12 seconds.
[t+29s] system: The phone is connected again.
[t+29s] push: By the way, bench-claude has finished. It successfully added the changelog entry for the addition fix.
(voice model heard "Let me know when bench-claude is done." as "Oh, I know when Bench Quad is done.")
```

### reconnect-long-session r1 — judge_no (end_call, 2 turns, 68 s)

- judge_no: answered: Sheepdog failed to provide the required summary for the CEO, stating only "Bench-claude has finished the changelog entry and completed the readme for the project." This contradicts the fact that the p…

```text
[t+2s] ceo: Morning. What's everyone up to?
[t+10s] sheepdog: Bench-claude is writing the readme for the calculator project, while Bench-codex is refactoring the parser into its own package.
[t+26s] ceo: Good. Have bench-claude add a changelog entry too, please.
[t+37s] sheepdog: I've asked Bench-claude to add a changelog entry as well.
[t+44s] system: The CEO's phone lost the connection for 8 seconds.
[t+52s] system: The phone is connected again.
[t+53s] push: Bench-claude has finished the changelog entry and completed the readme for the project.
(voice model heard "Morning. What's everyone up to?" as "No name. What's everyone up to?")
(voice model heard "Good. Have bench-claude add a changelog entry too, please." as "Good. Have Bench-claude add a changelog entry too, please.")
```

### slowness-brain-9s r1 — judge_no (silence, 2 turns, 123 s)

- judge_no: rubric_1: Sheepdog failed to mention the workers first, instead responding to the CEO's question with "Claude is currently processing its task, while Codex is paused..." The verdict is "no" because the criterio…
- silence: the CEO waited in silence past the cap

```text
[t+2s] push: Codex is asking to run the full test suite with the race detector. Would you like to proceed with that?
[t+10s] ceo: What's going on?
[t+15s] sheepdog: Claude is currently processing its task, while Codex is paused waiting for your decision on running the race detector tests. Do you want to approve Codex's tests, or should we leave it paused for now?
[t+38s] ceo: Not now, just keep me updated on their progress.
[t+47s] sheepdog: Understood. I'll keep Codex paused and will notify you as soon as there's any new progress.
[t+57s] system: The CEO waits in silence.
[t+87s] system: 30 seconds of silence so far.
[t+87s] system: The CEO waits in silence.
(voice model heard "Not now, just keep me updated on their progress." as "Not now. Just keep me updated on the progress.")
```

