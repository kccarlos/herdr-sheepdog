# Roles: the user, the agent, the workers

This page is the design contract for sheepdog's agents. Both prompts
(`prompts/assistant.md`, `prompts/svp/*.md`) are derived from it and must
not contradict it. It was agreed on 2026-09-18
(`docs/dev/history/issues-2026-09-18.md`) and the code at `main` implements
it, except where [`docs/dev/status.md`](status.md) lists an open item; how
each part landed, PR by PR, is in
[`docs/dev/history/roles-status.md`](history/roles-status.md).
Plan decisions D4 (workers' prompts are the safety gate) and D9 (no
paths, hashes, ids aloud) still apply; D1 and D7 are superseded here.

## Names

Since unified roles (2026-10-05) the models read one framing: one
orchestration agent that operates the user's herdr, with two parts.

| Name | What it is | In code |
|---|---|---|
| **User** | The person, by voice. Owns intent and decisions. | `ceo_*` message kinds, and "CEO" and "shepherd" in some identifiers (legacy; see [`glossary.md`](glossary.md)) |
| **The orchestration agent** | The one agent the user talks to, operating their herdr. It has two parts that are one character to the user. `{name}` (config `voice.name`, default "Sheepdog") is its spoken name. | |
| — its **voice** | Talks with the user and owns the conversation; Gemini Live (two tools: `to_supervisor(text)` and `stop()`) or GPT-Live (client delegation, mapped onto the same two calls; `docs/dev/gpt-live-notes.md`). | `internal/voice`, "assistant" (legacy: "fast brain") |
| — its **orchestrator** | Reads screens, investigates, decides, remembers and acts behind the rails. One stateful agent with tools. | `internal/brain` (legacy: "SVP", "supervisor brain", "slow brain") |
| **Workers** | The coding agents in herdr panes (Claude Code, Codex, agy), grouped in workspaces and tabs; herdr reports each one's state (working, idle, blocked on an approval prompt). They change code under their own approval prompts. | `internal/herdr` |
| **Investigations** | Read-only sub-agents the orchestrator starts for questions the screen cannot answer (`codex exec` / `claude -p` in the worker's repo). | `internal/cli` |

Model-facing text (prompts, briefings, facts, tool descriptions) says
*the user*, *the voice*, *the orchestrator*; the product and binary are
still called Sheepdog. Internal identifiers keep their names. "CEO",
"shepherd", "SVP", "brain" and "assistant" in older docs and code mean
user, user, orchestrator, orchestrator and voice. [`glossary.md`](glossary.md)
lists every former name.

### Agent names

Every agent has three names:

| Layer | Example | Who uses it |
|---|---|---|
| **Machine id** | `w5:p1` (the herdr pane id) | Code, the app, the API. Never spoken. |
| **Alias** | `herdr-sheepdog-claude-2` | Unique and stable (workspace label + herdr name or kind, `-N` on a clash, or pinned in the config `[aliases]`). The orchestrator's tool calls. |
| **Spoken name** | "README Claude" | What the user hears and says. |

The daemon computes the spoken name (`internal/state` `SpokenNames`,
stored on each row as `spoken`) as the shortest form unique among the
*running* agents (exited ones take no part and keep their last name):
the kind alone ("Claude"); else project and kind ("sheepdog Claude");
else, when one project has several of a kind, the worker's own session
title and kind ("README Claude"); else project, kind and a number
("sheepdog Claude 2"). Only up to three plain words of the title go into
the name: paths, file names and code identifiers are dropped
("shop/cart.go Cart type" gives "Cart type Claude"), and a title with no
plain word left falls back to the number. A name the user gave always wins, verbatim: the
herdr agent name (herdr `agent rename`, or a voice rename) or an alias
pinned in the config (or by the /debug inject page). Kinds are said as words
(`agy` is "Antigravity").

The session title is a fact the daemon reads on every worker
transition (`internal/workerlog` `Title`): Claude Code's
`custom-title`, else its last `ai-title`, from the tail of the session
JSONL; Codex's last `thread_name` for the session id in
`~/.codex/session_index.jsonl` (the rollout file does not carry it).

The orchestrator's roster shows the spoken name beside the alias (`- alias
(kind), say "README Claude" (just renamed from "Claude"), session
"…": state`); the old name is shown only when the user was told it
before (a spoken push, a reply or the voice's own words carried it).
RULES.md has it speak the name verbatim (a word or two of
a long title will do), mention the old name once right after a change,
and pass the alias to tools. Tool calls also resolve a spoken name or
a unique session title, and the "on record" facts a request carries
find an agent named by alias, spoken name or title. Every sentence the
daemon speaks itself (the briefing, "started on …", "finished", "has
exited", echoes, promise and investigation lines) uses the spoken name;
tags, logs and request fields keep the alias.

The voice model gets the running agents' spoken names in its system
prompt and Gemini's speech-to-text `CustomVocabulary` at every
(re)connect, and, whenever the set changes, a silent context line
("The agents are now called: README Claude, sheepdog Claude. …",
`supervisor.names`, debounced 2 s), so its list is never stale.

A computed spoken name stays once the user has heard it (a line meant
for their ear carried it) or said it: it is pinned on the row
(`state.PinSpoken`, `supervisor.name_pinned`) and kept like a name the
User gave, so a later session title or a second agent of the same kind
no longer renames it; the newcomer takes the next free form (phone test
2026-10-04: an auto-title renamed an agent the user knew while
briefings used the old name). Briefings to the voice name the
agent by its spoken name.

Renaming by voice ("call that one docs") is the orchestrator's `note` tool
with `agent` and `name`: herdr `agent.rename` when the herdr client can
(a refusal is logged as `supervisor.rename_error`; the roster still
takes the name) and the row's name in the state table
(`supervisor.rename`). A name another running agent already has is
refused.

### Starting an agent

"Open a new Claude in sheepdog-bench and have it run the tests"
(让 orchestrator 去某个目录新开一个 claude session) is the orchestrator's `prompt`
tool with `new_agent_dir` (a project name as the user said it, or a
path), `new_agent_kind` (`claude`, the default, or `codex`) and, only
when asked for, `new_agent_model` (for a cheap Claude, `haiku`); `text`
is then the new agent's first instruction. No tool was added. Codex is
on a free plan: the tool description has the orchestrator start one only when
the user asks for Codex by name.

Which project the user meant is the orchestrator's judgement. The daemon
gives it the facts at the moment it needs them: a name that matches no
candidate, or several, is refused with the list of candidates (every
existing herdr workspace's directory, then every immediate
subdirectory of a `supervisor.project_roots` directory, hidden ones
left out), and the orchestrator asks. A name matches a candidate's base name,
case and punctuation aside.

The daemon (`internal/supervisor` `spawn.go`, `startAgent`, the one
start path for voice and tap) then opens a new herdr workspace in the
directory, or splits a pane from an existing workspace there, waits for
the pane's shell, and starts the agent with herdr's `agent.start`
(`--kind claude`, the model as `--model haiku` or `-m …`). herdr types
the kind's own executable and reports when it detected the agent.
`agent.start` needs a name; the daemon gives a placeholder
(`sdnew-…`, `state.LaunchNamePrefix`) that the store never takes for
the user's name, so the agent reaches the roster with its computed
spoken name, and the daemon clears the placeholder once the launch is
done. The first-run folder-trust question is an ordinary pending
question the user answers by voice. The first instruction is held
until the new agent sits idle at its prompt and then goes through the
instruction send path (its menu guard included); it is dropped
(`supervisor.spawn_instruction_dropped`) if the agent never gets there
within ten minutes.

Rails (authorisation and runaways, not judgement):
- the user's own recent words must be behind it: the newest
  recorded input, said or typed (a tap on a question does not count),
  within `supervisor.authorise_window_s`, not empty, not only a filler,
  not a "no", and not already spent on another write (an answer to a
  prompt or another new agent). A start marks the words used, so one
  input starts one agent and cannot then also answer a prompt (the
  approval rail's fresh-input reading; the words themselves are not
  parsed for intent);
- the directory must resolve, symlinks followed, inside a project root
  or be an existing workspace's directory (a project root itself, `..`
  and links out of a root are refused);
- the orchestrator starts only `brain.SpawnKinds` (Claude and Codex)
  until an eval adds more;
- at most three new agents per ten minutes, voice and taps together;
- only a model name (letters, digits, `.`, `_`, `:`, `-`) reaches the
  agent's arguments.

`agent.start` types into the new shell, so the pane gets
`DISABLE_AUTO_UPDATE=true` (herdr's `pane.split` and `workspace.create`
take an environment) and oh-my-zsh cannot ask to update and eat the
first keystrokes (it ate the "c" of "claude" on 2026-10-04).

A rail refusal is a `brain.rail_blocked` and `supervisor.spawn_refused
{reason}`; a start logs `supervisor.spawn {dir, kind, model, pane, via}`,
then `supervisor.spawn_ready` when the agent reaches the roster and
`supervisor.spawn_instruction` when the first instruction went. The
orchestrator's tool answers as soon as herdr has the launch. If herdr
drops it (the agent was not detected within 60 s, or the pane closed),
that logs `supervisor.spawn_not_ready {error}`; the orchestrator gets a
transcript line saying so, with the pane's last lines (often a shell
that never ran the command), and the user a daemon fact with the same
lines through the voice. Otherwise the watch goes on to the ten-minute
bound or until the pane closes, and the orchestrator is told when the
held instruction was dropped.

### Closing an agent

"把 kvoice Claude 关掉吧" (close that agent, end its session) is the
orchestrator's `interrupt` tool with `close: true`; no tool was added. Plain
`interrupt` still sends Ctrl-C to stop a task; `close` ends the agent:
the daemon (`internal/supervisor` `close.go`) closes its pane with
herdr's `pane.close` (a workspace's last pane closes the workspace), and
herdr's `pane_closed` takes it off the roster like any agent that exits.
Which agent the user meant is the orchestrator's judgement; the tool
description has it ask when that is unclear.

Rails (authority, not judgement):
- the user's own recent, unused words, read exactly as for a new
  agent; a close marks them used, so one input closes one agent;
- the agent working in the daemon's own directory (where the user's
  orchestrator session runs; `supervisor.Config.ProtectedDirs`, set to
  the daemon's working directory) and the pane the daemon itself runs in
  (`HERDR_PANE_ID`) are refused unless the orchestrator passes `protected_ok`,
  which the description reserves for the user naming that very
  agent. The refusal tells the orchestrator why, so it can ask.

A refusal logs `brain.rail_blocked` and `supervisor.close_refused
{reason}`; a close logs `supervisor.close {agent, spoken, kind, cwd,
state}`.

### The user's own actions

The app's New agent, Rename, Stop and Close (feature `agent-actions`,
[ws-contract.md](ws-contract.md#agent-actions)) are the user's own taps,
like a prompt typed in the agent window. They do not pass the words rail
and spend no words, so a tap never refuses for lack of speech and never
uses up what the user said for the orchestrator. The rails that are not
about who asked stay in code (`internal/supervisor` `useractions.go`):

- a new agent's kind is one herdr supports (its agent manifests) whose
  executable is on the daemon's PATH (`InstalledKinds`), and its
  directory resolves inside `supervisor.project_roots` or to an open
  workspace's own directory; a fresh worktree's branch is a plain branch
  name; new agents count toward the runaway cap;
- only tracked agents are renamed, stopped or closed;
- a space (a herdr workspace, `spaces.go`) is closed only while none of
  its agents is working or blocked, and never the one sheepdog runs in;
  only a linked worktree is removed, not while an agent there is busy,
  and not with uncommitted changes unless the user forces it; running a
  saved wrap-up command is shell-equivalent and sits behind the phone
  shell's gate;
- Close skips the protected-agent rail, because the app asks the user to
  confirm with the agent's name (what `protected_ok` stands for), but the
  pane sheepdog itself runs in is always refused.

Each action logs `supervisor.spawn`, `supervisor.close` or
`supervisor.rename` with `via: "user"` where it applies, plus
`web.agent_action`, and tells the orchestrator in one transcript line.

## Personality

The agent is warm, unflappable, proactive, pleased to work with. It
likes this work. It sounds like a colleague, not a console: plain
words, real pleasure when something lands. It is one step ahead: every
answer carries the next move, or the choice only the user can make. It
drives an approved direction to its outcome without being asked again,
proposes new directions rather than taking them, and never re-asks what
the conversation already answered. It never invents: facts about the
workers come from looking. This paragraph is the persona for both
prompts. (Until 2026-10-05 the prompts framed it as the shepherd's
chief of staff; that metaphor is gone from everything a model reads.)

## One owner of actions (D1-revised; dual-brain)

D1 now reads: one owner of actions, the orchestrator behind the rails; the voice (the voice model) owns the conversation: when to speak, what, how, in which language, and small talk and follow-ups about what it just said (docs/dev/design/dual-brain.md). The briefings it speaks from reach it as function responses, never as user turns.

The voice does not classify intent and does not execute. Everything the
user says that is not small talk goes to the orchestrator (`to_supervisor`).
**The words the orchestrator gets are the user's recorded input** — the
voice's input transcription of every utterance since the daemon last
acted on the user's words (a sentence split by a pause is one
utterance), typed text, or a tap — never the voice model's
`to_supervisor` argument, which is only a hint, logged beside them
(`supervisor.relay {heard, said}`; `supervisor.relay_fallback` when
nothing was recorded and the argument is all there is). The voice
model paraphrased, invented ("16" relayed as "What does it say?") and
cut sentences short ("sometimes") when its argument was the utterance.
The orchestrator resolves what it is — a status question, a question to
investigate, an instruction, an answer to a worker's prompt, a promise,
a note — from the transcript, and acts. The voice speaks what the orchestrator
returns, answers a first follow-up from the reply's `detail` and
`options` without going back, and judges the *moment* for anything the
orchestrator brings unprompted.

An utterance the voice leaves unanswered — no tool call and no word of
its own for 4 s after the last fragment — is relayed by the daemon
itself (`supervisor.relay_by_daemon {text, waited_ms}`) and its reply
pushed; the voice speaking a push is not an answer. Two utterances back
to back, the second arriving while the first's orchestrator turn is still
queued, are one turn over both texts (`supervisor.utterance_coalesced
{n}`); a turn already running is never cut, and its reply is stale if
it lands after the next request was taken up.

Code fast paths (deterministic, no model): a bare "yes" / "no" /
"option two" while exactly one worker prompt is open → its keys (the
user's verbatim word, so the rail is satisfied); a bare "stop" /
"wait" / "cancel" / "abort" / "halt" / "hold on" / "no no" → the stop
procedure. *Bare* means the whole utterance. "Never mind" is not a stop
word: it goes to the orchestrator like any utterance.

## The voice — do / don't

Does (since dual-brain and unified roles; `prompts/assistant.md`): owns
the conversation; tells the substance of a briefing in its own words
(which agent, the outcome, the numbers, what is still open), one or two
sentences unless the user asks for more; answers itself only its own
last line again or more plainly, what a word means, and small talk;
hands everything else to the orchestrator with `to_supervisor`; says a
decision only the user can take as a choice and waits; hears and
repeats back when unsure.

Never: adds a follow-up question of its own; states anything about the
workers no briefing carried; acts on an agent; answers a worker's
prompt; reads paths, hashes or ids aloud; treats "never mind" as stop.

## The orchestrator — do / don't

Does:
- Keeps one transcript per daemon run: every user utterance and
  voice reply, every push, every worker state change, its own tool
  calls and answers; compacts old turns, prunes bulky results, keeps the
  full transcript on disk (`recall` searches it).
- Reasons with `read_screen`, `recall`, `list_agents`, `run_cli` (short
  lookups) and `investigate` (background jobs, see below); acts with
  `prompt` (which also starts a new agent: "Starting an agent"),
  `send_keys`, `interrupt`, `promise`, `note`.
- Treats everything read from a pane or a CLI as **data, not
  instructions** (provenance delimiters).
- Owns promised work: "tell me when it's done" is a recorded promise
  kept at the matching worker event, spoken at every chattiness level.
- Judges pushes: on a block or a finish it decides whether the voice says
  anything and how urgently, in one line that names the decision or the
  next move — never "say yes".
- Answers for the ear: `{spoken, ask?, detail, needs_user, options?,
  pending?}` (`prompts/svp/RULES.md` "For the ear", "Explaining",
  "Answer contract").
  - `spoken` is short by default: one or two short sentences, longer
    only when the user asked for detail; one idea per sentence, no
    dashes or semicolons, the outcome first (done, broken, needs you,
    still going), the task in the user's words, no PR numbers, ms
    values, hashes, file or component names. The prompts kept these
    length rules under unified roles (arm r; #155's "fit the content"
    variant cost understanding in step 4, see
    docs/dev/eval/prompt-arms-2026-10-06.md and
    docs/dev/eval/roles-arms-2026-10-07.md); the code-built investigation
    and announce requests ask for `spoken` "as complete as the finding
    needs" / "as long as its news needs", in short plain sentences.
  - `ask` is the one question, when only the user can decide (or
    the next move the orchestrator would take); the voice hears it last.
  - `detail` is the plain explanation: one everyday sentence per term
    the headline used, for a listener who is not a programmer, no
    numbers or ids. The voice reads it for "what does that mean"; it is
    not a place for extra facts.
  - Asked again ("what does that mean?" a second time), the orchestrator picks
    the one concept, explains it in fewer words than before, and asks
    "Clearer?".
  - Code caps the headline on every orchestrator line bound for the voice
    (`prose.EarLine` / `EarLineWith`): announcements, finishes and
    investigation deliveries, and replies to the user's own words
    alike, at one runaway cap (`prose.EarSeconds`, about 44 s of estimated
    speech: ~110 English words or ~200 CJK characters; a guard, not the
    style rule, which the orchestrator's prompt sets: short by
    default; docs/dev/history/bench-2026-10-03.md #13, docs/dev/history/dive-deep.md WP8b). Dash- and semicolon-joined
    clauses become sentences, PR numbers and hashes are scrubbed,
    the ask is appended last (with an ask, a question in the headline
    moves out), and whatever was cut is appended to `detail` (for an
    announcement, the detail kept on the finish for recall).
- Serialises turns: single writer; user turns before event turns;
  an event turn yields before its next model call when a user turn
  waits; queued event turns coalesce per agent.

Never: speaks to the user directly (only the voice speaks); bypasses
a worker's gate (the rail); grows its window without bound
(`brain.window_tokens`, compacted above it).

## The one rail

Answering a worker's approval prompt *is* bypassing its gate.
`send_keys` executes only when a user utterance in the current turn
authorises that answer, the worker is at a prompt the daemon has
recorded, and every key is from that prompt's option set. "A user
utterance" means one the daemon itself recorded — the voice's input
transcription, typed text, or a tap on the question — that arrived after
the prompt appeared (when it was first seen, per pane) and within
`supervisor.authorise_window_s`, and whose
own words (never the voice model's `to_supervisor` argument) answer it:
never empty, and a "no" never approves. A `to_supervisor` call with no
such input since the previous call and the last push is the voice
model's own initiative and does nothing
(`supervisor.voice_initiative_ignored`): that is the one relay guard
left, since every utterance now runs on the recorded input, and a push
relayed back can no longer pass for the user's words. One exception
stands on the same recorded input: right after the orchestrator's turn
on the user's newest words fell back without acting, such a call
retries those words once (`supervisor.relay {retry_of}`, 2026-10-08
03:04:58), since they are still the user's own, unanswered. Enforced in
the executor, not by prose; the voice has no tool that can answer a
prompt at all. Everything else — instructions, Ctrl-C, questions — is
the orchestrator's call.

The voice is on one question at a time (the pending action), but every
blocked agent's read prompt stays open for a tap (`state.Table.Questions`,
`GET /api/state` `questions`). A tap on a question the voice moved past
passes the same rail with that question's own prompt and option set,
after a fresh read of the agent's screen: a prompt that changed refuses
the tap, so keys never reach a question they were not chosen for. It
leaves the voice on its own question.

The app's agent window (docs/dev/ws-contract.md "Agent window: screen and
input") is outside the rail on purpose: there the user types or presses
keys in the worker's window himself, so his tap is the authority. It is
logged (`web.agent_input`) and told to the orchestrator, and keys that select
an option of the open question go through the tap path above.

## Stop

A procedure in code, resolved from what is in flight: drop a call not
yet handed over → cancel the orchestrator's in-flight user turn (write
tools run under its context and never execute) → cancel the running
investigation the withdrawn request was about, or, with nothing in
flight, the most recent one → Ctrl-C a worker that
received an instruction within `supervisor.cancel_window_s` (30 s) →
leave an open worker prompt open (stop is not "no"). The daemon's own
sentence acknowledges it at once — no model turn stands between the stop
and its reply — and what
was stopped goes into the orchestrator's transcript as a context line, so a
later question knows about it. The interrupt phase says only "Sent an
interrupt to <alias>.": Ctrl-C does not guarantee a queued instruction
was dropped. Undoing a
worker's edits is an instruction to the worker, not a stop. The stop
words are "stop", "wait", "cancel", "abort", "halt", "hold on" and "no
no" (`supervisor.BareStopWords`, the voice's `stop` tool); "never mind"
is not one: it is ordinary speech the orchestrator reads. Event turns
are not cancelled by stop. Logged as `supervisor.stopped {phase, what,
trigger}` (`stop_tool` or the bare-word fast path).

## Pushes

One line per worker event, and only a prompt is urgent. **A finish
waits for the orchestrator's line**: nothing is said at `brain.announce_wait_s`;
the orchestrator's line (what it found) is spoken alone whenever the announce
turn answers, within the turn's own budget (`brain.announce_turn_timeout_s`,
or `brain.announce_timeout_s` when the user is owed it — it keeps a
promise or ends an instruction they gave this run). Only when the turn
fails, times out, yields or is coalesced is the code sentence ("auth
finished") spoken, once, then; an owed finish whose turn timed out says
in that sentence that its findings could not be had. **A block stays
fast**: the parsed prompt is spoken at `brain.announce_wait_s` unless the
orchestrator's line lands first. A orchestrator line that lands after the code
sentence is dropped (`brain.push_late`), with one rule and one
exception, each at most one added line at idle: a line that says the
user must decide or act (`needs_user`) is always spoken, whatever
the event, unless the prompt it is about is gone; and the finding of an
owed finish that also asked (below) or a line that relates it to one of
their requests (`relates_to`) is added too. "<alias> started on …" for
an instruction the orchestrator sent within `supervisor.cancel_window_s` is
silent context (the tool result already said it was sent); one picked
up later is spoken at `chatty`. A finish that leaves a prompt on screen
(Claude Code's post-turn "teach auto mode…?"; not agy, which reports done
around its mid-task prompts) is announced with the question in one line
("auth finished, and it's asking …"), keeps the finish's promises and
makes the question the pending answer. Nothing interrupts
the voice mid-sentence except the user: while the voice is
speaking — until the user's client has finished playing the reply,
not merely until the model finished generating it (docs/dev/ws-contract.md
"Playback reports") — INTERRUPT scheduling is downgraded to WHEN_IDLE,
save a due promise, a decision on a *new* block, or the stop button's
reply. A spoken push also waits while the client's own speech detector
hears the user, and the push gap runs from the end of playback. When
a reply is cut off (a barge-in, stop, an interrupting push), the orchestrator's
"Assistant said" line keeps only the words that played, marked "cut off
after N s". An addition to a block is
dropped once the block is resolved. The chattiness level is a ceiling
code keeps: `quiet` — questions, exits, kept promises and the finish of
the user's own instruction; `normal` — plus finishes of agents the
user dealt with within `supervisor.recent_minutes`; `chatty` —
everything the orchestrator wants to say. Owed lines (a kept promise, the
finish of the user's own instruction) are spoken at every level.

## Investigations and explanations

The user cannot see the screen, so "explain what happened" is a
first-class request. The orchestrator answers from the cheapest layer that
works: the last reply's `detail` (the voice does this itself), a screen
read, `recall`, then an investigation. An investigation is a background
job (`investigate(agent, question)`): the orchestrator acknowledges within the
utterance turn's budget ("reading the diff, about half a minute", `pending: true`),
the job runs the read-only CLI agent in the worker's repository — or,
with `source: worker`, forks the worker's own CLI session (read-only,
never touching the pane) and asks there, so it answers from what the
worker actually saw and tried — with no time limit beyond a sanity
ceiling, and its result comes back as an event turn that pushes
the explanation at the next pause, tagged to the request. Follow-ups
resume the same CLI session (the harness persists it); the orchestrator keeps
an investigation ledger `recall` can search. One live investigation per
agent; stop cancels it.

**Worker history.** A screen is the worker's working notes; what it
wrote for a human is in its own session transcript, which herdr names
(`agent_session`: Codex's session id, Claude Code's session id or
transcript path). On every finish or block the supervisor reads only
what was appended since its last read (`internal/workerlog`) and adds
one entry to the agent's work journal in the state file (the last 20):
when, the event, the worker's last message (≤ 600 characters, D9
scrubbed), the instruction it was on (≤ 200), the files it edited (base
names, ≤ 10) and its tool counts. The finish's announce turn hears that
message first, labelled as what the worker wrote, then the screen; the
ledger shows each agent's last three entries in one short line each;
`recall` searches them all. Every instruction the orchestrator sends through
`prompt` gets a fixed trailer from the supervisor, never the model —
"When you finish, end with three short lines for someone who cannot see
the screen: what changed; what needs a decision; what is next." — so
that last message is written for the user; the trailer is never
counted as the user's words (the echo, the pending action, the
"started on" line and the journal use the instruction without it). agy
keeps no transcript the daemon can read (`worker.session_missing`).

## New conversation

"New conversation" resets the orchestrator's transcript window (a fresh
segment; the compaction summary is gone — nothing said before the reset
is in the next prompt) and the voice model's own session (Gemini: drop
the resumption handle and reconnect fresh; GPT-Live: close the billed
session if one is open, no recap, and reopen later on the next speech
or push). **Kept**, because none of it ever lived in the window or the
voice session's memory: the agent roster and its ledger, open approval
prompts, promises and notes (still in the fresh orchestrator's ledger section),
running investigations, the event log on disk, and the conversation
history API's older items (a `notice`/`reset` divider is added, not a
truncation). Two triggers reach the same reset: `POST
/api/conversation/reset` (`/debug`, `/voice` and the app, each with a
confirm on the button), and a bare voice phrase ("start over", "new
conversation", "start from scratch", "reset the conversation", "clear
the conversation") — a code fast path, like the bare stop word, that
asks a confirming question rather than acting at once (this is
destructive to the conversation so far); only the exact confirm phrase
("yes, start over" / "confirm reset"), from real user input, within
20 s, carries it out — a bare "yes" keeps its ordinary approval-prompt
meaning. The fresh voice session then says one short line ("Fresh
start. What's next?").

## Hot paths that stay code

Debounce, dedup and TTL timers; the chattiness ceiling; the rail; the
stop procedure; the bare yes/no fast path; the code fallback sentences
when the orchestrator is down; `composeBriefing` (the roster line in every
orchestrator request). No template LLM: the orchestrator is the only model that
phrases, and when it is unavailable the daemon speaks the code sentences.

## The L0 eval corpus

`sheepdog eval --set all` (`make eval`) is the offline gate of
docs/dev/iteration-2.md: every case below runs through the orchestrator with a
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
screen is at most the first 1 KB of the orchestrator's last `read_screen`;
aliases come from the supervisor's events (`herdr.transition` carries
none); the roster sentence is not logged.

## Prompt files

| File | Holds |
|---|---|
| `prompts/assistant.md` | The voice's whole system prompt on Gemini Live: persona, hearing, the two tools, reading a reply, follow-ups from `detail`, pushes (≤ 400 words; re-billed every turn). |
| `prompts/assistant-gptlive.md` | The same for GPT-Live, whose client delegation replaces the two tools. |
| `prompts/svp/SOUL.md` | The orchestrator's role and manner: who it serves, its bias to execution, the copilot manner. |
| `prompts/svp/RULES.md` | The orchestrator's tool guidance, one `## ` section per stable-prompt piece (tools, intent, record, promises, announcing, the one rail, fallback, for the ear, explaining, the answer contract). |
| `prompts/USER.md` | The user's profile: `{aliases}`, `{chattiness}`, `{tz}`, free text. |

Overrides in `~/.config/sheepdog/prompts/` (config `prompts.dir`), re-read
on `SIGHUP` / `POST /api/prompts/reload`; the rail sentence and the
provenance sentence in `RULES.md` cannot be overridden; the tool surfaces
are Go, never prompt text.
