package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func checkRollup(conclusion, runID, jobID string) string {
	return `[{"__typename":"CheckRun","name":"check","status":"COMPLETED","conclusion":"` + conclusion + `","detailsUrl":"https://github.com/acme/demo/actions/runs/` + runID + `/job/` + jobID + `"}]`
}

// TestCIRedRerunThenGreenNoRound verifies N33: a red check below flake_reruns
// is re-run once via gh run rerun --failed, turns green, and merges with no
// review round spent.
func TestCIRedRerunThenGreenNoRound(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, checkRollup("FAILURE", "44714", "13621"))
	l.pass()
	reruns := ghCalls(t, l.gh, "run rerun")
	if len(reruns) != 1 || reruns[0] != "run rerun 44714 --failed --repo acme/demo" {
		t.Fatalf("rerun calls = %q", reruns)
	}
	// Stale red snapshot on next pass waits instead of telling the worker prematurely.
	l.pass()
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 0 {
		t.Fatalf("worker told while rerun pending: %q", sends)
	}
	// Re-run passes -> N42 merges with the original 2 rounds (no round spent).
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	l.pass()
	a := l.state().Tasks[0].Attempts[0]
	if len(a.Rounds) != 2 || len(ghCalls(t, l.gh, "pr merge")) != 1 {
		t.Fatalf("after green rerun: rounds=%d merges=%q", len(a.Rounds), ghCalls(t, l.gh, "pr merge"))
	}
}

// TestCIRedAgainTellsWorkerOnceKBGIT14 verifies N33: after a re-run fails
// again, the worker is told once per failing run (no "pull first" when tip == H),
// and a repeat ready at the same commit does not re-tell (KB-GIT-14). When H is
// ahead of tip (engine updated branch), the message starts with "Pull first".
func TestCIRedAgainTellsWorkerOnceKBGIT14(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, checkRollup("FAILURE", "44714", "100"))
	l.pass() // re-run
	writePRJSON(t, l.gh, "view.json", l.c1, checkRollup("FAILURE", "44714", "200"))
	l.pass() // tell worker once
	l.pass() // still red: not told again
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Ready = append(s.Tasks[0].Attempts[0].Ready, flow.Ready{Head: l.c1})
	})
	l.pass() // KB-GIT-14: ready again at same commit still does not re-tell
	sends := sendsTo(t, l.srv.Requests(), l.worker)
	if len(sends) != 1 || !strings.Contains(sends[0], "44714") || strings.Contains(strings.ToLower(sends[0]), "pull first") {
		t.Fatalf("worker sends = %q, want 1 mentioning 44714 without pull first", sends)
	}
	a := l.state().Tasks[0].Attempts[0]
	if !flow.Done(a.Acts, "tell_worker_ci@44714@"+l.c1) {
		t.Fatalf("acts = %+v, want tell_worker_ci@44714@%s", a.Acts, l.c1)
	}

	// Engine updated branch (TipRel == RelBehind): execTellCI adds "Pull first".
	s := l.state()
	dec, _, err := l.eng.execTellCI(context.Background(), l.id, &s, 0, flow.Facts{
		TipRel: flow.RelBehind,
		PR:     flow.PR{Number: 7, FailedRun: "99999", FailedChecks: "check"},
	})
	if err != nil || !strings.Contains(dec, "99999") {
		t.Fatalf("execTellCI(behind) = %q, %v", dec, err)
	}
	sends = sendsTo(t, l.srv.Requests(), l.worker)
	if len(sends) != 2 || !strings.HasPrefix(sends[1], "Pull first") {
		t.Fatalf("behind worker msg = %q, want Pull first prefix", sends[len(sends)-1])
	}
}

