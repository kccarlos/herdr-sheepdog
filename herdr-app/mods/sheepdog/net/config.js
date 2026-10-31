// The one base URL the panel talks to (docs/dev/design/desktop-client.md,
// section 6.2). v1 is the daemon on this computer; v2 points it at
// Sheepdog.app's loopback proxy, and nothing else changes.

export const DEFAULT_URL = "http://127.0.0.1:7777";

// Where the daemon is: the mod's setting (props().url in a layout block, or
// the shared key sheepdog.url), else the default. A value that is not a plain
// http address, which is all the raw socket of net/sse.js speaks, is ignored.
export function baseUrl(setting) {
  const m = /^http:\/\/([^/:?#\s]+|\[[0-9a-f:]+\])(?::(\d{1,5}))?\/*$/i.exec(String(setting ?? "").trim());
  if (!m) return DEFAULT_URL;
  return `http://${m[1]}${m[2] ? `:${m[2]}` : ""}`;
}

// The host and port for `net.connect`, from a URL baseUrl() made.
export function hostPort(base) {
  const m = /^http:\/\/([^/:]+|\[[0-9a-f:]+\])(?::(\d+))?$/i.exec(base);
  return { host: m[1].replace(/^\[|\]$/g, ""), port: m[2] ? Number(m[2]) : 80, authority: m[1] + (m[2] ? `:${m[2]}` : "") };
}
