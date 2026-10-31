// The panel's HTTP reads, the base URL and the paging they feed: `make test-panel`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { baseUrl, DEFAULT_URL, hostPort } from "./config.js";
import { createHttp, DaemonError } from "./http.js";
import { applyNewest, applyOlder, emptyHistory } from "../model/history.js";

// A fetch that answers from `routes` (path and query to a status and body)
// and records every URL.
function fakeFetch(routes) {
  const urls = [];
  const fetch = async (url) => {
    urls.push(url);
    const route = routes[url.replace("http://127.0.0.1:7777", "")];
    if (route instanceof Error) throw route;
    if (!route) return { ok: false, status: 404, json: async () => ({}) };
    return { ok: route.status < 400, status: route.status, json: async () => (route.body instanceof Error ? Promise.reject(route.body) : route.body) };
  };
  return { fetch, urls };
}

test("the base URL is the setting when it is a plain http address, else the default", () => {
  assert.equal(baseUrl(undefined), DEFAULT_URL);
  assert.equal(baseUrl(""), DEFAULT_URL);
  assert.equal(baseUrl("http://127.0.0.1:7780/"), "http://127.0.0.1:7780");
  assert.equal(baseUrl("http://localhost:7777"), "http://localhost:7777");
  assert.equal(baseUrl("http://[::1]:7777"), "http://[::1]:7777");
  assert.equal(baseUrl("https://127.0.0.1:7777"), DEFAULT_URL, "the raw socket speaks plain http only");
  assert.equal(baseUrl("127.0.0.1:7777"), DEFAULT_URL);
  assert.equal(baseUrl("http://127.0.0.1:7777/api"), DEFAULT_URL);
});

test("the socket's host and port come from the base URL", () => {
  assert.deepEqual(hostPort("http://127.0.0.1:7780"), { host: "127.0.0.1", port: 7780, authority: "127.0.0.1:7780" });
  assert.deepEqual(hostPort("http://[::1]:7777"), { host: "::1", port: 7777, authority: "[::1]:7777" });
  assert.deepEqual(hostPort("http://localhost"), { host: "localhost", port: 80, authority: "localhost" });
});

test("conversation pages are asked for by limit and before, newest first", async () => {
  const { fetch, urls } = fakeFetch({
    "/api/conversation?limit=2": { status: 200, body: { items: [{ id: 3 }, { id: 4 }], next_before: 3 } },
    "/api/conversation?limit=2&before=3": { status: 200, body: { items: [{ id: 1 }, { id: 2 }], next_before: null } },
  });
  const http = createHttp({ fetch, base: DEFAULT_URL });
  let history = applyNewest(emptyHistory(), await http.conversation({ limit: 2 }));
  assert.equal(history.nextBefore, 3);
  history = applyOlder(history, await http.conversation({ limit: 2, before: history.nextBefore }));
  assert.deepEqual(history.items.map((i) => i.id), [1, 2, 3, 4]);
  assert.equal(history.nextBefore, null);
  assert.deepEqual(urls, [`${DEFAULT_URL}/api/conversation?limit=2`, `${DEFAULT_URL}/api/conversation?limit=2&before=3`]);
});

test("the reads are plain GETs: fetch is called with a URL and nothing else", async () => {
  const calls = [];
  const http = createHttp({ fetch: async (...args) => { calls.push(args); return { ok: true, status: 200, json: async () => ({ voice: {}, items: [] }) }; }, base: DEFAULT_URL });
  await http.state();
  await http.conversation({ limit: 5 });
  assert.ok(calls.every((args) => args.length === 1 && typeof args[0] === "string"));
});

// A fetch that records each call and answers with `status` and `body`.
function recordingFetch(status, body) {
  const calls = [];
  const fetch = async (url, options) => {
    calls.push({ url, options });
    return { ok: status < 400, status, json: async () => (body instanceof Error ? Promise.reject(body) : body) };
  };
  return { fetch, calls };
}

test("a typed line is POSTed to /api/turn as JSON with its Idempotency-Key", async () => {
  const { fetch, calls } = recordingFetch(200, { reply: "Done.", turn_id: "u1", acted: true });
  const http = createHttp({ fetch, base: DEFAULT_URL });
  assert.deepEqual(await http.turn("run the tests", "panel-key-1"), { reply: "Done.", turn_id: "u1", acted: true });
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, `${DEFAULT_URL}/api/turn`);
  assert.equal(calls[0].options.method, "POST");
  assert.equal(calls[0].options.headers["Idempotency-Key"], "panel-key-1");
  assert.equal(calls[0].options.headers["Content-Type"], "application/json");
  assert.deepEqual(JSON.parse(calls[0].options.body), { text: "run the tests" });
});

