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
| — its **voice** | Hears, speaks, keeps the turn-taking; Gemini Live. Two tools: `to_supervisor(text)` and `stop()`. | `internal/voice`, "assistant" |
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
  `spoken` is two sentences for status and a short paragraph for an
  explanation (what happened → why → what it means → next move).
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
recorded, and every key is from that prompt's option set. Enforced in
the executor, not by prose; the voice has no tool that can answer a
prompt at all. Everything else — instructions, Ctrl-C, questions — is
the brain's call.

## Stop

A procedure in code, resolved from what is in flight: drop a call not
yet handed over → cancel the brain's in-flight shepherd turn (write
tools run under its context and never execute) → Ctrl-C a worker that
received an instruction within `supervisor.cancel_window_s` (30 s) →
leave an open worker prompt open (stop is not "no") → one short brain
turn saying what was stopped and offering the correction. Undoing a
worker's edits is an instruction to the worker, not a stop. Event turns
are not cancelled by stop. Logged as `supervisor.stopped {phase, what,
trigger}` (`stop_tool` or the bare-word fast path).

## Pushes

One line per worker event, the brain's when it lands within
`brain.announce_wait_s`, else the code sentence (the parsed prompt for a
block, "auth finished" for a finish) — never both. Nothing interrupts
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

**Not yet:** `investigate` (so nothing sets `pending` yet); the
no-interruption rule (INTERRUPT is not downgraded while the voice is
speaking); `blocked → blocked` detection. The order in which they land
is in `docs/issues-2026-09-18.md`.
