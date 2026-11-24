// Project settings panel for /factory (plan.md PR 14.2, FactoryWebSettings &
// FactoryWebReadBack): generic over the daemon's SettingRow table.
import { esc, pairBoxHTML, ERR_ICO } from './asks.js';

const OK_ICO = '<svg class="ico" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12l5 5 9-10"></path></svg>';

export function proposeSetting(flow, project, name, value) {
  return flow.send(`/api/factory/projects/${encodeURIComponent(project)}/settings`, { name, value });
}

function rowHTML(r, asks, ui) {
  const slug = r.name.replace(/[^a-z0-9]+/gi, '-'), id = 'set-' + slug, metaId = 'set-meta-' + slug, errId = 'set-err-' + slug;
  const ask = (asks || []).find((a) => a.status === 'open' && a.set?.name === r.name && (!a.project || !ui.project || a.project === ui.project));
  const isChoice = Array.isArray(r.allowed) && r.allowed.length > 0, isNum = !isChoice && r.max > r.min;
  const curVal = ui.edits?.[r.name] !== undefined ? ui.edits[r.name] : (ask ? ask.set.value : r.value);
  const dirty = !ask && ui.edits?.[r.name] !== undefined && ui.edits[r.name] !== r.value;
  const changed = r.value !== r.default && !r.problem, sending = ui.busy === r.name;
  const allowMeta = isNum ? `allowed ${r.min}–${r.max}` : isChoice ? `allowed ${[...r.allowed.filter(Boolean), ...(r.allowed.includes('') ? ['not set'] : [])].join(', ')}` : '';
  const metaTxt = `default ${r.default || 'not set'}${allowMeta ? ' · ' + allowMeta : ''}`;
  const rowErr = ui.errors?.[r.name] || '';
  const storedBad = !rowErr && r.problem ? `The stored value ${r.value || 'not set'} is not allowed (${allowMeta || r.problem.split(': ')[1] || 'not allowed'}). Change it before the next start.` : '';
  const alertTxt = !ask ? (rowErr || storedBad) : '', bad = Boolean(alertTxt || (ask && rowErr)), desc = alertTxt ? `${metaId} ${errId}` : metaId;
  const opts = isChoice ? (r.allowed.includes(curVal) ? r.allowed : [curVal, ...r.allowed]) : [];
  const ctrl = isChoice
    ? `<select id="${id}" class="set-in${bad ? ' bad' : ''}" data-setting="${esc(r.name)}" aria-describedby="${desc}">${opts.map((o) => `<option value="${esc(o)}"${o === curVal ? ' selected' : ''}>${esc(o || 'not set')}</option>`).join('')}</select>`
    : `<input id="${id}" class="set-in${isNum ? ' num' : ''}${bad ? ' bad' : ''}" data-setting="${esc(r.name)}" type="${isNum ? 'number' : 'text'}"${isNum ? ` min="${r.min}" max="${r.max}"` : ' placeholder="not set"'} aria-describedby="${desc}" value="${esc(curVal)}">`;
  const btn = dirty ? ` <button type="button" class="ask-opt rec" data-propose="${esc(r.name)}" aria-label="${esc(`Change ${r.label}`)}"${sending ? ' disabled' : ''}>${sending ? 'Asking…' : 'Change'}</button>`
    : (!ask && changed ? ` <button type="button" class="ask-opt" data-default="${esc(r.name)}" aria-label="${esc(`Set ${r.label} back to the default, ${r.default || 'not set'}`)}"${sending ? ' disabled' : ''}>Back to the default</button>` : '');
  const marks = [r.problem ? '<span class="chip bad">not allowed</span>' : changed ? '<span class="chip chg">changed</span>' : '', r.note ? '<span class="chip">not used</span>' : ''].filter(Boolean).join(' ');
  let extra = '';
  if (ask) {
    const askOpts = (ask.options || []).map((opt) => {
      const rec = opt === ask.recommend;
      return `<button type="button" class="ask-opt${rec ? ' rec' : ''}" data-ask-id="${esc(ask.id)}" data-option="${esc(opt)}" aria-label="${esc(rec ? `${opt} (Recommended)` : opt)}"${ui.askBusyId === ask.id ? ' disabled' : ''}>${esc(opt)}${rec ? ' <span class="rec-tag">Recommended</span>' : ''}</button>`;
    }).join(' ');
    extra = `<div class="ask-card"><p class="ask-q">${esc(ask.question)}</p><p class="small">Waiting for your answer. The same question is under asks for you, on your phone and by voice.</p>${rowErr ? `<p id="${errId}" class="ask-err" role="alert">${ERR_ICO}<span>${esc(rowErr)}</span></p>` : ''}<div class="ask-opts" role="group" aria-label="Answer options">${askOpts}</div></div>`;
  } else if (alertTxt) extra = `<p id="${errId}" class="ask-err" role="alert">${ERR_ICO}<span>${esc(alertTxt)}</span></p>`;
  else if (ui.notices?.[r.name]) extra = ui.notices[r.name].startsWith('Not saved:') ? `<p class="set-note" role="status">${esc(ui.notices[r.name])}</p>` : `<p class="set-ok" role="status">${OK_ICO}<span>${esc(ui.notices[r.name])}</span></p>`;
  if (ui.pairRow === r.name) extra += pairBoxHTML(ui.pairState, 'pair-code-' + slug);
  return `<div class="srow" data-row="${esc(r.name)}"><div class="srow-top"><label class="sname" for="${id}">${esc(r.label)}</label> ${ctrl}${btn}</div> ` +
    `<div class="srow-meta muted small"><span id="${metaId}">${esc(metaTxt)}</span>${marks ? ' ' + marks : ''}</div>${r.note ? ` <p class="muted small">${esc(r.note)}</p>` : ''}${extra ? ' ' + extra : ''}</div>`;
}

export function renderSettingsHTML(rows, asks = [], ui = {}) {
  if (ui.error) return '<h2>settings</h2><p class="status" role="status"><span class="dot" aria-hidden="true"></span><span>Can&#39;t load the settings. Trying again.</span></p><p class="muted small">The rest of the page still works.</p>';
  if (!rows) return '<h2>settings</h2><p class="status" role="status"><span class="dot working" aria-hidden="true"></span><span>Loading the settings…</span></p><span class="set-sk" aria-hidden="true"></span><span class="set-sk" aria-hidden="true"></span><span class="set-sk" aria-hidden="true"></span>';
  const top = rows.filter((r) => r.top), rest = rows.filter((r) => !r.top);
  return `<div class="set-head"><h2>settings</h2><p class="muted small">For this project only. A change is read back to you and saved when you confirm.</p></div>` +
    top.map((r) => rowHTML(r, asks, ui)).join('') +
    (rest.length ? `<details class="all-settings"><summary>all settings (${rest.length})</summary>${rest.map((r) => rowHTML(r, asks, ui)).join('')}</details>` : '');
}
