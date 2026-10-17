// Text/background pairs the three pages use must reach WCAG AA (4.5:1) in light and dark
// (docs/dev/brand.md, accessibility). Colours come from tokens.css; `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const css = readFileSync(new URL('tokens.css', import.meta.url), 'utf8');
function block(selector) {
  const at = css.indexOf(selector + ' {');
  const open = css.indexOf('{', at);
  return css.slice(open + 1, css.indexOf('}', open));
}
const decl = (t) => Object.fromEntries([...t.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()]));
const light = decl(block(':root'));
const dark = { ...light, ...decl(block(':root[data-theme="dark"]')) };
const get = (vars, n, d = 0) => {
  const v = vars[n];
  assert.ok(v, `no ${n}`);
  const r = /^var\((--[\w-]+)\)$/.exec(v);
  return r && d < 5 ? get(vars, r[1], d + 1) : v;
};
const rgb = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const lum = (c) => { const [r, g, b] = c.map((x) => { x /= 255; return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4; }); return 0.2126 * r + 0.7152 * g + 0.0722 * b; };
const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
const mix = (a, b, pct) => a.map((x, i) => Math.round(x * pct + b[i] * (1 - pct))); // pct of a, rest b

// [text token or hex, background token or hex or {mix:[token,pct,onToken]}]
const pairs = [
  ['--fg', '--bg'], ['--fg', '--card'], ['--fg', '--code'],
  ['--muted', '--bg'], ['--muted', '--card'],
  ['--primary-text', '--bg'], ['--primary-text', '--card'], ['--primary-text', '--primary-soft'], ['--fg', '--primary-soft'],
  ['--on-primary', '--primary'], ['--on-primary', '--blocked'],
  ['--blocked-text', '--card'], ['--blocked-text', '--bg'],
  ['--danger', '--card'], ['--danger', '--bg'], ['--danger', '--danger-bg'],
  ['--working', '--card'], ['--done', '--card'], ['--idle', '--card'], ['--unknown', '--card'], ['--thinking', '--card'],
  ['--card', '--working'], ['--card', '--done'], ['--card', '--idle'], ['--card', '--unknown'], ['--card', '--thinking'],
  ['--fg', { mix: ['--blocked', 0.1, '--card'] }], ['--blocked-text', { mix: ['--blocked', 0.1, '--card'] }],
  ['--done', { mix: ['--done', 0.12, '--card'] }],
];
for (const [mode, vars] of [['light', light], ['dark', dark]]) {
  const col = (t) => {
    if (typeof t === 'object') return mix(rgb(get(vars, t.mix[0])), rgb(get(vars, t.mix[2])), t.mix[1]);
    return rgb(get(vars, t));
  };
  test(`text pairs reach 4.5:1 (${mode})`, () => {
    for (const [fg, bg] of pairs) {
      const r = ratio(col(fg), col(bg));
      assert.ok(r >= 4.5, `${JSON.stringify(fg)} on ${JSON.stringify(bg)} is ${r.toFixed(2)}:1 in ${mode}`);
    }
  });
}
