import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  TYPING_SETTLE_MS,
  DRAFT_TTL_MS,
  mayReload,
  saveReloadDraft,
  takeReloadDraft,
  createBuildReloader,
} from './reload.js';

function memStorage() {
  const m = new Map();
  return {
    getItem: (k) => (m.has(k) ? m.get(k) : null),
    setItem: (k, v) => m.set(k, String(v)),
    removeItem: (k) => m.delete(k),
  };
}

test('mayReload waits until audio is disconnected, taps finished, typing settled, and dialog closed', () => {
  const base = { userDisconnected: true, approvalsBusy: false, sinceTypedMs: TYPING_SETTLE_MS, dialogOpen: false };
  assert.equal(mayReload(base), true);
  assert.equal(mayReload({ ...base, userDisconnected: false }), false);
  assert.equal(mayReload({ ...base, approvalsBusy: true }), false);
  assert.equal(mayReload({ ...base, sinceTypedMs: TYPING_SETTLE_MS - 1 }), false);
  assert.equal(mayReload({ ...base, dialogOpen: true }), false);
});

test('createBuildReloader ignores initial/empty version, defers while busy, and reloads with saved draft when idle', () => {
  let t = 50_000;
  const storage = memStorage();
  let reloads = 0;
  const pendingNotices = [];
  const idle = { userDisconnected: false, approvalsBusy: false, sinceTypedMs: 10_000, dialogOpen: false };
  let draft = 'tell auth to run tests';

  const rel = createBuildReloader({
    now: () => t,
    storage,
    getIdleState: () => idle,
    getDraft: () => draft,
    reload: () => { reloads++; },
    onPendingChange: (v) => pendingNotices.push(v),
  });

  // Empty and initial versions never trigger reload.
  assert.equal(rel.noteVersion(''), false);
  assert.equal(rel.noteVersion('v0.4.1'), false);
  assert.equal(rel.noteVersion('v0.4.1'), false);
  assert.equal(reloads, 0);

  // Daemon restarts on v0.4.2 while user is in an active audio call: deferred!
  assert.equal(rel.noteVersion('v0.4.2'), true);
  assert.equal(rel.pending(), 'v0.4.2');
  assert.deepEqual(pendingNotices, ['v0.4.2']);
  assert.equal(reloads, 0);

  // User disconnects audio, but an approval tap is still in flight -> still deferred.
  idle.userDisconnected = true;
  idle.approvalsBusy = true;
  assert.equal(rel.check(), false);
  assert.equal(reloads, 0);

  // Approval tap finishes -> reloads immediately and saves draft!
  idle.approvalsBusy = false;
  assert.equal(rel.check(), true);
  assert.equal(reloads, 1);

  // Reloaded page restores draft once, and expires stale drafts.
  assert.equal(takeReloadDraft(storage, () => t + 500), 'tell auth to run tests');
  assert.equal(takeReloadDraft(storage, () => t + 500), '');

  saveReloadDraft(storage, 'stale text', () => t);
  assert.equal(takeReloadDraft(storage, () => t + DRAFT_TTL_MS + 1), '');
});
