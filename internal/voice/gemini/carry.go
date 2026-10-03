package gemini

import "github.com/kccarlos/herdr-sheepdog/internal/voice"

// RecapEntries is the recent conversation (the shepherd's pending words
// and the reply so far included), for a provider switch to carry over
// (voice.Switcher).
func (s *Session) RecapEntries() []voice.RecapEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushRecapLocked()
	return s.recap.Entries()
}

// SeedRecap takes over another provider's recent conversation: it goes
// into this session's recap (so a later re-seed carries it too) and is
// sent at once on the live connection as silent context, logged
// voice.reseeded {reason: provider_switch}.
func (s *Session) SeedRecap(entries []voice.RecapEntry) {
	s.mu.Lock()
	s.recap.Seed(entries)
	conn := s.conn
	s.mu.Unlock()
	text, items, chars := s.recapText(switchPrefix)
	s.sendRecap(conn, text, items, chars, "provider_switch")
}

// RecapFocus is the supervisor's latest current-focus line (TagFocus),
// for a provider switch to carry over.
func (s *Session) RecapFocus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recap.Focus()
}

// SeedFocus takes over another provider's current-focus line: the next
// recap sent (SeedRecap, a re-seed) ends with it.
func (s *Session) SeedFocus(text string) {
	s.mu.Lock()
	s.recap.SetFocus(text)
	s.mu.Unlock()
}
