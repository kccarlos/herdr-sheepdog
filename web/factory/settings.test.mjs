// Unit tests for PR 14.2: /factory settings panel, inline read-back, pairing
// beside the triggering control, and narrow-layout DOM/tab order (`make test-web`).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { PAIR_HINT, createAskFlow } from './asks.js';

const W = {
  sub: 'For this project only. A change is read back to you and saved when you confirm.',
  loading: 'Loading the settings…', cant: "Can't load the settings. Trying again.", cantRest: 'The rest of the page still works.',
  badStalled: 'The stored value 0 is not allowed (allowed 10–600). Change it before the next start.',
  unusedNote: 'not used while reviewer models are set', cardMeta: 'setting · the engine asks',
  wait: 'Waiting for your answer. The same question is under asks for you, on your phone and by voice.',
  saved: 'Saved. The engine uses it from now on.', kept: 'Kept at 2. Nothing changed.',
  pairH: 'pair this browser', pairHelp: 'No Sheepdog menu-bar app?', pairHelpLink: 'Download the Sheepdog menu-bar app',
  pairP: 'Changes from this page need a paired browser, once. On this Mac, open the Sheepdog menu-bar app (its icon is in the menu bar), choose Pair browser…, and type the code it shows.',
  pairKept: 'What you just asked for is kept. It is sent as soon as this browser is paired.',
  pairHelpP: 'The code comes from the Sheepdog menu-bar app. It is not installed on this Mac, or it is not open.',
  pairHelpThen: 'Open it, then choose Pair browser… from its icon in the menu bar.',
  pairWrong: 'That code did not work. It was mistyped, already used, or older than 5 minutes. Choose Pair browser… again for a new code.',
};

