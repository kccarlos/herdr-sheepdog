// Conversation view grouping for /debug: turns the flat event log into a
// chat-shaped list of items (decision D10: the page is a viewer of the
// log; nothing here talks to the daemon). Pure functions, unit-tested with
// `make test-web`; the DOM rendering lives in app.js.
//
// Item kinds (all carry `ts` of the first event and a stable `key`):
//   system_prompt  voice.system_prompt {text, aliases, model}
//   divider        session boundaries: voice.connect / resume /
//                  handle_dropped / recycle / stalled / disconnected / closed
//   user           voice.transcript.in fragments merged per turn, or a typed
//                  utterance (voice.text, web.utterance via the text router)
//   assistant      voice.transcript.out fragments merged per turn, or the
//                  spoken result of a tool run without a voice model
//   tool           voice.tool_call paired with its voice.tool_result by id,
//                  with the supervisor-side events in between nested
//   push           voice.push (supervisor → model), with the supervisor-side
//                  events that led to it nested

/** Supervisor-side event types nested under the tool call or push they belong to. */
export const NESTED_TYPES = /^(llm\.|cli\.run|herdr\.read|herdr\.send|pending\.|note\.created|supervisor\.)/;

/** Session boundary events and how they read. */
const DIVIDERS = {
  'voice.connect': (p) => ({ label: p.resumed ? 'session resumed' : 'new session', detail: `key ${p.key_index}${p.dur_ms != null ? ` · connect ${p.dur_ms} ms` : ''}`, cls: p.resumed ? 'resume' : 'connect' }),
  'voice.resume': (p) => (p.resumed
    ? { label: 'reconnected, context kept', detail: `${p.replayed_chunks || 0} audio chunks replayed`, cls: 'resume' }
    : { label: 'reconnected, context lost', detail: 'fresh session; the briefing is re-pushed', cls: 'lost' }),
  'voice.handle_dropped': (p) => ({ label: 'context lost', detail: `resumption handle dropped: ${p.reason || ''}`, cls: 'lost' }),
  'voice.recycle': (p) => ({ label: 'connection recycled', detail: `${p.reason}${p.handle ? ', with handle' : ', no handle'}`, cls: 'recycle' }),
  'voice.stalled': (p) => ({ label: 'stalled', detail: `no reply ${fmtMs(p.waited_ms)} after the last input (stall ${p.stalls})`, cls: 'stalled' }),
  'voice.disconnected': (p) => ({ label: 'disconnected', detail: p.error || '', cls: 'lost' }),
  'voice.closed': (p) => ({ label: 'session closed', detail: p.reason || '', cls: 'lost' }),
  'voice.goaway': (p) => ({ label: 'GoAway', detail: `${fmtMs(p.time_left_ms)} left`, cls: 'recycle' }),
};

/** Rough token estimate for the prompt header (4 chars per token). */
export function estimateTokens(text) {
  return Math.round((text || '').length / 4);
}

/** Milliseconds as "1.3 s" / "420 ms". */
export function fmtMs(ms) {
  if (ms == null || Number.isNaN(Number(ms))) return '';
  const n = Number(ms);
  return n >= 1000 ? `${(n / 1000).toFixed(1)} s` : `${Math.round(n)} ms`;
}

/**
 * One-line summary of a tool call's arguments for the collapsed row:
 * `propose(agent="auth", instruction="skip the flaky test…")`.
 */
export function argSummary(args, max = 60) {
  if (!args || typeof args !== 'object') return '';
  const parts = [];
  for (const [k, v] of Object.entries(args)) {
    if (v == null || v === '') continue;
    let s = typeof v === 'string' ? v : JSON.stringify(v);
    if (s.length > max) s = s.slice(0, max - 1) + '…';
    parts.push(`${k}=${typeof v === 'string' ? JSON.stringify(s) : s}`);
  }
  return parts.join(', ');
}

/**
 * Grouper folds events one at a time (the page feeds the SSE stream into
 * it) and keeps `items` current, including the in-progress user or
 * assistant message at the end. groupEvents() is the batch form.
 */
export class Grouper {
  constructor() {
    this.items = [];
    this.seq = 0;
    this.openTools = new Map(); // id → tool item (result not seen yet)
    this.toolStack = []; // open tool items, most recent last
    this.loose = []; // nested-type events with no open tool, for the next push
    this.user = null; // in-progress user item
    this.assistant = null; // in-progress assistant item
    this.turn = null; // { user, first } — first response item of the current turn
  }

