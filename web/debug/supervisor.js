// Supervisor tab logic for /debug: turns the brain's transcript
// (GET /api/brain/transcript), its context window (GET /api/brain/context)
// and the brain.* events into the items the tab renders, and the context
// into the prompt-budget meter's segments. Pure functions, no DOM,
// unit-tested with `make test-web` (decision D10: the tab is a viewer of
// the brain's own store; nothing here talks to the daemon).
//
// Item kinds (every message item carries `seq`, `ts`, `turn`, `inWindow`,
// `tokens` and `raw`, the API message for its JSON collapsible):
//   turn         a thin header before the first message of a brain turn,
//                with the matching brain.turn / brain.fallback data
//   marker       the window boundary: everything before it is on disk
//                only (compacted away or never in the window)
//   ceo          the CEO's verbatim words (ceo_utterance)
//   assistant    what the assistant said (assistant_said)
//   push         a push the supervisor sent, with its urgency
//   worker       a herdr transition ("auth: working → done")
//   request      the assistant's ask / propose as the SVP received it
//                (or a nudge: "Time is up", "That was not the JSON")
//   tool         brain_tool_call paired with its brain_tool_result by id
//   reply        the SVP's answer-contract reply (parsed when it is JSON)
//   compaction   a compaction_summary with the compaction's numbers
//   note         a note the CEO asked to be remembered
//   promise      a promise record (made by the promise tool, or kept /
//                expired / cancelled by the supervisor), with the status
//                the promise.* events report since
//   screen       the pane read the supervisor handed an event turn
//                (event_screen), pruned like a tool result
//   other        any kind this file does not know yet
import { argSummary } from './grouping.js';

/** The transcript kinds (internal/brain MessageKind) and their meter group. */
export const KIND_GROUPS = {
  ceo_utterance: { id: 'ceo', label: 'CEO' },
  assistant_said: { id: 'assistant', label: 'assistant' },
  push: { id: 'push', label: 'pushes' },
  worker_event: { id: 'worker', label: 'worker events' },
  note: { id: 'note', label: 'notes' },
  request: { id: 'request', label: 'requests' },
  brain_tool_call: { id: 'tool', label: 'tool calls' },
  brain_tool_result: { id: 'result', label: 'tool results' },
  reply: { id: 'reply', label: 'SVP replies' },
  compaction_summary: { id: 'compaction', label: 'summaries' },
  promise: { id: 'promise', label: 'promises' },
  event_screen: { id: 'result', label: 'tool results' },
};

/** Meter group order (transcript side), then the pruned stubs. */
export const GROUP_ORDER = ['compaction', 'ceo', 'assistant', 'push', 'worker', 'note', 'promise', 'request', 'tool', 'result', 'reply', 'other'];

const reRequest = /^Request from the assistant \((ask|propose)\): (?:question|instruction)=("(?:[^"\\]|\\.)*")(?: agent=("(?:[^"\\]|\\.)*"|\(none[^)]*\)))?(?: \[triggered by an event[^\]]*\])?/;
const reEvent = /^Event from the supervisor \((summarize|keep_promise)\): agent=("(?:[^"\\]|\\.)*") event=("(?:[^"\\]|\\.)*") template=("(?:[^"\\]|\\.)*")(?: promise=("(?:[^"\\]|\\.)*"))?/;

function unquote(s) {
  try { return JSON.parse(s); } catch (_) { return s; }
}

/**
 * Parses a request message (assemble.go requestText) into reqKind (ask /
 * propose), question, agent and trigger. A nudge ("Time is up…", "That
 * was not the JSON…") has reqKind null and `nudge: true`.
 */
