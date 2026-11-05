import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  LINGER_MS,
  GONE,
  openQuestions,
  pageTitle,
  approvalDigitOption,
  createApprovals,
  renderApprovalCardsHTML,
  newIdempotencyKey,
} from './approvals.js';

const sampleQuestion = (id = 'ap-1', agent = 'auth') => ({
  id,
  kind: 'answer_prompt',
  pane_id: 'w1:p1',
  agent,
  summary: 'auth wants to run the migration',
  question: 'Run migration?',
  command: 'make migrate && echo <done>',
  options: [
    { index: 1, label: 'Yes, proceed (Recommended)', title: 'Yes, proceed', recommended: true, keys: ['y'], default: true },
    { index: 2, label: 'No, skip', keys: ['n'], default: false, description: 'Leave database untouched' },
  ],
});

test('openQuestions reads questions first and falls back to pending_action', () => {
  const q1 = sampleQuestion('ap-1', 'auth');
  const q2 = sampleQuestion('ap-2', 'docs');
  assert.deepEqual(openQuestions({ questions: [q1, q2], pending_action: q1 }).map((q) => q.id), ['ap-1', 'ap-2']);
  assert.deepEqual(openQuestions({ questions: [], pending_action: q1 }).map((q) => q.id), ['ap-1']);
  assert.deepEqual(openQuestions({ questions: [{ id: 'x', kind: 'other', options: [] }] }), []);
});

test('tap answers 200 -> answered, lingers 4s after state drops it, then prunes', async () => {
  let t = 1000;
  const calls = [];
  let refreshed = 0;
  const q = sampleQuestion();
  let appState = { questions: [q] };

  const flow = createApprovals({
    now: () => t,
    newKey: () => 'idem-key-1',
    postAnswer: async (id, option, key) => {
      calls.push({ id, option, key });
      appState = { questions: [] };
      return { status: 200, body: { ok: true, spoken: 'Told auth yes.' } };
    },
    refresh: async () => { refreshed++; },
  });

  await flow.answer(q, q.options[0]);
  assert.deepEqual(calls, [{ id: 'ap-1', option: 1, key: 'idem-key-1' }]);
  assert.equal(refreshed, 1);

  const lingered = flow.cards(appState);
  assert.equal(lingered.length, 1);
  assert.equal(lingered[0].phase, 'answered');
  assert.equal(lingered[0].outcome, 'You chose Yes, proceed');
  assert.equal(lingered[0].spoken, 'Told auth yes.');

  // Before LINGER_MS: kept. At LINGER_MS: pruned.
  t += LINGER_MS - 1;
  flow.prune(appState);
  assert.equal(flow.cards(appState).length, 1);
  t += 2;
  flow.prune(appState);
  assert.equal(flow.cards(appState).length, 0);
});

test('409 Conflict settles as gone; network or 502 error reopens with retryable message', async () => {
  const q = sampleQuestion();
  let nextResp = { status: 502, body: { error: 'herdr could not send keys' } };

  const flow = createApprovals({
    now: () => 5000,
    newKey: () => 'idem-key-2',
    postAnswer: async () => nextResp,
    refresh: async () => {},
  });

  await flow.answer(q, q.options[1]);
  let cards = flow.cards({ questions: [q] });
  assert.equal(cards[0].phase, 'open');
  assert.equal(cards[0].error, 'herdr could not send keys');

  // Retry succeeds -> Wait: test 409 when answered elsewhere meanwhile!
  nextResp = { status: 409, body: { error: 'that question is no longer open' } };
  await flow.answer(q, q.options[1]);
  cards = flow.cards({ questions: [] });
  assert.equal(cards.length, 1);
  assert.equal(cards[0].phase, 'gone');
  assert.equal(cards[0].message, GONE);
});

test('renderApprovalCardsHTML escapes HTML, marks buttons with data attributes, and formats pageTitle', () => {
  const q = sampleQuestion('ap-xss', '<script>bad</script>');
  const flow = createApprovals({ postAnswer: async () => ({ status: 200, body: {} }) });
  const html = renderApprovalCardsHTML(flow.cards({ questions: [q] }), false);
  assert.ok(!html.includes('<script>'));
  assert.ok(!html.includes('<done>'));
  assert.ok(html.includes('&lt;script&gt;bad&lt;/script&gt;'));
  assert.ok(html.includes('&lt;done&gt;'));
  assert.match(html, /data-pending-id="ap-xss"/);
  assert.match(html, /data-option-index="1"/);
  assert.match(html, /Recommended/);
  assert.match(html, /Leave database untouched/);

  assert.equal(pageTitle(0), 'sheepdog voice');
  assert.equal(pageTitle(2), '(2) sheepdog voice');
  assert.match(newIdempotencyKey(), /^[A-Za-z0-9_-]{16,}$/);
});

test('approvalDigitOption maps 1-9 to primary open card only outside text fields and dialogs', () => {
  const q = sampleQuestion();
  const cards = [{ pending: q, phase: 'open', error: null }];
  const hit = approvalDigitOption(cards, '2', { inField: false, metaKey: false, ctrlKey: false, altKey: false, dialogOpen: false });
  assert.equal(hit.question.id, 'ap-1');
  assert.equal(hit.option.index, 2);

  assert.equal(approvalDigitOption(cards, '2', { inField: true, metaKey: false, ctrlKey: false, altKey: false, dialogOpen: false }), null);
  assert.equal(approvalDigitOption(cards, '2', { inField: false, metaKey: true, ctrlKey: false, altKey: false, dialogOpen: false }), null);
  assert.equal(approvalDigitOption(cards, '2', { inField: false, metaKey: false, ctrlKey: false, altKey: false, dialogOpen: true }), null);
  assert.equal(approvalDigitOption(cards, '9', { inField: false, metaKey: false, ctrlKey: false, altKey: false, dialogOpen: false }), null);
});
