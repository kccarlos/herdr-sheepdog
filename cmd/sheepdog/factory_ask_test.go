package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func testTokenSHA(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// TestFactoryReportAskAndAnswerCLIEndToEnd exercises `sheepdog factory report ask`
// from a committed worker worktree through the HTTP report route into Engine,
// checks State.Asks and the coordinator wake, then runs `sheepdog factory answer`
// as the coordinator and checks the worker terminal receives the delivered answer
// (KB-ASK-4, KB-ASK-6, KB-ASK-10; design.md 7.1, 7.2).
func TestFactoryReportAskAndAnswerCLIEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	srv := hctest.New(t)
	srv.OnExec = hctest.GitExec
	t.Setenv("HC_SOCKET", srv.Path)
	client := hc.NewClient(srv.Path)
	paths := factory.Paths{DataDir: t.TempDir(), ClaudeConfig: filepath.Join(t.TempDir(), ".claude.json")}
	eng := factory.New(paths, client)

	repo := filepath.Join(t.TempDir(), "repo")
	wt := filepath.Join(t.TempDir(), "wt")
	gitRun := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	if b, err := exec.Command("git", "init", "--initial-branch=main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, b)
	}
	_ = os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o600)
	gitRun(repo, "add", "main.go")
	gitRun(repo, "commit", "-m", "chore: init")
	baseSHA := gitRun(repo, "rev-parse", "HEAD")
	gitRun(repo, "worktree", "add", "-b", "task/proj-cli-T1-1", wt)
	_ = os.WriteFile(filepath.Join(wt, "feature.go"), []byte("package main\n// committed work\n"), 0o600)
	gitRun(wt, "add", "feature.go")
	gitRun(wt, "commit", "-m", "feat: partial work")

	idleAt := uint64(time.Now().Add(-time.Minute).UnixMilli())
	srv.Add(hc.Terminal{
		Address: "coord-1", Name: "coord-1", State: "live", CreatedMS: 10, StatusMS: &idleAt,
		Labels: map[string]string{"sheepdog.project": "proj-cli", "sheepdog.role": flow.RoleCoordinator, "sheepdog.run": "run-coord"},
		Status: &hc.Status{App: "claude-code", State: "idle"},
	})
	srv.Add(hc.Terminal{
		Address: "worker-1", Name: "worker-1", State: "live", CreatedMS: 20, StatusMS: &idleAt,
		Labels: map[string]string{"sheepdog.project": "proj-cli", "sheepdog.role": flow.RoleWorker, "sheepdog.task": "T1", "sheepdog.run": "run-worker"},
		Status: &hc.Status{App: "claude-code", State: "idle"},
	})

	st := flow.State{
		Seq:      1,
		Settings: flow.DefaultSettings(),
		Project: flow.Project{
			ID: "proj-cli", Folder: repo, Goal: "Build feature", Started: true,
			RunID: "coord-1", RunTokenHash: testTokenSHA("tok-coord-cli"),
			Run: &flow.RunRecord{ID: "run-coord", Address: "coord-1", CreatedMS: 10, Role: flow.RoleCoordinator, CLI: "claude", State: flow.PhaseIdle, Status: "idle", StatusReported: true},
		},
		Tasks: []flow.Task{{
			ID: "T1", Title: "feat: add cache", EstimateLines: 50,
			Outcomes: []flow.Outcome{{Text: "cache works", ProvedBy: "cmd: go test ./..."}},
			Attempts: []flow.Attempt{{
				BaseCommit: baseSHA, Branch: "task/proj-cli-T1-1", Worktree: wt, SizeCap: 100,
				RunID: "worker-1", RunTokenHash: testTokenSHA("tok-worker-cli"),
				Run:     &flow.RunRecord{ID: "run-worker", Address: "worker-1", CreatedMS: 20, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseIdle, Status: "idle", StatusReported: true},
				Ready:   []flow.Ready{},
				Rounds:  []flow.Round{},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done"}},
			}},
		}},
	}
	if err := factory.SaveState(paths.State("proj-cli"), st); err != nil {
		t.Fatal(err)
	}

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Token string          `json:"token"`
			Verb  string          `json:"verb"`
			Args  json.RawMessage `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := eng.Report(req.Token, req.Verb, req.Args); err != nil {
			var ref *factory.Refusal
			code := http.StatusInternalServerError
			if errors.As(err, &ref) {
				code = http.StatusBadRequest
			}
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	t.Cleanup(httpSrv.Close)
	_, port, _ := net.SplitHostPort(httpSrv.Listener.Addr().String())
	t.Setenv("SHEEPDOG_WEB_PORT", port)
	t.Chdir(wt)

	// 1. Committed worker runs `sheepdog factory report ask "<question>" --option ... --recommend ...`.
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-worker-cli")
	var out, errBuf bytes.Buffer
	code := run(context.Background(), []string{
		"factory", "report", "ask", "Keep cache in SQLite or Plain JSON?",
		"--option", "SQLite", "--option", "Plain JSON", "--recommend", "SQLite",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("report ask exit=%d stdout=%q stderr=%q", code, out.String(), errBuf.String())
	}

	loaded, err := factory.LoadState(paths.State("proj-cli"))
	if err != nil || len(loaded.Asks) != 1 || loaded.Asks[0].Status != flow.AskStatusOpen {
		t.Fatalf("State.Asks after report ask = %+v (err=%v)", loaded.Asks, err)
	}
	askID := loaded.Asks[0].ID
	if len(loaded.Project.Wakes) == 0 {
		t.Fatalf("expected coordinator wake queued on Project.Wakes, got none")
	}

	// 2. Coordinator runs `sheepdog factory answer <ask_id> "<option>" --why "<why>"`.
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-coord-cli")
	out.Reset()
	errBuf.Reset()
	code = run(context.Background(), []string{
		"factory", "answer", askID, "Plain JSON", "--why", "avoid extra driver",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("factory answer exit=%d stdout=%q stderr=%q", code, out.String(), errBuf.String())
	}

	loaded, _ = factory.LoadState(paths.State("proj-cli"))
	if loaded.Asks[0].Status != flow.AskStatusAnswered || loaded.Asks[0].Answer != "Plain JSON" {
		t.Fatalf("State.Asks after answer = %+v", loaded.Asks[0])
	}

	var workerMsgs []string
	for _, req := range srv.Requests() {
		var p struct{ Terminal, Text string }
		if req.Method == "terminal.send" && json.Unmarshal(req.Params, &p) == nil && p.Terminal == "worker-1" {
			workerMsgs = append(workerMsgs, p.Text)
		}
	}
	if len(workerMsgs) != 1 || !strings.Contains(workerMsgs[0], "Plain JSON") || !strings.Contains(workerMsgs[0], "avoid extra driver") {
		t.Fatalf("worker terminal messages = %q, want 1 message with Plain JSON and reason", workerMsgs)
	}
}

// TestFactoryAskAndAnswerCLIFlagsAndRefusals checks CLI flag ordering, usage
// help, and exit codes for `report ask`, `ask` and `answer` (KB-ASK-6,
// KB-ASK-15, KB-ASK-19; design.md 7.2).
func TestFactoryAskAndAnswerCLIFlagsAndRefusals(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, body)
		args, _ := body["args"].(map[string]any)
		if body["token"] == "tok-worker" && args["kind"] == "release" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"kind_not_allowed: workers may open only decision asks"}`))
			return
		}
		if body["verb"] == "answer" && args["ask"] == "ask-gated" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"proof_required: gated asks require owner AnswerProof"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	t.Setenv("SHEEPDOG_WEB_PORT", port)

	// Help text includes report ask, ask, answer, and their flags.
	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"factory", "--help"}, &out, &errBuf); code != 0 {
		t.Fatalf("factory --help code=%d", code)
	}
	for _, want := range []string{"report ask", "ask", "answer", "--kind", "--task", "--option", "--recommend"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("factory --help missing %q:\n%s", want, out.String())
		}
	}

	// Coordinator `factory ask` works with flags both before and after `<question>`.
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-coord")
	for _, argv := range [][]string{
		{"factory", "ask", "--kind", "release", "--task", "T1", "Tag v1.0.0?", "--option", "Yes, tag v1.0.0", "--option", "Not now", "--recommend", "Yes, tag v1.0.0"},
		{"factory", "ask", "Tag v1.0.0?", "--kind", "release", "--task", "T1", "--option", "Yes, tag v1.0.0", "--option", "Not now", "--recommend", "Yes, tag v1.0.0"},
	} {
		out.Reset()
		errBuf.Reset()
		if code := run(context.Background(), argv, &out, &errBuf); code != 0 {
			t.Fatalf("%v code=%d stderr=%s", argv, code, errBuf.String())
		}
		got := calls[len(calls)-1]
		args, _ := got["args"].(map[string]any)
		opts, _ := args["options"].([]any)
		if got["verb"] != "ask" || got["token"] != "tok-coord" || args["kind"] != "release" || args["task"] != "T1" ||
			args["question"] != "Tag v1.0.0?" || args["recommend"] != "Yes, tag v1.0.0" || !slices.Equal([]any{"Yes, tag v1.0.0", "Not now"}, opts) {
			t.Fatalf("%v posted %+v", argv, got)
		}
	}

	// Worker asking a gated kind exits 1 with the refusal reason on stderr.
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-worker")
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "ask", "--kind", "release", "Tag v1?", "--option", "Yes", "--option", "No", "--recommend", "Yes"}, &out, &errBuf); code != 1 || !strings.Contains(errBuf.String(), "kind_not_allowed") {
		t.Fatalf("worker gated ask: code=%d stderr=%q, want 1 with kind_not_allowed", code, errBuf.String())
	}

	// Coordinator answering a gated ask exits 1 with proof_required on stderr.
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-coord")
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "answer", "ask-gated", "Yes"}, &out, &errBuf); code != 1 || !strings.Contains(errBuf.String(), "proof_required") {
		t.Fatalf("coord gated answer: code=%d stderr=%q, want 1 with proof_required", code, errBuf.String())
	}

	// Missing positional args fail with usage exit 2 without calling the daemon.
	before := len(calls)
	for _, bad := range [][]string{
		{"factory", "report", "ask"},
		{"factory", "ask", "--kind", "release"},
		{"factory", "answer", "only-ask-id"},
	} {
		if code := run(context.Background(), bad, &out, &errBuf); code != 2 {
			t.Fatalf("%v code=%d, want 2", bad, code)
		}
	}
	if len(calls) != before {
		t.Fatalf("malformed CLI invocations posted %d request(s)", len(calls)-before)
	}
}

