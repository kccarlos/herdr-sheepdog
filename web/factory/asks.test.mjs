// Unit tests for /factory ask cards, on-demand browser pairing, and automatic
// WebCrypto re-authentication across daemon restarts: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { renderAskCardsHTML, createAskFlow } from './asks.js';

const fixture = JSON.parse(readFileSync(new URL('./testdata/asks-fixture.json', import.meta.url), 'utf8'));

function memStore() {
  const m = new Map();
  return {
    save: async (k, v) => m.set(k, v),
    load: async (k) => m.get(k),
    forget: async (k) => m.delete(k),
    m,
  };
}

// fakeFactoryDaemon simulates the sheepdog daemon for /factory ask cards:
// - GET /api/hello returns computer.id
// - POST /api/devices/enroll accepts SHORT-CODE for kind:"browser" and stores the public key
// - GET /api/auth/challenge + POST /api/auth/session verify the WebCrypto P-256 signature
//   and set an in-memory session cookie
// - POST /api/factory/asks/{id}/answer requires an active session cookie (else 403 proof_required
//   when unenrolled, or 401 session_expired after restart() clears sessions)
function fakeFactoryDaemon({ shortCode = 'ABCD-EFGH', daemonId = 'DAEMON_TEST_1' } = {}) {
  const calls = [];
  const answered = [];
  let enrolledPub = null;
  let enrolledDeviceId = '';
  let hasSession = false;
  let hadSessionBeforeRestart = false;

  const signedText = (dId, devId, nonce, unix) =>
    ['sheepdog-auth-v1', dId, '', devId, nonce, String(unix), 'session'].join('\n');

  const reply = (status, obj) => ({
    ok: status >= 200 && status < 300,
    status,
    json: async () => obj,
  });

  const fetch = async (path, init = {}) => {
    calls.push({ method: init.method || 'GET', path });
    const body = init.body ? JSON.parse(init.body) : null;

    if (path === '/api/hello') {
      return reply(200, { computer: { id: daemonId, name: 'Test Mac' } });
    }
    if (path === '/api/factory/asks') {
      return reply(200, fixture);
    }
    if (path === '/api/devices/enroll') {
      const norm = String(body.code || '').replace(/[\s-]+/g, '').toUpperCase();
      const want = shortCode.replace(/[\s-]+/g, '').toUpperCase();
      if (body.kind !== 'browser' || norm !== want) {
        return reply(410, { reason: 'code_used', error: 'Invalid or used code.' });
      }
      const raw = Uint8Array.from(atob(body.public_key.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
      enrolledPub = await webcrypto.subtle.importKey('raw', raw, { name: 'ECDSA', namedCurve: 'P-256' }, true, ['verify']);
      enrolledDeviceId = 'dev-browser-1';
      return reply(200, { device_id: enrolledDeviceId, daemon_id: daemonId, name: body.name || 'Test Browser' });
    }
    if (path === '/api/auth/challenge') {
      return reply(200, { nonce: 'NONCE-1', daemon_id: daemonId, expires_in: 60 });
    }
    if (path === '/api/auth/session') {
      if (!enrolledPub || body.device_id !== enrolledDeviceId || body.client !== 'browser') {
        return reply(401, { reason: 'unknown_device' });
      }
      const sig = Uint8Array.from(atob(body.signature.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
      const ok = await webcrypto.subtle.verify(
        { name: 'ECDSA', hash: 'SHA-256' },
        enrolledPub,
        sig,
        new TextEncoder().encode(signedText(daemonId, body.device_id, body.nonce, body.time)),
      );
      if (!ok) return reply(401, { reason: 'bad_signature' });
      hasSession = true;
      hadSessionBeforeRestart = false;
      return reply(200, { expires_in: 43200, device: { id: enrolledDeviceId, name: 'Test Browser' } });
    }
    const m = path.match(/^\/api\/factory\/asks\/([^/]+)\/answer$/);
    if (m && (init.method || 'GET') === 'POST') {
      if (!hasSession) {
        if (hadSessionBeforeRestart) {
          return reply(401, { error: 'unauthorized', reason: 'session_expired' });
        }
        return reply(403, { code: 'proof_required', error: 'proof_required' });
      }
      answered.push({ id: m[1], option: body.option, device_id: enrolledDeviceId });
      return reply(200, { ok: true, ask_id: m[1], device_id: enrolledDeviceId });
    }
    return reply(404, { error: 'not found' });
  };

  return {
    fetch,
    calls,
    answered,
    restartDaemon() {
      hasSession = false;
      hadSessionBeforeRestart = true;
    },
  };
}

test('open asks from the fixture render as cards with options, recommendation and escaping', () => {
  const xssAsk = {
    ...fixture.asks[0],
    id: 'ask-xss',
    question: 'Approve <script>alert(1)</script> for T1?',
  };
  const html = renderAskCardsHTML([...fixture.asks, xssAsk], {});
  assert.match(html, /Approve the plan for T1\?/);
  assert.match(html, /Tag and publish v0\.2\.0\?/);
  assert.match(html, /data-ask-id="ask-demo-1"/);
  assert.match(html, /data-option="Approve"/);
  assert.match(html, /T1/);
  assert.match(html, /Recommended/i);
  assert.ok(!html.includes('<script>alert(1)</script>'), 'ask question must be HTML-escaped');
  assert.ok(html.includes('&#60;script&#62;') || html.includes('&lt;script&gt;'), 'escaped script tag expected');
});

test('the /factory page replaces the read-only ask placeholder and fetches /api/factory/asks', () => {
  const html = readFileSync(new URL('./index.html', import.meta.url), 'utf8');
  const js = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
  assert.ok(!html.includes('Ask cards arrive in a later version'), 'index.html still has the read-only ask placeholder');
  assert.ok(js.includes('/api/factory/asks'), 'app.js does not fetch /api/factory/asks');
});

test('answering from an unenrolled browser shows how to pair and never auto-fetches a pairing code', async () => {
  const daemon = fakeFactoryDaemon();
  const store = memStore();
  const flow = createAskFlow({
    fetch: daemon.fetch,
    subtle: webcrypto.subtle,
    now: () => 1_763_812_800_000,
    ua: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15',
    touch: 0,
    ...store,
  });

  const res = await flow.answer(fixture.asks[0], 'Approve');
  assert.equal(res.ok, false);
  assert.equal(res.needsPairing, true);
  assert.match(res.message || '', /menu.?bar|pair/i, 'must explain how to pair from the Mac menu-bar app');
  assert.equal(daemon.answered.length, 0);
  assert.ok(
    !daemon.calls.some((c) => c.path.startsWith('/api/pair')),
    'must never fetch a pairing code automatically',
  );

  // Pairing on demand with a short code stores a non-extractable WebCrypto key and allows answering.
  const paired = await flow.pairWithCode('ABCD-EFGH');
  assert.equal(paired.ok, true);
  const rec = store.m.get('DAEMON_TEST_1');
  assert.ok(rec && rec.deviceId === 'dev-browser-1');
  assert.equal(rec.key.extractable, false, 'browser key must be non-extractable');

  const res2 = await flow.answer(fixture.asks[0], 'Approve');
  assert.equal(res2.ok, true);
  assert.deepEqual(daemon.answered, [{ id: 'ask-demo-1', option: 'Approve', device_id: 'dev-browser-1' }]);
});

test('after a daemon restart an enrolled browser re-authenticates with its WebCrypto key without pairing again', async () => {
  const daemon = fakeFactoryDaemon();
  const store = memStore();
  const flow = createAskFlow({
    fetch: daemon.fetch,
    subtle: webcrypto.subtle,
    now: () => 1_763_812_800_000,
    ua: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15',
    touch: 0,
    ...store,
  });

  // Pair once and answer the first ask.
  const paired = await flow.pairWithCode('ABCD-EFGH');
  assert.equal(paired.ok, true);
  const first = await flow.answer(fixture.asks[0], 'Approve');
  assert.equal(first.ok, true);

  // Simulate daemon restart: in-memory session tickets are gone, but devices.json kept the key.
  daemon.restartDaemon();
  const enrollCountBefore = daemon.calls.filter((c) => c.path === '/api/devices/enroll').length;

  // Answering the second ask silently re-authenticates via /api/auth/challenge + /api/auth/session.
  const second = await flow.answer(fixture.asks[1], 'Publish v0.2.0');
  assert.equal(second.ok, true);
  assert.notEqual(second.needsPairing, true);
  assert.equal(
    daemon.calls.filter((c) => c.path === '/api/devices/enroll').length,
    enrollCountBefore,
    'must not re-enroll after a daemon restart',
  );
  assert.deepEqual(daemon.answered[1], { id: 'ask-demo-2', option: 'Publish v0.2.0', device_id: 'dev-browser-1' });
});

// --- Round 2: full /factory page (app.js + asks.js) tests ---

const VOID_TAGS = new Set(['AREA', 'BASE', 'BR', 'COL', 'EMBED', 'HR', 'IMG', 'INPUT', 'LINK', 'META', 'SOURCE', 'TRACK', 'WBR']);

function decodeEntities(s) {
  return String(s ?? '')
    .replace(/&#(\d+);/g, (_, n) => String.fromCharCode(Number(n)))
    .replace(/&#x([0-9a-fA-F]+);/g, (_, h) => String.fromCharCode(parseInt(h, 16)))
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&#39;|&apos;/g, "'")
    .replace(/&amp;/g, '&');
}

function matchCompound(el, token) {
  let rest = token.trim();
  if (!rest) return false;
  if (rest.startsWith('*')) {
    rest = rest.slice(1);
    if (!rest) return true;
  } else {
    const tagMatch = rest.match(/^[a-zA-Z][a-zA-Z0-9-]*/);
    if (tagMatch) {
      if (el.tagName !== tagMatch[0].toUpperCase()) return false;
      rest = rest.slice(tagMatch[0].length);
    }
  }
  while (rest.length > 0) {
    if (rest[0] === '#') {
      const m = rest.match(/^#([A-Za-z0-9_-]+)/);
      if (!m || el.id !== m[1]) return false;
      rest = rest.slice(m[0].length);
    } else if (rest[0] === '.') {
      const m = rest.match(/^\.([A-Za-z0-9_-]+)/);
      if (!m || !el.className.split(/\s+/).includes(m[1])) return false;
      rest = rest.slice(m[0].length);
    } else if (rest[0] === '[') {
      const m = rest.match(/^\[([A-Za-z0-9_:-]+)(?:=(?:"([^"]*)"|'([^']*)'|([^\]]*)))?\]/);
      if (!m) return false;
      const attr = m[1].toLowerCase();
      const want = m[2] ?? m[3] ?? m[4];
      if (want === undefined) {
        if (attr === 'open' ? !el.open : attr === 'disabled' ? !el.disabled : attr === 'hidden' ? !el.hidden : !el.hasAttribute(attr)) {
          return false;
        }
      } else if (el.getAttribute(attr) !== want) {
        return false;
      }
      rest = rest.slice(m[0].length);
    } else {
      return false;
    }
  }
  return true;
}

function splitDescendantSelector(sel) {
  const parts = [];
  let cur = '';
  let bracketDepth = 0;
  let quote = '';
  const src = String(sel || '').trim();
  for (let i = 0; i < src.length; i++) {
    const ch = src[i];
    if (quote) {
      cur += ch;
      if (ch === '\\' && i + 1 < src.length) {
        cur += src[++i];
      } else if (ch === quote) {
        quote = '';
      }
      continue;
    }
    if (ch === '"' || ch === "'") {
      quote = ch;
      cur += ch;
    } else if (ch === '[') {
      bracketDepth++;
      cur += ch;
    } else if (ch === ']') {
      if (bracketDepth > 0) bracketDepth--;
      cur += ch;
    } else if (bracketDepth === 0 && /\s/.test(ch)) {
      if (cur) {
        parts.push(cur);
        cur = '';
      }
    } else {
      cur += ch;
    }
  }
  if (cur) parts.push(cur);
  return parts;
}

function matchSingleSelector(el, sel) {
  const parts = splitDescendantSelector(sel);
  if (!parts.length || !matchCompound(el, parts[parts.length - 1])) return false;
  let cur = el.parentElement;
  for (let i = parts.length - 2; i >= 0; i--) {
    while (cur && !matchCompound(cur, parts[i])) cur = cur.parentElement;
    if (!cur) return false;
    cur = cur.parentElement;
  }
  return true;
}

class MiniElement {
  constructor(tagName, ownerDocument) {
    this.nodeType = 1;
    this.tagName = tagName.toUpperCase();
    this.ownerDocument = ownerDocument;
    this.parentElement = null;
    this._nodes = [];
    this._attrs = new Map();
    this._listeners = new Map();
    this._rawHTML = '';
    this._value = undefined;
    this.selectionStart = 0;
    this.selectionEnd = 0;
    this.dataset = new Proxy({}, {
      get: (_, prop) => {
        if (typeof prop !== 'string') return undefined;
        const k = 'data-' + prop.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase());
        return this._attrs.get(k);
      },
      set: (_, prop, val) => {
        const k = 'data-' + String(prop).replace(/[A-Z]/g, (c) => '-' + c.toLowerCase());
        this.setAttribute(k, String(val));
        return true;
      },
    });
    this.elements = new Proxy({}, {
      get: (_, prop) => {
        if (typeof prop !== 'string') return undefined;
        return this.querySelectorAll('input, button, select, textarea')
          .find((c) => c.getAttribute('name') === prop || c.id === prop);
      },
    });
  }
  get children() { return this._nodes.filter((n) => n.nodeType === 1); }
  get id() { return this._attrs.get('id') || ''; }
  set id(v) { this.setAttribute('id', v); }
  get className() { return this._attrs.get('class') || ''; }
  set className(v) { this.setAttribute('class', v); }
  get hidden() { return this._attrs.has('hidden'); }
  set hidden(v) { if (v) this._attrs.set('hidden', ''); else this._attrs.delete('hidden'); }
  get disabled() { return this._attrs.has('disabled'); }
  set disabled(v) { if (v) this._attrs.set('disabled', ''); else this._attrs.delete('disabled'); }
  get open() { return this._attrs.has('open'); }
  set open(v) { if (v) this._attrs.set('open', ''); else this._attrs.delete('open'); }
  get value() { return this._value !== undefined ? this._value : (this._attrs.get('value') || ''); }
  set value(v) { this._value = String(v ?? ''); }
  get name() { return this._attrs.get('name') || ''; }
  set name(v) { this.setAttribute('name', v); }
  getAttribute(k) {
    const v = this._attrs.get(String(k).toLowerCase());
    return v === undefined ? null : v;
  }
  setAttribute(k, v) { this._attrs.set(String(k).toLowerCase(), String(v)); }
  hasAttribute(k) { return this._attrs.has(String(k).toLowerCase()); }
  removeAttribute(k) { this._attrs.delete(String(k).toLowerCase()); }
  get textContent() {
    return this._nodes.map((n) => (n.nodeType === 3 ? n.textContent : n.textContent)).join('');
  }
  set textContent(v) {
    this._rawHTML = String(v ?? '');
    this._nodes = v ? [{ nodeType: 3, textContent: String(v) }] : [];
  }
  get innerHTML() { return this._rawHTML; }
  set innerHTML(html) {
    const src = String(html ?? '');
    this._rawHTML = src;
    this._nodes = parseHTMLFragment(src, this.ownerDocument, this);
  }
  contains(other) {
    for (let cur = other; cur; cur = cur.parentElement) {
      if (cur === this) return true;
    }
    return false;
  }
  matches(selector) {
    return String(selector).split(',').some((s) => matchSingleSelector(this, s));
  }
  closest(selector) {
    for (let cur = this; cur; cur = cur.parentElement) {
      if (cur.nodeType === 1 && cur.matches(selector)) return cur;
    }
    return null;
  }
  querySelectorAll(selector) {
    const out = [];
    const walk = (node) => {
      for (const ch of node.children) {
        if (ch.matches(selector)) out.push(ch);
        walk(ch);
      }
    };
    walk(this);
    return out;
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  focus() { if (this.ownerDocument) this.ownerDocument.activeElement = this; }
  setSelectionRange(start, end) { this.selectionStart = start; this.selectionEnd = end; }
  addEventListener(type, fn) {
    if (!this._listeners.has(type)) this._listeners.set(type, []);
    this._listeners.get(type).push(fn);
  }
  dispatchEvent(ev) {
    const e = ev || { type: '' };
    if (!e.target) e.target = this;
    e.defaultPrevented = Boolean(e.defaultPrevented);
    e.preventDefault ||= () => { e.defaultPrevented = true; };
    e.stopPropagation ||= () => { e._stopped = true; };
    for (let cur = this; cur && !e._stopped; cur = cur.parentElement) {
      for (const fn of cur._listeners.get(e.type) || []) fn(e);
    }
    return !e.defaultPrevented;
  }
  click() {
    if (!this.disabled) this.dispatchEvent({ type: 'click', bubbles: true });
  }
}

function parseHTMLFragment(html, doc, parent) {
  const root = { _nodes: [], parentElement: null };
  const stack = [root];
  const tagRe = /<!--[\s\S]*?-->|<\/([a-zA-Z][a-zA-Z0-9-]*)\s*>|<([a-zA-Z][a-zA-Z0-9-]*)((?:\s+[A-Za-z_:][-A-Za-z0-9_:.]*(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>`]+))?)*)\s*\/?>/g;
  let last = 0;
  let m;
  while ((m = tagRe.exec(html)) !== null) {
    if (m.index > last) {
      stack[stack.length - 1]._nodes.push({ nodeType: 3, textContent: decodeEntities(html.slice(last, m.index)) });
    }
    last = tagRe.lastIndex;
    if (m[0].startsWith('<!--')) continue;
    if (m[1]) {
      const closeTag = m[1].toUpperCase();
      for (let i = stack.length - 1; i > 0; i--) {
        if (stack[i].tagName === closeTag) {
          stack.length = i;
          break;
        }
      }
      continue;
    }
    const el = new MiniElement(m[2], doc);
    const parentNode = stack[stack.length - 1];
    el.parentElement = parentNode === root ? parent : parentNode;
    parentNode._nodes.push(el);
    const attrSrc = m[3] || '';
    const attrRe = /([A-Za-z_:][-A-Za-z0-9_:.]*)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+)))?/g;
    let am;
    while ((am = attrRe.exec(attrSrc)) !== null) {
      const rawVal = am[2] ?? am[3] ?? am[4] ?? '';
      el.setAttribute(am[1], decodeEntities(rawVal));
    }
    if (!VOID_TAGS.has(el.tagName) && !m[0].endsWith('/>')) {
      stack.push(el);
    }
  }
  if (last < html.length) {
    stack[stack.length - 1]._nodes.push({ nodeType: 3, textContent: decodeEntities(html.slice(last)) });
  }
  return root._nodes;
}

function createMiniDocument(htmlSource) {
  const doc = {
    activeElement: null,
    root: null,
    getElementById(id) {
      return (function find(node) {
        for (const ch of node.children) {
          if (ch.id === id) return ch;
          const hit = find(ch);
          if (hit) return hit;
        }
        return null;
      })(this.root);
    },
    querySelector(sel) { return this.root.querySelector(sel); },
    querySelectorAll(sel) { return this.root.querySelectorAll(sel); },
    createElement(tag) { return new MiniElement(tag, this); },
  };
  const holder = new MiniElement('document', doc);
  holder._nodes = parseHTMLFragment(htmlSource, doc, null);
  doc.root = holder;
  return doc;
}

function createFakeIndexedDB() {
  const dbs = new Map();
  let pending = 0;
  return {
    dbs,
    get pending() { return pending; },
    open(name) {
      const req = { result: null, error: null, onupgradeneeded: null, onsuccess: null, onerror: null };
      pending++;
      queueMicrotask(() => {
        try {
          const isNew = !dbs.has(name);
          if (isNew) dbs.set(name, new Map());
          const stores = dbs.get(name);
          const db = {
            createObjectStore(s) { if (!stores.has(s)) stores.set(s, new Map()); },
            transaction(s) {
              const map = stores.get(s) || new Map();
              const tx = {
                oncomplete: null,
                onerror: null,
                objectStore() {
                  return {
                    put(val, key) { map.set(key, val); return { result: key }; },
                    get(key) { return { result: map.get(key) }; },
                    delete(key) { map.delete(key); return { result: undefined }; },
                  };
                },
              };
              pending++;
              queueMicrotask(() => {
                try {
                  tx.oncomplete?.({});
                } finally {
                  pending--;
                }
              });
              return tx;
            },
          };
          req.result = db;
          if (isNew) req.onupgradeneeded?.({});
          req.onsuccess?.({});
        } finally {
          pending--;
        }
      });
      return req;
    },
  };
}

function fakeFactoryPageDaemon({ shortCode = 'ABCD-EFGH', daemonId = 'DAEMON_PAGE_1', requireSessionForReads = false, asks = fixture.asks, tasks = null } = {}) {
  const calls = [];
  const answered = [];
  const openAsks = asks.map((a) => ({ ...a }));
  const graphTasks = tasks || [{ ref: 'T1', title: 'feat: demo task', stage: 'working', layer: 1, after: [], dependents: [] }];
  const projectCounts = {};
  for (const t of graphTasks) projectCounts[t.stage] = (projectCounts[t.stage] || 0) + 1;
  const challenges = new Set();
  const tickets = new Set();
  const cookieJar = { session: '' };
  let enrolledPub = null;
  let enrolledDeviceId = '';
  let revoked = false;
  let sticky401 = false;
  let nonceSeq = 0;
  let sessSeq = 0;
  let inFlight = 0;

  const signedText = (dId, devId, nonce, unix) =>
    ['sheepdog-auth-v1', dId, '', devId, nonce, String(unix), 'session'].join('\n');

  const reply = (status, obj) => ({
    ok: status >= 200 && status < 300,
    status,
    json: async () => obj,
  });

  const checkAuth = (sentCookie) => {
    if (sticky401) {
      if (sentCookie && cookieJar.session === sentCookie) cookieJar.session = '';
      return { ok: false, status: 401, body: { error: 'unauthorized', reason: 'session_expired' } };
    }
    if (sentCookie) {
      if (revoked || !tickets.has(sentCookie)) {
        if (cookieJar.session === sentCookie) cookieJar.session = '';
        return { ok: false, status: 401, body: { error: 'unauthorized', reason: revoked ? 'device_revoked' : 'session_expired' } };
      }
      return { ok: true, device: enrolledDeviceId };
    }
    if (requireSessionForReads) {
      return { ok: false, status: 401, body: { error: 'unauthorized', reason: 'no_session' } };
    }
    return { ok: true, device: 'local' };
  };

  const fetch = async (path, init = {}) => {
    const method = init.method || 'GET';
    const sentCookie = cookieJar.session;
    calls.push({ method, path, sentCookie });
    inFlight++;
    try {
      await Promise.resolve();
      const body = init.body ? JSON.parse(init.body) : null;

      if (path === '/api/hello') {
        return reply(200, { computer: { id: daemonId, name: 'Test Mac' } });
      }
      if (path === '/api/devices/enroll') {
        const norm = String(body.code || '').replace(/[\s-]+/g, '').toUpperCase();
        const want = shortCode.replace(/[\s-]+/g, '').toUpperCase();
        if (body.kind !== 'browser' || norm !== want) {
          return reply(410, { reason: 'code_used', error: 'Invalid or used code.' });
        }
        const raw = Uint8Array.from(atob(body.public_key.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
        enrolledPub = await webcrypto.subtle.importKey('raw', raw, { name: 'ECDSA', namedCurve: 'P-256' }, true, ['verify']);
        enrolledDeviceId = 'dev-browser-1';
        revoked = false;
        return reply(200, { device_id: enrolledDeviceId, daemon_id: daemonId, name: body.name || 'Test Browser' });
      }
      if (path === '/api/auth/challenge') {
        const nonce = `NONCE-${++nonceSeq}`;
        challenges.add(nonce);
        return reply(200, { nonce, daemon_id: daemonId, expires_in: 60 });
      }
      if (path === '/api/auth/session') {
        if (!challenges.delete(body.nonce)) {
          return reply(401, { error: 'unauthorized', reason: 'bad_nonce' });
        }
        if (revoked) {
          cookieJar.session = '';
          return reply(401, { error: 'unauthorized', reason: 'device_revoked' });
        }
        if (!enrolledPub || body.device_id !== enrolledDeviceId || body.client !== 'browser') {
          cookieJar.session = '';
          return reply(401, { error: 'unauthorized', reason: 'unknown_device' });
        }
        const sig = Uint8Array.from(atob(body.signature.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
        const ok = await webcrypto.subtle.verify(
          { name: 'ECDSA', hash: 'SHA-256' },
          enrolledPub,
          sig,
          new TextEncoder().encode(signedText(daemonId, body.device_id, body.nonce, body.time)),
        );
        if (!ok) return reply(401, { error: 'unauthorized', reason: 'bad_signature' });
        const t = `sess-${++sessSeq}`;
        tickets.add(t);
        cookieJar.session = t;
        return reply(200, { expires_in: 43200, device: { id: enrolledDeviceId, name: 'Test Browser' } });
      }

      if (path === '/api/state' || path.startsWith('/api/factory/')) {
        const auth = checkAuth(sentCookie);
        if (!auth.ok) return reply(auth.status, auth.body);

        if (path === '/api/state') {
          return reply(200, { ok: true });
        }
        if (path === '/api/factory/projects') {
          return reply(200, {
            protocol: 1,
            projects: [{ id: 'proj-demo', goal: 'demo goal', running: true, counts: projectCounts }],
          });
        }
        if (path === '/api/factory/projects/proj-demo/graph') {
          return reply(200, {
            protocol: 1,
            project: 'proj-demo',
            tasks: graphTasks,
          });
        }
        if (path.startsWith('/api/factory/projects/proj-demo/log')) {
          return reply(200, { protocol: 1, project: 'proj-demo', lines: [] });
        }
        if (path === '/api/factory/asks') {
          return reply(200, { protocol: 1, asks: openAsks.filter((a) => a.status === 'open') });
        }
        const m = path.match(/^\/api\/factory\/asks\/([^/]+)\/answer$/);
        if (m && method === 'POST') {
          if (auth.device === 'local' || !auth.device) {
            return reply(403, { code: 'proof_required', error: 'proof_required' });
          }
          const ask = openAsks.find((a) => a.id === m[1]);
          if (!ask || ask.status !== 'open') {
            return reply(409, { code: 'already_closed', error: 'already_closed' });
          }
          ask.status = 'answered';
          answered.push({ id: m[1], option: body.option, device_id: auth.device });
          return reply(200, { ok: true, ask_id: m[1], device_id: auth.device });
        }
      }
      return reply(404, { error: 'not found' });
    } finally {
      inFlight--;
    }
  };

  return {
    fetch,
    calls,
    answered,
    cookieJar,
    get inFlight() { return inFlight; },
    restartDaemon() { tickets.clear(); },
    revokeDevice() { revoked = true; tickets.clear(); },
    setRequireSessionForReads(v) { requireSessionForReads = v; },
    setSticky401(v) { sticky401 = v; },
  };
}

let appImportSeq = 0;

async function bootFactoryPage(t, daemon) {
  const html = readFileSync(new URL('./index.html', import.meta.url), 'utf8');
  const doc = createMiniDocument(html);
  const idb = createFakeIndexedDB();
  const origSetTimeout = globalThis.setTimeout;
  const origClearTimeout = globalThis.clearTimeout;
  const origCrypto = globalThis.crypto;
  const prev = {
    document: Object.getOwnPropertyDescriptor(globalThis, 'document'),
    location: Object.getOwnPropertyDescriptor(globalThis, 'location'),
    indexedDB: Object.getOwnPropertyDescriptor(globalThis, 'indexedDB'),
    fetch: Object.getOwnPropertyDescriptor(globalThis, 'fetch'),
    crypto: Object.getOwnPropertyDescriptor(globalThis, 'crypto'),
    addEventListener: Object.getOwnPropertyDescriptor(globalThis, 'addEventListener'),
    scrollTo: Object.getOwnPropertyDescriptor(globalThis, 'scrollTo'),
  };

  const pending = new Set();
  const track = (p) => {
    if (p && typeof p.then === 'function') {
      const marker = Promise.resolve(p).then(
        () => pending.delete(marker),
        () => pending.delete(marker),
      );
      pending.add(marker);
    }
    return p;
  };

  const origSubtle = origCrypto.subtle;
  const trackedSubtle = new Proxy(origSubtle, {
    get(target, prop) {
      const val = Reflect.get(target, prop, target);
      if (typeof val !== 'function') return val;
      return (...args) => track(val.apply(target, args));
    },
  });
  const trackedCrypto = new Proxy(origCrypto, {
    get(target, prop) {
      if (prop === 'subtle') return trackedSubtle;
      const val = Reflect.get(target, prop, target);
      return typeof val === 'function' ? val.bind(target) : val;
    },
  });

  let disposed = false;
  let nextPollFn = null;
  let timerSeq = 1000;
  const activeFakeTimers = new Set();
  const activeRealTimers = new Set();
  globalThis.setTimeout = (fn, ms, ...args) => {
    if (disposed) return ++timerSeq;
    if (Number(ms) >= 500) {
      const id = ++timerSeq;
      activeFakeTimers.add(id);
      nextPollFn = () => {
        if (disposed || !activeFakeTimers.has(id)) return;
        activeFakeTimers.delete(id);
        return fn(...args);
      };
      return id;
    }
    const id = origSetTimeout(() => {
      activeRealTimers.delete(id);
      if (!disposed) fn(...args);
    }, ms);
    activeRealTimers.add(id);
    return id;
  };
  globalThis.clearTimeout = (id) => {
    if (typeof id === 'number' && id > 1000) {
      activeFakeTimers.delete(id);
      if (activeFakeTimers.size === 0) nextPollFn = null;
      return;
    }
    activeRealTimers.delete(id);
    origClearTimeout(id);
  };

  const wrappedFetch = (path, init) => {
    if (disposed) return Promise.reject(new Error('factory page test disposed'));
    return track(daemon.fetch(path, init));
  };

  Object.defineProperty(globalThis, 'document', { value: doc, configurable: true, writable: true });
  Object.defineProperty(globalThis, 'location', { value: { hash: '', href: 'http://127.0.0.1:7777/factory' }, configurable: true, writable: true });
  Object.defineProperty(globalThis, 'indexedDB', { value: idb, configurable: true, writable: true });
  Object.defineProperty(globalThis, 'fetch', { value: wrappedFetch, configurable: true, writable: true });
  Object.defineProperty(globalThis, 'crypto', { value: trackedCrypto, configurable: true, writable: true });
  Object.defineProperty(globalThis, 'addEventListener', { value: () => {}, configurable: true, writable: true });
  Object.defineProperty(globalThis, 'scrollTo', { value: () => {}, configurable: true, writable: true });

  const hasPendingWork = () =>
    pending.size > 0 || daemon.inFlight > 0 || idb.pending > 0 || activeRealTimers.size > 0;

  const flush = async () => {
    const deadline = Date.now() + 5000;
    let quiet = 0;
    while (quiet < 3) {
      while (pending.size > 0) {
        await Promise.allSettled([...pending]);
      }
      await new Promise((r) => origSetTimeout(r, 0));
      if (hasPendingWork()) {
        quiet = 0;
        if (Date.now() > deadline) {
          throw new Error('flush() timed out waiting for pending async work');
        }
        continue;
      }
      quiet++;
    }
  };

  t.after(async () => {
    disposed = true;
    nextPollFn = null;
    activeFakeTimers.clear();
    for (const id of activeRealTimers) origClearTimeout(id);
    activeRealTimers.clear();
    await flush().catch(() => {});
    globalThis.setTimeout = origSetTimeout;
    globalThis.clearTimeout = origClearTimeout;
    for (const [k, desc] of Object.entries(prev)) {
      if (desc) Object.defineProperty(globalThis, k, desc);
      else delete globalThis[k];
    }
  });

  await import(`./app.js?boot=${++appImportSeq}`);
  await flush();

  return {
    doc,
    idb,
    flush,
    async pollNow() {
      if (nextPollFn) nextPollFn();
      else doc.getElementById('retry').click();
      await flush();
    },
  };
}

test('after a daemon restart an enrolled browser on /factory that gets 401 signs in again with its stored WebCrypto key, retries once and keeps working', async (t) => {
  const daemon = fakeFactoryPageDaemon({ daemonId: 'DAEMON_RESTART_1' });
  const page = await bootFactoryPage(t, daemon);
  const asksEl = page.doc.getElementById('asks');
  const mainEl = page.doc.getElementById('main');

  // Initial loopback poll shows the project board and both open asks.
  assert.match(mainEl.innerHTML, /T1/);
  assert.match(asksEl.innerHTML, /Approve the plan for T1\?/);

  // Clicking an option before pairing shows the pairing form on demand, then pairing submits the answer.
  const firstBtn = asksEl.querySelector('button[data-ask-id="ask-demo-1"][data-option="Approve"]');
  assert.ok(firstBtn, 'expected Approve button for ask-demo-1');
  firstBtn.click();
  await page.flush();

  const pairInput = asksEl.querySelector('#pair-code');
  const pairForm = asksEl.querySelector('form[data-pair-form]');
  assert.ok(pairInput && pairForm, 'expected on-demand short-code pairing form');
  pairInput.value = 'ABCD-EFGH';
  pairForm.dispatchEvent({ type: 'submit', bubbles: true });
  await page.flush();

  assert.equal(daemon.answered.length, 1);
  assert.deepEqual(daemon.answered[0], { id: 'ask-demo-1', option: 'Approve', device_id: 'dev-browser-1' });
  assert.ok(daemon.cookieJar.session !== '', 'browser cookie jar holds active session cookie');

  // Daemon restarts: in-memory session tickets are emptied while browser still holds the stale cookie + IndexedDB key.
  daemon.restartDaemon();
  const mark = daemon.calls.length;
  await page.pollNow();
  const sinceRestart = daemon.calls.slice(mark);

  // Must sign in automatically with stored WebCrypto key (never re-enrolling or fetching /api/pair*).
  assert.ok(
    sinceRestart.some((c) => c.method === 'POST' && c.path === '/api/auth/session'),
    'expected automatic WebCrypto POST /api/auth/session on 401 during /factory poll',
  );
  assert.ok(
    !sinceRestart.some((c) => c.path === '/api/devices/enroll' || c.path.startsWith('/api/pair')),
    'must not re-enroll or fetch /api/pair* after daemon restart',
  );

  // Failed poll GETs are retried once (initial 401 + single retry -> 200).
  const projCalls = sinceRestart.filter((c) => c.path === '/api/factory/projects').length;
  const askCalls = sinceRestart.filter((c) => c.path === '/api/factory/asks').length;
  assert.equal(projCalls, 2, `expected GET /api/factory/projects to retry once (2 calls), got ${projCalls}`);
  assert.ok(askCalls >= 1 && askCalls <= 2, `expected GET /api/factory/asks <= 2 calls, got ${askCalls}`);

  // Board and remaining ask stay live (no lockout error, no pairing prompt) and answering works.
  assert.doesNotMatch(mainEl.innerHTML, /can(&#39;|')t show the factory|unauthorized/i);
  assert.match(mainEl.innerHTML, /T1/);
  assert.match(asksEl.innerHTML, /Tag and publish v0\.2\.0\?/);
  assert.equal(asksEl.querySelector('#pair-code'), null);

  const secondBtn = asksEl.querySelector('button[data-ask-id="ask-demo-2"][data-option="Publish v0.2.0"]');
  assert.ok(secondBtn, 'expected Publish v0.2.0 button after restart');
  secondBtn.click();
  await page.flush();
  assert.deepEqual(daemon.answered[1], { id: 'ask-demo-2', option: 'Publish v0.2.0', device_id: 'dev-browser-1' });

  // Even if the server keeps returning 401 after sign-in, a poll retries at most once (no loop).
  daemon.setSticky401(true);
  const stickyMark = daemon.calls.length;
  await page.pollNow();
  const stickyProjCalls = daemon.calls.slice(stickyMark).filter((c) => c.path === '/api/factory/projects').length;
  assert.ok(stickyProjCalls <= 2, `expected at most 1 retry under sticky 401, got ${stickyProjCalls} calls`);
});

test('on /factory an unenrolled browser getting 401 sees the pairing hint without looping, and a revoked session cookie stops being sent', async (t) => {
  const daemon = fakeFactoryPageDaemon({ daemonId: 'DAEMON_UNENROLLED_1', requireSessionForReads: true });
  const page = await bootFactoryPage(t, daemon);
  const asksEl = page.doc.getElementById('asks');

  // 1. Unenrolled browser getting 401 does not loop and shows the pairing hint.
  const initialProjCalls = daemon.calls.filter((c) => c.path === '/api/factory/projects').length;
  const initialAskCalls = daemon.calls.filter((c) => c.path === '/api/factory/asks').length;
  assert.ok(initialProjCalls <= 2, `unenrolled 401 must not loop on /api/factory/projects (got ${initialProjCalls})`);
  assert.ok(initialAskCalls <= 2, `unenrolled 401 must not loop on /api/factory/asks (got ${initialAskCalls})`);
  assert.equal(
    daemon.calls.filter((c) => c.path === '/api/auth/session').length,
    0,
    'unenrolled browser has no key and must not post /api/auth/session',
  );
  assert.ok(
    !daemon.calls.some((c) => c.path.startsWith('/api/pair')),
    'must never auto-fetch /api/pair*',
  );
  assert.ok(asksEl.querySelector('#pair-code'), 'unenrolled browser getting 401 must see the pairing hint/form');
  assert.match(asksEl.innerHTML, /menu.?bar|pair/i);

  // 2. Pair the browser, then revoke it: the dead cookie is cleared instead of sent forever, and no loop occurs.
  asksEl.querySelector('#pair-code').value = 'ABCD-EFGH';
  asksEl.querySelector('form[data-pair-form]').dispatchEvent({ type: 'submit', bubbles: true });
  await page.flush();
  await page.pollNow();
  assert.ok(daemon.cookieJar.session !== '', 'expected active session cookie after pairing');

  daemon.setRequireSessionForReads(false);
  daemon.revokeDevice();
  const revMark = daemon.calls.length;
  await page.pollNow();
  const revCalls = daemon.calls.slice(revMark);
  assert.ok(
    revCalls.filter((c) => c.path === '/api/factory/projects').length <= 2,
    'revoked browser must not loop on /api/factory/projects',
  );
  assert.equal(daemon.cookieJar.session, '', 'revoked session cookie must be cleared');
  assert.equal(
    page.idb.dbs.get('sheepdog')?.get('devices')?.size || 0,
    0,
    'revoked browser key must be forgotten on 401 re-auth refusal',
  );

  // Next poll sends no dead cookie at all.
  const afterClearMark = daemon.calls.length;
  await page.pollNow();
  const afterClear = daemon.calls.slice(afterClearMark);
  assert.ok(afterClear.length > 0);
  assert.ok(afterClear.every((c) => c.sentCookie === ''), 'cleared cookie must not be sent again');
});

test('the /factory page shows no card for a worker decision ask meant for the coordinator', async (t) => {
  const workerDecisionAsk = {
    id: 'ask-decision-worker-1',
    project: 'proj-demo',
    kind: 'decision',
    gated: false,
    asked_by: 'worker',
    question: 'Should T1 use helper A or helper B?',
    options: ['Helper A', 'Helper B'],
    recommend: 'Helper A',
    task: 'T1',
    at: '2026-11-22T12:00:00Z',
    status: 'open',
  };
  const ownerGateAsk = { ...fixture.asks[0] };

  await t.test('renderAskCardsHTML excludes worker decision asks', () => {
    const mixedHtml = renderAskCardsHTML([workerDecisionAsk, ownerGateAsk], {});
    assert.doesNotMatch(mixedHtml, /ask-decision-worker-1/);
    assert.doesNotMatch(mixedHtml, /Should T1 use helper A or helper B\?/);
    assert.match(mixedHtml, /ask-demo-1/);
    assert.match(mixedHtml, /Approve the plan for T1\?/);

    const decisionOnlyHtml = renderAskCardsHTML([workerDecisionAsk], {});
    assert.doesNotMatch(decisionOnlyHtml, /ask-decision-worker-1/);
    assert.doesNotMatch(decisionOnlyHtml, /ask-card/);
    assert.match(decisionOnlyHtml, /No open asks right now\./);
  });

  await t.test('booted /factory page hides decision ask among mixed asks and counts only owner asks', async (t) => {
    const daemon = fakeFactoryPageDaemon({
      daemonId: 'DAEMON_DECISION_MIXED',
      asks: [workerDecisionAsk, ownerGateAsk],
    });
    const page = await bootFactoryPage(t, daemon);
    const asksEl = page.doc.getElementById('asks');

    assert.equal(asksEl.querySelectorAll('.ask-card').length, 1, 'only the owner ask card should render');
    assert.equal(asksEl.querySelector('[data-ask-id="ask-decision-worker-1"]'), null);
    assert.doesNotMatch(asksEl.innerHTML, /Should T1 use helper A or helper B\?/);
    assert.ok(asksEl.querySelector('[data-ask-id="ask-demo-1"]'), 'owner gate ask card must still render');
    assert.match(asksEl.innerHTML, /1 open/);
  });

  await t.test('booted /factory page shows no ask card when only a worker decision ask is open', async (t) => {
    const daemon = fakeFactoryPageDaemon({
      daemonId: 'DAEMON_DECISION_ONLY',
      asks: [workerDecisionAsk],
    });
    const page = await bootFactoryPage(t, daemon);
    const asksEl = page.doc.getElementById('asks');

    assert.equal(asksEl.querySelectorAll('.ask-card').length, 0, 'worker decision ask must not render as a card');
    assert.doesNotMatch(asksEl.innerHTML, /Should T1 use helper A or helper B\?/);
    assert.match(asksEl.innerHTML, /No open asks right now\./);
    assert.doesNotMatch(asksEl.innerHTML, /\b1 open\b/);
  });
});

test('override ask card renders its multi-line command with each PR, short H, checks and unreported work, escaped', async (t) => {
  const overrideAsk = {
    id: 'ask-ovr-1',
    project: 'proj-demo',
    kind: 'override',
    gated: true,
    question: 'Merge T1, T2 now without waiting for CI or review?',
    command: 'T1 #7 @ abcdef1 · red · has unreported work\nT2 #8 @ 9876543 · pending',
    options: ['Yes, merge now', 'No, wait'],
    recommend: 'No, wait',
    targets: [
      { task: 'T1', pr: 7, head: 'abcdef1234567890', checks: 'red', unreported: true },
      { task: 'T2', pr: 8, head: '9876543210fedcba', checks: 'pending' },
    ],
    at: '2026-11-23T10:00:00Z',
    status: 'open',
  };

  await t.test('renderAskCardsHTML includes multi-line command and escapes HTML', () => {
    const html = renderAskCardsHTML([overrideAsk], {});
    assert.match(html, /T1 #7 @ abcdef1 · red · has unreported work/);
    assert.match(html, /T2 #8 @ 9876543 · pending/);
    assert.match(html, /T1 #7 @ abcdef1 · red · has unreported work\nT2 #8 @ 9876543 · pending/);

    const xss = { ...overrideAsk, id: 'ask-ovr-xss', command: 'T1 <img src=x onerror=alert(1)>' };
    const xssHtml = renderAskCardsHTML([xss], {});
    assert.ok(!xssHtml.includes('<img src=x'), 'command must be HTML-escaped');
    assert.ok(xssHtml.includes('&#60;img') || xssHtml.includes('&lt;img'), 'escaped command tag expected');
  });

  await t.test('booted /factory page renders override command lines and CSS preserves newlines', async () => {
    const daemon = fakeFactoryPageDaemon({ daemonId: 'DAEMON_OVR_WEB', asks: [overrideAsk] });
    const page = await bootFactoryPage(t, daemon);
    const asksEl = page.doc.getElementById('asks');
    const card = asksEl.querySelector('[data-ask-id="ask-ovr-1"]');
    assert.ok(card, 'expected override ask card');
    assert.match(card.textContent, /T1 #7 @ abcdef1 · red · has unreported work/);
    assert.match(card.textContent, /T2 #8 @ 9876543 · pending/);
    assert.ok(card.querySelector('pre'), 'multi-line command should render in a <pre> block');

    const css = readFileSync(new URL('./style.css', import.meta.url), 'utf8');
    assert.match(css, /\.ask-cmd\b[^}]*white-space:\s*pre-wrap/, 'style.css should preserve newlines on .ask-cmd');
  });
});

test('KB-TSK-19: owner_gate and owner_answer stages have labels, dots, alert highlight and order at the top', async (t) => {
  await t.test('board renders owner_gate and owner_answer above blocked with labels, blocked dots, alert classes and sidebar counts', async (t) => {
    const daemon = fakeFactoryPageDaemon({
      daemonId: 'DAEMON_STAGES_9_1',
      asks: [],
      tasks: [
        { ref: 'T-q', title: 'feat: queued', stage: 'queued', why_waiting: 'Queued to start', layer: 2, after: [], dependents: [] },
        { ref: 'T-w', title: 'feat: working', stage: 'working', why_waiting: 'Worker is working', layer: 1, after: [], dependents: [] },
        { ref: 'T-b', title: 'feat: blocked', stage: 'blocked_or_failed', why_waiting: 'Parked, waiting on the coordinator', layer: 1, after: [], dependents: [] },
        { ref: 'T-a', title: 'feat: release', stage: 'owner_answer', why_waiting: 'Ship release v1?', layer: 1, after: [], dependents: [] },
        { ref: 'T-g', title: 'feat: gate', stage: 'owner_gate', why_waiting: 'Merge PR #7?', layer: 0, after: [], dependents: [] },
      ],
    });
    const page = await bootFactoryPage(t, daemon);
    const mainEl = page.doc.getElementById('main');
    const projectsEl = page.doc.getElementById('projects');

    const groups = mainEl.querySelectorAll('section.group');
    const labels = groups.map((g) => g.getAttribute('aria-label'));
    const byLabel = new Map(groups.map((g) => [g.getAttribute('aria-label'), g]));

    assert.ok(!labels.includes('owner_gate'), `raw stage name owner_gate should not appear in group labels: ${labels.join(', ')}`);
    assert.ok(!labels.includes('owner_answer'), `raw stage name owner_answer should not appear in group labels: ${labels.join(', ')}`);
    for (const want of ['needs your approval', 'waiting on you', 'blocked', 'working', 'queued']) {
      assert.ok(byLabel.has(want), `expected group ${JSON.stringify(want)}, got: ${labels.join(', ')}`);
    }

    const gateIdx = labels.indexOf('needs your approval');
    const answerIdx = labels.indexOf('waiting on you');
    const blockedIdx = labels.indexOf('blocked');
    const workingIdx = labels.indexOf('working');
    const queuedIdx = labels.indexOf('queued');
    assert.ok(gateIdx < blockedIdx, `owner_gate ("needs your approval" @ ${gateIdx}) must appear above blocked (@ ${blockedIdx})`);
    assert.ok(answerIdx < blockedIdx, `owner_answer ("waiting on you" @ ${answerIdx}) must appear above blocked (@ ${blockedIdx})`);
    assert.ok(blockedIdx < workingIdx && workingIdx < queuedIdx, `expected blocked < working < queued, got: ${labels.join(', ')}`);

    for (const alertLabel of ['needs your approval', 'waiting on you', 'blocked']) {
      const g = byLabel.get(alertLabel);
      assert.ok(g.querySelector('h2.alert'), `${alertLabel} heading must have class="alert"`);
      assert.ok(g.querySelector('.rows.alert'), `${alertLabel} rows container must have class="alert"`);
      assert.ok(g.querySelector('.row .dot.blocked'), `${alertLabel} task row must use .dot.blocked`);
      assert.equal(g.querySelector('.row .dot.idle'), null, `${alertLabel} task row must not fall back to .dot.idle`);
    }
    for (const plainLabel of ['working', 'queued']) {
      const g = byLabel.get(plainLabel);
      assert.equal(g.querySelector('h2.alert'), null, `${plainLabel} heading must not have class="alert"`);
      assert.equal(g.querySelector('.rows.alert'), null, `${plainLabel} rows container must not have class="alert"`);
    }

    assert.match(projectsEl.textContent, /1 needs your approval/);
    assert.match(projectsEl.textContent, /1 waiting on you/);
    assert.doesNotMatch(projectsEl.textContent, /\bowner_gate\b|\bowner_answer\b/);
  });

  await t.test('alert flag lives in STAGE and boardHTML does not repeat stage-name comparisons', () => {
    const js = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
    const stageMatch = js.match(/const\s+STAGE\s*=\s*\{([\s\S]*?)\};/);
    assert.ok(stageMatch, 'expected const STAGE = { ... }; in app.js');
    const stageBody = stageMatch[1];
    assert.match(
      stageBody,
      /owner_answer\s*:\s*\[\s*['"]waiting on you['"]\s*,\s*['"]blocked['"]\s*,\s*(?:true|1)\s*\]/,
      'STAGE.owner_answer must carry label, blocked dot, and truthy alert flag',
    );
    assert.match(
      stageBody,
      /owner_gate\s*:\s*\[\s*['"]needs your approval['"]\s*,\s*['"]blocked['"]\s*,\s*(?:true|1)\s*\]/,
      'STAGE.owner_gate must carry label, blocked dot, and truthy alert flag',
    );
    assert.match(
      stageBody,
      /blocked_or_failed\s*:\s*\[\s*['"]blocked['"]\s*,\s*['"]blocked['"]\s*,\s*(?:true|1)\s*\]/,
      'STAGE.blocked_or_failed must carry label, blocked dot, and truthy alert flag',
    );

    const boardMatch = js.match(/function\s+boardHTML\s*\([^)]*\)\s*\{([\s\S]*?)\n\}/);
    assert.ok(boardMatch, 'expected function boardHTML in app.js');
    assert.doesNotMatch(
      boardMatch[1],
      /blocked_or_failed|owner_gate|owner_answer/,
      'boardHTML must read the alert flag from STAGE instead of naming stages inline',
    );
  });
});
