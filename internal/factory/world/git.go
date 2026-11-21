package world

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

const gitBackoff = 30 * time.Second

var ErrGitSuppressed = errors.New("git temporarily suppressed after a child deadline")

// Git runs project Git commands in herdr-core's login session.
type Git struct {
	client         *hc.Client
	logSuppression func(string)
	mu             sync.Mutex
	blocked        map[string]time.Time
}

func NewGit(client *hc.Client, logSuppression func(folder string)) *Git {
	return &Git{client: client, logSuppression: logSuppression, blocked: make(map[string]time.Time)}
}

// GitError carries the child outcome together with any RPC failure.
type GitError struct {
	Result hc.ExecResult
	RPC    error
}

func (e *GitError) Error() string {
	switch {
	case e.RPC != nil:
		return e.RPC.Error()
	case e.Result.TimedOut:
		return "git child timed out"
	case e.Result.Truncated:
		return "git output truncated by herdr-core"
	case e.Result.Signal != nil:
		return fmt.Sprintf("git killed by signal %d: %s", *e.Result.Signal, strings.TrimSpace(string(e.Result.Stderr)))
	case e.Result.Code != nil:
		return fmt.Sprintf("git exit status %d: %s", *e.Result.Code, strings.TrimSpace(string(e.Result.Stderr)))
	default:
		return "herdr-core returned no git exit status"
	}
}

func (e *GitError) Unwrap() error {
	if e.RPC == nil && e.Result.TimedOut {
		return context.DeadlineExceeded
	}
	return e.RPC
}

// GitTimedOut reports a deadline confirmed by herdr-core for the child.
func GitTimedOut(err error) bool {
	var e *GitError
	return errors.As(err, &e) && e.RPC == nil && e.Result.TimedOut
}

func gitExit(err error, code int) bool {
	var e *GitError
	return errors.As(err, &e) && e.RPC == nil && !e.Result.TimedOut && !e.Result.Truncated &&
		e.Result.Signal == nil && e.Result.Code != nil && *e.Result.Code == code
}

func (g *Git) Suppressed(folder string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Now().Before(g.blocked[folder])
}

// Run returns the complete captured child result or an error.
func (g *Git) Run(ctx context.Context, dir string, args ...string) (hc.ExecResult, error) {
	if g == nil || g.client == nil {
		return hc.ExecResult{}, &GitError{RPC: errors.New("git requires a herdr-core client")}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return hc.ExecResult{}, err
	}
	if g.Suppressed(dir) {
		return hc.ExecResult{}, ErrGitSuppressed
	}
	budget := 10 * time.Second
	if end, ok := ctx.Deadline(); ok {
		left := time.Until(end)
		if left <= 0 {
			return hc.ExecResult{}, &GitError{RPC: context.DeadlineExceeded}
		}
		budget = min(budget, left-min(left/4, 2*time.Second))
	}
	limit := budget.Seconds()
	r, err := g.client.Exec(ctx, hc.ExecParams{
		Cwd: dir, Argv: append([]string{"git"}, args...), Timeout: &limit,
		Env: map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": ""},
	})
	if err == nil && r.TimedOut {
		g.mu.Lock()
		active := time.Now().Before(g.blocked[dir])
		g.blocked[dir] = time.Now().Add(gitBackoff)
		g.mu.Unlock()
		if !active && g.logSuppression != nil {
			g.logSuppression(dir)
		}
	}
	if err != nil || r.TimedOut || r.Truncated || r.Signal != nil || r.Code == nil || *r.Code != 0 {
		return r, &GitError{Result: r, RPC: err}
	}
	return r, nil
}

func (g *Git) gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	r, err := g.Run(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(r.Stdout)), nil
}
