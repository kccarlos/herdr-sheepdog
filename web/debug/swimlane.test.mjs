// Unit tests for the /debug swimlane layout (swimlane.js): `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { buildRows, LANES, GAP_MS, STALL_MS, latencyLevel, fmtClock } from './swimlane.js';
import { toolFamily } from './grouping.js';

let t0 = Date.parse('2026-09-18T05:39:00.000Z');
// ev builds one log line `ms` after the previous one.
function ev(type, payload, ms = 100, source = type.split('.')[0], pane) {
  t0 += ms;
  const e = { ts: new Date(t0).toISOString(), source, type, payload };
  if (pane) e.pane_id = pane;
  return e;
}

const cards = (rows) => rows.filter((r) => r.kind === 'card');
const arrows = (rows) => cards(rows).filter((r) => r.arrowOut).map((r) => `${r.arrowOut.from}>${r.arrowOut.to}:${r.arrowOut.kind}`);
const types = (rows) => rows.map((r) => (r.kind === 'card' ? r.type : r.kind));

test('lanes and helpers', () => {
  assert.deepEqual(LANES.map((l) => l.id), ['you', 'voice', 'sup', 'herdr']);
  assert.equal(latencyLevel(null), null);
  assert.equal(latencyLevel(200), 'ok');
  assert.equal(latencyLevel(1500), 'warn');
  assert.equal(latencyLevel(6000), 'slow');
  assert.match(fmtClock(Date.parse('2026-09-18T05:39:07.042Z')), /^\d\d:\d\d:07\.042$/);
  assert.deepEqual(toolFamily('voice.tool_call'), { prefix: 'voice', phase: 'call' });
  assert.deepEqual(toolFamily('brain.tool_result'), { prefix: 'brain', phase: 'result' });
  assert.equal(toolFamily('voice.tool_cancelled'), null);
  assert.equal(toolFamily('herdr.read'), null);
});

