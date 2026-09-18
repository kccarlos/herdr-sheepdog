// Swimlane tab of /debug: renders the rows from swimlane.js as a sequence
// diagram — one CSS grid row per event, a card in the event's lane and,
// under it, an arrow to the lane it talks to. Vanilla DOM, no framework
// (decision D11). Interactions: card click expands the raw JSON in place;
// arrow (or tool-call card) click highlights its chain and dims the rest.
import { buildRows, LANES } from './swimlane.js';
import { Dedupe, fmtMs } from './grouping.js';
import { el, jsonPre } from './dom.js';

/** Events kept in the view (the backfill is the same size). */
const MAX_EVENTS = 2000;

export class SwimlaneView {
  constructor(root, header) {
    this.root = root; // the scrolling container
    this.header = header; // the lane header strip (sticky, outside the scroller)
    this.events = [];
    this.seen = new Dedupe();
    this.opts = { hiddenLanes: new Set(), hiddenSources: new Set(), pane: '', search: '', hideStats: true, hideSilent: true };
    this.expanded = new Set(); // row keys open to their JSON
    this.highlight = null; // chain id
    this.dirty = false;
    this.rendered = 0;
    this.lastBuild = null;
    this.onNewEvents = () => {};
    this.onSelect = () => {}; // (ts) when a card is clicked: shared time selection
    this.onHighlight = () => {}; // (chainId|null)
    root.addEventListener('scroll', () => {
      if (this.atBottom()) this.onNewEvents(false);
    });
  }

  /** Feeds one log event; duplicates (same encoded line) are ignored. */
  add(ev, encoded) {
    if (!this.seen.add(encoded || JSON.stringify(ev))) return false;
    this.events.push(ev);
    if (this.events.length > MAX_EVENTS * 1.25) this.events = this.events.slice(-MAX_EVENTS);
    this.dirty = true;
    return true;
  }

  /** Drops every event (and the dedupe memory, so the same lines can be fed again). */
  clear() {
    this.events = [];
    this.seen = new Dedupe();
    this.rendered = 0;
    this.expanded.clear();
    this.highlight = null;
    this.dirty = true;
  }

  setOption(name, value) {
    this.opts[name] = value;
    this.render(true);
  }

  toggleLane(id, shown) {
    if (shown) this.opts.hiddenLanes.delete(id); else this.opts.hiddenLanes.add(id);
    this.render(true);
  }

  setHighlight(chain) {
    this.highlight = this.highlight === chain ? null : chain;
    this.onHighlight(this.highlight);
    this.applyHighlight();
  }

  /** Dims rows outside the highlighted chain without a rebuild. */
  applyHighlight() {
    const on = this.highlight;
    this.root.classList.toggle('sw-has-highlight', !!on);
    for (const row of this.root.children) {
      if (!row.dataset.chain && row.classList.contains('sw-gap')) continue;
      row.classList.toggle('sw-dim', !!on && row.dataset.chain !== on);
      row.classList.toggle('sw-lit', !!on && row.dataset.chain === on);
    }
  }

  toggleExpanded(key) {
    if (this.expanded.has(key)) this.expanded.delete(key); else this.expanded.add(key);
    this.render(true);
  }

  atBottom() {
    const r = this.root;
    return r.scrollTop + r.clientHeight >= r.scrollHeight - 40;
  }

  scrollToBottom() {
    this.root.scrollTop = this.root.scrollHeight;
    this.onNewEvents(false);
  }

  /** Scrolls to the first row at or after `ts` and flashes it. */
  scrollToTs(ts) {
    const t = Date.parse(ts);
    if (Number.isNaN(t)) return false;
    for (const row of this.root.children) {
      if (row.dataset.tms && Number(row.dataset.tms) >= t) {
        row.scrollIntoView({ block: 'center' });
        row.classList.add('sw-flash');
        setTimeout(() => row.classList.remove('sw-flash'), 1600);
        return true;
      }
    }
    return false;
  }

