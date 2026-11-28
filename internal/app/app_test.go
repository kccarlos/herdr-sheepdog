package app

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/cli"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/herdrtest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
)

// noKeys clears every provider key for the test: a developer's .env
// must never make an app test reach a real API (or hang on one).
func noKeys(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GEMINI_API_KEY", "GEMINI_API_KEY_1", "GEMINI_API_KEY_2", "GEMINI_API_KEY_3", "DEEPSEEK_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
}

// testConfig is a daemon config with everything pointed at temporary
// files, no keys in the environment and no CLI binary in reach.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	noKeys(t)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.State.Path = filepath.Join(dir, "state.json")
	cfg.Web.Port = 0
	// macOS limits unix socket paths to 104 bytes; t.TempDir() is longer.
	sockDir, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	cfg.Herdr.Socket = filepath.Join(sockDir, "h.sock")
	// Never the real herdr-core: auto detection asks this socket too.
	cfg.Herdr.HCSocket = filepath.Join(sockDir, "hc.sock")
	// App tests must never spawn a real codex/claude (cost, non-determinism
	// across machines); point [cli] at binaries that do not exist so
	// Detect always reports both absent.
	cfg.CLI.CodexBin = filepath.Join(dir, "no-such-codex")
	cfg.CLI.ClaudeBin = filepath.Join(dir, "no-such-claude")
	return cfg
}

func TestBuildValidation(t *testing.T) {
	cfg := testConfig(t)
	if _, err := Build(cfg, Options{Voice: "loud"}); err == nil || !strings.Contains(err.Error(), "--voice") {
		t.Errorf("bad voice: %v", err)
	}
	if _, err := Build(cfg, Options{Voice: VoiceOff, Herdr: "real"}); err == nil || !strings.Contains(err.Error(), "--herdr") {
		t.Errorf("bad herdr: %v", err)
	}
	if _, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, CLI: "sideways"}); err == nil || !strings.Contains(err.Error(), "--cli") {
		t.Errorf("bad cli: %v", err)
	}
	if _, err := Build(cfg, Options{Voice: VoiceGemini, Herdr: HerdrMock}); err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Errorf("gemini without keys: %v", err)
	}
	if _, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, Brain: "maybe"}); err == nil || !strings.Contains(err.Error(), "--brain") {
		t.Errorf("bad brain: %v", err)
	}
	for _, bad := range []int{-1, 65536} {
		if _, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, Port: bad}); err == nil || !strings.Contains(err.Error(), "--port") {
			t.Errorf("bad opts.Port=%d: %v", bad, err)
		}
		badCfg := cfg
		badCfg.Web.Port = bad
		if _, err := Build(badCfg, Options{Voice: VoiceOff, Herdr: HerdrMock}); err == nil || !strings.Contains(err.Error(), "web.port") {
			t.Errorf("bad cfg.Web.Port=%d: %v", bad, err)
		}
	}
	// A non-loopback web.bind needs nothing more: devices pair with a
	// one-time code.
	lan := cfg
	lan.Web.Bind = "0.0.0.0"
	lanApp, err := Build(lan, Options{Voice: VoiceOff, Herdr: HerdrMock})
	if err != nil {
		t.Fatalf("non-loopback bind: %v", err)
	}
	_ = lanApp.elog.Close()
	// A Gemini key without a network: Build succeeds (nothing connects),
	// and the orchestrator is attached with its transcript under the data dir.
	gem := cfg
	gem.Gemini.APIKeys = []string{"k"}
	a, err := Build(gem, Options{Voice: VoiceGemini, Herdr: HerdrMock})
	if err != nil {
		t.Fatalf("build with keys: %v", err)
	}
	if a.Brain() == nil || a.sup.Brain() != a.Brain() || !strings.HasPrefix(a.Brain().TranscriptPath(), filepath.Join(gem.DataDir, "brain")) {
		t.Errorf("brain not wired: %v", a.Brain())
	}
	_ = a.Brain().Close()
	_ = a.elog.Close()
	// --brain=off, brain.enabled=false and no key each leave it off.
	for name, tc := range map[string]struct {
		cfg    config.Config
		opts   Options
		reason string
	}{
		"flag":    {gem, Options{Voice: VoiceOff, Herdr: HerdrMock, Brain: BrainOff}, "--brain=off"},
		"config":  {func() config.Config { c := gem; c.Brain.Enabled = false; return c }(), Options{Voice: VoiceOff, Herdr: HerdrMock}, "brain.enabled=false"},
		"no-keys": {cfg, Options{Voice: VoiceOff, Herdr: HerdrMock}, "no Gemini key"},
	} {
		a, err := Build(tc.cfg, tc.opts)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if a.Brain() != nil || a.BrainMode() != BrainOff || a.BrainOffReason() != tc.reason {
			t.Errorf("%s: brain = %v, mode %s, reason %q", name, a.Brain(), a.BrainMode(), a.BrainOffReason())
		}
		_ = a.elog.Close()
	}
	// --brain=fake needs no key: the reflex model is attached and the
	// orchestrator endpoints answer.
	a, err = Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, Brain: BrainFake})
	if err != nil {
		t.Fatalf("fake: %v", err)
	}
	if a.Brain() == nil || a.BrainMode() != BrainFake || a.BrainOffReason() != "" || a.sup.Brain() != a.Brain() || a.Brain().Config().Model != FakeBrainModel {
		t.Errorf("fake brain not wired: %v %s %q", a.Brain(), a.BrainMode(), a.BrainOffReason())
	}
	_ = a.Brain().Close()
	_ = a.elog.Close()
}

