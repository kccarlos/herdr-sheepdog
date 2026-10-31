// "New conversation" asks before clearing the orchestrator's and voice's
// context. Agents, open questions, promises and recorded history stay.

import { h_flex, v_flex } from "gpui-base";
import { errorText, mutedText, overlayButton, overlayFrame } from "./overlay-widgets.js";

export function resetOverlay(t, { busy, error, onClose, onReset }) {
  const body = v_flex()
    .id("sheepdog-new-conversation")
    .flex_1()
    .w_full()
    .p(16)
    .gap(10)
    .child(div().text_size(13).text_color(t.foreground).child("Start a new conversation?"))
    .child(mutedText(t, "This clears what Sheepdog and the voice remember of this conversation. Your agents, open questions, promises and history stay."))
    .when(Boolean(error), (el) => el.child(errorText(t, error)));
  return overlayFrame(t, {
    id: "sheepdog-new-conversation",
    title: "New conversation",
    subtitle: "",
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
      .child(overlayButton(t, "new-conversation-cancel", "Cancel", onClose))
      .child(overlayButton(t, "new-conversation-confirm", busy ? "Starting…" : "Start a new conversation", onReset, { primary: true, disabled: busy })),
  });
}
