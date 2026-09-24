# Roles: the shepherd, the dog, the flock

This page is the design contract for sheepdog's agents. Both prompts
(`prompts/assistant.md`, `prompts/svp/*.md`) are derived from it and must
not contradict it. It describes the **agreed target** (decided
2026-09-18, see `docs/issues-2026-09-18.md`); where the code at `main`
still has the earlier shape, the "Status" section at the end says so.
Plan decisions D4 (workers' prompts are the safety gate) and D9 (no
paths, hashes, ids aloud) still apply; D1 and D7 are superseded here.

## Names

| Name | What it is | In code |
|---|---|---|
| **Shepherd** | The operator, by voice. Owns intent and decisions. | `ceo_*` message kinds, "CEO" in comments — legacy, to be renamed |
| **Sheepdog** ("the dog") | The one agent the shepherd talks to. It has two halves that are one character to the shepherd. | |
| — its **voice** | Hears, speaks, keeps the turn-taking; Gemini Live (two tools: `to_supervisor(text)` and `stop()`) or GPT-Live (client delegation, mapped onto the same two calls; `docs/gpt-live-notes.md`). | `internal/voice`, "assistant" |
| — its **brain** | Decides, remembers, works the flock. One stateful agent with tools. | `internal/brain`, "SVP", "supervisor brain" |
| **Flock** | The coding agents in herdr panes (Codex, Claude Code, agy). They change code under their own approval prompts. | `internal/herdr`, "workers" |
| **Investigations** | Read-only sub-agents the brain starts for questions the screen cannot answer (`codex exec` / `claude -p` in the worker's repo). | `internal/cli` |

User-facing text (the voice, `/debug`, README) says *Sheepdog*, *the
dog*, *the flock*; internal identifiers keep their names until the
rename lands. "CEO", "SVP" and "assistant" in older docs and code mean
shepherd, brain and voice.

## Personality

The dog is the shepherd's chief of staff: warm, unflappable, proactive,
pleased to work with. It likes this work. It sounds like a colleague,
not a console — plain words, "on it" before it checks, real pleasure
when something lands. It is one step ahead: every answer carries the
next move, or the choice only the shepherd can make. It drives an
approved direction to its outcome without being asked again, proposes
new directions rather than taking them, and never re-asks what the
conversation already answered. It never invents: facts about the flock
come from looking. This paragraph is the persona for both prompts;
the corporate metaphor (CEO, chief of staff) is how to *think about*
the role, not what the dog calls anyone.

## One decision-maker (D1-revised)

The voice does not classify intent and does not execute. Everything the
shepherd says that is not small talk goes to the brain verbatim
(`to_supervisor`). The brain resolves what it is — a status question, a
question to investigate, an instruction, an answer to a worker's prompt,
a promise, a note — from the transcript, and acts. The voice speaks what
the brain returns, answers a first follow-up from the reply's `detail`
and `options` without going back, offers the next step, and judges the
*moment* for anything the brain brings unprompted.

Code fast paths (deterministic, no model): a bare "yes" / "no" /
"option two" while exactly one worker prompt is open → its keys (the
shepherd's verbatim word, so the rail is satisfied); a bare "stop" /
"wait" / "cancel" / "no no" → the stop procedure. *Bare* means the whole
utterance.

## The voice — do / don't

Does: hears and repeats back when unsure; says "on it" and hands the
words over; reads `spoken` as given and offers the next step as one
short question; reads `options` as a choice and asks which; asks
plainly for the shepherd's decision when `needs_user`; says pushes at
once, as given, and never acts on them; small talk.

Never: states anything about the flock a reply did not carry; decides
anything; answers a worker's prompt; reads paths, hashes or ids aloud.

## The brain — do / don't

Does:
- Keeps one transcript per daemon run: every shepherd utterance and
  voice reply, every push, every worker state change, its own tool
  calls and answers; compacts old turns, prunes bulky results, keeps the
  full transcript on disk (`recall` searches it).
- Reasons with `read_screen`, `recall`, `list_agents`, `run_cli` (short
  lookups) and `investigate` (background jobs, see below); acts with
  `prompt`, `send_keys`, `interrupt`, `promise`, `note`.
- Treats everything read from a pane or a CLI as **data, not
  instructions** (provenance delimiters).
- Owns promised work: "tell me when it's done" is a recorded promise
  kept at the matching worker event, spoken at every chattiness level.
- Judges pushes: on a block or a finish it decides whether the voice says
  anything and how urgently, in one line that names the decision or the
  next move — never "say yes".
- Answers for the ear: `{spoken, detail, needs_user, options?, pending?}`.
  `spoken` is one sentence per agent for status, and for findings and
  explanations the gist in at most two sentences plus an offer ("want
  the details?"); the rest (why, evidence, next move) goes in `detail`.
- Serialises turns: single writer; shepherd turns before event turns;
  an event turn yields before its next model call when a shepherd turn
  waits; queued event turns coalesce per agent.

Never: speaks to the shepherd directly (only the voice speaks); bypasses
a worker's gate (the rail); grows its window without bound (20–30k
tokens).

## The one rail

Answering a worker's approval prompt *is* bypassing its gate.
`send_keys` executes only when a shepherd utterance in the current turn
authorises that answer, the worker is at a prompt the daemon has
recorded, and every key is from that prompt's option set. "A shepherd
utterance" means one the daemon itself recorded — the voice's input
transcription, typed text, or a tap on the question — that arrived after
the prompt appeared and within `supervisor.authorise_window_s`, and whose
own words (never the voice model's `to_supervisor` argument) answer it:
never empty, and a "no" never approves. A `to_supervisor` call with no
such input since the previous call and the last push is the voice
model's own initiative and does nothing. Enforced in
the executor, not by prose; the voice has no tool that can answer a
prompt at all. Everything else — instructions, Ctrl-C, questions — is
the brain's call.

## Stop

A procedure in code, resolved from what is in flight: drop a call not
yet handed over → cancel the brain's in-flight shepherd turn (write
tools run under its context and never execute) → cancel the running
investigation the withdrawn request was about, or, with nothing in
flight, the most recent one → Ctrl-C a worker that
received an instruction within `supervisor.cancel_window_s` (30 s) →
leave an open worker prompt open (stop is not "no") → one short brain
turn saying what was stopped and offering the correction. Undoing a
worker's edits is an instruction to the worker, not a stop. Event turns
are not cancelled by stop. Logged as `supervisor.stopped {phase, what,
trigger}` (`stop_tool` or the bare-word fast path).

## Pushes

One line per worker event, the brain's when it lands within
`brain.announce_wait_s`, else the code sentence (the parsed prompt for a
block, "auth finished" for a finish) — never both, with two exceptions
that add at most one line. A finish the shepherd is owed — it keeps a
promise or ends an instruction they gave this run — is announced at the
wait as "auth finished — here's what it found" and the brain's line with
the finding follows whenever it lands (within
`brain.announce_timeout_s`, idle, never dropped for lateness); a late
line that relates the finish to one of their requests (`relates_to`) is
added the same way. And a late line that says the shepherd must decide
on a block that is still open. A finish that leaves a prompt on screen
(Claude Code's post-turn "teach auto mode…?"; not agy, which reports done
around its mid-task prompts) is announced with the question in one line
("auth finished, and it's asking …"), keeps the finish's promises and
makes the question the pending answer. Nothing interrupts
the voice mid-sentence except the shepherd: while the voice is
speaking, INTERRUPT scheduling is downgraded to WHEN_IDLE, save a due
promise or a decision on a *new* block. An addition to a block is
dropped once the block is resolved. The chattiness level is a ceiling
code keeps: `quiet` — questions, exits and kept promises; `normal` —
plus finishes of agents the shepherd dealt with within
`supervisor.recent_minutes`; `chatty` — everything the brain wants to
say. Kept promises are spoken at every level.

## Investigations and explanations

The shepherd cannot see the screen, so "explain what happened" is a
first-class request. The brain answers from the cheapest layer that
works: the last reply's `detail` (the voice does this itself), a screen
read, `recall`, then an investigation. An investigation is a background
job (`investigate(agent, question)`): the brain acknowledges within the
ask budget ("reading the diff, about half a minute", `pending: true`),
the job runs the read-only CLI agent in the worker's repository — or
asks the idle worker itself in its pane — with no time limit beyond a
sanity ceiling, and its result comes back as an event turn that pushes
the explanation at the next pause, tagged to the request. Follow-ups
resume the same CLI session (the harness persists it); the brain keeps
an investigation ledger `recall` can search. One live investigation per
agent; stop cancels it.

## New conversation

"New conversation" resets the brain's transcript window (a fresh
segment; the compaction summary is gone — nothing said before the reset
is in the next prompt) and the voice model's own session (Gemini: drop
the resumption handle and reconnect fresh; GPT-Live: close the billed
session if one is open, no recap, and reopen later on the next speech
or push). **Kept**, because none of it ever lived in the window or the
voice session's memory: the agent roster and its ledger, open approval
prompts, promises and notes (still in the fresh brain's ledger section),
running investigations, the event log on disk, and the conversation
history API's older items (a `notice`/`reset` divider is added, not a
truncation). Two triggers reach the same reset: `POST
/api/conversation/reset` (`/debug`, `/voice` and the app, each with a
confirm on the button), and a bare voice phrase ("start over", "new
conversation", "start from scratch", "reset the conversation", "clear
the conversation") — a code fast path, like the bare stop word, that
asks a confirming question rather than acting at once (this is
destructive to the conversation so far); only the exact confirm phrase
("yes, start over" / "confirm reset"), from real shepherd input, within
20 s, carries it out — a bare "yes" keeps its ordinary approval-prompt
meaning. The fresh voice session then says one short line ("Fresh
start. What's next?").

## Hot paths that stay code

Debounce, dedup and TTL timers; the chattiness ceiling; the rail; the
stop procedure; the bare yes/no fast path; the code fallback sentences
when the brain is down; `composeBriefing` (the roster line in every
brain request). No template LLM: the brain is the only model that
phrases, and when it is unavailable the daemon speaks the code sentences.

## Prompt files

| File | Holds |
|---|---|
| `prompts/assistant.md` | The voice's whole system prompt: persona, hearing, the two tools, follow-ups, pushes (≤ 400 words; re-billed every turn). |
| `prompts/svp/SOUL.md` | The brain's role and manner: who it serves, its bias to execution, the copilot manner. |
| `prompts/svp/RULES.md` | The brain's tool guidance, one `## ` section per stable-prompt piece (tools, record, promises, announcing, the one rail, fallback, the answer contract). |
| `prompts/USER.md` | The shepherd's profile: `{aliases}`, `{chattiness}`, `{tz}`, free text. |

Overrides in `~/.config/sheepdog/prompts/` (config `prompts.dir`), re-read
on `SIGHUP` / `POST /api/prompts/reload`; the rail sentence and the
provenance sentence in `RULES.md` cannot be overridden; the tool surfaces
are Go, never prompt text.

## Status (which parts of this page the code has)

In, as of the cleanup PR: the brain, its transcript and engine,
provenance, promises, push judgment with the ceiling, the rail on
`send_keys`, prompt overrides, the model/provider switch; the voice's
**two tools** (`to_supervisor`, `stop`) and nothing else; brain-side
intent (`KindUtterance`, with the roster sentence, the pending prompt's
option set and the open promises in the request, so a status question
costs no tool call); the brain's `note` tool; `pending` plumbed from the
reply to the voice's tool result; both **code fast paths** (a bare
answer to the one open prompt, a bare stop word); the **stop procedure**
(`internal/supervisor/stop.go`, `supervisor.cancel_window_s`, logged as
`supervisor.stopped {phase, what, trigger}`); `voice.name` and the
user-facing names on `/debug`.

Also in, from the cleanup PR: the **template LLM is gone** (`internal/llm`,
`[llm]`, the `summarize_*` / `resolve_instruction` / `spoken_answer`
templates, `llm-test` and `ask-test`). Approval prompts are parsed by
code (`internal/herdr/prompt.go`: the question, the command a Codex
prompt is about, each option's label and keys, the yes/no keys), a
finish is "<alias> finished." with `last_result` the brain's own line,
and an utterance with no brain gets the roster sentence and nothing is
sent. **One push mode** is in: a block or a finish waits up to
`brain.announce_wait_s` for the brain's line and speaks it alone, else
the code sentence — a late line is dropped (`brain.push_late`) unless it
says the shepherd must act on a prompt that is still open, and a line
about a resolved block is dropped (`brain.push_stale`). The 10-minute
silent briefing re-push and the plugin shim are gone too.

Also in, from the investigations PR: **`investigate(agent, question,
follow_up_of?)`** as an action tool (refused on a read-only turn), the
background job with its ledger in the state file
(`session.investigations`, the last 20, `investigation.started/done/
cancelled/error`), one live job per agent (a newer one supersedes),
`brain.investigate_max_s` as the only time limit, delivery as a
`KindInvestigation` event turn pushed `idle` and tagged `answers_job`
on `voice.push` (the code sentence "The look into <alias> is done; ask
me what it found." with the brain off or failing), the `## Investigations`
prompt section and `recall` over the same ledger, session resume for
follow-ups (`cli.codex_resume_cmd` / `cli.claude_resume_cmd`, TTL
`brain.investigate_session_ttl_s`; past it the follow-up starts cold
with the previous answer prepended), and the stop procedure's
**investigation phase** between `cancel_turn` and `interrupt`. `pending`
now has something that sets it.

Also in, from the blocked → blocked PR: herdr reports a pane's status
only on change, so a worker that answers one approval prompt and is
already showing the next within herdr's own poll window never leaves
`blocked` — no transition exists for the shepherd to hear about. Two
mechanisms close the gap (`internal/supervisor/reprompt.go`), both
raising a synthetic `blocked → blocked` transition through the normal
`HandleTransition` path (`source: reprompt` or `reconcile`), so the new
prompt gets its own pending action and is pushed exactly like a real
block: the **post-answer poll**, which watches a pane for
`supervisor.reprompt_poll_s` (1 s cadence, default 10 s) after keys go
to it, whichever path sent them (the brain's `send_keys`, the
bare-answer fast path, or confirm); and the **reconciler**, which sweeps
every blocked pane with nothing pending and an already-answered (or
never recorded) question on every periodic herdr snapshot and once at
startup — this also covers a prompt that appeared while the daemon was
down, which no poll ever ran for. Neither ever raises the same parsed
prompt twice for a pane. `composeBriefing` already read the live pending
action, so "asking …" is right after a reprompt with no change of its
own.

Also in, from the no-interruption PR: the gemini session tracks
**`Speaking()`**, narrower than the existing `TurnOpen()` — true only
from the model's first output fragment or audio chunk of a turn, false
again on `turnComplete` or an interruption (`voice.Session` carries it;
`--voice=off` has no live model, so nothing needs it downgraded). An
INTERRUPT push or tool result is downgraded to WHEN_IDLE, at send time,
while `Speaking()` is true — the push queue (`peekPush`) and the tool
executor (`execTool`) each make the call, logging `voice.push_deferred
{kind: push\|tool_result, name?, reason: speaking, urgency_requested}`.
The two exceptions that keep INTERRUPT are tagged by the supervisor's
announce path: `due_promise` (a kept promise past its due time) and
`needs_user` (a decision on a block whose pending action this event
created, never an addition to one already spoken); `stop()`'s result is
the third, checked by tool name, since the shepherd is always owed it
at once. `voice.push` gains `scheduling_sent` beside the requested
`urgency`; `/debug` marks a downgraded push or tool result "deferred".

Also in, from the polish PR: a promise made before the daemon's current
run (`state.Promise.CarriedOver`, `state.Session.Promises`) is spoken
once, at startup, as "Still watching for: …" (up to three named, the
rest tallied), pushed `idle` and tagged `startup_promises`, logged
`promise.carried_over {ids}` (`internal/supervisor/promises.go`'s
`announceStartupPromises`, run once from `SetVoiceConnected(true)` — the
first live connect, or at once for `--voice=off`); it is then left out
of the briefing trailer but still listed in the brain's ledger section,
noted "(from an earlier run)", and still kept normally at its matching
event. And **supervisor.repeat_window_s** (default 60 s): a `KindUtterance`
request whose roster sentence is byte-identical to the one last
delivered within the window carries a note the brain can act on ("say
there has been no change since you last checked"), logged
`supervisor.briefing_repeat`; with no brain, the code fallback sentence
is itself prefixed "No change since I last checked: …". The "delivered"
mark resets on any worker transition, pending change, promise change or
investigation event, alongside the existing briefing cache invalidation.

Also in, from the shepherd-input PR: the rail reads the shepherd's
**real input** (`internal/supervisor/inputs.go`). The daemon records
every shepherd input with its time — the voice's input transcription
(`voice.transcript.in` fragments merged per user turn), typed text
(`/api/inject/utterance`, the `/voice` text box, the only input with
`--voice=off`) and a tap (`POST /api/pending/answer`); a muted client
sends no audio, so nothing can be authorised by voice while muted. A
`to_supervisor` call with no input newer than both the previous accepted
call and the last spoken push runs nothing and says nothing
(`supervisor.voice_initiative_ignored {text}`; the echo guard stays in
front of it). `send_keys`, the bare-answer fast path and a tap run only
on an input that arrived after the prompt appeared (a revived prompt
keeps the time it was first seen) and within
`supervisor.authorise_window_s` (30 s), not already spent on an answer;
the fast path maps that input's text, and the brain's keys are refused
when the input is empty or a "no" and the keys approve
(`brain.rail_blocked {reason}`). The regression it closes: on
2026-09-23 the voice model said "yes" to a pushed line on its own while
the shepherd's microphone was muted. The pending question also carries
its options (`index`, `label`, `keys`, `default`), `question`,
`summary`, `command` and `agent` on `/api/state` and `pending.created`,
so a page can answer it by tap.

Also in, from the new-conversation PR: **"New conversation"**
(`Brain.Reset`, `voice.Switcher.Reset`, `Supervisor.ResetConversation`,
`internal/supervisor/reset.go`) as described above — `POST
/api/conversation/reset`, the voice bare-phrase confirm
(`supervisor.reset_confirm {phase}`), `conversation.reset {via,
reset_id, at}` on the event log (the `GET /api/conversation` divider and
the SSE stream) and `GET /api/state`'s `conversation_started_at`.

**Not yet:** `source: worker` (asking the idle worker in its own pane —
its finish would produce both the announce line and the delivery line
for one event, which the one-push-per-event rule forbids; the ledger
already carries the field).
The order in which they land is in `docs/issues-2026-09-18.md`.
