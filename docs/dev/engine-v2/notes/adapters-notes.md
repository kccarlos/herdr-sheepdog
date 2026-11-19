# Agent-CLI adapter notes (engine v2)

Researcher: adapter researcher, 2026-11-18. Read-only research.
Source tags: [H-claude] = `claude --help` (2.1.325), [H-codex] = `codex --help` / `codex exec --help` /
`codex resume --help` (codex-cli 0.169.0), [H-agy] = `agy --help` (1.5.11), [H-codexbar] = `codexbar --help` (0.84.0).
Old engine = factory/mods/tasks/bin/herdr_task/ (file:line). UNVERIFIED = not seen in any source.

## 1. Installed CLI facts (from help output only)

### claude 2.1.325 [H-claude]
- Interactive by default: `claude [options] [prompt]` — positional prompt starts interactive session with it.
- `--model <alias|full name>` (aliases e.g. fable, opus, sonnet); `--effort low|medium|high|xhigh|max`;
  `--fallback-model <list>`.
- `--permission-mode acceptEdits|auto|bypassPermissions|manual|dontAsk|plan`;
  `--dangerously-skip-permissions`; `--allow-dangerously-skip-permissions`; `--allowedTools` / `--disallowedTools`;
  `--tools`; `--restricted` (drops Bash etc., refuses bypassPermissions, confines file tools to working dirs).
- Session: `--session-id <uuid>` (engine can PRE-ASSIGN the id), `-r/--resume [id|name]`, `-c/--continue`
  (most recent in cwd), `--fork-session`, `-n/--name`.
- `-p/--print` headless; NOTE help: "workspace trust dialog is skipped" in -p or when stdout not a TTY;
  `--output-format text|json|stream-json`, `--input-format stream-json`, `--include-hook-events`,
  `--no-session-persistence`, `--max-budget-usd`, `--permission-prompts host|none`.
- `--settings <file-or-json>` (inject hooks per session), `--setting-sources`, `--bare` (skips hooks, OAuth not read),
  `--safe-mode`, `--append-system-prompt`, `--add-dir`, `-w/--worktree`, `--bg` (background sessions with
  `claude agents/attach/logs/stop`), `--ax-screen-reader` (flat text output — possibly easier screen parsing).
- `claude update|upgrade`, `claude auth`, `claude doctor`. No model-list subcommand in help.

### codex 0.169.0 [H-codex]
- `codex [OPTIONS] [PROMPT]` interactive with optional first prompt. `-m/--model`; NO effort flag in help —
  effort only via `-c model_reasoning_effort=...` (UNVERIFIED key name; only `-c key=value` generic is in help).
- `-s/--sandbox read-only|workspace-write|danger-full-access`; `-a/--ask-for-approval on-request|never`;
  `--approve-for-me` (auto-review approvals under workspace-write); `--dangerously-bypass-approvals-and-sandbox`;
  `--dangerously-bypass-hook-trust` (hooks exist in Codex; need persisted trust).
- `-C/--cd <DIR>` working root, `--add-dir`, `--worktree`, `--no-alt-screen` (inline TUI, keeps scrollback —
  relevant for pane reading), `--no-daemon`, `-p/--profile` (NOTE: -p is profile, not print).
- `codex exec [PROMPT]` headless: prompt from argv or stdin (`-`), `--json` JSONL events, `-o/--output-last-message`,
  `--ephemeral`, `--skip-git-repo-check`, `--trust` (persist project trust), `--output-schema`; `codex exec resume`.
- `codex resume [SESSION_ID] [PROMPT]` — UUID or session name; `--last`; `--all` "disables cwd filtering" (so the
  default picker/--last IS filtered by cwd). Accepts -m/-s/-a/-C on resume.
- `codex queue` "Queue a message for an existing session" — a non-typing message channel (UNVERIFIED in practice).
- `codex update`, `codex login`, `codex doctor`. No model-list subcommand in help.

### agy 1.5.11 [H-agy]
- `--model`, `--effort low|medium|high|xhigh|max` (separate flag in 1.5.11; wiki/CLAUDE.md use model-suffix
  form e.g. `claude-sonnet-5-5-medium` — see section 3).
- `--mode accept-edits|auto|plan`; `--dangerously-skip-permissions`; `--sandbox` ("terminal restrictions").
- `-p/--print` one-shot, `--print-timeout` (default 0 = wait), `--output-format text|json|stream-json`,
  `--input-format stream-json`; `-i/--prompt-interactive <prompt>` = run initial prompt interactively and continue.
