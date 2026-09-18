// sheepdog /debug: a viewer of the event log plus the fake-injection
// controls (plan §3.8, decision D10). Vanilla JS module, no build step.
// Tabs: Conversation (conversation-view.js), Events (the raw list), State.
import { ConversationView } from './conversation-view.js';

const $ = (id) => document.getElementById(id);
const BACKFILL = 2000;
const SOURCES = ['herdr', 'voice', 'supervisor', 'llm', 'cli', 'web', 'eventlog', 'daemon'];
const MAX_ROWS = 2000;
const REPLAY = 300;
const STATE_POLL_MS = 2000;

// ---------------------------------------------------------------- events

const events = {
  paused: false,
  buffer: [],
  count: 0,
  sources: new Set(SOURCES),
  pane: '',
  search: '',
  es: null,
};

function fmtTime(ts) {
  const d = new Date(ts);
  if (isNaN(d)) return ts;
  const p = (n, w) => String(n).padStart(w, '0');
  return `${p(d.getHours(), 2)}:${p(d.getMinutes(), 2)}:${p(d.getSeconds(), 2)}.${p(d.getMilliseconds(), 3)}`;
}

function summarize(payload) {
  if (payload == null) return '';
  if (typeof payload !== 'object') return String(payload);
  const parts = [];
  for (const [k, v] of Object.entries(payload)) {
    let s;
    if (v == null) continue;
    if (typeof v === 'object') s = JSON.stringify(v);
    else s = String(v);
    if (s.length > 90) s = s.slice(0, 87) + '…';
    parts.push(`${k}=${s}`);
    if (parts.length >= 8) break;
  }
  return parts.join('  ');
}

function matchesFilter(row) {
  if (!events.sources.has(row.dataset.source)) return false;
  if (events.pane && row.dataset.pane !== events.pane) return false;
  if (events.search && !row.dataset.text.includes(events.search)) return false;
  return true;
}

function applyFilters() {
  const list = $('events');
  let shown = 0;
  for (const row of list.querySelectorAll('.ev')) {
    const ok = matchesFilter(row);
    row.classList.toggle('hidden-by-filter', !ok);
    if (row.nextElementSibling && row.nextElementSibling.classList.contains('ev-detail')) {
      row.nextElementSibling.classList.toggle('hidden-by-filter', !ok);
    }
    if (ok) shown++;
  }
  $('events-count').textContent = `${shown} shown / ${events.count} received`;
}

function renderEvent(ev) {
  const list = $('events');
  const row = document.createElement('div');
  const src = ev.source || 'unknown';
  row.className = `ev ${src}`;
  row.dataset.source = src;
  row.dataset.pane = ev.pane_id || '';
  row.dataset.text = JSON.stringify(ev).toLowerCase();
  const cell = (cls, text) => {
    const s = document.createElement('span');
    s.className = cls;
    s.textContent = text;
    row.appendChild(s);
  };
  cell('ts', fmtTime(ev.ts));
  cell('src-name', src);
  cell('type', ev.type);
  cell('pane', ev.pane_id || '');
  const cost = ev.payload && ev.payload.total_cost_usd != null ? `$${Number(ev.payload.total_cost_usd).toFixed(4)}  ` : '';
  cell('summary', (ev.dur_ms != null ? `${ev.dur_ms}ms  ` : '') + cost + summarize(ev.payload));
  row.addEventListener('click', () => {
    const next = row.nextElementSibling;
    if (next && next.classList.contains('ev-detail')) {
      next.remove();
      return;
    }
    const pre = document.createElement('pre');
    pre.className = 'ev-detail';
    pre.textContent = JSON.stringify(ev, null, 2);
    row.after(pre);
  });
  if (!matchesFilter(row)) row.classList.add('hidden-by-filter');
  list.appendChild(row);
  while (list.children.length > MAX_ROWS) {
    list.removeChild(list.firstChild);
  }
  if ($('events-follow').checked) list.scrollTop = list.scrollHeight;
}

function onEvent(ev, encoded) {
  events.count++;
  conversation.add(ev, encoded);
  scheduleConvRender();
  if (events.paused) {
    events.buffer.push(ev);
    $('events-pause').textContent = `Resume (${events.buffer.length})`;
    return;
  }
  renderEvent(ev);
  $('events-count').textContent = `${$('events').querySelectorAll('.ev:not(.hidden-by-filter)').length} shown / ${events.count} received`;
  if (/^(herdr\.transition|pending\.|supervisor\.|daemon\.|voice\.connect|voice\.closed|web\.inject)/.test(ev.type)) {
    scheduleStateRefresh();
  }
}

