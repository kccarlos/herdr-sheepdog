// sheepdog /debug: a viewer of the event log plus the fake-injection
// controls (plan §3.8, decision D10). Vanilla JS module, no build step.
// Tabs: Swimlane (swimlane-view.js), Conversation (conversation-view.js),
// Supervisor (supervisor-view.js over the brain's transcript API), Events
// (the raw list), State.
import { ConversationView } from './conversation-view.js';
import { SwimlaneView } from './swimlane-view.js';
import { SupervisorView } from './supervisor-view.js';
import { LANES } from './swimlane.js';
import { offState, parseTranscriptJSONL } from './supervisor.js';

const $ = (id) => document.getElementById(id);
const BACKFILL = 2000;
const SOURCES = ['herdr', 'voice', 'supervisor', 'brain', 'llm', 'cli', 'web', 'eventlog', 'daemon'];
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
  row.dataset.ts = ev.ts || '';
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
    selectTime(ev.ts);
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
  // The backfill and the SSE replay overlap; the grouper's dedupe says
  // whether this line is new, so the brain views count it once.
  const fresh = conversation.add(ev, encoded);
  swimlane.add(ev, encoded);
  if (fresh) onBrainEvent(ev);
  scheduleConvRender();
  if (events.paused) {
    events.buffer.push(ev);
    $('events-pause').textContent = `Resume (${events.buffer.length})`;
    return;
  }
  renderEvent(ev);
  $('events-count').textContent = `${$('events').querySelectorAll('.ev:not(.hidden-by-filter)').length} shown / ${events.count} received`;
  if (/^(herdr\.transition|pending\.|supervisor\.|daemon\.|voice\.connect|voice\.closed|web\.inject|brain\.model_changed)/.test(ev.type)) {
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
    td(a.current_task || (a.last_task ? `(last: ${a.last_task})` : ''), 'task');
    const lb = a.last_block;
    td(lb ? `${lb.question || '(no question)'}${lb.answered ? ' ✓' : ''}` : '', 'block');
    const lr = a.last_result;
    td(lr ? `${lr.spoken} [${lr.outcome}, ${since(lr.at)} ago]` : '', 'result');
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
  const brain = st.brain && st.brain.model ? ` · brain=${st.brain.model}` : '';
  $('daemon-info').textContent = `v${st.version || '?'} · herdr=${st.herdr}${brain} · voice_connected=${st.session && st.session.voice_connected}`;
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

// ---- the brain's model picker: the options come from GET
// /api/brain/models (once at start, and on ↻ with ?refresh=1); the
// selection is re-synced from /api/state (polled, and refreshed on a
// brain.model_changed event), so a switch made elsewhere shows up
// without rebuilding the list under the mouse. Hidden while the brain
// is off (503).
const modelState = { current: '', provider: 'gemini', models: [], error: '', stale: false, showAll: false, off: true };

function renderModelControl() {
  const box = $('model-control');
  box.classList.toggle('hidden', modelState.off);
  if (modelState.off) return;
  const sel = $('model-select');
  if (document.activeElement === sel) return;
  const recommendedOnly = !modelState.showAll;
  const listed = modelState.models.filter((m) => !recommendedOnly || m.recommended || m.name === modelState.current);
  // The current model is always an option, even when the catalog does
  // not (yet, or any more) list it.
  if (modelState.current && !listed.some((m) => m.name === modelState.current)) {
    listed.unshift({ name: modelState.current, display_name: 'not in the discovered list', recommended: false, unknown: true });
  }
  sel.replaceChildren();
  for (const m of listed) {
    const o = document.createElement('option');
    o.value = m.name;
    o.textContent = m.unknown ? `${m.name} (?)` : m.name;
    const bits = [m.display_name || m.name];
    if (m.input_token_limit) bits.push(`in ${m.input_token_limit.toLocaleString()}`);
    if (!m.recommended && !m.unknown) bits.push('not a text model the brain is known to run');
    o.title = bits.join(' · ');
    sel.appendChild(o);
  }
  sel.value = modelState.current;
  const note = $('model-note');
  if (modelState.error) {
    sel.title = `model list: ${modelState.error}`;
    note.textContent = modelState.stale ? 'list may be stale' : 'no model list';
    note.title = modelState.error;
    note.classList.add('error');
  } else {
    sel.title = `${modelState.models.length} models discovered · ${listed.length} shown`;
    note.textContent = '';
    note.title = '';
    note.classList.remove('error');
  }
}

// applyModels takes a GET /api/brain/models or PUT /api/brain/model body.
function applyModels(status, data) {
  if (status === 503) { modelState.off = true; renderModelControl(); return; }
  if (status !== 200) { modelState.error = (data && data.error) || `${status}`; renderModelControl(); return; }
  modelState.off = false;
  modelState.current = data.model || '';
  modelState.provider = data.provider || 'gemini';
  modelState.models = Array.isArray(data.models) ? data.models : [];
  modelState.error = data.error || '';
  modelState.stale = !!data.stale;
  renderModelControl();
}

async function fetchBrainModels(refresh) {
  try {
    const { status, data } = await getJSON(`/api/brain/models${refresh ? '?refresh=1' : ''}`);
    applyModels(status, data);
  } catch (err) {
    modelState.error = String(err);
    renderModelControl();
  }
}

// renderBrainModel keeps the selection in step with the daemon (the model
// can change through PUT /api/brain/model elsewhere, or a restart).
function renderBrainModel(brain) {
  const off = !brain || !brain.model;
  if (off !== modelState.off) {
    modelState.off = off;
    if (!off) { fetchBrainModels(false); return; }
    renderModelControl();
    return;
  }
  if (off || brain.model === modelState.current) return;
  modelState.current = brain.model;
  renderModelControl();
}

function setupModelControl() {
  const sel = $('model-select');
  const box = $('model-control');
  sel.addEventListener('change', async () => {
    const model = sel.value;
    box.classList.add('saving');
    try {
      const r = await fetch('/api/brain/model', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ provider: modelState.provider, model }) });
      const data = await r.json();
      if (!r.ok) throw new Error(data.error || `${r.status}`);
      sel.blur();
      applyModels(r.status, data);
      $('model-note').textContent = `switched to ${data.model}`;
      $('model-note').classList.remove('error');
    } catch (err) {
      console.error('model', err);
      sel.blur();
      $('model-note').textContent = `error: ${err.message}`;
      $('model-note').classList.add('error');
      fetchBrainModels(false);
    } finally {
      box.classList.remove('saving');
    }
  });
  $('model-refresh').addEventListener('click', () => fetchBrainModels(true));
  $('model-all').addEventListener('change', (e) => { modelState.showAll = e.target.checked; renderModelControl(); });
  fetchBrainModels(false);
}

