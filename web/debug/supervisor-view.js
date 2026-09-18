// Supervisor tab of /debug: the SVP's transcript as a chat (the CEO's
// words right, the assistant's replies muted left, the SVP's replies
// left in its own hue, tool calls collapsed between them, pushes, worker
// events, the window marker and per-turn headers) under the prompt-budget
// meter (the /context map). Renders the items of supervisor.js; the
// fetching lives in app.js. Vanilla DOM, no framework (decision D11).
import { buildTranscriptView, meterSegments, TurnLog, fmtTokens } from './supervisor.js';
import { fmtMs } from './grouping.js';
import { el, fmtTime, jsonPre, Openable, Follow } from './dom.js';

const MAX_ITEMS = 1500;

/** Prompt sources and transcript kinds → the meter's css classes. */
const SOURCE_CLS = { embedded: 'src-embedded', override: 'src-override', dynamic: 'src-dynamic' };

export class SupervisorView {
  constructor(root, meter) {
    this.root = root; // the transcript scroller
    this.meter = meter; // the meter + sections container above it
    this.messages = new Map(); // seq → API message
    this.context = null; // /api/brain/context
    this.prompt = null; // /api/brain/system_prompt (sections with text), fetched on demand
    this.turns = new TurnLog(); // the live run's brain.* events, fed whatever mode
    this.open = new Openable(() => this.render(true));
    this.follow = new Follow(root);
    this.filters = { hideWorker: false, hideAssistant: false, windowOnly: false };
    this.off = null; // {reason, message} when the brain is off
    this.error = ''; // last fetch error
    this.fixture = false; // a loaded transcript replaced the live one (setMessages)
    this.loading = null;
    this.dirty = false;
    this.meterDirty = false;
    this.onNeedPrompt = () => {}; // asked once when a section is opened without its text
  }

  set onNewMessages(fn) { this.follow.onNew = fn; }

  /** The highest seq held (the next incremental fetch starts after it). */
  get lastSeq() {
    let n = 0;
    for (const s of this.messages.keys()) if (s > n) n = s;
    return n;
  }

  /** Merges transcript messages (by seq); returns how many were new. */
  addMessages(list) {
    if (this.fixture && list !== this.loading) return 0;
    let added = 0;
    for (const m of list || []) {
      if (!m || typeof m.seq !== 'number') continue;
      if (!this.messages.has(m.seq)) added++;
      this.messages.set(m.seq, m);
    }
    if (added || (list && list.length)) this.dirty = true;
    if (added) this.off = null;
    return added;
  }

  /**
   * Replaces every message with a loaded fixture (the console handle):
   * the live context and brain.* events no longer apply, so they are
   * ignored until the next reload().
   */
  setMessages(list) {
    this.messages.clear();
    this.context = null;
    this.fixture = true;
    this.open.setExpandAll(false);
    this.loading = list;
    this.addMessages(list);
    this.loading = null;
    this.dirty = this.meterDirty = true;
  }

  /** Leaves fixture mode: the next fetches fill the view again. */
  reload() {
    this.messages.clear();
    this.context = null;
    this.fixture = false;
    this.dirty = this.meterDirty = true;
  }

  setContext(ctx) {
    if (this.fixture) return;
    this.context = ctx;
    this.off = null;
    this.dirty = this.meterDirty = true;
  }

  setPrompt(p) {
    this.prompt = p;
    this.meterDirty = true;
  }

  setOff(off) {
    this.off = off;
    this.dirty = this.meterDirty = true;
  }

  setError(msg) {
    this.error = msg || '';
    this.dirty = true;
  }

  setFilter(name, on) {
    this.filters[name] = !!on;
    this.render(true);
  }

  setExpandAll(on) {
    this.open.setExpandAll(on);
  }

  scrollToBottom() {
    this.follow.scrollToBottom();
  }

  /** Feeds one log event; true when it was a brain.* / daemon.start the view uses. */
  addEvent(ev) {
    const used = this.turns.add(ev);
    if (used && !this.fixture) this.dirty = true;
    return used;
  }

