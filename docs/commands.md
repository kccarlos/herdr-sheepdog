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

## Run

| Command | What it does |
|---|---|
| `sheepdog start` | Start the daemon in the background (through the login service if installed). Does nothing if it already runs. |
| `sheepdog stop` | Stop it. With the login service, it stays stopped until the next login or `sheepdog start`. |
| `sheepdog restart` | Stop, then start; with the login service, one restart through launchd or systemd (nothing is unloaded). Reads the env file the last run loaded (not the `./.env` of the directory you are in); `--env-file PATH` overrides it. |
| `sheepdog status` | Running or not: pid, version, URL, models, herdr, phones, last error, and which env file the keys came from (with a warning when it is not `~/.config/sheepdog/.env`). `--json` for scripts. Exit 0 running, 3 not running, 1 error. |
| `sheepdog logs` | The runtime log. `-f` follows, `-n N` sets the lines (default 100). |
| `sheepdog open` | Open the voice page; `sheepdog open setup` and `sheepdog open debug` open the others. Over SSH it opens nothing and prints the `ssh -N -L` line that reaches the page. The factory board is served directly at `http://127.0.0.1:7777/factory` (loopback only). |
| `sheepdog serve` | Run the daemon in this terminal; Ctrl-C stops it. |

## Factory

Run and inspect the unattended factory over `herdr-core` ([factory.md](factory.md)). The factory web board is served at `http://127.0.0.1:7777/factory` (loopback only; use your `web.port` if changed).

| Command | What it does |
|---|---|
| `sheepdog factory doctor` | Check unattended factory prerequisites for `[folder]` (default `.`) with one-line fixes: `herdr-core`, a timed project-volume probe, logged-in agent CLIs plus a canary start, `codexbar`, leftover sweep checks (terminals, worktrees, branches, local base, scoped Codex home), and all start checks ([factory.md](factory.md#what-you-need)). Exit 1 on any FAIL. `--no-canary` spends no tokens; `--folder PATH`, `--config PATH`, `--hc-socket PATH`. |
| `sheepdog factory start` | Start the factory coordinator for `<folder>` with `--goal "<sentence>"` (needs the running daemon and `herdr-core`). Runs the start checks first and refuses on any FAIL, printing each failing line with its fix. `--max-running N` (1–20, default 3, at a project's first start), `--config PATH`. |
| `sheepdog factory status` | Print a factory project's tasks (id, title, stage, last logged rule, age, and why line matching `/factory`) plus any active project alerts or outages, even with zero tasks. Queries the running daemon (including in-memory alerts), or falls back to stored `state.json` and `log.jsonl` when stopped. `[folder]` (repo root or any subfolder) is optional when only one project is started or stored; `--config PATH`. |
| `sheepdog factory log` | Print a factory project's step log (`log.jsonl`) and its first-look pass rate once any task has a reviewed round. `[folder]` is optional when only one project is started or stored; `-n N` shows the last N lines; `--config PATH`. |
| `sheepdog factory stop` | Stop a started factory project (`[folder]`, optional when only one is running) and its runs. `--config PATH`. |

### Factory agent commands

These subcommands are called only by factory coordinator, worker, and reviewer runs inside `herdr-core`. Each run authenticates with its per-run `SHEEPDOG_RUN_TOKEN`; running them from your own terminal is refused.

| Command | Role | What it does |
|---|---|---|
| `sheepdog factory plan apply` | Coordinator | Apply the initial JSON plan from `<file>` (Conventional Commit titles, scope fields, verifiable outcomes, line estimates, and optional `kind`, `gate`, `after`, `priority`, and model `request` overrides). Refused if a plan already exists or any check fails. |
| `sheepdog factory plan amend` | Coordinator | Add tasks or update existing open tasks (`title`, `brief`, `outcomes`, `estimate_lines`, `after`, `priority`, or `request`) from `<file>` with a reason. Changing a started task's brief or outcomes resets counted review rounds and notifies a live worker. |
| `sheepdog factory recap decide` | Coordinator | Resolve an open recap for `<task>` with `<choice>` and `--why "<why>"`: `split` (with `--child '<json>'`), `drop`, `new` (only after `plan amend` changed brief or outcomes), or `continue` (offered by size, CI, and time recaps, once per task by default; on a size recap, sets the cap to current changed lines plus 60). |
| `sheepdog factory task` | Coordinator | Run `park`, `unpark`, `drop`, `close`, or `send-back` on `<task>` with `--why "<why>"`. `close` requires a prior nothing-to-change report with no new commits. |
| `sheepdog factory task retry` | Coordinator | Retry `<task>` with `--why "<why>"` when it is stuck on exhausted runs or failures, a closed or wrong-base PR (starts a fresh attempt and PR), or a diverged or unreadable branch after a wake. |
| `sheepdog factory task say` | Coordinator | Send `--why "<message>"` to `<task>`'s worker (delivered now if idle, or held until next idle). |
| `sheepdog factory report ready` | Worker | Run proof commands (`--proof`), check artifacts (`--artifact`), save combined proof logs under the worktree's `.git` dir, and report `HEAD` ready with `--touched` and `--skipped`. Requires a clean worktree and new commit. Also supports `--reject "<why>"` and `--nothing-to-change --why "<why>"`. |
| `sheepdog factory report review` | Reviewer | Submit a `pass` or `changes` verdict with `--code`, `--head`, `--self-check-misses`, `--deletable`, and optional `--class`, `--not-run`, and `--findings <path>` (outside the worktree). A dirty worktree voids the round and stops the reviewer. |
| `sheepdog factory report ask` | Worker / Reviewer | Send a `decision` question (2–9 options and a `--recommend` pick) to the coordinator and wake it. |
| `sheepdog factory ask` | Coordinator | Open a structured ask (`--kind`), or open an engine-filled override card (`--kind override --task <task> [...]`). Gated kinds go to the owner. |
| `sheepdog factory answer` | Coordinator | Answer an open `decision` ask (`<ask> "<option>" [--why "<why>"]`) and deliver it to the asking worker (or hold it for a replacement) or live reviewer (cancelling the ask if the reviewer already ended). Gated asks require an owner proof. |

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
