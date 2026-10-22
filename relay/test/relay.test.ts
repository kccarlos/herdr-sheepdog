import { env as testEnv, SELF } from "cloudflare:test";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { resetJWTCache } from "../src/apns";
import { handle, MAX_SEND_BYTES, type Deps, type Env } from "../src/index";
import { b64urlDecode, b64urlEncode, openToken, sealToken } from "../src/token";

const DEVICE = "a1".repeat(32);
const TOPIC = "com.github.kccarlos.herdr-sheepdog-ios";
const SECRET = "test-relay-secret";

let pem = "";
let publicKey: CryptoKey;

beforeAll(async () => {
  const pair = (await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"])) as CryptoKeyPair;
  publicKey = pair.publicKey;
  const der = new Uint8Array((await crypto.subtle.exportKey("pkcs8", pair.privateKey)) as ArrayBuffer);
  let bin = "";
  for (const b of der) bin += String.fromCharCode(b);
  pem = `-----BEGIN PRIVATE KEY-----\n${btoa(bin).replace(/(.{64})/g, "$1\n")}\n-----END PRIVATE KEY-----\n`;
});

beforeEach(() => resetJWTCache());

const allow: RateLimit = { limit: async () => ({ success: true }) };

function fullEnv(over: Partial<Env> = {}): Env {
  return {
    APNS_KEY: pem,
    APNS_KEY_ID: "KEY1234567",
    APNS_TEAM_ID: "TEAM123456",
    RELAY_SECRET: SECRET,
    APNS_TOPIC: TOPIC,
    SEND_LIMITER: allow,
    REGISTER_LIMITER: allow,
    ...over,
  };
}

interface Call {
  url: string;
  headers: Record<string, string>;
  body: any;
}

/** A fake APNs: answers each request from the queue (default 200) and records it. */
function fakeAPNs(...answers: Array<{ status: number; reason?: string }>) {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const headers: Record<string, string> = {};
    new Headers(init?.headers).forEach((v, k) => (headers[k] = v));
    calls.push({ url: String(input), headers, body: JSON.parse(String(init?.body)) });
    const a = answers.shift() ?? { status: 200 };
    return new Response(a.reason ? JSON.stringify({ reason: a.reason }) : null, { status: a.status });
  });
  return { calls, fetch: fn as unknown as typeof fetch };
}

function deps(f: typeof fetch, now = () => Date.now()): Deps {
  return { fetch: f, now };
}

function post(path: string, body: unknown, headers: Record<string, string> = {}): Request {
  return new Request(`https://relay.test${path}`, {
    method: "POST",
    headers: { "content-type": "application/json", ...headers },
    body: typeof body === "string" ? body : JSON.stringify(body),
  });
}

async function register(e: Env, environment = "production"): Promise<string> {
  const r = await handle(post("/v1/register", { device_token: DEVICE, environment, bundle_id: TOPIC }), e, deps(fakeAPNs().fetch));
  expect(r.status).toBe(200);
  return ((await r.json()) as { relay_token: string }).relay_token;
}

const payload = { ciphertext: btoa("sealed bytes here"), nonce: btoa("123456789012"), kind: "approval", collapse_id: "ap-1" };

describe("health", () => {
  it("answers through the deployed entry point", async () => {
    const r = await SELF.fetch("https://relay.test/v1/health");
    expect(r.status).toBe(200);
    const j = (await r.json()) as { ok: boolean; version: string };
    expect(j.version).toBe("1.1.0");
    // The test bindings carry no APNS_KEY, so the relay is not fully configured.
    expect(j.ok).toBe(false);
  });

  it("is ok with every secret set", async () => {
    const r = await handle(new Request("https://relay.test/v1/health"), fullEnv(), deps(fakeAPNs().fetch));
    expect(await r.json()).toEqual({ ok: true, version: "1.1.0" });
  });

  it("404s an unknown path and 405s a GET register", async () => {
    expect((await handle(new Request("https://relay.test/nope"), fullEnv(), deps(fakeAPNs().fetch))).status).toBe(404);
    expect((await handle(new Request("https://relay.test/v1/register"), fullEnv(), deps(fakeAPNs().fetch))).status).toBe(405);
  });
});

