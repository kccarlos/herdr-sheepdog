# Security policy

## Reporting a vulnerability

Email **kccarlos.opensource@gmail.com** with a description, the steps to reproduce and the version (`sheepdog version`, or the iPhone app's Settings > About). Please do not open a public issue for a vulnerability.

Do not include real API keys or tokens in a report. `sheepdog doctor --report` redacts them.

## What sheepdog is, for threat-modelling

- It is a local daemon you run yourself. It listens on `127.0.0.1` by default; off-loopback traffic requires TLS and a paired device key ([docs/remote-access.md](docs/remote-access.md)).
- Loopback requests carrying reverse-proxy headers (`X-Forwarded-*`, `Forwarded`, `X-Real-IP`) or arriving while Tailscale Funnel targets the daemon's port never get unauthenticated local authority: paired devices (`sds_` session tickets and paired browser sessions) still work on public and device routes, while `/setup`, `/debug` and pairing routes answer `403`. Never expose the port to the open internet.
- Any paired device can read agent screens, open a phone shell (when `web.shell` is on) and answer prompts until it is revoked on the setup page. The one rail in code: an approval is answered only on the user's own words in that turn.
- API keys live in `~/.config/sheepdog/.env` (mode 0600) and are redacted from logs and reports.

### The iPhone app

- The app talks only to the sheepdog daemon at the address you give it (typed, or from the daemon's pairing code), and, for notifications, to Apple directly or through the push relay, which forwards end-to-end encrypted payloads it cannot read. It has no server of its own.
- Each paired device generates its own P-256 signing key (kept in the Secure Enclave on iPhone when available) and pins the daemon's TLS certificate fingerprint from the one-time pairing code. Answering an approval from a notification needs the passcode.
- Connections off the computer use HTTPS and WSS verified against the pinned certificate fingerprint. Use it on a trusted LAN or Tailscale only.
- A `sheepdog://pair` link changes nothing until you confirm "Pair with <host>?".

In scope: the daemon, the CLI, the herdr plugin scripts, the web pages, the push relay and the iPhone app in this repository.

## Supported versions

The latest release only (for the iPhone app, the latest TestFlight build).
