# sheepdog v1 — Implementation Plan

> **Historical.** All work packages here are delivered. The design has since
> been revised twice after real voice sessions: D7 (stateless supervisor) →
> a stateful brain (see the D7-revised note below), and on 2026-09-18 D1
> (voice tools that execute) → one decision-maker, plus removal of the
> template LLM, investigations and the names shepherd / dog / flock. The
> current contract is [`docs/roles.md`](../roles.md); the decisions and
> the order of work are in [`docs/issues-2026-09-18.md`](issues-2026-09-18.md).
> Read those first; read this for the original reasoning and the herdr facts.

> Working name: **sheepdog** (rename if taken). A voice supervisor for a herdr agent fleet.
> Audience: an orchestration agent that will split this into worker tasks. Every work package below has inputs, deliverables, and acceptance criteria so it can be assigned and verified independently.

---

## 1. Goal and scope

**Goal.** Let the operator supervise coding agents running inside herdr entirely by voice: ask what is happening, get told proactively when an agent is blocked, answer the agent's question, give new instructions, and interrupt — without touching a keyboard.

**v1 scope (local only).**
- Runs on the same machine as herdr (macOS, Apple Silicon). Manages local herdr agents only.
- Voice in/out through a browser page on that machine (`/voice`). No phone, no remote access, no HTTPS.
- Gemini 3.8 Live is the only voice model.
- A debug page (`/debug`) that shows every event: herdr state changes, voice tool calls, supervisor model calls, CLI invocations, pending actions.

**Explicit non-goals for v1** (designed for, not built):
- iOS companion, phone lock-screen audio, CarPlay.
- Remote (VPS) agents via SSH-forwarded socket.
- GPT-Live adapter, LiveKit/Pipecat session layer.
- Risk tiers / approval gates in the supervisor (see §4, decision D5).
- Structured memory ledger beyond the persisted state table.

**Success test.** Sitting at the desk with a Bluetooth headset, the operator can: (1) ask "what's going on" and get a one-sentence answer in under a second; (2) have an agent hit an approval prompt and hear sheepdog announce it within ~5 s without being asked; (3) say "yes, go ahead" and have the answer delivered to the right pane; (4) say "tell the auth one to skip the flaky test and rerun", hear it echoed back, and see the instruction land in the pane; (5) say "stop" mid-echo and have nothing sent.

---

## 2. Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  Browser  /voice   (mic → 16 kHz PCM → WS)  (WS → 24 kHz PCM → speaker)
│           /debug   (SSE tail of the event log, fake-event buttons)
└───────────────┬─────────────────────────────────────────────┘
                │ WebSocket (audio bytes + a few control JSON msgs)
┌───────────────▼─────────────────────────────────────────────┐
│  sheepdog daemon (Go, single binary)                         │
│                                                              │
│   VoiceSession ──── Gemini 3.8 Live (WebSocket, owned here)  │
│        │ 5 tools + push                                      │
│   Supervisor ── state table ── event log (JSONL) ── SSE      │
│        │              │                                      │
│   herdr client   cheap LLM (OpenRouter-compatible)           │
│   (unix socket)  CLI query tool (codex exec / claude -p,     │
│        │          read-only, timeout)                        │
└────────┼─────────────────────────────────────────────────────┘
         │ ~/.config/herdr/herdr.sock
   herdr ── worker agents (Codex / Claude Code / … in panes)
