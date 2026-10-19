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
	if !strings.Contains(errb.String(), "does not exist yet (run sheepdog setup)") {
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

func TestServiceStartNotInstalled(t *testing.T) {
	home, calls := fakeService(t)
	var out, errb strings.Builder
	code := run(context.Background(), []string{"service", "start", "--binary", "/usr/local/bin/sheepdog"}, &out, &errb)
	if code != 0 || !strings.Contains(out.String(), "not installed, nothing started") {
		t.Errorf("exit %d: %q %q", code, out.String(), errb.String())
	}
	if len(*calls) != 0 {
		t.Errorf("ran %q", *calls)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd")); err == nil {
		t.Error("start created service files")
	}
}

func TestServiceStartInstalled(t *testing.T) {
	_, calls := fakeService(t)
	common := []string{"--binary", "/usr/local/bin/sheepdog"}
	var out, errb strings.Builder
	if code := run(context.Background(), append([]string{"service", "install"}, common...), &out, &errb); code != 0 {
		t.Fatalf("install exit %d: %s", code, errb.String())
	}
	*calls = nil
	out.Reset()
	if code := run(context.Background(), append([]string{"service", "start"}, common...), &out, &errb); code != 0 || out.String() == "" {
		t.Errorf("start exit %d: %q", code, out.String())
	}
	if len(*calls) == 0 {
		t.Error("start asked the service manager nothing")
	}
}

// init's last step offers the login service: yes installs it, no (or a
// service that is there already) does not.
func TestInitOffersService(t *testing.T) {
	fakeKeyAPI(t)
	noOnboardKeys(t)
	_, calls := fakeService(t)
	oldExe := osExecutable
	osExecutable = func() (string, error) { return "/usr/local/bin/sheepdog", nil }
	t.Cleanup(func() { osExecutable = oldExe })
	// A fresh config dir per run, one service home for all three.
	args := func() []string {
		return []string{"--config", filepath.Join(t.TempDir(), "config.toml"), "--orchestrator", "gemini", "--skip-validate"}
	}

	code, out := runInitTest(t, "\n"+initGeminiKey+"\n\n\n\nn\n", args()...)
	if code != 0 || !strings.Contains(out, "Start at login (optional)") || !strings.Contains(out, "skipped") {
		t.Fatalf("decline: code %d: %s", code, out)
	}
	if len(*calls) != 0 {
		t.Errorf("declined, but ran %q", *calls)
	}
	code, out = runInitTest(t, "\n"+initGeminiKey+"\n\n\n\ny\n", args()...)
	if code != 0 || len(*calls) == 0 || !strings.Contains(out, "wrote ") {
		t.Fatalf("accept: code %d calls %q: %s", code, *calls, out)
	}
	*calls = nil
	code, out = runInitTest(t, "\n"+initGeminiKey+"\n\n\n\n", args()...)
	if code != 0 || !strings.Contains(out, "already installed") || len(*calls) > 1 {
		t.Errorf("already installed: code %d calls %q: %s", code, *calls, out)
	}
}

// Non-interactive init asks nothing, the service included.
func TestInitNonInteractiveDoesNotOfferService(t *testing.T) {
	fakeKeyAPI(t)
	noOnboardKeys(t)
	t.Setenv("GEMINI_API_KEY", initGeminiKey)
	_, calls := fakeService(t)
	code, out := runInitTest(t, "", "--non-interactive", "--config", filepath.Join(t.TempDir(), "config.toml"))
	if code != 0 || strings.Contains(out, "login service") || len(*calls) != 0 {
		t.Errorf("code %d calls %q: %s", code, *calls, out)
	}
}
