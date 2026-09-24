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
│                                               /api/pending/answer,            │
│                                               /api/conversation/reset)        │
└───────┼─────────────────────┼─────────────────────┼───────────────────────────┘
        │                     │                     │
┌───────┼─────────────────────┼─────────────────────┼─── SheepdogKit ───────────┐
│  PCMConverter          WSMessage             SSEParser                        │
│  FrameChunker          (wsproto mirror)      Hello / DaemonState / Agent      │
│  VoiceActivityDetector / CaptureGate         DaemonError                      │
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

## New conversation

The ⋯ menu's "New conversation" is `POST /api/conversation/reset
{"via":"app"}`, which forgets only the supervisor's and the voice model's
*short-term* context — the roster, open approvals, promises and notes, and
the scrollback itself all stay, so it is a much smaller thing than *Settings
▸ Clear conversation on this phone* (`clearConversation()`, which drops the
local copy of the scrollback too). `DaemonClient.resetConversation` returns
`nil` on a 404, the `voiceProviders()` convention: `conversationResetSupported`
starts `true` (optimistic) and only a real 404 hides the action, learned per
daemon (`endpointChanged()` resets it when the daemon itself changes).

There is deliberately no optimistic local divider. The daemon logs the
reset (and assigns it its event id) before answering the POST, so by the
time the app acts on either the reply or the `conversation.reset` SSE event
that follows, `history.fold` or the next `/api/conversation` sync already
has an item with that id — the same id-based merge every other notice
(`daemon.start`, `pending.created`, …) already relies on to survive being
seen twice, live and then from history. The event's `reset_id` rides along
as a second key `fold` also dedupes by, for the same belt-and-suspenders
reason `pending.*` events use their pending id rather than trusting event
ids alone. `ConversationReducer.apply(.reset)` only closes whatever bubble
was still open (the in-flight turn is stale, not worth continuing) — it adds
nothing itself, since the divider is the notice item, not a reducer bubble.

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
           ─▶ CaptureGate   (VoiceActivityDetector by default; hold-to-talk override;
                             the old SilenceGate behind a diagnostics toggle)
           ─▶ VoiceSocket   (one binary frame each)

/ws binary ─▶ PlaybackQueue (adaptive jitter buffer, then chunks in order;
                             owns the time-stretch rate policy)
           ─▶ AVAudioPlayerNode.scheduleBuffer (no `at:`; the node concatenates)
           ─▶ AVAudioUnitTimePitch (pitch 0, rate 0.85–1.05, set every 20 ms)
           ─▶ 24 kHz mono Float32 ─▶ main mixer ─▶ speaker
```

### The adaptive jitter buffer

`PlaybackQueue` used to hold a fixed 150 ms jitter buffer. Bursty arrival —
especially from the GPT-Live provider, which front-loads a reply and then
sends slower than real time — could still run the player dry mid-sentence:
audible gaps, sometimes mid-word. The buffer's target is now adaptive:

- It starts at 150 ms and tracks its own **playhead** (when the audio
  already handed to the player should run out), computed from chunk
  durations rather than from `AVAudioPlayerNode`'s completion callbacks,
  which land late and jittery on iOS and made a poor drain detector.
  `completed(_:)` still exists, for the queue depth and `isSpeaking`.
- A drain is an **underrun** — a gap the user actually heard — only if more
  audio of the *same* utterance arrives within 300 ms of it. Each underrun
  raises the target by 100 ms, capped at 500 ms, and the raised target is
  kept for the rest of the session.
- After an underrun the queue **refills before resuming**: it holds new
  chunks until the (now higher) target is buffered again, rather than
  scheduling the first one alone and risking another gap right behind it.
- A clean utterance (no underrun) decays the target 20 ms, down to the
  150 ms floor. A `flush` is a reset, not a drain: it does not move the
  target either way.
- The first chunk of a post-underrun refill gets an 8 ms linear fade-in, so
  the resumption doesn't click. There is deliberately **no matching
  fade-out** at the drain side: by the time the queue learns a chunk was
  the last one, that PCM is already with the player (or already played),
  so there is nothing left to fade without a lookahead buffer, which is out
  of scope here. The browser page hits the identical constraint with
  `AudioBufferSourceNode`.
- `AudioMetrics` and Settings ▸ About report `underruns`, the current
  target, `drains` (every time the node ran dry, underrun or not — the
  ordinary silence between utterances is a drain too) and the largest
  arrival gap seen while one was worth caring about. A second line shows
  the time-stretch: current rate, target, underruns and stretched seconds.
