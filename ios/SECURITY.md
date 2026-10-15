# Security policy

## Reporting a vulnerability

Email **kccarlos.opensource@gmail.com** with a description, the steps to
reproduce, and the app version (Settings > About). Please do not open a
public issue for a vulnerability.

Do not include real tokens or keys in a report. If the daemon is involved,
`sheepdog doctor --report` redacts them.

## What the app is, for threat modelling

- The app talks only to the sheepdog daemon at the address you give it
  (typed, or from the daemon's pairing code), and to Apple for push
  notifications when your daemon can send them. It has no server of its own.
- The daemon's token is the only credential. The app keeps it in the
  keychain (available only when the phone is unlocked). Answering an
  approval from a notification needs the passcode.
- The connection is **plain HTTP and WebSocket without TLS**, because the
  daemon offers none. Use it on a trusted LAN or Tailscale only. This is
  why the app sets `NSAllowsArbitraryLoads`.
- A `sheepdog://pair` link changes nothing until you confirm "Pair with
  <host>?".

In scope: the code in this repository. The daemon has
[its own policy](https://github.com/kccarlos/herdr-sheepdog/blob/main/SECURITY.md);
report daemon issues to the same address.

## Supported versions

The latest TestFlight build only.
