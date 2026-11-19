package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

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
	if err := os.WriteFile(plan, []byte(`{"tasks":[{"id":"T1","title":"feat: x"}]}`), 0o600); err != nil {
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

	// Extra positional argument is refused with exit 2.
	out.Reset()
	errBuf.Reset()
	if code := run(context.Background(), []string{"factory", "report", "ready", "extra"}, &out, &errBuf); code != 2 {
		t.Fatalf("report ready extra code=%d, want 2", code)
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
}
