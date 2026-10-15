// Pure audio helpers shared by the /voice page's worklets and unit-tested
// with `make test-web` (node --test, no build step, decision D11). Nothing in
// here touches the DOM or the Web Audio API, so the same code runs in an
// AudioWorkletGlobalScope, the page and node.

/** Sample rate of client→server PCM16 (docs/dev/ws-contract.md). */
export const CAPTURE_RATE = 16000;
/** Sample rate of server→client PCM16. */
export const PLAYBACK_RATE = 24000;
/** One capture frame: 20 ms at 16 kHz = 320 samples = 640 bytes. */
export const FRAME_SAMPLES = 320;

/**
 * Windowed-sinc low-pass FIR coefficients (Hamming window), normalised
 * to unity DC gain. Used before down-sampling so content above the target
 * Nyquist does not alias into the speech band.
 * @param {number} taps odd number of taps
 * @param {number} cutoffHz
 * @param {number} sampleRate
 * @returns {Float32Array}
 */
export function lowpassCoefficients(taps, cutoffHz, sampleRate) {
  if (taps % 2 === 0) taps += 1;
  const fc = cutoffHz / sampleRate; // cycles per sample
  const mid = (taps - 1) / 2;
  const h = new Float32Array(taps);
  let sum = 0;
  for (let i = 0; i < taps; i++) {
    const n = i - mid;
    const sinc = n === 0 ? 2 * fc : Math.sin(2 * Math.PI * fc * n) / (Math.PI * n);
    const w = 0.54 - 0.46 * Math.cos((2 * Math.PI * i) / (taps - 1));
    h[i] = sinc * w;
    sum += h[i];
  }
  for (let i = 0; i < taps; i++) h[i] /= sum;
  return h;
}

/** Streaming FIR filter: keeps the last taps-1 input samples between calls. */
export class FIRFilter {
  /** @param {Float32Array} coeffs */
  constructor(coeffs) {
    this.h = coeffs;
    this.hist = new Float32Array(coeffs.length - 1); // previous inputs, oldest first
  }

  /**
   * Filters one block. Output has the same length as the input; the
   * filter delay is (taps-1)/2 samples, irrelevant for speech.
   * @param {Float32Array} input
   * @returns {Float32Array}
   */
  process(input) {
    const h = this.h;
    const taps = h.length;
    const nh = taps - 1;
    const n = input.length;
    const out = new Float32Array(n);
    // Work on hist ++ input so the window can reach back across calls.
    const buf = new Float32Array(nh + n);
    buf.set(this.hist, 0);
    buf.set(input, nh);
    for (let i = 0; i < n; i++) {
      let acc = 0;
      // y[i] = sum_k h[k] * x[i - k]; x index i-k maps to buf[nh + i - k].
      const base = nh + i;
      for (let k = 0; k < taps; k++) acc += h[k] * buf[base - k];
      out[i] = acc;
    }
    if (n >= nh) {
      this.hist.set(input.subarray(n - nh));
    } else {
      this.hist.set(buf.subarray(n, n + nh));
    }
    return out;
  }
}

/**
 * Linear-interpolation resampler that keeps its phase across calls, so a
 * stream chopped into arbitrary blocks (128-frame worklet quanta) produces
 * exactly the same samples as one big call. Pair with a FIRFilter when
 * down-sampling; on its own it is fine for up-sampling.
 */
export class Resampler {
  constructor(inRate, outRate) {
    this.step = inRate / outRate; // input samples per output sample
    this.pos = 0; // fractional read position relative to `prev`
    this.prev = 0; // last input sample of the previous block
    this.primed = false; // false until the first input sample is seen
  }

  /**
   * @param {Float32Array} input
   * @returns {Float32Array} resampled samples (length varies by ±1 per call)
   */
  process(input) {
    const n = input.length;
    if (n === 0) return new Float32Array(0);
    if (!this.primed) {
      // Start on the first real sample instead of interpolating from 0.
      this.prev = input[0];
      this.primed = true;
      this.pos = 1; // position 1 == input[0] in the "prev ++ input" frame
    }
    // Virtual buffer: index 0 = prev, index k+1 = input[k]. Output while
    // the interpolation window [i, i+1] stays inside it.
    const out = new Float32Array(Math.ceil(n / this.step) + 1);
    let count = 0;
    let pos = this.pos;
    while (pos < n) {
      const i = Math.floor(pos);
      const frac = pos - i;
      const a = i === 0 ? this.prev : input[i - 1];
      const b = input[i];
      out[count++] = a + (b - a) * frac;
      pos += this.step;
    }
    this.pos = pos - n; // carry the phase into the next block
    this.prev = input[n - 1];
    return out.subarray(0, count);
  }
}

