// Context tab of /debug: the brain's exact request (left) and the voice
// model's reconstructed context (right), side by side, as each model
// receives it, coloured by section, linked across the panes, with a
// scrubber over every brain step and voice turn and a diff against the
// previous point. The logic is context.js; this file only builds DOM.
// Vanilla DOM, no framework (decision D11). Nothing is fetched or
// rendered while the tab is hidden: context.step events only mark it
// stale.
import { CLASSES, paneModel, rawText, budget, linkIndex, counterparts, interleave, pointsAt, resolvePath, firstLine } from './context.js';
import { fmtTokens } from './supervisor.js';
import { el, fmtTime } from './dom.js';

const PANES = {
  brain: { title: 'Brain (supervisor)', param: 'step', url: '/api/context/brain' },
  voice: { title: 'Voice model (assistant)', param: 'turn', url: '/api/context/voice' },
};

/** A small badge. */
function badge(text, cls, title) {
  const b = el('span', `badge cx-badge ${cls || ''}`, text);
  if (title) b.title = title;
  return b;
}

export class ContextView {
  /**
   * root: the tab's container; getJSON(url) → {status, data}.
   */
  constructor(root, getJSON) {
    this.root = root;
    this.getJSON = getJSON;
    this.timeline = null;
    this.points = [];
    this.pos = -1;
    this.live = true;
    this.diffMode = true;
    this.visible = false;
    this.stale = true;
    this.timer = 0;
    this.loading = false;
    this.data = { brain: null, voice: null };
    this.errors = { brain: '', voice: '' };
    this.shown = { brain: -1, voice: -1 };
    // Per pane: group/section keys toggled away from their default, and
    // the raw JSON blocks open.
    this.toggled = { brain: new Set(), voice: new Set() };
    this.rawOpen = new Set();
    this.bodyOpen = { brain: false, voice: false };
    this.selected = null; // {pane, key}
    this.nodes = { brain: new Map(), voice: new Map() }; // key → [section elements]
    this.index = { brain: new Map(), voice: new Map() }; // link → keys
    this.build();
  }

  // ------------------------------------------------------------- layout

  build() {
    const bar = el('div', 'toolbar cx-toolbar');
    this.prevBtn = el('button', 'icon', '◀');
    this.prevBtn.title = 'the previous brain step or voice turn';
    this.nextBtn = el('button', 'icon', '▶');
    this.nextBtn.title = 'the next brain step or voice turn';
    this.slider = el('input', 'cx-slider');
    this.slider.type = 'range';
    this.slider.min = '0';
    this.slider.max = '0';
    this.slider.title = 'every brain step (model call) and voice turn, in time order';
    const liveLbl = el('label', 'cx-live');
    this.liveBox = el('input');
    this.liveBox.type = 'checkbox';
    this.liveBox.checked = true;
    liveLbl.append(this.liveBox, ' live');
    liveLbl.title = 'follow the newest step and turn as they are recorded';
    const diffLbl = el('label');
    this.diffBox = el('input');
    this.diffBox.type = 'checkbox';
    this.diffBox.checked = true;
    diffLbl.append(this.diffBox, ' diff');
    diffLbl.title = 'mark what changed against the previous point of each pane: added (green edge), changed (amber edge), pruned to a stub, compacted into a summary, removed (struck out)';
    this.pointLabel = el('span', 'cx-point muted');
    const wrap = el('div', 'cx-scrub');
    this.ticks = el('div', 'cx-ticks');
    wrap.append(this.ticks, this.slider);
    bar.append(this.prevBtn, wrap, this.nextBtn, liveLbl, diffLbl, this.pointLabel);

    const legend = el('div', 'cx-legend');
    for (const c of CLASSES) {
      const chip = el('span', `cx-chip cx-${c.id}`);
      chip.append(el('span', 'swatch'), c.label);
      legend.appendChild(chip);
    }
    legend.append(el('span', 'cx-chip cx-diff-added', 'added'), el('span', 'cx-chip cx-diff-changed', 'changed'));

    const panes = el('div', 'cx-panes');
    this.panes = {};
    for (const [name, p] of Object.entries(PANES)) {
      const pane = el('section', `cx-pane cx-pane-${name}`);
      const head = el('div', 'cx-pane-head');
      const title = el('div', 'cx-title');
      title.append(el('b', '', p.title));
      const source = el('span', 'cx-source');
      title.appendChild(source);
      const copy = el('button', 'small', 'Copy raw');
      copy.title = 'copy the raw text of this context, as the model receives it';
      const body = el('button', 'small', name === 'brain' ? 'request JSON' : 'items JSON');
      body.title = name === 'brain' ? 'the provider-native request body' : 'every item with its outbound message';
      title.append(copy, body);
      const meta = el('div', 'cx-meta muted small');
      const tokens = el('div', 'cx-tokens small');
      head.append(title, meta, tokens);
      const scroller = el('div', 'cx-body');
      pane.append(head, scroller);
      panes.appendChild(pane);
      this.panes[name] = { pane, source, meta, tokens, scroller, copy, body };
      copy.addEventListener('click', () => this.copy(name));
      body.addEventListener('click', () => { this.bodyOpen[name] = !this.bodyOpen[name]; this.renderPane(name); });
    }
    this.root.replaceChildren(bar, legend, panes);

    this.slider.addEventListener('input', () => this.seek(Number(this.slider.value)));
    this.prevBtn.addEventListener('click', () => this.seek(this.pos - 1));
    this.nextBtn.addEventListener('click', () => this.seek(this.pos + 1));
    this.liveBox.addEventListener('change', () => {
      this.live = this.liveBox.checked;
      if (this.live) this.refresh();
    });
    this.diffBox.addEventListener('change', () => {
      this.diffMode = this.diffBox.checked;
      this.renderPane('brain');
      this.renderPane('voice');
    });
  }

