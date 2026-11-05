// The typed lines on their way: the pending line, the reply, the match with
// the conversation by id, the follow-up queue, inline retries, and shared-state
// persistence across mod reloads: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { DaemonError } from "../net/http.js";
import { createOutbox, IDEM_TTL_MS, KEEP_MS, NOTE_MS, OUTBOX_KEY, OUTBOX_TTL_MS, pendingItems } from "./outbox.js";

// An outbox over a scripted `turn`: each call takes the next outcome (a reply
// object, a Promise/deferred, or an error to throw) and records its text and key.
function harness(outcomes, { t0 = 1_000_000, saved = null, items = () => [] } = {}) {
  let clock = t0;
  const calls = [];
  const asked = [];
  const saves = [];
  let n = 0;
  const http = {
    async turn(text, key) {
      calls.push({ text, key });
      const outcome = outcomes.shift();
      const value = typeof outcome === "function" ? await outcome() : await outcome;
      if (value instanceof Error) throw value;
      return value;
    },
  };
  const outbox = createOutbox({
    http,
    newKey: () => `k${++n}`,
    now: () => clock,
    items,
    saved,
    save: (v) => {
      saves.push(v);
    },
    refresh: async () => {
      asked.push(clock);
    },
  });
  return {
    outbox,
    calls,
    asked,
    saves,
    advance: (ms) => {
      clock += ms;
    },
    clock: () => clock,
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
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
  assert.deepEqual(
    pendingItems(h.outbox.getState().entries).map((i) => [i.kind, i.text, i.pending]),
    [["shepherd", "run the tests", true]],
  );
  assert.deepEqual(await sent, { restore: null });
  assert.equal(h.outbox.getState().busy, false);
  assert.deepEqual(
    pendingItems(h.outbox.getState().entries).map((i) => [i.kind, i.text, Boolean(i.pending)]),
    [
      ["shepherd", "run the tests", false],
      ["dog", "Done.", false],
    ],
  );
  assert.equal(h.asked.length, 1, "it asked for the conversation");
  assert.ok(
    pendingItems(h.outbox.getState().entries).every((i, k, all) => k === 0 || i.id > all[k - 1].id),
    "the reply sorts after the line",
  );
  assert.equal(h.saves.at(-1), null, "completed turns clear persisted outbox state");
});

test("the conversation's items replace the pending ones by id, so nothing shows twice", async () => {
  const h = harness([reply("Done.")]);
  await h.outbox.submit("run the tests");
  const at = h.outbox.getState().entries[0].at;
  h.outbox.reconcile([line(at + 5, "run the tests")]);
  assert.deepEqual(
    pendingItems(h.outbox.getState().entries).map((i) => i.kind),
    ["dog"],
  );
  assert.equal(h.outbox.getState().entries[0].userId, at + 5);
  h.outbox.reconcile([line(at + 5, "run the tests"), answer(at + 9, "Done.")]);
  assert.deepEqual(h.outbox.getState().entries, []);
});

test("an older identical line is not claimed, and two identical sends claim one each", async () => {
  const h = harness([reply("one"), reply("two")]);
  await h.outbox.submit("again");
  h.advance(10);
  await h.outbox.submit("again");
  const [a, b] = h.outbox.getState().entries;
  const old = line(a.at - 500 * 1000 * 1000, "again");
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
  assert.deepEqual(
    pendingItems(h.outbox.getState().entries).map((i) => i.kind),
    ["shepherd"],
  );
  h.outbox.reconcile([line(h.outbox.getState().entries[0].at, "stop")]);
  assert.deepEqual(h.outbox.getState().entries, []);
});

test("failed sends stay retryable inline: unknown outcomes reuse the key and HTTP refusals mint a new key", async () => {
  const h = harness([
    new DaemonError("down", "nothing answered"),
    reply("ok"),
    new DaemonError("http", "the orchestrator is off (no key)", 503),
    reply("ok"),
  ]);
  // A failed send (whether typed or a tapped suggested reply) stays in the outbox as "failed"
  // so it can be retried, edited, or removed without losing the words.
  assert.deepEqual(await h.outbox.submit("same words"), { restore: null, failed: true });
  const failed = h.outbox.getState().entries[0];
  assert.equal(failed.phase, "failed");
  assert.equal(failed.reuseKey, true);
  assert.deepEqual(pendingItems(h.outbox.getState().entries), [], "failed entries render in the composer, not as timeline bubbles");

  assert.deepEqual(await h.outbox.retry(failed.key), { restore: null });
  assert.equal(h.calls[0].key, h.calls[1].key, "unknown outcome reuses the Idempotency-Key");

  assert.deepEqual(await h.outbox.submit("same words"), { restore: null, failed: true }, "503");
  const refused = h.outbox.getState().entries.find((e) => e.phase === "failed");
  assert.equal(refused.reuseKey, false);
  await h.outbox.retry(refused.key);
  assert.notEqual(h.calls[2].key, h.calls[3].key, "an HTTP refusal mints a new Idempotency-Key");
  assert.notEqual(h.calls[1].key, h.calls[2].key, "a sent line does not leak its key");
});

