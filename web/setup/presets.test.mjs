// Unit tests for the setup page's orchestrator preset helpers: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { presetById, savedPreset, orchValue, panel, keyLabel, keyEnv, savedAs, autoFind, localLine, modelToFill, pickGroups, geminiStatus } from './presets.js';

// The shape /api/setup/state sends (internal/onboard/presets.go).
const presets = [
  { id: 'gemini', label: 'Gemini', kind: 'native', key_env: 'GEMINI_API_KEY', key_needed: true, default_model: 'gemini-3.5-flash', note: 'free, can be slow or busy', key_saved: true },
  { id: 'deepseek', label: 'DeepSeek', kind: 'native', key_env: 'DEEPSEEK_API_KEY', key_needed: true, note: 'cost-effective · did well in our tests' },
  { id: 'vertex', label: 'Vertex AI', kind: 'native', key_env: 'VERTEX_API_KEY', key_needed: true, note: 'Gemini on Google Cloud' },
  { id: 'openrouter', label: 'OpenRouter', kind: 'compat', base_url: 'https://openrouter.ai/api/v1', key_env: 'OPENROUTER_API_KEY', key_needed: true, note: 'one key, many models' },
  { id: 'openai', label: 'OpenAI', kind: 'compat', base_url: 'https://api.openai.com/v1', key_env: 'OPENAI_ORCH_API_KEY', reuse_env: 'OPENAI_API_KEY', key_needed: true, note: 'paid per token', reuse_offered: true },
  { id: 'ollama', label: 'Ollama', kind: 'compat', base_url: 'http://localhost:11434/v1', local: true, key_needed: false, note: 'on this computer, no key' },
  { id: 'lmstudio', label: 'LM Studio', kind: 'compat', base_url: 'http://localhost:1234/v1', local: true, key_needed: false, note: 'on this computer, no key' },
  { id: 'other', label: 'Other URL', kind: 'other', key_needed: false, note: 'any OpenAI-compatible endpoint' },
];
const P = (id) => presetById(presets, id);

test('the saved orchestrator selects its preset, unknown ones fall back to Gemini', () => {
  assert.equal(savedPreset({ presets, orchestrator: { preset: 'ollama' } }), 'ollama');
  assert.equal(savedPreset({ presets, orchestrator: { preset: 'other' } }), 'other');
  assert.equal(savedPreset({ presets, orchestrator: { preset: 'anthropic' } }), 'gemini');
  assert.equal(savedPreset({ presets, orchestrator: {} }), 'gemini');
  assert.equal(savedPreset(null), 'gemini');
  assert.equal(orchValue('other'), 'compat');
  assert.equal(orchValue('openrouter'), 'openrouter');
});

test('native presets show their own block', () => {
  assert.equal(panel(P('gemini')).field, 'gemini');
  assert.equal(panel(P('deepseek')).field, 'deepseek');
  assert.equal(panel(P('vertex')).field, 'vertex');
  assert.equal(panel(null).field, 'gemini');
});

test('a keyed preset asks for its key and hides the base URL', () => {
  const pn = panel(P('openrouter'), false);
  assert.equal(pn.field, 'compat');
  assert.equal(pn.askKey, true);
  assert.equal(pn.other, false);
  assert.equal(pn.details, true, 'the base URL is read-only under Details');
  assert.equal(pn.local, false);
  assert.equal(pn.findLabel, 'Find models');
  assert.equal(keyLabel(P('openrouter')), 'OpenRouter API key');
});

test('a local preset asks for nothing and checks the server', () => {
  for (const id of ['ollama', 'lmstudio']) {
    const pn = panel(P(id), false);
    assert.equal(pn.askKey, false, id);
    assert.equal(pn.local, true, id);
    assert.equal(pn.findLabel, 'Check again', id);
  }
});

test('Other URL keeps the generic fields', () => {
  const pn = panel(P('other'));
  assert.equal(pn.other, true);
  assert.equal(pn.askKey, true);
  assert.equal(pn.details, false);
  assert.equal(keyLabel(P('other')), 'API key (empty for a local server)');
});

test('OpenAI offers the voice key only when one is saved, and hides the key input when ticked', () => {
  assert.equal(panel(P('openai'), false).reuseOffered, true);
  assert.equal(panel(P('openai'), false).askKey, true, 'off by default: ask for a key');
  assert.equal(panel(P('openai'), true).askKey, false);
  const noVoiceKey = { ...P('openai'), reuse_offered: false };
  assert.equal(panel(noVoiceKey, true).reuseOffered, false);
  assert.equal(panel(noVoiceKey, true).askKey, true, 'a tick without a saved voice key changes nothing');
  assert.equal(keyEnv(P('openai'), false), 'OPENAI_ORCH_API_KEY');
  assert.equal(keyEnv(P('openai'), true), 'OPENAI_API_KEY');
  assert.equal(keyEnv(noVoiceKey, true), 'OPENAI_ORCH_API_KEY');
});

