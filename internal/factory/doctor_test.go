package factory

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

type fakeEnv struct {
	mu       sync.Mutex
	bins     map[string]bool
	calls    [][]string
	handlers map[string]func(dir string, env []string, argv []string) CommandOutput
}

func newFakeEnv(bins ...string) *fakeEnv {
	m := map[string]bool{}
	for _, b := range bins {
		m[b] = true
	}
	return &fakeEnv{
		bins:     m,
		handlers: map[string]func(string, []string, []string) CommandOutput{},
	}
}

func (f *fakeEnv) lookPath(name string) (string, error) {
	f.mu.Lock()
	ok := f.bins[name]
	f.mu.Unlock()
	if !ok {
		return "", os.ErrNotExist
	}
	return "/usr/local/bin/" + name, nil
}

func (f *fakeEnv) run(_ context.Context, dir string, extraEnv []string, argv ...string) CommandOutput {
	cp := append([]string(nil), argv...)
	f.mu.Lock()
	f.calls = append(f.calls, cp)
	var fn func(string, []string, []string) CommandOutput
	if len(argv) > 0 {
		if len(argv) > 1 {
			fn = f.handlers[argv[0]+" "+argv[1]]
		}
		if fn == nil {
			fn = f.handlers[argv[0]]
		}
	}
	f.mu.Unlock()
	if fn != nil {
		return fn(dir, extraEnv, cp)
	}
	return CommandOutput{Exit: 0}
}

func findResult(t *testing.T, rs []onboard.Result, name string) onboard.Result {
	t.Helper()
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("result %q not found in %+v", name, rs)
	return onboard.Result{}
}

func TestDoctorAllGreenAndCleansUpProbeTerminal(t *testing.T) {
	srv := hctest.New(t)
	srv.OnRun = func(term hc.Terminal, _ hc.RunParams) {
		srv.FinishExit(term.Address, "exited", 0)
	}

	tmp := t.TempDir()
	proj := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	env := newFakeEnv("hc", "claude", "agy", "codexbar")
	env.handlers["claude auth"] = func(_ string, extra []string, _ []string) CommandOutput {
		if !slices.Contains(extra, "DISABLE_AUTOUPDATER=1") {
			t.Errorf("claude auth missing DISABLE_AUTOUPDATER=1: %v", extra)
		}
		return CommandOutput{Stdout: `{"loggedIn":true,"authMethod":"claude.ai"}`}
	}
	env.handlers["claude -p"] = func(dir string, extra []string, argv []string) CommandOutput {
		if dir != proj {
			t.Errorf("claude canary dir = %q, want %q", dir, proj)
		}
		if !slices.Contains(extra, "DISABLE_AUTOUPDATER=1") || !slices.Contains(argv, "--no-session-persistence") {
			t.Errorf("claude canary flags/env = %v / %v", argv, extra)
		}
		return CommandOutput{Stdout: `{"subtype":"success","is_error":false,"result":"OK"}`, Duration: 42 * time.Millisecond}
	}
	env.handlers["agy models"] = func(string, []string, []string) CommandOutput {
		return CommandOutput{
			Stdout: "\r⠋ Fetching available models...\rgemini-4-carbon-high\tGemini 4 Carbon (High)\nclaude-opus-5-5-high\tClaude Opus 5.5\ngemini-3.8-flash-low\tGemini 3.8 Flash (Low)\n",
		}
	}
	env.handlers["agy --mode"] = func(dir string, _ []string, argv []string) CommandOutput {
		if dir != proj {
			t.Errorf("agy canary dir = %q, want %q", dir, proj)
		}
		if !slices.Contains(argv, "gemini-3.8-flash-low") || !slices.Contains(argv, "-p="+canaryPrompt) {
			t.Errorf("agy canary argv = %v", argv)
		}
		return CommandOutput{Stdout: "OK\n", Duration: 55 * time.Millisecond}
	}
	env.handlers["codexbar --version"] = func(string, []string, []string) CommandOutput {
		return CommandOutput{Stdout: "CodexBar 0.84.0\n"}
	}

	rs := Doctor(context.Background(), DoctorOptions{
		Folder:        proj,
		HCSocket:      srv.Path,
		Canary:        true,
		GOOS:          "darwin",
		Home:          "/Users/alice",
		TempDir:       tmp,
		LookPath:      env.lookPath,
		RunCommand:    env.run,
		VolumeTimeout: time.Second,
		CanaryTimeout: time.Second,
		AuthTimeout:   time.Second,
	})
	if onboard.Failed(rs) {
		for _, r := range rs {
			t.Log(r.Line())
		}
		t.Fatal("Doctor reported failure on green setup")
	}

	if r := findResult(t, rs, "herdr-core"); r.Status != onboard.OK {
		t.Errorf("herdr-core = %+v", r)
	}
	if r := findResult(t, rs, "project volume"); r.Status != onboard.OK {
		t.Errorf("project volume = %+v", r)
	}
	if r := findResult(t, rs, "codex"); r.Status != onboard.Skip {
		t.Errorf("codex = %+v, want Skip", r)
	}
	if r := findResult(t, rs, "agent clis"); r.Status != onboard.OK || !strings.Contains(r.Detail, "claude, agy") {
		t.Errorf("agent clis = %+v", r)
	}
	if r := findResult(t, rs, "codexbar"); r.Status != onboard.OK || !strings.Contains(r.Detail, "CodexBar 0.84.0") {
		t.Errorf("codexbar = %+v", r)
	}

	// The volume probe terminal must be removed from herdr-core afterwards.
	list, err := hc.NewClient(srv.Path).List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Terminals)+list.HiddenFinished != 0 {
		t.Errorf("leftover terminals in herdr-core: %+v", list)
	}
}