/** Float32 [-1,1] → Int16 little-endian (the platform is LE everywhere we run). */
export function floatToInt16(input) {
  const out = new Int16Array(input.length);
  for (let i = 0; i < input.length; i++) {
    let s = input[i];
    if (s > 1) s = 1;
    else if (s < -1) s = -1;
    out[i] = s < 0 ? s * 0x8000 : s * 0x7fff;
  }
  return out;
}

/** Int16 → Float32 [-1,1). */
export function int16ToFloat(input) {
  const out = new Float32Array(input.length);
  for (let i = 0; i < input.length; i++) out[i] = input[i] / 0x8000;
  return out;
}

/** Peak and RMS of a block, for the level meter. */
export function levelOf(input) {
  let peak = 0;
  let sq = 0;
  for (let i = 0; i < input.length; i++) {
    const a = Math.abs(input[i]);
    if (a > peak) peak = a;
    sq += input[i] * input[i];
  }
  return { peak, rms: input.length ? Math.sqrt(sq / input.length) : 0 };
}

/** Re-chunks a stream of samples into fixed-size frames. */
export class FrameChunker {
  constructor(frameSamples = FRAME_SAMPLES) {
    this.size = frameSamples;
    this.pending = new Int16Array(frameSamples);
    this.fill = 0;
  }

  /**
   * @param {Int16Array} samples
   * @returns {Int16Array[]} every complete frame; the remainder is kept
   */
  push(samples) {
    const frames = [];
    let off = 0;
    while (off < samples.length) {
      const take = Math.min(this.size - this.fill, samples.length - off);
      this.pending.set(samples.subarray(off, off + take), this.fill);
      this.fill += take;
      off += take;
      if (this.fill === this.size) {
        frames.push(this.pending);
        this.pending = new Int16Array(this.size);
        this.fill = 0;
      }
    }
    return frames;
  }

  /** Samples waiting for the next frame. */
  get buffered() {
    return this.fill;
  }
}

/**
 * Single-producer single-consumer ring buffer of Float32 samples. Both
 * sides live on the audio thread here (messages arrive on the worklet's
 * port, which runs on the rendering thread), so no atomics are needed.
 */
export class RingBuffer {
  constructor(capacity) {
    this.buf = new Float32Array(capacity);
    this.cap = capacity;
    this.r = 0;
    this.w = 0;
    this.len = 0;
  }

  get available() {
    return this.len;
  }

  get free() {
    return this.cap - this.len;
  }

  /**
   * Appends samples; when full the oldest are overwritten (we would
   * rather lose the start of a very long utterance than stall).
   * @param {Float32Array} samples
   * @returns {number} samples overwritten
   */
  write(samples) {
    let src = samples;
    let dropped = 0;
    if (src.length > this.cap) {
      dropped += src.length - this.cap;
      src = src.subarray(src.length - this.cap);
    }
    const overflow = src.length - this.free;
    if (overflow > 0) {
      // Discard the oldest `overflow` samples to make room.
      this.r = (this.r + overflow) % this.cap;
      this.len -= overflow;
      dropped += overflow;
    }
    const first = Math.min(src.length, this.cap - this.w);
    this.buf.set(src.subarray(0, first), this.w);
    if (first < src.length) this.buf.set(src.subarray(first), 0);
    this.w = (this.w + src.length) % this.cap;
    this.len += src.length;
    return dropped;
  }

  /** Reads one sample; caller checks `available` first. */
  readOne() {
    const v = this.buf[this.r];
    this.r = (this.r + 1) % this.cap;
    this.len--;
    return v;
  }

  /**
   * Reads up to out.length samples into out.
   * @param {Float32Array} out
   * @returns {number} samples read
   */
  read(out) {
    const n = Math.min(out.length, this.len);
    const first = Math.min(n, this.cap - this.r);
    out.set(this.buf.subarray(this.r, this.r + first), 0);
    if (first < n) out.set(this.buf.subarray(0, n - first), first);
    this.r = (this.r + n) % this.cap;
    this.len -= n;
    return n;
  }

  clear() {
    this.r = 0;
    this.w = 0;
    this.len = 0;
  }
}

/**
 * Playback policy per voice provider (docs/dev/ws-contract.md "Playback jitter
 * buffer"; the iOS app uses the same numbers). `startMs` is the jitter
 * buffer target a session starts with (and its floor), `capMs` the most it
 * adapts up to on underruns. GPT-Live delivered audio at 0.72–0.86× real
 * time until the daemon paced its input at real time (2026-09-25); it now
 * runs at ≈1.0× and starts at 400 ms, keeping the 2000 ms cap and the
 * time-stretch for the bursts that remain.
 */
