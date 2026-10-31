# Sheepdog on your Mac (herdr-app)

**The same conversation and voice, beside your agents.**

The herdr-app panel shows Sheepdog's conversation in the sidebar. Sheepdog.app
is a small menu-bar app that gives the panel the microphone, the speaker,
notifications and a global shortcut. The sheepdog daemon still does the work,
and the phone can use the same daemon.

## What you get

### The panel

The Sheepdog block in herdr-app's sidebar contains:

- **The conversation**, newest first. It has what you and the voice said, what
  your agents did and the questions waiting for you. Drawing the last row loads
  older messages.
- **A box to type to Sheepdog.** Enter sends; Shift+Enter adds a line. The
  reply is text and becomes part of the conversation on the phone too.
- **Questions with one button per answer.** Tap an option to answer the agent.
  Long questions shrink to an **Answer** bar that opens the options.
- **Voice controls:** **Talk**, **Mute**, **Stop** and **Hold to talk**. The
  buttons control whichever device holds the voice, including your phone.
- **Settings** for models, languages, reply style, paired devices and the
  update notice. **Pair a phone** shows a QR code and link. **New conversation**
  clears Sheepdog's working memory after you confirm.
- **Agent details** when you tap an agent's name, and cards the orchestrator
  shows. A card can bring its agent forward in herdr.

In a narrow sidebar the panel becomes a small rail with the Sheepdog logo, a
voice dot and a count of agents that need you. Click the rail to open the full
panel as a window.

### Sheepdog.app

Sheepdog.app has no window and no Dock icon. Its menu-bar icon shows the voice
state and how many agents need you. From its menu you can:

- **Talk, mute and stop** the voice, and use the microphone and speaker on this
  Mac.
- **Use here** to move the voice from your phone or a browser to this Mac.
- **Hold Option+Space to talk.** A brief tap of Option+Space mutes or unmutes.
  You can choose Control+Space or Control+Option+Space instead.
- **Open the panel**, choose **Launch at login**, or quit the app. Launch at
  login is off until you turn it on.
- Receive **notifications** when an agent asks a question, finishes or reports
  a voice problem. Click one to open the Sheepdog panel.

## Requirements

- **herdr-app on herdr-core.** The desktop client does not use the old herdr
  interface.
- **The sheepdog daemon on the same Mac as herdr-app**, at its default loopback
  address. The desktop client is not for a daemon on another computer.
- **macOS 14 Sonoma or newer.**
- **Apple silicon** for Sheepdog.app. The panel has no separate architecture
  requirement; use whatever herdr-app supports.

You can use the panel without a browser. A browser is still useful for
`sheepdog setup`, but it is not needed to talk once Sheepdog.app is installed.

## Install

### 1. Install sheepdog

