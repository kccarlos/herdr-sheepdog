// Fast wake probe and network-wake recovery for /voice: when a phone or
// laptop unlocks (visibilitychange, pageshow) or regains connectivity
// (online), an open /ws must answer a ping within 1.5 s or be replaced at
// once, and a slept backoff timer or stale pre-sleep connect attempt starts
// over immediately. Pure logic (no DOM) so node --test covers every case.

/** Deadline for a pong after a wake probe (vs the 10 s steady-state timeout). */
export const WAKE_PONG_TIMEOUT_MS = 1500;
/** A connecting socket younger than this was started by a sibling wake event. */
export const FRESH_CONNECT_MS = 1000;
/** Minimum gap between wake side-effects (state / banner refresh) on burst events. */
export const WAKE_DEBOUNCE_MS = 500;

export function createWakeController({
  now = () => performance.now(),
  setTimer = (fn, ms) => setTimeout(fn, ms),
  clearTimer = (id) => clearTimeout(id),
  getState,
  sendPing,
  dropAndReconnect,
  reconnectNow,
  onWake,
}) {
  let wakeTimer = 0;
  let probedSocket = null;
  let lastWakeAt = -Infinity;

  function reset() {
    if (wakeTimer) {
      clearTimer(wakeTimer);
      wakeTimer = 0;
    }
    probedSocket = null;
  }

  function onPong() {
    reset();
  }

  function wake() {
    const st = getState();
    if (!st || st.hidden) return;
    const t = now();
    if (onWake && t - lastWakeAt >= WAKE_DEBOUNCE_MS) {
      lastWakeAt = t;
      onWake();
    }
    if (st.userDisconnected) return;

    if (st.wsState === 'open' && st.ws) {
      if (wakeTimer && probedSocket === st.ws) return;
      reset();
      const sock = st.ws;
      probedSocket = sock;
      sendPing();
      wakeTimer = setTimer(() => {
        wakeTimer = 0;
        probedSocket = null;
        const cur = getState();
        if (cur && !cur.userDisconnected && cur.ws === sock) {
          dropAndReconnect(sock);
        }
      }, WAKE_PONG_TIMEOUT_MS);
      return;
    }

    if (st.wsState === 'connecting' && st.ws) {
      if (t - (st.connectStartedAt || 0) < FRESH_CONNECT_MS) return;
      reset();
      dropAndReconnect(st.ws);
      return;
    }

    if (st.wsState === 'closed' || st.hasReconnectTimer) {
      reset();
      reconnectNow();
    }
  }

  return { wake, onPong, reset };
}