describe("register", () => {
  it("returns a relay token that opens to the device", async () => {
    const tok = await register(fullEnv(), "sandbox");
    const c = await openToken(SECRET, tok);
    expect(c).toMatchObject({ v: 1, device_token: DEVICE, environment: "sandbox" });
    expect(Math.abs(c!.issued_at - Date.now() / 1000)).toBeLessThan(5);
    // The device token is not readable from the relay token.
    expect(tok).not.toContain(DEVICE);
  });

  it("lowercases the device token", async () => {
    const r = await handle(post("/v1/register", { device_token: DEVICE.toUpperCase(), environment: "production", bundle_id: TOPIC }), fullEnv(), deps(fakeAPNs().fetch));
    const { relay_token } = (await r.json()) as { relay_token: string };
    expect((await openToken(SECRET, relay_token))!.device_token).toBe(DEVICE);
  });

  it.each([
    ["wrong bundle", { device_token: DEVICE, environment: "production", bundle_id: "com.example.other" }],
    ["missing bundle", { device_token: DEVICE, environment: "production" }],
    ["short token", { device_token: "abcd", environment: "production", bundle_id: TOPIC }],
    ["non-hex token", { device_token: "zz".repeat(32), environment: "production", bundle_id: TOPIC }],
    ["odd-length token", { device_token: DEVICE + "a", environment: "production", bundle_id: TOPIC }],
    ["bad environment", { device_token: DEVICE, environment: "staging", bundle_id: TOPIC }],
    ["not an object", [1, 2]],
    ["not JSON", "{nope"],
  ])("rejects %s with 400", async (_name, body) => {
    const r = await handle(post("/v1/register", body), fullEnv(), deps(fakeAPNs().fetch));
    expect(r.status).toBe(400);
  });

  it("rejects an oversized body with 413", async () => {
    const r = await handle(post("/v1/register", { device_token: DEVICE, environment: "production", bundle_id: TOPIC, pad: "x".repeat(2000) }), fullEnv(), deps(fakeAPNs().fetch));
    expect(r.status).toBe(413);
  });

  it("503s without RELAY_SECRET", async () => {
    const r = await handle(post("/v1/register", { device_token: DEVICE, environment: "production", bundle_id: TOPIC }), fullEnv({ RELAY_SECRET: undefined }), deps(fakeAPNs().fetch));
    expect(r.status).toBe(503);
  });
});

