package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
	t.Setenv("SHEEPDOG_HC_SOCKET", "")

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
}
