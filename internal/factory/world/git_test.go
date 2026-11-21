package world

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func fixtureGit(t *testing.T) *Git {
	t.Helper()
	srv := hctest.New(t)
	srv.OnExec = hctest.GitExec
	return NewGit(hc.NewClient(srv.Path), nil, nil)
}

func TestGitLargePatchAndTruncation(t *testing.T) {
	repo := sizeRepo(t)
	tip := commitFile(t, repo, "large.txt", strings.Repeat("a line of patch data\n", 8192))
	args := []string{"diff", "--no-color", tip + "^", tip}
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	want, err := cmd.Output()
	if err != nil || len(want) <= 8<<10 {
		t.Fatalf("fixture patch size %d: %v", len(want), err)
	}
	got, err := fixtureGit(t).Run(context.Background(), repo, args...)
	if err != nil || !bytes.Equal(got.Stdout, want) {
		t.Fatalf("captured patch size %d, want %d: %v", len(got.Stdout), len(want), err)
	}

	srv := hctest.New(t)
	srv.OnExec = func(hc.ExecParams) (hc.ExecResult, error) {
		code := 0
		return hc.ExecResult{Code: &code, Stdout: []byte("partial patch"), Truncated: true}, nil
	}
	g := NewGit(hc.NewClient(srv.Path), nil, nil)
	out, err := g.gitCmd(context.Background(), repo, args...)
	var bad *GitError
	if out != "" || !errors.As(err, &bad) || !bad.Result.Truncated || bad.RPC != nil {
		t.Fatalf("truncation = %q, %v", out, err)
	}
}

func TestGitBudgets(t *testing.T) {
	srv := hctest.New(t)
	var budget float64
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		budget = *p.Timeout
		code := 0
		return hc.ExecResult{Code: &code}, nil
	}
	g := NewGit(hc.NewClient(srv.Path), nil, nil)
	for _, tc := range []struct {
		verb string
		min  float64
	}{{"status", 20}, {"fetch", 30}, {"push", 30}} {
		t.Run(tc.verb, func(t *testing.T) {
			timeout := GitNetworkTimeout
			if tc.verb == "status" {
				timeout = GitStageTimeout
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if _, err := g.Run(ctx, t.TempDir(), tc.verb); err != nil || budget < tc.min {
				t.Fatalf("child budget = %.1fs, want at least %.1fs (err=%v)", budget, tc.min, err)
			}
		})
	}
}

func TestGitDeadlineAllowsProcessSweep(t *testing.T) {
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		// The caller affords a short child run and a full sweep.
		time.Sleep(time.Duration(*p.Timeout*float64(time.Second)) + 3200*time.Millisecond)
		return hc.ExecResult{TimedOut: true}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	g := NewGit(hc.NewClient(srv.Path), nil, nil)
	if _, err := g.Run(ctx, t.TempDir(), "status"); !GitTimedOut(err) {
		t.Fatalf("expected confirmed child deadline after sweep, got %v", err)
	}
}

func TestNewWorktreeAdoptionPropagatesGitFailure(t *testing.T) {
	wt := sizeRepo(t)
	repo := filepath.Join(filepath.Dir(wt), "repo")
	commitFile(t, wt, "worker.txt", "work\n")
	for _, verb := range []string{"rev-parse", "symbolic-ref", "remote", "merge-base"} {
		t.Run(verb, func(t *testing.T) {
			srv := hctest.New(t)
			srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
				if p.Argv[1] == verb && (verb != "symbolic-ref" || p.Cwd == wt) {
					return hc.ExecResult{}, errors.New("lost adoption reply")
				}
				return hctest.GitExec(p)
			}
			g := NewGit(hc.NewClient(srv.Path), nil, nil)
			got, err := g.NewWorktree(context.Background(), repo, wt, "t1", "feat: task")
			if got != (Worktree{}) || err == nil || !strings.Contains(err.Error(), "lost adoption reply") {
				t.Fatalf("adoption returned %+v, %v, want the original git error", got, err)
			}
		})
	}
}

func TestGitRepositoryBackoffEpisode(t *testing.T) {
	srv := hctest.New(t)
	calls := 0
	stalled := true
	srv.OnExec = func(hc.ExecParams) (hc.ExecResult, error) {
		calls++
		if stalled {
			return hc.ExecResult{TimedOut: true}, nil
		}
		code := 0
		return hc.ExecResult{Code: &code}, nil
	}
	var events []string
	now := time.Now()
	g := NewGit(hc.NewClient(srv.Path), func() time.Time { return now }, func(event, folder string) {
		events = append(events, event+" "+folder)
	})
	repo := t.TempDir()
	ctx := GitRepository(context.Background(), repo)
	first, second := t.TempDir(), t.TempDir()
	_, deadline := g.Run(ctx, first, "status")
	if !GitTimedOut(deadline) {
		t.Fatalf("first probe should confirm the child deadline: %v", deadline)
	}
	_, blocked := g.Run(ctx, second, "status")
	if !errors.Is(blocked, ErrGitSuppressed) || blocked.Error() != deadline.Error() || calls != 1 {
		t.Fatalf("sibling worktree = %v after %d calls, first error %v", blocked, calls, deadline)
	}
	now = now.Add(gitBackoff)
	if g.suppressed(repo) || len(g.blocked) != 0 {
		t.Fatalf("expired cooldown was retained: %v", g.blocked)
	}
	if _, err := g.Run(ctx, second, "status"); !GitTimedOut(err) || calls != 2 || len(events) != 1 {
		t.Fatalf("retry = %v after %d calls, events %v", err, calls, events)
	}
	now = now.Add(gitBackoff)
	stalled = false
	if _, err := g.Run(ctx, second, "status"); err != nil || len(g.blocked) != 0 || len(g.episodes) != 0 {
		t.Fatalf("recovery = %v, cooldowns %v, episodes %v", err, g.blocked, g.episodes)
	}
	if want := []string{"factory.git_suppressed " + repo, "factory.git_recovered " + repo}; !slices.Equal(events, want) {
		t.Fatalf("episode events = %v, want %v", events, want)
	}
}