test("a tap is POSTed to /api/pending/answer with the id, the 1-based option and a key", async () => {
  const { fetch, calls } = recordingFetch(200, { ok: true, spoken: "Sent." });
  const http = createHttp({ fetch, base: DEFAULT_URL });
  assert.deepEqual(await http.answer("p1", 2, "panel-key-2"), { ok: true, spoken: "Sent." });
  assert.equal(calls[0].url, `${DEFAULT_URL}/api/pending/answer`);
  assert.equal(calls[0].options.headers["Idempotency-Key"], "panel-key-2");
  assert.deepEqual(JSON.parse(calls[0].options.body), { id: "p1", option: 2 });
});

test("settings routes use their documented GET, PUT and DELETE shapes", async () => {
  const { fetch, calls } = recordingFetch(200, { ok: true });
  const http = createHttp({ fetch, base: DEFAULT_URL });
  await http.voiceProviders();
  await http.setVoiceProvider("openai");
  await http.brainModels("deepseek");
  await http.setBrainModel("deepseek", "deepseek-flash");
  await http.settings();
  await http.setSettings({ languages: ["zh-Hans", "en"] });
  await http.removeDevice("phone 1");
  assert.deepEqual(calls.map((call) => [call.url, call.options?.method ?? "GET"]), [
    [`${DEFAULT_URL}/api/voice/providers`, "GET"],
    [`${DEFAULT_URL}/api/voice/provider`, "PUT"],
    [`${DEFAULT_URL}/api/brain/models?provider=deepseek`, "GET"],
    [`${DEFAULT_URL}/api/brain/model`, "PUT"],
    [`${DEFAULT_URL}/api/settings`, "GET"],
    [`${DEFAULT_URL}/api/settings`, "PUT"],
    [`${DEFAULT_URL}/api/devices/phone%201`, "DELETE"],
  ]);
  assert.deepEqual(JSON.parse(calls[1].options.body), { provider: "openai" });
  assert.deepEqual(JSON.parse(calls[3].options.body), { provider: "deepseek", model: "deepseek-flash" });
  assert.deepEqual(JSON.parse(calls[5].options.body), { languages: ["zh-Hans", "en"] });
  assert.equal(calls[6].options.body, undefined);
});

test("agents, devices, pairing and reset use their API answers", async () => {
  const calls = [];
  const answers = [
    [{ pane_id: "w1:p1", alias: "api" }],
    { devices: [{ id: "phone-1", name: "Phone" }] },
    { link: "https://example.invalid/voice#pair=1", addresses: [] },
    { size: 1, modules: ["1"] },
    { ok: true, reset_id: "r1", at: "2026-10-30T12:00:00Z" },
  ];
  const fetch = async (url, options) => {
    calls.push({ url, options });
    return { ok: true, status: 200, json: async () => answers.shift() };
  };
  const http = createHttp({ fetch, base: DEFAULT_URL });
  assert.equal((await http.agents())[0].alias, "api");
  assert.equal((await http.devices())[0].id, "phone-1");
  assert.match((await http.pair()).link, /pair=1/);
  assert.deepEqual(await http.pairQR(), { size: 1, modules: ["1"] });
  assert.deepEqual(await http.reset("panel-reset-1"), { ok: true, reset_id: "r1", at: "2026-10-30T12:00:00Z" });
  assert.equal(calls[4].url, `${DEFAULT_URL}/api/conversation/reset`);
  assert.equal(calls[4].options.headers["Idempotency-Key"], "panel-reset-1");
  assert.deepEqual(JSON.parse(calls[4].options.body), { via: "app" });
});

test("a fresh pairing code is made only by /api/pair?new=1", async () => {
  const { fetch, urls } = fakeFetch({
    "/api/pair?new=1": { status: 200, body: { link: "https://example.invalid/voice#pair=1", addresses: [] } },
    "/api/pair/qr.json": { status: 200, body: { size: 1, modules: ["1"] } },
  });
  const http = createHttp({ fetch, base: DEFAULT_URL });
  await http.pair(true);
  await http.pairQR();
  assert.deepEqual(urls, [`${DEFAULT_URL}/api/pair?new=1`, `${DEFAULT_URL}/api/pair/qr.json`]);
});

