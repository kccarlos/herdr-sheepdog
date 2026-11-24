package factory

// PR 5b.9, TDD step 1: failing tests for the consent_prompt and disk alert
// observers (design.md sections 6, 7.3, 10.1, 15; KB-ENV-4, KB-ENV-5, KB-ENV-9).
//
// Built against the 5b.8 alert contract (persisted episode, once per episode,
// clear on own evidence, stopping alerts hold new starts) via small local
// helpers (cdStatusAlert, cdEngineStatusAlert, cdPersistedAlertBlob,
// cdAlertLogs, cdAttachHCProxy) so this file compiles cleanly on main today
// and merges cleanly with 5b.8.
//
// Uses fakes only: never fills a disk, never revokes access, never triggers a
// real macOS consent dialog, and never touches real $HOME, a running daemon,
// or GitHub.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

type cdProbeMode int

const (
	cdProbeOK cdProbeMode = iota
	cdProbeTimeout
	cdProbeBlank
)

type cdWorld struct {
	t         *testing.T
	ctx       context.Context
	srv       *hctest.Server
	hcOnline  atomic.Bool
	proxySock string
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

	mu                       sync.Mutex
	folderProbe              cdProbeMode
	gitAnswersOnProbeTimeout bool
	tmpProbeTimeout          bool
	probeCalls               int
	gitENOSPC                bool
	storeENOSPC              bool
	stateENOSPC              bool
	projectDisk              DiskReading
	dataDisk                 DiskReading
	diskReadPaths            []string
	diskReadCalls            int
	runs                     []hc.RunParams
}

func newCDWorld(t *testing.T, goal string) *cdWorld {
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

	w := &cdWorld{
		t:           t,
		ctx:         context.Background(),
		srv:         srv,
		paths:       testPaths(t),
		repo:        repo,
		baseSHA:     base,
		now:         time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
		daemonLog:   &bytes.Buffer{},
		stderrBuf:   &bytes.Buffer{},
		projectDisk: DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 40, Known: true},
		dataDisk:    DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
	}
	w.hcOnline.Store(true)
	w.proxySock = attachHCProxy(t, srv.Path, &w.hcOnline)
	w.client = hc.NewClient(w.proxySock)

	srv.OnRun = func(term hc.Terminal, p hc.RunParams) {
		// Temporary protected-folder probe terminal (doctor.go probeFolderInHC shape).
		if p.NoParent && p.NoNotify && len(p.Argv) >= 5 && filepath.Base(p.Argv[0]) == "sh" {
			target := p.Argv[len(p.Argv)-1]
			isRepo := sameOrUnderPath(w.repo, target)
			w.mu.Lock()
			mode := w.folderProbe
			tmpTO := w.tmpProbeTimeout
			if isRepo {
				w.probeCalls++
			}
			w.mu.Unlock()
			if isRepo {
				switch mode {
				case cdProbeOK:
					srv.SetScreen(term.Address, probedDirStart+w.repo+probedDirEnd+"\n")
					srv.FinishExit(term.Address, "exited", 0)
				case cdProbeBlank:
					srv.SetScreen(term.Address, "")
					srv.FinishExit(term.Address, "exited", 0)
				case cdProbeTimeout:
					// Leave terminal running until probe timeout fires.
				}
				return
			}
			if !tmpTO {
				srv.SetScreen(term.Address, probedDirStart+target+probedDirEnd+"\n")
				srv.FinishExit(term.Address, "exited", 0)
			}
			return
		}
		w.mu.Lock()
		w.runs = append(w.runs, p)
		w.mu.Unlock()
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

		// Check if this exec is a protected-folder probe or project-folder call.
		isShellProbe := len(p.Argv) >= 5 && filepath.Base(p.Argv[0]) == "sh" && p.Argv[1] == "-c" && strings.Contains(p.Argv[2], probedDirStart)
		targetsRepo := sameOrUnderPath(w.repo, p.Cwd) || (isShellProbe && sameOrUnderPath(w.repo, p.Argv[len(p.Argv)-1]))

		w.mu.Lock()
		mode := w.folderProbe
		tmpTO := w.tmpProbeTimeout
		gitFull := w.gitENOSPC
		gitAns := w.gitAnswersOnProbeTimeout
		w.mu.Unlock()

		if isShellProbe {
			if targetsRepo {
				w.mu.Lock()
				w.probeCalls++
				w.mu.Unlock()
				switch mode {
				case cdProbeTimeout:
					return hc.ExecResult{TimedOut: true}, nil
				case cdProbeBlank:
					return hc.ExecResult{Code: &zero}, nil
				default:
					return hc.ExecResult{Code: &zero, Stdout: []byte(probedDirStart + w.repo + probedDirEnd + "\n")}, nil
				}
			}
			if tmpTO {
				return hc.ExecResult{TimedOut: true}, nil
			}
			target := p.Argv[len(p.Argv)-1]
			return hc.ExecResult{Code: &zero, Stdout: []byte(probedDirStart + target + probedDirEnd + "\n")}, nil
		}

		if targetsRepo && mode == cdProbeTimeout && !gitAns {
			w.mu.Lock()
			w.probeCalls++
			w.mu.Unlock()
			return hc.ExecResult{TimedOut: true}, nil
		}

		if gitFull && len(argv) >= 2 && filepath.Base(argv[0]) == "git" {
			switch argv[1] {
			case "worktree", "fetch", "commit", "checkout", "config", "push":
				code := 128
				return hc.ExecResult{
					Code:   &code,
					Stderr: []byte("fatal: unable to write file: No space left on device\n"),
				}, nil
			}
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

func (w *cdWorld) newEngine() {
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
		w.diskReadCalls++
		w.diskReadPaths = append(w.diskReadPaths, path)
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
		full := w.storeENOSPC || w.stateENOSPC
		w.mu.Unlock()
		if full {
			return &os.PathError{Op: "write", Path: path, Err: syscall.ENOSPC}
		}
		return SaveState(path, s)
	}
	eng.writeLog = func(path string, l LogLine) error {
		w.mu.Lock()
		full := w.storeENOSPC
		w.mu.Unlock()
		if full {
			return &os.PathError{Op: "write", Path: path, Err: syscall.ENOSPC}
		}
		return AppendLog(path, l)
	}
	w.eng = eng
}

func (w *cdWorld) restart() {
	w.newEngine()
	w.trimNext = true
}

func (w *cdWorld) pass() {
	w.t.Helper()
	trim := w.trimNext
	w.trimNext = false
	w.eng.passProject(w.ctx, w.id, trim)
}

// advanceMinute moves the fake clock one minute.
func (w *cdWorld) advanceMinute() {
	w.mu.Lock()
	w.now = w.now.Add(time.Minute)
	w.mu.Unlock()
}

func (w *cdWorld) setFolderProbe(m cdProbeMode) {
	w.mu.Lock()
	w.folderProbe = m
	w.mu.Unlock()
}

func (w *cdWorld) setGitAnswersOnProbeTimeout(v bool) {
	w.mu.Lock()
	w.gitAnswersOnProbeTimeout = v
	w.mu.Unlock()
}

func (w *cdWorld) setTmpProbeTimeout(v bool) {
	w.mu.Lock()
	w.tmpProbeTimeout = v
	w.mu.Unlock()
}

func (w *cdWorld) setDisks(proj, data DiskReading) {
	w.mu.Lock()
	w.projectDisk = proj
	w.dataDisk = data
	w.mu.Unlock()
}

func (w *cdWorld) setStoreENOSPC(v bool) {
	w.mu.Lock()
	w.storeENOSPC = v
	w.mu.Unlock()
}

func (w *cdWorld) setStateENOSPC(v bool) {
	w.mu.Lock()
	w.stateENOSPC = v
	w.mu.Unlock()
}

func (w *cdWorld) setGitENOSPC(v bool) {
	w.mu.Lock()
	w.gitENOSPC = v
	w.mu.Unlock()
}

func (w *cdWorld) runCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.runs)
}

