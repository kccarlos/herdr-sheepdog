# Architecture

There are two targets and one rule: anything that can be tested without a
microphone lives in `SheepdogKit`. The app target is SwiftUI views and
`@MainActor` view models built on top of it.

The restructuring of the app's state and connection handling
(per-computer runtimes, a connection state machine, address failover) is
in [ios-state-architecture.md](../../../docs/dev/design/ios-state-architecture.md),
accepted and built in phases (its section 6 says which are done).

```
┌──────────────────────────── Sheepdog (app target) ────────────────────────────┐
│  MainView ── PaneSwitcher ── AgentStrip ── ConversationView / FlockView /      │
│              PinnedApprovalCard ── VoiceCapsule ── CapsuleComposer             │
│  SettingsView ── ModelsView                  PushCoordinator (APNs)            │
│        │                                                                      │
│  SessionViewModel (@MainActor): orchestrates; VoiceEngine adapts SessionCore  │
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
│  Metrics               ComputerLink          Push / MarkdownRenderer          │
│                        SessionPolicies       VoiceProviders                   │
│                                              ComputerStore / SecretStore      │
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
  10 s, the socket drops and tells the link (see "The connection").
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

Auth is the session ticket (`Authorization: Bearer sds_…`) on every request
to the paired computer, **including the WebSocket upgrades**, so it never
appears in a URL. Each paired computer has its own `DeviceCredentials`
(SheepdogKit, from `ComputerStore`), which signs in with the device key,
once for all callers. A `DaemonEndpoint` names its computer's credentials,
and every client and socket checks that computer's certificate by its pin
(`EndpointTrust`, which follows the endpoint across `update(endpoint:)`,
and `PinnedTrustDelegate`); see
[security.md](../../../docs/dev/design/security.md) section 5.5. After a
failed reconnect, `GET /api/state` (where a 401 is plain) tells whether the
phone was revoked: that opens "Please scan again".

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
3. **`URLSession.bytes(for:)` asks only the task-level auth challenge.**
   With a session-level method alone, the pin was never checked for
   `/events`, the system rejected the self-signed certificate (-1202), and
   the stream never opened on a paired phone. `PinnedTrustDelegate` now
   answers only at the task level, where URLSession sends every task's
   server-trust challenge (web sockets included), and each request passes
   its own `attempt()` delegate, so a key that matches no pin is that
   request's verdict alone. `EventsTLSE2ETests` opens `/events` over
   pinned TLS against the pair fixture daemon; `PairingE2ETests` checks a
   wrong pin per request and that a cancellation is never a mismatch.

The stream belongs to the computer's link (see "The connection"): it is
open while the link is online and in the foreground, it does not retry by
itself, and a drop tells the link, which probes once and reopens it.

## History and the cache

The app fetches `GET /api/conversation` (the newest page, 200 items) at
launch, before and without Connect. It fetches it again on every (re)open
of the event stream, on return to the foreground, and after each voice
turn. Under `ConversationHistory.applyNewest`, the server is the record for
the id range its page covers. Live items newer than the page survive, and
older pages come from `before=<next_before>`.

`ConversationCache` writes the newest 300 items, `next_before` and the last
event id to `Application Support/Conversations/computer-<daemon id>.json`,
one file per computer whatever address reached it (`<host>_<port>.json`
for a loopback fixture with no pairing), excluded from backups. The token is in neither the file nor its
name. `ConversationTimeline.rows` builds the screen: history and live
bubbles merged by time, date dividers, `notice` items as dividers, and
duplicate copies of a pinned question folded to one-line pointers
(`QuestionFocus`).

## Approvals

`ApprovalFlow` (Kit) is each computer's answer state, kept on its
`ComputerModel` (`model.flow`): one phase per question and the voice's
pinned question. Every surface reads it (the pinned approval card through
`model.approval`, the question cards and the floating questions through
`model.phase(id)`), and every answer writes it under the question's one
key: a tap anywhere (`ComputerModel.answer`) and a notification's Approve
while the app runs (`answerFromNotification`, which joins a tap in
flight). A question goes from *open* to *sending* (one option, further
taps ignored) to either *answered* (the daemon's `spoken`), *gone* (409)
or back to *open* with the error shown; a reply settles only a question
still sending. The voice's card says the answer the conversation's way
("You approved — Yes · by tap"), other cards "Answered: Yes". A
`pending.confirmed` from somewhere else settles the pinned card as
answered by voice; `expired`, `cancelled`, `replaced`, or a state refresh
without the question dismisses it while open. A settled card stays 3.5 s
(`settledCardLinger`, the model's clock), then the next question shows.
`/api/state`'s `pending_action` is the source of truth (refreshed by the
link's probes, at least every 30 s, and on every `pending.*` event), and
the event is the fast path. The pinned question and its phase stay with
their computer across a switch (decision 6). The POST to
`/api/pending/answer` sends the option's 1-based `index`. On the Agents
pane the card sits on its agent's card instead of above the voice
capsule.

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
  buttons on its agent's card (`questionCard(for:)`): the voice's own shows
  the pinned card, any other its own phase; both answer through the
  computer's model (`POST /api/pending/answer` with its id). Options are the agent's labels verbatim, in an `OptionFlow`
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

## iPad and several windows

The app runs on iPad (`TARGETED_DEVICE_FAMILY` 1,2 for every target; the
iPhone stays portrait, the iPad turns every way) with several windows
(`UIApplicationSupportsMultipleScenes`). Design: docs/dev/design/app-redesign.md
section 6.

- **One session, several windows** (`Scenes.swift`). The App holds the one
  `SessionViewModel`, so there is one voice session whatever the number of
  windows. `SceneCoordinator` tracks the main windows (`MainSceneRoot`
  registers each one):
  - **the audio owner** is the oldest main window still open. It shows the
    full capsule, reports `visibility` and turns the app-wide phase
    (`\.appPhase`, the `App`'s own `scenePhase`, which is the aggregate)
    into `becameActive` and `enteredBackground`. Another window going to
    the background does not end the session.
  - **the presenter** shows the session's sheets (Settings, New agent,
    Spaces, a card, pairing, the primers, the demo): the window whose menu
    asked (`SessionMenus` calls `request(from:)`), else the owner.
    Otherwise every window would open the same sheet.
  - **One sheet at a time** (`Presentation.swift`). Every session sheet and
    cover is a `Presentation` case whose `id` comes from what it shows
    (`settings`, `card.<id>`, `pairing.confirm.<daemon>.<code>`), never
    `UUID()`, and `SessionViewModel.presentation`
    (`PresentationCoordinator`, over Kit's pure `PresentationQueue`) owns
    which one is up. Code asks with an intent (`present`,
    `presentInstead`, `dismiss`); a request while another is up waits in
    the queue, and the next shows from the sheet's `onDismiss`, never after
    a guessed delay. Work that must wait for a clear screen (open an
    agent's window after a card or Spaces closes) is `dismiss(then:)`.
    Sheets local to one screen (Changes, More keys, Add a computer inside
    Settings) stay `@State`, with ids from their content too
    (`PairingSheet.id`).
  - every other window shows the **mini capsule** (`MiniCapsule`): a 30 pt
    orb (Mute while listening, else the big orb's action) and the voice
    menu.
  - `SessionViewModel.start()` runs once, whichever window appears first.
- **Layouts** (`MainView.swift`). `MainView` picks the layout and wraps it
  in `SessionChrome` (the sheets, dialogs and lifecycle every layout
  shares). `PhoneRootView` is the iPhone layout, also used on iPad at
  compact width (Slide Over, a narrow split).
- **The iPad layout** (`IPadRootView.swift`), at regular width: a
  `NavigationSplitView`, balanced in landscape and `prominentDetail` in
  portrait (the sidebar slides over; portrait opens on the detail).
  - `IPadSidebar` (390 pt): the title with `+` and `⋯`, Conversation, All
    agents with the waiting count, then the agents under their spaces
    (`SidebarSections` in Kit: spaces by name, agents by alias, running
    only; no computer row with one computer), and the capsule at the foot.
    Its keyboard button selects the Conversation and focuses its composer.
    No agents: one line with the action that helps (New agent, Connect,
    Pair, Retry).
  - The detail (`SceneUI.detail`): the Conversation in a 720 pt column
    with the open questions pinned above `IPadComposer`; All agents
    (`AllAgentsView`: `AgentCard`s in two independent columns, one at the
    accessibility sizes; `AgentFilter` All / Waiting / Working with a
    plain empty state and Show all; Expand all fills `expandedAgents` with
    every card that has no question; expanded cards add "Earlier", up to 3
    older messages, `Agent.earlierWorkerMessages`); or
    an agent's window as a pane (`AgentScreenView(pane:)` with a
    `PaneChrome`: its own header with the dot, name and space, ⋯, and
    Close instead of a pop). A shell pushes on the detail's own stack.
  - With the sidebar not beside the detail, the detail's header carries
    Show sidebar and the `MiniCapsule`, so one capsule is on screen.
- **Two panes** (`AgentPanes.swift`): `IPadDetail.pair` puts two agents
  side by side in landscape and stacks them in portrait, each with its own
  question, keys and composer. `SceneUI` holds the rules: a sidebar row
  focuses its agent or replaces the focused pane (`show`); a new agent, a
  drop or the pane's split menu goes beside the current pane, and of two
  the unfocused one makes way (`beside`); the same agent is never in both.
  The focused pane gets a 3 pt ink line; a tap, a key or a prompt in a pane
  focuses it (`AgentWindowController.onActivity`, since the terminal view
  takes the touches). `PaneDivider` drags between 30 and 70 %, and is
  adjustable with VoiceOver.
- **Dragging** a sidebar row carries an `AgentWindowRoute` (Transferable,
  the exported type `com.github.kccarlos.herdr-sheepdog-ios.agent` in
  project.yml). Over the detail a dashed drop target shows where it lands.
- **Open in New Window** (pane ⋯, only with `supportsMultipleWindows`)
  opens the `agent` `WindowGroup` with that route: `AgentWindowScene`, the
  pane alone with the mini capsule and ⋯ ▸ Close Window. It never
  registers with `SceneCoordinator`. While the demo is open it shows the
  demo's view model (`SceneCoordinator.demoModel`).
- New agent, Spaces and Settings are form sheets on iPad
  (`presentationSizing(.form)`); Start is ⌘↩ (`keyboardShortcut`).
- **Keyboard** (`Shortcuts.swift`): ⌘N, ⌘⇧M and ⌘1…9 (the sidebar's
  order, `SidebarSections.agents`) are invisible buttons with
  `keyboardShortcut` in each window (`KeyboardShortcuts`), not app
  `Commands`: the demo is a cover with its own view model, and the window's
  own shortcuts reach it and work while a terminal has the keyboard. ⌘K
  (Talk to the next computer that answers) exists only with several
  computers.
- **Pointer:** sidebar rows, the filters and the round and header buttons
  have hover effects; the pane divider highlights.
- **Tests:** `IPadUITests` (offline demo, skipped on iPhone) covers the
  sidebar and detail, portrait, All agents, two panes with drag and drop,
  Open in New Window and the shortcuts. ios.yml runs only these on an iPad
  simulator, from the same build as the iPhone run.
- **Opening a window** goes through one seam: `\.openAgentWindow`
  (`WindowRequest`: an agent, a new agent, a shell). The iPhone layout
  pushes on `navigationPath`. Requests from the session itself (a card's
  Open window, a Live Activity row) arrive as `model.windowRequest`, which
  the layout opens and clears.

## Several computers

Design: docs/dev/design/app-redesign.md sections 3 and 4 (option A). The
store is `ComputerStore` ("Settings" below).

- **One runtime per computer, the current one included** (Kit,
  docs/dev/design/ios-state-architecture.md 3.1). `ComputerFleet` holds
  every paired computer's runtime (plus one for a bare address or the
  demo) and `currentID`:
  - `ComputerConnection`, an actor, does its I/O: one immutable
    `DaemonClient` per address, `GET /api/state`, the answers and the
    `/events` loop;
  - `ComputerModel` (`@MainActor @Observable`) is what the views read: the
    agents, the open questions with their tap phases, `reachability` and
    `canTalkTo`, all from its own `ComputerLink`, which runs in a
    `LinkRuntime` there. It also keeps the computer's conversation
    (`ComputerConversation`: history, live bubbles, activity, the last
    event id), its last state, its hello and its features.
- **The session attaches to the current computer.** `attachCurrent` hooks
  `SessionViewModel` to the current model: its link (the chip), its
  state, its `/events` output (folded into the conversation) and the
  stream's life. Every other model is detached and refreshes itself on
  its own events. The voice (`/ws`) and the terminal (`/term`) are
  pointed at the current computer. Nothing from another computer reaches
  the current daemon, the conversation or the voice.
- **Requests on the current computer** go through `current { … }`
  (`ComputerFleet.onCurrent`): the reply comes back only if that computer
  is still current at the same epoch, else it throws (`Moved`, `Stale`)
  and nothing is applied. That is for reads.
- **Every tap that changes something** goes through the computer's
  `ActionRunner` (`ComputerModel.actions`, Kit; design 3.5), captured when
  it is tapped: the session's `action(key, kind) { … }` picks the current
  computer then, and the action finishes there even if the app switches
  (decision 6). That covers answers (the voice's approval card, the other
  questions' cards, a notification's Approve while the app runs, all under
  the question's key `answer:<computer>:<question>`, so they join), the
  typed line, New agent, agent input and keys, agent actions (rename,
  stop, close, close a shell) and the Spaces actions.
  - **One per key.** A second run under a key in flight joins the first
    and gets its result. Keys are per surface (`utter:<computer>`,
    `spawn:<computer>:<sheet>`) and carry what is sent (the prompt, a
    name, a command line), so the same words twice join and different
    words both go; keys add a sequence, since every press counts. The New
    agent sheet's idempotency key follows its choices.
  - **A timeout per kind** on the model's clock (answers 10 s, a typed
    line 10 s, New agent 60 s, input 5 s, Spaces 30 s), also set on the
    request, so New agent is not cut at the session's 8 s. No reply
    (`ActionRunner.isTransport`) retries once: an answer always (it is
    idempotent by question), a keyed action only on a daemon whose hello
    says `idempotency_keys`, with the same `Idempotency-Key` (a
    `@TaskLocal` that `DaemonClient` puts on the POST). A reply, even a
    refusal, is never retried. The typed line and the New agent sheet keep
    the key across the user's Try again after no reply, and drop it after
    any reply.
  - **The error shows where it was tapped**, as `RequestFailure` with that
    computer's link: a timeout while it answers is "Taking longer than
    usual… Try again", an unreachable one says so in the link's words.
  - **The background.** While any action is in flight the app holds one
    short background task (`AppActionBackground`, about 30 s), and the
    link stays up (`LinkDemand.actionsInFlight`).
  - **Push and removal** go through the computer's model too, on the
    address its link uses: push registration and status, the
    notification's Approve (straight to the stored endpoint only when the
    app has no model yet, a cold launch), and the unpair on Remove, which
    runs before the store forgets the computer.
- **Each link** works the same way (see "The connection"): in the
  foreground it streams `/events` and refreshes on each event and every
  30 s; another computer is idle in the background (decision 2). On a constrained
  path (Low Data Mode) it keeps `/events` closed and refreshes on the 30 s
  timer only; `NWPathMonitor` feeds the path (see "The connection"). While the voice
  runs on the current computer it backs off up to 60 s. An answer in flight
  keeps its link up until it finishes.
- **Epochs.** Every reply carries the epoch it was asked in, and the model
  applies it only while that epoch is still its own. Re-pairing a computer
  or changing its address starts a new epoch (`renew`); removing it stops
  its runtime. Switching computers does not: an answer already sent
  finishes on its own computer (decision 6).
- `SessionViewModel+Computers.swift` keeps the fleet in step with the
  store (`syncFleet`, on launch and on every change to the list, the
  current computer or its address). The views read the models through the
  session view model, and SwiftUI observes each model's properties
  directly.
  `computerSnapshots` (`ComputerSnapshot`, Kit) is every computer in the
  store's order, with "4 agents · 1 waiting" or "Offline since 9:12".
- **Questions.** `floatingQuestions` is the current computer's questions,
  then the others' oldest first across computers (`QuestionOrder.others`,
  by `created_at`). Another computer's card carries a namespaced id
  (`RemoteQuestion`, `remote:<computer>|<id>`), because question ids are
  unique only within one daemon. `answer(question:option:)` routes it to
  that computer's `POST /api/pending/answer`. The card says
  "Home Mac · blog", "Answers by tap go to Home Mac." and **Talk to Home
  Mac**.
- **Talk to** (`talk(to:)`, `switchComputer(to:)`) changes the current
  id: the session attaches to the new computer's model and shows its
  conversation and last state at once, the voice ends cleanly and
  reconnects there if it was running, and a "Talking to Home Mac" divider
  is added. The computer it left keeps running, so an answer in flight
  finishes there; its live bubbles end with the voice, and its
  conversation is saved to its own cache. A computer that
  does not answer is not switched to (`canTalkTo`: its link is backing
  off, offline, not verified or refused, even while the quiet period
  still reads "Connecting…"); a banner says why, and its link retries. It is reached
  from the voice menu, a section header on the Agents page, All agents and
  the iPad sidebar, a floating question, a computer's Settings page, and
  ⌘K.
- **Order.** Computers keep the store's order (pairing order unless the
  user reorders) on the Agents page, the iPad sidebar
  (`SidebarSections(snapshots:…)`), All agents, the voice menu and
  Settings. Moving the voice never moves a section. Every section header
  is leading-aligned (`ComputerSectionHeader`) and has a filled voice
  marker, a hollow move marker or a dashed retry marker; tapping only the
  name expands or collapses its list.
- **Where they show** (several computers only): the voice menu's Talk to;
  a section per computer on the Agents page and All agents (another
  computer's cards have no window button; an offline computer is one
  dashed row with Retry); the iPad sidebar's computer rows; strip chips
  for agents blocked on another computer, which open the Agents page; the
  Agents badge, which counts every computer; "Work Mac · webapp" under an
  agent window's name; Settings ▸ Computers with each computer's page
  (`ComputerSettingsView`: a local name, Talk to or the current
  computer's Connection, Models and Languages, Remove). **Add a computer**
  is also there with one computer.
- **Push.** Each daemon puts its id in `sheepdog.computer`
  (docs/dev/push.md). `PushCoordinator` registers this phone's token with
  every computer. It keeps one content key per computer
  (`PushContentKeyStore.forComputer`), and the Notification Service
  Extension and the Live Activity widget try each key. With several
  computers the app writes the names to the App Group
  (`PushComputerNames`), and the extension sets the sender's name as the
  subtitle. Approve and Decline answer on the computer named in the
  payload. A Live Activity row from another computer opens the Agents
  page instead of switching.
- **Not built yet:** an agent window for another computer's agent (its
  card has no window button: talk to that computer first) and reordering
  computers.
- **The Live Activity** names the current computer, and its waiting rows
  continue with the other computers' rows (up to three in all), each
  naming its computer.
- **Tests:** `ComputersTests`, `ComputerStoreTests`, `ComputerRuntimeTests`
  (two computers on a scripted network and a manual clock) and
  `ComputerSwitchPropertyTests` (fast switching with requests in flight:
  no reply is applied to the wrong computer, every answer finishes on its
  own), `ComputerFleetTests` and
  `ComputerRuntimePropertyTests` (seeded interleavings of refreshes,
  answers, replies in any order, re-pairing and the app's phase: no reply
  lands on the wrong computer or an older pairing) in Kit;
  `ComputersUITests` against `StubDaemon(.computer…)`: one computer
  shows no label, and two computers plus an offline one cover Talk to,
  the sections, a question answered on another computer, Settings and
  switching.

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
- It is the current computer's `/term` channel (`TermHub` is its
  `LinkChannelClient`, see "The connection") and never retries by itself.
  A dropped connection tells the link, which probes once and says when to
  open it again; then it reopens every channel the daemon had not closed,
  at its latest size and without takeover. While the link does not allow
  it (offline, no network, the background) it is closed and keeps its
  channels for then. A WebSocket ping goes every 20 s, and a ping still
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
      │            └── AgentWindowContent (values only: AgentWindowInputs)
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
- **Frames stay out of the views.** A `/term` frame goes from the socket
  to the surface; a `/ws` `screen` push goes through `ScreenStreamRouter`
  (Kit) straight to the controller of the window showing that pane. No
  published property carries either. The controller publishes only slow
  state (phase, notice, sending, typing, `hasScreen`,
  `snapshotUsable`); the visible text for VoiceOver and Copy is read at
  most twice a second and set on the terminal view directly.
  `AgentWindowView` observes the session and hands `AgentWindowContent`
  plain values (`AgentWindowInputs`), so a session change that leaves
  them equal re-renders nothing in the window; the shell window's content
  observes only its controller. The header and the ⋯ menu are `Equatable`
  views of values.
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
  count. That works in every kind of pane because the daemon sends one wheel
  notch per line (the contract's "Wheel notches"): herdr alone would send a
  full-screen app such as Codex one notch per message, so a long drag would
  move it far less than the finger and Latest would stop short. The app
  sends no cell; the daemon puts the wheel in the middle of the grid.
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

## The Live Activity

The voice session's Live Activity is a lock-screen banner first; the
Dynamic Island keeps a plain, system-style layout. Its content state is
SheepdogKit's `LiveActivityContent`, which the widget extension links for
the type and its words, so the app sends data and the widget writes the
sentences:

- the voice's phase (`Phase`, raw values on the wire) and mute;
- the current computer's name, only when several computers are paired
  (nil with one, so one computer looks as it always did);
- up to three waiting agents (`Row`: pane, name, the question in one line,
  and the computer only for a row from another computer), the open
  questions' order first (oldest first), then by how long they have been
  blocked;
- how many agents are waiting, working and done.

`LiveActivityContent.make` builds it from the session state, the agents and
`/api/state` `questions`; `LiveActivityContentTests` covers the mapping, the
cuts (questions to 90 characters, names to 32) and the encoded size, which
must stay well under ActivityKit's 4 KB.

**Layout.** The lock screen gives a Live Activity about 160 pt of height.
The banner has a header (orb, headline, the line under it with the
computer and the counts, Mute and End at 44 pt) and the waiting rows under
a hairline. Rows get three lines up to Large text, two at the largest
standard sizes and one at the accessibility sizes (capped at AX2); when not
every waiting agent fits, the last line says "N more waiting"
(`visibleRows(slots:)`). A row is a `Link` to `sheepdog://window/<pane>`
(`AppRoute.window`, with `?computer=<id>` for another computer), which
opens the agent's window; the rest of the banner opens the app. Each row
is one VoiceOver element ("auth-fix asks: Run npm test in webapp?"), and
the header reads the phase, the computer, the waiting count and the
counts. The widget has no asset catalog, so its colours are the app's
tokens in code (`ActivityPalette`).

