// The live layer: events make the store ask again, the poll covers a stream
// that is down: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createLive, MIN_GAP_MS, POLL_MS, QUIET_MS } from "./live.js";

const tick = async (n = 5) => {
  for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r));
};

function fakeClock() {
  let now = 0;
  const timers = [];
  return {
    now: () => now,
    sleep: (ms) => new Promise((resolve) => timers.push({ at: now + ms, resolve })),
    async advance(ms) {
      const target = now + ms;
      for (;;) {
        await tick(3);
        timers.sort((a, b) => a.at - b.at);
        if (!timers.length || timers[0].at > target) break;
        const timer = timers.shift();
        now = Math.max(now, timer.at);
        timer.resolve();
      }
      now = target;
      await tick(3);
    },
  };
}

// A store that records what it was asked, and a connect that hands out one
// scripted stream whose events the test pushes.
function harness() {
  const clock = fakeClock();
  const asked = [];
  const streams = [];
  const observed = [];
  const store = {
    refresh: async (what) => { asked.push(what); },
    setStream: (s) => streams.push(s),
    observe: (event, id) => observed.push([event.type, id]),
    newestId: () => 77,
  };
  let socket;
  const connect = async () => {
    const queue = [];
    let waiting = null;
    socket = {
      written: "",
      async write(d) { this.written += d; },
      read: () => (queue.length ? Promise.resolve(queue.shift()) : new Promise((r) => { waiting = r; })),
      close() {},
      feed(text) {
        const b = new TextEncoder().encode(text);
        if (waiting) { const r = waiting; waiting = null; r(b); } else queue.push(b);
      },
    };
    return socket;
  };
  const live = createLive({ store, connect, endpoint: { host: "127.0.0.1", port: 7777, authority: "127.0.0.1:7777" }, clock });
  const chunk = (s) => `${new TextEncoder().encode(s).length.toString(16)}\r\n${s}\r\n`;
  // Ids above the 77 the stream resumed from, or they would be replays.
  const event = (id, type) => chunk(`id: ${1000 + id}\nevent: log\ndata: ${JSON.stringify({ type, payload: {} })}\n\n`);
  return { clock, asked, observed, streams, live, chunk, event, socket: () => socket };
}

const HEAD = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n";

test("it asks once at the start, resumes the stream after the newest item, and asks again when the stream is up", async () => {
  const h = harness();
  const done = h.live.run();
  await h.clock.advance(MIN_GAP_MS); // the start's own round, and its gap
  assert.deepEqual(h.asked, [{ state: true, conversation: true }]);
  assert.match(h.socket().written, /since=77 /);
  assert.deepEqual(h.streams, ["connecting"]);
  h.socket().feed(HEAD + h.chunk(": hi\n\n"));
  await tick();
  assert.deepEqual(h.streams, ["connecting", "live"]);
  await h.clock.advance(MIN_GAP_MS);
  assert.equal(h.asked.length, 2, "caught up on going live");
  h.live.stop();
  await h.clock.advance(POLL_MS);
  await done;
});

test("a burst of events is one or two rounds, and only events that change something ask", async () => {
  const h = harness();
  const done = h.live.run();
  await h.clock.advance(MIN_GAP_MS); // the start's own round, and its gap
  h.socket().feed(HEAD + h.chunk(": hi\n\n"));
  await h.clock.advance(MIN_GAP_MS * 2);
  h.asked.length = 0;
  h.socket().feed(h.event(1, "pending.created") + h.event(2, "pending.created") + h.event(3, "voice.push") + h.event(4, "web.client.stats"));
  await h.clock.advance(MIN_GAP_MS * 3);
  assert.ok(h.asked.length >= 1 && h.asked.length <= 2, `asked ${h.asked.length} times`);
  assert.deepEqual(h.asked[0], { state: true, conversation: true });
  h.asked.length = 0;
  h.socket().feed(h.event(5, "web.client.stats") + h.event(6, "voice.playback"));
  await h.clock.advance(5000);
  assert.deepEqual(h.asked, []);
  h.live.stop();
  await h.clock.advance(POLL_MS);
  await done;
});

test("every event is handed to the store with its id, whether or not it asks", async () => {
  const h = harness();
  const done = h.live.run();
  await h.clock.advance(MIN_GAP_MS);
  h.socket().feed(HEAD + h.chunk(": hi\n\n"));
  await h.clock.advance(MIN_GAP_MS * 2);
  h.socket().feed(h.event(1, "voice.tool_result") + h.event(2, "web.client.stats"));
  await h.clock.advance(MIN_GAP_MS * 2);
  assert.deepEqual(h.observed, [["voice.tool_result", 1001], ["web.client.stats", 1002]]);
  h.live.stop();
  await h.clock.advance(POLL_MS);
  await done;
});

test("a state-only event does not ask for the conversation", async () => {
  const h = harness();
  const done = h.live.run();
  await h.clock.advance(MIN_GAP_MS); // the start's own round, and its gap
  h.socket().feed(HEAD + h.chunk(": hi\n\n"));
  await h.clock.advance(MIN_GAP_MS * 2);
  h.asked.length = 0;
  h.socket().feed(h.event(1, "web.client.attach"));
  await h.clock.advance(MIN_GAP_MS * 2);
  assert.deepEqual(h.asked, [{ state: true, conversation: false }]);
  h.live.stop();
  await h.clock.advance(POLL_MS);
  await done;
});

test("transcript fragments wait for a quiet moment, then ask for the conversation once", async () => {
  const h = harness();
  const done = h.live.run();
  await h.clock.advance(MIN_GAP_MS); // the start's own round, and its gap
  h.socket().feed(HEAD + h.chunk(": hi\n\n"));
  await h.clock.advance(MIN_GAP_MS * 2);
  h.asked.length = 0;
  for (let i = 1; i <= 5; i++) {
    h.socket().feed(h.event(i, "voice.transcript.in"));
    await h.clock.advance(QUIET_MS / 2);
  }
  assert.deepEqual(h.asked, [], "still talking");
  await h.clock.advance(QUIET_MS);
  assert.deepEqual(h.asked, [{ state: false, conversation: true }]);
  h.live.stop();
  await h.clock.advance(POLL_MS);
  await done;
});

test("with the stream down the daemon is polled every 3 s until stop()", async () => {
  // A connect that refuses: the stream never comes up.
  const clock = fakeClock();
  const asked = [];
  const streams = [];
  const live = createLive({
    store: { refresh: async (w) => { asked.push(w); }, setStream: (s) => streams.push(s), newestId: () => 0 },
    connect: async () => { throw new Error("refused"); },
    endpoint: { host: "127.0.0.1", port: 7777, authority: "127.0.0.1:7777" },
    clock,
  });
  const done = live.run();
  await clock.advance(POLL_MS * 3 + 1000);
  assert.ok(streams.includes("down"));
  // Once at the start, then a poll every 3 s (after the start's own gap).
  assert.equal(asked.length, 4);
  live.stop();
  await clock.advance(20_000);
  await done;
  assert.equal(asked.length, 4, "stopped means stopped");
});
