// sheepdog /voice: the browser side of the audio pipe (plan §3.8, decisions
// D2 and D11). Microphone → capture worklet (16 kHz PCM16 frames) → /ws;
// /ws (24 kHz PCM16) → playback worklet (ring buffer) → speaker. No model
// logic lives here: the page only moves audio and a few control messages
// (docs/dev/ws-contract.md). Vanilla JS module, no build step.

import { AEC_HINT, aecStatus, aecWarn, isMobile } from './aec.js';
import { Handshake, clientReady, HANDSHAKE_TIMEOUT_MS } from './handshake.js';
import { visibilityMessage, cardsEnabled, cardBodyHTML, cardLabel } from './cards.js';

const $ = (id) => document.getElementById(id);

// The ping carries the playback counters, so it runs at the daemon's
// web.client.stats interval (5 s): at 15 s, three stats lines in a row
// repeated one sample and read as a frozen player (2026-09-24 18:21:30).
const PING_EVERY_MS = 5000;
const PONG_TIMEOUT_MS = 10000;
const RECONNECT_MIN_MS = 1000;
const RECONNECT_MAX_MS = 10000;
const MAX_WS_BUFFERED = 1 << 20; // drop mic frames if the socket backs up this far
const TRANSCRIPT_LINES = 8;
const BUNDLED_FIXTURE = '/testdata/question.wav';

const state = {
  ctx: null,
  captureNode: null,
  playbackNode: null,
  micStream: null,
  micSource: null,
  fileBuffer: null,
  fileSource: null,
  fileName: '',
  source: 'mic',

  ws: null,
  wsState: 'idle', // idle | connecting | open | closed
  userDisconnected: true,
  reconnectDelay: RECONNECT_MIN_MS,
  reconnectTimer: 0,
  reconnects: 0,
  pingTimer: 0,
  pingSentAt: 0,
  pingOutstanding: false,
  pongMs: null,
  // The /ws protocol handshake of the current connection (handshake.js).
  handshake: null,
  handshakeTimer: 0,

  muted: false,
  daemonMuted: false,
  voice: '…',
  voiceReason: '',
  voiceStalled: false,

  framesSent: 0,
  bytesOut: 0,
  bytesIn: 0,
  framesDropped: 0,
  level: { peak: 0, rms: 0, muted: false, gated: false, gatedFrames: 0 },
  gate: { opts: {}, on: false, transitions: 0 }, // silence gate (docs/dev/ws-contract.md)
  playback: { speaking: false, state: 'idle', bufferedMs: 0, targetMs: 150, underruns: 0, drains: 0, dropped: 0, flushes: 0, playedMs: 0, rate: 1, stretchedMs: 0 },
  // The voice provider the playback policy was last set for (status.provider).
  playbackProvider: '',
  lastUnderruns: 0,
  flushSeq: 0,
  pendingFlushes: new Map(), // id → { kind, t0 }
  // aec: the microphone's echo cancellation as the browser reports it
  // ('true'/'false'/'unknown'), sent on the ping (docs/dev/ws-contract.md).
  aec: 'unknown',
  lastStopMs: null,
  lastFlushMs: null,

  transcript: [], // { dir, text, final }
  errorTimer: 0,
};

// ---------------------------------------------------------------- audio

function audioContextClass() {
  return window.AudioContext || window.webkitAudioContext;
}

// ensureAudio creates the AudioContext and both worklet nodes once. Call it
// from a user gesture the first time (autoplay policy: Chrome and Safari
// only start a context that was created or resumed inside a gesture).
async function ensureAudio() {
  if (state.ctx) {
    // Never await resume(): outside a gesture Chrome leaves the promise
    // pending until one happens, and the caller would hang with it.
    if (state.ctx.state !== 'running') state.ctx.resume().catch(() => {});
    return state.ctx;
  }
  const AC = audioContextClass();
  if (!AC) throw new Error('Web Audio is not available in this browser');
  // Native device rate (48 kHz on most Macs, 44.1 kHz elsewhere): the
  // worklets resample. Forcing 16 kHz here is rejected by Safari.
  const ctx = new AC({ latencyHint: 'interactive' });
  state.ctx = ctx;
  ctx.resume().catch(() => {}); // synchronously inside the gesture (see above)
  if (!ctx.audioWorklet) throw new Error('AudioWorklet is not available (Safari 14.1+, Chrome 66+ required)');
  await ctx.audioWorklet.addModule('/voice/audio-capture.worklet.js');
  await ctx.audioWorklet.addModule('/voice/audio-playback.worklet.js');

  const playback = new AudioWorkletNode(ctx, 'sheepdog-playback', {
    numberOfInputs: 0, numberOfOutputs: 1, outputChannelCount: [1],
  });
  playback.port.onmessage = (e) => onPlaybackMessage(e.data);
  playback.connect(ctx.destination);
  state.playbackNode = playback;
  state.playbackPolicySent = false;
  if (state.playbackProvider) setPlaybackPolicy(state.playbackProvider);

  const capture = new AudioWorkletNode(ctx, 'sheepdog-capture', {
    numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1],
    processorOptions: { muted: state.muted, gate: state.gate.opts },
  });
  capture.port.onmessage = (e) => onCaptureMessage(e.data);
  // A worklet is only rendered while it is connected to the destination;
  // its output is unused, so route it through a zero gain.
  const sink = ctx.createGain();
  sink.gain.value = 0;
  capture.connect(sink).connect(ctx.destination);
  state.captureNode = capture;

  ctx.onstatechange = () => render();
  console.log(`[voice] audio context ${ctx.sampleRate} Hz, base latency ${Math.round((ctx.baseLatency || 0) * 1000)} ms`);
  return ctx;
}