test('arrows per interaction kind, in their lanes', () => {
  const { rows } = buildRows([
    ev('voice.transcript.in', { text: 'tell auth ', final: false }),
    ev('voice.transcript.in', { text: 'to skip the flaky test', final: false }),
    ev('voice.tool_call', { id: 'c1', name: 'propose', args: { agent: 'auth', instruction: 'skip the flaky test' } }, 400),
    ev('llm.call', { template: 'resolve_instruction', model: 'gemini-3.5-flash-lite', dur_ms: 786 }, 786),
    ev('pending.created', { id: 'si-1', kind: 'send_instruction' }, 2, 'supervisor', 'wA:p1'),
    ev('herdr.send', { kind: 'prompt', text: 'skip the flaky test' }, 300, 'herdr', 'wA:p1'),
    ev('voice.tool_result', { id: 'c1', name: 'propose', spoken: 'Sent.', clarify: false, scheduling: 'interrupt', dur_ms: 1090, wait_ms: 0 }, 1),
    ev('voice.transcript.out', { text: 'Telling auth ', final: false }, 700),
    ev('voice.transcript.out', { text: 'to skip the flaky test.', final: false }, 300),
    ev('herdr.state', { state: 'working', agent: 'codex', source: 'snapshot' }, 100, 'herdr', 'wA:p1'),
    ev('herdr.transition', { from: 'done', to: 'working', agent: 'codex', name: 'one', workspace_label: 'auth', source: 'snapshot' }, 1, 'herdr', 'wA:p1'),
    ev('herdr.read', { source: 'recent_unwrapped', lines: 80, dur_ms: 1 }, 3000, 'herdr', 'wA:p1'),
    ev('cli.run', { backend: 'codex', dur_ms: 7200, exit: 0 }, 7200),
    ev('voice.push', { text: 'auth finished.', urgency: 'idle' }, 10),
    ev('voice.transcript.out', { text: 'auth finished.', final: false }, 650),
  ]);
  // One merged card per transcript; every hop has its arrow.
  assert.deepEqual(types(rows), [
    'voice.transcript.in', 'voice.tool_call', 'llm.call', 'pending.created', 'herdr.send', 'voice.tool_result',
    'voice.transcript.out', 'herdr.state', 'herdr.transition', 'herdr.read', 'gap', 'cli.run', 'voice.push', 'voice.transcript.out',
  ]);
  assert.deepEqual(arrows(rows), [
    'you>voice:input', 'voice>sup:call', 'sup>herdr:call', 'sup>herdr:call', 'sup>voice:result',
    'voice>you:speech', 'herdr>sup:state', 'sup>herdr:call', 'sup>herdr:call', 'sup>voice:push', 'voice>you:speech',
  ]);
  const c = cards(rows);
  const [input, call, llm, pending, send, result, speech, , transition, read, cli, push, spoken] = c;
  assert.equal(input.lane, 'you');
  assert.equal(input.text, 'tell auth to skip the flaky test');
  assert.equal(input.fragments, 2);
  assert.equal(input.arrowOut.label, 'speech');
  assert.equal(call.lane, 'voice');
  assert.match(call.arrowOut.label, /^propose\(agent="auth", instruction=/);
  assert.equal(llm.lane, 'herdr');
  assert.equal(llm.arrowOut.label, 'resolve_instruction · gemini-3.5-flash-lite · 786 ms');
  assert.equal(llm.arrowOut.level, 'ok');
  assert.equal(pending.lane, 'sup');
  assert.equal(pending.arrowOut, null);
  assert.equal(send.arrowOut.label, 'prompt · "skip the flaky test"');
  assert.equal(send.arrowOut.dashed, false, 'a send is a solid call');
  assert.equal(result.lane, 'sup');
  assert.equal(result.arrowOut.dashed, true, 'results are dashed');
  assert.equal(result.arrowOut.label, 'propose · 1.1 s · interrupt', 'dur_ms + scheduling');
  assert.equal(result.arrowOut.level, 'warn');
  assert.equal(speech.lane, 'voice');
  assert.equal(speech.text, 'Telling auth to skip the flaky test.');
  assert.equal(speech.arrowOut.label, '🔊');
  assert.equal(transition.state, 'working');
  assert.ok(transition.cls.includes('state-working'), 'transition card carries the state colour');
  assert.equal(transition.arrowOut.label, 'done → working');
  assert.equal(transition.arrowOut.state, 'working');
  assert.equal(read.arrowOut.dashed, true, 'a read is dashed');
  assert.equal(cli.arrowOut.label, 'codex · 7.2 s');
  assert.equal(cli.arrowOut.level, 'slow');
  assert.equal(push.lane, 'sup');
  assert.equal(push.arrowOut.dashed, false);
  assert.equal(push.arrowOut.label, 'push · idle');
  assert.equal(spoken.pushDelayMs, 650, 'push → speech delay');
  assert.equal(spoken.arrowOut.label, '🔊 650 ms after push');
  assert.equal(spoken.arrowOut.level, 'ok');
});

test('tool_result without dur_ms falls back to the wall-clock Δ; wait_ms is shown', () => {
  const { rows } = buildRows([
    ev('voice.tool_call', { id: 'c1', name: 'briefing', args: {} }),
    ev('voice.tool_result', { id: 'c1', name: 'briefing', spoken: 'All quiet.', scheduling: 'interrupt' }, 640),
    ev('voice.tool_call', { id: 'c2', name: 'briefing', args: {} }, 3000),
    ev('voice.tool_result', { id: 'c2', name: 'briefing', spoken: 'All quiet.', scheduling: 'interrupt', dur_ms: 0, wait_ms: 612 }, 612),
  ]);
  const results = cards(rows).filter((r) => r.type === 'voice.tool_result');
  assert.equal(results[0].arrowOut.label, 'briefing · Δ 640 ms · interrupt');
  assert.equal(results[0].latencyMs, 640);
  assert.equal(results[1].arrowOut.label, 'briefing · 0 ms +612 ms wait · interrupt');
  assert.equal(results[1].latencyMs, 0);
});

test('chain grouping: a user turn owns its call, the nested work, the result and the speech', () => {
  const { rows, chains } = buildRows([
    ev('voice.text', { text: 'what is going on?' }),
    ev('voice.tool_call', { id: 'c1', name: 'briefing', args: {} }, 400),
    ev('voice.tool_result', { id: 'c1', name: 'briefing', spoken: 'One agent working.', scheduling: 'interrupt', dur_ms: 0 }, 1),
    ev('voice.transcript.out', { text: 'One agent working.', final: false }, 600),
    ev('voice.turn', { turn: 1, end: 'complete', responded: true, input_to_response_ms: 1001, tool_calls: 1, dur_ms: 1600 }, 300),
    // A herdr story with no tool call: transition → read → llm → pending → push → speech.
    ev('herdr.state', { state: 'blocked', agent: 'agy', source: 'http' }, 4000, 'herdr', 'wA:p1'),
    ev('herdr.transition', { from: 'done', to: 'blocked', agent: 'agy', workspace_label: 'auth', name: 'one', source: 'http' }, 3000, 'herdr', 'wA:p1'),
    ev('herdr.read', { source: 'recent_unwrapped', lines: 80, dur_ms: 1 }, 3, 'herdr', 'wA:p1'),
    ev('llm.call', { template: 'summarize_block', model: 'gemini-3.5-flash-lite', dur_ms: 1278 }, 1278),
    ev('pending.created', { id: 'ap-1', kind: 'answer_prompt', question: 'accept the edit?' }, 2, 'supervisor', 'wA:p1'),
    ev('voice.push', { text: 'auth-one is waiting for you to accept the edit.', urgency: 'idle' }, 1),
    ev('voice.transcript.out', { text: 'Auth one is waiting.', final: false }, 669),
    // Next user turn: confirm.
    ev('voice.text', { text: 'Yes.' }, 2000),
    ev('voice.tool_call', { id: 'c2', name: 'confirm', args: {} }, 470),
    ev('herdr.send', { kind: 'keys', keys: ['1'] }, 2, 'herdr', 'wA:p1'),
    ev('pending.confirmed', { id: 'ap-1', kind: 'answer_prompt', keys: ['1'] }, 3, 'supervisor', 'wA:p1'),
    ev('voice.tool_result', { id: 'c2', name: 'confirm', spoken: 'Done.', scheduling: 'interrupt', dur_ms: 5 }, 12),
    ev('voice.transcript.out', { text: 'Done.', final: false }, 500),
  ]);
  const c = cards(rows);
  const chainOf = (type, n = 0) => c.filter((r) => r.type === type)[n].chain;
  assert.equal(chainOf('voice.text'), 'turn:1');
  assert.equal(chainOf('voice.tool_call'), 'turn:1');
  assert.equal(chainOf('voice.tool_result'), 'turn:1');
  assert.equal(chainOf('voice.transcript.out'), 'turn:1');
  assert.equal(chainOf('voice.turn'), 'turn:1');
  const loose = chainOf('herdr.transition');
  assert.match(loose, /^loose:/);
  for (const type of ['herdr.state', 'herdr.read', 'llm.call', 'pending.created', 'voice.push']) assert.equal(chainOf(type), loose, type);
  assert.equal(chainOf('voice.transcript.out', 1), loose, 'the speech after the push belongs to the push chain');
  const turn2 = chainOf('voice.text', 1);
  assert.match(turn2, /^turn:/);
  assert.notEqual(turn2, 'turn:1');
  for (const type of ['herdr.send', 'pending.confirmed']) assert.equal(chainOf(type), turn2, type);
  assert.equal(chainOf('voice.tool_call', 1), turn2);
  assert.equal(chainOf('voice.transcript.out', 2), turn2);
  assert.equal(chains.get('turn:1').kind, 'turn');
  assert.equal(chains.get(loose).kind, 'loose');
  assert.ok(c.find((r) => r.type === 'voice.tool_call').chainRoot);
});

test('voice.turn attaches input_to_response_ms to the You → Voice arrow, red from 10 s', () => {
  const build = (ms, extra = {}) => buildRows([
    ev('voice.text', { text: 'hello' }),
    ev('voice.tool_call', { id: 'c1', name: 'briefing', args: {} }, 200),
    ev('voice.tool_result', { id: 'c1', name: 'briefing', spoken: 'hi', scheduling: 'interrupt', dur_ms: 0 }, 1),
    ev('voice.transcript.out', { text: 'hi', final: false }, 600),
    ev('voice.turn', { turn: 1, end: 'complete', responded: true, input_to_response_ms: ms, tool_calls: 1, dur_ms: ms + 800, ...extra }, 300),
  ]).rows;
  let input = cards(build(812))[0];
  assert.equal(input.arrowOut.label, 'typed · ⏱ 812 ms');
  assert.equal(input.arrowOut.level, 'ok');
  assert.equal(input.latency.slow, false);
  assert.equal(input.latency.label, 'answered after 812 ms');
  input = cards(build(3200))[0];
  assert.equal(input.arrowOut.level, 'warn');
  input = cards(build(13210, { stalled: true }))[0];
  assert.equal(input.arrowOut.label, 'typed · ⏱ 13.2 s');
  assert.equal(input.arrowOut.level, 'slow', `≥ ${STALL_MS} ms is red`);
  assert.equal(input.latency.slow, true);
  assert.equal(input.latency.label, 'answered after 13.2 s (stalled)');
  const turnCard = cards(build(13210, { stalled: true })).find((r) => r.type === 'voice.turn');
  assert.ok(turnCard.cls.includes('error'));
  input = cards(build(null, { responded: false }))[0];
  assert.equal(input.arrowOut.label, 'typed · ⏱ no response');
  assert.equal(input.arrowOut.level, 'slow');
});

test('gap markers compress quiet time and the gutter marks minute changes', () => {
  const { rows } = buildRows([
    ev('voice.text', { text: 'a' }),
    ev('voice.text', { text: 'b' }, 4999),
    ev('voice.text', { text: 'c' }, 5001),
    ev('voice.text', { text: 'd' }, 12200),
    ev('voice.text', { text: 'e' }, 61000),
  ]);
  assert.deepEqual(types(rows), ['voice.text', 'voice.text', 'gap', 'voice.text', 'gap', 'voice.text', 'gap', 'voice.text']);
  const gaps = rows.filter((r) => r.kind === 'gap');
  assert.deepEqual(gaps.map((g) => g.label), ['5 s', '12.2 s', '61 s']);
  assert.equal(gaps[0].ms, 5001);
  assert.ok(GAP_MS === 5000);
  // The clock: full HH:MM:SS.mmm when the minute changes, seconds otherwise.
  const c = cards(rows);
  assert.equal(c[0].minuteChanged, true, 'first row shows the full clock');
  assert.match(c[0].time, /^\d\d:\d\d:\d\d\.\d{3}$/);
  assert.equal(c[1].minuteChanged, false);
  assert.equal(c[4].minuteChanged, true);
  // A custom threshold.
  const { rows: r2 } = buildRows([ev('voice.text', { text: 'a' }), ev('voice.text', { text: 'b' }, 900)], { gapMs: 500 });
  assert.deepEqual(types(r2), ['voice.text', 'gap', 'voice.text']);
});

test('a hidden lane collapses to stubs; arrows between two hidden lanes vanish', () => {
  const events = [
    ev('voice.text', { text: 'status?' }),
    ev('voice.tool_call', { id: 'c1', name: 'briefing', args: {} }, 300),
    ev('herdr.read', { source: 'recent', lines: 10, dur_ms: 1 }, 2, 'herdr', 'wA:p1'),
    ev('voice.tool_result', { id: 'c1', name: 'briefing', spoken: 'fine', scheduling: 'interrupt', dur_ms: 3 }, 2),
    ev('voice.transcript.out', { text: 'fine', final: false }, 500),
  ];
  const all = buildRows(events, {});
  assert.deepEqual(all.lanes, ['you', 'voice', 'sup', 'herdr']);
  assert.equal(cards(all.rows)[1].col, 1);
  assert.equal(cards(all.rows)[1].arrowOut.stub, null);

  const noVoice = buildRows(events, { hiddenLanes: ['voice'] });
  assert.deepEqual(noVoice.lanes, ['you', 'sup', 'herdr']);
  const c = cards(noVoice.rows);
  assert.equal(c.length, 5, 'cards in a hidden lane stay when they have an arrow to a visible lane');
  const call = c[1];
  assert.equal(call.laneHidden, true);
  assert.equal(call.col, null);
  assert.equal(call.arrowOut.stub, 'from', 'the call comes out of the hidden Voice lane');
  assert.equal(call.arrowOut.fromCol, 1, 'the stub sits in the Supervisor column');
  assert.equal(call.arrowOut.toCol, 1);
  assert.equal(call.arrowOut.stubSide, 'left');
  const result = c[3];
  assert.equal(result.arrowOut.stub, 'to');
  assert.equal(result.arrowOut.stubSide, 'left', 'points left, towards where Voice would be');
  const input = c[0];
  assert.equal(input.arrowOut.stub, 'to');
  assert.equal(input.arrowOut.stubSide, 'right');
  const read = c[2];
  assert.equal(read.arrowOut.stub, null, 'Supervisor → herdr is untouched');
  assert.equal(read.col, 2);

  const onlyHerdr = buildRows(events, { hiddenLanes: ['you', 'voice', 'sup'] });
  assert.deepEqual(types(onlyHerdr.rows), ['herdr.read'], 'a card whose lane and arrow target are both hidden is dropped');
  assert.equal(cards(onlyHerdr.rows)[0].arrowOut.stub, 'from');
});

test('filters: sources, search, stats, silent pushes, pane highlight', () => {
  const events = [
    ev('web.client.stats', { frames_in: 10, frames_out: 2 }),
    ev('voice.usage', { prompt: 1, response: 2, total: 3 }),
    ev('voice.push', { text: 'briefing: all quiet', urgency: 'silent' }),
    ev('supervisor.push_quiet', { text: 'x' }, 1, 'supervisor'),
    ev('voice.push', { text: 'auth is blocked', urgency: 'idle' }, 500),
    ev('herdr.transition', { from: 'done', to: 'blocked', agent: 'agy', source: 'http' }, 1, 'herdr', 'wA:p1'),
    ev('herdr.transition', { from: 'done', to: 'working', agent: 'codex', source: 'http' }, 1, 'herdr', 'wB:p1'),
  ];
  const dflt = buildRows(events, {});
  assert.deepEqual(types(dflt.rows), ['voice.push', 'herdr.transition', 'herdr.transition'], 'stats and silent pushes hidden by default');
  assert.equal(dflt.total, 7);
  assert.equal(dflt.shown, 3);
  const shown = buildRows(events, { hideStats: false, hideSilent: false });
  assert.equal(types(shown.rows).length, 7);
  assert.equal(cards(shown.rows)[2].silent, true);
  assert.equal(cards(shown.rows)[2].arrowOut.dashed, true, 'silent pushes are dashed');
  assert.equal(cards(shown.rows)[4].arrowOut.dashed, false);
  const noHerdr = buildRows(events, { hiddenSources: new Set(['herdr']) });
  assert.deepEqual(types(noHerdr.rows), ['voice.push']);
  const unknownSrc = buildRows([ev('brain.step', { template: 'agent', dur_ms: 5 }, 1, 'brain')], { hiddenSources: new Set(['voice']) });
  assert.equal(unknownSrc.rows.length, 1, 'an unknown source is never filtered out');
  const search = buildRows(events, { search: 'codex' });
  assert.deepEqual(types(search.rows), ['herdr.transition']);
  const pane = buildRows(events, { pane: 'wB:p1' });
  assert.deepEqual(cards(pane.rows).map((r) => r.paneHit), [false, false, true]);
});

test('a silent push does not claim the speech that follows; a skipped push is dashed', () => {
  const { rows } = buildRows([
    ev('voice.text', { text: 'status?' }),
    ev('voice.tool_call', { id: 'c1', name: 'briefing', args: {} }, 300),
    ev('voice.push', { text: 'periodic briefing', urgency: 'silent' }, 5),
    ev('voice.tool_result', { id: 'c1', name: 'briefing', spoken: 'fine', scheduling: 'interrupt', dur_ms: 0 }, 5),
    ev('voice.transcript.out', { text: 'fine', final: false }, 500),
    ev('voice.push', { text: 'not sent', urgency: 'idle', skipped: true }, 100),
  ], { hideSilent: false });
  const c = cards(rows);
  const speech = c.find((r) => r.type === 'voice.transcript.out');
  assert.equal(speech.chain, 'turn:1');
  assert.equal(speech.pushDelayMs, undefined);
  assert.equal(c[2].silent, true);
  assert.notEqual(c[2].chain, 'turn:1', 'the silent push starts its own chain');
  const skipped = c[c.length - 1];
  assert.equal(skipped.skipped, true);
  assert.equal(skipped.arrowOut.dashed, true);
  assert.equal(skipped.arrowOut.label, 'push · idle · not sent');
});

test('dividers: session lifecycle, system prompt (expandable text), daemon start', () => {
  const { rows } = buildRows([
    ev('daemon.start', { version: 'v0.9', herdr: 'mock', voice: 'off' }, 1, 'daemon'),
    ev('voice.connect', { key_index: 0, dur_ms: 420, resumed: false }),
    ev('voice.system_prompt', { text: 'You are sheepdog. '.repeat(50), model: 'gemini-3.8-live', aliases: ['auth-one'] }),
    ev('voice.resume', { resumed: true, replayed_chunks: 12 }),
    ev('voice.handle_dropped', { reason: 'expired' }),
    ev('voice.recycle', { reason: 'idle', handle: true }),
    ev('voice.stalled', { waited_ms: 12000, stalls: 1 }),
    ev('voice.goaway', { time_left_ms: 30000 }),
    ev('voice.disconnected', { error: '1011' }),
  ]);
  assert.ok(rows.every((r) => r.kind === 'divider'));
  assert.deepEqual(rows.map((r) => r.cls), ['daemon', 'connect', 'prompt', 'resume', 'lost', 'recycle', 'stalled', 'recycle', 'lost']);
  assert.equal(rows[2].label, 'system prompt');
  assert.equal(rows[2].detail, '~225 tokens · gemini-3.8-live · aliases auth-one');
  assert.equal(rows[2].text.length, 900);
  assert.equal(rows[0].detail, 'v0.9 · herdr=mock · voice=off');
  assert.equal(rows[6].detail, 'no reply 12.0 s after the last input (stall 1)');
});

test('web.client.* are small You-lane cards; typed text-router input goes to the Supervisor', () => {
  const { rows } = buildRows([
    ev('web.client.connect', { client_id: 1 }, 1, 'web'),
    ev('web.utterance', { text: 'What is going on?', via: 'voice' }, 1, 'web'),
    ev('voice.text', { text: 'What is going on?' }, 0),
    ev('web.utterance', { text: 'status', via: 'text' }, 3000, 'web'),
    ev('voice.tool_result', { id: '', name: 'briefing', spoken: 'All quiet.', scheduling: 'interrupt', via: 'text', dur_ms: 0 }, 2),
  ]);
  const c = cards(rows);
  assert.deepEqual(types(rows), ['web.client.connect', 'voice.text', 'web.utterance', 'voice.tool_result', 'voice.transcript.out']);
  assert.equal(c[0].lane, 'you');
  assert.ok(c[0].cls.includes('minor'));
  assert.equal(c[0].title, 'connect');
  assert.equal(c[1].typed, true, 'web.utterance via=voice is folded into voice.text: one card');
  assert.equal(c[2].arrowOut.to, 'sup');
  assert.equal(c[2].arrowOut.label, 'typed · text router');
  assert.equal(c[3].orphan, true, 'a result with no call is marked, not lost');
  assert.equal(c[4].synthetic, true, 'the daemon speaks the result itself without a voice model');
  assert.equal(c[4].chain, c[3].chain);
});

test('brain.* inside a voice.tool_call ask: generic pairing, nested under the ask chain', () => {
  const { rows, chains } = buildRows([
    ev('voice.text', { text: 'why did that one fail?' }),
    ev('voice.tool_call', { id: 'c1', name: 'ask', args: { question: 'why did that one fail?' } }, 400),
    ev('brain.step', { template: 'agent', model: 'gemini-3.5-flash-lite', dur_ms: 700, tool_calls: 1 }, 700, 'brain'),
    ev('brain.tool_call', { id: 'b1', name: 'read_screen', args: { agent: 'auth' } }, 1, 'brain'),
    ev('herdr.read', { source: 'recent_unwrapped', lines: 80, dur_ms: 2 }, 3, 'herdr', 'wA:p1'),
    ev('brain.tool_result', { id: 'b1', name: 'read_screen', dur_ms: 4, result: '80 lines' }, 2, 'brain'),
    ev('brain.step', { template: 'agent', model: 'gemini-3.5-flash-lite', dur_ms: 800, tool_calls: 1 }, 800, 'brain'),
    ev('brain.tool_call', { id: 'b2', name: 'run_cli', args: { backend: 'codex', question: 'why fail' } }, 1, 'brain'),
    ev('cli.run', { backend: 'codex', dur_ms: 6000, exit: 0 }, 6000),
    ev('brain.tool_result', { id: 'b2', name: 'run_cli', result: 'the clamp test asserts the wrong bound' }, 3, 'brain'),
    ev('brain.compaction', { before_tokens: 9000, after_tokens: 3000 }, 5, 'brain'),
    ev('brain.step', { template: 'agent', model: 'gemini-3.5-flash-lite', dur_ms: 600, tool_calls: 0 }, 600, 'brain'),
    ev('voice.tool_result', { id: 'c1', name: 'ask', spoken: 'The clamp test asserts the wrong bound.', scheduling: 'interrupt', dur_ms: 9100, wait_ms: 0 }, 2),
    ev('voice.transcript.out', { text: 'The clamp test asserts the wrong bound.', final: false }, 600),
    ev('voice.turn', { turn: 1, end: 'complete', responded: true, input_to_response_ms: 10400, tool_calls: 1, dur_ms: 12000 }, 500),
  ]);
  const c = cards(rows);
  assert.equal(chains.size, 1, 'everything hangs off the one user turn');
  assert.ok(c.every((r) => r.chain === 'turn:1'), 'every brain.* card and the reads/CLI it ran are in the ask chain');
  const lanes = Object.fromEntries(c.map((r) => [r.key, r.lane]));
  for (const r of c.filter((r) => r.type.startsWith('brain.'))) assert.equal(r.lane, 'sup', `${r.type} sits in the Supervisor lane`);
  const [, ask, step1, bcall1, read, bres1, step2, bcall2, cli, bres2, compaction, step3, askResult] = c;
  assert.equal(ask.type, 'voice.tool_call');
  assert.equal(step1.arrowOut.to, 'herdr');
  assert.equal(step1.arrowOut.label, 'agent · gemini-3.5-flash-lite · 700 ms · 1 tool call');
  assert.equal(step1.parentKey, undefined, 'a step between calls is not nested');
  assert.equal(bcall1.title, '⚙ brain read_screen');
  assert.equal(bcall1.summary, 'agent="auth"');
  assert.equal(bcall1.arrowOut, null, 'a brain call stays inside the Supervisor');
  assert.equal(read.parentKey, bcall1.key, 'the herdr.read ran inside read_screen (time window)');
  assert.deepEqual(bcall1.children, [read.key]);
  assert.equal(bres1.callKey, bcall1.key);
  assert.equal(bcall1.resultKey, bres1.key);
  assert.equal(bres1.latencyMs, 4);
  assert.equal(bres1.summary, '4 ms · 80 lines');
  assert.equal(step2.parentKey, undefined);
  assert.equal(cli.parentKey, bcall2.key, 'the cli.run ran inside run_cli');
  assert.equal(bres2.latencyMs, 6003, 'no dur_ms: Δ to the call');
  assert.match(bres2.summary, /^Δ 6\.0 s · the clamp/);
  assert.equal(bres2.latencyLevel, 'slow');
  assert.equal(compaction.summary, '9000 → 3000 tokens');
  assert.equal(lanes[compaction.key], 'sup');
  assert.equal(step3.type, 'brain.step');
  assert.equal(askResult.type, 'voice.tool_result');
  assert.equal(askResult.arrowOut.label, 'ask · 9.1 s · interrupt');
  assert.equal(askResult.arrowOut.level, 'slow');
  assert.equal(c[0].arrowOut.label, 'typed · ⏱ 10.4 s');
  assert.equal(c[0].arrowOut.level, 'slow');
  // A brain call with no voice call open joins the loose chain and pairs by id anyway.
  const { rows: r2 } = buildRows([
    ev('brain.tool_call', { id: 'x1', name: 'list_agents', args: {} }, 1, 'brain'),
    ev('brain.tool_result', { id: 'x1', name: 'list_agents', dur_ms: 1, result: '2 agents' }, 1, 'brain'),
    ev('brain.tool_result', { id: 'nope', name: 'list_agents', dur_ms: 1 }, 1, 'brain'),
  ]);
  const c2 = cards(r2);
  assert.match(c2[0].chain, /^loose:/);
  assert.equal(c2[1].callKey, c2[0].key);
  assert.equal(c2[2].orphan, true);
});

// A real slice of the WP7b end-to-end log (testdata/e2e/README.md,
// scenarios 2, 2/3, 5: lines 116–232): the arrow sequence reads like the
// README narrative.
test('e2e fixture, scenarios 2–5: the expected arrow sequence', () => {
  const file = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'testdata', 'e2e', '2026-09-17-e2e.jsonl');
  const lines = readFileSync(file, 'utf8').trim().split('\n');
  const events = lines.slice(115, 232).map((l) => JSON.parse(l)); // 1-based 116..232
  const t = process.hrtime.bigint();
  const { rows, chains, total, shown } = buildRows(events, {});
  const durMs = Number(process.hrtime.bigint() - t) / 1e6;
  assert.ok(durMs < 100, `buildRows over ${events.length} events took ${durMs} ms`);
  const skipped = events.filter((e) => e.type === 'web.utterance' && e.payload.via === 'voice').length;
  const outFrags = events.filter((e) => e.type === 'voice.transcript.out').length;
  const outCards = cards(rows).filter((r) => r.type === 'voice.transcript.out').length;
  assert.equal(events.length, 117);
  assert.equal(skipped, 4);
  assert.equal(outCards, 7, 'seven spoken turns');
  assert.equal(total, events.length - skipped - (outFrags - outCards), 'every event but web.utterance via=voice and the merged fragments is a row');
  assert.ok(shown < total, 'stats and silent pushes are hidden');
  const seq = cards(rows).filter((r) => r.arrowOut).map((r) => `${r.arrowOut.from}>${r.arrowOut.to} ${r.arrowOut.label}`);
  assert.deepEqual(seq, [
    // scenario 2: proactive block (agy) → summarize → push → spoken; then the snapshot tick clears it
    'herdr>sup done → blocked',
    'sup>herdr recent_unwrapped · 80 lines',
    'sup>herdr summarize_block · gemini-3.5-flash-lite · 1.3 s',
    'sup>voice push · idle',
    'voice>you 🔊 669 ms after push',
    'herdr>sup blocked → done',
    // (a typed confirm with nothing pending: line 138 onwards, before 159)
    'you>voice typed',
    'voice>sup confirm()',
    'sup>voice confirm · Δ 18 ms · interrupt',
    'voice>you 🔊',
    // scenario 2/3: block → push → "Yes." → keys ["1"] → confirmed → finished
    'herdr>sup done → blocked',
    'sup>herdr recent_unwrapped · 80 lines',
    'sup>herdr summarize_block · gemini-3.5-flash-lite · 952 ms',
    'sup>voice push · idle',
    'voice>you 🔊 600 ms after push',
    'you>voice typed',
    'voice>sup confirm()',
    'sup>herdr keys · keys ["1"]',
    'sup>voice confirm · Δ 17 ms · interrupt',
    'herdr>sup blocked → working',
    'voice>you 🔊',
    'herdr>sup working → done',
    'sup>voice push · idle',
    'voice>you 🔊 607 ms after push',
    // scenario 5: propose → resolve → prompt → "Stop." → ctrl+c → cancelled
    'you>voice typed',
    'voice>sup propose(agent="auth-one", instruction="explain …)',
    'sup>herdr resolve_instruction · gemini-3.5-flash-lite · 722 ms',
    'sup>herdr prompt · "explain in detail how the clamp function works and list three edge cases"',
    'sup>voice propose · Δ 1.0 s · interrupt',
    'herdr>sup done → working',
    'voice>you 🔊',
    'you>voice typed',
    'voice>sup cancel()',
    'sup>herdr keys · keys ["ctrl+c"]',
    'sup>voice cancel · Δ 2 ms · interrupt',
    'voice>you 🔊',
    'herdr>sup working → done',
    'sup>voice push · idle',
  ]);
  // Chains: each "Yes."/"Stop." turn owns its confirm/cancel and the send.
  const c = cards(rows);
  const yes = c.filter((r) => r.type === 'voice.text')[1];
  assert.equal(yes.text, 'Yes.');
  const keys = c.find((r) => r.type === 'herdr.send' && r.summary.includes('["1"]'));
  assert.equal(keys.chain, yes.chain);
  const stop = c.filter((r) => r.type === 'voice.text')[3];
  assert.equal(stop.text, 'Stop.');
  const ctrlc = c.find((r) => r.type === 'herdr.send' && r.summary.includes('ctrl+c'));
  assert.equal(ctrlc.chain, stop.chain);
  assert.equal(c.find((r) => r.type === 'pending.cancelled' && r.summary.includes('user')).chain, stop.chain);
  // The block story is one loose chain from the transition to the spoken push.
  const block = c.find((r) => r.type === 'herdr.transition' && r.summary.startsWith('done → blocked'));
  const spoken = c.find((r) => r.pushDelayMs === 669);
  assert.equal(spoken.chain, block.chain);
  assert.ok(chains.get(block.chain).kind === 'loose');
  // Gaps: quiet stretches longer than 5 s are compressed.
  const gaps = rows.filter((r) => r.kind === 'gap').map((g) => g.label);
  assert.ok(gaps.includes('30 s') && gaps.includes('12.2 s'), `gaps: ${gaps.join(', ')}`);
});

