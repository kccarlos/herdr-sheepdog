# Architecture

This is a short orientation for the code **as built at `main`**. The
contract it implements (the shepherd / dog / flock, the one rail, stop,
pushes, investigations) is [`docs/roles.md`](roles.md); what is done and
what is open is [`docs/status.md`](status.md). Where this page and
`roles.md` differ, `roles.md` is the intent and this page is what runs
today. The original plan
([`docs/history/sheepdog-v1-plan.md`](history/sheepdog-v1-plan.md)) and
the 2026-09-18 redesign's decisions
([`docs/history/issues-2026-09-18.md`](history/issues-2026-09-18.md))
are kept for history.

## Diagram (plan §2)

```
┌─────────────────────────────────────────────────────────────┐
│  Browser  /voice   (mic → 16 kHz PCM → WS)  (WS → 24 kHz PCM → speaker)
│           /debug   (conversation view, SSE tail of the event log, fake-event buttons)
└───────────────┬─────────────────────────────────────────────┘
                │ WebSocket (audio bytes + a few control JSON msgs)
┌───────────────▼─────────────────────────────────────────────┐
│  sheepdog daemon (Go, single binary)                         │
│                                                              │
│   VoiceSession ──── Gemini 3.8 Live (WebSocket, owned here)  │
│        │ 2 tools + push                                      │
│   Supervisor ── state table ── event log (JSONL) ── SSE      │
│        │  │           │                                      │
│        │  └─ brain (Gemini or DeepSeek + tools, transcript)  │
│   herdr client   prompt parser (code, no model)              │
│   (unix socket)  CLI query tool (codex exec / claude -p,     │
│        │          read-only, timeout)                        │
└────────┼─────────────────────────────────────────────────────┘
         │ ~/.config/herdr/herdr.sock
   herdr ── worker agents (Codex / Claude Code / … in panes)
```

Three layers, hard boundaries (plan §2):

| Layer | Owns | Never does |
|---|---|---|
| Voice model (Gemini 3.8 Live) | hearing, speaking, turn-taking, judging the *moment*; two tools — `to_supervisor(text)` relays the shepherd's words verbatim, `stop()` runs the stop procedure | classify intent, name an agent, touch herdr, hold task state, plan multi-step work (docs/roles.md "One decision-maker") |
| Supervisor (daemon) | state table, pending action, event routing, tool implementations, all model calls that are not voice; its brain (`internal/brain`, [`docs/roles.md`](roles.md)) reasons over the transcript with tools, one turn per shepherd utterance (`to_supervisor`), stop or worker event | speak; bypass a worker's approval gate (the one rail: `send_keys` only on the CEO's words, from the prompt's option set) |
| Workers (herdr panes) | actually changing code; their own approval prompts | — |

The Gemini session lives in the daemon, not the browser; the browser is
a dumb audio pipe. herdr feeds the daemon over its unix socket
subscription; `POST /api/herdr/event` is a second way in for the same
`herdr.Watcher` (another process, or `/debug`), not something herdr
needs configuring for.

## Package map

