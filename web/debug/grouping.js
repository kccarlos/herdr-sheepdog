// Event grouping helpers shared by the Conversation view (conversation.js)
// and the Swimlane view (swimlane.js) of /debug: how session boundaries
// read, how a tool call finds its result, how transcript fragments merge
// into one utterance, and one-line summaries of supervisor-side events.
// Pure functions, no DOM (decision D10: the page is a viewer of the log).

/** Supervisor-side event types that belong to the tool call or push in flight. */
export const NESTED_TYPES = /^(cli\.run|herdr\.read|herdr\.send|pending\.|note\.created|supervisor\.|brain\.|investigation\.)/;

/**
 * Tool call/result families: any `<prefix>.tool_call` / `<prefix>.tool_result`
 * pair carrying an `id` (voice.* today, the supervisor's agent loop as
 * brain.* soon). Returns {prefix, phase: 'call'|'result'} or null.
 */
export function toolFamily(type) {
  const m = /^([a-z][a-z0-9_]*)\.tool_(call|result)$/.exec(type || '');
  return m ? { prefix: m[1], phase: m[2] } : null;
}

/** Milliseconds as "1.3 s" / "420 ms". */
export function fmtMs(ms) {
  if (ms == null || Number.isNaN(Number(ms))) return '';
  const n = Number(ms);
  return n >= 1000 ? `${(n / 1000).toFixed(1)} s` : `${Math.round(n)} ms`;
}

/** Rough token estimate for the prompt header (4 chars per token). */
export function estimateTokens(text) {
  return Math.round((text || '').length / 4);
}

/**
 * Session boundary events (voice lifecycle, daemon start/stop) and how
 * they read: label, detail and a css class carrying the meaning
 * (connect / resume / lost / recycle / stalled / daemon).
 */
export const DIVIDERS = {
  'voice.connect': (p) => ({ label: p.resumed ? 'session resumed' : 'new session', detail: `key ${p.key_index}${p.dur_ms != null ? ` · connect ${p.dur_ms} ms` : ''}`, cls: p.resumed ? 'resume' : 'connect' }),
  'voice.connect_error': (p) => ({ label: 'connect failed', detail: `${p.error || ''}${p.dur_ms != null ? ` · after ${fmtMs(p.dur_ms)}` : ''}`, cls: 'lost' }),
  'voice.resume': (p) => (p.resumed
    ? { label: 'reconnected, context kept', detail: `${p.replayed_chunks || 0} audio chunks replayed`, cls: 'resume' }
    : { label: 'reconnected, context lost', detail: 'fresh session; the briefing is re-pushed', cls: 'lost' }),
  'voice.handle_dropped': (p) => ({ label: 'context lost', detail: `resumption handle dropped: ${p.reason || ''}`, cls: 'lost' }),
  'voice.recycle': (p) => ({ label: 'connection recycled', detail: `${p.reason}${p.handle ? ', with handle' : ', no handle'}`, cls: 'recycle' }),
  'voice.stalled': (p) => ({ label: 'stalled', detail: `no reply ${fmtMs(p.waited_ms)} after the last input (stall ${p.stalls})`, cls: 'stalled' }),
  'voice.disconnected': (p) => ({ label: 'disconnected', detail: p.error || '', cls: 'lost' }),
  'voice.closed': (p) => ({ label: 'session closed', detail: p.reason || '', cls: 'lost' }),
  'voice.goaway': (p) => ({ label: 'GoAway', detail: `${fmtMs(p.time_left_ms)} left`, cls: 'recycle' }),
  // "New conversation" (docs/roles.md): the brain's transcript window
  // and the voice session both started fresh; conversation.reset is the
  // event both the SSE-fed live views and GET /api/conversation key off
  // (internal/web/conversation.go renders the same divider server-side).
  'conversation.reset': (p) => ({ label: 'new conversation', detail: p.via ? `via ${p.via}` : '', cls: 'reset' }),
};

