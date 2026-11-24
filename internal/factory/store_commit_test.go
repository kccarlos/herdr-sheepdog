package factory

// PR G (store commit contract; alert raises use it), TDD step 1: failing tests
// for S1–S5, S2b, S2c, S3b (including Stop's exception), TrimLog dropping
// unparseable lines, and alert raises on the commit path.
//
// All assertions read only files (state.json, log.jsonl), public read views
// (ProjectLog, ProjectStatus, LogLines), stderr output, and fake-hc requests;
// no test reads internal engine maps.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// readRawLogLines parses every non-empty line in log.jsonl and fails the test
// if any line is unterminated (torn) or does not parse as JSON.
func readRawLogLines(t *testing.T, path string) []LogLine {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []LogLine
	for i, raw := range bytes.SplitAfter(data, []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		if !bytes.HasSuffix(raw, []byte("\n")) {
			t.Fatalf("log.jsonl line %d is torn (missing newline): %q", i+1, raw)
		}
		var l LogLine
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatalf("log.jsonl line %d failed to parse (%v): %q", i+1, err, raw)
		}
		out = append(out, l)
	}
	return out
}

// assertLogInvariants checks S1 and S3 on disk: every log.jsonl line parses,
// Seq is strictly increasing, and no line has Seq > state.Seq.
func assertLogInvariants(t *testing.T, statePath, logPath string) (flow.State, []LogLine) {
	t.Helper()
	st, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState(%s): %v", statePath, err)
	}
	lines := readRawLogLines(t, logPath)
	var prev int64
	for i, l := range lines {
		if l.Seq <= prev {
			t.Fatalf("log.jsonl line %d Seq=%d not strictly greater than previous Seq=%d (all seqs=%v)", i+1, l.Seq, prev, logSeqs(lines))
		}
		if l.Seq > st.Seq {
			t.Fatalf("log.jsonl line %d Seq=%d exceeds state.Seq=%d (rule=%q decision=%q)", i+1, l.Seq, st.Seq, l.Rule, l.Decision)
		}
		prev = l.Seq
	}
	return st, lines
}

func logSeqs(lines []LogLine) []int64 {
	seqs := make([]int64, len(lines))
	for i, l := range lines {
		seqs[i] = l.Seq
	}
	return seqs
}

type logDecision struct {
	Seq      int64
	By       string
	Task     string
	Rule     string
	Asked    []string
	Decision string
	Why      string
}

func extractLogDecisions(lines []LogLine) []logDecision {
	out := make([]logDecision, len(lines))
	for i, l := range lines {
		out[i] = logDecision{
			Seq:      l.Seq,
			By:       l.By,
			Task:     l.Task,
			Rule:     l.Rule,
			Asked:    l.Asked,
			Decision: l.Decision,
			Why:      l.Why,
		}
	}
	return out
}

// storeWorld is a self-contained fake-hc + temp-repo harness for store commit
// contract tests.
type storeWorld struct {
	t         *testing.T
	ctx       context.Context
	srv       *hctest.Server
	client    *hc.Client
	paths     Paths
	repo      string
	baseSHA   string
	eng       *Engine
	id        string
	now       time.Time
	trimNext  bool
	daemonLog *bytes.Buffer
	stderrBuf *bytes.Buffer

	mu          sync.Mutex
	projectDisk DiskReading
	dataDisk    DiskReading
	writeState  func(string, flow.State) error
	writeLog    func(string, LogLine) error
}

func newStoreWorld(t *testing.T, goal string) *storeWorld {
	t.Helper()
	codexHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	srv := hctest.New(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	bindRepoFakeGH(t, repo)

	w := &storeWorld{
		t:           t,
		ctx:         context.Background(),
		srv:         srv,
		client:      hc.NewClient(srv.Path),
		paths:       testPaths(t),
		repo:        repo,
		baseSHA:     base,
		now:         time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC),
		daemonLog:   &bytes.Buffer{},
		stderrBuf:   &bytes.Buffer{},
		projectDisk: DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 40, Known: true},
		dataDisk:    DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
	}

	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		argv := cdUnwrapExecArgv(p.Argv)
		zero := 0
		if len(argv) >= 2 && filepath.Base(argv[0]) == "claude" && argv[1] == "auth" {
			return hc.ExecResult{Code: &zero, Stdout: []byte(`{"loggedIn":true}`)}, nil
		}
		if len(argv) >= 2 && filepath.Base(argv[0]) == "codex" && argv[1] == "login" {
			return hc.ExecResult{Code: &zero, Stdout: []byte("Logged in using ChatGPT\n")}, nil
		}
		if len(argv) >= 4 && filepath.Base(argv[0]) == "codexbar" && argv[1] == "usage" {
			prov := "claude"
			for i := 0; i+1 < len(argv); i++ {
				if argv[i] == "--provider" {
					prov = argv[i+1]
				}
			}
			w.mu.Lock()
			now := w.now
			w.mu.Unlock()
			body := poolOut(prov, now, &poolWin{80, 3 * time.Hour}, &poolWin{80, 72 * time.Hour}, nil).Stdout
			return hc.ExecResult{Code: &zero, Stdout: []byte(body)}, nil
		}
		isShellProbe := len(p.Argv) >= 5 && filepath.Base(p.Argv[0]) == "sh" && p.Argv[1] == "-c" && strings.Contains(p.Argv[2], probedDirStart)
		if isShellProbe {
			target := p.Argv[len(p.Argv)-1]
			return hc.ExecResult{Code: &zero, Stdout: []byte(probedDirStart + target + probedDirEnd + "\n")}, nil
		}
		return hctest.GitExec(p)
	}

	w.newEngine()
	if err := w.eng.Start(repo, goal, 4); err != nil {
		t.Fatal(err)
	}
	w.id = w.eng.projectDirs()[0].Name()
	return w
}

