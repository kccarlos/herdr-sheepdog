// The factory page (design.md 12.1, the FactoryWeb* boards): polls the factory
// views and delegates ask answers, setting proposals and browser pairing to asks.js & settings.js.
import { esc, renderAskCardsHTML, createAskFlow, browserAskDeps, OK_ICO } from './asks.js';
import { renderSettingsHTML, proposeSetting, parseModelCandidates } from './settings.js';
import { renderNewProjectHTML, renderProjectActionsHTML, loadNewProject, checkProject, startProject, stopProject, projectState, EMPTY_CARD_HTML, PLUS_ICO } from './project.js';
import { renderMarkdown } from '../static/markdown.js';
import { goalTitle } from './goal.js';
import { renderGraphSection, syncGraphBox, highlightGraph } from './graph.js';
import { renderLogHTML } from './log.js';
import { needsByProject, projectName, renderOverviewHTML, renderNeedsYouHTML } from './overview.js';

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
let data = null, down = false, lastOK = 0, timer = 0, busy = false, again = false, curNarrow = true, npSeq = 0, paSeq = 0, justStarted = null, boardNotice = null, boardNoticeTimer = 0, needsOpen = false, pendingAskFocus = null;
let taskView = (() => { try { return globalThis.sessionStorage?.getItem('factory-view') === 'graph' ? 'graph' : 'list'; } catch { return 'list'; } })();
const started = Date.now(), noticeTimers = new Map();
const mq = globalThis.matchMedia?.('(max-width: 760px)') || null;
const askFlow = createAskFlow(browserAskDeps());
const askUI = { busyId: '', error: '', errorAskId: '', needsPairing: false, pairDone: false, pairMessage: '', pairError: '', pairCode: '', pairingBusy: false, pending: null };
const setUI = { project: '', rows: null, error: '', busy: '', edits: {}, errors: {}, fixes: {}, notices: {}, editor: null, copied: '' };
const emptyNP = () => ({ open: false, rows: null, spaces: null, spacesError: false, folder: '', goal: '', notGit: false, goalErr: false, edits: {}, errors: {}, fixes: {}, editor: null, checks: null, hasChecked: false, checking: false, checkError: '', starting: false, startError: '' });
const emptyPA = () => ({ mode: '', goal: '', busy: '', error: '', checks: null });
const npUI = emptyNP(), paUI = emptyPA(), logUI = { task: '', observations: false, hasObservations: false, error: '' };
const pairRow = () => askUI.pending?.kind === 'setting' ? askUI.pending.name : (askUI.pending?.row || '');
const setState = () => ({ ...setUI, pairRow: pairRow(), askBusyId: askUI.busyId, pairState: askUI });
const npState = () => ({ ...npUI, projects: data?.projects || [], backProject: data?.p || '', copied: setUI.copied, pairState: askUI });
const paState = () => ({ ...paUI, copied: setUI.copied, pairState: askUI });
const paBar = () => document.querySelector('[aria-label="This project"]');
const paRoot = () => (curNarrow ? $('tasks-top') : $('main'));
const sortedTasks = () => (data?.graph ? [...data.graph.tasks].sort((a, b) => a.layer - b.layer) : []);
const npSet = () => ({ ...npUI.edits });
const clearPair = () => Object.assign(askUI, { needsPairing: false, pairDone: false, pending: null, pairError: '', pairCode: '' });
const findRow = (n, root = $('settings')) => n && [...root.querySelectorAll('[data-row]')].find((x) => x.dataset.row === n);
const findCtrl = (n) => n && [...$('settings').querySelectorAll('[data-setting]')].find((x) => x.dataset.setting === n);
const focusRow = (n, root = $('settings'), r = findRow(n, root)) => (r?.querySelector('button[data-cand-save], button[data-propose], button[data-default], button[data-ask-id], button[data-edit]') || r?.querySelector('[data-setting]'))?.focus({ preventScroll: true });
const setNotice = (n, txt) => {
  clearTimeout(noticeTimers.get(n)); setUI.notices[n] = txt;
  noticeTimers.set(n, setTimeout(() => { delete setUI.notices[n]; noticeTimers.delete(n); if (data) render(); }, NOTICE_MS));
};
const closeNewProject = () => { ++npSeq; npUI.open = false; if (askUI.pending?.kind === 'start') clearPair(); };
const projAsks = (d = data) => (d?.p ? needsByProject(d.asks, d.projects).get(d.p) || [] : []);
const focusAskCard = (id) => {
  const card = [...$('asks').querySelectorAll('article.ask-card')].find((c) => c.dataset.askId === id);
  if (card) { card.scrollIntoView?.({ block: 'nearest' }); card.focus({ preventScroll: true }); }
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
    const sp = justStarted && (projects.find((x) => !justStarted.ids.has(x.id)) || projects.find((x) => x.folder === justStarted.folder));
    const p = sp ? sp.id : (projects.some((x) => x.id === want) ? want : (projects.length === 1 ? projects[0].id : ''));
    if (sp || !again) justStarted = null;
    if (sp) {
      clearTimeout(boardNoticeTimer);
      boardNotice = { project: sp.id, text: 'Started. The coordinator is planning the tasks.' };
      boardNoticeTimer = setTimeout(() => { boardNotice = null; if (data) render(); }, NOTICE_MS);
      if (location.hash !== '#' + enc(sp.id)) location.hash = '#' + enc(sp.id);
    }
    if (setUI.project !== p) {
      noticeTimers.forEach(clearTimeout); noticeTimers.clear();
      Object.assign(setUI, { project: p, rows: null, error: '', busy: '', edits: {}, errors: {}, fixes: {}, notices: {}, editor: null, copied: '' });
      ++paSeq; Object.assign(paUI, emptyPA());
      Object.assign(logUI, { task: '', observations: false, hasObservations: false, error: '' });
      askUI.error = ''; askUI.errorAskId = ''; if (askUI.pending && askUI.pending.kind !== 'start') clearPair();
    }
    const routeTask = !sp && !npUI.open && want === p ? (task || '') : '';
    let graph = null, log = [], problem = '';
    if (p) {
      setP = get(`/api/factory/projects/${enc(p)}/settings`).then((b) => ({ rows: b.rows || [] }), (e) => ({ error: e.message || 'error', http: e.http, needsPairing: e.needsPairing, pairMessage: e.pairMessage }));
      const fetchLog = (lt) => {
        const qs = `n=100${lt ? `&task=${enc(lt)}` : ''}${logUI.observations ? '&observations=1' : ''}`;
        return get(`/api/factory/projects/${enc(p)}/log?${qs}`).then((b) => ({ lines: b.lines || [], hasObs: Boolean(b.has_observations) }), (e) => ({ error: e.message || 'error', http: e.http, needsPairing: e.needsPairing, pairMessage: e.pairMessage }));
      };
      const prevKnown = !data?.graph || data.p !== p || data.graph.tasks?.some((x) => x.ref === routeTask);
      const initTask = (routeTask && prevKnown ? routeTask : '') || logUI.task;
      const logP = fetchLog(initTask);
      try {
        let lr;
        [graph, lr] = await Promise.all([get(`/api/factory/projects/${enc(p)}/graph`), logP]);
        const realTask = routeTask && graph?.tasks?.some((x) => x.ref === routeTask) ? routeTask : logUI.task;
        if (realTask !== initTask) lr = await fetchLog(realTask);
        if (lr.lines) { log = lr.lines; logUI.hasObservations = lr.hasObs; logUI.error = ''; }
        else { logUI.error = lr.error; if (lr.needsPairing) Object.assign(askUI, { needsPairing: true, pairMessage: lr.pairMessage || '' }); }
      } catch (e) { if (!e.http) throw e; if (e.needsPairing) Object.assign(askUI, { needsPairing: true, pairMessage: e.pairMessage || '' }); problem = e.message; }
    }
    next = { projects, asks, p, task: routeTask, graph, log, problem };
  } catch (e) {
    if (e.http) { if (e.needsPairing) Object.assign(askUI, { needsPairing: true, pairMessage: e.pairMessage || '' }); next = { projects: [], asks: [], p: '', task, graph: null, log: [], problem: e.message }; }
    else off = true;
  }
  const prev = data;
  data = next; down = off;
  const prevPr = !off && prev?.p === next?.p && prev?.projects.find((x) => x.id === next.p), nextPr = !off && next?.projects.find((x) => x.id === next.p);
  if (nextPr && !paUI.busy) {
    const st = projectState(nextPr), canStop = st === 'running' || (st === 'finished' && Boolean(nextPr.live));
    if (paUI.mode === 'stop' && !canStop) paUI.mode = '';
    if (prevPr && projectState(prevPr) !== st) { paUI.error = ''; paUI.checks = null; }
    if (askUI.pending?.kind === 'project' && ((askUI.pending.action === 'stop' && !canStop) || (askUI.pending.action === 'resume' && st !== 'stopped'))) clearPair();
  }
  if (setUI.editor && projAsks(next).some((a) => a.set?.name === setUI.editor.row)) setUI.editor = null;
  if (!off) lastOK = Date.now();
  render();
  if (setP && !off) {
    const sr = await setP;
    if (sr.rows) { setUI.rows = sr.rows; setUI.error = ''; }
    else if (sr.http || !setUI.rows) { setUI.error = sr.error; if (sr.needsPairing) Object.assign(askUI, { needsPairing: true, pairMessage: sr.pairMessage || '' }); }
    else down = true;
    if (prevPr && nextPr && projectState(prevPr) === 'running' && (nextPr.state ? nextPr.state === 'finished' : !nextPr.running)) {
      const nextPAsks = projAsks(next);
      for (const a of projAsks(prev)) if (a.set && !setUI.notices[a.set.name]
        && !nextPAsks.some((x) => x.id === a.id || x.set?.name === a.set.name)
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
const key = (e) => e.id ? `${e.tagName}#${e.id}` : e.dataset?.view ? `${e.tagName}:view:${e.dataset.view}` : e.dataset?.ref ? `${e.tagName}:ref:${e.dataset.ref}` : e.dataset?.ovProj ? `${e.tagName}:ov:${e.dataset.ovProj}` : e.dataset?.askId && !e.dataset?.option ? `${e.tagName}:ask:${e.dataset.askId}` : [e.tagName, e.dataset?.setting || e.dataset?.sugg || e.dataset?.pa || e.getAttribute('href') || e.dataset?.askId || e.closest?.('[data-row]')?.dataset.row || e.closest?.('[data-ask-id]')?.dataset.askId || '', e.dataset?.option || e.dataset?.copy || e.getAttribute('aria-label') || (e.dataset?.npCheck && 'np-check') || (e.dataset?.npStart && 'np-start') || e.className || e.parentElement?.className || e.textContent?.slice(0, 24) || ''].join(':');
function set(el, html) {
  if (el._html === html) return;
  const open = [...el.querySelectorAll('details[open]')].map(key);
  const active = el.contains(document.activeElement) ? document.activeElement : null;
  const focus = active && key(active), sel = active ? [active.selectionStart, active.selectionEnd] : null;
  el.innerHTML = html; el._html = html;
  for (const d of el.querySelectorAll('details')) d.open = open.includes(key(d));
  const hit = focus && [...el.querySelectorAll('a, button, summary, input, select, textarea, [tabindex]')].find((x) => key(x) === focus);
  if (hit) { hit.focus({ preventScroll: true }); if (sel && sel[0] != null && hit.setSelectionRange) { try { hit.setSelectionRange(sel[0], sel[1]); } catch {} } }
}

function syncNeeds(html) {
  const host = $('needs-you');
  if (host._html === html) return;
  const curBtn = host.querySelector('#ni-btn');
  if (!html || !curBtn || !host.appendChild) return set(host, html);
  const active = host.contains(document.activeElement) ? document.activeElement : null, focus = active && key(active);
  const tmp = document.createElement('div');
  tmp.innerHTML = html;
  const nextBtn = tmp.querySelector('#ni-btn'), nextList = tmp.querySelector('.ni-list');
  if (!nextBtn) return set(host, html);
  curBtn.setAttribute('aria-expanded', nextBtn.getAttribute('aria-expanded'));
  curBtn.setAttribute('aria-label', nextBtn.getAttribute('aria-label'));
  if (curBtn.textContent !== nextBtn.textContent) {
    while (curBtn.childNodes.length) curBtn.removeChild(curBtn.childNodes[0]);
    for (const n of [...nextBtn.childNodes]) curBtn.appendChild(n);
  }
  host.querySelector('.ni-list')?.remove();
  if (nextList) host.appendChild(nextList);
  host._html = html;
  const hit = focus && focus !== key(curBtn) && [...host.querySelectorAll('a, button')].find((x) => key(x) === focus);
  if (hit) hit.focus({ preventScroll: true });
  else if (active && !host.contains(active)) curBtn.focus({ preventScroll: true });
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
  const needs = needsByProject(data.asks, data.projects), hasNeeds = needs.size > 0;
  const wasInNeeds = $('needs-you').contains(document.activeElement);
  if (!hasNeeds) needsOpen = false;
  syncNeeds(renderNeedsYouHTML(data.projects, needs, needsOpen));
  const tasks = sortedTasks(), many = data.projects.length >= 2;
  const t = !npUI.open && data.task && tasks.find((x) => x.ref === data.task), pr = data.projects.find((x) => x.id === data.p);
  const overview = !npUI.open && many && !pr && !data.problem;
  const hideSide = !!t || (many && !pr);
  const graphOn = !npUI.open && !t && !overview && Boolean(pr) && !data.problem && tasks.length >= 2 && taskView === 'graph';
  $('board').className = graphOn ? 'cols graph-view' : 'cols';
  const narrow = !t && !npUI.open && !overview && !hideSide && Boolean(mq ? mq.matches : globalThis.innerWidth <= 760);
  if (curNarrow !== narrow && $('main').after) {
    if (narrow) { $('main').before($('asks')); $('projects').after($('settings')); }
    else { $('main').after($('settings')); $('projects').after($('asks')); }
    curNarrow = narrow;
  }
  $('aside').hidden = hideSide; $('asks').hidden = hideSide; $('tasks-top').hidden = !narrow;
  set($('tasks-top'), narrow ? boardHTML(tasks, 'top') : '');
  const prevBox = graphOn ? $('main').querySelector('.gbox') : null;
  const gScroll = prevBox ? prevBox.scrollLeft : 0, gBoxFocus = Boolean(prevBox && document.activeElement === prevBox);
  set($('main'), npUI.open ? renderNewProjectHTML(npUI.rows, npState()) : (t ? taskHTML(t) : (overview ? renderOverviewHTML(data.projects, needs) : boardHTML(tasks, narrow ? 'body' : ''))));
  if (graphOn) {
    syncGraphBox($('main'));
    const box = $('main').querySelector('.gbox');
    if (box && gScroll) box.scrollLeft = gScroll;
    if (box && gBoxFocus && box.getAttribute('tabindex') === '0') box.focus({ preventScroll: true });
    const a = document.activeElement?.closest?.('a.gnode');
    if (a && $('main').contains(a)) highlightGraph(a.closest('svg'), a.dataset.ref, true);
  }
  const pAsks = data.p ? (needs.get(data.p) || []) : [];
  $('settings').hidden = npUI.open || !!t || !pr || !!data.problem;
  $('settings').className = setUI.error ? 'card dashed' : 'card';
  if (!$('settings').hidden) set($('settings'), renderSettingsHTML(setUI.rows, pAsks, setState()));
  $('projects').hidden = many;
  set($('projects'), `<h2>projects</h2> ${data.projects.length ? data.projects.map(projHTML).join(' ') + ` <div class="ask-opts"><button type="button" class="ask-opt sm" data-new-project="1">${PLUS_ICO}New project</button></div>` : '<p class="muted">None yet.</p>'}`);
  set($('asks'), `<div class="head"><h2>asks for you</h2>${pAsks.length ? `<span>${pAsks.length} open</span>` : ''}</div>${renderAskCardsHTML(pAsks, askUI)}`);
  const runs = tasks.filter((x) => x.terminal);
  set($('agents'), `<div class="head"><h2>agents</h2><span>${runs.length} running</span></div>` + (runs.map((x) =>
    `<div class="agent"><span class="dot ${stage(x)[1]}" aria-hidden="true"></span><span><code>${esc(x.ref)}</code> ${esc(x.model || 'worker')} · ${esc(x.why_waiting || stage(x)[0])}</span></div>`).join('') || '<p class="muted">No workers running.</p>'));
  if (pendingAskFocus && data.p === pendingAskFocus.project && !npUI.open && !t && !$('asks').hidden) {
    const id = pendingAskFocus.askId; pendingAskFocus = null; focusAskCard(id);
  } else if (wasInNeeds && !hasNeeds) {
    ((!$('tasks-top').hidden && $('tasks-top').querySelector('a, button')) || $('main').querySelector('a, button, input, select, textarea, summary') || $('board').querySelector('a, button, input, select, textarea, summary'))?.focus({ preventScroll: true });
  }
}

const counts = (c = {}) => Object.entries(c).map(([k, n]) => `${n} ${(STAGE[k] || [k])[0]}`).join(' · ');
const projHTML = (pr) => `<a class="proj" href="#${enc(pr.id)}"${pr.id === data.p ? ' aria-current="true"' : ''}><span class="proj-title">${esc(goalTitle(pr.goal, pr.id))}</span> ` +
  `<span class="muted">${pr.error ? esc(pr.error) : esc(projectState(pr)) + (counts(pr.counts) ? ' · ' + esc(counts(pr.counts)) : '')}</span></a>`;

function boardHTML(tasks, part) {
  const many = data.projects.length >= 2, pr = data.projects.find((x) => x.id === data.p), done = tasks.filter((x) => x.stage === 'done');
  const showGraph = Boolean(pr) && !data.problem && tasks.length >= 2 && taskView === 'graph';
  const sw = pr && !data.problem && tasks.length >= 2 ? `<div class="seg" role="group" aria-label="show tasks as"><button type="button" class="${showGraph ? '' : 'on'}" data-view="list" aria-pressed="${!showGraph}">list</button><button type="button" class="${showGraph ? 'on' : ''}" data-view="graph" aria-pressed="${showGraph}">graph</button></div>` : '';
  const back = many && (pr || data.problem) ? '<a class="back" href="#">‹ all projects</a>' : '';
  let top = `${back}<div class="head"><h1>tasks</h1>${pr && !data.problem ? `<div class="head-r"><span>${done.length} of ${tasks.length} done</span>${sw}</div>` : ''}</div>`, body = '';
  if (data.problem) body = `<section class="card" role="status"><h2>can't show the factory</h2><p class="muted">${esc(data.problem)}</p></section>`;
  else if (!pr) body = EMPTY_CARD_HTML;
  else {
    const title = goalTitle(pr.goal, pr.id), rawGoal = (pr.goal || '').trim();
    const goalHead = many ? `<div class="goal-head"><span class="mono muted small">${esc(projectName(pr))}</span><p class="goal-title">${esc(title)}</p></div>` : `<p class="goal-title">${esc(title)}</p>`;
    top += goalHead + (rawGoal && rawGoal !== title ? `<details class="goal-full"><summary>full project goal</summary><div class="md-body">${renderMarkdown(pr.goal)}</div></details>` : '') + ' ' + renderProjectActionsHTML(pr, paState());
    if (boardNotice?.project === pr.id) body += `<p class="set-ok" role="status">${OK_ICO}<span>${esc(boardNotice.text)}</span></p>`;
    if (!tasks.length) body += `<section class="card" aria-label="No tasks"><h2>no tasks yet</h2><p class="muted">The coordinator has not planned this project yet.</p></section>`;
    else if (showGraph) body += renderGraphSection(tasks, pr.id);
    else {
      const other = [...new Set(tasks.map((x) => x.stage))].filter((s) => s !== 'done' && !ORDER.includes(s));
      for (const s of [...ORDER, ...other]) {
        const rows = tasks.filter((x) => x.stage === s), [rawLabel, , alert] = stage(rows[0] || { stage: s }), label = esc(rawLabel);
        if (rows.length) body += `<section class="group" aria-label="${label}"><h2${alert ? ' class="alert"' : ''}>${label} <span class="count">${rows.length}</span></h2><div class="rows${alert ? ' alert' : ''}">${rows.map(rowHTML).join('')}</div></section>`;
      }
      if (done.length) body += `<details class="done"><summary>${done.length} done · show them</summary><div class="rows">${done.map(rowHTML).join('')}</div></details>`;
    }
    body += renderLogHTML(data.log, { tasks: tasks.map((x) => x.ref), task: logUI.task, observations: logUI.observations, hasObservations: logUI.hasObservations, error: logUI.error }, pr.repo_url);
  }
  return part === 'top' ? top : part === 'body' ? body : top + body;
}

function rowHTML(t) {
  const [label, dot] = stage(t), why = t.why_waiting || label;
  const model = t.model || (t.stage === 'queued' || t.stage === 'waiting_on_dep' ? 'not started' : '');
  const sum = Array.isArray(t.steps) && t.steps.length ? stepSummary(t.steps) : null;
  const say = [t.ref, t.title, label, why !== label && why, sum?.aria, model, `layer ${t.layer}`].filter(Boolean).join(', ');
  return `<a class="row" href="#${enc(data.p)}/${enc(t.ref)}" aria-label="${esc(say)}"><span class="dot ${dot}" aria-hidden="true"></span>` +
    `<span class="what"><span><code>${esc(t.ref)}</code> <b>${esc(t.title)}</b></span><span class="why">${esc(why)}</span>${stepsHTML(t, false, sum)}</span>` +
    `<span class="chips">${model ? `<span class="chip">${esc(model)}</span>` : ''}<span>layer ${t.layer}</span></span></a>`;
}

const stepIco = (d) => `<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${d}"></path></svg>`;
const STEP_ST = {
  done: ['done', 'done', stepIco('M5 12l5 5 9-10')],
  now: ['now', 'now', '<span class="sm-dot"></span>'],
  todo: ['', 'to come', ''],
  skipped: ['skip', 'skipped', stepIco('M5 12h14')],
  ended: ['end', '', stepIco('M6 6l12 12M18 6L6 18')],
};
function stepSummary(steps) {
  const idx = steps.findIndex((s) => s.state === 'now' || s.state === 'ended');
  const i = idx >= 0 ? idx : steps.length - 1, cur = steps[i], sk = steps.find((s) => s.state === 'skipped');
  const words = cur.state === 'done' ? cur.name
    : cur.state === 'ended' ? (cur.text ? `${cur.name}, ${cur.text}` : cur.name)
    : sk ? `${cur.name}, ${sk.name} ${sk.text || 'skipped'}`
    : cur.name && cur.text?.startsWith(cur.name) ? cur.text
    : cur.text?.startsWith('sent back') ? `${cur.name}, ${cur.text.split(';')[0]}`
    : cur.name;
  return { words, aria: `step ${i + 1} of ${steps.length}, ${words}` };
}
function stepsHTML(t, full = false, sum = null) {
  const steps = Array.isArray(t.steps) ? t.steps : [];
  if (!steps.length) return '';
  if (!full) {
    const { words, aria } = sum || stepSummary(steps);
    const segs = steps.map((s) => { const c = Object.hasOwn(STEP_ST, s.state) ? STEP_ST[s.state][0] : ''; return `<span${c ? ` class="${c}"` : ''}></span>`; }).join('');
    return `<span class="sbar"><span class="bar" role="img" aria-label="${esc(aria)}">${segs}</span><span class="small muted">${esc(words)}</span></span>`;
  }
  const items = steps.map((s) => {
    const [cls, word, ico] = Object.hasOwn(STEP_ST, s.state) ? STEP_ST[s.state] : ['', s.state || 'to come', ''];
    const cur = s.state === 'now' || s.state === 'ended', todo = s.state === 'todo';
    return `<li class="step"><span class="sm-mark ${cls || 'todo'}" aria-hidden="true">${ico}</span>` +
      `<div class="step-body"><div class="step-top"><span class="${cur ? 'step-cur' : (todo ? 'step-name muted' : 'step-name')}">${esc(s.name)}</span>` +
      `${word ? ` <span class="small ${s.state === 'now' ? 'step-now' : 'muted'}">${esc(word)}</span>` : ''}</div>` +
      `${s.text ? ` <span class="small${cur ? '' : ' muted'}">${esc(s.text)}</span>` : ''}</div></li>`;
  }).join('');
  return `<section class="card" aria-label="steps"><h2>steps</h2><ol class="steps">${items}</ol></section>`;
}

function taskHTML(t) {
  const [label, dot] = stage(t), kv = (k, v) => v ? `<div class="kv"><span>${k}</span><span>${esc(v)}</span></div>` : '';
  const det = [t.last_rule && `rule ${t.last_rule}`, t.terminal && `run ${t.terminal}`, t.layer != null && `layer ${t.layer}`].filter(Boolean).join(' · ');
  const verdictSub = t.rounds ? `review ${t.rounds} round${t.rounds > 1 ? 's' : ''} done` : (!t.last_rule ? 'No step logged for this task yet.' : '');
  const repoURL = data.projects.find((x) => x.id === data.p)?.repo_url;
  return `<a class="back" href="#${enc(data.p)}">‹ board</a><div class="task-head"><div class="meta"><code>${esc(t.ref)}</code>` +
    `<span class="pill ${dot}"><span class="dot ${dot}" aria-hidden="true"></span>${esc(label)} · ${esc(t.why_waiting || label)}</span>` +
    `${t.terminal ? `<span>run ${esc(t.terminal)}</span>` : ''}</div><h1>${esc(t.title)}</h1></div><div class="cols"><div class="col-main">` +
    `${stepsHTML(t, true)}` +
    `${t.brief && t.brief.trim() ? `<section class="card" aria-label="Brief"><h2>brief</h2><div class="md-body">${renderMarkdown(t.brief)}</div></section>` : ''}` +
    `<section class="card" aria-label="Verdict"><h2>the ladder says: ${esc(t.last_decision || 'nothing yet')}</h2>${verdictSub ? `<span class="small muted">${esc(verdictSub)}</span>` : ''}` +
    `${det ? `<details class="task-details"><summary>details</summary><p class="small muted">${esc(det)}</p></details>` : ''}</section>` +
    renderLogHTML(data.log.filter((l) => l.task === t.ref), { forTask: true, heading: 'log for this task', empty: 'Nothing logged for this task yet.', observations: logUI.observations, hasObservations: logUI.hasObservations, error: logUI.error }, repoURL) + `</div><aside><section class="card" aria-label="Who worked on it"><h2>agents</h2>` +
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
      const was = isMod && ask.set.was ? parseModelCandidates(ask.set.was).map((c) => `${c.cli}/${c.model}/${c.effort}`).join(' then ') : (ask.set.was || 'not set');
      if (setUI.editor?.row === n) setUI.editor = null;
      delete setUI.edits[n]; delete setUI.errors[n]; delete setUI.fixes[n];
      setNotice(n, option === 'Save' ? (isMod ? 'Saved. The next agent started for this role uses it. Running agents keep their model.' : 'Saved. The engine uses it from now on.') : `Kept at ${was}. Nothing changed.`);
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

async function openNewProject() {
  const seq = ++npSeq; justStarted = null; Object.assign(npUI, emptyNP(), { open: true }); if (askUI.pending?.kind === 'start' || askUI.pending?.kind === 'project') clearPair(); render();
  const loaded = await loadNewProject(get); if (seq === npSeq && npUI.open) { Object.assign(npUI, loaded); render(); }
}
async function runProjectAction(action, goalOverride) {
  const pr = data?.projects.find((x) => x.id === data.p);
  if (!pr) return;
  const st = projectState(pr), goal = action === 'resume' ? (pr.goal ?? '') : (goalOverride !== undefined ? goalOverride : paUI.goal.trim());
  if (action === 'newgoal' && !goal) { Object.assign(paUI, { error: 'Write a goal first.', checks: null }); render(); return $('pa-goal')?.focus({ preventScroll: true }); }
  const seq = ++paSeq; Object.assign(paUI, action === 'resume' ? { mode: '', goal: '' } : {}, { busy: action, error: '', checks: null }); if (!askUI.pairDone) clearPair(); render();
  const res = await (action === 'stop' ? stopProject(askFlow, pr.id) : startProject(askFlow, pr.folder, goal)).catch((e) => ({ ok: false, message: e.message || 'Network error.' }));
  if (seq !== paSeq) return;
  paUI.busy = '';
  if (res.ok) { clearPair(); Object.assign(paUI, emptyPA()); await poll(); return paBar()?.querySelector('button')?.focus({ preventScroll: true }); }
  if (res.needsPairing) {
    if (action === 'stop') paUI.mode = '';
    Object.assign(askUI, { needsPairing: false, pairDone: false, pairMessage: res.message, pending: { kind: 'project', action, project: pr.id, goal: action === 'newgoal' ? goal : undefined } });
    render(); return paBar()?.querySelector('input[name="code"]')?.focus({ preventScroll: true });
  }
  clearPair();
  if (action === 'stop') paUI.mode = '';
  if (Array.isArray(res.results)) Object.assign(paUI, { error: `Not started: a start check failed. Fix it, then press ${action === 'resume' ? 'Resume' : (st === 'finished' ? 'Reopen' : 'New goal')} again.`, checks: res.results });
  else paUI.error = res.message || (action === 'stop' ? 'Could not stop project.' : 'Could not start project.');
  render();
  (action === 'newgoal' ? (paUI.checks ? paBar()?.querySelector('button[data-pa="ng-go"]') || $('pa-goal') : $('pa-goal')) : paBar()?.querySelector(`button[data-pa="${action}"]`) || paBar()?.querySelector('button'))?.focus({ preventScroll: true });
}
async function runCheck() {
  const seq = npSeq; Object.assign(npUI, { checking: true, checkError: '', startError: '', errors: {}, fixes: {} }); if (askUI.pending?.kind === 'start') clearPair(); render();
  const res = await checkProject(npUI.folder.trim(), npSet(), undefined, askFlow.reauth); if (seq !== npSeq || !npUI.open) return;
  npUI.checking = false;
  if (res.ok) Object.assign(npUI, { checks: res.results, hasChecked: true, notGit: false });
  else if (res.name) { npUI.checks = null; npUI.errors[res.name] = res.message; if (res.fix) npUI.fixes[res.name] = res.fix; }
  else Object.assign(npUI, { checks: null, hasChecked: true, checkError: res.message });
  render();
  if (res.name) focusRow(res.name, $('main'));
  else if (!$('main').contains(document.activeElement)) $('main').querySelector('button[data-np-check]')?.focus({ preventScroll: true });
}
async function runStart() {
  const folder = npUI.folder.trim(), goal = npUI.goal.trim(), set = npSet();
  if (!goal) { if (askUI.pending?.kind === 'start') clearPair(); npUI.goalErr = true; render(); return $('np-goal')?.focus({ preventScroll: true }); }
  const seq = npSeq, ids = new Set((data?.projects || []).map((x) => x.id)); Object.assign(npUI, { starting: true, goalErr: false, startError: '', errors: {}, fixes: {} }); if (!askUI.pairDone) clearPair(); render();
  const res = await startProject(askFlow, folder, goal, set).catch((e) => ({ ok: false, message: e.message || 'Network error.' }));
  if (seq !== npSeq) return;
  npUI.starting = false;
  if (res.ok) { clearPair(); npUI.open = false; justStarted = { folder, ids }; return poll(); }
  if (res.needsPairing) { Object.assign(askUI, { needsPairing: false, pairDone: false, pairMessage: res.message, pending: { kind: 'start' } }); render(); return $('main').querySelector('input[name="code"]')?.focus({ preventScroll: true }); }
  clearPair();
  if (res.name) { npUI.errors[res.name] = res.message; if (res.fix) npUI.fixes[res.name] = res.fix; }
  else if (Array.isArray(res.results)) Object.assign(npUI, { checks: res.results, hasChecked: true });
  else npUI.startError = res.message || 'Could not start project.';
  render();
  if (res.name) focusRow(res.name, $('main'));
  else $('main').querySelector('button[data-np-start]')?.focus({ preventScroll: true });
}

const syncEdit = (e) => {
  if (e.target.dataset?.logTask) { if (logUI.task !== e.target.value) { logUI.task = e.target.value; return poll(); } return; }
  if (e.target.dataset?.logObs) { if (logUI.observations !== Boolean(e.target.checked)) { logUI.observations = Boolean(e.target.checked); return poll(); } return; }
  if (e.target.id === 'pa-goal') {
    const dirty = Boolean(paUI.error || paUI.checks || askUI.pending?.kind === 'project');
    Object.assign(paUI, { goal: e.target.value, error: '', checks: null });
    if (askUI.pending?.kind === 'project') clearPair();
    if (dirty) render(); else paRoot()._html = boardHTML(sortedTasks(), curNarrow ? 'top' : '');
    return;
  }
  const inForm = Boolean(e.target.closest?.('[data-np-form]')), ctrl = e.target.closest?.('[data-setting]');
  if (inForm && (e.target.id === 'np-folder' || e.target.id === 'np-goal')) {
    const isF = e.target.id === 'np-folder', dirtyErr = isF ? (npUI.notGit || npUI.checks || npUI.checkError || npUI.startError || askUI.pending?.kind === 'start') : (npUI.goalErr || npUI.startError);
    if (isF) { Object.assign(npUI, { folder: e.target.value, notGit: false, checks: null, checkError: '', startError: '' }); if (askUI.pending?.kind === 'start') clearPair(); }
    else Object.assign(npUI, { goal: e.target.value, goalErr: false, startError: '' });
    if (dirtyErr) render(); else $('main')._html = renderNewProjectHTML(npUI.rows, npState());
  } else if (ctrl && data) {
    const ui = inForm ? npUI : setUI, n = ctrl.dataset.setting, row = (ui.rows || []).find((r) => r.name === n);
    if (row && ctrl.value === row.value) delete ui.edits[n]; else ui.edits[n] = ctrl.value;
    delete ui.errors[n]; delete ui.fixes[n];
    if (!inForm) { clearTimeout(noticeTimers.get(n)); noticeTimers.delete(n); delete setUI.notices[n]; }
    const root = inForm ? $('main') : $('settings'), html = inForm ? renderNewProjectHTML(npUI.rows, npState()) : renderSettingsHTML(setUI.rows, projAsks(), setState()), tmp = document.createElement('div');
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
      const act = inForm && root.querySelector('.np-actions'), nextAct = act && tmp.querySelector('.np-actions');
      if (act && nextAct && act.innerHTML !== nextAct.innerHTML) act.innerHTML = nextAct.innerHTML;
      root._html = html;
    } else render();
  } else if (e.target.dataset?.candField && (inForm ? npUI.editor : setUI.editor) && data) {
    const ui = inForm ? npUI : setUI, c = ui.editor.cands[+e.target.dataset.candIdx], f = e.target.dataset.candField, n = ui.editor.row;
    if (!c) return;
    c[f] = e.target.value; delete ui.errors[n]; delete ui.fixes[n];
    if (f !== 'model') return render();
    const srow = e.target.closest('[data-row]'), li = e.target.closest('li.cand'), al = srow?.querySelector('.cand-alert'), tag = `${c.cli} ${c.model.trim() || 'model'}`;
    srow?.querySelectorAll('.set-in.bad').forEach((x) => x.classList.remove('bad')); li?.querySelector('[data-cand-up]')?.setAttribute('aria-label', `Move ${tag} up`); li?.querySelector('[data-cand-rm]')?.setAttribute('aria-label', `Remove ${tag}`);
    if (al) al.innerHTML = '';
    if (inForm) $('main')._html = renderNewProjectHTML(npUI.rows, npState());
    else $('settings')._html = renderSettingsHTML(setUI.rows, projAsks(), setState());
  } else if (e.target.name === 'code') askUI.pairCode = e.target.value;
};
$('board').addEventListener('input', syncEdit);
$('board').addEventListener('change', syncEdit);
$('board').addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && paUI.mode === 'stop' && !paUI.busy && e.target.closest?.('.pa-confirm')) {
    e.preventDefault(); paUI.mode = ''; render(); return paBar()?.querySelector('button[data-pa="stop"]')?.focus({ preventScroll: true });
  }
  const ctrl = e.key === 'Enter' && !e.target.closest?.('[data-np-form]') && e.target.closest?.('input[data-setting]'), n = ctrl ? ctrl.dataset.setting : undefined;
  if (ctrl && setUI.edits[n] !== undefined) {
    e.preventDefault();
    if (!setUI.busy && !projAsks().some((a) => a.set?.name === n) && (!ctrl.reportValidity || ctrl.reportValidity())) submitSetting(data.p, n, ctrl.value);
  }
});
const openModelCands = (r, raw) => parseModelCandidates(raw, r.clis[0]).map((c) => ({ ...c, cli: r.clis.includes(c.cli) ? c.cli : r.clis[0] }));
$('board').addEventListener('click', (e) => {
  if (e.target.closest('button[data-log-show]')) { logUI.observations = true; $('log-obs')?.focus({ preventScroll: true }); return poll(); }
  const vw = e.target.closest('button[data-view]');
  if (vw) { taskView = vw.dataset.view === 'graph' ? 'graph' : 'list'; try { globalThis.sessionStorage?.setItem('factory-view', taskView); } catch {} return render(); }
  if (e.target.closest('button[data-new-project]')) return openNewProject();
  if (e.target.closest('a[data-np-back]')) { e.preventDefault(); closeNewProject(); if (data?.projects.length >= 2 && data.p) location.hash = ''; else render(); return; }
  if (npUI.open && e.target.closest('a.proj')) { closeNewProject(); return render(); }
  const pa = e.target.closest('button[data-pa]');
  if (pa && !paUI.busy) {
    const a = pa.dataset.pa;
    if (a === 'stop' || a === 'newgoal') {
      if (a === 'newgoal' && paUI.mode === 'newgoal') return paUI.checks && paUI.goal.trim() ? runProjectAction('newgoal') : $('pa-goal')?.focus({ preventScroll: true });
      const pr = data?.projects.find((x) => x.id === data.p);
      const goal = a === 'newgoal' && projectState(pr) === 'running' && pr?.goal_undelivered ? (pr.goal || '') : '';
      Object.assign(paUI, emptyPA(), { mode: a, goal }); if (askUI.pending?.kind === 'project') clearPair(); render();
      return (a === 'stop' ? paBar()?.querySelector('button[data-pa="stop-no"]') : $('pa-goal'))?.focus({ preventScroll: true });
    }
    if (a === 'stop-no' || a === 'ng-cancel') {
      Object.assign(paUI, emptyPA()); if (askUI.pending?.kind === 'project') clearPair(); render();
      return paBar()?.querySelector(`button[data-pa="${a === 'stop-no' ? 'stop' : 'newgoal'}"]`)?.focus({ preventScroll: true });
    }
    if (a === 'stop-yes' || a === 'resume' || a === 'ng-go') return runProjectAction(a === 'stop-yes' ? 'stop' : (a === 'resume' ? 'resume' : 'newgoal'));
  }
  const sugg = e.target.closest('button[data-sugg]');
  if (sugg) { Object.assign(npUI, { folder: sugg.dataset.sugg, notGit: sugg.dataset.suggGit !== '1', checks: null, checkError: '', startError: '' }); if (askUI.pending?.kind === 'start') clearPair(); return render(); }
  if (e.target.closest('button[data-np-check]') && !npUI.checking && !npUI.starting) return runCheck();
  const stBtn = e.target.closest('button[data-np-start]');
  if (stBtn && stBtn.getAttribute('aria-disabled') !== 'true' && !npUI.starting && !npUI.checking) return runStart();
  const opt = e.target.closest('button[data-ask-id][data-option]');
  if (opt && !askUI.busyId) return submitAnswer(opt.dataset.askId, opt.dataset.option, opt.closest('[data-row]')?.dataset.row || '');
  const inForm = Boolean(e.target.closest?.('[data-np-form]')), ui = inForm ? npUI : setUI, root = inForm ? $('main') : $('settings'), locked = inForm ? (npUI.checking || npUI.starting) : Boolean(setUI.busy);
  const ed = e.target.closest('button[data-edit]');
  if (ed && !locked) { const n = ed.dataset.edit, r = (ui.rows || []).find((x) => x.name === n); if (r) { if (inForm ? askUI.pending?.kind === 'start' : pairRow() === ui.editor?.row) clearPair(); delete ui.errors[n]; delete ui.fixes[n]; ui.editor = { row: n, cands: openModelCands(r, inForm && ui.edits[n] !== undefined ? ui.edits[n] : (r.value || r.default)) }; render(); findRow(n, root)?.querySelector('select, input')?.focus({ preventScroll: true }); } return; }
  if (ui.editor && !locked) {
    const n = ui.editor.row, r = (ui.rows || []).find((x) => x.name === n), cs = ui.editor.cands, clr = () => { delete ui.errors[n]; delete ui.fixes[n]; };
    if (e.target.closest('button[data-cand-add]') && r) { cs.push({ cli: r.clis[0], model: '', effort: 'medium' }); clr(); render(); return findRow(n, root)?.querySelectorAll('input[data-cand-field="model"]')[cs.length - 1]?.focus({ preventScroll: true }); }
    if (e.target.closest('button[data-cand-default]') && r) { ui.editor.cands = r.default ? openModelCands(r, r.default) : []; clr(); render(); return findRow(n, root)?.querySelector('select, input, button[data-cand-save]')?.focus({ preventScroll: true }); }
    const up = e.target.closest('button[data-cand-up]'), rm = e.target.closest('button[data-cand-rm]');
    if (up || (rm && cs.length > 1)) { if (up) { const i = +up.dataset.candUp; if (i > 0) [cs[i - 1], cs[i]] = [cs[i], cs[i - 1]]; } else cs.splice(+rm.dataset.candRm, 1); clr(); render(); if (!root.contains(document.activeElement)) findRow(n, root)?.querySelector('select, input')?.focus({ preventScroll: true }); return; }
    if (e.target.closest('button[data-cand-cancel]')) { if (!inForm && pairRow() === n) clearPair(); ui.editor = null; clr(); render(); return findRow(n, root)?.querySelector('button[data-edit]')?.focus({ preventScroll: true }); }
    if (e.target.closest('button[data-cand-save]')) {
      const emptyIdx = cs.findIndex((c) => !c.model.trim()), commaIdx = cs.findIndex((c) => c.model.includes(',')), badIdx = emptyIdx >= 0 ? emptyIdx : commaIdx;
      if (badIdx < 0) {
        const val = cs.map((c) => `${c.cli}/${c.model.trim()}/${c.effort}`).join(', ');
        if (!inForm) return submitSetting(data.p, n, val);
        const prev = npUI.edits[n] !== undefined ? npUI.edits[n] : (r?.value ?? '');
        if (r && val === r.value) delete npUI.edits[n]; else npUI.edits[n] = val;
        if (val !== prev) { npUI.checks = null; npUI.checkError = ''; if (askUI.pending?.kind === 'start') clearPair(); }
        npUI.editor = null; clr(); render(); return findRow(n, root)?.querySelector('button[data-edit]')?.focus({ preventScroll: true });
      }
      ui.errors[n] = emptyIdx >= 0 ? 'Type a model name.' : `${r?.label || n}: a model name cannot hold a comma`;
      delete ui.fixes[n]; render();
      return findRow(n, root)?.querySelectorAll('input[data-cand-field="model"]')[badIdx]?.focus({ preventScroll: true });
    }
  }
  const cp = e.target.closest('button[data-copy]');
  if (cp) return navigator.clipboard?.writeText?.(cp.dataset.copy)?.then(() => { setUI.copied = cp.dataset.copy; render(); setTimeout(() => { if (setUI.copied === cp.dataset.copy) { setUI.copied = ''; if (data) render(); } }, 2000); }, () => {});
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
  if (!res.ok) { askUI.pairError = res.message || 'Pairing failed.'; render(); return (askUI.pending?.kind === 'start' ? $('main') : askUI.pending?.kind === 'project' ? paBar() : pairRow() ? $('settings') : $('asks'))?.querySelector('input[name="code"]')?.focus({ preventScroll: true }); }
  const pending = askUI.pending;
  Object.assign(askUI, { pairDone: Boolean(pending), pairError: '', pairCode: '' });
  if (pending?.kind === 'start') await runStart();
  else if (pending?.kind === 'project') await runProjectAction(pending.action, pending.goal);
  else if (pending?.kind === 'setting') await submitSetting(pending.project, pending.name, pending.value);
  else if (pending?.askId) await submitAnswer(pending.askId, pending.option, pending.row || '');
  else { clearPair(); render(); await poll(); }
});
const gRef = (el) => el?.closest?.('a.gnode')?.dataset.ref || '';
const onGraphHi = (e) => { const a = e.target.closest?.('a.gnode'); if (a) highlightGraph(a.closest('svg'), a.dataset.ref, e.type === 'focusin'); };
const onGraphLo = (e) => { const a = e.target.closest?.('a.gnode'), svg = a?.closest('svg'); if (a && !(e.relatedTarget && a.contains(e.relatedTarget))) highlightGraph(svg, e.type === 'mouseout' && svg?.contains(document.activeElement) ? gRef(document.activeElement) : '', true); };
for (const ev of ['focusin', 'mouseover']) $('board').addEventListener(ev, onGraphHi);
for (const ev of ['focusout', 'mouseout']) $('board').addEventListener(ev, onGraphLo);
if (mq?.addEventListener) mq.addEventListener('change', () => data && render());
addEventListener('resize', () => { if (!data) return; if (!mq?.addEventListener) render(); else syncGraphBox($('main')); });
if (globalThis.ResizeObserver) new ResizeObserver(() => data && syncGraphBox($('main'))).observe($('main'));
$('needs-you').addEventListener('click', (e) => {
  if (e.target.closest('button[data-ni-toggle]')) { needsOpen = !needsOpen; return render(); }
  const link = e.target.closest('a[data-ask-id]');
  if (!link) return;
  const askId = link.dataset.askId, proj = link.dataset.niProj;
  const onBoard = data?.p === proj && !npUI.open && !(data.task && sortedTasks().some((x) => x.ref === data.task));
  needsOpen = false;
  if (onBoard) { e.preventDefault(); render(); return focusAskCard(askId); }
  pendingAskFocus = { project: proj, askId };
  if (npUI.open && location.hash === '#' + enc(proj)) { e.preventDefault(); render(); closeNewProject(); scrollTo(0, 0); return poll(); }
  render();
});
$('needs-you').addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && needsOpen) { e.preventDefault(); needsOpen = false; render(); $('ni-btn')?.focus({ preventScroll: true }); }
});
$('retry').addEventListener('click', poll);
addEventListener('hashchange', () => {
  if (npUI.open) closeNewProject();
  if (pendingAskFocus && route()[0] !== pendingAskFocus.project) pendingAskFocus = null;
  scrollTo(0, 0); poll();
});
poll();
