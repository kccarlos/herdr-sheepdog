// Failing tests for PR 14.6: /factory project actions (stop, resume, new goal,
// reopen), project state words, in-bar pairing, and task detail rule fold (`make test-web`).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const W = {
  region: 'This project',
  stop: 'Stop', stopAria: 'Stop this project',
  newGoal: 'New goal',
  resume: 'Resume', resumeAria: 'Resume this project',
  reopen: 'Reopen with a new goal',
  stoppedP: 'Stopped. No agent is running. Resume goes on with the same goal.',
  finP: 'Finished. To go on, reopen it with a new goal.',
  finLive: 'Finished. An agent is still running.',
  stopQ: 'Stop this project?',
  stopWhat: 'Its running agents end now. Resume goes on with the same goal.',
  stopWhatFin: 'Its last running agent ends now. The project stays finished.',
  stopYes: 'Stop the project',
  stopNo: 'Keep it running',
  ngLabel: 'new goal',
  ngHint: 'The folder and the settings stay as they are. The coordinator plans the new goal.',
  ngHintFin: 'The folder and the settings stay as they are. To change a model first, use settings below.',
  ngGoRun: 'Give it this goal',
  ngGoStop: 'Start with this goal',
  ngGoFin: 'Reopen with this goal',
  ngCancel: 'Cancel',
  ngSame: 'This project is finished. Give it a new goal.',
  refResume: 'Not started: a start check failed. Fix it, then press Resume again.',
  refNewGoal: 'Not started: a start check failed. Fix it, then press New goal again.',
  refReopen: 'Not started: a start check failed. Fix it, then press Reopen again.',
  refSet: 'Not started: a setting is not allowed. Change it in settings below.',
  refModels: 'Is the failed check for a CLI you do not use? Change the models in settings below.',
  fixCmd: 'Fix, in a terminal:',
  tkDetails: 'details',
  pairH: 'pair this browser',
};

const mkProj = (o = {}) => ({
  id: 'proj-demo',
  folder: '/Users/dev/code/notes',
  repo_url: 'https://github.com/owner/notes',
  goal: 'A notes page with search',
  state: 'running',
  running: true,
  live: true,
  counts: { working: 1 },
  ...o,
});

const mkTask = (o = {}) => ({
  ref: 'T1',
  title: 'Search box on the notes page',
  stage: 'working',
  why_waiting: 'In review round 2',
  layer: 2,
  rounds: 1,
  model: 'claude sonnet',
  terminal: 't-41',
  last_rule: 'N22',
  last_decision: 'started reviewer',
  after: ['T0'],
  dependents: ['T2'],
  ...o,
});

const baseRows = () => [
  { name: 'max_running', label: 'max running', value: '3', default: '3', min: 1, max: 20, top: true },
  { name: 'review_rounds', label: 'review rounds', value: '2', default: '2', min: 1, max: 5 },
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
    if (this.tagName === 'TEXTAREA') return this.textContent;
    if (this.tagName === 'SELECT') { const o = this.querySelectorAll('option'), s = o.find((x) => x.hasAttribute('selected')) || o[0]; return s ? (s.hasAttribute('value') ? s.getAttribute('value') : s.textContent) : ''; }
    return this._attrs.get('value') || '';
  }
  set value(v) { this._val = String(v ?? ''); }
  getAttribute(k) { const v = this._attrs.get(String(k).toLowerCase()); return v === undefined ? null : v; }
  setAttribute(k, v) { this._attrs.set(String(k).toLowerCase(), String(v)); }
  hasAttribute(k) { return this._attrs.has(String(k).toLowerCase()); } removeAttribute(k) { this._attrs.delete(String(k).toLowerCase()); }
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
  focus() { if (!this.disabled && this.ownerDocument) this.ownerDocument.activeElement = this; }
  scrollIntoView() {} select() {}
  contains(o) { for (let c = o; c; c = c.parentElement) if (c === this) return true; return false; }
  matches(s) { return String(s).split(',').some((p) => matchOne(this, p)); }
  closest(s) { for (let c = this; c; c = c.parentElement) if (c.nodeType === 1 && c.matches(s)) return c; return null; }
  querySelectorAll(s) { const out = [], w = (n) => { for (const c of n.children) { if (c.matches(s)) out.push(c); w(c); } }; w(this); return out; }
  querySelector(s) { return this.querySelectorAll(s)[0] || null; }
  checkValidity() {
    if (this.hasAttribute('required') && this.value === '') return false;
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
    if (ok && this.tagName === 'BUTTON' && (!this.type || this.type === 'submit')) {
      const form = this.closest('form');
      if (form) form.dispatchEvent({ type: 'submit', bubbles: true, submitter: this });
    }
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
    activeElement: null, root: null, _ls: new Map(), _winLs: null,
    getElementById(id) { return this.root.querySelectorAll('*').find((e) => e.id === id) || null; },
    querySelector(s) { return this.root.querySelector(s); },
    querySelectorAll(s) { return this.root.querySelectorAll(s); },
    createElement(t) { return new El(t, this); },
    addEventListener(t, fn) { (this._ls.get(t) || this._ls.set(t, []).get(t)).push(fn); },
    removeEventListener(t, fn) { const l = this._ls.get(t); if (l) this._ls.set(t, l.filter((x) => x !== fn)); },
  };
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

