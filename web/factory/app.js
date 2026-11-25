// The factory page (design.md 12.1, the FactoryWeb* boards): polls the factory
// views and delegates ask answers, setting proposals and browser pairing to asks.js & settings.js.
import { esc, isOwnerAsk, renderAskCardsHTML, createAskFlow, browserAskDeps } from './asks.js';
import { renderSettingsHTML, proposeSetting, parseModelCandidates, editorAlertHTML } from './settings.js';
import { renderMarkdown } from '../static/markdown.js';
import { goalTitle } from './goal.js';

const POLL_MS = 3000, GIVE_UP_MS = 8000, NOTICE_MS = 5000;
const $ = (id) => document.getElementById(id);
const enc = encodeURIComponent;
const clock = (t, sec) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: sec ? '2-digit' : undefined });
// stage (design.md 4.11) -> board group label, dot colour, alert flag; ORDER is the boards' group order.
const STAGE = { owner_gate: ['needs your approval', 'blocked', true], owner_answer: ['waiting on you', 'blocked', true],
  blocked_or_failed: ['blocked', 'blocked', true], ci: ['waiting on CI', 'working'], working: ['working', 'working'],
  waiting_on_dep: ['waiting on others', 'idle'], queued: ['queued', 'idle'], done: ['done', 'done'] };
const ORDER = ['owner_gate', 'owner_answer', 'blocked_or_failed', 'ci', 'working', 'waiting_on_dep', 'queued'];
// A stage this page does not know yet shows under its raw name rather than vanishing.
const stage = (t) => STAGE[t.stage] || [t.stage || 'unknown', 'idle'];
let data = null, down = false, lastOK = 0, timer = 0, busy = false, again = false, curNarrow = true;
const started = Date.now(), noticeTimers = new Map();
const mq = globalThis.matchMedia?.('(max-width: 760px)') || null;
const askFlow = createAskFlow(browserAskDeps());
const askUI = { busyId: '', error: '', errorAskId: '', needsPairing: false, pairDone: false, pairMessage: '', pairError: '', pairCode: '', pairingBusy: false, pending: null };
const setUI = { project: '', rows: null, error: '', busy: '', edits: {}, errors: {}, fixes: {}, notices: {}, editor: null, copied: '' };
const pairRow = () => askUI.pending?.kind === 'setting' ? askUI.pending.name : (askUI.pending?.row || '');
const setState = () => ({ ...setUI, pairRow: pairRow(), askBusyId: askUI.busyId, pairState: askUI });
const clearPair = () => Object.assign(askUI, { needsPairing: false, pairDone: false, pending: null, pairError: '', pairCode: '' });
const findRow = (n) => n && [...$('settings').querySelectorAll('[data-row]')].find((x) => x.dataset.row === n);
const findCtrl = (n) => n && [...$('settings').querySelectorAll('[data-setting]')].find((x) => x.dataset.setting === n);
const focusRow = (n, r = findRow(n)) => (r?.querySelector('button[data-cand-save], button[data-propose], button[data-default], button[data-ask-id], button[data-edit]') || r?.querySelector('[data-setting]'))?.focus({ preventScroll: true });
const setNotice = (n, txt) => {
  clearTimeout(noticeTimers.get(n)); setUI.notices[n] = txt;
  noticeTimers.set(n, setTimeout(() => { delete setUI.notices[n]; noticeTimers.delete(n); if (data) render(); }, NOTICE_MS));
};

async function get(url, retried = false) {
  const r = await fetch(url, { cache: 'no-store', signal: AbortSignal.timeout(2 * POLL_MS) });
  const body = await r.json().catch(() => ({}));
  if (r.status === 401 && !retried) {
    const auth = await askFlow.reauth();
    if (auth.ok) { askUI.needsPairing = false; return get(url, true); }
    throw Object.assign(new Error(body.error || 'HTTP 401'), { http: true, needsPairing: auth.needsPairing, pairMessage: auth.message });
  }
  if (!r.ok) throw Object.assign(new Error(body.error || `HTTP ${r.status}`), { http: true });
  return body;
}

// route reads #project/task; a hand-edited hash that does not decode means the project list.
function route() {
  try { return location.hash.slice(1).split('/').map(decodeURIComponent); } catch { return []; }
}

