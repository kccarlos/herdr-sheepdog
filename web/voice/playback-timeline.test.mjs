// Replays real GPT-Live /ws audio arrival timelines (recorded with
// `sheepdog audio-e2e --audio-timeline` against a live scratch daemon,
// docs/dev/history/audio-e2e-results.md) through the actual Player state machine to
// measure the adaptive jitter buffer against a naive fixed buffer on real
// bursty data, not a synthetic approximation of it.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { Player, PLAYBACK_POLICY } from './audio-util.js';

const here = dirname(fileURLToPath(import.meta.url));

function loadFixture(name) {
  return JSON.parse(readFileSync(join(here, 'testdata', name), 'utf8'));
}

const RATE = 24000; // fixtures are 24 kHz PCM16 (docs/dev/ws-contract.md)
const BLOCK = 128;
const BLOCK_MS = (BLOCK / RATE) * 1000;

/**
 * Replays a chunk timeline ({at_ms, bytes}[]) through a Player, pushing
 * each chunk when the simulated clock reaches its recorded arrival
 * offset and rendering at a fixed block cadence throughout, exactly the
 * pull/push shape audio-playback.worklet.js drives in the browser.
 */
function replay(chunks, playerOpts, policy) {
  const p = new Player({ inRate: RATE, outRate: RATE, ...playerOpts });
  if (policy) p.setPolicy(policy);
  let idx = 0;
  let simMs = 0;
  const endMs = chunks[chunks.length - 1].at_ms + 6000; // run past the last chunk to let it drain (a refill can wait ~2 s)
  const block = new Float32Array(BLOCK);
  while (simMs < endMs) {
    while (idx < chunks.length && chunks[idx].at_ms <= simMs) {
      const n = chunks[idx].bytes / 2;
      p.push(new Float32Array(n).fill(0.3));
      idx++;
    }
    p.render(block);
    simMs += BLOCK_MS;
  }
  return p;
}

// A session strings both recorded utterances together with a silent gap
// between them, like a real conversation: the adaptive target the first
// utterance's underruns raise carries into the second, exactly as it
// would across a page's lifetime (the target is not reset per utterance).
function chainedSession() {
  const a = loadFixture('gptlive-status-question.json').chunks;
  const b = loadFixture('gptlive-instruction-promise.json').chunks;
  const out = [];
  let offset = 0;
  for (const chunks of [a, b]) {
    for (const c of chunks) out.push({ at_ms: c.at_ms + offset, bytes: c.bytes });
    offset = out[out.length - 1].at_ms + 4000; // 4 s between utterances
  }
  return out;
}

test('recorded GPT-Live fixtures load and look like real bursty arrivals', () => {
  for (const name of ['gptlive-status-question.json', 'gptlive-instruction-promise.json']) {
    const f = loadFixture(name);
    assert.ok(f.chunks.length > 10, `${name}: too few chunks`);
    const gaps = f.chunks.slice(1).map((c, i) => c.at_ms - f.chunks[i].at_ms);
    assert.ok(Math.max(...gaps) > 150, `${name}: no gap over 150 ms — not representative of the bursts this fix targets`);
  }
});

test('the adaptive jitter buffer cuts underruns well below the fixed 150 ms baseline on the recorded chained session', () => {
  const chunks = chainedSession();

  const baseline = replay(chunks, { prebuffer: Math.round(RATE * 0.15), maxTarget: Math.round(RATE * 0.15), adaptStep: 0, decayStep: 0, stretch: false });
  const adaptive = replay(chunks, { stretch: false }); // defaults: 150 ms start, adapts up to 500 ms

  assert.ok(baseline.stats.underruns > 0, 'sanity: the recorded timeline does underrun a naive fixed 150 ms buffer');
  assert.ok(
    adaptive.stats.underruns < baseline.stats.underruns,
    `adaptive underruns (${adaptive.stats.underruns}) should be fewer than the fixed-150ms baseline (${baseline.stats.underruns})`,
  );
  // On this recording GPT-Live's own throughput falls behind real time for
  // multi-second replies (realtime_ratio 0.74-0.87 in the same run, daemon
  // voice.audio_timing log): a jitter buffer alone cannot always reach
  // zero underruns no matter how large, since the source cannot sustain
  // real-time delivery. What the adaptive policy guarantees is a large
  // cut versus the old fixed 150 ms buffer, and (audio-util.test.mjs) that
  // every remaining drain/resume seam is cross-faded instead of clicking.
  assert.ok(adaptive.stats.underruns <= baseline.stats.underruns / 2, 'expected at least a 2x reduction');
});

