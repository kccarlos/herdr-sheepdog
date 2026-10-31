# herdr-core backend

Status: built, 2026-10-30: `internal/herdr/backend.go` (the interface),
`internal/hc` (the client) and `internal/herdrcore` (the backend).

sheepdog talks to herdr, the terminal multiplexer its agents run in, over
herdr's unix socket ([herdr-protocol.md](../herdr-protocol.md)). herdr-core
(`hc`) is the new herdr: a different program with a different API, while
the old herdr stays maintained and keeps its users. sheepdog supports
both from one daemon: one backend interface, two implementations, chosen
at startup.

## What hc is, for sheepdog

- **API.** JSON-RPC 2.0 over a unix socket, one message per line
  (`hc.openrpc.json` in the herdr-core repo is the truth; `hc schema`
  prints it). The methods sheepdog needs: `hello`, `terminal.list`,
  `terminal.get`, `terminal.read`, `terminal.send`, `terminal.keys`,
  `terminal.interrupt`, `terminal.run`, `terminal.kill`,
  `events.subscribe`, and for the live terminal `terminal.attach` on a
  framed connection (`hello` with `framed`).
- **Socket.** `HC_SOCKET`, else `<session>.sock` under
  `$XDG_RUNTIME_DIR/herdr-core/`, else `/run/user/<uid>/herdr-core/`, else
  the session's state directory
  (`~/.local/state/herdr-core/default/default.sock`); `HC_SESSION` names
  the session (default `default`).
- **Status comes from the agent.** hc never guesses status from the
  screen. A program reports it through OSC 7501, the Program Status
  Protocol: a word (`idle`, `working`, `done`, `blocked`, `error`), and
  optionally `app`, `kind` (`permission`, `question`, `auth`), `msg`,
  `title` and `progress`. A program that does not report has no status.
