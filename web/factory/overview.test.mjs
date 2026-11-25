// Unit and page tests for PR 14.10: /factory project overview and needs-you indicator (`make test-web`).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { renderAskCardsHTML } from './asks.js';

const W = {
  ovH: 'projects',
  ovNoTasks: 'no tasks yet',
  ovErr: "can't read project",
  ovErrHint: 'Its state file could not be read.',
  ovAll: 'all projects',
  niH: 'needs you',
  niNone: 'No open asks right now.',
  emptyH: 'no project yet',
  projNone: 'None yet.',
  projNew: 'New project',
  npH: 'new project',
  cantShow: "can't show the factory",
};

const AGY_MODELS = ['gemini-4-carbon', 'gemini-4-neon', 'gemini-4-hydrogen', 'gemini-3.8-flash'];

const defaultRows = () => [
  { name: 'models.coordinator', label: 'coordinator models', value: 'claude/opus/medium', default: 'claude/opus/medium', min: 0, max: 0, top: true, clis: ['claude', 'codex', 'agy'], models: { agy: AGY_MODELS } },
  { name: 'models.worker', label: 'worker models', value: 'claude/sonnet/medium', default: 'claude/sonnet/medium', min: 0, max: 0, top: true, clis: ['claude', 'codex', 'agy'], models: { agy: AGY_MODELS } },
  { name: 'models.reviewer', label: 'reviewer models', value: 'claude/opus/medium', default: 'claude/opus/medium', min: 0, max: 0, top: true, clis: ['claude', 'codex', 'agy'], models: { agy: AGY_MODELS } },
  { name: 'models.retro', label: 'retro models', value: 'claude/opus/high', default: 'claude/opus/high', min: 0, max: 0, top: true, clis: ['claude', 'agy'], models: { agy: AGY_MODELS } },
  { name: 'max_running', label: 'max running', value: '3', default: '3', min: 1, max: 20, top: true },
  { name: 'review_rounds', label: 'review rounds', value: '2', default: '2', min: 1, max: 5 },
  { name: 'max_rounds', label: 'max rounds', value: '5', default: '5', min: 2, max: 10 },
  { name: 'ci_stalled_minutes', label: 'ci stalled minutes', value: '90', default: '90', min: 10, max: 600 },
  { name: 'second_reviewer', label: 'second reviewer', value: 'opus', default: 'opus', min: 0, max: 0, allowed: ['opus', 'codex'] },
  { name: 'design_gate', label: 'design gate', value: '', default: '', min: 0, max: 0, allowed: ['', 'owner', 'review'] },
  { name: 'dispatch_workflow', label: 'dispatch workflow', value: '', default: '', min: 0, max: 0 },
];

const mkTask = (over = {}) => ({
  ref: 'T1',
  title: 'Search box on the notes page',
  stage: 'working',
  why_waiting: 'In review round 2',
  after: [],
  dependents: [],
  layer: 0,
  rounds: 1,
  model: 'claude/sonnet/medium',
  terminal: 'run-1',
  ...over,
});

const mkProj = (over = {}) => ({
  id: 'proj-notes',
  folder: '/Users/dev/code/notes',
  goal: 'A notes page with search',
  state: 'running',
  running: true,
  live: true,
  counts: { working: 3, ci: 1, queued: 2, done: 2 },
  _tasks: [mkTask()],
  ...over,
});

const mkAsk = (over = {}) => ({
  id: 'ask-1',
  project: 'proj-notes',
  status: 'open',
  kind: 'gate',
  gated: true,
  task: 'T6',
  question: 'Delete the old notes table in the migration?',
  options: ['Keep it', 'Delete it'],
  recommend: 'Keep it',
  ...over,
});

const mkSettingAsk = (over = {}) => ({
  id: 'ask-set-1',
  project: 'proj-notes',
  status: 'open',
  kind: 'setting',
  gated: true,
  question: 'This project: review rounds 3 (now 2). Save?',
  options: ['Save', 'Keep current'],
  recommend: 'Save',
  set: { name: 'review_rounds', value: '3', was: '2' },
  ...over,
});

