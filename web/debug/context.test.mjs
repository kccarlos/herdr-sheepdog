// Unit tests for the /debug Context tab logic: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  CLASSES, classOf, diffState, paneModel, rawText, budget, linkIndex, counterparts, interleave, pointsAt, resolvePath, firstLine,
} from './context.js';

const sec = (key, cls, text, extra = {}) => ({ key, class: cls, label: key, text, tokens: Math.ceil(text.length / 4), ...extra });

function brainResp() {
  return {
    step: { step: 3, turn: 2, turn_step: 1, window_tokens: 20 },
    sections: [
      sec('meta', 'meta', '### request · gemini\n\n', { group: 'system' }),
      sec('prompt:role', 'system', '### system instruction\nYou are the SVP.\n\n', { group: 'system', path: 'config.systemInstruction' }),
      sec('prompt:time', 'system', '## Now\n\n10:01\n\n', { group: 'system' }),
      sec('tool:read_screen', 'tools', '### tools\nread_screen(agent)\n\n', { group: 'tools' }),
      sec('seq:1', 'shepherd', '### user\n[10:00] CEO said: "what is auth doing?"\n', { group: 'messages', links: ['call:c1'], path: 'contents[0].parts[0]' }),
      sec('seq:2', 'request', '[10:00] Request from the assistant (utterance)\n\n', { group: 'messages', links: ['call:c1'], current: true, path: 'contents[0].parts[0]' }),
      sec('seq:3', 'tool_result', '### user\n← read_screen: ', { group: 'messages', links: ['call:c1'] }),
      sec('seq:3#1', 'untrusted', '<<<BEGIN_WORKER_OUTPUT agent=auth source=screen>>>\nok\n<<<END_WORKER_OUTPUT>>>\n\n', { group: 'messages', links: ['call:c1'] }),
      sec('seq:4', 'pruned', '[screen of auth, pruned]\n\n', { group: 'messages', pruned: true }),
      sec('seq:5', 'push', '[10:01] Pushed to the assistant: "auth finished"\n\n', { group: 'messages', links: ['push:p1'] }),
    ],
    body: { contents: [{ role: 'user', parts: [{ text: 'hello' }] }], config: { systemInstruction: { parts: [{ text: 'You are the SVP.' }] } } },
    diff: { prev: 2, added: ['seq:3', 'seq:3#1', 'seq:5'], changed: ['prompt:time'], pruned: ['seq:4'], compacted: [], removed: [{ key: 'seq:0', label: 'voice said', class: 'voice', reason: 'compacted', text: 'old' }] },
  };
}

function voiceResp() {
  return {
    turn: { turn: 7, session: 'gemini#1' },
    sections: [
      sec('v:1', 'marker', '── daemon · session open (fresh) ──\nfresh\n\n', { group: 'gemini#1' }),
      sec('v:2', 'system', '── system · system instructions ──\nbe brief\n\n', { group: 'gemini#1' }),
      sec('v:3', 'shepherd', '── user · shepherd ──\n🎤 audio 1.2 s — heard: "what is auth doing?"\n\n', { group: 'gemini#1', links: ['call:c1'] }),
      sec('v:4', 'tool_call', '── model · tool call ──\n→ to_supervisor\n\n', { group: 'gemini#1', links: ['call:c1'] }),
      sec('v:5', 'push', '── user · push ──\nauth finished\n\n', { group: 'gemini#1', links: ['push:p1', 'evt:e1'] }),
    ],
    diff: { prev: 6, added: ['v:5'], changed: [], pruned: [], compacted: [], removed: [] },
  };
}

test('classes: every class the daemon emits has a colour, unknown ones fall back', () => {
  for (const c of ['system', 'tools', 'meta', 'shepherd', 'voice', 'brain', 'request', 'worker', 'untrusted', 'tool_call', 'tool_result', 'push', 'compaction', 'pruned', 'marker', 'audio']) {
    assert.ok(CLASSES.some((x) => x.id === c), c);
    assert.equal(classOf(c), `cx-${c}`);
  }
  assert.equal(classOf('weird'), 'cx-meta');
});

test('paneModel: the brain pane groups sections into system, tools, messages with the diff states', () => {
  const m = paneModel('brain', brainResp());
  assert.deepEqual(m.groups.map((g) => [g.id, g.collapsed, g.sections.length]), [['system', true, 3], ['tools', true, 1], ['messages', false, 6]]);
  const byKey = Object.fromEntries(m.groups.flatMap((g) => g.sections).map((s) => [s.key, s]));
  assert.equal(byKey['seq:3'].diff, 'added');
  assert.equal(byKey['seq:3#1'].css, 'cx-untrusted');
  assert.equal(byKey['prompt:time'].diff, 'changed');
  assert.equal(byKey['seq:4'].diff, 'pruned');
  assert.equal(byKey['seq:4'].pruned, true);
  assert.equal(byKey['seq:1'].diff, '');
  assert.equal(byKey['seq:2'].current, true);
  assert.equal(m.removed.length, 1);
  assert.equal(m.removed[0].reason, 'compacted');
  assert.equal(m.removed[0].css, 'cx-voice');
  assert.equal(m.total, m.groups.reduce((n, g) => n + g.tokens, 0));
  assert.equal(m.prev, 2);
});