// One poll at a time and one timer: a poll asked for while one runs (link, Try now)
// makes the running one drop its answer and fetch again when it ends.
async function poll() {
  clearTimeout(timer);
  if (busy) { again = true; return; }
  busy = true; again = false;
  const [want, task] = route();
  let next = data, off = false, setP = null;
  try {
    const [{ projects = [] }, { asks = [] }] = await Promise.all([get('/api/factory/projects'), get('/api/factory/asks')]);
    const p = projects.some((x) => x.id === want) ? want : (projects[0]?.id || '');
    if (setUI.project !== p) {
      noticeTimers.forEach(clearTimeout); noticeTimers.clear();
      Object.assign(setUI, { project: p, rows: null, error: '', busy: '', edits: {}, errors: {}, fixes: {}, notices: {}, editor: null, copied: '' });
    }
    let graph = null, log = [], problem = '';
    if (p) {
      setP = get(`/api/factory/projects/${enc(p)}/settings`).then((b) => ({ rows: b.rows || [] }), (e) => ({ error: e.message || 'error', http: e.http, needsPairing: e.needsPairing, pairMessage: e.pairMessage }));
      try {
        [graph, { lines: log }] = await Promise.all([get(`/api/factory/projects/${enc(p)}/graph`), get(`/api/factory/projects/${enc(p)}/log?n=100`)]);
      } catch (e) { if (!e.http) throw e; if (e.needsPairing) Object.assign(askUI, { needsPairing: true, pairMessage: e.pairMessage || '' }); problem = e.message; }
    }
    next = { projects, asks, p, task, graph, log: log || [], problem };
  } catch (e) {
    if (e.http) { if (e.needsPairing) Object.assign(askUI, { needsPairing: true, pairMessage: e.pairMessage || '' }); next = { projects: [], asks: [], p: '', task, graph: null, log: [], problem: e.message }; }
    else off = true;
  }
  const prev = data;
  data = next; down = off;
  if (!off) lastOK = Date.now();
  render();
  if (setP && !off) {
    const sr = await setP;
    if (sr.rows) { setUI.rows = sr.rows; setUI.error = ''; }
    else if (sr.http || !setUI.rows) { setUI.error = sr.error; if (sr.needsPairing) Object.assign(askUI, { needsPairing: true, pairMessage: sr.pairMessage || '' }); }
    else down = true;
    const prevPr = prev?.p === next?.p && prev?.projects.find((x) => x.id === next.p), nextPr = prevPr && next.projects.find((x) => x.id === next.p);
    if (prevPr && nextPr && (prevPr.state ? prevPr.state === 'running' : prevPr.running) && (nextPr.state ? nextPr.state === 'finished' : !nextPr.running)) {
      for (const a of prev.asks || []) if (a.status === 'open' && a.set && (!a.project || a.project === next.p) && !setUI.notices[a.set.name]
        && !next.asks.some((x) => x.id === a.id || (x.status === 'open' && x.project === next.p && x.set?.name === a.set.name))
        && (setUI.rows || []).find((r) => r.name === a.set.name)?.value !== a.set.value) {
        setNotice(a.set.name, 'Not saved: the project finished before you answered, so this change was cancelled. You can make it again.');
      }
    }
    render();
  }
  busy = false;
  if (again) return poll();
  timer = setTimeout(poll, POLL_MS);
}

// set replaces an element's HTML only when it changed, then reopens the <details>
// that were open and puts keyboard focus back on the same control.
const key = (e) => [e.tagName, e.dataset?.setting || e.id || e.getAttribute('href') || e.dataset?.askId || e.closest?.('[data-row]')?.dataset.row || e.closest?.('[data-ask-id]')?.dataset.askId || '', e.dataset?.option || e.getAttribute('aria-label') || e.className || e.parentElement?.className || e.textContent?.slice(0, 24) || ''].join(':');
function set(el, html) {
  if (el._html === html) return;
  const open = [...el.querySelectorAll('details[open]')].map(key);
  const active = el.contains(document.activeElement) ? document.activeElement : null;
  const focus = active && key(active), sel = active ? [active.selectionStart, active.selectionEnd] : null;
  el.innerHTML = html; el._html = html;
  for (const d of el.querySelectorAll('details')) d.open = open.includes(key(d));
  const hit = focus && [...el.querySelectorAll('a, button, summary, input, select, textarea')].find((x) => key(x) === focus);
  if (hit) { hit.focus({ preventScroll: true }); if (sel && sel[0] != null && hit.setSelectionRange) { try { hit.setSelectionRange(sel[0], sel[1]); } catch {} } }
}