  /** Rebuilds the DOM when something changed (or when forced). */
  render(force) {
    if (!this.dirty && !force) return;
    this.dirty = false;
    const wasAtBottom = this.atBottom() || this.rendered === 0;
    const keepTop = this.root.scrollTop;
    const events = this.events.slice(-MAX_EVENTS);
    const built = buildRows(events, this.opts);
    this.lastBuild = built;
    const lanes = built.lanes;
    // One grid template for the header and every row: the time gutter
    // plus one equal column per visible lane.
    const cols = `var(--sw-gutter) repeat(${Math.max(lanes.length, 1)}, minmax(0, 1fr))`;
    this.root.style.setProperty('--sw-cols', cols);
    this.header.style.setProperty('--sw-cols', cols);
    this.renderHeader(lanes);

    const byKey = new Map();
    for (const r of built.rows) if (r.kind === 'card') byKey.set(r.key, r);
    const frag = document.createDocumentFragment();
    for (const r of built.rows) {
      if (r.kind === 'card' && r.parentKey) r.parent = byKey.get(r.parentKey) || null;
      const node = r.kind === 'gap' ? this.renderGap(r) : (r.kind === 'divider' ? this.renderDivider(r) : this.renderCard(r, lanes));
      frag.appendChild(node);
    }
    if (built.rows.length === 0) {
      frag.appendChild(el('p', 'muted empty', events.length ? 'Every event is filtered out.' : 'No events yet: connect /voice, or send an utterance below.'));
    }
    this.root.replaceChildren(frag);
    this.applyHighlight();
    const grew = events.length > this.rendered;
    this.rendered = events.length;
    if (wasAtBottom) this.root.scrollTop = this.root.scrollHeight;
    else {
      this.root.scrollTop = keepTop;
      if (grew) this.onNewEvents(true);
    }
    if (this.atBottom()) this.onNewEvents(false);
    return built;
  }

  renderHeader(lanes) {
    const frag = document.createDocumentFragment();
    frag.appendChild(el('div', 'sw-gutter-head', 'time'));
    for (const id of lanes) {
      const lane = LANES.find((l) => l.id === id);
      frag.appendChild(el('div', `sw-lane-head lane-${id}`, lane ? lane.label : id));
    }
    this.header.replaceChildren(frag);
  }

  renderGap(r) {
    const row = el('div', 'sw-row sw-gap');
    row.dataset.key = r.key;
    const g = el('div', 'sw-gap-marker');
    g.appendChild(el('span', 'sw-gap-line'));
    g.appendChild(el('span', 'sw-gap-text', `${r.label} later`));
    g.appendChild(el('span', 'sw-gap-line'));
    row.appendChild(g);
    return row;
  }

  gutter(r) {
    const g = el('div', `sw-time${r.minuteChanged ? ' minute' : ''}`);
    // Only the seconds unless the minute changed; the full clock on hover.
    g.textContent = r.minuteChanged ? r.time : r.time.slice(6);
    g.title = r.time;
    return g;
  }

  renderDivider(r) {
    const row = el('div', `sw-row sw-divider-row`);
    row.dataset.key = r.key;
    row.dataset.tms = r.tms;
    row.appendChild(this.gutter(r));
    const open = this.expanded.has(r.key);
    const d = el('div', `sw-divider ${r.cls || ''}${open ? ' open' : ''}`);
    d.appendChild(el('span', 'line'));
    const t = el('span', 'text');
    t.appendChild(el('span', 'chev', open ? '▾' : '▸'));
    t.appendChild(el('b', null, r.label));
    if (r.detail) t.appendChild(el('span', 'detail', ` · ${r.detail}`));
    d.appendChild(t);
    d.appendChild(el('span', 'line'));
    d.addEventListener('click', () => { this.toggleExpanded(r.key); this.onSelect(r.ts); });
    row.appendChild(d);
    if (open) {
      const body = el('div', 'sw-divider-body');
      if (r.text) body.appendChild(el('pre', 'prompt-text', r.text));
      body.appendChild(jsonPre(r.event, 'sw-json'));
      row.appendChild(body);
    }
    return row;
  }

