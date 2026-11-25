// Project settings panel for /factory (plan.md PR 14.2 & 14.3): generic over SettingRow.
import { esc, pairBoxHTML, ERR_ICO, OK_ICO } from './asks.js';

const ORD = ['first', 'second', 'third', 'fourth', 'fifth', 'sixth', 'seventh', 'eighth', 'ninth'], EFFORTS = ['low', 'medium', 'high'];
export const MODELS_HINT = 'The CLI, model and effort each role uses. In order: the engine uses the first one it can start. A retro reviewer looks at a task whose reviews keep failing.';
export const proposeSetting = (flow, project, name, value) => flow.send(`/api/factory/projects/${encodeURIComponent(project)}/settings`, { name, value });
export function parseModelCandidates(raw, fallbackCLI = '') {
  const items = String(raw || '').split(',').map((s) => s.trim()).filter(Boolean).map((p) => { const i = p.indexOf('/'), j = p.lastIndexOf('/'); return i > 0 && j > i ? { cli: p.slice(0, i).trim(), model: p.slice(i + 1, j).trim(), effort: p.slice(j + 1).trim() || 'medium' } : { cli: fallbackCLI, model: p, effort: 'medium' }; });
  return items.length ? items : (fallbackCLI ? [{ cli: fallbackCLI, model: '', effort: 'medium' }] : []);
}
export function fixHTML(fix, copied = '') {
  if (!fix || !fix.startsWith('run:')) return fix ? `<p class="small"><b>Fix:</b> ${esc(fix)}</p>` : '';
  const body = fix.slice(4).trim(), cuts = [body.indexOf(' ('), body.indexOf(', then ')].filter((i) => i >= 0), cut = cuts.length ? Math.min(...cuts) : body.length, cmd = body.slice(0, cut).trim(), rest = body.slice(cut).replace(/^,\s*/, '').trim(), cp = copied === cmd;
  return `<div class="fix-blk"><span class="small"><b>Fix, in a terminal:</b></span><div class="cmd"><code>${esc(cmd)}</code><button type="button" class="ask-opt sm" data-copy="${esc(cmd)}" aria-label="${esc(cp ? `Copied ${cmd}` : `Copy the command ${cmd}`)}">${cp ? 'Copied' : 'Copy'}</button>${cp ? '<span class="sr" role="status">Copied</span>' : ''}</div>${rest ? `<p class="small">${esc(rest)}</p>` : ''}</div>`;
}
export const editorAlertHTML = (errId, err, fix, copied = '') => err ? `<p id="${errId}" class="ask-err" role="alert">${ERR_ICO}<span>${esc(err)}</span></p>${fixHTML(fix, copied)}` : '';