describe("send", () => {
  it("sends the placeholder alert and the ciphertext to APNs", async () => {
    const e = fullEnv();
    const tok = await register(e);
    const apns = fakeAPNs();
    const r = await handle(post("/v1/send", { relay_token: tok, payload }), e, deps(apns.fetch));
    expect(r.status).toBe(200);
    expect(await r.json()).toEqual({ ok: true });
    expect(apns.calls).toHaveLength(1);
    const c = apns.calls[0];
    expect(c.url).toBe(`https://api.push.apple.com/3/device/${DEVICE}`);
    expect(c.headers["apns-topic"]).toBe(TOPIC);
    expect(c.headers["apns-push-type"]).toBe("alert");
    expect(c.headers["apns-priority"]).toBe("10");
    expect(c.headers["apns-collapse-id"]).toBe("ap-1");
    expect(c.body).toEqual({
      aps: {
        alert: { title: "Sheepdog", body: "New update" },
        "mutable-content": 1,
        sound: "default",
        category: "APPROVAL",
        "interruption-level": "time-sensitive",
      },
      e2e: { c: payload.ciphertext, n: payload.nonce },
    });
    // The provider token is a valid ES256 JWT for the key.
    const [h, cl, sig] = c.headers.authorization.replace(/^bearer /, "").split(".");
    expect(JSON.parse(new TextDecoder().decode(b64urlDecode(h)!))).toEqual({ alg: "ES256", kid: "KEY1234567" });
    expect(JSON.parse(new TextDecoder().decode(b64urlDecode(cl)!))).toMatchObject({ iss: "TEAM123456" });
    const ok = await crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, publicKey, b64urlDecode(sig)!, new TextEncoder().encode(`${h}.${cl}`));
    expect(ok).toBe(true);
  });

  it("maps kind to category and leaves both out without one", async () => {
    const e = fullEnv();
    const tok = await register(e);
    const apns = fakeAPNs();
    await handle(post("/v1/send", { relay_token: tok, payload: { ...payload, kind: "done" }, priority: 5 }), e, deps(apns.fetch));
    await handle(post("/v1/send", { relay_token: tok, payload: { ciphertext: payload.ciphertext, nonce: payload.nonce } }), e, deps(apns.fetch));
    expect(apns.calls[0].body.aps.category).toBe("AGENT");
    expect(apns.calls[0].body.aps["interruption-level"]).toBe("active");
    expect(apns.calls[0].headers["apns-priority"]).toBe("5");
    expect(apns.calls[1].body.aps.category).toBeUndefined();
    expect(apns.calls[1].headers["apns-collapse-id"]).toBeUndefined();
  });

  it("uses the sandbox host for a sandbox token", async () => {
    const e = fullEnv();
    const tok = await register(e, "sandbox");
    const apns = fakeAPNs();
    await handle(post("/v1/send", { relay_token: tok, payload }), e, deps(apns.fetch));
    expect(apns.calls[0].url).toBe(`https://api.sandbox.push.apple.com/3/device/${DEVICE}`);
  });

  it("refuses a tampered token with 401 and never calls APNs", async () => {
    const e = fullEnv();
    const raw = b64urlDecode(await register(e))!;
    raw[20] ^= 0x01;
    const apns = fakeAPNs();
    const r = await handle(post("/v1/send", { relay_token: b64urlEncode(raw), payload }), e, deps(apns.fetch));
    expect(r.status).toBe(401);
    expect(apns.calls).toHaveLength(0);
  });

  it("refuses a token sealed under another secret", async () => {
    const other = await sealToken("another-secret", { v: 1, device_token: DEVICE, environment: "production", issued_at: 1 });
    const r = await handle(post("/v1/send", { relay_token: other, payload }), fullEnv(), deps(fakeAPNs().fetch));
    expect(r.status).toBe(401);
  });

  it.each([
    ["no relay_token", { payload }],
    ["no payload", { relay_token: "x" }],
    ["bad ciphertext", { relay_token: "x", payload: { ...payload, ciphertext: "***" } }],
    ["huge ciphertext", { relay_token: "x", payload: { ...payload, ciphertext: "A".repeat(4000) } }],
    ["short nonce", { relay_token: "x", payload: { ...payload, nonce: btoa("short") } }],
    ["bad kind", { relay_token: "x", payload: { ...payload, kind: "Approval!" } }],
    ["long collapse id", { relay_token: "x", payload: { ...payload, collapse_id: "c".repeat(65) } }],
    ["bad priority", { relay_token: "x", payload, priority: 7 }],
  ])("rejects %s with 400", async (_name, body) => {
    const r = await handle(post("/v1/send", body), fullEnv(), deps(fakeAPNs().fetch));
    expect(r.status).toBe(400);
  });

  it("rejects an oversized body with 413", async () => {
    const r = await handle(post("/v1/send", { relay_token: "x", payload, pad: "x".repeat(MAX_SEND_BYTES) }), fullEnv(), deps(fakeAPNs().fetch));
    expect(r.status).toBe(413);
  });

  it("503s while a secret is missing", async () => {
    const e = fullEnv();
    const tok = await register(e);
    const r = await handle(post("/v1/send", { relay_token: tok, payload }), fullEnv({ APNS_KEY: undefined }), deps(fakeAPNs().fetch));
    expect(r.status).toBe(503);
  });
});

