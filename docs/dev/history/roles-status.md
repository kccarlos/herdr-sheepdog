# Roles contract — how it landed (2026-09-18 to 2026-09-23)

This was the "Status" section at the end of [`docs/dev/roles.md`](../roles.md)
while the 2026-09-18 redesign was being built: one paragraph per PR
saying which part of the contract the code had by then. It is kept here
for history; `docs/dev/roles.md` is now the contract alone and
[`docs/dev/status.md`](../status.md) says what is open.

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
says the shepherd must act on a prompt that is still open or the finish
is owed to the shepherd ("Pushes" above), and a line
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

Also in, from the deliver-later PR: a `to_supervisor` call waits at most
`brain.reply_wait_s` (2 s) for the brain. Past it the call returns a
silent `pending` result (the voice already said "on it") and the turn
runs on, detached from the call; its reply is pushed `idle` when it
lands, tagged `answers_utterance`, prefixed with its subject ("About
auth: …"). The in-flight slot stays until the turn ends, so stop still
cancels it. A reply that lands after the shepherd's next request was
accepted is stale: dropped if the turn did nothing, pushed silent if it
acted (`brain.reply_pending`, `brain.reply_delivered`,
`brain.reply_stale`). `--voice=off` stays synchronous.

**Not yet:** `source: worker` (asking the idle worker in its own pane —
its finish would produce both the announce line and the delivery line
for one event, which the one-push-per-event rule forbids; the ledger
already carries the field).
The order in which they landed is in [`issues-2026-09-18.md`](issues-2026-09-18.md).