function modelsRowHTML(r, ask, ui, { pre, slug, metaId, errId, sending, rowErr, marks, extra, prior, curVal }) {
  if (ui.editor?.row === r.name && !ask) {
    const cands = ui.editor.cands || [], knownSet = [...new Set(cands.map((c) => c.cli).filter((c) => r.models?.[c]?.length))];
    const items = cands.map((c, i) => {
      const ord = `${ORD[i] || `#${i + 1}`} choice`, cid = `${pre}-${slug}-cli-${i}`, mid = `${pre}-${slug}-model-${i}`, eid = `${pre}-${slug}-eff-${i}`, known = r.models?.[c.cli], m = c.model.trim();
      const suggs = [...new Set([...(known || []), ...prior.filter((p) => p.cli === c.cli && p.model).map((p) => p.model)])], tag = `${c.cli} ${m || 'model'}`;
      const cliBad = rowErr.startsWith(`${c.cli} is not `), modBad = (rowErr === 'Type a model name.' && !m) || (rowErr.endsWith(': a model name cannot hold a comma') && m.includes(',')) || Boolean(m && rowErr.startsWith(`${c.cli} does not offer ${m}.`));
      const btns = [i > 0 && `<button type="button" class="ask-opt sm" data-cand-up="${i}" aria-label="${esc(`Move ${tag} up`)}"${sending ? ' disabled' : ''}>Move up</button>`, cands.length > 1 && `<button type="button" class="ask-opt sm" data-cand-rm="${i}" aria-label="${esc(`Remove ${tag}`)}"${sending ? ' disabled' : ''}>Remove</button>`].filter(Boolean).join(' ');
      return `<li class="cand"><div class="srow-top"><span class="small"><b>${esc(ord)}</b></span>${btns ? `<span class="ask-opts">${btns}</span>` : ''}</div><div class="cand-fields"><div class="cand-field"><label class="small" for="${cid}">CLI</label><select id="${cid}" class="set-in${cliBad ? ' bad' : ''}" aria-label="${esc(`CLI, ${ord}`)}" data-cand-idx="${i}" data-cand-field="cli">${r.clis.map((x) => `<option value="${esc(x)}"${x === c.cli ? ' selected' : ''}>${esc(x)}</option>`).join('')}</select></div>` +
        `<div class="cand-field wide"><label class="small" for="${mid}">model</label><input id="${mid}" class="set-in${modBad ? ' bad' : ''}" type="text" aria-label="${esc(`model, ${ord}`)}" data-cand-idx="${i}" data-cand-field="model" value="${esc(c.model)}"${suggs.length ? ` list="${mid}-list"` : ''}>${suggs.length ? `<datalist id="${mid}-list">${suggs.map((x) => `<option value="${esc(x)}"></option>`).join('')}</datalist>` : ''}</div>` +
        `<div class="cand-field"><label class="small" for="${eid}">effort</label><select id="${eid}" class="set-in" aria-label="${esc(`effort, ${ord}`)}" data-cand-idx="${i}" data-cand-field="effort">${EFFORTS.map((x) => `<option value="${x}"${x === c.effort ? ' selected' : ''}>${x}</option>`).join('')}</select></div></div>${!known?.length ? `<p class="muted small">sheepdog cannot list the models ${esc(c.cli)} offers. This name is checked when an agent starts.</p>` : ''}</li>`;
    }).join('');
    const saveLbl = ui.form ? 'Done' : (sending ? 'Asking…' : 'Change'), saveAria = ui.form ? `Done editing ${r.label}` : `Change ${r.label}`;
    return `<div class="srow" data-row="${esc(r.name)}"><div class="srow-top"><span class="sname">${esc(r.label)}</span></div>${ui.form ? '' : `<p class="muted small">${MODELS_HINT}</p>`}${items ? `<ol class="cand-ed">${items}</ol>` : '<p class="muted small">not set</p>'}` +
      knownSet.map((c) => `<p class="muted small">Model names ${esc(c)} offers on this account: ${r.models[c].map(esc).join(' · ')}. The field also takes a typed name.</p>`).join('') +
      `<div class="ask-opts"><button type="button" class="ask-opt sm" data-cand-add="${esc(r.name)}"${sending ? ' disabled' : ''}>Add a model</button> <button type="button" class="ask-opt sm" data-cand-default="${esc(r.name)}" aria-label="${esc(`Fill in the default ${r.label}`)}"${sending ? ' disabled' : ''}>Back to the default</button></div><div class="cand-alert">${editorAlertHTML(errId, rowErr, ui.fixes?.[r.name], ui.copied)}</div>` +
      `<div class="ask-opts"><button type="button" class="ask-opt rec" data-cand-save="${esc(r.name)}" aria-label="${esc(saveAria)}"${sending ? ' disabled' : ''}>${saveLbl}</button> <button type="button" class="ask-opt" data-cand-cancel="${esc(r.name)}"${sending ? ' disabled' : ''}>Cancel</button></div>${ui.pairRow === r.name ? pairBoxHTML(ui.pairState, 'pair-code-' + slug) : ''}</div>`;
  }
  const cands = parseModelCandidates(ui.form ? curVal : r.value), defTxt = parseModelCandidates(r.default).map((c) => `${c.cli}/${c.model}/${c.effort}`).join(' then ') || 'not set';
  const list = cands.length ? `<ol class="cand-list">${cands.map((c, i) => `<li><span class="muted">${i + 1}.</span> <span class="chip mono">${esc(`${c.cli} / ${c.model} / ${c.effort}`)}</span></li>`).join('')}</ol>` : '<span class="muted small">not set</span>';
  return `<div class="srow" data-row="${esc(r.name)}"><div class="srow-top"><div class="srow-models"><span class="sname">${esc(r.label)}</span>${list}</div>${!ask ? `<button type="button" class="ask-opt" data-edit="${esc(r.name)}" aria-label="${esc(`Edit ${r.label}`)}">Edit</button>` : ''}</div> <div class="srow-meta muted small"><span id="${metaId}">${esc(`default ${defTxt}`)}</span>${marks ? ' ' + marks : ''}</div>${r.note ? ` <p class="muted small">${esc(r.note)}</p>` : ''}${extra ? ' ' + extra : ''}</div>`;
}

