package state

// ByAlias returns the row whose alias matches.
func (s *Store) ByAlias(alias string) (Agent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.table.Agents {
		if a.Alias == alias {
			return a.clone(), true
		}
	}
	return Agent{}, false
}
