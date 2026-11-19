package factory

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
	return Paths{DataDir: t.TempDir(), ClaudeConfig: filepath.Join(t.TempDir(), ".claude.json")}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return repo
}

// TestEngineLoopStartsOneCoordinatorAndStopsCleanly checks KB-COO-1, KB-COO-4,
// KB-LCH-2, KB-LCH-4, KB-LCH-9, KB-LCH-11 and KB-TSK-2 against hctest and a temp DataDir.
func TestEngineLoopStartsOneCoordinatorAndStopsCleanly(t *testing.T) {
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo := initGitRepo(t)

	eng := New(paths, client)
	eng.PassInterval = 15 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		eng.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Before Start: the loop does nothing and touches neither disk nor hc.
	time.Sleep(40 * time.Millisecond)
	if len(srv.Requests()) != 0 {
		t.Fatalf("idle engine sent hc requests: %+v", srv.Requests())
	}
	if _, err := os.Stat(filepath.Join(paths.DataDir, "factory")); !os.IsNotExist(err) {
		t.Fatalf("idle engine created factory dir: %v", err)
	}

	const goal = "Add a greeting helper with a unit test."
	if err := eng.Start(repo, goal); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the coordinator terminal to appear in hctest.
	var coord hc.Terminal
	deadline := time.Now().Add(2 * time.Second)
	for {
		list, err := client.List(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Terminals) == 1 {
			coord = list.Terminals[0]
			break
		}
		if len(list.Terminals) > 1 {
			t.Fatalf("expected 1 coordinator, got %d", len(list.Terminals))
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for coordinator start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify git config project id, cwd, argv and prompt.
	out, err := exec.Command("git", "-C", repo, "config", "--local", "--get", "sheepdog.projectId").Output()
	if err != nil {
		t.Fatalf("git config sheepdog.projectId: %v", err)
	}
	projID := strings.TrimSpace(string(out))
	if projID == "" {
		t.Fatal("empty sheepdog.projectId")
	}
	realRepo, _ := filepath.EvalSymlinks(repo)
	if coord.Cwd != realRepo {
		t.Fatalf("coordinator cwd = %q, want %q", coord.Cwd, realRepo)
	}
	joinedArgv := strings.Join(coord.Argv, " ")
	for _, want := range []string{"DISABLE_AUTOUPDATER=1", "claude", "--model opus", "--effort medium", "--permission-mode bypassPermissions", `{"skipDangerousModePermissionPrompt":true}`, goal, realRepo} {
		if !strings.Contains(joinedArgv, want) {
			t.Errorf("argv %q missing %q", joinedArgv, want)
		}
	}

	// Let several PassInterval ticks run; still exactly one coordinator.
	srv.SetStatus(coord.Address, &hc.Status{App: "claude-code", State: "idle"})
	time.Sleep(60 * time.Millisecond)
	if list, err := client.List(ctx, true); err != nil || len(list.Terminals) != 1 {
		t.Fatalf("after ticks terminals = %+v, err = %v", list.Terminals, err)
	}

	// Stop with empty target stops the single started project and removes its terminal.
	if err := eng.Stop(""); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if list, err := client.List(ctx, true); err != nil || len(list.Terminals) != 0 {
		t.Fatalf("after Stop terminals = %+v, err = %v", list.Terminals, err)
	}

	// Let several ticks pass after Stop; no coordinator is started.
	time.Sleep(60 * time.Millisecond)
	if list, err := client.List(ctx, true); err != nil || len(list.Terminals) != 0 {
		t.Fatalf("after Stop ticks terminals = %+v, err = %v", list.Terminals, err)
	}

	st, err := LoadState(paths.State(projID))
	if err != nil {
		t.Fatal(err)
	}
	if st.Project.Started || st.Project.RunID != "" || st.Seq != 3 {
		t.Fatalf("final state = %+v", st)
	}
	logBytes, err := os.ReadFile(paths.Log(projID))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n"); len(lines) != 3 {
		t.Fatalf("expected 3 log commits (start, J7, stop), got %d:\n%s", len(lines), string(logBytes))
	}
}

// TestEngineStopKillsUnrecordedCoordinator checks Neon M2: if a crash or failed
// SaveState left s.Project.RunID empty while coord-<id> is live, Stop still kills
// and removes coord-<id>.
func TestEngineStopKillsUnrecordedCoordinator(t *testing.T) {
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo := initGitRepo(t)
	ctx := context.Background()

	eng := New(paths, client)
	if err := eng.Start(repo, "Ship slice 1."); err != nil {
		t.Fatal(err)
	}
	_, projID, err := world.RepoProject(ctx, repo, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := world.StartRun(ctx, client, world.RunSpec{
		Name:         "coord-" + strings.TrimPrefix(projID, "proj-"),
		Cwd:          repo,
		ClaudeConfig: paths.ClaudeConfig,
		Argv:         world.ClaudeArgv(flow.RoleCoordinator, "opus", "medium", "bypassPermissions"),
	}); err != nil {
		t.Fatal(err)
	}

	if err := eng.Stop(""); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if list, err := client.List(ctx, true); err != nil || len(list.Terminals) != 0 {
		t.Fatalf("coordinator with empty RunID left alive after Stop: %+v (err=%v)", list.Terminals, err)
	}
}

// TestEngineCrashAdoptTrimAndOutage checks Neon M8 (a), (b), (c) and (d).
func TestEngineCrashAdoptTrimAndOutage(t *testing.T) {
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo := initGitRepo(t)
	ctx := context.Background()

	runEngine := func(e *Engine) func() {
		rctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			e.Run(rctx)
		}()
		return func() {
			cancel()
			<-done
		}
	}

	eng1 := New(paths, client)
	eng1.PassInterval = 15 * time.Millisecond
	stop1 := runEngine(eng1)
	if err := eng1.Start(repo, "Goal A"); err != nil {
		t.Fatal(err)
	}
	_, projID, err := world.RepoProject(ctx, repo, false)
	if err != nil {
		t.Fatal(err)
	}
	var coordAddr string
	deadline := time.Now().Add(2 * time.Second)
	for {
		st, err := LoadState(paths.State(projID))
		if err == nil && st.Project.RunID != "" {
			coordAddr = st.Project.RunID
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for RunID persistence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop1() // simulate daemon crash

	// (c) Append an uncommitted log line past state.Seq plus a torn line.
	if err := AppendLog(paths.Log(projID), LogLine{Seq: 99, Project: projID, By: "engine", Rule: "uncommitted"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(paths.Log(projID), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"seq":100`)
	_ = f.Close()

	// (a) Second Engine on same Paths and hctest with RunID persisted: trims log and spawns no duplicate.
	eng2 := New(paths, client)
	eng2.PassInterval = 15 * time.Millisecond
	stop2 := runEngine(eng2)
	time.Sleep(60 * time.Millisecond)
	stop2()

	if list, err := client.List(ctx, true); err != nil || len(list.Terminals) != 1 || list.Terminals[0].Address != coordAddr {
		t.Fatalf("(a) after restart with RunID persisted: terminals=%+v err=%v", list.Terminals, err)
	}
	logRaw, err := os.ReadFile(paths.Log(projID))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(logRaw)), "\n"); len(lines) != 2 || strings.Contains(string(logRaw), "uncommitted") {
		t.Fatalf("(c) TrimLog did not drop uncommitted lines:\n%s", string(logRaw))
	}

	// (b) RunID empty while coord-<id> is live: adopted, not duplicated.
	st, err := LoadState(paths.State(projID))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = ""
	if err := SaveState(paths.State(projID), st); err != nil {
		t.Fatal(err)
	}
	eng3 := New(paths, client)
	eng3.PassInterval = 15 * time.Millisecond
	stop3 := runEngine(eng3)
	deadline = time.Now().Add(2 * time.Second)
	for {
		cur, err := LoadState(paths.State(projID))
		if err == nil && cur.Project.RunID == coordAddr {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("(b) expected RunID to adopt %q, got %+v", coordAddr, cur)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop3()
	if list, err := client.List(ctx, true); err != nil || len(list.Terminals) != 1 {
		t.Fatalf("(b) expected 1 adopted coordinator, got %+v (%v)", list.Terminals, err)
	}

	// (d) hc unreachable (closed socket): Start does not clear persisted RunID.
	srv.Close()
	eng4 := New(paths, client)
	if err := eng4.Start(repo, "Goal B during hc outage"); err != nil {
		t.Fatalf("(d) Start during hc outage: %v", err)
	}
	stAfter, err := LoadState(paths.State(projID))
	if err != nil || stAfter.Project.RunID != coordAddr {
		t.Fatalf("(d) RunID cleared during hc outage: state=%+v err=%v", stAfter, err)
	}
}

// TestPassProjectLogsErrorsAndSkipsDeadRunState checks item 4: passProject logs
// TrimLog and commit errors via Engine.Log and skips the dead RunState RPC when
// RunID is already set in slice 1.
func TestPassProjectLogsErrorsAndSkipsDeadRunState(t *testing.T) {
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo := initGitRepo(t)
	ctx := context.Background()

	var logBuf bytes.Buffer
	eng := New(paths, client)
	eng.Log = logging.NewJSONLLogger(&logBuf)

	const projID = "proj-deadbeef"
	realRepo, _ := filepath.EvalSymlinks(repo)
	st := flow.State{
		Seq:      1,
		Project:  flow.Project{ID: projID, Folder: realRepo, Goal: "Goal", Started: true, RunID: "t1"},
		Settings: flow.DefaultSettings(),
	}
	if err := SaveState(paths.State(projID), st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Log(projID), []byte("{corrupt\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reqsBefore := len(srv.Requests())
	eng.passProject(ctx, projID, true)
	if len(srv.Requests()) != reqsBefore {
		t.Fatalf("expected no hc RPC in passProject while RunID is set, got %+v", srv.Requests()[reqsBefore:])
	}
	if !strings.Contains(logBuf.String(), "factory.trim_error") {
		t.Fatalf("expected factory.trim_error in engine log, got %q", logBuf.String())
	}

	// Force commitLocked failure by replacing log.jsonl with a directory while RunID is empty.
	st.Project.RunID = ""
	if err := SaveState(paths.State(projID), st); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.Log(projID)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Log(projID), 0o700); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, projID, false)
	if !strings.Contains(logBuf.String(), "factory.commit_error") {
		t.Fatalf("expected factory.commit_error in engine log, got %q", logBuf.String())
	}
}
