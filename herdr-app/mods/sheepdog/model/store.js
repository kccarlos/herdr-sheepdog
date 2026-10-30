// The panel's state store: the conversation history, the daemon's /api/state,
// whether the daemon answers and whether the live stream is up. It asks the
// daemon (through `http`, which net/http.js makes) and tells subscribers when
// something changed; it never draws. No herdr imports: node tests it.

import { applyNewest, applyOlder, canLoadOlder, emptyHistory, newestId } from "./history.js";

// How many items one page asks for.
export const PAGE_SIZE = 50;

// `conversation: false` is for the rail, which shows the voice and the
// questions and has no use for the timeline.
export function createStore({ http, now = Date.now, pageSize = PAGE_SIZE, conversation = true }) {
  let snapshot = {
    history: emptyHistory(),
    app: null, // the last good /api/state
    daemon: "unknown", // unknown | up | down, from the last request
    detail: "", // why it is down
    stream: "connecting", // connecting | live | down
    loadingOlder: false,
  };
  const listeners = new Set();
  let running = null; // the refresh loop in flight
  let queued = null; // what asked for another round while it ran

  const set = (patch) => {
    snapshot = { ...snapshot, ...patch };
    for (const listener of listeners) listener(snapshot);
  };

  // One round: /api/state and the newest conversation page together.
  async function round(want) {
    // Event ids are Unix microseconds; an empty page ends where this began.
    const requestedAt = now() * 1000;
    const [state, page] = await Promise.allSettled([
      want.state ? http.state() : null,
      want.conversation && conversation ? http.conversation({ limit: pageSize }) : null,
    ]);
    const failed = [state, page].find((r) => r.status === "rejected");
    const patch = {};
    if (state.status === "fulfilled" && state.value) patch.app = state.value;
    if (page.status === "fulfilled" && page.value) patch.history = applyNewest(snapshot.history, page.value, requestedAt);
    if (failed) {
      // A refused connection says everything; anything else (an error status,
      // something that is not sheepdog) says what it was.
      patch.daemon = "down";
      patch.detail = failed.reason?.kind === "down" ? "" : (failed.reason?.message ?? "");
    } else {
      patch.daemon = "up";
      patch.detail = "";
    }
    set(patch);
  }

  // Asks again for `what` ({state, conversation}). Calls that come while a
  // round runs are folded into one more round, so a burst of events costs a
  // request or two, not one each.
  function refresh(what = { state: true, conversation: true }) {
    queued = { state: (queued?.state ?? false) || what.state, conversation: (queued?.conversation ?? false) || what.conversation };
    if (running) return running;
    running = (async () => {
      while (queued) {
        const want = queued;
        queued = null;
        await round(want);
      }
    })().finally(() => {
      running = null;
    });
    return running;
  }

  // The next older page, when there is one. Resolves with how many items it
  // added.
  async function loadOlder() {
    const { history, loadingOlder } = snapshot;
    if (loadingOlder || !canLoadOlder(history)) return 0;
    set({ loadingOlder: true });
    try {
      const page = await http.conversation({ limit: pageSize, before: history.nextBefore });
      const next = applyOlder(snapshot.history, page);
      const added = next.items.length - snapshot.history.items.length;
      set({ history: next, loadingOlder: false });
      return added;
    } catch {
      // The next round says whether the daemon is gone; paging can be retried.
      set({ loadingOlder: false });
      return 0;
    }
  }

  return {
    getState: () => snapshot,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    refresh,
    loadOlder,
    setStream: (stream) => set({ stream }),
    newestId: () => newestId(snapshot.history),
  };
}
