# sheepdog push relay

A small, stateless Cloudflare Worker that forwards sheepdog's phone
notifications to Apple's push service (APNs). It holds the Apple key, so
a sheepdog daemon needs none, and it never sees what a notification says:
the daemon encrypts the content with a key only the paired phone has, and
the relay passes the ciphertext through. The architecture and the
privacy trade-offs are in [`../docs/dev/push.md`](../docs/dev/push.md);
the user-facing choice between the shared relay, your own key and your
own relay is in [`../docs/iphone-app.md`](../docs/iphone-app.md).

## What it sees

- The phone's APNs device token (sealed inside a relay token the daemon
  stores; the relay keeps nothing between requests).
- When a notification is sent, its size, and its kind (`approval`,
  `blocked`, …), which picks the notification's category and urgency.
- The daemon's IP address.

It never sees a title, an agent name, a sentence or a pending prompt. It
keeps no database and no KV store, and logs only the route and the
status code of each request.

## API

All bodies are JSON.

| Route | Body | Answer |
|---|---|---|
| `GET /v1/health` | — | `200 {"ok":bool,"version":"1.0.0"}`. `ok` is false while a secret is missing. |
| `POST /v1/register` | `{"device_token":"<hex>","environment":"production"\|"sandbox","bundle_id":"<the app's bundle id>"}` | `200 {"relay_token":"…"}`. `400` for a bad token or environment, or a `bundle_id` other than `APNS_TOPIC`; `413` over 1 KiB; `429` over 10 a minute from one IP. |
| `POST /v1/send` | `{"relay_token":"…","payload":{"ciphertext":"<base64>","nonce":"<base64, 12 bytes>","kind":"approval"?,"collapse_id":"…"?},"priority":10?}` | `200 {"ok":true}`; `410 {"error","reason"}` when APNs says the device token is gone (`Unregistered`, `BadDeviceToken`, `DeviceTokenNotForTopic`): drop the relay token; `502 {"error","status","reason"}` for any other APNs refusal; `401` for a relay token this relay did not issue or that was altered; `400` for a malformed body; `413` over 8 KiB; `429` over 20 a minute for one device. |

A relay token is `base64url(0x01 || iv || AES-256-GCM(SHA-256(RELAY_SECRET), {"v":1,"device_token","environment","issued_at"}))`.
Changing `RELAY_SECRET` invalidates every relay token; daemons that still
know the device token register again on the `401`.

The notification it sends to APNs:

```json
{
  "aps": {
    "alert": {"title": "Sheepdog", "body": "New update"},
    "mutable-content": 1,
    "sound": "default",
    "category": "APPROVAL",
    "interruption-level": "time-sensitive"
  },
  "e2e": {"c": "<ciphertext>", "n": "<nonce>"}
}
```

`category` is `APPROVAL` for `kind: approval` and `AGENT` for any other
kind; `interruption-level` is `time-sensitive` for `approval` and
`blocked`, `active` otherwise. Without a kind both are left out. The
app's notification service extension decrypts `e2e` and replaces the
placeholder text.

## Self-hosting

You need your own relay only if you build the iOS app under your own
bundle id and want the daemon to stay keyless. (With your own build you
can also skip the relay entirely: `push.mode = "apns"` with your key in
the daemon's `[push.apns]`, see [`../CONFIG.md`](../CONFIG.md).) An
APNs device token only works with a key from the team that signed the
app, so a self-hosted relay cannot serve the App Store build.

1. **Apple.** In your Apple Developer account, enable Push Notifications
   on your app's identifier and create an APNs auth key (Keys → +, Apple
   Push Notifications service). Download the `.p8` file once and note its
   Key ID and your Team ID.
2. **Cloudflare.** A free account is enough. In this directory:

   ```sh
   npm ci
   npx wrangler login
   ```

3. **Your bundle id.** Set `APNS_TOPIC` in `wrangler.toml` to your app's
   bundle id.
4. **Secrets** (stored by Cloudflare, never in the repository):

   ```sh
   npx wrangler secret put APNS_KEY < AuthKey_XXXXXXXXXX.p8
   npx wrangler secret put APNS_KEY_ID      # the 10-character Key ID
   npx wrangler secret put APNS_TEAM_ID     # your Team ID
   openssl rand -base64 32 | npx wrangler secret put RELAY_SECRET
   ```

5. **Deploy:** `npx wrangler deploy`. It prints the URL,
   `https://sheepdog-relay.<your-subdomain>.workers.dev`;
   `curl <url>/v1/health` should answer `{"ok":true,…}`.
6. **Point the daemon at it** in `~/.config/sheepdog/config.toml`, then
   restart it:

   ```toml
   [push]
   mode = "relay"

   [push.relay]
   url = "https://sheepdog-relay.<your-subdomain>.workers.dev"
   ```

   `sheepdog doctor` checks that the relay answers.

The rate limits are in `wrangler.toml` (`[[ratelimits]]`; Cloudflare
allows a period of 10 or 60 seconds only).

## Development

```sh
npm ci
npm test            # vitest in the Workers runtime (Miniflare)
npm run typecheck
npx wrangler dev    # local server; put test secrets in .dev.vars
```

The tests mock APNs and generate a throwaway key; nothing reaches Apple.
CI runs them on every change under `relay/`
(`.github/workflows/relay.yml`), and a push to `main` deploys the
shared relay when the repository has Cloudflare credentials.
