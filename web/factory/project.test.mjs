// Unit and page tests for PR 14.5: /factory new-project form, start checks with
// terminal command fixes, and browser pairing inside the form (`make test-web`).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createAskFlow } from './asks.js';
import { keyStore } from '../voice/pairing.js';

const W = {
  emptyH: 'no project yet',
  emptyP: 'A project is one folder with a git repository and one goal. The coordinator plans the tasks, workers build them, reviewers check them, and you are asked only for decisions.',
  projNone: 'None yet.',
  projNew: 'New project',
  npH: 'new project',
  folderHint: 'A folder on this computer that is a git repository with a GitHub remote.',
  folderPh: '/path/to/your/repo',
  suggLbl: 'folders sheepdog knows',
  suggGit: 'git repository',
  suggNotGit: 'not a git repository',
  suggNone: "No folders to suggest: herdr cannot be reached. Type the folder's full path.",
  notGit: 'This folder is not a git repository. Pick another folder, or make it one.',
  goalHint: 'What you want built, in your own words. The coordinator turns it into tasks.',
  goalEmpty: 'Write a goal first.',
  modelsHint: 'The CLI, model and effort each role uses. In order: the engine uses the first one it can start. A retro reviewer looks at a task whose reviews keep failing.',
  checkBtn: 'Check this folder',
  checkAgain: 'Check again',
  startBtn: 'Start the project and its agents',
  startOff: 'Start turns on when no check fails.',
  quotaHint: 'Starting runs agents on your model accounts. They use your quota.',
  starting: 'Starting…',
  started: 'Started. The coordinator is planning the tasks.',
  ckH: 'start checks',
  ckIntro: 'Every check must pass before a project can start. A warning does not stop it.',
  ckRunning: 'Checking the folder…',
  ckFix: 'Fix:',
  ckFixCmd: 'Fix, in a terminal:',
  ckModels: 'Is a failed check for a CLI you do not use? Change the models above, then check again.',
  ckCant: "Can't run the start checks: sheepdog did not answer in time. Check that sheepdog is running, then check again.",
  pairH: 'pair this browser',
  pairP: 'Changes from this page need a paired browser, once. On this Mac, open the Sheepdog menu-bar app (its icon is in the menu bar), choose Pair browser…, and type the code it shows.',
  pairKept: 'What you just asked for is kept. It is sent as soon as this browser is paired.',
  pairWrong: 'That code did not work. It was mistyped, already used, or older than 5 minutes. Choose Pair browser… again for a new code.',
};

const AGY_MODELS = ['gemini-4-carbon', 'gemini-4-neon', 'gemini-4-hydrogen', 'gemini-3.8-flash'];

const defaultRows = () => [
  { name: 'models.coordinator', label: 'coordinator models', value: 'claude/opus/medium', default: 'claude/opus/medium', min: 0, max: 0, top: true, clis: ['claude', 'codex', 'agy'], models: { agy: AGY_MODELS } },
  { name: 'models.worker', label: 'worker models', value: 'claude/sonnet/medium', default: 'claude/sonnet/medium', min: 0, max: 0, top: true, clis: ['claude', 'codex', 'agy'], models: { agy: AGY_MODELS } },
  { name: 'models.reviewer', label: 'reviewer models', value: 'claude/opus/medium, claude/sonnet/medium', default: 'claude/opus/medium, claude/sonnet/medium', min: 0, max: 0, top: true, clis: ['claude', 'codex', 'agy'], models: { agy: AGY_MODELS } },
  { name: 'models.retro', label: 'retro models', value: 'claude/opus/high', default: 'claude/opus/high', min: 0, max: 0, top: true, clis: ['claude', 'agy'], models: { agy: AGY_MODELS } },
  { name: 'models.plan_reviewer', label: 'plan reviewer models', value: '', default: '', min: 0, max: 0, top: true, clis: ['claude', 'agy'], models: { agy: AGY_MODELS } },
  { name: 'max_running', label: 'max running', value: '3', default: '3', min: 1, max: 20, top: true },
  { name: 'review_rounds', label: 'review rounds', value: '2', default: '2', min: 1, max: 5 },
  { name: 'max_rounds', label: 'max rounds', value: '5', default: '5', min: 2, max: 10 },
  { name: 'ci_stalled_minutes', label: 'ci stalled minutes', value: '90', default: '90', min: 10, max: 600 },
  { name: 'second_reviewer', label: 'second reviewer', value: 'opus', default: 'opus', min: 0, max: 0, allowed: ['opus', 'codex'] },
  { name: 'design_gate', label: 'design gate', value: '', default: '', min: 0, max: 0, allowed: ['', 'owner', 'review'] },
  { name: 'dispatch_workflow', label: 'dispatch workflow', value: '', default: '', min: 0, max: 0 },
];

const defaultSpaces = () => ({
  spaces: [
    { id: 'sp-1', name: 'notes', path: '/Users/dev/code/notes', git: true, agents: [], shells: [], commands: [] },
  ],
  folders: [
    { name: 'recipes', path: '/Users/dev/code/recipes', git: true },
    { name: 'scratch', path: '/Users/dev/code/scratch', git: false },
  ],
  kinds: [],
  worktrees: true,
  needs_access: [],
});

const allPassChecks = () => [
  { name: 'git version', status: 'ok', detail: 'git version 2.47.1' },
  { name: 'start claude', status: 'ok', detail: 'claude is signed in' },
  { name: 'pr workflow', status: 'ok', detail: 'ci.yml on origin/main runs on pull requests' },
];

const passWithWarnChecks = () => [
  { name: 'git version', status: 'ok', detail: 'git version 2.47.1' },
  { name: 'start fetch', status: 'warn', detail: 'git failed to fetch origin, so the refs below may be stale', fix: 'run: git fetch origin (sign in to git if it asks)' },
  { name: 'start claude', status: 'ok', detail: 'claude is signed in' },
  { name: 'start agy', status: 'ok', detail: 'agy is signed in' },
  { name: 'gh push access', status: 'ok', detail: 'signed in; push access to you/notes' },
  { name: 'github remote', status: 'ok', detail: 'origin is you/notes; its default branch main is the base' },
  { name: 'pr workflow', status: 'ok', detail: 'ci.yml on origin/main runs on pull requests' },
];

