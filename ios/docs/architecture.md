# Architecture

Two targets, one rule: anything that can be tested without a microphone
lives in `SheepdogKit`, and the app target is SwiftUI views plus
`@MainActor` view models over it.

```
┌──────────────────────────── Sheepdog (app target) ────────────────────────────┐
│  MainView ── AgentStrip ── ConversationView ── ApprovalCardView ── ControlBar  │
│  SettingsView                                 (or PresenceOrb)                │
│        │                                                                      │
│  SessionViewModel (@MainActor): the only place that decides anything          │
│        │                     │                    │                           │
│  AudioEngine            VoiceSocket          DaemonClient                     │
│  (AVAudioEngine,        (/ws)                (/api/hello, /api/state,         │
│   AVAudioSession)                             /api/login, /events,            │
│                                               /api/conversation,              │
│                                               /api/pending/answer)            │
└───────┼─────────────────────┼─────────────────────┼───────────────────────────┘
        │                     │                     │
┌───────┼─────────────────────┼─────────────────────┼─── SheepdogKit ───────────┐
│  PCMConverter          WSMessage             SSEParser                        │
│  FrameChunker          (wsproto mirror)      Hello / DaemonState / Agent      │
│  SilenceGate                                 DaemonError                      │
│  PlaybackQueue         ConversationReducer   SettingsStore / SecretStore      │
│  AudioMetrics          SessionState          ConversationHistory / Item       │
│                        ApprovalFlow          ConversationTimeline             │
│                                              ConversationCache (JSON file)    │
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

Every event that is a conversation item of its own — a typed utterance
(`web.utterance` not via voice, `voice.text`), a reply spoken without a
voice model (`voice.tool_result` with a `via` other than `voice`), a push
that is not silent, an `answer_prompt`'s `pending.created` and its
resolution, `daemon.start` and the voice session's reconnects — is folded
into `ConversationHistory` by the daemon's own rules
(`internal/web/conversation.go`), keyed by its event id. That is the id
`/api/conversation` gives the same item later, so the two merge instead of
showing twice. Transcript fragments are the exception: `/ws` delivers them
sooner, so they stay id-less bubbles in `ConversationReducer` until a
newest-page sync (2.5 s after a turn ends) replaces them with the server's
items.

**Resume.** The client tracks the last SSE `id:` and reconnects with
`Last-Event-ID` and `?since=` (never `?since=0`: a small number is a count
to the daemon). The id is kept in the cache, so a relaunch resumes too. An
event at or below the last id is dropped, and folding is idempotent anyway:
a replayed `pending.created` cannot reopen an approval that was answered
since. Haptics fire only for events less than a minute old, so a replay
after a long sleep is silent.

## History and the cache

`GET /api/conversation` (newest page, 200 items) is fetched at launch when
an address is configured — before, and without, Connect — on every
(re)open of the event stream, on returning to the foreground, and shortly
after each voice turn. The merge rule (`ConversationHistory.applyNewest`):
the server is the record for the id range its page covers, so a local
item in that range that the server does not have is dropped; live items
newer than the page survive it; older ones (the cache, earlier pages) are
kept unless the page says there is nothing older. Older pages come from
`before=<next_before>` and merge by id.

`ConversationCache` writes the newest 300 items, `next_before` and the last
event id to `Application Support/Conversations/<host>_<port>.json`, one
file per daemon, excluded from backups, debounced by a second and written
at once when the app goes to the background. The token is not in the file
or its name. The screen is `ConversationTimeline.rows`: history and live
bubbles merged by time, a date divider where the day changes, `notice`
items as dividers, and the push that repeats a new approval's words folded
into the approval card.

## Approvals

`ApprovalFlow` is the pinned card's state machine: *open* → *sending* (one
option; further taps ignored) → *answered* (the daemon's `spoken`) or
*gone* (409, "That question was already answered or has gone.") or back to
*open* with the error under the buttons. `pending.confirmed` from somewhere
else settles an open card as answered (by voice); `expired`, `cancelled`,
`replaced`, or a state refresh without the question dismisses it. Our own
`pending.confirmed` can arrive before the HTTP reply; the reply settles the
card. A settled card stays 3.5 s, then the orb comes back — or the next
question, if one is waiting. `/api/state`'s `pending_action` is the source
of truth (refreshed every 5 s and on every `pending.*` event); the event is
the fast path. Only `answer_prompt` actions with options get a card; the
POST sends the option's 1-based `index`.

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
