// What the overlays (Settings, pairing, agent details, New conversation) do
// when pressed. Each handler changes the form on `host` at once, with the
// handler's own `cx`, and does anything that waits (a request, a program)
// through `later`, using only the context that gives it, so no handler
// touches a `cx` after an `await`. `host` is the view: it has `http`,
// `settingsForm`, `pairForm`, `agentForm`, `agentName`, `resetForm`,
// `resetKey`, `newResetKey()`, `closeOverlaySelf()`, `exec`, `platform()` and
// `base` (the daemon address). Pure of herdr imports, so node tests it
// (model/overlay-actions.test.mjs).

import { agentDetails } from "./agents.js";
import { later } from "./later.js";
import { openSetup } from "./setup-link.js";
import {
  addLanguage as withLanguage,
  askToRemoveDevice,
  beginSettingsAction,
  brainCatalogFailed,
  brainCatalogLoaded,
  brainCatalogLoading,
  brainNeedsCatalog,
  brainSelectionMatches,
  cancelDeviceRemoval,
  finishSettingsAction,
  moveLanguage as withMovedLanguage,
  removeLanguage as withoutLanguage,
  setupOpened,
  settingsFailed,
  settingsLoadFinished,
  settingsLoaded,
  toggleBrainProvider,
  toggleSection,
} from "./settings.js";

export const words = (error) => error?.message || String(error || "It did not answer.");

export function setSettings(host, form, cx) {
  host.settingsForm = form;
  cx.notify();
}

export function loadSettings(host, cx) {
  setSettings(host, { ...host.settingsForm, loading: true, notice: "", alert: "" }, cx);
  return later(cx, async (cx) => {
    const { http } = host;
    const [state, voice, brain, settings, devices] = await Promise.allSettled([
      http.state(),
      http.voiceProviders(),
      http.brainModels(),
      http.settings(),
      http.devices(),
    ]);
    let form = host.settingsForm;
    for (const [part, result] of [["state", state], ["voice", voice], ["brain", brain], ["settings", settings], ["devices", devices]]) {
      form = result.status === "fulfilled" ? settingsLoaded(form, part, result.value) : settingsFailed(form, part, words(result.reason));
    }
    setSettings(host, settingsLoadFinished(form), cx);
  });
}

// One request that changes a part of the form: `call` makes it, `part` is the
// form part its answer replaces, and `notice` is shown when it worked.
function change(host, cx, { name, part, notice, call, done = (form) => form }) {
  setSettings(host, beginSettingsAction(host.settingsForm, name), cx);
  return later(cx, async (cx) => {
    try {
      const value = await call();
      let form = finishSettingsAction(host.settingsForm, name, { notice });
      if (part) form = settingsLoaded(form, part, value);
      setSettings(host, done(form, value), cx);
    } catch (error) {
      setSettings(host, finishSettingsAction(host.settingsForm, name, { error: words(error) }), cx);
    }
  });
}

export function chooseVoiceProvider(host, id, cx) {
  if (host.settingsForm.busy || id === host.settingsForm.voice?.provider) return;
  return change(host, cx, { name: "voice", part: "voice", notice: "Voice model changed.", call: () => host.http.setVoiceProvider(id) });
}

export function chooseBrainModel(host, provider, model, cx) {
  if (host.settingsForm.busy || brainSelectionMatches(host.settingsForm.brain, provider, model)) return;
  return change(host, cx, {
    name: "brain",
    part: "brain",
    notice: "Orchestrator changed.",
    call: () => host.http.setBrainModel(provider, model),
    done: (form) => ({ ...form, brainOpen: "" }),
  });
}

// A press on a provider's row: open it (fetching its models if the daemon has
// not sent them), or close it.
export function toggleBrainRow(host, id, cx) {
  const was = host.settingsForm.brainOpen;
  const form = toggleBrainProvider(host.settingsForm, id);
  if (form === host.settingsForm) return;
  const fetchCatalog = form.brainOpen === id && was !== id && brainNeedsCatalog(form, id);
  setSettings(host, fetchCatalog ? brainCatalogLoading(form, id) : form, cx);
  if (!fetchCatalog) return;
  return later(cx, async (cx) => {
    try {
      const catalog = await host.http.brainModels(id);
      setSettings(host, brainCatalogLoaded(host.settingsForm, id, catalog), cx);
    } catch (error) {
      setSettings(host, brainCatalogFailed(host.settingsForm, id, words(error)), cx);
    }
  });
}

