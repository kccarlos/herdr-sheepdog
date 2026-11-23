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
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
)

// stubStartChecks replaces the start checks, so no test calls the real gh or claude.
func stubStartChecks(t *testing.T, res []onboard.Result) {
	t.Helper()
	old := factoryStartChecks
	factoryStartChecks = func(context.Context, factory.DoctorOptions) []onboard.Result { return res }
	t.Cleanup(func() { factoryStartChecks = old })
}

// TestFactoryStartRefusedByChecks: start prints every failing line with its fix, warns pass, nothing is posted.
func TestFactoryStartRefusedByChecks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posted = true }))
	t.Cleanup(srv.Close)
	_, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	cfgFile := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgFile, []byte("[web]\nbind = \"127.0.0.1\"\nport = "+portStr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubStartChecks(t, []onboard.Result{
		{Name: "pr workflow", Status: onboard.Fail, Detail: "none", Fix: "add ci.yml"},
		{Name: "start claude", Status: onboard.Fail, Detail: "signed out", Fix: "run: claude auth login"},
		{Name: "base protection", Status: onboard.Warn, Detail: "none", Fix: "optional"},
	})
	var out, errBuf bytes.Buffer
	code := run(context.Background(), []string{"factory", "start", home, "--goal", "g", "--config", cfgFile}, &out, &errBuf)
	if code != 1 || posted {
		t.Fatalf("code=%d posted=%v, want a refusal that posts nothing", code, posted)
	}
	for _, want := range []string{"fix: add ci.yml", "fix: run: claude auth login"} {
		if !strings.Contains(errBuf.String(), want) {
			t.Errorf("refusal lacks %q:\n%s", want, errBuf.String())
		}
	}
	if strings.Contains(errBuf.String(), "optional") {
		t.Errorf("a warn line was printed as a failure:\n%s", errBuf.String())
	}
}

func TestFactoryStartPassesConfigDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	customData := filepath.Join(home, "custom-data")
	cfgFile := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgFile, []byte("data_dir = \""+customData+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotOpts factory.DoctorOptions
	old := factoryStartChecks
	factoryStartChecks = func(_ context.Context, o factory.DoctorOptions) []onboard.Result {
		gotOpts = o
		return []onboard.Result{{Name: "start claude", Status: onboard.Fail, Detail: "signed out", Fix: "login"}}
	}
	t.Cleanup(func() { factoryStartChecks = old })
	var out, errBuf bytes.Buffer
	_ = run(context.Background(), []string{"factory", "start", home, "--goal", "g", "--config", cfgFile}, &out, &errBuf)
	if gotOpts.DataDir != customData {
		t.Fatalf("start --config DataDir = %q, want %q", gotOpts.DataDir, customData)
	}
}

func TestFactoryStartWarnsOnConfigLoadError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	badCfg := filepath.Join(home, "bad.toml")
	if err := os.WriteFile(badCfg, []byte("not = [valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubStartChecks(t, []onboard.Result{
		{Name: "start claude", Status: onboard.Fail, Detail: "signed out", Fix: "login"},
	})
	var out, errBuf bytes.Buffer
	code := run(context.Background(), []string{"factory", "start", home, "--goal", "g", "--config", badCfg}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	got := errBuf.String()
	if !strings.Contains(got, "project settings") || !strings.Contains(got, "codex") {
		t.Fatalf("stderr = %q, want warning that project settings could not be read and codex check is skipped", got)
	}
}

func TestFactoryCommandCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	_ = os.Unsetenv("SHEEPDOG_HC_SOCKET")
	t.Cleanup(func() { _ = os.Unsetenv("SHEEPDOG_HC_SOCKET") })

	var gotOpts factory.DoctorOptions
	var stubResults []onboard.Result
	old := factoryDoctorRunner
	factoryDoctorRunner = func(_ context.Context, opts factory.DoctorOptions) []onboard.Result {
		gotOpts = opts
		return stubResults
	}
	t.Cleanup(func() { factoryDoctorRunner = old })

	t.Run("help and unknown subcommand", func(t *testing.T) {
		var out, errBuf bytes.Buffer
		if code := run(context.Background(), []string{"factory", "--help"}, &out, &errBuf); code != 0 {
			t.Fatalf("factory --help code = %d", code)
		}
		if !strings.Contains(out.String(), "doctor") || !strings.Contains(out.String(), "--no-canary") {
			t.Errorf("unexpected help:\n%s", out.String())
		}
		out.Reset()
		errBuf.Reset()
		if code := run(context.Background(), []string{"factory", "nope"}, &out, &errBuf); code != 2 {
			t.Fatalf("factory nope code = %d, want 2", code)
		}
	})

	t.Run("doctor passes options and exits 0 on warn/ok", func(t *testing.T) {
		stubResults = []onboard.Result{
			{Name: "herdr-core", Status: onboard.OK, Detail: "hc on PATH"},
			{Name: "codexbar", Status: onboard.Warn, Detail: "not on PATH", Fix: "install codexbar"},
		}
		sock := filepath.Join(home, "hc.sock")
		proj := filepath.Join(home, "repo")
		var out, errBuf bytes.Buffer
		code := run(context.Background(), []string{"factory", "doctor", "--hc-socket", sock, "--no-canary", proj}, &out, &errBuf)
		if code != 0 {
			t.Fatalf("code = %d, stderr = %s", code, errBuf.String())
		}
		if gotOpts.Folder != proj || gotOpts.HCSocket != sock || gotOpts.Canary {
			t.Errorf("gotOpts = %+v", gotOpts)
		}
		if !strings.Contains(out.String(), "WARN  codexbar") || !strings.Contains(out.String(), "fix: install codexbar") {
			t.Errorf("stdout = %q", out.String())
		}
	})

	t.Run("doctor exits 1 on FAIL", func(t *testing.T) {
		stubResults = []onboard.Result{
			{Name: "herdr-core", Status: onboard.Fail, Detail: "hc is not on PATH", Fix: "install herdr-core"},
		}
		var out, errBuf bytes.Buffer
		code := run(context.Background(), []string{"factory", "doctor", "--hc-socket", filepath.Join(home, "hc.sock"), "--folder", home}, &out, &errBuf)
		if code != 1 {
			t.Fatalf("code = %d, want 1", code)
		}
		if !strings.Contains(out.String(), "FAIL  herdr-core") {
			t.Errorf("stdout = %q", out.String())
		}
	})

	t.Run("rejects both --folder and positional folder", func(t *testing.T) {
		var out, errBuf bytes.Buffer
		code := run(context.Background(), []string{"factory", "doctor", "--folder", home, home}, &out, &errBuf)
		if code != 2 {
			t.Fatalf("code = %d, want 2", code)
		}
	})

	t.Run("invalid --config prints FAIL line", func(t *testing.T) {
		badCfg := filepath.Join(home, "bad.toml")
		if err := os.WriteFile(badCfg, []byte("not = [valid\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, errBuf bytes.Buffer
		code := run(context.Background(), []string{"factory", "doctor", "--config", badCfg}, &out, &errBuf)
		if code != 1 || !strings.Contains(out.String(), "FAIL  config") {
			t.Fatalf("code = %d, out = %q", code, out.String())
		}
	})

	t.Run("accepts flags after positional folder", func(t *testing.T) {
		stubResults = []onboard.Result{{Name: "herdr-core", Status: onboard.OK, Detail: "ok"}}
		sock := filepath.Join(home, "after.sock")
		proj := filepath.Join(home, "after-repo")
		var out, errBuf bytes.Buffer
		code := run(context.Background(), []string{"factory", "doctor", proj, "--no-canary", "--hc-socket", sock}, &out, &errBuf)
		if code != 0 || gotOpts.Folder != proj || gotOpts.HCSocket != sock || gotOpts.Canary {
			t.Fatalf("trailing flags: code=%d opts=%+v stderr=%s", code, gotOpts, errBuf.String())
		}
	})

	t.Run("broken default config.toml fails even with --hc-socket", func(t *testing.T) {
		defCfg := filepath.Join(home, ".config", "sheepdog", "config.toml")
		if err := os.MkdirAll(filepath.Dir(defCfg), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(defCfg, []byte("broken = [\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(defCfg) })
		var out, errBuf bytes.Buffer
		code := run(context.Background(), []string{"factory", "doctor"}, &out, &errBuf)
		if code != 1 || !strings.Contains(out.String(), "FAIL  config") {
			t.Fatalf("broken default config: code=%d out=%q", code, out.String())
		}
		out.Reset()
		errBuf.Reset()
		code = run(context.Background(), []string{"factory", "doctor", "--hc-socket", filepath.Join(home, "override.sock")}, &out, &errBuf)
		if code != 1 || !strings.Contains(out.String(), "FAIL  config") {
			t.Fatalf("broken default config with --hc-socket: code=%d out=%q", code, out.String())
		}
	})

	t.Run("valid default config.toml supplies herdr.hc_socket without overriding canary model", func(t *testing.T) {
		stubResults = []onboard.Result{{Name: "herdr-core", Status: onboard.OK, Detail: "ok"}}
		defCfg := filepath.Join(home, ".config", "sheepdog", "config.toml")
		if err := os.MkdirAll(filepath.Dir(defCfg), 0o700); err != nil {
			t.Fatal(err)
		}
		cfgSock := filepath.Join(home, "from-config.sock")
		body := "[herdr]\nhc_socket = \"" + cfgSock + "\"\n"
		if err := os.WriteFile(defCfg, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(defCfg) })

		var out, errBuf bytes.Buffer
		code := run(context.Background(), []string{"factory", "doctor", "--no-canary"}, &out, &errBuf)
		if code != 0 || gotOpts.HCSocket != cfgSock || gotOpts.ClaudeModel != "" {
			t.Fatalf("valid default config: code=%d opts=%+v stderr=%s", code, gotOpts, errBuf.String())
		}
	})

	t.Run("doctor --config passes data_dir to runner", func(t *testing.T) {
		stubResults = []onboard.Result{{Name: "herdr-core", Status: onboard.OK, Detail: "ok"}}
		customData := filepath.Join(home, "custom-data")
		cfgFile := filepath.Join(home, "custom.toml")
		if err := os.WriteFile(cfgFile, []byte("data_dir = \""+customData+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, errBuf bytes.Buffer
		code := run(context.Background(), []string{"factory", "doctor", "--config", cfgFile, "--no-canary"}, &out, &errBuf)
		if code != 0 || gotOpts.DataDir != customData {
			t.Fatalf("doctor --config: code=%d DataDir=%q want %q stderr=%s", code, gotOpts.DataDir, customData, errBuf.String())
		}
	})
}

// TestFactoryStartAndStopCLI exercises `sheepdog factory start` and `stop`
// against a local test server without touching the real home or daemon (KB-TST-1).
func TestFactoryStartAndStopCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	var lastPath string
	var lastBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	cfgFile := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(fmt.Sprintf("[web]\nbind = \"127.0.0.1\"\nport = %d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(home, "scratch-repo")
	var out, errBuf bytes.Buffer
	stubStartChecks(t, nil)
	if code := run(context.Background(), []string{"factory", "start", repo, "--goal", "ship one task", "--config", cfgFile}, &out, &errBuf); code != 0 {
		t.Fatalf("start code=%d stderr=%s", code, errBuf.String())
	}
	if lastPath != "/api/factory/start" || lastBody["folder"] != repo || lastBody["goal"] != "ship one task" {
		t.Fatalf("start sent %s %+v", lastPath, lastBody)
	}
	if strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), repo) {
		t.Fatalf("start stdout = %q, want one plain line", out.String())
	}

	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "stop", "--config", cfgFile}, &out, &errBuf); code != 0 {
		t.Fatalf("stop code=%d stderr=%s", code, errBuf.String())
	}
	if lastPath != "/api/factory/stop" || lastBody["project"] != "" {
		t.Fatalf("stop sent %s %+v", lastPath, lastBody)
	}
	if strings.TrimSpace(out.String()) != "factory stopped" || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("stop stdout = %q", out.String())
	}

	// Missing --goal is refused with exit 2.
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "start", repo, "--config", cfgFile}, &out, &errBuf); code != 2 {
		t.Fatalf("start without goal code=%d, want 2", code)
	}

	// --max-running is forwarded on start, validated, and refused on stop.
	var anyBody map[string]any
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anyBody = nil
		_ = json.NewDecoder(r.Body).Decode(&anyBody)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv2.Close)
	_, port2Str, _ := net.SplitHostPort(srv2.Listener.Addr().String())
	cfgFile2 := filepath.Join(home, "config2.toml")
	if err := os.WriteFile(cfgFile2, []byte(fmt.Sprintf("[web]\nbind = \"127.0.0.1\"\nport = %s\n", port2Str)), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "start", repo, "--goal", "ship", "--max-running", "2", "--config", cfgFile2}, &out, &errBuf); code != 0 {
		t.Fatalf("start --max-running 2 code=%d stderr=%s", code, errBuf.String())
	}
	if anyBody["max_running"] != float64(2) {
		t.Fatalf("max_running sent = %#v, want 2", anyBody["max_running"])
	}
	if code := run(context.Background(), []string{"factory", "stop", "--max-running", "2", "--config", cfgFile2}, &out, &errBuf); code != 2 {
		t.Fatalf("stop --max-running 2 code=%d, want 2", code)
	}
	if code := run(context.Background(), []string{"factory", "stop", "--max-running", "0", "--config", cfgFile2}, &out, &errBuf); code != 2 {
		t.Fatalf("stop --max-running 0 code=%d, want 2", code)
	}
	for _, bad := range []string{"0", "-1", "21"} {
		anyBody = nil
		out.Reset()
		errBuf.Reset()
		if code := run(context.Background(), []string{"factory", "start", repo, "--goal", "ship", "--max-running", bad, "--config", cfgFile2}, &out, &errBuf); code != 1 {
			t.Fatalf("start --max-running %s code=%d stderr=%q, want 1", bad, code, errBuf.String())
		}
		if !strings.Contains(errBuf.String(), "allowed 1–20") || anyBody != nil {
			t.Fatalf("start --max-running %s stderr=%q body=%v, want allowed 1–20 and no request", bad, errBuf.String(), anyBody)
		}
	}
}

