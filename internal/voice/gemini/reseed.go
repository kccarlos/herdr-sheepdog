package gemini

import (
	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Re-seeding a connection that lost its context (docs/timelines.md C23,
// §6 rank 1). A 1011 from the server drops the resumption handle, so the
// next connection starts with no memory of the conversation: on
// 2026-09-23..25, 68 of 77 resumes came back resumed:false. Until now it
// got back only the supervisor's silent briefing (the roster), so a
// follow-up — "what does that mean?" — had nothing behind it. As
// GPT-Live does when it reopens a session, the new connection is first
// given a compact recap of the recent conversation as silent context;
// the briefing follows (EventResumed). A "New conversation" (Reset)
// clears the recap first, so it starts empty as intended.
const (
	// reseedChars bounds the recap's text (most recent first, the oldest
	// cut): enough for a few exchanges, small beside the system prompt.
	reseedChars = 1500
	// reseedTurns bounds the entries the session remembers.
	reseedTurns = voice.DefaultRecapTurns
)

// reseedPrefix introduces the recap to the model: context only, never
// something to answer or read out.
const reseedPrefix = "[system context, not spoken by the user] The voice connection dropped and came back without its history. " +
	"This is the recent conversation, oldest first, only so you can follow up on it. " +
	"Do not answer it, read it out or mention it; carry on and wait for the shepherd.\n"

// flushRecapLocked moves the shepherd's pending words and the voice's
// reply so far into the recap. Caller holds s.mu.
func (s *Session) flushRecapLocked() {
	s.recap.Add(voice.RecapUser, s.inText)
	s.recap.Add(voice.RecapAssistant, s.outText)
	s.inText, s.outText = "", ""
}

// reseedText is the recap to seed a context-lost connection with, how
// many entries it holds and its length ("" when there is nothing).
func (s *Session) reseedText() (text string, items, chars int) {
	s.mu.Lock()
	s.flushRecapLocked()
	entries := s.recap.Recent(reseedChars)
	s.mu.Unlock()
	if len(entries) == 0 {
		return "", 0, 0
	}
	lines := voice.RecapLines(entries)
	return reseedPrefix + lines, len(entries), len(lines)
}

// reseed sends the recap on conn — a fresh connection not installed yet,
// so nothing (a queued push, the supervisor's briefing) can go before it
// — as silent client content that does not complete the turn, and logs
// voice.reseeded. A send error is logged in the same event; the
// connection's own reader reports a dead socket.
func (s *Session) reseed(conn Conn) {
	text, items, chars := s.reseedText()
	if text == "" {
		return
	}
	s.sendMu.Lock()
	err := conn.SendClientContent(genai.LiveClientContentInput{
		Turns:        []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: text}}}},
		TurnComplete: genai.Ptr(false),
	})
	s.sendMu.Unlock()
	p := map[string]any{"items": items, "chars": chars, "reason": "not_resumed"}
	if err != nil {
		p["send_error"] = err.Error()
	}
	s.log.Log(logSource, "voice.reseeded", "", p)
}
