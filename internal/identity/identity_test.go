package identity

import (
	"os"
	"path/filepath"
	"testing"
)

// The id is made once, kept 0600 in a 0700 directory, and read back.
func TestDaemonIDIsMadeOnceAndKept(t *testing.T) {
	dir := t.TempDir()
	id, err := DaemonID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !idPattern.MatchString(id) {
		t.Fatalf("id = %q", id)
	}
	again, err := DaemonID(dir)
	if err != nil || again != id {
		t.Fatalf("again = %q, %v; want %q", again, err, id)
	}
	fi, err := os.Stat(filepath.Join(dir, "tls", "daemon_id"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, %v", fi.Mode(), err)
	}
	di, err := os.Stat(filepath.Join(dir, "tls"))
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, %v", di.Mode(), err)
	}
	if other, _ := DaemonID(t.TempDir()); other == id {
		t.Error("two data directories share an id")
	}
}

// A damaged id file is an error, not a new identity.
func TestDaemonIDRefusesADamagedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tls"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls", "daemon_id"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DaemonID(dir); err == nil {
		t.Error("a damaged id was accepted")
	}
}

func TestNamePrefersTheConfiguredOne(t *testing.T) {
	if got := Name("  Studio Mac "); got != "Studio Mac" {
		t.Errorf("Name = %q", got)
	}
	if got := Name(""); got == "" {
		t.Error("no system name")
	}
}
