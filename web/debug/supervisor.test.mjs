// Unit tests for the /debug Supervisor tab logic: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import {
  buildTranscriptView, meterSegments, TurnLog, windowInfo, offState, parseRequest, parseReply, parsePush, parseWorkerEvent, parsePromise, parseModelSwitch, parseTranscriptJSONL, fmtTokens,
} from './supervisor.js';

let t0 = Date.parse('2026-09-18T05:39:00.000Z');
let seq = 0;
// msg builds one transcript message `ms` after the previous one.
function msg(kind, fields = {}, ms = 100) {
  t0 += ms;
  seq++;
  const role = { reply: 'assistant', brain_tool_call: 'assistant', brain_tool_result: 'tool' }[kind] || 'user';
  return { seq, ts: new Date(t0).toISOString(), role, kind, tokens: 10, turn: 0, ...fields };
}
function ev(type, payload, ms = 100) {
  t0 += ms;
  return { ts: new Date(t0).toISOString(), source: type.split('.')[0], type, payload };
}
const kinds = (items) => items.map((it) => it.kind);
const reply = (spoken, extra = {}) => JSON.stringify({ spoken, detail: 'more', needs_user: false, clarify: false, ...extra });

function sampleRun() {
  seq = 0;
  return [
    msg('worker_event', { content: 'auth: ? -> working', agent: 'auth' }),
    msg('ceo_utterance', { content: 'what is auth doing?' }),
    msg('assistant_said', { content: 'Let me check.' }),
    msg('push', { content: '(interrupt) auth is asking whether it can create hello.txt.', agent: 'auth' }),
    msg('request', { turn: 1, content: 'Request from the assistant (ask): question="what did it say?" agent="auth"\nUse tools as needed, then reply with the JSON object of the answer contract.' }),
    msg('brain_tool_call', { turn: 1, tool_call: { id: 'bt-1-1', name: 'read_screen', args: { agent: 'auth', lines: 100 } } }),
    msg('brain_tool_result', { turn: 1, tokens: 300, content: '<<<BEGIN_WORKER_OUTPUT agent=auth source=screen>>>\nWould you like to run it?\n<<<END_WORKER_OUTPUT>>>', tool_result: { id: 'bt-1-1', name: 'read_screen', dur_ms: 12, summary: 'screen of auth, 2 lines' }, agent: 'auth' }),
    msg('reply', { turn: 1, content: reply('Auth is asking to create hello.txt.', { needs_user: true, options: ['yes', 'no'] }) }),
    msg('request', { turn: 2, content: 'Request from the assistant (propose): instruction="tell auth to go ahead" agent=(none: the CEO did not say a name)' }),
    msg('brain_tool_call', { turn: 2, tool_call: { id: 'bt-2-2', name: 'send_keys', args: { agent: 'auth', keys: ['enter'] } } }),
    msg('brain_tool_result', { turn: 2, content: '', tool_result: { id: 'bt-2-2', name: 'send_keys', dur_ms: 1, error: 'not permitted: auth has no recorded approval prompt to answer' }, agent: 'auth' }),
    msg('reply', { turn: 2, content: 'I cannot answer that' }),
    msg('request', { turn: 2, content: 'That was not the JSON object of the answer contract. Reply with the JSON object only.' }),
    msg('reply', { turn: 2, content: reply('I could not do that.') }),
    msg('compaction_summary', { content: 'The CEO asked about auth; auth was asking to create hello.txt.', tokens: 20 }),
    msg('note', { content: 'do not touch the migrations' }),
  ];
}