func (w *storeWorld) newEngine() {
	eng := New(w.paths, w.client)
	eng.StartCheck = noopStartCheck
	eng.Log = logging.NewJSONLLogger(w.daemonLog)
	eng.Stderr = w.stderrBuf
	eng.volumeTimeout = 80 * time.Millisecond
	eng.Now = func() time.Time {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.now
	}
	eng.diskRead = func(_ context.Context, path string) DiskReading {
		w.mu.Lock()
		defer w.mu.Unlock()
		if sameOrUnderPath(w.repo, path) {
			return w.projectDisk
		}
		if sameOrUnderPath(w.paths.DataDir, path) {
			return w.dataDisk
		}
		return DiskReading{Volume: "/Volumes/Other", UsedPercent: 20, Known: true}
	}
	eng.writeState = func(path string, s flow.State) error {
		w.mu.Lock()
		fn := w.writeState
		w.mu.Unlock()
		if fn != nil {
			return fn(path, s)
		}
		return SaveState(path, s)
	}
	eng.writeLog = func(path string, l LogLine) error {
		w.mu.Lock()
		fn := w.writeLog
		w.mu.Unlock()
		if fn != nil {
			return fn(path, l)
		}
		return AppendLog(path, l)
	}
	w.eng = eng
}

func (w *storeWorld) restart() {
	w.newEngine()
	w.trimNext = true
}

func (w *storeWorld) pass() {
	w.t.Helper()
	trim := w.trimNext
	w.trimNext = false
	w.eng.passProject(w.ctx, w.id, trim)
}

func (w *storeWorld) advanceMinute() {
	w.mu.Lock()
	w.now = w.now.Add(time.Minute)
	w.mu.Unlock()
}

func (w *storeWorld) setDisks(proj, data DiskReading) {
	w.mu.Lock()
	w.projectDisk = proj
	w.dataDisk = data
	w.mu.Unlock()
}

func (w *storeWorld) setWriteHooks(ws func(string, flow.State) error, wl func(string, LogLine) error) {
	w.mu.Lock()
	w.writeState = ws
	w.writeLog = wl
	w.mu.Unlock()
}

func (w *storeWorld) state() flow.State {
	w.t.Helper()
	s, err := LoadState(w.paths.State(w.id))
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}

func (w *storeWorld) edit(fn func(*flow.State)) {
	w.t.Helper()
	s := w.state()
	fn(&s)
	if err := SaveState(w.paths.State(w.id), s); err != nil {
		w.t.Fatal(err)
	}
}

// seedIdleCoordAndWarmUp places a live idle coordinator terminal ("coord1") and
// one parked task (so J4 does not fire) and runs one healthy pass so the
// coordinator RunRecord and initial sweep are settled before the test pass.
func (w *storeWorld) seedIdleCoordAndWarmUp() {
	w.t.Helper()
	w.mu.Lock()
	now := w.now
	w.mu.Unlock()
	past := now.Add(-time.Minute)
	pastMS := uint64(past.UnixMilli())
	w.srv.Add(hc.Terminal{
		Address:  "coord1",
		Name:     coordName(w.id),
		State:    "live",
		Labels:   testCoordLabels(w.id, "run-coord1"),
		Status:   &hc.Status{App: "claude-code", State: "idle"},
		Reported: true,
		StatusMS: &pastMS,
	})
	w.edit(func(s *flow.State) {
		s.Project.RunID = "coord1"
		s.Project.Run = &flow.RunRecord{
			ID:             "run-coord1",
			Address:        "coord1",
			Role:           flow.RoleCoordinator,
			CLI:            "claude",
			Model:          "opus",
			State:          flow.PhaseIdle,
			Status:         "idle",
			StatusReported: true,
			Turn:           1,
			StartedAt:      past,
			LastActivity:   past,
			LastTurnEnd:    past,
			StatusSince:    past,
		}
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: parked anchor", Parked: true, LastRow: "N3"}}
	})
	w.pass()
	w.advanceMinute()
}

// Test 1 (S1, S2, S3, S3b):
// Start; Say with writeState failing returns an error; Say again.
// Strictly increasing Seq, one say line, none past state.Seq.
func TestStore_FailedSaveLineIsDroppedBeforeNextAppend(t *testing.T) {
	repo := initGitRepo(t)
	paths := testPaths(t)
	eng := newGitTestEngine(t, paths, nil)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})

	if err := eng.Start(repo, "Store commit contract", 2); err != nil {
		t.Fatalf("Start: %v", err)
	}
	id := eng.projectDirs()[0].Name()

	var failSave atomic.Bool
	failSave.Store(true)
	eng.writeState = func(path string, s flow.State) error {
		if failSave.Load() {
			return errors.New("state write failed")
		}
		return SaveState(path, s)
	}

	if err := eng.Say(repo, "first attempt"); err == nil {
		t.Fatal("Say succeeded when writeState failed; want error (S3b)")
	}

	failSave.Store(false)
	if err := eng.Say(repo, "second attempt"); err != nil {
		t.Fatalf("second Say after recovery failed: %v", err)
	}

	st, lines := assertLogInvariants(t, paths.State(id), paths.Log(id))
	var sayLines []LogLine
	for _, l := range lines {
		if l.Rule == "say" {
			sayLines = append(sayLines, l)
		}
	}
	if len(sayLines) != 1 || sayLines[0].Why != "second attempt" {
		t.Fatalf("say lines = %+v, want exactly 1 for %q", sayLines, "second attempt")
	}
	if len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Facts != "second attempt" {
		t.Fatalf("state wakes = %+v, want 1 wake for %q", st.Project.Wakes, "second attempt")
	}
}

