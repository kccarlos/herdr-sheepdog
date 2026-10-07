package world

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// daemon runs a real daemon in process: mock herdr, no voice, no orchestrator.
func daemon(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"GEMINI_API_KEY", "GEMINI_API_KEY_1", "GEMINI_API_KEY_2", "GEMINI_API_KEY_3", "DEEPSEEK_API_KEY", "OPENAI_API_KEY", "VERTEX_API_KEY"} {
		t.Setenv(k, "")
	}
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.State.Path = filepath.Join(dir, "state.json")
	cfg.Web.Port = 0
	cfg.CLI.CodexBin = filepath.Join(dir, "no-codex")
	cfg.CLI.ClaudeBin = filepath.Join(dir, "no-claude")
	a, err := app.Build(cfg, app.Options{Voice: app.VoiceOff, Herdr: app.HerdrMock, Brain: app.BrainOff, CLI: app.CLIOff, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return a.URL()
}

func agents(t *testing.T, base string) map[string]state.Agent {
	t.Helper()
	resp, err := http.Get(base + "/api/agents")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var list []state.Agent
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	out := map[string]state.Agent{}
	for _, a := range list {
		out[a.Alias] = a
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWorldDrivesTheDaemon(t *testing.T) {
	base := daemon(t)
	fixtures, _ := filepath.Abs("../../../testdata/herdr")
	permission, err := os.ReadFile(filepath.Join(fixtures, "claude-permission-prompt.screen.txt"))
	if err != nil {
		t.Fatal(err)
	}
	sc := eval.SimScenario{
		ID: "t", Goal: "g",
		Agents: []eval.SimAgent{
			{Alias: "bench-codex", Kind: "codex", State: "idle", Screen: "› Ask Codex to do anything\n\n"},
			{Alias: "bench-claude", Kind: "claude", State: "blocked", Screen: string(permission)},
		},
		Events: []eval.SimEvent{{OnPromptTo: "bench-codex", PromptMatches: "test", State: "done", AppendScreen: "• All 12 tests pass.", DelayMS: 100}},
	}
	fired := make(chan int, 4)
	w, err := New(sc, Options{BaseURL: base, FixtureDir: fixtures, OnFire: func(i int, _ eval.SimEvent) { fired <- i }})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := agents(t, base)
	if got["bench-codex"].State != "idle" || got["bench-claude"].State != "blocked" || !got["bench-claude"].Synthetic {
		t.Fatalf("agents after start: %+v", got)
	}

	// The user taps an answer: the daemon sends keys, the world sees the
	// herdr.send and moves the worker on, as a real one would.
	var st struct {
		PendingAction *struct {
			ID string `json:"id"`
		} `json:"pending_action"`
	}
	resp, err := http.Get(base + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&st)
	_ = resp.Body.Close()
	if st.PendingAction == nil {
		t.Fatal("the blocked worker made no pending action")
	}
	body, _ := json.Marshal(map[string]any{"id": st.PendingAction.ID, "option": 1})
	resp, err = http.Post(base+"/api/pending/answer", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	waitFor(t, "the keys action", func() bool { return len(w.Actions()) == 1 && w.Actions()[0].Kind == eval.ActionKeys })
	waitFor(t, "bench-claude working", func() bool { return agents(t, base)["bench-claude"].State == "working" })

	// A prompt to bench-codex (as the daemon logs it) makes it busy and
	// fires the cue: done with the new screen line.
	w.onSend("w1:p1", map[string]any{"kind": "prompt", "text": "run the tests"})
	waitFor(t, "the cue", func() bool { return len(fired) == 1 })
	waitFor(t, "bench-codex done", func() bool { return agents(t, base)["bench-codex"].State == "done" })
	facts := w.Facts()
	for _, want := range []string{"Worker bench-codex (codex), now done.", "All 12 tests pass.", `sheepdog sent bench-codex the prompt "run the tests"`, "answered bench-claude's prompt with keys"} {
		if !strings.Contains(facts, want) {
			t.Errorf("facts lack %q:\n%s", want, facts)
		}
	}
	if errs := w.Errors(); len(errs) > 0 {
		t.Fatalf("inject errors: %v", errs)
	}
}

func TestQuerier(t *testing.T) {
	q := Querier{Agents: []eval.SimAgent{{Alias: "a"}, {Alias: "b", Answer: "the flake is a timeout", AnswerDelayMS: 1}}}
	if got, err := q.Query(context.Background(), "", "why"); err != nil || got != "the flake is a timeout" {
		t.Fatalf("query = %q %v", got, err)
	}
	res, err := q.Investigate(context.Background(), supervisor.InvestigationQuery{Workspace: "ws-b"})
	if err != nil || res.Answer != "the flake is a timeout" {
		t.Fatalf("investigate = %+v %v", res, err)
	}
	if _, err := (Querier{Agents: []eval.SimAgent{{Alias: "a"}}}).Query(context.Background(), "", "x"); err == nil {
		t.Fatal("no scripted answer should refuse")
	}
}