test('push judgment: brain.push_decision rides on the voice.push it produced; a skip stays its own card', () => {
  const rows = buildRows([
    ev('herdr.transition', { from: 'working', to: 'done', agent: 'codex' }, 1, 'herdr', 'w5:p1'),
    ev('brain.step', { template: 'announce', model: 'fake-reflex', dur_ms: 5, tool_calls: 0 }, 5, 'brain'),
    ev('brain.push_decision', { via: 'brain', urgency_requested: 'interrupt', urgency_applied: 'idle', reason: 'ceiling', ceiling: 'normal', event: 'done', template: 'auth finished: ran the tests.', spoken: 'Auth finished the rerun you asked for.' }, 1, 'supervisor', 'w5:p1'),
    ev('voice.push', { text: 'Auth finished the rerun you asked for.', urgency: 'idle' }, 1),
    ev('brain.push_decision', { via: 'brain', urgency_requested: 'skip', urgency_applied: 'skip', reason: 'brain', ceiling: 'normal', event: 'done', template: 'docs finished: wrote the readme.' }, 200, 'supervisor', 'w6:p1'),
    ev('brain.push_decision', { via: 'template', urgency_requested: '', urgency_applied: 'idle', reason: 'timeout', ceiling: 'chatty', event: 'done', template: 'docs finished.' }, 200, 'supervisor', 'w6:p1'),
    ev('voice.push', { text: 'docs finished.', urgency: 'idle' }, 1),
    ev('brain.yielded', { turn: 4, step: 2, agent: 'auth', event: 'done', ceo_waiting: 1 }, 1, 'brain'),
    ev('brain.event_coalesced', { agent: 'auth', event: 'done', kind: 'announce' }, 1, 'brain'),
  ]);
  const c = cards(rows.rows);
  const pushes = c.filter((r) => r.type === 'voice.push');
  assert.equal(pushes.length, 2);
  assert.deepEqual(pushes[0].decision, { via: 'brain', requested: 'interrupt', applied: 'idle', reason: 'ceiling', ceiling: 'normal', late: false });
  assert.equal(pushes[0].title, 'push · idle · brain asked interrupt (ceiling)');
  assert.ok(pushes[0].cls.includes('decided'));
  assert.deepEqual(pushes[1].decision.via, 'template');
  assert.equal(pushes[1].title, 'push · idle · template (timeout)');
  const decisions = c.filter((r) => r.type === 'brain.push_decision');
  assert.equal(decisions.length, 3, 'every decision is a card too');
  assert.equal(decisions[0].title, 'push judgment');
  assert.equal(decisions[1].summary, 'skip → skip · brain · ceiling normal · via brain · done · docs finished: wrote the readme.');
  assert.ok(decisions[0].summary.includes('via brain · done ·'));
  assert.equal(c.find((r) => r.type === 'brain.yielded').title, 'yielded to a CEO turn');
  assert.equal(c.find((r) => r.type === 'brain.yielded').summary, 'turn 4 · before step 2 · auth done · 1 CEO turn waiting');
  assert.equal(c.find((r) => r.type === 'brain.event_coalesced').summary, 'auth done · replaced the queued turn');
});