  get items() {
    const list = [...this.messages.values()].sort((a, b) => a.seq - b.seq).slice(-MAX_ITEMS);
    // A loaded fixture has no events of its own: turn headers come from
    // its request messages alone.
    return buildTranscriptView(list, this.context, this.fixture ? new TurnLog() : this.turns, this.filters);
  }

  /** Rebuilds the meter and the transcript when something changed (or when forced). */
  render(force) {
    if (this.meterDirty || force) {
      this.meterDirty = false;
      this.renderMeter();
    }
    if (!this.dirty && !force) return;
    this.dirty = false;
    const wasAtBottom = this.follow.begin();
    const frag = document.createDocumentFragment();
    if (this.off && this.messages.size === 0) {
      const box = el('div', 'svp-off');
      box.appendChild(el('b', null, 'Brain off'));
      box.appendChild(el('div', null, this.off.message || ''));
      box.appendChild(el('div', 'muted small', `reason: ${this.off.reason || 'unknown'} · run with --brain=on and a Gemini key, or --brain=fake (developer mode, no network) to see the SVP's transcript here`));
      frag.appendChild(box);
    } else if (this.error && this.messages.size === 0) {
      frag.appendChild(el('p', 'muted empty', `error: ${this.error}`));
    }
    const items = this.items;
    let shown = 0;
    for (const it of items) {
      const node = this.renderItem(it);
      if (!node) continue;
      node.dataset.ts = it.ts || '';
      node.dataset.seq = it.seq || '';
      if (it.inWindow === false) node.classList.add('svp-dim');
      frag.appendChild(node);
      shown++;
    }
    if (shown === 0 && !this.off && !this.error) frag.appendChild(el('p', 'muted empty', 'No transcript yet: the brain records the CEO\'s words, pushes and worker events as they happen, and its own turns when the assistant asks it something.'));
    this.root.replaceChildren(frag);
    this.follow.end(wasAtBottom, items.length);
  }

  // ---------------------------------------------------------------- meter

  renderMeter() {
    const m = meterSegments(this.context);
    const box = document.createDocumentFragment();
    if (!m.known) {
      box.appendChild(el('div', 'muted small', this.off ? `Brain off (${this.off.reason}): no window to meter.` : (this.fixture ? 'A loaded transcript (sheepdogDebug.loadTranscript): no live window to meter; Refresh returns to the daemon\'s run.' : 'Waiting for /api/brain/context…')));
      this.meter.replaceChildren(box);
      return;
    }
    const pct = (tokens) => `${Math.max(0, (tokens / m.scale) * 100).toFixed(2)}%`;
    const bar = (cls, segs, label, note) => {
      const row = el('div', `meter-row ${cls}`);
      const head = el('div', 'meter-head');
      head.appendChild(el('span', 'meter-label', label));
      if (note) head.appendChild(el('span', 'meta', note));
      row.appendChild(head);
      const b = el('div', 'meter-bar');
      for (const s of segs) {
        const seg = el('span', `meter-seg ${s.group === 'prompt' ? SOURCE_CLS[s.source] || 'src-unknown' : (s.group === 'pruned' ? 'kind-pruned' : `kind-${s.kind}`)}`);
        seg.style.width = pct(s.tokens);
        seg.title = s.group === 'prompt' ? `${s.label} · ${s.source} · ${s.chars} chars · ~${s.tokens} tokens` : `${s.label} · ${s.count} message${s.count === 1 ? '' : 's'} · ~${s.tokens} tokens`;
        b.appendChild(seg);
      }
      row.appendChild(b);
      return row;
    };
    const wrap = el('div', 'meter');
    wrap.appendChild(bar('prompt', m.prompt, `system prompt · ~${fmtTokens(m.promptTokens)} tokens`, ' · re-sent on every call, outside the window budget'));
    const share = m.budget ? Math.round((m.windowTokens / m.budget) * 100) : 0;
    const win = bar('window', m.transcript, `window · ~${fmtTokens(m.windowTokens)} / ${fmtTokens(m.budget)} tokens (${share}%)`, ` · ${m.inWindow} message${m.inWindow === 1 ? '' : 's'} in the window · ${m.onDisk} on disk only${m.over ? ' · OVER BUDGET: compaction due' : ''}`);
    if (m.over) win.classList.add('over');
    if (m.budget && m.budget < m.scale) {
      const line = el('span', 'meter-budget');
      line.style.left = pct(m.budget);
      line.title = `budget ${m.budget} tokens`;
      win.querySelector('.meter-bar').appendChild(line);
    }
    wrap.appendChild(win);
    const legend = el('div', 'meter-legend');
    for (const s of [...m.prompt.reduce((acc, s) => acc.set(s.source, { label: s.source, cls: SOURCE_CLS[s.source] || 'src-unknown' }), new Map()).values()]) {
      legend.appendChild(chip(s.cls, `prompt: ${s.label}`));
    }
    for (const s of m.transcript) legend.appendChild(chip(s.group === 'pruned' ? 'kind-pruned' : `kind-${s.kind}`, `${s.label} ×${s.count} · ~${fmtTokens(s.tokens)}`));
    wrap.appendChild(legend);
    if (this.context && this.context.last_compaction) {
      const c = this.context.last_compaction;
      legend.appendChild(el('span', 'meta', ` · last compaction ${fmtTime(c.at)}: ${c.dropped} messages, ${fmtTokens(c.before_tokens)} → ${fmtTokens(c.after_tokens)} tokens`));
    }
    box.appendChild(wrap);
    box.appendChild(this.renderSections(m));
    this.meter.replaceChildren(box);
  }