- The 15 s `ping` carries, as the browser page's does: `underruns`,
  `drains`, `buffer_ms` (audio buffered **now** — it used to be the target,
  which the browser never sent), `rate` (current) and `stretched_ms`
  (total wall-clock time played at a rate other than 1.0). The daemon
  mirrors them into `web.client.stats`; older daemons ignore what they do
  not know. `drains` used to read 0 there because the app never sent it.

### Adaptive time-stretch

GPT-Live delivers its audio at **0.72–0.86× real time** (the daemon's
`docs/audio-e2e-results.md`, and the phone: 84 underruns in 8 minutes,
the buffer at its 500 ms cap from the first reply on). No buffer that
starts playing within a second can absorb a source that is short for the
whole reply, so the player also **plays slower when the buffer runs low** —
pitch-preserving, through an `AVAudioUnitTimePitch` between the player node
and the mixer. The policy is pure (`PlaybackRatePolicy`, owned by
`PlaybackQueue`) and identical to the browser page's:

- **Target T per provider** (`PlaybackProfile`, from `/ws`
  `status.provider`): Gemini starts at 150 ms, cap 500 ms (as before);
  GPT-Live (`openai`) starts at 800 ms, cap 2 000 ms. The underrun step
  (+100 ms), decay (−20 ms per clean reply) and refill-before-resume are
  unchanged on top. The "start anyway" wait for a short reply scales with
  the target — T + 250 ms, as in the browser — so an 800 ms target is not
  cut short by the old fixed 400 ms.
- **Rate r from buffered audio b**: 1.0 when b ≥ T; below T it falls
  linearly to 0.90 at b = T/2 and to the **0.85 floor** at b ≤ T/4; 1.05
  when b > 2T, to catch up. The rate moves at most **0.02 per 20 ms**, so
  every change is a glide.
- **Catch-up needs latency debt.** Playing at r < 1 adds (1 − r)·dt of
  latency; only that debt is won back at 1.05. Gemini is ahead of real
  time — a 10 s reply lands in ~2.5 s, so b sits far above 2T for most of
  every reply — and would otherwise play everything 5 % fast for no reason.
  The debt resets when a reply ends. (The spec's rule read literally
  contradicts "Gemini stays at 1.0"; this is the reconciliation.)
- **The playhead runs at 1/r.** The queue's drain prediction is kept in
  wall-clock time and rescaled on every rate change (the audio left,
  (end − now)·r, lasts that divided by the new rate), and chunks appended
  while stretched extend it by duration/r. `bufferedMilliseconds(at:)` —
  the policy's b and the ping's `buffer_ms` — is audio, not wall time.
- `AudioEngine` runs the 20 ms control loop (a common-mode `Timer`) while
  the engine runs; a flush or Stop resets the rate to 1.0.

Replays (`TimeStretchReplayTests`, five seeds × 12 replies each; a
simulated node that plays at the rate the queue asked for and reports
completions 30 ms late). *Before* is the old fixed-rate buffer (150–500 ms
for every provider); *after* is time-stretch plus the provider targets:

| Timeline | before | targets only | after | time at r = 1.0 |
|---|---|---|---|---|
| GPT-Live, the two recorded turns (0.69×, 0.78×) | 45 underruns, 56.8 s of gaps | 25, 32.4 s | **15, 17.9 s** | 2 % |
| GPT-Live 0.85×, 2–6 s replies, 0.3–1.5 s stalls every 2–4 s | 31, 48.4 s | 20, 28.7 s | **15, 17.7 s** | 25 % |
| GPT-Live 0.75×, same shape | 47, 78.0 s | 21, 69.6 s | **19, 51.0 s** | 7 % |
| GPT-Live 0.75× with a 600 ms fast start | 95, 111.8 s | 30, 98.7 s | **27, 83.9 s** | 2 % |
| GPT-Live 0.75×, 5–12 s monologues (stress) | 66, 166.3 s | 45, 153.7 s | **37, 115.8 s** | 6 % |
| GPT-Live 0.85×, 5–12 s monologues (stress) | 47, 96.6 s | 38, 86.9 s | **27, 49.5 s** | 23 % |
| Gemini 4× | 0 | 0 | **0** | 94 % |

