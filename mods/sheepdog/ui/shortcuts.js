// The keyboard shortcuts sheet, opened over the timeline by `?` on the panel
// or the `?` control in the header, and closed by `?`, `Esc`, or Close.

import { div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { button } from "./widgets.js";

function keyBadge(t, label) {
  return div()
    .flex_none()
    .px(7)
    .py(2)
    .rounded(5)
    .bg(t.raised)
    .border(1)
    .border_color(t.border)
    .text_size(11)
    .font_family("monospace")
    .text_color(t.foreground)
    .child(label);
}

function shortcutRow(t, keys, description) {
  return h_flex()
    .w_full()
    .py(5)
    .gap(10)
    .items_start()
    .justify_between()
    .child(div().flex_1().min_w(0).text_size(12).text_color(t.foreground).child(description))
    .child(keyBadge(t, keys));
}

function section(t, title, rows, note = null) {
  return v_flex()
    .w_full()
    .gap(4)
    .child(div().text_size(11).font_semibold().text_color(t.muted).child(title))
    .child(
      v_flex()
        .w_full()
        .px(10)
        .py(6)
        .rounded(8)
        .bg(t.chrome)
        .border(1)
        .border_color(t.border)
        .children(rows),
    )
    .when(Boolean(note), (el) => el.child(div().text_size(11).text_color(t.dim).child(note)));
}

export function shortcutsSheet(t, { platform, onClose }) {
  const focusChord = platform === "macos" ? "⌘⇧S" : "Ctrl+Shift+S";
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
            .child(div().truncate().text_size(13).font_semibold().text_color(t.foreground).child("Keyboard shortcuts"))
            .child(div().truncate().text_size(11).text_color(t.muted).child("Press ? or Esc to close")),
        )
        .child(button(t, "shortcuts-close", "Close", onClose)),
    )
    .child(
      div()
        .id("shortcuts-body")
        .flex_1()
        .min_h(0)
        .w_full()
        .overflow_y_scroll()
        .px(12)
        .py(10)
        .child(
          v_flex()
            .w_full()
            .gap(12)
            .child(
              section(
                t,
                "Anywhere in herdr-app",
                [shortcutRow(t, focusChord, "Focus Sheepdog panel (or open window from narrow rail)")],
                "Opt-in during sheepdog setup, or bind \"sheepdog.focus\" in ~/.config/herdr-app/keys.json. Directional pane.go into the sidebar also focuses the panel.",
              ),
            )
            .child(
              section(t, "When the panel has focus", [
                shortcutRow(t, "Enter", "Write a message in the composer"),
                shortcutRow(t, "1–9", "Pick an open question option or suggested reply"),
                shortcutRow(t, "?", "Show or hide keyboard shortcuts"),
                shortcutRow(t, "Esc", "Close open sheet/window, or return to your pane"),
              ]),
            )
            .child(
              section(t, "In the message composer", [
                shortcutRow(t, "Enter", "Send message (or queue while a turn runs)"),
                shortcutRow(t, "Shift+Enter", "New line"),
                shortcutRow(t, "Esc", "Return focus to the panel"),
              ]),
            ),
        ),
    );
}
