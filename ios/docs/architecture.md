# Architecture

Two targets, one rule: anything that can be tested without a microphone
lives in `SheepdogKit`, and the app target is SwiftUI views plus
`@MainActor` view models over it.

```
┌──────────────────────────── Sheepdog (app target) ────────────────────────────┐
│  MainView ── AgentStrip ── ConversationView ── PresenceOrb ── ControlBar       │
│  SettingsView                                                                 │
│        │                                                                      │
│  SessionViewModel (@MainActor): the only place that decides anything          │
│        │                     │                    │                           │
│  AudioEngine            VoiceSocket          DaemonClient                     │
│  (AVAudioEngine,        (/ws)                (/api/hello, /api/state,         │
│   AVAudioSession)                             /api/login, /events)            │
└───────┼─────────────────────┼─────────────────────┼───────────────────────────┘
        │                     │                     │
┌───────┼─────────────────────┼─────────────────────┼─── SheepdogKit ───────────┐
│  PCMConverter          WSMessage             SSEParser                        │
│  FrameChunker          (wsproto mirror)      Hello / DaemonState / Agent      │
│  SilenceGate                                 DaemonError                      │
│  PlaybackQueue         ConversationReducer   SettingsStore / SecretStore      │
│  AudioMetrics          SessionState                                           │
└───────────────────────────────────────────────────────────────────────────────┘
                                   │
                        sheepdog daemon on your Mac
```

## The daemon contract, mirrored

`WSMessage` is a direct mirror of the daemon's `internal/voice/wsproto`
package: the same ten message types, the same required fields, the same
`omitempty` shape on the wire. One test per message decodes the literal JSON
the Go side writes, so the two cannot drift silently.

Everything the app sends is what `web/voice/app.js` sends, in the same
order:

- **Mute.** On: stop the microphone, *then* send `{"type":"mute","on":true}`.
  Off: send `{"type":"mute","on":false}` *first*, then resume. Muted means
  no binary frames at all, never zero-filled ones.
- **Silence.** The gate's transitions are announced with
  `{"type":"silence","on":…}` before the frames they affect.
- **Ping.** The contract's JSON `ping` every 15 s carrying `gated_frames` and
  `peak` (not `URLSessionWebSocketTask.sendPing`, which the daemon does not
  see); no `pong` within 10 s drops the socket into the reconnect path.
- **On every (re)connection** the mute and gate state are re-sent, because a
  reconnecting client starts unmuted and ungated on the daemon.
- **`flush`** empties the playback queue and closes the open reply bubble.
  Stop does the same locally and sends `cancel`.

Auth is `Authorization: Bearer <token>` on every request **including the
WebSocket upgrade** — `URLSession` can set a header where a browser cannot,
so the `?token=` query form is never used and the token never lands in a
URL. Before opening `/ws` the app probes `GET /api/state`, where a 401 is a
plain status code; a refused upgrade is far harder to read back.

## Events

`GET /events` is the app's second input: agent state changes (it refetches
`/api/state`, debounced), `voice.push` (a system card and a haptic), and
`voice.tool_call` / `voice.tool_result` (the orb's *thinking* state, with a
30 s timeout so a missed result cannot stick).

Against a daemon running `--voice=off` there are no transcript frames at
all, so the conversation is mirrored from the event log instead:
`web.utterance` becomes the shepherd's bubble and `voice.tool_result`'s
`spoken` becomes the dog's. In a normal Gemini session the WebSocket
transcripts are the only source and no mirroring happens, so nothing is
duplicated.

Two things about `/events` that cost a day to find, and that any other
client of this daemon will hit:

1. **Foundation's `AsyncLineSequence` drops empty lines.** An empty line is
   exactly what terminates one SSE record, so `for try await line in
   bytes.lines` yields every field and never completes an event. The client
   reads raw bytes and splits on `\n` itself.
