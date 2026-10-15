> Superseded by [`docs/dev/design/dual-brain.md`](../design/dual-brain.md) (adopted and merged in #122); kept as a dated snapshot.

# Voice layer research: problems, how voice-agent frameworks solve them, and a WebRTC/Pion assessment

> Superseded: recommendations 1–5 landed in #70 and #73 (see the "Done" column); later voice issues and their decided fixes are in [`../history/bench-2026-10-03.md`](../history/bench-2026-10-03.md).

Date: 2026-09-24. Research method: read-only over source code and official docs; no Gemini or OpenAI API calls were made.
Upstream source clones (livekit-agents, pipecat, ten-framework) live in the research session's scratchpad; none went into the repo.

Markers: **[source]** means source code read personally (with the path given); **[docs]** means the official documentation's own wording; **[inferred]** means not yet tested or without a primary source.

The "§6 Live-test addendum" at the end records log findings from a live test run the same day, 18:11–18:26 UTC.

## 0. Summary: top 5 recommendations (ranked by benefit/cost)

| # | Recommendation | Benefit | Cost | Confidence | Done |
|---|---|---|---|---|---|
| 1 | Pace GPT-Live input at real wall-clock time. Change `keepAlive` to make up owed silence against the wall clock, or under GPT-Live don't gate input at all and use `session.input_audio.mute` instead. Then re-measure `realtime_ratio`; once it's near 1.0, drop the 1200 ms start-of-playback buffer to about 300 ms | Could eliminate the stutter at the root, cutting time-to-first-word by about 0.9 s | Small; needs about 30–60 billed seconds, ask the user first | Medium-high [inferred, strong evidence] | **#73**, confirmed live: `realtime_ratio` 0.94–1.06 (was 0.74–0.87), start buffer 1200→400 ms (not the ~300 ms guess, but re-measured against the paced timeline; iOS `PlaybackProfile` still at 1200 ms, open in the iOS repo's docs/status.md) |
| 2 | Add a `playback` message to `/ws` so the client reports its actual playback position; truncate "Assistant said" to that position, and silently tell the voice model which part the user actually heard | Sharper referents for the brain | Medium | High [source+docs] | **#70**: `playback` message (`started`/`drained`/`flushed`, `played_ms`), "Assistant said" retracted and replaced with what was actually heard on a cut. The silent "the shepherd heard only…" push to the voice model itself is left a documented TODO |
| 3 | Have `Speaking()` look at client playback state too, not just generation state | Actually deliver on #34's "don't interrupt mid-sentence" | Small to medium | High [source] | **#70**: `Speaking()` now means generation or client playback (`started` through `drained`+300 ms or `flushed`) on both Gemini and GPT-Live |
| 4 | Echo: confirm platform AEC already covers this, then patch the blind spots (mobile browsers, Safari); read `track.getSettings()` and warn the user | Stops the model from interrupting itself | Small | High on desktop Chrome; the rest [unverified] | **#73** on the web client: reads `track.getSettings().echoCancellation` and warns when off or unknown on mobile; carried in the ping as `aec`. iOS side: `herdr-sheepdog-ios` #18 fixed the voice-processing rebuild after a route change and added a Health-pane status row, but device confirmation is still open (the iOS repo's docs/status.md) |
| 5 | Switch push timing to client-side VAD. Right now "user is speaking" = an input transcript arrived within 1.5 s, which lags | Fewer proactive announcements colliding with the user starting to talk | Small | Medium [inferred] | **#70**: a client speech detector (`speaking` message) holds spoken pushes while the gate is open and for 300 ms after, alongside the existing transcript-based hold (whichever is longer wins) |

WebRTC/Pion conclusion: don't migrate now, keep `/ws`. Once 1–3 are done and re-measured, do a spike for the browser only.

## 1. Problem table

| # | Symptom | Current fix | Code location | Where it's still fragile |
|---|---|---|---|---|
| P1 | GPT-Live playback stutters | Adaptive jitter buffer (#43), 1200 ms start-of-playback (#46), WSOLA rate change 0.85–1.05 (#45) | `web/voice/audio-util.js` `Player`/`Wsola`; iOS `PlaybackQueue` + `AVAudioUnitTimePitch` | Treats the symptom. Output only runs 0.74–0.87x real time, and first word waits an extra 1.2 s. Suspected root cause is on the input side (see below) |
| P2 | Acoustic echo causes self-interruption | Web: `getUserMedia({echoCancellation:true})` (`web/voice/app.js:207`); iOS: `.voiceChat` + `setVoiceProcessingEnabled(true)` (`AudioEngine.swift:137-142`) | same | Unverified on physical iOS devices; unverified on mobile browsers and Safari; GPT-Live has no VAD to tune, so echo that leaks through triggers an interruption |
| P3 | Model routes its own push back to `to_supervisor` | `isEcho` changed to require a whole word, ≥4 words, ≥60% coverage (#48) | `internal/supervisor/utterance.go:493` | Relies on a text heuristic; iteration-2 WP3 (#58) has already removed this guard, see §6 |
| P4 | A push/tool result cuts off the model mid-speech | INTERRUPT downgraded to WHEN_IDLE while speaking (#34) | `gemini/push.go`, `toolexec.go:83` | `Speaking()` looks at generation state, and goes false at `turnComplete` (`gemini/reader.go`). Gemini generates about 4x faster than real time, so the client is still playing after `turnComplete`; an INTERRUPT push arriving then calls `interrupt("push")` and flushes the client [source] |
| P5 | "Assistant said" doesn't equal what was actually played | none | `supervisor.OnVoiceEvent` → `brain.Said` → `flushOutLocked` (`internal/brain/brain.go`) | If interrupted, the brain thinks the user heard the whole thing. Gemini docs: "Only the information already sent to the client is retained in the session history" — but the daemon had already received the whole segment. GPT-Live context can only be appended to |
| P6 | "User is speaking" judgment lags | `SpeechHold` 1.5 s (based on input transcription) | `gemini/push.go:156-162` | The transcription itself lags |
| P7 | A silent stream degrades the session and stalls the turn | Silence gate + `audioStreamEnd` + comfort noise; stall recycling (#16) | `gemini/turn.go`, `session.go` | Reasonable for Gemini; counterproductive for GPT-Live (P1) |
| P8 | Recovery with a handle under 1011 keeps failing | On 1011, drop the handle and reconnect fresh (#41); pushes aren't dropped during reconnection (#48) | `gemini/errors.go`, `run.go` | Relies on matching error text; already more careful than LiveKit/Pipecat |
| P9 | Brain is slow | `reply_wait_s` (2 s) times out into a late push (#47) | supervisor, `gptlive/tools.go` | The drop rules are fairly involved |

**P1 root-cause hypothesis:**

1. OpenAI's docs require "supply a continuous microphone stream paced at its recorded sample rate," and note "If the session timeline stops, the acknowledgment can remain pending" [docs `guides/voice-websockets?api=live`, `live-conversations`].
2. When the client gate closes (the user isn't speaking, which is exactly when the model is replying), `keepAlive` ticks every 40 ms; if less than 80 ms have passed since the last send it skips, otherwise it sends 40 ms. So the input timeline advances at 0.5x at best (0.33x when tick jitter hits) [source `internal/voice/gptlive/loop.go:42-53`, confirmed in the orchestration session].
3. Measured output is 0.74–0.87x; Gemini doesn't depend on input pacing and runs at 4x.
4. For comparison:
   - LiveKit's comment says output "arrives at playback pace" (`llm/duplex_adapter.py`); the console pre-buffers `PREBUFFER_DURATION = 0.3`, reasoning "largest arrival gap observed with GPT-Live" (`cli/_legacy.py`).
   - LiveKit mutes input with `session.input_audio.mute` (`gpt_live_model.py` `mute_input`).
   - Pipecat appends continuous input as-is (`services/openai/live/llm.py`) [source].

## 2. Framework notes

### Versions and maturity (checked via the GitHub API on 2026-09-24)

| Project | Latest release | License | Language | Activity |
|---|---|---|---|---|
| LiveKit agents | 1.8.2 (09-15) | Apache-2.0 | Python | commits that day |
| agents-js | 1.9.x (09-15) | Apache-2.0 | TS | active |
| client-sdk-swift | 2.17.0 (09-14) | Apache-2.0 | Swift | active |
| Pipecat | v1.11.0 (09-18) | BSD-2 | Python | active |
| pipecat-client-ios | no release, last push 07-02 | BSD-2 | Swift | fairly cold |
| TEN | 0.11.73 (09-22) | Apache-2.0 + additional terms | C core + Go/Py/Node bindings | active |
| openai-agents-python | v0.22.3 (09-17) | MIT | Python | active |
| openai-agents-js | v0.18.0 (09-10) | MIT | TS | active |
| pion/webrtc | v4.2.21 (09-24) | MIT | Go | very active |
| pion/opus | v0.1.0 (06-08), both encode and decode | MIT | Go | active, just hit 0.1 |
| stasel/WebRTC | 153.0.0 (09-11) | upstream BSD | prebuilt binaries | tracks Chromium |

TEN's `LICENSE` text bans "host … on any End User devices" and deployments that compete with Agora. The daemon runs on the user's own Mac, which probably falls within those limits — so don't bring in TEN code. `ten-vad` has no end-user-device clause, but it does have a competition clause [source].

**Both LiveKit and Pipecat already implement GPT-Live's client delegation** — the same architecture as sheepdog, making them the most direct reference [source]:
- LiveKit: `livekit-plugins-openai/.../realtime/gpt_live_model.py`, example `examples/voice_agents/gpt-live/client_delegation.py`
- Pipecat: `pipecat/services/openai/live/llm.py`, example `examples/realtime/realtime-openai-live-client-delegation*.py`

### Jitter
- LiveKit: `rtc.AudioSource(queue_size_ms=200)`, handed off to NetEQ on the receive side.
- Pipecat: `base_output.py`'s `_audio_runs` chunks at a fixed cadence.
- OpenAI: client is recommended to use WebRTC; there are no timestamps over WS.
- sheepdog's client-side WSOLA is already finer-grained than either framework; what needs fixing is the source.

### AEC
- **Desktop Chrome:** In Chromium's `media/base/media_switches.cc`, `kChromeWideEchoCancellation` is `FEATURE_ENABLED_BY_DEFAULT`, commented "mix and cancel all audio playback going to a specific output device"; `media_options.gni` only compiles it for Win/Mac/Linux. So AudioWorklet output is also an AEC reference [source]. This matches discuss-webrtc's June 2023 test findings (gain=0 doesn't count).
- **Other platforms:** The older claim "Web Audio doesn't feed AEC" (crbug 40504498) applies to platforms without chrome-wide AEC (Android, WebKit on iOS) [inferred; the bug page needs login]. Safari unverified.
- **iOS:** VPIO is already on; iOS 17 has `voiceProcessingOtherAudioDuckingConfiguration` (found via search, secondhand).
- **Gemini docs:** recommend "Use headphones … prevent the model from interrupting itself."
- **The frameworks themselves:** neither LiveKit nor Pipecat has its own AEC code; both rely on the client's WebRTC APM. LiveKit has a Krisp noise-suppression plugin.

### Interruption
- **LiveKit:** `SpeechHandle.allow_interruptions`, with false-interruption recovery (`_start_false_interruption_timer`, `agent_activity.py`). The GPT-Live adapter's `interrupt()` is a no-op, commented "barge-in is the model's own."
- **Pipecat:** `InterruptionFrame` plus a per-frame `interruptible` flag; GPT-Live recommends `enable_interruptions=False`, with every tool set to `cancel_on_interruption=False`.
- **Gemini:** an `interrupted` field; `activityHandling: NO_INTERRUPTION`.
- **GPT-Live:** `instructions.append` can interrupt; blocking audio is only possible client-side or via a relay controlling playback [docs `voice-server-controls?api=live`].
- **Takeaway:** give pushes an `interruptible:false` flag, and have the client only clear segments marked interruptible.

### Transcript truncation
- **LiveKit:** `playback_position` plus `synchronized_transcript`; `chat_ctx` records `forwarded_text` (`agent_activity.py:~3297-3347`). OpenAI Realtime uses `truncate(audio_end_ms)`; the Google plugin sets `message_truncation=False` (`realtime_api.py:354`), which only patches the framework's own context.
- **Pipecat:** `_truncate_current_audio_response` takes `min(wall-clock played, audio duration)`.
- **Agents SDK:** `RealtimePlaybackTracker` (`on_play_bytes`/`on_play_ms`).
- **GPT-Live:** can't truncate. Output transcripts carry `start_ms/end_ms`; the docs say "Discarded speech remains in the model's conversation context."
- **Gemini:** no truncation API.
- **Approach:** truncate our own context the way LiveKit does, then tell the voice model silently what happened.

### Turn-taking/VAD
- LiveKit turn-detector (ONNX); Pipecat Silero plus Smart Turn v3 (with CoreML); TEN VAD and Turn Detection (Qwen2.5-7B).
- Gemini's `automaticActivityDetection` is tunable; GPT-Live has no VAD parameters at all.
- sheepdog doesn't need a turn detector; client-side VAD is only used for push timing and lazily opening a session.

### Reconnection
- **LiveKit's Google plugin:** GoAway is only logged, with a `TODO … isn't a seamless reconnection` left in.
- **Pipecat:** no special handling for goAway; forces one inference after reconnecting.
- **Gemini:** a handle is valid for 2 hours, and context compression can push past the duration cap.
- **GPT-Live:** no resumption; the docs suggest closing when idle and re-seeding from history, buffering the opening speech. sheepdog already does this.
- **Conclusion:** sheepdog is at parity or ahead here; no change needed.

### Filler/async
- LiveKit's `BackgroundAudioPlayer` plays thinking sounds.
- Pipecat uses `cancel_on_interruption`/`run_llm` (v1.11.0 PR #5820).
- Gemini has NON_BLOCKING plus scheduling.
- GPT-Live's delegation is inherently async.
- Option: play a light local thinking sound on the client while waiting on the brain, without it entering the model or the transcript.

### Proactive announcements
- LiveKit has `min_consecutive_speech_delay` and false-interruption recovery.
- GPT-Live uses `commentary.append`, letting the model decide when to speak.
- Gemini's WHEN_IDLE judges by generation state, not playback state.
- sheepdog's gaps here are P4 and P6.

## 3. WebRTC/Pion assessment

### Option A: client ↔ daemon over Pion, daemon still connects to the model over WS

**Would eliminate:** the AEC problem on mobile browsers and Safari (attach the receive track to an `<audio>` element), remote TCP head-of-line blocking, and client-side jitter (via NetEQ).

**Would not eliminate:** underrun on the model side. The daemon's 20 ms pacer would still starve; the underrun just moves from the client to the daemon. Truncation would still have to be done by hand (using `getSynchronizationSources`, `jitterBufferDelay`).

**Implementation notes:**
- `pion/opus` is pure-Go encode/decode (MIT, v0.1.0, maturity needs testing); the fallback is cgo-based `hraban/opus`. Needs 48↔16/24 kHz resampling either way.
- Signaling: `POST /rtc/offer` (same Bearer auth), wait for ICE gathering to finish (no trickle), following Pion's `whip-whep` example.
- `SetICEUDPMux` for a single UDP port. LAN and Tailscale should work via host candidates [inferred]; public internet access needs `pion/turn`, which isn't recommended — the answer there should be "use Tailscale."
- Browser: `RTCPeerConnection` plus `<audio>`; reuse the wsproto DataChannel; keep the worklet path as the `/ws` fallback.
- iOS: there's no first-party WebRTC, only `stasel/WebRTC` or LiveKit's xcframework (both third-party binaries), so iOS keeps `/ws` and VPIO.

**Effort and risk:** roughly 3–4 weeks, split into 4–5 PRs [inferred]. Risks: `pion/opus` maturity, Go timer jitter, one more open port to attack, and roughly double the test surface.

### Option B: client connects directly to OpenAI WebRTC, with a daemon sideband (GPT-Live only)

- **Approach:** the daemon uses its key to call `POST /v1/live/sessions` (transport webrtc), then connects to `wss://api.openai.com/v1/live/sessions/{id}/attach`; delegation arrives over the sideband, and the daemon sends `commentary`/`thinking`/`instructions.append` there; the sideband also receives reflected audio with `start_ms/end_ms` [docs `voice-webrtc?api=live`, `voice-server-controls?api=live`].
- **Would eliminate:** GPT-Live's pacing and jitter problems, and the browser AEC problem.
- **Cost:**
  - Gemini has no official WebRTC (only third-party integrations), so two transports would need maintaining;
  - lazy session opening and prerolled replay get harder;
  - each new session pre-charges 15 s (credited back once it runs);
  - iOS needs a WebRTC library.
- **Recommendation:** decide after testing Option C.

### Option C (recommended): WS + platform AEC + fix the root cause

- Desktop Chrome is already good enough [source].
- iOS should be good enough in theory, but the §6 live test shows echo still leaks through; needs debugging on a real device (use `--voice=off` so it doesn't burn API quota).
- Mobile browsers and Safari are uncertain; the UI should prompt the user to use headphones or the app.

## 4. Unverified

- Current state of crbug 40504498; Safari/WebKit's AEC behavior.
- The P1 hypothesis needs one billed live test; also need to check whether input was gated closed during the audio-e2e run in question.
- Apple VPIO documentation details (found via search, secondhand).
- README details for the stasel and LiveKit xcframeworks.
- `pion/opus` encoding quality and CPU cost.
- Actual host-candidate connectivity over Tailscale.

## 5. Work packages (each can be its own PR)

- **WP-A** `fix(voice): pace GPT-Live input at real time while the client is silent`
  - Files: `internal/voice/gptlive/loop.go` (make `keepAlive` cover the owed duration against the wall clock, dropping any debt over 200 ms), `session.go` (`sendAudio` accumulates duration), `session_test.go` (fake clock: 10 s of wall clock sends about 10 s of audio). Optionally switch to `input_audio.mute`.
  - Verification: offline unit test first; then, with the user's consent, one `audio-e2e --audio-timeline` run (GPT-Live).
  - Follow-on PRs: `docs/dev/ws-contract.md`, `web/voice/audio-util.js`, iOS `PlaybackProfile` to bring the GPT-Live start-of-playback buffer down to about 300–400 ms.
- **WP-B** `feat(ws): clients report playback state and played position`
  - Contract: client→server `{"type":"playback","state":"started|drained|flushed","played_ms":N}`, and it must be reported on flush.
  - Files: `docs/dev/ws-contract.md`, `internal/voice/wsproto/wsproto.go`, `internal/web/ws.go`, `web/voice/audio-util.js`, `web/voice/app.js`, plus tests; a separate PR in the iOS repo.
- **WP-C** `feat(brain): record what the shepherd actually heard`
  - Gemini: `gemini/reader.go` records, for each arriving transcript segment, how many audio bytes had been received so far, and truncates by `played_ms` on interruption. GPT-Live: align using `start_ms/end_ms`.
  - Data: add truncation fields to `voice.EventTranscript`; `brain/brain.go` writes the truncated text; `brain/assemble.go` renders it as "Assistant said (interrupted)".
  - Optional: silently tell the voice model.
- **WP-D** `fix(voice): a reply still playing on the client counts as speaking`
  - Files: `internal/voice/voice.go`, `switch.go`, `gemini/session.go`, `gemini/push.go` (start `PushGap` from drained), `gptlive/session.go`, `web/ws.go`, plus tests.
- **WP-E** `feat(voice-web): warn when echo cancellation is unavailable`
  - Files: `web/voice/app.js` (`getSettings`, platform warning), add an `aec` field to ping, `docs/dev/ws-contract.md`.
  - Real-device iOS echo testing goes into the iOS repo's `docs/status.md`.
- **WP-F** `feat(voice): use the client's speech detector for push timing`
  - Contract: `{"type":"speaking","on":bool}`.
  - Files: `docs/dev/ws-contract.md`, wsproto, `web/ws.go`, `gemini/push.go`, the corresponding spot in gptlive.
- **WP-G (optional spike)** browser WebRTC
  - Files: `internal/web/rtc/`, `POST /rtc/offer`, `web/voice/rtc.js`, `CONFIG.md`.
  - Exit criterion: clear improvement in mobile-browser AEC or remote underrun, otherwise drop it.
  - Alternative: write a one-page design note for Option B first, then decide whether to build it.

Order: A → measure → B → C and D (non-overlapping files, can run in parallel) → E, F → decide on G based on the data.

## 6. Live-test addendum (2026-09-24 18:11–18:26 UTC, daemon `d0f79b9`)

Data sources: `~/.local/share/sheepdog/events/2026-09-24.jsonl`, and the brain request snapshot `brain/20260924T181415Z.requests.jsonl`.
Session order: web GPT-Live (18:14–18:17) → web Gemini (18:21–18:22) → iOS Gemini (18:23–18:24) → iOS GPT-Live (18:24–18:25). The brain used was DeepSeek `deepseek-flash`.

### 6.1 Follow-up questions reliably fail: brain requests have messages in the wrong order (highest priority)

- **Symptom:** 5 brain turns failed with `deepseek: HTTP 400: An assistant message with 'tool_calls' must be followed by tool messages` (turns 6, 7, 8, 14, 15). Every one happened while you were asking a follow-up for detail ("details of the glitch," "what exactly did claude-3 say," "I meant a specific agent").
- **Root cause (confirmed in the request snapshots):**
  - `read_screen` gets called by the brain at the same moment the voice model says its filler line, "On it."
  - That "Assistant said" text gets appended to the transcript right between the tool call and the tool result.
  - The assembled order becomes `assistant(tool_calls) → user("Assistant said: On it.") → tool`, which violates the OpenAI-compatible API's constraint.
  - Trigger timing: `reply_wait_s = 2 s` expires right when the voice model happens to speak its filler line, which lines up closely with the brain's first tool call in that turn.
- **Consequences:**
  1. That turn falls back to the templated response "No change since I last checked: Four agents: …", which doesn't answer the question;
  2. The bad message order stays in the window, so **every** subsequent turn (including stop and finish announcements) fails until "New conversation" clears the window;
  3. The 18:18 finish announcement was reduced to just "herdr-sheepdog-claude finished." because of this.
- **Conclusion:** this is the direct cause of "can only give simple status reports, can't go deeper." **A deeper follow-up = a screen read = hitting this bug.**
- **Possible fixes:**
  - When assembling messages, move any context message that lands between a tool call and its result to after the result; do the same in Gemini's `toContents`;
  - or buffer sideband messages during a step and only write them to the transcript once the step finishes;
  - add a regression test either way.

### 6.2 The "No change since I last checked" fallback line

- Another orchestration session had already found: the first status answer after a failed turn also gets this prefix.
- This time it showed up in 3 of the 5 failures, reading out the same mechanical roster sentence.
- Fix: the fallback answer shouldn't be marked as "already reported," and shouldn't reuse that prefix.

### 6.3 Web-side Gemini "goes suddenly silent"

- **First silence (18:21:28–18:21:44) was the browser's playback getting stuck:**
  - Client stats show `buffer_ms` stuck at **3155 ms** for 15 s straight, `frames_out` stopped increasing, `rate 1`;
  - i.e., audio had reached the browser, but the player stopped consuming it. Gemini itself was fine (`realtime_ratio` 7.9, max gap 5.6 ms);
  - Suspicion: after switching from GPT-Live to Gemini, the player state (start-of-playback target, WSOLA) wasn't reset, or the AudioContext got suspended. Needs reproducing in-browser.
- **The following answer was spoken to nobody:**
  - You reconnected twice; the page closed at 18:21:54;
  - the 18:21:57 question (audio had already reached Gemini before the disconnect) got answered at 18:22:04, and the 12 s reply played out in full **with no client connected at all**.
  - The daemon should hold pushes back when there's no client, or replay them after reconnecting.

### 6.4 iOS Gemini self-interruption (echo)

- 18:23:50 the model starts saying "No change, all four are idle…";
- 18:23:51.4 the client's silence gate opens; 18:23:51.9 Gemini decides the user has interrupted (`voice.interrupted by:model`);
- 18:23:54 the transcript comes back as "New change" — which is really the echo of its own "No change" — and that gets handed to the brain as a new user input.
- Conclusion:
  - VPIO's echo cancellation on iOS isn't blocking the speaker output;
  - and since #58 removed the text-based echo guard, that echo now turns straight into a user utterance;
  - P2 on iOS isn't "unverified" anymore — it's **reproduced**.

### 6.5 Positive signals

- **Web GPT-Live performs acceptably:** `rate` was mostly 1, with 12 drains. This matches the P1 "input pacing" hypothesis: the web page keeps sending mic audio continuously, so the gate is closed less of the time than on iOS.
- **Playback interruptions improved:** only 1 `voice.interrupted by:push` this time, and it happened at stop, which is expected behavior.
