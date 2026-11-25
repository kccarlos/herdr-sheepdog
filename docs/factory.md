# factory

**Give a GitHub repository a goal and let sheepdog plan, build, review, test, and merge it while you step in only for decisions.**

The factory runs inside the sheepdog daemon. You give a repository a goal; a **coordinator** breaks it into small tasks, **workers** implement each task in its own git worktree, independent **reviewers** check each diff, GitHub CI runs on every pull request, and finished PRs squash-merge into your base branch. Routine chatter stays silent—as the **owner**, you are asked only when a decision or override needs you.

## What you need

- **A Mac (or Linux machine) running sheepdog on `herdr-core` (`hc`).** The factory uses `herdr-core`, not `herdr-tui`. On a Mac, [herdr-app and Sheepdog.app](desktop.md) let you pair your local browser to start projects, change settings, and answer asks right on `/factory`.
- **A local git repository (Git 2.38 or later) with a GitHub remote and push access.** Install the [GitHub CLI (`gh`)](https://cli.github.com) and sign in with an account that can push to the repo.
- **A pull-request CI workflow on the default branch** (under `.github/workflows/`), with **no required approving or code-owner review** on that branch—the factory merges passing PRs itself.
- **The coding CLIs you want to use, installed and signed in.** A new project defaults to Claude Code (`claude`) for the coordinator, workers, and reviewers; you can choose Codex (`codex`) or Antigravity CLI (`agy`) for any role when you start a project or later in settings.
- **`codexbar` (optional)** if you want the factory to pause before a provider quota window runs dry.

To check a folder from the terminal before starting, run:

```sh
sheepdog factory doctor ~/repo
```

Every failing check prints a one-line fix (`--no-canary` skips the short test prompt so it spends no tokens).

## Start a project from `/factory`

Make sure the daemon is running (`sheepdog start`), then open the factory page in a browser on the computer running sheepdog:

```text
http://127.0.0.1:7777/factory
```

Use your configured `web.port` if you changed it from `7777`. `/factory` opens only directly on this computer ([remote-access.md](remote-access.md#security)).

Click **New project** to set up your repository:

1. **Pick the folder** (or choose one under **folders sheepdog knows**) and write the **goal** in plain words.
2. **Choose models** if you want anything other than the Claude defaults, and set **max running** (how many tasks run at once; default `3`).
3. **Check this folder.** The **start checks** card verifies git, your chosen coding CLIs, GitHub access, your PR workflow, and branch review rules. A warning does not stop you, and every failure shows a copyable fix. If a failure is for a CLI you do not use, change the models above and click **Check again**.
4. **Click Start the project and its agents** once no check fails. On a Mac, the first time you start a project or answer an ask from `/factory`, the page asks for a one-time pairing code from **Sheepdog.app ▸ Pair browser…** in the menu bar ([desktop.md](desktop.md#sheepdogapp)).

<p align="center">
  <img src="../assets/readme/factory/new-project.png" alt="The New project form on /factory: folder picker, goal, per-role model lists, max running, and start checks with fixes" width="760">
</p>

You can also start a project from the terminal (`sheepdog factory start ~/repo --goal "Add dark mode to the settings screen"`), which runs the same start checks first. See [commands.md](commands.md#factory) for all CLI flags.

## Watching it

`/factory` refreshes automatically every few seconds.

### Several projects at once

When you have two or more projects, `/factory` opens the **projects** overview—one card per project, ordered so projects that need you or are actively running come first. Click any card to open its board, or **+ New project** to add another repository.

<p align="center">
  <img src="../assets/readme/factory/overview.png" alt="The /factory projects overview showing a running project with an open ask and a finished project" width="760">
</p>

### The task list and step bars

Inside a project, the **list** view groups tasks in attention order—**waiting on you**, **blocked**, **waiting on CI**, **working**, **waiting on others**, **queued**, and a collapsed **done** list. Each row shows a five-step progress bar (**planned → implementing → review → CI → merged**) and the active model. The sidebar shows open **asks for you** and currently running **agents** (one active worker or reviewer per task).

<p align="center">
  <img src="../assets/readme/factory/board-list.png" alt="The /factory task list view with attention-ordered groups, five-step progress bars, asks for you, and running agents" width="760">
</p>

### The task graph

Switch to **graph** to see how tasks depend on one another across layers. On a narrow window, the graph scrolls sideways (or you can switch back to **list**). Click any node to open that task.

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="../assets/readme/factory/graph-dark.png">
    <img src="../assets/readme/factory/graph-light.png" alt="The /factory task dependency graph showing twenty tasks across five layers and their states" width="760">
  </picture>
</p>

Every task state you will see on the graph and board:

| State | Meaning |
| --- | --- |
| **needs your approval** | Waiting on an owner gate you need to approve before it moves on. |
| **waiting on you** | Waiting on your answer to an open ask. |
| **blocked** | Paused or parked (for example waiting on the coordinator after a recap or failure). |
| **waiting on CI** | Reviews passed; waiting on GitHub CI checks and merge. |
| **working** | A worker is implementing (or fixing review findings), or a reviewer is checking the diff. |
| **waiting on others** | Depends on upstream tasks that are not done yet. |
| **queued** | Ready to run as soon as a concurrency slot opens. |
| **done** | Squash-merged into the base branch. |
| **closed** | Ended without merging (for example its PR was closed or nothing needed changing). |
| **dropped** | Removed from the plan by the coordinator without merging. |

### Inside a task

Click any task in the list or graph to see its five-step checklist, its **brief** and outcomes, the engine's current status (with an expandable **details** fold), its dependencies, and the log filtered to that task.

<p align="center">
  <img src="../assets/readme/factory/task-view.png" alt="A task detail view on /factory showing the steps checklist, brief, current review status, dependencies, and task log" width="760">
</p>

### The project log

At the bottom of the board, **log** records the story of the project—when you started it, what the coordinator planned, each PR opened, every review verdict, and each merge. Filter by a single task or tick **show routine checks** when you want the fuller trace.

<p align="center">
  <img src="../assets/readme/factory/recipes-log.png" alt="A finished project on /factory with its Reopen with a new goal button and full project log from start to merge" width="760">
</p>

From a terminal, `sheepdog factory status` prints every task's state plus any active alerts, and `sheepdog factory log -n 30` shows the recent steps.

## When it needs you

While the factory runs, routine worker, reviewer, and coordinator updates stay quiet so they do not interrupt your voice session or spam phone notifications. You hear from it only when:

- **An ask needs the owner** (such as a gate, delete, install, release, setting change, or merge override; routine design choices stay between workers and the coordinator).
- **An override finishes or stops.**
- **The coordinator is stuck** on a terminal prompt.

Whenever any project has an open ask, the **needs you** button in the top right of `/factory` opens a list across all projects and jumps straight to the ask card.

<p align="center">
  <img src="../assets/readme/factory/needs-you.png" alt="The needs you panel at the top of /factory listing an open ask across projects" width="760">
</p>

You can answer an open ask in three ways:

1. **On the card in `/factory` (Mac).** Click an option button on the card. If the browser is not paired yet, a pairing box opens below the card: choose **Pair browser…** in **Sheepdog.app**'s menu bar, enter the one-time 5-minute code, and your answer sends automatically. You can rename or revoke paired browsers later on `/setup`.
2. **On the card in the iPhone or iPad app.** Factory asks arrive as push notifications and appear as question cards. Tap the notification to open the card and choose an option—factory notifications deliberately have no lock-screen **Approve** or **Decline** buttons ([iphone-app.md](iphone-app.md#notifications)).
3. **By live voice.** While a voice session is live, sheepdog reads open asks with their options and recommendation. Say the option label, or *"option 1"* through *"option 3"* (*"first"* through *"third"*). Bare numbers like *"1"* are not matched, and asks with 4 or more options require the exact label. If more than one ask is open, name the task or project too.

On Linux (or a Mac without `Sheepdog.app`), answer from the paired iPhone or iPad app or by live voice. Typed chat messages and agents can never answer a gated ask.

### Merging early (overrides)

If you want to merge a ready task without waiting for review or CI, tell the coordinator in its terminal (for example, *"merge T1 now"*). The coordinator opens an **override** card filled directly from live git and GitHub state (task, PR, commit, CI state, and whether the worktree has unreported work) with **Yes, merge now** and **No, wait** (recommended). Confirming applies only to that exact commit and cancels itself if CI gets worse before the merge runs.

## Stop, resume, new goal, reopen

Under the project's goal on `/factory`, the project bar controls its lifecycle:

- **Stop** (confirmed with **Stop the project**) stops the project and every agent run it started while keeping the plan and history intact.
- **Resume** restarts a stopped project with the same goal.
- **New goal** sends a fresh goal to the coordinator so it updates the plan while keeping your folder and settings.
- **Reopen with a new goal** starts a new goal on a finished project (as shown on the `recipes` project above).

You can also do this by live voice—say *"stop the factory"* or *"start the factory"*—or from a terminal with `sheepdog factory stop` and `sheepdog factory start` (passing the same `--goal` to resume or a new `--goal` to change direction; see [commands.md](commands.md#factory)).

## Settings and models

Scroll down on a project's board to open **settings**. Everything here applies to **this project only**.

<p align="center">
  <img src="../assets/readme/factory/settings.png" alt="The per-project settings card on /factory showing role model lists, max running, and the all settings fold" width="680">
</p>

- **Models per role:** Click **Edit** on **coordinator models**, **worker models**, **reviewer models**, **retro models** (used when a task's reviews keep failing), or **plan reviewer models** (checks new plans before workers start) to choose an ordered list of CLI (`claude`, `codex`, `agy`), model, and effort (`low`, `medium`, `high`). The factory uses the first candidate it can start; reviewers automatically pick a different model from the worker when one is available.
- **Max running:** Sets how many tasks run concurrently (`1–20`, default `3`; one extra slot is kept for an urgent task or one you just answered).
- **All settings:** Expand **all settings** for review counts, timeouts, size caps, and quota floors—every key, default, and range is documented in [CONFIG.md](../CONFIG.md#factory-settings).

When you change a setting on `/factory`, sheepdog reads back the new and current values on a confirmation card (**Save** / **Keep current**). Confirming **Save** applies the change on the next pass without restarting running agents.

## Limits and safety

- **It pushes branches and merges to your repo.** Unlike interactive agents you supervise turn by turn, factory workers run without per-command approval prompts so they can work unattended. Once a task passes independent review and GitHub CI, the factory squash-merges its PR into your base branch.
- **It uses your model accounts' quota.** Every coordinator, worker, and reviewer run spends tokens on the coding CLI accounts you chose. Install `codexbar` if you want the factory to hand off and pause before a quota window hits its floor.
- **What keeps it safe.** Each worker edits in an isolated git worktree; reviewers run read-only and a dirty worktree voids the review; GitHub CI checks every PR; overrides are pinned to a single commit and never auto-approve; and gated asks can only be answered by you from a paired device or live voice.

Read [safety.md](safety.md#factory) for the full safety rules and [privacy.md](privacy.md) for what stays on your computer and what goes to your model providers and GitHub.

## Troubleshooting

Most problems show up in the **start checks** card on `/factory` or in `sheepdog factory doctor ~/repo`. Common failures and fixes:

| You see | Fix |
| --- | --- |
| `herdr-core` unavailable / socket does not answer | Start herdr-core (`hc server start`) or open **herdr-app**, then check again. |
| `git version` older than 2.38 | Upgrade Git (`brew install git` on macOS) so `git merge-tree --write-tree` is available. |
| `start fetch` warning | Run `git fetch origin` in the repo (sign in to git if prompted), then check again. |
| `start claude`, `start codex`, or `start agy` failed | Install and sign in to that CLI (`claude auth login`, `codex login`, or run `agy` to sign in)—or switch that role to a CLI you do have in **models**. |
| `gh push access` failed | Install the [GitHub CLI](https://cli.github.com), run `gh auth login`, and use an account with push access to the repository. |
| `github remote` failed | Add a `github.com` `origin` remote, push at least one commit so the repo is not empty, and run `git remote set-head origin -a` if `origin/HEAD` does not match GitHub's default branch. |
| `pr workflow` failed (or warned about path filters) | Add a workflow under `.github/workflows/` with `on: pull_request` covering your base branch (without `paths`/`paths-ignore` filtering out PRs), commit it to the base branch, and push. |
| `review rule` failed | In GitHub's branch protection rule or ruleset for the base branch, set required approving reviews to `0` and turn off code-owner review so the factory can merge passing PRs. |
| Project paused with no new tasks starting | Run `sheepdog factory status` to check for active alerts (exhausted quota, full disk, a macOS Files and Folders consent prompt, or a signed-out coordinator). |

## Further reading

- [commands.md](commands.md#factory) — every `sheepdog factory` command and flag.
- [CONFIG.md](../CONFIG.md#factory-settings) — all per-project factory settings and stored files.
- [safety.md](safety.md#factory) — owner proofs, role separation, and override rules.
- [privacy.md](privacy.md#what-is-stored-on-your-computer) — what the factory stores locally and sends to providers.
- [dev/engine-v2/README.md](dev/engine-v2/README.md) — developer docs for how the factory engine works inside.