  // ------------------------------------------------------------ control

  /** The tab became visible: catch up. */
  show() {
    this.visible = true;
    if (this.stale) this.refresh();
  }

  hide() { this.visible = false; }

  /** Log events: context.step (and a restart or reset) mean new points. */
  onEvent(ev) {
    const t = ev && ev.type;
    if (t !== 'context.step' && t !== 'daemon.start' && t !== 'brain.reset') return;
    this.stale = true;
    if (!this.visible || !this.live) {
      if (this.visible) this.scheduleTimeline();
      return;
    }
    if (this.timer) return;
    this.timer = setTimeout(() => { this.timer = 0; this.refresh(); }, 250);
  }

  /** Scrubbing (not live): only the slider's range grows. */
  scheduleTimeline() {
    if (this.timer) return;
    this.timer = setTimeout(async () => {
      this.timer = 0;
      await this.fetchTimeline();
      this.renderScrubber();
    }, 500);
  }

  async fetchTimeline() {
    try {
      const { status, data } = await this.getJSON('/api/context/timeline');
      if (status !== 200) throw new Error(data.error || String(status));
      this.timeline = data;
      this.points = interleave(data);
    } catch (err) {
      this.errors.brain = this.errors.voice = `timeline: ${err.message || err}`;
    }
  }

  async refresh() {
    if (this.loading) { this.stale = true; return; }
    this.loading = true;
    this.stale = false;
    try {
      await this.fetchTimeline();
      if (this.live || this.pos < 0 || this.pos >= this.points.length) this.pos = this.points.length - 1;
      await this.load(true);
    } finally {
      this.loading = false;
    }
    if (this.stale && this.live && this.visible) this.onEvent({ type: 'context.step' });
  }

  /** Moves the scrubber to point i (leaving live mode unless it is the newest). */
  seek(i) {
    if (!this.points.length) return;
    this.pos = Math.max(0, Math.min(this.points.length - 1, i));
    this.live = this.pos === this.points.length - 1 && this.live;
    this.liveBox.checked = this.live;
    this.load(false);
  }

