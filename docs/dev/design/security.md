# Security: pairing keys, encryption and device access

Status: **proposal for review** (2026-10-20). Nothing here is built yet.
Section 16 lists the phases; section 17 lists the open questions.

**Goal.** Make the phone and browser connection to the daemon safe on any
network, with **no extra steps for the user**. Pairing stays one QR scan.
Everything below rides on that scan.

**Today** ([remote-access.md](../../remote-access.md),
[ws-contract.md](../ws-contract.md#auth)):

- One shared secret, `web.token`. Every phone and browser holds the same
  one. It travels as a header, as `?token=` on `/ws`, `/events` and `/term`,
  or as the `sheepdog_token` cookie.
- Plain HTTP and WebSocket on the LAN. Tailscale encrypts its own tunnel
  (WireGuard), but the plain LAN does not.
- The phone shell is gated by network: on for loopback and Tailscale, on
  the LAN only with `web.shell_lan`
  ([term-contract.md](../term-contract.md#the-plain-shell)).
- The app has an ATS exception (`NSAllowsArbitraryLoads`) so it can speak
  plain HTTP.

**Decisions**

| Question | Decision |
|---|---|
| Encryption | TLS on every address except loopback. The daemon makes its own self-signed certificate. The pairing QR carries its fingerprint (an SPKI pin). The app pins it by fingerprint, never by hostname or IP. Loopback stays plain HTTP. |
| Browser certificate | If the user's Tailscale has HTTPS certificates turned on, the daemon also serves the real `*.ts.net` certificate, chosen by SNI. Otherwise browsers see the self-signed one and show a one-time warning. |
| One port | The daemon keeps one port. It reads the first byte of each connection: `0x16` is a TLS handshake, anything else is plain HTTP. Plain HTTP from a non-loopback address is allowed only for old apps during the migration. |
| Device identity | Each phone and browser makes its own **P-256 keypair** at pairing. On iPhone the key lives in the Secure Enclave. In a browser it is a non-extractable WebCrypto key. The daemon records each device's public key. |
| Proof of possession | A **signed challenge** that returns a short session ticket, not mutual TLS. Section 5 explains why. |
| Migration | A new app talking to a new daemon enrolls its key on its own, using the old token once. Old apps keep working until "Allow old apps" turns off. |
| Devices list | On the setup page: name, kind, last seen, capabilities, revoke, and "Disconnect all". Devices unused for 90 days expire. |
| Browsers elsewhere | The browser shows a QR code, a 6-digit code and "Waiting for approval". A paired phone approves it, by scanning or through an in-app prompt. The setup page can approve it too. |
| Tailscale identity | When a request comes from a tailnet address, the daemon asks the Tailscale LocalAPI who sent it. It allows only devices of the same Tailscale user. It skips this check silently when Tailscale is not installed. |
| Capabilities | Four tiers per device: **view**, **approve**, **control**, **shell and files**. Shell and file access becomes per device, not per network. |
| Face ID | Optional, not yet decided. Two variants are in section 9. |
| Limits | Voice: one client, with take-back. Terminal: 4 channels per device, 8 per daemon. About 32 connections in total. Rate limits on challenges and approval requests. |
| Router forwarding | Required scenario. Pins and device keys never depend on the source address, so a router that forwards or NATs the traffic works. |
| Future relay | Out of scope. The device keys and the pin are designed to work unchanged through a future outbound relay. |

---

## 1. Threat model

### What we defend against

1. **Other people on the same network.** Coffee-shop Wi-Fi, an office LAN,
   a guest network, a flatmate. They can sniff traffic, spoof ARP or DNS,
   and run a fake daemon. Today they can read the token and every
   keystroke on the plain LAN.
2. **A leaked secret.** A photo of the pairing QR, a screenshot sent to
   the wrong chat, a token copied into a log. Today the token in that
   photo works forever.
3. **A lost or stolen phone.** The user must be able to cut off one device
   without re-pairing the others.
4. **Other devices on the tailnet.** Nodes shared in from another
   tailnet, other users of a family or team tailnet, a compromised
   smart-home box with a tailnet address.
5. **Malicious web pages in the user's own browser.** Cross-site request
   forgery, cross-site WebSocket hijacking, and DNS rebinding against
   `127.0.0.1:7777`.
6. **Floods and guessing.** Someone on the LAN who opens many sockets,
   spams approval requests, or guesses codes.
7. **The push relay operator.** Already handled: notifications are
   end-to-end encrypted ([push.md](../push.md#end-to-end-encryption)).
   Nothing here changes that.

### What we do not defend against

- **Malware on the computer, or anyone with the user's login there.** It
  can read the daemon's data directory and keys, and talk to loopback.
  The agents themselves run as the user too.
- **Someone using the computer's unlocked session.** They can open the
  setup page and pair their own phone. The setup page is loopback-only,
  so this needs the computer itself.
- **A compromised, unlocked phone.** The key cannot be copied off the
  phone, but code on the phone can use it. Revoking the device stops it.
- **Breaks in TLS, WireGuard, the Secure Enclave or the browser.**
- **Traffic analysis.** Packet sizes and timing still show when the user
  talks or types.
- **A user who clicks through the browser warning on a hostile network on
  the very first visit, then approves by prompt rather than by scan.**
  Section 4.2 explains this case and why scanning closes it.

---

## 2. The layers and what each stops

| Layer | What it is | What it stops | What it does not stop |
|---|---|---|---|
| Reach | `web.bind`, loopback default, LAN or Tailscale | Strangers on the internet (no port forwarding to the internet, ever) | Anyone on the same LAN or tailnet |
| TLS with a pin | Self-signed certificate, fingerprint pinned from the QR | Sniffing and man-in-the-middle on the LAN, a fake daemon | Who the client is |
| Device key | P-256 key per device, signed challenge, short session ticket | A stolen QR photo, a leaked token, a copied cookie (short-lived), one lost device among many | A thief using an unlocked phone |
| Tailscale identity | LocalAPI `whois` on tailnet source addresses | Other users' devices and shared-in nodes on the tailnet | Traffic that reaches the daemon from the LAN |
| Capability tiers | view, approve, control, shell and files per device | A browser that only watches being able to type or open a shell | Misuse within a granted tier |
| Face ID (optional) | A second key that needs a face, for risky actions | Someone holding the unlocked phone opening a shell | Everyday actions |
| Host and Origin checks | Allowed `Host` names, same-origin `Origin` on upgrades and writes | DNS rebinding, cross-site WebSocket hijacking, CSRF | — |
| Limits | Connection caps, handshake timeouts, rate limits | Floods, code guessing | A large distributed attack |
| Audit | Device id on events, metadata only | Not knowing which device did what | — |
| Rails | Unchanged ([roles.md](../roles.md)) | The orchestrator acting without the user's words | — |

---

## 3. Encryption

### 3.1 The daemon's identity

- At first start the daemon makes:
  - a **daemon id**: 16 random bytes, base64url;
  - an **ECDSA P-256 key** and a **self-signed certificate** for it,
    signed with SHA-256, valid for 10 years. It has no meaningful names:
    nobody checks them.
- They live in `<data_dir>/tls/` (directory mode 0700, files 0600):
  `daemon_id`, `key.pem`, `cert.pem`.
- The **pin** is `base64url(SHA-256(SubjectPublicKeyInfo))` of that key.
  It changes only when the key changes. Re-issuing the certificate with
  the same key (for example when it nears expiry) keeps the pin.
- Losing the data directory means a new daemon id and a new pin. Every
  device then has to scan again (section 4.3).

### 3.2 One port, two protocols

- The listener peeks at the first byte of each new connection.
  - `0x16` (TLS handshake): serve TLS 1.2 or newer, ECDHE only.
  - Anything else: plain HTTP.
- **Loopback** (`127.0.0.0/8`, `::1`): plain HTTP, exactly as today. The
  local `/setup`, `/voice` and `/debug` pages do not change. TLS on
  loopback also works, but nothing needs it.
- **Non-loopback plain HTTP** is served only while "Allow old apps" is on
  (section 7), and only to requests that carry the legacy token. Anything
  else gets:
  - a page route (`GET /`, `/voice`, `/debug`): `308` to the same path on
    `https://` at the same host and port;
  - an API route or an upgrade: `426 {"error":"tls_required"}`.
- A device session (section 5) is never accepted over non-loopback plain
  HTTP, even while old apps are allowed.
- The port number stays the same, so stored `host:port` addresses keep
  working.

### 3.3 Which certificate a client gets

- **No SNI, or an IP address** (what the app sends): the self-signed
  certificate.
- **SNI equal to the machine's `*.ts.net` name**, when Tailscale HTTPS is
  on: the real certificate, fetched from the Tailscale LocalAPI (the same
  call `tailscale cert` uses). The daemon checks once a day and renews it
  when Tailscale does.
- The app **never pins the `*.ts.net` certificate**: it rotates about
  every 90 days and its key can change. The app always connects by IP
  and pins the self-signed key.
- Browsers use the `*.ts.net` name when the setup page offers one, so
  they show no warning. Otherwise they use the IP address and accept the
  self-signed certificate once.
- Tailscale's HTTPS certificates are public: their names go into
  Certificate Transparency logs, so the machine name and tailnet name
  become public. That is an open question (17.3).

### 3.4 The app checks the pin

- `DaemonClient` gets a `URLSession` delegate. For a server-trust
  challenge it:
  1. takes the leaf certificate's public key;
  2. hashes its SubjectPublicKeyInfo with SHA-256;
  3. accepts if it equals the stored pin (or the stored next pin,
     section 4.4), and refuses otherwise.
- The hostname and IP are not checked. That is what makes router
  forwarding work (section 14).
- The same delegate serves `URLSessionWebSocketTask`, so `/ws` and
  `/term` are pinned too.
- The pin is checked during the TLS handshake, before any request. A
  wrong pin sends nothing: no hello, no ticket, no audio.
- The app's own code refuses `http://` to a non-loopback address once it
  holds a pin. ATS is a second wall, not the only one.
- The ATS exception is removed only in a late phase (section 16, phase 10),
  because a new app must still reach an old daemon until then.

---

## 4. Pairing flows

### 4.1 Phone (new pairing)

The QR keeps today's form and gains three parameters:

```
sheepdog://pair?u=http://100.101.102.103:7777&u=http://192.168.1.20:7777&d=<daemon id>&k=<pin>&e=<enroll code>&t=<token>
```

- `u` stays `http://host:port`. Today's `PairingLink` drops any `u` that
  is not plain HTTP. If the QR listed `https://` URLs, an old app would
  find no address. A new app sees `k` and speaks TLS to the same
  `host:port`.
- `d`: the daemon id. `k`: the pin.
- `e`: a one-time **enroll code**, 16 random bytes. It works once and
  expires 10 minutes after the QR is shown. The setup page and
  `sheepdog pair` show a fresh one each time.
- `t`: the legacy token, only while "Allow old apps" is on, so an old app
  can still pair. Once that switch is off, `t` is left out, and an old
  QR photo is useless after 10 minutes.

Step by step:

1. The user scans the QR (in the app, or with the Camera app and a
   confirmation, as today).
2. The app tries the addresses, Tailscale first, with TLS pinned to `k`.
   It keeps the first that answers `GET /api/hello`. A pin mismatch on
   one address counts as "did not answer" for that address.
3. The app creates a key in the Secure Enclave, tagged with the daemon
   id (section 6).
4. The app calls `POST /api/devices/enroll` with the enroll code, its
   public key, its name (the system gives "iPhone"; the user can rename it later)
   and its kind (`ios`, `ipados`).
5. The daemon checks the code, records the device with the default tiers
   (section 8), and answers `{device_id, caps, daemon_id}`.
6. The app signs a challenge and gets its first session ticket
   (section 5).
7. The app saves the daemon's entry: daemon id, addresses, pin, device
   id, key tag. It saves no token.

The user sees what they see today: scan, a moment, connected.

### 4.2 Browser on another computer

The browser is on a laptop that is not the computer sheepdog runs on.

1. The user opens `https://<address>:7777/voice`. With Tailscale HTTPS
   on, the setup page shows a `*.ts.net` link instead, with no warning.
   Otherwise the browser warns once about the certificate.
2. The page finds no session. It creates a non-extractable P-256 key with
   WebCrypto and stores it in IndexedDB (section 6).
3. It calls `POST /api/pair-requests` with the public key, a kind
   (`browser`), and a name. The name starts as "Mac · Safari", from the
   user agent; the user can change it in a text field. A browser cannot
   tell "MacBook Air" from "Mac" (open question 17.7).
4. The daemon answers `{request_id, secret, code, expires_in: 300}`.
   `code` is 6 random digits. `secret` lets only this browser read the
   outcome.
5. The page shows:
   - a QR code: `sheepdog://approve?d=<daemon id>&r=<request id>&f=<SHA-256 of the browser's public key>`;
   - the 6-digit code, large;
   - "Waiting for approval from your phone or this computer's setup page".
6. Approval, by any one of:
   - **Scanning.** The phone scans the browser's QR. The app shows the
     approval sheet. It checks that `f` matches the key the daemon
     received for that request.
   - **In-app prompt.** Every connected device with the control tier gets
     a `pair-request` message on `/ws`. A phone that is not connected
     gets a notification that opens the app; its Allow button needs the
     phone unlocked. The sheet says: "New browser (Mac · Safari) wants
     to connect, code 482913. Allow?" It has an "Also allow shell and
     files" switch, off by default.
   - **The setup page** on the computer lists the pending request with
     the same text and Allow and Deny.
7. The approver calls `POST /api/pair-requests/{id}/approve` with the
   tiers. A phone can grant only tiers it holds itself.
8. The page has been waiting on `GET /api/pair-requests/{id}?secret=…`
   (long poll). It gets `{device_id, caps}`, signs a challenge, and
   receives its session cookie.

**Why scanning is stronger than the prompt.** With a self-signed
certificate the browser has no pin. If an attacker sits in the middle on
the very first visit, they can make their own request and show the
browser the attacker's code. The user would then approve the attacker.
Comparing 6 digits does not stop this, because the attacker relays the
code. Scanning does stop it: the browser's QR carries the full hash of
the browser's own key, computed in the browser, and the phone compares it
with the key the daemon received. With the `*.ts.net` certificate, or on
a network the user trusts, the prompt is as good as the scan. The sheet
for a request that came in over a plain LAN with a self-signed
certificate says "Scanning the code on that screen is safer."

**Limits:**

- At most 3 pending requests per daemon, and one new request per source
  address every 10 seconds, 20 per hour daemon-wide.
- A request expires after 5 minutes.
- Three denials from one source address block it for 15 minutes.

### 4.3 Re-pair after a lost certificate

The pin no longer matches. This happens when sheepdog was reinstalled,
its data directory was reset, or the computer was replaced. It also
happens if someone is intercepting the connection; the app cannot tell
the two apart, so it treats them the same.

1. The TLS handshake fails the pin check. The app sends nothing.
2. If every address fails the same way, the app stops retrying and
   shows:
   > **Scan the pairing code again.** This computer's security key
   > changed. That happens when sheepdog is reinstalled or its settings
   > are reset. If you did not do that, someone may be intercepting the
   > connection: do not continue on this network.
3. There is no "trust anyway" button.
4. The user scans a fresh QR. If the daemon id is new, the app deletes
   the old entry and its key, and pairs as a new device. If the daemon id
   is the same (only the TLS key was reset), the app replaces the pin and
   the enroll code re-binds its existing device record to its key.

The app shows a reason for every "scan again" case:

| Cause | Daemon says | App says |
|---|---|---|
| Pin mismatch | (nothing, TLS fails) | "This computer's security key changed…" (above) |
| Device revoked on the computer | `401 {"reason":"device_revoked"}` | "This phone was removed on your computer. Scan the pairing code again to reconnect." |
| Unused for 90 days | `401 {"reason":"device_expired"}` | "This phone was not used for 90 days, so your computer forgot it. Scan the pairing code again." |
| Unknown device (data reset, same daemon id) | `401 {"reason":"unknown_device"}` | "Your computer no longer knows this phone. Scan the pairing code again." |
| Old apps turned off, app still on the token | `401 {"reason":"token_retired"}` | "Scan the pairing code again to finish the security update." |
| Phone restored from a backup | (no key in the Secure Enclave) | "This is a new phone to your computer. Scan the pairing code again." |

### 4.4 Certificate rotation

The user, or a future release, replaces the TLS key while the data
directory survives.

1. The daemon makes the new key and keeps serving the old one.
2. It announces `next_pin` to every authenticated client for 7 days: in
   the session answer and in `bot-ready` (section 11).
3. Each app that connects in that window stores it next to the current
   pin.
4. After 7 days the daemon serves the new key. Apps that stored it accept
   it and drop the old pin.
5. An app that did not connect in the window sees a pin mismatch and
   shows the "scan again" message, with the reason "This computer
   changed its security certificate while this phone was away."

Rotation is rare: the certificate lasts 10 years, and the key changes only
when the user asks (open question 17.9).

---

## 5. Device authentication

### 5.1 The scheme: a signed challenge and a session ticket

1. `GET /api/auth/challenge` (public, rate-limited) answers
   `{nonce, daemon_id, expires_in: 60}`. The nonce is 32 random bytes and
   works once.
2. The client signs, with its device key (ECDSA P-256, SHA-256), the
   UTF-8 text:

   ```
   sheepdog-auth-v1
   <daemon id>
   <pin>
   <device id>
   <nonce>
   <unix time>
   session
   ```

   A browser does not know the pin, so it signs an empty line there; the
   daemon checks it the same way.
3. `POST /api/auth/session {device_id, nonce, time, signature, client}`.
   The daemon checks:
   - the nonce is unused and fresh;
   - the time is within 5 minutes;
   - the device exists and is not revoked or expired;
   - the signature is valid for the device's public key;
   - the Tailscale identity, when it applies (section 8.3).
4. It answers `{ticket, expires_in: 43200, caps, device: {id, name}}`.
   For a browser it also sets the `sheepdog_session` cookie (`HttpOnly`,
   `Secure`, `SameSite=Strict`, `Path=/`).
5. The ticket is 32 random bytes. The daemon keeps it in memory only,
   mapped to the device. A restart forgets all tickets, and clients sign
   again.
6. Every request carries the ticket:
   - the app: `Authorization: Bearer sds_<ticket>`;
   - a browser: the cookie.
7. On `401 {"reason":"session_expired"}` the client signs a new challenge
   and retries once.

Revoking a device drops its tickets **and closes its open `/ws`, `/term`
and `/events` connections** within a second.

### 5.2 WebSocket and SSE upgrades

- `/ws`, `/term` and `/events` authenticate **before the upgrade**, like
  any other request. `/ws` sends its `status` at once on connect, before
  any handshake ([ws-contract.md](../ws-contract.md#protocol-version-and-handshake)),
  so auth cannot wait for a first frame.
- The app sets the `Authorization` header on the upgrade request.
  `URLSessionWebSocketTask` allows that.
- A browser cannot set headers on a WebSocket or `EventSource`. It sends
  the cookie, which a same-origin upgrade carries.
- `?token=` is kept only for legacy token clients. New clients never put
  a secret in a URL, so none ends up in history, logs or a proxy.
- Every upgrade and every state-changing request checks `Origin`. It must
  be absent (the app) or the daemon's own origin. This stops cross-site
  WebSocket hijacking even if a cookie were sent.

### 5.3 Why not the alternatives

- **Mutual TLS with a client certificate.**
  - A browser cannot use a WebCrypto key as a TLS client certificate. We
    would need a second scheme for browsers.
  - On iOS, a client identity from a Secure Enclave key needs a
    certificate in the Keychain and a custom identity. That is possible
    but fragile.
  - A future relay may end TLS at the edge (section 18). An application
    signature survives that; a client certificate does not.
  - Loopback is plain HTTP, so it would need its own path anyway.
- **Signing every request.** No ticket to steal, but every REST call and
  upgrade would need a canonical form, a replay window and a Secure
  Enclave signature. That costs more for little gain, because TLS already
  protects the ticket in transit.
- **A random secret per device instead of a key.** Simpler, but it is a
  copyable bearer secret: it leaks through a backup, a log or a
  debugger. A Secure Enclave key cannot leave the phone.
- **Binding to the TLS session (an exporter).** Neither browsers nor
  `URLSession` expose one. Binding the signature to the daemon id and pin
  instead does the same job against a fake daemon, and survives a relay.

**P-256** is the one curve both the Secure Enclave and WebCrypto support
everywhere.

---

## 6. Key storage

| Platform | Device key | Other data | Notes |
|---|---|---|---|
| iPhone, iPad | Secure Enclave P-256 (`kSecAttrTokenIDSecureEnclave`), `.privateKeyUsage`, accessible after first unlock, this device only. One key per daemon, tagged `sheepdog.device.<daemon id>`. | Daemon id, addresses, pin, next pin, device id in the Keychain (this device only). The ticket in memory only. | "After first unlock" lets a notification's Approve work while the phone is locked. The key is not in backups and not in iCloud Keychain; a restored or new phone pairs again. |
| iOS Simulator | Software P-256 key in the Keychain, same attributes | Same | No Secure Enclave. The enroll call says `hardware: false`, and the devices list shows it. |
| Browser | WebCrypto ECDSA P-256, `extractable: false`, `["sign"]`, stored as a `CryptoKey` in IndexedDB, keyed by daemon id | Device id in IndexedDB; ticket in the `HttpOnly` cookie | Per origin: `https://192.168.1.20:7777`, `https://100.x:7777` and `https://mac.tailnet.ts.net:7777` are three devices. Private windows keep nothing. Clearing site data, or Safari's tracking prevention after long disuse, removes the key; the browser asks for approval again. |
| Daemon | Not applicable | `<data_dir>/tls/` (TLS key, certificate, daemon id), `<data_dir>/devices.json` (0600: id, name, kind, public key, caps, created, last seen, last network kind, how it enrolled, hardware flag). `web.token` stays in `~/.config/sheepdog/.env`. | The push device list gets a `device_id` link, so revoking a device also drops its push registration and content key. |

Push content keys are already per phone and stay as they are
([push.md](../push.md#end-to-end-encryption)).

---

## 7. Migration and compatibility

### 7.1 Who talks to whom

| App | Daemon | What happens |
|---|---|---|
| Old app | Old daemon | Unchanged. |
| Old app | New daemon | Plain HTTP and the token, as today, while "Allow old apps" is on. Its shell stays gated by network, exactly as today (`web.shell_lan`). After the switch is off: `401 {"reason":"token_retired"}`, and the old app says "Token refused". |
| New app | Old daemon | `/api/hello` has no `security` block. The app uses plain HTTP and the token, as today. After phase 10 the app says "Update sheepdog on your computer" instead. |
| New app | New daemon, new pairing | Section 4.1. |
| New app | New daemon, old pairing (token, no pin) | Automatic migration, below. |

### 7.2 Automatic migration with no re-scan

The app has a token and an address but no pin, because its QR had no
`k`. It must not trust the first certificate it sees.

1. The app calls `GET /api/hello` over plain HTTP, as today. It sees the
   `security` block.
2. It calls `GET /api/tls/pin-proof?nonce=<16 random bytes>` over plain
   HTTP. The daemon answers
   `{daemon_id, pin, mac}`, where `mac = HMAC-SHA256(token, "sheepdog-pin-v1" || nonce || daemon_id || pin)`.
   The token itself is not sent.
3. The app checks the MAC with its token. Someone in the middle without
   the token cannot forge it.
4. The app connects over TLS, pinned, and calls
   `POST /api/devices/enroll` with `Authorization: Bearer <token>`,
   inside TLS. This is the token's one and last use by this app.
5. The daemon records the device with `enrolled_via: "token"` and the
   tiers in 7.3.
6. The app deletes the token from its Keychain and keeps the pin, the
   daemon id and the device id.

The leftover risk: on a plain LAN, someone who already sniffed the token
earlier could forge the MAC. That person could already do everything
today, so migration is never worse than the present. Over Tailscale the
first step is already encrypted.

### 7.3 Tiers for migrated devices

| Device enrolled with the token over | Tiers |
|---|---|
| Tailscale | All four (it had the shell before) |
| The LAN, with `web.shell_lan` on | All four |
| The LAN, with `web.shell_lan` off | view, approve, control. Shell and files can be granted on the setup page. |

### 7.4 When the shared token stops working

The token bypasses every tier and every device record, so it must end.

- **"Allow old apps"** (a new config key, `web.legacy_token`, documented
  in [CONFIG.md](../../../CONFIG.md) when it is built). When it is on, the
  token works from non-loopback addresses as today.
- It turns off on its own **30 days after the last request that used the
  token from a non-loopback address.** The setup page can turn it off
  sooner. It shows "Old connection (shared token): last used 2 days ago
  from 192.168.1.31".
- On a fresh install with no old devices it starts off.
- The token keeps working **on loopback**, for the local pages, the
  herdr hooks and the CLI. Loopback is the computer itself.
- `web.shell_lan` then has no remaining purpose. It is kept only for old
  apps on plain HTTP and removed with the token.

### 7.5 Version and feature negotiation

- `GET /api/hello` gains a `security` block (section 11.1). Its presence
  is how a new app knows the daemon has TLS and device keys.
- `/ws` stays protocol 1. The new parts are **features** in `bot-ready`,
  as the contract asks: `devices`, `pair-requests`, `caps`, and `step-up`
  if Face ID is chosen.
- `/term` stays protocol 1. New refusal reasons are added; an old client
  treats an unknown reason as a refusal, which is correct.
- The daemon's own pages (`/voice`, `/debug`) ship with the daemon, so
  they need no negotiation.

---

## 8. Devices, capabilities and identity

### 8.1 The devices list

On the setup page (loopback only), a "Devices" section:

- Each row: name (editable), kind (iPhone, iPad, browser · Safari on
  Mac, "old connection"), tiers, added, last seen and over what (Tailscale,
  Wi-Fi, this computer), and a hardware-key badge.
- **Revoke** on each row. It drops the device's tickets, closes its
  connections and removes its push registration.
- **Disconnect all.** Revokes every device and turns off old apps. Every
  phone and browser must scan again. (Whether it also replaces the TLS key
  and the token is open question 17.10.)
- Pending browser requests, with Allow and Deny.
- Devices with no authenticated request for **90 days** are removed.
  "Last seen" counts any authenticated request, including an Approve from
  a notification.

A device can also see itself and unpair itself from the app
(`DELETE /api/devices/self`).

### 8.2 Capability tiers

Each tier includes the ones above it.

| Tier | Allows | Routes |
|---|---|---|
| **view** | Read state, agents, screens, history, events, cards, changes and diffs; receive notifications | `GET /api/state`, `/api/agents`, `/api/agents/{id}/screen`, `/api/agents/{id}/changes…`, `/api/conversation`, `/api/events`, `GET /events`; `/term` channels in `observe` mode; `/ws` without voice (cards, screens) |
| **approve** | Answer or cancel a pending approval | `POST /api/pending/answer`, `/api/pending/cancel`; notification actions |
| **control** | Talk to the voice, type, send keys, new, rename, stop and close an agent, change settings and models, approve new browsers | `/ws` with audio; `POST /api/agents/{id}/input`, `/api/agents` (new), `…/rename`, `…/interrupt`, `…/close`; `PUT /api/settings`, `/api/voice/provider`, `/api/brain/model`; `/term` in `control` mode; `POST /api/pair-requests/{id}/approve` |
| **shell and files** | Open and close the plain shell, attach files | `/term` shell target, `POST /api/shells/{id}/close`, `POST /api/agents/{id}/attachments` |

- Voice needs **control**, because the user can ask the orchestrator to
  type or start agents. The rails still decide what the orchestrator may
  do; the tier decides who may talk to it.
- A missing tier answers `403 {"error":"forbidden","need":"control"}`.
  On `/term` it is `refused:not_allowed`.
- Routes for the computer itself stay loopback-only: `/setup`,
  `/api/setup/*`, and now `/api/pair`, `/api/pair/qr.svg` (the QR holds an
  enroll code) and the device management routes. Proposed: the debug and
  hook routes (`/api/inject/*`, `/api/herdr/event`, `/api/prompts/reload`,
  `/api/brain/*`, `/api/context/*`) become loopback-only too (open
  question 17.6).

**Defaults, chosen so everyday use needs nothing extra:**

| Device | Default tiers | Why |
|---|---|---|
| Phone or iPad paired by QR | All four | Scanning needs the computer's screen, so it is the user. Today the shell is on over Tailscale; this keeps it on everywhere now that the LAN is encrypted. |
| Browser approved by a phone or the setup page | view, approve, control | Watching and talking from a laptop is the everyday use. "Also allow shell and files" is one switch on the approval sheet. |
| Migrated phone | Section 7.3 | Keeps what it had. |

### 8.3 Tailscale identity check

- At start, the daemon looks for the Tailscale LocalAPI (the
  `tailscaled` socket on Linux, the app's local port and token file on
  macOS). Not found: the check is off, silently, and is re-checked every
  few minutes.
- For a request whose **source address is a tailnet address**
  (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`), the daemon calls `whois` on
  the source `ip:port`, cached for 5 minutes per address.
- Allowed only when the peer's user is the same user as the daemon's own
  node. Refused: a node of another user, a node shared in from another
  tailnet, and a tagged node with no user.
- Refused requests get `403 {"error":"forbidden","reason":"tailscale_user"}`
  and a `web.tailscale.denied` event.
- If `whois` errors while Tailscale is installed, the request is refused
  (fail closed) and logged. If Tailscale is not installed, nothing is
  checked.
- A request from a **LAN address** is never checked, even if it came over
  Tailscale through a router. That is the router-forwarding case
  (section 14); the device key protects it.
- Whether to allow other users of the same tailnet is open question 17.4.

---

## 9. Face ID (optional, not decided)

Face ID would be asked only before **opening a shell, attaching files or
closing an agent**. Never on connect, never for voice, never for an
approval.

The main device key must **not** need Face ID, or every connection would
ask for a face.

| | A: in the app only | B: enforced by the daemon |
|---|---|---|
| How | The app shows `LAContext` before the action. | A second Secure Enclave key per daemon with a biometry access control (`.biometryCurrentSet`, passcode fallback). For those actions the daemon asks for a fresh signature from it (`step-up`, valid 5 minutes). |
| What it stops | A casual person with the unlocked phone, if they use our app | The same, and also any code that holds the main key without the user's face. The rule lives in code on the daemon, like the other rails. |
| Cost | Small, app only | One more key, one more route, a new `refused:verify` reason |
| Trade-off | Not a real rail; the daemon cannot tell it happened | One glance before risky actions. Closing an agent by voice with the phone in a pocket would still go through the orchestrator, which is not gated by it. |

Recommendation: **off by default; when the user turns it on (per device,
in the app's Settings), use B.** It breaks "no extra steps" only for the
user who chose it.

---

## 10. Connection limits

| What | Limit | Today |
|---|---|---|
| Voice on `/ws` | One client. A new one replaces the old one. The replaced client sees "Voice moved to Mac · Safari" and a **Take back** button, which simply reconnects. | One client, replaced |
| `/term` channels | 4 per **device** (was per connection), 8 per daemon | 4 per connection, 8 per daemon |
| `/events` streams | 4 per device | No limit |
| All connections | About 32 open connections from non-loopback addresses. Loopback is not counted, so the setup page always opens. | No limit |
| Unauthenticated connections | 8 per source address; TLS handshake and request headers within 10 seconds | No limit |
| Challenges | 30 a minute per source address. After 10 bad signatures in a minute, that address waits 5 minutes. | — |
| Approval requests | Section 4.2 | — |
| Enroll codes | 5 wrong codes in a minute from one address: it waits 5 minutes | — |

---

## 11. Wire changes

These are sketches for review. The real contract goes into
[ws-contract.md](../ws-contract.md) and [term-contract.md](../term-contract.md)
when each phase is built, with their contract tests.

### 11.1 `GET /api/hello`

Still public. It gains:

```
"security": {
  "tls": true,
  "pin": "<base64url SHA-256 of the SPKI>",
  "daemon_id": "<base64url>",
  "auth": ["device", "token"],
  "tailscale_https": "mac.tailnet.ts.net"
}
```

- `auth` lists `token` only while old apps are allowed.
- The old `auth` field stays `"token"` while old apps are allowed, so old
  apps behave as today; it becomes `"device"` after that.
- The pin is a public key hash, safe to publish. The migration does not
  trust this field; it uses the pin proof (7.2).

### 11.2 New routes

| Route | Auth | Purpose |
|---|---|---|
| `GET /api/auth/challenge` | public, rate-limited | A nonce |
| `POST /api/auth/session` | signature | A ticket (and a cookie for browsers) |
| `GET /api/tls/pin-proof?nonce=` | public | The pin, with a MAC keyed by the token (migration) |
| `POST /api/devices/enroll` | enroll code, or the legacy token inside TLS | Record a device's public key |
| `GET /api/devices/self`, `DELETE /api/devices/self` | ticket | See or unpair this device |
| `GET /api/devices`, `PATCH /api/devices/{id}`, `DELETE /api/devices/{id}`, `POST /api/devices/disconnect-all` | loopback only | The setup page's list |
| `POST /api/pair-requests` | public, rate-limited | A browser asks to join |
| `GET /api/pair-requests/{id}?secret=` | the request's secret | Long poll for the outcome |
| `POST /api/pair-requests/{id}/approve`, `…/deny` | control tier, or loopback | Approve or deny |
| `POST /api/auth/step-up` | step-up key signature | Only with Face ID variant B |

### 11.3 `/ws`

- Auth before the upgrade (5.2). Origin checked.
- `bot-ready` adds `device: {id, name, caps}`, `next_pin` during a
  rotation, and the features in 7.5.
- New server messages, sent only to clients whose `bot-ready` listed the
  feature:
  - `pair-request {request_id, name, kind, code, expires_in, network}` to
    control-tier clients, and `pair-request-done {request_id, outcome}`;
  - `status` with reason `replaced` gains `by` (the new device's name),
    for the Take back button.
- New close codes: **4003** device revoked or expired (with the reason
  text), **4004** session expired (sign again and reconnect).

### 11.4 `/term`

- Auth as `/ws`. `term-hello` is unchanged.
- The shell gate moves from the source address to the device's tiers.
  Old clients on plain HTTP keep the address gate.
- Channel limits per device (section 10).
- New refusals: `refused:not_allowed` (missing tier), `refused:verify`
  (Face ID variant B).
- Close codes 4003 and 4004 as on `/ws`.

### 11.5 `/events`

- Header or cookie. `?token=` only for old clients.
- Revocation closes the stream.

### 11.6 REST routes

- Every route names its tier (8.2). Missing ticket: `401` with a
  `reason`. Missing tier: `403` with `need`.
- Every `401` body gains `reason` (`session_expired`, `device_revoked`,
  `device_expired`, `unknown_device`, `token_retired`), so the app can say
  why.
- On loopback, every route checks that `Host` is `127.0.0.1`, `::1` or
  `localhost` (today only `/setup` does). This stops DNS rebinding.

---

## 12. Logging and audit

Metadata only. Never a key, a signature, a ticket, a token, an enroll
code or a 6-digit code.

- New events:
  - `web.device.enroll {device_id, kind, name, via, hardware, remote_kind}`
    (`via`: `qr`, `token`, `browser_approval`, with `approved_by` for the
    last);
  - `web.device.session {device_id, remote_kind}` (each new ticket);
  - `web.device.auth_failed {reason, remote, device_id?}`;
  - `web.device.revoke {device_id, by}`, `web.device.expire {device_id, last_seen}`,
    `web.device.disconnect_all {count}`;
  - `web.pair_request.create|approve|deny|expire {request_id, kind, name, remote_kind, by?}`;
  - `web.tls.created {pin}`, `web.tls.rotate {old_pin, new_pin, phase}`,
    `web.tls.tsnet {name, expires}`, `web.tls.pin_proof {remote_kind}`;
  - `web.tailscale.denied {remote, reason}`;
  - `web.legacy_token.used {remote_kind}` (at most once an hour) and
    `web.legacy_token.off {why}`;
  - `web.limit {what, remote}` when a cap or rate limit refuses
    something.
- Existing events gain `device_id`: `web.client.connect`, `web.client.hello`,
  `web.term.connect`, `web.term.open`, `web.shell.open`, `web.shell.close`,
  the agent action events, and the pending answer events. For an old app
  it is `"legacy"`; for loopback it is `"local"`.
- `remote_kind` keeps its values (`loopback`, `tailscale`, `lan`), so the
  log shows when a device came through a router.

---

## 13. Several computers

The app will manage several daemons, one per computer. Everything here is
**per daemon, keyed by daemon id**, never by address:

- one device key per daemon (`sheepdog.device.<daemon id>`), so revoking
  one computer's access does not touch another, and two computers cannot
  link the phone by its key;
- one pin, next pin, device id and address list per daemon;
- the push content key is already per daemon.

The daemon id also tells "a second computer at the same IP" apart from
"the same computer, reinstalled". Scanning a second computer's QR adds it;
it does not replace the first (the app's screens for this are a separate
design).

---

## 14. Router forwarding (required scenario)

**The case.** A work computer cannot install Tailscale. The user's router
runs Tailscale. Either:

- **(a) Subnet router.** The router advertises the home LAN to the
  tailnet. Away from home, the phone reaches `192.168.1.20:7777`
  directly through the router. By default the router rewrites the source
  (SNAT), so the daemon sees the router's LAN address.
- **(b) Port forward.** The router forwards its own Tailscale address,
  `100.101.1.1:7777`, to `192.168.1.20:7777`. The daemon sees the router's
  LAN address.
- **(c) Subnet router without SNAT.** The daemon sees the phone's
  `100.x` address, but has no Tailscale of its own.

**Why it works.**

- The pin is checked by fingerprint, not by name or IP, so any path to
  the daemon works.
- The device key is checked by signature, not by source address.
- The Tailscale check runs only for tailnet source addresses and only
  when the daemon has Tailscale. In (a) and (b) the source is a LAN
  address. In (c) the daemon has no Tailscale. So it never wrongly
  refuses.
- Shell and files follow the device's tiers, not the network, so they
  work through the router.

**The one gap: the address.** In (a) the QR's LAN address already works
away from home. In (b) the QR does not list the router's `100.101.1.1`,
because the daemon cannot know it. Two options (open question 17.5):

- a config key `web.advertise` (for example `["100.101.1.1:7777"]`), set
  once on the setup page under "Other addresses", that the QR then lists;
- or "Add address" in the app's Settings for that daemon. The pin and the
  key carry over, so no re-pairing.

---

## 15. Test plan

### 15.1 Phase 0 spikes (facts this doc cannot settle from here)

1. **ATS and a self-signed pin.** Does `URLSession` with ATS on accept a
   self-signed P-256 certificate when our delegate accepts the trust? Does
   `URLSessionWebSocketTask`? Test on a device against an IP address,
   over Wi-Fi and over Tailscale.
2. **Browser secure context.** Is a page on a self-signed certificate the
   user accepted a secure context, so `crypto.subtle` works? Check Safari,
   Chrome and Firefox. Does a non-extractable `CryptoKey` survive in
   IndexedDB across restarts in each?
3. **Tailscale LocalAPI on macOS.** Can the daemon reach the LocalAPI of
   the App Store Tailscale app, the standalone app and the open-source
   `tailscaled`? Can it fetch the `*.ts.net` certificate?
4. **Secure Enclave and notification actions.** Can the app sign with a
   "after first unlock" key in the background when a notification's
   Approve is tapped on a locked phone?

### 15.2 Go tests (no real network, no live daemon)

- Peeking listener: TLS and plain on one port; plain from non-loopback
  refused when old apps are off; `308` for pages, `426` for the API.
- Challenge and session: a good signature; a reused nonce; an old time; a
  signature by another key; a revoked device; an expired device; the
  ticket after a restart.
- Revocation closes live `/ws`, `/term` and `/events` connections within
  a second.
- Tiers: every route in 8.2 refused one tier below and allowed at its
  tier. A table test walks the route list, so a new route without a tier
  fails.
- Migration: the pin proof's MAC verifies with the token and fails
  without it; enroll with the token over TLS; the token refused over
  plain HTTP for enroll.
- "Allow old apps": turns off 30 days after the last use (fake clock);
  the QR drops `t` once it is off.
- Pair requests: rate limits, 3 pending, expiry, three denials, a
  control-tier phone cannot grant shell and files it does not hold.
- Tailscale: a fake LocalAPI: same user allowed, other user refused,
  tagged node refused, shared-in node refused, LocalAPI error refused,
  no LocalAPI skipped, LAN source never checked.
- Limits: the 33rd non-loopback connection refused while loopback still
  works; the fifth `/term` channel of one device refused; slow handshakes
  dropped after 10 seconds.
- Host and Origin: a rebinding `Host` on loopback refused; a cross-site
  `Origin` on `/ws` refused.
- Logs: no event carries a ticket, token, code or signature (a test greps
  the test log for the secrets it used).

### 15.3 Router forwarding

- **Go test.** A TCP forwarder in the test sits between the client and
  the daemon. A test hook makes the daemon classify the forwarder's
  address as `lan`. The client connects to the forwarder's address with
  the pin, enrolls, opens `/ws` and `/term`, and opens a shell with the
  shell tier. The test also runs with the Tailscale check on and a fake
  LocalAPI, and checks that it is not called for the LAN source.
- **iOS unit test.** The pin delegate accepts a certificate whose names
  match neither the host nor the IP, and refuses one with another key.
- **Manual check, once per release that touches this.** A router with
  Tailscale (OpenWrt or GL.iNet), the computer without Tailscale:
  1. Pair at home by QR.
  2. Subnet router with SNAT, phone on cellular: voice, an agent window,
     a shell, an attachment.
  3. The same without SNAT.
  4. Port forward from the router's `100.x` address, after adding that
     address.
  5. Revoke the phone on the setup page: the app shows the revoked
     message within a second.
  6. The log shows `remote_kind: lan` and the device id, and no
     `web.tailscale.denied`.

### 15.4 Certificate rotation

- Go test with a fake clock: start a rotation; a client that connects in
  the 7-day window gets `next_pin`; after the switch it connects with the
  new key; a client that did not connect fails the pin check.
- Go test: re-issuing the certificate with the same key keeps the pin.
- iOS test with `StubDaemon`: a pin mismatch on every address shows the
  "scan again" message with the right reason, and sends no request.
- iOS test: scanning a QR with the same daemon id and a new pin re-binds
  the device; with a new daemon id it replaces the entry and deletes the
  old key.

### 15.5 Compatibility

- Today's app (0.1.14) against the new daemon, old apps allowed: pairs
  from the new QR, works over plain HTTP, its shell follows `web.shell_lan`.
- The new app against today's daemon: works over plain HTTP until
  phase 10.
- The new app with an old pairing against the new daemon: migrates with
  no user action; afterwards the Keychain holds no token, and the stub
  daemon never saw the token over plain HTTP.

---

## 16. Phases (each bullet is one PR)

1. **`feat(web): TLS identity on the same port`.** Daemon id, key and
   certificate in `<data_dir>/tls/`; the peeking listener; the `security`
   block in `/api/hello`; the `Host` check on loopback. Plain HTTP still
   accepted everywhere. Files: `internal/web/server.go`, new
   `internal/web/tlsid.go`, `internal/web/auth.go`.
2. **`feat(web): device keys and signed sessions`.** `devices.json`,
   enroll by code or token, challenge and session, tickets, revocation
   that closes connections, `device_id` on events. The QR gains `d`, `k`
   and `e`. Every device gets all tiers for now. Files: new
   `internal/web/devices.go`, `internal/pair`, `internal/web/pair.go`,
   [ws-contract.md](../ws-contract.md).
3. **`feat(ios): pinned TLS and a Secure Enclave device key`.** The pin
   delegate, the key, `PairingLink` reads `d`, `k` and `e`, the
   per-daemon Keychain entry, the migration in 7.2, the "scan again"
   messages. Plain HTTP stays for old daemons. Files:
   `ios/Sources/SheepdogKit/DaemonClient.swift`, `PairingLink.swift`, new
   `DeviceKey.swift`.
4. **`feat(web): devices list on the setup page`.** The list, rename,
   revoke, "Disconnect all", 90-day expiry, "Allow old apps" and its
   automatic off. Files: `internal/web/setup.go`, `web/setup/`,
   [CONFIG.md](../../../CONFIG.md) (`web.legacy_token`).
5. **`feat(web): capability tiers`.** Tier per route, `403` with `need`,
   per-device shell, the migration mapping in 7.3; the app shows "Not
   allowed on this device" states (a small `feat(ios)` PR beside it).
6. **`feat(web): connection limits and rate limits`.** The caps in
   section 10, the handshake timeout, voice take-back with `by`.
7. **`feat(web): browser pairing`.** Pair requests, WebCrypto keys in
   `/voice` and `/debug`, the approval on the setup page. Files:
   `internal/web/pairreq.go`, `web/voice/`, `web/debug/`.
8. **`feat(ios): approve a new browser`.** The `pair-request` sheet,
   scanning the browser's QR, the notification with an unlock-required
   Allow.
9. **`feat(web): Tailscale identity and ts.net certificate`.** LocalAPI
   discovery, `whois` with a cache, the SNI choice, daily renewal, the
   setup page's `*.ts.net` link.
10. **`feat(ios): drop the ATS exception`.** Only once the daemon release
    with phase 2 has been out long enough (open question 17.2). The app
    then needs that daemon version and says "Update sheepdog on your
    computer" to older ones. File: `ios/project.yml`.
11. **`chore(web): retire the shared token off loopback`.** After the
    transition: `t` leaves the QR for good, `web.shell_lan` goes, and
    `?token=` is loopback-only.
12. *(Only if chosen)* **`feat: Face ID step-up for shell, files and close`**
    (variant B in section 9).

User docs ([remote-access.md](../../remote-access.md),
[iphone-app.md](../../iphone-app.md), [privacy.md](../../privacy.md)) change
in the phase that changes the behaviour, not before.

---

## 17. Open questions

1. **Face ID.** Offer it at all? If yes, variant A (app only) or B
   (daemon-enforced, recommended)? Off by default?
2. **How long old apps keep working.** Is 30 days after the last token
   use right? When may the app drop the ATS exception and stop talking to
   daemons without TLS?
3. **Public `*.ts.net` certificates.** They put the machine name and
   tailnet name into public Certificate Transparency logs. Use them
   automatically when Tailscale HTTPS is on (as agreed), or ask once on
   the setup page?
4. **Other users on the same tailnet.** Same user only (as agreed), or an
   allowlist for a family or team tailnet? What about tagged devices?
5. **Router port forward.** For case (b), a `web.advertise` setting on the
   setup page, "Add address" in the app, or both? Either is one extra step
   for that setup.
6. **Debug and hook routes.** Make `/api/inject/*`, `/api/herdr/event`,
   `/api/prompts/reload`, `/api/brain/*` and `/api/context/*`
   loopback-only? `/debug` from another computer would then lose those
   panels.
7. **Browser names.** A browser cannot tell "MacBook Air" from "Mac". Is
   "Mac · Safari" with an optional name field enough?
8. **Default tiers for browsers.** view, approve and control, with shell
   and files as a switch on the approval? Or approve-only by default?
9. **Certificate lifetime.** 10 years with a key change only on request,
   or a yearly automatic rotation through the 7-day `next_pin` window?
10. **"Disconnect all".** Should it also replace the TLS key and
    `web.token`, as a full reset after a suspected compromise? That makes
    every device scan again even if it has the newest app.

---

## 18. Room for a future relay

Out of scope now. A computer that cannot run Tailscale at all, with no
router to help, could open an outbound connection to a relay (for example
a Cloudflare Worker, like the push relay in
[relay/README.md](../../../relay/README.md)), and the phone would reach it
there.

What carries over unchanged:

- **The pin.** If the relay forwards raw bytes, the app runs TLS to the
  daemon through it and checks the same pin. The relay sees only
  ciphertext, as the push relay already does.
- **The device keys.** The signed challenge is bound to the daemon id and
  pin, not to a TLS session or an address, so it works through any path.
- **Tiers, revocation and the audit log** live in the daemon, so they do
  not change.

What would be new: browsers cannot run TLS inside a WebSocket themselves,
so a browser through the relay would need an end-to-end layer of its own
(for example a Noise handshake authenticated by the same device key). The
relay would also need its own rate limits and a way to find a daemon by
its id without learning anything else.
