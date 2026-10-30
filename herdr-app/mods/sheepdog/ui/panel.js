// The panel: a window over the layout with the read-only timeline
// (docs/dev/design/desktop-client.md, sections 6.1 and 6.4). A header with the
// voice, a banner for the states that replace or qualify the timeline (daemon
// down, setup needed, live updates paused), and the list. Sending, the voice
// controls and Settings are later PRs; this shows what the daemon says.

import { div, list } from "gpui-kit";
import { h_flex, v_flex, Scrollbar } from "gpui-base";
import { notice, rowView } from "./rows.js";
import { paused } from "./rail.js";
import { button, command, dot, logo, toneColor } from "./widgets.js";

// What a window opens at.
export const WINDOW = { width: 420, height: 640 };

// The header: the logo, the voice (and who holds it), and the window's own
// controls. A press the mod does not stop drags the window, so the controls
// stop it.
function header(t, status, { technical, onToggleTechnical, onClose }) {
  const stop = (_e, cx) => cx.stop_propagation();
  const control = (id, label, onClick) =>
    div().id(id).px(8).h(24).flex().items_center().rounded(6).cursor_pointer().text_size(11.5).text_color(t.muted).hover((s) => s.bg(t.hover)).on_mouse_down("left", stop).on_click(onClick).child(label);
  const ok = status.phase === "ok";
  return h_flex()
    .w_full()
    .px(12)
    .py(10)
    .gap(8)
    .items_center()
    .border_b(1)
    .border_color(t.border)
    .child(logo(20))
    .child(
      v_flex()
        .flex_1()
        .min_w(0)
        .child(div().text_size(13).font_semibold().text_color(t.foreground).child("Sheepdog"))
        .when(ok, (el) =>
          el.child(
            h_flex()
              .gap(6)
              .items_center()
              .child(dot(toneColor(t, status.voice.tone), 7))
              .child(div().truncate().text_size(11.5).text_color(t.muted).child(status.voice.label)),
          ),
        ),
    )
    .when(ok && status.needsYou > 0, (el) =>
      el.child(div().px(8).h(20).flex().items_center().rounded(10).bg(t.blocked).text_size(11).font_semibold().text_color(t.solid_foreground).child(`${status.needsYou} need${status.needsYou === 1 ? "s" : ""} you`)),
    )
    .child(control("technical", technical ? "Hide technical" : "Technical", onToggleTechnical))
    .when(Boolean(onClose), (el) => el.child(control("close", "✕", onClose)));
}

// Daemon down: the whole body is the explanation.
function down(t, status, retryMs, onSetup) {
  return v_flex()
    .p(16)
    .gap(10)
    .child(div().text_size(14).font_semibold().text_color(t.foreground).child("Sheepdog isn't running"))
    .child(div().text_size(12.5).text_color(t.muted).child(status.detail || "Sheepdog is the daemon that lets you manage your agents by voice. Nothing answered on this computer."))
    .child(div().text_size(12.5).text_color(t.muted).child("Start it with:"))
    .child(command(t, "sheepdog setup"))
    .child(h_flex().child(button(t, "run-setup", "Run it in a tab", onSetup, true)))
    .child(div().text_size(11.5).text_color(t.dim).child(`Looking again every ${retryMs / 1000} seconds.`));
}

// Setup needed: the daemon's own words.
function setup(t, status, onSetup) {
  return v_flex()
    .w_full()
    .p(12)
    .gap(8)
    .border_b(1)
    .border_color(t.border)
    .child(div().text_size(13).font_semibold().text_color(t.blocked).child("Setup needed"))
    .child(div().text_size(12.5).text_color(t.muted).child(status.detail))
    .child(h_flex().child(button(t, "run-setup", "Run sheepdog setup", onSetup, true)));
}

// The list, newest first (model/timeline.js says why): the rows, then a last
// row that says whether older history is coming. `list()` draws only what is
// on screen and asks each row by index, so drawing that last row means the
// bottom was reached: the panel pages back.
function timeline(t, { entries, canLoadOlder, loadingOlder, onEndShown }) {
  const end = () => {
    onEndShown();
    return div().w_full().px(12).py(12).child(notice(t, loadingOlder ? "Loading earlier…" : canLoadOlder ? "Earlier messages" : "This is the start of the conversation"));
  };
  return v_flex()
    .relative()
    .flex_1()
    .min_h(0)
    .w_full()
    .child(
      list(
        "timeline",
        entries.length + 1,
        (i) => (i === entries.length ? "end" : entries[i].row.id),
        (i) => (i === entries.length ? end() : rowView(t, entries[i])),
      ).size_full(),
    )
    .child(Scrollbar.vertical("timeline").absolute().inset_0());
}

// An empty conversation, or one that is still loading.
function empty(t, text) {
  return div().flex_1().flex().items_center().justify_center().px(24).text_size(12.5).text_color(t.dim).child(text);
}

// `paging`: { synced, canLoadOlder, loadingOlder } of the history.
export function panelView(t, { status, entries, paging, technical, retryMs, onSetup, onToggleTechnical, onClose, onEndShown }) {
  let body;
  if (status.phase === "loading") body = empty(t, "Looking for Sheepdog…");
  else if (status.phase === "down") body = down(t, status, retryMs, onSetup);
  else if (!paging.synced) body = empty(t, "Loading the conversation…");
  else if (entries.length === 0) body = empty(t, "Nothing has been said yet.");
  else body = timeline(t, { entries, ...paging, onEndShown });
  return v_flex()
    .id("sheepdog-panel")
    .size_full()
    .bg(t.chrome)
    .child(header(t, status, { technical, onToggleTechnical, onClose }))
    .when(status.phase === "setup", (el) => el.child(setup(t, status, onSetup)))
    .when(status.paused && status.phase !== "down", (el) => el.child(div().w_full().px(12).pt(8).child(paused(t))))
    .child(body);
}
