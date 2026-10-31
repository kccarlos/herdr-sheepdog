// What the panel lists, top to bottom: the daemon's items in time order, a day
// heading wherever the day changes, and the daemon's notices turned into
// quiet dividers; then what the neighbours decide (a reconnect that matters,
// a settled question and the push that announced it) and the ledger's joins.
// It ports ConversationTimeline.rows, TimelineContext, visibleRows,
// ledgerPositions and gap (ios/Sources/SheepdogKit) for the rows the panel
// has: history items, with no live bubbles, orchestrator replies or held
// pushes (those come over /ws, which the panel never opens), and no folded
// "N agent updates" group yet. Pure, so node tests it. No herdr imports.

import { classify, DAY_CLASS, daemonNotice, settledLine, VOICE_RESTARTED } from "./classify.js";

const MINUTE = 60 * 1000;

// A reconnect matters only when the voice may have lost something you just
// said: this long after the last dialogue.
export const RECONNECT_WINDOW = 5 * MINUTE;
// A settled question's own push and restatement are looked for this long
// after it was asked.
export const QUESTION_WINDOW = 2 * MINUTE;

const trimmed = (s) => (s ?? "").trim();

// An item's time in ms. An item with no `ts` is as old as its id (event ids
// are Unix microseconds).
export const itemTime = (item) => {
  const t = Date.parse(item.ts);
  return Number.isNaN(t) ? Math.floor(item.id / 1000) : t;
};

const pad = (n) => String(n).padStart(2, "0");
const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];

// Local time, which is what a person reads; tests pass their own.
export const localTime = {
  startOfDay(ms) {
    const d = new Date(ms);
    d.setHours(0, 0, 0, 0);
    return d.getTime();
  },
  // "17:42": the runtime may have no locale data, so no Intl.
  formatTime(ms) {
    const d = new Date(ms);
    return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  },
  // The calendar day a time falls on: weekday 0 is Sunday, month 0 January.
  parts(ms) {
    const d = new Date(ms);
    return { weekday: d.getDay(), day: d.getDate(), month: d.getMonth(), year: d.getFullYear() };
  },
};

// "Today", "Yesterday", or "Monday 21 September" (with the year when it is not
// this one).
export function dayTitle(day, now = Date.now(), time = localTime) {
  const today = time.startOfDay(now);
  if (day === today) return "Today";
  if (day === time.startOfDay(today - 1)) return "Yesterday";
  const p = time.parts(day);
  const title = `${DAYS[p.weekday]} ${p.day} ${MONTHS[p.month]}`;
  return p.year === time.parts(today).year ? title : `${title} ${p.year}`;
}

// "v0.9.1" for "0.9.1"; a name such as "dev" is left as it is.
const displayVersion = (version) => (/^\d/.test(version) ? `v${version}` : version);

// "Sheepdog restarted · 17:42", or "Sheepdog updated to v0.9.1 · 17:42" when
// the daemon says a version and the one before it (the previous start in the
// history) said another. An older daemon sends no version, so its restart is
// just "restarted".
export function dividerTitle(item, { afterContent, previousVersion, formatTime }) {
  let text = trimmed(item.text);
  if (daemonNotice(item) === "restarted") {
    text = afterContent ? "Sheepdog restarted" : "Sheepdog started";
    const version = trimmed(item.version);
    if (version && previousVersion && previousVersion !== version) text = `Sheepdog updated to ${displayVersion(version)}`;
  }
  text = text.replace(/\.+$/, "");
  if (!text) text = "Notice";
  return `${text} · ${formatTime(itemTime(item))}`;
}

const itemRow = (item) => ({ id: `item.${item.id}`, type: "item", date: itemTime(item), item });

// Builds the rows from the history (oldest first). Row ids are stable, so a
// row keeps its identity as later ones join.
export function buildRows(items, time = localTime) {
  const merged = [];
  let previousApproval = null;
  let sawContent = false;
  let lastVersion = null;
  for (const item of items) {
    switch (item.kind) {
      case "shepherd":
      case "dog":
      case "approval":
      case "card":
        merged.push(itemRow(item));
        sawContent = true;
        break;
      case "push":
        // The daemon announces a new approval as a push with the same words;
        // the approval row already says it.
        if (previousApproval && trimmed(previousApproval.text) === trimmed(item.text)) continue;
        merged.push(itemRow(item));
        sawContent = true;
        break;
      default: {
        // A notice, or a kind this build does not know.
        const title = dividerTitle(item, { afterContent: sawContent, previousVersion: lastVersion, formatTime: time.formatTime });
        merged.push({ ...itemRow(item), type: "divider", title });
        const version = trimmed(item.version);
        if (daemonNotice(item) === "restarted" && version) lastVersion = version;
      }
    }
    previousApproval = item.kind === "approval" ? item : null;
  }
  // Day headings where the day changes (and above the first row).
  const out = [];
  let currentDay = null;
  for (const row of merged) {
    const day = time.startOfDay(row.date);
    if (day !== currentDay) {
      out.push({ id: `day.${Math.floor(day / 1000)}`, type: "day", date: day });
      currentDay = day;
    }
    out.push(row);
  }
  return out;
}