- **No workspaces in core.** hc has terminals only. herdr-app, the GUI,
  keeps its spaces and tabs in a core document (`doc.get`/`doc.set`, name
  `herdr-app`) that core never interprets. It shows a terminal in a space
  and tab by the labels `herdr.space` and `herdr.tab` ("Spaces without the
  document").

### Do Claude Code and Codex report OSC 7501? Yes

Checked on 2026-10-30 with hc 0.1.15, Claude Code 2.1.314 and Codex CLI
0.166, each started with `hc run --bg` in a private core (its own
`HC_SOCKET`, `HC_SESSION` and state directory):

| Moment | Claude Code | Codex |
|---|---|---|
| first-run folder trust question | `status: null`, `reported: false` | `status: null`, `reported: false` |
| at its prompt | `{app: claude-code, state: idle}` | `{app: codex, state: idle}` |
| a turn | `working`, then `done` with `msg` (a one-line summary of the reply) | `working`, then `done` |
| an approval | `{state: blocked, kind: permission, msg: "approve Bash: curl -sI https://example.com"}` | `{state: blocked, kind: permission}` |

So the status words are the primary signal on hc, and they line up with
herdr's. One gap: the first-run trust question comes **before** the
agent's status reporting starts, so a just-started agent at that
question looks like a plain program with no status. The screen fallback
below covers it.

The event stream (`events.subscribe`, plain mode) carries a `status`
event per change, `turn` when a turn ends, `title`, `created`,
`finished`, `lost`, `removed`, `foreground`, a `heartbeat` every 15 s and
`missed` with a fresh snapshot when a subscriber falls behind.

## The interface

`internal/herdr` keeps its types as the shared vocabulary (`State`,
`AgentInfo`, `PaneInfo`, `Snapshot`, `ReadResult`, `StatusEvent`,
`FleetEvent`, `Transition`, the prompt parser). A second backend
translates into them; nothing above the backend learns a new word.

```go
// internal/herdr/backend.go
type EventSource interface {           // what the Watcher reads
	Snapshot(ctx) (Snapshot, error)
	Subscribe(ctx, paneID) <-chan StatusEvent
	SubscribeFleet(ctx) <-chan FleetEvent
	Resync() <-chan struct{}
}

type Backend interface {               // what the daemon needs
	EventSource
	Ping(ctx) (Pong, error)
	ListAgents, Read, Prompt, SendKeys, Interrupt   // = supervisor.Herdr
}
```

- The `Watcher` takes an `EventSource`, so its dedup, `blocked` debounce,
  rename and `Known` seeding apply to both backends unchanged.
- The optional capabilities stay where they are: the supervisor's small
  interfaces (`Spawner`, `Closer`, `Renamer`, `SpaceHost`,
  `WorktreeCreator`, `ANSIReader`) and the web layer's `ShellHost`, found
  by type assertion through `inject.Herdr`. A backend without one gets
  the clean refusal those paths already have.
- The live terminal (`/term`) has a bridge per backend: `web.TermDeps` is
  built for both, and `/api/hello` lists `term` only while the backend can
  run one (the old herdr's version, or hc's `hello` listing
  `terminal.attach`). Otherwise the app uses its snapshot window.

## Mapping

| herdr (old) | herdr-core (hc) | v1 rule |
|---|---|---|
| pane id `p_…` | terminal address `t7` | pane id = address, so `pane_id` in `/api/state` (agents and the pending question) is the hc address a herdr-app mod passes to hc to focus it. Terminals on added machines (`rohan/t7`) are left out: a `/` breaks `/api/agents/{pane_id}/…`. |
| agent (a pane where herdr detected one) | terminal | an agent is a live local terminal with a current status, or whose program is a known agent kind (foreground or start `argv[0]` is `claude`, `codex`, `agy`, …). `reported` alone is not enough: after a cleared status with the shell in front, the terminal is a shell again. A plain shell is not an agent and a phone shell becomes one if an agent program runs in it or it has a status; the `sheepdog=shell` label does not suppress that. |
| agent kind (`claude`, `codex`) | `status.app` (`claude-code`, `codex`), else `argv[0]` | `claude-code` → `claude`; otherwise the app or program name. |
| agent name | terminal `name` | as is; no rename on hc (the name is set at `terminal.run`). |
| workspace | none in core; herdr-app's space, which it shows for terminals labelled `herdr.space=<id>` | `WorkspaceID` = the `herdr.space` label (empty for an unlabeled terminal, which herdr-app does not show either), on panes, agents and status events. The snapshot lists one workspace per space id seen on a live local terminal, in order of first appearance, with its pane count and its most urgent agent's state (blocked, done, working, idle). |
| workspace label | herdr-app's space name, in its document | not read. The label follows herdr-app's rule for a space nobody named: its first (oldest) terminal's repo (the git checkout's folder name, from `.git`), else its folder, `~` at home. A name the user gave in herdr-app is not seen. |
| tab | herdr-app's tab, shown for `herdr.tab=<id>` | `TabID` = the `herdr.tab` label. |
| `idle`, `working`, `done`, `blocked` | the same words | as is. |
| — | `error` | `done`: the turn ended; the finish announcement reads the screen and says what went wrong. |
| `unknown` | status `null` after reporting (cleared), or any other word | `unknown`. |
| — | status `null`, never reported, known kind | the **screen fallback**: `terminal.read`, and `ParsePrompt` finding a question makes it `blocked`; otherwise `unknown`. Polled every few seconds only for such terminals. |
| `pane.read` sources | `terminal.read {lines}` (plain text; `stitched` for full-screen apps) | `visible` → `lines: 0`; the recent sources → `lines: n`. No ANSI read. |
| `agent.prompt` (refuses a blocked agent) | `terminal.send` (refuses `blocked` unless `force`) | never `force`; hc's `blocked` error is `ErrAgentBlocked`. |
| `pane.send_keys` | `terminal.keys` | the same key names (`enter`, `esc`, `up`, `down`, `tab`, `ctrl+c`, digits). |
| interrupt | `terminal.interrupt` (esc for status reporters, else ctrl+c) | as is. |
| `pane.agent_status_changed` | `status` event | → `StatusEvent`. |
| `pane_agent_detected` | the first `status` event of a terminal, or `created` of a known kind | → `FleetAgentDetected`. |
| `pane_exited` / `pane_closed` | `finished`, `lost` / `removed` | → `FleetPaneExited` / `FleetPaneClosed`. |
| resubscribe after `events_lost` | `missed`, a reconnect | → `Resync`. |
| `session.snapshot` | `terminal.list` | → `Snapshot` (panes = live local terminals, agents = the ones above). |
| `agent_session` (the worker's transcript) | not reported | none on hc: no worker history digest. |
| `ping` version | `hello` version | → `Pong`. |

## Conformance

`internal/herdr/conformance` is the behaviour contract shared by both
backends. Its scenarios drive a small backend-specific test driver over
`herdrtest` or `hctest`; both `internal/herdr` and `internal/herdrcore`
run the whole list. A new edge case belongs there first, and a fix for one
backend is accepted only when both drivers pass it.

The scenarios are:

1. a plain shell is never an agent;
2. Claude and Codex are detected fresh and resumed (`--resume` for Claude,
   `resume` for Codex), with the right kind and a sensible spoken name;
3. an exited agent leaves the terminal at a plain shell and is no longer
   listed as an agent;
4. a different agent started later in that same shell is detected with
   its new kind;
5. the phone shell can be opened, reused and closed with no agent, while
   an agent is running in it, and after that agent exited;
6. a new agent in a folder with an existing space joins that space (the
   old backend splits its pane; hc opens a new tab in the same space);
7. a blocked question with options is read and parsed the same way.

The exit decision follows the old protocol. `agent.list` contains only
panes that currently host a recognised agent. Exiting an agent emits
`unknown` and may keep the transition in the daemon's state history, but it
does not keep the terminal in the active agent list. hc therefore treats a
terminal as an agent only when it has a current status or a known agent
program in the foreground or start argv. `terminal.reported` remembers that
a program once reported; on its own it does not make a shell an agent.

## What v1 supports on hc

- the agent list, states and transitions, the blocked question with its
  options (the prompt parser on `terminal.read`), the finish announcement;
- reading a screen; sending a prompt, an option's keys and an interrupt;
- starting an agent (`terminal.run` of a shell in the folder, then the
  kind's command typed into it, ready when it reports status or shows a
  question) and closing one (`terminal.kill`). The shell is kept by the
  core and labelled `herdr.space` and `herdr.tab`, so herdr-app shows the
  agent: in the folder's existing space as a new tab, or in a new space
  (see "Spaces without the document");
- the phone shell, using that same `terminal.run` path with the user's
  login shell, `keep: true`, and the same folder-to-space match agent
  spawning uses. If a space already works in the folder, the shell is a
  new tab there; otherwise it opens a new space. It also carries
  `sheepdog=shell`, which the phone uses to find, reuse and close it. A
  plain phone shell stays off the agent list; once an agent reports or
  runs in it, the terminal is tracked as that agent while keeping the
  shell label;
- the agent list, spoken names and `/api/spaces` grouped by space, as with
  the old herdr;
- the app's agent window, no app change: live over `terminal.attach` in
  framed mode ([term-contract.md](../term-contract.md#on-herdr-core)), or
  as snapshots when the core cannot attach. hc sends the program's raw
  output and the app's terminal emulator draws it, so the daemon needs none
  of its own. hc has no scroll or mouse command: the daemon encodes the
  wheel and clicks itself, for programs that asked for the mouse, and a
  program without it has no scrollback to scroll.

Not in v1, each refused with the message the app already shows:

- anything that writes herdr-app's document: renaming a space (a name
  lives in the document), closing a space as a structure operation, real
  splits, worktrees (no `doc.set` from sheepdog). Closing a space is not
  turned into killing its terminals: that would end work the user did not
  name. Both are refused as before (`/api/spaces/{id}/rename` and `close`
  answer "spaces are not available here": the backend is not a
  `SpaceHost`);
- renaming an agent;
- scrollback in the live terminal for a program that does not use the
  mouse;
- the worker's own transcript (no `agent_session` on hc);
- terminals on machines added to the core.

## Choosing the backend

`herdr.backend` (CONFIG.md): `auto` (default), `herdr` or `hc`.
`herdr.hc_socket` overrides the hc socket path (default: hc's own rules
above). `auto` asks both sockets once at startup: the one that answers
wins; if both answer, the old herdr (behaviour unchanged for its users)
and the choice is logged as `daemon.herdr_backend`; if neither answers,
the old herdr, as today. Tests and the bench daemon set both sockets to
temporary paths, so no test can reach a real core.

## PR plan

1. **Interface**: `herdr.EventSource`, `herdr.Backend`, the
   Watcher over an `EventSource`, the app holding a `Backend`; this page.
   No behaviour change.
2. **hc client**: `internal/hc`, a typed JSON-RPC client for the methods
   above with a fake server (`internal/hc/hctest`). Nothing uses it yet.
3. **hc backend**: `internal/herdrcore` implementing `herdr.Backend`,
   `Spawner` and `Closer` on the client, the screen fallback, the
   detection and config keys (`internal/app/backend.go`), the doctor
   check, `/term`'s plain refusal and the user docs.
4. **Live terminal**: `internal/hc/framed.go` (framed mode, attach
   streams, credit), `internal/herdrcore/bridge.go` (the bridge the
   terminal socket runs per channel) and `web.TermDeps` for hc
   (`Check`, `Off`; `web.Deps.TermOff` is gone).

4. **Spaces from labels**: spawn labels its terminals and the snapshot
   reads the labels (below).

### Spaces without the document

herdr-app shows a core terminal in a space and tab when it carries the
labels `herdr.space=<id>` and `herdr.tab=<id>`, and makes a new space or
tab of an id it does not know. So sheepdog gets spaces from labels alone,
never reading or writing the document:

- `CreateWorkspace` runs the shell with `keep: true` and fresh space and
  tab ids; the new agent is a new space in herdr-app. `label` is ignored:
  herdr-app names an unnamed space after its first terminal.
- `SplitPane(paneID, cwd)` runs it in the space of `paneID` (its
  `herdr.space` label, from `terminal.get`) with a fresh tab id: the agent
  appears as a new tab of that space. The supervisor picks `paneID` the
  way it does for the old herdr (the first pane in the folder, or the
  space the user named), so "a new agent in an existing space" works
  unchanged, and so does a tap's folder that a space already works in: the
  agent joins that space as a new tab, never a second space with the same
  name. A `paneID` with no space label (a terminal herdr-app does
  not show) has no space to join, so the agent gets a new one; a missing
  `paneID` is `ErrPaneNotFound`.
- Ids: eight random hex digits (`newID`). herdr-app's own ids are not
  documented and it only compares them, so no format is copied; an id of
  its own, read off a live terminal's label, is reused as written.
- The terminals keep the `sheepdog=spawn` label, so they stay
  recognisable.

Implementation notes: `CreateWorkspace` and `SplitPane` both run a new
shell terminal (labels `sheepdog=spawn`, `herdr.space`, `herdr.tab`; kept,
no parent); `StartAgent` types the
kind's command into it (Codex with `--no-daemon`, which hc requires) and
returns, and the supervisor's existing wait polls `GetAgent`, which reports
the launch pending (`LaunchPending`, as herdr's `agent.start` does) until
the program shows in the terminal, and not found after the start timeout.
The phone shell uses the same run helper with `sheepdog=shell`; its space
comes from `herdr.PaneInDir`, which shares `herdr.PaneDirs`' real-path
folder match with agent spawning.
The fleet stream asks `terminal.get` on `created`, `restarted`, a
`foreground` change to a known agent program (one typed into an existing
shell) and a terminal's first status, to decide whether it is an agent.
`Interrupt` asks hc to watch for 2 s, below the call timeout. `sheepdog herdr watch` (a
developer tool) still talks to the old herdr only.

## Open questions

- Both running: prefer the old herdr (as above) or hc?
- `error` as `done`, or as `blocked`?
- Space names the user gave in herdr-app, from its document (read-only
  `doc.get`), once its shape is stable: today a named space is called by
  its folder's name here.
