// The adaptive time-stretch (audio-util.js Wsola, stretchRate, slewRate,
// Player.setPolicy): pitch preserved, duration scaled, no clicks at frame
// joins, exact at rate 1, and the shared rate policy table
// (docs/ws-contract.md "Playback jitter buffer"; the iOS app matches it).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  Wsola, RingBuffer, Player, stretchRate, slewRate, STRETCH, PLAYBACK_POLICY, PLAYBACK_RATE,
} from './audio-util.js';

const RATE = PLAYBACK_RATE; // 24 kHz mono, as the daemon sends it

function sine(freq, n, amp = 0.5) {
  const out = new Float32Array(n);
  for (let i = 0; i < n; i++) out[i] = amp * Math.sin((2 * Math.PI * freq * i) / RATE);
  return out;
}

function newWsola() {
  return new Wsola({ frame: Math.round((RATE * STRETCH.frameMs) / 1000), search: Math.round((RATE * STRETCH.searchMs) / 1000) });
}

// Runs all of input through a stretcher at a fixed rate.
function stretch(input, rate) {
  const ring = new RingBuffer(input.length + 16);
  ring.write(input);
  const w = newWsola();
  const out = [];
  for (let guard = 0; ring.available + w.pending > 0; guard++) {
    if (guard > 1e6) throw new Error('stretcher never finished');
    w.produce(ring, rate);
    while (w.ready > 0) out.push(w.readOne());
  }
  return Float32Array.from(out);
}

// Frequency by rising zero crossings over the steady middle of x.
function zeroCrossingHz(x) {
  const from = Math.floor(x.length * 0.1);
  const to = Math.floor(x.length * 0.9);
  let first = -1;
  let last = -1;
  let n = 0;
  for (let i = from + 1; i < to; i++) {
    if (x[i - 1] < 0 && x[i] >= 0) {
      // Linear interpolation of the crossing instant.
      const t = i - 1 + -x[i - 1] / (x[i] - x[i - 1]);
      if (first < 0) first = t;
      last = t;
      n++;
    }
  }
  return ((n - 1) * RATE) / (last - first);
}

function maxDelta(x) {
  let m = 0;
  for (let i = 1; i < x.length; i++) m = Math.max(m, Math.abs(x[i] - x[i - 1]));
  return m;
}

test('WSOLA at 0.85x keeps a 440 Hz sine at 440 Hz and stretches its duration by 1/0.85', () => {
  const input = sine(440, RATE * 2);
  const out = stretch(input, 0.85);
  const hz = zeroCrossingHz(out);
  assert.ok(Math.abs(hz - 440) <= 2, `pitch ${hz.toFixed(2)} Hz`);
  const ratio = out.length / input.length;
  assert.ok(Math.abs(ratio - 1 / 0.85) / (1 / 0.85) <= 0.02, `duration ratio ${ratio.toFixed(4)}, want ${(1 / 0.85).toFixed(4)} ± 2 %`);
});

test('WSOLA at the 1.05x catch-up rate keeps pitch and shortens by 1/1.05', () => {
  const input = sine(440, RATE * 2);
  const out = stretch(input, 1.05);
  assert.ok(Math.abs(zeroCrossingHz(out) - 440) <= 2, `pitch ${zeroCrossingHz(out)}`);
  assert.ok(Math.abs(out.length / input.length - 1 / 1.05) / (1 / 1.05) <= 0.02, `ratio ${out.length / input.length}`);
});

test('WSOLA frame joins do not click: sample-to-sample steps stay within a sine\'s own slope', () => {
  const amp = 0.5;
  const natural = amp * 2 * Math.PI * 440 / RATE; // the steepest step of the clean sine
  for (const rate of [0.85, 0.9, 0.95, 1.05]) {
    const out = stretch(sine(440, RATE, amp), rate);
    const d = maxDelta(out);
    assert.ok(d <= natural * 1.1, `rate ${rate}: max step ${d.toFixed(4)} vs sine slope ${natural.toFixed(4)}`);
  }
});

