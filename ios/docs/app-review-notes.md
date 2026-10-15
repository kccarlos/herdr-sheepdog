# App Review notes

Draft text for App Store Connect ▸ App Review Information ▸ Notes, plus the
facts behind it. Nothing has been submitted for App Store review or
external TestFlight review yet. The CEO decides when to submit. The text
for TestFlight (beta description, What to Test, Beta App Review notes) is
in [dev/testflight-metadata.md](dev/testflight-metadata.md).

## Notes for the reviewer (paste into App Store Connect)

> Sheepdog for herdr is a voice front end for **sheepdog**, an open-source
> daemon that people run on their own computer to supervise coding agents
> (Claude Code, Codex and others) in the herdr terminal multiplexer. For
> real use the app connects to that self-hosted daemon over the user's own
> local network or Tailscale. No account exists and no sign-in is needed.
>
> **To try the app without a daemon, use the built-in demo:**
> 1. Launch the app. The first screen is **Welcome**. It explains that the
>    app needs a self-hosted sheepdog daemon.
> 2. Tap the blue **Try the demo** button (also in Settings ▸ Try the demo).
> 3. The demo runs entirely on the device, with no network and no server.
>    Four scripted agents work on a sample project: claude-auth finishes,
>    claude-migration fails, codex-payments asks for approval to run a
>    command (about 15 seconds in), and codex-docs finishes later. The voice reads each announcement aloud with on-device
>    speech synthesis.
> 4. Open **Agents** to see the agent list. Tap **Yes, proceed** or **No**
>    on codex-payments' approval card. Tap **Open window** to see an agent's
>    (scripted) terminal.
> 5. Tap a suggested question under "Demo" ("What's going on?", "Why did
>    the migration fail?"), type one with the keyboard button, or, where the device
>    supports on-device speech recognition, ask it aloud. The answers are
>    scripted.
> 6. Tap **Leave** to return to the normal setup.
>
> Everything in the demo is labelled "Demo". It calls no cloud model and
> sends nothing anywhere. When the demo starts, iOS may ask for speech
> recognition and the microphone. Both are optional; you can tap or type
> the questions instead.
>
> Real use needs the sheepdog daemon running on the user's own computer,
> which a reviewer does not have. Without it, the app shows how to set it
> up and offers the demo. Push notifications are sent by the user's own
> daemon only when it is configured with this app team's APNs key, so the
> app asks for notification permission only when the connected daemon can
> send them. The demo never asks.

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
- Optional notifications: a daemon configured with this app team's APNs
  key sends them when an agent needs the user. A self-hosted daemon
  without that key cannot, and the app then does not ask for permission
  and says so in Settings ▸ Notifications.

## Permissions and why

| Permission | Why | When it is asked |
|---|---|---|
| Microphone (`NSMicrophoneUsageDescription`) | To talk to the user's own daemon (real use); in the demo, to hear spoken questions on the device | When a voice session starts, or when the demo starts on a device with on-device speech recognition |
| Speech recognition (`NSSpeechRecognitionUsageDescription`) | Demo only: spoken questions are recognised **on the device** (`requiresOnDeviceRecognition`); nothing is sent anywhere | When the demo starts, only on devices that support on-device recognition |
| Camera (`NSCameraUsageDescription`) | Only to scan the pairing QR code that `sheepdog setup` or `sheepdog pair` shows on the user's computer; nothing is recorded | When the user taps Scan pairing code |
| Local network (`NSLocalNetworkUsageDescription`) | The daemon runs on the user's own computer on the same Wi-Fi (or over Tailscale) | On the first connection |
| Notifications | "An agent needs you" alerts from the user's daemon | After the first successful connection, and only when that daemon reports it can send pushes (never in the demo) |

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

- Real use needs a self-hosted sheepdog daemon on a Mac or Linux computer
  running herdr. The demo shows the full flow without one.
- Sign-in: none. No demo account is needed.

## Before submitting (CEO)

- **Privacy-policy URL:**
  https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/privacy.md
  (the daemon repository's privacy page, which names this app). It
  resolves once that repository is public.
- **TODO (CEO): App Review contact** (name, phone, e-mail) in App Store
  Connect.
- Screenshots for the store page can come from the demo (it is labelled
  "Demo" on screen).
- The App Privacy questionnaire: "Data Not Collected" matches the app
  itself; the CEO confirms.
