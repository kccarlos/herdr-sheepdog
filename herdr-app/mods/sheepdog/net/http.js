// The panel's HTTP reads: /api/state and /api/conversation. `fetch` is the mod
// runtime's (buffered, so only for these small JSON answers; the event stream
// is net/sse.js), passed in so node tests it. Read-only: this PR POSTs nothing.

// What went wrong, for the store. `kind`: "down" (nothing answered), "http"
// (an error status) or "invalid" (it answered, but not as sheepdog does).
export class DaemonError extends Error {
  constructor(kind, message, status = 0) {
    super(message);
    this.kind = kind;
    this.status = status;
  }
}

export function createHttp({ fetch, base }) {
  async function getJson(path, query = {}) {
    const params = Object.entries(query).filter(([, v]) => v != null && v !== "").map(([k, v]) => `${k}=${encodeURIComponent(v)}`);
    const url = `${base}${path}${params.length ? `?${params.join("&")}` : ""}`;
    let res;
    try {
      res = await fetch(url);
    } catch {
      throw new DaemonError("down", "nothing answered");
    }
    if (!res.ok) throw new DaemonError("http", `Sheepdog answered ${path} with HTTP ${res.status}`, res.status);
    try {
      return await res.json();
    } catch {
      throw new DaemonError("invalid", `${path} did not answer JSON: is this sheepdog?`);
    }
  }

  return {
    // The daemon's state. Anything without a `voice` is not sheepdog.
    async state() {
      const state = await getJson("/api/state");
      if (!state || typeof state !== "object" || !state.voice) throw new DaemonError("invalid", "Something answered, but it is not sheepdog.");
      return state;
    },
    // One page of the conversation, newest last: the newest (no `before`) or
    // the one older than `before`.
    async conversation({ limit, before } = {}) {
      const page = await getJson("/api/conversation", { limit, before });
      if (!page || !Array.isArray(page.items)) throw new DaemonError("invalid", "/api/conversation did not answer a page.");
      return page;
    },
  };
}
