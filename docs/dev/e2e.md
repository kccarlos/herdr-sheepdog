# Private end-to-end environment

`scripts/e2e/` gives an agent (or you) a private copy of the whole stack to
verify a feature on running programs, not just in tests. It never touches the
live daemon on `:7777`, `~/.config/sheepdog`, `~/.local/share/sheepdog`,
`~/.config/herdr-app`, your own `hc` core, your Herdr window or Sheepdog.app.
macOS only for the window part; `--no-herdr-app` runs the daemon and core
anywhere.

```sh
scripts/e2e/up.sh [--port N] [--panel-from <dir>] [--no-herdr-app]
                  [--herdr mock|socket] [--brain fake|off] [--cli on|off]
scripts/e2e/scenario.sh <dir> <scenario> [arg]
scripts/e2e/shot.sh <dir> <name>
scripts/e2e/down.sh <dir>
scripts/e2e/selftest.sh [--no-herdr-app]
scripts/e2e/panel-auto.sh [--shots <dir>]
scripts/e2e/real-agent.sh claude|codex
scripts/e2e/real-voice.sh [--only <scenarios>]
scripts/e2e/nightly.sh [--only vm,linux,real] [--no-fixer]
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

`--herdr socket` aims the daemon at the private core (`SHEEPDOG_HERDR_BACKEND=hc`
and `SHEEPDOG_HC_SOCKET` set explicitly), so real programs started there with
`hc run` are its agents; the real-agent check uses it with `--brain off --cli
off`. The core starts before the daemon.

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

## Nightly regression

`scripts/e2e/nightly.sh` runs every night at 03:00 on the maintainer's Mac and
checks one commit in three places. Every step has a time limit (perl `alarm`,
because the VM and a stock Mac have no `timeout`). Its log is
`<report dir>/<step>.log`.

| Part | Where | What runs |
|---|---|---|
| `vm` | Tart VM `sd-qa`, reset from the clean snapshot `sd-qa-base` each night | It checks out the commit and runs `selftest.sh` (with the Herdr window), then `panel-auto.sh`, then copies the screenshots back and stops the VM. |
| `linux` | `ssh oci` (Ubuntu arm64) | It checks out the commit and runs `go test ./...`, `selftest.sh --no-herdr-app` and `real-agent.sh claude` / `codex`. The remote commands also run under `timeout`. |
| `real` | this Mac, private environment only | `real-voice.sh`, `real-agent.sh claude` and `real-agent.sh codex`. |

The VM and Linux check out the same commit as the clone that runs the job.
If that commit is not pushed, they check out `origin/main`, and the report
says so.

- **`panel-auto.sh`** is the scriptable part of the
  [panel checklist](e2e/checklists/panel.md). It presses real keys through
  System Events, so it runs only in the VM, where Accessibility is granted and
  nobody types. It checks:
  - Composer: Enter, a line, then Enter puts the line in `/api/conversation`
    as `typed` and not in the terminal.
  - Esc: Esc, Esc, then a typed `echo` runs in the terminal (`hc read`).
  - Approvals: an approval shows within 2 s, and `1` on the panel answers it.
  - Daemon down and back up: `herdr-app status` shows no errors.

  It takes screenshots at each step.
- **`real-agent.sh claude|codex`** runs a private daemon with `--herdr socket
  --brain off --cli off` and starts a real, cheap agent in the private core:
  - Claude Code: `--model haiku --permission-mode manual`.
  - Codex: its configured model (luna), `--sandbox read-only
    --ask-for-approval on-request`. `NIGHTLY_CODEX_MODEL` overrides the model.

  The agent runs in `/tmp/sheepdog-nightly-agent/<cli>`, a throwaway git repo
  at a fixed path, so the folder trust is remembered. Its task is
  `touch nightly-ok.txt`. The check asserts that sheepdog detects the agent,
  sees it blocked and raises an `answer_prompt`. It answers each question
  through `POST /api/pending/answer` (the folder-trust question first, when
  there is one) and asserts the file appears and the agent is no longer
  blocked.

  The agent runs with your real `HOME`, so it uses the CLI's own login.
  Nothing is copied, and the daemon, the core and the repo are private. A CLI
  that is missing, logged out or whose login fails on screen (a revoked token)
  is a SKIP with the CLI's words, not a failure.
- **`real-voice.sh`** restarts the private daemon with `--voice=gemini`, still
  with the fake orchestrator and mock herdr, and speaks `audio-e2e` scenarios
  into its `/ws`. The defaults are `status-question` (heard, relayed, answered
  aloud) and `room-noise-only` (cafe noise is not heard as speech).
  `approval-yes` needs the real orchestrator, so it is not in the defaults.
  `audio-e2e` checks the transcript, tool call and reply in `/events`.

  The Gemini key never touches disk: the daemon runs under
  `op run --env-file=<template of op:// references> --`, with the
  `cloud-agents` service account token read from
  `~/.config/op/cloud-agents.token`. That token file is what makes `op` work
  under launchd. Without `op` or the token file, the step is a SKIP.