  renderSections(m) {
    const table = el('table', 'svp-sections');
    const thead = el('thead');
    const tr = el('tr');
    for (const h of ['section', 'source', 'chars', '~tokens']) tr.appendChild(el('th', null, h));
    thead.appendChild(tr);
    table.appendChild(thead);
    const body = el('tbody');
    const texts = new Map((this.prompt && this.prompt.sections ? this.prompt.sections : []).map((s) => [s.name, s.text]));
    for (const s of m.prompt) {
      const key = `section:${s.label}`;
      const open = this.open.isOpen(key);
      const row = el('tr', `svp-section ${SOURCE_CLS[s.source] || ''}${open ? ' open' : ''}`);
      const name = el('td');
      name.appendChild(el('span', 'chev', open ? '▾' : '▸'));
      name.appendChild(el('code', null, s.label));
      row.appendChild(name);
      const src = el('td');
      src.appendChild(el('span', `badge ${SOURCE_CLS[s.source] || ''}`, s.source));
      row.appendChild(src);
      row.appendChild(el('td', 'num', String(s.chars)));
      row.appendChild(el('td', 'num', String(s.tokens)));
      row.addEventListener('click', () => {
        if (!this.open.isOpen(key) && !texts.has(s.label)) this.onNeedPrompt();
        this.open.toggle(key);
      });
      body.appendChild(row);
      if (open) {
        const tr2 = el('tr', 'svp-section-text');
        const td = el('td');
        td.colSpan = 4;
        td.appendChild(texts.has(s.label) ? el('pre', 'prompt-text', texts.get(s.label)) : el('div', 'muted small', 'fetching the section text…'));
        tr2.appendChild(td);
        body.appendChild(tr2);
      }
    }
    const total = el('tr', 'svp-total');
    total.appendChild(el('td', null, 'total'));
    total.appendChild(el('td'));
    total.appendChild(el('td', 'num', String(m.prompt.reduce((n, s) => n + s.chars, 0))));
    total.appendChild(el('td', 'num', String(m.promptTokens)));
    body.appendChild(total);
    table.appendChild(body);
    const details = el('details', 'svp-sections-wrap');
    details.open = this.open.isOpen('sections');
    const model = this.context && this.context.model ? ` · model ${this.context.model}` : '';
    details.appendChild(el('summary', null, `${m.prompt.length} prompt sections${model}`));
    details.appendChild(table);
    details.addEventListener('toggle', () => this.open.set('sections', details.open));
    return details;
  }