test('kinds map onto items and tool calls pair with their results', () => {
  const items = buildTranscriptView(sampleRun(), null, new TurnLog());
  assert.deepEqual(kinds(items), ['worker', 'ceo', 'assistant', 'push', 'turn', 'request', 'tool', 'reply', 'turn', 'request', 'tool', 'reply', 'request', 'reply', 'compaction', 'note']);
  const [worker, ceo, , push, turn1, request, tool, rep] = items;
  assert.deepEqual({ agent: worker.agent, from: worker.from, to: worker.to }, { agent: 'auth', from: '?', to: 'working' });
  assert.equal(ceo.text, 'what is auth doing?');
  assert.equal(ceo.inWindow, undefined, 'no context: window unknown');
  assert.deepEqual({ urgency: push.urgency, text: push.text }, { urgency: 'interrupt', text: 'auth is asking whether it can create hello.txt.' });
  assert.equal(turn1.turn, 1);
  assert.equal(turn1.reqKind, 'ask', 'kind from the request when no brain.turn event');
  assert.equal(turn1.complete, false);
  assert.deepEqual({ reqKind: request.reqKind, question: request.question, agent: request.agent, nudge: request.nudge }, { reqKind: 'ask', question: 'what did it say?', agent: 'auth', nudge: false });
  assert.equal(tool.name, 'read_screen');
  assert.equal(tool.summary, 'agent="auth", lines=100');
  assert.equal(tool.pending, false);
  assert.match(tool.result.content, /BEGIN_WORKER_OUTPUT agent=auth source=screen/);
  assert.equal(tool.result.durMs, 12);
  assert.equal(tool.tokens, 310, 'call + result tokens');
  assert.equal(tool.raw.seq, 6, 'raw JSON is the API message');
  assert.deepEqual({ spoken: rep.spoken, detail: rep.detail, needsUser: rep.needsUser, options: rep.options, json: rep.json }, { spoken: 'Auth is asking to create hello.txt.', detail: 'more', needsUser: true, options: ['yes', 'no'], json: true });
  // Turn 2: the rail-blocked send_keys, a bad reply, the nudge and the corrected reply.
  const blocked = items[10];
  assert.equal(blocked.name, 'send_keys');
  assert.match(blocked.result.error, /not permitted/);
  assert.equal(items[11].json, false, 'a non-JSON reply is shown raw');
  assert.equal(items[12].nudge, true);
  assert.equal(items[9].agent, '', 'agent=(none…) reads as no agent');
  assert.equal(items[9].kind, 'request');
  assert.equal(items[9].trigger, 'ceo');
  assert.equal(items[14].summary, 'The CEO asked about auth; auth was asking to create hello.txt.');
  assert.equal(items[14].info, null, 'no compaction event known');
  assert.equal(items[15].text, 'do not touch the migrations');
});

test('window marker sits before the first in-window message, the rest is dimmed and pruned stubs are marked', () => {
  const messages = sampleRun();
  // The window after a compaction: the summary (seq 15) first, then the
  // kept tail from seq 9 on; the tool result of turn 1 was pruned.
  const context = {
    window_tokens: 1000, estimated_tokens: 200, transcript_len: 16,
    window: [
      { seq: 15, kind: 'compaction_summary', turn: 2, tokens: 20 },
      ...[9, 10, 11, 12, 13, 14, 16].map((s) => ({ seq: s, kind: messages[s - 1].kind, turn: messages[s - 1].turn, tokens: 10 })),
    ],
    last_compaction: { before_tokens: 900, after_tokens: 200, dropped: 8, summary_seq: 15, at: '2026-09-18T05:40:00Z' },
    system_prompt: { sections: [], total_chars: 0, total_tokens: 0 },
  };
  const items = buildTranscriptView(messages, context, new TurnLog());
  const marker = items.find((it) => it.kind === 'marker');
  assert.ok(marker);
  const at = items.indexOf(marker);
  assert.equal(items[at + 1].kind, 'turn', 'the marker precedes turn 2');
  assert.equal(items[at + 2].seq, 9);
  assert.equal(marker.onDisk, 8, 'eight messages before it are on disk only');
  assert.equal(marker.inWindow, 8);
  for (const it of items.slice(0, at)) if (it.seq && it.kind !== 'turn') assert.equal(it.inWindow, false, `seq ${it.seq} dimmed`);
  for (const it of items.slice(at + 1)) if (it.seq && it.kind !== 'turn') assert.equal(it.inWindow, true, `seq ${it.seq} in window`);
  const comp = items.find((it) => it.kind === 'compaction');
  assert.deepEqual(comp.info, { before: 900, after: 200, dropped: 8, at: '2026-09-18T05:40:00Z' }, 'numbers from last_compaction by summary_seq');
  assert.equal(comp.inWindow, true);

  // A pruned stub: the read_screen result of turn 1 shows its stub but keeps the full result.
  context.window.push({ seq: 7, kind: 'brain_tool_result', turn: 1, tokens: 6, pruned: true, stub: '[screen of auth, 2 lines, pruned]' });
  const items2 = buildTranscriptView(messages, context, new TurnLog());
  const tool = items2.find((it) => it.kind === 'tool' && it.name === 'read_screen');
  assert.equal(tool.result.pruned, true);
  assert.equal(tool.result.stub, '[screen of auth, 2 lines, pruned]');
  assert.match(tool.result.content, /Would you like to run it/);
  assert.equal(tool.inWindow, true, 'a pruned result is still in the window');
  const m2 = items2.find((it) => it.kind === 'marker');
  assert.equal(items2[items2.indexOf(m2) + 1], tool, 'the marker moved up before the tool item whose result is still in');
  assert.equal(items2[items2.indexOf(m2) - 1].kind, 'request', 'the request of turn 1 stays above it, on disk only');
});

