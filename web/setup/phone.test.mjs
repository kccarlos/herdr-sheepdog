// Unit tests for the setup page's phone helpers: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { shellLAN, shellLANSaved } from './phone.js';

test('the shell switch shows only once phone access is on', () => {
  assert.equal(shellLAN({ enabled: false, shell_lan: true }).show, false);
  assert.equal(shellLAN(undefined).show, false);
  assert.equal(shellLAN({ enabled: true }).show, true);
});

test('the switch follows web.shell_lan and states the risk either way', () => {
  const off = shellLAN({ enabled: true, shell_lan: false });
  assert.equal(off.checked, false);
  assert.match(off.note, /Tailscale only/);
  assert.match(off.note, /could read what you type/);
  const on = shellLAN({ enabled: true, shell_lan: true });
  assert.equal(on.checked, true);
  assert.match(on.note, /could read what you type/);
});

test('the saved message says what changed, or why not', () => {
  assert.match(shellLANSaved({ ok: true, shell_lan: true }), /works on this network/);
  assert.match(shellLANSaved({ ok: true, shell_lan: false }), /Tailscale-only/);
  assert.equal(shellLANSaved({ error: 'disk full' }), 'Not saved: disk full');
  assert.match(shellLANSaved(null), /Not saved/);
});
