# privacy

**What stays on your computer, what leaves it, and what the project never collects.**

sheepdog is open-source software that runs on your computer. There is no
account and **no telemetry**. The project runs one small service: an
optional **push relay** that forwards phone notifications to Apple (see
"Phone notifications" below). It cannot read them, and nothing else is
sent to the project or its author. This page also serves as the privacy policy of the iOS
app "Sheepdog for herdr". Questions: kccarlos.opensource@gmail.com.

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
| `~/.local/share/sheepdog/attachments/` | Photos and files you attach in the iPhone app. They stay on your computer; the agent gets only their path in your prompt. | 7 days. |
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

## The iPhone app

The app has no server of its own and collects nothing. It talks to the sheepdog
daemon you point it at (and to Apple for push notifications, which reach it
through the push relay above unless you chose otherwise); no analytics or
tracking code is in it.

| Permission | Why | When asked |
|---|---|---|
| Microphone | To talk to your daemon. Audio goes only to the daemon you point the app at. | When a voice session starts |
| Speech recognition | Demo mode only: your spoken questions are recognised on the phone (on-device); nothing is sent anywhere. | When the demo starts, on devices that support it |
| Camera | Only to scan the pairing QR code that `sheepdog pair` shows. Nothing is recorded or kept. | When you tap Scan pairing code |
| Local network | To reach the daemon on your Wi-Fi or Tailscale. | On the first connection |
| Notifications | "An agent needs you" alerts, sent by your daemon. The app gives your daemon its **push token** and receives the key that encrypts the alerts' content; the daemon keeps both in `push_devices.json`. | After the first connection to a daemon (never in the demo) |

The live agent window and the phone shell send your keystrokes to the daemon
and show the pane's screen. The daemon passes them through without storing
them: its event log records only that a window or shell opened and closed
(which pane, which folder, how long, how many bytes), never what was typed or
shown.

What the app stores on the phone: the daemon's address, your settings and a
cache of the conversation. Each paired computer's entry (its id, addresses
and certificate fingerprint) is in the iOS keychain, not synced to iCloud,
and the phone's key for it is in the Secure Enclave: it cannot be copied
off the phone and is not in backups. The demo runs entirely on the phone, with no network and a throwaway
store. The app uses plain HTTP to your private address because the daemon has
no TLS ([remote-access.md](remote-access.md)). Removing the app deletes its
data; to delete a phone from the daemon, remove its entry in
`push_devices.json` or run `sheepdog data clear --all`.

Contact: kccarlos.opensource@gmail.com
