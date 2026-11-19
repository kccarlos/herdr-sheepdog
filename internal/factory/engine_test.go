package factory

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// TestStopWithoutHC: in mock mode (no herdr-core) Stop still records the
// stop instead of failing.
func TestStopWithoutHC(t *testing.T) {
	repo := initGitRepo(t)
	e := New(Paths{DataDir: t.TempDir()}, nil)
	if err := e.Start(repo, "a goal"); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(repo); err != nil {
		t.Fatalf("Stop without hc: %v", err)
	}
}

func initGitRepoWithRemote(t *testing.T) (repo, baseCommit string) {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	repo = filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "init", "--initial-branch=main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	gitIn(t, repo, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/demo\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "go.mod")
	gitIn(t, repo, "commit", "-m", "chore: init")
	gitIn(t, repo, "push", "-u", "origin", "main")
	return repo, gitIn(t, repo, "rev-parse", "HEAD")
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestEngineTaskLoopN11ThenN23OnceAndReady checks KB-LCH-1, KB-LCH-2, KB-LCH-4,
// KB-LCH-8, KB-LCH-9, KB-LCH-11, KB-LCH-24, KB-COO-12 and KB-RUN-58:
// a planned task lands on N11 then N23, executes each once (acts[]), starts the
// worker in its worktree with its own token, accepts `ready`, logs later asks
// (N26) as "not built yet" without executing them, and Stop cleans up all runs.
func TestEngineTaskLoopN11ThenN23OnceAndReady(t *testing.T) {
	srv := hctest.New(t)
	var runs []hc.RunParams
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { runs = append(runs, p) }
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, baseSHA := initGitRepoWithRemote(t)
	ctx := context.Background()

	var logBuf bytes.Buffer
	eng := New(paths, client)
	eng.Log = logging.NewJSONLLogger(&logBuf)
	eng.Bin = "/tmp/sheepdog-bin"
	eng.RunEnv = map[string]string{"SHEEPDOG_WEB_PORT": "17777"}

	if err := eng.Start(repo, "Add Greet helper."); err != nil {
		t.Fatalf("Start: %v", err)
	}
	id := eng.projectDirs()[0].Name()

	// Pass 1: coordinator J7 starts; no tasks yet.
	eng.passProject(ctx, id, false)
	if len(runs) != 1 {
		t.Fatalf("expected coordinator start, got %d runs", len(runs))
	}
	coordTok := runs[0].Env["SHEEPDOG_RUN_TOKEN"]
	plan := `{"tasks":[{"id":"T1","title":"feat: add Greet helper","brief":"Write Greet(name).","outcomes":["go test ./... passes"],"after":[]}]}`
	if err := eng.Report(coordTok, "plan apply", []byte(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}

	// Pass 2: task T1 matches N11 (new_attempt) and creates the worktree.
	eng.passProject(ctx, id, false)
	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Tasks[0].Attempts) != 1 {
		t.Fatalf("after N11 attempts = %+v", st.Tasks[0].Attempts)
	}
	att := st.Tasks[0].Attempts[0]
	wantWT, _ := filepath.EvalSymlinks(paths.Worktree(id, "T1", 1))
	if att.BaseCommit != baseSHA || att.Branch != "task/T1-feat-add-greet-helper" || att.Worktree != wantWT {
		t.Fatalf("attempt after N11 = %+v, want base=%s wt=%s", att, baseSHA, wantWT)
	}
	if flow.Count(att.Acts, flow.AskNewAttempt) != 1 || !flow.Done(att.Acts, flow.AskNewAttempt) {
		t.Fatalf("N11 acts = %+v", att.Acts)
	}
	if len(runs) != 1 {
		t.Fatalf("N11 should not start worker yet, runs=%d", len(runs))
	}

	// Pass 3: task T1 matches N23 (start_worker) and launches Claude worker in worktree.
	eng.passProject(ctx, id, false)
	if len(runs) != 2 {
		t.Fatalf("expected worker start on N23, got %d runs", len(runs))
	}
	wRun := runs[1]
	workerTok := wRun.Env["SHEEPDOG_RUN_TOKEN"]
	if workerTok == "" || workerTok == coordTok || wRun.Env["SHEEPDOG_WEB_PORT"] != "17777" {
		t.Fatalf("worker env = %v", wRun.Env)
	}
	if wRun.Cwd != att.Worktree || wRun.Labels["sheepdog.role"] != flow.RoleWorker || wRun.Labels["sheepdog.task"] != "T1" {
		t.Fatalf("worker RunParams = %+v", wRun)
	}
	wArgv := strings.Join(wRun.Argv, " ")
	for _, want := range []string{
		"DISABLE_AUTOUPDATER=1", "claude", "--model sonnet", "--effort medium",
		"--permission-mode bypassPermissions", `{"skipDangerousModePermissionPrompt":true}`,
		"feat: add Greet helper", "Write Greet(name).", "go test ./... passes",
		"/tmp/sheepdog-bin factory report ready", "do not push",
	} {
		if !strings.Contains(wArgv, want) {
			t.Errorf("worker argv/prompt missing %q:\n%s", want, wArgv)
		}
	}
	cfgRaw, err := os.ReadFile(paths.ClaudeConfig)
	if err != nil || !strings.Contains(string(cfgRaw), att.Worktree) {
		t.Fatalf("worktree not pre-trusted in ClaudeConfig: err=%v body=%s", err, cfgRaw)
	}

	st, _ = LoadState(paths.State(id))
	seqAfterN23 := st.Seq
	att = st.Tasks[0].Attempts[0]
	if flow.Count(att.Acts, flow.AskNewAttempt) != 1 || flow.Count(att.Acts, flow.AskStartWorker) != 1 || !flow.Done(att.Acts, flow.AskStartWorker) {
		t.Fatalf("acts after N23 = %+v", att.Acts)
	}

	// Extra passes while worker is working execute nothing new and write no duplicate commits.
	eng.passProject(ctx, id, false)
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if st.Seq != seqAfterN23 || len(runs) != 2 {
		t.Fatalf("repeated N23 passes mutated seq (%d -> %d) or runs (%d)", seqAfterN23, st.Seq, len(runs))
	}

	// Simulate a crash after StartRun before RunID and start_worker act were committed:
	// the next pass adopts the live worker without spawning another, and its token still works.
	savedRunID := st.Tasks[0].Attempts[0].RunID
	st.Tasks[0].Attempts[0].RunID = ""
	st.Tasks[0].Attempts[0].Acts = st.Tasks[0].Attempts[0].Acts[:1] // keep only new_attempt
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if len(runs) != 2 || st.Tasks[0].Attempts[0].RunID != savedRunID {
		t.Fatalf("worker crash adoption: runs=%d runID=%q want %q", len(runs), st.Tasks[0].Attempts[0].RunID, savedRunID)
	}

	// Worker commits in worktree and reports ready; next pass lands on N26 ("not built yet").
	if err := os.WriteFile(filepath.Join(att.Worktree, "greet.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, att.Worktree, "add", "greet.go")
	gitIn(t, att.Worktree, "commit", "-m", "feat: add greet.go")
	c1 := gitIn(t, att.Worktree, "rev-parse", "HEAD")

	if err := eng.Report(workerTok, "ready", []byte(`{"head":"`+c1+`"}`)); err != nil {
		t.Fatalf("Report ready: %v", err)
	}
	eng.passProject(ctx, id, false)
	eng.passProject(ctx, id, false) // second pass on N26 does not re-log or re-ask

	st, _ = LoadState(paths.State(id))
	att = st.Tasks[0].Attempts[0]
	if flow.H(att) != c1 || flow.Count(att.Acts, "push_and_open_pr@"+c1) != 1 {
		t.Fatalf("after ready+N26: H=%s acts=%+v", flow.H(att), att.Acts)
	}
	logRaw, _ := os.ReadFile(paths.Log(id))
	for _, want := range []string{`"rule":"N11"`, `"rule":"N23"`, `"rule":"ready"`, `"rule":"N26"`, `"not built yet"`} {
		if !strings.Contains(string(logRaw), want) {
			t.Errorf("log.jsonl missing %s:\n%s", want, logRaw)
		}
	}
	if !strings.Contains(logBuf.String(), "factory.not_built_yet") {
		t.Errorf("daemon log missing factory.not_built_yet:\n%s", logBuf.String())
	}
	stateRaw, _ := os.ReadFile(paths.State(id))
	for where, text := range map[string]string{"argv": wArgv, "log.jsonl": string(logRaw), "daemon log": logBuf.String(), "state.json": string(stateRaw)} {
		if strings.Contains(text, workerTok) {
			t.Fatalf("worker token leaked into %s", where)
		}
	}

	// Stop kills both coordinator and worker terminals and clears worker token hash.
	if err := eng.Stop(repo); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if list, err := client.List(ctx, true); err != nil || len(list.Terminals) != 0 {
		t.Fatalf("terminals left alive after Stop: %+v (%v)", list.Terminals, err)
	}
	if afterStop, _ := os.ReadFile(paths.State(id)); strings.Contains(string(afterStop), "run_token") {
		t.Fatalf("Stop left a run token hash in state.json:\n%s", afterStop)
	}
}

// TestTerminalNamesFitHC: names built from plan task ids (capitals, spaces,
// long ids) still pass herdr-core's name rule (seen live: "worker-…-T1-1"
// was refused), and shortened or sanitized names do not collide across tasks
// sharing a 32-char prefix, attempts, or case variants.
func TestTerminalNamesFitHC(t *testing.T) {
	ok := regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,31}$`)
	shared := strings.Repeat("a", 32)
	a1 := workerName("proj-4cd6ea6a", shared+"-alpha", 1)
	b1 := workerName("proj-4cd6ea6a", shared+"-bravo", 1)
	a2 := workerName("proj-4cd6ea6a", shared+"-alpha", 2)
	cap1 := workerName("proj-4cd6ea6a", "SDF-Alpha", 1)
	low1 := workerName("proj-4cd6ea6a", "sdf-alpha", 1)
	for _, n := range []string{
		workerName("proj-4cd6ea6a", "T1", 1),
		workerName("proj-4cd6ea6a", "Feat Add/Greeting", 12),
		workerName("proj-4cd6ea6a", strings.Repeat("X", 60), 3),
		coordName("proj-4cd6ea6a"),
		a1, b1, a2, cap1, low1,
	} {
		if !ok.MatchString(n) {
			t.Errorf("terminal name %q breaks herdr-core's rule", n)
		}
	}
	for _, n := range []string{a1, b1, a2} {
		if !strings.HasPrefix(n, "worker-4cd6ea6a-aaaaaaa-") {
			t.Fatalf("shortened name %q lost readable prefix", n)
		}
	}
	if a1 == b1 || a1 == a2 || b1 == a2 || cap1 == low1 {
		t.Fatalf("terminal name collision: a1=%q b1=%q a2=%q cap1=%q low1=%q", a1, b1, a2, cap1, low1)
	}
}

// TestEngineFailedExecutorsAskOnceAndDoNotSpin checks item 1 (KB-RUN-1):
// engine pass commits never wake the loop early, and a failed NewWorktree or
// StartRun (coordinator or worker) records a failed act so many passes ask once
// and write a bounded number of log lines.
func TestEngineFailedExecutorsAskOnceAndDoNotSpin(t *testing.T) {
	ctx := context.Background()
	runFor := func(e *Engine, d time.Duration) {
		rctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			e.Run(rctx)
		}()
		time.Sleep(d)
		cancel()
		<-done
	}

	// 1. Failing NewWorktree (repo has no remote) over many passes asks N11 once.
	srv1 := hctest.New(t)
	var runs1 []hc.RunParams
	srv1.OnRun = func(_ hc.Terminal, p hc.RunParams) { runs1 = append(runs1, p) }
	eng1 := New(testPaths(t), hc.NewClient(srv1.Path))
	eng1.PassInterval = 10 * time.Millisecond
	repoNoRemote := initGitRepo(t)
	if err := eng1.Start(repoNoRemote, "Goal with no remote"); err != nil {
		t.Fatal(err)
	}
	id1 := eng1.projectDirs()[0].Name()
	eng1.passProject(ctx, id1, false)
	if err := eng1.Report(runs1[0].Env["SHEEPDOG_RUN_TOKEN"], "plan apply", []byte(`{"tasks":[{"id":"t1","title":"feat: x"}]}`)); err != nil {
		t.Fatal(err)
	}
	runFor(eng1, 80*time.Millisecond)
	log1, err := os.ReadFile(eng1.Paths.Log(id1))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(log1)), "\n"); len(lines) != 4 {
		t.Fatalf("failing NewWorktree wrote %d log lines (want 4: start, J7, plan apply, 1 N11):\n%s", len(lines), log1)
	}
	if st1, err := LoadState(eng1.Paths.State(id1)); err != nil || flow.Count(st1.Tasks[0].Acts, flow.AskNewAttempt) != 1 || flow.Done(st1.Tasks[0].Acts, flow.AskNewAttempt) {
		t.Fatalf("failing NewWorktree task acts = %+v (err=%v)", st1.Tasks[0].Acts, err)
	}

	// 2. Failing coordinator StartRun over many passes asks J7 once.
	srv2 := hctest.New(t)
	p2 := testPaths(t)
	p2.ClaudeConfig = "/nonexistent-sheepdog-test-guard/.claude.json"
	eng2 := New(p2, hc.NewClient(srv2.Path))
	eng2.PassInterval = 10 * time.Millisecond
	if err := eng2.Start(initGitRepo(t), "Goal with failing coord"); err != nil {
		t.Fatal(err)
	}
	id2 := eng2.projectDirs()[0].Name()
	runFor(eng2, 80*time.Millisecond)
	log2, _ := os.ReadFile(eng2.Paths.Log(id2))
	if lines := strings.Split(strings.TrimSpace(string(log2)), "\n"); len(lines) != 2 {
		t.Fatalf("failing coordinator StartRun wrote %d log lines (want 2: start, 1 J7):\n%s", len(lines), log2)
	}
	if st2, err := LoadState(eng2.Paths.State(id2)); err != nil || flow.Count(st2.Project.Acts, "coordinator") != 1 || flow.Done(st2.Project.Acts, "coordinator") {
		t.Fatalf("failing coordinator project acts = %+v (err=%v)", st2.Project.Acts, err)
	}

	// 3. Pass commit does not self-wake Run, and failing worker StartRun over many passes asks N23 once.
	srv3 := hctest.New(t)
	runs3Ch := make(chan hc.RunParams, 8)
	srv3.OnRun = func(_ hc.Terminal, p hc.RunParams) { runs3Ch <- p }
	eng3 := New(testPaths(t), hc.NewClient(srv3.Path))
	eng3.PassInterval = time.Hour
	repo3, _ := initGitRepoWithRemote(t)
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		eng3.Run(rctx)
	}()
	if err := eng3.Start(repo3, "Goal"); err != nil {
		t.Fatal(err)
	}
	var coordRun hc.RunParams
	select {
	case coordRun = <-runs3Ch:
	case <-time.After(2 * time.Second):
		t.Fatal("coordinator did not start")
	}
	id3 := eng3.projectDirs()[0].Name()
	if err := eng3.Report(coordRun.Env["SHEEPDOG_RUN_TOKEN"], "plan apply", []byte(`{"tasks":[{"id":"t1","title":"feat: x"}]}`)); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if st, err := LoadState(eng3.Paths.State(id3)); err == nil && len(st.Tasks) == 1 && len(st.Tasks[0].Attempts) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("N11 did not create attempt after plan apply")
		}
	}
	time.Sleep(40 * time.Millisecond)
	cancel()
	<-done
	if len(runs3Ch) != 0 {
		t.Fatalf("engine pass commit woke loop early and started worker without tick: extra runs=%d", len(runs3Ch))
	}

	eng3.Paths.ClaudeConfig = "/nonexistent-sheepdog-test-guard/.claude.json"
	eng3.PassInterval = 10 * time.Millisecond
	reqsBefore := len(srv3.Requests())
	runFor(eng3, 80*time.Millisecond)
	if got := len(srv3.Requests()) - reqsBefore; got != 2 { // 1 launchRunLocked RunState + 1 StartRun Get, never retried
		t.Fatalf("failing worker StartRun sent %d hc requests across many passes, want 2 (1 try)", got)
	}
	log3, _ := os.ReadFile(eng3.Paths.Log(id3))
	if lines := strings.Split(strings.TrimSpace(string(log3)), "\n"); len(lines) != 5 {
		t.Fatalf("failing worker StartRun wrote %d log lines (want 5):\n%s", len(lines), log3)
	}
	if st3, err := LoadState(eng3.Paths.State(id3)); err != nil || flow.Count(st3.Tasks[0].Attempts[0].Acts, flow.AskStartWorker) != 1 || flow.Done(st3.Tasks[0].Attempts[0].Acts, flow.AskStartWorker) {
		t.Fatalf("failing worker attempt acts = %+v (err=%v)", st3.Tasks[0].Attempts[0].Acts, err)
	}
}

// TestAttemptWorktreePathPerAttemptAndLockGitTimeout checks item 6: worktree
// directories are keyed per attempt (worktrees/<task>-<n>), and git calls made
// while holding Engine.mu carry a context deadline rather than context.Background().
func TestAttemptWorktreePathPerAttemptAndLockGitTimeout(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	eng := New(paths, nil)
	if err := eng.Start(repo, "Goal"); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "t1"
	st.Tasks = []flow.Task{{ID: "t1", Title: "feat: add x", Attempts: []flow.Attempt{}}}
	if _, _, err := eng.execNewAttempt(ctx, id, &st, 0); err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	if _, _, err := eng.execNewAttempt(ctx, id, &st, 0); err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	w1, w2 := st.Tasks[0].Attempts[0].Worktree, st.Tasks[0].Attempts[1].Worktree
	if !strings.HasSuffix(w1, filepath.Join("worktrees", "t1-1")) || !strings.HasSuffix(w2, filepath.Join("worktrees", "t1-2")) || w1 == w2 {
		t.Fatalf("attempt worktrees = (%q, %q), want .../worktrees/t1-1 and .../worktrees/t1-2", w1, w2)
	}
}

func TestEngineLockGitCallsUseTimeout(t *testing.T) {
	old := lockGitTimeout
	lockGitTimeout = 60 * time.Millisecond
	t.Cleanup(func() { lockGitTimeout = old })

	paths := testPaths(t)
	repo, baseSHA := initGitRepoWithRemote(t)
	eng := New(paths, nil)
	const projID = "proj-timeout"
	st := flow.State{
		Seq:      1,
		Project:  flow.Project{ID: projID, Folder: repo, Started: true, RunID: "t1"},
		Settings: flow.DefaultSettings(),
		Tasks: []flow.Task{{
			ID: "t1", Title: "feat: x",
			Attempts: []flow.Attempt{{
				BaseCommit: baseSHA, Branch: "task/t1-feat-x", Worktree: repo,
				RunTokenHash: tokenHash("tok-w"),
			}},
		}},
	}
	if err := SaveState(paths.State(projID), st); err != nil {
		t.Fatal(err)
	}

	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte("#!/bin/sh\nexec sleep 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	start := time.Now()
	err := eng.Report("tok-w", "ready", []byte(`{"head":"`+baseSHA+`"}`))
	if elapsed := time.Since(start); err == nil || elapsed > time.Second {
		t.Fatalf("Report ready under hung git took %s (err=%v), want lock timeout", elapsed, err)
	}
	start = time.Now()
	eng.passProject(context.Background(), projID, false)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("passProject under hung git took %s, want lock timeout", elapsed)
	}
}
