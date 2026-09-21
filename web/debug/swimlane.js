// Swimlane (sequence) layout for /debug: turns the flat event log into
// time-ordered rows across four lanes — Shepherd · Voice · Brain · Flock /
// herdr / LLM / CLI — with arrows for the interactions between them.
// Pure functions, unit-tested with `make test-web`; the DOM rendering is
// swimlane-view.js. Decision D10: the page is a viewer of the log.
//
// buildRows(events, opts) → { rows, chains }
//   rows: in time order, each one of
//     card     one event (or one merged transcript) in a lane, optionally
//              with an arrow to another lane and a chain id
//     divider  session boundary (voice lifecycle, daemon start/stop,
//              system prompt), full width
//     gap      compressed time: "12 s" between two rows further apart
//              than opts.gapMs
//   chains: chain id → {kind, root} for the highlight legend
//
// Chains group what belongs together across lanes (a click on an arrow
// highlights its chain): `turn:N` is a user utterance with the tool calls
// it caused, their supervisor-side events, results and the speech that
// followed; `loose:N` is supervisor work no tool call started (a herdr
// transition → read → llm.call → pending.created) and the push it led to.
//
// Tool call/result pairing is generic over any `<prefix>.tool_call` /
// `<prefix>.tool_result` family with an `id` (grouping.js toolFamily):
// voice.* calls open a chain; any other family (the supervisor's agent
// loop, brain.*) is an inner call nested under the voice call in flight,
// and the herdr.read / cli.run / llm.call cards between an inner call and
// its result carry `parentKey` = that inner call.
import { NESTED_TYPES, DIVIDERS, DAEMON_DIVIDERS, argSummary, eventSummary, fmtMs, ToolPairer, Fragments, toolFamily, withProvider } from './grouping.js';

// The names of docs/roles.md: the shepherd operates, the dog's two
// halves are its voice and its brain, the flock are the herdr workers.
// Lane ids stay as they are: they key the event routing, not the label.
export const LANES = [
  { id: 'you', label: 'Shepherd' },
  { id: 'voice', label: 'Voice' },
  { id: 'sup', label: 'Brain' },
  { id: 'herdr', label: 'Flock / CLI' },
];
const LANE_INDEX = Object.fromEntries(LANES.map((l, i) => [l.id, i]));

/** Rows further apart than this get a gap marker between them. */
export const GAP_MS = 5000;
/** A turn that took this long to answer is shown in red. */
export const STALL_MS = 10000;
/** Latency colour ramp for llm.call / cli.run / tool results. */
export const LATENCY = { warn: 1000, slow: 5000 };

/** Lane of an event by its source (the fallback when no rule matches). */
export function laneOfSource(source) {
  switch (source) {
    case 'web': return 'you';
    case 'voice': return 'voice';
    case 'herdr': case 'llm': case 'cli': return 'herdr';
    default: return 'sup';
  }
}

/** Latency level for the colour ramp: ok / warn / slow. */
export function latencyLevel(ms) {
  if (ms == null) return null;
  if (ms >= LATENCY.slow) return 'slow';
  if (ms >= LATENCY.warn) return 'warn';
  return 'ok';
}

const HERDR_STATES = new Set(['blocked', 'working', 'done', 'idle', 'unknown']);
function stateClass(state) {
  return `state-${HERDR_STATES.has(state) ? state : 'unknown'}`;
}

function ms(ts) {
  const t = Date.parse(ts);
  return Number.isNaN(t) ? 0 : t;
}

function clip(text, max = 140) {
  const s = String(text || '').replace(/\s+/g, ' ').trim();
  return s.length > max ? s.slice(0, max - 1) + '…' : s;
}

/**
 * Builder folds events one at a time into cards and dividers (before
 * filters and gaps, which need the whole list — see buildRows).
 */
