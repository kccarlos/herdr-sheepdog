# Architecture

Two targets, one rule: anything that can be tested without a microphone
lives in `SheepdogKit`, and the app target is SwiftUI views plus
`@MainActor` view models over it.

```
┌──────────────────────────── Sheepdog (app target) ────────────────────────────┐
│  MainView ── AgentStrip ── ConversationView ── ApprovalCardView ── ControlBar  │
│  SettingsView                                 (or PresenceOrb)                │
│        │                                                                      │
│  SessionViewModel (@MainActor): adapts SessionCore, runs its effects          │
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
│  AudioMetrics          SessionCore           ConversationHistory / Item       │
│                        SessionState          ReconnectPolicy / MuteSync       │
│                        ApprovalFlow          ConversationTimeline             │
│                                              ConversationCache (JSON file)    │
└───────────────────────────────────────────────────────────────────────────────┘
                                   │
                        sheepdog daemon on your Mac
```

## The daemon contract, mirrored

`WSMessage` is a direct mirror of the daemon's `internal/voice/wsproto`
package: the same twelve message types, the same required fields, the same
`omitempty` shape on the wire. One test per message decodes the literal JSON
the Go side writes, so the two cannot drift silently.

Everything the app sends is what `web/voice/app.js` sends, in the same
order:

- **Mute.** On: stop the microphone, *then* send `{"type":"mute","on":true}`.
  Off: send `{"type":"mute","on":false}` *first*, then resume. Muted means
  no binary frames at all, never zero-filled ones.
- **Silence.** The gate's transitions are announced with
  `{"type":"silence","on":…}` before the frames they affect.
- **Speaking.** The gate is the app's own speech detector, so its opening on
  speech (or a hold) is also `{"type":"speaking","on":true}` and its closing
  `on:false` — once per change, never while muted (muting closes it; the
  gate reset that muting reports is not speech) and never from the fake
  voice's simulated gate. `SessionCore` decides it next to `silence`. The
  daemon treats `speaking` as authoritative once it has seen one, and holds
  spoken pushes while it is open.
- **Playback reports.** `PlaybackQueue` reports `started` (a run began
  playing after being empty), `drained` (it ran dry and nothing came within
  the 300 ms underrun window — an underrun that resumes inside it is the
  same run: no `drained`, no second `started`) and `flushed` (every flush,
  the daemon's or Stop's, even with nothing playing: `played_ms: 0`), the
  way `web/voice/audio-util.js`'s `Player` does. `played_ms` is **stream
  time**: the run's received samples minus the ones still buffered, where
  the playhead's remaining audio is wall-clock time left × rate — so 1 s of
  audio stretched to 0.85× reports 1000, not 1178. `AudioEngine` hands each
  report straight to the socket (`onPlaybackReport`), like the frames.
- **Ping.** The contract's JSON `ping` every 5 s (the daemon's
  `web.client.stats` interval, as `/voice`) carrying `gated_frames`, `peak`,
  the playback counters and `audio_state` (`running` / `interrupted` /
  `suspended`) — not `URLSessionWebSocketTask.sendPing`, which the daemon
  does not see. A ping waits for its `pong` before the next one; still
  unanswered after 10 s, the socket drops into the reconnect path. (At the
  old 15 s every tick overwrote the send time, which only worked because
  15 > 10.)
- **On every (re)connection** the mute, gate and speaking state are re-sent,
  because a reconnecting client starts unmuted and ungated on the daemon,
  and so that `speaking` is authoritative from the first frame.
- **`flush`** empties the playback queue (reporting `flushed`) and closes
  the open reply bubble. Stop does the same locally and sends `cancel`; the
  daemon's answering `flush` reports a second `flushed` with `played_ms: 0`,
  as the browser page does.

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

## The Flock pane, brain replies and typed input

