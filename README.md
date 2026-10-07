<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://github.com/kccarlos/herdr-sheepdog/raw/main/assets/logo-transparent.svg">
    <img src="https://github.com/kccarlos/herdr-sheepdog/raw/main/assets/logo-solid.svg" alt="sheepdog logo" width="160" height="160">
  </picture>
</p>

# herdr-sheepdog

A voice supervisor for a herdr agent fleet: ask what is happening, get
told proactively when an agent is blocked, answer its question, give new
instructions, and interrupt — without touching a keyboard. The voice is
Gemini Live (Google AI Studio or Vertex AI) or GPT-Live, switchable at
runtime; behind it a stateful orchestrator (Gemini, Gemini on Vertex
AI, or DeepSeek) reads the agents' screens and acts on them.

- [`docs/glossary.md`](docs/glossary.md) — the terms (user, voice, orchestrator, workers) and the former names
- [`docs/roles.md`](docs/roles.md) — the design contract
- [`docs/architecture.md`](docs/architecture.md) — the code map
- [`docs/status.md`](docs/status.md) — what is done, verified and open
- [`CONFIG.md`](CONFIG.md) — every config key

## Install

Build from a checkout (requires Go; see `go.mod`):

```sh
git clone https://github.com/kccarlos/herdr-sheepdog.git && cd herdr-sheepdog
make install   # builds bin/sheepdog and copies it to ~/.local/bin/
```

A tagged release on GitHub Releases carries
`sheepdog_<version>_darwin_arm64.tar.gz` and `..._linux_amd64.tar.gz`
with `checksums.txt`; the macOS binary is signed and notarized with
`make release-sign` ([`docs/signing.md`](docs/signing.md)).

## Configure

```sh
sheepdog config init     # writes ~/.config/sheepdog/config.toml (commented template)
cp .env.example .env     # fill in GEMINI_API_KEY at least
```

`sheepdog serve` loads `./.env` on its own. The keys it understands:
`GEMINI_API_KEY` (voice and the default orchestrator; `GEMINI_API_KEY_1`…`_9`
form a pool), `VERTEX_API_KEY` + `VERTEX_PROJECT` (Gemini on Vertex AI:
the voice with `gemini.backend = "vertex"`, and the orchestrator provider
`vertex`), `DEEPSEEK_API_KEY` (orchestrator provider `deepseek`) and
`OPENAI_API_KEY` (GPT-Live, voice only). `sheepdog config show` prints
the effective config with secrets redacted.

## Run

Start herdr, then the daemon:

```sh
sheepdog serve
```

The daemon subscribes to herdr's socket itself; nothing is installed on
herdr's side. Open **http://127.0.0.1:7777/voice** (or your `web.port`)
and hit Connect; **/debug** shows what the daemon, the voice and the
orchestrator are doing.

## Connect the phone

The daemon binds to `127.0.0.1` by default. To reach it from a phone on
the same LAN or over Tailscale, set a non-loopback bind and a token
(`config.toml`, or `SHEEPDOG_WEB_BIND` / `SHEEPDOG_WEB_TOKEN`):

```toml
[web]
bind = "0.0.0.0"       # or a specific LAN / Tailscale IP
token = "pick-something-long-and-random"
```

`sheepdog serve` refuses a non-loopback bind without a token. The banner
prints a `lan url` line for every non-loopback IPv4 address; use the LAN
or `100.x.x.x` (Tailscale) one.

- **iOS app** ("Sheepdog for herdr", repo `herdr-sheepdog-ios`,
  TestFlight internal testing): type the host:port from the `lan url`
  line and the token into its Settings. It speaks the same `/ws` contract
  (with its protocol handshake) as `/voice`, shows the workers with answerable prompts, a window onto each agent's screen with input (`/api/agents/{pane}/screen`, `.../input`), and gets a
  notification when an agent needs you once `[push.apns]` is configured
  ([`docs/push.md`](docs/push.md)).
- **Browser:** open `<lan url>/voice`; the page asks for the token once
  and keeps it in a cookie.

There is no TLS: keep this on a network you trust (a home LAN or
Tailscale), never the open internet. See
[`docs/ws-contract.md`](docs/ws-contract.md) "Auth".

## Commands

