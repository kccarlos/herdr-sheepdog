// The voice controls (docs/dev/design/desktop-client.md, sections 2 and 6.4):
// which buttons work in which state, what each sends, and the controller:
// `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { deriveStatus } from "./status.js";
import { appPaths, commandFor, createVoice, INSTALL_COMMAND, NO_VOICE, NOTICE_MS, PTT_MAX_MS, TALK_URL, voiceControls } from "./voice.js";

const phone = { name: "Test Phone", platform: "ios", since: "2026-10-30T10:00:00Z" };
const mac = { name: "Test Mac", platform: "macos", since: "2026-10-30T10:00:00Z" };
const voice = (extra = {}) => ({ mode: "gemini", status: "connected", muted: false, stalled: false, speaking: false, client_connected: false, ...extra });

// The controls for a daemon whose `voice` is `v`.
function controls(v, extra = {}) {
  const status = deriveStatus({ daemon: "up", detail: "", stream: "live", app: { voice: v, questions: [] } });
  return voiceControls({ phase: status.phase, voice: v, view: status.voice, ...extra });
}
const enabled = (c) => [c.talk.enabled, c.mute.enabled, c.stop.enabled, c.ptt.enabled];

test("the state table: which buttons work, for who holds the voice", () => {
  // Nobody holds it: Talk starts Sheepdog.app, nothing else works.
  const none = controls(voice());
  assert.deepEqual(enabled(none), [true, false, false, false]);
  assert.equal(none.talk.action, "use-here");
  assert.equal(none.useHere, false);

  // This Mac, listening: mute and hold-to-talk; Stop only while speaking.
  const listening = controls(voice({ client_connected: true, client: mac }));
  assert.deepEqual(enabled(listening), [false, true, false, true]);
  assert.equal(listening.useHere, false);
  assert.deepEqual(enabled(controls(voice({ client_connected: true, client: mac, speaking: true }))), [false, true, true, true]);

  // Muted: Talk unmutes, Mute is off.
  const muted = controls(voice({ client_connected: true, client: mac, muted: true }));
  assert.deepEqual(enabled(muted), [true, false, false, true]);
  assert.equal(muted.talk.action, "unmute");

  // The phone: the same controls, relayed, and Use here.
  const elsewhere = controls(voice({ client_connected: true, client: phone, speaking: true }));
  assert.deepEqual(enabled(elsewhere), [false, true, true, true]);
  assert.equal(elsewhere.useHere, true);
  assert.equal(elsewhere.useHereEnabled, true);
});

test("a daemon that does not name its holder is taken as a holder on this Mac", () => {
  const c = controls(voice({ client_connected: true }));
  assert.deepEqual(enabled(c), [false, true, false, true]);
  assert.equal(c.useHere, false);
});

test("no daemon, a setup problem or no voice: every button is off, and the voice off says why", () => {
  const off = { talk: false, mute: false, stop: false, ptt: false };
  for (const phase of ["loading", "down", "setup"]) {
    const c = voiceControls({ phase, voice: voice({ client_connected: true, client: mac }), view: { holder: { here: true }, elsewhere: false } });
    assert.deepEqual(enabled(c), Object.values(off), phase);
  }
  const none = controls(voice({ mode: "off" }));
  assert.deepEqual(enabled(none), Object.values(off));
  assert.match(none.hint, /voice is off/);
});

test("Sheepdog.app not installed: no voice on this Mac, with the brew command", () => {
  const c = controls(voice(), { installed: false });
  assert.deepEqual(enabled(c), [false, false, false, false]);
  assert.equal(c.install, "brew install --cask sheepdog-app");
  assert.equal(c.install, INSTALL_COMMAND);
  assert.match(c.hint, /not installed/);
  // Not looked yet is not a reason to turn Talk off.
  assert.equal(controls(voice(), { installed: null }).talk.enabled, true);
});

test("the phone holds the voice and the app is missing: its controls still work, Use here does not", () => {
  const c = controls(voice({ client_connected: true, client: phone }), { installed: false });
  assert.deepEqual(enabled(c), [false, true, false, true]);
  assert.equal(c.useHere, true);
  assert.equal(c.useHereEnabled, false);
  assert.equal(c.install, INSTALL_COMMAND);
});

test("a 409 seen (no voice) offers Use here, with Talk", () => {
  const c = controls(voice(), { noVoice: true });
  assert.equal(c.useHere, true);
  assert.equal(c.talk.action, "use-here");
});

test("each action maps to one command", () => {
  assert.deepEqual(commandFor("use-here"), { exec: ["open", [TALK_URL]] });
  assert.equal(TALK_URL, "sheepdog://talk");
  assert.deepEqual(commandFor("unmute"), { call: "mute", args: [false] });
  assert.deepEqual(commandFor("mute"), { call: "mute", args: [true] });
  assert.deepEqual(commandFor("stop"), { call: "stop", args: [] });
  assert.deepEqual(commandFor("ptt-down"), { call: "ptt", args: [true] });
  assert.deepEqual(commandFor("ptt-up"), { call: "ptt", args: [false] });
  assert.equal(commandFor("shutdown"), null);
});

