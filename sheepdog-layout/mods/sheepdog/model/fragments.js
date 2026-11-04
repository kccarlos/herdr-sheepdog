// The transcript the conversation is made of, shown as it arrives. The daemon
// folds `voice.transcript.in/out` fragments (and a few whole-text events) into
// items (internal/web/conversation.go); the panel mirrors that for the events
// whose payload is the whole item, so words appear when they are said and not
// when the next /api/conversation answers. An item made here has the id the
// daemon will give it (the event id of its first event), so the next page
// replaces it; the daemon stays the record. Pure. No herdr imports.

export const noRuns = () => ({ user: null, dog: null });

// Events that end the run of fragments in progress (the builder's closeAll).
const CLOSING = new Set([
  "daemon.start", "conversation.reset", "voice.resume", "voice.handle_dropped", "voice.closed",
  "voice.connect", "voice.connect_error", "voice.recycle", "voice.stalled", "voice.disconnected",
  "voice.goaway", "voice.handover", "voice.tool_call", "voice.turn", "card.shown",
]);

const text = (value) => (typeof value === "string" ? value : "");

// An item from a run of fragments, or null while it has no words (the daemon
// drops an empty one too).
function runItem(kind, run, extra) {
  const body = run.text.trim();
  return body ? { id: run.id, ts: run.ts, kind, text: body, ...extra } : null;
}

function wholeItem(event, id, kind, body, extra) {
  const trimmed = text(body).trim();
  return trimmed ? { id, ts: event.ts, kind, text: trimmed, ...extra } : null;
}

// Folds one daemon event (`id` is its event id) into `runs`, the fragments in
// progress; returns the new runs and the item to show now, or null when the
// event adds nothing to show.
export function foldFragment(runs, event, id) {
  const p = event.payload ?? {};
  switch (event.type) {
    case "voice.transcript.in": {
      const base = runs.user ?? { id, ts: event.ts, text: "" };
      const run = { ...base, text: base.text + text(p.text) };
      return { runs: { user: p.final ? null : run, dog: null }, item: runItem("shepherd", run, { via: "voice" }) };
    }
    case "voice.transcript.out": {
      const base = runs.dog ?? { id, ts: event.ts, text: "" };
      const run = { ...base, text: base.text + text(p.text) };
      return { runs: { user: null, dog: p.final ? null : run }, item: runItem("dog", run, {}) };
    }
    case "voice.text":
      return { runs: noRuns(), item: wholeItem(event, id, "shepherd", p.text, { via: "typed" }) };
    case "web.utterance":
      // A spoken one is followed by voice.text, the model path.
      if (p.via === "voice") return { runs, item: null };
      return { runs: noRuns(), item: wholeItem(event, id, "shepherd", p.text, { via: "typed" }) };
    case "voice.push":
      // The rest of a turn already shown as its own text.
      if (p.handoff) return { runs, item: null };
      if (p.urgency === "silent") return { runs: noRuns(), item: null };
      return { runs: noRuns(), item: wholeItem(event, id, "push", p.text, { urgency: p.urgency, agent: p.agent }) };
    case "voice.tool_result": {
      // Without a voice model the daemon speaks the result itself (the
      // builder's rule: a via other than voice, not cancelled, with words).
      if (!p.via || p.via === "voice" || p.cancelled) return { runs, item: null };
      const item = wholeItem(event, id, "dog", p.spoken, p.via === "typed" ? { via: "typed" } : {});
      return item ? { runs: { ...runs, dog: null }, item } : { runs, item: null };
    }
    case "voice.interrupted":
      return { runs: { ...runs, dog: null }, item: null };
    default:
      return { runs: CLOSING.has(event.type) ? noRuns() : runs, item: null };
  }
}

// ---- approvals -------------------------------------------------------------
// An approval card appears and settles from the pending.* events, like the
// builder does (internal/web/conversation.go, "pending.created" … "pending
// .replaced"), and the question card of /api/state the same way. The daemon's
// next page and state replace what is made here.

const present = (extra) => Object.fromEntries(Object.entries(extra).filter(([, v]) => v !== "" && v != null));

// What a pending.* event does to the conversation: { item } adds the approval
// item (an answer_prompt only), { pendingId, patch, onlyOpen } changes the item
// of that question, or null.
export function foldApproval(event, id) {
  const p = event.payload ?? {};
  switch (event.type) {
    case "pending.created": {
      if (p.kind !== "answer_prompt") return null;
      const body = text(p.summary) || text(p.question);
      const options = Array.isArray(p.options) && p.options.length ? { options: p.options } : {};
      const item = { id, ts: event.ts, kind: "approval", text: body.trim(), ...present({ agent: p.agent }), pending_id: text(p.id), ...options, answered: false, resolution: "open" };
      return { item };
    }
    case "pending.confirmed":
      return { pendingId: text(p.id), onlyOpen: false, patch: { answered: true, resolution: "confirmed", ...present({ answer: p.answer, via: p.via }) } };
    case "pending.expired":
      return { pendingId: text(p.id), onlyOpen: true, patch: { resolution: "expired", reason: "expired" } };
    case "pending.cancelled":
      return { pendingId: text(p.id), onlyOpen: true, patch: { resolution: "cancelled", ...present({ reason: p.reason }) } };
    case "pending.replaced":
      return { pendingId: text(p.old_id), onlyOpen: true, patch: { resolution: "replaced", reason: "replaced" } };
    default:
      return null;
  }
}

// The open questions of /api/state after a pending.* event. A question the
// voice moved past stays open for the app (replaced changes no list); an
// answered, cancelled or expired one goes. `app` is the state, or null before
// it has been read (nothing to fold into). The same object comes back when
// nothing changed.
export function foldQuestions(app, event) {
  if (!app) return app;
  const p = event.payload ?? {};
  const questions = Array.isArray(app.questions) ? app.questions : [];
  const dropped = (list) => list.filter((q) => q.id !== p.id);
  switch (event.type) {
    case "pending.created": {
      if (p.kind !== "answer_prompt" || !p.id) return app;
      const options = Array.isArray(p.options) ? p.options : [];
      const q = { id: p.id, kind: p.kind, summary: text(p.summary), spoken_summary: text(p.summary), question: text(p.question), options, ...present({ agent: p.agent, command: p.command }) };
      return { ...app, pending_action: p.held ? app.pending_action : q, questions: [...dropped(questions), q] };
    }
    case "pending.confirmed":
    case "pending.cancelled":
    case "pending.expired": {
      const had = questions.some((q) => q.id === p.id);
      const pendingGone = app.pending_action?.id === p.id;
      if (!had && !pendingGone) return app;
      return { ...app, ...(pendingGone ? { pending_action: null } : {}), ...(had ? { questions: dropped(questions) } : {}) };
    }
    default:
      return app;
  }
}