| Command | What it does |
|---|---|
| `sheepdog serve` | The daemon: herdr watcher, supervisor, orchestrator, voice session, web server (`/voice`, `/debug`, `/ws`, `/events`, `/api/*`). |
| `sheepdog config show` \| `init` | Print the effective config (redacted) \| scaffold `config.toml`. |
| `sheepdog herdr watch` | Print debounced agent state transitions as JSONL; `--once` prints the persisted state table. |
| `sheepdog voice-test` | Stream a WAV (or `--text`) through a Gemini Live session with echo tools; no daemon needed. |
| `sheepdog brain-test` | Run one orchestrator turn against a mock herdr and a screen fixture (`--question`, `--say`, `--provider`; `--fake` needs no network). |
| `sheepdog prompts export` \| `show` | Seed `prompts.dir` from the embedded prompts \| print one file's effective text and source. |
| `sheepdog eval` | The conversation eval sets (`--set assistant\|svp\|regressions\|extracted\|expanded\|all`); offline by default, `--live` calls the model. `eval extract` / `eval expand` build cases from the event log. `eval sim` runs simulated-user conversations graded by an LLM judge (`--live`, `--repeat`, `--fallback-provider`; [`docs/eval/eval-campaign-2026-10-03.md`](docs/eval/eval-campaign-2026-10-03.md)). |
| `sheepdog scorecard` | Ear and latency metrics over the event log ([`docs/iteration-2.md`](docs/iteration-2.md) "Scorecard metrics"). |
| `sheepdog bench` \| `bench validate` | The tier-2 voice benchmark: synthetic speech through a phone emulator into scratch daemons ([`docs/bench/README.md`](docs/bench/README.md), results in [`docs/bench/tier2-2026-10-03.md`](docs/bench/tier2-2026-10-03.md)). |
| `sheepdog audio-e2e` | Speak scripted scenarios (macOS `say` + noise) into a running daemon's `/ws` ([`docs/history/audio-e2e-results.md`](docs/history/audio-e2e-results.md)). |
| `sheepdog version` \| `help` | Version, commit and build date \| the command list. |

Useful `serve` flags:

```sh
sheepdog serve --voice=off --herdr=mock --dev              # no voice, no herdr: drive it from /debug
sheepdog serve --brain=fake --voice=off --herdr=mock --dev # the orchestrator loop over a scripted model, no key
sheepdog serve --voice=openai                              # start on GPT-Live (on | gemini | openai | off)
sheepdog serve --brain=off                                 # no orchestrator: code sentences only, nothing sent to a worker
sheepdog serve --cli=off                                   # no read-only CLI lookups
sheepdog serve --verbose --port 8080                       # echo every event to stderr
```

`--config`, `--env-file` (default `./.env`) and `--data-dir` point it at
other files.

