# GPT-Live notes for `internal/voice/gptlive`

Recorded 2026-09-23. Sources, in order of authority:

1. The OpenAI docs, read as Markdown (append `.md` to any page):
   `api/docs/models/gpt-live-1`, `api/docs/guides/live`,
   `api/docs/guides/live-delegation`, `api/docs/guides/live-conversations`,
   `api/docs/guides/voice-websockets?api=live`,
   `api/docs/guides/voice-server-controls?api=live`,
   `api/docs/guides/live-prompting`, `api/docs/guides/voice-latency-cost`,
   `api/docs/guides/live-migration`, and the event reference
   `api/reference/resources/live/primary-websocket`.
2. One raw-wire probe (a throwaway `coder/websocket` program in the
   session scratchpad, 11 s billed) and the audio-e2e runs
   (`docs/audio-e2e-results.md`), which confirmed every field below.

## Connection

- `wss://api.openai.com/v1/live/sessions`, no query parameters, header
  `Authorization: Bearer $OPENAI_API_KEY` and nothing else (the Python
  SDK's Live client adds only its auth and default headers; the probe
  connected with the bearer header alone).
- First message `session.start {event_id, session: {model, instructions,
  audio: {format: {type: "audio/pcm", rate: 24000}, output: {voice}},
  delegation: {type: "client"}, input: [...]}}`; wait for
  `session.started {session: {id, expires_at, model, …}}` before audio.
  Observed: ~0.5–0.9 s from dial to `session.started` (1.8 s the first
  time).
- Model, instructions, input history, audio format and voice are fixed
  per session. PCM16 at 16 or 24 kHz (one rate both ways) or G.711. We
  run 24 kHz and upsample the /ws 16 kHz input 2:3 (linear) in the
  daemon; output passes through unchanged.
- Voices: `marin` (default), `cedar`, `alloy`, `ash`, `ballad`, `coral`,
  `echo`, `sage`, `shimmer`, `verse`, plus `quartz`, `ripple`, `vesper`,
  `willow`, `stone`, `gleam`, `meridian`, `bossa`, `tempo`, `beacon`,
  `delta`, `cinder`.
- `session.input`: up to 128 text messages / 8,192 tokens, roles
  `developer`, `user` (`input_text`), `assistant` (`output_text`). We
  seed a reopened session with the last turns (≤ 2,000 chars) and a
  developer note, plus silent context received while closed.
- No turn-detection, noise or VAD options exist: GPT-Live is full duplex
  and decides itself when to speak. The prompting guide's "Silence and
  background noise" lines are the only lever (in our prompt).

## Events we use

| Direction | Event | Notes |
|---|---|---|
| → | `session.input_audio.append {audio}` | base64 PCM16; no ack. |
| → | `session.commentary.append {delegation_id, content}` | a result the model says (paraphrased); ack `session.commentary.appended {client_event_id, start_ms, end_ms}`. `delegation_id` is required and nullable. |
| → | `session.thinking.append {delegation_id, content}` | silent context. |
| → | `session.instructions.append {delegation_id, content}` | behaviour; "can interrupt speech in progress". |
| → | `session.close` | then wait for `session.closed`. |
| ← | `session.output_audio.delta {delta}` | **a continuous real-time stream, silence included** (10.5 s of audio in an 11 s session). No timing fields, no audio-done event. |
| ← | `session.input_transcript.delta {delta, start_ms, end_ms}` | 200 ms windows, leading spaces preserved, no end-of-turn event. |
| ← | `session.output_transcript.delta {…}` | same shape. |
| ← | `session.delegation.created {offset_ms, delegation: {id, type, target: "client"}}` | metadata only, **no text**. Arrived with the utterance's last transcript fragment in every run. |
| ← | `session.usage.updated {usage: {seconds}}` | cumulative per session; do not sum. |
| ← | `session.closed {reason, usage: {seconds}, session}` | `close_requested`, `expired`, `content`, `remote_hangup`, `connection_lost`. Billed seconds come from here. |
| ← | `error {error: {type, code, message, param, client_event_id}}` | a rejected command; some moderation errors cut the current speech. |

There is no interruption event and no way to cancel speech other than an
appended instruction; barge-in is the model's own (it stops when the user
talks over it). The daemon's "flush" for a stop is local: it drops the
queued output and tells the client to flush.

## How the adapter maps it (`internal/voice/gptlive`)

