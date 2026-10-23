// Unit tests for browser pairing: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { parsePairing, isAppleMobile, deviceName, appLink, signedText, enroll, signIn, MSG } from './pairing.js';

const hash = '#pair=1&d=DAEMONID&e=c0de&k=PIN&n=Studio+Mac+%26+co&u=100.101.102.103%3A7777&u=192.168.1.20%3A7777';

test('the fragment is read, and anything else is not a pairing link', () => {
  const p = parsePairing(hash);
  assert.deepEqual(p, { daemonId: 'DAEMONID', name: 'Studio Mac & co', pin: 'PIN', code: 'c0de', addresses: ['100.101.102.103:7777', '192.168.1.20:7777'] });
  assert.equal(parsePairing(''), null);
  assert.equal(parsePairing('#foo=1'), null);
  assert.equal(parsePairing('#pair=1&d=x'), null);
});

test('iPhone and iPad are offered the app first; the name comes from the browser', () => {
  const iphone = 'Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Mobile/15E148 Safari/604.1';
  const ipadDesktop = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Safari/605.1.15';
  const android = 'Mozilla/5.0 (Linux; Android 16; Pixel 10) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Mobile Safari/537.36';
  assert.equal(isAppleMobile(iphone, 5), true);
  assert.equal(isAppleMobile(ipadDesktop, 5), true);
  assert.equal(isAppleMobile(ipadDesktop, 0), false);
  assert.equal(isAppleMobile(android, 5), false);
  assert.equal(deviceName(android, 5), 'Android · Chrome');
  assert.equal(deviceName(ipadDesktop, 0), 'Mac · Safari');
  assert.equal(deviceName(iphone, 5), 'iPhone · Safari');
  assert.equal(appLink(hash), 'sheepdog://pair#' + hash.slice(1));
});

// fakeDaemon answers enroll, challenge and session like the daemon, and
// checks the signature with the enrolled key.
function fakeDaemon({ codeStatus = 200 } = {}) {
  const calls = [];
  let pub = null;
  const fetch = async (path, init = {}) => {
    calls.push(path);
    const body = init.body ? JSON.parse(init.body) : null;
    const reply = (status, obj) => ({ ok: status < 300, status, json: async () => obj });
    if (path === '/api/devices/enroll') {
      if (codeStatus === 410) return reply(410, { reason: 'code_used' });
      const raw = Uint8Array.from(atob(body.public_key.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
      pub = await webcrypto.subtle.importKey('raw', raw, { name: 'ECDSA', namedCurve: 'P-256' }, true, ['verify']);
      assert.equal(body.kind, 'browser');
      assert.equal(body.code, 'c0de');
      return reply(200, { device_id: 'dev1', daemon_id: 'DAEMONID', name: body.name });
    }
    if (path === '/api/auth/challenge') return reply(200, { nonce: 'N1', daemon_id: 'DAEMONID', expires_in: 60 });
    if (path === '/api/auth/session') {
      const sig = Uint8Array.from(atob(body.signature.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
      const ok = await webcrypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, pub, sig, new TextEncoder().encode(signedText('DAEMONID', body.device_id, body.nonce, body.time)));
      return ok && body.client === 'browser' ? reply(200, { ticket: 't' }) : reply(401, { reason: 'bad_signature' });
    }
    return reply(404, {});
  };
  return { fetch, calls };
}

function memStore() {
  const m = new Map();
  return { save: async (k, v) => m.set(k, v), load: async (k) => m.get(k), forget: async (k) => m.delete(k), m };
}

test('a browser enrolls a non-extractable key and signs in with it', async () => {
  const d = fakeDaemon();
  const store = memStore();
  const deps = { fetch: d.fetch, subtle: webcrypto.subtle, ua: 'Mozilla/5.0 (Linux; Android 16) Chrome/140.0', touch: 5, now: () => 1_700_000_000_000, ...store };
  const r = await enroll(parsePairing(hash), deps);
  assert.equal(r.ok, true);
  const rec = store.m.get('DAEMONID');
  assert.equal(rec.deviceId, 'dev1');
  assert.equal(rec.key.extractable, false);
  const s = await signIn('DAEMONID', deps);
  assert.deepEqual(s, { ok: true });
  assert.deepEqual(d.calls, ['/api/devices/enroll', '/api/auth/challenge', '/api/auth/session']);
});

test('a used or expired code says so', async () => {
  const d = fakeDaemon({ codeStatus: 410 });
  const store = memStore();
  const r = await enroll(parsePairing(hash), { fetch: d.fetch, subtle: webcrypto.subtle, ua: '', touch: 0, ...store });
  assert.deepEqual(r, { ok: false, message: MSG.used });
  assert.equal(store.m.size, 0);
});

test('without a saved key there is nothing to sign', async () => {
  const d = fakeDaemon();
  const r = await signIn('DAEMONID', { fetch: d.fetch, subtle: webcrypto.subtle, now: Date.now, ...memStore() });
  assert.deepEqual(r, { ok: false, reason: 'no_key' });
  assert.deepEqual(d.calls, []);
});
