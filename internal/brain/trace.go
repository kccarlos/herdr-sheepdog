package brain

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// Turn and utterance ids on the brain's events (docs/architecture.md
// "Event ids"). Every event a turn logs carries its number as payload
// "turn" (brain.step, brain.tool_call, brain.tool_result, brain.yielded,
// brain.fallback, brain.turn, brain.compaction, brain.transcript,
// context.step), so one decorator on the brain's logger stamps them all:
// it adds turn_id — a string unique across daemon runs, which the event
// log lifts to the line's top level — and the utterance_id of the CEO
// utterance the turn answers (Request.UtteranceID), with no change to
// any Log call. brain.turn_started marks the moment the turn lock was
// taken: its queue_ms is the wait for it, and it carries the worker
// event id an announce turn serves (Request.Link "evt:<id>").
//
// The decorator also knows which turn is running, for the daemon
// watchdog (RunningTurn): from brain.turn_started until that turn's
// brain.turn, which every turn logs exactly once as it ends.

// traceKeep is how many recent turns' ids the decorator remembers: late
// events of a turn (its compaction, a transcript append) arrive after
// the next turn started.
const traceKeep = 16

// turnTrace is one turn's ids and budget.
type turnTrace struct {
	n         int
	id        string
	utterance string
	event     string
	start     time.Time
	budget    time.Duration
}

// traceLog is the brain's logger decorator.
type traceLog struct {
	inner logging.Logger
	// run tags this brain's turn ids: turn numbers restart at 1 with
	// every daemon run.
	run string

	mu      sync.Mutex
	recent  [traceKeep]turnTrace // indexed by n % traceKeep
	running int                  // the running turn's number; 0 when none
}

func newTraceLog(inner logging.Logger) *traceLog {
	return &traceLog{inner: inner, run: strconv.FormatInt(time.Now().Unix(), 36)}
}

// Log implements logging.Logger: a map payload with a known "turn" gets
// that turn's turn_id and utterance_id (the caller built the map for
// this call; the event log marshals it at once).
func (l *traceLog) Log(source, typ, paneID string, payload any) {
	if m, ok := payload.(map[string]any); ok {
		if n, ok := m["turn"].(int); ok && n > 0 {
			l.mu.Lock()
			tr := l.recent[n%traceKeep]
			if typ == "brain.turn" && l.running == n {
				l.running = 0
			}
			l.mu.Unlock()
			if tr.n == n {
				if _, set := m["turn_id"]; !set {
					m["turn_id"] = tr.id
				}
				if _, set := m["utterance_id"]; !set && tr.utterance != "" {
					m["utterance_id"] = tr.utterance
				}
				if typ == "brain.turn" && tr.event != "" {
					m["event_id"] = tr.event
				}
			}
		}
	}
	l.inner.Log(source, typ, paneID, payload)
}

// traceTurn records turn t as running and logs brain.turn_started. Called
// with the turn lock held, once t.n is known.
func (b *Brain) traceTurn(t *turn) {
	// Wall clock, not cfg.Now: the watchdog compares it with its own
	// clock, and a scripted test clock must not see an extra call.
	now := time.Now()
	tr := turnTrace{
		n:         t.n,
		id:        "turn-" + b.trace.run + "-" + strconv.Itoa(t.n),
		utterance: t.req.UtteranceID,
		start:     now,
		budget:    t.soft.Sub(t.start) + b.cfg.FinalGrace,
	}
	if ev, ok := strings.CutPrefix(t.req.Link, "evt:"); ok {
		tr.event = ev
	}
	b.trace.mu.Lock()
	b.trace.recent[t.n%traceKeep] = tr
	b.trace.running = t.n
	b.trace.mu.Unlock()
	payload := map[string]any{
		"turn": t.n, "kind": string(t.req.Kind), "trigger": string(t.req.Trigger),
		"queue_ms": now.Sub(t.queued).Milliseconds(), "budget_ms": tr.budget.Milliseconds(),
	}
	if t.req.Agent != "" {
		payload["agent"] = t.req.Agent
	}
	if tr.event != "" {
		payload["event_id"] = tr.event
	}
	b.log.Log(logSource, "brain.turn_started", "", payload)
}

// RunningTurn reports the turn that holds the turn lock and is still
// running (from brain.turn_started to its brain.turn): when it started
// and its hard budget (the turn's timeout plus FinalGrace). ok is false
// when no turn is running. Read-only; the daemon watchdog polls it.
func (b *Brain) RunningTurn() (started time.Time, budget time.Duration, ok bool) {
	b.trace.mu.Lock()
	defer b.trace.mu.Unlock()
	if b.trace.running == 0 {
		return time.Time{}, 0, false
	}
	tr := b.trace.recent[b.trace.running%traceKeep]
	return tr.start, tr.budget, true
}
