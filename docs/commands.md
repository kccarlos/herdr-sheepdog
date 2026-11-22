# commands

**Every sheepdog command, grouped by what you want to do.**

`sheepdog --help` lists these; `--help` after a command (`sheepdog doctor --help`) gives its flags. Developer commands live under `sheepdog dev` and are covered in
[dev/development.md](dev/development.md).

## Set up

| Command | What it does |
|---|---|
| `sheepdog setup` | The browser wizard: key, checks, start at login, phone, talk. Starts the daemon if needed. Run it again any time. `--terminal` asks in the terminal; `--no-open` prints the URL only. |
| `sheepdog init` | The Models step in the terminal (also `sheepdog setup --terminal`): starting point, voice, orchestrator, keys checked live, a summary, then saved like the setup page. `--non-interactive` for scripts: [flags](#flags-of-sheepdog-init). |
| `sheepdog doctor` | One line per check with its fix: herdr, the herdr-app panel and `Sheepdog.app` on macOS, login service `PATH`, `HERDR_APP_HOME` / `HC_SESSION` shell overrides, every key, the orchestrator's tool calling, the web port, bind, project folders, push, config and env file permissions, and a warning when your keys live in a `./.env` instead of `~/.config/sheepdog/.env`. Exit 1 on any FAIL. `--no-probe` spends no tokens. |
| `sheepdog doctor --report` | A redacted diagnostic bundle for a bug report. `--out FILE` writes it (mode 0600); `--lines N` sets the log lines (default 200). |
| `sheepdog pair` | The QR code the iPhone app scans. `--compact` for a small window, `--plain` for no colour. |
| `sheepdog demo` | Three scripted agents, no herdr needed (needs the Gemini key: the one `sheepdog setup` saved, or `GEMINI_API_KEY`). `--port N`, `--no-open`. |
| `sheepdog factory doctor` | Check unattended factory prerequisites for `[folder]` (default `.`) with one-line fixes: herdr-core (`hc`), a timed probe of the project volume inside herdr-core (naming any waiting macOS consent prompt), at least one logged-in agent CLI (`claude`, `codex` or `agy`) plus a canary start, `gh` login and matching `origin` fetch and push URLs pointing to the same GitHub repo, and `codexbar`; then the start checks, each with a fix: `git` 2.38 or later, the CLIs the project uses (`start claude`, plus `start codex` when `second_reviewer` is `codex`), `gh push access` to the repo (`gh repo view` proves sign-in), a non-empty GitHub remote whose default branch is the base, a workflow whose `on:` trigger runs on `pull_request` on the remote base branch (after a `git fetch`, warning when every PR workflow uses `paths` or `paths-ignore`), no required approving review on the base (the engine merges itself), and branch protection or a ruleset on the base (reported, never required). The factory targets the GitHub repo from `origin` using `--repo` or explicit API paths. Exit 1 on any FAIL. `--no-canary` spends no tokens; `--folder PATH`, `--config PATH`, `--hc-socket PATH`. |
| `sheepdog factory start` | Start the factory coordinator for `<folder>` with `--goal "<sentence>"` (needs the running daemon and `herdr-core`). It first runs the start checks of `sheepdog factory doctor` and refuses on any FAIL, printing each failing line with its fix. `--max-running N` (1–20, default 3), `--config PATH`. |
| `sheepdog factory status` | Print a factory project's tasks, one line each: id, short title, stage, the last rule the engine logged for it with its age, and the task's current why line matching `/factory` (marked `(last logged …)` when the stage has since changed, or `no logged row` when none was logged). Reads stored state and the log only; works when the daemon is stopped. `[folder]` (repo root or any subfolder) is optional when only one project is started or stored; `--config PATH`. |
| `sheepdog factory log` | Print a factory project's log, one line per engine step: time, task, the rule that matched, what was asked, the decision and why (verdict lines include each round's self-check misses), followed by the project's first-look pass rate once any task has a reviewed round. `[folder]` is optional when only one project is started; `-n N` shows the last N lines; `--config PATH`. |
| `sheepdog factory plan apply` | Run by the factory coordinator inside its run: apply the JSON plan in `<file>` (`{"tasks": [{"id", "title", "brief", "for_whom", "if_skipped", "smallest", "not_doing", "outcomes": [{"text", "proved_by"}], "after", "priority", "estimate_lines"}]}`). `for_whom`, `if_skipped`, `smallest`, `not_doing`, at least one outcome with `proved_by` (`cmd: <shell command>`, `test: <name or path>`, or `manual: <artifact>`), and `estimate_lines` (the lines the task should add plus remove) are required, `priority` is optional (`"urgent"` or omitted), `title` must be a Conventional Commit subject (`type(scope)?: subject`), and a task over the size limit (400 by default) is refused: split it first. A task whose changed lines pass twice its estimate stops at a size recap. Sends the run's `SHEEPDOG_RUN_TOKEN`; the daemon refuses it from any other terminal, refuses a second plan (`plan_exists`) and lists every failing check (`plan_invalid`). |
| `sheepdog factory plan amend` | Run by the factory coordinator inside its run: change existing tasks' `title`, `brief`, `outcomes` or `estimate_lines` (which may lower a started task's size cap when no size recap is open, never raising it; the new estimate applies to the next attempt), or add new tasks, from the JSON in `<file>` (`{"why": "...", "tasks": [...]}`). An ended task is not changed; a changed brief or outcomes on a started task makes earlier review rounds stop counting, so the same commit is reviewed again, and a live worker is told what changed. Refused without a why (`why_missing`) or with every failing check (`plan_invalid`). |
| `sheepdog factory recap decide` | Run by the factory coordinator inside its run: decide `<task>`'s open recap with `<choice>` and `--why "<why>"`. `split` (with one or more `--child '<json>'` tasks) and `drop` end the task; `new` starts a new attempt and is allowed only after `plan amend` changed the brief or outcomes; `continue`, offered only by a size recap and once per task by default, lets the worker go on with a size cap of its changed lines now plus 60. A choice the recap does not allow is refused (`not_allowed`). |
| `sheepdog factory task` | Run by the factory coordinator inside its run: `park`, `unpark`, `drop`, `close` or `send-back` `<task>` with `--why "<why>"`. A parked task keeps no run alive; a dropped task is cleaned up and its dependants keep waiting on it; `close` is allowed only after the worker reported nothing to change, and is refused (`nothing_new`) if it committed since; `send-back` gives the task back to its worker with the why until it reports ready again. Refused for an ended task (`already_closed`) or without a why (`why_missing`). |
| `sheepdog factory task retry` | Run by the factory coordinator inside its run: retry `<task>` with `--why "<why>"` when it is at N5 (resets failure, restart, reviewer-retry and merge-try counts and allows an owner-quit run to start again) or N4 (closes the closed-PR attempt so a new attempt starts on a new branch with a new PR); refused when not at N4/N5 (`nothing_to_retry`) or on an ended task (`already_closed`). |
| `sheepdog factory task say` | Run by the factory coordinator inside its run: send `--why "<message>"` to `<task>`'s worker as a held message (delivered immediately when idle or at next idle when working); refused with no open attempt (`no_worker`) or on an ended task (`already_closed`). |
| `sheepdog factory report ready` | Run by a factory worker inside its task worktree: report the worktree's `HEAD` commit ready. Pass `--proof "<outcome>=<command>"` for each command/test outcome, `--artifact "<outcome>=<path>"` for each manual outcome, `--touched "<places>"` (callers, tests, fixtures, config), and `--skipped "<what was left out or not checked>"` (empty `--skipped ""` means nothing skipped; both are required with proofs, cleaned and capped at 500 characters). The client runs each proof command in the worktree (5-minute limit each), saves the combined output under the worktree's `.git` directory, checks each artifact exists, and sends the results with `SHEEPDOG_RUN_TOKEN`. The daemon refuses it from any other role (`kind_not_allowed`), with uncommitted changes (`dirty_tree`), with no new commit (`nothing_new`), when the attempt head moved ahead (`pull_first`), when any outcome lacks a proof (`outcome_missing`), or when any proof exited non-zero (`proof_failed`). `--reject "<why>"` rejects every finding of the latest review round; `--nothing-to-change --why "<why>"` reports that the task needs no change (refused with `why_missing` without a reason, or `nothing_new` when the worktree has commits over the base), and the coordinator is woken once to close it or send it back. |
| `sheepdog factory report ask` | Run by a factory worker or reviewer inside its run: ask the coordinator a `decision` question (`"<question>" --option "<choice>" --option "<choice>" --recommend "<choice>"`). Requires 2 to 9 distinct single-line options, a recommendation among them, and a single-line question of 1 to 300 characters; wakes the coordinator once. |
| `sheepdog factory ask` | Run by the factory coordinator inside its run: open an ask (`--kind <kind> [--task <task>] "<question>" --option "<choice>" --option "<choice>" --recommend "<choice>"`). Gated kinds (`gate`, `release`, `paid-key`, `delete`, `install`, `reset`, `rule`) go to the owner; workers and reviewers are refused (`kind_not_allowed`). |
| `sheepdog factory answer` | Run by the factory coordinator inside its run: answer an open `decision` ask (`<ask> "<option>" [--why "<why>"]`) and deliver the answer to the worker (or hold it until a replacement worker starts). Gated asks require the owner's proof (`proof_required`). |
| `sheepdog factory stop` | Stop a started factory project (`[folder]`, optional when only one is running) and its runs. `--config PATH`. |