  // ----------------------------------------------------------- transcript

  renderItem(it) {
    switch (it.kind) {
      case 'turn': return this.renderTurn(it);
      case 'marker': return this.renderMarker(it);
      case 'ceo': return this.renderBubble(it, 'user', 'CEO', it.text);
      case 'assistant': return this.renderBubble(it, 'assistant svp-assistant', 'assistant', it.text);
      case 'reply': return this.renderReply(it);
      case 'push': return this.renderPush(it);
      case 'worker': return this.renderLine(it, 'svp-worker', `${it.agent || '?'}: ${it.closed ? 'pane closed' : `${it.from} → ${it.to}`}${it.text ? it.text : ''}`);
      case 'note': return this.renderLine(it, 'svp-note', `note: ${it.text}`);
      case 'promise': return this.renderPromise(it);
      case 'brainEvent': return this.renderBrainEvent(it);
      case 'screen': return this.renderScreen(it);
      case 'request': return this.renderRequest(it);
      case 'tool': return this.renderTool(it);
      case 'compaction': return this.renderCompaction(it);
      default: return this.renderLine(it, 'svp-other', `${it.rawKind}: ${it.text}`);
    }
  }

  // rawToggle is the small "{}" that opens an item's raw JSON under it.
  rawToggle(it, meta) {
    const key = `${it.key}:raw`;
    const b = el('button', `raw-btn${this.open.isOpen(key) ? ' open' : ''}`, '{}');
    b.title = 'raw JSON';
    b.addEventListener('click', (e) => { e.stopPropagation(); this.open.toggle(key); });
    meta.appendChild(b);
    return this.open.isOpen(key) ? jsonPre(it.raw, 'detail svp-raw') : null;
  }

  metaLine(it, bits) {
    const meta = el('div', 'meta');
    const parts = [fmtTime(it.ts), `#${it.seq}`, ...bits];
    if (it.inWindow === false) parts.push('on disk only');
    if (it.tokens) parts.push(`~${it.tokens} tok`);
    meta.appendChild(el('span', null, parts.filter(Boolean).join(' · ')));
    return meta;
  }

  renderBubble(it, who, label, text) {
    const wrap = el('div', `msg ${who}`);
    const bubble = el('div', 'bubble');
    bubble.appendChild(el('span', 'who', label));
    bubble.appendChild(el('span', 'text', text));
    wrap.appendChild(bubble);
    const meta = this.metaLine(it, []);
    const raw = this.rawToggle(it, meta);
    wrap.appendChild(meta);
    if (raw) wrap.appendChild(raw);
    return wrap;
  }

  renderReply(it) {
    const wrap = el('div', 'msg assistant svp');
    const bubble = el('div', 'bubble');
    bubble.appendChild(el('span', 'who', 'SVP'));
    bubble.appendChild(el('span', 'text', it.spoken));
    const badges = el('span', 'badges');
    if (!it.json) badges.appendChild(el('span', 'badge error', 'not the answer contract'));
    if (it.needsUser) badges.appendChild(el('span', 'badge needs-user', 'needs the CEO'));
    if (it.clarify) badges.appendChild(el('span', 'badge clarify', 'clarify'));
    if (it.options.length) badges.appendChild(el('span', 'badge options', `${it.options.length} option${it.options.length === 1 ? '' : 's'}`));
    if (badges.childNodes.length) bubble.appendChild(badges);
    wrap.appendChild(bubble);
    const meta = this.metaLine(it, []);
    if (it.detail || it.options.length) {
      const key = `${it.key}:detail`;
      const b = el('button', `raw-btn${this.open.isOpen(key) ? ' open' : ''}`, this.open.isOpen(key) ? 'detail ▾' : 'detail ▸');
      b.addEventListener('click', (e) => { e.stopPropagation(); this.open.toggle(key); });
      meta.appendChild(b);
      if (this.open.isOpen(key)) {
        const d = el('div', 'svp-detail');
        if (it.detail) d.appendChild(el('div', 'text', it.detail));
        if (it.options.length) d.appendChild(el('div', 'muted small', `options: ${it.options.join(' · ')}`));
        wrap.appendChild(d);
      }
    }
    const raw = this.rawToggle(it, meta);
    wrap.appendChild(meta);
    if (raw) wrap.appendChild(raw);
    return wrap;
  }