  /** Fetches what each pane shows at the scrubber's position. */
  async load(force) {
    const at = pointsAt(this.points, this.pos);
    await Promise.all(Object.keys(PANES).map(async (name) => {
      const id = at[name];
      if (!force && id === this.shown[name]) return;
      this.shown[name] = id;
      if (!id) {
        this.data[name] = null;
        const off = name === 'brain' ? this.timeline && this.timeline.brain_off : this.timeline && this.timeline.voice_off;
        this.errors[name] = off ? `off (${off})` : (this.pos < 0 ? 'nothing recorded yet' : 'nothing recorded at this point yet');
        return;
      }
      try {
        const { status, data } = await this.getJSON(`${PANES[name].url}?${PANES[name].param}=${id}`);
        if (status !== 200) throw new Error(data.error || String(status));
        this.data[name] = data;
        this.errors[name] = '';
      } catch (err) {
        this.data[name] = null;
        this.errors[name] = String(err.message || err);
      }
    }));
    this.renderScrubber();
    this.renderPane('brain');
    this.renderPane('voice');
  }

  async copy(name) {
    const text = rawText(this.data[name]);
    const btn = this.panes[name].copy;
    try {
      await navigator.clipboard.writeText(text);
      btn.textContent = 'Copied';
    } catch (_) {
      btn.textContent = 'Copy failed';
    }
    setTimeout(() => { btn.textContent = 'Copy raw'; }, 1200);
  }

  // ------------------------------------------------------------ render

  renderScrubber() {
    const n = this.points.length;
    this.slider.max = String(Math.max(0, n - 1));
    this.slider.value = String(Math.max(0, this.pos));
    this.slider.disabled = n === 0;
    this.prevBtn.disabled = this.pos <= 0;
    this.nextBtn.disabled = this.pos >= n - 1;
    const p = this.points[this.pos];
    this.pointLabel.textContent = p ? `${this.pos + 1}/${n} · ${fmtTime(p.ts)} · ${p.label}` : 'no brain step or voice turn yet';
    // One tick per point, coloured by pane.
    const frag = document.createDocumentFragment();
    this.points.forEach((pt, i) => {
      const t = el('span', `cx-tick cx-tick-${pt.pane}${i === this.pos ? ' at' : ''}`);
      t.style.left = n > 1 ? `${(i / (n - 1)) * 100}%` : '50%';
      frag.appendChild(t);
    });
    this.ticks.replaceChildren(frag);
  }

  isOpen(name, key, def) {
    return this.toggled[name].has(key) ? !def : def;
  }

  toggle(name, key) {
    const s = this.toggled[name];
    if (s.has(key)) s.delete(key); else s.add(key);
    this.renderPane(name);
  }

