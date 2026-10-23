package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/herdrtest"
)

// syncBuffer is a bytes.Buffer safe for concurrent writers/readers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) lines() []string {
	out := strings.Split(strings.TrimSpace(s.String()), "\n")
	if len(out) == 1 && out[0] == "" {
		return nil
	}
	return out
}

func TestHerdrWatchPrintsTransitionsAndPersists(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w5", Label: "sheepdog-recon"})
	srv.SetAgent(herdr.AgentInfo{PaneID: "w5:p1", WorkspaceID: "w5", Status: herdr.StateIdle, Agent: "codex"})
	statePath := filepath.Join(t.TempDir(), "state.json")

	ctx, cancel := context.WithCancel(context.Background())
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"herdr", "watch", "--socket", srv.Path, "--state", statePath, "--debounce-ms", "100", "--config", filepath.Join(t.TempDir(), "none.toml")}, stdout, stderr)
	}()

	waitFor(t, func() bool { return len(stdout.lines()) >= 1 })
	// The first line comes from the snapshot; the pane's own subscription
	// opens after it. An event sent before the server has it (fleet +
	// w5:p1) reaches no one, and the next snapshot is 30 s away.
	waitFor(t, func() bool { return srv.SubscriptionCount() >= 2 })
	srv.EmitStatus("w5:p1", herdr.StateWorking)
	srv.EmitStatus("w5:p1", herdr.StateBlocked)
	waitFor(t, func() bool { return len(stdout.lines()) >= 3 })

	cancel() // SIGINT equivalent
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code %d, stderr:\n%s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not exit after cancel")
	}

	var got []watchLine
	for _, l := range stdout.lines() {
		var wl watchLine
		if err := json.Unmarshal([]byte(l), &wl); err != nil {
			t.Fatalf("stdout line %q is not JSON: %v", l, err)
		}
		got = append(got, wl)
	}
	if len(got) != 3 || got[0].To != herdr.StateIdle || got[1].To != herdr.StateWorking || got[2].To != herdr.StateBlocked {
		t.Fatalf("transitions = %+v", got)
	}
	for _, wl := range got {
		if wl.Alias != "sheepdog-recon-codex" || wl.PaneID != "w5:p1" {
			t.Errorf("line = %+v", wl)
		}
	}
	if !strings.Contains(stderr.String(), `"type":"herdr.state"`) {
		t.Errorf("stderr should carry herdr.state events:\n%s", stderr.String())
	}

	// Restart: --once shows the restored table.
	var once bytes.Buffer
	if code := run(context.Background(), []string{"herdr", "watch", "--once", "--state", statePath, "--config", filepath.Join(t.TempDir(), "none.toml")}, &once, stderr); code != 0 {
		t.Fatalf("--once exit %d", code)
	}
	var table struct {
		Agents map[string]struct {
			Alias      string      `json:"alias"`
			State      herdr.State `json:"state"`
			StateSince time.Time   `json:"state_since"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(once.Bytes(), &table); err != nil {
		t.Fatalf("--once output not JSON: %v\n%s", err, once.String())
	}
	a, ok := table.Agents["w5:p1"]
	if !ok || a.Alias != "sheepdog-recon-codex" || a.State != herdr.StateBlocked || !a.StateSince.Equal(got[2].At) {
		t.Errorf("restored table = %+v (want blocked since %v)", a, got[2].At)
	}
}

func TestHerdrUsageAndErrors(t *testing.T) {
	out, errb := &syncBuffer{}, &syncBuffer{}
	if code := run(context.Background(), []string{"herdr"}, out, errb); code != 2 {
		t.Errorf("bare herdr exit = %d", code)
	}
	if code := run(context.Background(), []string{"herdr", "bogus"}, out, errb); code != 2 {
		t.Errorf("unknown herdr subcommand exit = %d", code)
	}
	if code := run(context.Background(), []string{"herdr", "watch", "--nope"}, out, errb); code != 2 {
		t.Errorf("bad flag exit = %d", code)
	}
	if code := run(context.Background(), []string{"herdr", "help"}, out, errb); code != 0 || !strings.Contains(out.String(), "--once") {
		t.Errorf("herdr help exit = %d, out = %q", code, out.String())
	}
	// Unreachable socket: Run fails on the initial snapshot.
	if code := run(context.Background(), []string{"herdr", "watch", "--socket", filepath.Join(t.TempDir(), "none.sock"), "--state", "", "--config", filepath.Join(t.TempDir(), "none.toml")}, out, errb); code != 1 {
		t.Errorf("dead socket exit = %d, stderr = %s", code, errb.String())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
