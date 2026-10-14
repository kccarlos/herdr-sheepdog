<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://github.com/kccarlos/herdr-sheepdog-ios/raw/main/assets/logo-transparent.svg">
    <img src="https://github.com/kccarlos/herdr-sheepdog-ios/raw/main/assets/logo-solid.svg" alt="Sheepdog logo" width="160" height="160">
  </picture>
</p>

# Sheepdog for iOS

A phone front end for the [herdr-sheepdog](https://github.com/kccarlos/herdr-sheepdog)
daemon: talk to **Sheepdog**, the voice, and watch and answer your **agents**, the
coding agents it works with, from anywhere on your own network.

The daemon owns the model session. This app is the microphone, the speaker
and the screen. It speaks the daemon's `/ws` protocol the way the browser
page at `/voice` does (`docs/ws-contract.md` in the daemon repository):
PCM16 mono, 16 kHz up, 24 kHz down, plus a few JSON control frames.

| The conversation | An approval | Agents | Settings ▸ Models |
|---|---|---|---|
| ![Conversation and voice bar](docs/screenshots/review-light-voicebar-live.png) | ![Approval card](docs/screenshots/review-light-conversation-approval.png) | ![Agents pane](docs/screenshots/review-light-flock.png) | ![Models](docs/screenshots/review-light-models.png) |

## Try it without a Mac: the demo

Tap **Try the demo** on the first screen (or in Settings). Scripted agents
run on the phone itself: one finishes, one asks for an approval you can
answer, one fails. The voice reads the news aloud with on-device speech,
and you can tap a suggested question, type one, or ask it aloud where
on-device speech recognition is available. No daemon, no network, no
cloud model. **Leave** returns to the setup. How it works:
[`docs/architecture.md`](docs/architecture.md#the-offline-demo); the App
Review notes are in [`docs/app-review-notes.md`](docs/app-review-notes.md).

## Quick start

1. **Install from TestFlight.** The app is internal-testing only. Ask to be
   added to the TestFlight group **Me**, install **TestFlight** from the App
   Store, and install **"Sheepdog for herdr"** from the invite. ("Sheepdog"
   was taken on the App Store. The home-screen label is still "Sheepdog".)
2. **Make the daemon reachable.** The daemon binds to `127.0.0.1` by
   default, and a phone cannot reach that. On the Mac, set a bind address
   and a token in `~/.config/sheepdog/config.toml`:

   ```toml
   [web]
   bind = "0.0.0.0"      # or your LAN / Tailscale address
   token = "pick-something-long-and-random"
   ```

3. **Connect.** Run `sheepdog pair` on the Mac and tap **Scan pairing
   code** in the app's Settings ▸ Daemon, or point the iPhone's Camera app
   at it and open the `sheepdog://pair` link (the app asks "Pair with
   <host>?" before it changes anything): the code holds every LAN and
   Tailscale address and the token, and the app keeps the first address
   that answers. To type it instead: `sheepdog serve` prints one `lan url`
   line for each non-loopback address. In the app's Settings, enter the host and port from
   that line (for example `192.168.1.20:7777`, with no scheme) and the
   token, then tap **Test connection**. It calls `GET /api/hello` and, when a
   token is set, `GET /api/state`, so a wrong token is reported as a wrong
   token. Tap **Done** (changes apply as you leave the screen), then tap the
   orb to connect.
4. **Allow the microphone and notifications** when iOS asks. The app asks
   for notifications after the first successful connection. The daemon
   needs an APNs key to send them (`docs/push.md` in the daemon repository).

The token is stored in the keychain. There is no TLS (the daemon does not
offer it), so use this only on a network you trust, such as your LAN or
Tailscale.

## What it does

- **Voice.** You hold a conversation with the voice. A voice-activity
  detector sends audio only while you are speaking, echo cancellation stops
  the voice from hearing itself, and playback adapts to bursty audio. Mute
  turns off only the microphone. **Hold to talk** is an option.
- **Approvals.** When a worker stops at a prompt, a card shows who is
  asking, the command and one button per option. You can tap an answer or
  say it. Both go through `POST /api/pending/answer`.
- **Notifications.** When an agent needs you, a time-sensitive APNs
  notification arrives even if the app is closed. You can answer an
  approval from it with **Approve** or **Decline** (both need the
  passcode). Tapping it opens that agent's Agents card.
- **Conversation / Agents.** The conversation includes its history
  from the daemon and is cached on the phone. Agents lists the running
  agents (the others are folded into "Not running (N)") and shows an amber
  count of the agents waiting on you. Tapping an agent in the strip opens
  its window. Health (daemon, voice, orchestrator, recent problems) is behind the
  connection chip and under Settings ▸ Diagnostics. Tasks, questions and replies render markdown.
- **Settings ▸ Models.** Choose the voice provider and the orchestrator's provider
  and model. Changes apply on the daemon at once.
- **Typing.** Use "Type instead" when you cannot talk. ⋯ ▸ **New
  conversation** resets the short-term context without losing the
  scrollback.
- **Reconnects** through daemon restarts and network drops, and resumes the
  `/events` stream from the last event it saw.

What has and has not been checked on a real iPhone:
[`docs/status.md`](docs/status.md).

## Build from source

The project has no third-party dependencies. The Xcode project is generated
and not committed.

```sh
brew install xcodegen          # 2.46 or newer
xcodegen generate
open Sheepdog.xcodeproj
```

`project.yml` signs automatically with team `GV776BZVA8` and bundle ID
`com.github.kccarlos.herdr-sheepdog-ios`. To build under another team,
change `DEVELOPMENT_TEAM` and the bundle IDs there. It requires iOS 17 or
newer.

```sh
xcodebuild -scheme Sheepdog -destination 'platform=iOS Simulator,name=iPhone 17 Pro' build test
```

The `Sheepdog` scheme runs the unit tests and the UI tests. The UI tests
skip themselves unless a fixture daemon is running (see
[`docs/status.md`](docs/status.md#running-the-tests)).

CI and TestFlight run on **Xcode Cloud**, not GitHub Actions. Every pull
request and push to `main` runs the tests, and a `v*` tag sends a build to
TestFlight. See [`docs/xcode-cloud.md`](docs/xcode-cloud.md).

## Layout

- `Sources/SheepdogKit`: everything that can be tested without hardware.
  This covers the `/ws` mirror, the HTTP/SSE client, the voice-activity
  detector, sample-rate conversion, playback scheduling, the session core,
  conversation history, push payloads and markdown.
- `Sources/Sheepdog`: the SwiftUI views, the view models, the audio engine
  and push registration.
- `Tests/SheepdogKitTests`: the unit suite. The TTS speech fixtures live in
  `Tests/SheepdogKitTests/Audio` and are regenerated with
  `scripts/make-fixtures.sh`.
- `Tests/SheepdogUITests`: end-to-end tests against a fixture daemon.

[`docs/architecture.md`](docs/architecture.md) describes how the code
works.
