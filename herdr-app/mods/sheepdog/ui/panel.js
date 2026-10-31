// The panel: the one block, docked in the sidebar or opened as a window over
// the layout (docs/dev/design/desktop-client.md, sections 6.1 and 6.4). Top to
// bottom: the status header (the voice and who holds it, how many agents need
// you), a banner for the states that qualify it (daemon down, setup needed,
// live updates paused, "put me in the sidebar"), the voice controls, the
// floating question card, the composer with the suggested replies, and the
// timeline, newest first, filling the rest; a card or the questions can open
// over the timeline. Settings are a later PR.

import { div, list } from "gpui-kit";
import { h_flex, v_flex, Scrollbar } from "gpui-base";
import { cardSheet } from "./cards.js";
import { composerView } from "./composer.js";
import { questionBar, questionSheet, questionStack } from "./questions.js";
import { notice, rowView } from "./rows.js";
import { paused } from "./rail.js";
import { voiceBar } from "./voice.js";
import { button, command, dot, logo, toneColor } from "./widgets.js";

// What a window opens at.
export const WINDOW = { width: 420, height: 640 };

// The header: the logo, the needs-you count, and the window's own controls on
// the first line; the voice (and who holds it) and Technical on the second. A
// press the mod does not stop drags a window, so the controls stop it.
function header(t, status, { technical, onToggleTechnical, onClose }) {
  const stop = (_e, cx) => cx.stop_propagation();
  const control = (id, label, onClick) =>
    div().id(id).px(8).h(24).flex().items_center().rounded(6).cursor_pointer().text_size(11.5).text_color(t.muted).hover((s) => s.bg(t.hover)).on_mouse_down("left", stop).on_click(onClick).child(label);
  const ok = status.phase === "ok";
  return v_flex()
    .w_full()
    .px(12)
    .py(10)
    .gap(6)
    .border_b(1)
    .border_color(t.border)
    .child(
      h_flex()
        .w_full()
        .gap(8)
        .items_center()
        .child(logo(20))
        .child(div().flex_1().min_w(0).text_size(13).font_semibold().text_color(t.foreground).child("Sheepdog"))
        .when(ok && status.needsYou > 0, (el) =>
          el.child(div().flex_none().px(8).h(20).flex().items_center().rounded(10).bg(t.blocked).text_size(11).font_semibold().text_color(t.solid_foreground).child(`${status.needsYou} need${status.needsYou === 1 ? "s" : ""} you`)),
        )
        .when(Boolean(onClose), (el) => el.child(control("close", "✕", onClose))),
    )
    .child(
      h_flex()
        .w_full()
        .gap(6)
        .items_center()
        .when(ok, (el) =>
          el.child(
            h_flex()
              .flex_1()
              .min_w(0)
              .gap(6)
              .items_center()
              .child(dot(toneColor(t, status.voice.tone), 7))
              .child(div().truncate().text_size(11.5).text_color(t.muted).child(status.voice.label)),
          ),
        )
        .when(!ok, (el) => el.child(div().flex_1()))
        .child(control("technical", technical ? "Hide technical" : "Technical", onToggleTechnical)),
    );
}

// The window opened without the docked block: where the panel belongs. The
// mod cannot dock itself (herdr-app changes a layout only through a layout you
// own), so this says what to do, and the button does the one step it can: copy
// the shown layout, which makes it yours.
function dockHint(t, { note, onCopyLayout }) {
  return v_flex()
    .w_full()
    .p(12)
    .gap(6)
    .border_b(1)
    .border_color(t.border)
    .child(div().text_size(12.5).font_semibold().text_color(t.foreground).child("Put Sheepdog in the sidebar"))
    .child(div().text_size(12).text_color(t.muted).child("This window is the fallback. Docked, Sheepdog is this panel in your sidebar, and a narrow sidebar shrinks it to a small rail."))
    .child(div().text_size(12).text_color(t.muted).child("A layout that came with herdr-app cannot be changed. Copy it to make it yours (herdr-app switches to the copy and closes this window), then add the block for the mod sheepdog on its right side (herdr-app docks a mod the first time it sees the folder, into a layout you own)."))
    .child(h_flex().child(button(t, "dock-copy-layout", "Copy my layout", onCopyLayout)))
    .when(Boolean(note), (el) => el.child(div().text_size(11.5).text_color(t.blocked).child(note)));
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
// bottom was reached: the panel pages back. A press on a row is the list's
// `on_item_click`, with the row's key.
function timeline(t, { entries, canLoadOlder, loadingOlder, onEndShown, onOpenRow }) {
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
      )
        .size_full()
        .on_item_click(onOpenRow),
    )
    .child(Scrollbar.vertical("timeline").absolute().inset_0());
}

// An empty conversation, or one that is still loading.
function empty(t, text) {
  return div().flex_1().flex().items_center().justify_center().px(24).text_size(12.5).text_color(t.dim).child(text);
}

// `paging`: { synced, canLoadOlder, loadingOlder } of the history. `compose`:
// what ui/composer.js draws. `questions`: { cards, presentation, busy,
// onAnswer, onOpen } for the floating questions (ui/questions.js). `voice`:
// { controls, state, on } for the voice bar (ui/voice.js). `dock`: the
// "put Sheepdog in the sidebar" hint of a window that is not the docked
// block's, { note, onCopyLayout }, else null. `sheet`: what is open over the
// list, or null: { type: "card", card, note, onFocus, onClose } or { type:
// "questions", onClose }.
export function panelView(t, { status, entries, paging, technical, retryMs, compose, questions, voice, dock, sheet, onSetup, onToggleTechnical, onClose, onEndShown, onOpenRow }) {
  const up = status.phase === "ok" || status.phase === "setup";
  let body;
  if (status.phase === "loading") body = empty(t, "Looking for Sheepdog…");
  else if (status.phase === "down") body = down(t, status, retryMs, onSetup);
  else if (sheet?.type === "card") body = cardSheet(t, sheet.card, sheet);
  else if (sheet?.type === "questions") body = questionSheet(t, questions.cards, { busy: questions.busy, onAnswer: questions.onAnswer, onClose: sheet.onClose });
  else if (!paging.synced) body = empty(t, "Loading the conversation…");
  else if (entries.length === 0) body = empty(t, "Nothing has been said yet.");
  else body = timeline(t, { entries, ...paging, onEndShown, onOpenRow });
  const floating = up && sheet?.type !== "questions" && questions.cards.length > 0;
  return v_flex()
    .id("sheepdog-panel")
    .size_full()
    .bg(t.chrome)
    .child(header(t, status, { technical, onToggleTechnical, onClose }))
    .when(Boolean(dock), (el) => el.child(dockHint(t, dock)))
    .when(status.phase === "setup", (el) => el.child(setup(t, status, onSetup)))
    .when(status.paused && status.phase !== "down", (el) => el.child(div().w_full().px(12).pt(8).child(paused(t))))
    .when(status.phase === "ok", (el) => el.child(voiceBar(t, voice.controls, voice.state, voice.on)))
    // The question floats at the top, where it cannot be missed.
    .when(floating && questions.presentation === "inline", (el) => el.child(div().w_full().pt(8).child(questionStack(t, questions.cards, questions))))
    .when(floating && questions.presentation === "compact", (el) => el.child(div().w_full().pt(8).child(questionBar(t, questions.cards, questions))))
    .when(up, (el) => el.child(composerView(t, compose)))
    .child(body);
}
