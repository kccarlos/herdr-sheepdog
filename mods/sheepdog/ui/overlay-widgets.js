// Shared pieces of the overlays opened from the panel. Plain forms: a title,
// labelled choices, and an error in the daemon's own words. Every colour is a
// theme() name.

import { div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { button } from "./widgets.js";

const stop = (_e, cx) => cx.stop_propagation();

export function overlayButton(t, id, label, onClick, { primary = false, disabled = false } = {}) {
  return button(t, id, label, disabled ? () => {} : onClick, primary).on_mouse_down("left", stop).when(disabled, (el) => el.opacity(0.45));
}

export function overlayFrame(t, { id, title, subtitle, onClose, body, footer }) {
  return v_flex()
    .id(`${id}-surface`)
    .size_full()
    .bg(t.chrome)
    .child(
      h_flex()
        .w_full()
        .px(14)
        .py(11)
        .gap(10)
        .items_center()
        .border_b(1)
        .border_color(t.border)
        .child(
          v_flex()
            .flex_1()
            .min_w(0)
            .child(div().text_size(14).font_semibold().text_color(t.foreground).child(title))
            .when(Boolean(subtitle), (el) => el.child(div().text_size(11.5).text_color(t.muted).child(subtitle))),
        )
        .child(overlayButton(t, `${id}-close`, "Close", onClose)),
    )
    .child(body)
    .when(Boolean(footer), (el) => el.child(footer));
}

// A section of a form that folds to its title and current choice (`summary`).
// `content` is { rows, footer, error } while open, else null.
export function foldSection(t, id, { title, summary, open, onToggle, content }) {
  return v_flex()
    .w_full()
    .gap(7)
    .child(
      h_flex()
        .id(`fold-${id}`)
        .w_full()
        .min_h(32)
        .px(9)
        .py(5)
        .gap(8)
        .items_center()
        .rounded(7)
        .border(1)
        .border_color(t.border)
        .bg(open ? t.raised : t.chrome)
        .cursor_pointer()
        .hover((s) => s.bg(t.hover))
        .on_click(onToggle)
        .child(div().w(12).flex_none().text_size(11).text_color(t.muted).child(open ? "▾" : "▸"))
        .child(div().text_size(12.5).font_semibold().text_color(t.foreground).child(title))
        .child(div().flex_1().min_w(0).text_size(11.5).text_color(t.muted).truncate().text_right().child(open ? "" : summary)),
    )
    .when(Boolean(content), (el) =>
      el
        .children(content.rows)
        .when(Boolean(content.footer), (e) => e.child(div().text_size(11).text_color(t.dim).child(content.footer)))
        .when(Boolean(content.error), (e) => e.child(errorText(t, content.error))),
    );
}

export function choice(t, id, { title, detail = "", selected = false, disabled = false, onClick, right = "" }) {
  return h_flex()
    .id(id)
    .w_full()
    .min_h(36)
    .px(9)
    .py(6)
    .gap(8)
    .items_center()
    .rounded(7)
    .border(1)
    .border_color(selected ? t.accent : t.border)
    .bg(selected ? t.selected : t.raised)
    .when(!disabled && Boolean(onClick), (el) => el.cursor_pointer().hover((s) => s.bg(t.hover)).on_click(onClick))
    .when(disabled, (el) => el.opacity(0.52))
    .child(
      v_flex()
        .flex_1()
        .min_w(0)
        .gap(1)
        .child(div().text_size(12.5).text_color(t.foreground).child(title))
        .when(Boolean(detail), (el) => el.child(div().text_size(11).text_color(t.muted).child(detail))),
    )
    .when(Boolean(selected || right), (el) => el.child(div().flex_none().text_size(11.5).font_semibold().text_color(selected ? t.accent : t.muted).child(selected ? "✓" : right)));
}

export function smallButton(t, id, label, onClick, { disabled = false } = {}) {
  return div()
    .id(id)
    .h(24)
    .px(8)
    .flex()
    .items_center()
    .rounded(6)
    .border(1)
    .border_color(t.border)
    .text_size(11)
    .font_medium()
    .text_color(t.foreground)
    .when(disabled, (el) => el.opacity(0.45))
    .when(!disabled, (el) => el.cursor_pointer().hover((s) => s.bg(t.hover)).on_click(onClick))
    .child(label);
}

export const errorText = (t, text) => div().text_size(11.5).text_color(t.blocked).child(text);

export const mutedText = (t, text) => div().text_size(11.5).text_color(t.muted).child(text);

export function valueBlock(t, label, value) {
  return v_flex()
    .w_full()
    .gap(2)
    .child(div().text_size(10.5).font_semibold().text_color(t.dim).child(label))
    .child(div().text_size(12.5).text_color(t.foreground).child(value));
}
