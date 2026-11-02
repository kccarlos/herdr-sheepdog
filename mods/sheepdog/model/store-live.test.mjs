// What the store does with refetches that change nothing and with words that
// arrive on the stream: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { applyNewest, emptyHistory } from "./history.js";
import { createStore } from "./store.js";

const it = (id, extra = {}) => ({ id, ts: new Date(id / 1000).toISOString(), kind: "dog", text: `item ${id}`, ...extra });
const page = (ids) => ({ items: ids.map((id) => it(id)), next_before: null });

// A daemon that answers with fresh objects every time, like JSON does.
function fakeHttp() {
  const http = {
    state: async () => ({ voice: { mode: "gemini" }, questions: [] }),
    conversation: async () => structuredClone(page([1, 2])),
  };
  return http;
}

test("a refetch that brings nothing new does not notify; one that does, does", async () => {
  const http = fakeHttp();
  const store = createStore({ http, now: () => 0 });
  await store.refresh();
  let notified = 0;
  store.subscribe(() => notified++);
  await store.refresh();
  await store.refresh({ state: true, conversation: false });
  assert.equal(notified, 0);
  http.state = async () => ({ voice: { mode: "gemini" }, questions: [{ id: "q" }] });
  await store.refresh({ state: true, conversation: false });
  assert.equal(notified, 1);
});

test("an unchanged history keeps its identity, so the list is not rebuilt", () => {
  const h = applyNewest(emptyHistory(), page([1, 2]));
  assert.equal(applyNewest(h, page([1, 2])), h);
  assert.notEqual(applyNewest(h, page([1, 2, 3])), h);
});

test("words on the stream show at once as the item the daemon will give them, and its page replaces them", async () => {
  const http = fakeHttp();
  http.conversation = async () => page([1]);
  const store = createStore({ http, now: () => 0 });
  await store.refresh();
  const seen = [];
  store.subscribe((s) => seen.push(s.history.items.map((i) => `${i.id}:${i.kind}:${i.text}`).join("|")));
  const ev = (type, payload) => ({ type, ts: "2026-01-01T00:00:00Z", payload });
  store.observe(ev("voice.transcript.in", { text: "fix the " }), 100);
  store.observe(ev("voice.transcript.in", { text: "login" }), 101);
  store.observe(ev("voice.transcript.in", { text: " bug", final: true }), 102);
  store.observe(ev("voice.transcript.out", { text: "On it." }), 103);
  assert.deepEqual(seen, [
    "1:dog:item 1|100:shepherd:fix the",
    "1:dog:item 1|100:shepherd:fix the login",
    "1:dog:item 1|100:shepherd:fix the login bug",
    "1:dog:item 1|100:shepherd:fix the login bug|103:dog:On it.",
  ]);
  // The daemon's page has the same ids: its copies replace ours.
  http.conversation = async () => ({ items: [it(1), it(100, { kind: "shepherd", text: "fix the login bug", via: "voice" }), it(103, { text: "On it." })], next_before: null });
  await store.refresh({ state: false, conversation: true });
  assert.deepEqual(store.getState().history.items.map((i) => i.id), [1, 100, 103]);
  assert.equal(store.getState().history.items[1].via, "voice");
});

test("a shown line the daemon does not have after all is dropped by its page", async () => {
  const http = fakeHttp();
  http.conversation = async () => page([1]);
  const store = createStore({ http, now: () => 0 });
  await store.refresh();
  store.observe({ type: "voice.text", ts: "t", payload: { text: "ghost" } }, 50);
  assert.equal(store.getState().history.items.length, 2);
  http.conversation = async () => page([1, 60]);
  await store.refresh({ state: false, conversation: true });
  assert.deepEqual(store.getState().history.items.map((i) => i.id), [1, 60]);
});

test("the rail's store, which has no timeline, ignores the words", () => {
  const store = createStore({ http: fakeHttp(), now: () => 0, conversation: false });
  store.observe({ type: "voice.text", payload: { text: "hi" } }, 5);
  assert.equal(store.getState().history.items.length, 0);
});
