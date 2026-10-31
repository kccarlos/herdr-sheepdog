// When the questions go compact (the rule of QuestionPresentation.decide,
// ios/Sources/SheepdogKit, with estimated heights): `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { BUDGET_SHARE, decide, estimateHeight, natural, RETURN_SHARE, SPACING } from "./presentation.js";

const WIDTH = 420;
const AREA = 640;

test("a short question with a few options fits inline; a long one does not", () => {
  const command = { summary: "Allow git status?", command: "git status", options: [{ label: "Yes" }, { label: "Yes, always" }, { label: "No" }] };
  assert.equal(decide({ heights: [estimateHeight(command, { width: WIDTH })], area: AREA }, null), "inline");
  const described = { summary: "Which approach?", options: [1, 2, 3, 4].map((i) => ({ label: `Option ${i}`, description: "A long description of what this option would do to the project. ".repeat(3) })) };
  assert.equal(decide({ heights: [estimateHeight(described, { width: WIDTH })], area: AREA }, null), "compact");
});

test("a wider window needs less height; options and descriptions add theirs", () => {
  const q = { summary: "x".repeat(200), options: [{ label: "A", description: "d".repeat(100) }] };
  assert.ok(estimateHeight(q, { width: 300 }) > estimateHeight(q, { width: 800 }));
  assert.ok(estimateHeight({ ...q, options: [...q.options, { label: "B" }] }, { width: 420 }) > estimateHeight(q, { width: 420 }));
  assert.ok(estimateHeight({ ...q, command: "ls -la" }, { width: 420 }) > estimateHeight(q, { width: 420 }));
});

test("the question counts at most three lines, the command at most three", () => {
  const long = "word ".repeat(500);
  assert.equal(estimateHeight({ summary: long, options: [] }, { width: 420 }), estimateHeight({ summary: long + long, options: [] }, { width: 420 }));
  assert.equal(estimateHeight({ summary: "s", command: long, options: [] }, { width: 420 }), estimateHeight({ summary: "s", command: long + long, options: [] }, { width: 420 }));
});

test("stacked cards add their spacing", () => {
  assert.equal(natural([100, 50, 20]), 170 + 2 * SPACING);
  assert.equal(natural([]), 0);
});

test("inline while the cards fit the budget, compact when they do not", () => {
  const budget = AREA * BUDGET_SHARE;
  assert.equal(decide({ heights: [budget], area: AREA }, null), "inline");
  assert.equal(decide({ heights: [budget + 1], area: AREA }, null), "compact");
  assert.equal(decide({ heights: [budget / 2, budget / 2], area: AREA }, null), "compact", "the gap between them counts");
});

test("coming back from compact needs the cards to fit in the return share of the budget", () => {
  const budget = AREA * BUDGET_SHARE;
  const near = budget * 0.95;
  assert.equal(decide({ heights: [near], area: AREA }, "inline"), "inline");
  assert.equal(decide({ heights: [near], area: AREA }, "compact"), "compact", "near the line it stays");
  assert.equal(decide({ heights: [budget * RETURN_SHARE - 1], area: AREA }, "compact"), "inline");
});

test("held while the user is mid-answer; not measured yet keeps what is on screen", () => {
  assert.equal(decide({ heights: [10_000], area: AREA, held: true }, "inline"), "inline");
  assert.equal(decide({ heights: [10], area: AREA, held: true }, "compact"), "compact");
  assert.equal(decide({ heights: [10_000], area: AREA, held: true }, null), "compact", "nothing on screen yet: decide");
  assert.equal(decide({ heights: [10_000], area: 0 }, null), "inline");
  assert.equal(decide({ heights: [10_000], area: 0 }, "compact"), "compact");
  assert.equal(decide({ heights: [0], area: AREA }, null), "inline", "a card with no height is not measured");
});

test("no cards: what is on screen, inline at first", () => {
  assert.equal(decide({ heights: [], area: AREA }, null), "inline");
  assert.equal(decide({ heights: [], area: AREA }, "compact"), "compact");
});
