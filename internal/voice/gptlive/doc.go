// Package gptlive implements voice.Session for OpenAI's GPT-Live
// (model gpt-live-1) over the primary Live WebSocket
// (wss://api.openai.com/v1/live/sessions), in client delegation mode:
// the voice model hands work to the daemon with session.delegation.created
// and the daemon's supervisor (the brain) answers it. See
// docs/gpt-live-notes.md for the wire protocol as verified live.
//
// GPT-Live is billed per second of session, idle or not, so unlike the
// Gemini session this one is lazy: Start does not connect. A live session
// is opened when the client is attached and unmuted and there is speech to
// hear (a microphone frame above the open threshold) or a push to say, and
// closed again after Config.IdleClose without activity (no transcript, no
// model speech, no delegation or push in flight). The session reports
// voice.StatusConnected from Start on (reason "standby" while no live
// session is open) because the supervisor only pushes to a connected
// voice, and a push is exactly what should reopen it.
//
// The protocol has no turn events: no end-of-speech for the user, no
// turn-complete for the model, and output audio is a continuous real-time
// stream (silence included). The session therefore synthesises them:
//
//   - an input utterance ends SettleWait after its last
//     session.input_transcript.delta (EventTranscript{DirIn, "", Final});
//   - the model is speaking while it produced voiced output audio or an
//     output transcript fragment within SpeechHold; the transition to not
//     speaking emits EventTurnComplete (Speaking() and the push queue
//     additionally count the /ws client still playing the reply, from
//     its playback reports; the synthesised turn end does not);
//   - silent output audio is dropped, except for a short hangover after
//     speech so natural pauses survive, so the /ws client only receives
//     audio while the model talks.
//
// A delegation carries no text: the utterance is the input transcript not
// yet claimed by an earlier delegation, taken once the transcript settles.
// It becomes a to_supervisor tool call (or stop, per Config.Classify) and
// the result goes back with session.commentary.append on the delegation
// id. Pushes become session.commentary.append (idle), session.thinking.append
// (silent) or session.instructions.append (interrupt), with delegation_id
// null.
//
// Audio: /ws delivers 16 kHz PCM16; the Live session runs at 24 kHz, so
// input is resampled 2:3 here and output passes through unchanged.
package gptlive
