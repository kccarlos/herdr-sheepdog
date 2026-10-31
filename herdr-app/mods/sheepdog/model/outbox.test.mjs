// The typed lines on their way: the pending line, the reply, the match with
// the conversation by id, and what each failure does: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { DaemonError } from "../net/http.js";
import { createOutbox, KEEP_MS, NOTE_MS, pendingItems } from "./outbox.js";

// An outbox over a scripted `turn`: each call takes the next outcome (a reply
// object or an error to throw) and records its text and key.
function harness(outcomes, t0 = 1_000_000) {
  let clock = t0;
  const calls = [];
  const asked = [];
  let n = 0;
  const http = {
    async turn(text, key) {
      calls.push({ text, key });
      const outcome = outcomes.shift();
      if (outcome instanceof Error) throw outcome;
      return outcome;
    },
  };
  const outbox = createOutbox({ http, newKey: () => `k${++n}`, now: () => clock, refresh: async () => { asked.push(clock); } });
  return { outbox, calls, asked, advance: (ms) => { clock += ms; }, clock: () => clock };
}

const reply = (text) => ({ reply: text, turn_id: "u1", acted: false });
// What the daemon's conversation holds for a typed exchange, ids in the
// daemon's microseconds (the harness clock is in ms).
const line = (id, text) => ({ id, kind: "shepherd", via: "typed", text });
const answer = (id, text) => ({ id, kind: "dog", via: "typed", text });

test("a send shows as a pending line at once, then with the reply", async () => {
  const h = harness([reply("Done.")]);
  const sent = h.outbox.submit("run the tests");
  assert.equal(h.outbox.getState().busy, true);
  assert.deepEqual(pendingItems(h.outbox.getState().entries).map((i) => [i.kind, i.text, i.pending]), [["shepherd", "run the tests", true]]);
  assert.deepEqual(await sent, { restore: null });
  assert.equal(h.outbox.getState().busy, false);
  assert.deepEqual(pendingItems(h.outbox.getState().entries).map((i) => [i.kind, i.text, Boolean(i.pending)]), [["shepherd", "run the tests", false], ["dog", "Done.", false]]);
  assert.equal(h.asked.length, 1, "it asked for the conversation");
  assert.ok(pendingItems(h.outbox.getState().entries).every((i, k, all) => k === 0 || i.id > all[k - 1].id), "the reply sorts after the line");
});

test("the conversation's items replace the pending ones by id, so nothing shows twice", async () => {
  const h = harness([reply("Done.")]);
  await h.outbox.submit("run the tests");
  const at = h.outbox.getState().entries[0].at;
  // The line is in the conversation, the reply not yet.
  h.outbox.reconcile([line(at + 5, "run the tests")]);
  assert.deepEqual(pendingItems(h.outbox.getState().entries).map((i) => i.kind), ["dog"]);
  assert.equal(h.outbox.getState().entries[0].userId, at + 5);
  // Both are.
  h.outbox.reconcile([line(at + 5, "run the tests"), answer(at + 9, "Done.")]);
  assert.deepEqual(h.outbox.getState().entries, []);
});

test("an older identical line is not claimed, and two identical sends claim one each", async () => {
  const h = harness([reply("one"), reply("two")]);
  await h.outbox.submit("again");
  h.advance(10);
  await h.outbox.submit("again");
  const [a, b] = h.outbox.getState().entries;
  const old = line(a.at - 500 * 1000 * 1000, "again"); // long before the send
  h.outbox.reconcile([old, line(a.at + 1, "again")]);
  assert.equal(h.outbox.getState().entries[0].userId, a.at + 1);
  assert.equal(h.outbox.getState().entries[1].userId, null, "the second waits for its own line");
  h.outbox.reconcile([line(a.at + 1, "again"), answer(a.at + 2, "one"), line(b.at + 1, "again"), answer(b.at + 2, "two")]);
  assert.deepEqual(h.outbox.getState().entries, []);
});

test("a reply the conversation never shows is dropped after a while", async () => {
  const h = harness([reply("Done.")]);
  await h.outbox.submit("hi");
  h.advance(KEEP_MS - 1);
  h.outbox.prune();
  assert.equal(h.outbox.getState().entries.length, 1);
  h.advance(2);
  h.outbox.prune();
  assert.deepEqual(h.outbox.getState().entries, []);
});

