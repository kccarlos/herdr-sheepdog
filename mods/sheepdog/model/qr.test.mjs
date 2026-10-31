// The QR matrix becomes drawable rows: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { qrRows } from "./qr.js";

test("the daemon's module strings become square rows", () => {
  assert.deepEqual(qrRows({ size: 3, modules: ["101", "010", "111"] }), [
    ["1", "0", "1"],
    ["0", "1", "0"],
    ["1", "1", "1"],
  ]);
});

test("a missing, ragged or non-QR matrix is refused instead of drawn wrong", () => {
  for (const matrix of [
    null,
    { size: 2, modules: ["10"] },
    { size: 2, modules: ["10", "1"] },
    { size: 2, modules: ["10", "12"] },
    { size: 0, modules: [] },
  ]) {
    assert.throws(() => qrRows(matrix), /pairing code/);
  }
});
