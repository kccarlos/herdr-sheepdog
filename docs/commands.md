# commands

**Every sheepdog command, grouped by what you want to do.**

`sheepdog --help` lists these; `--help` after a command (`sheepdog doctor --help`) gives its flags. Developer commands live under `sheepdog dev` and are covered in
[dev/development.md](dev/development.md).

## Set up

| Command | What it does |
|---|---|
| `sheepdog setup` | The browser wizard: key, checks, start at login, phone, talk. Starts the daemon if needed. Run it again any time. `--terminal` asks in the terminal; `--no-open` prints the URL only. |
| `sheepdog init` | The Models step in the terminal (also `sheepdog setup --terminal`): starting point, voice, orchestrator, keys checked live, a summary, then saved like the setup page. `--non-interactive` for scripts: [flags](#flags-of-sheepdog-init). |
| `sheepdog doctor` | One line per check with its fix: herdr, every key, the orchestrator's tool calling, the web port, bind and token, push, file permissions, and a warning when your keys live in a `./.env` instead of `~/.config/sheepdog/.env`. Exit 1 on any FAIL. `--no-probe` spends no tokens. |
| `sheepdog doctor --report` | A redacted diagnostic bundle for a bug report. `--out FILE` writes it (mode 0600); `--lines N` sets the log lines (default 200). |
| `sheepdog pair` | The QR code the iPhone app scans. `--compact` for a small window, `--plain` for no colour. |
| `sheepdog demo` | Three scripted agents, no herdr needed (needs the Gemini key: the one `sheepdog setup` saved, or `GEMINI_API_KEY`). `--port N`, `--no-open`. |

## Run

| Command | What it does |
|---|---|
| `sheepdog start` | Start the daemon in the background (through the login service if installed). Does nothing if it already runs. |
| `sheepdog stop` | Stop it. With the login service, it stays stopped until the next login or `sheepdog start`. |
| `sheepdog restart` | Stop, then start. Reads the env file the last run loaded (not the `./.env` of the directory you are in); `--env-file PATH` overrides it. |
| `sheepdog status` | Running or not: pid, version, URL, models, herdr, phones, last error, and which env file the keys came from (with a warning when it is not `~/.config/sheepdog/.env`). `--json` for scripts. Exit 0 running, 3 not running, 1 error. |
| `sheepdog logs` | The runtime log. `-f` follows, `-n N` sets the lines (default 100). |
| `sheepdog open` | Open the voice page; `sheepdog open setup` and `sheepdog open debug` open the others. Over SSH it opens nothing and prints the `ssh -N -L` line that reaches the page. |
| `sheepdog serve` | Run the daemon in this terminal; Ctrl-C stops it. |

## Manage

| Command | What it does |
|---|---|
| `sheepdog update` | Check for a new release now and print how to upgrade for the way you installed it. Changes nothing. The daemon also checks once a day and shows the result in `sheepdog status` ([quick-start.md](quick-start.md#update)). |
| `sheepdog uninstall` | Stop, remove the login service and the `~/.local/bin/sheepdog` link, ask before deleting config and data, then print the herdr command that finishes the job. `--yes` deletes without asking; `--keep-data` keeps everything. |
| `sheepdog service install` | Start at login (launchd on macOS, `systemd --user` on Linux). Also `uninstall`, `start`, `status`, `logs`; `install --dry-run` prints the unit. |
| `sheepdog data clear` | Delete the event log and transcripts (`--events`, the default) or everything under the data directory (`--all`). Asks first; `--yes` skips. Stop the daemon first. |
| `sheepdog config show` | Print the effective config, secrets redacted. `sheepdog config init` writes a commented `config.toml`. Keys: [CONFIG.md](../CONFIG.md). |
| `sheepdog version` | Version, commit and build date. |

## herdr plugin actions

The plugin adds four actions to herdr (`herdr plugin action list --plugin kccarlos.sheepdog`): **Set up sheepdog**, **Pair your phone**, **Check setup** (`sheepdog doctor`) and **Logs** (`sheepdog logs -f`).

## Flags of `sheepdog init`

Non-interactive (`--non-interactive`) asks nothing: each choice is a flag, each
key comes from the environment or is kept from the env file, and with no choice
at all it is the Free quick start. Every key is still checked live (unless
`--skip-validate`), and nothing is written if one fails.

| Flag | Meaning |
|---|---|
| `--card ID` | Starting point: `recommended`, `cost`, `free` or `custom`. |
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
