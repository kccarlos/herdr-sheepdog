// Unit tests for the setup page's voice helpers: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { voiceCards, family, pickOf, talkLine, pickReady, modelsLine, modelOptions, changed } from './voice.js';

const state = {
  voice: {
    gemini: { model: 'gemini-3.8-live', voice: 'Puck', default_model: 'gemini-3.8-live', default_voice: 'Puck', voices: ['Puck', 'Kore', 'Charon', 'Aoede'] },
    openai: { model: 'gpt-live-1', voice: 'marin', default_model: 'gpt-live-1', default_voice: 'marin', voices: ['marin', 'cedar'] },
    openai_price: '$0.05 per minute of open session',
  },
};

test('three voice cards, AI Studio first, with the GPT-Live price', () => {
  const cards = voiceCards(state.voice.openai_price);
  assert.deepEqual(cards.map((c) => c.value), ['gemini', 'vertex', 'openai']);
  assert.equal(cards[0].label, 'Gemini Live');
  assert.match(cards[0].note, /free key/);
  assert.match(cards[1].label, /Vertex AI/);
  assert.equal(cards[2].label, 'GPT-Live');
  assert.equal(cards[2].note, 'OpenAI · $0.05 per minute of open session');
  assert.match(voiceCards(undefined)[2].note, /\$0\.05 per minute/);
});

test('AI Studio and Vertex share the Gemini pick', () => {
  assert.equal(family('gemini'), 'gemini');
  assert.equal(family('vertex'), 'gemini');
  assert.equal(family('openai'), 'openai');
  assert.equal(pickOf(state, 'vertex').voice, 'Puck');
  assert.equal(pickOf(state, 'openai').model, 'gpt-live-1');
  assert.deepEqual(pickOf({}, 'gemini').voices, []);
});

test("the you'll-talk-to line", () => {
  assert.equal(talkLine('gemini', 'gemini-3.8-live', 'Puck'), "You'll talk to gemini-3.8-live, voice Puck.");
  assert.equal(talkLine('vertex', 'gemini-3.8-live', 'Kore'), "You'll talk to gemini-3.8-live on Vertex AI, voice Kore.");
  assert.equal(talkLine('openai', 'gpt-live-1', 'marin'), "You'll talk to gpt-live-1, voice marin.");
});

test('the pickers show once the card is ready', () => {
  assert.equal(pickReady('gemini', {}), false);
  assert.equal(pickReady('gemini', { geminiValid: true }), true);
  assert.equal(pickReady('gemini', { geminiSaved: true }), true);
  assert.equal(pickReady('gemini', { geminiSaved: true, geminiTyped: true }), false, 'a new key is typed and not checked yet');
  assert.equal(pickReady('openai', { geminiValid: true }), false);
  assert.equal(pickReady('openai', { openaiValid: true }), true);
  assert.equal(pickReady('openai', { openaiSaved: true }), true);
  assert.equal(pickReady('vertex', { geminiValid: true }), false);
  assert.equal(pickReady('vertex', { vertexOK: true }), true);
});

test('model list lines and options', () => {
  assert.deepEqual(modelsLine({ models: ['a', 'b'], default: 'a' }), { kind: 'muted', text: '2 Live models this key can use. The default is a.' });
  assert.equal(modelsLine({ models: ['a'], default: 'a', note: 'Vertex AI does not list…' }).text, 'Vertex AI does not list…');
  assert.equal(modelsLine({ models: ['a'], error: 'Gemini (AI Studio) rejected the key' }).kind, 'fail');
  assert.equal(modelsLine(null).kind, 'fail');
  assert.deepEqual(modelOptions({ models: ['x', 'y'] }, 'old'), ['old', 'x', 'y']);
  assert.deepEqual(modelOptions({ models: ['x', 'y'] }, 'y'), ['x', 'y']);
  assert.deepEqual(modelOptions(null, 'cur'), ['cur']);
});

test('a save sends only what changed', () => {
  const g = state.voice.gemini;
  assert.deepEqual(changed(g, 'gemini-3.8-live', 'Puck'), {});
  assert.deepEqual(changed(g, 'gemini-3.8-live', 'Kore'), { voice_name: 'Kore' });
  assert.deepEqual(changed(g, 'gemini-new-live', 'Puck'), { voice_model: 'gemini-new-live' });
  assert.deepEqual(changed(state.voice.openai, 'gpt-live-1', ''), {});
});
