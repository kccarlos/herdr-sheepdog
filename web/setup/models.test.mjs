// Unit tests for the setup page's model finder helpers: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { sortModels, datalistOptions, modelsStatus, probeStatus } from './models.js';

const list = [
  { id: 'z/plain', tools: null },
  { id: 'b/tools', tools: true },
  { id: 'a/no', tools: false },
  { id: 'a/tools', tools: true },
];

test('tool-capable models come first, each group by id', () => {
  assert.deepEqual(sortModels(list).map((m) => m.id), ['a/tools', 'b/tools', 'a/no', 'z/plain']);
  assert.equal(list[0].id, 'z/plain', 'the input is not reordered');
});

test('datalist options keep the bare id as the value and mark tools', () => {
  assert.deepEqual(datalistOptions(list), [
    { value: 'a/tools', label: 'a/tools — tools' },
    { value: 'b/tools', label: 'b/tools — tools' },
    { value: 'a/no', label: '' },
    { value: 'z/plain', label: '' },
  ]);
});

test('status with tool info counts the marked models', () => {
  assert.deepEqual(modelsStatus({ models: list, tool_info: true }), { kind: 'ok', text: '4 models found, 2 marked as supporting tool calling' });
});

test('status without tool info asks to check the pick', () => {
  const models = [{ id: 'a', tools: null }, { id: 'b', tools: null }];
  assert.equal(modelsStatus({ models, tool_info: false }).text, '2 models found. Check that the one you pick supports tool calling.');
  assert.equal(modelsStatus({ models: [{ id: 'a' }], tool_info: false }).text, '1 model found. Check that the one you pick supports tool calling.');
});

test('an error shows its text and its fix', () => {
  const s = modelsStatus({ models: [], error: "This endpoint needs a valid API key.", fix: 'Paste it above, then press Find models again.' });
  assert.equal(s.kind, 'fail');
  assert.equal(s.text, 'This endpoint needs a valid API key. Paste it above, then press Find models again.');
  assert.equal(modelsStatus(null).kind, 'fail');
});

test('probe results', () => {
  assert.deepEqual(probeStatus({ ok: true, detail: 'model m calls tools' }), { kind: 'ok', text: '✓ model m calls tools' });
  assert.equal(probeStatus({ ok: false, detail: 'no tool call', fix: 'pick another' }).text, '✗ no tool call — pick another');
  assert.equal(probeStatus(undefined).kind, 'fail');
});
