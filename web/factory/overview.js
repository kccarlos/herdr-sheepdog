// Project overview and header needs-you indicator for /factory (plan.md PR 14.10, FactoryWebOverview boards).
import { esc, isOwnerAsk, ERR_ICO } from './asks.js';
import { goalTitle } from './goal.js';
import { projectState, PLUS_ICO, ST_DOT } from './project.js';

const enc = encodeURIComponent;
const MSG_ICO = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 5h16v11H9l-5 4z"></path></svg>';

export function projectName(pr) {
  const f = String(pr?.folder || '').replace(/[/\\]+$/, '');
  const seg = f ? f.slice(Math.max(f.lastIndexOf('/'), f.lastIndexOf('\\')) + 1) : '';
  return seg || pr?.id || '';
}

export function needsByProject(asks, projects) {
  const map = new Map();
  const ids = new Set((Array.isArray(projects) ? projects : []).filter((p) => p?.id && !p.error).map((p) => p.id));
  for (const a of Array.isArray(asks) ? asks : []) {
    if (!isOwnerAsk(a) || (a.status && a.status !== 'open') || !a.project || !ids.has(a.project)) continue;
    (map.get(a.project) || map.set(a.project, []).get(a.project)).push(a);
  }
  return map;
}

function cardRank(pr, needs) {
  if (pr.error) return 4;
  if ((needs?.get(pr.id)?.length || 0) > 0) return 0;
  const st = projectState(pr);
  return st === 'running' ? 1 : st === 'stopped' ? 2 : 3;
}

function projectCardHTML(pr, needs) {
  if (pr.error) {
    const id = pr.id || '';
    return `<div class="card ov-card err" role="group" aria-label="${esc(`${id}, can't read project, ${pr.error}`)}">` +
      `<div class="ov-top"><span class="mono muted small">${esc(id)}</span></div>` +
      `<span class="ov-err">${ERR_ICO}can&#39;t read project</span>` +
      `<p class="small">Its state file could not be read.</p>` +
      `<p class="small muted mono">${esc(pr.error)}</p></div>`;
  }
  const name = projectName(pr), title = goalTitle(pr.goal, pr.id), st = projectState(pr);
  const n = needs?.get(pr.id)?.length || 0;
  const c = pr.counts || {};
  const total = Object.values(c).reduce((s, v) => s + (Number(v) || 0), 0);
  const done = Number(c.done) || 0;
  const prog = total ? `${done} of ${total} done` : 'no tasks yet';
  const aria = [name, title, st, n ? `needs you: ${n}` : '', prog].filter(Boolean).join(', ');
  return `<a class="card ov-card${n ? ' needs' : ''}" href="#${esc(enc(pr.id))}" data-ov-proj="${esc(pr.id)}" aria-label="${esc(aria)}">` +
    `<div class="ov-top"><span class="mono muted small">${esc(name)}</span>${n ? `<span class="ov-needs">needs you: ${n}</span>` : ''}</div>` +
    `<span class="ov-goal">${esc(title)}</span>` +
    `<div class="ov-foot"><span class="pill"><span class="dot ${ST_DOT[st] || 'idle'}" aria-hidden="true"></span>${esc(st)}</span>` +
    `<span class="small muted">${esc(prog)}</span></div></a>`;
}

export function renderOverviewHTML(projects, needs) {
  const list = [...(Array.isArray(projects) ? projects : [])].sort((a, b) => cardRank(a, needs) - cardRank(b, needs));
  const cards = list.map((pr) => projectCardHTML(pr, needs)).join('');
  return `<div class="head"><h1>projects</h1></div><div class="ov-grid">${cards}</div>` +
    `<div class="ask-opts"><button type="button" class="ask-opt sm" data-new-project="1">${PLUS_ICO}New project</button></div>`;
}

export function renderNeedsYouHTML(projects, needs, open = false) {
  const groups = (Array.isArray(projects) ? projects : [])
    .filter((pr) => !pr.error)
    .map((pr) => [pr, needs?.get(pr.id) || []])
    .filter(([, list]) => list.length > 0);
  const total = groups.reduce((n, [, list]) => n + list.length, 0);
  if (!total) return '';
  const pc = groups.length;
  const lbl = `needs you: ${total} ask${total === 1 ? '' : 's'} in ${pc} project${pc === 1 ? '' : 's'}`;
  const btn = `<button type="button" id="ni-btn" class="ask-opt sm ni-btn" data-ni-toggle="1" aria-expanded="${open ? 'true' : 'false'}" aria-label="${esc(lbl)}">${MSG_ICO}needs you: ${total}</button>`;
  if (!open) return btn;
  const items = groups.map(([pr, list]) => `<div class="ni-group"><h3><span class="mono muted">${esc(projectName(pr))}</span> · ${esc(goalTitle(pr.goal, pr.id))}</h3>` +
    `<ul>${list.map((a) => `<li><a href="#${esc(enc(pr.id))}" data-ask-id="${esc(a.id)}" data-ni-proj="${esc(pr.id)}">${esc(a.question)}</a></li>`).join('')}</ul></div>`).join('');
  return `${btn}<section class="card ni-list" aria-label="needs you"><h2>needs you</h2>${items}</section>`;
}