func (w *cdWorld) getProbeCalls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.probeCalls
}

func (w *cdWorld) getDiskReadCalls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.diskReadCalls
}

func (w *cdWorld) addQueuedTask() {
	w.t.Helper()
	const id = "T1"
	s, err := LoadState(w.paths.State(w.id))
	if err != nil {
		w.t.Fatal(err)
	}
	s.Tasks = append(s.Tasks, flow.Task{
		ID:            id,
		Title:         "feat: queued task " + id,
		Brief:         "do work",
		Outcomes:      []flow.Outcome{{Text: "works", ProvedBy: "cmd: true"}},
		ForWhom:       "owner",
		IfSkipped:     "missed",
		Smallest:      "one file",
		NotDoing:      "extras",
		EstimateLines: 20,
	})
	if err := SaveState(w.paths.State(w.id), s); err != nil {
		w.t.Fatal(err)
	}
}

func (w *cdWorld) setDiskFullPercentOnDisk(pct int) {
	w.t.Helper()
	raw, err := os.ReadFile(w.paths.State(w.id))
	if err != nil {
		w.t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		w.t.Fatal(err)
	}
	settings, _ := doc["settings"].(map[string]any)
	if settings == nil {
		settings = map[string]any{}
	}
	settings["disk_full_percent"] = pct
	doc["settings"] = settings
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(w.paths.State(w.id), append(out, '\n'), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func sameOrUnderPath(root, target string) bool {
	if root == "" || target == "" {
		return false
	}
	cr, ct := filepath.Clean(root), filepath.Clean(target)
	if cr == ct || strings.HasPrefix(ct, cr+string(filepath.Separator)) {
		return true
	}
	if rr, errR := filepath.EvalSymlinks(cr); errR == nil {
		if rt, errT := filepath.EvalSymlinks(ct); errT == nil {
			return rr == rt || strings.HasPrefix(rt, rr+string(filepath.Separator))
		}
	}
	return false
}

func cdUnwrapExecArgv(argv []string) []string {
	if len(argv) >= 5 && filepath.Base(argv[0]) == "sh" && argv[1] == "-c" && argv[3] == "sh" {
		return argv[4:]
	}
	return argv
}

// cdEngineStatusAlert checks live `Engine.ProjectStatus(folder)` (which merges
// active in-memory alerts such as an unpersisted disk ENOSPC alert).
func cdEngineStatusAlert(t *testing.T, eng *Engine, folder, kind string) (TaskStatus, bool) {
	t.Helper()
	rows, err := eng.ProjectStatus(folder)
	if err != nil {
		t.Fatalf("Engine.ProjectStatus(%s): %v", folder, err)
	}
	return findAlertRow(rows, kind)
}

func findAlertRow(rows []TaskStatus, kind string) (TaskStatus, bool) {
	for _, ts := range rows {
		line := FormatStatusLine(ts)
		joined := strings.Join([]string{ts.ID, ts.Title, ts.Stage, ts.Rule, ts.Why, line}, " ")
		if strings.Contains(joined, kind) {
			return ts, true
		}
	}
	return TaskStatus{}, false
}

// Decision 1 & 4 (KB-ENV-4, KB-ENV-5):
// A simulated folder stall (fake hc probe timing out twice consecutively while
// temp dir answers) raises consent_prompt once, holds new starts, keeps probing
// on every pass while active, and clears on the first good probe. A single
// timeout followed by a good probe does not raise consent_prompt.
func TestPR5b9_ConsentPromptTwoTimeoutsRaisesOnceAndClearsOnGoodProbe(t *testing.T) {
	w := newCDWorld(t, "Consent timeout episode")
	w.addQueuedTask()

	// Single timeout followed by a good probe: must NOT raise consent_prompt.
	w.setFolderProbe(cdProbeTimeout)
	w.pass()
	if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "consent_prompt"); ok {
		t.Fatalf("single probe timeout must not raise consent_prompt; got %+v", ts)
	}
	w.advanceMinute()
	w.setFolderProbe(cdProbeOK)
	w.pass()
	if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "consent_prompt"); ok {
		t.Fatalf("good probe after single timeout must not raise consent_prompt; got %+v", ts)
	}

	// Now two consecutive timeouts: first timeout does not raise, second raises.
	w.advanceMinute()
	w.setFolderProbe(cdProbeTimeout)
	w.pass()
	if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "consent_prompt"); ok {
		t.Fatalf("first timeout of new streak must not raise consent_prompt; got %+v", ts)
	}

	runsBeforeRaise := w.runCount()
	w.advanceMinute()
	w.pass()

	ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "consent_prompt")
	if !ok {
		rows, _ := w.eng.ProjectStatus(w.repo)
		t.Fatalf("expected consent_prompt in factory status after two consecutive probe timeouts; got rows=%+v", rows)
	}
	if _, ok := statusAlert(t, w.paths, w.repo, "consent_prompt"); !ok {
		t.Fatalf("expected consent_prompt persisted to offline ProjectStatus")
	}
	if blob := persistedAlertBlob(t, w.paths, w.id); !strings.Contains(blob, "consent_prompt") {
		t.Fatalf("state.json must persist consent_prompt; got %s", blob)
	}
	if !strings.Contains(ts.Why+ts.Title, filepath.Base(w.repo)) && !strings.Contains(ts.Why+ts.Title, "consent") && !strings.Contains(ts.Why+ts.Title, "folder") {
		t.Errorf("consent_prompt status should explain folder/cause: %+v", ts)
	}
	logsAfterRaise := alertEpisodeLogs(t, w.paths, w.repo, "consent_prompt")
	if len(logsAfterRaise) != 1 {
		t.Fatalf("expected exactly 1 consent_prompt log entry on raise, got %d: %+v", len(logsAfterRaise), logsAfterRaise)
	}
	if w.runCount() != runsBeforeRaise {
		t.Fatalf("stopping alert consent_prompt must hold new starts; runs grew from %d to %d", runsBeforeRaise, w.runCount())
	}

	// While active, another pass probes again, holds starts, and does NOT log again.
	probesBefore := w.getProbeCalls()
	w.advanceMinute()
	w.pass()
	if w.getProbeCalls() <= probesBefore {
		t.Fatalf("active consent_prompt must be probed again on every pass (calls stayed %d)", probesBefore)
	}
	if got := alertEpisodeLogs(t, w.paths, w.repo, "consent_prompt"); len(got) != 1 {
		t.Fatalf("consent_prompt must log once per episode, got %d entries: %+v", len(got), got)
	}
	if w.runCount() != runsBeforeRaise {
		t.Fatalf("active consent_prompt must keep holding new starts; runs=%d want %d", w.runCount(), runsBeforeRaise)
	}

	// First good probe clears consent_prompt and allows starts to resume.
	w.advanceMinute()
	w.setFolderProbe(cdProbeOK)
	w.pass()
	if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "consent_prompt"); ok {
		t.Fatalf("first good probe must clear consent_prompt; still active: %+v", ts)
	}
	if blob := persistedAlertBlob(t, w.paths, w.id); strings.Contains(blob, "consent_prompt") {
		t.Fatalf("cleared consent_prompt must be removed from state.json; got %s", blob)
	}
}

