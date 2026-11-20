package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
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
