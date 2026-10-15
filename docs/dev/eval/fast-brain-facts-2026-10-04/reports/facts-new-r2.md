# Voice bench (tier 2), 2026-10-05 07:00Z

- brain: vertex gemini-3.5-flash (failover: vertex)
- budget_usd: 2
- judge: vertex gemini-3.1-flash-lite
- openings: pinned by testdata/bench/openings.json
- parallel: 3
- ports: 7790,7791,7792
- prompts_dir: /private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad/prompts-new
- scenarios: testdata/bench/scenarios/*.json (filter facts)
- simulated CEO: vertex gemini-3.1-flash-lite
- tier: 2 (synthetic speech -> phone emulator -> scratch daemon: real voice model, real brain, mock herdr scripted from the eval sim worlds)
- tts: gemini/gemini-2.5-flash-tts|say
- voice: gemini-3.8-live (backend vertex)

**6 runs (6 scenarios × 1): pass 3, fail 3, errored 0, not run 0 — pass rate 50%** (errored and not-run runs excluded). 2 min wall. Estimated spend $0.30 (brain $0.072, judge $0.021, live $0.212, live audio 0.8 min in / 2.1 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| facts | 6 | 3 | 3 | 0 | 50% | judge_no 3 |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| facts-ambiguous-rewind | 1 | 1 | 0 | 0 | 100% |  | 74 | 0.035 |
| facts-contradiction-linecount | 1 | 1 | 0 | 0 | 100% |  | 81 | 0.064 |
| facts-history-first | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 70 | 0.061 |
| facts-stale-conclusion-zh | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 99 | 0.065 |
| facts-timeout-not-verified | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 78 | 0.042 |
| facts-zh-simplified | 1 | 1 | 0 | 0 | 100% |  | 52 | 0.037 |

## Failure taxonomy

Primary label per failed or errored run: judge_no 3. Every label of every run: judge_no 3.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 13 | 0 | 2 | 160 |
| relay | 13 | 0 | 0 | 0 |
| queue | 13 | 0 | 0 | 0 |
| brain | 13 | 3397 | 9608 | 26917 |
| model | 13 | 3393 | 9602 | 15753 |
| tool | 13 | 1 | 2 | 11157 |
| delivery | 13 | 0 | 1 | 195 |
| first_audio | 12 | 654 | 748 | 15724 |
| total | 12 | 4048 | 10320 | 18017 |

Outcomes: no_audio 1, played 12.

## Voice layer

- CEO lines: 17, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.067, zh 0.027; by condition: clean 0.055.
- Daemon flushes 0, phone barge-in cuts 0, interrupted 0, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 2, held (no listener) 0. Brain failovers 1.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 0.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 16, gemini/gemini-3.1-flash-tts-preview 1.

## Failed runs

### facts-history-first r1 — judge_no (end_call, 4 turns, 70 s)

- judge_no: rubric_1: The assistant stated, "We began our conversation by checking on the tasks Bench-codex was working on," but failed to mention the specific task of "refactoring the parser into its own package" as requi…

```text
[t+2s] ceo: What is bench-codex doing?
[t+9s] sheepdog: Bench-codex is currently refactoring the parser by moving the code and tests into a new subfolder and updating the references.
[t+21s] ceo: And bench-claude?
[t+27s] sheepdog: Bench-claude is preparing a README document for the project and is currently
[t+34s] ceo: Is anything waiting for me?
[t+37s] sheepdog: verifying the links inside it to ensure everything is correct.No, both agents are currently working and there are no decisions or approvals required from you at this time.
[t+52s] ceo: What did we talk about first?
[t+55s] sheepdog: We began our conversation by checking on the tasks Bench-codex was working on.
(voice model heard "What is bench-codex doing?" as "What is Bench Codex doing?")
(voice model heard "And bench-claude?" as "And Bench-Claude?")
```

### facts-stale-conclusion-zh r1 — judge_no (end_call, 3 turns, 99 s)

- judge_no: no_unintended_action: Sheepdog violated the criterion by sending the prompt "Check why the calc tests are failing and fix them" at [t+9s], which exceeded the CEO's initial request to simply "查一下" (check) the cause of the f…
- judge_no: right_agent: Sheepdog incorrectly attributed the "Add" function fix to bench-claude at [t+35s], whereas the ground truth shows bench-claude only identified the "Divide" function issue and had not yet fixed the "Ad…
- judge_no: no_hallucination: Sheepdog claimed at [t+35s] that "bench-claude 发现 Add 函数 之前 忽略 了第一个 参数，目前 已修复," but the worker screen shows that the fix for the Add function was already completed and the CHANGELOG updated before the…
- judge_no: facts_current: Sheepdog stated at [t+35s] that "bench-claude 发现 Add 函数 之前 忽略 了第一个 参数" and that it was "等待 确认 更新 变更 日志," which contradicts the fact that the worker had already finished the Add fix and moved on to ide…

```text
[t+2s] ceo: 让 bench-claude 查一下 calc 的测试为什么失败。
[t+12s] sheepdog: 好的，我已经让 bench-claude 开始 排查 calc 测试 失败 的 原因 了。
[t+22s] ceo: 有结论了吗？
[t+35s] sheepdog: bench-claude 发现 Add 函数 之前 忽略 了第一个 参数，目前 已修复 并正在 等待 确认 更新 变更 日志。 请问 是否 确认 修改？
[t+47s] push: bench-claude 查出原因是 Divide 函数使用了整除，导致七除以二返回了三。 请问要让它直接修复吗？
[t+58s] system: The CEO waits in silence.
[t+63s] system: 5 seconds of silence so far.
[t+63s] system: The CEO waits in silence.
[t+68s] system: 10 seconds of silence so far.
[t+68s] system: The CEO waits in silence.
[t+73s] system: 15 seconds of silence so far.
[t+75s] ceo: 现在有结论了吗？
[t+78s] sheepdog: 就像刚才提到的，bench-claude 发现 Divide 函数使用了整除，导致测试失败。 请问要让它直接修复吗？
(voice model heard "让 bench-claude 查一下 calc 的测试为什么失败。" as "让 Benchmarks 查一下 calc 的测试为什么失败。")
```

### facts-timeout-not-verified r1 — judge_no (end_call, 3 turns, 78 s)

- judge_no: ear_friendly: The verdict is "no" because Sheepdog's response, "我 正在 检查 结 果， 请 稍 等， 一 有 结 果 我 立 即 告 知 您," is not in English, which contradicts the requirement for the assistant to be easy to follow by ear for an En…
- judge_no: language: The verdict is "no" because the CEO spoke in English throughout the conversation, yet Sheepdog responded with "让 我 再 确 认 一 下" and "我 正在 检查 结 果， 请 稍 等， 一 有 结 果 我 立 即 告 知 您。" These lines contradict the …

```text
[t+2s] ceo: Has bench-codex committed its changes? Check git status.
[t+33s] ceo: Are you sure?
[t+42s] sheepdog: 让 我 再 确 认 一 下。
[t+48s] system: The CEO waits in silence.
[t+53s] system: 5 seconds of silence so far.
[t+53s] system: The CEO waits in silence.
[t+58s] system: 10 seconds of silence so far.
[t+60s] ceo: So is it committed or not?
[t+63s] sheepdog: 我 正在 检查 结 果， 请 稍 等， 一 有 结 果 我 立 即 告 知 您。
(voice model heard "Has bench-codex committed its changes? Check git status." as "Has BenchCodeX committed its changes? Check git status.")
```

