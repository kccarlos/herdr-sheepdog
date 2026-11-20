// The factory page (design.md 12.1, the FactoryWeb* boards): read-only, polls the
// W0 views. Nothing here writes; the ask and override cards come with slice 6.
const POLL_MS = 3000, GIVE_UP_MS = 8000;
const $ = (id) => document.getElementById(id);
const enc = encodeURIComponent;
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
const clock = (t, sec) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: sec ? '2-digit' : undefined });
// stage (design.md 4.11) -> board group label, dot colour; ORDER is the boards' group order.
const STAGE = { blocked_or_failed: ['blocked', 'blocked'], ci: ['waiting on CI', 'working'], working: ['working', 'working'],
  waiting_on_dep: ['waiting on others', 'idle'], queued: ['queued', 'idle'], done: ['done', 'done'] };
const ORDER = ['blocked_or_failed', 'ci', 'working', 'waiting_on_dep', 'queued'];
const stage = (t) => STAGE[t.stage] || STAGE.blocked_or_failed;
let data = null, down = false, lastOK = 0, timer = 0;
const started = Date.now();

async function get(url) {
  const r = await fetch(url, { cache: 'no-store', signal: AbortSignal.timeout(2 * POLL_MS) });
  const body = await r.json().catch(() => ({}));
  if (!r.ok) throw Object.assign(new Error(body.error || `HTTP ${r.status}`), { http: true });
  return body;
}

const route = () => location.hash.slice(1).split('/').map(decodeURIComponent);

async function poll() {
  clearTimeout(timer);
  const [want, task] = route();
  try {
    const { projects = [] } = await get('/api/factory/projects');
    const p = projects.some((x) => x.id === want) ? want : (projects[0]?.id || '');
    let graph = null, log = [], problem = '';
    if (p) {
      try {
        [graph, { lines: log }] = await Promise.all([get(`/api/factory/projects/${enc(p)}/graph`), get(`/api/factory/projects/${enc(p)}/log?n=100`)]);
      } catch (e) { if (!e.http) throw e; problem = e.message; }
    }
    data = { projects, p, task, graph, log: log || [], problem };
    down = false; lastOK = Date.now();
  } catch (e) {
    if (e.http) data = { projects: [], p: '', task, graph: null, log: [], problem: e.message };
    else down = true;
  }
  render();
  timer = setTimeout(poll, POLL_MS);
}

// set replaces an element's HTML only when it changed, so polling keeps focus and scroll.
function set(el, html) { if (el._html !== html) { el.innerHTML = html; el._html = html; } }

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
  const t = data.task && tasks.find((x) => x.ref === data.task);
  $('aside').hidden = !!t;
  set($('main'), t ? taskHTML(t) : boardHTML(tasks));
  set($('projects'), `<h2>projects</h2>${data.projects.map(projHTML).join('') || '<p class="muted">None yet.</p>'}`);
  const runs = tasks.filter((x) => x.terminal);
  set($('agents'), `<div class="head"><h2>agents</h2><span>${runs.length} running</span></div>` + (runs.map((x) =>
    `<div class="agent"><span class="dot ${stage(x)[1]}" aria-hidden="true"></span><span><code>${esc(x.ref)}</code> ${esc(x.model || 'worker')} · ${esc(x.why_waiting || stage(x)[0])}</span></div>`).join('') || '<p class="muted">No workers running.</p>'));
}

const counts = (c = {}) => Object.entries(c).map(([k, n]) => `${n} ${(STAGE[k] || [k])[0]}`).join(' · ');
const projHTML = (pr) => `<a class="proj" href="#${enc(pr.id)}"${pr.id === data.p ? ' aria-current="true"' : ''}><b>${esc(pr.goal || pr.id)}</b>` +
  `<span class="muted">${pr.error ? esc(pr.error) : (pr.running ? 'running' : 'stopped') + (counts(pr.counts) ? ' · ' + esc(counts(pr.counts)) : '')}</span></a>`;