// Decision 1 (KB-ENV-4, KB-ENV-5):
// Two consecutive blank probe results (exit 0 with empty output / no probed
// directory) raise consent_prompt once; one good probe clears it.
func TestPR5b9_ConsentPromptTwoBlankResultsRaisesOnceAndClearsOnGoodProbe(t *testing.T) {
	w := newCDWorld(t, "Consent blank probe episode")

	w.setFolderProbe(cdProbeBlank)
	w.pass()
	if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "consent_prompt"); ok {
		t.Fatalf("single blank probe result must not raise consent_prompt; got %+v", ts)
	}

	w.advanceMinute()
	w.pass()
	if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, "consent_prompt"); !ok {
		rows, _ := w.eng.ProjectStatus(w.repo)
		t.Fatalf("expected consent_prompt after two consecutive blank probe results; got rows=%+v", rows)
	}
	if logs := alertEpisodeLogs(t, w.paths, w.repo, "consent_prompt"); len(logs) != 1 {
		t.Fatalf("expected 1 consent_prompt log entry, got %d: %+v", len(logs), logs)
	}

	w.advanceMinute()
	w.setFolderProbe(cdProbeOK)
	w.pass()
	if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "consent_prompt"); ok {
		t.Fatalf("good probe must clear blank-triggered consent_prompt; still active: %+v", ts)
	}
}

// Decision 1 (KB-ENV-4, KB-ENV-9):
// An hc outage or an inconclusive failure (both project folder and temp dir
// timing out) must NEVER raise consent_prompt; once positive evidence appears
// (hc online + temp dir answering while project folder stalls twice), it does.
func TestPR5b9_ConsentPromptHCOutageAndInconclusiveNeverRaiseConsent(t *testing.T) {
	t.Run("hc_outage", func(t *testing.T) {
		w := newCDWorld(t, "HC outage not consent")
		w.hcOnline.Store(false)
		for range 3 {
			w.advanceMinute()
			w.pass()
		}
		if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "consent_prompt"); ok {
			t.Fatalf("hc outage must never raise consent_prompt; got %+v", ts)
		}
		if logs := alertEpisodeLogs(t, w.paths, w.repo, "consent_prompt"); len(logs) != 0 {
			t.Fatalf("hc outage must not log consent_prompt; got %+v", logs)
		}

		// Once hc recovers and the project folder actually stalls twice, consent_prompt raises.
		w.hcOnline.Store(true)
		w.setFolderProbe(cdProbeTimeout)
		w.advanceMinute()
		w.pass()
		w.advanceMinute()
		w.pass()
		if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, "consent_prompt"); !ok {
			t.Fatalf("expected consent_prompt once hc is online and project folder stalls twice")
		}
	})

	t.Run("both_repo_and_tmp_timeout", func(t *testing.T) {
		w := newCDWorld(t, "Both stall inconclusive")
		w.setFolderProbe(cdProbeTimeout)
		w.setTmpProbeTimeout(true)
		for range 3 {
			w.advanceMinute()
			w.pass()
		}
		if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "consent_prompt"); ok {
			t.Fatalf("inconclusive stall (both repo and temp dir timing out) must not raise consent_prompt; got %+v", ts)
		}
		if logs := alertEpisodeLogs(t, w.paths, w.repo, "consent_prompt"); len(logs) != 0 {
			t.Fatalf("inconclusive stall must not log consent_prompt; got %+v", logs)
		}

		// Once temp dir answers while project folder still stalls twice, consent_prompt raises.
		w.setTmpProbeTimeout(false)
		w.advanceMinute()
		w.pass()
		w.advanceMinute()
		w.pass()
		if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, "consent_prompt"); !ok {
			t.Fatalf("expected consent_prompt once temp dir answers and project folder stalls twice")
		}
	})
}

