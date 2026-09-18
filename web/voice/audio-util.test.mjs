// Unit tests for the pure audio helpers: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  CAPTURE_RATE, PLAYBACK_RATE, FRAME_SAMPLES,
  lowpassCoefficients, FIRFilter, Resampler, floatToInt16, int16ToFloat,
  levelOf, FrameChunker, RingBuffer, Player, CapturePipeline, SilenceGate, peakInt16,
} from './audio-util.js';

function sine(freq, rate, n, amp = 0.5) {
  const out = new Float32Array(n);
  for (let i = 0; i < n; i++) out[i] = amp * Math.sin((2 * Math.PI * freq * i) / rate);
  return out;
}

function rms(a, from = 0) {
  let s = 0;
  for (let i = from; i < a.length; i++) s += a[i] * a[i];
  return Math.sqrt(s / (a.length - from));
}

// Runs `fn` over `input` in blocks of `size` and concatenates the outputs.
function inBlocks(input, size, fn) {
  const parts = [];
  for (let i = 0; i < input.length; i += size) parts.push(fn(input.subarray(i, Math.min(i + size, input.length))));
  const total = parts.reduce((n, p) => n + p.length, 0);
  const out = new Float32Array(total);
  let off = 0;
  for (const p of parts) {
    out.set(p, off);
    off += p.length;
  }
  return out;
}

test('lowpass passes 1 kHz and attenuates 12 kHz at 48 kHz', () => {
  const h = lowpassCoefficients(31, 7200, 48000);
  assert.equal(h.length, 31);
  const pass = new FIRFilter(h).process(sine(1000, 48000, 4800));
  const stop = new FIRFilter(h).process(sine(12000, 48000, 4800));
  const gainPass = rms(pass, 100) / rms(sine(1000, 48000, 4800), 100);
  const gainStop = rms(stop, 100) / rms(sine(12000, 48000, 4800), 100);
  assert.ok(gainPass > 0.9 && gainPass < 1.1, `pass-band gain ${gainPass}`);
  assert.ok(gainStop < 0.05, `stop-band gain ${gainStop}`); // < -26 dB
});

test('FIRFilter is block-size independent', () => {
  const h = lowpassCoefficients(31, 7200, 48000);
  const x = sine(3000, 48000, 2000);
  const whole = new FIRFilter(h).process(x);
  const f = new FIRFilter(h);
  const chopped = inBlocks(x, 7, (b) => f.process(b)); // 7 < taps-1 exercises the short-block history path
  assert.equal(chopped.length, whole.length);
  for (let i = 0; i < whole.length; i++) assert.ok(Math.abs(chopped[i] - whole[i]) < 1e-6, `sample ${i}`);
});

test('Resampler 48k→16k keeps phase across 128-sample blocks', () => {
  const x = sine(440, 48000, 48000);
  const whole = new Resampler(48000, 16000).process(x);
  const r = new Resampler(48000, 16000);
  const chopped = inBlocks(x, 128, (b) => r.process(b));
  assert.equal(whole.length, chopped.length);
  assert.ok(Math.abs(whole.length - 16000) <= 1, `length ${whole.length}`);
  for (let i = 0; i < whole.length; i++) assert.ok(Math.abs(chopped[i] - whole[i]) < 1e-6, `sample ${i}`);
  // The output is a 440 Hz sine at 16 kHz: compare against the ideal.
  const ideal = sine(440, 16000, whole.length);
  for (let i = 0; i < whole.length; i++) assert.ok(Math.abs(whole[i] - ideal[i]) < 0.01, `sample ${i}: ${whole[i]} vs ${ideal[i]}`);
});

test('Resampler handles non-integer ratios (44.1k→16k) and up-sampling (24k→48k)', () => {
  const down = new Resampler(44100, 16000);
  const y = inBlocks(sine(1000, 44100, 44100), 128, (b) => down.process(b));
  assert.ok(Math.abs(y.length - 16000) <= 2, `down length ${y.length}`);
  const up = new Resampler(24000, 48000);
  const z = inBlocks(sine(1000, 24000, 2400), 100, (b) => up.process(b));
  assert.ok(Math.abs(z.length - 4800) <= 2, `up length ${z.length}`);
  const ideal = sine(1000, 48000, z.length);
  for (let i = 0; i < z.length; i++) assert.ok(Math.abs(z[i] - ideal[i]) < 0.02, `sample ${i}`);
});

test('Int16 encode/decode round trip and clamping', () => {
  const f = new Float32Array([0, 0.5, -0.5, 1, -1, 1.5, -1.5]);
  const i = floatToInt16(f);
  assert.deepEqual(Array.from(i), [0, 16383, -16384, 32767, -32768, 32767, -32768]);
  const back = int16ToFloat(i);
  assert.ok(Math.abs(back[1] - 0.5) < 1e-3 && Math.abs(back[2] + 0.5) < 1e-3);
  assert.equal(back[4], -1);
});