// TestFactoryPlanApplyCLI checks that `plan apply` and `start` send the run's
// token from the environment, and that the run reaches the daemon through
// SHEEPDOG_WEB_PORT alone (design.md 7.2, 9.3).
func TestFactoryPlanApplyCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	var lastPath string
	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath, lastBody = r.URL.Path, nil
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	t.Setenv("SHEEPDOG_WEB_PORT", port)
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-1")

	plan := filepath.Join(home, "plan.json")
	if err := os.WriteFile(plan, []byte(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A run's terminal does not need the user's config file: break it.
	cfgFile := filepath.Join(home, ".config", "sheepdog", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgFile, []byte("[web\nbind = \"::1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"factory", "plan", "apply", plan}, &out, &errBuf); code != 0 {
		t.Fatalf("plan apply code=%d stderr=%s", code, errBuf.String())
	}
	if err := os.Remove(cfgFile); err != nil {
		t.Fatal(err)
	}
	args, _ := lastBody["args"].(map[string]any)
	if lastPath != "/api/factory/report" || lastBody["token"] != "tok-1" || lastBody["verb"] != "plan apply" || args["tasks"] == nil {
		t.Fatalf("plan apply sent %s %+v", lastPath, lastBody)
	}

	if err := os.WriteFile(plan, []byte(`{"tasks":`), 0o600); err != nil {
		t.Fatal(err)
	}
	lastPath = ""
	if code := run(context.Background(), []string{"factory", "plan", "apply", plan}, &out, &errBuf); code != 1 || lastPath != "" {
		t.Fatalf("invalid JSON plan: code=%d sent=%q", code, lastPath)
	}

	stubStartChecks(t, nil)
	if code := run(context.Background(), []string{"factory", "start", home, "--goal", "g"}, &out, &errBuf); code != 0 {
		t.Fatalf("start code=%d stderr=%s", code, errBuf.String())
	}
	if lastPath != "/api/factory/start" || lastBody["token"] != "tok-1" {
		t.Fatalf("start from a run's terminal sent %s %+v, want the token", lastPath, lastBody)
	}
}

// TestFactoryReportReadyCLI checks that `sheepdog factory report ready` reads
// HEAD from the worktree and sends it with SHEEPDOG_RUN_TOKEN (design.md 7.2, 9.3).
func TestFactoryReportReadyCLI(t *testing.T) {
	setFactoryGitSocket(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	var lastPath string
	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath, lastBody = r.URL.Path, nil
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	t.Setenv("SHEEPDOG_WEB_PORT", port)
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-worker-1")

	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"factory", "report"}, &out, &errBuf); code != 2 {
		t.Fatalf("factory report without verb code=%d, want 2", code)
	}

	// Outside a git repo: exits 1 without calling the daemon.
	nonGit := t.TempDir()
	t.Chdir(nonGit)
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "report", "ready"}, &out, &errBuf); code != 1 || lastPath != "" {
		t.Fatalf("outside git repo: code=%d lastPath=%q stderr=%s", code, lastPath, errBuf.String())
	}

	// Inside a git repo: resolves HEAD and posts verb=ready with args.head.
	repo := filepath.Join(t.TempDir(), "wt")
	if b, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, b)
	}
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "f.txt"}, {"commit", "-m", "feat: f"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
	}
	wantHeadBytes, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	wantHead := strings.TrimSpace(string(wantHeadBytes))

	t.Chdir(repo)
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "report", "ready"}, &out, &errBuf); code != 0 {
		t.Fatalf("report ready code=%d stderr=%s", code, errBuf.String())
	}
	args, _ := lastBody["args"].(map[string]any)
	if lastPath != "/api/factory/report" || lastBody["token"] != "tok-worker-1" || lastBody["verb"] != "ready" || args["head"] != wantHead || args["reject"] != nil {
		t.Fatalf("report ready sent %s %+v, want head %s", lastPath, lastBody, wantHead)
	}

	// `report ready --reject "<why>"` sends head and reject reason.
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "report", "ready", "--reject", "every finding is wrong"}, &out, &errBuf); code != 0 {
		t.Fatalf("report ready --reject code=%d stderr=%s", code, errBuf.String())
	}
	args, _ = lastBody["args"].(map[string]any)
	if lastBody["verb"] != "ready" || args["head"] != wantHead || args["reject"] != "every finding is wrong" {
		t.Fatalf("report ready --reject sent %+v", lastBody)
	}

	// `--reject ""` sends an empty reject field so the daemon's acceptance table
	// refuses with why_missing; bare `--reject` is rejected by flag parsing (exit 2).
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "report", "ready", "--reject", ""}, &out, &errBuf); code != 0 {
		t.Fatalf("report ready --reject \"\" code=%d stderr=%s", code, errBuf.String())
	}
	args, _ = lastBody["args"].(map[string]any)
	v, hasReject := args["reject"]
	if !hasReject || v != "" || args["head"] != wantHead {
		t.Fatalf("report ready --reject \"\" sent %+v, want empty reject key", lastBody)
	}
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "report", "ready", "--reject"}, &out, &errBuf); code != 2 {
		t.Fatalf("report ready bare --reject code=%d, want 2", code)
	}

	// `--nothing-to-change --why` sends the reason; --why alone or with --reject is a usage error.
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "report", "ready", "--nothing-to-change", "--why", "already upstream"}, &out, &errBuf); code != 0 {
		t.Fatalf("report ready --nothing-to-change code=%d stderr=%s", code, errBuf.String())
	}
	args, _ = lastBody["args"].(map[string]any)
	if args["nothing_to_change"] != "already upstream" || args["head"] != wantHead || args["reject"] != nil {
		t.Fatalf("report ready --nothing-to-change sent %+v", lastBody)
	}
	for _, bad := range [][]string{{"--why", "w"}, {"--nothing-to-change", "--reject", "w"}} {
		if code := run(context.Background(), append([]string{"factory", "report", "ready"}, bad...), &out, &errBuf); code != 2 {
			t.Fatalf("report ready %v code=%d, want 2", bad, code)
		}
	}

	// Extra positional argument is refused with exit 2.
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "report", "ready", "extra"}, &out, &errBuf); code != 2 {
		t.Fatalf("report ready extra code=%d, want 2", code)
	}
}

