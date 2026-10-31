// The open questions as the phone floats them (docs/dev/design/conversation-
// timeline.md, section 15): a card per question with the agent, the words, the
// command and one button per option, or, when they do not fit
// (model/presentation.js), one bar that opens them over the timeline. Above the
// composer, not over the list: a list row may carry no handler. No option is
// filled, so none reads as already chosen; the first question has the amber
// border.

import { div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { labelOf, summaryOf } from "../model/questions.js";
import { button, dot, tint } from "./widgets.js";

// "Codex", or "An agent".
const askerOf = (q) => q.agent?.trim() || "An agent";

// One option: a full-width row with its label, a "Recommended" tag when the
// agent recommends it, and its description. A "No…" option's label is not red
// (declining is the user's call), only plain.
function optionRow(t, q, option, { sending, enabled, onAnswer }) {
  return h_flex()
    .id(`answer-${q.id}-${option.index}`)
    .w_full()
    .min_h(36)
    .px(12)
    .py(8)
    .gap(8)
    .items_center()
    .rounded(8)
    .bg(t.raised)
    .border(1)
    .border_color(sending ? t.blocked : t.border)
    .when(enabled, (el) => el.cursor_pointer().hover((s) => s.bg(t.hover)).on_click((_e, cx) => onAnswer(q, option, cx)))
    .when(!enabled && !sending, (el) => el.opacity(0.5))
    .child(
      v_flex()
        .flex_1()
        .min_w(0)
        .gap(2)
        .child(
          h_flex()
            .gap(8)
            .items_center()
            .flex_wrap()
            .child(div().min_w(0).text_size(12.5).font_semibold().text_color(t.foreground).child(labelOf(option)))
            .when(Boolean(option.recommended), (el) => el.child(div().px(6).rounded(8).bg(tint(t.blocked, "1f")).text_size(10).font_semibold().text_color(t.blocked).child("Recommended"))),
        )
        .when(Boolean(option.description?.trim()), (el) => el.child(div().text_size(11.5).text_color(t.muted).child(option.description.trim()))),
    )
    .when(sending, (el) => el.child(div().text_size(11).text_color(t.muted).child("Sending…")));
}

// One question's card. `card`: { pending, phase, option, outcome, spoken,
// message, error } from model/questions.js.
export function questionCard(t, card, { primary, enabled, onAnswer }) {
  const q = card.pending;
  const settled = card.phase === "answered" || card.phase === "gone";
  return v_flex()
    .w_full()
    .gap(8)
    .px(12)
    .py(10)
    .rounded(10)
    .bg(t.chrome)
    .border(primary ? 2 : 1)
    .border_color(card.phase === "answered" ? t.done : primary && !settled ? t.blocked : t.border)
    .child(
      h_flex()
        .gap(7)
        .items_center()
        .child(dot(settled ? t.done : t.blocked, 7))
        .child(div().text_size(12).font_semibold().text_color(t.foreground).child(settled ? `${askerOf(q)} asked` : `${askerOf(q)} needs your answer`)),
    )
    .child(div().text_size(12.5).font_semibold().text_color(t.foreground).child(summaryOf(q)))
    .when(Boolean(q.command?.trim()), (el) => el.child(div().px(10).py(6).rounded(6).bg(t.raised).text_size(11.5).font_family("monospace").text_color(t.foreground).child(q.command.trim())))
    .when(card.phase === "answered", (el) =>
      el.child(
        v_flex()
          .gap(2)
          .child(div().text_size(12).font_semibold().text_color(t.done).child(`✓ ${card.outcome}`))
          .when(Boolean(card.spoken), (e) => e.child(div().text_size(11.5).text_color(t.muted).child(card.spoken))),
      ),
    )
    .when(card.phase === "gone", (el) => el.child(div().text_size(12).text_color(t.muted).child(card.message)))
    .when(!settled, (el) =>
      el.child(
        v_flex()
          .w_full()
          .gap(6)
          .children((q.options ?? []).map((option) => optionRow(t, q, option, { sending: card.phase === "sending" && card.option?.index === option.index, enabled, onAnswer }))),
      ),
    )
    .when(Boolean(card.error), (el) => el.child(div().text_size(11.5).text_color(t.blocked).child(card.error)));
}

// The stack of cards, inline.
export function questionStack(t, cards, { busy, onAnswer }) {
  return v_flex()
    .w_full()
    .px(12)
    .pb(8)
    .gap(8)
    .children(cards.map((card, i) => questionCard(t, card, { primary: i === 0, enabled: card.phase === "open" && !busy, onAnswer })));
}

// The compact bar's sheet: the cards, scrolling, over the timeline.
export function questionSheet(t, cards, { busy, onAnswer, onClose }) {
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
        .child(div().flex_1().text_size(13).font_semibold().text_color(t.foreground).child(cards.length === 1 ? "A question for you" : "Questions for you"))
        .child(button(t, "questions-close", "Close", onClose)),
    )
    .child(div().id("questions-body").flex_1().min_h(0).w_full().overflow_y_scroll().py(10).child(questionStack(t, cards, { busy, onAnswer })));
}

// The compact bar:"Codex asked · Answer", or how many are waiting.
export function questionBar(t, cards, { onOpen }) {
  const waiting = cards.filter((c) => c.phase === "open" || c.phase === "sending");
  const first = cards[0].pending;
  const words = cards.length === 1 ? `${askerOf(first)} asked` : `${cards.length} questions need you`;
  return div()
    .w_full()
    .px(12)
    .pb(8)
    .child(
      h_flex()
        .id("question-bar")
        .w_full()
        .px(12)
        .h(36)
        .items_center()
        .gap(8)
        .rounded(10)
        .bg(tint(t.blocked, "1f"))
        .border(1)
        .border_color(tint(t.blocked, "55"))
        .cursor_pointer()
        .on_click(onOpen)
        .child(dot(waiting.length ? t.blocked : t.done, 8))
        .child(div().flex_1().truncate().text_size(12.5).font_semibold().text_color(t.foreground).child(words))
        .child(div().text_size(12).font_semibold().text_color(t.blocked).child("Answer")),
    );
}
