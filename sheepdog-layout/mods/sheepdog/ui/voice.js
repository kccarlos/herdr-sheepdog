// The voice controls under the panel's header (docs/dev/design/desktop-client.md,
// section 6.4): Talk, Mute, Stop and hold-to-talk, Use here when another
// device holds the voice, and what to do when Sheepdog.app is missing. What
// each button does and when it works is model/voice.js's; this only draws
// it. A button that cannot work now is dimmed and does nothing.

import { div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { button, command } from "./widgets.js";

// A press the mod does not stop drags the window, so a control stops it.
const stop = (_e, cx) => cx.stop_propagation();

const chip = (t, id, label, enabled) =>
  h_flex()
    .id(id)
    .h(26)
    .px(10)
    .items_center()
    .justify_center()
    .rounded(6)
    .border(1)
    .border_color(t.border)
    .text_size(12)
    .font_medium()
    .text_color(t.foreground)
    .when(enabled, (el) => el.cursor_pointer().hover((s) => s.bg(t.hover)))
    .when(!enabled, (el) => el.opacity(0.4))
    .child(label);

// `controls`: model/voice.js's voiceControls. `state`: the controller's
// snapshot ({ ptt, notice }). `on`: { talk, mute, stop, pttDown, pttUp,
// useHere }, each (cx) => void.
export function voiceBar(t, controls, state, on) {
  const press = (id, label, enabled, handler) => chip(t, id, label, enabled).on_mouse_down("left", stop).on_click(enabled ? (_e, cx) => handler(cx) : () => {});
  const hold = controls.ptt.enabled;
  return v_flex()
    .w_full()
    .px(12)
    .py(8)
    .gap(6)
    .border_b(1)
    .border_color(t.border)
    .child(
      h_flex()
        .w_full()
        .flex_wrap()
        .gap(6)
        .child(press("voice-talk", "Talk", controls.talk.enabled, on.talk))
        .child(press("voice-mute", "Mute", controls.mute.enabled, on.mute))
        .child(press("voice-stop", "Stop", controls.stop.enabled, on.stop))
        // Held: the mic is open from the press to the release.
        .child(
          chip(t, "voice-ptt", "Hold to talk", hold)
            .when(state.ptt, (el) => el.bg(t.solid).text_color(t.solid_foreground))
            .when(hold, (el) =>
              el
                .on_mouse_down("left", (_e, cx) => {
                  cx.stop_propagation();
                  on.pttDown(cx);
                })
                .on_mouse_up("left", (_e, cx) => on.pttUp(cx)),
            ),
        ),
    )
    .when(controls.useHere, (el) => el.child(h_flex().child(button(t, "voice-use-here", "Use here", controls.useHereEnabled ? (_e, cx) => on.useHere(cx) : () => {}).when(!controls.useHereEnabled, (s) => s.opacity(0.4)))))
    .when(Boolean(controls.hint), (el) => el.child(div().text_size(11.5).text_color(t.muted).child(controls.hint)))
    .when(Boolean(controls.install), (el) => el.child(command(t, controls.install)))
    .when(Boolean(state.notice), (el) => el.child(div().text_size(11.5).text_color(state.notice.tone === "error" ? t.blocked : t.muted).child(state.notice.text)));
}