  renderLine(it, cls, text) {
    const row = el('div', `svp-line ${cls}`);
    row.appendChild(el('span', 'text', text));
    const meta = el('span', 'meta', ` · ${fmtTime(it.ts)} · #${it.seq}${it.inWindow === false ? ' · on disk only' : ''}`);
    const raw = this.rawToggle(it, meta);
    row.appendChild(meta);
    if (raw) row.appendChild(raw);
    return row;
  }

  renderPush(it) {
    const d = it.decision;
    const box = el('div', `item push ${it.urgency || 'idle'} svp-push${d ? ` decided via-${d.via}` : ''}`);
    const { h, open } = this.open.header(it.key, 'push-head', (h) => {
      h.appendChild(el('span', 'who', 'supervisor → assistant'));
      if (it.urgency) h.appendChild(el('span', `badge ${it.urgency}`, it.urgency));
      if (it.agent) h.appendChild(el('span', 'meta', it.agent));
      h.appendChild(el('span', 'text', it.text));
      if (d) h.appendChild(el('span', `badge decision ${d.via === 'brain' ? (d.requested === d.applied ? 'as-asked' : 'capped') : 'template'}`, decisionLabel(d)));
      h.appendChild(el('span', 'meta', ` · ${fmtTime(it.ts)} · #${it.seq}${it.inWindow === false ? ' · on disk only' : ''}`));
    });
    box.appendChild(h);
    if (open) {
      const body = el('div', 'body');
      if (d) {
        body.appendChild(el('div', 'label', 'push judgment (brain.push_decision)'));
        const bits = [`via ${d.via}`, d.requested ? `brain asked ${d.requested}` : 'no brain line', `applied ${d.applied}`, `reason ${d.reason}`, `ceiling ${d.ceiling}`, d.late ? 'late: the template was already spoken' : '', d.needsUser ? 'needs the CEO' : '', d.relatesTo ? `relates to "${d.relatesTo}"` : '', d.kept.length ? `keeps ${d.kept.join(', ')}` : '', d.error ? `error: ${d.error}` : ''];
        body.appendChild(el('div', 'text', bits.filter(Boolean).join(' · ')));
        if (d.template && d.template !== it.text) body.appendChild(el('div', 'muted small', `template: "${d.template}"`));
        if (d.detail) body.appendChild(el('div', 'muted small', `detail: ${d.detail}`));
        body.appendChild(el('div', 'label', 'decision event'));
        body.appendChild(jsonPre(d.raw));
      }
      body.appendChild(el('div', 'label', 'raw'));
      body.appendChild(jsonPre(it.raw));
      box.appendChild(body);
    }
    return box;
  }