// Test 2 (S2):
// writeLog writes half a line and errors; the next commit succeeds; every line parses.
func TestStore_TornLineIsDroppedBeforeNextAppend(t *testing.T) {
	repo := initGitRepo(t)
	paths := testPaths(t)
	eng := newGitTestEngine(t, paths, nil)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})

	if err := eng.Start(repo, "Torn log tail", 2); err != nil {
		t.Fatalf("Start: %v", err)
	}
	id := eng.projectDirs()[0].Name()

	var tornOnce atomic.Bool
	eng.writeLog = func(path string, l LogLine) error {
		if tornOnce.CompareAndSwap(false, true) {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, _ = f.WriteString(`{"seq":2,"rule":"sa`) // half a line, no trailing newline
			_ = f.Close()
			return errors.New("short write on log.jsonl")
		}
		return AppendLog(path, l)
	}

	if err := eng.Say(repo, "torn attempt"); err == nil {
		t.Fatal("Say succeeded when writeLog wrote a torn tail and errored")
	}
	if err := eng.Say(repo, "clean attempt"); err != nil {
		t.Fatalf("next Say after torn tail failed: %v", err)
	}

	_, lines := assertLogInvariants(t, paths.State(id), paths.Log(id))
	var sayLines []LogLine
	for _, l := range lines {
		if l.Rule == "say" {
			sayLines = append(sayLines, l)
		}
	}
	if len(sayLines) != 1 || sayLines[0].Why != "clean attempt" {
		t.Fatalf("say lines = %+v, want 1 clean line for %q", sayLines, "clean attempt")
	}
}

// Test 3 (S2, TrimLog):
// A garbled middle line in log.jsonl; a restart and one commit succeed; the
// garbled line is gone and every remaining line parses.
func TestStore_UnparseableLineDoesNotStopTheProject(t *testing.T) {
	repo := initGitRepo(t)
	paths := testPaths(t)
	eng1 := newGitTestEngine(t, paths, nil)
	eng1.Log = logging.NewJSONLLogger(&bytes.Buffer{})

	if err := eng1.Start(repo, "Garbled middle line", 2); err != nil {
		t.Fatalf("Start: %v", err)
	}
	id := eng1.projectDirs()[0].Name()
	if err := eng1.Say(repo, "before garble"); err != nil {
		t.Fatalf("first Say: %v", err)
	}

	raw, err := os.ReadFile(paths.Log(id))
	if err != nil {
		t.Fatal(err)
	}
	parts := bytes.SplitAfter(raw, []byte("\n"))
	if len(parts) < 2 {
		t.Fatalf("expected at least 2 log lines before garble, got %q", raw)
	}
	var garbled bytes.Buffer
	garbled.Write(parts[0])
	garbled.WriteString("{not valid json middle line\n")
	for _, p := range parts[1:] {
		garbled.Write(p)
	}
	if err := os.WriteFile(paths.Log(id), garbled.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	// Simulate daemon restart with a fresh engine and make one commit.
	eng2 := newGitTestEngine(t, paths, nil)
	eng2.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	if err := eng2.Say(repo, "after restart"); err != nil {
		t.Fatalf("commit after restart with garbled middle line failed: %v", err)
	}

	_, lines := assertLogInvariants(t, paths.State(id), paths.Log(id))
	var sayWhys []string
	for _, l := range lines {
		if l.Rule == "say" {
			sayWhys = append(sayWhys, l.Why)
		}
	}
	if !slices.Equal(sayWhys, []string{"before garble", "after restart"}) {
		t.Fatalf("say lines after restart = %v, want [before garble, after restart]", sayWhys)
	}
}

// Test 4 (S4):
// Readers hide log lines past state.Seq, pinned through ProjectLog (the CLI
// reads from another process with no lock, reading state.json before log.jsonl).
func TestStore_ReadersHideLinesPastStateSeq(t *testing.T) {
	paths := testPaths(t)
	id := "proj-readers-s4"
	folder := t.TempDir()
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

	st := flow.State{
		Seq:      2,
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: id, Folder: folder, Goal: "S4 reader filter", Started: true},
		Tasks:    []flow.Task{{ID: "T1", Title: "feat: task one", LastRow: "N11"}},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	for _, l := range []LogLine{
		{Seq: 1, At: at, Project: id, By: "owner", Rule: "start", Decision: "started", Why: "S4 reader filter"},
		{Seq: 2, At: at.Add(time.Minute), Project: id, By: "engine", Task: "T1", Rule: "N11", Decision: "started attempt", Why: "no attempt"},
		{Seq: 3, At: at.Add(2 * time.Minute), Project: id, By: "engine", Task: "T1", Rule: "N23", Decision: "orphan uncommitted", Why: "must be hidden"},
		{Seq: 4, At: at.Add(3 * time.Minute), Project: id, By: "owner", Rule: "say", Decision: "orphan say", Why: "past state.Seq"},
	} {
		if err := AppendLog(paths.Log(id), l); err != nil {
			t.Fatal(err)
		}
	}

	// Cross-process CLI path: ProjectLog with no Engine instance and no lock.
	lines, _, err := ProjectLog(context.Background(), paths, folder, 0)
	if err != nil {
		t.Fatalf("ProjectLog: %v", err)
	}
	if got := logSeqs(lines); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("ProjectLog returned seqs %v, want [1 2] (lines past state.Seq=2 must be hidden)", got)
	}

	// Web log view and offline status read through the same loadProjectCopy seam.
	eng := New(paths, nil)
	entries, err := eng.LogLines(id, 0)
	if err != nil {
		t.Fatalf("LogLines: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("LogLines returned %d entries (%+v), want 2", len(entries), entries)
	}
	rows, err := ProjectStatus(paths, folder)
	if err != nil {
		t.Fatalf("ProjectStatus: %v", err)
	}
	if len(rows) != 1 || rows[0].Rule != "N11" {
		t.Fatalf("ProjectStatus showed orphan task row: %+v, want T1 at N11", rows)
	}
}

// Test 5 (S2, S2b — reviewer probe 1):
// A held project wake, an idle coordinator, writeLog failing once at an earlier
// commit of the pass (the disk raise, with WaitKey seeded). 0 sends in that
// pass and WaitKey/Seq unchanged in state.json; 1 send after the next pass.
func TestStore_NoActAfterAFailedWriteInTheSamePass(t *testing.T) {
	w := newStoreWorld(t, "No act after failed write in same pass")
	w.seedIdleCoordAndWarmUp()

	w.edit(func(s *flow.State) {
		s.Project.WaitKey = "seeded-wait-key"
		s.Project.Wakes = []flow.Wake{{
			Key:   "owner_say:seeded",
			Why:   "owner said",
			Facts: "please check status",
			At:    w.now,
		}}
	})
	stBefore := w.state()
	seqBefore := stBefore.Seq

	// Trigger the disk alert raise earlier in the same pass, and fail writeLog once on it.
	w.setDisks(
		DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 95, Known: true},
		DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
	)
	var failedOnce atomic.Bool
	w.setWriteHooks(nil, func(path string, l LogLine) error {
		if failedOnce.CompareAndSwap(false, true) {
			return errors.New("transient log write failure")
		}
		return AppendLog(path, l)
	})

	w.pass()

	if !failedOnce.Load() {
		t.Fatal("test setup bug: writeLog failure hook did not trigger on the disk raise")
	}
	if sends := sendsTo(t, w.srv.Requests(), "coord1"); len(sends) != 0 {
		t.Fatalf("sends to coord1 in failing pass = %d, want 0 (S2b): %q", len(sends), sends)
	}
	stAfterFail := w.state()
	if stAfterFail.Project.WaitKey != "seeded-wait-key" || stAfterFail.Seq != seqBefore {
		t.Fatalf("state.json mutated after failed write: WaitKey=%q (want %q), Seq=%d (want %d)",
			stAfterFail.Project.WaitKey, "seeded-wait-key", stAfterFail.Seq, seqBefore)
	}
	if len(stAfterFail.Project.Wakes) != 1 {
		t.Fatalf("held wake lost on failed pass: %+v", stAfterFail.Project.Wakes)
	}

	// Next pass is healthy: store recovers, commits disk raise and delivers the wake once.
	w.advanceMinute()
	w.pass()

	if sends := sendsTo(t, w.srv.Requests(), "coord1"); len(sends) != 1 {
		t.Fatalf("sends to coord1 after recovery pass = %d, want 1: %q", len(sends), sends)
	}
	stRecovered, _ := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
	if len(stRecovered.Project.Wakes) != 0 {
		t.Fatalf("wakes still held after recovery pass: %+v", stRecovered.Project.Wakes)
	}
}

