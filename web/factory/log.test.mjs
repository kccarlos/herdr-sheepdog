// Unit tests for PR 14.7: readable /factory log (day headings & times in the
// browser's time zone, typed marks with icon + word, show routine checks toggle,
// task filter, task view's own log, tidy() for SHAs & PR links, empty/error states).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';

const W = {
  heading: 'log',
  taskHeading: 'log for this task',
  taskLabel: 'task',
  allTasks: 'all tasks',
  obsLabel: 'show routine checks',
  merged: 'merged',
  needs: 'held up',
  asked: 'asked you',
  yours: 'you',
  empty: 'Nothing logged yet.',
  taskEmpty: 'Nothing logged for this task yet.',
  onlyObs: 'Nothing but routine checks yet.',
  showThem: 'Show them',
  count100: 'The newest 100 lines.',
  cant: "Can't load the log. Trying again.",
};

const SHA1 = '3f9c1a2b7d0e4c5a9f8e7d6c5b4a39281706f5e4';
const SHA2 = 'abababababababababababababababababababab';

const VOID = new Set(['AREA', 'BASE', 'BR', 'COL', 'EMBED', 'HR', 'IMG', 'INPUT', 'LINK', 'META', 'SOURCE', 'TRACK', 'WBR']);
const unesc = (s) => String(s ?? '').replace(/&#(\d+);/g, (_, n) => String.fromCharCode(+n))
  .replace(/&#x([0-9a-f]+);/gi, (_, h) => String.fromCharCode(parseInt(h, 16)))
  .replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&#39;|&apos;/g, "'").replace(/&amp;/g, '&');

function matchPart(el, tok) {
  let r = tok.trim(), m;
  if (!r) return false;
  if (r[0] === '*') { if (!(r = r.slice(1))) return true; }
  else if ((m = r.match(/^[a-zA-Z][\w-]*/))) { if (el.tagName !== m[0].toUpperCase()) return false; r = r.slice(m[0].length); }
  while (r) {
    if (r[0] === '#' && (m = r.match(/^#([\w-]+)/))) { if (el.id !== m[1]) return false; }
    else if (r[0] === '.' && (m = r.match(/^\.([\w-]+)/))) { if (!el.className.split(/\s+/).includes(m[1])) return false; }
    else if (r[0] === '[' && (m = r.match(/^\[([\w:-]+)(?:=(?:"([^"]*)"|'([^']*)'|([^\]]*)))?\]/))) {
      const a = m[1].toLowerCase(), w = m[2] ?? m[3] ?? m[4];
      if (w === undefined ? (a === 'open' ? !el.open : a === 'disabled' ? !el.disabled : a === 'hidden' ? !el.hidden : a === 'checked' ? !el.checked : !el.hasAttribute(a)) : el.getAttribute(a) !== w) return false;
    } else return false;
    r = r.slice(m[0].length);
  }
  return true;
}
function matchOne(el, sel) {
  const ps = String(sel).trim().replace(/\s*>\s*/g, ' > ').match(/(?:[^\s"'\[]+|\[[^\]]*\]|"[^"]*"|'[^']*')+/g) || [];
  if (!ps.length || !matchPart(el, ps.at(-1))) return false;
  let c = el.parentElement;
  for (let i = ps.length - 2; i >= 0; i--) {
    if (ps[i] === '>') { if (!c || !matchPart(c, ps[--i])) return false; }
    else { while (c && !matchPart(c, ps[i])) c = c.parentElement; if (!c) return false; }
    c = c.parentElement;
  }
  return true;
}

class El {
  constructor(tag, doc) {
    Object.assign(this, { nodeType: 1, tagName: tag.toUpperCase(), ownerDocument: doc, parentElement: null, _nodes: [], _attrs: new Map(), _ls: new Map(), _raw: '', _val: undefined, _chk: undefined });
    const dk = (p) => 'data-' + String(p).replace(/[A-Z]/g, (c) => '-' + c.toLowerCase());
    this.dataset = new Proxy({}, { get: (_, p) => typeof p === 'string' ? this._attrs.get(dk(p)) : undefined, set: (_, p, v) => (this.setAttribute(dk(p), v), true) });
    this.elements = new Proxy({}, { get: (_, p) => typeof p === 'string' ? this.querySelectorAll('input,button,select,textarea').find((c) => c.getAttribute('name') === p || c.id === p) : undefined });
    const cl = () => new Set(this.className.split(/\s+/).filter(Boolean));
    this.classList = {
      contains: (c) => cl().has(c),
      add: (...cs) => { const s = cl(); cs.forEach((c) => s.add(c)); this.className = [...s].join(' '); },
      remove: (...cs) => { const s = cl(); cs.forEach((c) => s.delete(c)); this.className = [...s].join(' '); },
      toggle: (c, f) => { const on = f ?? !this.classList.contains(c); on ? this.classList.add(c) : this.classList.remove(c); return on; },
    };
  }
  get parentNode() { return this.parentElement; } get childNodes() { return this._nodes; }
  get children() { return this._nodes.filter((n) => n.nodeType === 1); } get firstElementChild() { return this.children[0] || null; }
  get nextElementSibling() { const ch = this.parentElement?.children || []; return ch[ch.indexOf(this) + 1] || null; }
  get id() { return this._attrs.get('id') || ''; } set id(v) { this.setAttribute('id', v); }
  get className() { return this._attrs.get('class') || ''; } set className(v) { this.setAttribute('class', v); }
  get name() { return this._attrs.get('name') || ''; } set name(v) { this.setAttribute('name', v); }
  get type() { return this._attrs.get('type') || ''; }
  get hidden() { return this._attrs.has('hidden'); } set hidden(v) { v ? this._attrs.set('hidden', '') : this._attrs.delete('hidden'); }
  get disabled() { return this._attrs.has('disabled'); } set disabled(v) { v ? this._attrs.set('disabled', '') : this._attrs.delete('disabled'); }
  get open() { return this._attrs.has('open'); } set open(v) { v ? this._attrs.set('open', '') : this._attrs.delete('open'); }
  get checked() { return this._chk ?? this._attrs.has('checked'); }
  set checked(v) { this._chk = Boolean(v); if (this._chk) this._attrs.set('checked', ''); else this._attrs.delete('checked'); }
  get value() {
    if (this._val !== undefined) return this._val;
    if (this.tagName === 'SELECT') { const o = this.querySelectorAll('option'), s = o.find((x) => x.hasAttribute('selected')) || o[0]; return s ? (s.hasAttribute('value') ? s.getAttribute('value') : s.textContent) : ''; }
    return this._attrs.get('value') || '';
  }
  set value(v) { this._val = String(v ?? ''); }
  getAttribute(k) { const v = this._attrs.get(String(k).toLowerCase()); return v === undefined ? null : v; }
  setAttribute(k, v) { this._attrs.set(String(k).toLowerCase(), String(v)); }
  hasAttribute(k) { return this._attrs.has(String(k).toLowerCase()); } removeAttribute(k) { this._attrs.delete(String(k).toLowerCase()); }
  get textContent() { return this._nodes.map((n) => n.textContent).join(''); }
  set textContent(v) { this._raw = String(v ?? ''); this._nodes = v ? [{ nodeType: 3, textContent: String(v) }] : []; }
  get innerHTML() { return this._raw; } set innerHTML(h) { this._raw = String(h ?? ''); this._nodes = parseHTML(this._raw, this.ownerDocument, this); }
  _det(n) { if (n?.parentElement) { const i = n.parentElement._nodes.indexOf(n); if (i >= 0) n.parentElement._nodes.splice(i, 1); n.parentElement = null; } }
  _norm(n) { return typeof n === 'string' ? { nodeType: 3, textContent: n } : n; }
  appendChild(n) { this._det(n); n.parentElement = this; this._nodes.push(n); return n; }
  removeChild(n) { const i = this._nodes.indexOf(n); if (i >= 0) { this._nodes.splice(i, 1); n.parentElement = null; } return n; }
  insertBefore(n, r) { if (!r) return this.appendChild(n); this._det(n); const i = this._nodes.indexOf(r); n.parentElement = this; i < 0 ? this._nodes.push(n) : this._nodes.splice(i, 0, n); return n; }
  append(...ns) { ns.forEach((n) => this.appendChild(this._norm(n))); }
  prepend(...ns) { const r = this._nodes[0] || null; ns.forEach((n) => this.insertBefore(this._norm(n), r)); }
  replaceChildren(...ns) { [...this._nodes].forEach((n) => this._det(n)); this.append(...ns); }
  before(...ns) { if (this.parentElement) ns.forEach((n) => this.parentElement.insertBefore(this._norm(n), this)); }
  after(...ns) { if (!this.parentElement) return; const p = this.parentElement, r = p._nodes[p._nodes.indexOf(this) + 1] || null; ns.forEach((n) => p.insertBefore(this._norm(n), r)); }
  remove() { this._det(this); } focus() { if (this.ownerDocument) this.ownerDocument.activeElement = this; }
  contains(o) { for (let c = o; c; c = c.parentElement) if (c === this) return true; return false; }
  matches(s) { return String(s).split(',').some((p) => matchOne(this, p)); }
  closest(s) { for (let c = this; c; c = c.parentElement) if (c.nodeType === 1 && c.matches(s)) return c; return null; }
  querySelectorAll(s) { const out = [], w = (n) => { for (const c of n.children) { if (c.matches(s)) out.push(c); w(c); } }; w(this); return out; }
  querySelector(s) { return this.querySelectorAll(s)[0] || null; }
  addEventListener(t, fn) { (this._ls.get(t) || this._ls.set(t, []).get(t)).push(fn); }
  removeEventListener(t, fn) { const l = this._ls.get(t); if (l) this._ls.set(t, l.filter((x) => x !== fn)); }
  dispatchEvent(ev) {
    const e = ev || { type: '' }; e.target ||= this; e.defaultPrevented = Boolean(e.defaultPrevented);
    e.preventDefault ||= () => { e.defaultPrevented = true; }; e.stopPropagation ||= () => { e._s = true; };
    for (let c = this; c && !e._s; c = c.parentElement) { e.currentTarget = c; for (const fn of c._ls.get(e.type) || []) this.ownerDocument?._track?.(fn(e)); }
    return !e.defaultPrevented;
  }
  click() {
    if (this.disabled) return;
    if (this.tagName === 'INPUT' && this.type === 'checkbox') {
      this.checked = !this.checked;
      this.dispatchEvent({ type: 'click', bubbles: true });
      this.dispatchEvent({ type: 'change', bubbles: true });
      return;
    }
    this.dispatchEvent({ type: 'click', bubbles: true });
  }
}

function parseHTML(html, doc, parent) {
  const root = { _nodes: [], parentElement: null }, stack = [root];
  const re = /<!--[\s\S]*?-->|<\/([a-zA-Z][\w-]*)\s*>|<([a-zA-Z][\w-]*)((?:\s+[A-Za-z_:][\w:.-]*(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>`]+))?)*)\s*\/?>/g;
  let last = 0, m;
  while ((m = re.exec(html))) {
    if (m.index > last) stack.at(-1)._nodes.push({ nodeType: 3, textContent: unesc(html.slice(last, m.index)) });
    last = re.lastIndex;
    if (m[0].startsWith('<!--')) continue;
    if (m[1]) { const t = m[1].toUpperCase(); for (let i = stack.length - 1; i > 0; i--) if (stack[i].tagName === t) { stack.length = i; break; } continue; }
    const el = new El(m[2], doc), p = stack.at(-1);
    el.parentElement = p === root ? parent : p; p._nodes.push(el);
    for (const a of (m[3] || '').matchAll(/([A-Za-z_:][\w:.-]*)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+)))?/g)) el.setAttribute(a[1], unesc(a[2] ?? a[3] ?? a[4] ?? ''));
    if (!VOID.has(el.tagName) && !m[0].endsWith('/>')) stack.push(el);
  }
  if (last < html.length) stack.at(-1)._nodes.push({ nodeType: 3, textContent: unesc(html.slice(last)) });
  return root._nodes;
}

function createDoc(html) {
  const doc = { activeElement: null, root: null, getElementById(id) { return this.root.querySelectorAll('*').find((e) => e.id === id) || null; }, querySelector(s) { return this.root.querySelector(s); }, querySelectorAll(s) { return this.root.querySelectorAll(s); }, createElement(t) { return new El(t, this); } };
  doc.root = new El('document', doc); doc.root._nodes = parseHTML(html, doc, null); return doc;
}

function createIDB() {
  const dbs = new Map(); let pending = 0;
  return { get pending() { return pending; }, open(name) {
    const req = { result: null, onupgradeneeded: null, onsuccess: null }; pending++;
    queueMicrotask(() => {
      const isNew = !dbs.has(name); if (isNew) dbs.set(name, new Map()); const st = dbs.get(name);
      req.result = { createObjectStore: (s) => { if (!st.has(s)) st.set(s, new Map()); }, transaction: (s) => {
        const m = st.get(s) || new Map(), tx = { oncomplete: null, objectStore: () => ({ put: (v, k) => (m.set(k, v), { result: k }), get: (k) => ({ result: m.get(k) }), delete: (k) => (m.delete(k), { result: undefined }) }) };
        pending++; queueMicrotask(() => { tx.oncomplete?.({}); pending--; }); return tx;
      } };
      if (isNew) req.onupgradeneeded?.({}); req.onsuccess?.({}); pending--;
    }); return req;
  } };
}

const defaultTasks = () => [
  { ref: 'T1', title: 'Search box', stage: 'working', layer: 0, after: [], dependents: ['T2'] },
  { ref: 'T2', title: 'Search index', stage: 'done', layer: 1, after: ['T1'], dependents: [] },
];

function fakeDaemon({ logLines = [], tasks = defaultTasks(), repoURL = 'https://github.com/you/notes', logStatus = 200 } = {}) {
  const requests = [];
  let inFlight = 0;
  const reply = (s, o) => ({ ok: s >= 200 && s < 300, status: s, json: async () => o });
  const fetch = async (path) => {
    requests.push(path);
    inFlight++;
    try {
      await Promise.resolve();
      const url = new URL(path, 'http://127.0.0.1:7777');
      if (url.pathname === '/api/factory/projects') {
        const proj = { id: 'proj-demo', goal: 'A notes page with search', state: 'running', running: true, counts: { working: 1, done: 1 } };
        if (repoURL) proj.repo_url = repoURL;
        return reply(200, { protocol: 1, projects: [proj] });
      }
      if (url.pathname === '/api/factory/projects/proj-demo/graph') {
        return reply(200, { protocol: 1, project: 'proj-demo', tasks });
      }
      if (url.pathname === '/api/factory/projects/proj-demo/settings') {
        return reply(200, { protocol: 1, rows: [{ name: 'max_running', label: 'max running', value: '3', default: '3', min: 1, max: 20, top: true }] });
      }
      if (url.pathname === '/api/factory/asks') {
        return reply(200, { protocol: 1, asks: [] });
      }
      if (url.pathname === '/api/factory/projects/proj-demo/log') {
        if (logStatus !== 200) return reply(logStatus, { error: 'log store unavailable' });
        const task = url.searchParams.get('task') || '';
        const obsParam = url.searchParams.get('observations');
        const showObs = obsParam === '1' || obsParam === 'true';
        const n = Math.min(Math.max(parseInt(url.searchParams.get('n') || '50', 10) || 50, 1), 500);
        const scoped = task ? logLines.filter((l) => l.task === task) : logLines;
        const filtered = showObs ? scoped : scoped.filter((l) => l.kind !== 'observation' && l.decision !== '');
        const hasObs = scoped.some((l) => l.kind === 'observation' || l.decision === '');
        return reply(200, { protocol: 1, project: 'proj-demo', lines: filtered.slice(-n), has_observations: hasObs });
      }
      return reply(404, { error: 'not found' });
    } finally { inFlight--; }
  };
  return { fetch, requests, get inFlight() { return inFlight; } };
}

const realSet = globalThis.setTimeout, realClr = globalThis.clearTimeout, realCrypto = globalThis.crypto;
let bootSeq = 0, activeCleanup = null;
async function bootPage(t, daemon, { hash = '', width = 1200 } = {}) {
  if (activeCleanup) await activeCleanup();
  const doc = createDoc(readFileSync(new URL('./index.html', import.meta.url), 'utf8')), idb = createIDB();
  const keys = ['document', 'window', 'location', 'indexedDB', 'fetch', 'crypto', 'addEventListener', 'removeEventListener', 'scrollTo', 'innerWidth', 'matchMedia'];
  const prev = Object.fromEntries(keys.map((k) => [k, Object.getOwnPropertyDescriptor(globalThis, k)]));
  const pending = new Set(), timers = new Map(), winLs = new Map();
  const loc = { hash, href: 'http://127.0.0.1:7777/factory' + hash };
  let seq = 1000, disposed = false;
  const px = (n, u) => +n * (u === 'px' ? 1 : 16);
  const evalMQ = (q) => { const mx = q.match(/max-width:\s*([\d.]+)(px|r?em)/), mn = q.match(/min-width:\s*([\d.]+)(px|r?em)/); return (!mx || width <= px(mx[1], mx[2])) && (!mn || width >= px(mn[1], mn[2])); };
  const track = (p) => { if (p?.then) { const m = Promise.resolve(p).then(() => {}, () => {}).finally(() => pending.delete(m)); pending.add(m); } return p; };
  doc._track = track;
  const subtle = new Proxy(realCrypto.subtle, { get(o, p) { const v = o[p]; return typeof v === 'function' ? (...a) => track(v.apply(o, a)) : v; } });
  globalThis.setTimeout = (fn, ms, ...a) => disposed ? ++seq : (+ms >= 500 ? (timers.set(++seq, { fn: () => fn(...a), ms: +ms }), seq) : realSet(() => { if (!disposed) fn(...a); }, ms));
  globalThis.clearTimeout = (id) => { timers.delete(id) || realClr(id); };
  const setP = (k, v) => Object.defineProperty(globalThis, k, { value: v, configurable: true, writable: true });
  for (const [k, v] of [['document', doc], ['window', globalThis], ['indexedDB', idb], ['crypto', { subtle, getRandomValues: (a) => realCrypto.getRandomValues(a), randomUUID: () => realCrypto.randomUUID() }], ['scrollTo', () => {}], ['innerWidth', width], ['location', loc],
    ['fetch', (p, i) => disposed ? Promise.reject(new Error('disposed')) : track(daemon.fetch(p, i))],
    ['addEventListener', (ev, fn) => (winLs.get(ev) || winLs.set(ev, []).get(ev)).push(fn)], ['removeEventListener', (ev, fn) => { const l = winLs.get(ev); if (l) winLs.set(ev, l.filter((x) => x !== fn)); }]]) setP(k, v);
  setP('matchMedia', (q) => ({ media: q, onchange: null, get matches() { return evalMQ(q); }, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }));
  const flush = async () => { for (let q = 0; q < 3;) { while (pending.size) await Promise.allSettled([...pending]); await new Promise((r) => setImmediate(r)); q = (pending.size || daemon.inFlight || idb.pending) ? 0 : q + 1; } };
  const cleanup = async () => {
    if (disposed) return;
    disposed = true; timers.clear();
    await flush().catch(() => {});
    globalThis.setTimeout = realSet; globalThis.clearTimeout = realClr;
    for (const [k, d] of Object.entries(prev)) d ? Object.defineProperty(globalThis, k, d) : delete globalThis[k];
    if (activeCleanup === cleanup) activeCleanup = null;
  };
  activeCleanup = cleanup; t.after(cleanup);
  await import(`./app.js?boot=${++bootSeq}`);
  await flush();
  return {
    doc, flush,
    async pollNow() { const e = [...timers.entries()].find(([, v]) => v.ms <= 3500); if (e) { timers.delete(e[0]); track(e[1].fn()); } else doc.getElementById('retry').click(); await flush(); },
    async setHash(h) { loc.hash = h; loc.href = 'http://127.0.0.1:7777/factory' + h; for (const fn of winLs.get('hashchange') || []) track(fn({ type: 'hashchange' })); await flush(); },
  };
}

const findCard = (doc, h) => doc.querySelectorAll('section').find((s) => s.querySelector('h2')?.textContent.trim().toLowerCase() === h);
const logItems = (sec) => sec.querySelectorAll('li').filter((li) => li.querySelector('time'));
const dayHeadings = (sec) => sec.querySelectorAll('h3, [role="heading"]');
const findControlByLabel = (sec, doc, labelText, selector) => {
  const lbl = sec.querySelectorAll('label').find((l) => l.textContent.trim().toLowerCase().includes(labelText.toLowerCase()));
  if (!lbl) return null;
  const f = lbl.getAttribute('for');
  const ctrl = (f ? doc.getElementById(f) : null) || lbl.querySelector(selector);
  return ctrl && ctrl.matches(selector) && sec.contains(ctrl) ? ctrl : null;
};
const findMark = (li, word) => li.querySelectorAll('span').find((s) => s.querySelector('svg, [aria-hidden="true"]') && s.textContent.trim() === word);

test('tidy shortens 40-char commit ids with full title, links PR #n only with a GitHub repoURL, and escapes HTML', async () => {
  const { tidy } = await import('./log.js');
  assert.equal(typeof tidy, 'function', 'log.js must export tidy(text, repoURL)');

  const doc = createDoc('');
  const box = doc.createElement('div');
  box.innerHTML = tidy(`accepted ${SHA1} and merged PR #7 as ${SHA2}`, 'https://github.com/you/notes');
  const shaEl1 = box.querySelector(`[title="${SHA1}"]`);
  const shaEl2 = box.querySelector(`[title="${SHA2}"]`);
  assert.ok(shaEl1 && shaEl1.textContent.trim() === SHA1.slice(0, 7), 'first 40-char SHA must shorten to 7 chars with full id as title');
  assert.ok(shaEl2 && shaEl2.textContent.trim() === SHA2.slice(0, 7), 'second 40-char SHA must shorten to 7 chars with full id as title');
  assert.ok(!box.textContent.includes(SHA1) && !box.textContent.includes(SHA2), 'full 40-char SHAs must not appear as text');
  const prLink = box.querySelector('a[href="https://github.com/you/notes/pull/7"]');
  assert.ok(prLink, 'PR #7 must link to GitHub pull/7 when repoURL is set');
  assert.equal(prLink.textContent.trim(), 'PR #7');
  assert.equal(prLink.getAttribute('aria-label'), 'PR #7, opens GitHub');

  // Without repoURL: PR #7 stays plain text, SHA still shortens.
  const noAddr = doc.createElement('div');
  noAddr.innerHTML = tidy(`merged PR #7 at ${SHA1}`, '');
  assert.equal(noAddr.querySelector('a'), null, 'PR #7 must stay plain text when repoURL is empty');
  assert.ok(noAddr.textContent.includes('PR #7'));
  assert.equal(noAddr.querySelector(`[title="${SHA1}"]`)?.textContent.trim(), SHA1.slice(0, 7));

  // Non-GitHub / unsafe URL must never produce an anchor link.
  const badAddr = doc.createElement('div');
  badAddr.innerHTML = tidy('merged PR #7', 'javascript:alert(1)');
  assert.equal(badAddr.querySelector('a'), null, 'unsafe repoURL must not produce a link');

  // HTML in log text is escaped; 7-char and 41-char hex tokens are left alone.
  const escaped = tidy(`<script>alert(1)</script> short 3f9c1a2 long ${'a'.repeat(41)} PR #12`, 'https://github.com/you/notes');
  assert.ok(!escaped.includes('<script>'), 'raw HTML must be escaped');
  const escBox = doc.createElement('div');
  escBox.innerHTML = escaped;
  assert.equal(escBox.querySelectorAll('[title]').length, 0, '7-char and 41-char hex must not be truncated as commit ids');
  assert.ok(escBox.querySelector('a[href="https://github.com/you/notes/pull/12"]'), 'PR #12 still links after escaped HTML');
});

test('renderLogHTML groups lines under browser-timezone day headings across midnight in two time zones', async () => {
  const logURL = new URL('./log.js', import.meta.url).href;
  const lines = [
    { at: '2025-03-10T23:30:00Z', by: 'engine', task: 'T1', decision: 'line-a', why: 'late utc march 10' },
    { at: '2025-03-11T01:30:00Z', by: 'engine', task: 'T1', decision: 'line-b', why: 'crosses midnight utc vs la' },
    { at: '2025-03-11T08:15:00Z', by: 'engine', task: 'T1', decision: 'line-c', why: 'early march 11 in both' },
  ];
  const renderInTZ = (tz) => execFileSync(
    process.execPath,
    ['--input-type=module', '-e', `const { renderLogHTML } = await import(${JSON.stringify(logURL)}); process.stdout.write(renderLogHTML(${JSON.stringify(lines)}, { tasks: ['T1'] }, ''));`],
    { env: { ...process.env, TZ: tz }, encoding: 'utf8' },
  );

  // Helper to collect day groups as [{ heading, items: [textContent...] }] in rendered order.
  const groupsOf = (html) => {
    const doc = createDoc(html);
    const out = [];
    for (const el of doc.root.querySelectorAll('h3, [role="heading"], li')) {
      if (el.matches('h3, [role="heading"]')) out.push({ heading: el.textContent.trim(), items: [] });
      else if (el.querySelector('time') && out.length) out.at(-1).items.push(el);
    }
    return out;
  };

  // In UTC: March 11 has [line-c, line-b]; March 10 has [line-a].
  const utc = groupsOf(renderInTZ('UTC'));
  assert.equal(utc.length, 2, `UTC must produce 2 day headings, got ${JSON.stringify(utc.map((g) => g.heading))}`);
  assert.match(utc[0].heading, /\b11\b/, `UTC first heading must be day 11, got "${utc[0].heading}"`);
  assert.match(utc[1].heading, /\b10\b/, `UTC second heading must be day 10, got "${utc[1].heading}"`);
  assert.equal(utc[0].items.length, 2, 'UTC day 11 must hold line-c and line-b');
  assert.equal(utc[1].items.length, 1, 'UTC day 10 must hold line-a');
  assert.ok(utc[0].items[0].textContent.includes('line-c') && utc[0].items[1].textContent.includes('line-b'), 'newest first within day');

  // In America/Los_Angeles (UTC-7 on 2025-03-11): March 11 has [line-c]; March 10 has [line-b, line-a].
  const la = groupsOf(renderInTZ('America/Los_Angeles'));
  assert.equal(la.length, 2, `LA must produce 2 day headings, got ${JSON.stringify(la.map((g) => g.heading))}`);
  assert.match(la[0].heading, /\b11\b/, `LA first heading must be day 11, got "${la[0].heading}"`);
  assert.match(la[1].heading, /\b10\b/, `LA second heading must be day 10, got "${la[1].heading}"`);
  assert.equal(la[0].items.length, 1, 'LA day 11 must hold only line-c');
  assert.equal(la[1].items.length, 2, 'LA day 10 must hold line-b and line-a across UTC midnight');
  assert.ok(la[1].items[0].textContent.includes('line-b') && la[1].items[1].textContent.includes('line-a'));
  // Times beside lines reflect the browser's time zone (line-b is 01:30 in UTC, 18:30 or 6:30 in LA).
  assert.match(utc[0].items[1].querySelector('time').textContent, /\b0?1:30\b/);
  assert.match(la[1].items[0].querySelector('time').textContent, /\b(?:18|0?6):30\b/);

  // Today / Yesterday / older day heading labels in the current browser TZ.
  const { renderLogHTML } = await import('./log.js');
  const now = new Date();
  const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1, 12, 0, 0);
  const older = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 3, 12, 0, 0);
  const relGroups = groupsOf(renderLogHTML([
    { at: older.toISOString(), by: 'engine', task: 'T1', decision: 'old-step', why: '' },
    { at: yesterday.toISOString(), by: 'engine', task: 'T1', decision: 'yest-step', why: '' },
    { at: now.toISOString(), by: 'engine', task: 'T1', decision: 'now-step', why: '' },
  ], { tasks: ['T1'] }, ''));
  assert.equal(relGroups.length, 3, 'expected 3 distinct day headings (Today, Yesterday, older)');
  assert.match(relGroups[0].heading, /^Today,\s+\S+/, `expected "Today, <date>", got "${relGroups[0].heading}"`);
  assert.match(relGroups[1].heading, /^Yesterday,\s+\S+/, `expected "Yesterday, <date>", got "${relGroups[1].heading}"`);
  assert.doesNotMatch(relGroups[2].heading, /^(?:Today|Yesterday),/, `older heading must be weekday and date alone, got "${relGroups[2].heading}"`);
});

test('board log renders marks with icon and word, reads you instead of owner, tidies SHAs and PR links, and shows newest-100 note only when full', async (t) => {
  const baseLines = [
    { at: '2026-11-23T21:30:00Z', by: 'owner', decision: 'started', why: 'A notes page with search', kind: 'yours' },
    { at: '2026-11-24T12:50:00Z', by: 'engine', task: 'T1', decision: 'opened a recap', why: '212 lines, cap 160', kind: 'needs_decision' },
    { at: '2026-11-24T13:22:00Z', by: 'engine', task: 'T1', decision: 'started reviewer', why: `review round 2 on ${SHA1}` },
    { at: '2026-11-24T13:41:00Z', by: 'engine', task: 'T1', decision: 'asked you', why: 'Delete the old notes table?', kind: 'asked_you' },
    { at: '2026-11-24T13:58:00Z', by: 'engine', task: 'T2', decision: `done: PR #7 merged as ${SHA2}`, why: 'the PR is merged', kind: 'merged' },
  ];
  const page = await bootPage(t, fakeDaemon({ logLines: baseLines, repoURL: 'https://github.com/you/notes' }));
  const sec = findCard(page.doc, W.heading);
  assert.ok(sec, 'expected log section on board');
  const items = logItems(sec);
  assert.equal(items.length, 5, 'expected 5 visible log lines');
  assert.ok(dayHeadings(sec).length >= 1, 'expected at least one day heading');

  // Newest first: merged -> asked_you -> plain -> needs_decision -> yours.
  assert.ok(findMark(items[0], W.merged), 'merged line must carry icon + "merged" mark');
  assert.ok(findMark(items[1], W.asked), 'asked_you line must carry icon + "asked you" mark');
  assert.equal(items[2].querySelectorAll('span').find((s) => s.querySelector('svg')), undefined, 'plain line must carry no mark');
  assert.ok(findMark(items[3], W.needs), 'needs_decision line must carry icon + "held up" mark');
  assert.doesNotMatch(items[3].textContent, /needs a decision|needs_decision/i, 'must use "held up", never "needs a decision"');
  assert.ok(findMark(items[4], W.yours), 'yours line must carry icon + "you" mark');
  assert.doesNotMatch(items[4].textContent, /\bowner\b/i, 'yours line must read "you", not "owner"');

  // Shortened commit ids and GitHub PR link.
  assert.equal(items[2].querySelector(`[title="${SHA1}"]`)?.textContent.trim(), SHA1.slice(0, 7));
  assert.equal(items[0].querySelector(`[title="${SHA2}"]`)?.textContent.trim(), SHA2.slice(0, 7));
  assert.ok(!sec.textContent.includes(SHA1) && !sec.textContent.includes(SHA2), 'no full 40-char commit id on screen');
  const prLink = items[0].querySelector('a[href="https://github.com/you/notes/pull/7"]');
  assert.ok(prLink && prLink.getAttribute('aria-label') === 'PR #7, opens GitHub', 'PR #7 must link to GitHub with wording-table aria-label');
  assert.ok(!sec.textContent.includes(W.count100), 'newest-100 footer must not show when fewer than 100 lines');

  // When 100 lines are returned, show "The newest 100 lines.".
  const hundred = Array.from({ length: 100 }, (_, i) => ({
    at: new Date(Date.UTC(2026, 10, 24, 10, 0, i)).toISOString(),
    by: 'engine', task: 'T1', decision: `step-${i}`, why: 'working',
  }));
  const fullPage = await bootPage(t, fakeDaemon({ logLines: hundred }));
  assert.ok(findCard(fullPage.doc, W.heading)?.textContent.includes(W.count100), 'expected "The newest 100 lines." when log is full');
});

test('PR number stays plain text when project has no stored repo_url', async (t) => {
  const lines = [{ at: '2026-11-24T13:58:00Z', by: 'engine', task: 'T2', decision: 'done: PR #7 merged', why: 'merged', kind: 'merged' }];
  const page = await bootPage(t, fakeDaemon({ logLines: lines, repoURL: '' }));
  const sec = findCard(page.doc, W.heading);
  assert.ok(sec.textContent.includes('PR #7'), 'PR #7 must appear as text');
  assert.equal(sec.querySelector('a[href*="/pull/7"]'), null, 'PR #7 must not link without repo_url');
});

test('show routine checks toggle asks daemon again with observations=1 and renders observations muted without bold decision', async (t) => {
  const lines = [
    { at: '2026-11-24T13:57:00Z', by: 'engine', task: 'T2', decision: '', why: 'the checks rollup changed: 4 of 4 passed', kind: 'observation' },
    { at: '2026-11-24T13:58:00Z', by: 'engine', task: 'T2', decision: 'merged PR #7', why: 'green', kind: 'merged' },
    { at: '2026-11-24T14:00:00Z', by: 'engine', task: 'T1', decision: '', why: 'the PR snapshot changed', kind: 'observation' },
  ];
  const daemon = fakeDaemon({ logLines: lines });
  const page = await bootPage(t, daemon);
  const sec = () => findCard(page.doc, W.heading);
  const chk = () => findControlByLabel(sec(), page.doc, W.obsLabel, 'input[type="checkbox"]');
  assert.ok(chk(), 'expected checkbox labelled "show routine checks"');
  assert.equal(chk().checked, false, 'show routine checks must start unchecked');
  assert.ok(!sec().textContent.includes('the PR snapshot changed'), 'observations must stay hidden until toggled on');

  daemon.requests.length = 0;
  chk().click();
  await page.flush();
  assert.ok(daemon.requests.some((u) => u.includes('/log') && /observations=(?:1|true)/.test(u)), 'turning toggle on must re-fetch log with observations=1');
  assert.equal(chk().checked, true, 'checkbox stays checked after re-render');
  const snapItem = logItems(sec()).find((li) => li.textContent.includes('the PR snapshot changed'));
  assert.ok(snapItem, 'observation line must render once toggle is on');
  const boldText = snapItem.querySelectorAll('b, strong, .b').map((e) => e.textContent.trim()).join('');
  assert.equal(boldText, '', 'observation line must have no bold decision');

  // Toggle survives 3 s poll.
  daemon.requests.length = 0;
  await page.pollNow();
  assert.equal(chk().checked, true, 'toggle state must survive 3 s poll');
  assert.ok(daemon.requests.some((u) => u.includes('/log') && /observations=(?:1|true)/.test(u)), '3 s poll must keep requesting observations=1 while checked');

  // Turning toggle off asks without observations=1 and hides snapshots again.
  daemon.requests.length = 0;
  chk().click();
  await page.flush();
  assert.equal(chk().checked, false);
  assert.ok(daemon.requests.some((u) => u.includes('/log') && !/observations=(?:1|true)/.test(u)), 'turning toggle off must fetch without observations=1');
  assert.ok(!sec().textContent.includes('the PR snapshot changed'), 'observations must hide again when toggle is off');
});

test('board task filter asks daemon for that task and survives 3 s poll', async (t) => {
  const lines = [
    { at: '2026-11-24T13:00:00Z', by: 'engine', task: 'T1', decision: 't1-work', why: 'search box' },
    { at: '2026-11-24T13:01:00Z', by: 'engine', task: 'T1', decision: '', why: 't1 snapshot', kind: 'observation' },
    { at: '2026-11-24T13:05:00Z', by: 'engine', task: 'T2', decision: 't2-merge', why: 'search index', kind: 'merged' },
  ];
  const daemon = fakeDaemon({ logLines: lines });
  const page = await bootPage(t, daemon);
  const sec = () => findCard(page.doc, W.heading);
  const sel = () => findControlByLabel(sec(), page.doc, W.taskLabel, 'select');
  assert.ok(sel(), 'expected <select> labelled "task" in board log');
  const optTexts = sel().querySelectorAll('option').map((o) => o.textContent.trim());
  assert.equal(optTexts[0], W.allTasks, 'first task filter option must be "all tasks"');
  assert.ok(optTexts.includes('T1') && optTexts.includes('T2'), `task filter must list tasks, got ${JSON.stringify(optTexts)}`);

  daemon.requests.length = 0;
  sel().value = 'T1';
  sel().dispatchEvent({ type: 'change', bubbles: true });
  await page.flush();
  assert.ok(daemon.requests.some((u) => u.includes('/log') && /[?&]task=T1(?:&|$)/.test(u)), 'selecting T1 must re-fetch with ?task=T1');
  assert.ok(sec().textContent.includes('t1-work') && !sec().textContent.includes('t2-merge'), 'filtered log must show only T1 lines');

  // Filter survives 3 s poll and combines with show routine checks.
  daemon.requests.length = 0;
  await page.pollNow();
  assert.equal(sel().value, 'T1', 'selected task filter must survive 3 s poll');
  assert.ok(daemon.requests.some((u) => u.includes('/log') && /[?&]task=T1(?:&|$)/.test(u)), '3 s poll must keep task=T1');

  daemon.requests.length = 0;
  findControlByLabel(sec(), page.doc, W.obsLabel, 'input[type="checkbox"]').click();
  await page.flush();
  assert.ok(daemon.requests.some((u) => u.includes('/log') && /[?&]task=T1(?:&|$)/.test(u) && /observations=(?:1|true)/.test(u)), 'toggle while filtered must send both task=T1 and observations=1');
  assert.ok(sec().textContent.includes('t1 snapshot'), 'T1 observation must render when both filter and toggle are on');
});

test('empty log shows Nothing logged yet with no Show them button', async (t) => {
  const page = await bootPage(t, fakeDaemon({ logLines: [] }));
  const sec = findCard(page.doc, W.heading);
  assert.ok(sec.textContent.includes(W.empty), 'empty log must show "Nothing logged yet."');
  assert.equal(sec.querySelectorAll('button').find((b) => b.textContent.trim() === W.showThem), undefined, 'empty log must not offer "Show them"');
});

test('log with only observations shows Nothing but routine checks yet and Show them turns the checkbox on', async (t) => {
  const lines = [
    { at: '2026-11-24T14:00:00Z', by: 'engine', task: 'T1', decision: '', why: 'the PR snapshot changed', kind: 'observation' },
  ];
  const daemon = fakeDaemon({ logLines: lines });
  const page = await bootPage(t, daemon);
  const sec = () => findCard(page.doc, W.heading);
  assert.ok(sec().textContent.includes(W.onlyObs), 'observation-only log must show "Nothing but routine checks yet."');
  const btn = sec().querySelectorAll('button').find((b) => b.textContent.trim() === W.showThem);
  assert.ok(btn, 'observation-only log must show "Show them" button');

  daemon.requests.length = 0;
  btn.click();
  await page.flush();
  assert.equal(findControlByLabel(sec(), page.doc, W.obsLabel, 'input[type="checkbox"]')?.checked, true, '"Show them" must turn the checkbox on');
  assert.ok(daemon.requests.some((u) => u.includes('/log') && /observations=(?:1|true)/.test(u)), '"Show them" must fetch with observations=1');
  assert.ok(sec().textContent.includes('the PR snapshot changed'), 'routine checks must render after clicking Show them');
});

test('cannot-load log state shows error status while tasks board still works', async (t) => {
  const page = await bootPage(t, fakeDaemon({ logStatus: 500 }));
  const sec = findCard(page.doc, W.heading);
  assert.ok(sec, 'log section must still render when /log fails');
  assert.ok(sec.querySelector('[role="status"]')?.textContent.includes(W.cant), 'expected role=status with "Can\'t load the log. Trying again."');
  assert.ok(page.doc.getElementById('main').textContent.includes('Search box'), 'tasks board must still render when /log fails');
});

test('task view renders log for this task, asks daemon for task lines past newest 100, has routine checks toggle without task select, and shows task empty state', async (t) => {
  const lines = [
    { at: '2026-11-23T22:40:00Z', by: 'engine', task: 'T1', decision: 'started worker', why: 'early T1 line' },
    { at: '2026-11-23T22:45:00Z', by: 'engine', task: 'T1', decision: '', why: 'early T1 snapshot', kind: 'observation' },
    ...Array.from({ length: 110 }, (_, i) => ({
      at: new Date(Date.UTC(2026, 10, 24, 10, 0, i)).toISOString(),
      by: 'engine', task: 'T2', decision: `t2-noise-${i}`, why: 'newer than T1',
    })),
  ];
  const daemon = fakeDaemon({ logLines: lines });
  const page = await bootPage(t, daemon, { hash: '#proj-demo/T1' });
  const sec = () => findCard(page.doc, W.taskHeading);
  assert.ok(sec(), 'task view must have "log for this task" heading (replacing "recent log")');
  assert.equal(findCard(page.doc, 'recent log'), undefined, 'old "recent log" heading must be gone');
  assert.ok(daemon.requests.some((u) => u.includes('/log') && /[?&]task=T1(?:&|$)/.test(u)), 'task view must request ?task=T1 from daemon');
  assert.ok(sec().textContent.includes('early T1 line') && !sec().textContent.includes('t2-noise-0'), 'task view must show T1 lines older than newest 100 project lines');
  assert.equal(findControlByLabel(sec(), page.doc, W.taskLabel, 'select'), null, 'task view log must not show the board task filter select');
  const chk = findControlByLabel(sec(), page.doc, W.obsLabel, 'input[type="checkbox"]');
  assert.ok(chk, 'task view log must offer "show routine checks" checkbox');
  daemon.requests.length = 0;
  chk.click();
  await page.flush();
  assert.ok(daemon.requests.some((u) => u.includes('/log') && /[?&]task=T1(?:&|$)/.test(u) && /observations=(?:1|true)/.test(u)), 'task view toggle must request both task=T1 and observations=1');
  assert.ok(sec().textContent.includes('early T1 snapshot'), 'T1 observation must render once toggled on in task view');

  // Empty task log shows "Nothing logged for this task yet."
  const emptyTaskPage = await bootPage(t, fakeDaemon({ logLines: [{ at: '2026-11-24T10:00:00Z', by: 'engine', task: 'T2', decision: 'other', why: '' }] }), { hash: '#proj-demo/T1' });
  assert.ok(findCard(emptyTaskPage.doc, W.taskHeading)?.textContent.includes(W.taskEmpty), 'empty task log must show "Nothing logged for this task yet."');
});

test('log.js exports renderLogHTML and tidy; app.js deletes logHTML and logWhy and keeps no POST', async () => {
  const mod = await import('./log.js');
  assert.equal(typeof mod.renderLogHTML, 'function', 'log.js must export renderLogHTML');
  assert.equal(typeof mod.tidy, 'function', 'log.js must export tidy');
  const appSrc = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
  assert.doesNotMatch(appSrc, /\b(?:const|let|var|function)\s+logHTML\b/, 'app.js must delete logHTML');
  assert.doesNotMatch(appSrc, /\b(?:const|let|var|function)\s+logWhy\b/, 'app.js must delete logWhy');
  assert.doesNotMatch(appSrc, /\bPOST\b|method\s*:/, 'app.js must still hold no POST');
});
