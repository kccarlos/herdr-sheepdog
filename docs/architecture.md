# Architecture

This is a short orientation for the code **as built at `main`**. The
design it implements is partly superseded: the agreed target (one
decision-maker, no template LLM, investigations, the names shepherd /
dog / flock) is in [`docs/roles.md`](roles.md) and the decisions and
order of work in [`docs/issues-2026-09-18.md`](issues-2026-09-18.md).
Where this page and `roles.md` differ, `roles.md` is the intent and this
page is what runs today. [`sheepdog-v1-plan.md`](../sheepdog-v1-plan.md)
is the original plan, kept for history.

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
│        │ 5 tools + push                                      │
│   Supervisor ── state table ── event log (JSONL) ── SSE      │
│        │  │           │                                      │
│        │  └─ brain (Gemini or DeepSeek + tools, transcript)  │
│   herdr client   cheap LLM (OpenAI-compatible)                │
│   (unix socket)  CLI query tool (codex exec / claude -p,     │
│        │          read-only, timeout)                        │
└────────┼─────────────────────────────────────────────────────┘
         │ ~/.config/herdr/herdr.sock            ▲
   herdr ── worker agents (Codex / Claude Code / … in panes)    │
         │                                                      │
         └── plugin hook shim (plugin/) ── POST /api/herdr/event┘