func TestCheckHerdrCoreFailures(t *testing.T) {
	ctx := context.Background()
	missing := newFakeEnv()
	r := CheckHerdrCore(ctx, DoctorOptions{LookPath: missing.lookPath, HCSocket: "/nonexistent/hc.sock"})
	if r.Status != onboard.Fail || !strings.Contains(r.Fix, "herdr.dev") {
		t.Errorf("missing hc = %+v", r)
	}

	deadDir := t.TempDir()
	present := newFakeEnv("hc")
	r = CheckHerdrCore(ctx, DoctorOptions{
		LookPath:    present.lookPath,
		HCSocket:    filepath.Join(deadDir, "dead.sock"),
		AuthTimeout: 200 * time.Millisecond,
	})
	if r.Status != onboard.Fail || !strings.Contains(r.Fix, "hc server start") {
		t.Errorf("dead socket = %+v", r)
	}

	// Doctor skips the project volume probe when herdr-core is down instead of double-failing.
	rs := Doctor(ctx, DoctorOptions{
		Folder:      deadDir,
		HCSocket:    filepath.Join(deadDir, "dead.sock"),
		LookPath:    present.lookPath,
		RunCommand:  present.run,
		AuthTimeout: 200 * time.Millisecond,
	})
	if vol := findResult(t, rs, "project volume"); vol.Status != onboard.Skip {
		t.Errorf("project volume with dead hc = %+v, want Skip", vol)
	}
}