function fakeDaemon({ projects = [mkProj()], tasks = [mkTask()], asks = [], rows = baseRows(), paired = false, onStart = null, onStop = null } = {}) {
  const list = projects.map((p) => ({ ...p }));
  const started = [], stopped = [], openAsks = asks.map((a) => ({ ...a }));
  let hasSession = paired, inFlight = 0;
  const reply = (s, o) => ({ ok: s >= 200 && s < 300, status: s, json: async () => o });
  const fetch = async (path, init = {}) => {
    const method = init.method || 'GET', body = init.body ? JSON.parse(init.body) : null; inFlight++;
    try {
      await Promise.resolve();
      if (path === '/api/hello') return reply(200, { computer: { id: 'D1', name: 'Test Mac' } });
      if (path === '/api/devices/enroll') return String(body.code || '').replace(/[\s-]+/g, '').toUpperCase() === 'ABCDEFGH' ? reply(200, { device_id: 'dev-1', daemon_id: 'D1' }) : reply(410, { reason: 'code_used', error: 'Invalid or used code.' });
      if (path === '/api/auth/challenge') return reply(200, { nonce: 'N1', daemon_id: 'D1', expires_in: 60 });
      if (path === '/api/auth/session') { hasSession = true; return reply(200, { expires_in: 43200, device: { id: 'dev-1' } }); }
      if (path === '/api/factory/projects' && method === 'GET') return reply(200, { protocol: 1, projects: list.map((p) => ({ ...p })) });
      if (path === '/api/factory/asks' && method === 'GET') return reply(200, { protocol: 1, asks: openAsks.filter((a) => a.status === 'open') });
      if (path === '/api/factory/projects/start' && method === 'POST') {
        if (!hasSession) return reply(403, { code: 'proof_required', error: 'proof_required' });
        started.push(body);
        if (onStart) { const res = onStart(body, list); if (res) return reply(res.status, res.body); }
        const pr = list.find((p) => p.folder === body.folder || p.id === body.project);
        if (pr && pr.state === 'finished' && String(body.goal || '').trim() === String(pr.goal || '').trim()) {
          return reply(409, { code: 'already_closed', error: W.ngSame });
        }
        if (pr) { pr.state = 'running'; pr.running = true; pr.live = true; if (body.goal !== undefined) pr.goal = body.goal; }
        return reply(200, { ok: true, device_id: 'dev-1' });
      }
      if (path === '/api/factory/projects/stop' && method === 'POST') {
        if (!hasSession) return reply(403, { code: 'proof_required', error: 'proof_required' });
        stopped.push(body);
        if (onStop) { const res = onStop(body, list); if (res) return reply(res.status, res.body); }
        const pr = list.find((p) => p.id === body.project || p.folder === body.project);
        if (!pr) return reply(404, { code: 'not_found', error: `No project "${body.project}" exists.` });
        if (pr.state === 'finished') { pr.live = false; pr.running = false; }
        else { pr.state = 'stopped'; pr.running = false; pr.live = false; }
        return reply(200, { ok: true, device_id: 'dev-1' });
      }
      const gm = path.match(/^\/api\/factory\/projects\/([^/]+)\/graph$/);
      if (gm && method === 'GET') {
        const id = decodeURIComponent(gm[1]), pr = list.find((p) => p.id === id);
        return reply(200, { protocol: 1, project: id, folder: pr?.folder || '', tasks });
      }
      const lm = path.match(/^\/api\/factory\/projects\/([^/]+)\/log/);
      if (lm && method === 'GET') {
        const id = decodeURIComponent(lm[1]);
        return reply(200, { protocol: 1, project: id, lines: [{ at: '2026-11-24T12:00:00Z', by: 'engine', task: 'T1', decision: 'started reviewer', why: 'review round 2' }] });
      }
      const sm = path.match(/^\/api\/factory\/projects\/([^/]+)\/settings$/);
      if (sm && method === 'GET') return reply(200, { protocol: 1, rows });
      return reply(404, { error: 'not found' });
    } finally { inFlight--; }
  };
  return { fetch, projects: list, started, stopped, get inFlight() { return inFlight; } };
}

const realSet = globalThis.setTimeout, realClr = globalThis.clearTimeout, realCrypto = globalThis.crypto;
let bootSeq = 0, activeCleanup = null;
async function bootPage(t, daemon, { width = 1200, settle = true, hash = '' } = {}) {
  if (activeCleanup) await activeCleanup();
  const doc = createDoc(readFileSync(new URL('./index.html', import.meta.url), 'utf8')), idb = createIDB();
  const keys = ['document', 'window', 'location', 'indexedDB', 'fetch', 'crypto', 'addEventListener', 'removeEventListener', 'scrollTo', 'innerWidth', 'matchMedia'];
  const prev = Object.fromEntries(keys.map((k) => [k, Object.getOwnPropertyDescriptor(globalThis, k)]));
  const pending = new Set(), timers = new Map(), mqls = [], winLs = new Map();
  const loc = { hash, href: 'http://127.0.0.1:7777/factory' + hash };
  let curW = width, seq = 1000, disposed = false;
  const px = (n, u) => +n * (u === 'px' ? 1 : 16);
  const evalMQ = (q) => { const mx = q.match(/max-width:\s*([\d.]+)(px|r?em)/), mn = q.match(/min-width:\s*([\d.]+)(px|r?em)/); return (!mx || curW <= px(mx[1], mx[2])) && (!mn || curW >= px(mn[1], mn[2])); };
  const track = (p) => { if (p?.then) { const m = Promise.resolve(p).then(() => {}, () => {}).finally(() => pending.delete(m)); pending.add(m); } return p; };
  if (settle) doc._track = track;
  doc._winLs = winLs;
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
  return { doc, flush,
    async pollNow() { const e = [...timers.entries()].find(([, v]) => v.ms <= 3500); if (e) { timers.delete(e[0]); track(e[1].fn()); } else doc.getElementById('retry').click(); await flush(); },
    async setHash(h) { loc.hash = h; loc.href = 'http://127.0.0.1:7777/factory' + h; for (const fn of winLs.get('hashchange') || []) track(fn({ type: 'hashchange' })); await flush(); },
  };
}

const projectBar = (doc) => {
  const bar = doc.querySelector(`[aria-label="${W.region}"]`);
  assert.ok(bar, `expected project bar with aria-label="${W.region}"`);
  return bar;
};
const btn = (root, text) => root.querySelectorAll('button').find((b) => b.textContent.trim() === text);
const btnTexts = (root) => root.querySelectorAll('button').map((b) => b.textContent.trim());
const textOutside = (node, skip) => {
  if (!node || node === skip) return '';
  if (node.nodeType === 3) return node.textContent;
  return (node._nodes || []).map((c) => textOutside(c, skip)).join('');
};
const goalTextarea = (bar, doc) => {
  const lbl = bar.querySelectorAll('label').find((l) => l.textContent.trim() === W.ngLabel);
  assert.ok(lbl, `expected <label> "${W.ngLabel}" in new-goal form`);
  const ta = lbl.getAttribute('for') ? doc.getElementById(lbl.getAttribute('for')) : lbl.querySelector('textarea');
  assert.ok(ta && ta.tagName === 'TEXTAREA' && bar.contains(ta), `label "${W.ngLabel}" must tie to a <textarea>`);
  return ta;
};

