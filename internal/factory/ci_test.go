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
	return `[{"__typename":"CheckRun","name":"check","workflowName":"ci","status":"COMPLETED","conclusion":"` + conclusion + `","detailsUrl":"https://github.com/acme/demo/actions/runs/` + runID + `/job/` + jobID + `"}]`
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
		Ask:    "tell_worker_ci@99999@" + l.c1,
		PR:     flow.PR{Number: 7, Failed: []flow.Failure{{Run: "99999", Name: "check"}}},
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
		writeGHFile(t, l.gh, "annotations.json", `[{"message":"The run was canceled by @example-user."}]`)
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
		if w := l.state().Project.Wakes; len(w) != 1 || w[0].Key != "checks_stopped@limit@"+l.c1 {
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
		if w := l.state().Project.Wakes; len(w) != 1 || w[0].Key != "checks_stopped@limit@"+l.c1 {
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
	t.Run("named workflow", func(t *testing.T) {
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
			s.Settings.DispatchWorkflow = "ci.yml"
			a := &s.Tasks[0].Attempts[0]
			a.Ready = []flow.Ready{{Head: c2}}
			a.Rounds = []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass}, {N: 2, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass}}
		})
		writePRJSON(t, l.gh, "view.json", c2, `[]`)
		l.pass() // within grace -> N36 waits
		agePR(l)
		l.pass() // past grace -> N35 dispatches once
		dispatches := ghCalls(t, l.gh, "api repos/acme/demo/actions/workflows/")
		if len(dispatches) != 1 || dispatches[0] != "api repos/acme/demo/actions/workflows/ci.yml/dispatches -X POST -f ref="+l.branch {
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
	t.Run("empty dispatch_workflow skips to wake", func(t *testing.T) {
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

// ciCheck is one rollup check of a workflow run and job.
func ciCheck(name, status, conclusion, runID, jobID string) string {
	return `{"__typename":"CheckRun","name":"` + name + `","workflowName":"ci","status":"` + status + `","conclusion":"` + conclusion +
		`","detailsUrl":"https://github.com/acme/demo/actions/runs/` + runID + `/job/` + jobID + `"}`
}

func writeGHFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCIWaitsForUnfinishedChecks: a red job beside an unfinished one is not
// re-run and not told until every check has finished.
func TestCIWaitsForUnfinishedChecks(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, "["+ciCheck("a", "COMPLETED", "FAILURE", "61", "610")+","+ciCheck("b", "IN_PROGRESS", "", "61", "611")+"]")
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "run rerun"); len(got) != 0 || len(sendsTo(t, l.srv.Requests(), l.worker)) != 0 {
		t.Fatalf("acted while a check runs: reruns %q", got)
	}
	writePRJSON(t, l.gh, "view.json", l.c1, "["+ciCheck("a", "COMPLETED", "FAILURE", "61", "610")+","+ciCheck("b", "COMPLETED", "SUCCESS", "61", "611")+"]")
	l.pass()
	if got := ghCalls(t, l.gh, "run rerun"); len(got) != 1 || got[0] != "run rerun 61 --failed --repo acme/demo" {
		t.Fatalf("reruns after the run finished = %q", got)
	}
}

// TestCITellsOncePerRun: with re-runs off, run 42 is told once; when run 55
// also fails while 42 stays red, only 55 is told.
func TestCITellsOncePerRun(t *testing.T) {
	l := newLanding(t)
	l.edit(func(s *flow.State) { s.Settings.FlakeReruns = 0 })
	writePRJSON(t, l.gh, "view.json", l.c1, "["+ciCheck("a", "COMPLETED", "FAILURE", "42", "420")+"]")
	l.pass()
	writePRJSON(t, l.gh, "view.json", l.c1, "["+ciCheck("a", "COMPLETED", "FAILURE", "42", "420")+","+ciCheck("b", "COMPLETED", "FAILURE", "55", "550")+"]")
	l.pass()
	l.pass()
	sends := sendsTo(t, l.srv.Requests(), l.worker)
	if len(sends) != 2 || !strings.Contains(sends[0], "42") || !strings.Contains(sends[1], "55") || strings.Contains(sends[1], "run 42") {
		t.Fatalf("worker sends = %q, want one about 42, then one about 55 only", sends)
	}
}

// TestCIRerunBudgetPerRun: each run id gets its own re-run request and
// budget; a failed request for run 55 is spent, never sent again for the
// same job once run 42 clears.
func TestCIRerunBudgetPerRun(t *testing.T) {
	l := newLanding(t)
	l.edit(func(s *flow.State) { s.Settings.FlakeReruns = 2 })
	writeGHFile(t, l.gh, "rerun_fail_55", "")
	writePRJSON(t, l.gh, "view.json", l.c1, "["+ciCheck("a", "COMPLETED", "FAILURE", "42", "420")+","+ciCheck("b", "COMPLETED", "FAILURE", "55", "550")+"]")
	l.pass()
	writePRJSON(t, l.gh, "view.json", l.c1, "["+ciCheck("a", "COMPLETED", "SUCCESS", "42", "421")+","+ciCheck("b", "COMPLETED", "FAILURE", "55", "550")+"]")
	l.pass()
	l.pass()
	var for55 []string
	for _, c := range ghCalls(t, l.gh, "run rerun") {
		if strings.HasPrefix(c, "run rerun 55 ") {
			for55 = append(for55, c)
		}
	}
	if len(for55) != 1 {
		t.Fatalf("re-runs of 55 = %q, want 1 (the failed request is spent)", for55)
	}
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 1 || !strings.Contains(sends[0], "55") {
		t.Fatalf("worker sends = %q, want one about 55", sends)
	}
}

// TestCICancelNeedsEvidence: a cancel with no time-limit and no
// canceled-by annotation is an unknown stop: one wake, never a re-run; a
// later read that finds the time limit does not wake again.
func TestCICancelNeedsEvidence(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, checkRollup("CANCELLED", "57001", "10"))
	l.pass()
	writeGHFile(t, l.gh, "annotations.json", `[{"message":"The job has exceeded the maximum execution time of 1m0s"}]`)
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "run rerun"); len(got) != 0 {
		t.Fatalf("cancel without evidence re-ran: %q", got)
	}
	if w := l.state().Project.Wakes; len(w) != 1 {
		t.Fatalf("wakes = %+v, want exactly 1", w)
	}
}