// Test 6 (S2b, S2c — reviewer probe 2):
// The same idle-coordinator + held-wake world with the store returning a plain
// error (not ENOSPC) on writes for five passes: at most 1 send (on main: 5).
// Pins S2c: freshLocked's write-back goes through writeState, so a failing
// store stays dirty even when TrimLog had nothing to drop.
func TestStore_FailingStoreDoesNotRepeatActs(t *testing.T) {
	w := newStoreWorld(t, "Failing store does not repeat acts")
	w.seedIdleCoordAndWarmUp()

	w.edit(func(s *flow.State) {
		s.Project.Wakes = []flow.Wake{{
			Key:   "owner_say:repeat-check",
			Why:   "owner said",
			Facts: "do not spam coordinator",
			At:    w.now,
		}}
	})
	seqBefore := w.state().Seq

	plainErr := errors.New("store write failed (plain error, not ENOSPC)")
	w.setWriteHooks(
		func(string, flow.State) error { return plainErr },
		func(string, LogLine) error { return plainErr },
	)

	for range 5 {
		w.pass()
		w.advanceMinute()
	}

	sends := sendsTo(t, w.srv.Requests(), "coord1")
	if len(sends) > 1 {
		t.Fatalf("sends to coord1 over 5 failing passes = %d, want at most 1 (S2b/S2c): %q", len(sends), sends)
	}
	st := w.state()
	if st.Seq != seqBefore || len(st.Project.Wakes) != 1 {
		t.Fatalf("state.json changed while store was failing: Seq=%d (want %d), wakes=%+v", st.Seq, seqBefore, st.Project.Wakes)
	}
}

// Test 7 (S2, e.waits — reviewer probe 3):
// A task on N9 with writeLog failing once on its waiting line; next pass
// healthy: one "waiting" line is written (on main: 0, because e.waits kept
// the uncommitted value).
func TestStore_WaitingLineIsWrittenAfterAFailedCommit(t *testing.T) {
	w := newStoreWorld(t, "N9 waiting line after failed commit")
	w.seedIdleCoordAndWarmUp()

	// T1 is parked (not done); T2 depends on T1 with no attempt -> matches N9 (waiting).
	w.edit(func(s *flow.State) {
		s.Tasks = append(s.Tasks, flow.Task{
			ID:            "T2",
			Title:         "feat: waits on T1",
			After:         []string{"T1"},
			EstimateLines: 20,
		})
	})

	var failedN9 atomic.Bool
	w.setWriteHooks(nil, func(path string, l LogLine) error {
		if l.Task == "T2" && l.Rule == "N9" && failedN9.CompareAndSwap(false, true) {
			return errors.New("log write failed on N9 waiting line")
		}
		return AppendLog(path, l)
	})

	w.pass()
	if !failedN9.Load() {
		t.Fatal("test setup bug: T2 did not attempt an N9 waiting log line on pass 1")
	}

	countN9Waiting := func() []LogLine {
		_, lines := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
		var out []LogLine
		for _, l := range lines {
			if l.Task == "T2" && l.Rule == "N9" && l.Decision == "waiting" {
				out = append(out, l)
			}
		}
		return out
	}
	if got := countN9Waiting(); len(got) != 0 {
		t.Fatalf("after failed N9 commit, waiting lines = %+v, want 0", got)
	}

	// Healthy pass: e.waits was cleared when the store became dirty, so N9 logs once.
	w.advanceMinute()
	w.pass()
	if got := countN9Waiting(); len(got) != 1 {
		t.Fatalf("after healthy recovery pass, N9 waiting lines for T2 = %d (%+v), want 1", len(got), got)
	}

	// Another healthy pass must not duplicate the waiting line.
	w.advanceMinute()
	w.pass()
	if got := countN9Waiting(); len(got) != 1 {
		t.Fatalf("after second healthy pass, N9 waiting lines for T2 = %d (%+v), want 1", len(got), got)
	}
}

