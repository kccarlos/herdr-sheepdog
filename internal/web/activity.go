package web

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// The voice session's Live Activity, kept current while the app is away
// (docs/dev/push.md "Live Activity updates"). The app registers its
// activity's push token; while the device holds a connection the app
// updates the banner itself, and once it has had none for activityGrace
// the daemon pushes each change of the waiting agents and the counts.

const (
	// activityGrace is how long a device must be away before the daemon
	// pushes: a reconnect or a short background trip is the app's own.
	activityGrace = 30 * time.Second
	// activityTick is how often the daemon looks for changes.
	activityTick = 5 * time.Second
	// activityLifetime is when iOS has ended the activity for sure (8
	// hours active): the token is forgotten.
	activityLifetime = 8 * time.Hour
	// activityStale is when a pushed banner dims as out of date.
	activityStale = 30 * time.Minute
	// The cuts the app makes too (LiveActivityContent).
	activityQuestionLimit = 90
	activityNameLimit     = 32
)

// pushActivityRequest is the body of POST /api/push/activity.
type pushActivityRequest struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
}

// handlePushActivity stores the device's Live Activity push token.
func (s *Server) handlePushActivity(w http.ResponseWriter, r *http.Request) {
	if s.deps.Push == nil {
		writeError(w, http.StatusServiceUnavailable, "push not available")
		return
	}
	var req pushActivityRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	dev := deviceOf(r)
	if dev == localDevice {
		dev = ""
	}
	if err := s.deps.Push.RegisterActivity(dev, req.Token, req.Environment); err != nil {
		if errors.Is(err, push.ErrNoDevice) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writePushError(w, err)
		return
	}
	s.activityWatch.reset(dev)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handlePushActivityEnd forgets the device's Live Activity (it ended).
func (s *Server) handlePushActivityEnd(w http.ResponseWriter, r *http.Request) {
	if s.deps.Push == nil {
		writeError(w, http.StatusServiceUnavailable, "push not available")
		return
	}
	s.deps.Push.ForgetActivity(deviceOf(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// activityWatch is what the daemon last knew each device's banner to
// show, and when the device was last connected.
type activityWatch struct {
	mu    sync.Mutex
	seen  map[string]time.Time
	known map[string]push.ActivityState
}

func (w *activityWatch) reset(dev string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.seen, dev)
	delete(w.known, dev)
}

// watchActivities pushes Live Activity updates until ctx ends.
func (s *Server) watchActivities(ctx context.Context) {
	if s.deps.Push == nil {
		return
	}
	t := time.NewTicker(activityTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.activityStep(ctx)
		}
	}
}

// activityStep looks at every registered activity once and pushes what
// changed for devices that have been away long enough.
func (s *Server) activityStep(ctx context.Context) {
	acts := s.deps.Push.Activities()
	if len(acts) == 0 {
		return
	}
	now := s.deps.Now()
	snap := s.activityState()
	w := &s.activityWatch
	for _, a := range acts {
		if now.Sub(a.At) > activityLifetime {
			s.deps.Push.ForgetActivity(a.DeviceID)
			w.reset(a.DeviceID)
			continue
		}
		w.mu.Lock()
		if w.seen == nil {
			w.seen, w.known = map[string]time.Time{}, map[string]push.ActivityState{}
		}
		if s.deviceConnected(a.DeviceID) {
			// The app updates the banner itself.
			w.seen[a.DeviceID], w.known[a.DeviceID] = now, snap
			w.mu.Unlock()
			continue
		}
		seen, ok := w.seen[a.DeviceID]
		if !ok {
			seen = a.At
		}
		known, had := w.known[a.DeviceID]
		if now.Sub(seen) < activityGrace || (had && reflect.DeepEqual(known, snap)) {
			w.mu.Unlock()
			continue
		}
		w.known[a.DeviceID] = snap
		w.mu.Unlock()
		u := push.ActivityUpdate{State: snap, Stale: now.Add(activityStale), Urgent: snap.Waiting > known.Waiting}
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_ = s.deps.Push.UpdateActivity(pctx, a.DeviceID, u)
		cancel()
	}
}

// deviceConnected reports whether the paired device holds a live
// connection (/ws, /term, /events).
func (s *Server) deviceConnected(dev string) bool {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	return len(s.live.conns[dev]) > 0
}

// activityState is what the banner should show with the app away: the
// voice is not listening, up to three waiting agents with their questions
// (the open questions' order first, then by how long they have been
// blocked), and the counts.
func (s *Server) activityState() push.ActivityState {
	agents := s.deps.Store.List()
	questions := s.deps.Store.Questions()
	st := push.ActivityState{Phase: push.PhaseAway, Rows: []push.ActivityRow{}}
	var blocked []state.Agent
	for _, a := range agents {
		switch a.State {
		case herdr.StateBlocked:
			st.Waiting++
			blocked = append(blocked, a)
		case herdr.StateWorking:
			st.Working++
		case herdr.StateDone:
			st.Done++
		}
	}
	order := map[string]int{}
	for i, q := range questions {
		if _, ok := order[q.PaneID]; !ok {
			order[q.PaneID] = i
		}
	}
	sortBlocked(blocked, order)
	for _, a := range blocked {
		if len(st.Rows) == push.ActivityRowLimit {
			break
		}
		var q *state.PendingAction
		for i := range questions {
			if questions[i].PaneID == a.PaneID {
				q = &questions[i]
				break
			}
		}
		text := ""
		if q != nil {
			text = firstLine(q.Question, q.SpokenSummary, q.Command)
		}
		if text == "" && a.LastBlock != nil && !a.LastBlock.Answered {
			text = firstLine(a.LastBlock.Question)
		}
		if text == "" {
			text = "Needs you"
		}
		name := clipLine(a.Alias, activityNameLimit)
		if name == "" {
			name = a.PaneID
		}
		st.Rows = append(st.Rows, push.ActivityRow{Pane: a.PaneID, Agent: name, Question: clipLine(text, activityQuestionLimit)})
	}
	return st
}

// sortBlocked orders blocked agents: those with an open question in its
// order, then by how long they have been blocked, then by alias.
func sortBlocked(agents []state.Agent, order map[string]int) {
	less := func(a, b state.Agent) bool {
		x, xok := order[a.PaneID]
		y, yok := order[b.PaneID]
		switch {
		case xok && yok:
			return x < y
		case xok != yok:
			return xok
		case !a.StateSince.Equal(b.StateSince):
			return a.StateSince.Before(b.StateSince)
		default:
			return a.Alias < b.Alias
		}
	}
	for i := 1; i < len(agents); i++ {
		for j := i; j > 0 && less(agents[j], agents[j-1]); j-- {
			agents[j], agents[j-1] = agents[j-1], agents[j]
		}
	}
}

// firstLine is the first non-empty text, whitespace folded.
func firstLine(texts ...string) string {
	for _, t := range texts {
		if f := strings.Join(strings.Fields(t), " "); f != "" {
			return f
		}
	}
	return ""
}

// clipLine folds whitespace and cuts to limit characters, the last one an
// ellipsis.
func clipLine(text string, limit int) string {
	f := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(f) <= limit {
		return f
	}
	r := []rune(f)
	return strings.TrimRight(string(r[:limit-1]), " ") + "…"
}
