package web

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

type stubFactory struct {
	startedFolder, startedGoal string
	startedMaxRunning          int
	stoppedProject             string
	reportToken, reportVerb    string
	reportArgs                 string
	reportErr                  error
	called                     int
}

func (f *stubFactory) Projects() []factory.ProjectView { return nil }
func (f *stubFactory) Graph(string) (factory.GraphView, error) {
	return factory.GraphView{}, factory.ErrProjectNotFound
}
func (f *stubFactory) LogLines(string, int) ([]factory.LogEntry, error) {
	return nil, factory.ErrProjectNotFound
}

func (f *stubFactory) Report(token, verb string, args json.RawMessage) error {
	f.called++
	f.reportToken, f.reportVerb, f.reportArgs = token, verb, string(args)
	return f.reportErr
}

func (f *stubFactory) Start(folder, goal string, maxRunning int) error {
	f.called++
	f.startedFolder, f.startedGoal, f.startedMaxRunning = folder, goal, maxRunning
	return nil
}

func (f *stubFactory) Stop(project string) error {
	f.called++
	f.stoppedProject = project
	return nil
}

// TestFactoryRoutesLoopbackOnly verifies POST /api/factory/{start,stop} work on
// loopback and refuse non-loopback or proxied requests (design.md 7.2; KB-ASK-19).
func TestFactoryRoutesLoopbackOnly(t *testing.T) {
	dir := t.TempDir()
	elog, err := eventlog.Open(dir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = elog.Close() })
	st, err := state.Open(filepath.Join(dir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	stub := &stubFactory{}
	srv, err := New(Deps{Log: elog, Store: st, DataDir: dir, Factory: stub})
	if err != nil {
		t.Fatal(err)
	}

	// Non-loopback callers are refused before reaching Factory.
	for _, path := range []string{"/api/factory/start", "/api/factory/stop", "/api/factory/report"} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"folder":"/tmp/x","goal":"g"}`))
		r.RemoteAddr, r.Host = "192.168.1.50:5000", "192.168.1.20:7777"
		r.TLS = &tls.ConnectionState{}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "loopback_only") {
			t.Fatalf("remote %s = %d %s, want 403 loopback_only", path, w.Code, w.Body.String())
		}
		// Loopback with X-Forwarded-For is also refused as untrusted loopback.
		rp := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"folder":"/tmp/x","goal":"g"}`))
		rp.RemoteAddr, rp.Host = "127.0.0.1:5000", "127.0.0.1:7777"
		rp.Header.Set("X-Forwarded-For", "203.0.113.9")
		wp := httptest.NewRecorder()
		srv.Handler().ServeHTTP(wp, rp)
		if wp.Code != http.StatusForbidden {
			t.Fatalf("proxied %s = %d, want 403", path, wp.Code)
		}
	}
	if stub.called != 0 {
		t.Fatalf("Factory called %d times by non-loopback requests", stub.called)
	}

	// Direct loopback succeeds.
	rs := httptest.NewRequest(http.MethodPost, "/api/factory/start", strings.NewReader(`{"folder":"/tmp/repo","goal":"build x","max_running":2}`))
	rs.RemoteAddr, rs.Host = "127.0.0.1:5000", "127.0.0.1:7777"
	ws := httptest.NewRecorder()
	srv.Handler().ServeHTTP(ws, rs)
	if ws.Code != http.StatusOK || stub.startedFolder != "/tmp/repo" || stub.startedGoal != "build x" || stub.startedMaxRunning != 2 {
		t.Fatalf("loopback start = %d %s, stub=%+v", ws.Code, ws.Body.String(), stub)
	}

	rt := httptest.NewRequest(http.MethodPost, "/api/factory/stop", strings.NewReader(`{"project":"/tmp/repo"}`))
	rt.RemoteAddr, rt.Host = "127.0.0.1:5000", "127.0.0.1:7777"
	wt := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wt, rt)
	if wt.Code != http.StatusOK || stub.stoppedProject != "/tmp/repo" {
		t.Fatalf("loopback stop = %d %s, stub=%+v", wt.Code, wt.Body.String(), stub)
	}

	// A report goes to the engine's acceptance step with its token, verb and args.
	rr := httptest.NewRequest(http.MethodPost, "/api/factory/report", strings.NewReader(`{"token":"tok","verb":"plan apply","args":{"tasks":[]}}`))
	rr.RemoteAddr, rr.Host = "127.0.0.1:5000", "127.0.0.1:7777"
	wr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wr, rr)
	if wr.Code != http.StatusOK || stub.reportToken != "tok" || stub.reportVerb != "plan apply" || stub.reportArgs != `{"tasks":[]}` {
		t.Fatalf("loopback report = %d %s, stub=%+v", wr.Code, wr.Body.String(), stub)
	}

	// A stop carrying a run's token never reaches Stop: the engine judges it.
	stub.stoppedProject = ""
	rk := httptest.NewRequest(http.MethodPost, "/api/factory/stop", strings.NewReader(`{"project":"/tmp/other","token":"tok-w"}`))
	rk.RemoteAddr, rk.Host = "127.0.0.1:5000", "127.0.0.1:7777"
	srv.Handler().ServeHTTP(httptest.NewRecorder(), rk)
	if stub.stoppedProject != "" || stub.reportToken != "tok-w" || stub.reportVerb != "stop" {
		t.Fatalf("stop with a run token reached Stop or skipped Report: stub=%+v", stub)
	}

	// A refusal keeps 400 and its code; a daemon fault is a 500.
	for _, c := range []struct {
		err  error
		want int
	}{{&factory.Refusal{Code: "kind_not_allowed", Why: "no"}, http.StatusBadRequest}, {errors.New("disk full"), http.StatusInternalServerError}} {
		stub.reportErr = c.err
		re := httptest.NewRequest(http.MethodPost, "/api/factory/report", strings.NewReader(`{"token":"tok","verb":"plan apply"}`))
		re.RemoteAddr, re.Host = "127.0.0.1:5000", "127.0.0.1:7777"
		we := httptest.NewRecorder()
		srv.Handler().ServeHTTP(we, re)
		if we.Code != c.want || !strings.Contains(we.Body.String(), c.err.Error()) {
			t.Fatalf("report error %v = %d %s, want %d", c.err, we.Code, we.Body.String(), c.want)
		}
	}
}

