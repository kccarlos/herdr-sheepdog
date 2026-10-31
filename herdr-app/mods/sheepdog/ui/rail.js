// The rail: the slim block the panel docks as (docs/dev/design/desktop-client.md,
// section 6.1). It says whether the voice is listening and who holds it, how
// many agents need you, and when the daemon is down or needs setting up;
// clicking it opens the panel. Under 160 px wide it is a compact column, like
// the shipped sidebars.

import { div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { button, command, dot, logo, tint, toneColor } from "./widgets.js";

// Narrower than this draws the compact column.
export const COMPACT_BELOW = 160;

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

function compact(t, status, onOpen) {
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

// What the rail says below the header, by state.
function body(t, status, retryMs) {
  switch (status.phase) {
    case "loading":
      return [div().text_size(12).text_color(t.dim).child("Looking for Sheepdog…")];
    case "down":
      return [
        div().text_size(13).font_semibold().text_color(t.foreground).child("Sheepdog isn't running"),
        div().text_size(12).text_color(t.muted).child(status.detail || "Start it with:"),
        command(t, "sheepdog setup"),
        div().text_size(11).text_color(t.dim).child(`Looking again every ${retryMs / 1000} seconds.`),
      ];
    case "setup":
      return [
        div().text_size(13).font_semibold().text_color(t.blocked).child("Setup needed"),
        div().text_size(12).text_color(t.muted).child(status.detail),
      ];
    default:
      return [
        h_flex().gap(8).items_center().child(dot(toneColor(t, status.voice.tone), 9)).child(div().text_size(13).font_semibold().text_color(t.foreground).child(status.voice.label)),
        ...(status.voice.note ? [div().text_size(12).text_color(t.muted).child(status.voice.note)] : []),
        status.needsYou > 0
          ? div().text_size(12).font_medium().text_color(t.blocked).child(`${status.needsYou} agent${status.needsYou === 1 ? "" : "s"} need${status.needsYou === 1 ? "s" : ""} you`)
          : div().text_size(12).text_color(t.dim).child("Nothing is waiting on you."),
      ];
  }
}

// A dashed line saying the stream is down and the panel is polling.
export const paused = (t) => div().px(10).py(6).rounded(8).border(1).border_dashed().border_color(t.border).text_size(11.5).text_color(t.muted).child("Live updates paused");

function wide(t, status, { onOpen, retryMs }) {
  return v_flex()
    .id("rail")
    .size_full()
    .bg(t.chrome)
    .p(12)
    .gap(10)
    .child(h_flex().gap(8).items_center().child(logo(22)).child(div().text_size(13).font_semibold().text_color(t.foreground).child("Sheepdog")))
    .children(body(t, status, retryMs))
    .when(status.paused, (el) => el.child(paused(t)))
    .child(h_flex().child(button(t, "open-panel", "Open panel", onOpen, status.phase === "ok" && status.needsYou > 0)));
}

export function railView(t, status, { width, onOpen, retryMs }) {
  return width < COMPACT_BELOW ? compact(t, status, onOpen) : wide(t, status, { onOpen, retryMs });
}
