// The composer under the question card, above the timeline (docs/dev/design/
// desktop-client.md, sections 6.1 and 6.3): the suggested replies the orchestrator offered (only the live
// ones), failed or queued outbox items with inline Retry / Edit / Remove
// controls, the textarea, and what the last send said. The textarea's state is
// made once in main.js (it needs a live host call) and handed in; Enter sends
// (or queues while busy), Shift+Enter is a new line (model/composer.js decides).

import { div } from "gpui-kit";
import { h_flex, Textarea, v_flex } from "gpui-base";
import { isLight } from "../model/palette.js";
import { button, stopDrag } from "./widgets.js";

function smallAction(t, id, label, onClick, primary = false) {
  return h_flex()
    .id(id)
    .h(22)
    .px(8)
    .items_center()
    .justify_center()
    .rounded(5)
    .cursor_pointer()
    .text_size(11)
    .font_medium()
    .when(primary, (el) => el.bg(t.solid).text_color(t.solid_foreground).hover((s) => s.opacity(0.88)))
    .when(!primary, (el) => el.border(1).border_color(t.border).text_color(t.foreground).hover((s) => s.bg(t.hover)))
    .on_mouse_down("left", stopDrag)
    .on_click(onClick)
    .child(label);
}

// Interactive rows for failed and queued outbox items. Rendered here rather
// than inside timeline `list()` because GPUI list rows only deliver item-level
// clicks, not child button clicks.
function outboxItems(t, entries, { busy, onRetry, onEdit, onRemove }) {
  const hasFailed = entries.some((e) => e.phase === "failed");
  return v_flex()
    .w_full()
    .gap(6)
    .children(
      entries.map((entry) => {
        const failed = entry.phase === "failed";
        const canSendNow = !busy && (failed || !hasFailed);
        return v_flex()
          .w_full()
          .px(10)
          .py(7)
          .gap(5)
          .rounded(8)
          .bg(t.raised)
          .border(1)
          .border_color(failed ? t.blocked : t.border)
          .child(
            h_flex()
              .w_full()
              .gap(6)
              .items_center()
              .child(
                div()
                  .flex_none()
                  .text_size(10.5)
                  .font_semibold()
                  .text_color(failed ? t.blocked : t.muted)
                  .child(failed ? "Not sent" : "Queued"),
              )
              .child(div().flex_1().min_w(0).truncate().text_size(12).text_color(t.foreground).child(entry.text)),
          )
          .when(failed && Boolean(entry.error), (el) =>
            el.child(div().w_full().text_size(11).text_color(t.muted).child(entry.error)),
          )
          .child(
            h_flex()
              .gap(6)
              .items_center()
              .when(failed || canSendNow, (el) =>
                el.child(
                  smallAction(
                    t,
                    `outbox-retry-${entry.key}`,
                    failed ? "Retry" : "Send",
                    busy ? () => {} : (_e, cx) => onRetry?.(entry.key, cx),
                    failed,
                  ).when(busy, (b) => b.opacity(0.5)),
                ),
              )
              .child(smallAction(t, `outbox-edit-${entry.key}`, "Edit", (_e, cx) => onEdit?.(entry.key, cx)))
              .child(smallAction(t, `outbox-remove-${entry.key}`, "Remove", (_e, cx) => onRemove?.(entry.key, cx))),
          );
      }),
    );
}

// The live suggested replies as buttons that send the option as a typed line.
// `busy`: another line is on its way, so the buttons wait.
function suggestions(t, live, { busy, compact, onSend }) {
  const buttons = live.options.map((option, i) =>
    button(t, `suggest-${live.replyId}-${i}`, option, busy ? () => {} : (_e, cx) => onSend(option, cx)).when(busy, (el) => el.opacity(0.5)),
  );
  if (compact) {
    return h_flex()
      .w_full()
      .min_w(0)
      .gap(6)
      .items_center()
      .child(div().flex_none().text_size(10.5).text_color(t.dim).child("Suggested"))
      .child(div().flex_1().min_w(0).overflow_x_scroll().child(h_flex().gap(6).children(buttons)));
  }
  return v_flex()
    .w_full()
    .gap(4)
    .child(div().text_size(10.5).text_color(t.dim).child("Suggested replies"))
    .child(
      h_flex()
        .w_full()
        .flex_wrap()
        .gap(6)
        .children(buttons),
    );
}

// `notice`: { tone: "error" | "info", text } of the last send; `busy`: a line
// is waiting for its reply. `entries`: outbox entries ("failed" and "queued"
// show inline controls). `compact`: the vertical budget is short, so the
// suggestions take one row and scroll sideways instead of wrapping. `maxHeight`
// is the share of the vertical budget reserved for it: a multiline draft or
// wrapped note scrolls here instead of pushing the timeline down.
export function composerView(t, { state, live, entries = [], notice, busy, compact, maxHeight, onSend, onRetry, onEdit, onRemove }) {
  const pending = entries.filter((e) => e.phase === "failed" || e.phase === "queued");
  const hasFailed = pending.some((e) => e.phase === "failed");
  const showNotice = notice && !(notice.tone === "error" && hasFailed);
  const hint = busy ? "Waiting for the reply · Enter queues a follow-up" : "Enter sends · Shift+Enter for a new line";
  const input = isLight(t.chrome) ? t.raised : t.input;
  return v_flex()
    .w_full()
    .flex_none()
    .max_h(maxHeight)
    .min_h(0)
    .overflow_y_scroll()
    .px(12)
    .py(10)
    .gap(8)
    .border_b(1)
    .border_color(t.border)
    .when(Boolean(live), (el) => el.child(suggestions(t, live, { busy, compact, onSend })))
    .when(pending.length > 0, (el) => el.child(outboxItems(t, pending, { busy, onRetry, onEdit, onRemove })))
    .child(div().w_full().px(10).py(6).rounded(8).bg(input).border(1).border_color(t.border).text_size(13).text_color(t.foreground).on_mouse_down("left", stopDrag).child(Textarea.new(state).w_full()))
    .child(
      showNotice
        ? div().text_size(11.5).text_color(notice.tone === "error" ? t.blocked : t.muted).child(notice.text)
        : div().text_size(10.5).text_color(t.dim).child(hint),
    );
}
