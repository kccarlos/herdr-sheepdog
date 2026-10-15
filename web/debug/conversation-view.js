// Conversation tab of /debug: renders the items produced by conversation.js
// as a chat transcript (user bubbles right, the voice left, tool calls and
// supervisor pushes as collapsible rows between them, session dividers).
// Vanilla DOM, no framework (decision D11).
import { Grouper } from './conversation.js';
import { fmtMs, eventSummary, Dedupe } from './grouping.js';
import { el, fmtTime, jsonPre, Openable, Follow } from './dom.js';

const MAX_ITEMS = 1500;
const STALL_MS = 10000;

// What the voice calls itself (config voice.name), for its own bubbles;
// setDogName is called with /api/state's voice.name.
let dogName = 'Sheepdog';

/** Sets the label used for the voice's bubbles (no-op for an empty name). */
export function setDogName(name) {
  if (name) dogName = name;
}

export class ConversationView {
  constructor(root) {
    this.root = root; // the scrolling container
    this.grouper = new Grouper();
    this.seen = new Dedupe(); // encoded events already folded (backfill vs. SSE replay)
    this.open = new Openable(() => this.render(true)); // item / nested keys open state
    this.follow = new Follow(root);
    this.hideSilent = true;
    this.hideStats = false;
    this.currentPrompt = null; // /api/voice/system_prompt when the log has none
    this.dirty = false;
  }

  set onNewMessages(fn) { this.follow.onNew = fn; }
  get onNewMessages() { return this.follow.onNew; }

  /** Feeds one log event; duplicates (same encoded line) are ignored. */
  add(ev, encoded) {
    if (!this.seen.add(encoded || JSON.stringify(ev))) return false;
    this.grouper.add(ev);
    this.dirty = true;
    return true;
  }

  setCurrentPrompt(p) {
    this.currentPrompt = p;
    this.dirty = true;
  }

  isOpen(key) {
    return this.open.isOpen(key);
  }

  toggle(key) {
    this.open.toggle(key);
  }

  setExpandAll(on) {
    this.open.setExpandAll(on);
  }

  atBottom() {
    return this.follow.atBottom();
  }

  scrollToBottom() {
    this.follow.scrollToBottom();
  }

  /** Rebuilds the DOM when something changed (or when forced). */
  render(force) {
    if (!this.dirty && !force) return;
    this.dirty = false;
    const wasAtBottom = this.follow.begin();
    const items = this.grouper.items.slice(-MAX_ITEMS);
    const frag = document.createDocumentFragment();
    if (this.currentPrompt && !items.some((it) => it.kind === 'system_prompt')) {
      frag.appendChild(this.renderSystemPrompt({
        key: 'system_prompt:current', text: this.currentPrompt.text, aliases: this.currentPrompt.aliases || [],
        tokens: Math.round((this.currentPrompt.text || '').length / 4), current: true,
      }));
    }
    let shown = 0;
    for (const it of items) {
      const node = this.renderItem(it);
      if (!node) continue;
      node.dataset.ts = it.ts || '';
      frag.appendChild(node);
      shown++;
    }
    if (shown === 0 && !this.currentPrompt) frag.appendChild(el('p', 'muted empty', 'No conversation yet: connect /voice, or send an utterance below.'));
    this.root.replaceChildren(frag);
    this.follow.end(wasAtBottom, items.length);
  }

  renderItem(it) {
    switch (it.kind) {
      case 'system_prompt': return this.renderSystemPrompt(it);
      case 'divider': return this.renderDivider(it);
      case 'user': return this.renderMessage(it, 'user');
      case 'assistant': return this.renderMessage(it, 'assistant');
      case 'tool': return this.renderTool(it);
      case 'push': return this.renderPush(it);
      default: return null;
    }
  }

  // header builds a clickable summary line with a chevron for `key`.
  header(key, cls, build) {
    return this.open.header(key, cls, build);
  }

  renderSystemPrompt(it) {
    const box = el('div', `item system-prompt${it.current ? ' current' : ''}`);
    const { h, open } = this.header(it.key, 'sys', (h) => {
      h.appendChild(el('span', 'name', 'System prompt'));
      h.appendChild(el('span', 'meta', `· ~${it.tokens} tokens${it.model ? ` · ${it.model}` : ''}${it.current ? ' · current (not from the log)' : ` · ${fmtTime(it.ts)}`}`));
    });
    box.appendChild(h);
    if (open) {
      const body = el('div', 'body');
      body.appendChild(el('pre', 'prompt-text', it.text));
      if (it.aliases && it.aliases.length) body.appendChild(el('div', 'meta', `aliases: ${it.aliases.join(', ')}`));
      box.appendChild(body);
    }
    return box;
  }

