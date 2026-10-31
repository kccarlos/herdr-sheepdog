# Desktop client: a herdr-app panel and a menu-bar app

*Proposal, 2026-10-30. Nothing is built yet. Where this document says
"verified" it means one of three experiments run on a Mac on 2026-10-30
(microphone permission, a test mod's text input and event stream, a linked
private core); their notes are not committed. Code references are to `main`
at `562d0054`.*

At the computer, the user talks to sheepdog by voice and by text, inside
herdr-app, and sees the same conversation the phone shows. Away from the
computer they open the phone and carry on. This document says how that is
built without a second sheepdog app: one **panel** (a herdr-app mod) for the
screen, one small **Sheepdog.app** for the things a mod cannot do (the
microphone, the speaker, notifications, a global key), and a few daemon
additions that both the panel and the phone use.

It builds on [ios-state-architecture.md](ios-state-architecture.md) (the
voice session, `SceneCoordinator`, "Take it back"),
[conversation-timeline.md](conversation-timeline.md) (what the timeline shows),
[../ws-contract.md](../ws-contract.md) and
[../../iphone-app.md](../../iphone-app.md) (the feature list this ports).

## 1. Goals, non-goals and decisions

**Goals**

- Everything the phone does for the conversation, at the computer, inside
  herdr-app: read the timeline, answer questions by tap, type, talk.
- One conversation per daemon, whichever device you use. Typed lines made on
  the desktop show on the phone, and the voice can move between them.
- The daemon stays pure Go. The iOS app's behaviour does not change.

**Non-goals**

- Old herdr (before herdr-app on herdr-core). A TUI or the existing `/voice`
  web page may serve those users later.
- A windowed Mac app, Mac Catalyst, or "Designed for iPad" on the Mac.
- Agents on remote machines in v1, and any remote daemon in v1.
- Terminal, agent list, Spaces and shells: herdr-app already has them.

**Decisions made by the owner** (not reopened here)

1. The desktop client is for users of the new herdr-app on herdr-core.
2. The whole UI lives in a herdr-app mod panel ("the panel").
3. **Sheepdog.app** is a native macOS app, Developer ID signed (team
   `GV776BZVA8`), menu-bar icon only, no window. It is the macOS front-end
   process: microphone, speaker and echo cancellation (AVAudioEngine voice
   processing). It holds the `/ws` voice session like the phone, shows macOS
   notifications, owns the global hotkey and launches at login. It is shipped
   as a Homebrew cask. It is started through LaunchServices (`open`), never
   spawned by the daemon: in the experiment a child process got the
   microphone silently denied, while a Developer ID app opened with `open`
   got the prompt once and kept the grant across a re-signed update.
4. v1: the daemon runs on the same computer as herdr-app; the panel reaches
   it over loopback without auth. Remote machines' agents are not watched.
   Switching computers is v2 (through a loopback proxy in Sheepdog.app that
   the panel talks to); the panel's data layer is built around one base URL
   so that v2 only swaps it.
5. Typed text goes straight to the orchestrator and gets a text reply, which
   is recorded in `/api/conversation` so the phone sees it. It is not read
   aloud by default; the voice gets a silent context line. The phone gets the
   same path later.
6. The feature list is the phone's, minus what herdr-app has (section 3).
7. Quality: no hacks, no regressions, logic testable; SheepdogKit is reused
   where it is platform-neutral and platform specifics sit behind protocols
   in the app layer; nothing moves back into `SessionViewModel`.

## 2. Architecture

```
 herdr-app                                       macOS
+----------------------------+          +--------------------------+
| the panel (mod, JS)        |          | Sheepdog.app (Swift)     |
|  rail + panel + overlays   |          |  menu-bar icon, no window|
|  data layer: one base URL  |          |  AVAudioEngine + VP      |
|                            |          |  notifications, hotkey   |
|  exec: open sheepdog://... |--------->|  login item              |
+------+---------------------+ LaunchServices +-------+------------+
       | HTTP + SSE (GET /events, raw net socket)     | /ws (audio + control)
       | http://127.0.0.1:7777, no auth               | same as the phone
       v                                              v
+-----------------------------------------------------------------+
| sheepdog daemon (Go): hub (one /ws client), voice, orchestrator, |
| /api/conversation, /api/state, /api/turn, /api/voice/*           |
+---------------------------+-------------------------------------+
                            v herdr-core (agents)        ^
                                                         | /ws, TLS + ticket
                                                 iPhone app (same hub)
```

