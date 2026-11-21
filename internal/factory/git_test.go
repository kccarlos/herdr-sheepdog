package factory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func fixtureGit(t *testing.T) *world.Git {
	t.Helper()
	srv := hctest.New(t)
	srv.OnExec = hctest.GitExec
	return world.NewGit(hc.NewClient(srv.Path), nil, nil)
}

func newGitTestEngine(t *testing.T, paths Paths, client *hc.Client) *Engine {
	t.Helper()
	e := New(paths, client)
	e.probeLaunch = func(context.Context, []string) flow.ResolveFacts { return flow.ResolveFacts{} }
	srv := hctest.New(t)
	srv.OnExec = hctest.GitExec
	e.git = world.NewGit(hc.NewClient(srv.Path), e.now, func(event, folder string) {
		e.logEvent(event, map[string]string{"folder": folder})
	})
	return e
}

func TestDaemonProjectPathsUseMachineExec(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	// The login-session runner can resolve the alias and read the proof.
	root := filepath.Join(parent, "login-session-only")
	alias := filepath.Join(parent, "alias")
	proof := filepath.Join(root, ".git", "sheepdog-proofs", "p.log")
	if err := os.MkdirAll(filepath.Dir(proof), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(proof, 0o600); err != nil {
		t.Fatal(err)
	}
	paths := testPaths(t)
	eng := New(paths, nil)
	state := flow.State{Project: flow.Project{ID: "project", Folder: root, Started: true}}
	if err := SaveState(paths.State("project"), state); err != nil {
		t.Fatal(err)
	}
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if p.Timeout == nil || *p.Timeout <= 0 {
			return hc.ExecResult{}, errors.New("missing child deadline")
		}
		zero, one := 0, 1
		switch {
		case p.Cwd == alias && slices.Contains(p.Argv, "--show-toplevel"):
			return hc.ExecResult{Code: &zero, Stdout: []byte(root + "\n")}, nil
		case slices.Contains(p.Argv, "sheepdog.projectId"):
			return hc.ExecResult{Code: &one}, nil
		case p.Cwd == root && slices.Contains(p.Argv, proof):
			return hc.ExecResult{Code: &zero, Stdout: []byte("login-session proof\n")}, nil
		default:
			return hc.ExecResult{}, errors.New("unexpected machine.exec call")
		}
	}
	eng.git = world.NewGit(hc.NewClient(srv.Path), nil, nil)
	if id, _, err := eng.findProjectLocked(ctx, alias); err != nil || id != "project" {
		t.Errorf("daemon project lookup = %q, %v", id, err)
	}
	a := flow.Attempt{Worktree: root, Review: &flow.ReviewRun{Code: "review"},
		Ready: []flow.Ready{{Head: "c1", Proofs: []flow.Proof{{Command: "test", OutputPath: proof}}}}}
	type result struct {
		prompt string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		prompt, err := eng.reviewerPrompt(ctx, "{proofs}", flow.Task{}, a, "c1", "", "", "", 1)
		done <- result{prompt, err}
	}()
	select {
	case got := <-done:
		if got.err != nil || !strings.Contains(got.prompt, "login-session proof") {
			t.Errorf("reviewer prompt missing worktree proof: %q, %v", got.prompt, got.err)
		}
	case <-time.After(2 * time.Second):
		if writer, err := os.OpenFile(proof, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = writer.Close()
		}
		t.Fatal("reviewer prompt blocked on the local proof FIFO")
	}
	if got := len(srv.Requests()); got != 3 {
		t.Errorf("machine.exec calls = %d, want 3", got)
	}
}

func TestGitRPCFailureLeavesTaskFactsUnknown(t *testing.T) {
	l := newLanding(t)
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if p.Cwd == l.wt && slices.Contains(p.Argv, "diff") {
			return hc.ExecResult{}, errors.New("fixture lost the machine.exec reply")
		}
		return hctest.GitExec(p)
	}
	l.eng.git = world.NewGit(hc.NewClient(srv.Path), nil, nil)
	l.pass()
	task := l.state().Tasks[0]
	if task.LastRow != "" || len(task.Recaps) != 0 || len(l.eng.tips) != 0 {
		t.Fatalf("failed RPC changed task facts: row=%q, recaps=%v, tips=%v", task.LastRow, task.Recaps, l.eng.tips)
	}
	if !strings.Contains(l.log.String(), "fixture lost the machine.exec reply") {
		t.Fatalf("missing RPC failure log: %s", l.log)
	}
}