describe("activity", () => {
  const state = { phase: "away", waiting: 2, working: 1, done: 0, sealed: { c: btoa("rows"), n: btoa("123456789012") } };

  it("sends a Live Activity update with the content state as given", async () => {
    const e = fullEnv();
    const tok = await register(e);
    const apns = fakeAPNs();
    const r = await handle(post("/v1/activity", { relay_token: tok, content_state: state, timestamp: 1_800_000_000, stale_date: 1_800_000_900 }), e, deps(apns.fetch));
    expect(r.status).toBe(200);
    const c = apns.calls[0];
    expect(c.url).toBe(`https://api.push.apple.com/3/device/${DEVICE}`);
    expect(c.headers["apns-topic"]).toBe(`${TOPIC}.push-type.liveactivity`);
    expect(c.headers["apns-push-type"]).toBe("liveactivity");
    expect(c.headers["apns-priority"]).toBe("5");
    expect(c.body).toEqual({
      aps: { timestamp: 1_800_000_000, event: "update", "content-state": state, "stale-date": 1_800_000_900 },
    });
  });

  it("ends with a dismissal date, at priority 10, and fills in the timestamp", async () => {
    const e = fullEnv();
    const tok = await register(e, "sandbox");
    const apns = fakeAPNs();
    const now = 1_800_000_123_456;
    const r = await handle(post("/v1/activity", { relay_token: tok, event: "end", content_state: state, dismissal_date: 1, priority: 10 }), e, deps(apns.fetch, () => now));
    expect(r.status).toBe(200);
    expect(apns.calls[0].url).toBe(`https://api.sandbox.push.apple.com/3/device/${DEVICE}`);
    expect(apns.calls[0].headers["apns-priority"]).toBe("10");
    expect(apns.calls[0].body.aps).toMatchObject({ event: "end", timestamp: 1_800_000_123, "dismissal-date": 1 });
  });

  it("maps a gone activity token to 410", async () => {
    const e = fullEnv();
    const tok = await register(e);
    const r = await handle(post("/v1/activity", { relay_token: tok, content_state: state }), e, deps(fakeAPNs({ status: 410, reason: "Unregistered" }).fetch));
    expect(r.status).toBe(410);
  });

  it("refuses a bad token with 401", async () => {
    const other = await sealToken("another-secret", { v: 1, device_token: DEVICE, environment: "production", issued_at: 1 });
    const apns = fakeAPNs();
    const r = await handle(post("/v1/activity", { relay_token: other, content_state: state }), fullEnv(), deps(apns.fetch));
    expect(r.status).toBe(401);
    expect(apns.calls).toHaveLength(0);
  });

  it.each([
    ["no relay_token", { content_state: state }],
    ["no content state", { relay_token: "x" }],
    ["a list as content state", { relay_token: "x", content_state: [1] }],
    ["a huge content state", { relay_token: "x", content_state: { pad: "x".repeat(3600) } }],
    ["a bad event", { relay_token: "x", content_state: state, event: "start" }],
    ["a bad stale date", { relay_token: "x", content_state: state, stale_date: "soon" }],
    ["a bad priority", { relay_token: "x", content_state: state, priority: 1 }],
  ])("rejects %s with 400", async (_name, body) => {
    const r = await handle(post("/v1/activity", body), fullEnv(), deps(fakeAPNs().fetch));
    expect(r.status).toBe(400);
  });

  it("is rate limited per activity token", async () => {
    const e = fullEnv({ SEND_LIMITER: { limit: async () => ({ success: false }) } });
    const tok = await register(e);
    const r = await handle(post("/v1/activity", { relay_token: tok, content_state: state }), e, deps(fakeAPNs().fetch));
    expect(r.status).toBe(429);
  });
});