test('each project state shows its state word and only its own actions in the project bar and projects list', async (t) => {
  const projects = [
    mkProj({ id: 'proj-run', goal: 'A notes page with search', state: 'running', running: true, live: true, counts: { working: 1 } }),
    mkProj({ id: 'proj-stop', goal: 'Import bookmarks from a file', state: 'stopped', running: false, live: false, counts: { queued: 2 } }),
    // Finished with running:true (Started stays true per design.md §6) must still render as finished from state alone.
    mkProj({ id: 'proj-fin', goal: 'Dark theme for the settings page', state: 'finished', running: true, live: false, counts: { done: 6 } }),
  ];
  const page = await bootPage(t, fakeDaemon({ projects, paired: true }), { hash: '#proj-run' });

  // Projects list shows each project's state word (running, stopped, finished).
  const links = page.doc.getElementById('projects').querySelectorAll('a.proj');
  assert.equal(links.length, 3);
  assert.match(links[0].textContent, /\brunning\b/);
  assert.match(links[1].textContent, /\bstopped\b/);
  assert.match(links[2].textContent, /\bfinished\b/);
  assert.doesNotMatch(links[2].textContent, /\brunning\b|\bstopped\b/, 'finished project must use state, not running bool');

  // Running project bar: Stop + New goal only.
  const runBar = projectBar(page.doc);
  assert.match(runBar.textContent, /\brunning\b/);
  assert.ok(runBar.querySelector('.dot'), 'state must include a dot beside its word');
  assert.deepEqual(btnTexts(runBar), [W.stop, W.newGoal]);
  assert.equal(btn(runBar, W.stop)?.getAttribute('aria-label'), W.stopAria);
  assert.doesNotMatch(runBar.textContent, /\b(N\d+|L\d+|J\d+|max_running|review_rounds)\b/, 'no jargon in project bar');

  // Stopped project bar: stopped sentence, Resume + New goal only.
  await page.setHash('#proj-stop');
  const stopBar = projectBar(page.doc);
  assert.match(stopBar.textContent, /\bstopped\b/);
  assert.ok(stopBar.textContent.includes(W.stoppedP), `expected "${W.stoppedP}"`);
  assert.deepEqual(btnTexts(stopBar), [W.resume, W.newGoal]);
  assert.equal(btn(stopBar, W.resume)?.getAttribute('aria-label'), W.resumeAria);

  // Finished (not live) project bar: finished sentence, Reopen with a new goal only.
  await page.setHash('#proj-fin');
  const finBar = projectBar(page.doc);
  assert.match(finBar.textContent, /\bfinished\b/);
  assert.ok(finBar.textContent.includes(W.finP), `expected "${W.finP}"`);
  assert.deepEqual(btnTexts(finBar), [W.reopen]);
});

test('a finished project shows Stop only while live, uses finished confirm wording, and stays finished after stopping', async (t) => {
  const daemon = fakeDaemon({
    projects: [mkProj({ id: 'proj-fin', state: 'finished', running: true, live: true, counts: { done: 8 } })],
    paired: true,
  });
  const page = await bootPage(t, daemon);
  const bar = projectBar(page.doc);

  assert.match(bar.textContent, /\bfinished\b/);
  assert.ok(bar.textContent.includes(W.finLive), `expected "${W.finLive}" while finished and live`);
  assert.deepEqual(btnTexts(bar), [W.stop, W.reopen], 'finished + live must offer Stop first, then Reopen with a new goal');
  assert.equal(btn(bar, W.stop)?.getAttribute('aria-label'), W.stopAria);

  // Clicking Stop asks first with finished-specific wording (no count, no roles).
  btn(bar, W.stop).click(); await page.flush();
  assert.equal(daemon.stopped.length, 0, 'Stop must never fire on one click');
  const afterClick = projectBar(page.doc);
  const group = afterClick.querySelector('[role="group"]');
  assert.ok(group, 'expected stop confirm group in place');
  const lblId = group.getAttribute('aria-labelledby');
  assert.equal(lblId ? page.doc.getElementById(lblId)?.textContent.trim() : group.querySelector('h3')?.textContent.trim(), W.stopQ);
  assert.ok(group.textContent.includes(W.stopWhatFin), `expected "${W.stopWhatFin}"`);
  assert.doesNotMatch(group.textContent, /\b(coordinator|worker|reviewer)\b/i, 'stop confirm must name no roles');
  assert.deepEqual(btnTexts(group), [W.stopNo, W.stopYes]);
  assert.equal(page.doc.activeElement, btn(group, W.stopNo), 'focus must land on Keep it running');

  // Confirming stops the live run; project stays finished and Stop disappears.
  btn(group, W.stopYes).click(); await page.flush();
  assert.deepEqual(daemon.stopped, [{ project: 'proj-fin' }]);
  const settled = projectBar(page.doc);
  assert.match(settled.textContent, /\bfinished\b/);
  assert.ok(settled.textContent.includes(W.finP), 'after last agent ends, show finished sentence');
  assert.deepEqual(btnTexts(settled), [W.reopen], 'Stop must disappear once finished project is no longer live');
});