export const PLAYBACK_POLICY = Object.freeze({
  gemini: Object.freeze({ startMs: 150, capMs: 500 }),
  openai: Object.freeze({ startMs: 400, capMs: 2000 }),
});

/** Time-stretch rate policy constants (shared with iOS). */
export const STRETCH = Object.freeze({
  minRate: 0.85, // floor, at b <= T/4
  midRate: 0.90, // at b = T/2
  catchUpRate: 1.05, // when b > 2T and slowing down left a lag debt
  slewPer20ms: 0.02, // max rate change per 20 ms of output
  frameMs: 20, // WSOLA frame
  overlapMs: 10, // WSOLA overlap (= hop: 50 %)
  searchMs: 8, // WSOLA similarity search, ± around the nominal position
});

/**
 * Target playback rate for `b` buffered samples against a jitter buffer
 * target `T` (same unit): 1.0 at b >= T; below T it falls linearly to 0.90
 * at b = T/2 and to 0.85 at b = T/4, floored there. Above 2T it is 1.05
 * only while `debt` > 0 — the lag that slowing down added this reply
 * (Player accumulates (1 − r)·dt while r < 1 and pays back (r − 1)·dt) —
 * so catch-up only wins back delay the stretch caused; a source that
 * arrives far ahead of real time (Gemini) plays at 1.0. Rates below 1
 * play slower than real time, pitch preserved by the WSOLA stretcher.
 * @param {number} b
 * @param {number} T
 * @param {number} [debt] lag debt (any unit; only its sign matters)
 * @returns {number}
 */
export function stretchRate(b, T, debt = 0) {
  if (!(T > 0)) return 1;
  if (b > 2 * T) return debt > 0 ? STRETCH.catchUpRate : 1;
  if (b >= T) return 1;
  if (b <= T / 4) return STRETCH.minRate;
  if (b >= T / 2) return STRETCH.midRate + ((b - T / 2) / (T / 2)) * (1 - STRETCH.midRate);
  return STRETCH.minRate + ((b - T / 4) / (T / 4)) * (STRETCH.midRate - STRETCH.minRate);
}

/**
 * Moves `rate` toward `target` by at most STRETCH.slewPer20ms per 20 ms
 * (`samples` of output at `sampleRate`), so a rate change is inaudible.
 */
export function slewRate(rate, target, samples, sampleRate) {
  const max = STRETCH.slewPer20ms * (samples / (sampleRate * 0.02));
  if (target > rate) return Math.min(target, rate + max);
  return Math.max(target, rate - max);
}

/**
 * WSOLA (waveform-similarity overlap-add) time-stretcher for mono audio:
 * changes duration without changing pitch. Frames of `frame` samples are
 * overlap-added with a periodic Hann window at 50 % overlap (hop H =
 * frame/2, the windows sum to exactly 1). The output advances H per frame;
 * the input advances H·rate on average, and each frame is taken from
 * within ±`search` samples of its nominal position where it best matches
 * the natural continuation of the previous frame (normalised
 * cross-correlation over the overlap), so the seams are phase-aligned and
 * do not click. At rate 1 every frame is that continuation, which
 * reconstructs the input exactly.
 *
 * It pulls from a RingBuffer on demand and needs frame + hop + search
 * samples of lookahead to stretch; with less (the end of an utterance, or a
 * nearly dry buffer) it plays the continuation at rate 1, and with less
 * than a frame it passes the remaining samples through unchanged, so it
 * never holds audio back.
 */
export class Wsola {
  /**
   * @param {object} o
   * @param {number} o.frame frame length in samples (even)
   * @param {number} o.search search radius in samples
   */
  constructor(o) {
    this.N = Math.max(4, o.frame & ~1);
    this.H = this.N >> 1;
    this.S = Math.max(0, Math.round(o.search));
    this.win = new Float32Array(this.N);
    for (let i = 0; i < this.N; i++) this.win[i] = 0.5 - 0.5 * Math.cos((2 * Math.PI * i) / this.N);
    this.cap = 8 * (this.N + this.S) + 64;
    this.buf = new Float32Array(this.cap);
    this.acc = new Float32Array(this.N);
    this.out = new Float32Array(Math.max(this.N, 1024));
    this.reset();
  }

  reset() {
    this.base = 0; // absolute index of buf[0]
    this.len = 0;
    this.head = 0; // absolute index of the first input sample not yet played
    this.started = false;
    this.pos = 0; // absolute start of the last frame
    this.nominal = 0;
    this.acc.fill(0);
    this.outLen = 0;
    this.outPos = 0;
  }

