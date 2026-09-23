// Unit tests for the /debug voice-provider picker: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { voiceOptions, voiceHint, billedText } from './voicepicker.js';

const body = {
  provider: 'gemini',
  model: 'gemini-3.8-live',
  providers: [
    { id: 'gemini', label: 'Gemini Live', model: 'gemini-3.8-live', available: true },
    { id: 'openai', label: 'GPT-Live', model: 'gpt-live-1', available: false, reason: 'no OPENAI_API_KEY', billing: 'per_minute' },
  ],
};

test('voiceOptions disables unavailable providers with the reason', () => {
  const opts = voiceOptions(body);
  assert.equal(opts.length, 2);
  assert.deepEqual(opts[0], { value: 'gemini', text: 'Gemini Live', disabled: false, title: 'Gemini Live (gemini-3.8-live)' });
  assert.equal(opts[1].disabled, true);
  assert.equal(opts[1].text, 'GPT-Live ($/min)');
  assert.match(opts[1].title, /no OPENAI_API_KEY/);
  assert.match(opts[1].title, /billed per minute/);
});

test('voiceOptions tolerates a missing body', () => {
  assert.deepEqual(voiceOptions(null), []);
  assert.deepEqual(voiceOptions({ provider: 'off', providers: [] }), []);
});

test('voiceHint marks the per-minute provider', () => {
  assert.equal(voiceHint(body), 'gemini-3.8-live');
  const openai = { ...body, provider: 'openai', model: 'gpt-live-1', providers: body.providers.map((p) => ({ ...p, available: true })) };
  assert.match(voiceHint(openai), /billed per minute/);
  assert.equal(voiceHint({ provider: 'off' }), 'voice off');
});

test('billedText renders seconds and an estimate', () => {
  assert.equal(billedText({}), '');
  assert.equal(billedText({ voice_billed_seconds: 0 }), '');
  assert.equal(billedText({ voice_billed_seconds: 120, voice_session_open: true }), '120 s billed (~$0.100) · live');
});
