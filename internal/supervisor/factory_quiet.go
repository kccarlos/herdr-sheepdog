package supervisor

import (
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// Factory quiet rule (docs/dev/factory/owner-contract.md §6.1, SDF-23):
// in a factory project, worker, reviewer, checks, helper, and task terminals
// keyed by sheepdog.* labels trigger zero announce turns, zero voice pushes,
// zero pending prompt cards, and zero phone notifications.

const quietReasonFactory = "factory_quiet"

func (s *Supervisor) isFactoryQuietRow(row state.Agent) bool {
	return herdr.IsFactoryQuiet(row.Labels)
}

func (s *Supervisor) isFactoryQuiet(paneID string) bool {
	row, ok := s.store.Get(paneID)
	if !ok {
		return false
	}
	return s.isFactoryQuietRow(row)
}

func isFactoryQuietTransition(t herdr.Transition, prev, row state.Agent) bool {
	return herdr.IsFactoryQuiet(t.Labels) || herdr.IsFactoryQuiet(row.Labels) || herdr.IsFactoryQuiet(prev.Labels)
}

func (s *Supervisor) logFactoryQuiet(paneID, text string) {
	s.log.Log(logSource, "supervisor.push_quiet", paneID, map[string]any{
		"text":       text,
		"chattiness": string(s.chattiness()),
		"reason":     quietReasonFactory,
	})
}

func (s *Supervisor) silenceFactoryAnnounce(a announcement, kept []state.Promise, level Chattiness, recordDecision bool) bool {
	if !s.isFactoryQuietRow(a.row) && !s.isFactoryQuiet(a.row.PaneID) {
		return false
	}
	if recordDecision && s.brain != nil {
		s.logDecision(a, level, decision{via: "ceiling", requested: "", applied: brain.UrgencySkip, reason: quietReasonFactory}, brain.Reply{}, kept, false)
	}
	s.recordFinish(a, "")
	s.logFactoryQuiet(a.row.PaneID, a.sentence)
	return true
}
