import { test } from 'node:test';
import assert from 'node:assert/strict';
import { bannerModel, applyUpdateBanner, UPDATE_DOC } from './update-banner.js';

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
  assert.equal(m.code, 'sheepdog update');
  const manual = bannerModel({ restart_pending: true, restart_to: 'v1.1.0' });
  assert.match(manual.text, /To finish, run/);
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

  applyUpdateBanner(doc, { available: true, latest: 'v1.2.0' }, storage);
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
