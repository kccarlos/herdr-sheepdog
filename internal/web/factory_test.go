package web

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
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
