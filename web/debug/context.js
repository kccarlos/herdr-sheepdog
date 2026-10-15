// Context tab of /debug: the logic behind the two panes (the orchestrator's
// exact request on the left, the voice model's reconstructed context on
// the right; docs/dev/architecture.md "Context capture"). Pure functions over
// the /api/context/* responses, so node --test covers them; the DOM lives
// in context-view.js. Vanilla JS, no framework (decision D11).

/**
 * The section classes, in legend order, with their labels. The same class
 * is the same colour in both panes (style.css .cx-<class>).
 */
export const CLASSES = [
  { id: 'system', label: 'system / prompt' },
  { id: 'tools', label: 'tool declarations' },
  { id: 'meta', label: 'request settings' },
  { id: 'shepherd', label: 'user' },
  { id: 'audio', label: 'audio (no transcript)' },
  { id: 'voice', label: 'voice said' },
  { id: 'request', label: 'request to the orchestrator' },
  { id: 'brain', label: 'orchestrator' },
  { id: 'tool_call', label: 'tool call' },
  { id: 'tool_result', label: 'tool result' },
  { id: 'worker', label: 'worker' },
  { id: 'untrusted', label: 'worker output (untrusted)' },
  { id: 'push', label: 'push' },
  { id: 'compaction', label: 'compaction summary' },
  { id: 'pruned', label: 'pruned stub' },
  { id: 'marker', label: 'session marker' },
];

const CLASS_IDS = new Set(CLASSES.map((c) => c.id));

/** The css class of a section class (unknown ones fall back to meta). */
export function classOf(cls) {
  return `cx-${CLASS_IDS.has(cls) ? cls : 'meta'}`;
}

/** The orchestrator's collapsible groups, in order; system and tools start closed. */
const BRAIN_GROUPS = [
  { id: 'system', label: 'system instruction', collapsed: true },
  { id: 'tools', label: 'tool declarations', collapsed: true },
  { id: 'messages', label: 'messages', collapsed: false },
];

/**
 * The diff state of one section: added, changed, pruned or compacted
 * (a new compaction summary), from the served diff; '' when unchanged or
 * when there is no diff.
 */
export function diffState(key, diff) {
  if (!diff) return '';
  if ((diff.compacted || []).includes(key)) return 'compacted';
  if ((diff.pruned || []).includes(key)) return 'pruned';
  if ((diff.added || []).includes(key)) return 'added';
  if ((diff.changed || []).includes(key)) return 'changed';
  return '';
}

/**
 * The rendering model of one pane: its sections grouped into collapsible
 * blocks, each section with its css class, diff state and tokens, the
 * pieces the diff says are gone, and the totals. `pane` is 'orchestrator' or
 * 'voice'; `resp` the API response.
 */
export function paneModel(pane, resp) {
  const sections = (resp && resp.sections) || [];
  const diff = resp && resp.diff;
  const groups = [];
  const byId = new Map();
  const group = (id, label, collapsed) => {
    let g = byId.get(id);
    if (!g) {
      g = { id, label, collapsed, tokens: 0, sections: [] };
      byId.set(id, g);
      groups.push(g);
    }
    return g;
  };
  if (pane === 'brain') for (const g of BRAIN_GROUPS) group(g.id, g.label, g.collapsed);
  for (const s of sections) {
    const gid = s.group || (pane === 'brain' ? 'messages' : 'session');
    const g = byId.get(gid) || group(gid, pane === 'voice' ? `session ${gid}` : gid, false);
    const item = {
      key: s.key, label: s.label, cls: s.class, css: classOf(s.class), text: s.text || '', tokens: s.tokens || 0,
      links: s.links || [], path: s.path || '', raw: s.raw, role: s.role || '', pruned: !!s.pruned, current: !!s.current,
      seq: s.seq || 0, diff: diffState(s.key, diff),
      // A voice system prompt or tool list starts closed, like the orchestrator's.
      collapsed: pane === 'voice' && (s.class === 'system' || s.class === 'tools'),
    };
    g.sections.push(item);
    g.tokens += item.tokens;
  }
  const kept = groups.filter((g) => g.sections.length > 0);
  const total = kept.reduce((n, g) => n + g.tokens, 0);
  const removed = ((diff && diff.removed) || []).map((r) => ({ ...r, css: classOf(r.class) }));
  return { pane, groups: kept, removed, total, prev: diff ? diff.prev : 0 };
}