test('windowOnly, hideWorker and hideAssistant filter the items', () => {
  const messages = sampleRun();
  const context = { window_tokens: 100, estimated_tokens: 40, window: [15, 16].map((s) => ({ seq: s, kind: messages[s - 1].kind, tokens: 20 })), system_prompt: { sections: [] } };
  assert.deepEqual(kinds(buildTranscriptView(messages, context, new TurnLog(), { windowOnly: true })), ['marker', 'compaction', 'note']);
  const some = buildTranscriptView(messages, null, new TurnLog(), { hideWorker: true, hideAssistant: true });
  assert.ok(!some.some((it) => it.kind === 'worker' || it.kind === 'assistant'));
  assert.equal(some.length, 14);
});

test('turn headers correlate with brain.turn / brain.fallback and reset on daemon.start', () => {
  const log = new TurnLog();
  log.add(ev('daemon.start', { version: 'old' }));
  log.add(ev('brain.turn', { turn: 1, trigger: 'ceo', kind: 'ask', steps: 9, model_calls: 9, dur_ms: 99999, fallback: true }));
  log.add(ev('daemon.start', { version: 'new' }));
  assert.equal(log.turn(1), null, 'the old run\'s turn 1 is gone');
  log.add(ev('brain.step', { turn: 1, step: 1, provider: 'deepseek', model: 'x' }));
  log.add(ev('brain.step', { turn: 1, step: 2, provider: 'deepseek', model: 'x' }));
  assert.equal(log.turn(1).modelCalls, 2, 'steps count until the turn completes');
  assert.equal(`${log.turn(1).provider}/${log.turn(1).model}`, 'deepseek/x', 'the first step names the turn\'s provider and model');
  log.add(ev('brain.turn', { turn: 1, trigger: 'ceo', kind: 'ask', provider: 'deepseek', model: 'x', steps: 2, model_calls: 2, dur_ms: 5100, fallback: false, tokens_in: 4000, tokens_out: 60, window_tokens: 1200, acted: 0 }));
  log.add(ev('brain.step', { turn: 1, step: 3 }));
  assert.equal(log.turn(1).modelCalls, 2, 'a complete turn keeps the logged count');
  log.add(ev('brain.fallback', { turn: 2, reason: 'timeout', kind: 'propose', trigger: 'ceo', acted: false, error: 'deadline' }));
  log.add(ev('brain.turn', { turn: 2, trigger: 'ceo', kind: 'propose', steps: 3, model_calls: 3, dur_ms: 15000, fallback: true }));
  log.add(ev('brain.compaction', { turn: 2, before_tokens: 900, after_tokens: 200, dropped: 8, dur_ms: 300 }));
  const items = buildTranscriptView(sampleRun(), null, log);
  const turns = items.filter((it) => it.kind === 'turn');
  assert.equal(turns.length, 2);
  assert.deepEqual({ modelCalls: turns[0].modelCalls, durMs: turns[0].durMs, fallback: turns[0].fallback, reqKind: turns[0].reqKind, trigger: turns[0].trigger, tokensIn: turns[0].tokensIn, windowTokens: turns[0].windowTokens, provider: turns[0].provider, model: turns[0].model }, { modelCalls: 2, durMs: 5100, fallback: false, reqKind: 'ask', trigger: 'ceo', tokensIn: 4000, windowTokens: 1200, provider: 'deepseek', model: 'x' });
  assert.deepEqual({ fallback: turns[1].fallback, reason: turns[1].reason, reqKind: turns[1].reqKind }, { fallback: true, reason: 'timeout', reqKind: 'propose' });
  const comp = items.find((it) => it.kind === 'compaction');
  assert.deepEqual({ before: comp.info.before, after: comp.info.after, dropped: comp.info.dropped }, { before: 900, after: 200, dropped: 8 }, 'the i-th compaction event when the context does not say');
});

