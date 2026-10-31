// Which of the panel and the compact rail a block draws, when the panel offers
// to be docked, and the vertical budget it shares (docs/dev/design/
// desktop-client.md, section 6.1): `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  COMPACT_BANNER_HEIGHT,
  COMPACT_BELOW,
  DOCKED_WIDTH,
  MIN_TIMELINE_SHARE,
  layoutMode,
  needsDock,
  panelBudget,
} from "./layout.js";

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

test("a short fallback window keeps 55% for the timeline and compacts its banners", () => {
  const full = panelBudget({
    height: 640,
    header: 76,
    banners: [150, 170],
    voice: 58,
    question: 310,
    composer: 128,
  });
  assert.equal(full.timelineMinHeight, Math.ceil(640 * MIN_TIMELINE_SHARE));
  assert.equal(full.compactBanners, true);
  assert.equal(full.detailsHeight, full.detailsLimit);
  assert.equal(full.detailsScroll, true, "the compact top stack still scrolls");
  assert.ok(full.questionArea >= 0);
});

test("a tall panel keeps full banners and gives the question its available area", () => {
  const tall = panelBudget({
    height: 2000,
    header: 76,
    banners: [150, 170],
    voice: 58,
    question: 310,
    composer: 128,
  });
  assert.equal(tall.timelineMinHeight, Math.ceil(2000 * MIN_TIMELINE_SHARE));
  assert.equal(tall.compactBanners, false);
  assert.equal(tall.detailsScroll, false);
  assert.equal(tall.questionArea, tall.detailsLimit - 150 - 170 - 58);
});

test("a compact top stack reports its exact fit, and empty content takes no height", () => {
  const compact = panelBudget({ height: 900, header: 76, banners: [150, 170], voice: 58, question: 44, composer: 128 });
  assert.equal(compact.detailsHeight, COMPACT_BANNER_HEIGHT * 2 + 58 + 44);
  assert.equal(compact.detailsScroll, false);
  const empty = panelBudget({ height: 640, header: 76, composer: 128 });
  assert.equal(empty.detailsHeight, 0);
  assert.equal(empty.questionArea, empty.detailsLimit);
});