test('stop on a running project asks first, focuses Keep it running, closes on Keep it running or Escape, and stops on confirm', async (t) => {
  const daemon = fakeDaemon({ projects: [mkProj({ id: 'proj-demo', state: 'running', running: true, live: true })], paired: true });
  const page = await bootPage(t, daemon);

  // First click opens confirm in place without sending POST.
  btn(projectBar(page.doc), W.stop).click(); await page.flush();
  assert.equal(daemon.stopped.length, 0, 'Stop must never fire on one click');
  let bar = projectBar(page.doc), group = bar.querySelector('[role="group"]');
  assert.ok(group && group.textContent.includes(W.stopQ) && group.textContent.includes(W.stopWhat), 'expected running stop confirm wording');
  assert.doesNotMatch(group.textContent, /\b(coordinator|worker|reviewer|\d+\s+agent)/i, 'stop confirm must name no count and no roles');
  assert.deepEqual(btnTexts(group), [W.stopNo, W.stopYes]);
  assert.equal(page.doc.activeElement, btn(group, W.stopNo), 'focus must land on Keep it running');

  // Keep it running closes confirm without stopping.
  btn(group, W.stopNo).click(); await page.flush();
  assert.equal(daemon.stopped.length, 0);
  assert.equal(projectBar(page.doc).querySelector('[role="group"]'), null, 'confirm must close on Keep it running');

  // Escape also closes confirm without stopping.
  btn(projectBar(page.doc), W.stop).click(); await page.flush();
  group = projectBar(page.doc).querySelector('[role="group"]');
  assert.ok(group);
  btn(group, W.stopNo).dispatchEvent({ type: 'keydown', key: 'Escape', bubbles: true }); await page.flush();
  assert.equal(daemon.stopped.length, 0);
  assert.equal(projectBar(page.doc).querySelector('[role="group"]'), null, 'confirm must close on Escape');

  // Confirming Stop the project sends POST /api/factory/projects/stop and updates state to stopped.
  btn(projectBar(page.doc), W.stop).click(); await page.flush();
  btn(projectBar(page.doc).querySelector('[role="group"]'), W.stopYes).click(); await page.flush();
  assert.deepEqual(daemon.stopped, [{ project: 'proj-demo' }]);
  bar = projectBar(page.doc);
  assert.match(bar.textContent, /\bstopped\b/);
  assert.deepEqual(btnTexts(bar), [W.resume, W.newGoal]);
  assert.match(page.doc.getElementById('projects').textContent, /\bstopped\b/);
});

test('resume on a stopped project sends the exact stored goal to start without settings and transitions to running', async (t) => {
  const fullGoal = '# A notes page with search\n\nInclude tags and keyboard navigation.';
  const daemon = fakeDaemon({
    projects: [mkProj({ id: 'proj-demo', folder: '/Users/dev/code/notes', goal: fullGoal, state: 'stopped', running: false, live: false })],
    paired: true,
  });
  const page = await bootPage(t, daemon);

  btn(projectBar(page.doc), W.resume).click(); await page.flush();
  assert.equal(daemon.started.length, 1, 'Resume must POST /api/factory/projects/start once');
  assert.equal(daemon.started[0].folder, '/Users/dev/code/notes');
  assert.equal(daemon.started[0].goal, fullGoal, 'Resume must send the exact stored goal, not a shortened title');
  assert.ok(!daemon.started[0].set || Object.keys(daemon.started[0].set).length === 0, 'Resume must not send settings overrides');
  assert.match(projectBar(page.doc).textContent, /\brunning\b/);
  assert.deepEqual(btnTexts(projectBar(page.doc)), [W.stop, W.newGoal]);
});

test('new goal form shows only the goal field with state-specific hint and submit label (reopen on finished)', async (t) => {
  const projects = [
    mkProj({ id: 'proj-run', folder: '/Users/dev/code/run', goal: 'Goal A', state: 'running', running: true, live: true }),
    mkProj({ id: 'proj-stop', folder: '/Users/dev/code/stop', goal: 'Goal B', state: 'stopped', running: false, live: false }),
    mkProj({ id: 'proj-fin', folder: '/Users/dev/code/fin', goal: 'Goal C', state: 'finished', running: true, live: false }),
  ];
  const daemon = fakeDaemon({ projects, paired: true });
  const page = await bootPage(t, daemon, { hash: '#proj-run' });

  // Running: New goal -> Give it this goal; Cancel closes; draft survives 3 s poll.
  btn(projectBar(page.doc), W.newGoal).click(); await page.flush();
  let bar = projectBar(page.doc), ta = goalTextarea(bar, page.doc);
  assert.equal(page.doc.activeElement, ta, 'focus must land on the new goal textarea');
  assert.ok(bar.textContent.includes(W.ngHint), `expected "${W.ngHint}"`);
  assert.ok(btn(bar, W.ngGoRun) && btn(bar, W.ngCancel), 'running new-goal form must have Give it this goal and Cancel');
  assert.equal(bar.querySelectorAll('input, select').length, 0, 'new-goal form must hold only the goal field (no folder or settings inputs)');
  ta.value = 'Add tags to notes'; ta.dispatchEvent({ type: 'input', bubbles: true }); await page.flush();
  await page.pollNow();
  assert.equal(goalTextarea(projectBar(page.doc), page.doc).value, 'Add tags to notes', 'typed new goal must survive 3 s poll');
  btn(projectBar(page.doc), W.ngCancel).click(); await page.flush();
  assert.equal(daemon.started.length, 0, 'Cancel must not start');
  assert.equal(projectBar(page.doc).querySelector('textarea'), null, 'Cancel must close the new-goal form');

  // Stopped: New goal -> Start with this goal.
  await page.setHash('#proj-stop');
  btn(projectBar(page.doc), W.newGoal).click(); await page.flush();
  bar = projectBar(page.doc); ta = goalTextarea(bar, page.doc);
  assert.ok(bar.textContent.includes(W.ngHint));
  assert.ok(btn(bar, W.ngGoStop), 'stopped new-goal form must use Start with this goal');
  ta.value = 'New stopped goal'; ta.dispatchEvent({ type: 'input', bubbles: true }); await page.flush();
  btn(projectBar(page.doc), W.ngGoStop).click(); await page.flush();
  assert.equal(daemon.started.at(-1)?.folder, '/Users/dev/code/stop');
  assert.equal(daemon.started.at(-1)?.goal, 'New stopped goal');
  assert.ok(!daemon.started.at(-1)?.set || Object.keys(daemon.started.at(-1).set).length === 0);

  // Finished: Reopen with a new goal -> finished hint + Reopen with this goal.
  await page.setHash('#proj-fin');
  btn(projectBar(page.doc), W.reopen).click(); await page.flush();
  bar = projectBar(page.doc); ta = goalTextarea(bar, page.doc);
  assert.equal(page.doc.activeElement, ta, 'focus must land on textarea when opening reopen form');
  assert.ok(bar.textContent.includes(W.ngHintFin), `expected "${W.ngHintFin}"`);
  assert.ok(btn(bar, W.ngGoFin) && btn(bar, W.ngCancel), 'finished reopen form must use Reopen with this goal and Cancel');
  assert.equal(btn(bar, W.ngGoRun), undefined);
  assert.equal(btn(bar, W.ngGoStop), undefined);
  ta.value = 'Add keyboard shortcuts'; ta.dispatchEvent({ type: 'input', bubbles: true }); await page.flush();
  btn(projectBar(page.doc), W.ngGoFin).click(); await page.flush();
  assert.equal(daemon.started.at(-1)?.folder, '/Users/dev/code/fin');
  assert.equal(daemon.started.at(-1)?.goal, 'Add keyboard shortcuts');
  assert.match(projectBar(page.doc).textContent, /\brunning\b/, 'reopened project transitions to running');
});