| Package | Owns |
|---|---|
| `cmd/sheepdog` | Flags, subcommands (`serve`, `config`, `herdr watch`, `voice-test`, `brain-test`, `prompts`, `eval`, `scorecard`, `version`), wiring into `internal/app`. |
| `internal/app` | Assembles the daemon: config → event log → state store → herdr client + `Watcher` (or `internal/inject`'s mock) → brain (`--brain`, one provider per key: `gemini` over the pool, `deepseek` over `DEEPSEEK_API_KEY`, starting on `brain.provider`; `--brain=fake` wires two `braintest.Reflex` providers `fake`/`fake2`, dev-only scripted models with no network call) → supervisor → voice session (or a no-op pusher with `--voice=off`) → web server; owns the run/shutdown lifecycle. |
| `internal/config` | Loads `~/.config/sheepdog/config.toml` + `SHEEPDOG_*` env overrides + `--env-file`; `config show`/`config init` support. See [`CONFIG.md`](../CONFIG.md). |
| `internal/herdr` | The herdr unix socket client and the `Watcher` that turns its event streams into deduplicated, debounced `Transition`s. See [`docs/herdr-protocol.md`](herdr-protocol.md). |
| `internal/state` | The persisted state table: per-pane rows (alias, kind, workspace, state, state_since, current task, last block, the reported `agent_session` and cwd, the work journal — the last 20 entries), the single-slot pending action, session bookkeeping; atomic JSON persistence with a debounced hardware-durable fsync option. |
| `internal/eventlog` | The append-only JSONL event log every component writes to (decision D10): one file per UTC day, an in-memory ring for late subscribers, SSE fan-out. Every line carries a stable `id` (strictly increasing across days and restarts, ~Unix microseconds); `Since(id)` reads what came after an id from the ring or the day files (SSE resume), `Days`/`ReadDay` read the files back (the conversation history). |
| `internal/prose` | The plain-text helpers the model-facing code shares: what goes into a window (`TailBytes`, `TruncateRunes`), what comes out (`ExtractJSON`), and the D9 scrubber (`Spoken`). |
| `internal/earmetrics` | Pure functions over one spoken line (docs/iteration-2.md "Scorecard metrics"): `Measure` counts words, sentences (cut the way `prose.Spoken` does), dashes, semicolons, technical tokens (PR numbers, ms values, versions, git hashes, file paths) and checked-in jargon terms, and reports whether the line ends with a question or a short closer; `Aggregate` summarises a set of lines (mean/median/p90 words, share over 30 words, share with a dash, share ending closed, mean jargon per line). No I/O; `internal/scorecard` is the only caller. |
| `internal/scorecard` | Reads `<data_dir>/events/*.jsonl` back (via `internal/eventlog`'s `Event` shape, not by touching the daemon's log) and computes every docs/iteration-2.md scorecard metric over it: latency (answer latency, time to first word, `brain.turn`/`brain.step` distributions), `internal/earmetrics` over every spoken line versus every brain line the supervisor dropped, repeat-ask rate, lost-utterance rate, the brain-line drop rate, Ctrl-C sends, comprehension follow-ups and duplicate speech. `--session` splits the report at each `daemon.start`. Read-only and measurement-only: it never writes to the log and has no effect on daemon behaviour. `cmd/sheepdog`'s `scorecard` subcommand prints its report as markdown or JSON. |
| `internal/cli` | The read-only CLI query tool: detects codex/claude on `PATH` and runs one non-interactively and read-only, for the brain's `run_cli` lookups and for a background investigation. It captures the sub-agent's own session id (codex's `thread.started`, claude's `session_id`) and can resume it for a follow-up (`Query.Session`, the `*_resume_cmd` templates). See [`docs/cli-query-tools.md`](cli-query-tools.md). |
| `internal/workerlog` | Reads a worker's own session transcript ([`docs/roles.md`](roles.md) "Worker history"): locates it from herdr's `agent_session` (Claude Code: the reported path, else `~/.claude/projects/<cwd with / and . as ->/<id>.jsonl`, else any project dir holding `<id>.jsonl`; Codex: `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<id>.jsonl`; agy: unsupported), then scans only the lines appended since the pane's last read (a byte offset per pane; a first read or one more than 8 MB behind starts at the last 2 MB) into a `Digest`: the last assistant message, the instructions, tool counts and edited file base names since the last instruction. Never hands a whole file to anyone. |
| `internal/supervisor` | The daemon's coordinator: pure Go logic over the herdr client, the state table, the parsed prompt and the voice session's push channel — the only component that touches herdr (decision D1). It handles the voice's two tools (`utterance.go`, `stop.go`): `to_supervisor` runs the code fast paths — a bare answer to the one open prompt, a bare stop word — and then one `utterance` brain turn over the shepherd's verbatim words; `stop` runs the stop procedure. It owns the pending-action lifecycle (decision D3) and is the brain's `Host` (roster, ledger, reads, `prompt`/`send_keys`/`interrupt`/`promise`/`note` over the same pending paths), answering from the roster on `ErrFallback`; it runs the brain's `announce` turn on every block/finish, speaks the one line that event gets and applies the chattiness ceiling to the brain's judgment (`announce.go`, [`docs/roles.md`](roles.md) "Pushes"); and it owns the investigation jobs the brain's `investigate` tool starts (`investigate.go`: one live job per agent, the ledger in `session.investigations`, the `KindInvestigation` delivery turn, cancellation from the stop procedure) — the same seam as promises, since the roster, the workspace, the state file and the push are all here); and `ResetConversation` ("New conversation", `reset.go`) — the single entry point `POST /api/conversation/reset` and the voice bare-phrase confirm (`start over`, … → "Say \"yes, start over\" to confirm" → `yes, start over` / `confirm reset` within 20 s) both call: it resets the brain's window (`Brain.Reset`) and, through the `Pusher`'s optional `Reset(reason)` (the concrete session behind it, `voice.Switcher`), the voice model's own memory, and moves `conversation_started_at`. |
| `internal/brain` | The SVP (plan D7-revised, [`docs/roles.md`](roles.md)): a stateful agent loop over a function-calling model behind the `Model` seam — `GenaiModel` (`genai` `GenerateContent`, key pool rotating on 429) and `OpenAICompatModel` (DeepSeek's chat completions over plain `net/http`: tools as JSON Schema, tool calls with the API's ids, usage and finish reason mapped, HTTP errors verbatim; it implements `MessageModel`, so the loop hands it the window's messages and it replays DeepSeek's `reasoning_content` from `Message.Raw` for the messages the same provider and model produced) — registered per provider id, the pair `(provider, model)` snapshotted per turn and switchable between turns (`SetModel`, `Models` per provider); with `read_screen` / `run_cli` / `list_agents` / `recall` and the actions `prompt` / `send_keys` / `interrupt`; the one rail in the `send_keys` executor; provenance delimiters around worker output; one JSONL transcript per run under `<data_dir>/brain/`; the `ContextEngine` seam (ingest / assemble / compact / afterTurn, the memory plug point) with a default engine that prunes tool results to stubs and compacts the oldest half into a summary; two-priority turn serialisation (event turns yield to a waiting CEO turn and coalesce per agent); a sectioned system prompt from `prompts/svp/{SOUL,RULES}.md`; `Reset` ("New conversation", [`docs/roles.md`](roles.md)) empties the window and opens a fresh transcript segment, under the turn lock so nothing from before the reset lands in it — the roster, promises, notes and investigations are never in the window (rendered fresh from the `Host` every turn), so a reset leaves them alone. `braintest` holds the scripted model and in-memory host for tests. |
| `internal/voice` | The model-agnostic `VoiceSession` contract (plan §3.7); `internal/voice/gemini` implements it for Gemini Live (session resumption, context compression, key rotation), `internal/voice/gptlive` for GPT-Live (a lazy, idle-closed session in client delegation mode; `docs/gpt-live-notes.md`), `voice.Switcher` runs either behind stable channels so `PUT /api/voice/provider` switches at runtime and its own `Reset` ("New conversation") forwards to whichever is current — Gemini drops the resumption handle and reconnects, GPT-Live closes an open billed session (no recap) to reopen later — `internal/voice/wav` and `internal/voice/wsproto` are its audio/framing helpers, `internal/voice/echotools` is a test double. |
| `internal/web` | The HTTP surface on `web.bind:web.port` (`127.0.0.1` by default; a non-loopback bind needs `web.token`, docs/ws-contract.md "Auth"): `/voice` and `/debug` (embedded static pages, public), `GET /api/hello` (public: name/version/voice name, whether a token is needed, the `/ws` audio format), `POST /api/login` (public: trades a correct token for the `sheepdog_token` cookie), `/events` (SSE; `id:` is the event id, `Last-Event-ID` / `?since=<id>` replays from the log across days and restarts, up to 2 000 events, `?n=` the ring's tail), `GET /api/conversation?limit=&before=` (the shepherd/dog conversation rebuilt server-side from the log — `internal/web/conversation.go` ports `web/debug/conversation.js`'s merging rules — newest last, paged backwards by id; docs/ws-contract.md "History and resume"), `POST /api/conversation/reset {via}` (`app`\|`debug`\|`voice_page`; "New conversation" — resets the brain's transcript window and the voice session's memory, logs `conversation.reset`, answers `{ok, reset_id, at}`), the audio+control WebSocket, and the small JSON API (state — its `pending_action` carries `agent`, `summary`, `question`, `command` and `options[{index, label, keys, default}]` — inject, `/api/herdr/event`; `POST /api/pending/answer {id, option}` — answer the open question by tap, a shepherd input through the same rail and executor, 409 `{"error":"that question is no longer open"}` for a stale id; `GET /api/brain/transcript?since=<seq>&n=` — the brain's transcript from its in-memory store, one `seq` per message, `GET /api/brain/context` — the seqs in the model's window with pruned stubs, estimated tokens vs `window_tokens`, the last compaction and the prompt sections' sizes and sources, `GET /api/brain/system_prompt` — the assembled sections with text, `GET /api/brain/models[?provider=<id>][&refresh=1]` — the discovered model catalog of the current or named provider (Gemini's model list filtered to `generateContent`, `recommended` for the text models; DeepSeek's `GET /models`, all recommended; cached 10 minutes per provider; a failed fetch keeps the last list and sets `error`) with the current `provider`/`model` and every provider in `providers[]`, unavailable ones with the `reason`, `PUT /api/brain/model {provider, model}` — the runtime switch (400 with the reason for a provider without a key), persisted in `session.brain_provider` / `session.brain_model`; all five 503 with the reason when the brain is off; `GET /api/context/{brain,voice,timeline}` — the Context tab's captured contexts, "Context capture" below). In `--dev` mode `/testdata/<name>.wav` and `/testdata/brain/<name>.jsonl` serve the fixtures. |
| `internal/ctxcap` | The Context tab's capture ("Context capture" below): the brain's request log (`BrainLog`, a 200-step ring plus `<data_dir>/brain/<run>.requests.jsonl`), the voice context log (`VoiceLog`, per provider session), the section map / snapshot shape both share, the diff, and the link ids. Depends on nothing else in the daemon. |
| `internal/inject` | The debug-only fake event path: lets `/debug` (and tests) drive the whole supervisor chain without a real herdr or a microphone. |
| `internal/logging` | The tiny structured-event `Logger` interface every component logs through. |
| `web/voice`, `web/debug` | Vanilla JS + AudioWorklet pages served by `internal/web`; `web/debug/conversation.js` is the pure event→chat grouping behind the Conversation tab, `web/debug/swimlane.js` the event→lanes/arrows/chains layout behind the Swimlane tab, both over `web/debug/grouping.js` (tool call/result pairing generic over any `<prefix>.tool_call`/`<prefix>.tool_result` family, fragments, dividers), `web/debug/context.js` the section→pane model, diff marks, link lookup and timeline interleave behind the Context tab (DOM in `context-view.js`), and `web/debug/supervisor.js` the transcript+context→items and context→meter-segments logic behind the Supervisor tab (window marker placement, tool pairing by id, turn headers from `brain.turn`, compaction cards); `web/debug/dom.js` holds the shared DOM helpers (raw-JSON block, expand/collapse state, follow-scroll). All `node --test`. |
| `prompts/` | The embedded prompt workspace: the voice's `assistant.md`, the shepherd's `USER.md` and the brain's `svp/{SOUL,RULES}.md`, with the override `Loader`. |