test("editing a failed send moves its text back, preserving the key only if submitted unchanged", async () => {
  const h = harness([new DaemonError("down", "nothing answered"), reply("ok"), new DaemonError("down", "nothing answered"), reply("ok")]);
  await h.outbox.submit("first");
  const firstKey = h.outbox.getState().entries[0].key;
  assert.equal(h.outbox.take(firstKey), "first");
  assert.deepEqual(h.outbox.getState().entries, []);
  assert.equal(h.outbox.getState().notice, null);
  await h.outbox.submit("second");
  assert.notEqual(h.calls[0].key, h.calls[1].key, "edited words get a new key");

  await h.outbox.submit("unchanged");
  const failed = h.outbox.getState().entries.find((e) => e.phase === "failed");
  assert.equal(h.outbox.take(failed.key), "unchanged");
  await h.outbox.submit("unchanged");
  assert.equal(h.calls[2].key, h.calls[3].key, "submitting the exact taken words reuses the unknown-outcome key");
});

test("503 keeps the failed entry with the daemon's reason and clears the error when removed", async () => {
  const h = harness([new DaemonError("http", "the orchestrator is off (no brain key)", 503)]);
  assert.deepEqual(await h.outbox.submit("hello"), { restore: null, failed: true });
  const s = h.outbox.getState();
  assert.equal(s.entries.length, 1);
  assert.equal(s.entries[0].phase, "failed");
  assert.equal(s.busy, false);
  assert.deepEqual([s.notice.tone, s.notice.text], ["error", "the orchestrator is off (no brain key)"]);
  h.outbox.remove(s.entries[0].key);
  assert.deepEqual(h.outbox.getState().entries, []);
  assert.equal(h.outbox.getState().notice, null);
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

test("an idempotency 409 is a failure to retry with a fresh key, not an answer by voice", async () => {
  const h = harness([
    new DaemonError("http", "the first request with this Idempotency-Key did not keep its reply; send a new key", 409),
    reply("ok"),
  ]);
  assert.deepEqual(await h.outbox.submit("hello"), { restore: null, failed: true });
  assert.equal(h.outbox.getState().notice.tone, "error");
  const failed = h.outbox.getState().entries[0];
  assert.equal(failed.reuseKey, false);
  await h.outbox.retry(failed.key);
  assert.notEqual(h.calls[0].key, h.calls[1].key);
});

test("follow-up sends while busy queue in FIFO order and drain as turns complete", async () => {
  const firstTurn = deferred();
  const secondTurn = deferred();
  const h = harness([() => firstTurn.promise, () => secondTurn.promise, reply("c")]);

  const first = h.outbox.submit("one");
  assert.deepEqual(await h.outbox.submit("two"), { restore: null, queued: true });
  assert.deepEqual(await h.outbox.submit("three"), { restore: null, queued: true });
  assert.deepEqual(
    h.outbox.getState().entries.map((e) => [e.text, e.phase]),
    [
      ["one", "sending"],
      ["two", "queued"],
      ["three", "queued"],
    ],
  );
  assert.deepEqual(
    pendingItems(h.outbox.getState().entries).map((i) => i.text),
    ["one"],
    "queued entries wait in the composer, not as timeline bubbles",
  );
  assert.equal(h.calls.length, 1, "only one turn runs at a time");

  firstTurn.resolve(reply("a"));
  await first;
  assert.deepEqual(
    h.calls.map((c) => c.text),
    ["one", "two"],
    "the second line starts as soon as the first turn completes",
  );

  secondTurn.resolve(reply("b"));
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(
    h.calls.map((c) => c.text),
    ["one", "two", "three"],
  );
});

test("a failed turn pauses queued follow-ups so they never run out of order", async () => {
  const firstTurn = deferred();
  const h = harness([() => firstTurn.promise, reply("fixed"), reply("committed")]);

  const first = h.outbox.submit("fix the bug");
  await h.outbox.submit("commit the fix");
  firstTurn.reject(new DaemonError("down", "nothing answered"));
  await first;

  assert.equal(h.calls.length, 1, "queued follow-up did NOT fire after failure");
  assert.deepEqual(
    h.outbox.getState().entries.map((e) => [e.text, e.phase]),
    [
      ["fix the bug", "failed"],
      ["commit the fix", "queued"],
    ],
  );

  // Taking the failed item to edit keeps the queued follow-up paused until the edited line sends.
  const failedKey = h.outbox.getState().entries[0].key;
  assert.equal(h.outbox.take(failedKey), "fix the bug");
  assert.equal(h.calls.length, 1, "taking for edit does not prematurely fire the queued follow-up");

  await h.outbox.submit("fix the bug properly");
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(
    h.calls.map((c) => c.text),
    ["fix the bug", "fix the bug properly", "commit the fix"],
  );
});

test("persisted sending entry replays with the saved Idempotency-Key on reload, unless already in conversation", async () => {
  assert.equal(OUTBOX_KEY, "sheepdog.outbox");
  const firstTurn = deferred();
  const h1 = harness([() => firstTurn.promise]);
  void h1.outbox.submit("run the tests");
  await h1.outbox.submit("then lint");
  const saved = h1.saves.at(-1);
  assert.equal(saved.entries.length, 2);
  assert.equal(saved.entries[0].phase, "sending");
  assert.equal(saved.entries[1].phase, "queued");

  // Reload 1: conversation does not have the reply yet -> resume() replays with the exact saved key, then drains queued.
  const h2 = harness([reply("All green."), reply("Lint clean.")], { t0: h1.clock() + 2_000, saved });
  assert.equal(h2.outbox.getState().busy, true);
  await h2.outbox.resume();
  await new Promise((r) => setImmediate(r));
  assert.equal(h2.calls[0].key, saved.entries[0].key, "reused saved Idempotency-Key across reload");
  assert.deepEqual(
    h2.calls.map((c) => c.text),
    ["run the tests", "then lint"],
  );

  // Reload 2: conversation ALREADY recorded both the user line and reply while reloading -> skip re-POSTing first turn!
  const at = saved.entries[0].at;
  const history = [line(at + 5, "run the tests"), answer(at + 9, "All green.")];
  const h3 = harness([reply("Lint clean.")], { t0: h1.clock() + 2_000, saved, items: () => history });
  h3.outbox.reconcile(history);
  await h3.outbox.resume();
  assert.deepEqual(
    h3.calls.map((c) => c.text),
    ["then lint"],
    "completed turn was reconciled without re-POSTing, then queued follow-up drained",
  );
});

test("persisted sending entries past IDEM_TTL_MS become retryable failures instead of auto-replaying, and OUTBOX_TTL_MS expires", async () => {
  const saved = {
    entries: [
      { key: "k-old-send", text: "deploy", at: 1_000_000 * 1000, phase: "sending", error: null, reuseKey: true },
      { key: "k-queued", text: "announce", at: 1_000_100 * 1000, phase: "queued", error: null, reuseKey: true },
    ],
  };
  const h = harness([reply("deployed")], { t0: 1_000_000 + IDEM_TTL_MS + 1_000, saved });
  assert.equal(h.outbox.getState().busy, false);
  await h.outbox.resume();
  assert.equal(h.calls.length, 0, "stale sending entry did not auto-replay or drain queued items");
  assert.deepEqual(
    h.outbox.getState().entries.map((e) => [e.phase, e.reuseKey]),
    [
      ["failed", false],
      ["queued", true],
    ],
  );

  const expired = harness([], { t0: 1_000_100 + OUTBOX_TTL_MS + 1, saved });
  assert.deepEqual(expired.outbox.getState().entries, []);
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

test("a reply the stream showed before the turn returned is not shown twice", async () => {
  const history = [];
  let outbox;
  const http = {
    async turn() {
      const at = outbox.getState().entries[0].at;
      history.push(line(at + 5, "run the tests"), answer(at + 9, "Done."));
      outbox.reconcile(history);
      return reply("Done.");
    },
  };
  outbox = createOutbox({ http, newKey: () => "k1", now: () => 1_000_000, items: () => history, refresh: async () => {} });
  await outbox.submit("run the tests");
  assert.deepEqual(pendingItems(outbox.getState().entries), []);
  assert.deepEqual(outbox.getState().entries, []);
});

test("a failed turn whose exchange already appeared in the stream is reconciled instead of failing", async () => {
  const history = [];
  let outbox;
  const http = {
    async turn() {
      const at = outbox.getState().entries[0].at;
      history.push(line(at + 1, "hello"), answer(at + 2, "Hi there."));
      outbox.reconcile(history);
      throw new DaemonError("down", "socket closed after daemon logged reply");
    },
  };
  outbox = createOutbox({ http, newKey: () => "k1", now: () => 1_000_000, items: () => history });
  assert.deepEqual(await outbox.submit("hello"), { restore: null });
  assert.deepEqual(outbox.getState().entries, []);
});