/** Daemon lifecycle, shown as dividers by the swimlane only. */
export const DAEMON_DIVIDERS = {
  'daemon.start': (p) => ({ label: 'daemon started', detail: [p.version, p.herdr ? `herdr=${p.herdr}` : '', p.voice ? `voice=${p.voice}` : ''].filter(Boolean).join(' · '), cls: 'daemon' }),
  'daemon.stop': (p) => ({ label: 'daemon stopped', detail: p.reason || '', cls: 'daemon' }),
};

/**
 * One-line summary of a tool call's arguments for a collapsed row:
 * `agent="auth", instruction="skip the flaky test…"`.
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

/** Renders "provider/model" ("model" alone when the event carries no provider, as older brain events do). */
export function withProvider(provider, model) {
  if (!model) return provider || '';
  return provider ? `${provider}/${model}` : model;
}

/** Short one-line summary of a supervisor-side event (brain.step, herdr.read, pending.*, …). */
export function eventSummary(ev) {
  const p = ev.payload && typeof ev.payload === 'object' ? ev.payload : {};
  const type = ev.type || '';
  const parts = [];
  switch (true) {
    case type === 'brain.step':
      parts.push(p.template, withProvider(p.provider, p.model), p.dur_ms != null ? `${p.dur_ms} ms` : '', p.attempts > 1 ? `${p.attempts} attempts` : '', p.tool_calls != null ? `${p.tool_calls} tool call${p.tool_calls === 1 ? '' : 's'}` : '', p.error ? `error: ${p.error}` : '');
      break;
    case type === 'brain.compaction':
      parts.push(p.before_tokens != null || p.after_tokens != null ? `${p.before_tokens ?? '?'} → ${p.after_tokens ?? '?'} tokens` : '', p.dropped != null ? `${p.dropped} dropped` : '', p.reason);
      break;
    case type === 'brain.push_decision':
      parts.push(`${p.urgency_requested || 'no brain line'} → ${p.urgency_applied || '?'}`, p.reason, p.ceiling ? `ceiling ${p.ceiling}` : '', p.via ? `via ${p.via}` : '', p.late ? 'late' : '', `${p.agent || ''} ${p.event || ''}`.trim(), p.spoken || p.template);
      break;
    case type === 'brain.yielded':
      parts.push(p.turn != null ? `turn ${p.turn}` : '', p.step != null ? `before step ${p.step}` : '', p.agent ? `${p.agent} ${p.event || ''}`.trim() : '', p.ceo_waiting ? `${p.ceo_waiting} shepherd turn${p.ceo_waiting === 1 ? '' : 's'} waiting` : '');
      break;
    case type === 'brain.event_coalesced':
      parts.push(p.agent ? `${p.agent} ${p.event || ''}`.trim() : '', 'replaced the queued turn');
      break;
    case type === 'brain.model_changed':
      parts.push(`${withProvider(p.from_provider, p.from)} → ${withProvider(p.provider, p.to)}`, p.via ? `via ${p.via}` : '');
      break;
    case type.endsWith('.tool_call'):
      parts.push(p.name, argSummary(p.args), p.via ? `via ${p.via}` : '');
      break;
    case type.endsWith('.tool_result'):
      parts.push(p.name, p.dur_ms != null ? `${p.dur_ms} ms` : '', p.scheduling, p.cancelled ? 'cancelled' : '', p.error ? `error: ${p.error}` : '', typeof p.result === 'string' ? p.result : (p.result != null ? JSON.stringify(p.result) : ''), p.spoken);
      break;
    case type === 'cli.run':
      parts.push(p.backend, p.dur_ms != null ? `${p.dur_ms} ms` : '', p.exit != null ? `exit ${p.exit}` : '', p.killed ? 'killed' : '', p.capped ? 'capped' : '', p.resumed ? 'resumed' : '', p.session ? 'session kept' : '');
      break;
    case type.startsWith('investigation.'):
      parts.push(p.id, p.agent, p.backend, p.dur_ms != null ? fmtMs(p.dur_ms) : '', p.follow_up_of ? `follows ${p.follow_up_of}` : '',
        p.bytes != null ? `${p.bytes} bytes` : '', p.session_id_present ? 'session kept' : '', p.reason, p.error ? `error: ${p.error}` : '', p.question);
      break;
    case type === 'herdr.read':
      parts.push(p.source, p.lines != null ? `${p.lines} lines` : '', p.error ? `error: ${p.error}` : '');
      break;
    case type === 'herdr.send':
      parts.push(p.kind, p.keys ? `keys ${JSON.stringify(p.keys)}` : '', p.text ? JSON.stringify(p.text) : '', p.error ? `error: ${p.error}` : '');
      break;
    case type === 'herdr.transition':
      parts.push(`${p.from || '?'} → ${p.to || '?'}`, p.workspace_label || p.name ? `${p.workspace_label || ''}${p.name ? `-${p.name}` : ''}` : '', p.agent, p.source, p.closed ? 'closed' : '');
      break;
    case type === 'herdr.state' || type === 'herdr.dedup':
      parts.push(p.state, p.agent, p.source);
      break;
    case type === 'herdr.fleet':
      parts.push(p.kind, p.pane_id, p.workspace_id);
      break;
    case type.startsWith('pending.'):
      parts.push(p.kind, p.id, p.answer ? `answer ${p.answer}` : '', p.keys ? `keys ${JSON.stringify(p.keys)}` : '', p.reason, p.question);
      break;
    case type === 'note.created':
      parts.push(p.text);
      break;
    case type.startsWith('supervisor.'):
      parts.push(p.error, p.text, p.reason, p.chattiness ? `chattiness ${p.chattiness}` : '');
      break;
    default:
      parts.push(JSON.stringify(p).slice(0, 120));
  }
  return parts.filter((x) => x != null && x !== '').join(' · ');
}

