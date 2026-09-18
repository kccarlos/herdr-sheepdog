// Unit tests for the /debug conversation grouping: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { groupEvents, Grouper, argSummary, estimateTokens, fmtMs } from './conversation.js';

let t0 = Date.parse('2026-09-18T05:39:00.000Z');
// ev builds one log line `ms` after the previous one.
function ev(type, payload, ms = 100, source = type.split('.')[0]) {
  t0 += ms;
  return { ts: new Date(t0).toISOString(), source, type, payload };
}

const kinds = (items) => items.map((it) => it.kind);

test('input fragments merge into one user message until the model reacts', () => {
  const items = groupEvents([
    ev('voice.transcript.in', { text: 'What', final: false }),
    ev('voice.transcript.in', { text: "'s going", final: false }),
    ev('voice.transcript.in', { text: ' on?', final: false }),
    ev('voice.tool_call', { id: 'c1', name: 'briefing', args: {} }),
    ev('voice.transcript.in', { text: 'Thanks.', final: true }),
  ]);
  assert.deepEqual(kinds(items), ['user', 'tool', 'user']);
  assert.equal(items[0].text, "What's going on?");
  assert.equal(items[0].fragments, 3);
  assert.equal(items[0].final, true);
  assert.equal(items[0].typed, false);
  assert.equal(items[2].text, 'Thanks.');
  assert.equal(items[2].fragments, 1);
});

test('output fragments merge per turn and the next input closes them', () => {
  const g = new Grouper();
  g.add(ev('voice.transcript.out', { text: 'One agent: ', final: false }));
  g.add(ev('voice.transcript.out', { text: 'auth is blocked.', final: false }));
  assert.equal(g.items.length, 1);
  assert.equal(g.items[0].kind, 'assistant');
  assert.equal(g.items[0].final, false, 'still in progress (live turn)');
  assert.equal(g.items[0].text, 'One agent: auth is blocked.');
  g.add(ev('voice.transcript.in', { text: 'ok', final: false }));
  assert.equal(g.items[0].final, true);
  assert.deepEqual(kinds(g.items), ['assistant', 'user']);
});

test('tool call pairs with its result by id and nests the supervisor events in between', () => {
  const items = groupEvents([
    ev('voice.transcript.in', { text: 'tell auth to skip the flaky test', final: false }),
    ev('voice.tool_call', { id: 'call_7', name: 'propose', args: { agent: 'auth', instruction: 'skip the flaky test' } }),
    ev('llm.call', { template: 'resolve_instruction', model: 'gemini-3.5-flash-lite', dur_ms: 786, output: '{"pane_id":"fake:1"}' }),
    ev('pending.created', { id: 'si-1', kind: 'send_instruction' }, 2, 'supervisor'),
    ev('herdr.send', { kind: 'prompt', text: 'skip the flaky test' }, 300),
    ev('herdr.state', { state: 'working' }, 1), // noise: not nested
    ev('voice.tool_result', { id: 'call_7', name: 'propose', spoken: 'Telling auth to skip the flaky test. Sent.', clarify: false, scheduling: 'interrupt' }, 1),
    ev('voice.transcript.out', { text: 'Telling auth to skip the flaky test. Sent.', final: false }, 700),
    ev('voice.turn', { turn: 1, end: 'complete', responded: true, input_to_response_ms: 13210, tool_calls: 1, pushes_spoken: 0, stalled: true, dur_ms: 16000 }, 800),
  ]);
  assert.deepEqual(kinds(items), ['user', 'tool', 'assistant']);
  const tool = items[1];
  assert.equal(tool.name, 'propose');
  assert.equal(tool.id, 'call_7');
  assert.equal(tool.pending, false);
  assert.equal(tool.summary, 'agent="auth", instruction="skip the flaky test"');
  assert.equal(tool.result.spoken, 'Telling auth to skip the flaky test. Sent.');
  assert.equal(tool.result.scheduling, 'interrupt');
  assert.deepEqual(tool.nested.map((e) => e.type), ['llm.call', 'pending.created', 'herdr.send']);
  // The stall shows on the first response of the turn (the tool row).
  assert.equal(tool.turn.responseMs, 13210);
  assert.equal(tool.turn.stalled, true);
  assert.equal(items[2].turn, undefined);
});

