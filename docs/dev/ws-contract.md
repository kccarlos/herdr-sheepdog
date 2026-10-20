# Browser ↔ daemon WebSocket contract

The Go types for every control message live in `internal/voice/wsproto`;
the WS server, the browser page and the iOS app are all built from this
document. Change the doc and the Go types together: every line of every
fenced `json` block below is one control message, and
`internal/voice/wsproto/contract_test.go` parses each one with the Go
type (unknown fields fail), re-encodes it and requires the same JSON back
(a field the type drops fails), and requires an example of every message
type. Schemas with placeholders (`true|false`, `N`) stay in tables and
inline code; only real messages go in `json` blocks.

## Endpoint

- `GET /ws` on the daemon's HTTP port, upgraded to a WebSocket. Same origin as
  `/voice`. Public (no token needed) when `web.bind` is loopback and
  `web.token` is unset, the default; see "Auth" below for a non-loopback
  bind (a phone on the LAN or over Tailscale).
- **One client at a time.** A second connection replaces the first: the
  daemon sends the first client `{"type":"status","voice":"closed","reason":"replaced"}`
  and closes it with WebSocket code 1000, then serves the new one. The
  replacement waits for the new client's handshake to be accepted (its
  `client-ready`, its first other frame, or 2 s of silence; see *Protocol
  version and handshake*), so a client refused for its protocol never
  kicks the attached one off. Until then the new client has had its
  connect `status` (with `muted:false`, the state it will start in) and
  nothing else; on attach it gets a fresh `status` if the voice state
  changed meanwhile. With no client attached a new one attaches at once.
- The Gemini session lives in the daemon (decision D2). Opening, closing or
  replacing a browser connection never restarts it. A new client immediately
  receives one `status` message describing the current voice state and then
  continues the same conversation.

## Protocol version and handshake

The daemon and its clients ship separately (the iOS app through
TestFlight, the daemon as a binary), so `/ws` carries a **protocol
version**: an integer that changes only when the contract below changes
in a way a client must know about. It is independent of the daemon's
binary version.