class Builder {
  constructor() {
    this.rows = [];
    this.chains = new Map();
    this.n = 0;
    this.chainSeq = 0;
    this.tools = new ToolPairer(); // voice tool calls waiting for their result
    this.inner = new Map(); // prefix → ToolPairer for nested families (brain.*)
    this.inFrag = new Fragments((ts) => this.card({ ts, lane: 'you', type: 'voice.transcript.in', source: 'voice', title: 'the shepherd said', events: [], merged: true }));
    this.outFrag = new Fragments((ts) => this.card({ ts, lane: 'voice', type: 'voice.transcript.out', source: 'voice', title: '🔊 spoke', events: [], merged: true }));
    this.turn = null; // { chain, input } — the user turn tool calls adopt
    this.loose = null; // { chain, work } — supervisor work with no tool call open
    this.trigger = null; // { kind, chain, card } — what the next speech answers
    this.decisions = []; // brain.push_decision payloads not yet matched to their voice.push
  }

  newChain(kind, root) {
    const id = `${kind}:${++this.chainSeq}`;
    this.chains.set(id, { kind, root });
    return id;
  }

  card(fields) {
    const row = { kind: 'card', key: `${fields.type}:${++this.n}`, tms: ms(fields.ts), chain: null, arrow: null, cls: [], ...fields };
    this.rows.push(row);
    return row;
  }

  divider(ev, p, d) {
    this.closeFragments();
    this.rows.push({ kind: 'divider', key: `${ev.type}:${++this.n}`, ts: ev.ts, tms: ms(ev.ts), type: ev.type, source: ev.source, pane: ev.pane_id || '', event: ev, ...d });
  }

  closeFragments() {
    this.inFrag.close();
    this.outFrag.close();
  }

  /** Card for one plain event in a lane: title = type, summary from the payload. */
  plain(ev, lane, extra = {}) {
    const p = ev.payload && typeof ev.payload === 'object' ? ev.payload : {};
    return this.card({
      ts: ev.ts, lane, type: ev.type, source: ev.source, pane: ev.pane_id || '', event: ev,
      title: ev.type, summary: eventSummary(ev), durMs: p.dur_ms != null ? Number(p.dur_ms) : (ev.dur_ms != null ? Number(ev.dur_ms) : null),
      ...extra,
    });
  }

  add(ev) {
    const type = ev.type || '';
    const p = ev.payload && typeof ev.payload === 'object' ? ev.payload : {};
    if (DIVIDERS[type]) return this.divider(ev, p, { ...DIVIDERS[type](p), lane: 'voice' });
    if (DAEMON_DIVIDERS[type]) return this.divider(ev, p, { ...DAEMON_DIVIDERS[type](p), lane: 'sup' });
    switch (type) {
      case 'voice.system_prompt':
        return this.divider(ev, p, { lane: 'voice', label: 'system prompt', detail: `~${Math.round((p.text || '').length / 4)} tokens${p.model ? ` · ${withProvider(p.provider, p.model)}` : ''}${p.aliases && p.aliases.length ? ` · aliases ${p.aliases.join(', ')}` : ''}`, cls: 'prompt', text: p.text || '' });
      case 'voice.transcript.in':
        return this.input(ev, p.text || '', !!p.final, false);
      case 'voice.text':
        this.closeFragments();
        return this.input(ev, p.text || '', true, true);
      case 'web.utterance':
        // via=voice is followed by voice.text (the model path); the text
        // router path has no other record of what the user typed.
        if (p.via === 'voice') return null;
        this.closeFragments();
        return this.input(ev, p.text || '', true, true, 'sup');
      case 'voice.transcript.out':
        return this.speech(ev, p.text || '', !!p.final);
      case 'voice.tool_call':
        return this.toolCall(ev, p);
      case 'voice.tool_result':
        return this.toolResult(ev, p);
      case 'brain.step':
        return this.brainStep(ev, p);
      case 'brain.transcript':
        // Bookkeeping ("message N was ingested"): a stats card, hidden
        // with the other stats by default; the Supervisor tab shows the
        // message itself.
        return this.plain(ev, 'sup', { title: 'transcript', summary: `#${p.seq ?? '?'} ${p.kind || ''}${p.turn ? ` · turn ${p.turn}` : ''}`, cls: ['minor', 'stats', 'brain'], stats: true });
      case 'brain.compaction': {
        const card = this.plain(ev, 'sup', { title: 'compaction', cls: ['minor', 'brain'] });
        card.chain = this.workChain(card);
        return card;
      }
      case 'voice.push':
        return this.push(ev, p);
      case 'voice.turn':
        return this.turnEnd(ev, p);
      case 'voice.interrupted':
        this.outFrag.close();
        return this.plain(ev, 'voice', { title: 'interrupted', summary: `by ${p.by || '?'}${p.dropped_chunks ? ` · ${p.dropped_chunks} chunks dropped` : ''}`, cls: ['minor'] });
      case 'voice.usage':
        return this.plain(ev, 'voice', { title: 'usage', summary: `${p.prompt ?? '?'} prompt · ${p.response ?? '?'} response · ${p.total ?? '?'} total`, cls: ['minor', 'stats'], stats: true });
      case 'web.client.stats':
        return this.plain(ev, 'you', { title: 'client stats', summary: `${p.frames_in ?? 0} frames in · ${p.frames_out ?? 0} out${p.dropped_audio ? ` · ${p.dropped_audio} dropped` : ''}`, cls: ['minor', 'stats'], stats: true });
      case 'herdr.transition':
        return this.transition(ev, p);
      case 'herdr.state':
        return this.state(ev, p);
      default: {
        const fam = toolFamily(type);
        if (fam) return fam.phase === 'call' ? this.innerCall(ev, p, fam.prefix) : this.innerResult(ev, p, fam.prefix);
        return this.other(ev, p);
      }
    }
  }

