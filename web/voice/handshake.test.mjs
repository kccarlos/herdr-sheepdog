import { test } from 'node:test';
import assert from 'node:assert/strict';

import { Handshake, clientReady, PROTOCOL, HANDSHAKE_TIMEOUT_MS } from './handshake.js';

test('client-ready names the page and its protocol', () => {
  assert.deepEqual(clientReady(), { type: 'client-ready', protocol: PROTOCOL, client: { name: 'sheepdog-voice', version: 'embedded', platform: 'web' } });
  assert.equal(HANDSHAKE_TIMEOUT_MS, 2000);
});

test('bot-ready completes the handshake; status before it passes through', () => {
  const h = new Handshake();
  assert.equal(h.state, 'waiting');
  assert.deepEqual(h.onMessage({ type: 'status', voice: 'connected', muted: false }), { consumed: false });
  const r = h.onMessage({ type: 'bot-ready', protocol: 1, min_protocol: 1, daemon: { version: '0.4.2' }, features: ['playback', 'push'] });
  assert.deepEqual(r, { consumed: true });
  assert.equal(h.state, 'ready');
  assert.equal(h.protocol, 1);
  assert.deepEqual(h.features, ['playback', 'push']);
  assert.equal(h.describe(), '1 (daemon 0.4.2)');
  h.onTimeout(); // a late timer changes nothing
  assert.equal(h.state, 'ready');
});

test('a newer daemon is spoken to at the page protocol', () => {
  const h = new Handshake();
  h.onMessage({ type: 'bot-ready', protocol: PROTOCOL + 2, daemon: { version: 'x' } });
  assert.equal(h.state, 'ready');
  assert.equal(h.protocol, PROTOCOL);
});

test('an old daemon: its unknown-type error is swallowed and means protocol 1', () => {
  const h = new Handshake();
  const r = h.onMessage({ type: 'error', message: 'wsproto: unknown type "client-ready"' });
  assert.deepEqual(r, { consumed: true });
  assert.equal(h.state, 'legacy');
  assert.equal(h.protocol, 1);
  // Later errors are the page's to show.
  assert.deepEqual(h.onMessage({ type: 'error', message: 'rate limited' }), { consumed: false });
});

test('an old daemon that says nothing: legacy after the timeout', () => {
  const h = new Handshake();
  h.onTimeout();
  assert.equal(h.state, 'legacy');
  assert.equal(h.describe(), '1 (older daemon, no handshake)');
  // A late bot-ready still wins.
  h.onMessage({ type: 'bot-ready', protocol: 1, daemon: { version: 'y' } });
  assert.equal(h.state, 'ready');
});

test('refused as too old: close, no reconnect, say so', () => {
  const h = new Handshake();
  assert.equal(h.onMessage({ type: 'error', message: 'This app speaks /ws protocol 0; … Update the app.' }).consumed, false);
  const r = h.onMessage({ type: 'status', voice: 'closed', muted: false, reason: 'protocol_too_old' });
  assert.equal(r.close, true);
  assert.match(r.message, /reload/);
  assert.equal(h.state, 'rejected');
});

test('a daemon below the page minimum closes', () => {
  const h = new Handshake();
  const r = h.onMessage({ type: 'bot-ready', protocol: 0, daemon: { version: 'z' } });
  assert.equal(r.close, true);
  assert.equal(h.state, 'daemon_old');
});

test('other status closed reasons are not the handshake', () => {
  const h = new Handshake();
  assert.equal(h.onMessage({ type: 'status', voice: 'closed', reason: 'replaced' }).consumed, false);
});
