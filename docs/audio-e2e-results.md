# Audio end-to-end results (2026-09-23)

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
# jitter-buffer replay (see "Audible gaps in GPT-Live playback" below).
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

## Audible gaps in GPT-Live playback (fix, 2026-09-23)

**Symptom:** the shepherd heard tiny gaps mid-sentence, even mid-word, in
the browser `/voice` page on GPT-Live (`internal/voice/gptlive`), with
`dropped_audio: 0` in the daemon logs (so nothing was being discarded)
and no network involved (`localhost`).

**Measured, before guessing at a fix.** Instrumentation was added first:
`voice.audio_timing` (per model turn: `chunks`, `bytes`, `audio_ms`,
`wall_ms`, `max_gap_ms`, `p95_gap_ms`, `realtime_ratio`) in both
`internal/voice/gptlive` and `internal/voice/gemini`'s read loops
(`internal/voice/audiotiming`, shared by both), and `send_wait_max_ms` /
`send_wait_p95_ms` in `web.client.stats` for how long an audio frame
waits in the per-client WS queue (`internal/web/ws.go`) before it is
written. The browser's playback worklet now reports `underruns`,
`drains` and `buffer_ms` on its `ping` too (`docs/ws-contract.md`).

A live run against a scratch daemon (port 7799, never 7777) with
`sheepdog audio-e2e --audio-timeline` (new flag: records every
server→client binary frame's arrival offset and byte length, the exact
input to the jitter-buffer simulation below) gave:

| Provider | Scenario | Turn | chunks | audio_ms | wall_ms | max_gap_ms | p95_gap_ms | realtime_ratio |
|---|---|---|---|---|---|---|---|---|
| GPT-Live | status-question | 1 | 38 | 3800 | 5121 | 1463.7 | 1421.1 | 0.74 |
| GPT-Live | status-question | 2 | 32 | 3200 | 4133 | 1032.6 | 1032.5 | 0.77 |
| GPT-Live | instruction-promise | 3 | 36 | 3600 | 4126 | 626.4 | 574.1 | 0.87 |
| GPT-Live | instruction-promise | 4 | 21 | 2100 | 2823 | 822.7 | 777.1 | 0.74 |
| GPT-Live | instruction-promise | 5 | 33 | 3300 | 4194 | 994.2 | 913.7 | 0.79 |
| Gemini Live | status-question | 1 | 5 | 1440 | 350 | 72.9 | 72.9 | 4.12 |
| Gemini Live | status-question | 2 | 9 | 1630 | 349 | 0 | 0 | 4.67 |

Every GPT-Live turn ran **behind** real time (`realtime_ratio` 0.74–0.87:
the model delivers less audio than the playback clock consumes) with
gaps up to **1.46 s**; Gemini ran **ahead** (ratio ~4×, gaps under 73 ms)
on the same harness. `send_wait_max_ms`/`send_wait_p95_ms` were **0 ms**
in every `web.client.stats` line of both runs: the daemon's forwarding
path (`internal/web/ws.go`'s per-client queue, the `gptlive`/`gemini`
read loops → unbuffered `out` channel → `Server.Run` → `hub.sendAudio` →
`enqueue`) adds no measurable delay — audio frames are never queued
behind control traffic and are written as soon as they arrive. **Root
cause: GPT-Live's own audio generation, not the daemon.** The browser's
fixed 150 ms prebuffer cannot absorb a source that is, on sustained
average, slower than real time; no daemon-side fix changes that.

**Live-call ledger** (scratch daemon, port 7799; keys from the main
checkout's `.env`; every session confirmed closed by the daemon, none
left open):

| Provider | Scenarios | Sessions | Billed |
|---|---|---|---|
| GPT-Live (`gpt-live-1`, `idle_close_s=5`) | status-question, instruction-promise | 2 (both `reason: idle`, `confirmed: true`) | 16 s (budget: ≤ 60 s) |
| Gemini Live (`gemini-3.8-live`) | status-question | 1 | not billed per-second the same way; 1 of the budgeted ≤ 3 sessions |

