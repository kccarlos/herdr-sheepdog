# herdr API review: what sheepdog uses and what it leaves on the table

Date: 2026-10-06. Handover queue item 4 ("Night of 2026-10-06").

## Scope and method

- herdr source: `https://github.com/herdrdev/herdr` cloned to
  `/Users/kc/Documents/herdr-src`, checked out at tag **v0.9.3** (latest
  release, 2026-09-29). References below of the form `src/...` are into
  that checkout at v0.9.3.
- sheepdog: `origin/main` at 5574404. References of the form `internal/...`
  are into this repo.
- Everything comes from reading source, the bundled docs
  (`docs/next/website/src/content/docs/*.mdx`) and the installed CLI's
  `--help`. **Nothing was verified live:** the CEO's herdr server was not
  running during the review (`herdr status`: server not running), and the
  review did not start one. Every proposal that changes behaviour needs a
  check in a bench named session (`internal/bench/tier3`) before it ships.

## Version gap

- Installed: `herdr 0.9.3` (protocol 22). Latest release: `v0.9.3`.
  **No gap; no upgrade is needed for anything proposed here.**
- `main` is 46 commits past v0.9.3; its CHANGELOG `## Unreleased` is empty.
  The unreleased commits worth watching for the next tag:
  - `fabcab10` fix: detect agy dialogs and mid-turn work, treat background
    work as idle for agy and grok (#4959). sheepdog runs agy workers, so this
    should make herdr's `blocked` for agy approval dialogs trustworthy.
  - `950d012c` fix: keep agent sessions while the agent is suspended or
    backgrounded (#4964): `agent_session` survives a Ctrl-Z.
  - `07e3840b` fix: restore codex idle detection.
  - `c4653a4f` / `3d9d2b18`: server stays up on hangup / failed thread spawns.
- Relevant changes in 0.9.2 (already installed) that sheepdog has not adapted
  to: event subscriptions now end with an `events_lost` error instead of
  silently dropping events (see P2); new panes drop inherited Claude/Codex
  session markers and set `TERM_PROGRAM=herdr`; agents can self-report a
  resume command.

## herdr's API surface (v0.9.3)

Socket methods are listed in `src/api/schema.rs:50-251`; the human reference
is `socket-api.mdx:96-113`.

| Area | Methods / commands | sheepdog uses |
|---|---|---|
| Session | `ping`, `session.snapshot` | both |
| Workspace | `workspace.create/list/get/focus/rename/move/move_block/report_metadata/close` | `create` |
| Tab | `tab.create/list/get/focus/rename/move/close` | none |
| Layout | `layout.export`, `layout.apply` (panes may carry `command` argv), `layout.set_split_ratio` | none |
| Pane | `pane.split` (cwd, env, no command), `pane.read` (sources visible/recent/recent_unwrapped/detection), `pane.send_text/send_keys/send_input`, `pane.rename`, `pane.close`, `pane.process_info`, `pane.wait_for_output`, `pane.report_agent`, `pane.report_agent_session`, `pane.release_agent`, `pane.clear_agent_authority`, `pane.report_metadata`, plus layout/focus/scroll/copy helpers | `split`, `read`, `send_input`, `send_keys`, `close` |
| Agent | `agent.list/get/read/explain/send_keys/rename/focus/start/prompt/wait`, `agent.view.set/clear` | `list`, `get`, `prompt`, `rename` |
| Events | `events.subscribe` (lifecycle kinds, `pane.agent_status_changed`, `pane.output_matched`, `pane.scroll_changed`), `events.wait` | `subscribe` |
| Notification | `notification.show` | none |
| Integrations | `integration.list/install/uninstall` (hooks for claude v10, codex v8, antigravity-cli v3 installed on this Mac) | none (installed by hand) |
| Plugins | `plugin.*` incl. `plugin.pane.open` | none |
| Raw terminal stream (client socket, not API socket) | `herdr terminal session observe <target>` / `control` | none |

Facts that bear on the proposals:

1. **No pane-with-command on split/create.** `PaneSplitParams`
   (`src/api/schema/panes.rs:18-35`), `WorkspaceCreateParams`
   (`src/api/schema/workspaces.rs:8-20`) and `TabCreateParams`
   (`src/api/schema/tabs.rs:8-19`) take cwd and env only. The one API that
   starts a pane with a program is `layout.apply`: `LayoutPane.command:
   Option<Vec<String>>` (`src/api/schema/panes.rs:194-205`), launched as argv
   with no shell (`spawn_argv_command`, `src/pane.rs:2312-2355`), agent
   detection enabled (`src/workspace/tab.rs:146`, `:371`), `HERDR_*` env still
   injected (`socket-api.mdx:245-249`). `layout.apply` always builds a **new
   tab** (`socket-api.mdx:210-213`; `src/app/api/layouts.rs:101-111`,
   `:415-421`).
2. **`agent.start` still types into the shell.** It checks the pane's
   foreground process is an interactive shell, quotes the argv for that
   shell, marks the terminal as a managed launch under the given name, then
   sends the command line as input (`src/app/agents.rs:145-232`, the send at
   `:218`). A name is mandatory (lowercase, 1-32 chars) and
   `agent.rename` is refused while the managed launch is pending
   (`src/app/agents.rs:124`). The readiness wait is in the CLI, not the socket
   method (`src/cli/agent.rs:289-460`, `wait_for_named_agent` at `:562`).
3. **Shell readiness check.** The CLI's `agent start` retries while the pane's
   shell is still initialising, using `pane.process_info`: foreground process
   group == shell pid and the foreground process is the shell
   (`src/cli/agent.rs:652-680`).
4. **There is no push event for "output changed".** `pane.output_changed`
   exists as an `EventKind` and `EventMatch` (`src/api/schema/events.rs:173`,
   `:216`, `:247`) but nothing emits it (only `src/app/api/plugins/context.rs:161`
   and a plugin test that asserts it does not run). `events.wait` supports
   only `pane_agent_status_changed` and refuses everything else with
   `unsupported_event_wait_match` (`src/api/wait.rs:759-779`).
   `pane.output_matched` is server-side polling of `pane.read` every 100 ms
   (`src/api/subscriptions.rs:361-395`, `CONNECTION_POLL_INTERVAL` at
   `src/api/server.rs:29`) and is edge-triggered on match, so a catch-all regex
   fires once and never again. `pane.read` always returns `revision: 0`
   (`src/app/api/panes.rs:1540`, `src/app/api/agents.rs:252`), so
   `ReadResult.Revision` (`internal/herdr/protocol.go:194`) cannot be used to
   skip unchanged reads. The comment in `internal/web/screen.go:17-24` (#151)
   is therefore correct.
5. **The raw terminal stream is push.** `herdr terminal session observe
   <pane|terminal|agent> [--cols N --rows N]` prints one NDJSON
   `terminal.frame` per output frame (`seq`, `width`, `height`, `full`,
   base64 ANSI `bytes`) and `terminal.closed` at the end
   (`src/client/terminal_sessions.rs:18-23`, `:123-158`; CLI spec
   `src/cli/spec.rs:702-730`). Observers take no input, resize, scroll or
   takeover authority; several may watch one terminal; an observer whose
   socket makes no write progress for 30 s is dropped, possibly without
   `terminal.closed` (`cli-reference.mdx:395-402`). It speaks the client
   socket's binary framing, not the NDJSON API, so the CLI subprocess is the
   practical way in.
6. **`events_lost`.** Since 0.9.2 a subscriber that falls behind gets an error
   response with code `events_lost` and the connection is closed; the
   documented recovery is resubscribe and reconcile with `session.snapshot`
   at once (`socket-api.mdx:795-815`). A per-pane `pane.agent_status_changed`
   subscription without a status filter emits no initial event on
   (re)subscribe (`src/api/subscriptions.rs:206-236`), so a state change
   during a reconnect gap is not replayed.
7. **Display metadata.** `pane.report_metadata` / `workspace.report_metadata`
   set title, `display_agent`, per-state labels and up to 32 named tokens
   (rendered as `$name` in sidebar rows), with optional TTL; display-only,
   never lifecycle (`socket-api.mdx:725-767`; params
   `src/api/schema/panes.rs:401`, `src/api/schema/workspaces.rs:49`).
8. **Detection introspection.** `agent.explain` returns the matched manifest
   rule, region evidence, visible blocker/working/idle flags and fallback
   reason (`DetectionExplain`, `src/detect/manifest.rs:28-47`;
   `agents.mdx:95-102`). `pane.read --source detection` returns the buffer
   detection ran on. Blocked detection is strict: a prompt shape herdr does
   not know falls back to `idle` (`agents.mdx:73-77`). Local manifest
   overrides can add rules without a herdr release (`agents.mdx:79-93`).
9. **`agent.prompt` can wait.** An optional `wait {until, timeout_ms}`
   submits and starts the wait in one request (`socket-api.mdx:114`;
   `src/api/wait.rs:177`). sheepdog sends no `wait`
   (`internal/herdr/client.go:172`).
10. **Lifecycle authority** (`report-agent`, `report-agent-session`,
    `release-agent`) belongs to the agent's own integration. The installed
    Claude hook (v10) only reports session identity at `SessionStart`; state
    comes from screen manifests. sheepdog should keep out of it: claiming
    authority would make sheepdog's code the judge of a worker's state.

## What sheepdog does now

- Client: one connection per request (`internal/herdr/client.go`), methods
  listed in the table above. Fleet discovery is one lifecycle subscription
  plus one `pane.agent_status_changed` subscription per agent pane
  (`internal/herdr/events.go`), reconciled with a `session.snapshot` diff
  every 30 s (`internal/herdr/watcher.go:67`, `:242`).
- Spawn (`internal/supervisor/spawn.go`): `pane.split` with
  `target_pane_id` from a pane already in the directory, else
  `workspace.create` (`:153-158`); both with `PaneEnv`
  `DISABLE_AUTO_UPDATE=true` (`internal/herdr/client.go:217`) after
  oh-my-zsh's update question ate the "c" of `claude` on 2026-10-04.
  `waitShell` waits up to 5 s for any non-empty text in the pane
  (`:405-418`), then `pane.send_input "<kind> [--model M]" enter` (`:168`).
  `followSpawn` polls the state table every 1 s; after 20 s
  (`spawnReadyWait`, `:71`) the brain is told the agent has not started.
  `agent.start` is deliberately not used because the name it requires would
  read as the CEO's own name (`:34-40`; `state.CEOName`,
  `internal/state/spoken.go:55`).
- Prompts: when herdr says `blocked` (or on a revive), the pane is read and
  `herdr.ParsePrompt` (`internal/herdr/prompt.go:143`) extracts question and
  options in code.
- Agent window (#151): one subscribed pane is read every 1 s, sooner on a
  `herdr.transition` or `herdr.send` for it, at least 250 ms apart; a frame is
  sent only when the hash changes (`internal/web/screen.go`,
  `internal/web/server.go:167-172`).
- Names: `agent.rename` with the herdr spelling of the spoken name
  (`internal/supervisor/names.go:154`); renames from herdr's UI are found in
  the snapshot diff.
- Close: `pane.close` (`internal/supervisor/close.go:72`).
- Bench: a named herdr session per run via the CLI (`herdr --session <name>`),
  never the default session (`internal/bench/tier3/herdr.go`).

## Proposals, ranked by value

Labels follow the project principle (the harness provides information at the
right time and prevents runaways; judgment stays with the models): **rail**
(a guard or mechanism that makes an action happen correctly), **information**
(gives a model or the CEO facts it lacked), **bug-fix**. None of these puts a
judgment in code. None needs a herdr upgrade.

### P1. Shell readiness from `pane.process_info` instead of "any text" (bug-fix, S)

- herdr: `pane.process_info` (`src/api/schema/panes.rs` `PaneProcessInfo`);
  the readiness rule herdr itself uses, `src/cli/agent.rs:652-680`.
- Now: `waitShell` returns as soon as the pane shows any text
  (`internal/supervisor/spawn.go:405-418`). A shell that has printed its
  first line but is still running rc files (oh-my-zsh update check, nvm
  lazy-load) passes, and the typed command can be eaten; `PaneEnv` covers only
  the one case seen so far.
- Change: poll `pane.process_info` until the foreground process group is the
  shell's and no rc child is in the foreground (same predicate as herdr's
  CLI), then a short settle, then type. Keep `PaneEnv`.
- Benefit: removes the class of "shell ate the command" failures, keeps the
  split-beside-existing-pane UX. Risk: low; worst case the wait times out and
  falls back to today's behaviour.

### P2. Reconcile at once after a subscription drops, including `events_lost` (bug-fix, S)

- herdr: `events_lost` contract, `socket-api.mdx:795-815`; no initial status
  event on resubscribe, `src/api/subscriptions.rs:206-236`.
- Now: `subscribeOnce` hands the `events_lost` error line to the event
  handler, where `ParseEvent` fails silently (`internal/herdr/events.go:278`);
  `subscribeLoop` reconnects with backoff (`:188-220`) but nothing triggers
  `refreshSnapshot`, so a `blocked` that happened during the gap waits for the
  30 s ticker (`internal/herdr/watcher.go:205`), on top of the 3 s debounce.
- Change: recognise an error envelope on the stream (log `events_lost`
  distinctly), and after every successful resubscribe signal the watcher to
  run `refreshSnapshot` immediately (serialised, as the docs prescribe).
- Benefit: an approval prompt raised during a reconnect reaches the CEO in
  seconds instead of up to 30 s. Risk: low; a few extra snapshots.

### P3. Agent window and other screen readers: `terminal session observe` as a change signal (information, M)

- herdr: `src/client/terminal_sessions.rs:18-23`, `:123-158`;
  `cli-reference.mdx:395-402`.
- Now: the subscribed window is read every 1 s whether or not anything
  changed, and a change can be up to 1 s late (`internal/web/screen.go`).
- Change: while a window is subscribed, run `herdr terminal session observe
  <pane_id>` as a child process. Do not decode the ANSI; treat each
  `terminal.frame` as "the screen changed" and schedule the existing
  `pane.read` (keep the 250 ms minimum gap and the hash check). Keep a slow
  fallback tick (e.g. 5 s). Restart the child with backoff when it exits
  (`terminal.closed`, the 30 s stall rule, a herdr restart). Pass `--session`
  when the daemon is pointed at a named session.
- Benefit: the CEO's phone sees changes within ~250 ms and an idle agent costs
  no reads. Can later feed other readers that poll (e.g. followSpawn).
  Risk: one extra process per subscribed window (at most one today); depends
  on a CLI output format that is documented but not part of the versioned
  API schema, so pin it in a test.

### P4. Start the agent as the pane's program with `layout.apply` (rail, M)

- herdr: `LayoutPane.command` (`src/api/schema/panes.rs:194-205`),
  argv spawn without a shell (`src/pane.rs:2312-2355`), detection enabled
  (`src/workspace/tab.rs:146`, `:371`), new tab only
  (`socket-api.mdx:210-237`).
- Now: type `claude`/`codex` into a fresh shell (P1 hardens that).
- Change: `layout.apply {workspace_id, tab_label, focus:false, root:{type:pane,
  cwd, env, command}}` with either the bare argv (`["claude","--model",M]`) or
  a login-shell wrapper (`["zsh","-lic","claude --model M; exec zsh -i"]`) so
  PATH matches the CEO's shell and a shell remains when the agent exits. For a
  directory without a workspace, `workspace.create` then `layout.apply` with
  the root tab's `tab_id` (replaces it).
- Benefit: nothing is typed, so no prompt race, no `waitShell`, no
  `DISABLE_AUTO_UPDATE` dependency; model names never pass through a typed
  command line. Risk: UX change (new tab instead of a split beside the
  existing agent); bare argv uses the herdr server's PATH, not the shell's;
  without the wrapper the pane ends when the agent exits (sheepdog already
  handles `pane.exited`). Needs a bench check that detection and
  `agent_session` reporting behave as for typed starts.

### P5. Show sheepdog's own facts in herdr with `pane.report_metadata` tokens (information, S)

- herdr: `socket-api.mdx:725-767`; `src/api/schema/panes.rs:401`.
- Now: herdr's sidebar knows nothing of sheepdog; a CEO looking at the screen
  cannot see which agent has a pending question, a held first instruction or
  what sheepdog calls it.
- Change: report tokens under source `sheepdog` such as `sd_name` (spoken
  name), `sd_pending` ("question waiting"), `sd_held` ("instruction held"),
  with a TTL so they vanish if the daemon dies; the CEO adds `$sd_pending`
  etc. to `ui` sidebar rows in config. Do not touch `title`/`display_agent`
  or lifecycle fields.
- Benefit: screen and voice tell the same story when the CEO is at the desk.
  Risk: low; display-only by contract. Note it writes to the CEO's live herdr
  UI, so ship behind a config switch.

### P6. `agent.explain` beside `ParsePrompt` when herdr says blocked (information, S)

- herdr: `DetectionExplain` (`src/detect/manifest.rs:28-47`),
  `agents.mdx:73-102`; `pane.read --source detection`.
- Now: when herdr reports `blocked` and `ParsePrompt` does not match, the
  brain gets the screen only.
- Change: in that case also fetch `agent.explain` and pass the matched rule id
  and visible-blocker flag to the brain as facts (no code decision on them);
  log them for prompt-parser fixtures. Optionally read `--source detection`
  so the parser sees the same region herdr matched.
- Benefit: the brain knows what kind of UI herdr saw (permission form,
  question, trust dialog), and parser misses become reproducible test cases.
  Risk: low; one extra request on a rare path.

### P7. `agent.prompt` with `wait`, and `events.wait` for spawn readiness (information, S)

- herdr: `socket-api.mdx:114`, `src/api/wait.rs:177-322` (prompt wait,
  `agent_prompt_stalled` when nothing starts within 5 s), `src/api/wait.rs:697`.
- Now: `Prompt` sends no wait (`internal/herdr/client.go:172`), so "the agent
  never started working on what it was told" is only noticed via the
  debounced watcher; `followSpawn` polls every 1 s.
- Change: send instructions with `wait {until:[working,blocked], timeout_ms:
  ~8000}` and report `agent_prompt_stalled` to the brain as a fact; replace
  the followSpawn poll with `events.wait` on `pane_agent_status_changed`.
- Benefit: a lost Enter or a swallowed prompt is reported in seconds.
  Risk: `agent.prompt` holds the connection open, so the client's 5 s timeout
  must be raised for that call.

### P8. Stable identity from `agent_session` across herdr restarts (information, M)

- herdr: `agent_session` on AgentInfo/PaneInfo; self-reported resume
  (0.9.2); sessions kept across suspend (#4964, unreleased).
- Now: the state table is keyed by pane id (`internal/state/table.go:9`);
  `agent_session` is decoded and used for transcripts only.
- Change: when a new pane reports an `agent_session` value that a removed row
  had, carry the CEO-given name, language and history over to the new pane
  id. Needs a live check of whether herdr restores pane ids on restart at all.
- Benefit: names survive a herdr restart or session restore. Risk: medium;
  must never merge two live agents (only adopt from rows already gone).

### P9. `agent.start` (rail, M; not recommended over P1/P4)

It would give a managed-launch window (prompts refused until ready) and
shell-safe quoting, but it still types into the shell (fact 2) and forces a
herdr name. If ever used, record the name as daemon-chosen so
`state.CEOName` ignores it, or clear it with `agent.rename` once the launch
is no longer pending.

### Not worth doing now

- `notification.show`: the CEO is away from the screen by design; phone push
  already covers it.
- `agent.view.set`, plugins, `pane.output_matched`: no use that P3/P6 do not
  cover better.
- Taking lifecycle authority (`report-agent`): would move judgment into code.
- Local detection-manifest overrides: useful if agy dialogs keep reading as
  `idle` before #4959 ships; revisit after the next herdr tag.

## Upstream asks (for herdr)

1. Emit `pane.output_changed` (already in the schema) and accept it in
   `events.wait` with `min_revision`; make `pane.read` return a real revision.
2. A `command` on `pane.split` / `workspace.create`, so a split can start a
   program without typing.
3. An optional "name is provisional" flag on `agent.start`.