// The similarity search is what keeps the joins in phase: a plain
// overlap-add (search 0) of the same frames cancels itself into a 10–30 %
// quieter, warbling tone; with the search the 10 ms loudness stays flat.
test('WSOLA joins are phase-aligned: the loudness of a stretched sine stays flat', () => {
  const want = 0.5 / Math.SQRT2;
  const W = 240;
  for (const rate of [0.85, 0.9, 1.05]) {
    const out = stretch(sine(440, RATE * 2), rate);
    for (let s = W * 4; s + W < out.length - W * 4; s += W / 2) {
      let e = 0;
      for (let i = 0; i < W; i++) e += out[s + i] ** 2;
      const r = Math.sqrt(e / W);
      assert.ok(Math.abs(r - want) / want <= 0.03, `rate ${rate}: rms ${r.toFixed(3)} at ${s}, want ${want.toFixed(3)} ± 3 %`);
    }
  }
});

test('WSOLA at rate 1 reconstructs its input exactly', () => {
  const input = new Float32Array(RATE);
  let seed = 7;
  for (let i = 0; i < input.length; i++) {
    seed = (seed * 1103515245 + 12345) & 0x7fffffff;
    input[i] = (seed / 0x7fffffff) * 2 - 1; // noise: no periodicity to hide behind
  }
  const out = stretch(input, 1);
  assert.equal(out.length, input.length);
  let err = 0;
  for (let i = 0; i < input.length; i++) err = Math.max(err, Math.abs(out[i] - input[i]));
  assert.ok(err < 1e-5, `max error ${err}`);
});

test('WSOLA passes a tail shorter than a frame straight through (never holds audio back)', () => {
  const input = sine(440, 300); // 12.5 ms < one 20 ms frame
  const out = stretch(input, 0.85);
  assert.deepEqual(Array.from(out), Array.from(input));
});

test('rate policy: 1.0 at the target, linear to 0.90 at T/2 and 0.85 at T/4; 1.05 above 2T only with lag debt', () => {
  const T = 800;
  // [b, rate with no debt, rate with debt]
  const table = [
    [0, 0.85, 0.85], [T / 8, 0.85, 0.85], [T / 4, 0.85, 0.85], [(3 * T) / 8, 0.875, 0.875], [T / 2, 0.9, 0.9],
    [(3 * T) / 4, 0.95, 0.95], [T, 1, 1], [1.5 * T, 1, 1], [2 * T, 1, 1], [2 * T + 1, 1, 1.05], [10 * T, 1, 1.05],
  ];
  for (const [b, noDebt, withDebt] of table) {
    assert.ok(Math.abs(stretchRate(b, T) - noDebt) < 1e-9, `b=${b}, no debt: ${stretchRate(b, T)} want ${noDebt}`);
    assert.ok(Math.abs(stretchRate(b, T, 0) - noDebt) < 1e-9, `b=${b}, debt 0: ${stretchRate(b, T, 0)}`);
    assert.ok(Math.abs(stretchRate(b, T, 240) - withDebt) < 1e-9, `b=${b}, debt: ${stretchRate(b, T, 240)} want ${withDebt}`);
  }
});

// Debt semantics in the Player: slowing down accrues (1 − r)·dt, catch-up
// at 1.05 pays (r − 1)·dt back and stops once it is paid, and a drain or
// flush (the end of a reply) clears it.
test('lag debt: accrues while slow, is paid back at 1.05, and resets at the end of a reply', () => {
  const p = new Player({ inRate: RATE, outRate: RATE });
  p.setPolicy(PLAYBACK_POLICY.openai); // GPT-Live policy
  const block = new Float32Array(128);
  const tone = sine(440, RATE * 20);
  // Start with 800 ms, then trickle at 0.5x until the rate is at its floor.
  p.push(tone.subarray(0, RATE * 0.8));
  let at = RATE * 0.8;
  let rendered = 0;
  while (p.rate > STRETCH.minRate + 1e-9 || p.debt === 0) {
    if (rendered % 256 === 0) { p.push(tone.subarray(at, at + 64)); at += 64; }
    p.render(block);
    rendered += 128;
    assert.ok(rendered < RATE * 10, 'never slowed down');
  }
  assert.ok(p.debt > 0, 'slowing down accrued debt');
  // A burst far above 2T: catch-up allowed while the debt lasts.
  p.push(tone.subarray(at, at + RATE * 5));
  at += RATE * 5;
  let fast = 0;
  let after = 0;
  for (let i = 0; i < 2000; i++) {
    p.render(block);
    if (p.rate > 1) fast++;
    if (p.debt === 0 && p.rate <= 1) after++;
    if (after > 50) break;
  }
  assert.ok(fast > 0, 'caught up at 1.05 while in debt');
  assert.equal(p.debt, 0, 'debt paid back');
  assert.equal(p.rate, 1, 'back to 1.0 once paid, although b > 2T');
  // Flush clears whatever debt is left.
  p.debt = 123;
  p.flush();
  assert.equal(p.debt, 0);
});

