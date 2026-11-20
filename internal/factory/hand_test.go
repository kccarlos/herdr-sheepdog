package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// TestN4PRClosedByHand verifies N4 end to end: a PR closed without merging
// stops any active reviewer, wakes the coordinator once, never reopens the PR,
// never opens a second PR, and never merges.
func TestN4PRClosedByHand(t *testing.T) {
	l := newLanding(t)
	revID, err := world.StartRun(context.Background(), l.client, world.RunSpec{Name: "rev-t1-1", Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(revID, &hc.Status{App: "claude-code", State: "working"})
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Rounds = nil
		s.Tasks[0].Attempts[0].Review = &flow.ReviewRun{Round: 1, Head: l.c1, RunID: revID, Code: "abc"}
	})

	writeGHFile(t, l.gh, "view.json", `{"number":7,"state":"CLOSED","headRefOid":"`+l.c1+`","mergeable":"MERGEABLE","statusCheckRollup":[],"baseRefName":"main"}`)
	l.pass()
	l.pass()

	st := l.state()
	if st.Tasks[0].Attempts[0].Review != nil {
		t.Fatalf("active reviewer was not stopped on closed PR: %+v", st.Tasks[0].Attempts[0].Review)
	}
	if len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Key != "pr_closed" {
		t.Fatalf("wakes = %+v, want 1 pr_closed wake", st.Project.Wakes)
	}
	if got := ghCalls(t, l.gh, "pr create"); len(got) != 0 {
		t.Fatalf("opened a second PR: %q", got)
	}
	if got := ghCalls(t, l.gh, "pr reopen"); len(got) != 0 {
		t.Fatalf("reopened PR: %q", got)
	}
	if got := ghCalls(t, l.gh, "pr merge"); len(got) != 0 {
		t.Fatalf("merged closed PR: %q", got)
	}
}

// pushExternalToBranch simulates someone outside the engine pushing (or
// force-pushing) a commit onto branch on l's bare origin.
func pushExternalToBranch(t *testing.T, l *landing, force bool, file, content string) string {
	t.Helper()
	sim := t.TempDir()
	rawURL := gitIn(t, l.repo, "remote", "get-url", "--push", "origin")
	gitIn(t, sim, "clone", rawURL, "repo")
	simRepo := filepath.Join(sim, "repo")
	gitIn(t, simRepo, "config", "user.name", "Human")
	gitIn(t, simRepo, "config", "user.email", "human@example.com")
	if force {
		gitIn(t, simRepo, "checkout", "-B", l.branch, "origin/main")
	} else {
		gitIn(t, simRepo, "checkout", l.branch)
	}
	if err := os.WriteFile(filepath.Join(simRepo, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, simRepo, "add", file)
	gitIn(t, simRepo, "commit", "-m", "chore: external "+file)
	sha := gitIn(t, simRepo, "rev-parse", "HEAD")
	if force {
		gitIn(t, simRepo, "push", "--force", "origin", sha+":refs/heads/"+l.branch)
	} else {
		gitIn(t, simRepo, "push", "origin", sha+":refs/heads/"+l.branch)
	}
	return sha
}

// TestN7ForcePushedAndForeignPRHead verifies N7 end to end for both a
// force-pushed (diverged) PR head and a foreign commit pushed onto the PR head:
// worker is told once, waits while working, coordinator is woken once at turn
// end naming say+retry, and the PR is never merged.
func TestN7ForcePushedAndForeignPRHead(t *testing.T) {
	for name, force := range map[string]bool{"force-pushed": true, "foreign commit": false} {
		t.Run(name, func(t *testing.T) {
			l := newLanding(t)
			ext := pushExternalToBranch(t, l, force, "ext.txt", "external\n")
			writePRJSON(t, l.gh, "view.json", ext, greenRollup)

			// Pass 1: worker is told once.
			l.pass()
			sends := sendsTo(t, l.srv.Requests(), l.worker)
			if len(sends) != 1 || !strings.Contains(sends[0], "restore branch") {
				t.Fatalf("pass 1 sent = %q, want 1 restore-branch message", sends)
			}
			if len(l.state().Project.Wakes) != 0 {
				t.Fatalf("pass 1 wakes = %+v, want none yet", l.state().Project.Wakes)
			}

			// Pass 2 while worker is working: waits, no second tell, no wake.
			l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
			l.pass()
			if len(sendsTo(t, l.srv.Requests(), l.worker)) != 1 || len(l.state().Project.Wakes) != 0 {
				t.Fatalf("while working: sent=%d wakes=%+v", len(sendsTo(t, l.srv.Requests(), l.worker)), l.state().Project.Wakes)
			}

			// Pass 3 after worker turn ends and divergence still holds: wakes coordinator once.
			l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "idle"})
			l.pass()
			l.pass()
			wakes := l.state().Project.Wakes
			if len(wakes) != 1 || wakes[0].Key != "diverged@"+ext+"@"+l.c1 || !strings.Contains(wakes[0].Why, "say") || !strings.Contains(wakes[0].Why, "retry") {
				t.Fatalf("wakes = %+v, want 1 N7 wake naming say+retry", wakes)
			}
			if got := len(sendsTo(t, l.srv.Requests(), l.worker)); got != 1 {
				t.Fatalf("worker told %d times, want 1", got)
			}
			if got := ghCalls(t, l.gh, "pr merge"); len(got) != 0 {
				t.Fatalf("diverged/foreign PR was merged: %q", got)
			}
		})
	}
}

