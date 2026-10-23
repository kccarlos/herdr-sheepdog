// sheepdog's push relay: a stateless Cloudflare Worker between sheepdog
// daemons and APNs. It holds the Apple key, so a daemon needs none; it
// never sees a notification's content (the daemon encrypts that with a
// key only the phone has). See README.md and docs/dev/push.md.

import { sendAPNs, type APNsCredentials } from "./apns";
import { b64Decode, isDeviceToken, isEnvironment, openToken, sealToken } from "./token";

export const VERSION = "1.1.0";

/** Request body caps (bytes). */
export const MAX_REGISTER_BYTES = 1024;
export const MAX_SEND_BYTES = 8 * 1024;
/** The base64 ciphertext cap, so the whole APNs payload stays under Apple's 4096 bytes. */
export const MAX_CIPHERTEXT = 3500;
/** The Live Activity content state cap (its JSON), for the same reason. */
export const MAX_CONTENT_STATE = 3500;

export interface Env {
  // Secrets (wrangler secret put).
  APNS_KEY?: string;
  APNS_KEY_ID?: string;
  APNS_TEAM_ID?: string;
  RELAY_SECRET?: string;
  // Vars (wrangler.toml).
  APNS_TOPIC?: string;
  // Rate Limiting bindings (wrangler.toml [[ratelimits]]).
  SEND_LIMITER?: RateLimit;
  REGISTER_LIMITER?: RateLimit;
}

export interface Deps {
  fetch: typeof fetch;
  now: () => number;
}

export default {
  fetch(request: Request, env: Env): Promise<Response> {
    return handle(request, env, { fetch: (input, init) => fetch(input, init), now: Date.now });
  },
} satisfies ExportedHandler<Env>;

export async function handle(request: Request, env: Env, deps: Deps): Promise<Response> {
  const url = new URL(request.url);
  let resp: Response;
  try {
    if (url.pathname === "/v1/health" && request.method === "GET") {
      resp = json(200, { ok: missingConfig(env).length === 0, version: VERSION });
    } else if (url.pathname === "/v1/register") {
      resp = request.method === "POST" ? await register(request, env) : json(405, { error: "method not allowed" });
    } else if (url.pathname === "/v1/send") {
      resp = request.method === "POST" ? await send(request, env, deps) : json(405, { error: "method not allowed" });
    } else if (url.pathname === "/v1/activity") {
      resp = request.method === "POST" ? await activity(request, env, deps) : json(405, { error: "method not allowed" });
    } else {
      resp = json(404, { error: "not found" });
    }
  } catch {
    resp = json(500, { error: "internal error" });
  }
  // Counts only: the route and the status, never a token or a payload.
  console.log(JSON.stringify({ route: url.pathname.slice(0, 32), status: resp.status }));
  return resp;
}

function missingConfig(env: Env): string[] {
  return (["APNS_KEY", "APNS_KEY_ID", "APNS_TEAM_ID", "RELAY_SECRET", "APNS_TOPIC"] as const).filter((k) => !env[k]);
}

async function register(request: Request, env: Env): Promise<Response> {
  if (!env.RELAY_SECRET || !env.APNS_TOPIC) return json(503, { error: "relay not configured" });
  const ip = request.headers.get("cf-connecting-ip") ?? "unknown";
  if (env.REGISTER_LIMITER && !(await env.REGISTER_LIMITER.limit({ key: `ip:${ip}` })).success) {
    return json(429, { error: "rate limited" });
  }
  const body = await readJSON(request, MAX_REGISTER_BYTES);
  if (body === TOO_LARGE) return json(413, { error: "body too large" });
  if (!isObject(body)) return json(400, { error: "body must be a JSON object" });
  const tok = typeof body.device_token === "string" ? body.device_token.toLowerCase() : undefined;
  if (!isDeviceToken(tok)) return json(400, { error: "device_token must be a hex APNs device token" });
  if (!isEnvironment(body.environment)) return json(400, { error: 'environment must be "production" or "sandbox"' });
  if (body.bundle_id !== env.APNS_TOPIC) return json(400, { error: "bundle_id is not the app this relay serves" });
  const relayToken = await sealToken(env.RELAY_SECRET, {
    v: 1,
    device_token: tok,
    environment: body.environment,
    issued_at: Math.floor(Date.now() / 1000),
  });
  return json(200, { relay_token: relayToken });
}

