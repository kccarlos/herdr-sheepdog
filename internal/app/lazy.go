package app

import (
	"context"
	"errors"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/cli"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// lazyPusher lets the supervisor be built before the voice session.
type lazyPusher struct {
	mu sync.Mutex
	p  supervisor.Pusher
}

func (l *lazyPusher) set(p supervisor.Pusher) {
	l.mu.Lock()
	l.p = p
	l.mu.Unlock()
}

// Push implements supervisor.Pusher.
func (l *lazyPusher) Push(text string, u voice.Urgency) error {
	return l.PushTagged(text, u, nil)
}

// PushTagged implements supervisor.TagPusher, so a tagged push
// (answers_job) keeps its tag through the lazy indirection — both the
// voice session and the --voice=off pusher take them.
func (l *lazyPusher) PushTagged(text string, u voice.Urgency, tags map[string]any) error {
	l.mu.Lock()
	p := l.p
	l.mu.Unlock()
	if p == nil {
		return errors.New("app: no pusher")
	}
	if tp, ok := p.(supervisor.TagPusher); ok && len(tags) > 0 {
		return tp.PushTagged(text, u, tags)
	}
	return p.Push(text, u)
}

// HasListener implements supervisor.Listener through the indirection:
// the voice session's attach state, or true with --voice=off or before
// the session exists (nothing is spoken there anyway).
func (l *lazyPusher) HasListener() bool {
	l.mu.Lock()
	p := l.p
	l.mu.Unlock()
	if li, ok := p.(supervisor.Listener); ok {
		return li.HasListener()
	}
	return true
}

// Reset forwards to the voice session's Reset ("New conversation",
// supervisor.ResetConversation) once one is attached (the voice.Switcher
// implements it); a no-op before then or with --voice=off, where there
// is nothing for a reset to touch.
func (l *lazyPusher) Reset(reason string) error {
	l.mu.Lock()
	p := l.p
	l.mu.Unlock()
	if r, ok := p.(interface{ Reset(string) error }); ok {
		return r.Reset(reason)
	}
	return nil
}

// lazyQuerier lets the supervisor be built (with a non-nil Querier, so
// run_cli is enabled) before CLI detection has run: detection
// happens in Run, after daemon.start is logged, so cli.detected/cli.run
// never precede it in the event log.
type lazyQuerier struct {
	mu sync.Mutex
	q  supervisor.Querier
	// runner is the same object when the querier is the real Runner: it
	// also backs the investigate tool (supervisor.Investigator).
	runner *cli.Runner
}

func (l *lazyQuerier) set(q supervisor.Querier) {
	l.mu.Lock()
	l.q = q
	if r, ok := q.(*cli.Runner); ok {
		l.runner = r
	}
	l.mu.Unlock()
}

// Query implements supervisor.Querier.
func (l *lazyQuerier) Query(ctx context.Context, workspace, question string) (string, error) {
	l.mu.Lock()
	q := l.q
	l.mu.Unlock()
	if q == nil {
		return "", cli.ErrUnavailable
	}
	return q.Query(ctx, workspace, question)
}

// Investigate implements supervisor.Investigator: the same runner, with
// the investigation's own ceiling and slot and the sub-agent's session
// kept so a follow-up can resume it.
func (l *lazyQuerier) Investigate(ctx context.Context, q supervisor.InvestigationQuery) (supervisor.InvestigationResult, error) {
	l.mu.Lock()
	runner := l.runner
	l.mu.Unlock()
	if runner == nil {
		return supervisor.InvestigationResult{}, cli.ErrUnavailable
	}
	res, err := runner.Run(ctx, cli.Query{Backend: cli.Backend(q.Backend), Workspace: q.Workspace, Question: q.Question, Session: q.Session, Fork: q.Fork, Timeout: q.Timeout, Slot: q.Slot})
	if err != nil {
		return supervisor.InvestigationResult{}, err
	}
	return supervisor.InvestigationResult{Backend: string(res.Backend), Answer: res.Raw, SessionID: res.SessionID}, nil
}

// HasBackend reports whether backend was detected, so asking a worker's
// own session is refused at once on a machine without that worker's
// CLI. Before detection ran it says yes and the run itself reports
// cli.ErrUnavailable.
func (l *lazyQuerier) HasBackend(backend string) bool {
	l.mu.Lock()
	runner := l.runner
	l.mu.Unlock()
	return runner == nil || runner.Has(cli.Backend(backend))
}

// ResolveBackend names the backend investigations will use ("" before
// detection ran), for the investigation.started event.
func (l *lazyQuerier) ResolveBackend() (string, error) {
	l.mu.Lock()
	runner := l.runner
	l.mu.Unlock()
	if runner == nil {
		return "", cli.ErrUnavailable
	}
	b, err := runner.ResolveBackend()
	return string(b), err
}

// teeLogger writes every event to both loggers.
type teeLogger struct{ a, b logging.Logger }

func (t teeLogger) Log(source, typ, paneID string, payload any) {
	t.a.Log(source, typ, paneID, payload)
	t.b.Log(source, typ, paneID, payload)
}