  /** Input samples buffered here and not yet played. */
  get pending() {
    return this.base + this.len - this.head;
  }

  /** Output samples produced and not yet read. */
  get ready() {
    return this.outLen - this.outPos;
  }

  /** Reads one produced sample; caller checks `ready` first. */
  readOne() {
    return this.out[this.outPos++];
  }

  _x(abs) {
    return this.buf[abs - this.base];
  }

  // Pulls from ring until the buffer reaches absolute index `upto` (or the
  // ring is empty), first dropping history no future frame can reach.
  _fill(ring, upto) {
    const keep = Math.max(this.base, Math.min(this.head, Math.floor(this.nominal) - this.S) - 1);
    if (keep > this.base) {
      const drop = keep - this.base;
      this.buf.copyWithin(0, drop, this.len);
      this.len -= drop;
      this.base = keep;
    }
    const want = Math.min(upto - (this.base + this.len), this.cap - this.len, ring.available);
    if (want > 0) this.len += ring.read(this.buf.subarray(this.len, this.len + want));
  }

  // Emits acc[0..H) and shifts the overlap tail down.
  _emitAcc() {
    const H = this.H;
    this.out.set(this.acc.subarray(0, H), 0);
    this.outLen = H;
    this.outPos = 0;
    this.acc.copyWithin(0, H, this.N);
    this.acc.fill(0, this.N - H);
  }

  // Emits the raw input [from, to) and ends the stretch run.
  _passThrough(from, to) {
    const n = Math.max(0, Math.min(to - from, this.out.length));
    for (let i = 0; i < n; i++) this.out[i] = this._x(from + i);
    this.outLen = n;
    this.outPos = 0;
    this.head = from + n;
    this.started = false;
    this.acc.fill(0);
    return n;
  }

  /**
   * Produces the next output samples at `rate` (call when `ready` is 0).
   * @param {RingBuffer} ring
   * @param {number} rate playback rate (<1 slower, >1 faster)
   * @returns {{out: number, consumed: number}} samples produced and input
   *   samples the nominal position advanced (their difference is the time
   *   the stretch added)
   */
  produce(ring, rate) {
    const N = this.N;
    const H = this.H;
    if (!this.started) {
      this._fill(ring, this.head + N);
      const end = this.base + this.len;
      if (end - this.head < N) {
        const n = this._passThrough(this.head, end);
        return { out: n, consumed: n };
      }
      // First frame: its first half is played as is (nothing to overlap),
      // its second half fades out under the next frame.
      this.pos = this.head;
      this.nominal = this.pos;
      for (let i = 0; i < H; i++) this.acc[i] = this._x(this.pos + i);
      for (let i = H; i < N; i++) this.acc[i] = this._x(this.pos + i) * this.win[i];
      this._emitAcc();
      this.head = this.pos + H;
      this.started = true;
      return { out: H, consumed: H };
    }
    const cont = this.pos + H; // the natural continuation
    let next = rate === 1 ? cont : this.nominal + H * rate;
    const c = Math.round(next);
    this._fill(ring, Math.max(c + this.S + N, cont + N));
    const end = this.base + this.len;
    let p;
    let consumed;
    if (rate !== 1 && end >= c + this.S + N && c - this.S >= this.base) {
      p = this._search(c, cont);
      consumed = next - this.nominal;
    } else if (end >= cont + N) {
      p = cont;
      next = cont;
      consumed = H;
    } else {
      // Not a frame left: play the rest as it is. The overlap tail in acc
      // faded that same input out, so the raw samples are its exact sum.
      const n = this._passThrough(cont, end);
      return { out: n, consumed: n };
    }
    for (let i = 0; i < N; i++) this.acc[i] += this._x(p + i) * this.win[i];
    this._emitAcc();
    this.nominal = next;
    this.pos = p;
    this.head = p + H;
    return { out: H, consumed };
  }

  // Best frame start within ±S of c: the normalised cross-correlation of
  // its first H samples with the continuation at `cont`. A coarse pass
  // (every 4th lag, every 2nd sample) then a fine pass around the best.
  _search(c, cont) {
    const H = this.H;
    const S = this.S;
    const score = (p, step) => {
      let xy = 0;
      let yy = 1e-9;
      for (let i = 0; i < H; i += step) {
        const y = this._x(p + i);
        xy += y * this._x(cont + i);
        yy += y * y;
      }
      return xy / Math.sqrt(yy);
    };
    let best = c;
    let bestScore = -Infinity;
    for (let d = -S; d <= S; d += 4) {
      const s = score(c + d, 2);
      if (s > bestScore) {
        bestScore = s;
        best = c + d;
      }
    }
    const lo = Math.max(c - S, best - 3);
    const hi = Math.min(c + S, best + 3);
    bestScore = -Infinity;
    let fine = best;
    for (let p = lo; p <= hi; p++) {
      const s = score(p, 1);
      if (s > bestScore) {
        bestScore = s;
        fine = p;
      }
    }
    return fine;
  }
}

