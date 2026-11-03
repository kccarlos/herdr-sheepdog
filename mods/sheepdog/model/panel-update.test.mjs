import assert from "node:assert/strict";
import test from "node:test";
import { panelUpdateNotice } from "./panel-update.js";

const base = { installed: true, version: "v1.0.0", modified: false, update_available: true, auto_update: true };

test("nothing without panel state, a panel, or a newer one", () => {
  assert.equal(panelUpdateNotice(undefined), null);
  assert.equal(panelUpdateNotice(null), null);
  assert.equal(panelUpdateNotice({ ...base, installed: false }), null);
  assert.equal(panelUpdateNotice({ ...base, update_available: false, modified: true }), null);
});

test("an unchanged panel is updated by the daemon: no notice", () => {
  assert.equal(panelUpdateNotice(base), null);
});

test("a changed panel gets the notice with the setup command", () => {
  const n = panelUpdateNotice({ ...base, modified: true });
  assert.equal(n.title, "A newer panel is available");
  assert.equal(n.command, "sheepdog setup");
  assert.match(n.text, /left as it is/);
  assert.match(n.text, /Run sheepdog setup/);
});

test("with auto update off the notice shows for an unchanged panel too", () => {
  const n = panelUpdateNotice({ ...base, auto_update: false });
  assert.match(n.text, /panel\.auto_update/);
});

test("an older daemon that does not say auto_update counts as on", () => {
  assert.equal(panelUpdateNotice({ installed: true, modified: false, update_available: true }), null);
});
