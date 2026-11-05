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
export const PANEL_WINDOW_KEY = "sheepdog.panel_window";
export const PANEL_WINDOW_FRESH_MS = 10 * 1000;
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
export function createOutbox({
  http,
  newKey,
  now = Date.now,
  refresh = async () => {},
  items = () => [],
  saved = null,
  load = () => saved,
  save = () => {},
  active: initialActive = true,
}) {
  let active = Boolean(initialActive);
  const initialEntries = active ? loadSavedEntries(saved, now()) : [];
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
  let activeKey = null;
  let editingHead = false;
  let resumedOnce = false;
  const reconciledIds = new Set();
  const listeners = new Set();

  const rememberReconciled = (id) => {
    if (id == null) return;
    if (reconciledIds.size >= 200) {
      reconciledIds.delete(reconciledIds.values().next().value);
    }
    reconciledIds.add(id);
  };

  const persist = (entries) => {
    if (!active && !activeKey) return;
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

  const hasFailure = () => snapshot.entries.some((e) => e.phase === "failed");

  function prepareNextQueued() {
    if (!active || activeKey || hasFailure() || editingHead) return null;
    const next = snapshot.entries.find((e) => e.phase === "queued");
    if (!next) return null;
    const sendKey = next.reuseKey ? next.key : newKey();
    if (sendKey !== next.key) {
      set({ entries: snapshot.entries.map((e) => (e.key === next.key ? { ...e, key: sendKey, reuseKey: true } : e)) }, true);
    }
    return sendKey;
  }

  function startNextQueued() {
    const sendKey = prepareNextQueued();
    if (!sendKey) return false;
    void runEntry(sendKey);
    return true;
  }

  async function runEntry(entryKey) {
    const current = snapshot.entries.find((e) => e.key === entryKey);
    if (!current) return { restore: null };
    activeKey = entryKey;
    const sendAt = current.phase === "queued" || (!current.resumed && !current.reuseKey) ? now() * 1000 : current.at;
    set(
      {
        entries: snapshot.entries.map((e) => (e.key === entryKey ? { ...e, at: sendAt, phase: "sending", error: null, resumed: false } : e)),
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
        if (activeKey === entryKey) activeKey = null;
        if (!startNextQueued()) set({ busy: false }, true);
        return { restore: null };
      }
      if (err.status === 409 && !/Idempotency-Key/.test(err.message)) {
        // A spoken turn claimed the words first and answers them.
        if (activeKey === entryKey) activeKey = null;
        const remaining = snapshot.entries.filter((e) => e.key !== entryKey);
        const willDrain = active && !editingHead && !remaining.some((e) => e.phase === "failed") && remaining.some((e) => e.phase === "queued");
        set(
          {
            entries: remaining,
            busy: willDrain,
            notice: { tone: "info", text: "Answered by voice.", at: now() },
          },
          true,
        );
        if (willDrain) startNextQueued();
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
      if (activeKey === entryKey) activeKey = null;
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
    if (snapshot.entries.some((e) => e.key === entryKey)) {
      set(
        {
          entries: snapshot.entries.map((e) =>
            e.key === entryKey
              ? { ...e, phase: "replied", reply: typeof res?.reply === "string" ? res.reply : "", repliedAt: now(), error: null, resumed: false }
              : e,
          ),
        },
        true,
      );
      reconcile(items());
    }
    try {
      await refresh();
    } catch {
      // refresh failures do not drop queued follow-ups
    } finally {
      if (activeKey === entryKey) activeKey = null;
    }
    if (!startNextQueued() && snapshot.busy) {
      set({ busy: false });
    }
    return { restore: null };
  }

  function drainNext() {
    if (snapshot.busy) return Promise.resolve({ restore: null });
    const sendKey = prepareNextQueued();
    if (!sendKey) return Promise.resolve({ restore: null });
    return runEntry(sendKey);
  }

  // Sends `text` (already stripped; see model/composer.js), or queues it if a
  // turn is already running or paused behind a failure.
  async function submit(text) {
    if (!snapshot.busy && !editingHead) {
      const existingIdx = snapshot.entries.findIndex((e) => e.phase === "failed" && same(e.text, text));
      if (
        existingIdx >= 0 &&
        !snapshot.entries.slice(0, existingIdx).some((e) => e.phase === "failed") &&
        !snapshot.entries.slice(existingIdx + 1).some((e) => e.phase === "queued")
      ) {
        consumeTakenKey(text);
        return retry(snapshot.entries[existingIdx].key);
      }
    }
    if (snapshot.busy || hasFailure()) {
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
    const spliceHead = editingHead;
    editingHead = false;
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
    const firstQueued = spliceHead ? snapshot.entries.findIndex((e) => e.phase === "queued") : -1;
    const entries =
      firstQueued < 0
        ? [...snapshot.entries, entry]
        : [...snapshot.entries.slice(0, firstQueued), entry, ...snapshot.entries.slice(firstQueued)];
    set({ entries, busy: true, notice: null }, true);
    return runEntry(key);
  }

  // Retries a failed entry (or starts a paused queued entry when idle and unblocked).
  async function retry(key) {
    if (snapshot.busy) return { restore: null };
    const idx = snapshot.entries.findIndex((e) => e.key === key && (e.phase === "failed" || e.phase === "queued"));
    if (idx < 0) return { restore: null };
    if (snapshot.entries.slice(0, idx).some((e) => e.phase === "failed")) return { restore: null };
    editingHead = false;
    const target = snapshot.entries[idx];
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
    if (!entries.some((e) => e.phase === "queued")) editingHead = false;
    const stillFailed = entries.some((e) => e.phase === "failed");
    const notice = snapshot.notice?.tone === "error" && !stillFailed ? null : snapshot.notice;
    set({ entries, notice }, true);
    if (target.phase === "failed" && !snapshot.busy && !stillFailed && !editingHead) void drainNext();
  }

  // Removes a queued or failed entry and returns its text so the composer can
  // edit it without prematurely firing queued follow-ups behind it.
  function take(key) {
    const idx = snapshot.entries.findIndex((e) => e.key === key && (e.phase === "queued" || e.phase === "failed"));
    if (idx < 0) return null;
    const target = snapshot.entries[idx];
    if (target.phase === "failed" && target.reuseKey) {
      takenRetry = { text: target.text, key: target.key, reuseKey: true };
    }
    const entries = snapshot.entries.filter((e) => e.key !== key);
    if (target.phase === "failed" && idx === 0 && entries.some((e) => e.phase === "queued")) {
      editingHead = true;
    } else if (!entries.some((e) => e.phase === "queued")) {
      editingHead = false;
    }
    const stillFailed = entries.some((e) => e.phase === "failed");
    const notice = snapshot.notice?.tone === "error" && !stillFailed ? null : snapshot.notice;
    set({ entries, notice }, true);
    return target.text;
  }

  function setActive(next) {
    const want = Boolean(next);
    if (want === active) return;
    active = want;
    if (!active) {
      if (!activeKey) {
        resumedOnce = false;
        editingHead = false;
        if (snapshot.entries.length > 0 || snapshot.busy || snapshot.notice) {
          set({ entries: [], busy: false, notice: null });
        }
      }
      return;
    }
    if (!activeKey && snapshot.entries.length === 0) {
      const loaded = loadSavedEntries(load(), now());
      const busy = loaded.some((e) => e.phase === "sending");
      const failed = loaded.find((e) => e.phase === "failed");
      resumedOnce = false;
      set({
        entries: loaded,
        busy,
        notice: !busy && failed ? { tone: "error", text: failed.error, at: now() } : null,
      });
    }
  }

  // Reattaches to an in-flight persisted turn (or drains restored queued items)
  // once the initial conversation snapshot has been reconciled.
  async function resume() {
    if (!active || resumedOnce) return { restore: null };
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
    if (!active && !activeKey) return;
    const claimed = new Set(reconciledIds);
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
        rememberReconciled(userId);
        rememberReconciled(replyId);
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
      set({ entries, busy: droppedSending ? activeKey != null || stillSending : snapshot.busy, notice }, shouldPersist);
    }
  }

  // Drops what has stayed long enough: a replied entry the history never
  // matched, expired queued/failed entries, and the note under the textarea.
  function prune() {
    if (!active && !activeKey) return;
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
    setActive,
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
