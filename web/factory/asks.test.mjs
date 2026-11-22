// Unit tests for /factory ask cards, on-demand browser pairing, and automatic
// WebCrypto re-authentication across daemon restarts: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { renderAskCardsHTML, createAskFlow } from './asks.js';

const fixture = JSON.parse(readFileSync(new URL('./testdata/asks-fixture.json', import.meta.url), 'utf8'));

function memStore() {
  const m = new Map();
  return {
    save: async (k, v) => m.set(k, v),
    load: async (k) => m.get(k),
    forget: async (k) => m.delete(k),
    m,
  };
}

// fakeFactoryDaemon simulates the sheepdog daemon for /factory ask cards:
// - GET /api/hello returns computer.id
// - POST /api/devices/enroll accepts SHORT-CODE for kind:"browser" and stores the public key
// - GET /api/auth/challenge + POST /api/auth/session verify the WebCrypto P-256 signature
//   and set an in-memory session cookie
// - POST /api/factory/asks/{id}/answer requires an active session cookie (else 403 proof_required
//   when unenrolled, or 401 session_expired after restart() clears sessions)
function fakeFactoryDaemon({ shortCode = 'ABCD-EFGH', daemonId = 'DAEMON_TEST_1' } = {}) {
  const calls = [];
  const answered = [];
  let enrolledPub = null;
  let enrolledDeviceId = '';
  let hasSession = false;
  let hadSessionBeforeRestart = false;

  const signedText = (dId, devId, nonce, unix) =>
    ['sheepdog-auth-v1', dId, '', devId, nonce, String(unix), 'session'].join('\n');

  const reply = (status, obj) => ({
    ok: status >= 200 && status < 300,
    status,
    json: async () => obj,
  });

  const fetch = async (path, init = {}) => {
    calls.push({ method: init.method || 'GET', path });
    const body = init.body ? JSON.parse(init.body) : null;

    if (path === '/api/hello') {
      return reply(200, { computer: { id: daemonId, name: 'Test Mac' } });
    }
    if (path === '/api/factory/asks') {
      return reply(200, fixture);
    }
    if (path === '/api/devices/enroll') {
      const norm = String(body.code || '').replace(/[\s-]+/g, '').toUpperCase();
      const want = shortCode.replace(/[\s-]+/g, '').toUpperCase();
      if (body.kind !== 'browser' || norm !== want) {
        return reply(410, { reason: 'code_used', error: 'Invalid or used code.' });
      }
      const raw = Uint8Array.from(atob(body.public_key.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
      enrolledPub = await webcrypto.subtle.importKey('raw', raw, { name: 'ECDSA', namedCurve: 'P-256' }, true, ['verify']);
      enrolledDeviceId = 'dev-browser-1';
      return reply(200, { device_id: enrolledDeviceId, daemon_id: daemonId, name: body.name || 'Test Browser' });
    }
    if (path === '/api/auth/challenge') {
      return reply(200, { nonce: 'NONCE-1', daemon_id: daemonId, expires_in: 60 });
    }
    if (path === '/api/auth/session') {
      if (!enrolledPub || body.device_id !== enrolledDeviceId || body.client !== 'browser') {
        return reply(401, { reason: 'unknown_device' });
      }
      const sig = Uint8Array.from(atob(body.signature.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
      const ok = await webcrypto.subtle.verify(
        { name: 'ECDSA', hash: 'SHA-256' },
        enrolledPub,
        sig,
        new TextEncoder().encode(signedText(daemonId, body.device_id, body.nonce, body.time)),
      );
      if (!ok) return reply(401, { reason: 'bad_signature' });
      hasSession = true;
      hadSessionBeforeRestart = false;
      return reply(200, { expires_in: 43200, device: { id: enrolledDeviceId, name: 'Test Browser' } });
    }
    const m = path.match(/^\/api\/factory\/asks\/([^/]+)\/answer$/);
    if (m && (init.method || 'GET') === 'POST') {
      if (!hasSession) {
        if (hadSessionBeforeRestart) {
          return reply(401, { error: 'unauthorized', reason: 'session_expired' });
        }
        return reply(403, { code: 'proof_required', error: 'proof_required' });
      }
      answered.push({ id: m[1], option: body.option, device_id: enrolledDeviceId });
      return reply(200, { ok: true, ask_id: m[1], device_id: enrolledDeviceId });
    }
    return reply(404, { error: 'not found' });
  };

  return {
    fetch,
    calls,
    answered,
    restartDaemon() {
      hasSession = false;
      hadSessionBeforeRestart = true;
    },
  };
}

test('open asks from the fixture render as cards with options, recommendation and escaping', () => {
  const xssAsk = {
    ...fixture.asks[0],
    id: 'ask-xss',
    question: 'Approve <script>alert(1)</script> for T1?',
  };
  const html = renderAskCardsHTML([...fixture.asks, xssAsk], {});
  assert.match(html, /Approve the plan for T1\?/);
  assert.match(html, /Tag and publish v0\.2\.0\?/);
  assert.match(html, /data-ask-id="ask-demo-1"/);
  assert.match(html, /data-option="Approve"/);
  assert.match(html, /T1/);
  assert.match(html, /Recommended/i);
  assert.ok(!html.includes('<script>alert(1)</script>'), 'ask question must be HTML-escaped');
  assert.ok(html.includes('&#60;script&#62;') || html.includes('&lt;script&gt;'), 'escaped script tag expected');
});

test('the /factory page replaces the read-only ask placeholder and fetches /api/factory/asks', () => {
  const html = readFileSync(new URL('./index.html', import.meta.url), 'utf8');
  const js = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
  assert.ok(!html.includes('Ask cards arrive in a later version'), 'index.html still has the read-only ask placeholder');
  assert.ok(js.includes('/api/factory/asks'), 'app.js does not fetch /api/factory/asks');
});

test('answering from an unenrolled browser shows how to pair and never auto-fetches a pairing code', async () => {
  const daemon = fakeFactoryDaemon();
  const store = memStore();
  const flow = createAskFlow({
    fetch: daemon.fetch,
    subtle: webcrypto.subtle,
    now: () => 1_763_812_800_000,
    ua: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15',
    touch: 0,
    ...store,
  });

  const res = await flow.answer(fixture.asks[0], 'Approve');
  assert.equal(res.ok, false);
  assert.equal(res.needsPairing, true);
  assert.match(res.message || '', /menu.?bar|pair/i, 'must explain how to pair from the Mac menu-bar app');
  assert.equal(daemon.answered.length, 0);
  assert.ok(
    !daemon.calls.some((c) => c.path.startsWith('/api/pair')),
    'must never fetch a pairing code automatically',
  );

  // Pairing on demand with a short code stores a non-extractable WebCrypto key and allows answering.
  const paired = await flow.pairWithCode('ABCD-EFGH');
  assert.equal(paired.ok, true);
  const rec = store.m.get('DAEMON_TEST_1');
  assert.ok(rec && rec.deviceId === 'dev-browser-1');
  assert.equal(rec.key.extractable, false, 'browser key must be non-extractable');

  const res2 = await flow.answer(fixture.asks[0], 'Approve');
  assert.equal(res2.ok, true);
  assert.deepEqual(daemon.answered, [{ id: 'ask-demo-1', option: 'Approve', device_id: 'dev-browser-1' }]);
});

test('after a daemon restart an enrolled browser re-authenticates with its WebCrypto key without pairing again', async () => {
  const daemon = fakeFactoryDaemon();
  const store = memStore();
  const flow = createAskFlow({
    fetch: daemon.fetch,
    subtle: webcrypto.subtle,
    now: () => 1_763_812_800_000,
    ua: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15',
    touch: 0,
    ...store,
  });

  // Pair once and answer the first ask.
  const paired = await flow.pairWithCode('ABCD-EFGH');
  assert.equal(paired.ok, true);
  const first = await flow.answer(fixture.asks[0], 'Approve');
  assert.equal(first.ok, true);

  // Simulate daemon restart: in-memory session tickets are gone, but devices.json kept the key.
  daemon.restartDaemon();
  const enrollCountBefore = daemon.calls.filter((c) => c.path === '/api/devices/enroll').length;

  // Answering the second ask silently re-authenticates via /api/auth/challenge + /api/auth/session.
  const second = await flow.answer(fixture.asks[1], 'Publish v0.2.0');
  assert.equal(second.ok, true);
  assert.notEqual(second.needsPairing, true);
  assert.equal(
    daemon.calls.filter((c) => c.path === '/api/devices/enroll').length,
    enrollCountBefore,
    'must not re-enroll after a daemon restart',
  );
  assert.deepEqual(daemon.answered[1], { id: 'ask-demo-2', option: 'Publish v0.2.0', device_id: 'dev-browser-1' });
});
