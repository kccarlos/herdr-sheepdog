# Audio end-to-end harness and results

`sheepdog audio-e2e` (internal/audioe2e) speaks the scenarios in
`testdata/audio-e2e/scenarios.json` into a scratch daemon's `/ws`:
speech from macOS `say -v Samantha` converted with `afconvert -f WAVE -d
LEI16@16000 -c 1`, a generated noise bed where the scenario asks for one
(white, pink, brown or a café bed: pink noise under a slow swell, babble
bursts and cup clinks), 40 ms frames at real-time pace behind the apps'
silence gate (peak < 8 LSB for 300 ms closes it, 100 ms pre-roll on
reopening). It records every `/ws` message, the audio byte counts and the
`/events` stream, and checks per scenario: the input transcript (fuzzy
word match), the tool call, a spoken reply (audio bytes and an out
transcript), latencies from the end of speech, no transcript during pure
noise, the stop and cancel paths, an approval authorised by the
transcript (a mock herdr block injected), and GPT-Live's idle close.

```sh
printf '' > /tmp/sd-empty.toml
SHEEPDOG_DATA_DIR=/tmp/sd-e2e SHEEPDOG_STATE_PATH=/tmp/sd-e2e/state.json \
SHEEPDOG_VOICE_OPENAI_IDLE_CLOSE_S=5 \
  sheepdog serve --config /tmp/sd-empty.toml --port 7799 --herdr=mock --cli=off --brain=fake --voice=gemini   # or --voice=openai
sheepdog audio-e2e --url http://127.0.0.1:7799 --json results.json --save audio/
# --audio-timeline additionally records every server→client audio frame's
# arrival offset and size in results.json (audio_chunks), for an offline
# jitter-buffer replay (see "GPT-Live playback gaps" below).
```

The audio fixtures are not committed: the harness renders and caches
them (`--cache`, default `$TMPDIR/sheepdog-audio-e2e`), so only
`scenarios.json` lives in the repo.

Latency columns: end of the last speech frame → first input transcript /
tool call / first reply audio / first output transcript, in ms. "–":
never, not measurable (the audio started before the speech, as with an
announcement), or — in these runs, before the harness printed negative
values — negative: the streaming transcript arrived before the speech
ended.

## GPT-Live playback gaps (2026-09-23/24)

The shepherd heard gaps mid-word on GPT-Live with nothing dropped. The
instrumentation added for it stays: `voice.audio_timing` per model turn
(`chunks`, `audio_ms`, `wall_ms`, `max_gap_ms`, `p95_gap_ms`,
`realtime_ratio`), `send_wait_*` in `web.client.stats`, and the page's
`underruns` / `drains` / `buffer_ms` on its ping. Every GPT-Live turn ran
behind real time (`realtime_ratio` 0.74–0.87, gaps up to 1.46 s) while
Gemini ran about 4× ahead; the daemon's forwarding added 0 ms. The
browser got an adaptive jitter buffer with a cross-fade and a
pitch-preserving time-stretch, per provider (`docs/ws-contract.md`
"Playback jitter buffer" has the numbers the iOS app mirrors). The
recorded timelines are fixtures (`web/voice/testdata/gptlive-*.json`)
replayed through the real `Player` by
`web/voice/playback-timeline.test.mjs`. The root cause was on the input
side, fixed below.

## GPT-Live input pacing (2026-09-25): output now runs at real time

**Hypothesis** (`docs/research/voice-frameworks-2026-09-24.md` P1): the
daemon's keep-alive sent one 40 ms tick of silence only when two ticks
had passed since the last send, so while the client's gate was closed —
exactly while the model replies — GPT-Live's input timeline advanced at
≤ 0.5× real time. The docs require "a continuous microphone stream paced
at its recorded sample rate", and the session timeline advances with
input.