  renderDivider(it) {
    const d = el('div', `divider ${it.cls || ''}`);
    d.appendChild(el('span', 'line'));
    const t = el('span', 'text');
    t.appendChild(el('b', null, it.label));
    if (it.detail) t.appendChild(el('span', 'detail', ` · ${it.detail}`));
    t.appendChild(el('span', 'ts', ` · ${fmtTime(it.ts)}`));
    d.appendChild(t);
    d.appendChild(el('span', 'line'));
    return d;
  }

  renderMessage(it, who) {
    const wrap = el('div', `msg ${who}${it.final ? '' : ' partial'}${it.synthetic ? ' synthetic' : ''}`);
    const bubble = el('div', 'bubble');
    bubble.appendChild(el('span', 'who', who === 'user' ? 'user' : dogName));
    bubble.appendChild(el('span', 'text', it.text + (it.final ? '' : ' …')));
    wrap.appendChild(bubble);
    const meta = el('div', 'meta');
    const bits = [fmtTime(it.ts)];
    if (it.typed) bits.push('typed');
    if (it.synthetic) bits.push('spoken by the daemon (no voice model)');
    if (it.fragments > 1) bits.push(`${it.fragments} fragments`);
    if (it.interrupted) bits.push('interrupted');
    meta.appendChild(el('span', null, bits.join(' · ')));
    this.turnMeta(meta, it);
    wrap.appendChild(meta);
    return wrap;
  }

  // turnMeta adds the voice.turn timing (and usage) to a message or tool row.
  turnMeta(meta, it) {
    if (it.turn) {
      const t = it.turn;
      if (t.responseMs != null) {
        const slow = t.responseMs >= STALL_MS;
        meta.appendChild(el('span', `latency${slow ? ' slow' : ''}`, ` · answered after ${fmtMs(t.responseMs)}${t.stalled ? ' (stalled)' : ''}`));
      } else if (t.responded === false) {
        meta.appendChild(el('span', 'latency slow', ' · no response'));
      }
      if (t.end && t.end !== 'complete') meta.appendChild(el('span', null, ` · turn ${t.end}`));
    }
    if (it.usage && !this.hideStats) {
      meta.appendChild(el('span', 'stats', ` · ${it.usage.prompt != null ? `${it.usage.prompt} prompt tokens` : ''}${it.usage.total != null ? `, ${it.usage.total} total` : ''}`));
    }
  }

  renderTool(it) {
    const box = el('div', `item tool${it.pending ? ' pending' : ''}${it.result && (it.result.error || it.result.cancelled) ? ' failed' : ''}`);
    const { h, open } = this.header(it.key, 'tool', (h) => {
      h.appendChild(el('span', 'icon', '⚙'));
      h.appendChild(el('span', 'name', it.name));
      if (it.summary) h.appendChild(el('span', 'args', `(${it.summary})`));
      const bits = [fmtTime(it.ts)];
      if (it.via && it.via !== 'voice') bits.push(`via ${it.via}`);
      if (it.pending) bits.push('waiting for result…');
      else if (it.result) {
        if (it.result.cancelled) bits.push('cancelled');
        else if (it.result.error) bits.push('error');
        if (it.result.durMs != null) bits.push(`${it.result.durMs} ms`);
        if (it.result.scheduling) bits.push(it.result.scheduling);
        if (it.result.deferred) bits.push('deferred (model was speaking)');
      }
      if (it.orphan) bits.push('result without a call');
      if (it.nested.length) bits.push(`${it.nested.length} event${it.nested.length > 1 ? 's' : ''}`);
      if (it.result && it.result.pendingAnswer) {
        // The acknowledgement of an investigation: the real answer is a
        // push further down, when the job lands.
        bits.push(it.answeredBy ? `answered below (${it.job || 'investigation'})` : 'answer still to come');
      }
      const meta = el('span', 'meta', ` · ${bits.join(' · ')}`);
      this.turnMeta(meta, it);
      h.appendChild(meta);
    });
    box.appendChild(h);
    if (open) {
      const body = el('div', 'body');
      body.appendChild(el('div', 'label', 'args'));
      body.appendChild(jsonPre(it.args, null));
      body.appendChild(el('div', 'label', 'result'));
      if (it.pending) body.appendChild(el('div', 'muted', 'no result yet'));
      else if (it.result.cancelled) body.appendChild(el('div', 'muted', 'cancelled (the user barged in)'));
      else {
        const r = it.result;
        body.appendChild(el('pre', null, JSON.stringify({
          spoken: r.spoken, clarify: r.clarify, scheduling: r.scheduling, dur_ms: r.durMs,
          ...(r.error ? { error: r.error } : {}), ...(r.sendError ? { send_error: r.sendError } : {}),
        }, null, 2)));
      }
      if (it.nested.length) {
        body.appendChild(el('div', 'label', 'supervisor events during the call'));
        body.appendChild(this.renderNested(it.key, it.nested));
      }
      box.appendChild(body);
    }
    return box;
  }

