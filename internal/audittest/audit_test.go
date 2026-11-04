package audittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditData(t *testing.T) {
	root := filepath.Join("..", "..", "scripts", "e2e")
	run := func(args ...string) (string, error) {
		b, err := exec.Command("python3", append([]string{filepath.Join(root, "audit_data.py")}, args...)...).CombinedOutput()
		return string(b), err
	}
	a, err := run("scope", "2026-11-04")
	if err != nil {
		t.Fatal(err)
	}
	b, err := run("scope", "2026-11-11")
	if err != nil || a != b {
		t.Fatalf("weekly rotation: %q %q %v", a, b, err)
	}
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	findings := write("findings", `{"file":"x.go","line":1,"scenario":"bad","fix":"fix","severity":"high"}`)
	verdicts := write("verdicts", `{"id":1,"verdict":"confirmed","reason":"evidence"}`)
	report := write("report", "# Audit\n")
	selected := filepath.Join(dir, "selected")
	if out, err := run("render", findings, verdicts, report, selected); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	data, err := os.ReadFile(selected)
	if err != nil || !strings.Contains(string(data), `"confirmed"`) {
		t.Fatalf("selection: %s %v", data, err)
	}
	write("verdicts", `{"id":2,"verdict":"confirmed","reason":"wrong"}`)
	if _, err := run("render", findings, verdicts, report, selected); err == nil {
		t.Fatal("accepted unmatched verdict")
	}
}