/** The raw text of the pane, as the model receives it ("Copy raw"). */
export function rawText(resp) {
  return ((resp && resp.sections) || []).map((s) => s.text || '').join('');
}

/**
 * The budget line of the orchestrator pane: the request's estimated tokens
 * against brain.window_tokens (which bounds the transcript window only:
 * the system prompt and tools come on top, so the messages are compared).
 */
export function budget(model, windowTokens) {
  const messages = (model.groups.find((g) => g.id === 'messages') || { tokens: 0 }).tokens;
  const pct = windowTokens > 0 ? Math.round((messages / windowTokens) * 100) : 0;
  return { total: model.total, messages, window: windowTokens || 0, pct, over: windowTokens > 0 && messages > windowTokens };
}

/** Link id → the keys of the sections carrying it. */
export function linkIndex(sections) {
  const idx = new Map();
  for (const s of sections || []) {
    for (const l of s.links || []) {
      if (!idx.has(l)) idx.set(l, []);
      idx.get(l).push(s.key);
    }
  }
  return idx;
}

/**
 * The keys in the other pane linked to a section: every section there
 * that shares one of its link ids (`links` of the hovered section, the
 * other pane's linkIndex).
 */
export function counterparts(links, otherIndex) {
  const out = new Set();
  for (const l of links || []) for (const k of otherIndex.get(l) || []) out.add(k);
  return [...out];
}

/**
 * The scrubber's points: every orchestrator step and voice turn of the
 * timeline, in time order (an orchestrator step before a voice turn at the same
 * instant), each {pane, id, ts, label}.
 */
export function interleave(timeline) {
  const pts = [];
  for (const s of (timeline && timeline.brain) || []) {
    pts.push({ pane: 'brain', id: s.step, ts: s.ts, t: Date.parse(s.ts), label: `orchestrator step ${s.step} · turn ${s.turn}.${s.turn_step} ${s.kind}` });
  }
  for (const v of (timeline && timeline.voice) || []) {
    pts.push({ pane: 'voice', id: v.turn, ts: v.ts, t: Date.parse(v.ts), label: `voice turn ${v.turn} · ${v.label}` });
  }
  pts.sort((a, b) => (a.t - b.t) || (a.pane === b.pane ? a.id - b.id : (a.pane === 'brain' ? -1 : 1)));
  return pts;
}

/**
 * What each pane shows at scrubber position i: the newest orchestrator step and
 * the newest voice turn at or before it (0 when there is none yet).
 */
export function pointsAt(points, i) {
  let brain = 0;
  let voice = 0;
  for (let j = 0; j <= i && j < points.length; j++) {
    if (points[j].pane === 'brain') brain = points[j].id;
    else voice = points[j].id;
  }
  return { brain, voice };
}

/**
 * The piece of a request body at path ("contents[3].parts[0]",
 * "config.systemInstruction", "messages[5].tool_calls[1]"), for a
 * section's raw JSON; undefined when the path does not resolve.
 */
export function resolvePath(body, path) {
  if (!path) return undefined;
  let cur = body;
  for (const tok of path.match(/[^.[\]]+/g) || []) {
    if (cur == null) return undefined;
    cur = /^\d+$/.test(tok) && Array.isArray(cur) ? cur[Number(tok)] : cur[tok];
  }
  return cur;
}

/** The first non-empty line of a section's text past its role header (a collapsed or removed piece's summary). */
export function firstLine(text) {
  const lines = (text || '').split('\n').filter((l) => l.trim() !== '');
  // Skip the role header a piece may open with ("### user", "── user · … ──").
  const body = lines.find((l) => !/^(### |── )/.test(l));
  return body || lines[0] || '';
}
