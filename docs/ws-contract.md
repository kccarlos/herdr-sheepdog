# Browser ↔ daemon WebSocket contract

The Go types for every control message live in `internal/voice/wsproto`;
the WS server, the browser page and the iOS app are all built from this
document. Change the doc and the Go types together.

## Endpoint

- `GET /ws` on the daemon's HTTP port, upgraded to a WebSocket. Same origin as
  `/voice`. Public (no token needed) when `web.bind` is loopback and
  `web.token` is unset, the default; see "Auth" below for a non-loopback
  bind (a phone on the LAN or over Tailscale).
- **One client at a time.** A second connection replaces the first: the
  daemon sends the first client `{"type":"status","voice":"closed","reason":"replaced"}`
  and closes it with WebSocket code 1000, then serves the new one.
- The Gemini session lives in the daemon (decision D2). Opening, closing or
  replacing a browser connection never restarts it. A new client immediately
  receives one `status` message describing the current voice state and then
  continues the same conversation.

## Frame types

| Frame        | Direction        | Payload                                             |
|--------------|------------------|-----------------------------------------------------|
| binary       | client → server  | raw PCM16 little-endian, mono, **16 000 Hz**        |
| binary       | server → client  | raw PCM16 little-endian, mono, **24 000 Hz**        |
| text         | both             | one JSON object per frame, `{"type": …}`            |

There is no framing inside a binary frame: it is a contiguous run of samples
(`byte length` must be even). Recommended client frame size is 20–100 ms
(640–3 200 bytes at 16 kHz); the daemon forwards frames as they arrive.
Server frames are whatever the model returns (observed 40–730 ms); the client
must play them gaplessly by appending to a playback buffer, never by
scheduling each frame at "now".

## Client → server control messages