test('meter segments: prompt sections by source, window by kind, pruned stubs; transcript segments sum to the estimate', () => {
  const context = {
    window_tokens: 24000, estimated_tokens: 1234, transcript_len: 40,
    window: [
      { seq: 1, kind: 'ceo_utterance', tokens: 100 }, { seq: 2, kind: 'ceo_utterance', tokens: 100 },
      { seq: 3, kind: 'brain_tool_call', tokens: 34 }, { seq: 4, kind: 'brain_tool_result', tokens: 6, pruned: true, stub: '[x, pruned]' },
      { seq: 5, kind: 'brain_tool_result', tokens: 900 }, { seq: 6, kind: 'reply', tokens: 50 }, { seq: 7, kind: 'compaction_summary', tokens: 44 },
    ],
    system_prompt: { sections: [{ name: 'role', source: 'embedded', chars: 400, tokens: 100 }, { name: 'rail', source: 'override', chars: 40, tokens: 10 }, { name: 'roster', source: 'dynamic', chars: 80, tokens: 20 }], total_chars: 524, total_tokens: 131 },
  };
  const m = meterSegments(context);
  assert.equal(m.known, true);
  assert.equal(m.budget, 24000);
  assert.equal(m.windowTokens, 1234);
  assert.equal(m.promptTokens, 131);
  assert.equal(m.transcript.reduce((n, s) => n + s.tokens, 0), 1234, 'segments sum to estimated_tokens');
  assert.deepEqual(m.transcript.map((s) => [s.id, s.tokens, s.count]), [['kind:compaction', 44, 1], ['kind:ceo', 200, 2], ['kind:tool', 34, 1], ['kind:result', 900, 1], ['kind:reply', 50, 1], ['pruned', 6, 1]]);
  assert.deepEqual(m.prompt.map((s) => [s.label, s.source, s.tokens]), [['role', 'embedded', 100], ['rail', 'override', 10], ['roster', 'dynamic', 20]]);
  assert.equal(m.onDisk, 33);
  assert.equal(m.inWindow, 7);
  assert.equal(m.over, false);
  assert.equal(m.scale, 24000);
  assert.equal(meterSegments({ window_tokens: 100, estimated_tokens: 150, window: [{ seq: 1, kind: 'note', tokens: 150 }] }).over, true);
  assert.equal(meterSegments(null).known, false);
});

test('brain-off state and the empty transcript', () => {
  assert.deepEqual(offState(503, { error: 'the supervisor brain is off (--brain=off)' }), { off: true, reason: '--brain=off', message: 'the supervisor brain is off (--brain=off)' });
  assert.equal(offState(503, { error: 'the supervisor brain is off (no Gemini key)' }).reason, 'no Gemini key');
  assert.equal(offState(200, {}), null);
  assert.deepEqual(buildTranscriptView([], null, new TurnLog()), []);
  assert.deepEqual(buildTranscriptView([], { window: [], estimated_tokens: 0, window_tokens: 10 }, new TurnLog()), []);
  const w = windowInfo({ window: [{ seq: 4, kind: 'note', tokens: 1 }, { seq: 2, kind: 'note', tokens: 1, pruned: true, stub: 's' }] });
  assert.equal(w.first, 2);
  assert.equal(w.pruned.get(2), 's');
});

test('parsers and formatting', () => {
  assert.deepEqual(parseRequest('Request from the assistant (propose): instruction="say \\"hi\\"" agent="auth" [triggered by an event, not by the CEO]\nUse tools'), { reqKind: 'propose', question: 'say "hi"', agent: 'auth', trigger: 'event', nudge: false });
  assert.equal(parseRequest('Time is up: answer now').nudge, true);
  assert.deepEqual(parseReply('```json\n{"spoken":"ok","detail":"","needs_user":false,"clarify":true}\n```').clarify, true);
  assert.equal(parseReply('plain').json, false);
  assert.deepEqual(parsePush('(idle) auth finished.'), { urgency: 'idle', text: 'auth finished.' });
  assert.deepEqual(parsePush('no prefix'), { urgency: '', text: 'no prefix' });
  assert.deepEqual(parseWorkerEvent('auth: pane closed'), { agent: 'auth', from: '', to: '', closed: true });
  assert.deepEqual(parseModelSwitch('model switched from gemini-3.5-flash to gemini-3.8-flash'), { from: 'gemini-3.5-flash', to: 'gemini-3.8-flash' });
  assert.deepEqual(parseModelSwitch('model switched from gemini/gemini-3.8-flash to deepseek/deepseek-flash'), { from: 'gemini/gemini-3.8-flash', to: 'deepseek/deepseek-flash' });
  assert.deepEqual(parseModelSwitch('something else'), { from: '', to: '' });
  assert.equal(fmtTokens(12400), '12.4k');
  assert.equal(fmtTokens(980), '980');
  const parsed = parseTranscriptJSONL('{"kind":"note","content":"a"}\n\nnot json\n{"kind":"note","content":"b"}\n');
  assert.deepEqual(parsed.map((m) => m.seq), [1, 2]);
});