export function parseRequest(content) {
  const m = reRequest.exec(content || '');
  if (!m) {
    const e = reEvent.exec(content || '');
    if (e) return { reqKind: e[1], question: unquote(e[4]), agent: unquote(e[2]), event: unquote(e[3]), promise: e[5] ? unquote(e[5]) : '', trigger: 'event', nudge: false };
    return { reqKind: null, question: content || '', agent: '', trigger: 'ceo', nudge: true };
  }
  const agent = m[3] && m[3].startsWith('"') ? unquote(m[3]) : '';
  return { reqKind: m[1], question: unquote(m[2]), agent, trigger: /\[triggered by an event/.test(content) ? 'event' : 'ceo', nudge: false };
}

/** The promise statuses in the order a promise moves through them. */
export const PROMISE_STATUSES = ['open', 'kept', 'expired', 'cancelled'];

/** Parses a promise message: the record on the message, or the content line. */
export function parsePromise(m) {
  const p = m && m.promise && typeof m.promise === 'object' ? m.promise : {};
  const line = /^Promise \((\w+)\): ("(?:[^"\\]|\\.)*"), (.*)$/.exec((m && m.content) || '');
  return {
    id: p.id || '', text: p.text || (line ? unquote(line[2]) : (m && m.content) || ''), agent: p.agent || (m && m.agent) || '', event: p.event || '',
    status: p.status || (line ? line[1] : 'open'), condition: line ? line[3] : '', createdTurn: p.created_turn || 0, due: p.due || null,
  };
}

/** Parses a reply message: the answer contract JSON, or the raw text when it is not one (a corrected bad reply). */
export function parseReply(content) {
  const text = (content || '').trim();
  const start = text.indexOf('{');
  const end = text.lastIndexOf('}');
  if (start >= 0 && end > start) {
    try {
      const o = JSON.parse(text.slice(start, end + 1));
      if (o && typeof o === 'object' && typeof o.spoken === 'string') {
        return { spoken: o.spoken, detail: o.detail || '', needsUser: !!o.needs_user, clarify: !!o.clarify, options: Array.isArray(o.options) ? o.options : [], json: true };
      }
    } catch (_) { /* not JSON */ }
  }
  return { spoken: text, detail: '', needsUser: false, clarify: false, options: [], json: false };
}

/** Parses a push message "(urgency) text". */
export function parsePush(content) {
  const m = /^\((\w+)\) ([\s\S]*)$/.exec(content || '');
  return m ? { urgency: m[1], text: m[2] } : { urgency: '', text: content || '' };
}

/** Parses a worker event "alias: from -> to" / "alias: pane closed". */
export function parseWorkerEvent(content) {
  let m = /^(.*?): (.+?) -> (.+)$/.exec(content || '');
  if (m) return { agent: m[1], from: m[2], to: m[3], closed: false };
  m = /^(.*?): pane closed$/.exec(content || '');
  if (m) return { agent: m[1], from: '', to: '', closed: true };
  return { agent: '', from: '', to: '', closed: false, text: content || '' };
}

/**
 * TurnLog collects the brain.* events the tab correlates with the
 * transcript: brain.turn and brain.fallback by turn number, brain.step
 * counts, brain.compaction in order, and the promise.* events by promise
 * id (the latest status of each promise, so a card made in one turn can
 * show it was kept later). A daemon.start resets the turns: the event
 * backfill spans the day, and turn numbers restart with the run; the
 * promises survive a restart, so their statuses are kept.
 */
export class TurnLog {
  constructor() {
    this.turns = new Map(); // turn → info
    this.compactions = [];
    this.promises = new Map(); // promise id → { status, ts, spoken, via, urgency }
    this.runs = 0;
  }

  info(n) {
    let t = this.turns.get(n);
    if (!t) {
      t = { turn: n, steps: 0, modelCalls: 0, durMs: null, fallback: false, reason: '', trigger: '', kind: '', tokensIn: 0, tokensOut: 0, windowTokens: null, acted: 0, complete: false };
      this.turns.set(n, t);
    }
    return t;
  }

