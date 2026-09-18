# Read-only CLI query tools (`internal/cli`, plan §3.5)

Verified 2026-09-17 on macOS with **codex-cli 0.154.0** and **Claude Code
2.1.275**, both on `PATH`. Each command below was run once in a scratch git
repository (`README.md`, `main.go`, `medium.txt` 4 KB, `big.log` 53 KB) and
`git status --short` was empty afterwards (no writes, no untracked files).
A second run asked each tool to *create* a file to confirm the refusal path.

Both tools are logged in through their own credentials (Codex via
`~/.codex/auth.json`, Claude Code via OAuth keychain), which is why sheepdog
must inherit the user's environment and **must not** pass `--bare` to Claude.

---

## 1. Codex — `codex exec`

### Verified command

```sh
codex exec \
  --sandbox read-only \
  --skip-git-repo-check \
  --ephemeral \
  --color never \
  -C "<agent workspace cwd>" \
  "<question>"
```

Add `-o <file>` (`--output-last-message`) to get only the final answer, or
`--json` for JSONL events. Stdin should be closed (`/dev/null`); when stdin
is a pipe Codex prints `Reading additional input from stdin...` and appends
it to the prompt.

| flag | why |
|---|---|
| `-s, --sandbox read-only` | OS sandbox: model-generated shell commands cannot write anywhere (`read-only \| workspace-write \| danger-full-access`). |
| `--skip-git-repo-check` | `exec` otherwise refuses to run outside a Git repository; agents' cwds are usually repos, but this keeps the tool usable for any workspace. |
| `--ephemeral` | no session files written to `~/.codex/sessions`. |
| `--color never` | plain text on stdout. |
| `-C, --cd <dir>` | working root for the agent; sheepdog passes the target agent's `cwd`. Prefer this over `cmd.Dir` so Codex's own repo detection agrees. |
| `-c model_reasoning_effort=low` | optional; the user config here has `model_reasoning_effort = "max"`, which made the simple question take 17–20 s. With `low` the refusal test took 4.9 s. Suggested default for sheepdog's `ask()`; make it part of the configurable command. |
| `--json` | JSONL on stdout: `thread.started`, `turn.started`, `item.started/completed` (`agent_message`, `command_execution`, …), `turn.completed{usage}`. Useful for the event log; the final `agent_message` item is the answer. |
| `-o, --output-last-message <file>` | writes just the final assistant message. |

Approvals: `codex exec` has no `--ask-for-approval` flag (only the interactive
CLI does, and 0.154 offers only `on-request | never`). In `exec` the effective
policy is printed as `approval: never` — commands that need escalation simply
fail inside the sandbox and the failure is returned to the model, so nothing
can prompt or hang. **Never** pass `--dangerously-bypass-approvals-and-sandbox`
or `--approve-for-me`.

Codex still runs its `SessionStart` hooks (`hook: SessionStart` in stderr).
The herdr integration hook is a no-op unless `HERDR_ENV=1` and `HERDR_PANE_ID`
are set — so **sheepdog must scrub `HERDR_*` variables from the child
environment**; if the daemon itself runs inside a herdr pane, an inherited
`HERDR_PANE_ID` would make the CLI report a bogus Codex session for that pane.

### Observed

- Question: "List the files in this directory and say which one is largest.
  Do not modify anything."
- Run time: **17.7 s** (text mode), **20.2 s** (`--json`), effort `max`.
  Refusal test with `-c model_reasoning_effort=low`: **4.9 s**.
- stdout size: 174 bytes (text mode) / 1 553 bytes (`--json`, 6 lines).
  stderr carries the banner (workdir, model, `approval: never`,
  `sandbox: read-only`, session id), the command transcript and
  `tokens used`. Exit status 0.
- Answer (text mode):

  ```
  Files in the directory:

  - `README.md` — 10 bytes
  - `main.go` — 29 bytes
  - `medium.txt` — 4,001 bytes
  - `big.log` — 53,337 bytes

  Largest: `big.log` at 53,337 bytes.
  ```