  // ---- You ↔ Voice model

  /** A user utterance: merged transcript fragments or one typed line. */
  input(ev, text, final, typed, to = 'voice') {
    this.outFrag.close();
    const fresh = !this.inFrag.open;
    const card = this.inFrag.add(ev.ts, text, final);
    card.events.push(ev);
    card.typed = card.typed || typed;
    if (fresh) {
      card.pane = ev.pane_id || '';
      card.source = ev.source;
      card.type = ev.type;
      card.chain = this.newChain('turn', card);
      card.chainRoot = true;
      card.arrow = { from: 'you', to, kind: 'input', label: typed ? 'typed' : 'speech', dashed: false };
      this.turn = { chain: card.chain, input: card };
      this.trigger = { kind: 'input', chain: card.chain, card };
    }
    if (typed) {
      card.title = to === 'sup' ? 'you typed (text router)' : 'you typed';
      card.arrow.label = to === 'sup' ? 'typed · text router' : 'typed';
    }
    if (final) this.inFrag.close();
    return card;
  }

  /** The model speaking: merged output fragments, one card per turn. */
  speech(ev, text, final) {
    this.inFrag.close();
    const fresh = !this.outFrag.open;
    const card = this.outFrag.add(ev.ts, text, final);
    card.events.push(ev);
    if (fresh) {
      card.arrow = { from: 'voice', to: 'you', kind: 'speech', label: '🔊', dashed: false };
      const trig = this.trigger;
      if (trig) {
        card.chain = trig.chain;
        if (trig.kind === 'push' && !trig.claimed) {
          // push → first speech: how long the model took to voice it.
          trig.claimed = true;
          const delay = card.tms - trig.card.tms;
          card.pushDelayMs = delay;
          card.arrow.label = `🔊 ${fmtMs(delay)} after push`;
          card.arrow.level = latencyLevel(delay);
        }
      }
    }
    return card;
  }

  // ---- Voice model ↔ Supervisor

  toolCall(ev, p) {
    this.closeFragments();
    const name = p.name || '?';
    const card = this.plain(ev, 'voice', {
      title: `⚙ ${name}`, name, id: p.id || '', summary: argSummary(p.args), via: p.via || '',
      cls: ['tool'], chainRoot: true,
    });
    // A tool call in a user turn belongs to that turn; on its own (the
    // model acting on a push, or a debug call) it opens its own chain.
    card.chain = this.turn ? this.turn.chain : this.newChain('tool', card);
    card.arrow = { from: 'voice', to: 'sup', kind: 'call', label: `${name}(${clip(argSummary(p.args, 30), 40)})${card.via && card.via !== 'voice' ? ` · via ${card.via}` : ''}`, dashed: false };
    this.tools.open(card.id, card);
    return card;
  }

