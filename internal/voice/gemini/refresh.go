package gemini

import (
	"context"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Session refresh (docs/dev/history/bench-2026-10-03.md #9). A Gemini Live session
// resumed again and again degrades: in run 1 (2026-10-02), an 18-hour
// session resumed ~120 times voiced 5 % of push bodies and relayed 17 of
// 17 Chinese push questions back through to_supervisor, while run 2's
// fresh session voiced them whole (median coverage 100 %, no
// self-relays). So instead of resuming, the session starts over — the
// existing context-lost path: no handle, the recap re-seed (reseed.go),
// then the supervisor's silent briefing on EventResumed{ContextLost} —
// when
//   - a client attaches after more than refreshAway with none, or
//   - the model session has been alive (resumed throughout) longer than
//     refreshMaxAge,
//
// at the next quiet moment: the model is not generating, the client is
// not playing, the user is not speaking, no tool call or reply is
// in flight and no question is waiting for the user (the
// supervisor's open check). Logged voice.session_refreshed {reason,
// age_s}. These are runaway guards on the session's lifetime, not
// tuning, so they are constants. Since a GoAway on a resumed connection
// opens a fresh session anyway (handle.go, "never resume a resumed
// session"), freshAt moves every second GoAway (~18 min) and the max-age
// refresh rarely fires; it stays as the guard.
const (
	refreshAway   = 30 * time.Minute
	refreshMaxAge = 2 * time.Hour
	// refreshOpenMax bounds how long a due refresh waits for an open
	// question to be answered: a prompt can stay open for hours.
	refreshOpenMax = 10 * time.Minute
	// owedReplyWait bounds how long a to_supervisor call answered
	// "pending" counts as still owed its reply (the orchestrator's utterance
	// budget is 25 s, plus a 3 s final grace).
	owedReplyWait = 35 * time.Second
	// refreshEvery is how often a due refresh looks for a quiet moment.
	refreshEvery = time.Second
)

// Refresh reasons (voice.session_refreshed).
const (
	refreshClientAway = "client_away"
	refreshAge        = "max_age"
)

// refreshRequest asks the run loop for a fresh connection.
type refreshRequest struct {
	reason string
	age    time.Duration
}

// SetOpenCheck installs the supervisor's open check (voice.OpenCheck):
// asked with nil tags whether a question waits for the user (a due
// refresh then holds off), and with a push's tags whether what it is
// about is still open.
func (s *Session) SetOpenCheck(f voice.OpenCheck) {
	s.mu.Lock()
	s.openCheck = f
	s.mu.Unlock()
}

// refreshLoop looks for a quiet moment once a refresh is due, and gives
// the heard watch (heard.go) its timed checks.
func (s *Session) refreshLoop(ctx context.Context) {
	tick := time.NewTicker(refreshEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.maybeHandover()
			s.maybeRefresh()
		}
	}
}

// noteAttachLocked records a client attaching or leaving and marks a
// refresh due when one comes back after more than refreshAway with
// none, unless the session already started fresh while it was away.
// Caller holds s.mu.
func (s *Session) noteAttachLocked(now time.Time, attached, changed bool) {
	if !changed {
		return
	}
	if !attached {
		if s.everAttached {
			s.clientGoneAt = now
		}
		return
	}
	s.everAttached = true
	if !s.clientGoneAt.IsZero() && now.Sub(s.clientGoneAt) > refreshAway && s.freshAt.Before(s.clientGoneAt) {
		s.markRefreshLocked(now, refreshClientAway)
	}
	s.clientGoneAt = time.Time{}
}

// markRefreshLocked marks a refresh due (the first reason wins).
func (s *Session) markRefreshLocked(now time.Time, reason string) {
	if s.refreshDue == "" {
		s.refreshDue, s.refreshDueAt = reason, now
	}
}

// quietLocked reports whether nothing is in flight that a fresh
// connection would cut: generation, playback, the user's speech, a
// tool call, or a reply the voice was told follows. Caller holds s.mu.
func (s *Session) quietLocked(now time.Time) bool {
	if s.conn == nil || s.status != voice.StatusConnected || s.turnOpen || s.speakingLocked(now) {
		return false
	}
	if s.speakingUntilLocked(now) > 0 || len(s.inflight) > 0 {
		return false
	}
	for id, at := range s.owedReplies {
		if now.Sub(at) < owedReplyWait {
			return false
		}
		delete(s.owedReplies, id)
	}
	return true
}

// maybeRefresh asks the run loop for a fresh connection when a refresh
// is due and the moment is quiet.
func (s *Session) maybeRefresh() {
	s.mu.Lock()
	now := s.now()
	if s.refreshing || !s.started || s.status != voice.StatusConnected {
		s.mu.Unlock()
		return
	}
	if s.refreshDue == "" && !s.freshAt.IsZero() && now.Sub(s.freshAt) > refreshMaxAge {
		s.markRefreshLocked(now, refreshAge)
	}
	reason, dueAt, check := s.refreshDue, s.refreshDueAt, s.openCheck
	quiet := reason != "" && s.quietLocked(now)
	s.mu.Unlock()
	if !quiet {
		return
	}
	if check != nil && check(nil) && now.Sub(dueAt) < refreshOpenMax {
		return
	}
	s.mu.Lock()
	if s.refreshing || s.refreshDue != reason {
		s.mu.Unlock()
		return
	}
	s.refreshing = true
	s.refreshDue = ""
	age := now.Sub(s.freshAt)
	s.mu.Unlock()
	select {
	case s.refreshReq <- refreshRequest{reason: reason, age: age}:
	default:
		s.mu.Lock()
		s.refreshing = false
		s.markRefreshLocked(now, reason)
		s.mu.Unlock()
	}
}

// refresh replaces the live connection with a fresh one (run loop). On
// a failed dial the old connection stays and the refresh is due again.
func (s *Session) refresh(ctx context.Context, r refreshRequest) {
	s.dropHandle("session_refresh")
	ok := s.proactiveReconnect(ctx, "refresh")
	s.mu.Lock()
	s.refreshing = false
	if !ok {
		s.markRefreshLocked(s.now(), r.reason)
	}
	s.mu.Unlock()
	if ok {
		s.log.Log(logSource, "voice.session_refreshed", "", map[string]any{"reason": r.reason, "age_s": int(r.age.Seconds())})
	}
	s.poke()
}