// A controller over fakes: the http calls and exec runs are recorded.
function setup({ fail = {}, installed = [], execResult = { code: 0, stdout: "", stderr: "" } } = {}) {
  const log = [];
  let at = 1_000_000;
  const call = (name) => async (...args) => {
    log.push([name, ...args.slice(0, -1)]);
    if (fail[name]) throw fail[name];
    return { ok: true };
  };
  const http = { mute: call("mute"), stop: call("stop"), ptt: call("ptt") };
  const exec = async (command, args) => {
    log.push(["exec", command, args]);
    if (execResult instanceof Error) throw execResult;
    return execResult;
  };
  const seen = [];
  const exists = async (path) => {
    seen.push(path);
    return installed.includes(path);
  };
  let keys = 0;
  const voice = createVoice({ http, exec, exists, home: () => "/Users/test", newKey: () => `k${keys++}`, now: () => at, refresh: async () => log.push(["refresh"]) });
  return { voice, log, seen, advance: (ms) => (at += ms) };
}

test("mute, unmute and stop call the daemon with a key, then ask the state again", async () => {
  const { voice, log } = setup();
  await voice.send("mute");
  await voice.send("unmute");
  await voice.send("stop");
  assert.deepEqual(log, [["mute", true], ["refresh"], ["mute", false], ["refresh"], ["stop"], ["refresh"]]);
  assert.equal(voice.getState().notice, null);
});

test("Use here runs open sheepdog://talk, and says it is starting", async () => {
  const { voice, log } = setup();
  await voice.send("use-here");
  assert.deepEqual(log[0], ["exec", "open", ["sheepdog://talk"]]);
  assert.equal(voice.getState().notice.tone, "info");
  assert.equal(voice.getState().busy, null);
});

test("Use here that cannot start the app, or exits non-zero, says to install it", async () => {
  for (const execResult of [new Error("no such program"), { code: 1, stdout: "", stderr: "Unable to find application" }]) {
    const { voice } = setup({ execResult });
    await voice.send("use-here");
    assert.match(voice.getState().notice.text, /brew install --cask sheepdog-app/);
    assert.equal(voice.getState().notice.tone, "error");
  }
});

test("409 from the daemon is 'No voice is connected'; 503 is the daemon's reason; nothing answering is retry", async () => {
  const refused = (status, message) => Object.assign(new Error(message), { status });
  const none = setup({ fail: { stop: refused(409, "no voice client") } });
  await none.voice.send("stop");
  assert.equal(none.voice.getState().notice.text, NO_VOICE);
  assert.equal(none.voice.getState().notice.code, "no-voice");

  const off = setup({ fail: { mute: refused(503, "the voice is off (--voice=off)") } });
  await off.voice.send("mute");
  assert.equal(off.voice.getState().notice.text, "the voice is off (--voice=off)");

  const down = setup({ fail: { mute: new Error("nothing answered") } });
  await down.voice.send("mute");
  assert.match(down.voice.getState().notice.text, /did not answer/);
});

test("one tap at a time: a second while the first is on its way is dropped", async () => {
  const { voice, log } = setup();
  const first = voice.send("mute");
  await voice.send("stop");
  await first;
  assert.deepEqual(log.filter((l) => l[0] !== "refresh"), [["mute", true]]);
});

test("hold-to-talk sends on at the press and off at the release, in that order", async () => {
  const { voice, log } = setup();
  const down = voice.pttDown();
  const up = voice.pttUp();
  await Promise.all([down, up]);
  assert.deepEqual(log, [["ptt", true], ["ptt", false]]);
  assert.equal(voice.getState().ptt, false);
  // A release with nothing held, and a second press while held, send nothing.
  await voice.pttUp();
  await voice.pttDown();
  await voice.pttDown();
  assert.deepEqual(log.slice(2), [["ptt", true]]);
});

test("a hold nobody released is closed after PTT_MAX_MS", async () => {
  const { voice, log, advance } = setup();
  await voice.pttDown();
  advance(PTT_MAX_MS - 1);
  voice.prune();
  assert.equal(voice.getState().ptt, true);
  advance(1);
  voice.prune();
  await voice.pttUp();
  assert.deepEqual(log, [["ptt", true], ["ptt", false]]);
  assert.equal(voice.getState().ptt, false);
});

test("a hold the daemon refused is not held, and says why", async () => {
  const { voice } = setup({ fail: { ptt: Object.assign(new Error("no voice client"), { status: 409 }) } });
  await voice.pttDown();
  assert.equal(voice.getState().ptt, false);
  assert.equal(voice.getState().notice.text, NO_VOICE);
});

test("a note goes after NOTICE_MS", async () => {
  const { voice, advance } = setup({ fail: { stop: Object.assign(new Error("no voice client"), { status: 409 }) } });
  await voice.send("stop");
  advance(NOTICE_MS - 1);
  voice.prune();
  assert.notEqual(voice.getState().notice, null);
  advance(1);
  voice.prune();
  assert.equal(voice.getState().notice, null);
});

test("the app is looked for read-only in both Applications folders, at most every 30 seconds", async () => {
  assert.deepEqual(appPaths("/Users/test"), ["/Applications/Sheepdog.app", "/Users/test/Applications/Sheepdog.app"]);
  const none = setup();
  await none.voice.check();
  assert.equal(none.voice.getState().installed, false);
  assert.deepEqual(none.seen, appPaths("/Users/test"));
  await none.voice.check();
  assert.equal(none.seen.length, 2, "not again so soon");
  none.advance(30000);
  await none.voice.check();
  assert.equal(none.seen.length, 4);

  const user = setup({ installed: ["/Users/test/Applications/Sheepdog.app"] });
  await user.voice.check();
  assert.equal(user.voice.getState().installed, true);
});

test("a look that throws leaves the answer unknown", async () => {
  const voice = createVoice({ http: {}, exec: async () => ({ code: 0 }), exists: async () => { throw new Error("denied"); }, home: () => "/Users/test", newKey: () => "k" });
  await voice.check();
  assert.equal(voice.getState().installed, null);
});