function onCaptureMessage(m) {
  if (m.type === 'level') {
    state.level = m;
    return;
  }
  if (m.type === 'silence') {
    // The gate flipped: tell the daemon before (on) / after (off) the
    // frames it affects, exactly like mute.
    state.gate.on = m.on;
    state.gate.transitions++;
    send({ type: 'silence', on: m.on });
    console.log(`[voice] silence gate ${m.on ? 'on' : 'off'} (peak ${Math.round(m.peak)} LSB, frames sent ${state.framesSent})`);
    return;
  }
  if (m.type !== 'frame') return;
  if (state.muted) return; // the worklet already stops; belt and braces
  const ws = state.ws;
  if (!ws || ws.readyState !== WebSocket.OPEN) return;
  if (ws.bufferedAmount > MAX_WS_BUFFERED) {
    state.framesDropped++;
    return;
  }
  ws.send(m.data);
  state.framesSent++;
  state.bytesOut += m.data.byteLength;
}

// setPlaybackPolicy tells the playback worklet which provider's jitter
// buffer policy to use (docs/dev/ws-contract.md "Playback jitter buffer").
// Remembered so a worklet created later (audio starts on a gesture) gets
// it too.
function setPlaybackPolicy(provider) {
  if (provider === state.playbackProvider && state.playbackPolicySent) return;
  state.playbackProvider = provider;
  state.playbackPolicySent = false;
  if (!state.playbackNode) return;
  state.playbackNode.port.postMessage({ type: 'policy', provider });
  state.playbackPolicySent = true;
  console.log(`[voice] playback policy: ${provider}`);
}

function onPlaybackMessage(m) {
  if (m.type === 'playback') {
    // The daemon keeps "the voice is speaking" true until the reply has
    // actually played, and trims a cut-off reply to what was heard
    // (docs/dev/ws-contract.md "Playback reports").
    send({ type: 'playback', state: m.state, played_ms: m.playedMs });
    if (m.state !== 'started') console.log(`[voice] playback ${m.state} after ${m.playedMs} ms`);
    return;
  }
  if (m.type === 'stats') {
    state.playback = m;
    if (m.underruns !== state.lastUnderruns) {
      console.log(`[voice] playback underruns=${m.underruns} drains=${m.drains} buffered=${m.bufferedMs}ms`);
      state.lastUnderruns = m.underruns;
    }
    return;
  }
  if (m.type === 'flushed') {
    const p = state.pendingFlushes.get(m.id);
    if (!p) return;
    state.pendingFlushes.delete(m.id);
    const ms = performance.now() - p.t0;
    const lat = state.ctx ? Math.round(((state.ctx.baseLatency || 0) + (state.ctx.outputLatency || 0)) * 1000) : 0;
    if (p.kind === 'stop') {
      state.lastStopMs = ms;
      console.log(`[voice] stop: click → ring buffer empty ${ms.toFixed(1)} ms (+ ${lat} ms output latency to silence)`);
    } else {
      state.lastFlushMs = ms;
      console.log(`[voice] flush: receipt → ring buffer empty ${ms.toFixed(1)} ms (+ ${lat} ms output latency to silence)`);
    }
    render();
  }
}

// localFlush drops the playback buffer now and measures how long it took.
function localFlush(kind) {
  if (!state.playbackNode) return;
  const id = ++state.flushSeq;
  state.pendingFlushes.set(id, { kind, t0: performance.now() });
  state.playbackNode.port.postMessage({ type: 'flush', id });
}

function onAudio(buf) {
  state.bytesIn += buf.byteLength;
  if (state.playbackNode) state.playbackNode.port.postMessage({ type: 'audio', data: buf }, [buf]);
}