/**
 * Gapless player state machine: pulls 24 kHz samples from a RingBuffer,
 * resamples them to the output rate and fills output blocks. It waits for
 * `target` samples (an adaptive jitter buffer) before starting, or a
 * bounded number of output blocks if the utterance is shorter than that,
 * then plays until the ring runs dry. A dry ring closely followed by more
 * audio counts as an underrun; one followed by nothing is simply the end
 * of the utterance.
 *
 * The jitter buffer target adapts within a session (docs/dev/ws-contract.md,
 * "Playback jitter buffer"): it starts at `prebuffer` (150 ms), rises by
 * `adaptStep` (100 ms) on every mid-utterance underrun up to `maxTarget`
 * (500 ms), and decays by `decayStep` (20 ms) after every utterance that
 * played all the way through without one, down to `prebuffer` again. A
 * post-underrun resume waits for the (now higher) target to refill before
 * playing again — it does not resume on the first new chunk — and every
 * `_start()` ramps in over `crossFadeLen` samples so a resumption or a
 * fresh utterance's first samples never click. Those defaults are
 * Gemini's; `setPolicy(PLAYBACK_POLICY.openai)` gives GPT-Live 400 ms /
 * 2000 ms.
 *
 * Adaptive time-stretch (unless `stretch: false`): between the ring and
 * the resampler a WSOLA stretcher plays at rate `stretchRate(buffered,
 * target)`, slewed by `slewRate` — slower than real time (pitch kept) as
 * the buffer falls below the target, 1.05× above twice the target only to
 * pay back the lag debt slowing down added this reply (reset on a drain or
 * flush) — so a source that delivers slower than real time drains the
 * buffer more slowly instead of running dry, and one that delivers faster
 * plays at 1.0.
 */
export class Player {
  /**
   * @param {object} o
   * @param {number} o.inRate ring buffer sample rate (24000)
   * @param {number} o.outRate audio context sample rate
   * @param {number} [o.capacity] ring capacity in samples (default 30 s)
   * @param {number} [o.prebuffer] initial/floor jitter buffer target, samples at inRate (150 ms)
   * @param {number} [o.maxTarget] the jitter buffer target's ceiling, samples at inRate (500 ms)
   * @param {number} [o.adaptStep] target increase per mid-utterance underrun, samples at inRate (100 ms)
   * @param {number} [o.decayStep] target decrease per clean utterance, samples at inRate (20 ms)
   * @param {number} [o.maxWaitBlocks] fixed override for the "start anyway" ceiling (blocks); unset scales with the target
   * @param {number} [o.crossFadeLen] fade-in length on every start/resume, output samples (8 ms)
   * @param {number} [o.underrunWindow] output samples after a drain within which new data means underrun
   * @param {boolean} [o.stretch] adaptive time-stretch (default true); false plays the ring as is
   */
  constructor(o) {
    this.inRate = o.inRate;
    this.outRate = o.outRate;
    this.ring = new RingBuffer(o.capacity || o.inRate * 30);
    // 150 ms floor: the model's chunks arrive in bursts with gaps of up
    // to ~100 ms between them in the common case (observed in WP6 live
    // runs); a session that sees worse bursts adapts upward (below).
    this.basePrebuffer = o.prebuffer == null ? Math.round(o.inRate * 0.15) : o.prebuffer;
    this.maxTarget = o.maxTarget == null ? Math.round(o.inRate * 0.5) : o.maxTarget;
    this.adaptStep = o.adaptStep == null ? Math.round(o.inRate * 0.1) : o.adaptStep;
    this.decayStep = o.decayStep == null ? Math.round(o.inRate * 0.02) : o.decayStep;
    this.target = this.basePrebuffer;
    // A lone short chunk still starts rather than waiting forever: the
    // ceiling scales with the current target (plus headroom) unless a
    // fixed override is given (tests; a caller with its own policy).
    this.fixedMaxWaitBlocks = o.maxWaitBlocks;
    this.waitCeilingPadMs = 250;
    this.underrunWindow = o.underrunWindow == null ? Math.round(o.outRate * 0.3) : o.underrunWindow;
    this.crossFadeLen = o.crossFadeLen == null ? Math.round(o.outRate * 0.008) : o.crossFadeLen;
    this.step = o.inRate / o.outRate;
    this.state = 'idle'; // idle | buffering | playing
    this.waited = 0;
    this.frac = 0;
    this.cur = 0;
    this.next = 0;
    this.last = 0; // last output sample, for the fade after a flush
    this.fade = 0; // samples of fade-out left
    this.fadeLen = Math.round(o.outRate * 0.002);
    this.fadeIn = 0; // samples of fade-in left after a start/resume
    this.outSince = 0; // output samples rendered since the last drain
    // Per-utterance bookkeeping for the adaptive target (see class doc).
    this.playedThisUtterance = false;
    this.utteranceHadUnderrun = false;
    this.flushedThisUtterance = false;
    // Adaptive time-stretch (class doc): a WSOLA stretcher between the
    // ring and the resampler, its rate a function of the buffered level.
    this.wsola = o.stretch === false ? null : new Wsola({
      frame: Math.round((o.inRate * STRETCH.frameMs) / 1000),
      search: Math.round((o.inRate * STRETCH.searchMs) / 1000),
    });
    this.rate = 1;
    // debt: the lag (output samples) slowing down added this reply, which
    // catch-up at 1.05 may win back; reset at a drain or flush.
    this.debt = 0;
    // stretched: output samples played at a rate other than 1.0
    // (wall-clock time under time-stretch, either direction).
    this.stats = { underruns: 0, drains: 0, dropped: 0, played: 0, flushes: 0, stretched: 0 };
    // Playback reports (docs/dev/ws-contract.md "Playback reports"):
    // onPlayback(state, playedMs) is called with 'started' when playing
    // begins after being empty, 'drained' once the ring ran dry and
    // stayed dry for underrunWindow (an underrun that resumes within it
    // continues the same run), and 'flushed' on every flush. playedMs is
    // the source audio (24 kHz stream time, not wall clock under
    // time-stretch) played in the run since the last drained/flushed.
    this.onPlayback = null;
    this.runIn = 0; // source samples received in the current run
    this.drainPending = false; // ran dry; drained not reported yet
    this.continuing = false; // the next start resumes an underrun
  }

