# sheepdog configuration

Configuration is read from `~/.config/sheepdog/config.toml` when it
exists (create one with `sheepdog config init`), then overridden by
`SHEEPDOG_*` environment variables (and, for the Gemini key pool,
`GEMINI_API_KEY`/`GEMINI_API_KEY_1`…`GEMINI_API_KEY_9`). The environment
always wins over the file. See `internal/config/config.go` for the
authoritative source; this file is checked against it in
`internal/config/doc_test.go` — a struct field without a matching entry
here fails the build.

`sheepdog serve --env-file PATH` (default `./.env` if present) loads
`KEY=VALUE` lines into the process environment *before* the config file
and its `SHEEPDOG_*` overrides are read, so any key below with an env
override can also come from a `.env` file in a dev checkout. See
`.env.example`. `sheepdog config show` prints the fully-resolved config
with secrets redacted (as counts); `sheepdog config init` writes a
commented template of every key below to `~/.config/sheepdog/config.toml`
(or `--path`), refusing to overwrite an existing file without `--force`.

Two keys — `[aliases]` and the `[cli]` argv templates (`codex_cmd`,
`claude_cmd`) — are TOML-only: they are not simple scalars an environment
variable could sanely hold, so they have no `SHEEPDOG_*` override.

## Top level

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `data_dir` | `SHEEPDOG_DATA_DIR` | `~/.local/share/sheepdog` | Where the daemon keeps its own files: the JSONL event log and (unless overridden) the state file. |
| `debounce_ms` | `SHEEPDOG_DEBOUNCE_MS` | `3000` | How long a herdr `blocked` state must persist before it becomes a transition (plan §3.1); filters transient flicker. |
| `snapshot_interval_s` | `SHEEPDOG_SNAPSHOT_INTERVAL_S` | `30` | Period of the herdr `session.snapshot` diff loop that catches panes the event stream missed. |
| `pending_ttl_s` | `SHEEPDOG_PENDING_TTL_S` | `120` | How long a pending action (an unanswered agent question, or an in-flight instruction) stays confirmable/cancellable before it expires. |
| `aliases` | *(TOML only)* | `{}` | Table mapping a herdr pane id to a spoken alias, e.g. `"w9:p1" = "auth"`. Pinned aliases win over derived ones, for new agents and for rows already on disk. |

## `[herdr]`

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `herdr.socket` | `SHEEPDOG_HERDR_SOCKET` | `~/.config/herdr/herdr.sock` | Path of the herdr unix domain socket (`--herdr=socket`, the default; ignored under `--herdr=mock`). |

## `[state]`

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `state.path` | `SHEEPDOG_STATE_PATH` | `<data_dir>/state.json` | JSON file the persisted state table is written to. |
| `state.full_fsync` | `SHEEPDOG_STATE_FULL_FSYNC` | `false` | Turn on the hardware-durable fsync pass after a mutation (debounced to one flush per ~100ms burst, plus a final one on shutdown). Off by default: on macOS the durable path is `F_FULLFSYNC`, dramatically slower than the plain fsync used otherwise (see `internal/state`); turn it on only if the state table must survive a power loss, not just a process crash. |

## `[gemini]`

Voice session (plan §6).

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `gemini.api_keys` | `GEMINI_API_KEY`, `GEMINI_API_KEY_1`…`GEMINI_API_KEY_9` | `[]` | The Gemini Live key pool; the voice session binds one key per connection and rotates to the next on a rate-limit error. Normally left out of the TOML file and set via the environment (or `--env-file`) so keys never sit in a checked-in file; any env key found replaces the whole pool from the file. Also the fallback key source for `[llm]` when `llm.api_key` is empty and `llm.base_url` is Gemini's. |
| `gemini.model` | `SHEEPDOG_GEMINI_MODEL` | `gemini-3.8-live` | The Live API model name. |
| `gemini.voice` | `SHEEPDOG_GEMINI_VOICE` | `Puck` | The prebuilt voice name. |
| `gemini.stall_after_s` | `SHEEPDOG_GEMINI_STALL_AFTER_S` | `10` | Seconds an utterance may go without any model response (tool call or audio) before `voice.stalled` is logged and the pages show "stalled". The second stall on one connection (or the second idle push that outwaits the 30 s turn timeout) recycles the connection: a reconnect with the still-valid resumption handle (`voice.recycle`), keeping the conversation. |

## `[llm]`

The cheap text-in/text-out model (plan §3.4): any OpenAI-compatible chat
endpoint, including a local Ollama server.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `llm.base_url` | `SHEEPDOG_LLM_BASE_URL` | `https://generativelanguage.googleapis.com/v1beta/openai/` | API root. Use `http://localhost:11434/v1` for Ollama. |
| `llm.model` | `SHEEPDOG_LLM_MODEL` | `gemini-3.5-flash-lite` | Model name passed on every request. |
| `llm.api_key` | `SHEEPDOG_LLM_API_KEY` | `""` | Authenticates requests; never logged. Empty and `llm.base_url` is the Gemini endpoint → the `gemini.api_keys` pool is used instead. Empty and pointed elsewhere (e.g. Ollama) → any non-empty placeholder is sent, since local endpoints usually ignore it. |
| `llm.timeout_s` | `SHEEPDOG_LLM_TIMEOUT_S` | `15` | Per-attempt timeout of one template call. Raise it for a slow local model (Ollama gemma4 needs more than the default on a cold start). |

