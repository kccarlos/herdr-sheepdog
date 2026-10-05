package supervisor

import (
	"fmt"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// AgentNow is the voice layer's view of one agent as a briefing reaches
// it (gemini.Config.AgentNow): its spoken name and its state from the
// table, with how long it has been so and when it last finished work.
// Information, not judgement: the fast brain has no clock and cannot
// see the screens, so a briefing read minutes later could otherwise
// pass for the present (phone test 2026-10-04: "still analysing" about
// an agent that had finished, or had not started). ok is false for an
// alias the table does not know.
func (s *Supervisor) AgentNow(alias string) (name, st string, ok bool) {
	row, found := findAlias(s.store.List(), alias)
	if !found {
		return "", "", false
	}
	name = state.Say(row)
	if name == "" {
		name = aliasOr(row.Alias)
	}
	return name, agentState(row, s.cfg.Now()), true
}

// agentState is the state sentence of AgentNow.
func agentState(a state.Agent, now time.Time) string {
	since := ""
	if !a.StateSince.IsZero() && now.After(a.StateSince) {
		since = " for " + spokenAge(now.Sub(a.StateSince))
	}
	var st string
	switch a.State {
	case herdr.StateWorking:
		st = "working" + since
	case herdr.StateBlocked:
		st = "waiting at a prompt for an answer" + since
	case herdr.StateIdle, herdr.StateDone:
		st = "idle, ready for input," + since
		if since == "" {
			st = "idle, ready for input"
		}
	default:
		return "no longer running"
	}
	if a.LastResult != nil && !a.LastResult.At.IsZero() {
		st += fmt.Sprintf("; it last finished work %s ago", spokenAge(now.Sub(a.LastResult.At)))
	} else if a.State == herdr.StateIdle || a.State == herdr.StateDone {
		st += "; no finished work recorded"
	}
	return "as this reached you: " + st
}

// spokenAge renders a duration coarsely for a model's ear: "45 s",
// "4 min", "2 h 5 min".
func spokenAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	default:
		h := int(d.Hours())
		if m := int(d.Minutes()) - 60*h; m > 0 {
			return fmt.Sprintf("%d h %d min", h, m)
		}
		return fmt.Sprintf("%d h", h)
	}
}