  _report(state, samples) {
    if (!this.onPlayback) return;
    this.onPlayback(state, Math.max(0, Math.round((samples * 1000) / this.inRate)));
  }

  _reportDrained() {
    this.drainPending = false;
    this._report('drained', this.runIn);
    this.runIn = 0;
  }

  /**
   * Applies a provider's playback policy (PLAYBACK_POLICY): the jitter
   * buffer target starts (and floors) at startMs and adapts up to capMs.
   * @param {{startMs: number, capMs: number}} policy
   */
  setPolicy(policy) {
    if (!policy) return;
    this.basePrebuffer = Math.round((this.inRate * policy.startMs) / 1000);
    this.maxTarget = Math.round((this.inRate * policy.capMs) / 1000);
    this.target = this.basePrebuffer;
  }

  /** Audio waiting to be played, in input samples: ring plus stretcher. */
  get buffered() {
    if (!this.wsola) return this.ring.available;
    return this.ring.available + this.wsola.pending + this.wsola.ready;
  }

  /** Wall-clock ms played at a rate other than 1.0 so far. */
  get stretchedMs() {
    return Math.round((this.stats.stretched / this.inRate) * 1000);
  }

  // Samples the playback source can still give: the ring, or the ring
  // through the stretcher.
  _srcAvail() {
    return this.buffered;
  }

  // Next source sample at inRate; caller checks _srcAvail() first.
  _srcRead() {
    const w = this.wsola;
    if (!w) return this.ring.readOne();
    if (w.ready === 0) {
      this.rate = slewRate(this.rate, stretchRate(this.buffered, this.target, this.debt), w.H, this.inRate);
      const r = w.produce(this.ring, this.rate);
      if (this.rate !== 1) this.stats.stretched += r.out;
      // Lag debt: (1 − r)·dt accrues while slower, (r − 1)·dt pays it back.
      this.debt = Math.max(0, this.debt + (1 - this.rate) * r.out);
    }
    return w.readOne();
  }

  get speaking() {
    return this.state === 'playing';
  }

  /** Current adaptive jitter buffer target, in ms (for the stats line). */
  get targetMs() {
    return Math.round((this.target / this.inRate) * 1000);
  }