test('typing the stored goal again on a finished project is refused in place with the daemon sentence and keeps text across polls', async (t) => {
  const daemon = fakeDaemon({
    projects: [mkProj({ id: 'proj-fin', goal: 'A notes page with search', state: 'finished', running: true, live: false })],
    paired: true,
  });
  const page = await bootPage(t, daemon);

  btn(projectBar(page.doc), W.reopen).click(); await page.flush();
  const ta = goalTextarea(projectBar(page.doc), page.doc);
  ta.value = 'A notes page with search'; ta.dispatchEvent({ type: 'input', bubbles: true }); await page.flush();
  btn(projectBar(page.doc), W.ngGoFin).click(); await page.flush();

  const bar = projectBar(page.doc);
  assert.equal(bar.querySelector('[role="alert"]')?.textContent.trim(), W.ngSame, 'same goal on finished must show daemon refusal in place');
  assert.equal(goalTextarea(bar, page.doc).value, 'A notes page with search', 'field must keep typed text after refusal');

  await page.pollNow();
  const afterPoll = projectBar(page.doc);
  assert.equal(afterPoll.querySelector('[role="alert"]')?.textContent.trim(), W.ngSame, 'refusal must survive 3 s poll');
  assert.equal(goalTextarea(afterPoll, page.doc).value, 'A notes page with search', 'typed text must survive 3 s poll');
});

test('refused Resume, New goal, and Reopen by start checks show the action-specific sentence, checks list with fixes, and models hint', async (t) => {
  const results = [
    { name: 'git version', status: 'ok', detail: 'git version 2.47.1' },
    { name: 'start claude', status: 'fail', detail: 'claude is not signed in', fix: 'run: claude auth login' },
  ];
  const refuseChecks = () => ({ status: 400, body: { error: 'start checks failed', results } });

  // Stopped -> Resume refused by start check.
  const pageStop = await bootPage(t, fakeDaemon({
    projects: [mkProj({ state: 'stopped', running: false, live: false })],
    paired: true, onStart: refuseChecks,
  }));
  btn(projectBar(pageStop.doc), W.resume).click(); await pageStop.flush();
  let bar = projectBar(pageStop.doc);
  assert.equal(bar.querySelector('[role="alert"]')?.textContent.trim(), W.refResume);
  assert.ok(bar.textContent.includes(W.refModels), `expected "${W.refModels}"`);
  assert.ok(bar.textContent.includes('git version') && bar.textContent.includes('git version 2.47.1') && /\bpass\b/.test(bar.textContent));
  assert.ok(bar.textContent.includes('start claude') && bar.textContent.includes('claude is not signed in') && /\bfail\b/.test(bar.textContent));
  assert.ok(bar.textContent.includes(W.fixCmd) && bar.textContent.includes('claude auth login'), 'run: fix must show in a terminal block');
  assert.doesNotMatch(bar.textContent, /check failed\. Fix it, then check again\./, 'refused start must omit the new-project form summary line');
  await pageStop.pollNow();
  assert.equal(projectBar(pageStop.doc).querySelector('[role="alert"]')?.textContent.trim(), W.refResume, 'start-check refusal must survive 3 s poll');

  // Running -> New goal refused by start check.
  const pageRun = await bootPage(t, fakeDaemon({
    projects: [mkProj({ state: 'running', running: true, live: true })],
    paired: true, onStart: refuseChecks,
  }));
  btn(projectBar(pageRun.doc), W.newGoal).click(); await pageRun.flush();
  const taRun = goalTextarea(projectBar(pageRun.doc), pageRun.doc);
  taRun.value = 'Add tags'; taRun.dispatchEvent({ type: 'input', bubbles: true }); await pageRun.flush();
  btn(projectBar(pageRun.doc), W.ngGoRun).click(); await pageRun.flush();
  assert.equal(projectBar(pageRun.doc).querySelector('[role="alert"]')?.textContent.trim(), W.refNewGoal);

  // Finished -> Reopen refused by start check.
  const pageFin = await bootPage(t, fakeDaemon({
    projects: [mkProj({ state: 'finished', running: true, live: false })],
    paired: true, onStart: refuseChecks,
  }));
  btn(projectBar(pageFin.doc), W.reopen).click(); await pageFin.flush();
  const taFin = goalTextarea(projectBar(pageFin.doc), pageFin.doc);
  taFin.value = 'Add dark mode'; taFin.dispatchEvent({ type: 'input', bubbles: true }); await pageFin.flush();
  btn(projectBar(pageFin.doc), W.ngGoFin).click(); await pageFin.flush();
  assert.equal(projectBar(pageFin.doc).querySelector('[role="alert"]')?.textContent.trim(), W.refReopen);
});

