# iPhone app

**Sheepdog for herdr puts the microphone, the speaker and the lock screen in your pocket.**

**Sheepdog for herdr** is the phone side of sheepdog: the microphone, the
speaker and the screen. The daemon on your computer owns the models and
the agents. The app talks only to your own daemon (and to Apple for
notifications, if you set up push). It needs iOS 26 or newer.

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

1. On the computer: open the setup page (`sheepdog setup`), or run
   `sheepdog pair`. It shows a pairing code. Each code works once, for 10
   minutes.
2. In the app: **Scan the pairing code** on the first screen (later:
   ⋯ ▸ Settings, your computer's row, **Scan a new code**). The app asks
   for the camera first.
3. The app checks the computer's key against the code, makes this phone's
   own key and shows **Paired with <computer>**. Every other paired device
   is told that a new one was added.
4. Allow the microphone and notifications. Tap the orb to connect.
   Notifications reach you even with the app closed: see
   [Notifications](#notifications).

The connection is encrypted, and the app checks the computer's certificate
on every connection. There is no address to type and no password. Settings ▸
Connection shows the address, the date you paired and the start of the
certificate's fingerprint, with **Test connection**, **Scan a new code** and
**Remove** (the computer forgets this phone too).

If the app says **Please scan again**, the phone was removed on the
computer, or the computer's certificate changed (sheepdog reinstalled, or
**Reset pairing**). Show a new code and scan it. If you did neither, do not
continue on that network.

On the same Wi-Fi this just works. Away from home, see
[remote-access.md](remote-access.md) (Tailscale, then pair again).

## What it does

- **The main screen.** At the top, **Conversation | Agents** (the badge
  counts the agents waiting on you), **+** (New agent, New shell) and **⋯**
  (New conversation, Stop, End voice session, Settings).
- **Voice.** The capsule at the bottom holds the microphone, the orb, the
  voice's name and status, and the keyboard. Talk to the voice; the mic
  button turns off only the microphone. While the voice talks or thinks,
  the orb wears a small stop badge: tap it to stop. Tap the name for the
  voice menu: the connection (tap it for the details and Health), Manage
  computers… and **End voice session** (your agents keep working). "Hold to
  talk" is an option. The keyboard turns the capsule into a text field
  when you cannot talk; the orb on its left goes back to voice.
- **Approvals.** A card shows who is asking, the command and one button per
  option. Tap an answer or say it.
- **Agents.** One compact card per agent: its state and for how long, and
  what it works on. Tap a card for the details (the last result, what the
  agent last wrote, the files it touched, the last question it was asked
  and your answer, its space). Every waiting agent shows its question and
  the agent's own answers as buttons, so you can answer any of them, not
  only the one the voice is on. The round button opens the agent's window
  (below). **+ ▸ New agent** takes two steps. **Where:** one of the
  spaces herdr has open (with the agents already in it), or a folder in
  `supervisor.project_roots` that has no space yet. **Which agent:** the
  coding agents installed on your computer (Claude Code, Codex, agy and
  the others herdr knows), **New worktree** (off; it gives the agent its
  own branch and folder, and is hidden when the folder is not a git
  repository), and an optional first prompt. Start waits until the agent
  is ready, then opens its window. If it does not start, the sheet says
  why (not installed, not a git repository, the branch is checked out
  elsewhere, it took too long) and, when a pane was left open, offers
  **Open the pane**.
- **Spaces.** ⋯ ▸ **Spaces** lists herdr's spaces on your computer (a
  folder with its agents and shells), each with its branch and how its
  agents are doing. Swipe one to **Rename** or **Close** it, or tap **+**
  to open a space in a project folder that has none. Close asks first,
  names what will stop, and waits until no agent there is working or
  waiting on you. A space's page lists its agents (tap one for its
  window), its **worktrees** (⋯ opens one as its own space; a worktree
  space can be removed, and a checkout with uncommitted changes asks a
  second time) and its **wrap-up commands**, such as "Run tests ·
  `npm test`": tap one to run it in a new tab of that space on your
  computer, swipe to delete, or **Add a command…**. Commands are saved on
  your computer and need the phone shell to be on.
