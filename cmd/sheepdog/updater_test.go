package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/update"
)

const (
	runningLine = "sheepdog v0.1.0 (commit aaa, built d)"
	newLine     = "sheepdog v0.2.0 (commit bbb, built d)"
)

// fakeInstall writes a script that answers `version` with runningLine, the
// stand-in for the executable an upgrade replaces, and makes the test
// daemon believe it was started from it.
func fakeInstall(t *testing.T) (write func(string)) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "sheepdog")
	write = func(l string) {
		tmp := exe + ".new"
		if err := os.WriteFile(tmp, []byte("#!/bin/sh\necho '"+l+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(tmp, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
		if err := os.Rename(tmp, exe); err != nil {
			t.Fatal(err)
		}
	}
	write(runningLine)
	oldArgs, oldExe, oldVer, oldCommit, oldDate := os.Args, osExecutable, version, commit, date
	t.Cleanup(func() { os.Args, osExecutable, version, commit, date = oldArgs, oldExe, oldVer, oldCommit, oldDate })
	os.Args = []string{exe}
	osExecutable = func() (string, error) { return exe, nil }
	version, commit, date = "v0.1.0", "aaa", "d"
	return write
}

// serveForUpdate runs `serve` on a scratch data dir with fast update timers.
func serveForUpdate(t *testing.T, extraCfg string) (base, dataDir string, cancel context.CancelFunc, done chan int, stdout *syncBuffer) {
	t.Helper()
	noKeys(t)
	cfgPath, dataDir, port := lifeSetup(t, extraCfg)
	oldTick, oldDelay := updateTick, updateStartDelay
	updateTick, updateStartDelay = 20*time.Millisecond, time.Hour
	t.Cleanup(func() { updateTick, updateStartDelay = oldTick, oldDelay })
	var ctx context.Context
	ctx, cancel = context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stdout = &syncBuffer{}
	stderr := &syncBuffer{}
	done = make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--config", cfgPath, "--voice", "off", "--herdr", "mock"}, stdout, stderr)
	}()
	base = fmt.Sprintf("http://127.0.0.1:%d", port)
	waitFor(t, func() bool { _, err := daemon.GetHello(ctx, base); return err == nil })
	return base, dataDir, cancel, done, stdout
}

func stateUpdate(t *testing.T, base string) update.Status {
	t.Helper()
	s, ok := tryStateUpdate(base)
	if !ok {
		t.Fatal("/api/state has no update object")
	}
	return s
}

// tryStateUpdate reads /api/state's update object (false while the daemon
// is rebuilding itself and does not answer).
func tryStateUpdate(base string) (update.Status, bool) {
	resp, err := http.Get(base + "/api/state")
	if err != nil {
		return update.Status{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	var st struct {
		Update *update.Status `json:"update"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&st)
	if st.Update == nil {
		return update.Status{}, false
	}
	return *st.Update, true
}

func TestServeReexecsIntoNewBinaryWhenIdle(t *testing.T) {
	upgrade := fakeInstall(t)
	type call struct {
		path string
		argv []string
		held bool
	}
	calls := make(chan call, 4)
	var dataDirForExec atomic.Value
	oldExec, oldSvc := execSelf, underService
	t.Cleanup(func() { execSelf, underService = oldExec, oldSvc })
	underService = func() bool { return false }
	execSelf = func(path string, argv, _ []string) error {
		dd, _ := dataDirForExec.Load().(string)
		_, held := daemon.Running(dd)
		calls <- call{path, argv, held}
		return errors.New("test: not really exec'd")
	}
	base, dataDir, cancel, done, stdout := serveForUpdate(t, "")
	dataDirForExec.Store(dataDir)

	if s := stateUpdate(t, base); s.RestartPending || s.Current != "v0.1.0" || !s.AutoRestart {
		t.Fatalf("before the upgrade: %+v", s)
	}
	upgrade(newLine)
	var c call
	select {
	case c = <-calls:
	case <-time.After(5 * time.Second):
		t.Fatalf("no re-exec after the upgrade:\n%s", stdout.String())
	}
	if c.path != os.Args[0] || len(c.argv) < 2 || c.argv[0] != os.Args[0] || c.argv[1] != "serve" || !strings.Contains(strings.Join(c.argv, " "), "--herdr mock") {
		t.Errorf("exec %q %v", c.path, c.argv)
	}
	if c.held {
		t.Error("the single-instance lock must be released before exec")
	}
	if !strings.Contains(stdout.String(), "restarting into it") {
		t.Errorf("no line before the restart:\n%s", stdout.String())
	}
	// The exec "failed": the daemon carries on, shows the notice, and does
	// not try again.
	waitFor(t, func() bool {
		s, ok := tryStateUpdate(base)
		return ok && s.RestartPending && s.RestartTo == "v0.2.0" && !s.AutoRestart
	})
	select {
	case c := <-calls:
		t.Fatalf("a second exec attempt: %v", c.argv)
	case <-time.After(300 * time.Millisecond):
	}
	if !update.Attempted(dataDir, newLine, time.Now()) {
		t.Error("the attempt is not recorded")
	}
	evs, _ := filepath.Glob(filepath.Join(dataDir, "events", "*.jsonl"))
	var logged bool
	for _, f := range evs {
		b, _ := os.ReadFile(f)
		logged = logged || bytes.Contains(b, []byte(`"daemon.restart"`)) && bytes.Contains(b, []byte(`"to":"v0.2.0"`)) && bytes.Contains(b, []byte(`"from":"v0.1.0"`))
	}
	if !logged {
		t.Error("daemon.restart with the old and new versions not logged")
	}
	if _, held := daemon.Running(dataDir); !held {
		t.Error("the daemon must hold its lock again after a failed exec")
	}
	cancel()
	<-done
}

func TestServeExitsForTheLoginServiceAfterAnUpgrade(t *testing.T) {
	upgrade := fakeInstall(t)
	oldExec, oldSvc := execSelf, underService
	t.Cleanup(func() { execSelf, underService = oldExec, oldSvc })
	underService = func() bool { return true }
	execSelf = func(string, []string, []string) error { t.Error("exec under the login service"); return nil }
	_, dataDir, _, done, _ := serveForUpdate(t, "")
	upgrade(newLine)
	select {
	case code := <-done:
		if code != exitUpdateRestart || code == 0 {
			t.Fatalf("exit %d, want %d (non-zero: systemd Restart=on-failure)", code, exitUpdateRestart)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit for the service to restart it")
	}
	if _, held := daemon.Running(dataDir); held {
		t.Error("lock still held after exit")
	}
}

// The watcher marks the restart pending as soon as the file changes, and
// only learns the new version (and whether it may restart into it) once
// `sheepdog version` answers, so the test waits for that answer
// (RestartTo) rather than for RestartPending plus a fixed sleep: a slow
// probe under load used to leave AutoRestart still true at the assert.
func TestServeNoRestartWhenAutoRestartOffOrSameVersion(t *testing.T) {
	for name, tc := range map[string]struct{ cfg, line, to string }{
		"auto_restart off": {"[update]\nauto_restart = false\n", newLine, "v0.2.0"},
		"same version":     {"", runningLine, "v0.1.0"},
	} {
		t.Run(name, func(t *testing.T) {
			upgrade := fakeInstall(t)
			oldExec, oldSvc := execSelf, underService
			t.Cleanup(func() { execSelf, underService = oldExec, oldSvc })
			underService = func() bool { return false }
			execSelf = func(string, []string, []string) error { t.Error("exec"); return nil }
			base, _, cancel, done, _ := serveForUpdate(t, tc.cfg)
			upgrade(tc.line)
			waitFor(t, func() bool { s, ok := tryStateUpdate(base); return ok && s.RestartPending && s.RestartTo == tc.to })
			// Time for a wrong exec to show up (execSelf fails the test).
			time.Sleep(200 * time.Millisecond)
			if s := stateUpdate(t, base); s.AutoRestart || !s.RestartPending || s.RestartTo != tc.to {
				t.Errorf("%+v", s)
			}
			cancel()
			<-done
		})
	}
}

func TestCLINotices(t *testing.T) {
	cfgPath, dataDir, port := lifeSetup(t, "")
	l, code, ok := loadLifecycle(context.Background(), "t", cfgPath, "", &bytes.Buffer{}, &bytes.Buffer{})
	if !ok {
		t.Fatalf("load: %d", code)
	}
	oldVer, oldExe := version, osExecutable
	t.Cleanup(func() { version, osExecutable = oldVer, oldExe })
	version = "v0.2.0"
	osExecutable = func() (string, error) { return "/opt/homebrew/Cellar/sheepdog/0.2.0/bin/sheepdog", nil }

	// A daemon that still runs v0.1.0.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"sheepdog","version":"v0.1.0"}`))
	}))
	ln, err := listenOn(port)
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	now := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	printCLINotices(context.Background(), l, &out, now)
	want := "The running sheepdog is v0.1.0; v0.2.0 is installed. Run `sheepdog restart` to use it."
	if !strings.Contains(out.String(), want) {
		t.Errorf("no mismatch line:\n%s", out.String())
	}
	if strings.Contains(out.String(), "is available") {
		t.Errorf("no cache, no update line:\n%s", out.String())
	}

	// A newer release is known: one line a day, with the command.
	_ = update.WriteCache(dataDir, update.Cache{CheckedAt: now, Tag: "v0.3.0"})
	out.Reset()
	printCLINotices(context.Background(), l, &out, now)
	if !strings.Contains(out.String(), "sheepdog v0.3.0 is available (you have v0.2.0). To update: brew upgrade sheepdog, then sheepdog restart") {
		t.Errorf("update line:\n%s", out.String())
	}
	out.Reset()
	printCLINotices(context.Background(), l, &out, now.Add(time.Hour))
	if strings.Contains(out.String(), "is available") {
		t.Errorf("second notice within a day:\n%s", out.String())
	}
	out.Reset()
	printCLINotices(context.Background(), l, &out, now.Add(25*time.Hour))
	if !strings.Contains(out.String(), "is available") {
		t.Errorf("no notice the next day:\n%s", out.String())
	}

	// update.check = false: nothing about releases.
	l.cfg.Update.Check = false
	_ = os.Remove(filepath.Join(dataDir, "update-notice.json"))
	out.Reset()
	printCLINotices(context.Background(), l, &out, now)
	if strings.Contains(out.String(), "is available") {
		t.Errorf("notice with update.check off:\n%s", out.String())
	}
}