```

Three layers, hard boundaries:

| Layer | Owns | Never does |
|---|---|---|
| Voice model (Gemini 3.8 Live) | hearing, speaking, turn-taking, deciding *whether* to call a tool | touch herdr, hold task state, plan multi-step work |
| Supervisor (daemon) | state table, pending action, event routing, tool implementations, all model calls that are not voice | speak; make risk judgements beyond "did I understand the target" |
| Workers (herdr panes) | actually changing code; their own approval prompts | — |

The Gemini session lives in the daemon, not the browser. The browser is a dumb audio pipe.

---

## 3. Components

### 3.1 herdr client
- Connects to the herdr unix socket. Subscribes to agent state events (`blocked` / `working` / `done` / `idle`). Reads pane screen content. Sends text and keys to a pane. Wraps the same surface as `herdr agent …` / `herdr wait …` CLI.
- Source of truth for the protocol: `SOCKET_API.md` in the herdr repo. Do not rely on herdr-mesh for v1.
- Reliability pattern (copied from `herdr-agent-notify`): two event channels (socket subscription + the plugin hook shim in 3.8), deduplicated by (pane, state, timestamp window); plus a periodic snapshot diff every 30 s that re-emits any transition both channels missed. Per-pane debounce: a `blocked` state must persist ≥ 3 s before it becomes a supervisor event.

### 3.2 State table
Persisted to disk (JSON file, atomic write on change; SQLite acceptable). Survives daemon restart.

```
agents[pane_id]:
  alias          string   # short spoken name, e.g. "auth", "vps-one" (user-configurable; default derived from workspace + pane title)
  kind           string   # codex | claude | …
  workspace      string
  state          enum     # blocked | working | done | idle | unknown
  state_since    time
  current_task   string   # last instruction given (by user via propose, or read from pane)
  last_block     { question string, seen_at time, answered bool }
  last_seen_screen_hash string

pending_action:            # at most ONE at a time (v1 rule)
  id, pane_id, kind (answer_prompt | send_instruction | interrupt),
  spoken_summary, payload_text, created_at, expires_at (created_at + 120 s)

session:
  voice_connected bool, last_briefing string, last_briefing_at time
