// Unit tests for PR 14.8: /factory task step bar on board rows, full named
// steps card and details fold in the task view (`make test-web`).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const W = {
  stepsH: 'steps',
  details: 'details',
  done: 'done',
  now: 'now',
  todo: 'to come',
  skipped: 'skipped',
  reviewNow: 'review round 2; needs 2 passing, at most 5',
  reviewNeeds: 'needs 2 passing, at most 5',
  reviewTwo: '2 rounds done',
  reviewOne: '1 round done',
  reviewThree: '3 rounds done',
  sentBack: 'sent back from review round 1; 1 round done',
  barBack: 'implementing, sent back from review round 1',
  skipText: 'skipped by your override',
  overrideWhy: 'Merging now, by your override',
  barSkip: 'CI, review skipped by your override',
  retroText: 'a retro reviewer is looking at why review keeps failing',
  droppedText: 'ended without merging',
  barDropped: 'dropped, ended without merging',
  closedText: 'ended without merging: its PR was closed',
  parkedText: 'parked here, waiting on the coordinator',
};

const sampleTasks = () => [
  {
    ref: 'T7', title: 'Highlight the words found', stage: 'waiting_on_dep', why_waiting: 'Waiting on T4, T5',
    layer: 2, after: ['T4', 'T5'], dependents: [], rounds: 0,
    steps: [
      { name: 'planned', state: 'now' },
      { name: 'implementing', state: 'todo' },
      { name: 'review', state: 'todo' },
      { name: 'CI', state: 'todo' },
      { name: 'merged', state: 'todo' },
    ],
  },
  {
    ref: 'T4', title: 'Search box on the notes page', brief: 'Add a search box above the notes list.',
    stage: 'working', why_waiting: 'In review round 2', model: 'claude sonnet', terminal: 't-41',
    last_rule: 'N22', last_decision: 'started reviewer', layer: 2, after: ['T3'], dependents: ['T7'], rounds: 1,
    steps: [
      { name: 'planned', state: 'done' },
      { name: 'implementing', state: 'done' },
      { name: 'review', state: 'now', text: W.reviewNow },
      { name: 'CI', state: 'todo' },
      { name: 'merged', state: 'todo' },
    ],
  },
  {
    ref: 'T5', title: 'Search index for note text', stage: 'working', why_waiting: 'Working · claude sonnet',
    model: 'claude sonnet', terminal: 't-51', layer: 1, after: [], dependents: ['T7'], rounds: 1,
    steps: [
      { name: 'planned', state: 'done' },
      { name: 'implementing', state: 'now', text: W.sentBack },
      { name: 'review', state: 'todo', text: W.reviewNeeds },
      { name: 'CI', state: 'todo' },
      { name: 'merged', state: 'todo' },
    ],
  },
  {
    ref: 'T9', title: 'Sort notes by date', stage: 'ci', why_waiting: W.overrideWhy,
    model: 'claude sonnet', layer: 1, after: [], dependents: [], rounds: 0,
    steps: [
      { name: 'planned', state: 'done' },
      { name: 'implementing', state: 'done' },
      { name: 'review', state: 'skipped', text: W.skipText },
      { name: 'CI', state: 'now', text: W.overrideWhy },
      { name: 'merged', state: 'todo' },
    ],
  },
  {
    ref: 'T2', title: 'Notes table and model', stage: 'done', why_waiting: '',
    model: 'claude sonnet', layer: 0, after: [], dependents: ['T3'], rounds: 2,
    steps: [
      { name: 'planned', state: 'done' },
      { name: 'implementing', state: 'done' },
      { name: 'review', state: 'done', text: W.reviewTwo },
      { name: 'CI', state: 'done' },
      { name: 'merged', state: 'done', text: 'PR #7' },
    ],
  },
  {
    ref: 'T10', title: 'Export notes as a file', stage: 'done', why_waiting: '',
    layer: 1, after: [], dependents: [], rounds: 0,
    steps: [
      { name: 'planned', state: 'done' },
      { name: 'implementing', state: 'done' },
      { name: 'dropped', state: 'ended', text: W.droppedText },
    ],
  },
  {
    ref: 'T11', title: 'Retro task', stage: 'working', why_waiting: 'In retro',
    model: 'claude opus', layer: 1, after: [], dependents: [], rounds: 3,
    steps: [
      { name: 'planned', state: 'done' },
      { name: 'implementing', state: 'done' },
      { name: 'review', state: 'done', text: W.reviewThree },
      { name: 'retro', state: 'now', text: W.retroText },
      { name: 'CI', state: 'todo' },
      { name: 'merged', state: 'todo' },
    ],
  },
  {
    ref: 'T12', title: 'Closed PR task', stage: 'done', why_waiting: '',
    layer: 1, after: [], dependents: [], rounds: 1,
    steps: [
      { name: 'planned', state: 'done' },
      { name: 'implementing', state: 'done' },
      { name: 'review', state: 'done', text: W.reviewOne },
      { name: 'closed', state: 'ended', text: W.closedText },
    ],
  },
  {
    ref: 'T13', title: 'Parked task', stage: 'blocked_or_failed', why_waiting: 'Parked, waiting on the coordinator',
    layer: 1, after: [], dependents: [], rounds: 0,
    steps: [
      { name: 'planned', state: 'done' },
      { name: 'implementing', state: 'now', text: W.parkedText },
      { name: 'review', state: 'todo' },
      { name: 'CI', state: 'todo' },
      { name: 'merged', state: 'todo' },
    ],
  },
  {
    ref: 'T14', title: 'Waiting on plan review', stage: 'queued', why_waiting: 'Waiting on the plan review',
    layer: 0, after: [], dependents: [], rounds: 0,
    steps: [
      { name: 'plan review', state: 'now' },
      { name: 'planned', state: 'todo' },
      { name: 'implementing', state: 'todo' },
      { name: 'review', state: 'todo' },
      { name: 'CI', state: 'todo' },
      { name: 'merged', state: 'todo' },
    ],
  },
];

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
      if (w === undefined ? (a === 'open' ? !el.open : a === 'disabled' ? !el.disabled : a === 'hidden' ? !el.hidden : !el.hasAttribute(a)) : el.getAttribute(a) !== w) return false;
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
    Object.assign(this, { nodeType: 1, tagName: tag.toUpperCase(), ownerDocument: doc, parentElement: null, _nodes: [], _attrs: new Map(), _ls: new Map(), _raw: '', _val: undefined });
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
  get value() { return this._val !== undefined ? this._val : (this._attrs.get('value') || ''); }
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
  click() { if (!this.disabled) this.dispatchEvent({ type: 'click', bubbles: true }); }
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

