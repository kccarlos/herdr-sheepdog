// How each row of the timeline looks (docs/dev/design/conversation-timeline.md,
// section 12): shape tells the kinds apart and colour confirms. Dialogue is a
// bubble with one 6 px corner (you on the right, the voice on the left); Needs
// you is the one amber, a pointer; outcomes are lines of a ledger, consecutive
// ones in one card; context is a hairline with a caption; plumbing (only in
// the technical view) and the panel's own notices are dashed. Every colour is
// a theme() name. These are drawn inside a `list()` row, which may carry no
// handlers: nothing here is clickable. A press on a row is the list's own
// `on_item_click` (ui/panel.js), which is how a card row opens.

import { div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { CATEGORIES, kindOf, settledLine } from "../model/classify.js";
import { agentName, cardOf, displayTitle, kindWord } from "../model/cards.js";
import { classifyRow } from "../model/timeline.js";
import { tint } from "./widgets.js";

const CORNER = 6;

const caption = (t, text) => div().text_size(10.5).text_color(t.dim).child(text);

// A centred caption between two hairlines: a day, a restart, a new
// conversation.
function divider(t, text) {
  const line = () => div().flex_1().h(1).bg(t.border);
  return h_flex().w_full().items_center().gap(10).child(line()).child(div().flex_none().text_size(11).text_color(t.muted).child(text)).child(line());
}

// A dashed line of plumbing or of the panel's own: a notice.
export function notice(t, text, code) {
  return h_flex()
    .w_full()
    .items_center()
    .gap(8)
    .px(10)
    .py(6)
    .rounded(8)
    .border(1)
    .border_dashed()
    .border_color(t.border)
    .when(Boolean(code), (el) => el.child(div().flex_none().text_size(10).font_semibold().text_color(t.dim).child(code)))
    .child(div().flex_1().text_size(11.5).text_color(t.muted).child(text));
}

// What you or the voice said: the voice's words are left, yours right.
function bubble(t, item) {
  const mine = kindOf(item) === "shepherd";
  const wrote = !mine && (item.via === "text_router" || item.via === "typed");
  const note = item.pending ? "Sending…" : mine ? (item.via === "typed" ? "Typed" : "") : wrote ? "Sheepdog wrote" : "";
  const body = div()
    .px(12)
    .py(8)
    .rounded(14)
    .when(Boolean(item.pending), (el) => el.opacity(0.6))
    .when(mine, (el) => el.rounded_br(CORNER).bg(t.accent).text_color(t.accent_foreground))
    .when(!mine, (el) => el.rounded_bl(CORNER).bg(t.raised).text_color(t.foreground))
    .text_size(13)
    .child(item.text);
  return h_flex()
    .w_full()
    .when(mine, (el) => el.justify_end())
    .child(
      v_flex()
        .gap(2)
        .max_w_4_5()
        .when(mine, (el) => el.items_end())
        .child(body)
        .when(Boolean(note), (el) => el.child(caption(t, note))),
    );
}

// An open question (or a push that interrupts): a one-line pointer in amber.
// It says who is waiting; the card that answers floats above the composer.
function pointer(t, item) {
  const asker = (item.agent ?? "").trim() || "An agent";
  const question = kindOf(item) === "approval";
  return v_flex()
    .w_full()
    .gap(3)
    .px(10)
    .py(8)
    .rounded(10)
    .bg(tint(t.blocked, "1f"))
    .border(1)
    .border_color(tint(t.blocked, "55"))
    .child(
      h_flex()
        .gap(7)
        .items_center()
        .child(div().size(8).rounded(4).flex_none().bg(t.blocked))
        .child(
          div()
            .flex_1()
            .min_w(0)
            .text_size(12.5)
            .font_semibold()
            .text_color(t.foreground)
            .child(question ? `${asker} asked` : item.text)
            .when(question, (el) => el.child(div().text_color(t.blocked).child(" — waiting for your answer"))),
        ),
    )
    .when(question && Boolean(item.text), (el) => el.child(div().pl(15).text_size(12).text_color(t.muted).child(item.text)));
}

// The well on a ledger line's leading edge, by what the line is.
function look(t, item) {
  if (kindOf(item) === "card") return { glyph: "▢", color: t.foreground, fill: t.hover, square: true };
  if (kindOf(item) === "push") return { glyph: ">_", color: t.working, fill: tint(t.working, "26") };
  switch (item.resolution) {
    case "confirmed":
      // Declining is your call, not an error: neutral, no red.
      return /^(no\b|deny|reject)/i.test((item.answer ?? "").trim())
        ? { glyph: "×", color: t.foreground, fill: t.hover }
        : { glyph: "✓", color: t.done, fill: tint(t.done, "26") };
    default:
      // Closed by itself: a hollow ring.
      return { glyph: "–", color: t.muted, fill: null, ring: t.border };
  }
}

// One line of the ledger. A line joined above or below drops that corner and
// its border, so a run is one card.
function ledgerLine(t, row, ledger, timeLabel) {
  const item = row.item;
  const w = look(t, item);
  const joinsAbove = ledger?.joinsAbove ?? false;
  const joinsBelow = ledger?.joinsBelow ?? false;
  const text = settledLine(item) ?? item.text;
  const card = cardOf(item);
  const agent = kindOf(item) === "push" ? (item.agent ?? "").trim() : "";
  return h_flex()
    .w_full()
    .items_center()
    .gap(10)
    .px(10)
    .py(8)
    .bg(t.raised)
    .border_l(1)
    .border_r(1)
    .when(!joinsAbove, (el) => el.border_t(1).rounded_tl(10).rounded_tr(10))
    .when(!joinsBelow, (el) => el.border_b(1).rounded_bl(10).rounded_br(10))
    .when(joinsAbove, (el) => el.border_t(1))
    .border_color(t.border)
    .child(
      div()
        .size(28)
        .flex_none()
        .flex()
        .items_center()
        .justify_center()
        .text_size(12)
        .font_semibold()
        .text_color(w.color)
        .when(w.square, (el) => el.rounded(7))
        .when(!w.square, (el) => el.rounded(14))
        .when(Boolean(w.fill), (el) => el.bg(w.fill))
        .when(Boolean(w.ring), (el) => el.border(1).border_color(w.ring))
        .child(w.glyph),
    )
    .child(
      v_flex()
        .flex_1()
        .min_w(0)
        .gap(1)
        .when(Boolean(agent), (el) => el.child(div().text_size(11).font_semibold().text_color(t.muted).child(agent)))
        .child(div().text_size(12.5).text_color(t.foreground).child(card ? displayTitle(card) : text))
        .child(caption(t, card ? [kindWord(card), agentName(card), timeLabel].filter(Boolean).join(" · ") : timeLabel)),
    )
    // A card that is still stored opens on a press.
    .when(Boolean(card), (el) => el.child(div().flex_none().text_size(14).text_color(t.dim).child("›")));
}

// One entry of the list: the row, the space above it, its place in a ledger
// run, and its time. `technical` adds the category's code in a gutter.
export function rowView(t, entry) {
  const { row, gap, ledger, timeLabel, technical } = entry;
  const kind = classifyRow(row);
  let body;
  if (row.type === "day") body = divider(t, entry.dayLabel);
  else if (kind.category === "context") body = divider(t, row.title ?? row.item.text);
  else if (kind.category === "system") body = notice(t, row.title ?? row.item.text, CATEGORIES.system.code);
  else if (kind.category === "dialogue") body = bubble(t, row.item);
  else if (kind.category === "needsYou") body = pointer(t, row.item);
  else if (kind.ledger) body = ledgerLine(t, row, ledger, timeLabel);
  else body = ledgerLine(t, row, null, timeLabel); // a card shown: an object of its own
  return h_flex()
    .w_full()
    .pt(gap)
    .px(12)
    .gap(6)
    .items_start()
    .when(technical, (el) => el.child(div().w(30).flex_none().pt(6).text_size(10).text_color(t.dim).child(CATEGORIES[kind.category].code)))
    .child(div().flex_1().min_w(0).child(body));
}
