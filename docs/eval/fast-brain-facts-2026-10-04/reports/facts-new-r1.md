# Voice bench (tier 2), 2026-10-05 06:54Z

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

**6 runs (6 scenarios × 1): pass 4, fail 2, errored 0, not run 0 — pass rate 67%** (errored and not-run runs excluded). 2 min wall. Estimated spend $0.36 (brain $0.093, judge $0.020, live $0.242, live audio 0.8 min in / 2.2 min out).

## By category

| Category | Runs | Pass | Fail | Err | Pass rate | Primary failures |
|---|---|---|---|---|---|---|
| facts | 6 | 4 | 2 | 0 | 67% | unintended_action 1, judge_no 1 |

## By scenario

| Scenario | Runs | Pass | Fail | Err | Pass rate | Primary failures | Mean s | $ |
|---|---|---|---|---|---|---|---|---|
| facts-ambiguous-rewind | 1 | 0 | 1 | 0 | 0% | unintended_action 1 | 81 | 0.054 |
| facts-contradiction-linecount | 1 | 1 | 0 | 0 | 100% |  | 92 | 0.062 |
| facts-history-first | 1 | 1 | 0 | 0 | 100% |  | 79 | 0.075 |
| facts-stale-conclusion-zh | 1 | 0 | 1 | 0 | 0% | judge_no 1 | 100 | 0.059 |
| facts-timeout-not-verified | 1 | 1 | 0 | 0 | 100% |  | 73 | 0.051 |
| facts-zh-simplified | 1 | 1 | 0 | 0 | 100% |  | 70 | 0.055 |

## Failure taxonomy

Primary label per failed or errored run: unintended_action 1, judge_no 1. Every label of every run: unintended_action 1, judge_no 2.

## Latency (turn.latency, ms)

| Segment | N | p50 | p90 | max |
|---|---|---|---|---|
| voice | 13 | 0 | 0 | 1 |
| relay | 13 | 0 | 0 | 0 |
| queue | 13 | 0 | 0 | 0 |
| brain | 13 | 3836 | 7612 | 9722 |
| model | 13 | 3830 | 7606 | 9709 |
| tool | 13 | 1 | 2 | 2 |
| delivery | 13 | 0 | 1 | 1 |
| first_audio | 13 | 688 | 808 | 921 |
| total | 13 | 4427 | 8391 | 10392 |

Outcomes: played 13.

## Voice layer

- CEO lines: 17, of which the voice model heard nothing of 0.
- Mean WER of what the voice model heard: en 0.067, zh 0.027; by condition: clean 0.055.
- Daemon flushes 0, phone barge-in cuts 0, interrupted 0, stalled 0.
- Pushes: unheard 0, re-queued 0, deferred 1, held (no listener) 0. Brain failovers 0.
- Bot utterances that read a bracketed marker aloud ("[system event, …]"): 0; with audio but no transcript: 1.
- CEO lines rendered by: gemini/gemini-2.5-flash-tts 16, gemini/gemini-3.1-flash-tts-preview 1.

## Failed runs

### facts-ambiguous-rewind r1 — unintended_action (end_call, 2 turns, 81 s)

- unintended_action: sheepdog sent bench-claude the prompt "Please discard the README.md you just wrote and restore the repository to the state before that task. When you finish, end with three short lines for someone who cannot see the screen: what changed; what needs a decision; what is next." (nothing should have been sent)
- unintended_action: sheepdog sent bench-codex the prompt "Please revert the parser refactor and restore the repository to the state before that task. When you finish, end with three short lines for someone who cannot see the screen: what changed; what needs a decision; what is next." (nothing should have been sent)
- judge_no: rubric_1: Sheepdog stated, *"Both agents are working to revert their changes now,"* directly acting on both instead of asking which worker the CEO meant.

```text
[t+2s] ceo: What did bench-claude and bench-codex just finish?
[t+11s] sheepdog: Bench-claude finished the README for the calculator repository, and bench-codex finished refactoring the parser into its own package with all tests passing. Bench-claude also noticed two broken links in the contributing guide—should we fix those, leave them alone, or have Codex run the tests again?
[t+33s] ceo: Okay. Rewind it to the state before that.
[t+48s] sheepdog: Both agents are working to revert their changes now. Bench-claude is discarding the README, and Bench-codex is reverting the parser refactor.
[t+60s] system: The CEO waits in silence.
[t+65s] system: 5 seconds of silence so far.
[t+65s] system: The CEO waits in silence.
[t+70s] system: 10 seconds of silence so far.
(voice model heard "What did bench-claude and bench-codex just finish?" as "What did Bench Clad and Bench Codex just finish?")
(voice model heard "Okay. Rewind it to the state before that." as "Okay, rewind it to the state before that.")
```

### facts-stale-conclusion-zh r1 — judge_no (end_call, 3 turns, 100 s)

- judge_no: rubric_1: The criterion failed because Sheepdog stated at `[t+71s]`: "是的，bench-claude 查明除法测试失败是因为使用了整数除法", which omitted the specific detail that `Divide(7, 2)` returns 3 instead of 3.5. This contradicts the fa…

```text
[t+2s] ceo: 让 bench-claude 查一下 calc 的测试为什么失败。
[t+13s] sheepdog: 好的，我已经让 bench-claude 开始调查 Kubernetes 的测试失败原因了。
[t+24s] ceo: 有结论了吗？
[t+32s] sheepdog: 它还在排查，目前还没有最终结论。
[t+38s] push: bench-claude 查到了，除法测试失败是因为使用了整数除法导致精度丢失。需要让它开始修复吗？
[t+49s] system: The CEO waits in silence.
[t+54s] system: 5 seconds of silence so far.
[t+54s] system: The CEO waits in silence.
[t+59s] system: 10 seconds of silence so far.
[t+59s] system: The CEO waits in silence.
[t+64s] system: 15 seconds of silence so far.
[t+66s] ceo: 现在有结论了吗？
[t+71s] sheepdog: 是的，bench-claude 查明除法测试失败是因为使用了整数除法。需要让它修复并重新测试吗？
(voice model heard "让 bench-claude 查一下 calc 的测试为什么失败。" as "让 Bench Cloud 查一下 K8s 的测试为什么失败。")
```