// ---------------------------------------------------------------- sources

async function startSource() {
  stopSource();
  const ctx = await ensureAudio();
  if (state.source === 'mic') {
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      throw new Error('getUserMedia is not available (open the page over http://localhost or https)');
    }
    const stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
    state.micStream = stream;
    state.micSource = ctx.createMediaStreamSource(stream);
    state.micSource.connect(state.captureNode);
    const track = stream.getAudioTracks()[0];
    console.log(`[voice] microphone: ${track ? track.label : '?'}`);
    checkEchoCancellation(track);
  } else if (state.fileBuffer) {
    playFile();
  }
}

// checkEchoCancellation reads what the browser actually applied (a request
// for echoCancellation is only a hint) and shows a one-line warning when
// the model would hear its own voice: echo cancellation off, or not
// reported on a mobile browser.
function checkEchoCancellation(track) {
  let settings;
  try { settings = track && track.getSettings ? track.getSettings() : undefined; } catch (_) { settings = undefined; }
  state.aec = aecStatus(settings);
  const warn = aecWarn(state.aec, isMobile(navigator.userAgent, navigator.maxTouchPoints || 0));
  const el = $('aec-hint');
  el.textContent = AEC_HINT;
  el.classList.toggle('hidden', !warn);
  console.log(`[voice] echo cancellation: ${state.aec}${warn ? ' (warning shown)' : ''}`);
}

function stopSource() {
  if (state.micSource) {
    try { state.micSource.disconnect(); } catch (_) { /* already gone */ }
    state.micSource = null;
  }
  if (state.micStream) {
    for (const t of state.micStream.getTracks()) t.stop();
    state.micStream = null;
  }
  stopFile();
}

// playFile feeds the decoded file through the capture worklet at real-time
// pace, exactly like a microphone; when it ends the worklet sees silence.
function playFile() {
  stopFile();
  if (!state.fileBuffer || !state.ctx) return;
  const src = state.ctx.createBufferSource();
  src.buffer = state.fileBuffer;
  src.loop = $('file-loop').checked;
  src.connect(state.captureNode);
  src.onended = () => {
    if (state.fileSource === src) state.fileSource = null;
    render();
  };
  src.start();
  state.fileSource = src;
  render();
}

function stopFile() {
  if (!state.fileSource) return;
  try { state.fileSource.stop(); } catch (_) { /* not started */ }
  try { state.fileSource.disconnect(); } catch (_) { /* fine */ }
  state.fileSource = null;
}

async function loadFileBytes(bytes, name) {
  // Decode with a throwaway offline context so loading a file (possibly
  // outside a user gesture, e.g. ?src=file at page load) never creates the
  // real AudioContext; that stays a Connect-click job (autoplay policy).
  const decoder = new (window.OfflineAudioContext || window.webkitOfflineAudioContext)(1, 1, 48000);
  // Old Safari only offers the callback form of decodeAudioData.
  const buffer = await new Promise((resolve, reject) => {
    const p = decoder.decodeAudioData(bytes, resolve, reject);
    if (p && p.then) p.then(resolve, reject);
  });
  state.fileBuffer = buffer;
  state.fileName = name;
  console.log(`[voice] loaded ${name}: ${buffer.duration.toFixed(2)} s, ${buffer.sampleRate} Hz, ${buffer.numberOfChannels} ch`);
  render();
}

async function loadBundledFixture(quiet) {
  const resp = await fetch(BUNDLED_FIXTURE, { cache: 'no-cache' });
  if (!resp.ok) {
    if (!quiet) showError(`${BUNDLED_FIXTURE} → ${resp.status}: start the daemon with --dev (or set web.dev) to serve the fixtures, or pick a .wav file.`);
    return false;
  }
  await loadFileBytes(await resp.arrayBuffer(), 'question.wav');
  return true;
}

// ---------------------------------------------------------------- websocket

function wsURL() {
  const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
  return proto + location.host + '/ws';
}