**Fix: an adaptive jitter buffer with a cross-fade, in the browser**
(`web/voice/audio-util.js`'s `Player`, per `docs/ws-contract.md`
"Playback jitter buffer"): starts at 150 ms; a mid-utterance underrun
raises the target by 100 ms (capped 500 ms) and the next resume waits
for the *raised* target to refill rather than playing the first new
chunk; a clean utterance (no underrun) decays the target by 20 ms back
toward the 150 ms floor; every start or resume ramps in over 8 ms so a
seam never clicks. No daemon change was needed (send-wait was already
~0 ms) beyond the new instrumentation.

**Offline replay, real recordings.** The two GPT-Live timelines above
were committed as fixtures (`web/voice/testdata/gptlive-*.json`) and
replayed through the real `Player` in Node
(`web/voice/playback-timeline.test.mjs`), chained into one session (a
real conversation does not reset the buffer between utterances):

| Buffer policy | Underruns (57 chunks, both utterances chained) |
|---|---|
| Fixed 150 ms (the old default) | 10 |
| Fixed 250 ms | 6 |
| Fixed 400 ms | 3 |
| **Adaptive 150→500 ms (shipped)** | **4** |

The adaptive policy cuts underruns by more than half versus the old
fixed 150 ms buffer, and every remaining one is cross-faded instead of
clicking (`audio-util.test.mjs`, "ramps in over crossFadeLen samples").
It does **not** reach zero on this recording: honestly, a fixed 400 ms
buffer alone did slightly better here (3 vs. 4), because a few GPT-Live
gaps (up to 1.46 s) and the sustained sub-real-time delivery on longer
replies outrun even the 500 ms cap. Recommendation, if this remains
audible in practice: raise `maxTarget` (the 500 ms cap) and/or
`adaptStep` (the 100 ms-per-underrun rise) for the GPT-Live provider
specifically — the shipped numbers match the cross-platform contract
another agent is implementing for the iOS app in parallel
(`docs/ws-contract.md`), so this PR does not change the default; a
provider-aware override would be a follow-up. The cross-fade is the part
that reliably removes the *audible click*; the buffer size only trades
off *how often* a brief pause happens against added latency.

Added latency: none in the common case (the target only rises after an
underrun already happened, and decays back down when it is not needed);
worst case with the target pinned at the 500 ms cap, first-audio latency
is 500 ms − 150 ms = 350 ms more than the old fixed buffer.

### Follow-up: provider policy and adaptive time-stretch (2026-09-24)

