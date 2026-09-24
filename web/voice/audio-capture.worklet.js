// Capture worklet: microphone (or the file test source) at the context's
// native rate → anti-alias low-pass → 16 kHz → Int16 → 20 ms frames →
// silence gate → posted to the page, which sends each one as a WebSocket
// binary frame.
//
// Runs on the audio rendering thread. Mute is honoured here as well as in
// the page, so a muted client posts nothing at all (contract: muted means
// silence, not zeros). The silence gate withholds near-zero frames after
// a quiet stretch for the same reason (docs/history/e2e-results.md): the page is
// told about each transition so the daemon can log it.
import { CapturePipeline, SilenceGate, levelOf, FRAME_SAMPLES } from './audio-util.js';

const LEVEL_EVERY_BLOCKS = 16; // ~43 ms at 48 kHz

class CaptureProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const o = (options && options.processorOptions) || {};
    this.pipeline = new CapturePipeline(sampleRate, o.outRate || 16000, o.frameSamples || FRAME_SAMPLES);
    this.gate = o.gate === false ? null : new SilenceGate(o.gate || {});
    this.muted = !!o.muted;
    this.silence = new Float32Array(128);
    this.blocks = 0;
    this.peak = 0;
    this.rms = 0;
    this.port.onmessage = (e) => {
      const m = e.data;
      if (m.type === 'mute') this.muted = !!m.on;
    };
  }

  process(inputs) {
    const ch = inputs[0] && inputs[0][0];
    // A source that has ended (file playback finished) leaves the input
    // empty; treat that as silence so frames keep flowing like a quiet mic
    // (the gate then closes after its hold time).
    const block = ch && ch.length ? ch : this.silence;
    if (this.muted) {
      // Drop everything while muted. The pipeline keeps its filter state,
      // which is fine: a mute is a discontinuity anyway.
      if (this.blocks++ % LEVEL_EVERY_BLOCKS === 0) this.port.postMessage({ type: 'level', peak: 0, rms: 0, muted: true, gated: false, gatedFrames: this.gatedFrames() });
      return true;
    }
    const lv = levelOf(block);
    if (lv.peak > this.peak) this.peak = lv.peak;
    this.rms += lv.rms;
    if (++this.blocks % LEVEL_EVERY_BLOCKS === 0) {
      this.port.postMessage({
        type: 'level', peak: this.peak, rms: this.rms / LEVEL_EVERY_BLOCKS, muted: false,
        gated: !!(this.gate && this.gate.gated), gatedFrames: this.gatedFrames(),
      });
      this.peak = 0;
      this.rms = 0;
    }
    for (const frame of this.pipeline.process(block)) {
      if (!this.gate) {
        this.post(frame);
        continue;
      }
      const r = this.gate.push(frame);
      if (r.transition) this.port.postMessage({ type: 'silence', on: r.transition === 'on', peak: this.gate.peak });
      for (const f of r.send) this.post(f);
    }
    return true;
  }

  gatedFrames() {
    return this.gate ? this.gate.gatedFrames : 0;
  }

  post(frame) {
    // Transfer the buffer: no copy, and the page owns it from here.
    this.port.postMessage({ type: 'frame', data: frame.buffer }, [frame.buffer]);
  }
}

registerProcessor('sheepdog-capture', CaptureProcessor);