// Test 8 (S2, S3, S4, §2):
// Table: {raise} × {append fails, save fails} × {go on in process, restart};
// recover, one pass. State and log decisions are equal in both columns; a held
// alert shows in ProjectStatus in the in-process column.
func TestStore_FailureThenRecoveryEqualsRestart(t *testing.T) {
	type outcome struct {
		seq    int64
		alerts map[string]flow.Alert
		logs   []logDecision
	}

	normalizeAlerts := func(in map[string]flow.Alert) map[string]flow.Alert {
		out := make(map[string]flow.Alert, len(in))
		for k, a := range in {
			a.At = time.Time{}
			out[k] = a
		}
		return out
	}

	runColumn := func(t *testing.T, failMode string, doRestart bool) outcome {
		t.Helper()
		w := newStoreWorld(t, "Raise recovery parity "+failMode)
		w.seedIdleCoordAndWarmUp()
		seqBefore := w.state().Seq

		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 95, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
		)
		writeErr := errors.New("injected " + failMode + " failure")
		switch failMode {
		case "append_fails":
			w.setWriteHooks(nil, func(string, LogLine) error { return writeErr })
		case "save_fails":
			w.setWriteHooks(func(string, flow.State) error { return writeErr }, nil)
		default:
			t.Fatalf("unknown failMode %q", failMode)
		}

		// Failing pass: raise commit fails.
		w.pass()

		// Before recovery: state.json must not have the alert, and readers
		// (ProjectLog) must hide any uncommitted orphan line (S4).
		stMid := w.state()
		if stMid.Seq != seqBefore || stMid.Project.HasAlert(flow.AlertDisk) {
			t.Fatalf("after failed %s: state.json mutated (Seq=%d want %d, alerts=%+v)", failMode, stMid.Seq, seqBefore, stMid.Project.Alerts)
		}
		visibleMid, _, err := ProjectLog(w.ctx, w.paths, w.repo, 0)
		if err != nil {
			t.Fatalf("ProjectLog before recovery: %v", err)
		}
		for _, l := range visibleMid {
			if l.Seq > stMid.Seq {
				t.Fatalf("ProjectLog exposed uncommitted line past state.Seq=%d before recovery: %+v", stMid.Seq, l)
			}
		}

		if doRestart {
			w.restart()
			// A restart loses in-memory held alerts; offline/restarted status has none yet.
			if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, flow.AlertDisk); ok {
				t.Fatalf("restarted engine unexpectedly showed held disk alert before recovery pass: %+v", ts)
			}
		} else {
			// In-process column: held alert must be visible in Engine.ProjectStatus.
			if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, flow.AlertDisk); !ok {
				rows, _ := w.eng.ProjectStatus(w.repo)
				t.Fatalf("in-process Engine.ProjectStatus missing held disk alert after %s; rows=%+v", failMode, rows)
			}
		}

		// Recover writes, advance clock, and run one pass.
		w.setWriteHooks(nil, nil)
		w.advanceMinute()
		w.pass()

		stEnd, lines := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
		if !stEnd.Project.HasAlert(flow.AlertDisk) {
			t.Fatalf("after recovery pass (%s, restart=%t), disk alert missing from state.json", failMode, doRestart)
		}
		if _, ok := statusAlert(t, w.paths, w.repo, flow.AlertDisk); !ok {
			t.Fatalf("after recovery pass (%s, restart=%t), offline ProjectStatus missing disk alert", failMode, doRestart)
		}
		return outcome{
			seq:    stEnd.Seq,
			alerts: normalizeAlerts(stEnd.Project.Alerts),
			logs:   extractLogDecisions(lines),
		}
	}

	for _, op := range []string{"raise"} {
		for _, failMode := range []string{"append_fails", "save_fails"} {
			t.Run(op+"/"+failMode, func(t *testing.T) {
				inProc := runColumn(t, failMode, false)
				restarted := runColumn(t, failMode, true)
				if !reflect.DeepEqual(inProc, restarted) {
					t.Fatalf("%s/%s diverged between in-process and restart:\nin-process: %+v\nrestart:    %+v",
						op, failMode, inProc, restarted)
				}
			})
		}
	}
}

// Test 9 (S3b exception — design review round 2 condition):
// A started project with a live worker run and a live coordinator; writeState
// fails on every call; Stop returns an error, fake hc shows both terminals
// stopped, and state.json (and log.jsonl) are unchanged without a commit.
func TestStore_StopStillStopsRunsOnADirtyStore(t *testing.T) {
	w := newStoreWorld(t, "Stop still stops runs on dirty store")
	w.srv.Add(hc.Terminal{
		Address: "coord1",
		Name:    coordName(w.id),
		State:   "live",
		Labels:  testCoordLabels(w.id, "run-coord1"),
	})
	w.srv.Add(hc.Terminal{
		Address: "worker1",
		Name:    workerName(w.id, "T1", 1),
		State:   "live",
		Labels:  map[string]string{"sheepdog.project": w.id, "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-worker1"},
	})
	w.edit(func(s *flow.State) {
		s.Project.Started = true
		s.Project.RunID = "coord1"
		s.Project.Run = &flow.RunRecord{
			ID:      "run-coord1",
			Address: "coord1",
			Role:    flow.RoleCoordinator,
			CLI:     "claude",
			State:   flow.PhaseIdle,
		}
		s.Tasks = []flow.Task{{
			ID:    "T1",
			Title: "feat: live worker",
			Attempts: []flow.Attempt{{
				BaseCommit: w.baseSHA,
				RunID:      "worker1",
				Run: &flow.RunRecord{
					ID:      "run-worker1",
					Address: "worker1",
					Role:    flow.RoleWorker,
					CLI:     "claude",
					State:   flow.PhaseWorking,
				},
			}},
		}}
	})

	stateBefore, err := os.ReadFile(w.paths.State(w.id))
	if err != nil {
		t.Fatal(err)
	}
	logBefore, err := os.ReadFile(w.paths.Log(w.id))
	if err != nil {
		t.Fatal(err)
	}

	// Fresh engine starts dirty; writeState fails on every call so freshLocked fails.
	w.restart()
	w.setWriteHooks(func(string, flow.State) error {
		return errors.New("state write failed")
	}, nil)

	if err := w.eng.Stop(w.repo); err == nil {
		t.Fatal("Stop succeeded when writeState failed on every call; want error")
	}

	list, err := w.client.List(w.ctx, true)
	if err != nil {
		t.Fatalf("hc List: %v", err)
	}
	if len(list.Terminals) != 0 {
		t.Fatalf("Stop on a dirty store left terminals running: %+v", list.Terminals)
	}

	stateAfter, err := os.ReadFile(w.paths.State(w.id))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stateAfter, stateBefore) {
		t.Fatalf("state.json changed on failed Stop:\nbefore:\n%s\nafter:\n%s", stateBefore, stateAfter)
	}
	logAfter, err := os.ReadFile(w.paths.Log(w.id))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(logAfter, logBefore) {
		t.Fatalf("log.jsonl changed on failed Stop (must return error without a commit):\nbefore:\n%s\nafter:\n%s", logBefore, logAfter)
	}
}