async function refreshState() {
  try {
    const r = await fetch('/api/state', { cache: 'no-store' });
    const st = await r.json();
    renderAgents(st.agents || []);
    renderPending(st.pending_action);
    renderVoice(st.voice || {}, st);
    renderSettings(st.settings);
    renderBrainModel(st.brain);
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
  // done / working send the textarea too (an "after" preset shows what the
  // agent finished with; an empty textarea keeps the pane text as it is).
  $('inj-done').addEventListener('click', () => showResult('inj-result', post('/api/inject/done', { pane_id: pane(), screen: $('inj-screen').value })));
  $('inj-working').addEventListener('click', () => showResult('inj-result', post('/api/inject/state', { pane_id: pane(), state: 'working', screen: $('inj-screen').value })));
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
    renderSwimlane(false);
  }, 50);
}

function renderSwimlane(force) {
  if ($('tab-swimlane').classList.contains('hidden') && !force) return; // stays dirty until shown
  const built = swimlane.render(force);
  if (built) $('sw-count').textContent = `${built.shown} of ${built.total} rows · ${swimlane.events.length} events`;
}

// backfillConversation feeds the log tail into the grouper, the swimlane
// and the Supervisor tab's turn log before the SSE replay (which only
// carries the last few hundred events); duplicates are dropped by their
// encoded line.
async function backfillConversation() {
  try {
    const r = await fetch(`/api/events?n=${BACKFILL}`, { cache: 'no-store' });
    const list = await r.json();
    for (const ev of list) {
      const enc = JSON.stringify(ev);
      const fresh = conversation.add(ev, enc);
      swimlane.add(ev, enc);
      if (fresh) onBrainEvent(ev);
    }
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

// ------------------------------------------------------------ supervisor

// The Supervisor tab reads the brain's own store (GET /api/brain/…), not
// the event log (D10: one source of truth for the transcript); the
// brain.transcript event only says "fetch again". Context (the window)
// is re-fetched after any brain.* event, a little later than brain.turn
// because pruning happens after that event is logged.
const supervisor = new SupervisorView($('supervisor'), $('svp-meter'));
const brainState = { off: null, transcriptTimer: 0, contextTimer: 0, fetching: false, promptAsked: false };

async function getJSON(url) {
  const r = await fetch(url, { cache: 'no-store' });
  const text = await r.text();
  let data;
  try { data = JSON.parse(text); } catch { data = { error: text }; }
  return { status: r.status, data };
}

async function fetchBrainTranscript(full) {
  if (brainState.fetching) { brainState.transcriptTimer = 0; scheduleTranscriptFetch(200); return; }
  brainState.fetching = true;
  try {
    const since = full ? 0 : supervisor.lastSeq;
    const { status, data } = await getJSON(`/api/brain/transcript?since=${since}&n=2000`);
    const off = offState(status, data);
    if (off) { brainState.off = off; supervisor.setOff(off); }
    else if (status !== 200) supervisor.setError(data.error || `${status}`);
    else { brainState.off = null; supervisor.addMessages(data); supervisor.setError(''); }
  } catch (err) {
    supervisor.setError(String(err));
  } finally {
    brainState.fetching = false;
  }
  scheduleSupervisorRender();
}

async function fetchBrainContext() {
  try {
    const { status, data } = await getJSON('/api/brain/context');
    const off = offState(status, data);
    if (off) { brainState.off = off; supervisor.setOff(off); }
    else if (status === 200) supervisor.setContext(data);
  } catch (err) {
    supervisor.setError(String(err));
  }
  scheduleSupervisorRender();
}

async function fetchBrainPrompt() {
  try {
    const { status, data } = await getJSON('/api/brain/system_prompt');
    if (status === 200) supervisor.setPrompt(data);
  } catch (_) { /* the sections stay collapsed */ }
  scheduleSupervisorRender();
}

function scheduleTranscriptFetch(ms) {
  if (brainState.transcriptTimer) return;
  brainState.transcriptTimer = setTimeout(() => { brainState.transcriptTimer = 0; fetchBrainTranscript(false); }, ms);
}

function scheduleContextFetch(ms) {
  if (brainState.contextTimer) clearTimeout(brainState.contextTimer);
  brainState.contextTimer = setTimeout(() => { brainState.contextTimer = 0; fetchBrainContext(); }, ms);
}

function onBrainEvent(ev) {
  const type = ev.type || '';
  supervisor.addEvent(ev);
  if (type === 'brain.transcript') { scheduleTranscriptFetch(150); scheduleContextFetch(600); }
  else if (type === 'brain.turn' || type === 'brain.compaction') scheduleContextFetch(type === 'brain.turn' ? 700 : 200);
  else if (type === 'daemon.start') {
    // A new run restarts the seqs at 1: drop the old run and fetch
    // afresh (debounced: the backfill replays every start of the day).
    supervisor.reload();
    if (brainState.transcriptTimer) clearTimeout(brainState.transcriptTimer);
    brainState.transcriptTimer = setTimeout(() => { brainState.transcriptTimer = 0; fetchBrainTranscript(true).then(fetchBrainContext); }, 300);
  }
}

let svpTimer = 0;
function scheduleSupervisorRender() {
  if (svpTimer) return;
  svpTimer = setTimeout(() => {
    svpTimer = 0;
    if ($('tab-supervisor').classList.contains('hidden')) return; // stays dirty until shown
    supervisor.render(false);
    $('svp-count').textContent = `${supervisor.messages.size} messages`;
  }, 50);
}

function setupSupervisor() {
  $('svp-expand').addEventListener('click', () => supervisor.setExpandAll(true));
  $('svp-collapse').addEventListener('click', () => supervisor.setExpandAll(false));
  $('svp-hide-worker').addEventListener('change', (e) => supervisor.setFilter('hideWorker', e.target.checked));
  $('svp-hide-assistant').addEventListener('change', (e) => supervisor.setFilter('hideAssistant', e.target.checked));
  $('svp-window-only').addEventListener('change', (e) => supervisor.setFilter('windowOnly', e.target.checked));
  $('svp-refresh').addEventListener('click', () => { supervisor.reload(); fetchBrainTranscript(true).then(fetchBrainContext); });
  const pill = $('svp-new');
  supervisor.onNewMessages = (show) => pill.classList.toggle('hidden', !show);
  pill.addEventListener('click', () => supervisor.scrollToBottom());
  supervisor.onNeedPrompt = () => { if (!brainState.promptAsked) { brainState.promptAsked = true; fetchBrainPrompt(); } };
  const sendChat = () => {
    const text = $('svp-text').value.trim();
    if (!text) return;
    $('svp-text').value = '';
    showResult('svp-result', post('/api/inject/utterance', { text }));
  };
  $('svp-send').addEventListener('click', sendChat);
  $('svp-text').addEventListener('keydown', (e) => { if (e.key === 'Enter') sendChat(); });
  fetchBrainTranscript(true).then(fetchBrainContext);
}

// -------------------------------------------------------------- swimlane

const swimlane = new SwimlaneView($('swimlane'), $('sw-header'));

function setupSwimlane() {
  const lanes = $('sw-lanes');
  for (const lane of LANES) {
    const label = document.createElement('label');
    label.className = `lane-${lane.id}`;
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = true;
    cb.addEventListener('change', () => swimlane.toggleLane(lane.id, cb.checked));
    label.appendChild(cb);
    label.appendChild(document.createTextNode(lane.label));
    lanes.appendChild(label);
  }
  // Source filter as a hidden set: a source the page does not know (a
  // future brain.* family) stays visible until it gets its own box.
  const holder = $('sw-source-filters');
  const hidden = new Set();
  for (const src of SOURCES) {
    const label = document.createElement('label');
    label.className = `src ${src}`;
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = true;
    cb.addEventListener('change', () => {
      if (cb.checked) hidden.delete(src); else hidden.add(src);
      swimlane.setOption('hiddenSources', new Set(hidden));
    });
    label.appendChild(cb);
    label.appendChild(document.createTextNode(src));
    holder.appendChild(label);
  }
  $('sw-pane').addEventListener('input', (e) => swimlane.setOption('pane', e.target.value.trim()));
  $('sw-search').addEventListener('input', (e) => swimlane.setOption('search', e.target.value.trim().toLowerCase()));
  $('sw-hide-stats').addEventListener('change', (e) => swimlane.setOption('hideStats', e.target.checked));
  $('sw-hide-silent').addEventListener('change', (e) => swimlane.setOption('hideSilent', e.target.checked));
  $('sw-collapse').addEventListener('click', () => { swimlane.expanded.clear(); swimlane.render(true); });
  const pill = $('sw-new');
  swimlane.onNewEvents = (show) => pill.classList.toggle('hidden', !show);
  pill.addEventListener('click', () => swimlane.scrollToBottom());
  swimlane.onSelect = selectTime;
  swimlane.onHighlight = (chain) => {
    const info = $('sw-chain');
    info.replaceChildren();
    if (!chain) return;
    const b = document.createElement('b');
    b.textContent = chain;
    info.append('chain ', b, ' — click again to clear');
  };
  const sendChat = () => {
    const text = $('sw-text').value.trim();
    if (!text) return;
    $('sw-text').value = '';
    showResult('sw-result', post('/api/inject/utterance', { text }));
  };
  $('sw-send').addEventListener('click', sendChat);
  $('sw-text').addEventListener('keydown', (e) => { if (e.key === 'Enter') sendChat(); });
}

// ------------------------------------------------------------------ tabs

function showTab(name) {
  for (const b of document.querySelectorAll('#tabs button')) b.classList.toggle('active', b.dataset.tab === name);
  for (const sec of document.querySelectorAll('main > .tab')) sec.classList.toggle('hidden', sec.id !== `tab-${name}`);
  if (name === 'conversation') conversation.render(true);
  if (name === 'supervisor') { supervisor.render(true); $('svp-count').textContent = `${supervisor.messages.size} messages`; if (!brainState.off) fetchBrainContext(); }
  if (name === 'swimlane') renderSwimlane(true);
  if (name === 'events' && $('events-follow').checked) $('events').scrollTop = $('events').scrollHeight;
  try { localStorage.setItem('sheepdog-debug-tab', name); } catch (_) { /* private mode */ }
  const ts = selectedTime();
  if (ts) setTimeout(() => scrollTabTo(name, ts), 0);
}

// ---- shared time selection: a clicked card/row stores its timestamp in
// the URL hash (#t=…) and the other tabs scroll to it when shown.
function selectedTime() {
  const m = /[#&]t=([^&]+)/.exec(location.hash);
  return m ? decodeURIComponent(m[1]) : '';
}

function selectTime(ts) {
  if (!ts) return;
  history.replaceState(null, '', `#t=${encodeURIComponent(ts)}`);
}

function scrollTabTo(name, ts) {
  const t = Date.parse(ts);
  if (Number.isNaN(t)) return;
  if (name === 'swimlane') { swimlane.scrollToTs(ts); return; }
  const list = name === 'events' ? $('events') : (name === 'conversation' ? $('conversation') : (name === 'supervisor' ? $('supervisor') : null));
  if (!list) return;
  for (const row of list.children) {
    const rts = Date.parse(row.dataset.ts || '');
    if (!Number.isNaN(rts) && rts >= t && !row.classList.contains('hidden-by-filter')) {
      row.scrollIntoView({ block: 'center' });
      row.classList.add('sw-flash');
      setTimeout(() => row.classList.remove('sw-flash'), 1600);
      return;
    }
  }
}

// pickTab: ?tab= wins, then the last choice, else the Swimlane.
function pickTab() {
  const param = new URLSearchParams(location.search).get('tab');
  if (param) return showTab(param);
  let saved = null;
  try { saved = localStorage.getItem('sheepdog-debug-tab'); } catch (_) { /* fine */ }
  return showTab(saved || 'swimlane');
}

function setupTabs() {
  for (const b of document.querySelectorAll('#tabs button')) b.addEventListener('click', () => showTab(b.dataset.tab));
}

// ------------------------------------------------------------------ main

setupTabs();
setupEventControls();
setupSettings();
setupModelControl();
setupInject();
setupConversation();
setupSupervisor();
setupSwimlane();
pickTab();
backfillConversation().then(connectEvents);
refreshState();
setInterval(refreshState, STATE_POLL_MS);

// Console handle: feed extra log lines into the conversation view (e.g.
// paste a voice-test run), inspect its items, or load a brain transcript
// (a JSONL url such as /testdata/brain/sample-run.jsonl in --dev mode,
// JSONL text, or an array of messages) into the Supervisor tab.
window.sheepdogDebug = {
  conversation, swimlane, supervisor,
  addEvents: (list) => { for (const ev of list) { conversation.add(ev); swimlane.add(ev); onBrainEvent(ev); } scheduleConvRender(); scheduleSupervisorRender(); },
  clearEvents: () => { swimlane.clear(); renderSwimlane(true); },
  loadTranscript: async (src) => {
    let messages;
    if (Array.isArray(src)) messages = src.map((m, i) => ({ ...m, seq: typeof m.seq === 'number' ? m.seq : i + 1 }));
    else if (typeof src === 'string' && /^\s*\{/.test(src)) messages = parseTranscriptJSONL(src);
    else messages = parseTranscriptJSONL(await (await fetch(src, { cache: 'no-store' })).text());
    supervisor.setMessages(messages);
    showTab('supervisor');
    return messages.length;
  },
};
