package supervisor

import "github.com/kccarlos/herdr-sheepdog/internal/brain"

// runningInvestigation returns the running job of a pane.
func (s *Supervisor) runningInvestigation(paneID string) (*liveInvestigation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live, ok := s.investigating[paneID]
	return live, ok
}

// BrainHostForTest exposes the supervisor's brain.Host to external tests.
func (s *Supervisor) BrainHostForTest() brain.Host { return brainHost{s} }

// BriefingForTest returns the cached status briefing text for external tests.
func (s *Supervisor) BriefingForTest() string { text, _ := s.briefingText(); return text }

// SyncFactoryAsksForTest refreshes bridged factory asks synchronously for tests.
func (s *Supervisor) SyncFactoryAsksForTest() { s.syncFactoryAsks() }

// ShutdownForTest stops background supervisor goroutines in external tests.
func (s *Supervisor) ShutdownForTest() { s.shutdown() }