test("the voice's controls are POSTed to /api/voice/*, each with a key; stop has no body", async () => {
  const { fetch, calls } = recordingFetch(200, { ok: true });
  const http = createHttp({ fetch, base: DEFAULT_URL });
  await http.mute(true, "k1");
  await http.stop("k2");
  await http.ptt(false, "k3");
  assert.deepEqual(calls.map((c) => c.url), [`${DEFAULT_URL}/api/voice/mute`, `${DEFAULT_URL}/api/voice/stop`, `${DEFAULT_URL}/api/voice/ptt`]);
  assert.deepEqual(calls.map((c) => c.options.headers["Idempotency-Key"]), ["k1", "k2", "k3"]);
  assert.deepEqual(JSON.parse(calls[0].options.body), { on: true });
  assert.equal(calls[1].options.body, undefined);
  assert.deepEqual(JSON.parse(calls[2].options.body), { on: false });
  const none = createHttp({ fetch: recordingFetch(409, { error: "no voice client" }).fetch, base: DEFAULT_URL });
  await assert.rejects(none.ptt(true, "k"), (e) => e.status === 409 && e.message === "no voice client");
});

test("a refused POST carries the daemon's own words and its status", async () => {
  const off = createHttp({ fetch: recordingFetch(503, { error: "the orchestrator is off (no brain key)" }).fetch, base: DEFAULT_URL });
  await assert.rejects(off.turn("hi", "k"), (e) => e.kind === "http" && e.status === 503 && e.message === "the orchestrator is off (no brain key)");

  const stale = createHttp({ fetch: recordingFetch(409, { error: "that question is no longer open" }).fetch, base: DEFAULT_URL });
  await assert.rejects(stale.answer("p1", 1, "k"), (e) => e.status === 409);

  const bare = createHttp({ fetch: recordingFetch(500, new Error("not json")).fetch, base: DEFAULT_URL });
  await assert.rejects(bare.turn("hi", "k"), (e) => e.status === 500 && /HTTP 500/.test(e.message));
});

test("a POST nothing answered is 'down'; a 200 that is not JSON is 'invalid'", async () => {
  const refused = createHttp({ fetch: async () => { throw new Error("connection refused"); }, base: DEFAULT_URL });
  await assert.rejects(refused.turn("hi", "k"), (e) => e instanceof DaemonError && e.kind === "down" && e.status === 0);
  const html = createHttp({ fetch: recordingFetch(200, new Error("not json")).fetch, base: DEFAULT_URL });
  await assert.rejects(html.turn("hi", "k"), (e) => e.kind === "invalid");
});

test("nothing answering is 'down'; an error status, and not-sheepdog answers, say what they were", async () => {
  const refused = createHttp({ fetch: fakeFetch({ "/api/state": new Error("connection refused") }).fetch, base: DEFAULT_URL });
  await assert.rejects(refused.state(), (e) => e instanceof DaemonError && e.kind === "down");

  const broken = createHttp({ fetch: fakeFetch({ "/api/state": { status: 500, body: {} } }).fetch, base: DEFAULT_URL });
  await assert.rejects(broken.state(), (e) => e.kind === "http" && e.status === 500 && /HTTP 500/.test(e.message));

  const noCode = createHttp({ fetch: fakeFetch({ "/api/pair/qr.json": { status: 409, body: { error: "no pairing link" } } }).fetch, base: DEFAULT_URL });
  await assert.rejects(noCode.pairQR(), (e) => e.status === 409 && e.message === "no pairing link");

  const html = createHttp({ fetch: fakeFetch({ "/api/state": { status: 200, body: new Error("not json") } }).fetch, base: DEFAULT_URL });
  await assert.rejects(html.state(), (e) => e.kind === "invalid");

  const other = createHttp({ fetch: fakeFetch({ "/api/state": { status: 200, body: { hello: "world" } }, "/api/conversation": { status: 200, body: { nope: 1 } } }).fetch, base: DEFAULT_URL });
  await assert.rejects(other.state(), (e) => e.kind === "invalid" && /not sheepdog/.test(e.message));
  await assert.rejects(other.conversation(), (e) => e.kind === "invalid");
});