function setSSE(status, text) {
  $('sse-dot').className = `dot ${status}`;
  $('sse-text').textContent = `events: ${text}`;
}

function connectEvents() {
  if (events.es) events.es.close();
  const es = new EventSource(`/events?since=${REPLAY}`);
  events.es = es;
  setSSE('connecting', 'connecting');
  es.onopen = () => setSSE('connected', 'live');
  es.onerror = () => setSSE('reconnecting', 'reconnecting');
  es.addEventListener('log', (e) => {
    try {
      onEvent(JSON.parse(e.data), e.data);
    } catch (err) {
      console.error('bad event', e.data, err);
    }
  });
  es.addEventListener('closed', (e) => {
    setSSE('closed', `closed (${e.data})`);
  });
}

function setupEventControls() {
  const holder = $('source-filters');
  for (const src of SOURCES) {
    const label = document.createElement('label');
    label.className = `src ${src}`;
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = true;
    cb.addEventListener('change', () => {
      if (cb.checked) events.sources.add(src); else events.sources.delete(src);
      applyFilters();
    });
    label.appendChild(cb);
    label.appendChild(document.createTextNode(src));
    holder.appendChild(label);
  }
  $('filter-pane').addEventListener('input', (e) => { events.pane = e.target.value.trim(); applyFilters(); });
  $('filter-search').addEventListener('input', (e) => { events.search = e.target.value.trim().toLowerCase(); applyFilters(); });
  $('events-pause').addEventListener('click', () => {
    events.paused = !events.paused;
    if (!events.paused) {
      for (const ev of events.buffer) renderEvent(ev);
      events.buffer = [];
      $('events-pause').textContent = 'Pause';
      applyFilters();
    } else {
      $('events-pause').textContent = 'Resume (0)';
    }
  });
  $('events-clear').addEventListener('click', () => {
    $('events').replaceChildren();
    events.count = 0;
    applyFilters();
  });
}

// ----------------------------------------------------------------- state

let stateTimer = null;
function scheduleStateRefresh() {
  if (stateTimer) return;
  stateTimer = setTimeout(() => { stateTimer = null; refreshState(); }, 200);
}