**The panel never opens `/ws`.** The hub serves one client; a second one
replaces the first (`reason: replaced`). A panel on `/ws` would cut off the
phone's voice. The panel uses HTTP and SSE only.

**How the panel controls voice.** Decision: panel, then daemon HTTP, then
a `/ws` control message to whichever client holds the voice.

- Mute already works this way since #487: `POST /api/voice/mute`
  changes the hub state and the attached client receives the confirming
  `status`. Stop and hold-to-talk get the same shape (section 4).
- It works for the phone too: the panel does not need to know who holds the
  voice, and a phone in a pocket can be muted or stopped from the desk.
- Rejected: the panel talking to Sheepdog.app over a local socket. It needs a
  second protocol, only works when the Mac holds the voice, and moves state
  out of the daemon.
- The one command the daemon cannot relay is "Use here" when no `/ws` client
  is on the Mac. The panel runs `open sheepdog://talk` through `exec`, which
  LaunchServices routes to Sheepdog.app (starting it if needed). The app then
  connects to `/ws`, which replaces the phone ("Take it back"). `sheepdog://`
  is the scheme the iOS app already registers (`AppRoute`); Sheepdog.app
  registers it too and shares `AppRoute`. Equivalent: `open -a Sheepdog`.

**Handoff.** Phone opens its app: `/ws` replaces the Mac, the phone's timeline
shows the same `/api/conversation`, typed lines included. Back at the desk:
"Use here" in the panel.

## 3. Feature table

| Phone feature | Desktop | Why |
|---|---|---|
| Conversation timeline: dialogue, Needs you, agent outcomes, context, system notices, Details/thinking, technical-events toggle | Panel | Same classification as the phone ([conversation-timeline.md](conversation-timeline.md)), ported to JS. |
| Question and approval cards, tap to answer, compact mode | Panel | `POST /api/pending/answer`. |
| Suggested replies | Panel | A tap sends through `POST /api/turn`. |
| Orchestrator cards (tables, lists) | Panel; "agent screen" becomes `agent.focus` in herdr-app | herdr-app shows the screen. |
| Text input | Panel | `POST /api/turn` (section 4). |
| Voice: talk, mute, stop, hold-to-talk, voice state, voice name | Panel buttons; audio in Sheepdog.app | The mod has no microphone. |
| Global hotkey | Sheepdog.app | A mod key has no key-up, so it can only toggle; the app can hold. |
| Computer switcher, "voice elsewhere / Use here" | Panel. "Use here" in v1, switcher in v2 | v1 has one computer. |
| Settings: models, languages, AI-service consent, paired devices, pair a phone by QR, update notice | Panel overlay | Same daemon endpoints as the phone. |
| Agent details (works on, last result, last question) | Panel overlay, opened from an agent name in the timeline | |
| Agents tab, terminal, key bar, attachments, Spaces, new-agent sheet, shells | Dropped | herdr-app has them. |
| Live Activity, Dynamic Island | Replaced by the menu-bar icon | Voice state at a glance. |
| APNs push | Replaced by macOS notifications from Sheepdog.app; herdr-app `notify()` toast while the panel is visible | |
| Siri, Shortcuts, Action Button | Replaced by the global hotkey | |
| Earbud remote, haptics, demo mode | Dropped in v1 | No desktop need. |
| QR scan pairing | Replaced by local auto-connect; v2 pairing code | Loopback needs no pairing. |

## 4. Daemon additions

Each one is small, has Go tests (section 7), and the phone benefits from it.
Paths are in `internal/web` unless stated.

