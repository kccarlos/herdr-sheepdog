# commands

**Every sheepdog command, grouped by what you want to do.**

`sheepdog --help` lists these; `--help` after a command (`sheepdog doctor --help`) gives its flags. Developer commands live under `sheepdog dev` and are covered in
[dev/development.md](dev/development.md).

## Set up

| Command | What it does |
|---|---|
| `sheepdog setup` | The browser wizard: key, checks, start at login, phone, talk. Starts the daemon if needed. Run it again any time. `--terminal` asks in the terminal; `--no-open` prints the URL only. |
| `sheepdog init` | The same questions in the terminal (also `sheepdog setup --terminal`). `--non-interactive` reads keys from the environment; `--orchestrator gemini\|deepseek\|compat`. |
| `sheepdog doctor` | One line per check with its fix: herdr, every key, the orchestrator's tool calling, the web port, bind and token, push, file permissions. Exit 1 on any FAIL. `--no-probe` spends no tokens. |
| `sheepdog doctor --report` | A redacted diagnostic bundle for a bug report. `--out FILE` writes it (mode 0600); `--lines N` sets the log lines (default 200). |
| `sheepdog pair` | The QR code the iPhone app scans. `--compact` for a small window, `--plain` for no colour. |
| `sheepdog demo` | Three scripted agents, no herdr needed (needs the Gemini key: the one `sheepdog setup` saved, or `GEMINI_API_KEY`). `--port N`, `--no-open`. |

## Run

| Command | What it does |
|---|---|
| `sheepdog start` | Start the daemon in the background (through the login service if installed). Does nothing if it already runs. |
| `sheepdog stop` | Stop it. With the login service, it stays stopped until the next login or `sheepdog start`. |
| `sheepdog restart` | Stop, then start. |
| `sheepdog status` | Running or not: pid, version, URL, models, herdr, phones, last error. `--json` for scripts. Exit 0 running, 3 not running, 1 error. |
| `sheepdog logs` | The runtime log. `-f` follows, `-n N` sets the lines (default 100). |
| `sheepdog open` | Open the voice page; `sheepdog open setup` and `sheepdog open debug` open the others. Over SSH it opens nothing and prints the `ssh -N -L` line that reaches the page. |
| `sheepdog serve` | Run the daemon in this terminal; Ctrl-C stops it. |

## Manage

| Command | What it does |
|---|---|
| `sheepdog update` | Check for a new release and print how to upgrade for the way you installed it. Changes nothing. |
| `sheepdog uninstall` | Stop, remove the login service and the `~/.local/bin/sheepdog` link, ask before deleting config and data, then print the herdr command that finishes the job. `--yes` deletes without asking; `--keep-data` keeps everything. |
| `sheepdog service install` | Start at login (launchd on macOS, `systemd --user` on Linux). Also `uninstall`, `start`, `status`, `logs`; `install --dry-run` prints the unit. |
| `sheepdog data clear` | Delete the event log and transcripts (`--events`, the default) or everything under the data directory (`--all`). Asks first; `--yes` skips. Stop the daemon first. |
| `sheepdog config show` | Print the effective config, secrets redacted. `sheepdog config init` writes a commented `config.toml`. Keys: [CONFIG.md](../CONFIG.md). |
| `sheepdog version` | Version, commit and build date. |

## herdr plugin actions

The plugin adds four actions to herdr (`herdr plugin action list --plugin kccarlos.sheepdog`): **Set up sheepdog**, **Pair your phone**, **Check setup** (`sheepdog doctor`) and **Logs** (`sheepdog logs -f`).