function since(ts) {
  const d = new Date(ts);
  if (isNaN(d)) return '';
  let s = Math.max(0, Math.round((Date.now() - d.getTime()) / 1000));
  const h = Math.floor(s / 3600); s -= h * 3600;
  const m = Math.floor(s / 60); s -= m * 60;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${s}s`;
  return `${s}s`;
}

function pill(text, cls) {
  const s = document.createElement('span');
  s.className = `pill ${cls || text}`;
  s.textContent = text;
  return s;
}

function renderAgents(agents) {
  const body = $('state-table').querySelector('tbody');
  body.replaceChildren();
  $('state-empty').classList.toggle('hidden', agents.length > 0);
  for (const a of agents) {
    const tr = document.createElement('tr');
    const td = (content, cls) => {
      const c = document.createElement('td');
      if (cls) c.className = cls;
      if (content instanceof Node) c.appendChild(content); else c.textContent = content ?? '';
      if (typeof content === 'string') c.title = content;
      tr.appendChild(c);
      return c;
    };
    const alias = td(a.alias || '');
    if (a.synthetic) {
      const tag = document.createElement('span');
      tag.className = 'synthetic';
      tag.textContent = ' (fake)';
      alias.appendChild(tag);
    }
    td(a.pane_id || '');
    td(a.kind || '');
    td(a.workspace_label || a.workspace || '');
    td(pill(a.state || 'unknown'));
    td(since(a.state_since));
    td(a.current_task || '', 'task');
    const lb = a.last_block;
    td(lb ? `${lb.question || '(no question)'}${lb.answered ? ' ✓' : ''}` : '', 'block');
    const actions = document.createElement('td');
    if (a.synthetic) {
      const b = document.createElement('button');
      b.textContent = 'use';
      b.title = 'fill the inject form with this pane';
      b.addEventListener('click', () => { $('inj-pane').value = a.pane_id; $('inj-alias').value = a.alias; $('inj-kind').value = a.kind || 'codex'; });
      actions.appendChild(b);
    }
    tr.appendChild(actions);
    body.appendChild(tr);
  }
}

function renderPending(p) {
  $('pending-card').classList.toggle('hidden', !p);
  $('pending-empty').classList.toggle('hidden', !!p);
  if (!p) return;
  $('pending-id').textContent = p.id;
  $('pending-kind').replaceChildren(pill(p.kind));
  $('pending-pane').textContent = p.pane_id;
  $('pending-summary').textContent = p.spoken_summary || '';
  $('pending-question').textContent = p.question || '';
  $('pending-payload').textContent = p.payload_text || '';
  $('pending-options').textContent = (p.options || []).map((o, i) => `${i + 1}. ${o.label} [${(o.keys || []).join(' ')}]`).join('   ')
    + (p.yes_keys ? `   yes=${p.yes_keys.join(' ')}` : '') + (p.no_keys ? ` no=${p.no_keys.join(' ')}` : '');
  const exp = new Date(p.expires_at);
  const left = Math.round((exp.getTime() - Date.now()) / 1000);
  $('pending-expires').textContent = isNaN(exp) ? '' : `${exp.toLocaleTimeString()} (${left >= 0 ? `${left}s left` : 'expired'})`;
}

function renderVoice(v, st) {
  const status = v.mode === 'off' ? 'off' : v.status;
  $('voice-dot').className = `dot ${status}`;
  let text = v.mode === 'off' ? 'voice: off' : `voice: ${v.status}${v.reason ? ` (${v.reason})` : ''}`;
  if (v.muted) text += ' · muted';
  if (v.stalled) text += ' · STALLED';
  text += v.client_connected ? ' · browser attached' : ' · no browser';
  $('voice-indicator').classList.toggle('stalled', !!v.stalled);
  $('voice-text').textContent = text;
  $('daemon-info').textContent = `v${st.version || '?'} · herdr=${st.herdr} · voice_connected=${st.session && st.session.voice_connected}`;
}

// renderSettings keeps the header's chattiness switch in step with the
// daemon (the level can also change through PUT /api/settings elsewhere).
function renderSettings(settings) {
  const sel = $('chattiness');
  if (!settings || !settings.chattiness || document.activeElement === sel) return;
  if (sel.value !== settings.chattiness) sel.value = settings.chattiness;
}

function setupSettings() {
  const sel = $('chattiness');
  sel.addEventListener('change', async () => {
    const level = sel.value;
    try {
      const r = await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ chattiness: level }) });
      const data = await r.json();
      if (!r.ok) throw new Error(data.error || `${r.status}`);
      renderSettings(data);
    } catch (err) {
      console.error('settings', err);
      refreshState();
    }
  });
}

async function refreshState() {
  try {
    const r = await fetch('/api/state', { cache: 'no-store' });
    const st = await r.json();
    renderAgents(st.agents || []);
    renderPending(st.pending_action);
    renderVoice(st.voice || {}, st);
    renderSettings(st.settings);
    $('state-updated').textContent = `updated ${new Date().toLocaleTimeString()}`;
  } catch (err) {
    $('state-updated').textContent = `error: ${err}`;
  }
}

// ---------------------------------------------------------------- inject

async function post(url, body) {
  const r = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const text = await r.text();
  let data;
  try { data = JSON.parse(text); } catch { data = { error: text }; }
  if (!r.ok) throw new Error(data.error || `${r.status}`);
  return data;
}

function showResult(id, promise) {
  const el = $(id);
  el.textContent = '…';
  promise.then((data) => {
    if (data.spoken !== undefined) {
      el.textContent = `${data.tool || data.via}: "${data.spoken}"${data.clarify ? ' (clarify)' : ''}`;
    } else if (data.agent) {
      el.textContent = `ok: ${data.agent.alias} is ${data.agent.state} (${data.dur_ms} ms)`;
    } else {
      el.textContent = JSON.stringify(data);
    }
    refreshState();
  }).catch((err) => {
    el.textContent = `error: ${err.message}`;
    refreshState();
  });
}

function setupInject() {
  const sel = $('inj-preset');
  for (const p of PRESETS) {
    const o = document.createElement('option');
    o.value = p.id;
    o.textContent = p.label;
    sel.appendChild(o);
  }
  const applyPreset = () => {
    const p = PRESETS.find((x) => x.id === sel.value) || PRESETS[0];
    $('inj-screen').value = p.screen;
    $('inj-kind').value = p.kind;
  };
  sel.addEventListener('change', applyPreset);
  applyPreset();

  const pane = () => $('inj-pane').value.trim();
  $('inj-blocked').addEventListener('click', () => showResult('inj-result', post('/api/inject/blocked', {
    pane_id: pane(), alias: $('inj-alias').value.trim(), kind: $('inj-kind').value, screen: $('inj-screen').value,
  })));
  $('inj-done').addEventListener('click', () => showResult('inj-result', post('/api/inject/done', { pane_id: pane() })));
  $('inj-working').addEventListener('click', () => showResult('inj-result', post('/api/inject/state', { pane_id: pane(), state: 'working' })));
  $('inj-remove').addEventListener('click', () => showResult('inj-result', post('/api/inject/remove', { pane_id: pane() })));

  const sendUtterance = (text) => {
    if (!text) return;
    showResult('utt-result', post('/api/inject/utterance', { text }));
  };
  $('utt-send').addEventListener('click', () => sendUtterance($('utt-text').value.trim()));
  $('utt-text').addEventListener('keydown', (e) => { if (e.key === 'Enter') sendUtterance($('utt-text').value.trim()); });
  for (const b of document.querySelectorAll('.utt-quick')) {
    b.addEventListener('click', () => { $('utt-text').value = b.dataset.text; sendUtterance(b.dataset.text); });
  }
  $('pending-cancel').addEventListener('click', () => showResult('pending-result', post('/api/pending/cancel', {})));
}

// ---------------------------------------------------------- conversation

const conversation = new ConversationView($('conversation'));
// A short timer, not requestAnimationFrame: background tabs pause rAF and
// the view would fall behind the log until the tab is looked at.
let convTimer = 0;
function scheduleConvRender() {
  if (convTimer) return;
  convTimer = setTimeout(() => {
    convTimer = 0;
    conversation.render(false);
    $('conv-count').textContent = `${conversation.grouper.items.length} items`;
  }, 50);
}

// backfillConversation feeds the log tail into the grouper before the SSE
// replay (which only carries the last few hundred events); duplicates are
// dropped by their encoded line.
async function backfillConversation() {
  try {
    const r = await fetch(`/api/events?n=${BACKFILL}`, { cache: 'no-store' });
    const list = await r.json();
    for (const ev of list) conversation.add(ev, JSON.stringify(ev));
  } catch (err) {
    console.error('backfill', err);
  }
  try {
    const r = await fetch('/api/voice/system_prompt', { cache: 'no-store' });
    if (r.ok) conversation.setCurrentPrompt(await r.json());
  } catch (_) { /* no prompt to show */ }
  scheduleConvRender();
}

function setupConversation() {
  $('conv-expand').addEventListener('click', () => conversation.setExpandAll(true));
  $('conv-collapse').addEventListener('click', () => conversation.setExpandAll(false));
  $('conv-hide-silent').addEventListener('change', (e) => { conversation.hideSilent = e.target.checked; conversation.render(true); });
  $('conv-hide-stats').addEventListener('change', (e) => { conversation.hideStats = e.target.checked; conversation.render(true); });
  const pill = $('conv-new');
  conversation.onNewMessages = (show) => pill.classList.toggle('hidden', !show);
  pill.addEventListener('click', () => conversation.scrollToBottom());
  const sendChat = () => {
    const text = $('conv-text').value.trim();
    if (!text) return;
    $('conv-text').value = '';
    showResult('conv-result', post('/api/inject/utterance', { text }));
  };
  $('conv-send').addEventListener('click', sendChat);
  $('conv-text').addEventListener('keydown', (e) => { if (e.key === 'Enter') sendChat(); });
}

// ------------------------------------------------------------------ tabs

function showTab(name) {
  for (const b of document.querySelectorAll('#tabs button')) b.classList.toggle('active', b.dataset.tab === name);
  for (const sec of document.querySelectorAll('main > .tab')) sec.classList.toggle('hidden', sec.id !== `tab-${name}`);
  if (name === 'conversation') conversation.render(true);
  if (name === 'events' && $('events-follow').checked) $('events').scrollTop = $('events').scrollHeight;
  try { localStorage.setItem('sheepdog-debug-tab', name); } catch (_) { /* private mode */ }
}

// pickTab: ?tab= wins, then the last choice, else Conversation when a voice
// session exists and Events otherwise.
async function pickTab() {
  const param = new URLSearchParams(location.search).get('tab');
  if (param) return showTab(param);
  let saved = null;
  try { saved = localStorage.getItem('sheepdog-debug-tab'); } catch (_) { /* fine */ }
  if (saved) return showTab(saved);
  try {
    const st = await (await fetch('/api/state', { cache: 'no-store' })).json();
    return showTab(st.voice && st.voice.mode !== 'off' ? 'conversation' : 'events');
  } catch (_) {
    return showTab('events');
  }
}

function setupTabs() {
  for (const b of document.querySelectorAll('#tabs button')) b.addEventListener('click', () => showTab(b.dataset.tab));
}

// ------------------------------------------------------------------ main

setupTabs();
setupEventControls();
setupSettings();
setupInject();
setupConversation();
pickTab();
backfillConversation().then(connectEvents);
refreshState();
setInterval(refreshState, STATE_POLL_MS);

// Console handle: feed extra log lines into the conversation view (e.g.
// paste a voice-test run) or inspect its items.
window.sheepdogDebug = { conversation, addEvents: (list) => { for (const ev of list) conversation.add(ev); scheduleConvRender(); } };