function render() {
  $('link-dot').className = 'dot ' + (down ? 'idle' : 'done');
  $('link-text').textContent = down ? 'not connected' : 'live';
  $('offline').hidden = !(down && data);
  if (down && data) $('offline').textContent = `Can't reach sheepdog since ${clock(lastOK)}. Showing what it showed last; trying again.`;
  const waiting = !data;
  $('loading').hidden = !waiting || (down && Date.now() - started > GIVE_UP_MS);
  $('unreachable').hidden = !waiting || $('loading').hidden === false;
  $('skeleton').hidden = !waiting;
  $('board').hidden = waiting;
  if (waiting) return;
  const tasks = data.graph ? [...data.graph.tasks].sort((a, b) => a.layer - b.layer) : [];
  const t = data.task && tasks.find((x) => x.ref === data.task), pr = data.projects.find((x) => x.id === data.p);
  const narrow = !t && Boolean(mq ? mq.matches : globalThis.innerWidth <= 760);
  if (curNarrow !== narrow && $('main').after) {
    if (narrow) { $('main').before($('asks')); $('projects').after($('settings')); }
    else { $('main').after($('settings')); $('projects').after($('asks')); }
    curNarrow = narrow;
  }
  $('aside').hidden = !!t; $('asks').hidden = !!t; $('tasks-top').hidden = !narrow;
  set($('tasks-top'), narrow ? boardHTML(tasks, 'top') : '');
  set($('main'), t ? taskHTML(t) : boardHTML(tasks, narrow ? 'body' : ''));
  $('settings').hidden = !!t || !pr || !!data.problem;
  $('settings').className = setUI.error ? 'card dashed' : 'card';
  if (!$('settings').hidden) set($('settings'), renderSettingsHTML(setUI.rows, data.asks, setState()));
  set($('projects'), `<h2>projects</h2>${data.projects.map(projHTML).join('') || '<p class="muted">None yet.</p>'}`);
  const asks = (data.asks || []).filter(isOwnerAsk);
  set($('asks'), `<div class="head"><h2>asks for you</h2>${asks.length ? `<span>${asks.length} open</span>` : ''}</div>${renderAskCardsHTML(asks, askUI)}`);
  const runs = tasks.filter((x) => x.terminal);
  set($('agents'), `<div class="head"><h2>agents</h2><span>${runs.length} running</span></div>` + (runs.map((x) =>
    `<div class="agent"><span class="dot ${stage(x)[1]}" aria-hidden="true"></span><span><code>${esc(x.ref)}</code> ${esc(x.model || 'worker')} · ${esc(x.why_waiting || stage(x)[0])}</span></div>`).join('') || '<p class="muted">No workers running.</p>'));
}

const counts = (c = {}) => Object.entries(c).map(([k, n]) => `${n} ${(STAGE[k] || [k])[0]}`).join(' · ');
const projHTML = (pr) => `<a class="proj" href="#${enc(pr.id)}"${pr.id === data.p ? ' aria-current="true"' : ''}><span class="proj-title">${esc(goalTitle(pr.goal, pr.id))}</span>` +
  `<span class="muted">${pr.error ? esc(pr.error) : (pr.running ? 'running' : 'stopped') + (counts(pr.counts) ? ' · ' + esc(counts(pr.counts)) : '')}</span></a>`;