**Lifetime.** `VoiceLiveActivity` (`AwayControls.swift`) starts the
activity when a voice session starts (the app is in the foreground then;
`Activity.request` fails in the background), updates it from every state
change while the app runs (the audio session keeps the app running with
the screen locked), moves the stale date along every five minutes, and
ends it with immediate dismissal when the session ends. A banner the user
swiped away stays gone until the next session; one a killed app left
behind is ended at the next launch and goes stale (dimmed, "May have
ended") after 15 minutes. It does not run without a voice session
(docs/dev/design/app-redesign.md, closed question 9).

**While the app is away.** The activity is requested with
`pushType: .token`; every token iOS issues goes to the daemon
(`POST /api/push/activity`, `DELETE` when the activity ends). After 30
seconds without a connection from this phone the daemon pushes each
change (phase `away`, "Not listening"), with the rows sealed under the
push content key (`sealed {c, n}`, `rows: []` in the clear). The widget
joins the App Group so `openedWithStoredKey()` can read that key from the
group keychain and open the rows; without it the banner shows the counts
("2 agents waiting"). The daemon side is the daemon's docs/dev/push.md,
"Live Activity updates".

**Limits and cost.** iOS ends an activity after 8 hours (it can stay on
the lock screen up to 4 more), and an update, local or pushed, is at most
4 KB. Local updates cost nothing; pushed ones go through the relay like
notifications, priority 10 only for a new waiting agent (iOS budgets
those) and 5 otherwise.

`-sheepdogLiveActivityGallery one|several|many|quiet|away|away-counts|stale` draws the
widget's own banner on a stand-in lock screen for screenshots (a simulator
cannot be locked from a script); `-sheepdogDemoLiveActivity <state>`
starts a real activity with sample rows and no session.

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
buffers arrive). In hold-to-talk it judges only while the gate is open
(orb held, or the lock-screen tap-to-talk): releasing the orb disarms it
and clears the state, and the next press starts a fresh 3 s grace period,
so an idle orb never raises a false alarm (or its haptic and restart).
That means the capture path is broken. The status line
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

