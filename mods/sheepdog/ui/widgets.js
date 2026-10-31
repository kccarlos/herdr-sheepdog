// Small pieces both views use. Every colour is a `theme()` name, so dark and
// light both work (docs/dev/design/desktop-client.md, section 6.5).

import { div, image } from "gpui-kit";
import { h_flex } from "gpui-base";

// Referenced from the app's folder; the colour logo keeps its colours with
// image() (svg() would tint it to one).
export const LOGO = "mods/sheepdog/logo.svg";

export const logo = (size) => image(LOGO).w(size).h(size).flex_none();

// A theme colour with an alpha (a "#rrggbb" plus two hex digits).
export const tint = (color, alpha) => color + alpha;

// The colour of a tone name from model/status.js.
export const toneColor = (t, tone) => t[tone] ?? t.dim;

export const dot = (color, size = 8) => div().size(size).rounded(size / 2).flex_none().bg(color);

// A text button: primary is the theme's solid, else an outline.
export function button(t, id, label, onClick, primary = false) {
  return h_flex()
    .id(id)
    .h(28)
    .px(12)
    .items_center()
    .justify_center()
    .rounded(6)
    .cursor_pointer()
    .text_size(12.5)
    .font_medium()
    .when(primary, (el) => el.bg(t.solid).text_color(t.solid_foreground).hover((s) => s.opacity(0.88)))
    .when(!primary, (el) => el.border(1).border_color(t.border).text_color(t.foreground).hover((s) => s.bg(t.hover)))
    .on_click(onClick)
    .child(label);
}

// A command to copy by eye, in a box.
export const command = (t, text) => div().px(10).py(8).rounded(6).bg(t.raised).border(1).border_color(t.border).text_size(12).text_color(t.foreground).child(text);