test('a Gemini-like source (4x real time) plays at 1.0 for at least 95 % of the time', () => {
  const p = new Player({ inRate: RATE, outRate: RATE }); // Gemini policy (default)
  const tone = sine(440, RATE * 10);
  const chunk = 2400; // 100 ms, one every 25 ms of output: 4x real time
  const block = new Float32Array(128);
  let pushed = 0;
  let t = 0;
  let playing = 0;
  let atOne = 0;
  while (pushed < tone.length || p.state !== 'idle') {
    while (pushed < tone.length && t >= (pushed / chunk) * 600) {
      p.push(tone.subarray(pushed, pushed + chunk));
      pushed += chunk;
    }
    p.render(block);
    if (p.state === 'playing') {
      playing++;
      if (p.rate === 1) atOne++;
    }
    t += 128;
    assert.ok(t < RATE * 30, 'never drained');
  }
  assert.ok(atOne / playing >= 0.95, `played at 1.0 for ${((100 * atOne) / playing).toFixed(1)} %`);
  assert.equal(p.stretchedMs < 500, true, `stretched ${p.stretchedMs} ms`);
});

test('rate changes are slewed at most 0.02 per 20 ms', () => {
  const hop = RATE * 0.01; // one 10 ms WSOLA hop
  assert.ok(Math.abs(slewRate(1, 0.85, hop, RATE) - 0.99) < 1e-9);
  assert.ok(Math.abs(slewRate(0.85, 1.05, hop, RATE) - 0.86) < 1e-9);
  assert.equal(slewRate(0.995, 1, hop, RATE), 1); // reaches the target exactly
  let r = 1;
  for (let i = 0; i < 15; i++) r = slewRate(r, 0.85, hop, RATE);
  assert.ok(Math.abs(r - 0.85) < 1e-9, 'reaches the floor in 150 ms');
});

test('provider playback policy: Gemini 150/500 ms, GPT-Live 400/2000 ms', () => {
  assert.deepEqual({ ...PLAYBACK_POLICY.gemini }, { startMs: 150, capMs: 500 });
  assert.deepEqual({ ...PLAYBACK_POLICY.openai }, { startMs: 400, capMs: 2000 });
  const p = new Player({ inRate: RATE, outRate: 48000 });
  assert.equal(p.targetMs, 150);
  p.setPolicy(PLAYBACK_POLICY.openai);
  assert.equal(p.targetMs, 400);
  assert.equal(p.maxTarget, RATE * 2);
});

test('the Player slows down (pitch kept) when its buffer falls below target, and counts the stretched time', () => {
  // 3 s of tone arriving at 0.75x real time behind a 1200 ms buffer (GPT-Live
  // before its input was paced).
  const p = new Player({ inRate: RATE, outRate: RATE });
  p.setPolicy({ startMs: 1200, capMs: 2000 });
  const chunk = 2400; // 100 ms
  const tone = sine(440, RATE * 3);
  const block = new Float32Array(128);
  const out = [];
  let pushed = 0;
  let t = 0;
  while (pushed < tone.length || p.state !== 'idle') {
    // One chunk every 133 ms of output: 0.75x real time.
    while (pushed < tone.length && t >= (pushed / chunk) * 3200) {
      p.push(tone.subarray(pushed, pushed + chunk));
      pushed += chunk;
    }
    p.render(block);
    if (p.state === 'playing' || out.length) out.push(...block);
    t += 128;
    if (t > RATE * 20) throw new Error('never drained');
  }
  assert.equal(p.stats.underruns, 0, `underruns ${p.stats.underruns}`);
  assert.ok(p.stretchedMs > 200, `stretched ${p.stretchedMs} ms`);
  const played = Float32Array.from(out).subarray(0, p.stats.played);
  assert.ok(Math.abs(zeroCrossingHz(played) - 440) <= 2, `pitch ${zeroCrossingHz(played)}`);
});