| Message                              | Meaning                                                                                                   |
|--------------------------------------|-----------------------------------------------------------------------------------------------------------|
| `{"type":"mute","on":true\|false}`   | Mute state changed. See *Mute semantics*.                                                                 |
| `{"type":"silence","on":true\|false}` | The client's silence gate closed (`on:true`, no binary frames follow) or reopened (`on:false`). See *Silence gate*. |
| `{"type":"speaking","on":true\|false}` | The client's own speech detector opened (`on:true`, it hears the shepherd) or closed. Optional; see *Client speech detector*. |
| `{"type":"playback","state":"started\|drained\|flushed","played_ms":N}` | The client's player started, drained or flushed. See *Playback reports*. `played_ms` is required for `drained` and `flushed` (optional, taken as 0, for `started`). |
| `{"type":"cancel"}`                  | The "stop" button. The daemon stops current playback (it will send `flush`) and runs the `stop` tool (the stop procedure) in the supervisor. Equivalent to the user saying "stop". |
| `{"type":"text","text":"…"}`         | Simulate an utterance: the text is fed to the voice session as if the user had spoken it (`/debug` and the `/voice` text box use this). It is recorded as a typed shepherd input, which the one rail accepts like a transcript (docs/roles.md "The one rail"). Without a voice session (`--voice=off`) the daemon echoes it as an `in` transcript and answers through its text router with one `out` transcript (a text routed to `cancel` yields `flush` then the `out`), or with an `error` when the text cannot be routed. |
| `{"type":"ping","gated_frames":N?,"peak":N?,"underruns":N?,"drains":N?,"buffer_ms":N?,"rate":F?,"stretched_ms":N?,"audio_state":S?,"aec":S?}` | Liveness probe; the daemon answers `pong`. Clients should send one every 5–30 s; `/voice` sends one every 5 s (the `web.client.stats` interval) and waits for the pong before the next. The optional counters (frames withheld by the silence gate so far; last input peak in Int16 LSB; the playback jitter buffer's underrun and drain counts and the audio buffered right now in ms; the time-stretch's current playback rate and the total wall-clock ms played at a rate other than 1.0 — see "Playback jitter buffer" below; the page's AudioContext state, `running`/`suspended`/`interrupted`/`closed` — a context that is not running plays nothing and stops updating the counters; `aec`, the microphone's echo cancellation as the browser reports it in `track.getSettings().echoCancellation`: `"true"`, `"false"`, or `"unknown"` when the browser does not say — other values are ignored; `/voice` also shows "Use headphones or the app: this browser's echo cancellation is off" when it is `false`, or `unknown` on a mobile browser) are copied into every following `web.client.stats` event until the next ping, with `playback_age_ms` (how old that sample is; absent before the first ping with `buffer_ms`), `audio_state` and `aec`. A value repeated across stats lines with a growing `playback_age_ms` is one old sample, not a stalled player. The event also carries `send_wait_max_ms`/`send_wait_p95_ms` (how long a recent audio frame waited in the daemon's per-client send queue; near 0 in normal operation). |

Unknown `type` values are answered with an `error` message and otherwise
ignored; the connection stays open.

## Server → client control messages

| Message                                                                  | Meaning                                                                                                         |
|--------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------|
| `{"type":"flush"}`                                                       | **Drop the playback buffer now.** Sent when the model was interrupted (barge-in, `cancel`, an interrupting push). Audio frames received before this message must not be played; frames received after it start a new utterance. |
| `{"type":"status","voice":"connecting\|connected\|reconnecting\|closed","muted":bool,"reason":"…"?,"stalled":true?,"provider":"gemini\|openai"?}` | Voice session state. Sent once on connect and on every change. `reason` is set for `closed` (e.g. `"replaced"`, `"shutdown"`, `"error"`); with GPT-Live, `connected` carries `reason:"standby"` while no billed session is open (it opens on the next speech or push) and no reason while one is. `muted` echoes the daemon's view of the mute state. `stalled` is present (true) while the model has not reacted to the user's last utterance for `gemini.stall_after_s`; the next status without it means the model answered or the connection was recycled. `provider` is the voice provider behind the session (absent with `--voice=off`); a provider switch sends `flush` then a fresh `status` with the new provider. |
| `{"type":"transcript","dir":"in\|out","text":"…","final":bool}`          | Incremental transcription. `in` = what the model heard from the user, `out` = what the model is saying. Fragments of one utterance arrive in order; `final:true` closes it. Output fragments may never carry `final:true`; treat the next `in` transcript or a `flush` as the boundary. GPT-Live has no end-of-utterance event: the daemon closes an `in` utterance with an **empty** fragment (`"text":"","final":true`) once the words stop; a client appends nothing and starts a new bubble for the next `in` text. |
| `{"type":"error","message":"…"}`                                         | A non-fatal problem the user should see (e.g. rate limited, bad control message). The connection stays open.  |
| `{"type":"pong"}`                                                        | Reply to `ping`.                                                                                                |

## Mute semantics

- **Muted means silence, not zeros.** While muted the client sends **no
  binary frames at all** (do not send zero-filled frames). The daemon also
  drops any audio frame that arrives while it believes the client is muted,
  so a race around the `mute` message cannot leak audio.
- The client sends `{"type":"mute","on":true}` *before* it stops sending and
  `{"type":"mute","on":false}` *before* it resumes. The daemon confirms the
  state in the next `status` message.
- Muting does not stop playback: the model can keep speaking and pushes are
  still delivered. There is no barge-in from a muted mic (plan §6).
- Reconnecting clients start **unmuted**; a client that wants to stay muted
  sends `mute` right after connecting.

## Silence gate

Near-zero PCM streamed continuously degrades the model session (WP7b,
`docs/history/e2e-results.md`: turns answered only after 4–35 s, idle pushes
swallowed), and a hardware-muted or very quiet microphone produces exactly
that. The client therefore gates its capture:

- A frame whose peak is below **8 LSB** (Int16) counts as silent. After
  **300 ms** of consecutive silent frames the gate closes: the client sends
  `{"type":"silence","on":true}` and then **no binary frames** until the
  input rises again.
- The first frame at or above the floor reopens the gate: the client sends
  `{"type":"silence","on":false}`, then the last **100 ms** of withheld
  frames (pre-roll, so the first syllable is not clipped), then the live
  frames.
- The daemon logs each transition as `web.client.silence {on}`; on
  `on:true` it tells the model the audio stream paused (Gemini: 1 s of a
  ±8 LSB comfort-noise floor at real-time pace, then `audioStreamEnd`,
  the same signal `mute` sends) so the server VAD closes the user's turn
  instead of waiting for frames that will not come — without the tail an
  utterance followed by digital silence was never transcribed (the
  audio-e2e harness, `docs/audio-e2e-results.md`). The next binary frame
  cancels a tail still running and reopens the stream on the model side.
  GPT-Live instead gets silence frames from the daemon while the gate is
  closed, since its session timeline advances with input audio.
- `web.client.stats` reports `silent` (gate closed right now),
  `silence_transitions`, and the ping-carried `gated_frames` and `peak`.
- The thresholds are the client's; `/voice` takes `?gate=off` and
  `?gate_floor=LSB&gate_hold=ms&gate_preroll=ms`. A client that never sends
  `silence` is treated as always open, and a reconnecting client starts
  open (send `silence` right after connecting if the gate is closed).

`mute` and `silence` are independent: a muted client sends neither frames
nor `silence` transitions; the gate resumes its own bookkeeping on unmute.

## Playback reports

Added for docs/research/voice-frameworks-2026-09-24.md P4/P5 (WP-B,
WP-C, WP-D). The daemon only knows what it *sent*; Gemini generates
~4× real time, so the model's turn is over seconds before the shepherd
has heard it. The client therefore reports what its player does:

```json
{"type":"playback","state":"started","played_ms":0}
{"type":"playback","state":"drained","played_ms":4200}
{"type":"playback","state":"flushed","played_ms":1850}
```

- **`started`** — the player began playing after being empty (the
  jitter buffer reached its start target). Not sent when it resumes
  from a mid-reply underrun (see `drained`).
- **`drained`** — the queue ran empty, the last sample played, and no
  new audio arrived within the underrun window (300 ms; *Playback
  jitter buffer*). An underrun that resumes within the window is the
  same run: no `drained`, no `started`, and `played_ms` keeps counting.
  A client that cannot tell may send `drained` at every dry ring: the
  daemon keeps "speaking" for 300 ms after `drained` and carries the
  run's remaining audio over, so the only cost is an extra message.
- **`flushed`** — on **every** `flush` from the daemon (and on a local
  flush such as the page's stop button), even with nothing playing
  (`played_ms: 0`).
- **`played_ms`** is the audio of the current run actually played since
  the last `drained`/`flushed` — at `drained` the run's whole length,
  at `flushed` how much of the cut-off run was heard. It is measured in
  **stream time**: milliseconds of the daemon's 24 kHz audio consumed
  by the player (samples received minus samples still buffered), not
  wall-clock time, so the adaptive time-stretch (0.85–1.05×) does not
  skew it. The web page's `Player` (`audio-util.js`) implements this;
  the iOS app must match (stream time, and `flushed` on every flush).

What the daemon does with them:

- **"Speaking" means the shepherd is still hearing it.** The voice
  session counts as speaking while the model generates **or** the
  client plays: from `started` until `drained` (+ 300 ms, a gap hold)
  or `flushed`; audio sent that the client has not started yet counts
  until its expected end. An INTERRUPT push that arrives while the
  client still plays a finished turn is deferred to WHEN_IDLE
  (`voice.push_deferred`) instead of cutting the sentence, and the gap
  between spoken pushes (`PushGap`, 2 s) runs from `drained`. A started
  run with no `drained` stops counting 5 s after the expected end of
  the audio sent (a lost message cannot pin the voice "speaking"), and
  a client that detaches or is replaced is forgotten at once.
- **Without playback reports** (an older client, `voice-test`) the
  daemon falls back to a timing guess: a reply counts as playing until
  the audio sent so far would have finished at real time from when it
  started arriving (audio_ms delivered minus wall time), with no
  jitter-buffer allowance. The session treats a client as reporting
  once it has sent one `playback` message.
- **The brain hears what the shepherd heard.** Each `flushed` whose
  `played_ms` is short of the run (by more than 150 ms) maps
  `played_ms` onto the run's output transcript — each transcript
  fragment is placed at the number of audio bytes sent when it arrived,
  and the fragment straddling the cut contributes a proportional share
  of its words — and the brain's `Assistant said` line for that speech
  becomes `Assistant said (cut off after N s): "…heard part…"`. GPT-Live
  uses the same byte-count method rather than the `start_ms`/`end_ms` on
  its output transcript: the daemon drops silent output audio before it
  reaches `/ws`, so the model's own timeline is not the stream the
  client played.
- Each report is logged as `voice.playback {state, played_ms, run_ms}`
  (`run_ms`: audio the daemon sent in that run; a cut adds `cut`,
  `heard`, `said`).
- Not done (TODO): telling the voice model silently which part was
  heard ("the shepherd heard only: …"). A context push right after a
  barge-in lands while the shepherd is talking, where Gemini's turn
  handling is most fragile, and it cannot be verified offline.

## Client speech detector

Push timing needs to know when the shepherd is talking; the input
transcript arrives late (the daemon's own rule is "an input transcript
within 1.5 s"), so a push can start just as the shepherd does.

- A client with its own voice-activity detector sends
  `{"type":"speaking","on":true}` when it opens on the shepherd's
  speech and `{"type":"speaking","on":false}` when it closes. Once a
  client has sent one `speaking` message, `speaking` is
  **authoritative** for that connection.
- A client that never sends `speaking` has its **silence gate** read
  the same way: `silence` `on:false` (the gate reopened) counts as
  speaking, `on:true` as not. This is what `/voice` does today — its
  "detector" is the gate, which opens on any input above the noise
  floor.
- While the detector is open, spoken pushes wait (both urgencies); when
  it closes they wait 300 ms more. An open detector holds pushes on its
  own for at most 8 s since it opened — a gate held open by room noise
  must not hold pushes forever; past that only input transcripts hold
  them. The transcript rule stays in force alongside (whichever holds
  longer): after `silence on:true` the daemon still streams 1 s of
  comfort noise before `audioStreamEnd`, so the model's own turn for
  the shepherd is still open for a moment.
- Logged as `web.client.speaking {client_id, on}` (`speaking`
  messages; gate transitions stay `web.client.silence`).

## Playback jitter buffer

Server audio frames arrive in bursts (observed 40–730 ms per frame, and
on GPT-Live, gaps up to ~1.5 s within one reply — `docs/audio-e2e-results.md`,
"GPT-Live playback gaps"), and GPT-Live delivered its audio at
only **0.72–0.86× real time** on sustained average until the daemon paced
its input at real time (2026-09-25: ≈1.0×, gaps under ~320 ms; see
"GPT-Live input pacing" in `docs/audio-e2e-results.md`). A client that starts
playing a frame the instant it arrives, or that buffers a fixed small
amount, underruns audibly mid-sentence. Every client (the web page's
`audio-util.js` `Player` + `Wsola`; the iOS app implements the **same
numbers**) runs an **adaptive jitter buffer with pitch-preserving
adaptive time-stretch**:

**Target per provider** (the client learns the provider from
`status.provider`; until it knows, Gemini's):

| Provider | Start target (and floor) | Cap |
|---|---|---|
| `gemini` | 150 ms | 500 ms |
| `openai` (GPT-Live) | 400 ms | 2000 ms |

- **Start**: buffer the target before playing the first frame of an
  utterance (or of a resume after running dry); a short utterance that
  never reaches it starts anyway after target + 250 ms of waiting.
- **On a mid-utterance underrun** (the buffer ran dry and more audio
  arrives within 300 ms — not the natural end of the reply): raise the
  target by **100 ms**, up to the cap, and do **not** resume on the first
  new chunk — wait until the buffer refills to the (now higher) target.
  The raised target persists for the session.
- **After an utterance that played through without an underrun**, decay
  the target by **20 ms**, floored at the provider's start value.
- **Playback rate** r is a function of the buffered audio b (everything
  received and not yet played, including the stretcher's own lookahead)
  against the current target T:

  | b | r |
  |---|---|
  | b > 2T | 1.05 while the lag debt is > 0 (catch up), else 1.00 |
  | T ≤ b ≤ 2T | 1.00 |
  | T/2 ≤ b < T | linear, 0.90 at T/2 → 1.00 at T |
  | T/4 < b < T/2 | linear, 0.85 at T/4 → 0.90 at T/2 |
  | b ≤ T/4 | 0.85 (floor) |

  r < 1 plays slower than real time, so the buffer drains more slowly.
  **Catch-up only pays back lag the slow-down added:** while r < 1 the
  client accumulates a lag debt of (1 − r)·dt; r = 1.05 is allowed only
  while the debt is > 0 and b > 2T, and each step at r > 1 subtracts
  (r − 1)·dt (the debt never goes below 0). The debt is reset at the end
  of each reply (a drain) and on `flush`. So a source that arrives far
  ahead of real time (Gemini, ~4×) plays at 1.00, not 5 % fast.
  Rate changes are **slewed at most 0.02 per 20 ms** of output (re-evaluated
  every 10 ms hop), so they are inaudible. r is only applied while playing.
- **Time-stretch is pitch-preserving WSOLA** (waveform-similarity
  overlap-add) on the 24 kHz mono stream, before resampling to the device
  rate: frame **20 ms** (480 samples), overlap **10 ms** (50 %, periodic
  Hann windows that sum to 1), similarity search **±8 ms** (±192 samples)
  around the nominal input position, maximising the normalised
  cross-correlation with the natural continuation of the previous frame
  over the overlap. The output advances 10 ms per frame, the input
  10 ms × r on average. At r = 1 every frame is the natural continuation,
  which reconstructs the input exactly. With less lookahead than a frame
  plus the search (the end of a reply, a nearly dry buffer) the stretcher
  plays the continuation at r = 1, and with less than a frame it passes
  the remaining samples through unchanged — it never holds audio back.
- **Cross-fade every start or resume over ~8 ms** (ramp in from silence)
  so a fresh utterance or a post-underrun resume never clicks; fade out
  over ~2 ms on a drain or a `flush`, which also resets the rate to 1 and
  the lag debt to 0 and empties the stretcher.
- Report `underruns`, `drains`, `buffer_ms` (audio buffered right now,
  including the stretcher's lookahead), the current `rate` and
  `stretched_ms` (total wall-clock ms played at r ≠ 1.0, either direction)
  on the `ping`
  message (above) so `web.client.stats` can see them.

This does not guarantee zero underruns: the floor is 0.85×, so a reply
whose audio arrives slower than that on sustained average
(`realtime_ratio` in the daemon's `voice.audio_timing` log) still runs dry
once the buffer is spent. On the recorded GPT-Live timelines
(`web/voice/testdata/gptlive-*.json`, 0.78× and 0.69×) underruns go from
3 → 0, 2 → 1 and 4 → 1 (chained) versus the Gemini policy without
stretch (`playback-timeline.test.mjs`); on a synthetic 60 s reply built
from the same recorded gaps (0.73×) from 23 → 11. What the policy
reliably removes is the *click* — an underrun is a faded pause.

## Ordering guarantees

- Within one direction, frames are delivered in the order sent (WebSocket
  guarantee). The daemon emits `flush` on the same connection *before* the
  first audio frame of the next model utterance, so "everything received
  before `flush` is stale" is exact.
- Text and binary frames are interleaved in the order the daemon produced
  them; a `transcript` may precede or follow the audio it describes by a few
  frames, so clients must not synchronise captions to audio byte counts.
- The daemon never sends binary frames before the first `status` message.
- Client control messages are handled in order; `cancel` followed by audio
  frames means the audio is a new utterance.

## Reconnect behaviour

- **Browser reconnect.** The daemon session persists. The new client gets a
  `status` and then continues: any model audio produced while no client was
  attached is **dropped**, not queued (the conversation is real time), and
  logged once per model turn as `web.audio_unheard {bytes, provider}` (the
  turn's `voice.audio_timing` has its length). A pending `flush` is not
  replayed.
- **Pushes wait for a listener.** While no client is attached, the voice
  session holds every spoken push (idle or interrupt) — a brain reply that
  came after the page closed, an announce, a kept promise — instead of
  making the model say it to nobody: `voice.push_held {text, urgency,
  reason: no_listener, push_id, …}` once per push, then `voice.push_released
  {…, held_ms}` and the usual `voice.push` when a client attaches, where
  it goes out under the usual urgency rules (after the model's turn, never
  over the shepherd). A push held 5 minutes is dropped
  (`voice.push_dropped {reason: no_listener, age_ms}`). Silent pushes are
  context and are never held. Both providers log `voice.client {attached}`
  when the attach state changes; an open GPT-Live session is not kept open
  (billed) for a held push, and a fresh one opens for it on attach. What
  cannot be held is the model's own reply to audio it heard before the
  page went away (see `web.audio_unheard` above).
- **Voice reconnect.** When the daemon reconnects to the model (GoAway, drop,
  rate limit), clients see `status: reconnecting` then `status: connected`.
  Audio frames the client sends during the gap are buffered by the daemon
  (bounded, oldest dropped) and replayed on reconnect; nothing is required
  from the client.
- **Voice closed.** `status: closed` with a `reason` means the voice session
  is gone for good (daemon shutting down, all API keys exhausted). The client
  should stop capturing and show the reason. It may retry `/ws` after a few
  seconds; a `connecting` status means the daemon is trying again.

## Auth

`web.bind`/`web.token` (CONFIG.md "web.token") let the daemon be reached
from a phone on the LAN or over Tailscale. With `web.token` unset (the
loopback default) every endpoint below is open. With it set:

- `GET /`, `/voice`, `/debug` and their assets, and `GET /api/hello`
  stay public — they hold no data, and a client has to be able to reach
  `/api/hello` before it knows the daemon needs a token at all.
- Every other endpoint — `GET /ws`, `GET /events`, every `/api/*` route —
  requires the token, presented one of three ways: the header
  `Authorization: Bearer <token>`; the query parameter `?token=<token>`,
  accepted only on `/ws` and `/events` (a browser cannot set a header on
  a WebSocket or `EventSource` request); or the cookie `sheepdog_token`,
  set by `POST /api/login {"token": "…"}` so the token is typed once —
  `/voice` and `/debug` do this themselves, showing an inline prompt the
  first time a request answers 401.
- Missing or wrong: `401` with a JSON body `{"error":"unauthorized"}`,
  logged as `web.unauthorized {path, remote}` (never the token itself).
  A WebSocket upgrade or an SSE stream refused this way never reaches
  the contract below it — the client sees an ordinary HTTP 401, not a
  `status`/`error` frame.
- `GET /api/hello` answers `{"name":"sheepdog","version":"…","voice_name":"…","auth":"token"|"none","ws":"/ws","audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"},"voice_provider":"gemini"|"openai"|"off","voice_model":"…"}`
  so a client can confirm it found a sheepdog daemon, whether it needs a
  token, this contract's audio format and the voice model behind it
  before it ever opens `/ws`.

## Voice provider

The voice behind `/ws` is Gemini Live (`gemini`) or GPT-Live
(`openai`), switchable at runtime. **The `/ws` contract does not change
with the provider**: 16 kHz PCM16 in, 24 kHz PCM16 out, the same
control messages; the daemon resamples for GPT-Live (which runs at
24 kHz both ways).

- `GET /api/voice/providers` (token-protected) →
  `{"provider":"gemini","model":"gemini-3.8-live","providers":[{"id":"gemini","label":"Gemini Live","model":"gemini-3.8-live","available":true},{"id":"openai","label":"GPT-Live","model":"gpt-live-1","available":false,"reason":"no OPENAI_API_KEY","billing":"per_minute"}]}`.
  `reason` appears only on an unavailable provider, `billing:"per_minute"`
  only on one billed by session time (GPT-Live, $0.05/min). With
  `--voice=off`: `{"provider":"off","model":"","providers":[]}`.
- `PUT /api/voice/provider {"provider":"openai"}` switches: the current
  voice session closes, the new provider's starts (GPT-Live: in standby,
  opening on the next speech or push), the choice is kept in the state
  file (`session.voice_provider`, it wins over `voice.provider` on
  restart) and `voice.provider_changed {from, to, via}` is logged. It
  answers with the GET shape; `400 {"error":"provider \"x\": …"}` for an
  unknown or unavailable provider (the reason is in the message), `503`
  with `--voice=off`.
- `GET /api/state` has top-level `voice_provider` and `voice_model`, and
  `conversation_started_at`: when the current conversation began (the
  last "New conversation" reset, below, or daemon start).
- **GPT-Live is billed per second of open session**, so the daemon opens
  one only while a client is attached (`/ws` connected) and unmuted and
  there is speech (a frame loud enough) or a push to say, and closes it
  after `voice.openai.idle_close_s` (default 45 s) without a transcript,
  a reply, a delegation or a push (`voice.session_open {reason}` /
  `voice.session_close {reason, dur_ms, billed_s}`;
  `web.client.stats` carries `voice_billed_seconds` and
  `voice_session_open`). The first words after a standby are buffered
  (about 0.5 s of pre-roll plus the ~0.6–0.9 s the session takes to
  open) and replayed, so nothing the shepherd says is lost; a client
  needs no special handling beyond showing `standby` as "ready".
  Disconnecting, muting or letting the silence gate close all let the
  idle timer run.

Every comparison is constant-time (`crypto/subtle.ConstantTimeCompare`);
there is no rate limiting or lockout on a wrong token.

## History and resume

Added so a client that was away (a phone asleep, a browser tab
reloaded, the daemon restarted) can show the conversation it missed and
answer a question by tap. All three need the token under "Auth".

- **Event ids.** Every event-log line carries `id`: an integer, strictly
  increasing across day files and daemon restarts, close to the event's
  time in Unix microseconds (the log takes max(now, last + 1)). Lines
  written before ids existed get one derived the same way when read
  back.
- **`GET /events` resumes.** Every SSE frame is `id: <event id>`,
  `event: log`, `data: <the JSON line>`. A reconnecting `EventSource`
  sends `Last-Event-ID` itself; `?since=<id>` does the same by hand
  (the header wins). The daemon replays what came after that id from the
  log — across days and restarts — then streams live, with no
  duplicates. The replay is bounded to the newest 2 000 events after the
  id (a `: replay truncated …` comment says so). `?n=<count>` replays the
  last count events of the current run instead (what `/debug` does on a
  fresh load; a `?since=` below 10¹² is read as a count, as before).
- **`GET /api/conversation?limit=200&before=<id>`** answers
  `{"items": [...], "next_before": <id>|null}`: the conversation rebuilt
  from the log, newest last, at most `limit` items (default 200, at most
  1 000) older than `before` (omit it for the newest page); pass
  `next_before` as the next `before` to page back, until it is `null`.
  Each item is `{id, ts, kind, text, via?, agent?, urgency?, pending_id?,
  options?, answered?, answer?, resolution?}` with `kind`:
  - `shepherd` — what the shepherd said (voice transcript fragments
    merged per turn, `via: "voice"`) or typed (`via: "typed"`);
  - `dog` — what the dog said (output transcript merged per turn, or the
    spoken reply with `--voice=off`);
  - `push` — a push the dog spoke (never a silent one), with `urgency`
    and the `agent` it is about;
  - `approval` — a worker's approval prompt: `text` its summary,
    `agent`, `pending_id`, `options` `[{index, label, keys, default}]`,
    `answered`, `answer` and `via` (`tap`, `fast_path`, `brain`) once
    answered, `resolution` `open` | `confirmed` | `expired` |
    `cancelled` | `replaced`;
  - `notice` — a divider: the daemon started, the voice reconnected (with
    or without its context), the voice session closed, or (`subkind:
    "reset"`, `text: "New conversation"`) a "New conversation" reset.

  `id` is the first event's id, so items sort and page with the SSE
  stream's ids.
- **New conversation.** `POST /api/conversation/reset {"via":"app"|
  "debug"|"voice_page"}` resets the brain's transcript window (a fresh
  segment; the compaction summary is gone) and the voice model's own
  session (Gemini: a fresh reconnect with no resumption handle;
  GPT-Live: an open billed session closes now, no recap, and reopens
  later) — the roster, its ledger, open approval prompts, promises,
  notes, investigations and the event log itself are untouched
  (docs/roles.md "New conversation"). It answers `{"ok":true,
  "reset_id":"<id>","at":"<RFC3339>"}`, logs `conversation.reset {via,
  reset_id, at}` (so the SSE stream and a reconnecting client's replay
  both carry it), and adds the `notice`/`reset` item above to
  `GET /api/conversation`. The same reset is reachable by a bare voice
  phrase ("start over", "new conversation", "start from scratch", "reset
  the conversation", "clear the conversation"): the dog asks "Start a
  new conversation? Say \"yes, start over\" to confirm.", and only the
  exact confirm phrase ("yes, start over" / "confirm reset"), from real
  shepherd input, within 20 s, carries it out — a bare "yes" keeps its
  ordinary approval-prompt meaning, and the fresh session then says one
  short line ("Fresh start. What's next?").
- **Approve by tap.** `GET /api/state`'s `pending_action` (and the
  `pending.created` event) carries `agent`, `summary`, `question`,
  `command` and `options: [{index, label, keys, default}]`.
  `POST /api/pending/answer {"id": "<pending id>", "option": <index>}`
  answers it: the tap is recorded as a shepherd input and passes the same
  rail as a spoken answer (docs/roles.md "The one rail"), the option's
  keys go to the worker, and the answer is `{"ok": true, "spoken":
  "Told auth Yes."}`; the voice is told silently ("The shepherd tapped
  Yes on auth's question"). A question that is no longer open (answered,
  replaced, or expired and gone from the screen) is `409
  {"error":"that question is no longer open"}`; an option the question
  does not have is `400`.

## Phone notifications

Not part of `/ws`: the iOS app registers its APNs device token over
`POST /api/push/register` and the daemon sends system notifications for
events that need the shepherd, whether or not a `/ws` client is attached.
The contract (routes, payload, when an alert is sent) is in
[`push.md`](push.md).

## Non-goals

No compression, no multiple clients, no client-side resampling contract
beyond "send 16 kHz PCM16 mono", no timestamps in frames. No TLS: the
`Secure` cookie flag stays off and the token travels in the clear, so a
non-loopback bind should be a private network (a LAN you trust, or a
Tailscale address) rather than an open one.