function boardHTML(tasks, part) {
  const pr = data.projects.find((x) => x.id === data.p), done = tasks.filter((x) => x.stage === 'done');
  let top = `<div class="head"><h1>tasks</h1>${pr ? `<span>${done.length} of ${tasks.length} done</span>` : ''}</div>`, body = '';
  if (data.problem) body = `<section class="card" role="status"><h2>can't show the factory</h2><p class="muted">${esc(data.problem)}</p></section>`;
  else if (!pr) body = `<section class="card" aria-label="No tasks"><h2>nothing running</h2><p class="muted">No factory projects on this computer. Start one in a terminal with <code>sheepdog factory start &lt;folder&gt; --goal "…"</code>; it shows here within seconds.</p></section>`;
  else {
    const title = goalTitle(pr.goal, pr.id), rawGoal = (pr.goal || '').trim();
    top += `<p class="goal-title">${esc(title)}</p>` + (rawGoal && rawGoal !== title ? `<details class="goal-full"><summary>full project goal</summary><div class="md-body">${renderMarkdown(pr.goal)}</div></details>` : '');
    if (!tasks.length) body += `<section class="card" aria-label="No tasks"><h2>no tasks yet</h2><p class="muted">The coordinator has not planned this project yet.</p></section>`;
    const other = [...new Set(tasks.map((x) => x.stage))].filter((s) => s !== 'done' && !ORDER.includes(s));
    for (const s of [...ORDER, ...other]) {
      const rows = tasks.filter((x) => x.stage === s), [rawLabel, , alert] = stage(rows[0] || { stage: s }), label = esc(rawLabel);
      if (rows.length) body += `<section class="group" aria-label="${label}"><h2${alert ? ' class="alert"' : ''}>${label} <span class="count">${rows.length}</span></h2><div class="rows${alert ? ' alert' : ''}">${rows.map(rowHTML).join('')}</div></section>`;
    }
    if (done.length) body += `<details class="done"><summary>${done.length} done · show them</summary><div class="rows">${done.map(rowHTML).join('')}</div></details>`;
    body += logHTML(data.log, 'log');
  }
  return part === 'top' ? top : part === 'body' ? body : top + body;
}

function rowHTML(t) {
  const [label, dot] = stage(t), why = t.why_waiting || label;
  const model = t.model || (t.stage === 'queued' || t.stage === 'waiting_on_dep' ? 'not started' : '');
  const rounds = roundsDone(t);
  const say = [t.ref, t.title, label, why !== label && why, rounds, model, `layer ${t.layer}`].filter(Boolean).join(', ');
  return `<a class="row" href="#${enc(data.p)}/${enc(t.ref)}" aria-label="${esc(say)}"><span class="dot ${dot}" aria-hidden="true"></span>` +
    `<span class="what"><span><code>${esc(t.ref)}</code> <b>${esc(t.title)}</b></span><span class="why">${esc(why)}</span></span>` +
    `<span class="chips">${[model, rounds].filter(Boolean).map((c) => `<span class="chip">${esc(c)}</span>`).join('')}<span>layer ${t.layer}</span></span></a>`;
}

// t.rounds counts finished review rounds; why_waiting names the one running ("In review round 2").
const roundsDone = (t) => t.rounds ? `${t.rounds} round${t.rounds > 1 ? 's' : ''} done` : '';

const logWhy = (l) => (l.by === 'owner' && l.decision === 'started' && l.why ? goalTitle(l.why, '') : l.why);
const logHTML = (lines, title, empty = 'Nothing logged yet.') => `<section class="card" aria-label="${title}"><h2>${title}</h2>` + (lines.length
  ? `<ul class="log">${lines.slice().reverse().map((l) => `<li><time datetime="${esc(l.at)}">${clock(l.at, true)}</time> ${esc(l.by)}` +
    `${l.task ? ` <code>${esc(l.task)}</code>` : ''} <b>${esc(l.decision)}</b> <span class="muted">${esc(logWhy(l))}</span></li>`).join('')}</ul>`
  : `<p class="muted">${empty}</p>`) + '</section>';