test('the sample run fixture renders every kind, a pruned stub, a rail block and a compaction card', () => {
  const dir = dirname(fileURLToPath(import.meta.url));
  const text = readFileSync(join(dir, '..', '..', 'testdata', 'brain', 'sample-run.jsonl'), 'utf8');
  const messages = parseTranscriptJSONL(text);
  assert.ok(messages.length >= 50, `fixture has ${messages.length} messages`);
  const items = buildTranscriptView(messages, null, new TurnLog());
  const seen = new Set(kinds(items));
  for (const k of ['ceo', 'assistant', 'push', 'worker', 'request', 'tool', 'reply', 'compaction', 'note', 'turn']) assert.ok(seen.has(k), `fixture has a ${k} item`);
  assert.ok(!seen.has('other'), 'no unknown kinds');
  const tools = items.filter((it) => it.kind === 'tool');
  assert.ok(tools.every((it) => !it.pending && !it.orphan), 'every tool call has its result');
  assert.ok(tools.some((it) => it.name === 'read_screen' && /BEGIN_WORKER_OUTPUT/.test(it.result.content)), 'provenance delimiters');
  assert.ok(tools.some((it) => it.name === 'run_cli'), 'a run_cli turn');
  assert.ok(tools.some((it) => it.name === 'send_keys' && /not permitted/.test(it.result.error)), 'a rail-blocked send_keys');
  const comp = items.find((it) => it.kind === 'compaction');
  assert.match(comp.summary, /\S/);
});

test('promise cards: made in a turn, kept later; the promise.* events give the current status', () => {
  seq = 0;
  const promise = { id: 'pr-1', text: "let me know when it's done", agent: 'auth', event: 'done', status: 'open', created_turn: 1, created_at: '2026-09-18T05:40:00Z' };
  const msgs = [
    msg('ceo_utterance', { content: "run the tests and let me know when it's done" }),
    msg('request', { turn: 1, content: 'Request from the assistant (propose): instruction="run the tests and let me know when it\'s done" agent=(none: the CEO did not say a name)' }),
    msg('brain_tool_call', { turn: 1, tool_call: { id: 'bt-1-1', name: 'prompt', args: { agent: 'auth', text: 'run the tests' } } }),
    msg('brain_tool_result', { turn: 1, content: 'Telling auth to run the tests. Sent.', tool_result: { id: 'bt-1-1', name: 'prompt', dur_ms: 3 }, agent: 'auth' }),
    msg('brain_tool_call', { turn: 1, tool_call: { id: 'bt-1-2', name: 'promise', args: { agent: 'auth', text: "let me know when it's done", event: 'done' } } }),
    msg('brain_tool_result', { turn: 1, content: 'I will tell you when auth finishes.', tool_result: { id: 'bt-1-2', name: 'promise', dur_ms: 1 }, agent: 'auth' }),
    msg('promise', { turn: 1, content: 'Promise (open): "let me know when it\'s done", when auth finishes', agent: 'auth', promise }),
    msg('reply', { turn: 1, content: reply('Telling auth to run the tests. Sent; I will tell you when auth finishes.') }),
    msg('worker_event', { content: 'auth: working -> done', agent: 'auth' }),
    msg('push', { content: '(idle) auth finished: ran the tests.', agent: 'auth' }),
    msg('request', { turn: 2, content: 'Event from the supervisor (summarize): agent="auth" event="done" template="auth finished: ran the tests."\nThe assistant already said…' }),
    msg('event_screen', { turn: 2, tokens: 200, content: '<<<BEGIN_WORKER_OUTPUT agent=auth source=screen>>>\nok\n<<<END_WORKER_OUTPUT>>>', tool_result: { name: 'read_screen', summary: 'screen of auth at done, 2 lines' }, agent: 'auth' }),
    msg('reply', { turn: 2, content: '{"spoken": "", "detail": "Nothing to add."}' }),
    msg('request', { turn: 3, content: 'Event from the supervisor (keep_promise): agent="auth" event="done" template="auth finished: ran the tests." promise="let me know when it\'s done" condition="when auth finishes"\nThe CEO asked…' }),
    msg('reply', { turn: 3, content: reply('You asked me to tell you when auth was done: it ran the tests.') }),
    msg('promise', { content: 'Promise (kept): "let me know when it\'s done", when auth finishes', agent: 'auth', promise: { ...promise, status: 'kept' } }),
    msg('push', { content: '(idle) You asked me to tell you when auth was done: it ran the tests.', agent: 'auth' }),
  ];
  const log = new TurnLog();
  log.add(ev('brain.turn', { turn: 1, trigger: 'ceo', kind: 'propose', steps: 2, model_calls: 2, dur_ms: 40, acted: 2 }));
  log.add(ev('promise.created', { id: 'pr-1', text: "let me know when it's done", agent: 'auth', event: 'done', turn: 1 }));
  log.add(ev('brain.turn', { turn: 2, trigger: 'event', kind: 'summarize', steps: 1, model_calls: 1, dur_ms: 30 }));
  log.add(ev('brain.turn', { turn: 3, trigger: 'event', kind: 'keep_promise', steps: 1, model_calls: 1, dur_ms: 25 }));
  assert.ok(log.add(ev('promise.kept', { id: 'pr-1', spoken: 'You asked me to tell you when auth was done: it ran the tests.', via: 'brain', urgency: 'idle', chattiness: 'quiet' })));
  assert.deepEqual(log.promise('pr-1').status, 'kept');
  assert.equal(log.add(ev('promise.created', { id: 'pr-1' })), true, 'a late created event is ours');
  assert.equal(log.promise('pr-1').status, 'kept', 'created never overrides a resolution');
  assert.equal(log.promise('nope'), null);

  const items = buildTranscriptView(msgs, null, log);
  assert.deepEqual(kinds(items), ['ceo', 'turn', 'request', 'tool', 'tool', 'promise', 'reply', 'worker', 'push', 'turn', 'request', 'screen', 'reply', 'turn', 'request', 'reply', 'promise', 'push']);
  const [made, kept] = items.filter((it) => it.kind === 'promise');
  assert.deepEqual({ id: made.id, text: made.text, agent: made.agent, event: made.event, status: made.status, now: made.now, condition: made.condition, createdTurn: made.createdTurn }, { id: 'pr-1', text: "let me know when it's done", agent: 'auth', event: 'done', status: 'open', now: 'kept', condition: 'when auth finishes', createdTurn: 1 });
  assert.equal(made.latest.via, 'brain');
  assert.deepEqual({ status: kept.status, now: kept.now, turn: kept.turn }, { status: 'kept', now: 'kept', turn: 0 });
  const [t1, t2, t3] = items.filter((it) => it.kind === 'turn');
  assert.deepEqual([t1.reqKind, t2.reqKind, t3.reqKind], ['propose', 'summarize', 'keep_promise']);
  assert.deepEqual([t1.trigger, t2.trigger, t3.trigger], ['ceo', 'event', 'event']);
  assert.equal(t1.acted, 2);
  const [, sum, keep] = items.filter((it) => it.kind === 'request');
  assert.deepEqual({ reqKind: sum.reqKind, agent: sum.agent, event: sum.event, question: sum.question, trigger: sum.trigger, nudge: sum.nudge }, { reqKind: 'summarize', agent: 'auth', event: 'done', question: 'auth finished: ran the tests.', trigger: 'event', nudge: false });
  assert.equal(keep.promise, "let me know when it's done");
  const screen = items.find((it) => it.kind === 'screen');
  assert.deepEqual({ summary: screen.summary, pruned: screen.pruned, turn: screen.turn }, { summary: 'screen of auth at done, 2 lines', pruned: false, turn: 2 });
  // The event screen is pruned like a tool result in the window.
  const ctx = { window: msgs.map((m) => ({ seq: m.seq, kind: m.kind, turn: m.turn, tokens: m.tokens, pruned: m.kind === 'event_screen', stub: m.kind === 'event_screen' ? '[screen of auth at done, 2 lines, pruned]' : '' })), estimated_tokens: 100, window_tokens: 1000 };
  const withCtx = buildTranscriptView(msgs, ctx, log).find((it) => it.kind === 'screen');
  assert.deepEqual({ pruned: withCtx.pruned, stub: withCtx.stub }, { pruned: true, stub: '[screen of auth at done, 2 lines, pruned]' });
  const meter = meterSegments(ctx);
  assert.ok(meter.transcript.some((s) => s.kind === 'promise' && s.count === 2), 'promises are a meter group');
  assert.ok(meter.transcript.some((s) => s.group === 'pruned' && s.count === 1), 'the pruned screen is a stub');
  // A promise message without the record parses from its content line.
  assert.deepEqual(parsePromise({ content: 'Promise (expired): "tell me if docs asks", when docs asks something', agent: 'docs' }), { id: '', text: 'tell me if docs asks', agent: 'docs', event: '', status: 'expired', condition: 'when docs asks something', createdTurn: 0, due: null });
});