function boardHTML(tasks) {
  const pr = data.projects.find((x) => x.id === data.p);
  const done = tasks.filter((x) => x.stage === 'done');
  let h = `<div class="head"><h1>tasks</h1>${pr ? `<span>${esc(pr.goal || pr.id)} · ${done.length} of ${tasks.length} done</span>` : ''}</div>`;
  if (data.problem) return h + `<section class="card" role="status"><h2>can't show the factory</h2><p class="muted">${esc(data.problem)}</p></section>`;
  if (!pr) return h + `<section class="card" aria-label="No tasks"><h2>nothing running</h2><p class="muted">No factory projects on this computer. Start one in a terminal with <code>sheepdog factory start &lt;folder&gt; --goal "…"</code>; it shows here within seconds.</p></section>`;
  if (!tasks.length) h += `<section class="card" aria-label="No tasks"><h2>no tasks yet</h2><p class="muted">The coordinator has not planned this project yet.</p></section>`;
  for (const s of ORDER) {
    const rows = tasks.filter((x) => x.stage === s);
    if (rows.length) h += `<section class="group" aria-label="${STAGE[s][0]}"><h2${s === 'blocked_or_failed' ? ' class="alert"' : ''}>${STAGE[s][0]} <span class="count">${rows.length}</span></h2><div class="rows${s === 'blocked_or_failed' ? ' alert' : ''}">${rows.map(rowHTML).join('')}</div></section>`;
  }
  if (done.length) h += `<details class="done"><summary>${done.length} done · show them</summary><div class="rows">${done.map(rowHTML).join('')}</div></details>`;
  return h + logHTML(data.log, 'log');
}

function rowHTML(t) {
  const [label, dot] = stage(t), why = t.why_waiting || label;
  const model = t.model || (t.stage === 'queued' || t.stage === 'waiting_on_dep' ? 'not started' : '');
  const rounds = t.rounds ? `round ${t.rounds}` : '';
  const say = [t.ref, t.title, label, why !== label && why, rounds, model, `layer ${t.layer}`].filter(Boolean).join(', ');
  return `<a class="row" href="#${enc(data.p)}/${enc(t.ref)}" aria-label="${esc(say)}"><span class="dot ${dot}" aria-hidden="true"></span>` +
    `<span class="what"><span><code>${esc(t.ref)}</code> <b>${esc(t.title)}</b></span><span class="why">${esc(why)}</span></span>` +
    `<span class="chips">${[model, rounds].filter(Boolean).map((c) => `<span class="chip">${esc(c)}</span>`).join('')}<span>layer ${t.layer}</span></span></a>`;
}

const logHTML = (lines, title) => `<section class="card" aria-label="${title}"><h2>${title}</h2>` + (lines.length
  ? `<ol class="log" reversed>${lines.slice().reverse().map((l) => `<li><time datetime="${esc(l.at)}">${clock(l.at, true)}</time> ${esc(l.by)}` +
    `${l.task ? ` <code>${esc(l.task)}</code>` : ''} <b>${esc(l.decision)}</b> <span class="muted">${esc(l.why)}</span></li>`).join('')}</ol>`
  : '<p class="muted">Nothing logged yet.</p>') + '</section>';

function taskHTML(t) {
  const [label, dot] = stage(t), kv = (k, v) => v ? `<div class="kv"><span>${k}</span><span>${esc(v)}</span></div>` : '';
  return `<a class="back" href="#${enc(data.p)}">‹ board</a><div class="task-head"><div class="meta"><code>${esc(t.ref)}</code>` +
    `<span class="pill ${dot}"><span class="dot ${dot}" aria-hidden="true"></span>${esc(label)} · ${esc(t.why_waiting || label)}</span>` +
    `${t.terminal ? `<span>run ${esc(t.terminal)}</span>` : ''}</div><h1>${esc(t.title)}</h1></div><div class="cols"><div class="col-main">` +
    `<section class="card" aria-label="Verdict"><h2>the ladder says: ${esc(t.last_decision || 'nothing yet')}</h2><span class="muted">` +
    `${t.last_rule ? `rule ${esc(t.last_rule)}` : 'No step logged for this task yet.'}${t.rounds ? ` · ${t.rounds} review round${t.rounds > 1 ? 's' : ''}` : ''}</span></section>` +
    logHTML(data.log.filter((l) => l.task === t.ref), 'recent log') + `</div><aside><section class="card" aria-label="Who worked on it"><h2>agents</h2>` +
    kv('model', t.model || 'not started') + kv('waits on', t.why_waiting) + kv('after', t.after.join(', ')) + kv('unblocks', t.dependents.join(', ')) +
    kv('layer', String(t.layer)) + '</section></aside></div>';
}

$('retry').addEventListener('click', poll);
addEventListener('hashchange', () => { scrollTo(0, 0); poll(); });
poll();
