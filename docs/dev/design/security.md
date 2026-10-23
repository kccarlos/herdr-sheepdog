# Security: pairing keys, encryption and device access

Status: **decided** (2026-10-21). Nothing here is built yet. Section 15
lists the phases; section 16 records how each open question was closed.

**Goal.** Make the phone and browser connection to the daemon safe on any
network, with **no extra steps for the user**. Pairing stays one QR scan.
Everything below rides on that scan.

**Today** ([remote-access.md](../../remote-access.md),
[ws-contract.md](../ws-contract.md#auth)), all of which this design
replaces:

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

**No compatibility.** The new design fully replaces the old one. There is
no shared token, no migration path, no "Allow old apps" switch and no code
for old apps or old daemons. An app or daemon from before this design
simply stops talking to the other side; the user updates both and scans
the QR once more.

**Decisions**

| Question | Decision |
|---|---|
| Encryption | TLS on every address except loopback. The daemon makes its own self-signed certificate. The pairing QR carries its fingerprint (an SPKI pin). The app pins it by fingerprint, never by hostname or IP. Loopback stays plain HTTP for the local `/setup`, `/voice` and `/debug` pages. |
| Certificate lifetime | 1 year. Rotated automatically 7 days before it expires; paired devices learn the next pin in advance (section 4.4). |
| Browser certificate | The self-signed one by default; browsers show a one-time warning. The real Tailscale `*.ts.net` certificate is a setup-page switch, **off** by default, because it puts the machine name in public logs (section 3.3). |
| One port | The daemon keeps one port. It reads the first byte of each connection: `0x16` is a TLS handshake, anything else is plain HTTP. Plain HTTP is served only to loopback. |
| Device identity | Each phone and browser makes its own **P-256 keypair** at pairing. On iPhone the key lives in the Secure Enclave. In a browser it is a non-extractable WebCrypto key. The daemon records each device's public key. There is no shared token. |
| Proof of possession | A **signed challenge** that returns a short session ticket, not mutual TLS. Section 5 explains why. |
| Pairing QR | **One-time, expires in 10 minutes.** Generated only on the computer: the loopback setup page or `sheepdog pair`. The routes that make a QR are loopback-only. |
| New device alert | When a device pairs, a **push goes to every other paired device**: "New device paired: Pixel · Chrome. Not you? Open the computer's setup page to revoke." |
| Browsers and Android | They scan the **same QR** with the camera. It opens `/voice` in the browser, which enrolls a WebCrypto key. There is no "phone approves browser" flow. |
| Devices list | On the setup page: name, kind, last seen and revoke, plus two separate buttons, **Disconnect all devices** and **Reset pairing** (which also replaces the certificate and keys). |
| Shell | **On by default.** One switch per computer, on the setup page. It applies to every device paired with that computer. There are no per-device tiers and no per-network rule. |
| Loopback-only routes | Pairing, device management, setup, debug and internal routes (hooks, injection, prompt reload, brain and context inspection). The composer's `/api/inject/utterance` is renamed to `/api/utterance` and stays open to paired devices. |
| Removing a computer in the app | Also revokes this phone's key on that computer, best effort (`DELETE /api/devices/self`). |
| Limits | Voice: one client, with take-back. Terminals: 4 per device, 8 per daemon. About 32 connections in total; loopback is not counted. |
| Router forwarding | Required scenario. Pins and device keys never depend on the source address. Subnet routing needs nothing; a port forward needs `web.advertise` or "Add address" in the app (section 13). |
| Not done | No Tailscale identity check. No Face ID or other biometrics for risky actions. |
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
   photo works forever. With this design the QR works once, for 10
   minutes, and every other device hears about its use.
3. **A lost or stolen phone.** The user must be able to cut off one device
   without re-pairing the others.
4. **Other devices on the tailnet.** Nodes shared in from another
   tailnet, other users of a family or team tailnet, a compromised
   smart-home box with a tailnet address. They can reach the port, but
   without a paired device key they get nothing beyond `/api/hello`.
5. **Malicious web pages in the user's own browser.** Cross-site request
   forgery, cross-site WebSocket hijacking, and DNS rebinding against
   `127.0.0.1:7777`.
6. **Floods and guessing.** Someone on the LAN who opens many sockets or
   guesses pairing codes.
7. **The push relay operator.** Already handled: notifications are
   end-to-end encrypted ([push.md](../push.md#end-to-end-encryption)).
   Nothing here changes that.

### What we do not defend against

- **Malware on the computer, or anyone with the user's login there.** It
  can read the daemon's data directory and keys, and talk to loopback.
  The agents themselves run as the user too.
- **Someone using the computer's unlocked session.** They can open the
  setup page and pair their own phone. The setup page is loopback-only,
  so this needs the computer itself, and the other devices get the
  new-device push.
- **A compromised, unlocked phone.** The key cannot be copied off the
  phone, but code on the phone can use it. Revoking the device stops it.
  There is no Face ID gate, by decision.
- **Breaks in TLS, WireGuard, the Secure Enclave or the browser.**
- **Traffic analysis.** Packet sizes and timing still show when the user
  talks or types.
- **A browser's first visit on a hostile network.** A browser cannot pin,
  so someone in the middle on that first visit can show a fake page that
  reads the QR's one-time code and enrolls itself first. Three things
  limit this: the code works once (the real browser then fails and says
  so), it expires in 10 minutes, and every other device gets the
  new-device push. The `*.ts.net` certificate (section 3.3) closes it for
  users who turn it on. The app is not affected: it pins from the QR.

---

## 2. The layers and what each stops

| Layer | What it is | What it stops | What it does not stop |
|---|---|---|---|
| Reach | `web.bind`, loopback default, LAN or Tailscale | Strangers on the internet (no port forwarding to the internet, ever) | Anyone on the same LAN or tailnet reaching the port |
| TLS with a pin | Self-signed certificate, fingerprint pinned from the QR | Sniffing and man-in-the-middle on the LAN, a fake daemon | Who the client is |
| One-time QR | Enroll code that works once, for 10 minutes, made only on the computer | A stolen QR photo used later | A thief who scans it within the 10 minutes (the push reveals it) |
| Device key | P-256 key per device, signed challenge, short session ticket | A leaked QR after use, a copied cookie (short-lived), one lost device among many, other tailnet nodes | A thief using an unlocked phone |
| New-device push | A push to every other device when one pairs | Pairing that goes unnoticed | A user who ignores it |
| Shell switch | One switch per computer on the setup page | A shell on a computer where the user does not want one | Misuse by a paired device while it is on |
| Loopback-only routes | Pairing, setup, debug and hook routes refuse non-loopback callers | A paired device or a LAN stranger injecting fake herdr events or reading prompts | Malware on the computer |
| Host and Origin checks | Allowed `Host` names, same-origin `Origin` on upgrades and writes | DNS rebinding, cross-site WebSocket hijacking, CSRF | — |
| Limits | Connection caps, handshake timeouts, rate limits | Floods, code guessing | A large distributed attack |
| Audit | Device id on events, metadata only | Not knowing which device did what | — |
| Rails | Unchanged ([roles.md](../roles.md)) | The orchestrator acting without the user's words | — |

---

## 3. Encryption

### 3.1 The daemon's identity

- At first start the daemon makes:
  - a **daemon id**: 16 random bytes, base64url. It is also the computer
    id the app keys everything by;
  - an **ECDSA P-256 key** and a **self-signed certificate** for it,
    signed with SHA-256, **valid for 1 year**. It has no meaningful names:
    nobody checks them.
- They live in `<data_dir>/tls/` (directory mode 0700, files 0600):
  `daemon_id`, `key.pem`, `cert.pem`, and during a rotation `next_key.pem`
  and `next_cert.pem`.
- The **pin** is `base64url(SHA-256(SubjectPublicKeyInfo))` of that key.
  Each yearly rotation makes a new key, so a new pin (section 4.4).
- Losing the data directory means a new daemon id, key and pin.
  Pressing **Reset pairing** replaces the key and pin but keeps the daemon
  id, so the app still knows which computer it is. Either way every
  device has to scan again (section 4.3).

### 3.2 One port, two protocols

- The listener peeks at the first byte of each new connection.
  - `0x16` (TLS handshake): serve TLS 1.2 or newer, ECDHE only.
  - Anything else: plain HTTP.
- **Loopback** (`127.0.0.0/8`, `::1`): plain HTTP. The local `/setup`,
  `/voice` and `/debug` pages, the herdr hooks and the CLI keep working
  with no TLS. TLS on loopback also works, but nothing needs it.
- **Non-loopback plain HTTP** is never served:
  - a page route (`GET /`, `/voice`, `/debug`): `308` to the same path on
    `https://` at the same host and port;
  - an API route or an upgrade: `426 {"error":"tls_required"}`.
- The port number stays the same.

### 3.3 Which certificate a client gets

- **No SNI, or an IP address** (what the app sends): the self-signed
  certificate.
- **SNI equal to the machine's `*.ts.net` name**, only when the setup
  page's **"Use a Tailscale certificate for browsers"** switch is on: the
  real certificate, fetched from the Tailscale LocalAPI (the same call
  `tailscale cert` uses). The daemon checks once a day and renews it when
  Tailscale does.
- The switch is **off by default**. Its copy says: "Browsers on other
  computers open sheepdog with no warning. Tailscale's certificates are
  public: this computer's name and your tailnet's name get logged in
  public certificate logs." The switch is hidden when Tailscale or its
  HTTPS feature is not available.
- The app **never pins the `*.ts.net` certificate**: it rotates about
  every 90 days and its key can change. The app always connects by IP
  and pins the self-signed key.
- With the switch on, the QR's browser link uses the `*.ts.net` name, so
  browsers show no warning. Otherwise browsers use the IP address and
  accept the self-signed certificate once.

### 3.4 The app checks the pin

- `DaemonClient` gets a `URLSession` delegate. For a server-trust
  challenge it:
  1. takes the leaf certificate's public key;
  2. hashes its SubjectPublicKeyInfo with SHA-256;
  3. accepts if it equals the stored pin (or the stored next pin,
     section 4.4), and refuses otherwise.
- The hostname and IP are not checked. That is what makes router
  forwarding work (section 13).
- The same delegate serves `URLSessionWebSocketTask`, so `/ws` and
  `/term` are pinned too.
- The pin is checked during the TLS handshake, before any request. A
  wrong pin sends nothing: no hello, no ticket, no audio.
- The app refuses `http://` to any non-loopback address. It keeps the
  ATS exception `NSAllowsArbitraryLoads`, because ATS cannot express a
  pin: even after the delegate accepts the pinned key, ATS runs its own
  system trust check and fails a self-signed certificate on a Tailscale
  or LAN address (`NSURLErrorDomain -1200`, "ATS failed system trust"),
  and exception domains cannot name IP addresses. The pin is the check.
  `NSAllowsLocalNetworking` must stay unset: when present, iOS ignores
  `NSAllowsArbitraryLoads`.

---

## 4. Pairing flows

### 4.1 The QR

The setup page (loopback only) and `sheepdog pair` show it. Each showing
makes a fresh enroll code; showing a new one invalidates the previous one.

```
https://100.101.102.103:7777/voice#pair=1&d=<daemon id>&n=<computer name>&k=<pin>&e=<enroll code>&u=100.101.102.103:7777&u=192.168.1.20:7777
```

- It is an `https://` link to `/voice`, so a phone camera or an Android
  phone opens it in the browser (section 4.2). The app's scanner reads
  the same link and never opens the browser (section 4.1.1).
- Everything after `#` is a fragment: browsers do not send it to the
  server, so it never lands in an access log or a proxy.
- The host is the first address: the Tailscale address when there is
  one, else the LAN address, or the `*.ts.net` name when that switch is
  on. `web.advertise` addresses are listed in `u` after the daemon's own.
- `d`: the daemon id. `n`: the computer's name from the daemon config.
  `k`: the pin.
- `e`: the one-time **enroll code**, 16 random bytes. It works once and
  expires 10 minutes after the QR is shown. The setup page shows the
  countdown and a "New code" button; `sheepdog pair` prints the expiry
  time.

#### 4.1.1 Phone or iPad

1. The user scans the QR in the app (the first-run screen or Settings ▸
   Connection ▸ Scan a new code). If the iPhone Camera app opens it in
   Safari instead, the `/voice` page does **not** enroll yet: it offers
   "Open in the Sheepdog app" (a `sheepdog://pair` link with the same
   fragment) first, and "Use in this browser" second (section 4.1.2).
2. The app tries the addresses, Tailscale first, with TLS pinned to `k`.
   It keeps the first that answers `GET /api/hello`. A pin mismatch on
   one address counts as "did not answer" for that address.
3. The app creates a key in the Secure Enclave, tagged with the daemon
   id (section 6).
4. The app calls `POST /api/devices/enroll` with the enroll code, its
   public key, its name (the system gives "iPhone"; the user can rename
   it on the setup page) and its kind (`ios`, `ipados`).
5. The daemon checks the code, burns it, records the device and answers
   `{device_id, daemon_id, name}`.
6. The app signs a challenge and gets its first session ticket
   (section 5).
7. The app saves the computer's entry: daemon id, name, addresses, pin,
   device id, key tag, paired date.
8. The daemon sends the new-device push to every other device
   (section 4.5).

The user sees what they see today: scan, a moment, connected.

#### 4.1.2 Browser or Android phone

1. The user points the camera at the QR, or opens the link on a laptop.
   The browser opens `https://<address>:7777/voice#pair=…` and warns once
   about the certificate, unless the `*.ts.net` switch is on.
2. The page reads the fragment, then removes it from the address bar
   (`history.replaceState`) so it does not stay in history.
3. On an iPhone or iPad (from the user agent), it stops here and shows
   two buttons: **Open in the Sheepdog app** and **Use in this browser**.
   The code is burnt only by whichever enrolls, so handing it to the app
   works. A self-hosted IP address cannot use Universal Links, so the app
   cannot take the link before Safari does. Other platforms go straight
   on.
4. It creates a non-extractable P-256 key with WebCrypto and stores it in
   IndexedDB (section 6).
5. It calls `POST /api/devices/enroll` with the enroll code, the public
   key, the kind (`browser`) and a name from the user agent, such as
   "Mac · Safari" or "Android · Chrome". The user can rename it on the
   setup page.
6. It signs a challenge and receives its session cookie (section 5), then
   shows the voice page.
7. The daemon sends the new-device push to every other device.

There is no 6-digit code, no request waiting for approval and no phone
approving a browser.

If the code was already used or has expired, the page says: "This code
was already used or has expired. Open the setup page on your computer and
scan the new code." The app says the same (see the table in 4.3).

### 4.2 Pairing a second computer

The app keeps one entry per daemon id. Scanning another computer's QR
adds a computer; it does not replace the first. Scanning a QR whose
daemon id the app already has replaces that entry's pin and addresses and
re-enrolls with the new code (section 4.3).

### 4.3 When the app must scan again

The pin no longer matches, or the daemon no longer knows the device. A
pin mismatch happens when sheepdog was reinstalled, **Reset pairing** was
pressed, its data directory was lost, or the computer was replaced. It
also happens if someone is intercepting the connection; the app cannot
tell the two apart, so it treats them the same.

1. The TLS handshake fails the pin check. The app sends nothing.
2. If every address fails the same way, the app stops retrying and
   shows:
   > **Scan the pairing code again.** This computer's security key
   > changed. That happens when sheepdog is reinstalled or pairing is
   > reset. If you did not do that, someone may be intercepting the
   > connection: do not continue on this network.
3. There is no "trust anyway" button.
4. The user scans a fresh QR. If the daemon id is new, the app replaces
   the old entry and deletes its key. If the daemon id is the same, the
   app replaces the pin and enrolls its existing key again with the new
   code.

The app shows a reason for every "scan again" case:

| Cause | Daemon says | App says |
|---|---|---|
| Pin mismatch | (nothing, TLS fails) | "This computer's security key changed…" (above) |
| Device revoked, or Disconnect all devices | `401 {"reason":"device_revoked"}` | "This phone was removed on your computer. Scan the pairing code again to reconnect." |
| Unknown device (data reset, same daemon id) | `401 {"reason":"unknown_device"}` | "Your computer no longer knows this phone. Scan the pairing code again." |
| Code used or expired | `410 {"reason":"code_used"}` / `{"reason":"code_expired"}` | "This code was already used or has expired. Show a new code on your computer and scan it." |
| Phone restored from a backup | (no key in the Secure Enclave) | "This is a new phone to your computer. Scan the pairing code again." |
| Rotation missed (section 4.4) | (nothing, TLS fails) | "This computer renewed its security certificate while this phone was away. Scan the pairing code again." |

### 4.4 Certificate rotation

The certificate lasts 1 year and rotates on its own.

1. **30 days before the switch** (37 days before expiry), the daemon makes
   the next key and certificate and keeps serving the current one.
2. From then on it announces `next_pin` to every authenticated client: in
   the session answer and in `bot-ready` (section 10).
3. Each app that connects in that window stores it next to the current
   pin. Browsers do not pin, so they need nothing.
4. **7 days before expiry**, the daemon switches to the new key. Apps
   that stored `next_pin` accept it and drop the old pin.
5. An app that did not connect in those 30 days sees a pin mismatch.
   Because it knows the old certificate's expiry date, it shows the
   "renewed while this phone was away" reason instead of the
   interception warning.

The 30-day lead time is this design's choice, not part of the decision
(which sets only the switch 7 days before expiry and that devices learn
the pin in advance); it is easy to change.

**Reset pairing** is the manual version: a new key at once, with no
window, so every device scans again.

### 4.5 The new-device push

- Sent by the daemon after every successful enroll, to every **other**
  paired device that has push registered, through the existing
  end-to-end encrypted relay ([push.md](../push.md)).
- Text: "New device paired: Pixel · Chrome." The daemon cannot know how
  many computers the phone has, so it never names itself; the app's
  notification service extension adds "with Home Mac" only when a second
  computer is paired. Tapping it
  opens a sheet: "Not you? Open the computer's setup page to revoke it."
  It names the device and the time; it has no revoke button of its own,
  because revoking is done on the computer.
- Devices without push (browsers, Android, an app without the app team's
  APNs key) get a `device-paired` message on `/ws` the next time they
  connect, shown as a banner with the same text.
- Logged as `web.device.enroll`; the push itself as `push.sent` as today.

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

   A browser does not know the pin for sure, so it signs an empty line
   there; the daemon checks it the same way.
3. `POST /api/auth/session {device_id, nonce, time, signature, client}`.
   The daemon checks:
   - the nonce is unused and fresh;
   - the time is within 5 minutes;
   - the device exists and is not revoked;
   - the signature is valid for the device's public key.
4. It answers `{ticket, expires_in: 43200, device: {id, name}, next_pin?}`.
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

**Loopback** needs no ticket. A request from `127.0.0.1` or `::1` with an
allowed `Host` (section 10.6) is the computer itself: the local pages,
the herdr hooks and the CLI.

### 5.2 WebSocket and SSE upgrades

- `/ws`, `/term` and `/events` authenticate **before the upgrade**, like
  any other request. `/ws` sends its `status` at once on connect, before
  any handshake ([ws-contract.md](../ws-contract.md#protocol-version-and-handshake)),
  so auth cannot wait for a first frame.
- The app sets the `Authorization` header on the upgrade request.
  `URLSessionWebSocketTask` allows that.
- A browser cannot set headers on a WebSocket or `EventSource`. It sends
  the cookie, which a same-origin upgrade carries.
- `?token=` goes away. No secret is ever put in a URL query, so none ends
  up in history, logs or a proxy.
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
  - A future relay may end TLS at the edge (section 17). An application
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

### 5.4 Intended exception: the model picker

`GET /api/brain/models` and `PUT /api/brain/model` stay open to paired
devices (with a ticket), not loopback-only like the other `/api/brain/*`
debug routes, because the app's Settings ▸ Models screen uses them. They
change which model the orchestrator uses, nothing else; a device that can
already steer agents gains nothing from them. The route table test pins
this.

### 5.5 The app side (phase 4)

The app keeps one record per paired computer in the keychain (daemon id,
name, addresses, pin, next pin, device id, paired date) and the key in
the Secure Enclave (`DeviceKey`, a software key on the Simulator). One
process-wide `DeviceCredentials` holds the pins every `URLSession` checks
(`PinnedTrustDelegate`: HTTP, `/events`, `/ws`, `/term`, push, intents)
and the ticket; sign-in is single-flight, refreshed 30 minutes before
expiry and once more on `401 no_session`/`session_expired`. `401
device_revoked`/`unknown_device` and a key that matches no pin open
"Please scan again". The ATS setting is `NSAllowsArbitraryLoads`
(see section 4: ATS would otherwise reject the pinned self-signed
certificate). `PairingE2ETests` and `PairingLinkUITests.testPairingWalkthrough`
run the whole path against the pair fixture daemon.

## 6. Key storage

| Platform | Device key | Other data | Notes |
|---|---|---|---|
| iPhone, iPad | Secure Enclave P-256 (`kSecAttrTokenIDSecureEnclave`), `.privateKeyUsage`, accessible after first unlock, this device only. One key per daemon, tagged `sheepdog.device.<daemon id>`. | Daemon id, name, addresses, pin, next pin, certificate expiry, device id, paired date in the Keychain (this device only). The ticket in memory only. | "After first unlock" lets a notification's Approve work while the phone is locked. The key is not in backups and not in iCloud Keychain; a restored or new phone pairs again. |
| iOS Simulator | Software P-256 key in the Keychain, same attributes | Same | No Secure Enclave. The enroll call says `hardware: false`. |
| Browser | WebCrypto ECDSA P-256, `extractable: false`, `["sign"]`, stored as a `CryptoKey` in IndexedDB, keyed by daemon id | Device id in IndexedDB; ticket in the `HttpOnly` cookie | Per origin: `https://192.168.1.20:7777`, `https://100.x:7777` and `https://mac.tailnet.ts.net:7777` are three devices. Private windows keep nothing. Clearing site data, or Safari's tracking prevention after long disuse, removes the key; the browser scans a new QR. |
| Daemon | Not applicable | `<data_dir>/tls/` (TLS key, certificate, next key during rotation, daemon id), `<data_dir>/devices.json` (0600: id, name, kind, public key, created, last seen, last network kind, hardware flag). | The push device list gets a `device_id` link, so revoking a device also drops its push registration and content key. `web.token` is removed from the config. |

Push content keys are already per phone and stay as they are
([push.md](../push.md#end-to-end-encryption)).

---

## 7. Devices and the shell switch

### 7.1 The devices list

On the setup page (loopback only), a "Devices" section:

- Each row: **name** (editable), **kind** (iPhone, iPad, Android ·
  Chrome, Mac · Safari), **last seen** (with the network: Tailscale,
  Wi-Fi, this computer) and **Revoke**. Revoke drops the device's
  tickets, closes its connections and removes its push registration.
- **Disconnect all devices.** Revokes every device. The certificate,
  pin and daemon id stay, so the next QR scan is an ordinary pairing.
  Confirm: "Every phone and browser will need to scan the code again."
- **Reset pairing.** Revokes every device **and** replaces the TLS key,
  certificate and pin; the daemon id stays. Use it after a suspected compromise.
  Confirm: "Every phone and browser will need to scan the code again,
  and each will show a security warning until it does."
- An empty list says: "No devices yet. Scan the code above with the
  Sheepdog app or your phone's camera."

A device can also unpair itself (`DELETE /api/devices/self`). The app
calls it, best effort, when the user removes a computer in Settings ▸
Connection ▸ Remove: if the computer is offline the app removes its entry
anyway, and the stale record stays on the computer until it is revoked
there.

There is no automatic expiry of unused devices.

### 7.2 The shell switch

- **"Shell from your phone"** on the setup page, **on by default**, one
  per computer (config `web.shell`, documented in
  [CONFIG.md](../../../CONFIG.md) when it is built).
- It applies to every paired device of that computer, on every network.
  The old "on this network" wording and `web.shell_lan` go away.
- When it is off, `/term`'s shell target answers `refused:shell_off`, and
  the app hides "Open shell here" and the shell window for that computer.
  Agent windows (`/term` on an agent pane) stay available.
- Attachments follow the same switch, since they write files on the
  computer.

### 7.3 Loopback-only routes

These refuse any non-loopback caller with `403 {"error":"loopback_only"}`:

- setup: `/setup`, `/api/setup/*`;
- pairing: `/api/pair`, `/api/pair/qr.svg` (the QR holds an enroll
  code);
- device management: `GET /api/devices`, `PATCH /api/devices/{id}`,
  `DELETE /api/devices/{id}`, `POST /api/devices/disconnect-all`,
  `POST /api/devices/reset-pairing`;
- debug and internal: `/api/inject/*` (blocked, pane, done, state, screen,
  remove), `/api/herdr/event` (the herdr hooks), `/api/prompts/reload`,
  `/api/brain/*`, `/api/context/*`, `/testdata/*`;
- the `/debug` page itself.

`/debug` from another computer loses those panels; that is intended.

**The composer route is renamed.** The voice page and the app type to
the orchestrator through `POST /api/inject/utterance` today. It becomes
`POST /api/utterance`, open to paired devices, so the loopback sweep does
not break typing. The old path is removed, not aliased.

Every other route is open to any paired device.

---

## 8. Connection limits

| What | Limit | Today |
|---|---|---|
| Voice on `/ws` | One client. A new one replaces the old one. The replaced client sees "Voice moved to Mac · Safari" and a **Take back** button, which simply reconnects. | One client, replaced |
| `/term` channels | 4 per **device** (was per connection), 8 per daemon | Done (`termServer.reserve`) |
| All connections | About 32 open connections from non-loopback addresses. Loopback is not counted, so the setup page always opens. | Done (`capListener`, 32) |
| Unauthenticated connections | 8 per source address; TLS handshake and request headers within 10 seconds | No limit |
| Challenges | 30 a minute per source address. After 10 bad signatures in a minute, that address waits 5 minutes. | Done (`429 rate_limited`) |
| Enroll codes | 5 wrong codes in a minute from one address: it waits 5 minutes | Done (`429 rate_limited`) |

---

## 9. Logging and audit

Metadata only. Never a key, a signature, a ticket or an enroll code.

- New events:
  - `web.device.enroll {device_id, kind, name, hardware, remote_kind}`;
  - `web.device.session {device_id, remote_kind}` (each new ticket);
  - `web.device.auth_failed {reason, remote, device_id?}`;
  - `web.device.revoke {device_id, by}` (`by`: `setup`, `self`),
    `web.device.disconnect_all {count}`, `web.device.reset_pairing {count}`;
  - `web.pair.code {expires}` (a new QR shown), `web.pair.code_refused {reason, remote}`;
  - `web.tls.created {pin, expires}`, `web.tls.rotate {old_pin, new_pin, phase}`,
    `web.tls.tsnet {name, expires}`;
  - `web.loopback_only {path, remote}`;
  - `web.limit {what, remote}` when a cap or rate limit refuses
    something.
- Existing events gain `device_id`: `web.client.connect`, `web.client.hello`,
  `web.term.connect`, `web.term.open`, `web.shell.open`, `web.shell.close`,
  the agent action events, and the pending answer events. For loopback it
  is `"local"`.
- `remote_kind` keeps its values (`loopback`, `tailscale`, `lan`), so the
  log shows when a device came through a router.

---

## 10. Wire changes

These are sketches for review. The real contract goes into
[ws-contract.md](../ws-contract.md) and [term-contract.md](../term-contract.md)
when each phase is built, with their contract tests. Nothing keeps the old
shapes.

### 10.1 `GET /api/hello`

Still public. `auth` becomes `"device"`, and it gains:

```
"computer": {"id": "<daemon id>", "name": "Home Mac"},
"security": {
  "pin": "<base64url SHA-256 of the SPKI>",
  "cert_expires": "2027-10-21T00:00:00Z",
  "tailscale_https": "mac.tailnet.ts.net"
}
```

- `computer.name` comes from the daemon config; the app may rename it
  locally.
- `tailscale_https` appears only while that switch is on.
- The pin is a public key hash, safe to publish. The app never trusts this
  field; it trusts the QR's `k`.

### 10.2 New and changed routes

| Route | Auth | Purpose |
|---|---|---|
| `GET /api/auth/challenge` | public, rate-limited | A nonce |
| `POST /api/auth/session` | signature | A ticket (and a cookie for browsers) |
| `POST /api/devices/enroll` | one-time enroll code | Record a device's public key |
| `GET /api/devices/self`, `DELETE /api/devices/self` | ticket | See or unpair this device |
| `GET /api/devices`, `PATCH /api/devices/{id}`, `DELETE /api/devices/{id}`, `POST /api/devices/disconnect-all`, `POST /api/devices/reset-pairing` | loopback only | The setup page's list |
| `GET /api/pair`, `GET /api/pair/qr.svg` | loopback only | A fresh one-time QR |
| `POST /api/utterance` | ticket | Typed text to the orchestrator (was `/api/inject/utterance`) |
| `POST /api/login` | — | Removed with the token |

### 10.3 `/ws`

- Auth before the upgrade (5.2). Origin checked.
- `bot-ready` adds `device: {id, name}`, `computer: {id, name}`,
  `shell: true|false`, and `next_pin` during a rotation.
- New server messages:
  - `device-paired {device_id, name, kind, at}` to every other connected
    device;
  - `status` with reason `replaced` gains `by` (the new device's name),
    for the Take back button.
- New close codes: **4003** device revoked (with the reason text),
  **4004** session expired (sign again and reconnect).

### 10.4 `/term`

- Auth as `/ws`. `term-hello` is unchanged.
- The shell gate moves from the source address to the computer's shell
  switch: `refused:shell_off`.
- Channel limits per device (section 8).
- Close codes 4003 and 4004 as on `/ws`.

### 10.5 `/events`

- Header or cookie. `?token=` is removed.
- Revocation closes the stream.

### 10.6 REST routes

- Missing or bad ticket: `401` with a `reason` (`session_expired`,
  `device_revoked`, `unknown_device`), so the app can say why.
- Loopback-only routes from elsewhere: `403 {"error":"loopback_only"}`.
- On loopback, every route checks that `Host` is `127.0.0.1`, `::1` or
  `localhost` (today only `/setup` does). This stops DNS rebinding.

---

## 11. Several computers

Option A of the app redesign: each computer runs its own
daemon. Everything here is **per daemon, keyed by daemon id**, never by
address:

- one device key per daemon (`sheepdog.device.<daemon id>`), so revoking
  one computer's access does not touch another, and two computers cannot
  link the phone by its key;
- one pin, next pin, device id and address list per daemon;
- the push content key is already per daemon;
- the new-device push goes to the devices of the computer that was
  paired, not to other computers.

The daemon id also tells "a second computer at the same IP" apart from
"the same computer, reinstalled".

---

## 12. Removing a computer

Settings ▸ Connection ▸ Remove in the app:

1. The app calls `DELETE /api/devices/self` on that computer, with a
   short timeout. Success or not, it goes on.
2. It deletes the Keychain entry and the Secure Enclave key for that
   daemon id, and unregisters push for it.
3. If the call failed, the computer's devices list still shows the phone
   until it is revoked there; the key is gone from the phone, so the
   record is useless.

---

## 13. Router forwarding (required scenario)

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
- Nothing depends on the network kind: there is no Tailscale identity
  check and the shell switch is per computer.

**The address.** Subnet routing ((a), (c)) needs nothing: the QR's LAN
address already works away from home. A port forward (b) needs the
router's address, which the daemon cannot know. Both ways are offered:

- the config key `web.advertise` (for example `["100.101.1.1:7777"]`),
  also set on the setup page under "Other addresses", which the QR then
  lists (documented in [CONFIG.md](../../../CONFIG.md) when it is built);
- "Add address" in the app's Settings ▸ Connection for that computer. The
  pin and the key carry over, so no re-pairing.

---

## 14. Test plan

### 14.1 Phase 0 spikes (facts this doc cannot settle from here)

1. **ATS and a self-signed pin.** Does `URLSession` with ATS on accept a
   self-signed P-256 certificate when our delegate accepts the trust?
   **No** (found on iOS 26 over Tailscale, 2026-10-22): ATS still fails
   system trust, so the app keeps `NSAllowsArbitraryLoads`.
   `AppTransportSecurityTests` guards the setting.
2. **Browser secure context.** Is a page on a self-signed certificate the
   user accepted a secure context, so `crypto.subtle` works? Check Safari,
   Chrome (desktop and Android) and Firefox. Does a non-extractable
   `CryptoKey` survive in IndexedDB across restarts in each?
3. **Camera to browser.** Does the iPhone Camera app and Android's camera
   open an `https://<ip>:7777/voice#…` link, and does the fragment
   survive the certificate warning in Safari and Chrome?
4. **Tailscale certificate on macOS.** Can the daemon fetch the
   `*.ts.net` certificate from the App Store Tailscale app, the
   standalone app and the open-source `tailscaled`? Only needed for the
   optional switch.
5. **Secure Enclave and notification actions.** Can the app sign with an
   "after first unlock" key in the background when a notification's
   Approve is tapped on a locked phone?

### 14.2 Go tests (no real network, no live daemon)

- Peeking listener: TLS and plain on one port; plain from non-loopback
  refused; `308` for pages, `426` for the API.
- Enroll codes: a code works once; a second use is `code_used`; after 10
  minutes (fake clock) it is `code_expired`; a new QR invalidates the
  old code; `/api/pair` refused from non-loopback.
- Challenge and session: a good signature; a reused nonce; an old time; a
  signature by another key; a revoked device; the ticket after a restart.
- Revocation, Disconnect all devices and Reset pairing close live `/ws`,
  `/term` and `/events` connections within a second; Reset pairing
  changes the pin and keeps the daemon id.
- New-device push: enrolling device B sends one push to device A and
  none to B; a connected browser gets `device-paired`.
- Loopback-only: a table test walks every route; each loopback-only route
  refuses a non-loopback caller, every other route refuses an
  unauthenticated one, so a new route without a rule fails.
  `/api/utterance` works for a paired device; `/api/inject/utterance` is
  gone.
- Shell switch: off refuses the shell target with `refused:shell_off` and
  refuses attachments; agent panes still open.
- Limits: the 33rd non-loopback connection refused while loopback still
  works; the fifth `/term` channel of one device refused; the ninth of the
  daemon refused; slow handshakes dropped after 10 seconds; voice
  take-back carries `by`.
- Host and Origin: a rebinding `Host` on loopback refused; a cross-site
  `Origin` on `/ws` refused.
- Logs: no event carries a ticket, code or signature (a test greps the
  test log for the secrets it used).

### 14.3 Router forwarding

- **Go test.** A TCP forwarder in the test sits between the client and
  the daemon. A test hook makes the daemon classify the forwarder's
  address as `lan`. The client connects to the forwarder's address with
  the pin, enrolls, opens `/ws` and `/term`, and opens a shell.
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
  6. The log shows `remote_kind: lan` and the device id.

### 14.4 Certificate rotation

- Go test with a fake clock: at expiry − 37 days the next key appears and
  `next_pin` is announced; at expiry − 7 days the daemon serves the new
  key; a client that stored `next_pin` connects; one that did not fails
  the pin check.
- iOS test with `StubDaemon`: a pin mismatch on every address shows the
  "scan again" message with the right reason (interception warning, or
  "renewed while away" after the old expiry date), and sends no request.
- iOS test: scanning a QR with the same daemon id and a new pin re-enrolls
  the existing key; with a new daemon id it replaces the entry and
  deletes the old key.
- The fixture-daemon UI tests (GitHub Actions) reach their daemon on
  loopback over plain HTTP, which stays allowed after phase 4: the app's
  plain-HTTP refusal and ATS both exempt loopback.
- iOS test: Remove calls `DELETE /api/devices/self` and deletes the key
  even when the stub refuses the call.

### 14.5 Browser pairing

- Web test (`make test-web`): the `/voice` page reads the fragment,
  clears it from the address bar, enrolls with a fake daemon and shows
  the used-or-expired message for a `410`.
- Web test: with an iPhone user agent the page shows the two buttons and
  sends no enroll until "Use in this browser" is tapped; "Open in the
  Sheepdog app" links to `sheepdog://pair` with the same fragment.

---

## 15. Phases (each bullet is one PR)

Each phase lands whole; there is no transition release. Old apps and old
daemons stop working at phase 3, which is fine with one tester.

1. **`feat(web): TLS identity on the same port`.** Daemon id, 1-year key
   and certificate in `<data_dir>/tls/`; the peeking listener with plain
   HTTP only on loopback; `computer` and `security` in `/api/hello`; the
   `Host` check on loopback. Files: `internal/web/server.go`, new
   `internal/web/tlsid.go`.
2. **`feat(web): loopback-only routes and /api/utterance`.** The route
   sweep in 7.3 and its table test; the composer route renamed in the
   daemon, `/voice` and the app together. Files: `internal/web/server.go`,
   `web/voice/`, `ios/Sources/SheepdogKit/DaemonClient.swift`.
3. **`feat(web): device keys, one-time QR and signed sessions`.**
   `devices.json`, the one-time enroll code, challenge and session,
   tickets, revocation that closes connections, `device_id` on events,
   the new QR. `web.token`, `/api/login`, `?token=` and `web.shell_lan`
   are deleted in the same PR. Files: new `internal/web/devices.go`,
   `internal/pair`, `internal/web/pair.go`, `internal/web/auth.go`,
   [ws-contract.md](../ws-contract.md), [CONFIG.md](../../../CONFIG.md).
4. **`feat(ios): pinned TLS and a Secure Enclave device key`.** The pin
   delegate, the key, `PairingLink` reads the new QR, the per-computer
   Keychain entry, the "scan again" messages, Remove revokes, the ATS
   exception removed, the token field removed. Files:
   `ios/Sources/SheepdogKit/DaemonClient.swift`, `PairingLink.swift`, new
   `DeviceKey.swift`, `ios/project.yml`.
5. **`feat(web): browser and Android pairing`.** WebCrypto keys in
   `/voice` from the QR fragment, the used-or-expired page, "Open in the
   Sheepdog app". Files: `web/voice/`.
6. **`feat(web): devices list, shell switch and new-device push`.** The
   list, rename, revoke, Disconnect all devices, Reset pairing, the
   `web.shell` switch, the push and `device-paired`. Files:
   `internal/web/setup.go`, `web/setup/`, `internal/web/push.go`; the
   app's new-device sheet in a small `feat(ios)` PR beside it.
7. **`feat(web): connection limits and rate limits`.** The caps in
   section 8, the handshake timeout, voice take-back with `by`.
8. **`feat(web): certificate rotation`.** The 30-day `next_pin` window,
   the switch 7 days before expiry; the app stores `next_pin`.
9. **`feat(web): port-forward addresses and the ts.net switch`.**
   `web.advertise` and "Other addresses" on the setup page, "Add address"
   in the app, and the off-by-default Tailscale certificate switch.

User docs ([remote-access.md](../../remote-access.md),
[iphone-app.md](../../iphone-app.md), [privacy.md](../../privacy.md)) change
in the phase that changes the behaviour, not before.

---

## 16. Closed questions

Every question the proposal left open is now decided (2026-10-21):

1. **Face ID.** Not done, in either variant.
2. **How long old apps keep working.** Not at all: there is no
   compatibility, no migration and no shared token. The app keeps the ATS
   exception (see section 4).
3. **Public `*.ts.net` certificates.** A setup-page switch, off by
   default, whose copy says the machine name gets logged publicly.
4. **Other users on the same tailnet.** No Tailscale identity check. A
   tailnet node needs a paired device key like anyone else.
5. **Router port forward.** Both: `web.advertise` (and "Other addresses"
   on the setup page) and "Add address" in the app. Subnet routing needs
   nothing.
6. **Debug and hook routes.** Loopback-only. The composer route is renamed
   to `/api/utterance` and stays open to paired devices.
7. **Browser names.** "Mac · Safari" or "Android · Chrome" from the user
   agent, renamed on the setup page if wanted. No name field at pairing.
8. **Default tiers for browsers.** There are no tiers. Every paired device
   has the same access; the shell is one switch per computer, on by
   default.
9. **Certificate lifetime.** 1 year, rotated automatically 7 days before
   expiry, with `next_pin` announced 30 days ahead.
10. **"Disconnect all".** Two separate buttons: **Disconnect all devices**
    (revokes devices, keeps the certificate) and **Reset pairing** (also
    replaces the certificate and keys, keeping the daemon id).

---

## 17. Room for a future relay

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
- **Revocation and the audit log** live in the daemon, so they do not
  change.

What would be new: browsers cannot run TLS inside a WebSocket themselves,
so a browser through the relay would need an end-to-end layer of its own
(for example a Noise handshake authenticated by the same device key). The
relay would also need its own rate limits and a way to find a daemon by
its id without learning anything else.
