package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// samplePath fills a route pattern's wildcards.
func samplePath(pattern string) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	path = strings.TrimSuffix(path, "{$}")
	path = regexp.MustCompile(`\{[a-z_]+\.\.\.\}`).ReplaceAllString(path, "a/b")
	path = regexp.MustCompile(`\{[a-z_]+\}`).ReplaceAllString(path, "x")
	return method, path
}

// Every route has a rule, and each rule holds for a caller on the
// network: a loopback-only route answers 403 loopback_only, a device
// route 401 without credentials, a setup route 403, and a public one
// neither.
func TestEveryRouteHasAnAccessRule(t *testing.T) {
	f := newSetupFixture(t, config.Default())
	h := f.h
	table, err := h.srv.routeTable()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[access]int{}
	for _, rt := range table {
		seen[rt.access]++
		method, path := samplePath(rt.pattern)
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		r.RemoteAddr, r.Host = "192.168.1.50:5000", "192.168.1.20:7777"
		r.TLS = &tls.ConnectionState{}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(w, r)
		switch rt.access {
		case accessLoopback:
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "loopback_only") {
				t.Errorf("%s from the network = %d %s, want 403 loopback_only", rt.pattern, w.Code, w.Body)
			}
		case accessDevice:
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s without credentials = %d, want 401", rt.pattern, w.Code)
			}
		case accessSetup:
			if w.Code != http.StatusForbidden {
				t.Errorf("%s from the network = %d, want 403", rt.pattern, w.Code)
			}
		case accessPublic:
			// The sign-in route refuses a bad signature with 401 itself.
			if (w.Code == http.StatusUnauthorized && rt.pattern != "POST /api/auth/session") || w.Code == http.StatusForbidden {
				t.Errorf("public %s = %d", rt.pattern, w.Code)
			}
		default:
			t.Errorf("%s has no access rule", rt.pattern)
		}
	}
	for _, a := range []access{accessPublic, accessDevice, accessLoopback, accessSetup} {
		if seen[a] == 0 {
			t.Errorf("no route with access %d: the table walk is not covering it", a)
		}
	}

	// The debug and internal routes still work on the computer itself.
	for _, path := range []string{"/debug", "/api/brain/transcript", "/api/context/timeline", "/api/pair"} {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:7777"
		w := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(w, r)
		if w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
			t.Errorf("%s on loopback = %d", path, w.Code)
		}
	}
}

// The composer's route is /api/utterance, open to a paired device; the
// old /api/inject/utterance is gone.
func TestUtteranceRouteRenamed(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	var tr map[string]any
	if code, body := h.postJSON("/api/utterance", map[string]any{"text": "hello"}, &tr); code != http.StatusOK {
		t.Errorf("/api/utterance = %d %s", code, body)
	}
	if code, _ := h.postJSON("/api/inject/utterance", map[string]any{"text": "hello"}, nil); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Errorf("/api/inject/utterance still answers %d", code)
	}
}
