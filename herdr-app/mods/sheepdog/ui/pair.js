// Pair a phone: a QR drawn from /api/pair/qr.json's module matrix with divs
// (no file is written), the same link as plain text, and the expiry. Refresh
// asks /api/pair?new=1 and then draws the matrix for that one new code.

import { div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { qrRows } from "../model/qr.js";
import { errorText, mutedText, overlayButton, overlayFrame } from "./overlay-widgets.js";

const MODULE = 5;
const QR_DARK = "#000000";
const QR_LIGHT = "#ffffff";

function matrixView(rows) {
  return v_flex()
    .self_center()
    .p(8)
    .bg(QR_LIGHT)
    .children(
      rows.map((row) =>
        h_flex().children(row.map((module) => div().size(MODULE).flex_none().bg(module === "1" ? QR_DARK : QR_LIGHT))),
      ),
    );
}

export function pairOverlay(t, { form, onClose, onRefresh }) {
  let rows = [];
  let codeError = "";
  try {
    rows = qrRows(form.matrix);
  } catch (error) {
    codeError = error.message;
  }
  const body = v_flex()
    .id("sheepdog-pair")
    .flex_1()
    .min_h(0)
    .w_full()
    .overflow_y_scrollbar()
    .p(16)
    .gap(12)
    .items_center()
    .when(Boolean(form.error), (el) => el.child(errorText(t, form.error)))
    .when(Boolean(codeError), (el) => el.child(errorText(t, codeError)))
    .when(rows.length > 0, (el) => el.child(matrixView(rows)))
    .child(div().text_size(13).font_semibold().text_color(t.foreground).child("Scan this with the Sheepdog app"))
    .child(mutedText(t, "Open the iPhone app, choose Scan a new code, and point the camera here."))
    .child(mutedText(t, "This code works once and expires in 10 minutes."))
    .when(Boolean(form.link), (el) =>
      el.child(
        v_flex()
          .w_full()
          .gap(5)
          .p(10)
          .rounded(8)
          .bg(t.raised)
          .border(1)
          .border_color(t.border)
          .child(div().text_size(10.5).font_semibold().text_color(t.dim).child("Or enter this link"))
          .child(div().text_size(10.5).font_family("monospace").text_color(t.foreground).selectable().child(form.link)),
      ),
    );
  return overlayFrame(t, {
    id: "sheepdog-pair",
    title: "Pair a phone",
    subtitle: "No code is written to disk",
    onClose,
    body,
    footer: h_flex()
      .w_full()
      .px(14)
      .py(8)
      .gap(8)
      .justify_end()
      .border_t(1)
      .border_color(t.border)
      .child(overlayButton(t, "pair-refresh", form.loading ? "Refreshing…" : "New code", onRefresh, { disabled: form.loading }))
      .child(overlayButton(t, "pair-close", "Done", onClose, { primary: true })),
  });
}