**0. Already merged (#487).** It adds `POST /api/voice/mute {"on":bool}` (through `hub.setMuted(on, "http")`,
event `web.client.mute` with `via:"http"`) and `voice.speaking` in
`GET /api/state`. Everything below builds on its
`handleVoiceMute` shape and on `hub.setMuted`; nothing here adds a second mute
or speaking state.

**1. `POST /api/turn {text}` answers `{reply, turn_id, acted}`.** A typed turn
straight to the orchestrator with a text reply. Today `POST /api/utterance`
with a voice session calls `Voice.SendText` (`api.go:529-537`), so the reply
is spoken to whichever client holds `/ws`, or held if none; without a voice
session it hits the debug router (`api.go:539-546`). The new handler:

- Reuses `s.typed()`, which calls `Supervisor.ShepherdInput(InputTyped)`
  (`api.go:553-557`), and the orchestrator's utterance turn (the `brain.reply_*`
  path the voice's `to_supervisor` uses), so tools, approvals and history work
  as for a spoken turn.
- Tells the voice a silent context line with `voice.push` at
  `urgency: silent`, so the next spoken turn knows what was typed and
  answered. Not read aloud.
- Is wrapped in `idempotent()` (`server.go:419-423`), so a retried tap does
  not run twice.
- Answers `503` with the reason when the orchestrator is off, like
  `handleVoiceMute` does for `--voice=off`.
- Access: `accessDevice`, like the other `/api` routes (loopback is device
  `local`, `auth.go:41,116-121`).

**2. Conversation item for the reply.** The typed user line already carries
`via: "typed"`. The reply is a `kind: "dog"` item with `via: "typed"`, merged
by the rules in `conversation.go`, so `GET /api/conversation` and the SSE
stream show it and the phone renders the desktop exchange. The phone's
timeline is checked against a `dog`/`typed` item (a fixture in the iOS tests;
no iOS code change expected).

**3. `GET /events?kinds=<type-prefix,...>`.** A kind filter so the panel's
stream carries only what it shows. A kind is the first dotted component of
the event `type` (`pending.created` is `pending`, `voice.push` is `voice`);
an unknown kind is 400. Extends `parseFilter` (`sse.go:84`); no filter keeps
today's behaviour. (`GET /api/events` is a JSON tail, not SSE.)

**4. `GET /api/state` `voice.client`.** `{name, platform, since}` of the
`/ws` holder, from `client-ready`'s `client` field and the hub. Lets the
panel say "Voice on iPhone" instead of the draft mod's "Listening" (which
only meant that some client held `/ws`), and decide whether to offer "Use
here". Together with `voice.muted` and `voice.speaking` (#487) it is
the whole voice state. Additive; the iOS decoder ignores unknown keys, which
a fixture test in `ios/` pins.

**5. HTTP voice controls beyond mute.** `POST /api/voice/stop` (cut the
current speech, the same hub path as the `/ws` cancel) and
`POST /api/voice/ptt {"on":bool}` (hold-to-talk: the client opens its mic
while true). Both take #487's body checks and errors. A `/ws`
control message `voice-command` (action `stop` or `ptt`, plus `on`) reaches
the holder. A client that does not know the type ignores it (pinned by a
test in `ios/Tests/SheepdogKitTests`); Sheepdog.app implements it first, the
phone later in its own PR. With no holder the answer is 409 with
"no voice client". The exact message names are fixed in
`ws-contract.md` in that PR.

**6. Mid-turn handoff.** Model audio produced while no client is attached is
dropped (`web.audio_unheard`) while pushes are held. When the voice moves
(`replaced` or detach) mid-turn, the unheard remainder of the turn's
out-transcript becomes a held push, reusing the push-hold path
(`voice.push_held`, `push_released`, five minutes), so the next client says
the rest. The chat item keeps the full text either way. Tests use a fake
session as in `mute_test.go`.

**7. `GET /api/pair/qr.json`.** The pairing QR as a matrix, from
`internal/pair`'s QR code, so the panel draws it with `div`s and no enroll
code is written to disk. Same access as `/api/pair/qr.svg`.