// What the screen does with one row: a day heading is context, the rest is
// their item's.
export const classifyRow = (row) => (row.type === "day" ? DAY_CLASS : classify(row.item));

const isVoiceSpeech = (row) => row.type === "item" && row.item.kind === "dog";
const isUserSpeech = (row) => row.type === "item" && row.item.kind === "shepherd";

// "Voice context lost" and the fresh reconnect after it are one event, and it
// is shown, as one row, only when the voice was in a conversation: dialogue in
// the last 5 minutes. Every other reconnect stays plumbing.
function promoteReconnects(rows, context) {
  let lastDialogue = null;
  for (const row of rows) {
    if (classifyRow(row).category === "dialogue") {
      lastDialogue = row.date;
      continue;
    }
    if (row.type === "day" || daemonNotice(row.item) !== "freshReconnect") continue;
    if (lastDialogue == null || row.date - lastDialogue > RECONNECT_WINDOW) continue;
    const item = { ...row.item, subkind: VOICE_RESTARTED };
    const title = `Voice restarted — it may not remember the last minute · ${context.formatTime(row.date)}`;
    context.promoted.set(row.id, { ...row, type: "divider", item, title });
  }
}

// A push that says the question: its words, or an interrupt from the same
// agent. Plain news about the agent is not folded away.
function announces(row, summary, agent) {
  if (row.type !== "item" || row.item.kind !== "push") return false;
  if (trimmed(row.item.text).toLowerCase() === summary) return true;
  return row.item.urgency === "interrupt" && agent !== "" && trimmed(row.item.agent).toLowerCase() === agent;
}

// A settled question is one line; the push that announced it, and the voice's
// restatement, fold into it. The restatement folds only when the user did not
// reply to it: a voice line you answered is dialogue.
function foldSettledQuestions(rows, context) {
  rows.forEach((row, index) => {
    if (row.type !== "item" || row.item.kind !== "approval" || settledLine(row.item) == null) return;
    const question = row.item;
    const agent = trimmed(question.agent).toLowerCase();
    const summary = trimmed(question.text).toLowerCase();
    const end = row.date + QUESTION_WINDOW;
    const folded = [];

    // The announcing push can land a moment before the item.
    let start = index;
    while (start > 0 && index - start < 2 && row.date - rows[start - 1].date <= QUESTION_WINDOW) start--;

    let sawVoice = false;
    for (let at = start; at < rows.length; at++) {
      if (at === index) continue;
      const other = rows[at];
      if (at > index && other.date > end) break;
      if (other.type === "day") {
        if (at > index) break;
        continue;
      }
      if (context.absorbed.has(other.id)) continue;
      if (announces(other, summary, agent)) {
        folded.push(other);
      } else if (at > index && !sawVoice && isVoiceSpeech(other)) {
        // Only the first thing the voice said afterwards.
        sawVoice = true;
        const text = trimmed(other.item.text).toLowerCase();
        const mentions = text === summary || (agent !== "" && text.includes(agent));
        let replied = false;
        for (let next = at + 1; next < rows.length && rows[next].date <= end; next++) {
          if (isUserSpeech(rows[next])) {
            replied = true;
            break;
          }
        }
        if (mentions && !replied) folded.push(other);
      }
    }
    if (folded.length === 0) return;
    context.attached.set(row.id, [...(context.attached.get(row.id) ?? []), ...folded]);
    for (const f of folded) context.absorbed.add(f.id);
  });
}

// What the neighbours decide about the rows, in one pass: `promoted` rows to
// show in place of a row, `absorbed` ids not listed on their own because they
// are `attached` to another row.
export function contextOf(rows, time = localTime) {
  const context = { promoted: new Map(), absorbed: new Set(), attached: new Map(), formatTime: time.formatTime };
  promoteReconnects(rows, context);
  foldSettledQuestions(rows, context);
  return context;
}

