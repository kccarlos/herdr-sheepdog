// What an /events record means for the panel. The panel does not fold events
// into items itself: the daemon rebuilds the conversation by its own rules
// (internal/web/conversation.go), so a record that changes what the panel
// shows only makes the store ask again. No herdr imports.

// "now": the conversation and the state may have changed. "later": a
// transcript fragment, which comes many times a second while someone talks and
// settles into one item, so it waits for a quiet moment. "state": only
// /api/state changed (who holds the voice, mute, speaking). Any other type
// (audio stats, usage, playback) changes nothing the panel shows.
const NOW = new Set([
  "daemon.start", "conversation.reset", "web.utterance", "card.shown",
  "voice.text", "voice.tool_result", "voice.push", "voice.push_held", "voice.push_dropped",
  "voice.resume", "voice.handle_dropped", "voice.closed",
]);
const LATER = new Set(["voice.transcript.in", "voice.transcript.out"]);
const STATE = new Set([
  "web.client.connect", "web.client.attach", "web.client.disconnect", "web.client.mute", "web.client.speaking",
  "voice.client", "voice.connect", "voice.connect_error", "voice.disconnected", "voice.muted",
  "voice.stalled", "voice.session_open", "voice.session_close", "voice.session_error",
  "voice.standby", "voice.provider_changed",
]);

export function eventEffect(type) {
  if (NOW.has(type) || type.startsWith("pending.")) return "now";
  if (LATER.has(type)) return "later";
  return STATE.has(type) ? "state" : null;
}

// The first dotted component of each type above: what `GET /events?kinds=`
// selects, so the stream carries only what the panel reads.
export const KINDS = [...new Set([...NOW, ...LATER, ...STATE, "pending.created"].map((type) => type.split(".")[0]))].sort();