```

### 3.3 Event log
Append-only JSONL, one file per day under the daemon's data dir. Every component writes to it. This is what `/debug` tails and what future tests replay.

```
{ts, source: herdr|voice|supervisor|llm|cli|web, type, pane_id?, payload{...}, dur_ms?}
```
Required event types: `herdr.state`, `herdr.read`, `herdr.send`, `voice.tool_call`, `voice.tool_result`, `voice.push`, `voice.transcript.in`, `voice.transcript.out`, `voice.interrupted`, `llm.call` (model, prompt hash, tokens, truncated output), `cli.run` (cmd, exit, dur, truncated output), `pending.created|confirmed|cancelled|expired`, `web.client.connect|disconnect`.

### 3.4 Cheap LLM adapter
- OpenAI-compatible chat endpoint (OpenRouter by default; base URL + model + key from config). Used **text-in/text-out only** — no function calling required from this model, so any OpenRouter model works. Default: a fast Gemini Flash-class model; DeepSeek acceptable.
- Three prompt templates, each returning a small JSON object:
  1. `summarize_block(screen_tail, alias)` → `{spoken: string, is_question: bool, options: [string]?, needs_decision: bool}`
  2. `resolve_instruction(user_text, agent_list)` → `{pane_id | null, instruction_text, echo: string, ambiguous: bool, clarify_question?: string}`
  3. `spoken_answer(raw, question)` → `{spoken: string}` (≤ 2 sentences, no paths/hashes, aliases only)
- Hard limits: input truncated to N KB of screen tail (default 8 KB, last lines first), 15 s timeout, one retry.

### 3.5 CLI query tool (optional dependency)
- Detected at startup on PATH: `codex` and/or `claude`. If absent, `ask()` still works with the cheap LLM alone.
- Invocation is **read-only, non-interactive, bounded**: e.g. `claude -p "<q>" --allowedTools "Read,Grep,Glob,WebSearch"` or `codex exec --sandbox read-only "<q>"` (verify exact flags against current CLI docs; put them in config so they can be changed without a rebuild). cwd = the agent's workspace. 60 s timeout, output capped at 16 KB, then passed through `spoken_answer`.
- It may read the repo, diffs, logs, and the internet. It **must not** write. The only write path to a repo is `herdr send` to a worker pane.

### 3.6 Supervisor
The daemon's brain. Pure Go logic plus the adapters above.

Responsibilities:
- Maintain the state table from herdr events.
- On `blocked` (post-debounce): read pane tail → `summarize_block` → update `last_block` → if the voice session is connected, push a spoken sentence (§3.7) and, if `is_question`, create a `pending_action` of kind `answer_prompt`. If not connected, only log (v2: forward to a notifier).
- On `done`: low-priority push ("auth finished"), no pending action.
- Implement the five voice tools (§5).
- Queue pushes: one at a time, FIFO, minimum 2 s gap, never while the user is speaking (see Gemini scheduling in §6).
- Expire `pending_action` after 120 s; a new pending action replaces the old one and the replacement is spoken ("dropping the earlier question about migrations; now auth is asking …").

### 3.7 VoiceSession (Gemini 3.8 Live)
- Owns one WebSocket session to the Live API for the lifetime of the daemon (reconnect with session resumption on `GoAway`/drop; connections are ~10 min, sessions persist).
- Bridges audio: browser PCM 16 kHz mono → Live API input; Live API output 24 kHz PCM → browser. Maintains an output queue; on `interrupted` from the API, flushes the queue **and** tells the browser to flush its playback buffer.
- Declares the five tools as non-blocking function declarations. Executes tool calls by calling Supervisor methods in-process and returning the function response with the scheduling hint the Supervisor chose.
- `push(text, urgency)`: inject a text turn / client content so the model speaks unprompted. Map `urgency`: `interrupt` → immediate; `idle` → when the model is idle; `silent` → context only.
- Input and output transcription enabled (needed for the debug page and for `resolve_instruction`).
- Context window compression enabled with a small target (see §6). System prompt kept short (§7).
- Keyword biasing / custom vocabulary with agent aliases if the API exposes it; otherwise put aliases in the system prompt.

### 3.8 Web server
- Serves `/voice` and `/debug` as static files from the binary (embed). One WebSocket endpoint for audio + control, one SSE endpoint for the event log, one small HTTP API for the debug page (list agents, inject fake event, cancel pending).
- `/voice`: AudioWorklet capture → resample to 16 kHz → Int16 PCM frames → WS binary. Receive WS binary → gapless 24 kHz playback. Buttons: connect, mute, "stop". Control messages (JSON): `{type: "flush"}`, `{type: "mute", on: bool}`, `{type: "status", …}`.
- `/debug`: live-scrolling event list with filters by source / pane; current state table; pending action with a cancel button; **fake event injection**: "make pane X blocked with this screen text", "make pane X done", "simulate user utterance <text>" (feeds a text turn to the voice session so the tool path can be tested without a mic).
- herdr plugin shim (thin): a hook executable installed via `herdr plugin install` that forwards each herdr event as an HTTP POST to the daemon (second event channel) and, on install, drops a launchd plist for the daemon. Keep it < 100 lines.

---

## 4. Settled design decisions (do not re-litigate; change only with a note)

- **D1 — Voice model never touches herdr.** It only sees the five tools. All herdr reads/writes go through the Supervisor.
- **D2 — Gemini session lives in the daemon.** Tool calls are in-process; pushes are trivial; API key stays server-side; browser reconnects do not lose the model session.
- **D3 — Pending action lives in the Supervisor, not in the voice model's context.** `confirm()` with no id means "the most recent pending action". One pending action at a time. Expired → refuse and offer to repeat.
- **D4 — `propose()` = echo, then send, then cancellable. No confirmation step.** The worker agents' own approval prompts are the safety gate. Sheepdog echoes its understanding in one sentence and sends immediately; "stop"/"cancel" sends Ctrl-C to that pane and clears the pending action. Prerequisite: workers must NOT run in fully auto-approve mode; document this.
- **D5 — No risk classification in v1.** If workers keep their approval gates, the supervisor needs none.
- **D6 — Two kinds of "unsure" live in two prompts.** Mis-hearing (audio ambiguity) is handled by the voice model's prompt: repeat back what it heard and ask. Mis-resolving (which agent, what "that") is handled by the Supervisor: `resolve_instruction` returns `ambiguous=true` with a clarifying question that the voice model reads out; nothing is sent.
- **D7 — Cheap model is text-in/text-out.** Orchestration is rules in Go, not model function calling. Any OpenRouter model is swappable by config.
  - **D7 revised (2026-09-18, after the first real voice sessions).** Stateless template calls lose the conversational referent at the tool boundary ("why did *that one* fail?" reached `codex exec` as five context-free words). The supervisor's *judgment* paths — `ask`, the resolution half of `propose`, block/done summaries, push judgment — move to a stateful agent (`internal/brain`): a per-session transcript (every utterance, every spoken reply, every push, its own tool exchanges), a per-agent ledger, and tools `read_screen`, `run_cli`, `list_agents`, `recall`, with the future memory system (P6) attached to `recall`. Requires a function-calling model (`gemini-3.5-flash`); Flash-Lite templates stay for option/key parsing. Kept as plain code for **latency**, not safety: the cached `briefing` (sub-second success test), debounce/dedup/TTL timers. The chattiness level remains a hard ceiling on push judgment. The one rail that is about bypassing a worker's gate: an approval keystroke is sent only when a user utterance in the current turn authorises it, drawn from the option set parsed off the screen, never proactively. Rolled out in phases: (1) ask + propose resolution, (2) summaries with context, (3) push judgment.
- **D8 — CLI is a read-only query tool of the Supervisor, not a supervisor.** Only path that changes code is `herdr send` to a worker.
- **D9 — Every tool result is written for the ear.** ≤ 2 sentences, aliases not pane ids, no paths, no hashes, no URLs.
- **D10 — Everything goes through the JSONL event log.** The debug page is a viewer, not a second source of truth.
- **D11 — Go for the daemon, vanilla JS for the pages.** Single static binary; cross-compile for darwin/arm64 and linux/amd64 from day one.

---

## 5. Voice tool surface

Exactly five function declarations. All are non-blocking from the voice model's point of view; the Supervisor picks the scheduling of the response.

```jsonc
// 1. briefing — synchronous, must return < 300 ms from the state table cache. The only tool that never calls a model.
{ "name": "briefing",
  "description": "Current status of all agents in one spoken sentence. Call when the user asks what's happening, how things are going, or for a status.",
  "parameters": { "type": "object", "properties": {} },
  "returns": { "spoken": "string" } }