  renderCard(r, lanes) {
    const row = el('div', `sw-row lane-${r.lane}${r.paneHit ? ' pane-hit' : ''}`);
    row.dataset.key = r.key;
    row.dataset.tms = r.tms;
    if (r.chain) row.dataset.chain = r.chain;
    row.appendChild(this.gutter(r));
    const open = this.expanded.has(r.key);
    if (!r.laneHidden) {
      const card = el('div', `sw-card ${r.cls.join(' ')}${open ? ' open' : ''}${r.chainRoot ? ' root' : ''}${r.parentKey ? ' nested' : ''}`);
      card.style.gridColumn = String(r.col + 2); // 1 = gutter
      const head = el('div', 'sw-head');
      head.appendChild(el('span', 'chev', open ? '▾' : '▸'));
      head.appendChild(el('span', 'sw-lane-badge', LANES.find((l) => l.id === r.lane).label));
      if (r.parentKey) {
        // Nested under an inner (brain.*) call: "↳ brain read_screen".
        const n = el('span', 'sw-nest', `↳ ${r.parent ? r.parent.title.replace(/^⚙ /, '') : 'call'}`);
        n.title = `ran inside ${r.parent ? r.parent.title : r.parentKey}`;
        head.appendChild(n);
      }
      head.appendChild(el('span', 'sw-title', r.title));
      if (r.latencyMs != null && r.cls.includes('inner')) head.appendChild(el('span', `sw-lat lvl-${r.latencyLevel || 'ok'}`, `⏱ ${r.durMs != null ? '' : 'Δ '}${fmtMs(r.latencyMs)}`));
      if (r.state) head.appendChild(el('span', `pill ${r.state}`, r.state));
      if (r.urgency) head.appendChild(el('span', `badge ${r.urgency}`, r.urgency));
      if (r.skipped) head.appendChild(el('span', 'badge skipped', 'not sent'));
      if (r.orphan) head.appendChild(el('span', 'badge', 'no call'));
      if (r.typed) head.appendChild(el('span', 'sw-meta', 'typed'));
      if (r.merged && r.fragments > 1) head.appendChild(el('span', 'sw-meta', `${r.fragments} fragments`));
      if (r.merged && !r.final) head.appendChild(el('span', 'sw-meta', '…'));
      if (r.pane) head.appendChild(el('span', 'sw-meta sw-pane', r.pane));
      if (r.chain) {
        const b = el('button', 'sw-chain-btn', '🔗');
        b.title = `highlight chain ${r.chain}`;
        b.addEventListener('click', (e) => { e.stopPropagation(); this.setHighlight(r.chain); });
        head.appendChild(b);
      }
      card.appendChild(head);
      const text = r.text != null ? r.text : (r.summary || '');
      if (text) card.appendChild(el('div', `sw-text${open ? '' : ' clamp'}`, text));
      if (r.latency) {
        // voice.turn: input → first model reaction, red from STALL_MS.
        card.appendChild(el('div', `sw-latency${r.latency.slow ? ' slow' : ''}`, `⏱ ${r.latency.label}`));
      }
      card.addEventListener('click', (e) => {
        if (e.target.closest('button, a')) return;
        const opening = !this.expanded.has(r.key);
        // A tool call is the natural root of its chain: opening it lights
        // the chain up, collapsing it clears it (setHighlight toggles).
        if (r.cls.includes('tool') && r.chain && (this.highlight === r.chain) !== opening) this.setHighlight(r.chain);
        this.toggleExpanded(r.key);
        this.onSelect(r.ts);
      });
      row.appendChild(card);
    }
    const a = r.arrowOut;
    if (a) row.appendChild(this.renderArrow(a, r, lanes));
    // The raw JSON spans every lane on a third grid row so a narrow lane
    // does not turn one expanded card into a column of text.
    if (open && !r.laneHidden) row.appendChild(jsonPre(r.events || r.event, 'sw-json sw-json-row'));
    return row;
  }

  // renderArrow draws the arrow of a card on the grid row under it,
  // spanning from the centre of its source column to the centre of the
  // target column. Lane columns are equal, so the centre of the first
  // spanned column sits at 50%/span of the element's width (see the
  // --span custom property in style.css); a stub spans one column and
  // runs from its centre to the edge on the hidden lane's side.
  renderArrow(a, r, lanes) {
    const lo = Math.min(a.fromCol, a.toCol);
    const hi = Math.max(a.fromCol, a.toCol);
    const span = hi - lo + 1;
    const cls = ['sw-arrow', `kind-${a.kind}`, `dir-${a.dir}`, `from-${a.from}`];
    if (a.dashed) cls.push('dashed');
    if (a.level) cls.push(`lvl-${a.level}`);
    if (a.stub) cls.push('stub', `stub-${a.stubSide}`);
    if (a.state) cls.push(`state-${a.state}`);
    const arrow = el('div', cls.join(' '));
    arrow.style.gridColumn = `${lo + 2} / ${hi + 3}`;
    arrow.style.setProperty('--span', String(span));
    arrow.appendChild(el('span', 'sw-line'));
    arrow.appendChild(el('span', 'sw-head-mark'));
    const label = el('span', 'sw-label', a.label || '');
    const to = LANES.find((l) => l.id === a.to);
    const from = LANES.find((l) => l.id === a.from);
    label.title = `${from ? from.label : a.from} → ${to ? to.label : a.to}${a.stub ? ' (lane hidden)' : ''}`;
    if (!lanes.includes(a.to) || !lanes.includes(a.from)) label.classList.add('stubbed');
    // Phone width shows the arrow as a text line: "→ Supervisor · label".
    const narrow = el('span', 'sw-narrow', `${a.dir === 'right' ? '→' : '←'} ${to ? to.label : a.to} · `);
    label.prepend(narrow);
    arrow.appendChild(label);
    if (r.chain) {
      arrow.classList.add('chained');
      arrow.addEventListener('click', (e) => { e.stopPropagation(); this.setHighlight(r.chain); });
    }
    return arrow;
  }
}
