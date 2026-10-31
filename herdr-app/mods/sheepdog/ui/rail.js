// The rail: what the docked block shrinks to when its sidebar is too narrow
// for the panel (model/layout.js, docs/dev/design/desktop-client.md, section
// 6.1): the logo, a dot for the voice and a badge for how many agents need
// you, like the shipped sidebars' compact column. Clicking it opens the
// panel as a window.

import { div } from "gpui-kit";
import { v_flex } from "gpui-base";
import { dot, logo, tint, toneColor } from "./widgets.js";

// The words the rail says for a status, shared by its tooltip and its body.
export function summary(status) {
  switch (status.phase) {
    case "loading": return "Looking for Sheepdog…";
    case "down": return "Sheepdog isn't running";
    case "setup": return "Setup needed";
    default: {
      const need = status.needsYou ? `, ${status.needsYou} need${status.needsYou === 1 ? "s" : ""} you` : "";
      return `${status.voice.label}${need}`;
    }
  }
}

export function railView(t, status, { onOpen }) {
  const color = status.phase === "ok" ? toneColor(t, status.voice.tone) : status.phase === "setup" ? t.blocked : t.dim;
  return v_flex()
    .id("rail")
    .size_full()
    .items_center()
    .pt(12)
    .gap(8)
    .bg(t.chrome)
    .cursor_pointer()
    .on_click(onOpen)
    .tooltip(summary(status))
    .child(logo(26))
    .child(dot(color))
    .when(status.needsYou > 0, (el) =>
      el.child(div().px(5).h(16).rounded(8).bg(tint(t.blocked, "26")).text_size(10.5).font_bold().text_color(t.blocked).flex().items_center().child(String(status.needsYou))),
    );
}

// A dashed line saying the stream is down and the panel is polling.
export const paused = (t) => div().px(10).py(6).rounded(8).border(1).border_dashed().border_color(t.border).text_size(11.5).text_color(t.muted).child("Live updates paused");