func TestFactoryViewRoutes(t *testing.T) {
	dir := t.TempDir()
	elog, err := eventlog.Open(dir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = elog.Close() })
	st, err := state.Open(filepath.Join(dir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	paths := factory.Paths{DataDir: dir}
	fs := flow.State{
		Seq:      2,
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "proj-web", Folder: "/tmp/repo", Goal: "demo", Started: true},
		Tasks: []flow.Task{
			{ID: "T1", Title: "feat: one", Done: &flow.Merged{MergedCommit: "abc"}},
			{ID: "T2", Title: "feat: two", After: []string{"T1"}},
		},
	}
	if err := factory.SaveState(paths.State("proj-web"), fs); err != nil {
		t.Fatal(err)
	}
	for _, l := range []factory.LogLine{
		{Seq: 1, Project: "proj-web", By: "engine", Task: "T1", Rule: "N1", Input: map[string]string{"folder": "/tmp/repo"}, Facts: map[string]any{"pr": 1}, Decision: "done", Why: "ended"},
		{Seq: 2, Project: "proj-web", By: "engine", Task: "T2", Rule: "N11", Input: map[string]string{"secret": "hidden"}, Facts: map[string]any{"checks": "green"}, Decision: "queued", Why: "no attempt"},
	} {
		if err := factory.AppendLog(paths.Log("proj-web"), l); err != nil {
			t.Fatal(err)
		}
	}

	eng := factory.New(paths, nil)
	srv, err := New(Deps{Log: elog, Store: st, DataDir: dir, Factory: eng})
	if err != nil {
		t.Fatal(err)
	}

	// Unauthorised remote and proxied callers are refused with 401.
	for _, path := range []string{"/api/factory/projects", "/api/factory/projects/proj-web/graph", "/api/factory/projects/proj-web/log?n=1"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr, r.Host = "192.168.1.50:5000", "192.168.1.20:7777"
		r.TLS = &tls.ConnectionState{}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("remote %s = %d %s, want 401", path, w.Code, w.Body.String())
		}
		rp := httptest.NewRequest(http.MethodGet, path, nil)
		rp.RemoteAddr, rp.Host = "127.0.0.1:5000", "127.0.0.1:7777"
		rp.Header.Set("X-Forwarded-For", "203.0.113.9")
		wp := httptest.NewRecorder()
		srv.Handler().ServeHTTP(wp, rp)
		if wp.Code != http.StatusUnauthorized {
			t.Fatalf("proxied %s = %d, want 401", path, wp.Code)
		}
	}

	get := func(target *Server, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:7777"
		w := httptest.NewRecorder()
		target.Handler().ServeHTTP(w, r)
		return w
	}

	// GET /api/factory/projects
	wp := get(srv, "/api/factory/projects")
	if wp.Code != http.StatusOK {
		t.Fatalf("projects = %d %s", wp.Code, wp.Body.String())
	}
	var projResp struct {
		Protocol int                   `json:"protocol"`
		Projects []factory.ProjectView `json:"projects"`
	}
	if err := json.Unmarshal(wp.Body.Bytes(), &projResp); err != nil || projResp.Protocol != 1 || len(projResp.Projects) != 1 || projResp.Projects[0].ID != "proj-web" {
		t.Fatalf("unexpected projects JSON: %s (err=%v)", wp.Body.String(), err)
	}

	// GET /api/factory/projects/{id}/graph
	wg := get(srv, "/api/factory/projects/proj-web/graph")
	if wg.Code != http.StatusOK {
		t.Fatalf("graph = %d %s", wg.Code, wg.Body.String())
	}
	var gResp factory.GraphView
	if err := json.Unmarshal(wg.Body.Bytes(), &gResp); err != nil || gResp.Project != "proj-web" || len(gResp.Tasks) != 2 || gResp.Tasks[0].Ref != "T1" || gResp.Tasks[1].Stage != "queued" {
		t.Fatalf("unexpected graph JSON: %s (err=%v)", wg.Body.String(), err)
	}

	// GET /api/factory/projects/{id}/log?n=1 returns only projected LogEntry fields (no input/facts).
	wl := get(srv, "/api/factory/projects/proj-web/log?n=1")
	if wl.Code != http.StatusOK {
		t.Fatalf("log = %d %s", wl.Code, wl.Body.String())
	}
	var logResp struct {
		Protocol int              `json:"protocol"`
		Project  string           `json:"project"`
		Lines    []map[string]any `json:"lines"`
	}
	if err := json.Unmarshal(wl.Body.Bytes(), &logResp); err != nil || len(logResp.Lines) != 1 || logResp.Lines[0]["decision"] != "queued" || logResp.Lines[0]["task"] != "T2" {
		t.Fatalf("unexpected log JSON: %s (err=%v)", wl.Body.String(), err)
	}
	for _, forbidden := range []string{"input", "facts", "seq", "project", "rule", "asked"} {
		if _, leaked := logResp.Lines[0][forbidden]; leaked {
			t.Fatalf("log route leaked %q: %s", forbidden, wl.Body.String())
		}
	}

	// 404 for unknown project, 400 for invalid n.
	if w := get(srv, "/api/factory/projects/nope/graph"); w.Code != http.StatusNotFound {
		t.Fatalf("missing graph = %d, want 404", w.Code)
	}
	if w := get(srv, "/api/factory/projects/nope/log"); w.Code != http.StatusNotFound {
		t.Fatalf("missing log = %d, want 404", w.Code)
	}
	for _, bad := range []string{"/api/factory/projects/proj-web/log?n=-1", "/api/factory/projects/proj-web/log?n=abc"} {
		if w := get(srv, bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", bad, w.Code)
		}
	}

	// 503 when Factory is nil.
	srvNil, err := New(Deps{Log: elog, Store: st, DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/factory/projects", "/api/factory/projects/proj-web/graph", "/api/factory/projects/proj-web/log"} {
		if w := get(srvNil, path); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("nil factory %s = %d, want 503", path, w.Code)
		}
	}
}