export function rowHTML(r, asks, ui, prior = []) {
  const pre = ui.prefix || 'set', slug = r.name.replace(/[^a-z0-9]+/gi, '-'), id = `${pre}-${slug}`, metaId = `${pre}-meta-${slug}`, errId = `${pre}-err-${slug}`;
  const ask = ui.form ? null : (asks || []).find((a) => a.status === 'open' && a.set?.name === r.name && (!a.project || !ui.project || a.project === ui.project));
  const isModels = Array.isArray(r.clis) && r.clis.length > 0, isChoice = !isModels && Array.isArray(r.allowed) && r.allowed.length > 0;
  const isNum = !isModels && !isChoice && Number.isFinite(r.min) && Number.isFinite(r.max) && r.max >= r.min && (r.min !== 0 || r.max !== 0);
  const curVal = ui.edits?.[r.name] !== undefined ? ui.edits[r.name] : (ask ? ask.set.value : r.value), dirty = !ui.form && !ask && ui.edits?.[r.name] !== undefined && ui.edits[r.name] !== r.value;
  const changed = (ui.form ? curVal : r.value) !== r.default && !r.problem, sending = ui.busy === r.name;
  const allowMeta = isNum ? `allowed ${r.min}–${r.max}` : isChoice ? `allowed ${[...r.allowed.filter(Boolean), ...(r.allowed.includes('') ? ['not set'] : [])].join(', ')}` : '';
  const metaTxt = `default ${r.default || 'not set'}${allowMeta ? ' · ' + allowMeta : ''}`, rowErr = ui.errors?.[r.name] || '';
  const storedBad = !rowErr && r.problem ? (allowMeta ? `The stored value ${r.value || 'not set'} is not allowed (${allowMeta}). Change it before the next start.` : `${r.problem}. Change it before the next start.`) : '';
  const alertTxt = !ask ? (rowErr || storedBad) : '', bad = Boolean(alertTxt || (ask && rowErr)), desc = bad ? `${metaId} ${errId}` : metaId;
  const marks = [r.problem ? '<span class="chip bad">not allowed</span>' : changed ? '<span class="chip chg">changed</span>' : '', r.note ? '<span class="chip">not used</span>' : ''].filter(Boolean).join(' ');
  let extra = '';
  if (ask) {
    const askOpts = (ask.options || []).map((opt) => `<button type="button" class="ask-opt${opt === ask.recommend ? ' rec' : ''}" data-ask-id="${esc(ask.id)}" data-option="${esc(opt)}" aria-label="${esc(opt === ask.recommend ? `${opt} (Recommended)` : opt)}"${ui.askBusyId === ask.id ? ' disabled' : ''}>${esc(opt)}${opt === ask.recommend ? ' <span class="rec-tag">Recommended</span>' : ''}</button>`).join(' ');
    extra = `<div class="ask-card"><p class="ask-q">${esc(ask.question)}</p><p class="small">Waiting for your answer. The same question is under asks for you, on your phone and by voice.</p>${rowErr ? `<p id="${errId}" class="ask-err" role="alert">${ERR_ICO}<span>${esc(rowErr)}</span></p>` : ''}<div class="ask-opts" role="group" aria-label="Answer options">${askOpts}</div></div>`;
  } else if (alertTxt) extra = editorAlertHTML(errId, alertTxt, ui.fixes?.[r.name], ui.copied);
  else if (ui.notices?.[r.name]) extra = ui.notices[r.name].startsWith('Not saved:') ? `<p class="set-note" role="status">${esc(ui.notices[r.name])}</p>` : `<p class="set-ok" role="status">${OK_ICO}<span>${esc(ui.notices[r.name])}</span></p>`;
  if (ui.pairRow === r.name) extra += pairBoxHTML(ui.pairState, 'pair-code-' + slug);
  if (isModels) return modelsRowHTML(r, ask, ui, { pre, slug, metaId, errId, sending, rowErr, marks, extra, prior, curVal });
  const opts = isChoice ? (r.allowed.includes(curVal) ? r.allowed : [curVal, ...r.allowed]) : [];
  const ctrl = isChoice ? `<select id="${id}" class="set-in${bad ? ' bad' : ''}" data-setting="${esc(r.name)}" aria-describedby="${desc}">${opts.map((o) => `<option value="${esc(o)}"${o === curVal ? ' selected' : ''}>${esc(o || 'not set')}</option>`).join('')}</select>`
    : `<input id="${id}" class="set-in${isNum ? ' num' : ''}${bad ? ' bad' : ''}" data-setting="${esc(r.name)}" type="${isNum ? 'number' : 'text'}"${isNum ? ` min="${esc(r.min)}" max="${esc(r.max)}" required` : ' placeholder="not set"'} aria-describedby="${desc}" value="${esc(curVal)}">`;
  const btn = ui.form ? '' : (dirty ? ` <button type="button" class="ask-opt rec" data-propose="${esc(r.name)}" aria-label="${esc(`Change ${r.label}`)}"${sending ? ' disabled' : ''}>${sending ? 'Asking…' : 'Change'}</button>`
    : (!ask && changed ? ` <button type="button" class="ask-opt" data-default="${esc(r.name)}" aria-label="${esc(`Set ${r.label} back to the default, ${r.default || 'not set'}`)}"${sending ? ' disabled' : ''}>${sending ? 'Asking…' : 'Back to the default'}</button>` : ''));
  return `<div class="srow" data-row="${esc(r.name)}"><div class="srow-top"><label class="sname" for="${id}">${esc(r.label)}</label> ${ctrl}${btn}</div> <div class="srow-meta muted small"><span id="${metaId}">${esc(metaTxt)}</span>${marks ? ' ' + marks : ''}</div>${r.note ? ` <p class="muted small">${esc(r.note)}</p>` : ''}${extra ? ' ' + extra : ''}</div>`;
}