  add(ev) {
    const type = ev.type || '';
    const p = ev.payload && typeof ev.payload === 'object' ? ev.payload : {};
    if (DIVIDERS[type]) {
      this.closeAll();
      this.loose = [];
      const d = DIVIDERS[type](p);
      this.push({ kind: 'divider', ts: ev.ts, type, ...d, payload: p });
      return;
    }
    switch (type) {
      case 'voice.system_prompt':
        this.closeAll();
        this.push({ kind: 'system_prompt', ts: ev.ts, text: p.text || '', aliases: p.aliases || [], model: p.model || '', tokens: estimateTokens(p.text) });
        return;
      case 'voice.transcript.in':
        this.closeAssistant();
        this.userFragment(ev.ts, p.text || '', !!p.final, false);
        return;
      case 'voice.text':
        this.closeAssistant();
        this.closeUser();
        this.userFragment(ev.ts, p.text || '', true, true);
        return;
      case 'web.utterance':
        // via=voice is followed by voice.text (the model path); the text
        // router path has no other record of what the user typed.
        if (p.via === 'voice') return;
        this.closeAssistant();
        this.closeUser();
        this.userFragment(ev.ts, p.text || '', true, true);
        return;
      case 'voice.transcript.out':
        this.closeUser();
        this.assistantFragment(ev.ts, p.text || '', !!p.final);
        return;
      case 'voice.tool_call':
        this.closeUser();
        this.closeAssistant();
        this.toolCall(ev, p);
        return;
      case 'voice.tool_result':
        this.toolResult(ev, p);
        return;
      case 'voice.push':
        this.closeUser();
        this.closeAssistant();
        this.push({
          kind: 'push', ts: ev.ts, text: p.text || '', urgency: p.urgency || 'idle', skipped: !!p.skipped,
          nested: this.loose.splice(0), expandable: true,
        });
        return;
      case 'voice.turn':
        this.closeUser();
        this.closeAssistant();
        this.applyTurn(p);
        return;
      case 'voice.interrupted':
        if (this.assistant) {
          this.assistant.interrupted = true;
          this.closeAssistant();
        }
        return;
      case 'voice.usage': {
        const last = this.lastOf((it) => it.kind === 'assistant' || it.kind === 'tool');
        if (last) last.usage = { prompt: p.prompt, response: p.response, total: p.total };
        return;
      }
      default:
        if (NESTED_TYPES.test(type)) this.nest(ev);
    }
  }

  // ---- helpers

  push(item) {
    item.key = `${item.kind}:${++this.seq}`;
    this.items.push(item);
    return item;
  }

  lastOf(pred) {
    for (let i = this.items.length - 1; i >= 0; i--) if (pred(this.items[i])) return this.items[i];
    return null;
  }

  userFragment(ts, text, final, typed) {
    if (!this.user) {
      this.user = this.push({ kind: 'user', ts, text: '', typed, fragments: 0, final: false });
      this.turn = { user: this.user, first: null };
    }
    this.user.text += text;
    this.user.fragments++;
    this.user.typed = this.user.typed || typed;
    if (final) this.closeUser();
  }

  closeUser() {
    if (this.user) {
      this.user.final = true;
      this.user = null;
    }
  }

  assistantFragment(ts, text, final) {
    if (!this.assistant) {
      this.assistant = this.push({ kind: 'assistant', ts, text: '', fragments: 0, final: false });
      this.firstResponse(this.assistant);
    }
    this.assistant.text += text;
    this.assistant.fragments++;
    if (final) this.closeAssistant();
  }

  closeAssistant() {
    if (this.assistant) {
      this.assistant.final = true;
      this.assistant = null;
    }
  }

  closeAll() {
    this.closeUser();
    this.closeAssistant();
  }

  // firstResponse remembers the first model reaction of the current turn
  // so voice.turn's input_to_response_ms can be shown on it.
  firstResponse(item) {
    if (this.turn && !this.turn.first) this.turn.first = item;
  }

  applyTurn(p) {
    if (!this.turn) return;
    const target = this.turn.first || this.turn.user;
    if (target) {
      target.turn = {
        responseMs: p.input_to_response_ms, toolCalls: p.tool_calls, pushesSpoken: p.pushes_spoken,
        end: p.end, responded: p.responded !== false, stalled: !!p.stalled, durMs: p.dur_ms,
      };
    }
    this.turn = null;
  }

  toolCall(ev, p) {
    const item = this.push({
      kind: 'tool', ts: ev.ts, id: p.id || '', name: p.name || '?', args: p.args || {}, via: p.via || '',
      summary: argSummary(p.args), nested: [], result: null, pending: true, expandable: true,
    });
    this.firstResponse(item);
    if (item.id) this.openTools.set(item.id, item);
    this.toolStack.push(item);
  }

  toolResult(ev, p) {
    let item = p.id ? this.openTools.get(p.id) : null;
    if (!item) {
      // Unmatched id: take the most recent open call with the same name.
      for (let i = this.toolStack.length - 1; i >= 0; i--) {
        if (this.toolStack[i].pending && this.toolStack[i].name === p.name) { item = this.toolStack[i]; break; }
      }
    }
    if (!item) {
      item = this.push({ kind: 'tool', ts: ev.ts, id: p.id || '', name: p.name || '?', args: {}, via: p.via || '', summary: '', nested: [], result: null, pending: true, expandable: true, orphan: true });
    }
    item.pending = false;
    item.result = {
      spoken: p.spoken || '', clarify: !!p.clarify, scheduling: p.scheduling || '', durMs: p.dur_ms,
      error: p.error || '', cancelled: !!p.cancelled, sendError: p.send_error || '',
    };
    item.resultTs = ev.ts;
    if (item.id) this.openTools.delete(item.id);
    this.toolStack = this.toolStack.filter((t) => t !== item);
    // Without a voice model the daemon speaks the result itself (the /voice
    // page shows it as an out transcript): render it as the assistant.
    if (item.via && item.via !== 'voice' && !p.cancelled && item.result.spoken) {
      this.closeAssistant();
      const a = this.push({ kind: 'assistant', ts: ev.ts, text: item.result.spoken, fragments: 1, final: true, synthetic: true });
      if (this.turn && this.turn.first === item) this.turn.first = a;
    }
  }

  nest(ev) {
    const open = this.toolStack.length ? this.toolStack[this.toolStack.length - 1] : null;
    if (open) open.nested.push(ev);
    else this.loose.push(ev);
  }
}

/**
 * groupEvents folds a whole event array (oldest first) into items.
 * @param {object[]} events eventlog lines: {ts, source, type, pane_id, payload}
 * @returns {object[]} items
 */
export function groupEvents(events) {
  const g = new Grouper();
  for (const ev of events) g.add(ev);
  return g.items;
}
