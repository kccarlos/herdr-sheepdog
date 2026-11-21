package factory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func argValue(argv []string, flag string) string {
	if i := slices.Index(argv, flag); i >= 0 && i+1 < len(argv) {
		return argv[i+1]
	}
	return ""
}

// TestWorkerKilledMidTurnResumedAndTaskFinishes verifies PR 5a.2's core demo:
// a Claude worker starts with an engine-chosen --session-id, is killed mid-turn,
// is resumed after backoff with --resume <same uuid> and explicit --model +
// --effort, commits and says ready, a killed reviewer is retried, and the task
// merges and finishes.
func TestWorkerKilledMidTurnResumedAndTaskFinishes(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	var runs []hc.RunParams
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { runs = append(runs, p) }
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := installFakeGH(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	eng := New(paths, client)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Demo alive", 2); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	eng.passProject(ctx, id, false) // J7 coordinator
	stInit, _ := LoadState(paths.State(id))
	srv.SetStatus(stInit.Project.RunID, &hc.Status{App: "claude-code", State: "working"})
	coordTok := runs[0].Env["SHEEPDOG_RUN_TOKEN"]
	if err := eng.Report(coordTok, "plan apply", []byte(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: resilient hello","brief":"Add hello.go.",`+testScopeJSON+`,"outcomes":[{"text":"hello exists","proved_by":"manual: hello.go"}]}]}`)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N11 worktree
	eng.passProject(ctx, id, false) // N23 fresh worker

	wRun1 := runs[len(runs)-1]
	sid := argValue(wRun1.Argv, "--session-id")
	if len(sid) != 36 || argValue(wRun1.Argv, "--model") != "sonnet" || argValue(wRun1.Argv, "--effort") != "medium" {
		t.Fatalf("fresh worker argv = %q", wRun1.Argv)
	}
	st, _ := LoadState(paths.State(id))
	w1 := st.Tasks[0].Attempts[0].RunID
	srv.SetStatus(w1, &hc.Status{App: "claude-code", State: "working"})
	now = now.Add(5 * time.Second)
	eng.passProject(ctx, id, false)

	// Kill worker mid-turn; before backoff passes it waits, after 1m it resumes same session.
	if _, err := client.Kill(ctx, w1); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	eng.passProject(ctx, id, false) // R33 -> ended(exited), waiting on 1m backoff
	now = now.Add(59 * time.Second)
	eng.passProject(ctx, id, false)
	if st, _ = LoadState(paths.State(id)); st.Tasks[0].Attempts[0].RunID != "" {
		t.Fatalf("restarted before backoff elapsed: %+v", st.Tasks[0].Attempts[0])
	}
	now = now.Add(time.Second)
	eng.passProject(ctx, id, false) // N23 resumes worker

	wRun2 := runs[len(runs)-1]
	if argValue(wRun2.Argv, "--resume") != sid || argValue(wRun2.Argv, "--session-id") != "" ||
		argValue(wRun2.Argv, "--model") != "sonnet" || argValue(wRun2.Argv, "--effort") != "medium" {
		t.Fatalf("resumed worker argv = %q, want --resume %s with --model sonnet --effort medium", wRun2.Argv, sid)
	}
	st, _ = LoadState(paths.State(id))
	if a := st.Tasks[0].Attempts[0]; a.Restarts != 1 || a.RunID == "" || !a.Run.Resumed {
		t.Fatalf("after resume: %+v", a)
	}
	w2, w2Tok := st.Tasks[0].Attempts[0].RunID, wRun2.Env["SHEEPDOG_RUN_TOKEN"]
	srv.SetStatus(w2, &hc.Status{App: "claude-code", State: "idle"})
	eng.passProject(ctx, id, false)
	now = now.Add(25 * time.Second)
	eng.passProject(ctx, id, false) // R20 -> idle

	// Resumed worker commits c1 and reports ready -> N26 pushes -> N29 starts round 1 reviewer.
	wt := st.Tasks[0].Attempts[0].Worktree
	if err := os.WriteFile(filepath.Join(wt, "hello.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "hello.go")
	gitIn(t, wt, "commit", "-m", "feat: add hello")
	c1 := gitIn(t, wt, "rev-parse", "HEAD")
	writePRJSON(t, gh, "pr.json", c1, greenRollup)
	if err := eng.Report(w2Tok, "ready", []byte(`{"head":"`+c1+`","proofs":[{"outcome":"hello exists","artifact":"hello.go"}],"touched":"hello.go","skipped":""}`)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N26 pushes and records PR #7
	writePRJSON(t, gh, "view.json", c1, greenRollup)
	eng.passProject(ctx, id, false) // N29 round 1 reviewer

	// Kill round 1 reviewer mid-review -> retried automatically and passes both rounds -> merges and finishes!
	st, _ = LoadState(paths.State(id))
	if _, err := client.Kill(ctx, st.Tasks[0].Attempts[0].Review.RunID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	eng.passProject(ctx, id, false) // R33 on reviewer -> N29 retries round 1
	st, _ = LoadState(paths.State(id))
	rev1 := st.Tasks[0].Attempts[0].Review
	if rev1 == nil || rev1.Retries != 1 || rev1.RunID == "" {
		t.Fatalf("retried round 1 review = %+v", rev1)
	}
	passRound := func(round int) {
		st, _ = LoadState(paths.State(id))
		rv := st.Tasks[0].Attempts[0].Review
		srv.SetScreen(rv.RunID, "❯ prompt\n\n⏺ Looks good.\nself-check misses: 0\ndeletable: none\n<report>{\"code\":\""+rv.Code+"\",\"round\":"+strings.TrimSpace(jsonNumber(round))+",\"head\":\""+c1+"\",\"verdict\":\"pass\",\"majors\":0}</report>\n")
		srv.SetStatus(rv.RunID, &hc.Status{App: "claude-code", State: "done"})
		eng.passProject(ctx, id, false)
		now = now.Add(25 * time.Second)
		eng.passProject(ctx, id, false)
	}
	passRound(1)
	eng.passProject(ctx, id, false) // N29 starts round 2
	passRound(2)
	eng.passProject(ctx, id, false) // N42 asks merge
	mergedJSON := `[{"number":7,"state":"MERGED","headRefOid":"` + c1 + `","baseRefName":"main","isCrossRepository":false,"mergeCommit":{"oid":"merged99"},"mergeStateStatus":"CLEAN","reviewDecision":"","statusCheckRollup":` + greenRollup + `}]`
	writeGHFile(t, gh, "pr.json", mergedJSON)
	writeGHFile(t, gh, "view.json", mergedJSON[1:len(mergedJSON)-1])
	eng.passProject(ctx, id, false) // N2 marks done
	eng.passProject(ctx, id, false) // N1 cleans up
	st, _ = LoadState(paths.State(id))
	if st.Tasks[0].Done == nil || st.Tasks[0].Done.MergedCommit != "merged99" {
		t.Fatalf("task did not finish merged: %+v", st.Tasks[0])
	}
}

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestResumeFailedFallsBackToHandoffNote verifies R13 + 4.8 handoff: when a
// resumed worker exits on its own within start_minutes, R13 marks the session
// not resumable and the next restart launches a fresh --session-id with a
// handoff note naming the brief path, rounds so far, and last findings.
func TestResumeFailedFallsBackToHandoffNote(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	var runs []hc.RunParams
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { runs = append(runs, p) }
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	now := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)

	eng := New(paths, client)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Handoff goal", 2); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	eng.passProject(ctx, id, false) // coordinator
	if err := eng.Report(runs[0].Env["SHEEPDOG_RUN_TOKEN"], "plan apply", []byte(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: handoff","brief":"Write widget.go.",`+testPlanJSON+`}]}`)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N11
	eng.passProject(ctx, id, false) // N23 fresh worker
	sid1 := argValue(runs[len(runs)-1].Argv, "--session-id")

	st, _ := LoadState(paths.State(id))
	repPath := paths.Report(id, "T1", 1)
	st.Tasks[0].Attempts[0].Rounds = []flow.Round{{N: 1, Kind: flow.RoundFull, Head: st.Tasks[0].Attempts[0].BaseCommit, Verdict: flow.VerdictChanges, Majors: 2, ReportPath: repPath}}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	// First crash -> resume after 1m.
	srv.Finish(st.Tasks[0].Attempts[0].RunID, "killed")
	now = now.Add(5 * time.Second)
	eng.passProject(ctx, id, false)
	now = now.Add(time.Minute)
	eng.passProject(ctx, id, false) // restart 1: resume
	st, _ = LoadState(paths.State(id))
	if argValue(runs[len(runs)-1].Argv, "--resume") != sid1 {
		t.Fatalf("restart 1 did not resume %s: %q", sid1, runs[len(runs)-1].Argv)
	}

	// Resumed worker exits within start_minutes (not killed) -> R13 ended(resume_failed), Resumable=false.
	srv.Finish(st.Tasks[0].Attempts[0].RunID, "exited")
	now = now.Add(10 * time.Second)
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if r := st.Tasks[0].Attempts[0].Run; r.Result != flow.EndResumeFailed || r.Resumable {
		t.Fatalf("after broken resume: %+v, want resume_failed with Resumable=false", r)
	}

	// After 2m backoff (2^1 * 1m): restart 2 starts fresh --session-id with handoff note.
	now = now.Add(2 * time.Minute)
	eng.passProject(ctx, id, false)
	last := runs[len(runs)-1]
	sid2 := argValue(last.Argv, "--session-id")
	prompt := last.Argv[len(last.Argv)-1]
	if sid2 == "" || sid2 == sid1 || argValue(last.Argv, "--resume") != "" {
		t.Fatalf("handoff start argv = %q (sid1=%s)", last.Argv, sid1)
	}
	for _, want := range []string{"Handoff from an earlier worker run:", paths.Brief(id, "T1"), "Rounds so far: 1 (r1 changes (full, 2 major(s)))", "Last findings: " + repPath} {
		if !strings.Contains(prompt, want) {
			t.Errorf("handoff prompt missing %q:\n%s", want, prompt)
		}
	}
	if b, err := os.ReadFile(paths.Brief(id, "T1")); err != nil || !strings.Contains(string(b), "Write widget.go.") {
		t.Fatalf("brief file = %q, %v", b, err)
	}
}

// TestWorkerRestartsBackoffSlotsAndCoordinatorWake checks doubling backoff
// (1m, 2m, 4m), the N10 slot rule on restarts, stopping at worker_restarts,
// and waking the coordinator once.
func TestWorkerRestartsBackoffSlotsAndCoordinatorWake(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	var runs []hc.RunParams
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { runs = append(runs, p) }
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	eng := New(paths, client)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Backoff goal", 1); err != nil { // max_running = 1
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	eng.passProject(ctx, id, false)
	stCoord, _ := LoadState(paths.State(id))
	srv.SetStatus(stCoord.Project.RunID, &hc.Status{App: "claude-code", State: "working"})
	coord, coordTok := runs[0], runs[0].Env["SHEEPDOG_RUN_TOKEN"]
	plan := `{"tasks":[
		{"id":"T1","estimate_lines":50,"title":"feat: first",` + testPlanJSON + `},
		{"id":"T2","estimate_lines":50,"title":"feat: second",` + testPlanJSON + `}
	]}`
	if err := eng.Report(coordTok, "plan apply", []byte(plan)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N11 T1 (T2 waits at N10)
	eng.passProject(ctx, id, false) // N23 starts T1 worker

	// T1 worker dies -> frees slot -> T2 takes the single slot!
	st, _ := LoadState(paths.State(id))
	srv.Finish(st.Tasks[0].Attempts[0].RunID, "killed")
	now = now.Add(5 * time.Second)
	eng.passProject(ctx, id, false) // T1 R33; T2 N11 takes slot
	eng.passProject(ctx, id, false) // T2 N23 starts worker

	// Past T1's 1m backoff while T2 holds the only slot: T1 cannot restart yet (N10 slot rule).
	now = now.Add(time.Minute)
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if st.Tasks[0].Attempts[0].RunID != "" || st.Tasks[0].Attempts[0].Restarts != 0 {
		t.Fatalf("T1 restarted without a free slot: %+v", st.Tasks[0].Attempts[0])
	}

	// Drop T2 to free the slot -> T1 restarts (restart 1), then test 2m and 4m backoffs and final wake.
	if err := eng.Report(coordTok, "task", []byte(`{"action":"drop","task":"T2","why":"free slot"}`)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // T2 N1 frees slot next pass
	eng.passProject(ctx, id, false) // T1 restart 1
	for wantRestart, wait := range []time.Duration{1: 2 * time.Minute, 2: 4 * time.Minute} {
		if wantRestart == 0 {
			continue
		}
		st, _ = LoadState(paths.State(id))
		if st.Tasks[0].Attempts[0].Restarts != wantRestart {
			t.Fatalf("restarts = %d, want %d", st.Tasks[0].Attempts[0].Restarts, wantRestart)
		}
		srv.Finish(st.Tasks[0].Attempts[0].RunID, "killed")
		now = now.Add(5 * time.Second)
		eng.passProject(ctx, id, false)
		now = now.Add(wait - time.Second)
		eng.passProject(ctx, id, false)
		if st, _ = LoadState(paths.State(id)); st.Tasks[0].Attempts[0].RunID != "" {
			t.Fatalf("restarted before %v backoff", wait)
		}
		now = now.Add(time.Second)
		eng.passProject(ctx, id, false)
	}
	// Restart 3 now running; kill it -> Restarts (3) == WorkerRestarts (3) -> wakes coordinator once!
	st, _ = LoadState(paths.State(id))
	if st.Tasks[0].Attempts[0].Restarts != 3 || st.Tasks[0].Attempts[0].RunID == "" {
		t.Fatalf("expected restart 3 running, got %+v", st.Tasks[0].Attempts[0])
	}
	srv.Finish(st.Tasks[0].Attempts[0].RunID, "killed")
	now = now.Add(5 * time.Second)
	st, _ = LoadState(paths.State(id))
	coordID := st.Project.RunID
	srv.SetStatus(coordID, &hc.Status{App: "claude-code", State: "idle"})
	eng.passProject(ctx, id, false)
	now = now.Add(25 * time.Second)
	for range 2 {
		eng.passProject(ctx, id, false)
	}
	_ = coord
	sends := sendsTo(t, srv.Requests(), coordID)
	if len(sends) != 1 || !strings.Contains(sends[0], "worker_failed") {
		t.Fatalf("coordinator wakes = %q, want 1 worker_failed wake", sends)
	}
}

// TestHeldMessagesLifecycle checks R24-R26, R18, R33, R36 and start delivery:
//  1. A message to a working run is held on Run.Held (R26) and delivered at idle (R18).
//  2. An urgent message to a working run interrupts then sends (R25).
//  3. A message to an ended run is refused run_ended (R36), goes back to the task,
//     and is delivered first when 4.8 restarts the worker.
func TestHeldMessagesLifecycle(t *testing.T) {
	l := newLanding(t).withTokens()
	now := time.Now()
	l.eng.Now = func() time.Time { return now }

	// Move worker into PhaseWorking.
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
	l.pass()

	// 1. Send-back while worker is working -> held on Run.Held (R26), not sent yet; sent at idle (R18).
	l.report("tok-coord", "task", `{"action":"send-back","task":"T1","why":"hold until idle"}`, "")
	l.pass()
	if got := sendsTo(t, l.srv.Requests(), l.worker); len(got) != 0 {
		t.Fatalf("sent to working worker before idle: %q", got)
	}
	if held := l.state().Tasks[0].Attempts[0].Run.Held; len(held) != 1 || !strings.Contains(held[0], "hold until idle") {
		t.Fatalf("Run.Held = %q, want 1 held send-back", held)
	}
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "idle"})
	now = now.Add(5 * time.Second)
	l.pass() // R17 records StatusSince
	now = now.Add(25 * time.Second)
	l.pass() // R18 sends held message!
	if got := sendsTo(t, l.srv.Requests(), l.worker); len(got) != 1 || !strings.Contains(got[0], "hold until idle") {
		t.Fatalf("after idle grace sends = %q, want 1 held message delivered", got)
	}

	// 2. Urgent message to working worker (N12 -> N6 size recap tell_worker_stop) -> R25 interrupts then sends.
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
	now = now.Add(5 * time.Second)
	l.pass()
	reqsBefore := len(l.srv.Requests())
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].SizeCap = 0 }) // 1 changed line > cap 0 -> N12 then N6
	l.pass()                                                           // N12 opens size recap
	l.pass()                                                           // N6 urgent tell_worker_stop -> R25
	var methods []string
	for _, r := range l.srv.Requests()[reqsBefore:] {
		if (r.Method == "terminal.interrupt" || r.Method == "terminal.send") && strings.Contains(string(r.Params), l.worker) {
			methods = append(methods, r.Method)
		}
	}
	if !slices.Equal(methods, []string{"terminal.interrupt", "terminal.send"}) {
		t.Fatalf("urgent R25 methods = %v, want [terminal.interrupt terminal.send]", methods)
	}

	// 3. Close recap via continue, kill worker, then add round with findings -> R36 refuses run_ended,
	//    holds findings on task Attempt.Held, and delivers them first on worker restart!
	var launched []hc.RunParams
	l.srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { launched = append(launched, p) }
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].SizeCap = 100
		s.Tasks[0].Recaps[0].Decided = &flow.RecapDecision{Choice: flow.ChoiceContinue, Why: "ok", At: now}
		s.Tasks[0].Attempts[0].Run.SessionID = "11111111-2222-4333-8444-555555555555"
		s.Tasks[0].Attempts[0].Run.Resumable = true
	})
	l.srv.Finish(l.worker, "killed")
	now = now.Add(5 * time.Second)
	l.pass() // R33 -> worker ended
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Rounds = append(a.Rounds, flow.Round{N: len(a.Rounds) + 1, Kind: flow.RoundFull, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1, ReportPath: "/r/dead-worker.md"})
	})
	l.pass() // N22 tell_worker_findings -> R36 run_ended -> Attempt.Held
	if r36 := logRules(t, l.paths.Log(l.id), "R36"); len(r36) != 1 || r36[0].Decision != flow.RefuseRunEnded {
		t.Fatalf("R36 log = %+v, want 1 run_ended", r36)
	}
	if held := l.state().Tasks[0].Attempts[0].Held; len(held) != 1 || !strings.Contains(held[0], "/r/dead-worker.md") {
		t.Fatalf("Attempt.Held after R36 = %q, want findings", held)
	}
	now = now.Add(time.Minute)
	l.pass() // 4.8 resumes worker and delivers Attempt.Held as prompt!
	if len(launched) != 1 || !strings.Contains(launched[0].Argv[len(launched[0].Argv)-1], "/r/dead-worker.md") {
		t.Fatalf("restarted worker did not deliver held findings first: %+v", launched)
	}
	if len(l.state().Tasks[0].Attempts[0].Held) != 0 {
		t.Fatalf("Attempt.Held not cleared on start: %q", l.state().Tasks[0].Attempts[0].Held)
	}
}

