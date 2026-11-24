// Unit tests for shared safe markdown renderer in web/static/markdown.js: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { esc, inline, renderMarkdown } from './markdown.js';

test('esc escapes HTML special characters', () => {
  assert.equal(esc(`<a href="x" title='y'>&`), '&lt;a href=&quot;x&quot; title=&#39;y&#39;&gt;&amp;');
});

test('renderMarkdown renders ## and ### headings as h4 and h5', () => {
  const html = renderMarkdown('## Scope\n\n### Details');
  assert.match(html, /<h4>Scope<\/h4>/);
  assert.match(html, /<h5>Details<\/h5>/);
});

test('renderMarkdown renders bullet and numbered lists', () => {
  const html = renderMarkdown('- alpha\n- **beta**\n\n1. first\n2. `second`');
  assert.match(html, /<ul><li>alpha<\/li><li><strong>beta<\/strong><\/li><\/ul>/);
  assert.match(html, /<ol><li>first<\/li><li><code>second<\/code><\/li><\/ol>/);
});

test('renderMarkdown keeps raw HTML escaped', () => {
  const html = renderMarkdown('<script>alert(1)</script>\n\n- <img src=x onerror=alert(1)>\n- <b>bold</b>');
  assert.ok(!html.includes('<script'), 'raw <script tag must not appear');
  assert.ok(!html.includes('<img'), 'raw <img tag must not appear');
  assert.ok(!html.includes('<b>'), 'raw <b> tag must not appear');
  assert.ok(html.includes('&lt;script&gt;alert(1)&lt;/script&gt;'));
  assert.ok(html.includes('&lt;img src=x onerror=alert(1)&gt;'));
  assert.ok(html.includes('&lt;b&gt;bold&lt;/b&gt;'));
});

test('links reject javascript: URLs and add rel="noopener noreferrer" to https:// links', () => {
  assert.ok(!inline('[x](javascript:alert(1))').includes('<a'));
  assert.ok(!renderMarkdown('[x](javascript:alert(1))').includes('<a'));

  const linkHtml = inline('[docs](https://example.com/a)');
  assert.match(linkHtml, /<a href="https:\/\/example\.com\/a"[^>]*rel="noopener noreferrer"[^>]*>docs<\/a>/);
  assert.match(
    renderMarkdown('See [docs](https://example.com/a).'),
    /<a href="https:\/\/example\.com\/a"[^>]*rel="noopener noreferrer"[^>]*>docs<\/a>/,
  );
});
