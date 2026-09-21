package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/herdrtest"
)

// noKeys clears every provider key for the test: a developer's .env
// must never make an app test reach a real API (or hang on one).
func noKeys(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GEMINI_API_KEY", "GEMINI_API_KEY_1", "GEMINI_API_KEY_2", "GEMINI_API_KEY_3", "DEEPSEEK_API_KEY"} {
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
	// A Gemini key without a network: Build succeeds (nothing connects),
	// and the brain is attached with its transcript under the data dir.
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
	// brain endpoints answer.
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
	resp, err = http.Post(url+"/api/inject/utterance", "application/json", bytes.NewReader(body))
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
	evs, err := eventlog.Replay(f)
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
		evs, err := eventlog.Replay(f)
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
		evs, err := eventlog.Replay(f)
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
		evs, err := eventlog.Replay(f)
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
