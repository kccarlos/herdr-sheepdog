# Architecture

See [`sheepdog-v1-plan.md`](../sheepdog-v1-plan.md) for the full design;
this is a short orientation for the code as built.

## Diagram (plan §2)

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
| Supervisor (daemon) | state table, pending action, event routing, tool implementations, all model calls that are not voice | speak; make risk judgements beyond "did I understand the target" |
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
| `cmd/sheepdog` | Flags, subcommands (`serve`, `config`, `herdr watch`, `voice-test`, `llm-test`, `ask-test`, `version`), wiring into `internal/app`. |
| `internal/app` | Assembles the daemon: config → event log → state store → herdr client + `Watcher` (or `internal/inject`'s mock) → cheap LLM client → supervisor → voice session (or a no-op pusher with `--voice=off`) → web server; owns the run/shutdown lifecycle. |
| `internal/config` | Loads `~/.config/sheepdog/config.toml` + `SHEEPDOG_*` env overrides + `--env-file`; `config show`/`config init` support. See [`CONFIG.md`](../CONFIG.md). |
| `internal/herdr` | The herdr unix socket client and the `Watcher` that turns its event streams into deduplicated, debounced `Transition`s. See [`docs/herdr-protocol.md`](herdr-protocol.md). |
| `internal/state` | The persisted state table: per-pane rows (alias, kind, workspace, state, state_since, current task, last block), the single-slot pending action, session bookkeeping; atomic JSON persistence with a debounced hardware-durable fsync option. |
| `internal/eventlog` | The append-only JSONL event log every component writes to (decision D10): one file per UTC day, an in-memory ring for late subscribers, SSE fan-out. |
| `internal/llm` | The cheap-LLM adapter (decision D7, text-in/text-out only): an OpenAI-compatible `Client` plus the `prompts/*.txt` templates (`SummarizeBlock`, `ResolveInstruction`, `SpokenAnswer`) and the D9 scrubber (`Spoken`). |
| `internal/cli` | The read-only CLI query tool: detects codex/claude on `PATH` and runs one non-interactively and read-only to back `ask()`'s deep path. See [`docs/cli-query-tools.md`](cli-query-tools.md). |
| `internal/supervisor` | The daemon's brain: pure Go logic over the herdr client, the state table, the cheap LLM templates and the voice session's push channel — the only component that touches herdr (decision D1); implements the five voice tools and the pending-action lifecycle (decision D3). |
| `internal/voice` | The model-agnostic `VoiceSession` contract (plan §3.7); `internal/voice/gemini` implements it for Gemini Live (session resumption, context compression, key rotation), `internal/voice/wav` and `internal/voice/wsproto` are its audio/framing helpers, `internal/voice/echotools` is a test double. |
| `internal/web` | The HTTP surface on `127.0.0.1`: `/voice` and `/debug` (embedded static pages), `/events` (SSE), the audio+control WebSocket, and the small JSON API (state, inject, `/api/herdr/event`). |
| `internal/inject` | The debug-only fake event path: lets `/debug` (and tests) drive the whole supervisor chain without a real herdr or a microphone. |
| `internal/logging` | The tiny structured-event `Logger` interface every component logs through. |
| `web/voice`, `web/debug` | Vanilla JS + AudioWorklet pages served by `internal/web`. |
| `prompts/` | Embedded prompt templates for the cheap LLM. |
| `plugin/` | The herdr plugin shim: manifest, hook script, install/uninstall, launchd plist template. See [`docs/herdr-protocol.md`](herdr-protocol.md) §8. |

## Event types

Every `logging.Logger.Log(source, type, paneID, payload)` call's `type`,
grepped from the source (`grep -rhoE '"[a-z_]+\.[a-z_]+"' internal cmd`)
and grouped by source. This is decision D10's ground truth; `/debug` is
only a viewer of it.

- **cli**: `cli.detected`, `cli.run`
- **daemon**: `daemon.start`, `daemon.stop`, `daemon.cli_error`, `daemon.drain_timeout`, `daemon.herdr_unavailable`, `daemon.voice_error`, `daemon.web_error`
- **eventlog**: `eventlog.subscriber_dropped`
- **herdr**: `herdr.state`, `herdr.transition`, `herdr.debounce_cancelled`, `herdr.dedup`, `herdr.fleet`, `herdr.list_error`, `herdr.read`, `herdr.send`, `herdr.snapshot_error`
- **llm**: `llm.call`, `llm.rate_limited`
- **supervisor**: `note.created`, `pending.cancelled`, `pending.confirmed`, `pending.created`, `pending.expired`, `pending.replaced`, `pending.revived`, `supervisor.ask_error`, `supervisor.block_error`, `supervisor.blocked_no_question`, `supervisor.cancel_error`, `supervisor.confirm_error`, `supervisor.note_error`, `supervisor.propose_ambiguous`, `supervisor.propose_error`, `supervisor.push_error`, `supervisor.push_skipped`, `supervisor.query_error`, `supervisor.state_error`
- **voice**: `voice.closed`, `voice.connect`, `voice.connect_error`, `voice.disconnected`, `voice.drop`, `voice.event_dropped`, `voice.goaway`, `voice.handle_dropped`, `voice.interrupted`, `voice.key_rotated`, `voice.muted`, `voice.push`, `voice.push_timeout`, `voice.rate_limited`, `voice.recycle`, `voice.resume`, `voice.stalled`, `voice.text`, `voice.tool_call`, `voice.tool_cancelled`, `voice.tool_result`, `voice.transcript.in`, `voice.transcript.out`, `voice.turn`, `voice.usage`
- **web**: `web.client.backpressure`, `web.client.connect`, `web.client.disconnect`, `web.client.flush`, `web.client.mute`, `web.client.reject`, `web.client.silence`, `web.client.stats`, `web.herdr_event`, `web.inject`, `web.push_error`, `web.utterance`, `web.utterance_unrouted`
