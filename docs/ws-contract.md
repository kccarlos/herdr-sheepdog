# Browser ↔ daemon WebSocket contract

Written in WP2 (2026-09-17). The Go types for every control message live in
`internal/voice/wsproto`; the browser page (WP6) and the WS server (WP5/WP7)
are both built from this document. Change the doc and the Go types together.

## Endpoint

- `GET /ws` on the daemon's HTTP port, upgraded to a WebSocket. Same origin as
  `/voice`; no auth in v1 (localhost only, plan §1).
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
| `{"type":"cancel"}`                  | The "stop" button. The daemon stops current playback (it will send `flush`) and invokes the `cancel` tool path in the supervisor. Equivalent to the user saying "stop". |
| `{"type":"text","text":"…"}`         | Simulate an utterance: the text is fed to the voice session as if the user had spoken it (`/debug` and the `/voice` text box use this). Without a voice session (`--voice=off`) the daemon echoes it as an `in` transcript and answers through its text router with one `out` transcript (a text routed to `cancel` yields `flush` then the `out`), or with an `error` when the text cannot be routed. |
| `{"type":"ping"}`                    | Liveness probe; the daemon answers `pong`. Clients should send one every 15–30 s while idle.               |

Unknown `type` values are answered with an `error` message and otherwise
ignored; the connection stays open.

## Server → client control messages

| Message                                                                  | Meaning                                                                                                         |
|--------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------|
| `{"type":"flush"}`                                                       | **Drop the playback buffer now.** Sent when the model was interrupted (barge-in, `cancel`, an interrupting push). Audio frames received before this message must not be played; frames received after it start a new utterance. |
| `{"type":"status","voice":"connecting\|connected\|reconnecting\|closed","muted":bool,"reason":"…"?}` | Voice session state. Sent once on connect and on every change. `reason` is set for `closed` (e.g. `"replaced"`, `"shutdown"`, `"error"`). `muted` echoes the daemon's view of the mute state. |
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

## Non-goals (v1)

No authentication, no compression, no multiple clients, no client-side
resampling contract beyond "send 16 kHz PCM16 mono", no timestamps in frames.