function fakeDaemon({ tasks = sampleTasks(), asks = [] } = {}) {
  let inFlight = 0;
  const reply = (s, o) => ({ ok: s >= 200 && s < 300, status: s, json: async () => o });
  const fetch = async (path) => {
    inFlight++;
    try {
      await Promise.resolve();
      if (path === '/api/hello') return reply(200, { computer: { id: 'D1', name: 'Test Mac' } });
      if (path === '/api/factory/projects') return reply(200, { protocol: 1, projects: [{ id: 'proj-demo', goal: 'A notes page with search', state: 'running', running: true, counts: { working: 3 } }] });
      if (path === '/api/factory/projects/proj-demo/graph') return reply(200, { protocol: 1, project: 'proj-demo', tasks });
      if (path.startsWith('/api/factory/projects/proj-demo/log')) return reply(200, { protocol: 1, project: 'proj-demo', lines: [{ at: '2026-11-24T13:22:00Z', by: 'engine', task: 'T4', decision: 'started reviewer', why: 'review round 2' }] });
      if (path === '/api/factory/asks') return reply(200, { protocol: 1, asks });
      if (path === '/api/factory/projects/proj-demo/settings') return reply(200, { protocol: 1, rows: [{ name: 'max_running', label: 'max running', value: '3', default: '3', min: 1, max: 20, top: true }] });
      return reply(404, { error: 'not found' });
    } finally { inFlight--; }
  };
  return { fetch, get inFlight() { return inFlight; } };
}