- **Its look** is one of 16 `CapsuleMode`s (SheepdogKit, table-tested), from
  the session state, the talk mode, holding, whether a question is open,
  whether the link says the computer is unreachable
  (`LinkPresentation.computerUnreachable`) and whether another window holds
  the audio: live (orange orb with the cream ring), quiet (a question has
  the accent), speaking, thinking and stalled (with the stop badge), muted
  (the red mic), paused, no microphone, reconnecting, stopped and stopped
  quiet (the voice ended by choice, the computer answers: an orange mic orb
  starts it, "Not talking · tap the orb"), elsewhere (iPad, another window
  holds the audio: "In another window", and "Use here" replaces the
  keyboard), offline (no session and the computer unreachable: wifi-slash,
  the orb retries), voice off, hold and holding. `-sheepdogCapsuleGallery`
  shows them all on one screen. `SceneCoordinator.claimAudio` is "Use
  here": it puts the window first in the one owner order, the only audio
  ownership there is.
- **What the orb does** is `VoiceBarModel`: Connect when disconnected,
  Stop while the voice is thinking or speaking, Resume after an audio
  interruption, otherwise nothing. Its words too: "Tap the orb to connect",
  "Listening — just talk", "Muted — tap to unmute", "Hold the orb to talk".
- **The voice menu** (tap the name): the connection, Manage computers… and
  End voice session ("Your agents keep working."), or Start voice session.
  With several computers it adds Talk to (see "Several computers"), and
  the capsule shows the current computer's name instead of the voice's.