// TestCIStaleRerunWaitIsBounded: an accepted re-run whose job never
// changes waits at most checks_grace_minutes, then the worker is told once
// and nothing is re-run again.
func TestCIStaleRerunWaitIsBounded(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, checkRollup("FAILURE", "44714", "100"))
	l.pass() // re-run
	l.pass() // stale: wait
	l.edit(func(s *flow.State) {
		for i := range s.Tasks[0].Attempts[0].Acts {
			s.Tasks[0].Attempts[0].Acts[i].At = time.Now().Add(-10 * time.Minute)
		}
	})
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "run rerun"); len(got) != 1 {
		t.Fatalf("reruns = %q, want 1", got)
	}
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 1 || !strings.Contains(sends[0], "44714") {
		t.Fatalf("worker sends = %q, want one about 44714", sends)
	}
}

// TestCIRerunNotRepeatedAfterFailedCommit: when the commit after a re-run
// fails, the intent saved before the call keeps the next pass from sending
// it again.
func TestCIRerunNotRepeatedAfterFailedCommit(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, checkRollup("FAILURE", "44714", "100"))
	if err := os.Chmod(l.paths.Log(l.id), 0o400); err != nil { // the commit's log append fails
		t.Fatal(err)
	}
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "run rerun"); len(got) != 1 {
		t.Fatalf("reruns = %q, want 1", got)
	}
}