const baseRows = () => [
  { name: 'max_running', label: 'max running', value: '4', default: '3', min: 1, max: 20, top: true },
  { name: 'review_rounds', label: 'review rounds', value: '2', default: '2', min: 1, max: 5 },
  { name: 'max_rounds', label: 'max rounds', value: '3', default: '5', min: 2, max: 10 },
  { name: 'ci_stalled_minutes', label: 'ci stalled minutes', value: '0', default: '90', min: 10, max: 600, problem: 'ci stalled minutes 0: allowed 10–600' },
  { name: 'second_reviewer', label: 'second reviewer', value: 'opus', default: 'opus', min: 0, max: 0, allowed: ['opus', 'codex'], note: W.unusedNote },
  { name: 'design_gate', label: 'design gate', value: '', default: '', min: 0, max: 0, allowed: ['', 'owner', 'review'] },
  { name: 'dispatch_workflow', label: 'dispatch workflow', value: '', default: '', min: 0, max: 0 },
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
  checkValidity() {
    if (this.tagName !== 'INPUT' || this.type !== 'number') return true;
    const n = Number(this.value), mn = this.getAttribute('min'), mx = this.getAttribute('max');
    return this.value !== '' && !Number.isNaN(n) && (mn === null || n >= +mn) && (mx === null || n <= +mx);
  }
  reportValidity() { return this.checkValidity(); }
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

function fakeDaemon({ rows = baseRows(), asks = [], paired = false, settingsStatus = 200, holdSettings = null, onPropose = null } = {}) {
  const proposed = [], answered = [], openAsks = asks.map((a) => ({ ...a }));
  let hasSession = paired, askSeq = 0, inFlight = 0;
  const reply = (s, o) => ({ ok: s >= 200 && s < 300, status: s, json: async () => o });
  const fetch = async (path, init = {}) => {
    const method = init.method || 'GET', body = init.body ? JSON.parse(init.body) : null; inFlight++;
    try {
      await Promise.resolve();
      if (path === '/api/hello') return reply(200, { computer: { id: 'D1', name: 'Test Mac' } });
      if (path === '/api/devices/enroll') return String(body.code || '').replace(/[\s-]+/g, '').toUpperCase() === 'ABCDEFGH' ? reply(200, { device_id: 'dev-1', daemon_id: 'D1' }) : reply(410, { reason: 'code_used', error: 'Invalid or used code.' });
      if (path === '/api/auth/challenge') return reply(200, { nonce: 'N1', daemon_id: 'D1', expires_in: 60 });
      if (path === '/api/auth/session') { hasSession = true; return reply(200, { expires_in: 43200, device: { id: 'dev-1' } }); }
      if (path === '/api/factory/projects') return reply(200, { protocol: 1, projects: [{ id: 'proj-demo', goal: 'A notes page with search', running: true, counts: { working: 1 } }] });
      if (path === '/api/factory/projects/proj-demo/graph') return reply(200, { protocol: 1, project: 'proj-demo', tasks: [{ ref: 'T1', title: 'Search box', stage: 'working', layer: 1, after: [], dependents: [] }] });
      if (path.startsWith('/api/factory/projects/proj-demo/log')) return reply(200, { protocol: 1, project: 'proj-demo', lines: [{ at: '2026-11-24T12:00:00Z', by: 'engine', task: 'T1', decision: 'started', why: 'worker' }] });
      if (path === '/api/factory/asks') return reply(200, { protocol: 1, asks: openAsks.filter((a) => a.status === 'open') });
      if (path === '/api/factory/projects/proj-demo/settings' && method === 'GET') {
        if (holdSettings) { inFlight--; await holdSettings; inFlight++; }
        return settingsStatus === 200 ? reply(200, { protocol: 1, rows }) : reply(settingsStatus, { error: 'boom' });
      }
      if (path === '/api/factory/projects/proj-demo/settings' && method === 'POST') {
        if (!hasSession) return reply(403, { code: 'proof_required', error: 'proof_required' });
        proposed.push(body);
        if (onPropose) { const res = onPropose(body, rows, openAsks); if (res) return reply(res.status, res.body); }
        const row = rows.find((r) => r.name === body.name), val = body.value === '' ? row.default : body.value;
        const ask = { id: `ask-set-${++askSeq}`, project: 'proj-demo', kind: 'rule', gated: true, asked_by: 'engine', status: 'open', question: `This project: ${row.label} ${val || 'empty'} (now ${row.value || 'empty'}). Save?`, options: ['Save', 'Keep current'], recommend: 'Save', set: { name: row.name, value: val, was: row.value } };
        openAsks.push(ask); return reply(200, { ok: true, ask });
      }
      const m = path.match(/^\/api\/factory\/asks\/([^/]+)\/answer$/);
      if (m && method === 'POST') {
        if (!hasSession) return reply(403, { code: 'proof_required', error: 'proof_required' });
        const ask = openAsks.find((a) => a.id === m[1] && a.status === 'open');
        if (!ask) return reply(409, { code: 'already_closed', error: 'already_closed' });
        ask.status = 'answered'; answered.push({ id: m[1], option: body.option });
        if (body.option === 'Save' && ask.set) { const row = rows.find((r) => r.name === ask.set.name); if (row) row.value = ask.set.value; }
        return reply(200, { ok: true, ask_id: m[1], device_id: 'dev-1' });
      }
      return reply(404, { error: 'not found' });
    } finally { inFlight--; }
  };
  return { fetch, proposed, answered, rows, openAsks, get inFlight() { return inFlight; } };
}
const realSet = globalThis.setTimeout, realClr = globalThis.clearTimeout, realCrypto = globalThis.crypto;
let bootSeq = 0, activeCleanup = null;
async function bootPage(t, daemon, { width = 1200, settle = true } = {}) {
  if (activeCleanup) await activeCleanup();
  const doc = createDoc(readFileSync(new URL('./index.html', import.meta.url), 'utf8')), idb = createIDB();
  const keys = ['document', 'window', 'location', 'indexedDB', 'fetch', 'crypto', 'addEventListener', 'removeEventListener', 'scrollTo', 'innerWidth', 'matchMedia'];
  const prev = Object.fromEntries(keys.map((k) => [k, Object.getOwnPropertyDescriptor(globalThis, k)]));
  const pending = new Set(), timers = new Map(), mqls = [], winLs = new Map();
  let curW = width, seq = 1000, disposed = false;
  const px = (n, u) => +n * (u === 'px' ? 1 : 16);
  const evalMQ = (q) => { const mx = q.match(/max-width:\s*([\d.]+)(px|r?em)/), mn = q.match(/min-width:\s*([\d.]+)(px|r?em)/); return (!mx || curW <= px(mx[1], mx[2])) && (!mn || curW >= px(mn[1], mn[2])); };
  const track = (p) => { if (p?.then) { const m = Promise.resolve(p).then(() => {}, () => {}).finally(() => pending.delete(m)); pending.add(m); } return p; };
  if (settle) doc._track = track;
  const subtle = new Proxy(realCrypto.subtle, { get(o, p) { const v = o[p]; return typeof v === 'function' ? (...a) => track(v.apply(o, a)) : v; } });
  globalThis.setTimeout = (fn, ms, ...a) => disposed ? ++seq : (+ms >= 500 ? (timers.set(++seq, { fn: () => fn(...a), ms: +ms }), seq) : realSet(() => { if (!disposed) fn(...a); }, ms));
  globalThis.clearTimeout = (id) => { timers.delete(id) || realClr(id); };
  const setP = (k, v) => Object.defineProperty(globalThis, k, { value: v, configurable: true, writable: true });
  for (const [k, v] of [['document', doc], ['window', globalThis], ['indexedDB', idb], ['crypto', { subtle, getRandomValues: (a) => realCrypto.getRandomValues(a), randomUUID: () => realCrypto.randomUUID() }], ['scrollTo', () => {}], ['innerWidth', curW], ['location', { hash: '', href: 'http://127.0.0.1:7777/factory' }],
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
  return { doc, flush,
    async pollNow() { const e = [...timers.entries()].find(([, v]) => v.ms <= 3500); if (e) { timers.delete(e[0]); track(e[1].fn()); } else doc.getElementById('retry').click(); await flush(); },
    async setWidth(w) { curW = w; setP('innerWidth', w); mqls.forEach((m) => m._fire()); (winLs.get('resize') || []).forEach((fn) => fn({ type: 'resize' })); await flush(); },
  };
}

const findCard = (doc, h) => doc.querySelectorAll('section').find((s) => s.querySelector('h2')?.textContent.trim().toLowerCase() === h);
const rowOf = (sec, label, doc) => {
  const lbl = sec.querySelectorAll('label').find((l) => l.textContent.trim() === label);
  assert.ok(lbl, `expected <label> "${label}" in settings`);
  const ctrl = lbl.getAttribute('for') ? doc.getElementById(lbl.getAttribute('for')) : lbl.querySelector('input,select');
  assert.ok(ctrl && sec.contains(ctrl), `label "${label}" must tie to an input/select`);
  let box = lbl.parentElement; while (box && !box.contains(ctrl)) box = box.parentElement;
  const others = baseRows().map((r) => r.label).filter((l) => l !== label);
  while (box?.parentElement && box.parentElement !== sec && box.parentElement.tagName !== 'DETAILS'
    && !box.parentElement.querySelectorAll('label').some((l) => others.includes(l.textContent.trim()))) box = box.parentElement;
  return { lbl, ctrl, box };
};
const editRow = async (page, sec, label, val, ev = 'input') => {
  const r = rowOf(sec, label, page.doc); r.ctrl.value = val; r.ctrl.dispatchEvent({ type: ev, bubbles: true }); await page.flush();
  return rowOf(sec, label, page.doc);
};

test('GET settings renders top rows first, all settings fold, plain labels, default/range, marks, escaping and no jargon', async (t) => {
  const rows = baseRows(); rows[6].value = '<script>alert(1)</script>';
  const page = await bootPage(t, fakeDaemon({ rows })), sec = findCard(page.doc, 'settings');
  assert.ok(sec && sec.textContent.includes(W.sub), 'expected settings section with subtitle');
  const fold = sec.querySelector('details');
  assert.ok(fold && !fold.open, 'all settings fold must exist and start closed');
  assert.match(fold.querySelector('summary')?.textContent || '', /all settings \(6\)/);
  const maxRun = rowOf(sec, 'max running', page.doc);
  assert.ok(!fold.contains(maxRun.box), 'top row max running must render above the fold');
  assert.match(maxRun.box.textContent, /default 3 · allowed 1–20/); assert.match(maxRun.box.textContent, /\bchanged\b/);
  assert.equal(maxRun.box.querySelectorAll('button').find((b) => b.textContent.trim() === 'Back to the default')?.getAttribute('aria-label'), 'Set max running back to the default, 3');
  const rev = rowOf(sec, 'review rounds', page.doc);
  assert.ok(fold.contains(rev.box), 'non-top row review rounds must sit inside all settings fold');
  assert.equal(rev.ctrl.getAttribute('type'), 'number'); assert.equal(rev.ctrl.getAttribute('min'), '1'); assert.equal(rev.ctrl.getAttribute('max'), '5');
  assert.ok(rev.ctrl.getAttribute('aria-describedby'), 'number input must reference its range via aria-describedby');
  assert.match(rev.box.textContent, /default 2 · allowed 1–5/); assert.doesNotMatch(rev.box.textContent, /\bchanged\b|Back to the default/);
  const bad = rowOf(sec, 'ci stalled minutes', page.doc);
  assert.match(bad.box.textContent, /\bnot allowed\b/); assert.equal(bad.box.querySelector('[role="alert"]')?.textContent.trim(), W.badStalled);
  const sr = rowOf(sec, 'second reviewer', page.doc);
  assert.equal(sr.ctrl.tagName, 'SELECT'); assert.match(sr.box.textContent, /default opus · allowed opus, codex/);
  assert.match(sr.box.textContent, /\bnot used\b/); assert.ok(sr.box.textContent.includes(W.unusedNote));
  const dg = rowOf(sec, 'design gate', page.doc);
  assert.equal(dg.ctrl.tagName, 'SELECT'); assert.match(dg.box.textContent, /default not set/);
  assert.ok(dg.ctrl.querySelectorAll('option').some((o) => o.textContent.trim() === 'not set'));
  for (const el of sec.querySelectorAll('input, select')) assert.ok(el.id && sec.querySelector(`label[for="${el.id}"]`), `control ${el.tagName} must have a tied <label>`);
  assert.ok(!sec.innerHTML.includes('<script>alert(1)</script>') && (sec.innerHTML.includes('&lt;script&gt;') || sec.innerHTML.includes('&#60;script&#62;')), 'setting value HTML must be escaped');
  for (const r of rows) assert.ok(!sec.textContent.includes(r.name), `raw setting key ${r.name} must not appear as text`);
  assert.doesNotMatch(sec.textContent, /\b(SE\d+|N\d+|A[1-5])\b/, 'rule ids must not appear in settings panel');
});

test('settings panel renders loading and cannot-load states while the rest of the page works', async (t) => {
  let release; const hold = new Promise((r) => { release = r; });
  const loadPage = await bootPage(t, fakeDaemon({ holdSettings: hold }), { settle: false });
  assert.ok(findCard(loadPage.doc, 'settings')?.querySelector('[role="status"]')?.textContent.includes(W.loading), 'expected Loading the settings… status');
  release(); await loadPage.flush();
  const errPage = await bootPage(t, fakeDaemon({ settingsStatus: 500 })), errSec = findCard(errPage.doc, 'settings');
  assert.ok(errSec?.querySelector('[role="status"]')?.textContent.includes(W.cant) && errSec.textContent.includes(W.cantRest));
  assert.match(errPage.doc.getElementById('main').textContent, /Search box/, 'tasks board must still render');
});

test('Change button appears only while a field differs from its stored value with wording-table aria-labels', async (t) => {
  const page = await bootPage(t, fakeDaemon({ paired: true })), sec = findCard(page.doc, 'settings');
  const changeBtn = (b) => b.querySelectorAll('button').find((x) => x.textContent.trim() === 'Change');
  assert.equal(changeBtn(rowOf(sec, 'review rounds', page.doc).box), undefined, 'Change must hide while unchanged');
  assert.equal(changeBtn((await editRow(page, sec, 'review rounds', '3')).box)?.getAttribute('aria-label'), 'Change review rounds');
  assert.equal(changeBtn((await editRow(page, sec, 'review rounds', '2')).box), undefined, 'Change must hide when reverted');
  assert.equal(changeBtn((await editRow(page, sec, 'second reviewer', 'codex', 'change')).box)?.getAttribute('aria-label'), 'Change second reviewer');
});

test('unpaired Change opens pairing form beside the row (not in #asks), handles wrong code, and resumes proposal', async (t) => {
  const daemon = fakeDaemon({ paired: false }), page = await bootPage(t, daemon), sec = findCard(page.doc, 'settings');
  (await editRow(page, sec, 'review rounds', '3')).box.querySelector('button[aria-label="Change review rounds"]').click(); await page.flush();
  const pairBox = sec.querySelector('[role="region"][aria-label="pair this browser"]');
  assert.ok(pairBox, 'pairing box must appear inside settings beside the control that needed it');
  assert.equal(page.doc.getElementById('asks').querySelector('form[data-pair-form]'), null, 'pairing box must not appear in #asks');
  for (const txt of [W.pairH, W.pairP, W.pairKept, W.pairHelp, W.pairHelpP, W.pairHelpLink, W.pairHelpThen]) assert.ok(pairBox.textContent.includes(txt), `missing "${txt}"`);
  const code = pairBox.querySelector('input[name="code"]');
  assert.equal(pairBox.querySelector(`label[for="${code?.id}"]`)?.textContent.trim(), 'pairing code');
  code.value = 'BAD-CODE'; pairBox.querySelector('form[data-pair-form]').dispatchEvent({ type: 'submit', bubbles: true }); await page.flush();
  assert.equal(sec.querySelector('[role="region"][aria-label="pair this browser"] [role="alert"]')?.textContent.trim(), W.pairWrong);
  sec.querySelector('input[name="code"]').value = 'ABCD-EFGH';
  sec.querySelector('form[data-pair-form]').dispatchEvent({ type: 'submit', bubbles: true }); await page.flush();
  assert.deepEqual(daemon.proposed, [{ name: 'review_rounds', value: '3' }]);
  assert.equal(sec.querySelector('form[data-pair-form]'), null, 'pairing box must close after pairing');
});

test('read-back Save/Keep current renders in row with recommended outline (not filled) and status survives 3 s poll', async (t) => {
  const recRule = readFileSync(new URL('./style.css', import.meta.url), 'utf8').match(/(?:^|[\n}])\s*\.ask-opt\.rec\s*\{([^}]*)\}/);
  assert.ok(recRule, 'style.css must style .ask-opt.rec');
  assert.doesNotMatch(recRule[1], /background(?:-color)?\s*:\s*var\(--primary\)/, 'recommended button must be outlined, not filled with --primary');
  const page = await bootPage(t, fakeDaemon({ paired: true })), sec = findCard(page.doc, 'settings');
  const propose = async (val) => { (await editRow(page, sec, 'review rounds', val)).box.querySelector('button[aria-label="Change review rounds"]').click(); await page.flush(); };
  await propose('3');
  const row = rowOf(sec, 'review rounds', page.doc).box;
  assert.ok(row.textContent.includes('This project: review rounds 3 (now 2). Save?') && row.textContent.includes(W.wait));
  const saveBtn = row.querySelector('button[aria-label="Save (Recommended)"]'), keepBtn = row.querySelectorAll('button').find((b) => b.textContent.trim() === 'Keep current');
  assert.ok(saveBtn?.textContent.includes('Recommended') && keepBtn, 'row must show Save (Recommended) and Keep current');
  assert.equal(row.querySelector('button[aria-label="Change review rounds"]'), null, 'Change hides while waiting for confirm');
  assert.ok(page.doc.getElementById('asks').textContent.includes(W.cardMeta), 'asks card must show setting · the engine asks');
  keepBtn.click(); await page.flush();
  assert.ok(rowOf(sec, 'review rounds', page.doc).box.querySelector('[role="status"]')?.textContent.includes(W.kept));
  await page.pollNow();
  assert.ok(rowOf(sec, 'review rounds', page.doc).box.querySelector('[role="status"]')?.textContent.includes(W.kept), 'Kept status must survive 3 s poll');
  await propose('3');
  rowOf(sec, 'review rounds', page.doc).box.querySelector('button[aria-label="Save (Recommended)"]').click(); await page.flush();
  assert.ok(rowOf(sec, 'review rounds', page.doc).box.querySelector('[role="status"]')?.textContent.includes(W.saved));
  await page.pollNow();
  assert.ok(rowOf(sec, 'review rounds', page.doc).box.querySelector('[role="status"]')?.textContent.includes(W.saved), 'Saved status must survive 3 s poll');
});

test('refusal messages render under the named row with role=alert and survive the 3 s poll', async (t) => {
  const msg = 'review rounds 4 does not fit: max rounds is 3 (allowed 4–10). Raise max rounds first.';
  const page = await bootPage(t, fakeDaemon({ paired: true, onPropose: () => ({ status: 400, body: { code: 'out_of_range', error: msg } }) }));
  const sec = findCard(page.doc, 'settings');
  (await editRow(page, sec, 'review rounds', '4')).box.querySelector('button[aria-label="Change review rounds"]').click(); await page.flush();
  assert.equal(rowOf(sec, 'review rounds', page.doc).box.querySelector('[role="alert"]')?.textContent.trim(), msg);
  await page.pollNow();
  assert.equal(rowOf(sec, 'review rounds', page.doc).box.querySelector('[role="alert"]')?.textContent.trim(), msg, 'refusal must survive 3 s poll');
  assert.equal(rowOf(sec, 'review rounds', page.doc).ctrl.value, '4', 'edited value must survive 3 s poll');
});

test('narrow layout (<760 px) reorders sections in DOM source and tab order, and restores wide order above 760 px', async (t) => {
  const ask = { id: 'a1', project: 'proj-demo', kind: 'gate', gated: true, question: 'Merge T1?', options: ['Merge', 'Hold'], recommend: 'Merge', status: 'open' };
  const page = await bootPage(t, fakeDaemon({ asks: [ask] }), { width: 360 }), board = page.doc.getElementById('board'), all = board.querySelectorAll('*');
  const h1 = board.querySelector('h1'), asks = page.doc.getElementById('asks'), group = board.querySelector('section.group');
  const log = findCard(page.doc, 'log'), projs = page.doc.getElementById('projects'), set = findCard(page.doc, 'settings'), agents = page.doc.getElementById('agents');
  const order = [h1, asks, group, log, projs, set, agents].map((el) => all.indexOf(el));
  assert.ok(order.every((i) => i >= 0), 'all 7 sections must exist inside #board');
  for (let i = 1; i < order.length; i++) assert.ok(order[i - 1] < order[i], `narrow DOM order wrong at ${i}: ${order.join(', ')}`);
  const inClosed = (e) => { for (let c = e.parentElement; c; c = c.parentElement) if (c.tagName === 'DETAILS' && !c.open) return e.tagName !== 'SUMMARY' || e.parentElement !== c; return false; };
  const tabbable = all.filter((e) => !e.disabled && !inClosed(e) && (e.matches('a[href], button, input, select, textarea, summary') || e.getAttribute('tabindex') === '0'));
  const tPos = (el) => tabbable.indexOf(el);
  assert.ok(tPos(asks.querySelector('button')) < tPos(group.querySelector('a.row')), 'tab order: asks before task groups');
  assert.ok(tPos(group.querySelector('a.row')) < tPos(projs.querySelector('a.proj')), 'tab order: task groups before projects');
  assert.ok(tPos(projs.querySelector('a.proj')) < tPos(set.querySelector('input')), 'tab order: projects before settings');
  await page.setWidth(1200);
  const wideAll = board.querySelectorAll('*');
  assert.ok(wideAll.indexOf(set) < wideAll.indexOf(projs) && wideAll.indexOf(projs) < wideAll.indexOf(asks), 'wide order must put settings in main before aside');
});

test('asks.js exports updated PAIR_HINT, pairBoxHTML and send; settings.js exports renderSettingsHTML; app.js has no POST', async () => {
  assert.equal(PAIR_HINT, W.pairP);
  const asksMod = await import('./asks.js');
  assert.equal(typeof asksMod.pairBoxHTML, 'function', 'asks.js must export pairBoxHTML');
  const flow = createAskFlow({ load: async () => null, fetch: async (u) => u === '/api/hello' ? { ok: true, status: 200, json: async () => ({ computer: { id: 'D1' } }) } : { ok: false, status: 403, json: async () => ({ code: 'proof_required' }) } });
  assert.equal(typeof flow.send, 'function', 'createAskFlow must expose send(url, body)');
  const res = await flow.send('/api/factory/projects/p1/settings', { name: 'review_rounds', value: '3' });
  assert.equal(res.needsPairing, true);
  const setMod = await import('./settings.js');
  assert.equal(typeof setMod.renderSettingsHTML, 'function', 'settings.js must export renderSettingsHTML');
  assert.ok(!/\bPOST\b|method\s*:/.test(readFileSync(new URL('./app.js', import.meta.url), 'utf8')), 'app.js must hold no POST');
});
