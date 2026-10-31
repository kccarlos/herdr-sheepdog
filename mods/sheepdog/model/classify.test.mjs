// The panel's classifier against the cases it shares with the phone:
// `make test-panel`. ios/Tests/SheepdogKitTests/TimelineClassificationFixtureTests.swift
// runs the same file through ConversationTimeline.classify.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { CATEGORIES, classify, daemonNotice, settledLine } from "./classify.js";

const fixture = JSON.parse(readFileSync(new URL("../../../ios/Tests/Fixtures/timeline-classification.json", import.meta.url), "utf8"));

test("every case classifies as the fixture says", () => {
  assert.ok(fixture.cases.length > 0);
  for (const c of fixture.cases) {
    const row = classify(c.item);
    assert.equal(row.category, c.category, c.name);
    assert.deepEqual({ shownByDefault: row.shownByDefault, folds: row.folds, bridgesRuns: row.bridgesRuns, ledger: row.ledger }, c.style, c.name);
    assert.equal(settledLine(c.item), c.line, c.name);
  }
});

test("the fixture covers every category an item can have", () => {
  const covered = new Set(fixture.cases.map((c) => c.category));
  // "screen" is the phone's folded group, which no single item makes.
  const expected = Object.keys(CATEGORIES).filter((c) => c !== "screen");
  assert.deepEqual([...covered].sort(), expected.sort());
});

test("the fixture covers every item kind and settled ending", () => {
  const kinds = new Set(fixture.cases.map((c) => c.item.kind));
  for (const kind of ["shepherd", "dog", "push", "approval", "card", "notice"]) assert.ok(kinds.has(kind), kind);
  const endings = new Set(fixture.cases.map((c) => c.item.resolution));
  for (const resolution of ["confirmed", "expired", "cancelled", "replaced"]) assert.ok(endings.has(resolution), resolution);
});

test("a notice is told by its subkind first and its words only without one", () => {
  assert.equal(daemonNotice({ kind: "notice", subkind: "fresh", text: "Voice context lost." }), "freshReconnect");
  assert.equal(daemonNotice({ kind: "notice", text: "Voice context lost." }), "contextLost");
  assert.equal(daemonNotice({ kind: "dog", text: "Sheepdog started." }), "other");
});
