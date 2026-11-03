// The overlays' handlers never use a click's cx after it returned:
// `make test-panel`. herdr reports "cx is no longer valid" when a handler
// keeps its cx across an await; the fake below does the same.
import { test } from "node:test";
import assert from "node:assert/strict";
import * as actions from "./overlay-actions.js";
import { emptySettingsForm, settingsLoaded } from "./settings.js";

// A cx for one host call: valid until the call returns. `spawn` hands its
// body an async context that stays valid across awaits.
function clickCx(log) {
  const cx = {
    valid: true,
    notify() {
      if (!cx.valid) throw new Error("cx is no longer valid: it was captured during an earlier call and used later.");
      log.notifies++;
    },
    spawn(body) {
      const acx = { notify: () => { log.notifies++; log.async++; }, spawn: (b) => b(acx) };
      const task = Promise.resolve().then(() => body(acx));
      log.tasks.push(task);
      return task;
    },
  };
  return cx;
}

// Runs a handler the way herdr does: with a fresh cx, then the call returns
// and that cx is stale; waits for every task it started.
async function click(log, handler) {
  const cx = clickCx(log);
  try {
    handler(cx);
  } finally {
    cx.valid = false;
  }
  await Promise.all(log.tasks);
  // Tasks that started tasks.
  await new Promise((r) => setTimeout(r, 0));
  await Promise.all(log.tasks);
}

const brain = {
  provider: "gemini",
  model: "g-2",
  models_provider: "gemini",
  models: [{ name: "g-1" }, { name: "g-2" }],
  providers: [{ id: "gemini", label: "Gemini", available: true }, { id: "claude", label: "Claude", available: true }],
};

function fakeHost() {
  const calls = [];
  const ok = (name, value) => async (...args) => {
    calls.push([name, ...args]);
    await new Promise((r) => setTimeout(r, 1));
    return typeof value === "function" ? value(...args) : value;
  };
  const host = {
    http: {
      state: ok("state", {}),
      voiceProviders: ok("voiceProviders", { provider: "a", providers: [{ id: "a" }, { id: "b" }] }),
      brainModels: ok("brainModels", (id) => ({ ...brain, models_provider: id || "gemini", models: [{ name: "c-1" }] })),
      settings: ok("settings", { languages: ["en"], chattiness: "normal", levels: ["quiet", "normal"], language_options: [{ code: "en" }, { code: "de" }] }),
      devices: ok("devices", [{ id: "d1", name: "Phone" }]),
      setVoiceProvider: ok("setVoiceProvider", (id) => ({ provider: id, providers: [{ id }] })),
      setBrainModel: ok("setBrainModel", (provider, model) => ({ ...brain, provider, model, models_provider: provider })),
      setSettings: ok("setSettings", (patch) => ({ language_options: [], levels: [], chattiness: "normal", languages: [], ...patch })),
      removeDevice: ok("removeDevice", {}),
      pair: ok("pair", { link: "sheepdog://x" }),
      pairQR: ok("pairQR", { rows: [] }),
      agents: ok("agents", []),
      reset: ok("reset", {}),
    },
    settingsForm: settingsLoaded(settingsLoaded(emptySettingsForm(), "brain", brain), "devices", [{ id: "d1", name: "Phone" }]),
    pairForm: { loading: true, error: "", link: "", matrix: null },
    agentForm: { loading: true, error: "", agent: null },
    agentName: "x",
    resetForm: { busy: false, error: "" },
    resetKey: "k1",
    newResetKey: () => "k2",
    closed: 0,
    closeOverlaySelf() { this.closed++; },
    exec: async () => ({ code: 0, stdout: "", stderr: "" }),
    platform: () => "macos",
    base: "http://127.0.0.1:7777",
  };
  host.settingsForm = { ...host.settingsForm, settings: { languages: ["en"], chattiness: "quiet", levels: ["quiet", "normal"], language_options: [{ code: "en" }, { code: "de" }] }, voice: { provider: "a", providers: [{ id: "a" }, { id: "b" }] } };
  return { host, calls };
}

const handlers = {
  loadSettings: (h, cx) => actions.loadSettings(h, cx),
  chooseVoiceProvider: (h, cx) => actions.chooseVoiceProvider(h, "b", cx),
  chooseBrainModel: (h, cx) => actions.chooseBrainModel(h, "gemini", "g-1", cx),
  chooseBrainModelOtherProvider: (h, cx) => actions.chooseBrainModel(h, "claude", "c-1", cx),
  toggleBrainRow: (h, cx) => actions.toggleBrainRow(h, "claude", cx),
  chooseChattiness: (h, cx) => actions.chooseChattiness(h, "normal", cx),
  setLanguages: (h, cx) => actions.setLanguages(h, ["en", "de"], cx),
  addLanguage: (h, cx) => actions.addLanguage(h, "de", cx),
  removeLanguage: (h, cx) => actions.removeLanguage(h, "en", cx),
  moveLanguage: (h, cx) => actions.moveLanguage(h, "en", 1, cx),
  askRemoveDevice: (h, cx) => actions.askRemoveDevice(h, "d1", cx),
  cancelRemoveDevice: (h, cx) => actions.cancelRemoveDevice(h, cx),
  removeDevice: (h, cx) => actions.removeDevice(h, "d1", cx),
  toggleSettingsSection: (h, cx) => actions.toggleSettingsSection(h, "brain", cx),
  openSetupPage: (h, cx) => actions.openSetupPage(h, cx),
  loadPair: (h, cx) => actions.loadPair(h, cx, true),
  loadAgent: (h, cx) => actions.loadAgent(h, cx),
  resetConversation: (h, cx) => actions.resetConversation(h, cx),
};