test('levelOf reports peak and rms', () => {
  const l = levelOf(sine(125, 1000, 1000, 0.8)); // 8 samples per period: sample 2 is the exact peak
  assert.ok(Math.abs(l.peak - 0.8) < 1e-3);
  assert.ok(Math.abs(l.rms - 0.8 / Math.SQRT2) < 1e-2);
  assert.deepEqual(levelOf(new Float32Array(0)), { peak: 0, rms: 0 });
});

test('FrameChunker emits fixed 320-sample frames', () => {
  const c = new FrameChunker(FRAME_SAMPLES);
  const frames = [];
  let seq = 0;
  for (const len of [100, 300, 320, 5, 1000]) {
    const s = new Int16Array(len);
    for (let i = 0; i < len; i++) s[i] = seq++;
    frames.push(...c.push(s));
  }
  assert.equal(frames.length, 5); // 1725 samples → 5 frames + 125 left
  assert.equal(c.buffered, 125);
  let n = 0;
  for (const f of frames) {
    assert.equal(f.length, FRAME_SAMPLES);
    assert.equal(f.byteLength, 640);
    for (const v of f) assert.equal(v, n++);
  }
});

test('RingBuffer wraps, overwrites oldest when full, and clears', () => {
  const r = new RingBuffer(8);
  assert.equal(r.write(new Float32Array([1, 2, 3, 4, 5, 6])), 0);
  const out = new Float32Array(4);
  assert.equal(r.read(out), 4);
  assert.deepEqual(Array.from(out), [1, 2, 3, 4]);
  assert.equal(r.write(new Float32Array([7, 8, 9, 10, 11, 12, 13])), 1); // 2 left + 7 = 9 > 8: drop one
  assert.equal(r.available, 8);
  assert.equal(r.readOne(), 6);
  const rest = new Float32Array(10);
  assert.equal(r.read(rest), 7);
  assert.deepEqual(Array.from(rest.subarray(0, 7)), [7, 8, 9, 10, 11, 12, 13]);
  assert.equal(r.write(new Float32Array(20).fill(1)), 12); // larger than capacity
  assert.equal(r.available, 8);
  r.clear();
  assert.equal(r.available, 0);
});

test('Player waits for the jitter buffer, then plays gaplessly at the context rate', () => {
  const p = new Player({ inRate: PLAYBACK_RATE, outRate: 48000, prebuffer: 2400 });
  const block = new Float32Array(128);
  assert.equal(p.render(block), false);
  p.push(sine(600, PLAYBACK_RATE, 1200)); // 50 ms: below the prebuffer
  assert.equal(p.render(block), false);
  assert.equal(p.state, 'buffering');
  p.push(sine(600, PLAYBACK_RATE, 1200, 0.5).map((v) => v)); // another 50 ms → 100 ms
  p.push(sine(600, PLAYBACK_RATE, 24000)); // plus 1 s
  const out = [];
  let blocks = 0;
  while (p.render(block)) {
    out.push(...block);
    blocks++;
    if (blocks > 100000) throw new Error('never drains');
  }
  assert.ok(p.speaking === false && p.state === 'idle');
  // (2400 + 24000) input samples at 24 kHz → ~52800 output samples at 48 kHz.
  const played = p.stats.played;
  assert.ok(Math.abs(played - 52800) < 256, `played ${played}`);
  assert.equal(p.stats.drains, 1);
  assert.equal(p.stats.underruns, 0);
  // No gaps: the rendered 600 Hz tone has a steady RMS over the middle.
  const middle = new Float32Array(out.slice(4800, 48000));
  assert.ok(Math.abs(rms(middle) - 0.5 / Math.SQRT2) < 0.02, `rms ${rms(middle)}`);
});

test('Player starts a short utterance after maxWaitBlocks and counts underruns', () => {
  const p = new Player({ inRate: PLAYBACK_RATE, outRate: 48000, prebuffer: 2400, maxWaitBlocks: 3 });
  const block = new Float32Array(128);
  p.push(new Float32Array(240).fill(0.25)); // 10 ms only
  assert.equal(p.render(block), false);
  assert.equal(p.render(block), false);
  assert.equal(p.render(block), true); // waited 3 blocks → plays anyway
  while (p.render(block)) { /* drain */ }
  assert.equal(p.stats.drains, 1);
  p.push(new Float32Array(240).fill(0.25)); // right after a drain → underrun
  assert.equal(p.stats.underruns, 1);
  for (let i = 0; i < 3; i++) p.render(block);
  while (p.render(block)) { /* drain */ }
  for (let i = 0; i < 200; i++) p.render(block); // > 300 ms of silence
  p.push(new Float32Array(240).fill(0.25)); // a new utterance, not an underrun
  assert.equal(p.stats.underruns, 1);
});

test('Player.flush empties the ring immediately and fades the tail', () => {
  const p = new Player({ inRate: PLAYBACK_RATE, outRate: 48000, prebuffer: 240 });
  const block = new Float32Array(128);
  p.push(new Float32Array(24000).fill(0.5));
  assert.equal(p.render(block), true);
  assert.ok(p.speaking);
  p.flush();
  assert.equal(p.buffered, 0);
  assert.equal(p.speaking, false);
  assert.equal(p.render(block), false);
  assert.ok(block[0] > 0 && block[0] <= 0.5, 'fade starts from the last sample');
  assert.equal(block[127], 0, 'fade completes within one block');
  p.render(block);
  assert.ok(block.every((v) => v === 0));
  // Audio after a flush is a new utterance, not an underrun.
  p.push(new Float32Array(2400).fill(0.5));
  assert.equal(p.stats.underruns, 0);
  assert.equal(p.stats.flushes, 1);
});

