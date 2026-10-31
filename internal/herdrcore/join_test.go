package herdrcore_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// simulateTerminals plays the machine for the fake core: a new terminal
// shows its shell (a program in the foreground, a prompt on screen), and
// once the agent's command was typed into it the agent reports idle.
func simulateTerminals(t *testing.T, srv *hctest.Server, client *hc.Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			list, err := client.List(ctx, false)
			if err == nil {
				for _, term := range list.Terminals {
					switch {
					case term.Reported:
					case term.Foreground == nil:
						pid := 100
						term.PID = &pid
						term.Foreground = &hc.Foreground{PID: pid, Name: "zsh", Argv: []string{"zsh"}}
						srv.Add(term)
						srv.SetScreen(term.Address, "% ")
					case sentTo(srv, term.Address):
						term.Argv = []string{"claude"}
						srv.Add(term)
						srv.SetStatus(term.Address, &hc.Status{State: "idle", App: "claude"})
					}
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

// sentTo reports whether a command was typed into the terminal.
func sentTo(srv *hctest.Server, address string) bool {
	for _, r := range srv.Requests() {
		if r.Method == "terminal.send" {
			var p sendParams
			if json.Unmarshal(r.Params, &p) == nil && p.Terminal == address {
				return true
			}
		}
	}
	return false
}

// The user starts two agents from the app in the same folder: the second
// joins the first one's space, as a new tab, instead of opening a second
// space with the same name.
func TestUserNewAgentInTheSameFolderJoinsTheSpace(t *testing.T) {
	b, srv := newBackend(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "bench"), 0o755); err != nil {
		t.Fatal(err)
	}
	// "claude" is installed here, for the backend and the supervisor.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	simulateTerminals(t, srv, hc.NewClient(srv.Path))

	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sup, err := supervisor.New(st, b, nil, nil, nil, supervisor.Config{ProjectRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := testContext(t)
	for range 2 {
		if _, err := sup.UserNewAgent(ctx, supervisor.NewAgentRequest{Kind: "claude", Dir: filepath.Join(root, "bench") + "/"}); err != nil {
			t.Fatal(err)
		}
	}
	runs := requestParams[hc.RunParams](t, srv, "terminal.run")
	if len(runs) != 2 {
		t.Fatalf("terminal.run requests = %d, want 2", len(runs))
	}
	spaces, tabs := map[string]bool{}, map[string]bool{}
	for _, r := range runs {
		spaces[r.Labels["herdr.space"]] = true
		tabs[r.Labels["herdr.tab"]] = true
	}
	if len(spaces) != 1 || len(tabs) != 2 || spaces[""] || tabs[""] {
		t.Fatalf("spaces = %v, tabs = %v; want one space with two tabs", spaces, tabs)
	}
}