function openWS() {
  clearTimeout(state.reconnectTimer);
  state.reconnectTimer = 0;
  const ws = new WebSocket(wsURL());
  ws.binaryType = 'arraybuffer';
  state.ws = ws;
  state.wsState = 'connecting';
  render();
  ws.onopen = () => {
    if (state.ws !== ws) return;
    state.wsState = 'open';
    state.reconnectDelay = RECONNECT_MIN_MS;
    // client-ready goes first, before anything else on the connection
    // (docs/dev/ws-contract.md "Protocol version and handshake"); nothing
    // waits for bot-ready — an older daemon never sends one.
    state.handshake = new Handshake();
    state.cardsOn = false;
    send(clientReady());
    clearTimeout(state.handshakeTimer);
    state.handshakeTimer = setTimeout(() => {
      if (state.ws !== ws || !state.handshake) return;
      state.handshake.onTimeout();
      render();
    }, HANDSHAKE_TIMEOUT_MS);
    // Reconnecting clients start unmuted and ungated on the daemon (contract).
    if (state.muted) send({ type: 'mute', on: true });
    if (state.gate.on) send({ type: 'silence', on: true });
    startPing();
    render();
  };
  ws.onmessage = (e) => {
    if (state.ws !== ws) return;
    if (e.data instanceof ArrayBuffer) {
      onAudio(e.data);
      return;
    }
    let m;
    try { m = JSON.parse(e.data); } catch (_) { return; }
    onControl(m);
  };
  ws.onclose = (e) => {
    if (state.ws !== ws) return;
    state.ws = null;
    state.wsState = 'closed';
    stopPing();
    clearTimeout(state.handshakeTimer);
    console.log(`[voice] ws closed code=${e.code} reason=${JSON.stringify(e.reason)}`);
    if (state.handshake && (state.handshake.state === 'rejected' || state.handshake.state === 'daemon_old')) {
      // A protocol mismatch does not fix itself: no reconnect loop.
      return;
    }
    if (e.reason === 'replaced') {
      // Another client (a second tab, the phone) took the session over.
      // Reconnecting would take it back, and that client would do the
      // same, in a loop (docs/dev/timelines.md C14): stop, as the iOS app
      // does, and let the user press Connect to take it back.
      disconnect();
      showError('Another client took over the voice session. Press Connect to take it back.');
      return;
    }
    if (!state.userDisconnected) scheduleReconnect();
    render();
  };
  ws.onerror = () => { /* onclose follows */ };
}

function scheduleReconnect() {
  const delay = state.reconnectDelay;
  state.reconnectDelay = Math.min(state.reconnectDelay * 2, RECONNECT_MAX_MS);
  console.log(`[voice] reconnecting in ${delay} ms`);
  state.reconnectTimer = setTimeout(() => {
    if (state.userDisconnected) return;
    state.reconnects++;
    openWS();
  }, delay);
}

function send(m) {
  const ws = state.ws;
  if (!ws || ws.readyState !== WebSocket.OPEN) return false;
  ws.send(JSON.stringify(m));
  return true;
}

function startPing() {
  stopPing();
  state.pingOutstanding = false;
  state.pingTimer = setInterval(() => {
    if (state.pingOutstanding) {
      // Wait for the pong rather than send another ping (which would
      // restart the clock): the ping runs more often than the timeout.
      if (performance.now() - state.pingSentAt >= PONG_TIMEOUT_MS) {
        console.log('[voice] pong timeout; closing socket');
        if (state.ws) state.ws.close(4000, 'pong timeout');
      }
      return;
    }
    state.pingSentAt = performance.now();
    // The ping carries the gate counters and the jitter buffer's playback
    // stats so web.client.stats can report them (docs/dev/ws-contract.md).
    state.pingOutstanding = send({
      type: 'ping',
      gated_frames: state.level.gatedFrames || 0,
      peak: Math.round((state.level.peak || 0) * 0x7fff),
      underruns: state.playback.underruns || 0,
      drains: state.playback.drains || 0,
      buffer_ms: state.playback.bufferedMs || 0,
      rate: state.playback.rate || 1,
      stretched_ms: state.playback.stretchedMs || 0,
      // A context that is not running plays nothing and its worklet stops
      // reporting: the counters above are then its last values.
      audio_state: state.ctx ? state.ctx.state : undefined,
      aec: state.aec,
    });
  }, PING_EVERY_MS);
}

function stopPing() {
  clearInterval(state.pingTimer);
  state.pingTimer = 0;
  state.pingOutstanding = false;
}

