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
- **A silence gate**, the same one the browser page uses (peak below 8 LSB
  for 300 ms closes it, 100 ms of pre-roll when it reopens), so a quiet room
  does not degrade the model session.
- **The flock**, from `/api/state` and the `/events` stream: one chip per
  agent with a state dot, the pending question as its subtitle, and a
  highlight when it is blocked. Tap one for its task, its last result and
  the question it is waiting on.
- **The conversation**, built from the daemon's transcript fragments the way
  `/voice` builds it, with the daemon's pushes as system cards and a haptic
  when one needs a decision.
- **A presence orb** that says what is happening — listening, quiet,
  thinking, speaking, muted, stalled, reconnecting — with VoiceOver labels
  for every state. It never pretends: against a daemon started with
  `--voice=off` it says so.
- **Reconnects** with backoff after a drop. If another client takes the
  session (the daemon serves one at a time) the app says so and waits for
  you rather than fighting for it.
- **Background audio**, optional: a toggle in Settings keeps the session
  alive with the screen locked.

## What v1 does not do

- No text input: this is a voice client. Use `/debug` on the Mac to type.
- No answering a worker's approval prompt directly from the app — that is
  the dog's job, under its own rails (`docs/roles.md`).
- No settings beyond the daemon address, the token and the lock-screen
  toggle. No chattiness control, no model picker, no event log viewer.
- No push notifications: the app has to be open (or holding the audio
  session) to hear a push.
- Nothing has run on a real device yet — see [`docs/status.md`](docs/status.md).

## Layout

- `Sources/SheepdogKit` — everything testable without hardware: the wsproto
  mirror, the HTTP/SSE client, the WebSocket, the silence gate, the sample
  rate conversion, the playback scheduling, the conversation reducer, the
  orb's state machine, the keychain wrapper.
- `Sources/Sheepdog` — SwiftUI views, `@MainActor` view models, the audio
  engine.
- `Tests/SheepdogKitTests` — the unit suite (runs in CI).
- `Tests/SheepdogUITests` — an end-to-end walkthrough against a real daemon;
  see `docs/status.md` for how to run it.

[`docs/architecture.md`](docs/architecture.md) has the data flow and the
audio numbers.
