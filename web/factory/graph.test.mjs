// Unit and page tests for PR 14.9: /factory task graph (`web/factory/graph.js`,
// list/graph switch in `app.js`, stage tokens and scroll box in `style.css`, `make test-web`).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const W = {
  group: 'show tasks as',
  list: 'list',
  graph: 'graph',
  scroll: 'The graph is wider than the window. Scroll it sideways, or switch to the list.',
};

const RAW_STAGES = ['owner_gate', 'owner_answer', 'blocked_or_failed', 'waiting_on_dep'];

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
    else if (r[0] === '[' && (m = r.match(/^\[([\w:-]+)(?:(\^?=)(?:"([^"]*)"|'([^']*)'|([^\]]*)))?\]/))) {
      const a = m[1].toLowerCase(), op = m[2], w = m[3] ?? m[4] ?? m[5];
      if (w === undefined ? (a === 'open' ? !el.open : a === 'disabled' ? !el.disabled : a === 'hidden' ? !el.hidden : !el.hasAttribute(a)) : op === '^=' ? !(el.getAttribute(a) || '').startsWith(w) : el.getAttribute(a) !== w) return false;
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
  get clientWidth() {
    if (this._clientWidth !== undefined) return this._clientWidth;
    if (this.tagName === 'SVG') return Number(this.getAttribute('width')) || 0;
    return this.ownerDocument?._viewportWidth || 1200;
  }
  set clientWidth(v) { this._clientWidth = v; }
  get scrollWidth() {
    if (this._scrollWidth !== undefined) return this._scrollWidth;
    const svgs = this.querySelectorAll('svg').map((s) => Number(s.getAttribute('width')) || 0);
    return Math.max(this.clientWidth, ...svgs);
  }
  set scrollWidth(v) { this._scrollWidth = v; }
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
    const bubble = e.bubbles !== false;
    for (let c = this; c && !e._s; c = bubble ? c.parentElement : null) {
      e.currentTarget = c;
      for (const fn of c._ls.get(e.type) || []) this.ownerDocument?._track?.(fn(e));
    }
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

function createDoc(html, viewportWidth = 1200) {
  const doc = {
    activeElement: null, root: null, _viewportWidth: viewportWidth,
    getElementById(id) { return this.root.querySelectorAll('*').find((e) => e.id === id) || null; },
    querySelector(s) { return this.root.querySelector(s); },
    querySelectorAll(s) { return this.root.querySelectorAll(s); },
    createElement(t) { return new El(t, this); },
  };
  doc.root = new El('document', doc);
  doc.root._nodes = parseHTML(html, doc, null);
  return doc;
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

function createStorage() {
  const m = new Map();
  return {
    getItem: (k) => (m.has(String(k)) ? m.get(String(k)) : null),
    setItem: (k, v) => { m.set(String(k), String(v)); },
    removeItem: (k) => { m.delete(String(k)); },
    clear: () => { m.clear(); },
  };
}

function makeTask(ref, title, stage, layer, after = [], dependents = []) {
  return { ref, title, stage, layer, after, dependents };
}

function fakeDaemon({ tasks = [], asks = [] } = {}) {
  let inFlight = 0;
  const reply = (s, o) => ({ ok: s >= 200 && s < 300, status: s, json: async () => o });
  const fetch = async (path) => {
    inFlight++;
    try {
      await Promise.resolve();
      if (path === '/api/hello') return reply(200, { computer: { id: 'D1', name: 'Test Mac' } });
      if (path === '/api/factory/projects') return reply(200, { protocol: 1, projects: [{ id: 'proj-demo', goal: 'A notes page with search', running: true, counts: { working: 1 } }] });
      if (path === '/api/factory/projects/proj-demo/graph') return reply(200, { protocol: 1, project: 'proj-demo', tasks });
      if (path.startsWith('/api/factory/projects/proj-demo/log')) return reply(200, { protocol: 1, project: 'proj-demo', lines: [] });
      if (path === '/api/factory/projects/proj-demo/settings') return reply(200, { protocol: 1, rows: [] });
      if (path === '/api/factory/asks') return reply(200, { protocol: 1, asks });
      return reply(404, { error: 'not found' });
    } finally { inFlight--; }
  };
  return { fetch, get inFlight() { return inFlight; } };
}

const realSet = globalThis.setTimeout, realClr = globalThis.clearTimeout, realCrypto = globalThis.crypto;
let bootSeq = 0, activeCleanup = null;

async function bootPage(t, daemon, { width = 1280 } = {}) {
  if (activeCleanup) await activeCleanup();
  const doc = createDoc(readFileSync(new URL('./index.html', import.meta.url), 'utf8'), width), idb = createIDB();
  const keys = ['document', 'window', 'location', 'indexedDB', 'fetch', 'crypto', 'addEventListener', 'removeEventListener', 'scrollTo', 'innerWidth', 'matchMedia', 'sessionStorage', 'ResizeObserver'];
  const prev = Object.fromEntries(keys.map((k) => [k, Object.getOwnPropertyDescriptor(globalThis, k)]));
  const pending = new Set(), timers = new Map(), mqls = [], winLs = new Map();
  let curW = width, seq = 1000, disposed = false;
  const px = (n, u) => +n * (u === 'px' ? 1 : 16);
  const evalMQ = (q) => { const mx = q.match(/max-width:\s*([\d.]+)(px|r?em)/), mn = q.match(/min-width:\s*([\d.]+)(px|r?em)/); return (!mx || curW <= px(mx[1], mx[2])) && (!mn || curW >= px(mn[1], mn[2])); };
  const track = (p) => { if (p?.then) { const m = Promise.resolve(p).then(() => {}, () => {}).finally(() => pending.delete(m)); pending.add(m); } return p; };
  doc._track = track;
  const subtle = new Proxy(realCrypto.subtle, { get(o, p) { const v = o[p]; return typeof v === 'function' ? (...a) => track(v.apply(o, a)) : v; } });
  globalThis.setTimeout = (fn, ms, ...a) => disposed ? ++seq : (+ms >= 500 ? (timers.set(++seq, { fn: () => fn(...a), ms: +ms }), seq) : realSet(() => { if (!disposed) fn(...a); }, ms));
  globalThis.clearTimeout = (id) => { timers.delete(id) || realClr(id); };
  const setP = (k, v) => Object.defineProperty(globalThis, k, { value: v, configurable: true, writable: true });
  class StubRO { constructor(cb) { this.cb = cb; } observe() {} unobserve() {} disconnect() {} }
  for (const [k, v] of [
    ['document', doc], ['window', globalThis], ['indexedDB', idb], ['sessionStorage', createStorage()], ['ResizeObserver', StubRO],
    ['crypto', { subtle, getRandomValues: (a) => realCrypto.getRandomValues(a), randomUUID: () => realCrypto.randomUUID() }],
    ['scrollTo', () => {}], ['innerWidth', curW], ['location', { hash: '', href: 'http://127.0.0.1:7777/factory' }],
    ['fetch', (p, i) => disposed ? Promise.reject(new Error('disposed')) : track(daemon.fetch(p, i))],
    ['addEventListener', (ev, fn) => (winLs.get(ev) || winLs.set(ev, []).get(ev)).push(fn)],
    ['removeEventListener', (ev, fn) => { const l = winLs.get(ev); if (l) winLs.set(ev, l.filter((x) => x !== fn)); }],
  ]) setP(k, v);
  setP('matchMedia', (q) => {
    const ls = [], mq = { media: q, onchange: null, get matches() { return evalMQ(q); }, _last: evalMQ(q), addEventListener: (e, fn) => e === 'change' && ls.push(fn), removeEventListener: (e, fn) => { const i = ls.indexOf(fn); if (i >= 0) ls.splice(i, 1); }, addListener: (fn) => ls.push(fn), removeListener: (fn) => { const i = ls.indexOf(fn); if (i >= 0) ls.splice(i, 1); }, _fire() { const now = evalMQ(q); if (now !== mq._last) { mq._last = now; const ev = { matches: now, media: q }; mq.onchange?.(ev); ls.forEach((fn) => fn(ev)); } } };
    mqls.push(mq); return mq;
  });
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
    async setWidth(w) { curW = w; doc._viewportWidth = w; setP('innerWidth', w); mqls.forEach((m) => m._fire()); (winLs.get('resize') || []).forEach((fn) => fn({ type: 'resize' })); await flush(); },
  };
}

// Layout geometry helpers tolerant of either { w, h } or { width, height } and ref or task.ref.
const loadGraph = () => import('./graph.js');
const nRef = (n) => n.ref ?? n.task?.ref;
const nW = (n) => n.w ?? n.width;
const nH = (n) => n.h ?? n.height;
const eFrom = (e) => (typeof e.from === 'string' ? e.from : nRef(e.from));
const eTo = (e) => (typeof e.to === 'string' ? e.to : nRef(e.to));

function assertNoOverlap(nodes) {
  for (let i = 0; i < nodes.length; i++) {
    const a = nodes[i], aw = nW(a), ah = nH(a);
    assert.ok(Number.isFinite(a.x) && Number.isFinite(a.y) && aw > 0 && ah > 0, `node ${nRef(a)} must have positive finite box geometry`);
    for (let j = i + 1; j < nodes.length; j++) {
      const b = nodes[j], bw = nW(b), bh = nH(b);
      const sep = a.x + aw <= b.x || b.x + bw <= a.x || a.y + ah <= b.y || b.y + bh <= a.y;
      assert.ok(sep, `nodes ${nRef(a)} and ${nRef(b)} overlap: (${a.x},${a.y},${aw}x${ah}) vs (${b.x},${b.y},${bw}x${bh})`);
    }
  }
}

function assertUniformBoxes(nodes) {
  if (nodes.length < 2) return;
  const w0 = nW(nodes[0]), h0 = nH(nodes[0]);
  for (const n of nodes) {
    assert.equal(nW(n), w0, `node ${nRef(n)} width must match uniform node width ${w0}`);
    assert.equal(nH(n), h0, `node ${nRef(n)} height must match uniform node height ${h0}`);
  }
}

function assertEdgesDownward(nodes, edges) {
  const byRef = new Map(nodes.map((n) => [nRef(n), n]));
  for (const e of edges) {
    const from = byRef.get(eFrom(e)), to = byRef.get(eTo(e));
    assert.ok(from && to, `edge ${eFrom(e)} -> ${eTo(e)} must reference laid-out nodes`);
    assert.ok(from.y + nH(from) <= to.y, `edge ${eFrom(e)} -> ${eTo(e)} must run strictly downward (${from.y}+${nH(from)} <= ${to.y})`);
  }
}

// Convert a flow.State fixture (testdata/plan-20.state.json) into GraphView-shaped tasks.
function tasksFromStateFixture(raw) {
  const list = raw.tasks || [];
  const byId = new Map(list.map((t) => [t.ref || t.id, t]));
  const layerMemo = new Map();
  const layerOf = (id, seen = new Set()) => {
    if (layerMemo.has(id)) return layerMemo.get(id);
    if (seen.has(id)) return 1;
    seen.add(id);
    const t = byId.get(id);
    if (!t) return 1;
    if (typeof t.layer === 'number' && t.layer > 0) { layerMemo.set(id, t.layer); return t.layer; }
    const deps = (t.after || []).filter((d) => byId.has(d));
    const l = deps.length ? 1 + Math.max(...deps.map((d) => layerOf(d, new Set(seen)))) : 1;
    layerMemo.set(id, l);
    return l;
  };
  const depsOf = new Map(list.map((t) => [t.ref || t.id, []]));
  for (const t of list) for (const d of t.after || []) depsOf.get(d)?.push(t.ref || t.id);
  return list.map((t) => {
    const ref = t.ref || t.id;
    const stage = t.stage || (t.done ? 'done' : (t.after?.length ? 'waiting_on_dep' : 'queued'));
    return makeTask(ref, t.title || ref, stage, layerOf(ref), t.after || [], depsOf.get(ref) || []);
  });
}

// --- Pure layout() tests (direct import from ./graph.js) ---

test('layout on a chain places tasks in descending rows with downward edges and no overlap', async () => {
  const { layout } = await loadGraph();
  const tasks = [
    makeTask('T1', 'Notes table', 'done', 1, [], ['T2']),
    makeTask('T2', 'Notes API', 'done', 2, ['T1'], ['T3']),
    makeTask('T3', 'Search box', 'working', 3, ['T2'], ['T4']),
    makeTask('T4', 'Export notes', 'waiting_on_dep', 4, ['T3'], []),
  ];
  const g = layout(tasks);
  assert.equal(g.nodes.length, 4);
  assert.equal(g.edges.length, 3);
  assert.ok(g.width > 0 && g.height > 0, 'layout must return positive width and height');
  assertNoOverlap(g.nodes);
  assertUniformBoxes(g.nodes);
  assertEdgesDownward(g.nodes, g.edges);
  assert.equal(new Set(g.nodes.map((n) => n.y)).size, 4, 'a 4-task chain must occupy 4 distinct rows');
});

test('layout on a diamond places split and join with no overlap and downward edges', async () => {
  const { layout } = await loadGraph();
  const tasks = [
    makeTask('T1', 'Notes table', 'done', 1, [], ['T2', 'T3']),
    makeTask('T2', 'Notes API', 'working', 2, ['T1'], ['T4']),
    makeTask('T3', 'Search index', 'working', 2, ['T1'], ['T4']),
    makeTask('T4', 'Search box', 'waiting_on_dep', 3, ['T2', 'T3'], []),
  ];
  const g = layout(tasks);
  assert.equal(g.nodes.length, 4);
  assert.equal(g.edges.length, 4);
  assertNoOverlap(g.nodes);
  assertUniformBoxes(g.nodes);
  assertEdgesDownward(g.nodes, g.edges);
  const byRef = new Map(g.nodes.map((n) => [nRef(n), n]));
  assert.equal(byRef.get('T2').y, byRef.get('T3').y, 'same-layer tasks must share one row');
  assert.ok(byRef.get('T1').y < byRef.get('T2').y && byRef.get('T2').y < byRef.get('T4').y, 'layers must run top to bottom');
});

test('layout on a fan of 8 keeps uniform node size, avoids overlap, and exceeds 1280 px width', async () => {
  const { layout } = await loadGraph();
  const mid = Array.from({ length: 8 }, (_, i) => makeTask(`U${i + 1}`, `Feature ${i + 1}`, i < 3 ? 'working' : 'queued', 2, ['U0'], ['U9']));
  const tasks = [
    makeTask('U0', 'User table and model', 'done', 1, [], mid.map((t) => t.ref)),
    ...mid,
    makeTask('U9', 'Verify the milestone', 'waiting_on_dep', 3, mid.map((t) => t.ref), []),
  ];
  const g = layout(tasks);
  assert.equal(g.nodes.length, 10);
  assert.equal(g.edges.length, 16);
  assertNoOverlap(g.nodes);
  assertUniformBoxes(g.nodes);
  assertEdgesDownward(g.nodes, g.edges);
  assert.equal(new Set(g.nodes.map((n) => n.y)).size, 3, 'fan of 8 must span 3 rows');
  assert.ok(nW(g.nodes[0]) >= 140, 'nodes must keep a readable fixed width rather than shrinking to fit');
  assert.ok(g.width > 1280, `fan of 8 must exceed 1280 px so it scrolls in its own box (got ${g.width})`);
});

test('layout and renderGraphSVG on the 20-task fixture (testdata/plan-20.state.json) have 5 layers, no overlap, downward edges, and boxes wide enough for text', async () => {
  const { layout, renderGraphSVG } = await loadGraph();
  const raw = JSON.parse(readFileSync(new URL('./testdata/plan-20.state.json', import.meta.url), 'utf8'));
  const tasks = tasksFromStateFixture(raw);
  assert.equal(tasks.length, 20, 'plan-20.state.json must contain 20 tasks');
  assert.equal(new Set(tasks.map((t) => t.layer)).size, 5, 'plan-20.state.json must span 5 layers');

  const g = layout(tasks);
  assert.equal(g.nodes.length, 20);
  assert.equal(new Set(g.nodes.map((n) => n.y)).size, 5, '20-task fixture must lay out into 5 rows');
  assertNoOverlap(g.nodes);
  assertUniformBoxes(g.nodes);
  assertEdgesDownward(g.nodes, g.edges);
  assert.ok(g.width <= 1280, `20-task fixture should fit a 1280 px window without sideways scroll (got ${g.width})`);

  const doc = createDoc(renderGraphSVG(tasks, 'proj-demo'));
  const svg = doc.querySelector('svg');
  assert.ok(svg, 'renderGraphSVG must output an <svg>');
  const links = svg.querySelectorAll('a');
  assert.equal(links.length, 20, 'every task in the 20-task fixture must render as a link node');
  for (const a of links) {
    const rect = a.querySelector('rect');
    assert.ok(rect, 'each node must contain a <rect> box');
    const boxW = Number(rect.getAttribute('width'));
    assert.ok(boxW > 0, 'node rect must have a positive width');
    for (const txt of a.querySelectorAll('text')) {
      // Conservative floor at 12 px font size (~6 px/char): box must never be narrower than its drawn text.
      const drawnLen = txt.textContent.trim().length;
      assert.ok(drawnLen * 6 <= boxW, `node text "${txt.textContent}" (${drawnLen} chars) is wider than box width ${boxW}`);
    }
  }
});

test('layout orders tasks within a row by the average position of their after tasks (one-pass barycenter)', async () => {
  const { layout } = await loadGraph();
  const tasks = [
    makeTask('T1', 'Left root', 'done', 1, [], ['T4']),
    makeTask('T2', 'Right root', 'done', 1, [], ['T3']),
    // Supplied in reverse order relative to parents so one-pass ordering must swap them to avoid crossing.
    makeTask('T3', 'Depends on right', 'queued', 2, ['T2'], []),
    makeTask('T4', 'Depends on left', 'queued', 2, ['T1'], []),
  ];
  const g = layout(tasks);
  assertNoOverlap(g.nodes);
  assertEdgesDownward(g.nodes, g.edges);
  const byRef = new Map(g.nodes.map((n) => [nRef(n), n]));
  const leftRoot = byRef.get('T1').x < byRef.get('T2').x ? 'T1' : 'T2';
  const childOfLeft = leftRoot === 'T1' ? 'T4' : 'T3';
  const childOfRight = leftRoot === 'T1' ? 'T3' : 'T4';
  assert.ok(byRef.get(childOfLeft).x < byRef.get(childOfRight).x, 'child of left parent must be placed left of child of right parent');
});

test('layout and renderGraphSVG handle an isolated task with no after and none after it', async () => {
  const { layout, renderGraphSVG } = await loadGraph();
  const tasks = [
    makeTask('T1', 'Base schema', 'done', 1, [], ['T2']),
    makeTask('T2', 'List view', 'working', 2, ['T1'], []),
    makeTask('T3', 'Standalone docs', 'queued', 1, [], []),
  ];
  const g = layout(tasks);
  assert.equal(g.nodes.length, 3);
  assert.equal(g.edges.length, 1);
  assertNoOverlap(g.nodes);
  assertEdgesDownward(g.nodes, g.edges);

  const doc = createDoc(renderGraphSVG(tasks, 'proj-demo'));
  const t3 = doc.querySelectorAll('svg a').find((a) => (a.getAttribute('aria-label') || '').startsWith('T3,'));
  assert.ok(t3, 'isolated task T3 must render as a node link');
  assert.equal(t3.getAttribute('aria-label'), 'T3, Standalone docs, queued');
});

test('layout and renderGraphSVG draw all nodes and skip edges for a cycle, self-loop, or unknown after without looping', async () => {
  const { layout, renderGraphSVG } = await loadGraph();
  const cyclic = [
    makeTask('C1', 'First cycle node', 'working', 1, ['C2'], ['C2']),
    makeTask('C2', 'Second cycle node', 'working', 1, ['C1'], ['C1']),
    makeTask('C3', 'Self loop node', 'queued', 2, ['C3'], ['C3']),
  ];
  const gCycle = layout(cyclic);
  assert.equal(gCycle.nodes.length, 3, 'cycle nodes must all be drawn');
  assertNoOverlap(gCycle.nodes);
  assertEdgesDownward(gCycle.nodes, gCycle.edges);
  assert.equal(gCycle.edges.length, 0, 'same-layer cycle and self-loop edges must be skipped');

  const unknownDep = [
    makeTask('T1', 'Known parent', 'done', 1, [], ['T2']),
    makeTask('T2', 'Has unknown dep', 'working', 2, ['T1', 'T999'], []),
    makeTask('T3', 'Upward ref', 'queued', 1, ['T2'], []),
  ];
  const gUnk = layout(unknownDep);
  assert.equal(gUnk.nodes.length, 3, 'all nodes must be drawn when an after ref is unknown or inverted');
  assertNoOverlap(gUnk.nodes);
  assertEdgesDownward(gUnk.nodes, gUnk.edges);
  assert.equal(gUnk.edges.length, 1, 'only the valid downward edge T1 -> T2 should remain');
  assert.equal(eFrom(gUnk.edges[0]), 'T1');
  assert.equal(eTo(gUnk.edges[0]), 'T2');

  const svgDoc = createDoc(renderGraphSVG(unknownDep, 'proj-demo'));
  assert.equal(svgDoc.querySelectorAll('svg a').length, 3, 'renderGraphSVG must still render all nodes');
});

test('renderGraphSVG gives complete, unique node labels, task links, stage words (no jargon), truncated long titles, and escapes HTML', async () => {
  const { renderGraphSVG } = await loadGraph();
  const longTitle = 'A very long task title that definitely cannot fit inside a single fixed-width graph node box';
  const tasks = [
    makeTask('T1', 'Notes table', 'done', 1, [], ['T4']),
    makeTask('T2', 'Notes API', 'ci', 1, [], ['T4']),
    makeTask('T3', 'Page shell <script>alert(1)</script>', 'owner_answer', 1, [], ['T4']),
    makeTask('T4', longTitle, 'waiting_on_dep', 2, ['T1', 'T2', 'T3'], ['T5']),
    makeTask('T5', 'Approve deploy', 'owner_gate', 3, ['T4'], ['T6']),
    makeTask('T6', 'Blocked step', 'blocked_or_failed', 4, ['T5'], []),
  ];
  const html = renderGraphSVG(tasks, 'proj-demo');
  assert.ok(!html.includes('<script>alert(1)</script>'), 'raw HTML in task title must be escaped');
  assert.ok(html.includes('&lt;script&gt;'), 'escaped HTML entities must appear');

  const doc = createDoc(html);
  const svg = doc.querySelector('svg');
  assert.ok(svg, 'renderGraphSVG must return an <svg>');
  const nodes = svg.querySelectorAll('a');
  assert.equal(nodes.length, tasks.length);

  const labels = nodes.map((a) => a.getAttribute('aria-label') || '');
  assert.equal(new Set(labels).size, tasks.length, 'every node aria-label must be unique');
  assert.equal(labels[0], 'T1, Notes table, done');
  assert.equal(labels[1], 'T2, Notes API, waiting on CI');
  assert.equal(labels[2], 'T3, Page shell <script>alert(1)</script>, waiting on you');
  assert.equal(labels[3], `T4, ${longTitle}, waiting on others, after T1 and T2 and T3`);
  assert.equal(labels[4], 'T5, Approve deploy, needs your approval, after T4');
  assert.equal(labels[5], 'T6, Blocked step, blocked, after T5');

  for (let i = 0; i < tasks.length; i++) {
    const a = nodes[i], t = tasks[i];
    assert.equal(a.getAttribute('href'), `#proj-demo/${encodeURIComponent(t.ref)}`, `node ${t.ref} must link to its task view`);
    const rect = a.querySelector('rect');
    assert.ok(rect, `node ${t.ref} must have a <rect>`);
    const boxW = Number(rect.getAttribute('width'));
    const texts = a.querySelectorAll('text').map((x) => x.textContent.trim());
    assert.ok(texts.some((x) => x === t.ref), `node ${t.ref} must display its ref`);
    for (const txt of texts) assert.ok(txt.length * 6 <= boxW, `node ${t.ref} text "${txt}" exceeds box width ${boxW}`);
    // Stage must not be communicated by colour alone: both a visual mark and stage word are present.
    assert.ok(a.querySelector('circle, path'), `node ${t.ref} must draw a stage mark`);
  }
  const t4Texts = nodes[3].querySelectorAll('text').map((x) => x.textContent.trim());
  assert.ok(t4Texts.some((x) => x.includes('…') && x.length < longTitle.length), 'long title must be cut to fit with ellipsis');

  for (const rawStage of RAW_STAGES) assert.ok(!svg.textContent.includes(rawStage), `raw stage key "${rawStage}" must not appear as text`);
  assert.doesNotMatch(svg.textContent, /\b(N\d+|A[1-5])\b/, 'rule ids must not appear in the task graph');
  assert.ok(svg.querySelectorAll('path').length >= 5, 'edges must be drawn as SVG paths');
});

// --- Page integration tests (list/graph switch, tab order, scroll box, focus/hover) ---

test('board hides list/graph switch for no plan and for one task, and offers it from two tasks up', async (t) => {
  const emptyPage = await bootPage(t, fakeDaemon({ tasks: [] }));
  assert.equal(emptyPage.doc.querySelector(`[role="group"][aria-label="${W.group}"]`), null, 'no switch when there are 0 tasks');
  assert.match(emptyPage.doc.getElementById('main').textContent, /no tasks yet/i);

  const oneTaskPage = await bootPage(t, fakeDaemon({ tasks: [makeTask('T1', 'Only task', 'working', 1)] }));
  assert.equal(oneTaskPage.doc.querySelector(`[role="group"][aria-label="${W.group}"]`), null, 'no switch when there is only 1 task');
  assert.equal(oneTaskPage.doc.querySelector('#main svg'), null, 'no graph rendered for 1 task');

  const twoTaskPage = await bootPage(t, fakeDaemon({
    tasks: [
      makeTask('T1', 'Base table', 'done', 1, [], ['T2']),
      makeTask('T2', 'List page', 'working', 2, ['T1'], []),
    ],
  }));
  const sw = twoTaskPage.doc.querySelector(`[role="group"][aria-label="${W.group}"]`);
  assert.ok(sw, 'switch must appear from 2 tasks up');
  const btns = sw.querySelectorAll('button');
  const listBtn = btns.find((b) => b.textContent.trim() === W.list);
  const graphBtn = btns.find((b) => b.textContent.trim() === W.graph);
  assert.ok(listBtn && graphBtn, 'switch must contain list and graph buttons');
  assert.equal(listBtn.getAttribute('aria-pressed'), 'true', 'list must be the default view');
  assert.equal(graphBtn.getAttribute('aria-pressed'), 'false');
});

test('switching to graph renders SVG nodes in layer order, remembers choice across polls, puts switch before graph in tab order, and expands board to full width', async (t) => {
  const tasks = [
    makeTask('T1', 'Notes table', 'done', 1, [], ['T2', 'T3']),
    makeTask('T2', 'Notes API', 'working', 2, ['T1'], ['T4']),
    makeTask('T3', 'Search index', 'ci', 2, ['T1'], ['T4']),
    makeTask('T4', 'Search box', 'waiting_on_dep', 3, ['T2', 'T3'], []),
  ];
  const page = await bootPage(t, fakeDaemon({ tasks }));
  const board = page.doc.getElementById('board');
  const listBoardClass = board.className;

  const getSwitch = () => page.doc.querySelector(`[role="group"][aria-label="${W.group}"]`);
  const btnNamed = (name) => getSwitch().querySelectorAll('button').find((b) => b.textContent.trim() === name);

  btnNamed(W.graph).click();
  await page.flush();

  assert.equal(btnNamed(W.graph).getAttribute('aria-pressed'), 'true');
  assert.equal(btnNamed(W.list).getAttribute('aria-pressed'), 'false');
  const region = page.doc.querySelector('[role="region"][aria-label^="task graph:"]');
  assert.ok(region, 'graph view must render a region labelled "task graph: …"');
  assert.match(region.getAttribute('aria-label'), /^task graph: 4 tasks in 3 layers/);
  const nodeLinks = region.querySelectorAll('svg a');
  assert.equal(nodeLinks.length, 4);
  assert.deepEqual(nodeLinks.map((a) => a.getAttribute('href')), ['#proj-demo/T1', '#proj-demo/T2', '#proj-demo/T3', '#proj-demo/T4']);

  // Full width layout marker changes on #board (or .cols) in graph view so aside moves below.
  assert.notEqual(board.className, listBoardClass, 'graph view must toggle a full-width layout class on #board');

  // Tab order: switch comes before the graph region and before every node link.
  const all = board.querySelectorAll('*');
  assert.ok(all.indexOf(getSwitch()) < all.indexOf(region), 'switch must precede graph box in DOM/tab order');
  assert.ok(all.indexOf(region) < all.indexOf(nodeLinks[0]), 'graph box must precede first node');

  // Choice is remembered for the visit across the 3 s poll.
  await page.pollNow();
  assert.equal(btnNamed(W.graph).getAttribute('aria-pressed'), 'true', 'graph choice must survive 3 s poll');
  assert.ok(page.doc.querySelector('[role="region"][aria-label^="task graph:"] svg'), 'graph SVG must still be shown after poll');

  // List is always one switch away.
  btnNamed(W.list).click();
  await page.flush();
  assert.equal(btnNamed(W.list).getAttribute('aria-pressed'), 'true');
  assert.equal(page.doc.querySelector('[role="region"][aria-label^="task graph:"]'), null, 'switching to list must remove the graph view');
});

test('wide graph (fan of 8 or 360 px window) shows gr.scroll hint and makes graph box keyboard-focusable', async (t) => {
  const mid = Array.from({ length: 8 }, (_, i) => makeTask(`U${i + 1}`, `Feature ${i + 1}`, 'queued', 2, ['U0'], ['U9']));
  const tasks = [
    makeTask('U0', 'Root task', 'done', 1, [], mid.map((x) => x.ref)),
    ...mid,
    makeTask('U9', 'Join task', 'waiting_on_dep', 3, mid.map((x) => x.ref), []),
  ];
  const page = await bootPage(t, fakeDaemon({ tasks }), { width: 1280 });
  const sw = page.doc.querySelector(`[role="group"][aria-label="${W.group}"]`);
  sw.querySelectorAll('button').find((b) => b.textContent.trim() === W.graph).click();
  await page.flush();

  assert.ok(page.doc.getElementById('main').textContent.includes(W.scroll), `wide graph must show scroll hint "${W.scroll}"`);
  const box = page.doc.querySelector('[role="region"][aria-label^="task graph:"]');
  assert.ok(box, 'expected graph region box');
  assert.equal(box.getAttribute('aria-label'), 'task graph: 10 tasks in 3 layers; scrolls sideways');
  assert.equal(box.getAttribute('tabindex'), '0', 'scrolling graph box must be keyboard-focusable with tabindex="0"');

  // Narrow 360 px window also keeps the graph scrollable inside its own box with the hint.
  await page.setWidth(360);
  assert.ok(page.doc.getElementById('main').textContent.includes(W.scroll), '360 px window must show scroll hint');
  assert.equal(page.doc.querySelector('[role="region"][aria-label^="task graph:"]')?.getAttribute('tabindex'), '0');
});

test('focus or hover on a node highlights its own edges and neighbours and dims unrelated nodes and edges', async (t) => {
  const tasks = [
    makeTask('T1', 'Root A', 'done', 1, [], ['T2', 'T3']),
    makeTask('T2', 'Child A1', 'working', 2, ['T1'], ['T4']),
    makeTask('T3', 'Child A2', 'working', 2, ['T1'], ['T4']),
    makeTask('T4', 'Join A', 'waiting_on_dep', 3, ['T2', 'T3'], []),
    makeTask('T5', 'Root B', 'done', 1, [], ['T6']),
    makeTask('T6', 'Child B1', 'queued', 2, ['T5'], []),
  ];
  const page = await bootPage(t, fakeDaemon({ tasks }));
  page.doc.querySelector(`[role="group"][aria-label="${W.group}"]`).querySelectorAll('button').find((b) => b.textContent.trim() === W.graph).click();
  await page.flush();

  const region = page.doc.querySelector('[role="region"][aria-label^="task graph:"]');
  const nodeByRef = (ref) => region.querySelectorAll('svg a').find((a) => a.getAttribute('href') === `#proj-demo/${ref}`);
  const isDim = (el) => el.classList.contains('dim') || el.hasAttribute('data-dim');

  const n1 = nodeByRef('T1');
  n1.focus();
  n1.dispatchEvent({ type: 'focusin', bubbles: true });
  n1.dispatchEvent({ type: 'focus', bubbles: false });
  await page.flush();

  // T1 and its direct neighbours (T2, T3) stay strong; unrelated nodes (T4, T5, T6) are dimmed.
  assert.ok(!isDim(nodeByRef('T1')), 'focused node T1 must not be dimmed');
  assert.ok(!isDim(nodeByRef('T2')), 'direct neighbour T2 must not be dimmed');
  assert.ok(!isDim(nodeByRef('T3')), 'direct neighbour T3 must not be dimmed');
  assert.ok(isDim(nodeByRef('T4')), 'non-neighbour T4 must be dimmed');
  assert.ok(isDim(nodeByRef('T5')), 'unrelated node T5 must be dimmed');
  assert.ok(isDim(nodeByRef('T6')), 'unrelated node T6 must be dimmed');

  const paths = region.querySelectorAll('svg path');
  assert.ok(paths.some((p) => isDim(p)), 'unrelated edges must be dimmed while a node is focused');
  assert.ok(paths.some((p) => !isDim(p)), 'incident edges must stay strong while a node is focused');

  // Clearing focus removes dimming from all nodes and edges.
  n1.dispatchEvent({ type: 'focusout', bubbles: true });
  n1.dispatchEvent({ type: 'blur', bubbles: false });
  await page.flush();
  assert.ok(region.querySelectorAll('svg a').every((a) => !isDim(a)), 'clearing focus must restore all nodes');
  assert.ok(region.querySelectorAll('svg path').every((p) => !isDim(p)), 'clearing focus must restore all edges');
});

test('graph CSS keeps node text >= 12 px, scrolls inside graph box, uses stage tokens, and app.js holds no POST', () => {
  const css = readFileSync(new URL('./style.css', import.meta.url), 'utf8');
  assert.match(css, /overflow-x:\s*auto/, 'style.css must scroll the graph inside its own box with overflow-x: auto');
  assert.match(css, /\.gnode/, 'style.css must style .gnode');
  for (const m of css.matchAll(/\.gnode[^{]*\{[^}]*font-size:\s*([\d.]+)px/g)) {
    assert.ok(Number(m[1]) >= 12, `graph node text font-size must be >= 12px (found ${m[1]}px)`);
  }
  const app = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
  assert.ok(!/\bPOST\b|method\s*:/.test(app), 'app.js must hold no POST');
});