func TestCheckProjectVolumeConsentPromptsAndTimeouts(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	t.Run("external volume ok", func(t *testing.T) {
		srv := hctest.New(t)
		srv.OnRun = func(term hc.Terminal, _ hc.RunParams) {
			srv.FinishExit(term.Address, "exited", 0)
		}
		r := CheckProjectVolume(ctx, DoctorOptions{
			Folder:        "/Volumes/Ext/wukongmind",
			HCSocket:      srv.Path,
			GOOS:          "darwin",
			Home:          "/Users/alice",
			TempDir:       tmp,
			VolumeTimeout: time.Second,
		})
		if r.Status != onboard.OK || !strings.Contains(r.Detail, "/Volumes/Ext") {
			t.Errorf("external volume ok = %+v", r)
		}
	})

	t.Run("not a directory", func(t *testing.T) {
		srv := hctest.New(t)
		srv.OnRun = func(term hc.Terminal, _ hc.RunParams) {
			srv.FinishExit(term.Address, "exited", 2)
		}
		r := CheckProjectVolume(ctx, DoctorOptions{
			Folder:        "/Users/alice/file.txt",
			HCSocket:      srv.Path,
			GOOS:          "darwin",
			Home:          "/Users/alice",
			TempDir:       tmp,
			VolumeTimeout: time.Second,
		})
		if r.Status != onboard.Fail || !strings.Contains(r.Detail, "not a directory") {
			t.Errorf("not a directory = %+v", r)
		}
	})

	promptCases := []struct {
		folder     string
		wantPrompt string
	}{
		{"/Volumes/Ext/wukongmind", "macOS Removable Volumes (/Volumes/Ext)"},
		{"/Users/alice/Documents/repo", "macOS Documents folder"},
		{"/Users/alice/Desktop/repo", "macOS Desktop folder"},
		{"/Users/alice/Downloads/repo", "macOS Downloads folder"},
		{"/Users/alice/Library/Mobile Documents/com~apple~CloudDocs/repo", "macOS iCloud Drive"},
		{"/Users/alice/src/repo", "macOS Files and Folders"},
	}
	for _, tc := range promptCases {
		t.Run("denied "+tc.wantPrompt, func(t *testing.T) {
			srv := hctest.New(t)
			srv.OnRun = func(term hc.Terminal, _ hc.RunParams) {
				srv.FinishExit(term.Address, "exited", 1)
			}
			r := CheckProjectVolume(ctx, DoctorOptions{
				Folder:        tc.folder,
				HCSocket:      srv.Path,
				GOOS:          "darwin",
				Home:          "/Users/alice",
				TempDir:       tmp,
				VolumeTimeout: time.Second,
			})
			if r.Status != onboard.Fail || !strings.Contains(r.Detail, tc.wantPrompt) || !strings.Contains(r.Fix, "Privacy & Security") {
				t.Errorf("denied %s = %+v", tc.folder, r)
			}
		})
	}

	t.Run("timeout on removable volume while tmp answers", func(t *testing.T) {
		srv := hctest.New(t)
		srv.OnRun = func(term hc.Terminal, p hc.RunParams) {
			// p.Argv is ["sh", "-c", probeScript, "sh", target].
			if len(p.Argv) >= 5 && p.Argv[4] == tmp {
				srv.FinishExit(term.Address, "exited", 0)
			}
			// Target /Volumes/Ext/wukongmind stays live to simulate a waiting macOS prompt.
		}
		r := CheckProjectVolume(ctx, DoctorOptions{
			Folder:        "/Volumes/Ext/wukongmind",
			HCSocket:      srv.Path,
			GOOS:          "darwin",
			Home:          "/Users/alice",
			TempDir:       tmp,
			VolumeTimeout: 80 * time.Millisecond,
		})
		if r.Status != onboard.Fail ||
			!strings.Contains(r.Detail, "macOS Removable Volumes (/Volumes/Ext) permission prompt") ||
			!strings.Contains(r.Fix, "click Allow") {
			t.Errorf("volume timeout = %+v", r)
		}
		list, err := hc.NewClient(srv.Path).List(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Terminals)+list.HiddenFinished != 0 {
			t.Errorf("hung probe terminal was not cleaned up: %+v", list)
		}
	})

	t.Run("timeout on both target and tmp", func(t *testing.T) {
		srv := hctest.New(t)
		r := CheckProjectVolume(ctx, DoctorOptions{
			Folder:        "/Users/alice/src/repo",
			HCSocket:      srv.Path,
			GOOS:          "darwin",
			Home:          "/Users/alice",
			TempDir:       tmp,
			VolumeTimeout: 60 * time.Millisecond,
		})
		if r.Status != onboard.Fail || !strings.Contains(r.Fix, "hc server restart") {
			t.Errorf("both timed out = %+v", r)
		}
	})
}

