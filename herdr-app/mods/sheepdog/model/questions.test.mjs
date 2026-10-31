// The open questions and what a tap on an option does: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { DaemonError } from "../net/http.js";
import { createAnswers, GONE, labelOf, LINGER_MS, openQuestions, summaryOf } from "./questions.js";

const option = (index, label, extra = {}) => ({ index, label, keys: [String(index)], default: index === 1, ...extra });
const question = (id, extra = {}) => ({ id, kind: "answer_prompt", agent: "Codex", summary: "Allow it?", options: [option(1, "Yes"), option(2, "No")], ...extra });

function harness(outcomes) {
  let clock = 1000;
  const calls = [];
  let refreshed = 0;
  let n = 0;
  const http = {
    async answer(id, index, key) {
      calls.push({ id, index, key });
      const outcome = outcomes.shift();
      if (outcome instanceof Error) throw outcome;
      return outcome;
    },
  };
  const answers = createAnswers({ http, newKey: () => `k${++n}`, now: () => clock, refresh: async () => { refreshed++; } });
  return { answers, calls, advance: (ms) => { clock += ms; }, refreshed: () => refreshed };
}

test("the open questions are the state's, oldest first; without any, the voice's own", () => {
  const a = question("a");
  const b = question("b");
  assert.deepEqual(openQuestions({ questions: [a, b], pending_action: b }).map((q) => q.id), ["a", "b"]);
  assert.deepEqual(openQuestions({ questions: [], pending_action: b }).map((q) => q.id), ["b"]);
  assert.deepEqual(openQuestions({ questions: [], pending_action: null }), []);
  assert.deepEqual(openQuestions(null), []);
});

test("only a worker's prompt with options is a card", () => {
  const state = { questions: [question("a"), question("b", { kind: "send_instruction" }), question("c", { options: [] }), question("d", { options: undefined })] };
  assert.deepEqual(openQuestions(state).map((q) => q.id), ["a"]);
});

test("its words: the summary, else the spoken summary, else the question", () => {
  assert.equal(summaryOf({ summary: " S ", spoken_summary: "x", question: "y" }), "S");
  assert.equal(summaryOf({ summary: "", spoken_summary: "Spoken", question: "y" }), "Spoken");
  assert.equal(summaryOf({ question: "Q" }), "Q");
  assert.equal(summaryOf({}), "An agent is waiting for your answer.");
  assert.equal(labelOf({ label: "Yes (Recommended)", title: "Yes" }), "Yes");
  assert.equal(labelOf({ label: "No" }), "No");
});

test("a tap posts the option's index and the question's id, with a key, and the card says what was chosen", async () => {
  const h = harness([{ ok: true, spoken: "Sent." }]);
  const q = question("a");
  await h.answers.answer(q, q.options[1]);
  assert.deepEqual(h.calls, [{ id: "a", index: 2, key: "k1" }]);
  const [card] = h.answers.cards({ questions: [q] });
  assert.deepEqual([card.phase, card.outcome, card.spoken], ["answered", "You chose No", "Sent."]);
  assert.equal(h.refreshed(), 1, "the state is asked again");
});

test("while a tap is in flight nothing else sends", async () => {
  const h = harness([{ ok: true }]);
  const [a, b] = [question("a"), question("b")];
  const first = h.answers.answer(a, a.options[0]);
  assert.equal(h.answers.cards({ questions: [a, b] })[0].phase, "sending");
  await h.answers.answer(b, b.options[0]);
  await first;
  assert.deepEqual(h.calls.map((c) => c.id), ["a"]);
});

test("an answered question does not send again", async () => {
  const h = harness([{ ok: true }, { ok: true }]);
  const q = question("a");
  await h.answers.answer(q, q.options[0]);
  await h.answers.answer(q, q.options[1]);
  assert.equal(h.calls.length, 1);
});

test("a stale question is gone (409), the state is asked again and the card says so", async () => {
  const h = harness([new DaemonError("http", "that question is no longer open", 409)]);
  const q = question("a");
  await h.answers.answer(q, q.options[0]);
  const [card] = h.answers.cards({ questions: [] });
  assert.deepEqual([card.phase, card.message], ["gone", GONE]);
  assert.equal(h.refreshed(), 1);
});

test("an error leaves the card open with the reason; nothing answering says to try again", async () => {
  const h = harness([new DaemonError("http", "could not send the keys", 502), new DaemonError("down", "nothing answered"), { ok: true }]);
  const q = question("a");
  await h.answers.answer(q, q.options[0]);
  assert.deepEqual([h.answers.cards({ questions: [q] })[0].phase, h.answers.cards({ questions: [q] })[0].error], ["open", "could not send the keys"]);
  await h.answers.answer(q, q.options[0]);
  assert.match(h.answers.cards({ questions: [q] })[0].error, /did not answer/);
  await h.answers.answer(q, q.options[0]);
  assert.equal(h.answers.cards({ questions: [q] })[0].phase, "answered", "open again, so a retry works");
  assert.equal(new Set(h.calls.map((c) => c.key)).size, 3, "every tap has its own key");
});

test("a settled card stays a moment after the daemon stops listing it, then goes", async () => {
  const h = harness([{ ok: true }]);
  const q = question("a");
  await h.answers.answer(q, q.options[0]);
  const gone = { questions: [] };
  assert.deepEqual(h.answers.cards(gone).map((c) => c.phase), ["answered"]);
  h.advance(LINGER_MS - 1);
  h.answers.prune(gone);
  assert.equal(h.answers.cards(gone).length, 1);
  h.advance(2);
  h.answers.prune(gone);
  assert.deepEqual(h.answers.cards(gone), []);
});

test("the settled card is in front of the questions that are still open", async () => {
  const h = harness([{ ok: true }]);
  const [a, b] = [question("a"), question("b")];
  await h.answers.answer(a, a.options[0]);
  assert.deepEqual(h.answers.cards({ questions: [b] }).map((c) => [c.pending.id, c.phase]), [["a", "answered"], ["b", "open"]]);
});

test("a phase for a question that left while open is forgotten", async () => {
  const h = harness([new DaemonError("http", "x", 502)]);
  const q = question("a");
  await h.answers.answer(q, q.options[0]);
  h.answers.prune({ questions: [] });
  assert.deepEqual(h.answers.getState().phases, {});
});
