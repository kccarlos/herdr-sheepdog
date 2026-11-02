// The words shown before the daemon's page has them: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { foldFragment, noRuns } from "./fragments.js";
import { eventEffect } from "./events.js";

const ev = (type, payload = {}) => ({ type, ts: "t", payload });

test("fragments of one utterance grow one item with the id of the first", () => {
  let r = foldFragment(noRuns(), ev("voice.transcript.in", { text: "hello " }), 10);
  assert.deepEqual(r.item, { id: 10, ts: "t", kind: "shepherd", text: "hello", via: "voice" });
  r = foldFragment(r.runs, ev("voice.transcript.in", { text: "there", final: true }), 11);
  assert.equal(r.item.id, 10);
  assert.equal(r.item.text, "hello there");
  // Final closes the run: the next words are a new item.
  r = foldFragment(r.runs, ev("voice.transcript.in", { text: "again" }), 12);
  assert.equal(r.item.id, 12);
});

test("the voice's words and the user's words end each other's run", () => {
  let r = foldFragment(noRuns(), ev("voice.transcript.in", { text: "a" }), 1);
  r = foldFragment(r.runs, ev("voice.transcript.out", { text: "b" }), 2);
  assert.equal(r.runs.user, null);
  r = foldFragment(r.runs, ev("voice.transcript.in", { text: "c" }), 3);
  assert.equal(r.item.id, 3);
  assert.equal(r.item.text, "c");
});

test("a closing event ends the run; an interruption ends only the voice's", () => {
  let r = foldFragment(noRuns(), ev("voice.transcript.in", { text: "a" }), 1);
  r = foldFragment(r.runs, ev("voice.tool_call"), 2);
  assert.deepEqual(r.runs, noRuns());
  r = foldFragment(noRuns(), ev("voice.transcript.out", { text: "x" }), 3);
  r = foldFragment(r.runs, ev("voice.interrupted"), 4);
  assert.equal(r.runs.dog, null);
});

test("whole-text events: a typed line, a push; not a spoken utterance, a handoff or a silent push", () => {
  assert.equal(foldFragment(noRuns(), ev("voice.text", { text: " hi " }), 5).item.text, "hi");
  assert.equal(foldFragment(noRuns(), ev("web.utterance", { text: "hi", via: "typed" }), 5).item.via, "typed");
  assert.equal(foldFragment(noRuns(), ev("web.utterance", { text: "hi", via: "voice" }), 5).item, null);
  const push = foldFragment(noRuns(), ev("voice.push", { text: "done", urgency: "info", agent: "api" }), 6).item;
  assert.deepEqual(push, { id: 6, ts: "t", kind: "push", text: "done", urgency: "info", agent: "api" });
  assert.equal(foldFragment(noRuns(), ev("voice.push", { text: "x", handoff: true }), 7).item, null);
  assert.equal(foldFragment(noRuns(), ev("voice.push", { text: "x", urgency: "silent" }), 7).item, null);
  assert.equal(foldFragment(noRuns(), ev("voice.text", { text: "  " }), 8).item, null);
});

test("an event with nothing to show leaves the runs as they were", () => {
  const runs = foldFragment(noRuns(), ev("voice.transcript.in", { text: "a" }), 1).runs;
  assert.equal(foldFragment(runs, ev("web.client.stats"), 2).runs, runs);
});

test("every event that shows words at once is one that settles, not one that refetches at once", () => {
  for (const type of ["voice.transcript.in", "voice.transcript.out", "voice.text", "web.utterance", "voice.push"]) {
    assert.equal(eventEffect(type), "settle", type);
  }
});
