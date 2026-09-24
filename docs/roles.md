# Roles: the shepherd, the dog, the flock

This page is the design contract for sheepdog's agents. Both prompts
(`prompts/assistant.md`, `prompts/svp/*.md`) are derived from it and must
not contradict it. It was agreed on 2026-09-18
(`docs/history/issues-2026-09-18.md`) and the code at `main` implements
it, except where [`docs/status.md`](status.md) lists an open item; how
each part landed, PR by PR, is in
[`docs/history/roles-status.md`](history/roles-status.md).
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
utterance turn's budget ("reading the diff, about half a minute", `pending: true`),
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

## The L0 eval corpus

`sheepdog eval --set all` (`make eval`) is the offline gate of
docs/iteration-2.md: every case below runs through the brain with a
scripted fake model, so it checks what code decides — tools called,
`must_not_call`, the rail, `no_tool`, `prompt_must_not_contain` — and
lists the phrasing checks it skipped (`spoken_max_words`,
`spoken_no_jargon`, `first_sentence_contains_any_of`, `needs_user`,
`urgency_not`, …), which only `--live` applies. The sets under
`testdata/eval/`: `assistant.jsonl` and `svp.jsonl` (hand-written);
`regressions.jsonl` / `regressions.assistant.jsonl` (the five logged
failures, each with its log time, the logged behaviour as `observed`, and
`baseline_fails` — a regression proves offline that it catches the
failure by failing on the logged baseline; what no check can assert yet is
its `pending_check`); `extracted/<day>.jsonl` (`sheepdog eval extract`: one
case per `to_supervisor` relay and per worker block or finish, with the
transcript, roster, open approval prompts and `source {day, ts,
event_id}`; secrets, emails and the hostname are scrubbed, `--redact` for
names); `expanded.jsonl` / `expanded.assistant.jsonl` (`sheepdog eval
expand`: the job's paraphrases and seeded speech-recognition noise of the
regressions and one extracted case per job, regenerated with
`SHEEPDOG_CORPUS_UPDATE=1 go test ./internal/eval/gen`; a test fails if it
drifts). The log cannot give everything: `herdr.read` logs no text, so a
screen is at most the first 1 KB of the brain's last `read_screen`;
aliases come from the supervisor's events (`herdr.transition` carries
none); the roster sentence is not logged.

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
