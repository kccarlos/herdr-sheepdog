# Architecture

There are two targets and one rule: anything that can be tested without a
microphone lives in `SheepdogKit`. The app target is SwiftUI views and
`@MainActor` view models built on top of it.

```
┌──────────────────────────── Sheepdog (app target) ────────────────────────────┐
│  MainView ── PaneSwitcher ── AgentStrip ── ConversationView / FlockView /      │
│              PinnedApprovalCard ── VoiceCapsule ── CapsuleComposer             │
│  SettingsView ── ModelsView                  PushCoordinator (APNs)            │
│        │                                                                      │
│  SessionViewModel (@MainActor): adapts SessionCore, runs its effects          │
│        │                     │                    │                           │
│  AudioEngine            VoiceSocket          DaemonClient                     │
│  (AVAudioEngine,        (/ws)                (/api/*, /events)                │
│   AVAudioSession)                                                             │
└───────┼─────────────────────┼─────────────────────┼───────────────────────────┘
        │                     │                     │
┌───────┼─────────────────────┼─────────────────────┼─── SheepdogKit ───────────┐
│  PCMConverter          WSMessage             SSEParser                        │
│  FrameChunker          (wsproto mirror)      DaemonModels / Flock             │
│  VoiceActivityDetector / CaptureGate         ConversationHistory / Timeline   │
│  MicSignalWatchdog     SessionCore           ConversationCache (JSON file)    │
│  PlaybackQueue /       SessionState          ApprovalFlow / QuestionFocus     │
│  PlaybackRatePolicy    VoiceBarModel         BrainActivity / DaemonHealth     │
│  Metrics               ConnectionChip        Push / MarkdownRenderer          │
│                        SessionPolicies       VoiceProviders                   │
│                                              SettingsStore / SecretStore      │
└───────────────────────────────────────────────────────────────────────────────┘
                                   │
                        sheepdog daemon on your Mac
```

## The daemon contract, mirrored

`WSMessage` mirrors the daemon's `internal/voice/wsproto` package directly:
the same message types, the same required fields and the same `omitempty`
shape on the wire. Each message has a test that decodes the literal JSON the
Go side writes, so the two cannot drift apart silently.

The app sends what `web/voice/app.js` sends, in the same order:

- **Handshake.** The first frame on every open is
  `{"type":"client-ready","protocol":1,"client":{"name":"sheepdog-ios","version":"0.1.1 (20)","platform":"ios"}}`
  (version from `CFBundleShortVersionString` and `CFBundleVersion`); see
  "The protocol handshake" below.
- **Mute.** To mute, the app stops the microphone *and then* sends
  `{"type":"mute","on":true}`. To unmute, it sends `{"type":"mute","on":false}`
  *first* and then resumes. While muted, no binary frames are sent at all
  (never zero-filled ones).
- **Silence.** The gate announces each transition with
  `{"type":"silence","on":…}` before the frames it affects.
- **Speaking.** The gate is the app's own speech detector. When it opens on
  speech (or a hold), the app sends `{"type":"speaking","on":true}`, and
  `on:false` when it closes. This is sent once per change and never while
  muted. Once the daemon has seen a `speaking` message, it treats that
  signal as authoritative and holds spoken pushes while it is on.
- **Playback reports.** `PlaybackQueue` reports `started` (a run began
  playing after the queue was empty), `drained` (the queue ran dry and
  nothing arrived within 300 ms) and `flushed` (on every flush), the same
  way `web/voice/audio-util.js` does. `played_ms` is stream time: 1 s of
  audio stretched to 0.85× reports 1000.
- **Drains.** With a daemon whose `bot-ready` lists `feedback`, every
  time the player runs dry `PlaybackQueue` also records a `drain`
  (`{"type":"drain","at":…,"buffer_ms":…,"played_ms":…,"received_ms":…,"reason":"dry"|"early"}`,
  daemon docs/ws-contract.md "Feedback and drains") and the app sends it.
  `early` means the player node said it had finished well before the
  predicted playhead (an engine restart discards scheduled buffers), and
  `buffer_ms` is the audio that never played. Diagnostic only.
- **Feedback marks.** The flag at the top right of the conversation
  (`FeedbackButton`, shown only with the `feedback` feature) remembers
  the moment it was tapped and opens a short sheet: Paused first and
  biggest, then Wrong answer, Too short, Too long, Misheard me, Other,
  and an optional note. A tag sends `POST /api/feedback` (`FeedbackMark`,
  `DaemonClient.sendFeedback`), then a success haptic and "Noted". The
  voice session is untouched. VoiceOver reads it as "Mark this moment",
  with a "Mark a pause" action that files Paused at once. The daemon
  keeps a 30 s snapshot with each mark; `sheepdog feedback` diagnoses it.
- **Ping.** Every 5 s the app sends a JSON `ping` carrying `gated_frames`,
  `peak`, the playback counters (`underruns`, `drains`, `buffer_ms`,
  `rate`, `stretched_ms`) and `audio_state`. If a ping has no `pong` after
  10 s, the socket goes into the reconnect path.
- **On every (re)connection** the app re-sends the mute, gate and speaking
  state, because the daemon treats a reconnecting client as unmuted and
  ungated.
- **`flush`** empties the playback queue and closes the open reply bubble.
  Stop does the same locally and also sends `cancel`.

### The protocol handshake

