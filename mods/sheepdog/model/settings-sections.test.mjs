// Settings' folding sections: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  emptySettingsForm,
  openSections,
  sectionOpen,
  sectionSummary,
  SECTIONS,
  settingsFailed,
  settingsLoaded,
  toggleSection,
} from "./settings.js";

test("the common choices start open and the rest start folded", () => {
  const form = emptySettingsForm();
  assert.deepEqual(SECTIONS.filter((s) => sectionOpen(form, s.id)).map((s) => s.id), ["voice", "devices"]);
});

test("every section of the form is one of SECTIONS, so each setting stays reachable", () => {
  assert.deepEqual(SECTIONS.map((s) => s.id).sort(), ["brain", "devices", "languages", "replies", "voice"]);
});

test("a toggle opens a folded section and folds an open one; others stay", () => {
  let form = toggleSection(emptySettingsForm(), "brain");
  assert.equal(sectionOpen(form, "brain"), true);
  assert.equal(sectionOpen(form, "voice"), true);
  form = toggleSection(toggleSection(form, "voice"), "brain");
  assert.equal(sectionOpen(form, "voice"), false);
  assert.equal(sectionOpen(form, "brain"), false);
});

test("what was saved overrides the defaults; junk in it is ignored", () => {
  assert.deepEqual(openSections({ voice: false, brain: true, languages: "yes", nope: true }), {
    voice: false, devices: true, brain: true, languages: false, replies: false,
  });
  assert.equal(sectionOpen(emptySettingsForm({ replies: true }), "replies"), true);
  assert.deepEqual(openSections(null), openSections(undefined));
});

test("a section with an error is open, and folding it is a no-op until the error clears", () => {
  let form = settingsFailed(emptySettingsForm(), "settings", "no supervisor");
  assert.equal(sectionOpen(form, "languages"), true);
  assert.equal(sectionOpen(form, "replies"), true);
  assert.equal(sectionOpen(form, "brain"), false);
  form = settingsLoaded(form, "settings", { chattiness: "normal", levels: [] });
  assert.equal(sectionOpen(form, "languages"), false);
});

test("a folded section says its current choice", () => {
  let form = emptySettingsForm();
  assert.equal(sectionSummary(form, "voice"), "");
  assert.equal(sectionSummary(form, "devices"), "None paired");
  form = settingsLoaded(form, "voice", { provider: "gemini", providers: [{ id: "gemini", label: "Gemini Live" }, { id: "openai", label: "GPT Live" }] });
  form = settingsLoaded(form, "brain", { model: "m1", models: [{ name: "m1", display_name: "Model One" }] });
  form = settingsLoaded(form, "settings", { chattiness: "talkative", languages: ["en"], language_options: [{ code: "en", name: "English", native: "English" }, { code: "de", name: "German", native: "Deutsch" }] });
  form = settingsLoaded(form, "devices", [{ id: "a" }, { id: "b" }]);
  assert.equal(sectionSummary(form, "voice"), "Gemini Live");
  assert.equal(sectionSummary(form, "brain"), "Model One");
  assert.equal(sectionSummary(form, "languages"), "English");
  assert.equal(sectionSummary(form, "replies"), "talkative");
  assert.equal(sectionSummary(form, "devices"), "2 paired");
  form = settingsLoaded(form, "settings", { chattiness: "normal", languages: [], language_options: [] });
  assert.equal(sectionSummary(form, "languages"), "Follows what you speak");
});
