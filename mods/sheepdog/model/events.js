// What an /events record means for the panel. The daemon rebuilds the
// conversation by its own rules (internal/web/conversation.go), so a record
// that changes what the panel shows makes the store ask again; the words of a
// transcript, a typed line or a push are also shown at once from the event
// (model/fragments.js) and only checked later.
// "now": the conversation and the state may have changed; ask. "settle": the
// event's words are already shown; ask once things have been quiet for a
// moment, to let the daemon's own item replace the shown one. "state": only
// /api/state changed (who holds the voice, mute, speaking, an agent's state or
// attention, a space mute). Any other type (audio stats, usage, playback)
// changes nothing the panel shows.
const NOW = new Set([
  "daemon.start", "conversation.reset", "card.shown",
  "voice.push_held", "voice.push_dropped",
  "voice.resume", "voice.handle_dropped", "voice.closed",
]);
const SETTLE = new Set([
  "web.utterance", "voice.text", "voice.push", "voice.transcript.in", "voice.transcript.out", "voice.tool_result",
  "pending.created", "pending.confirmed", "pending.expired", "pending.cancelled", "pending.replaced",
]);
const STATE = new Set([
  "web.client.connect", "web.client.attach", "web.client.disconnect", "web.client.mute", "web.client.speaking",
  "voice.client", "voice.connect", "voice.connect_error", "voice.disconnected", "voice.muted",
  "voice.stalled", "voice.session_open", "voice.session_close", "voice.session_error",
  "voice.standby", "voice.provider_changed",
  // An agent's state moved (older daemons say nothing else), its attention or
  // its group changed, a space was muted: the status line's counts follow. A
  // held question promoted (a kept promise, a finished instruction) becomes
  // the voice's question with no other event.
  "herdr.transition", "agent.attention", "space.muted", "pending.promoted",
]);

export function eventEffect(type) {
  if (NOW.has(type)) return "now";
  if (SETTLE.has(type)) return "settle";
  return STATE.has(type) ? "state" : null;
}

// The first dotted component of each type above: what `GET /events?kinds=`
// selects, so the stream carries only what the panel reads.
export const KINDS = [...new Set([...NOW, ...SETTLE, ...STATE].map((type) => type.split(".")[0]))].sort();