## 5. Sheepdog.app

### 5.1 Targets in `ios/project.yml`

`SheepdogKit` is `type: framework, platform: iOS` and its sources are one
directory, so the Mac needs its own build of it. Plan:

- **`SheepdogKitMac`**: a framework, `platform: macOS`, same
  `Sources/SheepdogKit` path, with `excludes` for what cannot build there.
  One source tree, two products, so the two cannot drift.
- **`SheepdogMac`**: the application, `platform: macOS`, sources
  `Sources/SheepdogMac`, product name `Sheepdog`, bundle id
  `com.github.kccarlos.herdr-sheepdog-mac`, hardened runtime, entitlement
  `com.apple.security.device.audio-input`, `LSUIElement: true`. Not sandboxed
  (it talks to a loopback daemon and posts notifications; a sandbox needs
  extra entitlements for no gain on a Developer ID app).
- **`SheepdogMacTests`**: a unit-test bundle hosted by `SheepdogMac`.
- A shared scheme `SheepdogMac`; the iOS scheme `Sheepdog` is unchanged.
  `ios.yml` gains a macOS build-and-test job. Swift 5 mode and minimal strict
  concurrency carry over (the audio-callback reasons in the file's comment).

**What in SheepdogKit is iOS-only today.** Most Kit files import only
Foundation (plus CryptoKit, Security, Observation, os, CoreGraphics). The
others:

| File | Why | Isolation |
|---|---|---|
| `SwiftTermSurface.swift` (`import UIKit`) | SwiftTerm's UIKit view | Excluded from `SheepdogKitMac`; the app uses the `TerminalSurface` protocol only, and Sheepdog.app has no terminal. |
| `LocalNotice.swift` (`AVAudioSession.Port`, line 193) | audio route names | The port-to-words mapping moves behind a small `AudioRoute` value type the iOS layer fills in; Kit keeps the words. iOS behaviour and tests unchanged. |
| `PCMConverter.swift`, `NetworkMonitor.swift` (AVFoundation, Network) | exist on both platforms | No change; the macOS build checks them. |
| `Sources/SheepdogPush` (PushE2E, content key store) | Notification Service Extension | Not in `SheepdogKitMac`. macOS notifications are local. |
| Demo*, Term*, Spaces, KeyEncoding | phone-only features | Compile on macOS, unused by Sheepdog.app. Not excluded: excluding them buys nothing and risks drift. |

If a further file fails the macOS build, the fix is a protocol or
`#if os(iOS)` at that one seam, never a copy of the file.

**Reused from Kit as is:** `SessionCore` (the voice session's decisions as an
event-to-effects function), `VoiceSocket`, `WSMessage`, `ProtocolHandshake`,
`PlaybackQueue`, `PCMLevel`, `MicSignalWatchdog`, `DaemonClient`,
`DaemonModels`, `SSEParser`, `AppRoute`, `LocalNotice`. For v2: `ComputerFleet`,
`DeviceAuth`, `PinnedTrust`.

**Not reused:** `SessionViewModel` and the SwiftUI views stay in the iOS
app. Sheepdog.app gets its own thin host, `MacVoiceHost`, implementing the
same `VoiceEngineHost` effects (today `SessionViewModel`) over macOS
services. Where Sheepdog.app needs a decision the phone makes inline in
`SessionViewModel`, the decision is first extracted to Kit with the iOS tests
covering it, in its own PR. Nothing moves the other way.

### 5.2 Layout of `Sources/SheepdogMac`

`SheepdogMacApp` (`@main`, no windows), `MacVoiceHost`, `MacAudioEngine`,
`StatusItemController`, and one file per platform service
(`NotificationService`, `HotkeyService`, `LoginItem`, `DaemonLocator`: v1 is
`127.0.0.1` and the configured `web.port`). Each service is a protocol in the
app layer with a fake in `SheepdogMacTests`.

### 5.3 Audio

`MacAudioEngine` is the macOS counterpart of the phone's `AudioEngine.swift`
and implements the protocol `VoiceEngine` already drives.

- `AVAudioEngine` with `inputNode.setVoiceProcessingEnabled(true)` (macOS
  10.15+) for echo cancellation, as on the phone.
- The experiment showed the voice-processing input is six channels at 48 kHz,
  and a test tone read RMS 0 with it on. So the tap reads the mixed-down
  format, not channel 0 of the hardware format, converts with `PCMConverter`
  to what `/ws` expects, and `MicSignalWatchdog` stays on (it already reports
  an all-silent mic). This needs a real microphone to confirm (section 7).
- No `AVAudioSession` on macOS. Route changes (AirPods switching to the
  headset mic) come from `AVAudioEngineConfigurationChange`; the engine
  rebuilds and reports through `LocalNotice`.
- Handshake: `client-ready` with name `sheepdog-mac` and platform `macos`,
  which the daemon now exposes as `voice.client`.
- Permission: `AVCaptureDevice.requestAccess(for: .audio)` on the first
  talk. Denied: the menu and the panel say "Microphone off: open System
  Settings".
- `NSMicrophoneUsageDescription`: "Sheepdog uses the microphone so you can
  talk to your coding agents. What you say goes to the sheepdog daemon on
  your own computer, and from there to the AI voice and orchestrator services
  you set up."

### 5.4 Menu bar, notifications, hotkey, login item

- **Menu bar.** `NSStatusItem`, template image so it follows light and dark.
  States as the Live Activity has them: off, listening, muted, speaking,
  voice elsewhere, needs you (badge). Menu: state line, Talk / Mute, Stop,
  Use here, Open panel (`herdr-app do mod.open`), Launch at login, Quit.
  No window, no Dock icon.
- **Notifications.** `UNUserNotificationCenter`, fed by the SSE `/events`
  stream (question opened, agent finished, voice problem) with Kit's
  classification, so the words match the phone's. Suppressed while the panel
  is visible (herdr-app `notify()` toasts instead); a click opens the panel on
  that item. Local only; no APNs, no relay.
- **Hotkey.** Carbon `RegisterEventHotKey`: no Accessibility or Input
  Monitoring permission, and it reports key-down and key-up. Holding is
  hold-to-talk; a tap under 300 ms toggles mute. Configurable in the menu,
  kept in `UserDefaults` (a property of this Mac).
- **Login item.** `SMAppService.mainApp.register()`, toggled from the menu,
  off by default. No LaunchAgent plist.

### 5.5 Signing, notarization, Homebrew cask

- `release.yml`'s `sign-macos` job (macos-15, `release` environment,
  maintainer approval, `v*` tags) gains steps after the daemon:
  `xcodebuild archive` of `SheepdogMac`; `codesign --options runtime
  --timestamp` with the Developer ID Application identity from the same
  1Password material; `notarytool submit --wait` on a `ditto` zip;
  `stapler staple` (an `.app` can be stapled, unlike the bare daemon); the
  zip's sha256 appended to `checksums.txt` and uploaded. The `policy` check
  already allows macOS only in this job. [../signing.md](../signing.md) gets
  a "Sheepdog.app" section; "No entitlements" stays true for the daemon.
