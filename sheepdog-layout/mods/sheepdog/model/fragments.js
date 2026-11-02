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
    case "voice.interrupted":
      return { runs: { ...runs, dog: null }, item: null };
    default:
      return { runs: CLOSING.has(event.type) ? noRuns() : runs, item: null };
  }
}