// Decision 2, 3 & 4 (KB-ENV-5):
// A disk-full write (fake store returning ENOSPC) raises disk with the volume
// named, stays visible in memory in `factory status` and is logged to stderr
// even when persisting it fails, holds new starts, persists on the first
// successful write, and clears once writes succeed and readings are under the
// threshold.
func TestPR5b9_DiskENOSPCStaysVisibleInMemoryAndClearsOnRecovery(t *testing.T) {
	w := newCDWorld(t, "Disk ENOSPC in memory")
	w.addQueuedTask()
	w.setDisks(
		DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 40, Known: true},
		DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 95, Known: true},
	)
	w.setStoreENOSPC(true)

	runsBefore := w.runCount()
	w.pass()

	// Alert must be visible in Engine.ProjectStatus even though store writes failed.
	ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk")
	if !ok {
		rows, _ := w.eng.ProjectStatus(w.repo)
		t.Fatalf("expected active in-memory disk alert in Engine.ProjectStatus when store returns ENOSPC; got rows=%+v", rows)
	}
	statusText := strings.Join([]string{ts.ID, ts.Title, ts.Stage, ts.Rule, ts.Why, FormatStatusLine(ts)}, " ")
	if !strings.Contains(statusText, "/Volumes/DataVol") {
		t.Errorf("disk alert in factory status must name the affected volume /Volumes/DataVol; got %q", statusText)
	}
	// Must be logged to stderr when persisting fails.
	stderrText := w.stderrBuf.String()
	if !strings.Contains(stderrText, "disk") || !strings.Contains(stderrText, "/Volumes/DataVol") {
		t.Errorf("unpersisted disk ENOSPC alert must be logged to stderr with volume name; got %q", stderrText)
	}
	// Stopping alert holds new starts.
	if w.runCount() != runsBefore {
		t.Fatalf("disk alert must hold new starts; runs grew from %d to %d", runsBefore, w.runCount())
	}

	// Second pass while store still fails: probed again, still visible, still holds starts.
	readsBefore := w.getDiskReadCalls()
	w.advanceMinute()
	w.pass()
	if w.getDiskReadCalls() <= readsBefore {
		t.Fatalf("active disk alert must be probed again on every pass (reads stayed %d)", readsBefore)
	}
	if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); !ok {
		t.Fatalf("disk alert disappeared while ENOSPC still active")
	}

	// Store becomes writable again while data volume is still >= 90%:
	// alert is persisted on the first successful write without duplicating logs.
	w.setStoreENOSPC(false)
	w.advanceMinute()
	w.pass()
	if _, ok := statusAlert(t, w.paths, w.repo, "disk"); !ok {
		t.Fatalf("disk alert must be persisted to state.json on first successful write")
	}
	if blob := persistedAlertBlob(t, w.paths, w.id); !strings.Contains(blob, "disk") {
		t.Fatalf("state.json must contain persisted disk alert after store recovers; got %s", blob)
	}

	// Recovery: volume reading drops under disk_full_percent and write succeeds -> clears.
	w.setDisks(
		DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 40, Known: true},
		DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 60, Known: true},
	)
	w.advanceMinute()
	w.pass()
	if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); ok {
		t.Fatalf("disk alert must clear once writes succeed and volumes read under threshold; still active: %+v", ts)
	}
	if ts, ok := statusAlert(t, w.paths, w.repo, "disk"); ok {
		t.Fatalf("disk alert must be cleared from persisted ProjectStatus; still active: %+v", ts)
	}
}

// Decision 2 (KB-ENV-5):
// A git write returning ENOSPC raises disk naming the project volume, holds
// new starts, and clears when writes succeed and readings are under threshold.
func TestPR5b9_DiskGitENOSPCRaisesWithProjectVolumeAndClearsOnRecovery(t *testing.T) {
	w := newCDWorld(t, "Git write ENOSPC")
	w.addQueuedTask()
	w.setDisks(
		DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 75, Known: true},
		DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
	)
	w.setGitENOSPC(true)

	w.pass()

	ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk")
	if !ok {
		rows, _ := w.eng.ProjectStatus(w.repo)
		t.Fatalf("expected disk alert after git write ENOSPC; got rows=%+v", rows)
	}
	statusText := strings.Join([]string{ts.ID, ts.Title, ts.Stage, ts.Rule, ts.Why, FormatStatusLine(ts)}, " ")
	if !strings.Contains(statusText, "/Volumes/ProjectVol") {
		t.Errorf("git ENOSPC disk alert must name project volume /Volumes/ProjectVol; got %q", statusText)
	}

	// Recovery: git writes succeed and both volumes read under threshold.
	w.setGitENOSPC(false)
	w.advanceMinute()
	w.pass()
	if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); ok {
		t.Fatalf("disk alert must clear after git ENOSPC recovers and readings are under threshold; got %+v", ts)
	}
}

// Decision 2 & 4 (KB-ENV-5, KB-ENV-9):
// Volume reading at or over disk_full_percent raises disk naming the full
// volume (project or data dir); under it clears; unknown never raises or clears;
// custom disk_full_percent setting is honored.
func TestPR5b9_DiskVolumeThresholdBothVolumesUnknownAndCustomSetting(t *testing.T) {
	t.Run("project_volume_at_default_90_and_clears_at_89", func(t *testing.T) {
		w := newCDWorld(t, "Project volume 90%")
		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 90, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
		)
		runsBefore := w.runCount()
		w.pass()

		ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk")
		if !ok {
			rows, _ := w.eng.ProjectStatus(w.repo)
			t.Fatalf("project volume at 90%% (>= default 90) must raise disk; got rows=%+v", rows)
		}
		statusText := strings.Join([]string{ts.ID, ts.Title, ts.Stage, ts.Rule, ts.Why, FormatStatusLine(ts)}, " ")
		if !strings.Contains(statusText, "/Volumes/ProjectVol") {
			t.Errorf("disk alert must name /Volumes/ProjectVol; got %q", statusText)
		}
		if w.runCount() != runsBefore {
			t.Fatalf("disk alert must hold new starts; runs=%d want %d", w.runCount(), runsBefore)
		}

		// Drops to 89% -> clears.
		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 89, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
		)
		w.advanceMinute()
		w.pass()
		if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); ok {
			t.Fatalf("project volume at 89%% (< 90) must clear disk; still active: %+v", ts)
		}
	})

	t.Run("data_volume_over_threshold_named", func(t *testing.T) {
		w := newCDWorld(t, "Data volume 93%")
		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 30, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 93, Known: true},
		)
		w.pass()

		ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk")
		if !ok {
			rows, _ := w.eng.ProjectStatus(w.repo)
			t.Fatalf("data volume at 93%% must raise disk; got rows=%+v", rows)
		}
		statusText := strings.Join([]string{ts.ID, ts.Title, ts.Stage, ts.Rule, ts.Why, FormatStatusLine(ts)}, " ")
		if !strings.Contains(statusText, "/Volumes/DataVol") {
			t.Errorf("disk alert must name /Volumes/DataVol; got %q", statusText)
		}

		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 30, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 80, Known: true},
		)
		w.advanceMinute()
		w.pass()
		if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); ok {
			t.Fatalf("data volume at 80%% must clear disk; still active: %+v", ts)
		}
	})

	t.Run("unknown_reading_never_raises_or_clears", func(t *testing.T) {
		w := newCDWorld(t, "Unknown disk reading")
		w.setDisks(
			DiskReading{Known: false, Unknown: "statfs permission denied"},
			DiskReading{Known: false, Unknown: "statfs permission denied"},
		)
		w.pass()
		if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); ok {
			t.Fatalf("unknown volume reading must never raise disk; got %+v", ts)
		}

		// Raise via positive 95% reading, then switch to unknown -> must stay active.
		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 95, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
		)
		w.advanceMinute()
		w.pass()
		if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); !ok {
			t.Fatalf("95%% reading must raise disk")
		}

		w.setDisks(
			DiskReading{Known: false, Unknown: "statfs timeout"},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
		)
		w.advanceMinute()
		w.pass()
		if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); !ok {
			t.Fatalf("unknown volume reading must never clear an active disk alert")
		}
	})

	t.Run("custom_disk_full_percent", func(t *testing.T) {
		w := newCDWorld(t, "Custom disk_full_percent 75")
		w.setDiskFullPercentOnDisk(75)
		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 75, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
		)
		w.pass()
		if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); !ok {
			t.Fatalf("75%% with disk_full_percent=75 must raise disk")
		}

		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 74, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
		)
		w.advanceMinute()
		w.pass()
		if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); ok {
			t.Fatalf("74%% with disk_full_percent=75 must clear disk; got %+v", ts)
		}
	})
}

