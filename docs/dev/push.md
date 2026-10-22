# Phone notifications (APNs)

The daemon sends a system notification to the iOS companion app when an
agent needs the user, even when the app is closed. This is the
contract between the daemon, the push relay and the app; the code is
`internal/push` (sender, device registry, relay client, end-to-end
sealing), the hook is `internal/supervisor/notify.go`, the HTTP routes
are `internal/web/push.go`, and the relay is `relay/` (a Cloudflare
Worker, [`../../relay/README.md`](../../relay/README.md)).

## Architecture

APNs accepts a notification only when it is signed with an auth key of
the Apple team that signed the app. The App Store build's key cannot be
handed to every user, so there are two delivery paths, picked by
`push.mode`:

```
relay (default):  daemon --(sealed payload + relay token)--> relay --(JWT, placeholder alert + e2e)--> APNs --> phone (extension decrypts)
apns:             daemon --(JWT, full payload)--------------------------------------------------------> APNs --> phone
```

- **`relay`**: the daemon holds no Apple key. It encrypts the notification
  with the phone's content key ("End-to-end encryption" below) and posts
  the ciphertext to the relay's `POST /v1/send` with the phone's relay
  token. The relay opens the relay token (AES-GCM under its own secret;
  it stores nothing), signs an ES256 provider token with the App Store
  team's key, and sends APNs a placeholder alert ("Sheepdog" / "New
  update", `mutable-content: 1`) carrying `e2e: {c, n}`. The app's
  Notification Service Extension decrypts `e2e` and replaces the alert
  with the real title, body and `sheepdog` dictionary. The relay sees the
  device token, the timing, the size and the kind, never the content.
- **`apns`**: the direct path, for a self-built app with your own key in
  `[push.apns]`; the payload goes to APNs in the clear ("Payload" below).
- **`off`**: nothing is sent; devices can still register.

With `push.mode` unset, a fully configured `[push.apns]` means `apns` (an
existing self-built setup keeps working), otherwise `relay` at
`push.relay.url` (default: the shared relay, the `DefaultRelayURL`
constant in `internal/config`). Until the shared relay's URL is filled in,
the relay path is disabled with that reason.

## Configuration

`[push]` and `[push.apns]` in the config file, or the `SHEEPDOG_PUSH_*`
and `SHEEPDOG_APNS_*` environment variables (see
[`../CONFIG.md`](../../CONFIG.md)):

```toml
[push]
mode = "relay"            # or "apns", "off"; unset: apns when [push.apns] is complete

[push.relay]
url = "https://sheepdog-relay.example.workers.dev"   # default: the shared relay
```

For `push.mode = "apns"`:

```toml
[push.apns]
key_path = "~/.config/sheepdog/apns/AuthKey_XXXXXXXXXX.p8"
key_id = "XXXXXXXXXX"
team_id = "GV776BZVA8"
topic = "com.github.kccarlos.herdr-sheepdog-ios"
```

All four are needed for the direct path. With any one missing, or the key file unreadable or
not a P-256 PKCS#8 key, sending is disabled: `push.disabled` is logged at
startup with the reason, `GET /api/push/status` reports it, and devices
can still register. An APNs auth key is team-wide, so one key serves
every app of the team; a key from another team is refused by APNs with
`403 InvalidProviderToken`.

## Device registration (HTTP)

The same auth as every other `/api` route (a paired device's session
ticket, [`ws-contract.md`](ws-contract.md) "Auth").

| Route | Body | Answer |
|---|---|---|
| `POST /api/push/register` | `{"token":"<hex device token>","relay_token":"<optional>","environment":"production"\|"sandbox","device":"<optional name>"}` | `200 {"ok":true,"content_key":"<base64, 32 bytes>","encryption":"aes-256-gcm"}`. Idempotent: the same token again updates its environment, name and relay token and answers the **same** content key. The registration belongs to the paired device whose ticket sent it: a device has one registration (a new token replaces its old one), and revoking the device drops it. A new device's pairing pushes every other registered device (kind `device_paired`: "New device paired to <computer>. Not you? Open the computer's setup page to revoke."). `token` may be left out when `relay_token` is given (then `environment` is optional). `400` for a token that is not hex (64+ hex digits), a relay token that is not base64url, or an unknown environment; `503` when the daemon has no push service. |
| `DELETE /api/push/register` | `{"token":"..."}` or `{"relay_token":"..."}` | `200 {"ok":true}`, also for an unknown token. |
| `POST /api/push/activity` | `{"token":"<hex activity push token>","environment":"production"\|"sandbox"}` | `200 {"ok":true}`. The voice session's Live Activity push token, for the paired device whose ticket sent it (one per device; a new one replaces it; revoking the device drops it). Kept in `push_activities.json` under the data directory (mode 0600) so a restart keeps updating the banner, at most 8 hours. `400` from loopback (no paired device) or for a bad token or environment. See "Live Activity updates". |
| `DELETE /api/push/activity` | — | `200 {"ok":true}`: the activity ended. |
| `GET /api/push/status` | — | `{"enabled":bool,"reason":"<why not, when disabled>","devices":N,"mode":"relay"\|"apns"\|"off"}` |