## Run

| Command | What it does |
|---|---|
| `sheepdog start` | Start the daemon in the background (through the login service if installed). Does nothing if it already runs. |
| `sheepdog stop` | Stop it. With the login service, it stays stopped until the next login or `sheepdog start`. |
| `sheepdog restart` | Stop, then start; with the login service, one restart through launchd or systemd (nothing is unloaded). Reads the env file the last run loaded (not the `./.env` of the directory you are in); `--env-file PATH` overrides it. |
| `sheepdog status` | Running or not: pid, version, URL, models, herdr, phones, last error, and which env file the keys came from (with a warning when it is not `~/.config/sheepdog/.env`). `--json` for scripts. Exit 0 running, 3 not running, 1 error. |
| `sheepdog logs` | The runtime log. `-f` follows, `-n N` sets the lines (default 100). |
| `sheepdog open` | Open the voice page; `sheepdog open setup` and `sheepdog open debug` open the others. Over SSH it opens nothing and prints the `ssh -N -L` line that reaches the page. |
| `sheepdog serve` | Run the daemon in this terminal; Ctrl-C stops it. |

## Manage

| Command | What it does |
|---|---|
| `sheepdog update` | Check for a new release now; if there is one, install it the way you installed sheepdog (installer, Homebrew, herdr plugin; on a Mac also Sheepdog.app when Homebrew installed it) and restart the running daemon. Asks nothing. A source checkout gets the steps instead. The daemon also checks once a day and shows the result in `sheepdog status` ([quick-start.md](quick-start.md#update)). |
| `sheepdog uninstall` | Stop, remove the login service, the `~/.local/bin/sheepdog` link, and unchanged `herdr-app` panel/layout/shortcut files (restoring your previous layout if `sheepdog` was active), ask before deleting config and data, then print the herdr-tui command that finishes the job. `--dry-run` previews every step without changing anything; `--yes` deletes without asking; `--keep-data` keeps config and data. |
| `sheepdog service install` | Start at login (launchd on macOS, `systemd --user` on Linux). Also `uninstall`, `start`, `status`, `logs`; `install --dry-run` prints the unit. |
| `sheepdog data clear` | Delete the event log and transcripts (`--events`, the default) or everything under the data directory (`--all`). Asks first; `--yes` skips. Stop the daemon first. |
| `sheepdog redact-logs` | Mask API keys, tokens and private keys in existing `brain/*.jsonl` and `*.requests.jsonl` logs atomically, keeping each JSON line valid. `--config PATH`, `--env-file PATH`. Stop the daemon first. |
| `sheepdog config show` | Print the effective config, secrets redacted. `sheepdog config init` writes a commented `config.toml`. Keys: [CONFIG.md](../CONFIG.md). |
| `sheepdog version` | Version, commit and build date. |

## [herdr-tui](https://github.com/herdrdev/herdr) plugin actions

The plugin adds four actions to herdr-tui (`herdr plugin action list --plugin kccarlos.sheepdog`): **Set up sheepdog**, **Pair your phone**, **Check setup** (`sheepdog doctor`) and **Logs** (`sheepdog logs -f`).

## Flags of `sheepdog init`

Non-interactive (`--non-interactive`) asks nothing: each choice is a flag, each
key comes from the environment or is kept from the env file, and with no choice
at all it is the Free quick start. Every key is still checked live (unless
`--skip-validate`), and nothing is written if one fails.

| Flag | Meaning |
|---|---|
| `--card ID` | Starting point: `recommended` (DeepSeek), `opus` (Claude Opus 5.5), `free` or `custom`; `cost` is an older name for `recommended`. |
| `--voice ID` | `gemini` (AI Studio), `vertex` or `openai` (GPT-Live). |
| `--voice-model ID`, `--voice-name NAME` | The voice's Live model and voice; the configured ones by default. |
| `--orchestrator ID` | `anthropic`, `gemini`, `deepseek`, `vertex`, `openrouter`, `openai`, `ollama`, `lmstudio` or `other` (`compat` is the same). |
| `--model ID` | The orchestrator's model (`--compat-model` is the same). A local preset takes the first model that calls tools. |
| `--vertex-project ID`, `--vertex-location LOC` | Vertex AI (also `VERTEX_PROJECT`); the location defaults to `us-central1`. |
| `--reuse-openai-key` | The OpenAI orchestrator uses the voice's `OPENAI_API_KEY`. |
| `--test-tools` | Also run the tool-calling test (a few tokens; a paid endpoint may bill them). |
| `--compat-name`, `--compat-base-url`, `--compat-key-env` | For `other`: the provider name, the API root, and the key's variable. |
| `--config PATH`, `--env-file PATH`, `--skip-validate` | As in `sheepdog doctor`; the last makes no API call. |

Keys are read from `GEMINI_API_KEY`, `ANTHROPIC_API_KEY`, `DEEPSEEK_API_KEY`,
`OPENAI_API_KEY` (GPT-Live), `VERTEX_API_KEY`, `OPENROUTER_API_KEY`,
`OPENAI_ORCH_API_KEY` and the `--compat-key-env` variable.
