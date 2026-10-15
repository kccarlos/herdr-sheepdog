package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeExe makes a file that stands in for this sheepdog binary.
func fakeExe(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "sheepdog")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := osExecutable
	osExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { osExecutable = old })
	return exe
}

func TestUninstallWithoutServiceKeepData(t *testing.T) {
	_, calls := fakeService(t)
	fakeExe(t)
	cfg, data, _ := lifeSetup(t, "")
	_ = os.MkdirAll(data, 0o700)
	_ = os.WriteFile(filepath.Join(data, "state.json"), []byte("{}"), 0o600)
	code, out, errs := runCLI("uninstall", "--keep-data", "--config", cfg)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	for _, want := range []string{"sheepdog is not running", "login service: not installed", "kept " + cfg, "kept " + data} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[len(lines)-1] != "  herdr plugin uninstall kccarlos.sheepdog" {
		t.Errorf("last step is not the herdr command:\n%s", out)
	}
	if _, err := os.Stat(data); err != nil {
		t.Error("--keep-data deleted the data")
	}
	for _, c := range *calls {
		if strings.Contains(c, "herdr") {
			t.Errorf("uninstall ran a herdr command: %s", c)
		}
	}
}

func TestUninstallRemovesServiceLinkAndAsks(t *testing.T) {
	home, calls := fakeService(t)
	exe := fakeExe(t)
	cfg, data, _ := lifeSetup(t, "")
	_ = os.MkdirAll(data, 0o700)
	_ = os.WriteFile(filepath.Join(data, "state.json"), []byte("{}"), 0o600)
	spec, err := serviceSpec(cfg, "", exe)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Dir(spec.UnitPath()), 0o755)
	_ = os.WriteFile(spec.UnitPath(), []byte("x"), 0o644)
	if !strings.HasPrefix(spec.UnitPath(), home) {
		t.Fatalf("unit outside the fake home: %s", spec.UnitPath())
	}
	link := localBin()
	_ = os.MkdirAll(filepath.Dir(link), 0o755)
	_ = os.Remove(link)
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	withStdin(t, "n\ny\n") // keep the config, delete the data
	code, out, errs := runCLI("uninstall", "--config", cfg)
	if code != 0 {
		t.Fatalf("exit %d: %s\n%s", code, errs, out)
	}
	if _, err := os.Stat(spec.UnitPath()); err == nil {
		t.Error("unit file left behind")
	}
	if _, err := os.Lstat(link); err == nil {
		t.Error("~/.local/bin link left behind")
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Error("config deleted after a no")
	}
	if _, err := os.Stat(data); err == nil {
		t.Error("data kept after a yes")
	}
	if !strings.Contains(out, "removed the link "+link) || !strings.Contains(out, "herdr plugin uninstall kccarlos.sheepdog") {
		t.Errorf("output:\n%s", out)
	}
	if len(*calls) == 0 {
		t.Error("the service manager was not asked to remove the service")
	}
}

func TestUninstallLeavesAForeignLink(t *testing.T) {
	fakeService(t)
	exe := fakeExe(t)
	other := filepath.Join(t.TempDir(), "sheepdog")
	_ = os.WriteFile(other, nil, 0o755)
	link := localBin()
	_ = os.MkdirAll(filepath.Dir(link), 0o755)
	_ = os.Remove(link)
	_ = os.Symlink(other, link)
	t.Cleanup(func() { _ = os.Remove(link) })
	if msg := removeLocalBinLink(exe); !strings.Contains(msg, "points to another sheepdog") {
		t.Errorf("msg %q", msg)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Error("a link to another sheepdog was removed")
	}
}

// TestUninstallCustomConfigDeletesOnlyTheFile: --config in a project
// directory must never take the directory with it.
func TestUninstallCustomConfigDeletesOnlyTheFile(t *testing.T) {
	fakeService(t)
	fakeExe(t)
	cfg, data, _ := lifeSetup(t, "")
	sibling := filepath.Join(filepath.Dir(cfg), "my-project-notes.txt")
	_ = os.WriteFile(sibling, []byte("keep me"), 0o600)
	_ = os.MkdirAll(filepath.Join(data, "events"), 0o700)
	code, out, errs := runCLI("uninstall", "--yes", "--config", cfg)
	if code != 0 {
		t.Fatalf("exit %d: %s\n%s", code, errs, out)
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Error("config file kept")
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Error("uninstall deleted a file next to a custom --config")
	}
	if _, err := os.Stat(data); err == nil {
		t.Error("data dir kept with --yes")
	}
}

// TestDataDirGuard: a data_dir that is the home directory, an ancestor of
// it, or not a sheepdog data dir is never deleted.
func TestDataDirGuard(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, d := range []string{"/", home, filepath.Dir(home), t.TempDir()} {
		if err := checkDataDir(d); err == nil {
			t.Errorf("%s accepted", d)
		}
	}
	ok := t.TempDir()
	_ = os.MkdirAll(filepath.Join(ok, "events"), 0o700)
	if err := checkDataDir(ok); err != nil {
		t.Errorf("a real data dir refused: %v", err)
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(cfg, []byte(fmt.Sprintf("data_dir = %q\n[web]\nport = %d\n", home, freePort(t))), 0o600)
	if code, _, errs := runCLI("data", "clear", "--all", "--yes", "--config", cfg); code != 1 || !strings.Contains(errs, "refusing to delete") {
		t.Errorf("data clear --all on HOME: exit %d %q", code, errs)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatal("HOME deleted")
	}
}
