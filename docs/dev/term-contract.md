# Terminal socket contract (`GET /term`)

The iPhone app's live agent window talks to the daemon over its own
WebSocket, `GET /term`, separate from `/ws`. The design and its reasons are
in [design/phone-terminal.md](design/phone-terminal.md); this page is the
wire contract. The Go types are in `internal/termproto`, and its contract
test parses every ` ```json ` block below, so the examples and the code
cannot drift.

## Endpoint and discovery

- `GET /term` on the daemon's port, with the same token as `/ws`
  (`Authorization: Bearer`, `?token=`, or the cookie; see
  [ws-contract.md](ws-contract.md#auth)). Without it: `401`.
- **Discovery.** `bot-ready` on `/ws` lists the feature `term` only when
  the daemon talks to a real herdr (not `--herdr=mock`) whose `ping`
  reports **0.9.2 or newer**. A client opens `/term` only then. When herdr
  is older or not running, `GET /term` answers `503` before the upgrade.
- **Several connections** (a phone and an iPad) are allowed. Each carries
  up to **4 channels**; the daemon holds at most **8** across all of them.

## Channels

A channel is one open window on one pane, with a client-chosen id `ch`
from 1 to 65535. For each channel the daemon runs one
`herdr terminal session control|observe <pane>` child process (the
*bridge*, `internal/herdr/bridge.go`) pointed at `herdr.socket`.

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
`{"shell":{"cwd":"…"}}` asks for a plain shell, which is refused with
`refused:shell_off` until the shell exists (phase 5).

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

Scroll herdr's scrollback, or send wheel events to a TUI that asked for the
mouse; `col` and `row` (together, optional) place the wheel. Mouse clicks
are encoded by herdr for the app's mouse mode and dropped when the app did
not ask for the mouse.

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

`term-ready.shell` says whether to offer the plain shell at all (always
false until phase 5). `term-opened` comes before the channel's first frame.
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
