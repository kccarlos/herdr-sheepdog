# App Review notes

The text for App Store Connect ▸ App Review Information ▸ Notes, and the facts
behind it. It comes from the App Store review audit (section 4)
and is checked against the code. Nothing has been submitted for App Store
review or external TestFlight review yet; the CEO decides when. The app
name is **TBD — pending CEO decision**. The text for TestFlight is in
[dev/testflight-metadata.md](dev/testflight-metadata.md); the privacy policy
is [docs/privacy.md](../../docs/privacy.md).

Before pasting, fill `<VIDEO LINK>`. The AI-services consent screen and its
Settings ▸ Privacy switch are planned and not in the app yet: paste the
"AI SERVICES AND CONSENT" paragraph only once they ship (until then say that
the user's computer sends the voice to the AI services the user configured,
and that removing the computer stops it). The contact name, phone and email
go in App Store Connect, not in this repository.

> **Stale in parts (2026-10-25).** The App Store audit in
> [../../docs/dev/app-store-review.md](../../docs/dev/app-store-review.md) supersedes these notes
> (ATS, AI consent, privacy manifest). Its section 4 replaces them when the docs fix lands.

## Notes for the reviewer (paste into App Store Connect)

Limit: 4,000 characters.

> No sign-in is needed. The app has no accounts.
>
> WHAT IT IS. The app (name: TBD — pending CEO decision) is a voice remote for sheepdog, a free open-source program that people
> run on their own Mac or Linux computer to supervise coding agents (Claude Code, Codex and
> others) in the herdr terminal: https://github.com/kccarlos/herdr-sheepdog. The app talks only
> to that program on the user's own computer. We run no server for it, except an optional relay
> that forwards end-to-end encrypted notifications to APNs.
>
> HOW TO REVIEW WITHOUT A COMPUTER. Tap "Try the demo" on the first screen. The demo runs
> entirely on the device, with no network. Four scripted agents work on a sample project. After
> about 15 seconds, codex-payments asks for approval: tap "Yes, proceed". Open Agents for the
> list, tap an agent and "Open window" to see its (scripted) terminal, and tap a suggested
> question such as "What's going on?" to hear a spoken answer. iOS may ask for the microphone
> and speech recognition (on-device only). Both are optional; tap or type instead. "Leave"
> returns to setup. A recording of real use with a computer: <VIDEO LINK>.
>
> AI SERVICES AND CONSENT. In real use, the user's own computer sends the voice and the
> conversation to the AI services the user configured there with their own API keys (Google
> Gemini or OpenAI for the voice; Google Gemini, DeepSeek, Anthropic, an OpenAI-compatible
> service or a local model for answers). Before the first real connection, the app shows a
> consent screen that names these services. Nothing is sent until the user taps Agree, and
> Settings > Privacy withdraws consent (once that screen ships; until then removing the computer stops all sharing). The demo uses no AI service and sends nothing.
>
> THE SHELL AND AGENT WINDOWS. An agent window shows the terminal of a coding agent on the
> user's own computer. "New shell" opens a terminal there. The app sends keystrokes and draws the
> text, like an SSH client. Nothing is downloaded or run on the iPhone, and the app's features
> do not change. The user can turn the shell off on their computer.
>
> NETWORK SECURITY (ATS). NSAllowsArbitraryLoads is on because the app connects to the user's
> own computer at a private IP address (home Wi-Fi or Tailscale), whose certificate is
> self-signed. ATS cannot pin a self-signed certificate on an IP address, so the app checks the
> certificate itself against the fingerprint in the pairing QR code, and rejects any other
> certificate. The app refuses plain HTTP to any address except this device (127.0.0.1, used in
> development). It never loads web content.
>
> BACKGROUND AUDIO. A voice session is live two-way audio with the user's computer, like a
> call. The audio mode keeps it going with the screen locked, if the user leaves "Keep listening
> with the screen locked" on in Settings. The microphone indicator shows while it listens, and
> Stop or the Live Activity ends it.
>
> NOTIFICATIONS AND LIVE ACTIVITY. The app asks for notification permission only after it
> connects to a computer that can send notifications, never in the demo. Notifications are
> encrypted end to end with a key only the phone and the user's computer hold. "Time sensitive"
> is used only when an agent is waiting for the user's approval. The Live Activity shows the
> voice session and the agents waiting. The user's computer updates it, and it ends with the
> session.
>
> PERMISSIONS. Microphone: the voice session (demo: spoken questions). Speech recognition: demo
> only, on device. Camera: scanning the pairing code, or a photo the user attaches for an agent.
> Local network: reaching the user's computer. All are optional.

## Network security, plainly

- The app connects to the user's computer over TLS and accepts only the
  certificate whose fingerprint is in the pairing code. It refuses plain HTTP
  to every address except loopback (development). It never loads web content.
- `NSAllowsArbitraryLoads` is on because ATS cannot express a self-signed
  certificate on a private IP address (exception domains cannot name IP
  addresses, and ATS runs its own trust check even after the pin is accepted).
  `NSAllowsLocalNetworking` is deliberately unset: when it is present, iOS
  ignores `NSAllowsArbitraryLoads`. See `ios/project.yml`.
- If Apple asks for the ATS justification: "connects only to the user's own
  self-hosted server at a private IP address, over TLS pinned to that
  server's self-signed certificate".
- Export compliance: `ITSAppUsesNonExemptEncryption = false` (only what iOS
  provides).

## Before submitting (CEO)

- Privacy-policy URL:
  https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/privacy.md
  (resolves once the repository is public).
- App Review contact (name, phone, e-mail) in App Store Connect.
- App Privacy label: "Data Not Collected" is the recommended answer
  (audit, section 5); the CEO confirms.
