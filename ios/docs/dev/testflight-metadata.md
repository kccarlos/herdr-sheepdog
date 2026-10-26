# TestFlight metadata (draft)

The text for App Store Connect ▸ TestFlight ▸ Test Information, for public
(external) testing. Nothing here has been submitted; another step fills
the form. Each quoted block is ready to paste.

## Beta App Description

Limit: 4,000 characters. This draft is about 1,500.

> Sheepdog: Voice for AI Agents is the iPhone app for sheepdog, an open-source tool
> that lets you manage the coding agents (Claude Code, Codex and others)
> running in the herdr terminal on your computer by voice.
>
> Talk to Sheepdog to hear what your agents did, answer their approval
> prompts by tap or by voice, and look at each agent's screen when you are
> away from your desk. A Live Activity shows the voice session on the Lock
> Screen and in the Dynamic Island.
>
> The app has no server and no account. It talks only to the sheepdog
> daemon that you run on your own Mac or Linux computer, over your Wi-Fi or
> Tailscale. Install the daemon from the herdr plugin marketplace, run
> "sheepdog setup", and scan the pairing code it shows.
>
> No computer at hand? Tap "Try the demo" on the first screen. Scripted
> agents run on the phone: one finishes, one fails and one asks for your
> approval. The voice reads the news aloud, and you can ask it questions.
> The demo needs no daemon, no account and no network.
>
> What to know for this beta:
> - Notifications while the app is closed come through a shared relay,
>   end-to-end encrypted: the relay sees when a notification is sent and
>   whether it is an approval, never what it says. Your daemon uses it by
>   default; no account is needed.
> - The connection to your daemon uses TLS, pinned to your computer's own
>   certificate from the pairing code. Use it on your home Wi-Fi or
>   Tailscale.
>
> Source code, setup and privacy: https://github.com/kccarlos/herdr-sheepdog

## What to Test (this build)

Limit: 4,000 characters.

> Thanks for testing! Please try:
> 1. First launch: does the Welcome screen make clear what you need? Tap
>    "Try the demo" and answer the approval card for codex-payments
>    (about 15 seconds in). Ask a suggested question.
> 2. If you have sheepdog: pair by scanning the code from "sheepdog setup"
>    (step 4) or "sheepdog pair", then tap the orb and talk.
> 3. Errors: with the daemon stopped, or with the phone on another network,
>    does the app tell you what to do?
> 4. Larger text sizes, dark mode and smaller iPhones.
>
> Send feedback with a screenshot from TestFlight, or e-mail
> kccarlos.opensource@gmail.com.

## Feedback e-mail

kccarlos.opensource@gmail.com

## Marketing URL

https://github.com/kccarlos/herdr-sheepdog

## Privacy Policy URL

https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/privacy.md

This URL resolves once the daemon repository is public. The page names this
app and says it collects nothing.

## Beta App Review information

- **Sign-in required:** No. There are no accounts.
- **Contact:** to be filled in App Store Connect by the CEO. It is not kept
  in this repository.
- **Review notes:**

> No sign-in is needed. The app connects to a sheepdog daemon that each
> user runs on their own computer (open source:
> https://github.com/kccarlos/herdr-sheepdog), which a reviewer will not
> have. To see the app working, tap "Try the demo" on the first screen.
> The demo runs entirely on the device, with no network. Four scripted
> agents work on a sample project. About 15 seconds in, codex-payments asks
> for approval to run a command: tap "Yes, proceed" on its card. Open
> "Agents" to see the list, tap "Open window" for an agent's (scripted)
> terminal, and tap a suggested question such as "What's going on?" to
> hear a spoken answer. "Leave" returns to setup.
>
> Permissions: Microphone, to talk to your own daemon, or to ask
> questions aloud in the demo. Speech recognition, demo only, on the
> device. Camera, only to scan the daemon's pairing code. Local network,
> to reach the daemon on your Wi-Fi. Notifications are requested only when
> the connected daemon can send them, never in the demo. All are optional;
> the demo works with each one declined.

Longer background for App Review is in
[../app-review-notes.md](../app-review-notes.md).
