// Package gemini implements the VoiceSession: one long-lived WebSocket
// session to the Gemini Live API, owned by the daemon for its lifetime
// (with reconnect and session resumption on GoAway or drop). It bridges
// audio between the browser and the Live API, declares the five voice
// tools as non-blocking function declarations, executes tool calls against
// the Supervisor and returns the chosen scheduling hint, and exposes
// push(text, urgency) so the model can speak unprompted.
package gemini
