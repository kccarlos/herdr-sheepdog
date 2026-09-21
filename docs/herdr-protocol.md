# herdr socket protocol — notes for `internal/herdr`

Recorded 2026-09-17 against **herdr 0.9.0, protocol 22**, running locally on
macOS. Everything below was either read from the bundled schema
(`herdr api schema --json`, also at
`/Users/kc/Documents/herdr/docs/next/api/herdr-api.schema.json`), from the Rust
source (`src/api/`, `src/app/api/`, `src/app/api/plugins/`), or observed live
against the running server. Captured raw samples live in `testdata/herdr/`
(see §10).

Sources of truth, in order of preference:

1. `herdr api schema --json` — JSON Schema for `request`, `success_response`,
   `error_response`, `event`, `subscription_event`.
2. `herdr --skill` — agent-facing usage guide (semantics of states, waits).
3. `/Users/kc/Documents/herdr/docs/next/website/src/content/docs/socket-api.mdx`
   — the human docs page (this is what the plan called `SOCKET_API.md`).
4. Rust source, notably `src/api/server.rs` (framing), `src/api/subscriptions.rs`
   (subscription semantics), `src/app/api.rs` (`emit_pane_state_update`),
   `src/app/api/plugins/runtime.rs` (hook execution).

---

## 1. Transport and framing

- **Unix domain socket** at `~/.config/herdr/herdr.sock` (named sessions:
  `~/.config/herdr/sessions/<name>/herdr.sock`). Resolution order used by the
  CLI: `--session <name>` → `$HERDR_SOCKET_PATH` → `$HERDR_SESSION=<name>` →
  default. Socket file mode is `0600`.
- **Newline-delimited JSON.** One JSON object per line, `\n` terminated, UTF-8.
  No length prefix, no hello/handshake.
- **One request per connection.** The server reads exactly one line
  (`read_initial_request_line`, 5 s timeout, 1 MiB max), dispatches it, writes
  one response line and closes the connection. Exceptions that keep the
  connection open: `events.subscribe` (streams events until the client closes),
  `pane.graphics.stream`, and the blocking waits (`events.wait`, `agent.wait`,
  `agent.prompt` with `wait`, `pane.wait_for_output`) which hold the connection
  until they resolve and then write one response.
  Consequence for the Go client: open a fresh `net.Dial("unix", …)` per
  request; keep one long-lived connection per subscription.
- Server-side write timeout 5 s; app dispatch timeout 5 s
  (`APP_RESPONSE_TIMEOUT`). Subscription polling interval is 100 ms
  (`CONNECTION_POLL_INTERVAL`), so event latency is ≤ ~100 ms after herdr
  itself detects a change.
- **Version negotiation:** none. `ping` returns the server version/protocol;
  compare `protocol` (22) if you want to gate features. JSON clients are told to
  ignore unknown fields and treat unsupported methods as ordinary errors.

### Request envelope

```json
{"id":"<client-chosen string>","method":"<dot.name>","params":{...}}
```

`id` is echoed back. `params` is required even when empty (`{}`).

### Success envelope

```json
{"id":"<same id>","result":{"type":"<result_type>", ...}}
```

`result.type` is a snake_case discriminator (`pong`, `agent_list`,
`agent_info`, `pane_read`, `ok`, `subscription_started`, `agent_prompted`, …).

### Error envelope

```json
{"id":"<same id or \"\" if the request could not be parsed>","error":{"code":"<snake_case>","message":"<human text>"}}
```

Codes seen in the source/live: `invalid_request` (unparseable JSON or unknown
method; `id` is `""`), `invalid_params`, `pane_not_found`, `agent_not_found`,
`agent_blocked`, `agent_not_ready`, `agent_prompt_stalled`, `agent_name_taken`,
`invalid_agent_name`, `invalid_key`, `invalid_target`, `timeout`,
`server_unavailable`, `internal_error`, `pane_send_failed`,
`plugin_disabled`, `platform_unsupported`, `workspace_group_close_required`.
CLI wrappers print server errors as JSON on stderr with exit status 1.

### Observed: ping

```json
→ {"id":"recon-ping","method":"ping","params":{}}
← {"id":"recon-ping","result":{"type":"pong","version":"0.9.0","protocol":22,"capabilities":{"live_handoff":true,"detached_server_daemon":true,"endpoint_protocol_generation":1,"surface_interest":true,"health_check":true}}}
```

### Observed: errors

```json
← {"id":"e1","error":{"code":"pane_not_found","message":"pane w99:p1 not found"}}
← {"id":"e2","error":{"code":"agent_not_found","message":"agent target nope not found"}}
← {"id":"","error":{"code":"invalid_request","message":"invalid request: unknown variant `bogus.method`, expected one of `ping`, ..."}}
← {"id":"e6","error":{"code":"invalid_key","message":"unsupported key bogus+key"}}
← {"id":"recon-prompt-while-blocked","error":{"code":"agent_blocked","message":"agent recon is blocked and requires interactive input"}}
```

---

## 2. Identifiers and the agent state enum