function onControl(m) {
  if (state.handshake) {
    const r = state.handshake.onMessage(m);
    if (r.close) {
      console.log(`[voice] protocol: ${state.handshake.state}`);
      const handshake = state.handshake;
      disconnect();
      state.handshake = handshake;
      showError(r.message);
      render();
      return;
    }
    if (r.consumed) {
      if (state.handshake.state === 'ready' && cardsEnabled(state.handshake.features)) {
        // Each connection starts "not visible" on the daemon: say so now.
        state.cardsOn = true;
        send(visibilityMessage(document.hidden));
      }
      render();
      return;
    }
  }
  switch (m.type) {
    case 'status':
      state.voice = m.voice;
      state.voiceReason = m.reason || '';
      state.voiceStalled = !!m.stalled;
      state.daemonMuted = !!m.muted;
      if (m.provider) setPlaybackPolicy(m.provider);
      if (m.voice === 'closed' && m.reason === 'setup_needed') showError(`Setup needed: there is no voice key yet. Finish setup at ${location.origin}/setup (or run \`sheepdog setup\` on this computer).`);
      else if (m.voice === 'closed' && m.reason && m.reason !== 'voice_off') showError(`voice session closed: ${m.reason}`);
      break;
    case 'flush':
      // Everything received before this is stale: drop it now, measure it.
      localFlush('flush');
      closeTranscript('out');
      break;
    case 'transcript':
      addTranscript(m.dir, m.text, !!m.final);
      break;
    case 'card':
      if (m.card && !state.transcript.some((t) => t.card && t.card.card_id === m.card.card_id)) {
        closeTranscript('in');
        closeTranscript('out');
        state.transcript.push({ dir: 'card', text: cardLabel(m.card), final: true, card: m.card });
        while (state.transcript.length > TRANSCRIPT_LINES) state.transcript.shift();
        openCard(m.card);
      }
      break;
    case 'error':
      if (m.problem) showAccountProblem(m);
      else showError(m.message || 'error');
      break;
    case 'pong':
      state.pongMs = performance.now() - state.pingSentAt;
      state.pingOutstanding = false;
      break;
    default:
      break;
  }
  render();
}

// ---------------------------------------------------------------- transcript

function addTranscript(dir, text, final) {
  const last = state.transcript[state.transcript.length - 1];
  // A typed utterance is echoed locally; --voice=off sends it back as an
  // `in` transcript, so skip that duplicate.
  if (dir === 'in' && final && last && last.dir === 'in' && last.local && last.text === text) {
    last.local = false;
    return;
  }
  // Output fragments may never carry final (contract): the other
  // direction starting is the boundary, and so is a flush.
  closeTranscript(dir === 'in' ? 'out' : 'in');
  if (last && last.dir === dir && !last.final) {
    last.text += text;
    last.final = final;
  } else {
    state.transcript.push({ dir, text, final });
  }
  while (state.transcript.length > TRANSCRIPT_LINES) state.transcript.shift();
}

function closeTranscript(dir) {
  const last = state.transcript[state.transcript.length - 1];
  if (last && last.dir === dir) last.final = true;
}

// ---------------------------------------------------------------- actions

async function connect() {
  state.userDisconnected = false;
  hideError();
  try {
    await startSource();
  } catch (err) {
    showError(`audio: ${err.message || err}`);
    state.userDisconnected = true;
    render();
    return;
  }
  openWS();
}

function disconnect() {
  state.userDisconnected = true;
  clearTimeout(state.reconnectTimer);
  state.reconnectTimer = 0;
  stopPing();
  if (state.ws) {
    const ws = state.ws;
    state.ws = null;
    ws.close(1000, 'user disconnect');
  }
  state.wsState = 'idle';
  stopSource();
  localFlush('stop');
  if (state.ctx && state.ctx.state === 'running') state.ctx.suspend().catch(() => {});
  state.voice = '…';
  render();
}

function setMuted(on) {
  if (state.muted === on) return;
  state.muted = on;
  const port = state.captureNode && state.captureNode.port;
  if (on) {
    // Stop the source first so no frame can follow the mute message.
    if (port) port.postMessage({ type: 'mute', on: true });
    send({ type: 'mute', on: true });
  } else {
    // Tell the daemon first so it does not drop the first frames.
    send({ type: 'mute', on: false });
    if (port) port.postMessage({ type: 'mute', on: false });
  }
  console.log(`[voice] mute ${on ? 'on' : 'off'} (frames sent so far: ${state.framesSent})`);
  render();
}

// stop is the "stop" button: silence now, and ask the daemon to cancel
// (it answers with flush, which we measure separately).
function stop() {
  localFlush('stop');
  closeTranscript('out');
  send({ type: 'cancel' });
  render();
}

// resetConversation is the "New conversation" button: it resets the
// orchestrator's transcript window and the voice session's memory
// (docs/dev/roles.md) through POST /api/conversation/reset {via:
// "voice_page"}; a confirm() guards it since it clears the shown
// conversation (the agents, promises, notes and pending prompts are
// unaffected).
async function resetConversation() {
  if (!confirm('Start a new conversation? This clears the conversation so far; the agents, promises, notes and pending prompts are kept.')) return;
  const btn = $('btn-new-conversation');
  const result = $('new-conversation-result');
  btn.disabled = true;
  result.textContent = '…';
  try {
    const r = await fetch('/api/conversation/reset', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ via: 'voice_page' }) });
    const data = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(data.error || `${r.status}`);
    result.textContent = `reset ${data.reset_id} at ${data.at}`;
  } catch (err) {
    result.textContent = `error: ${err.message}`;
  } finally {
    btn.disabled = false;
  }
}

