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
  assert.deepEqual(keyAction("1", { onPanel: true, question: { ...question, phase: "sending" }, suggestions }), { type: "send", text: "first" });
});

test("in the composer digits and Enter are text, and 0 or letters mean nothing", () => {
  assert.equal(keyAction("1", { onPanel: false, question, suggestions }), null);
  assert.equal(keyAction("enter", { onPanel: false }), null);
  assert.equal(keyAction("0", { onPanel: true, suggestions }), null);
  assert.equal(keyAction("a", { onPanel: true, suggestions }), null);
});

test("leaving focuses the pane that had the keys, whether the panel was entered by pane.go or a click", () => {
  assert.deepEqual(leaveCommand({ pane: "p1", terminal: "t1" }), { name: "pane.focus", args: { pane: "p1" } });
  assert.deepEqual(leaveCommand({ pane: null, terminal: "t1" }), { name: "pane.focus", args: { terminal: "t1" } });
  assert.deepEqual(leaveCommand(null), { name: "pane.go", args: { direction: "left" } });
  assert.deepEqual(leaveCommand({ pane: null, terminal: null }), { name: "pane.go", args: { direction: "left" } });
});
