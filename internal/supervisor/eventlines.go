package supervisor

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Lines about one worker event (docs/history/bench-2026-10-03.md #15). The
// daemon remembers what it pushed to be spoken about each event (the
// pushes carry the event id, links.go) for two things:
//
//   - the facts of the event's announce turn (announceFacts): when the
//     template sentence went out at brain.announce_wait_s while the brain
//     was still writing, or while its turn was queued, the brain is told
//     that and the exact words, and whether the prompt is still waiting,
//     answered or gone — so it can decide to skip, add or correct
//     instead of announcing the event as news a second time;
//   - the one guard (noteEventLine): a fourth spoken line about the same
//     event within eventLineWindow is dropped.
//
// "Spoken" here is handed to the voice to speak (queued); whether the
// CEO heard it is the voice layer's to report.

// eventLineWindow is the anti-loop window, and eventLineMax the lines
// one event may get inside it.
const (
	eventLineWindow = 30 * time.Second
	eventLineMax    = 3
	// eventLineKeep is how long a line is remembered for the facts: an
	// announce turn lasts at most brain.announce_timeout_s plus grace.
	eventLineKeep = 2 * time.Minute
)

// eventLine is one spoken line pushed about an event.
type eventLine struct {
	text string
	at   time.Time
}

// noteEventLine records a spoken line about the event its tags name and
// reports whether it may go out. Invariant it protects: one worker event
// never turns into a loop of speech. Each path that speaks about an event
// is bounded on its own (one template, at most one late line, one
// reprompt per new prompt), but nothing bounds them together, and a
// line repeated over and over is the one failure the CEO cannot stop
// short of muting. The architecture cannot rule it out because the
// pushes come from independent timelines (announce, late lines, the
// reconciler); so past eventLineMax lines in eventLineWindow the next is
// dropped (supervisor.push_loop_dropped). Not a judgement of what to
// say: the brain decides that with the facts above.
func (s *Supervisor) noteEventLine(paneID, text string, tags map[string]any) bool {
	id, _ := tags[tagEventID].(string)
	if id == "" {
		return true
	}
	s.mu.Lock()
	now := s.cfg.Now()
	if s.eventLines == nil {
		s.eventLines = map[string][]eventLine{}
	}
	for k, lines := range s.eventLines {
		if len(lines) > 0 && now.Sub(lines[len(lines)-1].at) > eventLineKeep {
			delete(s.eventLines, k)
		}
	}
	recent := 0
	for _, l := range s.eventLines[id] {
		if now.Sub(l.at) <= eventLineWindow {
			recent++
		}
	}
	if recent >= eventLineMax {
		s.mu.Unlock()
		s.log.Log(logSource, "supervisor.push_loop_dropped", paneID, map[string]any{"event_id": id, "text": text, "lines": recent, "window_s": int(eventLineWindow.Seconds())})
		return false
	}
	s.eventLines[id] = append(s.eventLines[id], eventLine{text: text, at: now})
	s.mu.Unlock()
	return true
}

// spokenAbout is what was pushed to be spoken about event id, oldest
// first.
func (s *Supervisor) spokenAbout(id string) []string {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, l := range s.eventLines[id] {
		out = append(out, l.text)
	}
	return out
}

// announceFacts is the facts func of a's announce turn (brain.Request
// Facts).
func (s *Supervisor) announceFacts(a announcement) func() brain.EventFacts {
	return func() brain.EventFacts {
		return brain.EventFacts{Spoken: s.spokenAbout(a.eventID), Prompt: s.promptState(a), Language: s.ceoLanguage()}
	}
}

// promptState is the real state of the prompt a block created: still
// open (its pending action, or — when another pane's question took the
// one slot — the pane still blocked at an unanswered question), answered
// by the CEO, or gone (the pane left it, or moved on to another
// prompt); "" when the event created none.
func (s *Supervisor) promptState(a announcement) string {
	if a.pending == "" {
		return ""
	}
	s.mu.Lock()
	answered := s.answeredPrompt[a.row.PaneID] == a.pending
	s.mu.Unlock()
	p, has := s.store.Pending()
	if has && p.ID == a.pending {
		return brain.PromptWaiting
	}
	row, ok := s.store.Get(a.row.PaneID)
	switch {
	case answered:
		return brain.PromptAnswered
	case !ok || row.LastBlock == nil || row.LastBlock.Answered:
		// Left without the shepherd's answer (Store.Apply marks a block
		// answered when the pane leaves it).
		return brain.PromptGone
	case has && p.PaneID == a.row.PaneID:
		// A newer prompt of the same pane replaced it.
		return brain.PromptGone
	case row.State == herdr.StateBlocked:
		return brain.PromptWaiting
	}
	return brain.PromptGone
}
