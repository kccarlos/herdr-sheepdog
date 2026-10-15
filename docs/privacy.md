# Privacy

sheepdog is open-source software that runs on your computer. There is no
sheepdog server, no account and **no telemetry**: nothing is sent to the
project or its author. This page also serves as the privacy policy of the iOS
app "Sheepdog for herdr". Questions: kccarlos.opensource@gmail.com.

## What goes to which provider

| Data | Goes to | Why |
|---|---|---|
| Your microphone audio | The **voice provider** you chose (Google Gemini Live or OpenAI GPT-Live), through the daemon with your own API key | So the voice can hear you |
| What you said (text), agent screens, session records, command output | The **orchestrator provider** you chose (Google Gemini, DeepSeek, or your OpenAI-compatible endpoint) | So it can answer and act |
| The daemon's briefings to the voice (short summaries of agent events) | The voice provider | So the voice can tell you the news |
| A push payload: which agent needs you | Apple's push service (APNs), only if you configured `[push.apns]` | The lock-screen notification |

Each provider handles that data under its own terms; read them, especially for
free-tier keys. A local Ollama orchestrator keeps screens and text on your
machine. Your API keys are used only to call those providers, and are never
logged.

## What is stored on your computer

Everything is in two directories you own; `data_dir` moves the second.

| Path | What it holds | Kept |
|---|---|---|
| `~/.config/sheepdog/config.toml` | Your settings (mode 0600 when it holds `web.token`). | Until you delete it. |
| `~/.config/sheepdog/.env` | Your API keys and the web token (mode 0600). | Until you delete it. |
| `~/.config/sheepdog/prompts/` | Your prompt overrides, if any. | Until you delete it. |
| `~/.local/share/sheepdog/events/YYYY-MM-DD.jsonl` | The event log: every event of the daemon, including **transcripts of what you said**, the voice's and the orchestrator's answers, and agent screen excerpts. | 14 days (`events.retention_days`). |
| `~/.local/share/sheepdog/brain/` | The orchestrator's transcripts and the requests it sent to its model; they include what you said. | `events.retention_days`, deleted with the event log. |
| `~/.local/share/sheepdog/notes.jsonl` | Notes the orchestrator wrote about your agents. | Until you delete it. |
| `~/.local/share/sheepdog/state.json` | The agent table, aliases, pending questions, your runtime settings. | Overwritten as it changes. |
| `~/.local/share/sheepdog/push_devices.json` | The notification tokens of your phones, when push is configured. | Until a phone unregisters. |
| `~/.local/share/sheepdog/logs/sheepdog.log` | The runtime log, with secrets redacted. | Rotated at 10 MB, 3 old files. |
| `~/.local/share/sheepdog/logs/stdio.log` | What a background daemon printed outside its log; normally empty. | Until `sheepdog data clear --all`. |
| `~/.local/share/sheepdog/sheepdog.pid` | The running daemon's pid, URL and version. | While it runs. |

(Source of truth: [CONFIG.md](../CONFIG.md), "What sheepdog stores".)

## Delete your data

| To delete | Run |
|---|---|
| The event log and transcripts now | `sheepdog stop`, then `sheepdog data clear --events` |
| Everything under the data directory (config and keys stay) | `sheepdog stop`, then `sheepdog data clear --all` |
| Everything, including config and keys | `sheepdog uninstall` (asks before each directory) |

All three ask first. Deleting the plugin is `herdr plugin uninstall kccarlos.sheepdog`.

## The iPhone app

The app has no server of its own and collects nothing. It talks to the sheepdog
daemon you point it at (and to Apple for push notifications); no analytics or
tracking code is in it.

| Permission | Why | When asked |
|---|---|---|
| Microphone | To talk to your daemon. Audio goes only to the daemon you point the app at. | When a voice session starts |
| Speech recognition | Demo mode only: your spoken questions are recognised on the phone (on-device); nothing is sent anywhere. | When the demo starts, on devices that support it |
| Camera | Only to scan the pairing QR code that `sheepdog pair` shows. Nothing is recorded or kept. | When you tap Scan pairing code |
| Local network | To reach the daemon on your Wi-Fi or Tailscale. | On the first connection |
| Notifications | "An agent needs you" alerts, sent by your daemon. The app gives your daemon its **push token**, and the daemon keeps it in `push_devices.json`. | After the first connection to a daemon (never in the demo) |

What the app stores on the phone: the daemon's address, your settings and a
cache of the conversation. The daemon token is in the iOS keychain, not synced
to iCloud. The demo runs entirely on the phone, with no network and a throwaway
store. The app uses plain HTTP to your private address because the daemon has
no TLS ([remote-access.md](remote-access.md)). Removing the app deletes its
data; to delete a phone from the daemon, remove its entry in
`push_devices.json` or run `sheepdog data clear --all`.

Contact: kccarlos.opensource@gmail.com
