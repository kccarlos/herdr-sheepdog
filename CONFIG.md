# sheepdog configuration

Configuration is read from `~/.config/sheepdog/config.toml` when it
exists (create one with `sheepdog config init`), then overridden by
`SHEEPDOG_*` environment variables (and, for the keys, `DEEPSEEK_API_KEY`,
the Gemini pool `GEMINI_API_KEY`/`GEMINI_API_KEY_1`…`GEMINI_API_KEY_9`
and the Vertex AI `VERTEX_API_KEY`/`VERTEX_PROJECT`/`VERTEX_LOCATION`).
The environment always wins over the file. See `internal/config/` for the
authoritative source; this file is checked against it in
`internal/config/doc_test.go` — a struct field without a matching entry
here fails `go test`.

`sheepdog serve --env-file PATH` (default `./.env` if present, else
`~/.config/sheepdog/.env`, the secret file `sheepdog init` writes) loads
`KEY=VALUE` lines into the process environment *before* the config file
and its `SHEEPDOG_*` overrides are read, so any key below with an env
override can also come from a `.env` file in a dev checkout. See
`.env.example`. `sheepdog config show` prints the fully-resolved config
with secrets redacted (as counts); `sheepdog config init` writes a
commented template of every key below to `~/.config/sheepdog/config.toml`
(or `--path`), refusing to overwrite an existing file without `--force`.

`sheepdog init` is the guided setup on top of it: it checks herdr (and says
how to install it), asks for one key — Gemini AI Studio by default, or
DeepSeek / any OpenAI-compatible orchestrator (`[[brain.openai_compat]]`)
plus a Gemini key for the voice — validates each with a free model-list
call, and writes the keys to `~/.config/sheepdog/.env` (or `--env-file`)
and, only when there is none yet, `config.toml` (both mode 0600). A new
an existing `config.toml` is never rewritten (init prints the lines to
add). Keys are read without echo and never printed. `--non-interactive`
takes the keys from the environment (for scripts and tests).
`sheepdog doctor` checks the result and prints one line per check with
its fix: herdr's socket, every configured key (live; DeepSeek's balance and
an OpenRouter key's credit where the API shows them; AI Studio does not
expose its quota), a one-call tool-calling probe on each OpenAI-compatible
orchestrator (a few tokens; `--no-probe` skips it), the orchestrator and
voice providers, the web port (free, this daemon, or another program),
who can reach the daemon (the bind), push (the relay's health, or the `[push.apns]` key) and the secret files' permissions.
It exits 1 on any failure.
`sheepdog pair` asks the running daemon for a new one-time pairing code
and prints it as a QR code: the app scans it, and so does any phone's
camera (it opens `/voice` in the browser). It holds the daemon's
Tailscale and LAN addresses, `web.advertise`, the certificate's
fingerprint and a code that works once and expires after 10 minutes
(docs/dev/ws-contract.md "Pairing"); on a terminal it draws the code in explicit colours, so it scans on a light or a dark theme (`--plain` and `--invert` otherwise). It needs a running daemon with a non-loopback bind.
`sheepdog start` runs the daemon in the background and `stop`, `restart`,
`status [--json]` and `logs [-f] [-n N]` manage it, with or without the
login service. `sheepdog service install` runs it as a user service (a
launchd agent `~/Library/LaunchAgents/io.github.kccarlos.sheepdog.plist`
on macOS with `KeepAlive`; a systemd unit
`~/.config/systemd/user/sheepdog.service` on Linux with
`Restart=on-failure`): `sheepdog serve --background --env-file
~/.config/sheepdog/.env`, plus `--config` when one is given, with the
installing shell's `PATH`. Without the service, `start` runs the same
command line as a detached process. Keys belong in `~/.config/sheepdog/.env`;
a daemon that loaded another env file (`--env-file`, or the `./.env` of its
working directory) records that path (never a value) in
`<data_dir>/env-file.json`, and `restart`, `start` and the restart after an
upgrade pass it back as `--env-file <absolute path>` (an `--env-file` you
give wins). `service install` keeps the default file and warns when the
recorded one differs, because a login service should not depend on a file in
a project directory. `service status` and `service
uninstall` manage the unit; `install --dry-run` prints it without
changing anything. One daemon runs per `data_dir`: a second `serve` or
`start` says `sheepdog is already running (pid N, http://…)`.

A config error names the file, the line and the key, and what is wrong:

```
config: /Users/me/.config/sheepdog/config.toml line 5, key supervisor.chattiness: "loud" is not valid (want one of quiet, normal, chatty)
config: /Users/me/.config/sheepdog/config.toml line 2, key web.port: wrong type: want an integer, got a string (in quotes)
```

Two keys — `[aliases]` and the `[cli]` argv templates (`codex_cmd`,
`claude_cmd`, `codex_resume_cmd`, `claude_resume_cmd`) — are TOML-only:
they are not simple scalars an environment variable could sanely hold,
so they have no `SHEEPDOG_*` override.

## Top level

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `data_dir` | `SHEEPDOG_DATA_DIR` | `~/.local/share/sheepdog` | Where the daemon keeps its own files: the JSONL event log and (unless overridden) the state file. |
| `debounce_ms` | `SHEEPDOG_DEBOUNCE_MS` | `3000` | How long a herdr `blocked` state must persist before it becomes a transition (plan §3.1); filters transient flicker. |
| `snapshot_interval_s` | `SHEEPDOG_SNAPSHOT_INTERVAL_S` | `30` | Period of the herdr `session.snapshot` diff loop that catches panes the event stream missed. |
| `pending_ttl_s` | `SHEEPDOG_PENDING_TTL_S` | `120` | How long an in-flight instruction's pending action stays cancellable before it expires. An unanswered agent question has no TTL: it lives while the agent is blocked at that prompt (herdr state and screen), and the bare yes/no fast path answers it only within a minute of its announcement; later the words go to the orchestrator. |
| `aliases` | *(TOML only)* | `{}` | Table mapping a herdr pane id to a spoken alias, e.g. `"w9:p1" = "auth"`. Pinned aliases win over derived ones, for new agents and for rows already on disk. |

## `[herdr]`

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `herdr.backend` | `SHEEPDOG_HERDR_BACKEND` | `auto` | Which multiplexer the agents run in: `herdr` (the herdr app), `hc` (herdr-core, the new herdr), or `auto`: at startup the daemon asks both sockets and takes the one that answers; when both answer it takes `herdr`, and when neither does it waits for `herdr`. `sheepdog setup` writes `SHEEPDOG_HERDR_BACKEND` when the running setup makes the choice clear, and asks when both answer. The choice is logged as `daemon.herdr_backend`. On herdr-core some things are not available yet: renaming or closing a space, worktrees and renaming an agent ([`docs/dev/design/herdr-core-backend.md`](docs/dev/design/herdr-core-backend.md)). |
| `herdr.socket` | `SHEEPDOG_HERDR_SOCKET` | `~/.config/herdr/herdr.sock` | Path of the herdr unix domain socket (`--herdr=socket`, the default; ignored under `--herdr=mock`). |
| `herdr.bin` | `SHEEPDOG_HERDR_BIN` | *(empty)* | The herdr binary the daemon runs for the iPhone app's live agent windows (`herdr terminal session control`, one child process per open window, pointed at `herdr.socket`). Empty finds `herdr` on `PATH`, then `~/.local/bin/herdr`. The live window needs herdr 0.9.2 or newer; with an older herdr the app shows the read-only screen instead. |
| `herdr.hc_socket` | `SHEEPDOG_HC_SOCKET` | hc's own: `$HC_SOCKET`, else `<session>.sock` in `$XDG_RUNTIME_DIR/herdr-core/`, else `~/.local/state/herdr-core/<session>/<session>.sock` (`HC_SESSION`, default `default`) | Path of herdr-core's socket, for `herdr.backend = "hc"` or `auto`. |

## `[state]`

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `state.path` | `SHEEPDOG_STATE_PATH` | `~/.local/share/sheepdog/state.json` | JSON file the persisted state table is written to. It does not follow a changed `data_dir`: a second (scratch) daemon must set both. |
| `state.full_fsync` | `SHEEPDOG_STATE_FULL_FSYNC` | `false` | Turn on the hardware-durable fsync pass after a mutation (debounced to one flush per ~100ms burst, plus a final one on shutdown). Off by default: on macOS the durable path is `F_FULLFSYNC`, dramatically slower than the plain fsync used otherwise (see `internal/state`); turn it on only if the state table must survive a power loss, not just a process crash. |

## `[gemini]`

The Gemini Live voice session.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `gemini.api_keys` | `GEMINI_API_KEY`, `GEMINI_API_KEY_1`…`GEMINI_API_KEY_9` | `[]` | The Gemini Live key pool; the voice session binds one key per connection and rotates to the next on a rate-limit error. Normally left out of the TOML file and set via the environment (or `--env-file`) so keys never sit in a checked-in file; any env key found replaces the whole pool from the file. |
| `gemini.model` | `SHEEPDOG_GEMINI_MODEL` | `gemini-3.8-live` | The Live API model name. |
| `gemini.voice` | `SHEEPDOG_GEMINI_VOICE` | `Puck` | The prebuilt voice name. The setup page's voice picker writes the model and the voice to the env file ([docs/models.md](docs/models.md#choosing-the-voice-on-the-setup-page)). |
| `gemini.stall_after_s` | `SHEEPDOG_GEMINI_STALL_AFTER_S` | `10` | Seconds an utterance may go without any model response (tool call or audio) before `voice.stalled` is logged and the pages show "stalled". The second stall on one connection (or the second idle push that outwaits the 30 s turn timeout) recycles the connection: a reconnect with the still-valid resumption handle (`voice.recycle`), keeping the conversation. |
| `gemini.start_sensitivity` | `SHEEPDOG_GEMINI_START_SENSITIVITY` | `low` | Live's automatic activity detection, start of speech: `low`, `high` or `default` (the server's own). `low` needs clearer speech before a turn starts, so room noise is not transcribed as words ("fishing", "Discord" were heard from noise at the default). |
| `gemini.end_sensitivity` | `SHEEPDOG_GEMINI_END_SENSITIVITY` | `default` | End of speech: `low` (waits longer before closing the turn), `high` (closes sooner) or `default`. |
| `gemini.briefing_channel` | `SHEEPDOG_GEMINI_BRIEFING_CHANNEL` | `function_response` | How the daemon's and the orchestrator's words reach the Live model: `function_response` (a function response to a call the model never made, named `briefing`, labelled with who it comes from, the time and how it relates to earlier briefings; docs/dev/design/dual-brain.md) or `user_turn` (the older marked user-role client turn). |
| `gemini.backend` | `SHEEPDOG_GEMINI_BACKEND` | `aistudio` | Where the Gemini Live connection goes: `aistudio` (Google AI Studio, the `gemini.api_keys` pool) or `vertex` (Vertex AI, `[gemini.vertex]`). Only the connection differs: session, pushes, re-seed, tools and playback are the same. The `/debug` voice picker labels the backend ("Gemini Live (Vertex)") and `voice.connect` carries `backend`. The orchestrator uses the AI Studio pool as provider `gemini`; with `[gemini.vertex]` key and project set it can also run on Vertex as provider `vertex`, whatever this says (`[brain]`). |

