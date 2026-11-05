package web

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func TestAuditSensitiveRoots(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(home, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(data, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	h := newHarness(t, harnessOpts{dataDir: data})
	trackIn(t, h, "w1:p1", home)
	trackIn(t, h, "w1:p2", filepath.Dir(home))
	trackIn(t, h, "w1:p3", data)
	trackIn(t, h, "w1:p4", filepath.Join(data, "sub"))
	trackIn(t, h, "w1:p5", filepath.Join(home, "project"))

	for _, tc := range []struct {
		pane   string
		reason string
		errSub string
	}{
		{"w1:p1", "home_root", "home folder"},
		{"w1:p2", "home_root", "home folder"},
		{"w1:p3", "private_root", "data folder"},
		{"w1:p4", "private_root", "data folder"},
	} {
		for _, suffix := range []string{"files", "changes"} {
			var body map[string]string
			code := h.getJSON(windowPath(tc.pane, suffix), &body)
			if code != 403 || body["reason"] != tc.reason || !strings.Contains(body["error"], tc.errSub) {
				t.Errorf("%s %s = %d %v, want 403 %s (%s)", tc.pane, suffix, code, body, tc.reason, tc.errSub)
			}
		}
	}
	if code := h.getJSON(windowPath("w1:p5", "files"), nil); code != 200 {
		t.Errorf("project under home = %d, want 200", code)
	}
}

func TestAuditMissingTicketClosesStream(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	d := h.pairDevice()
	r := h.trackedRequest(d)
	h.srv.auth.mu.Lock()
	delete(h.srv.auth.tickets, d.ticket)
	h.srv.auth.mu.Unlock()
	closed := make(chan struct{}, 1)
	untrack, _ := h.srv.track(r, func(string) { closed <- struct{}{} })
	defer untrack()
	select {
	case <-closed:
	default:
		t.Fatal("missing ticket left stream open")
	}
}

func TestAuditAttachmentCountLimit(t *testing.T) {
	data := t.TempDir()
	dir := filepath.Join(data, "attachments", "pane")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < attachmentMaxFiles+1; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{log: logging.NopLogger{}}
	s.deps.DataDir = data
	s.deps.Now = time.Now
	s.pruneAttachments()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != attachmentMaxFiles {
		t.Fatalf("files = %d", len(files))
	}
}
