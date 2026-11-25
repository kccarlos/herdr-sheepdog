// New-project form, start checks and start/check requests for /factory (plan.md PR 14.5).
import { esc, pairBoxHTML, ERR_ICO, OK_ICO } from './asks.js';
import { MODELS_HINT, fixHTML, rowHTML, parseModelCandidates } from './settings.js';

const ico14 = (d) => `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${d}"></path></svg>`;
export const PLUS_ICO = ico14('M12 5v14M5 12h14');
const FAIL_ICO = ico14('M6 6l12 12M18 6L6 18'), SKIP_ICO = ico14('M5 12h14');
const CK_ST = { ok: ['pass', 'ck-ok', OK_ICO], warn: ['warning', 'ck-warn', ERR_ICO], fail: ['fail', 'ck-fail', FAIL_ICO], skip: ['skipped', 'ck-skip', SKIP_ICO] };
export const CK_CANT = "Can't run the start checks: sheepdog did not answer in time. Check that sheepdog is running, then check again.";
export const EMPTY_CARD_HTML = `<section class="card" aria-label="No project"><h2>no project yet</h2><p class="muted">A project is one folder with a git repository and one goal. The coordinator plans the tasks, workers build them, reviewers check them, and you are asked only for decisions.</p><div class="ask-opts"><button type="button" class="ask-opt pri" data-new-project="1">${PLUS_ICO}New project</button></div></section>`;

export async function loadNewProject(get) {
  const [setRes, spRes] = await Promise.all([
    get('/api/factory/settings').catch(() => null),
    fetch('/api/spaces', { cache: 'no-store', signal: AbortSignal.timeout(6000) })
      .then(async (r) => (r.ok ? { spaces: await r.json().catch(() => null), error: false } : { spaces: null, error: true }))
      .catch(() => ({ spaces: null, error: true })),
  ]);
  return { rows: setRes?.rows || [], spaces: spRes.spaces, spacesError: spRes.error };
}

export async function checkProject(folder, set = {}, fetchFn = globalThis.fetch) {
  try {
    const r = await fetchFn('/api/factory/projects/check', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ folder, set }), signal: AbortSignal.timeout(45000) });
    const body = await r.json().catch(() => ({}));
    if (r.ok) return { ok: true, results: Array.isArray(body.results) ? body.results : [] };
    if (body.name) return { ok: false, name: body.name, fix: body.fix || '', message: body.message || body.error || '' };
    return { ok: false, cant: r.status >= 500 || !body.error, message: body.error || CK_CANT };
  } catch { return { ok: false, cant: true, message: CK_CANT }; }
}

export const startProject = (flow, folder, goal, set = {}) => flow.send('/api/factory/projects/start', { folder, goal, set });

function suggestionsOf(spaces, projects) {
  const map = new Map(), add = (path, git) => { if (path && !map.has(path)) map.set(path, { path, git: Boolean(git) }); };
  for (const s of spaces?.spaces || []) add(s.path, s.git);
  for (const f of spaces?.folders || []) add(f.path, f.git);
  for (const p of projects || []) add(p.folder, true);
  return [...map.values()];
}

export function renderChecksHTML(results, ui = {}) {
  let body = '';
  if (ui.checking) body = '<p class="status" role="status"><span class="dot working" aria-hidden="true"></span><span>Checking the folder…</span></p><span class="set-sk" aria-hidden="true"></span><span class="set-sk" aria-hidden="true"></span><span class="set-sk" aria-hidden="true"></span>';
  else if (ui.checkError) body = `<p class="ask-err" role="alert">${ERR_ICO}<span>${esc(ui.checkError)}</span></p>`;
  else if (!Array.isArray(results)) body = '<p class="small muted">Every check must pass before a project can start. A warning does not stop it.</p>';
  else {
    const nFail = results.filter((r) => r.status === 'fail').length, nWarn = results.filter((r) => r.status === 'warn').length, nOk = results.filter((r) => r.status === 'ok').length;
    if (nFail > 0) body += `<p class="ask-err" role="alert">${ERR_ICO}<span>${nFail === 1 ? '1 check failed. Fix it, then check again.' : `${nFail} checks failed. Fix them, then check again.`}</span></p><p class="small muted">Is a failed check for a CLI you do not use? Change the models above, then check again.</p>`;
    else if (nWarn > 0) body += `<p class="set-ok" role="status">${OK_ICO}<span>${nOk} pass, ${nWarn} warning${nWarn === 1 ? '' : 's'}. You can start.</span></p>`;
    else body += `<p class="set-ok" role="status">${OK_ICO}<span>All ${results.length} checks pass.</span></p>`;
    body += `<ul class="ck-list">${results.map((r) => {
      const [word, cls, ico] = CK_ST[r.status] || [r.status || 'unknown', 'ck-skip', SKIP_ICO];
      return `<li class="srow ck-row"><div class="ck-head"><span class="ck-st ${cls}">${ico}${esc(word)}</span><span class="ck-name">${esc(r.name)}</span></div>${r.detail ? `<p class="small muted">${esc(r.detail)}</p>` : ''}${r.fix ? fixHTML(r.fix, ui.copied) : ''}</li>`;
    }).join('')}</ul>`;
  }
  return `<section class="card" aria-label="start checks"><h2>start checks</h2>${body}</section>`;
}