- **The composer.** The keyboard morphs the capsule into `CapsuleComposer`:
  the quiet orb (back to voice), the field and the ink send button.
  Sending goes through `POST /api/utterance`.
- A first-run coach mark explains that the mic is only for muting; the
  first time Mute is used, the status line explains it. **Hold to talk**
  (Settings ▸ Microphone) turns the orb into a push-to-talk button and
  hides Mute.

The **connection** is the current computer's `ComputerLink` (see "The
connection" below), rendered through `LinkPresentation`: "Connected",
"Connecting…", "Reconnecting…", "Offline", "No network", "Not verified",
"Scan again", "Update needed" or "Not connected". It says whether the app
can talk to the computer, never the voice state (the capsule shows that),
and never the `/events` stream alone, which Health shows as "Live
updates". It is the first row of the voice menu, which opens
`ConnectionDetailView`: the status and its explanation, the address, a
timed round trip, the daemon's version, the voice provider and model, the
orchestrator, Health, and Reconnect / Open Settings.

## The connection

`ComputerLink` (Kit) is one computer's connection as a value, the one
truth for "can the app talk to this computer, and if not, why"
(docs/dev/design/ios-state-architecture.md 3.2–3.4). Each computer's
`ComputerModel` runs its link in a `LinkRuntime` ("Several computers"):

