// The orchestrator's replies from the stream, and which suggested replies are
// live (the rule of SuggestedReplies, ios/Sources/SheepdogKit): `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { CAPACITY, foldReply, liveSuggestions } from "./replies.js";

const result = (payload) => ({ type: "voice.tool_result", payload: { name: "to_supervisor", spoken: "Shall I?", ...payload } });
const fold = (replies, id, payload) => foldReply(replies, result(payload), id);
const item = (id, kind, extra = {}) => ({ id, kind, text: "t", ...extra });
const live = (items, replies) => liveSuggestions(items, replies)?.replyId ?? null;

test("a to_supervisor result is a reply with its options", () => {
  const replies = fold([], 10, { options: ["Yes, start it", " Not yet ", "", 3] });
  assert.deepEqual(replies, [{ id: 10, options: ["Yes, start it", "Not yet"], pending: false }]);
});

test("other events, other tools, cancelled and empty results are not replies", () => {
  assert.deepEqual(foldReply([], { type: "voice.push", payload: {} }, 1), []);
  assert.deepEqual(fold([], 1, { name: "stop" }), []);
  assert.deepEqual(fold([], 1, { cancelled: true }), []);
  assert.deepEqual(fold([], 1, { spoken: "  " }), []);
  assert.equal(fold([], 1, { spoken: "", pending: true }).length, 1, "a pending reply has no words yet");
  assert.deepEqual(fold([], 0, {}), []);
});

test("the same id is one reply: the last copy wins, an identical one changes nothing", () => {
  const pending = fold([], 10, { spoken: "", pending: true });
  const final = fold(pending, 10, { options: ["Yes"] });
  assert.deepEqual(final, [{ id: 10, options: ["Yes"], pending: false }]);
  assert.equal(fold(final, 10, { options: ["Yes"] }), final);
});

test("replies stay in id order and the newest CAPACITY are kept", () => {
  let replies = fold(fold([], 30, {}), 20, {});
  assert.deepEqual(replies.map((r) => r.id), [20, 30]);
  for (let id = 100; id < 100 + CAPACITY + 5; id++) replies = fold(replies, id, {});
  assert.equal(replies.length, CAPACITY);
  assert.equal(replies[replies.length - 1].id, 100 + CAPACITY + 4);
});

test("the newest reply with options is live", () => {
  const replies = fold([], 10, { options: ["Yes", "No"] });
  assert.deepEqual(liveSuggestions([item(5, "shepherd")], replies), { replyId: 10, options: ["Yes", "No"] });
});

test("a reply without options, or a pending one, is not", () => {
  assert.equal(live([], fold([], 10, {})), null);
  assert.equal(live([], fold([], 10, { spoken: "", pending: true, options: ["Yes"] })), null);
});

test("the user's words end it, said, typed or a pending line of the panel", () => {
  const replies = fold([], 10, { options: ["Yes"] });
  assert.equal(live([item(11, "shepherd", { via: "voice" })], replies), null);
  assert.equal(live([item(11, "shepherd", { via: "typed" })], replies), null);
  assert.equal(live([item(11, "shepherd", { via: "typed", pending: true })], replies), null);
  assert.equal(live([item(9, "shepherd")], replies), 10, "words from before the reply do not");
});

test("a newer reply ends it, with options or without (a typed turn's reply has none)", () => {
  let replies = fold([], 10, { options: ["Yes"] });
  assert.equal(live([], fold(replies, 20, {})), null);
  replies = fold(replies, 20, { options: ["Later"] });
  assert.deepEqual(liveSuggestions([], replies), { replyId: 20, options: ["Later"] }, "only the newest is live");
});

test("a new conversation, a restart or the voice losing its context ends it", () => {
  const replies = fold([], 10, { options: ["Yes"] });
  for (const subkind of ["reset", "start", "context_lost", "fresh"]) {
    assert.equal(live([item(11, "notice", { subkind })], replies), null, subkind);
  }
  assert.equal(live([item(11, "notice", { subkind: "resumed" })], replies), 10, "a reconnect that continues does not");
  assert.equal(live([item(11, "notice", { text: "Voice session closed (shutdown)" })], replies), 10, "nor does a closed session");
});

test("the voice saying the reply, agent updates and questions do not end it", () => {
  const replies = fold([], 10, { options: ["Yes"] });
  const items = [item(11, "dog"), item(12, "push", { urgency: "idle" }), item(13, "approval", { resolution: "open" }), item(14, "card")];
  assert.equal(live(items, replies), 10);
});

test("items and replies are compared by id wherever they come from", () => {
  const replies = fold(fold([], 30, { options: ["B"] }), 10, { options: ["A"] });
  assert.equal(live([item(20, "shepherd")], replies), 30, "the user spoke between the two");
  assert.equal(live([item(40, "shepherd")], replies), null);
});
