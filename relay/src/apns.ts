// APNs: the ES256 provider token and the HTTP/2 request. Workers' fetch
// speaks HTTP/2 to api.push.apple.com on its own.

import { b64Decode, b64urlEncode, type Environment } from "./token";

export const APNS_HOSTS: Record<Environment, string> = {
  production: "https://api.push.apple.com",
  sandbox: "https://api.sandbox.push.apple.com",
};

/** How long one provider token is reused: Apple rejects one older than an hour. */
export const JWT_REFRESH_MS = 50 * 60 * 1000;

export interface APNsCredentials {
  key: string; // the .p8 file's PEM text
  keyId: string;
  teamId: string;
}

interface CachedJWT {
  jwt: string;
  at: number;
  id: string; // keyId|teamId|key fingerprint the jwt was made for
}

let cached: CachedJWT | null = null;
let cachedKey: { pem: string; key: Promise<CryptoKey> } | null = null;

/** Drops the cached provider token (tests). */
export function resetJWTCache(): void {
  cached = null;
  cachedKey = null;
}

/** Imports a .p8 auth key: a PKCS#8 PEM P-256 key. */
export function importKey(pem: string): Promise<CryptoKey> {
  if (cachedKey && cachedKey.pem === pem) return cachedKey.key;
  const body = pem.replace(/-----(BEGIN|END) PRIVATE KEY-----/g, "").replace(/\s+/g, "");
  const der = b64Decode(body);
  const key = der
    ? crypto.subtle.importKey("pkcs8", der, { name: "ECDSA", namedCurve: "P-256" }, false, ["sign"])
    : Promise.reject(new Error("APNS_KEY is not a PEM key"));
  cachedKey = { pem, key };
  key.catch(() => {
    if (cachedKey?.key === key) cachedKey = null;
  });
  return key;
}

/** Signs a provider token: header {alg: ES256, kid}, claims {iss, iat}. Web Crypto's ECDSA signature is already JWS's raw r||s. */
export async function signJWT(creds: APNsCredentials, nowMs: number): Promise<string> {
  const enc = new TextEncoder();
  const h = b64urlEncode(enc.encode(JSON.stringify({ alg: "ES256", kid: creds.keyId })));
  const c = b64urlEncode(enc.encode(JSON.stringify({ iss: creds.teamId, iat: Math.floor(nowMs / 1000) })));
  const signing = `${h}.${c}`;
  const sig = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, await importKey(creds.key), enc.encode(signing));
  return `${signing}.${b64urlEncode(new Uint8Array(sig))}`;
}

/** The cached provider token, minted again after JWT_REFRESH_MS or when fresh is set. */
export async function providerToken(creds: APNsCredentials, nowMs: number, fresh = false): Promise<string> {
  const id = `${creds.keyId}|${creds.teamId}|${creds.key.length}`;
  if (!fresh && cached && cached.id === id && nowMs - cached.at < JWT_REFRESH_MS) return cached.jwt;
  const jwt = await signJWT(creds, nowMs);
  cached = { jwt, at: nowMs, id };
  return jwt;
}

export interface APNsResult {
  status: number;
  reason: string;
}

export interface APNsRequest {
  deviceToken: string;
  environment: Environment;
  topic: string;
  /** apns-push-type: "alert" (the default) or "liveactivity". */
  pushType?: "alert" | "liveactivity";
  priority: 5 | 10;
  collapseId?: string;
  body: string;
}

/** Posts one notification; a 403 ExpiredProviderToken mints a fresh token and retries once. */
export async function sendAPNs(
  creds: APNsCredentials,
  req: APNsRequest,
  fetcher: typeof fetch,
  now: () => number,
): Promise<APNsResult> {
  let r = await post(creds, req, fetcher, await providerToken(creds, now()));
  if (r.status === 403 && r.reason === "ExpiredProviderToken") {
    r = await post(creds, req, fetcher, await providerToken(creds, now(), true));
  }
  return r;
}

async function post(creds: APNsCredentials, req: APNsRequest, fetcher: typeof fetch, jwt: string): Promise<APNsResult> {
  const headers: Record<string, string> = {
    authorization: `bearer ${jwt}`,
    "apns-topic": req.topic,
    "apns-push-type": req.pushType ?? "alert",
    "apns-priority": String(req.priority),
    "content-type": "application/json",
  };
  if (req.collapseId) headers["apns-collapse-id"] = req.collapseId;
  const resp = await fetcher(`${APNS_HOSTS[req.environment]}/3/device/${req.deviceToken}`, {
    method: "POST",
    headers,
    body: req.body,
  });
  let reason = "";
  if (resp.status !== 200) {
    try {
      const j = (await resp.json()) as { reason?: unknown };
      if (typeof j.reason === "string") reason = j.reason.slice(0, 64);
    } catch {
      // no JSON body
    }
  } else {
    await resp.body?.cancel();
  }
  return { status: resp.status, reason };
}
