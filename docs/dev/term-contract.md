# Terminal socket contract (`GET /term`)

The iPhone app's live agent window talks to the daemon over its own
WebSocket, `GET /term`, separate from `/ws`. The design and its reasons are
in [design/phone-terminal.md](design/phone-terminal.md); this page is the
wire contract. The Go types are in `internal/termproto`, and its contract
test parses every ` ```json ` block below, so the examples and the code
cannot drift.

## Endpoint and discovery

- `GET /term` on the daemon's port, authenticated like `/ws` before the
  upgrade (`Authorization: Bearer sds_<ticket>` or the session cookie; see
  [ws-contract.md](ws-contract.md#auth)). Without it: `401`. Revoking
  the device closes it with code **4003**.
- **Discovery.** `bot-ready` on `/ws` lists the feature `term` only when
  the daemon talks to a real herdr (not `--herdr=mock`) whose `ping`
  reports **0.9.2 or newer**, or to herdr-core whose `hello` lists
  `terminal.attach` ([herdr-core](#on-herdr-core)). A client opens `/term`
  only then. When herdr is older or not running, `GET /term` answers `503`
  before the upgrade.
- **Several connections** (a phone and an iPad) are allowed. Each carries
  up to **4 channels**; the daemon holds at most **8** across all of them.

## Channels

A channel is one open window on one pane, with a client-chosen id `ch`
from 1 to 65535. For each channel the daemon runs one
`herdr terminal session control|observe <pane>` child process (the
*bridge*, `internal/herdr/bridge.go`) pointed at `herdr.socket`. On
herdr-core the bridge is an attach stream instead ([On
herdr-core](#on-herdr-core)); the rest of this page holds for both.

- A channel targets only a **tracked agent** (a row of `GET /api/agents`).
  Any other pane is refused with `refused:not_tracked`, so the user's other
  herdr panes are never reachable.
- **`control`** resizes the real pane to the channel's size and takes
  input. herdr allows one controller per pane; a second one is refused with
  `refused:busy` unless it asks for `takeover`. herdr returns the pane to
  its layout size when the controller leaves, if the computer's herdr UI is
  attached.
- **`observe`** is read-only and renders at its own size without resizing
  the pane. A resize reopens its bridge at the new size.
- Closing a channel releases control. The pane, and the agent in it, keep
  running.

## On herdr-core

The wire is the same, and the app is the same; only the bridge differs
(`internal/herdrcore/bridge.go`, built on the framed connection of
`internal/hc/framed.go`). For each channel the daemon opens one framed hc
connection and a `terminal.attach` stream on the pane (the terminal's
address) and keeps its credit flowing, as hc's
"Framed mode" asks.

- **Frames.** hc sends the program's raw output, not rendered diffs. The app
  feeds every frame to a full terminal emulator, so the daemon passes the
  bytes through: each hc DATA frame is one terminal frame, the first paint
  is `full`, and `seq` counts frames from 1. The daemon has no emulator.
- **Resync.** A resize to the current size (and an overflow of the send
  queue) attaches again: the new first paint is a `full` frame, preceded by
  `ESC[0m ESC[H ESC[2J` so it replaces what the screen shows.
- **Modes.** hc has no single controller. `control` attaches as the active
  viewer (it sets the size, and `term-resize` sends a RESIZE); `observe`
  attaches read-only and never resizes the terminal (what size its stream
  has is hc's). A second `control` is never `refused:busy`, and
  `takeover` changes nothing; `taken_over` never happens.
- **Input** is written to the stream unchanged. hc never encodes keys: a
  program that asked for Windows' win32-input-mode (`CSI ? 9001 h`) is not
  supported.
- **Wheel and clicks.** hc has no scroll command and no mouse command, so
  the daemon writes the mouse bytes itself, in the encoding the program
  asked for (SGR with `?1006`, else the legacy one up to column and row
  223). It follows the program's `CSI ? 1000|1002|1003|1006 h|l` in the
  stream, nothing else. A scroll is `lines` wheel notches
  ([below](#wheel-notches)), and a click or drag is sent only when the
  program asked for the mouse (a drag, for motion). A program that did not
  ask for the mouse has no scrollback the daemon can move: its `term-scroll`
  is dropped.
- **Close reasons.** The program exiting or being killed is `gone`; hc
  stopping, a lost shell or a broken connection is `herdr`.
- **Phone shell.** The same shell target works. hc runs the user's login
  shell with `keep: true` in the folder, labels it `sheepdog=shell`, and
  puts it in the folder's existing space as a new tab, or in a new space.
  `term-ready.shell` and `/api/hello`'s `shell` are true only while
  `terminal.attach` works.

## Control messages (text frames)

JSON objects with a `type`, the same convention as `/ws`.

**Client → daemon**

`term-hello` must be the first message. Anything else first gets a
`term-error` and the connection closes.

```json
{"type":"term-hello","protocol":1,"client":{"name":"Sheepdog","version":"0.1.13","platform":"iOS 26.1"}}
```

Open a window. `mode` is `control` or `observe`; `takeover` (default
false) takes control from another device. A `target` of
`{"shell":{"cwd":"…"}}` asks for the plain shell in that folder (see
[The plain shell](#the-plain-shell)); `term-opened.kind` is then `shell`.

```json
{"type":"term-open","ch":1,"target":{"pane_id":"w1:p2"},"mode":"control","cols":58,"rows":30}
{"type":"term-open","ch":2,"target":{"pane_id":"w1:p2"},"mode":"control","cols":58,"rows":30,"takeover":true}
{"type":"term-open","ch":3,"target":{"shell":{"cwd":"/Users/me/Documents/app"}},"mode":"control","cols":58,"rows":30}
```

Resize (the client debounces it to one per 150 ms). A resize to the
current size asks for a full repaint, the resync primitive.

```json
{"type":"term-resize","ch":1,"cols":44,"rows":28}
```

Scroll: `lines` of scrolling in direction `dir`. The daemon turns it into
that many **wheel notches** (see [below](#wheel-notches)). `col` and `row`
(together, optional) place the wheel; without them it lands on the middle of
the channel's grid. Mouse clicks are encoded by herdr for the app's mouse mode
and dropped when the app did not ask for the mouse.

```json
{"type":"term-scroll","ch":1,"dir":"up","lines":3}
{"type":"term-scroll","ch":1,"dir":"down","lines":3,"col":10,"row":5}
{"type":"term-mouse","ch":1,"action":"down","col":12,"row":5}
{"type":"term-mouse","ch":1,"action":"up","button":"left","col":12,"row":5}
{"type":"term-close","ch":1}
```

**Daemon → client**

```json
{"type":"term-ready","protocol":1,"limits":{"channels":4},"shell":false}
{"type":"term-opened","ch":1,"pane_id":"w1:p2","mode":"control","cols":58,"rows":30,"kind":"agent"}
{"type":"term-closed","ch":1,"reason":"released"}
{"type":"term-closed","ch":2,"reason":"refused:busy","can_take_over":true}
{"type":"term-error","ch":1,"message":"no control channel"}
{"type":"term-free","ch":2,"pane_id":"w1:p2"}
```

`term-ready.shell` says whether to offer the plain shell at all on this
connection (the gate below; the daemon checks it again on every open). `term-opened` comes before the channel's first frame.
`term-error` is a problem that does not end the channel (`ch` 0 is about the
connection). `term-free` goes to every `observe` channel on a pane when a
`control` channel this daemon ran on that pane is released (closed by its
client, or its connection ended): the watching client may open `control`
now, without `takeover`. A controller outside the daemon gives no signal.

`term-closed.reason` is one of:

| Reason | Meaning | What the app does |
|---|---|---|
| `released` | The client closed it, or the connection ended | Nothing |
| `taken_over` | Another device took control | Offer to take it back |
| `gone` | The pane closed, or herdr does not know it | "This agent closed." |
| `herdr` | The bridge failed (herdr not running, crashed, updating) | Fall back to the `screen` window |
| `refused:busy` | Another device controls the pane; `can_take_over` is true | Offer takeover, or open `observe` |
| `refused:too_many` | 4 channels on this connection or 8 on the daemon | Close a window first |
| `refused:not_tracked` | Not an agent the daemon tracks | Back to the agent list |
| `refused:shell_off` | No plain shell on this connection | Do not offer it |
| `refused:outside_projects` | The shell's folder is outside `supervisor.project_roots` and is no tracked agent's folder | Open it from the agent instead |

## Wheel notches

A scroll is `lines` wheel notches, one per line, whatever the pane runs. herdr
moves its own scrollback by a `terminal.scroll` command's `lines`, but sends a
full-screen app that asked for the mouse (Codex, on the alternate screen with
mouse reporting) exactly **one** wheel event per command, whatever `lines`
says. The daemon (`Bridge.Scroll`) therefore sends one command per line:
the same distance in the scrollback and the right number of notches for the
app, so it never has to know which one the pane is in. Consequences:

- The client counts lines, not modes: a drag of N lines is `lines: N`, and
  scrolling back down by the same count (its "↓ Latest") undoes it in both.
- One message is capped at **1000** notches (`herdr.MaxWheelNotches`); a drag
  frame is a few, so only a runaway client reaches it.
- An app that accelerates fast wheel events (Claude Code's full-screen mode
  does) still does, so a fast flick moves it further than the finger; a
  slow drag is one line per notch. The daemon does not pace notches.
- A TUI ignores a wheel outside its scrollable area and herdr's default cell
  is the top-left corner, so a scroll without `col` and `row` lands on the
  middle of the grid (kept current by `term-resize`).

## Binary frames

Big-endian.

- **Daemon → client, frame:** `0x01 | ch u16 | seq u32 | cols u16 | rows
  u16 | flags u8 | ANSI bytes`. Flag bit 0 is `full`. The bytes are
  herdr's TerminalAnsi render stream: the changed cells as cursor moves, SGR
  and text, each frame wrapped in DEC 2026 synchronized output. herdr has
  already resolved alternate screen, mouse modes and scrollback, so the
  client needs only a small VT subset. `seq` is herdr's per-attach
  sequence: a gap means a missed frame.
- **Client → daemon, input:** `0x02 | ch u16 | raw input bytes`, at most
  64 KB. These are the bytes a terminal sends (UTF-8 text, `\r` for Enter,
  `\x1b` for Esc, `\x1b[A` for Up, `\x03` for ctrl+c). The daemon forwards
  them unchanged and never interprets them. Input on an `observe` channel
  gets a `term-error`.

## Never dropped silently

Frames are diffs, so a lost frame corrupts the screen.

- The daemon reads every bridge all the time (herdr drops a stream reader
  that stalls for 30 s).
- Each channel's send queue holds 256 KB. On overflow the daemon throws
  away that channel's queued diffs, skips diffs until a full frame, and asks
  herdr for one: a same-size resize for `control`, a reopen for `observe`.
- The client checks `seq`. On a gap, or a diff before any full frame, it
  sends `term-resize` with its current size.
- The daemon pings every 20 s. A client that does not answer within 60 s is
  closed and its bridges released.

## The plain shell

A phone shell is a plain shell in a project folder. On the old herdr the
daemon opens it as a tab in a workspace labelled `phone shell` (the
workspace itself the first time). On herdr-core it opens as a new tab in
the space already working in that folder, or in a new space when none
does. Either way the user sees it on the computer too.

- **One per folder, reused.** `term-open {target:{shell:{cwd}}}` attaches
  to the folder's shell when there is one, else opens it. Opens are
  serialised, so two quick taps make one shell. After a daemon restart the
  daemon finds old-herdr shells in the `phone shell` workspace and
  herdr-core shells by their `sheepdog=shell` label.
- **Folder.** `cwd` is resolved (symlinks, `~`) and must lie inside a
  `supervisor.project_roots` entry or be a tracked agent's own folder (a
  worktree herdr put elsewhere). An empty `cwd` is the first project root.
  Anything else is `refused:outside_projects`.
- **The gate** is the computer's shell switch, [`web.shell`](../../CONFIG.md)
  (on by default, set on the setup page), the same for every paired
  device on every network. It applies to the shell target, to a shell's
  `pane_id` (nobody attaches to a shell while the switch is off) and to
  closing one. Off: `refused:shell_off`; `term-ready.shell` is false.
- **Leaving** the window (`term-close`) only detaches; the shell keeps
  running. **Closing** it ends the pane:

  `POST /api/shells/{pane_id}/close` → `200 {"ok":true}`; `403` where the
  shell is gated off; `404` for a pane that is no phone shell. On
  herdr-core the backend also verifies the terminal carries
  `sheepdog=shell` before `terminal.kill`.

Phone shells are not agents: a plain shell is not in the agent list and
the orchestrator is not told about it. On herdr-core a terminal carrying
`sheepdog=shell` stays off the agent list.

## Event log

Metadata only, never screen content or input:

- `web.term.connect {client_id, remote_kind}` (`loopback`, `tailscale` or
  `lan`), `web.term.hello {client_id, protocol, client}` and
  `web.term.disconnect {client_id, reason}`
- `web.term.open {ch, pane_id, mode, kind, cols, rows, remote_kind,
  takeover}` and `web.term.refused {ch, pane_id, reason}`
- `web.term.resize {ch, pane_id, cols, rows}`, at most one a second per
  channel
- `web.term.close {ch, pane_id, reason, seconds, frames, bytes_out,
  bytes_in, resyncs}`
- `web.shell.open {client_id, pane_id, cwd, remote_kind, reused}`,
  `web.shell.close {pane_id, remote_kind}` and `web.shell.error {client_id,
  cwd, error}` (herdr could not open the shell)
- `web.setup_saved {step: "shell", enable}` when the setup page's switch
  changes