### Budgets

Real checks cost money, so `scripts/e2e/budget.sh` keeps a per-day counter in
`~/Library/Logs/sheepdog-nightly/budget.json` (local date). Every real run
reserves its turns before it starts, so a failed run still counts. Hand runs
and fix sessions count too. Past a cap the step is a SKIP.

| Kind | Cap per day | The nightly uses |
|---|---|---|
| voice turns (one per scenario) | 20 (`SHEEPDOG_BUDGET_VOICE`) | 2 |
| agent tasks (one per run) | 30 (`SHEEPDOG_BUDGET_AGENT`) | 4 (2 on the Mac, 2 on Linux) |

`scripts/e2e/budget.sh show` prints today's counts.

### Reports, failure and the fixer

Each run writes `~/Library/Logs/sheepdog-nightly/<YYYY-MM-DD>/`. It holds:

- `report.md`: the step table, the PASS/FAIL/SKIP lines, the `audio-e2e`
  table, the VM screenshots and the budget.
- one log per step.
- `shots/vm/*.png`.

A second run on the same day gets `-HHMMSS` added to the folder name. The run
also writes `~/Library/Logs/sheepdog-nightly/latest.json`
(`{date, status, report, commit, failed, fixer_pr}`), which an interactive
Claude loop can read. The script exits non-zero when any step failed.

On a failure it shows a macOS notification with the report path. It never
pushes to a phone. Then it hands the report to a headless Claude Code fixer,
which runs in a fresh worktree at `/tmp/sheepdog-nightly-fix-*` with this
command:

```sh
claude -p "<prompt>" --model sonnet --permission-mode auto
```

The fixer has a 45-minute limit. Its instructions are to reproduce the first
failure with `scripts/e2e`, find the root cause and fix it with a test. It
verifies with `make test`, `make vet lint`, `selftest.sh` and the failing
check, then pushes a branch, opens a PR titled `fix: nightly <date> <check>`
and writes `FIX.md` into the report folder. It must not merge, tag or touch
the live daemon or configs, and it must not create cloud storage. Its output
is in `fixer.log`.

The fixer does not start when any of these is true:

- you passed `--no-fixer`;
- another fixer holds `fixer.lock`;
- an open PR whose title has "nightly" already names the same check;
- `codexbar usage --provider claude` shows 20 % or less of the session left.

### Scheduling and running by hand

```sh
scripts/e2e/nightly-install.sh [--ref origin/<branch>]   # install and load
scripts/e2e/nightly-install.sh --uninstall
launchctl kickstart gui/$(id -u)/io.github.kccarlos.sheepdog-nightly   # run now
```

The installer makes a dedicated clone at `~/.sheepdog-nightly/herdr-sheepdog`,
so the job never runs from or changes a checkout you work in. It writes
`~/Library/LaunchAgents/io.github.kccarlos.sheepdog-nightly.plist` from the
template `scripts/e2e/io.github.kccarlos.sheepdog-nightly.plist`, which runs at
03:00 with `PATH` covering `~/.local/bin` and `/opt/homebrew/bin` for `tart`,
`go`, `op`, `gh`, `claude` and `codex`. It loads the job with
`launchctl bootstrap`.

Each scheduled run first moves the clone to `origin/main`. While main has no
`nightly.sh` yet, it uses `--ref` instead. Then the run starts again from the
new copy. launchd output goes to
`~/Library/Logs/sheepdog-nightly/launchd.log`.

The VM lives on an external disk, and macOS asks once before a launchd job
reads a removable volume: "bash" would like to access files on a removable
volume. Until someone clicks Allow, or allows `/bin/bash` under System
Settings, Privacy & Security, Files and Folders, the `vm` part fails with
that reason after 30 s. The other parts run anyway.

By hand, from any checkout:

```sh
scripts/e2e/nightly.sh                    # everything
scripts/e2e/nightly.sh --only vm          # or linux, real, or a list: vm,linux
scripts/e2e/nightly.sh --only real --no-fixer
```

Only one nightly runs at a time (`nightly.lock`). Do not use the VM while one
is running, because the run resets the VM.