- **Cask** next to the existing formula in the tap: `app "Sheepdog.app"`, a
  minimum macOS version, `zap` for preferences. The formula keeps the daemon;
  an `.app` in a formula is awkward. The release job updates both, and
  `brew install --cask sheepdog` puts the app in `/Applications`, where the
  panel looks.
- The bundle id and team never change; the mic grant is tied to them.

## 6. The panel

### 6.1 Structure

A herdr-app mod, `sheepdog`, plain JS like the draft (`main.js`, `RECIPE.md`),
split into modules so logic is testable under `node`.

- **Rail.** A slim docked or `peek` block: voice state and holder, agents that
  need you, daemon-down and setup states. Click opens the panel.
- **Panel.** A `kind: "window"` overlay with `key` and `keep: true`: floats,
  drags, returns after a restart, needs no personal layout. It holds the
  timeline, the floating question card, suggested replies, the textarea and
  the voice controls.
- **Overlays.** Settings, agent details and the pairing QR, each its own
  overlay so the panel stays small.

**One classification, two languages.** The phone classifies timeline items
in `ConversationTimeline.classify` (Swift); the panel needs the same rules in
JS. To keep them from drifting, the cases live in one fixture file
(`ios/Tests/Fixtures/timeline-classification.json`: item in, category and
style out). `SheepdogKitTests` and the panel's `node --test` both run it, and
a change to either classifier lands with a fixture change in the same PR.