func TestCheckAgentCLIsMatrix(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	t.Run("none installed", func(t *testing.T) {
		env := newFakeEnv()
		rs := CheckAgentCLIs(ctx, DoctorOptions{TempDir: tmp, LookPath: env.lookPath, RunCommand: env.run, Canary: true}, true)
		if !onboard.Failed(rs) {
			t.Fatalf("expected failure when no CLI installed: %+v", rs)
		}
		gate := findResult(t, rs, "agent clis")
		if gate.Status != onboard.Fail || !strings.Contains(gate.Detail, "found on PATH") {
			t.Errorf("gate = %+v", gate)
		}
	})

	t.Run("installed but none logged in", func(t *testing.T) {
		env := newFakeEnv("claude", "codex", "agy")
		env.handlers["claude auth"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Stdout: `{"loggedIn":false}`, Exit: 1}
		}
		env.handlers["codex login"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Stdout: "Not logged in\n", Exit: 1}
		}
		env.handlers["agy models"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Stderr: "Error: Please sign in\n", Exit: 1}
		}
		rs := CheckAgentCLIs(ctx, DoctorOptions{TempDir: tmp, LookPath: env.lookPath, RunCommand: env.run, Canary: true}, true)
		if !onboard.Failed(rs) {
			t.Fatalf("expected failure when none logged in: %+v", rs)
		}
		if r := findResult(t, rs, "claude"); r.Status != onboard.Warn || !strings.Contains(r.Fix, "claude auth login") {
			t.Errorf("claude = %+v", r)
		}
		if r := findResult(t, rs, "codex"); r.Status != onboard.Warn || !strings.Contains(r.Fix, "codex login") {
			t.Errorf("codex = %+v", r)
		}
		if r := findResult(t, rs, "agy"); r.Status != onboard.Warn || !strings.Contains(r.Fix, "agy") {
			t.Errorf("agy = %+v", r)
		}
		if gate := findResult(t, rs, "agent clis"); gate.Status != onboard.Fail || !strings.Contains(gate.Detail, "no agent CLI is logged in") {
			t.Errorf("gate = %+v", gate)
		}
	})

	t.Run("one ready CLI passes overall even if another fails quota canary", func(t *testing.T) {
		env := newFakeEnv("claude", "codex")
		secret := "sk-" + "liveSecretToken0123456789abcdef"
		env.handlers["claude auth"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Stdout: `{"loggedIn":true}`}
		}
		env.handlers["claude -p"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{
				Stdout: `{"is_error":true,"subtype":"error_during_execution","errors":["usage limit for ` + secret + `"]}`,
				Exit:   1,
			}
		}
		env.handlers["codex login"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Stdout: "Logged in using ChatGPT\n"}
		}
		env.handlers["codex exec"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Stdout: `{"type":"thread.started","thread_id":"t1"}` + "\n"}
		}
		rs := CheckAgentCLIs(ctx, DoctorOptions{TempDir: tmp, LookPath: env.lookPath, RunCommand: env.run, Canary: true}, true)
		if onboard.Failed(rs) {
			t.Fatalf("one ready CLI should satisfy gate: %+v", rs)
		}
		cl := findResult(t, rs, "claude")
		if cl.Status != onboard.Warn || strings.Contains(cl.Detail, secret) {
			t.Errorf("claude warning or leaked secret: %+v", cl)
		}
		if gate := findResult(t, rs, "agent clis"); gate.Status != onboard.OK || !strings.Contains(gate.Detail, "codex") {
			t.Errorf("gate = %+v", gate)
		}
	})

	t.Run("logged-in CLI canary timeout fails and names consent prompt", func(t *testing.T) {
		env := newFakeEnv("claude", "codex")
		env.handlers["claude auth"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Stdout: `{"loggedIn":true}`}
		}
		env.handlers["claude -p"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{TimedOut: true}
		}
		env.handlers["codex login"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Stdout: "Logged in using ChatGPT\n"}
		}
		env.handlers["codex exec"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Exit: 0}
		}
		rs := CheckAgentCLIs(ctx, DoctorOptions{
			Folder:        "/Volumes/Ext/repo",
			GOOS:          "darwin",
			Home:          "/Users/alice",
			TempDir:       "/Volumes/Ext",
			LookPath:      env.lookPath,
			RunCommand:    env.run,
			Canary:        true,
			CanaryTimeout: 5 * time.Second,
		}, false)
		if !onboard.Failed(rs) {
			t.Fatalf("expected canary timeout to fail doctor: %+v", rs)
		}
		cl := findResult(t, rs, "claude")
		if cl.Status != onboard.Fail || !strings.Contains(cl.Detail, "Removable Volumes") || strings.Contains(cl.Detail, "inside herdr-core") {
			t.Errorf("local claude timeout = %+v", cl)
		}
		if gate := findResult(t, rs, "agent clis"); gate.Status != onboard.Fail || !strings.Contains(gate.Detail, "claude") {
			t.Errorf("agent clis gate on timeout = %+v, want Fail", gate)
		}
	})

	t.Run("no-canary skips canary invocations", func(t *testing.T) {
		env := newFakeEnv("codex")
		env.handlers["codex login"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Stdout: "Logged in using ChatGPT\n"}
		}
		env.handlers["codex exec"] = func(string, []string, []string) CommandOutput {
			t.Fatal("canary ran despite Canary: false")
			return CommandOutput{}
		}
		rs := CheckAgentCLIs(ctx, DoctorOptions{TempDir: tmp, LookPath: env.lookPath, RunCommand: env.run, Canary: false}, true)
		if onboard.Failed(rs) {
			t.Fatalf("no-canary failed: %+v", rs)
		}
		if r := findResult(t, rs, "codex"); r.Status != onboard.OK || !strings.Contains(r.Detail, "canary skipped") {
			t.Errorf("codex = %+v", r)
		}
	})
}

