// The open questions as the panel floats them, and what a tap on an option
// does (docs/dev/design/conversation-timeline.md, section 15; ports
// ApprovalFlow, ios/Sources/SheepdogKit). Each question, by the daemon's id,
// is open, sending, answered or gone:
//
//   open --tap--> sending --200--> answered
//     ^              |--409--> gone (and the state is asked again)
//     '----error-----'  (open again, with the error under the buttons)
//
// A settled card stays LINGER_MS after the daemon stopped listing it, so the
// answer can be read. It asks the daemon through `http` and tells subscribers
// when something changed; it never draws. No herdr imports: node tests it.

// How long an answered or gone question stays once /api/state no longer has it.
export const LINGER_MS = 4000;

export const GONE = "That question was already answered or has gone.";

// A worker's approval prompt the user can answer by tap.
const answerable = (q) => q?.kind === "answer_prompt" && Array.isArray(q.options) && q.options.length > 0;

// The open questions of /api/state, oldest first: `questions`, else the
// voice's `pending_action`.
export function openQuestions(app) {
  const list = app?.questions?.length ? app.questions : app?.pending_action ? [app.pending_action] : [];
  return list.filter(answerable);
}

// What a card says, in plain words.
export function summaryOf(q) {
  for (const text of [q.summary, q.spoken_summary, q.question]) if (text?.trim()) return text.trim();
  return "An agent is waiting for your answer.";
}

// The label to show: without the recommendation marker when the daemon split
// it off (the card shows a "Recommended" tag instead).
export const labelOf = (option) => option.title?.trim() || option.label;

export function createAnswers({ http, newKey, now = Date.now, refresh = async () => {} }) {
  // phases: id -> { phase: "sending" | "answered" | "gone", option, outcome, spoken, message, pending, at } or { phase: "open", error }
  let snapshot = { phases: {} };
  const listeners = new Set();

  const set = (id, phase) => {
    snapshot = { phases: { ...snapshot.phases, [id]: phase } };
    for (const listener of listeners) listener(snapshot);
  };
  const phaseOf = (id) => snapshot.phases[id] ?? { phase: "open", error: null };
  const busy = () => Object.values(snapshot.phases).some((p) => p.phase === "sending");

  // Taps `option` (an entry of the question's `options`) of question `q`.
  // One tap at a time; a tap on a question that is not open sends nothing.
  async function answer(q, option) {
    if (busy() || phaseOf(q.id).phase !== "open") return;
    set(q.id, { phase: "sending", option, pending: q, at: now() });
    try {
      const res = await http.answer(q.id, option.index, newKey());
      set(q.id, { phase: "answered", option, outcome: `You chose ${labelOf(option)}`, spoken: res.spoken ?? null, pending: q, at: now() });
    } catch (err) {
      if (err.status === 409) set(q.id, { phase: "gone", message: GONE, pending: q, at: now() });
      else set(q.id, { phase: "open", error: err.status ? err.message : "Sheepdog did not answer. Try again." });
    }
    // The question is settled either way; the state says what is still open.
    await refresh();
  }

  // The cards to float: the open questions with their phases, and in front of
  // them the settled ones the daemon no longer lists (until they have lingered).
  function cards(app) {
    const open = openQuestions(app);
    const ids = new Set(open.map((q) => q.id));
    const lingering = Object.entries(snapshot.phases)
      .filter(([id, p]) => !ids.has(id) && (p.phase === "answered" || p.phase === "gone" || p.phase === "sending"))
      .map(([, p]) => p.pending);
    return [...lingering, ...open].map((pending) => ({ pending, ...phaseOf(pending.id) }));
  }

  // Forgets a settled question that has lingered, and a phase of a question
  // that went away while open. `app` is the state as it is now.
  function prune(app) {
    const ids = new Set(openQuestions(app).map((q) => q.id));
    const t = now();
    const kept = Object.entries(snapshot.phases).filter(([id, p]) => {
      if (p.phase === "sending") return true;
      if (p.phase === "open") return ids.has(id);
      return ids.has(id) || t - p.at < LINGER_MS;
    });
    if (kept.length === Object.keys(snapshot.phases).length) return;
    snapshot = { phases: Object.fromEntries(kept) };
    for (const listener of listeners) listener(snapshot);
  }

  return {
    getState: () => snapshot,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    answer,
    cards,
    prune,
    busy,
  };
}
