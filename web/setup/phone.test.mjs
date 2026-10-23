// Unit tests for the setup page's phone helpers: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { shellSwitch, shellSaved, tsnetSwitch, tsnetSaved, countdown, deviceKind, lastSeen } from './phone.js';

test('the shell switch shows only once phone access is on', () => {
  assert.equal(shellSwitch({ enabled: false, shell: true }).show, false);
  assert.equal(shellSwitch(undefined).show, false);
  assert.equal(shellSwitch({ enabled: true }).show, true);
});

test('the switch follows web.shell and says what it covers', () => {
  const off = shellSwitch({ enabled: true, shell: false });
  assert.equal(off.checked, false);
  assert.match(off.note, /no shell and no attachments/);
  const on = shellSwitch({ enabled: true, shell: true });
  assert.equal(on.checked, true);
  assert.doesNotMatch(on.note, /network/);
});

test('the saved message says what changed, or why not', () => {
  assert.match(shellSaved({ ok: true, shell: true }), /can open a shell/);
  assert.match(shellSaved({ ok: true, shell: false }), /off/);
  assert.equal(shellSaved({ error: 'disk full' }), 'Not saved: disk full');
  assert.match(shellSaved(null), /Not saved/);
});

test('the Tailscale certificate switch is off by default and says the name is logged', () => {
  assert.equal(tsnetSwitch({ enabled: true, tailscale: false }).show, false);
  assert.equal(tsnetSwitch({ enabled: false, tailscale: true }).show, false);
  const off = tsnetSwitch({ enabled: true, tailscale: true });
  assert.equal(off.show, true);
  assert.equal(off.checked, false);
  assert.match(off.note, /logged in public certificate logs/);
  const on = tsnetSwitch({ enabled: true, tailscale: true, tailscale_https: true, tailscale_name: 'studio.tail1234.ts.net' });
  assert.equal(on.checked, true);
  assert.match(on.note, /https:\/\/studio\.tail1234\.ts\.net/);
  assert.match(tsnetSwitch({ enabled: true, tailscale: true, tailscale_https: true, tailscale_error: 'no cert' }).note, /no certificate yet: no cert/);
});

test('the Tailscale switch message says what changed, or why not', () => {
  assert.match(tsnetSaved({ ok: true, tailscale_https: true, name: 'studio.tail1234.ts.net' }), /studio\.tail1234\.ts\.net/);
  assert.match(tsnetSaved({ ok: true, tailscale_https: false }), /own certificate/);
  assert.match(tsnetSaved({ error: 'Tailscale did not give this computer a certificate' }), /^Not turned on: Tailscale/);
});

test('the code countdown runs to empty', () => {
  const now = Date.parse('2026-10-21T12:00:00Z');
  assert.equal(countdown('2026-10-21T12:10:00Z', now), '10:00');
  assert.equal(countdown('2026-10-21T12:00:05Z', now), '0:05');
  assert.equal(countdown('2026-10-21T11:59:00Z', now), '');
  assert.equal(countdown(undefined, now), '');
});

test('device rows name the kind and the last contact', () => {
  const now = Date.parse('2026-10-21T12:00:00Z');
  assert.equal(deviceKind({ kind: 'ios' }), 'iPhone');
  assert.equal(deviceKind({ kind: 'browser' }), 'Browser');
  assert.equal(lastSeen({}, now), 'Not seen yet');
  assert.equal(lastSeen({ last_seen: '2026-10-21T11:55:00Z', last_network: 'lan' }, now), 'Wi-Fi · 5 min ago');
  assert.equal(lastSeen({ last_seen: '2026-10-21T11:59:59Z', last_network: 'tailscale' }, now), 'Tailscale · just now');
  assert.equal(lastSeen({ last_seen: '2026-10-19T12:00:00Z' }, now), '2 d ago');
});