test('refused start by a stored bad setting and refused stop show the daemon sentence with role=alert and survive polls', async (t) => {
  const pageSet = await bootPage(t, fakeDaemon({
    projects: [mkProj({ state: 'stopped', running: false, live: false })],
    paired: true,
    onStart: () => ({ status: 400, body: { error: W.refSet } }),
  }));
  btn(projectBar(pageSet.doc), W.resume).click(); await pageSet.flush();
  assert.equal(projectBar(pageSet.doc).querySelector('[role="alert"]')?.textContent.trim(), W.refSet);
  assert.doesNotMatch(projectBar(pageSet.doc).textContent, /\bci_stalled_minutes\b|\breview_rounds\b/, 'never print raw setting keys');
  await pageSet.pollNow();
  assert.equal(projectBar(pageSet.doc).querySelector('[role="alert"]')?.textContent.trim(), W.refSet, 'setting refusal must survive 3 s poll');

  const stopErr = 'No project "proj-demo" exists.';
  const pageStop = await bootPage(t, fakeDaemon({
    projects: [mkProj({ state: 'running', running: true, live: true })],
    paired: true,
    onStop: () => ({ status: 404, body: { code: 'not_found', error: stopErr } }),
  }));
  btn(projectBar(pageStop.doc), W.stop).click(); await pageStop.flush();
  btn(projectBar(pageStop.doc).querySelector('[role="group"]'), W.stopYes).click(); await pageStop.flush();
  assert.equal(projectBar(pageStop.doc).querySelector('[role="alert"]')?.textContent.trim(), stopErr);
  await pageStop.pollNow();
  assert.equal(projectBar(pageStop.doc).querySelector('[role="alert"]')?.textContent.trim(), stopErr, 'stop refusal must survive 3 s poll');
});

test('task detail places the rule id inside a closed details fold and nowhere outside it', async (t) => {
  const page = await bootPage(t, fakeDaemon({ tasks: [mkTask({ ref: 'T1', last_rule: 'N22', last_decision: 'started reviewer' })], paired: true }), { hash: '#proj-demo/T1' });
  const main = page.doc.getElementById('main');
  const verdict = main.querySelector('section[aria-label="Verdict"]');
  assert.ok(verdict, 'expected Verdict card on task detail view');
  const fold = verdict.querySelector('details');
  assert.ok(fold, 'expected a <details> fold inside the Verdict card');
  assert.equal(fold.open, false, 'details fold must start closed');
  const summary = fold.querySelector('summary');
  assert.equal(summary?.textContent.trim(), W.tkDetails, 'details fold summary must read "details"');
  assert.ok(textOutside(fold, summary).includes('rule N22'), 'rule N22 must live inside the details fold');
  assert.doesNotMatch(textOutside(main, fold), /\bN22\b|rule\s+N22/i, 'rule id must not appear outside the details fold');
  assert.doesNotMatch(summary.textContent, /\bN22\b/, 'rule id must not appear in the summary text');

  // Opening the fold survives a 3 s poll.
  fold.open = true;
  await page.pollNow();
  assert.equal(page.doc.getElementById('main').querySelector('section[aria-label="Verdict"] details')?.open, true);
});

test('unpaired stop and resume open the pairing box inside the project bar (not in #asks) and resume the action after pairing', async (t) => {
  // Unpaired Stop (after confirming): pairing box opens in project bar and resumes Stop.
  const stopDaemon = fakeDaemon({ projects: [mkProj({ id: 'proj-demo', state: 'running', running: true, live: true })], paired: false });
  const stopPage = await bootPage(t, stopDaemon);
  btn(projectBar(stopPage.doc), W.stop).click(); await stopPage.flush();
  btn(projectBar(stopPage.doc).querySelector('[role="group"]'), W.stopYes).click(); await stopPage.flush();

  const stopBar = projectBar(stopPage.doc);
  const stopPair = stopBar.querySelector(`[role="region"][aria-label="${W.pairH}"]`);
  assert.ok(stopPair, 'pairing box must appear inside the project bar beside the action');
  assert.equal(stopPage.doc.getElementById('asks').querySelector('form[data-pair-form]'), null, 'pairing box must not appear in #asks');
  assert.equal(stopPage.doc.activeElement?.getAttribute('name'), 'code', 'focus must move to pairing code input');
  stopPair.querySelector('input[name="code"]').value = 'ABCD-EFGH';
  stopPair.querySelector('form[data-pair-form]').dispatchEvent({ type: 'submit', bubbles: true }); await stopPage.flush();
  assert.deepEqual(stopDaemon.stopped, [{ project: 'proj-demo' }], 'paired stop must resume automatically');
  assert.equal(projectBar(stopPage.doc).querySelector('form[data-pair-form]'), null, 'pairing box must close after pairing');
  assert.match(projectBar(stopPage.doc).textContent, /\bstopped\b/);

  // Unpaired Resume: pairing box opens in project bar and resumes Start with the stored goal.
  const resDaemon = fakeDaemon({ projects: [mkProj({ id: 'proj-demo', folder: '/Users/dev/code/notes', goal: 'Stored goal', state: 'stopped', running: false, live: false })], paired: false });
  const resPage = await bootPage(t, resDaemon);
  btn(projectBar(resPage.doc), W.resume).click(); await resPage.flush();
  const resPair = projectBar(resPage.doc).querySelector(`[role="region"][aria-label="${W.pairH}"]`);
  assert.ok(resPair, 'pairing box must appear in project bar for unpaired Resume');
  assert.equal(resPage.doc.getElementById('asks').querySelector('form[data-pair-form]'), null);
  resPair.querySelector('input[name="code"]').value = 'ABCD-EFGH';
  resPair.querySelector('form[data-pair-form]').dispatchEvent({ type: 'submit', bubbles: true }); await resPage.flush();
  assert.equal(resDaemon.started.length, 1);
  assert.equal(resDaemon.started[0].goal, 'Stored goal');
  assert.match(projectBar(resPage.doc).textContent, /\brunning\b/);
});