test('announce turns: the request parses promises and template_spoken; the push card carries its decision; skips, yields and coalesced turns are lines', () => {
  seq = 0;
  const req = parseRequest('Event from the supervisor (announce): agent="auth" event="done" template="auth finished: ran the tests."\nPromises this event keeps (the CEO asked to be told; your line says so, with the result):\n- pr-1: "let me know when it\'s done" (when auth finishes)\n- pr-2: "tell me if anything happens" (when auth changes state)\nNothing has been said about this yet. Decide…');
  assert.deepEqual({ reqKind: req.reqKind, agent: req.agent, event: req.event, question: req.question, promise: req.promise, promises: req.promises, templateSpoken: req.templateSpoken, trigger: req.trigger }, {
    reqKind: 'announce', agent: 'auth', event: 'done', question: 'auth finished: ran the tests.', promise: "let me know when it's done",
    promises: [{ id: 'pr-1', text: "let me know when it's done", condition: 'when auth finishes' }, { id: 'pr-2', text: 'tell me if anything happens', condition: 'when auth changes state' }], templateSpoken: false, trigger: 'event',
  });
  const block = parseRequest('Event from the supervisor (announce): agent="auth" event="blocked" template="auth is asking whether it can create hello.txt."\nThe assistant already said the sentence in template (a question is never delayed). Decide…');
  assert.deepEqual({ promises: block.promises, templateSpoken: block.templateSpoken }, { promises: [], templateSpoken: true });
  // Earlier runs' transcripts still parse.
  assert.equal(parseRequest('Event from the supervisor (keep_promise): agent="auth" event="done" template="x" promise="tell me" condition="when auth finishes"').promise, 'tell me');
  // A delivered investigation is an event turn like an announce.
  const inv = parseRequest('Event from the supervisor (investigation): agent="auth" event="investigation.done" template="The look into auth is done; ask me what it found."\nThe look you started (inv-1, through codex) came back. You asked it: "why did the tests fail?"\nWhat it found:\n<<<BEGIN_WORKER_OUTPUT agent=auth source=investigation>>>\nThe clamp test asserts the wrong bound.\n<<<END_WORKER_OUTPUT>>>\nAnswer the CEO now…');
  assert.deepEqual({ reqKind: inv.reqKind, agent: inv.agent, event: inv.event, trigger: inv.trigger, promises: inv.promises }, {
    reqKind: 'investigation', agent: 'auth', event: 'investigation.done', trigger: 'event', promises: [],
  });

  const msgs = [
    msg('worker_event', { content: 'auth: working -> blocked', agent: 'auth' }),
    msg('push', { content: '(idle) auth is asking whether it can create hello.txt.', agent: 'auth' }),
    msg('request', { turn: 1, content: 'Event from the supervisor (announce): agent="auth" event="blocked" template="auth is asking whether it can create hello.txt."\nThe assistant already said the sentence in template…' }),
    msg('reply', { turn: 1, content: '{"spoken": "", "urgency": "skip", "detail": "Nothing to add."}' }),
    msg('worker_event', { content: 'auth: blocked -> working', agent: 'auth' }),
    msg('worker_event', { content: 'auth: working -> done', agent: 'auth' }),
    msg('request', { turn: 2, content: 'Event from the supervisor (announce): agent="auth" event="done" template="auth finished: ran the tests."\n- pr-1: "let me know when it\'s done" (when auth finishes)\nNothing has been said…' }),
    msg('reply', { turn: 2, content: '{"spoken": "You asked to be told: auth ran the tests and they pass.", "urgency": "interrupt", "keeps_promise_ids": ["pr-1"]}' }),
    msg('push', { content: '(idle) You asked to be told: auth ran the tests and they pass.', agent: 'auth' }),
    msg('worker_event', { content: 'docs: working -> done', agent: 'docs' }),
    msg('push', { content: '(idle) docs finished: wrote the readme.', agent: 'docs' }),
    msg('push', { content: '(idle) That was the readme you asked for.', agent: 'docs' }),
  ];
  const log = new TurnLog();
  const at = (i) => msgs[i].ts;
  const between = (i, j) => new Date((Date.parse(msgs[i].ts) + Date.parse(msgs[j].ts)) / 2).toISOString();
  const dev = (ts, payload, pane) => ({ ts, source: 'supervisor', type: 'brain.push_decision', pane_id: pane, payload });
  assert.ok(log.add(dev(between(3, 4), { via: 'brain', urgency_requested: 'skip', urgency_applied: 'skip', reason: 'brain', ceiling: 'normal', event: 'blocked', template: 'auth is asking whether it can create hello.txt.', template_spoken: true, spoken: '' }, 'w5:p1')));
  log.add(dev(between(7, 8), { via: 'brain', urgency_requested: 'interrupt', urgency_applied: 'idle', reason: 'ceiling', ceiling: 'normal', event: 'done', template: 'auth finished: ran the tests.', spoken: 'You asked to be told: auth ran the tests and they pass.', kept: ['pr-1'], keeps_promise_ids: ['pr-1'] }, 'w5:p1'));
  log.add(dev(between(9, 10), { via: 'template', urgency_requested: '', urgency_applied: 'idle', reason: 'wait', ceiling: 'chatty', event: 'done', template: 'docs finished: wrote the readme.' }, 'w6:p1'));
  log.add(dev(between(10, 11), { via: 'brain', urgency_requested: 'idle', urgency_applied: 'idle', reason: 'brain', ceiling: 'chatty', event: 'done', late: true, template: 'docs finished: wrote the readme.', spoken: 'That was the readme you asked for.', relates_to: 'the readme' }, 'w6:p1'));
  assert.ok(log.add({ ts: between(4, 5), source: 'brain', type: 'brain.yielded', payload: { turn: 9, step: 2, agent: 'auth', event: 'done', ceo_waiting: 1 } }));
  assert.ok(log.add({ ts: between(5, 6), source: 'brain', type: 'brain.event_coalesced', payload: { agent: 'auth', event: 'done', kind: 'announce' } }));
  log.add(ev('brain.turn', { turn: 1, trigger: 'event', kind: 'announce', steps: 1, model_calls: 1, dur_ms: 30 }));
  log.add(ev('brain.turn', { turn: 2, trigger: 'event', kind: 'announce', steps: 2, model_calls: 2, dur_ms: 60 }));

  const items = buildTranscriptView(msgs, null, log);
  assert.deepEqual(kinds(items), ['worker', 'push', 'turn', 'request', 'reply', 'brainEvent', 'worker', 'brainEvent', 'worker', 'brainEvent', 'turn', 'request', 'reply', 'push', 'worker', 'push', 'push']);
  const lines = items.filter((it) => it.kind === 'brainEvent');
  assert.deepEqual(lines.map((l) => l.type), ['decision', 'yielded', 'coalesced']);
  assert.deepEqual({ applied: lines[0].applied, reason: lines[0].reason, template: lines[0].template }, { applied: 'skip', reason: 'brain', template: 'auth is asking whether it can create hello.txt.' });
  assert.deepEqual({ turn: lines[1].turn, step: lines[1].step, ceoWaiting: lines[1].ceoWaiting }, { turn: 9, step: 2, ceoWaiting: 1 });
  const pushes = items.filter((it) => it.kind === 'push');
  assert.equal(pushes[0].decision, null, 'the question itself is pushed before any decision');
  assert.deepEqual({ via: pushes[1].decision.via, requested: pushes[1].decision.requested, applied: pushes[1].decision.applied, reason: pushes[1].decision.reason, kept: pushes[1].decision.kept }, { via: 'brain', requested: 'interrupt', applied: 'idle', reason: 'ceiling', kept: ['pr-1'] });
  assert.deepEqual({ via: pushes[2].decision.via, reason: pushes[2].decision.reason }, { via: 'template', reason: 'wait' });
  assert.deepEqual({ late: pushes[3].decision.late, relatesTo: pushes[3].decision.relatesTo }, { late: true, relatesTo: 'the readme' });
  const turns = items.filter((it) => it.kind === 'turn');
  assert.deepEqual(turns.map((t) => [t.reqKind, t.agent, t.event, t.modelCalls]), [['announce', 'auth', 'blocked', 1], ['announce', 'auth', 'done', 2]]);
  const requests = items.filter((it) => it.kind === 'request');
  assert.equal(requests[0].templateSpoken, true);
  assert.deepEqual(requests[1].promises, [{ id: 'pr-1', text: "let me know when it's done", condition: 'when auth finishes' }]);
  // daemon.start resets the decisions and notes with the turns.
  log.add(ev('daemon.start', {}));
  assert.deepEqual([log.decisions.length, log.notes.length], [0, 0]);
  assert.equal(buildTranscriptView(msgs, null, log).filter((it) => it.kind === 'brainEvent').length, 0);
});