- Resume: `--conversation <ID>`, `-c/--continue` (most recent). No `--session-id` pre-assign. No `-C/--cd`: cwd =
  process cwd (inferred from absence in help).
- `--settings <json|file>` session-only settings; `--add-dir`; `--project`, `--new-project`.
- `agy models` "List available models" — the only CLI of the three with a model-list probe.
- `agy update`, `agy changelog`, `agy acp` (Agent Client Protocol stdio server — structured channel).

### codexbar 0.84.0 [H-codexbar]
- `codexbar usage --provider <p> [--json|--format json]` ; providers include claude, codex, antigravity, gemini.
  Text output (seen for claude): `Session: N% left`, `Resets in 1h 24m`, `Weekly: N% left`, `Resets in 4d 11h`.

## 2. Old engine (herdr_task) — what it does per CLI

### 2a. Every place that builds or rewrites an agent argv (duplication)
| # | function | file:line | what |
|---|---|---|---|
| 1 | `AGENTS` base argv | settings.py:17 | claude=`claude`, codex=`codex --no-daemon`, agy=`agy` |
| 2 | `argv_for(pick)` | settings.py:421-458 | worker/assistant: model, effort, fast, perm per CLI (mirrors JS `mods/chat/agent.js argvFor`, per its docstring) |
| 3 | `permission_flags(agent, perm)` | settings.py:461-472 | perm -> flags table (see 2c) |
| 4 | `worker_permissions(argv, perm, replace)` | settings.py:475-518 | re-parses an argv, strips/replaces perm flags, adds codex hook-trust |
| 5 | `codex_review_argv(pick, folder)` | settings.py:400-418 | gate reviewer on Codex: scratch -C, trust table, `-s workspace-write -a never`, hooks off |
| 6 | reviewer command (brief's Reviewer line) | settings.py:~798-811 | codex `--sandbox read-only --ask-for-approval never --disable hooks`; claude `--permission-mode plan` |
| 7 | `own_argv(argv, folder, ...)` | launch.py:131-~220 | injects `--settings` JSON (allow rules, idleCompaction=false, skipDangerousModePermissionPrompt) for claude; trust `-c projects={...}` for codex; merged `--settings` (allow + trustedWorkspaces) for agy |
| 8 | `claude_mode` + `resume_argv` | launch.py:294-341 | resume: claude `--permission-mode <mode> --resume <id>`; codex `codex resume <flags> <id>`; agy `--conversation <id>` |
| 9 | `sandboxed(argv, ...)` / `SANDBOX_SH` | launch.py:240-281 | agy reviewer under sandbox-exec + reviewer.sb, fresh HOME |
| 10 | `no_update_env`, `clean_env`, `in_shell`, `reviewer_shell`, `low_priority` | launch.py:357-453 | env/shell wrappers (DISABLE_AUTOUPDATER=1 claude only; unset CLAUDECODE/CLAUDE_CODE_*; login shell; nice/taskpolicy) |
| 11 | `launch(...)` | launch.py:770-826 | initial prompt: agy `-i <msg>`, others positional; Codex reviewer prompt HELD and typed via `send_when_clear` |
| 12 | revive `parse`/`first`/`resume_argv` | revive.py:40-117 | re-parses recorded argv with CLAUDE_VALUE/CODEX_VALUE/AGY_VALUE flag tables (hand-kept copies of --help) |
| 13 | `backup_argv` | takeover.py:140-146 | takeover: argv_for({"agent": kind}) |
| 14 | `machine.py` usage probes | machine.py:276-601 | runs claude `/usage` and codex under sandbox-exec/bwrap to read limits |
Also JS: `mods/chat/agent.js argvFor`, `core/agents.js CLAUDE_VALUE/CODEX_VALUE` (named in comments; not opened).

### 2b. Initial prompt delivery (launch.py:788-790, 736-751)
- claude, codex: last positional argv word. agy: `-i <msg>` ("agy reads prompts only from -i").
- Codex REVIEWER: prompt held, then `hc read` polled until CODEX_READY (`^\s*[›>]\s|context left|? for shortcuts`)
  and not CODEX_TRUST (`Trust this folder|Do you trust the contents|Yes, continue`); trust answered with one Enter;
  `hc send` the prompt; 60 s timeout -> kill, gate tries second CLI.

### 2c. Permission mapping today (settings.py:461-472)
| engine perm | claude | codex | agy |
|---|---|---|---|
| full (default!) | `--dangerously-skip-permissions` (+settings skipDangerousModePermissionPrompt) | `--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust` | `--dangerously-skip-permissions` |
| accept-edits | `--permission-mode acceptEdits` | `-s workspace-write -a on-request` | `--mode accept-edits` |
| read-only | `--permission-mode plan` | `-s read-only -a on-request` | `--mode plan` |
| manual | `--permission-mode default` (NB: help lists `manual`, not `default`) | workspace-write/on-request | accept-edits |
Reviewer (gate): claude plan; codex read-only+never (or workspace-write with scratch -C, settings.py:400); agy
`--dangerously-skip-permissions` INSIDE sandbox-exec (Mac only; elsewhere plan + allow list), launch.py:240-253.

### 2d. Sessions (sessions.py:76-117, revive.py)
- claude: `~/.claude/sessions/<pid>.json` -> `sessionId`; log `~/.claude/projects/*/<sid>.jsonl`.
- codex: open file `rollout-...<uuid>.jsonl` (via /proc fd or `lsof -p`), under `~/.codex/sessions/Y/M/D/`.
- agy: open file `~/.gemini/antigravity-cli/conversations/<uuid>.db` (sqlite; no readable log).
- Refreshed each tick (changes on /clear). Recorded on terminal label `herdr.resume` (revive.py:1-13).
- Resume lost: LOST regex `no (saved )?(conversation|session|rollout) ... found` -> fresh session + FRESH handover.
- Claude resume brings back the permission mode the session ENDED in (#1056) -> must restate mode (launch.py:327-336).
- Resume needs same cwd: codex picker/--last is cwd-filtered ([H-codex] `--all` "disables cwd filtering");
  revive trusts worktree/cwd (revive.py:319). Explicit-UUID resume across cwd: UNVERIFIED.
- Go side already gets session from herdr: `AgentSession{Source "herdr:codex|herdr:claude|herdr:antigravity_cli",
  Kind id|path, Value}` (internal/herdr/protocol.go:129) — no pid/lsof needed.

### 2e. State signals
- Old engine: `status_word(k)` = herdr-core's status (agents.py:383), fed by Herdr's CLI integrations (OSC 7501
  reports incl. kind auth / permission, agents.py:695-700); Claude PermissionRequest hook req held by Herdr
  (agents.py:688). Plus screen parsing: Claude TRUST/BYPASS/ONBOARD dialogs (agents.py:407-410) because they show
  before any hook fires; watchdog API_ERROR / SIGNED_OUT regexes (watchdog.py:19-20); WORKING_LINE spinner
  digest for "unchanged screen" stuck detection (watchdog.py:30-37, STUCK_MS 30 min, QUIET_MS 20 min);
  agy background tasks `N task(s)` (agents.py:1221).
- Go daemon: Watcher turns herdr status events into Transitions idle|working|blocked|done|unknown with
  Closed (pane gone) / Released (agent exited, shell stayed) (watcher.go:9-48, protocol.go:14-18).
  `ParsePrompt` one parser for all three CLIs' numbered-option prompts (prompt.go:12-19); `EndsInQuestion`
  turns a "done" ending in a question into blocked because "agy reports done around its prompts" (prompt.go:219-224).
- Codex hooks: SDF-53 dropped "the Codex hook path"; reviewers run `--disable hooks`; full workers get
  `--dangerously-bypass-hook-trust` so a new hook does not stop them on "Hooks need review" (settings.py:404,487,517).

### 2f. Message delivery to a running agent
- Go: `SendInput(pane, text, keys...)` = herdr `pane.send_input` text + keys in one write; `SendKeys` named keys;
  `Interrupt` = Ctrl-C leaves Codex/Claude at its prompt (client.go:420-450). `TypedInput/TypedInputPlain` detect
  unsent text in the input box before typing (typing.go:29-60).
- Old: `hc send <addr> <msg>`, `hc keys <addr> enter` (launch.py:745-748).

### 2g. Status protocol (docs/dev/design/herdr-core-backend.md:27-56)
- hc never guesses status: programs report OSC 7501 words idle|working|done|blocked|error + app, kind
  (permission|question|auth), msg. Verified 2026-10-30 (Claude 2.1.314, Codex 0.166): both report idle/working/done/
  blocked(permission); Claude's done carries msg (one-line reply summary). agy reports too (`app: antigravity`,
  blocked kind permission with full command, kind question, done with first line) [wiki agy.md, agy 1.5.3].
- GAP: first-run trust question comes BEFORE reporting starts -> `status: null, reported: false` -> screen fallback.
- hc events: status, turn (turn ended), title, created, finished, lost, removed, foreground, heartbeat 15 s, missed.

## 3. Wiki / journal / task facts (field)
- agy effort: wiki says "effort is the model name's suffix" (`gemini-4-argon-high`) [agy.md:15]; agy 1.5.11 also has
  `--effort` [H-agy] and the old engine passes `--effort` (settings.py:455). Whether `--effort` with a suffixed
  model conflicts, and whether claude-* models on agy take suffixes (`claude-sonnet-5-5-medium` in CLAUDE.md): UNVERIFIED.
- agy: rejects positional prompt; `-i` interactive; untrusted folder -> prompt waits behind trust question; trust
  answered late -> relative files land in `~/.gemini/antigravity-cli/scratch/` [agy.md:84,92]. Pre-trust via
  `--settings {"trustedWorkspaces":[...]}` (launch.py:187).
- agy `--settings` allow list REPLACES global list [agy.md:87]; allow list matches literal prefixes -> prompts on
  every variation [agy.md:76]; `agy -p` default mode asks nothing, unrestricted [agy.md:75]; `--mode auto` not
  available on this account (falls back) [agy.md:74].
- agy messages: `hc send` while working only QUEUES ("alt+m to send immediately"); `hc keys alt+m` injects mid-turn;
  `hc interrupt` = esc. Sending from inside an hc terminal wraps text as `<hc from=..>..</hc>`, so slash commands
  become prompts; use `env -u HC_TERMINAL_ID -u HC_SCOPE hc send` [agy.md:89-90].
- agy `-c` resumes latest conversation of cwd — another agy in the same folder can take it; use `--conversation <id>`
  [agy.md:90]. Conversation db + brain/<id> grow (1.6G/2.1G) [agy.md:78].
- agy headless: "stream idle timeout (5m0s)" with status ERROR on a finished job; exit code meaningless; a denied
  command ends the turn exit 0 with empty output (`denied_actions` in last stream-json event) [agy.md:31, codex-deepseek.md];
  hangs of 34 min on edit_file; `http2: server sent GOAWAY` after 28 min [agy.md:32,88]. Never set GEMINI_API_KEY
  (switches billing) -> `env -u GEMINI_API_KEY` [codex-deepseek.md].
- agy built-in sandbox via `--settings {"enableTerminalSandbox":true,"toolPermission":"proceed-in-sandbox",...}`
  blocks network (git push/gh fail) [agy.md:101]. sandbox-exec + `--dangerously-skip-permissions` is the
  current reviewer boundary; sandbox can't reach keychain (agy sign-in) -> opt-out flag (launch.py:255-258).
- Codex: `codex exec` waits on stdin when not a TTY -> `</dev/null` required [codex-deepseek.md]. `--approve-for-me`
  conflicted with `--sandbox` on 0.165 [codex-deepseek.md]. Shared app-server daemon can keep a stale sign-in ->
  every turn fails -> always `--no-daemon` (#745, settings.py:791). "Trust this folder?" and "Hooks need review"
  dialogs block unattended runs (#798, #753) -> `-c projects={...trust_level="trusted"}` table (dotted key breaks on
  dots in path), `--disable hooks` / `--dangerously-bypass-hook-trust` (settings.py:791-797).
- Codex sessions resumed after auto_review may record a Guardian thread id; that resume fails [journal 11-17:23].
- Codex model source of truth: `~/.codex/models_cache.json` `models[].slug` (account-specific; ChatGPT account
  gets fewer, #948) (settings.py:724-740). Field: 6 of 11 Codex tasks ran on whatever ~/.codex/config.toml named
  because argv lacked -m/perm (SDF-53/54 "Why"). SDF-70: reviewer model validated against WORKER's CLI (bug).
- SDF-53 rule: every start and resume argv states perm; default `full`; resume rebuilds flags from the current rule
  and keeps only the session id. Regression found: revive applied bypass flags to helper/reviewer terminals
  [journal 11-17:20]. SDF-54: every argv states model+effort, refuse add without a resolvable model; never the CLI's
  own default; run record fields model, effort, model_source, perm, model_used (from session file), resumed_from.
  It grew to ~2,800 lines (launch plan stored twice) and was split [SDF-54 brief changes; journal 11-18:35].
- Claude: auto-update at night -> macOS asks whether the NEW binary may access the removable volume -> every new
  agent hung blank until Allow clicked [lessons/software-factory.md:47]; fix DISABLE_AUTOUPDATER=1 (launch.py:357-365;
  Codex/agy have no switch in help — note `codex update`/`agy update` exist as commands only).
- herdr-core lost Files and Folders access to ~/Documents -> 3h15m stall, every git hung [lessons:46]; again
  2026-11-18 after herdr-app auto-update [journal 11-18:6]. Watchdog "New agents start blank" outage alert (2+
  blank screens after STARTING_MS) (watchdog.py:72-107).
- Claude first-run dialogs before status: folder trust (#755), Bypass Permissions confirm, onboarding theme picker
  (agents.py:405-433) -> pre-accept `hasTrustDialogAccepted` in ~/.claude.json (launch.py:84-120) and
  `skipDangerousModePermissionPrompt` via --settings. Claude inherits parent-session env (CLAUDECODE etc.) -> no
  transcript and skips trust prompt -> unset (launch.py:343-355). Claude idle compaction wakes a Ready worker ->
  `idleCompaction:false` (FAC-107, launch.py:163).
- Claude --help: `-p` skips the workspace trust dialog and silently ignores invalid settings [H-claude].
- API/limit screen text: watchdog API_ERROR `API Error|Please run /login|overloaded|rate.?limit...`; SIGNED_OUT
  `not logged in|invalid api key|authentication_error|oauth token .*expired|credit balance` (watchdog.py:19-20);
  OSC 7501 kind auth = signed out (agents.py:695). Exact limit-hit wording per CLI: UNVERIFIED.

## 4. Quota (usage.py, codexbar)
- `codexbar usage --provider <claude|codex|antigravity> --json` -> `[0].usage.primary/secondary` each
  `{usedPercent, windowMinutes (300=5h, 10080=week), resetsAt}`; antigravity: `usage.extraRateWindows[]` ids
  `antigravity-quota-summary-(gemini|3p)-(5h|weekly)` -> pools antigravity-gemini, antigravity-claude-gpt
  (usage.py:9-50). Floors 5h 5%, week 1%; cached 60 s. agy pool depends on the model prefix (gemini vs claude/gpt)
  (handoff.py:15-29). Text form verified live for claude (Session/Weekly % left, Resets in).
- Wiki: codexbar Antigravity bars did not move after small runs -> may not reflect billing [codex-deepseek.md].
- machine.py also reads Claude `/usage` and Codex limits under sandbox-exec/bwrap (machine.py:8, 276-601).

## 5. Per-CLI tables (sources in brackets)

### claude
| topic | fact |
|---|---|
| fresh argv | `claude --model M --effort E --settings JSON <perm> [prompt]`; cwd = pane cwd (hc --cwd) [H-claude, settings.py:431-440, launch.py:805] |
| prompt | positional last word [launch.py:790]; can also pre-set `--session-id <uuid>` [H-claude] |
| headless | `-p --output-format stream-json`; skips trust dialog. Factory: NO for workers (can't steer/watch, invisible in herdr) [H-claude; agy.md:88 analog] |
| resume | id from herdr AgentSession or `~/.claude/sessions/<pid>.json`; argv `--resume <id>` + RESTATE perm (#1056 resumes in ended mode); `--session-id` lets engine own the id [launch.py:327-336, sessions.py:76-93] |
| perm | read-only=`--permission-mode plan`; edit=`acceptEdits`; full=`--dangerously-skip-permissions` (+skipDangerousModePermissionPrompt). Also `auto`, `dontAsk`, `manual` [H-claude, settings.py:466-468] |
| signals | OSC 7501 via Herdr integration (hooks; PermissionRequest req held by Herdr) [agents.py:688-700, herdr-core-backend.md:38-56] |
| models | no list command; `--model` alias or full name; validation UNVERIFIED (unknown model error text unseen) |
| quota | codexbar provider claude |
| failures | trust/bypass/onboarding dialogs pre-status; auto-update -> TCC removable-volume prompt; inherited CLAUDECODE env; idle compaction; resume mode drift |
| message | hc send / pane.send_input + enter; Ctrl-C interrupts turn [client.go:420-450] |

### codex
| topic | fact |
|---|---|
| fresh argv | `codex --no-daemon -m M -c model_reasoning_effort="E" -c service_tier="default" <perm> -c projects={trust} [-C dir] [prompt]` [settings.py:442-448, launch.py:172-175] |
| prompt | positional; reviewers: typed after CODEX_READY (trust screen answered) [launch.py:736-751] |
| headless | `codex exec` (stdin must be </dev/null; `--json`, `-o`); factory: no for workers |
| resume | id = open `rollout-*<uuid>.jsonl` or herdr AgentSession; `codex resume <flags> <id>` (subcommand FIRST); picker/--last cwd-filtered; Guardian-thread resume fails; "No saved session found" [launch.py:337-338, H-codex, journal 11-17] |
| perm | read-only=`-s read-only -a never`(reviewer)/`on-request`; edit=`-s workspace-write -a on-request`; full=`--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust`; `--approve-for-me` auto-review [settings.py:463-465, H-codex] |
| signals | OSC 7501 idle/working/done/blocked(permission) [herdr-core-backend.md]; Codex hook path dropped (SDF-53); `codex queue` exists [H-codex] |
| models | `~/.codex/models_cache.json` slugs (account-specific) [settings.py:724] — closest thing to a probe |
| quota | codexbar provider codex |
| failures | stale shared daemon sign-in (#745); trust + hooks-review dialogs (#798,#753); config.toml silently supplies model/perm when argv omits them (SDF-53/54); Linux bwrap sandbox refusal (sandbox.py:72) |
| message | same as claude; `--no-alt-screen` keeps scrollback readable |

### agy
| topic | fact |
|---|---|
| fresh argv | `env -u GEMINI_API_KEY agy --model M [--effort E or model suffix] --settings JSON(allow+trustedWorkspaces) <perm> -i <prompt>`; cwd = process cwd (no -C flag) [H-agy, settings.py:451-456, launch.py:176-220] |
| prompt | ONLY `-i` (positional rejected) [agy.md:84, launch.py:790] |
| headless | `-p`/`--print`, stream-json; unrestricted in default mode, exit code meaningless, 5m stream idle timeout. Factory: no |
| resume | id = open `conversations/<uuid>.db` (lsof) or herdr AgentSession; `--conversation <id>`; alive check = db file exists; never `-c` [revive.py:120-124, agy.md:85,90] |
| perm | read-only=`--mode plan`; edit=`--mode accept-edits`; full=`--dangerously-skip-permissions`; reviewer = full inside sandbox-exec; `--sandbox` flag / enableTerminalSandbox (no network) [settings.py:469-471, launch.py:240-281, agy.md:101] |
| signals | OSC 7501 `app: antigravity`; reports done around prompts -> EndsInQuestion fix; background `N task(s)` [agy.md:83, prompt.go:219, agents.py:1221] |
| models | `agy models` subcommand [H-agy] (output not inspected — UNVERIFIED shape) |
| quota | codexbar antigravity, pool by model prefix |
| failures | trust prompt blocks -i; scratch-dir misplacement; queued messages; hc wrapper on send; keychain unreachable in sandbox; `--mode auto` unavailable; GEMINI_API_KEY billing switch; disk growth |
| message | `hc send` queues while working; `alt+m` to inject; send with HC_TERMINAL_ID/HC_SCOPE unset |

## 6. Proposed adapter interface (7 operations)

Engine-level enums: Perm = readonly | edit | full. Role = worker | reviewer | coordinator. Intent = {role, cli,
model, effort, perm, resume_session|"", worktree/cwd, prompt}.

```go
type Adapter interface {
    // 1. Pure: intent -> exact process spec. The ONLY place argv is built. No re-parsing of old argv ever.
    Command(in Intent) (Spec, error)          // Spec{Argv []string; Env []string; Unset []string; Cwd string;
                                              //      PromptVia: Argv|Flag|TypeWhenReady; Prepare []FileEdit}
    // 2. Session id of a live agent (from herdr AgentSession first; file fallback).
    Session(pane PaneInfo) (id string, ok bool)
    // 3. Is a stored session still resumable? (claude/codex log exists, agy db exists)
    Resumable(id string) bool
    // 4. Models this account can use, from the CLI/its cache; nil = unknown (then accept and verify on start).
    Models(ctx) ([]string, error)
    // 5. Classify a screen when status is absent or suspicious: trust | bypass-confirm | onboarding | signed-out |
    //    api-error | limit | lost-session | ready-for-input | none.
    Screen(text string) ScreenState
    // 6. How to deliver a message to a running agent: text + keys (agy: + alt+m when working; raw, no hc wrapper).
    Deliver(msg string, working bool) Delivery   // {Text, Keys []string, RawSend bool}
    // 7. Quota pool(s) the intent spends: codexbar provider + pool id.
    Pool(in Intent) string
}
```
State (idle/working/blocked/done/closed/released) is NOT per adapter: it comes from OSC 7501 via the existing
Watcher for all three; only `Screen` covers the pre-status gap and the screen-text failures.

## 7. Data vs code per CLI

DATA (one table row per CLI; a struct literal, no logic):
| field | claude | codex | agy |
|---|---|---|---|
| bin + fixed args | claude | codex --no-daemon | agy |
| model flag | --model | -m | --model |
| effort | --effort E | -c model_reasoning_effort="E" | --effort E (or model suffix — UNVERIFIED which wins) |
| perm readonly | --permission-mode plan | -s read-only -a never | --mode plan (reviewer: full + sandbox-exec) |
| perm edit | --permission-mode acceptEdits | -s workspace-write -a on-request | --mode accept-edits |
| perm full | --dangerously-skip-permissions | --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust | --dangerously-skip-permissions |
| prompt via | positional | positional (reviewer: type when ready) | -i |
| resume form | flag `--resume ID` + restated perm | subcommand `resume … ID` | flag `--conversation ID` |
| cwd | pane cwd | pane cwd (or -C) | pane cwd |
| env set/unset | DISABLE_AUTOUPDATER=1; unset CLAUDECODE, CLAUDE_PID, CLAUDE_CODE_* | – | unset GEMINI_API_KEY |
| herdr session source | herdr:claude | herdr:codex | herdr:antigravity_cli |
| session file | ~/.claude/projects/*/ID.jsonl | ~/.codex/sessions/Y/M/D/rollout-*ID.jsonl | ~/.gemini/antigravity-cli/conversations/ID.db |
| codexbar provider | claude | codex | antigravity (+pool by model prefix) |
| screen regexes | TRUST/BYPASS/ONBOARD, LOST | CODEX_TRUST/CODEX_READY, hooks-review, LOST | trust, queued "alt+m" |

CODE (genuinely per CLI):
- settings injection: claude `--settings` merge (allow rules, idleCompaction, skipDangerous…) + ~/.claude.json
  `hasTrustDialogAccepted` write; codex `-c projects={…}` trust TOML table; agy `--settings` that must carry the
  person's global allow list (replace semantics) + trustedWorkspaces.
- agy reviewer sandbox-exec wrapper (Mac only; HOME copy; keychain opt-out).
- model listing: codex reads models_cache.json; agy runs `agy models`; claude: none (aliases) — UNVERIFIED probe.
- agy pool-by-model-prefix.
- Codex reviewer "type when ready" loop (could become generic: wait for status idle OR CODEX_READY).

## 8. Open questions (UNVERIFIED)
1. agy: `--effort` vs model-name suffix — which does 1.5.11 honour; are suffixed ids still listed by `agy models`?
2. `agy models` output shape and whether it reflects account entitlement (not run: only --help allowed).
3. Claude: any way to list models the account can use (no subcommand in help); error text for an unknown model.
4. Codex effort key `model_reasoning_effort` — only seen in the old engine and SDF-54, not in --help.
5. Does a model change on resume stick (claude `--model` + `--resume`; codex `resume -m`; agy `--conversation` +
   `--model`)? No source shows it either way. Does `--effort` persist across resume?
6. Codex `resume <uuid>` from a different cwd: allowed? Help only says the picker/--last is cwd-filtered.
7. Exact limit-hit screen text and OSC 7501 report per CLI when quota runs out (watchdog regex is generic).
8. `codex queue` as a non-typing message channel to a running interactive session — semantics unknown.
9. Claude `--session-id` pre-assignment with an interactive session (would remove pid/file discovery for claude).
10. Whether Claude Code's OSC 7501 comes from Herdr's hook integration or natively (doc says "programs report";
    agents.py:688 says "the ask Herdr's hook holds") — matters if hooks are skipped (`--bare`, `--safe-mode`).
11. agy auto-update: no disable switch found in help; `last_check.timestamp` exists in its state dir.

## 9. Corrections and additions after review

### 9a. More argv sites (untruncated grep; completes table 2a)
| # | function | file:line | what |
|---|---|---|---|
| 15 | `worker_pick` / `worker_argv` | settings.py:521, 539-552 | settings -> pick -> `worker_permissions(argv_for(pick))` |
| 16 | `argv_model` | settings.py:555-566 | re-parses model/effort back out of an argv (incl. codex `-c model_reasoning_effort=`) |
| 17 | `effective_argv` | settings.py:814-821 | resolution entry point; the function SDF-54/SDF-70 kept failing on |
| 18 | `agent_argv` | settings.py:1273-1284 | stored argv JSON or AGENTS default; re-adds codex `--no-daemon` |
| 19 | `resolve_again_argv` | commands.py:1999-2005 | restart: worker_argv(previous=…) |
| 20 | restart/resume call | commands.py:2023-2037 | `resume_argv(...)` when same CLI + session, else fresh launch |
| 21 | reviewer start | commands.py:903 | codex_review_argv or argv_for({..., perm}) |
| 22 | retro round | retro.py:231 | `argv_for({**FABLE, "perm": "dontAsk"})` (FABLE = claude, claude-fable-5-2, settings.py:1144) |
| 23 | revive | revive.py:57 `base_argv`, 320 `own_argv(worker_argv(previous=rec.argv))`, 322 | |
| 24 | tick / automations | tick.py:449, automations.py:383 | `q.argv or worker_argv(s,t)` — a QUEUED argv stored on the task (the "launch plan stored twice") |
Plus queue writes of a computed argv at commands.py:462-463, 1827, 1881, 2113. Coordinator start goes through
`lease.restart` (watchdog.py:23; not opened). project.py / autopilot.py / assistant_settings.py: no matches for the
argv-builder names in the caller grep.
- DISAGREEMENT: revive.py:115 resumes Claude with the old argv as-is ("never launch's #1056 mode"), while
  launch.py:327-336 restates the permission mode. Two resume builders, two rules.
- `claude_mode()` returns "default" and resume appends `--permission-mode default`, but claude 2.1.325 --help lists
  only acceptEdits|auto|bypassPermissions|manual|dontAsk|plan. Possibly a rejected value — UNVERIFIED, and important.

### 9b. Session source correction
- `AgentSession` is filled from the herdr(-app) snapshot in internal/herdr/watcher.go; `internal/herdrcore` has no
  reference to it (grep). So on the herdr-core backend the session id is NOT known to come from herdr — keep the
  file/process fallback (sessions.py) as primary until verified. Section 2d's "no pid/lsof needed" is withdrawn.
- Open Q10 widened: do OSC 7501 reports survive `--disable hooks` (codex reviewers), `--bare`/`--safe-mode` (claude)?
  If they come from hooks, a hooks-off agent has no status and falls back to the screen.

### 9c. Delivery (old engine news.py:442-494)
- Before typing: `typing()` = `hc get` live + `typed_input(screen)`; if the person has text in the input line, the
  message is HELD (held.json under flock) and delivered later by `held_pass`. agy: a message typed into a busy worker
  whose turn did not take alt+m is held `pending: after_turn`. Paste/Enter specifics for claude/codex beyond
  `hc send` (text + Enter) / `pane.send_input(text, keys)`: no source found — UNVERIFIED (bracketed paste, multi-line).

### 9d. Sign-in probe (missing from interface; add as op 8 or fold into Models)
- claude `claude auth status` (JSON with `"loggedIn"`), codex `codex login status` (`Logged in` / `Not logged in`)
  (takeover.py:25-26, settings.py:760-763); agy: login command is just `agy` (settings.py:721) — no status probe seen.
- Suggest `Ready(ctx) (installed, signedIn bool, why string)` as an 8th op, or return it from `Models`.

### 9e. Fixtures available for screen classifiers
factory/test/fixtures: claude_screen_t30p.txt, claude_screen_t31s.txt (Claude idle prompt with "auto mode on"
footer), fac78_agy_reviewer_tail.txt, fac78_claude_reviewer_tail.txt, codexbar/{antigravity,claude,codex}.json
(codexbar JSON shapes), codex-code-mode/. No limit-hit screen seen; open Q7 stays open.
