<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-transparent.svg">
    <img src="assets/logo-solid.svg" alt="sheepdog logo" width="160" height="160">
  </picture>
</p>

# herdr-sheepdog

A voice supervisor for a herdr agent fleet, built on Gemini Live: ask
what is happening, get told proactively when an agent is blocked,
answer its question, give new instructions, and interrupt — without
touching a keyboard. See [`sheepdog-v1-plan.md`](sheepdog-v1-plan.md)
for the full design and [`docs/architecture.md`](docs/architecture.md)
for a shorter orientation.

**Status:** WP1–WP7a are in — herdr client, state table, the Gemini
Live voice session, the supervisor and its five voice tools, the CLI
query tool behind `ask()`'s deep path, the daemon (`sheepdog serve`)
with `/voice` and `/debug`, the herdr plugin shim, and packaging/docs.
WP7b ran the plan's scenarios against real agy, Codex and Claude Code
panes and a live Gemini session: see [`docs/e2e-results.md`](docs/e2e-results.md)
(scenario table, measured latencies, open issues) and the recorded event
log in `testdata/e2e/`.

## Install in 3 steps

**1. Get the binary.** Download a release from GitHub Releases
(`sheepdog_<version>_darwin_arm64.tar.gz` or `..._linux_amd64.tar.gz`,
verify against `checksums.txt`), or:

```sh
go install github.com/kccarlos/herdr-sheepdog/cmd/sheepdog@latest   # -> $(go env GOPATH)/bin/sheepdog
```

or build from a checkout (requires Go; see `go.mod`):

```sh
git clone https://github.com/kccarlos/herdr-sheepdog.git && cd herdr-sheepdog
make build   # -> bin/sheepdog
```

**2. Configure it.**

```sh
sheepdog config init            # writes ~/.config/sheepdog/config.toml (commented template)
cp .env.example .env            # fill in GEMINI_API_KEY (Google AI Studio, free tier works)
```

`sheepdog serve` loads `./.env` on its own (no `source` needed); see
[`CONFIG.md`](CONFIG.md) for every key. `sheepdog config show` prints the
effective config with secrets redacted, so you can check what actually
loaded.

**3. Wire up herdr.** From the repo (or wherever you installed
`plugin/`):

```sh
plugin/install.sh
```

This links the herdr plugin shim (`herdr plugin link`) so herdr forwards
every agent-state event to the daemon over HTTP, and on macOS also
renders and (re)loads a launchd agent that runs `sheepdog serve` in the
background and restarts it if it exits. On Linux (no launchd), or if you
skip this step, just run `sheepdog serve` yourself. `make plugin-install`
runs the same script; `SHEEPDOG_DRY_RUN=1 plugin/install.sh` shows what
it would do without touching anything, and `plugin/uninstall.sh`
reverses it.

Then open **http://127.0.0.1:7777/voice** (or whatever `web.port` you
configured) and hit Connect.

## Usage

```sh
sheepdog serve                              # the daemon: http://127.0.0.1:7777/debug
sheepdog serve --voice=off --herdr=mock     # no Gemini, no herdr: drive it from /debug
sheepdog serve --verbose --port 8080        # echo every event to stderr
sheepdog serve --cli=off                    # disable the CLI query tool; ask() answers from the screen only
sheepdog serve --brain=off                  # keep ask/propose on the template path (no supervisor brain)
sheepdog serve --env-file path/to/.env      # load KEY=VALUE lines before config (default ./.env if present)
sheepdog serve --voice=off --herdr=mock --dev  # also serve testdata/audio/*.wav at /testdata/ for /voice?src=file
```

| Command | What it does |
|---|---|
| `sheepdog serve` | The daemon: herdr watcher, supervisor, voice session, web server (`/voice`, `/debug`, `/ws`, `/events`). |
| `sheepdog config show` \| `init` | Print the effective config (redacted) \| scaffold `config.toml`. |
| `sheepdog herdr watch` | Print debounced agent state transitions as JSONL; `--once` prints the persisted state table and exits. |
| `sheepdog voice-test` | Stream a WAV (or `--text`) through a Gemini Live session with the echo tools; no daemon needed. |
| `sheepdog llm-test` | Run one cheap-LLM template (`summarize_block`, `resolve_instruction`, `spoken_answer`, `summarize_done`) against a screen or instruction. |
| `sheepdog ask-test` | Run the CLI query tool (codex/claude) plus the `spoken_answer` pass once. |
| `sheepdog brain-test` | Run one supervisor-brain turn (`--question`, `--kind ask\|propose`) against a mock herdr with a screen fixture, an optional seed transcript and `--say` utterances; prints the reply and the transcript, `brain.*` events on stderr (`--fake` needs no network). |
| `sheepdog version` \| `help` | Version/commit/build date \| this table. |

