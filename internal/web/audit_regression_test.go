package web

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func TestAuditSensitiveRoots(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(t.TempDir(), "data")
	t.Setenv("HOME", home)
	s := &Server{}
	s.deps.DataDir = data
	for _, p := range []string{home, filepath.Dir(home), data, filepath.Join(data, "sub")} {
		if !s.privateRoot(p) {
			t.Errorf("allowed %s", p)
		}
	}
	if s.privateRoot(filepath.Join(home, "project")) {
		t.Error("project under home refused")
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
