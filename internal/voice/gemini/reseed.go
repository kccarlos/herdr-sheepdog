package gemini

import (
	"fmt"
	"strings"

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

// switchPrefix introduces a recap carried over from another voice
// provider (voice.Switcher).
const switchPrefix = "[system context, not spoken by the user] You have just taken over this voice conversation from another voice model. " +
	"This is the recent conversation, oldest first, only so you can follow up on it. " +
	"Do not answer it, read it out or mention it; carry on and wait for the user.\n"

// reseedPrefix introduces the recap to the model: context only, never
// something to answer or read out.
const reseedPrefix = "[system context, not spoken by the user] The voice connection dropped and came back without its history. " +
	"This is the recent conversation, oldest first, only so you can follow up on it. " +
	"Do not answer it, read it out or mention it; carry on and wait for the user.\n"

// flushRecapLocked moves the user's pending words and the voice's
// reply so far into the recap. Caller holds s.mu.
func (s *Session) flushRecapLocked() {
	s.recap.Add(voice.RecapUser, s.inText)
	s.recap.Add(voice.RecapAssistant, s.outText)
	s.inText, s.outText = "", ""
}

// reseedText is the recap to seed a context-lost connection with, how
// many entries it holds and its length ("" when there is nothing).
func (s *Session) reseedText() (text string, items, chars int) {
	return s.recapText(reseedPrefix)
}

// recapText is the recap under prefix, how many entries it holds and
// the length of its lines ("" when there is nothing).
func (s *Session) recapText(prefix string) (text string, items, chars int) {
	s.mu.Lock()
	s.flushRecapLocked()
	entries := s.recap.Recent(reseedChars)
	focus := s.recap.Focus()
	s.mu.Unlock()
	if len(entries) == 0 && focus == "" {
		return "", 0, 0
	}
	lines := voice.RecapLines(entries)
	if focus != "" {
		// The supervisor's current-focus line (voice.TagFocus): the
		// old connection had it as silent context, the fresh one
		// learns it here.
		lines = strings.TrimSpace(lines + "\n" + focus)
	}
	return prefix + lines, len(entries), len(lines)
}

// reseed sends the recap on conn — a fresh connection not installed yet,
// so nothing (a queued push, the supervisor's briefing) can go before it
// — as silent client content that does not complete the turn, and logs
// voice.reseeded. A send error is logged in the same event; the
// connection's own reader reports a dead socket.
func (s *Session) reseed(conn Conn) {
	text, items, chars := s.reseedText()
	s.sendRecap(conn, text, items, chars, s.freshReason())
}

// freshReason is why a reconnect came back without its context:
// reasonResumedTwice when the session left out a handle from a resumed
// connection (handle.go), else "not_resumed".
func (s *Session) freshReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.freshWhy != "" {
		return s.freshWhy
	}
	return "not_resumed"
}

// The recap carries the conversation and the spoken briefings (cut to
// its budget), not the silent ones: the roster, names, delivery facts,
// a orchestrator result sent as context. So a fresh connection is also
// given the most recent briefings again, silently, each marked as
// re-sent with its first time (the supervisor's roster briefing on
// EventResumed follows). A "New conversation" clears the ledger first.
const (
	// reseedBriefs bounds how many briefings are re-sent.
	reseedBriefs = 8
	// reseedBriefChars bounds their facts in all (newest kept).
	reseedBriefChars = 3000
)

// reseedBriefingsLocked picks the briefings to re-send, oldest first:
// the newest ones within the bounds, not the current-focus lines (the
// recap ends with the latest focus). Caller holds s.mu.
func (s *Session) reseedBriefingsLocked() []briefRec {
	var out []briefRec
	chars := 0
	for i := len(s.brief.recs) - 1; i >= 0 && len(out) < reseedBriefs; i-- {
		r := s.brief.recs[i]
		if r.kind == "current focus" {
			continue
		}
		n := len(fmt.Sprint(r.payload["facts"]))
		if chars+n > reseedBriefChars && len(out) > 0 {
			break
		}
		chars += n
		out = append(out, r)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// reseedBriefings sends the recent briefings on conn — a fresh
// connection not installed yet — as SILENT function responses, logging
// voice.briefing_resent {reason} for each. Briefing channel only.
func (s *Session) reseedBriefings(conn Conn) {
	if conn == nil || !s.useBriefings() {
		return
	}
	reason := s.freshReason()
	s.mu.Lock()
	recs := s.reseedBriefingsLocked()
	s.mu.Unlock()
	for _, r := range recs {
		rr := r
		rr.payload = s.resentPayload(r, "re-sent because the voice connection restarted without its history; first sent at "+clock(r.at)+"; a newer briefing supersedes it")
		s.sendMu.Lock()
		err := conn.SendToolResponse(briefingResponse(rr, voice.UrgencySilent))
		s.sendMu.Unlock()
		s.log.Log(logSource, "voice.briefing_resent", "", map[string]any{"briefing": r.id, "at": clock(r.at), "reason": reason, "error": errString(err)})
		if err != nil {
			return
		}
	}
}

// sendRecap sends a rendered recap on conn as silent client content
// that does not complete the turn, and logs voice.reseeded {reason}.
func (s *Session) sendRecap(conn Conn, text string, items, chars int, reason string) {
	if text == "" || conn == nil {
		return
	}
	s.sendMu.Lock()
	err := conn.SendClientContent(genai.LiveClientContentInput{
		Turns:        []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: text}}}},
		TurnComplete: genai.Ptr(false),
	})
	s.sendMu.Unlock()
	p := map[string]any{"items": items, "chars": chars, "reason": reason}
	if err != nil {
		p["send_error"] = err.Error()
	}
	s.log.Log(logSource, "voice.reseeded", "", p)
}
