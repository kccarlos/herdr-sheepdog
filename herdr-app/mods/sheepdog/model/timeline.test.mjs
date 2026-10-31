// The rows the panel lists, and what the neighbours decide about them
// (docs/dev/design/conversation-timeline.md, sections 5 and 11): `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { buildRows, dayTitle, gap, layoutEntries, ledgerPositions, newestFirst, visibleRows } from "./timeline.js";

// UTC days and times, so the tests do not depend on the machine's zone.
const DAY = 24 * 60 * 60 * 1000;
const time = {
  startOfDay: (ms) => Math.floor(ms / DAY) * DAY,
  formatTime: (ms) => new Date(ms).toISOString().slice(11, 16),
  parts: (ms) => {
    const d = new Date(ms);
    return { weekday: d.getUTCDay(), day: d.getUTCDate(), month: d.getUTCMonth(), year: d.getUTCFullYear() };
  },
};
const T0 = Date.UTC(2026, 8, 22, 8, 0, 0); // 2026-09-22 08:00 UTC

// An item `sec` seconds after T0, with an id that sorts like its time.
let next = 1;
const item = (sec, kind, extra = {}) => ({
  id: (T0 + sec * 1000) * 1000 + next++, ts: new Date(T0 + sec * 1000).toISOString(), kind, text: "t", ...extra,
});
const ids = (rows) => rows.map((r) => r.id);
const types = (rows) => rows.map((r) => r.type);

test("a day heading goes above the first row and wherever the day changes", () => {
  const rows = buildRows([item(0, "shepherd"), item(60, "dog"), item(DAY / 1000 + 60, "dog")], time);
  assert.deepEqual(types(rows), ["day", "item", "item", "day", "item"]);
  assert.equal(rows[0].id, `day.${time.startOfDay(T0) / 1000}`);
});

test("a push that repeats the approval just above it is not listed twice", () => {
  const ask = item(0, "approval", { text: "Allow it?", resolution: "open" });
  const push = item(1, "push", { text: "Allow it?", urgency: "interrupt" });
  const other = item(2, "push", { text: "Something else.", urgency: "idle" });
  assert.deepEqual(types(buildRows([ask, push, other], time)), ["day", "item", "item"]);
});

test("a restart reads started, restarted or updated, with its time", () => {
  const start = (sec, version) => item(sec, "notice", { text: "Sheepdog started.", subkind: "start", version });
  const rows = buildRows([start(0, "0.9.0"), item(10, "dog"), start(20, "0.9.0"), start(30, "0.9.1")], time);
  const titles = rows.filter((r) => r.type === "divider").map((r) => r.title);
  assert.deepEqual(titles, ["Sheepdog started · 08:00", "Sheepdog restarted · 08:00", "Sheepdog updated to v0.9.1 · 08:00"]);
});

test("the plumbing is hidden, and brought back with the technical view", () => {
  const items = [
    item(0, "shepherd"), item(1, "notice", { text: "Voice context lost.", subkind: "context_lost" }),
    item(2, "notice", { text: "Voice reconnected; the conversation continues.", subkind: "resumed" }),
    item(3, "wobble", { text: "" }), item(4, "dog"),
  ];
  const rows = buildRows(items, time);
  const shown = visibleRows(rows, { time }).rows;
  assert.deepEqual(types(shown), ["day", "item", "item"]);
  assert.deepEqual(ids(visibleRows(rows, { showTechnical: true, time }).rows), ids(rows));
});

test("a day heading left over a hidden row goes with it", () => {
  const items = [item(0, "notice", { text: "Voice context lost.", subkind: "context_lost" }), item(DAY / 1000, "dog")];
  assert.deepEqual(types(visibleRows(buildRows(items, time), { time }).rows), ["day", "item"]);
});

test("a fresh reconnect is shown as one row only within five minutes of dialogue", () => {
  const lost = (sec) => item(sec, "notice", { text: "Voice context lost.", subkind: "context_lost" });
  const fresh = (sec) => item(sec, "notice", { text: "Voice reconnected with a fresh session; earlier context was lost.", subkind: "fresh" });
  const near = visibleRows(buildRows([item(0, "shepherd"), lost(100), fresh(101)], time), { time }).rows;
  const restarted = near.filter((r) => r.type === "divider");
  assert.equal(restarted.length, 1);
  assert.equal(restarted[0].title, "Voice restarted — it may not remember the last minute · 08:01");
  assert.equal(restarted[0].item.subkind, "voice_restarted");

  const far = visibleRows(buildRows([item(0, "shepherd"), lost(400), fresh(401)], time), { time }).rows;
  assert.equal(far.filter((r) => r.type === "divider").length, 0);
  const alone = visibleRows(buildRows([fresh(10)], time), { time }).rows;
  assert.equal(alone.filter((r) => r.type === "divider").length, 0);
});

test("a settled question is one line: its push and unanswered restatement fold into it", () => {
  const push = item(0, "push", { text: "docs asks to run it.", agent: "docs", urgency: "interrupt" });
  const ask = item(1, "approval", { text: "docs asks to run it.", agent: "docs", resolution: "cancelled" });
  const said = item(5, "dog", { text: "docs is asking to run it." });
  const rows = buildRows([push, ask, said], time);
  const { rows: shown, attached } = visibleRows(rows, { time });
  assert.deepEqual(ids(shown), [rows[0].id, `item.${ask.id}`]);
  assert.deepEqual(attached.get(`item.${ask.id}`).map((r) => r.id), [`item.${push.id}`, `item.${said.id}`]);
});

