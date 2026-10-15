# Security policy

## Reporting a vulnerability

Email **kccarlos.opensource@gmail.com** with a description, the steps to reproduce and the version (`sheepdog version`). Please do not open a public issue for a vulnerability.

Do not include real API keys or tokens in a report. `sheepdog doctor --report` redacts them.

## What sheepdog is, for threat-modelling

- It is a local daemon you run yourself. It listens on `127.0.0.1` by default and refuses a non-loopback address without a token.
- It has **no TLS**. Use your LAN or Tailscale; never expose the port to the open internet ([docs/remote-access.md](docs/remote-access.md)).
- Anyone with the web token and network access can read agent screens and answer their prompts. The one rail in code: an approval is answered only on the user's own words in that turn.
- API keys live in `~/.config/sheepdog/.env` (mode 0600) and are redacted from logs and reports.

In scope: the daemon, the CLI, the herdr plugin scripts and the web pages in this repository. The iOS app has [its own repository](https://github.com/kccarlos/herdr-sheepdog-ios); report issues in it to the same address.

## Supported versions

The latest release only.
