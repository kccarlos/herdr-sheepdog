// The composer at the bottom of the panel (docs/dev/design/desktop-client.md,
// section 6.3): the suggested replies the orchestrator offered (only the live
// ones), a note under them, the textarea, and what the last send said. The
// textarea's state is made once in main.js (it needs a live host call) and
// handed in; Enter sends, Shift+Enter is a new line (model/composer.js decides).

import { div } from "gpui-kit";
import { h_flex, Textarea, v_flex } from "gpui-base";
import { button } from "./widgets.js";

// The live suggested replies as buttons that send the option as a typed line.
// `busy`: another line is on its way, so the buttons wait.
function suggestions(t, live, { busy, onSend }) {
  return v_flex()
    .w_full()
    .gap(4)
    .child(div().text_size(10.5).text_color(t.dim).child("Suggested replies"))
    .child(
      h_flex()
        .w_full()
        .flex_wrap()
        .gap(6)
        .children(
          live.options.map((option, i) =>
            button(t, `suggest-${live.replyId}-${i}`, option, busy ? () => {} : (_e, cx) => onSend(option, cx)).when(busy, (el) => el.opacity(0.5)),
          ),
        ),
    );
}

// `notice`: { tone: "error" | "info", text } of the last send; `busy`: a line
// is waiting for its reply.
export function composerView(t, { state, live, notice, busy, onSend }) {
  const hint = busy ? "Waiting for the reply…" : "Enter sends · Shift+Enter for a new line";
  return v_flex()
    .w_full()
    .px(12)
    .py(10)
    .gap(8)
    .border_t(1)
    .border_color(t.border)
    .when(Boolean(live), (el) => el.child(suggestions(t, live, { busy, onSend })))
    .child(div().w_full().px(10).py(6).rounded(8).bg(t.input).border(1).border_color(t.border).text_size(13).text_color(t.foreground).child(Textarea.new(state).w_full()))
    .child(
      notice
        ? div().text_size(11.5).text_color(notice.tone === "error" ? t.blocked : t.muted).child(notice.text)
        : div().text_size(10.5).text_color(t.dim).child(hint),
    );
}
