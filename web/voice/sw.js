// sheepdog /voice Service Worker: shows RFC 8291 Web Push notifications on
// Android, Linux and desktop browsers, answers approval buttons directly
// through POST /api/pending/answer (re-signing the session cookie from
// IndexedDB if the daemon restarted or the 12-hour ticket expired), and
// closes stale approval alerts when /voice reports open question IDs.

function b64url(bytes) {
  let s = '';
  const b = new Uint8Array(bytes);
  for (let i = 0; i < b.length; i++) s += String.fromCharCode(b[i]);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function newIdemKey() {
  const bytes = new Uint8Array(16);
  self.crypto.getRandomValues(bytes);
  return b64url(bytes);
}

function originURL(path) {
  return new URL(path, self.registration.scope).href;
}

function voiceClients() {
  return self.clients.matchAll({ type: 'window', includeUncontrolled: false });
}

function loadDeviceKey(daemonId) {
  if (typeof indexedDB === 'undefined' || !indexedDB) return Promise.resolve(null);
  return new Promise((resolve) => {
    try {
      const req = indexedDB.open('sheepdog', 1);
      req.onupgradeneeded = () => req.result.createObjectStore('devices');
      req.onerror = () => resolve(null);
      req.onsuccess = () => {
        try {
          const tx = req.result.transaction('devices', 'readonly');
          const getReq = tx.objectStore('devices').get(daemonId);
          tx.oncomplete = () => resolve(getReq.result || null);
          tx.onerror = () => resolve(null);
        } catch (_) {
          resolve(null);
        }
      };
    } catch (_) {
      resolve(null);
    }
  });
}

async function signInFromSW() {
  try {
    const hello = await (await fetch(originURL('/api/hello'), { cache: 'no-store', credentials: 'same-origin' })).json();
    const daemonId = hello && hello.computer && hello.computer.id;
    if (!daemonId) return false;
    const rec = await loadDeviceKey(daemonId);
    if (!rec || !rec.key || !rec.deviceId) return false;
    const ch = await (await fetch(originURL('/api/auth/challenge'), { cache: 'no-store', credentials: 'same-origin' })).json();
    if (!ch || !ch.nonce) return false;
    const now = Math.floor(Date.now() / 1000);
    const text = ['sheepdog-auth-v1', ch.daemon_id || daemonId, '', rec.deviceId, ch.nonce, String(now), 'session'].join('\n');
    const sig = await self.crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, rec.key, new TextEncoder().encode(text));
    const r = await fetch(originURL('/api/auth/session'), {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        device_id: rec.deviceId,
        nonce: ch.nonce,
        time: now,
        signature: b64url(sig),
        client: 'browser',
      }),
    });
    return Boolean(r && r.ok);
  } catch (_) {
    return false;
  }
}

async function answerPending(pendingId, optionIndex) {
  const key = newIdemKey();
  const post = () => fetch(originURL('/api/pending/answer'), {
    method: 'POST',
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      'Idempotency-Key': key,
    },
    body: JSON.stringify({ id: pendingId, option: optionIndex }),
  });
  try {
    let r = await post();
    if (r.status === 401 && (await signInFromSW())) {
      r = await post();
    }
    return Boolean(r && (r.ok || r.status === 409));
  } catch (_) {
    return false;
  }
}

async function openOrFocusVoice(path) {
  const target = originURL(path || '/voice');
  const list = await voiceClients();
  const existing = list.find((c) => c && typeof c.focus === 'function');
  if (existing) {
    try {
      const focused = await existing.focus();
      (focused || existing).postMessage({ type: 'focus-voice' });
      return focused;
    } catch (_) {
      /* fall through to openWindow */
    }
  }
  return self.clients.openWindow(target);
}

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('push', (e) => {
  let d = { title: 'Sheepdog', body: 'New update' };
  try {
    if (e.data) d = e.data.json() || d;
  } catch (_) {
    /* keep fallback */
  }
  e.waitUntil(
    voiceClients().then((list) => {
      const visible = list.filter((c) => c && c.visibilityState === 'visible');
      if (visible.length > 0) {
        for (const c of visible) {
          try { c.postMessage({ type: 'push-event', payload: d }); } catch (_) { /* ignore */ }
        }
        return undefined;
      }
      const maxActions = typeof Notification !== 'undefined' && Notification.maxActions > 0 ? Notification.maxActions : 0;
      const opts = {
        body: d.body || '',
        tag: d.tag || 'sheepdog',
        renotify: Boolean(d.needs_you),
        icon: '/static/apple-touch-icon.png',
        badge: '/static/favicon-32.png',
        data: {
          url: '/voice',
          pending_id: d.pending_id || '',
          kind: d.kind || '',
          options: Array.isArray(d.options) ? d.options : [],
        },
      };
      if (d.pending_id && Array.isArray(d.options) && d.options.length > 0 && maxActions > 0) {
        opts.actions = d.options.slice(0, maxActions).map((o) => ({
          action: 'option:' + o.index,
          title: String(o.label || ''),
        }));
      }
      return self.registration.showNotification(d.title || 'Sheepdog', opts);
    }),
  );
});

self.addEventListener('notificationclick', (e) => {
  const d = (e.notification && e.notification.data) || {};
  e.notification.close();
  if (e.action && e.action.startsWith('option:') && d.pending_id) {
    const option = Number(e.action.slice('option:'.length));
    e.waitUntil(
      answerPending(d.pending_id, option).then(async (ok) => {
        if (ok) {
          const list = await voiceClients();
          for (const c of list) {
            try { c.postMessage({ type: 'pending-answered', id: d.pending_id }); } catch (_) { /* ignore */ }
          }
          return undefined;
        }
        return openOrFocusVoice(d.url || '/voice');
      }),
    );
    return;
  }
  e.waitUntil(openOrFocusVoice(d.url || '/voice'));
});

self.addEventListener('message', (e) => {
  const m = (e && e.data) || {};
  if (m.type === 'sync-open-questions' && Array.isArray(m.openIds)) {
    const openSet = new Set(m.openIds);
    e.waitUntil(
      self.registration.getNotifications().then((list) => {
        for (const n of list) {
          if (n.data && n.data.pending_id && !openSet.has(n.data.pending_id)) {
            n.close();
          }
        }
      }).catch(() => {}),
    );
  }
});