// TestFactoryReportReadyProofsCLI checks PR 3.3a: the report client runs each
// --proof command in a linked git worktree, saves combined output under the
// worktree's .git dir (leaving git status clean), records exit codes and
// timeouts, checks --artifact existence, and sends proofs to the daemon.
func TestFactoryReportReadyProofsCLI(t *testing.T) {
	setFactoryGitSocket(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	var lastReq struct {
		Verb string `json:"verb"`
		Args struct {
			Head   string       `json:"head"`
			Proofs []flow.Proof `json:"proofs"`
		} `json:"args"`
	}
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted = true
		lastReq = struct {
			Verb string `json:"verb"`
			Args struct {
				Head   string       `json:"head"`
				Proofs []flow.Proof `json:"proofs"`
			} `json:"args"`
		}{}
		_ = json.NewDecoder(r.Body).Decode(&lastReq)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	t.Setenv("SHEEPDOG_WEB_PORT", port)
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-worker-1")

	// Set up a main repo and a linked worktree (so .git is a file, not a dir).
	mainRepo := filepath.Join(t.TempDir(), "main")
	wt := filepath.Join(t.TempDir(), "wt")
	gitRun := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	if b, err := exec.Command("git", "init", mainRepo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, b)
	}
	_ = os.WriteFile(filepath.Join(mainRepo, "a.txt"), []byte("a\n"), 0o600)
	gitRun(mainRepo, "add", "a.txt")
	gitRun(mainRepo, "commit", "-m", "feat: init")
	gitRun(mainRepo, "worktree", "add", "-b", "task/t1", wt)
	t.Chdir(wt)

	art := filepath.Join(t.TempDir(), "shot.png")
	_ = os.WriteFile(art, []byte("png"), 0o600)

	var out, errBuf bytes.Buffer
	code := run(context.Background(), []string{
		"factory", "report", "ready",
		"--proof", "tests pass=echo ok-out; echo ok-err >&2",
		"--proof", "lint fails=echo bad-line >&2; exit 3",
		"--artifact", "looks right=" + art,
		"--touched", "a.txt", "--skipped", "",
	}, &out, &errBuf)
	if code != 0 || !posted {
		t.Fatalf("code=%d posted=%v stderr=%s", code, posted, errBuf.String())
	}
	if len(lastReq.Args.Proofs) != 3 {
		t.Fatalf("proofs = %+v, want 3", lastReq.Args.Proofs)
	}
	p0, p1, p2 := lastReq.Args.Proofs[0], lastReq.Args.Proofs[1], lastReq.Args.Proofs[2]
	if p0.Outcome != "tests pass" || p0.Exit != 0 || !filepath.IsAbs(p0.OutputPath) || !strings.Contains(p0.OutputPath, "sheepdog-proofs") {
		t.Fatalf("p0 = %+v", p0)
	}
	if b, err := os.ReadFile(p0.OutputPath); err != nil || !strings.Contains(string(b), "ok-out") || !strings.Contains(string(b), "ok-err") {
		t.Fatalf("p0 output = %q, err=%v", b, err)
	}
	if p1.Outcome != "lint fails" || p1.Exit != 3 {
		t.Fatalf("p1 = %+v, want exit 3", p1)
	}
	if b, err := os.ReadFile(p1.OutputPath); err != nil || !strings.Contains(string(b), "bad-line") {
		t.Fatalf("p1 output = %q, err=%v", b, err)
	}
	if p2.Outcome != "looks right" || p2.Artifact != art {
		t.Fatalf("p2 = %+v", p2)
	}
	// Output files live under the worktree's .git dir, so git status stays clean.
	if status := gitRun(wt, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree dirtied by proof outputs: %q", status)
	}

	// Non-existent artifact is refused before calling the daemon.
	posted = false
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "report", "ready", "--artifact", "looks right=no-such-file.png", "--touched", "a.txt", "--skipped", ""}, &out, &errBuf); code != 1 || posted {
		t.Fatalf("missing artifact: code=%d posted=%v stderr=%s", code, posted, errBuf.String())
	}

	// Timed-out proof gets exit 124 and notes the timeout in its output file.
	oldTimeout := proofTimeout
	proofTimeout = 100 * time.Millisecond
	t.Cleanup(func() { proofTimeout = oldTimeout })
	posted = false
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "report", "ready", "--proof", "slow=sleep 5", "--touched", "a.txt", "--skipped", ""}, &out, &errBuf); code != 0 || !posted {
		t.Fatalf("timeout proof: code=%d posted=%v stderr=%s", code, posted, errBuf.String())
	}
	if len(lastReq.Args.Proofs) != 1 || lastReq.Args.Proofs[0].Exit != 124 {
		t.Fatalf("timeout proof = %+v, want exit 124", lastReq.Args.Proofs)
	}

	// Bad syntax or combining --proof with --reject/--nothing-to-change -> exit 2.
	for _, bad := range [][]string{
		{"--proof", "no-equals"},
		{"--proof", "=cmd"},
		{"--artifact", "outcome="},
		{"--proof", "a=true", "--reject", "w"},
		{"--artifact", "a=" + art, "--nothing-to-change", "--why", "w"},
	} {
		if code := run(context.Background(), append([]string{"factory", "report", "ready"}, bad...), &out, &errBuf); code != 2 {
			t.Fatalf("report ready %v code=%d, want 2", bad, code)
		}
	}
}

