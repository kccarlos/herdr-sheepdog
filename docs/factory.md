# factory

**Run coding agents unattended from a goal to merged pull requests, while you stay in charge of gated asks and overrides.**

The factory engine runs inside the sheepdog daemon. You give it a GitHub repository and a goal. A **coordinator** plans the work into small tasks, **workers** implement each task in its own git worktree, fresh **reviewers** check each diff, and the engine waits for GitHub CI before squash-merging into the base branch.

You watch progress at `http://127.0.0.1:7777/factory` (or `sheepdog factory status`) and step in only when the factory opens a gated ask, asks for an override, or hits a blocked coordinator dialog.

## Requirements

The factory uses **herdr-core** (`hc`), not `herdr-tui`. Before starting a project, run:

```sh
sheepdog factory doctor ~/repo
```

Every check prints a one-line fix when it fails (`--no-canary` skips the short test prompt so it spends no tokens):

- **`herdr-core` (`hc`)** on `PATH` and its socket answering.
- **Project volume probe** inside `herdr-core`, catching macOS Files and Folders consent prompts and slow external disks before an unattended run starts.
- **Agent CLIs:** at least one logged-in coding CLI (`claude`, `codex`, or `agy`) that passes its canary start, plus usable CLIs for the project's roles (`coordinator`, `worker`, `reviewer`).
- **Git 2.38 or later** (for `git merge-tree --write-tree`).
- **GitHub (`gh`)** signed in with push access (`WRITE`, `MAINTAIN`, or `ADMIN`) and matching `origin` fetch and push URLs pointing to the same non-empty GitHub repository whose default branch matches `origin/HEAD`.
- **A pull-request CI workflow** on the remote base branch under `.github/workflows/` with an `on: pull_request` trigger (warns if every PR workflow filters with `paths` or `paths-ignore`).
- **No required approving review** on the base branch, because the engine merges pull requests itself. Branch protection or a ruleset is reported but not required.
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

- `sheepdog factory start <folder> --goal "<sentence>"` runs the start checks first and refuses to start on any `FAIL`. Pass `--max-running N` (1–20, default 3) to set concurrent tasks; one extra slot is reserved for urgent tasks.
- `sheepdog factory status` queries the running daemon (including in-memory alerts) and prints each task's id, title, stage, last logged rule, age, and reason, plus any active alerts. When the daemon is stopped, it falls back to stored `state.json` and `log.jsonl`.
- `sheepdog factory log` prints each engine step from `log.jsonl`, followed by the project's first-look pass rate once any task has a reviewed round.
- `sheepdog factory stop` stops the project and every agent run it started, keeping the plan and stored state so you can start again later.
- **By live voice:** when a voice session is live, saying *"stop the factory"* or *"start the factory"* stops or restarts your existing factory projects.

Coordinator, worker, and reviewer runs call their own `sheepdog factory` verbs (`plan apply`, `plan amend`, `recap decide`, `task`, `report ready`, `report review`, `report ask`, `ask`, `answer`) using a per-run `SHEEPDOG_RUN_TOKEN`. See [commands.md](commands.md#factory) for the full list.

## The `/factory` board

Open the web board directly on the computer running the daemon:

```text
http://127.0.0.1:7777/factory
```

Use your configured `web.port` if it is not `7777`. `/factory` answers only direct loopback requests on this computer (`403` from another device or through a reverse proxy; see [remote-access.md](remote-access.md#security)).

The board refreshes every 3 seconds and shows:

- **Tasks**, grouped in attention order: **needs your approval** (`owner_gate`), **waiting on you** (`owner_answer`), **blocked** (`blocked_or_failed`), **waiting on CI** (`ci`), **working**, **waiting on others** (`waiting_on_dep`), **queued**, and a collapsed **done** list. Click any task row to open its detail view (latest rule and verdict, review rounds, active model, dependencies, and recent log lines).
- **Projects** in the sidebar, with running or stopped status and stage counts.
- **Asks for you**, showing open gated asks and override cards.
- **Agents**, listing active worker and reviewer runs.

## Answer asks and overrides

Workers and reviewers send routine `decision` questions to the coordinator (`sheepdog factory report ask`). Questions that need you — **gated asks** (`gate`, `release`, `override`, `paid-key`, `delete`, `install`, `reset`, and `rule`) — are held for the owner and require a verified **answer proof** (`AnswerProof`).

You can answer an open owner ask in three ways:

1. **On `/factory` in a paired local browser.** Click an option button on the ask card. If the browser is not paired yet, the card shows a short-code pairing box: open **Sheepdog.app** in the Mac menu bar, choose **Pair browser…** to get a one-time 5-minute `XXXX-XXXX` code, and enter it on `/factory` ([desktop.md](desktop.md#sheepdogapp)). No phone access is required. You can rename or revoke the browser later on `/setup`.
2. **On the card in the paired iPhone or iPad app.** Factory asks appear as question cards and arrive as push notifications. Factory notifications carry no lock-screen **Approve** or **Decline** buttons: tap the notification to open the card and choose an option ([iphone-app.md](iphone-app.md#notifications)).
3. **By live voice.** While a voice session is live, sheepdog announces open owner asks with their numbered options and recommendation. Say the option label or its number. If more than one ask is open (or an agent prompt is also waiting), include the task or project name with your answer.

Typed chat messages, agent runs, and unpaired local HTTP requests cannot answer a gated ask.

### Overrides

When you want to merge a task without waiting for review or CI, the coordinator opens an **override** ask (`sheepdog factory ask --kind override --task <task>`). The engine fills the card directly from live git and GitHub state: each task id, PR number, commit `H`, CI check state, and whether the worktree has unreported work. The options are **Yes, merge now** and **No, wait** (recommended).

An approved override applies only to commit `H` (or clean base merges ahead of it), can never be delegated to a standing rule, and cancels itself if CI checks become worse than what the card showed before the merge runs. Once the merge finishes or stops, sheepdog announces the result by voice and notification.

## Quiet by default

While the factory runs, worker and reviewer transitions are silenced so they do not interrupt your voice session or phone notifications. Only owner asks, override results, and blocked coordinator dialogs reach voice and phone pushes. Alerts (such as `no_pool`, `disk`, `consent_prompt`, `main_red`, `ci_stalled`, or `agents_down`) appear in `sheepdog factory status` and `sheepdog factory log`.

## Further reading

- [safety.md](safety.md#factory) — `AnswerProof`, role tokens, override rules, and start checks.
- [commands.md](commands.md#factory) — every `sheepdog factory` subcommand and flag.
- [privacy.md](privacy.md#what-is-stored-on-your-computer) — factory files stored under `~/.local/share/sheepdog/factory/`.
- [CONFIG.md](../CONFIG.md) — per-project factory settings in `state.json`.