Install the daemon from [the quick start](quick-start.md), then run
`sheepdog setup` and finish its checks. If you use herdr-core, follow the
Homebrew path in [Other ways to install](quick-start.md#other-ways-to-install).

### 2. Choose herdr-core if both herdr versions are running

`herdr.backend` defaults to `auto`. If both old herdr and herdr-core answer,
`auto` picks old herdr. To make Sheepdog use herdr-core, put this in
`~/.config/sheepdog/config.toml`:

```toml
[herdr]
backend = "hc"
```

Then run `sheepdog restart`. The key and its environment override are in
[CONFIG.md](../CONFIG.md).

### 3. Install the panel

Run `sheepdog setup` after herdr-app is installed. It installs or updates the
panel for you.

If you need to install it by hand from a checkout of this repository:

```sh
HERDR_APP_MODS="${HERDR_APP_HOME:-$HOME/.config/herdr-app}/mods"
mkdir -p "$HERDR_APP_MODS"
cp -R mods/sheepdog "$HERDR_APP_MODS/"
herdr-app check "$HERDR_APP_MODS/sheepdog"
```

On a layout you own, herdr-app docks Sheepdog on the right the first time it
sees the folder. A layout that ships with herdr-app cannot be changed by a
mod. If the panel opens as a window instead, run:

```sh
herdr-app do mod.open --name sheepdog
```

Choose **Copy my layout**. That makes a layout you own and switches
herdr-app to it, but it does not add the Sheepdog block. Open the copied
`layouts/<name>.json` file in herdr-app's folder and add a block for the mod
`sheepdog` on the right side. Reload or restart herdr-app. The
[panel README](../mods/sheepdog/README.md) has herdr-app's current
docking details.

### 4. Install Sheepdog.app

```sh
brew install --cask kccarlos/tap/sheepdog-app
```

Sheepdog.app is available from the first sheepdog release that ships it;
older releases do not have the cask. Open **Sheepdog** from Applications. The
microphone prompt appears the first time you press **Talk** or hold
Option+Space.

The panel only needs the daemon for typing and reading the conversation. It
offers the voice controls when it finds Sheepdog.app in `/Applications` or
`~/Applications`.

## Move between Mac and phone

There is one voice at a time. Starting the voice on your phone takes it from
the Mac. The panel then says **Voice on iPhone** and offers **Use here**. Start
the voice on the Mac from **Use here** in the panel, **Use here** in the
menu-bar app, or **Talk**.

The conversation does not move between separate copies: both clients use the
same daemon. A line you type in the panel appears with the same reply on the
phone. You can keep typing and answering questions in the panel while the phone
holds the voice; **Mute**, **Stop** and **Hold to talk** are sent to the phone.

Read [the iPhone app guide](iphone-app.md) for pairing, notifications and using
the phone away from your desk.

## Troubleshooting

| You see | Fix |
|---|---|
| **Sheepdog isn't running** in the panel | Start the daemon with `sheepdog start`. If it will not start, run `sheepdog doctor` and follow its fix. |
| **Voice on iPhone**, or another device has the voice | That device took the voice. Choose **Use here** in the panel or the menu-bar app to bring it back. |
| The voice controls are off and the panel says Sheepdog.app is not installed | Install it with `brew install --cask kccarlos/tap/sheepdog-app`, then reopen Applications. The panel notices it within 30 seconds. |
| **Open panel** says the Sheepdog panel isn't installed in herdr-app | Install the panel (step 3 above), then choose **Open panel** again. |
| The microphone prompt was denied, or the menu says **Microphone off** | Open System Settings, Privacy & Security, Microphone, and turn on Sheepdog. The app's menu also has **Open Microphone Settings…** after a denial. |
| There are no notifications | Open System Settings, Notifications, Sheepdog, and allow notifications. Sheepdog.app posts them locally; you do not need phone push for them. |
| The panel cannot find the daemon, or Sheepdog.app talks to the wrong port | Point both clients at the same port, below. |

### A daemon on another port

The default is `http://127.0.0.1:7777`. The daemon's port is `web.port` in
[CONFIG.md](../CONFIG.md).

For the panel, set `url` in the layout block's `props`, or the shared key
`sheepdog.url`, to the plain HTTP address, for example
`http://127.0.0.1:7801`. The panel's event stream does not support HTTPS.

For Sheepdog.app, quit it, then change its port:

```sh
defaults write com.github.kccarlos.herdr-sheepdog-mac sheepdogDaemonPort -int 7801
```

Open Sheepdog.app again. Its other address default is the
`sheepdogDaemonHost` key, which is `127.0.0.1` unless changed. The desktop
client is for a daemon on this Mac, so leave the host at loopback.

## Privacy

The panel and Sheepdog.app talk only to the sheepdog daemon on your Mac over
loopback. The panel never opens the microphone; Sheepdog.app sends microphone
audio to your daemon, which uses the same AI voice and orchestrator services
you configured for the phone. Mac notifications stay on the Mac.

See [what stays on your computer and what leaves it](privacy.md), including
[the Mac client](privacy.md#the-mac-client).