- **Flock.** The main screen's segmented control switches the middle of
  the screen between the conversation and the Flock pane; the voice bar
  stays put. Each agent's card mirrors `/api/state`'s `agents` (the same
  `Store.List()` as `/api/agents`), refreshed over `/events`:
  - its state and how long it has been in it (`state_since`);
  - the current or last task;
  - the open question: the approval card itself, answerable there, which
    then leaves the pinned spot above the controls. Otherwise the
    `last_block` question, marked answered when it was;
  - `last_result.spoken` with its `detail` behind a disclosure;
  - what the worker last wrote for a human: the newest `journal` entry's
    `last_assistant` (≤ 600 characters, read from the worker's own
    transcript), with the files it touched and its tool counts.

  `Agent` used to read `last_result.summary` and `last_block.at`/`options`,
  none of which the daemon sends, so the last result never showed. It now
  decodes `state.Result` (`spoken`, `detail`, `at`), `state.Block`
  (`question`, `seen_at`, `answered`) and `state.JournalEntry` as they are.
- **Brain replies** (`BrainActivity`). `voice.tool_result` for
  `to_supervisor` carries what the brain told the voice: `spoken`, and
  `detail`, `options`, `clarify` and `pending` when present. The brain's
  `ask` is not a separate field on the wire: the daemon folds it into
  `spoken` as the last sentence. With a live voice the reply is a purple
  "Brain → Sheepdog" card, with the detail and the raw payload behind a
  disclosure. With `--voice=off` the text router's reply is already the
  dog's bubble (same event id), so only a compact detail/options footer
  goes under it.
- **Held pushes.** `voice.push_held` shows as a dashed "Waiting for a
  listener" card. On `voice.push_released` it disappears, and the
  `voice.push` that follows is the ordinary push card. After
  `voice.push_dropped` it stays, struck through.
- These are kept apart from `ConversationHistory`, whose newest-page merge
  drops local items the server does not list. `/events` feeds them live.
  `GET /api/events?type=voice.tool_result` and `?type=voice.push_` (the
  daemon's current run) backfill them after a relaunch or a stream
  reconnect.
- **Typed input.** The keyboard button at the end of the status line (or
  ⋯ ▸ Type instead) opens a text field. Sending goes through
  `POST /api/inject/utterance`, which records the text as typed shepherd
  input before it reaches the voice or the text router, so the one rail
  takes it like a transcript. The words and the answer arrive in the
  conversation the usual way; a refusal (422 "nothing to relay", 502
  voice error) shows under the field. The ⋯ menu also has Stop, the same
  as the orb's stop, reachable whatever the orb is doing.

## The Health pane

The switch's third pane, "Health", replaces what the Mac's `/debug` page
was needed for during a test:

- **Daemon.** The connection, the address and the version. Uptime is
  measured from the newest `daemon.start` (`GET /api/events?type=daemon.start`),
  or from the history's "Sheepdog started" divider when the 2 000-event
  ring has rolled past it.
- **Voice.** Status, the provider picker (the same
  `GET/PUT /api/voice/provider` calls as Settings) and the model.
  GPT-Live's billed seconds come from `web.client.stats.voice_billed_seconds`
  and `voice.billed_seconds.total_s`, with the cost at $0.05/min.
- **Brain.** Provider and model pickers from `GET /api/brain/models`,
  switched with `PUT /api/brain/model`: a new provider loads its catalog
  and takes its first recommended model. The current model is always
  offered, even when the catalog does not list it. An unavailable provider
  shows the daemon's reason.
- **Pushes.** Chattiness levels from `GET /api/settings`, changed with
  `PUT /api/settings`. Hidden without a supervisor.
- **Audio on this phone.** The echo-cancellation row (as in Settings ▸
  About) and the playback counters.
- **Recent problems** (`DaemonHealth`). One line each, with its time, for:
  - `brain.fallback` (the reason) and `brain.rate_limited`;
  - `voice.connect_error`;
  - `voice.push_held` / `voice.push_dropped`;
  - `supervisor.echo_dropped` and `web.audio_unheard`;
  - `voice.playback` with `cut`;
  - `worker.session_missing`.

  The raw event is behind a tap. The pane fills from
  `GET /api/events?type=…` (one small request per prefix, since the filter
  takes one) when it opens or is pulled; `/events` keeps it current. The
  switch reads "Health · n" while there are non-informational problems
  from the last 15 minutes.

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
- The 5 s `ping` carries, as the browser page's does: `underruns`,
  `drains`, `buffer_ms` (audio buffered **now** — it used to be the target,
  which the browser never sent), `rate` (current) and `stretched_ms`
  (total wall-clock time played at a rate other than 1.0). The daemon
  mirrors them into `web.client.stats`; older daemons ignore what they do
  not know. `drains` used to read 0 there because the app never sent it.

### Adaptive time-stretch

GPT-Live delivered its audio at **0.72–0.86× real time** (the daemon's
`docs/audio-e2e-results.md`, and the phone: 84 underruns in 8 minutes,
the buffer at its 500 ms cap from the first reply on) until the daemon
paced its input at real time (2026-09-25); it now runs at ≈1.0×, but a
source that falls behind can still run the player dry mid-sentence, so the
player also **plays slower when the buffer runs low** —
pitch-preserving, through an `AVAudioUnitTimePitch` between the player node
and the mixer. The policy is pure (`PlaybackRatePolicy`, owned by
`PlaybackQueue`) and identical to the browser page's:

- **Target T per provider** (`PlaybackProfile`, from `/ws`
  `status.provider`): Gemini starts at 150 ms, cap 500 ms (as before);
  GPT-Live (`openai`) starts at 400 ms, cap 2 000 ms (raised to 1 200 ms on
  2026-09-24 while GPT-Live's input ran slow, then lowered back to 400 ms
  on 2026-09-25 once the daemon started pacing that input at real time).
  The underrun step (+100 ms), decay (−20 ms per clean reply) and
  refill-before-resume are unchanged on top. The "start anyway" wait for a
  short reply scales with the target — T + 250 ms, as in the browser.
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
for every provider); *after* is time-stretch plus the provider targets. The
GPT-Live rows below are pinned to the **1 200 ms** target GPT-Live used
before the daemon paced its input at real time (2026-09-25) — the decision
this table backs — not to the current 400 ms `.openAI` default; see below
the table for the real, paced measurement:

