// Playback worklet: 24 kHz PCM16 frames from the daemon → Float32 → ring
// buffer → resampled to the context rate, gaplessly, on the audio thread.
//
// Why a ring buffer rather than scheduled AudioBufferSourceNodes: the
// model's frames are 40–730 ms and arrive in bursts; appending them to one
// continuous buffer is sample-accurate (no drift between scheduled
// sources), and `flush` empties it in the next 128-frame block (< 3 ms)
// instead of having to stop every scheduled source. The state machine
// itself (jitter buffer, adaptive time-stretch, underrun accounting,
// fade-out) lives in audio-util.js so it is unit-tested.
import { Player, int16ToFloat, PLAYBACK_RATE, PLAYBACK_POLICY } from './audio-util.js';

const STATS_EVERY_BLOCKS = 32; // ~85 ms at 48 kHz

class PlaybackProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const o = (options && options.processorOptions) || {};
    this.player = new Player({
      inRate: o.inRate || PLAYBACK_RATE,
      outRate: sampleRate,
      prebuffer: o.prebuffer, // samples at inRate; default 100 ms
      capacity: o.capacity,
    });
    this.blocks = 0;
    this.lastSpeaking = false;
    this.port.onmessage = (e) => this.onMessage(e.data);
  }

  onMessage(m) {
    switch (m.type) {
      case 'audio': {
        // PCM16 LE mono; a trailing odd byte would be a contract violation.
        const n = m.data.byteLength >> 1;
        this.player.push(int16ToFloat(new Int16Array(m.data, 0, n)));
        break;
      }
      case 'policy': {
        // The page learned the voice provider (/ws status.provider):
        // GPT-Live's slow delivery gets a deeper jitter buffer.
        const policy = PLAYBACK_POLICY[m.provider] || PLAYBACK_POLICY.gemini;
        this.player.setPolicy(policy);
        this.postStats(true);
        break;
      }
      case 'flush':
        this.player.flush();
        this.port.postMessage({ type: 'flushed', id: m.id, at: currentTime, buffered: this.player.buffered });
        this.postStats(true);
        break;
      default:
        break;
    }
  }

  postStats(force) {
    const p = this.player;
    const speaking = p.speaking;
    if (!force && speaking === this.lastSpeaking && this.blocks % STATS_EVERY_BLOCKS !== 0) return;
    this.lastSpeaking = speaking;
    this.port.postMessage({
      type: 'stats',
      speaking,
      state: p.state,
      bufferedMs: Math.round((p.buffered / p.inRate) * 1000),
      targetMs: p.targetMs,
      underruns: p.stats.underruns,
      drains: p.stats.drains,
      dropped: p.stats.dropped,
      flushes: p.stats.flushes,
      playedMs: Math.round((p.stats.played / p.outRate) * 1000),
      rate: Math.round(p.rate * 1000) / 1000,
      stretchedMs: p.stretchedMs,
      at: currentTime,
    });
  }

  process(_inputs, outputs) {
    const out = outputs[0];
    const ch0 = out[0];
    if (!ch0) return true;
    this.player.render(ch0);
    for (let c = 1; c < out.length; c++) out[c].set(ch0); // mono → every output channel
    this.blocks++;
    this.postStats(false);
    return true;
  }
}

registerProcessor('sheepdog-playback', PlaybackProcessor);