test('narrow layout places the project bar before asks for you, escapes HTML in refusals, and app.js holds no POST', async (t) => {
  const ask = { id: 'a1', project: 'proj-demo', kind: 'gate', gated: true, question: 'Merge T1?', options: ['Merge', 'Hold'], recommend: 'Merge', status: 'open' };
  const page = await bootPage(t, fakeDaemon({
    projects: [mkProj({ state: 'running', running: true, live: true })],
    asks: [ask],
    paired: true,
    onStop: () => ({ status: 400, body: { error: '<img src=x onerror=alert(1)>' } }),
  }), { width: 360 });
  const all = page.doc.getElementById('board').querySelectorAll('*');
  const bar = projectBar(page.doc), asksEl = page.doc.getElementById('asks');
  assert.ok(all.indexOf(bar) >= 0 && all.indexOf(bar) < all.indexOf(asksEl), 'in narrow layout, project bar must precede asks for you in DOM order');

  btn(bar, W.stop).click(); await page.flush();
  btn(projectBar(page.doc).querySelector('[role="group"]'), W.stopYes).click(); await page.flush();
  const alertEl = projectBar(page.doc).querySelector('[role="alert"]');
  assert.ok(alertEl && !alertEl.innerHTML.includes('<img') && alertEl.textContent.includes('<img src=x onerror=alert(1)>'), 'refusal HTML must be escaped');
  assert.ok(!/\bPOST\b|method\s*:/.test(readFileSync(new URL('./app.js', import.meta.url), 'utf8')), 'app.js must hold no POST');
});

test('clicking Resume while the new-goal form is open closes the form and shows any refusal under Resume', async (t) => {
  const daemon = fakeDaemon({
    projects: [mkProj({ state: 'stopped', running: false, live: false })],
    paired: true,
    onStart: () => ({ status: 400, body: { error: W.refSet } }),
  });
  const page = await bootPage(t, daemon);
  btn(projectBar(page.doc), W.newGoal).click(); await page.flush();
  assert.ok(page.doc.getElementById('pa-goal'));

  btn(projectBar(page.doc), W.resume).click(); await page.flush();
  const bar = projectBar(page.doc);
  assert.equal(bar.querySelector('textarea'), null, 'clicking Resume must close the abandoned new-goal form');
  assert.equal(bar.querySelector('#pa-goal-err'), null, 'Resume refusal must not attach to #pa-goal');
  assert.equal(bar.querySelector('[role="alert"]')?.textContent.trim(), W.refSet);
});

test('start-check refusal on New goal or Reopen focuses the submit button (not Resume or Stop) and pressing the named bar button retries', async (t) => {
  const results = [{ name: 'start claude', status: 'fail', detail: 'claude is not signed in', fix: 'run: claude auth login' }];

  // Stopped -> New goal: focus stays on Start with this goal (not Resume), and pressing New goal retries.
  let failStop = true;
  const stopDaemon = fakeDaemon({
    projects: [mkProj({ folder: '/Users/dev/code/notes', goal: 'Old goal', state: 'stopped', running: false, live: false })],
    paired: true,
    onStart: () => (failStop ? { status: 400, body: { error: 'start checks failed', results } } : null),
  });
  const stopPage = await bootPage(t, stopDaemon);
  btn(projectBar(stopPage.doc), W.newGoal).click(); await stopPage.flush();
  const taStop = goalTextarea(projectBar(stopPage.doc), stopPage.doc);
  taStop.value = 'New goal after login'; taStop.dispatchEvent({ type: 'input', bubbles: true }); await stopPage.flush();
  btn(projectBar(stopPage.doc), W.ngGoStop).click(); await stopPage.flush();

  const stopBar = projectBar(stopPage.doc);
  assert.notEqual(stopPage.doc.activeElement, btn(stopBar, W.resume), 'focus must not jump onto Resume (old goal)');
  assert.equal(stopPage.doc.activeElement, btn(stopBar, W.ngGoStop), 'focus must stay on Start with this goal');

  failStop = false;
  btn(stopBar, W.newGoal).click(); await stopPage.flush();
  assert.equal(stopDaemon.started.length, 2, 'pressing New goal again as ac.refused instructs must retry start');
  assert.equal(stopDaemon.started[1].goal, 'New goal after login');

  // Finished + live -> Reopen: focus stays on Reopen with this goal (not Stop), and pressing Reopen retries.
  let failFin = true;
  const finDaemon = fakeDaemon({
    projects: [mkProj({ folder: '/Users/dev/code/notes', goal: 'Old finished goal', state: 'finished', running: true, live: true })],
    paired: true,
    onStart: () => (failFin ? { status: 400, body: { error: 'start checks failed', results } } : null),
  });
  const finPage = await bootPage(t, finDaemon);
  btn(projectBar(finPage.doc), W.reopen).click(); await finPage.flush();
  const taFin = goalTextarea(projectBar(finPage.doc), finPage.doc);
  taFin.value = 'Reopen after login'; taFin.dispatchEvent({ type: 'input', bubbles: true }); await finPage.flush();
  btn(projectBar(finPage.doc), W.ngGoFin).click(); await finPage.flush();

  const finBar = projectBar(finPage.doc);
  assert.notEqual(finPage.doc.activeElement, btn(finBar, W.stop), 'focus must not jump onto Stop on finished+live reopen refusal');
  assert.equal(finPage.doc.activeElement, btn(finBar, W.ngGoFin), 'focus must stay on Reopen with this goal');

  failFin = false;
  btn(finBar, W.reopen).click(); await finPage.flush();
  assert.equal(finDaemon.started.length, 2, 'pressing Reopen again as ac.refused instructs must retry start');
  assert.equal(finDaemon.started[1].goal, 'Reopen after login');
});