func TestHungGitDoesNotSkipHealthyProjectInSameTick(t *testing.T) {
	hung := newLanding(t)
	healthy := newLanding(t)
	eng := hung.eng
	for _, item := range []struct {
		id    string
		state flow.State
	}{
		{"a-hung", hung.state()},
		{"b-healthy", healthy.state()},
	} {
		state := item.state
		state.Project.ID = item.id
		if item.id == "b-healthy" {
			state.Tasks[0].Attempts = nil
		}
		if err := SaveState(eng.Paths.State(item.id), state); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(eng.Paths.State(hung.id)); err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.EvalSymlinks(hung.repo)
	if err != nil {
		t.Fatal(err)
	}

	bin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte("#!/bin/sh\n"+
		"if [ -f .fixture-hung ]; then exec sleep 20; fi\nexec "+strconv.Quote(bin)+" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hung.wt, ".fixture-hung"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+":"+os.Getenv("PATH"))
	oldTimeout := lockGitTimeout
	t.Cleanup(func() { lockGitTimeout = oldTimeout })
	lockGitTimeout = 1200 * time.Millisecond

	eng.PassInterval, eng.wake = time.Hour, nil
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		eng.Run(ctx)
	}()
	defer func() { cancel(); <-done }()

	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("healthy project did not start an attempt on the first tick")
		case <-tick.C:
			s, err := LoadState(eng.Paths.State("b-healthy"))
			if err == nil && len(s.Tasks[0].Attempts) == 1 && s.Tasks[0].LastRow == "N11" {
				// N11 saves the attempt before the project finishes its other rules.
				eng.mu.Lock()
				cancel()
				eng.mu.Unlock()
				<-done
				if !strings.Contains(hung.log.String(), `"folder":"`+repo+`"`) || strings.Count(hung.log.String(), "factory.git_suppressed") != 1 {
					t.Fatalf("timeout or suppression missing: %s", hung.log)
				}
				eng.passProject(context.Background(), "a-hung", false)
				if strings.Count(hung.log.String(), "factory.git_suppressed") != 1 {
					t.Fatalf("suppression logged again: %s", hung.log)
				}
				return
			}
		}
	}
}

func TestTruncatedChangedLinesOpensSizeRecap(t *testing.T) {
	l := newLanding(t)
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if p.Argv[1] == "diff" {
			code := 0
			return hc.ExecResult{Code: &code, Truncated: true}, nil
		}
		return hctest.GitExec(p)
	}
	l.eng.git = world.NewGit(hc.NewClient(srv.Path), nil, nil)
	l.pass()
	if recap := flow.OpenRecap(l.state().Tasks[0]); recap == nil || recap.Trigger != flow.TriggerSize {
		t.Fatalf("truncated diff must open a size recap, got %+v", recap)
	}
}

func TestReadyRefusesUnknownMergeOnly(t *testing.T) {
	l := newLanding(t).withTokens()
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Ready = nil })
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if p.Argv[1] == "merge-base" && p.Argv[2] != "--is-ancestor" {
			return hc.ExecResult{}, errors.New("lost merge-only reply")
		}
		return hctest.GitExec(p)
	}
	l.eng.git = world.NewGit(hc.NewClient(srv.Path), nil, nil)
	err := l.eng.Report("tok-worker", "ready", []byte(`{"head":"`+l.c1+`"}`))
	if err == nil || !strings.Contains(err.Error(), "lost merge-only reply") || len(l.state().Tasks[0].Attempts[0].Ready) != 0 {
		t.Fatalf("ready with unknown merge-only returned %v", err)
	}
}