// TestFactoryLogCLI reads log.jsonl straight from the data dir named by
// --config, one line per step in local time; -n keeps the last lines.
func TestFactoryLogCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	data := filepath.Join(home, "data")
	cfgFile := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgFile, []byte("data_dir = \""+data+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := factory.Paths{DataDir: data}
	if err := factory.SaveState(paths.State("proj-1"), flow.State{Project: flow.Project{ID: "proj-1", Started: true}}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 19, 18, 0, 0, 0, time.UTC)
	for i, rule := range []string{"N26", "N42"} {
		if err := factory.AppendLog(paths.Log("proj-1"), factory.LogLine{Seq: int64(i + 1), At: at, Task: "T1", Rule: rule, Why: "w"}); err != nil {
			t.Fatal(err)
		}
	}
	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"factory", "log", "-n", "1", "--config", cfgFile}, &out, &errBuf); code != 0 {
		t.Fatalf("log code=%d stderr=%s", code, errBuf.String())
	}
	want := at.Local().Format("2006-01-02 15:04:05") + "  T1   N42         asked: - | decision: - | why: w\n"
	if out.String() != want {
		t.Fatalf("factory log -n 1 = %q, want %q", out.String(), want)
	}
	out.Reset()
	if code := run(context.Background(), []string{"factory", "log", filepath.Join(home, "nowhere"), "--config", cfgFile}, &out, &errBuf); code != 1 || out.Len() != 0 {
		t.Fatalf("log of an unknown folder: code=%d out=%q", code, out.String())
	}

	// With reviewed tasks, factory log appends the project's first-look pass rate
	// and verdict lines display each round's self-check misses.
	if err := factory.SaveState(paths.State("proj-1"), flow.State{
		Project: flow.Project{ID: "proj-1", Started: true},
		Tasks: []flow.Task{
			{ID: "T1", Attempts: []flow.Attempt{{Rounds: []flow.Round{{N: 1, Kind: flow.RoundFull, Verdict: flow.VerdictPass}}}}},
			{ID: "T2", Attempts: []flow.Attempt{{Rounds: []flow.Round{{N: 1, Kind: flow.RoundFull, Verdict: flow.VerdictChanges, Majors: 1}}}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := factory.AppendLog(paths.Log("proj-1"), factory.LogLine{
		Seq: 3, At: at, Task: "T2", Rule: "verdict", Decision: "changes",
		Why: "round 1 (full, opus) at abc1234: changes (1 major(s), 1 self-check miss(es))",
	}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run(context.Background(), []string{"factory", "log", "-n", "1", "--config", cfgFile}, &out, &errBuf); code != 0 {
		t.Fatalf("log with rate code=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "1 self-check miss(es)") || !strings.Contains(out.String(), "first-look pass rate: 1/2 (50%)\n") {
		t.Fatalf("factory log output =\n%s", out.String())
	}
}

// TestFactoryStatusCLI checks `sheepdog factory status [<folder>]` reads stored
// state and log.jsonl without a running daemon or git repo, prints one line per
// task (working, waiting_on_dep by name, done, not yet reached), and agrees with
// GraphViewOf on every task's stage.
func TestFactoryStatusCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	data := filepath.Join(home, "data")
	cfgFile := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgFile, []byte("data_dir = \""+data+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "demo-repo") // not a git repo: proves no git call
	paths := factory.Paths{DataDir: data}
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "proj-1", Folder: repo, Started: false},
		Tasks: []flow.Task{
			{ID: "T1", Title: "feat: done task", Done: &flow.Merged{MergedCommit: "aaa"}},
			{ID: "T2", Title: "feat: working task", Attempts: []flow.Attempt{{BaseCommit: "000", RunID: "w-2"}}},
			{ID: "T3", Title: "feat: dep task", After: []string{"T2"}},
			{ID: "T4", Title: "feat: untouched task", After: []string{"T1"}},
		},
	}
	if err := factory.SaveState(paths.State("proj-1"), st); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	logs := []factory.LogLine{
		{Seq: 1, At: at, By: "engine", Task: "T1", Rule: "N1", Why: "ended: stop its runs and clean up"},
		{Seq: 2, At: at, By: "engine", Task: "T2", Rule: "N23", Why: "the worker is working"},
		{Seq: 3, At: at, By: "engine", Task: "T3", Rule: "N9", Why: "waiting on T2"},
	}
	for _, l := range logs {
		if err := factory.AppendLog(paths.Log("proj-1"), l); err != nil {
			t.Fatal(err)
		}
	}

	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"factory", "status", "--config", cfgFile}, &out, &errBuf); code != 0 {
		t.Fatalf("status code=%d stderr=%s", code, errBuf.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("status lines = %d, want 4:\n%s", len(lines), out.String())
	}
	graph := factory.GraphViewOf(st, logs)
	for i, wantSub := range []string{"N1", "N23", "Project is stopped", "no logged row"} {
		if !strings.Contains(lines[i], graph.Tasks[i].Stage) || !strings.Contains(lines[i], wantSub) {
			t.Errorf("line %d = %q, want stage %q and %q", i, lines[i], graph.Tasks[i].Stage, wantSub)
		}
	}

	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "status", repo, "--config", cfgFile}, &out, &errBuf); code != 0 || !strings.Contains(out.String(), "Project is stopped") {
		t.Fatalf("status <folder>: code=%d out=%q stderr=%q", code, out.String(), errBuf.String())
	}
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "status", filepath.Join(home, "missing"), "--config", cfgFile}, &out, &errBuf); code != 1 {
		t.Fatalf("status unknown folder: code=%d, want 1", code)
	}
	if code := run(context.Background(), []string{"factory", "status", repo, "extra", "--config", cfgFile}, &out, &errBuf); code != 2 {
		t.Fatalf("status extra arg: code=%d, want 2", code)
	}
}

func TestFactoryRecapDecideCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	var lastPath string
	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath, lastBody = r.URL.Path, nil
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	t.Setenv("SHEEPDOG_WEB_PORT", port)
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-coord-1")

	var out, errBuf bytes.Buffer
	code := run(context.Background(), []string{
		"factory", "recap", "decide", "T1", "split",
		"--why", "too big",
		"--child", `{"id":"T1a","estimate_lines":50,"title":"feat: a"}`,
		"--child", `{"id":"T1b","estimate_lines":50,"title":"feat: b","after":["T1a"]}`,
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("recap decide split code=%d stderr=%s", code, errBuf.String())
	}
	args, _ := lastBody["args"].(map[string]any)
	children, _ := args["children"].([]any)
	if lastPath != "/api/factory/report" || lastBody["token"] != "tok-coord-1" || lastBody["verb"] != "recap decide" ||
		args["task"] != "T1" || args["choice"] != "split" || args["why"] != "too big" || len(children) != 2 {
		t.Fatalf("recap decide sent %s %+v", lastPath, lastBody)
	}

	// Bad child JSON fails before calling daemon.
	lastPath = ""
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "recap", "decide", "T1", "split", "--why", "w", "--child", "{bad"}, &out, &errBuf); code != 2 || lastPath != "" {
		t.Fatalf("bad child JSON: code=%d lastPath=%q", code, lastPath)
	}
	// Missing args exits 2.
	if code := run(context.Background(), []string{"factory", "recap", "decide", "T1"}, &out, &errBuf); code != 2 {
		t.Fatalf("missing choice: code=%d, want 2", code)
	}
}

// TestFactoryTaskAndAmendCLI: `task <action> <task> --why` and `plan amend
// <file>` post the coordinator's token, verb and args.
func TestFactoryTaskAndAmendCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody = nil
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	t.Setenv("SHEEPDOG_WEB_PORT", port)
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-coord-1")

	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"factory", "task", "send-back", "T1", "--why", "tests missing"}, &out, &errBuf); code != 0 {
		t.Fatalf("task send-back code=%d stderr=%s", code, errBuf.String())
	}
	args, _ := lastBody["args"].(map[string]any)
	if lastBody["verb"] != "task" || lastBody["token"] != "tok-coord-1" || args["action"] != "send-back" || args["task"] != "T1" || args["why"] != "tests missing" {
		t.Fatalf("task send-back sent %+v", lastBody)
	}
	if code := run(context.Background(), []string{"factory", "task", "park"}, &out, &errBuf); code != 2 {
		t.Fatalf("task without id code=%d, want 2", code)
	}
	file := filepath.Join(t.TempDir(), "amend.json")
	if err := os.WriteFile(file, []byte(`{"why":"w","tasks":[{"id":"T1","brief":"b"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run(context.Background(), []string{"factory", "plan", "amend", file}, &out, &errBuf); code != 0 || !strings.Contains(out.String(), "plan amended") {
		t.Fatalf("plan amend code=%d out=%s stderr=%s", code, out.String(), errBuf.String())
	}
	if args, _ := lastBody["args"].(map[string]any); lastBody["verb"] != "plan amend" || args["why"] != "w" {
		t.Fatalf("plan amend sent %+v", lastBody)
	}
}

func setupProofTestEnv(t *testing.T) (wt string, lastProofs *[]flow.Proof) {
	setFactoryGitSocket(t)
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	var proofs []flow.Proof
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Args struct {
				Proofs []flow.Proof `json:"proofs"`
			} `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		proofs = req.Args.Proofs
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	t.Setenv("SHEEPDOG_WEB_PORT", port)
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-worker-1")

	mainRepo := filepath.Join(t.TempDir(), "main")
	wt = filepath.Join(t.TempDir(), "wt")
	gitRun := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
	}
	if b, err := exec.Command("git", "init", mainRepo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, b)
	}
	_ = os.WriteFile(filepath.Join(mainRepo, "a.txt"), []byte("a\n"), 0o600)
	gitRun(mainRepo, "add", "a.txt")
	gitRun(mainRepo, "commit", "-m", "feat: init")
	gitRun(mainRepo, "worktree", "add", "-b", "task/t1", wt)
	return wt, &proofs
}

// TestFactoryReportReadyProofRunsAtWorktreeRoot checks item 1: running
// `report ready --proof` from a subfolder still runs the proof at the worktree root.
func TestFactoryReportReadyProofRunsAtWorktreeRoot(t *testing.T) {
	wt, proofs := setupProofTestEnv(t)
	sub := filepath.Join(wt, "sub", "dir")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"factory", "report", "ready", "--proof", "at root=test -f a.txt && pwd", "--touched", "a.txt", "--skipped", ""}, &out, &errBuf); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errBuf.String())
	}
	if len(*proofs) != 1 || (*proofs)[0].Exit != 0 {
		t.Fatalf("proofs = %+v, want exit 0 at worktree root", *proofs)
	}
	got, err := os.ReadFile((*proofs)[0].OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	wantDir, _ := filepath.EvalSymlinks(wt)
	gotDir, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	if gotDir != wantDir {
		t.Fatalf("proof ran in %q, want worktree root %q", gotDir, wantDir)
	}
}

// TestFactoryReportReadyProofOutputBounded checks item 4a: proof output is
// capped at 64 KiB and notes the truncation.
func TestFactoryReportReadyProofOutputBounded(t *testing.T) {
	wt, proofs := setupProofTestEnv(t)
	t.Chdir(wt)

	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"factory", "report", "ready", "--proof", "noisy=head -c 100000 /dev/zero | tr '\\0' 'x'", "--touched", "a.txt", "--skipped", ""}, &out, &errBuf); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errBuf.String())
	}
	if len(*proofs) != 1 || (*proofs)[0].Exit != 0 {
		t.Fatalf("proofs = %+v, want exit 0", *proofs)
	}
	b, err := os.ReadFile((*proofs)[0].OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 64*1024+256 || !strings.Contains(string(b), "truncated") {
		t.Fatalf("output len=%d (contains truncated=%v), want <= %d with truncation note", len(b), strings.Contains(string(b), "truncated"), 64*1024+256)
	}
}