- Write attempt ("Create a file named pwned.txt …"): exit 0, answer
  `I cannot create \`pwned.txt\` because the filesystem is read-only.`,
  `git status` clean.
- Cost signal: `--json` `turn.completed.usage` =
  `{"input_tokens":24500,"cached_input_tokens":22016,"output_tokens":890,"reasoning_output_tokens":623}`.

## 2. Claude Code — `claude -p`

### Verified command

```sh
claude -p "<question>" \
  --tools "Read,Grep,Glob,WebSearch" \
  --allowedTools "Read,Grep,Glob,WebSearch" \
  --strict-mcp-config \
  --permission-mode dontAsk \
  --permission-prompts none \
  --no-session-persistence \
  --output-format json \
  --max-budget-usd 0.50
```

Run with `cmd.Dir = <agent workspace cwd>`; there is no `--cd` flag and the
file tools are confined to the working directory (plus `--add-dir`).
Stdin must be `/dev/null` (with `-p`, a piped stdin is read as additional prompt).

| flag | why |
|---|---|
| `-p, --print` | non-interactive, exit after the answer. |
| `--tools "Read,Grep,Glob,WebSearch"` | **removes** every other built-in tool (Bash, Edit, Write, …) from the session. This is the real read-only guarantee; `--allowedTools` alone only pre-approves. |
| `--allowedTools "Read,Grep,Glob,WebSearch"` | pre-approves the remaining tools so nothing prompts. |
| `--strict-mcp-config` | ignore the user's MCP servers. Without it the user's connectors (Claude Docs, deepwiki, GitHub, …) are still exposed and some can write; verified: with the flag the model reported its tool list as exactly `Glob, Grep, Read, WebSearch`. |
| `--permission-mode dontAsk` | anything not pre-approved is denied instead of prompting. Choices in 2.1.275: `acceptEdits, auto, bypassPermissions, manual, dontAsk, plan` — there is **no `default`** value (the plan's assumption). |
| `--permission-prompts none` | belt and braces: with `--print`, nobody answers prompts; anything that would prompt is denied automatically. |
| `--no-session-persistence` | no transcript written to `~/.claude/projects`. |
| `--output-format json` | one JSON object on stdout (below). `text` is the default; `stream-json` streams events. |
| `--max-budget-usd 0.50` | hard cost cap per invocation (the 60 s timeout is still enforced by sheepdog). |
| `--model sonnet` (optional) | cheaper/faster than the user's default Opus; leave configurable. |
| `--effort low` (optional) | effort level; not tested. |
| `--restricted` (optional) | stricter variant of `--tools`: strips code-running tools and WebFetch, ignores user/project settings, confines file tools to cwd. Not required when `--tools` is explicit; consider it if settings files are untrusted. |
| do **not** use `--bare` | it disables keychain/OAuth reads; the run failed with `Not logged in · Please run /login` (`is_error: true`, exit 1). |
| `--max-turns` | **does not exist** in 2.1.275 (plan assumed it). Bound the run with `--max-budget-usd` + sheepdog's timeout instead. |

### Observed

- Same question. Run time: **17.0 s** wall (`duration_api_ms` 15 381),
  7 turns (Glob + Read calls, since Bash is unavailable), cost **$0.43**
  with the user's default Opus model. stdout: 2 484 bytes of JSON, exit 0.
- Result JSON keys: `type:"result"`, `subtype:"success"`, `is_error`,
  `result` (the answer text), `session_id`, `num_turns`, `duration_ms`,
  `duration_api_ms`, `total_cost_usd`, `usage{input_tokens,output_tokens,…}`,
  `modelUsage`, `permission_denials` (array — should stay empty), `stop_reason`,
  `terminal_reason` (`completed` / `api_error`), `ttft_ms`, `uuid`, … Sheepdog
  should read `result` and treat `is_error:true` or non-zero exit as failure.
- Answer (`result`):

  ```
  Files in the repo (excluding `.git/`):

  | File | Approx. size |
  |---|---|
  | `big.log` | ~53 KB (one very long line, 53,337 chars) |
  | `medium.txt` | ~4 KB (one long line) |
  | `main.go` | ~30 bytes (3 lines) |
  | `README.md` | ~10 bytes (1 line) |

  **Largest: `big.log`**, at roughly 53 KB — more than 10× the next-largest file, `medium.txt`.

  Nothing was modified.
  ```

- Write attempt: exit 0, 4.2 s, `result` = "I cannot create the file because
  I have no file-writing or shell tool available in this session …",
  `permission_denials: []`, `git status` clean. (That run lacked
  `--strict-mcp-config` and the model listed the user's MCP tools as
  available — hence the flag above.)
- Tool inventory check with `--strict-mcp-config`: 2.8 s, `result` =
  `Glob, Grep, Read, WebSearch`.

## 3. Proposed config defaults

```toml
[cli]
# {cwd} and {question} are substituted by sheepdog; argv arrays, no shell.
codex_cmd  = ["codex", "exec", "--sandbox", "read-only", "--skip-git-repo-check",
              "--ephemeral", "--color", "never", "-c", "model_reasoning_effort=low",
              "-C", "{cwd}", "{question}"]
claude_cmd = ["claude", "-p", "{question}",
              "--tools", "Read,Grep,Glob,WebSearch",
              "--allowedTools", "Read,Grep,Glob,WebSearch",
              "--strict-mcp-config",
              "--permission-mode", "dontAsk", "--permission-prompts", "none",
              "--no-session-persistence", "--output-format", "json",
              "--max-budget-usd", "0.50"]
timeout_s     = 60      # kill the process group on expiry (plan §3.5)
output_cap_kb = 16
prefer        = "codex" # first detected wins; both tools took ~17 s on a trivial question at default effort
```

Runner notes for WP4:

- `exec.Command` with `Stdin = nil` (→ `/dev/null`), `Dir = cwd` (Claude) plus
  `-C cwd` (Codex), `Setpgid` and kill the whole group on timeout (both tools
  spawn children: `/bin/zsh -lc …` for Codex, `rg` for Claude).
- Strip `HERDR_*` from `cmd.Env`; keep `HOME`, `PATH`, and the tools' own
  auth env untouched. Node for Claude is installed via nvm; `claude` resolved on
  `PATH` in this shell, but a launchd-started daemon needs an explicit `PATH`
  containing `~/.nvm/versions/node/*/bin` and `~/.local/bin` (herdr).
- Parse: Codex → stdout text (or the last `agent_message` item with `--json`);
  Claude → `json.result`, error if `is_error`.
- Expect **15–20 s** per call with default reasoning settings and
  **$0.03–0.45** per Claude call; the plan's 60 s timeout is adequate, the
  `spoken_answer` step must add its own latency budget.

## 4. Deviations from sheepdog-v1-plan.md

- **§3.5 `claude -p "<q>" --allowedTools "Read,Grep,Glob,WebSearch"`** is not
  read-only by itself: `--allowedTools` only pre-approves; every other tool
  (Bash, Write, Edit, MCP) would still be available and would prompt/deny.
  The verified read-only form needs `--tools …` to remove the other built-ins
  and `--strict-mcp-config` to drop MCP servers, plus
  `--permission-mode dontAsk --permission-prompts none` so nothing can block.
- **`--permission-mode default`** does not exist as a value in 2.1.275
  (choices: `acceptEdits, auto, bypassPermissions, manual, dontAsk, plan`).
- **`--max-turns`** does not exist in 2.1.275; use `--max-budget-usd` and the
  external timeout.
- **`codex exec --sandbox read-only "<q>"`** works as written but needs
  `--skip-git-repo-check` for non-repo cwds and `--ephemeral` to avoid
  writing session files; there is no approval flag on `exec` (0.154 has only
  `on-request|never` on the interactive CLI).
- **Latency assumption:** a trivial question took ~17 s on both tools with
  the user's default (max-effort / Opus) settings — the `ask()` path should
  say "checking" immediately (already in the plan) and consider
  `model_reasoning_effort=low` / `--model sonnet` defaults.
- **New requirement:** scrub `HERDR_*` env vars before spawning either CLI
  (their herdr integration hooks would otherwise report a session for the
  inherited pane).