async function send(request: Request, env: Env, deps: Deps): Promise<Response> {
  const missing = missingConfig(env);
  if (missing.length > 0) return json(503, { error: "relay not configured" });
  const body = await readJSON(request, MAX_SEND_BYTES);
  if (body === TOO_LARGE) return json(413, { error: "body too large" });
  if (!isObject(body)) return json(400, { error: "body must be a JSON object" });
  if (typeof body.relay_token !== "string") return json(400, { error: "relay_token is required" });
  const p = body.payload;
  if (!isObject(p)) return json(400, { error: "payload must be an object" });
  if (typeof p.ciphertext !== "string" || p.ciphertext.length === 0 || p.ciphertext.length > MAX_CIPHERTEXT || !b64Decode(p.ciphertext)) {
    return json(400, { error: `payload.ciphertext must be base64, at most ${MAX_CIPHERTEXT} characters` });
  }
  const nonce = typeof p.nonce === "string" ? b64Decode(p.nonce) : null;
  if (!nonce || nonce.length !== 12) return json(400, { error: "payload.nonce must be 12 bytes of base64" });
  if (p.kind !== undefined && (typeof p.kind !== "string" || !/^[a-z_]{1,32}$/.test(p.kind))) {
    return json(400, { error: "payload.kind must be a short lowercase word" });
  }
  if (p.collapse_id !== undefined && (typeof p.collapse_id !== "string" || p.collapse_id.length === 0 || new TextEncoder().encode(p.collapse_id).length > 64)) {
    return json(400, { error: "payload.collapse_id must be 1 to 64 bytes" });
  }
  const priority = body.priority === undefined ? 10 : body.priority;
  if (priority !== 5 && priority !== 10) return json(400, { error: "priority must be 5 or 10" });

  const claims = await openToken(env.RELAY_SECRET!, body.relay_token);
  if (!claims) return json(401, { error: "bad relay_token" });

  // Limit per device, not per relay token: a device can mint any number of tokens.
  if (env.SEND_LIMITER && !(await env.SEND_LIMITER.limit({ key: `dev:${await shortHash(claims.device_token)}` })).success) {
    return json(429, { error: "rate limited" });
  }

  const creds: APNsCredentials = { key: env.APNS_KEY!, keyId: env.APNS_KEY_ID!, teamId: env.APNS_TEAM_ID! };
  const r = await sendAPNs(
    creds,
    {
      deviceToken: claims.device_token,
      environment: claims.environment,
      topic: env.APNS_TOPIC!,
      priority,
      collapseId: p.collapse_id as string | undefined,
      body: JSON.stringify(notification(p.ciphertext, p.nonce as string, p.kind as string | undefined)),
    },
    deps.fetch,
    deps.now,
  );
  return apnsAnswer(r);
}

/**
 * POST /v1/activity: one Live Activity update (or end) for an activity
 * push token the daemon registered like a device token. The content
 * state passes through as given; the daemon seals everything but the
 * voice's phase and the counts with the phone's key (the widget opens
 * it), so the relay sees no agent name or question here either.
 */