// example spoken: "Three agents. Auth is running tests, docs is idle, vps-one is blocked asking whether to delete the old migration."

// 2. ask — asynchronous; may take seconds. Supervisor reads panes, calls the cheap model, optionally the CLI tool.
{ "name": "ask",
  "description": "Ask a question that needs looking at an agent's screen, diff, logs, or the repo. Use for 'why', 'what did it change', 'is it stuck'. Tell the user you're checking, then relay the answer when it arrives.",
  "parameters": { "type": "object", "properties": { "question": { "type": "string" }, "agent": { "type": "string", "description": "alias if the user named one" } }, "required": ["question"] },
  "returns": { "spoken": "string" } }

// 3. propose — asynchronous. Any write: answer an agent's question, give a new instruction, interrupt an agent.
{ "name": "propose",
  "description": "Send an instruction or an answer to an agent. Use when the user tells an agent to do, stop, or answer something. The system will echo what it will send; the user can say stop to cancel.",
  "parameters": { "type": "object", "properties": { "agent": { "type": "string" }, "instruction": { "type": "string", "description": "what the user said, in their words" } }, "required": ["instruction"] },
  "returns": { "spoken": "string", "clarify": "boolean" } }
// spoken on success: the echo, e.g. "Telling auth to skip the flaky test and rerun. Sent."
// spoken on ambiguity: "Do you mean auth or vps-one?" with clarify=true

// 4. confirm / cancel — one word each; synchronous.
{ "name": "confirm",
  "description": "User says yes / go ahead / do it to the most recent question from an agent.",
  "parameters": { "type": "object", "properties": { "answer": { "type": "string", "description": "the user's answer if it isn't a plain yes, e.g. 'no', 'option 2', 'keep the old one'" } } },
  "returns": { "spoken": "string" } }
{ "name": "cancel",
  "description": "User says stop, cancel, wait, or no. Cancels the in-flight action or the pending question.",
  "parameters": { "type": "object", "properties": {} },
  "returns": { "spoken": "string" } }

