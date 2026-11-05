// The typed lines the panel sent or queued: the active turn shows at once as a
// pending line, then with the orchestrator's reply when POST /api/turn returns,
// and is dropped once /api/conversation has the same exchange (the daemon's own
// items are the record, matched by id, so nothing shows twice). Follow-up lines
// submitted while a turn is running queue in FIFO order; failed sends (both
// typed and tapped suggested replies) stay retryable inline with Retry / Edit /
// Remove, pausing auto-drain of queued follow-ups so nothing runs out of order.
// Active, queued and failed entries persist in herdr-app shared state across
// mod reloads so in-flight turns reattach via Idempotency-Key. No herdr
// imports: node tests it (docs/dev/design/desktop-client.md, sections 4.1, 4.2
// and 6.4).

export const OUTBOX_KEY = "sheepdog.outbox";
// Matches internal/web/idempotency.go's idemTTL (10 minutes).
export const IDEM_TTL_MS = 10 * 60 * 1000;
// How long persisted queued/failed entries survive across reloads.
export const OUTBOX_TTL_MS = 60 * 60 * 1000;
// A typed line and its reply are looked for in the conversation from this
// long before the send (the daemon's clock is this computer's; this only
// covers the two being read a moment apart), in event-id microseconds.
const SLACK = 60 * 1000 * 1000;
// A replied entry the conversation never showed is dropped after this long.
export const KEEP_MS = 2 * 60 * 1000;
// How long a note ("Answered by voice") stays under the textarea.
export const NOTE_MS = 6000;

const same = (a, b) => (a ?? "").trim() === (b ?? "").trim();

function loadSavedEntries(saved, nowMs) {
  let obj = saved;
  if (typeof obj === "string") {
    try {
      obj = JSON.parse(obj);
    } catch {
      return [];
    }
  }
  const list = Array.isArray(obj?.entries) ? obj.entries : [];
  const out = [];
  let hasSending = false;
  for (const raw of list) {
    if (!raw || typeof raw !== "object") continue;
    if (typeof raw.key !== "string" || !raw.key || typeof raw.text !== "string" || !raw.text.trim() || typeof raw.at !== "number") continue;
    const ageMs = nowMs - raw.at / 1000;
    if (ageMs >= OUTBOX_TTL_MS || ageMs < -OUTBOX_TTL_MS) continue;
    if (raw.phase === "sending") {
      if (!hasSending && ageMs < IDEM_TTL_MS) {
        hasSending = true;
        out.push({
          key: raw.key,
          text: raw.text,
          at: raw.at,
          phase: "sending",
          error: null,
          reuseKey: true,
          resumed: true,
          reply: null,
          repliedAt: 0,
          userId: null,
          replyId: null,
        });
      } else if (ageMs < IDEM_TTL_MS) {
        out.push({
          key: raw.key,
          text: raw.text,
          at: raw.at,
          phase: "queued",
          error: null,
          reuseKey: true,
          resumed: false,
          reply: null,
          repliedAt: 0,
          userId: null,
          replyId: null,
        });
      } else {
        out.push({
          key: raw.key,
          text: raw.text,
          at: raw.at,
          phase: "failed",
          error: "Sheepdog reloaded before this turn finished. Retry if it still needs to run.",
          reuseKey: false,
          resumed: false,
          reply: null,
          repliedAt: 0,
          userId: null,
          replyId: null,
        });
      }
    } else if (raw.phase === "queued") {
      out.push({
        key: raw.key,
        text: raw.text,
        at: raw.at,
        phase: "queued",
        error: null,
        reuseKey: Boolean(raw.reuseKey ?? true),
        resumed: false,
        reply: null,
        repliedAt: 0,
        userId: null,
        replyId: null,
      });
    } else if (raw.phase === "failed") {
      out.push({
        key: raw.key,
        text: raw.text,
        at: raw.at,
        phase: "failed",
        error: typeof raw.error === "string" && raw.error ? raw.error : "Sheepdog did not answer.",
        reuseKey: Boolean(raw.reuseKey),
        resumed: false,
        reply: null,
        repliedAt: 0,
        userId: null,
        replyId: null,
      });
    }
  }
  return out;
}

function serializeEntries(entries) {
  const persisted = entries
    .filter((e) => e.phase === "sending" || e.phase === "queued" || e.phase === "failed")
    .map(({ key, text, at, phase, error, reuseKey }) => ({ key, text, at, phase, error, reuseKey }));
  return persisted.length > 0 ? { entries: persisted } : null;
}

