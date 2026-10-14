# App Review notes

Draft text for App Store Connect ▸ App Review Information ▸ Notes, plus the
facts behind it. Nothing has been submitted for App Store review or
external TestFlight review yet. The CEO decides when to submit.

## Notes for the reviewer (paste into App Store Connect)

> Sheepdog for herdr is a voice front end for **sheepdog**, an open-source
> daemon that people run on their own Mac to supervise coding agents
> (Claude Code, Codex and others) in the herdr terminal multiplexer. For
> real use the app connects to that self-hosted daemon over the user's own
> local network or Tailscale. No account exists and no sign-in is needed.
>
> **To try the app without a daemon, use the built-in demo:**
> 1. Launch the app. The first screen is "Daemon" setup.
> 2. Tap **Try the demo** (also in Settings ▸ Try the demo).
> 3. The demo runs entirely on the device, with no network and no server.
>    Four scripted agents work on a sample project. One finishes, one asks
>    for approval to run a command (about 15 seconds in), one fails, and one
>    finishes later. The voice reads each announcement aloud with on-device
>    speech synthesis.
> 4. Open **Agents** to see the agent list. Tap **Yes, proceed** or **No**
>    on billing's approval card. Tap **Open window** to see an agent's
>    (scripted) terminal.
> 5. Tap a suggested question under "Demo" ("What's going on?", "What
>    failed?"), type one with the keyboard button, or, where the device
>    supports on-device speech recognition, ask it aloud. The answers are
>    scripted.
> 6. Tap **Leave** to return to the normal setup.
>
> Everything in the demo is labelled "Demo". It calls no cloud model and
> sends nothing anywhere.

## What the app does (real use)

- Talks to the user's own sheepdog daemon at an address the user gives
  (typed, or from the daemon's pairing QR code). The daemon is free,
  open-source software the user runs themselves:
  https://github.com/kccarlos/herdr-sheepdog
- Streams the user's voice to that daemon and plays back its spoken
  replies. The daemon (not the app) talks to the voice model the user
  configured on it (Google Gemini Live or OpenAI GPT-Live, with the user's
  own API key).
- Shows the agents the daemon supervises, their approval prompts, and each
  agent's terminal. Lets the user answer prompts by tap or voice.
- Optional notifications: the daemon sends them through APNs when an agent
  needs the user.

## Permissions and why

| Permission | Why | When it is asked |
|---|---|---|
| Microphone (`NSMicrophoneUsageDescription`) | To talk to the user's own daemon (real use), and to hear spoken questions in the demo | When a voice session starts |
| Speech recognition (`NSSpeechRecognitionUsageDescription`) | Demo only: spoken questions are recognised **on the device** (`requiresOnDeviceRecognition`); nothing is sent anywhere | When the demo starts, only on devices that support on-device recognition |
| Camera (`NSCameraUsageDescription`) | Only to scan the pairing QR code that `sheepdog pair` shows on the Mac; nothing is recorded | When the user taps Scan pairing code |
| Local network (`NSLocalNetworkUsageDescription`) | The daemon runs on the user's Mac on the same LAN (or over Tailscale) | On the first connection |
| Notifications | "An agent needs you" alerts from the user's daemon | After the first successful connection to a daemon (never in the demo) |

The app uses plain HTTP to a private address by design (the daemon has no
TLS), which is why `NSAllowsArbitraryLoads` is set. Encryption export
compliance: `ITSAppUsesNonExemptEncryption = false` (only what iOS
provides).

## Data

- The app has no server of its own and collects nothing. Audio, text and
  agent state go only between the phone and the user's own daemon.
- The daemon token is kept in the keychain.
- The demo stores nothing: its settings live in a separate, throwaway
  defaults suite and memory, and its conversation in a temporary directory.

## Requirements to say plainly

- Real use needs a self-hosted sheepdog daemon on a Mac (or Linux machine)
  running herdr. The demo shows the full flow without one.
- Sign-in: none. No demo account is needed.

## Before submitting (CEO)

- **TODO (CEO): privacy-policy URL.** App Store Connect requires one. None
  exists yet; do not invent one. It should say what "Data" above says.
- **TODO (CEO): App Review contact** (name, phone, e-mail) in App Store
  Connect.
- Screenshots for the store page can come from the demo (it is labelled
  "Demo" on screen).
- The App Privacy questionnaire: "Data Not Collected" matches the app
  itself; the CEO confirms.