function sendText() {
  const input = $('text-input');
  const text = input.value.trim();
  if (!text) return;
  if (!send({ type: 'text', text })) {
    showError('not connected');
    return;
  }
  input.value = '';
  // The voice session does not transcribe typed input; show it ourselves.
  closeTranscript('out');
  state.transcript.push({ dir: 'in', text, final: true, local: true });
  while (state.transcript.length > TRANSCRIPT_LINES) state.transcript.shift();
}

function showError(msg, ms = 12000) {
  const el = $('error');
  el.textContent = msg;
  el.classList.remove('hidden');
  clearTimeout(state.errorTimer);
  state.errorTimer = setTimeout(hideError, ms);
  console.warn(`[voice] ${msg}`);
}

// showAccountProblem shows an account problem (docs/dev/ws-contract.md
// "Account problems") with its fix, longer than a passing error. When it
// is the voice's own provider, the voice cannot say it, so the browser
// says it once per problem.
function showAccountProblem(m) {
  const fix = m.fix ? ` Fix: ${m.fix}.` : '';
  showError(`${m.message}${fix}`, 60000);
  const key = `${m.role}/${m.provider}/${m.problem}`;
  state.accountSpoken = state.accountSpoken || new Set();
  if (m.role !== 'voice' || state.accountSpoken.has(key)) return;
  state.accountSpoken.add(key);
  try {
    if (window.speechSynthesis) window.speechSynthesis.speak(new SpeechSynthesisUtterance(m.message));
  } catch (e) {
    console.warn('[voice] speech synthesis failed', e);
  }
}

function hideError() {
  $('error').classList.add('hidden');
}

// ---------------------------------------------------------------- render

