# Private end-to-end environment

`scripts/e2e/` gives an agent (or you) a private copy of the whole stack to
verify a feature on running programs, not just in tests. It never touches the
live daemon on `:7777`, `~/.config/sheepdog`, `~/.local/share/sheepdog`,
`~/.config/herdr-app`, your own `hc` core, your Herdr window or Sheepdog.app.
macOS only for the window part; `--no-herdr-app` runs the daemon and core
anywhere.

```sh
scripts/e2e/up.sh [--port N] [--panel-from <dir>] [--no-herdr-app]
scripts/e2e/scenario.sh <dir> <scenario> [arg]
scripts/e2e/shot.sh <dir> <name>
scripts/e2e/down.sh <dir>
scripts/e2e/selftest.sh [--no-herdr-app]
```

## What `up.sh` makes

Everything lives in `/tmp/sde-<id>/` (a short path, because of the unix socket
limit); `env.sh` there records ports, pids and variables.

- The daemon, built from this worktree, with a private `HOME` and `XDG_*`:
  `serve --brain=fake --herdr=mock --voice=off --dev --port N` (a free port
  from 18000, never 7777).
- A private herdr-core: `hc server start` with `HC_SESSION=sde<id>` and
  `HC_SOCKET=<dir>/hc.sock`.
- Unless `--no-herdr-app`: a private `HERDR_APP_HOME` holding a copy of the
  panel (`--panel-from` is a worktree root or a `mods/sheepdog` folder, default
  this worktree) whose `DEFAULT_URL` points at the private daemon, plus the
  `sheepdog` layout. The first-run welcome and what's-new dialogs are
  pre-dismissed through their state files (no app command closes them). Herdr
  starts with `open -n -a ~/Applications/Herdr.app --env ...` (not through the
  `herdr-app` symlink: a launch from the CLI is hidden from computer-use
  screenshots), then switches to the layout, makes a space `e2e` and maximizes.
  Its pid is found with `ps -E` by its `HERDR_APP_HOME`.

`down.sh` kills only the recorded pids, runs `hc server stop` on the private
core and removes the directory; running it twice is fine. Nothing is ever
killed by name.

Raise the private window by pid (`up.sh` prints the line):

```sh
osascript -e 'tell application "System Events" to set frontmost of (first process whose unix id is <pid>) to true'
```

To use the tools by hand: `source /tmp/sde-<id>/env.sh`, then `herdr-app status`,
`hc ls` and `curl $E2E_URL/api/state` all address the private stack.

## Scenarios

| Scenario | Effect |
|---|---|
| `approval` | `POST /api/inject/blocked` with the Codex approval screen from `testdata/herdr/`: the supervisor's real blocked path parses it and records a real `answer_prompt` pending, so the question card and the conversation's approval item appear. Nothing is faked in state. |
| `agent-blocked` | An agent blocked on a free-form question (no approval card). |
| `agent-working`, `agent-done` | An agent working, then finishing (`/api/inject/pane`, `/api/inject/state`). |
| `voice-client [connected\|speaking\|disconnected]` | `scripts/e2e/wsclient` attaches to `/ws` as a `macos` client with no audio, so `/api/state` `voice.client` is real and `client_connected` is true. `speaking` also sends a text message; it needs `voice-on`. `disconnected` stops the client. |
| `voice-on` / `voice-off` | Restart the daemon with `--voice=fake` / `--voice=off`. |
| `daemon-down` / `daemon-up` | Stop and start the private daemon (same port). |

The fake voice (`--voice=fake`, `internal/voice/fakevoice`) is a developer
mode like `--herdr=mock`: it needs `--dev`, does no network, reports the voice
open at once, holds one client, and answers a text message with a canned
transcript while it claims to speak for 6 s. It never calls a tool and never
touches a pane, so it cannot answer an approval. Production builds refuse
`--voice=fake` without `--dev`, and `/api/inject/*` stays loopback-only.

## Driving the window

Shoot with `shot.sh <dir> <name>` (writes `<dir>/shots/<name>.png`, prints
`herdr-app status` so a mod error shows). For keys, note that computer-use
captures Escape globally: use System Events key code 53 with computer-use
idle, or read the panel's `onKey` debug log.

The acceptance steps for the panel are in
[`e2e/checklists/panel.md`](e2e/checklists/panel.md).

## Limits

The brain is the scripted reflex model, so replies are canned. Disk: the
daemon build is the only large step; `up.sh` refuses to start below 8 GB free.
Every wait in the scripts is time-limited.
