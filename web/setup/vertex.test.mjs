// Unit tests for the setup page's Vertex AI helpers: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { voiceChoice, vertexFields, savedKeyFor, savedNote, validateBody, checkLine } from './vertex.js';

const key = 'AQ.a-key-that-is-long-enough-0000';
const saved = { keys: { vertex: true }, vertex: { project: 'p-1', default_location: 'us-central1', live_model: 'gemini-3.8-live', voice: 'Puck' } };

test('the saved settings pick the radios', () => {
  assert.equal(voiceChoice({ provider: 'gemini', backend: 'vertex' }), 'vertex');
  assert.equal(voiceChoice({ provider: 'gemini', backend: 'aistudio' }), 'gemini');
  assert.equal(voiceChoice({ provider: 'openai', backend: 'vertex' }), 'openai');
  assert.equal(voiceChoice(undefined), 'gemini');
});

test('which Vertex fields show', () => {
  assert.deepEqual(vertexFields('gemini', 'deepseek'), { block: false, location: false, orchModel: false, geminiNeeded: true });
  assert.deepEqual(vertexFields('vertex', 'deepseek'), { block: true, location: true, orchModel: false, geminiNeeded: false });
  assert.deepEqual(vertexFields('openai', 'vertex'), { block: true, location: false, orchModel: true, geminiNeeded: false });
  assert.deepEqual(vertexFields('vertex', 'gemini'), { block: true, location: true, orchModel: false, geminiNeeded: true });
});

test('the saved key is used only for its own project', () => {
  assert.equal(savedKeyFor(saved, 'p-1'), true);
  assert.equal(savedKeyFor(saved, 'p-2'), false);
  assert.equal(savedKeyFor({ keys: { vertex: false }, vertex: { project: 'p-1' } }, 'p-1'), false);
  assert.match(savedNote(saved), /saved for project p-1.*another project needs its own key/);
  assert.equal(savedNote({ keys: { vertex: false } }), '');
});

test('what is checked, and when', () => {
  assert.equal(validateBody({ key: '', project: '' }, saved), null);
  assert.deepEqual(validateBody({ key, project: '' }, saved), { wait: 'Type the project id to check the key.' });
  assert.deepEqual(validateBody({ key: 'AQ.short', project: 'p-2' }, saved), { wait: 'Keep pasting…' });
  assert.equal(validateBody({ key: '', project: 'p-2' }, saved), null, 'no key and another project: nothing to check');
  assert.deepEqual(validateBody({ key: '', project: 'p-1' }, saved), { kind: 'vertex', key: '', project: 'p-1' });
  assert.deepEqual(validateBody({ key: ' ' + key + ' ', project: ' p-2 ', location: '', voice: true }, saved),
    { kind: 'vertex', key, project: 'p-2', voice: true, location: 'us-central1' });
  assert.equal(validateBody({ key, project: 'p-2', location: 'us', voice: true }, saved).location, 'us');
  assert.equal(validateBody({ key, project: 'p-2', voice: true, model: 'gemini-3.8-live' }, saved).live_model, 'gemini-3.8-live', 'the picked Live model is checked');
  assert.equal(validateBody({ key, project: 'p-2', model: 'gemini-3.8-live' }, saved).live_model, undefined, 'no Live check for the orchestrator alone');
});

test('check result lines', () => {
  assert.deepEqual(checkLine({ status: 'ok', detail: 'Vertex AI accepted the key for project p-1' }), { kind: 'ok', text: '✓ Vertex AI accepted the key for project p-1' });
  assert.deepEqual(checkLine({ status: 'fail', detail: 'location global does not serve gemini-3.8-live', fix: 'use us-central1' }),
    { kind: 'fail', text: '✗ location global does not serve gemini-3.8-live — use us-central1' });
  assert.equal(checkLine({ status: 'warn', detail: 'busy', fix: 'wait' }).text, '! busy — wait');
  assert.equal(checkLine(null).kind, 'fail');
});