Modules: `net/` (base URL, `http.js`, `sse.js`), `model/` (timeline reducer
and classifier, state store; no herdr imports), `ui/` (views), `voice.js`
(commands, "Use here"). Only `ui/` and `voice.js` touch `herdr`.

### 6.2 Data layer

- **One base URL**, `http://127.0.0.1:PORT`, read once from the mod's
  settings (default 7777). v2 points it at Sheepdog.app's loopback proxy,
  which holds the pinned TLS and ticket for the chosen computer; nothing else
  changes.
- **SSE reader.** `fetch` is buffered (8 MiB, 30 s), so SSE is hand-written
  over `net.connect`: send `GET /events?kinds=<prefix,...>&since=ID` with the
  event type prefixes it shows, **dechunk** the chunked body (verified:
  hex-length frames), parse `id:`,
  `event:`, `data:`, and take the `: keepalive` comment (every 15 s) as the
  liveness signal. No frame for 40 s: reconnect with backoff (1 s doubling to
  15 s) and `Last-Event-ID`. The parser mirrors Kit's `SSEParser` and uses the
  same fixture strings in its tests.
- **Poll fallback.** With the stream down, a 3 s `fetch` of `/api/state` and
  `/api/conversation` keeps the panel usable; the rail says "Live updates
  paused".
- **History.** `GET /api/conversation?limit=&before=` pages back on scroll to
  the top; live items merge by id. Every POST carries an idempotency key.

### 6.3 Rules learned in the experiments

- **Never write inside the mod folder at runtime.** herdr-app reloads a mod
  when its folder changes; a log file there reset the textarea on every key.
  State goes to `localStorage`/`shared` or memory. `fs.writeFile` also throws
  under `herdr-app check`: wrap it. This is why the QR is drawn with `div`s
  from `qr.json` and no file is written.
- **Strip the trailing newline on submit.** A textarea inserts the newline
  and also fires `submit` (with `shift`). On `submit` without shift, send the
  value minus one trailing newline and clear it. Shift+Enter keeps its
  newline. An empty value never sends.
- **IME commit fires `change`, not `submit`.** Measured for one pinyin
  commit; the retest in section 7 confirms the commit-Enter case.
- **Never open `/ws`** (section 2).
- **`list()` cannot be scrolled from code.** `herdr-app check` warns that
  `track_scroll` is ignored on it, and only `v_virtual_list` (rows of
  stated heights) takes a scroll handle. Panel A lists newest first, so the
  list needs no scrolling to show the latest: older pages are added below,
  where they move nothing, and drawing the last row pages back. The rows of a
  `list()` may carry no handlers, so the folded "N agent updates" group, which
  opens on a tap, is not drawn yet (each update is its own line).
