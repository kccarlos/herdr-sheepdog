<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://github.com/kccarlos/herdr-sheepdog-ios/raw/main/assets/logo-transparent.svg">
    <img src="https://github.com/kccarlos/herdr-sheepdog-ios/raw/main/assets/logo-solid.svg" alt="Sheepdog logo" width="160" height="160">
  </picture>
</p>

# Sheepdog for iOS

A phone-sized front end for the [herdr-sheepdog](https://github.com/kccarlos/herdr-sheepdog)
daemon: talk to **Sheepdog** — the dog — and watch the **flock** of coding
agents it is herding, from wherever you are on your own network.

The daemon owns the model session; this app is the microphone, the speaker
and the screen. It speaks the daemon's `/ws` protocol exactly as the browser
page at `/voice` does (`docs/ws-contract.md`): PCM16 mono, 16 kHz up,
24 kHz down, a handful of JSON control frames.

| First launch | The flock and the conversation | One agent |
|---|---|---|
| ![Settings on first launch](docs/screenshots/01-first-launch-settings.png) | ![Agents and conversation](docs/screenshots/04-main-agents-conversation.png) | ![Agent sheet](docs/screenshots/05-agent-sheet.png) |

| History, across a restart | An approval | Answered by tap |
|---|---|---|
| ![History with a restart divider](docs/screenshots/10-history-restart-divider.png) | ![Approval card](docs/screenshots/11-approval-card.png) | ![Answered approval](docs/screenshots/12-approval-answered.png) |

| The voice bar | The connection chip |
|---|---|
| ![Mute, orb, End](docs/screenshots/16-voicebar-connected-voice-off.png) | ![Connection chip popover](docs/screenshots/17-voicebar-chip-popover.png) |

| Just talk | Muted | Voice and microphone | Voice detection |
|---|---|---|---|
| ![Coach mark](docs/screenshots/19-coach-mark.png) | ![Muted](docs/screenshots/21-mic-muted.png) | ![Settings: voice provider and microphone](docs/screenshots/22-settings-voice-microphone.png) | ![Settings: voice detection](docs/screenshots/23-settings-voice-detection.png) |

## Build it

No third-party dependencies. The Xcode project is generated, not committed.

```sh
brew install xcodegen          # 2.46 or newer
xcodegen generate
open Sheepdog.xcodeproj
```

Then in Xcode: select the **Sheepdog** target ▸ Signing & Capabilities ▸ set
your Team, pick your iPhone, and run. The bundle identifier
`com.kccarlos.sheepdog.app` is in `project.yml`; change it if it clashes with
something you already have.

Requires iOS 17 or newer. Built and tested with Xcode 27 against the iOS 26
simulator.

From the command line:

```sh
xcodebuild -scheme Sheepdog -destination 'platform=iOS Simulator,name=iPhone 17 Pro' build test
```

## Point it at your daemon

The daemon binds to `127.0.0.1` by default, which a phone cannot reach. On
the Mac running it, set a non-loopback bind and a token
(`~/.config/sheepdog/config.toml`):

```toml
[web]
bind = "0.0.0.0"      # or your LAN / Tailscale address
token = "pick-something-long-and-random"
```

`sheepdog serve` then prints a `lan url` line per non-loopback address. Take
the host and port from the right one and type them into the app's Settings —
`192.168.8.141:7777`, no scheme needed — with the same token. **Test
connection** calls the daemon's public `GET /api/hello` and, when a token is
configured, `GET /api/state` as well, so a wrong token is named as such
instead of failing later at the WebSocket.

The token is kept in the keychain. There is no TLS in v1 (the daemon's own
non-goal), so keep this to a network you trust — your LAN, or Tailscale.

## What v1 does

- **Voice.** Hold a conversation with the dog: the microphone at the
  hardware rate is converted to 16 kHz PCM16 and sent in 40 ms frames; what
  the dog says comes back at 24 kHz and is played gaplessly. Echo
  cancellation is on (`.playAndRecord` / `.voiceChat`), so the dog does not
  hear itself. Calls, Siri and AirPods going in or out are handled.
- **A voice-activity detector**, not a silence gate: audio goes to the
  daemon only while your voice is clearly above the room's noise floor
  (speech band, adaptive floor and margin, 120 ms attack, 400 ms hangover,
  380 ms of pre-roll so the first syllable is kept). Room noise, fans, other
  voices and keyboards stay home instead of becoming words. Tested against
  macOS text-to-speech in five kinds of noise — numbers in
  [`docs/architecture.md`](docs/architecture.md#voice-detection-what-gets-sent).
  Settings ▸ Voice detection shows the live level, noise floor, gate state
  and spurious opens, with the browser's old peak gate there to compare.
- **Just talk.** The status line says "Listening — just talk"; the mic
  button shows your level and only mutes ("Muted — tap to unmute"); a
  first-run coach mark says so. **Hold to talk** is an option in Settings.
- **Microphone mode.** Settings shows Standard / Voice Isolation / Wide
  Spectrum and opens iOS's panel; in a noisy room, choose Voice Isolation.
- **No microphone signal** is said, not silently gated: 3 s of digital zero
  while unmuted and the status line tells you, and capture restarts once.
- **Voice provider.** Against a daemon that offers more than one (Gemini
  Live, GPT-Live), Settings ▸ Voice switches between them; GPT-Live is
  marked as billed per minute.
- **The flock**, from `/api/state` and the `/events` stream: one chip per
  agent with a state dot, the pending question as its subtitle, and a
  highlight when it is blocked. Tap one for its task, its last result and
  the question it is waiting on.
- **The conversation, with its history.** The app opens on the last
  conversation at once — it keeps the newest 300 messages per daemon in a
  small file on the phone (no token in it) — then loads the daemon's own
  record from `GET /api/conversation` and merges the two by event id, the
  daemon's copy winning. Restarts and voice reconnects show as quiet
  dividers ("Sheepdog restarted · 6:52 PM"), days get a date divider, and
  pulling down at the top (or tapping *Earlier conversation*) loads the page
  before. Live, it is built from the daemon's transcript fragments the way
  `/voice` builds it, with pushes as system cards and a haptic when one
  needs a decision. *Settings ▸ Clear conversation on this phone* forgets the
  local copy.
- **Approve by tap.** When a worker stops at an approval prompt, a card
  pinned above the controls shows who is asking, the summary in plain words,
  the command, and one button per option — the prompt's default prominent,
  the "No…" option in red. A tap answers through
  `POST /api/pending/answer`, the same rail as a spoken answer; the card
  shows the daemon's reply ("You approved — Yes, proceed · by tap"), or
  "That question was already answered or has gone." if the voice or the Mac
  got there first. The conversation keeps each approval and how it ended.
  VoiceOver reads the buttons as "Approve: Yes, proceed" / "Decline: No, …".
- **Nothing missed across drops.** The `/events` stream resumes from the
  last event id (`Last-Event-ID`, and `?since=` as the fallback), across
  reconnects and relaunches; replayed events change nothing twice.
- **A compact voice bar** — Mute, the orb, End (✕) — 72 pt at the bottom,
  with a small status line above it, so the conversation and the approval
  card get the rest of the screen. The orb says what is happening —
  listening, quiet, thinking, speaking, muted, stalled, reconnecting — with
  VoiceOver labels for every state; it never pretends, saying so against a
  daemon started with `--voice=off`. Its tap does the one thing that makes
  sense right now: Connect when disconnected, Stop while the dog is
  thinking or speaking, nothing otherwise. Mute and End sit in fixed slots
  so a hidden or disabled control never shifts the orb.
- **A connection chip**, top left: a dot and one word for the daemon
  connection itself — the `/ws` socket, the `/events` stream, and the token
  — never the voice state, so it does not read grey while happily connected
  and quiet. Green "Connected", amber "Reconnecting…", red "Offline" or
  "Token refused", grey "Not connected". Tapping it opens the full address,
  a timed round trip, the daemon's version and voice name, the supervisor's
  model (`brain` on `/api/state`, when the daemon reports one), and
  Reconnect / Open Settings.
- **Reconnects** with backoff after a drop. If another client takes the
  session (the daemon serves one at a time) the app says so and waits for
  you rather than fighting for it.
- **Background audio**, optional: a toggle in Settings keeps the session
  alive with the screen locked.
- **Mute is only the microphone.** "Mute stops your microphone; Sheepdog can
  still talk." — the button's VoiceOver hint, and a one-time caption the
  first time it is used.

## What v1 does not do

- No text input: this is a voice client. Use `/debug` on the Mac to type.
- No chattiness control, no supervisor-model picker, no event log viewer.
- No push notifications: the app has to be open (or holding the audio
  session) to hear a push.
- Nothing has run on a real device yet — see [`docs/status.md`](docs/status.md).

## Layout

- `Sources/SheepdogKit` — everything testable without hardware: the wsproto
  mirror, the HTTP/SSE client, the WebSocket, the voice-activity detector
  and the capture gate, the microphone-signal watchdog, the sample
  rate conversion, the playback scheduling, the conversation reducer, the
  orb's state machine, the keychain wrapper.
- `Sources/Sheepdog` — SwiftUI views, `@MainActor` view models, the audio
  engine.
- `Tests/SheepdogKitTests` — the unit suite (runs in CI), with the TTS
  speech fixtures in `Tests/SheepdogKitTests/Audio` (regenerate with
  `scripts/make-fixtures.sh`).
- `Tests/SheepdogUITests` — an end-to-end walkthrough against a real daemon;
  see `docs/status.md` for how to run it.

[`docs/architecture.md`](docs/architecture.md) has the data flow and the
audio numbers.
