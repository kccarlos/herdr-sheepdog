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
//                  events that led to it nested; a push answering an
//                  investigation (answers_job) links back to the
//                  acknowledgement card that started the job; `deferred`
//                  marks a requested INTERRUPT the model was speaking
//                  through and so was sent WHEN_IDLE instead (a tool
//                  result's `deferred` is the same, on its `result`)

import { NESTED_TYPES, DIVIDERS, fmtMs, estimateTokens, argSummary, ToolPairer, Fragments, toolFamily } from './grouping.js';

// Re-exported for the tests and older importers; the definitions live in
// grouping.js, shared with the swimlane.
export { NESTED_TYPES, fmtMs, estimateTokens, argSummary };

/**
 * Grouper folds events one at a time (the page feeds the SSE stream into
 * it) and keeps `items` current, including the in-progress user or
 * assistant message at the end. groupEvents() is the batch form.
 */
export class Grouper {
  constructor() {
    this.items = [];
    this.seq = 0;
    this.tools = new ToolPairer(); // voice tool calls waiting for their result
    this.inner = new Map(); // prefix → ToolPairer for nested families (brain.*)
    this.loose = []; // nested-type events with no open tool, for the next push
    this.userFrag = new Fragments((ts) => this.push({ kind: 'user', ts, typed: false }));
    this.assistantFrag = new Fragments((ts) => this.push({ kind: 'assistant', ts }));
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
      case 'brain.transcript':
        // Bookkeeping only ("message N was ingested"); the Supervisor
        // tab fetches the message itself. Not a nested row here.
        return;
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
      case 'voice.push': {
        this.closeUser();
        this.closeAssistant();
        // scheduling_sent differs from the requested urgency only when a
        // speaking model deferred an INTERRUPT to WHEN_IDLE
        // (docs/roles.md "Pushes"); voice.push_deferred carries the reason.
        const deferred = p.urgency === 'interrupt' && p.scheduling_sent === 'when_idle';
        const item = this.push({
          kind: 'push', ts: ev.ts, text: p.text || '', urgency: p.urgency || 'idle', skipped: !!p.skipped,
          deferred, answersJob: p.answers_job || '', nested: this.loose.splice(0), expandable: true,
        });
        // An investigation's answer: link it to the acknowledgement the
        // shepherd heard when the job started, and that card back to it.
        if (item.answersJob) {
          const ack = this.lastOf((x) => x.kind === 'tool' && x.job === item.answersJob);
          if (ack) {
            ack.answeredBy = item.key;
            item.answers = ack.key;
          }
        }
        return;
      }
      case 'voice.turn':
        this.closeUser();
        this.closeAssistant();
        this.applyTurn(p);
        return;
      case 'voice.interrupted':
        if (this.assistantFrag.open) {
          this.assistantFrag.open.interrupted = true;
          this.closeAssistant();
        }
        return;
      case 'voice.usage': {
        const last = this.lastOf((it) => it.kind === 'assistant' || it.kind === 'tool');
        if (last) last.usage = { prompt: p.prompt, response: p.response, total: p.total };
        return;
      }
      case 'investigation.started': {
        // The job a pending acknowledgement is about: its id is what the
        // answering push carries (answers_job).
        const open = this.tools.last;
        if (open && p.id) open.job = p.id;
        this.nest(ev);
        return;
      }
      default: {
        // Any other `<prefix>.tool_call` / `<prefix>.tool_result` family
        // (the supervisor's agent loop, brain.*) nests under the voice
        // call in flight, with what happened between call and result
        // nested one level deeper.
        const fam = toolFamily(type);
        if (fam) return fam.phase === 'call' ? this.innerCall(ev, p, fam.prefix) : this.innerResult(ev, p, fam.prefix);
        if (NESTED_TYPES.test(type)) this.nest(ev);
      }
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
    const fresh = !this.userFrag.open;
    const item = this.userFrag.add(ts, text, final);
    if (fresh) this.turn = { user: item, first: null };
    item.typed = item.typed || typed;
  }

  closeUser() {
    this.userFrag.close();
  }

  assistantFragment(ts, text, final) {
    const fresh = !this.assistantFrag.open;
    const item = this.assistantFrag.add(ts, text, final);
    if (fresh) this.firstResponse(item);
  }

  closeAssistant() {
    this.assistantFrag.close();
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
    this.tools.open(item.id, item);
  }

  toolResult(ev, p) {
    // By id, else the most recent open call with the same name.
    let item = this.tools.close(p.id, p.name);
    if (!item) {
      item = this.push({ kind: 'tool', ts: ev.ts, id: p.id || '', name: p.name || '?', args: {}, via: p.via || '', summary: '', nested: [], result: null, pending: true, expandable: true, orphan: true });
    }
    item.pending = false;
    item.result = {
      spoken: p.spoken || '', clarify: !!p.clarify, scheduling: p.scheduling || '', durMs: p.dur_ms,
      error: p.error || '', cancelled: !!p.cancelled, sendError: p.send_error || '',
      // The real answer follows on its own (an investigation).
      pendingAnswer: !!p.pending,
      // A speaking model deferred an INTERRUPT result to WHEN_IDLE
      // (docs/roles.md "Pushes"); voice.push_deferred carries the reason.
      deferred: p.scheduling === 'interrupt' && p.scheduling_sent === 'when_idle',
    };
    item.resultTs = ev.ts;
    // Without a voice model the daemon speaks the result itself (the /voice
    // page shows it as an out transcript): render it as the assistant.
    if (item.via && item.via !== 'voice' && !p.cancelled && item.result.spoken) {
      this.closeAssistant();
      const a = this.push({ kind: 'assistant', ts: ev.ts, text: item.result.spoken, fragments: 1, final: true, synthetic: true });
      if (this.turn && this.turn.first === item) this.turn.first = a;
    }
  }

  /** Files a supervisor-side event under the innermost open call (an inner brain call, else the voice call), or the loose list. */
  nest(ev, inner = true) {
    if (inner) {
      for (const pairer of this.inner.values()) {
        if (pairer.last) { pairer.last.children.push(ev); return; }
      }
    }
    const open = this.tools.last;
    if (open) open.nested.push(ev);
    else this.loose.push(ev);
  }

  innerPairer(prefix) {
    let pairer = this.inner.get(prefix);
    if (!pairer) { pairer = new ToolPairer(); this.inner.set(prefix, pairer); }
    return pairer;
  }

  /** A nested tool call: a copy of the event with its children and result. */
  innerCall(ev, p, prefix) {
    const node = { ...ev, id: p.id || '', name: p.name || '?', children: [], result: null, pending: true };
    this.nest(node);
    this.innerPairer(prefix).open(node.id, node);
  }

  innerResult(ev, p, prefix) {
    const node = this.innerPairer(prefix).close(p.id, p.name);
    if (!node) { this.nest(ev, false); return; } // orphan: a plain row at the outer level
    node.result = ev;
    node.pending = false;
    node.durMs = p.dur_ms != null ? Number(p.dur_ms) : Date.parse(ev.ts) - Date.parse(node.ts);
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
