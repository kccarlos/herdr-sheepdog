# Phone terminal: a real agent window on the iPhone

Status: **being built** (phase 0 decided 2026-10-20). Phases 1–5 below are
the implementation plan.

**Goal.** Anything the user can do at the computer, they can do from the
phone, so they can leave the computer. The phone talks only to the daemon
(HTTP and WebSocket over the LAN or Tailscale, with token auth). It never
uses SSH.

**Decisions**

| Question | Decision |
|---|---|
| Renderer | **SwiftTerm** (MIT, pure-Swift SPM source), pinned to an exact 1.x version, behind our own `TerminalSurface` protocol. libghostty (through libghostty-spm) is the planned replacement if SwiftTerm fails the width or speed checks in phase 2. |
| Transport | **(b) herdr's `terminal session control` bridge.** For each open window the daemon runs one `herdr terminal session control <pane> --cols C --rows R` child. It speaks newline-delimited JSON on stdin and stdout, with no PTY. The daemon forwards the frames to the phone over a new terminal WebSocket. |
| Resize | The phone **resizes the real pane** while it is in control. herdr returns the pane to its layout size on detach when a herdr UI client is attached, which is the normal case (measured below). |
| Fallback | Older daemons, and herdr older than 0.9.2, keep today's `screen` path. It gets ANSI colour (option c) and draws in the same renderer. |
| Plain shell | A herdr pane that the daemon opens for the phone. It uses the same bridge, socket and renderer, and gets its own security gate. |

Heeler (Apache-2.0) was read for its trade-offs only; none of its code is
used. Heeler attaches over SSH to `herdr agent attach --takeover` in a PTY
and renders with libghostty-spm. Our setup differs in two ways, and both
change the answers:

- **We have a daemon on the computer.** It can use herdr's structured bridge
  instead of driving a PTY.
- **We render a stream that herdr has already composed** (below), not raw
  program output.

---

## 1. What herdr actually sends (the fact that drives both choices)

Every attach path (`agent attach`, `terminal attach`, `terminal session
observe|control`) uses herdr's **TerminalAnsi render stream**.

- herdr runs its own emulator (ghostty-vt) on the real PTY.
- Per client, a *blit encoder* compares the pane grid with what that client
  last saw and sends only the **changed cells as ANSI**: cursor moves, SGR
  with 256 colours or true colour, and text including wide characters.
- Each frame is wrapped in synchronized output (DEC 2026).
- Source: herdr `src/server/render_stream.rs` (`prepare_frame`) and
  `src/protocol/wire.rs` (`TerminalFrame {seq, width, height, full, bytes}`).

Consequences:

- **The app does not see alt screen, mouse modes, scrollback or bracketed
  paste.** herdr resolves all of them on the server. Scrolling is a
  `terminal.scroll` command: herdr either moves its own scrollback or
  forwards wheel events to a TUI that asked for mouse. Clicks are
  `terminal.mouse` commands, which herdr encodes for whatever mouse mode the
  app enabled (0.9.2+). Heeler had to build a DEC-mode tracker and wheel
  translation on the phone; we need neither.
- **The byte rate is bounded by screen size, not by output rate.** herdr
  coalesces changes into frames, so a fast build log costs a few frames per
  second.
- **The phone's emulator only needs a small VT subset**, but it must agree
  with ghostty-vt on character widths (see §2, risk R1).

## 2. Renderer

What each candidate offers, from public sources (gathered 2026-10-20) plus
what Heeler records in its repository. App sizes are **estimates**.

