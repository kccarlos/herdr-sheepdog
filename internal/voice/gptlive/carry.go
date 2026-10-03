package gptlive

import "github.com/kccarlos/herdr-sheepdog/internal/voice"

// RecapEntries is the recent conversation (the utterance and reply in
// progress included), for a provider switch to carry over
// (voice.Switcher).
func (s *Session) RecapEntries() []voice.RecapEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addRecapLocked(voice.RecapUser, s.inText)
	s.inText = ""
	s.flushReplyLocked()
	return s.recap.Entries()
}

// SeedRecap takes over another provider's recent conversation: the next
// billed session opens with it as its history (recapItemsLocked).
func (s *Session) SeedRecap(entries []voice.RecapEntry) {
	s.mu.Lock()
	s.recap.Seed(entries)
	s.recapCarried = len(entries) > 0
	s.mu.Unlock()
}

// RecapFocus is the supervisor's latest current-focus line (TagFocus),
// for a provider switch to carry over.
func (s *Session) RecapFocus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recap.Focus()
}

// SeedFocus takes over another provider's current-focus line: the next
// billed session opens with it (recapItemsLocked).
func (s *Session) SeedFocus(text string) {
	s.mu.Lock()
	s.recap.SetFocus(text)
	s.mu.Unlock()
}
