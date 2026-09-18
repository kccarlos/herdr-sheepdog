package web

import (
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

// handleEvents streams the event log as SSE.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	filter := parseFilter(r)
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, ": sheepdog events %s\n\n", s.deps.Version)
	flusher.Flush()

	ctx := r.Context()
	// Subscribe before replaying so nothing logged in between is lost;
	// duplicates are skipped by sequence number.
	live := s.deps.Log.Subscribe(ctx)
	var last uint64
	if since > 0 {
		for _, ev := range s.deps.Log.Tail(since) {
			last = ev.Seq
			if filter.match(ev) {
				writeEvent(w, ev)
			}
		}
		flusher.Flush()
	}

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
			if ev.Seq <= last {
				continue
			}
			last = ev.Seq
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
					if more.Seq > last {
						last = more.Seq
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
	_, _ = fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", ev.Seq, ev.Encoded())
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