`/voice`: mic → AudioWorklet → 16 kHz PCM16 → WebSocket, gapless 24 kHz
playback, mute/stop, auto-reconnect, a silence gate for quiet mics.
`/debug`: four tabs — **Swimlane** (the default: a sequence diagram with
four lanes, You · Voice model · Supervisor · herdr / LLM / CLI, one card
per event and arrows for every hop with its latency, chains that light up
across lanes when you click a tool call or an arrow, compressed quiet time,
lane toggles and the Events-tab filters; a card click shows its raw JSON;
under 700 px it becomes one column with a lane badge on each card),
**Conversation** (the voice session as a chat: system prompt, your
utterances, the dog's replies, tool calls that expand to their arguments,
result and the supervisor events behind them, supervisor pushes, session
dividers, per-turn response times), **Events** (the live event log with
filters) and **State** (state table, pending action, fake-event injection
so the tool path is testable without a mic or herdr) — see
[`docs/ws-contract.md`](docs/ws-contract.md). Everything on
`/debug` is read off the event log (D10); `GET /api/voice/system_prompt`
returns the prompt the session runs with, and the header's chattiness
dropdown switches the push policy at runtime (see below).

## The supervisor brain

Behind `ask` and `propose` sits the SVP of [`docs/roles.md`](docs/roles.md):
a stateful agent (`internal/brain`, `gemini-3.5-flash` with function
calling) that keeps one transcript per daemon run — your verbatim words,
what the voice said, every push and worker state change, its own tool
calls — so "why did *that one* fail?" still means something when it reads
a screen. It reasons with `read_screen`, `run_cli`, `list_agents` and
`recall`, acts with `prompt`, `send_keys` and `interrupt`, and treats
everything read from a worker as data, never instructions. The one rail:
it may answer a worker's approval prompt only on your words in that turn,
with a key from the prompt's own options; the code enforces it, not the
prompt. It falls back to the fast template path when it errors or runs out
of budget (`brain.ask_timeout_s`, `brain.max_steps`), so the voice never
waits on a loop. `sheepdog serve --brain=off` turns it off; it is also off
without a Gemini key. Try it without a daemon: `sheepdog brain-test
--question "what did it say?" --say "what did it say?" --fixture
codex-after-approval.screen.txt`. The transcript lives at
`<data_dir>/brain/<run-id>.jsonl`; every step is a `brain.*` event on
`/debug`.

## How chatty is it?

Sheepdog volunteers three kinds of news without being asked, and you pick
how much of it you want to hear. Change it any time from the dropdown in
the `/debug` header (or `PUT /api/settings {"chattiness": "…"}`); the
choice is kept in the state file, so it survives a restart. The initial
value comes from `supervisor.chattiness` in the config (default `normal`).

| Level | Announced |
|---|---|
| `quiet` | An agent's question ("auth is asking whether it can create hello.txt") and an agent exiting or losing its pane while busy. Nothing else. |
| `normal` | `quiet`, plus: informational blocks (a trust dialog, an idle prompt herdr flagged), the "dropping the earlier question from …" phrasing when a new question displaces one, and the finish of any agent you dealt with recently, with a one-line result: "auth finished: created hello.txt with the requested line." |
| `chatty` | `normal`, plus every finish (agents you never touched too) and "docs started on 'write the readme'" when an instruction you sent is picked up. |

"Dealt with recently" means that within the last `supervisor.recent_minutes`
(default 30) the agent was blocked and announced, was sent an answer or an
instruction, was asked about, or was named in a briefing you asked for
(the silent 10-minute context refresh does not count). An agent going
from done to idle is never announced at any level; a finish that follows
your own "stop" is not announced either.