  /** @param {Float32Array} samples at inRate */
  push(samples) {
    if (samples.length === 0) return;
    const dropped = this.ring.write(samples);
    this.stats.dropped += dropped;
    if (this.state === 'idle') {
      const resumingUnderrun = this.stats.drains > 0 && this.outSince < this.underrunWindow;
      // The run's playback report: an underrun resume continues it;
      // otherwise a drain not yet reported ends it now.
      this.continuing = resumingUnderrun && this.drainPending;
      if (this.continuing) this.drainPending = false;
      else if (this.drainPending) this._reportDrained();
      if (resumingUnderrun) {
        this.stats.underruns++;
        this.utteranceHadUnderrun = true;
        this.target = Math.min(this.maxTarget, this.target + this.adaptStep);
      } else {
        // The previous utterance (if any) truly ended, or was flushed:
        // score it for decay, then reset the per-utterance bookkeeping.
        if (this.playedThisUtterance && !this.utteranceHadUnderrun && !this.flushedThisUtterance) {
          this.target = Math.max(this.basePrebuffer, this.target - this.decayStep);
        }
        this.playedThisUtterance = false;
        this.utteranceHadUnderrun = false;
        this.flushedThisUtterance = false;
      }
      this.state = 'buffering';
      this.waited = 0;
    }
    this.runIn += samples.length - dropped;
  }

  /** Drop everything now; the next block fades the last sample to zero. */
  flush() {
    // What of the run was heard: everything received minus what is
    // still buffered (all of it once the ring ran dry).
    const played = this.drainPending ? this.runIn : Math.max(0, this.runIn - this.buffered);
    this.ring.clear();
    if (this.wsola) this.wsola.reset();
    this.rate = 1;
    this.debt = 0;
    if (this.state === 'playing') this.fade = this.fadeLen;
    if (this.state !== 'idle') this.flushedThisUtterance = true;
    this.state = 'idle';
    this.frac = 0;
    this.cur = 0;
    this.next = 0;
    this.waited = 0;
    this.outSince = this.underrunWindow; // audio after a flush is a new utterance, not an underrun
    this.stats.flushes++;
    this.drainPending = false;
    this.continuing = false;
    this._report('flushed', played);
    this.runIn = 0;
  }

  // The "start anyway" ceiling in render() calls (~128-sample blocks),
  // scaling with the current target unless a fixed override was given.
  _maxWaitBlocks() {
    if (this.fixedMaxWaitBlocks != null) return this.fixedMaxWaitBlocks;
    const targetMs = (this.target / this.inRate) * 1000;
    return Math.round(((targetMs + this.waitCeilingPadMs) / 1000 * this.outRate) / 128);
  }

  // Starts playing: seeds the interpolation pair from the ring and arms
  // the fade-in so the seam (a fresh utterance or a post-underrun resume)
  // does not click.
  _start() {
    this.cur = this._srcRead();
    this.next = this._srcAvail() ? this._srcRead() : this.cur;
    this.frac = 0;
    this.state = 'playing';
    this.fadeIn = this.crossFadeLen;
    this.playedThisUtterance = true;
    if (!this.continuing) this._report('started', 0);
    this.continuing = false;
  }

  /**
   * Fills `out` (at outRate). Returns true when audible samples were
   * written.
   * @param {Float32Array} out
   */
  render(out) {
    const n = out.length;
    if (this.state === 'buffering') {
      this.waited++;
      if (this.buffered >= this.target || this.waited >= this._maxWaitBlocks()) {
        if (this._srcAvail() > 0) this._start();
        else this.state = 'idle';
      }
    }
    if (this.state !== 'playing') {
      out.fill(0);
      if (this.fade > 0) this._renderFade(out);
      this.outSince += n;
      if (this.drainPending && this.state === 'idle' && this.outSince >= this.underrunWindow) this._reportDrained();
      return false;
    }
    let i = 0;
    for (; i < n; i++) {
      let s = this.cur + (this.next - this.cur) * this.frac;
      if (this.fadeIn > 0) {
        s *= 1 - this.fadeIn / this.crossFadeLen;
        this.fadeIn--;
      }
      out[i] = s;
      this.frac += this.step;
      while (this.frac >= 1) {
        this.frac -= 1;
        this.cur = this.next;
        if (this._srcAvail() === 0) {
          // Ran dry: finish this sample, then go idle.
          this.next = this.cur;
          this.state = 'idle';
          this.stats.drains++;
          this.debt = 0; // the reply (or this run of it) ended
          this.outSince = 0;
          this.drainPending = true;
          this.last = out[i];
          this.fade = this.fadeLen;
          i++;
          out.fill(0, i);
          this._renderFade(out, i);
          this.stats.played += i;
          this.frac = 0;
          return true;
        }
        this.next = this._srcRead();
      }
    }
    this.last = out[n - 1];
    this.stats.played += n;
    return true;
  }