- it executes the link's effects: a pinned `GET /api/state` probe with a
  4 s timeout (through the computer's `ComputerConnection`, one immutable
  client per address), timers with tokens (`LinkClock`), the channel set,
  the address in use and the race winners per interface
  (`LinkWinnerDefaults`);
- it feeds back every result, plus what the rest of the app sees: each
  request's outcome (`ComputerModel.request`), every channel opening and
  dropping, a handshake mismatch, the network path, the app going to the
  background and back;
- a probe's `/api/state` reply is applied as the data, so the 30 s
  safety-net refresh replaces the old 5 s poll;
- switching computers rebuilds nothing; new credentials start a new epoch,
  and the old epoch's late replies land nowhere. A new address set or
  Prefer does not: the pin, not the address, is the computer
  (`ComputerModel.setAddresses`, `prefer`);
- a promoted pin or a certificate expiry learned at sign-in reaches it as
  `credentialsChanged(.pinPromoted)` (`DeviceCredentials.certificateChanged`),
  so the rotation window is current without a relaunch.

Errors are classified per request (`LinkError(_:)`): a key that matches no
pin is `DaemonError.untrusted`, never "scan again" by itself. The link
decides: with one address it is the untrusted-answer warning ("Not
verified", Try again); only a mismatch inside the certificate's rotation
window asks for a new scan. A 401 with a known reason (revoked, unknown
device) is `refused`: the link opens the scan sheet, once, and the voice
ends. A request that times out is `DaemonError.timedOut`: with the link
online it is one slow answer, and the place the user tapped says "taking
longer than usual, try again" (`RequestFailure`), not "couldn't reach".

Every status surface renders `linkPresentation`: the voice menu's first
row, the chip's sheet, Settings ▸ Computers' row for this computer, the
Agents page's empty states (`FlockPaneState.resolve(link:)`), the iPad
sidebar, Health, and this computer in the snapshots (`Reachability(link)`).
Foreground notifications stay quiet while the link is usable.

**Channels and one backoff.** `/ws`, `/events` and `/term` are the
link's channels, and no socket retries by itself:

- the model opens `/events` itself; the session attaches the voice's
  `VoiceSocket` and the `TermHub` to the current computer's model
  (`ComputerModel.attach`, `LinkChannelClient`), and detaches them on a
  switch;
- a channel opens only while it is wanted (the voice is on, a window or a
  warm-up wants `/term`) and the link allows it (`linkAllows`: online,
  with the channel in the demand table below);
- a drop reports `lost` through its `LinkChannelReporter`, which carries
  the computer's epoch and the attachment, so a late callback from an
  older pairing or a computer the app left lands nowhere;
- the link probes once (hysteresis: still Connected), and on a good probe
  says `reopen`. The first loss reopens at once; a channel that keeps
  failing while the computer answers (lost again within 10 s of opening)
  waits on the link's `reopen` timer, `LinkBackoff` with full jitter, up
  to the cap. That replaced `VoiceSocket`'s and `TermSocket`'s own 1–10 s
  loops and `/events`' wait after failures;
- a failed probe makes the link back off (`waiting`, then `offline` after
  30 s), and every channel reopens when it is online again.

**The network path.** `SystemNetworkMonitor` (`NWPathMonitor`, Kit's
`NetworkMonitor.swift`) feeds `ComputerFleet.setPath`, which hands every
computer's model the path (`NetworkPath`: satisfied, the interface kind,
Low Data Mode), including a model added later:

