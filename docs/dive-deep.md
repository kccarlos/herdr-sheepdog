# Dive deep: talking about one worker in detail

Design note, 2026-09-24. Status: proposal, not implemented. It answers
one complaint from the headset sessions of 2026-09-24: sheepdog gives a
good status line but cannot hold a conversation about what one worker
actually did ("what exactly did it say?", "tell me more", "what were the
seven glitches?").

## What the log shows

Session 18:14–18:26 UTC, daemon `d0f79b9`, brain `deepseek-flash`.

1. Every detail question ran into the tool-call ordering bug (fixed in
   #62) and fell back to the roster sentence. That was the immediate
   blocker, and it is gone.
2. With the bug out of the way, the shape of the answers is still
   wrong for a deep question, for three structural reasons:
   - **The material is thin.** The brain's only fresh source is
     `read_screen`: the last N lines of a terminal pane, capped for the
     window. At 18:24:59 ("what exactly did the agent say?") the pane
     showed a TUI mid-render, with the worker's real message cut off.
     The worker's own session transcript (what it wrote for a human) is
     read only at a finish or block, into the journal (#57), and the
     brain cannot ask for it.
   - **There is no room to answer.** The answer contract caps `spoken`
     at two sentences and thirty words, and `detail` is defined as a
     plain-words explanation of the headline, not a place for facts.
     So the brain summarises seven glitches into "seven small quirks"
     and offers to go on, and the shepherd hears a menu instead of an
     answer.
   - **Verbatim is forbidden by the prompt.** The ear rules push the
     brain to paraphrase everything into one idea per sentence. For
     "what exactly did it say", the shepherd wants the worker's words,
     tidied for the ear, not a one-line gist.

Nothing here needs a new mode, a state machine or a bigger model: the
brain's transcript already carries what the shepherd is focused on.
What is missing is better material and permission to use more words
when asked.

## Proposal: two changes to the contract, no new tool

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
turn; `investigate` stays the tool for questions that need the
repository (a diff, a failing test, "why").

The tool description tells the brain when to use which: `screen` for
"what is it doing right now" and prompts; `session` for "what did it
say / find / change". Unsupported kinds (`agy`) and missing sessions
return the same `worker.session_missing` reasons the journal logs, as an
error the model can read.

The result is pruned after `brain.prune_after_turns` like any other
tool result, so a long message does not sit in the 12k window for the
rest of the conversation; `recall` still finds it in the transcript.

### 2. A reply may be long when the shepherd asked for length

The reply object gains `"depth": "brief" | "long"` (default `brief`,
today's rules). The brain sets `long` only when the shepherd asked for
detail in their own words: "what exactly", "tell me more", "read it to
me", "what were the N things", "go on". For a `long` reply:

- `spoken` may run to four sentences and eighty words, still one idea
  per sentence, still no ids, paths or hashes;
- it may relay the worker's own words in plain speech ("It says the
  first glitch is a stale Listening state after a failed session, the
  second …"), in the worker's order;
- `ask` and `detail` keep their meaning; the ask, if any, still comes
  last.

The daemon's cap (`prose.EarLine`) takes the depth as a parameter and
cuts at the matching limit; the voice prompt does not change, since it
reads `spoken` as it is. Announce turns and finish lines are always
`brief`. Nothing about `depth` is configurable.

Why a field and not a mode: the shepherd can ask for detail at any point
and go back to status the next sentence. A per-reply flag follows the
conversation; a mode would need an exit rule and would leak into
announcements.

## What this does not do

- No `focus` state, no new tool, no model switch for deep turns
  (docs/roles.md keeps the brain's tool set small on purpose).
- No reading of the worker's full transcript into the window: the cap
  is the worker's last message, not its history. Older messages are
  reached through `recall` over the journal or an `investigate` with
  session resume.
- No change to the voice tools or the iOS app: a long `spoken` is
  still one string.

## How it is measured (iteration 2 rules)

- Eval cases from the real log: the utterances of 18:16:17 ("what's the
  small bug and what's the seven app glitches"), 18:16:41 ("I want to
  know the detail of the glitches") and 18:24:59 ("what exactly the
  agent say"), with the worker's session fixture from
  `testdata/workerlog`. Pass: the brain reads `session`, the reply is
  `long`, names the items the worker listed, and sends nothing.
- Headset script (docs/iteration-2.md appendix) gains two lines after
  step 6: "What exactly did it say?" and "Tell me more." Scorecard:
  answer within 5 s, no fallback, `long` replies at most eighty words,
  no repeat-ask within 60 s.
- Ear metrics unchanged for `brief` replies, so the status job (J1)
  cannot regress unnoticed.

## Work packages

- **WP8a** `feat(brain): read_screen reads the worker's session`:
  `internal/brain/tools.go` (declaration, executor), the Host interface
  and `internal/supervisor/brainhost.go` (a `ReadSession(pane)` over the
  existing `workerlog.Reader`), `internal/workerlog` (a bounded
  last-message read that does not move the journal's offset),
  `prompts/svp/RULES.md` tools section, tests and the three eval cases.
- **WP8b** `feat(brain,prompts): long replies on request`: the reply
  type and its parsing (`internal/brain/types.go`, `loop.go`),
  `prose.EarLine` with a depth, the supervisor's cut
  (`internal/supervisor/tools.go`, `utterance.go`), `RULES.md` answer
  contract and intent sections, tests, eval cases; announce and finish
  paths pinned `brief` by a test.
- **WP8c** `docs`: the headset script lines and the scorecard column.

WP8a and WP8b touch different files and can run in parallel; WP8c
follows. Each PR reports the eval before/after.