- **Events only trigger a refetch.** Panel A does not fold `/events` records
  into items (as the phone's `ConversationHistory.fold` does): the daemon's
  own rebuild stays the only copy of those rules, so a record makes the panel
  ask `/api/state` and `/api/conversation` again, with transcript fragments
  waiting for a quiet 1.5 s.
- Each open copy of a mod has its own state and poll; `key` keeps one window.

### 6.4 States

| State | Seen when | Panel shows |
|---|---|---|
| Daemon down | connect refused, no SSE, no poll | "Sheepdog isn't running", the start command, retry timer. |
| Setup needed | `/api/state` voice `problem` set | The daemon's words and a button to Settings. |
| Sheepdog.app not installed | no app in `/Applications` or `~/Applications` (read-only check) | Voice buttons off; "brew install --cask sheepdog" with copy. Text still works. |
| Sheepdog.app not running | installed, `voice.client` not this Mac | "Talk" runs `open sheepdog://talk`. |
| Voice elsewhere | `voice.client.platform` is another device | "Voice on iPhone" and **Use here**. |
| Muted, speaking, listening | `voice.muted`, `voice.speaking` | Rail dot and label; Stop only while speaking. |
| Mic denied | the app's report | The System Settings fix, in words. |
| Orchestrator off | `POST /api/turn` is 503 | The reason under the textarea; typed text kept. |

### 6.5 Theming and the logo

`theme()` supplies every colour, so dark and light both work. The colour logo
is `logo.svg` with its cream sticker outline, drawn with `image()`, which keeps
colour (`svg()` would tint it to one colour).


## 7. Testing

- **Go (`internal/web`).** A table-driven test file per endpoint, on the
  `fakeSession` harness #487 uses. `/api/turn`: reply returned, `dog`/`typed`
  item in `/api/conversation`, silent push reaches the fake voice, nothing
  spoken, 503 with the orchestrator off, 400 on a bad body, same idempotency
  key runs once. `/events?kinds=`: filtered, unknown kind 400, no filter
  unchanged. `voice.client`: absent, set, cleared, replaced. `stop`/`ptt`:
  frame reaches the holder, 409 with none, 503 voice off, auth as
  `/api/state`. Handoff: replace mid-turn leaves a held push with the unheard
  text, a finished turn none. `qr.json` matrix equals the SVG's. Protocol
  fixtures, `ws-contract.md` and the `architecture.md` event list move with
  each PR.
- **SheepdogKit.** `SheepdogKitTests` stays as is on iOS; the platform-neutral
  subset also runs on macOS through `SheepdogKitMac`. New cases: `AudioRoute`
  words, unknown `/ws` control types ignored, `dog`/`typed` item and
  `voice.client` decoded.
- **`SheepdogMacTests`.** `MacVoiceHost` with fake audio, notification, hotkey
  and login services: events in, effects out (the `SessionCoreTests` style).
  Hotkey hold and tap thresholds on a fake clock. Menu state from a state
  table. `MacAudioEngine` format handling with generated buffers.
- **Panel.** `node --test` on `model/` and `net/sse.js` (chunk boundary inside
  a frame, keepalive timeout, reconnect with `Last-Event-ID`, classification
  from recorded `/api/conversation` fixtures). `herdr-app check` must pass.
  Screenshots of every state in 6.4 and every overlay, dark and light, taken
  from a **private `HERDR_APP_HOME`** against the **fixture daemons** `ios.yml`
  already starts, so no live herdr-app or daemon is touched. They go in
  `docs/dev/design/desktop-client/`, like the other designs.
- **iOS.** The whole suite (`SheepdogKitTests`, the `SheepdogUITests` shards)
  stays green on every PR that touches `ios/`; the PR says so.
- **Needs a real microphone and a human** (not CI; the maintainer, for each
  PR that touches audio): (1) echo cancellation, with the voice speaking
  through the laptop speaker and no barge-in or feedback; (2) the six-channel
  VP format gives real audio, not silence; (3) first-run prompt, and the grant
  kept after an updated, re-signed build; (4) AirPods switching to the headset
  mic mid-session; (5) the IME retest: type pinyin, commit with Enter, read
  the log, then Enter to send; (6) a phone to Mac handoff mid-answer, and back.

## 8. PR plan

Each PR is small, independently green (Go, iOS and, once it exists, macOS),
updates the docs it touches, and is reviewed and merged by Claude.
**codex-ds** = DeepSeek, for well-specified code; **claude-credit** = Sonnet,
for harder pieces.

| # | PR | Who | Acceptance |
|---|---|---|---|
| 0 | This document | Claude | Owner review. |
| 2 | `voice.client`, `/events?kinds=` | codex-ds | Go tests; iOS fixture decode test; protocol fixtures. |
| 3 | `POST /api/turn`, `dog`/`typed` item, silent push | claude-credit | Go tests plus one through the real supervisor; iOS renders the item (fixture). |
| 4 | `voice-command`, `POST /api/voice/stop` and `/ptt` | claude-credit | Go tests; `ws-contract.md`; iOS ignores the type (test). |
| 5 | Mid-turn handoff held push | claude-credit | Handoff tests; no change with no turn running. |
| 6 | `GET /api/pair/qr.json` | codex-ds | Matrix test. |
| 7 | `SheepdogKitMac`, `AudioRoute`, macOS CI job | claude-credit | iOS and macOS build; iOS suite unchanged and green. |
| 8 | Sheepdog.app shell: menu bar, `/events` status, `sheepdog://`, login item | codex-ds | Fake-service tests; `open` launches it and it shows state from a fixture daemon. |
| 9 | `MacAudioEngine`, `MacVoiceHost`, `/ws` session, mic permission | claude-credit | Fake-audio tests; real-mic checks 1 to 3. |
| 10 | Notifications, global hotkey | codex-ds | Fake-service tests; hold and tap thresholds. |
| 11 | Release: archive, sign, notarize, staple, cask | claude-credit | Dry run on the macOS job; `spctl --assess`; `brew audit --cask`; `policy` check. |
| 12 | Panel A: `net/`, `model/`, read-only timeline, rail, states | claude-credit | `node --test`; `herdr-app check`; screenshots; a double reload leaves one window. |
| 13 | Panel B: send, cards, answers, compact mode, suggested replies, agent focus | codex-ds | Fixture tests; screenshots; IME retest. |
| 14 | Panel C: voice controls, "Use here", missing-app states | codex-ds | Command-mapping tests; screenshots; checks 4 to 6. |
| 15 | Panel D: settings, agent details, pairing QR, update notice | codex-ds | Screenshots; the QR scans on a phone. |
| 16 | Docs: `iphone-app.md` pointer, `status.md`, install guide | Claude | Links resolve; steps run on a clean user. |

Order: PRs 2 to 6 run in parallel. 7 gates 8 to 11. 12 needs 2 and
runs beside 7 to 11. 13 needs 3, 14 needs 4, 9 and 11, 15 needs 6. v2 (not
planned here): the loopback proxy and computer switcher, a pairing code.

## 9. Open risks

- **Echo cancellation is untested on a real microphone.** The experiment's
  only input was a virtual device, and VP gave RMS 0 for a loopback tone. If
  VP proves unusable: VP off, "headphones recommended", and a half-duplex gate
  (mic held while `voice.speaking`). Decide after PR 9.
- **herdr-app overlay quirks.** In the experiment `overlay.close --id` did
  not close a `window` overlay and reloads left stale instances. `key` should
  prevent duplicates; PR 12 checks by reloading twice. herdr-app is young: the
  README names the version tested.
- **Homebrew cask.** A notarized `.app` in a cask and Gatekeeper's first run.
  One script updates formula and cask together and the job fails if only one
  moved, so they cannot disagree on versions.
- **Mic grant fragility.** A changed bundle id or team resets the grant;
  ad-hoc development builds are another identity and prompt again.
- **Launching.** `open sheepdog://talk` could be taken by the iOS app if it
  runs on the Mac as "Designed for iPad". If seen, use `open -a Sheepdog` only.
- **Laptop sleep.** A daemon on a sleeping laptop is unreachable from the
  phone: a known v1 limit. Remote agents stay unwatched (the hc-backend
  `isLocal` filter); lifting that is a separate product decision.
- **IME.** Only one commit was measured; the retest may add a guard.
- **Where the panel's code lives.** Proposed: in this repo, under
  `herdr-app/mods/sheepdog/`, so it shares the classification fixtures, the
  protocol fixtures and CI with the daemon and the app, and the `herdr-mod`
  topic goes on this repo. herdr.dev's indexer is not documented for a mod in
  a subfolder of a larger repo; if it needs the mod at a repo's root, a small
  mirror repo is published from this folder by the release job. The draft in
  the separate local repo is folded in by PR 12.