// The rows without the plumbing, and without a day heading left over it. With
// `showTechnical` every row comes back unchanged. Rows the neighbours decide
// are applied.
export function visibleRows(rows, { showTechnical = false, time = localTime } = {}) {
  if (showTechnical) return { rows, attached: new Map() };
  const context = contextOf(rows, time);
  const shown = [];
  for (const row of rows) {
    if (context.absorbed.has(row.id)) continue;
    const promoted = context.promoted.get(row.id);
    if (promoted) shown.push(promoted);
    else if (classifyRow(row).shownByDefault) shown.push(row);
  }
  const listed = shown.filter((row, i) => {
    if (row.type !== "day") return true;
    return i + 1 < shown.length && shown[i + 1].type !== "day";
  });
  return { rows: listed, attached: context.attached };
}

// The position of every ledger line (by row id) in its run: consecutive
// agent outcomes share one card, so a line joined above or below drops that
// corner and its border. Anything that is not a ledger line ends the run.
export function ledgerPositions(rows) {
  const isLedger = (i) => i >= 0 && i < rows.length && classifyRow(rows[i]).ledger;
  const out = new Map();
  rows.forEach((row, i) => {
    if (isLedger(i)) out.set(row.id, { joinsAbove: isLedger(i - 1), joinsBelow: isLedger(i + 1) });
  });
  return out;
}

const isCardShown = (row) => row.type === "item" && row.item.kind === "card";
const isFromUser = (row) => row.type === "item" && row.item.kind === "shepherd";

// The space above `row` under `previous`: 6 within one exchange, 14 from a
// divider to dialogue, 18 between different categories (and when a new
// exchange starts), 0 inside a ledger run, 8 between a run and a card. The
// technical view is a log: 6 everywhere.
export function gap(previous, row, { joined = false, technical = false } = {}) {
  if (technical) return 6;
  if (joined) return 0;
  const [a, b] = [classifyRow(previous).category, classifyRow(row).category];
  if (isCardShown(previous) || isCardShown(row)) {
    const ledger = classifyRow(previous).ledger || classifyRow(row).ledger;
    return ledger || (isCardShown(previous) && isCardShown(row)) ? 8 : 18;
  }
  if (a === "context") return b === "dialogue" ? 14 : 8;
  if (a === "dialogue" && b === "dialogue") return !isFromUser(previous) && isFromUser(row) ? 18 : 6;
  if (a === "dialogue" && b === "needsYou") return 4;
  return 18;
}

// Newest first: each day's heading above that day's rows, the newest day at
// the top and each day's newest row first. The list element cannot be scrolled
// from code (the mod runtime ignores a scroll handle on it), so a list that
// starts at its top has to start with what you want first, and older pages
// are added below, where they move nothing.
export function newestFirst(rows) {
  const days = [];
  for (const row of rows) {
    if (row.type === "day") days.push([row]);
    else days[days.length - 1].push(row);
  }
  return days.reverse().flatMap(([day, ...rest]) => [day, ...rest.reverse()]);
}

// Everything the list draws, in one pass: the rows (the plumbing out, the
// neighbours' decisions applied, or with `showTechnical` every row as it is),
// newest first, each with the space above it, its place in a ledger run and
// its words for the time. A row's neighbours are the ones drawn beside it, but
// the space and the joins are the phone's, from the order things happened, so
// the run's "above" is the next older row.
export function layoutEntries(items, { showTechnical = false, time = localTime, now = Date.now() } = {}) {
  const { rows } = visibleRows(buildRows(items, time), { showTechnical, time });
  const ledger = ledgerPositions(rows);
  return newestFirst(rows).map((row, i, drawn) => {
    const above = drawn[i - 1];
    const place = ledger.get(row.id);
    let space = 12;
    if (above?.type === "day") {
      // Under a day's heading: the table's divider-to-row space.
      space = gap(above, row, { technical: showTechnical });
    } else if (above && row.type === "day") {
      // Between two days.
      space = showTechnical ? 6 : 18;
    } else if (above) {
      // Between two rows: the space the phone puts between the older and the
      // newer, and none inside a ledger run.
      space = gap(row, above, { joined: place?.joinsBelow ?? false, technical: showTechnical });
    }
    return {
      row,
      gap: space,
      // Drawn upside down: what is older is below.
      ledger: place ? { joinsAbove: place.joinsBelow, joinsBelow: place.joinsAbove } : null,
      timeLabel: time.formatTime(row.date),
      dayLabel: row.type === "day" ? dayTitle(row.date, now, time) : "",
      technical: showTechnical,
    };
  });
}
