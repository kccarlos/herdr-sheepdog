import { test } from 'node:test';
import assert from 'node:assert/strict';
import { bannerModel, applyUpdateBanner, startUpdate, UPDATE_DOC } from './update-banner.js';

test('no banner without an update or when current', () => {
  assert.equal(bannerModel(undefined), null);
  assert.equal(bannerModel({ current: 'v1.0.0', latest: 'v1.0.0', available: false }), null);
  assert.equal(bannerModel({ current: 'dev', available: false }), null);
});

test('a newer release offers the Update button; a source checkout gets the command', () => {
  const m = bannerModel({ current: 'v1.0.0', latest: 'v1.1.0', available: true, how: 'sheepdog update', can_install: true });
  assert.match(m.text, /sheepdog v1\.1\.0 is available/);
  assert.equal(m.action, 'Update');
  assert.equal(m.code, undefined);
  assert.equal(m.key, 'available:v1.1.0');
  const source = bannerModel({ current: 'v1.0.0', latest: 'v1.1.0', available: true, how: 'git -C /src pull && make -C /src build' });
  assert.equal(source.action, undefined);
  assert.equal(source.code, 'git -C /src pull && make -C /src build');
  const none = bannerModel({ latest: 'v1.1.0', available: true });
  assert.equal(none.link.href, UPDATE_DOC);
  assert.match(UPDATE_DOC, /quick-start\.md#update$/);
});

test('installing, failed and an installed upgrade', () => {
  assert.match(bannerModel({ available: true, latest: 'v1.1.0', state: 'installing' }).text, /Installing sheepdog v1\.1\.0/);
  const failed = bannerModel({ available: true, latest: 'v1.1.0', can_install: true, state: 'failed', error: 'brew upgrade: exit status 1' });
  assert.match(failed.text, /did not finish: brew upgrade: exit status 1/);
  assert.equal(failed.action, 'Try again');
  const restart = bannerModel({ current: 'v1.0.0', restart_pending: true, restart_to: 'v1.1.0', auto_restart: true, available: true, latest: 'v1.1.0' });
  assert.match(restart.text, /v1\.1\.0 is installed/);
  assert.equal(restart.action, 'Restart now');
});

test('startUpdate posts, follows the restart and reloads on the new version', async () => {
  const calls = [];
  const replies = [
    { ok: true, json: async () => ({ current: 'v1.0.0', latest: 'v1.1.0', state: 'installing' }) },
    null, // the daemon is restarting
    { ok: true, json: async () => ({ update: { current: 'v1.0.0', latest: 'v1.1.0', state: 'installing' } }) },
    { ok: true, json: async () => ({ update: { current: 'v1.1.0', latest: 'v1.1.0' } }) },
  ];
  const fetchFn = async (url, opts) => { calls.push(`${opts?.method || 'GET'} ${url}`); const r = replies.shift(); if (!r) throw new Error('refused'); return r; };
  const doc = { getElementById: () => null, querySelector: () => null, createElement: () => ({ dataset: {}, setAttribute() {}, addEventListener() {}, append() {} }) };
  let reloaded = false;
  await startUpdate(doc, 'v1.0.0', { fetchFn, reload: () => { reloaded = true; }, wait: async () => {} });
  assert.equal(calls[0], 'POST /api/update');
  assert.equal(calls.length, 4);
  assert.ok(reloaded);
  // A refusal is shown, with no polling.
  calls.length = 0;
  replies.push({ ok: false, statusText: 'Conflict', json: async () => ({ error: 'sheepdog v1.1.0 is up to date' }) });
  await startUpdate(doc, 'v1.1.0', { fetchFn, reload: () => {}, wait: async () => {} });
  assert.deepEqual(calls, ['POST /api/update']);
  // Try again after a refusal (no version known yet): no reload while installing.
  calls.length = 0;
  reloaded = false;
  replies.push(
    { ok: true, json: async () => ({ current: 'v1.0.0', latest: 'v1.1.0', state: 'installing' }) },
    { ok: true, json: async () => ({ update: { current: 'v1.0.0', state: 'failed', error: 'checksum' } }) },
  );
  await startUpdate(doc, undefined, { fetchFn, reload: () => { reloaded = true; }, wait: async () => {} });
  assert.equal(reloaded, false);
  assert.equal(calls.length, 2);
});

test('applyUpdateBanner replaces available banner when upgrade becomes restart_pending and clears when current', () => {
  const nodes = [];
  const header = { after(el) { nodes.push(el); el.remove = () => { const i = nodes.indexOf(el); if (i >= 0) nodes.splice(i, 1); }; } };
  const doc = {
    createElement(tag) {
      return {
        tag, id: '', className: '', textContent: '', dataset: {}, children: [],
        setAttribute(k, v) { this[k] = v; },
        addEventListener() {},
        append(...xs) { this.children.push(...xs); },
        remove() {},
      };
    },
    getElementById(id) { return nodes.find((n) => n.id === id) || null; },
    querySelector(sel) { return sel === 'header' ? header : null; },
  };
  const storage = { getItem: () => null, setItem() {} };

  applyUpdateBanner(doc, { available: true, latest: 'v1.2.0', can_install: true }, storage);
  assert.equal(nodes.length, 1);
  assert.equal(nodes[0].dataset.key, 'available:v1.2.0');

  // Upgrade installed -> replaces old banner instead of leaving stale "available" copy.
  applyUpdateBanner(doc, { restart_pending: true, restart_to: 'v1.2.0', auto_restart: true }, storage);
  assert.equal(nodes.length, 1);
  assert.equal(nodes[0].dataset.key, 'restart:v1.2.0');

  // Daemon restarted and is now current -> banner removed.
  applyUpdateBanner(doc, { current: 'v1.2.0', latest: 'v1.2.0', available: false }, storage);
  assert.equal(nodes.length, 0);
});