```

Three layers, hard boundaries (plan §2):

| Layer | Owns | Never does |
|---|---|---|
| Voice model (Gemini 3.8 Live) | hearing, speaking, turn-taking, deciding *whether* to call a tool | touch herdr, hold task state, plan multi-step work |
| Supervisor (daemon) | state table, pending action, event routing, tool implementations, all model calls that are not voice; its brain (`internal/brain`, [`docs/roles.md`](roles.md)) reasons over the transcript with tools behind `ask`/`propose` | speak; bypass a worker's approval gate (the one rail: `send_keys` only on the CEO's words, from the prompt's option set) |
| Workers (herdr panes) | actually changing code; their own approval prompts | — |

The Gemini session lives in the daemon, not the browser; the browser is
a dumb audio pipe. herdr feeds the daemon over two channels: the unix
socket subscription (per-pane) and the plugin hook shim (fleet-wide,
`plugin/`), both landing on the same `herdr.Watcher`/`/api/herdr/event`
path — see [`docs/herdr-protocol.md`](herdr-protocol.md) §8 for why both
exist.

## Package map

| Package | Owns |
|---|---|
| `cmd/sheepdog` | Flags, subcommands (`serve`, `config`, `herdr watch`, `voice-test`, `llm-test`, `ask-test`, `brain-test`, `version`), wiring into `internal/app`. |
| `internal/app` | Assembles the daemon: config → event log → state store → herdr client + `Watcher` (or `internal/inject`'s mock) → cheap LLM client → brain (`--brain`, one provider per key: `gemini` over the pool, `deepseek` over `DEEPSEEK_API_KEY`, starting on `brain.provider`; `--brain=fake` wires two `braintest.Reflex` providers `fake`/`fake2`, dev-only scripted models with no network call) → supervisor → voice session (or a no-op pusher with `--voice=off`) → web server; owns the run/shutdown lifecycle. |
| `internal/config` | Loads `~/.config/sheepdog/config.toml` + `SHEEPDOG_*` env overrides + `--env-file`; `config show`/`config init` support. See [`CONFIG.md`](../CONFIG.md). |
| `internal/herdr` | The herdr unix socket client and the `Watcher` that turns its event streams into deduplicated, debounced `Transition`s. See [`docs/herdr-protocol.md`](herdr-protocol.md). |
| `internal/state` | The persisted state table: per-pane rows (alias, kind, workspace, state, state_since, current task, last block), the single-slot pending action, session bookkeeping; atomic JSON persistence with a debounced hardware-durable fsync option. |
| `internal/eventlog` | The append-only JSONL event log every component writes to (decision D10): one file per UTC day, an in-memory ring for late subscribers, SSE fan-out. |
| `internal/llm` | The cheap-LLM adapter (decision D7, text-in/text-out only): an OpenAI-compatible `Client` plus the `prompts/*.txt` templates (`SummarizeBlock`, `ResolveInstruction`, `SpokenAnswer`) and the D9 scrubber (`Spoken`). |
| `internal/cli` | The read-only CLI query tool: detects codex/claude on `PATH` and runs one non-interactively and read-only to back `ask()`'s deep path. See [`docs/cli-query-tools.md`](cli-query-tools.md). |
| `internal/supervisor` | The daemon's coordinator: pure Go logic over the herdr client, the state table, the cheap LLM templates and the voice session's push channel — the only component that touches herdr (decision D1); implements the five voice tools and the pending-action lifecycle (decision D3), routes `ask`/`propose` through the brain when one is attached and is its `Host` (roster, ledger, reads, actions over the same pending paths), falling back to the template path on `ErrFallback`; runs the brain's `announce` turn on every block/finish and applies the chattiness ceiling to its judgment (`announce.go`, [`docs/roles.md`](roles.md) "Push judgment"). |
| `internal/brain` | The SVP (plan D7-revised, [`docs/roles.md`](roles.md)): a stateful agent loop over a function-calling model behind the `Model` seam — `GenaiModel` (`genai` `GenerateContent`, key pool rotating on 429) and `OpenAICompatModel` (DeepSeek's chat completions over plain `net/http`: tools as JSON Schema, tool calls with the API's ids, usage and finish reason mapped, HTTP errors verbatim; it implements `MessageModel`, so the loop hands it the window's messages and it replays DeepSeek's `reasoning_content` from `Message.Raw` for the messages the same provider and model produced) — registered per provider id, the pair `(provider, model)` snapshotted per turn and switchable between turns (`SetModel`, `Models` per provider); with `read_screen` / `run_cli` / `list_agents` / `recall` and the actions `prompt` / `send_keys` / `interrupt`; the one rail in the `send_keys` executor; provenance delimiters around worker output; one JSONL transcript per run under `<data_dir>/brain/`; the `ContextEngine` seam (ingest / assemble / compact / afterTurn, the memory plug point) with a default engine that prunes tool results to stubs and compacts the oldest half into a summary; two-priority turn serialisation (event turns yield to a waiting CEO turn and coalesce per agent); a sectioned system prompt from `prompts/svp/{SOUL,RULES}.md`. `braintest` holds the scripted model and in-memory host for tests. |
| `internal/voice` | The model-agnostic `VoiceSession` contract (plan §3.7); `internal/voice/gemini` implements it for Gemini Live (session resumption, context compression, key rotation), `internal/voice/wav` and `internal/voice/wsproto` are its audio/framing helpers, `internal/voice/echotools` is a test double. |
| `internal/web` | The HTTP surface on `127.0.0.1`: `/voice` and `/debug` (embedded static pages), `/events` (SSE), the audio+control WebSocket, and the small JSON API (state, inject, `/api/herdr/event`; `GET /api/brain/transcript?since=<seq>&n=` — the brain's transcript from its in-memory store, one `seq` per message, `GET /api/brain/context` — the seqs in the model's window with pruned stubs, estimated tokens vs `window_tokens`, the last compaction and the prompt sections' sizes and sources, `GET /api/brain/system_prompt` — the assembled sections with text, `GET /api/brain/models[?provider=<id>][&refresh=1]` — the discovered model catalog of the current or named provider (Gemini's model list filtered to `generateContent`, `recommended` for the text models; DeepSeek's `GET /models`, all recommended; cached 10 minutes per provider; a failed fetch keeps the last list and sets `error`) with the current `provider`/`model` and every provider in `providers[]`, unavailable ones with the `reason`, `PUT /api/brain/model {provider, model}` — the runtime switch (400 with the reason for a provider without a key), persisted in `session.brain_provider` / `session.brain_model`; all five 503 with the reason when the brain is off). In `--dev` mode `/testdata/<name>.wav` and `/testdata/brain/<name>.jsonl` serve the fixtures. |
| `internal/inject` | The debug-only fake event path: lets `/debug` (and tests) drive the whole supervisor chain without a real herdr or a microphone. |
| `internal/logging` | The tiny structured-event `Logger` interface every component logs through. |
| `web/voice`, `web/debug` | Vanilla JS + AudioWorklet pages served by `internal/web`; `web/debug/conversation.js` is the pure event→chat grouping behind the Conversation tab, `web/debug/swimlane.js` the event→lanes/arrows/chains layout behind the Swimlane tab, both over `web/debug/grouping.js` (tool call/result pairing generic over any `<prefix>.tool_call`/`<prefix>.tool_result` family, fragments, dividers), and `web/debug/supervisor.js` the transcript+context→items and context→meter-segments logic behind the Supervisor tab (window marker placement, tool pairing by id, turn headers from `brain.turn`, compaction cards); `web/debug/dom.js` holds the shared DOM helpers (raw-JSON block, expand/collapse state, follow-scroll). All `node --test`. |
| `prompts/` | Embedded prompt templates for the cheap LLM and the voice model; `prompts/svp/` holds the brain's SOUL.md and RULES.md. |
| `plugin/` | The herdr plugin shim: manifest, hook script, install/uninstall, launchd plist template. See [`docs/herdr-protocol.md`](herdr-protocol.md) §8. |

## Event types

Every `logging.Logger.Log(source, type, paneID, payload)` call's `type`,
grepped from the source (`grep -rhoE '"[a-z_]+\.[a-z_]+"' internal cmd`)
and grouped by source. This is decision D10's ground truth; `/debug` is
only a viewer of it.

- **brain**: `brain.turn` (`trigger` ceo\|event, `kind` ask\|propose, `provider`, `model`, `steps`, `model_calls`, `dur_ms`, `fallback`, `tokens_in`, `tokens_out`, `window_tokens`, `acted`), `brain.step` (one model call: `step`, `provider`, `model`, `dur_ms`, `prompt_tokens`, `completion_tokens`, `reasoning_tokens` when the provider reports them, `function_calls` names, `tool_calls` count, `forced` when answer-only, `template` = kind), `brain.tool_call` / `brain.tool_result` (paired by `id`; `name`, `args` / `dur_ms`, `result` truncated to 1 KB, `error`), `brain.rail_blocked` (a refused `send_keys`, or any action tool named in an event turn: `agent`, `keys`, `trigger`, `reason`), `brain.compaction` (`before_tokens`, `after_tokens`, `dropped`; its summary call is not counted in `brain.turn.model_calls`), `brain.fallback` (`reason`: timeout, step_budget, model_error, rate_limited, bad_reply, queue_timeout, cancelled; `acted`), `brain.system_prompt` (once per run: `sections` with `chars`, `total_chars`, `provider`, `model`), `brain.rate_limited` (`provider` for DeepSeek, `key_index`, `keys`, `retry_after_ms`), `brain.transcript` (one per message appended to the run's transcript: `seq`, `kind`, `turn` — the Supervisor tab's cue to fetch `/api/brain/transcript?since=`; the message itself is not in the event), `brain.transcript_error`, `brain.yielded` (an event turn stopped before a model call because a CEO turn was waiting: `turn`, `kind`, `agent`, `event`, `step`, `steps_done`, `ceo_waiting`; followed by `brain.fallback {reason: yielded}`), `brain.event_coalesced` (a newer event turn about the same agent replaced a queued one: `agent`, `event`, `kind`; the replaced turn logs `brain.fallback {reason: coalesced}`), `brain.model_changed` (the runtime switch: `from_provider`, `from`, `provider`, `to`, `via` debug\|state; a `system` transcript message marks it), `brain.catalog` / `brain.catalog_error` (a model-list fetch: `provider`, `models` count or `error`, `refresh`, `cached`). `brain.turn.kind` is ask\|propose for CEO turns and announce for event turns (`trigger` event); `brain.tool_call.name` may be `promise`. `brain.fallback.reason` adds yielded and coalesced. Logged by the supervisor (source supervisor): `brain.push_decision` (one per announce judgment: `via` brain\|template\|ceiling (skipped before any turn), `urgency_requested` interrupt\|idle\|silent\|skip\|"", `urgency_applied`, `ceiling` = the chattiness level, `reason` brain\|ceiling\|duplicate\|promise\|promise_due\|wait\|timeout\|fallback\|yielded\|coalesced, `late`, `agent`, `event`, `template`, `template_spoken`, `spoken`, `detail`, `relates_to`, `needs_user`, `kept` ids, `keeps_promise_ids`)
- **cli**: `cli.detected`, `cli.run`
- **daemon**: `daemon.start` (`brain` on\|off\|fake, `brain_provider`, `brain_model` — the live pair, the configured one until a switch was restored), `daemon.stop`, `daemon.brain_model_error` (the state file's `session.brain_provider` / `brain_model` was rejected: `provider`, `model`, `error`), `daemon.cli_error`, `daemon.drain_timeout`, `daemon.herdr_unavailable`, `daemon.voice_error`, `daemon.web_error`
- **eventlog**: `eventlog.subscriber_dropped`
- **herdr**: `herdr.state`, `herdr.transition`, `herdr.debounce_cancelled`, `herdr.dedup`, `herdr.fleet`, `herdr.list_error`, `herdr.read`, `herdr.send`, `herdr.snapshot_error`
- **llm**: `llm.call` (payload `template` is one of `summarize_block`, `resolve_instruction`, `spoken_answer`, `summarize_done`), `llm.rate_limited`
- **supervisor**: `note.created`, `promise.created` (`id`, `text`, `agent`, `event`, `turn`, `due`), `promise.kept` (`id`, `text`, `agent`, `event`, `spoken`, `urgency`, `via` brain\|template, `chattiness`), `promise.expired` (`id`, `age_s`), `promise.cancelled` (`id`, `reason`), `pending.cancelled`, `pending.confirmed`, `pending.created`, `pending.expired`, `pending.replaced`, `pending.revived`, `supervisor.ask_error`, `supervisor.block_error`, `supervisor.blocked_no_question`, `supervisor.cancel_error`, `supervisor.confirm_error`, `supervisor.done_error`, `supervisor.done_question`, `supervisor.finished_suppressed`, `supervisor.note_error`, `supervisor.propose_ambiguous`, `supervisor.propose_error`, `supervisor.push_error`, `supervisor.push_quiet` (a push the chattiness level suppressed), `supervisor.push_skipped`, `supervisor.query_error`, `supervisor.settings` (chattiness changed at runtime), `supervisor.state_error`
- **voice**: `voice.closed`, `voice.connect`, `voice.connect_error`, `voice.disconnected`, `voice.drop`, `voice.event_dropped`, `voice.goaway`, `voice.handle_dropped`, `voice.interrupted`, `voice.key_rotated`, `voice.muted`, `voice.push`, `voice.push_timeout`, `voice.rate_limited`, `voice.recycle`, `voice.resume`, `voice.stalled`, `voice.text`, `voice.tool_call`, `voice.tool_cancelled`, `voice.tool_result`, `voice.transcript.in`, `voice.transcript.out`, `voice.turn`, `voice.usage`
- **web**: `web.client.backpressure`, `web.client.connect`, `web.client.disconnect`, `web.client.flush`, `web.client.mute`, `web.client.reject`, `web.client.silence`, `web.client.stats`, `web.herdr_event`, `web.inject`, `web.push_error`, `web.utterance`, `web.utterance_unrouted`