// Test 10 (S2 — r1 review: NeonH Finding 1 / Carbon Major 2):
// When observeReviewLocked dirties the store on its Review.Age save (or enters
// dirty), it must not write reports/<task>-r<n>.md or stop the live Codex
// reviewer terminal; on the next healthy pass, the verdict commits cleanly.
func TestStore_DirtyStoreDoesNotDestroyCodexVerdict(t *testing.T) {
	ctx := context.Background()
	srv, eng, paths, id, c1 := codexRound2(t)
	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	rev := st.Tasks[0].Attempts[0].Review
	reply := "• All checks pass.\nself-check misses: 0\ndeletable: none\n" +
		`<report>{"code":"` + rev.Code + `","round":2,"head":"` + c1 + `","verdict":"pass","majors":0}</report>` + "\n"
	srv.SetScreen(rev.RunID, codexScreen(rev.Code, c1, reply))
	time.Sleep(2 * time.Millisecond) // ensure passDelta > 0 so Review.Age triggers saveLocked

	var failedAgeSave atomic.Bool
	eng.writeState = func(path string, s flow.State) error {
		if len(s.Tasks) > 0 && len(s.Tasks[0].Attempts) > 0 &&
			s.Tasks[0].Attempts[0].Review != nil && len(s.Tasks[0].Attempts[0].Rounds) == 1 &&
			failedAgeSave.CompareAndSwap(false, true) {
			return errors.New("transient state write failure on Review.Age")
		}
		return SaveState(path, s)
	}

	eng.passProject(ctx, id) // Review.Age save fails -> store dirty before acceptVerdictLocked
	if !failedAgeSave.Load() {
		t.Fatal("setup bug: Review.Age save did not fire")
	}
	if _, err := eng.HC.Get(ctx, rev.RunID); err != nil {
		t.Fatalf("live Codex reviewer %q was stopped/removed on a dirty store: %v", rev.RunID, err)
	}
	if _, err := os.Stat(paths.Report(id, "T1", 2)); !os.IsNotExist(err) {
		t.Fatalf("report file %s was written while store was dirty (err=%v)", paths.Report(id, "T1", 2), err)
	}

	eng.passProject(ctx, id) // store healthy: verdict must now commit cleanly
	stAfter, _ := assertLogInvariants(t, paths.State(id), paths.Log(id))
	rounds := stAfter.Tasks[0].Attempts[0].Rounds
	if len(rounds) != 2 || rounds[1].Verdict != flow.VerdictPass {
		t.Fatalf("rounds after recovery = %+v, want 2 passing rounds (verdict was lost)", rounds)
	}
	if _, err := os.Stat(paths.Report(id, "T1", 2)); err != nil {
		t.Fatalf("report file missing after healthy recovery pass: %v", err)
	}
}

// Test 11 (S2 — r1 review: NeonH Finding 2 / Carbon Major 1):
// When an earlier write in the same pass already dirtied the store, a later
// task at N9 must not poison e.waits with an uncommitted waiting key; on the
// next healthy pass it writes its N9 waiting line (matching crash + restart).
func TestStore_WaitingLineWrittenWhenStoreAlreadyDirtyEarlierInPass(t *testing.T) {
	w := newStoreWorld(t, "N9 waiting line after earlier failed commit in same pass")
	w.seedIdleCoordAndWarmUp() // T1 is parked

	w.edit(func(s *flow.State) {
		s.Tasks = append(s.Tasks,
			flow.Task{ID: "T0", Title: "feat: independent", EstimateLines: 20},
			flow.Task{ID: "T2", Title: "feat: waits on T1", After: []string{"T1"}, EstimateLines: 20},
		)
	})

	var failedT0 atomic.Bool
	w.setWriteHooks(nil, func(path string, l LogLine) error {
		if l.Task == "T0" && l.Rule == "N11" && failedT0.CompareAndSwap(false, true) {
			return errors.New("log write failed on T0 N11")
		}
		return AppendLog(path, l)
	})

	w.pass() // T0 fails commit -> store dirty -> T2 runs on dirty store
	if !failedT0.Load() {
		t.Fatal("setup bug: T0 did not attempt N11 commit")
	}

	w.advanceMinute()
	w.pass() // healthy recovery pass

	_, lines := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
	var waits int
	for _, l := range lines {
		if l.Task == "T2" && l.Rule == "N9" && l.Decision == "waiting" {
			waits++
		}
	}
	if waits != 1 {
		t.Fatalf("T2 N9 waiting lines after recovery = %d, want 1 (same as restart)", waits)
	}
}

