package factory

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	herdrsheepdog "github.com/kccarlos/herdr-sheepdog"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
)

type recordedEvent struct {
	source  string
	typ     string
	paneID  string
	payload map[string]any
}

type memLogger struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (m *memLogger) Log(source, typ, paneID string, payload any) {
	var p map[string]any
	if raw, err := json.Marshal(payload); err == nil && len(raw) > 0 && string(raw) != "null" {
		_ = json.Unmarshal(raw, &p)
	}
	m.mu.Lock()
	m.events = append(m.events, recordedEvent{source: source, typ: typ, paneID: paneID, payload: p})
	m.mu.Unlock()
}

func (m *memLogger) snapshot() []recordedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recordedEvent(nil), m.events...)
}

func (m *memLogger) count(typ string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.events {
		if e.typ == typ {
			n++
		}
	}
	return n
}

func cloneEmbeddedFactory(t *testing.T, extraNote string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(herdrsheepdog.Factory, "factory", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := fs.ReadFile(herdrsheepdog.Factory, p)
		if rerr != nil {
			return rerr
		}
		if p == "factory/NOTICE" && extraNote != "" {
			b = append(append([]byte(nil), b...), []byte("\n"+extraNote+"\n")...)
		}
		out[p] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func waitForCondition(t *testing.T, timeout time.Duration, desc string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, desc)
}

func TestHasStartedProjectAndPathIsolation(t *testing.T) {
	t.Run("empty and internal-only files stay off", func(t *testing.T) {
		dir := t.TempDir()
		tasksDir := filepath.Join(dir, "tasks")
		settingsPath := filepath.Join(dir, "settings.json")
		if HasStartedProject(tasksDir, settingsPath) {
			t.Fatal("expected false before any files exist")
		}
		if err := os.MkdirAll(tasksDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Internal metadata and archived tasks alone do not mark a live project.
		for name, body := range map[string]string{
			".seen.json":        `{"demo": {"1": ["added"]}}`,
			".news.json":        `[]`,
			".scheduler.json":   `{"pid": 1}`,
			"demo.archive.json": `{"folder": "/work/demo", "tasks": []}`,
			".autopilot.json":   `{"projects": {"/work/demo": {"on": false}}}`,
		} {
			if err := os.WriteFile(filepath.Join(tasksDir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(settingsPath, []byte(`{"version": 1, "everywhere": {"tasks": {"sync": "cheap"}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if HasStartedProject(tasksDir, settingsPath) {
			t.Fatal("expected false with only internal metadata and global settings")
		}
	})

	t.Run("detects settings project, task store including dot-slug, and enabled autopilot", func(t *testing.T) {
		dir := t.TempDir()
		settingsPath := filepath.Join(dir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(`{"projects": {"/work/app": {"folder": "/work/app"}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if !HasStartedProject("", settingsPath) {
			t.Fatal("expected settings.json project to turn factory on")
		}

		tasksDir := filepath.Join(t.TempDir(), "tasks")
		if err := os.MkdirAll(tasksDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tasksDir, ".config-app.json"), []byte(`{"folder": "/work/.config/app", "tasks": []}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if !HasStartedProject(tasksDir, "") {
			t.Fatal("expected dot-prefixed project task store to turn factory on")
		}

		apDir := filepath.Join(t.TempDir(), "tasks")
		if err := os.MkdirAll(apDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(apDir, ".autopilot.json"), []byte(`{"projects": {"/work/app": {"on": true}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if !HasStartedProject(apDir, "") {
			t.Fatal("expected enabled autopilot project to turn factory on")
		}
	})

	t.Run("ResolveFactoryPaths isolates custom dataDir from real home config", func(t *testing.T) {
		fakeHome := t.TempDir()
		noEnv := func(string) string { return "" }

		defData := filepath.Join(fakeHome, ".local", "share", "sheepdog")
		tasksDir, settingsPath, customStore, customSettings := ResolveFactoryPaths(defData, fakeHome, noEnv)
		if tasksDir != filepath.Join(defData, "tasks") || settingsPath != filepath.Join(fakeHome, ".config", "sheepdog", "settings.json") {
			t.Fatalf("default paths = (%q, %q)", tasksDir, settingsPath)
		}
		if customStore || customSettings {
			t.Fatalf("default paths flagged custom: store=%v settings=%v", customStore, customSettings)
		}

		scratchData := t.TempDir()
		tasksDir, settingsPath, customStore, customSettings = ResolveFactoryPaths(scratchData, fakeHome, noEnv)
		if tasksDir != filepath.Join(scratchData, "tasks") || settingsPath != filepath.Join(scratchData, "settings.json") {
			t.Fatalf("scratch paths = (%q, %q), want inside %q", tasksDir, settingsPath, scratchData)
		}
		if !customStore || !customSettings {
			t.Fatalf("scratch paths must flag custom: store=%v settings=%v", customStore, customSettings)
		}
	})
}

func TestTickSupervisorStaysOffUntilProjectStartedAndStopsWhenCleared(t *testing.T) {
	srv := hctest.New(t)
	dataDir := t.TempDir()
	fakeHome := t.TempDir()
	pinned := PinnedPythonVersion()
	pyHome := createManagedPythonStub(t, dataDir, pinned)

	var pyProbes atomic.Int32
	log := &memLogger{}

	var (
		runMu     sync.Mutex
		lastRun   hc.RunParams
		runCounts int
	)
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) {
		runMu.Lock()
		lastRun = p
		runCounts++
		runMu.Unlock()
	}

	sup := NewTickSupervisor(TickOptions{
		DataDir:      dataDir,
		HCSocket:     srv.Path,
		Home:         fakeHome,
		TempDir:      t.TempDir(),
		Getenv:       func(string) string { return "" },
		Engine:       EngineSource{FS: herdrsheepdog.Factory, Version: "v0.2.0"},
		Log:          log,
		PollInterval: 25 * time.Millisecond,
		MinBackoff:   20 * time.Millisecond,
		MaxBackoff:   80 * time.Millisecond,
		RunCommand: func(_ context.Context, _ string, _ []string, _ ...string) CommandOutput {
			pyProbes.Add(1)
			return CommandOutput{Stdout: pinned + "\n" + pyHome + "\n"}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sup.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Let a couple of polls run with no project started.
	time.Sleep(80 * time.Millisecond)
	if _, err := os.Stat(EnginesDir(dataDir)); !os.IsNotExist(err) {
		t.Fatalf("engine unpacked before any project was started: err=%v", err)
	}
	if got := pyProbes.Load(); got != 0 {
		t.Fatalf("python probed %d times before any project was started", got)
	}
	if reqs := srv.Requests(); len(reqs) != 0 {
		t.Fatalf("hc received %d requests while factory was off: %+v", len(reqs), reqs)
	}

	// Start a project by writing settings.json in the isolated dataDir.
	settingsPath := filepath.Join(dataDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"version":1,"projects":{"/work/demo":{"folder":"/work/demo","name":"demo"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sup.Wake()

	waitForCondition(t, 3*time.Second, "sheepdog-factory-tick to start", func() bool {
		return sup.TerminalAddress() != "" && log.count("factory.tick_started") == 1
	})

	client := hc.NewClient(srv.Path)
	term, err := client.Get(ctx, TickTerminalName)
	if err != nil {
		t.Fatalf("Get(%s): %v", TickTerminalName, err)
	}
	if !term.Keep || term.State != "live" || term.Cwd != fakeHome {
		t.Fatalf("unexpected tick terminal: %+v", term)
	}

	runMu.Lock()
	rp := lastRun
	runs := runCounts
	runMu.Unlock()
	if runs != 1 {
		t.Fatalf("expected 1 terminal.run, got %d", runs)
	}
	if !rp.Keep || !rp.NoParent || !rp.NoNotify {
		t.Fatalf("RunParams flags = Keep:%v NoParent:%v NoNotify:%v, want all true", rp.Keep, rp.NoParent, rp.NoNotify)
	}
	if rp.Labels[LabelRole] != RoleTick || rp.Labels[LabelEngineDigest] == "" {
		t.Fatalf("unexpected labels: %+v", rp.Labels)
	}
	if rp.Env["HERDR_TASK_STORE"] != filepath.Join(dataDir, "tasks") || rp.Env["HERDR_TASK_SETTINGS"] != settingsPath {
		t.Fatalf("unexpected custom store env: %+v", rp.Env)
	}
	if !slices.Contains(rp.Argv, filepath.Join(pyHome, "bin", "python3")) || !slices.Contains(rp.Argv, "scheduler") {
		t.Fatalf("unexpected Argv: %v", rp.Argv)
	}

	// Verify herdrcore does not surface the tick terminal as a worker agent.
	core := herdrcore.New(client, nil)
	snap, err := core.Snapshot(ctx)
	if err != nil {
		t.Fatalf("herdrcore Snapshot: %v", err)
	}
	if len(snap.Agents) != 0 {
		t.Fatalf("tick terminal leaked into herdrcore agent roster: %+v", snap.Agents)
	}

	// Removing all projects stops the managed tick terminal cleanly.
	if err := os.Remove(settingsPath); err != nil {
		t.Fatal(err)
	}
	sup.Wake()
	waitForCondition(t, 3*time.Second, "sheepdog-factory-tick to stop when no projects remain", func() bool {
		return sup.TerminalAddress() == "" && log.count("factory.tick_stopped") == 1
	})
	if _, err := client.Get(ctx, TickTerminalName); err == nil {
		t.Fatal("expected tick terminal to be removed after projects cleared")
	}
}

func TestTickSupervisorAdoptsAcrossRestartAndReplacesOnUpgrade(t *testing.T) {
	srv := hctest.New(t)
	dataDir := t.TempDir()
	fakeHome := t.TempDir()
	pinned := PinnedPythonVersion()
	pyHome := createManagedPythonStub(t, dataDir, pinned)

	tasksDir := filepath.Join(dataDir, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "demo.json"), []byte(`{"folder":"/work/demo","tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	makeSup := func(src EngineSource, log *memLogger) *TickSupervisor {
		return NewTickSupervisor(TickOptions{
			DataDir:      dataDir,
			HCSocket:     srv.Path,
			Home:         fakeHome,
			TempDir:      t.TempDir(),
			Getenv:       func(string) string { return "" },
			Engine:       src,
			Log:          log,
			PollInterval: 30 * time.Millisecond,
			MinBackoff:   15 * time.Millisecond,
			MaxBackoff:   60 * time.Millisecond,
			RunCommand: func(_ context.Context, _ string, _ []string, _ ...string) CommandOutput {
				return CommandOutput{Stdout: pinned + "\n" + pyHome + "\n"}
			},
		})
	}

	// 1. First daemon run starts t1 and then shuts down, leaving t1 running (`hc run --keep`).
	log1 := &memLogger{}
	srcV1 := EngineSource{FS: cloneEmbeddedFactory(t, ""), Version: "v0.2.0"}
	sup1 := makeSup(srcV1, log1)
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan struct{})
	go func() { defer close(done1); sup1.Run(ctx1) }()

	waitForCondition(t, 3*time.Second, "first tick start", func() bool {
		return sup1.TerminalAddress() != ""
	})
	firstAddr := sup1.TerminalAddress()
	cancel1()
	<-done1

	client := hc.NewClient(srv.Path)
	stillLive, err := client.Get(context.Background(), TickTerminalName)
	if err != nil || stillLive.State != "live" || stillLive.Address != firstAddr {
		t.Fatalf("tick terminal did not survive daemon shutdown: term=%+v err=%v", stillLive, err)
	}

	// 2. Second daemon run on the same build adopts firstAddr without restarting it.
	log2 := &memLogger{}
	sup2 := makeSup(srcV1, log2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { defer close(done2); sup2.Run(ctx2) }()

	waitForCondition(t, 3*time.Second, "tick adoption", func() bool {
		return sup2.TerminalAddress() == firstAddr && log2.count("factory.tick_adopted") == 1
	})
	if log2.count("factory.tick_started") != 0 {
		t.Fatalf("adopted terminal should not be restarted: events=%+v", log2.snapshot())
	}
	cancel2()
	<-done2

	// 3. Third daemon run after a binary/engine update replaces firstAddr with the new build.
	log3 := &memLogger{}
	srcV2 := EngineSource{FS: cloneEmbeddedFactory(t, "build-2"), Version: "v0.2.1"}
	sup3 := makeSup(srcV2, log3)
	ctx3, cancel3 := context.WithCancel(context.Background())
	done3 := make(chan struct{})
	go func() { defer close(done3); sup3.Run(ctx3) }()
	defer func() {
		cancel3()
		<-done3
	}()

	waitForCondition(t, 3*time.Second, "upgraded tick replacement", func() bool {
		addr := sup3.TerminalAddress()
		return addr != "" && addr != firstAddr && log3.count("factory.tick_started") == 1
	})
	evs := log3.snapshot()
	if len(evs) == 0 || evs[0].payload["replaced"] != firstAddr || evs[0].payload["reason"] != "version_changed" {
		t.Fatalf("expected version_changed replacement of %s, got %+v", firstAddr, evs)
	}
}

func TestTickSupervisorRecoversFromExitAndShellFallback(t *testing.T) {
	srv := hctest.New(t)
	dataDir := t.TempDir()
	fakeHome := t.TempDir()
	pinned := PinnedPythonVersion()
	pyHome := createManagedPythonStub(t, dataDir, pinned)

	tasksDir := filepath.Join(dataDir, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "demo.json"), []byte(`{"folder":"/work/demo","tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	log := &memLogger{}
	sup := NewTickSupervisor(TickOptions{
		DataDir:      dataDir,
		HCSocket:     srv.Path,
		Home:         fakeHome,
		TempDir:      t.TempDir(),
		Getenv:       func(string) string { return "" },
		Engine:       EngineSource{FS: herdrsheepdog.Factory, Version: "v0.2.0"},
		Log:          log,
		PollInterval: 40 * time.Millisecond,
		MinBackoff:   15 * time.Millisecond,
		MaxBackoff:   60 * time.Millisecond,
		HealthyAfter: 50 * time.Millisecond,
		RunCommand: func(_ context.Context, _ string, _ []string, _ ...string) CommandOutput {
			return CommandOutput{Stdout: pinned + "\n" + pyHome + "\n"}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); sup.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	waitForCondition(t, 3*time.Second, "initial tick terminal", func() bool {
		return sup.TerminalAddress() != "" && srv.Subscribers() >= 1
	})
	addr1 := sup.TerminalAddress()

	// Case A: Tick process exits (terminal becomes finished).
	srv.FinishExit(addr1, "exited", 1)
	waitForCondition(t, 3*time.Second, "restart after process exit", func() bool {
		cur := sup.TerminalAddress()
		return cur != "" && cur != addr1 && log.count("factory.tick_started") >= 2
	})
	addr2 := sup.TerminalAddress()

	// Case B: herdr-core `--keep` replaces a lost program with a login shell (`-zsh`).
	client := hc.NewClient(srv.Path)
	curTerm, err := client.Get(ctx, addr2)
	if err != nil {
		t.Fatal(err)
	}
	curTerm.Argv = []string{"/bin/zsh"}
	curTerm.Foreground = &hc.Foreground{PID: 4242, Name: "zsh", Argv: []string{"-zsh"}, Cwd: fakeHome}
	srv.Add(curTerm)
	srv.Missed()

	waitForCondition(t, 3*time.Second, "restart after shell fallback", func() bool {
		cur := sup.TerminalAddress()
		return cur != "" && cur != addr2 && log.count("factory.tick_started") >= 3
	})
	events := log.snapshot()
	last := events[len(events)-1]
	if last.typ != "factory.tick_started" || last.payload["replaced"] != addr2 || last.payload["reason"] != "shell" {
		t.Fatalf("expected shell replacement of %s, got %+v", addr2, last)
	}
}

func TestTickSupervisorDeduplicatesErrorsWhenHCDown(t *testing.T) {
	dataDir := t.TempDir()
	fakeHome := t.TempDir()
	pinned := PinnedPythonVersion()
	pyHome := createManagedPythonStub(t, dataDir, pinned)

	tasksDir := filepath.Join(dataDir, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "demo.json"), []byte(`{"folder":"/work/demo","tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	sockDir, err := os.MkdirTemp("", "sdt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	closedSock := filepath.Join(sockDir, "missing.sock")

	log := &memLogger{}
	sup := NewTickSupervisor(TickOptions{
		DataDir:      dataDir,
		HCSocket:     closedSock,
		Home:         fakeHome,
		TempDir:      t.TempDir(),
		Getenv:       func(string) string { return "" },
		Engine:       EngineSource{FS: herdrsheepdog.Factory, Version: "v0.2.0"},
		Log:          log,
		PollInterval: 20 * time.Millisecond,
		MinBackoff:   15 * time.Millisecond,
		MaxBackoff:   40 * time.Millisecond,
		HCTimeout:    100 * time.Millisecond,
		RunCommand: func(_ context.Context, _ string, _ []string, _ ...string) CommandOutput {
			return CommandOutput{Stdout: pinned + "\n" + pyHome + "\n"}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); sup.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	waitForCondition(t, 2*time.Second, "initial hc_get error", func() bool {
		return log.count("factory.tick_error") >= 1
	})
	// Let several polls elapse while the socket remains absent; error must stay deduplicated.
	time.Sleep(80 * time.Millisecond)
	if got := log.count("factory.tick_error"); got != 1 {
		evs := log.snapshot()
		var msgs []string
		for _, e := range evs {
			msgs = append(msgs, e.typ+":"+strings.TrimSpace(e.payload["stage"].(string)))
		}
		t.Fatalf("expected deduplicated 1 factory.tick_error, got %d (%v)", got, msgs)
	}
}
