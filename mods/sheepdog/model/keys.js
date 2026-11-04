// The keyboard of the docked panel, once herdr-app hands it the keys
// (`"focusable": true`, a "focus" command from `pane.go`). What a key means is
// decided here and only here; main.js does what it says. Pure. No herdr
// imports.

// `key` is the key event's `key` ("enter", "escape", "1"…). `onPanel` is
// whether the panel itself holds the keys; in the composer the digits are
// text. `question` is the first open question card ({ pending, phase }) or
// null, `suggestions` the live suggested replies ({ options }) or null.
// `modified`: Cmd, Ctrl or Alt is down, which is the app's shortcut, not ours.
// `sheet`: a card or the questions are open over the list. `window`: this is
// the panel's own window, which Esc closes. `busy`: an answer is on its way,
// so a digit must not send a suggested reply instead. `active`: the panel
// shows its conversation (not the daemon-down or rail states).
// Returns what to do, or null when the key means nothing here:
//   { type: "composer" }                 Enter: write a message
//   { type: "answer", question, option } 1-9: tap that option of the question
//   { type: "send", text }               1-9: send that suggested reply
//   { type: "panel" }                    Esc in the composer: back to the panel
//   { type: "sheet" }                    Esc with a sheet open: close it
//   { type: "close" }                    Esc on the panel's window: close it
//   { type: "leave" }                    Esc on the panel: give the keys back
export function keyAction(key, { onPanel, question = null, suggestions = null, modified = false, sheet = false, window = false, busy = false, active = true }) {
  if (modified) return null;
  if (key === "escape") {
    if (sheet) return { type: "sheet" };
    if (!onPanel) return { type: "panel" };
    return { type: window ? "close" : "leave" };
  }
  if (!onPanel || !active) return null;
  if (key === "enter") return { type: "composer" };
  if (!/^[1-9]$/.test(key)) return null;
  const at = Number(key) - 1;
  if (question?.phase === "open") {
    const option = question.pending?.options?.[at];
    return option ? { type: "answer", question: question.pending, option } : null;
  }
  if (busy) return null;
  const text = suggestions?.options?.[at];
  return text ? { type: "send", text } : null;
}

// How `leave` hands the keys back. `pane.go left` starts at the panel only
// while herdr-app's `focused_block` is the panel, which only `pane.go` sets: a
// click into the panel or its composer leaves it unset, and then `pane.go`
// starts at the terminal beside the panel and goes nowhere. Focusing the pane
// that was focused before the panel (herdr's `focused()` keeps it) works in
// both cases. `focus` is herdr's `focused()` ({ pane, terminal } or null);
// returns { name, args } for `run`.
export function leaveCommand(focus) {
  if (focus?.pane) return { name: "pane.focus", args: { pane: focus.pane } };
  if (focus?.terminal) return { name: "pane.focus", args: { terminal: focus.terminal } };
  return { name: "pane.go", args: { direction: "left" } };
}