**Fix** (`internal/voice/gptlive/pace.go`, `loop.go` `keepAlive`): each
connection counts the input audio it sent against the wall clock since
its first send; every tick, while the client is not streaming (no client
frame for 3 ticks), keepAlive sends the owed silence (at most 200 ms at
once; debt beyond that is forgiven and logged as
`voice.input_debt_dropped {ms}`). The pre-roll and the frames buffered
while the session opened are replayed as audio from the past (they move
the anchor back instead of counting as lead), and a lead over 2 s (a
client clock running fast) is forgiven. Unit tests on a fake clock: `internal/voice/gptlive` `TestKeepAlive*`.

Silence rather than `session.input_audio.mute`: the WebSocket guide says
only that muting "does not cancel backend work or stop generated speech"
— not that the timeline advances while muted and nothing is sent — while
it does require a continuous, real-time-paced stream. LiveKit can mute
because its client streams continuously; ours gates, so the daemon has to
carry the timeline, and paced silence is the path the docs describe.
Whether a server-side mute changes billing is still unknown.

**Measurement** — one run, scratch daemon on port 7799 (empty config,
scratch `SHEEPDOG_DATA_DIR`/`SHEEPDOG_STATE_PATH`, `--voice=openai
--brain=fake --herdr=mock --cli=off`, `idle_close_s=5`), `sheepdog
audio-e2e --audio-timeline` with a scratch scenario ("Hi! I'm testing my
speakers. Could you please count slowly from one to twenty for me?"):
the model answered itself ("Sure. One. Two. … Seventeen.", no tool call)
while the harness's gate was closed.

| | Measured | realtime_ratio | max_gap_ms | p95_gap_ms |
|---|---|---|---|---|
| Before (2026-09-23) | 5 replies, 2 scenarios (relayed commentary) | 0.74–0.87 | 626–1464 | 574–1421 |
| After (2026-09-25) | 1 self-answered reply, 5 `audio_timing` segments split by the model's pauses | **1.00, 0.98, 1.00, 1.06, 0.94** | 171, 233, 88, 97, 313 | 141, 209, 61, 97, 158 |

`voice.audio_timing` per turn after the fix: 9000 ms of audio in 8984 ms,
2100/2152, 22100/22000, 1200/1129, 4240/4513. No
`voice.input_debt_dropped`. Client counters (`web.client.stats`):
`dropped_audio 0`, `send_wait_max_ms 0.1`, `queued 0`; EOS → first
reply audio 1248 ms, first output transcript 1017 ms. **Billed: 42 s**
(one session, closed `idle`, confirmed by `session.closed`).

The hypothesis holds: with the input paced at real time, GPT-Live's
output runs at real time and its mid-reply gaps fall from 0.6–1.5 s to
under ~320 ms. Caveat: the before replies were relayed supervisor
commentary (`commentary.append`), this one the model's own answer (no
tool call); the fix is upstream of both, but the commentary path was not
re-measured within the 60 s budget.

**Start buffer lowered to 400 ms** (`PLAYBACK_POLICY.openai`, cap still
2000 ms, time-stretch unchanged). The run's forwarded-audio timeline is
committed as `web/voice/testdata/gptlive-paced-long-reply.json`; replayed
through the `Player`, 400 ms and the old 1200 ms both see exactly one
underrun, at the same place — a pause the model made mid-count, where the
daemon's `PauseHold` drops the silence and leaves an 869 ms hole in the
forwarded stream — not inside a word (800 ms happens to bridge it). The
older, pre-pacing recordings keep replaying under the 1200 ms policy they
were tuned for. The first GPT-Live word now plays about 0.8 s sooner.

## Worth knowing (2026-09-23 runs)

- Gemini: a short spoken "stop" over a reply interrupted the model but
  was transcribed only with the next utterance; the stop button works,
  and on GPT-Live the spoken stop works.
- Both providers heard the TTS alias "auth" as "off" with no `[aliases]`
  configured; configured aliases go into Gemini's custom vocabulary and
  GPT-Live's prompt.
- The harness waits for 1.5 s of quiet (at most 12 s) before each
  scenario, so a late reply is not scored against the next one.
