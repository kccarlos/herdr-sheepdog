# Dive deep: talking about one worker in detail

Design note, 2026-09-25. Status: implemented — WP8a and WP8b in #68,
WP8c in #69.

It answers one complaint from the headset sessions of 2026-09-24:
sheepdog gave a good status line but could not hold a conversation about
what one worker actually did ("what exactly did it say?", "tell me
more", "why did it do that?"). The material was thin (only the last
lines of a pane), the daemon cut every reply at two sentences, and
`investigate` started a fresh sub-agent that never saw what the worker
tried. None of this needs a new mode or a bigger model: the brain's
transcript already carries what the shepherd is focused on.

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

Before WP8b, `prose.EarLine` cut an utterance reply at two sentences and
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
- **What the spike found** (docs/cli-query-tools.md §2c; three short
  runs per backend on scratch sessions, before WP8c was built):
  - Claude `--resume <id> --fork-session` answers from memory into a
    new session id; the original JSONL is byte-identical afterwards,
    and the fork writes only its own file. Confirmed read-only and
    safe to build on.
  - Codex `codex exec resume` **appends to the original rollout**
    (34,518 → 41,423 bytes in the spike run), so it is never used on a
    worker's live session. `codex exec fork <id>` (codex-cli 0.155) is
    the non-appending form instead: a new thread whose rollout
    references the parent by byte offset, original untouched, sandbox
    read-only. This is a deviation from the brief above, which assumed
    Codex would be refused if resume appends — it is supported through
    `fork` instead.
  - Cost and time: 2–3.5 s and about $0.02 per run on the spike's tiny
    scratch sessions. A real day-long session's cost and time were not
    measured.
- The fork never touches the pane, so it cannot collide with the
  worker's own finish or announce.

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

- **WP8a** `feat(brain): read_screen reads the worker's session` —
  **done, #68**: `internal/brain/tools.go` (declaration, executor), the
  Host interface and `internal/supervisor/brainhost.go` (a
  `ReadSession(pane)` over the existing `workerlog.Reader`),
  `internal/workerlog` (a bounded last-message read that does not move
  the journal's offset), `prompts/svp/RULES.md` tools section, tests and
  the three eval cases.
- **WP8b** `feat(prompts): detail on request; the utterance cap is a
  safety net` — **done, #68** (same PR as WP8a): `internal/prose` cap
  for utterance replies, `prompts/svp/RULES.md`, eval cases; announce
  and finish caps pinned by a test.
- **WP8c** spike, then `feat(supervisor): investigate can ask the
  worker's own session` — **done, #69**: the spike report first (one
  run per backend on a scratch session, findings into
  docs/cli-query-tools.md §2c); then `internal/cli` (fork template),
  `internal/supervisor/investigate.go` (`source: worker` resolves the
  pane's session id), `RULES.md`, tests with the CLI harness.
- **WP8d** docs: headset script lines, scorecard column — **done**.