// 2026-09-24 18:21:06–18:21:44, the "player froze with 3.1 s in hand"
// report: the page had played a GPT-Live session, the provider switched to
// Gemini (the page applies the new policy on the next status), "On it."
// (760 ms at 10.7x real time) then the 7.9 s answer in 26 chunks over
// 998 ms (7.9x), then nothing. The Player must play all of it out and go
// idle; it does — the "frozen" buffer_ms 3155 in the log was one 15 s-old
// ping sample repeated on three web.client.stats lines (the page now pings
// every 5 s and the daemon logs playback_age_ms). The policy is also
// switched mid-reply, and a flush lands mid-reply, to cover a start
// threshold or stretcher state left over from the old policy.
test('a GPT-Live → Gemini policy switch, then a 7.9x burst and end of stream, drains to 0 without stalling', () => {
  const OUT = 48000;
  const block = new Float32Array(128);
  const blocksFor = (sec) => Math.round((sec * OUT) / 128);
  const drain = (p, maxSec) => {
    let n = 0;
    let stuck = 0;
    let last = p.buffered;
    while (p.buffered > 0 || p.state !== 'idle') {
      p.render(block);
      n++;
      assert.ok(n < blocksFor(maxSec), `not drained after ${maxSec} s: state=${p.state} buffered=${p.buffered}`);
      if (p.buffered === last && p.state !== 'buffering') stuck++;
      else stuck = 0;
      last = p.buffered;
      assert.ok(stuck < 4, `playing but not consuming: state=${p.state} buffered=${p.buffered}`);
    }
    return (n * 128) / OUT;
  };
  const burst = (p, sec, chunks, wallSec, midway) => {
    const total = Math.round(RATE * sec);
    const per = Math.ceil(total / chunks);
    const tone = sine(220, total, 0.3);
    for (let c = 0, at = 0; c < chunks; c++, at += per) {
      p.push(tone.subarray(at, Math.min(total, at + per)));
      if (c === Math.floor(chunks / 2) && midway) midway(p);
      for (let i = 0; i < blocksFor(wallSec / chunks); i++) p.render(block);
    }
  };
  const cases = {
    'switch between replies': null,
    'switch mid-reply back to GPT-Live': (p) => p.setPolicy(PLAYBACK_POLICY.openai),
    'switch mid-reply to Gemini': (p) => p.setPolicy(PLAYBACK_POLICY.gemini),
    'flush mid-reply': (p) => p.flush(),
  };
  for (const [name, midway] of Object.entries(cases)) {
    const p = new Player({ inRate: RATE, outRate: OUT });
    p.setPolicy(PLAYBACK_POLICY.openai);
    burst(p, 3, 30, 3.8, null); // a GPT-Live reply at 0.79x real time
    drain(p, 10);
    p.setPolicy(PLAYBACK_POLICY.gemini); // voice.provider_changed → status.provider
    burst(p, 0.76, 6, 0.071, null); // "On it."
    drain(p, 3);
    for (let i = 0; i < blocksFor(1.5); i++) p.render(block); // the orchestrator thinks (18:21:16.6 → 18:21:18.6)
    const drainsBefore = p.stats.drains;
    const underrunsBefore = p.stats.underruns;
    burst(p, 7.92, 26, 0.998, midway); // the answer
    const sec = drain(p, 12);
    assert.equal(p.buffered, 0, name);
    assert.equal(p.state, 'idle', name);
    assert.equal(p.stats.underruns, underrunsBefore, `${name}: an underrun in a 7.9x burst`);
    assert.ok(p.stats.drains > drainsBefore, `${name}: the answer's end counts as a drain`);
    // Gemini delivers far ahead of real time: what is left plays at 1.0
    // apart from the slow-down at the very end of the reply.
    if (!midway) assert.ok(sec < 7.92 - 0.998 + 0.5, `${name}: ${sec.toFixed(2)} s to drain the rest`);
  }
});