test('external project state transitions across polls clear stale start refusals and hidden stop confirms', async (t) => {
  const results = [{ name: 'start claude', status: 'fail', detail: 'claude is not signed in', fix: 'run: claude auth login' }];
  const daemon = fakeDaemon({
    projects: [mkProj({ id: 'proj-demo', state: 'stopped', running: false, live: false })],
    paired: true,
    onStart: () => ({ status: 400, body: { error: 'start checks failed', results } }),
  });
  const page = await bootPage(t, daemon);

  btn(projectBar(page.doc), W.resume).click(); await page.flush();
  assert.equal(projectBar(page.doc).querySelector('[role="alert"]')?.textContent.trim(), W.refResume);
  daemon.projects[0].state = 'running'; daemon.projects[0].running = true; daemon.projects[0].live = true;
  await page.pollNow();
  assert.equal(projectBar(page.doc).querySelector('[role="alert"]'), null, 'running project must not keep showing a stale Not started refusal');
  assert.equal(projectBar(page.doc).querySelector('section[aria-label="start checks"]'), null);

  daemon.projects[0].state = 'finished'; daemon.projects[0].live = true;
  await page.pollNow();
  btn(projectBar(page.doc), W.stop).click(); await page.flush();
  assert.ok(projectBar(page.doc).querySelector('[role="group"]'));
  daemon.projects[0].live = false;
  await page.pollNow();
  assert.equal(projectBar(page.doc).querySelector('[role="group"]'), null);
  daemon.projects[0].state = 'running'; daemon.projects[0].live = true;
  await page.pollNow();
  assert.equal(projectBar(page.doc).querySelector('[role="group"]'), null, 'hidden stop confirm must not reappear unprompted');
});

test('Escape inside stop confirm is ignored while Stop is in flight', async (t) => {
  let release;
  const gate = new Promise((r) => { release = r; });
  const daemon = fakeDaemon({
    projects: [mkProj({ state: 'running', running: true, live: true })],
    paired: true,
  });
  const origFetch = daemon.fetch;
  daemon.fetch = async (p, i) => { if (p === '/api/factory/projects/stop') await gate; return origFetch(p, i); };

  const page = await bootPage(t, daemon);
  btn(projectBar(page.doc), W.stop).click(); await page.flush();
  const group = projectBar(page.doc).querySelector('[role="group"]');
  assert.ok(group, 'expected stop confirm group');
  try {
    btn(group, W.stopYes).click();
    const confirm = projectBar(page.doc).querySelector('.pa-confirm');
    assert.ok(confirm, 'stop confirm must stay visible while Stop is in flight');
    confirm.dispatchEvent({ type: 'keydown', key: 'Escape', bubbles: true });
    assert.ok(projectBar(page.doc).querySelector('.pa-confirm'), 'Escape must not dismiss stop confirm while Stop is in flight');
  } finally {
    release();
  }
  await page.flush();
});

test('renderProjectActionsHTML handles prototype-key project.state safely', async () => {
  const { renderProjectActionsHTML } = await import('./project.js');
  const html = renderProjectActionsHTML({ id: 'p1', state: 'toString' }, { mode: 'newgoal' });
  assert.doesNotMatch(html, /function toString/);
  assert.match(html, /class="dot idle"/);
  assert.match(html, />Give it this goal</);
});

test('style.css sets font-size: 13px on .pill so the project bar state pill matches the accepted board', () => {
  const css = readFileSync(new URL('./style.css', import.meta.url), 'utf8');
  const pillRule = css.match(/(?:^|[\n}])\s*\.pill\s*\{([^}]*)\}/);
  assert.ok(pillRule, 'style.css must define .pill');
  assert.match(pillRule[1], /font-size\s*:\s*13px\b/, '.pill must set font-size: 13px so it does not inherit 15px body size in .pa-st');
});

test('undelivered goal shows one plain line on a running project, prefills New goal with the same goal, and disappears once settled', async (t) => {
  const undeliveredMsg = 'The coordinator did not get the new goal. Give it the goal again.';
  const daemon = fakeDaemon({
    projects: [
      mkProj({ id: 'proj-run', folder: '/Users/dev/code/notes', goal: 'Add search filters', state: 'running', running: true, live: true, goal_undelivered: true }),
      mkProj({ id: 'proj-stop', folder: '/Users/dev/code/stop', goal: 'Stopped goal', state: 'stopped', running: false, live: false, goal_undelivered: true }),
    ],
    paired: true,
    onStart: (body, list) => {
      const pr = list.find((p) => p.folder === body.folder);
      if (pr) pr.goal_undelivered = false;
      return null;
    },
  });
  const page = await bootPage(t, daemon, { hash: '#proj-run' });

  const bar = projectBar(page.doc);
  assert.ok(bar.textContent.includes(undeliveredMsg), `expected "${undeliveredMsg}" in project bar`);
  assert.doesNotMatch(bar.textContent, /\b(J12|S9d|new_goal|goal_undelivered)\b/, 'no engine jargon in undelivered goal notice');
  assert.deepEqual(btnTexts(bar), [W.stop, W.newGoal]);

  // Existing New goal button opens the form prefilled with the exact current goal.
  btn(bar, W.newGoal).click(); await page.flush();
  const openBar = projectBar(page.doc);
  const ta = goalTextarea(openBar, page.doc);
  assert.equal(ta.value, 'Add search filters', 'New goal must prefill the same goal when undelivered');
  assert.ok(btn(openBar, W.ngGoRun), 'running new-goal form must offer Give it this goal');

  // Submitting sends the same goal to start and clears the notice once settled.
  btn(openBar, W.ngGoRun).click(); await page.flush();
  assert.equal(daemon.started.length, 1);
  assert.deepEqual({ folder: daemon.started[0].folder, goal: daemon.started[0].goal }, { folder: '/Users/dev/code/notes', goal: 'Add search filters' });
  const settledBar = projectBar(page.doc);
  assert.ok(!settledBar.textContent.includes(undeliveredMsg), 'undelivered notice must disappear once settled');

  // Once settled, opening New goal starts empty again.
  btn(settledBar, W.newGoal).click(); await page.flush();
  assert.equal(goalTextarea(projectBar(page.doc), page.doc).value, '', 'settled New goal must not prefill old goal');

  // Non-running states never show the undelivered notice.
  await page.setHash('#proj-stop');
  assert.ok(!projectBar(page.doc).textContent.includes(undeliveredMsg), 'stopped project must not show undelivered goal notice');
});

