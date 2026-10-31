# privacy

**What stays on your computer, what leaves it, and what the project never collects.**

sheepdog is open-source software that runs on your computer, and the Sheepdog
iPhone app is its remote control. **The developer of the app collects nothing.**
There is no account, no analytics, no advertising and no tracking, in the app
or in the daemon. The project runs one small service: an optional **push
relay** that forwards phone notifications to Apple (see "Phone notifications").
It cannot read them. Nothing else is sent to the project or its author.
This page is the privacy policy of the Sheepdog iPhone and iPad app and of the
sheepdog daemon. Questions: kccarlos.opensource@gmail.com.

**In short.** Your voice, what you say, your agents' screens and the photos you
attach go from the app to *your own* computer, over TLS. Your computer passes
your voice, words and screens to the AI services you set up there with your own
API keys, and the coding agent you send a photo to passes it to its own AI
service. The developer never sees them.

## What goes to which provider

| Data | Goes to | Why |
|---|---|---|
| Your microphone audio | The **voice provider** you chose (Google Gemini Live or OpenAI GPT-Live), through the daemon with your own API key | So the voice can hear you |
| What you said (text), agent screens, session records, command output | The **orchestrator provider** you chose (Google Gemini, DeepSeek, Anthropic, or your OpenAI-compatible endpoint) | So it can answer and act |
| The daemon's briefings to the voice (short summaries of agent events) | The voice provider | So the voice can tell you the news |
| A phone notification: which agent needs you and what it asks | Encrypted, through sheepdog's push relay to Apple's push service (APNs); or straight to APNs with your own key (`push.mode = "apns"`) | The lock-screen notification |

Each provider handles that data under its own terms; read them, especially for
free-tier keys. A local Ollama orchestrator keeps screens and text on your
machine.

With the **Anthropic** orchestrator, each call sends Anthropic the
orchestrator's instructions, its tool list, the recent conversation (your
words as text, what the assistant said, agent events) and the screen excerpts,
session records and command output it read, plus your API key in the
`x-api-key` header. When Anthropic's safety classifiers decline a request,
Anthropic's server-side fallback (on by default, `anthropic.fallbacks`) sends
the same request to another Claude model inside Anthropic. Nothing else goes
to Anthropic: the voice audio never does. Your API keys are used only to call those providers, and are never
logged.

## AI services

The app itself talks to no AI service. In real use, the sheepdog daemon on
your computer does, with the API keys you put in its configuration. **The app
asks you first.** Before the first real voice session or typed line, a screen
names the services your computer uses (read from the daemon, with a generic list
when it has not said), says that the developer of sheepdog never sees your voice, text, photos or
screens and that the one service we run, a push relay, forwards notifications it
cannot read (what it does see is under "Phone notifications" below), and
has **Agree** and **Learn more** (this page). Nothing is sent until you tap
Agree. The answer is kept on the phone and asked again if the text changes in
a way that matters. The demo uses no AI service and never asks.

| Service | What it receives | Chosen by |
|---|---|---|
| The **voice service**: Google Gemini Live or OpenAI GPT-Live | Your microphone audio, and short briefings about agent events | You, in the daemon's config |
| The **orchestrator**: DeepSeek, Google Gemini, Anthropic, an OpenAI-compatible service, or a local model (Ollama) | What you said as text, the agents' screens, session records and command output the orchestrator reads | You, in the daemon's config |

A photo you attach in the app goes to your computer and is stored there (see
"What is stored on your computer"); the coding agent you send it to receives
the file path, reads the photo and passes it to its own AI service. The demo uses no AI service and sends nothing.

These services act under your own account and your own terms with them; the
developer of sheepdog is not a party and cannot see the data. Choose providers
whose terms you accept; a local model keeps the text on your machine. Each
provider is expected to give the data the protection its own policy
promises: read it, especially for free-tier keys.

## Withdraw your consent

- **Stop the AI sharing:** disconnect the app (Settings, remove the computer)
  or stop the daemon (`sheepdog stop`); nothing is sent while either is off.
  To cut a provider off, remove its key from `~/.config/sheepdog/.env`.
- **Remove the phone:** delete the computer in the app's Settings (this
  deletes its keys from the keychain and the Secure Enclave), or delete the
  app. Delete the phone from the daemon with the device list on the setup page
  or `sheepdog data clear --all`.
