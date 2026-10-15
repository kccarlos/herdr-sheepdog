// Relay tokens: the device token and its APNs environment, sealed with
// AES-256-GCM under a key derived from RELAY_SECRET. The relay keeps no
// state; a token is only readable by a relay holding the same secret.
//
// Wire format (base64url, no padding):
//   0x01 (version) || iv (12 bytes) || AES-GCM ciphertext + 16-byte tag
// The plaintext is JSON {v: 1, device_token, environment, issued_at}.

export type Environment = "production" | "sandbox";

export interface TokenClaims {
  v: 1;
  device_token: string;
  environment: Environment;
  issued_at: number; // unix seconds
}

const VERSION = 0x01;
const AAD = new TextEncoder().encode("sheepdog-relay-token-v1");

const keyCache = new Map<string, Promise<CryptoKey>>();

/** The AES key: SHA-256 of RELAY_SECRET, so the secret can be any string. */
function aesKey(secret: string): Promise<CryptoKey> {
  let k = keyCache.get(secret);
  if (!k) {
    k = crypto.subtle
      .digest("SHA-256", new TextEncoder().encode(secret))
      .then((raw) => crypto.subtle.importKey("raw", raw, { name: "AES-GCM" }, false, ["encrypt", "decrypt"]));
    keyCache.set(secret, k);
  }
  return k;
}

export function isEnvironment(v: unknown): v is Environment {
  return v === "production" || v === "sandbox";
}

/** A device token: lowercase hex, an even count of 64 to 200 digits. */
export function isDeviceToken(v: unknown): v is string {
  return typeof v === "string" && v.length >= 64 && v.length <= 200 && v.length % 2 === 0 && /^[0-9a-f]+$/.test(v);
}

export async function sealToken(secret: string, claims: TokenClaims): Promise<string> {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const plain = new TextEncoder().encode(JSON.stringify(claims));
  const ct = new Uint8Array(
    await crypto.subtle.encrypt({ name: "AES-GCM", iv, additionalData: AAD }, await aesKey(secret), plain),
  );
  const out = new Uint8Array(1 + iv.length + ct.length);
  out[0] = VERSION;
  out.set(iv, 1);
  out.set(ct, 1 + iv.length);
  return b64urlEncode(out);
}

/** Opens a relay token; null when it is malformed, tampered with or sealed under another secret. */
export async function openToken(secret: string, token: string): Promise<TokenClaims | null> {
  if (typeof token !== "string" || token.length > 1024) return null;
  const raw = b64urlDecode(token);
  if (!raw || raw.length < 1 + 12 + 16 || raw[0] !== VERSION) return null;
  let plain: ArrayBuffer;
  try {
    plain = await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: raw.subarray(1, 13), additionalData: AAD },
      await aesKey(secret),
      raw.subarray(13),
    );
  } catch {
    return null;
  }
  let c: unknown;
  try {
    c = JSON.parse(new TextDecoder().decode(plain));
  } catch {
    return null;
  }
  const t = c as Partial<TokenClaims>;
  if (t.v !== 1 || !isDeviceToken(t.device_token) || !isEnvironment(t.environment) || typeof t.issued_at !== "number") {
    return null;
  }
  return t as TokenClaims;
}

export function b64urlEncode(b: Uint8Array): string {
  let s = "";
  for (const x of b) s += String.fromCharCode(x);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function b64urlDecode(s: string): Uint8Array | null {
  if (!/^[A-Za-z0-9_-]*$/.test(s)) return null;
  return b64Decode(s.replace(/-/g, "+").replace(/_/g, "/"));
}

/** Standard base64 (padding optional); null when it is not base64. */
export function b64Decode(s: string): Uint8Array | null {
  try {
    const pad = s.length % 4 === 0 ? "" : "=".repeat(4 - (s.length % 4));
    const bin = atob(s + pad);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  } catch {
    return null;
  }
}