  renderPane(name) {
    const p = this.panes[name];
    const data = this.data[name];
    const scroller = p.scroller;
    const atBottom = scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 40;
    const keepTop = scroller.scrollTop;
    this.nodes[name] = new Map();
    this.index[name] = linkIndex(data ? data.sections : []);
    p.source.replaceChildren();
    p.meta.textContent = '';
    p.tokens.replaceChildren();
    if (!data) {
      scroller.replaceChildren(el('div', 'cx-empty muted', this.errors[name] || 'loading…'));
      return;
    }
    p.source.appendChild(badge(data.source, name === 'brain' ? 'cx-exact' : 'cx-reconstructed',
      name === 'brain' ? 'the body the daemon built for the provider (Gemini: the SDK serialises it; DeepSeek: the exact JSON sent)' : 'Live models keep their context server-side and it cannot be read back: this is what the daemon sent, in order'));
    const model = paneModel(name, data);
    if (name === 'brain') {
      const s = data.step;
      p.meta.textContent = `step ${s.step} · turn ${s.turn}.${s.turn_step} ${s.kind}${s.agent ? ` (${s.agent})` : ''} · ${s.provider}/${s.model} · ${fmtTime(s.ts)} · ${s.format} body${s.forced ? ' · answer-only' : ''}${s.error ? ` · error: ${s.error}` : ''}`;
      const b = budget(model, s.window_tokens);
      const bar = el('span', `cx-budget${b.over ? ' over' : ''}`);
      bar.title = 'brain.window_tokens bounds the transcript window (the messages); the system prompt and tools come on top';
      const fill = el('span', 'cx-budget-fill');
      fill.style.width = `${Math.min(100, b.pct)}%`;
      bar.appendChild(fill);
      p.tokens.append(`${fmtTokens(b.total)} tokens (est.) · messages ${fmtTokens(b.messages)} / ${fmtTokens(b.window)} window `, bar, ` ${b.pct}%`);
      if (s.usage) p.tokens.append(el('span', 'muted', ` · provider counted ${fmtTokens(s.usage.prompt_tokens)} prompt${s.usage.cached_tokens ? ` (${fmtTokens(s.usage.cached_tokens)} cached)` : ''}`));
    } else {
      const t = data.turn;
      p.meta.textContent = `turn ${t.turn} · ${t.label} · ${t.session} · context #${t.epoch} · ${fmtTime(t.ts)}${data.evicted ? ' · older items evicted' : ''}`;
      p.tokens.append(`${fmtTokens(model.total)} tokens (est. of the text sent; audio is not counted)`);
    }

    const frag = document.createDocumentFragment();
    if (this.bodyOpen[name]) {
      const body = name === 'brain' ? data.body : data.sections.map((s) => ({ key: s.key, label: s.label, links: s.links, raw: s.raw }));
      frag.appendChild(el('pre', 'cx-raw cx-raw-body', JSON.stringify(body, null, 2)));
    }
    for (const g of model.groups) {
      const open = name === 'brain' ? this.isOpen(name, `group:${g.id}`, !g.collapsed) : true;
      if (name === 'brain' || model.groups.length > 1) {
        const head = el('div', `cx-group-head${open ? ' open' : ''}`);
        head.append(el('span', 'chev', open ? '▾' : '▸'), el('b', '', g.label), el('span', 'muted', ` · ${g.sections.length} piece${g.sections.length === 1 ? '' : 's'} · ${fmtTokens(g.tokens)} tok`));
        if (name === 'brain') head.addEventListener('click', () => this.toggle(name, `group:${g.id}`));
        frag.appendChild(head);
      }
      if (!open) continue;
      for (const s of g.sections) frag.appendChild(this.sectionEl(name, s, data));
    }
    if (this.diffMode && model.removed.length) {
      const head = el('div', 'cx-group-head cx-removed-head');
      head.append(el('b', '', `removed since point ${model.prev}`), el('span', 'muted', ` · ${model.removed.length} piece${model.removed.length === 1 ? '' : 's'}`));
      frag.appendChild(head);
      for (const r of model.removed) {
        const row = el('div', `cx-sec cx-removed ${r.css}`);
        const gut = el('div', 'cx-gutter');
        gut.append(el('span', 'cx-name', r.label), badge(r.reason, `cx-reason-${r.reason}`, r.reason === 'compacted' ? 'replaced by the compaction summary' : r.reason === 'reset' ? 'dropped by a reset / a fresh session' : 'no longer in the context'));
        row.append(gut, el('pre', 'cx-text', firstLine(r.text)));
        frag.appendChild(row);
      }
    }
    scroller.replaceChildren(frag);
    if (this.live && atBottom) scroller.scrollTop = scroller.scrollHeight;
    else scroller.scrollTop = keepTop;
    if (this.selected) this.highlight(this.selected.pane, this.selected.key, false, 'cx-selected');
  }

