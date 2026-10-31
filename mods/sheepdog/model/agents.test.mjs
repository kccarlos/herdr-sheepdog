// Agent details use the same state-table fields as the phone.
import { test } from "node:test";
import assert from "node:assert/strict";
import { agentDetails } from "./agents.js";

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