test('the adaptive target ratchets up on the recorded chained session and stays elevated into the second utterance', () => {
  const chunks = chainedSession();
  const p = replay(chunks, { stretch: false });
  assert.ok(p.targetMs > 150, `target should have adapted upward, got ${p.targetMs}ms`);
});

// The GPT-Live policy before the input-pacing fix (2026-09-25): a
// 1200 ms start buffer for a source running at 0.74-0.87x real time.
// The recordings below were made then, so they are replayed with it.
const PREPACING_POLICY = Object.freeze({ startMs: 1200, capMs: 2000 });

// Before (the adaptive jitter buffer alone, Gemini's 150/500 ms policy,
// which is what every provider got until then) versus after (GPT-Live's
// then 1200/2000 ms policy plus the adaptive time-stretch) on the GPT-Live
// timelines recorded before the input-pacing fix. These recordings arrive at 0.78x and 0.69x real
// time; the time-stretch floor is 0.85x, so a long enough reply still
// runs dry eventually — the recorded ones are short enough that it
// (almost) does not.
test('GPT-Live policy plus time-stretch: underruns before vs after on the recorded timelines', (t) => {
  const cases = [
    ['status-question', loadFixture('gptlive-status-question.json').chunks],
    ['instruction-promise', loadFixture('gptlive-instruction-promise.json').chunks],
    ['chained', chainedSession()],
  ];
  const after = {};
  for (const [name, chunks] of cases) {
    const b = replay(chunks, { stretch: false });
    const a = replay(chunks, {}, PREPACING_POLICY);
    after[name] = a.stats.underruns;
    t.diagnostic(`${name}: underruns before ${b.stats.underruns}, after ${a.stats.underruns} (played at r != 1 for ${a.stretchedMs} ms)`);
    assert.ok(a.stats.underruns < b.stats.underruns, `${name}: after ${a.stats.underruns} not below before ${b.stats.underruns}`);
  }
  assert.equal(after['status-question'], 0);
  assert.ok(after['instruction-promise'] === 0 && after.chained === 0, `after = ${JSON.stringify(after)}`);
});

// After the input-pacing fix (gptlive keepAlive pays the silence debt at
// real time, docs/dev/history/audio-e2e-results.md 2026-09-25) GPT-Live delivers at
// ≈1.0x real time: the recorded 25 s reply plays through GPT-Live's
// 400 ms start buffer with no more underruns than the old 1200 ms buffer
// — one, at the same spot under both: a pause the model made mid-count
// (the daemon drops silence past PauseHold, leaving a 869 ms hole in the
// forwarded stream), not a gap inside a word.
test('paced GPT-Live timeline plays through the 400 ms start buffer', (t) => {
  const chunks = loadFixture('gptlive-paced-long-reply.json').chunks;
  const a = replay(chunks, {}, PLAYBACK_POLICY.openai);
  const old = replay(chunks, {}, PREPACING_POLICY);
  t.diagnostic(`paced long reply: underruns ${a.stats.underruns} at 400 ms, ${old.stats.underruns} at 1200 ms`);
  assert.equal(PLAYBACK_POLICY.openai.startMs, 400);
  assert.ok(a.stats.underruns <= 1 && a.stats.underruns <= old.stats.underruns, `underruns ${a.stats.underruns} (1200 ms: ${old.stats.underruns})`);
  // From 6.8 s on the reply is continuous (no hole over ~190 ms): at ≈1.0x
  // the 400 ms buffer alone carries it without an underrun.
  const tail = chunks.slice(54);
  const t0 = tail[0].at_ms;
  const b = replay(tail.map((c) => ({ at_ms: c.at_ms - t0, bytes: c.bytes })), {}, PLAYBACK_POLICY.openai);
  assert.equal(b.stats.underruns, 0, `continuous tail underruns ${b.stats.underruns}`);
});
