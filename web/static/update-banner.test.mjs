import { test } from 'node:test';
import assert from 'node:assert/strict';
import { bannerModel, UPDATE_DOC } from './update-banner.js';

test('no banner without an update or when current', () => {
  assert.equal(bannerModel(undefined), null);
  assert.equal(bannerModel({ current: 'v1.0.0', latest: 'v1.0.0', available: false }), null);
  assert.equal(bannerModel({ current: 'dev', available: false }), null);
});

test('a newer release links the update docs', () => {
  const m = bannerModel({ current: 'v1.0.0', latest: 'v1.1.0', available: true, how: 'brew upgrade sheepdog' });
  assert.match(m.text, /sheepdog v1\.1\.0 is available/);
  assert.equal(m.link.href, UPDATE_DOC);
  assert.match(UPDATE_DOC, /quick-start\.md#update$/);
  assert.equal(m.key, 'available:v1.1.0');
});

test('an installed upgrade says to restart, and mentions the auto restart', () => {
  const m = bannerModel({ current: 'v1.0.0', restart_pending: true, restart_to: 'v1.1.0', auto_restart: true, available: true, latest: 'v1.1.0' });
  assert.match(m.text, /v1\.1\.0 is installed/);
  assert.match(m.text, /restarts itself when idle/);
  assert.equal(m.code, 'sheepdog restart');
  const manual = bannerModel({ restart_pending: true, restart_to: 'v1.1.0' });
  assert.match(manual.text, /To finish, run/);
});
