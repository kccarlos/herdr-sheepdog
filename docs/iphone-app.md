# iPhone app

**Sheepdog for herdr** is the phone side of sheepdog: the microphone, the
speaker and the screen. The daemon on your computer owns the models and
the agents. The app talks only to your own daemon (and to Apple for
notifications). It needs iOS 17 or newer.

## Install

<!-- TESTFLIGHT_LINK: when the public link exists, restore the bullet: "**TestFlight:** [join](https://testflight.apple.com/join/...). Install the TestFlight app first, then open the link." -->
- **TestFlight:** the public link is coming; until it exists, build the app yourself (next bullet). The App Store name is "Sheepdog for herdr"; the home-screen label is "Sheepdog".
- **Build it yourself:** the source and steps are in
  [herdr-sheepdog-ios](https://github.com/kccarlos/herdr-sheepdog-ios)
  (XcodeGen, Xcode, your own Apple team).

## Try it without a computer: the demo

Tap **Try the demo** on the first screen. Scripted agents run on the phone: one
finishes, one fails, one asks for an approval you can answer. The voice reads
the news aloud with on-device speech and you can tap a suggested question,
type one, or ask it aloud. No daemon, no key, no network. **Leave** returns to
setup.

## Pair

1. On the computer: `sheepdog setup`, step 4 (**Turn on phone access**), or
   later `sheepdog pair`.
2. In the app: Settings, Daemon, **Scan pairing code**. (Or point the iPhone
   Camera at the code and open the `sheepdog://pair` link; the app asks
   "Pair with <host>?" first.)
3. Allow the microphone and, when asked, notifications. Tap the orb to connect.

Prefer typing: `sheepdog serve` prints a `lan url` line; enter its host and
port (for example `<lan-address>:7777`, no scheme) and the token in Settings,
then **Test connection**.

On the same Wi-Fi this just works. Away from home, see
[remote-access.md](remote-access.md) (Tailscale, then pair again).

## What it does

- **Voice.** Talk to the voice; mute turns off only the microphone. "Hold to
  talk" is an option. "Type instead" works when you cannot talk.
- **Approvals.** A card shows who is asking, the command and one button per
  option. Tap an answer or say it.
- **Agents.** Who is working, blocked or done, and a window onto each agent's
  screen.
- **Settings, Models.** Switch the voice and the orchestrator
  ([models.md](models.md)). **Settings, Languages** sets the languages you
  speak, most preferred first.
- **Live Activity.** While a voice session runs, the lock screen and Dynamic
  Island show it, with buttons.
- **Earbud remote.** Play/pause on earbuds or AirPods mutes and unmutes the
  microphone; next track stops what is in flight.
- **Siri, Shortcuts, Action Button.** "Talk to Sheepdog" starts a session.

## Notifications

When an agent needs you, the daemon sends a time-sensitive notification through
Apple's push service, even if the app is closed. **Approve** and **Decline**
work from it (both need your passcode); tapping it opens that agent.

This needs an Apple push key configured on the daemon (`[push.apns]`, see
[dev/push.md](dev/push.md)), and Apple only accepts a push key from the team
that owns the app. The daemon's `sheepdog doctor` line `push` says
`push not configured (optional…)` when it is off. Without push, the app still
works while it is open and connected, with the Live Activity and the earbud
remote, but nothing arrives while it is closed. If you build the app under
your own Apple team, configure your own key.

## Troubleshooting

| Symptom | Fix |
|---|---|
| Camera refuses to scan | Allow Camera in iOS Settings, or type the host and token. |
| Offline or Token refused | [remote-access.md](remote-access.md). |
| No sound from the voice | Check the daemon: `sheepdog status` shows "NOT connected" with the reason ([troubleshooting.md](troubleshooting.md)). |
| Local network prompt dismissed | iOS Settings, Sheepdog, Local Network on. |
| App talks but agents are empty | herdr is not running or not reachable: `sheepdog doctor`. |

Everything the app collects is in [privacy.md](privacy.md).