function fmtBytes(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / 1024 / 1024).toFixed(2)} MiB`;
}

function setDot(id, cls, text) {
  $(id + '-dot').className = `dot ${cls}`;
  $(id + '-text').textContent = text;
}

function render() {
  const connected = !state.userDisconnected;
  const open = state.wsState === 'open';
  setDot('ws', open ? 'open' : state.wsState === 'connecting' ? 'connecting' : state.wsState === 'closed' ? 'closed' : 'idle',
    `ws: ${state.wsState}${state.wsState === 'closed' && connected ? ' (reconnecting)' : ''}`);
  const v = connected ? state.voice : '…';
  setDot('voice', v === '…' ? 'idle' : state.voiceStalled ? 'stalled' : v === 'open' && state.playback.speaking ? 'open speaking' : v, `voice: ${v}${state.voiceReason && v === 'closed' ? ` (${state.voiceReason})` : ''}${state.daemonMuted && open ? ' · muted' : ''}${state.voiceStalled && open ? ' · stalled (no reply)' : ''}`);

  const btnConnect = $('btn-connect');
  btnConnect.textContent = connected ? 'Disconnect' : 'Connect';
  btnConnect.classList.toggle('primary', !connected);
  $('btn-mute').disabled = !connected;
  $('btn-mute').textContent = state.muted ? 'Unmute' : 'Mute';
  $('btn-mute').classList.toggle('active', state.muted);
  $('btn-stop').disabled = !open;
  $('btn-text').disabled = !open;
  $('mute-flag').classList.toggle('hidden', !state.muted);
  $('speaking-flag').classList.toggle('hidden', !state.playback.speaking);

  const meter = document.querySelector('.meter');
  meter.classList.toggle('muted', state.muted);
  const lv = state.muted ? { peak: 0, rms: 0 } : state.level;
  $('level-bar').style.width = `${Math.min(100, Math.round(lv.rms * 300))}%`;
  $('level-peak').style.left = `${Math.min(100, Math.round(lv.peak * 100))}%`;
  $('meter-text').textContent = state.muted ? 'muted' : lv.peak > 0 ? `peak ${(20 * Math.log10(lv.peak)).toFixed(0)} dBFS` : '';

  $('file-controls').classList.toggle('hidden', state.source !== 'file');
  $('file-play').disabled = !state.fileBuffer;
  $('file-play').textContent = state.fileSource ? 'restart file' : 'play into mic';
  $('file-text').textContent = state.fileBuffer
    ? `${state.fileName} (${state.fileBuffer.duration.toFixed(1)} s)${state.fileSource ? ' · playing' : ''}`
    : 'no file loaded';

  const ctx = state.ctx;
  $('st-ctx').textContent = ctx ? `${ctx.state}, ${ctx.sampleRate} Hz, out latency ${Math.round(((ctx.baseLatency || 0) + (ctx.outputLatency || 0)) * 1000)} ms` : '–';
  $('st-frames').textContent = `${state.framesSent}${state.framesDropped ? ` (dropped ${state.framesDropped})` : ''}`;
  const g = state.gate;
  $('st-gate').textContent = g.opts === false ? 'off (?gate=off)'
    : `${state.level.gated && !state.muted ? 'silent (gated)' : 'open'} · ${state.level.gatedFrames || 0} frames gated · ${g.transitions} transitions`;
  $('st-bytes-out').textContent = fmtBytes(state.bytesOut);
  $('st-bytes-in').textContent = fmtBytes(state.bytesIn);
  const p = state.playback;
  $('st-play').textContent = `${p.state}, buffered ${p.bufferedMs} ms, played ${(p.playedMs / 1000).toFixed(1)} s, flushes ${p.flushes}${p.dropped ? `, dropped ${p.dropped}` : ''}`;
  $('st-underruns').textContent = `${p.underruns} (drains ${p.drains}, target ${p.targetMs != null ? p.targetMs : 150} ms) · rate ${(p.rate || 1).toFixed(2)}× · stretched ${((p.stretchedMs || 0) / 1000).toFixed(1)} s`;
  $('st-stop').textContent = state.lastStopMs == null ? '–' : `click → buffer empty ${state.lastStopMs.toFixed(1)} ms`;
  $('st-flush').textContent = state.lastFlushMs == null ? '–' : `flush → buffer empty ${state.lastFlushMs.toFixed(1)} ms`;
  $('st-reconnects').textContent = String(state.reconnects);
  $('st-ping').textContent = state.pongMs == null ? '–' : `${state.pongMs.toFixed(0)} ms rtt`;
  $('st-protocol').textContent = state.handshake ? state.handshake.describe() : '–';

  renderTranscript();
}

function renderTranscript() {
  const box = $('transcript');
  const key = JSON.stringify(state.transcript);
  if (box.dataset.key === key) return;
  box.dataset.key = key;
  box.textContent = '';
  if (state.transcript.length === 0) {
    const p = document.createElement('p');
    p.className = 'muted';
    p.textContent = 'Nothing yet.';
    box.appendChild(p);
    return;
  }
  for (const t of state.transcript) {
    if (t.card) {
      const line = document.createElement('div');
      line.className = 'line card-row';
      const dir = document.createElement('span');
      dir.className = 'dir';
      dir.textContent = 'card';
      const open = document.createElement('button');
      open.className = 'card-open';
      open.textContent = t.text;
      open.setAttribute('aria-label', `Open card: ${t.text}`);
      open.addEventListener('click', () => openCard(t.card));
      line.append(dir, open);
      box.appendChild(line);
      continue;
    }
    const line = document.createElement('div');
    line.className = `line ${t.dir}${t.final ? '' : ' partial'}`;
    const dir = document.createElement('span');
    dir.className = 'dir';
    dir.textContent = t.dir === 'in' ? 'you' : 'voice';
    const text = document.createElement('span');
    text.className = 'text';
    text.textContent = t.text + (t.final ? '' : ' …');
    line.append(dir, text);
    box.appendChild(line);
  }
  box.scrollTop = box.scrollHeight;
}

// ---------------------------------------------------------------- cards

function openCard(card) {
  const dlg = $('card-dialog');
  $('card-title').textContent = card.title || 'Card';
  const who = card.spoken || card.agent || '';
  $('card-meta').textContent = who;
  $('card-body').innerHTML = cardBodyHTML(card); // cards.js escapes everything
  if (typeof dlg.showModal === 'function') {
    if (!dlg.open) dlg.showModal();
  } else {
    dlg.setAttribute('open', '');
  }
}

document.addEventListener('visibilitychange', () => {
  if (state.cardsOn) send(visibilityMessage(document.hidden));
});

// ---------------------------------------------------------------- auth

// authGate probes an endpoint that needs the daemon's token (config
// web.token) before anything else runs. No token configured, or the
// token cookie from an earlier POST /api/login already covers it: the
// probe answers normally and the rest of init() proceeds. A 401 means
// the daemon wants a token: show the inline prompt and hide the rest of
// the page instead of wiring up a Connect button that would only 401
// itself on /ws. Submitting POSTs /api/login, which sets the cookie
// every later request on this origin then carries automatically —
// including the WebSocket one this page cannot attach a header to — and
// reloads the page. Returns false while the gate is up.
async function authGate() {
  let res;
  try {
    res = await fetch('/api/state', { cache: 'no-store' });
  } catch (_) {
    return true; // offline: let the rest of the page surface the error
  }
  if (res.status !== 401) return true;
  $('auth-gate').classList.remove('hidden');
  $('app-main').classList.add('hidden');
  const submit = async () => {
    const token = $('auth-token').value.trim();
    if (!token) return;
    $('auth-submit').disabled = true;
    try {
      const r = await fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }) });
      const data = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(data.error || `${r.status}`);
      location.reload();
    } catch (err) {
      $('auth-error').textContent = `error: ${err.message}`;
      $('auth-error').classList.remove('hidden');
      $('auth-submit').disabled = false;
    }
  };
  $('auth-submit').addEventListener('click', submit);
  $('auth-token').addEventListener('keydown', (e) => { if (e.key === 'Enter') submit(); });
  return false;
}

// ---------------------------------------------------------------- wiring

// gateOptions reads the silence gate knobs from the URL: ?gate=off disables
// it, ?gate_floor=LSB, ?gate_hold=ms and ?gate_preroll=ms override the
// defaults (8 LSB, 300 ms, 100 ms; see SilenceGate in audio-util.js).
function gateOptions(params) {
  if (params.get('gate') === 'off') return false;
  const o = {};
  const num = (key, field) => {
    const v = Number(params.get(key));
    if (params.has(key) && Number.isFinite(v) && v >= 0) o[field] = v;
  };
  num('gate_floor', 'floor');
  num('gate_hold', 'holdMs');
  num('gate_preroll', 'prerollMs');
  return o;
}

function resumeIfSuspended() {
  const ctx = state.ctx;
  if (ctx && !state.userDisconnected && ctx.state !== 'running' && ctx.state !== 'closed') {
    ctx.resume().catch(() => {});
  }
}

async function init() {
  if (!(await authGate())) return; // token prompt is up; wiring resumes after the reload
  const params = new URLSearchParams(location.search);
  state.gate.opts = gateOptions(params);
  if (params.get('src') === 'file') {
    state.source = 'file';
    $('source').value = 'file';
    loadBundledFixture(true).catch(() => {});
  }

  $('btn-connect').addEventListener('click', () => (state.userDisconnected ? connect() : disconnect()));
  $('btn-mute').addEventListener('click', () => setMuted(!state.muted));
  $('btn-stop').addEventListener('click', stop);
  $('btn-new-conversation').addEventListener('click', resetConversation);
  $('btn-text').addEventListener('click', sendText);
  $('text-input').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      sendText();
    }
  });
  $('source').addEventListener('change', async (e) => {
    state.source = e.target.value;
    if (!state.userDisconnected) {
      try { await startSource(); } catch (err) { showError(`audio: ${err.message || err}`); }
    }
    render();
  });
  $('file-input').addEventListener('change', async (e) => {
    const f = e.target.files && e.target.files[0];
    if (!f) return;
    try {
      await loadFileBytes(await f.arrayBuffer(), f.name);
      if (!state.userDisconnected) playFile();
    } catch (err) {
      showError(`decode ${f.name}: ${err.message || err}`);
    }
  });
  $('file-bundled').addEventListener('click', async () => {
    try {
      if (await loadBundledFixture(false) && !state.userDisconnected) playFile();
    } catch (err) {
      showError(`fetch ${BUNDLED_FIXTURE}: ${err.message || err}`);
    }
  });
  $('file-play').addEventListener('click', async () => {
    try {
      await ensureAudio();
      playFile();
    } catch (err) {
      showError(`audio: ${err.message || err}`);
    }
  });
  $('file-loop').addEventListener('change', (e) => {
    if (state.fileSource) state.fileSource.loop = e.target.checked;
  });

  document.addEventListener('keydown', (e) => {
    const inField = e.target && (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA' || e.target.tagName === 'SELECT');
    if ($('card-dialog').open) return; // Esc closes the card, not the voice
    if (e.key === 'Escape') {
      if (!$('btn-stop').disabled) stop();
      return;
    }
    if (inField || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === ' ') {
      e.preventDefault();
      if (!$('btn-stop').disabled) stop();
    } else if (e.key === 'm' || e.key === 'M') {
      if (!$('btn-mute').disabled) setMuted(!state.muted);
    }
  });

  // Tab hidden / phone locked: keep the socket; if the browser suspended
  // the context, resume it when we are visible again or on the next gesture.
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') resumeIfSuspended();
  });
  window.addEventListener('pageshow', resumeIfSuspended);
  window.addEventListener('pagehide', () => { /* nothing: the daemon keeps the session (D2) */ });
  document.addEventListener('pointerdown', resumeIfSuspended, { capture: true });

  setInterval(render, 100);
  render();

  // Debug handle (console only): inspect state, inject 24 kHz PCM16 into
  // the playback path without a model, or flush it.
  window.sheepdogVoice = { state, injectAudio: onAudio, flush: () => localFlush('stop') };
}

init();