  /** Feeds one log event; returns true when it was one of ours. */
  add(ev) {
    const type = ev.type || '';
    const p = ev.payload && typeof ev.payload === 'object' ? ev.payload : {};
    switch (type) {
      case 'daemon.start':
        this.turns.clear();
        this.compactions = [];
        this.runs++;
        return true;
      case 'brain.turn': {
        const t = this.info(p.turn);
        Object.assign(t, {
          trigger: p.trigger || '', kind: p.kind || '', steps: p.steps ?? t.steps, modelCalls: p.model_calls ?? t.modelCalls, durMs: p.dur_ms ?? null,
          fallback: !!p.fallback, tokensIn: p.tokens_in || 0, tokensOut: p.tokens_out || 0, windowTokens: p.window_tokens ?? null, acted: p.acted || 0, complete: true, ts: ev.ts,
        });
        return true;
      }
      case 'brain.fallback': {
        if (!p.turn) return true;
        const t = this.info(p.turn);
        t.fallback = t.fallback || !p.acted;
        t.reason = p.reason || t.reason;
        t.error = p.error || t.error;
        return true;
      }
      case 'brain.step': {
        const t = this.info(p.turn);
        if (!t.complete) {
          t.modelCalls++;
          t.steps = Math.max(t.steps, p.step || 0);
        }
        return true;
      }
      case 'brain.compaction':
        if (p.dropped != null) this.compactions.push({ ts: ev.ts, turn: p.turn, before: p.before_tokens, after: p.after_tokens, dropped: p.dropped, durMs: p.dur_ms });
        return true;
      case 'promise.created':
      case 'promise.kept':
      case 'promise.expired':
      case 'promise.cancelled': {
        if (!p.id) return true;
        const status = type.slice('promise.'.length);
        const prev = this.promises.get(p.id);
        // A later transition wins over an earlier one; created never
        // overrides a resolution that was already seen (backfill order).
        if (!prev || status !== 'created' || prev.status === 'open') {
          this.promises.set(p.id, { status: status === 'created' ? 'open' : status, ts: ev.ts, spoken: p.spoken || '', via: p.via || '', urgency: p.urgency || '', text: p.text || (prev && prev.text) || '', agent: p.agent || (prev && prev.agent) || '', event: p.event || (prev && prev.event) || '' });
        }
        return true;
      }
      default:
        return false;
    }
  }

  turn(n) {
    return this.turns.get(n) || null;
  }

  /** The latest known state of promise id, or null. */
  promise(id) {
    return this.promises.get(id) || null;
  }
}

/**
 * windowInfo reduces /api/brain/context to what the transcript needs:
 * which seqs are in the window, which are pruned (seq → stub), the
 * lowest in-window seq (the marker goes before it) and the numbers.
 */
export function windowInfo(context) {
  if (!context || !Array.isArray(context.window)) return { known: false, seqs: new Set(), pruned: new Map(), first: 0, tokens: 0, budget: 0 };
  const seqs = new Set();
  const pruned = new Map();
  let first = 0;
  for (const w of context.window) {
    seqs.add(w.seq);
    if (w.pruned) pruned.set(w.seq, w.stub || '');
    if (!first || w.seq < first) first = w.seq;
  }
  return { known: true, seqs, pruned, first, tokens: context.estimated_tokens || 0, budget: context.window_tokens || 0, count: context.window.length };
}

/**
 * buildTranscriptView turns transcript messages (oldest first, each with
 * its seq) plus the context and the TurnLog into the tab's items.
 * opts: hideWorker, hideAssistant, windowOnly.
 */