  // A small line for a brain.* event without a message: a push judgment
  // that spoke nothing, an event turn that yielded, a coalesced turn.
  renderBrainEvent(it) {
    let text;
    switch (it.type) {
      case 'yielded':
        text = `yielded: turn ${it.turn} (${it.agent} ${it.event}) gave way to ${it.ceoWaiting === 1 ? 'a CEO turn' : `${it.ceoWaiting} CEO turns`} before step ${it.step}; the template spoke instead`;
        break;
      case 'coalesced':
        text = `coalesced: a newer ${it.agent} ${it.event} event replaced the queued ${it.agent} turn; the template spoke instead`;
        break;
      default:
        text = `push judgment: ${it.applied}${it.requested && it.requested !== it.applied ? ` (brain asked ${it.requested})` : ''} · ${it.reason}${it.reason === 'ceiling' ? ` ${it.ceiling}` : ''} · ${it.agent || it.pane || ''} ${it.event}${it.template ? ` · "${it.template}"` : ''}${it.late ? ' · late' : ''}`;
    }
    const row = el('div', `svp-line svp-brain-event ${it.type}${it.type === 'decision' ? ` reason-${it.reason}` : ''}`);
    row.appendChild(el('span', 'text', text));
    const meta = el('span', 'meta', ` · ${fmtTime(it.ts)}`);
    const raw = this.rawToggle(it, meta);
    row.appendChild(meta);
    if (raw) row.appendChild(raw);
    return row;
  }

  renderRequest(it) {
    if (it.nudge) return this.renderLine(it, 'svp-nudge', `→ SVP: ${it.question}`);
    const event = it.trigger === 'event' && it.event;
    const box = el('div', `item svp-request${event ? ' svp-event' : ''}`);
    const { h, open } = this.open.header(it.key, 'request-head', (h) => {
      h.appendChild(el('span', 'who', event ? 'supervisor → SVP' : 'assistant → SVP'));
      h.appendChild(el('span', `badge req-${it.reqKind}`, it.reqKind));
      if (event) {
        const n = it.promises ? it.promises.length : (it.promise ? 1 : 0);
        h.appendChild(el('span', 'text', `${it.agent} ${it.event} · template "${it.question}"${it.templateSpoken ? ' (already spoken)' : ''}${n > 1 ? ` · keeps ${n} promises` : (it.promise ? ` · promise "${it.promise}"` : '')}`));
      } else {
        h.appendChild(el('span', 'text', `"${it.question}"`));
      }
      const bits = [event ? '' : (it.agent ? `agent ${it.agent}` : 'no agent named'), it.trigger === 'event' ? 'triggered by an event' : '', fmtTime(it.ts), `#${it.seq}`];
      if (it.inWindow === false) bits.push('on disk only');
      h.appendChild(el('span', 'meta', ` · ${bits.filter(Boolean).join(' · ')}`));
    });
    box.appendChild(h);
    if (open) {
      const body = el('div', 'body');
      body.appendChild(el('div', 'label', 'as the SVP received it'));
      body.appendChild(el('pre', null, it.raw.content || ''));
      body.appendChild(el('div', 'label', 'raw'));
      body.appendChild(jsonPre(it.raw));
      box.appendChild(body);
    }
    return box;
  }

  renderTool(it) {
    const r = it.result;
    const failed = r && r.error;
    const rail = failed && /not permitted/.test(r.error);
    const box = el('div', `item tool svp-tool${it.pending ? ' pending' : ''}${failed ? ' failed' : ''}${rail ? ' rail' : ''}`);
    const { h, open } = this.open.header(it.key, 'tool', (h) => {
      h.appendChild(el('span', 'icon', '⚙'));
      h.appendChild(el('span', 'name', it.name));
      if (it.summary) h.appendChild(el('span', 'args', `(${it.summary})`));
      const bits = [];
      if (it.pending) bits.push('waiting for the result…');
      else if (r) {
        if (rail) bits.push('rail blocked');
        else if (r.error) bits.push('error');
        if (r.durMs != null) bits.push(fmtMs(r.durMs));
        if (r.pruned) bits.push('pruned from window');
      }
      if (it.orphan) bits.push('result without a call');
      bits.push(fmtTime(it.ts), `#${it.seq}${r ? `–${r.seq}` : ''}`);
      if (it.inWindow === false) bits.push('on disk only');
      if (it.tokens) bits.push(`~${it.tokens} tok`);
      h.appendChild(el('span', 'meta', ` · ${bits.join(' · ')}`));
      if (r && r.pruned) h.appendChild(el('span', 'badge pruned', 'pruned'));
    });
    box.appendChild(h);
    if (open) {
      const body = el('div', 'body');
      if (it.thought) {
        body.appendChild(el('div', 'label', 'model text with the call'));
        body.appendChild(el('div', 'svp-thought', it.thought));
      }
      body.appendChild(el('div', 'label', 'args'));
      body.appendChild(jsonPre(it.args, null));
      body.appendChild(el('div', 'label', 'result'));
      if (it.pending) body.appendChild(el('div', 'muted', 'no result yet'));
      else if (r.error) body.appendChild(el('pre', 'svp-error', r.error));
      else {
        if (r.pruned) {
          body.appendChild(el('div', 'svp-stub', `in the window now: ${r.stub}`));
          body.appendChild(el('div', 'label', 'full result (on disk)'));
        }
        body.appendChild(el('pre', 'svp-result', r.content || '(empty)'));
      }
      if (r && r.summary) body.appendChild(el('div', 'meta', `stub when pruned: [${r.summary}, pruned]`));
      body.appendChild(el('div', 'label', 'raw'));
      body.appendChild(jsonPre(r ? { call: it.raw, result: r.raw } : it.raw));
      box.appendChild(body);
    }
    return box;
  }

