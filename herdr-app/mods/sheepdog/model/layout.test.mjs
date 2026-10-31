// Which of the panel and the compact rail a block draws, and when the panel
// offers to be docked (docs/dev/design/desktop-client.md, section 6.1):
// `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { COMPACT_BELOW, DOCKED_WIDTH, layoutMode, needsDock } from "./layout.js";

test("a docked block is the panel from 160 px, the rail under it", () => {
  assert.equal(COMPACT_BELOW, 160);
  assert.equal(layoutMode({ width: 340, overlay: undefined }), "panel");
  assert.equal(layoutMode({ width: 160, overlay: undefined }), "panel");
  assert.equal(layoutMode({ width: 159.5, overlay: undefined }), "rail");
  assert.equal(layoutMode({ width: 48, overlay: undefined }), "rail");
});

test("a window is always the panel, however narrow it is reported", () => {
  assert.equal(layoutMode({ width: 100, overlay: "ov1" }), "panel");
  assert.equal(layoutMode({ width: 420, overlay: "ov1" }), "panel");
});

test("before the first size report a docked block is taken as wide enough for the panel", () => {
  assert.equal(layoutMode({ width: DOCKED_WIDTH, overlay: undefined }), "panel");
});

test("only a window that the docked rail did not open offers docking", () => {
  assert.equal(needsDock({ overlay: "ov1", docked: undefined }), true, "mod.open");
  assert.equal(needsDock({ overlay: "ov1", docked: true }), false, "opened from the rail");
  assert.equal(needsDock({ overlay: undefined, docked: undefined }), false, "already docked");
});