- **Agent window.** "Open window" on an agent shows its live terminal,
  sized to the phone, in the agent's own colours. While the window is open
  the agent's pane on your computer takes the phone's size; it goes back
  when you leave the window or the app goes to the background.
  - **Pinch** to change the text size (⋯ ▸ Reset text size undoes it).
    **Drag** to scroll back through the output, and **Latest** returns to
    the bottom. A **tap** clicks, for programs that use the mouse.
    **Long-press** selects text to copy; ⋯ ▸ Copy screen copies all of it.
  - The keys under the terminal send esc, tab, the arrows and return
    straight to the agent. Control-C is at the top of **···** (More keys),
    under Interrupt, and goes at once; esc already stops Claude and Codex.
    In a shell window control-C sits on the bar. **⌨︎** types straight into
    the terminal with the phone's keyboard, dictation and input methods
    included, with **⌃**, which holds Control for the next key. **···** has
    the rest: Ctrl and Alt for the next key,
    shift-tab, the arrows, Home, End, Page up and down, y, n, the digits,
    F1–F12, and your **snippets** (saved text: a tap puts it in the field,
    or types it in typing mode; swipe to delete).
  - The field below sends a prompt, the same as saying it (up to 64 KB, so
    a pasted log arrives whole). With the field empty while the agent
    works, its button is **Stop**, which ends the current turn. When the
    agent asks for approval, the approval card is there too.
  - ⋯ ▸ **Rename**, **Stop current turn** and **Close agent** (it asks,
    naming the agent; the session ends and its pane closes on your
    computer). These, and New agent, need an up-to-date sheepdog.
  - **Another device has control** means another phone or iPad has this
    window open. You can watch; Take over (it asks first) moves control to
    this phone.
  - ⋯ ▸ Switch agent opens another agent's window in place.
  - **📎 Attach** (beside the prompt field) sends a photo, a camera shot,
    a file or a copied image to your computer, up to 20 MB (images, text
    and PDF). Its path on the computer goes into the prompt, and the agent
    reads it from there. The file stays on your computer and is deleted
    after 7 days ([privacy.md](privacy.md)).
  - **⋯ ▸ Changes** lists the files the agent changed in its folder, with
    lines added and removed; tap one for its diff. It only shows changes
    and never edits anything. "No repository" means the agent's folder is
    not a git repository.
  - **⋯ ▸ Open shell here** opens a plain shell in the agent's folder,
    titled "Shell · folder". It has the same terminal and keys, no prompt
    field, and starts in typing mode. **+ ▸ New shell** opens one
    in your first project folder. Each folder has one shell, which the app
    reuses. On your computer the shell is a tab in herdr's "phone shell"
    workspace, so you see it there too. Leaving the window keeps the shell
    running, and ⋯ ▸ **Close shell** ends it (it asks first).
    - The shell is on unless **Shell from your phone** is turned off on
      your computer's setup page
      ([remote-access.md](remote-access.md#the-phone-shell)).
    - Where it is off, + ▸ New shell is greyed out, and Settings says
      why. The shell opens only in your project folders
      (`supervisor.project_roots`) or an agent's own folder.
  - The live window needs herdr 0.9.2 or newer and an up-to-date sheepdog
    on your computer. Otherwise the window is a snapshot of the screen that
    refreshes every few seconds (in colour with an up-to-date sheepdog), and
    it says so. "herdr is not running"
    means sheepdog cannot reach herdr: start herdr on your computer, then
    tap Try again.
- **Cards.** Ask for a list or the details ("show me the failing tests",
  "show me what auth is asking") while the app is open on the conversation,
  and the orchestrator puts them on your screen: a card with a table, a
  list, an agent's screen or a way into its window. It opens once by
  itself and stays in the conversation, so a tap opens it again. A card
  only shows things; you still answer approvals on the approval card. With
  the phone locked, the app in the background, or another screen open
  (Settings, the Agents pane, an agent window), the voice says it instead.
  Needs a daemon new enough to send cards; the demo's "Show me the agents"
  shows one.
- **Settings** (⋯ ▸ Settings) has three parts. **Computer:** your computer
  with how the phone reaches it (tap it for the connection), and what
  belongs to that computer: Models, Languages, and the shell's state.
  **This phone:** Voice (microphone mode, hold to talk, keep listening
  with the screen locked) and Notifications. **Help:** Diagnostics and
  About. **Try the demo** is at the bottom.
- **Settings, Models.** Switch the voice and the orchestrator
  ([models.md](models.md)). **Settings, Languages** sets the languages you
  speak, most preferred first.
- **Update notice.** When the daemon knows of a newer sheepdog, the top of
  Settings says so in one line, with the command to run on your computer
  (for example "sheepdog v0.1.5 is available. To update, run this on your
  computer: `brew upgrade sheepdog`"). The phone cannot update the daemon;
  once the update is installed, the daemon restarts into it when no one is
  using it, or tells you to run `sheepdog restart`
  ([quick-start.md](quick-start.md#update)). There is no line when the
  daemon is up to date, has `update.check` off, or is too old to report
  updates.
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
| Camera refuses to scan | Allow Camera for Sheepdog in iOS Settings (the app has an **Open Settings** button). |
| This code has expired or was used | Show a new code on the computer and scan it. |
| Offline, or Please scan again | [remote-access.md](remote-access.md). |
| No sound from the voice | Check the daemon: `sheepdog status` shows "NOT connected" with the reason ([troubleshooting.md](troubleshooting.md)). |
| Local network prompt dismissed | iOS Settings, Sheepdog, Local Network on. |
| App talks but agents are empty | herdr is not running or not reachable: `sheepdog doctor`. |

Everything the app collects is in [privacy.md](privacy.md).