test('paneModel: the voice pane groups by session and starts its instructions closed', () => {
  const m = paneModel('voice', voiceResp());
  assert.deepEqual(m.groups.map((g) => g.id), ['gemini#1']);
  const secs = m.groups[0].sections;
  assert.equal(secs.find((s) => s.key === 'v:2').collapsed, true);
  assert.equal(secs.find((s) => s.key === 'v:3').collapsed, false);
  assert.equal(secs.find((s) => s.key === 'v:5').diff, 'added');
  assert.deepEqual(paneModel('voice', null).groups, []);
});

test('diffState: compacted wins over added, and no diff means no state', () => {
  const d = { added: ['a', 'sum'], changed: ['b'], pruned: ['c'], compacted: ['sum'] };
  assert.equal(diffState('sum', d), 'compacted');
  assert.equal(diffState('a', d), 'added');
  assert.equal(diffState('b', d), 'changed');
  assert.equal(diffState('c', d), 'pruned');
  assert.equal(diffState('z', d), '');
  assert.equal(diffState('a', null), '');
});

test('rawText and budget: the copy is the sections joined; the window compares the messages', () => {
  const r = brainResp();
  assert.equal(rawText(r), r.sections.map((s) => s.text).join(''));
  const b = budget(paneModel('brain', r), 20);
  assert.equal(b.window, 20);
  assert.ok(b.messages > 20);
  assert.equal(b.over, true);
  assert.equal(budget(paneModel('brain', r), 0).pct, 0);
});

test('links: hovering a piece finds its counterparts in the other pane', () => {
  const b = brainResp().sections;
  const v = voiceResp().sections;
  const vIdx = linkIndex(v);
  const bIdx = linkIndex(b);
  assert.deepEqual(vIdx.get('call:c1'), ['v:3', 'v:4']);
  assert.deepEqual(counterparts(['call:c1'], vIdx), ['v:3', 'v:4']);
  assert.deepEqual(counterparts(['push:p1', 'evt:e1'], bIdx), ['seq:5']);
  assert.deepEqual(counterparts([], bIdx), []);
  assert.deepEqual(counterparts(['call:none'], bIdx), []);
});

test('interleave and pointsAt: the scrubber orders both panes by time and shows the newest of each', () => {
  const tl = {
    brain: [
      { step: 1, turn: 1, turn_step: 1, kind: 'announce', ts: '2026-09-23T10:00:01.000Z' },
      { step: 2, turn: 2, turn_step: 1, kind: 'utterance', ts: '2026-09-23T10:00:03.000Z' },
    ],
    voice: [
      { turn: 1, label: 'session open', ts: '2026-09-23T10:00:00.000Z' },
      { turn: 2, label: 'shepherd', ts: '2026-09-23T10:00:03.000Z' },
      { turn: 3, label: 'tool response', ts: '2026-09-23T10:00:04.000Z' },
    ],
  };
  const pts = interleave(tl);
  assert.deepEqual(pts.map((p) => `${p.pane}${p.id}`), ['voice1', 'brain1', 'brain2', 'voice2', 'voice3']);
  assert.deepEqual(pointsAt(pts, 0), { brain: 0, voice: 1 });
  assert.deepEqual(pointsAt(pts, 2), { brain: 2, voice: 1 });
  assert.deepEqual(pointsAt(pts, 4), { brain: 2, voice: 3 });
  assert.deepEqual(pointsAt(pts, 99), { brain: 2, voice: 3 });
  assert.deepEqual(interleave(null), []);
});

test('resolvePath: a section path finds its raw JSON in the body', () => {
  const body = brainResp().body;
  assert.deepEqual(resolvePath(body, 'contents[0].parts[0]'), { text: 'hello' });
  assert.equal(resolvePath(body, 'config.systemInstruction.parts[0].text'), 'You are the SVP.');
  assert.equal(resolvePath(body, 'contents[9].parts[0]'), undefined);
  assert.equal(resolvePath(body, ''), undefined);
  assert.equal(resolvePath({ messages: [{ tool_calls: [{ id: 'a' }, { id: 'b' }] }] }, 'messages[0].tool_calls[1]').id, 'b');
});

test('firstLine: the summary of a collapsed section', () => {
  assert.equal(firstLine('\n### system instruction\nYou are…'), 'You are…');
  assert.equal(firstLine('### model\n'), '### model');
  assert.equal(firstLine('── user · push ──\nauth finished\n'), 'auth finished');
  assert.equal(firstLine('one line'), 'one line');
  assert.equal(firstLine(''), '');
});
