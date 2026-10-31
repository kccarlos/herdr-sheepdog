// The typed lines the panel sent and is waiting on: each shows at once as a
// pending line, then with the orchestrator's reply when POST /api/turn
// returns, and is dropped once /api/conversation has the same exchange (the
// daemon's own items are the record, matched by id, so nothing shows twice).
// A failed send leaves no line behind and says why. It asks the daemon through
// `http` and tells subscribers when something changed; it never draws. No
// herdr imports: node tests it (docs/dev/design/desktop-client.md, sections 4.1,
// 4.2 and 6.4).

// A typed line and its reply are looked for in the conversation from this
// long before the send (the daemon's clock is this computer's; this only
// covers the two being read a moment apart), in event-id microseconds.
const SLACK = 60 * 1000 * 1000;
// A replied entry the conversation never showed is dropped after this long.
export const KEEP_MS = 2 * 60 * 1000;
// How long a note ("Answered by voice") stays under the textarea.
export const NOTE_MS = 6000;

const same = (a, b) => (a ?? "").trim() === (b ?? "").trim();

export function createOutbox({ http, newKey, now = Date.now, refresh = async () => {} }) {
  let snapshot = {
    entries: [], // { key, text, at, phase: "sending" | "replied", reply, repliedAt, userId, replyId }
    busy: false, // a turn is running; one at a time, as the daemon claims one typed line per turn
    notice: null, // { tone: "error" | "info", text, at }, under the textarea
  };
  // The text and key of a send whose outcome is unknown (nothing answered):
  // sending the same text again reuses the key, so the daemon replays the reply
  // if it did run the turn. A refusal is remembered by the daemon too (it keeps
  // 4xx and 5xx replies per key), so after one the next send gets a new key.
  let retry = null;
  const listeners = new Set();

  const set = (patch) => {
    snapshot = { ...snapshot, ...patch };
    for (const listener of listeners) listener(snapshot);
  };
  const forget = (entry) => snapshot.entries.filter((e) => e !== entry);
  const refuse = (entry, notice, again) => {
    retry = again;
    set({ entries: forget(entry), busy: false, notice: { ...notice, at: now() } });
  };

  // Sends `text` (already stripped; see model/composer.js). Resolves with
  // { restore }: the text the textarea should hold again, or null when it took
  // the line (or voice did).
  async function submit(text) {
    if (snapshot.busy) return { restore: text };
    const key = retry?.text === text ? retry.key : newKey();
    const entry = { key, text, at: now() * 1000, phase: "sending", reply: null, repliedAt: 0, userId: null, replyId: null };
    set({ entries: [...snapshot.entries, entry], busy: true, notice: null });
    let res;
    try {
      res = await http.turn(text, key);
    } catch (err) {
      if (err.status === 409 && /Idempotency-Key/.test(err.message)) {
        refuse(entry, { tone: "error", text: "Sheepdog could not repeat that send. Send it again." }, null);
      } else if (err.status === 409) {
        // A spoken turn claimed the words first and answers them.
        refuse(entry, { tone: "info", text: "Answered by voice." }, null);
        return { restore: null };
      } else if (err.status) {
        refuse(entry, { tone: "error", text: err.message }, null);
      } else {
        refuse(entry, { tone: "error", text: "Sheepdog did not answer. Your text is kept; send it again to retry." }, { text, key });
      }
      return { restore: text };
    }
    retry = null;
    const replied = { ...entry, phase: "replied", reply: typeof res.reply === "string" ? res.reply : "", repliedAt: now() };
    set({ entries: snapshot.entries.map((e) => (e === entry ? replied : e)), busy: false });
    // The conversation has the exchange by now; ask, so it is matched soon.
    await refresh();
    return { restore: null };
  }

  // Matches the entries against the conversation's items by id and drops the
  // ones it shows in full. A user line is the first unclaimed typed `shepherd`
  // item with the same words from the send on; its reply the first typed `dog`
  // item after it with the reply's words. Call it whenever the history changes.
  function reconcile(items) {
    const claimed = new Set();
    const find = (match) => items.find((i) => !claimed.has(i.id) && match(i));
    let changed = false;
    const entries = [];
    for (const entry of snapshot.entries) {
      let { userId, replyId } = entry;
      if (userId == null) {
        const line = find((i) => i.kind === "shepherd" && i.via === "typed" && i.id >= entry.at - SLACK && same(i.text, entry.text));
        userId = line?.id ?? null;
      }
      if (userId != null) claimed.add(userId);
      if (userId != null && replyId == null && entry.reply) {
        const reply = find((i) => i.kind === "dog" && i.via === "typed" && i.id > userId && same(i.text, entry.reply));
        replyId = reply?.id ?? null;
      }
      if (replyId != null) claimed.add(replyId);
      const shown = userId != null && entry.phase === "replied" && (replyId != null || !entry.reply.trim());
      if (shown) {
        changed = true;
        continue;
      }
      if (userId !== entry.userId || replyId !== entry.replyId) {
        changed = true;
        entries.push({ ...entry, userId, replyId });
      } else {
        entries.push(entry);
      }
    }
    if (changed) set({ entries });
  }

  // Drops what has stayed long enough: a replied entry the history never
  // matched, and the note under the textarea. Called from a timer.
  function prune() {
    const t = now();
    const entries = snapshot.entries.filter((e) => e.phase !== "replied" || t - e.repliedAt < KEEP_MS);
    const notice = snapshot.notice && snapshot.notice.tone === "info" && t - snapshot.notice.at >= NOTE_MS ? null : snapshot.notice;
    if (entries.length !== snapshot.entries.length || notice !== snapshot.notice) set({ entries, notice });
  }

  return {
    getState: () => snapshot,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    submit,
    reconcile,
    prune,
  };
}

// The entries as timeline items, for what the conversation does not show yet:
// the typed line (pending while its turn runs) and, once it came back, the
// reply. Ids are event-id microseconds of the send and the reply, so they sort
// after everything the daemon has.
export function pendingItems(entries) {
  const out = [];
  for (const e of entries) {
    const ts = (at) => new Date(at).toISOString();
    if (e.userId == null) out.push({ id: e.at, ts: ts(e.at / 1000), kind: "shepherd", via: "typed", text: e.text, pending: e.phase === "sending" });
    if (e.phase === "replied" && e.replyId == null && e.reply.trim()) out.push({ id: Math.max(e.repliedAt * 1000, e.at + 1), ts: ts(e.repliedAt), kind: "dog", via: "typed", text: e.reply });
  }
  return out;
}