Underruns fall to a third on the recorded turns and gaps to a third, but
**not to zero**. The reason is arithmetic: both recorded turns arrive
below the 0.85 floor, so even fully stretched the player consumes faster
than GPT-Live sends; an 800 ms start runs out near the end of a 3 s turn.
The target then settles where +100 ms per underrun balances −20 ms per
clean reply: about one reply in five still has a gap. Offline experiments
(not committed) showed what would close it — starting GPT-Live at
**1 200 ms** gave 0 underruns on the recorded turns and 1 on the 0.85×
timeline; no decay for GPT-Live, or a 0.75 rate floor, each helped less.
Those change the shared policy, so they are left for the daemon/browser
side to agree on. For Gemini, what is not 1.0 is mostly the last ~150 ms of
each reply (the tail always runs below the target once the final chunk is
in) and brief dips when two slow chunks meet.

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

### Voice detection (what gets sent)

The browser's gate — peak below 8 LSB (−72 dBFS) for 300 ms — was ported
first, and on a phone it almost never closed: Chrome runs its own noise
suppression in front of that gate, iOS did not, so room noise streamed to
the model continuously and came back as words ("fishing", "Discord"). It
is replaced by `VoiceActivityDetector` (`Sources/SheepdogKit`), per 40 ms
frame:

1. **Level in the speech band.** The frame goes through a 2nd-order
   high-pass at 300 Hz and low-pass at 3.4 kHz (fans, HVAC, handling rumble
   and hiss are mostly outside), is cut into four 10 ms sub-blocks, and its
   level is the **second-quietest** sub-block's energy in dBFS — sustained
   speech barely moves, a keyboard click (one or two sub-blocks) vanishes.
2. **Adaptive noise floor**: the 15th percentile of those levels over the
   last 4 s, never below −72 dBFS. The pauses between words set it, not the
   words; it follows a louder room up within the window (~3.5 s) and a
   quieter one down within about a second.
3. **Adaptive margin**: the gate opens at floor + 6…10 dB. The margin is
   `4 + 2 × spread`, where spread is the 15th minus the 2nd percentile of
   the same history (speech never reaches down there): a steady fan gets
   6 dB, a room of other voices ~10.
4. **Attack and hangover**: open once 120 ms (3 frames) of the last 280 ms
   are above the margin — a window, not a run, because speech dips between
   syllables; close after 400 ms below the margin minus 3 dB of hysteresis.
5. **Pre-roll**: on opening, the attack window plus 100 ms of withheld audio
   is sent ahead of the opening frame (380 ms), so the first syllable — and
   an initial "s" the band filter does not count — is never clipped.

The `/ws` contract is unchanged: `{"type":"silence","on":…}` on every
transition, frames only while open. `CaptureGate` wraps the detector, the
old `SilenceGate` (Settings ▸ Voice detection ▸ *Use the old peak gate*, for
comparison) and hold-to-talk's manual override, and emits a transition
exactly when its visible state changes, so switching mid-session never
leaves the daemon out of step. All tunables are in `VADTuning`.

**Tested with macOS text-to-speech.** `scripts/make-fixtures.sh` renders six
voices (Samantha, Daniel, Karen, Moira, Fred, Rishi) with `say` and
`afconvert` to 16 kHz mono PCM16 (508 KB, committed). The tests generate
white, pink and brown noise, **babble** (the other five voices at once) and
a **keyboard** (3–8 ms bursts 80–350 ms apart), mix them under two
utterances at 20/10/5 dB SNR (speech at −26 dBFS active level), and score
three voice pairs per cell. Latency is from the first frame where the voice
is 6 dB above the noise in the speech band; coverage is the share of
frames where the voice is above the noise (full band) that reached the
socket.

| Noise | SNR | Open latency, median / worst | Coverage (worst) | Opens on the noise between utterances |
|---|---|---|---|---|
| white | 20 dB | 120 / 120 ms | 100 % | 0 |
| white | 10 dB | 120 / 160 ms | 100 % | 0 |
| white | 5 dB | 120 / 160 ms | 100 % | 0 |
| pink | 20 dB | 120 / 160 ms | 100 % | 0 |
| pink | 10 dB | 160 / 200 ms | 100 % | 0 |
| pink | 5 dB | 120 / 160 ms | 100 % | 0 |
| brown | 20 dB | 160 / 200 ms | 100 % | 0 |
| brown | 10 dB | 120 / 120 ms | 100 % | 0 |
| brown | 5 dB | 120 / 120 ms | 100 % | 0 |
| babble | 20 dB | 120 / 160 ms | 100 % | 0 |
| babble | 10 dB | 120 / 160 ms | 100 % | 0 |
| babble | 5 dB | 160 / **1 120 ms** | **29 %** | 0 |
| clicks | 20 dB | 160 / 200 ms | 100 % | 0 |
| clicks | 10 dB | 160 / 200 ms | 100 % | 0 |
| clicks | 5 dB | 160 / 200 ms | 100 % | 0 |

