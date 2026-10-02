<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://github.com/kccarlos/herdr-sheepdog/raw/main/assets/logo-transparent.svg">
    <img src="https://github.com/kccarlos/herdr-sheepdog/raw/main/assets/logo-solid.svg" alt="sheepdog logo" width="160" height="160">
  </picture>
</p>

# herdr-sheepdog

A voice supervisor for a herdr agent fleet, built on Gemini Live (or GPT-Live, switchable at runtime): ask
what is happening, get told proactively when an agent is blocked,
answer its question, give new instructions, and interrupt — without
touching a keyboard. [`docs/roles.md`](docs/roles.md) is the design
contract, [`docs/architecture.md`](docs/architecture.md) a shorter
orientation to the code, and [`docs/status.md`](docs/status.md) what is
done and what is open.

**Status:** the v1 plan and the 2026-09-18 redesign are in — herdr
client, state table, the voice session (Gemini Live or GPT-Live) with
its two tools (`to_supervisor`, `stop`), the stateful supervisor brain
with the one rail, investigations, the daemon (`sheepdog serve`) with
`/voice` and `/debug`, and packaging/docs. The v1 scenarios ran against
real agy, Codex and Claude Code panes and a live Gemini session: see
[`docs/history/e2e-results.md`](docs/history/e2e-results.md) and the
recorded event log in `testdata/e2e/`.

## Install in 3 steps