const realSet = globalThis.setTimeout, realClr = globalThis.clearTimeout, realCrypto = globalThis.crypto;
let bootSeq = 0, activeCleanup = null;

async function bootPage(t, daemon, { width = 1200, hash = '', settle = true } = {}) {
  if (activeCleanup) await activeCleanup();
  const doc = createDoc(readFileSync(new URL('./index.html', import.meta.url), 'utf8')), idb = createIDB();
  const keys = ['document', 'window', 'location', 'indexedDB', 'fetch', 'crypto', 'addEventListener', 'removeEventListener', 'scrollTo', 'innerWidth', 'matchMedia'];
  const prev = Object.fromEntries(keys.map((k) => [k, Object.getOwnPropertyDescriptor(globalThis, k)]));
  const pending = new Set(), timers = new Map(), mqls = [], winLs = new Map();
  const loc = { hash, href: `http://127.0.0.1:7777/factory${hash}` };
  let curW = width, seq = 1000, disposed = false;
  const px = (n, u) => +n * (u === 'px' ? 1 : 16);
  const evalMQ = (q) => { const mx = q.match(/max-width:\s*([\d.]+)(px|r?em)/), mn = q.match(/min-width:\s*([\d.]+)(px|r?em)/); return (!mx || curW <= px(mx[1], mx[2])) && (!mn || curW >= px(mn[1], mn[2])); };
  const track = (p) => { if (p?.then) { const m = Promise.resolve(p).then(() => {}, () => {}).finally(() => pending.delete(m)); pending.add(m); } return p; };
  if (settle) doc._track = track;
  const subtle = new Proxy(realCrypto.subtle, { get(o, p) { const v = o[p]; return typeof v === 'function' ? (...a) => track(v.apply(o, a)) : v; } });
  globalThis.setTimeout = (fn, ms, ...a) => disposed ? ++seq : (+ms >= 500 ? (timers.set(++seq, { fn: () => fn(...a), ms: +ms }), seq) : realSet(() => { if (!disposed) fn(...a); }, ms));
  globalThis.clearTimeout = (id) => { timers.delete(id) || realClr(id); };
  const setP = (k, v) => Object.defineProperty(globalThis, k, { value: v, configurable: true, writable: true });
  for (const [k, v] of [['document', doc], ['window', globalThis], ['indexedDB', idb], ['crypto', { subtle, getRandomValues: (a) => realCrypto.getRandomValues(a), randomUUID: () => realCrypto.randomUUID() }], ['scrollTo', () => {}], ['innerWidth', curW], ['location', loc],
    ['fetch', (p, i) => disposed ? Promise.reject(new Error('disposed')) : (settle ? track(daemon.fetch(p, i)) : daemon.fetch(p, i))],
    ['addEventListener', (ev, fn) => (winLs.get(ev) || winLs.set(ev, []).get(ev)).push(fn)], ['removeEventListener', (ev, fn) => { const l = winLs.get(ev); if (l) winLs.set(ev, l.filter((x) => x !== fn)); }]]) setP(k, v);
  setP('matchMedia', (q) => {
    const ls = [], mq = { media: q, onchange: null, get matches() { return evalMQ(q); }, _last: evalMQ(q), addEventListener: (e, fn) => e === 'change' && ls.push(fn), removeEventListener: (e, fn) => { const i = ls.indexOf(fn); if (i >= 0) ls.splice(i, 1); }, addListener: (fn) => ls.push(fn), removeListener: (fn) => { const i = ls.indexOf(fn); if (i >= 0) ls.splice(i, 1); }, _fire() { const now = evalMQ(q); if (now !== mq._last) { mq._last = now; const ev = { matches: now, media: q }; mq.onchange?.(ev); ls.forEach((fn) => fn(ev)); } } };
    mqls.push(mq); return mq;
  });
  const flush = async () => { for (let q = 0; q < 3;) { while (pending.size) await Promise.allSettled([...pending]); await new Promise((r) => setImmediate(r)); q = (pending.size || daemon.inFlight || idb.pending) ? 0 : q + 1; } };
  const cleanup = async () => {
    if (disposed) return;
    disposed = true; timers.clear();
    if (settle) await flush().catch(() => {});
    globalThis.setTimeout = realSet; globalThis.clearTimeout = realClr;
    for (const [k, d] of Object.entries(prev)) d ? Object.defineProperty(globalThis, k, d) : delete globalThis[k];
    if (activeCleanup === cleanup) activeCleanup = null;
  };
  activeCleanup = cleanup; t.after(cleanup);
  await import(`./app.js?boot=${++bootSeq}`);
  await flush();
  return {
    doc, flush,
    async navigate(nextHash) {
      loc.hash = nextHash; loc.href = `http://127.0.0.1:7777/factory${nextHash}`;
      for (const fn of winLs.get('hashchange') || []) track(fn({ type: 'hashchange' }));
      await flush();
    },
  };
}