- no network: every link is `noNetwork` ("No network", grey), retries
  stop and every channel closes; Connect says "This phone is offline."
  instead of "can't reach" the address;
- the network back, or a new interface (Wi-Fi ↔ cellular): every link with
  demand tries at once, from attempt 0. An online link re-checks without
  leaving online, and once that probe wins every open channel reopens on
  the new interface (a socket on the old one is dead before its keepalive
  says so). The first interface the monitor reports is not a change;
- Low Data Mode (`isConstrained` only, decision 2): other computers keep
  `/events` closed and refresh every 30 s.

The demo always has Wi-Fi (`ManualNetworkPath`), and the debug launch
argument `-sheepdogNoNetwork` has none (`LinkStatusUITests`).

**Foreground and background** (`LinkDemand.channels(on:)`):

| | current computer | other computers |
|---|---|---|
| foreground | `/events`, `/term`, and `/ws` while the voice is on | `/events` (not under Low Data Mode), `/term` |
| background, voice on, Keep listening on | `/ws` only | idle |
| background, Keep listening off or voice off | idle: everything closes (the core has already paused the audio; the Live Activity says paused, not "Reconnecting…") | idle |
| an answer in flight | REST only, until it finishes | same |

Coming to the foreground kicks every link: a probe now, the backoff
starts over, and the channels reopen once it answers.

