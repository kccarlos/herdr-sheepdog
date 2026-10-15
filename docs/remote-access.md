# Remote access

By default sheepdog listens on `127.0.0.1`, which only this computer can
reach. Turning on **phone access** (step 4 of `sheepdog setup`, or
`SHEEPDOG_WEB_BIND=0.0.0.0` with a token) makes it reachable from your phone.

## Same Wi-Fi

Pair once and it works whenever the phone and the computer are on the same
network:

```sh
sheepdog pair
```

Scan the QR code in the app (Settings, Daemon, Scan pairing code). The code
holds every LAN and Tailscale address the daemon listens on, plus the token;
the app uses the first address that answers. See [iphone-app.md](iphone-app.md).

## Away from home: Tailscale

[Tailscale](https://tailscale.com) gives your computer and phone a private
network, with no router settings and no public address.

1. Install Tailscale on the computer and on the phone.
2. Sign into the **same account** on both.
3. Run `sheepdog pair` again and scan the new code. The code now includes the
   computer's Tailscale address. (An old code does not.)

The setup page shows which variant applies: "Works on the same Wi-Fi. To use
it away from home, install Tailscale on this computer and your phone", or, when
Tailscale is on, "Tailscale is on this computer: with Tailscale on your phone
too (same account), the app works away from home."

## Security

- **The token** (`web.token`) is the only credential. Anyone who has it and can
  reach the port can read your agents' screens and answer their prompts.
  `sheepdog setup` creates a random one and never replaces it silently.
  It sits in `~/.config/sheepdog/.env` (mode 0600) and in the phone's keychain.
- **There is no TLS.** Traffic between phone and computer is plain HTTP and
  WebSocket. A trusted LAN or Tailscale (which encrypts its own tunnel) is
  fine.
- **Never expose the port to the open internet:** no port forwarding, no public
  tunnel, no `0.0.0.0` on a machine with a public address.
- The daemon refuses to start on a non-loopback address without a token.
- The setup page (`/setup`) answers only the computer sheepdog runs on, never
  the phone or the LAN.

To rotate the token, press **New token** on the setup page and pair again. To
turn phone access off, press **Turn phone access off**: the daemon returns to
loopback.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| Setup page stays on "Waiting for the phone…" | The phone is on another network. Same Wi-Fi, or use Tailscale as above. |
| App says Token refused | The token changed. Run `sheepdog pair` and scan again. |
| App says Offline at home | The computer's address changed or the daemon is down. `sheepdog status`; scan `sheepdog pair` again. |
| Works at home, not away | Tailscale is not on both devices, or they use different accounts. Scan a fresh `sheepdog pair` after fixing. |
| The pairing code has no Tailscale address | Tailscale was not running when you paired. Start it, then `sheepdog pair`. |
| `sheepdog status` shows `0 registered` phones | Normal until a phone has connected and allowed notifications. |
| `sheepdog start` says the bind needs a token | `web.bind` is not loopback and no token is set. Run `sheepdog setup` and turn on phone access. |
| `403` opening `/setup` from the phone | By design. Open `http://127.0.0.1:7777/setup` on the computer. |
| Corporate or guest Wi-Fi blocks it | These networks often isolate devices. Use Tailscale. |

`sheepdog doctor` checks the bind, the token and the port.
