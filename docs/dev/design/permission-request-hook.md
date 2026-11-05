# Lease-Gated Claude Code PermissionRequest Hook

**Decided (KC, 2026-11-05):** install by default, with an opt-out.

## Decision needed

1. **Install during `sheepdog setup`: default-on or opt-in?**
   - **Option A (Recommended): Install by default whenever `claude` is on `PATH`** (opt out with `sheepdog setup --no-hooks` or `hooks.claude_permission = false` in [CONFIG.md](../../../CONFIG.md)). When the daemon is stopped — or when `claude` runs outside a tracked `herdr` pane — the hook exits `0` in under 1 ms with empty output and changes nothing. Default-on gives phone and voice users full diffs, plans, and untruncated commands without hunting for a hidden flag.
   - **Option B: Opt-in (`hooks.claude_permission = true`).** Leaves `~/.claude/settings.json` untouched unless requested, at the cost of most users staying on 40-line screen-scraping.
2. **When should a running daemon hold a Claude `PermissionRequest` instead of letting Claude draw its terminal menu immediately?**
   - **Option A (Recommended): Hold whenever the daemon is running, `hooks.claude_permission` is enabled, and the pane is a tracked worker** — unless `--voice=off` is set *and* no phone is paired *and* no `herdr-app` panel is installed. Even when no phone is connected right now, APNs push notifications and opening the phone a few seconds later need the structured hook request waiting; anyone sitting at the Mac can release it with one tap (`Answer in terminal` in the `herdr-app` panel or phone) or cancel with `Ctrl+C` in the pane.
   - **Option B: Hold only while a live `/ws` voice session or SSE client (`/api/events`) is actively connected.** Keeps the terminal menu native whenever all clients are closed, but loses structured diffs and plans on prompts that trigger a wake-up push notification while the phone is locked.
3. **When the screen-scrape fallback (`Codex`, `agy`, or unhooked Claude) sees a command cut off by the 40-line window, what should happen?**
   - **Option A (Recommended): Keep fallback approvals working, mark `command_truncated: true`, show `"Command truncated — open terminal for full text"` on the card, and say `"a multi-line <driver> command"` instead of reading a partial line as if it were complete.** Preserves hands-free Codex workflows today while making truncation explicit.
   - **Option B: Block voice approval when `command_truncated` is true and require phone live-terminal review or answering in the terminal.** Stricter, but interrupts hands-free Codex sessions on long test or build commands.

The rest of this document assumes **Option A** on all three choices.

---

## Why screen-scraping is not enough

Today ([architecture.md](../architecture.md), backlog item 16 in [ideas-backlog.md](../ideas-backlog.md)), `herdr.ParsePrompt` reads the last 40 lines of a blocked pane after a 3 s debounce:

- **Truncation and missing tool inputs:** `Bash` commands over ~35 lines lose their top lines; `Edit`, `MultiEdit`, `Write` diffs, `ExitPlanMode` plans, and MCP tool arguments are either scrolled off or rendered in forms `claudeCommand` does not parse.
- **Fragile multi-step menus:** `AskUserQuestion` with multiple tabbed questions, `multiSelect` checkboxes, or free-text `"Type something."` cannot be answered reliably without multi-keystroke navigation races; `"Chat about this"` is dropped (`isChatOption`).
- **Blind declines:** saying *"No, run just the auth package tests instead"* can only send `Esc`, followed by a separate prompt turn after the worker lands back at `idle`.
- **3-second debounce tax:** the user waits 3 s for screen output to settle before Sheepdog even reads the question.

Claude Code's `PermissionRequest` hook passes the full structured tool call on `stdin` and accepts a structured JSON decision on `stdout` before drawing the terminal menu. Keeping `herdr.ParsePrompt` beside it covers Codex, `agy`, unhooked Claude sessions, first-run folder trust prompts, and any prompt released to the terminal.

## Architecture and lease

While a `PermissionRequest` hook process is running, **Claude Code hides its interactive approval menu in the terminal**. A naive hook that blocks on an unreachable or wedged daemon freezes the terminal session.

