package world

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

const (
	// herdr-core sweep.rs waits HUP 0.5 s, TERM 0.5 s, and KILL 2 s.
	// exec.rs then reaps the child and allows up to 2 s for pipe drain.
	gitReplyGrace     = 6 * time.Second
	gitLocalLimit     = 20 * time.Second
	gitNetworkLimit   = 40 * time.Second
	ghRequestLimit    = 30 * time.Second
	psCommandLimit    = 5 * time.Second
	GitStageTimeout   = gitLocalLimit + gitReplyGrace + 2*time.Second
	GitNetworkTimeout = gitNetworkLimit + gitReplyGrace + 2*time.Second
	GitPassTimeout    = 90 * time.Second
	gitBackoff        = 30 * time.Second
	maxGitBackoff     = 5 * time.Minute
	maxGitEpisodes    = 128
)

// Leave room in the engine's read deadline for the git reads after the snapshot.
var prSnapshotLimit = 40 * time.Second

var ErrGitSuppressed = errors.New("git temporarily suppressed after an exec deadline")

type gitRepoKey struct{}

// GitRepository shares the repository's back-off across its worktrees.
func GitRepository(ctx context.Context, folder string) context.Context {
	if abs, err := absolutePath(folder); folder != "" && err == nil {
		return context.WithValue(ctx, gitRepoKey{}, abs)
	}
	return ctx
}

// absolutePath avoids os.Getwd's filesystem checks of PWD on relative paths.
func absolutePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	dir, err := syscall.Getwd()
	return filepath.Join(dir, path), err
}

// Git runs project commands in herdr-core's login session.
type Git struct {
	client   *hc.Client
	now      func() time.Time
	log      func(event, folder string)
	mu       sync.Mutex
	episodes map[string]gitEpisode
	enospc   map[string]bool
	attrSrc  int8 // 0 unchecked, 1 honoured, -1 ignored
}

type gitEpisode struct {
	until time.Time
	delay time.Duration
}

func NewGit(client *hc.Client, now func() time.Time, log func(event, folder string)) *Git {
	if now == nil {
		now = time.Now
	}
	return &Git{client: client, now: now, log: log, episodes: make(map[string]gitEpisode), enospc: make(map[string]bool)}
}

// GitError carries the git or gh child outcome together with any RPC failure.
type GitError struct {
	Result     hc.ExecResult
	RPC        error
	command    string
	suppressed bool
}

func (e *GitError) Error() string {
	command := cmp.Or(e.command, "git")
	switch {
	case e.suppressed:
		return ErrGitSuppressed.Error()
	case e.RPC != nil:
		return e.RPC.Error()
	case e.Result.TimedOut:
		return command + " child timed out"
	case e.Result.Truncated:
		return command + " output truncated by herdr-core"
	case e.Result.Signal != nil:
		return fmt.Sprintf("%s killed by signal %d: %s", command, *e.Result.Signal, strings.TrimSpace(string(e.Result.Stderr)))
	case e.Result.Code != nil:
		return fmt.Sprintf("%s exit status %d: %s", command, *e.Result.Code, strings.TrimSpace(string(e.Result.Stderr)))
	default:
		return "herdr-core returned no " + command + " exit status"
	}
}

func (e *GitError) Is(err error) bool { return e.suppressed && err == ErrGitSuppressed }

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

func gitExitCode(err error) (int, bool) {
	var e *GitError
	if errors.As(err, &e) && e.RPC == nil && !e.suppressed && !e.Result.TimedOut && !e.Result.Truncated &&
		e.Result.Signal == nil && e.Result.Code != nil {
		return *e.Result.Code, true
	}
	return 0, false
}

func gitExit(err error, code int) bool {
	c, ok := gitExitCode(err)
	return ok && c == code
}

func (g *Git) suppressed(repo string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.now().Before(g.episodes[repo].until)
}