const findCard = (doc, h) => doc.querySelectorAll('section').find((s) => s.querySelector('h2')?.textContent.trim().toLowerCase() === h);
const taskRow = (doc, ref) => doc.querySelectorAll('#main a.row').find((a) => a.getAttribute('href') === `#proj-demo/${ref}`);

test('board rows render compact step bar with segments, current-step words, aria-labels, and no review-rounds chip', async (t) => {
  const page = await bootPage(t, fakeDaemon());

  const checks = [
    { ref: 'T7', segs: 5, stepOf: 'step 1 of 5', words: 'planned', states: ['now', 'todo', 'todo', 'todo', 'todo'] },
    { ref: 'T4', segs: 5, stepOf: 'step 3 of 5', words: W.reviewNow, states: ['done', 'done', 'now', 'todo', 'todo'] },
    { ref: 'T5', segs: 5, stepOf: 'step 2 of 5', words: W.barBack, states: ['done', 'now', 'todo', 'todo', 'todo'] },
    { ref: 'T9', segs: 5, stepOf: 'step 4 of 5', words: W.barSkip, states: ['done', 'done', 'skip', 'now', 'todo'] },
    { ref: 'T2', segs: 5, stepOf: 'step 5 of 5', words: 'merged', states: ['done', 'done', 'done', 'done', 'done'] },
    { ref: 'T10', segs: 3, stepOf: 'step 3 of 3', words: W.barDropped, states: ['done', 'done', 'end'] },
    { ref: 'T11', segs: 6, stepOf: 'step 4 of 6', words: 'retro', states: ['done', 'done', 'done', 'now', 'todo', 'todo'] },
    { ref: 'T14', segs: 6, stepOf: 'step 1 of 6', words: 'plan review', states: ['now', 'todo', 'todo', 'todo', 'todo', 'todo'] },
  ];

  for (const c of checks) {
    const row = taskRow(page.doc, c.ref);
    assert.ok(row, `expected board row for ${c.ref}`);
    const bar = row.querySelector('[role="img"]');
    assert.ok(bar, `${c.ref}: board row must render a compact step bar with role="img"`);
    assert.equal(bar.children.length, c.segs, `${c.ref}: expected ${c.segs} bar segments`);
    for (let i = 0; i < c.states.length; i++) {
      const seg = bar.children[i], cls = `${seg.className} ${seg.getAttribute('data-state') || ''}`;
      if (c.states[i] === 'todo') assert.ok(seg.className === '' || /todo/.test(cls), `${c.ref} seg ${i}: expected todo styling, got "${cls}"`);
      else assert.match(cls, new RegExp(c.states[i]), `${c.ref} seg ${i}: expected ${c.states[i]} class/state, got "${cls}"`);
    }
    const barAria = bar.getAttribute('aria-label') || '';
    assert.ok(barAria.includes(c.stepOf), `${c.ref}: bar aria-label "${barAria}" must include "${c.stepOf}"`);
    assert.ok(barAria.includes(c.words), `${c.ref}: bar aria-label "${barAria}" must include "${c.words}"`);
    assert.ok(row.textContent.includes(c.words), `${c.ref}: current step words "${c.words}" must be visible beside the bar`);
    const rowAria = row.getAttribute('aria-label') || '';
    assert.ok(rowAria.includes(c.stepOf) && rowAria.includes(c.words), `${c.ref}: row aria-label "${rowAria}" must read current step`);
    assert.doesNotMatch(row.textContent, /round\s+\d+\s+of\s+\d+/i, `${c.ref}: review text must never read as 'round N of M'`);
    const chipsText = row.querySelector('.chips')?.textContent || '';
    assert.doesNotMatch(chipsText, /\d+\s+rounds?\s+done/, `${c.ref}: review-rounds chip must be removed from board row (.chips="${chipsText}")`);
  }
});