- Public ids are opaque strings: workspace `w4`, tab `w4:t1`, pane `w4:p1`.
  Closed ids are never reused; a pane moved across workspaces gets a new id.
- **`AgentStatus` enum (exact):** `"idle" | "working" | "blocked" | "done" | "unknown"`.
  - `done` = idle **and not yet "seen"** by any client/CLI focus command; `idle`
    = idle and seen. Both mean "ready for input". Reads do not mark seen;
    `agent.focus` does. For sheepdog, a finish is the `working → {done|idle}`
    edge: herdr reports `done` when the user was not looking at the pane and
    `idle` when they had it focused when the agent finished
    (`herdr/src/pane/state.rs`'s "seen" bit) — `done` vs `idle` only encodes
    whether the user was looking, not whether the agent is finished.
  - `blocked` = herdr recognised an approval/question UI. herdr reports
    this only on change: a worker that answers one prompt and is already
    showing the next within herdr's own poll window never leaves
    `blocked`, so no `pane.agent_status_changed` event exists for it —
    sheepdog fills the gap itself with a post-answer poll and a periodic
    reconciler that read the pane directly and raise a synthetic
    `blocked → blocked` transition (`docs/roles.md` "blocked → blocked is
    a block").
  - `unknown` = agent present but unclassifiable (also emitted when an agent
    exits and the pane returns to a shell).
- The state a hook may *report* (`pane.report_agent`) is the narrower
  `PaneAgentState`: `idle | working | blocked | unknown` (no `done`).
- The plan's `blocked | working | done | idle` matches herdr except for the
  extra `unknown`, which sheepdog's state table already has.

**How state is detected for Codex and Claude Code:** by herdr's *screen
manifest detection* (remote rule files under
`~/.local/state/herdr/agent-detection/remote/<agent>.toml`), not by hooks. The
installed `claude`/`codex` integrations are "session identity" integrations
only: their hook scripts (`~/.claude/hooks/herdr-agent-state.sh`,
`~/.codex/herdr-agent-state.sh`) run on `SessionStart` and send
`pane.report_agent_session` (session id / transcript path) so herdr can resume
the session after restart. They never send `pane.report_agent`. Only Pi, OMP,
Kimi, OpenCode, Kilo and MastraCode integrations are "lifecycle authority"
integrations. `herdr agent explain <target>` shows which rule matched; for the
Codex approval prompt the winning rule was `osc_title_blocked` (priority 1100,
matches `Action Required` in the OSC window title) with `live_strong_blocker`
(priority 900) also matching on the on-screen option list.

---

## 3. Listing / getting agents

```json
→ {"id":"1","method":"agent.list","params":{}}
← {"id":"1","result":{"type":"agent_list","agents":[ <AgentInfo>, ... ]}}

→ {"id":"2","method":"agent.get","params":{"target":"recon"}}        // name or pane id
← {"id":"2","result":{"type":"agent_info","agent": <AgentInfo> }}
```

`target` accepts a unique live agent **name** (`[a-z][a-z0-9_-]{0,31}`) or the
**pane id** hosting an agent. Bare kind labels (`codex`) are not targets.
`agent.list` returns only panes that currently host a recognised agent
(empty array when none).

`AgentInfo` (required fields starred):

| field | type | notes |
|---|---|---|
| `pane_id`* | string | `w5:p1` — the key sheepdog should use |
| `workspace_id`*, `tab_id`*, `terminal_id`* | string | |
| `agent_status`* | AgentStatus | |
| `focused`*, `revision`* | bool, uint64 | |
| `agent` | string\|null | kind label: `codex`, `claude`, … (this is the plan's `kind`) |
| `name` | string\|null | user-assigned live name (`agent.start <name>`, `agent.rename`) |
| `cwd`, `foreground_cwd` | string\|null | pane cwd / cwd of the foreground process |
| `title`, `display_agent`, `state_labels`, `tokens` | | plugin/hook presentation metadata (`pane.report_metadata`), usually absent |
| `terminal_title`, `terminal_title_stripped` | string\|null | latest OSC 0/2 window title; Codex puts `[ ! ] Action Required \| <task summary> \| <dir>` here when blocked and `⠸ <task summary> \| <dir>` while working; Claude Code puts `✳ <task summary>` |
| `agent_session` | object\|null | `{source:"herdr:codex",agent:"codex",kind:"id"\|"path",value:"…"}` |
| `interactive_ready`, `launch_pending`, `screen_detection_skipped` | bool | |
| `state_change_seq` | uint64 | monotonically increases per state change, useful for ordering/dedup |

Observed while blocked (`testdata/herdr/codex-approval-prompt.agent-get.json`):

```json
{"id":"recon-get-blocked","result":{"type":"agent_info","agent":{"terminal_id":"term_65bb68107f52a2","name":"recon","agent":"codex","terminal_title":"[ ! ] Action Required | Create hello.txt file | repo","terminal_title_stripped":"[ ! ] Action Required | Create hello.txt file | repo","agent_status":"blocked","agent_session":{"source":"herdr:codex","agent":"codex","kind":"id","value":"01a0b1cb-a237-7722-8eea-8db9f1dd95b6"},"workspace_id":"w5","tab_id":"w5:t1","pane_id":"w5:p1","focused":false,"interactive_ready":true,"state_change_seq":5,"cwd":"/…/scratchpad/repo","foreground_cwd":"/…/scratchpad/repo","revision":48}}}
```

### Workspaces / panes (for aliases and snapshot diff)

```json
→ {"id":"3","method":"workspace.list","params":{}}
← {"id":"3","result":{"type":"workspace_list","workspaces":[{"workspace_id":"w4","number":1,"label":"kvoice","focused":true,"pane_count":1,"tab_count":1,"active_tab_id":"w4:t1","agent_status":"unknown"}]}}

→ {"id":"4","method":"pane.list","params":{"workspace_id":"w4"}}   // workspace_id optional
← {"id":"4","result":{"type":"pane_list","panes":[ <PaneInfo>, ... ]}}

→ {"id":"5","method":"session.snapshot","params":{}}               // == `herdr api snapshot`
← {"id":"5","result":{"type":"session_snapshot","snapshot":{"version":"0.9.0","protocol":22,"focused_workspace_id":"w4","focused_tab_id":"w4:t1","focused_pane_id":"w4:p1","workspaces":[…],"tabs":[…],"panes":[ <PaneInfo> ],"layouts":[…],"agents":[ <AgentInfo> ]}}}
```

`PaneInfo` = `AgentInfo` minus `name`/`interactive_ready`/`launch_pending`/
`state_change_seq`, plus `label` (user pane label, `pane.rename`) and `scroll`.
`WorkspaceInfo.label` is the workspace name shown in the sidebar (derived from
the cwd basename unless renamed).

**Default alias derivation (proposal):** `agent.name` if set; else
`workspace.label` (+ `-<n>` when the workspace has more than one agent pane,
or `pane.label`/`title` when present). `terminal_title_stripped` is *not* a
stable alias source — it changes per task.

**Snapshot diff for §3.1:** poll `session.snapshot` (one request) every 30 s
and diff `agents[].pane_id → agent_status`. Use `agents`, not `panes`, so
shells without an agent are ignored.

---

## 4. Subscribing to events

### The `events.subscribe` request

```json
→ {"id":"sub-1","method":"events.subscribe","params":{"subscriptions":[
     {"type":"pane.agent_status_changed","pane_id":"w5:p1"},
     {"type":"pane.agent_detected"},
     {"type":"pane.created"},
     {"type":"pane.closed"},
     {"type":"pane.exited"},
     {"type":"workspace.closed"}
  ]}}
← {"id":"sub-1","result":{"type":"subscription_started"}}
← <one event JSON object per line, forever>
```

The ack line comes first; afterwards each line is an event. Events are **not
replayed** from before the ack; bootstrap with `session.snapshot` *after* the
ack and apply buffered events (the docs recommend exactly this). No
timestamps are included in events — stamp them client-side.

Two different envelope shapes arrive on the same stream:

1. **Per-pane subscription events** (`pane.agent_status_changed`,
   `pane.output_matched`, `pane.scroll_changed`) use the
   `subscription_event` schema — `event` is the **dotted** name, `data` has no
   `type`:

   ```json
   {"data":{"agent":"codex","agent_status":"blocked","pane_id":"w5:p1","workspace_id":"w5"},"event":"pane.agent_status_changed"}
   ```

   Full `PaneAgentStatusChangedEvent` fields: `pane_id`*, `workspace_id`*,
   `agent_status`*, `agent` (kind label), `title`, `display_agent`,
   `state_labels` (the last three are plugin metadata; omitted when null/empty).
   **The event carries no reason/question text.** To learn *what* the agent is
   asking, read the pane (§5) and/or `agent.get.terminal_title`.

2. **Lifecycle events** (`pane.created`, `pane.closed`, `pane.agent_detected`,
   `workspace.closed`, …) use the `event` schema — `event` is the
   **snake_case** `EventKind` and `data.type` repeats it:

   ```json
   {"data":{"agent":"codex","pane_id":"w5:p1","type":"pane_agent_detected","workspace_id":"w5"},"event":"pane_agent_detected"}
   {"data":{"agent":"codex","final_status":"done","pane_id":"w5:p1","released":true,"type":"pane_agent_detected","workspace_id":"w5"},"event":"pane_agent_detected"}
   {"data":{"type":"workspace_closed","workspace":{…},"workspace_id":"w6"},"event":"workspace_closed"}
   {"data":{"pane":{ <PaneInfo> },"type":"pane_created"},"event":"pane_created"}
   ```

   `pane_agent_detected` with `released:true` + `final_status` means the agent
   process exited; it is followed by `pane.agent_status_changed` → `unknown`.

Important constraints found in `src/api/subscriptions.rs`:

- `pane.agent_status_changed` **requires `pane_id`** — there is no fleet-wide
  status subscription. Optional `agent_status` filters to one state and, if the
  pane is *already* in that state at subscribe time, emits one initial event.
  A subscription for a pane that does not exist fails the whole request with
  `pane_not_found`.
- `pane.updated` does **not** fire on status changes (only when the agent name
  changes or the terminal title changes); it is *very* chatty (92 of 117 lines
  in the captured session were `pane_updated` title spinners). Do not use it
  for state.
- `workspace.close` emits **only `workspace_closed`**, not `pane_closed` /
  `pane_exited` for the panes inside it (verified live,
  `testdata/herdr/events-workspace-close.jsonl`).
- Lifecycle subscriptions are cheap (event-hub replay). Per-pane status
  subscriptions additionally probe `pane.get` every 100 ms when no event hub
  activity is seen, so keep the number of open subscription connections modest.

**Recommended pattern for sheepdog (WP1):** one long-lived connection
subscribed to `pane.agent_detected`, `pane.created`, `pane.closed`,
`pane.exited`, `workspace.closed` (fleet discovery), plus one
`pane.agent_status_changed` subscription per agent pane — either bundled into
the same request when the pane set is known, or one connection per pane opened
on `pane_agent_detected` and dropped on `released:true`/`pane_closed`/
`workspace_closed`. Alternatively use the plugin hook channel (§8), which *is*
fleet-wide. Reconcile both with the 30 s `session.snapshot` diff.

### `events.wait` (one-shot)

```json
→ {"id":"w1","method":"events.wait","params":{"match_event":{"event":"pane_agent_status_changed","pane_id":"w5:p1","agent_status":"blocked"},"timeout_ms":60000}}
← {"id":"w1","result":{"type":"wait_matched","event":{"event":"pane_agent_status_changed","data":{…}}}}   // or error code "timeout"
```

### Observed timeline (Codex, real approval prompt)

From `testdata/herdr/events-agent-status.timeline.txt` (wall clock added by the
capture script):

```
16:54:34 subscription_started
16:54:42 pane_agent_detected agent=codex
16:54:46 pane.agent_status_changed idle          # Codex trust dialog on screen — NOT classified as blocked
16:55:07 working                                  # after Enter on the trust dialog
16:55:08 done
16:55:21 working                                  # agent.prompt "Create hello.txt …" submitted
16:55:32 blocked                                  # approval prompt visible (11 s after prompt)
16:56:07 working                                  # Enter sent (approve)
16:56:17 done                                     # file written
16:56:51 working / 16:56:56 blocked               # second prompt
16:57:08 done                                     # Esc sent (decline) — command NOT run
16:57:38 working / 16:57:59 done                  # sleep 90; Ctrl-C sent at 16:57:59 → turn interrupted, Codex stays alive
16:58:17 pane_agent_detected released=true final_status=done   # /quit
16:58:18 pane.agent_status_changed unknown
```

Claude Code (`claude --permission-mode default`), same pane:

```
16:58:35 pane_agent_detected agent=claude
16:58:38 blocked                                  # Claude's folder-trust dialog IS classified blocked
16:58:47 idle                                     # after Down+Enter
16:59:04 working                                  # agent.prompt "Create claude.txt …"
16:59:08 blocked                                  # Write permission prompt (4 s after prompt)
16:59:20 working / 16:59:21 done                  # Enter sent (approve)
16:59:38 released=true → unknown                  # /exit
```

Note the lack of flapping in these runs: no spurious `blocked` shorter than
the plan's 3 s debounce, but `working → done` can be 1 s apart on trivial
turns, and Codex's startup trust dialog is reported as `idle` (so a debounce
on `blocked` alone will not catch it).

---

## 5. Reading a pane's screen

```json
→ {"id":"r1","method":"agent.read","params":{"target":"recon","source":"visible"}}
→ {"id":"r2","method":"agent.read","params":{"target":"recon","source":"recent_unwrapped","lines":120}}
→ {"id":"r3","method":"pane.read","params":{"pane_id":"w5:p1","source":"recent_unwrapped","lines":120,"format":"text","strip_ansi":true}}
← {"id":"r1","result":{"type":"pane_read","read":{"pane_id":"w5:p1","workspace_id":"w5","tab_id":"w5:t1","source":"visible","format":"text","text":"…\n","revision":0,"truncated":false}}}
```

- `source` (**required**, raw enum uses underscore): `visible` (rendered
  viewport), `recent` (recent output incl. soft wraps), `recent_unwrapped`
  (soft wraps joined — best for feeding an LLM), `detection` (plain-text
  bottom-buffer snapshot used by the state detector). The CLI spells it
  `recent-unwrapped` and defaults to `recent`; `herdr agent read` is a thin
  wrapper over `agent.read` with `format:"text", strip_ansi:true`.
- `lines`: optional, clamped to 1000. For `recent*` the default is 80. For
  `visible`/`detection` omitted = whole snapshot; when given, the **last N
  lines** are returned and `truncated` says whether anything was dropped.
- `format`: `text` (default) or `ansi`; `strip_ansi` default `true`.
- `agent.read` resolves `target` (name or pane id) and requires an agent in
  the pane; `pane.read` works on any pane.
- Alt-screen caveat (from `herdr --skill`): rows that leave the alternate
  screen never enter scrollback, so a larger `lines` cannot recover a long
  completed response. Codex and Claude Code both run in the alt screen; the
  approval prompt is always in the visible viewport, which is what sheepdog's
  the prompt parser needs. `recent_unwrapped` + `lines:120` still returned the
  full visible content plus what was on screen before.

For the captured Codex approval screen see
`testdata/herdr/codex-approval-prompt.screen.txt`; the question, options and
key hints are all present verbatim:

```
  Would you like to run the following command?
  Environment: local
  Reason: May I create the requested hello.txt file in the current workspace?
  $ printf '%s\n' 'hello from codex.' > hello.txt
› 1. Yes, proceed (y)
  2. Yes, and don't ask again for commands that start with `printf …` (p)
  3. No, and tell Codex what to do differently (esc)
  Press enter to confirm or esc to cancel
```

Claude Code (`testdata/herdr/claude-permission-prompt.screen.txt`):

```
⏺ Write(claude.txt)
 Create file
 claude.txt
  1 hello from claude
 Do you want to create claude.txt?
 ❯ 1. Yes
   2. Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)
   3. No
 Esc to cancel · Tab to amend
```

---

## 6. Sending text and keys

### Prompt (text + Enter as one submission) — `herdr agent prompt`

```json
→ {"id":"p1","method":"agent.prompt","params":{"target":"recon","text":"Skip the flaky test and rerun."}}
← {"id":"p1","result":{"type":"agent_prompted","agent":{ <AgentInfo> }}}

→ {"id":"p2","method":"agent.prompt","params":{"target":"recon","text":"…","wait":{"until":["blocked"],"timeout_ms":180000}}}
← {"id":"p2","result":{"type":"agent_prompted","agent":{ …,"agent_status":"blocked",… }}}
```

- The server encodes `text` honouring the pane's bracketed-paste mode
  (`ESC[200~ … ESC[201~`), then writes the terminal's Enter encoding, in one
  ordered submission (with a size-dependent delay for Codex on Windows).
- **Refused with `agent_blocked` (nothing written) when the agent is already at
  an approval/question UI** — verified live. So `confirm()` must answer with
  `agent.send_keys`, never with `agent.prompt`.
- Also refused with `agent_not_ready` while startup is pending.
- `wait` (optional): `until` defaults to the settled set `idle|done|blocked`;
  with `wait` present the server first requires observed `working`/`blocked`
  activity within 5 s or returns `agent_prompt_stalled`; `timeout` error if
  `timeout_ms` elapses. The response is the `AgentInfo` at settle time.
- Slash commands work through the same path (`/quit`, `/exit` were used to
  stop the agents in the capture).

### Keys — `herdr agent send-keys` / `pane.send_keys`

```json
→ {"id":"k1","method":"agent.send_keys","params":{"target":"recon","keys":["enter"]}}
→ {"id":"k2","method":"agent.send_keys","params":{"target":"recon","keys":["ctrl+c"]}}
→ {"id":"k3","method":"pane.send_keys","params":{"pane_id":"w5:p1","keys":["down","enter"]}}
← {"id":"k1","result":{"type":"ok"}}
```

- Key strings: printable chars (`y`, `1`), `enter`/`return`, `esc`/`escape`,
  `tab`, `backspace`, `up/down/left/right`, `home/end`, `pageup/pagedown`,
  `f1`…, named punctuation (`minus`, `plus`, `space`, `slash`, …), modifier
  chords `ctrl+c` / `control+c` / `alt+x` / `shift+tab`. `C-c` is accepted as an
  alias for `ctrl+c`. All keys are validated before any byte is written
  (`invalid_key` otherwise). Encoding respects the pane's keyboard protocol
  (kitty vs legacy), which is why sheepdog should send `"enter"` rather than a
  literal `\r`.
- **Ctrl-C semantics observed:** on a *working* Codex turn, `ctrl+c` interrupted
  the turn ("Conversation interrupted") and Codex stayed alive at its prompt →
  status `done`. It did not exit the agent. (Codex's own hint is `esc to
  interrupt`; both work. Claude Code uses Esc to interrupt as well; Ctrl-C at an
  idle Claude prompt twice exits — untested here.)
- **Answering prompts observed:** Codex approval — `enter` = option 1 "Yes,
  proceed"; `esc` = "No" (command was *not* executed; file unchanged). Claude
  Code permission — `enter` = "1. Yes"; `esc` cancels. Both agents also accept
  the digit keys shown in the menu (`1`/`2`/`3`, and `y`/`p` for Codex).

### Raw text without Enter — `pane.send_text` / `pane.send_input`

```json
→ {"id":"t1","method":"pane.send_text","params":{"pane_id":"w5:p1","text":"hello"}}          // bracketed paste if enabled, no Enter
→ {"id":"t2","method":"pane.send_input","params":{"pane_id":"w5:p1","text":"hello","keys":["enter"]}}   // text then keys in one write
```

`pane.run` (CLI) = `pane.send_input` with the command text and `enter`, meant
for shells.

---

## 7. Agent lifecycle helpers used by the recon (and useful for e2e tests)

```json
→ {"id":"a","method":"workspace.create","params":{"cwd":"/abs/path","label":"sheepdog-recon","focus":false}}
← {"id":"a","result":{"type":"workspace_created","workspace":{…"workspace_id":"w5"…},"tab":{…},"root_pane":{…"pane_id":"w5:p1"…}}}

→ {"id":"b","method":"agent.start","params":{"name":"recon","kind":"codex","pane_id":"w5:p1","timeout_ms":60000,"args":["--sandbox","read-only","-a","on-request"]}}
← {"id":"b","result":{"type":"agent_started","agent":{…},"argv":["codex","--sandbox","read-only","-a","on-request"]}}

→ {"id":"c","method":"agent.wait","params":{"target":"recon","until":["blocked"],"timeout_ms":120000}}
← {"id":"c","result":{"type":"agent_info","agent":{…}}}      // or {"error":{"code":"timeout",…}}

→ {"id":"d","method":"workspace.close","params":{"workspace_id":"w5"}}
← {"id":"d","result":{"type":"ok"}}
```

`agent.wait` without `until` returns **immediately** if the agent is already
in a settled state (`idle|done|blocked`) — observed: it returned `blocked` at
once right after the approval was answered but before the screen changed. Use
`until:["working"]` or a status-specific wait when you need an edge.
`agent.start` requires an existing shell pane at its prompt; `kind` values:
`pi|claude|codex|gemini|cursor|devin|agy|cline|omp|mastracode|opencode|copilot|kimi|kiro|droid|amp|grok|hermes|kilo|qodercli|qwen|maki|muse`.

Trust dialogs: Codex 0.154 shows a directory-trust dialog on first launch in a
folder (herdr classifies it `idle`), Claude Code 2.1.275 shows a folder-trust
dialog (herdr classifies it `blocked`). The e2e harness should pre-trust the
scratch directory or answer the dialog before prompting.

---

## 8. Plugin system (herdr's own; sheepdog ships no plugin)

**There is no `herdr plugin install <local path>` in the plan's sense.**
`herdr plugin install` accepts only GitHub shorthand (`owner/repo[/subdir]`,
`--ref`, `--yes`) and clones with `git`. For a local directory use
`herdr plugin link <path> [--disabled]` (socket method `plugin.link`). The
`plugin` command group is hidden from `herdr --help` but exists:

```
herdr plugin install <owner>/<repo>[/subdir...] [--ref REF] [--yes]
herdr plugin uninstall <plugin_id|owner/repo[/subdir...]>
herdr plugin link <path> [--disabled]
herdr plugin unlink <plugin_id>
herdr plugin list [--plugin ID] [--json]
herdr plugin config-dir <plugin_id>
herdr plugin log list --plugin <plugin_id>
```

### Manifest — `herdr-plugin.toml` in the plugin directory

Verified live with this minimal manifest (`plugin.link` succeeded, hooks fired):

```toml
id = "sheepdog.herdr-shim"              # ASCII letters, digits, . : _ -
name = "Sheepdog herdr shim"
version = "0.1.0"
min_herdr_version = "0.9.0"             # REQUIRED; link refused if newer than the binary
description = "Forwards agent state events to the sheepdog daemon"
platforms = ["macos", "linux"]

[[events]]
on = "pane.agent_status_changed"        # dotted event name
command = ["sh", "shim.sh"]             # argv, no shell expansion, cwd = plugin dir

[[events]]
on = "pane.agent_detected"
command = ["sh", "shim.sh"]

# optional one-shot at server start (not a daemon supervisor):
# [[startup]]
# command = ["sh", "shim.sh"]           # receives HERDR_PLUGIN_EVENT=startup
```

Hookable event names (`PLUGIN_HOOK_EVENT_KINDS`): `workspace.created`,
`workspace.updated`, `workspace.closed`, `workspace.renamed`,
`workspace.moved`, `workspace.reordered`, `workspace.focused`,
`worktree.created`, `worktree.opened`, `worktree.removed`, `tab.created`,
`tab.closed`, `tab.renamed`, `tab.moved`, `tab.focused`, `pane.created`,
`pane.closed`, `pane.focused`, `pane.moved`, `pane.exited`,
`pane.agent_detected`, **`pane.agent_status_changed`**. Unknown names link with
a `warnings` entry. `pane.updated`, `pane.output_changed` and
`workspace.metadata_updated` are *not* hookable.

### Hook execution model (verified)

- **One process per event per hook** (`std::process::Command::spawn`, no
  shell), spawned from the herdr app thread on `emit_event` (before the event
  is pushed to the socket event hub). Max 32 concurrent plugin commands
  (`plugin_command_limit_reached` otherwise); stdout/stderr captured up to
  64 KiB and stored in the 200-entry log (`plugin.log.list`). No timeout is
  applied — the hook must exit quickly on its own.
- **The event is delivered in the environment, not on stdin:**
  - `HERDR_PLUGIN_EVENT=pane.agent_status_changed` (dotted name)
  - `HERDR_PLUGIN_EVENT_JSON={"event":"pane_agent_status_changed","data":{"type":"pane_agent_status_changed","pane_id":"w7:p1","workspace_id":"w7","agent_status":"blocked","agent":"codex"}}`
    (the lifecycle `EventEnvelope` shape, snake_case)
  - `HERDR_PLUGIN_CONTEXT_JSON={"workspace_id":"w7","workspace_label":"recon-hook-test","workspace_cwd":"/…","tab_id":"w7:t1","tab_label":"1","focused_pane_id":"w7:p1","focused_pane_cwd":"/…","focused_pane_agent":"codex","focused_pane_status":"blocked","invocation_source":"api","correlation_id":"pane.agent_status_changed"}`
  - plus `HERDR_ENV=1`, `HERDR_SOCKET_PATH`, `HERDR_BIN_PATH`, `HERDR_PLUGIN_ID`,
    `HERDR_PLUGIN_ROOT`, `HERDR_PLUGIN_CONFIG_DIR`
    (`~/.config/herdr/plugins/config/<id>`), `HERDR_PLUGIN_STATE_DIR`
    (`~/.local/state/herdr/plugins/<id>`), `HERDR_WORKSPACE_ID`, `HERDR_TAB_ID`,
    `HERDR_PANE_ID` (= the event's pane).
  Full dump: `testdata/herdr/plugin-hook-env-dump.txt`.
- Hooks fire for every pane (fleet-wide), so a hook script would be a cheap
  fleet-wide channel; sheepdog does not use one (the socket subscription is
  its only event source, and `POST /api/herdr/event` stays open for anything
  that wants to feed the watcher). Latency: the two hooks for a
  `report-agent` call started within ~20 ms of the event.
- Hooks are **only** invoked for events emitted through `emit_event`; the
  `message` passed to `pane.report_agent` is stored in the pane's
  `HookAuthority` but is **not** surfaced in any event or API response.
- Linking is global to the user and persisted in `plugins.json` next to
  `session.json`; it works even when no server is running. The plugin
  registry is re-read before each hook dispatch, so `herdr plugin link` takes
  effect without a restart.

Shim sketch (< 100 lines is easy): `shim.sh` does
`curl -s -m 2 -X POST --data-binary "$HERDR_PLUGIN_EVENT_JSON" http://127.0.0.1:<port>/herdr/event`
and exits 0. The shim cannot be a long-running daemon; launchd remains the
right place for the sheepdog daemon itself (`[[startup]]` could `launchctl
kickstart` it).

---

## 9. Integration install hooks (what `herdr integration install claude|codex` does)

- Writes `~/.claude/hooks/herdr-agent-state.sh` and adds a `SessionStart`
  hook (`bash '…/herdr-agent-state.sh' session`, timeout 10) to
  `~/.claude/settings.json`; writes `~/.codex/herdr-agent-state.sh` and a
  `session_start` entry in `~/.codex/hooks.json` (+ `hooks = true` in
  `~/.codex/config.toml`).
- Both scripts: exit unless `HERDR_ENV=1`, `HERDR_SOCKET_PATH` and
  `HERDR_PANE_ID` are set; parse the hook JSON from stdin; on `SessionStart`
  connect to the Unix socket with a 0.5 s timeout and send **one**
  `pane.report_agent_session` request:
  ```json
  {"id":"herdr:codex:<ms>:<rand>","method":"pane.report_agent_session","params":{"pane_id":"w5:p1","source":"herdr:codex","agent":"codex","seq":<time_ns>,"agent_session_id":"<uuid>","session_start_source":"startup"}}
  ```
  (Claude's variant adds `agent_session_path` = transcript path and skips
  subagents.)
- They report **session identity only**; they never report `working` /
  `blocked` / `idle`. Agent state for Codex and Claude Code is screen-detected
  (§2). This matters for sheepdog's reliability story: there is no hook-side
  state channel to dedupe against — the two channels are (a) socket
  subscription and (b) plugin hook shim, both fed by the same detector.

---

## 10. Captured samples (`testdata/herdr/`)

All files are verbatim server output (paths under the session scratchpad are
left as-is; the WP7b `agy-*` files additionally have the hostname, home path
and account e-mail redacted). Screens are the `text` field of
`agent.read … source:"visible"`.

| file | what |
|---|---|
| `codex-approval-prompt.screen.txt` | Codex 0.154.0 approval prompt (blocked) — the primary WP3 fixture |
| `codex-approval-prompt.read.json` / `.read-detection.json` | raw `agent.read` responses (`visible` / `detection`) |
| `codex-approval-prompt.agent-get.json` / `.agent-list.json` | `agent.get` / `agent.list` while blocked |
| `codex-approval-prompt.explain.json` | `agent.explain` — which detection rules matched |
| `codex-approval-prompt.prompt-wait-response.json` | `agent.prompt` + `wait until blocked` response |
| `codex-prompt-while-blocked.error.json` | the `agent_blocked` error |
| `codex-trust-dialog.screen.txt` | Codex startup trust dialog (classified `idle`) |
| `codex-after-approval.screen.txt` | after Enter; command ran |
| `codex-declined-esc.screen.txt` | after Esc; command not run |
| `codex-working.screen.txt` / `codex-after-ctrl-c.screen.txt` | working turn / interrupted by `ctrl+c` |
| `claude-permission-prompt.screen.txt` / `.read.json` / `.agent-get.json` / `.explain.json` | Claude Code 2.1.275 Write permission prompt (blocked) |
| `claude-after-approval.screen.txt` | after Enter |
| `agy-trust-dialog.read.json` / `.explain.json` | Antigravity CLI 1.2.5 folder-trust dialog (WP7b; classified `idle`) |
| `agy-approval-prompt.screen.txt` / `.read.json` / `.read-detection.json` / `.agent-get.json` / `.agent-list.json` / `.explain.json` / `agy-prompt-wait-response.json` | agy 1.2.5 command-approval prompt ("Run this command?", options 1–4). herdr 0.9.0 with remote manifest 2026.06.24.1 reports it as **`done`**, not `blocked` (`default_known_agent_idle_fallback`); see `docs/e2e-results.md` |
| `agy-edit-prompt.screen.txt` / `.read.json` / `.agent-get.json` / `.explain.json` | agy 1.2.5 file-edit prompt ("Accept this file edit?", 1 accept / 2 reject), also reported `done` |
| `agy-edit-prompt-esc-disabled.read.json` | the same prompt after `esc`: "Esc disabled during file edits — press 1 to accept or 2 to reject" — `no` must send `2` for agy edit prompts |
| `events-agent-status.jsonl` | the 25 status/lifecycle events of the whole session, verbatim |
| `events-agent-status.timeline.txt` | same, prefixed with capture wall-clock (HH:MM:SS) |
| `events-session-full.jsonl` | all 117 lines including the noisy `pane_updated` events |
| `events-workspace-close.jsonl` | shows `workspace.close` emits only `workspace_closed` |
| `plugin-hook-env-dump.txt` | environment a `[[events]]` hook receives |
| `workspace-create.response.json` / `agent-start.response.json` | creation responses |

---

## 11. Deviations from sheepdog-v1-plan.md

- **§3.1 / §9 WP0:** there is no `SOCKET_API.md` in the herdr repo. The
  equivalents are `herdr api schema --json`, the `socket-api.mdx` docs page,
  and `herdr --skill`.
- **§3.1 state enum:** herdr's enum is `idle|working|blocked|done|unknown`
  (plan lists four; `unknown` is real and is what an exited agent becomes).
  `done` vs `idle` is a "seen" flag, not a semantic difference.
- **§11 "does `blocked` carry a reason":** **No.** `pane.agent_status_changed`
  carries only `pane_id`, `workspace_id`, `agent_status`, `agent` (+ optional
  plugin presentation fields). The question text must come from `agent.read`;
  `agent.get.terminal_title` gives a one-line task summary for Codex/Claude.
- **§3.1 "subscribe to agent state events":** the socket has **no fleet-wide**
  agent-status subscription; it is per `pane_id`. Fleet discovery needs the
  lifecycle subscriptions (`pane.agent_detected`, `pane.created`, `pane.closed`,
  `pane.exited`, `workspace.closed`) or the plugin hook channel, which is
  fleet-wide.
- **§3.1 dedup key `(pane, state, timestamp window)`:** events have no server
  timestamp; use client receive time, or `AgentInfo.state_change_seq` when a
  channel provides it (the subscription event does not; a snapshot does).
- **§3.8 / §11 `herdr plugin install`:** installs only from GitHub. Local
  shims are registered with `herdr plugin link <dir>`. Manifest is
  `herdr-plugin.toml` (TOML, `min_herdr_version` mandatory). Hooks *are*
  per-event processes as assumed, but the payload arrives in
  `HERDR_PLUGIN_EVENT_JSON` (env), not stdin, and the plugin cannot host a
  daemon (`[[startup]]` is one-shot).
- **Orchestrator note "integrations are agent-state hooks":** for `claude` and
  `codex` they are session-identity hooks only; state is screen-detected. Works
  without the integrations installed at all.
- **§3.6 `confirm()` implementation:** must use `agent.send_keys` (`enter`,
  `esc`, `1`/`2`/`3`, `y`); `agent.prompt` is rejected with `agent_blocked`
  while a prompt is showing.
- **D4 "cancel sends Ctrl-C":** confirmed safe for Codex mid-turn (interrupts
  the turn, agent survives). For an *idle* agent Ctrl-C may start the
  exit sequence; consider `esc` first when the agent is `working` and only
  Ctrl-C for shells.
- **Transport detail not in the plan:** one request per connection; only
  `events.subscribe` keeps the connection open.