// Test 12 (S2 — r1 review: NeonH Finding 3 / Carbon Major 4):
// readPRLocked must not retain an uncommitted PR snapshot in e.prs after a
// failed pr_snapshot commit; on the next healthy pass, pr_snapshot is written
// once, matching crash + restart.
func TestStore_PRSnapshotLineWrittenAfterFailedCommit(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)

	var failedOnce atomic.Bool
	l.eng.writeLog = func(path string, line LogLine) error {
		if line.Rule == "pr_snapshot" && failedOnce.CompareAndSwap(false, true) {
			return errors.New("log write failed on pr_snapshot")
		}
		return AppendLog(path, line)
	}

	l.pass()
	if !failedOnce.Load() {
		t.Fatal("setup bug: pr_snapshot did not attempt a commit on pass 1")
	}

	l.pass() // healthy pass: must write pr_snapshot once, matching a restart
	_, lines := assertLogInvariants(t, l.paths.State(l.id), l.paths.Log(l.id))
	var snaps int
	for _, ln := range lines {
		if ln.Rule == "pr_snapshot" {
			snaps++
		}
	}
	if snaps != 1 {
		t.Fatalf("pr_snapshot lines after recovery = %d, want 1", snaps)
	}
}

// Test 13 (S2b — r1 review: NeonH Finding 4):
// Inside a pass executor (actTaskLocked -> execTellConflict -> deliverWorkerLocked),
// if advanceRunLocked fails a state write and dirties the store, deliverWorkerLocked
// must not send to the worker terminal.
func TestStore_DeliverWorkerDoesNotSendAfterAdvanceRunWriteFail(t *testing.T) {
	w := newStoreWorld(t, "No worker send after advanceRunLocked write fail in actTaskLocked")
	past := w.now.Add(-time.Minute)
	pastMS := uint64(past.UnixMilli())

	w.srv.Add(hc.Terminal{
		Address:  "w1",
		Name:     "w1",
		State:    "live",
		Labels:   map[string]string{"sheepdog.project": w.id, "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-w1"},
		Status:   &hc.Status{App: "claude-code", State: "idle"},
		Reported: true,
		StatusMS: &pastMS,
	})
	w.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{
			ID:            "T1",
			Title:         "feat: conflict",
			EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap:    100,
				BaseCommit: w.baseSHA,
				Branch:     "T1-1",
				PR:         7,
				RunID:      "w1",
				Run: &flow.RunRecord{
					ID: "run-w1", Address: "w1", Role: flow.RoleWorker,
					CLI: "claude", State: flow.PhaseWorking, Status: "", // triggers advanceRunLocked in deliverWorkerLocked
					StartedAt: past, LastActivity: past,
				},
				Ready: []flow.Ready{{Head: "c1"}},
				Rounds: []flow.Round{
					{N: 1, Kind: "full", Head: "c1", Verdict: flow.VerdictPass},
					{N: 2, Kind: "full", Head: "c1", Verdict: flow.VerdictPass},
				},
			}},
		}}
	})

	var failedSave atomic.Bool
	w.setWriteHooks(func(path string, s flow.State) error {
		if failedSave.CompareAndSwap(false, true) {
			return errors.New("state save failed in advanceRunLocked")
		}
		return SaveState(path, s)
	}, nil)

	s := w.state()
	var baseTried bool
	facts := flow.Facts{
		Tip:    "c1",
		TipRel: flow.RelSame,
		Worker: flow.Run{State: flow.RunWorking},
		PR: flow.PR{
			Answered:  true,
			State:     flow.PROpen,
			Number:    7,
			Head:      "c1",
			Mergeable: flow.MergeableConflict,
			Checks:    flow.ChecksGreen,
		},
	}

	w.eng.mu.Lock()
	w.eng.actTaskLocked(w.ctx, w.id, &s, 0, facts, &baseTried)
	w.eng.mu.Unlock()

	if !failedSave.Load() {
		t.Fatal("setup bug: advanceRunLocked did not attempt a state save")
	}
	if sends := sendsTo(t, w.srv.Requests(), "w1"); len(sends) != 0 {
		t.Fatalf("sent to worker w1 after advanceRunLocked write failure dirtied store (S2b): %q, want 0 sends", sends)
	}
}

// Test 14 (S2b — r1 review: Carbon Major 3):
// During Report("plan amend") across multiple tasks, if task 1's advanceRunLocked
// inside deliverWorkerLocked fails a write and dirties the store, neither task 1's
// worker nor task 2's worker may receive a send while dirty.
func TestStore_DeliverWorkerDoesNotSendAfterFailedSaveInAdvanceRun(t *testing.T) {
	w := newStoreWorld(t, "No worker send after failed save in deliverWorkerLocked")
	coordTok := "coord-amend-tok"
	past := w.now.Add(-time.Minute)
	pastMS := uint64(past.UnixMilli())

	for _, addr := range []string{"worker1", "worker2"} {
		w.srv.Add(hc.Terminal{
			Address:  addr,
			Name:     addr,
			State:    "live",
			Labels:   map[string]string{"sheepdog.project": w.id, "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-" + addr},
			Status:   &hc.Status{App: "claude-code", State: "idle"},
			Reported: true,
			StatusMS: &pastMS,
		})
	}
	mkTask := func(tid, addr string) flow.Task {
		return flow.Task{
			ID: tid, Title: "feat: " + tid, Brief: "old", ForWhom: "u", IfSkipped: "b",
			Smallest: "s", NotDoing: "n", Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "test: go test ./..."}},
			EstimateLines: 20,
			Attempts: []flow.Attempt{{
				BaseCommit: w.baseSHA,
				RunID:      addr,
				Run: &flow.RunRecord{
					ID: "run-" + addr, Address: addr, Role: flow.RoleWorker,
					CLI: "claude", State: flow.PhaseWorking, Status: "", // triggers advanceRunLocked in deliverWorkerLocked
					StartedAt: past, LastActivity: past,
				},
			}},
		}
	}
	w.edit(func(s *flow.State) {
		s.Project.RunTokenHash = tokenHash(coordTok)
		s.Tasks = []flow.Task{mkTask("T1", "worker1"), mkTask("T2", "worker2")}
	})

	w.restart()
	var writes atomic.Int32
	w.setWriteHooks(func(path string, s flow.State) error {
		if writes.Add(1) == 1 {
			return SaveState(path, s) // 1st call: freshLocked write-back succeeds
		}
		return errors.New("state save failed in advanceRunLocked")
	}, nil)

	amend := json.RawMessage(`{"why":"tighten scope","tasks":[{"id":"T1","brief":"new 1"},{"id":"T2","brief":"new 2"}]}`)
	if err := w.eng.Report(coordTok, "plan amend", amend); err == nil {
		t.Fatal("Report(plan amend) succeeded when writeState failed; want error")
	}
	if s1, s2 := sendsTo(t, w.srv.Requests(), "worker1"), sendsTo(t, w.srv.Requests(), "worker2"); len(s1) != 0 || len(s2) != 0 {
		t.Fatalf("sent to workers after failed write dirtied store (S2b): worker1=%q worker2=%q, want 0 sends", s1, s2)
	}
}

