package web

import (
	"crypto/tls"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	static "github.com/kccarlos/herdr-sheepdog/web"
)

func newFactoryPageServer(t *testing.T) *Server {
	t.Helper()
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
	srv, err := New(Deps{Log: elog, Store: st, DataDir: dir, Factory: &stubFactory{}})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// The factory page is served to this computer only, like /debug.
func TestFactoryPageLoopbackOnly(t *testing.T) {
	srv := newFactoryPageServer(t)
	for _, path := range []string{"/factory", "/factory/", "/factory/app.js", "/factory/style.css"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:7777"
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("loopback GET %s = %d", path, w.Code)
		}
		if path == "/factory" && !strings.Contains(w.Body.String(), `src="/factory/app.js"`) {
			t.Errorf("GET /factory does not load app.js: %.200s", w.Body.String())
		}
		r = httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr, r.Host = "192.168.1.50:5000", "192.168.1.20:7777"
		r.TLS = &tls.ConnectionState{}
		w = httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "loopback_only") {
			t.Errorf("remote GET %s = %d %s, want 403 loopback_only", path, w.Code, w.Body)
		}
	}
}

// Every API URL the page fetches resolves to a GET factory view route, and the
// page uses all three W0 views (and no POST route: the page writes nothing).
func TestFactoryPageFetchesW0Routes(t *testing.T) {
	src, err := fs.ReadFile(static.FS, "factory/app.js")
	if err != nil {
		t.Fatal(err)
	}
	table, err := newFactoryPageServer(t).routeTable()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, rt := range table {
		mux.HandleFunc(rt.pattern, func(http.ResponseWriter, *http.Request) {})
	}
	urls := regexp.MustCompile("[`'\"](/api/[^`'\"]*)").FindAllStringSubmatch(string(src), -1)
	used := map[string]bool{}
	for _, m := range urls {
		url := regexp.MustCompile(`\$\{[^}]*\}`).ReplaceAllString(m[1], "p1")
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, url, nil))
		if !strings.HasPrefix(pattern, "GET /api/factory/") {
			t.Errorf("app.js fetches %s, which matches %q, not a factory view route", m[1], pattern)
		}
		used[pattern] = true
	}
	for _, want := range []string{"GET /api/factory/projects", "GET /api/factory/projects/{id}/graph", "GET /api/factory/projects/{id}/log"} {
		if !used[want] {
			t.Errorf("app.js never fetches %s", want)
		}
	}
	if strings.Contains(string(src), "POST") || strings.Contains(string(src), "method:") {
		t.Error("app.js sends a write; the factory page is read-only")
	}
}