describe("APNs errors", () => {
  it.each([
    [{ status: 410, reason: "Unregistered" }, 410, "Unregistered"],
    [{ status: 400, reason: "BadDeviceToken" }, 410, "BadDeviceToken"],
    [{ status: 400, reason: "DeviceTokenNotForTopic" }, 410, "DeviceTokenNotForTopic"],
    [{ status: 400, reason: "PayloadTooLarge" }, 502, "PayloadTooLarge"],
    [{ status: 429, reason: "TooManyRequests" }, 502, "TooManyRequests"],
    [{ status: 403, reason: "InvalidProviderToken" }, 502, "InvalidProviderToken"],
    [{ status: 500 }, 502, ""],
  ])("APNs %o answers %i", async (answer, want, reason) => {
    const e = fullEnv();
    const tok = await register(e);
    const r = await handle(post("/v1/send", { relay_token: tok, payload }), e, deps(fakeAPNs(answer).fetch));
    expect(r.status).toBe(want);
    expect(((await r.json()) as { reason: string }).reason).toBe(reason);
  });

  it("mints a fresh provider token once on ExpiredProviderToken", async () => {
    const e = fullEnv();
    const tok = await register(e);
    let t = 1_000_000;
    const apns = fakeAPNs({ status: 403, reason: "ExpiredProviderToken" }, { status: 200 });
    const r = await handle(post("/v1/send", { relay_token: tok, payload }), e, deps(apns.fetch, () => (t += 1000)));
    expect(r.status).toBe(200);
    expect(apns.calls).toHaveLength(2);
    expect(apns.calls[0].headers.authorization).not.toBe(apns.calls[1].headers.authorization);
  });

  it("reuses the provider token for 50 minutes", async () => {
    const e = fullEnv();
    const tok = await register(e);
    let t = 1_000_000_000;
    const apns = fakeAPNs();
    const send = () => handle(post("/v1/send", { relay_token: tok, payload }), e, deps(apns.fetch, () => t));
    await send();
    t += 49 * 60 * 1000;
    await send();
    t += 2 * 60 * 1000;
    await send();
    const auth = apns.calls.map((c) => c.headers.authorization);
    expect(auth[0]).toBe(auth[1]);
    expect(auth[2]).not.toBe(auth[1]);
  });
});

describe("rate limits", () => {
  it("limits sends per device, whatever relay token it uses", async () => {
    const keys: string[] = [];
    const limiter: RateLimit = {
      limit: async ({ key }) => {
        keys.push(key);
        return { success: keys.length <= 1 };
      },
    };
    const e = fullEnv({ SEND_LIMITER: limiter });
    const apns = fakeAPNs();
    const r1 = await handle(post("/v1/send", { relay_token: await register(e), payload }), e, deps(apns.fetch));
    const r2 = await handle(post("/v1/send", { relay_token: await register(e), payload }), e, deps(apns.fetch));
    expect(r1.status).toBe(200);
    expect(r2.status).toBe(429);
    expect(apns.calls).toHaveLength(1);
    expect(keys[0]).toBe(keys[1]);
    expect(keys[0]).not.toContain(DEVICE);
  });

  it("limits register per client IP", async () => {
    const keys: string[] = [];
    const limiter: RateLimit = {
      limit: async ({ key }) => {
        keys.push(key);
        return { success: false };
      },
    };
    const r = await handle(post("/v1/register", { device_token: DEVICE, environment: "production", bundle_id: TOPIC }, { "cf-connecting-ip": "198.51.100.7" }), fullEnv({ REGISTER_LIMITER: limiter }), deps(fakeAPNs().fetch));
    expect(r.status).toBe(429);
    expect(keys).toEqual(["ip:198.51.100.7"]);
  });

  it("the configured register binding stops a burst from one IP", async () => {
    // The real [[ratelimits]] binding from wrangler.toml (10 a minute).
    const e = fullEnv({ REGISTER_LIMITER: testEnv.REGISTER_LIMITER });
    const statuses: number[] = [];
    for (let i = 0; i < 12; i++) {
      const r = await handle(post("/v1/register", { device_token: DEVICE, environment: "production", bundle_id: TOPIC }, { "cf-connecting-ip": "203.0.113.9" }), e, deps(fakeAPNs().fetch));
      statuses.push(r.status);
    }
    expect(statuses.slice(0, 10).every((s) => s === 200)).toBe(true);
    expect(statuses.slice(10)).toEqual([429, 429]);
  });
});

describe("logging", () => {
  it("never logs a device token, relay token or ciphertext", async () => {
    const lines: string[] = [];
    const spy = vi.spyOn(console, "log").mockImplementation((...a: unknown[]) => void lines.push(a.map(String).join(" ")));
    try {
      const e = fullEnv();
      const tok = await register(e);
      await handle(post("/v1/send", { relay_token: tok, payload }), e, deps(fakeAPNs({ status: 410, reason: "Unregistered" }).fetch));
      expect(lines.length).toBeGreaterThan(0);
      for (const l of lines) {
        expect(l).not.toContain(DEVICE);
        expect(l).not.toContain(tok);
        expect(l).not.toContain(payload.ciphertext);
      }
    } finally {
      spy.mockRestore();
    }
  });
});
