# Security policy

## Reporting a vulnerability

Email **kccarlos.opensource@gmail.com** with a description, the steps to reproduce and the version (`sheepdog version`, or the iPhone app's Settings > About). Please do not open a public issue for a vulnerability.

Do not include real API keys or tokens in a report. `sheepdog doctor --report` redacts them.

## What sheepdog is, for threat-modelling

- It is a local daemon you run yourself. It listens on `127.0.0.1` by default; off loopback, phone access serves HTTPS and WSS only and admits paired devices only.
- Off `127.0.0.1`, every connection uses **TLS** with the daemon's own ECDSA P-256 certificate (`tls.crt` / `tls.key`, or an optional Tailscale certificate `tsnet.crt` for browsers); plain HTTP answers `127.0.0.1` only. Use your LAN or Tailscale, and never expose the port to the open internet ([docs/remote-access.md](docs/remote-access.md)).
- Each paired device authenticates with its own P-256 key and can read agent screens and answer their prompts until it is revoked on the setup page. The one rail in code: an approval is answered only on the user's own words in that turn.
- API keys live in `~/.config/sheepdog/.env` (mode 0600) and are redacted from logs and reports.

### The iPhone app

- The app talks only to the sheepdog daemon at the address you give it (typed, or from the daemon's pairing code), and, for notifications, to Apple directly or through the push relay, which forwards end-to-end encrypted payloads it cannot read. It has no server of its own.
- Each paired device has its own P-256 key in the Secure Enclave / keychain (and any legacy token credential stays in the keychain, available only when the phone is unlocked). Answering an approval from a notification needs the passcode.
- Off loopback, the connection uses **HTTPS and WSS with TLS**, pinned to the certificate fingerprint in the pairing code (`PinnedTrustDelegate`); plain HTTP is used for loopback only. The app sets `NSAllowsArbitraryLoads` because App Transport Security rejects self-signed certificates on LAN and Tailscale IP addresses even after pinned trust evaluation. Use it on a trusted LAN or Tailscale only.
- A `sheepdog://pair` link changes nothing until you confirm "Pair with <host>?".

In scope: the daemon, the CLI, the herdr plugin scripts, the web pages, the push relay and the iPhone app in this repository.

## Supported versions

The latest release only (for the iPhone app, the latest TestFlight build).
