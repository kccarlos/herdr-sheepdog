// The panel's HTTP calls: the reads /api/state and /api/conversation, and the
// POSTs it makes: /api/turn (a typed line), /api/pending/answer (a tap on a
// question) and the voice's /api/voice/mute, /stop and /ptt. `fetch` is the mod runtime's (buffered, so only for these
// small JSON answers; the event stream is net/sse.js), passed in so node tests
// it. Every POST carries an Idempotency-Key (docs/dev/ws-contract.md).

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

  // A POST of `body` as JSON with its Idempotency-Key. A refusal's message is
  // the daemon's `{"error": "…"}` (503 says why the orchestrator is off).
  async function postJson(path, body, key) {
    let res;
    try {
      res = await fetch(`${base}${path}`, { method: "POST", headers: { "Content-Type": "application/json", "Idempotency-Key": key }, body: JSON.stringify(body) });
    } catch {
      throw new DaemonError("down", "nothing answered");
    }
    let answer = null;
    try {
      answer = await res.json();
    } catch {
      // No JSON body: the status says what it was.
    }
    if (!res.ok) throw new DaemonError("http", answer?.error || `Sheepdog answered ${path} with HTTP ${res.status}`, res.status);
    if (!answer || typeof answer !== "object") throw new DaemonError("invalid", `${path} did not answer JSON: is this sheepdog?`);
    return answer;
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
    // A typed line for the orchestrator: { reply, turn_id, acted }. It lasts
    // as long as the orchestrator's turn; a retry with the same `key` replays
    // the reply instead of running the turn again.
    turn: (text, key) => postJson("/api/turn", { text }, key),
    // A tap on option `option` (1-based) of the open question `id`:
    // { ok, spoken }. 409 when the question is no longer open.
    answer: (id, option, key) => postJson("/api/pending/answer", { id, option }, key),
    // The voice's controls, relayed by the daemon to whoever holds it: mute or
    // unmute, cut the speech, hold-to-talk on or off. Each answers { ok } (mute
    // { muted }); 409 when no client holds the voice, 503 when it is off.
    mute: (on, key) => postJson("/api/voice/mute", { on }, key),
    stop: (key) => postJson("/api/voice/stop", undefined, key),
    ptt: (on, key) => postJson("/api/voice/ptt", { on }, key),
  };
}
