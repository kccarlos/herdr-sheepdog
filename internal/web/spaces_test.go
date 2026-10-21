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
	h, _, _ := newShellHarness(t, "lan", false)
	if code := h.getJSON("/api/spaces", nil); code != http.StatusBadGateway {
		t.Errorf("GET /api/spaces on mock herdr = %d", code)
	}
	if code := h.getJSON("/api/projects", nil); code == http.StatusOK {
		t.Error("the old /api/projects route still answers")
	}
	if code, body := h.postJSON("/api/spaces/w1/commands/abc/run", map[string]any{}, nil); code != http.StatusForbidden || !strings.Contains(body, "like the shell") {
		t.Errorf("run on a plain LAN with the shell off = %d %s", code, body)
	}
	h.srv.kindHook = func(string) string { return "loopback" }
	if code, _ := h.postJSON("/api/spaces/w1/commands/abc/run", map[string]any{}, nil); code == http.StatusForbidden {
		t.Error("run refused on loopback")
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