func TestCheckCodexbar(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	missing := newFakeEnv()
	if r := CheckCodexbar(ctx, DoctorOptions{GOOS: "darwin", TempDir: tmp, LookPath: missing.lookPath, RunCommand: missing.run}); r.Status != onboard.Warn || !strings.Contains(r.Fix, "brew install") {
		t.Errorf("darwin missing codexbar = %+v", r)
	}
	if r := CheckCodexbar(ctx, DoctorOptions{GOOS: "linux", TempDir: tmp, LookPath: missing.lookPath, RunCommand: missing.run}); r.Status != onboard.Warn || strings.Contains(r.Fix, "brew install") {
		t.Errorf("linux missing codexbar = %+v", r)
	}
}

func TestSystemRunCommandScrubsEnvAndKillsGroupOnTimeout(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-kept")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("HERDR_TASK_STORE", "/tmp/x")
	t.Setenv("SHEEPDOG_WEB_PORT", "9999")
	t.Setenv("GH_REPO", "wrong/repo")

	tmp := t.TempDir()
	out := SystemRunCommand(context.Background(), tmp, []string{"FACTORY_CANARY_TEST=yes"}, "sh", "-c",
		`printf "cc=%s ep=%s oauth=%s br=%s herdr=%s sd=%s gh=%s extra=%s" "$CLAUDECODE" "$CLAUDE_CODE_ENTRYPOINT" "$CLAUDE_CODE_OAUTH_TOKEN" "$CLAUDE_CODE_USE_BEDROCK" "$HERDR_TASK_STORE" "$SHEEPDOG_WEB_PORT" "$GH_REPO" "$FACTORY_CANARY_TEST"`)
	if out.Exit != 0 || out.Stdout != "cc= ep= oauth=oauth-kept br=1 herdr= sd= gh= extra=yes" {
		t.Fatalf("SystemRunCommand env scrub = %+v", out)
	}

	// WaitDelay bounds Wait when a background child keeps stdout open after exit.
	start := time.Now()
	held := SystemRunCommand(context.Background(), tmp, nil, "sh", "-c", "sleep 10 & printf ready")
	if held.Exit != 0 || !strings.Contains(held.Stdout, "ready") || time.Since(start) > 3*time.Second {
		t.Fatalf("WaitDelay did not bound held stdout pipe (%s): %+v", time.Since(start), held)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	hang := SystemRunCommand(ctx, tmp, nil, "sh", "-c", "sleep 10")
	if !hang.TimedOut {
		t.Fatalf("expected timeout on sleeping child: %+v", hang)
	}
}

func TestVolumeSignalExitAndParentFolderAndHCExecCanary(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	// A signal-killed probe terminal (nil ExitCode) on darwin must report signal
	// death rather than claiming a macOS TCC permission denial.
	srvSig := hctest.New(t)
	srvSig.OnRun = func(term hc.Terminal, _ hc.RunParams) {
		srvSig.Finish(term.Address, "signal")
	}
	sigRes := CheckProjectVolume(ctx, DoctorOptions{
		Folder: "/Users/alice/Documents/repo", HCSocket: srvSig.Path, GOOS: "darwin", Home: "/Users/alice", TempDir: tmp, VolumeTimeout: time.Second,
	})
	if sigRes.Status != onboard.Fail || !strings.Contains(sigRes.Detail, "signal") || strings.Contains(sigRes.Detail, "denied") {
		t.Errorf("signal-killed probe on darwin = %+v, want signal message without TCC denial", sigRes)
	}

	// Probing a not-yet-created child folder reads the resolved parent from inside
	// herdr-core (even when the path does not exist locally) and runs both login
	// checks and canaries through herdr-core's machine.exec without leaking caller
	// PATH or credentials.
	virtualParent := "/Volumes/VirtualSSD/work"
	newChild := filepath.Join(virtualParent, "not-created-yet")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-caller-shell-only")
	t.Setenv("OPENAI_API_KEY", "sk-caller-shell-only")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "caller-oauth-only")

	srvExec := hctest.New(t)
	srvExec.OnRun = func(term hc.Terminal, _ hc.RunParams) {
		srvExec.SetScreen(term.Address, "<SD_DIR>"+virtualParent+"</SD_DIR>\n")
		srvExec.FinishExit(term.Address, "exited", 0)
	}
	var (
		execCwds []string
		execArgv [][]string
	)
	srvExec.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		for _, forbidden := range []string{"PATH", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
			if _, leaked := p.Env[forbidden]; leaked {
				t.Errorf("machine.exec leaked caller env %s: %v", forbidden, p.Env)
			}
		}
		execCwds = append(execCwds, p.Cwd)
		execArgv = append(execArgv, append([]string(nil), p.Argv...))
		zero := 0
		if slices.Contains(p.Argv, "auth") && slices.Contains(p.Argv, "status") {
			if p.Env["DISABLE_AUTOUPDATER"] != "1" {
				t.Errorf("claude auth in hc missing DISABLE_AUTOUPDATER=1: %v", p.Env)
			}
			return hc.ExecResult{Code: &zero, Stdout: []byte(`{"loggedIn":true}`), MS: 5}, nil
		}
		if slices.Contains(p.Argv, "-p") {
			if !slices.Contains(p.Argv, "haiku") {
				t.Errorf("expected default haiku canary model, got argv=%v", p.Argv)
			}
			return hc.ExecResult{
				Code:   &zero,
				Stdout: []byte(`{"subtype":"success","is_error":false,"result":"OK"}`),
				MS:     15,
			}, nil
		}
		return hc.ExecResult{Code: &zero}, nil
	}

	rs := Doctor(ctx, DoctorOptions{
		Folder:   newChild,
		HCSocket: srvExec.Path,
		Canary:   true,
		GOOS:     "darwin",
		Home:     "/Users/alice",
		TempDir:  tmp,
		LookPath: func(name string) (string, error) {
			switch name {
			case "hc":
				return "/usr/bin/hc", nil
			case "claude":
				return "/usr/local/bin/claude", nil
			}
			return "", os.ErrNotExist
		},
		VolumeTimeout: time.Second,
		CanaryTimeout: time.Second,
		AuthTimeout:   time.Second,
	})
	vol := findResult(t, rs, "project volume")
	if vol.Status != onboard.OK || !strings.Contains(vol.Detail, virtualParent+" (parent of "+newChild+")") {
		t.Errorf("new child volume = %+v, want parent named from herdr-core probe", vol)
	}
	if cl := findResult(t, rs, "claude"); cl.Status != onboard.OK {
		t.Errorf("claude via hc exec = %+v", cl)
	}
	if !slices.Contains(execCwds, virtualParent) || len(execArgv) < 2 {
		t.Errorf("expected both auth and canary machine.exec calls (canary in %q), cwds=%v count=%d", virtualParent, execCwds, len(execArgv))
	}

	// A CLI on the caller's PATH that is missing from herdr-core's PATH (exit 127)
	// fails with an explicit `hc server restart` fix rather than passing locally.
	srvNarrowPath := hctest.New(t)
	srvNarrowPath.OnRun = func(term hc.Terminal, _ hc.RunParams) {
		srvNarrowPath.SetScreen(term.Address, "<SD_DIR>"+tmp+"</SD_DIR>\n")
		srvNarrowPath.FinishExit(term.Address, "exited", 0)
	}
	srvNarrowPath.OnExec = func(_ hc.ExecParams) (hc.ExecResult, error) {
		notFound := 127
		return hc.ExecResult{Code: &notFound}, nil
	}
	narrowRs := Doctor(ctx, DoctorOptions{
		Folder:   tmp,
		HCSocket: srvNarrowPath.Path,
		Canary:   false,
		GOOS:     "darwin",
		Home:     "/Users/alice",
		TempDir:  tmp,
		LookPath: func(name string) (string, error) {
			if name == "hc" || name == "claude" {
				return "/opt/homebrew/bin/" + name, nil
			}
			return "", os.ErrNotExist
		},
		VolumeTimeout: time.Second,
		AuthTimeout:   time.Second,
	})
	if cl := findResult(t, narrowRs, "claude"); cl.Status != onboard.Warn || !strings.Contains(cl.Detail, "herdr-core's PATH") {
		t.Errorf("narrow hc PATH claude = %+v", cl)
	}
	if gate := findResult(t, narrowRs, "agent clis"); gate.Status != onboard.Fail || !strings.Contains(gate.Fix, "hc server restart") {
		t.Errorf("narrow hc PATH gate = %+v", gate)
	}

	// cleanReason truncates on rune boundaries rather than splitting multi-byte UTF-8.
	got := cleanReason(strings.Repeat("界", 200))
	if strings.ContainsRune(got, '\uFFFD') || len([]rune(got)) != 161 {
		t.Errorf("cleanReason UTF-8 = len %d runes, valid=%v", len([]rune(got)), !strings.ContainsRune(got, '\uFFFD'))
	}
}
