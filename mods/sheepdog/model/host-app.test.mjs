// What the panel says when the daemon is up and nobody holds the voice, and
// the daemon-down actions: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { INSTALL_COMMAND, noHolderView, OPEN_COMMAND, RUNNING_COMMAND, pathExists, runningFrom, sheepdogPaths, sheepdogProgram, startDaemon } from "./host-app.js";
import { deriveStatus } from "./status.js";
import { voiceControls, commandFor, createVoice, RUN_CHECK_MS } from "./voice.js";

test("macOS: installed but not running offers to open the app", () => {
  const v = noHolderView({ platform: "macos", installed: true, running: false });
  assert.equal(v.label, "Sheepdog.app isn't running");
  assert.match(v.note, /Open it to talk from this Mac, or use your phone\./);
  assert.deepEqual(v.action, { id: "open-app", label: "Open Sheepdog.app" });
});

test("macOS: not installed points to the cask in the tap, and Open Setup", () => {
  const v = noHolderView({ platform: "macos", installed: false, running: false });
  assert.equal(v.install, "brew install --cask kccarlos/tap/sheepdog-app");
  assert.equal(v.install, INSTALL_COMMAND);
  assert.deepEqual(v.action, { id: "open-setup", label: "Open Setup" });
});

test("macOS: running but not connected yet is Connecting, with no button", () => {
  const v = noHolderView({ platform: "macos", installed: true, running: true });
  assert.match(v.label, /^Connecting/);
  assert.equal(v.tone, "working");
  assert.equal(v.action, null);
});

test("macOS before the look is done, and other systems, keep neutral wording", () => {
  assert.equal(noHolderView({ platform: "macos" }).label, "Ready");
  assert.equal(noHolderView({ platform: "macos" }).action, null);
  const linux = noHolderView({ platform: "linux", installed: false, running: false });
  assert.equal(linux.label, "Ready");
  assert.match(linux.note, /phone/);
  assert.doesNotMatch(linux.note, /computer/);
  assert.equal(linux.action, null);
  assert.equal(noHolderView().label, "Ready");
});

test("the status carries the host's view only when nobody holds the voice", () => {
  const host = { platform: "macos", installed: true, running: false };
  const snap = (voice) => ({ daemon: "up", detail: "", stream: "live", app: { voice, questions: [] } });
  const none = deriveStatus(snap({ mode: "gemini", status: "connected" }), host);
  assert.equal(none.voice.label, "Sheepdog.app isn't running");
  assert.equal(none.voice.noHolder, true);
  const held = deriveStatus(snap({ mode: "gemini", status: "connected", client_connected: true, client: { platform: "ios", name: "x" } }), host);
  assert.equal(held.voice.label, "Voice on iPhone");
  assert.equal(held.voice.noHolder, undefined);
});

test("the controls carry the action and the note", () => {
  const voice = { mode: "gemini", status: "connected", client_connected: false };
  const host = { platform: "macos", installed: true, running: false };
  const status = deriveStatus({ daemon: "up", detail: "", stream: "live", app: { voice, questions: [] } }, host);
  const c = voiceControls({ phase: "ok", voice, view: status.voice, installed: true });
  assert.equal(c.action.id, "open-app");
  assert.match(c.hint, /Open it to talk/);
  const missing = deriveStatus({ daemon: "up", detail: "", stream: "live", app: { voice, questions: [] } }, { platform: "macos", installed: false, running: false });
  const m = voiceControls({ phase: "ok", voice, view: missing.voice, installed: false });
  assert.equal(m.action.id, "open-setup");
  assert.equal(m.install, INSTALL_COMMAND);
});

test("the look is an exact-name pgrep, and its exit code is the answer", () => {
  assert.deepEqual(RUNNING_COMMAND, ["pgrep", ["-x", "Sheepdog"]]);
  assert.deepEqual(OPEN_COMMAND, ["open", ["-a", "Sheepdog"]]);
  assert.equal(runningFrom({ code: 0 }), true);
  assert.equal(runningFrom({ code: 1 }), false);
  assert.equal(runningFrom({ code: 2 }), null);
  assert.equal(runningFrom(undefined), null);
  assert.deepEqual(commandFor("open-app"), { exec: OPEN_COMMAND });
});

function controller(codes) {
  let at = 1_000_000;
  const log = [];
  const exec = async (command, args) => {
    log.push([command, args]);
    if (command !== "pgrep") return { code: 0, stdout: "", stderr: "" };
    if (codes.length === 0) throw new Error("no pgrep");
    return { code: codes.shift(), stdout: "", stderr: "" };
  };
  const voice = createVoice({ http: {}, exec, exists: async () => true, home: () => "/h", newKey: () => "k", now: () => at, refresh: async () => {}, openSetup: async () => ({ ok: true }) });
  return { voice, log, advance: (ms) => (at += ms) };
}

test("running is looked at only while watched, at most every few seconds, and follows the app", async () => {
  const { voice, log, advance } = controller([1, 0]);
  voice.prune(false);
  await new Promise((r) => setImmediate(r));
  assert.equal(log.filter((l) => l[0] === "pgrep").length, 0, "not watched, not looked");
  voice.prune(true);
  await new Promise((r) => setImmediate(r));
  assert.equal(voice.getState().running, false);
  voice.prune(true);
  await new Promise((r) => setImmediate(r));
  assert.equal(log.filter((l) => l[0] === "pgrep").length, 1, "too soon");
  advance(RUN_CHECK_MS);
  voice.prune(true);
  await new Promise((r) => setImmediate(r));
  assert.equal(voice.getState().running, true);
});

