package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// fakeLLM answers summarize_block for the Codex fixture.
func fakeLLM(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{
			"content": `{"spoken":"auth is asking whether it can create hello.txt.","question":"whether it can create hello.txt","is_question":true,"needs_decision":true,"options":[{"label":"Yes","key":"enter"}],"answer_keys":{"yes":["enter"],"no":["esc"]}}`,
		}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testConfig(t *testing.T, llmURL string) config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.State.Path = filepath.Join(dir, "state.json")
	cfg.LLM.BaseURL = llmURL + "/v1"
	cfg.LLM.Model = "fake"
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
	cfg := testConfig(t, "http://127.0.0.1:1")
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
	gem := cfg
	gem.LLM.BaseURL = config.GeminiOpenAIBaseURL
	if _, err := Build(gem, Options{Voice: VoiceOff, Herdr: HerdrMock}); err == nil || !strings.Contains(err.Error(), "cheap LLM") {
		t.Errorf("gemini llm without keys: %v", err)
	}
	// A Gemini key without a network: Build succeeds (nothing connects).
	gem.Gemini.APIKeys = []string{"k"}
	a, err := Build(gem, Options{Voice: VoiceGemini, Herdr: HerdrMock})
	if err != nil {
		t.Fatalf("build with keys: %v", err)
	}
	_ = a.elog.Close()
}

func TestRunMockVoiceOffFullChain(t *testing.T) {
	llm := fakeLLM(t)
	cfg := testConfig(t, llm.URL)
	var stderr bytes.Buffer
	a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, Version: "t1", Stderr: &stderr, Drain: time.Second})
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
		{"herdr.state", "llm.call", "pending.created", "voice.push"},
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

func TestRunSocketModeRetriesWatcherAndAcceptsHookEvents(t *testing.T) {
	llm := fakeLLM(t)
	cfg := testConfig(t, llm.URL)
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
	// up and a hook-channel event flows through the watcher into the
	// supervisor.
	srv := herdrtest.New(t)
	// herdrtest picks its own path; point a symlink at it.
	if err := os.Symlink(srv.Path, cfg.Herdr.Socket); err != nil {
		t.Fatal(err)
	}
	srv.SetAgent(herdr.AgentInfo{PaneID: "w1:p1", WorkspaceID: "w1", Agent: "codex", Status: herdr.StateWorking})
	srv.SetScreen("w1:p1", "Would you like to run the following command?\n")
	waitFor(t, 5*time.Second, func() bool {
		for _, ev := range a.elog.Tail(0) {
			if ev.Type == "herdr.transition" && ev.PaneID == "w1:p1" {
				return true
			}
		}
		return false
	}, "watcher discovered the pane after retry")

	hook := `{"event":"pane_agent_status_changed","data":{"type":"pane_agent_status_changed","pane_id":"w1:p1","workspace_id":"w1","agent_status":"blocked","agent":"codex"}}`
	resp, err := http.Post(url+"/api/herdr/event", "application/json", strings.NewReader(hook))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("hook = %d", resp.StatusCode)
	}
	waitFor(t, 5*time.Second, func() bool {
		for _, ev := range a.elog.Tail(0) {
			if ev.Type == "pending.created" && ev.PaneID == "w1:p1" {
				return true
			}
		}
		return false
	}, "hook event reached the supervisor")

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
	llm := fakeLLM(t)

	runOnce := func(t *testing.T, cliMode string) []string {
		t.Helper()
		cfg := testConfig(t, llm.URL)
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