// Decision 4 (KB-ENV-5):
// Restart: an active consent_prompt or disk alert survives a daemon restart
// without repeating its log line, continues holding new starts and probing on
// every pass, and clears on recovery after restart.
func TestPR5b9_RestartPreservesActiveConsentAndDiskWithoutRepeatingLog(t *testing.T) {
	t.Run("consent_prompt_survives_restart", func(t *testing.T) {
		w := newCDWorld(t, "Restart consent alert")
		w.setFolderProbe(cdProbeTimeout)
		w.pass()
		w.advanceMinute()
		w.pass()

		if _, ok := statusAlert(t, w.paths, w.repo, "consent_prompt"); !ok {
			t.Fatalf("expected persisted consent_prompt before restart")
		}
		if logs := alertEpisodeLogs(t, w.paths, w.repo, "consent_prompt"); len(logs) != 1 {
			t.Fatalf("expected 1 consent_prompt log before restart, got %d", len(logs))
		}

		// Simulate daemon restart while folder still stalls.
		w.restart()
		runsAfterRestart := w.runCount()
		probesBefore := w.getProbeCalls()
		w.advanceMinute()
		w.pass()
		w.advanceMinute()
		w.pass()

		if _, ok := statusAlert(t, w.paths, w.repo, "consent_prompt"); !ok {
			t.Fatalf("consent_prompt must survive restart")
		}
		if w.getProbeCalls() <= probesBefore {
			t.Fatalf("consent_prompt must keep probing after restart")
		}
		if w.runCount() != runsAfterRestart {
			t.Fatalf("consent_prompt must keep holding starts after restart; runs=%d want %d", w.runCount(), runsAfterRestart)
		}
		if logs := alertEpisodeLogs(t, w.paths, w.repo, "consent_prompt"); len(logs) != 1 {
			t.Fatalf("active consent_prompt must not repeat its log line across restart; got %d entries: %+v", len(logs), logs)
		}

		// Recover after restart.
		w.advanceMinute()
		w.setFolderProbe(cdProbeOK)
		w.pass()
		if ts, ok := statusAlert(t, w.paths, w.repo, "consent_prompt"); ok {
			t.Fatalf("consent_prompt must clear on good probe after restart; got %+v", ts)
		}
	})

	t.Run("disk_survives_restart", func(t *testing.T) {
		w := newCDWorld(t, "Restart disk alert")
		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 94, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
		)
		w.pass()

		if _, ok := statusAlert(t, w.paths, w.repo, "disk"); !ok {
			t.Fatalf("expected persisted disk alert before restart")
		}
		if logs := alertEpisodeLogs(t, w.paths, w.repo, "disk"); len(logs) != 1 {
			t.Fatalf("expected 1 disk log before restart, got %d", len(logs))
		}

		// Simulate daemon restart while volume stays >= 90%.
		w.restart()
		runsAfterRestart := w.runCount()
		readsBefore := w.getDiskReadCalls()
		w.advanceMinute()
		w.pass()
		w.advanceMinute()
		w.pass()

		if _, ok := statusAlert(t, w.paths, w.repo, "disk"); !ok {
			t.Fatalf("disk alert must survive restart")
		}
		if w.getDiskReadCalls() <= readsBefore {
			t.Fatalf("disk alert must keep probing after restart")
		}
		if w.runCount() != runsAfterRestart {
			t.Fatalf("disk alert must keep holding starts after restart; runs=%d want %d", w.runCount(), runsAfterRestart)
		}
		if logs := alertEpisodeLogs(t, w.paths, w.repo, "disk"); len(logs) != 1 {
			t.Fatalf("active disk alert must not repeat its log line across restart; got %d entries: %+v", len(logs), logs)
		}

		// Recover after restart.
		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 50, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
		)
		w.advanceMinute()
		w.pass()
		if ts, ok := statusAlert(t, w.paths, w.repo, "disk"); ok {
			t.Fatalf("disk alert must clear on recovery after restart; got %+v", ts)
		}
	})
}