Every finish is summarised whatever the level (the `summarize_done`
template reads the pane's last 200 lines) and stored as the agent's
`last_result`, so the next briefing can say "auth is idle; it last
created hello.txt with the requested line" without a model call. A
finish whose screen ends in a question herdr did not flag as blocked
(Antigravity CLI does this) is treated as the question it is, at every
level.

## ⚠️ Worker agents must not run fully auto-approve

Sheepdog's `propose()` sends an instruction to a worker immediately
after echoing it back by voice — there is **no confirmation step**
(plan decision D4). The worker's own approval prompts are the only
safety gate between "the operator said something" and "code changed."
If a worker is configured to approve everything on its own, that gate
is gone.

**Before pointing sheepdog at a pane, check the agent was *not* started
with one of these:**

| Agent | Flags to avoid |
|---|---|
| Codex | `--full-auto`, `-a never` (or any config setting that disables approval prompts). Start it as `codex --sandbox read-only -a on-request`: that is the mode that reliably prompts for file changes and commands. `--sandbox workspace-write -a on-request` runs workspace writes silently and never asked in the WP7b run or the first headset session, so nothing proactive ever happened. |
| Claude Code | `--dangerously-skip-permissions`, `--permission-mode bypassPermissions` |
| agy (Antigravity CLI) | `--dangerously-skip-permissions`, `--mode accept-edits` (verified on 1.2.5: the default mode prompts for commands and file edits) |
| anything else herdr supports | check its own docs for the equivalent "skip all approvals" flag and avoid it too |

(The CLI query tool behind `ask()`'s deep path is a separate, intentionally
read-only invocation of codex/claude — see
[`docs/cli-query-tools.md`](docs/cli-query-tools.md) — and is not
affected by this; this warning is about the worker panes herdr manages.)

## Microphone permission

`/voice` needs mic access from the browser, which needs it from macOS:

- **macOS:** System Settings → Privacy & Security → Microphone → enable
  it for your browser (Chrome, Safari, …).
- **Browser:** Chrome/Safari prompt for mic access the first time you
  hit Connect on `/voice`. If you dismissed it, the browser's site
  settings (the padlock/site-info icon in the address bar) let you
  reset the permission.
- **Origin matters:** the permission is scoped to the exact
  `scheme://host:port` you opened, so `http://127.0.0.1:7777/voice` and
  `http://localhost:7777/voice` are two different origins the browser
  will ask about separately, and a different `--port` is a third. Open
  the page at the same host:port sheepdog printed on startup.
- `http://127.0.0.1` and `http://localhost` are both treated as secure
  contexts by browsers, so mic access works without HTTPS (v1 is
  localhost-only by design, plan §1).

## Latency expectations

- **herdr detecting a blocked agent, end to end:** the plan's success
  test (§1) targets hearing sheepdog announce a blocked agent within
  ~5 s of it happening, without being asked. That budget is: herdr's own
  screen-polling detection (measured in WP7b at 3–4.6 s for Claude Code
  and ~9 s for Codex after the prompt was submitted) + sheepdog's
  `debounce_ms` (default 3000 ms, plan §3.1 — a `blocked` state must hold
  for this long before it becomes a transition, so a one-frame screen
  flicker doesn't fire early) + one cheap-LLM `summarize_block` call
  (0.8–1.3 s) + the model's first audio (0.6–0.9 s). Measured: 4.6–5.0 s
  from the `blocked` event to the first spoken audio
  (`docs/e2e-results.md`). herdr 0.9.0's bundled/remote detection rules
  do not recognise Antigravity CLI 1.2.5 prompts, so `agy` panes are
  never reported `blocked` (see the same document).
- **`briefing`:** answered from the in-memory state table cache, no
  model call — target under 1.2 s total added latency (utterance end to
  audio start, plan §9 WP7 acceptance). Measured in WP7b: 0.92–0.99 s for
  a typed utterance, 1.6–1.9 s for spoken audio, of which ~1 s is the
  Live API's end-of-speech detection and < 1 ms is sheepdog's handler.
- **`ask`/`propose` with a deep CLI lookup:** dominated by the codex/claude
  invocation itself, typically several seconds to tens of seconds (see
  the "Observed" numbers in
  [`docs/cli-query-tools.md`](docs/cli-query-tools.md)); sheepdog tells
  the user it's checking rather than going quiet.

## Quota notes

- Gemini free-tier API keys work for both the voice session and the
  default cheap-LLM backend, but rate-limit sooner than a paid key.
- `gemini.api_keys` (or `GEMINI_API_KEY` / `GEMINI_API_KEY_1`…`_9`) is a
  **pool**: the voice session binds one key per connection and rotates
  to the next on a rate-limit error (`voice.key_rotated` in the event
  log); the cheap-LLM client does the same per call
  (`llm.rate_limited`). Add a few keys if you're hitting limits.
- Watch `/debug` (source `voice` or `llm`) or `sheepdog serve --verbose`
  for `rate_limited`/`key_rotated` events to see this happening.

## Ollama fallback for the cheap LLM

The cheap LLM (`[llm]`, plan §3.4) is any OpenAI-compatible chat
endpoint, so a local Ollama server works without any API key:

```sh
SHEEPDOG_LLM_BASE_URL=http://localhost:11434/v1 \
SHEEPDOG_LLM_MODEL=llama3.1 \
SHEEPDOG_LLM_API_KEY=ollama \
sheepdog serve
```

(`SHEEPDOG_LLM_API_KEY` can be any non-empty string; local endpoints
usually ignore it.) The voice session itself still needs a real Gemini
key — only `[llm]` is swappable.

## Development

Requires Go (see `go.mod` for the version) and
[golangci-lint](https://golangci-lint.run/) v2.

```sh
cp .env.example .env   # fill in GEMINI_API_KEY etc.; loaded automatically by serve --env-file
make build              # -> bin/sheepdog
make run-dev             # serve --voice=off --herdr=mock --dev, for poking at /debug
make test               # go test -race ./...
make test-web            # web/voice/*.test.mjs (skipped if node is not on PATH)
make vet                # go vet ./...
make lint                # golangci-lint run
make cross               # dist/sheepdog-darwin-arm64, dist/sheepdog-linux-amd64
make install              # copies bin/sheepdog to ~/.local/bin/
make changelog            # regenerates CHANGELOG.md (scripts/changelog.sh)
make clean
```

See [`sheepdog-v1-plan.md`](sheepdog-v1-plan.md) for the full design and
work package breakdown, and [`CONFIG.md`](CONFIG.md) for every config
key.
