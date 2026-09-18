package supervisor

import (
	"fmt"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// Chattiness is the push policy: how much the supervisor volunteers
// without being asked (README "How chatty is it?").
type Chattiness string

// The three levels, quietest first.
const (
	// Quiet pushes only what needs the user: an agent's question (blocked)
	// and an agent exiting or crashing while busy.
	Quiet Chattiness = "quiet"
	// Normal adds informational blocks, finishes (with a one-line result)
	// of agents the user dealt with recently, and the "dropping the earlier
	// question" replacement phrasing.
	Normal Chattiness = "normal"
	// Chatty adds every finish and "<alias> started on …" when an
	// instruction the user sent is picked up.
	Chatty Chattiness = "chatty"
)

// Levels lists the accepted values in order.
var Levels = []Chattiness{Quiet, Normal, Chatty}

// ParseChattiness accepts a level name, case-insensitively.
func ParseChattiness(s string) (Chattiness, error) {
	v := Chattiness(strings.ToLower(strings.TrimSpace(s)))
	for _, l := range Levels {
		if v == l {
			return l, nil
		}
	}
	return "", fmt.Errorf("supervisor: chattiness %q: want quiet, normal or chatty", s)
}

// pushKind classifies a proactive push for the policy table.
type pushKind int

const (
	// pushQuestion: an agent is asking something (blocked with a question,
	// or a done screen that ends in a question).
	pushQuestion pushKind = iota
	// pushExit: an agent exited, crashed or lost its pane while busy.
	pushExit
	// pushBlockedInfo: blocked, but not a question (trust dialog, idle
	// prompt, error), or the screen could not be read.
	pushBlockedInfo
	// pushReplacement: the "dropping the earlier question …" phrasing when
	// a new question displaces an unanswered one.
	pushReplacement
	// pushDoneInvolved: a finish of an agent the user dealt with recently.
	pushDoneInvolved
	// pushDoneOther: a finish of an agent the user has not dealt with.
	pushDoneOther
	// pushStarted: an instruction the user sent was picked up.
	pushStarted
	// pushPromiseOnly: a working/idle sentence that is never announced
	// on its own, only spoken as a kept promise.
	pushPromiseOnly
)

// allows is the policy table: which pushes each level speaks. "idle
// again" is never announced at any level.
func (l Chattiness) allows(k pushKind) bool {
	switch k {
	case pushQuestion, pushExit:
		return true
	case pushBlockedInfo, pushReplacement, pushDoneInvolved:
		return l != Quiet
	case pushDoneOther, pushStarted:
		return l == Chatty
	default:
		return false
	}
}

// Settings is the runtime-changeable part of the supervisor's behaviour.
type Settings struct {
	Chattiness Chattiness `json:"chattiness"`
}

// Settings returns the current runtime settings.
func (s *Supervisor) Settings() Settings {
	return Settings{Chattiness: s.chattiness()}
}

// SetSettings applies new runtime settings: the level is validated,
// persisted in the state file's session (so a restart keeps it) and
// logged as supervisor.settings.
func (s *Supervisor) SetSettings(in Settings) error {
	level, err := ParseChattiness(string(in.Chattiness))
	if err != nil {
		return err
	}
	old := s.chattiness()
	if err := s.store.UpdateSession(func(ss *state.Session) { ss.Chattiness = string(level) }); err != nil {
		return err
	}
	s.log.Log(logSource, "supervisor.settings", "", map[string]any{"chattiness": string(level), "previous": string(old)})
	return nil
}

// chattiness returns the effective level: the session's runtime choice,
// else the configured initial value.
func (s *Supervisor) chattiness() Chattiness {
	if v := s.store.Snapshot().Session.Chattiness; v != "" {
		if l, err := ParseChattiness(v); err == nil {
			return l
		}
	}
	return s.cfg.chattiness
}

// allows reports whether the current level speaks pushes of kind k.
func (s *Supervisor) allows(k pushKind) bool { return s.chattiness().allows(k) }

// doneKind classifies a finish: involved when the user dealt with the
// agent within RecentWindow (state.Agent.LastInteraction, or a spoken
// briefing that named it).
func (s *Supervisor) doneKind(row state.Agent) pushKind {
	now := s.cfg.Now()
	s.mu.Lock()
	mentioned, ok := s.mentioned[row.PaneID]
	s.mu.Unlock()
	if ok && now.Sub(mentioned) <= s.cfg.RecentWindow {
		return pushDoneInvolved
	}
	if row.LastInteraction != nil && now.Sub(*row.LastInteraction) <= s.cfg.RecentWindow {
		return pushDoneInvolved
	}
	return pushDoneOther
}

// touch records that the user dealt with paneID just now.
func (s *Supervisor) touch(paneID string) {
	now := s.cfg.Now()
	if _, err := s.store.Update(paneID, func(a *state.Agent) { a.LastInteraction = &now }); err != nil {
		s.log.Log(logSource, "supervisor.state_error", paneID, map[string]any{"error": err.Error()})
	}
}