function taskHTML(t) {
  const [label, dot] = stage(t), kv = (k, v) => v ? `<div class="kv"><span>${k}</span><span>${esc(v)}</span></div>` : '';
  return `<a class="back" href="#${enc(data.p)}">‹ board</a><div class="task-head"><div class="meta"><code>${esc(t.ref)}</code>` +
    `<span class="pill ${dot}"><span class="dot ${dot}" aria-hidden="true"></span>${esc(label)} · ${esc(t.why_waiting || label)}</span>` +
    `${t.terminal ? `<span>run ${esc(t.terminal)}</span>` : ''}</div><h1>${esc(t.title)}</h1></div><div class="cols"><div class="col-main">` +
    `${t.brief && t.brief.trim() ? `<section class="card" aria-label="Brief"><h2>brief</h2><div class="md-body">${renderMarkdown(t.brief)}</div></section>` : ''}` +
    `<section class="card" aria-label="Verdict"><h2>the ladder says: ${esc(t.last_decision || 'nothing yet')}</h2><span class="muted">` +
    `${t.last_rule ? `rule ${esc(t.last_rule)}` : 'No step logged for this task yet.'}${t.rounds ? ` · review ${roundsDone(t)}` : ''}</span></section>` +
    logHTML(data.log.filter((l) => l.task === t.ref), 'recent log', data.log.length ? 'No log lines for this task in the last 100.' : undefined) + `</div><aside><section class="card" aria-label="Who worked on it"><h2>agents</h2>` +
    kv('model', t.model || 'not started') + kv('waits on', t.why_waiting) + kv('after', t.after.join(', ')) + kv('unblocks', t.dependents.join(', ')) +
    kv('layer', String(t.layer)) + '</section></aside></div>';
}

async function submitSetting(project, name, value) {
  setUI.busy = name; delete setUI.errors[name]; delete setUI.fixes[name]; delete setUI.notices[name];
  if (!askUI.pairDone) clearPair();
  render();
  const res = await proposeSetting(askFlow, project, name, value).catch((e) => ({ ok: false, message: e.message || 'Network error.' }));
  setUI.busy = '';
  if (res.ok) { delete setUI.edits[name]; if (setUI.editor?.row === name) setUI.editor = null; clearPair(); await poll(); return focusRow(name); }
  if (res.needsPairing) {
    Object.assign(askUI, { needsPairing: false, pairDone: false, pairMessage: res.message, pending: { kind: 'setting', project, name, value } });
    render(); return $('settings').querySelector('input[name="code"]')?.focus({ preventScroll: true });
  }
  clearPair(); setUI.errors[name] = res.message || 'Could not change setting.'; if (res.fix) setUI.fixes[name] = res.fix; render(); focusRow(name);
}

async function submitAnswer(askId, option, inRow = '') {
  const ask = (data?.asks || []).find((a) => a.id === askId);
  Object.assign(askUI, { busyId: askId, error: '', errorAskId: '', pairError: '' });
  if (!askUI.pairDone) Object.assign(askUI, { needsPairing: false, pending: null });
  render();
  const res = await askFlow.answer(askId, option).catch((e) => ({ ok: false, message: e.message || 'Network error.' }));
  askUI.busyId = '';
  if (res.ok) {
    clearPair();
    if (ask?.set) {
      const n = ask.set.name, isMod = (setUI.rows || []).find((r) => r.name === n)?.clis?.length > 0;
      delete setUI.edits[n]; delete setUI.errors[n]; delete setUI.fixes[n];
      setNotice(n, option === 'Save' ? (isMod ? 'Saved. The next agent started for this role uses it. Running agents keep their model.' : 'Saved. The engine uses it from now on.') : `Kept at ${ask.set.was || 'not set'}. Nothing changed.`);
    }
    await poll();
    return (inRow ? (findCtrl(inRow) || findRow(inRow)?.querySelector('button[data-edit]')) : $('asks').querySelector('button[data-ask-id], a, summary'))?.focus({ preventScroll: true });
  }
  if (res.needsPairing) {
    Object.assign(askUI, { needsPairing: !inRow, pairDone: false, pairMessage: res.message, pending: { kind: 'ask', askId, option, row: inRow } });
    render(); return (inRow ? $('settings') : $('asks')).querySelector('input[name="code"]')?.focus({ preventScroll: true });
  }
  clearPair();
  const msg = ask?.set && res.code === 'out_of_range' ? `Not saved: another setting changed after you asked. ${res.message}` : (res.message || 'Could not record answer.');
  if (ask?.set) setUI.errors[ask.set.name] = msg;
  if (!inRow) Object.assign(askUI, { error: msg, errorAskId: askId });
  render();
  [...(inRow ? findRow(inRow) || $('settings') : $('asks')).querySelectorAll('button[data-ask-id]')].find((b) => b.dataset.askId === askId && b.dataset.option === option)?.focus({ preventScroll: true });
}

