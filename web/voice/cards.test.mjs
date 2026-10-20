import { test } from 'node:test';
import assert from 'node:assert/strict';
import { visibilityMessage, cardsEnabled, renderMarkdown, cardBodyHTML, inline } from './cards.js';

test('visibility: the page is the voice screen, visible unless hidden', () => {
  assert.deepEqual(visibilityMessage(false), { type: 'visibility', visible: true, voice_screen: true });
  assert.deepEqual(visibilityMessage(true), { type: 'visibility', visible: false, voice_screen: true });
});

test('cards only when bot-ready lists the feature', () => {
  assert.equal(cardsEnabled(['screen', 'cards']), true);
  assert.equal(cardsEnabled(['screen']), false);
  assert.equal(cardsEnabled(undefined), false);
});

test('markdown escapes HTML everywhere', () => {
  const html = renderMarkdown('<script>alert(1)</script>\n\n- <b>x</b>\n\n```\n<img src=x>\n```');
  assert.ok(!html.includes('<script>'));
  assert.ok(!html.includes('<b>'));
  assert.ok(!html.includes('<img'));
  assert.ok(html.includes('&lt;script&gt;'));
});

test('markdown: table, list, heading, code fence', () => {
  const html = renderMarkdown('## Today\n\n| Agent | State |\n|---|---|\n| auth | done |\n| docs | **working** |\n\n1. one\n2. two\n\n```diff\n+a\n```');
  assert.match(html, /<h4>Today<\/h4>/);
  assert.match(html, /<table><thead><tr><th>Agent<\/th><th>State<\/th><\/tr><\/thead><tbody><tr><td>auth<\/td><td>done<\/td><\/tr><tr><td>docs<\/td><td><strong>working<\/strong><\/td><\/tr><\/tbody><\/table>/);
  assert.match(html, /<ol><li>one<\/li><li>two<\/li><\/ol>/);
  assert.match(html, /<pre class="code lang-diff"><code>\+a<\/code><\/pre>/);
});

test('links: only http(s)', () => {
  assert.match(inline('[docs](https://example.com/a)'), /<a href="https:\/\/example.com\/a"/);
  assert.ok(!inline('[x](javascript:alert(1))').includes('<a'));
});

test('approval card is read-only: no buttons', () => {
  const html = cardBodyHTML({ kind: 'approval', question: 'run the migration?', command: 'make migrate', options: [{ index: 1, label: 'Yes' }, { index: 2, label: 'No' }] });
  assert.ok(!html.includes('<button'));
  assert.match(html, /Yes · No/);
  assert.match(html, /make migrate/);
});

test('excerpt is monospace and escaped', () => {
  assert.equal(cardBodyHTML({ kind: 'excerpt', text: 'a < b' }), '<pre class="code excerpt"><code>a &lt; b</code></pre>');
});