test('task view renders named steps ordered list first with state words, icons, step text, and non-focusable items', async (t) => {
  const page = await bootPage(t, fakeDaemon(), { hash: '#proj-demo/T4' });
  const main = page.doc.getElementById('main');
  const sections = main.querySelectorAll('section');
  const stepsCard = findCard(page.doc, W.stepsH);
  const briefCard = findCard(page.doc, 'brief');
  const verdictCard = sections.find((s) => /the ladder says/i.test(s.querySelector('h2')?.textContent || ''));

  assert.ok(stepsCard, 'task view must render a "steps" card');
  assert.ok(briefCard && verdictCard, 'task view must still render brief and verdict cards');
  assert.ok(sections.indexOf(stepsCard) < sections.indexOf(briefCard) && sections.indexOf(stepsCard) < sections.indexOf(verdictCard), 'steps card must render before brief and verdict');

  const ol = stepsCard.querySelector('ol');
  assert.ok(ol, 'steps card must render an ordered list <ol>');
  assert.notEqual(ol.getAttribute('tabindex'), '0', 'steps <ol> must not be focusable');
  const items = ol.children.filter((el) => el.tagName === 'LI');
  assert.equal(items.length, 5, 'T4 must render 5 named steps');

  const want = [
    { name: 'planned', stateWord: W.done },
    { name: 'implementing', stateWord: W.done },
    { name: 'review', stateWord: W.now, text: W.reviewNow },
    { name: 'CI', stateWord: W.todo },
    { name: 'merged', stateWord: W.todo },
  ];
  for (let i = 0; i < want.length; i++) {
    const li = items[i];
    assert.notEqual(li.getAttribute('tabindex'), '0', `step ${i} <li> must not be focusable`);
    assert.ok(li.querySelector('[aria-hidden="true"]'), `step ${i} (${want[i].name}) must render a visual mark/icon beside text`);
    assert.ok(li.textContent.includes(want[i].name), `step ${i} must name "${want[i].name}"`);
    assert.ok(li.textContent.includes(want[i].stateWord), `step ${i} (${want[i].name}) must include state word "${want[i].stateWord}"`);
    if (want[i].text) assert.ok(li.textContent.includes(want[i].text), `step ${i} must include "${want[i].text}"`);
  }
});

