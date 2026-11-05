import { test } from 'node:test';
import assert from 'node:assert/strict';
import { voiceKeyAction } from './shortcuts.js';

test('? opens the keyboard shortcuts dialog, and ? or Esc closes it', () => {
  assert.deepEqual(voiceKeyAction({ key: '?' }, {}), { type: 'open-shortcuts' });
  assert.deepEqual(voiceKeyAction({ key: '?' }, { shortcutsOpen: true }), { type: 'close-shortcuts' });
  assert.deepEqual(voiceKeyAction({ key: 'Escape' }, { shortcutsOpen: true, canStop: true }), { type: 'close-shortcuts' });
  assert.equal(voiceKeyAction({ key: 'm' }, { shortcutsOpen: true, canMute: true }), null, 'other keys do nothing while shortcuts dialog is open');
});

test('typing in an input field keeps ?, m, and Space as text, while Esc still stops playback', () => {
  assert.equal(voiceKeyAction({ key: '?' }, { inField: true }), null);
  assert.equal(voiceKeyAction({ key: 'm' }, { inField: true, canMute: true }), null);
  assert.equal(voiceKeyAction({ key: ' ' }, { inField: true, canStop: true }), null);
  assert.deepEqual(voiceKeyAction({ key: 'Escape' }, { inField: true, canStop: true }), { type: 'stop' });
});

test('Space stops, M toggles mute, modifiers and open cards leave keys alone', () => {
  assert.deepEqual(voiceKeyAction({ key: ' ' }, { canStop: true }), { type: 'stop', preventDefault: true });
  assert.deepEqual(voiceKeyAction({ key: ' ' }, { canStop: false }), { type: 'ignore', preventDefault: true });
  assert.deepEqual(voiceKeyAction({ key: 'm' }, { canMute: true }), { type: 'toggle-mute' });
  assert.deepEqual(voiceKeyAction({ key: 'M' }, { canMute: true }), { type: 'toggle-mute' });
  assert.equal(voiceKeyAction({ key: 'm' }, { canMute: false }), null);
  assert.equal(voiceKeyAction({ key: '?', metaKey: true }, {}), null);
  assert.equal(voiceKeyAction({ key: '?', ctrlKey: true }, {}), null);
  assert.equal(voiceKeyAction({ key: '?' }, { cardOpen: true }), null);
});