| Protocol | Contract |
|---|---|
| 1 | Everything in this document (the contract as of the handshake's introduction, plus the handshake itself). |

- **`GET /api/hello`** carries `protocol` (what the daemon speaks) and
  `min_protocol` (the oldest client protocol it still serves), so a
  client can tell before it connects. A daemon without them is protocol 1.
- **`client-ready`.** A protocol-aware client sends this as its **first
  text frame**, before `mute`, `silence`, `speaking` or any audio:

  ```json
  {"type":"client-ready","protocol":1,"client":{"name":"sheepdog-ios","version":"0.2.0 (12)","platform":"ios"}}
  {"type":"client-ready","protocol":1,"client":{"name":"sheepdog-voice","version":"embedded","platform":"web"}}
  ```

  `protocol` is required; `client` (`name`, `version`, `platform`) is
  for the log and should always be sent.
- **`bot-ready`.** The daemon answers with the protocol it will speak on
  this connection (its own: a newer client steps down to it, or closes if
  it cannot), the oldest it serves, its binary version and its
  **features** — optional parts of the contract and the HTTP API beside
  it, so a client branches on a name rather than a version number:

  ```json
  {"type":"bot-ready","protocol":1,"min_protocol":1,"daemon":{"version":"0.4.2"},"features":["playback","speaking","silence","provider","history","push","models","screen","feedback"]}
  ```

  | Feature | Meaning | Listed |
  |---|---|---|
  | `playback` | playback reports drive "speaking" and cut transcripts (*Playback reports*) | always |
  | `speaking` | the client speech detector (*Client speech detector*) | always |
  | `silence` | the silence gate (*Silence gate*) | always |
  | `provider` | `status.provider` and the runtime voice switch (*Voice provider*) | when a voice is configured |
  | `history` | `/events` resume, `/api/conversation`, reset, answer by tap (*History and resume*) | always |
  | `push` | Push registration and the content key ([`push.md`](push.md)) | when the daemon has a push service |
  | `models` | the orchestrator model picker (`/api/brain/models`) | when the orchestrator is on |
  | `screen` | the agent window over `/ws` (*Agent window over /ws*) | when the daemon has a supervisor (always, in practice) |
  | `feedback` | `POST /api/feedback` and the client's `drain` messages (*Feedback and drains*) | always |
  | `cards` | the client's `visibility` reports and the daemon's `card` messages (*Cards*) | when the daemon has a supervisor (always, in practice) |
  | `term` | the terminal socket `GET /term` for live agent windows ([`term-contract.md`](term-contract.md)) | when herdr is 0.9.2 or newer (not with `--herdr=mock`) |

  A new optional part of the contract is a new feature, not a new
  protocol: a client uses it only when `bot-ready` lists it, and a
  daemon without it never sees its messages, so both directions stay
  compatible and `protocol` stays 1.

- **Order.** The daemon still sends its `status` **at once** on connect,
  before the handshake, exactly as before (a legacy client depends on
  it); `bot-ready` follows the client's `client-ready`. A client must
  not wait for `bot-ready` before it sends audio or its `mute`/`silence`
  state — it sends `client-ready` first and carries on.
- **Legacy clients.** A client whose first frame is anything other than
  `client-ready` (a control message or audio), or that sends nothing for
  **2 s**, is a **legacy protocol-1 client** (the iOS app 0.1.0, older
  `/voice` pages, `voice-test`). It is served exactly as before and is
  never sent `bot-ready`. A `client-ready` that arrives later anyway is
  still answered with `bot-ready`.
- **Too old.** A client below `min_protocol` (a `client-ready` with a
  lower `protocol`, or a legacy client once `min_protocol` is above 1)
  gets an `error` it can show as is, then `status` `closed` with reason
  `protocol_too_old`, then a close with WebSocket code **4001** and a
  short reason; it sees no `bot-ready`. It should not reconnect in a
  loop: it tells the user to update the app. A refused client never
  replaces the client already attached.

  ```json
  {"type":"error","message":"This app speaks /ws protocol 0; the daemon (version 0.4.2) needs protocol 1 or newer. Update the app."}
  {"type":"status","voice":"closed","muted":false,"reason":"protocol_too_old"}
  ```

- **Too new.** A client that needs a newer daemon than the one it got (a
  `bot-ready` `protocol` below the client's own minimum) closes and tells
  the user to update the daemon.
- **An older daemon** has no handshake: it answers `client-ready` with
  `{"type":"error","message":"wsproto: unknown type \"client-ready\""}`
  and never sends `bot-ready`. A client takes that error (or 2 s without
  `bot-ready`) to mean protocol 1, and does not show it as a problem.
- `web.client.connect {client_id, remote, replaced, pending}` —
  `pending` when another client was attached, in which case
  `web.client.attach {client_id, replaced}` follows once the handshake is
  accepted.
- Logged as `web.client.hello {client_id, protocol, client, legacy,
  accepted}`: `client` is the `client-ready`'s `{name, version,
  platform}` (null for a legacy client); a legacy client adds `why`
  (`first_frame` or `timeout`); a `client-ready` after the client was
  already resolved adds `late: true`.

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
| `{"type":"speaking","on":true\|false}` | The client's own speech detector opened (`on:true`, it hears the user) or closed. Optional; see *Client speech detector*. |
| `{"type":"playback","state":"started\|drained\|flushed","played_ms":N}` | The client's player started, drained or flushed. See *Playback reports*. `played_ms` is required for `drained` and `flushed` (optional, taken as 0, for `started`). |
| `{"type":"drain","at":"…","buffer_ms":N,"played_ms":N,"received_ms":N?,"reason":"dry\|early"?}` | The client's player ran dry, for diagnosis only. Requires the `feedback` feature. See *Feedback and drains*. |
| `{"type":"cancel"}`                  | The "stop" button. The daemon stops current playback (it will send `flush`) and runs the `stop` tool (the stop procedure) in the supervisor. Equivalent to the user saying "stop". |
| `{"type":"text","text":"…"}`         | Simulate an utterance: the text is fed to the voice session as if the user had spoken it (`/debug` and the `/voice` text box use this). It is recorded as a typed user input, which the one rail accepts like a transcript (docs/dev/roles.md "The one rail"). Without a voice session (`--voice=off`) the daemon echoes it as an `in` transcript and answers through its text router with one `out` transcript (a text routed to `cancel` yields `flush` then the `out`), or with an `error` when the text cannot be routed. |
| `{"type":"screen-subscribe","pane_id":"…","lines":N?}` | Show me this agent's window; `screen` frames follow. Replaces the client's previous subscription. See *Agent window over /ws*. |
| `{"type":"screen-unsubscribe"}`      | Stop the window's `screen` frames. |
| `{"type":"visibility","visible":true\|false,"voice_screen":true\|false}` | Whether the client's screen can be seen now (`visible`: the app in the foreground, the page not hidden) and its voice screen is the one in front (`voice_screen`, taken as false when absent). Requires the `cards` feature. See *Cards*. |
| `{"type":"ping","gated_frames":N?,"peak":N?,"underruns":N?,"drains":N?,"buffer_ms":N?,"rate":F?,"stretched_ms":N?,"audio_state":S?,"aec":S?}` | Liveness probe; the daemon answers `pong`. Clients should send one every 5–30 s; `/voice` sends one every 5 s (the `web.client.stats` interval) and waits for the pong before the next. The optional counters (frames withheld by the silence gate so far; last input peak in Int16 LSB; the playback jitter buffer's underrun and drain counts and the audio buffered right now in ms; the time-stretch's current playback rate and the total wall-clock ms played at a rate other than 1.0 — see "Playback jitter buffer" below; the page's AudioContext state, `running`/`suspended`/`interrupted`/`closed` — a context that is not running plays nothing and stops updating the counters; `aec`, the microphone's echo cancellation as the browser reports it in `track.getSettings().echoCancellation`: `"true"`, `"false"`, or `"unknown"` when the browser does not say — other values are ignored; `/voice` also shows "Use headphones or the app: this browser's echo cancellation is off" when it is `false`, or `unknown` on a mobile browser) are copied into every following `web.client.stats` event until the next ping, with `playback_age_ms` (how old that sample is; absent before the first ping with `buffer_ms`), `audio_state` and `aec`. A value repeated across stats lines with a growing `playback_age_ms` is one old sample, not a stalled player. The event also carries `send_wait_max_ms`/`send_wait_p95_ms` (how long a recent audio frame waited in the daemon's per-client send queue; near 0 in normal operation), and with Gemini Live the echo gate's `echo_armed`, `echo_withheld_frames`, `echo_released_frames`, `echo_opens` (docs/dev/research/echo-2026-10-03.md: once this client's echo has shown itself, microphone audio the playing reply explains is held back). |

Unknown `type` values are answered with an `error` message and otherwise
ignored; the connection stays open. Unknown *fields* are ignored (a newer
client's extra `ping` counters must not break an older daemon).

Examples (`client-ready` is under *Protocol version and handshake*):

```json
{"type":"mute","on":true}
{"type":"silence","on":false}
{"type":"speaking","on":true}
{"type":"cancel"}
{"type":"text","text":"what is auth doing"}
{"type":"screen-subscribe","pane_id":"w1:p2","lines":200}
{"type":"screen-subscribe","pane_id":"w1:p2"}
{"type":"screen-unsubscribe"}
{"type":"visibility","visible":true,"voice_screen":true}
{"type":"visibility","visible":false,"voice_screen":true}
{"type":"ping"}
{"type":"ping","gated_frames":120,"peak":3400,"underruns":1,"drains":4,"buffer_ms":180,"rate":0.97,"stretched_ms":2300,"audio_state":"running","aec":"true"}
```

## Server → client control messages

| Message                                                                  | Meaning                                                                                                         |
|--------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------|
| `{"type":"flush"}`                                                       | **Drop the playback buffer now.** Sent when the model was interrupted (barge-in, `cancel`, an interrupting push). Audio frames received before this message must not be played; frames received after it start a new utterance. |
| `{"type":"status","voice":"connecting\|connected\|reconnecting\|closed","muted":bool,"reason":"…"?,"stalled":true?,"provider":"gemini\|openai"?}` | Voice session state. Sent once on connect and on every change. `reason` is set for `closed` (e.g. `"replaced"`, `"shutdown"`, `"error"`); with GPT-Live, `connected` carries `reason:"standby"` while no billed session is open (it opens on the next speech or push) and no reason while one is. `muted` echoes the daemon's view of the mute state. `stalled` is present (true) while the model has not reacted to the user's last utterance for `gemini.stall_after_s`; the next status without it means the model answered or the connection was recycled. `provider` is the voice provider behind the session (absent with `--voice=off`); a provider switch sends `flush` then a fresh `status` with the new provider. |
| `{"type":"transcript","dir":"in\|out","text":"…","final":bool}`          | Incremental transcription. `in` = what the model heard from the user, `out` = what the model is saying. Fragments of one utterance arrive in order; `final:true` closes it. Output fragments may never carry `final:true`; treat the next `in` transcript or a `flush` as the boundary. GPT-Live has no end-of-utterance event: the daemon closes an `in` utterance with an **empty** fragment (`"text":"","final":true`) once the words stop; a client appends nothing and starts a new bubble for the next `in` text. |
| `{"type":"error","message":"…","problem":"…"?,"role":"…"?,"provider":"…"?,"fix":"…"?}` | A non-fatal problem the user should see (e.g. rate limited, bad control message). The connection stays open. With `problem` it is an account problem; see *Account problems*. |
| `{"type":"pong"}`                                                        | Reply to `ping`.                                                                                                |
| `{"type":"screen","screen":{…}}`                                         | The subscribed agent's window changed (*Agent window over /ws*).                                                 |
| `{"type":"screen-error","pane_id":"…","message":"…","ended":true?}`      | The subscribed window could not be read; `ended` means the subscription is over.                                 |
| `{"type":"card","card":{…}}`                                             | The orchestrator put something on the user's screen (*Cards*). Only sent to a client that reported itself visible. |

Examples (`bot-ready` is under *Protocol version and handshake*):

```json
{"type":"flush"}
{"type":"status","voice":"connected","muted":false,"provider":"gemini"}
{"type":"status","voice":"connected","muted":false,"reason":"standby","provider":"openai"}
{"type":"status","voice":"connected","muted":true,"stalled":true,"provider":"gemini"}
{"type":"status","voice":"closed","muted":false,"reason":"replaced"}
{"type":"transcript","dir":"in","text":"is auth done","final":true}
{"type":"transcript","dir":"out","text":"Auth finished its tests.","final":false}
{"type":"error","message":"rate limited, retrying"}
{"type":"error","message":"DeepSeek says the balance is used up, so the orchestrator cannot use it.","problem":"balance","role":"orchestrator","provider":"deepseek","fix":"top up the account (https://platform.deepseek.com)"}
{"type":"pong"}
```

## Account problems

When the orchestrator's or the voice's provider fails because of the
account — an invalid key (`problem:"invalid_key"`), a used-up quota
(`"quota"`, not a per-minute rate limit) or a used-up balance
(`"balance"`, HTTP 402) — the daemon sends one `error` message with
these optional fields:

- `message`: a plain sentence naming the service and what it blocks;
- `problem`: `invalid_key`, `quota` or `balance`;
- `role`: what it blocks, `orchestrator` or `voice`;
- `provider`: the provider id (`gemini`, `vertex`, `deepseek`, `openai`, or an
  OpenAI-compatible provider's name);
- `fix`: the one-line fix, which may name the key's variable and the
  console URL.

It is sent once when the problem starts, not on every retry, and again
to each client that attaches while it lasts (after the first `status`).
It lasts until the provider answers again (or, for the orchestrator, the
provider is selected again in the model picker). Nothing tells the
client that it ended; a client may hide it after a while. Show `message`
and `fix`; an older client shows `message` alone, which is a complete
sentence. The orchestrator's problems are also spoken once by the voice,
in the user's language (and are the answer to the user's words while the
orchestrator has no provider left). The voice's own problems cannot be
spoken by the voice: `/voice` reads them aloud once with the browser's
speech synthesis.

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
`docs/dev/history/e2e-results.md`: turns answered only after 4–35 s, idle pushes
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
  audio-e2e harness, `docs/dev/history/audio-e2e-results.md`). The next binary frame
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

Added for docs/dev/research/voice-frameworks-2026-09-24.md P4/P5 (WP-B,
WP-C, WP-D). The daemon only knows what it *sent*; Gemini generates
~4× real time, so the model's turn is over seconds before the user
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

- **"Speaking" means the user is still hearing it.** The voice
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
- **The orchestrator hears what the user heard.** Each `flushed` whose
  `played_ms` is short of the run (by more than 150 ms) maps
  `played_ms` onto the run's output transcript — each transcript
  fragment is placed at the number of audio bytes sent when it arrived,
  and the fragment straddling the cut contributes a proportional share
  of its words — and the orchestrator's `Assistant said` line for that speech
  becomes `Assistant said (cut off after N s): "…heard part…"`. GPT-Live
  uses the same byte-count method rather than the `start_ms`/`end_ms` on
  its output transcript: the daemon drops silent output audio before it
  reaches `/ws`, so the model's own timeline is not the stream the
  client played.
- Each report is logged as `voice.playback {state, played_ms, run_ms}`
  (`run_ms`: audio the daemon sent in that run; a cut adds `cut`,
  `heard`, `said`).
- Not done (TODO): telling the voice model silently which part was
  heard ("the user heard only: …"). A context push right after a
  barge-in lands while the user is talking, where Gemini's turn
  handling is most fragile, and it cannot be verified offline.

## Feedback and drains

The user can mark a moment of the conversation from the app ("that
paused"), and the mark has to say where the pause came from: the voice
model stopped sending audio, the audio was late on its way to the
phone, or the phone's player lost or ran out of audio it had. Both
parts are behind the `feedback` feature.

**`POST /api/feedback`** (auth like every `/api` route). Body:

```text
{"tag":"paused","note":"that paused","client_ts":"2026-10-08T00:49:01.250Z","conversation_turn":"b-12"}
```

- `tag` is one of `paused` (the app's one-tap default, and what an
  empty tag means), `wrong_answer`, `too_short`, `too_long`,
  `misheard`, `other`; anything else is `400`.
- `note` is optional free text (the first 500 characters are kept);
  `client_ts` is when the user tapped, RFC 3339 on the phone's clock
  (`400` if it does not parse); `conversation_turn` optionally names
  the conversation item on screen (the app's own id).
- Answer: `{"ok":true,"id":"fb-…","ts":"…"}`.
- Logged as `user.feedback {id, tag, note, server_ts, client_ts,
  skew_ms, conversation_turn, snapshot}`. `skew_ms` is the server
  time minus `client_ts` (request latency plus clock offset). The
  `snapshot` is the last 30 s as the daemon saw it: the voice provider
  and model; each run of the voice's output audio with a chunk in the
  window, with its timing (the `voice.audio_timing` shape) and every
  chunk's arrival, length and write to the client
  (`[arrival ms, audio ms, written ms or -1]` from the run's start);
  the window's transcripts, playback reports, drains, client stats,
  pushes, tool calls and orchestrator turns (long strings cut to 200
  characters); and the orchestrator turns still in flight.
  `sheepdog feedback` lists the marks with a diagnosis read off it.

**`drain`** (client → server). Each time the client's player runs out
of audio — the end of every reply as well as a mid-reply underrun — it
may send:

```json
{"type":"drain","at":"2026-10-08T00:48:51.950Z","buffer_ms":0,"played_ms":3850,"received_ms":3850,"reason":"dry"}
{"type":"drain","at":"2026-10-08T00:48:51.950Z","buffer_ms":3900,"played_ms":3850,"received_ms":7800,"reason":"early"}
```

- `at` (required) is when it ran dry, RFC 3339 with milliseconds on
  the client's clock; `played_ms` (required) and `received_ms` are the
  current run's stream ms played and received by then (as in
  *Playback reports*); `buffer_ms` (required) is what the client
  believed it still had buffered: 0 for an ordinary dry ring, more when
  the player finished early and that audio never played.
- `reason`: `dry` (the predicted playhead ran out) or `early` (the
  player said it had finished before the predicted playhead got there:
  on iOS an audio-engine restart discards what was scheduled).
- **Diagnostic only.** It does not count as a playback report: the
  `started`/`drained` reports still drive "speaking", and a daemon
  without the feature answers it with an `error`, so a client sends it
  only when `bot-ready` lists `feedback`.
- Logged as `web.client.drain {client_id, at, buffer_ms, played_ms,
  received_ms, reason, skew_ms}` (`skew_ms`: the daemon's receive time
  minus `at`).

## Client speech detector

Push timing needs to know when the user is talking; the input
transcript arrives late (the daemon's own rule is "an input transcript
within 1.5 s"), so a push can start just as the user does.

- A client with its own voice-activity detector sends
  `{"type":"speaking","on":true}` when it opens on the user's
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
  the user is still open for a moment.
- Logged as `web.client.speaking {client_id, on}` (`speaking`
  messages; gate transitions stay `web.client.silence`).

## Playback jitter buffer

Server audio frames arrive in bursts (observed 40–730 ms per frame, and
on GPT-Live, gaps up to ~1.5 s within one reply — `docs/dev/history/audio-e2e-results.md`,
"GPT-Live playback gaps"), and GPT-Live delivered its audio at
only **0.72–0.86× real time** on sustained average until the daemon paced
its input at real time (2026-09-25: ≈1.0×, gaps under ~320 ms; see
"GPT-Live input pacing" in `docs/dev/history/audio-e2e-results.md`). A client that starts
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
  `bot-ready` comes after that `status`, in reply to `client-ready`;
  audio may arrive before it.
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
  session holds every spoken push (idle or interrupt) — an orchestrator reply that
  came after the page closed, an announce, a kept promise — instead of
  making the model say it to nobody: `voice.push_held {text, urgency,
  reason: no_listener, push_id, …}` once per push, then `voice.push_released
  {…, held_ms}` and the usual `voice.push` when a client attaches, where
  it goes out under the usual urgency rules (after the model's turn, never
  over the user). A push held 5 minutes is dropped
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

## Pairing

`sheepdog pair` prints, and `/debug` (State → Pair a phone; `GET
/api/pair/qr.svg`) shows, a QR code holding one link:

```
sheepdog://pair?u=<base URL>&u=<base URL>…&t=<token>
```

- `u` (one or more, URL-encoded): an `http://host:port` base URL of the
  daemon — every IPv4 LAN address and then every Tailscale address
  (100.64.0.0/10 or a `tailscale*` interface) when `web.bind` is
  `0.0.0.0`, else the bound address itself. Loopback and link-local
  addresses are never listed; with a loopback bind there is no code.
- `t`: `web.token`, absent when none is set.

The app tries the addresses in order with `GET /api/hello` and keeps the
first that answers. The token is never printed beside the code, never
logged, and `GET /api/pair` (the address list for `/debug`) leaves it
out. Both routes need the token like the rest of `/api`.

After pairing, a phone that allows notifications registers with
`POST /api/push/register` (authenticated like the rest of `/api`). The
answer carries the phone's **notification content key**
(`{"ok":true,"content_key":"<base64, 32 bytes>","encryption":"aes-256-gcm"}`):
generated by the daemon for that phone at its first registration, the
same on every later one. The app keeps it for its notification service
extension, which decrypts the end-to-end encrypted notifications that
come through the push relay ([`push.md`](push.md), "End-to-end
encryption").

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
- `/setup` and `/api/setup/*` (the setup page, `sheepdog setup`) need no
  token but answer only a loopback client that names a loopback host
  (`127.0.0.1`, `::1`, `localhost`), and a request that changes something
  only from that origin as JSON; anything else gets `403` with "The setup
  page only opens on the computer sheepdog runs on…". The phone never
  uses them. `POST /api/setup/login` sets the `sheepdog_token` cookie for
  this browser, so `/voice` opened from the setup page asks for nothing.
- Missing or wrong: `401` with a JSON body `{"error":"unauthorized"}`,
  logged as `web.unauthorized {path, remote}` (never the token itself).
  A WebSocket upgrade or an SSE stream refused this way never reaches
  the contract below it — the client sees an ordinary HTTP 401, not a
  `status`/`error` frame.
- `GET /api/hello` answers `{"name":"sheepdog","version":"…","voice_name":"…","auth":"token"|"none","ws":"/ws","audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"},"voice_provider":"gemini"|"openai"|"off","voice_model":"…","protocol":1,"min_protocol":1}`
  so a client can confirm it found a sheepdog daemon, whether it needs a
  token, this contract's audio format, the voice model behind it and the
  `/ws` protocol (*Protocol version and handshake*) before it ever opens
  `/ws`.

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
  last "New conversation" reset, below, or daemon start), and `update`,
  the update notice (omitted when the daemon does not track updates):
  `{current, latest, available, how, url, restart_pending, restart_to,
  auto_restart}`. `available`: `latest` is a newer `v*` release than
  `current` (never for a development build); `how` is the upgrade command
  for the install method; `restart_pending`: the program on disk is not the
  one running ("update installed, restart to finish"), `restart_to` its
  version. Display only: the voice never speaks it
  ([`update.md`](update.md)).
- **GPT-Live is billed per second of open session**, so the daemon opens
  one only while a client is attached (`/ws` connected) and unmuted and
  there is speech (a frame loud enough) or a push to say, and closes it
  after `voice.openai.idle_close_s` (default 45 s) without a transcript,
  a reply, a delegation or a push (`voice.session_open {reason}` /
  `voice.session_close {reason, dur_ms, billed_s}`;
  `web.client.stats` carries `voice_billed_seconds` and
  `voice_session_open`). The first words after a standby are buffered
  (about 0.5 s of pre-roll plus the ~0.6–0.9 s the session takes to
  open) and replayed, so nothing the user says is lost; a client
  needs no special handling beyond showing `standby` as "ready".
  Disconnecting, muting or letting the silence gate close all let the
  idle timer run.

Every comparison is constant-time (`crypto/subtle.ConstantTimeCompare`);
there is no rate limiting or lockout on a wrong token.

## Settings

`GET /api/settings` (token-protected; also the `settings` field of
`GET /api/state`) →
`{"chattiness":"normal","levels":["quiet","normal","chatty"],"languages":["zh-Hans","en"],"language_options":[{"code":"en","name":"English","native":"English","providers":["gemini","openai"]},{"code":"zh-Hans","name":"Chinese (Simplified)","native":"简体中文","providers":["gemini","openai"]},…]}`.

- `languages` is the user's language preference, most preferred
  first (`[]` when none is set). `language_options` is the catalog a
  client offers, in display order (English, 简体中文, 繁體中文, then
  alphabetical); `providers` lists the voice providers that support each
  entry (today every entry is supported by both;
  [`design/language-preference.md`](design/language-preference.md)).
- `PUT /api/settings` takes either field or both; a field left out is
  unchanged (`{"chattiness":"quiet"}` keeps the languages,
  `{"languages":["zh-Hans","en"]}` keeps the level, `{"languages":[]}`
  clears the preference). Codes match case-insensitively and come back
  in catalog spelling, duplicates dropped. It answers with the GET shape;
  `400 {"error":…}` for an unknown level or language code (nothing is
  changed). The choice is kept in the state file (`session.chattiness`,
  `session.languages`) and logged as `supervisor.settings`.
- A changed language preference is information for the voice and the orchestrator: the
  orchestrator's state block gets it on its next call, the running voice
  session gets it at once as silent context (`supervisor.languages`,
  then a `voice.push`), and every new voice session has it as the last
  line of its system instruction (`voice.system_prompt` carries
  `language`). Nothing in the daemon picks a language for the models.

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
  - `shepherd` — what the user said (voice transcript fragments
    merged per turn, `via: "voice"`) or typed (`via: "typed"`);
  - `dog` — what the voice said (output transcript merged per turn, or the
    spoken reply with `--voice=off`);
  - `push` — a push the voice spoke (never a silent one), with `urgency`
    and the `agent` it is about;
  - `approval` — a worker's approval prompt: `text` its summary,
    `agent`, `pending_id`, `options` `[{index, label, keys, default}]`,
    `answered`, `answer` and `via` (`tap`, `fast_path`, `brain`) once
    answered, `resolution` `open` | `confirmed` | `expired` |
    `cancelled` | `replaced`;
  - `card` — a card the orchestrator showed (*Cards*): `text` its title,
    `card_id`, `agent`, and `card`;
  - `notice` — a divider: the daemon started, the voice reconnected (with
    or without its context), the voice session closed, or (`subkind:
    "reset"`, `text: "New conversation"`) a "New conversation" reset.

  `id` is the first event's id, so items sort and page with the SSE
  stream's ids.
- **New conversation.** `POST /api/conversation/reset {"via":"app"|
  "debug"|"voice_page"}` resets the orchestrator's transcript window (a fresh
  segment; the compaction summary is gone) and the voice model's own
  session (Gemini: a fresh reconnect with no resumption handle;
  GPT-Live: an open billed session closes now, no recap, and reopens
  later) — the roster, its ledger, open approval prompts, promises,
  notes, investigations and the event log itself are untouched
  (docs/dev/roles.md "New conversation"). It answers `{"ok":true,
  "reset_id":"<id>","at":"<RFC3339>"}`, logs `conversation.reset {via,
  reset_id, at}` (so the SSE stream and a reconnecting client's replay
  both carry it), and adds the `notice`/`reset` item above to
  `GET /api/conversation`. The same reset is reachable by a bare voice
  phrase ("start over", "new conversation", "start from scratch", "reset
  the conversation", "clear the conversation"): the voice asks "Start a
  new conversation? Say \"yes, start over\" to confirm.", and only the
  exact confirm phrase ("yes, start over" / "confirm reset"), from real
  user input, within 20 s, carries it out — a bare "yes" keeps its
  ordinary approval-prompt meaning, and the fresh session then says one
  short line ("Fresh start. What's next?").
- **Approve by tap.** `GET /api/state`'s `pending_action` (and the
  `pending.created` event) carries `agent`, `summary`, `question`,
  `command` and `options: [{index, label, keys, default}]`.
  `POST /api/pending/answer {"id": "<pending id>", "option": <index>}`
  answers it: the tap is recorded as a user input and passes the same
  rail as a spoken answer (docs/dev/roles.md "The one rail"), the option's
  keys go to the worker, and the answer is `{"ok": true, "spoken":
  "Told auth Yes."}`; the voice is told silently ("The user tapped
  Yes on auth's question"). A question that is no longer open (answered,
  replaced, or expired and gone from the screen) is `409
  {"error":"that question is no longer open"}`; an option the question
  does not have is `400`.

