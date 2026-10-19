// Unit tests for the starting-point cards and the summary bar: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { startCards, cardRadios, cardFor, initialChoice, nextCard, voiceCost, orchCost, summary } from './start.js';

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
  assert.deepEqual(startCards.map((c) => c.id), ['recommended', 'cost', 'free', 'custom']);
  assert.match(startCards[0].need, /Anthropic API key \(paid\)/);
});

test('a card maps to the voice and orchestrator radios; Custom to none', () => {
  assert.deepEqual(cardRadios('recommended'), { voice: 'gemini', orch: 'anthropic' });
  assert.deepEqual(cardRadios('cost'), { voice: 'gemini', orch: 'deepseek' });
  assert.deepEqual(cardRadios('free'), { voice: 'gemini', orch: 'gemini' });
  assert.equal(cardRadios('custom'), null);
  assert.equal(cardRadios('nope'), null);
});

test('the matching card for a saved setup, else Custom', () => {
  assert.equal(cardFor('gemini', 'anthropic'), 'recommended');
  assert.equal(cardFor('gemini', 'deepseek'), 'cost');
  assert.equal(cardFor('gemini', 'gemini'), 'free');
  assert.equal(cardFor('openai', 'anthropic'), 'custom');
  assert.equal(cardFor('vertex', 'vertex'), 'custom');
  assert.equal(cardFor('gemini', 'ollama'), 'custom');
});

test('nothing saved selects Recommended; a saved setup selects its card', () => {
  const fresh = { voice: { setup_needed: true }, presets };
  assert.deepEqual(initialChoice(fresh, 'gemini', 'gemini'), { card: 'recommended', voice: 'gemini', orch: 'anthropic' });
  const saved = { voice: { setup_needed: false }, presets };
  assert.deepEqual(initialChoice(saved, 'gemini', 'gemini'), { card: 'free', voice: 'gemini', orch: 'gemini' });
  assert.deepEqual(initialChoice(saved, 'openai', 'ollama'), { card: 'custom', voice: 'openai', orch: 'ollama' });
  // No Anthropic preset (old daemon): keep the saved choice.
  assert.equal(initialChoice({ voice: { setup_needed: true }, presets: [] }, 'gemini', 'gemini').card, 'free');
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
  assert.equal(voiceCost('gemini'), 'free with daily limits');
  assert.equal(voiceCost('vertex'), 'billed to your Google Cloud project');
  assert.equal(voiceCost('openai'), '$0.05/min of open session');
  assert.equal(orchCost(byId('anthropic'), 'claude-opus-5-5'), '$4/$20 per million tokens');
  assert.equal(orchCost(byId('anthropic'), 'claude-sonnet-5-5'), '$2/$10 per million tokens');
  assert.equal(orchCost(byId('anthropic'), 'claude-future'), 'pay per use');
  assert.equal(orchCost(byId('deepseek'), 'deepseek-flash'), 'pay per use');
  assert.equal(orchCost(byId('openrouter'), 'x'), 'pay per use');
  assert.equal(orchCost(byId('ollama'), 'x'), 'free on this computer');
  assert.equal(orchCost(byId('gemini'), 'x'), 'free with daily limits');
  assert.equal(orchCost({ id: 'vertex', label: 'Vertex AI', kind: 'native' }, 'x'), 'billed to your Google Cloud project');
});

test('the summary for each starting point and a few mixes', () => {
  assert.equal(
    summary({ voice: 'gemini', voiceModel: 'gemini-3.8-live', preset: byId('anthropic'), orchModel: 'claude-opus-5-5' }),
    'You talk to gemini-3.8-live (Google AI Studio). Your agents are handled by claude-opus-5-5 (Anthropic). Cost: free with daily limits · $4/$20 per million tokens');
  assert.equal(
    summary({ voice: 'gemini', voiceModel: 'gemini-3.8-live', preset: byId('deepseek'), orchModel: 'deepseek-flash' }),
    'You talk to gemini-3.8-live (Google AI Studio). Your agents are handled by deepseek-flash (DeepSeek). Cost: free with daily limits · pay per use');
  assert.equal(
    summary({ voice: 'gemini', voiceModel: 'gemini-3.8-live', preset: byId('gemini'), orchModel: 'gemini-3.5-flash' }),
    'You talk to gemini-3.8-live (Google AI Studio). Your agents are handled by gemini-3.5-flash (Gemini). Cost: free with daily limits · free with daily limits');
  assert.equal(
    summary({ voice: 'openai', voiceModel: 'gpt-live-1', preset: byId('ollama'), orchModel: 'qwen3' }),
    'You talk to gpt-live-1 (OpenAI). Your agents are handled by qwen3 (Ollama). Cost: $0.05/min of open session · free on this computer');
  assert.match(summary({ voice: 'vertex', voiceModel: 'gemini-3.8-live', preset: { id: 'vertex', label: 'Vertex AI', kind: 'native' }, orchModel: 'gemini-3.5-flash' }),
    /\(Google Vertex AI\)\. .*\(Vertex AI\)\. Cost: billed to your Google Cloud project · billed to your Google Cloud project$/);
});

test('the summary has fallbacks while a model is not chosen yet', () => {
  const s = summary({ voice: 'gemini', voiceModel: '', preset: byId('openrouter'), orchModel: '' });
  assert.match(s, /You talk to a Live model \(Google AI Studio\)/);
  assert.match(s, /handled by a model of your choice \(OpenRouter\)/);
  assert.match(summary({ voice: 'gemini', voiceModel: 'm', preset: byId('other'), orchModel: 'x', otherName: 'groq' }), /\(groq\)/);
  assert.match(summary({ voice: 'gemini', voiceModel: 'm', preset: byId('other'), orchModel: 'x' }), /\(your server\)/);
});