The app and the daemon ship separately, so `/ws` carries an integer
protocol version (daemon docs/ws-contract.md "Protocol version and
handshake"; this app speaks 1 and works with daemons from 1 up).
`ProtocolHandshake` (SheepdogKit, pure, `ProtocolHandshakeTests`) is the
state machine; `VoiceSocket` feeds it every control message and a 2 s
timer:

```
idle ──open, client-ready sent──▶ waiting
waiting ──bot-ready (protocol ≥ 1)──────────────▶ ready(protocol, daemon version, features)
waiting ──error "unknown type client-ready"─────▶ legacy   (an older daemon; error not shown)
waiting ──2 s, no bot-ready─────────────────────▶ legacy   (protocol 1)
any ──status closed "protocol_too_old"──────────▶ appTooOld(the daemon's error text)  → close
any ──bot-ready below our minimum───────────────▶ daemonTooOld                         → close
```

- Nothing waits for `bot-ready`: `mute`, `silence`, `speaking` and audio
  follow `client-ready` at once, so an older daemon costs nothing.
- `bot-ready` and the old daemon's error are swallowed; the session never
  sees them.
- A mismatch closes the socket like `replaced`, without reconnecting
  (`VoiceSocket` reports `.closed(reason: "protocol", retrying: false)`).
  The core raises `SessionBanner.protocolMismatch` with the words to show
  ("Update the app" / "Update the daemon"), and the connection reads
  **Update needed** (red) until the next Connect.
- Health and the chip's sheet show a **Protocol** row: the handshake's
  result, or `/api/hello`'s `protocol`/`min_protocol` before a socket
  opened (no fields there: a daemon from before the handshake, protocol 1).
- `LiveHandshakeTests` runs the handshake against a real daemon when
  `TEST_RUNNER_SHEEPDOG_LIVE_WS=127.0.0.1:<port>` is set (new daemon:
  `ready`; old daemon: `legacy` with no error shown), and skips otherwise.

Auth is `Authorization: Bearer <token>` on every request, **including the
WebSocket upgrade**, so the token never appears in a URL. Before opening
`/ws`, the app probes `GET /api/state`, where a 401 is a plain status code.
After a failed reconnect, the same probe tells whether the token was
revoked mid-session.

## Events

`GET /events` is the app's second input. It carries agent state changes
(which trigger a debounced refetch of `/api/state`), pushes,
`voice.tool_call` / `voice.tool_result` (the orb's *thinking* state, with a
30 s timeout), orchestrator replies, held pushes and `pending.*`.

Every event that is a conversation item in its own right (a typed
utterance, a reply without a voice model, a push, an approval and its
resolution, `daemon.start`, voice reconnects) is folded into
`ConversationHistory` by the daemon's own rules and keyed by its event id.
`/api/conversation` later returns the same item under the same id, so the
two copies merge instead of showing twice. Transcript fragments are the
exception: `/ws` delivers them sooner, so they stay id-less bubbles in
`ConversationReducer` until a sync 2.5 s after the turn replaces them.

**Resume.** The client tracks the last SSE `id:` and reconnects with
`Last-Event-ID` and `?since=`. The id is kept in the cache, so a relaunch
resumes too. Folding is idempotent: a replayed `pending.created` cannot
reopen an approval that was answered in the meantime. Haptics fire only for
events less than a minute old.

There are two `/events` pitfalls that any client of this daemon will hit:

1. **Foundation's `AsyncLineSequence` drops empty lines**, and an empty line
   is what ends an SSE record. The client reads raw bytes and splits on
   `\n` itself.
2. **The stream needs its own `URLSession`.** `timeoutIntervalForRequest`
   is an inactivity timeout, and the 8 s value used for the JSON endpoints
   would kill the stream between the daemon's 15 s keepalives. The stream
   session allows 60 s.

## History and the cache

The app fetches `GET /api/conversation` (the newest page, 200 items) at
launch, before and without Connect. It fetches it again on every (re)open
of the event stream, on return to the foreground, and after each voice
turn. Under `ConversationHistory.applyNewest`, the server is the record for
the id range its page covers. Live items newer than the page survive, and
older pages come from `before=<next_before>`.

`ConversationCache` writes the newest 300 items, `next_before` and the last
event id to `Application Support/Conversations/<host>_<port>.json`, one file
per daemon, excluded from backups. The token is in neither the file nor its
name. `ConversationTimeline.rows` builds the screen: history and live
bubbles merged by time, date dividers, `notice` items as dividers, and
duplicate copies of a pinned question folded to one-line pointers
(`QuestionFocus`).

## Approvals

`ApprovalFlow` is the state machine behind the pinned card. It goes from
*open* to *sending* (one option, further taps ignored) to either
*answered* (the daemon's `spoken`), *gone* (409) or back to *open* with the
error shown. A `pending.confirmed` from somewhere else settles the card as
answered by voice. `expired`, `cancelled`, `replaced`, or a state refresh
without the question dismisses it. `/api/state`'s `pending_action` is the
source of truth (refreshed every 5 s and on every `pending.*` event), and
the event is the fast path. The POST to `/api/pending/answer` sends the
option's 1-based `index`. On the Agents pane the card sits on its agent's
card instead of above the voice capsule.

## New conversation

⋯ ▸ New conversation calls `POST /api/conversation/reset {"via":"app"}`.
This resets only the orchestrator's and the voice model's short-term context
and keeps the scrollback. *Settings ▸ Clear conversation on this phone*
drops the local copy instead. A 404 hides the menu item, and the app learns
this separately for each daemon. The app adds no optimistic divider: the
daemon logs the reset before it answers, so the divider arrives as an
ordinary notice item, deduplicated by event id and `reset_id`.

## Visual tokens

The redesign ([docs/dev/design/app-redesign.md](../../../docs/dev/design/app-redesign.md))
draws from one palette, `Theme` (`Sources/Sheepdog/Theme.swift`), backed by
colorsets with a light and a dark value each, taken from
`assets/brand/colors.json`:

- **Surfaces:** `Surface` (the screen), `Card`, `Fill` (code, wells),
  `Line` (hairlines), `Muted` (secondary words), `Track` and `Selected`
  (the segmented control), `Ink` (primary words, cream in dark).
- **Accent and states:** `AccentColor`, `AccentSoft` (a waiting card's
  question), `OnAccent` (text on orange is always ink), the agent-state
  colours and their soft tints (`WorkingSoft`, `ThinkingSoft`,
  `DoneSoft`), `Danger` for "No" options and the muted mic.
- **Shape and motion:** radii 8, 14 (cards) and 22; one easing,
  `Theme.ease` (cubic-bezier(.2,.8,.2,1), 200 ms), which turns into a
  short fade under Reduce Motion.
- **Mascot:** `MascotSleeping` (first run, offline, no agents),
  `MascotListening` (empty conversation, microphone primer),
  `MascotAlert` (notification primer), `MascotCelebrating` (paired). The
  same transparent art serves both appearances.
- **Splash:** the launch screen shows `LaunchLogo` (168 pt) on
  `LaunchBackground`; `SplashView` keeps the logo in place, adds the
  wordmark and fades out after 0.6 s.

The app needs iOS 26: it uses Liquid Glass (`.glassEffect`) and the zoom
navigation transition directly, and swaps glass for a solid surface under
Reduce Transparency.

## Main screen panes

`PaneSwitcher` switches the middle of the screen between Conversation,
Agents. It is built from buttons, so Agents can carry an amber
badge with the number of blocked agents, and it turns into a menu at the
accessibility text sizes. The voice capsule stays in place.

- **Agents** (`Flock`, `FlockView`, `AgentCard`). Only running agents are
  listed (idle, working, blocked, done): blocked first, then working, then
  idle and done. Unknown and exited agents fold into a closed "Not running
  (N)" group; with none running, the sleeping mascot and New agent. A card
  is a dot, the name, the kind, the state with its time, and the second
  line (`Agent.cardLine`: current task, else last task, else `title`),
  plus the round window button. A tap opens the details (kept per session
  in `expandedAgents`): working on, `last_result` with its detail, the
  newest journal entry with its files and tools, the last answered
  question with the chosen answer (`Agent.answeredQuestion`), the space.
  **Every open question** (`/api/state` `questions`) puts its answer
  buttons on its agent's card (`questionCard(for:)`): the voice's own goes
  through `ApprovalFlow`, any other straight to `POST /api/pending/answer`
  with its id. Options are the agent's labels verbatim, in an `OptionFlow`
  (a long label takes a row); only the screen's first question has the
  filled accent, the others are outlined in amber. The agent strip above
  the conversation shows the same running agents.
- **Agent window** (`AgentScreenView`). "Open window" on an Agents card
  (a `NavigationLink` to `AgentWindowRoute` on the main `NavigationStack`),
  or in the sheet behind a strip chip, pushes the agent's live terminal;
  see "The live agent window" below.
- **Orchestrator replies and held pushes** (`BrainActivity`). `voice.tool_result`
  for `to_supervisor` shows as an "Orchestrator → Sheepdog" card. With
  `--voice=off`, only a detail/options footer appears under the voice's
  bubble. `voice.push_held` shows as a "Waiting for a listener" card, which
  goes away on release and is struck through on drop. After a relaunch,
  these are backfilled from `GET /api/events?type=…`.
- **Typed input.** The capsule's keyboard turns it into the composer
  (see "The voice capsule"); the words and the answer arrive in the
  conversation the usual way.
- **Health** (`HealthView`, `DaemonHealth`), pushed from the voice menu's
  connection sheet and from Settings ▸ Diagnostics. It shows the daemon's
  connection, address, version and uptime (from the newest `daemon.start`),
  plus the voice and orchestrator read-only with "Change in Settings ›", which
  opens Settings ▸ Models. It also shows GPT-Live's billed time, chattiness
  (`GET/PUT /api/settings`), this phone's audio path, and recent problems
  from `GET /api/events?type=…`: errors only, repeats grouped as ×N, and
  the raw event behind a tap.
- **Markdown** (`MarkdownRenderer`). Tasks, questions, the worker's last
  message and orchestrator replies support inline bold, italic, code and links,
  lists, and monospaced fenced code. Headings render as bold body text.
  GitHub tables parse to a `.table` block: one line per row in the
  single-`Text` rendering, a real table in a card (below).
- **Cards** (`Cards.swift`: `ScreenCard`, `CardVisibility`, `CardInbox`;
  `CardViews.swift`), daemon docs/dev/ws-contract.md "Cards". Only with a
  daemon whose `bot-ready` lists `cards` (and in the demo).
  `CardVisibility` sends `visibility {visible, voice_screen}` after every
  ready handshake (each connection starts not visible on the daemon) and
  on every change. `visible` is `scenePhase == .active` (inactive and
  background send false); `voice_screen` is the Conversation pane with
  nothing pushed on `navigationPath`, Settings closed, no demo cover, and
  no sheet that reports itself with `.coversVoiceScreen(_:)` (the
  connection sheet's, the feedback flag's). A card's own sheet counts as
  the voice screen, so the next card replaces it. A `/ws` `card` goes
  through `CardInbox.receive`: an id seen before (live, from
  `/api/conversation` or the cache) does nothing, a new one opens as a
  sheet (medium/large) when the voice screen is in front and is only a
  row otherwise. Live cards are their own `TimelineRow.card` until the
  history's `card` item with the same `card_id` replaces them; an item
  whose card the daemon's store dropped is filled from what the phone
  got, else shown as a plain row. The `card.shown` SSE event is not
  folded (metadata only). In the sheet, tables are a `Grid` when its
  ideal width fits and one small card per row otherwise (`ViewThatFits`,
  always stacked at the accessibility sizes), so nothing truncates; code
  and excerpts scroll sideways. An approval card is read-only: its choices
  are plain chips and "Go to the question" only closes the sheet. "Open
  window" closes the sheet, then pushes `AgentWindowRoute`. The demo's
  "Show me the agents" (`DemoDaemon.onCards`) shows one; the debug launch
  argument `-sheepdogDemoCard` asks it by itself for screenshots
  (`CardsUITests`, `TEST_RUNNER_SHEEPDOG_CARD_SHOTS=<dir>`).

## The terminal surface

The live agent window ([phone-terminal.md](../../../docs/dev/design/phone-terminal.md))
draws herdr's terminal stream through two renderer-agnostic protocols in
`SheepdogKit/TerminalSurface.swift`:

- `TerminalGrid`: a headless emulator (`feed`, `resize`, `cell(col:row:)`,
  plus `lineText`, `snapshotText` and `column(of:row:)`). Tests and replay
  use it.
- `TerminalSurface` (`@MainActor`): the drawn view (`feed`, `cols`/`rows`,
  `onSizeChange`, `onInput`, `snapshotText()`, `setFontSize(_:)`,
  `accessibilityLines`). The surface sizes its grid from its bounds and
  font and reports the size; the caller sends it to the daemon.

`SheepdogKit/SwiftTermSurface.swift` is the only file that imports
[SwiftTerm](https://github.com/migueldeicaza/SwiftTerm) (MIT).
`TerminalRenderer.makeGrid(cols:rows:)` and `makeSurface(fontSize:)` hide
which renderer is behind them, so a move to libghostty-spm replaces that
one file. It lives in `SheepdogKit` rather than the app target so the unit
tests reach the headless grid, and so SwiftTerm, a static library, is
linked into one binary only.

**The pin.** SwiftTerm is the project's first Swift package, pinned with
`exactVersion` in `project.yml` (`packages:`). The generated project is not
committed, so `ios/Package.resolved` (all transitive versions) is, and
XcodeGen's `postGenCommand` (`scripts/install-package-resolved.sh`) copies
it into `Sheepdog.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/` on
every `xcodegen generate`. Xcode Cloud needs that file: it does not resolve
packages itself. SwiftTerm also runs a build-tool plugin, which Xcode asks
a person to trust once; `ci_post_clone.sh` trusts package plugins on Xcode
Cloud, `ios.yml` and local command-line builds pass
`-skipPackagePluginValidation`, and in Xcode you click Trust & Enable.

To update the pin: check the newest tag with `git ls-remote --tags
https://github.com/migueldeicaza/SwiftTerm`, change `exactVersion` and
`TerminalRenderer.name`, run `xcodegen generate` and then
`scripts/install-package-resolved.sh --update`, run the tests (above all
`TerminalWidthTests`), and commit `project.yml` with `Package.resolved`.

**Width agreement (R1).** herdr composes the screen with ghostty-vt and
sends changed cells with an absolute cursor move in front of most cells
after a non-ASCII one. `TerminalWidthTests` replays recorded frames
(`Tests/SheepdogKitTests/Terminal/*.jsonl`, with herdr's `pane read
--source visible` text beside each; `TerminalFixtures.swift` says how they
were recorded) and checks every row's text, every grapheme herdr placed and
the column of the text after CJK, VS16 (`☁️`), ZWJ (`👩‍💻`), emoji, flags,
box drawing, combining marks, ambiguous-width and half-width characters.
SwiftTerm 1.99.0 agrees on all of them. `TerminalThroughputTests` prints the
simulator's feed and draw times per frame (R2), which are no substitute for
a device.

### The terminal socket

`TermSocket` (`SheepdogKit`) is the client side of the daemon's `/term`
WebSocket ([phone-terminal.md](../../../docs/dev/design/phone-terminal.md)
section 4). `TermMessages.swift` holds its JSON messages (`TermMessage`,
which mirrors the daemon's `internal/termproto`), `TermCloseReason`, and the
binary frames (`TermFrame` for output, `TermInput` for input, both
big-endian). Use it only when `bot-ready` lists the `term` feature.

- Same address and token as `/ws` (`Authorization: Bearer` on the upgrade),
  path `/term`. `term-hello` goes first, and the connection is ready on
  `term-ready`. Channels opened before that wait for it.
- `open(target:mode:cols:rows:takeover:)` returns a channel id, or nil past
  `term-ready.limits.channels`. Frames come out of `onEvent` as
  `.frame(TermFrame)`, in order.
- **Resync.** On a `seq` gap, or a diff before any full frame, the channel
  drops diffs and sends `term-resize` at its current size (herdr answers
  with a full frame). It asks again if no full frame arrives within a
  second.
- `resize(ch:cols:rows:)` is debounced by 150 ms. The same size is sent
  too, because it asks for a repaint.
- Input goes as binary frames of at most 64 KB; a long paste is split.
- A dropped connection reconnects with backoff (1 s, doubling to 10 s) and
  reopens every channel the daemon had not closed, at its latest size and
  without takeover. A WebSocket ping goes every 20 s, and a ping still
  unanswered at the next tick drops the connection.
- `TermSocketTests` run it on a fake `TermTransport`. One test uses the real
  URLSession transport against a local server, and one replays a recorded
  herdr fixture through the socket into a grid.

### The live agent window

The controls (phase 3) are their own files: `KeyBar.swift` (the bar,
`KeyCap` and `MoreKeysSheet`), `TerminalComposer.swift` (the field, Send ↔
Stop, a `leading` slot for attachments, and the controller's key, typing
and Stop methods) and `AgentActionsMenu.swift` (⋯ ▸ Rename, Stop, Close and
their dialogs). `KeyEncoding` (Kit) holds every key's bytes, the one-shot
`KeyModifiers` and `SnippetStore`; on the snapshot path only keys with an
`allowListed` name work. Typing mode is SwiftTerm's own `UITextInput`
(marked text for IMEs, dictation), switched by `SwiftTermSurface.setTyping`.
`AgentAction` (Kit) is the client of the daemon's agent actions;
`NewAgentSheet` is presented from the Agents list.

```
AgentScreenView ── AgentWindowView (one per pane; ⋯ ▸ Switch agent swaps it)
      │                 header (name, state chip, ⋯) · NoticeBar · TerminalHost
      │                 · PinnedApprovalCard · key bar · composer
AgentWindowController (@MainActor) ── AgentWindowState (Kit, pure)
      │            │
SwiftTermSurface   TermHub (one /term connection, held by SessionViewModel)
                   └── TermSocket ── URLSession, or DemoTermTransport in the demo
```

- **`AgentWindowState`** (SheepdogKit, `AgentWindowTests`) decides
  everything; the controller feeds it events and runs the commands it
  returns (`open`, `close`, `startSnapshot`, `stopSnapshot`, `scroll`).
- **Which path.** `TermAvailability` comes from `bot-ready`'s `term`
  feature. Without a voice session the window uses the features this daemon
  listed last time, else tries `/term` anyway (`unknown`). `no` (a daemon
  without `term`, or one older than the handshake) draws the `screen`
  snapshot. The window does not know which of the two (old daemon or herdr
  older than 0.9.2) it is, so its notice names both.
- **Size.** The channel opens on the surface's first reported grid (after
  layout), so the pane is resized once; SF Mono 11 pt is 58 columns on a
  402 pt wide phone. Pinch changes the font when the fingers lift (the view
  scales as a preview) and the keyboard shrinks the view; both give a new
  grid and one debounced `term-resize`. The font size is a per-device
  default (`agentWindow.fontSize`).
- **Gestures** (`SwiftTermSurface`). SwiftTerm's taps, scroll view and
  mouse reporting are off; herdr owns scrollback and mouse modes. A
  one-finger vertical drag becomes `term-scroll` in whole lines (finger
  down = older = `up`); a tap becomes `term-mouse` down and up on the cell;
  long-press stays SwiftTerm's selection with Copy; Paste shows only on a
  live `control` channel (`GatedTerminalView` withholds the action's
  target otherwise). `inputView` is an empty
  view, so selecting never raises a keyboard (phase 3's typing mode sets
  `keyboardEnabled`).
- **"Latest".** herdr never reports its scroll position, so the window
  counts its own scrolls; Latest and any raw input scroll back down by that
  count.
- **`term-free`.** When the controller this daemon ran lets go, an
  observing window closes its `observe` channel and opens `control`
  without a takeover; a refusal (`busy`) puts it back to observing.
- **`term-closed`.** `refused:busy` and `taken_over` reopen as `observe` with
  "Another device has control · Take over" (confirmed, then `takeover:
  true`); `gone` and `refused:not_tracked` end the window; `herdr` and
  `refused:too_many` fall back to the snapshot with Try again. A dropped
  connection keeps the last screen ("Reconnecting…"); a window with nothing
  drawn yet shows the snapshot until the socket comes back.
- **Snapshot in the same surface.** `SnapshotScreen.bytes` turns the
  `screen` text into terminal bytes: cursor hidden, autowrap off (wide
  lines are clipped, not wrapped) and the last rows that fit. When the
  daemon lists `screen-ansi` the subscription asks for `ansi: true`, and the
  poll always sends `?ansi=1` (older daemons ignore it); a frame with `ansi:
  true` keeps its SGR colours, each line ending in a reset.
- **Background.** Going to the background closes every channel and the
  connection (`TermHub.disconnectIfIdle`), so the computer gets its layout
  back; the foreground reopens it (as an observer if it was one). The hub
  keeps the connection for 20 s after the last window closes, so switching
  agents reuses it; past `term-ready.limits.channels` a window says "Too
  many live windows".
- **Input.** The key bar sends raw bytes on a live `control` channel (esc
  `1b`, tab `09`, ↑ `1b 5b 41`, ↓ `1b 5b 42`, ⌃C `03`, ⏎ `0d`), else posts
  allow-listed keys as before. The composer always posts
  `POST /api/agents/{pane}/input`.
- **Theme.** `TerminalTheme.light` / `.dark` use the brand surfaces and a
  16-colour palette that reads at 3:1 or better on its background
  (`AgentWindowTests`); the window reapplies it when the appearance
  changes.
- **VoiceOver.** The surface's outer view (`TerminalContainerView`) is one
  element whose value is the visible text and whose rotor moves by line
  (`UIAccessibilityReadingContent`); SwiftTerm's own element is hidden.
- **Demo.** `DemoTermTransport` answers `/term` from `DemoWorld`'s screens:
  full frames at the window's size, a spinner diff every second, typed
  input echoed in the prompt box. `-sheepdogDemoTerm busy|gone|herdr`
  forces the other states; `AgentWindowDemoUITests` covers them without a
  daemon.

## Settings ▸ Models

`ModelsView` takes effect at once, like every Settings screen (there is
no Save or Cancel):

- **Voice.** `GET /api/voice/providers` lists the providers, and
  `PUT /api/voice/provider` switches between them. Unavailable providers
  are disabled with the daemon's reason. GPT-Live is marked "Billed per
  minute". A 404 hides the section, and the provider fields on hello, state
  and `/ws` `status` are optional, so older daemons still work.
- **Orchestrator.** `GET /api/brain/models` provides the provider and model
  pickers, and `PUT /api/brain/model` switches. A new provider takes its
  first recommended model. The current model is always offered.
- **Notifications.** This phone's registration and the daemon's
  `GET /api/push/status`.

## Notifications (APNs)

`PushCoordinator` asks for permission after the first successful
connection, when the daemon's `GET /api/push/status` reports it can send
(mode `relay` or `apns`). It sends the device token to the daemon with
`POST /api/push/register`, sends it again when the token or the daemon
address changes, and removes it (`DELETE`) when notifications are turned
off or the app moves to another daemon. Debug builds register as `sandbox`
and Release builds as `production`. This comes from the build setting
`SHEEPDOG_PUSH_ENVIRONMENT` (Info.plist `SheepdogPushEnvironment`), which
matches the `aps-environment` entitlement in `project.yml`. The
time-sensitive entitlement lets needs-you alerts break through Focus.

**End-to-end encrypted relay notifications.** The register answer carries
the phone's `content_key` (base64, 32 bytes). `PushCoordinator` stores it
with `PushContentKeyStore` (`Sources/SheepdogPush`): one keychain item in
the App Group's access group (`SHEEPDOG_APP_GROUP`), accessible after the
first unlock, this device only. Each registration overwrites it (the same
daemon answers the same key, another daemon its own); unregistering,
moving to another daemon, notifications turned off or a daemon without
push remove it. The relay delivers a placeholder alert with
`mutable-content: 1` and `e2e: {c, n}`. The `SheepdogNotificationService`
extension (CryptoKit, Security and UserNotifications only: no SheepdogKit,
no networking) opens it with AES-GCM (`c` is the ciphertext with the
16-byte tag appended, `n` the 12-byte nonce), then copies the plaintext's
alert, thread id, category, `aps` and `sheepdog` dictionaries into the
notification, so the tap and Approve / Decline code reads it exactly like a
direct push (`PushE2E.rewrite`). With no key, a bad ciphertext or a timeout
it shows "Sheepdog / Open the app to see the update" and no raw data. A
push without `e2e` (the direct APNs path) passes through untouched.
`Sources/SheepdogPush` is compiled into both SheepdogKit (for the tests)
and the extension. The test vector in `PushE2ETests` comes from
`scripts/push-e2e-vector.go`, which seals like the daemon's
`internal/push.Seal`.

The `APPROVAL` category has **Approve** (the first "Yes…" option, otherwise
option 1) and **Decline** (the first "No…" option). Both require the
passcode, because the daemon token is a when-unlocked keychain item. If an
answer fails or is stale, a notification remains that opens the app when
tapped. Tapping any notification opens that agent's Agents card. Nothing is
shown in the foreground while the session is live. The payload format is
described in the daemon's `docs/dev/push.md`.

## Audio

```
microphone ─▶ AVAudioEngine input tap (hardware format, ~4 096 frames)
           ─▶ copy off the render thread ─▶ main actor
           ─▶ PCMConverter  (AVAudioConverter: anti-alias + rate change → 16 kHz mono Int16)
           ─▶ FrameChunker  (640 samples = 40 ms = 1 280 bytes)
           ─▶ CaptureGate   (VoiceActivityDetector; hold-to-talk override)
           ─▶ VoiceSocket   (one binary frame each)

/ws binary ─▶ PlaybackQueue (adaptive jitter buffer, then chunks in order;
                             owns the time-stretch rate policy)
           ─▶ AVAudioPlayerNode.scheduleBuffer (no `at:`; the node concatenates)
           ─▶ AVAudioUnitTimePitch (pitch 0, rate 0.85–1.05, set every 20 ms)
           ─▶ 24 kHz mono Float32 ─▶ main mixer ─▶ speaker
```

The session is `.playAndRecord` with mode `.voiceChat` and the options
`.allowBluetooth`, `.allowBluetoothA2DP` and `.defaultToSpeaker`.
`.voiceChat` turns on the system echo canceller. Interruptions (a call,
Siri) pause the engine and flush playback. If iOS will not give the audio
back, the orb says "Audio paused by iOS — tap to resume". Route changes
rebuild the capture chain, because the hardware sample rate changes
underneath the tap. The tap buffer is copied on the render thread and
converted on the main actor, which owns the converter, the chunker and the
gate.

| | Rate | Format |
|---|---|---|
| Microphone hardware | 48 000 Hz (44 100 on some devices) | Float32 mono |
| Up to the daemon | 16 000 Hz | PCM16 LE mono, 40 ms frames (1 280 B) |
| Down from the daemon | 24 000 Hz | PCM16 LE mono, arbitrary chunks |
| Player node | 24 000 Hz | Float32 mono (the engine resamples to the device) |

`AudioLatencyTests` measured capturing, converting, chunking and gating one
85.3 ms tap buffer at about 0.07 ms on average in the simulator, so the
conversion is not a bottleneck. The live numbers are in Settings ▸ About and
in `os_log` (subsystem `com.github.kccarlos.herdr-sheepdog-ios`, categories
`audio`, `socket` and `session`).

### The adaptive jitter buffer

`PlaybackQueue` tracks its own **playhead**, computed from chunk durations
rather than from `AVAudioPlayerNode`'s completion callbacks, which arrive
late and jittery.

- A drain counts as an **underrun** (a gap you can hear) only if more audio
  from the same utterance arrives within 300 ms. Each underrun raises the
  target by 100 ms, up to the provider's ceiling.
- After an underrun, the queue **refills before resuming**. The first chunk
  of the refill gets an 8 ms fade-in.
- An utterance with no underrun decays the target by 20 ms, down to the
  provider's floor. A `flush` does not move the target.
- The "start anyway" wait for a short reply is the target plus 250 ms.

### Adaptive time-stretch

The player **plays slower when the buffer runs low**, preserving pitch,
through an `AVAudioUnitTimePitch`. The policy (`PlaybackRatePolicy`) is pure
and matches the browser page's:

- **Target T per provider** (`PlaybackProfile`, chosen from `/ws`
  `status.provider`): Gemini starts at 150 ms with a 500 ms cap. GPT-Live
  (`openai`) starts at 400 ms with a 2 000 ms cap.
- **Rate r from buffered audio b**: 1.0 when b ≥ T. Below T it falls
  linearly to 0.90 at T/2 and reaches the 0.85 floor at T/4. Above 2T it
  is 1.05. The rate changes by at most 0.02 per 20 ms.
- **Catching up needs latency debt.** The player speeds up to 1.05 only to
  win back latency it added by playing slower than 1.0. Gemini arrives far
  ahead of real time and would otherwise play everything 5 % fast. The
  debt resets when a reply ends.
- The playhead runs at 1/r, and `bufferedMilliseconds(at:)` (the policy's b
  and the ping's `buffer_ms`) measures audio, not wall-clock time.
  `AudioEngine` runs the 20 ms control loop, and a flush or Stop resets the
  rate to 1.0.

`TimeStretchReplayTests` replays recorded and synthetic GPT-Live and Gemini
arrival timelines through the queue. `TimePitchGraphTests` renders the real
player → time-pitch → mixer graph offline.

### Voice detection (what gets sent)

`VoiceActivityDetector` runs on each 40 ms frame:

1. **Level in the speech band.** The frame passes through a 2nd-order
   high-pass filter at 300 Hz and a low-pass filter at 3.4 kHz. It is then
   cut into four 10 ms sub-blocks, and its level is the energy of the
   **second-quietest** sub-block. Sustained speech barely moves this value,
   while a keyboard click disappears.
2. **Adaptive noise floor**: the 15th percentile of the levels over the
   last 4 s, never below −72 dBFS.
3. **Adaptive margin**: the gate opens at floor + `4 + 2 × spread` dB, where
   spread is the 15th minus the 2nd percentile. A steady fan gives about
   6 dB, and a room of other voices about 10.
4. **Attack and hangover**: the gate opens once 120 ms of the last 280 ms
   are above the margin, and closes after 400 ms below the margin minus
   3 dB.
5. **Pre-roll**: when the gate opens, 380 ms of withheld audio is sent
   first, so the first syllable is never clipped.

`CaptureGate` wraps the detector and the hold-to-talk override. It emits a
transition exactly when its visible state changes. All the tunables are in
`VADTuning`.

**Tested with macOS text-to-speech.** `scripts/make-fixtures.sh` renders six
voices with `say`. The tests mix them with white, pink and brown noise,
babble (the other voices) and keyboard clicks at 20, 10 and 5 dB SNR.

| Noise | SNR | Open latency, median / worst | Coverage (worst) | Opens on the noise between utterances |
|---|---|---|---|---|
| white | 20 / 10 / 5 dB | 120 / 120–160 ms | 100 % | 0 |
| pink | 20 / 10 / 5 dB | 120–160 / 160–200 ms | 100 % | 0 |
| brown | 20 / 10 / 5 dB | 120–160 / 120–200 ms | 100 % | 0 |
| babble | 20 / 10 dB | 120 / 160 ms | 100 % | 0 |
| babble | 5 dB | 160 / **1 120 ms** | **29 %** | 0 |
| clicks | 20 / 10 / 5 dB | 160 / 200 ms | 100 % | 0 |

Noise alone (60 s of each kind at −31, −45 and −60 dBFS) opens the gate
**0 times**. The weak cell is 5 dB babble, one voice barely above a room of
others. It is asserted separately, and Voice Isolation is the answer for
that room.

**Voice Isolation.** Settings ▸ Microphone shows
`AVCaptureDevice.activeMicrophoneMode` and opens the system panel. A
one-time tip suggests it after the first listening session.

**No microphone signal.** `MicSignalWatchdog` watches for 3 s in which the
app is unmuted and capturing but every sample is digital zero (or no
buffers arrive). That means the capture path is broken. The status line
then says "No microphone signal — check the mic", the engine logs the input
format, the voice-processing state and both input mutes, and it rebuilds
the capture chain once. At start, the engine clears the voice-processing
unit's input mute and iOS's app-wide input mute, and downmixes a
multi-channel input. When the microphone is denied or will not start, the
orb says "No microphone" (`SessionState.noMicrophone`).

## The voice capsule

`VoiceCapsule` (`VoiceBar.swift`) floats over the bottom of the main
screen, 12 pt from the sides, in Liquid Glass (a solid card under Reduce
Transparency); the content scrolls under it (`safeAreaInset`). It holds
four fixed 44 pt slots: the mic (mute) button, the orb, the name and status
(the voice menu) and the keyboard. It grows with the text size up to a cap,
and the status wraps to two lines at the accessibility sizes.

- **Its look** is one of 13 `CapsuleMode`s (SheepdogKit, table-tested), from
  the session state, the talk mode, holding, and whether a question is
  open: live (orange orb with the cream ring), quiet (a question has the
  accent), speaking, thinking and stalled (with the stop badge), muted (the
  red mic), paused, no microphone, reconnecting, offline, voice off, hold
  and holding. `-sheepdogCapsuleGallery` shows them all on one screen.
- **What the orb does** is `VoiceBarModel`: Connect when disconnected,
  Stop while the voice is thinking or speaking, Resume after an audio
  interruption, otherwise nothing. Its words too: "Tap the orb to connect",
  "Listening — just talk", "Muted — tap to unmute", "Hold the orb to talk".
- **The voice menu** (tap the name): the connection, Manage computers… and
  End voice session ("Your agents keep working."), or Start voice session.
  With several computers it will add Talk to.
- **The composer.** The keyboard morphs the capsule into `CapsuleComposer`:
  the quiet orb (back to voice), the field and the ink send button.
  Sending goes through `POST /api/utterance`.
- A first-run coach mark explains that the mic is only for muting; the
  first time Mute is used, the status line explains it. **Hold to talk**
  (Settings ▸ Microphone) turns the orb into a push-to-talk button and
  hides Mute.

The **connection** (`ConnectionChip`) reflects only the daemon connection
(the `/ws` socket, the `/events` stream and the token), never the voice
state: "Connected", "Connecting…", "Reconnecting…", "Offline", "Token
refused" or "Not connected". It is the first row of the voice menu, which
opens `ConnectionDetailView`: the address, a timed round trip, the daemon's
version, the voice provider and model, the orchestrator, Health, and
Reconnect / Open Settings.

**The top bar** (`TopBar` in `MainView.swift`) is `Conversation | Agents`
(`PaneSwitcher`, with the waiting badge; a menu at the accessibility sizes)
and one pill with `+` (New agent, New shell) and `⋯` (New conversation,
Stop, End voice session, Settings). There is no title and no connection
chip.

A new question with options plays a distinct haptic pattern and a short
chime through the audio session (`Haptics`).

## The session core

`SessionCore` (Kit) holds every decision the voice session makes, as a
value: connecting and reconnecting, who owns mute, when the audio engine
runs, hold-to-talk, playback, the thinking timeout, the background pause
and the "paused by iOS" state. `SessionViewModel` only adapts it. Every
callback (a tap, the socket, the event stream, the audio engine, the scene
phase, the UI ticker) becomes a `SessionEvent`. `apply(_:now:)` updates the
core and returns `[SessionEffect]`, and the view model runs those effects
in order. `SessionCoreTests` pins the behaviour as event sequences with
their exact effects.

| Group | Fields |
|---|---|
| `connection` | `wantsConnection`, `socket` and `events` (`SocketLife`), `failed`, `tokenRefused`, `reconnect` (`ReconnectPolicy`), `muteSync` (`MuteSync`), `socketMuted`, `socketGateClosed` |
| `voice` | `status`, `closedReason`, `stalled`, `provider`, `saidVoiceIsOff` |
| `mic` | `talkMode`, `muted` (the user's intent), `holding`, `gateClosed`, `noSignal` |
| `playback` | `speaking`, `hold` (`SpeakingHold`), `heldSpeaking` |
| `thinkingExpiry` | a tool call's 30 s timeout |
| `suspension` | `pausedByIOS`, `backgroundSuspended`, `keepListeningWhenLocked` |

These are derived as pure computed properties: `sessionState` (the orb),
`connectionChip`, `voiceBar`, `statusLine`, `daemonMute`, `acceptsPlayback`
and `playbackBusy`.

Behaviour worth knowing:

- **Reconnects** use backoff of up to 10 s. For the first 30 s the orb
  quietly says "Reconnecting…". After that, one banner says the daemon is
  unreachable while the app keeps trying until End. A refused token stops
  the session at once. If another client takes the session (the daemon
  serves one at a time), the app says so once and waits.
- **The app owns mute.** It tells the daemon its mute state on every
  connect and corrects a daemon that disagrees. Hold-to-talk never mutes.
- **Keep listening with the screen locked** (on by default). When it is
  off, going to the background pauses the microphone and playback and
  sends `mute`, so a per-minute GPT-Live session stops billing. Both resume
  when the app comes back.

## The orb

`SessionState` is a pure function of the socket life, voice status,
stalled, muted, gate closed, speaking, a tool call in flight, microphone
availability and audio paused by iOS. It is resolved through one ordered
list of rules, most urgent first. The views render only this value, so the
orb, the status line and the buttons cannot disagree. *Speaking* is held
for 800 ms across playback gaps (`SpeakingHold`), and a flush or Stop drops
it at once. Mute is not an orb state while the voice is speaking or thinking.
Instead the orb wears a small red mic badge. `capturing` is derived from
the same value: the microphone runs only in states where something can
hear it.

## Concurrency

The project uses Swift 5 language mode on purpose. The audio path (tap
callbacks, `AVAudioConverter` input blocks, `AVAudioPlayerNode` completion
handlers) is not `Sendable`-clean. The design is still safe: the Kit types
are value types, the view models and the audio engine are `@MainActor`, and
the one real-time boundary is the explicit buffer copy in the tap.

## Settings

`SettingsView` is a shallow menu: Daemon, Voice, Models, Languages,
Notifications, Diagnostics, About, each one screen (`SettingsScreens.swift`,
`ModelsView`, `LanguagesView`). Nothing needs saving. Switches write when
flipped; the daemon address and token are written, and the app repointed,
when you leave the Daemon screen or tap Done. The first run opens on the
Daemon screen and cannot be closed until an address is in.

Above the menu, `UpdateNoticeRow` shows `/api/state`'s `update` object
(daemon docs/dev/update.md) as one line when it reports a newer release
or an installed one not running yet; `UpdateInfo.notice` (SheepdogKit,
`UpdateNoticeTests`) picks the words from `available`, `how`,
`restart_pending` and `auto_restart`. Settings refreshes `/api/state` when
it opens. A daemon without the field shows nothing
(`UpdateNoticeUITests`, against `StubDaemon`).

The Daemon screen can also scan the pairing code `sheepdog pair` prints
(`PairingScanner.swift`: an `AVCaptureSession` with a QR metadata output,
behind the camera permission). `PairingLink` (SheepdogKit, tested) reads
`sheepdog://pair?u=…&u=…&t=…`; the screen fills in the token, tries every
address at once with `GET /api/hello` (three seconds each) and keeps the
first in the code's order that answers (LAN before Tailscale), then runs
Test connection.

The same code scanned with the system Camera app opens the
`sheepdog://pair?…` link in the app. `SessionViewModel.open(url:)` reads it
as a `PairingLink` before `AppRoute` sees it, closes Settings if it is open
(its fields would overwrite the result when it closes) and asks "Pair with
<host>?" (`PairingLink.confirmation`, which lists the addresses and never
shows the token). Nothing is contacted or saved before the user taps Pair.
Then `resolvePairing`, which Settings' scanner shares, finds the first
address that answers; the address and token are saved and the app
repointed, and a banner says how it went. Declining, or a code whose
addresses all fail, reopens the first-run Settings when no daemon is saved
yet. `PairingLinkUITests` drives this without a daemon.

## The offline demo

Settings ▸ **Try the demo** (and the same button on the first-run Daemon
screen) shows the app on a scripted world, for App Store review and for
anyone without a daemon. The bar is that a reviewer can try the core flow
with no server: agents finish, one asks for an approval, one fails; the
voice speaks; questions get answers.

- **`DemoWorld` (SheepdogKit, tested):** four agents and a script on an
  injected clock. Approve/decline change the story; `ask` answers a few
  questions from the world as it is (status, failures, the open prompt, one
  agent's result) and approves or declines on "approve" / "decline". It
  logs the same events a daemon would (`pending.created`, `voice.push`,
  `web.utterance`, `voice.tool_result`, …) and folds them through
  `ConversationHistory`, so the conversation page and the stream agree.
- **`DemoDaemon` + `DemoURLProtocol` (SheepdogKit, tested):** the world
  behind the daemon's own routes and JSON (`/api/hello`, `/api/state`,
  `/api/conversation`, `/api/events`, `/events` held open as SSE,
  `/api/pending/answer`, `/api/utterance`,
  `/api/agents/{pane}/screen` and `/input`). `DaemonClient` reaches it on
  a `URLSession` whose protocol class answers everything for the host
  `demo.sheepdog.invalid`, so no request leaves the app. The agent list,
  the approval card and the agent window are the real views.
- **A second `SessionViewModel(demo:)`** in a full-screen cover
  (`DemoShell`), with its own settings (a throwaway defaults suite, secrets
  in memory) and a cache in a temporary directory. The real view model,
  its address, token and conversation are untouched; leaving drops the demo
  model and reopens first-run Settings if no daemon is saved. The demo
  model skips the Live Activity, the intents and push registration.
- **No `/ws`:** `runDemo(_:)` takes the session core's socket and audio
  effects. The socket "opens" at once with a connected status; the
  `DemoVoice` speaks the world's lines and answers with
  `AVSpeechSynthesizer` (its start/stop drive the orb's speaking state) and,
  where `SFSpeechRecognizer.supportsOnDeviceRecognition` holds and the user
  allows it, hears questions with `requiresOnDeviceRecognition` and sends
  them as typed questions. Otherwise the status line says to tap a
  suggested question (the `DemoHeader` under the toolbar) or type.
  Nothing calls a cloud model.

## Empty and error states

`FlockPaneState` (SheepdogKit, tested) decides what an empty Agents pane
says from the connection: connecting, reconnecting, no agents, not
connected, daemon unreachable, token refused, update needed. `StateViews`
shows each as a `ContentUnavailableView` with one action. Agents already on
screen are never hidden by a connection problem. The orb's halo and the hold
spring honour Reduce Motion.