// TestFactoryReportReadyProofTimeoutKillsProcessGroup checks item 4b: on
// timeout the whole process group is killed, not just sh.
func TestFactoryReportReadyProofTimeoutKillsProcessGroup(t *testing.T) {
	wt, proofs := setupProofTestEnv(t)
	t.Chdir(wt)

	oldTimeout := proofTimeout
	proofTimeout = 150 * time.Millisecond
	t.Cleanup(func() { proofTimeout = oldTimeout })

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmdStr := fmt.Sprintf("sleep 30 & echo $! > %s; wait", pidFile)
	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"factory", "report", "ready", "--proof", "hang=" + cmdStr, "--touched", "a.txt", "--skipped", ""}, &out, &errBuf); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errBuf.String())
	}
	if len(*proofs) != 1 || (*proofs)[0].Exit != 124 {
		t.Fatalf("proofs = %+v, want exit 124", *proofs)
	}
	rawPID, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil || childPID <= 0 {
		t.Fatalf("invalid child pid %q: %v", rawPID, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(childPID, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild pid %d still alive after proof timeout", childPID)
}

// TestFactoryReportReadyTouchedSkippedCLI checks PR 3.3b CLI flags:
// --touched and --skipped are sent in the ready payload (including empty
// --skipped ""), omitted when not passed, and refused with --reject or
// --nothing-to-change.
func TestFactoryReportReadyTouchedSkippedCLI(t *testing.T) {
	setFactoryGitSocket(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	var lastArgs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Args map[string]any `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		lastArgs = body.Args
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	t.Setenv("SHEEPDOG_WEB_PORT", port)
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-worker-1")

	repo := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
	}
	gitRun("init")
	_ = os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o600)
	gitRun("add", "a.txt")
	gitRun("commit", "-m", "feat: init")
	t.Chdir(repo)

	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"factory", "report", "ready", "--touched", "a.txt", "--skipped", ""}, &out, &errBuf); code != 0 {
		t.Fatalf("empty skipped code=%d stderr=%s", code, errBuf.String())
	}
	if v, ok := lastArgs["skipped"]; !ok || v != "" || lastArgs["touched"] != "a.txt" {
		t.Fatalf("empty skipped sent %+v, want touched=a.txt and explicit skipped=\"\"", lastArgs)
	}

	if code := run(context.Background(), []string{"factory", "report", "ready", "--touched", "a.txt, b.txt", "--skipped", "e2e"}, &out, &errBuf); code != 0 {
		t.Fatalf("non-empty skipped code=%d stderr=%s", code, errBuf.String())
	}
	if lastArgs["touched"] != "a.txt, b.txt" || lastArgs["skipped"] != "e2e" {
		t.Fatalf("non-empty skipped sent %+v", lastArgs)
	}

	for _, bad := range [][]string{
		{"factory", "report", "ready", "--reject", "no", "--touched", "a.txt"},
		{"factory", "report", "ready", "--nothing-to-change", "--why", "done", "--skipped", ""},
	} {
		if code := run(context.Background(), bad, &out, &errBuf); code != 2 {
			t.Fatalf("%v code=%d, want 2", bad, code)
		}
	}
}

// TestFactoryReportReadyRefusesProofsWithoutTouchedOrSkippedBeforeRunning checks
// PR 3.3b review fix 2: the client refuses a ready with proofs when --touched
// or --skipped is missing (or --touched is blank) before running any proof.
func TestFactoryReportReadyRefusesProofsWithoutTouchedOrSkippedBeforeRunning(t *testing.T) {
	wt, proofs := setupProofTestEnv(t)
	t.Chdir(wt)

	marker := filepath.Join(t.TempDir(), "proof-ran")
	cases := []struct {
		name string
		args []string
	}{
		{"missing both", []string{"factory", "report", "ready", "--proof", "p=touch " + marker}},
		{"missing touched", []string{"factory", "report", "ready", "--proof", "p=touch " + marker, "--skipped", ""}},
		{"blank touched", []string{"factory", "report", "ready", "--proof", "p=touch " + marker, "--touched", " \t\n ", "--skipped", ""}},
		{"missing skipped", []string{"factory", "report", "ready", "--proof", "p=touch " + marker, "--touched", "a.txt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(marker)
			*proofs = nil
			var out, errBuf bytes.Buffer
			if code := run(context.Background(), tc.args, &out, &errBuf); code == 0 {
				t.Fatalf("code=0, want refusal; stdout=%s stderr=%s", out.String(), errBuf.String())
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("proof command ran before --touched/--skipped check (marker stat err=%v)", err)
			}
			if len(*proofs) != 0 {
				t.Fatalf("posted proofs to daemon despite client refusal: %+v", *proofs)
			}
		})
	}
}

// TestFactoryTemplatesNameRealCommands: every `{sheepdog} factory …` a role
// template names in backticks is a command in the factory usage, and each of
// its flags is in the usage's flags line (prompts.md 7).
func TestFactoryTemplatesNameRealCommands(t *testing.T) {
	var usage bytes.Buffer
	printFactoryUsage(&usage)
	lines := strings.Split(usage.String(), "\n")
	flags := lines[slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "flags: ") })]
	for _, role := range []string{"coordinator", "worker", "reviewer"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "factory", role+".md"))
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, span := range regexp.MustCompile("`\\{sheepdog\\} factory ([^`]+)`").FindAllStringSubmatch(string(raw), -1) {
			found++
			words := strings.Fields(span[1])
			subs := []string{""}
			if len(words) > 1 && regexp.MustCompile(`^[a-z|-]+$`).MatchString(words[1]) {
				subs = strings.Split(words[1], "|")
			}
			for _, sub := range subs {
				if !slices.ContainsFunc(lines, func(l string) bool {
					f := strings.Fields(l)
					return len(f) > 0 && f[0] == words[0] && (sub == "" || len(f) > 1 && (f[1] == sub || strings.Contains(f[1], "|"+sub+"|") || strings.Contains(f[1], "<"+sub+"|") || strings.Contains(f[1], "|"+sub+">")))
				}) {
					t.Errorf("%s names `factory %s %s`, not in the usage", role, words[0], sub)
				}
			}
			for _, f := range regexp.MustCompile(`(?:^|\s)(--[a-z-]+)`).FindAllStringSubmatch(span[1], -1) {
				if !strings.Contains(flags, f[1]+" ") && !strings.Contains(flags, f[1]+",") && !strings.HasSuffix(flags, f[1]) {
					t.Errorf("%s names flag %s, not in the usage", role, f[1])
				}
			}
		}
		if found == 0 && role != "reviewer" {
			t.Errorf("%s: no `{sheepdog} factory` command found; the check reads nothing", role)
		}
	}
}

func setFactoryGitSocket(t *testing.T) {
	t.Helper()
	srv := hctest.New(t)
	srv.OnExec = hctest.GitExec
	t.Setenv("HC_SOCKET", srv.Path)
}

// TestFactoryStatusDaemonInMemoryDiskAlert checks PR 5b.9 round 2 (Blocker #1):
// when a daemon is running and its Engine holds an unpersisted in-memory disk
// alert (store writes fail so state.json does not have the alert), `sheepdog
// factory status` queries the running daemon and displays the disk alert; when
// the daemon is stopped, it falls back to offline ProjectStatus.
func TestFactoryStatusDaemonInMemoryDiskAlert(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	dataDir := filepath.Join(home, "data")
	paths := factory.Paths{DataDir: dataDir}
	repo := filepath.Join(home, "repo")
	if b, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, b)
	}
	if b, err := exec.Command("git", "-C", repo, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-m", "chore: init").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, b)
	}

	hcSrv := hctest.New(t)
	hcSrv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		argv := p.Argv
		if len(argv) >= 5 && filepath.Base(argv[0]) == "sh" && argv[1] == "-c" && strings.Contains(argv[2], "<SD_DIR>") {
			zero := 0
			target := argv[len(argv)-1]
			return hc.ExecResult{Code: &zero, Stdout: []byte("<SD_DIR>" + target + "</SD_DIR>\n")}, nil
		}
		if len(argv) >= 3 && filepath.Base(argv[0]) == "df" && argv[1] == "-kP" {
			zero := 0
			target := filepath.Clean(argv[2])
			cleanData := filepath.Clean(dataDir)
			if target == cleanData || strings.HasPrefix(target, cleanData+string(filepath.Separator)) {
				return hc.ExecResult{
					Code:   &zero,
					Stdout: []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk2 1000 950 50 95% /Volumes/DataVol\n"),
				}, nil
			}
			return hc.ExecResult{
				Code:   &zero,
				Stdout: []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk1 1000 400 600 40% /Volumes/ProjectVol\n"),
			}, nil
		}
		return hctest.GitExec(p)
	}

	eng := factory.New(paths, hc.NewClient(hcSrv.Path))
	eng.PassInterval = 20 * time.Millisecond
	if err := eng.Start(repo, "Verify daemon status", 2); err != nil {
		t.Fatalf("Engine.Start: %v", err)
	}

	projRoot := filepath.Join(dataDir, "factory", "projects")
	entries, err := os.ReadDir(projRoot)
	if err != nil || len(entries) != 1 {
		t.Fatalf("project dirs = %+v, err=%v", entries, err)
	}
	projDir := filepath.Join(projRoot, entries[0].Name())
	logFile := paths.Log(entries[0].Name())
	if err := os.Chmod(logFile, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(projDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(projDir, 0o700)
		_ = os.Chmod(logFile, 0o600)
	})

	engCtx, engCancel := context.WithCancel(context.Background())
	engDone := make(chan struct{})
	go func() {
		defer close(engDone)
		eng.Run(engCtx)
	}()
	t.Cleanup(func() {
		engCancel()
		<-engDone
	})

	waitFor(t, func() bool {
		rows, err := eng.ProjectStatus(repo)
		if err != nil {
			return false
		}
		for _, ts := range rows {
			if ts.ID == "ALERT" && ts.Title == "disk" {
				return true
			}
		}
		return false
	})

	if raw, err := os.ReadFile(paths.State(entries[0].Name())); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(raw), `"disk"`) {
		t.Fatalf("expected disk alert to stay unpersisted in memory, but state.json contains it:\n%s", raw)
	}

	webDir := t.TempDir()
	elog, err := eventlog.Open(webDir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = elog.Close() })
	st, err := state.Open(filepath.Join(webDir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tlsID, err := web.LoadTLSIdentity("", nil)
	if err != nil {
		t.Fatal(err)
	}
	webSrv, err := web.New(web.Deps{Log: elog, Store: st, DataDir: webDir, TLS: tlsID, Factory: eng})
	if err != nil {
		t.Fatal(err)
	}
	if err := webSrv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	srvCtx, srvCancel := context.WithCancel(context.Background())
	srvDone := make(chan error, 1)
	go func() { srvDone <- webSrv.Serve(srvCtx, 100*time.Millisecond) }()
	stopSrv := func() {
		srvCancel()
		select {
		case <-srvDone:
		case <-time.After(5 * time.Second):
		}
	}
	t.Cleanup(stopSrv)

	_, portStr, err := net.SplitHostPort(webSrv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	cfgFile := filepath.Join(home, "config.toml")
	cfgBody := fmt.Sprintf("data_dir = %q\n\n[web]\nbind = \"127.0.0.1\"\nport = %s\n", dataDir, portStr)
	if err := os.WriteFile(cfgFile, []byte(cfgBody), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"factory", "status", repo, "--config", cfgFile},
		{"factory", "status", "--config", cfgFile},
	} {
		var out, errBuf bytes.Buffer
		code := run(context.Background(), args, &out, &errBuf)
		if code != 0 {
			t.Fatalf("%v code=%d stderr=%s", args, code, errBuf.String())
		}
		if !strings.Contains(out.String(), "ALERT") || !strings.Contains(out.String(), "disk") || !strings.Contains(out.String(), "/Volumes/DataVol") {
			t.Fatalf("%v did not show in-memory disk alert from running daemon:\n%s", args, out.String())
		}
	}

	// When the daemon is stopped, `sheepdog factory status` falls back to offline state.
	stopSrv()
	var offOut, offErr bytes.Buffer
	if code := run(context.Background(), []string{"factory", "status", repo, "--config", cfgFile}, &offOut, &offErr); code != 0 {
		t.Fatalf("offline fallback status code=%d stderr=%s", code, offErr.String())
	}
	if !strings.Contains(offOut.String(), "no tasks yet") {
		t.Fatalf("offline fallback output = %q, want no tasks yet", offOut.String())
	}
}
