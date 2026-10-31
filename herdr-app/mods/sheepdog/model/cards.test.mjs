// The orchestrator's cards and the agent they open in herdr-app:
// `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { agentName, cardOf, displayTitle, focusCommand, focusTarget, kindWord } from "./cards.js";

test("a card item opens its card; a card the daemon no longer stores opens nothing", () => {
  const card = { kind: "card", title: "Tests" };
  assert.equal(cardOf({ kind: "card", card }), card);
  assert.equal(cardOf({ kind: "card", text: "no longer stored" }), null);
  assert.equal(cardOf({ kind: "dog", card }), null);
  assert.equal(cardOf(undefined), null);
});

test("the title is the card's own, else a plain one for its kind", () => {
  assert.equal(displayTitle({ kind: "card", title: " Tests " }), "Tests");
  assert.equal(displayTitle({ kind: "card" }), "Card");
  assert.equal(displayTitle({ kind: "agent", agent: "Codex" }), "Codex's window");
  assert.equal(displayTitle({ kind: "excerpt", spoken: "Codex" }), "Codex's screen");
  assert.equal(displayTitle({ kind: "excerpt" }), "Agent screen");
  assert.equal(displayTitle({ kind: "approval", agent: "Codex" }), "Codex is asking");
  assert.equal(displayTitle({ kind: "approval" }), "An agent is asking");
  assert.equal(displayTitle({ kind: "future" }), "Card");
});

test("the agent's name is as the voice says it, else its alias", () => {
  assert.equal(agentName({ spoken: "the docs agent", agent: "docs" }), "the docs agent");
  assert.equal(agentName({ spoken: " ", agent: "docs" }), "docs");
  assert.equal(agentName({}), null);
  assert.equal(kindWord({ kind: "agent" }), "Agent window");
  assert.equal(kindWord({ kind: "excerpt" }), "Screen");
  assert.equal(kindWord({ kind: "approval" }), "Question");
  assert.equal(kindWord({ kind: "card" }), "Card");
});

test("the agent opens through herdr-app's agent.focus with the card's pane_id as the terminal", () => {
  assert.equal(focusTarget({ pane_id: " t7 " }), "t7");
  assert.deepEqual(focusCommand({ pane_id: "t7" }), { name: "agent.focus", args: { terminal: "t7" } });
  assert.equal(focusCommand({ kind: "card" }), null);
  assert.equal(focusCommand({ pane_id: "  " }), null);
});
