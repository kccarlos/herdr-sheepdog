// Keeps the store current: the live stream when it is up, a poll when it is
// not (docs/dev/design/desktop-client.md, section 6.2). The words of an event
// are shown at once by the store (model/fragments.js); a daemon event that
// changes anything else makes the store ask /api/state and /api/conversation
// again, and the ones whose words are already shown ask once things settle.
// While the stream is down a 3 s poll asks instead, and the stream's status is
// what the rail reads as "Live updates paused". A slow check while it is up
// catches anything an event did not say.
// Each event is also handed to the store's `observe`.
// Takes `connect` and the clock as arguments, no herdr imports: node tests it.

import { eventEffect, KINDS } from "../model/events.js";
import { SseClient } from "./sse.js";

// How often the daemon is asked while the stream is down.
export const POLL_MS = 3000;
// The least time between two rounds of asking, so a burst of events costs a
// request or two.
export const MIN_GAP_MS = 40;
// How long events whose words are already shown must stop before the
// conversation is checked: transcript fragments come many times a second and
// settle into one item.
export const QUIET_MS = 700;
// How often the daemon is asked while the stream is up, in case an event was
// not enough to know what changed.
export const CHECK_MS = 30000;

const ALL = { state: true, conversation: true };

export function createLive({ store, connect, endpoint, clock, kinds = KINDS }) {
  let stopped = false;
  let live = false; // the stream is delivering
  let pending = null; // what is waiting for the next round
  let draining = false;
  let settle = 0; // counts transcript fragments, to tell the last one
  let client = null;
  let lastRound = clock.now(); // when the daemon was last asked

  // Asks for `what`, one round at a time with a gap between rounds.
  async function request(what) {
    pending = { state: (pending?.state ?? false) || what.state, conversation: (pending?.conversation ?? false) || what.conversation };
    if (draining) return;
    draining = true;
    while (pending && !stopped) {
      const next = pending;
      pending = null;
      await store.refresh(next);
      lastRound = clock.now();
      await clock.sleep(MIN_GAP_MS);
    }
    draining = false;
  }

  async function quietThen(what) {
    const mine = ++settle;
    await clock.sleep(QUIET_MS);
    if (mine === settle && !stopped) await request(what);
  }

  function onEvent(event, id) {
    store.observe(event, id);
    switch (eventEffect(event.type)) {
      case "now": request(ALL); break;
      case "state": request({ state: true, conversation: false }); break;
      case "settle": quietThen(ALL); break;
      default: break;
    }
  }

  function onStatus({ state }) {
    live = state === "live";
    store.setStream(state);
    // Whatever happened while the stream was down is asked for in one go.
    if (live) request(ALL);
  }

  async function poll() {
    while (!stopped) {
      await clock.sleep(POLL_MS);
      // With the stream up, only a long silence is asked about.
      if (!stopped && (!live || clock.now() - lastRound >= CHECK_MS)) await request(ALL);
    }
  }

  return {
    // Resolves when stop() is called.
    async run() {
      await request(ALL);
      if (stopped) return;
      client = new SseClient({ connect, ...endpoint, kinds, clock, onEvent, onStatus, lastId: store.newestId() });
      await Promise.all([client.run(), poll()]);
    },
    stop() {
      stopped = true;
      client?.stop();
    },
  };
}