## Agent window: screen and input

Not part of `/ws`: two token-authenticated JSON routes let the app show an
agent's live terminal window and operate it. Both serve only the agents
the daemon tracks (the rows of `GET /api/agents`); any other pane id is
`404 {"error":"no such agent"}`, so the user's other herdr panes are never
reachable. A pane id with `:` or `%` is path-escaped as usual.

- **`GET /api/agents/{pane_id}/screen?lines=N&hash=H`** — the agent's
  current screen: the same herdr read the orchestrator's `read_screen` uses
  (`pane.read`, source `recent-unwrapped`, ANSI stripped; no model is
  involved). `lines` defaults to 120 and is capped at 500. The answer is

  ```jsonc
  {"pane_id": "w1:p2", "alias": "auth", "spoken": "Auth fix", "kind": "codex",
   "state": "working", "state_since": "…", "text": "…", "lines": 120,
   "truncated": true, "hash": "9f2c0e1a44b7d310", "unchanged": false}
  ```

  `hash` is a fingerprint of `state` and `text`. A client that polls
  passes the last `hash` back as `?hash=`; when nothing changed the
  answer is `{"pane_id", "alias", "spoken", "kind", "state", "state_since",
  "lines", "hash", "unchanged": true}` without `text`, so an idle window
  costs a few bytes. A client whose daemon lists the `screen` feature
  streams the window over `/ws` instead (*Agent window over /ws*); this
  route stays for older daemons and for one-off reads, where polling
  every 1–2 s is the intended use. A read herdr refuses is `502`. Reading a window does not
  make the agent the conversation's subject: looking is not talking.
