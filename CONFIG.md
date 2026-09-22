# sheepdog configuration

Configuration is read from `~/.config/sheepdog/config.toml` when it
exists (create one with `sheepdog config init`), then overridden by
`SHEEPDOG_*` environment variables (and, for the keys, `DEEPSEEK_API_KEY`
and the Gemini pool `GEMINI_API_KEY`/`GEMINI_API_KEY_1`…`GEMINI_API_KEY_9`).
The environment always wins over the file. See `internal/config/config.go` for the
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
`claude_cmd`, `codex_resume_cmd`, `claude_resume_cmd`) — are TOML-only:
they are not simple scalars an environment variable could sanely hold,
so they have no `SHEEPDOG_*` override.

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
| `gemini.api_keys` | `GEMINI_API_KEY`, `GEMINI_API_KEY_1`…`GEMINI_API_KEY_9` | `[]` | The Gemini Live key pool; the voice session binds one key per connection and rotates to the next on a rate-limit error. Normally left out of the TOML file and set via the environment (or `--env-file`) so keys never sit in a checked-in file; any env key found replaces the whole pool from the file. |
| `gemini.model` | `SHEEPDOG_GEMINI_MODEL` | `gemini-3.8-live` | The Live API model name. |
| `gemini.voice` | `SHEEPDOG_GEMINI_VOICE` | `Puck` | The prebuilt voice name. |
| `gemini.stall_after_s` | `SHEEPDOG_GEMINI_STALL_AFTER_S` | `10` | Seconds an utterance may go without any model response (tool call or audio) before `voice.stalled` is logged and the pages show "stalled". The second stall on one connection (or the second idle push that outwaits the 30 s turn timeout) recycles the connection: a reconnect with the still-valid resumption handle (`voice.recycle`), keeping the conversation. |

## `[voice]`

The dog's spoken half, as the operator meets it
([`docs/roles.md`](docs/roles.md) "Names").

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `voice.name` | `SHEEPDOG_VOICE_NAME` | `Sheepdog` | What the dog calls itself. Substituted into `prompts/assistant.md` as `{name}`, so it is the name the voice model introduces itself by and answers to; the aliases of the agents it herds are separate (`[aliases]`). |

## `[deepseek]`

The second supervisor-brain provider: DeepSeek's OpenAI-compatible chat
completions API (`deepseek-flash`, `deepseek-v4-pro`), **paid per
token**. With a key, `deepseek` is offered in the `/debug` model picker
whatever `brain.provider` says, and `sheepdog brain-test --provider
deepseek` runs a turn on it. The voice session never uses it.

Measured on 2026-09-18 with `brain-test` (`what is auth doing?` on the
`codex-working` fixture, two model calls each: a `read_screen`, then the
answer; ~3 100 prompt tokens per call): `reasoning_effort = "low"` took
2.84 s wall (1.30 s + 1.54 s, 213 completion tokens), `"high"` 2.92 s
and 2.91 s on two runs (1.31 s + 1.60 s, 269 completion tokens of which
21 + 98 reasoning) — thinking is on (the `reasoning_tokens` are there)
but a screen question draws little of it at either effort, so no latency
penalty and no visible gain; a propose that sent `prompt` and `promise`
in one step took 3.31 s at `low` (53 + 33 reasoning tokens), a seeded
follow-up ask 3.04 s (4 + 147). Both efforts fit the 8 s
`brain.ask_timeout_s` with room for a second tool round; `low` is the
default because it is the cheaper of two equals on this work, and `max`
is untested.

