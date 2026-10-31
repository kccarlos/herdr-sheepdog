// Settings' form state and choices: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  addLanguage,
  askToRemoveDevice,
  beginSettingsAction,
  cancelDeviceRemoval,
  defaultBrainModel,
  emptySettingsForm,
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
  assert.equal(defaultBrainModel({ models_provider: "deepseek", models: [{ name: "first" }, { name: "best", recommended: true }] }, "deepseek"), "best");
  assert.equal(defaultBrainModel({ model: "old", models_provider: "openai", models: [{ name: "first" }] }, "openai"), "first");
  assert.equal(defaultBrainModel({ models_provider: "deepseek", error: "catalog down", models: [{ name: "first" }] }, "deepseek"), "");
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
