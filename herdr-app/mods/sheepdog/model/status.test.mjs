// The states the rail and the panel show without sending anything
// (docs/dev/design/desktop-client.md, section 6.4), and which daemon events
// make the panel ask again: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { deriveStatus, voiceView } from "./status.js";
import { eventEffect, KINDS } from "./events.js";

const voice = (extra = {}) => ({ mode: "gemini", status: "connected", muted: false, stalled: false, speaking: false, client_connected: false, ...extra });
const snap = (extra = {}) => ({ daemon: "up", detail: "", stream: "live", app: { voice: voice(), questions: [] }, ...extra });

test("nothing has answered yet: loading, not an error", () => {
  assert.equal(deriveStatus({ daemon: "unknown", detail: "", stream: "connecting", app: null }).phase, "loading");
});

test("daemon down: it says why only when something else answered", () => {
  const refused = deriveStatus(snap({ daemon: "down", app: null, stream: "down" }));
  assert.equal(refused.phase, "down");
  assert.equal(refused.detail, "");
  const other = deriveStatus(snap({ daemon: "down", detail: "Something answered, but it is not sheepdog.", app: null }));
  assert.match(other.detail, /not sheepdog/);
  assert.equal(other.needsYou, 0);
});

test("a voice problem is setup needed, in the daemon's words", () => {
  const status = deriveStatus(snap({ app: { voice: voice({ setup_needed: true, problem: "Setup needed: no Gemini API key yet." }), questions: [] } }));
  assert.equal(status.phase, "setup");
  assert.equal(status.detail, "Setup needed: no Gemini API key yet.");
  const bare = deriveStatus(snap({ app: { voice: voice({ setup_needed: true }), questions: [] } }));
  assert.match(bare.detail, /sheepdog setup/);
});

test("the stream down while the daemon answers is paused, not down", () => {
  const status = deriveStatus(snap({ stream: "down" }));
  assert.equal(status.phase, "ok");
  assert.equal(status.paused, true);
  assert.equal(deriveStatus(snap({ stream: "connecting" })).paused, false, "starting up is not a pause");
  assert.equal(deriveStatus(snap()).paused, false);
});

test("agents that need you are the open questions", () => {
  const app = { voice: voice(), questions: [{ id: "a" }, { id: "b" }] };
  assert.equal(deriveStatus(snap({ app })).needsYou, 2);
});

test("the voice, in words", () => {
  const view = (v) => voiceView(voice(v));
  assert.equal(view({}).label, "Ready");
  assert.equal(view({ mode: "off" }).label, "Voice off");
  assert.equal(view({ status: "reconnecting" }).label, "Connecting");
  assert.equal(view({ status: "closed" }).label, "Voice not connected");
  assert.equal(view({ stalled: true }).label, "Waiting for the voice");
  assert.equal(view({ client_connected: true }).label, "Listening", "a holder that did not say who it is");
});

test("who holds the voice: elsewhere says where, this Mac is just listening", () => {
  const phone = { name: "sheepdog-ios", platform: "ios", since: "2026-09-22T08:00:00Z" };
  const mac = { name: "sheepdog-mac", platform: "macos", since: "2026-09-22T08:00:00Z" };
  const page = { name: "sheepdog-voice", platform: "web", since: "2026-09-22T08:00:00Z" };
  const elsewhere = voiceView(voice({ client_connected: true, client: phone }));
  assert.equal(elsewhere.label, "Voice on iPhone");
  assert.equal(elsewhere.elsewhere, true);
  assert.equal(voiceView(voice({ client_connected: true, client: page })).label, "Voice on a browser");
  assert.equal(voiceView(voice({ client_connected: true, client: { name: "kiosk", platform: "tv" } })).label, "Voice on kiosk");
  const here = voiceView(voice({ client_connected: true, client: mac }));
  assert.equal(here.label, "Listening");
  assert.equal(here.elsewhere, false);
});

test("muted and speaking show on the label and the tone", () => {
  const phone = { name: "sheepdog-ios", platform: "ios" };
  const mac = { name: "sheepdog-mac", platform: "macos" };
  const label = (client, extra) => voiceView(voice({ client_connected: true, client, ...extra })).label;
  assert.equal(label(mac, { muted: true }), "Muted");
  assert.equal(label(mac, { speaking: true }), "Speaking");
  assert.equal(label(phone, { muted: true }), "Voice on iPhone, muted");
  assert.equal(label(phone, { speaking: true }), "Voice on iPhone, speaking");
  assert.equal(voiceView(voice({ client_connected: true, client: mac, muted: true })).tone, "idle");
  assert.equal(voiceView(voice({ client_connected: true, client: mac })).tone, "done");
});

test("events: what changes the conversation, only the state, or nothing", () => {
  assert.equal(eventEffect("pending.created"), "now");
  assert.equal(eventEffect("pending.confirmed"), "now");
  assert.equal(eventEffect("web.utterance"), "now");
  assert.equal(eventEffect("daemon.start"), "now");
  assert.equal(eventEffect("voice.transcript.in"), "later");
  assert.equal(eventEffect("voice.transcript.out"), "later");
  assert.equal(eventEffect("web.client.attach"), "state");
  assert.equal(eventEffect("voice.muted"), "state");
  assert.equal(eventEffect("web.client.stats"), null);
  assert.equal(eventEffect("voice.playback"), null);
  assert.equal(eventEffect("worker.title"), null);
});

test("the stream asks only for the kinds those events come from", () => {
  assert.deepEqual(KINDS, ["card", "conversation", "daemon", "pending", "voice", "web"]);
});