test('task view renders sent-back, retro, override-skipped, merged, dropped, closed, parked, and plan-review paths', async (t) => {
  const page = await bootPage(t, fakeDaemon(), { hash: '#proj-demo/T5' });

  const expectView = async (ref, expected) => {
    await page.navigate(`#proj-demo/${ref}`);
    const card = findCard(page.doc, W.stepsH);
    assert.ok(card, `${ref}: expected steps card`);
    const items = card.querySelector('ol')?.children.filter((el) => el.tagName === 'LI') || [];
    assert.equal(items.length, expected.length, `${ref}: expected ${expected.length} steps, got ${items.length}`);
    for (let i = 0; i < expected.length; i++) {
      for (const s of expected[i]) {
        assert.ok(items[i].textContent.includes(s), `${ref} step ${i}: expected "${s}" in "${items[i].textContent}"`);
      }
    }
  };

  await expectView('T5', [
    ['planned', W.done],
    ['implementing', W.now, W.sentBack],
    ['review', W.todo, W.reviewNeeds],
    ['CI', W.todo],
    ['merged', W.todo],
  ]);
  await expectView('T11', [
    ['planned', W.done],
    ['implementing', W.done],
    ['review', W.done, W.reviewThree],
    ['retro', W.now, W.retroText],
    ['CI', W.todo],
    ['merged', W.todo],
  ]);
  await expectView('T9', [
    ['planned', W.done],
    ['implementing', W.done],
    ['review', W.skipped, W.skipText],
    ['CI', W.now, W.overrideWhy],
    ['merged', W.todo],
  ]);
  await expectView('T2', [
    ['planned', W.done],
    ['implementing', W.done],
    ['review', W.done, W.reviewTwo],
    ['CI', W.done],
    ['merged', W.done, 'PR #7'],
  ]);
  await expectView('T10', [
    ['planned', W.done],
    ['implementing', W.done],
    ['dropped', W.droppedText],
  ]);
  await expectView('T12', [
    ['planned', W.done],
    ['implementing', W.done],
    ['review', W.done, W.reviewOne],
    ['closed', W.closedText],
  ]);
  await expectView('T13', [
    ['planned', W.done],
    ['implementing', W.now, W.parkedText],
    ['review', W.todo],
    ['CI', W.todo],
    ['merged', W.todo],
  ]);
  await expectView('T14', [
    ['plan review', W.now],
    ['planned', W.todo],
    ['implementing', W.todo],
    ['review', W.todo],
    ['CI', W.todo],
    ['merged', W.todo],
  ]);
});

test('task view puts rule id inside a closed details fold and keeps rule ids off the visible card', async (t) => {
  const page = await bootPage(t, fakeDaemon(), { hash: '#proj-demo/T4' });
  const main = page.doc.getElementById('main');
  const verdict = main.querySelectorAll('section').find((s) => /the ladder says/i.test(s.querySelector('h2')?.textContent || ''));
  assert.ok(verdict, 'expected Verdict card in task view');
  const fold = verdict.querySelector('details');
  assert.ok(fold, 'Verdict card must hold a <details> fold for rule id');
  assert.equal(fold.open, false, 'details fold must start closed');
  assert.equal(fold.querySelector('summary')?.textContent.trim(), W.details);
  assert.ok(fold.textContent.includes('rule N22'), 'details fold must contain "rule N22"');

  const outsideText = (el) => {
    if (el.nodeType === 3) return el.textContent;
    if (el.tagName === 'DETAILS') return '';
    return el.childNodes.map(outsideText).join('');
  };
  assert.doesNotMatch(outsideText(main), /\brule\s+N\d+\b|\bN22\b/, 'no rule id may show outside a <details> fold');
});