  // Short linear fade from `last` to 0 so a flush or drain does not click.
  _renderFade(out, from = 0) {
    for (let i = from; i < out.length && this.fade > 0; i++, this.fade--) {
      out[i] = this.last * (this.fade / this.fadeLen);
    }
  }
}

/**
 * Capture pipeline: context-rate float blocks in, 16 kHz Int16 frames out.
 * Anti-alias low-pass (when down-sampling) → linear resample → Int16 →
 * fixed frames. Same code for the microphone and the file test source.
 */
export class CapturePipeline {
  constructor(inRate, outRate = CAPTURE_RATE, frameSamples = FRAME_SAMPLES) {
    this.filter = null;
    if (inRate > outRate) {
      // Cutoff at 90 % of the target Nyquist, 31 taps: ~ -40 dB in the stop
      // band for 44.1/48 kHz inputs, cheap enough for the audio thread.
      this.filter = new FIRFilter(lowpassCoefficients(31, outRate * 0.45, inRate));
    }
    this.resampler = inRate === outRate ? null : new Resampler(inRate, outRate);
    this.chunker = new FrameChunker(frameSamples);
  }

  /**
   * @param {Float32Array} block samples at inRate
   * @returns {Int16Array[]} complete 16 kHz frames
   */
  process(block) {
    let x = block;
    if (this.filter) x = this.filter.process(x);
    if (this.resampler) x = this.resampler.process(x);
    return this.chunker.push(floatToInt16(x));
  }
}

/**
 * Silence gate for capture frames (docs/dev/ws-contract.md, "Silence gate").
 * Near-zero PCM streamed continuously degrades the Gemini session
 * (docs/dev/history/e2e-results.md, "zero-valued audio frames"), so frames whose peak
 * stays below `floor` (Int16 LSB) for `holdMs` are not sent at all. The
 * last `prerollMs` of gated frames are kept in a ring and released with
 * the first loud frame so the first syllable is not clipped.
 *
 * Pure: `push(frame)` returns what to send and whether the gate flipped.
 */
export class SilenceGate {
  /**
   * @param {object} [o]
   * @param {number} [o.floor] peak below this (Int16 LSB) counts as silent (default 8)
   * @param {number} [o.holdMs] silence needed before gating (default 300)
   * @param {number} [o.prerollMs] gated audio replayed when speech resumes (default 100)
   * @param {number} [o.frameMs] duration of one frame (default 20)
   */
  constructor(o = {}) {
    this.floor = o.floor == null ? 8 : o.floor;
    this.holdMs = o.holdMs == null ? 300 : o.holdMs;
    this.frameMs = o.frameMs == null ? 20 : o.frameMs;
    this.prerollFrames = Math.max(0, Math.round((o.prerollMs == null ? 100 : o.prerollMs) / this.frameMs));
    this.gated = false; // true while frames are being withheld
    this.quietMs = 0; // consecutive silent time seen so far
    this.preroll = []; // withheld frames, oldest first, at most prerollFrames
    this.gatedFrames = 0; // frames withheld in total
    this.peak = 0; // peak of the last frame, in LSB
  }

  /**
   * @param {Int16Array} frame
   * @returns {{send: Int16Array[], transition: ('on'|'off'|null)}} frames to
   *   send now (possibly including pre-roll) and whether the gate switched
   *   on (silence started) or off (speech resumed) on this frame.
   */
  push(frame) {
    const peak = peakInt16(frame);
    this.peak = peak;
    if (peak >= this.floor) {
      this.quietMs = 0;
      if (this.gated) {
        this.gated = false;
        const send = this.preroll;
        this.preroll = [];
        send.push(frame);
        return { send, transition: 'off' };
      }
      return { send: [frame], transition: null };
    }
    this.quietMs += this.frameMs;
    if (!this.gated) {
      if (this.quietMs < this.holdMs) return { send: [frame], transition: null };
      this.gated = true;
      this.withhold(frame);
      return { send: [], transition: 'on' };
    }
    this.withhold(frame);
    return { send: [], transition: null };
  }

  withhold(frame) {
    this.gatedFrames++;
    if (this.prerollFrames === 0) return;
    this.preroll.push(frame);
    if (this.preroll.length > this.prerollFrames) this.preroll.shift();
  }
}

/** Peak absolute sample of an Int16 frame, in LSB. */
export function peakInt16(frame) {
  let peak = 0;
  for (let i = 0; i < frame.length; i++) {
    const a = frame[i] < 0 ? -frame[i] : frame[i];
    if (a > peak) peak = a;
  }
  return peak;
}
