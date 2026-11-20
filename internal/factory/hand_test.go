package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// TestN4PRClosedByHand verifies N4 end to end: a PR closed without merging
// stops any active reviewer, wakes the coordinator once, never reopens the PR,
// never opens a second PR, and never merges.
func TestN4PRClosedByHand(t *testing.T) {
	l := newLanding(t)
	revID, err := startRun(context.Background(), l.client, world.RunSpec{Name: "rev-t1-1", Cwd: l.wt, Argv: []string{"sh"}})
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
	if r := st.Tasks[0].Attempts[0].Review; r == nil || r.RunID != "" || r.Run == nil || r.Run.Result != flow.EndStopped {
		t.Fatalf("active reviewer was not stopped on closed PR: %+v", r)
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
			setIdle(t, l.srv, l.client, l.worker, "idle")
			l.pass()
			l.pass()
			wakes := l.state().Project.Wakes
			if len(wakes) != 1 || wakes[0].Key != "diverged@"+l.c1 || !strings.Contains(wakes[0].Why, "say") || !strings.Contains(wakes[0].Why, "retry") {
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

// simulateUpdateBranch advances origin/main from a separate clone (leaving
// l.wt's refs/remotes/origin/main stale) and pushes a clean merge commit onto
// l's remote branch, returning that merge commit SHA.
func simulateUpdateBranch(t *testing.T, l *landing) string {
	t.Helper()
	sim := t.TempDir()
	rawURL := gitIn(t, l.repo, "remote", "get-url", "--push", "origin")
	gitIn(t, sim, "clone", rawURL, "repo")
	simRepo := filepath.Join(sim, "repo")
	gitIn(t, simRepo, "config", "user.name", "GitHub")
	gitIn(t, simRepo, "config", "user.email", "noreply@example.com")
	if err := os.WriteFile(filepath.Join(simRepo, "base.go"), []byte("package demo\nvar B = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, simRepo, "add", "base.go")
	gitIn(t, simRepo, "commit", "-m", "feat: base")
	gitIn(t, simRepo, "push", "origin", "main")
	gitIn(t, simRepo, "checkout", l.branch)
	gitIn(t, simRepo, "merge", "--no-edit", "origin/main")
	m1 := gitIn(t, simRepo, "rev-parse", "HEAD")
	gitIn(t, simRepo, "push", "origin", m1+":refs/heads/"+l.branch)
	return m1
}

// TestN7UpdateBranchMergeOnlyFetchFailsWaits verifies item 1: when the
// merge-only fetch fails on an update-branch head, its merge_only state is
// unknown (not foreign), so N7 waits instead of telling/waking; once fetch
// recovers on the next pass, N20 moves H to the merge commit.
func TestN7UpdateBranchMergeOnlyFetchFailsWaits(t *testing.T) {
	l := newLanding(t)
	m1 := simulateUpdateBranch(t, l)
	// Fetch m1 into l.wt's object store without updating refs/remotes/origin/main
	// so Relation(c1, m1) is RelAhead while MergeOnly sees a stale origin/main.
	gitIn(t, l.wt, "fetch", "origin", m1)
	// Break origin fetch so readPRLocked's FetchRemote retry fails.
	goodURL := gitIn(t, l.wt, "config", "--get", "url."+gitIn(t, l.repo, "remote", "get-url", "--push", "origin")+".insteadOf")
	barePath := gitIn(t, l.repo, "remote", "get-url", "--push", "origin")
	_ = goodURL
	gitIn(t, l.wt, "config", "--unset", "url."+barePath+".insteadOf")
	gitIn(t, l.wt, "config", "url./nonexistent/remote.git.insteadOf", "https://github.com/acme/demo.git")

	writePRJSON(t, l.gh, "view.json", m1, greenRollup)
	l.pass()
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 0 {
		t.Fatalf("merge_only unknown told worker: %q", sends)
	}
	if wakes := l.state().Project.Wakes; len(wakes) != 0 {
		t.Fatalf("merge_only unknown woke coordinator: %+v", wakes)
	}

	// Age HeadSince past checks_grace_minutes -> wakes coordinator once, never tells or merges.
	l.eng.mu.Lock()
	p := l.eng.prs[l.wt]
	p.HeadSince = time.Now().Add(-time.Duration(l.state().Settings.ChecksGraceMinutes+1) * time.Minute)
	l.eng.prs[l.wt] = p
	l.eng.mu.Unlock()
	l.pass()
	l.pass()
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 0 {
		t.Fatalf("merge_only unknown past grace told worker: %q", sends)
	}
	if merges := ghCalls(t, l.gh, "pr merge"); len(merges) != 0 {
		t.Fatalf("merge_only unknown past grace merged: %q", merges)
	}
	wantWake := "merge_only_unknown@" + l.c1
	if wakes := l.state().Project.Wakes; len(wakes) != 1 || wakes[0].Key != wantWake {
		t.Fatalf("wakes past grace = %+v, want 1 with key %q", wakes, wantWake)
	}

	// Restore remote and pass again: fetch succeeds, N20 moves H to m1.
	gitIn(t, l.wt, "config", "--unset", "url./nonexistent/remote.git.insteadOf")
	gitIn(t, l.wt, "config", "url."+barePath+".insteadOf", "https://github.com/acme/demo.git")
	l.pass()
	if got := flow.H(l.state().Tasks[0].Attempts[0]); got != m1 {
		t.Fatalf("after fetch recovery, H = %s, want %s", got, m1)
	}
}

// TestN7TellsOnceAndWakesOncePerH verifies item 2: N7 keys tell and wake on H
// only, so a worker committing again on a diverged tip is not told a second
// time and the coordinator is woken once at turn end.
func TestN7TellsOnceAndWakesOncePerH(t *testing.T) {
	l := newLanding(t)
	// Diverge tip at t1.
	gitIn(t, l.wt, "commit", "--allow-empty", "--amend", "-m", "feat: diverged t1")
	l.pass()
	if got := len(sendsTo(t, l.srv.Requests(), l.worker)); got != 1 {
		t.Fatalf("pass 1 sends = %d, want 1", got)
	}

	// Worker works and makes another commit t2 on the diverged tip.
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
	gitIn(t, l.wt, "commit", "--allow-empty", "-m", "feat: diverged t2")
	l.pass()
	if got := len(sendsTo(t, l.srv.Requests(), l.worker)); got != 1 {
		t.Fatalf("while working at t2 sends = %d, want 1", got)
	}

	// Worker turn ends at t2: must wake once (keyed on H), NOT send a second tell.
	setIdle(t, l.srv, l.client, l.worker, "idle")
	l.pass()
	if got := len(sendsTo(t, l.srv.Requests(), l.worker)); got != 1 {
		t.Fatalf("after turn end at t2 sends = %d, want 1 (no second tell)", got)
	}
	wakes := l.state().Project.Wakes
	if len(wakes) != 1 || wakes[0].Key != "diverged@"+l.c1 {
		t.Fatalf("wakes = %+v, want 1 wake keyed diverged@%s", wakes, l.c1)
	}

	// Another commit t3 does not tell or wake again for the same H.
	gitIn(t, l.wt, "commit", "--allow-empty", "-m", "feat: diverged t3")
	l.pass()
	if got := len(sendsTo(t, l.srv.Requests(), l.worker)); got != 1 {
		t.Fatalf("at t3 sends = %d, want 1", got)
	}
	if len(l.state().Project.Wakes) != 1 {
		t.Fatalf("at t3 wakes = %+v, want still 1", l.state().Project.Wakes)
	}
}

// TestN7WaitsForWorkerTurnBeforeWaking verifies that N7 waits within
// checks_grace_minutes for the worker's turn, wakes early if the worker's turn
// end is observed, and never holds forever: if the worker is never seen working
// (a turn shorter than a pass) or the engine restarts after the tell (losing
// in-memory turn state), N7 wakes the coordinator once past checks_grace_minutes.
func TestN7WaitsForWorkerTurnBeforeWaking(t *testing.T) {
	ageTell := func(l *landing) {
		l.edit(func(s *flow.State) {
			a := &s.Tasks[0].Attempts[0]
			for i := range a.Acts {
				if a.Acts[i].Key == flow.AskTellDiverged+"@"+l.c1 {
					a.Acts[i].At = time.Now().Add(-time.Duration(s.Settings.ChecksGraceMinutes+1) * time.Minute)
				}
			}
		})
	}

	t.Run("turn end wakes early", func(t *testing.T) {
		l := newLanding(t)
		ext := pushExternalToBranch(t, l, true, "ext.txt", "external\n")
		writePRJSON(t, l.gh, "view.json", ext, greenRollup)

		// Pass 1: idle worker is told.
		l.pass()
		if len(sendsTo(t, l.srv.Requests(), l.worker)) != 1 || len(l.state().Project.Wakes) != 0 {
			t.Fatalf("pass 1: sends=%d wakes=%+v", len(sendsTo(t, l.srv.Requests(), l.worker)), l.state().Project.Wakes)
		}

		// Pass 2: worker is still idle within checks_grace_minutes -> waits.
		l.pass()
		if wakes := l.state().Project.Wakes; len(wakes) != 0 {
			t.Fatalf("pass 2 (idle within grace): wakes = %+v, want none", wakes)
		}

		// Pass 3: worker is working -> still no wake.
		l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
		l.pass()
		if wakes := l.state().Project.Wakes; len(wakes) != 0 {
			t.Fatalf("pass 3 (working): wakes = %+v, want none", wakes)
		}

		// Pass 4: worker is idle again (turn ended within grace) -> wakes once early.
		setIdle(t, l.srv, l.client, l.worker, "idle")
		l.pass()
		if wakes := l.state().Project.Wakes; len(wakes) != 1 || wakes[0].Key != "diverged@"+l.c1 {
			t.Fatalf("pass 4 (after turn end): wakes = %+v, want 1 wake keyed diverged@%s", wakes, l.c1)
		}
	})

	t.Run("never seen working wakes after grace", func(t *testing.T) {
		l := newLanding(t)
		ext := pushExternalToBranch(t, l, true, "ext.txt", "external\n")
		writePRJSON(t, l.gh, "view.json", ext, greenRollup)

		l.pass() // idle worker is told
		l.pass() // still idle within grace -> waits
		if wakes := l.state().Project.Wakes; len(wakes) != 0 {
			t.Fatalf("within grace: wakes = %+v, want none", wakes)
		}

		// Worker never showed as working (turn shorter than a pass); tell ages past grace.
		ageTell(l)
		l.pass()
		l.pass()
		if wakes := l.state().Project.Wakes; len(wakes) != 1 || wakes[0].Key != "diverged@"+l.c1 {
			t.Fatalf("after grace without working: wakes = %+v, want 1 wake keyed diverged@%s", wakes, l.c1)
		}
		if got := len(sendsTo(t, l.srv.Requests(), l.worker)); got != 1 {
			t.Fatalf("worker told %d times, want 1", got)
		}
		if got := ghCalls(t, l.gh, "pr merge"); len(got) != 0 {
			t.Fatalf("diverged PR was merged: %q", got)
		}
	})

	t.Run("engine restart wakes after grace", func(t *testing.T) {
		l := newLanding(t)
		ext := pushExternalToBranch(t, l, true, "ext.txt", "external\n")
		writePRJSON(t, l.gh, "view.json", ext, greenRollup)

		l.pass() // idle worker is told
		// Engine restarts: in-memory workerTurn map is lost.
		oldLog := l.eng.Log
		l.eng = New(l.paths, l.client)
		l.eng.Log = oldLog

		ageTell(l)
		l.pass()
		l.pass()
		if wakes := l.state().Project.Wakes; len(wakes) != 1 || wakes[0].Key != "diverged@"+l.c1 {
			t.Fatalf("after restart and grace: wakes = %+v, want 1 wake keyed diverged@%s", wakes, l.c1)
		}
		if got := len(sendsTo(t, l.srv.Requests(), l.worker)); got != 1 {
			t.Fatalf("worker told %d times, want 1", got)
		}
	})
}

// TestPRSnapshotByNumberAndBaseCheck verifies item 4: once the attempt has a
// PR number, PRSnapshot reads by number (`gh pr view <n>`), and a PR whose
// base differs from the project base is not ours (never merged, never wakes N4).
func TestPRSnapshotByNumberAndBaseCheck(t *testing.T) {
	l := newLanding(t)
	l.pass()
	views := ghCalls(t, l.gh, "pr view")
	if len(views) == 0 || !strings.HasPrefix(views[0], "pr view 7 --repo ") {
		t.Fatalf("pr view calls = %q, want `pr view 7 --repo ...`", views)
	}

	// Open PR on another base ("release") must never be merged.
	l2 := newLanding(t)
	writeGHFile(t, l2.gh, "view.json", `{"number":7,"state":"OPEN","headRefOid":"`+l2.c1+`","mergeable":"MERGEABLE","statusCheckRollup":`+greenRollup+`,"baseRefName":"release"}`)
	l2.pass()
	if merges := ghCalls(t, l2.gh, "pr merge"); len(merges) != 0 {
		t.Fatalf("other-base PR was merged: %q", merges)
	}

	// Closed PR on another base must not wake N4.
	l3 := newLanding(t)
	l3.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].PR = 0 })
	writeGHFile(t, l3.gh, "view.json", `{"number":9,"state":"CLOSED","headRefOid":"`+l3.c1+`","mergeable":"MERGEABLE","statusCheckRollup":[],"baseRefName":"release"}`)
	l3.pass()
	if wakes := l3.state().Project.Wakes; len(wakes) != 0 {
		t.Fatalf("closed other-base PR woke coordinator: %+v", wakes)
	}
}

// TestN7DivergedTipAfterUpdateBranchToldToPull verifies item 5: when the
// worker's tip diverged from H only because the engine updated the branch and
// the worker committed without pulling, N7 tells the worker to pull (merge
// origin/<branch>), not restore.
func TestN7DivergedTipAfterUpdateBranchToldToPull(t *testing.T) {
	l := newLanding(t)
	m1 := simulateUpdateBranch(t, l)
	writePRJSON(t, l.gh, "view.json", m1, greenRollup)
	l.pass() // N20 moves H to m1; worker tip is still c1

	// Worker commits on top of c1 without pulling m1: tip is diverged from m1, ahead of c1.
	if err := os.WriteFile(filepath.Join(l.wt, "more.go"), []byte("package demo\nvar M = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, l.wt, "add", "more.go")
	gitIn(t, l.wt, "commit", "-m", "feat: more")
	l.pass()

	sends := sendsTo(t, l.srv.Requests(), l.worker)
	if len(sends) != 1 || !strings.Contains(sends[0], "pull") || strings.Contains(sends[0], "restore") {
		t.Fatalf("sent = %q, want pull (not restore)", sends)
	}
}