export function renderSettingsHTML(rows, asks = [], ui = {}) {
  if (ui.error) return `<h2>settings</h2><p class="status" role="status"><span class="dot" aria-hidden="true"></span><span>Can&#39;t load the settings. Trying again.${typeof ui.error === 'string' && !/^(?:HTTP \d+|error)$/.test(ui.error) ? ' ' + esc(ui.error) : ''}</span></p><p class="muted small">The rest of the page still works.</p>`;
  if (!rows) return '<h2>settings</h2><p class="status" role="status"><span class="dot working" aria-hidden="true"></span><span>Loading the settings…</span></p><span class="set-sk" aria-hidden="true"></span><span class="set-sk" aria-hidden="true"></span><span class="set-sk" aria-hidden="true"></span>';
  const prior = rows.filter((r) => r.clis?.length).flatMap((r) => [...parseModelCandidates(r.value), ...parseModelCandidates(r.default)]);
  const top = rows.filter((r) => r.top), rest = rows.filter((r) => !r.top);
  return `<div class="set-head"><h2>settings</h2><p class="muted small">For this project only. A change is read back to you and saved when you confirm.</p></div>${top.map((r) => rowHTML(r, asks, ui, prior)).join('')}${rest.length ? `<details class="all-settings"><summary>all settings (${rest.length})</summary>${rest.map((r) => rowHTML(r, asks, ui, prior)).join('')}</details>` : ''}`;
}