  // A promise card: the record's transition (made / kept / expired /
  // cancelled) with the status it has now, from the promise.* events.
  renderPromise(it) {
    const box = el('div', `item svp-promise status-${it.now}`);
    const { h, open } = this.open.header(it.key, 'promise-head', (h) => {
      h.appendChild(el('span', 'who', 'promise'));
      h.appendChild(el('span', `badge promise-${it.status}`, it.status));
      if (it.now !== it.status) h.appendChild(el('span', `badge promise-${it.now}`, `now ${it.now}`));
      h.appendChild(el('span', 'text', `"${it.text}"`));
      const bits = [it.condition || (it.agent ? `${it.agent} · ${it.event}` : ''), it.createdTurn ? `made in turn ${it.createdTurn}` : '', it.due ? `due ${fmtTime(it.due)}` : '', fmtTime(it.ts), `#${it.seq}`];
      if (it.inWindow === false) bits.push('on disk only');
      h.appendChild(el('span', 'meta', ` · ${bits.filter(Boolean).join(' · ')}`));
    });
    box.appendChild(h);
    if (open) {
      const body = el('div', 'body');
      if (it.latest) {
        body.appendChild(el('div', 'label', 'from the promise.* events'));
        const l = it.latest;
        body.appendChild(el('div', 'text', `${l.status}${l.ts ? ` at ${fmtTime(l.ts)}` : ''}${l.spoken ? ` · spoken "${l.spoken}"` : ''}${l.via ? ` · via ${l.via}` : ''}${l.urgency ? ` · ${l.urgency}` : ''}`));
      }
      body.appendChild(el('div', 'label', 'raw'));
      body.appendChild(jsonPre(it.raw));
      box.appendChild(body);
    }
    return box;
  }

  // The screen the supervisor read for an event turn, collapsed like a
  // tool result and marked when the window holds only its stub.
  renderScreen(it) {
    const box = el('div', `item tool svp-tool svp-screen`);
    const { h, open } = this.open.header(it.key, 'tool', (h) => {
      h.appendChild(el('span', 'icon', '⚙'));
      h.appendChild(el('span', 'name', 'screen from the supervisor'));
      if (it.summary) h.appendChild(el('span', 'args', `(${it.summary})`));
      const bits = [];
      if (it.pruned) bits.push('pruned from window');
      bits.push(fmtTime(it.ts), `#${it.seq}`);
      if (it.inWindow === false) bits.push('on disk only');
      if (it.tokens) bits.push(`~${it.tokens} tok`);
      h.appendChild(el('span', 'meta', ` · ${bits.join(' · ')}`));
      if (it.pruned) h.appendChild(el('span', 'badge pruned', 'pruned'));
    });
    box.appendChild(h);
    if (open) {
      const body = el('div', 'body');
      if (it.pruned) {
        body.appendChild(el('div', 'svp-stub', `in the window now: ${it.stub}`));
        body.appendChild(el('div', 'label', 'full screen (on disk)'));
      } else body.appendChild(el('div', 'label', 'as the SVP sees it'));
      body.appendChild(el('pre', 'svp-result', it.content || '(empty)'));
      body.appendChild(el('div', 'label', 'raw'));
      body.appendChild(jsonPre(it.raw));
      box.appendChild(body);
    }
    return box;
  }

