// The panel's Settings form: daemon-owned choices, paired devices and the
// update notice. The views replace one part at a time, so a route that is
// missing or off does not hide the rest. Pure, so node tests it. No herdr
// imports.

const EMPTY_ERRORS = {
  state: "",
  voice: "",
  brain: "",
  settings: "",
  devices: "",
};

// The form's sections, in order. `part` is the daemon route a section reads
// (where its error lands); `open` is whether it starts open. The common
// choices (which voice, which devices are paired) start open and the rest
// start folded to their title and current choice.
export const SECTIONS = [
  { id: "voice", title: "Voice", part: "voice", open: true },
  { id: "devices", title: "Paired devices", part: "devices", open: true },
  { id: "brain", title: "Orchestrator", part: "brain", open: false },
  { id: "languages", title: "Languages", part: "settings", open: false },
  { id: "replies", title: "Replies", part: "settings", open: false },
];

// The shared value (herdr's `shared`) that remembers which sections are open.
export const SETTINGS_OPEN_KEY = "sheepdog.settings.open";

// Which sections are open: the defaults, changed by what was saved (`saved`
// is { id: boolean }; anything else in it is ignored).
export function openSections(saved) {
  const open = {};
  for (const section of SECTIONS) open[section.id] = typeof saved?.[section.id] === "boolean" ? saved[section.id] : section.open;
  return open;
}

// A section with an error is shown open, so the error is not hidden.
export function sectionOpen(form, id) {
  const section = SECTIONS.find((s) => s.id === id);
  return Boolean(form.open[id]) || Boolean(section && form.errors[section.part]);
}

export function toggleSection(form, id) {
  return { ...form, open: { ...form.open, [id]: !sectionOpen(form, id) } };
}

// What a folded section shows beside its title: the current choice.
export function sectionSummary(form, id) {
  switch (id) {
    case "voice": {
      const voice = form.voice;
      const current = voice?.providers?.find((p) => p.id === voice.provider);
      return current ? String(current.label || current.id) : "";
    }
    case "brain": {
      const brain = form.brain;
      if (!brain?.model) return "";
      const model = brain.models?.find((m) => m.name === brain.model);
      return String(model?.display_name || brain.model);
    }
    case "languages": {
      if (!form.settings) return "";
      const { chosen } = languageChoices(form.settings);
      return chosen.length ? chosen.map((o) => o.native || o.name || o.code).join(", ") : "Follows what you speak";
    }
    case "replies":
      return form.settings ? String(form.settings.chattiness || "") : "";
    case "devices":
      return form.devices.length === 0 ? "None paired" : `${form.devices.length} paired`;
    default:
      return "";
  }
}

// Every part of the form before any request answers. `saved` is the open
// state remembered from the last time (see openSections).
export function emptySettingsForm(saved) {
  return {
    open: openSections(saved),
    state: null,
    voice: null,
    brain: null,
    settings: null,
    devices: [],
    loading: true,
    busy: "",
    errors: { ...EMPTY_ERRORS },
    notice: "",
    alert: "",
    deviceToRemove: "",
    // Orchestrator: the one provider whose models show, and the model lists
    // fetched for providers other than the daemon's current one.
    brainOpen: "",
    brainCatalogs: {},
  };
}

export function settingsLoaded(form, part, value) {
  return {
    ...form,
    [part]: value,
    loading: false,
    errors: { ...form.errors, [part]: "" },
  };
}

export function settingsFailed(form, part, message) {
  return {
    ...form,
    loading: false,
    errors: { ...form.errors, [part]: String(message || "It did not answer.") },
  };
}

export function settingsLoadFinished(form) {
  return { ...form, loading: false };
}

export function beginSettingsAction(form, name) {
  return { ...form, busy: name, notice: "", alert: "" };
}

export function finishSettingsAction(form, name, { error = "", notice = "" } = {}) {
  return {
    ...form,
    busy: form.busy === name ? "" : form.busy,
    notice: error ? "" : notice,
    errors: { ...form.errors, [name]: error ? String(error) : "" },
  };
}

// Removing a phone is two presses: Unpair asks, then Remove confirms.
export function askToRemoveDevice(form, id) {
  return { ...form, deviceToRemove: id };
}

export function cancelDeviceRemoval(form) {
  return { ...form, deviceToRemove: "" };
}

export function addLanguage(codes, code) {
  return codes.includes(code) ? [...codes] : [...codes, code];
}

export function removeLanguage(codes, code) {
  return codes.filter((candidate) => candidate !== code);
}

// Move one preference, keeping the order the voice and orchestrator receive.
export function moveLanguage(codes, code, delta) {
  const from = codes.indexOf(code);
  const to = from + delta;
  if (from < 0 || to < 0 || to >= codes.length) return [...codes];
  const next = [...codes];
  [next[from], next[to]] = [next[to], next[from]];
  return next;
}

// The chosen and remaining languages in the daemon's catalog order.
export function languageChoices(settings) {
  const options = Array.isArray(settings?.language_options) ? settings.language_options : [];
  const codes = Array.isArray(settings?.languages) ? settings.languages : [];
  return {
    chosen: codes.map((code) => options.find((option) => option.code === code)).filter(Boolean),
    others: options.filter((option) => !codes.includes(option.code)),
  };
}

// Recommended models first, then the rest. Keep the current model even when
// a stale catalog no longer lists it.
export function orderedBrainModels(brain) {
  const models = Array.isArray(brain?.models) ? brain.models : [];
  const recommended = models.filter((model) => model.recommended === true);
  const rest = models.filter((model) => model.recommended !== true);
  const current = brain?.model && !models.some((model) => model.name === brain.model)
    ? [{ name: brain.model, display_name: brain.model }]
    : [];
  return [...recommended, ...current, ...rest];
}