`/debug` has six tabs: **Swimlane** (every hop between user, voice,
orchestrator and workers with its latency), **Conversation** (the voice session
as a chat), **Supervisor** (the orchestrator's transcript and prompt budget),
**Context** (each model's exact or reconstructed context, step by step;
[`docs/architecture.md`](docs/architecture.md) "Context capture"),
**Events** (the live event log) and **State** (the state table, the
pending action, fake-event injection). Its header switches the orchestrator
provider and model, the voice provider and the chattiness level at
runtime. **New conversation** (on `/debug` and `/voice`, or saying
"start over" and confirming) resets the orchestrator's transcript and the voice
model's memory but keeps the roster, promises and open prompts.

## How it behaves

The voice relays what you say to the orchestrator; the orchestrator answers, acts on
the workers (`prompt`, `send_keys`, `interrupt`), starts background
read-only **investigations** for questions a screen cannot answer, and
decides what is worth telling you when a worker blocks or finishes. The
contract is [`docs/roles.md`](docs/roles.md).

- **Promises:** "tell me when auth is done" is recorded and spoken at the
  matching event at every chattiness level; it survives a restart and
  expires after `supervisor.promise_ttl_s`.
- **Stop:** a bare "stop" (or "wait", "cancel", …) is acknowledged at
  once by the daemon and interrupts what is in flight; "never mind" is
  not a stop.
- **The one rail:** a worker's approval prompt is answered only on your
  own recorded words (or a tap) in that turn, with a key from the
  prompt's options. The code enforces it, not a prompt.
- **Prompts:** `sheepdog prompts export` seeds
  `~/.config/sheepdog/prompts/`; edits are picked up on `SIGHUP` or
  `POST /api/prompts/reload` ([`docs/roles.md`](docs/roles.md) "Prompt
  files").

### Which language?

Pick your languages, most preferred first, in the iOS app's Settings ▸
Languages (or `PUT /api/settings {"languages": ["zh-Hans", "en"]}`).
简体中文 and 繁體中文 are separate entries. The voice and the orchestrator are told the
setting and answer in the language you use, in your script; the choice
survives a restart.

### How chatty is it?

Pick the level from the `/debug` header (or
`PUT /api/settings {"chattiness": "…"}`); the choice survives a restart,
and `supervisor.chattiness` (default `normal`) is the initial value.

| Level | Announced |
|---|---|
| `quiet` | An agent's question, an agent exiting or losing its pane while busy, and kept promises. |
| `normal` | `quiet`, plus informational blocks and the finish of any agent you dealt with in the last `supervisor.recent_minutes`. |
| `chatty` | `normal`, plus every finish and "started on …" when an instruction you sent is picked up. |

The level is a ceiling, not the sentence: for every block or finish the
orchestrator decides whether to say anything and how, and the level caps it.
A block is spoken within `brain.announce_wait_s`; a finish waits for the
orchestrator's line with what the agent found ([`docs/roles.md`](docs/roles.md)
"Pushes").

## ⚠️ Worker agents must not run fully auto-approve

The orchestrator sends an instruction to a worker right after echoing it back —
there is no confirmation step. The worker's own approval prompts are the
only safety gate between "the operator said something" and "code
changed". Before pointing sheepdog at a pane, check the agent was *not*
started with one of these:

| Agent | Flags to avoid |
|---|---|
| Codex | `--full-auto`, `-a never` (or any config that disables approval prompts). `codex --sandbox read-only -a on-request` reliably prompts for file changes and commands; `--sandbox workspace-write -a on-request` writes inside the workspace without asking. |
| Claude Code | `--dangerously-skip-permissions`, `--permission-mode bypassPermissions` |
| agy (Antigravity CLI) | `--dangerously-skip-permissions`, `--mode accept-edits` |
| anything else herdr supports | its own "skip all approvals" flag |

The CLI behind investigations is a separate, read-only invocation of
codex/claude ([`docs/cli-query-tools.md`](docs/cli-query-tools.md)) and
is not affected.

## Microphone permission

- **macOS:** System Settings → Privacy & Security → Microphone → enable
  your browser.
- **Browser:** the permission is per origin, so `127.0.0.1:7777` and
  `localhost:7777` are asked about separately. Both count as secure
  contexts, so no HTTPS is needed there.

## Latency and quota

- A blocked agent is announced about 5 s after herdr reports the block
  (`debounce_ms` 3 s, then the parsed prompt); herdr's own screen
  detection adds a few seconds before that.
- From the 2026-10-02 phone run: a status question answered in about
  2 s, an instruction delivered in about 4 s, an investigation answered
  in about 30 s (acknowledged at once).
- Gemini free-tier keys work but rate-limit early; the voice rotates
  through the key pool on a 429 (`voice.key_rotated`). Vertex AI and
  DeepSeek are paid per token; GPT-Live is billed per minute of open
  session and closes when idle.

## Development

Requires Go (`go.mod`), [golangci-lint](https://golangci-lint.run/) v2,
and Node for the browser tests.

```sh
make build         # bin/sheepdog
make run-dev       # serve --voice=off --herdr=mock --dev, for poking at /debug
make test          # go test -race ./...
make test-web      # web/voice and web/debug *.test.mjs (skipped without node)
make vet lint
make eval          # offline eval sets (no network)
make eval-live     # the small eval sets against the real model (burns quota)
make cross         # dist/sheepdog-darwin-arm64, dist/sheepdog-linux-amd64
make install       # copy bin/sheepdog to ~/.local/bin/
make changelog     # regenerate CHANGELOG.md
make release-sign TAG=v0.1.0   # sign and notarize a release's darwin binary (docs/signing.md)
```

### CI/CD

GitHub Actions runs Linux jobs only, on self-hosted runners in Cloud Run
worker pools ([infra/ci-runners](infra/ci-runners/README.md); repo variable
`CI_RUNNER=ubuntu-latest` switches back to GitHub-hosted): vet, test, lint, web
tests, the offline eval, the policy guard and the cross-compile on every PR, and the
Ubuntu release on `v*` tags. Anything Xcode- or iOS-related (build, test,
archive, TestFlight) runs only on Xcode Cloud, never on GitHub Actions,
because macOS runners are too expensive; the `policy` CI job fails if a
workflow uses a `macos-*` runner or calls `xcodebuild`. Signing the darwin
release binary stays on the maintainer's Mac ([docs/signing.md](docs/signing.md)).

The original v1 plan and older run reports are in
[`docs/history/`](docs/history/).
