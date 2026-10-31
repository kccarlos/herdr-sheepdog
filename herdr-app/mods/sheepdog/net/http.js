// The panel's HTTP calls: the conversation, settings, pairing and agent
// routes, plus the user actions /api/turn, /api/pending/answer,
// /api/conversation/reset, the two model switches and device removal.
// `fetch` is the mod runtime's (buffered, so only for these small JSON
// answers; the event stream is net/sse.js), passed in so node tests it.
// Every POST carries an Idempotency-Key (docs/dev/ws-contract.md).

// What went wrong, for the store. `kind`: "down" (nothing answered), "http"
// (an error status, `status`; for a POST the message is the daemon's own
// `error` words) or "invalid" (it answered, but not as sheepdog does).
export class DaemonError extends Error {
  constructor(kind, message, status = 0) {
    super(message);
    this.kind = kind;
    this.status = status;
  }
}

export function createHttp({ fetch, base }) {
  async function requestJson(path, { method = "GET", query = {}, body, key } = {}) {
    const params = Object.entries(query).filter(([, v]) => v != null && v !== "").map(([k, v]) => `${k}=${encodeURIComponent(v)}`);
    const url = `${base}${path}${params.length ? `?${params.join("&")}` : ""}`;
    let res;
    try {
      const options = method === "GET" ? undefined : {
        method,
        headers: {
          "Content-Type": "application/json",
          ...(key ? { "Idempotency-Key": key } : {}),
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      };
      res = options ? await fetch(url, options) : await fetch(url);
    } catch {
      throw new DaemonError("down", "nothing answered");
    }
    let answer = null;
    try {
      answer = await res.json();
    } catch {
      // No JSON body: an error status still has a useful status to show.
    }
    if (!res.ok) throw new DaemonError("http", answer?.error || `Sheepdog answered ${path} with HTTP ${res.status}`, res.status);
    if (!answer || typeof answer !== "object") throw new DaemonError("invalid", `${path} did not answer JSON: is this sheepdog?`);
    return answer;
  }

  // A POST of `body` as JSON with its Idempotency-Key. A refusal's message is
  // the daemon's `{"error": "…"}` (503 says why the orchestrator is off).
  async function postJson(path, body, key) {
    return requestJson(path, { method: "POST", body, key });
  }

  return {
    // The daemon's state. Anything without a `voice` is not sheepdog.
    async state() {
      const state = await requestJson("/api/state");
      if (!state || typeof state !== "object" || !state.voice) throw new DaemonError("invalid", "Something answered, but it is not sheepdog.");
      return state;
    },
    // One page of the conversation, newest last: the newest (no `before`) or
    // the one older than `before`.
    async conversation({ limit, before } = {}) {
      const page = await requestJson("/api/conversation", { query: { limit, before } });
      if (!page || !Array.isArray(page.items)) throw new DaemonError("invalid", "/api/conversation did not answer a page.");
      return page;
    },
    // GET /api/agents: the state table's rows for the agent-details overlay.
    async agents() {
      const agents = await requestJson("/api/agents");
      if (!Array.isArray(agents)) throw new DaemonError("invalid", "/api/agents did not answer a list.");
      return agents;
    },
    // The voice provider choices and the live one.
    voiceProviders: () => requestJson("/api/voice/providers"),
    setVoiceProvider: (provider) => requestJson("/api/voice/provider", { method: "PUT", body: { provider } }),
    // The orchestrator catalog. A discovery error is in the 200 answer.
    brainModels: (provider) => requestJson("/api/brain/models", { query: { provider } }),
    setBrainModel: (provider, model) => requestJson("/api/brain/model", { method: "PUT", body: { provider, model } }),
    // Chattiness and languages.
    settings: () => requestJson("/api/settings"),
    setSettings: (patch) => requestJson("/api/settings", { method: "PUT", body: patch }),
    // The setup page's paired devices. A remove is final once confirmed.
    async devices() {
      const answer = await requestJson("/api/devices");
      if (!Array.isArray(answer.devices)) throw new DaemonError("invalid", "/api/devices did not answer a list.");
      return answer.devices;
    },
    removeDevice: (id) => requestJson(`/api/devices/${encodeURIComponent(id)}`, { method: "DELETE" }),
    // The plain pairing link (the fallback under the QR) and its matrix.
    // Only the link request makes a new code: asking both with new=1 would
    // invalidate the code the other had just drawn.
    pair: (fresh = false) => requestJson("/api/pair", { query: fresh ? { new: 1 } : {} }),
    pairQR: () => requestJson("/api/pair/qr.json"),
    // A typed line for the orchestrator: { reply, turn_id, acted }. It lasts
    // as long as the orchestrator's turn; a retry with the same `key` replays
    // the reply instead of running the turn again.
    turn: (text, key) => postJson("/api/turn", { text }, key),
    // A tap on option `option` (1-based) of the open question `id`:
    // { ok, spoken }. 409 when the question is no longer open.
    answer: (id, option, key) => postJson("/api/pending/answer", { id, option }, key),
    // "New conversation": reset the orchestrator and voice context, not the
    // agents, questions, promises or recorded history.
    reset: (key) => postJson("/api/conversation/reset", { via: "app" }, key),
  };
}
