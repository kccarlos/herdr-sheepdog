// Unit tests for web/factory/goal.js: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { goalTitle } from './goal.js';

test('goalTitle returns fallback for empty, null, undefined, whitespace, or marker-only input', () => {
  assert.equal(goalTitle('', 'proj-1'), 'proj-1');
  assert.equal(goalTitle(null, 'proj-1'), 'proj-1');
  assert.equal(goalTitle(undefined, 'proj-1'), 'proj-1');
  assert.equal(goalTitle('   \t\r\n  \n ', 'proj-1'), 'proj-1');
  assert.equal(goalTitle('###   ', 'proj-1'), 'proj-1');
  assert.equal(goalTitle('****', 'proj-1'), 'proj-1');
  assert.equal(goalTitle('', ''), '');
});

test('goalTitle trims a single short line', () => {
  assert.equal(goalTitle('  Ship the factory view  ', 'proj-1'), 'Ship the factory view');
});

test('goalTitle skips leading blank lines and handles CRLF line endings', () => {
  assert.equal(
    goalTitle('\r\n  \r\nFirst real line\r\nSecond line\r\n', 'proj-1'),
    'First real line',
  );
  assert.equal(
    goalTitle('\n\n\t\nSpeed up the drawer\n\n## Details\n- item one', 'proj-1'),
    'Speed up the drawer',
  );
});

test('goalTitle strips leading ATX headings (#..######) and ** bold markers only', () => {
  assert.equal(goalTitle('# Main goal', 'proj-1'), 'Main goal');
  assert.equal(goalTitle('### **Speed up** the **drawer**', 'proj-1'), 'Speed up the drawer');
  assert.equal(goalTitle('###### Level six heading', 'proj-1'), 'Level six heading');
  assert.equal(goalTitle('- Keep leading dash and `code`', 'proj-1'), '- Keep leading dash and `code`');
});

test('goalTitle cuts lines over 120 code points at the last word boundary and appends ellipsis', () => {
  const exact120 = 'a'.repeat(120);
  assert.equal(goalTitle(exact120, 'proj-1'), exact120);

  // 23 * 5 = 115 chars ("word " repeated), last space at index 114, followed by a 12-char word straddling 120.
  const longWithSpaces = 'word '.repeat(23) + 'longwordhere';
  const expectedWordCut = `${'word '.repeat(22)}word…`;
  assert.equal(goalTitle(longWithSpaces, 'proj-1'), expectedWordCut);
});

test('goalTitle hard-cuts at 120 code points when there is no whitespace and never splits an emoji', () => {
  const noSpaces = 'x'.repeat(130);
  assert.equal(goalTitle(noSpaces, 'proj-1'), `${'x'.repeat(120)}…`);

  // 119 ASCII chars + 1 surrogate-pair emoji (UTF-16 indices 119..120, code-point index 119) + 10 ASCII chars.
  const withEmojiAtBoundary = `${'a'.repeat(119)}🚀${'b'.repeat(10)}`;
  const got = goalTitle(withEmojiAtBoundary, 'proj-1');
  assert.equal(got, `${'a'.repeat(119)}🚀…`);
  assert.equal([...got].length, 121);
});
