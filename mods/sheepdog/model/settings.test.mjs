// Settings' form state and choices: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  addLanguage,
  askToRemoveDevice,
  beginSettingsAction,
  brainProviderGroups,
  brainSelectionMatches,
  cancelDeviceRemoval,
  brainCatalogFailed,
  brainCatalogLoaded,
  brainCatalogLoading,
  brainNeedsCatalog,
  emptySettingsForm,
  setupOpened,
  toggleBrainProvider,
  finishSettingsAction,
  languageChoices,
  moveLanguage,
  orderedBrainModels,
  removeLanguage,
  settingsFailed,
  settingsLoaded,
  updateNotice,
  voiceProviderDetail,
} from "./settings.js";

test("one failed settings route leaves the other parts in place", () => {
  let form = emptySettingsForm();
  form = settingsLoaded(form, "voice", { provider: "gemini", providers: [] });
  form = settingsFailed(form, "settings", "no supervisor");
  assert.equal(form.voice.provider, "gemini");
  assert.equal(form.errors.settings, "no supervisor");
  assert.equal(form.errors.voice, "");
  assert.equal(form.loading, false);
});

test("an action owns one busy slot and its error is cleared by the next action", () => {
  let form = beginSettingsAction(emptySettingsForm(), "brain");
  assert.equal(form.busy, "brain");
  form = finishSettingsAction(form, "brain", { error: "no OPENAI_API_KEY" });
  assert.equal(form.busy, "");
  assert.equal(form.errors.brain, "no OPENAI_API_KEY");
  form = beginSettingsAction(form, "brain");
  assert.equal(form.errors.brain, "no OPENAI_API_KEY", "starting keeps the failure visible");
  form = finishSettingsAction(form, "brain", { notice: "Saved." });
  assert.equal(form.errors.brain, "");
  assert.equal(form.notice, "Saved.");
});

test("a paired device asks before removal and Keep cancels it", () => {
  const form = emptySettingsForm();
  const asked = askToRemoveDevice(form, "phone-1");
  assert.equal(asked.deviceToRemove, "phone-1");
  assert.equal(cancelDeviceRemoval(asked).deviceToRemove, "");
  assert.equal(form.deviceToRemove, "", "the reducer does not mutate its input");
});

test("languages keep preference order, reject duplicates and move one step", () => {
  let codes = [];
  codes = addLanguage(codes, "zh-Hans");
  codes = addLanguage(codes, "en");
  codes = addLanguage(codes, "zh-Hans");
  assert.deepEqual(codes, ["zh-Hans", "en"]);
  assert.deepEqual(moveLanguage(codes, "en", -1), ["en", "zh-Hans"]);
  assert.deepEqual(moveLanguage(codes, "zh-Hans", -1), ["zh-Hans", "en"], "the first cannot move up");
  assert.deepEqual(removeLanguage(codes, "zh-Hans"), ["en"]);
  const catalog = {
    languages: codes,
    language_options: [
      { code: "en", native: "English" },
      { code: "zh-Hans", native: "简体中文" },
      { code: "fr", native: "Français" },
    ],
  };
  assert.deepEqual(languageChoices(catalog).chosen.map((option) => option.code), codes);
  assert.deepEqual(languageChoices(catalog).others.map((option) => option.code), ["fr"]);
});

test("brain choices put recommended models first and keep an unlisted current model", () => {
  const brain = {
    model: "custom",
    models: [
      { name: "other" },
      { name: "recommended", recommended: true },
    ],
  };
  assert.deepEqual(orderedBrainModels(brain).map((model) => model.name), ["recommended", "custom", "other"]);
});

test("brain choices group models under providers and check only the current pair", () => {
  const brain = {
    provider: "fake",
    model: "fake-reflex",
    models_provider: "fake",
    providers: [
      { id: "fake", label: "Fake" },
      { id: "fake2", label: "Fake 2" },
      { id: "openai", label: "OpenAI", available: false, reason: "no OPENAI_API_KEY" },
    ],
    models: [
      { name: "gemini-3.8-flash", display_name: "Gemini 3.8 Flash", recommended: true },
      { name: "fake-reflex", display_name: "Scripted reflex (no network)" },
    ],
  };
  // Collapsed, no row lists a model; the current one's summary names its model.
  const closed = brainProviderGroups(brain);
  assert.deepEqual(closed.flatMap((group) => group.models), []);
  assert.deepEqual(closed.map((group) => group.summary), ["Scripted reflex (no network)", "Available", "Unavailable — no OPENAI_API_KEY"]);
  const groups = brainProviderGroups(brain, {}, "fake");
  assert.deepEqual(groups.map((group) => group.label), ["Fake", "Fake 2", "OpenAI"]);
  assert.deepEqual(groups[0].models.map((model) => model.name), ["gemini-3.8-flash", "fake-reflex"]);
  assert.deepEqual(groups.flatMap((group) => group.models).filter((model) => model.selected).map((model) => `${model.provider}/${model.name}`), ["fake/fake-reflex"]);
  assert.equal(brainSelectionMatches(brain, "fake", "fake-reflex"), true);
  assert.equal(brainSelectionMatches(brain, "fake2", "fake-reflex"), false);
  assert.equal(groups[2].selectable, false);
  assert.equal(groups[2].reason, "no OPENAI_API_KEY");
  assert.deepEqual(groups[2].models, []);
});