// PR 5b.9 round 2 (Major #1):
// An ENOSPC from a store write in Start, Stop, or Report (outside a pass)
// raises the disk alert even when both volumes read below disk_full_percent,
// and good Start/Stop/Report writes never drop an in-memory alert — they
// persist it to state.json on the first good write.
func TestPR5b9R2_OutOfPassStoreENOSPCDoesNotLoseOrDropDiskAlert(t *testing.T) {
	t.Run("enospc_in_start_stop_report_raises_disk", func(t *testing.T) {
		for _, op := range []string{"start", "stop", "report"} {
			t.Run(op, func(t *testing.T) {
				w := newCDWorld(t, "Out-of-pass ENOSPC "+op)
				w.setDisks(
					DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 85, Known: true},
					DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
				)
				w.pass()

				var tok string
				if op == "report" {
					w.mu.Lock()
					if len(w.runs) == 0 {
						w.mu.Unlock()
						t.Fatal("expected coordinator run with SHEEPDOG_RUN_TOKEN")
					}
					tok = w.runs[0].Env["SHEEPDOG_RUN_TOKEN"]
					w.mu.Unlock()
				}

				w.setStoreENOSPC(true)
				var opErr error
				switch op {
				case "start":
					opErr = w.eng.Start(w.repo, "Updated goal under store ENOSPC", 4)
				case "stop":
					opErr = w.eng.Stop(w.repo)
				case "report":
					opErr = w.eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":20,"title":"feat: t",`+testPlanJSON+`}]}`))
				}
				if opErr == nil {
					t.Fatalf("%s unexpectedly succeeded under store ENOSPC", op)
				}
				// Clear storeENOSPC before the next pass so the pass makes no
				// failing store write of its own: the disk alert must still be
				// raised from the out-of-pass ENOSPC in Start/Stop/Report.
				w.setStoreENOSPC(false)

				w.advanceMinute()
				w.pass()

				ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk")
				if !ok {
					rows, _ := w.eng.ProjectStatus(w.repo)
					t.Fatalf("store ENOSPC during %s must raise disk alert; got rows=%+v", op, rows)
				}
				statusText := strings.Join([]string{ts.ID, ts.Title, ts.Stage, ts.Rule, ts.Why, FormatStatusLine(ts)}, " ")
				if !strings.Contains(statusText, "/Volumes/DataVol") {
					t.Errorf("disk alert after %s ENOSPC must name /Volumes/DataVol; got %q", op, statusText)
				}
			})
		}
	})

	t.Run("first_good_start_stop_report_persists_in_memory_alert", func(t *testing.T) {
		for _, op := range []string{"start", "stop", "report"} {
			t.Run(op, func(t *testing.T) {
				w := newCDWorld(t, "Good write persists alert "+op)
				var tok string
				if op == "report" {
					w.pass()
					w.mu.Lock()
					if len(w.runs) == 0 {
						w.mu.Unlock()
						t.Fatal("expected coordinator run with SHEEPDOG_RUN_TOKEN")
					}
					tok = w.runs[0].Env["SHEEPDOG_RUN_TOKEN"]
					w.mu.Unlock()
					w.advanceMinute()
				}

				w.setDisks(
					DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 40, Known: true},
					DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 95, Known: true},
				)
				w.setStoreENOSPC(true)
				w.pass()

				if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); !ok {
					t.Fatal("expected in-memory disk alert after pass ENOSPC")
				}
				if blob := persistedAlertBlob(t, w.paths, w.id); strings.Contains(blob, "disk") {
					t.Fatalf("expected disk alert to be unpersisted before first good write; got %s", blob)
				}

				w.setStoreENOSPC(false)
				var opErr error
				switch op {
				case "start":
					opErr = w.eng.Start(w.repo, "Updated goal after space freed", 4)
				case "stop":
					opErr = w.eng.Stop(w.repo)
				case "report":
					opErr = w.eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":20,"title":"feat: t",`+testPlanJSON+`}]}`))
				}
				if opErr != nil {
					t.Fatalf("%s after space freed failed: %v", op, opErr)
				}

				if op == "stop" {
					// Stop drops the project's held alerts and writes no raise line for a stopped project.
					if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); ok {
						t.Fatalf("Stop must drop held in-memory disk alert; got %+v", ts)
					}
					if blob := persistedAlertBlob(t, w.paths, w.id); strings.Contains(blob, "disk") {
						t.Fatalf("Stop must not persist unlogged disk alert to state.json; got %s", blob)
					}
					if raises := diskRaiseLogs(t, w.paths, w.repo); len(raises) != 0 {
						t.Fatalf("Stop must not log a disk raise on a stopped project; got %+v", raises)
					}
					return
				}

				// Start/Report do not merge held alerts into state.json without their raise line;
				// the held alert stays visible in memory and commits on the next pass.
				if blob := persistedAlertBlob(t, w.paths, w.id); strings.Contains(blob, "disk") {
					t.Fatalf("good %s must not write held disk alert into state.json without a raise line; state.json=%s", op, blob)
				}
				if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); !ok {
					t.Fatalf("good %s dropped held in-memory disk alert before next pass", op)
				}

				w.advanceMinute()
				w.pass()
				if blob := persistedAlertBlob(t, w.paths, w.id); !strings.Contains(blob, "disk") {
					t.Fatalf("next pass after good %s failed to flush held disk alert to state.json; state.json=%s", op, blob)
				}
				if _, ok := statusAlert(t, w.paths, w.repo, "disk"); !ok {
					t.Fatalf("offline ProjectStatus missing disk alert after next pass following %s", op)
				}
				if raises := diskRaiseLogs(t, w.paths, w.repo); len(raises) != 1 {
					t.Fatalf("expected 1 disk raise log line after flush following %s, got %d: %+v", op, len(raises), raises)
				}

				w.restart()
				w.advanceMinute()
				w.pass()
				if _, ok := statusAlert(t, w.paths, w.repo, "disk"); !ok {
					t.Fatalf("disk alert lost across restart after %s", op)
				}
			})
		}
	})
}

func diskRaiseLogs(t *testing.T, paths Paths, repo string) []LogLine {
	t.Helper()
	var raises []LogLine
	for _, l := range alertEpisodeLogs(t, paths, repo, "disk") {
		if !strings.Contains(strings.ToLower(l.Decision), "clear") {
			raises = append(raises, l)
		}
	}
	return raises
}