// Test 15 (S2, S2b — r2 review: NeonH Finding 1, J6 path):
// When J10's commitLogLocked fails and dirties the store, passProjectRulesLocked
// must stop immediately rather than re-matching against uncommitted in-memory
// state and falling through into J6 (holding a phantom agents_down@j6 alert in
// ProjectStatus and flushing it to log.jsonl on recovery).
func TestStore_J10CommitFailStopsProjectRules(t *testing.T) {
	w := newStoreWorld(t, "J10 commit failure stops subsequent project rules")
	w.edit(func(s *flow.State) {
		s.Settings.CoordinatorStartsPerHour = 1
		s.Settings.CoordinatorGapMinutes = 1
		s.Project.Acts = []flow.Act{{Key: flow.AskStartCoordinator, Result: "done", At: w.now.Add(-59 * time.Minute)}}
		s.Project.RunID = ""
		s.Project.Run = &flow.RunRecord{
			ID: "coord-r29", Address: "coord-r29", Role: flow.RoleCoordinator, CLI: "claude",
			State: flow.PhaseEnded, Result: flow.EndStuck,
			BlockedSince: w.now.Add(-5 * time.Minute), EndedAt: w.now.Add(-time.Minute),
		}
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: parked", Parked: true, EstimateLines: 20}}
	})

	var failedJ10 atomic.Bool
	w.setWriteHooks(nil, func(path string, l LogLine) error {
		if l.Rule == "J10" && failedJ10.CompareAndSwap(false, true) {
			return errors.New("log write failed on J10")
		}
		return AppendLog(path, l)
	})

	w.pass() // J10 fails -> store dirty; must NOT fall through into J6/J7
	if !failedJ10.Load() {
		t.Fatal("setup bug: J10 did not attempt a commit")
	}
	rows, err := w.eng.ProjectStatus(w.repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == "ALERT" && row.Title == flow.AlertAgentsDown && row.Rule == "J6" {
			t.Fatalf("dirty store after J10 failure fell through to J6 and held agents_down@j6: %+v", row)
		}
	}

	// Advance past the 1-hour start cap and recover cleanly: no phantom J6 alert may be logged.
	w.advanceMinute()
	w.advanceMinute()
	w.pass()

	_, lines := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
	for _, l := range lines {
		if l.Rule == "alert" && slices.Contains(l.Asked, flow.AlertAgentsDown) && strings.Contains(l.Why, "coordinator_starts_per_hour") {
			t.Fatalf("phantom J6 agents_down alert committed after J10 failure: %+v", l)
		}
	}
}

// Test 16 (S2b — r2 review: NeonH Finding 1, J7 path):
// When J10's commitLogLocked fails and the coordinator start cap is not active,
// passProjectRulesLocked must stop immediately rather than falling through into
// J7 (startCoordLocked), which would stop/remove the old coordinator terminal
// and attempt a launch on a dirty store.
func TestStore_J10CommitFailDoesNotFallThroughToJ7(t *testing.T) {
	w := newStoreWorld(t, "J10 commit failure does not fall through to J7")
	w.srv.Add(hc.Terminal{
		Address: "coord-r29",
		Name:    coordName(w.id),
		State:   "live",
		Labels:  testCoordLabels(w.id, "coord-r29"),
	})
	w.edit(func(s *flow.State) {
		s.Project.RunID = ""
		s.Project.Run = &flow.RunRecord{
			ID: "coord-r29", Address: "coord-r29", Role: flow.RoleCoordinator, CLI: "claude",
			State: flow.PhaseEnded, Result: flow.EndStuck,
			BlockedSince: w.now.Add(-5 * time.Minute), EndedAt: w.now.Add(-time.Minute),
		}
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: parked", Parked: true, EstimateLines: 20}}
	})

	var failedJ10 atomic.Bool
	w.setWriteHooks(nil, func(path string, l LogLine) error {
		if l.Rule == "J10" && failedJ10.CompareAndSwap(false, true) {
			return errors.New("log write failed on J10")
		}
		return AppendLog(path, l)
	})

	w.pass() // J10 fails -> store dirty; must NOT fall through into J7
	if !failedJ10.Load() {
		t.Fatal("setup bug: J10 did not attempt a commit")
	}
	if _, err := w.client.Get(w.ctx, "coord-r29"); err != nil {
		t.Fatalf("dirty store after J10 failure fell through to J7 and stopped/removed coord-r29: %v", err)
	}
	if strings.Contains(w.daemonLog.String(), "factory.exec_error") {
		t.Fatalf("dirty store after J10 failure fell through to J7 and logged factory.exec_error:\n%s", w.daemonLog.String())
	}

	// Next healthy pass: J10 commits and J7 cleanly replaces coord-r29 with a fresh coordinator.
	w.advanceMinute()
	w.pass()

	st, lines := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
	if st.Project.RunID == "" || st.Project.RunID == "coord-r29" {
		t.Fatalf("after healthy recovery pass, RunID = %q, want a newly started coordinator", st.Project.RunID)
	}
	var sawJ10, sawJ7 bool
	for _, l := range lines {
		if l.Rule == "J10" {
			sawJ10 = true
		}
		if l.Rule == "J7" && strings.HasPrefix(l.Decision, "started coordinator ") {
			sawJ7 = true
		}
	}
	if !sawJ10 || !sawJ7 {
		t.Fatalf("expected both J10 and J7 started-coordinator lines after recovery (sawJ10=%t, sawJ7=%t); lines=%+v", sawJ10, sawJ7, lines)
	}
}