2. **The stream needs its own `URLSession`.** `timeoutIntervalForRequest` is
   an inactivity timeout; with the 8 s value that suits the JSON endpoints,
   the stream dies between the daemon's 15 s keepalives and reconnects
   forever. The stream session allows 60 s.

## Audio

```
microphone ─▶ AVAudioEngine input tap (hardware format, ~4 096 frames)
           ─▶ copy off the render thread ─▶ main actor
           ─▶ PCMConverter  (AVAudioConverter: anti-alias + rate change → 16 kHz mono Int16)
           ─▶ FrameChunker  (640 samples = 40 ms = 1 280 bytes)
           ─▶ SilenceGate   (peak < 8 LSB for 300 ms closes; 100 ms pre-roll on reopen)
           ─▶ VoiceSocket   (one binary frame each)

/ws binary ─▶ PlaybackQueue (150 ms jitter buffer, then chunks in order)
           ─▶ AVAudioPlayerNode.scheduleBuffer (no `at:`; the node concatenates)
           ─▶ 24 kHz mono Float32 ─▶ main mixer ─▶ speaker
```

The session is `.playAndRecord` with mode `.voiceChat` and options
`.allowBluetooth`, `.allowBluetoothA2DP`, `.defaultToSpeaker`. `.voiceChat`
is what turns on the system echo canceller: without it the dog's own voice
out of the speaker comes back up the microphone and it interrupts itself.
Interruptions (a call, Siri) pause the engine and flush playback; route
changes (AirPods in or out) rebuild the capture chain, because the hardware
sample rate changes underneath the tap.

The input tap runs on the audio render thread, so the buffer is copied there
and the conversion happens on the main actor, which owns the converter, the
chunker and the gate. One copy of ~85 ms of float samples, about twelve
times a second, is far cheaper than making that state thread-safe.

### Sample rates

| | Rate | Format |
|---|---|---|
| Microphone hardware | 48 000 Hz (44 100 on some devices) | Float32 mono |
| Up to the daemon | 16 000 Hz | PCM16 LE mono, 40 ms frames (1 280 B) |
| Down from the daemon | 24 000 Hz | PCM16 LE mono, arbitrary chunks |
| Player node | 24 000 Hz | Float32 mono (the engine resamples to the device) |

### Measured

On the iOS 26 simulator on an Apple-silicon Mac, over 117 tap buffers of
85.3 ms each (`AudioLatencyTests`):

- **Capture, convert, chunk and gate one tap buffer: mean 0.073 ms, worst
  0.171 ms.** The real-time budget for that buffer is 85.3 ms, so the
  conversion uses about 0.09 % of it. The sample-rate path is not the
  bottleneck and will not be on a device either.
- **Playback queue depth**, driven with the chunk sizes the model actually
  produces (40–730 ms): **730 ms worst case**, which is one long chunk — the
  queue adds a 150 ms jitter buffer on top of whatever the model sends and
  nothing more.

The same two numbers are live in the app under Settings ▸ About, and go to
`os_log` under the subsystem `com.kccarlos.sheepdog`, categories `audio`,
`socket` and `session`.

## The orb

`SessionState` is a pure function of eight inputs — socket life, voice
status and its reason, stalled, muted, gate closed, audio playing, a tool
call in flight — resolved in one ordered list of rules, most urgent first.
The views render it and nothing else, so the orb, the title and the buttons
can never disagree. `capturing` comes off the same value: the microphone
runs only in the states where something can hear it, which is why
`--voice=off` keeps it shut and says why.

## Concurrency

Swift 5 language mode, on purpose. The audio path (tap callbacks,
`AVAudioConverter` input blocks, `AVAudioPlayerNode` completion handlers) is
not `Sendable`-clean and making it so under Swift 6 strict concurrency would
have cost more than it bought for v1. The shape is still safe: the Kit types
are value types, the view models and the audio engine are `@MainActor`, and
the one real-time boundary is the explicit buffer copy in the tap.
