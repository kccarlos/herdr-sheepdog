// What the rail and the panel say about the daemon and the voice, from the
// store's snapshot: the states of docs/dev/design/desktop-client.md, section
// 6.4, that apply without sending anything. Words and a theme colour name
// per state; the views only draw them. No herdr imports.

// What the user calls the device that holds the voice, from the `platform` of
// its client-ready (docs/dev/ws-contract.md "Protocol version and handshake").
const DEVICES = { ios: "iPhone", macos: "this Mac", web: "a browser" };

// The /api/state `voice.client` holder, in words. `here` is this Mac's own
// Sheepdog.app, which holds the voice for the panel; every other is elsewhere.
export function holderOf(voice) {
  const client = voice?.client;
  if (!client) return null;
  return { name: DEVICES[client.platform] ?? (client.name || "another device"), here: client.platform === "macos" };
}

// What the voice is doing, in words, from /api/state's `voice`. `tone` is a
// theme colour name.
export function voiceView(voice) {
  const v = voice ?? {};
  const holder = holderOf(v);
  const elsewhere = holder != null && !holder.here;
  const base = { holder, elsewhere };
  if (v.mode === "off") return { ...base, label: "Voice off", note: "The daemon runs without a voice model.", tone: "dim" };
  if (v.status === "connecting" || v.status === "reconnecting") return { ...base, label: "Connecting", note: "", tone: "working" };
  if (v.status === "closed") return { ...base, label: "Voice not connected", note: "", tone: "dim" };
  if (v.stalled) return { ...base, label: "Waiting for the voice", note: "The model is slow to answer.", tone: "working" };
  if (!holder && !v.client_connected) {
    return { ...base, label: "Ready", note: "Open Sheepdog on your phone or computer to talk.", tone: "dim" };
  }
  const where = elsewhere ? `Voice on ${holder.name}` : "Listening";
  if (v.muted) return { ...base, label: elsewhere ? `${where}, muted` : "Muted", note: "", tone: "idle" };
  if (v.speaking) return { ...base, label: elsewhere ? `${where}, speaking` : "Speaking", note: "", tone: "working" };
  return { ...base, label: where, note: "", tone: "done" };
}

// The panel's one state. `phase`:
//  loading  nothing has answered yet
//  down     the daemon did not answer (or something else answered)
//  setup    the daemon is up but its voice says it needs setting up
//  ok       the daemon is up
// `paused`: the daemon answers but the live stream is down, so the panel is
// polling ("Live updates paused"). `needsYou`: open questions.
export function deriveStatus(snapshot) {
  const { daemon, detail, app, stream } = snapshot;
  const voice = voiceView(app?.voice);
  const needsYou = app?.questions?.length ?? 0;
  if (daemon === "unknown" && !app) return { phase: "loading", detail: "", paused: false, needsYou: 0, voice };
  if (daemon === "down") return { phase: "down", detail, paused: false, needsYou: 0, voice };
  const problem = app?.voice?.setup_needed || app?.voice?.problem;
  const paused = stream === "down";
  if (problem) return { phase: "setup", detail: app.voice.problem || "Run sheepdog setup to add a voice key.", paused, needsYou, voice };
  return { phase: "ok", detail: "", paused, needsYou, voice };
}