// TestFactoryStatusLexicalOnly checks PR 5b.9 round 3: GET /api/factory/status
// runs inside the launchd daemon and must use only the lexical underFolder
// check without calling filepath.EvalSymlinks on project folders.
func TestFactoryStatusLexicalOnly(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	elog, err := eventlog.Open(dir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = elog.Close() })
	st, err := state.Open(filepath.Join(dir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	paths := factory.Paths{DataDir: dir}
	missingRepo := filepath.Join(realDir, "unmounted", "repo")
	if err := factory.SaveState(paths.State("proj-missing"), flow.State{
		Seq:      1,
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "proj-missing", Folder: missingRepo, Started: true},
		Tasks:    []flow.Task{{ID: "T1", Title: "feat: unmounted"}},
	}); err != nil {
		t.Fatal(err)
	}

	realRepo := filepath.Join(realDir, "real-repo")
	if err := os.MkdirAll(realRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	linkRepo := filepath.Join(realDir, "link-repo")
	if err := os.Symlink(realRepo, linkRepo); err != nil {
		t.Fatal(err)
	}
	if err := factory.SaveState(paths.State("proj-real"), flow.State{
		Seq:      1,
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "proj-real", Folder: realRepo, Started: true},
		Tasks:    []flow.Task{{ID: "T2", Title: "feat: real"}},
	}); err != nil {
		t.Fatal(err)
	}

	eng := factory.New(paths, nil)
	srv, err := New(Deps{Log: elog, Store: st, DataDir: dir, Factory: eng})
	if err != nil {
		t.Fatal(err)
	}

	getStatus := func(folder string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/factory/status?folder="+folder+"&data_dir="+dir, nil)
		r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:7777"
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}

	// Lexical subfolder match succeeds even when EvalSymlinks would fail on an unmounted path.
	wMissing := getStatus(filepath.Join(missingRepo, "internal", "pkg"))
	if wMissing.Code != http.StatusOK || !strings.Contains(wMissing.Body.String(), `"T1"`) {
		t.Fatalf("status for lexical subfolder of unmounted repo = %d %s, want 200 with T1", wMissing.Code, wMissing.Body.String())
	}

	// Uncanonicalized symlink path must not be resolved by the daemon.
	wLink := getStatus(linkRepo)
	if wLink.Code == http.StatusOK || !strings.Contains(wLink.Body.String(), "no factory project") {
		t.Fatalf("status for symlink %q = %d %s, want non-200 no factory project", linkRepo, wLink.Code, wLink.Body.String())
	}
}

