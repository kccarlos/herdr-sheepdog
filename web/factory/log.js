// Readable /factory log (design.md 12.1, FactoryWebLog board).
import { esc } from './asks.js';
import { goalTitle } from './goal.js';

const SVG = (d) => `<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${d}</svg>`;
const MARKS = {
  merged: ['merged', 'merged', SVG('<circle cx="6" cy="6" r="2.5"></circle><circle cx="6" cy="18" r="2.5"></circle><circle cx="18" cy="12" r="2.5"></circle><path d="M6 8.5v7"></path><path d="M6 9c0 3 4 3 9.5 3"></path>')],
  needs_decision: ['needs', 'held up', SVG('<path d="M12 4l9 16H3z"></path><path d="M12 10v4"></path><path d="M12 17v.5"></path>')],
  asked_you: ['asked', 'asked you', SVG('<path d="M4 5h16v11H9l-5 4z"></path>')],
  yours: ['yours', 'you', SVG('<circle cx="12" cy="8" r="3.5"></circle><path d="M5 20c1-4 4-5.5 7-5.5s6 1.5 7 5.5"></path>')],
};

const ghRepo = (url) => {
  const m = String(url || '').trim().match(/^https:\/\/github\.com\/([A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+?)(?:\.git)?\/?$/);
  return m ? `https://github.com/${m[1]}` : '';
};

export function tidy(text, repoURL = '') {
  const repo = ghRepo(repoURL);
  return esc(text).replace(/\b(?:PR #(\d+)|([0-9a-fA-F]{40}))\b/g, (m, pr, sha) => {
    if (sha) return `<code title="${sha}">${sha.slice(0, 7)}</code>`;
    return repo ? `<a href="${esc(repo)}/pull/${pr}" aria-label="PR #${pr}, opens GitHub">PR #${pr}</a>` : m;
  });
}

const dayKey = (d) => `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
const clockHM = (d) => isNaN(d) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });

function dayHeading(d, today, yest) {
  if (isNaN(d)) return '';
  const k = dayKey(d), label = d.toLocaleDateString('en-GB', { weekday: 'long', day: 'numeric', month: 'long' });
  return k === today ? `Today, ${label}` : k === yest ? `Yesterday, ${label}` : label;
}

function lineHTML(l, repoURL, forTask) {
  const d = new Date(l.at), mark = MARKS[l.kind];
  const by = l.kind === 'yours' ? '' : (l.by === 'owner' ? 'you' : l.by);
  const why = l.by === 'owner' && l.decision === 'started' && l.why ? goalTitle(l.why, '') : l.why;
  const parts = [
    by && `<span class="muted">${esc(by)}</span>`,
    !forTask && l.task && `<code>${esc(l.task)}</code>`,
    l.decision && `<span class="b">${tidy(l.decision, repoURL)}</span>`,
    why && `<span class="muted">${tidy(why, repoURL)}</span>`,
  ].filter(Boolean).join(' ');
  return `<li class="ll"><time datetime="${esc(l.at)}">${esc(clockHM(d))}</time>` +
    `${mark ? `<span class="mark ${mark[0]}">${mark[2]}${mark[1]}</span>` : ''}<span class="txt">${parts}</span></li>`;
}

export function renderLogHTML(lines = [], ui = {}, repoURL = '') {
  const list = Array.isArray(lines) ? lines : [], title = ui.heading || (ui.forTask ? 'log for this task' : 'log');
  const limit = ui.limit || 100, sel = ui.task || '';
  const taskOpts = [...new Set([...(ui.tasks || []), ...(sel ? [sel] : [])])];
  const filter = ui.forTask ? '' : `<div class="log-f small"><label for="log-task">task</label><select id="log-task" class="set-in log-sel" data-log-task="1">` +
    `<option value=""${!sel ? ' selected' : ''}>all tasks</option>${taskOpts.map((r) => `<option value="${esc(r)}"${r === sel ? ' selected' : ''}>${esc(r)}</option>`).join('')}</select></div>`;
  const obs = `<label class="log-chk small" for="log-obs"><input id="log-obs" type="checkbox" data-log-obs="1"${ui.observations ? ' checked' : ''}>show routine checks</label>`;
  let body = '';
  if (ui.error) {
    body = `<p class="status" role="status"><span class="dot" aria-hidden="true"></span><span>Can&#39;t load the log. Trying again.</span></p>`;
  } else if (!list.length) {
    body = !ui.observations && ui.hasObservations
      ? `<div class="log-empty"><p class="small muted">Nothing but routine checks yet.</p><button type="button" class="linkbtn" data-log-show="1">Show them</button></div>`
      : `<p class="small muted">${esc(ui.empty || (ui.forTask ? 'Nothing logged for this task yet.' : 'Nothing logged yet.'))}</p>`;
  } else {
    const now = new Date(), today = dayKey(now), yest = dayKey(new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1));
    let curDay = '', items = '';
    for (const l of list.slice().reverse()) {
      const d = new Date(l.at), dk = isNaN(d) ? '' : dayKey(d);
      if (dk && dk !== curDay) { curDay = dk; items += `<li class="day" role="heading" aria-level="3">${esc(dayHeading(d, today, yest))}</li>`; }
      items += lineHTML(l, repoURL, ui.forTask);
    }
    body = `<ol class="log">${items}</ol>${list.length >= limit ? `<p class="small muted">The newest ${limit} lines.</p>` : ''}`;
  }
  return `<section class="card" aria-label="${esc(title)}"><div class="log-head"><h2>${esc(title)}</h2><div class="log-ctrls">${filter}${obs}</div></div>${body}</section>`;
}