| Timeline | before | targets only | after | time at r = 1.0 |
|---|---|---|---|---|
| GPT-Live, the two recorded turns (0.69×, 0.78×) | 45 underruns, 56.8 s of gaps | 5, 7.8 s | **0, 0.0 s** | 0 % |
| GPT-Live 0.85×, 2–6 s replies, 0.3–1.5 s stalls every 2–4 s | 31, 48.4 s | 6, 9.3 s | **1, 1.1 s** | 15 % |
| GPT-Live 0.75×, same shape | 47, 78.0 s | 22, 45.9 s | **9, 21.0 s** | 4 % |
| GPT-Live 0.75× with a 600 ms fast start | 95, 111.8 s | 27, 79.0 s | **22, 52.9 s** | 2 % |
| GPT-Live 0.75×, 5–12 s monologues (stress) | 66, 166.3 s | 39, 141.0 s | **30, 94.4 s** | 5 % |
| GPT-Live 0.85×, 5–12 s monologues (stress) | 47, 96.6 s | 27, 72.7 s | **17, 31.1 s** | 15 % |
| Gemini 4× | 0 | 0 | **0** | 94 % |

Underruns and gaps both fall sharply from *before* through *targets only*
to *after* on every row, and on the two recorded turns they reach **zero**:
GPT-Live's 1 200 ms provider target alone (*targets only*) is already close
to the 800 ms + 250 ms wait those turns need, and the stretch finishes the
job. The synthetic 2–6 s and 5–12 s timelines — deliberately harder than
the recording, with stalls up to 1.5 s — do not reach zero: both run below
the 0.85 rate floor for stretches long enough that even fully stretched
the player consumes faster than the source sends, so the target keeps
finding a new underrun to grow from. Where it settles balances +100 ms per
underrun against −20 ms per clean reply — from about one reply in two
(0.75×, 5–12 s) to about one in sixty (0.85×, 2–6 s). Offline experiments
(not committed) showed what would close the remaining gap further —
starting GPT-Live at 1 500 ms, or a 0.75 rate floor — each at the cost of
more silence before a short reply starts. Those change the shared policy,
so they are left for the daemon/browser
side to agree on. For Gemini, what is not 1.0 is mostly the last ~150 ms of
each reply (the tail always runs below the target once the final chunk is
in) and brief dips when two slow chunks meet.

