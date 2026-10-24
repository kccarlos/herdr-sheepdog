package onboard

import (
	"os"
	"strings"
	"testing"
)

func TestCheckProjectRoots(t *testing.T) {
	ok := CheckProjectRoots([]string{t.TempDir()})
	if len(ok) != 1 || ok[0].Status != OK {
		t.Fatalf("readable = %+v", ok)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	got := CheckProjectRoots([]string{dir, dir})
	if len(got) != 1 || got[0].Status != Warn || strings.Count(got[0].Detail, dir) != 1 || !strings.Contains(got[0].Fix, "Files and Folders") {
		t.Errorf("blocked = %+v", got)
	}
}
