# factory

**Run coding agents unattended from a goal to merged pull requests, while you stay in charge of gated asks and overrides.**

The factory engine runs inside the sheepdog daemon. You give it a GitHub repository and a goal. A **coordinator** plans the work into small tasks, **workers** implement each task in its own git worktree, fresh **reviewers** check each diff, and the engine waits for GitHub CI before squash-merging into the base branch. By default, each diff must pass **two** review rounds on a model different from the worker (`claude` `sonnet` worker and `opus` reviewer), with up to 5 rounds before a coordinator recap (or earlier if a task grows past twice its line estimate).

You watch progress at `http://127.0.0.1:7777/factory` (or `sheepdog factory status`). You step in when the factory opens a gated ask (including an override), hits a coordinator stuck on a terminal prompt, or pauses on a stopping alert such as exhausted quota, a full disk, a macOS folder-access prompt, or a signed-out coordinator.

## Requirements

The factory uses **herdr-core** (`hc`), not `herdr-tui`. Before starting a project, run:

```sh
sheepdog factory doctor ~/repo
```

Every check prints a one-line fix when it fails (`--no-canary` skips the short test prompt so it spends no tokens). `doctor` also checks for leftover terminals, worktrees, branches, and scoped Codex files from past runs.

- **`herdr-core` (`hc`)** on `PATH` and its socket answering.
- **Project volume probe** inside `herdr-core`, catching macOS Files and Folders consent prompts and slow external disks before an unattended run starts.
- **Agent CLIs:** a new project defaults to **Claude Code (`claude`)** for every role (`coordinator`, `worker`, `reviewer`), so `claude` must be installed, signed in, and able to pass a canary start. To use `codex` or `agy` instead, set `models` in the project's `state.json` ([CONFIG.md](../CONFIG.md)).
- **Git 2.38 or later** (for `git merge-tree --write-tree`).
- **GitHub (`gh`)** signed in with push access (`WRITE`, `MAINTAIN`, or `ADMIN`) and matching `origin` fetch and push URLs pointing to the same non-empty `github.com` repository. If local `origin/HEAD` is set, it must match the repository's default branch (an unset `origin/HEAD` warns rather than fails).
- **A pull-request CI workflow** on the remote base branch under `.github/workflows/` with a `pull_request` or `pull_request_target` trigger (warns if every PR workflow filters with `paths` or `paths-ignore`).
- **No required approving or code-owner review** on the base branch, because the engine merges pull requests itself. Branch protection or a ruleset is reported but not required.
- **`codexbar`** (optional) for provider quota checks.

## Start, watch, and stop

Make sure the sheepdog daemon is running (`sheepdog start`), then start a factory project from your terminal:

```sh
sheepdog factory doctor ~/repo
sheepdog factory start ~/repo --goal "Add dark mode to the settings screen"
sheepdog factory status
sheepdog factory log -n 30
sheepdog factory stop
```

- `sheepdog factory start <folder> --goal "<sentence>"` runs the start checks first and refuses to start on any `FAIL`. Pass `--max-running N` (1–20, default 3) to set concurrent tasks at a project's first start (change it later in the settings panel on `/factory`); one extra slot beyond `N` is available for an urgent task or a task you just answered.
- `sheepdog factory status` queries the running daemon (including in-memory alerts) and prints each task's id, title, stage, last logged rule, age, and reason, plus any active alerts. When the daemon is stopped, it falls back to stored `state.json` and `log.jsonl`.
- `sheepdog factory log` prints each engine step from `log.jsonl`, followed by the project's first-look pass rate once any task has a reviewed round.
- `sheepdog factory stop` stops the project and every agent run it started, keeping the plan and stored state so you can start again later. Restarting with `sheepdog factory start` always requires `--goal`: pass the same goal to resume, or a new goal to have the coordinator update the plan.
- **By live voice:** when a voice session is live, saying *"stop the factory"* or *"start the factory"* stops or restarts your existing factory projects.