**1. Get the binary.** Download a release from GitHub Releases
(`sheepdog_<version>_darwin_arm64.tar.gz` or `..._linux_amd64.tar.gz`,
verify against `checksums.txt`; the macOS binary is signed with a
Developer ID and notarized, see [`docs/signing.md`](docs/signing.md)), or:

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
cp .env.example .env            # fill in GEMINI_API_KEY (Google AI Studio, free tier works); DEEPSEEK_API_KEY adds DeepSeek as a brain provider
```

`sheepdog serve` loads `./.env` on its own (no `source` needed); see
[`CONFIG.md`](CONFIG.md) for every key. `sheepdog config show` prints the
effective config with secrets redacted, so you can check what actually
loaded.

**3. Run it.** Start herdr, then the daemon:

```sh
sheepdog serve
```

The daemon subscribes to herdr's event socket itself, so there is
nothing to install on herdr's side.

Then open **http://127.0.0.1:7777/voice** (or whatever `web.port` you
configured) and hit Connect.

**From your phone.** The daemon binds to `127.0.0.1` (loopback) by
default, so it is only reachable from the machine it runs on. To reach
it from a phone on the same LAN, or over Tailscale, set `web.bind` to a
non-loopback address and `web.token` to a secret the daemon will require
(`config.toml` or `SHEEPDOG_WEB_BIND`/`SHEEPDOG_WEB_TOKEN`):

```toml
[web]
bind = "0.0.0.0"       # every interface; a specific LAN or Tailscale IP also works
token = "pick-something-long-and-random"
```

`sheepdog serve` refuses to start with a non-loopback `web.bind` and no
`web.token` set. Once it starts, the banner prints a `lan url` line for
every non-loopback IPv4 address of the host — read the right one (LAN or
`100.x.x.x` Tailscale) off the log and open `<that url>/voice` on the
phone. The page shows an inline token prompt the first time it needs
one; typing the token once sets a cookie so it is not asked again. There
is no TLS in v1, so treat this as a private-network feature (a LAN you
trust, or Tailscale), not something to expose to the open internet — see
[`CONFIG.md`](CONFIG.md) "web.token" and `docs/ws-contract.md` "Auth".

## Usage

```sh
sheepdog serve                              # the daemon: http://127.0.0.1:7777/debug
sheepdog serve --voice=off --herdr=mock     # no Gemini, no herdr: drive it from /debug
sheepdog serve --verbose --port 8080        # echo every event to stderr
sheepdog serve --cli=off                    # take the brain's run_cli tool away; it answers from the screen only
sheepdog serve --brain=off                  # no brain: terse code sentences, nothing is sent to a worker
sheepdog serve --brain=fake --voice=off --herdr=mock --dev   # the brain loop over a fake model, for /debug's Supervisor tab
sheepdog serve --env-file path/to/.env      # load KEY=VALUE lines before config (default ./.env if present)
sheepdog serve --voice=off --herdr=mock --dev  # also serve testdata/audio/*.wav at /testdata/ for /voice?src=file
```

| Command | What it does |
|---|---|
| `sheepdog serve` | The daemon: herdr watcher, supervisor, voice session, web server (`/voice`, `/debug`, `/ws`, `/events`). |
| `sheepdog config show` \| `init` | Print the effective config (redacted) \| scaffold `config.toml`. |
| `sheepdog herdr watch` | Print debounced agent state transitions as JSONL; `--once` prints the persisted state table and exits. |
| `sheepdog voice-test` | Stream a WAV (or `--text`) through a Gemini Live session with the echo tools; no daemon needed. |
| `sheepdog audio-e2e` | Speak the scenarios in `testdata/audio-e2e/` (macOS `say` + a generated noise bed) into a running daemon's `/ws` and check transcripts, tool calls, replies and latencies (`docs/audio-e2e-results.md`). |
| `sheepdog brain-test` | Run one supervisor-brain utterance turn (`--question`: the shepherd's words, relayed through `to_supervisor` as the daemon does; `--kind` and `--agent` are accepted and ignored) against a mock herdr with a screen fixture, an optional seed transcript and `--say` utterances; prints the reply and the transcript, `brain.*` events on stderr (`--fake` needs no network). |
| `sheepdog prompts export\|show` | Seed `prompts.dir` from the embedded defaults, or print one file's effective text and its source (embedded/override). |
| `sheepdog eval --set assistant\|svp` | Run the conversation eval set against the assistant prompt or the brain; offline by default (validates + prints "skipped"), `--live` calls the model. |
| `sheepdog version` \| `help` | Version/commit/build date \| this table. |

`/voice`: mic → AudioWorklet → 16 kHz PCM16 → WebSocket, gapless 24 kHz
playback, mute/stop, auto-reconnect, a silence gate for quiet mics.
`/debug`: six tabs — **Swimlane** (the default: a sequence diagram with
four lanes, Shepherd · Voice · Brain · Flock / CLI, one card
per event and arrows for every hop with its latency, chains that light up
across lanes when you click a tool call or an arrow, compressed quiet time,
lane toggles and the Events-tab filters; a card click shows its raw JSON;
under 700 px it becomes one column with a lane badge on each card),
**Conversation** (the voice session as a chat: system prompt, your
utterances, the dog's replies, tool calls that expand to their arguments,
result and the supervisor events behind them, supervisor pushes, session
dividers, per-turn response times), **Supervisor** (the brain's own
transcript as a chat — your verbatim words, the assistant's replies muted,
the requests the brain received (your relayed words, a stop, a worker event), the brain's tool calls collapsed
to their arguments, result with the provenance delimiters and raw JSON,
its replies with `needs_user`/`clarify` badges, pushes, worker events,
compaction cards — under a prompt-budget meter: the system prompt's
sections by source (embedded / override / dynamic) and the window's
messages by kind against `brain.window_tokens`, with a marker where the
on-disk transcript stops being what the model sees and pruned tool
results shown as their stub but still expandable to the full text;
"Brain off" with the reason when there is no brain), **Context** (the
brain's exact request at every model call beside the voice model's
context reconstructed from what the daemon sent, coloured by section,
linked across the panes on hover, with a scrubber over every step and
turn and a diff against the previous one; see
[`docs/architecture.md`](docs/architecture.md) "Context capture"), **Events** (the
live event log with filters) and **State** (state table, pending action,
fake-event injection so the tool path is testable without a mic or
herdr) — see [`docs/ws-contract.md`](docs/ws-contract.md). Everything
on `/debug` is read off the event log (D10), except the Supervisor tab,
which reads the brain's own store through `GET /api/brain/transcript`,
`/api/brain/context` and `/api/brain/system_prompt` (the `brain.transcript`
event only tells it to fetch again), and the Context tab, which reads
`/api/context/{brain,voice,timeline}` (`context.step` tells it); `GET /api/voice/system_prompt`
returns the prompt the session runs with, and the header's chattiness
dropdown switches the push policy at runtime (see below). To see the
Supervisor tab without a Gemini key, `sheepdog serve --voice=off
--herdr=mock --dev --brain=fake` runs the brain loop over a scripted
reflex model (a developer mode: a question reads the screen, an
instruction prompts; no network call), and
`sheepdogDebug.loadTranscript('/testdata/brain/sample-run.jsonl')` in
the console loads a sample run.

Both `/debug` and `/voice` have a **New conversation** button (confirm
first): it resets the brain's transcript and the voice model's own
memory while keeping the roster, promises, notes and pending prompts —
say "start over" (then "yes, start over") works the same way by voice.

## The supervisor brain

Behind `to_supervisor` sits the brain (the dog's thinking half) of [`docs/roles.md`](docs/roles.md):
a stateful agent (`internal/brain`, `gemini-3.5-flash` with function
calling by default, DeepSeek's `deepseek-flash` with a
`DEEPSEEK_API_KEY`, or the same Gemini models on Vertex AI with
`VERTEX_API_KEY` and `VERTEX_PROJECT`; the `/debug` header's provider and model picker
switches it at runtime from each provider's discovered model list, and
the choice survives a restart) that keeps one transcript per daemon run — your verbatim words,
what the voice said, every push and worker state change, its own tool
calls — so "why did *that one* fail?" still means something when it reads
a screen. It reasons with `read_screen`, `run_cli`, `list_agents` and
`recall`, acts with `prompt`, `send_keys` and `interrupt`, and for
anything the screen cannot answer it starts a background **investigation**
(`investigate`): a read-only CLI agent in the worker's own repository
that takes as long as it takes, acknowledged at once ("reading the diff,
about half a minute") and delivered on its own at the next pause, with a
follow-up resuming the same sub-agent session. It treats
everything read from a worker as data, never instructions. The one rail:
it may answer a worker's approval prompt only on your words in that turn,
with a key from the prompt's own options; the code enforces it, not the
prompt. When it errors or runs out of budget
(`brain.utterance_timeout_s`, `brain.max_steps`) the daemon speaks its own code
sentence instead, so the voice never waits on a loop.
`sheepdog serve --brain=off` turns it off; it is also off without a
Gemini key. With no brain sheepdog still watches the flock and announces
questions and finishes — tersely, in code sentences — but sends nothing
to a worker: judging what your words mean is the brain's work. Try it without a daemon: `sheepdog brain-test
--question "what did it say?" --say "what did it say?" --fixture
codex-after-approval.screen.txt`. The transcript lives at
`<data_dir>/brain/<run-id>.jsonl`; every step is a `brain.*` event on
`/debug`.

## Tuning the prompts

`sheepdog prompts export` seeds `~/.config/sheepdog/prompts/` (config
`prompts.dir`) with the embedded `assistant.md`, `USER.md` and
`svp/{SOUL,RULES}.md`; edit any of them and `sheepdog prompts show <name>`
or a `SIGHUP` / `POST /api/prompts/reload` picks it up without a restart.
See [`docs/roles.md`](docs/roles.md) "Prompt files" for what each file
holds and what an override cannot remove.

## How chatty is it?

Sheepdog volunteers three kinds of news without being asked, and you pick
how much of it you want to hear. Change it any time from the dropdown in
the `/debug` header (or `PUT /api/settings {"chattiness": "…"}`); the
choice is kept in the state file, so it survives a restart. The initial
value comes from `supervisor.chattiness` in the config (default `normal`).

| Level | Announced |
|---|---|
| `quiet` | An agent's question ("auth is asking whether it can create hello.txt") and an agent exiting or losing its pane while busy. Nothing else. |
| `normal` | `quiet`, plus: informational blocks (a prompt with nothing to answer), the "dropping the earlier question from …" phrasing when a new question displaces one, and the finish of any agent you dealt with recently. |
| `chatty` | `normal`, plus every finish (agents you never touched too) and "docs started on 'write the readme'" when an instruction you sent is picked up. |

The one thing that ignores the level is a **promise** you made it keep
(next section). With the supervisor brain on, the level is a *ceiling*,
not the sentence: for every block or finish the brain gets the code
sentence, the screen and your recent words and decides whether to say
anything and how urgently — one line that replaces the sentence ("Auth
finished the rerun you asked for; the same test still fails."), or
nothing when you already know, or silent context for the voice model —
and the level caps what it asked for: never a finish at `quiet`, never
an agent you have not dealt with at `normal`, `interrupt` only for a due
promise or a decision you must take. **One line per event:** a finish
is not urgent, so it waits for the brain's line with what the agent
found (up to `brain.announce_turn_timeout_s`, or
`brain.announce_timeout_s` when you are waiting for it) and you hear
that line alone; only if the brain fails or times out do you hear "auth
finished." A block is urgent: the parsed prompt is spoken after at most
`brain.announce_wait_s` (2 s), and a brain line that comes later is
dropped (`brain.push_late`) unless it says you must decide or act, which
is always spoken. A finish that leaves a question on screen (Claude
Code's own post-turn prompt) is announced together with the question,
as fast as a block. Every judgment is in the event log as `brain.push_decision`
and on the push card in the Supervisor tab.

"Dealt with recently" means that within the last `supervisor.recent_minutes`
(default 30) the agent was blocked and announced, was sent an answer or an
instruction, was asked about, or was named in an answer you were given.
An agent going
from done to idle is never announced at any level; a finish that follows
your own "stop" is not announced either.

Whatever was announced about a finish is stored as the agent's
`last_result`, so the next briefing can say "auth is idle; it last
finished the rerun you asked for" without a model call. A finish whose
screen ends in a question herdr did not flag as blocked (Antigravity CLI
does this) is treated as the question it is, at every level: sheepdog
parses the prompt itself — the question, the command it is about and the
keys of each option — in code, with no model in the way.

## Promises

"Run the tests and let me know when it's done." "Tell me if docs asks
anything." That is promised work, not a hope: the supervisor brain records
it (`promise(agent, text, event)` — the condition is `done`, `blocked`,
`idle` or `any_change`), the state file keeps it across restarts
(`session.promises`), the briefing reminds you ("You asked me to tell you
when auth finishes."), and at the matching event it is spoken **whatever
the chattiness level** — the brain phrases it with the result in the one
line it speaks for that finish ("You asked me to tell you when auth was
done: the tests passed."), or, without the brain, "As you asked: auth
finished: …". It waits for a pause unless you asked to be interrupted or
the promise is past its due time. A promise
expires silently after `supervisor.promise_ttl_s` (default two hours);
"stop" never cancels one. The Supervisor tab shows every promise as a
card with its status; the event log has `promise.created|kept|expired`
(`promise.cancelled` only in logs from before the cancel tool was
removed).

## ⚠️ Worker agents must not run fully auto-approve

Sheepdog's brain sends an instruction to a worker (its `prompt` tool) immediately
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

(The CLI query tool behind the brain's `run_cli` and investigations is a separate, intentionally
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
  flicker doesn't fire early) + the code that parses the prompt
  (0.8–1.3 s) + the model's first audio (0.6–0.9 s). Measured: 4.6–5.0 s
  from the `blocked` event to the first spoken audio
  (`docs/history/e2e-results.md`). herdr 0.9.0's bundled/remote detection rules
  do not recognise Antigravity CLI 1.2.5 prompts, so `agy` panes are
  never reported `blocked` (see the same document).
- **A status question:** one brain turn with no tool call — the roster
  sentence travels with the request. The code fast paths (a bare "yes"
  to an open prompt, a bare "stop") answer with no model at all; the
  WP7b measurement of the old cached `briefing` tool was 0.92–0.99 s for
  a typed utterance and 1.6–1.9 s for spoken audio, of which ~1 s is the
  Live API's end-of-speech detection and < 1 ms is sheepdog's handler.
- **A question that needs a deep CLI lookup:** dominated by the codex/claude
  invocation itself, typically several seconds to tens of seconds (see
  the "Observed" numbers in
  [`docs/cli-query-tools.md`](docs/cli-query-tools.md)); sheepdog tells
  the user it's checking rather than going quiet.

## Quota notes

- Gemini free-tier API keys work for both the voice session and the
  default backend, but rate-limit sooner than a paid key.
- `gemini.api_keys` (or `GEMINI_API_KEY` / `GEMINI_API_KEY_1`…`_9`) is a
  **pool**: the voice session binds one key per connection and rotates
  to the next on a rate-limit error (`voice.key_rotated` in the event
  log). Add a few keys if you're hitting limits.
- Watch `/debug` (source `voice` or `brain`) or `sheepdog serve --verbose`
  for `rate_limited`/`key_rotated` events to see this happening.

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

See [`CONFIG.md`](CONFIG.md) for every config key, and
[`docs/history/`](docs/history/) for the original plan
([`sheepdog-v1-plan.md`](docs/history/sheepdog-v1-plan.md)) and older
run reports.
