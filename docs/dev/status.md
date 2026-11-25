# Status — 2026-11-25

Living handoff note: what is on `main`, what is verified how, and what is
open. The contract is [`roles.md`](roles.md), the code map is
[`architecture.md`](architecture.md), the factory engine v2 design and plan
are [`engine-v2/design.md`](engine-v2/design.md) and
[`engine-v2/plan.md`](engine-v2/plan.md), every option is in
[`CONFIG.md`](../../CONFIG.md), and release history is in GitHub Releases
(latest daemon release `v0.1.30`; `git log` for PR-by-PR history).

## Where main is

The voice + orchestrator daemon, the desktop client (herdr-app panel +
`Sheepdog.app`), the iPhone/iPad app in [`ios/`](../../ios/README.md), and
**factory engine v2** slices 1–9, 11, 12, and 14 are all on `main`.

**CI and releases.** Linux CI runs on self-hosted Cloud Run runners, iOS
unit and UI tests run on GitHub-hosted Macs (`ios.yml`, secret-free against
fixture daemons), daemon/Mac signing runs on `v*` tags in `release.yml`,
and TestFlight builds run on Xcode Cloud on `ios-v*` tags (`ios/project.yml`
is at `0.2.12`). Details live in [`development.md`](development.md#cicd),
[`signing.md`](signing.md),
[`ios/docs/dev/xcode-cloud.md`](../../ios/docs/dev/xcode-cloud.md), and
[`ios/docs/dev/status.md`](../../ios/docs/dev/status.md).

What is on `main`:

- **herdr backends:** both herdr-tui (`internal/herdr`) and herdr-core
  (`internal/hc`, `internal/herdrcore`) behind `herdr.backend = auto|herdr|hc`,
  with debounced transitions, persisted state table, event log with stable ids
  and `Last-Event-ID` replay, shared seen/pinned attention marks
  (`internal/attention`), space mutes, and code-parsed approval prompts.
- **Voice & orchestrator:** dual-brain architecture ([`design/dual-brain.md`](design/dual-brain.md))
  with Gemini Live (AI Studio or Vertex AI) and GPT-Live switchable at runtime,
  stateful orchestrator (`internal/brain`: Gemini, Vertex AI, DeepSeek,
  Anthropic, and `[[brain.openai_compat]]`), language preference
  (`internal/lang`), approval rail in code (`supervisor.authorise_window_s`),
  background investigations, display cards (`show`), and end-to-end encrypted
  push notifications via the shared relay (`push.mode = "relay"`, default) or
  direct APNs ([`push.md`](push.md)).
- **Security & surfaces:** paired-device P-256 keys, pinned TLS on one port,
  and `sds_` session tickets (`internal/pair`, `internal/web/auth.go`;
  [`design/security.md`](design/security.md) — static `web.token` is gone).
  Embedded web pages at `/voice` (public), `/setup` (`accessSetup`), `/debug`
  and `/factory` (`accessLoopback`). Live phone terminal (`/term`, SwiftTerm,
  `internal/web/term.go`), git changes, and read-only file browser
  (`internal/files/`, `internal/web/files.go`).
- **iPhone & iPad app (`ios/`):** compact cards, iPad sidebar/split/multi-window
  (`IPadRootView.swift`), New agent + Spaces + worktrees
  (`internal/web/spaces.go`), multiple paired computers (`ComputerFleet`),
  Live Activity with push updates, and AI consent flow.
- **Desktop client (complete):** herdr-app panel (`mods/sheepdog/`, synced to
  `sheepdog-layout/`), `Sheepdog.app` menu-bar app (`ios/Sources/SheepdogMac/`,
  holding the Mac voice session, notifications, global hotkey, and **Pair
  browser…** short codes for `/factory`), signed and notarized via
  `scripts/release-sign-app.sh` and distributed via the Homebrew cask
  `sheepdog-app` ([`docs/desktop.md`](../desktop.md)).
- **Factory engine v2 (`internal/factory`, slices 1–9, 11, 12, and 14 on `main`):**
  single-writer Go engine inside the daemon driving projects from plan to merge
  over `herdr-core`, git worktrees, and GitHub (`gh`), replacing the removed
  Python engine (`factory/`). Merged on `main`: version 1 (slices 1–8),
  **O32** (no quick-reply options on factory ask pushes, #774), **slice 9**
  (`/factory` web board, #777), **slice 11** (plan review before tasks start,
  #800, #803, #807), **slice 12** (the retro round on non-converging majors,
  #780, #784, #794), and **slice 14** (the factory's whole life on `/factory`:
  project overview, lifecycle check/start/stop, settings panel with gated
  `rule` asks, task step bar, and task graph, 14.1–14.10, #801–#819).
  **Slice 10** (engine-run evidence) was parked by the owner on 2026-11-23
  (#775). Full design and slice records: [`engine-v2/design.md`](engine-v2/design.md),
  [`engine-v2/plan.md`](engine-v2/plan.md).
- **Evals & observability:** [`eval/README.md`](eval/README.md),
  `sheepdog dev eval`, `sheepdog dev scorecard`, `sheepdog dev feedback`,
  `sheepdog dev audio-e2e`, `sheepdog dev bench`, per-utterance `turn.latency`,
  and the private E2E harness + nightly regression ([`e2e.md`](e2e.md)).

## Open

- **Next up:** docs pass, then daemon release `v0.1.31`.
- **Factory engine v2 remaining slices ([`engine-v2/plan.md`](engine-v2/plan.md)):**
  slice 13 (coordinator rotation and journal), and later phone / iPad factory
  boards. Slice 10 (engine-run evidence) is parked.
- **The roster in the orchestrator system text defeats prompt caching
  (deferred):** the system text lists the agent aliases, so any agent joining
  or leaving rewrites the cached prefix. Moving it out needs eval evidence
  first ([`eval/anthropic-opus-2026-10-19.md`](eval/anthropic-opus-2026-10-19.md),
  [`anthropic-adapter.md`](anthropic-adapter.md)).
- **Gemini Live chained resumes on Vertex:** Vertex loses context on a second
  resume ([`research/live-resume-context-2026-10-04.md`](research/live-resume-context-2026-10-04.md));
  mitigated in code by re-seeding fresh sessions on Vertex (AI Studio keeps
  context across chained resumes and refreshes cleanly at 54 min;
  [`gemini-live-notes.md`](gemini-live-notes.md#goaway)).
- **A stop does not drop a queued instruction:** a single Ctrl-C to a busy
  Claude Code pane interrupts the running turn but a queued prompt still runs.
- **Not yet checked on a device:** AirPods and route changes, and a
  multi-question (tabbed) AskUserQuestion widget.
- **agy blocks:** herdr 0.9.0 does not detect Antigravity CLI prompts as
  `blocked` (needs a local agent-detection override or an upstream fix).
- **Daemon signing note:** `v0.1.0` was unsigned; `v0.1.1` onward are signed
  and notarized in `release.yml` ([`signing.md`](signing.md)).
- **Scratch daemons:** `SHEEPDOG_DATA_DIR` does not move `state.path`; set
  `SHEEPDOG_STATE_PATH` too, or a second daemon shares the user's `state.json`.

## How work is done here

Conventional Commits; one PR per change; before opening a PR run `make vet
lint`, `make test` (Go race detector, includes `internal/docscheck`),
`make test-web` (`web/voice`, `web/debug`, `web/static`, `web/setup`,
`web/factory`, and `mods/sheepdog/*/*.test.mjs`), and `make eval` for prompt
or orchestrator changes. For `ios/` changes, run the simulator tests
([`CONTRIBUTING.md`](../../CONTRIBUTING.md)). CI is watched to green; PRs are
squash-merged with the conventional title as the subject. Agents never touch
port 7777 (the running daemon), the live herdr socket, or the default config
and data directories: a scratch daemon gets its own port, `SHEEPDOG_DATA_DIR`,
`SHEEPDOG_STATE_PATH`, an empty `--config`, and `--brain=fake --herdr=mock
--voice=off --dev` (or use [`scripts/e2e/up.sh`](e2e.md)). Documents are
written in English.