// TestFactoryStartHTTPUsesEngineStartChecks_KB_STA_18 verifies O31 row 10 / J1a:
// POST /api/factory/start cannot bypass Engine.Start's start checks.
func TestFactoryStartHTTPUsesEngineStartChecks_KB_STA_18(t *testing.T) {
	dir := t.TempDir()
	elog, err := eventlog.Open(dir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = elog.Close() })
	st, err := state.Open(filepath.Join(dir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	repo := filepath.Join(t.TempDir(), "repo")
	if b, err := exec.Command("git", "init", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, b)
	}
	hcSrv := hctest.New(t)
	hcSrv.OnExec = hctest.GitExec

	eng := factory.New(factory.Paths{DataDir: dir}, hc.NewClient(hcSrv.Path))
	checkCalls := 0
	eng.StartCheck = func(ctx context.Context, opts factory.DoctorOptions) []onboard.Result {
		checkCalls++
		opts.HCSocket = ""
		opts.RunCommand = func(_ context.Context, _ string, _ []string, argv ...string) factory.CommandOutput {
			key := strings.Join(argv, " ")
			switch {
			case strings.HasSuffix(key, "git --version"):
				return factory.CommandOutput{Stdout: "git version 2.45.0\n"}
			case strings.HasSuffix(key, "rev-parse --show-toplevel"):
				return factory.CommandOutput{Stdout: repo}
			case strings.HasSuffix(key, "remote get-url --all origin"), strings.HasSuffix(key, "remote get-url --push --all origin"):
				return factory.CommandOutput{Stdout: "https://github.com/o/r.git"}
			case strings.HasSuffix(key, "refs/remotes/origin/HEAD"):
				return factory.CommandOutput{Stdout: "origin/main"}
			case strings.HasSuffix(key, "rev-parse --verify --quiet origin/main"):
				return factory.CommandOutput{Stdout: "abc1234\n"}
			case strings.HasSuffix(key, "fetch --prune origin"):
				return factory.CommandOutput{}
			case strings.HasSuffix(key, "claude auth status --json"):
				return factory.CommandOutput{Stdout: `{"loggedIn":false}`}
			case strings.HasSuffix(key, "ls-tree --name-only origin/main .github/workflows/"):
				return factory.CommandOutput{Stdout: ".github/workflows/ci.yml"}
			case strings.HasSuffix(key, "show origin/main:.github/workflows/ci.yml"):
				return factory.CommandOutput{Stdout: "on:\n  pull_request:\n    branches: [main]\njobs: {}\n"}
			case strings.Contains(key, "gh repo view"):
				return factory.CommandOutput{Stdout: `{"defaultBranchRef":{"name":"main"},"viewerPermission":"WRITE"}`}
			case strings.Contains(key, "gh api repos/o/r/branches/main"):
				return factory.CommandOutput{Stdout: `{"protected":false}`}
			case strings.Contains(key, "gh api repos/o/r/rules/branches/main"):
				return factory.CommandOutput{Stdout: "[]"}
			default:
				return factory.CommandOutput{Exit: 1, Stderr: "unexpected: " + key}
			}
		}
		return factory.StartChecks(ctx, opts)
	}

	srv, err := New(Deps{Log: elog, Store: st, DataDir: dir, Factory: eng})
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"folder": repo, "goal": "ship 8.1"})
	r := httptest.NewRequest(http.MethodPost, "/api/factory/start", strings.NewReader(string(body)))
	r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:7777"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if checkCalls != 1 {
		t.Fatalf("Engine.StartCheck calls = %d, want 1", checkCalls)
	}
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "claude auth login") {
		t.Fatalf("POST /api/factory/start = %d %s, want 400 with claude auth login fix", w.Code, w.Body.String())
	}
	if got := eng.Projects(); len(got) != 0 {
		t.Fatalf("refused HTTP start created project state: %+v", got)
	}
}

