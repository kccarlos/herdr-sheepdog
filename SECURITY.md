# Security policy

## Reporting a vulnerability

Email **kccarlos.opensource@gmail.com** with a description, the steps to reproduce and the version (`sheepdog version`, or the iPhone app's Settings > About). Please do not open a public issue for a vulnerability.

Do not include real API keys or tokens in a report. `sheepdog doctor --report` redacts them.

## What sheepdog is, for threat-modelling

- It is a local daemon you run yourself. It listens on `127.0.0.1` by default and refuses a non-loopback address without a token.
- It has **no TLS**. Use your LAN or Tailscale; never expose the port to the open internet ([docs/remote-access.md](docs/remote-access.md)).
- Anyone with the web token and network access can read agent screens and answer their prompts. The one rail in code: an approval is answered only on the user's own words in that turn.
- API keys live in `~/.config/sheepdog/.env` (mode 0600) and are redacted from logs and reports.

### The iPhone app

- The app talks only to the sheepdog daemon at the address you give it (typed, or from the daemon's pairing code), and, for notifications, to Apple directly or through the push relay, which forwards end-to-end encrypted payloads it cannot read. It has no server of its own.
- The daemon's token is the only credential. The app keeps it in the keychain (available only when the phone is unlocked). Answering an approval from a notification needs the passcode.
- The connection is **plain HTTP and WebSocket without TLS**, because the daemon offers none. Use it on a trusted LAN or Tailscale only. This is why the app sets `NSAllowsArbitraryLoads`.
- A `sheepdog://pair` link changes nothing until you confirm "Pair with <host>?".

In scope: the daemon, the CLI, the herdr plugin scripts, the web pages, the push relay and the iPhone app in this repository.

## Supported versions

The latest release only (for the iPhone app, the latest TestFlight build).