test("a pgrep that cannot run leaves running unknown", async () => {
  const { voice } = controller([]);
  voice.prune(true);
  await new Promise((r) => setImmediate(r));
  assert.equal(voice.getState().running, null);
});

test("Open Sheepdog.app runs open -a Sheepdog and looks again at once", async () => {
  const { voice, log } = controller([1, 0]);
  voice.prune(true);
  await new Promise((r) => setImmediate(r));
  await voice.send("open-app");
  assert.deepEqual(log.find((l) => l[0] === "open"), ["open", ["-a", "Sheepdog"]]);
  voice.prune(true);
  await new Promise((r) => setImmediate(r));
  assert.equal(voice.getState().running, true, "no wait for the next interval");
});

test("Open Setup uses the setup opener; a failure is a note", async () => {
  const bad = createVoice({ http: {}, exec: async () => ({ code: 0 }), exists: async () => true, home: () => "/h", newKey: () => "k", openSetup: async () => ({ ok: false, error: "Open http://x/setup yourself" }) });
  await bad.send("open-setup");
  assert.match(bad.getState().notice.text, /yourself/);
});

test("Start Sheepdog runs `sheepdog start`; failures say what to do, and never reject", async () => {
  const calls = [];
  assert.deepEqual(await startDaemon(async (...a) => (calls.push(a), { code: 0 })), { ok: true, text: "Starting Sheepdog…" });
  assert.deepEqual(calls[0], ["sheepdog", ["start"]]);
  assert.equal((await startDaemon(async () => ({ code: 1 }))).ok, false);
  const thrown = await startDaemon(async () => { throw new Error("ENOENT"); });
  assert.equal(thrown.ok, false);
  assert.match(thrown.text, /Run setup/);
});

test("Start Sheepdog falls back to the known install places when herdr's PATH lacks sheepdog", async () => {
  const seen = [];
  const exec = async (command, args) => {
    seen.push([command, args]);
    if (command === "sheepdog") throw new Error("cannot run sheepdog: No such file or directory");
    return { code: 0 };
  };
  const exists = async (path) => path === "/opt/homebrew/bin/sheepdog" || path === "/usr/local/bin/sheepdog";
  assert.deepEqual(await startDaemon(exec, { exists, home: "/Users/me" }), { ok: true, text: "Starting Sheepdog…" });
  assert.deepEqual(seen, [["sheepdog", ["start"]], ["/opt/homebrew/bin/sheepdog", ["start"]]], "PATH first, then the first place that exists");
  assert.deepEqual(sheepdogPaths("/Users/me/"), ["/Users/me/.local/bin/sheepdog", "/opt/homebrew/bin/sheepdog", "/usr/local/bin/sheepdog"]);
});

test("Start Sheepdog prefers PATH, and says so when nothing is installed anywhere", async () => {
  const seen = [];
  await startDaemon(async (c) => (seen.push(c), { code: 0 }), { exists: async () => true, home: "/h" });
  assert.deepEqual(seen, ["sheepdog"], "no look at the disk when PATH works");
  const down = await startDaemon(async () => { throw new Error("nope"); }, { exists: async () => false, home: "/h" });
  assert.equal(down.ok, false);
  assert.match(down.text, /Run setup/);
  const brokenPlace = await startDaemon(async (c) => { throw new Error(c); }, { exists: async () => true, home: "/h" });
  assert.equal(brokenPlace.ok, false);
  const failing = await startDaemon(async (c) => { if (c === "sheepdog") throw new Error("x"); return { code: 1 }; }, { exists: async (p) => p.startsWith("/usr"), home: "/h" });
  assert.match(failing.text, /did not start/);
});

test("Run setup uses a known install place, else the bare name", async () => {
  assert.equal(await sheepdogProgram({ exists: async (p) => p === "/h/.local/bin/sheepdog", home: "/h" }), "/h/.local/bin/sheepdog");
  assert.equal(await sheepdogProgram({ exists: async () => false, home: "/h" }), "sheepdog");
  assert.equal(await sheepdogProgram({ exists: async () => { throw new Error("denied"); }, home: "/h" }), "sheepdog");
});

test("the install places outside the home folder are found with test -e when fs/promises refuses them", async () => {
  const exists = async (path) => {
    if (!path.startsWith("/Users/me/")) throw new Error("outside every granted read root");
    return false;
  };
  const seen = [];
  const exec = async (command, args) => {
    seen.push([command, ...args]);
    if (command === "sheepdog") throw new Error("cannot run sheepdog");
    if (command === "test") return { code: args[1] === "/opt/homebrew/bin/sheepdog" ? 0 : 1 };
    return { code: 0 };
  };
  assert.deepEqual(await startDaemon(exec, { exists, home: "/Users/me" }), { ok: true, text: "Starting Sheepdog…" });
  assert.deepEqual(seen.at(-1), ["/opt/homebrew/bin/sheepdog", "start"]);
  assert.equal(await sheepdogProgram({ exists, exec, home: "/Users/me" }), "/opt/homebrew/bin/sheepdog");
});
