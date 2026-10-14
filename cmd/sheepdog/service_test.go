package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/service"
)

// fakeService points `sheepdog service` at a temp home and records the
// service-manager commands instead of running them.
func fakeService(t *testing.T) (home string, calls *[]string) {
	t.Helper()
	home = t.TempDir()
	var got []string
	oldRun, oldHome := serviceRunner, serviceHome
	serviceRunner = func(_ context.Context, name string, args ...string) (string, error) {
		got = append(got, name+" "+strings.Join(args, " "))
		return "", nil
	}
	serviceHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { serviceRunner, serviceHome = oldRun, oldHome })
	return home, &got
}

func TestServiceDryRun(t *testing.T) {
	home, calls := fakeService(t)
	cfg := filepath.Join(home, "config.toml")
	var out, errb strings.Builder
	code := run(context.Background(), []string{"service", "install", "--dry-run", "--binary", "/usr/local/bin/sheepdog", "--config", cfg, "--env-file", filepath.Join(home, "x.env")}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := service.Spec{OS: runtimeGOOS, Home: home}
	if !strings.Contains(out.String(), "would write "+s.UnitPath()) || !strings.Contains(out.String(), "/usr/local/bin/sheepdog") || !strings.Contains(out.String(), cfg) {
		t.Errorf("dry run output:\n%s", out.String())
	}
	if len(*calls) != 0 {
		t.Errorf("dry run ran %q", *calls)
	}
	if _, err := os.Stat(s.UnitPath()); !os.IsNotExist(err) {
		t.Errorf("dry run wrote the unit: %v", err)
	}
}

func TestServiceInstallStatusUninstall(t *testing.T) {
	home, calls := fakeService(t)
	cfg := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfg, []byte("[web]\nport = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	common := []string{"--binary", "/usr/local/bin/sheepdog", "--config", cfg}
	var out, errb strings.Builder
	if code := run(context.Background(), append([]string{"service", "install"}, common...), &out, &errb); code != 0 {
		t.Fatalf("install exit %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "does not exist yet (run sheepdog init)") {
		t.Errorf("no warning about the missing env file: %q", errb.String())
	}
	if len(*calls) == 0 {
		t.Error("install ran no service-manager command")
	}
	out.Reset()
	if code := run(context.Background(), append([]string{"service", "status"}, common...), &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "installed (") {
		t.Errorf("status exit %d: %q", code, out.String())
	}
	out.Reset()
	if code := run(context.Background(), append([]string{"service", "uninstall"}, common...), &out, &errb); code != 0 || !strings.Contains(out.String(), "stopped and removed") {
		t.Errorf("uninstall exit %d: %q", code, out.String())
	}
	if code := run(context.Background(), []string{"service", "bogus"}, &out, &errb); code != 2 {
		t.Errorf("unknown verb exit %d", code)
	}
}

func TestStableExecutableKeepsPlainPath(t *testing.T) {
	exe, err := stableExecutable()
	// The test binary is built by go test into a go-build temp dir.
	if err == nil && strings.Contains(exe, "go-build") {
		t.Errorf("a go-build path was accepted: %s", exe)
	}
}
