import { test } from "node:test";
import assert from "node:assert/strict";
import { sameValue } from "./equal.js";

test("equal JSON is the same value, any difference is not", () => {
  assert.equal(sameValue({ a: [1, { b: null }] }, { a: [1, { b: null }] }), true);
  assert.equal(sameValue({ a: 1 }, { a: 2 }), false);
  assert.equal(sameValue({ a: 1 }, { a: 1, b: undefined }), false);
  assert.equal(sameValue([1, 2], [1, 2, 3]), false);
  assert.equal(sameValue([], {}), false);
  assert.equal(sameValue(null, {}), false);
  assert.equal(sameValue("x", "x"), true);
});