// TestCIStoppedExternalAndTimeout verifies N34: an externally cancelled check
// is re-run once then wakes the coordinator once; a job time limit, or a
// cancel whose cause cannot be read, wakes once without re-running.
func TestCIStoppedExternalAndTimeout(t *testing.T) {
	t.Run("external cancel", func(t *testing.T) {
		l := newLanding(t)
		writePRJSON(t, l.gh, "view.json", l.c1, checkRollup("CANCELLED", "55001", "10"))
		l.pass() // re-run
		if len(ghCalls(t, l.gh, "run rerun")) != 1 {
			t.Fatalf("external cancel rerun calls = %q", ghCalls(t, l.gh, "run rerun"))
		}
		writePRJSON(t, l.gh, "view.json", l.c1, checkRollup("CANCELLED", "55001", "20"))
		l.pass() // no re-runs left -> wake once
		l.pass()
		if w := l.state().Project.Wakes; len(w) != 1 || w[0].Key != "checks_stopped@external@"+l.c1 {
			t.Fatalf("wakes = %+v, want 1 external wake", w)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		l := newLanding(t)
		if err := os.WriteFile(filepath.Join(l.gh, "annotations.json"), []byte(`[{"message":"The job has exceeded the maximum execution time of 1m0s"}]`), 0o600); err != nil {
			t.Fatal(err)
		}
		writePRJSON(t, l.gh, "view.json", l.c1, checkRollup("CANCELLED", "55002", "30"))
		l.pass()
		l.pass()
		if len(ghCalls(t, l.gh, "run rerun")) != 0 {
			t.Fatalf("timeout must not re-run: %q", ghCalls(t, l.gh, "run rerun"))
		}
		if w := l.state().Project.Wakes; len(w) != 1 || w[0].Key != "checks_stopped@timeout@"+l.c1 {
			t.Fatalf("wakes = %+v, want 1 timeout wake", w)
		}
	})
	t.Run("cause unread", func(t *testing.T) {
		l := newLanding(t)
		if err := os.WriteFile(filepath.Join(l.gh, "annotations_fail"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		writePRJSON(t, l.gh, "view.json", l.c1, checkRollup("CANCELLED", "55003", "40"))
		l.pass()
		l.pass()
		if len(ghCalls(t, l.gh, "run rerun")) != 0 {
			t.Fatalf("unread cause must not re-run: %q", ghCalls(t, l.gh, "run rerun"))
		}
		if w := l.state().Project.Wakes; len(w) != 1 || w[0].Key != "checks_stopped@unknown@"+l.c1 {
			t.Fatalf("wakes = %+v, want 1 unknown wake", w)
		}
	})
}

// TestCINoChecksDispatchAndWake verifies N35: past checks_grace_minutes, a
// workflow_dispatch workflow is dispatched once, waits within grace, then wakes
// the coordinator once naming the ways out; without workflow_dispatch it skips
// directly to the wake; absent checks never merge.
func TestCINoChecksDispatchAndWake(t *testing.T) {
	agePR := func(l *landing) {
		l.eng.mu.Lock()
		p := l.eng.prs[l.wt]
		p.HeadSince = time.Now().Add(-10 * time.Minute)
		l.eng.prs[l.wt] = p
		l.eng.mu.Unlock()
	}
	t.Run("with workflow_dispatch", func(t *testing.T) {
		l := newLanding(t)
		wfDir := filepath.Join(l.wt, ".github", "workflows")
		if err := os.MkdirAll(wfDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wfDir, "ci.yml"), []byte("on:\n  pull_request:\n  workflow_dispatch:\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, l.wt, "add", ".github/workflows/ci.yml")
		gitIn(t, l.wt, "commit", "-m", "ci: add dispatch")
		c2 := gitIn(t, l.wt, "rev-parse", "HEAD")
		l.edit(func(s *flow.State) {
			a := &s.Tasks[0].Attempts[0]
			a.Ready = []flow.Ready{{Head: c2}}
			a.Rounds = []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass}, {N: 2, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass}}
		})
		writePRJSON(t, l.gh, "view.json", c2, `[]`)
		l.pass() // within grace -> N36 waits
		agePR(l)
		l.pass() // past grace -> N35 dispatches once
		dispatches := ghCalls(t, l.gh, "workflow run")
		if len(dispatches) != 1 || dispatches[0] != "workflow run ci.yml --repo acme/demo --ref "+l.branch {
			t.Fatalf("dispatches = %q", dispatches)
		}
		l.pass() // dispatched < grace ago -> wait, no wake yet
		if len(l.state().Project.Wakes) != 0 {
			t.Fatalf("woke before dispatch grace elapsed: %+v", l.state().Project.Wakes)
		}
		// Age the dispatch act past grace -> wakes coordinator once naming ways out.
		l.edit(func(s *flow.State) {
			for i := range s.Tasks[0].Attempts[0].Acts {
				if s.Tasks[0].Attempts[0].Acts[i].Key == "dispatch_ci@"+c2 {
					s.Tasks[0].Attempts[0].Acts[i].At = time.Now().Add(-10 * time.Minute)
				}
			}
		})
		l.pass()
		l.pass()
		w := l.state().Project.Wakes
		if len(w) != 1 || w[0].Key != "no_checks@"+c2 || !strings.Contains(w[0].Why, "override") || !strings.Contains(w[0].Why, "drop") {
			t.Fatalf("wakes = %+v", w)
		}
		if len(ghCalls(t, l.gh, "pr merge")) != 0 {
			t.Fatal("PR with absent checks was merged")
		}
	})
	t.Run("no workflow_dispatch skips to wake", func(t *testing.T) {
		l := newLanding(t)
		writePRJSON(t, l.gh, "view.json", l.c1, `[]`)
		l.pass()
		agePR(l)
		l.pass() // records skipped dispatch
		l.pass() // wakes coordinator immediately without waiting grace
		if len(ghCalls(t, l.gh, "workflow run")) != 0 || len(ghCalls(t, l.gh, "pr merge")) != 0 {
			t.Fatalf("unexpected gh calls: dispatch=%q merge=%q", ghCalls(t, l.gh, "workflow run"), ghCalls(t, l.gh, "pr merge"))
		}
		a := l.state().Tasks[0].Attempts[0]
		if act := flow.LastAct(a.Acts, "dispatch_ci@"+l.c1); act == nil || act.Result != "skipped" {
			t.Fatalf("dispatch act = %+v, want skipped", act)
		}
		if w := l.state().Project.Wakes; len(w) != 1 || w[0].Key != "no_checks@"+l.c1 {
			t.Fatalf("wakes = %+v, want 1 no_checks wake", w)
		}
	})
}

// TestCIExecutorFailuresRecordedOnce verifies failed rerun and dispatch calls
// are recorded as acts and not retried on every pass.
func TestCIExecutorFailuresRecordedOnce(t *testing.T) {
	l := newLanding(t)
	if err := os.WriteFile(filepath.Join(l.gh, "rerun_fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "idle"})
	writePRJSON(t, l.gh, "view.json", l.c1, checkRollup("FAILURE", "44714", "100"))
	l.pass() // rerun fails, recorded once
	l.pass() // does not re-run again; tells worker once
	l.pass()
	if len(ghCalls(t, l.gh, "run rerun")) != 1 {
		t.Fatalf("failed rerun called %d times, want 1", len(ghCalls(t, l.gh, "run rerun")))
	}
	a := l.state().Tasks[0].Attempts[0]
	if act := flow.LastAct(a.Acts, "rerun_ci@44714@100@"+l.c1); act == nil || act.Result != "failed" {
		t.Fatalf("rerun act = %+v, want failed", act)
	}
}