**The reasoning replay trade-off.** DeepSeek's thinking mode returns a
`reasoning_content` with every assistant message and, while tools are
declared, requires it back on the assistant messages of the current
turn's tool loop (a missing one is a 400). The brain keeps it on the
transcript message (`raw`, tagged with `provider` and `model`) and
replays it only for messages the same provider and model produced; a
Gemini step is rebuilt as a plain tool call for DeepSeek and vice
versa. Older turns keep their reasoning for as long as they are in the
window — the blobs are not counted in the window's token estimate
(`brain.window_tokens` counts content only), so the real prompt is
larger than the meter says — and lose it when compaction drops them,
which the API accepts (verified live with a seeded transcript whose
`raw` was stripped). Switch models mid-turn and the running turn still
finishes on the pair it started with.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `deepseek.api_key` | `DEEPSEEK_API_KEY` | `""` | The DeepSeek API key (Bearer). Like `GEMINI_API_KEY`, normally left out of the TOML file and set via the environment or `--env-file`; an empty env value does not clear a file value. Never logged: `config show` prints `set`/`unset`. Without it the provider is listed in the picker as unavailable ("no DEEPSEEK_API_KEY") and `brain.provider = "deepseek"` leaves the brain off. One key, no pool: a 429 fails the turn (`brain.rate_limited`, then the daemon's own sentence), it is not retried. |
| `deepseek.base_url` | `SHEEPDOG_DEEPSEEK_BASE_URL` | `https://api.deepseek.com` | API root; `/chat/completions` and `/models` are appended. |
| `deepseek.thinking` | `SHEEPDOG_DEEPSEEK_THINKING` | `true` | DeepSeek's thinking mode (`"thinking": {"type": "enabled"}`); `false` sends `disabled`. On, the brain replays `reasoning_content` as described above. |
| `deepseek.reasoning_effort` | `SHEEPDOG_DEEPSEEK_REASONING_EFFORT` | `low` | `low`, `high` or `max`; sent only with thinking on. See the measurements above. |

## `[web]`

The HTTP server (plan §3.8). Binds to `web.bind:web.port`, loopback
only by default; a phone on the LAN or over Tailscale can reach it once
`web.bind` is a non-loopback address and `web.token` is set (README
"From your phone").

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `web.bind` | `SHEEPDOG_WEB_BIND` | `127.0.0.1` | The listen address. `127.0.0.1` (or `localhost`, `::1`) is loopback: reachable only from this machine, and `web.token` is not required. Any other address — a LAN IP, a Tailscale IP, or `0.0.0.0` for every interface — needs `web.token` set; without one the daemon refuses to start (exit 2, "web.token is required when web.bind is not loopback"). With a non-loopback bind, `daemon.start`'s `urls` and the startup banner list every non-loopback IPv4 address of the host, so you can read the LAN URL off the log instead of guessing which interface it is. |
| `web.port` | `SHEEPDOG_WEB_PORT` | `7777` | TCP port. `0` with an empty config picks a free port (used by tests). |
| `web.token` | `SHEEPDOG_WEB_TOKEN` | `""` (unset) | The bearer token every request to `/ws`, `/events` and `/api/*` must present once this is set: `Authorization: Bearer <token>`, `?token=<token>` for `/ws` and `/events` (a browser cannot set a header on a WebSocket or EventSource request), or the cookie `POST /api/login {"token": "…"}` sets so it is typed once — `/voice` and `/debug` show an inline prompt and do this themselves the first time a request answers 401. Missing or wrong: `401 {"error":"unauthorized"}`, logged as `web.unauthorized {path, remote}` (never the token itself). The static pages (`/`, `/voice`, `/debug`, assets) and `GET /api/hello` stay public — they hold no data. Never logged: `config show` prints `set`/`unset`, like the LLM keys. Like `DEEPSEEK_API_KEY`, normally left out of the TOML file and set via the environment or `--env-file` so it never sits in a checked-in file; an empty env value does not clear a file value. |
| `web.dev` | *(flag only: `serve --dev`)* | `false` | Turns on developer-only routes: `/testdata/<name>.wav` serves the audio fixtures so `/voice?src=file` can use one as a fake microphone. |
| `web.dev_testdata_dir` | *(TOML only)* | `testdata/audio` (relative to the working directory) | Where `/testdata/` serves from when `web.dev` is on and this is empty. |

## `[cli]`

The read-only CLI query tool (plan §3.5, WP4): codex and/or claude,
invoked non-interactively and read-only to answer questions the brain
cannot answer from a screen alone (its `run_cli` tool). See `docs/cli-query-tools.md` for why each
flag in the argv templates is there.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `cli.prefer` | `SHEEPDOG_CLI_PREFER` | `auto` | Picks the backend: `codex`, `claude`, or `auto` (codex if detected on `PATH`, else claude, else the brain simply has no `run_cli` tool). |
| `cli.codex_bin` | `SHEEPDOG_CLI_CODEX_BIN` | `codex` | Looked up on `PATH`, or an absolute path; also becomes `argv[0]` of `cli.codex_cmd`. |
| `cli.claude_bin` | `SHEEPDOG_CLI_CLAUDE_BIN` | `claude` | Same, for the claude backend and `cli.claude_cmd`. |
| `cli.codex_cmd` | *(TOML only)* | see `internal/config/config.go`'s `defaultCLI` | Argv template for the codex invocation; `{question}`, `{cwd}`, `{model}`, `{reasoning}` are substituted. No shell is invoked. It asks for `--json`, so the sub-agent's thread id is captured and an investigation's follow-up can resume it. |
| `cli.claude_cmd` | *(TOML only)* | see `internal/config/config.go`'s `defaultCLI` | Same, for claude; its `session_id` comes from the JSON result. |
| `cli.codex_resume_cmd` | *(TOML only)* | see `internal/config/config.go`'s `defaultCLI` | Argv template of a follow-up in an existing codex session (`codex exec resume`): `{session}` is substituted as well. `resume` has no `--sandbox`, `-C` or `--color` of its own, so the sandbox is set with `-c sandbox_mode=read-only` and the directory by the child's cwd. |
| `cli.claude_resume_cmd` | *(TOML only)* | see `internal/config/config.go`'s `defaultCLI` | Same, for claude (`claude -p --resume {session}`), with the same read-only flags as `cli.claude_cmd`. |
| `cli.claude_model` | `SHEEPDOG_CLI_CLAUDE_MODEL` | `claude-haiku-4-5-20251001` | Fills `{model}` in `cli.claude_cmd`. |
| `cli.codex_reasoning` | `SHEEPDOG_CLI_CODEX_REASONING` | `low` | Fills `{reasoning}` in `cli.codex_cmd`. |
| `cli.timeout_s` | `SHEEPDOG_CLI_TIMEOUT_S` | `60` | Bounds one invocation of the brain's `run_cli` lookup; the process group is killed on expiry. It does **not** bound a background investigation, which has `brain.investigate_max_s` instead. |
| `cli.max_output_kb` | `SHEEPDOG_CLI_MAX_OUTPUT_KB` | `16` | Caps captured stdout; the tail is kept. |

## `[supervisor]`

How proactive the voice side is. See the README section "How chatty is
it?" for what each level announces.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `supervisor.chattiness` | `SHEEPDOG_SUPERVISOR_CHATTINESS` | `normal` | The initial push policy: `quiet` (agent questions and exits only), `normal` (plus the finish of any agent you dealt with recently, with a one-line result), or `chatty` (every finish, plus "started on …" when an instruction you sent is picked up). Changeable at runtime from the `/debug` header or `PUT /api/settings {"chattiness": …}`; the runtime choice is kept in the state file's `session.chattiness` and wins over this value on restart. |
| `supervisor.recent_minutes` | `SHEEPDOG_SUPERVISOR_RECENT_MINUTES` | `30` | The `normal` level's window: a finish is announced when the agent was blocked, was sent an answer or an instruction, was asked about, or was named in a spoken briefing within this many minutes. |
| `supervisor.promise_ttl_s` | `SHEEPDOG_SUPERVISOR_PROMISE_TTL_S` | `7200` | How long a promise ("tell me when it's done", README "Promises") waits for its event before it expires silently (`promise.expired`). Open promises live in the state file's `session.promises`, so a restart keeps them. |
| `supervisor.cancel_window_s` | `SHEEPDOG_SUPERVISOR_CANCEL_WINDOW_S` | `30` | How soon after an instruction went to a *working* agent a "stop" still interrupts it with Ctrl-C (the stop procedure's phase 3, [`docs/roles.md`](docs/roles.md) "Stop"). Past it a stop no longer touches the agent: use an instruction ("tell it to revert that") instead. |
| `supervisor.reprompt_poll_s` | `SHEEPDOG_SUPERVISOR_REPROMPT_POLL_S` | `10` | How long the post-answer poll watches a pane, once every second, after keys go to it: herdr reports a pane's status only on change, so a worker that answers one prompt and is already showing the next within herdr's own poll window never leaves `blocked` ([`docs/roles.md`](docs/roles.md) "blocked → blocked is a block"). A periodic reconciler (the `snapshot_interval_s` cadence) and a startup sweep catch what the poll's window missed. |
| `supervisor.repeat_window_s` | `SHEEPDOG_SUPERVISOR_REPEAT_WINDOW_S` | `60` | How long an identical roster sentence counts as "already reported": a status question inside the window carries a note the brain can act on ("no change since I last checked") instead of the roster spoken again in full; with the brain off, the daemon's own fallback sentence is prefixed the same way (`docs/issues-2026-09-18.md` "Smaller items"). |

## `[brain]`

The supervisor brain (plan D7-revised, [`docs/roles.md`](docs/roles.md)):
a stateful agent behind `ask` and `propose` that reasons with tools
(`read_screen`, `run_cli`, `list_agents`, `recall`) and acts with
`prompt`, `send_keys`, `interrupt` over a function-calling model — Gemini
(the key pool) or DeepSeek (`[deepseek]`), switchable at runtime —
keeping one transcript per daemon run at `<data_dir>/brain/<run-id>.jsonl`.
When it errors or runs out of budget, the daemon speaks its own code
sentence (a parsed prompt, "<alias> finished.") or reports the roster. Its provider and model can be switched while the
daemon runs, from the `/debug` header's model picker (the provider
select lists every provider, a keyless one greyed out with the reason;
the model select shows that provider's discovered list — Gemini's model
list, DeepSeek's `GET /models` — and the switch happens when a model is
picked) or `PUT /api/brain/model {"provider": "deepseek", "model":
"deepseek-flash"}`: the switch takes effect at the next turn (a turn in
flight finishes on the provider and model it started with), is logged
as `brain.model_changed` and marked in the transcript ("model switched
from gemini/gemini-3.5-flash to deepseek/deepseek-flash"), and is kept
in the state file's `session.brain_provider` / `session.brain_model`,
which win over `brain.provider` / `brain.model` on restart (a persisted
provider that has no key in the new run is logged as
`daemon.brain_model_error` and the configured pair kept; a file from
before providers existed names a Gemini model). To follow the config
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
watch a CEO turn overtake an event turn (`brain.yielded`) or the
announce wait elapse.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `brain.enabled` | `SHEEPDOG_BRAIN_ENABLED` | `true` | Route `ask`/`propose` through the brain (`serve --brain` overrides). |
| `brain.provider` | `SHEEPDOG_BRAIN_PROVIDER` | `gemini` | The provider the brain starts on: `gemini` (the key pool, rotating on a rate limit) or `deepseek` (`[deepseek]`, one key). Every provider with a key is registered regardless, so the picker can switch to the other at runtime. The initial value only: the pair picked at runtime is kept in the state file's `session.brain_provider` / `session.brain_model` and wins over this on restart. Set `brain.model` to a model of this provider (`deepseek-flash` with `deepseek`; the default `gemini-3.5-flash` is Gemini's, and a mismatch shows as a `brain.step` error at the first turn). |
| `brain.model` | `SHEEPDOG_BRAIN_MODEL` | `gemini-3.5-flash` | The function-calling model the brain starts on, a model of `brain.provider`. The initial value only: a model picked at runtime (`/debug`, `PUT /api/brain/model`) is kept in the state file's `session.brain_model` and wins over this on restart. |
| `brain.max_steps` | `SHEEPDOG_BRAIN_MAX_STEPS` | `6` | Model calls one turn may make (each tool round is one); beyond it the turn falls back. |
| `brain.ask_timeout_s` | `SHEEPDOG_BRAIN_ASK_TIMEOUT_S` | `8` | Wall-clock budget of one `ask` turn for tool rounds. When less than 3 s of it remain after a tool round (or at the last step) the next model call is answer-only (tools disabled, "time is up") and may run up to 3 s past the budget; only then does the daemon answer for itself. |
| `brain.propose_timeout_s` | `SHEEPDOG_BRAIN_PROPOSE_TIMEOUT_S` | `12` | Same for `propose`. |
| `brain.window_tokens` | `SHEEPDOG_BRAIN_WINDOW_TOKENS` | `24000` | Context window target in estimated tokens; above it the oldest half of the window is compacted into one summary message (`brain.compaction`). |
| `brain.prune_after_turns` | `SHEEPDOG_BRAIN_PRUNE_AFTER_TURNS` | `3` | Turns a tool result (screen, CLI output) stays verbatim in the window before it becomes a one-line stub; the disk transcript keeps the full text. |
| `brain.step_reserve_s` | `SHEEPDOG_BRAIN_STEP_RESERVE_S` | `0` (default `3`) | When less of the turn's timeout remains after a tool round than this, the next model call is answer-only (no more tools), so the turn ends with an answer instead of another read. `0` takes the package default (3s); negative is rejected. |
| `brain.final_grace_s` | `SHEEPDOG_BRAIN_FINAL_GRACE_S` | `0` (default `3`) | How far past the turn's timeout that one forced answer-only call may still run before the turn falls back to the code sentence. `0` takes the package default (3s); negative disables the grace entirely (fall back right at the timeout). |
| `brain.enrich_timeout_s` | `SHEEPDOG_BRAIN_ENRICH_TIMEOUT_S` | `6` | Budget of an event turn: the `announce` turn that decides what the assistant says about a block or finish and how urgently (README "How chatty is it?"). On expiry the daemon's own sentence is spoken instead (`brain.push_decision {via: template, reason: timeout}`; a promise falls back to "As you asked: …"). Event turns get only the read tools. |
| `brain.announce_wait_s` | `SHEEPDOG_BRAIN_ANNOUNCE_WAIT_S` | `2` | How long a worker event — a block *or* a finish — waits for the brain's announce turn before the daemon speaks its own code sentence (the parsed prompt, or "<alias> finished."; `brain.push_decision {via: template, reason: wait}`). One line per event: a brain line that lands after the wait is dropped (`brain.push_late`), except a `needs_user` line about a prompt that is still open, which is added at idle. A line about a prompt that was answered meanwhile is dropped too (`brain.push_stale`). |
| `brain.investigate_max_s` | `SHEEPDOG_BRAIN_INVESTIGATE_MAX_S` | `600` | Sanity ceiling of one background investigation (`investigate`, docs/roles.md "Investigations and explanations") — the only limit on how long its answer may take, so a stuck job dies. What is bounded tightly is the acknowledgement, which is an ordinary brain turn. A job past the ceiling is killed and logged as `investigation.error`. |
| `brain.investigate_session_ttl_s` | `SHEEPDOG_BRAIN_INVESTIGATE_SESSION_TTL_S` | `3600` | How long a finished investigation's sub-agent session stays resumable: within it, `investigate(follow_up_of: …)` continues the same CLI session (`cli.codex_resume_cmd` / `cli.claude_resume_cmd`); past it the follow-up starts a fresh sub-agent with the previous answer prepended to the question. The ledger row itself stays either way (`session.investigations`, the last 20). |

## `[prompts]`

The prompt workspace (docs/roles.md "Prompt files"): user overrides of
the embedded `assistant.md`, `USER.md` and `svp/{SOUL,RULES}.md`.

| Key | Env override | Default | Meaning |
|---|---|---|---|
| `prompts.dir` | `SHEEPDOG_PROMPTS_DIR` | `~/.config/sheepdog/prompts` | Override directory: `<dir>/<name>` (the same relative path as under `prompts/` in the repo, e.g. `assistant.md`, `svp/RULES.md`) replaces the matching embedded file when present; a missing file or directory is not an error. Re-read on `SIGHUP` and `POST /api/prompts/reload`, not just at daemon start. An override of `svp/RULES.md` that drops the rail or the provenance sentence is rejected (`prompts.override_rejected`) and the embedded file is used instead — the one rail cannot be removed by a prompt edit. `sheepdog prompts export [dir]` seeds this directory from the embedded defaults; `sheepdog prompts show <name>` prints the effective text and its source. |
