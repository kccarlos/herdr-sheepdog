package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotatingFileRotatesBySizeAndKeepsMaxFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "sheepdog.log")
	r, err := OpenRotating(path, 200, 2)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return time.Date(2026, 10, 14, 1, 2, 3, 0, time.UTC) }
	line := strings.Repeat("x", 50) + "\n"
	for i := 0; i < 20; i++ {
		_, _ = r.Write([]byte(line))
	}
	_ = r.Close()
	for _, p := range []string{path, path + ".1", path + ".2"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if st.Size() > 200 {
			t.Errorf("%s is %d bytes, over the limit", p, st.Size())
		}
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Error("kept more than max_files rotated files")
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), "2026-10-14T01:02:03.000Z xxxx") {
		t.Errorf("no timestamp prefix: %q", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestRotatingFilePartialLinesGetOneStamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.log")
	r, _ := OpenRotating(path, 0, 0)
	_, _ = r.Write([]byte("hel"))
	_, _ = r.Write([]byte("lo\nworld\n"))
	_ = r.Close()
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "Z hello") || !strings.HasSuffix(lines[1], "Z world") {
		t.Errorf("got %q", b)
	}
}

func TestEventMirrorLevels(t *testing.T) {
	events := []string{"voice.connect_error", "eventlog.subscriber_dropped", "daemon.start", "herdr.state"}
	want := map[string][]string{
		LevelDebug: {"ERROR voice.connect_error", "WARN eventlog.subscriber_dropped", "INFO daemon.start", "DEBUG herdr.state"},
		LevelInfo:  {"ERROR voice.connect_error", "WARN eventlog.subscriber_dropped", "INFO daemon.start"},
		LevelWarn:  {"ERROR voice.connect_error", "WARN eventlog.subscriber_dropped"},
		LevelError: {"ERROR voice.connect_error"},
	}
	for level, lines := range want {
		var b strings.Builder
		m := NewEventMirror(&b, level)
		for _, e := range events {
			m.Log("x", e, "", map[string]any{"error": "boom"})
		}
		got := strings.Split(strings.TrimSpace(b.String()), "\n")
		if len(got) != len(lines) {
			t.Errorf("%s: got %q", level, got)
			continue
		}
		for i := range lines {
			if !strings.HasPrefix(got[i], lines[i]) {
				t.Errorf("%s: line %d = %q, want prefix %q", level, i, got[i], lines[i])
			}
		}
	}
}