const VOID = new Set(['AREA', 'BASE', 'BR', 'COL', 'EMBED', 'HR', 'IMG', 'INPUT', 'LINK', 'META', 'SOURCE', 'TRACK', 'WBR']);
const FOCUSABLE = new Set(['A', 'BUTTON', 'INPUT', 'SELECT', 'TEXTAREA', 'SUMMARY']);
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
    Object.assign(this, { nodeType: 1, tagName: tag.toUpperCase(), ownerDocument: doc, parentElement: null, _nodes: [], _attrs: new Map(), _ls: new Map(), _raw: '', _val: undefined, _scrolled: 0 });
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
  get parentNode() { return this.parentElement; }
  get childNodes() { return this._nodes; }
  get children() { return this._nodes.filter((n) => n.nodeType === 1); }
  get firstElementChild() { return this.children[0] || null; }
  get nextElementSibling() { const ch = this.parentElement?.children || []; return ch[ch.indexOf(this) + 1] || null; }
  get id() { return this._attrs.get('id') || ''; }
  set id(v) { this.setAttribute('id', v); }
  get className() { return this._attrs.get('class') || ''; }
  set className(v) { this.setAttribute('class', v); }
  get name() { return this._attrs.get('name') || ''; }
  set name(v) { this.setAttribute('name', v); }
  get type() { return this._attrs.get('type') || ''; }
  get hidden() { return this._attrs.has('hidden'); }
  set hidden(v) { v ? this._attrs.set('hidden', '') : this._attrs.delete('hidden'); }
  get disabled() { return this._attrs.has('disabled'); }
  set disabled(v) { v ? this._attrs.set('disabled', '') : this._attrs.delete('disabled'); }
  get open() { return this._attrs.has('open'); }
  set open(v) { v ? this._attrs.set('open', '') : this._attrs.delete('open'); }
  get value() {
    if (this._val !== undefined) return this._val;
    if (this.tagName === 'SELECT') { const o = this.querySelectorAll('option'), s = o.find((x) => x.hasAttribute('selected')) || o[0]; return s ? (s.hasAttribute('value') ? s.getAttribute('value') : s.textContent) : ''; }
    if (this.tagName === 'TEXTAREA') return this.textContent;
    return this._attrs.get('value') || '';
  }
  set value(v) { this._val = String(v ?? ''); }
  getAttribute(k) { const v = this._attrs.get(String(k).toLowerCase()); return v === undefined ? null : v; }
  setAttribute(k, v) { this._attrs.set(String(k).toLowerCase(), String(v)); }
  hasAttribute(k) { return this._attrs.has(String(k).toLowerCase()); }
  removeAttribute(k) { this._attrs.delete(String(k).toLowerCase()); }
  get textContent() { return this._nodes.map((n) => n.textContent).join(''); }
  set textContent(v) { this._raw = String(v ?? ''); this._nodes = v ? [{ nodeType: 3, textContent: String(v) }] : []; }
  get innerHTML() { return this._raw; }
  set innerHTML(h) {
    if (this.ownerDocument?.activeElement && this.contains(this.ownerDocument.activeElement)) this.ownerDocument.activeElement = null;
    this._raw = String(h ?? ''); this._nodes = parseHTML(this._raw, this.ownerDocument, this);
  }
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
  remove() { this._det(this); }
  focus() {
    if (this.disabled || !this.ownerDocument) return;
    if (FOCUSABLE.has(this.tagName) || this.hasAttribute('tabindex')) this.ownerDocument.activeElement = this;
  }
  scrollIntoView() { this._scrolled++; if (this.ownerDocument) this.ownerDocument._scrolled.push(this); }
  select() {}
  contains(o) { for (let c = o; c; c = c.parentElement) if (c === this) return true; return false; }
  matches(s) { return String(s).split(/,(?=(?:[^"]*"[^"]*")*[^"]*$)(?![^\[]*\])/).some((p) => matchOne(this, p)); }
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
    if (e.bubbles && !e._s && this.ownerDocument) {
      e.currentTarget = this.ownerDocument;
      for (const fn of this.ownerDocument._ls.get(e.type) || []) this.ownerDocument._track?.(fn(e));
      if (!e._s && this.ownerDocument._winLs) {
        e.currentTarget = globalThis;
        for (const fn of this.ownerDocument._winLs.get(e.type) || []) this.ownerDocument._track?.(fn(e));
      }
    }
    return !e.defaultPrevented;
  }
  click() {
    if (this.disabled) return;
    const ok = this.dispatchEvent({ type: 'click', bubbles: true });
    if (!ok) return;
    if (this.tagName === 'BUTTON' && (this.type === 'submit' || (!this.type && this.closest('form')))) {
      const f = this.closest('form'); if (f) f.dispatchEvent({ type: 'submit', bubbles: true, submitter: this });
      return;
    }
    const href = this.tagName === 'A' ? this.getAttribute('href') : null;
    if (href && href.startsWith('#') && globalThis.location) globalThis.location.hash = href;
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
  const doc = {
    activeElement: null, root: null, _ls: new Map(), _winLs: null, _scrolled: [],
    getElementById(id) { return this.root.querySelectorAll('*').find((e) => e.id === id) || null; },
    querySelector(s) { return this.root.querySelector(s); },
    querySelectorAll(s) { return this.root.querySelectorAll(s); },
    createElement(t) { return new El(t, this); },
    addEventListener(t, fn) { (this._ls.get(t) || this._ls.set(t, []).get(t)).push(fn); },
    removeEventListener(t, fn) { const l = this._ls.get(t); if (l) this._ls.set(t, l.filter((x) => x !== fn)); },
  };
  doc.root = new El('document', doc); doc.root._nodes = parseHTML(html, doc, doc.root); return doc;
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

function fakeDaemon({
  projects = [],
  asks = [],
  rows = defaultRows(),
  paired = false,
  offline = false,
  onProjects = null,
} = {}) {
  const projs = projects.map((p) => ({ ...p }));
  const openAsks = asks.map((a) => ({ ...a }));
  const answered = [], fetched = [];
  let hasSession = paired, isOffline = offline, inFlight = 0;
  const reply = (s, o) => ({ ok: s >= 200 && s < 300, status: s, json: async () => o });
  const fetch = async (rawPath, init = {}) => {
    const path = String(rawPath), [urlPath] = path.split('?'), method = init.method || 'GET';
    const body = init.body ? JSON.parse(init.body) : null;
    fetched.push({ method, path: urlPath });
    inFlight++;
    try {
      await Promise.resolve();
      if (isOffline) throw new TypeError('Failed to fetch');
      if (urlPath === '/api/hello') return reply(200, { computer: { id: 'D1', name: 'Test Mac' } });
      if (urlPath === '/api/devices/enroll') return String(body.code || '').replace(/[\s-]+/g, '').toUpperCase() === 'ABCDEFGH' ? reply(200, { device_id: 'dev-1', daemon_id: 'D1' }) : reply(410, { reason: 'code_used', error: 'Invalid or used code.' });
      if (urlPath === '/api/auth/challenge') return reply(200, { nonce: 'N1', daemon_id: 'D1', expires_in: 60 });
      if (urlPath === '/api/auth/session') { hasSession = true; return reply(200, { expires_in: 43200, device: { id: 'dev-1' } }); }
      if (urlPath === '/api/spaces' && method === 'GET') return reply(200, { spaces: [], folders: [], kinds: [], worktrees: true, needs_access: [] });
      if (urlPath === '/api/factory/settings' && method === 'GET') return reply(200, { protocol: 1, rows });
      if (urlPath === '/api/factory/projects' && method === 'GET') {
        if (onProjects) { const res = await onProjects(); if (res) return reply(res.status, res.body); }
        return reply(200, { protocol: 1, projects: projs.map((p) => ({ ...p })) });
      }
      if (urlPath === '/api/factory/asks' && method === 'GET') {
        return reply(200, { protocol: 1, asks: openAsks.filter((a) => a.status !== 'answered').map((a) => ({ ...a })) });
      }
      const am = urlPath.match(/^\/api\/factory\/asks\/([^/]+)\/answer$/);
      if (am && method === 'POST') {
        if (!hasSession) return reply(403, { code: 'proof_required', error: 'proof_required' });
        const id = decodeURIComponent(am[1]), a = openAsks.find((x) => x.id === id);
        if (!a) return reply(404, { error: 'not found' });
        a.status = 'answered';
        answered.push({ id, option: body?.option });
        return reply(200, { ok: true, device_id: 'dev-1' });
      }
      const gm = urlPath.match(/^\/api\/factory\/projects\/([^/]+)\/graph$/);
      if (gm && method === 'GET') {
        const p = projs.find((x) => x.id === decodeURIComponent(gm[1]));
        if (!p) return reply(404, { error: 'not found' });
        if (p.error) return reply(500, { error: p.error });
        return reply(200, { protocol: 1, project: p.id, folder: p.folder || '', tasks: p._tasks || [] });
      }
      const lm = urlPath.match(/^\/api\/factory\/projects\/([^/]+)\/log$/);
      if (lm && method === 'GET') {
        const p = projs.find((x) => x.id === decodeURIComponent(lm[1]));
        if (!p) return reply(404, { error: 'not found' });
        if (p.error) return reply(500, { error: p.error });
        return reply(200, { protocol: 1, project: p.id, lines: p._log || [] });
      }
      const sm = urlPath.match(/^\/api\/factory\/projects\/([^/]+)\/settings$/);
      if (sm && method === 'GET') {
        const p = projs.find((x) => x.id === decodeURIComponent(sm[1]));
        if (!p) return reply(404, { error: 'not found' });
        if (p.error) return reply(500, { error: p.error });
        return reply(200, { protocol: 1, rows: p._rows || rows });
      }
      return reply(404, { error: 'not found' });
    } finally { inFlight--; }
  };
  return {
    fetch, projects: projs, asks: openAsks, answered, fetched, rows,
    get offline() { return isOffline; },
    set offline(v) { isOffline = Boolean(v); },
    get inFlight() { return inFlight; },
  };
}

const realSet = globalThis.setTimeout, realClr = globalThis.clearTimeout, realCrypto = globalThis.crypto;
let bootSeq = 0, activeCleanup = null;

async function bootPage(t, daemon, { width = 1200, hash = '', settle = true } = {}) {
  if (activeCleanup) await activeCleanup();
  const doc = createDoc(readFileSync(new URL('./index.html', import.meta.url), 'utf8')), idb = createIDB();
  const keys = ['document', 'window', 'location', 'indexedDB', 'fetch', 'crypto', 'addEventListener', 'removeEventListener', 'scrollTo', 'innerWidth', 'matchMedia'];
  const prev = Object.fromEntries(keys.map((k) => [k, Object.getOwnPropertyDescriptor(globalThis, k)]));
  const pending = new Set(), timers = new Map(), mqls = [], winLs = new Map();
  const normHash = (v) => { const s = String(v || ''); return !s || s === '#' ? '' : (s.startsWith('#') ? s : '#' + s); };
  let curW = width, curHash = normHash(hash), hashChanges = 0, seq = 1000, disposed = false;
  const px = (n, u) => +n * (u === 'px' ? 1 : 16);
  const evalMQ = (q) => { const mx = q.match(/max-width:\s*([\d.]+)(px|r?em)/), mn = q.match(/min-width:\s*([\d.]+)(px|r?em)/); return (!mx || curW <= px(mx[1], mx[2])) && (!mn || curW >= px(mn[1], mn[2])); };
  const track = (p) => { if (p?.then) { const m = Promise.resolve(p).then(() => {}, () => {}).finally(() => pending.delete(m)); pending.add(m); } return p; };
  if (settle) doc._track = track;
  doc._winLs = winLs;
  const loc = {
    get hash() { return curHash; },
    set hash(v) {
      const next = normHash(v);
      if (next !== curHash) {
        curHash = next;
        hashChanges++;
        track(Promise.resolve().then(() => {
          if (!disposed) for (const fn of winLs.get('hashchange') || []) track(fn({ type: 'hashchange' }));
        }));
      }
    },
    get href() { return 'http://127.0.0.1:7777/factory' + curHash; },
  };
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
  if (settle) await flush();
  else await new Promise((r) => setImmediate(r));
  return {
    doc, flush, location: loc,
    get hashChanges() { return hashChanges; },
    async setHash(h) { loc.hash = h; await flush(); },
    async pollNow() {
      const e = [...timers.entries()].find(([, v]) => v.ms === 3000) || [...timers.entries()].find(([, v]) => v.ms <= 3500);
      if (e) { timers.delete(e[0]); track(e[1].fn()); } else doc.getElementById('retry').click();
      await flush();
    },
  };
}

const isVisible = (el) => {
  if (!el) return false;
  for (let c = el; c; c = c.parentElement) if (c.hidden) return false;
  return true;
};
const cleanLink = (el) => (el?.textContent || '').replace(/[‹«←]/g, '').trim();
const visibleLinks = (root) => root.querySelectorAll('a').filter(isVisible);
const findLink = (root, text) => visibleLinks(root).find((a) => cleanLink(a) === text) || null;
const btnByText = (root, text) => root.querySelectorAll('button').find((b) => isVisible(b) && b.textContent.trim() === text) || null;
const headerNeedsBtn = (doc) => doc.querySelector('header button[aria-expanded]')
  || doc.querySelectorAll('header button').find((b) => b.textContent.includes('needs you:'))
  || null;
const needsList = (doc) => {
  const sec = doc.querySelector('section[aria-label="needs you"]');
  if (sec && isVisible(sec)) return sec;
  const h = doc.querySelectorAll('h2').find((x) => x.textContent.trim() === W.niH && isVisible(x));
  return h ? (h.closest('section') || h.parentElement) : null;
};
const overviewCards = (root) => root.querySelectorAll('a[aria-label], [role="group"][aria-label]').filter((el) => !el.classList.contains('back'));
const linkAskId = (el) => el?.getAttribute('data-ask-id') || el?.getAttribute('data-ask') || '';
const labelOf = (el, doc) => {
  const aria = el.getAttribute('aria-label')?.trim();
  if (aria) return aria;
  const id = el.id;
  if (id) {
    const lbl = doc.querySelector(`label[for="${id}"]`);
    if (lbl?.textContent.trim()) return lbl.textContent.trim();
  }
  const wrap = el.closest('label');
  if (wrap?.textContent.trim()) return wrap.textContent.trim();
  return el.textContent.trim();
};

test('overview.js exports needsByProject, projectName, renderOverviewHTML and renderNeedsYouHTML; needsByProject filters owner asks and ignores missing or unknown projects', async () => {
  const mod = await import('./overview.js');
  for (const fn of ['needsByProject', 'projectName', 'renderOverviewHTML', 'renderNeedsYouHTML']) {
    assert.equal(typeof mod[fn], 'function', `overview.js must export ${fn}`);
  }

  assert.equal(mod.projectName({ id: 'p-1', folder: '/Users/dev/code/notes' }), 'notes');
  assert.equal(mod.projectName({ id: 'p-1', folder: '/Users/dev/code/notes/' }), 'notes', 'trailing slash must be stripped');
  assert.equal(mod.projectName({ id: 'old-importer', folder: '' }), 'old-importer', 'empty folder falls back to id');
  assert.equal(mod.projectName({ id: 'no-folder' }), 'no-folder', 'missing folder falls back to id');

  const projects = [
    mkProj({ id: 'proj-a', folder: '/Users/dev/code/notes' }),
    mkProj({ id: 'proj-b', folder: '/Users/dev/code/bookmarks' }),
  ];
  const asks = [
    mkAsk({ id: 'a-1', project: 'proj-a', question: 'Q1' }),
    mkAsk({ id: 'a-2', project: 'proj-a', question: 'Q2' }),
    mkAsk({ id: 'a-non-gated', project: 'proj-a', gated: false, question: 'Not gated' }),
    mkAsk({ id: 'a-decision', project: 'proj-a', kind: 'decision', question: 'Coordinator decision' }),
    mkAsk({ id: 'a-empty-proj', project: '', question: 'No project string' }),
    mkAsk({ id: 'a-missing-proj', project: undefined, question: 'Missing project field' }),
    mkAsk({ id: 'a-unknown-proj', project: 'proj-ghost', question: 'Unknown project' }),
    mkAsk({ id: 'b-1', project: 'proj-b', question: 'Q3' }),
  ];

  const needs = mod.needsByProject(asks, projects);
  assert.equal(needs instanceof Map, true, 'needsByProject must return a Map');
  assert.deepEqual((needs.get('proj-a') || []).map((a) => a.id), ['a-1', 'a-2'], 'proj-a must keep only owner asks');
  assert.deepEqual((needs.get('proj-b') || []).map((a) => a.id), ['b-1'], 'proj-b must keep its owner ask');
  assert.equal((needs.get('') || []).length, 0, 'empty project asks must be excluded');
  assert.equal((needs.get('proj-ghost') || []).length, 0, 'asks for unlisted projects must be excluded');
  const total = [...needs.values()].reduce((n, list) => n + list.length, 0);
  assert.equal(total, 3, 'total grouped asks must exclude non-owner, empty-project, and unknown-project asks');
});

test('renderOverviewHTML renders card text, aria-labels, progress, needs-you mark, no progress bar, and escapes HTML', async () => {
  const { needsByProject, renderOverviewHTML } = await import('./overview.js');
  const projects = [
    mkProj({ id: 'proj-notes', folder: '/Users/dev/code/notes', goal: 'A notes page with search', state: 'running', counts: { working: 3, ci: 1, queued: 2, done: 2 } }),
    mkProj({ id: 'proj-bookmarks', folder: '/Users/dev/code/bookmarks', goal: 'Import bookmarks from a file', state: 'stopped', running: false, live: false, counts: { queued: 3, done: 3 } }),
    mkProj({ id: 'proj-recipes', folder: '/Users/dev/code/recipes', goal: 'Tags for recipes', state: 'running', counts: {} }),
    mkProj({ id: 'proj-settings', folder: '/Users/dev/code/settings-app', goal: 'Dark theme for the settings page', state: 'finished', running: false, live: false, counts: { done: 6 } }),
  ];
  const asks = [
    mkAsk({ id: 'a1', project: 'proj-notes' }),
    mkSettingAsk({ id: 'a2', project: 'proj-notes' }),
    mkAsk({ id: 'b1', project: 'proj-bookmarks', question: 'Install the csv parser?' }),
  ];
  const needs = needsByProject(asks, projects);
  const html = renderOverviewHTML(projects, needs);
  assert.equal(typeof html, 'string', 'renderOverviewHTML must return an HTML string');
  const doc = createDoc(html);

  assert.equal(doc.querySelector('h1')?.textContent.trim(), W.ovH, 'overview h1 must be "projects"');
  assert.equal(doc.querySelectorAll('progress').length, 0, 'overview cards must not draw a progress bar');
  assert.equal(Boolean(btnByText(doc.root, W.projNew)), true, 'overview must include New project button');

  const cards = overviewCards(doc.root);
  assert.equal(cards.length, 4, 'expected 4 project cards');

  assert.equal(cards[0].tagName, 'A');
  assert.equal(cards[0].getAttribute('href'), '#proj-notes');
  assert.equal(cards[0].getAttribute('aria-label'), 'notes, A notes page with search, running, needs you: 2, 2 of 8 done');
  for (const s of ['notes', 'A notes page with search', 'running', 'needs you: 2', '2 of 8 done']) {
    assert.equal(cards[0].textContent.includes(s), true, `notes card missing "${s}"`);
  }

  assert.equal(cards[1].tagName, 'A');
  assert.equal(cards[1].getAttribute('href'), '#proj-bookmarks');
  assert.equal(cards[1].getAttribute('aria-label'), 'bookmarks, Import bookmarks from a file, stopped, needs you: 1, 3 of 6 done');
  for (const s of ['bookmarks', 'Import bookmarks from a file', 'stopped', 'needs you: 1', '3 of 6 done']) {
    assert.equal(cards[1].textContent.includes(s), true, `bookmarks card missing "${s}"`);
  }

  assert.equal(cards[2].tagName, 'A');
  assert.equal(cards[2].getAttribute('aria-label'), 'recipes, Tags for recipes, running, no tasks yet');
  for (const s of ['recipes', 'Tags for recipes', 'running', W.ovNoTasks]) {
    assert.equal(cards[2].textContent.includes(s), true, `recipes card missing "${s}"`);
  }
  assert.equal(cards[2].textContent.includes('needs you'), false, 'recipes card with 0 asks must omit "needs you"');

  assert.equal(cards[3].tagName, 'A');
  assert.equal(cards[3].getAttribute('aria-label'), 'settings-app, Dark theme for the settings page, finished, 6 of 6 done');
  for (const s of ['settings-app', 'Dark theme for the settings page', 'finished', '6 of 6 done']) {
    assert.equal(cards[3].textContent.includes(s), true, `settings-app card missing "${s}"`);
  }
  assert.equal(cards[3].textContent.includes('needs you'), false, 'finished card with 0 asks must omit "needs you"');

  const xssHtml = renderOverviewHTML([
    mkProj({ id: 'p-xss', folder: '/Users/dev/code/<script>a</script>', goal: '<img src=x onerror=alert(1)>' }),
    { id: 'bad-<x>', error: '<b>broken</b>' },
  ], new Map());
  assert.equal(xssHtml.includes('<script>a</script>'), false, 'overview must escape folder name');
  assert.equal(xssHtml.includes('<img src=x'), false, 'overview must escape goal');
  assert.equal(xssHtml.includes('<b>broken</b>'), false, 'overview must escape project error');
});

test('renderOverviewHTML renders unreadable project card without link, state, or progress, and orders all cards correctly', async () => {
  const { needsByProject, renderOverviewHTML } = await import('./overview.js');
  const errMsg = 'read state.json: unexpected end of JSON input';
  const projects = [
    { id: 'old-importer', error: errMsg },
    mkProj({ id: 'p-fin', folder: '/Users/dev/code/settings-app', goal: 'Finished goal', state: 'finished', running: false, live: false, counts: { done: 4 } }),
    mkProj({ id: 'p-stop', folder: '/Users/dev/code/archive', goal: 'Stopped goal', state: 'stopped', running: false, live: false, counts: { done: 1, queued: 2 } }),
    mkProj({ id: 'p-run-1', folder: '/Users/dev/code/alpha', goal: 'Running one', state: 'running', counts: { working: 1 } }),
    mkProj({ id: 'p-run-2', folder: '/Users/dev/code/beta', goal: 'Running two', state: 'running', counts: {} }),
    mkProj({ id: 'p-need-stop', folder: '/Users/dev/code/bookmarks', goal: 'Stopped with ask', state: 'stopped', running: false, live: false, counts: { done: 3, queued: 3 } }),
    mkProj({ id: 'p-need-run', folder: '/Users/dev/code/notes', goal: 'Running with ask', state: 'running', counts: { done: 2, working: 2 } }),
  ];
  const asks = [
    mkAsk({ id: 'ask-stop', project: 'p-need-stop' }),
    mkAsk({ id: 'ask-run', project: 'p-need-run' }),
  ];
  const html = renderOverviewHTML(projects, needsByProject(asks, projects));
  const doc = createDoc(html);
  const cards = overviewCards(doc.root);

  assert.equal(cards.length, 7, 'all 7 projects must render');
  const order = cards.map((c) => c.getAttribute('aria-label')?.split(',')[0].trim());
  assert.deepEqual(
    order,
    ['bookmarks', 'notes', 'alpha', 'beta', 'archive', 'settings-app', 'old-importer'],
    'order must be: needs you (daemon order), running (daemon order), stopped, finished, unreadable last',
  );

  const badCard = cards[6];
  assert.equal(badCard.tagName === 'A', false, 'unreadable card must not be a link');
  assert.equal(badCard.getAttribute('role'), 'group', 'unreadable card must have role="group"');
  assert.equal(badCard.querySelectorAll('a').length, 0, 'unreadable card must contain no link');
  for (const s of ['old-importer', W.ovErr, W.ovErrHint, errMsg]) {
    assert.equal(badCard.textContent.includes(s), true, `unreadable card missing "${s}"`);
  }
  for (const forbid of ['running', 'stopped', 'finished', 'done', W.ovNoTasks, 'needs you']) {
    assert.equal(badCard.textContent.includes(forbid), false, `unreadable card must not show "${forbid}"`);
  }
  const badAria = badCard.getAttribute('aria-label') || '';
  assert.equal(badAria.includes(`old-importer, ${W.ovErr}`), true, 'unreadable card aria-label must include id and "can\'t read project"');
  assert.equal(badAria.includes(errMsg), true, 'unreadable card aria-label must include daemon error');
});

test('renderNeedsYouHTML hides at 0, formats all three aria-label forms, groups asks by project with ask ids, and escapes HTML', async () => {
  const { needsByProject, renderNeedsYouHTML } = await import('./overview.js');
  const pA = mkProj({ id: 'proj-notes', folder: '/Users/dev/code/notes', goal: 'A notes page with search' });
  const pB = mkProj({ id: 'proj-bookmarks', folder: '/Users/dev/code/bookmarks', goal: 'Import bookmarks from a file' });

  // 0 asks -> absent
  const emptyHTML = renderNeedsYouHTML([pA, pB], needsByProject([], [pA, pB]), false);
  assert.equal(typeof emptyHTML, 'string');
  assert.equal(emptyHTML.trim(), '', 'renderNeedsYouHTML must return empty string at 0 asks');

  // 1 ask in 1 project
  const oneAsk = [mkAsk({ id: 'ask-1', project: 'proj-notes', question: 'Delete the old notes table in the migration?' })];
  const d1 = createDoc(renderNeedsYouHTML([pA, pB], needsByProject(oneAsk, [pA, pB]), false));
  const b1 = d1.querySelector('button');
  assert.equal(Boolean(b1), true, 'expected header button for 1 ask');
  assert.equal(b1.textContent.trim(), 'needs you: 1');
  assert.equal(b1.getAttribute('aria-label'), 'needs you: 1 ask in 1 project');
  assert.equal(b1.getAttribute('aria-expanded'), 'false');
  assert.equal(d1.querySelectorAll('a').length, 0, 'closed list must not render ask links');

  // 2 asks in 1 project
  const twoAsks = [
    ...oneAsk,
    mkSettingAsk({ id: 'ask-2', project: 'proj-notes' }),
  ];
  const d2 = createDoc(renderNeedsYouHTML([pA, pB], needsByProject(twoAsks, [pA, pB]), false));
  const b2 = d2.querySelector('button');
  assert.equal(b2?.textContent.trim(), 'needs you: 2');
  assert.equal(b2?.getAttribute('aria-label'), 'needs you: 2 asks in 1 project');

  // 3 asks in 2 projects, open
  const threeAsks = [
    ...twoAsks,
    mkAsk({ id: 'ask-3', project: 'proj-bookmarks', question: 'Install the csv parser the importer needs?' }),
  ];
  const d3 = createDoc(renderNeedsYouHTML([pA, pB], needsByProject(threeAsks, [pA, pB]), true));
  const b3 = d3.querySelector('button');
  assert.equal(b3?.textContent.trim(), 'needs you: 3');
  assert.equal(b3?.getAttribute('aria-label'), 'needs you: 3 asks in 2 projects');
  assert.equal(b3?.getAttribute('aria-expanded'), 'true');

  assert.equal(d3.querySelector('h2')?.textContent.trim(), W.niH, 'open list heading must be "needs you"');
  const h3s = d3.querySelectorAll('h3').map((h) => h.textContent.replace(/\s+/g, ' ').trim());
  assert.deepEqual(h3s, ['notes · A notes page with search', 'bookmarks · Import bookmarks from a file'], 'list must group asks under project name · goal headings');

  const links = d3.querySelectorAll('a');
  assert.equal(links.length, 3, 'expected 3 ask links in open list');
  assert.deepEqual(links.map(linkAskId), ['ask-1', 'ask-2', 'ask-3'], 'every ask link must carry its ask id');
  assert.deepEqual(
    links.map((a) => a.textContent.trim()),
    [
      'Delete the old notes table in the migration?',
      'This project: review rounds 3 (now 2). Save?',
      'Install the csv parser the importer needs?',
    ],
  );

  // Escaping
  const xssAsk = [mkAsk({ id: 'ask-x', project: 'proj-notes', question: '<script>evil()</script>?' })];
  const xssHTML = renderNeedsYouHTML([pA], needsByProject(xssAsk, [pA]), true);
  assert.equal(xssHTML.includes('<script>evil()</script>'), false, 'renderNeedsYouHTML must escape ask questions');
});

test('asks.js puts tabindex="-1" on .ask-card with question aria-label and keeps existing metadata unchanged', () => {
  const html = renderAskCardsHTML([
    mkAsk({ id: 'ask-t', kind: 'gate', task: 'T6', question: 'Delete the old notes table in the migration?' }),
    mkSettingAsk({ id: 'ask-s', question: 'This project: review rounds 3 (now 2). Save?' }),
  ]);
  const doc = createDoc(html);
  const cards = doc.querySelectorAll('article.ask-card');
  assert.equal(cards.length, 2);

  assert.equal(cards[0].getAttribute('tabindex'), '-1', 'task ask card must carry tabindex="-1"');
  assert.equal(cards[0].getAttribute('aria-label'), 'Delete the old notes table in the migration?');
  assert.equal(cards[0].querySelector('.meta .chip')?.textContent.trim(), 'gate');
  assert.equal(cards[0].querySelector('.meta code')?.textContent.trim(), 'T6');

  assert.equal(cards[1].getAttribute('tabindex'), '-1', 'setting ask card must carry tabindex="-1"');
  assert.equal(cards[1].getAttribute('aria-label'), 'This project: review rounds 3 (now 2). Save?');
  assert.equal(cards[1].querySelector('.meta')?.textContent.trim(), 'setting · the engine asks');
});

test('routing opens empty page for 0 projects, project board for 1 project, and overview for 2+ projects or unknown project hash', async (t) => {
  // 0 projects
  const p0 = await bootPage(t, fakeDaemon({ projects: [] }));
  assert.equal(p0.doc.getElementById('main').textContent.includes(W.emptyH), true, '0 projects must show empty page');
  assert.equal(p0.doc.getElementById('projects').hidden, false, '0 projects must keep #projects card visible');
  assert.equal(p0.doc.getElementById('projects').textContent.includes(W.projNone), true, '0 projects #projects card must say None yet.');
  assert.equal(Boolean(headerNeedsBtn(p0.doc)), false, '0 projects must show no header needs-you button');

  // 1 project
  const one = mkProj({ id: 'proj-only', folder: '/Users/dev/code/notes', goal: 'Only project' });
  const p1 = await bootPage(t, fakeDaemon({ projects: [one] }));
  assert.equal(p1.doc.getElementById('main').querySelector('h1')?.textContent.trim(), 'tasks', '1 project must open its board directly');
  assert.equal(p1.doc.getElementById('main').textContent.includes('Only project'), true);
  assert.equal(Boolean(findLink(p1.doc.root, W.ovAll)), false, '1 project must not show "all projects" link');
  assert.equal(p1.doc.getElementById('projects').hidden, false, '1 project must keep #projects card visible');
  assert.equal(Boolean(btnByText(p1.doc.getElementById('projects'), W.projNew)), true, '1 project #projects card must offer New project');

  // 2 projects, empty hash -> overview
  const two = [
    mkProj({ id: 'proj-a', folder: '/Users/dev/code/notes', goal: 'First project' }),
    mkProj({ id: 'proj-b', folder: '/Users/dev/code/bookmarks', goal: 'Second project', state: 'stopped', running: false, live: false }),
  ];
  const p2 = await bootPage(t, fakeDaemon({ projects: two }));
  assert.equal(p2.doc.getElementById('main').querySelector('h1')?.textContent.trim(), W.ovH, '2 projects with empty hash must open overview');
  assert.equal(overviewCards(p2.doc.getElementById('main')).length, 2, 'overview must show 2 project cards');
  assert.equal(p2.doc.getElementById('aside').hidden, true, 'overview must hide #aside');
  assert.equal(p2.doc.getElementById('asks').hidden, true, 'overview must hide #asks');
  assert.equal(p2.doc.getElementById('tasks-top').hidden, true, 'overview must hide #tasks-top');

  // 2 projects, unknown hash -> overview
  const pUnknown = await bootPage(t, fakeDaemon({ projects: two }), { hash: '#does-not-exist' });
  assert.equal(pUnknown.doc.getElementById('main').querySelector('h1')?.textContent.trim(), W.ovH, 'unknown project hash with 2+ projects must open overview');
  assert.equal(overviewCards(pUnknown.doc.getElementById('main')).length, 2);
});

test('two projects with the same goal line are told apart by name on overview cards, in the needs-you list, and on the project board', async (t) => {
  const projects = [
    mkProj({ id: 'proj-web', folder: '/Users/dev/code/notes-web', goal: 'Add search', counts: { working: 1 } }),
    mkProj({ id: 'proj-ios', folder: '/Users/dev/code/notes-ios', goal: 'Add search', counts: { working: 1 } }),
  ];
  const asks = [
    mkAsk({ id: 'ask-w', project: 'proj-web', question: 'Index web notes locally?' }),
    mkAsk({ id: 'ask-i', project: 'proj-ios', question: 'Index iOS notes with Spotlight?' }),
  ];
  const page = await bootPage(t, fakeDaemon({ projects, asks }));
  const cards = overviewCards(page.doc.getElementById('main'));
  assert.equal(cards.length, 2);
  assert.equal(cards[0].getAttribute('aria-label')?.startsWith('notes-web, Add search,'), true, 'first card must identify notes-web by name');
  assert.equal(cards[1].getAttribute('aria-label')?.startsWith('notes-ios, Add search,'), true, 'second card must identify notes-ios by name');
  assert.equal(cards[0].textContent.includes('notes-web'), true);
  assert.equal(cards[1].textContent.includes('notes-ios'), true);

  headerNeedsBtn(page.doc).click();
  await page.flush();
  const list = needsList(page.doc);
  assert.equal(Boolean(list), true, 'expected open needs-you list');
  const headings = list.querySelectorAll('h3').map((h) => h.textContent.replace(/\s+/g, ' ').trim());
  assert.deepEqual(headings, ['notes-web · Add search', 'notes-ios · Add search'], 'list headings must distinguish projects with the same goal by name');

  await page.setHash('#proj-web');
  const boardMain = page.doc.getElementById('main');
  assert.equal(boardMain.textContent.includes('notes-web'), true, 'project board must show the project name beside the goal');
  assert.equal(boardMain.textContent.includes('Add search'), true);
});

test('header count is the sum over all projects, stays unchanged across views, hides at 0, ignores asks without a project, and behaves across loading and offline', async (t) => {
  const projects = [
    mkProj({ id: 'proj-a', folder: '/Users/dev/code/notes', goal: 'Notes', _tasks: [mkTask({ ref: 'T1' })] }),
    mkProj({ id: 'proj-b', folder: '/Users/dev/code/bookmarks', goal: 'Bookmarks', _tasks: [mkTask({ ref: 'T2' })] }),
  ];
  const asks = [
    mkAsk({ id: 'a1', project: 'proj-a', question: 'Ask A1?' }),
    mkAsk({ id: 'a2', project: 'proj-a', question: 'Ask A2?' }),
    mkAsk({ id: 'b1', project: 'proj-b', question: 'Ask B1?' }),
    mkAsk({ id: 'orphan-empty', project: '', question: 'Orphan empty project?' }),
    mkAsk({ id: 'orphan-missing', project: undefined, question: 'Orphan missing project?' }),
    mkAsk({ id: 'orphan-unknown', project: 'proj-ghost', question: 'Orphan unknown project?' }),
  ];

  // Before first load: no header button
  let releaseLoad;
  const holdLoad = new Promise((r) => { releaseLoad = r; });
  const daemon = fakeDaemon({
    projects,
    asks,
    onProjects: async () => { await holdLoad; return null; },
  });
  const page = await bootPage(t, daemon, { settle: false });
  try {
    assert.equal(page.doc.getElementById('loading').hidden, false, 'loading state must be visible before first load');
    assert.equal(Boolean(headerNeedsBtn(page.doc)), false, 'header button must not appear before first load');
  } finally {
    releaseLoad();
  }
  await page.flush();

  const checkHeader3 = (where) => {
    const b = headerNeedsBtn(page.doc);
    assert.equal(Boolean(b), true, `${where}: expected header needs-you button`);
    assert.equal(b.textContent.trim(), 'needs you: 3', `${where}: header count must be 3 (ignoring orphan asks)`);
    assert.equal(b.getAttribute('aria-label'), 'needs you: 3 asks in 2 projects', `${where}: header aria-label must match 3 asks in 2 projects`);
  };

  checkHeader3('overview');
  await page.setHash('#proj-a');
  checkHeader3('proj-a board');
  await page.setHash('#proj-b');
  checkHeader3('proj-b board');
  await page.setHash('#proj-a/T1');
  checkHeader3('task view');
  await page.setHash('');
  btnByText(page.doc.getElementById('main'), W.projNew).click();
  await page.flush();
  checkHeader3('new-project form');

  // Offline keeps last known asks in header
  daemon.offline = true;
  await page.pollNow();
  assert.equal(page.doc.getElementById('offline').hidden, false, 'offline banner must appear when link is lost');
  checkHeader3('offline');

  // Zero real asks (only orphan & non-owner asks) -> absent in header and on cards
  const zeroPage = await bootPage(t, fakeDaemon({
    projects,
    asks: [
      mkAsk({ id: 'o1', project: '', question: 'No project?' }),
      mkAsk({ id: 'o2', project: undefined, question: 'Undefined project?' }),
      mkAsk({ id: 'o3', project: 'proj-a', kind: 'decision', question: 'Decision only?' }),
    ],
  }));
  assert.equal(Boolean(headerNeedsBtn(zeroPage.doc)), false, 'header button must be absent at 0 owner asks');
  for (const card of overviewCards(zeroPage.doc.getElementById('main'))) {
    assert.equal(card.textContent.includes('needs you'), false, 'overview card must omit needs you at 0');
  }
});

test("project A's setting ask is in A's ask cards and A's settings row and in neither on B's page, where asks reads No open asks right now.", async (t) => {
  const projects = [
    mkProj({ id: 'proj-a', folder: '/Users/dev/code/notes', goal: 'Notes project' }),
    mkProj({ id: 'proj-b', folder: '/Users/dev/code/bookmarks', goal: 'Bookmarks project' }),
  ];
  const settingAsk = mkSettingAsk({ id: 'ask-set-a', project: 'proj-a' });
  const daemon = fakeDaemon({ projects, asks: [settingAsk] });

  // Project A's page
  const page = await bootPage(t, daemon, { hash: '#proj-a' });
  const asksA = page.doc.getElementById('asks');
  const setRowA = page.doc.getElementById('settings').querySelector('[data-row="review_rounds"]');
  assert.equal(Boolean(asksA.querySelector('article.ask-card[data-ask-id="ask-set-a"]')), true, "A's setting ask must appear in A's asks card");
  assert.equal(asksA.textContent.includes('1 open'), true, "A's asks card must count its 1 open ask");
  assert.equal(Boolean(setRowA), true, "A's settings must have review_rounds row");
  assert.equal(setRowA.textContent.includes(settingAsk.question), true, "A's settings row must show A's read-back question");
  assert.equal(Boolean(setRowA.querySelector('button[data-ask-id="ask-set-a"][data-option="Save"]')), true, "A's settings row must show Save button");
  assert.equal(Boolean(setRowA.querySelector('button[data-ask-id="ask-set-a"][data-option="Keep current"]')), true, "A's settings row must show Keep current button");

  // Project B's page
  await page.setHash('#proj-b');
  const asksB = page.doc.getElementById('asks');
  const setRowB = page.doc.getElementById('settings').querySelector('[data-row="review_rounds"]');
  assert.equal(asksB.textContent.includes(W.niNone), true, "B's asks card must read 'No open asks right now.' while A has an open ask");
  assert.equal(asksB.querySelectorAll('article.ask-card').length, 0, "B's asks card must list no ask cards");
  assert.equal(asksB.textContent.includes('1 open'), false, "B's asks card must not show A's open count");
  assert.equal(Boolean(setRowB), true, "B's settings must have review_rounds row");
  assert.equal(setRowB.textContent.includes(settingAsk.question), false, "B's settings row must not show A's setting question");
  assert.equal(setRowB.textContent.includes('Waiting for your answer'), false, "B's settings row must not show A's read-back status");
  assert.equal(Boolean(setRowB.querySelector('button[data-ask-id="ask-set-a"]')), false, "B's settings row must not show Save / Keep current for A's ask");

  // Header still counts A's ask on B's page
  const btn = headerNeedsBtn(page.doc);
  assert.equal(btn?.textContent.trim(), 'needs you: 1', "header must still count A's ask on B's page");
  assert.equal(btn?.getAttribute('aria-label'), 'needs you: 1 ask in 1 project');
});

test("activating an ask link closes the list and focuses that ask's card from another project, task view, new-project form, and the shown board (no hashchange only on the shown board)", async (t) => {
  const projects = [
    mkProj({ id: 'proj-a', folder: '/Users/dev/code/notes', goal: 'Notes project', _tasks: [mkTask({ ref: 'T1' })] }),
    mkProj({ id: 'proj-b', folder: '/Users/dev/code/bookmarks', goal: 'Bookmarks project', _tasks: [mkTask({ ref: 'T2' })] }),
  ];
  const asks = [
    mkAsk({ id: 'ask-a1', project: 'proj-a', question: 'First question on A?' }),
    mkAsk({ id: 'ask-a2', project: 'proj-a', question: 'Second question on A?' }),
    mkAsk({ id: 'ask-b1', project: 'proj-b', question: 'Question on B?' }),
  ];

  const openListAndClick = async (page, askId) => {
    const btn = headerNeedsBtn(page.doc);
    assert.equal(Boolean(btn), true, 'expected header needs-you button');
    btn.click();
    await page.flush();
    const list = needsList(page.doc);
    assert.equal(Boolean(list), true, 'expected open needs-you list');
    const link = list.querySelectorAll('a').find((a) => linkAskId(a) === askId) || null;
    assert.equal(Boolean(link), true, `expected ask link for ${askId}`);
    link.click();
    await page.flush();
  };

  const assertCardFocused = (page, askId, label) => {
    assert.equal(headerNeedsBtn(page.doc)?.getAttribute('aria-expanded'), 'false', `${label}: list button must be collapsed`);
    assert.equal(Boolean(needsList(page.doc)), false, `${label}: open list must close`);
    assert.equal(page.doc.getElementById('asks').hidden, false, `${label}: #asks must be visible on project board`);
    const card = page.doc.getElementById('asks').querySelector(`article.ask-card[data-ask-id="${askId}"]`);
    assert.equal(Boolean(card), true, `${label}: expected ask card ${askId} in #asks`);
    assert.equal(card.getAttribute('tabindex'), '-1', `${label}: ask card must have tabindex="-1"`);
    assert.equal(page.doc.activeElement === card, true, `${label}: focus must land on ask card ${askId}`);
    assert.equal(card._scrolled > 0, true, `${label}: ask card ${askId} must be scrolled into view`);
  };

  // 1. From another project (#proj-b -> ask-a2 in proj-a)
  const daemon = fakeDaemon({ projects, asks });
  const page = await bootPage(t, daemon, { hash: '#proj-b' });
  const hc1 = page.hashChanges;
  await openListAndClick(page, 'ask-a2');
  assert.equal(page.location.hash, '#proj-a', 'another project: hash must move to #proj-a');
  assert.equal(page.hashChanges > hc1, true, 'another project: hashchange must occur');
  assertCardFocused(page, 'ask-a2', 'another project');

  // 2. From shown project's task view (#proj-a/T1 -> ask-a2 in proj-a)
  await page.setHash('#proj-a/T1');
  assert.equal(page.doc.getElementById('asks').hidden, true, 'task view hides #asks');
  const hc2 = page.hashChanges;
  await openListAndClick(page, 'ask-a2');
  assert.equal(page.location.hash, '#proj-a', 'task view: hash must return to #proj-a board');
  assert.equal(page.hashChanges > hc2, true, 'task view: hashchange must occur');
  assertCardFocused(page, 'ask-a2', 'task view');

  // 3. From new-project form (opened from overview at "" -> ask-a2 in proj-a)
  await page.setHash('');
  btnByText(page.doc.getElementById('main'), W.projNew).click();
  await page.flush();
  assert.equal(page.doc.getElementById('main').querySelector('h1')?.textContent.trim(), W.npH, 'new-project form must be open');
  const hc3 = page.hashChanges;
  await openListAndClick(page, 'ask-a2');
  assert.equal(page.location.hash, '#proj-a', 'new-project form: hash must move to #proj-a');
  assert.equal(page.hashChanges > hc3, true, 'new-project form from overview: hashchange must occur');
  assert.equal(page.doc.getElementById('main').querySelector('h1')?.textContent.trim(), 'tasks', 'new-project form must close into board');
  assertCardFocused(page, 'ask-a2', 'new-project form');

  // 4a. Already on proj-a's board (#proj-a, 2 projects): NO hashchange and NO reload fetch
  const hc4 = page.hashChanges;
  const fetches4 = daemon.fetched.length;
  page.doc.getElementById('asks').querySelector('article.ask-card[data-ask-id="ask-a1"]')._scrolled = 0;
  await openListAndClick(page, 'ask-a1');
  assert.equal(page.hashChanges, hc4, 'shown board (#proj-a): must not fire hashchange');
  assert.equal(daemon.fetched.length, fetches4, 'shown board (#proj-a): must not reload via fetch');
  assertCardFocused(page, 'ask-a1', 'shown board (#proj-a)');

  // 4b. Single-project board at empty hash ("", 1 project): still NO hashchange and NO reload fetch
  const singleDaemon = fakeDaemon({
    projects: [projects[0]],
    asks: [asks[0], asks[1]],
  });
  const singlePage = await bootPage(t, singleDaemon, { hash: '' });
  const hcSingle = singlePage.hashChanges;
  const fetchesSingle = singleDaemon.fetched.length;
  await openListAndClick(singlePage, 'ask-a2');
  assert.equal(singlePage.hashChanges, hcSingle, 'single-project board at "": must not fire hashchange');
  assert.equal(singleDaemon.fetched.length, fetchesSingle, 'single-project board at "": must not reload via fetch');
  assertCardFocused(singlePage, 'ask-a2', 'single-project board at ""');

  // 3b. New-project form opened from 1-project board (at "") -> ask-a1 closes form and focuses card
  btnByText(singlePage.doc.getElementById('projects'), W.projNew).click();
  await singlePage.flush();
  assert.equal(singlePage.doc.getElementById('main').querySelector('h1')?.textContent.trim(), W.npH);
  const hcSingleForm = singlePage.hashChanges;
  await openListAndClick(singlePage, 'ask-a1');
  assert.equal(singlePage.hashChanges > hcSingleForm, true, 'new-project form at "": navigating to #proj-a triggers hashchange');
  assert.equal(singlePage.doc.getElementById('main').querySelector('h1')?.textContent.trim(), 'tasks');
  assertCardFocused(singlePage, 'ask-a1', 'new-project form on single project');
});

test('polling keeps focus on the second ask link of one project while list is open, preserves .ask-card focus, closes on Escape, and resets cleanly when the last ask resolves', async (t) => {
  const projects = [
    mkProj({ id: 'proj-a', folder: '/Users/dev/code/notes', goal: 'Notes project' }),
    mkProj({ id: 'proj-b', folder: '/Users/dev/code/bookmarks', goal: 'Bookmarks project' }),
  ];
  const daemon = fakeDaemon({
    projects,
    asks: [
      mkAsk({ id: 'ask-a1', project: 'proj-a', question: 'First question on A?' }),
      mkAsk({ id: 'ask-a2', project: 'proj-a', question: 'Second question on A?' }),
    ],
  });
  const page = await bootPage(t, daemon);

  // Open list and focus the 2nd ask link of proj-a
  headerNeedsBtn(page.doc).click();
  await page.flush();
  const linksBefore = needsList(page.doc).querySelectorAll('a');
  assert.equal(linksBefore.length, 2);
  linksBefore[1].focus();
  assert.equal(linkAskId(page.doc.activeElement), 'ask-a2');

  // Add a 3rd ask on proj-b so the list HTML changes and set() re-renders on poll
  daemon.asks.push(mkAsk({ id: 'ask-b1', project: 'proj-b', question: 'New question on B?' }));
  await page.pollNow();
  assert.equal(headerNeedsBtn(page.doc)?.getAttribute('aria-expanded'), 'true', 'list must stay open across poll');
  assert.equal(needsList(page.doc)?.contains(page.doc.activeElement), true, 'focus must remain on a live link inside the open list after poll');
  assert.equal(linkAskId(page.doc.activeElement), 'ask-a2', 'focus must stay on the second ask (ask-a2), not jump to ask-a1');

  // Escape closes the open list and returns focus to the header button
  page.doc.activeElement.dispatchEvent({ type: 'keydown', key: 'Escape', bubbles: true });
  await page.flush();
  assert.equal(Boolean(needsList(page.doc)), false, 'Escape must close the needs-you list');
  assert.equal(headerNeedsBtn(page.doc)?.getAttribute('aria-expanded'), 'false', 'Escape must set aria-expanded="false"');
  assert.equal(page.doc.activeElement === headerNeedsBtn(page.doc), true, 'Escape must return focus to the header needs-you button');

  // Focus on .ask-card survives a poll that updates #asks HTML
  headerNeedsBtn(page.doc).click();
  await page.flush();
  needsList(page.doc).querySelectorAll('a').find((a) => linkAskId(a) === 'ask-a2').click();
  await page.flush();
  assert.equal(page.doc.activeElement?.dataset?.askId, 'ask-a2');
  daemon.asks.push(mkAsk({ id: 'ask-a3', project: 'proj-a', question: 'Third question on A?' }));
  await page.pollNow();
  assert.equal(page.doc.getElementById('asks').contains(page.doc.activeElement), true, 'focus must stay on a live .ask-card inside #asks across polls');
  assert.equal(page.doc.activeElement?.tagName, 'ARTICLE');
  assert.equal(page.doc.activeElement?.dataset?.askId, 'ask-a2', 'focus must stay on ask-a2 .ask-card across polls');

  // Poll resolves the last open ask while the list is open: resets to closed, moves focus cleanly, does not reopen on a later ask
  headerNeedsBtn(page.doc).click();
  await page.flush();
  needsList(page.doc).querySelector('a').focus();
  for (const a of daemon.asks) a.status = 'answered';
  await page.pollNow();
  assert.equal(Boolean(headerNeedsBtn(page.doc)), false, 'header button must vanish when last ask resolves');
  assert.equal(Boolean(needsList(page.doc)), false, 'open list must vanish when last ask resolves');
  assert.equal(Boolean(page.doc.activeElement) && page.doc.root.contains(page.doc.activeElement), true, 'focus must move cleanly onto a live control instead of being stranded');

  daemon.asks.push(mkAsk({ id: 'ask-later', project: 'proj-a', question: 'Later question?' }));
  await page.pollNow();
  assert.equal(headerNeedsBtn(page.doc)?.getAttribute('aria-expanded'), 'false', 'list must not auto-reopen when a later ask arrives');
  assert.equal(Boolean(needsList(page.doc)), false, 'list must stay closed when a later ask arrives');
});

test('answering an ask lowers the header count on the next poll and removes the header button at 0', async (t) => {
  const projects = [
    mkProj({ id: 'proj-a', folder: '/Users/dev/code/notes', goal: 'Notes project' }),
    mkProj({ id: 'proj-b', folder: '/Users/dev/code/bookmarks', goal: 'Bookmarks project' }),
  ];
  const daemon = fakeDaemon({
    projects,
    asks: [
      mkAsk({ id: 'ask-a1', project: 'proj-a', question: 'Question A1?' }),
      mkAsk({ id: 'ask-a2', project: 'proj-a', question: 'Question A2?' }),
      mkAsk({ id: 'ask-b1', project: 'proj-b', question: 'Question B1?' }),
    ],
    paired: true,
  });
  const page = await bootPage(t, daemon, { hash: '#proj-a' });
  assert.equal(headerNeedsBtn(page.doc)?.textContent.trim(), 'needs you: 3');

  // Answer first ask on proj-a
  const opt1 = page.doc.getElementById('asks').querySelector('button[data-ask-id="ask-a1"][data-option="Keep it"]');
  assert.equal(Boolean(opt1), true);
  opt1.click();
  await page.flush();
  assert.equal(headerNeedsBtn(page.doc)?.textContent.trim(), 'needs you: 2', 'header count must drop to 2 after answering ask-a1');
  assert.equal(headerNeedsBtn(page.doc)?.getAttribute('aria-label'), 'needs you: 2 asks in 2 projects');

  // Answer second ask on proj-a
  const opt2 = page.doc.getElementById('asks').querySelector('button[data-ask-id="ask-a2"][data-option="Keep it"]');
  assert.equal(Boolean(opt2), true);
  opt2.click();
  await page.flush();
  assert.equal(headerNeedsBtn(page.doc)?.textContent.trim(), 'needs you: 1', 'header count must drop to 1 after answering ask-a2');
  assert.equal(headerNeedsBtn(page.doc)?.getAttribute('aria-label'), 'needs you: 1 ask in 1 project');
  assert.equal(page.doc.getElementById('asks').textContent.includes(W.niNone), true);

  // Answer last ask on proj-b -> header button disappears
  await page.setHash('#proj-b');
  const opt3 = page.doc.getElementById('asks').querySelector('button[data-ask-id="ask-b1"][data-option="Keep it"]');
  assert.equal(Boolean(opt3), true);
  opt3.click();
  await page.flush();
  assert.equal(Boolean(headerNeedsBtn(page.doc)), false, 'header button must disappear when last ask is answered');
});

test('back links and projects card follow the one-back-link rule across board, task view, new-project form, and unreadable bookmark', async (t) => {
  // 0 projects: new-project form has "board", never "all projects"
  const p0 = await bootPage(t, fakeDaemon({ projects: [] }));
  btnByText(p0.doc.getElementById('main'), W.projNew).click();
  await p0.flush();
  assert.equal(Boolean(findLink(p0.doc.getElementById('main'), 'board')), true, '0 projects form must have "board" back link');
  assert.equal(Boolean(findLink(p0.doc.getElementById('main'), W.ovAll)), false, '0 projects form must not have "all projects"');

  // 1 project: board has no back link & keeps #projects; task view and form have "board"
  const one = mkProj({ id: 'proj-one', folder: '/Users/dev/code/notes', goal: 'Single project', _tasks: [mkTask({ ref: 'T1' })] });
  const p1 = await bootPage(t, fakeDaemon({ projects: [one] }));
  assert.equal(Boolean(findLink(p1.doc.root, W.ovAll)), false, '1 project board must not have "all projects"');
  assert.equal(Boolean(findLink(p1.doc.root, 'board')), false, '1 project board must not have "board" back link');
  assert.equal(p1.doc.getElementById('projects').hidden, false, '1 project board must keep #projects visible');

  await p1.setHash('#proj-one/T1');
  assert.equal(Boolean(findLink(p1.doc.getElementById('main'), 'board')), true, '1 project task view must have "board"');
  assert.equal(Boolean(findLink(p1.doc.getElementById('main'), W.ovAll)), false, '1 project task view must not have "all projects"');

  await p1.setHash('#proj-one');
  btnByText(p1.doc.getElementById('projects'), W.projNew).click();
  await p1.flush();
  assert.equal(Boolean(findLink(p1.doc.getElementById('main'), 'board')), true, '1 project form must have "board"');
  assert.equal(Boolean(findLink(p1.doc.getElementById('main'), W.ovAll)), false, '1 project form must not have "all projects"');

  // 2+ projects (including an unreadable project)
  const many = [
    mkProj({ id: 'proj-a', folder: '/Users/dev/code/notes', goal: 'Notes', _tasks: [mkTask({ ref: 'T1' })] }),
    mkProj({ id: 'proj-b', folder: '/Users/dev/code/bookmarks', goal: 'Bookmarks' }),
    { id: 'old-importer', error: 'read state.json: unexpected end of JSON input' },
  ];
  const pMany = await bootPage(t, fakeDaemon({ projects: many }), { hash: '#proj-a' });

  // Board: "all projects", no "board", #projects hidden
  assert.equal(Boolean(findLink(pMany.doc.getElementById('board'), W.ovAll)), true, '2+ projects board must show "all projects"');
  assert.equal(Boolean(findLink(pMany.doc.getElementById('board'), 'board')), false, '2+ projects board must not show "board"');
  assert.equal(pMany.doc.getElementById('projects').hidden, true, '2+ projects board must hide #projects card');

  // Task view: "board" only, never "all projects"
  await pMany.setHash('#proj-a/T1');
  assert.equal(Boolean(findLink(pMany.doc.getElementById('main'), 'board')), true, '2+ projects task view must keep "board"');
  assert.equal(Boolean(findLink(pMany.doc.getElementById('main'), W.ovAll)), false, '2+ projects task view must not show "all projects"');

  // Direct bookmark to unreadable project: "all projects" above "can't show the factory"
  await pMany.setHash('#old-importer');
  const badMain = pMany.doc.getElementById('main');
  assert.equal(Boolean(findLink(badMain, W.ovAll)), true, 'unreadable bookmark with 2+ projects must show "all projects"');
  assert.equal(badMain.textContent.includes(W.cantShow), true, 'unreadable bookmark must show "can\'t show the factory"');

  // Clicking "all projects" returns to overview
  findLink(badMain, W.ovAll).click();
  await pMany.flush();
  assert.equal(pMany.doc.getElementById('main').querySelector('h1')?.textContent.trim(), W.ovH, 'clicking "all projects" must open overview');
  assert.equal(Boolean(findLink(pMany.doc.getElementById('main'), W.ovAll)), false, 'overview must have no "all projects" back link');

  // New-project form opened from overview: "all projects" only, hides #aside/#asks/#tasks-top, returns to overview
  btnByText(pMany.doc.getElementById('main'), W.projNew).click();
  await pMany.flush();
  assert.equal(pMany.doc.getElementById('main').querySelector('h1')?.textContent.trim(), W.npH);
  assert.equal(Boolean(findLink(pMany.doc.getElementById('main'), W.ovAll)), true, '2+ projects form must show "all projects"');
  assert.equal(Boolean(findLink(pMany.doc.getElementById('main'), 'board')), false, '2+ projects form must not show "board"');
  assert.equal(pMany.doc.getElementById('aside').hidden, true, 'form opened from overview must hide #aside');
  assert.equal(pMany.doc.getElementById('asks').hidden, true, 'form opened from overview must hide #asks');
  assert.equal(pMany.doc.getElementById('tasks-top').hidden, true, 'form opened from overview must hide #tasks-top');

  findLink(pMany.doc.getElementById('main'), W.ovAll).click();
  await pMany.flush();
  assert.equal(pMany.doc.getElementById('main').querySelector('h1')?.textContent.trim(), W.ovH, 'clicking "all projects" on form must return to overview');
});

test('every new control has a label, header button carries aria-expanded, no rule id or setting key appears in the HTML, and app.js holds no POST', async (t) => {
  const projects = [
    mkProj({ id: 'proj-a', folder: '/Users/dev/code/notes', goal: 'A notes page with search' }),
    mkProj({ id: 'proj-b', folder: '/Users/dev/code/bookmarks', goal: 'Import bookmarks from a file', state: 'stopped', running: false, live: false }),
    { id: 'old-importer', error: 'read state.json: unexpected end of JSON input' },
  ];
  const asks = [
    mkAsk({ id: 'ask-1', project: 'proj-a', question: 'Delete the old notes table in the migration?' }),
    mkSettingAsk({ id: 'ask-2', project: 'proj-a' }),
    mkAsk({ id: 'ask-3', project: 'proj-b', question: 'Install the csv parser the importer needs?' }),
  ];

  const page = await bootPage(t, fakeDaemon({ projects, asks }));
  const btn = headerNeedsBtn(page.doc);
  assert.equal(Boolean(btn), true, 'expected header needs-you button');
  assert.equal(btn.getAttribute('aria-expanded'), 'false', 'header button must carry aria-expanded="false" when closed');

  btn.click();
  await page.flush();
  assert.equal(btn.getAttribute('aria-expanded'), 'true', 'header button must carry aria-expanded="true" when open');

  const list = needsList(page.doc);
  assert.equal(Boolean(list), true, 'expected open needs-you list');
  const allEls = page.doc.root.querySelectorAll('*');
  assert.equal(allEls.indexOf(btn) < allEls.indexOf(list), true, 'open list must follow the header button in DOM order');
  assert.equal(allEls.indexOf(list) < allEls.indexOf(page.doc.getElementById('board')), true, 'open list must precede #board in DOM order');
  assert.equal(page.doc.getElementById('board').contains(list), false, 'open list must sit under the header, not inside #board');

  // Every visible interactive control has a non-empty accessible label
  const controls = page.doc.root.querySelectorAll('a, button, input, select, textarea').filter(isVisible);
  assert.equal(controls.length > 0, true);
  for (const ctrl of controls) {
    assert.equal(labelOf(ctrl, page.doc).length > 0, true, `every visible control (${ctrl.tagName}) must have a label`);
  }

  // No rule ids and no raw setting keys in overview / needs-you HTML or visible text
  const { needsByProject, renderOverviewHTML, renderNeedsYouHTML } = await import('./overview.js');
  const needs = needsByProject(asks, projects);
  const combinedHTML = renderOverviewHTML(projects, needs) + '\n' + renderNeedsYouHTML(projects, needs, true);
  const visibleText = page.doc.querySelector('header').textContent + '\n' + list.textContent + '\n' + page.doc.getElementById('main').textContent;
  const ruleRe = /\b(?:SE\d+|N\d+|A[1-5]|J\d+)\b/;
  const keyRe = /\b(?:max_running|review_rounds|max_rounds|ci_stalled_minutes|second_reviewer|design_gate|dispatch_workflow)\b|models\.(?:coordinator|worker|reviewer|retro|plan_reviewer)/;
  assert.doesNotMatch(combinedHTML, ruleRe, 'overview/needs-you HTML must contain no rule ids');
  assert.doesNotMatch(combinedHTML, keyRe, 'overview/needs-you HTML must contain no setting keys');
  assert.doesNotMatch(visibleText, ruleRe, 'overview/needs-you text must contain no rule ids');
  assert.doesNotMatch(visibleText, keyRe, 'overview/needs-you text must contain no setting keys');

  // Narrow viewport (360 px): overview hides #aside/#asks/#tasks-top; inside a project keeps #projects hidden and places #asks before #main
  const narrow = await bootPage(t, fakeDaemon({ projects, asks }), { width: 360 });
  assert.equal(narrow.doc.getElementById('aside').hidden, true, 'narrow overview must hide #aside');
  assert.equal(narrow.doc.getElementById('asks').hidden, true, 'narrow overview must hide #asks');
  assert.equal(narrow.doc.getElementById('tasks-top').hidden, true, 'narrow overview must hide #tasks-top');
  await narrow.setHash('#proj-a');
  assert.equal(narrow.doc.getElementById('projects').hidden, true, 'narrow 2+ project board must hide #projects');
  assert.equal(narrow.doc.getElementById('tasks-top').hidden, false, 'narrow project board must show #tasks-top');
  assert.equal(Boolean(findLink(narrow.doc.getElementById('board'), W.ovAll)), true, 'narrow project board must show "all projects"');

  // app.js still holds no POST
  const appSrc = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
  assert.equal(/\bPOST\b|method\s*:/.test(appSrc), false, 'app.js must hold no POST');
});
