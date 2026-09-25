# Dive deep: talking about one worker in detail

Design note, 2026-09-25. Status: proposal, not implemented. It answers
one complaint from the headset sessions of 2026-09-24: sheepdog gives a
good status line but cannot hold a conversation about what one worker
actually did ("what exactly did it say?", "tell me more", "why did it
do that?").

## What the log shows

Session 18:14–18:26 UTC, daemon `d0f79b9`, brain `deepseek-flash`.

1. Every detail question ran into the tool-call ordering bug (fixed in
   #62) and fell back to the roster sentence. That was the immediate
   blocker, and it is gone.
2. With the bug out of the way, the answers are still the wrong shape
   for a deep question, for three reasons:
   - **The material is thin.** The brain's only fresh source is
     `read_screen`: the last lines of a terminal pane, capped for the
     window. At 18:24:59 ("what exactly did the agent say?") the pane
     showed a TUI mid-render, with the worker's real message cut off.
     The worker's own session transcript (what it wrote for a human) is
     read only at a finish or block, into the journal (#57), and the
     brain cannot ask for it.
   - **There is no room to answer.** The daemon cuts `spoken` at two
     sentences and thirty words, whatever was asked. So the brain
     summarises seven glitches into "seven small quirks" and offers to
     go on, and the shepherd hears a menu instead of an answer.
   - **"Why" has no memory behind it.** `investigate` starts a fresh
     read-only CLI agent in the repository, which "has not heard this
     conversation" and never saw what the worker tried. The shepherd,
     by contrast, asks the worker itself a side question in its own
     pane and gets an answer from the worker's memory.

None of this needs a new mode, a state machine or a bigger model: the
brain's transcript already carries what the shepherd is focused on.
What is missing is better material, room to use it, and a way to ask
the worker.

## Proposal: three small changes, no new tool

### 1. `read_screen` can read the worker's session instead of its pane

`read_screen(agent, lines, source)` gains `source: "screen" | "session"`
(default `screen`, unchanged). With `session`, the host reads the
worker's own transcript through `internal/workerlog` (the reader the
journal already uses) and returns, inside the usual provenance
delimiters:

- the worker's last message for a human (`Digest.LastAssistant`), whole
  up to a cap of about 3,000 characters, oldest part cut first;
- the last instruction it received;
- the files it edited and its tool calls by name, one line.

It is local and takes milliseconds, so it fits inside an utterance
turn. The tool description says which source is for what: `screen` for
"what is it doing right now" and prompts; `session` for "what did it
say / find / change". Unsupported kinds (`agy`) and missing sessions
return the `worker.session_missing` reasons the journal already logs,
as an error the model can read. The result is pruned after
`brain.prune_after_turns` like any other tool result; `recall` still
finds it in the transcript.

Why not put the last message into the ledger of every prompt: four
agents at 400 characters each would add 1.6k characters to every call,
against what #59 just trimmed. On demand is cheaper.

### 2. Length is the prompt's call; the code cap becomes a safety net

Today `prose.EarLine` cuts an utterance reply at two sentences and
thirty words. That cap was a reaction to 15–25 s replies (#45, #55) and
it is right for a status line, but it makes a detail answer impossible.
The iteration-2 rule is to constrain with measurement, not with code,
so:

- the cap on an utterance reply rises to about four sentences and
  sixty words (roughly twenty seconds of speech), and stays a guard
  against a runaway reply, not a style rule. The caps on announce and
  finish lines (#60) do not change;
- the prompt (`RULES.md` intent and answer sections) says: brief by
  default, the same two sentences as today; when the shepherd asks for
  detail in their own words ("what exactly", "tell me more", "what were
  the N things", "go on"), relay the worker's own words in plain speech,
  in the worker's order, up to the cap.

No field, no per-reply parsing, nothing configurable. If status lines
drift longer, the scorecard's words-per-line column (J1, ≤ 20 words)
shows it on the next headset session.

### 3. `investigate` can ask the worker itself, with its memory

The shepherd's own way of understanding a worker is to ask it a side
question in its pane. The supervisor gets the same, without touching
the pane: `investigate` gains `source: "worker"`, which resumes the
worker's **own** session in a read-only, forked sub-process, and asks
the question there.

- The machinery exists. `internal/cli` already resumes sessions for
  follow-ups: `claude -p "<q>" --resume <session>` with the read-only
  flags, and `codex exec resume <thread> "<q>"` with
  `-c sandbox_mode=read-only` (docs/cli-query-tools.md §2b). The
  worker's session id is on the state row since #57 (herdr
  `agent_session`).
- The difference from today's `investigate`: the sub-agent starts with
  everything the worker saw and did, so "why did you skip the
  migration?" is answered from memory in one turn instead of a fresh
  read of the repository. It is still background work delivered as a
  push at the next pause, with the same one-job-per-agent rule, and it
  still cannot write.
- **Must be verified before building** (a spike, one run per backend,
  on a scratch session, not the shepherd's live worker):
  - Claude: resuming a session the TUI still has open must not write
    into the live session's JSONL. `--fork-session` (a new session id
    from the resumed one) is the safe form; confirm it exists in the
    installed version and that the fork is read-only.
  - Codex: whether `codex exec resume` of a thread the TUI is still
    running is allowed, and whether it forks or appends.
  - Cost and time: a long day's session is a large context; measure
    one resume (tens of seconds and the shepherd's own Claude/Codex
    usage, not Gemini quota) and cap the question with the existing
    `investigate_max_s`.
- This replaces the open item "source: worker types into the idle
  worker's pane" (docs/status.md): the fork never touches the pane, so
  the finish/announce collision that blocked it does not arise.

### What this does not do

- No `focus` state, no new tool, no model switch for deep turns
  (docs/roles.md keeps the brain's tool set small on purpose).
- No reading of the worker's full transcript into the window: the cap
  is the worker's last message. Its history is reached through
  `recall` over the journal, or by asking the worker (3).
- No writes by the sub-agent, ever; the rail is unchanged.
- No change to the voice tools or the iOS app: a longer `spoken` is
  still one string.

## How it is measured (iteration 2 rules)

- Eval cases from the real log: the utterances of 18:16:17 ("what's the
  small bug and what's the seven app glitches"), 18:16:41 ("I want to
  know the detail of the glitches") and 18:24:59 ("what exactly the
  agent say"), with a worker session fixture under `testdata/workerlog`.
  Pass: the brain reads `session`, the reply names the items the worker
  listed, and nothing is sent.
- Headset script (docs/iteration-2.md appendix) gains three lines after
  step 6: "What exactly did it say?", "Tell me more." and "Ask it why."
  Scorecard: answer within 5 s for the first two, no fallback, the
  answer at most sixty words, no repeat-ask within 60 s; the third is
  delivered as a push within `investigate_max_s`.
- Ear metrics unchanged for status replies, so J1 cannot regress
  unnoticed.

## Work packages

- **WP8a** `feat(brain): read_screen reads the worker's session`:
  `internal/brain/tools.go` (declaration, executor), the Host interface
  and `internal/supervisor/brainhost.go` (a `ReadSession(pane)` over the
  existing `workerlog.Reader`), `internal/workerlog` (a bounded
  last-message read that does not move the journal's offset),
  `prompts/svp/RULES.md` tools section, tests and the three eval cases.
- **WP8b** `feat(prompts): detail on request; the utterance cap is a
  safety net`: `internal/prose` cap for utterance replies,
  `prompts/svp/RULES.md`, eval cases; announce and finish caps pinned by
  a test.
- **WP8c** spike, then `feat(supervisor): investigate can ask the
  worker's own session`: the spike report first (one run per backend on
  a scratch session, findings into docs/cli-query-tools.md §2c); then
  `internal/cli` (fork template), `internal/supervisor/investigate.go`
  (`source: worker` resolves the pane's session id), `RULES.md`, tests
  with the CLI harness.
- **WP8d** docs: headset script lines, scorecard column, status.md.

WP8a and WP8b touch different files and go in one PR or two; WP8c
follows its spike; WP8d last.