// TestFactoryLogRouteTaskAndObservationsFilter verifies plan.md PR 14.7 on
// GET /api/factory/projects/{id}/log: kind projection without rule leakage,
// observations excluded by default so n counts visible lines, ?observations=1,
// ?task= returning an older task's own lines past 100 newer lines, and an
// observation-only project returning an empty JSON array by default.
func TestFactoryLogRouteTaskAndObservationsFilter(t *testing.T) {
	dir := t.TempDir()
	elog, err := eventlog.Open(dir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = elog.Close() })
	st, err := state.Open(filepath.Join(dir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	paths := factory.Paths{DataDir: dir}
	t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	var lines []factory.LogLine
	var seq int64
	add := func(by, task, rule string, asked []string, dec, why string) {
		seq++
		lines = append(lines, factory.LogLine{
			Seq: seq, At: t0.Add(time.Duration(seq) * time.Second), Project: "proj-log",
			By: by, Task: task, Rule: rule, Asked: asked, Decision: dec, Why: why,
		})
	}
	add("owner", "", "start", nil, "started", "ship readable log")
	add("engine", "T1", "N23", []string{flow.AskStartWorker}, "started worker", "claude sonnet")
	add("engine", "T1", "pr_snapshot", nil, "", "the PR snapshot changed")
	add("engine", "T1", "N28", []string{flow.AskOpenRecap + "@review"}, "opened recap", "max rounds")
	add("engine", "T1", "A2", []string{flow.AskNotifyOwner}, "asked you", "Delete the old table?")
	add("engine", "T1", "N2", []string{flow.AskMarkDone}, "done: PR #7 merged", "the PR is merged")
	add("engine", "T1", "pr_snapshot", nil, "", "the PR snapshot changed")
	for i := 1; i <= 55; i++ {
		add("engine", "T2", "N23", []string{flow.AskStartWorker}, fmt.Sprintf("t2-step-%d", i), "working")
		add("engine", "T2", "pr_snapshot", nil, "", "the PR snapshot changed")
	}
	add("engine", "T2", "pr_snapshot", nil, "", "the PR snapshot changed")

	if err := factory.SaveState(paths.State("proj-log"), flow.State{
		Seq: seq, Settings: flow.DefaultSettings(),
		Project: flow.Project{ID: "proj-log", Folder: "/tmp/log", Goal: "ship readable log", Started: true},
	}); err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if err := factory.AppendLog(paths.Log("proj-log"), l); err != nil {
			t.Fatal(err)
		}
	}
	if err := factory.SaveState(paths.State("proj-obs"), flow.State{
		Seq: 2, Settings: flow.DefaultSettings(),
		Project: flow.Project{ID: "proj-obs", Folder: "/tmp/obs", Goal: "obs only", Started: true},
	}); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 2; i++ {
		if err := factory.AppendLog(paths.Log("proj-obs"), factory.LogLine{
			Seq: i, At: t0.Add(time.Duration(i) * time.Second), Project: "proj-obs",
			By: "engine", Task: "T1", Rule: "pr_snapshot", Why: "the PR snapshot changed",
		}); err != nil {
			t.Fatal(err)
		}
	}

	eng := factory.New(paths, nil)
	srv, err := New(Deps{Log: elog, Store: st, DataDir: dir, Factory: eng})
	if err != nil {
		t.Fatal(err)
	}

	getLog := func(path string) []map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:7777"
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s, want 200", path, w.Code, w.Body.String())
		}
		var resp struct {
			Protocol int              `json:"protocol"`
			Project  string           `json:"project"`
			Lines    []map[string]any `json:"lines"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Lines == nil {
			t.Fatalf("GET %s bad JSON or nil lines: %s (err=%v)", path, w.Body.String(), err)
		}
		for _, ln := range resp.Lines {
			for _, forbidden := range []string{"rule", "asked", "input", "facts", "seq", "project"} {
				if _, leaked := ln[forbidden]; leaked {
					t.Errorf("GET %s leaked %q: %v", path, forbidden, ln)
				}
			}
		}
		return resp.Lines
	}

	// Default n=2 skips trailing observations and returns 2 visible lines.
	vis2 := getLog("/api/factory/projects/proj-log/log?n=2")
	if len(vis2) != 2 || vis2[0]["decision"] != "t2-step-54" || vis2[1]["decision"] != "t2-step-55" {
		t.Errorf("?n=2 = %v, want [t2-step-54, t2-step-55]", vis2)
	}
	// ?observations=0 behaves the same as omitting observations.
	vis0 := getLog("/api/factory/projects/proj-log/log?n=2&observations=0")
	if len(vis0) != 2 || vis0[1]["decision"] != "t2-step-55" {
		t.Errorf("?n=2&observations=0 = %v, want [t2-step-54, t2-step-55]", vis0)
	}
	// ?observations=1 includes trailing observations with kind="observation".
	obs2 := getLog("/api/factory/projects/proj-log/log?n=2&observations=1")
	if len(obs2) != 2 || obs2[0]["kind"] != "observation" || obs2[1]["kind"] != "observation" {
		t.Errorf("?n=2&observations=1 = %v, want 2 observation lines", obs2)
	}

	// ?task=T1 recovers T1's lines older than T2's >100 newer lines, without observations.
	t1Vis := getLog("/api/factory/projects/proj-log/log?task=T1")
	if len(t1Vis) != 4 {
		t.Fatalf("?task=T1 len = %d (%v), want 4 visible T1 lines", len(t1Vis), t1Vis)
	}
	wantKinds := []any{nil, "needs_decision", "asked_you", "merged"}
	for i, want := range wantKinds {
		got, hasKind := t1Vis[i]["kind"]
		if t1Vis[i]["task"] != "T1" {
			t.Errorf("?task=T1[%d].task = %v, want T1", i, t1Vis[i]["task"])
		}
		if want == nil {
			if hasKind && got != "" {
				t.Errorf("?task=T1[%d].kind = %v, want empty/omitted", i, got)
			}
		} else if got != want {
			t.Errorf("?task=T1[%d].kind = %v, want %v", i, got, want)
		}
	}

	// ?task=T1&observations=1 includes T1's 2 observations too.
	t1All := getLog("/api/factory/projects/proj-log/log?task=T1&observations=1")
	if len(t1All) != 6 || t1All[1]["kind"] != "observation" || t1All[5]["kind"] != "observation" {
		t.Errorf("?task=T1&observations=1 = len %d (%v), want 6 T1 lines with observations", len(t1All), t1All)
	}

	// ?task=T1&n=2 returns T1's newest 2 non-observation lines.
	t1Two := getLog("/api/factory/projects/proj-log/log?task=T1&n=2")
	if len(t1Two) != 2 || t1Two[0]["kind"] != "asked_you" || t1Two[1]["kind"] != "merged" {
		t.Errorf("?task=T1&n=2 = %v, want [asked_you, merged]", t1Two)
	}

	// Observation-only project returns empty array by default and observations when asked.
	if got := getLog("/api/factory/projects/proj-obs/log"); len(got) != 0 {
		t.Errorf("proj-obs default len = %d (%v), want 0", len(got), got)
	}
	if got := getLog("/api/factory/projects/proj-obs/log?observations=1"); len(got) != 2 || got[0]["kind"] != "observation" {
		t.Errorf("proj-obs ?observations=1 = %v, want 2 observation lines", got)
	}
}