**Address failover** (design doc 3.3). The link races every address of
the computer: the store's address (the preferred one, set by Prefer) first,
then the pairing's (`raceAddresses`), with the remembered winner for the
current interface ahead of both:

- the race is staggered: the next address starts after 300 ms without an
  answer, or at once after a failure; the first pinned `GET /api/state`
  success wins and the probes still in flight are cancelled. The pin is the
  only check, so any address that verifies is the computer;
- the winner is remembered per interface (Wi-Fi, cellular, wired; decision
  10) in `LinkWinnerDefaults` (`computers.linkWinners.<id>`), and removing a
  computer forgets them;
- one address at a time: the model passes the link's address with every
  request and `/events` open (it never relies on the actor having heard
  `use` first), and tells the attached `/ws` and `/term` clients
  (`LinkChannelClient.linkUses`) before the reopen that moves them; the
  session's `channelEndpoint` does the same when it repoints the voice or
  the terminal after a switch;
- a pin mismatch on the address in use starts a full race, so another
  address that verifies wins and the mismatching one is marked; only when
  nothing verifies is it `untrustedAnswer` (decision 1);
- Settings ▸ Connection ▸ Addresses renders `AddressRow` (Kit): "In use on
  Wi-Fi", "Checking…", "Answered with another key", and Preferred. Tapping
  a row prefers it (`preferAddress`): the link re-races without leaving
  online and forgets the old winners; there is no separate check. Add still
  checks an address by the pin before keeping it.

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
and the "paused by iOS" state. `VoiceEngine` (app target, `@Observable`)
adapts it: it owns the core, the `AudioEngine`, the `/ws` `VoiceSocket` and
the `VoiceMeter`, and publishes the orb, mute, talk mode, hold, the
microphone modes and the one-time hints for the views (`model.voice`).
Every callback (a tap, the socket, the event stream, the audio engine, the
scene phase, the UI ticker) becomes a `SessionEvent`. `apply(_:now:)`
updates the core and returns `[SessionEffect]`, and the engine runs those
effects in order. Its host (`VoiceEngineHost`, today `SessionViewModel`)
sees every effect first and runs the ones that are not the voice's: the
probe (behind the AI consent gate), the conversation's lines, banners and
refreshes, and the offline demo's on-device stand-ins. The socket's other
traffic (transcripts, the agent window, cards) goes to the host too.
`VoiceEngineTests` drive the engine with a host that runs every effect. `SessionCoreTests` pins the behaviour as event sequences with
their exact effects.