Sheepdog avoids external shell, `jq`, or Python scripts by shipping the hook inside the existing Go binary as `sheepdog hook claude-permission`, gated by a **two-layer lease** in `<data_dir>` (`~/.local/share/sheepdog`, mode `0700`):

```text
Claude Code ──stdin JSON──► sheepdog hook claude-permission
                                │  1. check $HERDR_PANE_ID / $HC_TERMINAL_ID (else exit 0)
                                │  2. stat/read <data_dir>/hook.lease (TTL 15s; else exit 0)
                                │  3. connect <data_dir>/hook.sock (500ms dial timeout)
                                ▼
                          sheepdog daemon (internal/hook + internal/supervisor)
                                │  keepalive every 5s; EOF / release / decision
                                ▼
Claude Code ◄─stdout JSON / empty─┘
```

1. **Environment pre-filter (< 0.5 ms, no disk I/O):** `sheepdog hook claude-permission` checks `$HERDR_PANE_ID` and `$HC_TERMINAL_ID`. If both are empty, `claude` is running in plain Terminal, iTerm, Ghostty, or Sheepdog's own scrubbed `internal/cli` runner — the hook prints nothing and exits `0` immediately.
2. **File lease (`<data_dir>/hook.lease`, mode `0600`):** while the daemon is running and accepting hook requests, it atomically writes `hook.lease` (`{pid, socket, instance_id, expires_at_unix}`) every **5 s** with a **15 s** TTL, *only after* an in-process health ping confirms the `Supervisor` loop is responsive, and deletes `hook.lease` on clean shutdown. If `hook.lease` is missing, unreadable, malformed, or expired, the hook prints nothing and exits `0` without dialing a socket.
3. **Live socket hold (`<data_dir>/hook.sock`, mode `0600`):** the hook dials `hook.sock` (500 ms connect timeout) and sends one NDJSON request containing Claude's raw `stdin` JSON plus `{pid, ppid, herdr_pane_id, herdr_socket, hc_terminal_id, hc_socket}`.
   - The daemon verifies that the socket path matches its own backend (`HERDR_SOCKET_PATH` or `HC_SOCKET`), the pane is tracked (and is **not** `Supervisor.OwnPane`), and `tool_name` is an interactive permission or question. Otherwise it replies `{"action":"release"}` immediately.
   - While holding the request open, the daemon writes `{"action":"keepalive"}` every **5 s** (driven by the same supervisor health check). The hook refreshes a **15 s** read deadline on each keepalive.

## Failure modes: never hang, never auto-approve

In Claude Code's hook contract:
- **Empty `stdout` + exit `0`** means *no hook decision*: Claude Code immediately renders its normal TUI approval menu in the terminal.
- **Exit `2`** aborts the tool call with a hook error instead of showing the menu.
- **`behavior: "allow"`** executes the tool without asking in the terminal.

**Invariant:** `sheepdog hook claude-permission` has no code path that synthesizes `behavior: "allow"` locally, and never exits non-zero. Every error, timeout, signal, or disconnect prints zero bytes to `stdout` and exits `0`:

| Situation | What happens | Time to terminal menu |
| --- | --- | --- |
| Daemon is not running | `hook.lease` missing or expired (`> 15 s`); hook exits `0` with empty `stdout` | `< 2 ms` |
| `claude` run outside `herdr` (or in `internal/cli`) | Neither `HERDR_PANE_ID` nor `HC_TERMINAL_ID` is set; hook exits `0` | `< 1 ms` |
| Different `herdr`/`hc` server, `OwnPane`, or untracked shell pane | Daemon replies `{"action":"release"}`; hook exits `0` | `< 5 ms` |
| Daemon crashes (`SIGKILL` / panic) mid-wait | Kernel closes `hook.sock` immediately; hook reads `EOF` before any reply and exits `0` | `~0 ms` |
| Daemon stops cleanly (`SIGTERM`, restart, `sheepdog uninstall`) | Daemon broadcasts `{"action":"release"}` to all open hook connections, unlinks `hook.lease`, closes `hook.sock` | `~0 ms` |
| Daemon process freezes / deadlocks while alive | Keepalives stop; hook's `15 s` read deadline fires and exits `0` | `<= 15 s` |
| User sits for a long time without answering | Hook refreshes keepalives up to `1790 s` (`10 s` short of `settings.json`'s `timeout: 1800`), then exits `0` cleanly so Claude draws the menu instead of timing out the hook | `29m 50s` |
| User presses `Ctrl+C` or `Esc` in the terminal | Claude signals the hook (`SIGINT`/`SIGTERM`/`SIGHUP`); hook closes `hook.sock`, daemon sees `EOF` and clears the card as `"Cancelled in terminal"` | `~0 ms` |
| User taps `"Answer in terminal"` on phone/panel (or asks by voice) | Daemon sends `{"action":"release"}`; hook exits `0` and Claude draws its TUI menu | `< 20 ms` |
| Corrupt `stdin`, disk full, permission error on `<data_dir>` | Hook catches the error, writes nothing to `stdout`, exits `0` | `< 2 ms` |

## Install, doctor, and removal

### Where it is installed

Claude Code reads user-level hooks from `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PermissionRequest": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/opt/homebrew/bin/sheepdog hook claude-permission",
            "timeout": 1800
          }
        ]
      }
    ]
  }
}
```

### `sheepdog setup` and binary upgrades

- **Stable binary path:** `internal/onboard` resolves a stable path (`/opt/homebrew/bin/sheepdog` or `/usr/local/bin/sheepdog` when symlinked from Homebrew Cellar, never a versioned `/Cellar/sheepdog/0.x.y/bin/sheepdog` path that breaks on `brew upgrade`).
- **Surgical JSON merge:** reads `~/.claude/settings.json` (creating `~/.claude` with `0700` only if `claude` is installed and the user enabled the hook), preserves every existing top-level key and every non-Sheepdog entry in `hooks.PermissionRequest` (identified by matching `hook claude-permission` in `command`), updates the path in place if Sheepdog moved, writes a `.bak` backup beside `<config_dir>/config.toml`, and atomically renames `~/.claude/settings.json.tmp` into place.
- **Config key ([CONFIG.md](../../../CONFIG.md)):** `hooks.claude_permission` (`bool`, default `true`; env `SHEEPDOG_HOOK_CLAUDE_PERMISSION`). Setting it to `false` stops the daemon from renewing `hook.lease` immediately (and `sheepdog setup` removes the entry from `~/.claude/settings.json`).

### `sheepdog doctor`

Adds a `claude-hook` check in `internal/onboard/doctor.go`:
- **OK:** hook installed in `~/.claude/settings.json` and its binary path exists and matches the running `sheepdog` version.
- **Warn:** `claude` is installed and `hooks.claude_permission = true`, but the hook is missing or points to a moved/deleted `sheepdog` binary (fix: `run sheepdog setup`). Note: even with a stale binary path, Claude Code treats a missing hook command as non-blocking unless exit `2` is returned, and `doctor` surfaces the exact fix.

### `sheepdog uninstall`

Extends `cmd/sheepdog/uninstall.go` (before removing `<config_dir>` or `<data_dir>`):
- Always inspects `~/.claude/settings.json` — **even when `--keep-data` is passed** — so uninstalling Sheepdog never leaves a dangling hook entry behind in Claude Code.
- `--dry-run` prints `would remove sheepdog PermissionRequest hook from ~/.claude/settings.json` without modifying the file.
- Strips only matcher entries whose `command` invokes `sheepdog hook claude-permission`. If `PermissionRequest` becomes empty, deletes that key; if `hooks` becomes empty, deletes `hooks`; leaves all other `~/.claude/settings.json` keys intact.

## Interaction with the approval rail

All safety rails stay in Go code in `internal/supervisor` ([roles.md](../roles.md)) — never in prompts and never in the hook subprocess.

### Same rail as `send_keys`

A hook-backed prompt creates a `state.PendingAction` (`Source: "hook"`, `HookID: reqID`) in `Table.Questions` and `Table.PendingAction`, using the same announcement, muted-space holding, `authorise_window_s` (30 s), single-use input (`in.used`), and [Approval Rail v2](approval-rail-v2.md) `told` rules as screen-scraped prompts:

1. **Canonical spoken text (`exactPromptText(p)`):** built deterministically in `internal/supervisor/pending.go` so speech stays short while cards hold full detail:
   - `Bash`: `"<agent> is waiting for approval: Run <spokenCommand>? Yes or No."`
   - `Edit` / `MultiEdit` / `Write`: `"<agent> is waiting for approval: Edit <basename> (+14, -3 lines)? Yes or No."` (or `"Create <basename> (48 lines)?"`)
   - `ExitPlanMode`: `"<agent> is waiting for approval on its plan: <first heading or 80-char summary>. Approve plan or No?"`
   - `AskUserQuestion`: `"<agent> is asking: <question>"` plus numbered option labels (for multi-question forms, `"<agent> has <N> questions starting with: <question 1>"`).
2. **Positive delivery (`told`) and bare answers:** a bare `"yes"`, `"no"`, or `"option 2"` authorises a hook answer only when `p` became `told` before `in.began` (via `EventPromptPlayed` on voice or verbatim `exactPromptText(p)` on `POST /api/turn`) and is not displaced. Naming the agent (`"yes, approve auth"`) or tapping (`POST /api/pending/answer`) works without `told`, subject to the same-pane 30 s prompt-change guard in [approval-rail-v2.md](approval-rail-v2.md).
3. **Negative-input guard:** `negativeInput` (`"no"`, `"stop"`, `"wait"`, `"don't"`, `"hold on"`, `"cancel"`, etc.) **never** produces `behavior: "allow"` on any path, even if the orchestrator mis-maps an option index.

### Richer answers the hook unlocks

Once the rail authorises the user's input for `p`, `Supervisor` resolves the waiting socket request instead of calling `Backend.SendKeys`:

- **Allow once (`Yes`):** sends `decision: {"behavior": "allow"}`.
- **Scoped `"Always allow"` (when Claude sent `permission_suggestions`):** exposed as an explicit option matching Claude's own suggestion (for example, `"Yes, and allow edits in this session"`), sending `decision: {"behavior": "allow", "updatedPermissions": [...]}`. Voice fast-path `"yes"` always picks **allow once**, never session-wide `updatedPermissions`, unless the user explicitly chooses that option number or taps it.
- **Decline with reason (`"No, use the existing helper in internal/auth"`):** `answer_prompt` (and `POST /api/pending/answer`) accepts an optional `message` on negative answers. Instead of blind `Esc`, the daemon sends `decision: {"behavior": "deny", "message": "<user's reason>", "interrupt": false}` so Claude adjusts immediately in the same turn. Plain `"No"` sends `{"behavior": "deny", "message": "Declined by the user.", "interrupt": true}`.
- **`"Chat about this"` on `AskUserQuestion`:** kept on hook-backed questions instead of filtered out; sends `{"behavior": "deny", "message": "The user wants to discuss this question first; pause and wait for their message.", "interrupt": true}`.
- **Multi-question, `multiSelect`, and `"Other"` (`AskUserQuestion`):** answered atomically in one JSON payload (`decision: {"behavior": "allow", "updatedInput": {"questions": ..., "answers": {...}}}`), with zero arrow/tab/typing races.
- **`"Answer in terminal"` (`release: true`):** writes `{"action":"release"}` to `hook.sock` and records `releasedSig[paneID] = promptSignature` for 60 s. When Claude immediately renders the same prompt on screen as a TUI menu, `Watcher` / `reconcileReprompts` sees matching `releasedSig` and marks the screen prompt `Held` (quiet, no duplicate push or voice announcement), while keeping it answerable if the user changes their mind.

### Redaction, provenance, and prompt liveness

- **Untrusted worker content:** `tool_input` (`command`, `old_string`, `new_string`, `content`, `plan`, `questions`, MCP args) comes from the worker and workspace files. Before reaching the orchestrator, every string passes through `Redactor.String` and `<<<BEGIN_WORKER_OUTPUT>>>` provenance fencing ([architecture.md](../architecture.md)). `SpokenSummary` continues to strip secrets and shell noise via `EarLine` / `spokenCommand`.
- **Reconciler (`internal/supervisor/reprompt.go`):** while a hook connection is open, Claude has not drawn a TUI menu on screen, so `herdr.ParsePrompt(screen)` returns `nil`. For `p.Source == "hook"`, `stillAsking`, `checkOpenPrompt`, and `checkQuestion` check `hookRegistry.IsOpen(p.HookID)` (live socket connection) instead of scraping the terminal viewport. The instant the socket closes (answered, released, or `Ctrl+C` in terminal), the registry fires an event that clears `p` from `Questions` and `PendingAction`.

## Both `herdr` backends and Codex

### `herdr-tui` (`herdr.backend = "herdr"`)

- **Pane matching:** `herdr-tui` sets `HERDR_PANE_ID` (for example `w5:p1`) and `HERDR_SOCKET_PATH` in every pane's environment. If a wrapper stripped `HERDR_PANE_ID`, the daemon falls back to matching the hook's ancestor PIDs against `pane.process_info` (`ShellPID` / `ForegroundProcessGroup`) or `session_id` against `AgentInfo.Session`.
- **State transition without 3 s debounce:** `herdr-tui` infers `blocked` partly from screen heuristics. Because the hook runs *before* Claude draws its TUI menu, `herdr-tui` may still report `agent_status: "working"`. An accepted `hook.sock` request transitions the tracked worker in `Supervisor` to `blocked` immediately (`0 ms` debounce) and suppresses any duplicate `blocked` transition if `herdr-tui` reports `blocked` moments later. When the hook resolves with `allow` or non-interrupt `deny`, `Supervisor` returns the worker to `working` until `herdr-tui`'s next status event.

### `herdr-core` (`herdr.backend = "hc"`)

- **Terminal matching:** `herdr-core` ([herdr-core-backend.md](herdr-core-backend.md)) sets `HC_TERMINAL_ID` (`t7`, which is already `AgentInfo.PaneID` in `internal/herdrcore`) and `HC_SOCKET`.
- **OSC 7501 deduplication and lifecycle:** Claude Code's `herdr-core` integration emits OSC 7501 status updates (`blocked` with `kind: permission|question`). When an open hook request is already present on `t7`, `Supervisor` ignores the redundant screen-scrape trigger from OSC 7501 `blocked`. Conversely, if `herdr-core` emits `terminal.removed`, `finished`, or transitions `t7` from `blocked` back to `working`/`idle` while a hook is open, `hookRegistry` releases and closes the stale hook entry immediately.

### Codex, `agy`, and screen-scrape coexistence

- **Codex and `agy`:** Codex CLI currently exposes `SessionStart` hooks (`~/.codex/hooks.json`) without a `PermissionRequest` decision hook; `agy`, unhooked Claude sessions, and Claude's pre-session folder trust dialog also have no `PermissionRequest` hook. All of them continue using `herdr.ParsePrompt` unchanged.
- **Per-pane precedence rule:**
  1. If `hookRegistry` has an open request on `paneID`, it owns `Questions[paneID]`; `onBlocked` and `reconcileReprompts` skip `ParsePrompt` for that pane.
  2. Otherwise, `ParsePrompt` runs on `blocked` transitions exactly as today.
  3. If `ParsePrompt` sees `ReadResult.Truncated` or a command block touching the top of the 40-line read window, it sets `CommandTruncated: true` on `PendingAction` (per Decision 3).

## What shows on the phone (and panel)

### Additive wire fields (`GET /api/state`, `pending.created`, `POST /api/pending/answer`)

Older iPhone builds and `/voice` keep working without a protocol bump because existing `summary`, `question`, `command`, and 1-based `options[]` are always populated. New optional fields on `PendingView`:

```json
{
  "id": "pa_9f3c",
  "pane_id": "t7",
  "agent": "auth-claude",
  "kind": "answer_prompt",
  "source": "hook",
  "tool_name": "Edit",
  "detail_kind": "diff",
  "summary": "auth-claude is waiting for approval: Edit middleware.go (+14, -3 lines)?",
  "command": "internal/auth/middleware.go (+14 -3)",
  "file_path": "internal/auth/middleware.go",
  "diff": "@@ -41,5 +41,16 @@ ...",
  "detail_truncated": false,
  "can_deny_reason": true,
  "can_release": true,
  "options": [
    { "index": 1, "label": "Yes", "default": true },
    { "index": 2, "label": "Yes, allow all edits this session" },
    { "index": 3, "label": "No" }
  ]
}
```

- Payload cap: `command`, `diff`, and `plan` are capped at **64 KB / 1,500 lines** after redaction (`detail_truncated: true` if clipped, with a button to open the live file in [file-browser.md](file-browser.md) or terminal).
- For multi-question `AskUserQuestion` (`detail_kind: "questions"`), `form_questions[]` carries `[{header, question, multi_select, options: [{label, description, recommended}]}]`.
- `POST /api/pending/answer` keeps `{"id", "option"}` and adds optional `{"message": "...", "answers": {...}, "release": true}`:
  - `release: true` releases the hook to the terminal (`200 {"ok": true, "released": true}`).
  - `message` attaches the user's feedback when `option` is negative (`No`).
  - `answers` submits multi-question / `multiSelect` / `"Other"` answers in one tap.

### iPhone (`ApprovalCardView` + detail sheet) and `herdr-app` panel

Every card keeps fast one-tap primary buttons on the compact floating card (`ApprovalCard.swift`) while making the full payload one tap away:

1. **`Bash` (`detail_kind: "command"`):** shows Claude's `description` caption (when provided) above a 4-line monospaced command preview. When the command exceeds 4 lines, a `"Show full command (N lines)"` button opens a scrollable monospaced sheet with copy support — never cut at 40 terminal lines.
2. **`Edit` / `MultiEdit` / `Write` (`detail_kind: "diff"`):** shows the workspace-relative path (`internal/auth/middleware.go`), `+added / -removed` counts, and a 6-line unified diff preview using the same green/red diff tokens as the Agent `Changes` tab. Tapping the preview opens the full scrollable diff sheet (`Write` shows the new file with `+` lines).
3. **`ExitPlanMode` (`detail_kind: "plan"`):** shows a `"Plan ready to approve"` header, a 5-line preview, and `"Read full plan"`, which opens the full plan rendered with `MarkdownRenderer` (`SheepdogKit`).
4. **`AskUserQuestion` (`detail_kind: "questions"`):**
   - Single-select single question keeps today's direct one-tap option rows, plus `"Other…"` (inline text field) and `"Chat about this"`.
   - Multi-question or `multiSelect: true` renders compact stacked sections (or a `1 of N` stepper on small screens) with checkboxes, an `"Other…"` row per question, and a single `"Send answers"` primary button.
5. **Decline with reason (`can_deny_reason: true`):** beside `"No"`, a compact `"No, tell it why…"` pill expands a single-line text field (with dictation mic support) and sends `option: <No>, message: text`.
6. **`"Answer in terminal"` (`can_release: true`):** shown as a quiet secondary action at the bottom of the card and inside the detail sheet (`terminal` SF Symbol). Tapping it releases the hook immediately and shows a transient `"Moved to terminal"` confirmation.
7. **First-time user, empty/error, and accessibility checks:**
   - If the question was already answered or cancelled in the terminal when the user taps (`409`), the card dismisses cleanly with `"Already answered in terminal"`.
   - When `command_truncated` is true on a screen-scraped fallback card, an amber `"Truncated to last 40 terminal lines — check Live Terminal"` banner appears above the buttons.
   - Full VoiceOver labels (`"Edit diff for middleware.go, 14 additions, 3 deletions, button, double-tap to read full diff"`, `"Answer in terminal, moves the approval menu back to your Mac"`), Dynamic Type layout switching buttons from horizontal to vertical stack at accessibility sizes without clipping, and dark-mode contrast checks on diff lines.

## Test and eval plan

1. **Hook binary & lease (`internal/hook`, `cmd/sheepdog`):**
   - Missing env vars, missing `hook.lease`, expired `hook.lease`, corrupt `hook.lease`, closed `hook.sock`, daemon crash (`conn.Close()` without reply), stalled keepalive (`readDeadline` expiry), `SIGINT`/`SIGTERM`, and `"release"` reply all exit `0` with `0` bytes on `stdout` and never touch real `~/.config/sheepdog` or `~/.local/share/sheepdog`.
   - Round-trip tests for `Bash`, `Edit`, `MultiEdit`, `Write`, `ExitPlanMode`, single/multi `AskUserQuestion`, `permission_suggestions` -> `updatedPermissions`, and `deny` with `message`.
2. **Onboarding, doctor, and uninstall (`internal/onboard`, `cmd/sheepdog`):**
   - Merge into absent, empty, and user-customized `~/.claude/settings.json` inside `t.TempDir()`; idempotent re-run; binary path update; `doctor` stale-path warning; `uninstall --dry-run` and `uninstall --keep-data` removing only Sheepdog's hook entry.
3. **Supervisor & approval rail (`internal/supervisor`):**
   - Hook-backed `PendingAction` obeys `railInputLocked`, `negativeInput`, muted-space holding, `authorise_window_s`, and [Approval Rail v2](approval-rail-v2.md) `told` (`EventPromptPlayed` / `POST /api/turn` `exactPromptText`).
   - Fast-path `"yes"` never selects session-wide `updatedPermissions`; negative voice/typed input with feedback sends `deny.message`; `"Answer in terminal"` suppresses re-announcing the resulting screen-scraped TUI prompt; `reconcileReprompts` keeps hook-backed prompts alive while the terminal shows no menu and clears them on socket `EOF`.
   - Redaction and `<<<BEGIN_WORKER_OUTPUT>>>` provenance verified on secrets inside `tool_input` (`Bash` env assignments, `Edit` diffs, `Write` content).
4. **Backend conformance (`herdrtest` and `hctest`):**
   - `herdr-tui`: hook arrival while pane is `working` transitions worker to `blocked` with `0 ms` debounce and deduplicates subsequent `blocked` event.
   - `herdr-core`: `HC_TERMINAL_ID` matches `t7`, deduplicates OSC 7501 `blocked`, and closes open hook on `terminal.removed` or return to `idle`.
   - Unhooked Codex and `agy` panes in the same session continue through `herdr.ParsePrompt`.
5. **Clients, docs, and eval gates:**
   - `make test` (including `go test ./internal/docscheck`), `make test-web` (`web/voice/cards.test.mjs` and `mods/sheepdog` question card tests), `make vet lint`.
   - iOS simulator (`xcodebuild -scheme Sheepdog -destination 'platform=iOS Simulator,name=iPhone 17 Pro' -skipPackagePluginValidation test`): `StubDaemon` unit and `-sheepdogNoAnimations` UI tests covering untruncated `Bash` sheet, `Edit` diff sheet, `ExitPlanMode` Markdown sheet, multi-question `AskUserQuestion`, `"No, tell it why…"`, `"Answer in terminal"`, and `409` conflict handling.
   - Evals ([eval README](../eval/README.md)): `sheepdog eval --set all` offline gate (0 unintended actions) plus new sim scenarios for hook `deny.message`, `ExitPlanMode` summary, and `AskUserQuestion` multi-select, followed by **>= 15 live runs of `workday-scale-ten`** confirming zero regressions on `no_unintended_action` and `right_agent`.
