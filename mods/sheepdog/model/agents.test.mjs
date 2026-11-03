// Agent details use the same state-table fields as the phone.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { agentDetails, attentionCounts, attentionLine } from "./agents.js";

const fixture = JSON.parse(readFileSync(new URL("../../../ios/Tests/Fixtures/agent-attention.json", import.meta.url), "utf8"));

test("an agent name resolves to what it works on, its result, question and space", () => {
  const detail = agentDetails(
    [{
      pane_id: "w1:p1",
      alias: "auth",
      spoken: "the auth agent",
      kind: "claude",
      state: "blocked",
      current_task: "Fix sign-in",
      workspace_label: "shop",
      last_result: { spoken: "The tests pass.", detail: "Two files changed.", at: "2026-10-30T12:00:00Z" },
      last_block: { question: "Run the migration?", answer: "Yes", answered: true },
    }],
    "The Auth Agent",
  );
  assert.equal(detail.paneID, "w1:p1");
  assert.equal(detail.worksOn, "Fix sign-in");
  assert.equal(detail.lastResult, "The tests pass.");
  assert.equal(detail.lastResultDetail, "Two files changed.");
  assert.equal(detail.lastQuestion, "Run the migration?");
  assert.equal(detail.lastAnswer, "Yes");
  assert.equal(detail.space, "shop");
});

test("an agent that has gone is not invented from its old timeline name", () => {
  assert.equal(agentDetails([], "auth"), null);
  assert.equal(agentDetails([{ alias: "auth" }], ""), null);
});

const none = { needsYou: 0, working: 0, done: 0, notRunning: 0 };
const KEY = { needs_you: "needsYou", working: "working", done: "done", not_running: "notRunning" };

test("attention counts follow the daemon's group on every shared fixture case", () => {
  for (const c of fixture.classify) {
    const counts = attentionCounts({ agents: [{ state: c.state, attention: { group: c.group, unread: c.unread, pinned: c.pinned } }] });
    assert.deepEqual(counts, { ...none, [KEY[c.group]]: 1 }, c.name);
  }
});

test("without attention (an older daemon) the state decides, and agrees with the fixture", () => {
  for (const c of fixture.classify.filter((x) => !x.marks.error)) {
    assert.deepEqual(attentionCounts({ agents: [{ state: c.state }] }), { ...none, [KEY[c.group]]: 1 }, c.name);
  }
  // A group the client does not know falls back to the state too.
  assert.deepEqual(attentionCounts({ agents: [{ state: "blocked", attention: { group: "later" } }] }), { ...none, needsYou: 1 });
});

test("attention counts tolerate a state with no agents", () => {
  for (const state of [null, undefined, {}, { agents: null }, { agents: [] }]) assert.deepEqual(attentionCounts(state), none);
});

test("the status line lists the groups that have agents, needs you first, and leaves out agents not running", () => {
  assert.equal(attentionLine({ needsYou: 1, working: 2, done: 1, notRunning: 3 }), "1 needs you · 2 working · 1 done");
  assert.equal(attentionLine({ needsYou: 2, working: 0, done: 0, notRunning: 0 }), "2 need you");
  assert.equal(attentionLine({ ...none, working: 1, done: 4 }), "1 working · 4 done");
  assert.equal(attentionLine({ ...none, notRunning: 2 }), "");
  assert.equal(attentionLine(none), "");
});
