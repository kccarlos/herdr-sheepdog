// Which of the panel and the compact rail a block draws, when the panel offers
// to be docked, and the vertical budget it shares (docs/dev/design/
// desktop-client.md, section 6.1): `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  COMPACT_BANNER_HEIGHT,
  COMPACT_BELOW,
  COMPACT_QUESTION_HEIGHT,
  DOCKED_WIDTH,
  MIN_TIMELINE_SHARE,
  layoutMode,
  needsDock,
  panelBudget,
  resolvePanelLayout,
} from "./layout.js";
import { estimateHeight, RETURN_SHARE } from "./presentation.js";

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

test("routine approvals stay inline on desktop panels without double-halving questionArea, while short windows and long descriptions compact", () => {
  const codexApproval = {
    agent: "auth",
    summary: "auth asks to run printf.",
    command: "printf 'approve-ok\\n'",
    options: [
      { label: "Yes, proceed" },
      { label: "Yes, and don't ask again for commands that start with `printf 'approve-ok\\n'`" },
      { label: "No, and tell Codex what to do differently" },
    ],
  };
  const longDescribed = {
    agent: "claude",
    summary: "Which approach should we take for session caching?",
    options: [1, 2, 3, 4].map((i) => ({
      label: `Option ${i}`,
      description: "A long description of what this option would do to the project and why you might pick it.",
    })),
  };
  const approvalHeight = estimateHeight(codexApproval, { width: DOCKED_WIDTH });
  const longHeight = estimateHeight(longDescribed, { width: DOCKED_WIDTH });

  // 1050 px (16" MacBook / 1080p) and 1385 px (27" 1440p): routine 3-option approval fits inline without scrolling.
  for (const height of [1050, 1385]) {
    const res = resolvePanelLayout({
      height,
      header: 54,
      voice: 88,
      composer: 80,
      questionHeights: [approvalHeight],
    });
    assert.equal(res.presentation, "inline", `3-option approval should stay inline at height=${height}`);
    assert.equal(res.layout.detailsScroll, false);
    assert.equal(res.layout.timelineMinHeight, Math.ceil(height * MIN_TIMELINE_SHARE));
  }

  // Long described options still compact on a 1050-1385 px sidebar.
  const longDesktop = resolvePanelLayout({
    height: 1385,
    header: 54,
    voice: 88,
    composer: 80,
    questionHeights: [longHeight],
  });
  assert.equal(longDesktop.presentation, "compact");
  assert.equal(longDesktop.layout.detailsHeight, 88 + COMPACT_QUESTION_HEIGHT);

  // Short windows (420x640 window or 662 px VM) compact the 3-option approval so the timeline keeps 55%.
  for (const height of [640, 662]) {
    const shortWin = resolvePanelLayout({
      height,
      header: 54,
      voice: 88,
      composer: 80,
      questionHeights: [approvalHeight],
    });
    assert.equal(shortWin.presentation, "compact", `short height=${height} compacts to preserve timeline`);
    assert.equal(shortWin.layout.detailsScroll, false);
  }

  // Returning from compact still requires fitting RETURN_SHARE of questionArea, and held freezes presentation.
  const base = panelBudget({ height: 1050, header: 54, voice: 88, composer: 80 });
  const near = Math.floor(base.questionArea * 0.95);
  assert.equal(
    resolvePanelLayout({ height: 1050, header: 54, voice: 88, composer: 80, questionHeights: [near] }, "compact").presentation,
    "compact",
  );
  assert.equal(
    resolvePanelLayout(
      { height: 1050, header: 54, voice: 88, composer: 80, questionHeights: [Math.floor(base.questionArea * RETURN_SHARE) - 1] },
      "compact",
    ).presentation,
    "inline",
  );
  assert.equal(
    resolvePanelLayout({ height: 640, header: 54, voice: 88, composer: 80, questionHeights: [approvalHeight], held: true }, "inline").presentation,
    "inline",
  );
});
