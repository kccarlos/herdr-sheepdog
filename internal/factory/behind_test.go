package factory

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// TestN40BehindUpdateAndMergeNoRound verifies KB-MRG-43 and N40 end to end:
// a behind PR is updated once with expected_head_sha=H, waits within grace,
// moves H on the new merge_only PR head without touching the worker worktree,
// waits while the new head's checks are pending, and merges only when the new
// head's checks pass — with no new review round spent.
func TestN40BehindUpdateAndMergeNoRound(t *testing.T) {
	l := newLanding(t)
	// Base moves on origin/main; PR #7 at c1 reads behind.
	if err := os.WriteFile(filepath.Join(l.repo, "base.go"), []byte("package demo\nvar Base = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, l.repo, "add", "base.go")
	gitIn(t, l.repo, "commit", "-m", "feat: base moves")
	gitIn(t, l.repo, "push", "origin", "main")
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)

	// Pass 1: N40 calls GitHub update-branch pinned to H (c1); no merge yet.
	l.pass()
	updates := ghCalls(t, l.gh, "api -X PUT")
	wantUpdate := "api -X PUT repos/acme/demo/pulls/7/update-branch -f expected_head_sha=" + l.c1
	if len(updates) != 1 || updates[0] != wantUpdate {
		t.Fatalf("update calls = %q, want [%q]", updates, wantUpdate)
	}
	if len(ghCalls(t, l.gh, "pr merge")) != 0 {
		t.Fatal("behind PR was merged before update")
	}

	// Pass 2 (within grace, PR still at c1): waits, no second update-branch call.
	l.pass()
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 1 {
		t.Fatalf("update calls within grace = %q, want 1", got)
	}

	// Simulate GitHub creating a clean merge commit m1 on the PR branch on origin
	// (without touching the worker's worktree l.wt, which stays at c1).
	// Clone from l.repo's bare remote using a fresh clone so l.wt never has m1 or new origin/main yet.
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

	// New head m1 has pending checks and behind_by 0.
	pendingRollup := `[{"__typename":"CheckRun","name":"check","workflowName":"ci","status":"IN_PROGRESS","detailsUrl":"https://github.com/acme/demo/actions/runs/99/job/990"}]`
	writePRJSON(t, l.gh, "view.json", m1, pendingRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":0}`)

	// Pass 3: readPRLocked fetches m1 + origin/main, N20 moves H to m1; no merge, no new round.
	l.pass()
	a := l.state().Tasks[0].Attempts[0]
	if flow.H(a) != m1 || len(a.MergeOnly) != 1 || a.MergeOnly[0] != (flow.Advance{From: l.c1, To: m1}) {
		t.Fatalf("after PR update: H=%s merge_only=%+v, want %s", flow.H(a), a.MergeOnly, m1)
	}
	if len(a.Rounds) != 2 || a.Review != nil || len(ghCalls(t, l.gh, "pr merge")) != 0 {
		t.Fatalf("after move to m1 (pending): rounds=%d review=%+v merges=%q", len(a.Rounds), a.Review, ghCalls(t, l.gh, "pr merge"))
	}

	// Pass 4: checks still pending at m1 -> waits at N36, no merge, no review.
	l.pass()
	if len(ghCalls(t, l.gh, "pr merge")) != 0 || l.state().Tasks[0].Attempts[0].Review != nil {
		t.Fatal("merged or started review while m1 checks were pending")
	}

	// Pass 5: m1 checks turn green -> N42 accepts and merges m1 with original 2 rounds.
	writePRJSON(t, l.gh, "view.json", m1, greenRollup)
	l.pass()
	a = l.state().Tasks[0].Attempts[0]
	merges := ghCalls(t, l.gh, "pr merge")
	if len(merges) != 1 || !strings.Contains(merges[0], "--match-head-commit "+m1) {
		t.Fatalf("merges = %q, want 1 pinned to %s", merges, m1)
	}
	if len(a.Rounds) != 2 || a.Review != nil || len(a.Accepts) != 1 || a.Accepts[0].Head != m1 || !slices.Equal(a.Accepts[0].Rounds, []int{1, 2}) {
		t.Fatalf("after merge: rounds=%d review=%+v accepts=%+v", len(a.Rounds), a.Review, a.Accepts)
	}
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 1 {
		t.Fatalf("total update-branch calls = %d, want 1", len(got))
	}
}

// TestN40BehindPastGraceFailedTry verifies that when update-branch does not
// move the PR head within checks_grace_minutes, N40 records a failed try and
// does not re-ask update-branch.
func TestN40BehindPastGraceFailedTry(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	l.pass() // asks update_branch@c1
	// Age the act past checks_grace_minutes.
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		for i := range a.Acts {
			if a.Acts[i].Key == flow.AskUpdateBranch+"@"+l.c1 {
				a.Acts[i].At = time.Now().Add(-time.Duration(s.Settings.ChecksGraceMinutes+1) * time.Minute)
			}
		}
	})
	l.pass() // marks update_branch@c1 failed
	l.pass() // stays failed, no second update-branch call
	a := l.state().Tasks[0].Attempts[0]
	act := flow.LastAct(a.Acts, flow.AskUpdateBranch+"@"+l.c1)
	if act == nil || act.Result != "failed" {
		t.Fatalf("update_branch act = %+v, want failed", act)
	}
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 1 {
		t.Fatalf("update calls = %q, want 1", got)
	}
}