## Context capture

The `/debug` **Context** tab shows, side by side, what each model is
given: on the left the brain's exact request at a model call, on the
right the voice model's context. Both panes use one shape (package
`internal/ctxcap`): the raw text as the model receives it, a section map
over it (sorted, contiguous byte ranges that cover the text exactly),
the provider-native body, estimated tokens per section (4 characters a
token), and a diff against the previous point of the same pane.

**Brain.** After every model call the loop records the step
(`Brain.Requests()`, a `ctxcap.BrainLog`): turn, step within the turn,
kind, provider/model, time, duration, the provider's usage when it
returned, and the request. The body is the chat completions JSON exactly
as sent for an OpenAI-compatible provider (DeepSeek, built by the same
code path — `requestFor` — and compared field for field with the request
an httptest endpoint received), and `{model, contents, config}` as the
daemon handed it to the SDK for Gemini (the SDK owns the wire bytes).
The section map comes from tracing `toContents` / `messagesOf` (the one
implementation of each, with an optional origin callback): a `meta`
section (model, thinking, tool choice), one section per system prompt
section (`role (SOUL.md)`, `tools`, `intent`, `record`, `promises`,
`announcing`, `rail`, `fallback`, `contract`, `USER.md`, the engine's
addition), one per tool
declaration, then every window message by kind (shepherd utterance,
voice said, request — the turn's own marked `current`, a daemon nudge,
brain tool call, brain tool result, push, worker event, promise, note,
daemon note, compaction summary, pruned stub), then the state block —
the last message of every request, cut into `roster`, `ledger`,
`investigations` and `time` (docs/iteration-2.md WP5: the system message
holds only the prompt files, so the provider's prefix cache covers the
system prompt, the tools and the conversation) — with worker output inside
the provenance delimiters cut out as its own `untrusted` piece. DeepSeek's
replayed `reasoning_content` (the current turn's only) is part of the
body and of the pane's estimate, but not of the window estimate
(`brain.window_tokens`), which counts message content only (CONFIG.md
`[deepseek]`). The tab's
"messages / window" line counts the rendered text (timestamps, role
headers, call JSON), so it runs a little higher than the Supervisor
meter's window estimate. The last 200 steps
stay in memory; every step is also appended to
`<data_dir>/brain/<run>.requests.jsonl` (one file per daemon run, a
`segment` field naming the transcript segment, which a "New
conversation" changes), and a restart loads the tail of the newest file,
numbering on from it. Nothing is redacted: the bodies hold no key (a test
asserts it).

**Voice.** Both live models keep their context server-side and it cannot
be read back, so the right pane is reconstructed from what the daemon
sent, and says so. Each provider wraps its connection in a recorder
(`capture.go`) that sees every message in wire order — Gemini's sends
all run under `sendMu`, GPT-Live's through one `Send` — plus what the
model produced that stays in its context. A `ctxcap.VoiceLog` (one per
daemon, each provider session a `VoiceSession`) holds the items: the
session opening (Gemini's setup with its system instructions and tools on
a fresh connection, a "resumed" marker on a resumed one; GPT-Live's
`session.start` with its instructions, the client-delegation setting and
its input items: the recap and the silent context held in standby),
microphone audio as `🎤 audio 3.2 s` placeholders (durations, never
bytes; GPT-Live's keep-alive silence folds into them) with what the model
heard of it, typed text, the model's output transcript, tool calls /
delegations and the responses sent back (with their scheduling), pushes
(`[system event…]` / `[system context…]` client content on Gemini;
commentary / thinking / instructions appends on GPT-Live), and markers
for session close, conversation reset, cancelled tool calls and a
provider switch. An epoch is one context: every fresh session starts
one; a turn point opens at each input the model reacts to (an utterance,
a tool response, a push, a marker). With `--voice=off` the /debug text
router and the skipped pusher record into a `text-router` session
instead, labelled as not reaching any model.

**Links.** Pieces that exist in both panes share a link id:
`call:<voice tool call id>` on the voice's tool call, its response and
the utterance it relayed (claimed when the call arrives), and on the
brain's request for that turn (`Request.Link`), with the turn's tool
calls, results and reply; `push:<id>` minted by `Supervisor.pushTagged`
(tag `push_id`) on the voice push and the brain's push message;
`evt:<id>` minted per herdr transition on the brain's worker event, the
announce turn's request and every push about that event (tag
`event_id`); a late reply's push also carries its call (`call_id`). A
shepherd utterance in the brain's window takes the call link of the
request that relayed it.

**API** (token-protected like the rest):

- `GET /api/context/brain?step=<n|latest>` → `{step: {step, run,
  segment, turn, turn_step, kind, trigger, agent, provider, model, ts,
  format, forced, window_tokens, tokens, usage, error, dur_ms}, source:
  "exact request", sections: [{key, label, class, group, start, end,
  tokens, path, role, seq, links, pruned, current, text}], body, tokens:
  {<class>: n, total}, diff}`; 404 before the first call or past the
  ring, 503 when the brain is off.
- `GET /api/context/voice?turn=<n|latest>` → `{turn: {turn, ts, epoch,
  session, provider, kind, label, first}, source: "reconstructed from
  what the daemon sent", evicted, sections (with `raw`: the outbound
  message), tokens, diff}`.
- `GET /api/context/timeline` → `{brain: [step metadata], voice: [turns],
  window_tokens, brain_off, voice_off}`, oldest first, for the scrubber
  to interleave by time.
- `diff` is `{prev, added, changed, pruned, compacted, removed: [{key,
  label, class, reason: compacted|reset|removed, text}]}`, by section
  key against the previous point of that pane; a different run or
  transcript segment (restart, reset) or voice epoch between the two
  makes what is gone `reset`.
- A `context.step` event (source `brain`, payload `pane: brain, step,
  turn, turn_step, kind, tokens`; source `voice`, payload `pane: voice,
  turn, kind, session, updated`) is logged at every recorded step or
  turn (and when a voice turn's model output completes), so the tab
  follows live over `/events`.

**Limits.** The voice pane cannot show what the provider did with the
context: Gemini's sliding-window compression, what a resumed session
still holds, or GPT-Live's own truncation are invisible; output audio is
represented by its transcript; token counts are estimates of the text
sent (audio is not counted). Capture is synchronous but cheap (one render
per model call, one mutex per voice item); the tab fetches and renders
only while it is shown.

## Event types

Every `logging.Logger.Log(source, type, paneID, payload)` call's `type`,
grepped from the source (`grep -rhoE '"[a-z_]+\.[a-z_]+"' internal cmd`)
and grouped by source. This is decision D10's ground truth; `/debug` is
only a viewer of it.

- **brain**: `brain.turn` (`trigger` ceo\|event, `kind` utterance\|stop\|announce\|investigation, `provider`, `model`, `steps`, `model_calls`, `dur_ms`, `fallback`, `tokens_in`, `tokens_out`, `window_tokens`, `acted`), `brain.step` (one model call: `step`, `provider`, `model`, `dur_ms`, `prompt_tokens`, `completion_tokens`, `reasoning_tokens` when the provider reports them, `cache_hit_tokens` when the provider served part of the prompt from its prefix cache (DeepSeek's `prompt_cache_hit_tokens`, Gemini's cached content; docs/iteration-2.md WP5), `function_calls` names, `tool_calls` count, `forced` when answer-only, `template` = kind), `brain.tool_call` / `brain.tool_result` (paired by `id`; `name`, `args` / `dur_ms`, `result` truncated to 1 KB, `error`), `brain.rail_blocked` (a refused `send_keys`, or any action tool named in an event turn: `agent`, `keys`, `trigger`, `reason`), `brain.compaction` (`before_tokens`, `after_tokens`, `dropped`; its summary call is not counted in `brain.turn.model_calls`), `brain.fallback` (`reason`: timeout, step_budget, model_error, rate_limited, bad_reply, queue_timeout, cancelled; `acted`), `brain.system_prompt` (once per run: `sections` with `chars` — the state block's included —, `total_chars` of the system message, `state_chars` of the state block, `provider`, `model`), `brain.rate_limited` (`provider` for DeepSeek, `key_index`, `keys`, `retry_after_ms`), `brain.transcript` (one per message appended to the run's transcript: `seq`, `kind`, `turn` — the Supervisor tab's cue to fetch `/api/brain/transcript?since=`; the message itself is not in the event), `brain.transcript_error`, `brain.yielded` (an event turn stopped before a model call because a CEO turn was waiting: `turn`, `kind`, `agent`, `event`, `step`, `steps_done`, `ceo_waiting`; followed by `brain.fallback {reason: yielded}`), `brain.event_coalesced` (a newer event turn about the same agent replaced a queued one: `agent`, `event`, `kind`; the replaced turn logs `brain.fallback {reason: coalesced}`), `brain.model_changed` (the runtime switch: `from_provider`, `from`, `provider`, `to`, `via` debug\|state; a `system` transcript message marks it), `brain.catalog` / `brain.catalog_error` (a model-list fetch: `provider`, `models` count or `error`, `refresh`, `cached`), `brain.reset` ("New conversation": `via`, `reset_id` — also names the fresh transcript segment), `context.step` (one recorded model call for the Context tab: `pane` brain, `step`, `turn`, `turn_step`, `kind`, `tokens`). `brain.turn.kind` is ask\|propose\|utterance\|stop for CEO turns and announce\|investigation for event turns (`trigger` event); `brain.tool_call.name` may be `promise`, `note` or `investigate`. `brain.fallback.reason` adds yielded and coalesced. Logged by the supervisor (source supervisor): `brain.push_decision` (one per announce judgment: `via` brain\|template\|ceiling (skipped before any turn), `urgency_requested` interrupt\|idle\|silent\|skip\|"", `urgency_applied`, `ceiling` = the chattiness level, `reason` brain\|ceiling\|promise\|promise_due\|stale\|late\|owed\|needs_user\|wait\|timeout\|fallback\|yielded\|coalesced, `late`, `agent`, `event`, `template` (the code sentence), `spoken`, `detail`, `relates_to`, `needs_user`, `kept` ids, `keeps_promise_ids`, and when a turn ran `wait` turn\|announce_wait (a finish waits for the turn's own budget, a block for `brain.announce_wait_s`) with `waited_ms`: a finish's `{wait: turn, via: brain}` is the brain's line spoken after the wait, `{wait: turn, via: template, reason: timeout}` the code sentence after the turn timed out), `brain.push_late` (a brain line that arrived after a block's code sentence was spoken: `turn`, `dur_ms`, `spoken`, `needs_user`, `relates_to`, `added` — true when it was spoken as the one addition, `needs_user` or `owed` on its `push_decision`), `brain.push_stale` (a line about a prompt that is no longer open: `turn`, `pending`, `spoken`), `brain.reply_pending` (a `to_supervisor` call returned a silent `pending` result because the brain's reply was not in within `brain.reply_wait_s`: `id` of the utterance, `waited_ms`, `call_id` the voice's tool call, `cancelled` when the voice dropped the call before the wait was up), `brain.reply_delivered` (that late reply pushed as `voice.push {answers_utterance: id}`: `id`, `dur_ms` since the call, `urgency` idle\|interrupt\|silent, `acted`, `fallback` when it is the fallback line of a failed turn: "Sorry, I couldn't get an answer just now…", or the roster sentence with the brain off), `brain.reply_stale` (the late reply landed after the shepherd's next request was accepted: `id`, `acted`; dropped unless it acted, then pushed silent)
- **cli**: `cli.detected`, `cli.run` (`backend`, `cmd`, `exit`, `dur_ms`, `output`, `killed`, `capped`, `session` — whether the backend reported one, never the id itself — and `resumed`)
- **investigation** (logged by the supervisor, source supervisor): `investigation.started` (`id`, `agent`, `source` cli\|worker, `backend`, `question`, `follow_up_of`, `resumed`), `investigation.done` (`id`, `agent`, `backend`, `dur_ms`, `bytes`, `session_id_present`), `investigation.cancelled` (`id`, `agent`, `reason` superseded\|stop\|shutdown, `dur_ms`), `investigation.error` (`id`, `agent`, `dur_ms`, `error`). The answer is delivered by a `brain.turn {kind: investigation, trigger: event}` whose line is pushed `idle` as `voice.push {answers_job}`.
- **daemon**: `daemon.start` (`brain` on\|off\|fake, `brain_provider`, `brain_model` — the live pair, the configured one until a switch was restored), `daemon.stop`, `daemon.brain_model_error` (the state file's `session.brain_provider` / `brain_model` was rejected: `provider`, `model`, `error`), `daemon.cli_error`, `daemon.drain_timeout`, `daemon.herdr_unavailable`, `daemon.voice_error`, `daemon.web_error`
- **eventlog**: `eventlog.subscriber_dropped`
- **herdr**: `herdr.state`, `herdr.transition`, `herdr.debounce_cancelled`, `herdr.dedup`, `herdr.fleet`, `herdr.list_error`, `herdr.read`, `herdr.send`, `herdr.snapshot_error`
- **supervisor**: `conversation.reset` ("New conversation": `via` app\|debug\|voice_page\|voice, `reset_id`, `at`; drives the `GET /api/conversation` divider and the SSE stream), `note.created`, `promise.created` (`id`, `text`, `agent`, `event`, `turn`, `due`), `promise.kept` (`id`, `text`, `agent`, `event`, `spoken`, `urgency`, `via` brain\|template, `chattiness`), `promise.expired` (`id`, `age_s`), `promise.cancelled` (`id`, `reason`; only in logs from before the cancel tool was removed), `promise.carried_over` (`ids`: promises made before this daemon run, announced once at startup as "Still watching for: …" and flagged so the briefing trailer and any later restart never repeat them), `pending.cancelled`, `pending.confirmed` (`id`, `answer`, `keys`, `via` brain\|fast_path\|confirm\|tap, `option` for a tap), `pending.created` (`id`, `kind`, `agent`, `summary`, `question`, `command`, `options` — `[{index, label, keys, default}]` — `yes_keys`, `no_keys`, `expires_at`), `pending.expired`, `pending.replaced`, `pending.revived`, `supervisor.block_error`, `supervisor.blocked_no_question`, `supervisor.briefing_repeat` (a `KindUtterance` request's roster sentence was byte-identical to the one last delivered within `supervisor.repeat_window_s`: `roster`, `age_s`), `supervisor.cancel_error`, `supervisor.confirm_error`, `supervisor.done_error`, `supervisor.done_question`, `supervisor.finished_suppressed`, `supervisor.note_error`, `supervisor.propose_error`, `supervisor.push_error`, `supervisor.push_quiet` (a push the chattiness level suppressed), `supervisor.push_skipped`, `supervisor.query_error`, `supervisor.reconcile_error`, `supervisor.reprompt` (a synthetic `blocked → blocked` transition raised: `after_ms`, `question`, `source` reprompt\|reconcile), `supervisor.reprompt_poll` (the post-answer poll ended: `polls`, `outcome` raised\|left_blocked\|same_prompt\|timeout), `supervisor.settings` (chattiness changed at runtime), `supervisor.signoff` (once per run, the first time an instruction the brain sent carries the sign-off trailer: `trailer`), `supervisor.state_error`, `worker.journal` (a work journal entry appended on a finish or block, before the announce turn: `agent`, `event` done\|idle\|blocked, `bytes` of transcript scanned, `files` edited base names), `worker.session_missing` (the worker's transcript could not be read, once per pane and reason: `agent`, `reason` not_reported\|unsupported_kind\|not_found\|unreadable), `supervisor.voice_initiative_ignored` (`text`: a `to_supervisor` call with no shepherd input — transcript, typed text or tap — since the previous call and the last spoken push; nothing runs, docs/roles.md "The one rail"), `supervisor.relay` (an accepted `to_supervisor` call: `heard` — the recorded words the utterance runs on — `said` — the voice model's argument, a hint only — `inputs` claimed, `input` voice\|typed, `call_id`), `supervisor.relay_fallback` (same fields: the recorded input had no words, so the argument was used), `supervisor.relay_by_daemon` (an utterance with no tool call and no speech from the voice for 4 s, relayed by the daemon and its reply pushed: `text`, `waited_ms`, `inputs`), `supervisor.utterance_coalesced` (a new utterance took over one whose brain turn was still queued, one turn runs over both: `n` utterances, `text`, `id`, `replaces`), `supervisor.echo_ignored` (only in logs from before WP3: a relay of a push just spoken), `supervisor.echo_dropped` (a voice input taken for the assistant's own speech picked up by the microphone — it began within 2.5 s of the voice's output and its words line up with what the voice just said — and not relayed: `heard`, `said`), `brain.rail_blocked` (also logged by the supervisor for the fast path and a tap: `agent`, `keys`, `trigger` fast_path\|tap, `reason`, `id`)
- **voice**: `voice.closed`, `voice.connect`, `voice.connect_error`, `voice.disconnected`, `voice.drop`, `voice.event_dropped`, `voice.goaway`, `voice.handle_dropped`, `voice.interrupted`, `voice.key_rotated`, `voice.muted`, `voice.push` (now carries `scheduling_sent` interrupt\|when_idle\|silent beside the requested `urgency`: what was actually sent once a speaking model downgraded an INTERRUPT; and `agent`, the alias the push is about, when it is about one), `voice.playback` (a /ws client playback report as the session saw it: `state` started\|drained\|flushed, `played_ms` the client played in the run, `run_ms` the audio the daemon sent in it; a flushed run cut short adds `cut: true`, `heard` — the words that played — and `said`, the run's whole output transcript; docs/ws-contract.md "Playback reports"), `voice.push_deferred` (a push or tool result downgraded from INTERRUPT to WHEN_IDLE because the model was speaking — which now includes the client still playing the reply: `kind` push\|tool_result, `name` for a tool result, `reason` always speaking, `urgency_requested`), `voice.client` (`attached`: a /ws client came or went, as the voice session sees it), `voice.push_held` / `voice.push_released` / `voice.push_dropped` (a spoken push held while no /ws client is attached: `text`, `urgency`, the push's tags, `reason` no_listener; released with `held_ms` when a client attaches, dropped with `age_ms` after 5 minutes; docs/ws-contract.md "Reconnect behaviour"), `voice.push_timeout`, `voice.rate_limited`, `voice.recycle`, `voice.resume`, `voice.stalled`, `voice.text`, `voice.tool_call`, `voice.tool_cancelled`, `voice.tool_result` (also gains `scheduling_sent`), `voice.transcript.in`, `voice.transcript.out`, `voice.turn`, `voice.usage`, `context.step` (a new voice turn point for the Context tab: `pane` voice, `turn`, `kind`, `session`, `updated` when the model's output in it completed)
- **web**: `web.audio_unheard` (model audio dropped because no /ws client was attached, once per model turn: `bytes` of the first chunk, `provider`), `web.client.backpressure`, `web.client.connect`, `web.client.disconnect`, `web.client.flush`, `web.client.mute`, `web.client.reject`, `web.client.silence`, `web.client.speaking` (`client_id`, `on`: the client's own speech detector, docs/ws-contract.md "Client speech detector"), `web.client.stats` (the ping-carried playback counters with `playback_age_ms`, the sample's age, and `audio_state`; `playback_reports`, the playback messages received), `web.herdr_event`, `web.inject`, `web.push_error`, `web.unauthorized` (`path`, `remote`; a request answered 401 under `web.token` — never the token), `web.utterance`, `web.utterance_unrouted`