- **`POST /api/agents/{pane_id}/input`** — the user operating the window
  himself. Exactly one of:
  - `{"text": "…", "submit": true}` — a prompt, sent as one submission
    (`agent.prompt`, text + Enter; `submit` defaults to true, `false` is
    `400`). Text is trimmed, non-empty, at most 64 KB (a pasted log or
    file reaches the agent whole). The agent's `current_task`, and the
    orchestrator's note below, get its first 600 bytes. An agent waiting at an approval or
    question prompt refuses a prompt: `409` with the reason.
  - `{"keys": ["enter"]}` — up to 8 keys from the allow-list `enter`,
    `esc`, `ctrl+c`, `1`…`9`, `up`, `down`, `tab` (anything else is
    `400`), sent as one `pane.send_keys`.

  The answer is `{"ok": true, "kind": "prompt"|"keys"|"answer", "spoken":
  "…"}`. The authority is the user's own tap, so this does not pass the
  orchestrator's approval rail (docs/dev/roles.md "The one rail"); instead every
  input is logged as `web.agent_input {kind, len}` (a prompt's length,
  never its text) or `{kind, keys}`, and the orchestrator is told in a
  transcript line ("the user typed a prompt in Auth fix's window: …", "the
  User pressed esc in Auth fix's window") so its picture stays right. When
  the keys select one option of the open approval question for that
  agent (the option's own keys or the digit it is listed under), they answer
  it through the tap path (`POST /api/pending/answer`'s
  code): `kind` is `answer`, the pending action is confirmed and cleared
  (`pending.confirmed {via: "tap"}`), and the voice and orchestrator hear it the
  way they hear a tap. `enter` (and every other key) goes to the pane
  as it is: the user may have moved the cursor with `up`/`down`, so the
  recorded default is not necessarily what it selects; the transition
  off `blocked` closes the question. A herdr send that fails is `502`.

## Agent window over /ws

Feature `screen`. The same window as `GET /api/agents/{pane_id}/screen`,
pushed over the `/ws` connection the app already holds instead of
polled. The client asks for one agent's window; the daemon sends a
frame at once and then whenever the window changes.

- **`screen-subscribe {pane_id, lines?}`** starts the subscription;
  `lines` is as on the route (default 120, cap 500). **At most one per
  client**: a second subscribe replaces the first (to switch agents, or
  to get a fresh frame at once). Only agents the daemon tracks, as on
  the route.
- **`screen-unsubscribe`** ends it. Closing the window sends it; the
  connection ending (closed, replaced, refused, daemon shutdown) ends
  it too, and a reconnecting client subscribes again: nothing carries
  over to the next connection, nor to a client that replaces this one.
- **`screen {screen}`**: `screen` is the route's body without
  `unchanged`: `pane_id`, `alias`, `spoken`, `kind`, `state`,
  `state_since`, `text`, `lines`, `truncated`, `hash`. The first frame
  comes right after the subscribe; after that a frame is sent only when
  `hash` (state and text) changed, so an idle window sends nothing.

  ```json
  {"type":"screen","screen":{"pane_id":"w1:p2","alias":"auth","spoken":"Auth fix","kind":"codex","state":"working","state_since":"2026-10-05T09:12:03Z","text":"› run the tests\n• Running go test ./...","lines":200,"truncated":true,"hash":"9f2c0e1a44b7d310"}}
  ```

- **`screen-error {pane_id, message, ended?}`**: `ended: true` when the
  subscription is over — the pane is not (or no longer) a tracked agent
  (`no such agent`), or the daemon has no supervisor. Otherwise a read
  failed (herdr down): it is sent once per run of failures, reads go on,
  and the next good read sends a full `screen` frame even if nothing
  changed, so the client can clear its error.

  ```json
  {"type":"screen-error","pane_id":"w1:p9","message":"no such agent","ended":true}
  {"type":"screen-error","pane_id":"w1:p2","message":"herdr: pane.read: read: EOF"}
  ```

- **When the daemon reads.** herdr offers no plain "output changed"
  event to wait on (`events.wait` refuses output matches, herdr protocol
  22; a catch-all `pane.output_matched` subscription was not evaluated),
  so the daemon reads the subscribed window itself, the same `pane.read` the route does: every
  **1 s**, and sooner when the event log shows the watcher's debounced
  `herdr.transition` or a `herdr.send` for that pane (a state change,
  or input the user, the orchestrator or a tap sent it). **Rate cap:** two
  reads are never less than **250 ms** apart, so at most four frames a
  second and usually one. With one `/ws` client and one subscription
  per client, the daemon reads at most one window this way, however
  the app is used.
- **Fallback.** A client that does not see `screen` in `bot-ready`
  (an older daemon, a legacy connection, no `/ws` connection at the
  moment) polls the route as before. A subscribed client that loses its
  `/ws` connection polls until it is back, then subscribes again.
- Logged as `web.screen.subscribe {client_id, lines}` and
  `web.screen.end {client_id, reason, frames, reads, failures}` with the
  pane id; `reason` is `unsubscribe`, `resubscribe`, `gone`, `closed`
  (replaced, refused or shut down) or `disconnect`. Each read logs
  `herdr.read` as the route's does.

## Cards

Feature `cards`. The orchestrator's `show` tool puts something on the
user's screen instead of saying it — a list, a table, steps, a diff,
an agent's window, a screen excerpt, an open approval — and the voice
says one short sentence ("it's on your screen") with the gist. It is
display only: a card changes nothing, and an approval card carries no
way to answer (the user answers through the existing approval card,
`POST /api/pending/answer`, docs/dev/roles.md "The one rail").

- **`visibility {visible, voice_screen}`** (client → server): the client
  says whether its screen can be seen. The iOS app sends it on every
  change of its scene phase and of the screen in front; `/voice` sends
  it on `visibilitychange` (its page is the voice screen). The daemon
  shows a card only while the **attached** client's last report was
  `visible: true` and `voice_screen: true`. A client starts not
  visible, and a replaced or disconnected client stops counting, so a
  client that never reports (an older app or page) never gets a card:
  the tool then answers "not visible" and the orchestrator speaks.
  Logged as `web.client.visibility {client_id, visible, voice_screen}`
  on a change.
- **`card {card}`** (server → client): `card` is `card_id`, `kind`,
  `at` (RFC 3339) and `title`, plus by kind:

  | `kind` | Fields | Show it as |
  |---|---|---|
  | `card` | `markdown` (at most 6 000 bytes; a longer one is cut and ends `…(cut)`) | rendered markdown: headings, lists, tables, code fences |
  | `agent` | `agent` (alias), `spoken`, `pane_id` | a way into that agent's window (*Agent window*) |
  | `excerpt` | `agent`, `spoken`, `pane_id`, `text` (the last lines of its screen, verbatim, read by the daemon) | monospace text |
  | `approval` | `agent`, `spoken`, `pane_id`, `pending_id`, `question`, `command`, `options` (as `pending_action`'s) | the question, read-only, pointing at the approval card that answers it |

  A client shows a new card once by itself (a sheet), keeps it in the
  conversation as a row that opens it again, and never pops up a card
  it got from history or replay.

  ```json
  {"type":"card","card":{"card_id":"cmh2x1","kind":"card","at":"2026-10-20T09:12:03Z","title":"Failing tests","markdown":"| Test | Error |\n|---|---|\n| TestLogin | timeout |"}}
  {"type":"card","card":{"card_id":"cmh2x2","kind":"approval","at":"2026-10-20T09:13:10Z","title":"Auth fix is asking","agent":"auth","spoken":"Auth fix","pane_id":"w1:p2","pending_id":"p7","question":"to run the migration","command":"make migrate","options":[{"index":1,"label":"Yes","keys":["y"],"default":true},{"index":2,"label":"No","keys":["n"],"default":false}]}}
  ```

- **History.** The card is stored in `<data_dir>/cards.jsonl` (the
  newest 500 are kept) and logged as `card.shown {card_id, kind,
  agent, turn, title_chars, bytes, options}` with the pane id: metadata
  only. `GET /api/conversation` has a `card` item at that point: `text`
  is the title, `card_id`, `agent`, and `card` (the card as above;
  absent, with a text that says so, once the store no longer has it).
  A show refused because no screen was visible logs `card.refused
  {kind, turn, reason}` (`not_visible`, or `not_delivered` when the
  client went away between the check and the send).

## Phone notifications

Not part of `/ws`: the iOS app registers its APNs device token over
`POST /api/push/register` and the daemon sends system notifications for
events that need the user, whether or not a `/ws` client is attached.
The contract (routes, payload, when an alert is sent) is in
[`push.md`](push.md).

## Non-goals

No compression, no multiple clients, no more than one agent window per client, no client-side resampling contract
beyond "send 16 kHz PCM16 mono", no timestamps in frames. No TLS: the
`Secure` cookie flag stays off and the token travels in the clear, so a
non-loopback bind should be a private network (a LAN you trust, or a
Tailscale address) rather than an open one.
