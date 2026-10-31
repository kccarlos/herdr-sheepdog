// Keeps the store current: the live stream when it is up, a poll when it is
// not (docs/dev/design/desktop-client.md, section 6.2). A daemon event that
// changes what the panel shows makes the store ask /api/state and
// /api/conversation again; while the stream is down a 3 s poll does the same,
// and the stream's status is what the rail reads as "Live updates paused".
// Each event is also handed to the store's `observe`, for the replies'
// suggested options, which only the event's payload has.
// Takes `connect` and the clock as arguments, no herdr imports: node tests it.

import { eventEffect, KINDS } from "../model/events.js";
import { SseClient } from "./sse.js";

// How often the daemon is asked while the stream is down.
export const POLL_MS = 3000;
// The least time between two rounds of asking, so a burst of events costs a
// request or two.
export const MIN_GAP_MS = 250;
// How long transcript fragments must stop before the conversation is asked
// again: they come many times a second and settle into one item.
export const QUIET_MS = 1500;

const ALL = { state: true, conversation: true };

export function createLive({ store, connect, endpoint, clock, kinds = KINDS }) {
  let stopped = false;
  let live = false; // the stream is delivering
  let pending = null; // what is waiting for the next round
  let draining = false;
  let settle = 0; // counts transcript fragments, to tell the last one
  let client = null;

  // Asks for `what`, one round at a time with a gap between rounds.
  async function request(what) {
    pending = { state: (pending?.state ?? false) || what.state, conversation: (pending?.conversation ?? false) || what.conversation };
    if (draining) return;
    draining = true;
    while (pending && !stopped) {
      const next = pending;
      pending = null;
      await store.refresh(next);
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
      case "later": quietThen({ state: false, conversation: true }); break;
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
      if (!stopped && !live) await request(ALL);
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
