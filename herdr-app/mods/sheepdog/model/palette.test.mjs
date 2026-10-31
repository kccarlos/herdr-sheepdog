// The amber of "needs you", by the theme's light or dark: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { AMBER_DARK, AMBER_LIGHT, amber, isLight } from "./palette.js";

test("the phone's amber: darker on a light sidebar, brighter on a dark one", () => {
  assert.equal(AMBER_LIGHT, "#D98718");
  assert.equal(AMBER_DARK, "#F39C1F");
  assert.equal(amber("#f4f4f5"), AMBER_LIGHT);
  assert.equal(amber("#ffffffff"), AMBER_LIGHT);
  assert.equal(amber("#1b1b1f"), AMBER_DARK);
});

test("a colour it cannot read is dark", () => {
  for (const odd of [undefined, null, "", "red", "#fff", "rgb(255,255,255)"]) assert.equal(isLight(odd), false, String(odd));
  assert.equal(amber(undefined), AMBER_DARK);
});
