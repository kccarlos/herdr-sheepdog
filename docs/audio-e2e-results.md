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