## `[web]`

The HTTP server (plan §3.8); binds to `127.0.0.1` only.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `web.port` | `SHEEPDOG_WEB_PORT` | `7777` | TCP port on localhost. `0` with an empty config picks a free port (used by tests). |
| `web.dev` | *(flag only: `serve --dev`)* | `false` | Turns on developer-only routes: `/testdata/<name>.wav` serves the audio fixtures so `/voice?src=file` can use one as a fake microphone. |
| `web.dev_testdata_dir` | *(TOML only)* | `testdata/audio` (relative to the working directory) | Where `/testdata/` serves from when `web.dev` is on and this is empty. |

## `[cli]`

The read-only CLI query tool (plan §3.5, WP4): codex and/or claude,
invoked non-interactively and read-only to answer questions the cheap LLM
cannot from the screen alone. See `docs/cli-query-tools.md` for why each
flag in the argv templates is there.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `cli.prefer` | `SHEEPDOG_CLI_PREFER` | `auto` | Picks the backend: `codex`, `claude`, or `auto` (codex if detected on `PATH`, else claude, else the deep-ask path is skipped). |
| `cli.codex_bin` | `SHEEPDOG_CLI_CODEX_BIN` | `codex` | Looked up on `PATH`, or an absolute path; also becomes `argv[0]` of `cli.codex_cmd`. |
| `cli.claude_bin` | `SHEEPDOG_CLI_CLAUDE_BIN` | `claude` | Same, for the claude backend and `cli.claude_cmd`. |
| `cli.codex_cmd` | *(TOML only)* | see `internal/config/config.go`'s `defaultCLI` | Argv template for the codex invocation; `{question}`, `{cwd}`, `{model}`, `{reasoning}` are substituted. No shell is invoked. |
| `cli.claude_cmd` | *(TOML only)* | see `internal/config/config.go`'s `defaultCLI` | Same, for claude. |
| `cli.claude_model` | `SHEEPDOG_CLI_CLAUDE_MODEL` | `claude-haiku-4-5-20251001` | Fills `{model}` in `cli.claude_cmd`. |
| `cli.codex_reasoning` | `SHEEPDOG_CLI_CODEX_REASONING` | `low` | Fills `{reasoning}` in `cli.codex_cmd`. |
| `cli.timeout_s` | `SHEEPDOG_CLI_TIMEOUT_S` | `60` | Bounds one invocation; the process group is killed on expiry. |
| `cli.max_output_kb` | `SHEEPDOG_CLI_MAX_OUTPUT_KB` | `16` | Caps captured stdout; the tail is kept. |

## `[supervisor]`

How proactive the voice side is. See the README section "How chatty is
it?" for what each level announces.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `supervisor.chattiness` | `SHEEPDOG_SUPERVISOR_CHATTINESS` | `normal` | The initial push policy: `quiet` (agent questions and exits only), `normal` (plus the finish of any agent you dealt with recently, with a one-line result), or `chatty` (every finish, plus "started on …" when an instruction you sent is picked up). Changeable at runtime from the `/debug` header or `PUT /api/settings {"chattiness": …}`; the runtime choice is kept in the state file's `session.chattiness` and wins over this value on restart. |
| `supervisor.recent_minutes` | `SHEEPDOG_SUPERVISOR_RECENT_MINUTES` | `30` | The `normal` level's window: a finish is announced when the agent was blocked, was sent an answer or an instruction, was asked about, or was named in a spoken briefing within this many minutes. |

## `[brain]`

The supervisor brain (plan D7-revised, [`docs/roles.md`](docs/roles.md)):
a stateful agent behind `ask` and `propose` that reasons with tools
(`read_screen`, `run_cli`, `list_agents`, `recall`) and acts with
`prompt`, `send_keys`, `interrupt` over a function-calling Gemini model,
keeping one transcript per daemon run at `<data_dir>/brain/<run-id>.jsonl`.
When it errors or runs out of budget, the supervisor answers from the
template path instead. `sheepdog serve --brain=off` disables it for one
run; it is also off, silently, when no Gemini key is configured.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `brain.enabled` | `SHEEPDOG_BRAIN_ENABLED` | `true` | Route `ask`/`propose` through the brain (`serve --brain` overrides). |
| `brain.model` | `SHEEPDOG_BRAIN_MODEL` | `gemini-3.5-flash` | The function-calling model; the Gemini key pool applies, rotating on a rate limit. |
| `brain.max_steps` | `SHEEPDOG_BRAIN_MAX_STEPS` | `6` | Model calls one turn may make (each tool round is one); beyond it the turn falls back. |
| `brain.ask_timeout_s` | `SHEEPDOG_BRAIN_ASK_TIMEOUT_S` | `8` | Wall-clock budget of one `ask` turn, tools included; on expiry the template path answers. |
| `brain.propose_timeout_s` | `SHEEPDOG_BRAIN_PROPOSE_TIMEOUT_S` | `12` | Same for `propose`. |
| `brain.window_tokens` | `SHEEPDOG_BRAIN_WINDOW_TOKENS` | `24000` | Context window target in estimated tokens; above it the oldest half of the window is compacted into one summary message (`brain.compaction`). |
| `brain.prune_after_turns` | `SHEEPDOG_BRAIN_PRUNE_AFTER_TURNS` | `3` | Turns a tool result (screen, CLI output) stays verbatim in the window before it becomes a one-line stub; the disk transcript keeps the full text. |
