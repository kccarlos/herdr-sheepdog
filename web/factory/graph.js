// Task dependency graph: layered one-pass barycenter layout, plain SVG renderer,
// scroll-box sync, and focus/hover edge highlighting (plan.md PR 14.9).
const W = 168, H = 58, GX = 16, GY = 46, PAD = 16, TITLE_MAX = 23;
const ENT = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ENT[c]);
const enc = encodeURIComponent;
const clip = (s, max = TITLE_MAX) => (s.length > max ? s.slice(0, max - 1).trimEnd() + '…' : s);

export const GRAPH_SCROLL_HINT = 'The graph is wider than the window. Scroll it sideways, or switch to the list.';

const STAGE = {
  owner_gate: ['needs your approval', 'blocked'],
  owner_answer: ['waiting on you', 'blocked'],
  blocked_or_failed: ['blocked', 'blocked'],
  ci: ['waiting on CI', 'working'],
  working: ['working', 'working'],
  waiting_on_dep: ['waiting on others', 'idle'],
  queued: ['queued', 'idle'],
  done: ['done', 'done'],
};
const stageInfo = (s) => (Object.hasOwn(STAGE, s) ? STAGE[s] : [s || 'unknown', 'idle']);

export function layout(tasks = []) {
  if (!tasks.length) return { nodes: [], edges: [], width: 0, height: 0, layers: 0 };
  const byLayer = new Map();
  tasks.forEach((t, i) => {
    const l = Number(t.layer) || 0;
    (byLayer.get(l) || byLayer.set(l, []).get(l)).push({ t, i });
  });
  const rows = [...byLayer.keys()].sort((a, b) => a - b).map((l) => byLayer.get(l));
  const maxCols = Math.max(...rows.map((r) => r.length));
  const width = maxCols * W + (maxCols - 1) * GX + 2 * PAD;
  const height = rows.length * H + (rows.length - 1) * GY + 2 * PAD;
  const placed = new Map(), nodes = [], edges = [];
  rows.forEach((row, r) => {
    if (r > 0) {
      const bary = (t) => {
        const xs = [...new Set(t.after || [])].map((d) => placed.get(d)?.x).filter((x) => x !== undefined);
        return xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : Infinity;
      };
      row.sort((a, b) => (bary(a.t) - bary(b.t)) || (a.i - b.i));
    }
    const x0 = Math.round((width - (row.length * W + (row.length - 1) * GX)) / 2);
    const y = PAD + r * (H + GY);
    row.forEach(({ t }, c) => {
      const node = { ref: t.ref, task: t, x: x0 + c * (W + GX), y, w: W, h: H, row: r };
      nodes.push(node);
      placed.set(t.ref, node);
    });
  });
  for (const n of nodes) {
    for (const dep of new Set(n.task.after || [])) {
      const from = placed.get(dep);
      if (from && from.row < n.row) {
        edges.push({ from: from.ref, to: n.ref, x1: from.x + W / 2, y1: from.y + H, x2: n.x + W / 2, y2: n.y });
      }
    }
  }
  return { nodes, edges, width, height, layers: rows.length };
}

function stageMark(dot, x, y) {
  if (dot === 'done') return `<path class="gmark done" d="M${x + 13} ${y + 44} l2 2 l4 -5" fill="none" stroke-width="1.5" aria-hidden="true"></path>`;
  if (dot === 'working' || dot === 'blocked') return `<circle class="gmark ${dot}" cx="${x + 16}" cy="${y + 43}" r="4" aria-hidden="true"></circle>`;
  return `<circle class="gmark idle" cx="${x + 16}" cy="${y + 43}" r="3.5" fill="none" stroke-width="1.5" aria-hidden="true"></circle>`;
}

export function renderGraphSVG(tasks = [], project = '') {
  const g = layout(tasks);
  const edgeHTML = g.edges.map((e) => {
    const my = Math.round((e.y1 + e.y2) / 2), attrs = `data-from="${esc(e.from)}" data-to="${esc(e.to)}" aria-hidden="true"`;
    return `<path class="gedge" ${attrs} d="M${e.x1} ${e.y1} C${e.x1} ${my} ${e.x2} ${my} ${e.x2} ${e.y2 - 5}"></path>` +
      `<path class="garrow" ${attrs} d="M${e.x2 - 4} ${e.y2 - 7} L${e.x2 + 4} ${e.y2 - 7} L${e.x2} ${e.y2} Z"></path>`;
  }).join('');
  const nodeHTML = g.nodes.map(({ task: t, x, y, w, h }) => {
    const [label, dot] = stageInfo(t.stage);
    const after = (t.after || []).length ? `, after ${t.after.join(' and ')}` : '';
    const aria = `${t.ref}, ${t.title}, ${label}${after}`;
    return `<a class="gnode" data-ref="${esc(t.ref)}" href="#${enc(project)}/${enc(t.ref)}" aria-label="${esc(aria)}">` +
      `<rect class="box" x="${x}" y="${y}" width="${w}" height="${h}" rx="10"></rect>` +
      `<text class="ref" x="${x + 10}" y="${y + 16}">${esc(t.ref)}</text>` +
      `<text x="${x + 10}" y="${y + 32}">${esc(clip(t.title || ''))}</text>` +
      `${stageMark(dot, x, y)}<text class="st" x="${x + 26}" y="${y + 47}">${esc(clip(label, 21))}</text></a>`;
  }).join('');
  return `<svg width="${g.width}" height="${g.height}" viewBox="0 0 ${g.width} ${g.height}" role="group" aria-label="task graph">${edgeHTML}${nodeHTML}</svg>`;
}

export function renderGraphSection(tasks, project) {
  const layers = new Set(tasks.map((t) => Number(t.layer) || 0)).size;
  const base = `task graph: ${tasks.length} tasks in ${layers} layer${layers === 1 ? '' : 's'}`;
  return `<p class="small muted graph-hint" data-ghint="1" hidden></p>` +
    `<div class="gbox" role="region" data-glabel="${esc(base)}" aria-label="${esc(base)}">${renderGraphSVG(tasks, project)}</div>`;
}

export function syncGraphBox(root) {
  const box = root?.querySelector('.gbox');
  if (!box) return;
  const scrolls = box.scrollWidth > box.clientWidth;
  box.setAttribute('aria-label', box.dataset.glabel + (scrolls ? '; scrolls sideways' : ''));
  if (scrolls) box.setAttribute('tabindex', '0'); else box.removeAttribute('tabindex');
  const hint = root.querySelector('[data-ghint]');
  if (hint) { hint.hidden = !scrolls; hint.textContent = scrolls ? GRAPH_SCROLL_HINT : ''; }
}

export function highlightGraph(svg, ref) {
  if (!svg) return;
  const keep = new Set(ref ? [ref] : []);
  for (const e of svg.querySelectorAll('path[data-from]')) {
    const f = e.dataset.from, t = e.dataset.to, hot = Boolean(ref) && (f === ref || t === ref);
    if (hot) { keep.add(f); keep.add(t); }
    const base = (e.getAttribute('class') || '').split(' ')[0];
    e.setAttribute('class', `${base}${hot ? ' hot' : ref ? ' dim' : ''}`);
  }
  for (const a of svg.querySelectorAll('a.gnode')) {
    const r = a.dataset.ref;
    a.setAttribute('class', `gnode${!ref ? '' : r === ref ? ' focus' : keep.has(r) ? ' hot' : ' dim'}`);
  }
}