The app need not talk to the relay itself: in relay mode the daemon
trades a registered device token for a relay token (`POST /v1/register`
on the relay, with the app's bundle id: `[push.apns] topic`, else the App
Store build's) before the first notification, and keeps it. An app may
instead send a `relay_token` it got from the relay; the daemon uses it as
is. A relay token the relay refuses with `401` (another relay, or a
rotated relay secret) is renewed once from the device token; one without
a device token behind it is dropped.

## End-to-end encryption

Each registered phone has a **content key**: 32 random bytes the daemon
generates at the phone's first registration and answers (base64) in
every `POST /api/push/register` response, over the same authenticated
connection as the rest of `/api` (it is as private as the device's key). The
app keeps it where its Notification Service Extension can read it (the
shared app-group keychain). It lives in `push_devices.json` (mode 600)
with the device and goes when the device is unregistered or dropped. A
device registered before content keys existed gets one at its next
notification, and the app learns it at its next registration (every
launch).

For the relay path the daemon seals the same JSON object the direct path
sends as its body (`{"aps": …, "sheepdog": …}`, "Payload" below) with
AES-256-GCM under the content key, a fresh random 12-byte nonce and no
associated data. `POST /v1/send` carries
`payload.ciphertext` (standard base64 of the ciphertext with the 16-byte
tag appended) and `payload.nonce` (standard base64), and APNs delivers
them as `e2e.c` and `e2e.n`. In CryptoKit:
`AES.GCM.open(AES.GCM.SealedBox(nonce: n, ciphertext: c.dropLast(16), tag: c.suffix(16)), using: key)`.
The extension copies `aps.alert`, `aps.thread-id` and the `sheepdog`
dictionary from the plaintext into the notification it shows; if it
cannot decrypt, the placeholder stays. The sealed JSON is kept under
2400 bytes (the relay's 3500-character ciphertext cap keeps the APNs
payload under 4 KiB): a longer sentence is cut with an ellipsis.

In the clear, the relay gets `kind` (`approval`, `blocked`, `done`,
`idle`), because the category and interruption level an extension cannot
change follow from it (`approval` → `APPROVAL`, anything else → `AGENT`;
`approval` and `blocked` are `time-sensitive`, others `active`), and
`collapse_id` (the pending or event id, an opaque counter).

Until the app ships the extension, relay notifications show the
placeholder text, and Approve / Decline on them do nothing (the app
finds no `sheepdog` dictionary); a tap still opens the app.

TestFlight and App Store builds register `production`; Xcode debug builds
`sandbox` (the APNs environment the token was issued for). Tokens are
stored lowercased in `<data_dir>/push_devices.json` (mode 600).

## When a notification is sent

Once per worker event that needs the user, at the place the
supervisor decides an announcement's line (`codePath`, `applyResult`,
`dropLate` in `announce.go`, plus the block whose screen could not be
read):

- a **blocked** agent: an approval prompt (`kind: approval`, with the
  pending action's options) or a block with nothing answerable
  (`kind: blocked`);
- a **kept promise** the user asked for ("tell me when auth
  finishes"): `kind` is the promise's event (`done`, `idle`, …).

Plain finishes, starts, status chatter and silent context are never
sent. The notification goes out whether or not a voice client is
attached and regardless of the chattiness level (a block is spoken at
every level anyway); the app suppresses the banner itself while it is in
the foreground. When the orchestrator's line about an event is skipped as stale
(the prompt was answered meanwhile) or the orchestrator decides to say nothing,
nothing is sent either: the phone hears exactly what the voice would.

Dedupe: the sender remembers the last 512 event ids (or pending ids when
no event id was minted) and sends each once, so a late orchestrator line about
an event already notified is not sent again.

## Payload

```json
{
  "aps": {
    "alert": {"title": "auth", "body": "auth asks to run git push. Yes or no?"},
    "sound": "default",
    "thread-id": "w5:p1",
    "interruption-level": "time-sensitive",
    "category": "APPROVAL"
  },
  "sheepdog": {
    "kind": "approval",
    "pane_id": "w5:p1",
    "agent": "auth",
    "pending_id": "ap-tm9bu0-1",
    "event_id": "evt-12",
    "options": [{"index": 1, "label": "Yes, proceed"}, {"index": 2, "label": "No"}]
  }
}
```

- `aps.alert.title` is the agent's alias, or `Sheepdog` when it has none;
  `aps.alert.body` is the same spoken sentence the voice says.
- `aps.thread-id` is the pane id, so the app groups an agent's alerts.
- `aps.category` is `APPROVAL` when there is a pending answer-prompt
  action (the app can answer it with
  `POST /api/pending/answer {"id": pending_id, "option": index}`),
  otherwise `AGENT`.
- `aps.interruption-level` is `time-sensitive` for needs-you events
  (blocked, approval) and `active` for a kept promise. Time-sensitive
  alerts need the app's
  `com.apple.developer.usernotifications.time-sensitive` entitlement;
  without it iOS delivers them as `active`.
- `sheepdog.pending_id` and `sheepdog.options` are present only for an
  approval; `sheepdog.event_id` when the supervisor minted one (with the
  orchestrator on).

Headers: `apns-push-type: alert`, `apns-topic: <topic>`,
`apns-priority: 10` for every alert (a kept promise was asked for, so it is not left to power batching),
`apns-collapse-id` = the pending id, else the event id (a later alert
about the same prompt replaces the earlier one).

## Delivery and errors

Relay path: `200` is `push.sent`; `410` (APNs said `Unregistered`,
`BadDeviceToken` or `DeviceTokenNotForTopic`) removes the device; `401`
renews the relay token as above; anything else (`502` with APNs' status
and reason, `429` from the relay's per-device rate limit) is
`push.failed {status, reason}` and keeps the device.

Direct path: the provider token is an ES256 JWT (`kid` = key id, `iss` = team id,
`iat`), signed with `crypto/ecdsa` and reused for 50 minutes; a
`403 ExpiredProviderToken` mints a fresh one and retries once. Each
device is sent to its own environment's host (`api.push.apple.com` or
`api.sandbox.push.apple.com`) over HTTP/2. A `410 Unregistered` or
`400 BadDeviceToken` removes the device.

Events (source `push`): `push.enabled {mode, …}` / `push.disabled {mode, reason}` at
startup, `push.registered`, `push.unregistered {reason}`, `push.sent`,
`push.failed {status, reason}`, `push.activity.registered`,
`push.activity.sent {event, priority, waiting}` and
`push.activity.failed {status, reason}`. A log line carries only the token's last
six characters (`token_tail`), never the whole token, and never anything
from the key file.

## Live Activity updates

The iPhone app shows the voice session as a Live Activity on the lock
screen: the voice's phase, up to three agents waiting for an answer with
their questions, and how many agents are working and done. While the app
runs it updates the banner itself. It requests the activity with a push
token and sends it to `POST /api/push/activity`, so the daemon can keep
the banner current once the app is away (suspended with the screen
locked, or closed).

- **When.** Every 5 seconds the daemon looks at each registered activity.
  While its device holds a connection (`/ws`, `/term`, `/events`), the app
  is in charge and the daemon only notes what the banner shows. After 30
  seconds without one, each change of the banner's content is pushed:
  phase `away` ("Not listening"), the waiting rows and the counts. A new
  waiting agent goes at priority 10, anything else at 5 (iOS rate-limits
  priority 10 per activity; priority 5 is not counted). Each update moves
  the stale date 30 minutes on. An activity older than 8 hours (iOS ends
  it) is forgotten, and so is one APNs reports gone (`410`).
- **Privacy.** The content state's `rows` (agent names, questions, pane
  ids) are sealed like a notification: `sealed {c, n}`, AES-256-GCM under
  the phone's content key, with `rows: []` in the clear. The relay and
  Apple see the phase and the three counts only. The widget opens the rows
  with the key in the App Group keychain; without a key (the phone never
  registered for notifications) the daemon sends the counts alone.
- **Relay path:** the activity token is traded for a relay token with
  `/v1/register` like a device token, then `POST /v1/activity` with
  `{relay_token, event, content_state, timestamp, stale_date, priority}`
  ([relay/README.md](../../relay/README.md)). A relay older than 1.1.0
  answers `404` and the banner simply stops updating while the app is
  away.
- **Direct path:** `apns-push-type: liveactivity`, topic
  `<topic>.push-type.liveactivity`, body `{"aps": {timestamp, event,
  content-state, stale-date}}`.

The content state is the app's `LiveActivityContent`
(`ios/Sources/SheepdogKit/LiveActivityContent.swift`); `ActivityState` in
`internal/push/activity.go` mirrors it.
