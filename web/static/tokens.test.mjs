// web/static/tokens.css and assets/brand/colors.json are two views of one palette
// (docs/dev/brand.md). This fails if a colour they share differs: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const dir = new URL('.', import.meta.url);
const css = readFileSync(new URL('tokens.css', dir), 'utf8');
const json = JSON.parse(readFileSync(new URL('../../assets/brand/colors.json', dir), 'utf8'));

// The text between the braces of the first rule that starts with `selector`.
function block(selector) {
  const at = css.indexOf(selector + ' {');
  assert.ok(at >= 0, `tokens.css has no ${selector} rule`);
  const open = css.indexOf('{', at);
  return css.slice(open + 1, css.indexOf('}', open));
}
const declarations = (text) => Object.fromEntries([...text.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()]));

// Light is :root; dark is the explicit [data-theme="dark"] rule laid over it.
const light = declarations(block(':root'));
const dark = { ...light, ...declarations(block(':root[data-theme="dark"]')) };
// The prefers-color-scheme rule must say the same as the explicit dark rule.
const media = css.slice(css.indexOf('@media (prefers-color-scheme: dark)'), css.indexOf(':root[data-theme="dark"] {'));
const darkAuto = { ...light, ...declarations(media.slice(media.indexOf('{', media.indexOf('{') + 1) + 1)) };

function resolve(vars, name, depth = 0) {
  let v = vars[name];
  assert.ok(v !== undefined, `tokens.css does not define ${name}`);
  const ref = /^var\((--[\w-]+)\)$/.exec(v);
  if (ref) { assert.ok(depth < 5, `var() loop at ${name}`); return resolve(vars, ref[1], depth + 1); }
  return v.toUpperCase();
}
const hex = (v) => String(v).toUpperCase();

// [json value, css token, mode]
const pairs = [];
for (const [k, v] of Object.entries(json.brand)) {
  const token = { orangeDeep: '--orange-deep' }[k] ?? `--${k}`;
  pairs.push([v, token, 'light'], [v, token, 'dark']);
}
for (const mode of ['light', 'dark']) {
  for (const [k, v] of Object.entries(json.surface[mode])) pairs.push([v, `--${k}`, mode]);
}
pairs.push([json.accent.primary, '--primary', 'light'], [json.accent.primary, '--primary', 'dark']);
pairs.push([json.accent.onPrimary, '--on-primary', 'light'], [json.accent.onPrimary, '--on-primary', 'dark']);
for (const mode of ['light', 'dark']) {
  pairs.push([json.accent.primaryText[mode], '--primary-text', mode], [json.accent.primarySoft[mode], '--primary-soft', mode]);
  for (const [k, v] of Object.entries(json.agentState)) {
    const token = k === 'blockedText' ? '--blocked-text' : `--${k}`;
    pairs.push([v[mode], token, mode]);
  }
}

test('tokens.css matches colors.json (light, dark, and the automatic dark rule)', () => {
  assert.ok(pairs.length >= 44, 'expected the test to cover the whole palette');
  for (const [want, token, mode] of pairs) {
    const vars = mode === 'light' ? light : dark;
    assert.equal(resolve(vars, token), hex(want), `${token} (${mode})`);
    if (mode === 'dark') assert.equal(resolve(darkAuto, token), hex(want), `${token} (dark, prefers-color-scheme rule)`);
  }
});

test('app icon background is in the palette file', () => {
  assert.match(json.appIcon.background, /^#[0-9A-F]{6}$/i);
});
