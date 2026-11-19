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
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
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
