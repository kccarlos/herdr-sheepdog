package web

import (
	"net/http"
	"strings"
	"testing"
)

// The spaces routes answer in plain words on a herdr that cannot manage
// spaces (mock herdr), and running a wrap-up command is gated like the
// phone shell.
func TestSpacesRoutes(t *testing.T) {
	h, _, _ := newShellHarness(t, false)
	if code := h.getJSON("/api/spaces", nil); code != http.StatusBadGateway {
		t.Errorf("GET /api/spaces on mock herdr = %d", code)
	}
	if code := h.getJSON("/api/projects", nil); code == http.StatusOK {
		t.Error("the old /api/projects route still answers")
	}
	if code, body := h.postJSON("/api/spaces/w1/commands/abc/run", map[string]any{}, nil); code != http.StatusForbidden || !strings.Contains(body, "like the shell") {
		t.Errorf("run with the shell off = %d %s", code, body)
	}
	if code, body := h.postJSON("/api/spaces/w1/run", map[string]any{"command": "make"}, nil); code != http.StatusForbidden || !strings.Contains(body, "like the shell") {
		t.Errorf("run a line with the shell off = %d %s", code, body)
	}
	h.srv.shellOn.Store(true)
	if code, _ := h.postJSON("/api/spaces/w1/commands/abc/run", map[string]any{}, nil); code == http.StatusForbidden {
		t.Error("run refused on loopback")
	}
	if code, _ := h.postJSON("/api/spaces/w1/run", map[string]any{"command": "make"}, nil); code == http.StatusForbidden {
		t.Error("run a line refused on loopback")
	}
	if code, _ := h.postJSON("/api/spaces/w1/rename", map[string]any{"name": "x"}, nil); code < 400 {
		t.Errorf("rename on mock herdr = %d", code)
	}
	if code, _ := h.postJSON("/api/agents", map[string]any{"kind": "claude", "space": "w1", "dir": "bench"}, nil); code != http.StatusBadRequest {
		t.Errorf("space and dir = %d, want 400", code)
	}
	if code, _ := h.postJSON("/api/agents", map[string]any{"kind": "claude", "dir": "bench", "worktree": map[string]any{}}, nil); code != http.StatusBadRequest {
		t.Errorf("worktree without a branch = %d, want 400", code)
	}
}

// A saved command is shell text that a later run sends to a shell tab, so
// saving, changing and deleting one is behind the phone shell's gate too;
// listing stays open.
func TestSavedCommandEditsAreGatedLikeTheShell(t *testing.T) {
	h, _, _ := newShellHarness(t, false)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/spaces/w1/commands"},
		{http.MethodPut, "/api/spaces/w1/commands/abc"},
		{http.MethodDelete, "/api/spaces/w1/commands/abc"},
	} {
		code, body := h.sendJSON(tc.method, tc.path, map[string]any{"name": "Run tests", "command": "make"}, nil)
		if code != http.StatusForbidden || !strings.Contains(body, "like the shell") {
			t.Errorf("%s %s with the shell off = %d %s", tc.method, tc.path, code, body)
		}
	}
	if code := h.getJSON("/api/spaces/w1/commands", nil); code == http.StatusForbidden {
		t.Error("listing saved commands is refused with the shell off")
	}
	h.srv.shellOn.Store(true)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/spaces/w1/commands"},
		{http.MethodDelete, "/api/spaces/w1/commands/abc"},
	} {
		if code, _ := h.sendJSON(tc.method, tc.path, map[string]any{"name": "x", "command": "make"}, nil); code == http.StatusForbidden {
			t.Errorf("%s %s refused with the shell on", tc.method, tc.path)
		}
	}
}
