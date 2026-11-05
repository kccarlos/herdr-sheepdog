// Optional browser Web Push for /voice on Android, Linux and desktop
// browsers (docs/dev/push.md). Registers /voice/sw.js with scope /voice and
// posts the browser's RFC 8291 subscription to POST /api/push/web. Pure +
// dependency-injected so node --test covers it without a browser.

import { isAppleMobile, deviceName } from './pairing.js';

/** Decodes a base64url VAPID public key into a Uint8Array for PushManager.subscribe. */
export function vapidKeyBytes(b64u) {
  const pad = '='.repeat((4 - (b64u.length % 4)) % 4);
  const raw = atob(String(b64u).replace(/-/g, '+').replace(/_/g, '/') + pad);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

/** Reports whether this browser can use Web Push on /voice. */
export function pushSupport({ isSecureContext, hasSW, hasPushManager, hasNotification, ua, touchPoints }) {
  if (isAppleMobile(ua, touchPoints)) {
    return { supported: false, reason: 'apple_mobile' };
  }
  if (!hasSW || !hasPushManager || !hasNotification) {
    return { supported: false, reason: 'browser_unsupported' };
  }
  if (!isSecureContext) {
    return {
      supported: false,
      reason: 'insecure_context',
      message: 'Browser notifications need HTTPS (for example Tailscale HTTPS).',
    };
  }
  return { supported: true };
}

/** Turns a ServiceWorker / PushManager exception into a plain-English fix. */
export function explainPushError(err) {
  const name = (err && err.name) || '';
  const msg = String((err && err.message) || err || '');
  if (name === 'NotAllowedError' || /denied|not allowed/i.test(msg)) {
    return 'Notifications are blocked in this browser. Allow notifications for this site in your browser settings.';
  }
  if (name === 'SecurityError' || /certificate|ssl|tls|secure|trust/i.test(msg)) {
    return 'Browser notifications need a trusted HTTPS certificate (turn on Tailscale HTTPS on the setup page).';
  }
  if (name === 'AbortError' || /push service/i.test(msg)) {
    return 'The browser push service refused the subscription. Check that HTTPS uses a trusted certificate (such as Tailscale HTTPS).';
  }
  return msg || 'Could not enable browser notifications.';
}

/**
 * Registers /voice/sw.js, subscribes to Web Push with the daemon's VAPID
 * key, and registers the subscription with POST /api/push/web.
 */
export async function enableWebPush(deps) {
  let st;
  try {
    const r = await deps.fetch('/api/push/status', { cache: 'no-store' });
    st = await r.json().catch(() => ({}));
    if (!r.ok) return { ok: false, message: st.error || `Could not check push status (${r.status}).` };
  } catch (err) {
    return { ok: false, message: explainPushError(err) };
  }
  if (!st.web_push_enabled || !st.vapid_public_key) {
    return { ok: false, message: 'Notifications are turned off on the computer (push.mode = off).' };
  }

  let perm = deps.Notification.permission;
  if (perm === 'default' && typeof deps.Notification.requestPermission === 'function') {
    perm = await deps.Notification.requestPermission();
  }
  if (perm !== 'granted') {
    return { ok: false, message: explainPushError({ name: 'NotAllowedError' }) };
  }

  let reg;
  let sub;
  try {
    reg = await deps.swContainer.register('/voice/sw.js', { scope: '/voice' });
    sub = await reg.pushManager.getSubscription();
    if (!sub) {
      sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: vapidKeyBytes(st.vapid_public_key),
      });
    }
  } catch (err) {
    return { ok: false, message: explainPushError(err) };
  }

  const json = sub.toJSON ? sub.toJSON() : sub;
  if (!json || !json.endpoint || !json.keys || !json.keys.p256dh || !json.keys.auth) {
    return { ok: false, message: 'Browser returned an incomplete push subscription.' };
  }
  const res = await deps.fetch('/api/push/web', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      endpoint: json.endpoint,
      keys: { p256dh: json.keys.p256dh, auth: json.keys.auth },
      device: deviceName(deps.ua, deps.touchPoints),
    }),
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    return { ok: false, message: body.error || `The computer refused the subscription (${res.status}).` };
  }
  return { ok: true, endpoint: json.endpoint };
}

/** Unsubscribes this browser and removes its endpoint from the daemon. */
export async function disableWebPush(deps) {
  try {
    const reg = await deps.swContainer.getRegistration('/voice');
    const sub = reg && (await reg.pushManager.getSubscription());
    if (!sub) return { ok: true };
    const endpoint = sub.endpoint || (sub.toJSON && sub.toJSON().endpoint) || '';
    await sub.unsubscribe().catch(() => {});
    if (endpoint) {
      await deps.fetch('/api/push/web', {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ endpoint }),
      }).catch(() => {});
    }
    return { ok: true };
  } catch (err) {
    return { ok: false, message: explainPushError(err) };
  }
}

/**
 * Checks whether this browser already has an active Web Push subscription
 * and re-syncs it idempotently with the daemon on startup.
 */
export async function syncExistingWebPush(deps) {
  try {
    const reg = await deps.swContainer.getRegistration('/voice');
    if (!reg) return { active: false };
    const sub = await reg.pushManager.getSubscription();
    if (!sub) return { active: false };
    const json = sub.toJSON ? sub.toJSON() : sub;
    if (json && json.endpoint && json.keys && json.keys.p256dh && json.keys.auth) {
      await deps.fetch('/api/push/web', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          endpoint: json.endpoint,
          keys: { p256dh: json.keys.p256dh, auth: json.keys.auth },
          device: deviceName(deps.ua, deps.touchPoints),
        }),
      }).catch(() => {});
    }
    return { active: true, endpoint: json.endpoint };
  } catch (_) {
    return { active: false };
  }
}