In the field the phone hit 84 underruns in 8 minutes on GPT-Live with the
buffer pinned at its 500 ms cap. The recommended follow-up shipped as a
per-provider policy plus a pitch-preserving WSOLA time-stretch
(`docs/ws-contract.md` "Playback jitter buffer" has the exact numbers the
iOS app mirrors): GPT-Live starts at 800 ms and adapts up to 2000 ms;
below the target the page plays slower than real time (down to 0.85×,
pitch kept), and above twice the target it catches up at 1.05× — only
while paying back the lag the slow-down added this reply (the iOS app's
lag-debt rule; a Gemini-like 4× source plays at 1.0 for 98.6 % of the
time in `stretch.test.mjs`'s replay, 145 ms of 10 s stretched). The
stretcher costs about 0.2 % of one core (60 s of audio in 113 ms in Node).

Offline replay through the real `Player`
(`web/voice/playback-timeline.test.mjs`; "before" is the shipped
Gemini-policy adaptive buffer, which every provider got until now):

| Timeline (arrival ratio) | Before: 150→500 ms, no stretch | 800→2000 ms, no stretch | **After: 800→2000 ms + stretch** | Played at r ≠ 1 (`stretched_ms`) |
|---|---|---|---|---|
| status-question (0.78×) | 3 | 0 | **0** | 2.9 s |
| instruction-promise (0.69×) | 2 | 1 | **1** | 3.3 s |
| both chained | 4 | 1 | **1** | 6.2 s |
| synthetic 20 s reply from the same gaps (0.73×) | 9 | 5 | **4** | 22.4 s |
| synthetic 60 s reply from the same gaps (0.73×) | 23 | 14 | **11** | 66.8 s |

Near zero on the recorded replies, not on long ones: most of the gain is
the deeper GPT-Live buffer, and the stretch adds a further cut. A reply
that arrives at 0.69–0.73× outruns a 0.85× floor by 12–16 % for as long
as it lasts, so a long reply still runs dry once the 2 s cap is spent;
only a lower floor (audibly slow speech) or faster delivery from the
provider removes that. Cost: first audio of a GPT-Live reply comes about
0.8 s after its first chunk (was ~0.1–0.2 s).

## GPT-Live settings check (2026-09-24): no pacing or turn-detection option

Read for this check: the GPT-Live guide
(https://developers.openai.com/api/docs/guides/live), the primary
WebSocket reference
(https://developers.openai.com/api/reference/resources/live/primary-websocket),
the WebSocket connection guide
(https://developers.openai.com/api/docs/guides/voice-websockets?api=live),
the conversations guide
(https://developers.openai.com/api/docs/guides/live-conversations), the
server-controls guide
(https://developers.openai.com/api/docs/guides/voice-server-controls?api=live)
and the prompting guide
(https://developers.openai.com/api/docs/guides/live-prompting).

- **Output audio pacing: none.** `session.start`'s `session` takes only
  `model`, `instructions`, `audio` (`format.type`/`format.rate`,
  `output.voice`), `delegation`, `client`, `input` and `store`; the
  WebSocket guide says the primary WebSocket "sends output audio without
  timing fields or an output-audio-done event", and nothing documents
  faster-than-real-time delivery or a buffering/speed control.
- **Turn detection / end-of-turn eagerness: none.** There is no
  `turn_detection`, VAD or eagerness field on `session.start`, and
  `session.update` can change only `delegation` ("startup settings such
  as the frontend model and audio configuration stay fixed"). The model
  is full duplex; the only turn-taking lever documented is the prompt
  (e.g. "For routine questions, give one or two short sentences").
- Worth knowing: the client events include `session.input_audio.mute` /
  `session.input_audio.unmute`; the daemon currently drops muted frames
  and sends its own silence keep-alive instead. The docs do not say
  whether a server-side mute changes billing.

Nothing to wire into `internal/voice/gptlive`, so no before/after
`audio-e2e` measurement was run. **GPT-Live seconds billed for this
check: 0** (no live session opened).

## Gemini Live (`gemini-3.8-live`, fake brain)

Final run (daemon session 6 of 8), after the comfort-noise fix below;
`gemini.start_sensitivity = low` (the new default).

| Scenario | Result | Heard (in) | Said (out) | Tool calls | EOS→in / tool / audio / out (ms) |
|---|---|---|---|---|---|
| status-question | pass | What's going on with my agents? | Checking on the status of your agents for you. One agent: auth is asking… | to_supervisor | 1145 / 1650 / 1146 / 1146 |
| approval-yes | pass | Yes. | On it. Told auth yes. (keys `y` sent to the mock pane, fast path) | to_supervisor "Yes" | 1513 / 1557 / 1513 / 1513 |
| instruction-promise | pass | Tell off to run the tests. And let me know when it's done. | On it. I'm letting auth know to tell off to run the tests. I will let you know as soon as it's finished. | to_supervisor | 1536 / 1537 / 1536 / 1536 |
| follow-up | pass | Which agent was that about? | Checking. It was about auth, which was told to tell off to run the tests. | to_supervisor | 1233 / 1439 / 1233 / 1233 |
| stop-mid-reply | **fail** | Give me a full rundown of every agent and what each one is doing. | I'll check the current status of all agents now. | to_supervisor | – |
| stop-button | pass | How is Off doing? | Stopped: nothing was in flight. Shall I tell it something else? (flush sent) | stop (the late "Stop."), to_supervisor, stop (the button) | 1151 / 1873 / 3089 / 3089 |
| room-noise-only | pass | (nothing) | – | – | – |
| speech-snr-10db | pass* | How is the Docks agent doing? | Checking on the status of the Docks agent for you… | to_supervisor "How is the docs agent doing?" | 929 / 1657 / 936 / 936 |
| long-silence | pass | (nothing) | – | – | – |

\* scored as a fail by the run (the expected words were "docs|doc|dogs");
"Docks" is the homophone, and the model itself relayed "docs". The
scenario now accepts "docks".

**stop-mid-reply (open):** a short "Stop." spoken over the reply made
Gemini interrupt itself (`voice.interrupted {by: model}`), but no input
transcript and no `stop` call followed; the word was transcribed only
with the next utterance ("Stop. How is Off doing?" → `stop`), 10 s later.
Tried: start sensitivity `default` (session 7) — same. The stop button
(`cancel`) path works; on GPT-Live (below) the spoken stop works.

With the real brain (DeepSeek `deepseek-flash`, session 8):

| Scenario | Result | Heard (in) | Said (out) | Tool calls | EOS→in / tool / audio / out (ms) |
|---|---|---|---|---|---|
| status-question | pass | What's going on with my agents? | No agents are running — the flock is empty right now. Want me to help you get one going? | to_supervisor | 1240 / 1240 / 3239 / 3239 |

## GPT-Live (`gpt-live-1`, fake brain, `idle_close_s = 5`)

The first full pass found bugs 2–5 below; status-question, follow-up
and room-noise-only passed there and were not re-run (to save billed
seconds); the other six rows are from the run after the fixes.

| Scenario | Result | Heard (in) | Said (out) | Tool calls | EOS→in / tool / audio / out (ms) |
|---|---|---|---|---|---|
| status-question | pass | What's going on with my agents | On it, I'm checking. No agents are running right now. | to_supervisor | 732 / 2027 / 2218 / 2292 |
| approval-yes | pass | Yes | Auth wants a shell command to write "hello from codex." … Okay, I'll tell them yes. (keys sent, fast path) | to_supervisor "Yes" | 1039 / 1459 / – / 1739 |
| instruction-promise | pass | Tell off to run the tests and let me know when it's done | On it. I told auth to tell off to run the tests, and I'll let you know the result when they're done. | to_supervisor | – / 1474 / 1410 / 1307 |
| follow-up | pass | Which agent was that about | That's Auth. (answered from context, no delegation) | – | 76 / – / 2196 / 1971 |
| stop-mid-reply | pass† | Give me a full rundown of every agent and what each one is doing · Stop | Checking. Okay, stopping. Stopped — nothing was in flight. Want me to tell it something else? | to_supervisor, stop | – / – / – / 1281 |
| stop-button | pass | How is auth doing | Checking that. … I've stopped them. Nothing was in flight. (flush sent) | to_supervisor, stop (button) | 150 / 1673 / 1654 / 1572 |
| room-noise-only | pass | (nothing; −45 dBFS café noise never opened a session: 0 s billed) | – | – | – |
| speech-snr-10db | pass | How is the docs agent doing | Checking. I see them working through your instruction to have off run the tests. | to_supervisor | – / 1727 / 2106 / 2002 |
| long-silence | pass† | (nothing) | – | – | – |

† Re-scored from the recording after two harness fixes (no daemon
change, no new session): the run joined the two utterances as
"doingStop", so the word match missed the `stop` that was relayed and
run; and the long-silence close (`voice.session_close {reason: idle}`
0.7 s into the scenario, in the daemon log) landed in the harness's
settle window, which then discarded it. The harness now separates
utterances on GPT-Live's empty final fragment and keeps
`voice.session_*` events seen while settling; the DeepSeek run below
shows the close scored live.

With the real brain (DeepSeek `deepseek-flash`, one brain call):

| Scenario | Result | Heard (in) | Said (out) | Tool calls | EOS→in / tool / audio / out (ms) |
|---|---|---|---|---|---|
| status-question | pass | What's going on with my agents | Checking. Your roster is empty. Start one in a pane and I'll tell you where it stands. | to_supervisor | – / 1505 / 1688 / 1323 |
| long-silence | pass | (nothing) | – | – | – (`voice.session_close {reason: idle, billed_s: 12}`) |

### GPT-Live seconds billed

| Session | What | Opened by | Billed s |
|---|---|---|---|
| probe | raw wire check (docs verification) | — | 11 |
| 1 | status-question | audio | 9 |
| 2 | approval-yes (first pass) | push (the block announcement) | 16 |
| 3 | follow-up (first pass) | audio | 7 |
| 4 | stop-mid-reply (first pass) | audio | 6 |
| 5, 6 | speech-snr-10db (first pass: the noise bed opened it, the idle close cut the speech) | audio | 6 + 4 |
| 7 | approval-yes (fixed) | push | 11 |
| 8 | instruction-promise (fixed) | audio | 13 |
| 9 | stop-mid-reply, stop-button, speech-snr-10db (fixed; one session across three scenarios) | audio | 43 |
| 10 | status-question + long-silence, DeepSeek brain | audio | 12 |
| | **total** | | **138 s ≈ $0.115** |

Every session was closed by the daemon (`reason: idle`, confirmed by
`session.closed`); none was left open.

## Bugs the harness found (fixed, each with a unit test)

1. **Gemini never heard an utterance followed by the gate's digital
   silence** (`internal/voice/gemini`). After the client's 300 ms of
   zeros and `audioStreamEnd`, Live did not close the turn; the words
   were transcribed — twice — only when a later stream carried a real
   noise floor. A 1 s ±8 LSB burst before the stream end fixed the plain
   case but not a short "stop" while a tool result was pending; the tail
   is now streamed at real-time pace and cancelled by the next client
   frame. (`TestAudioStreamEndSendsComfortNoiseFirst`,
   `TestGateReopenCancelsComfortTail`.) Real microphones rarely produce
   300 ms under 8 LSB, which is why this had not shown up in use; a
   hardware-muted or very quiet mic does.
2. **The rail refused a spoken "yes" on GPT-Live** (`internal/supervisor`).
   The model's speech ended (turn complete closes the voice input) just
   after the "Yes" fragment arrived, and the utterance's empty final
   fragment then became an empty input of its own; the fast path saw an
   empty newest input and silently gave up. An empty fragment now only
   closes the open input. (`TestEmptyFinalFragmentIsNotAnInput`.)
3. **GPT-Live's idle close cut a sentence** (`internal/voice/gptlive`):
   it fired 0.7 s into an utterance, before its first transcript, and
   disarmed the audio trigger, so the rest was never heard. Loud frames
   now hold the close off (up to `NoiseRun`, 8 s), and the trigger is
   disarmed only while loud frames keep coming.
   (`TestIdleCloseWaitsForSpeechInProgress`,
   `TestNoisyRoomClosesAndDisarms`,
   `TestIdleCloseBillsAndStaysArmedForTheNextUtterance`.)
4. **GPT-Live glued utterances together**: words the model answered by
   itself (no delegation) stayed "unclaimed" and were prefixed to the
   next utterance, so a later bare "stop" was not a bare stop.
   (`TestUnclaimedWordsDoNotGlueToTheNextUtterance`,
   `TestLateDelegationTakesTheSettledUtterance`.)
5. **GPT-Live split the shepherd's input**: the model's speech ending
   mid-utterance emitted a turn complete, which closes the supervisor's
   voice input. It now waits for the utterance to settle.
   (`TestTurnCompleteWaitsForTheShepherdsUtterance`.)

## Not fixed / worth knowing

- Gemini: a short spoken "stop" over a reply (above).
- Both providers heard the TTS alias "auth" as "off" in "Tell auth to…".
  The scratch config had no `[aliases]`; the everyday config's aliases
  go into Gemini's custom vocabulary and GPT-Live's prompt.
- The approval announcement is pushed as "auth asks to run printf '%s\n'
  'hello from codex.' > hello.txt." — Gemini reads the shell command out
  (against D9, "never read paths"); GPT-Live paraphrases it. The push text
  comes from the fake brain's announce; worth checking with the real one.
- Harness spill-over: a reply or a relay that arrives after a scenario's
  tail belongs to the next one; the harness now waits for 1.5 s of quiet
  (at most 12 s) before each scenario and drops what it saw meanwhile.

### GPT-Live start buffer raised to 1200 ms (2026-09-24)

On the recorded GPT-Live replies the 800 ms start still left one underrun
on the 0.69× instruction-promise reply and on the chained session. With a
1200 ms start (cap unchanged at 2000 ms, time-stretch as before) all three
recorded replay cases drop to **0 underruns** (status-question 3 → 0,
instruction-promise 2 → 0, chained 4 → 0 against the old Gemini-policy
baseline). Cost: the first GPT-Live word waits about 0.4 s longer. Gemini is
unchanged (150/500 ms). The iOS app uses the same numbers.