const syncEdit = (e) => {
  const ctrl = e.target.closest?.('[data-setting]');
  if (ctrl && data) {
    const n = ctrl.dataset.setting, row = (setUI.rows || []).find((r) => r.name === n);
    if (row && ctrl.value === row.value) delete setUI.edits[n]; else setUI.edits[n] = ctrl.value;
    clearTimeout(noticeTimers.get(n)); noticeTimers.delete(n);
    delete setUI.errors[n]; delete setUI.notices[n];
    const html = renderSettingsHTML(setUI.rows, data.asks, setState()), tmp = document.createElement('div');
    tmp.innerHTML = html;
    const srow = ctrl.closest('[data-row]'), nextRow = [...tmp.querySelectorAll('[data-row]')].find((x) => x.dataset.row === n);
    const top = srow?.querySelector('.srow-top'), nextTop = nextRow?.querySelector('.srow-top'), nextCtrl = nextTop?.querySelector('[data-setting]');
    if (top && nextTop && nextCtrl && srow.appendChild) {
      ctrl.className = nextCtrl.className;
      ctrl.setAttribute('aria-describedby', nextCtrl.getAttribute('aria-describedby'));
      top.querySelector('button')?.remove();
      const btn = nextTop.querySelector('button'); if (btn) top.appendChild(btn);
      while (top.nextElementSibling) top.nextElementSibling.remove();
      while (nextTop.nextElementSibling) srow.appendChild(nextTop.nextElementSibling);
      $('settings')._html = html;
    } else render();
  } else if (e.target.dataset?.candField && setUI.editor && data) {
    const c = setUI.editor.cands[+e.target.dataset.candIdx], f = e.target.dataset.candField, n = setUI.editor.row;
    if (!c) return;
    c[f] = e.target.value; delete setUI.errors[n]; delete setUI.fixes[n];
    if (f !== 'model') return render();
    const li = e.target.closest('li.cand'), al = e.target.closest('[data-row]')?.querySelector('.cand-alert'), tag = `${c.cli} ${c.model.trim() || 'model'}`;
    e.target.classList.remove('bad'); li?.querySelector('[data-cand-up]')?.setAttribute('aria-label', `Move ${tag} up`); li?.querySelector('[data-cand-rm]')?.setAttribute('aria-label', `Remove ${tag}`);
    if (al) al.innerHTML = ''; $('settings')._html = renderSettingsHTML(setUI.rows, data.asks, setState());
  } else if (e.target.name === 'code') askUI.pairCode = e.target.value;
};
$('board').addEventListener('input', syncEdit);
$('board').addEventListener('change', syncEdit);
$('board').addEventListener('keydown', (e) => {
  const ctrl = e.key === 'Enter' && e.target.closest?.('input[data-setting]'), n = ctrl?.dataset.setting;
  if (ctrl && setUI.edits[n] !== undefined) {
    e.preventDefault();
    if (!setUI.busy && !(data?.asks || []).some((a) => a.status === 'open' && a.set?.name === n && (!a.project || a.project === data.p)) && (!ctrl.reportValidity || ctrl.reportValidity())) submitSetting(data.p, n, ctrl.value);
  }
});
const openModelCands = (r, raw) => parseModelCandidates(raw, r.clis[0]).map((c) => ({ ...c, cli: r.clis.includes(c.cli) ? c.cli : r.clis[0] }));
$('board').addEventListener('click', (e) => {
  const opt = e.target.closest('button[data-ask-id][data-option]');
  if (opt && !askUI.busyId) return submitAnswer(opt.dataset.askId, opt.dataset.option, opt.closest('[data-row]')?.dataset.row || '');
  const ed = e.target.closest('button[data-edit]');
  if (ed) { const n = ed.dataset.edit, r = (setUI.rows || []).find((x) => x.name === n); if (r) { delete setUI.errors[n]; delete setUI.fixes[n]; setUI.editor = { row: n, cands: openModelCands(r, r.value || r.default) }; render(); findRow(n)?.querySelector('select, input')?.focus({ preventScroll: true }); } return; }
  if (setUI.editor) {
    const n = setUI.editor.row, r = (setUI.rows || []).find((x) => x.name === n), cs = setUI.editor.cands, clr = () => { delete setUI.errors[n]; delete setUI.fixes[n]; };
    if (e.target.closest('button[data-cand-add]') && r) { cs.push({ cli: r.clis[0], model: '', effort: 'medium' }); clr(); render(); return findRow(n)?.querySelectorAll('input')[cs.length - 1]?.focus({ preventScroll: true }); }
    if (e.target.closest('button[data-cand-default]') && r) { setUI.editor.cands = openModelCands(r, r.default); clr(); render(); return findRow(n)?.querySelector('select, input')?.focus({ preventScroll: true }); }
    const up = e.target.closest('button[data-cand-up]'), rm = e.target.closest('button[data-cand-rm]');
    if (up || (rm && cs.length > 1)) { if (up) { const i = +up.dataset.candUp; if (i > 0) [cs[i - 1], cs[i]] = [cs[i], cs[i - 1]]; } else cs.splice(+rm.dataset.candRm, 1); clr(); render(); if (!$('settings').contains(document.activeElement)) findRow(n)?.querySelector('select, input')?.focus({ preventScroll: true }); return; }
    if (e.target.closest('button[data-cand-cancel]')) { setUI.editor = null; clr(); render(); return findRow(n)?.querySelector('button[data-edit]')?.focus({ preventScroll: true }); }
    if (e.target.closest('button[data-cand-save]') && !setUI.busy) {
      const emptyIdx = cs.findIndex((c) => !c.model.trim());
      if (emptyIdx < 0) return submitSetting(data.p, n, cs.map((c) => `${c.cli}/${c.model.trim()}/${c.effort}`).join(', '));
      setUI.errors[n] = 'Type a model name.'; delete setUI.fixes[n];
      const srow = findRow(n), al = srow?.querySelector('.cand-alert'), mIn = srow?.querySelectorAll('input')[emptyIdx];
      if (al) { al.innerHTML = editorAlertHTML('set-err-' + n.replace(/[^a-z0-9]+/gi, '-'), setUI.errors[n], ''); mIn?.classList.add('bad'); $('settings')._html = renderSettingsHTML(setUI.rows, data.asks, setState()); } else render();
      return mIn?.focus({ preventScroll: true });
    }
  }
  const cp = e.target.closest('button[data-copy]');
  if (cp) { navigator.clipboard?.writeText?.(cp.dataset.copy).catch(() => {}); setUI.copied = cp.dataset.copy; render(); setTimeout(() => { if (setUI.copied === cp.dataset.copy) { setUI.copied = ''; if (data) render(); } }, 2000); return; }
  const prop = e.target.closest('button[data-propose]'), def = e.target.closest('button[data-default]');
  if (prop && !setUI.busy) { const n = prop.dataset.propose, ctrl = findCtrl(n); if (!ctrl?.reportValidity || ctrl.reportValidity()) submitSetting(data.p, n, setUI.edits[n] ?? ctrl?.value ?? ''); }
  else if (def && !setUI.busy) submitSetting(data.p, def.dataset.default, (setUI.rows || []).find((r) => r.name === def.dataset.default)?.default ?? '');
});
$('board').addEventListener('submit', async (e) => {
  const form = e.target.closest('form[data-pair-form]');
  if (!form || askUI.pairingBusy) return;
  e.preventDefault();
  const code = form.elements.code?.value || '';
  Object.assign(askUI, { pairingBusy: true, pairError: '', pairCode: code }); render();
  const res = await askFlow.pairWithCode(code).catch((err) => ({ ok: false, message: err.message || 'Pairing failed.' }));
  askUI.pairingBusy = false;
  if (!res.ok) { askUI.pairError = res.message || 'Pairing failed.'; render(); return (pairRow() ? $('settings') : $('asks')).querySelector('input[name="code"]')?.focus({ preventScroll: true }); }
  const pending = askUI.pending;
  Object.assign(askUI, { pairDone: Boolean(pending), pairError: '', pairCode: '' });
  if (pending?.kind === 'setting') await submitSetting(pending.project, pending.name, pending.value);
  else if (pending?.askId) await submitAnswer(pending.askId, pending.option, pending.row || '');
  else { clearPair(); render(); await poll(); }
});
if (mq?.addEventListener) mq.addEventListener('change', () => data && render());
else addEventListener('resize', () => data && render());
$('retry').addEventListener('click', poll);
addEventListener('hashchange', () => { scrollTo(0, 0); poll(); });
poll();