func TestRunMockVoiceOffFullChain(t *testing.T) {
	cfg := testConfig(t)
	var stderr bytes.Buffer
	cfg.Web.DevTestdataDir = filepath.Join("..", "..", "testdata", "audio")
	a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, Dev: true, Version: "t1", Stderr: &stderr, Drain: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Listen(); err != nil {
		t.Fatal(err)
	}
	url := a.URL()
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("url = %q", url)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	// The daemon answers; voice counts as connected with pushes skipped.
	var st map[string]any
	getJSON(t, url+"/api/state", &st)
	if st["herdr"] != "mock" || st["version"] != "t1" {
		t.Fatalf("state = %v", st)
	}
	// --dev serves the audio fixtures for the /voice page's file source.
	resp0, err := http.Get(url + "/testdata/question.wav")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp0.Body.Close()
	if resp0.StatusCode != http.StatusOK || resp0.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("GET /testdata/question.wav = %d %s", resp0.StatusCode, resp0.Header.Get("Content-Type"))
	}
	if v := st["voice"].(map[string]any); v["mode"] != "off" {
		t.Fatalf("voice = %v", v)
	}
	screen, err := os.ReadFile(filepath.Join("..", "..", "testdata", "herdr", "codex-approval-prompt.screen.txt"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": string(screen)})
	resp, err := http.Post(url+"/api/inject/blocked", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inject = %d", resp.StatusCode)
	}
	body, _ = json.Marshal(map[string]any{"text": "yes"})
	resp, err = http.Post(url+"/api/utterance", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("utterance = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}

	// The JSONL file holds the whole chain, daemon.start first and
	// daemon.stop last; the state table survived.
	f, err := os.Open(a.elog.Path(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	evs, err := readEventFile(f)
	if err != nil {
		t.Fatal(err)
	}
	types := make([]string, 0, len(evs))
	for _, ev := range evs {
		types = append(types, ev.Type)
	}
	if types[0] != "daemon.start" || types[len(types)-1] != "daemon.stop" {
		t.Fatalf("first/last = %s / %s", types[0], types[len(types)-1])
	}
	for _, chain := range [][]string{
		{"herdr.state", "pending.created", "voice.push"},
		{"voice.tool_call", "herdr.send", "pending.confirmed"},
	} {
		if !subsequence(types, chain) {
			t.Fatalf("chain %v missing in %v", chain, types)
		}
	}
	data, err := os.ReadFile(cfg.State.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"fake:1"`) || !strings.Contains(string(data), `"synthetic": true`) {
		t.Fatalf("state not persisted: %s", data)
	}
	if stderr.Len() != 0 {
		t.Logf("stderr: %s", stderr.String())
	}
}

func TestRunSocketModeRetriesWatcherAndAcceptsHTTPEvents(t *testing.T) {
	cfg := testConfig(t)
	cfg.DebounceMS = 50
	// herdr not running yet: the watcher must retry, not kill the daemon.
	a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrSocket, Drain: time.Second, WatcherRetry: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	if err := a.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- a.Run(ctx) }()
	url := a.URL()

	waitFor(t, 3*time.Second, func() bool {
		for _, ev := range a.elog.Tail(0) {
			if ev.Type == "daemon.herdr_unavailable" {
				return true
			}
		}
		return false
	}, "herdr_unavailable logged")

	// Now start a fake herdr on the configured socket; the retry picks it
	// up and an event posted to the HTTP channel flows through the
	// watcher into the supervisor.
	srv := herdrtest.New(t)
	// herdrtest picks its own path; point a symlink at it.
	if err := os.Symlink(srv.Path, cfg.Herdr.Socket); err != nil {
		t.Fatal(err)
	}
	srv.SetAgent(herdr.AgentInfo{PaneID: "w1:p1", WorkspaceID: "w1", Agent: "codex", Status: herdr.StateWorking})
	srv.SetScreen("w1:p1", "  Would you like to run the following command?\n\n  $ ls\n\n› 1. Yes, proceed (y)\n  2. No, and tell Codex what to do differently (esc)\n")
	waitFor(t, 5*time.Second, func() bool {
		for _, ev := range a.elog.Tail(0) {
			if ev.Type == "herdr.transition" && ev.PaneID == "w1:p1" {
				return true
			}
		}
		return false
	}, "watcher discovered the pane after retry")

	posted := `{"event":"pane_agent_status_changed","data":{"type":"pane_agent_status_changed","pane_id":"w1:p1","workspace_id":"w1","agent_status":"blocked","agent":"codex"}}`
	resp, err := http.Post(url+"/api/herdr/event", "application/json", strings.NewReader(posted))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("posted event = %d", resp.StatusCode)
	}
	waitFor(t, 5*time.Second, func() bool {
		for _, ev := range a.elog.Tail(0) {
			if ev.Type == "pending.created" && ev.PaneID == "w1:p1" {
				return true
			}
		}
		return false
	}, "posted event reached the supervisor")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

// TestBuildQuerierWiresConfiguredResumeCommands: cli.codex_resume_cmd /
// cli.claude_resume_cmd from config must reach the *cli.Runner
// buildQuerier constructs, not just codex_cmd/claude_cmd — otherwise a
// follow-up investigation (Session set) always ran the built-in default
// resume template instead of the configured one (audit B6).
func TestBuildQuerierWiresConfiguredResumeCommands(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakebin.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho ARGV:\"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &App{
		cfg: config.Config{CLI: config.CLI{
			Prefer:          "codex",
			CodexBin:        bin,
			ClaudeBin:       bin,
			CodexCmd:        []string{bin, "{question}"},
			ClaudeCmd:       []string{bin, "{question}"},
			CodexResumeCmd:  []string{bin, "resume-marker", "{session}", "{question}"},
			ClaudeResumeCmd: []string{bin, "resume-marker", "{session}", "{question}"},
			TimeoutS:        5,
			MaxOutputKB:     16,
		}},
		log: logging.NopLogger{},
	}
	runner, err := a.buildQuerier()
	if err != nil {
		t.Fatalf("buildQuerier: %v", err)
	}
	if runner == nil {
		t.Fatal("buildQuerier returned nil: the fake bin was not detected")
	}
	res, err := runner.Run(context.Background(), cli.Query{Backend: cli.Codex, Workspace: dir, Question: "why", Session: "sess-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Raw, "resume-marker") || !strings.Contains(res.Raw, "sess-1") {
		t.Errorf("resume run = %q, want the configured codex_resume_cmd (with {session} substituted), not the default", res.Raw)
	}
}

// TestQuerierEnvFileNamesReachSubprocess checks provenance across the loader,
// app wiring and subprocess environment, including a custom provider key.
func TestQuerierEnvFileNamesReachSubprocess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "shell-marker")
	// Use a non-SHEEPDOG name to exercise the file-name filter itself.
	t.Setenv("TEST_COMPAT_KEY", "")
	if err := os.Unsetenv("TEST_COMPAT_KEY"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("ANTHROPIC_API_KEY=file-ignored\nTEST_COMPAT_KEY=file-marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	names, err := config.LoadEnvFileNames(path)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fakebin.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'shell=%s file=%s' \"$ANTHROPIC_API_KEY\" \"${TEST_COMPAT_KEY-unset}\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: config.Config{CLI: config.CLI{Prefer: "codex", CodexBin: bin, CodexCmd: []string{bin}, TimeoutS: 5, MaxOutputKB: 16}}, opts: Options{EnvFileNames: names}, log: logging.NopLogger{}}
	runner, err := a.buildQuerier()
	if err != nil || runner == nil {
		t.Fatalf("buildQuerier: runner=%v err=%v", runner, err)
	}
	res, err := runner.Run(context.Background(), cli.Query{Backend: cli.Codex, Workspace: dir, Question: "why"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Raw, "shell=shell-marker file=unset") {
		t.Errorf("subprocess environment = %q", res.Raw)
	}
}

// TestRunCLIDetection proves cli.detected is logged after daemon.start
// (WP4), that neither backend being found does not break ask(), and that
// --cli=off skips detection entirely.
func TestRunCLIDetection(t *testing.T) {
	runOnce := func(t *testing.T, cliMode string) []string {
		t.Helper()
		cfg := testConfig(t)
		a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, CLI: cliMode, Drain: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Listen(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx) }()
		waitFor(t, 2*time.Second, func() bool {
			_, err := os.Stat(a.elog.Path(time.Now()))
			return err == nil
		}, "event log file")
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return")
		}
		f, err := os.Open(a.elog.Path(time.Now()))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		evs, err := readEventFile(f)
		if err != nil {
			t.Fatal(err)
		}
		types := make([]string, 0, len(evs))
		for _, ev := range evs {
			types = append(types, ev.Type)
		}
		return types
	}

	on := runOnce(t, CLIOn)
	if on[0] != "daemon.start" {
		t.Fatalf("first event = %s, want daemon.start", on[0])
	}
	idxStart, idxDetected := -1, -1
	for i, ty := range on {
		switch ty {
		case "daemon.start":
			idxStart = i
		case "cli.detected":
			idxDetected = i
		}
	}
	if idxDetected == -1 {
		t.Fatal("cli.detected not logged with --cli=on (default)")
	}
	if idxDetected < idxStart {
		t.Errorf("cli.detected (%d) logged before daemon.start (%d)", idxDetected, idxStart)
	}

	off := runOnce(t, CLIOff)
	for _, ty := range off {
		if ty == "cli.detected" {
			t.Error("cli.detected should not be logged with --cli=off")
		}
	}
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func subsequence(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", msg)
}

// A model chosen at runtime (PUT /api/brain/model) is kept in the state
// file and applied on the next start over the configured initial value,
// after daemon.start (brain.model_changed {via: state}); daemon.start
// reports the live model.
func TestBrainModelPersistsAcrossRestart(t *testing.T) {
	cfg := testConfig(t)
	run := func(check func(url string)) (types []string, models []string) {
		t.Helper()
		a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, Brain: BrainFake, Drain: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Listen(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx) }()
		check(a.URL())
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return")
		}
		f, err := os.Open(a.elog.Path(time.Now()))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		evs, err := readEventFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// The log file is per day: both runs land in it, so only this
		// run's events (from the last daemon.start) are returned.
		for _, ev := range evs {
			if ev.Type == "daemon.start" {
				types, models = nil, nil
				var p struct {
					BrainModel string `json:"brain_model"`
				}
				_ = json.Unmarshal(ev.Payload, &p)
				models = append(models, p.BrainModel)
			}
			types = append(types, ev.Type)
		}
		return types, models
	}

	types, models := run(func(url string) {
		var st map[string]any
		getJSON(t, url+"/api/state", &st)
		if b, _ := st["brain"].(map[string]any); b["model"] != FakeBrainModel || b["provider"] != FakeBrainProvider {
			t.Fatalf("state.brain = %v", st["brain"])
		}
		body, _ := json.Marshal(map[string]any{"provider": FakeBrainProvider2, "model": "gemini-3.8-flash"})
		req, _ := http.NewRequest(http.MethodPut, url+"/api/brain/model", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT /api/brain/model = %d", resp.StatusCode)
		}
	})
	if !subsequence(types, []string{"daemon.start", "brain.model_changed", "daemon.stop"}) || strings.Join(models, ",") != FakeBrainModel {
		t.Fatalf("first run: types = %v models = %v", types, models)
	}
	data, err := os.ReadFile(cfg.State.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"brain_provider": "fake2"`) || !strings.Contains(string(data), `"brain_model": "gemini-3.8-flash"`) {
		t.Fatalf("state file lacks brain_provider / brain_model: %s", data)
	}

	types, models = run(func(url string) {
		var st map[string]any
		getJSON(t, url+"/api/state", &st)
		if b, _ := st["brain"].(map[string]any); b["model"] != "gemini-3.8-flash" || b["provider"] != FakeBrainProvider2 {
			t.Fatalf("state.brain after restart = %v", st["brain"])
		}
	})
	// The switch is applied after daemon.start (which still names the
	// initial model), as brain.model_changed {via: state}.
	if !subsequence(types, []string{"daemon.start", "brain.model_changed"}) || strings.Join(models, ",") != FakeBrainModel {
		t.Fatalf("second run: types = %v models = %v", types, models)
	}
}

// A persisted provider that is not registered in this run (its key is
// gone; here "gemini" under --brain=fake, which registers fake and
// fake2) is logged as daemon.brain_model_error and the configured pair
// is kept; a persisted model without a provider (a state file from
// before providers existed) is Gemini's, so under the fake it is
// refused the same way rather than applied to the configured provider.
func TestBrainProviderRestoreFallsBackToConfigured(t *testing.T) {
	cfg := testConfig(t)
	run := func(session string, check func(url string)) []string {
		t.Helper()
		if err := os.WriteFile(cfg.State.Path, []byte(`{"agents":{},"session":`+session+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
		a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, Brain: BrainFake, Drain: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Listen(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx) }()
		check(a.URL())
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run: %v", err)
		}
		f, err := os.Open(a.elog.Path(time.Now()))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		evs, err := readEventFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var types []string
		for _, ev := range evs {
			if ev.Type == "daemon.start" {
				types = nil
			}
			types = append(types, ev.Type)
		}
		return types
	}
	types := run(`{"brain_provider":"gemini","brain_model":"gemini-3.8-flash"}`, func(url string) {
		var st map[string]any
		getJSON(t, url+"/api/state", &st)
		if b, _ := st["brain"].(map[string]any); b["provider"] != FakeBrainProvider || b["model"] != FakeBrainModel {
			t.Errorf("state.brain = %v", st["brain"])
		}
	})
	if !subsequence(types, []string{"daemon.start", "daemon.brain_model_error"}) || subsequence(types, []string{"brain.model_changed"}) {
		t.Errorf("unregistered provider: types = %v", types)
	}
	types = run(`{"brain_model":"gemini-3.8-flash"}`, func(url string) {
		var st map[string]any
		getJSON(t, url+"/api/state", &st)
		if b, _ := st["brain"].(map[string]any); b["provider"] != FakeBrainProvider || b["model"] != FakeBrainModel {
			t.Errorf("state.brain = %v", st["brain"])
		}
	})
	if !subsequence(types, []string{"daemon.start", "daemon.brain_model_error"}) || subsequence(types, []string{"brain.model_changed"}) {
		t.Errorf("model only: types = %v", types)
	}
}

// TestWebDeviceAuth proves the daemon serves device auth: /api/hello
// says so and carries the TLS pin from the data directory, this
// computer (loopback) needs no ticket, a made-up ticket is refused, and
// daemon.start carries urls.
func TestWebDeviceAuth(t *testing.T) {
	cfg := testConfig(t)
	a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, Drain: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Listen(); err != nil {
		t.Fatal(err)
	}
	url := a.URL()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	var hello map[string]any
	getJSON(t, url+"/api/hello", &hello)
	sec, _ := hello["security"].(map[string]any)
	if hello["auth"] != "device" || hello["ws"] != "/ws" || sec["pin"] == "" {
		t.Fatalf("hello = %v", hello)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "tls", "key.pem")); err != nil {
		t.Errorf("no TLS key in the data directory: %v", err)
	}

	resp, err := http.Get(url + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/state from this computer = %d, want 200", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, url+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer sds_made-up")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/state with a made-up ticket = %d, want 401", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	found := false
	for _, ev := range a.elog.Tail(0) {
		if ev.Type != "daemon.start" {
			continue
		}
		found = true
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		urls, _ := p["urls"].([]any)
		if len(urls) != 1 || urls[0] != url {
			t.Errorf("daemon.start.urls = %v, want [%s]", urls, url)
		}
	}
	if !found {
		t.Error("daemon.start not logged")
	}
}

// TestAppURLsNonLoopbackBind proves App.URLs() falls back to URL() alone
// when web.bind is loopback (unchanged from before web.bind existed)
// and returns at least one well-formed http:// URL for a non-loopback
// bind (CONFIG.md "web.bind": the LAN URL a phone reads off the log).
func TestAppURLsNonLoopbackBind(t *testing.T) {
	cfg := testConfig(t)
	a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.elog.Close() }()
	if err := a.Listen(); err != nil {
		t.Fatal(err)
	}
	if urls := a.URLs(); len(urls) != 1 || urls[0] != a.URL() {
		t.Errorf("loopback URLs() = %v, want [%s]", urls, a.URL())
	}

	lan := cfg
	lan.Web.Bind = "0.0.0.0"
	a2, err := Build(lan, Options{Voice: VoiceOff, Herdr: HerdrMock})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a2.elog.Close() }()
	if err := a2.Listen(); err != nil {
		t.Fatal(err)
	}
	urls := a2.URLs()
	if len(urls) == 0 {
		t.Fatal("non-loopback URLs() is empty")
	}
	for _, u := range urls {
		if !strings.HasPrefix(u, "https://") {
			t.Errorf("url %q does not start with https://", u)
		}
	}
}

// TestLanURLsShape proves lanURLs never returns a loopback address and
// always sorted, well-formed "https://<ip>:<port>" strings — the
// enumeration itself is environment-dependent (a sandbox may have no
// non-loopback interface at all), so this only checks shape.
func TestLanURLsShape(t *testing.T) {
	urls := lanURLs("1234")
	if !sort.StringsAreSorted(urls) {
		t.Errorf("lanURLs not sorted: %v", urls)
	}
	for _, u := range urls {
		if !strings.HasPrefix(u, "https://") || !strings.HasSuffix(u, ":1234") || strings.Contains(u, "127.0.0.1") {
			t.Errorf("lanURLs entry %q malformed or loopback", u)
		}
	}
}

// readEventFile decodes a JSONL event file (the daemon's own log, so a
// malformed line is a test failure, not something to skip).
func readEventFile(r io.Reader) ([]eventlog.Event, error) {
	var out []eventlog.Event
	dec := json.NewDecoder(r)
	for dec.More() {
		var ev eventlog.Event
		if err := dec.Decode(&ev); err != nil {
			return out, err
		}
		out = append(out, ev)
	}
	return out, nil
}

type stubQuerier struct{}

func (stubQuerier) Query(context.Context, string, string) (string, error) { return "scripted", nil }

// TestBuildQuerierOption: Options.Querier replaces the CLI querier (the
// voice bench never runs a real coding agent), whatever --cli says.
func TestBuildQuerierOption(t *testing.T) {
	cfg := testConfig(t)
	a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, Brain: BrainOff, CLI: CLIOn, Querier: stubQuerier{}, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.elog.Close() }()
	if a.QuerierLazy() {
		t.Fatal("the CLI querier was built although Options.Querier was set")
	}
	b, err := Build(testConfig(t), Options{Voice: VoiceOff, Herdr: HerdrMock, Brain: BrainOff, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.elog.Close() }()
	if !b.QuerierLazy() {
		t.Fatal("without Options.Querier the CLI querier is the default")
	}
}

// BrainConfig hands the orchestrator each OpenAI-compatible entry's key
// variable, so its account-problem fix line names the one in use.
func TestBrainConfigKeyVars(t *testing.T) {
	cfg := config.Default()
	cfg.Brain.OpenAICompat = []config.OpenAICompat{
		{Name: "openai", BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_ORCH_API_KEY"},
		{Name: "ollama", BaseURL: "http://localhost:11434/v1"},
	}
	got := BrainConfig(cfg).KeyVars
	if len(got) != 1 || got["openai"] != "OPENAI_ORCH_API_KEY" {
		t.Errorf("KeyVars = %v", got)
	}
}

// TestBuildPortOverrideSetsConfigAndFactoryRunEnv proves Options.Port
// (--port) updates the daemon's config web.port and factory RunEnv from
// one source of truth, and Listen() propagates an ephemeral (0) port too.
func TestBuildPortOverrideSetsConfigAndFactoryRunEnv(t *testing.T) {
	cfg := testConfig(t)
	cfg.Web.Port = config.DefaultWebPort // 7777, as when SHEEPDOG_WEB_PORT is unset
	a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, CLI: CLIOff, Brain: BrainOff, Port: 18091, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.elog.Close() }()
	if a.Config().Web.Port != 18091 {
		t.Errorf("Config().Web.Port = %d, want 18091", a.Config().Web.Port)
	}
	if got := a.Config().String(); !strings.Contains(got, "web.port=18091") {
		t.Errorf("Config().String() missing web.port=18091: %s", got)
	}
	if got := a.factory.RunEnv["SHEEPDOG_WEB_PORT"]; got != "18091" {
		t.Errorf("factory.RunEnv[SHEEPDOG_WEB_PORT] = %q, want %q", got, "18091")
	}

	// Ephemeral port (0): Listen() binds a free port and updates the same
	// config and factory RunEnv before Run() starts any factory work.
	eph := testConfig(t)
	eph.Web.Port = 0
	b, err := Build(eph, Options{Voice: VoiceOff, Herdr: HerdrMock, CLI: CLIOff, Brain: BrainOff, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.elog.Close() }()
	if err := b.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = b.web.Serve(ctx, time.Second)
	})
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(b.URL(), "http://"))
	if err != nil {
		t.Fatalf("URL %q: %v", b.URL(), err)
	}
	bound, err := strconv.Atoi(portStr)
	if err != nil || bound <= 0 {
		t.Fatalf("bound port %q: %v", portStr, err)
	}
	if b.Config().Web.Port != bound {
		t.Errorf("ephemeral Config().Web.Port = %d, want %d", b.Config().Web.Port, bound)
	}
	if got := b.factory.RunEnv["SHEEPDOG_WEB_PORT"]; got != portStr {
		t.Errorf("ephemeral factory.RunEnv[SHEEPDOG_WEB_PORT] = %q, want %q", got, portStr)
	}
}