test("every exported handler is covered below", () => {
  const exported = Object.keys(actions).filter((n) => !["words", "setSettings"].includes(n)).sort();
  const covered = new Set(Object.keys(handlers).map((n) => n.replace(/OtherProvider$/, "")));
  assert.deepEqual(exported.filter((n) => !covered.has(n)), []);
});

for (const [name, handler] of Object.entries(handlers)) {
  test(`${name} never touches a click's cx after the call returned`, async () => {
    const { host } = fakeHost();
    const log = { notifies: 0, async: 0, tasks: [] };
    // A stale cx throws inside the task, which would reject it.
    await click(log, (cx) => handler(host, cx));
    assert.ok(log.notifies > 0, "the view was told to redraw");
  });
}

test("a handler that waits finishes its work with the spawned context", async () => {
  const { host, calls } = fakeHost();
  const log = { notifies: 0, async: 0, tasks: [] };
  await click(log, (cx) => actions.chooseBrainModel(host, "claude", "c-1", cx));
  assert.deepEqual(calls[0], ["setBrainModel", "claude", "c-1"]);
  assert.ok(log.async > 0, "the answer was drawn through the spawned cx");
  assert.equal(host.settingsForm.brain.provider, "claude");
  assert.equal(host.settingsForm.brainOpen, "");
  assert.equal(host.settingsForm.busy, "");
  assert.equal(host.settingsForm.notice, "Orchestrator changed.");
});

test("a failed request shows its words through the spawned context too", async () => {
  const { host } = fakeHost();
  host.http.setVoiceProvider = async () => { throw new Error("no key"); };
  const log = { notifies: 0, async: 0, tasks: [] };
  await click(log, (cx) => actions.chooseVoiceProvider(host, "b", cx));
  assert.equal(host.settingsForm.errors.voice, "no key");
  assert.equal(host.settingsForm.busy, "");
});

test("opening another provider's row fetches its models once", async () => {
  const { host, calls } = fakeHost();
  await click({ notifies: 0, async: 0, tasks: [] }, (cx) => actions.toggleBrainRow(host, "claude", cx));
  assert.equal(host.settingsForm.brainOpen, "claude");
  assert.deepEqual(calls.filter((c) => c[0] === "brainModels"), [["brainModels", "claude"]]);
  assert.equal(host.settingsForm.brainCatalogs.claude.models[0].name, "c-1");
  // Close, then open again: the list is already here.
  await click({ notifies: 0, async: 0, tasks: [] }, (cx) => actions.toggleBrainRow(host, "claude", cx));
  assert.equal(host.settingsForm.brainOpen, "");
  await click({ notifies: 0, async: 0, tasks: [] }, (cx) => actions.toggleBrainRow(host, "claude", cx));
  assert.equal(calls.filter((c) => c[0] === "brainModels").length, 1);
});

test("Open Setup runs the platform's opener on the setup page and says when it fails", async () => {
  const { host } = fakeHost();
  const seen = [];
  host.exec = async (...a) => { seen.push(a); return { code: 0 }; };
  await click({ notifies: 0, async: 0, tasks: [] }, (cx) => actions.openSetupPage(host, cx));
  assert.deepEqual(seen, [["open", ["http://127.0.0.1:7777/setup"]]]);
  assert.equal(host.settingsForm.notice, "Setup opened in your browser.");
  host.exec = async () => { throw new Error("no such program"); };
  await click({ notifies: 0, async: 0, tasks: [] }, (cx) => actions.openSetupPage(host, cx));
  assert.match(host.settingsForm.alert, /http:\/\/127\.0\.0\.1:7777\/setup/);
});

test("New conversation closes its window when it worked, and a failed key is replaced", async () => {
  const { host } = fakeHost();
  await click({ notifies: 0, async: 0, tasks: [] }, (cx) => actions.resetConversation(host, cx));
  assert.equal(host.closed, 1);
  host.resetForm = { busy: false, error: "" };
  host.http.reset = async () => { throw new Error("busy"); };
  await click({ notifies: 0, async: 0, tasks: [] }, (cx) => actions.resetConversation(host, cx));
  assert.equal(host.resetForm.error, "busy");
  assert.equal(host.resetKey, "k2");
});