// TestFactoryAnswerCLIWhenWorkerDiedBetweenPasses verifies that `sheepdog factory answer`
// succeeds (HTTP 200, exit 0) and holds the answer on Attempt.Held when an idle
// worker terminal exited in hc before the next pass observed it (KB-ASK-10).
func TestFactoryAnswerCLIWhenWorkerDiedBetweenPasses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	srv := hctest.New(t)
	srv.OnExec = hctest.GitExec
	t.Setenv("HC_SOCKET", srv.Path)
	client := hc.NewClient(srv.Path)
	paths := factory.Paths{DataDir: t.TempDir(), ClaudeConfig: filepath.Join(t.TempDir(), ".claude.json")}
	eng := factory.New(paths, client)

	repo := filepath.Join(t.TempDir(), "repo")
	wt := filepath.Join(t.TempDir(), "wt")
	gitRun := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	if b, err := exec.Command("git", "init", "--initial-branch=main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, b)
	}
	_ = os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o600)
	gitRun(repo, "add", "main.go")
	gitRun(repo, "commit", "-m", "chore: init")
	baseSHA := gitRun(repo, "rev-parse", "HEAD")
	gitRun(repo, "worktree", "add", "-b", "task/proj-cli-dead-T1-1", wt)
	_ = os.WriteFile(filepath.Join(wt, "feature.go"), []byte("package main\n// committed work\n"), 0o600)
	gitRun(wt, "add", "feature.go")
	gitRun(wt, "commit", "-m", "feat: partial work")

	idleAt := uint64(time.Now().Add(-time.Minute).UnixMilli())
	srv.Add(hc.Terminal{
		Address: "coord-1", Name: "coord-1", State: "live", CreatedMS: 10, StatusMS: &idleAt,
		Labels: map[string]string{"sheepdog.project": "proj-cli-dead", "sheepdog.role": flow.RoleCoordinator, "sheepdog.run": "run-coord"},
		Status: &hc.Status{App: "claude-code", State: "idle"},
	})
	srv.Add(hc.Terminal{
		Address: "worker-1", Name: "worker-1", State: "live", CreatedMS: 20, StatusMS: &idleAt,
		Labels: map[string]string{"sheepdog.project": "proj-cli-dead", "sheepdog.role": flow.RoleWorker, "sheepdog.task": "T1", "sheepdog.run": "run-worker"},
		Status: &hc.Status{App: "claude-code", State: "idle"},
	})

	st := flow.State{
		Seq:      1,
		Settings: flow.DefaultSettings(),
		Project: flow.Project{
			ID: "proj-cli-dead", Folder: repo, Goal: "Build feature", Started: true,
			RunID: "coord-1", RunTokenHash: testTokenSHA("tok-coord-cli"),
			Run: &flow.RunRecord{ID: "run-coord", Address: "coord-1", CreatedMS: 10, Role: flow.RoleCoordinator, CLI: "claude", State: flow.PhaseIdle, Status: "idle", StatusReported: true},
		},
		Tasks: []flow.Task{{
			ID: "T1", Title: "feat: add cache", EstimateLines: 50,
			Outcomes: []flow.Outcome{{Text: "cache works", ProvedBy: "cmd: go test ./..."}},
			Attempts: []flow.Attempt{{
				BaseCommit: baseSHA, Branch: "task/proj-cli-dead-T1-1", Worktree: wt, SizeCap: 100,
				RunID: "worker-1", RunTokenHash: testTokenSHA("tok-worker-cli"),
				Run:     &flow.RunRecord{ID: "run-worker", Address: "worker-1", CreatedMS: 20, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseIdle, Status: "idle", StatusReported: true},
				Ready:   []flow.Ready{},
				Rounds:  []flow.Round{},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done"}},
			}},
		}},
	}
	if err := factory.SaveState(paths.State("proj-cli-dead"), st); err != nil {
		t.Fatal(err)
	}

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Token string          `json:"token"`
			Verb  string          `json:"verb"`
			Args  json.RawMessage `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := eng.Report(req.Token, req.Verb, req.Args); err != nil {
			var ref *factory.Refusal
			code := http.StatusInternalServerError
			if errors.As(err, &ref) {
				code = http.StatusBadRequest
			}
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	t.Cleanup(httpSrv.Close)
	_, port, _ := net.SplitHostPort(httpSrv.Listener.Addr().String())
	t.Setenv("SHEEPDOG_WEB_PORT", port)
	t.Chdir(wt)

	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-worker-cli")
	var out, errBuf bytes.Buffer
	code := run(context.Background(), []string{
		"factory", "report", "ask", "Keep cache in SQLite or Plain JSON?",
		"--option", "SQLite", "--option", "Plain JSON", "--recommend", "SQLite",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("report ask exit=%d stdout=%q stderr=%q", code, out.String(), errBuf.String())
	}
	loaded, err := factory.LoadState(paths.State("proj-cli-dead"))
	if err != nil || len(loaded.Asks) != 1 {
		t.Fatalf("State.Asks = %+v (err=%v)", loaded.Asks, err)
	}
	askID := loaded.Asks[0].ID

	// Worker terminal dies between passes while state.json still shows PhaseIdle.
	if _, err := client.Kill(context.Background(), "worker-1"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-coord-cli")
	out.Reset()
	errBuf.Reset()
	code = run(context.Background(), []string{
		"factory", "answer", askID, "Plain JSON", "--why", "avoid extra driver",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("factory answer when worker died between passes exit=%d stdout=%q stderr=%q", code, out.String(), errBuf.String())
	}

	loaded, err = factory.LoadState(paths.State("proj-cli-dead"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Asks[0].Status != flow.AskStatusAnswered || loaded.Asks[0].Answer != "Plain JSON" {
		t.Fatalf("State.Asks after answer = %+v", loaded.Asks[0])
	}
	if held := loaded.Tasks[0].Attempts[0].Held; len(held) != 1 || !strings.Contains(held[0], "Plain JSON") || !strings.Contains(held[0], "avoid extra driver") {
		t.Fatalf("Attempt.Held = %q, want 1 held answer with Plain JSON and reason", held)
	}
}