async function activity(request: Request, env: Env, deps: Deps): Promise<Response> {
  if (missingConfig(env).length > 0) return json(503, { error: "relay not configured" });
  const body = await readJSON(request, MAX_SEND_BYTES);
  if (body === TOO_LARGE) return json(413, { error: "body too large" });
  if (!isObject(body)) return json(400, { error: "body must be a JSON object" });
  if (typeof body.relay_token !== "string") return json(400, { error: "relay_token is required" });
  const event = body.event === undefined ? "update" : body.event;
  if (event !== "update" && event !== "end") return json(400, { error: 'event must be "update" or "end"' });
  if (!isObject(body.content_state) || JSON.stringify(body.content_state).length > MAX_CONTENT_STATE) {
    return json(400, { error: `content_state must be an object, at most ${MAX_CONTENT_STATE} bytes of JSON` });
  }
  for (const k of ["timestamp", "stale_date", "dismissal_date"] as const) {
    const v = body[k];
    if (v !== undefined && (typeof v !== "number" || !Number.isInteger(v) || v < 0)) {
      return json(400, { error: `${k} must be a Unix time in seconds` });
    }
  }
  const priority = body.priority === undefined ? 5 : body.priority;
  if (priority !== 5 && priority !== 10) return json(400, { error: "priority must be 5 or 10" });

  const claims = await openToken(env.RELAY_SECRET!, body.relay_token);
  if (!claims) return json(401, { error: "bad relay_token" });
  if (env.SEND_LIMITER && !(await env.SEND_LIMITER.limit({ key: `act:${await shortHash(claims.device_token)}` })).success) {
    return json(429, { error: "rate limited" });
  }

  const aps: Record<string, unknown> = {
    timestamp: (body.timestamp as number | undefined) ?? Math.floor(deps.now() / 1000),
    event,
    "content-state": body.content_state,
  };
  if (body.stale_date !== undefined) aps["stale-date"] = body.stale_date;
  if (body.dismissal_date !== undefined) aps["dismissal-date"] = body.dismissal_date;
  const creds: APNsCredentials = { key: env.APNS_KEY!, keyId: env.APNS_KEY_ID!, teamId: env.APNS_TEAM_ID! };
  const r = await sendAPNs(
    creds,
    {
      deviceToken: claims.device_token,
      environment: claims.environment,
      topic: `${env.APNS_TOPIC!}.push-type.liveactivity`,
      pushType: "liveactivity",
      priority,
      body: JSON.stringify({ aps }),
    },
    deps.fetch,
    deps.now,
  );
  return apnsAnswer(r);
}

function apnsAnswer(r: { status: number; reason: string }): Response {
  if (r.status === 200) return json(200, { ok: true });
  if (r.status === 410 || (r.status === 400 && (r.reason === "BadDeviceToken" || r.reason === "DeviceTokenNotForTopic"))) {
    return json(410, { error: "device token is no longer valid", reason: r.reason || "Unregistered" });
  }
  return json(502, { error: "apns refused", status: r.status, reason: r.reason });
}

/**
 * The APNs payload: a placeholder alert the app's Notification Service
 * Extension replaces with the decrypted text, and the ciphertext. The
 * category and interruption level stay in the clear because an extension
 * cannot change them; they follow from kind.
 */
export function notification(ciphertext: string, nonce: string, kind?: string): Record<string, unknown> {
  const aps: Record<string, unknown> = {
    alert: { title: "Sheepdog", body: "New update" },
    "mutable-content": 1,
    sound: "default",
  };
  if (kind) {
    aps.category = kind === "approval" ? "APPROVAL" : "AGENT";
    aps["interruption-level"] = kind === "approval" || kind === "blocked" ? "time-sensitive" : "active";
  }
  return { aps, e2e: { c: ciphertext, n: nonce } };
}

const TOO_LARGE = Symbol("too large");

/** Reads at most max bytes of JSON; TOO_LARGE past it, undefined when it is not JSON. */
async function readJSON(request: Request, max: number): Promise<unknown> {
  const declared = Number(request.headers.get("content-length") ?? "0");
  if (declared > max) return TOO_LARGE;
  if (!request.body) return undefined;
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let n = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    n += value.length;
    if (n > max) {
      await reader.cancel();
      return TOO_LARGE;
    }
    chunks.push(value);
  }
  const buf = new Uint8Array(n);
  let off = 0;
  for (const c of chunks) {
    buf.set(c, off);
    off += c.length;
  }
  try {
    return JSON.parse(new TextDecoder().decode(buf));
  } catch {
    return undefined;
  }
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

async function shortHash(s: string): Promise<string> {
  const d = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s)));
  return Array.from(d.subarray(0, 12), (b) => b.toString(16).padStart(2, "0")).join("");
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}