// TestN40BehindUnknownWaits verifies that a failed compare (BehindUnknown)
// never calls update-branch: it waits at N36 when checks are pending (or behind
// is also set) and does not block N42 when checks are green and not behind.
func TestN40BehindUnknownWaits(t *testing.T) {
	l := newLanding(t)
	pendingRollup := `[{"__typename":"CheckRun","name":"check","workflowName":"ci","status":"IN_PROGRESS","detailsUrl":"https://github.com/acme/demo/actions/runs/99/job/990"}]`
	writePRJSON(t, l.gh, "view.json", l.c1, pendingRollup)
	writeGHFile(t, l.gh, "compare_fail", "")
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 0 {
		t.Fatalf("update calls on BehindUnknown = %q, want none", got)
	}
	if got := ghCalls(t, l.gh, "pr merge"); len(got) != 0 {
		t.Fatalf("merge calls on BehindUnknown (pending) = %q, want none", got)
	}
	// Once checks are green and not behind, BehindUnknown does not block N42.
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	l.pass()
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 0 {
		t.Fatalf("update calls on BehindUnknown (green) = %q, want none", got)
	}
	if got := ghCalls(t, l.gh, "pr merge"); len(got) != 1 {
		t.Fatalf("merge calls on BehindUnknown (green, not behind) = %q, want 1", got)
	}
}

// TestKBMRG6WakeNoCommitKeepsPass verifies KB-MRG-6: a passed task whose worker
// wakes and ends its turn with no new commit keeps its pass and merges once
// checks are green, with no review round spent.
func TestKBMRG6WakeNoCommitKeepsPass(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	// Worker wakes (working), no new commit: N39 holds, no merge, pass kept.
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
	l.pass()
	if got := ghCalls(t, l.gh, "pr merge"); len(got) != 0 {
		t.Fatalf("merged while worker working: %q", got)
	}
	// Worker finishes turn on the same commit c1: N42 merges, still 2 rounds.
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "idle"})
	l.pass()
	a := l.state().Tasks[0].Attempts[0]
	if merges := ghCalls(t, l.gh, "pr merge"); len(merges) != 1 || len(a.Rounds) != 2 || a.Review != nil {
		t.Fatalf("after stray wake: merges=%q rounds=%d review=%+v", merges, len(a.Rounds), a.Review)
	}
}
