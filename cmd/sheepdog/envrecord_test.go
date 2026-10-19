package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
)

func TestArgsWithEnvFile(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  string
		want []string
	}{
		{"adds it", []string{"--herdr", "mock"}, "/a/.env", []string{"--env-file", "/a/.env", "--herdr", "mock"}},
		{"no env file loaded", []string{"--herdr", "mock"}, "", []string{"--herdr", "mock"}},
		{"explicit wins", []string{"--env-file", "/x/.env"}, "/a/.env", []string{"--env-file", "/x/.env"}},
		{"explicit = form wins", []string{"--env-file=/x/.env"}, "/a/.env", []string{"--env-file=/x/.env"}},
	}
	for _, c := range cases {
		if got := argsWithEnvFile(c.args, c.env); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestAutoRestartPassesTheEnvFile: a daemon that found ./.env re-executes
// itself with --env-file <absolute path>, so the new process does not
// depend on the working directory. The exec is injected.
func TestAutoRestartPassesTheEnvFile(t *testing.T) {
	upgrade := fakeInstall(t)
	noKeys(t)
	wd := t.TempDir()
	t.Chdir(wd)
	envPath := filepath.Join(wd, ".env")
	const secret = "SHEEPDOG_TEST_ENVREC=sk-EnvRecordSecret000000000"
	if err := os.WriteFile(envPath, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("SHEEPDOG_TEST_ENVREC") })
	calls := make(chan []string, 4)
	oldExec, oldSvc := execSelf, underService
	t.Cleanup(func() { execSelf, underService = oldExec, oldSvc })
	underService = func() bool { return false }
	execSelf = func(_ string, argv, _ []string) error {
		calls <- argv
		return fmt.Errorf("test: not really exec'd")
	}
	oldTick, oldDelay := updateTick, updateStartDelay
	updateTick, updateStartDelay = 20*time.Millisecond, time.Hour
	t.Cleanup(func() { updateTick, updateStartDelay = oldTick, oldDelay })

	cfgPath, dataDir, port := lifeSetup(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--config", cfgPath, "--voice", "off", "--herdr", "mock"}, &stdout, &stderr)
	}()
	// Stop the daemon before the temp dirs go.
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitFor(t, func() bool { _, err := daemon.GetHello(ctx, base); return err == nil })

	// The record names the file, and only the path.
	rec := daemon.ReadEnvRecord(dataDir)
	want, _ := filepath.EvalSymlinks(envPath)
	if got, _ := filepath.EvalSymlinks(rec.Path); got != want {
		t.Errorf("recorded %q, want %q", rec.Path, want)
	}
	if b, _ := os.ReadFile(daemon.EnvRecordFile(dataDir)); strings.Contains(string(b), "sk-EnvRecord") {
		t.Errorf("the record holds a value: %s", b)
	}

	upgrade(newLine)
	select {
	case argv := <-calls:
		got := strings.Join(argv, " ")
		if !strings.Contains(got, "serve --env-file ") || !strings.Contains(got, filepath.Base(envPath)) || !filepath.IsAbs(flagValue(argv, "env-file")) {
			t.Errorf("exec argv = %v", argv)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no re-exec:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "sk-EnvRecord") {
		t.Error("a secret value reached the log")
	}
}

func TestKeysStatusMessages(t *testing.T) {
	dir := t.TempDir()
	if keysStatus(dir) != nil {
		t.Error("no record: want nil")
	}
	gone := filepath.Join(dir, "gone", ".env")
	_ = daemon.WriteEnvRecord(dir, daemon.EnvRecord{Path: gone, HadKeys: true})
	k := keysStatus(dir)
	if k == nil || k.Problem != "keys came from "+gone+" last time; that file is gone or unreadable" {
		t.Errorf("gone: %+v", k)
	}
	var out strings.Builder
	printStatus(&out, statusReport{Keys: k, Service: "not installed"}, time.Now())
	if !strings.Contains(out.String(), "keys came from "+gone+" last time; that file is gone or unreadable") {
		t.Errorf("status text:\n%s", out.String())
	}
	// A file that exists outside the default: a warning with the fix.
	here := filepath.Join(dir, ".env")
	_ = os.WriteFile(here, nil, 0o600)
	_ = daemon.WriteEnvRecord(dir, daemon.EnvRecord{Path: here, HadKeys: true})
	k = keysStatus(dir)
	if k == nil || k.Problem != "" || !strings.Contains(k.Advice, "copy it to") || !strings.Contains(k.Advice, "chmod 600") {
		t.Errorf("outside the default: %+v", k)
	}
	if r, ok := envLocationCheck(dir, here); !ok || !strings.Contains(r.Fix, "chmod 600") {
		t.Errorf("doctor check: %+v %v", r, ok)
	}
	def := filepath.Join(dir, "default.env")
	_ = os.WriteFile(def, nil, 0o600)
	old := configEnvPath
	configEnvPath = func() string { return def }
	t.Cleanup(func() { configEnvPath = old })
	if _, ok := envLocationCheck(dir, def); ok {
		t.Error("the default env file must not warn")
	}
}

// TestRestartKeepsTheEnvFileEndToEnd builds the real binary and starts a
// daemon with an env file in one working directory, restarts it from
// another, and checks the key survived; then that an explicit --env-file
// wins, and that a vanished file is reported by status without a value.
func TestRestartKeepsTheEnvFileEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "sheepdog")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := filepath.Join(dir, "home") // no ~/.config/sheepdog/.env
	cfgDir := filepath.Join(home, ".config", "sheepdog")
	_ = os.MkdirAll(cfgDir, 0o700)
	port := freePort(t)
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(fmt.Sprintf("[web]\nport = %d\n[brain]\nenabled = false\n[voice]\nprovider = \"openai\"\n", port)), 0o600)
	sock := "SHEEPDOG_HERDR_SOCKET=" + filepath.Join(dir, "no-herdr.sock") + "\n"
	const keyA, keyB = "sk-KeyAaaaaaaaaaaaaaaaaaaaaaaaaaa", "sk-KeyBbbbbbbbbbbbbbbbbbbbbbbbbbb"
	dirA, dirB := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	_ = os.MkdirAll(dirA, 0o700)
	_ = os.MkdirAll(dirB, 0o700)
	envA, envB := filepath.Join(dirA, ".env"), filepath.Join(dirB, "other.env")
	_ = os.WriteFile(envA, []byte("OPENAI_API_KEY="+keyA+"\n"+sock), 0o600)
	_ = os.WriteFile(envB, []byte("OPENAI_API_KEY="+keyB+"\n"+sock), 0o600)

	sd := func(cwd string, args ...string) (int, string) {
		cmd := exec.Command(bin, args...)
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		cmd.Dir = cwd
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return code, string(out)
	}
	noSecrets := func(what, out string) {
		t.Helper()
		if strings.Contains(out, keyA) || strings.Contains(out, keyB) {
			t.Errorf("%s printed a key value:\n%s", what, out)
		}
	}
	t.Cleanup(func() { sd(dirA, "stop") })

	// 1. Started in dirA: ./.env is picked up. start has no key outside it.
	if code, out := sd(dirA, "start", "--env-file", envA); code != 0 {
		t.Fatalf("start: %d %s", code, out)
	}
	if rec := daemon.ReadEnvRecord(filepath.Join(home, ".local", "share", "sheepdog")); filepath.Base(rec.Path) != ".env" || !rec.HadKeys {
		t.Fatalf("record after start: %+v", rec)
	}

	// 2. restart from dirB, no flags, no ./.env there: the key survives.
	code, out := sd(dirB, "restart")
	noSecrets("restart", out)
	if code != 0 || !strings.Contains(out, "using the env file the last run loaded") || !strings.Contains(out, "sheepdog started") || strings.Contains(out, "no voice key") {
		t.Fatalf("restart from another directory: %d\n%s", code, out)
	}
	code, out = sd(dirB, "status")
	noSecrets("status", out)
	if code != 0 || !strings.Contains(out, "keys          from ") || !strings.Contains(out, "copy it to") {
		t.Errorf("status after restart: %d\n%s", code, out)
	}

	// 3. An explicit --env-file wins, and becomes the record.
	if code, out = sd(dirB, "restart", "--env-file", envB); code != 0 || strings.Contains(out, "using the env file the last run loaded") {
		t.Fatalf("restart --env-file: %d\n%s", code, out)
	}
	if rec := daemon.ReadEnvRecord(filepath.Join(home, ".local", "share", "sheepdog")); filepath.Base(rec.Path) != "other.env" {
		t.Errorf("explicit --env-file not recorded: %+v", rec)
	}

	// 4. The file disappears: stop, remove it; status says so, start fails
	// plainly, and no value is printed anywhere.
	sd(dirB, "stop")
	_ = os.Remove(envB)
	code, out = sd(dirB, "status")
	noSecrets("status (gone)", out)
	if !strings.Contains(out, "keys came from "+envB) || !strings.Contains(out, "that file is gone or unreadable") {
		t.Errorf("status with the file gone: %d\n%s", code, out)
	}
	code, out = sd(dirB, "start")
	noSecrets("start (gone)", out)
	if code != 0 || !strings.Contains(out, "that file is gone or unreadable") || !strings.Contains(out, "no voice key yet") {
		t.Errorf("start with the file gone: %d\n%s", code, out)
	}
	_, logs := sd(dirB, "logs", "-n", "500")
	noSecrets("logs", logs)
	if !strings.Contains(logs, "the previous run had its keys from "+envB) {
		t.Errorf("the log does not say the keys file is missing:\n%s", logs)
	}
}
