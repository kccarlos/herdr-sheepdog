// The history merge and the store that drives it: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { applyNewest, applyOlder, emptyHistory, MEMORY_CAPACITY } from "./history.js";
import { createStore } from "./store.js";
import { DaemonError } from "../net/http.js";

const it = (id, extra = {}) => ({ id, ts: new Date(id / 1000).toISOString(), kind: "dog", text: `item ${id}`, ...extra });
const page = (ids, next_before = null) => ({ items: ids.map((id) => it(id)), next_before });
const ids = (h) => h.items.map((i) => i.id);

test("the newest page is the record: its range is replaced, newer and older items stay", () => {
  let h = applyNewest(emptyHistory(), page([10, 11, 12], 10));
  assert.deepEqual(ids(h), [10, 11, 12]);
  assert.equal(h.nextBefore, 10);
  // 11 is gone on the server, 13 arrived while the request was in flight.
  h = { ...h, items: [...h.items, it(20)] };
  h = applyNewest(h, page([10, 12, 14], 10));
  assert.deepEqual(ids(h), [10, 12, 14, 20]);
});

test("an item the server has again takes the server's copy", () => {
  let h = applyNewest(emptyHistory(), { items: [it(5, { kind: "approval", resolution: "open" })], next_before: null });
  h = applyNewest(h, { items: [it(5, { kind: "approval", resolution: "confirmed" })], next_before: null });
  assert.equal(h.items.length, 1);
  assert.equal(h.items[0].resolution, "confirmed");
});

test("older pages merge by id and move the cursor; the end says there is no more", () => {
  let h = applyNewest(emptyHistory(), page([30, 31], 30));
  h = applyOlder(h, page([20, 21, 30], 20));
  assert.deepEqual(ids(h), [20, 21, 30, 31]);
  assert.equal(h.nextBefore, 20);
  h = applyOlder(h, page([10], null));
  assert.equal(h.nextBefore, null);
});

test("a refresh keeps the cursor of the pages already loaded", () => {
  let h = applyNewest(emptyHistory(), page([30, 31], 30));
  h = applyOlder(h, page([20, 21], 20));
  h = applyNewest(h, page([30, 31, 32], 30));
  assert.deepEqual(ids(h), [20, 21, 30, 31, 32]);
  assert.equal(h.nextBefore, 20);
});

test("a short history with nothing older drops what the server no longer has", () => {
  let h = applyNewest(emptyHistory(), page([1, 2, 3], null));
  h = applyNewest(h, page([3], null));
  assert.deepEqual(ids(h), [3]);
});

test("an empty newest page ends where the request began", () => {
  let h = applyNewest(emptyHistory(), page([1, 2], null));
  h = applyNewest(h, { items: [], next_before: null }, 100);
  assert.deepEqual(ids(h), []);
  h = applyNewest({ ...h, items: [it(500)] }, { items: [], next_before: null }, 100);
  assert.deepEqual(ids(h), [500], "newer than the request, so kept");
});

test("memory is bounded: paging back past it drops the old end, a newest page drops the top", () => {
  const many = Array.from({ length: MEMORY_CAPACITY }, (_, i) => it(1000 + i));
  let h = applyNewest(emptyHistory(), { items: many, next_before: 1000 });
  h = applyOlder(h, page([1, 2, 3], null));
  assert.equal(h.items.length, MEMORY_CAPACITY);
  assert.equal(h.items[0].id, 1);
  h = applyNewest(h, page([9000, 9001], 9000));
  assert.equal(h.items.length, MEMORY_CAPACITY);
  assert.equal(h.items[h.items.length - 1].id, 9001);
  assert.equal(h.nextBefore, h.items[0].id);
});

// A daemon that serves `pages` newest first, and counts the requests.
function fakeHttp({ state = { voice: { mode: "gemini" }, questions: [] }, pages = [], down = false } = {}) {
  const calls = { state: 0, conversation: [] };
  return {
    calls,
    async state() {
      calls.state++;
      if (down) throw new DaemonError("down", "nothing answered");
      return state;
    },
    async conversation(q) {
      calls.conversation.push(q);
      if (down) throw new DaemonError("down", "nothing answered");
      const at = q.before ? pages.findIndex((p) => p.items.some((i) => i.id === q.before)) + 1 : 0;
      return pages[at] ?? { items: [], next_before: null };
    },
  };
}

test("the store loads the newest page and the state, then pages back", async () => {
  const http = fakeHttp({ pages: [page([30, 31], 30), page([20, 21], 20), page([10], null)] });
  const store = createStore({ http, now: () => 0, pageSize: 2 });
  assert.equal(store.getState().daemon, "unknown");
  await store.refresh();
  assert.equal(store.getState().daemon, "up");
  assert.deepEqual(ids(store.getState().history), [30, 31]);
  assert.equal(await store.loadOlder(), 2);
  assert.equal(await store.loadOlder(), 1);
  assert.equal(await store.loadOlder(), 0, "nothing older");
  assert.deepEqual(http.calls.conversation, [{ limit: 2 }, { limit: 2, before: 30 }, { limit: 2, before: 20 }]);
  assert.deepEqual(ids(store.getState().history), [10, 20, 21, 30, 31]);
});

test("requests that arrive while a round runs become one more round", async () => {
  const http = fakeHttp({ pages: [page([1], null)] });
  const store = createStore({ http, now: () => 0 });
  const first = store.refresh();
  store.refresh();
  store.refresh({ state: true, conversation: false });
  await first;
  assert.equal(http.calls.state, 2);
  assert.equal(http.calls.conversation.length, 2);
});

test("the rail's store never asks for the conversation", async () => {
  const http = fakeHttp();
  const store = createStore({ http, now: () => 0, conversation: false });
  await store.refresh();
  assert.equal(http.calls.state, 1);
  assert.equal(http.calls.conversation.length, 0);
});

test("a daemon that does not answer is down, and answering again clears it", async () => {
  const http = fakeHttp({ down: true });
  const store = createStore({ http, now: () => 0 });
  const seen = [];
  store.subscribe((s) => seen.push(s.daemon));
  await store.refresh();
  assert.equal(store.getState().daemon, "down");
  assert.equal(store.getState().detail, "");
  http.state = async () => ({ voice: { mode: "gemini" }, questions: [] });
  http.conversation = async () => page([1], null);
  await store.refresh();
  assert.equal(store.getState().daemon, "up");
  assert.deepEqual(seen, ["down", "up"]);
});

test("an answer that is not sheepdog's says what it was", async () => {
  const http = fakeHttp();
  http.state = async () => {
    throw new DaemonError("invalid", "Something answered, but it is not sheepdog.");
  };
  const store = createStore({ http, now: () => 0 });
  await store.refresh();
  assert.equal(store.getState().daemon, "down");
  assert.match(store.getState().detail, /not sheepdog/);
});

test("observed events keep the orchestrator's replies, and tell subscribers only when one changed", () => {
  const store = createStore({ http: fakeHttp(), now: () => 0 });
  let heard = 0;
  store.subscribe(() => heard++);
  const result = { type: "voice.tool_result", payload: { name: "to_supervisor", spoken: "Shall I?", options: ["Yes"] } };
  store.observe(result, 10);
  store.observe({ type: "voice.push", payload: {} }, 11);
  store.observe(result, 10);
  assert.deepEqual(store.getState().replies, [{ id: 10, options: ["Yes"], pending: false }]);
  assert.equal(heard, 1);
});