**Start buffer lowered to 400 ms (2026-09-25).** The daemon now paces
GPT-Live's input at real time (`docs/audio-e2e-results.md`, the daemon
repo): one billed run measured `realtime_ratio` 0.94–1.06, against
0.74–0.87 before. `PlaybackProfile.openAI` follows: 400 ms start, the
2 000 ms cap and the time-stretch unchanged.
`testPacedGPTLiveLongReplyPlaysThroughTheFourHundredMillisecondStartBuffer`
replays that run's real `/ws` arrival timeline (`Timelines.pacedLongReplyMs`,
mirroring the daemon's committed
`web/voice/testdata/gptlive-paced-long-reply.json`) through the 400 ms
profile: at most one underrun, no more than the old 1 200 ms profile sees
on the same timeline (both catch the same 869 ms hole the daemon's
`PauseHold` leaves around a mid-count pause — not a gap inside a word),
and zero from the point the reply becomes continuous. The older, recorded
pre-pacing timelines above keep replaying under the 1 200 ms profile they
were tuned for (`TimeStretchReplayTests.prepacingProfile`), not `.openAI`.

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
transition, frames only while open. `CaptureGate` wraps the detector and
hold-to-talk's manual override, and emits a transition exactly when its
visible state changes, so switching mid-session never leaves the daemon out
of step. (The ported peak gate, `SilenceGate`, is gone: it only ever served
as a comparison and it processed every frame.) All tunables are in `VADTuning`.

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
and through the detector **0 %** (measured before the peak gate was removed).

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
`os_log` under the subsystem `com.github.kccarlos.herdr-sheepdog-ios`, categories `audio`,
`socket` and `session`.

## The LiveKit transport (optional)