// finishProbe keeps a bounded episode history across expired cooldowns.
func (g *Git) finishProbe(repo string, stalled bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	episode, active := g.episodes[repo]
	if stalled {
		if !active && len(g.episodes) == maxGitEpisodes {
			for old := range g.episodes {
				delete(g.episodes, old)
				break
			}
		}
		delay := min(maxGitBackoff, max(gitBackoff, 2*episode.delay))
		g.episodes[repo] = gitEpisode{until: g.now().Add(delay), delay: delay}
		if !active && g.log != nil {
			g.log("factory.git_suppressed", repo)
		}
	} else if active {
		delete(g.episodes, repo)
		if g.log != nil {
			g.log("factory.git_recovered", repo)
		}
	}
}

// Run returns the complete captured child result or an error.
func (g *Git) Run(ctx context.Context, dir string, args ...string) (hc.ExecResult, error) {
	return g.run(ctx, dir, "git", args, map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": ""})
}

func (g *Git) run(ctx context.Context, dir, command string, args []string, env map[string]string) (hc.ExecResult, error) {
	if g == nil || g.client == nil {
		return hc.ExecResult{}, &GitError{command: command, RPC: errors.New("command requires a herdr-core client")}
	}
	projectCommand := command == "git" || command == "/bin/sh"
	if dir != "" || projectCommand || command == "gh" {
		var err error
		if dir, err = absolutePath(dir); err != nil {
			return hc.ExecResult{}, &GitError{command: command, RPC: err}
		}
	}
	folder, _ := ctx.Value(gitRepoKey{}).(string)
	repo := cmp.Or(folder, dir)
	if projectCommand && g.suppressed(repo) {
		return hc.ExecResult{}, ErrGitSuppressed
	}
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	budget, grace := gitLocalLimit, gitReplyGrace
	network := verb == "push" || verb == "fetch" || verb == "ls-remote"
	if command == "gh" {
		budget = ghRequestLimit
	} else if command == "ps" {
		budget = psCommandLimit
	} else if network {
		budget = gitNetworkLimit
	}
	if end, ok := ctx.Deadline(); ok {
		left := time.Until(end)
		if left <= 0 {
			return hc.ExecResult{}, &GitError{command: command, RPC: context.DeadlineExceeded}
		}
		// Leave time to recognize an RPC deadline before the stage expires.
		left -= min(500*time.Millisecond, left/6)
		if left <= grace {
			grace = left / 3
		}
		budget = min(budget, left-grace)
	}
	limit := budget.Seconds()
	client := *g.client
	client.Timeout = budget + grace
	// Exec's own timeout floor can exceed the available reply window.
	callCtx, cancel := context.WithTimeout(ctx, client.Timeout)
	defer cancel()
	r, err := client.Exec(callCtx, hc.ExecParams{
		Cwd: dir, Argv: append([]string{command}, args...), Timeout: &limit, Env: env,
	})
	stalled := projectCommand && !network && verb != "diff" && (r.TimedOut || errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil)
	if projectCommand && (stalled || err == nil && !r.TimedOut && r.Code != nil && r.Signal == nil) {
		g.finishProbe(repo, stalled)
	}
	if err != nil || r.TimedOut || r.Truncated || r.Signal != nil || r.Code == nil || *r.Code != 0 {
		if command == "git" && (errors.Is(err, syscall.ENOSPC) || strings.Contains(strings.ToLower(string(r.Stderr)), "no space left on device")) {
			g.mu.Lock()
			g.enospc[repo] = true
			g.mu.Unlock()
		}
		return r, &GitError{command: command, Result: r, RPC: err, suppressed: stalled}
	}
	return r, nil
}

// Stalled reports whether folder has an open local-git stall episode.
func (g *Git) Stalled(folder string) bool {
	if g == nil || folder == "" {
		return false
	}
	repo, err := absolutePath(folder)
	if err != nil {
		repo = filepath.Clean(folder)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, active := g.episodes[repo]
	return active
}

// TakeNoSpace reports and clears whether a git call on folder hit ENOSPC.
func (g *Git) TakeNoSpace(folder string) bool {
	repo, err := absolutePath(folder)
	if err != nil {
		repo = filepath.Clean(folder)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	hit := g.enospc[repo]
	delete(g.enospc, repo)
	return hit
}

func (g *Git) gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	r, err := g.Run(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(r.Stdout)), nil
}
