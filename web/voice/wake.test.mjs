import { test } from 'node:test';
import assert from 'node:assert/strict';
import { WAKE_PONG_TIMEOUT_MS, createWakeController } from './wake.js';

function fakeClock(start = 10_000) {
  let t = start;
  let nextId = 1;
  const timers = new Map();
  return {
    now: () => t,
    advance(ms) {
      t += ms;
      for (const [id, entry] of [...timers.entries()]) {
        if (entry.at <= t) {
          timers.delete(id);
          entry.fn();
        }
      }
    },
    setTimer(fn, ms) {
      const id = nextId++;
      timers.set(id, { at: t + ms, fn });
      return id;
    },
    clearTimer(id) {
      timers.delete(id);
    },
    pendingCount: () => timers.size,
  };
}

test('wake probes an open socket and drops + reconnects after 1500 ms without pong', () => {
  assert.equal(WAKE_PONG_TIMEOUT_MS, 1500);
  const clock = fakeClock();
  const sock = { id: 'ws-1' };
  let pings = 0;
  let wakes = 0;
  const dropped = [];
  let reconnected = 0;
  const st = { hidden: false, userDisconnected: false, wsState: 'open', ws: sock, connectStartedAt: 1000, hasReconnectTimer: false };

  const ctrl = createWakeController({
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
    getState: () => st,
    sendPing: () => { pings++; return true; },
    dropAndReconnect: (s) => dropped.push(s),
    reconnectNow: () => { reconnected++; },
    onWake: () => { wakes++; },
  });

  ctrl.wake();
  // Simultaneous online event 40 ms after visibilitychange does not double-ping or double-fetch.
  clock.advance(40);
  ctrl.wake();
  assert.equal(pings, 1);
  assert.equal(wakes, 1);
  assert.equal(dropped.length, 0);

  // Still waiting at 1490 ms total; drops at 1500 ms.
  clock.advance(1450);
  assert.equal(dropped.length, 0);
  clock.advance(20);
  assert.deepEqual(dropped, [sock]);
  assert.equal(reconnected, 0);
});

test('pong within 1500 ms cancels the wake drop', () => {
  const clock = fakeClock();
  const sock = { id: 'ws-live' };
  const dropped = [];
  const st = { hidden: false, userDisconnected: false, wsState: 'open', ws: sock, connectStartedAt: 1000, hasReconnectTimer: false };

  const ctrl = createWakeController({
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
    getState: () => st,
    sendPing: () => true,
    dropAndReconnect: (s) => dropped.push(s),
    reconnectNow: () => {},
  });

  ctrl.wake();
  clock.advance(400);
  ctrl.onPong();
  assert.equal(clock.pendingCount(), 0);
  clock.advance(5000);
  assert.equal(dropped.length, 0);
});

test('wake reconnects immediately when socket is closed with a slept backoff timer', () => {
  const clock = fakeClock();
  let reconnected = 0;
  const st = { hidden: false, userDisconnected: false, wsState: 'closed', ws: null, connectStartedAt: 0, hasReconnectTimer: true };

  const ctrl = createWakeController({
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
    getState: () => st,
    sendPing: () => true,
    dropAndReconnect: () => {},
    reconnectNow: () => { reconnected++; },
  });

  ctrl.wake();
  assert.equal(reconnected, 1);
});

test('wake replaces a stale connecting socket (>1s old) but keeps a fresh one (<1s old)', () => {
  const clock = fakeClock(20_000);
  const dropped = [];
  const freshSock = { id: 'fresh' };
  const st = { hidden: false, userDisconnected: false, wsState: 'connecting', ws: freshSock, connectStartedAt: 19_500, hasReconnectTimer: false };

  const ctrl = createWakeController({
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
    getState: () => st,
    sendPing: () => true,
    dropAndReconnect: (s) => dropped.push(s),
    reconnectNow: () => {},
  });

  // 500 ms old: keep it (e.g. online firing right after visibilitychange started connecting).
  ctrl.wake();
  assert.equal(dropped.length, 0);

  // 2500 ms old (started before sleep): replace it immediately.
  const staleSock = { id: 'stale' };
  st.ws = staleSock;
  st.connectStartedAt = 17_500;
  ctrl.wake();
  assert.deepEqual(dropped, [staleSock]);
});

test('wake does nothing while hidden, and never opens /ws when userDisconnected', () => {
  const clock = fakeClock();
  let pings = 0;
  let reconnected = 0;
  let wakes = 0;
  const st = { hidden: true, userDisconnected: false, wsState: 'closed', ws: null, connectStartedAt: 0, hasReconnectTimer: true };

  const ctrl = createWakeController({
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
    getState: () => st,
    sendPing: () => { pings++; return true; },
    dropAndReconnect: () => {},
    reconnectNow: () => { reconnected++; },
    onWake: () => { wakes++; },
  });

  ctrl.wake();
  assert.equal(wakes, 0);
  assert.equal(reconnected, 0);

  // Visible, but user has not clicked Connect: onWake runs (for approvals / banner), /ws stays idle.
  st.hidden = false;
  st.userDisconnected = true;
  ctrl.wake();
  assert.equal(wakes, 1);
  assert.equal(pings, 0);
  assert.equal(reconnected, 0);
});