// One row per orchestrator provider, collapsed to its name and a summary:
// the chosen model for the current provider, else its availability. Only the
// provider in `open` lists models. The daemon sends one provider's catalog at
// a time (`models_provider`); the others are fetched when their row opens
// (`form.brainCatalogs`). Only the daemon's exact current pair is checked.
export function brainProviderGroups(brain, catalogs = {}, open = "") {
  const currentProvider = String(brain?.provider || "");
  const catalogProvider = String(brain?.models_provider || "");
  let providers = Array.isArray(brain?.providers) ? brain.providers : [];
  if (currentProvider && !providers.some((provider) => String(provider?.id || "") === currentProvider)) {
    providers = [...providers, { id: currentProvider, label: currentProvider, available: true }];
  }
  return providers.map((provider) => {
    const id = String(provider?.id || "");
    const available = provider?.available !== false;
    const current = id === currentProvider;
    const expanded = available && id === open;
    const own = id === catalogProvider ? brain : catalogs[id];
    const catalog = own ? { models: own.models, model: current ? brain.model : "", error: own.error, loading: own.loading } : null;
    const models = available && catalog && !catalog.loading
      ? orderedBrainModels(catalog).map((model) => ({
        ...model,
        provider: id,
        selected: brainSelectionMatches(brain, id, model?.name),
      }))
      : [];
    const reason = String(provider?.reason || "");
    return {
      id,
      label: String(provider?.label || id),
      available,
      selectable: available,
      reason,
      current,
      expanded,
      summary: brainRowSummary({ available, current, reason, brain, models }),
      loading: Boolean(expanded && (!catalog || catalog.loading)),
      error: expanded && catalog && !catalog.loading && catalog.error && models.length === 0 ? String(catalog.error) : "",
      models: expanded ? models : [],
    };
  });
}

function brainRowSummary({ available, current, reason, brain, models }) {
  if (!available) return reason ? `Unavailable — ${reason}` : "Unavailable";
  if (!current) return "Available";
  const name = String(brain?.model || "");
  if (!name) return "No model chosen";
  const model = models.find((m) => m.name === name) || (brain.models || []).find((m) => m.name === name);
  return String(model?.display_name || name);
}

// A press on a provider's row: it opens (closing any other), or closes. An
// unavailable provider has nothing to show.
export function toggleBrainProvider(form, id) {
  const provider = form.brain?.providers?.find((p) => String(p?.id || "") === id);
  if (provider?.available === false) return form;
  return { ...form, brainOpen: form.brainOpen === id ? "" : id };
}

// Does opening `id` need its model list fetched? The current catalog is the
// daemon's own; a failed fetch is tried again on the next opening.
export function brainNeedsCatalog(form, id) {
  if (!form.brain || String(form.brain.models_provider || "") === id) return false;
  const cached = form.brainCatalogs[id];
  return !cached || Boolean(cached.error);
}

export function brainCatalogLoading(form, id) {
  return { ...form, brainCatalogs: { ...form.brainCatalogs, [id]: { models: [], error: "", loading: true } } };
}

export function brainCatalogLoaded(form, id, catalog) {
  return { ...form, brainCatalogs: { ...form.brainCatalogs, [id]: { models: catalog?.models || [], error: String(catalog?.error || ""), loading: false } } };
}

export function brainCatalogFailed(form, id, message) {
  return { ...form, brainCatalogs: { ...form.brainCatalogs, [id]: { models: [], error: String(message || "It did not answer."), loading: false } } };
}

// "Open Setup" worked, or did not: a failure says where to go instead.
export function setupOpened(form, result) {
  return result.ok ? { ...form, alert: "", notice: "Setup opened in your browser." } : { ...form, notice: "", alert: result.error };
}

export function brainSelectionMatches(brain, provider, model) {
  return String(brain?.provider || "") === String(provider || "") &&
    String(brain?.model || "") === String(model || "");
}

export function voiceProviderDetail(provider) {
  const parts = [];
  if (provider?.model) parts.push(provider.model);
  if (provider?.available === false) parts.push(provider.reason ? `Unavailable — ${provider.reason}` : "Unavailable");
  else if (provider?.billing === "per_minute") parts.push("Billed per minute");
  return parts.join(" · ");
}

// The same words the phone's UpdateInfo.notice uses. The desktop cannot
// update the daemon; it only shows the command to run on this computer.
export function updateNotice(update) {
  const trimmed = (value) => (typeof value === "string" && value.trim() ? value.trim() : "");
  if (!update) return null;
  if (update.restart_pending) {
    const to = trimmed(update.restart_to) ? `sheepdog ${trimmed(update.restart_to)}` : "A new sheepdog";
    if (update.auto_restart) return { text: `${to} is installed and restarts itself when no one is using it.`, command: "", after: "" };
    return { text: `${to} is installed. To finish, run this on your computer:`, command: "sheepdog update", after: "" };
  }
  if (!update.available || !trimmed(update.latest)) return null;
  const after = update.auto_restart ? "It then restarts itself when idle." : "";
  if (trimmed(update.how)) {
    return { text: `sheepdog ${trimmed(update.latest)} is available. To update, run this on your computer:`, command: trimmed(update.how), after };
  }
  return { text: `sheepdog ${trimmed(update.latest)} is available. Update it on your computer.`, command: "", after };
}
