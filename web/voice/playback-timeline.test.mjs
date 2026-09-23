// Replays real GPT-Live /ws audio arrival timelines (recorded with
// `sheepdog audio-e2e --audio-timeline` against a live scratch daemon,
// docs/audio-e2e-results.md) through the actual Player state machine to
// measure the adaptive jitter buffer against a naive fixed buffer on real
// bursty data, not a synthetic approximation of it.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { Player } from './audio-util.js';

const here = dirname(fileURLToPath(import.meta.url));

function loadFixture(name) {
  return JSON.parse(readFileSync(join(here, 'testdata', name), 'utf8'));
}

const RATE = 24000; // fixtures are 24 kHz PCM16 (docs/ws-contract.md)
const BLOCK = 128;
const BLOCK_MS = (BLOCK / RATE) * 1000;

/**
 * Replays a chunk timeline ({at_ms, bytes}[]) through a Player, pushing
 * each chunk when the simulated clock reaches its recorded arrival
 * offset and rendering at a fixed block cadence throughout, exactly the
 * pull/push shape audio-playback.worklet.js drives in the browser.
 */
function replay(chunks, playerOpts) {
  const p = new Player({ inRate: RATE, outRate: RATE, ...playerOpts });
  let idx = 0;
  let simMs = 0;
  const endMs = chunks[chunks.length - 1].at_ms + 2000; // run past the last chunk to let it drain
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

  const baseline = replay(chunks, { prebuffer: Math.round(RATE * 0.15), maxTarget: Math.round(RATE * 0.15), adaptStep: 0, decayStep: 0 });
  const adaptive = replay(chunks, {}); // defaults: 150 ms start, adapts up to 500 ms

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
  const p = replay(chunks, {});
  assert.ok(p.targetMs > 150, `target should have adapted upward, got ${p.targetMs}ms`);
});
