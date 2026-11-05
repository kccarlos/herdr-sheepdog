// The docked panel's keys: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { keyAction, leaveCommand } from "./keys.js";

const question = { phase: "open", pending: { id: "q1", options: [{ index: 0, label: "Yes" }, { index: 1, label: "No" }] } };
const suggestions = { options: ["first", "second"] };

test("Enter on the panel writes a message", () => {
  assert.deepEqual(keyAction("enter", { onPanel: true }), { type: "composer" });
});

test("Escape leaves from the panel and returns to it from the composer", () => {
  assert.deepEqual(keyAction("escape", { onPanel: true }), { type: "leave" });
  assert.deepEqual(keyAction("escape", { onPanel: false }), { type: "panel" });
});

test("digits pick an option of the open question, else a suggested reply", () => {
  assert.deepEqual(keyAction("2", { onPanel: true, question, suggestions }), { type: "answer", question: question.pending, option: question.pending.options[1] });
  assert.deepEqual(keyAction("2", { onPanel: true, suggestions }), { type: "send", text: "second" });
  assert.equal(keyAction("3", { onPanel: true, question, suggestions }), null, "no such option");
  assert.deepEqual(keyAction("1", { onPanel: true, suggestions }), { type: "send", text: "first" });
  assert.equal(keyAction("1", { onPanel: true, suggestions, busy: true }), null, "not while an answer is on its way");
});

test("Cmd, Ctrl and Alt keep their own meaning", () => {
  assert.equal(keyAction("1", { onPanel: true, question, modified: true }), null);
  assert.equal(keyAction("enter", { onPanel: true, modified: true }), null);
  assert.equal(keyAction("escape", { onPanel: true, modified: true }), null);
});

test("Escape closes an open sheet first, then the panel's window", () => {
  assert.deepEqual(keyAction("escape", { onPanel: true, sheet: true, window: true }), { type: "sheet" });
  assert.deepEqual(keyAction("escape", { onPanel: false, sheet: true }), { type: "sheet" });
  assert.deepEqual(keyAction("escape", { onPanel: true, window: true }), { type: "close" });
});

test("digits and Enter do nothing while the panel is not showing the conversation", () => {
  assert.equal(keyAction("1", { onPanel: true, question, suggestions, active: false }), null);
  assert.equal(keyAction("enter", { onPanel: true, active: false }), null);
});

test("in the composer digits and Enter are text, and 0 or letters mean nothing", () => {
  assert.equal(keyAction("1", { onPanel: false, question, suggestions }), null);
  assert.equal(keyAction("enter", { onPanel: false }), null);
  assert.equal(keyAction("0", { onPanel: true, suggestions }), null);
  assert.equal(keyAction("a", { onPanel: true, suggestions }), null);
});

test("? on the panel opens or closes keyboard shortcuts, but stays text in the composer", () => {
  assert.deepEqual(keyAction("?", { onPanel: true }), { type: "shortcuts" });
  assert.deepEqual(keyAction("?", { onPanel: true, active: false }), { type: "shortcuts" });
  assert.deepEqual(keyAction("?", { onPanel: true, sheet: true }), { type: "shortcuts" });
  assert.equal(keyAction("?", { onPanel: false }), null, "typing ? in the composer types ?");
  assert.equal(keyAction("?", { onPanel: false, sheet: "questions" }), null, "typing ? in composer with questions sheet open still types ?");
  assert.equal(keyAction("?", { onPanel: true, modified: true }), null);
});

test("shortcuts and card sheets block blind answers and composer focus; ? closes shortcuts even without panel focus", () => {
  assert.equal(keyAction("1", { onPanel: true, question, suggestions, sheet: "shortcuts" }), null);
  assert.equal(keyAction("enter", { onPanel: true, sheet: "shortcuts" }), null);
  assert.deepEqual(keyAction("?", { onPanel: false, sheet: "shortcuts" }), { type: "shortcuts" });
  assert.deepEqual(keyAction("escape", { onPanel: false, sheet: "shortcuts" }), { type: "sheet" });

  assert.equal(keyAction("1", { onPanel: true, question, suggestions, sheet: "card" }), null);
  assert.equal(keyAction("enter", { onPanel: true, sheet: "card" }), null);

  assert.deepEqual(
    keyAction("1", { onPanel: true, question, suggestions, sheet: "questions" }),
    { type: "answer", question: question.pending, option: question.pending.options[0] },
  );
  assert.equal(keyAction("1", { onPanel: true, suggestions, sheet: "questions" }), null, "questions sheet does not send hidden suggested replies");
  assert.equal(keyAction("enter", { onPanel: true, sheet: "questions" }), null);
});

test("a compact question bar whose options are hidden never allows blind digit answers", () => {
  assert.equal(keyAction("1", { onPanel: true, question, questionVisible: false, suggestions }), null);
});

test("leaving focuses the pane that had the keys, whether the panel was entered by pane.go or a click", () => {
  assert.deepEqual(leaveCommand({ pane: "p1", terminal: "t1" }), { name: "pane.focus", args: { pane: "p1" } });
  assert.deepEqual(leaveCommand({ pane: null, terminal: "t1" }), { name: "pane.focus", args: { terminal: "t1" } });
  assert.deepEqual(leaveCommand(null), { name: "pane.go", args: { direction: "left" } });
  assert.deepEqual(leaveCommand({ pane: null, terminal: null }), { name: "pane.go", args: { direction: "left" } });
});