type captureDemo struct {
	mu      sync.Mutex
	started config.Config
}

func (d *captureDemo) Start(cfg config.Config) (web.DemoState, error) {
	d.mu.Lock()
	d.started = cfg
	d.mu.Unlock()
	return web.DemoState{Running: true, URL: "http://127.0.0.1:7778"}, nil
}
func (d *captureDemo) Stop(string)          {}
func (d *captureDemo) Ping()                {}
func (d *captureDemo) State() web.DemoState { return web.DemoState{} }
func (d *captureDemo) got() config.Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.started
}

// installFakeTailscale puts a fake `tailscale` binary first on PATH whose
// `serve status --json` reports Funnel on funnelPort and whose `cert` copies a
// valid self-signed cert for tsName.
func installFakeTailscale(t *testing.T, tsName string, funnelPort int) {
	t.Helper()
	binDir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(11),
		Subject:      pkix.Name{CommonName: tsName},
		DNSNames:     []string{tsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certSrc := filepath.Join(binDir, "cert.pem")
	keySrc := filepath.Join(binDir, "key.pem")
	if err := os.WriteFile(certSrc, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keySrc, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "status" ]; then
  echo '{"Self":{"DNSName":"%s."}}'
  exit 0
fi
if [ "$1" = "serve" ] && [ "$2" = "status" ]; then
  echo '{"AllowFunnel":{"%s:443":true},"Web":{"%s:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:%d"}}}}}'
  exit 0
fi
if [ "$1" = "cert" ]; then
  cp %q "$3"
  cp %q "$5"
  exit 0
fi
exit 1
`, tsName, tsName, tsName, funnelPort, certSrc, keySrc)
	fakeTS := filepath.Join(binDir, "tailscale")
	if err := os.WriteFile(fakeTS, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestPortReachesSetupChecksDemoAndTailscaleCert proves that both an
// ephemeral listen port (0) and an Options.Port (--port) override flow into
// /api/setup/checks, /api/setup/demo, and /api/setup/tailscale_https
// (web.TailscaleCertCLI) without falling back to port 0 or the default 7777.
func TestPortReachesSetupChecksDemoAndTailscaleCert(t *testing.T) {
	const tsName = "studio.tail1234.ts.net"

	pickFreePort := func(t *testing.T) int {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		return port
	}

	cases := []struct {
		name       string
		cfgPort    int
		optPort    int
		funnelPort int
	}{
		{name: "ephemeral_0", cfgPort: 0, optPort: 0, funnelPort: 3000},
		{name: "override_port", cfgPort: config.DefaultWebPort, optPort: pickFreePort(t), funnelPort: config.DefaultWebPort},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installFakeTailscale(t, tsName, tc.funnelPort)

			cfg := testConfig(t)
			cfg.Web.Port = tc.cfgPort
			cfg.Push.Mode = config.PushModeOff
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.toml")
			envPath := filepath.Join(dir, ".env")
			if err := os.WriteFile(envPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			demo := &captureDemo{}
			a, err := Build(cfg, Options{
				Voice:  VoiceOff,
				Herdr:  HerdrMock,
				CLI:    CLIOff,
				Brain:  BrainOff,
				Port:   tc.optPort,
				Stderr: io.Discard,
				Setup: &SetupOptions{
					ConfigPath: cfgPath,
					EnvPath:    envPath,
					Demo:       demo,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Listen(); err != nil {
				_ = a.elog.Close()
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- a.Run(ctx) }()
			t.Cleanup(func() {
				cancel()
				<-done
			})

			_, portStr, err := net.SplitHostPort(strings.TrimPrefix(a.URL(), "http://"))
			if err != nil {
				t.Fatalf("URL %q: %v", a.URL(), err)
			}
			bound, err := strconv.Atoi(portStr)
			if err != nil || bound <= 0 {
				t.Fatalf("bound port %q: %v", portStr, err)
			}

			// 1. GET /api/setup/checks must check the bound port (not 0 or 7777)
			// and must not falsely fail Tailscale Funnel for an unrelated port.
			resp, err := http.Get(a.URL() + "/api/setup/checks")
			if err != nil {
				t.Fatal(err)
			}
			var checksResp struct {
				Checks []web.SetupCheck `json:"checks"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&checksResp); err != nil {
				_ = resp.Body.Close()
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("/api/setup/checks status = %d", resp.StatusCode)
			}
			wantAddr := fmt.Sprintf("127.0.0.1:%d", bound)
			sawWebPort := false
			for _, c := range checksResp.Checks {
				if c.Name == "web port" {
					sawWebPort = true
					if !strings.Contains(c.Detail, wantAddr) {
						t.Errorf("web port check detail = %q, want %q", c.Detail, wantAddr)
					}
				}
				if c.Name == "tailscale funnel" {
					t.Errorf("unexpected tailscale funnel failure for unrelated port %d: %+v", tc.funnelPort, c)
				}
			}
			if !sawWebPort {
				t.Errorf("missing web port check in %+v", checksResp.Checks)
			}

			// 2. POST /api/setup/demo must receive the bound Web.Port.
			dresp, err := http.Post(a.URL()+"/api/setup/demo", "application/json", strings.NewReader(`{"action":"start"}`))
			if err != nil {
				t.Fatal(err)
			}
			_ = dresp.Body.Close()
			if dresp.StatusCode != http.StatusOK {
				t.Fatalf("/api/setup/demo status = %d", dresp.StatusCode)
			}
			if got := demo.got().Web.Port; got != bound {
				t.Errorf("demo.Start Web.Port = %d, want %d", got, bound)
			}

			// 3. POST /api/setup/tailscale_https must pass the bound port to
			// TailscaleCertCLI so unrelated Funnel traffic does not refuse certs.
			tresp, err := http.Post(a.URL()+"/api/setup/tailscale_https", "application/json", strings.NewReader(`{"enable":true}`))
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(tresp.Body)
			_ = tresp.Body.Close()
			if tresp.StatusCode != http.StatusOK {
				t.Fatalf("/api/setup/tailscale_https status = %d body=%s", tresp.StatusCode, body)
			}
			var tsResp struct {
				OK   bool   `json:"ok"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(body, &tsResp); err != nil {
				t.Fatalf("decode tailscale_https: %v (%s)", err, body)
			}
			if !tsResp.OK || tsResp.Name != tsName {
				t.Errorf("tailscale_https = %+v, want ok=true name=%q", tsResp, tsName)
			}
		})
	}
}
