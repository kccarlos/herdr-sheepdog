// The words shown before the daemon's page has them: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { foldApproval, foldFragment, foldQuestions, noRuns } from "./fragments.js";
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

// ---- the builder's approval and tool-result cases (internal/web/conversation_test.go)

const options = [{ index: 0, label: "Yes", keys: ["y"], default: true }, { index: 1, label: "No", keys: ["n"] }];
const created = (extra = {}) => ev("pending.created", { id: "ap-1", kind: "answer_prompt", agent: "auth", summary: "auth asks to run git diff.", question: "run git diff", options, ...extra });

test("pending.created of an answer_prompt is an open approval with the event's id", () => {
  assert.deepEqual(foldApproval(created(), 40), {
    item: { id: 40, ts: "t", kind: "approval", text: "auth asks to run git diff.", agent: "auth", pending_id: "ap-1", options, answered: false, resolution: "open" },
  });
  assert.equal(foldApproval(created({ summary: "", question: " run it " }), 41).item.text, "run it", "the question when there is no summary");
  assert.equal(foldApproval(created({ options: 2 }), 42).item.options, undefined, "older lines carry a count, not the options");
  assert.equal(foldApproval(created({ kind: "send_instruction" }), 43), null);
});

test("pending.confirmed answers it; expired, cancelled and replaced end only an open one", () => {
  const confirmed = foldApproval(ev("pending.confirmed", { id: "ap-1", answer: "Yes", via: "tap", keys: ["y"] }), 50);
  assert.deepEqual(confirmed, { pendingId: "ap-1", onlyOpen: false, patch: { answered: true, resolution: "confirmed", answer: "Yes", via: "tap" } });
  assert.deepEqual(foldApproval(ev("pending.expired", { id: "ap-2" }), 51), { pendingId: "ap-2", onlyOpen: true, patch: { resolution: "expired", reason: "expired" } });
  assert.deepEqual(foldApproval(ev("pending.cancelled", { id: "ap-3", reason: "prompt_gone" }), 52).patch, { resolution: "cancelled", reason: "prompt_gone" });
  assert.deepEqual(foldApproval(ev("pending.cancelled", { id: "ap-3" }), 52).patch, { resolution: "cancelled" }, "an old line has no reason");
  assert.deepEqual(foldApproval(ev("pending.replaced", { old_id: "ap-1", new_id: "ap-9" }), 53), { pendingId: "ap-1", onlyOpen: true, patch: { resolution: "replaced", reason: "replaced" } });
  assert.equal(foldApproval(ev("voice.text", {}), 54), null);
});

test("the question card follows: created lists it, confirmed and cancelled drop it, replaced keeps the one the voice moved past", () => {
  const app = { agents: [], questions: [], pending_action: null };
  const one = foldQuestions(app, created());
  assert.equal(one.questions.length, 1);
  assert.deepEqual(one.questions[0], { id: "ap-1", kind: "answer_prompt", summary: "auth asks to run git diff.", spoken_summary: "auth asks to run git diff.", question: "run git diff", options, agent: "auth" });
  assert.equal(one.pending_action.id, "ap-1");
  assert.deepEqual(foldQuestions(one, created()).questions.length, 1, "the same id is listed once");
  const two = foldQuestions(one, created({ id: "ap-2", agent: "docs" }));
  assert.deepEqual(two.questions.map((q) => q.id), ["ap-1", "ap-2"]);
  assert.equal(foldQuestions(two, ev("pending.replaced", { old_id: "ap-1", new_id: "ap-2" })), two, "ap-1 stays answerable on the app");
  const answered = foldQuestions(two, ev("pending.confirmed", { id: "ap-2", answer: "Yes" }));
  assert.deepEqual(answered.questions.map((q) => q.id), ["ap-1"]);
  assert.equal(answered.pending_action, null);
  assert.deepEqual(foldQuestions(answered, ev("pending.cancelled", { id: "ap-1", reason: "closed" })).questions, []);
  assert.equal(foldQuestions(answered, ev("pending.cancelled", { id: "unknown" })), answered);
  assert.equal(foldQuestions(app, created({ kind: "send_instruction" })), app, "only a question the user can answer");
  assert.equal(foldQuestions(null, created()), null, "nothing before the state is read");
});

test("a tool result spoken without a voice model is the voice's line; a typed one keeps its via", () => {
  const spoken = ev("voice.tool_result", { name: "to_supervisor", spoken: " Auth is running the tests. ", via: "text_router" });
  assert.deepEqual(foldFragment(noRuns(), spoken, 60).item, { id: 60, ts: "t", kind: "dog", text: "Auth is running the tests." });
  assert.deepEqual(foldFragment(noRuns(), ev("voice.tool_result", { spoken: "Done.", via: "typed" }), 61).item, { id: 61, ts: "t", kind: "dog", text: "Done.", via: "typed" });
  const after = foldFragment({ user: null, dog: { id: 1, ts: "t", text: "x" } }, spoken, 62);
  assert.equal(after.runs.dog, null, "it ends the voice's run");
  // With a voice model (no via, or voice), cancelled, or silent: the transcript says it.
  const runs = foldFragment(noRuns(), ev("voice.transcript.out", { text: "hi" }), 1).runs;
  for (const payload of [{ spoken: "Hi" }, { spoken: "Hi", via: "voice" }, { spoken: "Hi", via: "typed", cancelled: true }, { spoken: "  ", via: "typed" }]) {
    const r = foldFragment(runs, ev("voice.tool_result", payload), 70);
    assert.equal(r.item, null);
    assert.equal(r.runs, runs);
  }
});