/**
 * ToolPairer matches voice.tool_result events to their voice.tool_call by
 * id, falling back to the most recent open call with the same name when
 * the id is missing or unknown. `open()` registers a call item, `close()`
 * returns the matching item (or null) and removes it from the open set.
 */
export class ToolPairer {
  constructor() {
    this.byId = new Map();
    this.stack = []; // open items, oldest first
  }

  open(id, item) {
    if (id) this.byId.set(id, item);
    this.stack.push(item);
    return item;
  }

  close(id, name) {
    let item = id ? this.byId.get(id) : null;
    if (!item) {
      for (let i = this.stack.length - 1; i >= 0; i--) {
        if (this.stack[i].name === name) { item = this.stack[i]; break; }
      }
    }
    if (!item) return null;
    if (id) this.byId.delete(id);
    const own = [...this.byId.entries()].find(([, v]) => v === item);
    if (own) this.byId.delete(own[0]);
    this.stack = this.stack.filter((t) => t !== item);
    return item;
  }

  /** The most recently opened call still waiting for its result. */
  get last() {
    return this.stack.length ? this.stack[this.stack.length - 1] : null;
  }

  get size() {
    return this.stack.length;
  }
}

/**
 * Fragments merges transcript fragments (voice.transcript.in/out arrive
 * as partial text) into one item until `final` or an explicit close().
 * `create(ts)` makes the item on the first fragment.
 */
export class Fragments {
  constructor(create) {
    this.create = create;
    this.item = null;
  }

  add(ts, text, final) {
    if (!this.item) {
      this.item = this.create(ts);
      this.item.text = '';
      this.item.fragments = 0;
      this.item.final = false;
    }
    const it = this.item;
    it.text += text;
    it.fragments++;
    if (final) this.close();
    return it;
  }

  close() {
    const it = this.item;
    if (it) {
      it.final = true;
      this.item = null;
    }
    return it;
  }

  get open() {
    return this.item;
  }
}

/**
 * Dedupe remembers encoded log lines so the backfill (/api/events) and the
 * SSE replay do not show the same event twice; bounded to `max` entries.
 */
export class Dedupe {
  constructor(max = 4000) {
    this.max = max;
    this.seen = new Set();
    this.queue = [];
  }

  /** Returns true when `key` is new. */
  add(key) {
    if (this.seen.has(key)) return false;
    this.seen.add(key);
    this.queue.push(key);
    if (this.queue.length > this.max) this.seen.delete(this.queue.shift());
    return true;
  }
}
