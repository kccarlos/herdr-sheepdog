# Phone notifications (APNs)

The daemon sends a system notification to the iOS companion app when an
agent needs the user, even when the app is closed. This is the
contract between the daemon and the app; the code is `internal/push`
(sender, device registry), the hook is `internal/supervisor/notify.go`,
and the HTTP routes are `internal/web/push.go`.

## Configuration

`[push.apns]` in the config file, or the `SHEEPDOG_APNS_*` environment
variables (see [`../CONFIG.md`](../CONFIG.md)):

```toml
[push.apns]
key_path = "~/.config/sheepdog/apns/AuthKey_XXXXXXXXXX.p8"
key_id = "XXXXXXXXXX"
team_id = "GV776BZVA8"
topic = "com.github.kccarlos.herdr-sheepdog-ios"
```

All four are needed. With any one missing, or the key file unreadable or
not a P-256 PKCS#8 key, sending is disabled: `push.disabled` is logged at
startup with the reason, `GET /api/push/status` reports it, and devices
can still register. An APNs auth key is team-wide, so one key serves
every app of the team; a key from another team is refused by APNs with
`403 InvalidProviderToken`.

## Device registration (HTTP)

The same auth as every other `/api` route (`web.token`: bearer header or
the login cookie, [`ws-contract.md`](ws-contract.md) "Auth").

| Route | Body | Answer |
|---|---|---|
| `POST /api/push/register` | `{"token":"<hex device token>","environment":"production"\|"sandbox","device":"<optional name>"}` | `200 {"ok":true}`. Idempotent: the same token again updates its environment and name. `400` for a token that is not hex (64+ hex digits) or an unknown environment; `503` when the daemon has no push service. |
| `DELETE /api/push/register` | `{"token":"..."}` | `200 {"ok":true}`, also for an unknown token. |
| `GET /api/push/status` | — | `{"enabled":bool,"reason":"<why not, when disabled>","devices":N}` |

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

The provider token is an ES256 JWT (`kid` = key id, `iss` = team id,
`iat`), signed with `crypto/ecdsa` and reused for 50 minutes; a
`403 ExpiredProviderToken` mints a fresh one and retries once. Each
device is sent to its own environment's host (`api.push.apple.com` or
`api.sandbox.push.apple.com`) over HTTP/2. A `410 Unregistered` or
`400 BadDeviceToken` removes the device.

Events (source `push`): `push.enabled` / `push.disabled {reason}` at
startup, `push.registered`, `push.unregistered {reason}`, `push.sent`,
`push.failed {status, reason}`. A log line carries only the token's last
six characters (`token_tail`), never the whole token, and never anything
from the key file.