export function buildTranscriptView(messages, context, turns, opts = {}) {
  const win = windowInfo(context);
  const log = turns || new TurnLog();
  const items = [];
  const openTools = new Map(); // tool call id → tool item
  let markerDone = !win.known;
  let lastTurn = 0;
  let onDisk = 0;
  let compactionIdx = 0;
  const lastComp = context && context.last_compaction ? context.last_compaction : null;

  for (const m of messages) {
    if (!m || typeof m.seq !== 'number') continue;
    const inWindow = win.known ? win.seqs.has(m.seq) : undefined;
    if (win.known && !inWindow) onDisk++;
    if (opts.windowOnly && win.known && !inWindow) continue;
    if (opts.hideWorker && m.kind === 'worker_event') continue;
    if (opts.hideAssistant && m.kind === 'assistant_said') continue;

    // The window boundary sits before the first message the model still
    // sees (before the whole tool item when that message is a result).
    if (!markerDone && inWindow) {
      const marker = { kind: 'marker', key: 'marker', seq: m.seq, ts: m.ts, onDisk, inWindow: win.count, tokens: win.tokens, budget: win.budget };
      const paired = m.kind === 'brain_tool_result' && m.tool_result && openTools.get(m.tool_result.id);
      if (paired) items.splice(items.indexOf(paired), 0, marker); else items.push(marker);
      markerDone = true;
    }
    // A thin header where a brain turn starts.
    if (m.turn && m.turn !== lastTurn) {
      lastTurn = m.turn;
      const info = log.turn(m.turn);
      const req = m.kind === 'request' ? parseRequest(m.content) : null;
      items.push({
        kind: 'turn', key: `turn:${m.turn}`, seq: m.seq, ts: m.ts, turn: m.turn,
        trigger: (info && info.trigger) || (req && req.trigger) || '', reqKind: (info && info.kind) || (req && req.reqKind) || '',
        modelCalls: info ? info.modelCalls : null, durMs: info ? info.durMs : null, fallback: !!(info && info.fallback), reason: info ? info.reason : '',
        tokensIn: info ? info.tokensIn : 0, tokensOut: info ? info.tokensOut : 0, windowTokens: info ? info.windowTokens : null, acted: info ? info.acted : 0, complete: !!(info && info.complete),
      });
    }

    const base = { key: `m:${m.seq}`, seq: m.seq, ts: m.ts, turn: m.turn || 0, inWindow, tokens: m.tokens || 0, agent: m.agent || '', raw: m };
    switch (m.kind) {
      case 'ceo_utterance':
        items.push({ ...base, kind: 'ceo', text: m.content || '' });
        break;
      case 'assistant_said':
        items.push({ ...base, kind: 'assistant', text: m.content || '' });
        break;
      case 'push':
        items.push({ ...base, kind: 'push', ...parsePush(m.content) });
        break;
      case 'worker_event':
        items.push({ ...base, kind: 'worker', ...parseWorkerEvent(m.content) });
        break;
      case 'note':
        items.push({ ...base, kind: 'note', text: m.content || '' });
        break;
      case 'promise': {
        const p = parsePromise(m);
        const latest = p.id ? log.promise(p.id) : null;
        // The message records one transition; the card also shows where
        // the promise stands now (from the events), when that is later.
        const now = latest && PROMISE_STATUSES.indexOf(latest.status) > PROMISE_STATUSES.indexOf(p.status) ? latest.status : p.status;
        items.push({ ...base, kind: 'promise', ...p, now, latest });
        break;
      }
      case 'event_screen': {
        const tr = m.tool_result || {};
        items.push({ ...base, kind: 'screen', content: m.content || '', summary: tr.summary || '', pruned: win.pruned.has(m.seq), stub: win.pruned.get(m.seq) || '' });
        break;
      }
      case 'request':
        items.push({ ...base, kind: 'request', ...parseRequest(m.content) });
        break;
      case 'brain_tool_call': {
        const tc = m.tool_call || {};
        const it = { ...base, kind: 'tool', id: tc.id || '', name: tc.name || '?', args: tc.args || {}, summary: argSummary(tc.args || {}), thought: m.content || '', result: null, pending: true };
        if (tc.id) openTools.set(tc.id, it);
        items.push(it);
        break;
      }
      case 'brain_tool_result': {
        const tr = m.tool_result || {};
        const result = { seq: m.seq, ts: m.ts, content: m.content || '', error: tr.error || '', durMs: tr.dur_ms ?? null, summary: tr.summary || '', tokens: m.tokens || 0, raw: m, inWindow, pruned: win.pruned.has(m.seq), stub: win.pruned.get(m.seq) || '' };
        const it = tr.id ? openTools.get(tr.id) : null;
        if (it) {
          openTools.delete(tr.id);
          it.result = result;
          it.pending = false;
          it.inWindow = it.inWindow || inWindow;
          it.tokens += result.tokens;
        } else {
          items.push({ ...base, kind: 'tool', id: tr.id || '', name: tr.name || '?', args: {}, summary: '', result, pending: false, orphan: true });
        }
        break;
      }
      case 'reply':
        items.push({ ...base, kind: 'reply', ...parseReply(m.content) });
        break;
      case 'compaction_summary': {
        let info = null;
        if (lastComp && lastComp.summary_seq === m.seq) info = { before: lastComp.before_tokens, after: lastComp.after_tokens, dropped: lastComp.dropped, at: lastComp.at };
        else if (log.compactions[compactionIdx]) info = log.compactions[compactionIdx];
        compactionIdx++;
        items.push({ ...base, kind: 'compaction', summary: m.content || '', info });
        break;
      }
      default:
        items.push({ ...base, kind: 'other', text: m.content || '', rawKind: m.kind || '' });
    }
  }
  if (!markerDone && win.known && messages.length) {
    // Nothing in the window at all (everything compacted away): the
    // marker closes the list.
    items.push({ kind: 'marker', key: 'marker', seq: 0, ts: '', onDisk, inWindow: 0, tokens: win.tokens, budget: win.budget });
  }
  return items;
}

