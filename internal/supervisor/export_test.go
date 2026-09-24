package supervisor

// runningInvestigation returns the running job of a pane.
func (s *Supervisor) runningInvestigation(paneID string) (*liveInvestigation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live, ok := s.investigating[paneID]
	return live, ok
}