- **Withdraw inside the app:** Settings, Privacy, turn off **Send to AI
  services**. A running voice session ends, and while the switch is off the app
  starts no voice session and sends no typed line. Turning it back on, or Agree
  on the screen the app then shows, is the way back.

## The daily update check

Once a day (and shortly after the daemon starts, if the last check is older
than a day) the daemon sends one request to `api.github.com` asking for the
latest release of sheepdog, to tell you when a new version exists. The
request carries nothing about you, your agents or your setup beyond what any
web request carries (your IP address, and a generic user agent). The answer
is cached in `~/.local/share/sheepdog/update-check.json`. Turn it off with
`update.check = false` ([CONFIG.md](../CONFIG.md)); `sheepdog update` makes
the same request when you run it.

## What is stored on your computer

Everything is in two directories you own; `data_dir` moves the second.

| Path | What it holds | Kept |
|---|---|---|
| `~/.config/sheepdog/config.toml` | Your settings (mode 0600). | Until you delete it. |
| `~/.config/sheepdog/.env` | Your API keys (mode 0600). | Until you delete it. |
| `~/.config/sheepdog/prompts/` | Your prompt overrides, if any. | Until you delete it. |
| `~/.local/share/sheepdog/events/YYYY-MM-DD.jsonl` | The event log: every event of the daemon, including **transcripts of what you said**, the voice's and the orchestrator's answers, and agent screen excerpts. | 14 days (`events.retention_days`). |
| `~/.local/share/sheepdog/brain/` | The orchestrator's transcripts and the requests it sent to its model; they include what you said. | `events.retention_days`, deleted with the event log. |
| `~/.local/share/sheepdog/notes.jsonl` | Notes the orchestrator wrote about your agents. | Until you delete it. |
| `~/.local/share/sheepdog/state.json` | The agent table, aliases, pending questions, your runtime settings. | Overwritten as it changes. |
| `~/.local/share/sheepdog/attachments/` | Photos and files you attach in the iPhone app. They are stored on your computer; the agent gets their path in your prompt and reads the file. | 7 days. |
| `~/.local/share/sheepdog/devices.json` | Your paired phones and browsers: their names, kinds, public keys and when each was last seen. No secret. | Until you delete it or revoke a device on the setup page. |
| `~/.local/share/sheepdog/tls/` | This computer's id and its own TLS certificate and key. | Until you delete it. |
| `~/.local/share/sheepdog/push_devices.json` | Your phones' notification tokens and each phone's notification encryption key. | Until a phone unregisters. |
| `~/.local/share/sheepdog/logs/sheepdog.log` | The runtime log, with secrets redacted. | Rotated at 10 MB, 3 old files. |
| `~/.local/share/sheepdog/logs/stdio.log` | What a background daemon printed outside its log; normally empty. | Until `sheepdog data clear --all`. |
| `~/.local/share/sheepdog/sheepdog.pid` | The running daemon's pid, URL and version. | While it runs. |
| `~/.local/share/sheepdog/update-check.json`, `update-notice.json`, `update-restart.json` | The last answer of the daily update check, when a command last printed the update notice, and the last automatic restart after an upgrade. No personal data. | Until you delete them. |

(Source of truth: [CONFIG.md](../CONFIG.md), "What sheepdog stores".)

## Phone notifications

Apple delivers a notification only when it is sent with the Apple key of
the team that published the app, and that key cannot be shared. So by
default (`push.mode = "relay"`) your daemon sends notifications through
sheepdog's push relay, a small stateless service on Cloudflare
([relay/README.md](../relay/README.md)) that holds the key and passes
each notification on to Apple.

Before a notification leaves your computer, the daemon encrypts its
content (the agent's name, the sentence, the question's options) with a
key that only your phone has: the daemon makes a fresh 32-byte key for
each phone when it registers and hands it over on the authenticated
connection to your daemon. The phone decrypts it.

| The relay sees | The relay never sees |
|---|---|
| Your phone's push token (sealed into a relay token the daemon keeps), your daemon's IP address, when a notification is sent, its size, and its kind (approval, blocked, done, idle). For the lock-screen Live Activity while the app is away: its push token, that the voice is not listening, and how many agents are waiting, working and done | The title, the agent's name, the text, the options, a waiting agent's question, your screens or anything you said |

The relay keeps no database and logs only how many requests it answered
and with which status. Apple sees what any push service sees: the token,
the timing and the encrypted payload.