// TestParkUnparkAndAmendRequestRestartUncounted verifies N3 park/unpark resumes
// the same Claude session without counting toward worker_restarts or waiting on
// backoff, and plan amend changing request.worker restarts with a handoff
// without counting.
func TestParkUnparkAndAmendRequestRestartUncounted(t *testing.T) {
	origProbe := planResolveFacts
	planResolveFacts = func(context.Context, []string) flow.ResolveFacts {
		return flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{"claude": {Installed: true, SignedIn: true}, "codex": {Installed: true, SignedIn: true}}}
	}
	t.Cleanup(func() { planResolveFacts = origProbe })

	ctx := context.Background()
	srv := hctest.New(t)
	var runs []hc.RunParams
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { runs = append(runs, p) }
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	now := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)

	eng := New(paths, client)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Park goal", 2); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	eng.passProject(ctx, id, false) // coordinator
	coordTok := runs[0].Env["SHEEPDOG_RUN_TOKEN"]
	if err := eng.Report(coordTok, "plan apply", []byte(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: park","brief":"Parkable.",`+testPlanJSON+`}]}`)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N11
	eng.passProject(ctx, id, false) // N23 starts worker
	sid1 := argValue(runs[len(runs)-1].Argv, "--session-id")

	// Park -> N3 stops worker (EndStopped); immediate unpark (0s elapsed) resumes same session, Restarts=0.
	if err := eng.Report(coordTok, "task", []byte(`{"action":"park","task":"T1","why":"hold"}`)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N3 stops worker
	if st, _ := LoadState(paths.State(id)); st.Tasks[0].Attempts[0].RunID != "" || st.Tasks[0].Attempts[0].Run.Result != flow.EndStopped {
		t.Fatalf("after park = %+v", st.Tasks[0].Attempts[0])
	}
	if err := eng.Report(coordTok, "task", []byte(`{"action":"unpark","task":"T1","why":"go"}`)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // immediate unpark -> resumes without backoff or counting!
	st, _ := LoadState(paths.State(id))
	if st.Tasks[0].Attempts[0].Restarts != 0 || argValue(runs[len(runs)-1].Argv, "--resume") != sid1 {
		t.Fatalf("after unpark restarts=%d argv=%q, want 0 and --resume %s", st.Tasks[0].Attempts[0].Restarts, runs[len(runs)-1].Argv, sid1)
	}

	// Amend request.worker to opus/high (with codex reviewer so reviewer != worker) -> restarts uncounted with handoff!
	amend := `{"why":"stronger model","tasks":[{"id":"T1","request":{"worker":[{"cli":"claude","model":"opus","effort":"high"}],"reviewer":[{"cli":"codex","model":"gpt-6.2-sol","effort":"medium"}]}}]}`
	if err := eng.Report(coordTok, "plan amend", []byte(amend)); err != nil {
		t.Fatalf("plan amend request: %v", err)
	}
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	last := runs[len(runs)-1]
	if st.Tasks[0].Attempts[0].Restarts != 0 || argValue(last.Argv, "--model") != "opus" || argValue(last.Argv, "--effort") != "high" ||
		argValue(last.Argv, "--session-id") == "" || !strings.Contains(last.Argv[len(last.Argv)-1], "Handoff from an earlier worker run:") {
		t.Fatalf("after request amend restarts=%d argv=%q", st.Tasks[0].Attempts[0].Restarts, last.Argv)
	}
}

// TestReviewerNoReportRetriesCandidateRotationThenFails verifies that a
// reviewer ending with no valid report is started again with the next candidate
// while tries < reviewer_retries, and then counts as a failure waking once.
func TestReviewerNoReportRetriesCandidateRotationThenFails(t *testing.T) {
	ctx := context.Background()
	codexHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	srv := hctest.New(t)
	var runs []hc.RunParams
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { runs = append(runs, p) }
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := installFakeGH(t)
	eng := New(paths, client)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	if err := eng.Start(repo, "Reviewer retries", 2); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	eng.passProject(ctx, id, false) // coordinator
	st, _ := LoadState(paths.State(id))
	st.Settings.SecondReviewer = "codex" // candidates: [opus, gpt-6.2-sol]
	st.Tasks = []flow.Task{{ID: "T1", Title: "feat: rev", EstimateLines: 50}}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N11
	eng.passProject(ctx, id, false) // N23
	st, _ = LoadState(paths.State(id))
	wt := st.Tasks[0].Attempts[0].Worktree
	if err := os.WriteFile(filepath.Join(wt, "r.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "r.go")
	gitIn(t, wt, "commit", "-m", "feat: r")
	c1 := gitIn(t, wt, "rev-parse", "HEAD")
	writePRJSON(t, gh, "pr.json", c1, "[]")
	st.Tasks[0].Attempts[0].Ready = []flow.Ready{{Head: c1}}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N26
	eng.passProject(ctx, id, false) // N29 try 0 -> opus

	// Try 0 (opus), retry 1 (codex), retry 2 (opus fresh), then failure wake!
	wantModels := []string{"opus", codexModel, "opus"}
	for i, wantModel := range wantModels {
		st, _ = LoadState(paths.State(id))
		rev := st.Tasks[0].Attempts[0].Review
		if rev == nil || rev.Retries != i || rev.Reviewer.Model != wantModel || rev.RunID == "" {
			t.Fatalf("try %d review = %+v, want Retries=%d Model=%s", i, rev, i, wantModel)
		}
		srv.Finish(rev.RunID, "exited")
		eng.passProject(ctx, id, false)
	}
	// Past reviewer_retries (2): no 4th reviewer starts, wakes coordinator once.
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if st.Tasks[0].Attempts[0].Review.RunID != "" || len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Key != "reviewer_failed@1@"+c1 {
		t.Fatalf("past reviewer_retries: review=%+v wakes=%+v", st.Tasks[0].Attempts[0].Review, st.Project.Wakes)
	}
}

func methodFailProxy(t *testing.T, target, method string, fail *atomic.Bool) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					return
				}
				var req struct {
					ID     uint64 `json:"id"`
					Method string `json:"method"`
				}
				_ = json.Unmarshal(line, &req)
				if req.Method == method && fail.Load() {
					_, _ = fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"%s failed"}}`+"\n", req.ID, method)
					return
				}
				up, err := net.Dial("unix", target)
				if err != nil {
					return
				}
				defer func() { _ = up.Close() }()
				if _, err := up.Write(line); err == nil {
					_, _ = io.Copy(c, up)
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// TestParkDuringReviewUnparkRestartsReviewer (from Opus probe): parking mid-review
// records EndStopped on the reviewer run (never deleting Review), and unparking
// starts the reviewer again for the same round and commit without counting.
func TestParkDuringReviewUnparkRestartsReviewer(t *testing.T) {
	l := newLanding(t).withTokens()
	ctx := context.Background()
	rev, err := startRun(ctx, l.client, world.RunSpec{Name: "rev-t1", Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(rev, &hc.Status{App: "claude-code", State: "working"})
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Rounds = nil
		a.Review = &flow.ReviewRun{RunID: rev, Round: 1, Head: l.c1, Code: "c", Reviewer: flow.Reviewer{CLI: "claude", Model: "opus"}}
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done"}, {Key: "push_and_open_pr@" + l.c1, Result: "done"}, {Key: "start_reviewer@1@" + l.c1, Result: "done"}}
	})
	l.pass()
	l.report("tok-coord", "task", `{"action":"park","task":"T1","why":"x"}`, "")
	l.pass()
	l.report("tok-coord", "task", `{"action":"unpark","task":"T1","why":"y"}`, "")
	for range 4 {
		l.pass()
	}
	s := l.state()
	a := s.Tasks[0].Attempts[0]
	if a.Review == nil || a.Review.RunID == "" || a.Review.Round != 1 || a.Review.Head != l.c1 || a.Review.Retries != 0 {
		t.Fatalf("no uncounted reviewer after unpark (row=%s review=%+v acts=%+v)", s.Tasks[0].LastRow, a.Review, a.Acts)
	}
}

// TestAmendBriefDuringReviewRestartsReviewer (from Opus probe): plan amend of
// brief or outcomes during a review round records EndStopped on the reviewer and
// starts a fresh reviewer for the round when the worker changes nothing.
func TestAmendBriefDuringReviewRestartsReviewer(t *testing.T) {
	l := newLanding(t).withTokens()
	ctx := context.Background()
	rev, err := startRun(ctx, l.client, world.RunSpec{Name: "rev-t1", Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(rev, &hc.Status{App: "claude-code", State: "working"})
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Rounds = nil
		a.Review = &flow.ReviewRun{RunID: rev, Round: 1, Head: l.c1, Code: "c", Reviewer: flow.Reviewer{CLI: "claude", Model: "opus"}}
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done"}, {Key: "push_and_open_pr@" + l.c1, Result: "done"}, {Key: "start_reviewer@1@" + l.c1, Result: "done"}}
	})
	l.pass()
	l.report("tok-coord", "plan amend", `{"why":"clarify","tasks":[{"id":"T1","brief":"updated brief"}]}`, "")
	for range 3 {
		l.pass()
	}
	s := l.state()
	a := s.Tasks[0].Attempts[0]
	if a.Review == nil || a.Review.RunID == "" || a.Review.Retries != 0 {
		t.Fatalf("no reviewer after brief amend (row=%s review=%+v)", s.Tasks[0].LastRow, a.Review)
	}
}

// TestRecapAndMovedTipVoidRecordEngineStop verifies that both a recap voiding an
// in-flight round and a clean moved-tip void record EndStopped (never nil) so
// once the round is wanted again at H, the reviewer restarts uncounted.
func TestRecapAndMovedTipVoidRecordEngineStop(t *testing.T) {
	l := newLanding(t).withTokens()
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Rounds = nil })
	l.pass() // N29 starts round 1 reviewer at c1
	rev1 := l.state().Tasks[0].Attempts[0].Review
	if rev1 == nil || rev1.RunID == "" {
		t.Fatalf("expected round 1 reviewer running, got %+v", rev1)
	}

	// 1. Open size recap -> voids reviewer as EndStopped; continue -> restarts round 1 at c1 uncounted.
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].SizeCap = 0 })
	l.pass() // N12 opens size recap
	l.pass() // observeReviewLocked voids round 1 as EndStopped
	if rv := l.state().Tasks[0].Attempts[0].Review; rv == nil || rv.Run == nil || rv.Run.Result != flow.EndStopped {
		t.Fatalf("after recap void: review = %+v, want EndStopped", rv)
	}
	l.report("tok-coord", "recap decide", `{"task":"T1","choice":"continue","why":"allow lines"}`, "")
	l.pass() // N29 restarts reviewer for round 1 at c1
	rev2 := l.state().Tasks[0].Attempts[0].Review
	if rev2 == nil || rev2.RunID == "" || rev2.RunID == rev1.RunID || rev2.Retries != 0 {
		t.Fatalf("after recap continue: review = %+v, want restarted round 1 with Retries=0", rev2)
	}

	// 2. Worker commits c2 mid-review (tip moves off c1) -> voids reviewer as EndStopped;
	// worker resets back to c1 -> N29 restarts round 1 at c1 uncounted.
	commitFile(t, l.wt, "tmp.go", "package demo\n")
	l.pass() // tip ahead -> voids round 1 as EndStopped
	if rv := l.state().Tasks[0].Attempts[0].Review; rv == nil || rv.Run == nil || rv.Run.Result != flow.EndStopped {
		t.Fatalf("after tip-moved void: review = %+v, want EndStopped", rv)
	}
	gitIn(t, l.wt, "reset", "--hard", l.c1)
	l.pass()
	rev3 := l.state().Tasks[0].Attempts[0].Review
	if rev3 == nil || rev3.RunID == "" || rev3.Retries != 0 {
		t.Fatalf("after reset back to c1: review = %+v, want restarted round 1 with Retries=0", rev3)
	}
}

// TestOwnerStopStartRestartsWorker (from Opus probe): owner Stop records
// EndStopped on open runs (never clearRuns), and owner Start restarts the worker.
func TestOwnerStopStartRestartsWorker(t *testing.T) {
	l := newLanding(t).withTokens()
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Ready, a.Rounds = nil, nil
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done"}}
	})
	l.pass()
	if err := l.eng.Stop(l.repo); err != nil {
		t.Fatal(err)
	}
	if err := l.eng.Start(l.repo, "Goal", 0); err != nil {
		t.Fatal(err)
	}
	l.edit(func(s *flow.State) { s.Project.RunID = "coord-elsewhere" })
	for range 3 {
		l.pass()
	}
	s := l.state()
	a := s.Tasks[0].Attempts[0]
	if a.RunID == "" {
		t.Fatalf("worker not restarted after owner stop+start (silent wait at %s, rec=%+v)", s.Tasks[0].LastRow, a.Run)
	}
}

// TestActBackoffDoublingOutageN5AndRetryE2E verifies PR 5a.3a end to end:
//  1. A failure while github_unavailable is known is marked Uncounted and not counted.
//  2. Transient failures retry after back-off (1m, then doubled 2m) and not before.
//  3. ensure_failures (3) in a row lands on N5 (never S2), waking the coordinator
//     once with what failed and its cleaned last words, and never re-fires.
//  4. Recording a retry act clears N5 so the key is asked again.
//  5. One settled failure lands on N5 at once.
func TestActBackoffDoublingOutageN5AndRetryE2E(t *testing.T) {
	l := newLanding(t)
	now := time.Now()
	l.eng.Now = func() time.Time { return now }
	var failSend atomic.Bool
	l.eng.HC = hc.NewClient(methodFailProxy(t, l.srv.Path, "terminal.send", &failSend))
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Rounds = []flow.Round{{N: 1, Kind: flow.RoundFull, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1, ReportPath: "/r/1.md"}}
	})

	// 1. Outage: fail gh (github_unavailable) while terminal.send fails -> Uncounted, never reaches N5 even after 3 tries.
	failSend.Store(true)
	writeGHFile(t, l.gh, "fail", "")
	for range 3 {
		l.pass()
		now = now.Add(time.Minute)
	}
	_ = os.Remove(filepath.Join(l.gh, "fail"))
	if s := l.state(); s.Tasks[0].LastRow == "N5" || s.Tasks[0].LastRow == "S2" || len(s.Project.Wakes) != 0 {
		t.Fatalf("outage failures counted toward N5/S2: row=%s wakes=%+v acts=%+v", s.Tasks[0].LastRow, s.Project.Wakes, s.Tasks[0].Attempts[0].Acts)
	}

	// 2. GitHub back: 3 counted transient failures with 1m then 2m doubling back-off -> N5 wakes once.
	l.pass() // counted failure 1 at now
	now = now.Add(59 * time.Second)
	l.pass() // before 1m back-off: must not retry
	if got := flow.Count(l.state().Tasks[0].Attempts[0].Acts, "tell_worker_findings@1"); got != 4 {
		t.Fatalf("retried before 1m backoff: count=%d, want 4 (3 uncounted + 1 counted)", got)
	}
	now = now.Add(time.Second)
	l.pass() // at 1m: counted failure 2
	now = now.Add(2*time.Minute - time.Second)
	l.pass() // before 2m doubled back-off: must not retry
	if got := flow.Count(l.state().Tasks[0].Attempts[0].Acts, "tell_worker_findings@1"); got != 5 {
		t.Fatalf("retried before 2m doubled backoff: count=%d, want 5", got)
	}
	now = now.Add(time.Second)
	l.pass() // at 2m: counted failure 3 (hits ensure_failures=3)
	l.pass() // lands on N5 and wakes once
	now = now.Add(40 * time.Minute)
	l.pass() // past stuck_minutes: stays N5, never S2, never re-wakes or re-fires
	s := l.state()
	if s.Tasks[0].LastRow != "N5" || flow.Count(s.Tasks[0].Attempts[0].Acts, "tell_worker_findings@1") != 6 {
		t.Fatalf("after 3 counted failures: row=%s acts=%+v", s.Tasks[0].LastRow, s.Tasks[0].Attempts[0].Acts)
	}
	if len(s.Project.Wakes) != 1 || s.Project.Wakes[0].Key != "failed@tell_worker_findings@1" || !strings.Contains(s.Project.Wakes[0].Why, "terminal.send failed") {
		t.Fatalf("N5 wakes = %+v, want 1 failed@tell_worker_findings@1 with last words", s.Project.Wakes)
	}

	// 3. Recorded retry clears N5; tell succeeds on the next pass!
	failSend.Store(false)
	l.edit(func(st *flow.State) {
		st.Project.Wakes = nil
		st.Tasks[0].Attempts[0].Acts = append(st.Tasks[0].Attempts[0].Acts, flow.Act{Key: flow.AskRetry, Result: "done", At: now})
	})
	l.pass()
	if !flow.Done(l.state().Tasks[0].Attempts[0].Acts, "tell_worker_findings@1") {
		t.Fatalf("key not asked again after retry: %+v", l.state().Tasks[0].Attempts[0].Acts)
	}

	// 4. Settled failure (merge refused: Head branch was modified) -> N5 at once without retrying, never S2.
	l2 := newLanding(t)
	l2.eng.Now = func() time.Time { return now }
	writeGHFile(t, l2.gh, "merge_fail", "")
	l2.pass() // try 1 fails settled
	l2.pass() // N5 wakes once immediately
	now = now.Add(40 * time.Minute)
	l2.pass() // never retries, never S2
	s2 := l2.state()
	if s2.Tasks[0].LastRow != "N5" || len(ghCalls(t, l2.gh, "pr merge")) != 1 {
		t.Fatalf("settled merge failure: row=%s merges=%q, want N5 and 1 merge call", s2.Tasks[0].LastRow, ghCalls(t, l2.gh, "pr merge"))
	}
	if len(s2.Project.Wakes) != 1 || s2.Project.Wakes[0].Key != "failed@merge@"+l2.c1 || !strings.Contains(s2.Project.Wakes[0].Why, "Head branch was modified") {
		t.Fatalf("settled N5 wake = %+v, want 1 wake naming merge@%s and Head branch was modified", s2.Project.Wakes, l2.c1)
	}
}

// TestS2IdleReviewerNoTagWakesAfterStuckMinutes verifies reduced S2 (until 5a.5):
// a reviewer idle with no <report> tag wakes the coordinator once after stuck_minutes.
func TestS2IdleReviewerNoTagWakesAfterStuckMinutes(t *testing.T) {
	l := newLanding(t)
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	l.eng.Now = func() time.Time { return now }
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Rounds = nil })
	l.pass() // N29 starts round 1 reviewer
	revID := l.state().Tasks[0].Attempts[0].Review.RunID
	l.srv.SetScreen(revID, "❯ prompt\n\n⏺ Reviewed everything, forgot the tag.\n")
	l.srv.SetStatus(revID, &hc.Status{App: "claude-code", State: "done"})
	now = now.Add(5 * time.Second)
	l.pass() // R9 -> working
	now = now.Add(25 * time.Second)
	l.pass() // R20 -> idle (ReportNotYet)
	now = now.Add(29 * time.Minute)
	l.pass()
	if w := l.state().Project.Wakes; len(w) != 0 {
		t.Fatalf("woke before stuck_minutes: %+v", w)
	}
	now = now.Add(2 * time.Minute)
	l.pass() // S2 wakes once
	l.pass() // no duplicate wake
	w := l.state().Project.Wakes
	if len(w) != 1 || w[0].Key != "stuck@reviewer_idle@1@"+l.c1 {
		t.Fatalf("wakes past stuck_minutes = %+v, want [stuck@reviewer_idle@1@%s]", w, l.c1)
	}
}

// TestRestartStopsOldLiveTerminalInsteadOfAdopting verifies Item 3a: when an
// ended worker restarts while an old terminal with the same name is still live,
// the engine stops the old terminal first and launches a fresh terminal that
// receives held messages and the right session id (never adopting the old one).
func TestRestartStopsOldLiveTerminalInsteadOfAdopting(t *testing.T) {
	l := newLanding(t)
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	l.eng.Now = func() time.Time { return now }
	var launched []hc.RunParams
	l.srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { launched = append(launched, p) }

	// Leave a live terminal with the exact workerName in hc (as if R22's stop failed).
	wName := workerName(l.id, "T1", 1)
	oldTerm, err := startRun(context.Background(), l.client, world.RunSpec{Name: wName, Cwd: l.wt, Argv: []string{"sh", "-c", "sleep 60"}})
	if err != nil {
		t.Fatal(err)
	}
	const origSID = "22222222-3333-4444-8555-666666666666"
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Ready, a.Rounds, a.RunID = nil, nil, ""
		a.Held = []string{"held findings across failed stop"}
		a.Run = &flow.RunRecord{Address: oldTerm, Role: flow.RoleWorker, CLI: "claude", SessionID: origSID, Resumable: true, State: flow.PhaseEnded, Result: flow.EndStuck, EndedAt: now.Add(-2 * time.Minute)}
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done"}}
	})
	launched = nil
	l.pass()
	a := l.state().Tasks[0].Attempts[0]
	if len(launched) != 1 || !strings.Contains(launched[0].Argv[len(launched[0].Argv)-1], "held findings across failed stop") {
		t.Fatalf("restart adopted old terminal instead of launching with held messages: launched=%+v", launched)
	}
	if a.Run == nil || a.Run.SessionID != origSID || len(a.Held) != 0 {
		t.Fatalf("after restart: run=%+v held=%q, want SessionID=%s and held cleared", a.Run, a.Held, origSID)
	}
}

// TestHeldMessagesClearedOnlyAfterSuccessfulLaunch verifies Item 3b: held
// messages stay on Attempt.Held when a restart cannot stop the old live terminal
// or fails to launch, and are cleared only once a launch actually delivers them.
func TestHeldMessagesClearedOnlyAfterSuccessfulLaunch(t *testing.T) {
	l := newLanding(t)
	now := time.Date(2026, 11, 20, 15, 30, 0, 0, time.UTC)
	l.eng.Now = func() time.Time { return now }
	var failKill, failRun atomic.Bool
	p1 := methodFailProxy(t, l.srv.Path, "terminal.kill", &failKill)
	p2 := methodFailProxy(t, p1, "terminal.run", &failRun)
	l.eng.HC = hc.NewClient(p2)
	var launched []hc.RunParams
	l.srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { launched = append(launched, p) }

	wName := workerName(l.id, "T1", 1)
	oldTerm, err := startRun(context.Background(), l.client, world.RunSpec{Name: wName, Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Ready, a.Rounds, a.RunID = nil, nil, ""
		a.Held = []string{"important CI failure"}
		a.Run = &flow.RunRecord{Address: oldTerm, Role: flow.RoleWorker, CLI: "claude", SessionID: "33333333-4444-4555-8666-777777777777", Resumable: true, State: flow.PhaseEnded, Result: flow.EndStuck, EndedAt: now.Add(-2 * time.Minute)}
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done"}}
	})
	launched = nil

	// 1. Old terminal cannot be killed -> no adoption, Held kept.
	failKill.Store(true)
	l.pass()
	if got := l.state().Tasks[0].Attempts[0].Held; len(got) != 1 {
		t.Fatalf("Held cleared when old terminal kill failed: %q", got)
	}

	// 2. Old terminal killed, but terminal.run fails -> Held still kept.
	failKill.Store(false)
	failRun.Store(true)
	l.pass()
	if got := l.state().Tasks[0].Attempts[0].Held; len(got) != 1 {
		t.Fatalf("Held cleared when terminal.run failed: %q", got)
	}

	// 3. terminal.run succeeds after backoff -> Held delivered and cleared.
	failRun.Store(false)
	now = now.Add(5 * time.Minute)
	l.pass()
	if len(launched) != 1 || !strings.Contains(launched[0].Argv[len(launched[0].Argv)-1], "important CI failure") {
		t.Fatalf("launched prompts = %+v, want held message delivered", launched)
	}
	if got := l.state().Tasks[0].Attempts[0].Held; len(got) != 0 {
		t.Fatalf("Held not cleared after successful launch: %q", got)
	}
}

// TestSessionIDSavedBeforeLaunchAndKeptAcrossCrash verifies Item 3c: the engine
// persists SessionID to state.json before launching the terminal, so a crash
// between launch and post-pass save never records an id the terminal never had.
func TestSessionIDSavedBeforeLaunchAndKeptAcrossCrash(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	eng := New(paths, client)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	if err := eng.Start(repo, "Crash goal", 2); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	var runs []hc.RunParams
	var preCommitState []byte
	var launchedSID string
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) {
		runs = append(runs, p)
		if p.Labels["sheepdog.role"] == flow.RoleWorker {
			launchedSID = argValue(p.Argv, "--session-id")
			preCommitState, _ = os.ReadFile(paths.State(id))
		}
	}
	eng.passProject(ctx, id, false) // coordinator
	if err := eng.Report(runs[0].Env["SHEEPDOG_RUN_TOKEN"], "plan apply", []byte(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: crash",`+testPlanJSON+`}]}`)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N11
	eng.passProject(ctx, id, false) // N23 launches worker

	var savedBefore flow.State
	if err := json.Unmarshal(preCommitState, &savedBefore); err != nil {
		t.Fatalf("unmarshal pre-launch state: %v", err)
	}
	if savedBefore.Tasks[0].Attempts[0].Run == nil || savedBefore.Tasks[0].Attempts[0].Run.SessionID != launchedSID {
		t.Fatalf("SessionID not saved before launch: disk=%+v want=%q", savedBefore.Tasks[0].Attempts[0].Run, launchedSID)
	}

	// Simulate daemon crash right after hc launched the worker (restore preCommitState).
	if err := os.WriteFile(paths.State(id), preCommitState, 0o600); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // adopts the live worker
	st, _ := LoadState(paths.State(id))
	if got := st.Tasks[0].Attempts[0].Run; got == nil || got.SessionID != launchedSID {
		t.Fatalf("after crash adoption: Run=%+v, want SessionID=%q", got, launchedSID)
	}
}

// TestResumableFalseWhenLaunchFails verifies Item 4a: a failed fresh launch
// leaves Resumable=false so the next restart uses a handoff instead of wasting
// a restart trying --resume on a session that was never created.
func TestResumableFalseWhenLaunchFails(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	var failRun atomic.Bool
	var runs []hc.RunParams
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { runs = append(runs, p) }
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	now := time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)
	eng := New(paths, hc.NewClient(methodFailProxy(t, srv.Path, "terminal.run", &failRun)))
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Launch fail goal", 2); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	eng.passProject(ctx, id, false) // coordinator
	if err := eng.Report(runs[0].Env["SHEEPDOG_RUN_TOKEN"], "plan apply", []byte(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: launch",`+testPlanJSON+`}]}`)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N11
	failRun.Store(true)
	eng.passProject(ctx, id, false) // N23 launch fails (R2)
	st, _ := LoadState(paths.State(id))
	if r := st.Tasks[0].Attempts[0].Run; r == nil || r.Resumable {
		t.Fatalf("after failed launch: Run=%+v, want Resumable=false", r)
	}
	failRun.Store(false)
	now = now.Add(time.Minute)
	eng.passProject(ctx, id, false) // restart 1 uses handoff (--session-id, not --resume)
	last := runs[len(runs)-1]
	if argValue(last.Argv, "--resume") != "" || argValue(last.Argv, "--session-id") == "" {
		t.Fatalf("restart after failed fresh launch tried --resume: %q", last.Argv)
	}
}

// TestAppendHeldKeepsRepeatedMessagesWithSameText verifies Item 4c: two separate
// held messages with identical text are both kept, never dropped by text dedupe.
func TestAppendHeldKeepsRepeatedMessagesWithSameText(t *testing.T) {
	got := appendHeld(nil, "same message", "same message")
	if len(got) != 2 || got[0] != "same message" || got[1] != "same message" {
		t.Fatalf("appendHeld dropped duplicate text: %q, want 2 copies", got)
	}
}

// TestWorkerFailedStatusShowsFailedNotWorking verifies Item 4d: once worker
// restarts are exhausted and worker_failed wakes the coordinator, both
// ProjectStatus and Graph report blocked_or_failed with a worker-failed why
// (neither working nor stale).
func TestWorkerFailedStatusShowsFailedNotWorking(t *testing.T) {
	l := newLanding(t)
	now := time.Date(2026, 11, 20, 17, 0, 0, 0, time.UTC)
	l.eng.Now = func() time.Time { return now }
	l.srv.Finish(l.worker, "killed")
	l.edit(func(s *flow.State) {
		s.Settings.WorkerRestarts = 0
		a := &s.Tasks[0].Attempts[0]
		a.Ready, a.Rounds = nil, nil
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done"}}
	})
	l.pass()
	st, err := ProjectStatus(l.paths, l.repo)
	if err != nil {
		t.Fatal(err)
	}
	g, err := l.eng.Graph(l.id)
	if err != nil {
		t.Fatal(err)
	}
	if st[0].Stage != "blocked_or_failed" || st[0].Stale || !strings.Contains(strings.ToLower(st[0].Why), "worker failed") {
		t.Fatalf("status after worker_failed = %+v, want fresh blocked_or_failed mentioning worker failed", st[0])
	}
	if g.Tasks[0].Stage != "blocked_or_failed" || !strings.Contains(strings.ToLower(g.Tasks[0].WhyWaiting), "worker failed") {
		t.Fatalf("graph after worker_failed = %+v, want blocked_or_failed mentioning worker failed", g.Tasks[0])
	}
}

// TestProbeS2HoldsAfterWake (Opus round-2 probe A): after S2's wake for a
// failed act, S2 lets go so rows below run — a send-back reaches the worker,
// and a dead worker is restarted.
func TestProbeS2HoldsAfterWake(t *testing.T) {
	l := newLanding(t).withTokens()
	now := time.Now()
	l.eng.Now = func() time.Time { return now }
	if err := os.WriteFile(filepath.Join(l.gh, "merge_fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l.pass()
	now = now.Add(31 * time.Minute)
	l.pass()
	l.pass()
	// the coordinator now sends the task back: N22 should run (tell the worker), S2 should not hold
	l.report("tok-coord", "task", `{"action":"send-back","task":"T1","why":"redo"}`, "")
	l.pass()
	l.pass()
	s := l.state()
	t.Logf("row after wake + send-back: %s acts=%+v", s.Tasks[0].LastRow, s.Tasks[0].Attempts[0].Acts)
	if s.Tasks[0].LastRow == "S2" {
		t.Errorf("S2 keeps matching after its wake; rows below never run")
	}
	if !flow.Done(s.Tasks[0].Attempts[0].Acts, "tell_worker_findings@3") {
		t.Errorf("send-back did not reach the worker: acts=%+v", s.Tasks[0].Attempts[0].Acts)
	}

	// Kill the worker while the failed merge act is still in Acts: N22 below S2
	// must still run EnsureRun and restart the dead worker after backoff.
	l.srv.Finish(s.Tasks[0].Attempts[0].RunID, "killed")
	now = now.Add(10 * time.Second)
	l.pass() // R33 -> ended(exited)
	now = now.Add(2 * time.Minute)
	l.pass() // N22 -> EnsureRun restarts worker
	if a := l.state().Tasks[0].Attempts[0]; a.RunID == "" || a.Restarts != 1 {
		t.Errorf("dead worker not restarted after S2 wake: row=%s runID=%q restarts=%d", l.state().Tasks[0].LastRow, a.RunID, a.Restarts)
	}
}

// TestProbeCrashWindowRestart (Opus round-2 probe B): crash between the
// pre-launch save and the commit during a counted restart recovers in 3 passes.
func TestProbeCrashWindowRestart(t *testing.T) {
	l := newLanding(t).withTokens()
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Ready, a.Rounds = nil, nil
		a.RunID, a.Restarts = "", 1
		a.Run = &flow.RunRecord{Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseStarting, SessionID: "11111111-1111-4111-8111-111111111111", StartedAt: time.Now(), LastActivity: time.Now()}
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done"}}
	})
	for range 3 {
		l.pass()
	}
	s := l.state()
	a := s.Tasks[0].Attempts[0]
	t.Logf("row=%s runID=%q rec=%+v", s.Tasks[0].LastRow, a.RunID, a.Run)
	if a.RunID == "" {
		t.Errorf("silent wait after crash in the launch window (row %s)", s.Tasks[0].LastRow)
	}
}

// TestCrashWindowRestartTakesOverLiveTerminalAndReviewerRetry verifies that a
// saved starting record with no address takes over an already-spawned terminal
// by name without counting again (preserving SessionID and Resumed), and that a
// reviewer retry crashing in the same window also recovers uncounted.
func TestCrashWindowRestartTakesOverLiveTerminalAndReviewerRetry(t *testing.T) {
	l := newLanding(t).withTokens()
	var launched []hc.RunParams
	l.srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { launched = append(launched, p) }

	// 1. Worker terminal was spawned in hc right before crash; state.json has starting with Address == "".
	const sid = "44444444-5555-4666-8777-888888888888"
	wName := workerName(l.id, "T1", 1)
	liveTerm, err := startRun(context.Background(), l.client, world.RunSpec{
		Name: wName, Cwd: l.wt, ClaudeConfig: l.paths.ClaudeConfig, Prompt: "held across crash",
		Argv: world.ClaudeSessionArgv(flow.RoleWorker, "sonnet", "medium", "bypassPermissions", sid, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Ready, a.Rounds, a.RunID, a.Restarts = nil, nil, "", 1
		a.Held = []string{"held across crash"}
		a.Run = &flow.RunRecord{Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseStarting, SessionID: sid, Resumed: true, StartedAt: time.Now(), LastActivity: time.Now()}
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done"}}
	})
	launched = nil
	for range 3 {
		l.pass()
	}
	if a := l.state().Tasks[0].Attempts[0]; a.RunID != liveTerm || a.Restarts != 1 || len(launched) != 0 || a.Run == nil || a.Run.SessionID != sid || !a.Run.Resumed || len(a.Held) != 0 || len(a.Run.Held) != 0 {
		t.Fatalf("worker crash takeover: runID=%q (want %q) restarts=%d launched=%d run=%+v held=%q/%q", a.RunID, liveTerm, a.Restarts, len(launched), a.Run, a.Held, a.Run.Held)
	}

	// 2. Reviewer retry crashed in the pre-launch window (Retries=1, Run starting with no address).
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Ready = []flow.Ready{{Head: l.c1}}
		a.Rounds = nil
		a.Review = &flow.ReviewRun{Round: 1, Head: l.c1, Code: "code123456", Reviewer: flow.Reviewer{CLI: "claude", Model: "sonnet"}, Retries: 1, FailedModels: []string{"opus"},
			Run: &flow.RunRecord{Role: flow.RoleReviewer, CLI: "claude", State: flow.PhaseStarting, StartedAt: time.Now(), LastActivity: time.Now()}}
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done"}, {Key: "push_and_open_pr@" + l.c1, Result: "done"}, {Key: "start_reviewer@1@" + l.c1, Result: "done"}}
	})
	for range 3 {
		l.pass()
	}
	if rv := l.state().Tasks[0].Attempts[0].Review; rv == nil || rv.RunID == "" || rv.Retries != 1 || rv.Code != "code123456" {
		t.Fatalf("reviewer retry crash recovery: review=%+v, want non-empty RunID with Retries=1 and Code preserved", rv)
	}
}