test('a tool call whose result never came stays pending', () => {
  const items = groupEvents([
    ev('voice.tool_call', { id: 'c1', name: 'ask', args: { question: 'why did the tests fail?' } }),
    ev('herdr.read', { bytes: 100 }),
    ev('voice.tool_call', { id: 'c2', name: 'cancel', args: {} }, 5000),
    ev('voice.tool_result', { id: 'c2', name: 'cancel', spoken: 'Cancelled.', scheduling: 'interrupt' }),
  ]);
  assert.deepEqual(kinds(items), ['tool', 'tool']);
  assert.equal(items[0].pending, true);
  assert.equal(items[0].result, null);
  assert.deepEqual(items[0].nested.map((e) => e.type), ['herdr.read']);
  assert.equal(items[1].pending, false);
  assert.equal(items[1].result.spoken, 'Cancelled.');
});

test('a cancelled result and an orphan result are shown, not lost', () => {
  const items = groupEvents([
    ev('voice.tool_call', { id: 'c1', name: 'ask', args: { question: 'q' } }),
    ev('voice.tool_result', { id: 'c1', name: 'ask', cancelled: true }),
    ev('voice.tool_result', { id: 'zzz', name: 'briefing', spoken: 'late' }),
  ]);
  assert.deepEqual(kinds(items), ['tool', 'tool']);
  assert.equal(items[0].result.cancelled, true);
  assert.equal(items[1].orphan, true);
  assert.equal(items[1].result.spoken, 'late');
});

test('a turn with no tool call: user, assistant, turn timing on the assistant', () => {
  const items = groupEvents([
    ev('voice.transcript.in', { text: 'How do you say the name of this repo?', final: false }),
    ev('voice.transcript.out', { text: 'You say ', final: false }, 900),
    ev('voice.transcript.out', { text: 'auth.', final: false }),
    ev('voice.usage', { prompt: 6400, response: 20, total: 6420 }),
    ev('voice.turn', { turn: 2, end: 'complete', responded: true, input_to_response_ms: 900, tool_calls: 0, pushes_spoken: 0, stalled: false }),
  ]);
  assert.deepEqual(kinds(items), ['user', 'assistant']);
  assert.equal(items[1].text, 'You say auth.');
  assert.equal(items[1].turn.responseMs, 900);
  assert.equal(items[1].turn.toolCalls, 0);
  assert.equal(items[1].usage.prompt, 6400);
  assert.equal(items[1].final, true);
});

test('pushes render as supervisor messages with their urgency and the events that led to them', () => {
  const items = groupEvents([
    ev('herdr.transition', { to: 'blocked' }),
    ev('herdr.read', { bytes: 2622 }),
    ev('llm.call', { template: 'summarize_block', dur_ms: 1143 }),
    ev('pending.created', { id: 'ap-1', kind: 'answer_prompt' }, 1, 'supervisor'),
    ev('voice.push', { text: 'auth is asking whether it can run a command.', urgency: 'idle' }),
    ev('voice.push', { text: 'One agent: auth is blocked.', urgency: 'silent' }, 60000),
    ev('voice.push', { text: 'Cancelled.', urgency: 'interrupt', skipped: true }),
  ]);
  assert.deepEqual(kinds(items), ['push', 'push', 'push']);
  assert.equal(items[0].urgency, 'idle');
  assert.deepEqual(items[0].nested.map((e) => e.type), ['herdr.read', 'llm.call', 'pending.created']);
  assert.equal(items[1].urgency, 'silent');
  assert.deepEqual(items[1].nested, []);
  assert.equal(items[2].skipped, true);
});

