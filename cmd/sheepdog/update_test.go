package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/update"
)

// fakeUpdateTools puts a logging brew and herdr (no Sheepdog.app cask) on
// PATH and makes os.Args[0] a sheepdog that reports version installed.
func fakeUpdateTools(t *testing.T, installed string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	for _, name := range []string{"brew", "herdr"} {
		script := "#!/bin/sh\nif [ \"$1 $2\" = 'list --cask' ]; then exit 1; fi\necho " + name + " \"$@\" >> " + log + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(dir, "sheepdog")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho 'sheepdog "+installed+" (commit x, built y)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{exe}
	return log
}

func TestUpdateRestartsOnlyADaemonOnAnotherVersion(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.2.0","html_url":"https://example/v0.2.0"}]`))
	}))
	defer api.Close()
	oldAPI, oldVersion, oldExe, oldDir := releaseAPI, version, osExecutable, updateDataDir
	t.Cleanup(func() { releaseAPI, version, osExecutable, updateDataDir = oldAPI, oldVersion, oldExe, oldDir })
	releaseAPI, version = api.URL, "v0.1.0"
	osExecutable = func() (string, error) { return "/opt/homebrew/Cellar/sheepdog/0.1.0/bin/sheepdog", nil }
	updateDataDir = func() string { return t.TempDir() }
	fakeUpdateTools(t, "v0.2.0")
	port := freePort(t)
	t.Setenv("SHEEPDOG_WEB_PORT", fmt.Sprint(port))

	running := "v0.2.0"
	daemonSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"sheepdog","version":"` + running + `"}`))
	}))
	ln, err := listenOn(port)
	if err != nil {
		t.Fatal(err)
	}
	daemonSrv.Listener = ln
	daemonSrv.Start()
	defer daemonSrv.Close()

	// Already on the new version: no restart, the version it reports.
	code, out, errs := runCLI("update")
	if code != 0 || !strings.HasSuffix(out, "sheepdog v0.2.0 is running.\n") || strings.Contains(out+errs, "stop") {
		t.Errorf("current daemon: exit %d\n%s\n%s", code, out, errs)
	}
	// On the old version: restarted (here the stop refuses a daemon
	// without a pid file, which shows the restart was asked for).
	running = "v0.1.0"
	code, _, errs = runCLI("update")
	if code != 1 || !strings.Contains(errs, "left no pid file") {
		t.Errorf("old daemon: exit %d\n%s", code, errs)
	}
}

// herdr 0.9.4+ installs each plugin version in its own folder
// (github-installations/<id>/<n>/checkout) and removes the old one after
// `herdr plugin update`; plugin-fetch.sh moves ~/.local/bin/sheepdog to the
// new one. The login service must record that link, not the folder.
func TestStableExecutableOutlivesAHerdrPluginUpdate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	install := func(n string) string {
		root := filepath.Join(home, ".config", "herdr", "plugins", "github-installations", "kccarlos.sheepdog", n, "checkout")
		_ = os.MkdirAll(filepath.Join(root, "bin"), 0o755)
		_ = os.WriteFile(filepath.Join(root, "herdr-plugin.toml"), nil, 0o644)
		bin := filepath.Join(root, "bin", "sheepdog")
		_ = os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
		link := filepath.Join(home, ".local", "bin", "sheepdog")
		_ = os.MkdirAll(filepath.Dir(link), 0o755)
		_ = os.Remove(link)
		if err := os.Symlink(bin, link); err != nil {
			t.Fatal(err)
		}
		return bin
	}
	first := install("1")
	oldExe := osExecutable
	t.Cleanup(func() { osExecutable = oldExe })
	// What Linux's /proc/self/exe gives, and macOS for the plugin's popup.
	osExecutable = func() (string, error) { return first, nil }
	recorded, err := stableExecutable()
	if err != nil {
		t.Fatal(err)
	}
	install("2")
	_ = os.RemoveAll(filepath.Dir(filepath.Dir(filepath.Dir(first))))
	if _, err := os.Stat(recorded); err != nil {
		t.Errorf("the login service would run %s, gone after the update: %v", recorded, err)
	}
}

// POST /api/update does what `sheepdog update` does: refuses when there is
// nothing to install or a source checkout, otherwise installs in the
// background, then asks for the update restart at once; a second press
// while it runs is refused, and a failure is reported in the status.
func TestInstallNowInstallsThenRestarts(t *testing.T) {
	oldVersion, oldExe := version, osExecutable
	t.Cleanup(func() { version, osExecutable = oldVersion, oldExe })
	version = "v0.1.0"
	osExecutable = func() (string, error) { return "/opt/homebrew/Cellar/sheepdog/0.1.0/bin/sheepdog", nil }
	log := fakeUpdateTools(t, "v0.2.0")
	tracker := func(tag string) *update.Tracker {
		tr := update.NewTracker(t.TempDir(), "v0.1.0", "sheepdog update", false)
		tr.SetCache(update.Cache{Tag: tag})
		return tr
	}
	var mu sync.Mutex
	var notes []string
	note := func(typ string, _ map[string]any) { mu.Lock(); notes = append(notes, typ); mu.Unlock() }
	restarted := make(chan string, 1)
	trigger := func(_, to string) error { restarted <- to; return nil }

	if err := installNow(context.Background(), tracker("v0.1.0"), trigger, note, io.Discard)(); err == nil || !strings.Contains(err.Error(), "up to date") {
		t.Errorf("nothing newer: %v", err)
	}
	tr := tracker("v0.2.0")
	start := installNow(context.Background(), tr, trigger, note, io.Discard)
	if !tr.Status().CanInstall {
		t.Error("a Homebrew install cannot install")
	}
	if err := start(); err != nil {
		t.Fatal(err)
	}
	if err := start(); err == nil || tr.Status().State != "installing" {
		t.Errorf("second press: %v, state %q", err, tr.Status().State)
	}
	select {
	case to := <-restarted:
		if update.VersionOf(to) != "v0.2.0" {
			t.Errorf("restarted into %q", to)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no restart after the install")
	}
	if b, _ := os.ReadFile(log); string(b) != "brew update\nbrew upgrade kccarlos/tap/sheepdog\n" {
		t.Errorf("ran:\n%s", b)
	}

	// The restart cannot start: the status says why.
	tr = tracker("v0.2.0")
	done := make(chan struct{})
	failing := func(_, _ string) error { defer close(done); return errors.New("no restart today") }
	if err := installNow(context.Background(), tr, failing, note, io.Discard)(); err != nil {
		t.Fatal(err)
	}
	<-done
	for i := 0; i < 100 && tr.Status().State != "failed"; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if st := tr.Status(); st.State != "failed" || st.Error != "no restart today" {
		t.Errorf("failure: %+v", st)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(notes, " ") != "update.installing update.installing update.failed" {
		t.Errorf("events: %v", notes)
	}

	// A source checkout gets the steps, not an install.
	root := t.TempDir()
	for _, f := range []string{"go.mod", "herdr-plugin.toml"} {
		_ = os.WriteFile(filepath.Join(root, f), nil, 0o644)
	}
	osExecutable = func() (string, error) { return filepath.Join(root, "bin", "sheepdog"), nil }
	tr = update.NewTracker(t.TempDir(), "v0.1.0", "git pull", false)
	tr.SetCache(update.Cache{Tag: "v0.2.0"})
	if err := installNow(context.Background(), tr, trigger, note, io.Discard)(); err == nil || !strings.Contains(err.Error(), "git pull") {
		t.Errorf("source checkout: %v", err)
	}
}