  renderCompaction(it) {
    const box = el('div', 'item svp-compaction');
    const info = it.info;
    const { h, open } = this.open.header(it.key, 'compaction-head', (h) => {
      h.appendChild(el('span', 'who', 'compaction'));
      h.appendChild(el('span', 'text', info ? `compacted ${info.dropped} messages · ${fmtTokens(info.before)} → ${fmtTokens(info.after)} tokens` : 'compacted the oldest half into this summary'));
      h.appendChild(el('span', 'meta', ` · ${fmtTime(it.ts)} · #${it.seq}${it.inWindow === false ? ' · on disk only' : ''} · ~${it.tokens} tok`));
    });
    box.appendChild(h);
    if (open) {
      const body = el('div', 'body');
      body.appendChild(el('div', 'label', 'summary the model sees instead'));
      body.appendChild(el('div', 'svp-summary', it.summary));
      body.appendChild(el('div', 'label', 'raw'));
      body.appendChild(jsonPre(it.raw));
      box.appendChild(body);
    }
    return box;
  }

  renderTurn(it) {
    const row = el('div', `svp-turn${it.fallback ? ' fallback' : ''}${it.complete ? '' : ' running'}`);
    const bits = [`turn ${it.turn}`, it.trigger, it.reqKind, it.agent ? `${it.agent} ${it.event}` : ''];
    if (it.modelCalls != null) bits.push(`${it.modelCalls} model call${it.modelCalls === 1 ? '' : 's'}`);
    if (it.durMs != null) bits.push(fmtMs(it.durMs));
    if (it.tokensIn) bits.push(`${fmtTokens(it.tokensIn)} in / ${fmtTokens(it.tokensOut)} out`);
    if (it.windowTokens != null) bits.push(`window ${fmtTokens(it.windowTokens)}`);
    if (it.acted) bits.push(`${it.acted} action${it.acted === 1 ? '' : 's'}`);
    if (it.fallback) bits.push(`FALLBACK${it.reason ? ` (${it.reason})` : ''}`);
    else if (!it.complete) bits.push('no brain.turn event yet');
    row.appendChild(el('span', 'line'));
    row.appendChild(el('span', 'text', bits.filter(Boolean).join(' · ')));
    row.appendChild(el('span', 'line'));
    return row;
  }

  renderMarker(it) {
    const row = el('div', 'svp-marker');
    row.appendChild(el('div', 'above', `▲ on disk only · ${it.onDisk} message${it.onDisk === 1 ? '' : 's'} the model no longer sees ▲`));
    row.appendChild(el('div', 'below', `▼ in the context window · ${it.inWindow} message${it.inWindow === 1 ? '' : 's'} · ~${fmtTokens(it.tokens)} / ${fmtTokens(it.budget)} tokens ▼`));
    return row;
  }
}

/** The push card's badge: "brain: idle", "brain: interrupt → idle (ceiling)", "template (timeout)". */
function decisionLabel(d) {
  if (d.via !== 'brain') return `template${d.reason && d.reason !== 'brain' ? ` (${d.reason})` : ''}`;
  if (d.requested === d.applied) return `brain: ${d.applied}${d.late ? ' · late' : ''}`;
  return `brain: ${d.requested} → ${d.applied} (${d.reason})${d.late ? ' · late' : ''}`;
}

function chip(cls, text) {
  const c = el('span', `meter-chip ${cls}`);
  c.appendChild(el('span', 'swatch'));
  c.appendChild(document.createTextNode(text));
  return c;
}