You can avoid the shared relay: run your own copy of it on your own
Cloudflare account, or build the app under your own Apple team and set
`push.mode = "apns"` so the daemon talks to Apple directly
([iphone-app.md](iphone-app.md), "Notifications"). `push.mode = "off"`
sends nothing.

## Delete your data

| To delete | Run |
|---|---|
| The event log and transcripts now | `sheepdog stop`, then `sheepdog data clear --events` |
| Everything under the data directory (config and keys stay) | `sheepdog stop`, then `sheepdog data clear --all` |
| Everything, including config and keys | `sheepdog uninstall` (asks before each directory) |

All three ask first. Deleting the plugin is `herdr plugin uninstall kccarlos.sheepdog`.

## The Mac client

The herdr-app panel and Sheepdog.app are local clients of the daemon on the
same Mac. They use its loopback address and do not connect to another
computer. The panel reads the conversation and sends the text and answers you
choose; it never opens the microphone. Sheepdog.app sends your microphone
audio to the daemon and plays the voice's audio from it, so the same rows in
"What goes to which provider" apply. Its notifications are made locally by
macOS; they do not use Apple's push service or the sheepdog relay.

## The iPhone app

The app has no server of its own and collects nothing; the one service the project runs is the push relay. It talks to the
sheepdog daemon you pair it with, and to Apple for push notifications (these
reach it through the push relay above unless you chose otherwise). No
analytics or tracking code is in it, and it loads no web content.

**The connection.** Pairing scans a code from `sheepdog pair` that holds your
computer's address and the fingerprint of its self-signed TLS certificate. The
app then connects over TLS and accepts only a certificate with that
fingerprint, and it signs in with a key held in the phone's Secure Enclave
([security.md](dev/design/security.md)). It refuses plain HTTP to any
address except the phone itself (used in development). Because iOS cannot
check a pinned self-signed certificate on a home or Tailscale IP address, the
app turns off iOS's own certificate check (`NSAllowsArbitraryLoads`) and
relies on that pin.

| Permission | Why | When asked |
|---|---|---|
| Microphone | Your voice goes to your daemon, which passes it to the voice service you chose ("AI services"). | When a voice session starts |
| Speech recognition | Demo only: your spoken questions are recognised on the phone, on-device; nothing is sent anywhere. | When the demo starts, on devices that support it |
| Camera | To scan the pairing QR code, and to take a photo you attach for an agent. A photo goes to the sheepdog daemon on your own computer, and the coding agent you send it to passes it to its own AI service. Nothing is recorded in the background. | When you tap Scan pairing code or the camera in the attach menu |
| Photos and files | You pick a photo or file to attach through the system picker; the app sees only what you pick, and has no access to your library. It goes to your computer, and the coding agent you send it to passes it to its own AI service. | When you tap attach |
| Local network | To reach the daemon on your Wi-Fi or Tailscale. | On the first connection |
| Notifications | "An agent needs you" alerts, sent by your daemon. The app gives your daemon its **push token** and receives the key that encrypts the alerts' content; the daemon keeps both in `push_devices.json`. | Once, after the app connects to a daemon that can send notifications (never in the demo, never for a daemon that cannot send them) |

**Live Activity.** While a voice session runs, the lock screen and Dynamic
Island show whether the voice is listening and how many agents are waiting,
working and done. Your daemon updates it, through the push relay when the
app is away (the relay sees the counts and the Live Activity's push token, as
the table in "Phone notifications" says). It ends with the session.

The live agent window and the phone shell send your keystrokes to the daemon
and show the pane's screen. The daemon passes them through without storing
them: its event log records only that a window or shell opened and closed
(which pane, which folder, how long, how many bytes), never what was typed or
shown.

**What the app stores on the phone:** the daemon's address, your settings and a
cache of the conversation. Each paired computer's entry (its id, addresses
and certificate fingerprint) is in the iOS keychain, not synced to iCloud,
and the phone's key for it is in the Secure Enclave: it cannot be copied
off the phone and is not in backups. The demo runs entirely on the phone, with
no network and a throwaway store. Removing the app deletes its data. To
delete a phone from the daemon, revoke it on the setup page, remove its entry
in `push_devices.json`, or run `sheepdog data clear --all`.

Contact: kccarlos.opensource@gmail.com