test("switching provider moves the one checked row into that provider's group", () => {
  const brain = {
    provider: "fake2",
    model: "deepseek-flash",
    models_provider: "fake2",
    providers: [{ id: "fake", label: "Fake" }, { id: "fake2", label: "Fake 2" }],
    models: [
      { name: "deepseek-flash", recommended: true },
      { name: "fake-reflex" },
    ],
  };
  const groups = brainProviderGroups(brain, {}, "fake2");
  assert.deepEqual(groups[0].models, []);
  assert.deepEqual(groups[1].models.filter((model) => model.selected).map((model) => model.name), ["deepseek-flash"]);
  assert.equal(brainSelectionMatches(brain, "fake2", "deepseek-flash"), true);
  assert.equal(brainSelectionMatches(brain, "fake", "deepseek-flash"), false);
});

const dropdownBrain = {
  provider: "gemini",
  model: "g-2",
  models_provider: "gemini",
  providers: [
    { id: "gemini", label: "Gemini" },
    { id: "claude", label: "Claude" },
    { id: "openai", label: "OpenAI", available: false, reason: "no OPENAI_API_KEY" },
  ],
  models: [{ name: "g-1", display_name: "G One" }, { name: "g-2", display_name: "G Two" }],
};

test("only one provider row is open at a time, and an unavailable one never opens", () => {
  let form = settingsLoaded(emptySettingsForm(), "brain", dropdownBrain);
  assert.equal(form.brainOpen, "");
  form = toggleBrainProvider(form, "gemini");
  assert.deepEqual(brainProviderGroups(form.brain, form.brainCatalogs, form.brainOpen).filter((g) => g.expanded).map((g) => g.id), ["gemini"]);
  form = toggleBrainProvider(form, "claude");
  assert.deepEqual(brainProviderGroups(form.brain, form.brainCatalogs, form.brainOpen).filter((g) => g.expanded).map((g) => g.id), ["claude"]);
  assert.equal(toggleBrainProvider(form, "openai"), form);
  assert.equal(toggleBrainProvider(form, "claude").brainOpen, "");
});

test("another provider's models come from a fetched list, once", () => {
  let form = settingsLoaded(emptySettingsForm(), "brain", dropdownBrain);
  assert.equal(brainNeedsCatalog(form, "gemini"), false);
  assert.equal(brainNeedsCatalog(form, "claude"), true);
  form = toggleBrainProvider(brainCatalogLoading(form, "claude"), "claude");
  let claude = brainProviderGroups(form.brain, form.brainCatalogs, form.brainOpen)[1];
  assert.equal(claude.loading, true);
  assert.deepEqual(claude.models, []);
  form = brainCatalogLoaded(form, "claude", { models: [{ name: "c-1", recommended: true }, { name: "c-2" }] });
  claude = brainProviderGroups(form.brain, form.brainCatalogs, form.brainOpen)[1];
  assert.deepEqual(claude.models.map((m) => `${m.provider}/${m.name}/${m.selected}`), ["claude/c-1/false", "claude/c-2/false"]);
  assert.equal(claude.summary, "Available");
  assert.equal(brainNeedsCatalog(form, "claude"), false);
  // A failed fetch shows its reason and is tried again.
  form = brainCatalogFailed(form, "claude", "catalog down");
  claude = brainProviderGroups(form.brain, form.brainCatalogs, form.brainOpen)[1];
  assert.equal(claude.error, "catalog down");
  assert.equal(brainNeedsCatalog(form, "claude"), true);
});

test("the current provider's row names the chosen model without opening", () => {
  const form = settingsLoaded(emptySettingsForm(), "brain", dropdownBrain);
  const [gemini, claude, openai] = brainProviderGroups(form.brain, form.brainCatalogs, form.brainOpen);
  assert.equal(gemini.summary, "G Two");
  assert.equal(gemini.current, true);
  assert.equal(gemini.expanded, false);
  assert.equal(claude.summary, "Available");
  assert.equal(openai.summary, "Unavailable — no OPENAI_API_KEY");
  assert.equal(openai.available, false);
});

test("Open Setup leaves a notice when it worked and an alert with the address when it did not", () => {
  const form = emptySettingsForm();
  assert.equal(setupOpened(form, { ok: true }).notice, "Setup opened in your browser.");
  const failed = setupOpened(form, { ok: false, error: "Open http://x/setup yourself" });
  assert.equal(failed.alert, "Open http://x/setup yourself");
  assert.equal(beginSettingsAction(failed, "voice").alert, "");
});

test("provider detail and update notices say what the daemon reported", () => {
  assert.equal(voiceProviderDetail({ model: "gpt-live-1", billing: "per_minute" }), "gpt-live-1 · Billed per minute");
  assert.equal(voiceProviderDetail({ available: false, reason: "no key" }), "Unavailable — no key");
  assert.deepEqual(updateNotice({ available: true, latest: "1.2.3", how: "brew upgrade sheepdog" }), {
    text: "sheepdog 1.2.3 is available. To update, run this on your computer:",
    command: "brew upgrade sheepdog",
    after: "",
  });
  assert.equal(updateNotice({ available: false }), null);
});