export function renderNewProjectHTML(rows, ui = {}) {
  const top = (Array.isArray(rows) ? rows : []).filter((r) => r.top), isMod = (r) => Array.isArray(r.clis) && r.clis.length > 0;
  const modRows = top.filter(isMod), otherTop = top.filter((r) => !isMod(r)), rowUI = { ...ui, form: true, prefix: 'np-set' };
  const prior = modRows.flatMap((r) => [...parseModelCandidates(r.value), ...parseModelCandidates(r.default)]);
  const suggs = suggestionsOf(ui.spaces, ui.projects);
  const suggHTML = suggs.length
    ? `<div class="np-sugg"><span class="small muted">folders sheepdog knows</span><div class="sugg">${suggs.map((s) => `<button type="button" data-sugg="${esc(s.path)}" data-sugg-git="${s.git ? '1' : '0'}"><span class="mono">${esc(s.path)}</span><span class="small muted">${s.git ? 'git repository' : 'not a git repository'}</span></button>`).join('')}</div></div>`
    : (ui.spacesError ? '<p class="small muted">No folders to suggest: herdr cannot be reached. Type the folder&#39;s full path.</p>' : '');
  const canStart = Array.isArray(ui.checks) && ui.checks.length > 0 && !ui.checks.some((r) => r.status === 'fail') && !ui.checking && !ui.notGit;
  const startBtn = canStart || ui.starting
    ? `<button type="button" class="ask-opt pri" data-np-start="1"${ui.starting ? ' aria-disabled="true"' : ''}>${ui.starting ? 'Starting…' : 'Start the project and its agents'}</button>`
    : '<button type="button" class="ask-opt off" data-np-start="1" aria-disabled="true" aria-describedby="np-start-off">Start the project and its agents</button><span id="np-start-off" class="small muted">Start turns on when no check fails.</span>';
  const pairHTML = ui.pairState?.pending?.kind === 'start' ? pairBoxHTML(ui.pairState, 'pair-code-np') : '';
  return `<div class="np-wrap" data-np-form="1"><a class="back" href="#${esc(encodeURIComponent(ui.backProject || ''))}" data-np-back="1">‹ board</a><h1 class="np-h1">new project</h1>` +
    `<section class="card" aria-label="new project"><div class="np-field"><label class="np-lbl" for="np-folder">folder</label><span id="np-folder-hint" class="small muted">A folder on this computer that is a git repository with a GitHub remote.</span>` +
    `<input id="np-folder" name="folder" class="set-in full mono${ui.notGit ? ' bad' : ''}" type="text" value="${esc(ui.folder || '')}" placeholder="/path/to/your/repo" aria-describedby="np-folder-hint${ui.notGit ? ' np-folder-err' : ''}">` +
    `${ui.notGit ? `<p id="np-folder-err" class="ask-err" role="alert">${ERR_ICO}<span>This folder is not a git repository. Pick another folder, or make it one.</span></p>` : ''}${suggHTML}</div>` +
    `<div class="np-field"><label class="np-lbl" for="np-goal">goal</label><span id="np-goal-hint" class="small muted">What you want built, in your own words. The coordinator turns it into tasks.</span>` +
    `<textarea id="np-goal" name="goal" class="set-in full${ui.goalErr ? ' bad' : ''}" rows="3" aria-describedby="np-goal-hint${ui.goalErr ? ' np-goal-err' : ''}">${esc(ui.goal || '')}</textarea>` +
    `${ui.goalErr ? `<p id="np-goal-err" class="ask-err" role="alert">${ERR_ICO}<span>Write a goal first.</span></p>` : ''}</div>` +
    `${modRows.length ? `<fieldset class="np-models"><legend class="np-lbl">models</legend><p class="small muted">${MODELS_HINT}</p>${modRows.map((r) => rowHTML(r, [], rowUI, prior)).join('')}</fieldset>` : ''}` +
    `${otherTop.length ? `<div>${otherTop.map((r) => rowHTML(r, [], rowUI, prior)).join('')}</div>` : ''}</section>` +
    `${renderChecksHTML(ui.checks, ui)}<div class="np-actions"><div class="ask-opts"><button type="button" class="ask-opt${canStart ? '' : ' rec'}" data-np-check="1"${ui.checking || ui.starting ? ' disabled' : ''}>${ui.hasChecked ? 'Check again' : 'Check this folder'}</button>${startBtn}</div>` +
    `${ui.startError ? `<p class="ask-err" role="alert">${ERR_ICO}<span>${esc(ui.startError)}</span></p>` : ''}${pairHTML}` +
    `<p class="small muted">Starting runs agents on your model accounts. They use your quota.</p></div></div>`;
}