test('session dividers: connect, resume, handle dropped, recycle, stalled', () => {
  const items = groupEvents([
    ev('voice.connect', { key_index: 0, resumed: false, dur_ms: 380 }),
    ev('voice.system_prompt', { text: 'You are the voice of a supervisor. Aliases: auth.', aliases: ['auth'], model: 'gemini-3.8-live' }),
    ev('voice.transcript.in', { text: 'hello?', final: false }),
    ev('voice.stalled', { turn: 3, waited_ms: 10000, stalls: 1 }, 10000),
    ev('voice.transcript.in', { text: ' anyone?', final: false }),
    ev('voice.recycle', { reason: 'stalled', handle: true }, 10000),
    ev('voice.connect', { key_index: 0, resumed: true, dur_ms: 400 }),
    ev('voice.resume', { handle: 'h', resumed: true, replayed_chunks: 3 }),
    ev('voice.disconnected', { error: 'close 1011' }, 60000),
    ev('voice.handle_dropped', { reason: 'close 1011 internal error' }),
    ev('voice.connect', { key_index: 1, resumed: false, dur_ms: 500 }),
    ev('voice.resume', { handle: '', resumed: false, replayed_chunks: 0 }),
  ]);
  assert.deepEqual(kinds(items), ['divider', 'system_prompt', 'user', 'divider', 'user', 'divider', 'divider', 'divider', 'divider', 'divider', 'divider', 'divider']);
  assert.equal(items[0].label, 'new session');
  assert.equal(items[0].detail, 'key 0 · connect 380 ms');
  assert.equal(items[1].tokens, estimateTokens(items[1].text));
  assert.deepEqual(items[1].aliases, ['auth']);
  assert.equal(items[2].final, true, 'the stall divider closes the in-progress message');
  assert.equal(items[3].label, 'stalled');
  assert.equal(items[3].detail, 'no reply 10.0 s after the last input (stall 1)');
  assert.equal(items[4].text, ' anyone?', 'input after a divider starts a new message');
  assert.equal(items[5].label, 'connection recycled');
  assert.equal(items[6].label, 'session resumed');
  assert.equal(items[7].label, 'reconnected, context kept');
  assert.equal(items[9].label, 'context lost');
  assert.equal(items[9].cls, 'lost');
  assert.equal(items[11].label, 'reconnected, context lost');
});

test('typed utterances: voice.text and the text-router path without a voice model', () => {
  const items = groupEvents([
    ev('web.utterance', { text: 'yes', via: 'voice' }), // followed by voice.text: not doubled
    ev('voice.text', { text: 'yes' }),
    ev('voice.tool_call', { id: 'c1', name: 'confirm', args: {} }),
    ev('voice.tool_result', { id: 'c1', name: 'confirm', spoken: 'Told auth yes.', scheduling: 'interrupt' }),
    // --voice=off: web.utterance is the only record, and the daemon speaks the result itself.
    ev('web.utterance', { text: 'stop', via: 'text_router' }),
    ev('voice.tool_call', { id: 'text-3', name: 'cancel', args: {}, via: 'text_router' }, 1, 'web'),
    ev('herdr.send', { keys: ['ctrl+c'] }),
    ev('voice.tool_result', { id: 'text-3', name: 'cancel', spoken: 'Cancelled.', scheduling: 'interrupt', via: 'text_router', dur_ms: 1 }, 1, 'web'),
  ]);
  assert.deepEqual(kinds(items), ['user', 'tool', 'user', 'tool', 'assistant']);
  assert.equal(items[0].text, 'yes');
  assert.equal(items[0].typed, true);
  assert.equal(items[2].text, 'stop');
  assert.equal(items[3].via, 'text_router');
  assert.deepEqual(items[3].nested.map((e) => e.type), ['herdr.send']);
  assert.equal(items[4].synthetic, true);
  assert.equal(items[4].text, 'Cancelled.');
});

test('interrupted marks the assistant message; keys are stable and unique', () => {
  const items = groupEvents([
    ev('voice.transcript.out', { text: 'Telling auth to…', final: false }),
    ev('voice.interrupted', { by: 'push', dropped_chunks: 3 }),
    ev('voice.transcript.out', { text: 'Cancelled.', final: false }),
  ]);
  assert.deepEqual(kinds(items), ['assistant', 'assistant']);
  assert.equal(items[0].interrupted, true);
  assert.equal(items[0].final, true);
  assert.notEqual(items[0].key, items[1].key);
});

