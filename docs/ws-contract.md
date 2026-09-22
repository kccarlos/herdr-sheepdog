# Browser ↔ daemon WebSocket contract

Written in WP2 (2026-09-17). The Go types for every control message live in
`internal/voice/wsproto`; the browser page (WP6) and the WS server (WP5/WP7)
are both built from this document. Change the doc and the Go types together.

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
| `{"type":"cancel"}`                  | The "stop" button. The daemon stops current playback (it will send `flush`) and invokes the `cancel` tool path in the supervisor. Equivalent to the user saying "stop". |
| `{"type":"text","text":"…"}`         | Simulate an utterance: the text is fed to the voice session as if the user had spoken it (`/debug` and the `/voice` text box use this). Without a voice session (`--voice=off`) the daemon echoes it as an `in` transcript and answers through its text router with one `out` transcript (a text routed to `cancel` yields `flush` then the `out`), or with an `error` when the text cannot be routed. |
| `{"type":"ping","gated_frames":N?,"peak":N?}` | Liveness probe; the daemon answers `pong`. Clients should send one every 15–30 s while idle. The optional counters (frames withheld by the silence gate so far; last input peak in Int16 LSB) are copied into the next `web.client.stats` event. |

Unknown `type` values are answered with an `error` message and otherwise
ignored; the connection stays open.

## Server → client control messages

| Message                                                                  | Meaning                                                                                                         |
|--------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------|
| `{"type":"flush"}`                                                       | **Drop the playback buffer now.** Sent when the model was interrupted (barge-in, `cancel`, an interrupting push). Audio frames received before this message must not be played; frames received after it start a new utterance. |
| `{"type":"status","voice":"connecting\|connected\|reconnecting\|closed","muted":bool,"reason":"…"?,"stalled":true?}` | Voice session state. Sent once on connect and on every change. `reason` is set for `closed` (e.g. `"replaced"`, `"shutdown"`, `"error"`). `muted` echoes the daemon's view of the mute state. `stalled` is present (true) while the model has not reacted to the user's last utterance for `gemini.stall_after_s`; the next status without it means the model answered or the connection was recycled. |
| `{"type":"transcript","dir":"in\|out","text":"…","final":bool}`          | Incremental transcription. `in` = what the model heard from the user, `out` = what the model is saying. Fragments of one utterance arrive in order; `final:true` closes it. Output fragments may never carry `final:true`; treat the next `in` transcript or a `flush` as the boundary. |
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
`docs/e2e-results.md`: turns answered only after 4–35 s, idle pushes
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
  `on:true` it tells the model the audio stream paused (Gemini
  `audioStreamEnd`, the same signal `mute` sends) so the server VAD closes
  the user's turn instead of waiting for frames that will not come. The next
  binary frame reopens the stream on the model side.
- `web.client.stats` reports `silent` (gate closed right now),
  `silence_transitions`, and the ping-carried `gated_frames` and `peak`.
- The thresholds are the client's; `/voice` takes `?gate=off` and
  `?gate_floor=LSB&gate_hold=ms&gate_preroll=ms`. A client that never sends
  `silence` is treated as always open, and a reconnecting client starts
  open (send `silence` right after connecting if the gate is closed).

`mute` and `silence` are independent: a muted client sends neither frames
nor `silence` transitions; the gate resumes its own bookkeeping on unmute.

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
  attached is **dropped**, not queued (the conversation is real time). A
  pending `flush` is not replayed.
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

Added when `web.bind`/`web.token` (CONFIG.md "web.token") let the daemon
be reached from a phone on the LAN or over Tailscale, not just
localhost. With `web.token` unset (the loopback default) every endpoint
below is open, as in the original v1 contract. With it set:

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
- `GET /api/hello` answers `{"name":"sheepdog","version":"…","voice_name":"…","auth":"token"|"none","ws":"/ws","audio":{"in_hz":16000,"out_hz":24000,"format":"pcm16le mono"}}`
  so a client can confirm it found a sheepdog daemon, whether it needs a
  token, and this contract's audio format before it ever opens `/ws`.

Every comparison is constant-time (`crypto/subtle.ConstantTimeCompare`);
there is no rate limiting or lockout on a wrong token.

## Non-goals (v1)

No compression, no multiple clients, no client-side resampling contract
beyond "send 16 kHz PCM16 mono", no timestamps in frames. No TLS: the
`Secure` cookie flag stays off and the token travels in the clear, so a
non-loopback bind should be a private network (a LAN you trust, or a
Tailscale address) rather than an open one.