  // renderNested lists the supervisor-side events of a call or push. A
  // nested tool call (brain.tool_call, see Grouper.innerCall) carries its
  // own `children` (what ran between call and result) and `result`, shown
  // one level deeper.
  renderNested(parentKey, events) {
    const list = el('div', 'nested');
    events.forEach((ev, i) => {
      const key = `${parentKey}:n${i}`;
      const inner = Array.isArray(ev.children);
      const row = el('div', `nested-row ${ev.source || ''}${inner ? ' inner-call' : ''}`);
      const { h, open } = this.header(key, 'nested-head', (h) => {
        h.appendChild(el('span', 'type', ev.type));
        h.appendChild(el('span', 'summary', eventSummary(ev)));
        if (inner) {
          const r = ev.result && ev.result.payload ? ev.result.payload : null;
          const bits = ev.pending ? ['pending'] : [fmtMs(ev.durMs), r && r.error ? `error: ${r.error}` : ''];
          h.appendChild(el('span', `badge ${ev.pending ? 'pending' : (r && r.error ? 'error' : 'ok')}`, bits.filter(Boolean).join(' · ')));
        }
        h.appendChild(el('span', 'meta', ` · ${fmtTime(ev.ts)}`));
      });
      row.appendChild(h);
      if (open) {
        row.appendChild(jsonPre(ev.payload));
        if (inner && ev.children.length) row.appendChild(this.renderNested(key, ev.children));
        if (inner && ev.result) {
          row.appendChild(el('div', 'label', `${ev.result.type} · ${fmtTime(ev.result.ts)}`));
          row.appendChild(jsonPre(ev.result.payload));
        }
      }
      list.appendChild(row);
    });
    return list;
  }

  renderPush(it) {
    if (it.urgency === 'silent' && this.hideSilent) return null;
    const box = el('div', `item push ${it.urgency}${it.skipped ? ' skipped' : ''}`);
    const silent = it.urgency === 'silent';
    const build = (h) => {
      h.appendChild(el('span', 'who', 'supervisor → model'));
      h.appendChild(el('span', `badge ${it.urgency}`, it.urgency));
      if (it.skipped) h.appendChild(el('span', 'badge skipped', 'not sent (voice off)'));
      // Asked to interrupt, sent when idle: the model was mid-sentence
      // (docs/dev/roles.md "Pushes").
      if (it.deferred) h.appendChild(el('span', 'badge deferred', 'deferred: model was speaking'));
      // The answer to a look the user was told was coming.
      if (it.answersJob) h.appendChild(el('span', 'badge', `answers ${it.answersJob}`));
      h.appendChild(el('span', silent ? 'text one-line' : 'text', it.text));
      h.appendChild(el('span', 'meta', ` · ${fmtTime(it.ts)}${it.nested.length ? ` · ${it.nested.length} event${it.nested.length > 1 ? 's' : ''}` : ''}`));
    };
    const { h, open } = this.header(it.key, 'push-head', build);
    box.appendChild(h);
    if (open) {
      const body = el('div', 'body');
      if (silent) body.appendChild(el('div', 'full-text', it.text));
      if (it.nested.length) {
        body.appendChild(el('div', 'label', 'what led to it'));
        body.appendChild(this.renderNested(it.key, it.nested));
      } else if (!silent) body.appendChild(el('div', 'muted', 'no supervisor events recorded before this push'));
      box.appendChild(body);
    }
    return box;
  }
}
