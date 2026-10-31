// A card the orchestrator showed, opened over the timeline (the phone opens
// it as a sheet): a title, then what its kind carries. A `card` is markdown
// (headings in bold, lists, tables, code), an `excerpt` the agent's last screen
// lines, an `agent` card and an `approval` one the agent they are about, with a
// button that shows it in herdr-app. Display only: an approval card never
// answers; the floating question does.

import { div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { agentName, displayTitle, focusTarget, kindWord } from "../model/cards.js";
import { blocks, plainInline } from "../model/markdown.js";
import { summaryOf } from "../model/questions.js";
import { button } from "./widgets.js";

const mono = (t, text) => div().w_full().px(10).py(6).rounded(6).bg(t.raised).border(1).border_color(t.border).text_size(11.5).font_family("monospace").text_color(t.foreground).child(text);

// A table as rows of equal columns: a bold header over the body, in a card.
function table(t, block) {
  const align = (el, a) => (a === "trailing" ? el.justify_end().text_right() : a === "center" ? el.justify_center().text_center() : el);
  const row = (cells, bold, last) =>
    h_flex()
      .w_full()
      .when(!last, (el) => el.border_b(1).border_color(t.border))
      .children(
        cells.map((cell, i) =>
          align(h_flex().flex_1().min_w(0).px(8).py(5), block.alignments[i]).child(div().text_size(11.5).text_color(bold ? t.foreground : t.muted).when(bold, (el) => el.font_semibold()).child(plainInline(cell))),
        ),
      );
  return v_flex()
    .w_full()
    .rounded(8)
    .border(1)
    .border_color(t.border)
    .bg(t.raised)
    .child(row(block.header, true, block.rows.length === 0))
    .children(block.rows.map((cells, i) => row(cells, false, i === block.rows.length - 1)));
}

function list(t, block) {
  return v_flex()
    .w_full()
    .gap(3)
    .children(
      block.items.map((item) =>
        h_flex()
          .w_full()
          .gap(6)
          .pl(item.depth * 14)
          .items_start()
          .child(div().flex_none().text_size(12.5).text_color(t.muted).child(item.marker))
          .child(div().flex_1().min_w(0).text_size(12.5).text_color(t.foreground).child(plainInline(item.text))),
      ),
    );
}

function markdown(t, text) {
  return v_flex()
    .w_full()
    .gap(10)
    .children(
      blocks(text).map((block) => {
        switch (block.type) {
          case "heading": return div().text_size(13).font_semibold().text_color(t.foreground).child(plainInline(block.text));
          case "list": return list(t, block);
          case "code": return mono(t, block.text);
          case "table": return table(t, block);
          default: return div().text_size(12.5).text_color(t.foreground).child(plainInline(block.text));
        }
      }),
    );
}

// What the card's kind carries.
function body(t, card) {
  switch (card.kind) {
    case "card":
      return markdown(t, card.markdown ?? "");
    case "excerpt":
      return mono(t, card.text ?? "");
    case "approval":
      return v_flex()
        .w_full()
        .gap(8)
        .child(div().text_size(12.5).font_semibold().text_color(t.foreground).child(summaryOf(card)))
        .when(Boolean(card.command?.trim()), (el) => el.child(mono(t, card.command.trim())))
        .children((card.options ?? []).map((option) => div().text_size(12).text_color(t.muted).child(`${option.index}. ${option.title?.trim() || option.label}`)))
        .child(div().text_size(11.5).text_color(t.dim).child("Answer it in the question card."));
    default:
      return card.markdown ? markdown(t, card.markdown) : div().text_size(12.5).text_color(t.muted).child("This card has nothing to show.");
  }
}

// The sheet: a bar with the title and Close, the body, and the agent button.
// `note`: what the last focus said, when it could not.
export function cardSheet(t, card, { note, onFocus, onClose }) {
  const who = agentName(card);
  return v_flex()
    .flex_1()
    .min_h(0)
    .w_full()
    .child(
      h_flex()
        .w_full()
        .px(12)
        .py(8)
        .gap(8)
        .items_center()
        .border_b(1)
        .border_color(t.border)
        .child(
          v_flex()
            .flex_1()
            .min_w(0)
            .child(div().truncate().text_size(13).font_semibold().text_color(t.foreground).child(displayTitle(card)))
            .child(div().truncate().text_size(11).text_color(t.muted).child(who ? `${kindWord(card)} · ${who}` : kindWord(card))),
        )
        .child(button(t, "card-close", "Close", onClose)),
    )
    .child(div().id("card-body").flex_1().min_h(0).w_full().overflow_y_scroll().px(12).py(10).child(body(t, card)))
    .when(Boolean(focusTarget(card)), (el) =>
      el.child(
        v_flex()
          .w_full()
          .px(12)
          .py(8)
          .gap(4)
          .border_t(1)
          .border_color(t.border)
          .child(h_flex().child(button(t, "card-focus", who ? `Show ${who} in herdr` : "Show the agent in herdr", onFocus, true)))
          .when(Boolean(note), (e) => e.child(div().text_size(11.5).text_color(t.blocked).child(note))),
      ),
    );
}
