// What the composer sends, what a failed send puts back, and the keys:
// `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { keyMaker, restored, submission } from "./composer.js";

test("Enter sends the value minus one trailing newline", () => {
  assert.equal(submission("hello\n"), "hello");
  assert.equal(submission("hello\r\n"), "hello");
  assert.equal(submission("two\n\n"), "two\n", "only ONE newline goes");
  assert.equal(submission("a\nb\n"), "a\nb", "inner newlines (Shift+Enter) stay");
  assert.equal(submission("no newline"), "no newline");
});

test("Shift+Enter is a newline: nothing is sent", () => {
  assert.equal(submission("hello\n", { shift: true }), null);
  assert.equal(submission("hello", { shift: true }), null);
});

test("an empty or blank value never sends", () => {
  for (const value of ["", "\n", "   ", " \n", "\n\n", "\t\n"]) assert.equal(submission(value), null, JSON.stringify(value));
});

test("a failed send puts the text back ahead of what was typed since", () => {
  assert.equal(restored("first", ""), "first");
  assert.equal(restored("first", "second"), "first\nsecond");
});

test("keys are visible ASCII of at most 128 characters and differ", () => {
  const key = keyMaker({ now: () => 1_700_000_000_000, random: () => 0.5 });
  const [a, b] = [key(), key()];
  assert.notEqual(a, b);
  for (const k of [a, b]) assert.match(k, /^[\x21-\x7e]{1,128}$/);
});
