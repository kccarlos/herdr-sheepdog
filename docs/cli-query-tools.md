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

## 2b. Sessions and resume (investigations)

Verified 2026-09-21 in this repository, one run each (same versions as
above). A background investigation (`docs/roles.md` "Investigations and
explanations") keeps the sub-agent's session so a follow-up
(`investigate(..., follow_up_of)`) continues the same conversation
instead of re-reading the repository. The backend is the persistence
layer: sheepdog stores only the id.

- **Codex.** Drop `--ephemeral` (it is what suppresses the session file)
  and add `--json`; the first stdout line is
  `{"type":"thread.started","thread_id":"01a0c540-…"}`, the answer is the
  `text` of the last `{"type":"item.completed","item":{"type":"agent_message"}}`,
  and `turn.completed` carries the usage. Resume with
  `codex exec resume <thread_id> "<question>"`: it re-emits the same
  `thread.started` id and answered from the earlier turn's context
  without re-reading the file. **`codex exec resume` has no `--sandbox`,
  `-C` or `--color`** of its own (checked with `--help`), so the sandbox
  goes through `-c sandbox_mode=read-only` — accepted under
  `--strict-config`, so it is a real key, not a silently ignored one —
  and the directory through the child's cwd, which the runner sets
  anyway. Sessions live under `~/.codex/sessions` and `resume` filters by
  cwd unless `--all` is passed.
- **Claude.** Drop `--no-session-persistence`; the JSON result carries
  `session_id` ("401669a7-…"). Resume with `claude -p "<question>"
  --resume <session_id>` and the same read-only flags: the same
  `session_id` comes back, `permission_denials` is still `[]`, and the
  answer used the earlier turn's context. Keep `-p "{question}"` first
  and `--resume {session}` after it — `--resume [value]` takes an
  optional value and would otherwise swallow a following positional.
- Both runs were made with `HERDR_*`, `CLAUDECODE` and `CLAUDE_CODE_*`
  scrubbed, as the runner does.
- **The output cap must not eat the thread id.** Codex streams every
  command's `aggregated_output` into the JSONL, so a real investigation
  is far past `cli.max_output_kb` and the kept tail no longer holds line
  one. The runner therefore keeps the first 4 KB of stdout as well and
  reads the thread id from there when the tail has none; the answer
  still comes from the tail, where the final `agent_message` is.
- Consequence for `run_cli`: the shared templates now persist a session
  for every invocation, not only for investigations (codex writes one
  under `~/.codex/sessions`, claude under `~/.claude/projects`). That is
  the price of being able to follow up; nothing reads those files but
  the CLIs themselves.

## 2c. Forking a worker's own session

Spike of 2026-09-25 for `investigate(source: "worker")` (docs/dive-deep.md
§3), on **Claude Code 2.1.282** and **codex-cli 0.155.0**, three short
runs per backend, each in a scratch git repository with a scratch session
(never a live worker's). Every run went through the runner's environment
rules: `HERDR_*`, `CLAUDECODE` and `CLAUDE_CODE_*` unset, stdin
`/dev/null`. The question: can the supervisor ask a worker's own session
a side question, with its memory, without writing into the session the
worker's TUI still has open? `[verified]` marks what a run showed,
`[inferred]` what the file layout implies but no run tested.

### Claude: `--resume <id> --fork-session`

```sh
# 1. a scratch "worker" session
claude -p "Remember the word pelican. Reply OK." <read-only flags>
# 2. the fork: a new session id, the old one untouched
claude -p "What word did I ask you to remember?" \
  --resume <id> --fork-session <read-only flags>
# 3. a follow-up on the fork (plain resume, no --fork-session)
claude -p "Spell that word backwards." --resume <fork id> <read-only flags>
```

`<read-only flags>` is the §2 set: `--model claude-haiku-4-5-20251001
--tools Read,Grep,Glob,WebSearch --allowedTools Read,Grep,Glob,WebSearch
--strict-mcp-config --permission-mode dontAsk --permission-prompts none
--output-format json --max-budget-usd 0.50`.

| run | wall | `duration_ms` | cost | tokens | `session_id` |
|---|---|---|---|---|---|
| 1 create | 2.5 s | 1 247 | $0.018 | cache write 8 798, out 68 | `50b69d96-…` |
| 2 fork | 2.1 s | 1 040 | $0.019 | cache read 8 798, write 143, out 40 | `218f5ac7-…` (new) |
| 3 follow-up | 2.8 s | 1 354 | $0.021 | cache read 8 941, write 112, out 55 | `218f5ac7-…` (same) |

- `--fork-session` exists: "When resuming, create a new session ID
  instead of reusing the original (use with --resume or --continue)"
  [verified, `claude --help`].
- The fork remembers: run 2 answered `pelican` [verified].
- It is a fork: run 2 returned a new `session_id`, the original
  `~/.claude/projects/<encoded cwd>/50b69d96-….jsonl` kept its size
  (67 310 bytes), mtime and SHA-1 through runs 2 and 3, and the fork
  wrote its own `218f5ac7-….jsonl` holding a copy of the history under
  the new id [verified].
- A follow-up resumes the fork, not the worker: run 3 (plain `--resume
  <fork id>`) returned the fork's id, answered from its memory and grew
  only the fork's file (73 085 → 79 466 bytes) [verified].
- Read-only: `permission_denials: []` on every run, `git status` clean
  [verified]; the tool set is the §2 one, unchanged by `--resume`.
- herdr: the herdr integration's `SessionStart` hook in
  `~/.claude/settings.json` matches `startup|resume|clear|compact|fork`,
  so it *would* fire for a fork, but its script exits unless
  `HERDR_ENV=1`, `HERDR_SOCKET_PATH` and `HERDR_PANE_ID` are set. With the
  runner's scrub it is a no-op: `herdr agent list` showed the same panes
  and the same `agent_session` values before and after all three runs
  [verified]. Without the scrub (a daemon inside a herdr pane) the fork
  would report itself as that pane's session [inferred from the script].
- Claude looks the session up under the project directory named after
  the child's cwd, so the fork must run in the worker's cwd (herdr's
  `cwd` for the pane, which is also where `internal/workerlog` looks)
  [inferred; a cwd mismatch was not run].
- Forking a session the TUI still has open: the fork only reads the
  original file and writes its own, so the live session is not touched
  [inferred from the file layout; the spike's original was closed]. A
  worker resumed mid-turn (a dangling tool call) was not tested.

### Codex: `codex exec resume` appends; `codex exec fork` forks

```sh
# 1. a scratch "worker" thread (§2b: no --ephemeral, --json)
codex exec --sandbox read-only --skip-git-repo-check --color never \
  -c model_reasoning_effort=low -C <dir> --json "Remember the word pelican. Reply OK."
# 2. the fork
codex exec fork <thread> "What word did I ask you to remember?" \
  -c sandbox_mode=read-only -c model_reasoning_effort=low --skip-git-repo-check --json
# 3. resume, for comparison
codex exec resume <thread> "What word did I ask you to remember?" \
  -c sandbox_mode=read-only -c model_reasoning_effort=low --skip-git-repo-check --json
```

| run | wall | `turn.completed.usage` | `thread_id` |
|---|---|---|---|
| 1 create | 3.2 s | in 11 306 (cached 8 960), out 5 | `01a0d7a6-7d0f-…` |
| 2 fork | 3.5 s | in 22 632 (cached 17 920), out 11 | `01a0d7a6-b0eb-…` (new) |
| 3 resume | 3.1 s | in 22 632 (cached 19 968), out 11 | `01a0d7a6-7d0f-…` (same) |

- **`codex exec resume` appends to the original rollout**: run 3
  re-emitted the original thread id and the original
  `~/.codex/sessions/…/rollout-…-01a0d7a6-7d0f-….jsonl` grew from 34 518
  to 41 423 bytes (SHA-1 changed) [verified]. `codex exec resume --help`
  has no fork flag [verified]. So resume must never be pointed at a
  worker's live thread.
- **`codex exec fork <SESSION_ID> [PROMPT]`** exists in 0.155 ("Fork a
  previous session by id into a new session") with the same options as
  `exec resume` (`-c`, `--skip-git-repo-check`, `--json`, `--ephemeral`,
  …; no `--sandbox`, `-C`, `--color` or `--all`) [verified, `--help`].
  Run 2 answered `Pelican`, returned a new thread id, left the original
  byte-identical (checked before run 3) and wrote a new rollout
  [verified].
- The fork's rollout does not copy the history: its `session_meta` has
  `forked_from_id`, `forked_from_ordinal_exclusive: 13` and
  `history_base {thread_id, end_ordinal_exclusive: 13, end_byte_offset:
  34518}`, and its own lines start at ordinal 13 [verified]. A fork is a
  read of the parent up to a byte offset; later growth of the parent
  (the worker working on) is not part of the fork [inferred from the
  layout].
- Read-only: the fork's `turn_context` records `sandbox_policy:
  {type: read-only}`, `approval_policy: never` from `-c
  sandbox_mode=read-only` [verified]; `git status` clean [verified].
- A follow-up on the fork is `codex exec resume <fork id>`: it appends
  to the fork's own rollout, which only sheepdog uses [inferred from
  run 3's behaviour on the original].
- Forking a thread whose interactive TUI is still open: the fork only
  reads the parent's rollout up to the recorded offset and writes a new
  file, so the TUI's file is never written [inferred; not runnable
  without a live worker]. Resuming that thread would interleave two
  writers in one rollout: refused.
- The herdr integration's Codex hook (`~/.codex/hooks.json`) has the same
  `HERDR_*` gate; `herdr agent list` was unchanged [verified].
- Whether `exec fork` finds a thread created under a different cwd is
  untested (`exec resume` filters by cwd only for `--last`/the picker)
  [inferred]; sheepdog forks in the worker's cwd anyway.

### Cost and time

The scratch sessions were tiny (≈ 9–23 k tokens with the tools' own
system prompts), so the runs took 2–3.5 s. A worker's real session is
much larger: the fork's first turn pays for the worker's whole context,
and for Claude with a reduced tool list the prompt-cache prefix differs
from the worker's, so it is not a cache hit [inferred]. Two consequences
for WP8c:

- The Claude fork runs without `--model`, on the user's default model:
  Haiku's 200 k window cannot hold a long day's session, and the fork
  should reason with the worker's own model family. `--max-budget-usd`
  is raised to 2.00 for the fork (notional on a subscription; a large
  context on the default model would trip 0.50 on the first turn)
  [inferred; neither limit was hit in the spike].
- The run is bounded by `brain.investigate_max_s` like any
  investigation; it uses the shepherd's own Claude/Codex usage, never
  Gemini quota.

### Consequence

Both backends can answer from the worker's memory without writing into
its session: Claude with `--resume <id> --fork-session`, Codex with
`codex exec fork <thread>` (not `exec resume`). The fork's own id is the
session a follow-up resumes. The argv is fixed in `internal/cli`
(`defaultClaudeForkCmd`, `defaultCodexForkCmd`), not configurable.

## 3. Config defaults

```toml
[cli]
# {cwd}, {question}, {model}, {reasoning} and (resume only) {session}
# are substituted by sheepdog; argv arrays, no shell.
codex_cmd  = ["codex", "exec", "--sandbox", "read-only", "--skip-git-repo-check",
              "--color", "never", "-c", "model_reasoning_effort=low",
              "-C", "{cwd}", "--json", "{question}"]
claude_cmd = ["claude", "-p", "{question}", "--model", "{model}",
              "--tools", "Read,Grep,Glob,WebSearch",
              "--allowedTools", "Read,Grep,Glob,WebSearch",
              "--strict-mcp-config",
              "--permission-mode", "dontAsk", "--permission-prompts", "none",
              "--output-format", "json",
              "--max-budget-usd", "0.50"]
codex_resume_cmd  = ["codex", "exec", "resume", "{session}", "{question}",
                     "-c", "sandbox_mode=read-only",
                     "-c", "model_reasoning_effort={reasoning}",
                     "--skip-git-repo-check", "--json"]
claude_resume_cmd = ["claude", "-p", "{question}", "--resume", "{session}",
                     "--model", "{model}",
                     "--tools", "Read,Grep,Glob,WebSearch",
                     "--allowedTools", "Read,Grep,Glob,WebSearch",
                     "--strict-mcp-config",
                     "--permission-mode", "dontAsk", "--permission-prompts", "none",
                     "--output-format", "json",
                     "--max-budget-usd", "0.50"]
timeout_s     = 60      # kill the process group on expiry (plan §3.5); run_cli only
output_cap_kb = 16
prefer        = "codex" # first detected wins; both tools took ~17 s on a trivial question at default effort
```

The fork templates of `investigate(source: "worker")` (§2c) are not in
the config: they are fixed in `internal/cli` (`defaultClaudeForkCmd`,
`defaultCodexForkCmd`), because the fork is only safe in the verified
form (`--resume {session} --fork-session` for claude, `codex exec fork
{session}` for codex, never `codex exec resume` on a worker's thread).
The fork runs on the worker's own backend, whatever `prefer` says, and a
follow-up resumes the fork's id through the resume templates above.

An investigation is not bounded by `timeout_s` but by
`brain.investigate_max_s` (600 s), which is a sanity ceiling, not a
budget: the answer may take as long as it takes (CONFIG.md).

Runner notes for WP4:

- `exec.Command` with `Stdin = nil` (→ `/dev/null`), `Dir = cwd` (Claude) plus
  `-C cwd` (Codex), `Setpgid` and kill the whole group on timeout (both tools
  spawn children: `/bin/zsh -lc …` for Codex, `rg` for Claude).
- Strip `HERDR_*` from `cmd.Env`; keep `HOME`, `PATH`, and the tools' own
  auth env untouched. Node for Claude is installed via nvm; `claude` resolved on
  `PATH` in this shell, but a launchd-started daemon needs an explicit `PATH`
  containing `~/.nvm/versions/node/*/bin` and `~/.local/bin` (herdr).
- Parse: Codex → the last `agent_message` item of the `--json` stream
  (plain stdout when a template asks for no `--json`); Claude →
  `json.result`, error if `is_error`. The session id comes from the same
  output (§2b).
- Expect **15–20 s** per call with default reasoning settings and
  **$0.03–0.45** per Claude call; the plan's 60 s timeout is adequate, and
  the brain turn that reads the output adds its own latency on top.

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