Coordinator, worker, and reviewer runs report back to the daemon through internal `sheepdog factory` commands using a per-run `SHEEPDOG_RUN_TOKEN`. See [commands.md](commands.md#factory-agent-commands) for the list.

## The `/factory` board

Open the web board directly on the computer running the daemon:

```text
http://127.0.0.1:7777/factory
```

Use your configured `web.port` if it is not `7777`. `/factory` answers only direct loopback requests on this computer (`403` from another device or through a reverse proxy; see [remote-access.md](remote-access.md#security)).

The board refreshes every 3 seconds and shows:

- **Tasks**, grouped in attention order: **needs your approval**, **waiting on you**, **blocked**, **waiting on CI**, **working**, **waiting on others**, **queued**, and a collapsed **done** list. Click any task row to open its detail view (latest rule and verdict, review rounds, active model, dependencies, and recent log lines).
- **Projects** in the sidebar, with running or stopped status and stage counts.
- **Asks for you**, showing open gated asks and override cards.
- **Agents**, listing up to one active run per task (the reviewer while reviewing, otherwise the worker; the coordinator is not listed here).

## Answer asks and overrides

Workers and reviewers send routine `decision` questions to the coordinator. Questions that need you — **gated asks** (`gate`, `release`, `override`, `paid-key`, `delete`, `install`, `reset`, and `rule`) — wait for the owner and require a verified **owner proof**.

You can answer an open owner ask in three ways:

1. **On `/factory` in a paired local browser (Mac).** Click an option button on the ask card. If the browser is not paired yet, clicking an option opens a short-code pairing box below the cards: open **Sheepdog.app** (Mac) in the menu bar, choose **Pair browser…** to get a one-time 5-minute `XXXX-XXXX` code, and enter it on `/factory` ([desktop.md](desktop.md#sheepdogapp)). No phone access is required. Once paired, your answer is sent automatically. You can rename or revoke the browser later on `/setup`. On Linux (or a Mac without `Sheepdog.app`), answer from the paired iPhone or iPad app or by live voice instead.
2. **On the card in the paired iPhone or iPad app.** Factory asks appear as question cards and arrive as push notifications. Factory notifications carry no lock-screen **Approve** or **Decline** buttons: tap the notification to open the card and choose an option ([iphone-app.md](iphone-app.md#notifications)).
3. **By live voice.** While a voice session is live, sheepdog announces open owner asks with their numbered options and recommendation. Say the option label, or *"option 1"* through *"option 3"* (*"first"* through *"third"*); bare numbers like *"1"* are not matched, and asks with 4 or more options require the exact label. If more than one ask is open (or an agent prompt is also waiting), include the task or project name with your answer.

Typed chat messages, agent runs, and unpaired local HTTP requests cannot answer a gated ask.

### Overrides

When you want to merge a ready task without waiting for review or CI, tell the coordinator in its terminal (for example, *"merge T1 now"*). The coordinator opens an **override** ask for you to confirm (only the coordinator can open one, and only for tasks that already have a ready report). The engine fills the card directly from live git and GitHub state: each task id, PR number, the reported commit, CI check state, and whether the worktree has unreported work. The options are **Yes, merge now** and **No, wait** (recommended).

An approved override applies only to that exact commit (or clean base merges ahead of it). It always needs your confirmation — it cannot be auto-approved by a rule — and cancels itself if CI checks become worse than what the card showed before the merge runs. Once the merge finishes or stops, sheepdog announces the result by voice and notification.

### Project settings

Number and choice settings live per project in `state.json` ([CONFIG.md](../CONFIG.md#factory-settings)). When a setting change is proposed from a paired browser session, the engine opens a gated **rule** ask that reads back the new and current values (*"This project: review rounds 3 (now 2). Save?"*) with **Save** (recommended) and **Keep current**. Answering **Save** with your owner proof writes the setting and takes effect on the next pass without restarting running agents.

## Quiet by default

While the factory runs, routine worker, reviewer, and coordinator transitions are silenced so they do not interrupt your voice session or phone notifications. Only owner asks, override results, and a coordinator stuck on a terminal prompt reach voice and phone pushes. Project alerts — such as exhausted quota, a full disk, a macOS folder-access prompt, a red base branch, stalled CI, or a signed-out or stopped coordinator — appear in `sheepdog factory status` and `sheepdog factory log` (stopping alerts pause new work until resolved and do not send voice or phone alerts).

## Further reading

- [safety.md](safety.md#factory) — owner proofs, role tokens, override rules, and start checks.
- [commands.md](commands.md#factory) — every `sheepdog factory` subcommand and flag.
- [privacy.md](privacy.md#what-is-stored-on-your-computer) — factory files stored under `~/.local/share/sheepdog/factory/`.
- [CONFIG.md](../CONFIG.md) — per-project factory settings in `state.json`.