| Group | Fields |
|---|---|
| `connection` | `wantsConnection`, `socket` (`SocketLife`), `handshake`, `muteSync` (`MuteSync`), `socketMuted`, `socketGateClosed` |
| `voice` | `status`, `closedReason`, `stalled`, `provider`, `saidVoiceIsOff` |
| `mic` | `talkMode`, `muted` (the user's intent), `holding`, `gateClosed`, `noSignal` |
| `playback` | `speaking`, `hold` (`SpeakingHold`), `heldSpeaking` |
| `thinkingExpiry` | a tool call's 30 s timeout |
| `suspension` | `pausedByIOS`, `backgroundSuspended`, `keepListeningWhenLocked` |

These are derived as pure computed properties: `sessionState` (the orb),
`voiceBar`, `statusLine`, `daemonMute`, `acceptsPlayback` and
`playbackBusy`. Whether the computer answers at all is the link's, not the
core's.

Behaviour worth knowing:

- **Reconnects** are the link's. While `/ws` waits for it the orb quietly
  says "Reconnecting…", for as long as it takes, until End; the chip says
  why (Reconnecting…, then Offline with Try now after 30 s, or No
  network). The voice raises no banner of its own for it (the old "still
  trying every few seconds" banner duplicated Offline). A refused token
  stops the session at once. If another client takes the session (the
  daemon serves one at a time), the app says so once and waits.
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

## Re-renders

The session view model is still one `ObservableObject` (until phase 8 of
docs/dev/design/ios-state-architecture.md; the scene, window, push and
consent objects are already `@Observable`, and so are the models split out
of it so far: `HealthModel`, `SettingsModel` (models, chattiness, languages,
voice providers), `NoticeModel` (banner, update and pairing notices),
`LinkModel` (link presentation, address rows, stream life, handshake, hello),
`NavigationModel` (pane, navigation path, Settings route, window request),
`AgentsModel` (agents, open questions and the voice's question, read from
the current `ComputerModel`; opened cards, screen covers, shell offer), `ConversationModel` (rows, paging, the typed line) and
`VoiceEngine` (the orb, mute, talk mode, hold, microphone modes, hints; views read
them through the session, as in `model.settingsModel`), so anything the session itself announces
re-renders every view that observes it. The rules that keep that cheap:

- **Assign only on change.** A poll or a refresh assigns a published
  property only when the value differs (`SessionViewModel.assign`,
  `ComputerModel.set`); an equal value still announces a change, and an
  `@Observable` setter notifies its observers even for an equal value.
  Another computer's refresh that found nothing new must notify nothing
  (`ComputerRuntimeTests.testAnUnchangedRefreshAssignsNothing`).
- **High-frequency values have one reader.** Terminal frames: the surface
  (above). Audio levels: `VoiceMeter` is `@Observable`, and only
  `MeteredOrb` and `MeteredMicButton` read `micLevel` and `outputLevel`, so
  the capsule and its menu do not re-render per level tick. Anything that
  changes more than twice a second names its only reader in a comment.
- **Menus are drawn from values.** Every menu that stays on screen while
  the session changes is a `StableMenu` (or an `Equatable` view like
  `AgentWindowMenu`): it re-renders only when what it shows changes, so an
  open menu does not flicker or close. `SessionMenus` builds the voice,
  `+` and `⋯` menus from `voiceValue` and `barValue`.
- **Tests.** `RenderChurnUITests` and `IPadRenderChurnUITests` open each
  menu under `-sheepdogRenderChurn` (the model announces a change ten
  times a second) and check it stays open and is not drawn again;
  `testTerminalFramesReRenderOnlyTheSurface` streams frames
  (`-sheepdogFrameChurn`) and counts announcements with
  `-sheepdogCountRenders` (`RenderProbe`).

## Concurrency

The project uses Swift 5 language mode on purpose. The audio path (tap
callbacks, `AVAudioConverter` input blocks, `AVAudioPlayerNode` completion
handlers) is not `Sendable`-clean. The design is still safe: the Kit types
are value types, the view models and the audio engine are `@MainActor`, and
the one real-time boundary is the explicit buffer copy in the tap.

## Settings

`SettingsView` is a shallow menu in three sections, each row one screen
(`SettingsScreens.swift`, `ModelsView`, `LanguagesView`):

- **Computer:** the computer's row (`ComputerRow`: `[computer] name` from
  `/api/hello`, a status dot and "Talking here · address" / "Offline ·
  address"), which opens the Connection screen, then what belongs to the
  computer: Models, Languages and the shell's state. With several
  computers this section becomes the list of computers, each with its own
  page; with one it stays a single row and no computer label shows
  anywhere else.
- **This phone:** Voice and Notifications.
- **Help:** Diagnostics and About; then Try the demo.

**The computers** (`ComputerStore`, SheepdogKit, `ComputerStoreTests`):

- Each paired computer is keyed by its daemon id. Its record
  (`PairedComputer`: name, the local name, addresses, pins, device id,
  paired date) is one keychain item, `daemon.computer.<id>`. The order, the
  current id and the address in use for each computer are in
  `UserDefaults`.
- A re-pair of a known id updates that computer in place; a new id is
  added at the end.
- The current computer is the one the user last picked, else the first.
  The app never changes it on its own. Removing the current computer moves
  the voice to the first one left.
- `SettingsStore` keeps the phone's own switches and reaches the current
  computer through `address`, `computer`, `credentials` and `endpoint`.
- `SessionViewModel.switchComputer(to:)` sets the current id and runs
  `endpointChanged()`'s "another daemon" path: the old voice session ends,
  the new computer's conversation loads from its cache, and the voice
  reconnects if it was running.
- `showsComputerLabels` (more than one computer) is the one switch for
  every computer label. Every such label's accessibility identifier starts
  with `computerLabel.` (Talk to's with `voice.talkTo`), so
  `ComputersUITests.testOneComputerLooksExactlyAsItAlwaysDid` catches any
  label that shows with one computer.
- **No update path from one computer.** The one-time move of the
  pre-multi-computer record was removed in phase 10 (no backward
  compatibility); a user of that old build pairs again.
- `-sheepdogComputers "Work Mac=127.0.0.1:7801;Home Mac=…"` (DEBUG) starts
  with loopback fixture daemons as computers (plain HTTP, no pin, ids
  `fixture-1`, `fixture-2`, …).

Nothing needs saving. Switches write when flipped; the address is written,
and the app repointed, when you leave the Connection screen or tap Done.
The first run opens on that screen ("Welcome") and cannot be closed until
an address is in.

Above the menu, `UpdateNoticeRow` shows `/api/state`'s `update` object
(daemon docs/dev/update.md) as one line when it reports a newer release
or an installed one not running yet; `UpdateInfo.notice` (SheepdogKit,
`UpdateNoticeTests`) picks the words from `available`, `how`,
`restart_pending` and `auto_restart`. Settings refreshes `/api/state` when
it opens. A daemon without the field shows nothing
(`UpdateNoticeUITests`, against `StubDaemon`).

Pairing is by code only (`PairingFlow.swift`): one sheet runs the camera
primer, the scanner (an `AVCaptureSession` with a QR metadata output),
"Pairing…", and the end: "Paired with X", "This code has expired or was
used", or "Please scan again" (`PairingProblem`). `PairingCode`
(SheepdogKit) reads the daemon's `https://…/voice#pair=1&d=…&k=…&e=…&u=…`
link, or the `sheepdog://pair#…` hand-off the voice page offers.
`SessionViewModel.pair` tries every address at once over TLS pinned to the
code's pin, makes the device key, enrolls once (codes are one-time), signs
in, and only then saves the computer. It repoints the app only for the
first computer or a re-pair of the current one; another computer is added
beside it. A link from
outside the app opens the same sheet, asking "Pair with X?" first; nothing
is contacted before Pair. Settings ▸ Connection shows the computer, "Paired
<date> · cert <pin>", Test connection, Scan a new code and Remove (`DELETE
/api/devices/self`, best effort, then the key and record are deleted).
`PairingLinkUITests` drives the sheet, including a full walkthrough against
the pair fixture daemon; DEBUG builds show any stage with
`-sheepdogPairingScreen <stage>` for screenshots.

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
