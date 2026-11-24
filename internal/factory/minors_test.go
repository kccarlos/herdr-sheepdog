package factory

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

func TestJ12KeepsUnlistedWakes(t *testing.T) {
	h := newProjectHarness(t, "Wake batch")
	h.pass()
	addr := h.state().Project.RunID
	h.edit(func(s *flow.State) {
		for i := range 21 {
			s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: fmt.Sprintf("batch-key-%02d", i)})
		}
	})
	h.coordIdleWithStatus(addr, "idle")
	sends := sendsTo(t, h.srv.Requests(), addr)
	held := h.state().Project.Wakes
	logs := logRules(t, h.paths.Log(h.id), "J12")
	if len(sends) != 1 || !strings.Contains(sends[0], "batch-key-19") || strings.Contains(sends[0], "batch-key-20") {
		t.Fatalf("first batch = %q, want keys through batch-key-19 only", sends)
	}
	if len(logs) != 1 {
		t.Fatalf("first batch logged %d J12 deliveries, want one", len(logs))
	}
	if len(held) != 1 || held[0].Key != "batch-key-20" || len(logs[0].Asked) != 20 {
		t.Fatalf("first batch left %d wakes and logged %d delivered keys, want 1 and 20", len(held), len(logs[0].Asked))
	}
	h.coordIdleWithStatus(addr, "idle")
	sends = sendsTo(t, h.srv.Requests(), addr)
	logs = logRules(t, h.paths.Log(h.id), "J12")
	if len(sends) != 2 || !strings.Contains(sends[1], "batch-key-20") || len(h.state().Project.Wakes) != 0 ||
		len(logs) != 2 || len(logs[1].Asked) != 1 || logs[1].Asked[0] != "project:batch-key-20" {
		t.Fatalf("second batch sends=%q, J12 logs=%+v", sends, logs)
	}
}

func TestMergeSeesAcceptOnDiskAtCall(t *testing.T) {
	l := newLanding(t)
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	captured := filepath.Join(dir, "state-at-merge.json")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1 $2" = "pr merge" ]; then
	cp %q %q || exit 1
fi
exec %q "$@"
`, l.paths.State(l.id), captured, gh)
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	l.pass()
	st, err := LoadState(captured)
	if err != nil {
		t.Fatal(err)
	}
	a := st.Tasks[0].Attempts[0]
	if len(a.Accepts) != 1 || a.Accepts[0].Head != l.c1 {
		t.Fatalf("accepts on disk when gh merge ran = %v, want one for the merged head", a.Accepts)
	}
	merge := flow.LastAct(a.Acts, "merge@"+l.c1)
	if merge == nil || merge.Result != "asked" {
		t.Fatalf("merge intent on disk = %+v, want asked", merge)
	}

	// Resume from the captured state, as if the daemon stopped during the call.
	l.edit(func(s *flow.State) { *s = st })
	l.eng = newTestEngine(t, l.paths, l.client)
	l.writeMerged(mergedSHA)
	l.pass()
	if got := l.state().Tasks[0].Done; got == nil || got.MergedCommit != mergedSHA {
		t.Fatalf("after merged snapshot on restart: done=%+v", got)
	}
	n2 := logRules(t, l.paths.Log(l.id), "N2")
	if len(n2) != 1 || len(n2[0].Asked) != 1 || n2[0].Asked[0] != flow.AskMarkDone {
		t.Fatalf("N2 after interrupted merge = %+v, want mark_done with no outside-bar wake", n2)
	}
}

func TestN36PendingSpellKeepsWallTimeAcrossRestart(t *testing.T) {
	l := newLanding(t)
	now := time.Now()
	l.eng.Now = func() time.Time { return now }
	setIdle(t, l.srv, l.client, l.worker, "working")
	l.pass() // Green first observation of the head, held by the working worker.
	deadline := time.Duration(l.state().Settings.CIStalledMinutes) * time.Minute
	now = now.Add(2 * deadline)
	writePRJSON(t, l.gh, "view.json", l.c1, `[{"__typename":"CheckRun","name":"ci","status":"IN_PROGRESS"}]`)
	s := l.state()
	l.eng.readPRLocked(t.Context(), l.id, &s, 0)
	spell := s.Tasks[0].Attempts[0].PendingChecks
	if spell == nil || spell.Head != l.c1 || spell.Since != now.Round(0) {
		t.Fatalf("pending spell = %+v, want head %s at wall time %v", spell, l.c1, now.Round(0))
	}
	l.pass()
	wakeKey := flow.AskWake + "@ci_stalled@" + l.c1
	if flow.Done(l.state().Tasks[0].Attempts[0].Acts, wakeKey) {
		t.Fatal("N36 woke on the first pending observation of an old head")
	}
	l.eng = newTestEngine(t, l.paths, l.client)
	l.eng.Now = func() time.Time { return now }
	now = now.Add(deadline - time.Second)
	l.pass()
	if flow.Done(l.state().Tasks[0].Attempts[0].Acts, wakeKey) {
		t.Fatal("N36 woke before the pending deadline")
	}
	now = now.Add(time.Second)
	l.pass()
	if !flow.Done(l.state().Tasks[0].Attempts[0].Acts, wakeKey) {
		t.Fatal("N36 forgot the pending deadline across restart")
	}
}

func TestTaskHoldLogsReasonOnceAndShowsIt(t *testing.T) {
	l := newLanding(t)
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].PR = 0 })
	writeGHFile(t, l.gh, "fail", "")
	l.pass()
	l.eng = newTestEngine(t, l.paths, l.client)
	l.pass()
	logs := logRules(t, l.paths.Log(l.id), "N26")
	if len(logs) != 1 || logs[0].Decision != "waiting" || !strings.Contains(logs[0].Why, "GitHub did not answer") {
		t.Fatalf("N26 waiting logs = %+v, want one unanswered snapshot reason", logs)
	}
	st := l.state()
	st.Tasks[0].Attempts[0].Review = &flow.ReviewRun{Round: 1, Run: &flow.RunRecord{State: flow.PhaseWorking}}
	if got := StatusViewOf(st, l.logLines())[0].Why; !strings.Contains(got, "GitHub did not answer") {
		t.Fatalf("N26 status why = %q, want unanswered snapshot reason", got)
	}
}

func TestWorkerRestartBackoffLogsWhy(t *testing.T) {
	l := newLanding(t)
	now := time.Now()
	l.eng.Now = func() time.Time { return now }
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Ready = nil
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now}}
	})
	l.srv.Finish(l.worker, "exited")
	l.pass()
	l.pass()
	logs := logRules(t, l.paths.Log(l.id), "N23")
	if len(logs) != 1 || logs[0].Decision != "waiting" || !strings.Contains(logs[0].Why, "back-off") {
		t.Fatalf("N23 waiting logs = %+v, want one worker restart back-off reason", logs)
	}
	if got := StatusViewOf(l.state(), l.logLines())[0].Why; !strings.Contains(got, "back-off") {
		t.Fatalf("N23 status why = %q, want worker restart back-off", got)
	}
}