Settings ▸ Voice transport picks **WebSocket** (the default, everything
above) or **LiveKit**: WebRTC through
[sheepdog-bridge](https://github.com/kccarlos/herdr-sheepdog-bridge), whose
`docs/client-contract.md` is the spec. The goal is fewer dropouts, lower
latency and surviving Wi-Fi ↔ cellular (Opus over UDP, NetEQ, ICE restarts)
without touching the daemon. The WebSocket path is unchanged and stays the
fallback.

```
SessionViewModel ── VoiceTransport (SheepdogKit protocol)
                      ├─ VoiceSocket        (Kit)  /ws, PCM + JSON
                      └─ LiveKitTransport   (app)  LiveKit Swift SDK 2.17.0
                              │ GET {bridge}/livekit/token  (bearer = daemon token)
                              │ Room.connect, publish mic, subscribe sheepdog-voice
                              └ data topic sheepdog.control  (reliable, ws-contract JSON)
```

- **The seam.** `VoiceTransport` is connect/disconnect, `send(WSMessage)`,
  the replay-on-open state (`muted`, `gateClosed`, `speaking`) and
  `VoiceTransportEvent` (`opened`, `control`, `audio`, `closed`, `failed`,
  and LiveKit's `link`). `VoiceSocket.Event` is now a typealias for it;
  nothing else in the socket changed. Only the app target links LiveKit;
  the pure half (`LiveKitBridge`: bridge address, token request/response,
  `ControlPacket` decoding of daemon frames and the bridge's
  `{"type":"bridge","daemon":…}`, the outgoing filter) is in Kit and unit
  tested. `SessionCore` is unchanged and drives both.
- **Audio is LiveKit's.** `AudioEngine` does not start: no capture tap, VAD,
  jitter buffer, time-stretch or playback reports. On this path the view
  model maps the core's audio effects onto the microphone track: it is
  live while the session's audio would run, unmuted, not paused in the
  background, and (hold-to-talk) held. Mute sends `mute` and mutes the
  track (the core mutes the source first, then sends — the bridge drops
  frames while muted, so either order is safe). LiveKit's `AudioManager`
  owns the session with voice-processing I/O (echo cancellation). The orb's
  "Speaking" comes from the bridge participant's active-speaker state.
- **What the app does not send.** `playback` (the bridge owns the reports)
  and `speaking` (one would switch off the bridge's active-speaker
  detection for the session). In open mic the app never sends `silence`
  either, so the bridge runs the contract's gate; hold-to-talk sends
  `silence` from the hold (the hold is the gate), which makes the app's
  `silence` authoritative for that bridge session.
- **Open / closed.** `opened` once the room is joined and the bridge has the
  daemon (its `bridge` message says `connected`, or any daemon message
  arrives). `closed(retrying)` when the bridge loses the daemon or leaves,
  when LiveKit starts its own reconnect (then, after it, the last `status`
  is replayed, since the bridge's `/ws` stayed up and the daemon sends no
  new one), and when the room drops for good (then a fresh token and a
  rejoin with the socket's 1–10 s backoff). A refused bearer (401) ends the
  session as "token refused". `bridge: replaced` (the browser page has the
  daemon) shows a "Take it back" banner; rejoining reclaims it. Health shows
  the transport, the bridge address and its link to the daemon.
- **Joining.** The identity is stable per device (`phone-` and 12 hex
  digits of a SHA-256 of `identifierForVendor`), as the contract
  recommends: a rejoin is a new participant SID under the same identity,
  which the bridge treats as a join, and no ghost is left in the room.
  Once connected, as soon as the bridge's `sheepdog-voice` is subscribed
  (the downlink is up; at most 2 s), the app says `mute` (and a closed
  gate): that is
  the control message the contract asks for, which the bridge answers with
  its link state and the daemon's latest `status` (a join-time message
  alone can be lost), and it restores the phone state the bridge forgets
  when the last phone leaves. If neither has arrived 8 s after joining,
  the transport reports a retrying failure so the reconnect banner can
  appear.

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

## The session core

`SessionCore` (Kit) is every decision the voice session makes, as a value:
connecting and reconnecting, who owns mute, when the audio engine runs,
hold-to-talk, playback, the thinking timeout, the background pause and the
"paused by iOS" state. `SessionViewModel` only adapts. Each callback — a tap,
the `/ws` socket, the `/events` stream, the audio engine, the scene phase,
the UI ticker — becomes a `SessionEvent`; `apply(_:now:)` updates the core
and returns `[SessionEffect]`; the view model runs the effects in order,
copies `socketMuted`/`socketGateClosed` onto `VoiceSocket` (which says them
on every open), and publishes what the core derives. An effect may call
back synchronously (`AudioEngine.setMuted` reports the gate), which just
feeds the next event: `apply` has returned by then. `SessionCoreTests`
pins the behaviour as event sequences with the exact effects.

The core's fields, by concern:

| Group | Fields |
|---|---|
| `connection` | `wantsConnection`, `socket` and `events` (`SocketLife`), `failed`, `tokenRefused`, `reconnect` (`ReconnectPolicy`), `muteSync` (`MuteSync`), `socketMuted`, `socketGateClosed` |
| `voice` | `status`, `closedReason`, `stalled`, `provider`, `saidVoiceIsOff` |
| `mic` | `talkMode`, `muted` (the user's intent), `holding`, `gateClosed`, `noSignal` |
| `playback` | `speaking`, `hold` (`SpeakingHold`), `heldSpeaking` |
| `thinkingExpiry` | a tool call's 30 s timeout |
| `suspension` | `pausedByIOS`, `backgroundSuspended`, `keepListeningWhenLocked` |

Derived, as pure computed properties: `sessionState` (the orb),
`connectionChip`, `voiceBar` (`VoiceBarModel`), `statusLine`, `daemonMute`,
`acceptsPlayback` (not while paused in the background) and `playbackBusy`
(the ticker's fast cadence).

**Events.** The user: `connectTapped`, `endTapped`, `muteTapped`,
`holdBegan`, `holdEnded`, `talkModeChanged`, `resumeTapped`, `stopTapped`,
`keepListeningLockedChanged`, `providerSelected`. Connecting:
`probeSucceeded(micGranted:)`, `probeFailed`, `tokenRefused` (a 401 on
`/events`), `eventsChanged(SocketLife)`. The socket: `socketOpened`,
`socketClosed(reason:willRetry:)`, `socketFailed(_:willRetry:)`, `status`,
`flush`, `daemonError`. The engine: `gateChanged`, `simulatedGate` (the
fake-voice debug mode), `audioStarted(gateClosed:)`, `audioStartFailed`,
`playbackChanged`, `micSignal(ok:)`, `interruption(paused:)`,
`audioResumeFailed`. `/events`: `toolCall`, `toolResult`. The app:
`scenePhase(active|background)`, `tick`.

**Effects.** `probe` (the `/api/state` auth probe, then the microphone
permission), `openSocket`, `closeSocket`, `startStreams`/`stopStreams`
(`/events` and the 5 s state refresh), `send(WSMessage)`, `startAudio`
(unless the engine runs), `stopAudio`, `suspendAudio`, `resumeAudio`,
`setCaptureMuted`, `setManualGate`, `flushPlayback`, `flushTranscript`,
`startTicker`/`stopTicker`, `showBanner(SessionBanner)`/`clearBanner`,
`notice`, `haptic`, `openSettings`, `refreshState` (debounced),
`syncHistory` (2.5 s later), `log`.

The connection, as a state diagram (`connection.socket`, with
`wantsConnection`):

```
            connectTapped                      probeSucceeded
  idle ───────────────────▶ connecting ─────────────────────────▶ connecting
   ▲  ▲   [clearBanner, probe]   │        [openSocket, startStreams,  (socket)
   │  │                          │         startTicker]                 │
   │  │ probeFailed / 401        │                                      │ socketOpened
   │  └──────────────────────────┘                                      ▼ [clearBanner, haptic]
   │     [showBanner, (openSettings), closeSocket, stopAudio,  ┌────── open ◀──────────┐
   │      stopStreams, stopTicker]                              │        │              │
   │                                                            │        │ socketClosed │ socketOpened
   │  endTapped (from anywhere): [closeSocket, stopAudio,       │        │ willRetry    │
   │   stopStreams, stopTicker, (setManualGate(false))]         │        ▼ [stopAudio]  │
   │                                                            │   reconnecting ───────┘
   │  socketClosed !willRetry: [stopAudio, stopStreams,         │        │ socketFailed: quiet for 30 s
   └── stopTicker, (showBanner(.replaced))] ◀───────────────────┘        │ [log], then one banner
                                                                         │ [showBanner(stillReconnecting)],
      socketFailed 401 or !willRetry ─▶ idle (as probeFailed)            │ then quiet until End
```

Once open, the voice and the microphone:

```
  status(connected|connecting…) ─▶ [startAudio] ─▶ audioStarted(gateClosed) ─▶ [send(silence) if it differs]
  status(closed)                ─▶ [stopAudio, notice once per session]
  status(muted ≠ the app's)     ─▶ [log once, send(mute(app's))]      (never adopted)
  muteTapped (open mic)         ─▶ mute:   [setCaptureMuted(true), send(mute(true)), haptic]
                                   unmute: [send(mute(false)), setCaptureMuted(false), startAudio?, haptic]
  talkModeChanged(hold)         ─▶ unmute first if muted, then [setManualGate(false)]; Mute is inert there
  holdBegan / holdEnded         ─▶ [stop… if the dog is busy, setManualGate(true), haptic] / [setManualGate(false)]
  scenePhase(background), lock toggle off ─▶ [suspendAudio, send(mute(true)), log]
  scenePhase(active) after that           ─▶ [send(mute(user's)), startAudio?, log]
  interruption(paused: true)    ─▶ orb "Audio paused by iOS"; resumeTapped ─▶ [resumeAudio]
  toolCall ─▶ thinking until toolResult [refreshState], stopTapped, End, or a tick 30 s later
  stopTapped ─▶ [flushPlayback, flushTranscript, send(cancel), haptic]; drops the speaking hold
  flush      ─▶ [flushPlayback, flushTranscript, syncHistory]; drops the speaking hold
```

## The orb

`SessionState` is a pure function of eight inputs — socket life (`SocketLife`,
shared with `ConnectionChip` and `VoiceSocket`'s own bookkeeping), voice
status, stalled, muted, gate closed, speaking, a tool call in flight, and
audio paused by iOS — resolved in one ordered list of rules, most urgent
first; `SessionCore.sessionState` feeds it from the core's fields. The views render it and nothing else, so the orb, the title and the
buttons can never disagree. (The `closed` status's `reason` string is not
one of the eight: it only feeds the notice text when voice goes off, never
the state machine itself.)

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