### `[gemini.vertex]`

Gemini Live on Vertex AI, used when `gemini.backend = "vertex"`. One key,
no pool: a rate limit (429 / `RESOURCE_EXHAUSTED`) logs
`voice.rate_limited`, waits (the server's retry delay, else 30 s) and
retries the same key, never a hot retry. The Live socket is
`wss://{location}-aiplatform.googleapis.com/ws/google.cloud.aiplatform.v1beta1.LlmBidiService/BidiGenerateContent`
with the key in an `x-goog-api-key` header; `gemini.model` stays the bare
id (`gemini-3.8-live`) and the daemon names it
`projects/{project}/locations/{location}/publishers/google/models/{model}`
in the setup. See `docs/dev/research/vertex-live-spike-2026-09-29.md`.

The same key and project also back the orchestrator's `vertex`
provider ("Gemini (Vertex)" in the `/debug` picker), whatever
`gemini.backend` says: with both set it is registered next to `gemini`
and `deepseek`, and `sheepdog brain-test --provider vertex` runs a turn on
it. The orchestrator calls the native `generateContent`
(`https://aiplatform.googleapis.com/v1beta1/projects/{project}/locations/global/publishers/google/models/{model}:generateContent`,
key in `x-goog-api-key`) at location **`global`, always**:
`gemini.vertex.location` is for the Live model only (the text models are
served at `global` and not at `us-central1`; `gemini-3.8-live` is the
other way round). It is the
same adapter as `gemini` (bare model ids such as `gemini-3.8-flash`;
thought signatures replayed with the model's tool calls), one key: a 429
logs `brain.rate_limited {provider: "vertex"}` and fails that call, no
retry. Vertex's model listing refuses an API key, so the picker shows a
fixed list: `gemini-3.8-flash`, `gemini-3.5-flash`,
`gemini-3.5-flash-lite`. Paid per token on the project's billing account.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `gemini.vertex.api_key` | `VERTEX_API_KEY` | `""` | The Vertex AI API key (restricted to `aiplatform.googleapis.com`, bound to a service account). Normally set via the environment or `--env-file`; an empty env value does not clear a file value. Never logged: `config show` prints `set`/`unset`. |
| `gemini.vertex.project` | `VERTEX_PROJECT` | `""` | The Google Cloud project id. |
| `gemini.vertex.location` | `VERTEX_LOCATION` | `us-central1` | The Vertex AI location. `gemini-3.8-live` is served from `us-central1` and the `us` multi-region, not from `global` (the setup is refused with close 1008 "Publisher model … was not found"). |

The setup page's "Google Cloud (Vertex AI)" block writes `project` and
`location` here and the key to the env file as `VERTEX_API_KEY`; it removes
`VERTEX_PROJECT` and `VERTEX_LOCATION` from the env file, which would
otherwise override them (a variable exported in the shell that starts the
daemon still wins). Choosing "Gemini Live on Vertex AI" also sets
`SHEEPDOG_GEMINI_BACKEND=vertex` in the env file (`aistudio` for Gemini Live
on AI Studio). A saved key is reused only for the project it was saved for.

## `[voice]`

The voice's spoken half, as the operator meets it
([`docs/dev/roles.md`](docs/dev/roles.md) "Names").

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `voice.name` | `SHEEPDOG_VOICE_NAME` | `Sheepdog` | What the voice calls itself. Substituted into `prompts/assistant.md` (and `prompts/assistant-gptlive.md`) as `{name}`, so it is the name the voice model introduces itself by and answers to; the aliases of the agents it herds are separate (`[aliases]`). |
| `voice.provider` | `SHEEPDOG_VOICE_PROVIDER` | `gemini` | The voice model the daemon starts on: `gemini` (Gemini Live, `[gemini]`; idle costs nothing, the session stays open) or `openai` (GPT-Live, `[voice.openai]`; **billed $0.05 per minute of open session**, so it is opened only for speech or a push and closed when idle). The initial value only: `PUT /api/voice/provider {"provider": "openai"}` or the `/debug` voice picker switches at runtime (the current voice session closes, the new provider opens on the next need; `voice.provider_changed {from, to, via}`), and the pick is kept in the state file's `session.voice_provider`, which wins over this on restart. `serve --voice=gemini` / `--voice=openai` force the initial provider for one run; `--voice=on` (the default) uses this. |

### `[voice.openai]`

GPT-Live (`gpt-live-1`) over the Live WebSocket in **client
delegation** mode: when the model hands work off
(`session.delegation.created`) the daemon relays the user's
transcribed words to the supervisor as a `to_supervisor` call — the
orchestrator answers exactly as with Gemini — and the result goes back with
`session.commentary.append`. Pushes become `session.commentary.append`
(spoken), `session.thinking.append` (silent) or
`session.instructions.append` (interrupt). The session is lazy: it opens
(`voice.session_open {reason: audio|push}`) only while a `/ws` client is
attached and unmuted and there is speech (a frame loud enough) or a push
to say, and closes (`voice.session_close {reason, dur_ms, billed_s}`)
after `voice.openai.idle_close_s` without a transcript, a reply, a
delegation or a push. A reopened session gets the last few turns as text
history. `voice.billed_seconds {total_s}` is logged at every close and
`web.client.stats` carries `voice_billed_seconds`.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `voice.openai.api_key` | `OPENAI_API_KEY` | `""` | The OpenAI API key for GPT-Live. The voice only: the orchestrator uses it only when the user ticks "Use the same OpenAI key" for the OpenAI orchestrator preset ([the OpenAI key rule](#brainopenai_compat)). Normally left out of the TOML file and set via the environment or `--env-file`; an empty env value does not clear a file value. Never logged: `config show` prints `set`/`unset`. Without it `openai` is listed by `GET /api/voice/providers` as unavailable ("no OPENAI_API_KEY"). |
| `voice.openai.model` | `SHEEPDOG_VOICE_OPENAI_MODEL` | `gpt-live-1` | The Live model. |
| `voice.openai.voice` | `SHEEPDOG_VOICE_OPENAI_VOICE` | `marin` | The output voice (`marin`, `cedar`, `quartz`, `vesper`, …; fixed per session). The setup page's voice picker writes it to the env file ([docs/models.md](docs/models.md#choosing-the-voice-on-the-setup-page)). |
| `voice.openai.idle_close_s` | `SHEEPDOG_VOICE_OPENAI_IDLE_CLOSE_S` | `45` | Close the live GPT-Live session after this many seconds with no input transcript, no model speech, no delegation or push in flight. A client that disconnects, mutes or goes silent stops all of those, so this is also how long a forgotten session keeps billing. While the client is muted, loud frames sent just before the mute and an unsettled input transcript do not hold the session open: muted, nothing playing and no push pending is idle (a delegation in flight still holds it for its answer). After an idle close the same room noise does not reopen it: the next opening needs the client's silence gate to close first, a quiet second, an unmute or a reconnect. |

## `[deepseek]`

The second orchestrator provider: DeepSeek's OpenAI-compatible chat
completions API (`deepseek-flash`, `deepseek-v4-pro`), **paid per
token**. With a key, `deepseek` is offered in the `/debug` model picker
whatever `brain.provider` says, and `sheepdog brain-test --provider
deepseek` runs a turn on it. The voice session never uses it.

**Event turns do not think.** An announce or investigation-delivery
turn is sent with `"thinking": {"type": "disabled"}` and no
`reasoning_effort`, whatever `deepseek.thinking` says (on Gemini: the
`minimal` thinking level, `low` on a Pro model); so is the compaction
summary: reasoning tokens cost latency, and a judgement call on an
announce does not need them (docs/dev/iteration-2.md WP5). User turns
keep the setting below.

**The reasoning replay trade-off.** DeepSeek's thinking mode returns a
`reasoning_content` with every assistant message and, while tools are
declared, requires it back on the assistant messages of the current
turn's tool loop (a missing one is a 400). The orchestrator keeps it on the
transcript message (`raw`, tagged with `provider` and `model`) and
replays it only for messages the same provider and model produced in
the current turn, and never into a call with thinking disabled; a
Gemini step is rebuilt as a plain tool call for DeepSeek and vice
versa. Earlier turns' reasoning stays on disk: the API accepts the
messages without it (verified live with a seeded transcript whose `raw`
was stripped), and replaying it costs prompt tokens the window's estimate
does not see (`brain.window_tokens` counts content only). Dropping
it ends the prefix cache's match at the previous turn, which the next
request pays for once. Switch models mid-turn and the running turn still
finishes on the pair it started with.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `deepseek.api_key` | `DEEPSEEK_API_KEY` | `""` | The DeepSeek API key (Bearer). Like `GEMINI_API_KEY`, normally left out of the TOML file and set via the environment or `--env-file`; an empty env value does not clear a file value. Never logged: `config show` prints `set`/`unset`. Without it the provider is listed in the picker as unavailable ("no DEEPSEEK_API_KEY") and `brain.provider = "deepseek"` leaves the orchestrator off. One key, no pool: a 429 fails the turn (`brain.rate_limited`, then the daemon's own sentence), it is not retried. |
| `deepseek.base_url` | `SHEEPDOG_DEEPSEEK_BASE_URL` | `https://api.deepseek.com` | API root; `/chat/completions` and `/models` are appended. |
| `deepseek.thinking` | `SHEEPDOG_DEEPSEEK_THINKING` | `true` | DeepSeek's thinking mode for user turns (`"thinking": {"type": "enabled"}`); `false` sends `disabled`. Event turns and compaction summaries are always sent `disabled` (above). On, the orchestrator replays the current turn's `reasoning_content` as described above. |
| `deepseek.reasoning_effort` | `SHEEPDOG_DEEPSEEK_REASONING_EFFORT` | `low` | `low`, `high` or `max`; sent only with thinking on (so never on an event turn). `low` and `high` measured the same latency on screen questions, so the cheaper one is the default; `max` is untested. |

## `[anthropic]`

The native Anthropic orchestrator provider: Claude over the Messages
API (official Go SDK), **paid per token** (docs/models.md). With a key,
`anthropic` is offered in the `/debug` model picker whatever
`brain.provider` says, and `sheepdog brain-test --provider anthropic`
runs a turn on it. `brain.provider = "anthropic"` without `brain.model`
starts on `claude-opus-5-5`. The voice session never uses it. The design
is in docs/dev/anthropic-adapter.md.

**Thinking is always on** on the current models, so effort is the only
control: `anthropic.effort` on user turns, `anthropic.event_effort` on
announce and investigation-delivery turns and on the compaction summary.

**Thinking blocks are replayed for the current turn only.** Claude's
thinking blocks are bound to the conversation that produced them: an
edited history (pruned tool results, a compaction, the refreshed state
block) would invalidate them, and accounts created on or after
2026-08-31 get a 400 for that. The orchestrator replays a block only
in the turn that produced it and keeps that turn append-only. Every
request also asks the API to drop, not reject, a block whose history
changed (`thinking-binding-controls-2026-08-01`, `drop_block`). A
drop is logged as `brain.thinking_dropped`. Earlier turns go back as
their text and tool calls.

**Refusals.** With `anthropic.fallbacks` on (the default), a request the
safety classifiers decline is re-served inside the same call by the
model Anthropic recommends for the category (`fallbacks: "default"`,
beta `server-side-fallback-2026-07-01`; for Opus 5.5, Opus 5, Sonnet 5.5
and Fable 5.1). A request still refused fails the turn (`refused
(category …)` on `brain.step`) and the daemon answers for itself.

The SDK client reads nothing from the environment besides what the
table lists: `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN` (which Claude
Code sets for itself) are ignored. No SDK retries: a 429, a 529 or a 5xx
fails the call and the orchestrator's failover decides.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `anthropic.api_key` | `ANTHROPIC_API_KEY` | `""` | The Anthropic API key (`x-api-key`). Normally left out of the TOML file and set via the environment or `--env-file`; an empty env value does not clear a file value. Never logged: `config show` prints `set`/`unset`. Without it the provider is listed in the picker as unavailable ("no ANTHROPIC_API_KEY") and `brain.provider = "anthropic"` leaves the orchestrator off. A 401 or 402 marks the provider down (`brain.provider_down`) and the user is told which key to fix or to top up. |
| `anthropic.base_url` | `SHEEPDOG_ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | API root. A proxy here that rejects the beta headers is retried once without them. |
| `anthropic.effort` | `SHEEPDOG_ANTHROPIC_EFFORT` | `medium` | `output_config.effort` on user turns: `low`, `medium`, `high`, `xhigh` or `max`. `medium` is Opus 5.5's own default. Measured 2026-10-19 (docs/dev/eval/anthropic-opus-2026-10-19.md): user turns 7.4 s median, 12.5 s p90, well inside `brain.utterance_timeout_s`; `low` here is not yet measured. |
| `anthropic.event_effort` | `SHEEPDOG_ANTHROPIC_EVENT_EFFORT` | `low` | The same on event turns and the compaction summary, which have the shortest budgets (`brain.announce_turn_timeout_s`). Measured at `low`: 4.8 s median, 7.2 s p90 against the 6 s budget. |
| `anthropic.fallbacks` | `SHEEPDOG_ANTHROPIC_FALLBACKS` | `true` | Server-side refusal fallbacks (above). `false` sends none: a refusal fails the turn. |

## `[web]`

The HTTP server. Binds to `web.bind:web.port`, loopback only by
default; a phone on the LAN or over Tailscale can reach it once
`web.bind` is a non-loopback address, over TLS and only as a paired
device (docs/remote-access.md).

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `web.bind` | `SHEEPDOG_WEB_BIND` | `127.0.0.1` | The listen address. `127.0.0.1` (or `localhost`, `::1`) is loopback: reachable only from this machine. Any other address — a LAN IP, a Tailscale IP, or `0.0.0.0` for every interface — lets paired devices in: off this machine the daemon answers TLS only, with its own certificate, and every request needs a paired device's session (docs/dev/ws-contract.md "Auth"). With a non-loopback bind, `daemon.start`'s `urls` and the startup banner list every non-loopback IPv4 address of the host, so you can read the LAN URL off the log instead of guessing which interface it is. A single LAN or Tailscale IP also gets a loopback listener on the same port, so `sheepdog status`, `open`, `pair` and the setup page keep working on this machine. |
| `web.port` | `SHEEPDOG_WEB_PORT` | `7777` | TCP port. `0` with an empty config picks a free port (used by tests). |
| `web.dev` | *(flag only: `serve --dev`)* | `false` | Turns on developer-only routes: `/testdata/<name>.wav` serves the audio fixtures so `/voice?src=file` can use one as a fake microphone. |
| `web.dev_testdata_dir` | *(TOML only)* | `testdata/audio` (relative to the working directory) | Where `/testdata/` serves from when `web.dev` is on and this is empty. |
| `web.shell` | `SHEEPDOG_WEB_SHELL` | `true` | Lets paired devices open the iPhone app's plain shell and send attachments, on every network. One switch per computer: the setup page's **Shell from your phone** writes it to the env file and applies it at once, with no restart. Off, the shell target of `/term` answers `refused:shell_off`, attachments and wrap-up commands answer `403`; agent windows stay available. See [docs/remote-access.md](docs/remote-access.md#the-phone-shell). |
| `web.advertise` | `SHEEPDOG_WEB_ADVERTISE` (comma-separated) | `[]` | More `host:port` addresses for the pairing code, listed after the daemon's own Tailscale and LAN addresses: a router's port forward, whose address the daemon cannot see (for example `["100.101.1.1:7777"]`). A host without a port gets `7777`. Subnet routing needs nothing. See [docs/remote-access.md](docs/remote-access.md#through-a-router). |
| `web.tailscale_https` | `SHEEPDOG_WEB_TAILSCALE_HTTPS` | `false` | Serve a real certificate for this computer's Tailscale name (`<machine>.<tailnet>.ts.net`, from `tailscale cert`) to browsers that open that name, so `/voice` opens in a browser without a certificate warning. **Off by default: turning it on logs the machine name publicly** in the certificate transparency logs. The pairing code's browser link then uses the name; the app keeps using the addresses and the daemon's own pinned key. Needs Tailscale on this computer with HTTPS certificates enabled for the tailnet. Set by the setup page's switch. See [docs/remote-access.md](docs/remote-access.md#a-real-certificate-for-browsers). |

## `[cli]`

The read-only CLI query tool: codex and/or claude,
invoked non-interactively and read-only to answer questions the orchestrator
cannot answer from a screen alone (its `run_cli` tool). See `docs/dev/cli-query-tools.md` for why each
flag in the argv templates is there.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `cli.prefer` | `SHEEPDOG_CLI_PREFER` | `auto` | Picks the backend: `codex`, `claude`, or `auto` (codex if detected on `PATH`, else claude, else the orchestrator simply has no `run_cli` tool). |
| `cli.codex_bin` | `SHEEPDOG_CLI_CODEX_BIN` | `codex` | Looked up on `PATH`, or an absolute path; also becomes `argv[0]` of `cli.codex_cmd`. |
| `cli.claude_bin` | `SHEEPDOG_CLI_CLAUDE_BIN` | `claude` | Same, for the claude backend and `cli.claude_cmd`. |
| `cli.codex_cmd` | *(TOML only)* | see `internal/config/sections.go`'s `defaultCLI` | Argv template for the codex invocation; `{question}`, `{cwd}`, `{model}`, `{reasoning}` are substituted. No shell is invoked. It asks for `--json`, so the sub-agent's thread id is captured and an investigation's follow-up can resume it. |
| `cli.claude_cmd` | *(TOML only)* | see `internal/config/sections.go`'s `defaultCLI` | Same, for claude; its `session_id` comes from the JSON result. |
| `cli.codex_resume_cmd` | *(TOML only)* | see `internal/config/sections.go`'s `defaultCLI` | Argv template of a follow-up in an existing codex session (`codex exec resume`): `{session}` is substituted as well. `resume` has no `--sandbox`, `-C` or `--color` of its own, so the sandbox is set with `-c sandbox_mode=read-only` and the directory by the child's cwd. |
| `cli.claude_resume_cmd` | *(TOML only)* | see `internal/config/sections.go`'s `defaultCLI` | Same, for claude (`claude -p --resume {session}`), with the same read-only flags as `cli.claude_cmd`. |
| `cli.claude_model` | `SHEEPDOG_CLI_CLAUDE_MODEL` | `claude-haiku-4-5-20251001` | Fills `{model}` in `cli.claude_cmd`. |
| `cli.codex_reasoning` | `SHEEPDOG_CLI_CODEX_REASONING` | `low` | Fills `{reasoning}` in `cli.codex_cmd`. |
| `cli.timeout_s` | `SHEEPDOG_CLI_TIMEOUT_S` | `60` | Bounds one invocation of the orchestrator's `run_cli` lookup; the process group is killed on expiry. It does **not** bound a background investigation, which has `brain.investigate_max_s` instead. |
| `cli.max_output_kb` | `SHEEPDOG_CLI_MAX_OUTPUT_KB` | `16` | Caps captured stdout; the tail is kept. |

## `[supervisor]`

How proactive the voice side is. See [docs/faq.md](docs/faq.md) "How talkative is it?" for what each level
announces.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `supervisor.chattiness` | `SHEEPDOG_SUPERVISOR_CHATTINESS` | `chatty` | The initial push policy: `quiet` (agent questions and exits, plus what you are owed at every level: a kept promise, the finish of an instruction you gave), `normal` (plus the finish of any agent you dealt with recently, with a one-line result), or `chatty` (every finish, plus "started on …" when an instruction you sent is picked up). Changeable at runtime from the `/debug` header or `PUT /api/settings {"chattiness": …}`; the runtime choice is kept in the state file's `session.chattiness` and wins over this value on restart. |
| `supervisor.recent_minutes` | `SHEEPDOG_SUPERVISOR_RECENT_MINUTES` | `30` | The `normal` level's window: a finish is announced when the agent was blocked, was sent an answer or an instruction, was asked about, or was named in a spoken briefing within this many minutes. |
| `supervisor.promise_ttl_s` | `SHEEPDOG_SUPERVISOR_PROMISE_TTL_S` | `7200` | How long a promise ("tell me when it's done") waits for its event before it expires silently (`promise.expired`). Open promises live in the state file's `session.promises`, so a restart keeps them. |
| `supervisor.cancel_window_s` | `SHEEPDOG_SUPERVISOR_CANCEL_WINDOW_S` | `30` | How soon after an instruction went to a *working* agent a "stop" still interrupts it with Ctrl-C (the stop procedure's phase 3, [`docs/dev/roles.md`](docs/dev/roles.md) "Stop"). Past it a stop no longer touches the agent: use an instruction ("tell it to revert that") instead. |
| `supervisor.reprompt_poll_s` | `SHEEPDOG_SUPERVISOR_REPROMPT_POLL_S` | `10` | How long the post-answer poll watches a pane, once every second, after keys go to it: herdr reports a pane's status only on change, so a worker that answers one prompt and is already showing the next within herdr's own poll window never leaves `blocked` ([`docs/dev/roles.md`](docs/dev/roles.md) "blocked → blocked is a block"). A periodic reconciler (the `snapshot_interval_s` cadence) and a startup sweep catch what the poll's window missed. |
| `supervisor.repeat_window_s` | `SHEEPDOG_SUPERVISOR_REPEAT_WINDOW_S` | `60` | How long an identical roster sentence counts as "already reported": a status question inside the window carries a note the orchestrator can act on ("no change since I last checked") instead of the roster spoken again in full; with the orchestrator off, the daemon's own fallback sentence is prefixed the same way (`docs/dev/history/issues-2026-09-18.md` "Smaller items"). |
| `supervisor.authorise_window_s` | `SHEEPDOG_SUPERVISOR_AUTHORISE_WINDOW_S` | `30` | How recent the user's own words must be to answer a worker's approval prompt (the one rail, [`docs/dev/roles.md`](docs/dev/roles.md)). The daemon records every user input — a voice transcript, typed text (`/api/utterance`, the `/voice` text box) or a tap (`POST /api/pending/answer`) — and `send_keys` or the bare-answer fast path runs only when one arrived after the prompt appeared and within this many seconds, and that input's own text (never the voice model's tool argument) answers it. A refusal logs `brain.rail_blocked {reason}`. |
| `supervisor.project_roots` | `SHEEPDOG_SUPERVISOR_PROJECT_ROOTS` (`:`-separated) | `["~/Documents"]` | Where the orchestrator may start a new Claude Code or Codex agent by voice ("open a new Claude in sheepdog-bench", the `prompt` tool's `new_agent_*` parameters, [`docs/dev/roles.md`](docs/dev/roles.md) "Starting an agent"). A new agent may start in an immediate subdirectory of one of these directories, or in the directory of an existing herdr workspace; any other path is refused, symlinks included (`supervisor.spawn_refused {reason}`). A leading `~/` is the home directory. |

Two timings are fixed, not options ([`docs/dev/roles.md`](docs/dev/roles.md) "One
decision-maker"): a `to_supervisor` call with no fresh user input
waits up to 1.5 s for the input transcription (it and the tool call reach
the daemon on different paths, and Gemini's transcription can trail the
call). An utterance with no
tool call and no speech from the voice for 4 s is relayed by the daemon.

## `[brain]`

The orchestrator (plan D7-revised, [`docs/dev/roles.md`](docs/dev/roles.md)):
a stateful agent behind `to_supervisor` (one utterance turn per thing the
user says) and the workers' announcements, that reasons with tools
(`read_screen`, `run_cli`, `list_agents`, `recall`) and acts with
`prompt`, `send_keys`, `interrupt` over a function-calling model — Gemini
(the key pool), DeepSeek (`[deepseek]`), Anthropic (`[anthropic]`),
Gemini on Vertex AI (`[gemini.vertex]`) or any OpenAI-compatible endpoint
(`[[brain.openai_compat]]`, below), switchable at runtime —
keeping one transcript per daemon run at `<data_dir>/brain/<run-id>.jsonl`.
When it errors or runs out of budget, the daemon speaks its own code
sentence (a parsed prompt, "<alias> finished.") or reports the roster. Its provider and model can be switched while the
daemon runs, from the `/debug` header's model picker (the provider
select lists every provider, a keyless one greyed out with the reason;
the model select shows that provider's discovered list — Gemini's model
list, DeepSeek's and every OpenAI-compatible provider's `GET /models`, Vertex's fixed list — and the switch happens when a model is
picked) or `PUT /api/brain/model {"provider": "deepseek", "model":
"deepseek-flash"}`: the switch takes effect at the next turn (a turn in
flight finishes on the provider and model it started with), is logged
as `brain.model_changed` and marked in the transcript ("model switched
from gemini/gemini-3.5-flash to deepseek/deepseek-flash"), and is kept
in the state file's `session.brain_provider` / `session.brain_model`,
which win over `brain.provider` / `brain.model` on restart (a persisted
provider that has no key in the new run is logged as
`daemon.brain_model_error` and the configured pair kept). To follow the config
values again, delete both from the state file (or pick them — the
picker always records the choice). Whether a Gemini model supports
function calling is not in its list, so a bad pick shows up as a
`brain.step` error and the daemon answers for itself until you switch back.
The Gemini free tier's daily cap is per model, so switching models also
switches quota buckets; DeepSeek is paid per token. `sheepdog serve
--brain=off` disables it for one run; it is also off, silently, when the
initial provider has no key.
`--brain=fake` (a developer mode, like `--herdr=mock`) runs the same loop
over a scripted reflex model that needs no key and makes no network call,
so `/debug`'s Supervisor tab has something to show; `SHEEPDOG_FAKE_BRAIN_DELAY_MS`
(env only, fake mode only) makes every fake model call take that long, to
watch a user turn overtake an event turn (`brain.yielded`) or the
announce wait elapse.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `brain.enabled` | `SHEEPDOG_BRAIN_ENABLED` | `true` | Route the user's words (`to_supervisor`) and the workers' events through the orchestrator (`serve --brain` overrides). |
| `brain.provider` | `SHEEPDOG_BRAIN_PROVIDER` | `gemini` | The provider the orchestrator starts on: `gemini` (the key pool, rotating on a rate limit), `deepseek` (`[deepseek]`, one key), `anthropic` (`[anthropic]`, one key; `claude-opus-5-5` when `brain.model` is not set), `vertex` (Gemini on Vertex AI with the `[gemini.vertex]` key and project, location `global`; same model ids as `gemini`) or the `name` of a `[[brain.openai_compat]]` entry. Every provider with a key is registered regardless, so the picker can switch to the other at runtime. The initial value only: the pair picked at runtime is kept in the state file's `session.brain_provider` / `session.brain_model` and wins over this on restart. Set `brain.model` to a model of this provider (`deepseek-flash` with `deepseek`; the default `gemini-3.5-flash` is Gemini's, and a mismatch shows as a `brain.step` error at the first turn). With an OpenAI-compatible provider and no `brain.model` in the file or the environment, the entry's own `model` is used. |
| `brain.model` | `SHEEPDOG_BRAIN_MODEL` | `gemini-3.5-flash` | The function-calling model the orchestrator starts on, a model of `brain.provider`. The initial value only: a model picked at runtime (`/debug`, `PUT /api/brain/model`) is kept in the state file's `session.brain_model` and wins over this on restart. |
| `brain.max_steps` | `SHEEPDOG_BRAIN_MAX_STEPS` | `6` | Model calls one turn may make (each tool round is one); beyond it the turn falls back. |
| `brain.utterance_timeout_s` | `SHEEPDOG_BRAIN_UTTERANCE_TIMEOUT_S` | `25` | Wall-clock budget of one utterance turn — the orchestrator's turn over the user's relayed words, which may be a question or an instruction — and of an investigation's delivery turn, tool rounds included. When less than `brain.step_reserve_s` of it remains after a tool round (or at the last step) the next model call is answer-only (tools disabled, "time is up") and may run up to `brain.final_grace_s` past the budget; only then does the daemon answer for itself. 25 s since the 2026-10-03 eval campaign (at 12 s, 45 of 130 Vertex runs had a turn time out); `brain.reply_wait_s` (30 s) is above this budget, so the answer is the `to_supervisor` call's own result and a longer budget only delays the slowest answers. |
| `brain.window_tokens` | `SHEEPDOG_BRAIN_WINDOW_TOKENS` | `12000` | Context window target in estimated tokens; above it the oldest half of the window is compacted into one summary message (`brain.compaction`, capped at 600 tokens). A trigger, not a hard cap: the turn that crosses it runs on the full window and compaction follows it. |
| `brain.prune_after_turns` | `SHEEPDOG_BRAIN_PRUNE_AFTER_TURNS` | `2` | Turns a tool result (screen, CLI output) stays verbatim in the window before it becomes a one-line stub; the disk transcript keeps the full text. While it is verbatim, the window holds at most its last 4 KB (fixed); the transcript keeps it whole. |
| `brain.step_reserve_s` | `SHEEPDOG_BRAIN_STEP_RESERVE_S` | `0` (default `3`) | When less of the turn's timeout remains after a tool round than this, the next model call is answer-only (no more tools), so the turn ends with an answer instead of another read. `0` takes the package default (3s); negative is rejected. |
| `brain.final_grace_s` | `SHEEPDOG_BRAIN_FINAL_GRACE_S` | `0` (default `3`) | How far past the turn's timeout that one forced answer-only call may still run before the turn falls back to the code sentence. `0` takes the package default (3s); negative disables the grace entirely (fall back right at the timeout). |
| `brain.announce_turn_timeout_s` | `SHEEPDOG_BRAIN_ANNOUNCE_TURN_TIMEOUT_S` | `6` | Budget of an ordinary event turn: the `announce` turn that decides what the assistant says about a block or finish and how urgently (docs/faq.md "How talkative is it?"). A finish waits this long (plus `brain.final_grace_s`) for the orchestrator's line before anything is said about it; on expiry the daemon's own sentence is spoken instead (`brain.push_decision {via: template, reason: timeout, wait: turn}`; a promise falls back to "As you asked: …"). Event turns get only the read tools. An announce the user is owed gets the longer `brain.announce_timeout_s` instead. |
| `brain.announce_timeout_s` | `SHEEPDOG_BRAIN_ANNOUNCE_TIMEOUT_S` | `15` | Budget of an announce turn the user is owed: a finish (or block) that keeps a promise, or the finish of an instruction the user gave this run. Such a finish waits up to this long for the orchestrator's line with the finding and speaks it alone; if the turn times out, the one line is the code sentence saying the findings could not be had ("auth finished, but I couldn't get its findings in time. Ask me and I'll read its screen."; docs/dev/roles.md "Pushes"). Every other event turn keeps `brain.announce_turn_timeout_s`. A timed-out model call logs `prompt_tokens_est` on its `brain.step`. Note: one model call can run this long, and a user utterance waiting behind it only overtakes it between steps. |
| `brain.announce_wait_s` | `SHEEPDOG_BRAIN_ANNOUNCE_WAIT_S` | `2` | How long a **block** (an approval prompt, or a finish that left a prompt on screen) waits for the orchestrator's announce turn before the daemon speaks its own code sentence, the parsed prompt (`brain.push_decision {via: template, reason: wait, wait: announce_wait}`). A finish does not use it: a finish is not urgent and waits for the announce turn's own budget (`brain.announce_turn_timeout_s`, or `brain.announce_timeout_s` when owed). A orchestrator line that lands after the prompt was spoken is dropped (`brain.push_late`) unless it says the user must act (`needs_user`), or it is the finding of an owed finish that also asked; either is added once at idle. A line about a prompt that was answered meanwhile is dropped too. |
| `brain.reply_wait_s` | `SHEEPDOG_BRAIN_REPLY_WAIT_S` | `30` | How long a `to_supervisor` call waits for the orchestrator's reply. A reply inside the wait is the tool result, as before; past it the call returns at once with a silent `pending` result (the voice has already said "on it"; `brain.reply_pending`) and the reply is pushed at the next pause when it lands, tagged `answers_utterance` (`brain.reply_delivered`). A late reply is stale once the user's next request was accepted: dropped if the turn sent nothing (`brain.reply_stale`), pushed silently as context if it acted. A stop still cancels the turn and nothing is delivered. `0` takes the default; negative waits for the whole turn (the synchronous behaviour, which `--voice=off` and `brain-test` always use). |
| `brain.investigate_max_s` | `SHEEPDOG_BRAIN_INVESTIGATE_MAX_S` | `600` | Sanity ceiling of one background investigation (`investigate`, docs/dev/roles.md "Investigations and explanations") — the only limit on how long its answer may take, so a stuck job dies. What is bounded tightly is the acknowledgement, which is an ordinary orchestrator turn. A job past the ceiling is killed and logged as `investigation.error`. |
| `brain.investigate_session_ttl_s` | `SHEEPDOG_BRAIN_INVESTIGATE_SESSION_TTL_S` | `3600` | How long a finished investigation's sub-agent session stays resumable: within it, `investigate(follow_up_of: …)` continues the same CLI session (`cli.codex_resume_cmd` / `cli.claude_resume_cmd`); past it the follow-up starts a fresh sub-agent with the previous answer prepended to the question. The ledger row itself stays either way (`session.investigations`, the last 20). |
| `brain.openai_compat` | — (the key: the variable `api_key_env` names) | none | Extra orchestrator providers over any OpenAI-compatible chat completions endpoint, one `[[brain.openai_compat]]` table each (below). |

### `[[brain.openai_compat]]`

Any OpenAI-compatible chat completions endpoint can serve as an orchestrator
provider: OpenRouter, a local Ollama (`/v1`), Groq, a self-hosted vLLM, ….
Each table adds one provider; with its key set it is registered with the
orchestrator, listed in the `/debug` picker (its `GET /models` fills the
model select), selectable with `brain.provider = "<name>"`, `PUT
/api/brain/model` and `sheepdog brain-test --provider <name>`, and it
joins failover after the built-in providers (a failover to it runs its
`model`). Without a key it is listed greyed out ("no OPENROUTER_API_KEY").
`config show` prints each entry with its key as `set`/`unset`. The voice
never uses these providers: it stays on Gemini Live or GPT-Live
(`[voice]`).

The requests are the plain OpenAI shape: `model`, `messages`, `tools`,
`tool_choice`, bearer auth; none of DeepSeek's extensions (`thinking`,
`reasoning_effort`, `reasoning_content` replay) is sent. The model must
support **tool calling**: a model without it fails every turn
(`brain.step` error) and the daemon answers for itself. How good the
orchestrator is depends on the model; the evaluated ones are Gemini 3.5
Flash (`gemini`/`vertex`) and `deepseek-flash` (docs/dev/eval/README.md).

```toml
[[brain.openai_compat]]
name = "openrouter"
base_url = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"   # the default for this name anyway
model = "deepseek/deepseek-v3.2"

[[brain.openai_compat]]
name = "ollama"
base_url = "http://localhost:11434/v1"   # loopback: no key needed
model = "qwen3:8b"

[[brain.openai_compat]]
name = "groq"
base_url = "https://api.groq.com/openai/v1"
model = "openai/gpt-oss-120b"
```

| Key | Default | Meaning |
|---|---|---|
| `name` | required | The provider id: lowercase letters, digits, `-`, `_` (at most 32); not `gemini`, `deepseek`, `vertex`, `anthropic` or `fake…`; unique. |
| `label` | `name` | The display name in the `/debug` picker. |
| `base_url` | required | The API root, `http(s)://…`; `/chat/completions` and `/models` are appended (a trailing `/` is ignored). |
| `api_key_env` | `<NAME>_API_KEY` (`-` as `_`) | The environment variable (or `--env-file` entry) holding the key. The key itself never goes in the TOML file. Not needed when `base_url` is a loopback address (`localhost`, `127.0.0.1`, `::1`): the provider is registered without a key and sends no `Authorization` header. |
| `model` | required | The model the provider runs by default: the initial `brain.model` when `brain.provider` names this entry and `brain.model` is not set, and the model a failover to this provider uses. |

**Entries the setup page writes.** Picking the OpenRouter, OpenAI, Ollama or
LM Studio preset in `sheepdog setup` creates the entry named after it, or
updates its `base_url`, `api_key_env` and `model` in place (a `label` and
comments stay), and sets `SHEEPDOG_BRAIN_PROVIDER` / `SHEEPDOG_BRAIN_MODEL`
in the env file. The file must still load, or the old one is restored.

| Preset | `name` | `base_url` | `api_key_env` |
|---|---|---|---|
| OpenRouter | `openrouter` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` |
| OpenAI | `openai` | `https://api.openai.com/v1` | `OPENAI_ORCH_API_KEY`, or `OPENAI_API_KEY` when reused |
| Ollama | `ollama` | `http://localhost:11434/v1` | none needed (loopback) |
| LM Studio | `lmstudio` | `http://localhost:1234/v1` | none needed (loopback) |

**The OpenAI key rule.** `OPENAI_API_KEY` is the GPT-Live voice key
(`[voice.openai]`). The OpenAI orchestrator keeps its own key in
`OPENAI_ORCH_API_KEY`, even when the name `openai` is typed under Other URL.
Only when the user ticks "Use the same OpenAI key (also used for the voice)"
(offered when a voice key is saved, off by default) does the entry point at
the voice key with `api_key_env = "OPENAI_API_KEY"`; the setup page then
writes no key. Unticking it later switches the same entry back to
`OPENAI_ORCH_API_KEY`, which then needs a key.

### Fixed values (not configurable)

Tuned from logs and kept as constants: the Gemini voice session refresh
(30 min away, or 2 h of age, 54 min on AI Studio, `voice.session_refreshed`,
[`docs/dev/timelines.md`](docs/dev/timelines.md) §1.4); the orchestrator's per-tool
timeouts (2 s in-memory reads, 4 s `read_screen`, 5 s starting an
investigation, 8 s `run_cli`; `brain.tool_timeout`); provider failover
(one retry of a step on the next registered provider on 429 / 5xx /
timeout, `brain.failover`); the anti-loop cap on spoken lines about one
event (4th within 30 s dropped).

## `[prompts]`

The prompt workspace (docs/dev/roles.md "Prompt files"): user overrides of
the embedded `assistant.md`, `assistant-gptlive.md`, `USER.md` and
`svp/{SOUL,RULES}.md`.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `prompts.dir` | `SHEEPDOG_PROMPTS_DIR` | `~/.config/sheepdog/prompts` | Override directory: `<dir>/<name>` (the same relative path as under `prompts/` in the repo, e.g. `assistant.md`, `svp/RULES.md`) replaces the matching embedded file when present; a missing file or directory is not an error. Re-read on `SIGHUP` and `POST /api/prompts/reload`, not just at daemon start. An override of `svp/RULES.md` that drops the rail or the provenance sentence is rejected (`prompts.override_rejected`) and the embedded file is used instead — the one rail cannot be removed by a prompt edit. `sheepdog prompts export [dir]` seeds this directory from the embedded defaults; `sheepdog prompts show <name>` prints the effective text and its source. |

## `[log]`

The runtime log, `<data_dir>/logs/sheepdog.log` (`sheepdog logs`): every
line the daemon prints, each with a UTC timestamp, plus the events
`log.level` selects as one text line each. Secrets — every key and token
in the config and the environment, and anything shaped like one
(`AIza…`, `sk-…`, `Bearer …`, `token=…`) — are replaced by `[REDACTED]`
before a line is written.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `log.level` | `SHEEPDOG_LOG_LEVEL` | `info` | Which events are mirrored into the runtime log: `debug` every event, `info` errors, warnings and the daemon's lifecycle (`daemon.*`), `warn` errors and warnings, `error` errors only. The daemon's own lines (the banner, warnings, the exit reason) are always written. `serve --verbose` additionally echoes every event to stderr as JSON. |
| `log.max_size_mb` | *(TOML only)* | `10` | The log is rotated when it would grow past this: `sheepdog.log` becomes `sheepdog.log.1`, `.1` becomes `.2`, and so on. |
| `log.max_files` | *(TOML only)* | `3` | Rotated files kept; the oldest is deleted. With the defaults the log takes at most 40 MB. |

## `[events]`

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `events.retention_days` | `SHEEPDOG_EVENTS_RETENTION_DAYS` | `14` | Days of event log kept. The event log (`<data_dir>/events/YYYY-MM-DD.jsonl`) holds transcripts of what you said and what the voice and the orchestrator answered; so do the orchestrator's transcripts in `<data_dir>/brain/`. Day files older than this (UTC), and transcripts of runs that started and were last written before then, are deleted when the daemon starts and once a day while it runs (`events.pruned`); `0` keeps them forever. Only `YYYY-MM-DD.jsonl` files directly in `events/` and `<run id>[.requests].jsonl` files directly in `brain/` are deleted — never another file, a subdirectory or a symlink — and never under a `docs/dev/bench`, `docs/dev/eval` or `testdata` directory. `sheepdog dev scorecard`, `dev feedback` and `dev eval extract` read this directory, so keep a longer retention if you analyse older days. `sheepdog data clear` deletes it now. |

## `[update]`

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `update.check` | `SHEEPDOG_UPDATE_CHECK` | `true` | Once a day (and shortly after the daemon starts, when the last check is older than a day) the daemon asks `api.github.com` for the latest `v*` release of `kccarlos/herdr-sheepdog` and, when a newer one exists, shows a notice in `sheepdog status`, and the setup and voice pages. The result is cached in `<data_dir>/update-check.json`. One request, nothing personal sent ([privacy](docs/privacy.md)). The voice never speaks it. `false` turns the check and every notice off. |
| `update.auto_restart` | `SHEEPDOG_UPDATE_AUTO_RESTART` | `true` | After an upgrade (the `sheepdog` executable on disk changed) the daemon restarts itself into the new one, but only while it is idle: no voice client connected, no orchestrator turn running, no approval waiting, no demo running on the setup page. It checks every minute and never interrupts a live session. `false` leaves the restart to you (`sheepdog restart`); the pages and `sheepdog status` still say "update installed, restart to finish". |

## `[computer]`

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `computer.name` | `SHEEPDOG_COMPUTER_NAME` | the system's computer name | What the apps call this computer (`GET /api/hello` `computer.name`). Empty uses macOS's computer name (System Settings ▸ General ▸ Sharing), else the host name. The app may rename it on the phone. The app tells computers apart by the daemon id in `<data_dir>/tls/daemon_id`, made once at first start. |

## What sheepdog stores

Everything is under two directories (and `data_dir` moves the second).
Nothing is sent anywhere except to the model providers you configured.

| Path | What it holds | Kept |
|---|---|---|
| `~/.config/sheepdog/config.toml` | Your settings (mode 0600). | Until you delete it (`sheepdog uninstall` asks). |
| `~/.config/sheepdog/.env` | Your API keys (mode 0600). | Until you delete it. |
| `~/.config/sheepdog/prompts/` | Your prompt overrides, if you made any (`prompts.dir`). | Until you delete it. |
| `~/.local/share/sheepdog/events/YYYY-MM-DD.jsonl` | The event log: every event of the daemon, including **transcripts of what you said**, the voice's and the orchestrator's answers, and agent screen excerpts. | `events.retention_days` (14 days). `sheepdog data clear --events` deletes it now. |
| `~/.local/share/sheepdog/brain/<run>.jsonl`, `<run>.requests.jsonl` | The orchestrator's transcripts and the requests it sent to its model, one pair per daemon run: they include what you said. | `events.retention_days`, counted from the run's start and its last write. Deleted with the event log. |
| `~/.local/share/sheepdog/notes.jsonl` | The notes the orchestrator wrote down about your agents (its `note` tool). | Until you delete it. |
| `~/.local/share/sheepdog/state.json` | The agent table, aliases, pending questions and your runtime settings (`state.path`). | Overwritten as it changes. |
| `~/.local/share/sheepdog/attachments/<pane>/` | Photos and files you attached from the iPhone app for an agent (mode 0600). | 7 days. |
| `~/.local/share/sheepdog/push_devices.json` | The phones registered for notifications: device tokens, relay tokens and each phone's notification content key. | Until a phone unregisters. |
| `~/.local/share/sheepdog/logs/sheepdog.log` | The runtime log (`[log]`), secrets redacted. | Rotated at `log.max_size_mb`, `log.max_files` old files. |
| `~/.local/share/sheepdog/logs/stdio.log` | What a background daemon printed outside its log: an error before the log opened, or a crash. Normally empty. | Until `sheepdog data clear --all`. |
| `~/.local/share/sheepdog/env-file.json` | The path of the env file the last daemon loaded its keys from, and whether it had them. A path only, no value. | Until you delete it. |
| `~/.local/share/sheepdog/tls/` | This computer's id for the apps (`daemon_id`, 16 random bytes) and its TLS key and self-signed certificate (`key.pem`, `cert.pem`; during a renewal also `next_key.pem`, `next_cert.pem`). Directory 0700, files 0600. No personal data. | Until you delete it; Reset pairing replaces the key and certificate. |
| `~/.local/share/sheepdog/devices.json` | The paired phones and browsers: a name, a kind, each one's public key, when it paired and was last seen, and whether it was revoked (mode 0600). No secret: the private keys stay on the devices. | Until you delete it. |
| `~/.local/share/sheepdog/sheepdog.pid` | The running daemon's pid, URL and version (empty when it is stopped). | While it runs. |
| `~/.local/share/sheepdog/update-check.json`, `update-notice.json`, `update-restart.json` | The last answer of the daily update check, when a command last printed the update notice, and the last automatic restart after an upgrade. No personal data. | Until you delete them. |

`sheepdog data clear --events` deletes the event log, `--all` everything
under `data_dir`; both ask first. `sheepdog uninstall` removes the
service and asks before deleting either directory.

## `[push]`

Phone notifications for the iOS companion app (docs/dev/push.md; the
three ways to set them up are in docs/iphone-app.md "Notifications").

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `push.mode` | `SHEEPDOG_PUSH_MODE` | unset: `apns` when all four `[push.apns]` keys are set, else `relay` | How notifications reach Apple. `relay`: through sheepdog's shared relay (or your own, `push.relay.url`); the daemon needs no Apple key, and the content is end-to-end encrypted with a key only the phone has, so the relay sees only the device token, the timing, the size and the kind. `apns`: straight to Apple with your own key (`[push.apns]`), for an app you build yourself. `off`: no notifications (phones can still register). |
| `push.relay.url` | `SHEEPDOG_PUSH_RELAY_URL` | sheepdog's shared relay | The relay's base URL, for `push.mode = "relay"`. Point it at your own deployment of `relay/` (relay/README.md) to self-host. |

## `[push.apns]`

Direct delivery to the Apple Push Notification service with your own key,
used when `push.mode` is `apns` (or unset with all four keys present). All
four keys are needed; with any one empty, sending is disabled and `push.disabled` at startup says which
are missing (devices can still register, so nothing is lost while the key
is being set up). `config show` prints `key_path` as set/unset only; the
key file's contents are never read into a log.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `push.apns.key_path` | `SHEEPDOG_APNS_KEY_PATH` | empty (disabled) | The `.p8` APNs auth key (ES256, PKCS#8 PEM) from the Apple Developer account. A leading `~/` is expanded. Keep it mode 600. |
| `push.apns.key_id` | `SHEEPDOG_APNS_KEY_ID` | empty | The auth key's 10-character Key ID (the JWT `kid`). |
| `push.apns.team_id` | `SHEEPDOG_APNS_TEAM_ID` | empty | The Apple Developer Team ID the key belongs to (the JWT `iss`). A key from another team is rejected by APNs with `403 InvalidProviderToken`. |
| `push.apns.topic` | `SHEEPDOG_APNS_TOPIC` | empty | The iOS app's bundle id (`apns-topic`). |

Registered devices, with each phone's relay token and end-to-end content key, persist in `<data_dir>/push_devices.json` (mode 600).