test("an empty reply is done once the line is in the conversation", async () => {
  const h = harness([reply("")]);
  await h.outbox.submit("stop");
  assert.deepEqual(pendingItems(h.outbox.getState().entries).map((i) => i.kind), ["shepherd"]);
  h.outbox.reconcile([line(h.outbox.getState().entries[0].at, "stop")]);
  assert.deepEqual(h.outbox.getState().entries, []);
});

test("a retry after nothing answered reuses the key; anything else gets a new one", async () => {
  const h = harness([new DaemonError("down", "nothing answered"), reply("ok"), new DaemonError("http", "the orchestrator is off (no key)", 503), reply("ok")]);
  assert.deepEqual(await h.outbox.submit("same words"), { restore: "same words" });
  assert.deepEqual(await h.outbox.submit("same words"), { restore: null });
  assert.equal(h.calls[0].key, h.calls[1].key, "unknown outcome: the daemon may have run it, so the key is kept");

  assert.deepEqual(await h.outbox.submit("same words"), { restore: "same words" }, "503");
  await h.outbox.submit("same words");
  assert.notEqual(h.calls[2].key, h.calls[3].key, "a refusal is remembered by the daemon per key");
  assert.notEqual(h.calls[1].key, h.calls[2].key, "a sent line does not leak its key");
});

test("different text after a failure gets a new key", async () => {
  const h = harness([new DaemonError("down", "nothing answered"), reply("ok")]);
  await h.outbox.submit("first");
  await h.outbox.submit("second");
  assert.notEqual(h.calls[0].key, h.calls[1].key);
});

test("503 leaves no line, shows the daemon's reason and gives the text back", async () => {
  const h = harness([new DaemonError("http", "the orchestrator is off (no brain key)", 503)]);
  assert.deepEqual(await h.outbox.submit("hello"), { restore: "hello" });
  const s = h.outbox.getState();
  assert.deepEqual(s.entries, []);
  assert.equal(s.busy, false);
  assert.deepEqual([s.notice.tone, s.notice.text], ["error", "the orchestrator is off (no brain key)"]);
});

test("409 says it was answered by voice and keeps nothing", async () => {
  const h = harness([new DaemonError("http", "a spoken turn is already answering this", 409)]);
  assert.deepEqual(await h.outbox.submit("hello"), { restore: null });
  const s = h.outbox.getState();
  assert.deepEqual(s.entries, []);
  assert.deepEqual([s.notice.tone, s.notice.text], ["info", "Answered by voice."]);
  h.advance(NOTE_MS + 1);
  h.outbox.prune();
  assert.equal(h.outbox.getState().notice, null, "the note goes by itself");
});

test("an idempotency 409 is a failure to retry, not an answer by voice", async () => {
  const h = harness([new DaemonError("http", "the first request with this Idempotency-Key did not keep its reply; send a new key", 409)]);
  assert.deepEqual(await h.outbox.submit("hello"), { restore: "hello" });
  assert.equal(h.outbox.getState().notice.tone, "error");
});

test("nothing answering says so and keeps the text", async () => {
  const h = harness([new DaemonError("down", "nothing answered")]);
  assert.deepEqual(await h.outbox.submit("hello"), { restore: "hello" });
  assert.match(h.outbox.getState().notice.text, /did not answer/);
});

test("one line at a time: a second send while one runs sends nothing", async () => {
  const h = harness([reply("a")]);
  const first = h.outbox.submit("one");
  assert.deepEqual(await h.outbox.submit("two"), { restore: "two" });
  await first;
  assert.deepEqual(h.calls.map((c) => c.text), ["one"]);
});

test("a new send clears the last note, and subscribers hear every change", async () => {
  const h = harness([new DaemonError("http", "off", 503), reply("ok")]);
  let heard = 0;
  h.outbox.subscribe(() => heard++);
  await h.outbox.submit("a");
  assert.ok(h.outbox.getState().notice);
  const before = heard;
  await h.outbox.submit("b");
  assert.equal(h.outbox.getState().notice, null);
  assert.ok(heard > before);
});
