package gemini

import (
	"context"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// GoAway handover on a backend that allows one connection per session.
//
// A GoAway first tries make-before-break (proactiveReconnect): dial the
// new connection with the handle while the old one still works, then
// swap. AI Studio refuses that dial with close 1008 "BidiGenerateContent
// session is already connected to an existing client" (live log
// 2026-10-12..20, every hourly GoAway; scratch repro 2026-10-20: the same
// dial succeeds right after the old connection is closed, and a fresh
// dial is not refused). Before this, the old connection then ran until
// the GoAway deadline, closed with 1011 "Deadline expired", and the 1011
// rule dropped the handle, so every GoAway on AI Studio lost the live
// context. Now the refused dial schedules a break-before-make handover:
// at the next quiet moment (quietLocked), or handoverMargin before the
// deadline at the latest, the old connection is closed and the session
// resumes with the same handle; audio in the gap is buffered and
// replayed. If the deadline 1011 still comes first, the handle is kept
// (afterGoAwayLocked) and the reconnect resumes with it.
const (
	// handoverMargin is how long before the GoAway deadline the handover
	// happens even if the moment is not quiet.
	handoverMargin = 5 * time.Second
	// handoverNoDeadline bounds the wait for a quiet moment when the
	// refused dial had no GoAway deadline (a recycle).
	handoverNoDeadline = 30 * time.Second
	// maxRefusedResumes is how many "already connected" refusals a
	// reconnect retries with the handle before counting them as failures.
	maxRefusedResumes = 3
	// handoverMessage explains voice.handover in the log.
	handoverMessage = "the server allows one connection per session: closing the old connection, then resuming with the same handle"
)

// isAlreadyConnected reports whether a dial was refused because the
// session's previous connection is still open (AI Studio, close 1008).
func isAlreadyConnected(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already connected to an existing client")
}

// noteGoAwayLocked records a GoAway on connection gen: its deadline,
// for the handover and the deadline-1011 rule. Caller holds s.mu.
func (s *Session) noteGoAwayLocked(gen int, left time.Duration) {
	if gen != s.connGen {
		return
	}
	s.goAwayGen = gen
	s.goAwayDeadline = s.now().Add(left)
}

// afterGoAwayLocked reports whether connection gen had received a
// GoAway: its close at the deadline (1011 "Deadline expired") is the
// expected end of the connection, not a sign the handle is dead. Caller
// holds s.mu.
func (s *Session) afterGoAwayLocked(gen int) bool {
	return gen != 0 && gen == s.goAwayGen
}

// scheduleHandover is called when a proactive dial was refused because
// the old connection is still open.
func (s *Session) scheduleHandover(why string) {
	s.mu.Lock()
	now := s.now()
	due := now.Add(handoverNoDeadline)
	if s.goAwayGen == s.connGen && !s.goAwayDeadline.IsZero() {
		due = s.goAwayDeadline.Add(-handoverMargin)
	}
	s.handoverDue, s.handoverWhy = due, why
	s.mu.Unlock()
	s.log.Log(logSource, "voice.handover", "", map[string]any{
		"step": "scheduled", "why": why, "within_ms": max(due.Sub(now).Milliseconds(), 0), "message": handoverMessage,
	})
}

// maybeHandover asks the run loop for the handover once one is due and
// the moment is quiet, or the deadline is near (refresh loop tick).
func (s *Session) maybeHandover() {
	s.mu.Lock()
	due := s.handoverDue
	now := s.now()
	ready := !due.IsZero() && s.conn != nil && (s.quietLocked(now) || !now.Before(due))
	s.mu.Unlock()
	if !ready {
		return
	}
	select {
	case s.handoverReq <- struct{}{}:
	default:
	}
}

// handover closes the live connection and reconnects with the same
// handle (run loop).
func (s *Session) handover(ctx context.Context, backoff *time.Duration) bool {
	s.mu.Lock()
	if s.handoverDue.IsZero() || s.conn == nil {
		s.mu.Unlock()
		return true
	}
	now := s.now()
	quiet := s.quietLocked(now)
	why := s.handoverWhy
	s.handoverDue, s.handoverWhy = time.Time{}, ""
	old := s.conn
	s.conn = nil
	s.connGen++ // the old reader's exit is stale from here
	s.status = voice.StatusReconnecting
	handle := s.handle != ""
	s.mu.Unlock()
	_ = old.Close()
	s.log.Log(logSource, "voice.handover", "", map[string]any{"step": "closing", "why": why, "quiet": quiet, "handle": handle})
	return s.reconnect(ctx, backoff)
}