Noise alone, 60 s of each kind at −31, −45 and −60 dBFS: **0 opens** in
every one of the 15 runs (the brief's limit was 1 per 10 s). Loud clicks at
−20 dBFS: 0. The one weak cell is **5 dB babble** — one voice barely above a
room of others — where Fred's "Stop." is lost and the gate opens on the
next words; it is asserted separately (at most two of six utterances late,
mean coverage ≥ 80 %). Voice Isolation (below) is the answer for that room.
The same room noise at −50 dBFS went through the old peak gate **100 %**
and through the detector **0 %** (`testTheOldPeakGateStreamedTheRoomAndTheDetectorDoesNot`).

**Voice Isolation.** iOS's microphone modes (Standard, Voice Isolation,
Wide Spectrum) apply before the app sees a sample, to apps using voice
processing. Settings ▸ Microphone mode shows `AVCaptureDevice.activeMicrophoneMode`
and opens the system panel (`showSystemUserInterface(.microphoneModes)`);
a one-time tip suggests it after the first listening session.

**No microphone signal.** `MicSignalWatchdog`: unmuted, capturing, and every
sample digital zero (or no buffers at all) for 3 s means the capture path is
broken, not that the room is quiet — a real room is never exactly zero. The
status line says "No microphone signal — check the mic", the mic button gets
an amber badge, the engine logs the input format, voice-processing state and
both input mutes, and rebuilds the capture chain once. At start the engine
also clears the voice-processing unit's own input mute and iOS 17's app-wide
input mute (AirPods' press-to-mute drives it) — either left on gives exactly
the 25 s of zeros seen on the phone on 2026-09-22 — and a multi-channel
input is downmixed rather than reduced to its first channel.

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
  queue adds a jitter buffer (150–500 ms, adaptive — see above) on top of
  whatever the model sends and nothing more.

The same two numbers are live in the app under Settings ▸ About, and go to
`os_log` under the subsystem `com.kccarlos.sheepdog`, categories `audio`,
`socket` and `session`.

## The voice bar's words

`VoiceBarModel` also owns what the bar *says*. The mic button is never a
bare microphone: live, it shows the input level as a ring and reads "Mute
microphone"; muted, it is a red slashed mic reading "Unmute microphone",
and the status line says "Muted — tap to unmute". Connected and unmuted the
status line says "Listening — just talk", and the first time that happens a
coach mark says "Just talk — Sheepdog is listening. Tap the mic only to
mute." (People had been muting to talk.) **Hold to talk** (Settings,
off by default) turns the orb into a walkie-talkie — press opens the gate
with its pre-roll, release closes it, pressing while the dog talks stops
it — and hides Mute.

## The voice provider

`GET /api/voice/providers` lists what the daemon can run the live session on
(Gemini Live, GPT-Live); `PUT /api/voice/provider {"provider":…}` switches.
Settings ▸ Voice lists them, unavailable ones disabled with the daemon's
reason, GPT-Live marked "Billed per minute". `/api/hello`, `/api/state`
(`voice_provider`, `voice_model`) and the `/ws` `status` (`provider`) are
decoded as optional, and a 404 on the list hides the section, so the app
works unchanged against an older daemon. The connection sheet shows the
provider and model.

## The orb

`SessionState` is a pure function of eight inputs — socket life, voice
status and its reason, stalled, muted, gate closed, audio playing, a tool
call in flight — resolved in one ordered list of rules, most urgent first.
The views render it and nothing else, so the orb, the title and the buttons
can never disagree.

*Speaking* is held for **800 ms** across playback gaps (`SpeakingHold`):
the user saw the orb flick between speaking and muted while GPT-Live
underran, because speaking outranks muted and every gap dropped it for a
moment. A flush or Stop drops the hold at once. Mute is not an orb state
while the dog is speaking or thinking: the orb keeps that state and wears a
small red slashed-mic badge (`SessionState.showsMuteBadge`); the mute
button stays the source of truth for mute. `capturing` comes off the same value: the microphone
runs only in the states where something can hear it, which is why
`--voice=off` keeps it shut and says why.

## Concurrency

Swift 5 language mode, on purpose. The audio path (tap callbacks,
`AVAudioConverter` input blocks, `AVAudioPlayerNode` completion handlers) is
not `Sendable`-clean and making it so under Swift 6 strict concurrency would
have cost more than it bought for v1. The shape is still safe: the Kit types
are value types, the view models and the audio engine are `@MainActor`, and
the one real-time boundary is the explicit buffer copy in the tap.