// PR 5b.9 round 2 (Major #2):
// A disk episode that starts under store ENOSPC and recovers in one step
// between two passes logs its raise once and shows as cleared; a partial commit
// (AppendLog succeeded, SaveState failed with ENOSPC) does not log the raise
// twice on retry.
func TestPR5b9R2_DiskOneStepRecoveryAndPartialCommitLogging(t *testing.T) {
	t.Run("one_step_recovery_logs_raise_once_and_clears", func(t *testing.T) {
		w := newCDWorld(t, "One-step disk recovery")
		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 40, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 95, Known: true},
		)
		w.setStoreENOSPC(true)
		w.pass()

		if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); !ok {
			t.Fatal("expected in-memory disk alert on pass 1")
		}
		if got := alertEpisodeLogs(t, w.paths, w.repo, "disk"); len(got) != 0 {
			t.Fatalf("expected 0 persisted disk log lines while store had ENOSPC, got %+v", got)
		}

		// Single-step recovery between passes: store becomes writable and volume drops below limit.
		w.setStoreENOSPC(false)
		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 40, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 60, Known: true},
		)
		w.advanceMinute()
		w.pass()

		if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); ok {
			t.Fatalf("disk alert must clear on one-step recovery; still in live status: %+v", ts)
		}
		if ts, ok := statusAlert(t, w.paths, w.repo, "disk"); ok {
			t.Fatalf("disk alert must clear in offline status; got %+v", ts)
		}
		if blob := persistedAlertBlob(t, w.paths, w.id); strings.Contains(blob, "disk") {
			t.Fatalf("cleared disk alert must not remain in state.json; got %s", blob)
		}

		if raises := diskRaiseLogs(t, w.paths, w.repo); len(raises) != 1 {
			t.Fatalf("one-step recovered disk episode must log its raise once; got %d raise entries (%+v)", len(raises), raises)
		}

		w.advanceMinute()
		w.pass()
		if raises := diskRaiseLogs(t, w.paths, w.repo); len(raises) != 1 {
			t.Fatalf("subsequent pass must not re-log raise; got %d raise entries (%+v)", len(raises), raises)
		}
	})

	t.Run("partial_commit_does_not_log_raise_twice_on_retry", func(t *testing.T) {
		w := newCDWorld(t, "Partial commit disk alert")
		w.setDisks(
			DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 40, Known: true},
			DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 95, Known: true},
		)
		w.setStateENOSPC(true)
		w.pass()

		if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, "disk"); !ok {
			t.Fatal("expected in-memory disk alert after partial commit")
		}
		if blob := persistedAlertBlob(t, w.paths, w.id); strings.Contains(blob, "disk") {
			t.Fatalf("expected state.json not to hold disk after SaveState ENOSPC; got %s", blob)
		}

		// Store becomes writable while volume stays >= 90%: retry persists without duplicating log.
		w.setStateENOSPC(false)
		w.advanceMinute()
		w.pass()

		if _, ok := statusAlert(t, w.paths, w.repo, "disk"); !ok {
			t.Fatal("expected disk alert persisted to state.json on retry")
		}
		if blob := persistedAlertBlob(t, w.paths, w.id); !strings.Contains(blob, "disk") {
			t.Fatalf("state.json missing persisted disk alert after retry; got %s", blob)
		}

		if raises := diskRaiseLogs(t, w.paths, w.repo); len(raises) != 1 {
			t.Fatalf("partial commit retry logged disk raise %d times, want 1: %+v", len(raises), raises)
		}

		w.restart()
		w.advanceMinute()
		w.pass()
		if _, ok := statusAlert(t, w.paths, w.repo, "disk"); !ok {
			t.Fatal("disk alert disappeared after restart")
		}
		if raises := diskRaiseLogs(t, w.paths, w.repo); len(raises) != 1 {
			t.Fatalf("restart after partial commit retry logged raise %d times, want 1: %+v", len(raises), raises)
		}
	})
}

// Trial finding 2 (KB-ENV-4):
// Folder probe times out on four passes in a row while temp dir answers and
// the engine's own git calls on the project folder answer every time: no
// consent_prompt is raised or persisted, starts are not held, no log line
// names consent_prompt, and exactly one factory.volume_slow event is logged.
func TestTrial_ConsentSlowFolderWhileGitAnswersDoesNotRaise(t *testing.T) {
	w := newCDWorld(t, "Slow folder while git answers")
	w.addQueuedTask()
	w.setFolderProbe(cdProbeTimeout)
	w.setGitAnswersOnProbeTimeout(true)

	for range 4 {
		w.advanceMinute()
		w.pass()
	}

	if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, flow.AlertConsentPrompt); ok {
		t.Fatalf("slow folder while git answers must not raise consent_prompt in live status; got %+v", ts)
	}
	if ts, ok := statusAlert(t, w.paths, w.repo, flow.AlertConsentPrompt); ok {
		t.Fatalf("slow folder while git answers must not raise consent_prompt in persisted status; got %+v", ts)
	}
	if blob := persistedAlertBlob(t, w.paths, w.id); strings.Contains(blob, flow.AlertConsentPrompt) {
		t.Fatalf("slow folder while git answers must not persist consent_prompt in state.json; got %s", blob)
	}
	if w.runCount() == 0 {
		t.Fatal("slow folder while git answers must not hold starts")
	}
	if logs := alertEpisodeLogs(t, w.paths, w.repo, flow.AlertConsentPrompt); len(logs) != 0 {
		t.Fatalf("slow folder while git answers must not log consent_prompt; got %+v", logs)
	}
	if n := strings.Count(w.daemonLog.String(), "factory.volume_slow"); n != 1 {
		t.Fatalf("expected exactly 1 factory.volume_slow daemon log event per streak, got %d:\n%s", n, w.daemonLog.String())
	}
}

// Review round 1 non-blocking coverage note 3 (KB-ENV-4):
// An "ok" folder probe between two slow streaks resets the streak so the second
// slow streak logs factory.volume_slow once again (and still raises no alert).
func TestTrial_ConsentSlowStreakResetsAfterOKProbe(t *testing.T) {
	w := newCDWorld(t, "Slow folder streak resets after ok")
	w.setFolderProbe(cdProbeTimeout)
	w.setGitAnswersOnProbeTimeout(true)

	// First slow streak (3 passes): logs factory.volume_slow once.
	for range 3 {
		w.advanceMinute()
		w.pass()
	}
	if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, flow.AlertConsentPrompt); ok {
		t.Fatalf("first slow streak must not raise consent_prompt; got %+v", ts)
	}
	if n := strings.Count(w.daemonLog.String(), "factory.volume_slow"); n != 1 {
		t.Fatalf("expected 1 factory.volume_slow after first streak, got %d:\n%s", n, w.daemonLog.String())
	}

	// Intervening "ok" probe resets the slow streak.
	w.setFolderProbe(cdProbeOK)
	w.advanceMinute()
	w.pass()
	if n := strings.Count(w.daemonLog.String(), "factory.volume_slow"); n != 1 {
		t.Fatalf("ok probe must not log factory.volume_slow; got %d:\n%s", n, w.daemonLog.String())
	}

	// Second slow streak: pass 1 does not log yet; pass 2 logs a second
	// factory.volume_slow; pass 3 does not duplicate it.
	w.setFolderProbe(cdProbeTimeout)
	w.advanceMinute()
	w.pass()
	if n := strings.Count(w.daemonLog.String(), "factory.volume_slow"); n != 1 {
		t.Fatalf("first timeout of new streak must not log factory.volume_slow yet; got %d:\n%s", n, w.daemonLog.String())
	}
	w.advanceMinute()
	w.pass()
	if n := strings.Count(w.daemonLog.String(), "factory.volume_slow"); n != 2 {
		t.Fatalf("second timeout of new streak must log a second factory.volume_slow; got %d:\n%s", n, w.daemonLog.String())
	}
	w.advanceMinute()
	w.pass()
	if n := strings.Count(w.daemonLog.String(), "factory.volume_slow"); n != 2 {
		t.Fatalf("third timeout of new streak must not duplicate factory.volume_slow; got %d:\n%s", n, w.daemonLog.String())
	}
	if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, flow.AlertConsentPrompt); ok {
		t.Fatalf("second slow streak while git answers must not raise consent_prompt; got %+v", ts)
	}
	if ts, ok := statusAlert(t, w.paths, w.repo, flow.AlertConsentPrompt); ok {
		t.Fatalf("second slow streak while git answers must not persist consent_prompt; got %+v", ts)
	}
}