// TestN7TipBehindAfterUpdateBranchToldToPull verifies that when the worker's
// tip is behind H (e.g. after update-branch moved H and a foreign commit hits
// the PR, or when the tip is behind H without covering it), N7 tells the worker
// to pull (`git pull --ff-only`).
func TestN7TipBehindAfterUpdateBranchToldToPull(t *testing.T) {
	l := newLanding(t)
	// Base moves; simulate update-branch creating merge commit m1 and moving H to m1 while worktree stays at c1.
	if err := os.WriteFile(filepath.Join(l.repo, "base.go"), []byte("package demo\nvar B = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, l.repo, "add", "base.go")
	gitIn(t, l.repo, "commit", "-m", "feat: base")
	gitIn(t, l.repo, "push", "origin", "main")

	sim := t.TempDir()
	rawURL := gitIn(t, l.repo, "remote", "get-url", "--push", "origin")
	gitIn(t, sim, "clone", rawURL, "repo")
	simRepo := filepath.Join(sim, "repo")
	gitIn(t, simRepo, "config", "user.name", "GitHub")
	gitIn(t, simRepo, "config", "user.email", "noreply@example.com")
	gitIn(t, simRepo, "checkout", l.branch)
	gitIn(t, simRepo, "merge", "--no-edit", "origin/main")
	m1 := gitIn(t, simRepo, "rev-parse", "HEAD")
	gitIn(t, simRepo, "push", "origin", m1+":refs/heads/"+l.branch)

	writePRJSON(t, l.gh, "view.json", m1, greenRollup)
	l.pass() // N20 moves H to m1; worker tip is still c1 (behind m1)

	// Now push a foreign commit on top of m1: N7 tells worker to pull first.
	f1 := pushExternalToBranch(t, l, false, "foreign.txt", "foreign\n")
	writePRJSON(t, l.gh, "view.json", f1, greenRollup)
	l.pass()
	sends := sendsTo(t, l.srv.Requests(), l.worker)
	if len(sends) != 1 || !strings.Contains(sends[0], "git pull --ff-only") {
		t.Fatalf("sent = %q, want pull-first instruction", sends)
	}

	// Also: a worker tip only behind H (uncovered, PR head is H) is told to pull.
	l2 := newLanding(t)
	gitIn(t, l2.wt, "reset", "--hard", "HEAD~1")
	writePRJSON(t, l2.gh, "view.json", l2.c1, greenRollup)
	l2.pass()
	sends2 := sendsTo(t, l2.srv.Requests(), l2.worker)
	if len(sends2) != 1 || !strings.Contains(sends2[0], "git pull --ff-only") {
		t.Fatalf("tip-only-behind sent = %q, want git pull --ff-only", sends2)
	}
}