// TestCIDispatchOptInAndFailureOnce: N35 never dispatches a workflow the
// owner did not name, however dispatchable; with dispatch_workflow = ci.yml
// a failed dispatch is recorded once, sent once, and wakes once.
func TestCIDispatchOptInAndFailureOnce(t *testing.T) {
	l := newLanding(t)
	wfDir := filepath.Join(l.wt, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeGHFile(t, wfDir, "a-deploy.yml", "on:\n  pull_request:\n  workflow_dispatch:\njobs: {}\n")
	writeGHFile(t, wfDir, "ci.yml", "on:\n  pull_request:\n  workflow_dispatch:\njobs: {}\n")
	writeGHFile(t, l.gh, "dispatch_fail", "")
	gitIn(t, l.wt, "add", ".github")
	gitIn(t, l.wt, "commit", "-m", "ci: workflows")
	c2 := gitIn(t, l.wt, "rev-parse", "HEAD")
	l.edit(func(s *flow.State) {
		s.Settings.DispatchWorkflow = "ci.yml"
		a := &s.Tasks[0].Attempts[0]
		a.Ready = []flow.Ready{{Head: c2}}
		a.Rounds = []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass}, {N: 2, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass}}
	})
	writePRJSON(t, l.gh, "view.json", c2, `[]`)
	l.pass()
	l.eng.mu.Lock()
	p := l.eng.prs[l.wt]
	p.HeadSince = time.Now().Add(-10 * time.Minute)
	l.eng.prs[l.wt] = p
	l.eng.mu.Unlock()
	l.pass()
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "api repos/acme/demo/actions/workflows/"); len(got) != 1 || got[0] != "api repos/acme/demo/actions/workflows/ci.yml/dispatches -X POST -f ref="+l.branch {
		t.Fatalf("dispatches = %q, want one of ci.yml", got)
	}
	if act := flow.LastAct(l.state().Tasks[0].Attempts[0].Acts, "dispatch_ci@"+c2); act == nil || act.Result != "failed" {
		t.Fatalf("dispatch act = %+v, want failed", act)
	}
	if w := l.state().Project.Wakes; len(w) != 1 || w[0].Key != "no_checks@"+c2 {
		t.Fatalf("wakes = %+v, want 1 no_checks", w)
	}
}

// TestCIWaitsForPendingStatus: a pending commit status beside a red job
// holds the re-run and the tell, like an unfinished check run.
func TestCIWaitsForPendingStatus(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, "["+ciCheck("a", "COMPLETED", "FAILURE", "62", "620")+`,{"__typename":"StatusContext","context":"ci/jenkins","state":"PENDING"}]`)
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "run rerun"); len(got) != 0 || len(sendsTo(t, l.srv.Requests(), l.worker)) != 0 {
		t.Fatalf("acted while a status is pending: reruns %q", got)
	}
}

// TestCIRerunOnlyActionsRunsOfThisRepo: a run id comes only from a GitHub
// Actions check run whose URL is this repository's actions run; an external
// check linking to a run, or an Actions URL of another repository, is told,
// never re-run.
func TestCIRerunOnlyActionsRunsOfThisRepo(t *testing.T) {
	for name, rollup := range map[string]string{
		"external check": `[{"__typename":"CheckRun","name":"ext","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://github.com/acme/demo/actions/runs/999/job/1"}]`,
		"other repo":     `[{"__typename":"CheckRun","name":"a","workflowName":"ci","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://github.com/other/repo/actions/runs/998/job/1"}]`,
	} {
		l := newLanding(t)
		writePRJSON(t, l.gh, "view.json", l.c1, rollup)
		l.pass()
		l.pass()
		if got := ghCalls(t, l.gh, "run rerun"); len(got) != 0 || len(sendsTo(t, l.srv.Requests(), l.worker)) != 1 {
			t.Errorf("%s: reruns %q, sends %d; want none and one tell", name, got, len(sendsTo(t, l.srv.Requests(), l.worker)))
		}
	}
}

