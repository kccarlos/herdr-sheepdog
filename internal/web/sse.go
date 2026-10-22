package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// eventFilter is the ?source= / ?pane= / ?type= selection of /events.
type eventFilter struct {
	sources map[string]bool
	pane    string
	typ     string // prefix
}

func parseFilter(r *http.Request) eventFilter {
	f := eventFilter{pane: r.URL.Query().Get("pane"), typ: r.URL.Query().Get("type")}
	if src := r.URL.Query().Get("source"); src != "" {
		f.sources = map[string]bool{}
		for _, s := range strings.Split(src, ",") {
			if s = strings.TrimSpace(s); s != "" {
				f.sources[s] = true
			}
		}
	}
	return f
}

func (f eventFilter) match(ev eventlog.Event) bool {
	if f.sources != nil && !f.sources[ev.Source] {
		return false
	}
	if f.pane != "" && ev.PaneID != f.pane {
		return false
	}
	if f.typ != "" && !strings.HasPrefix(ev.Type, f.typ) {
		return false
	}
	return true
}

// minEventID tells an event id apart from the older ?since=<count>: any
// real id is a time in Unix microseconds (~1.8e15), far above this.
const minEventID = 1_000_000_000_000

// replayFrom reads where a stream resumes: after the id in Last-Event-ID
// (what a reconnecting EventSource sends) or ?since=<id>, or else the
// last n events of this run's ring (?n=<count>, or the older
// ?since=<count>). after is -1 when there is no id.
func replayFrom(r *http.Request) (after int64, n int) {
	if h := strings.TrimSpace(r.Header.Get("Last-Event-ID")); h != "" {
		if id, err := strconv.ParseInt(h, 10, 64); err == nil && id > 0 {
			return id, 0
		}
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64); err == nil && v > 0 {
		if v >= minEventID {
			return v, 0
		}
		n = int(v)
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 {
		n = v
	}
	return -1, n
}

// handleEvents streams the event log as SSE. Every frame's id: is the
// event's stable id, so a client resumes with Last-Event-ID (or
// ?since=<id>) and gets what it missed from the log — across day files
// and restarts, up to eventlog.DefaultReplayMax events — then the live
// stream.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	filter := parseFilter(r)
	after, n := replayFrom(r)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, ": sheepdog events %s\n\n", s.deps.Version)
	flusher.Flush()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer s.track(r, cancel)()
	// Subscribe before replaying so nothing logged in between is lost;
	// duplicates are skipped by id.
	live := s.deps.Log.Subscribe(ctx)
	var last int64
	var replay []eventlog.Event
	switch {
	case after >= 0:
		last = after
		evs, truncated, err := s.deps.Log.Since(after, eventlog.DefaultReplayMax)
		if err != nil {
			_, _ = fmt.Fprintf(w, ": replay error %s\n\n", strings.ReplaceAll(err.Error(), "\n", " "))
		}
		if truncated {
			_, _ = fmt.Fprintf(w, ": replay truncated to the newest %d events\n\n", eventlog.DefaultReplayMax)
		}
		replay = evs
	case n > 0:
		replay = s.deps.Log.Tail(n)
	}
	for _, ev := range replay {
		last = max(last, ev.ID)
		if filter.match(ev) {
			writeEvent(w, ev)
		}
	}
	flusher.Flush()

	heartbeat := time.NewTicker(s.deps.Heartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closing:
			_, _ = fmt.Fprint(w, "event: closed\ndata: {\"reason\":\"shutdown\"}\n\n")
			flusher.Flush()
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case ev, ok := <-live:
			if !ok {
				// Dropped as a slow subscriber (or the log closed); the
				// browser's EventSource reconnects on its own.
				_, _ = fmt.Fprint(w, "event: closed\ndata: {\"reason\":\"dropped\"}\n\n")
				flusher.Flush()
				return
			}
			if ev.ID <= last {
				continue
			}
			last = ev.ID
			if !filter.match(ev) {
				continue
			}
			writeEvent(w, ev)
			// Drain whatever else is ready before flushing once.
			for drained := true; drained; {
				select {
				case more, ok := <-live:
					if !ok {
						drained = false
						continue
					}
					if more.ID > last {
						last = more.ID
						if filter.match(more) {
							writeEvent(w, more)
						}
					}
				default:
					drained = false
				}
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, ev eventlog.Event) {
	_, _ = fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", ev.ID, ev.Encoded())
}

// handleEventsTail answers the last n events as a JSON array (n defaults
// to 100), with the same filters as /events.
func (s *Server) handleEventsTail(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 {
		n = 100
	}
	filter := parseFilter(r)
	out := make([]eventlog.Event, 0, n)
	for _, ev := range s.deps.Log.Tail(0) {
		if filter.match(ev) {
			out = append(out, ev)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	writeJSON(w, http.StatusOK, out)
}