test('a model switch is a system item between turns, on turn 0, in the window and in the notes meter group', () => {
  seq = 0;
  const messages = [
    msg('request', { turn: 1, content: 'Request from the assistant (ask): question="what is auth doing?" agent="auth"' }),
    msg('reply', { turn: 1, content: reply('Auth is working.') }),
    msg('system', { role: 'system', content: 'model switched from gemini-3.5-flash to gemini-3.8-flash' }),
    msg('request', { turn: 2, content: 'Request from the assistant (ask): question="and now?" agent="auth"' }),
    msg('reply', { turn: 2, content: reply('Still working.') }),
  ];
  const context = { window: messages.map((m) => ({ seq: m.seq, kind: m.kind, turn: m.turn, tokens: m.tokens })), estimated_tokens: 50, window_tokens: 24000, transcript_len: 5 };
  const items = buildTranscriptView(messages, context, new TurnLog());
  assert.deepEqual(kinds(items), ['marker', 'turn', 'request', 'reply', 'system', 'turn', 'request', 'reply']);
  const sys = items.find((it) => it.kind === 'system');
  assert.equal(sys.from, 'gemini-3.5-flash');
  assert.equal(sys.to, 'gemini-3.8-flash');
  assert.equal(sys.turn, 0);
  assert.equal(sys.inWindow, true);
  assert.equal(sys.seq, 3);
  const meter = meterSegments(context);
  assert.deepEqual(meter.transcript.find((seg) => seg.id === 'kind:note'), { id: 'kind:note', label: 'notes', group: 'transcript', kind: 'note', tokens: 10, count: 1 }, 'system messages count under notes');
  assert.ok(!items.some((it) => it.kind === 'other'));
});