// TestCIRunListGatesReruns: the head's workflow runs decide. A failed job
// whose run is still in progress (its other jobs not in the rollup yet)
// waits; a same-repo run id that is not a run of H is never re-run; an
// unreadable run list never re-runs and tells after checks_grace_minutes.
func TestCIRunListGatesReruns(t *testing.T) {
	t.Run("run in progress", func(t *testing.T) {
		l := newLanding(t)
		writePRJSON(t, l.gh, "view.json", l.c1, "["+ciCheck("a", "COMPLETED", "FAILURE", "70", "700")+"]")
		writeGHFile(t, l.gh, "workflow-runs.json", `{"total_count":1,"workflow_runs":[{"id":70,"status":"in_progress","conclusion":null}]}`)
		l.pass()
		l.pass()
		if got := ghCalls(t, l.gh, "run rerun"); len(got) != 0 || len(sendsTo(t, l.srv.Requests(), l.worker)) != 0 {
			t.Fatalf("acted while run 70 runs: reruns %q", got)
		}
	})
	t.Run("stale run id", func(t *testing.T) {
		l := newLanding(t)
		writePRJSON(t, l.gh, "view.json", l.c1, "["+ciCheck("a", "COMPLETED", "FAILURE", "71", "710")+"]")
		writeGHFile(t, l.gh, "workflow-runs.json", `{"total_count":1,"workflow_runs":[{"id":72,"status":"completed","conclusion":"failure"}]}`)
		l.pass()
		l.pass()
		if got := ghCalls(t, l.gh, "run rerun"); len(got) != 0 || len(sendsTo(t, l.srv.Requests(), l.worker)) != 1 {
			t.Fatalf("stale run 71: reruns %q, sends %d; want none and one tell", got, len(sendsTo(t, l.srv.Requests(), l.worker)))
		}
	})
	t.Run("run list unreadable", func(t *testing.T) {
		l := newLanding(t)
		writePRJSON(t, l.gh, "view.json", l.c1, "["+ciCheck("a", "COMPLETED", "FAILURE", "73", "730")+"]")
		writeGHFile(t, l.gh, "workflow_runs_fail", "")
		l.pass()
		l.pass()
		if got := ghCalls(t, l.gh, "run rerun"); len(got) != 0 || len(sendsTo(t, l.srv.Requests(), l.worker)) != 0 {
			t.Fatalf("unreadable run list inside the grace: reruns %q", got)
		}
		l.eng.mu.Lock()
		p := l.eng.prs[l.wt]
		p.HeadSince = time.Now().Add(-10 * time.Minute)
		l.eng.prs[l.wt] = p
		l.eng.mu.Unlock()
		l.pass()
		l.pass()
		if got := ghCalls(t, l.gh, "run rerun"); len(got) != 0 || len(sendsTo(t, l.srv.Requests(), l.worker)) != 1 {
			t.Fatalf("unreadable run list past the grace: reruns %q, sends %d; want none and one tell", got, len(sendsTo(t, l.srv.Requests(), l.worker)))
		}
	})
}

// TestCINoRerunOfARunWithATimeLimit: gh run rerun --failed also re-runs
// dependents, so a run holding a job stopped by its own time limit (here an
// always() job after a failed build) is never re-run; the worker is told.
func TestCINoRerunOfARunWithATimeLimit(t *testing.T) {
	l := newLanding(t)
	writeGHFile(t, l.gh, "annotations.json", `[{"message":"The job has exceeded the maximum execution time of 1m0s"}]`)
	writePRJSON(t, l.gh, "view.json", l.c1, "["+ciCheck("build", "COMPLETED", "FAILURE", "80", "800")+","+ciCheck("report", "COMPLETED", "CANCELLED", "80", "801")+"]")
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "run rerun"); len(got) != 0 || len(sendsTo(t, l.srv.Requests(), l.worker)) != 1 {
		t.Fatalf("run 80 with a timed-out job: reruns %q, sends %d; want none and one tell", got, len(sendsTo(t, l.srv.Requests(), l.worker)))
	}
}