// Trial finding 2 (KB-ENV-5, KB-ENV-6):
// When both the folder probe and the engine's git calls on the project folder
// time out (a real consent prompt or stalled volume), consent_prompt is raised
// within three passes, holds new starts, and logs once.
func TestTrial_ConsentBlockedFolderStillRaises(t *testing.T) {
	w := newCDWorld(t, "Blocked folder raises consent_prompt")
	w.addQueuedTask()
	w.setFolderProbe(cdProbeTimeout) // fake git on w.repo also times out

	w.pass()
	runsAfterPass1 := w.runCount()
	w.advanceMinute()
	w.pass()
	w.advanceMinute()
	w.pass()

	if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, flow.AlertConsentPrompt); !ok {
		rows, _ := w.eng.ProjectStatus(w.repo)
		t.Fatalf("expected consent_prompt within three passes for blocked folder; got rows=%+v", rows)
	}
	if _, ok := statusAlert(t, w.paths, w.repo, flow.AlertConsentPrompt); !ok {
		t.Fatal("expected consent_prompt persisted in state.json")
	}
	if w.runCount() != runsAfterPass1 {
		t.Fatalf("consent_prompt must hold new starts; runs grew from %d to %d", runsAfterPass1, w.runCount())
	}
	if logs := alertEpisodeLogs(t, w.paths, w.repo, flow.AlertConsentPrompt); len(logs) != 1 {
		t.Fatalf("expected 1 consent_prompt raise log line, got %d: %+v", len(logs), logs)
	}
}

// Trial finding 2 (KB-ENV-4, KB-ENV-5):
// Probes time out while git answers (nothing raised, one factory.volume_slow),
// then a git call on the folder times out; the next pass raises consent_prompt.
func TestTrial_ConsentRaisesOnFirstGitStallAfterSlowProbes(t *testing.T) {
	w := newCDWorld(t, "Slow probes then git stall")
	w.setFolderProbe(cdProbeTimeout)
	w.setGitAnswersOnProbeTimeout(true)

	for range 3 {
		w.advanceMinute()
		w.pass()
	}
	if ts, ok := cdEngineStatusAlert(t, w.eng, w.repo, flow.AlertConsentPrompt); ok {
		t.Fatalf("expected no consent_prompt while git still answers; got %+v", ts)
	}
	if n := strings.Count(w.daemonLog.String(), "factory.volume_slow"); n != 1 {
		t.Fatalf("expected 1 factory.volume_slow event during slow streak, got %d:\n%s", n, w.daemonLog.String())
	}

	// Now git calls on the folder stall too: the next pass raises consent_prompt.
	w.setGitAnswersOnProbeTimeout(false)
	w.advanceMinute()
	w.pass()

	if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, flow.AlertConsentPrompt); !ok {
		rows, _ := w.eng.ProjectStatus(w.repo)
		t.Fatalf("expected consent_prompt on pass where git stalls after slow probes; got rows=%+v", rows)
	}
	if _, ok := statusAlert(t, w.paths, w.repo, flow.AlertConsentPrompt); !ok {
		t.Fatal("expected consent_prompt persisted in state.json")
	}
	if logs := alertEpisodeLogs(t, w.paths, w.repo, flow.AlertConsentPrompt); len(logs) != 1 {
		t.Fatalf("expected 1 consent_prompt raise log line, got %d: %+v", len(logs), logs)
	}
}

// Trial finding 2 (KB-ENV-4, KB-ENV-5):
// The timeout cause says the folder did not answer and names both a stalling
// volume and the macOS prompt; the fix includes the Settings step and notes
// that it clears by itself when no prompt is showing. The blank cause and fix
// are unchanged.
func TestTrial_ConsentTimeoutCauseNamesBothCauses(t *testing.T) {
	home, _ := os.UserHomeDir()

	t.Run("timeout", func(t *testing.T) {
		w := newCDWorld(t, "Consent timeout cause wording")
		w.setFolderProbe(cdProbeTimeout)
		w.pass()
		w.advanceMinute()
		w.pass()

		a := findStoredAlert(t, w.paths, w.id, flow.AlertConsentPrompt)
		prompt, baseFix := consentPrompt(home, w.repo)
		for _, want := range []string{"did not answer", "stalling", prompt} {
			if !strings.Contains(a.Cause, want) {
				t.Errorf("timeout cause = %q, want substring %q", a.Cause, want)
			}
		}
		for _, want := range []string{baseFix, "clears by itself"} {
			if !strings.Contains(a.Fix, want) {
				t.Errorf("timeout fix = %q, want substring %q", a.Fix, want)
			}
		}
	})

	t.Run("blank_unchanged", func(t *testing.T) {
		w := newCDWorld(t, "Consent blank cause unchanged")
		w.setFolderProbe(cdProbeBlank)
		w.pass()
		w.advanceMinute()
		w.pass()

		a := findStoredAlert(t, w.paths, w.id, flow.AlertConsentPrompt)
		prompt, baseFix := consentPrompt(home, w.repo)
		if !strings.Contains(a.Cause, "returned blank twice in herdr-core") || !strings.Contains(a.Cause, prompt) {
			t.Errorf("blank cause = %q, want unchanged blank wording naming %q", a.Cause, prompt)
		}
		if a.Fix != baseFix {
			t.Errorf("blank fix = %q, want unchanged %q", a.Fix, baseFix)
		}
	})
}