- **Lazy session.** `Start` does not dial. A session opens for a loud
  (RMS ≥ 300) microphone frame from an attached, unmuted, armed client
  (the ~0.5 s pre-roll and the frames that arrive while it opens are
  replayed) or for a spoken push while a client is attached. It closes
  after `voice.openai.idle_close_s` without an input transcript, voiced
  model audio, a delegation or push in flight — but not while loud
  frames suggest the shepherd has just started a sentence (for up to
  8 s; a longer loud run is room noise). An idle close with loud frames
  still coming disarms the audio trigger until the gate closes, a quiet
  second passes, the client unmutes or reconnects, so a noisy room does
  not reopen what just closed.
- **Speaking.** Voiced output (peak ≥ 200) or an output transcript within
  800 ms. Silent output chunks are dropped, except 1.2 s after voiced
  audio so pauses inside a reply survive. The end of speech emits
  `EventTurnComplete` — deferred to the end of the shepherd's utterance
  if they are talking, since it closes the supervisor's voice input.
- **Utterances.** Input fragments are forwarded as they come; 700 ms
  after the last one the utterance is closed with an empty final
  fragment. A delegation waits until the input has paused 400 ms (at most
  2 s), then takes every word no earlier delegation claimed (or the
  utterance that settled in the last 4 s) as `to_supervisor {text}` — or
  `stop` for a bare stop word (`inject.Router`). A bare stop word that no
  delegation claims runs `stop` on its own when it settles.
- **Results.** `Spoken` (+ options) → `session.commentary.append` on the
  delegation; `Detail` → `session.thinking.append` on the delegation; a
  silent result → thinking. If the session closed meanwhile, the result
  becomes a push.
- **Pushes.** Idle → `commentary.append` (null id) once the model is not
  speaking, the shepherd is not talking and `PushGap` passed; interrupt
  → local flush + `instructions.append("Stop what you are saying and
  tell the shepherd this now …")`, downgraded to idle while the model
  speaks unless tagged `due_promise` / `needs_user` / `stop` (the Gemini
  rule, docs/roles.md "Pushes"); silent → `thinking.append`, or kept for
  the next session's history while closed. A spoken push that no client
  can hear waits up to 5 minutes, then `voice.push_dropped`.
- **Typed text** (`SendText`) goes straight to the tool handler — GPT-Live
  has no user text input — and the reply is pushed.
- **Keep-alive (input pacing).** While a session is open and the client
  sends nothing (gate closed, muted), the daemon sends silence paced at
  real time against the wall clock (`pace.go`: at most 200 ms a tick,
  larger debt forgiven as `voice.input_debt_dropped`): the session
  timeline advances with input audio ("supply a continuous microphone
  stream paced at its recorded sample rate"; "if the session timeline
  stops, the acknowledgment can remain pending"). Until 2026-09-25 it
  advanced at ≤ 0.5× real time and the model's output ran at 0.74–0.87×;
  paced, the output runs at ≈1.0× (`docs/audio-e2e-results.md`, "GPT-Live
  input pacing"). Silence rather than `session.input_audio.mute`: the
  docs say muting "does not cancel backend work or stop generated
  speech", not that the timeline advances while nothing is sent.
- **Billing.** `voice.session_close {billed_s, confirmed}` uses
  `session.closed`'s usage (wall clock when it never came);
  `voice.billed_seconds {total_s}` after every close; `web.client.stats`
  carries `voice_billed_seconds`.

## Observed behaviour worth knowing

- Transcripts are good: "What's going on with my agents", "Yes", "How is
  the docs agent doing" under a café bed at 10 dB SNR (Gemini heard
  "Docks"). Neither provider heard the alias "auth" in "Tell auth to…"
  from a TTS voice ("off"); with no `[aliases]` configured Gemini's custom
  vocabulary was empty too.
- The model answers some follow-ups from context without delegating
  ("Which agent was that about?" → "That's Auth."), as the prompting
  guide says it will.
- It paraphrases commentary freely: the approval announcement "auth asks
  to run printf '%s\n' 'hello from codex.' > hello.txt." became "Auth
  wants to write hello from codex to a new file. Allow it?" — better for
  the ear than Gemini's verbatim reading of the shell command.
- Latency, end of speech → first reply audio: 1.4–2.2 s (the first
  "on it" / "checking"), about the same as Gemini (0.9–1.5 s to the
  first audio with the fake brain).
- Session setup costs ~0.5–0.9 s; with the pre-roll replay the first
  words were never lost in the runs.