test('step rendering escapes HTML, tolerates missing steps, works at 360 px, uses token CSS with non-colour cues, and keeps app.js POST-free', async (t) => {
  const xssTask = {
    ref: 'TX', title: 'Escape test', stage: 'working', why_waiting: 'In review',
    layer: 1, after: [], dependents: [], rounds: 1,
    steps: [
      { name: 'planned', state: 'done' },
      { name: '<img src=x onerror=alert(1)>', state: 'now', text: '<script>alert(2)</script>' },
    ],
  };
  const emptyStepsTask = {
    ref: 'TE', title: 'Legacy task without steps', stage: 'working', why_waiting: 'Working',
    layer: 1, after: [], dependents: [], rounds: 0,
  };
  const page = await bootPage(t, fakeDaemon({ tasks: [xssTask, emptyStepsTask] }), { width: 360 });
  const main = page.doc.getElementById('main');
  assert.ok(taskRow(page.doc, 'TX')?.querySelector('[role="img"]'), '360 px narrow board must still render compact step bar');
  assert.ok(taskRow(page.doc, 'TE'), 'task with missing steps must still render without throwing');
  assert.ok(!main.innerHTML.includes('<script>alert(2)</script>') && !main.innerHTML.includes('<img src=x onerror=alert(1)>'), 'board row must escape step HTML');

  await page.navigate('#proj-demo/TX');
  assert.ok(findCard(page.doc, W.stepsH), 'task view renders steps card for TX');
  assert.ok(!main.innerHTML.includes('<script>alert(2)</script>') && !main.innerHTML.includes('<img src=x onerror=alert(1)>'), 'task view must escape step HTML');

  await page.navigate('#proj-demo/TE');
  assert.ok(main.querySelector('h1')?.textContent.includes('Legacy task without steps'), 'task view must tolerate missing steps without throwing');

  const css = readFileSync(new URL('./style.css', import.meta.url), 'utf8');
  assert.doesNotMatch(css, /#[0-9a-fA-F]{3,8}\b/, 'style.css must use design tokens from tokens.css, never hardcoded hex colours');
  assert.match(css, /\.(?:bar|step-bar|steps-bar|sbar)\b/, 'style.css must style the compact step bar');
  assert.match(css, /\.steps?\b/, 'style.css must style the task view steps list');
  assert.match(css, /skip[^{]*\{[^}]*dashed/i, 'skipped step style must use a dashed border (not colour alone)');
  assert.match(css, /now[^{]*\{[^}]*(?:outline|height|border)/i, 'current ("now") step style must use a non-colour visual cue');

  const appSrc = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
  assert.ok(!/\bPOST\b|method\s*:/.test(appSrc), 'app.js must hold no POST');
});

test('style.css keeps todo step-bar segments visually distinct from hovered board rows', () => {
  const css = readFileSync(new URL('./style.css', import.meta.url), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({
    sels: m[1].split(',').map((s) => s.trim().replace(/\s+/g, ' ')),
    body: m[2],
  }));
  const lastProp = (list, names) => {
    let out = '';
    for (const r of list) {
      for (const n of names) {
        for (const m of r.body.matchAll(new RegExp(`(?:^|;)\\s*${n}\\s*:\\s*([^;]+)`, 'gi'))) {
          out = m[1].trim().replace(/\s+/g, ' ');
        }
      }
    }
    return out;
  };
  const rowHoverRules = rules.filter((r) => r.sels.includes('.row:hover'));
  const barSpanRules = rules.filter((r) => r.sels.includes('.bar span'));
  assert.ok(rowHoverRules.length > 0, 'expected .row:hover rule in style.css');
  assert.ok(barSpanRules.length > 0, 'expected .bar span rule in style.css');

  const hoverBg = lastProp(rowHoverRules, ['background', 'background-color']);
  const todoBg = lastProp(barSpanRules, ['background', 'background-color']);
  const todoEdge = lastProp(barSpanRules, ['border', 'outline']);
  const hoverBarRules = rules.filter((r) => r.sels.some((s) => /^\.row:hover\b.*\.bar\s+span\b/.test(s)));
  const hasHoverBarOverride = Boolean(lastProp(hoverBarRules, ['background', 'background-color', 'border', 'outline']));

  assert.ok(
    (hoverBg && todoBg && hoverBg !== todoBg) || Boolean(todoEdge) || hasHoverBarOverride,
    `todo .bar span (${todoBg || 'none'}) blends into .row:hover (${hoverBg || 'none'}) with no border/outline or hover override`,
  );
});