test("a voice line the user answered stays dialogue", () => {
  const ask = item(0, "approval", { text: "Allow it?", agent: "docs", resolution: "confirmed", answer: "Yes" });
  const said = item(5, "dog", { text: "docs wants an answer." });
  const reply = item(8, "shepherd", { text: "yes" });
  const { rows: shown } = visibleRows(buildRows([ask, said, reply], time), { time });
  assert.equal(shown.filter((r) => r.type === "item").length, 3);
});

test("an open question and news about the same agent are not folded", () => {
  const ask = item(0, "approval", { text: "Allow it?", agent: "docs", resolution: "open" });
  const news = item(1, "push", { text: "docs finished the build.", agent: "docs", urgency: "idle" });
  assert.equal(visibleRows(buildRows([ask, news], time), { time }).rows.filter((r) => r.type === "item").length, 2);
});

test("consecutive ledger lines share one card", () => {
  const rows = buildRows([
    item(0, "dog"), item(1, "push", { urgency: "idle" }), item(2, "push", { urgency: "idle" }), item(3, "dog"), item(4, "push", { urgency: "idle" }),
  ], time);
  const pos = ledgerPositions(rows);
  const [, , a, b, , c] = rows;
  assert.deepEqual(pos.get(a.id), { joinsAbove: false, joinsBelow: true });
  assert.deepEqual(pos.get(b.id), { joinsAbove: true, joinsBelow: false });
  assert.deepEqual(pos.get(c.id), { joinsAbove: false, joinsBelow: false });
  assert.equal(pos.size, 3);
});

test("the space between rows follows the phone's table", () => {
  const rows = buildRows([
    item(0, "shepherd"), item(1, "dog"), item(2, "shepherd"), item(3, "approval", { resolution: "open" }),
    item(4, "push", { text: "news", urgency: "idle" }), item(5, "card"), item(6, "notice", { subkind: "reset", text: "New conversation" }), item(7, "dog"),
  ], time);
  const [day, you, dog, you2, ask, push, card, reset, dog2] = rows;
  assert.equal(gap(day, you), 14, "a divider to dialogue");
  assert.equal(gap(you, dog), 6, "within one exchange");
  assert.equal(gap(dog, you2), 18, "a new exchange starts");
  assert.equal(gap(you2, ask), 18, "dialogue to a question: the normal space, not tucked");
  assert.equal(gap(ask, push), 18, "between categories");
  assert.equal(gap(push, card), 8, "a run and a card");
  assert.equal(gap(card, reset), 18);
  assert.equal(gap(reset, dog2), 14);
  assert.equal(gap(push, push, { joined: true }), 0);
  assert.equal(gap(you, ask, { technical: true }), 6);
});

test("the list is newest first: each day's heading above its rows, the newest day on top", () => {
  const items = [item(0, "shepherd"), item(60, "dog"), item(DAY / 1000, "shepherd"), item(DAY / 1000 + 60, "dog")];
  const drawn = newestFirst(buildRows(items, time));
  assert.deepEqual(drawn.map((r) => r.type), ["day", "item", "item", "day", "item", "item"]);
  assert.deepEqual(drawn.filter((r) => r.type === "item").map((r) => r.item.id), [items[3].id, items[2].id, items[1].id, items[0].id]);
  assert.ok(drawn[0].date > drawn[3].date);
});

test("the list's entries carry the space above, the ledger place, the time and the day's words", () => {
  const items = [item(0, "shepherd"), item(1, "push", { urgency: "idle" }), item(2, "push", { urgency: "idle" }), item(3, "notice", { text: "Voice context lost.", subkind: "context_lost" })];
  const now = T0 + 3600 * 1000;
  const entries = layoutEntries(items, { time, now });
  // Newest first: the day, the two updates (the later one on top), you.
  assert.deepEqual(entries.map((e) => e.row.id), [entries[0].row.id, `item.${items[2].id}`, `item.${items[1].id}`, `item.${items[0].id}`], "the plumbing is out");
  assert.equal(entries[0].gap, 12);
  assert.equal(entries[0].dayLabel, "Today");
  assert.equal(entries[1].gap, 8, "under a heading, a divider to a row");
  assert.equal(entries[2].gap, 0, "inside a ledger run");
  assert.equal(entries[3].gap, 18, "from the updates to what you said");
  // The run is drawn upside down: the later update is its top.
  assert.deepEqual(entries[1].ledger, { joinsAbove: false, joinsBelow: true });
  assert.deepEqual(entries[2].ledger, { joinsAbove: true, joinsBelow: false });
  assert.equal(entries[3].timeLabel, "08:00");
  const technical = layoutEntries(items, { showTechnical: true, time, now });
  assert.equal(technical.length, 5);
  assert.ok(technical.slice(1).every((e) => e.gap === 6 && e.technical));
});

test("two days are 18 apart, and a day's rows keep the phone's spacing", () => {
  const items = [item(0, "dog"), item(DAY / 1000, "shepherd"), item(DAY / 1000 + 1, "dog")];
  const entries = layoutEntries(items, { time });
  assert.deepEqual(entries.map((e) => e.row.type), ["day", "item", "item", "day", "item"]);
  assert.equal(entries[3].gap, 18);
  assert.equal(entries[2].gap, 6, "the voice answering you is one exchange");
});

test("a day is Today, Yesterday or its name, with the year only when it is not this one", () => {
  const now = Date.UTC(2026, 8, 22, 15, 0, 0);
  const today = time.startOfDay(now);
  assert.equal(dayTitle(today, now, time), "Today");
  assert.equal(dayTitle(today - DAY, now, time), "Yesterday");
  assert.equal(dayTitle(Date.UTC(2026, 8, 14), now, time), "Monday 14 September");
  assert.equal(dayTitle(Date.UTC(2025, 11, 31), now, time), "Wednesday 31 December 2025");
});