export function chooseChattiness(host, level, cx) {
  if (host.settingsForm.busy || level === host.settingsForm.settings?.chattiness) return;
  return change(host, cx, { name: "settings", part: "settings", notice: "Reply style changed.", call: () => host.http.setSettings({ chattiness: level }) });
}

export function setLanguages(host, codes, cx) {
  if (host.settingsForm.busy) return;
  return change(host, cx, { name: "settings", part: "settings", notice: "Languages changed.", call: () => host.http.setSettings({ languages: codes }) });
}

const chosenLanguages = (host) => host.settingsForm.settings?.languages || [];

export function addLanguage(host, code, cx) {
  return setLanguages(host, withLanguage(chosenLanguages(host), code), cx);
}

export function removeLanguage(host, code, cx) {
  return setLanguages(host, withoutLanguage(chosenLanguages(host), code), cx);
}

export function moveLanguage(host, code, delta, cx) {
  return setLanguages(host, withMovedLanguage(chosenLanguages(host), code, delta), cx);
}

export function askRemoveDevice(host, id, cx) {
  setSettings(host, askToRemoveDevice(host.settingsForm, id), cx);
}

export function cancelRemoveDevice(host, cx) {
  setSettings(host, cancelDeviceRemoval(host.settingsForm), cx);
}

export function removeDevice(host, id, cx) {
  if (host.settingsForm.busy) return;
  const name = host.settingsForm.devices.find((device) => device.id === id)?.name || "The device";
  return change(host, cx, {
    name: "devices",
    notice: `${name} was unpaired.`,
    call: () => host.http.removeDevice(id),
    done: (form) => cancelDeviceRemoval(settingsLoaded(form, "devices", form.devices.filter((device) => device.id !== id))),
  });
}

export function toggleSettingsSection(host, id, cx) {
  setSettings(host, toggleSection(host.settingsForm, id), cx);
}

// "Open Setup" / "Add a key in Setup": the daemon's setup page in the browser.
export function openSetupPage(host, cx) {
  return later(cx, async (cx) => {
    const result = await openSetup({ exec: host.exec, platform: host.platform(), base: host.base });
    setSettings(host, setupOpened(host.settingsForm, result), cx);
  });
}

export function loadPair(host, cx, fresh) {
  host.pairForm = { ...host.pairForm, loading: true, error: "" };
  cx.notify();
  return later(cx, async (cx) => {
    try {
      // The first request may mint the code; the matrix request reuses it.
      const pair = await host.http.pair(fresh);
      const matrix = await host.http.pairQR();
      host.pairForm = { loading: false, error: "", link: String(pair.link || ""), matrix };
    } catch (error) {
      host.pairForm = { loading: false, error: words(error), link: "", matrix: null };
    }
    cx.notify();
  });
}

export function loadAgent(host, cx) {
  return later(cx, async (cx) => {
    try {
      const agents = await host.http.agents();
      host.agentForm = { loading: false, error: "", agent: agentDetails(agents, host.agentName) };
    } catch (error) {
      host.agentForm = { loading: false, error: words(error), agent: null };
    }
    cx.notify();
  });
}

export function resetConversation(host, cx) {
  if (host.resetForm.busy) return;
  host.resetForm = { busy: true, error: "" };
  cx.notify();
  return later(cx, async (cx) => {
    let answered = false;
    try {
      await host.http.reset(host.resetKey);
      host.closeOverlaySelf();
      return;
    } catch (error) {
      answered = error?.kind !== "down";
      host.resetForm = { busy: false, error: words(error) };
    }
    if (answered) host.resetKey = host.newResetKey();
    cx.notify();
  });
}
