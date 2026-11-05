import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import {
  vapidKeyBytes,
  pushSupport,
  explainPushError,
  enableWebPush,
  disableWebPush,
} from './push.js';
import { b64url, signedText } from './pairing.js';

// Sample 65-byte uncompressed P-256 public key in base64url.
const sampleVAPID = b64url(Uint8Array.from({ length: 65 }, (_, i) => (i === 0 ? 4 : i)));

test('pushSupport checks secure context, browser APIs, and skips Apple mobile', () => {
  assert.deepEqual(
    pushSupport({ isSecureContext: true, hasSW: true, hasPushManager: true, hasNotification: true, ua: 'Mozilla/5.0 (Linux; Android 15) Chrome/131.0', touchPoints: 5 }),
    { supported: true },
  );
  assert.equal(
    pushSupport({ isSecureContext: true, hasSW: true, hasPushManager: true, hasNotification: true, ua: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)', touchPoints: 5 }).supported,
    false,
  );
  const insecure = pushSupport({ isSecureContext: false, hasSW: true, hasPushManager: true, hasNotification: true, ua: 'Mozilla/5.0 (X11; Linux x86_64) Chrome/131.0', touchPoints: 0 });
  assert.equal(insecure.supported, false);
  assert.match(insecure.message, /HTTPS/);
  assert.equal(vapidKeyBytes(sampleVAPID).length, 65);
});

test('explainPushError translates self-signed TLS SecurityError and denied permissions into plain fixes', () => {
  assert.match(explainPushError({ name: 'SecurityError', message: 'SSL certificate error' }), /Tailscale HTTPS/);
  assert.match(explainPushError({ name: 'NotAllowedError', message: 'denied' }), /Allow notifications/);
});

test('enableWebPush and disableWebPush register /voice/sw.js with scope /voice and sync /api/push/web', async () => {
  const posted = [];
  let unsubscribed = 0;
  let regCall = null;
  const fakeSub = {
    endpoint: 'https://fcm.googleapis.com/fcm/send/sub-1',
    toJSON: () => ({
      endpoint: 'https://fcm.googleapis.com/fcm/send/sub-1',
      keys: { p256dh: sampleVAPID, auth: 'MDEyMzQ1Njc4OWFiY2RlZg' },
    }),
    unsubscribe: async () => { unsubscribed++; return true; },
  };
  const fakeReg = {
    pushManager: {
      getSubscription: async () => null,
      subscribe: async (opts) => {
        assert.equal(opts.userVisibleOnly, true);
        assert.equal(opts.applicationServerKey.length, 65);
        return fakeSub;
      },
    },
  };
  const deps = {
    ua: 'Mozilla/5.0 (Linux; Android 15) Chrome/131.0',
    touchPoints: 5,
    Notification: { permission: 'default', requestPermission: async () => 'granted' },
    swContainer: {
      register: async (script, opts) => { regCall = { script, opts }; return fakeReg; },
      getRegistration: async () => ({
        pushManager: { getSubscription: async () => fakeSub },
      }),
    },
    fetch: async (url, init = {}) => {
      if (url === '/api/push/status') {
        return { ok: true, status: 200, json: async () => ({ web_push_enabled: true, vapid_public_key: sampleVAPID }) };
      }
      posted.push({ url, method: init.method || 'GET', body: JSON.parse(init.body) });
      return { ok: true, status: 200, json: async () => ({ ok: true }) };
    },
  };

  const res = await enableWebPush(deps);
  assert.equal(res.ok, true);
  assert.deepEqual(regCall, { script: '/voice/sw.js', opts: { scope: '/voice' } });
  assert.equal(posted.length, 1);
  assert.equal(posted[0].url, '/api/push/web');
  assert.equal(posted[0].method, 'POST');
  assert.equal(posted[0].body.device, 'Android · Chrome');

  const off = await disableWebPush(deps);
  assert.equal(off.ok, true);
  assert.equal(unsubscribed, 1);
  assert.equal(posted[1].method, 'DELETE');
  assert.equal(posted[1].body.endpoint, 'https://fcm.googleapis.com/fcm/send/sub-1');
});

function loadServiceWorkerEnv(overrides = {}) {
  const listeners = {};
  const shown = [];
  const openedWindows = [];
  let clientList = [];

  const selfObj = {
    skipWaiting: () => {},
    clients: {
      claim: async () => {},
      matchAll: async () => clientList,
      openWindow: async (url) => { openedWindows.push(url); return { url }; },
    },
    registration: {
      scope: 'https://box.tail.ts.net:7777/voice',
      showNotification: async (title, options) => {
        const closed = { done: false };
        shown.push({ title, options, data: options.data, tag: options.tag, closed, close() { closed.done = true; } });
      },
      getNotifications: async () => shown.filter((n) => !n.closed.done),
    },
    addEventListener(type, fn) {
      listeners[type] = fn;
    },
    crypto: globalThis.crypto,
    ...overrides,
  };

  const ctx = vm.createContext({
    self: selfObj,
    fetch: overrides.fetch || (async () => ({ ok: true, status: 200, json: async () => ({ ok: true }) })),
    Notification: overrides.Notification || { maxActions: 2 },
    URL,
    TextEncoder,
    Uint8Array,
    btoa: globalThis.btoa,
    Date: overrides.Date || Date,
    indexedDB: overrides.indexedDB,
    crypto: globalThis.crypto,
    Promise,
  });

  const code = readFileSync(new URL('./sw.js', import.meta.url), 'utf8');
  vm.runInContext(code, ctx, { filename: 'sw.js' });

  return {
    listeners,
    shown,
    openedWindows,
    setClients(list) { clientList = list; },
    async dispatch(type, event) {
      let pending = Promise.resolve();
      listeners[type]({
        ...event,
        waitUntil(p) { pending = Promise.resolve(p); },
      });
      await pending;
    },
  };
}

test('sw.js shows approval push with inline actions, skips visible /voice tab, and retracts answered alerts', async () => {
  const env = loadServiceWorkerEnv();
  const payload = {
    title: 'auth',
    body: 'auth asks to run git push. Yes or no?',
    tag: 'ap-7',
    kind: 'approval',
    pending_id: 'ap-7',
    needs_you: true,
    options: [
      { index: 1, label: 'Yes' },
      { index: 2, label: 'No' },
      { index: 3, label: 'Always' },
    ],
  };

  // Visible /voice client suppresses duplicate OS alert and forwards push-event to tab.
  const tabMessages = [];
  env.setClients([{ visibilityState: 'visible', postMessage: (m) => tabMessages.push(m) }]);
  await env.dispatch('push', { data: { json: () => payload } });
  assert.equal(env.shown.length, 0);
  assert.equal(tabMessages.length, 1);

  // Hidden / closed tab shows notification with Notification.maxActions (2) inline buttons.
  env.setClients([]);
  await env.dispatch('push', { data: { json: () => payload } });
  assert.equal(env.shown.length, 1);
  assert.equal(env.shown[0].title, 'auth');
  assert.deepEqual(
    Array.from(env.shown[0].options.actions, (a) => ({ action: a.action, title: a.title })),
    [{ action: 'option:1', title: 'Yes' }, { action: 'option:2', title: 'No' }],
  );

  // Page message sync-open-questions closes notifications whose pending_id is no longer open.
  await env.dispatch('message', { data: { type: 'sync-open-questions', openIds: ['ap-other'] } });
  assert.equal(env.shown[0].closed.done, true);
});

test('sw.js notificationclick answers option button and re-signs session from IndexedDB on 401 after daemon restart', async () => {
  const pair = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign', 'verify']);
  const calls = [];
  let signedIn = false;

  // Fake IndexedDB holding the paired browser's non-extractable P-256 key.
  const fakeIDB = {
    open() {
      const req = {};
      queueMicrotask(() => {
        req.result = {
          transaction() {
            const tx = {
              objectStore() {
                return {
                  get(id) {
                    const g = {};
                    queueMicrotask(() => {
                      g.result = id === 'daemon-xyz' ? { deviceId: 'dev-browser-1', key: pair.privateKey } : undefined;
                      if (tx.oncomplete) tx.oncomplete();
                    });
                    return g;
                  },
                };
              },
            };
            return tx;
          },
        };
        if (req.onsuccess) req.onsuccess();
      });
      return req;
    },
  };

  const env = loadServiceWorkerEnv({
    indexedDB: fakeIDB,
    Date: { now: () => 1_700_000_000_000 },
    fetch: async (url, init = {}) => {
      const path = String(url).replace(/^https:\/\/[^/]+/, '');
      calls.push({ path, headers: init.headers || {}, body: init.body ? JSON.parse(init.body) : null });
      if (path === '/api/pending/answer' && !signedIn) {
        return { ok: false, status: 401, json: async () => ({ error: 'unauthorized', reason: 'session_expired' }) };
      }
      if (path === '/api/hello') {
        return { ok: true, status: 200, json: async () => ({ computer: { id: 'daemon-xyz' } }) };
      }
      if (path === '/api/auth/challenge') {
        return { ok: true, status: 200, json: async () => ({ daemon_id: 'daemon-xyz', nonce: 'nonce-99' }) };
      }
      if (path === '/api/auth/session') {
        const body = JSON.parse(init.body);
        const sigBytes = Uint8Array.from(atob(body.signature.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
        const ok = await crypto.subtle.verify(
          { name: 'ECDSA', hash: 'SHA-256' },
          pair.publicKey,
          sigBytes,
          new TextEncoder().encode(signedText('daemon-xyz', 'dev-browser-1', 'nonce-99', 1_700_000_000)),
        );
        assert.equal(ok, true, 'SW signed valid P-256 auth challenge');
        signedIn = true;
        return { ok: true, status: 200, json: async () => ({ ok: true }) };
      }
      if (path === '/api/pending/answer' && signedIn) {
        return { ok: true, status: 200, json: async () => ({ ok: true, spoken: 'Told auth yes.' }) };
      }
      throw new Error('unexpected fetch: ' + path);
    },
  });

  let closed = false;
  await env.dispatch('notificationclick', {
    action: 'option:1',
    notification: {
      data: { url: '/voice', pending_id: 'ap-77' },
      close() { closed = true; },
    },
  });

  assert.equal(closed, true);
  assert.equal(signedIn, true);
  const answerCalls = calls.filter((c) => c.path === '/api/pending/answer');
  assert.equal(answerCalls.length, 2);
  assert.deepEqual(answerCalls[0].body, { id: 'ap-77', option: 1 });
  // Retried with the EXACT same Idempotency-Key!
  assert.ok(answerCalls[0].headers['Idempotency-Key']);
  assert.equal(answerCalls[0].headers['Idempotency-Key'], answerCalls[1].headers['Idempotency-Key']);
  assert.equal(env.openedWindows.length, 0);
});
