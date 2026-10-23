# remote access

**Reach sheepdog from your phone, at home or away, without opening it to the internet.**

By default sheepdog listens on `127.0.0.1`, which only this computer can
reach. Turning on **phone access** (step 4 of `sheepdog setup`, or
`SHEEPDOG_WEB_BIND=0.0.0.0`) makes it reachable from your phone, encrypted
and for paired devices only.

## Pair a phone or a browser

Show a pairing code on the computer, on the setup page or with:

```sh
sheepdog pair
```

- **iPhone or iPad:** scan it in the Sheepdog app (the first screen, or
  Settings ▸ Connection ▸ Scan a new code). See [iphone-app.md](iphone-app.md).
- **Android, a laptop or any browser:** point the camera at it, or open
  the link. The browser opens sheepdog's voice page, warns once about the
  certificate (accept it), and pairs itself.

The code holds the computer's Tailscale address first (when Tailscale is
running), then its Wi-Fi/LAN address (a private 192.168.x, 10.x or
172.16-31.x address; VPN, proxy and container addresses are left out), and
the fingerprint of its certificate. With no Tailscale it holds the Wi-Fi
address only, so the phone must be on the same Wi-Fi.

**A code works once and expires after 10 minutes.** Showing a new one
replaces it. The setup page lists every paired device; if one is not
yours, revoke it there.

## Away from home: Tailscale

[Tailscale](https://tailscale.com) gives your computer and phone a private
network, with no router settings and no public address.

1. Install Tailscale on the computer and on the phone.
2. Sign into the **same account** on both.
3. Show a new code and scan it. The code now includes the computer's
   Tailscale address. (An old code does not.)

The setup page shows which variant applies: "Works on the same Wi-Fi. To use
it away from home, install Tailscale on this computer and your phone", or, when
Tailscale is on, "Tailscale is on this computer: with Tailscale on your phone
too (same account), the app works away from home."

## Through a router

A work computer that cannot run Tailscale can still be reached through a
router that does (OpenWrt, GL.iNet and the like):

- **Subnet routing** (the router shares your home network with the
  tailnet): nothing to do. The Wi-Fi address in the code works away from
  home.
- **A port forward** from the router's own address: the computer cannot
  know that address. Add it to `web.advertise` in
  [CONFIG.md](../CONFIG.md) (for example `["100.101.1.1:7777"]`) and show
  a new code.

The app checks the computer by its certificate's fingerprint, never by
its address, so any path to the computer works.

## Security

- **Everything off this computer is encrypted.** sheepdog serves TLS on its
  usual port with its own certificate, made on first start and valid for
  a year.
  Plain HTTP answers only this computer itself (`127.0.0.1`): the local
  setup, voice and debug pages.
- **Each device has its own key.** The iPhone keeps it in the Secure
  Enclave, a browser in its own storage; it never leaves the device. There
  is no shared password: a photo of an old pairing code is worthless.
- **The setup page lists every paired device** with its last contact.
  **Revoke** cuts one off at once. **Disconnect all devices** cuts off every
  device; each scans a new code to come back. **Reset pairing** does that
  and also replaces the certificate, for when you suspect someone copied
  this computer's files.
- **Debug and internal pages answer only this computer:** the setup page,
  `/debug`, pairing codes and the herdr hooks. From a phone they answer
  `403`.
- **Never expose the port to the open internet:** no port forwarding to
  the internet, no public tunnel, no `0.0.0.0` on a machine with a public
  address.
- **A browser on a hostile network** cannot check the certificate the way
  the app does. On public Wi-Fi, pair browsers over Tailscale.

To turn phone access off, press **Turn phone access off**: the daemon returns
to loopback.

## The phone shell

The iPhone app can open a plain shell in a project folder ("Open shell
here" in an agent's ⋯ menu). **Shell from your phone** on the setup page
switches it for every paired device of this computer; it is on by default
and takes effect at once (`web.shell` in [CONFIG.md](../CONFIG.md)).
Turned off, the app hides the shell and attachments; agent windows still
work.

A tunnel or reverse proxy on the computer itself (one that reaches sheepdog
on `127.0.0.1`) looks like this computer to sheepdog. `tailscale serve` is
fine; never put a public tunnel in front of sheepdog (see above).

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| Setup page stays on "Waiting for the phone…" | The phone is on another network. Same Wi-Fi, or use Tailscale as above. |
| "This code was already used or has expired" | Codes work once, for 10 minutes. Show a new code and scan it. |
| App says "Scan the pairing code again" | The computer's certificate changed (sheepdog reinstalled, or Reset pairing) or this phone was revoked. Show a new code and scan it. If you did neither, do not continue on that network. |
| App says Offline at home | The computer's address changed or the daemon is down. `sheepdog status`; scan a new code. |
| Works at home, not away | Tailscale is not on both devices, or they use different accounts. Scan a fresh code after fixing. |
| The pairing code has no Tailscale address | Tailscale was not running when you paired. Start it, then show a new code. |
| The browser warns about the certificate | Expected once per browser: sheepdog makes its own certificate. Check you opened the address from the code. |
| `sheepdog status` shows `0 registered` phones | Normal until a phone has connected and allowed notifications. |
| `403` opening `/setup` from the phone | By design. Open `http://127.0.0.1:7777/setup` on the computer. |
| Corporate or guest Wi-Fi blocks it | These networks often isolate devices. Use Tailscale. |

`sheepdog doctor` checks the bind and the port.