test('helpers: argSummary, fmtMs, estimateTokens', () => {
  assert.equal(argSummary({}), '');
  assert.equal(argSummary({ answer: 'no', n: 2, empty: '' }), 'answer="no", n=2');
  assert.equal(argSummary({ instruction: 'x'.repeat(80) }, 10), `instruction="${'x'.repeat(9)}…"`);
  assert.equal(fmtMs(420), '420 ms');
  assert.equal(fmtMs(13210), '13.2 s');
  assert.equal(fmtMs(null), '');
  assert.equal(estimateTokens('a'.repeat(400)), 100);
});

test('brain.* inside an ask call: nested under the call, reads/CLI nested under the brain call', () => {
  const items = groupEvents([
    ev('voice.text', { text: 'why did that one fail?' }),
    ev('voice.tool_call', { id: 'c1', name: 'ask', args: { question: 'why did that one fail?' } }, 400),
    ev('brain.step', { template: 'agent', model: 'gemini-3.5-flash-lite', dur_ms: 700, tool_calls: 1 }, 700, 'brain'),
    ev('brain.tool_call', { id: 'b1', name: 'read_screen', args: { agent: 'auth' } }, 1, 'brain'),
    ev('herdr.read', { source: 'recent_unwrapped', lines: 80, dur_ms: 2 }, 3),
    ev('brain.tool_result', { id: 'b1', name: 'read_screen', dur_ms: 4, result: '80 lines' }, 2, 'brain'),
    ev('brain.tool_call', { id: 'b2', name: 'run_cli', args: { backend: 'codex' } }, 1, 'brain'),
    ev('cli.run', { backend: 'codex', dur_ms: 6000, exit: 0 }, 6000),
    ev('brain.tool_result', { id: 'b2', name: 'run_cli', result: 'the clamp test asserts the wrong bound' }, 3, 'brain'),
    ev('brain.compaction', { before_tokens: 9000, after_tokens: 3000 }, 5, 'brain'),
    ev('brain.step', { template: 'agent', model: 'gemini-3.5-flash-lite', dur_ms: 600, tool_calls: 0 }, 600, 'brain'),
    ev('voice.tool_result', { id: 'c1', name: 'ask', spoken: 'The clamp test asserts the wrong bound.', scheduling: 'interrupt', dur_ms: 9100 }, 2),
    ev('voice.transcript.out', { text: 'The clamp test asserts the wrong bound.', final: false }, 600),
  ]);
  assert.deepEqual(kinds(items), ['user', 'tool', 'assistant']);
  const ask = items[1];
  assert.equal(ask.pending, false);
  assert.deepEqual(ask.nested.map((e) => e.type), ['brain.step', 'brain.tool_call', 'brain.tool_call', 'brain.compaction', 'brain.step']);
  const [, read, cli] = ask.nested;
  assert.equal(read.name, 'read_screen');
  assert.equal(read.pending, false);
  assert.deepEqual(read.children.map((e) => e.type), ['herdr.read']);
  assert.equal(read.result.type, 'brain.tool_result');
  assert.equal(read.durMs, 4, 'dur_ms from the result');
  assert.deepEqual(cli.children.map((e) => e.type), ['cli.run']);
  assert.equal(cli.durMs, 6003, 'no dur_ms: wall-clock distance to the call');
  assert.equal(cli.result.payload.result, 'the clamp test asserts the wrong bound');
  // The wrapped call is a copy: the log event itself is untouched.
  assert.equal(Object.hasOwn(read, 'children'), true);
  const g = new Grouper();
  const raw = ev('brain.tool_call', { id: 'b9', name: 'list_agents', args: {} }, 1, 'brain');
  g.add(raw);
  assert.equal(raw.children, undefined);
  assert.equal(g.loose[0].children.length, 0);
  // An orphan result (unknown id, no open call of that name) is a plain nested row.
  g.add(ev('brain.tool_result', { id: 'nope', name: 'recall', dur_ms: 1 }, 1, 'brain'));
  assert.deepEqual(g.loose.map((e) => e.type), ['brain.tool_call', 'brain.tool_result']);
  assert.equal(g.loose[0].pending, true);
});