const mixedFailChecks = () => [
  { name: 'git version', status: 'ok', detail: 'git version 2.47.1' },
  { name: 'start fetch', status: 'warn', detail: 'git failed to fetch origin, so the refs below may be stale', fix: 'run: git fetch origin (sign in to git if it asks)' },
  { name: 'start claude', status: 'ok', detail: 'claude is signed in' },
  { name: 'start agy', status: 'fail', detail: 'agy is not signed in', fix: 'run agy without arguments and sign in' },
  { name: 'gh push access', status: 'fail', detail: 'cannot read you/notes with gh (not signed in or no access): HTTP 401', fix: 'run: gh auth login (an account with push access to the repo)' },
  { name: 'github remote', status: 'skip', detail: 'you/notes: needs the gh check above to pass' },
  { name: 'pr workflow', status: 'ok', detail: 'ci.yml on origin/main runs on pull requests' },
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
  set innerHTML(h) { this._raw = String(h ?? ''); this._nodes = parseHTML(this._raw, this.ownerDocument, this); }
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
  focus() { if (this.ownerDocument) this.ownerDocument.activeElement = this; }
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
    return !e.defaultPrevented;
  }
  click() {
    if (this.disabled) return;
    const ok = this.dispatchEvent({ type: 'click', bubbles: true });
    if (!ok) return;
    if (this.tagName === 'BUTTON' && (this.type === 'submit' || (!this.type && this.closest('form')))) {
      const f = this.closest('form'); if (f) f.dispatchEvent({ type: 'submit', bubbles: true });
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

function fakeDaemon({
  projects = [],
  rows = defaultRows(),
  spaces = defaultSpaces(),
  spacesStatus = 200,
  paired = false,
  onCheck = null,
  onStart = null,
} = {}) {
  const projs = projects.map((p) => ({ ...p }));
  const checks = [], starts = [], proposed = [], fetched = [];
  let hasSession = paired, inFlight = 0, projSeq = 0;
  const reply = (s, o) => ({ ok: s >= 200 && s < 300, status: s, json: async () => o });
  const fetch = async (rawPath, init = {}) => {
    const path = String(rawPath), [urlPath] = path.split('?'), method = init.method || 'GET';
    const body = init.body ? JSON.parse(init.body) : null;
    fetched.push({ method, path: urlPath });
    inFlight++;
    try {
      await Promise.resolve();
      if (urlPath === '/api/hello') return reply(200, { computer: { id: 'D1', name: 'Test Mac' } });
      if (urlPath === '/api/devices/enroll') return String(body.code || '').replace(/[\s-]+/g, '').toUpperCase() === 'ABCDEFGH' ? reply(200, { device_id: 'dev-1', daemon_id: 'D1' }) : reply(410, { reason: 'code_used', error: 'Invalid or used code.' });
      if (urlPath === '/api/auth/challenge') return reply(200, { nonce: 'N1', daemon_id: 'D1', expires_in: 60 });
      if (urlPath === '/api/auth/session') { hasSession = true; return reply(200, { expires_in: 43200, device: { id: 'dev-1' } }); }
      if (urlPath === '/api/spaces' && method === 'GET') {
        return spacesStatus === 200 ? reply(200, spaces) : reply(spacesStatus, { error: "could not list herdr's workspaces" });
      }
      if (urlPath === '/api/factory/settings' && method === 'GET') return reply(200, { protocol: 1, rows });
      if (urlPath === '/api/factory/projects' && method === 'GET') return reply(200, { protocol: 1, projects: projs });
      if (urlPath === '/api/factory/asks' && method === 'GET') return reply(200, { protocol: 1, asks: [] });
      if (urlPath === '/api/factory/projects/check' && method === 'POST') {
        checks.push(body);
        if (onCheck) {
          const res = await onCheck(body);
          if (res) return reply(res.status, res.body);
        }
        return reply(200, { results: allPassChecks() });
      }
      if (urlPath === '/api/factory/projects/start' && method === 'POST') {
        if (!hasSession) return reply(403, { code: 'proof_required', error: 'proof_required' });
        starts.push(body);
        if (onStart) {
          const res = await onStart(body, projs);
          if (res) return reply(res.status, res.body);
        }
        const existing = projs.find((p) => p.folder === body.folder);
        if (existing) {
          Object.assign(existing, { goal: body.goal, state: 'running', live: true, running: true });
        } else {
          projs.push({ id: `proj-new-${++projSeq}`, folder: body.folder, goal: body.goal, state: 'running', live: true, running: true, counts: {} });
        }
        return reply(200, { ok: true, device_id: 'dev-1' });
      }
      const gm = urlPath.match(/^\/api\/factory\/projects\/([^/]+)\/graph$/);
      if (gm && method === 'GET') {
        const p = projs.find((x) => x.id === decodeURIComponent(gm[1]));
        return p ? reply(200, { protocol: 1, project: p.id, tasks: p._tasks || [] }) : reply(404, { error: 'not found' });
      }
      const lm = urlPath.match(/^\/api\/factory\/projects\/([^/]+)\/log$/);
      if (lm && method === 'GET') {
        const p = projs.find((x) => x.id === decodeURIComponent(lm[1]));
        return p ? reply(200, { protocol: 1, project: p.id, lines: [] }) : reply(404, { error: 'not found' });
      }
      const sm = urlPath.match(/^\/api\/factory\/projects\/([^/]+)\/settings$/);
      if (sm && method === 'GET') {
        const p = projs.find((x) => x.id === decodeURIComponent(sm[1]));
        return p ? reply(200, { protocol: 1, rows }) : reply(404, { error: 'not found' });
      }
      if (sm && method === 'POST') {
        proposed.push(body);
        return reply(200, { ok: true });
      }
      return reply(404, { error: 'not found' });
    } finally { inFlight--; }
  };
  return { fetch, projects: projs, checks, starts, proposed, fetched, rows, get inFlight() { return inFlight; } };
}

const realSet = globalThis.setTimeout, realClr = globalThis.clearTimeout, realCrypto = globalThis.crypto;
let bootSeq = 0, activeCleanup = null;

async function bootPage(t, daemon, { width = 1200, settle = true } = {}) {
  if (activeCleanup) await activeCleanup();
  const doc = createDoc(readFileSync(new URL('./index.html', import.meta.url), 'utf8')), idb = createIDB();
  const keys = ['document', 'window', 'location', 'indexedDB', 'fetch', 'crypto', 'addEventListener', 'removeEventListener', 'scrollTo', 'innerWidth', 'matchMedia'];
  const prev = Object.fromEntries(keys.map((k) => [k, Object.getOwnPropertyDescriptor(globalThis, k)]));
  const pending = new Set(), timers = new Map(), mqls = [], winLs = new Map();
  let curW = width, curHash = '', seq = 1000, disposed = false;
  const px = (n, u) => +n * (u === 'px' ? 1 : 16);
  const evalMQ = (q) => { const mx = q.match(/max-width:\s*([\d.]+)(px|r?em)/), mn = q.match(/min-width:\s*([\d.]+)(px|r?em)/); return (!mx || curW <= px(mx[1], mx[2])) && (!mn || curW >= px(mn[1], mn[2])); };
  const track = (p) => { if (p?.then) { const m = Promise.resolve(p).then(() => {}, () => {}).finally(() => pending.delete(m)); pending.add(m); } return p; };
  if (settle) doc._track = track;
  const loc = {
    get hash() { return curHash; },
    set hash(v) {
      const next = v ? (String(v).startsWith('#') ? String(v) : '#' + v) : '';
      if (next !== curHash) {
        curHash = next;
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
  await flush();
  return {
    doc, flush,
    async pollNow() {
      const e = [...timers.entries()].find(([, v]) => v.ms === 3000) || [...timers.entries()].find(([, v]) => v.ms <= 3500);
      if (e) { timers.delete(e[0]); track(e[1].fn()); } else doc.getElementById('retry').click();
      await flush();
    },
  };
}

const btnByText = (root, text) => root.querySelectorAll('button').find((b) => b.textContent.trim() === text) || null;
const fieldByLabel = (root, label, doc) => {
  const lbl = root.querySelectorAll('label').find((l) => l.textContent.trim() === label);
  assert.ok(lbl, `expected <label> "${label}"`);
  const ctrl = lbl.getAttribute('for') ? doc.getElementById(lbl.getAttribute('for')) : lbl.querySelector('input,textarea,select');
  assert.ok(ctrl && root.contains(ctrl), `label "${label}" must tie to an input/textarea/select`);
  return { lbl, ctrl };
};
const setField = async (page, ctrl, val, ev = 'input') => {
  ctrl.value = val;
  ctrl.dispatchEvent({ type: ev, bubbles: true });
  await page.flush();
};
const modelRow = (root, name, label) => root.querySelector(`[data-row="${name}"]`)
  || root.querySelector(`button[aria-label="Edit ${label}"]`)?.closest('.srow')
  || null;
const openNewProject = async (page, from = 'main') => {
  const container = page.doc.getElementById(from);
  const btn = btnByText(container, W.projNew);
  assert.ok(btn, `expected "${W.projNew}" button in #${from}`);
  btn.click();
  await page.flush();
  return page.doc.getElementById('main');
};

test('empty page and projects list offer New project and open the new-project form', async (t) => {
  const emptyPage = await bootPage(t, fakeDaemon({ projects: [] }));
  const emptyMain = emptyPage.doc.getElementById('main');
  assert.ok(emptyMain.textContent.includes(W.emptyH), 'empty page must show "no project yet" heading');
  assert.ok(emptyMain.textContent.includes(W.emptyP), 'empty page must show empty.p wording');
  assert.ok(!emptyMain.textContent.includes('nothing running'), 'empty page must replace "nothing running"');
  assert.ok(emptyPage.doc.getElementById('projects').textContent.includes(W.projNone), 'projects card must show None yet.');
  const formFromEmpty = await openNewProject(emptyPage, 'main');
  assert.equal(formFromEmpty.querySelector('h1')?.textContent.trim(), W.npH, 'form h1 must be "new project"');
  assert.ok(formFromEmpty.querySelectorAll('a').some((a) => a.textContent.includes('board')), 'form must show back link "board"');
  assert.equal(emptyPage.doc.getElementById('settings').hidden, true, 'project settings card must hide while in the new-project form');

  const proj = { id: 'proj-demo', folder: '/Users/dev/code/demo', goal: 'Existing project', state: 'running', live: true, running: true, counts: { working: 1 } };
  const listPage = await bootPage(t, fakeDaemon({ projects: [proj] }));
  const formFromList = await openNewProject(listPage, 'projects');
  assert.equal(formFromList.querySelector('h1')?.textContent.trim(), W.npH, 'projects card New project button must open the new-project form');
});

test('form renders only the Top rows of GET /api/factory/settings with no Change buttons, no raw keys and no rule ids', async (t) => {
  const rows = [
    ...defaultRows(),
    { name: 'custom_top_limit', label: 'custom top limit', value: '7', default: '7', min: 1, max: 10, top: true },
  ];
  const daemon = fakeDaemon({ projects: [], rows });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');

  assert.ok(daemon.fetched.some((f) => f.method === 'GET' && f.path === '/api/factory/settings'), 'form must fetch GET /api/factory/settings');
  const fs = main.querySelector('fieldset');
  assert.ok(fs && fs.querySelector('legend')?.textContent.trim() === 'models', 'models rows must sit in a <fieldset> with <legend>models</legend>');
  assert.ok(fs.textContent.includes(W.modelsHint), 'models group must show np.models.hint');

  for (const r of rows.filter((x) => x.top)) {
    assert.ok(main.textContent.includes(r.label), `Top row "${r.label}" must appear in the form`);
  }
  for (const r of rows.filter((x) => !x.top)) {
    assert.ok(!main.textContent.includes(r.label), `non-Top row "${r.label}" must NOT appear in the form`);
  }
  assert.equal(main.querySelector('details.all-settings'), null, 'form must not render an all-settings fold');
  assert.equal(btnByText(main, 'Change'), null, 'form rows must not have a Change button before start');
  assert.doesNotMatch(main.textContent, /Waiting for your answer/, 'form must not render settings read-back');

  const maxRun = fieldByLabel(main, 'max running', page.doc);
  assert.equal(maxRun.ctrl.getAttribute('type'), 'number');
  assert.equal(maxRun.ctrl.getAttribute('min'), '1');
  assert.equal(maxRun.ctrl.getAttribute('max'), '20');
  assert.equal(maxRun.ctrl.value, '3');
  assert.ok(maxRun.ctrl.getAttribute('aria-describedby'), 'max running must have aria-describedby');
  assert.match(main.textContent, /default 3 · allowed 1–20/);

  for (const el of main.querySelectorAll('input, select, textarea')) {
    const labeled = Boolean(el.getAttribute('aria-label')) || Boolean(el.id && main.querySelector(`label[for="${el.id}"]`)) || Boolean(el.closest('label'));
    assert.ok(labeled, `every form control (${el.tagName}) must have a label`);
  }
  for (const r of rows) {
    assert.ok(!main.textContent.includes(r.name), `raw setting key "${r.name}" must not appear as text`);
  }
  assert.doesNotMatch(main.textContent, /\b(SE\d+|N\d+|A[1-5]|J\d+)\b/, 'rule ids must not appear in the form');
});

test('form with no suggestions (502 from /api/spaces) shows empty state and accepts a typed folder path', async (t) => {
  const daemon = fakeDaemon({ projects: [], spacesStatus: 502 });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');

  assert.ok(main.textContent.includes(W.suggNone), '502 from /api/spaces must show np.sugg.none');
  assert.ok(!main.textContent.includes(W.suggLbl), 'folders sheepdog knows label hides when there are no suggestions');

  const folder = fieldByLabel(main, 'folder', page.doc);
  assert.equal(folder.ctrl.getAttribute('placeholder'), W.folderPh);
  assert.ok(main.textContent.includes(W.folderHint), 'folder field must show np.folder.hint');
  await setField(page, folder.ctrl, '/Users/dev/code/typed-repo');

  const checkBtn = btnByText(main, W.checkBtn);
  assert.ok(checkBtn, 'expected Check this folder button');
  checkBtn.click();
  await page.flush();
  assert.equal(daemon.checks.length, 1, 'Check this folder must POST /api/factory/projects/check for a typed path');
  assert.equal(daemon.checks[0].folder, '/Users/dev/code/typed-repo');
});

test('folder suggestions combine /api/spaces and known projects, fill the field, and refuse a non-git suggestion', async (t) => {
  const existing = { id: 'proj-known', folder: '/Users/dev/code/known-proj', goal: 'Known project', state: 'stopped', live: false, running: false, counts: {} };
  const daemon = fakeDaemon({ projects: [existing] });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'projects');

  assert.ok(main.textContent.includes(W.suggLbl), 'suggestions must show folders sheepdog knows label');
  for (const p of ['/Users/dev/code/notes', '/Users/dev/code/recipes', '/Users/dev/code/scratch', '/Users/dev/code/known-proj']) {
    const b = main.querySelectorAll('button').find((x) => x.textContent.includes(p));
    assert.ok(b, `expected suggestion button for ${p}`);
  }
  const notGitBtn = main.querySelectorAll('button').find((x) => x.textContent.includes('/Users/dev/code/scratch'));
  assert.ok(notGitBtn.textContent.includes(W.suggNotGit), 'non-git suggestion must carry "not a git repository" mark');
  notGitBtn.click();
  await page.flush();

  assert.equal(fieldByLabel(main, 'folder', page.doc).ctrl.value, '/Users/dev/code/scratch');
  assert.ok(main.querySelectorAll('[role="alert"]').some((a) => a.textContent.includes(W.notGit)), 'picking non-git suggestion must show np.notgit refusal with role="alert"');

  const gitBtn = main.querySelectorAll('button').find((x) => x.textContent.includes('/Users/dev/code/notes'));
  assert.ok(gitBtn.textContent.includes(W.suggGit), 'git suggestion must carry "git repository" mark');
  gitBtn.click();
  await page.flush();
  assert.equal(fieldByLabel(main, 'folder', page.doc).ctrl.value, '/Users/dev/code/notes');
  assert.ok(!main.textContent.includes(W.notGit), 'picking a git repository suggestion must clear np.notgit');
});

test('start checks show intro, running status, every check status with its fix, summary counts, CLI hint, and unreachable error', async (t) => {
  let releaseCheck, checkMode = 'fail';
  const holdCheck = new Promise((r) => { releaseCheck = r; });
  const daemon = fakeDaemon({
    projects: [],
    onCheck: async () => {
      if (checkMode === 'hold') { await holdCheck; return { status: 200, body: { results: mixedFailChecks() } }; }
      if (checkMode === 'onefail') return { status: 200, body: { results: [{ name: 'git version', status: 'ok', detail: 'git version 2.47.1' }, { name: 'start git', status: 'fail', detail: 'git failed: not a git repo', fix: 'run git rev-parse --show-toplevel in the project folder' }] } };
      if (checkMode === 'warn') return { status: 200, body: { results: passWithWarnChecks() } };
      if (checkMode === 'pass') return { status: 200, body: { results: allPassChecks() } };
      if (checkMode === 'err') throw new TypeError('Failed to fetch');
      return { status: 200, body: { results: mixedFailChecks() } };
    },
  });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');
  assert.ok(main.textContent.includes(W.ckH) && main.textContent.includes(W.ckIntro), 'before first check, card shows start checks heading and ck.intro');

  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');

  checkMode = 'hold';
  try {
    btnByText(main, W.checkBtn).click();
    await new Promise((r) => setImmediate(r));
    assert.ok(main.querySelectorAll('[role="status"]').some((s) => s.textContent.includes(W.ckRunning)), 'while checking, card shows Checking the folder… with role="status"');
  } finally { releaseCheck(); }
  await page.flush();

  for (const word of ['pass', 'warning', 'fail', 'skipped']) {
    assert.ok(main.textContent.includes(word), `start checks must show status word "${word}"`);
  }
  assert.ok(main.textContent.includes('agy is not signed in'), 'check detail must appear as written');
  assert.ok(main.textContent.includes(W.ckFix) && main.textContent.includes('run agy without arguments and sign in'), 'plain fix must render under Fix:');
  assert.ok(main.querySelectorAll('[role="alert"]').some((a) => a.textContent.includes('2 checks failed. Fix them, then check again.')), '2 failed checks must show plural failure summary as role="alert"');
  assert.ok(main.textContent.includes(W.ckModels), 'failed CLI check must show ck.models hint pointing to models rows');

  checkMode = 'onefail';
  btnByText(main, W.checkAgain).click();
  await page.flush();
  assert.ok(main.querySelectorAll('[role="alert"]').some((a) => a.textContent.includes('1 check failed. Fix it, then check again.')), '1 failed check must show singular failure summary');

  checkMode = 'warn';
  btnByText(main, W.checkAgain).click();
  await page.flush();
  assert.ok(main.querySelectorAll('[role="status"]').some((s) => s.textContent.includes('6 pass, 1 warning. You can start.')), 'pass+warning summary must show ck.sum.warn as role="status"');

  checkMode = 'pass';
  btnByText(main, W.checkAgain).click();
  await page.flush();
  assert.ok(main.querySelectorAll('[role="status"]').some((s) => s.textContent.includes('All 3 checks pass.')), 'all-pass summary must show All N checks pass. as role="status"');

  checkMode = 'err';
  btnByText(main, W.checkAgain).click();
  await page.flush();
  assert.ok(main.querySelectorAll('[role="alert"]').some((a) => a.textContent.includes(W.ckCant)), 'failed/timed-out check request must show ck.cant with role="alert"');
});

test('run: fix renders in a copyable terminal block following the cut rule and Copy copies only the command', async (t) => {
  const copied = [];
  const prevNav = Object.getOwnPropertyDescriptor(globalThis, 'navigator');
  t.after(() => { if (prevNav) Object.defineProperty(globalThis, 'navigator', prevNav); else delete globalThis.navigator; });
  Object.defineProperty(globalThis, 'navigator', {
    value: { userAgent: '', maxTouchPoints: 0, clipboard: { writeText: async (txt) => { copied.push(txt); } } },
    configurable: true,
    writable: true,
  });

  const checksWithRuns = [
    { name: 'start fetch', status: 'warn', detail: 'stale refs', fix: 'run: git fetch origin (sign in to git if it asks)' },
    { name: 'start claude', status: 'fail', detail: 'claude is not signed in', fix: 'run: claude auth login, then run doctor again' },
    { name: 'gh push access', status: 'fail', detail: 'not signed in', fix: 'run: gh auth login' },
  ];
  const daemon = fakeDaemon({ projects: [], onCheck: () => ({ status: 200, body: { results: checksWithRuns } }) });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');
  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  btnByText(main, W.checkBtn).click();
  await page.flush();

  assert.ok(main.textContent.includes(W.ckFixCmd), 'run: fix must be marked "Fix, in a terminal:"');
  const codes = main.querySelectorAll('code').map((c) => c.textContent.trim());
  assert.ok(codes.includes('git fetch origin'), 'run: fix cut at " (" must put only "git fetch origin" in <code>');
  assert.ok(codes.includes('claude auth login'), 'run: fix cut at ", then " must put only "claude auth login" in <code>');
  assert.ok(codes.includes('gh auth login'), 'plain run: fix must put "gh auth login" in <code>');
  assert.ok(!codes.some((c) => c.includes('sign in to git') || c.includes('then run doctor') || c.startsWith('run:')), 'trailing prose and "run:" prefix must stay outside <code>');
  assert.ok(main.textContent.includes('(sign in to git if it asks)'), 'trailing text after " (" must render as plain text under the block');
  assert.ok(main.textContent.includes('then run doctor again'), 'trailing text after ", then " must render as plain text under the block');

  const copyFetch = main.querySelector('button[aria-label="Copy the command git fetch origin"]');
  assert.ok(copyFetch && copyFetch.textContent.trim() === 'Copy', 'expected Copy button with exact aria-label');
  copyFetch.click();
  await page.flush();
  assert.deepEqual(copied, ['git fetch origin'], 'Copy must copy only the command block');
  assert.ok(main.querySelectorAll('[role="status"]').some((s) => s.textContent.includes('Copied')) || main.textContent.includes('Copied'), 'Copy must announce Copied');

  const copyClaude = main.querySelector('button[aria-label="Copy the command claude auth login"]');
  assert.ok(copyClaude, 'expected Copy button for ", then " cut command');
  copyClaude.click();
  await page.flush();
  assert.deepEqual(copied, ['git fetch origin', 'claude auth login']);
});

test('start stays disabled before checks and while a check fails, enables after Check again passes, refuses empty goal, and opens new project on start', async (t) => {
  let passNow = false, releaseStart;
  const holdStart = new Promise((r) => { releaseStart = r; });
  const daemon = fakeDaemon({
    projects: [],
    paired: true,
    onCheck: () => ({ status: 200, body: { results: passNow ? passWithWarnChecks() : mixedFailChecks() } }),
    onStart: async () => { await holdStart; return null; },
  });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');

  const checkOff = (label) => {
    const startBtn = btnByText(main, W.startBtn);
    assert.ok(startBtn, 'Start the project and its agents button must exist');
    assert.equal(startBtn.getAttribute('aria-disabled'), 'true', `${label}: start button must have aria-disabled="true"`);
    assert.equal(startBtn.disabled, false, `${label}: start button stays focusable (aria-disabled, not disabled)`);
    assert.ok(main.textContent.includes(W.startOff), `${label}: must show np.start.off hint`);
    const descIds = (startBtn.getAttribute('aria-describedby') || '').split(/\s+/).filter(Boolean);
    assert.ok(descIds.some((id) => page.doc.getElementById(id)?.textContent.includes(W.startOff)), `${label}: start button must be described by np.start.off`);
    startBtn.click();
  };

  assert.ok(main.textContent.includes(W.quotaHint), 'quota hint must appear under buttons');
  checkOff('before checks');
  await page.flush();
  assert.equal(daemon.starts.length, 0, 'clicking start before checks must not POST /start');

  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  btnByText(main, W.checkBtn).click();
  await page.flush();

  assert.ok(btnByText(main, W.checkAgain), 'button must change to Check again after checks run');
  checkOff('while check fails');
  await page.flush();
  assert.equal(daemon.starts.length, 0, 'clicking start while a check fails must not POST /start');

  passNow = true;
  btnByText(main, W.checkAgain).click();
  await page.flush();

  const enabledStart = btnByText(main, W.startBtn);
  assert.ok(enabledStart && enabledStart.getAttribute('aria-disabled') !== 'true' && !enabledStart.disabled, 'start must enable once Check again passes (warnings allowed)');
  assert.ok(!main.textContent.includes(W.startOff), 'np.start.off hint must hide once start is enabled');

  // Empty goal refuses locally and moves focus to the goal textarea.
  enabledStart.click();
  await page.flush();
  assert.equal(daemon.starts.length, 0, 'empty goal must not POST /start');
  assert.ok(main.querySelectorAll('[role="alert"]').some((a) => a.textContent.includes(W.goalEmpty)), 'empty goal must show Write a goal first. with role="alert"');
  assert.strictEqual(page.doc.activeElement, fieldByLabel(main, 'goal', page.doc).ctrl, 'empty goal must move focus to goal textarea');

  // Fill goal and start.
  await setField(page, fieldByLabel(main, 'goal', page.doc).ctrl, 'A notes page with search');
  assert.ok(main.textContent.includes(W.goalHint), 'goal field must show np.goal.hint');
  try {
    btnByText(main, W.startBtn).click();
    await new Promise((r) => setImmediate(r));
    assert.ok(btnByText(main, W.starting) || main.textContent.includes(W.starting), 'start button must show Starting… while start is in flight');
  } finally { releaseStart(); }
  await page.flush();

  assert.equal(daemon.starts.length, 1);
  assert.equal(daemon.starts[0].folder, '/Users/dev/code/notes');
  assert.equal(daemon.starts[0].goal, 'A notes page with search');
  assert.ok(main.textContent.includes('A notes page with search'), 'after start, page must show the new project board');
  assert.ok(main.querySelectorAll('[role="status"]').some((s) => s.textContent.includes(W.started)), 'after start, board must announce np.started with role="status"');
});

test('models editor in the form uses Done, updates row with changed mark, and sends chosen Top settings on check and start', async (t) => {
  const daemon = fakeDaemon({ projects: [], paired: true });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');

  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  await setField(page, fieldByLabel(main, 'goal', page.doc).ctrl, 'Ship search');
  await setField(page, fieldByLabel(main, 'max running', page.doc).ctrl, '1');

  const wEdit = main.querySelector('button[aria-label="Edit worker models"]');
  assert.ok(wEdit, 'worker models row must offer Edit button');
  wEdit.click();
  await page.flush();

  const wRowOpen = modelRow(main, 'models.worker', 'worker models');
  assert.ok(wRowOpen, 'worker models row must exist while editing');
  assert.ok(btnByText(wRowOpen, 'Done'), 'models editor in form must use Done button');
  assert.equal(btnByText(wRowOpen, 'Change'), null, 'models editor in form must NOT show Change button');

  const cliSel = wRowOpen.querySelector('select[aria-label="CLI, first choice"]') || wRowOpen.querySelector('select');
  await setField(page, cliSel, 'agy', 'change');
  const modIn = modelRow(main, 'models.worker', 'worker models').querySelector('input[aria-label="model, first choice"]') || modelRow(main, 'models.worker', 'worker models').querySelector('input');
  await setField(page, modIn, 'gemini-4-neon', 'input');
  btnByText(modelRow(main, 'models.worker', 'worker models'), 'Done').click();
  await page.flush();

  assert.equal(daemon.proposed.length, 0, 'Done in form must send no POST to /settings');
  const wRowClosed = modelRow(main, 'models.worker', 'worker models');
  assert.ok(wRowClosed.textContent.includes('agy / gemini-4-neon / medium'), 'row after Done must show chosen candidate');
  assert.match(wRowClosed.textContent, /\bchanged\b/, 'row after Done must show changed mark');

  btnByText(main, W.checkBtn).click();
  await page.flush();
  assert.equal(daemon.checks.length, 1);
  assert.equal(daemon.checks[0].set?.['models.worker'], 'agy/gemini-4-neon/medium');
  assert.equal(daemon.checks[0].set?.max_running, '1');

  btnByText(main, W.startBtn).click();
  await page.flush();
  assert.equal(daemon.starts.length, 1);
  assert.equal(daemon.starts[0].set?.['models.worker'], 'agy/gemini-4-neon/medium');
  assert.equal(daemon.starts[0].set?.max_running, '1');
});

test('refused start shows daemon sentence, refused setting pair shows under its row, and start-time check failure disables start', async (t) => {
  let checkReply = null, startReply = null;
  const daemon = fakeDaemon({
    projects: [],
    paired: true,
    onCheck: () => checkReply || { status: 200, body: { results: allPassChecks() } },
    onStart: () => startReply,
  });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');

  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  await setField(page, fieldByLabel(main, 'goal', page.doc).ctrl, 'Build search');

  // 1. Refused setting pair {name, message} from check shows under that row (np.r.row).
  const rowMsg = 'worker models: a model name cannot hold a comma';
  checkReply = { status: 400, body: { name: 'models.worker', message: rowMsg, error: rowMsg } };
  btnByText(main, W.checkBtn).click();
  await page.flush();
  const wRow = modelRow(main, 'models.worker', 'worker models');
  assert.ok(wRow?.querySelector('[role="alert"]')?.textContent.includes(rowMsg), 'refused {name, message} must appear as role="alert" under the named row');

  // 2. Checks pass, then start returns a plain daemon refusal sentence.
  checkReply = null;
  (btnByText(main, W.checkAgain) || btnByText(main, W.checkBtn)).click();
  await page.flush();

  const daemonSentence = 'Not started: a setting is not allowed. Change it in settings below.';
  startReply = { status: 400, body: { error: daemonSentence } };
  btnByText(main, W.startBtn).click();
  await page.flush();
  assert.ok(main.querySelectorAll('[role="alert"]').some((a) => a.textContent.includes(daemonSentence)), 'refused start must show the daemon sentence as role="alert"');
  await page.pollNow();
  assert.ok(main.querySelectorAll('[role="alert"]').some((a) => a.textContent.includes(daemonSentence)), 'start refusal must survive the 3 s poll');
  assert.equal(fieldByLabel(main, 'goal', page.doc).ctrl.value, 'Build search', 'refused start must keep typed goal');

  // 3. Refused setting pair {name, message} from start also shows under the named row.
  const maxMsg = 'max running 99: allowed 1–20';
  startReply = { status: 400, body: { name: 'max_running', message: maxMsg, error: maxMsg } };
  btnByText(main, W.startBtn).click();
  await page.flush();
  const maxRow = main.querySelector('[data-row="max_running"]') || fieldByLabel(main, 'max running', page.doc).ctrl.closest('.srow');
  assert.ok(maxRow?.querySelector('[role="alert"]')?.textContent.includes(maxMsg), 'refused {name, message} on start must show under max_running row');

  // 4. Start refused because a start check failed at start time updates results and turns Start off.
  (btnByText(main, W.checkAgain) || btnByText(main, W.checkBtn)).click();
  await page.flush();
  startReply = { status: 400, body: { error: 'not starting; fix these first', results: mixedFailChecks() } };
  btnByText(main, W.startBtn).click();
  await page.flush();
  assert.ok(main.textContent.includes('agy is not signed in'), 'start-time failed checks must render in the checks card');
  assert.equal(btnByText(main, W.startBtn)?.getAttribute('aria-disabled'), 'true', 'start-time check failure must turn Start off again');
});

test('form keeps what was typed across 3 s polls and across browser pairing in the form, then resumes start', async (t) => {
  const daemon = fakeDaemon({ projects: [], paired: false });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');

  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  await setField(page, fieldByLabel(main, 'goal', page.doc).ctrl, 'Keep everything typed');
  await setField(page, fieldByLabel(main, 'max running', page.doc).ctrl, '2');

  main.querySelector('button[aria-label="Edit worker models"]').click();
  await page.flush();
  const wOpen = modelRow(main, 'models.worker', 'worker models');
  await setField(page, wOpen.querySelector('select[aria-label="CLI, first choice"]') || wOpen.querySelector('select'), 'agy', 'change');
  const wOpen2 = modelRow(main, 'models.worker', 'worker models');
  await setField(page, wOpen2.querySelector('input[aria-label="model, first choice"]') || wOpen2.querySelector('input'), 'gemini-4-neon', 'input');
  btnByText(modelRow(main, 'models.worker', 'worker models'), 'Done').click();
  await page.flush();

  // Check works without pairing.
  btnByText(main, W.checkBtn).click();
  await page.flush();
  assert.equal(main.querySelector('form[data-pair-form]'), null, 'Check this folder must never ask for pairing');
  assert.equal(daemon.checks.length, 1);

  // 3 s poll keeps all typed fields, edited model and passing checks.
  await page.pollNow();
  assert.equal(main.querySelector('h1')?.textContent.trim(), W.npH, 'form must stay open across 3 s poll');
  assert.equal(fieldByLabel(main, 'folder', page.doc).ctrl.value, '/Users/dev/code/notes');
  assert.equal(fieldByLabel(main, 'goal', page.doc).ctrl.value, 'Keep everything typed');
  assert.equal(fieldByLabel(main, 'max running', page.doc).ctrl.value, '2');
  assert.ok(modelRow(main, 'models.worker', 'worker models').textContent.includes('agy / gemini-4-neon / medium'), 'edited models row must survive 3 s poll');
  assert.ok(btnByText(main, W.startBtn).getAttribute('aria-disabled') !== 'true', 'passing checks must survive 3 s poll');

  // Start on unpaired browser opens pairing box inside the form (not in #asks) and keeps typed values.
  btnByText(main, W.startBtn).click();
  await page.flush();
  const pairBox = main.querySelector('[role="region"][aria-label="pair this browser"]');
  assert.ok(pairBox, 'pairing box must appear inside the new-project form');
  assert.equal(page.doc.getElementById('asks').querySelector('form[data-pair-form]'), null, 'pairing box must not appear in #asks');
  for (const s of [W.pairH, W.pairP, W.pairKept]) assert.ok(pairBox.textContent.includes(s), `pairing box missing "${s}"`);

  // Poll while pairing box is open keeps both the pairing box and typed fields.
  await page.pollNow();
  assert.ok(main.querySelector('form[data-pair-form]'), 'pairing box must survive 3 s poll');
  assert.equal(fieldByLabel(main, 'folder', page.doc).ctrl.value, '/Users/dev/code/notes');
  assert.equal(fieldByLabel(main, 'goal', page.doc).ctrl.value, 'Keep everything typed');
  assert.equal(fieldByLabel(main, 'max running', page.doc).ctrl.value, '2');

  // Wrong code stays in pairing box; valid code pairs and automatically resumes Start with kept values.
  main.querySelector('input[name="code"]').value = 'BAD-CODE';
  main.querySelector('form[data-pair-form]').dispatchEvent({ type: 'submit', bubbles: true });
  await page.flush();
  assert.ok(main.querySelector('[role="region"][aria-label="pair this browser"] [role="alert"]')?.textContent.includes(W.pairWrong), 'wrong code must show pr.wrong alert');

  main.querySelector('input[name="code"]').value = 'ABCD-EFGH';
  main.querySelector('form[data-pair-form]').dispatchEvent({ type: 'submit', bubbles: true });
  await page.flush();
  assert.equal(daemon.starts.length, 1, 'pairing must automatically resume Start without retyping');
  assert.equal(daemon.starts[0].folder, '/Users/dev/code/notes');
  assert.equal(daemon.starts[0].goal, 'Keep everything typed');
  assert.equal(daemon.starts[0].set?.max_running, '2');
  assert.equal(daemon.starts[0].set?.['models.worker'], 'agy/gemini-4-neon/medium');
  assert.ok(main.querySelectorAll('[role="status"]').some((s) => s.textContent.includes(W.started)), 'resumed start must show new project with np.started status');
});

test('project.js exports renderNewProjectHTML, renderChecksHTML, checkProject and startProject, escapes HTML, holds no hard-coded setting names, and app.js has no POST', async () => {
  const mod = await import('./project.js');
  for (const fn of ['renderNewProjectHTML', 'renderChecksHTML', 'checkProject', 'startProject']) {
    assert.equal(typeof mod[fn], 'function', `project.js must export ${fn}`);
  }

  const checksHTML = mod.renderChecksHTML([
    { name: '<script>x</script>', status: 'fail', detail: '<img src=x onerror=alert(1)>', fix: 'run: gh auth login (for <owner>)' },
    { name: 'ok check', status: 'ok', detail: 'fine' },
  ]);
  assert.ok(!checksHTML.includes('<script>x</script>') && !checksHTML.includes('<img src=x'), 'renderChecksHTML must escape HTML');
  assert.ok(checksHTML.includes('Fix, in a terminal:') && checksHTML.includes('gh auth login'), 'renderChecksHTML must render run: fix in terminal block');

  const formHTML = mod.renderNewProjectHTML(defaultRows(), {});
  assert.ok(formHTML.includes('max running') && !formHTML.includes('review rounds'), 'renderNewProjectHTML must draw Top rows only');

  const flow = createAskFlow({
    load: async () => null,
    fetch: async (u) => u === '/api/hello'
      ? { ok: true, status: 200, json: async () => ({ computer: { id: 'D1' } }) }
      : { ok: false, status: 403, json: async () => ({ code: 'proof_required', error: 'proof_required' }) },
  });
  const res = await flow.send('/api/factory/projects/start', { folder: '/Users/dev/code/notes', goal: 'Build search', set: { max_running: '1' } });
  assert.equal(res.needsPairing, true, 'asks.js:send must return needsPairing on 403 proof_required for /projects/start');

  const projCode = readFileSync(new URL('./project.js', import.meta.url), 'utf8').replace(/\/\*[\s\S]*?\*\/|\/\/.*$/gm, '');
  assert.doesNotMatch(projCode, /models\.(?:coordinator|worker|reviewer|retro|plan_reviewer)|\bmax_running\b/, 'project.js must hold no hard-coded setting names or defaults');
  const appSrc = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
  assert.ok(!/\bPOST\b|method\s*:/.test(appSrc), 'app.js must hold no POST');
});

const wrapDaemon = (base, fetch) => Object.assign(Object.create(base), { fetch });
async function seedEnrolledKey(daemonId = 'D1', deviceId = 'dev-1') {
  const pair = await globalThis.crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign']);
  await keyStore(globalThis.indexedDB).save(daemonId, { deviceId, key: pair.privateKey });
}

test('starting a path canonicalized by git switches to the new project and announces np.started', async (t) => {
  const existing = { id: 'proj-old', folder: '/Users/dev/code/old', goal: 'Old project', state: 'running', live: true, running: true, counts: {} };
  const daemon = fakeDaemon({
    projects: [existing],
    paired: true,
    onStart: (body, projs) => {
      projs.push({
        id: 'proj-new',
        folder: body.folder.replace(/\/+$/, '').replace(/^\/tmp\//, '/private/tmp/'),
        goal: body.goal,
        state: 'running',
        live: true,
        running: true,
        counts: {},
      });
      return { status: 200, body: { ok: true, device_id: 'dev-1' } };
    },
  });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'projects');
  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/tmp/new-repo/');
  await setField(page, fieldByLabel(main, 'goal', page.doc).ctrl, 'Canonical repo goal');
  btnByText(main, W.checkBtn).click();
  await page.flush();
  btnByText(main, W.startBtn).click();
  await page.flush();

  assert.ok(main.textContent.includes('Canonical repo goal'), 'page must switch to the newly started project even when git canonicalized the folder');
  assert.ok(main.querySelectorAll('[role="status"]').some((s) => s.textContent.includes(W.started)), 'page must announce np.started with role="status"');
});

test('editing models and clicking Done after checks pass disables Start until re-checked', async (t) => {
  const daemon = fakeDaemon({ projects: [], paired: true });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');
  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  btnByText(main, W.checkBtn).click();
  await page.flush();
  assert.notEqual(btnByText(main, W.startBtn).getAttribute('aria-disabled'), 'true');

  main.querySelector('button[aria-label="Edit worker models"]').click();
  await page.flush();
  const w = modelRow(main, 'models.worker', 'worker models');
  await setField(page, w.querySelector('select[aria-label="CLI, first choice"]'), 'codex', 'change');
  await setField(page, modelRow(main, 'models.worker', 'worker models').querySelector('input[aria-label="model, first choice"]'), 'gpt-5', 'input');
  btnByText(modelRow(main, 'models.worker', 'worker models'), 'Done').click();
  await page.flush();

  assert.equal(btnByText(main, W.startBtn).getAttribute('aria-disabled'), 'true', 'changing models after checks pass must disable Start until re-checked');
});

test('opening the models editor after checks pass disables Start while the editor is open', async (t) => {
  const daemon = fakeDaemon({ projects: [], paired: true });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');
  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  btnByText(main, W.checkBtn).click();
  await page.flush();
  assert.notEqual(btnByText(main, W.startBtn).getAttribute('aria-disabled'), 'true');

  main.querySelector('button[aria-label="Edit worker models"]').click();
  await page.flush();
  assert.equal(btnByText(main, W.startBtn).getAttribute('aria-disabled'), 'true', 'Start must be disabled while a models editor is open');
});

test('refused setting pair on Check again disables Start and clears passing summary', async (t) => {
  let refuse = false;
  const daemon = fakeDaemon({
    projects: [],
    onCheck: () => refuse
      ? { status: 400, body: { name: 'max_running', message: 'max running 99: allowed 1–20', error: 'max running 99: allowed 1–20' } }
      : { status: 200, body: { results: allPassChecks() } },
  });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');
  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  btnByText(main, W.checkBtn).click();
  await page.flush();
  assert.notEqual(btnByText(main, W.startBtn).getAttribute('aria-disabled'), 'true');

  refuse = true;
  await setField(page, fieldByLabel(main, 'max running', page.doc).ctrl, '99');
  (btnByText(main, W.checkAgain) || btnByText(main, W.checkBtn)).click();
  await page.flush();

  assert.equal(btnByText(main, W.startBtn).getAttribute('aria-disabled'), 'true', 'refused {name, message} on check must disable Start');
  assert.ok(!main.textContent.includes('All 3 checks pass.'), 'refused {name, message} on check must clear stale passing summary');
});

test('refused setting pair on Start disables Start until the setting is fixed', async (t) => {
  const daemon = fakeDaemon({
    projects: [],
    paired: true,
    onStart: () => ({ status: 400, body: { name: 'max_running', message: 'max running 99: allowed 1–20', error: 'max running 99: allowed 1–20' } }),
  });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');
  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  await setField(page, fieldByLabel(main, 'goal', page.doc).ctrl, 'Build search');
  btnByText(main, W.checkBtn).click();
  await page.flush();
  btnByText(main, W.startBtn).click();
  await page.flush();

  assert.equal(btnByText(main, W.startBtn).getAttribute('aria-disabled'), 'true', 'refused {name, message} on start must disable Start');
});

test('passing Check this folder clears prior non-git suggestion refusal and enables Start', async (t) => {
  const page = await bootPage(t, fakeDaemon({ projects: [] }));
  const main = await openNewProject(page, 'main');
  main.querySelector('button[data-sugg="/Users/dev/code/scratch"]').click();
  await page.flush();
  assert.ok(main.textContent.includes(W.notGit));

  btnByText(main, W.checkBtn).click();
  await page.flush();
  assert.ok(!main.textContent.includes(W.notGit), 'passing start checks must clear prior np.notgit alert');
  assert.notEqual(btnByText(main, W.startBtn).getAttribute('aria-disabled'), 'true', 'Start must enable once checks pass');
});

test('folder suggestion buttons expose comma-separated path and git status label', async (t) => {
  const page = await bootPage(t, fakeDaemon({ projects: [] }));
  const main = await openNewProject(page, 'main');
  const notes = main.querySelector('button[data-sugg="/Users/dev/code/notes"]');
  const scratch = main.querySelector('button[data-sugg="/Users/dev/code/scratch"]');
  assert.equal(notes.getAttribute('aria-label') || notes.textContent.trim(), '/Users/dev/code/notes, git repository');
  assert.equal(scratch.getAttribute('aria-label') || scratch.textContent.trim(), '/Users/dev/code/scratch, not a git repository');
});

test('activating a non-first folder suggestion keeps keyboard focus on that suggestion', async (t) => {
  const page = await bootPage(t, fakeDaemon({ projects: [] }));
  const main = await openNewProject(page, 'main');
  const scratch = main.querySelector('button[data-sugg="/Users/dev/code/scratch"]');
  scratch.focus();
  scratch.click();
  await page.flush();
  assert.equal(page.doc.activeElement?.dataset?.sugg, '/Users/dev/code/scratch', 'clicking 3rd suggestion must not jump focus to 1st suggestion');
});

test('typing in goal after empty-goal refusal keeps focus on live #np-goal', async (t) => {
  const page = await bootPage(t, fakeDaemon({ projects: [], paired: true }));
  const main = await openNewProject(page, 'main');
  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  btnByText(main, W.checkBtn).click();
  await page.flush();

  btnByText(main, W.startBtn).click();
  await page.flush();
  const goalBefore = fieldByLabel(main, 'goal', page.doc).ctrl;
  assert.strictEqual(page.doc.activeElement, goalBefore);

  await setField(page, goalBefore, 'A');
  const goalAfter = fieldByLabel(main, 'goal', page.doc).ctrl;
  assert.strictEqual(page.doc.activeElement, goalAfter, 'first keystroke after goal refusal must keep focus on live #np-goal');
});

test('typing in max running inside the new-project form keeps the same input DOM node', async (t) => {
  const page = await bootPage(t, fakeDaemon({ projects: [], paired: true }));
  const main = await openNewProject(page, 'main');
  const maxBefore = fieldByLabel(main, 'max running', page.doc).ctrl;
  maxBefore.focus();
  await setField(page, maxBefore, '1');
  assert.strictEqual(fieldByLabel(main, 'max running', page.doc).ctrl, maxBefore, 'typing in max running must update row metadata in place without replacing the <input>');
  await setField(page, maxBefore, '12');
  assert.strictEqual(fieldByLabel(main, 'max running', page.doc).ctrl, maxBefore, 'typing a second digit must keep the same <input> node');
});

test('pressing Check this folder keeps keyboard focus inside the form after checks pass', async (t) => {
  const page = await bootPage(t, fakeDaemon({ projects: [] }));
  const main = await openNewProject(page, 'main');
  const proto = main.constructor.prototype;
  const origFocus = proto.focus, origInner = Object.getOwnPropertyDescriptor(proto, 'innerHTML');
  t.after(() => { proto.focus = origFocus; Object.defineProperty(proto, 'innerHTML', origInner); });
  proto.focus = function () { if (!this.disabled && this.ownerDocument) this.ownerDocument.activeElement = this; };
  Object.defineProperty(proto, 'innerHTML', {
    get: origInner.get,
    configurable: true,
    set(h) {
      if (this.ownerDocument?.activeElement && this.contains(this.ownerDocument.activeElement)) this.ownerDocument.activeElement = null;
      origInner.set.call(this, h);
    },
  });

  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  const checkBtn = btnByText(main, W.checkBtn);
  checkBtn.focus();
  checkBtn.click();
  await page.flush();

  assert.ok(main.contains(page.doc.activeElement), 'focus must stay inside the form after checks pass (not lost to disabled or class change)');
});

test('post-pairing refused Start returns keyboard focus to the Start button', async (t) => {
  const daemon = fakeDaemon({
    projects: [],
    paired: false,
    onStart: () => ({ status: 400, body: { error: 'Not started: a setting is not allowed. Change it in settings below.' } }),
  });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');
  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  await setField(page, fieldByLabel(main, 'goal', page.doc).ctrl, 'Ship');
  btnByText(main, W.checkBtn).click();
  await page.flush();
  btnByText(main, W.startBtn).click();
  await page.flush();

  const codeIn = main.querySelector('input[name="code"]');
  codeIn.value = 'ABCD-EFGH';
  codeIn.focus();
  main.querySelector('form[data-pair-form]').dispatchEvent({ type: 'submit', bubbles: true });
  await page.flush();

  assert.strictEqual(page.doc.activeElement, btnByText(main, W.startBtn), 'after Pair succeeds and Start is refused, focus must return to Start');
});

test('401 session_expired on GET /api/spaces re-auths silently instead of showing herdr unreachable', async (t) => {
  let spacesExpired = true;
  const base = fakeDaemon({ projects: [], paired: true });
  const daemon = wrapDaemon(base, async (path, init = {}) => {
    const [u] = String(path).split('?');
    if (spacesExpired && u === '/api/spaces') {
      return { ok: false, status: 401, json: async () => ({ error: 'unauthorized', reason: 'session_expired' }) };
    }
    if (u === '/api/auth/session') spacesExpired = false;
    return base.fetch(path, init);
  });
  const page = await bootPage(t, daemon);
  await seedEnrolledKey();
  const main = await openNewProject(page, 'main');

  assert.ok(!main.textContent.includes(W.suggNone), '401 session_expired on /api/spaces must re-auth instead of showing herdr unreachable');
  assert.ok(main.querySelector('button[data-sugg="/Users/dev/code/notes"]'), 'suggestions must load after silent re-auth');
});

test('401 session_expired on POST /api/factory/projects/check re-auths silently instead of showing unauthorized', async (t) => {
  let checkExpired = true;
  const base = fakeDaemon({ projects: [], paired: true });
  const daemon = wrapDaemon(base, async (path, init = {}) => {
    const [u] = String(path).split('?');
    if (checkExpired && u === '/api/factory/projects/check') {
      return { ok: false, status: 401, json: async () => ({ error: 'unauthorized', reason: 'session_expired' }) };
    }
    if (u === '/api/auth/session') checkExpired = false;
    return base.fetch(path, init);
  });
  const page = await bootPage(t, daemon);
  await seedEnrolledKey();
  const main = await openNewProject(page, 'main');
  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  btnByText(main, W.checkBtn).click();
  await page.flush();

  assert.ok(!main.textContent.includes('unauthorized'), '401 session_expired on /projects/check must re-auth instead of showing raw unauthorized');
  assert.ok(main.textContent.includes('All 3 checks pass.'), 'check results must render after silent re-auth');
});

test('changing folder to non-git while pairing box is open closes pairing box and drops stale start', async (t) => {
  const daemon = fakeDaemon({ projects: [], paired: false });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');
  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  await setField(page, fieldByLabel(main, 'goal', page.doc).ctrl, 'First goal');
  btnByText(main, W.checkBtn).click();
  await page.flush();
  btnByText(main, W.startBtn).click();
  await page.flush();
  assert.ok(main.querySelector('form[data-pair-form]'));

  main.querySelector('button[data-sugg="/Users/dev/code/scratch"]').click();
  await page.flush();
  assert.equal(main.querySelector('form[data-pair-form]'), null, 'picking a non-git folder while pairing box is open must close the stale pairing box');
});

test('editing goal while pairing box is open does not start with the stale goal on Pair', async (t) => {
  const daemon = fakeDaemon({ projects: [], paired: false });
  const page = await bootPage(t, daemon);
  const main = await openNewProject(page, 'main');
  await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, '/Users/dev/code/notes');
  await setField(page, fieldByLabel(main, 'goal', page.doc).ctrl, 'Typo goal');
  btnByText(main, W.checkBtn).click();
  await page.flush();
  btnByText(main, W.startBtn).click();
  await page.flush();

  await setField(page, fieldByLabel(main, 'goal', page.doc).ctrl, 'Corrected goal');
  const pairForm = main.querySelector('form[data-pair-form]');
  if (pairForm) {
    main.querySelector('input[name="code"]').value = 'ABCD-EFGH';
    pairForm.dispatchEvent({ type: 'submit', bubbles: true });
    await page.flush();
    assert.equal(daemon.starts[0]?.goal, 'Corrected goal', 'resumed start after Pair must use the edited goal, not the stale snapshot');
  }
});

test('renderChecksHTML with an empty results array does not claim All 0 checks pass', async () => {
  const { renderChecksHTML } = await import('./project.js');
  const html = renderChecksHTML([]);
  assert.ok(!html.includes('All 0 checks pass.'), 'empty results array must not claim All 0 checks pass.');
});

test('fixHTML does not mark an empty run: command as Copied before Copy is clicked', async () => {
  const { fixHTML } = await import('./settings.js');
  const html = fixHTML('run: ');
  assert.ok(!html.includes('Copied'), 'empty run: command must not match default copied="" and render Copied before click');
});

test('folder suggestions cap initial visible rows when many folders exist and still let the owner reach the rest', async (t) => {
  const manySpaces = {
    spaces: [],
    folders: Array.from({ length: 40 }, (_, i) => ({
      name: `repo-${String(i + 1).padStart(2, '0')}`,
      path: `/Users/dev/code/repo-${String(i + 1).padStart(2, '0')}`,
      git: true,
    })),
    kinds: [],
    worktrees: true,
    needs_access: [],
  };
  const page = await bootPage(t, fakeDaemon({ projects: [], spaces: manySpaces }));
  const main = await openNewProject(page, 'main');
  const inClosedDetails = (el) => {
    for (let c = el.parentElement; c; c = c.parentElement) if (c.tagName === 'DETAILS' && !c.open) return true;
    return false;
  };
  const visibleSuggs = () => main.querySelectorAll('button[data-sugg]').filter((b) => !b.hidden && !inClosedDetails(b));

  const initial = visibleSuggs();
  assert.ok(initial.length > 0 && initial.length <= 10, `expected at most 10 visible suggestions initially for 40 folders, got ${initial.length}`);

  const target = '/Users/dev/code/repo-35';
  if (!initial.some((b) => b.dataset.sugg === target)) {
    const suggWrap = main.querySelector('.np-sugg') || main;
    const moreDetails = suggWrap.querySelector('details');
    const moreBtn = suggWrap.querySelectorAll('button').find((b) => !b.hasAttribute('data-sugg'));
    if (moreDetails) moreDetails.open = true;
    else if (moreBtn) { moreBtn.click(); await page.flush(); }
    else await setField(page, fieldByLabel(main, 'folder', page.doc).ctrl, 'repo-35');
    assert.ok(visibleSuggs().some((b) => b.dataset.sugg === target), 'owner must be able to reach suggestions beyond the initial cap');
  }
});
