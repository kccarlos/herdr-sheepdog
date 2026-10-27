# Live-status questions go to the orchestrator (2026-10-27)

CEO decision 2026-10-27: a question about live state ("有结果了吗",
"现在什么状态", "did X finish") always goes to the orchestrator; the voice
never answers it from memory. Raw runs and the runner:
[`../bench/results/2026-10-27-livestatus/`](../bench/results/2026-10-27-livestatus/).

## The failure (live log, 2026-10-27 PDT, Gemini Live voice, DeepSeek orchestrator)

- 23:14:36 "有结果了吗?" and 23:14:49 "此时已经有结果了。": the voice
  answered itself ("我正在帮您查看…", "我一直在密切关注…") with no
  `to_supervisor` call, so nothing was checked. The agent had already
  replied. The third ask was handed off, and the orchestrator found the
  answer in 1 s.
- 23:18:38 "现在是什么状态?": answered from an old briefing.
- 23:19:20 "刚才为什么你的语音突然停了?": got an invented cause
  ("可能出现了连接波动").

## The change

Prompt only (`prompts/assistant.md`, plus the `to_supervisor` description
in `internal/voice/gemini/tools.go`). The old wording had three gaps that
let the voice answer from a briefing:

- it handed off "anything **new** about an agent";
- the tool description said "anything you have **no fresh briefing** for";
- "why" did not cover the system itself.

The new sentence hands off every question about an agent's live state
(result, status, progress, finished or replied, what it is doing now)
every time, even when a briefing told it or the voice just said it was
checking, because a briefing is only what was true when it was sent. It
also hands off "why" anything happened, with the agents or with the
voice, and says never to guess a cause.

**No code rail.** Two kinds of rail were considered:

- A word list on the user's utterance ("is this a status question?") or
  on the voice's reply ("我正在帮您查看" without a call). Both are
  judgment in code, which the CEO ruled out on 2026-10-03.
- The physical signal: a turn the voice answered with no tool call. It
  cannot tell a status answer from small talk, so a daemon fact ("nothing
  was checked") would go out after every "thanks". It also could not fix
  the failures the eval does reproduce: each is the first answer to a
  question, which a later fact cannot change.

## Eval

Gemini Live itself, not the text model: `livecases.go.txt` (copy it to
`_livecases/main.go` and run it with `go run`) sends each assistant case
as client-content text turns to Vertex `gemini-3.8-live`. It uses the
daemon's voice prompt and the NON_BLOCKING tool declarations, and records
the first tool call. Briefings in the case context are bracketed
user-role text, as in the text assistant set, not function responses.
Each arm is a binary built from that arm's prompt and tool description.

The 11 new `livestatus-*` cases in `testdata/eval/assistant.jsonl` cover
Chinese and English, including a repeated "有结果了吗" after an "I'm
checking" reply. The 3 small-talk cases there (`none-*`) are the
over-hand-off control. Each case ran 3 times per arm.

| | main | fix |
|---|---|---|
| status questions handed off | 20/27 (74 %) | **27/27 (100 %)** |
| · "现在是什么状态?" / "What is docs-codex doing right now?" after a "working" briefing | 2/3, 0/3 | 3/3, 3/3 |
| · "刚才为什么你的语音突然停了?" | 0/3 (all three invented a cause: "网络延迟", "系统处理停顿") | 3/3 |
| · repeated "有结果了吗" / "Any result yet?" after "I'm checking" | 3/3, 3/3 | 3/3, 3/3 |
| small talk answered by the voice (`livestatus-*-thanks`) | 6/6 | 6/6 |
| `none-*` self-answer controls | 12/15 | 12/15 (same miss both arms: "Okay, let's use that then." handed off) |
| hand-off latency (to the tool call) | ~1.0 s | ~1.0 s |

- The repeated-question failure from the log did not reproduce in a
  short context: both arms hand it off 6/6. The log's session was long.
  What did reproduce is answering from an old briefing and inventing a
  cause, and the fix removes both in this set.
- The text-mode set (`sheepdog eval --set assistant --live`, gemini-3.5-flash)
  passed 25/27 status runs on main
  ([`text-flash-main.txt`](../bench/results/2026-10-27-livestatus/text-flash-main.txt)).
  It is too lenient to show this effect, which is why Live was used.
- Four logged utterances are offline regressions in
  `testdata/eval/regressions.assistant.jsonl`
  (`livestatus-logged-*`). Their logged behaviour (no call) fails them,
  so `make eval` proves at no cost that the cases catch it.
- Cost of the change: about 4 s more for each line that moves from a
  self-answer to a hand-off (10-04 measurement), only on status and
  "why" questions.

Gemini spend: 97 short Live sessions (Vertex) and 33 gemini-3.5-flash
calls. No tier-2 audio, TTS or judge was needed: the check is a tool
call, graded in code.

## Open

- `assistant-gptlive.md` was not changed; GPT-Live is not the default
  voice.
- Watch live logs for status questions the voice still answers itself.
  If they continue after long sessions, the physical signal above is the
  next thing to measure.