  toolResult(ev, p) {
    const call = this.tools.close(p.id, p.name);
    const failed = !!(p.error || p.cancelled || p.send_error);
    const card = this.plain(ev, 'sup', {
      title: `${p.cancelled ? 'cancelled' : 'result'} ${p.name || '?'}`, name: p.name || '?', id: p.id || '',
      summary: p.cancelled ? 'cancelled (the user barged in)' : (p.error ? `error: ${p.error}` : clip(p.spoken || '', 160)),
      cls: ['result', ...(failed ? ['error'] : [])], orphan: !call,
    });
    card.chain = call ? call.chain : this.newChain('tool', card);
    // Latency: dur_ms is the handler time when the emitter recorded it;
    // otherwise the wall-clock distance to the call (Δ), which includes
    // the wait for the model to close its turn.
    let lat = card.durMs;
    let latLabel = lat != null ? fmtMs(lat) : '';
    if (lat != null && p.wait_ms != null && Number(p.wait_ms) > 0) latLabel += ` +${fmtMs(p.wait_ms)} wait`;
    if (lat == null && call) {
      lat = card.tms - call.tms;
      latLabel = `Δ ${fmtMs(lat)}`;
    }
    card.latencyMs = lat;
    const bits = [p.name || '?', latLabel, p.scheduling, p.clarify ? 'clarify' : '', p.cancelled ? 'cancelled' : '', p.error ? 'error' : ''].filter(Boolean);
    card.arrow = { from: 'sup', to: 'voice', kind: 'result', label: bits.join(' · '), dashed: true, level: failed ? 'error' : latencyLevel(lat) };
    if (call) call.resultKey = card.key;
    this.trigger = { kind: 'result', chain: card.chain, card };
    // Without a voice model the daemon speaks the result itself.
    if (p.via && p.via !== 'voice' && !p.cancelled && p.spoken) {
      const s = this.card({ ts: ev.ts, lane: 'voice', type: 'voice.transcript.out', source: ev.source, event: ev, title: '🔊 spoke (daemon, no voice model)', text: p.spoken, synthetic: true, chain: card.chain, cls: ['synthetic'] });
      s.arrow = { from: 'voice', to: 'you', kind: 'speech', label: '🔊 daemon', dashed: true };
    }
    return card;
  }

  push(ev, p) {
    this.closeFragments();
    const urgency = p.urgency || 'idle';
    const card = this.plain(ev, 'sup', {
      title: `push · ${urgency}`, text: p.text || '', urgency, skipped: !!p.skipped, silent: urgency === 'silent',
      cls: ['push', `push-${urgency}`, ...(p.skipped ? ['skipped'] : [])], chainRoot: true,
    });
    card.summary = clip(p.text || '', 160);
    // The brain.push_decision that produced this push (matched by text).
    const di = this.decisions.findIndex((d) => (d.spoken && d.spoken === p.text) || (d.via === 'template' && (d.template === p.text || `As you asked: ${d.template}` === p.text)));
    if (di >= 0) {
      const d = this.decisions.splice(di, 1)[0];
      card.decision = { via: d.via, requested: d.urgency_requested || '', applied: d.urgency_applied || '', reason: d.reason || '', ceiling: d.ceiling || '', late: !!d.late };
      card.cls.push('decided');
      card.title += d.via === 'brain' ? (d.urgency_requested && d.urgency_requested !== d.urgency_applied ? ` · brain asked ${d.urgency_requested} (${d.reason})` : ' · brain') : ` · template${d.reason && d.reason !== 'brain' ? ` (${d.reason})` : ''}`;
    }
    if (this.loose) {
      card.chain = this.loose.chain;
      this.loose = null;
    } else {
      card.chain = this.newChain('push', card);
    }
    card.arrow = { from: 'sup', to: 'voice', kind: 'push', label: `push · ${urgency}${p.skipped ? ' · not sent' : ''}`, dashed: urgency === 'silent' || !!p.skipped };
    // The user turn stays open (a silent briefing push can land mid-turn
    // and voice.turn still has to reach the input card); voice.turn closes
    // it. A silent or unsent push causes no speech, so it does not claim
    // the next transcript either.
    if (urgency !== 'silent' && !p.skipped) this.trigger = { kind: 'push', chain: card.chain, card, claimed: false };
    return card;
  }

