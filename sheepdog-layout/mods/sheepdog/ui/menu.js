// The panel header's menu. Each row opens its own overlay.

import { div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";

const item = (t, id, label, detail, onClick) =>
  h_flex()
    .id(id)
    .w_full()
    .min_h(38)
    .px(10)
    .gap(8)
    .items_center()
    .rounded(7)
    .cursor_pointer()
    .hover((s) => s.bg(t.hover))
    .on_click(onClick)
    .child(
      v_flex()
        .flex_1()
        .min_w(0)
        .child(div().text_size(12.5).text_color(t.foreground).child(label))
        .when(Boolean(detail), (el) => el.child(div().text_size(10.5).text_color(t.muted).child(detail))),
    )
    .child(div().text_size(13).text_color(t.dim).child("›"));

export function menuOverlay(t, { onSettings, onPair, onReset }) {
  return v_flex()
    .id("sheepdog-menu")
    .w_full()
    .p(5)
    .bg(t.popover)
    .child(item(t, "menu-settings", "Settings", "Models, languages, devices and updates", onSettings))
    .child(item(t, "menu-pair", "Pair a phone", "Show a fresh pairing code", onPair))
    .child(item(t, "menu-new-conversation", "New conversation", "Clear Sheepdog's working memory", onReset));
}