test('CapturePipeline turns 48 kHz float blocks into 20 ms Int16 frames', () => {
  const c = new CapturePipeline(48000, CAPTURE_RATE, FRAME_SAMPLES);
  const x = sine(1000, 48000, 48000); // one second
  const frames = [];
  for (let i = 0; i < x.length; i += 128) frames.push(...c.process(x.subarray(i, i + 128)));
  assert.ok(frames.length === 49 || frames.length === 50, `frames ${frames.length}`); // 16000 / 320 (minus the FIR latency)
  for (const f of frames) assert.equal(f.byteLength, 640);
  // The tone survives: compare frame 10 against an ideal 1 kHz tone at 16 kHz.
  const got = int16ToFloat(frames[10]);
  assert.ok(Math.abs(rms(got) - 0.5 / Math.SQRT2) < 0.02, `rms ${rms(got)}`);
  const same = new CapturePipeline(CAPTURE_RATE); // no filter, no resampler
  assert.equal(same.filter, null);
  assert.equal(same.resampler, null);
  assert.equal(same.process(new Float32Array(320)).length, 1);
});

// ---------------------------------------------------------------- silence gate

function int16Frame(peak, n = FRAME_SAMPLES) {
  const f = new Int16Array(n);
  if (peak) f[n >> 1] = peak;
  return f;
}

test('peakInt16 reports the largest absolute sample', () => {
  const f = new Int16Array([3, -20, 7]);
  assert.equal(peakInt16(f), 20);
  assert.equal(peakInt16(new Int16Array(4)), 0);
});

test('SilenceGate closes after holdMs of near-zero frames and counts them', () => {
  const g = new SilenceGate({ floor: 8, holdMs: 300, prerollMs: 100, frameMs: 20 });
  // 14 quiet frames (280 ms) still pass through.
  for (let i = 0; i < 14; i++) {
    const r = g.push(int16Frame(3));
    assert.equal(r.send.length, 1, `frame ${i} sent`);
    assert.equal(r.transition, null);
  }
  // The 15th (300 ms) closes the gate and is withheld.
  let r = g.push(int16Frame(0));
  assert.equal(r.transition, 'on');
  assert.equal(r.send.length, 0);
  assert.equal(g.gated, true);
  for (let i = 0; i < 50; i++) {
    r = g.push(int16Frame(7));
    assert.equal(r.send.length, 0);
    assert.equal(r.transition, null);
  }
  assert.equal(g.gatedFrames, 51);
});

test('SilenceGate reopens on the first loud frame with the pre-roll in front', () => {
  const g = new SilenceGate({ floor: 20, holdMs: 300, prerollMs: 100, frameMs: 20 });
  for (let i = 0; i < 40; i++) g.push(int16Frame(0));
  assert.equal(g.gated, true);
  // Tag the withheld frames (all below the floor) so the pre-roll order can be checked.
  for (let i = 1; i <= 9; i++) g.push(int16Frame(i));
  const loud = int16Frame(500);
  const r = g.push(loud);
  assert.equal(r.transition, 'off');
  assert.equal(g.gated, false);
  // 100 ms / 20 ms = 5 pre-roll frames, the most recent five, then the loud one.
  assert.equal(r.send.length, 6);
  assert.deepEqual(r.send.slice(0, 5).map(peakInt16), [5, 6, 7, 8, 9]);
  assert.equal(r.send[5], loud);
  // Loud audio keeps flowing one frame at a time and resets the quiet timer.
  assert.deepEqual(g.push(int16Frame(300)).send.length, 1);
  assert.equal(g.quietMs, 0);
});

test('SilenceGate: a short pause below holdMs never gates', () => {
  const g = new SilenceGate({ floor: 8, holdMs: 300, frameMs: 20 });
  let transitions = 0;
  for (let round = 0; round < 5; round++) {
    for (let i = 0; i < 10; i++) if (g.push(int16Frame(0)).transition) transitions++; // 200 ms quiet
    if (g.push(int16Frame(1000)).transition) transitions++;
  }
  assert.equal(transitions, 0);
  assert.equal(g.gatedFrames, 0);
});

test('SilenceGate: floor is inclusive-exclusive at the configured LSB and prerollMs=0 sends nothing old', () => {
  const g = new SilenceGate({ floor: 8, holdMs: 40, prerollMs: 0, frameMs: 20 });
  assert.equal(g.push(int16Frame(8)).send.length, 1); // at the floor counts as sound
  g.push(int16Frame(7));
  assert.equal(g.push(int16Frame(7)).transition, 'on');
  const r = g.push(int16Frame(-9)); // negative peaks count too
  assert.equal(r.transition, 'off');
  assert.equal(r.send.length, 1);
});