func TestNonSizeRecapStillObservesClosedPR(t *testing.T) {
	l := newLanding(t)
	l.edit(func(s *flow.State) {
		s.Tasks[0].Recaps = []flow.Recap{{Trigger: flow.TriggerCI}}
	})
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if p.Argv[1] == "diff" {
			return hc.ExecResult{TimedOut: true}, nil
		}
		return hctest.GitExec(p)
	}
	l.eng.git = world.NewGit(hc.NewClient(srv.Path), l.eng.now, nil)
	l.pass()
	if err := os.WriteFile(filepath.Join(l.gh, "view.json"), []byte(
		`{"number":7,"state":"CLOSED","headRefOid":"`+l.c1+`","baseRefName":"main"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	l.pass()
	if row := l.state().Tasks[0].LastRow; row != "N4" {
		t.Fatalf("closed PR with CI recap: row %s, want N4", row)
	}
}

func TestGitFailuresDoNotSetGitHubUnavailable(t *testing.T) {
	for _, transport := range []bool{false, true} {
		l := newLanding(t)
		srv := hctest.New(t)
		srv.OnExec = func(hc.ExecParams) (hc.ExecResult, error) {
			code := 128
			return hc.ExecResult{Code: &code}, nil
		}
		if transport {
			srv.Close()
		}
		l.eng.git = world.NewGit(hc.NewClient(srv.Path), nil, nil)
		s := l.state()
		l.eng.observeBaseLocked(context.Background(), l.id, &s)
		if l.eng.ghUnavail[l.id] {
			t.Errorf("base read set github_unavailable for git failure (transport=%v)", transport)
		}
		l.eng.readPRLocked(context.Background(), l.id, &s, 0)
		if l.eng.ghUnavail[l.id] {
			t.Errorf("PR read set github_unavailable for git failure (transport=%v)", transport)
		}
	}
}

func TestGitActUsesTheFailingSystemOutage(t *testing.T) {
	hcDown := flow.Facts{HCUnavailable: true}
	ghDown := flow.Facts{GitHubUnavailable: true}
	rpcErr := &world.GitError{RPC: errors.New("socket unavailable")}
	for _, verb := range []string{flow.AskNewAttempt, flow.AskPushPR, flow.AskMerge} {
		if !actOutage(verb, hcDown, rpcErr) || actOutage(verb, ghDown, rpcErr) ||
			actOutage(verb, hcDown, errors.New("gh failed")) {
			t.Errorf("%s attributed the failure to the wrong system", verb)
		}
	}
}

func TestGitExitDuringGitHubOutageDoesNotExhaustRetries(t *testing.T) {
	l := newLanding(t)
	gitIn(t, l.repo, "worktree", "remove", l.wt)
	l.edit(func(s *flow.State) {
		s.Settings.EnsureFailures = 1
		s.Tasks[0].Attempts = nil
	})
	writeGHFile(t, l.gh, "fail", "") // HTTP 502 while observing the base
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if p.Argv[1] == "fetch" {
			code := 128
			return hc.ExecResult{Code: &code, Stderr: []byte("unable to access remote: HTTP 503")}, nil
		}
		return hctest.GitExec(p)
	}
	l.eng.git = world.NewGit(hc.NewClient(srv.Path), l.eng.now, nil)
	l.pass()

	s := l.state()
	acts := s.Tasks[0].Acts
	last := flow.LastAct(acts, flow.AskNewAttempt)
	if !l.eng.ghUnavail[l.id] || last == nil || last.Result != "failed" || !strings.Contains(last.Err, "git exit status 128") {
		t.Fatalf("expected a git fetch exit during GitHub outage, got acts=%+v, github_unavailable=%v", acts, l.eng.ghUnavail[l.id])
	}
	if !last.Uncounted || flow.Exhausted(acts, last.Key, s.Settings) {
		t.Fatalf("git fetch exit consumed ensure_failures during GitHub outage: %+v", *last)
	}
}

func TestTaskCloseSharesRepositoryGitBackoff(t *testing.T) {
	l := newLanding(t).withTokens()
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].NothingToChange = "already done" })
	repo := l.state().Project.Folder
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if p.Cwd == repo {
			return hc.ExecResult{TimedOut: true}, nil
		}
		return hctest.GitExec(p)
	}
	l.eng.git = world.NewGit(hc.NewClient(srv.Path), l.eng.now, nil)
	if _, err := l.eng.git.Run(context.Background(), repo, "status"); !world.GitTimedOut(err) {
		t.Fatalf("expected a repository timeout, got %v", err)
	}
	err := l.eng.Report("tok-coord", "task", []byte(`{"action":"close","task":"T1","why":"already done"}`))
	if !errors.Is(err, world.ErrGitSuppressed) || len(srv.Requests()) != 1 {
		t.Fatalf("task close returned %v after %d git calls, want suppression after one call", err, len(srv.Requests()))
	}
}

func TestReviewerDefersWhenProofReadIsUnknown(t *testing.T) {
	l := newLanding(t)
	for _, failure := range []string{"RPC", "timeout", "truncation", "budget", "lookup"} {
		t.Run(failure, func(t *testing.T) {
			s := l.state()
			a := &s.Tasks[0].Attempts[0]
			a.Rounds = nil
			a.Ready[0].Proofs = []flow.Proof{{Command: "true", OutputPath: filepath.Join(l.wt, "proof")}}
			prev := &flow.ReviewRun{Round: 1, Head: l.c1, Code: "review", Reviewer: flow.Reviewer{CLI: "claude", Model: "opus"}, Retries: 2}
			a.Review = prev
			ctx := context.Background()
			if failure == "budget" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			srv := hctest.New(t)
			srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
				code := 0
				if p.Argv[0] == "git" {
					return hctest.GitExec(p)
				}
				switch failure {
				case "RPC":
					return hc.ExecResult{}, errors.New("lost proof reply")
				case "timeout":
					return hc.ExecResult{TimedOut: true}, nil
				case "truncation":
					return hc.ExecResult{Code: &code, Truncated: true}, nil
				case "budget":
					<-ctx.Done()
					return hc.ExecResult{}, ctx.Err()
				default:
					code = 3
					return hc.ExecResult{Code: &code, Stderr: []byte("proof lookup failed")}, nil
				}
			}
			l.eng.git = world.NewGit(hc.NewClient(srv.Path), nil, nil)
			before := len(l.srv.Requests())
			_, deferLaunch, err := l.eng.execStartReviewer(ctx, l.id, &s, 0, flow.Facts{Ask: "start_reviewer@head@retry@1"})
			if err == nil || !deferLaunch || a.Review != prev || a.Review.Retries != 2 || a.Review.Run != nil || len(l.srv.Requests()) != before+1 {
				t.Errorf("review launch on %s: defer=%v err=%v retries=%d run started=%v terminal RPCs=%d", failure, deferLaunch, err, a.Review.Retries, a.Review.Run != nil, len(l.srv.Requests())-before)
			}
		})
	}
}

func TestProjectLookupSkipsUnrelatedStoredFolders(t *testing.T) {
	paths := testPaths(t)
	eng := New(paths, nil)
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	for _, id := range []string{"old-a", "old-b"} {
		if err := SaveState(paths.State(id), flow.State{Project: flow.Project{ID: id, Folder: filepath.Join(parent, id)}}); err != nil {
			t.Fatal(err)
		}
	}
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if p.Cwd == target {
			return hc.ExecResult{}, errors.New("target does not exist")
		}
		if p.Cwd == "/" && slices.Contains(p.Argv, target) {
			zero := 0
			return hc.ExecResult{Code: &zero, Stdout: []byte(target + "\x00")}, nil
		}
		return hc.ExecResult{}, errors.New("unrelated stored folder")
	}
	eng.git = world.NewGit(hc.NewClient(srv.Path), nil, nil)
	if _, _, err := eng.findProjectLocked(context.Background(), target); err == nil || err.Error() != "no started factory project" || len(srv.Requests()) != 2 {
		t.Errorf("findProjectLocked = %v after %d RPCs, want no match after one canonicalization", err, len(srv.Requests()))
	}
}

func TestCodexReviewerUsesStoredWorktree(t *testing.T) {
	l := newLanding(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := l.state()
	a := &s.Tasks[0].Attempts[0]
	a.Rounds = nil
	a.Review = &flow.ReviewRun{Round: 1, Head: l.c1, Code: "review", Reviewer: flow.Reviewer{CLI: world.CodexCLI, Model: "gpt-5.4"}}
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if slices.Contains(p.Argv, "--show-toplevel") {
			return hc.ExecResult{}, errors.New("unexpected second worktree resolution")
		}
		return hctest.GitExec(p)
	}
	l.eng.git = world.NewGit(hc.NewClient(srv.Path), nil, nil)
	if _, _, err := l.eng.execStartReviewer(context.Background(), l.id, &s, 0, flow.Facts{}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectLogUsesStoredLookup(t *testing.T) {
	paths := testPaths(t)
	if err := SaveState(paths.State("stopped"), flow.State{Project: flow.Project{ID: "stopped", Folder: t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	if err := AppendLog(paths.Log("stopped"), LogLine{Seq: 1, Decision: "stopped"}); err != nil {
		t.Fatal(err)
	}
	if lines, _, err := ProjectLog(context.Background(), paths, "", 0); err != nil || len(lines) != 1 {
		t.Errorf("ProjectLog = %v, %v, want the single stored project's log", lines, err)
	}
}
