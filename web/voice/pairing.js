// Browser pairing (docs/dev/design/security.md "Browser or Android
// phone"): the pairing QR is an https link to this page with everything in
// the fragment. The page enrolls a non-extractable WebCrypto P-256 key with
// the one-time code, keeps it in IndexedDB, and signs a challenge for a
// session cookie whenever the daemon asks. Pure helpers plus a flow with
// its effects passed in, so `make test-web` runs it in node.

// parsePairing reads a pairing fragment ("#pair=1&d=…&n=…&k=…&e=…&u=…"),
// or null when the fragment is not one.
export function parsePairing(hash) {
  const raw = String(hash || '').replace(/^#/, '');
  if (!raw) return null;
  const q = new URLSearchParams(raw);
  if (q.get('pair') !== '1') return null;
  const p = { daemonId: q.get('d') || '', name: q.get('n') || '', pin: q.get('k') || '', code: q.get('e') || '', addresses: q.getAll('u') };
  if (!p.daemonId || !p.code) return null;
  return p;
}

// isAppleMobile reports an iPhone or iPad browser, where the page offers
// the app first (an iPad in desktop mode reports a Mac with touch).
export function isAppleMobile(ua, touchPoints) {
  const s = String(ua || '');
  if (/iPhone|iPad|iPod/.test(s)) return true;
  return /Macintosh/.test(s) && Number(touchPoints) > 1;
}

// deviceName is the name a browser pairs as: "Mac · Safari",
// "Android · Chrome". The setup page can rename it.
export function deviceName(ua, touchPoints) {
  const s = String(ua || '');
  let os = 'Computer';
  if (/Android/.test(s)) os = 'Android';
  else if (/iPhone/.test(s)) os = 'iPhone';
  else if (/iPad/.test(s) || (/Macintosh/.test(s) && Number(touchPoints) > 1)) os = 'iPad';
  else if (/Macintosh|Mac OS X/.test(s)) os = 'Mac';
  else if (/Windows/.test(s)) os = 'Windows';
  else if (/CrOS/.test(s)) os = 'Chromebook';
  else if (/Linux/.test(s)) os = 'Linux';
  let browser = 'Browser';
  if (/Edg\//.test(s)) browser = 'Edge';
  else if (/Firefox\/|FxiOS\//.test(s)) browser = 'Firefox';
  else if (/Chrome\/|CriOS\//.test(s)) browser = 'Chrome';
  else if (/Safari\//.test(s)) browser = 'Safari';
  return os + ' · ' + browser;
}

// appLink is the link that hands the pairing to the Sheepdog app.
export function appLink(hash) {
  return 'sheepdog://pair#' + String(hash || '').replace(/^#/, '');
}

// signedText is what a device signs for a session; a browser signs an
// empty pin line (it cannot be sure of the certificate it accepted).
export function signedText(daemonId, deviceId, nonce, unix) {
  return ['sheepdog-auth-v1', daemonId, '', deviceId, nonce, String(unix), 'session'].join('\n');
}

export function b64url(bytes) {
  let s = '';
  const b = new Uint8Array(bytes);
  for (let i = 0; i < b.length; i++) s += String.fromCharCode(b[i]);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// Messages the page shows.
export const MSG = {
  used: 'This code was already used or has expired. Open the setup page on your computer and scan the new code.',
  pair: 'Pair this browser: show the pairing code on your computer (its setup page, or sheepdog pair) and scan it with this device\'s camera.',
  revoked: 'This browser was removed on your computer. Scan the pairing code again to reconnect.',
};

// enroll makes a key, enrolls it with the code and saves it. deps:
// fetch, subtle (crypto.subtle), save(daemonId, record), ua, touch.
// Returns {ok:true} or {ok:false, message}.
export async function enroll(p, deps) {
  const pair = await deps.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign']);
  const pub = await deps.subtle.exportKey('raw', pair.publicKey);
  const r = await deps.fetch('/api/devices/enroll', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ code: p.code, public_key: b64url(pub), kind: 'browser', name: deviceName(deps.ua, deps.touch) }),
  });
  const body = await r.json().catch(() => ({}));
  if (r.status === 410) return { ok: false, message: MSG.used };
  if (!r.ok) return { ok: false, message: body.error || ('The computer refused (' + r.status + ').') };
  await deps.save(body.daemon_id || p.daemonId, { deviceId: body.device_id, key: pair.privateKey });
  return { ok: true, daemonId: body.daemon_id || p.daemonId };
}

// signIn signs a challenge with the saved key for daemonId; the daemon
// answers with the session cookie. Returns {ok, reason?}.
export async function signIn(daemonId, deps) {
  const rec = await deps.load(daemonId);
  if (!rec) return { ok: false, reason: 'no_key' };
  const ch = await (await deps.fetch('/api/auth/challenge', { cache: 'no-store' })).json();
  const now = Math.floor(deps.now() / 1000);
  const sig = await deps.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, rec.key, new TextEncoder().encode(signedText(ch.daemon_id || daemonId, rec.deviceId, ch.nonce, now)));
  const r = await deps.fetch('/api/auth/session', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ device_id: rec.deviceId, nonce: ch.nonce, time: now, signature: b64url(sig), client: 'browser' }),
  });
  if (r.ok) return { ok: true };
  const body = await r.json().catch(() => ({}));
  if (body.reason === 'device_revoked' || body.reason === 'unknown_device') await deps.forget(daemonId);
  return { ok: false, reason: body.reason || String(r.status) };
}

// keyStore is IndexedDB, keyed by daemon id (a CryptoKey is stored as
// itself; it stays non-extractable).
export function keyStore(idb) {
  const open = () => new Promise((resolve, reject) => {
    const req = idb.open('sheepdog', 1);
    req.onupgradeneeded = () => req.result.createObjectStore('devices');
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
  const run = async (mode, fn) => {
    const db = await open();
    return new Promise((resolve, reject) => {
      const tx = db.transaction('devices', mode);
      const req = fn(tx.objectStore('devices'));
      tx.oncomplete = () => resolve(req && req.result);
      tx.onerror = () => reject(tx.error);
    });
  };
  return {
    save: (id, rec) => run('readwrite', (s) => s.put(rec, id)),
    load: (id) => run('readonly', (s) => s.get(id)),
    forget: (id) => run('readwrite', (s) => s.delete(id)),
  };
}
