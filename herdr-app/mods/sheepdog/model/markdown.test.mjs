// The card's markdown, split into blocks as the phone does
// (MarkdownRenderer.blocks): `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { blocks, cells, delimiterRow, plainInline } from "./markdown.js";

test("paragraphs keep their lines; a blank line starts the next", () => {
  assert.deepEqual(blocks("one\ntwo\n\nthree"), [{ type: "paragraph", text: "one\ntwo" }, { type: "paragraph", text: "three" }]);
  assert.deepEqual(blocks(""), []);
});

test("a table is a header, alignments and rows padded or cut to the header", () => {
  const md = "| Test | Error |\n|:---|---:|\n| TestLogin | timeout |\n| TestB |\n| x | y | z |";
  assert.deepEqual(blocks(md), [{ type: "table", header: ["Test", "Error"], alignments: ["leading", "trailing"], rows: [["TestLogin", "timeout"], ["TestB", ""], ["x", "y"]] }]);
});

test("cells and delimiter rows", () => {
  assert.deepEqual(cells("| a \\| b | c |"), ["a | b", "c"]);
  assert.deepEqual(cells("a | b"), ["a", "b"]);
  assert.deepEqual(delimiterRow("|---|:---:|--:|"), ["leading", "center", "trailing"]);
  assert.equal(delimiterRow("| a | b |"), null);
  assert.equal(delimiterRow("plain"), null);
});

test("a header row without a delimiter row is a paragraph", () => {
  assert.deepEqual(blocks("| a | b |\n| c | d |").map((b) => b.type), ["paragraph"]);
});

test("bullets, numbers, tasks, nesting and continuation lines", () => {
  const md = "- one\n  more of one\n  - nested\n1. first\n2) second\n- [ ] todo\n- [x] done";
  assert.deepEqual(blocks(md), [{ type: "list", items: [
    { marker: "•", depth: 0, text: "one more of one" },
    { marker: "•", depth: 1, text: "nested" },
    { marker: "1.", depth: 0, text: "first" },
    { marker: "2.", depth: 0, text: "second" },
    { marker: "☐", depth: 0, text: "todo" },
    { marker: "☑", depth: 0, text: "done" },
  ] }]);
});

test("headings are text without their hashes; seven hashes or no space are not headings", () => {
  assert.deepEqual(blocks("## Plan ##"), [{ type: "heading", text: "Plan" }]);
  assert.deepEqual(blocks("####### no").map((b) => b.type), ["paragraph"]);
  assert.deepEqual(blocks("#tag").map((b) => b.type), ["paragraph"]);
});

test("fenced code keeps its lines, an unclosed fence runs to the end", () => {
  assert.deepEqual(blocks("before\n```js\nlet a = 1;\n\n  b();\n```\nafter"), [
    { type: "paragraph", text: "before" },
    { type: "code", text: "let a = 1;\n\n  b();" },
    { type: "paragraph", text: "after" },
  ]);
  assert.deepEqual(blocks("~~~\nopen"), [{ type: "code", text: "open" }]);
});

test("inline markers go and the words stay", () => {
  assert.equal(plainInline("**bold** and *it* and `code` and ~~gone~~"), "bold and it and code and gone");
  assert.equal(plainInline("see [the docs](https://example.com/x) now"), "see the docs now");
  assert.equal(plainInline("2 * 3 * 4"), "2 * 3 * 4");
  assert.equal(plainInline("snake_case_name"), "snake_case_name");
});