// `items` gives the conversation's items as they are now: the stream can show
// the exchange before `turn` returns, and nothing would match it afterwards.
export function createOutbox({ http, newKey, now = Date.now, refresh = async () => {}, items = () => [], saved = null, save = () => {} }) {
  const initialEntries = loadSavedEntries(saved, now());
  const initialBusy = initialEntries.some((e) => e.phase === "sending");
  const initialFailed = initialEntries.find((e) => e.phase === "failed");
  let snapshot = {
    entries: initialEntries,
    busy: initialBusy,
    notice: !initialBusy && initialFailed ? { tone: "error", text: initialFailed.error, at: now() } : null,
  };
  // When a failed entry with unknown outcome is moved back into the composer
  // via `take(key)`, keep its key so submitting the exact same words reuses it.
  let takenRetry = null;
  let resumedOnce = false;
  const listeners = new Set();

  const persist = (entries) => {
    try {
      save(serializeEntries(entries));
    } catch {
      // shared state failures must never break sending
    }
  };

  const set = (patch, shouldPersist = false) => {
    snapshot = { ...snapshot, ...patch };
    if (shouldPersist) persist(snapshot.entries);
    for (const listener of listeners) listener(snapshot);
  };

  const consumeTakenKey = (text) => {
    const prev = takenRetry;
    takenRetry = null;
    if (prev && prev.reuseKey && same(prev.text, text)) return prev.key;
    return null;
  };

  const hasBlockingFailureAhead = () => {
    const firstQueued = snapshot.entries.findIndex((e) => e.phase === "queued");
    if (firstQueued < 0) return false;
    return snapshot.entries.slice(0, firstQueued).some((e) => e.phase === "failed");
  };

  async function runEntry(entryKey) {
    const current = snapshot.entries.find((e) => e.key === entryKey);
    if (!current) return { restore: null };
    set(
      {
        entries: snapshot.entries.map((e) => (e.key === entryKey ? { ...e, phase: "sending", error: null, resumed: false } : e)),
        busy: true,
        notice: null,
      },
      true,
    );
    let res;
    try {
      res = await http.turn(current.text, entryKey);
    } catch (err) {
      // If the conversation stream already recorded the completed exchange
      // while the request was in flight, reconcile already dropped this entry.
      if (!snapshot.entries.some((e) => e.key === entryKey)) {
        set({ busy: false }, true);
        void drainNext();
        return { restore: null };
      }
      if (err.status === 409 && !/Idempotency-Key/.test(err.message)) {
        // A spoken turn claimed the words first and answers them.
        set(
          {
            entries: snapshot.entries.filter((e) => e.key !== entryKey),
            busy: false,
            notice: { tone: "info", text: "Answered by voice.", at: now() },
          },
          true,
        );
        void drainNext();
        return { restore: null };
      }
      let errorText;
      let reuseKey = false;
      if (err.status === 409 && /Idempotency-Key/.test(err.message)) {
        errorText = "Sheepdog could not repeat that send. Retry to send it again.";
      } else if (err.status) {
        errorText = err.message;
      } else {
        errorText = "Sheepdog did not answer. Retry to send again, or edit it.";
        reuseKey = true;
      }
      set(
        {
          entries: snapshot.entries.map((e) =>
            e.key === entryKey ? { ...e, phase: "failed", error: errorText, reuseKey, resumed: false } : e,
          ),
          busy: false,
          notice: { tone: "error", text: errorText, at: now() },
        },
        true,
      );
      return { restore: null, failed: true };
    }
    set(
      {
        entries: snapshot.entries.map((e) =>
          e.key === entryKey
            ? { ...e, phase: "replied", reply: typeof res.reply === "string" ? res.reply : "", repliedAt: now(), error: null, resumed: false }
            : e,
        ),
        busy: false,
      },
      true,
    );
    reconcile(items());
    await refresh();
    void drainNext();
    return { restore: null };
  }

  function drainNext() {
    if (snapshot.busy || hasBlockingFailureAhead()) return Promise.resolve({ restore: null });
    const next = snapshot.entries.find((e) => e.phase === "queued");
    if (!next) return Promise.resolve({ restore: null });
    const sendKey = next.reuseKey ? next.key : newKey();
    if (sendKey !== next.key) {
      set({ entries: snapshot.entries.map((e) => (e.key === next.key ? { ...e, key: sendKey, reuseKey: true } : e)) }, true);
    }
    return runEntry(sendKey);
  }

  // Sends `text` (already stripped; see model/composer.js), or queues it if a
  // turn is already running or paused behind a failure.
  async function submit(text) {
    if (snapshot.busy || hasBlockingFailureAhead()) {
      const key = consumeTakenKey(text) ?? newKey();
      const queued = {
        key,
        text,
        at: now() * 1000,
        phase: "queued",
        error: null,
        reuseKey: true,
        resumed: false,
        reply: null,
        repliedAt: 0,
        userId: null,
        replyId: null,
      };
      set({ entries: [...snapshot.entries, queued] }, true);
      return { restore: null, queued: true };
    }
    const existingFailed = snapshot.entries.find((e) => e.phase === "failed" && same(e.text, text));
    if (existingFailed) {
      consumeTakenKey(text);
      return retry(existingFailed.key);
    }
    const key = consumeTakenKey(text) ?? newKey();
    const entry = {
      key,
      text,
      at: now() * 1000,
      phase: "sending",
      error: null,
      reuseKey: true,
      resumed: false,
      reply: null,
      repliedAt: 0,
      userId: null,
      replyId: null,
    };
    // Insert ahead of any queued entries that were paused while editing a failed item.
    const firstQueued = snapshot.entries.findIndex((e) => e.phase === "queued");
    const entries =
      firstQueued < 0
        ? [...snapshot.entries, entry]
        : [...snapshot.entries.slice(0, firstQueued), entry, ...snapshot.entries.slice(firstQueued)];
    set({ entries, busy: true, notice: null }, true);
    return runEntry(key);
  }

  // Retries a failed entry (or starts a paused queued entry when idle).
  async function retry(key) {
    if (snapshot.busy) return { restore: null };
    const target = snapshot.entries.find((e) => e.key === key && (e.phase === "failed" || e.phase === "queued"));
    if (!target) return { restore: null };
    const sendKey = target.reuseKey ? target.key : newKey();
    if (sendKey !== target.key) {
      set({ entries: snapshot.entries.map((e) => (e.key === key ? { ...e, key: sendKey, reuseKey: true } : e)) }, true);
    }
    return runEntry(sendKey);
  }

  // Drops a queued or failed entry; if it was the blocking failure, queued
  // follow-ups behind it resume.
  function remove(key) {
    const target = snapshot.entries.find((e) => e.key === key && (e.phase === "queued" || e.phase === "failed"));
    if (!target) return;
    const entries = snapshot.entries.filter((e) => e.key !== key);
    const stillFailed = entries.some((e) => e.phase === "failed");
    const notice = snapshot.notice?.tone === "error" && !stillFailed ? null : snapshot.notice;
    set({ entries, notice }, true);
    if (!snapshot.busy) void drainNext();
  }

  // Removes a queued or failed entry and returns its text so the composer can
  // edit it without prematurely firing queued follow-ups behind it.
  function take(key) {
    const target = snapshot.entries.find((e) => e.key === key && (e.phase === "queued" || e.phase === "failed"));
    if (!target) return null;
    if (target.phase === "failed" && target.reuseKey) {
      takenRetry = { text: target.text, key: target.key, reuseKey: true };
    }
    const entries = snapshot.entries.filter((e) => e.key !== key);
    const stillFailed = entries.some((e) => e.phase === "failed");
    const notice = snapshot.notice?.tone === "error" && !stillFailed ? null : snapshot.notice;
    set({ entries, notice }, true);
    return target.text;
  }

  // Reattaches to an in-flight persisted turn (or drains restored queued items)
  // once the initial conversation snapshot has been reconciled.
  async function resume() {
    if (resumedOnce) return { restore: null };
    resumedOnce = true;
    reconcile(items());
    const restoredSending = snapshot.entries.find((e) => e.phase === "sending" && e.resumed);
    if (restoredSending) return runEntry(restoredSending.key);
    if (!snapshot.busy) return drainNext();
    return { restore: null };
  }

  // Matches the entries against the conversation's items by id and drops the
  // ones it shows in full.
  function reconcile(convItems) {
    const claimed = new Set();
    const find = (match) => convItems.find((i) => !claimed.has(i.id) && match(i));
    let changed = false;
    let droppedSending = false;
    let shouldPersist = false;
    const entries = [];
    for (const entry of snapshot.entries) {
      if (entry.phase === "queued") {
        entries.push(entry);
        continue;
      }
      let { userId, replyId } = entry;
      if (userId == null) {
        const line = find((i) => i.kind === "shepherd" && i.via === "typed" && i.id >= entry.at - SLACK && same(i.text, entry.text));
        userId = line?.id ?? null;
      }
      if (userId != null) claimed.add(userId);
      if (userId != null && replyId == null) {
        if (entry.reply != null) {
          if (entry.reply) {
            const reply = find((i) => i.kind === "dog" && i.via === "typed" && i.id > userId && same(i.text, entry.reply));
            replyId = reply?.id ?? null;
          }
        } else if (entry.phase === "sending" || (entry.phase === "failed" && entry.reuseKey)) {
          // Unknown reply text (in-flight turn or reload): if the conversation
          // already recorded a typed dog reply before any later typed user line,
          // the turn finished on the daemon.
          const nextUser = convItems.find((i) => i.kind === "shepherd" && i.via === "typed" && i.id > userId);
          const upper = nextUser ? nextUser.id : Infinity;
          const reply = find((i) => i.kind === "dog" && i.via === "typed" && i.id > userId && i.id < upper);
          replyId = reply?.id ?? null;
        }
      }
      if (replyId != null) claimed.add(replyId);
      const shown =
        userId != null &&
        ((entry.phase === "replied" && (replyId != null || !entry.reply.trim())) ||
          ((entry.phase === "sending" || (entry.phase === "failed" && entry.reuseKey)) && replyId != null));
      if (shown) {
        changed = true;
        if (entry.phase === "sending") droppedSending = true;
        if (entry.phase === "sending" || entry.phase === "failed") shouldPersist = true;
        continue;
      }
      if (userId !== entry.userId || replyId !== entry.replyId) {
        changed = true;
        entries.push({ ...entry, userId, replyId });
      } else {
        entries.push(entry);
      }
    }
    if (changed) {
      const stillSending = entries.some((e) => e.phase === "sending");
      const stillFailed = entries.some((e) => e.phase === "failed");
      const notice = snapshot.notice?.tone === "error" && !stillFailed ? null : snapshot.notice;
      set({ entries, busy: droppedSending ? stillSending : snapshot.busy, notice }, shouldPersist);
    }
  }

  // Drops what has stayed long enough: a replied entry the history never
  // matched, expired queued/failed entries, and the note under the textarea.
  function prune() {
    const t = now();
    let shouldPersist = false;
    const entries = snapshot.entries.filter((e) => {
      if (e.phase === "replied") return t - e.repliedAt < KEEP_MS;
      if (e.phase === "queued" || e.phase === "failed") {
        const keep = t - e.at / 1000 < OUTBOX_TTL_MS;
        if (!keep) shouldPersist = true;
        return keep;
      }
      return true;
    });
    const stillFailed = entries.some((e) => e.phase === "failed");
    let notice = snapshot.notice && snapshot.notice.tone === "info" && t - snapshot.notice.at >= NOTE_MS ? null : snapshot.notice;
    if (notice?.tone === "error" && !stillFailed) notice = null;
    if (entries.length !== snapshot.entries.length || notice !== snapshot.notice) set({ entries, notice }, shouldPersist);
  }

  return {
    getState: () => snapshot,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    submit,
    retry,
    remove,
    take,
    resume,
    reconcile,
    prune,
  };
}

// The entries as timeline items, for what the conversation does not show yet:
// only active ("sending") and completed ("replied") turns appear as timeline
// bubbles; "queued" and "failed" entries render as interactive controls in the
// composer so Retry / Edit / Remove buttons receive clicks.
export function pendingItems(entries) {
  const out = [];
  for (const e of entries) {
    if (e.phase !== "sending" && e.phase !== "replied") continue;
    const ts = (at) => new Date(at).toISOString();
    if (e.userId == null) out.push({ id: e.at, ts: ts(e.at / 1000), kind: "shepherd", via: "typed", text: e.text, pending: e.phase === "sending" });
    if (e.phase === "replied" && e.replyId == null && e.reply.trim()) out.push({ id: Math.max(e.repliedAt * 1000, e.at + 1), ts: ts(e.repliedAt), kind: "dog", via: "typed", text: e.reply });
  }
  return out;
}