  sectionEl(name, s, data) {
    const def = !s.collapsed;
    const open = this.isOpen(name, s.key, def);
    const row = el('div', `cx-sec ${s.css}${this.diffMode && s.diff ? ` cx-diff-${s.diff}` : ''}${s.current ? ' cx-current' : ''}${open ? '' : ' closed'}`);
    row.dataset.key = s.key;
    const gut = el('div', 'cx-gutter');
    const chev = el('span', 'chev', open ? '▾' : '▸');
    gut.append(chev, el('span', 'cx-name', s.label), el('span', 'cx-tok muted', `${fmtTokens(s.tokens)}`));
    if (s.current) gut.appendChild(badge('current', 'cx-b-current', "this step's turn"));
    if (s.pruned) gut.appendChild(badge('stub', 'cx-b-pruned', 'the window holds only this stub of the tool result'));
    if (this.diffMode && s.diff === 'compacted') gut.appendChild(badge('summary', 'cx-b-summary', 'a compaction summary that replaced older messages'));
    if (this.diffMode && s.diff === 'pruned') gut.appendChild(badge('pruned now', 'cx-b-pruned'));
    if (s.links.length) gut.appendChild(badge('⇄', 'cx-b-link', `linked: ${s.links.join(', ')}`));
    const rawBtn = el('button', `raw-btn${this.rawOpen.has(`${name}:${s.key}`) ? ' open' : ''}`, '{ }');
    rawBtn.title = name === 'brain' ? `raw JSON at ${s.path || '(no path)'}` : 'the outbound message, raw';
    gut.appendChild(rawBtn);
    const text = el('pre', 'cx-text', open ? s.text : firstLine(s.text));
    row.append(gut, text);
    if (this.rawOpen.has(`${name}:${s.key}`)) {
      const raw = name === 'brain' ? resolvePath(data.body, s.path) : s.raw;
      row.appendChild(el('pre', 'cx-raw', raw === undefined ? '(no raw JSON of its own)' : JSON.stringify(raw, null, 2)));
    }
    gut.addEventListener('click', (e) => {
      if (e.target === rawBtn) {
        const k = `${name}:${s.key}`;
        if (this.rawOpen.has(k)) this.rawOpen.delete(k); else this.rawOpen.add(k);
        this.renderPane(name);
        return;
      }
      this.toggle(name, s.key);
    });
    text.addEventListener('click', () => this.select(name, s.key));
    row.addEventListener('mouseenter', () => this.highlight(name, s.key, true, 'cx-hover'));
    row.addEventListener('mouseleave', () => this.clear('cx-hover'));
    const list = this.nodes[name].get(s.key) || [];
    list.push(row);
    this.nodes[name].set(s.key, list);
    return row;
  }

  // ------------------------------------------------------------ linking

  /** The links of a section key in a pane. */
  linksOf(name, key) {
    const out = [];
    for (const [l, keys] of this.index[name]) if (keys.includes(key)) out.push(l);
    return out;
  }

  /**
   * Marks a piece and its counterparts in the other pane (every piece
   * sharing a link id) with cls, scrolling the first counterpart into
   * view when scroll is set.
   */
  highlight(name, key, scroll, cls) {
    this.clear(cls);
    const other = name === 'brain' ? 'voice' : 'brain';
    const links = this.linksOf(name, key);
    for (const n of this.nodes[name].get(key) || []) n.classList.add(cls);
    if (!links.length) return;
    // Its own pane's pieces with the same link (a request and its reply).
    for (const k of counterparts(links, this.index[name])) {
      if (k === key) continue;
      for (const n of this.nodes[name].get(k) || []) n.classList.add(`${cls}-peer`);
    }
    let first = null;
    for (const k of counterparts(links, this.index[other])) {
      for (const n of this.nodes[other].get(k) || []) {
        n.classList.add(`${cls}-linked`);
        if (!first) first = n;
      }
    }
    if (scroll && first) first.scrollIntoView({ block: cls === 'cx-selected' ? 'center' : 'nearest', behavior: 'smooth' });
  }

  clear(cls) {
    for (const n of this.root.querySelectorAll(`.${cls}, .${cls}-linked, .${cls}-peer`)) n.classList.remove(cls, `${cls}-linked`, `${cls}-peer`);
  }

  /** Click: a sticky selection (click again to clear). */
  select(name, key) {
    if (this.selected && this.selected.pane === name && this.selected.key === key) {
      this.selected = null;
      this.clear('cx-selected');
      return;
    }
    this.selected = { pane: name, key };
    this.highlight(name, key, true, 'cx-selected');
  }
}
