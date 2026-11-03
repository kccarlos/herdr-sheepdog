// Opening the daemon's setup page: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { openCommand, openSetup, setupUrl } from "./setup-link.js";

test("the setup page is the daemon's /setup, with no token", () => {
  assert.equal(setupUrl("http://127.0.0.1:7777"), "http://127.0.0.1:7777/setup");
  assert.equal(setupUrl("http://127.0.0.1:7791/"), "http://127.0.0.1:7791/setup");
});

test("each platform opens the browser its own way", () => {
  const url = "http://127.0.0.1:7777/setup";
  assert.deepEqual(openCommand("macos", url), ["open", [url]]);
  assert.deepEqual(openCommand("linux", url), ["xdg-open", [url]]);
  assert.deepEqual(openCommand("windows", url), ["cmd", ["/c", "start", "", url]]);
  assert.deepEqual(openCommand(undefined, url), ["xdg-open", [url]]);
});

test("openSetup reports success, a failing program and a missing one", async () => {
  const base = "http://127.0.0.1:7777";
  const seen = [];
  const exec = async (...a) => { seen.push(a); return { code: 0, stdout: "", stderr: "" }; };
  assert.deepEqual(await openSetup({ exec, platform: "linux", base }), { ok: true });
  assert.deepEqual(seen, [["xdg-open", ["http://127.0.0.1:7777/setup"]]]);
  const failing = await openSetup({ exec: async () => ({ code: 3, stdout: "", stderr: "x" }), platform: "macos", base });
  assert.equal(failing.ok, false);
  assert.match(failing.error, /Open http:\/\/127\.0\.0\.1:7777\/setup yourself/);
  const missing = await openSetup({ exec: async () => { throw new Error("not installed"); }, platform: "linux", base });
  assert.equal(missing.ok, false);
});