// 5. note — synchronous, cheap. Records a decision or constraint for later.
{ "name": "note",
  "description": "Remember something the user decided or wants kept in mind, e.g. 'don't touch the migrations today'.",
  "parameters": { "type": "object", "properties": { "text": { "type": "string" } }, "required": ["text"] },
  "returns": { "spoken": "string" } }
```

Push (not a tool): Supervisor → VoiceSession `push(text, urgency)`. Blocked = `idle` urgency by default, `interrupt` only for agent crash/exit; done = `idle`.

---

## 6. Gemini 3.8 Live session configuration

Verify every field name against the current Live API reference before coding; the intent is fixed, the names may differ.

- Model: `gemini-3.8-live` (not Extended Thinking; see rationale in decision log: voice layer should be fast and thin).
- Response modality: audio. Input: 16 kHz PCM16 mono. Output: 24 kHz PCM16.
- Input + output transcription: on.
- Tools: the five declarations above, non-blocking behavior; function responses carry a scheduling hint (`INTERRUPT` / `WHEN_IDLE` / `SILENT`) chosen by the Supervisor.
- Context window compression: on, sliding window, `trigger_tokens` ≈ 16 000, `target_tokens` ≈ 4 000. Rationale: the voice model only needs the last few minutes for reference resolution; all durable state is in the Supervisor; every turn re-bills the whole context.
- After each compression (or on reconnect), the Supervisor pushes the current briefing as `silent` context so the model re-learns the situation.
- Session resumption: on. Handle `GoAway` by reconnecting with the resumption handle before the ~10 min connection limit.
- VAD: API default for v1. Half-duplex behavior when muted: mic frames are not sent at all (no barge-in from a muted mic).

---

## 7. Prompts

**Voice model system prompt** (short; style + when to delegate; nothing about business rules):
- You are the voice of a supervisor for coding agents. Be brief: one or two sentences. Speak agent names as their aliases: `{aliases}`.
- When the user asks how things are, call `briefing`. When they ask why/what changed/is it stuck, call `ask` and say you're checking. When they tell an agent to do or answer something, call `propose`. "Yes"/"go ahead" → `confirm`. "Stop"/"cancel"/"wait" → `cancel`. "Remember…" → `note`.
- If you are not sure you heard correctly, repeat what you think you heard and ask, instead of guessing. Never invent status; if a tool hasn't answered, say so.
- Read tool results aloud as given. Do not read paths, hashes, or ids.

**Supervisor prompt templates** live in Go as text files embedded in the binary (`prompts/*.txt`), each with a JSON output contract as listed in §3.4.

---

## 8. Repository layout (Go)

```
sheepdog/
  cmd/sheepdog/main.go          # flags, config, wiring, launchd-friendly run loop
  internal/herdr/               # socket client, events, snapshot diff, debounce
  internal/state/               # state table, persistence
  internal/eventlog/            # JSONL writer, SSE broadcaster
  internal/llm/                 # OpenAI-compatible client, prompt templates
  internal/cli/                 # read-only codex/claude runner
  internal/supervisor/          # tools, pending action, push queue
  internal/voice/gemini/        # Live API session, audio bridge, tool plumbing
  internal/web/                 # HTTP/WS/SSE, embedded static
  web/voice/  web/debug/        # vanilla JS + AudioWorklet
  prompts/                      # embedded prompt templates
  plugin/                       # herdr plugin shim (hook + install script + launchd plist)
  testdata/                     # captured pane screens, event logs for replay
  README.md  CONFIG.md
```

Config: `~/.config/sheepdog/config.toml` (or env overrides): `gemini_api_key`, `llm.base_url`, `llm.model`, `llm.api_key`, `herdr.socket`, `cli.codex_cmd`, `cli.claude_cmd`, `aliases = { "w9:pD" = "auth", … }`, `web.port`, `debounce_ms`, `pending_ttl_s`.

---

## 9. Work packages

Dependencies are noted; WP1, WP2, WP5 can start in parallel. WP3 needs WP1. WP6 needs WP2's WS contract (define it first, in WP2's first task).

### WP0 — Protocol reconnaissance (½ day, blocks WP1/WP2 details)
- Read `SOCKET_API.md` in the herdr clone; write `docs/herdr-protocol.md` with the exact subscribe/read/send messages and the state enum, plus 3 captured real event samples (run a Codex pane, trigger an approval prompt, capture).
- Read the Live API WebSocket reference and the Go genai SDK's Live surface; write `docs/gemini-live-notes.md` with the exact field names for: setup message, audio chunks, function declarations with non-blocking behavior, function response scheduling, context compression config, session resumption, `GoAway`, `interrupted`, transcription events. Decide SDK vs raw WebSocket (prefer SDK if it exposes all of the above; otherwise raw).
- Verify the read-only invocation flags for `codex exec` and `claude -p`.
- **Acceptance:** both docs exist with copy-pasteable message shapes; a decision on SDK vs raw is recorded.

### WP1 — herdr client + state table
- Implement `internal/herdr` (subscribe, read pane tail, send text, send keys/Ctrl-C), `internal/state` (table + JSON persistence), snapshot diff loop, per-pane debounce, dedup between two channels (HTTP shim channel can be stubbed).
- Alias assignment from config with sensible defaults.
- **Acceptance:** a `sheepdog herdr watch` subcommand prints deduplicated, debounced transitions while a real agent runs; killing and restarting the daemon restores the table; a fabricated `blocked` flap < 3 s produces no event.

### WP2 — Gemini Live session + audio bridge
- Define the browser ↔ daemon WS contract first (binary frames = PCM; JSON text frames = control) and document it in `docs/ws-contract.md`.
- Implement `internal/voice/gemini`: connect, setup with config from §6, audio in/out, output queue with flush on `interrupted`, transcription events → event log, tool declarations, tool call → Go callback → function response with scheduling, `push(text, urgency)`, resumption/reconnect on `GoAway`.
- **Acceptance:** with a stub tool set (echo tools), a `sheepdog voice-test` subcommand streams a WAV file in and writes the reply audio to a WAV out; a fake tool call round-trips; an injected `push` makes the model speak unprompted; a forced disconnect resumes without losing the conversation.

### WP3 — Supervisor logic + cheap LLM + tools (needs WP1)
- Implement `internal/llm` (client, three templates, truncation, timeout), `internal/supervisor` (five tools, pending action with TTL and single-slot rule, push queue, blocked/done handlers, briefing cache).
- Unit tests with recorded pane screens in `testdata/` covering: approval prompt (Codex), yes/no question (Claude Code), bare idle prompt, error exit; ambiguous vs unambiguous `resolve_instruction` cases; pending expiry; replacement announcement.
- **Acceptance:** all tool functions callable from a Go test without any voice session; `briefing` from cache in < 5 ms; `propose` with ambiguous target returns `clarify=true` and sends nothing; `cancel` after `propose` sends Ctrl-C to the correct pane.

### WP4 — CLI query tool (needs WP3 interfaces)
- Implement `internal/cli`: detection, read-only invocation from config, cwd = workspace, 60 s timeout, 16 KB cap, result → `spoken_answer`.
- `ask` routing rule in Supervisor: use CLI only if the question mentions why/diff/log/failing/changed or the cheap model's first pass says it can't answer from the screen.
- **Acceptance:** `ask("why did auth's tests fail")` on a repo with a failing test returns ≤ 2 spoken sentences naming the failing test; the CLI process is killed at the timeout; no file in the repo is modified (assert via git status).

### WP5 — Event log + debug page + fake injection
- `internal/eventlog` (JSONL, rotation by day, SSE fan-out), `/debug` page (tail with filters, state table, pending action + cancel, inject buttons, "simulate utterance" text box).
- **Acceptance:** with the daemon running and no herdr, injecting a fake `blocked` for a fake pane produces the full chain in the log: `herdr.state → llm.call → pending.created → voice.push`; "simulate utterance: yes" produces `voice.tool_call confirm → herdr.send` (against a mock herdr).

### WP6 — `/voice` page (needs WP2 contract)
- AudioWorklet capture, resample to 16 kHz, Int16 frames over WS; gapless playback of 24 kHz PCM; handle `flush`; mute (stops sending frames); "stop" button that sends a `cancel` control message; connection status.
- **Acceptance:** round-trip talk with the daemon on localhost Chrome and Safari; interrupting the model mid-sentence stops audio within ~300 ms with no leftover tail; muting sends zero frames (verify in log).

### WP7 — Integration, packaging, docs
- Wire everything in `cmd/sheepdog`; launchd plist; herdr plugin shim (hook → HTTP POST; install script); cross-compile targets; `README.md` (install in 3 steps, worker permission-mode warning, mic permission note), `CONFIG.md`.
- End-to-end run of the five success-test scenarios in §1 with a real Codex and a real Claude Code pane; record the event log of that run into `testdata/e2e/`.
- **Acceptance:** fresh machine: `herdr plugin install …` + two keys + open the page works; all five scenarios pass; total added latency for `briefing` (utterance end → audio start) measured and logged, target < 1.2 s.

---

## 10. Test scenarios (end to end)

1. **Status.** "What's going on?" → `briefing` → one sentence; no LLM call in the log.
2. **Proactive block.** Worker hits an approval prompt → within 5 s the model says "Auth is asking whether it can push to main." → `pending.created`.
3. **Answer by voice.** "Yes." → `confirm` → `herdr.send` to that pane → "Told auth yes." → `pending.confirmed`.
4. **Instruction with echo.** "Tell the auth one to skip the flaky test and rerun." → `propose` → "Telling auth to skip the flaky test and rerun. Sent." → `herdr.send`.
5. **Cancel in time.** Same as 4 but "stop" during the echo → `cancel` → Ctrl-C to pane → "Cancelled." → nothing else sent (or the partial instruction is interrupted).
6. **Ambiguity.** Two agents running; "tell it to continue" → `propose` → "Do you mean auth or docs?" → nothing sent.
7. **Deep ask.** "Why did auth's tests fail?" → "Checking." → CLI run → ≤ 2 sentences.
8. **Expiry.** Block announced, no answer for 2 min, then "yes" → "That question expired; auth was asking about the migration — should I answer yes?"
9. **Reconnect.** Kill the browser tab mid-conversation, reopen → model still knows the last exchange (session persisted in daemon).
10. **Compression.** 20 min of chatter → compression fires → briefing re-pushed silently → next "what's going on" still correct.

---

## 11. Known unknowns (verify in WP0; do not assume)

- Exact herdr socket event schema and whether the subscription delivers `blocked` with a reason or only the state.
- Whether herdr plugin hooks are per-event processes (assumed) and the manifest format for `herdr plugin install`.
- Gemini 3.8 Live: exact names for non-blocking function behavior and response scheduling; whether keyword biasing / custom vocabulary is exposed on Live; Go SDK coverage of resumption and compression.
- Current read-only flags for `codex exec` and `claude -p`.
- Live API remains "preview" as a surface even though the 3.8 models are stable; watch for breaking changes.

---

## 12. Later phases (for context only)

- **P2:** GPT-Live-1 adapter behind the same `VoiceSession` interface (client delegation → Supervisor is the delegation backend). This is the test that the interface is right.
- **P3:** LiveKit session layer as a Python sidecar; `/voice` becomes a LiveKit client; daemon unchanged.
- **P4:** iOS companion (SwiftUI + audio background mode) replacing `/voice`; remote access via Tailscale.
- **P5:** VPS agents via SSH-forwarded herdr socket; notifier fallback (ntfy) when no voice session is connected.
- **P6:** Memory ledger (per-agent page: current task, last block, decisions given) grown from the state table and `note()` entries.
