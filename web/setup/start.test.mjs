// Unit tests for the starting-point cards and the summary bar: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { cardRadios, cardFor, initialChoice, nextCard, voiceCost, orchCost, summary } from './start.js';

// The table the daemon serves (internal/onboard/start.go; a Go test keeps
// the fixture in step) and the golden summaries Go checks too.
const here = (f) => new URL(f, import.meta.url);
const info = JSON.parse(readFileSync(here('./start.fixture.json'), 'utf8'));
const cases = JSON.parse(readFileSync(here('./summary.cases.json'), 'utf8'));

const presets = [
  { id: 'anthropic', label: 'Anthropic', kind: 'native' },
  { id: 'gemini', label: 'Gemini', kind: 'native' },
  { id: 'deepseek', label: 'DeepSeek', kind: 'native' },
  { id: 'openrouter', label: 'OpenRouter', kind: 'compat' },
  { id: 'ollama', label: 'Ollama', kind: 'compat', local: true },
  { id: 'other', label: 'Other URL', kind: 'other' },
];
const byId = (id) => presets.find((p) => p.id === id);

test('four cards in order, Custom last', () => {
  assert.deepEqual(info.cards.map((c) => c.id), ['recommended', 'cost', 'free', 'custom']);
  assert.match(info.cards[0].need, /Anthropic API key \(paid\)/);
});

test('a card maps to the voice and orchestrator radios; Custom to none', () => {
  assert.deepEqual(cardRadios(info, 'recommended'), { voice: 'gemini', orch: 'anthropic' });
  assert.deepEqual(cardRadios(info, 'cost'), { voice: 'gemini', orch: 'deepseek' });
  assert.deepEqual(cardRadios(info, 'free'), { voice: 'gemini', orch: 'gemini' });
  assert.equal(cardRadios(info, 'custom'), null);
  assert.equal(cardRadios(info, 'nope'), null);
});

test('the matching card for a saved setup, else Custom', () => {
  assert.equal(cardFor(info, 'gemini', 'anthropic'), 'recommended');
  assert.equal(cardFor(info, 'gemini', 'deepseek'), 'cost');
  assert.equal(cardFor(info, 'gemini', 'gemini'), 'free');
  assert.equal(cardFor(info, 'openai', 'anthropic'), 'custom');
  assert.equal(cardFor(info, 'vertex', 'vertex'), 'custom');
  assert.equal(cardFor(info, 'gemini', 'ollama'), 'custom');
});

test('nothing saved selects Recommended; a saved setup selects its card', () => {
  const fresh = { voice: { setup_needed: true }, presets, start: info };
  assert.deepEqual(initialChoice(fresh, 'gemini', 'gemini'), { card: 'recommended', voice: 'gemini', orch: 'anthropic' });
  const saved = { voice: { setup_needed: false }, presets, start: info };
  assert.deepEqual(initialChoice(saved, 'gemini', 'gemini'), { card: 'free', voice: 'gemini', orch: 'gemini' });
  assert.deepEqual(initialChoice(saved, 'openai', 'ollama'), { card: 'custom', voice: 'openai', orch: 'ollama' });
  // No Anthropic preset (old daemon): keep the saved choice.
  assert.equal(initialChoice({ voice: { setup_needed: true }, presets: [], start: info }, 'gemini', 'gemini').card, 'free');
});

test('arrow keys move between cards and wrap', () => {
  assert.equal(nextCard('ArrowRight', 0, 4), 1);
  assert.equal(nextCard('ArrowDown', 3, 4), 0);
  assert.equal(nextCard('ArrowLeft', 0, 4), 3);
  assert.equal(nextCard('ArrowUp', 2, 4), 1);
  assert.equal(nextCard('Home', 2, 4), 0);
  assert.equal(nextCard('End', 0, 4), 3);
  assert.equal(nextCard('a', 0, 4), -1);
  assert.equal(nextCard('ArrowRight', 0, 0), -1);
});

test('cost lines', () => {
  assert.equal(voiceCost(info, 'gemini'), 'free with daily limits');
  assert.equal(voiceCost(info, 'vertex'), 'billed to your Google Cloud project');
  assert.equal(voiceCost(info, 'openai'), '$0.05/min of open session');
  assert.equal(orchCost(info, byId('anthropic'), 'claude-opus-5-5'), '$4/$20 per million tokens');
  assert.equal(orchCost(info, byId('anthropic'), 'claude-sonnet-5-5'), '$2/$10 per million tokens');
  assert.equal(orchCost(info, byId('anthropic'), 'claude-future'), 'pay per use');
  assert.equal(orchCost(info, byId('deepseek'), 'deepseek-flash'), 'pay per use');
  assert.equal(orchCost(info, byId('openrouter'), 'x'), 'pay per use');
  assert.equal(orchCost(info, byId('ollama'), 'x'), 'free on this computer');
  assert.equal(orchCost(info, byId('gemini'), 'x'), 'free with daily limits');
  assert.equal(orchCost(info, { id: 'vertex', label: 'Vertex AI', kind: 'native' }, 'x'), 'billed to your Google Cloud project');
});

test('the golden summaries (Go checks the same file)', () => {
  const all = [...presets, { id: 'vertex', label: 'Vertex AI', kind: 'native' }];
  for (const c of cases) {
    const preset = all.find((p) => p.id === c.preset);
    assert.equal(summary(info, { voice: c.voice, voiceModel: c.voice_model, preset, orchModel: c.orch_model, otherName: c.other_name }), c.want);
  }
});