func TestFlagValueAndNoticeCommands(t *testing.T) {
	if flagValue([]string{"doctor", "--config", "/x"}, "config") != "/x" || flagValue([]string{"doctor", "-config=/y"}, "config") != "/y" || flagValue([]string{"doctor"}, "config") != "" {
		t.Error("flagValue")
	}
	for _, c := range []string{"setup", "doctor", "open", "start"} {
		if !noticeCommands[c] {
			t.Errorf("%s should print the notices", c)
		}
	}
	for _, c := range []string{"serve", "stop", "logs", "version", "status", "service"} {
		if noticeCommands[c] {
			t.Errorf("%s should not", c)
		}
	}
}

func TestStatusShowsUpdateLines(t *testing.T) {
	var b bytes.Buffer
	r := statusReport{Running: true, Version: "v0.1.0", CLIVersion: "v0.2.0", URL: "http://x", Service: "not installed",
		Update: &update.Status{Current: "v0.1.0", Latest: "v0.3.0", Available: true, How: "brew upgrade sheepdog"}}
	printStatus(&b, r, time.Now())
	for _, want := range []string{"v0.3.0 is available (you have v0.1.0)", "brew upgrade sheepdog, then sheepdog restart", "this sheepdog is v0.2.0: run `sheepdog restart`"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
	b.Reset()
	r.Update = &update.Status{RestartPending: true, RestartTo: "v0.2.0", AutoRestart: true}
	printStatus(&b, r, time.Now())
	if !strings.Contains(b.String(), "update installed (v0.2.0), restart to finish: sheepdog restart (or it restarts itself when idle)") {
		t.Errorf("restart line:\n%s", b.String())
	}
}

func listenOn(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}