  turnEnd(ev, p) {
    this.closeFragments();
    const bits = [`turn ${p.turn ?? '?'}`, p.end || '', p.dur_ms != null ? fmtMs(p.dur_ms) : '', p.tool_calls ? `${p.tool_calls} tool call${p.tool_calls > 1 ? 's' : ''}` : '', p.pushes_spoken ? `${p.pushes_spoken} push${p.pushes_spoken > 1 ? 'es' : ''} spoken` : '', p.stalled ? 'stalled' : ''];
    const card = this.plain(ev, 'voice', { title: 'turn', summary: bits.filter(Boolean).join(' · '), cls: ['minor', ...(p.stalled || p.responded === false ? ['error'] : [])] });
    if (this.turn) {
      const input = this.turn.input;
      const responded = p.responded !== false;
      const rms = p.input_to_response_ms != null ? Number(p.input_to_response_ms) : null;
      input.latency = {
        ms: rms, slow: !responded || (rms != null && rms >= STALL_MS), stalled: !!p.stalled, responded,
        label: !responded ? 'no response' : (rms != null ? `answered after ${fmtMs(rms)}${p.stalled ? ' (stalled)' : ''}` : ''),
      };
      // input_to_response_ms labels the You → Voice arrow; red from STALL_MS.
      if (input.arrow && (rms != null || !responded)) {
        input.arrow.label = `${input.arrow.label} · ⏱ ${!responded ? 'no response' : fmtMs(rms)}`;
        input.arrow.level = input.latency.slow ? 'slow' : (rms >= LATENCY.warn ? 'warn' : 'ok');
      }
      card.chain = this.turn.chain;
      this.turn = null;
    }
    return card;
  }

  // ---- Supervisor-internal tool families (brain.*)

  /** Chain for supervisor work: the open voice call's, else the loose one. */
  workChain(card) {
    const open = this.tools.last;
    return open ? open.chain : this.looseChain(true, card);
  }

  innerPairer(prefix) {
    let pairer = this.inner.get(prefix);
    if (!pairer) { pairer = new ToolPairer(); this.inner.set(prefix, pairer); }
    return pairer;
  }

  /** The innermost open inner call (brain.tool_call without its result yet). */
  get openInner() {
    let best = null;
    for (const pairer of this.inner.values()) if (pairer.last && (!best || pairer.last.tms >= best.tms)) best = pairer.last;
    return best;
  }