| | SwiftTerm | libghostty-spm (`GhosttyTerminal`) | xterm.js in WKWebView | Own grid view on a parser |
|---|---|---|---|---|
| CJK, emoji, true colour | Good. CJK alignment fixed in 1.14; grapheme tests in 1.99 | Best (Ghostty's width and grapheme engine) | Good with the graphemes addon | Depends on the parser |
| Fast output | CoreText or Metal (1.12+) | Metal, paced to the display | WebGL is fast, but every byte crosses the JS bridge | CoreText per dirty row; fine for one pane |
| App size (estimate) | ~1–3 MB of Swift | ~10–20 MB linked arm64 (the multi-platform download is a 76.5 MB zip) | ~1–2 MB of JS | ~1–5 MB |
| Xcode Cloud | Plain SPM source; nothing to install | Prebuilt binary xcframework: needs a checksum pin, and Xcode 26 / Swift 6.2; Zig only to rebuild it | Bundle the JS as a resource | libghostty-vt has to be built with Zig and vendored |
| Selection | Built-in selection engine | No native selection (Heeler shows a text sheet instead) | DOM selection; poor on touch | Build it ourselves |
| VoiceOver | **Exposes terminal text to VoiceOver on iOS since 1.14** | **None** (Heeler: "no per-line VoiceOver") | Screen-reader mode in the DOM; unverified on mobile Safari | Full control, but all our own work |
| TUI support | Alt screen, mouse, DEC 2026, Kitty keyboard (1.12+) | All of them | Alt screen, mouse, DEC 2026; no Kitty keyboard | Whatever the parser supports |
| iOS fit | UIKit view, keyboard accessory, hardware keyboard, iPad | Hardware keys and IME. Heeler worked around its pinch gesture, touch-to-first-responder, scroll and tap-offset behaviour | Weak touch support (xterm.js #5377 open) | Build it ourselves |
| Maintenance, licence | MIT; ~1.7k stars; very active (1.99.0 on 2026-10-08); used by Secure ShellFish, La Terminal, CodeEdit; 2.0 will break APIs | MIT; one maintainer, weekly releases, unofficial wrapper whose API changes | MIT; ~21k stars | libghostty-vt's C API is officially "WIP, not stable" |

**Why SwiftTerm:**

1. **The stream needs little.** herdr has already resolved TUI modes (§1).
   Ghostty's lead in raw PTY throughput and Kitty keyboard support matters
   for a local shell, which is not what we render.
2. **Accessibility is a hard rule** ([AGENTS.md](../../../AGENTS.md):
   VoiceOver on every screen). SwiftTerm is the only candidate that exposes
   the terminal text to VoiceOver on iOS today.
3. **It is source, not a binary.** That means no binary to trust, no
   checksum churn, and the smallest Xcode Cloud risk. It is also the
   project's first SPM dependency, so pin an exact version in
   `ios/project.yml`.
4. **We keep the option to switch.** The app talks to a `TerminalSurface`
   protocol: `feed(bytes)`, `onSizeChange(cols, rows)`, `snapshotText()`,
   `setFontSize(_)` and `accessibilityLines`. Moving to libghostty-spm, or
   to a CoreText view on official libghostty-vt once its API is stable, then
   touches one file.

xterm.js is rejected because of touch, IME and keyboard behaviour on iOS,
plus the bridge cost. A Rust `alacritty_terminal` over FFI is rejected
because it puts a Rust toolchain on Xcode Cloud. hterm is effectively
frozen.

**Phase 2 has to prove** (the first PR of phase 2, before any UI work):

- **R1, width agreement.** herdr measures cell widths with ghostty-vt. If
  SwiftTerm disagrees on a VS16 emoji (`☁️` = U+2601 U+FE0F, seen in a real
  shell prompt during the spike), a ZWJ sequence or an ambiguous-width CJK
  character, the rest of that row is shifted.
  - **Test:** a fixture of recorded herdr frames (CJK, emoji, VS16, ZWJ,
    box drawing, true colour). Feed it to SwiftTerm and compare the
    resulting grid with the text herdr's `pane.read` reports for the same
    pane.
  - **If it fails:** pass the case through SwiftTerm's width override, or
    switch to libghostty-spm. Moving to libghostty-spm is the documented
    trigger for revisiting this decision.
- **R2, throughput.** Replay a 60-second capture of Claude Code streaming,
  plus a `seq 1 200000` burst, on an iPhone 12-class device.
  - **Pass:** no frame drops at 60 fps, and main-thread time under 4 ms per
    frame.

## 3. Transport

### What was measured

The spike ran against a **scratch herdr session** (`herdr --session <scratch>
server`, herdr 0.9.5, protocol 22). It never touched the user's session.
The scripts lived in the session scratchpad and were not committed.

| Measurement | Result |
|---|---|
| Keystroke to echoed frame (control bridge, local shell, 20 keys) | p50 **18.1 ms**, p90 18.7 ms, max 19.8 ms. This includes the shell's own echo. |
| One-character diff frame | **118 bytes** (sync wrapper, cursor move, SGR, text) |
| First frame, 44×30 | `full: true`, 1.7 KB |
| `terminal.resize` 44×30 → 60×25 | The next frame (full, 60×25) arrived in **2.4 ms**. `tput` in the pane then printed `60x25`. |
| `seq 1 20000` | **4 frames, 1.3 KB in total**. herdr coalesces, so output volume never reaches the phone. |
| Frames wrapped in DEC 2026 | 34 of 34 |
| `terminal.resize` to the same size | The next frame is `full: true`. This is the **resync primitive**. |
| A second controller without `--takeover` | Refused: `terminal.closed`, reason "already has an attached client; retry with --takeover" |
| A second controller with `--takeover` | The old controller gets `terminal.closed` "terminal attach taken over" and exits 0. The new one gets a full frame. |
| An observer (100×20) while another client controls | It gets frames at **its own size** without resizing the pane. It survives takeover and release. |
| Release or killed bridge, **with a herdr UI client attached** (120×40 PTY) | The pane goes back to the UI's layout size (93×39) in both cases |
| Release, **with no UI client** (headless server) | The pane **keeps the phone size** (44×30). The next UI client to attach resizes it to its layout (93×39). |
| Resize back to the pre-attach size, then release (headless) | Restored (119×40) |
| Input from the phone, as seen in the computer's herdr UI | It shows up live |
| `pane.read --format ansi` (option c) | ~4 ms per call through the CLI. Coloured lines at the **pane's** width. No cursor position and no change events, so it has to be polled. |

**Other details from the herdr source:**

- *Attach owners.* A controller is herdr's "direct attach owner". The herdr
  UI on the computer is not one, so a phone attaching **does not need
  `--takeover` against the computer**. Takeover only matters against another
  attach client: Heeler, a second phone, or an iPad.
- *Resize lock.* While a controller is attached, herdr puts the terminal in
  `direct_attach_resize_locks`, so the computer's layout cannot resize it
  back. The lock is removed on detach (`remove_client` in
  `src/server/headless.rs`). So, inferred from the source rather than
  measured, the computer's UI keeps drawing the pane while it is locked at
  the phone's size.
- *Not tested yet.* The claim that a phone needs no `--takeover` against the
  computer comes from the source. Phase 1's first PR confirms it with a
  test.
- *Retry needed.* Attach is refused with "has a read in progress; retry"
  while an alt-screen `pane.read` is pending, and the daemon's own reads can
  trigger this. The bridge retries with backoff: 100, 200 and 400 ms.
- *Slow readers are dropped.* herdr disconnects a stream reader that makes
  no write progress for 30 s (0.9.1). The daemon must always drain the
  bridge, whatever the phone is doing.
- *What the bridge drops.* The CLI bridge drops `MouseCapture`, the
  Kitty-keyboard flags, OSC 52 clipboard, bell and window title. So text an
  app copies through OSC 52 does not reach the phone (use selection
  instead), and the phone cannot tell whether the app wants mouse events.
  herdr drops clicks that the app did not ask for, so sending them is
  harmless.
- *Finding the server.* The bridge finds herdr's client socket from
  `HERDR_SOCKET_PATH`: `<dir>/<stem>-client.sock` next to the API socket.
  The daemon sets it to `herdr.socket`.
- *Versions.* `terminal session observe|control` exists since **0.7.2**.
  `terminal.mouse` exists since **0.9.2**. The herdr `ping` reports
  `version`.

### The three options compared

| | (a) `agent attach --takeover` in a Go PTY | **(b) `terminal session control` bridge** | (c) `pane.read` with ANSI |
|---|---|---|---|
| Latency | Same frames as (b), plus PTY hops | ~18 ms echo; frames pushed as they change | Polling (250 ms–1 s) plus ~4 ms per read |
| Resize | PTY `TIOCSWINSZ` → herdr `Resize` | An explicit `terminal.resize`, plus same-size resync | None: the pane width is fixed, so a phone either wraps or scrolls sideways |
| Computer's view | Pane locked to the phone size while attached | Same as (a) | Untouched |
| Detach | herdr restores the layout size when a UI client exists | Same as (a), on both release and a killed bridge | Nothing to restore |
| Several viewers | One writer; no observers | One controller, any number of observers at their own sizes | Unlimited |
| What the phone must handle | herdr's host handshake: DA1 (`ESC [ c`) and DECRQM `?1016` queries the phone has to answer, `1049h`, mouse-mode toggles it would have to translate into raw SGR mouse bytes, a detach key | Only frames. Input, resize, scroll, mouse and release are JSON commands. | Text snapshots |
| Moving parts | A child process **and** a PTY | A child process, no PTY | None new |

**(b) wins.** It produces the same frames as (a) without a PTY or host
handshake, and it adds observers, explicit scroll and mouse, and a clean
release. The captured start of (a) began with mouse-mode resets, `1049h`,
`?2031l`, a `?1016$p` query and `ESC [ c`, all of which a phone would have
to fake. (c) is kept only as the colour upgrade for the old `screen` path.

**Later option, not chosen now.** The daemon could speak herdr's client
protocol directly on `<stem>-client.sock` (bincode, "endpoint generation 1",
variant order frozen) and skip the child process. That protocol is herdr's
own, not a published API, and the JSON bridge exists for "bridge processes"
(herdr CHANGELOG 0.7.2). Revisit it only if process count or start-up time
(~250 ms for the handshake, from herdr's client log) becomes a problem.

## 4. Protocol: the terminal socket

### Why a separate socket, not `/ws`

- `/ws` binary frames are untagged PCM audio. A legacy client would play
  terminal bytes as sound.
- `/ws` allows one client, and a new one replaces the old. A phone and an
  iPad (or two windows) must be able to coexist.
- A burst of terminal frames would sit in front of audio on the same TCP
  stream.

So the terminal gets **`GET /term`**: a WebSocket on the same port, with the
same `authFunc` and token (header, `?token=` or cookie) as `/ws`.

**Discovery.** `bot-ready` (on the authenticated `/ws`) lists the feature
`term`. The daemon lists it only when herdr's `ping` reports 0.9.2 or newer,
so the app never has to compare versions itself. The public `/api/hello`
gets nothing new: herdr's version and whether a shell is reachable are
visible only after auth (`term-ready.shell`).

### Channels (several windows per client)

One `/term` connection per app carries **channels**. A channel is one open
window on one pane. Why one connection with channels, not one socket per
window:

- one auth and one reconnect;
- one place to enforce per-client limits;
- the channel id costs 2 bytes.

Head-of-line blocking does not matter here, because every channel's byte
rate is bounded by its screen size (§1).

- **Up to 4 channels per connection, and up to 8 bridges across the
  daemon.** Beyond that, `term-open` is refused with `too_many`.
- **Several connections at once** (phone and iPad) are allowed. For each
  pane, herdr decides who controls (one controller; takeover is explicit).

### Framing

Text frames are JSON control messages, `{"type": …}`, the same convention
as `/ws`. The Go types live in a new `internal/termproto` package with a
contract test like `wsproto`'s. Each message is listed here as `type:
fields`.

**Client → daemon**

- `term-hello: {protocol:1, client:{name,version,platform}}` is sent first.
  The daemon answers `term-ready: {protocol:1, limits:{channels:4},
  shell:bool}`.
- `term-open: {ch, target, mode, cols, rows, takeover?}`:
  - `ch` is a client-chosen id from 1 to 65535.
  - `target` is `{pane_id}` (a tracked agent or a phone shell) or
    `{shell:{cwd?}}` (§4.6).
  - `mode` is `control` or `observe`.
  - `takeover` defaults to false.
- `term-resize: {ch, cols, rows}`. The client debounces it (150 ms). A
  resize to the current size asks for a full repaint.
- `term-scroll: {ch, dir:"up"|"down", lines, col?, row?}`
- `term-mouse: {ch, action:"down"|"up"|"drag", button?, col, row}`
- `term-close: {ch}` releases control. The pane keeps running.

**Daemon → client**

- `term-opened: {ch, pane_id, mode, cols, rows, kind:"agent"|"shell"}`
- `term-closed: {ch, reason}`. `reason` is one of:
  - `released`
  - `taken_over` (another device took control)
  - `gone` (the pane closed or is no longer tracked)
  - `refused:<why>` (`busy`, which comes with `can_take_over: true`;
    `too_many`; `shell_off`; `not_tracked`)
  - `herdr` (the bridge failed; the app falls back to `screen`)
- `term-error: {ch, message}` is a problem that does not end the channel,
  such as a retry.

**Binary frames.** Big-endian:

- daemon → client, frame: `0x01 | ch u16 | seq u32 | cols u16 | rows u16 |
  flags u8 (bit0 = full) | ANSI bytes`. `seq` is herdr's per-bridge
  sequence, so a gap means the client missed a frame.
- client → daemon, input: `0x02 | ch u16 | raw input bytes`. These are the
  bytes a terminal would send: text as UTF-8, Enter as `\r`, Esc as `\x1b`,
  arrows as `\x1b[A`…, ctrl+c as `\x03`. The app encodes keys; the daemon
  forwards the bytes as `terminal.input` and never interprets them. Capped
  at 64 KB per frame.

### Backpressure

The frames are **diffs**, so dropping one corrupts the screen. Never drop
silently:

- The daemon reads every bridge continuously, so herdr's 30 s stall rule
  never fires.
- Each channel has a bounded send queue (256 KB). On overflow the daemon
  throws away the queued diffs for that channel, marks it *stale*, and, once
  the socket drains, sends a same-size `terminal.resize`. herdr answers with
  a full frame.
- Observe bridges have no stdin, so they cannot ask for a repaint. An
  overflowing observer is closed and reopened, which also gives a full first
  frame.
- The client checks `seq`. If there is a gap, or a diff arrives before any
  full frame, it sends `term-resize` with its current size.
- WebSocket ping every 20 s. A client silent for 60 s is closed, and its
  bridges released.

### Resize

- **Only a `control` channel resizes the pane.** `observe` renders at its
  own size, cropped or padded by herdr.
- **The app computes `cols × rows` from the view size and font size:**
  - SF Mono 11 pt is about 58 columns on an iPhone 17 Pro in portrait.
  - Pinch changes the font size, which changes the grid, which sends one
    debounced `term-resize`.
  - The keyboard showing or hiding resizes the pane too, so a TUI's input
    line stays visible.
- **Restoring on detach.**
  - herdr restores the layout size itself whenever the computer's herdr UI
    is attached (measured).
  - With no UI client the pane keeps the phone size until a UI attaches
    (measured: the first UI attach fixes it). That is harmless, so phase 1
    does not attempt an exact restore.
  - herdr's API reports the layout rect (120 wide) rather than the PTY size
    (119), so an exact restore would need a herdr field for the PTY size.
    That is an upstream ask, not a blocker.

### Input: raw bytes versus prompts

There are two paths, deliberately:

- **The composer's Send** still uses `POST /api/agents/{pane}/input` (`text`,
  through `agent.prompt`). That path guarantees submission and logs
  `web.agent_input`, and it updates `current_task` and tells the
  orchestrator. Its text limit goes up from 4 000 bytes to **64 KB**.
- **Typing into the terminal and the key bar** send raw bytes on the
  channel.
  - **Approvals:** if raw keys answer an approval prompt, the watcher sees
    the pane leave `blocked`, and the existing rule closes the pending
    question. That is the same rule as for the window's `enter` today.
  - **Telling the orchestrator:** it gets **one** transcript line when the
    channel closes ("the user worked in Auth fix's window from the phone for
    3 min"), never the keystrokes.

### Auth and gating

- **Every window needs the token.** Like the existing window routes, a
  channel can target only **tracked agents** and the **phone shells the
  daemon opened**. Any other herdr pane is `refused:not_tracked`.
- **Raw control of an agent pane is allowed on every network the token
  works on.** It gives no more power than a prompt does: Claude Code's `!`
  or a prompt can already run commands.
- **The plain shell is gated by the address the request came from**
  (`RemoteAddr`, classified with the existing `pair.Kind`), not by the bind
  address:

  | Client address | Shell |
  |---|---|
  | loopback | on |
  | Tailscale (100.64.0.0/10, fd7a:115c:a1e0::/48) | on |
  | anything else (plain-LAN HTTP, unencrypted) | **off** unless `web.shell_lan = true`, which is set only from the setup page (loopback-only) |

  The reason is that plain-LAN HTTP sends the token and every keystroke in
  clear text.
- `term-ready.shell` tells the app whether to offer the shell at all, so
  there is never a button that fails.

### Event log

Log metadata only, never screen content or input:

- `web.term.connect {client_id, remote_kind}` and `web.term.disconnect
  {client_id, reason}`
- `web.term.open {ch, pane_id, mode, kind, cols, rows, remote_kind,
  takeover}` and `web.term.refused {ch, pane_id, reason}`
- `web.term.resize {ch, pane_id, cols, rows}`, rate-limited to one a second
  per channel
- `web.term.close {ch, pane_id, reason, seconds, frames, bytes_out,
  bytes_in, resyncs}`
- `web.shell.open {pane_id, cwd, remote_kind}` and `web.shell.close`

### Fallback for old daemons and old herdr

| Daemon / herdr | What the app does |
|---|---|
| A new daemon with herdr 0.9.2 or newer | `/term` |
| A new daemon with herdr older than 0.9.2 (no `term` feature) | `screen` over `/ws` with the new `ansi: true` option (option c): coloured text at the pane's width, drawn in the same renderer, with sideways scroll, and today's 8-key input |
| An old daemon (no `term`; `screen` feature) | Today's `screen` frames (plain text), drawn in the same renderer |
| A legacy daemon (no `screen`) | Polls `GET /api/agents/{pane}/screen`, as today |

## 5. Phone UX sketch

**Principles:**

- The terminal is the screen; everything else is one tap away.
- One accent colour (orange, for "needs you").
- Secondary actions go in menus.
- Every state has its plain fix.

### Agent window (control)

```
┌──────────────────────────────────────┐
│ ‹ Agents   Auth fix ● working    ⋯   │  name, state chip, actions menu
├──────────────────────────────────────┤
│ ● Running go test ./...              │
│   ok  internal/auth   0.41s          │  live terminal, the agent's own
│   ok  internal/web    2.10s          │  colours, sized to the phone
│ ✻ Thinking…                          │  pinch to zoom, drag to scroll
│                                      │  (herdr scrollback or TUI wheel)
│ › _                                  │  tap = click when the TUI wants it
│                                      │
│                       [ ↓ Latest ]   │  shown only when scrolled up
├──────────────────────────────────────┤
│ ┌──────────────────────────────┐ ➤  │  composer: a prompt goes through
│ │ Ask Auth fix…              📎│    │  agent.prompt; 📎 attaches
│ └──────────────────────────────┘    │
│  esc  ⇥  ↑  ↓  ⌃C  ⏎  ⌨︎  ···      │  key bar; ⌨︎ = type straight into
└──────────────────────────────────────┘  the terminal; ··· = more keys
```

- **Approvals.** When the agent is blocked, the approval card appears above
  the composer with the options as buttons, the same as the Agents tab.
  The terminal stays visible.
- **"Typing in terminal" mode** (⌨︎). The composer hides and the system
  keyboard sends raw bytes. The key bar stays, and its ⌃ key latches for
  the next character.
- **"··· more keys"** opens a sheet: digits 1–9, ⇧⇥, PgUp/PgDn, Home/End,
  ⌃D, ⌃Z and a custom-text row.
- **Observe-only.** It is shown as a slim banner "Viewing: Heeler on
  another device has control · Take over" and the composer is disabled.
  Takeover asks for confirmation.
- **Empty and error states:**
  - "Connecting…" shows a skeleton.
  - "herdr is not running: start herdr on your computer."
  - "This agent closed. ‹ Back to agents."
  - "Your computer's herdr is older than 0.9.2, so this is a read-only
    snapshot. Update herdr for the live window." This is the fallback path.
- **VoiceOver.** The terminal exposes its lines; the rotor moves by line;
  the "Latest" action reads the last screen. Every key-bar button has a
  label ("Escape", "Control C").
- **Dynamic Type.** It scales the chrome. The terminal follows pinch and
  never truncates its labels.

### Actions menu (⋯)

```
┌──────────────────────────┐
│ ✎  Rename                │
│ ⧉  Changes (12 files)    │  → changes view
│ ⌘  Open shell here       │  only when the shell is allowed
│ ─────────────────────── │
│ ■  Stop current turn     │  confirm: sends Esc / ⌃C per agent kind
│ ✕  Close agent           │  destructive; confirm with the agent's name
└──────────────────────────┘
```

**New agent** lives on the Agents tab as a `+` button:

```
┌──────────────────────────────────────┐
│ New agent                       Done │
├──────────────────────────────────────┤
│ Agent     [ Claude Code ▾ ]          │
│ Project   [ sheepdog-bench ▾ ]       │  only supervisor.project_roots
│ ○ In the project folder              │
│ ● In a new worktree  [ fix-login ]   │  herdr worktree helper
│ First prompt (optional)              │
│ ┌──────────────────────────────────┐ │
│ └──────────────────────────────────┘ │
│                         [ Start ]    │
└──────────────────────────────────────┘
```

### Attachments (📎)

There is one menu: Photo, Camera, File and Paste image.

- The file uploads to the daemon, and a chip appears in the composer:
  `🖼 screenshot.png ✕`.
- On Send, the prompt gets the file's path on the computer. Claude Code and
  Codex both read image and file paths given in a prompt.
- **Errors:** "Too big (max 20 MB)" and "Upload failed: retry".

### Changes view

```
┌──────────────────────────────────────┐
│ ‹ Auth fix   Changes        Refresh  │
├──────────────────────────────────────┤
│ main ← fix-login · 3 files +42 −7    │
│ ▸ internal/auth/token.go     +30 −5  │
│ ▾ internal/web/api.go        +12 −2  │
│   @@ -88,6 +88,8 @@                  │
│    func handle(…) {                  │
│   + if tok == "" {                   │  green and red tints; the line
│   +   return errNoToken              │  wraps by default, with a ↔
│   - log(tok)                         │  toggle for wide lines
└──────────────────────────────────────┘
```

- It is read-only (`git diff` in the agent's working directory).
- **Empty state:** "No changes yet."
- **Not a repository:** "This folder is not a git repository."

### Shell

- **Where it opens:** from the Agents tab `+` → "Shell", or the actions
  menu → "Open shell here".
- **What it is:** the same window as an agent's, titled "Shell ·
  sheepdog-bench", with no composer. It opens straight in typing mode and
  has the key bar.
- **On the computer**, it is a normal herdr pane in a workspace named
  "phone shell", so the user sees it there too.
- **Closing:** "Close shell" asks for confirmation. Leaving the window only
  detaches.
- **When the shell is off:** the `+` menu shows nothing for it. Settings
  explains why: "Shell is available over Tailscale, or turn it on for this
  network on your computer's setup page."

## 6. Phases (each bullet is one PR)

Every PR follows [AGENTS.md](../../../AGENTS.md):

- tests and checks: `make test`, `make vet lint`, and the iOS simulator
  tests for app PRs;
- light and dark screenshots for app PRs;
- docs updated in the same PR.

The daemon PRs never touch the user's herdr in tests. They use a fake bridge
binary, built in tests from a Go helper that replays recorded JSONL, and a
scratch `herdr --session` only in an opt-in e2e test.

### Phase 1: daemon bridge and `/term` (no app changes)

1. **`feat(herdr): terminal session bridge`.**
   - New `internal/herdr/bridge.go` and `bridge_test.go`.
   - Runs `herdr terminal session control|observe` with
     `HERDR_SOCKET_PATH`.
   - Decodes `terminal.frame` and `terminal.closed`, and encodes input,
     resize, scroll, mouse and release.
   - Retries "read in progress" with backoff, always drains stdout, and
     reaps the process.
   - Recorded fixtures go in `testdata/herdr/terminal-session/`.
   - New config key `herdr.bin`, defaulting to `herdr` on `PATH`, then
     `~/.local/bin/herdr`. Document it in [CONFIG.md](../../../CONFIG.md).
   - Version gate from `Ping`: 0.9.2 or newer.
2. **`feat(web): /term socket with channels`.**
   - New `internal/termproto/` (types and a contract test) and
     `internal/web/term.go` with `term_test.go`.
   - Route in `internal/web/server.go`; `term` feature in
     `internal/web/handshake.go`.
   - Channels, limits, per-channel queue and resync, ping, the tracked-agent
     check, and the event-log types above.
   - Docs: a new "Terminal socket" section in
     [ws-contract.md](../ws-contract.md). Put its JSON in tables or
     ```` ```jsonc ```` blocks until `termproto` has a contract test of its
     own: `json` blocks are parsed as `/ws` messages.
3. **`feat(web): ANSI screen frames for the fallback`.**
   - `screen-subscribe {ansi:true}` and `?ansi=1` on the screen route.
   - Files: `internal/supervisor/window.go` (`strip_ansi:false`, visible
     source), `internal/web/screen.go`, `internal/web/window.go`.
   - Feature `screen-ansi`.
4. **`feat(web): raise the prompt limit, add the window transcript line`.**
   - `internal/web/window.go`: 64 KB prompts. The one-line orchestrator
     note when a channel closes goes in `internal/web/term.go`.
   - Docs: [ws-contract.md](../ws-contract.md).
   - **Check:** confirm with the eval harness that the orchestrator
     transcript line does not change behaviour
     ([eval README](../eval/README.md)). It is context, not a prompt change,
     but it is new text the orchestrator reads.
5. **`test: width at phone size does not break state detection`.** An e2e
   test on a scratch herdr session:
   - Resize a Claude Code-like fixture pane to 44 and 58 columns.
   - Assert that herdr's agent state detection and the orchestrator's
     `read_screen` (recent-unwrapped) still report correctly.
   - If either one breaks, set a minimum width and have the app zoom out
     below it.

### Phase 2: renderer and live window (app)

1. **`feat(ios): SwiftTerm surface behind TerminalSurface` (spike gate).**
   - `ios/project.yml`: an exact `packages:` pin.
   - `ios/Sources/SheepdogKit/TerminalSurface.swift` (the protocol).
   - `ios/Sources/SheepdogKit/SwiftTermSurface.swift` (in `SheepdogKit`, not
     the app target, so SwiftTerm is linked into one binary and the unit
     tests reach the headless grid).
   - `ios/Tests/SheepdogKitTests/TerminalWidthTests.swift` covers R1 with
     recorded herdr frames. R2 is run on a device, with the results noted in
     the PR.
   - **If R1 or R2 fails,** stop and switch this PR to libghostty-spm.
2. **`feat(ios): /term client`.**
   - `ios/Sources/SheepdogKit/TermSocket.swift`: framing, channels, `seq`
     gap → resync, reconnect.
   - `TermMessages.swift`.
   - Tests against `StubDaemon` with recorded frames, so no daemon is
     needed.
3. **`feat(ios): live agent window`.**
   - `AgentScreenView.swift` is rewritten around the surface.
   - Covers pinch zoom → resize, drag → `term-scroll`, tap → `term-mouse`,
     the "Latest" button, the observe banner and takeover.
   - The fallback renders `screen`/`screen-ansi` frames.
   - Files: `ios/Sources/SheepdogKit/AgentScreen.swift`, `DaemonClient.swift`.
   - Covers the empty and error states, VoiceOver lines, and the
     `-sheepdogDemoSilent` demo world (`DemoDaemon.swift`, `DemoWorld.swift`)
     with a recorded session.
4. **`docs: iPhone app window`.**
   - User docs: [iphone-app.md](../../iphone-app.md).
   - Developer docs: `ios/docs/dev/architecture.md`.

### Phase 3: composer, key bar, actions

1. **`feat(ios): composer and key bar`.**
   - New `ios/Sources/Sheepdog/TerminalComposer.swift` and `KeyBar.swift`.
   - Typing mode, the ⌃ latch and the more-keys sheet.
   - Key encoding goes in `ios/Sources/SheepdogKit/KeyEncoding.swift`, with
     tests.
2. **`feat(web): agent actions API`.**
   - Routes: `POST /api/agents` (new agent or worktree, within
     `supervisor.project_roots`, reusing the spawn rail in
     `internal/supervisor`), `POST /api/agents/{pane}/rename`, `/interrupt`
     and `/close`.
   - Files: `internal/web/agents_actions.go` with tests,
     `internal/herdr/client.go` (worktree helper).
   - Each action is logged as `web.agent_action {action}`, and the
     orchestrator gets a transcript line.
   - Docs: [ws-contract.md](../ws-contract.md), [roles.md](../roles.md) if a
     rail is involved.
3. **`feat(ios): actions menu and New agent sheet`.**
   - `AgentScreenView.swift`, a new `NewAgentSheet.swift`,
     `FlockView.swift` (`+`).
   - Confirmation for stop and close.

### Phase 4: attachments and changes

1. **`feat(web): attachments`.**
   - `POST /api/agents/{pane}/attachments` (multipart, 20 MB, images, text
     and PDF).
   - Files are stored under `<data_dir>/attachments/<pane>/`, and the
     computer path is returned. Files older than 7 days are pruned.
   - Files: `internal/web/attachments.go`.
   - Docs: [privacy.md](../../privacy.md) (files stay on the computer).
2. **`feat(ios): attach from photos, camera, files, paste`.**
   - Composer chips; `PHPicker`, `UIDocumentPicker`.
   - The camera usage string goes in `ios/project.yml`.
3. **`feat(web): changes route`.**
   - `GET /api/agents/{pane}/changes` (stat) and
     `/changes/{path}` (unified diff).
   - Runs `git` in the pane's working directory, read-only, with paths
     confined to that repository.
   - Files: `internal/web/changes.go`.
4. **`feat(ios): changes view`.** New `ChangesView.swift`, with the diff
   parser in `SheepdogKit` and its tests.

### Phase 5: plain shell

1. **`feat(web): phone shell panes and the shell gate`.**
   - `term-open {target:{shell}}` creates a herdr pane in the "phone shell"
     workspace (cwd within `supervisor.project_roots`). The daemon
     remembers these panes as phone shells, not agents.
   - Applies the `RemoteAddr` gate (`pair.Kind` knows only 100.64.0.0/10
    today; this PR adds Tailscale's IPv6 range fd7a:115c:a1e0::/48), and
    adds `web.shell_lan` to
     [CONFIG.md](../../../CONFIG.md) and to the setup page
     (`web/setup/index.html`, `setup.js` with tests).
   - Files: `internal/web/term.go`, `internal/web/setup.go`, `internal/pair`.
2. **`feat(ios): shell window`.**
   - Entry points, typing mode by default, the close confirmation, and the
     Settings explanation when the shell is off.
   - Docs: [iphone-app.md](../../iphone-app.md) and
     [privacy.md](../../privacy.md).

## 7. Risks and asks

- **No blocker.** herdr 0.9.5 (installed) has everything needed. 0.9.2 is
  the minimum: `terminal.mouse` arrived there, and the bridge itself in
  0.7.2.
- **R1 (cell widths) and R2 (throughput)** decide between SwiftTerm and
  libghostty in phase 2's first PR.
- **Phone width and detection.** A 44–58 column pane changes how Claude Code
  and Codex lay out their screens, which herdr's state detection and the
  orchestrator read. Phase 1 step 5 checks this before the app ships the
  live window.
- **Ask herdr upstream:** a PTY-size field in `pane.get` (for an exact
  restore when no UI client is attached), and forwarding `MouseCapture` and
  OSC 52 on the JSON bridge.
- **The computer user during a phone session.** The pane stays locked at the
  phone size until the phone detaches. The app detaches when it goes to the
  background and when the window closes, so the computer gets its layout
  back without anyone doing anything.