/**
 * meterSegments turns /api/brain/context into the prompt-budget meter:
 * the system prompt sections (re-sent on every call, outside the window
 * budget) and the window's messages grouped by kind, with the pruned
 * stubs as their own segment. The transcript segments sum to
 * estimated_tokens; both bars share one scale (tokens per pixel) so the
 * prompt's size reads against the window's.
 */
export function meterSegments(context) {
  if (!context || !Array.isArray(context.window)) return { known: false, budget: 0, windowTokens: 0, promptTokens: 0, scale: 1, prompt: [], transcript: [], onDisk: 0, inWindow: 0 };
  const sp = context.system_prompt || { sections: [] };
  const prompt = (sp.sections || []).map((s) => ({ id: `prompt:${s.name}`, label: s.name, group: 'prompt', source: s.source || '', tokens: s.tokens || 0, chars: s.chars || 0 }));
  const promptTokens = sp.total_tokens != null ? sp.total_tokens : prompt.reduce((n, s) => n + s.tokens, 0);
  const byGroup = new Map();
  let pruned = { id: 'pruned', label: 'pruned stubs', group: 'pruned', tokens: 0, count: 0 };
  for (const w of context.window) {
    if (w.pruned) { pruned.tokens += w.tokens || 0; pruned.count++; continue; }
    const g = KIND_GROUPS[w.kind] || { id: 'other', label: w.kind || 'other' };
    let seg = byGroup.get(g.id);
    if (!seg) { seg = { id: `kind:${g.id}`, label: g.label, group: 'transcript', kind: g.id, tokens: 0, count: 0 }; byGroup.set(g.id, seg); }
    seg.tokens += w.tokens || 0;
    seg.count++;
  }
  const transcript = GROUP_ORDER.filter((id) => byGroup.has(id)).map((id) => byGroup.get(id));
  for (const [id, seg] of byGroup) if (!GROUP_ORDER.includes(id)) transcript.push(seg);
  if (pruned.count) transcript.push(pruned); else pruned = null;
  const windowTokens = context.estimated_tokens != null ? context.estimated_tokens : transcript.reduce((n, s) => n + s.tokens, 0);
  const budget = context.window_tokens || 0;
  const scale = Math.max(budget, windowTokens, promptTokens, 1);
  return {
    known: true, budget, windowTokens, promptTokens, scale, prompt, transcript,
    onDisk: Math.max(0, (context.transcript_len || 0) - context.window.length), inWindow: context.window.length,
    over: budget > 0 && windowTokens > budget,
  };
}

/** The "Brain off" state from a 503 body: the reason in the parentheses when there is one. */
export function offState(status, body) {
  if (status !== 503) return null;
  const msg = body && body.error ? String(body.error) : 'the supervisor brain is off';
  const m = /\(([^)]+)\)\s*$/.exec(msg);
  return { off: true, reason: m ? m[1] : msg, message: msg };
}

/**
 * Messages from a raw JSONL transcript (the on-disk file, or
 * testdata/brain/sample-run.jsonl) get their seq from their line
 * number, as the daemon assigns it.
 */
export function parseTranscriptJSONL(text) {
  const out = [];
  for (const line of String(text || '').split('\n')) {
    const s = line.trim();
    if (!s) continue;
    try {
      const m = JSON.parse(s);
      if (m && typeof m === 'object') out.push({ ...m, seq: out.length + 1 });
    } catch (_) { /* skip a bad line */ }
  }
  return out;
}

/** Formats a token count: "12.4k", "980". */
export function fmtTokens(n) {
  if (n == null || Number.isNaN(Number(n))) return '?';
  const v = Number(n);
  return v >= 1000 ? `${(v / 1000).toFixed(1)}k` : String(Math.round(v));
}