  /** brain.step: the agent loop's own LLM call (llm.call's payload), from the Supervisor lane. */
  brainStep(ev, p) {
    const isErr = !!p.error;
    const card = this.plain(ev, 'sup', { title: `brain step${p.template ? ` ${p.template}` : ''}`, cls: ['brain', ...(isErr ? ['error'] : [])] });
    card.chain = this.workChain(card);
    const inner = this.openInner;
    if (inner) card.parentKey = inner.key;
    card.arrow = { from: 'sup', to: 'herdr', kind: 'call', label: [p.template, withProvider(p.provider, p.model), card.durMs != null ? fmtMs(card.durMs) : '', p.tool_calls ? `${p.tool_calls} tool call${p.tool_calls === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · '), dashed: false, level: isErr ? 'error' : latencyLevel(card.durMs) };
    return card;
  }

  /** `<prefix>.tool_call` of a nested family: a Supervisor-lane card under the voice call in flight. */
  innerCall(ev, p, prefix) {
    const name = p.name || '?';
    const card = this.plain(ev, 'sup', { title: `⚙ ${prefix} ${name}`, name, id: p.id || '', summary: argSummary(p.args), cls: ['tool', 'inner', prefix], family: prefix, children: [] });
    card.chain = this.workChain(card);
    const outer = this.openInner;
    if (outer) card.parentKey = outer.key;
    this.innerPairer(prefix).open(card.id, card);
    return card;
  }

  innerResult(ev, p, prefix) {
    const call = this.innerPairer(prefix).close(p.id, p.name);
    const failed = !!(p.error || p.cancelled);
    const card = this.plain(ev, 'sup', {
      title: `${p.cancelled ? 'cancelled' : 'result'} ${prefix} ${p.name || '?'}`, name: p.name || '?', id: p.id || '',
      summary: p.error ? `error: ${p.error}` : clip(typeof p.result === 'string' ? p.result : (p.result != null ? JSON.stringify(p.result) : ''), 160),
      cls: ['result', 'inner', prefix, ...(failed ? ['error'] : [])], family: prefix, orphan: !call,
    });
    card.chain = call ? call.chain : this.workChain(card);
    if (call) {
      card.parentKey = call.parentKey || null;
      card.callKey = call.key;
      call.resultKey = card.key;
    }
    let lat = card.durMs;
    if (lat == null && call) lat = card.tms - call.tms;
    card.latencyMs = lat;
    card.latencyLevel = failed ? 'error' : latencyLevel(lat);
    if (lat != null) card.summary = `${card.durMs != null ? '' : 'Δ '}${fmtMs(lat)}${card.summary ? ` · ${card.summary}` : ''}`;
    return card;
  }

  // ---- Supervisor ↔ herdr / LLM / CLI

  /**
   * Work with no tool call open goes to the loose chain, which the next
   * push takes. `work` marks real supervisor work (read, llm.call,
   * pending.*) as opposed to the herdr pings that merely start a story.
   */
  looseChain(work, card) {
    if (!this.loose) this.loose = { chain: this.newChain('loose', card), work: false };
    if (work) this.loose.work = true;
    return this.loose.chain;
  }

  /** Chain for a herdr ping: the open tool call's, else the loose one. */
  herdrChain(card) {
    const open = this.tools.last;
    if (open) return open.chain;
    // Work that led to no push (a finish the chattiness level
    // silenced) ends its story here: the next ping starts a new one.
    if (this.loose && this.loose.work) this.loose = null;
    return this.looseChain(false, card);
  }

  transition(ev, p) {
    const to = p.to || 'unknown';
    const card = this.plain(ev, 'herdr', {
      title: `${p.workspace_label || ''}${p.name ? `-${p.name}` : ''}`.replace(/^-/, '') || (ev.pane_id || 'pane'),
      summary: `${p.from || '?'} → ${to}${p.agent ? ` · ${p.agent}` : ''}${p.source ? ` · ${p.source}` : ''}${p.closed ? ' · closed' : ''}`,
      state: to, cls: ['transition', stateClass(to)],
    });
    card.arrow = { from: 'herdr', to: 'sup', kind: 'state', label: `${p.from || '?'} → ${to}`, dashed: false, state: to };
    card.chain = this.herdrChain(card);
    return card;
  }

  state(ev, p) {
    const st = p.state || 'unknown';
    const card = this.plain(ev, 'herdr', { title: 'state', summary: `${st}${p.agent ? ` · ${p.agent}` : ''}${p.source ? ` · ${p.source}` : ''}`, state: st, cls: ['minor', stateClass(st)] });
    card.chain = this.herdrChain(card);
    return card;
  }

  other(ev, p) {
    const type = ev.type || '';
    const lane = laneOfSource(ev.source);
    const isErr = /error|_error|unrouted|reject|dropped/.test(type) || !!p.error || p.killed || (type === 'cli.run' && p.exit != null && p.exit !== 0);
    const cls = isErr ? ['error'] : [];
    if (NESTED_TYPES.test(type)) {
      const card = this.plain(ev, lane, { cls });
      card.chain = this.workChain(card);
      // Between an inner call (brain.tool_call read_screen / run_cli) and
      // its result, the herdr.read / cli.run / llm.call it ran belongs to it.
      const inner = this.openInner;
      if (inner) { card.parentKey = inner.key; inner.children.push(card.key); }
      if (type === 'llm.call') {
        card.title = `LLM ${p.template || ''}`.trim();
        card.arrow = { from: 'sup', to: 'herdr', kind: 'call', label: [p.template, withProvider(p.provider, p.model), card.durMs != null ? fmtMs(card.durMs) : ''].filter(Boolean).join(' · '), dashed: false, level: isErr ? 'error' : latencyLevel(card.durMs) };
      } else if (type === 'cli.run') {
        card.title = `CLI ${p.backend || ''}`.trim();
        card.arrow = { from: 'sup', to: 'herdr', kind: 'call', label: [p.backend, card.durMs != null ? fmtMs(card.durMs) : '', p.exit != null && p.exit !== 0 ? `exit ${p.exit}` : '', p.killed ? 'killed' : ''].filter(Boolean).join(' · '), dashed: false, level: isErr ? 'error' : latencyLevel(card.durMs) };
      } else if (type === 'herdr.read' || type === 'herdr.send') {
        card.title = type === 'herdr.read' ? 'read pane' : `send ${p.kind || ''}`.trim();
        card.arrow = { from: 'sup', to: 'herdr', kind: 'call', label: eventSummary(ev), dashed: type === 'herdr.read', level: isErr ? 'error' : null };
      } else if (type.startsWith('investigation.')) {
        // A background look runs a CLI sub-agent in the worker's
        // repository, so it belongs in the Flock / CLI lane with the
        // cli.run it will make, not in the Brain lane it was started from.
        card.lane = 'herdr';
        card.title = `investigation ${type.slice('investigation.'.length)}`;
        card.cls.push('investigation');
        card.arrow = {
          from: 'sup', to: 'herdr', kind: 'call',
          label: [p.id, p.backend, card.durMs != null ? fmtMs(card.durMs) : '', p.reason].filter(Boolean).join(' · '),
          dashed: type !== 'investigation.started', level: isErr ? 'error' : latencyLevel(card.durMs),
        };
      } else if (type.startsWith('pending.')) {
        card.title = type.slice('pending.'.length);
        card.cls.push('pending', `pending-${card.title}`);
        card.summary = eventSummary(ev);
      } else {
        card.cls.push('minor');
      }
      if (type === 'supervisor.push_quiet' || type === 'supervisor.push_skipped') card.silent = true;
      if (type === 'brain.push_decision') {
        card.title = 'push judgment';
        card.cls.push('brain', 'decision');
        // A decision that speaks nothing stays a card of its own; one
        // that leads to a push is shown on that push (skip never does).
        if (p.urgency_applied !== 'skip') this.decisions.push(p);
      } else if (type === 'brain.yielded' || type === 'brain.event_coalesced') {
        card.title = type === 'brain.yielded' ? 'yielded to a shepherd turn' : 'event turn coalesced';
        card.cls.push('brain');
      } else if (type === 'brain.model_changed') {
        card.title = 'model switched';
        card.cls.push('brain');
      }
      return card;
    }
    if (type.startsWith('web.client.') || type === 'web.inject' || type === 'web.utterance_unrouted' || type === 'web.push_error') {
      return this.plain(ev, 'you', { title: type.replace(/^web\.(client\.)?/, ''), cls: ['minor', ...cls] });
    }
    if (type === 'web.herdr_event') return this.plain(ev, 'herdr', { title: 'plugin hook', cls: ['minor', ...cls] });
    if (type === 'herdr.dedup' || type === 'herdr.fleet' || type === 'cli.detected' || type.startsWith('voice.')) {
      const card = this.plain(ev, lane, { cls: ['minor', ...cls] });
      if (type === 'herdr.dedup' && this.loose) card.chain = this.loose.chain;
      return card;
    }
    return this.plain(ev, lane, { cls });
  }
}

/** Wall-clock label "HH:MM:SS.mmm" (local time). */
export function fmtClock(tms) {
  const d = new Date(tms);
  const p = (n, w = 2) => String(n).padStart(w, '0');
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`;
}

/**
 * buildRows lays out the whole event list (oldest first).
 * @param {object[]} events eventlog lines {ts, source, type, pane_id, payload}
 * @param {object} [opts]
 *   hiddenLanes  lane ids to collapse (arrows to them become stubs)
 *   hiddenSources Set of sources to drop (unknown sources always show)
 *   pane         pane id to highlight (cards get paneHit)
 *   search       lower-case substring the event JSON must contain
 *   hideStats    drop web.client.stats / voice.usage (default true)
 *   hideSilent   drop silent pushes and push_quiet (default true)
 *   gapMs        gap marker threshold (default GAP_MS)
 * @returns {{rows: object[], chains: Map, total: number, shown: number}}
 */
export function buildRows(events, opts = {}) {
  const hidden = new Set(opts.hiddenLanes || []);
  const hiddenSources = opts.hiddenSources instanceof Set ? opts.hiddenSources : null;
  const search = (opts.search || '').toLowerCase();
  const hideStats = opts.hideStats !== false;
  const hideSilent = opts.hideSilent !== false;
  const gapMs = opts.gapMs ?? GAP_MS;
  const pane = opts.pane || '';

  const b = new Builder();
  for (const ev of events) b.add(ev);

  // Visible lanes → grid columns (0-based among the visible lanes).
  const visible = LANES.filter((l) => !hidden.has(l.id)).map((l) => l.id);
  const col = Object.fromEntries(visible.map((id, i) => [id, i]));

  const rows = [];
  let prevTms = null;
  let prevMinute = null;
  for (const r of b.rows) {
    if (hiddenSources && hiddenSources.has(r.source)) continue;
    if (search && !JSON.stringify(r.events || r.event).toLowerCase().includes(search)) continue;
    if (r.kind === 'card') {
      if (hideStats && r.stats) continue;
      if (hideSilent && r.silent) continue;
      const laneHidden = hidden.has(r.lane);
      let arrow = null;
      if (r.arrow) {
        const a = r.arrow;
        const fromHidden = hidden.has(a.from);
        const toHidden = hidden.has(a.to);
        if (!(fromHidden && toHidden)) {
          // A stub keeps the arrow's visible end and points at the edge
          // of the column on the side where the hidden lane would be.
          const dir = LANE_INDEX[a.to] > LANE_INDEX[a.from] ? 'right' : 'left';
          arrow = {
            ...a, dir,
            fromCol: fromHidden ? col[a.to] : col[a.from],
            toCol: toHidden ? col[a.from] : col[a.to],
            stub: fromHidden ? 'from' : (toHidden ? 'to' : null),
          };
          // the side of the single visible column the stub attaches to
          if (arrow.stub === 'from') arrow.stubSide = dir === 'right' ? 'left' : 'right';
          if (arrow.stub === 'to') arrow.stubSide = dir;
        }
      }
      if (laneHidden && !arrow) continue;
      r.col = laneHidden ? null : col[r.lane];
      r.laneHidden = laneHidden;
      r.arrowOut = arrow;
      r.paneHit = !!pane && r.pane === pane;
    }
    if (prevTms != null && r.tms - prevTms > gapMs) {
      rows.push({ kind: 'gap', key: `gap:${r.key}`, ms: r.tms - prevTms, label: fmtMs(r.tms - prevTms).replace(/\.0 s$/, ' s') });
    }
    const minute = Math.floor(r.tms / 60000);
    r.time = fmtClock(r.tms);
    r.minuteChanged = minute !== prevMinute;
    prevMinute = minute;
    prevTms = r.tms;
    rows.push(r);
  }
  return { rows, chains: b.chains, lanes: visible, total: b.rows.length, shown: rows.filter((r) => r.kind !== 'gap').length };
}
