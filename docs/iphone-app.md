# iPhone app

**Sheepdog for herdr puts the microphone, the speaker and the lock screen in your pocket.**

**Sheepdog for herdr** is the phone side of sheepdog: the microphone, the
speaker and the screen. The daemon on your computer owns the models and
the agents. The app talks only to your own daemon (and to Apple for
notifications, if you set up push). It needs iOS 17 or newer.

## Install

- **TestFlight:** [join the beta](https://testflight.apple.com/join/bjCaFYgK). Install the TestFlight app first, then open the link. The App Store name is "Sheepdog for herdr"; the home-screen label is "Sheepdog".
- **Build it yourself:** the source is in [`ios/`](../ios/README.md) in this
  repository, with the steps (XcodeGen, Xcode, your own Apple team).

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
3. Allow the microphone. Tap the orb to connect. (Notifications with the app
   closed need your own push key: see [Notifications](#notifications).)

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

Apple only delivers a notification sent with the push key of the team that
publishes the app, so there are three ways to set this up (`push.mode` in
[CONFIG.md](../CONFIG.md)):

1. **The shared relay (the default, nothing to set up).** The daemon sends
   each notification through sheepdog's push relay, which holds the App
   Store app's key. The content is end-to-end encrypted: the daemon gives
   your phone its own key when the phone registers, and encrypts every
   notification with it. The relay sees your phone's push token, when a
   notification is sent, its size and its kind (approval, blocked, done),
   never the agent, the text or the options
   ([privacy.md](privacy.md), "Phone notifications").
2. **Your own key, with an app you build yourself.** Build the app under
   your own Apple team, create an APNs key, put it in `[push.apns]` and the
   daemon talks to Apple directly; no relay is involved. With all four
   `[push.apns]` values set and `push.mode` unset, this is what the daemon
   uses.
3. **Your own relay.** Deploy `relay/` to your own Cloudflare account with
   your own Apple key and point `push.relay.url` at it
   ([relay/README.md](../relay/README.md)). This keeps a self-built app's
   key off your computer, or serves several computers from one key.

`sheepdog doctor`'s `push` line shows the mode and whether the relay
answers. `push.mode = "off"` turns notifications off. Without them the app
still works while it is open and connected, with the Live Activity and the
earbud remote, but nothing arrives while it is closed. The architecture is
in [dev/push.md](dev/push.md).

## Troubleshooting

| Symptom | Fix |
|---|---|
| Camera refuses to scan | Allow Camera in iOS Settings, or type the host and token. |
| Offline or Token refused | [remote-access.md](remote-access.md). |
| No sound from the voice | Check the daemon: `sheepdog status` shows "NOT connected" with the reason ([troubleshooting.md](troubleshooting.md)). |
| Local network prompt dismissed | iOS Settings, Sheepdog, Local Network on. |
| App talks but agents are empty | herdr is not running or not reachable: `sheepdog doctor`. |

Everything the app collects is in [privacy.md](privacy.md).