test('the Details line names the config entry and the key', () => {
  assert.equal(savedAs(P('openrouter'), false), 'Saved as the [[brain.openai_compat]] entry "openrouter" in config.toml; the key is read from OPENROUTER_API_KEY.');
  assert.equal(savedAs(P('openai'), true), 'Saved as the [[brain.openai_compat]] entry "openai" in config.toml; the key is read from OPENAI_API_KEY (the voice key).');
  assert.equal(savedAs(P('ollama'), false), 'Saved as the [[brain.openai_compat]] entry "ollama" in config.toml; no key needed.');
  assert.equal(savedAs(P('gemini')), '');
});

test('models are listed at once only when they can be', () => {
  assert.equal(autoFind(P('ollama'), {}), true, 'local: the list is the running check');
  assert.equal(autoFind(P('openrouter'), {}), false, 'no key yet');
  assert.equal(autoFind(P('openrouter'), { keyValid: true }), true);
  assert.equal(autoFind({ ...P('openrouter'), key_saved: true }, {}), true, 'a saved key');
  assert.equal(autoFind({ ...P('openrouter'), key_saved: true }, { keyTyped: true }), false, 'a new key is checked first');
  assert.equal(autoFind(P('openai'), { reuse: true }), true);
  assert.equal(autoFind({ ...P('openai'), reuse_offered: false }, { reuse: true }), false);
  assert.equal(autoFind(P('other'), { keyValid: true }), false, 'Other URL: Find models by hand');
  assert.equal(autoFind(P('gemini'), { keyValid: true }), false);
});

test('the local line says running with a count, or how to start it', () => {
  assert.deepEqual(localLine(P('ollama'), { running: true, models: [{ id: 'a' }, { id: 'b' }, { id: 'c' }] }), { kind: 'ok', text: 'Ollama is running, 3 models.' });
  assert.deepEqual(localLine(P('lmstudio'), { running: true, models: [{ id: 'a' }] }), { kind: 'ok', text: 'LM Studio is running, 1 model.' });
  assert.deepEqual(localLine(P('ollama'), { running: false, models: [], error: 'Ollama is not running.' }), { kind: 'fail', text: 'Not running: start Ollama, then press Check again.' });
  assert.equal(localLine(P('ollama'), { running: true, models: [], error: 'Ollama is running but has no models yet.', fix: 'Pull one.' }).text, 'Ollama is running but has no models yet. Pull one.');
  assert.equal(localLine(P('ollama'), null).kind, 'fail');
});

test('the suggestion fills an empty or auto-filled model, never a typed or saved one', () => {
  const r = { suggested: 'gpt-9-mini' };
  assert.equal(modelToFill('', false, r), 'gpt-9-mini');
  assert.equal(modelToFill('old-auto', true, r), 'gpt-9-mini');
  assert.equal(modelToFill('my-model', false, r), null);
  assert.equal(modelToFill('', false, { suggested: '' }), null, 'OpenRouter: the user picks');
  assert.equal(modelToFill('', false, null), null);
});

test('the model select puts tool-capable models first when the endpoint says', () => {
  const models = [{ id: 'z', tools: false }, { id: 'b', tools: true }, { id: 'a', tools: true }, { id: 'y', tools: null }];
  assert.deepEqual(pickGroups(models, true), [
    { label: 'Supports tool calling', ids: ['a', 'b'] },
    { label: 'Other models', ids: ['y', 'z'] },
  ]);
  assert.deepEqual(pickGroups([{ id: 'q' }, { id: 'p' }], false), [{ label: '', ids: ['p', 'q'] }]);
  assert.deepEqual(pickGroups([], true), []);
});

test('the Gemini model line', () => {
  assert.deepEqual(geminiStatus({ models: ['gemini-3.8-flash', 'gemini-3.5-flash'], default: 'gemini-3.5-flash' }), { kind: 'ok', text: '2 models this key can use. The default is gemini-3.5-flash.' });
  assert.equal(geminiStatus({ models: [], error: 'Gemini (AI Studio) rejected the key', fix: 'paste a valid key' }).text, 'Gemini (AI Studio) rejected the key — paste a valid key');
  assert.equal(geminiStatus(null).kind, 'fail');
});
