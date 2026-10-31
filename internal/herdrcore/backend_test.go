package herdrcore_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// seedFleet fills the server with one terminal of every kind the mapping
// has a rule for.
func seedFleet(srv *hctest.Server) {
	claude := reported("t1", "claude-code", "working")
	claude.Name = "api"
	claude.Cwd = "/work/start"
	claude.Title = "Refactor"
	claude.Turn = 7
	claude.Foreground = &hc.Foreground{PID: 11, Name: "claude", Argv: []string{"claude"}, Cwd: "/work/now"}
	srv.Add(claude)

	srv.Add(reported("t2", "codex", "error"))

	shell := liveTerminal("t3", "zsh")
	shell.Foreground = &hc.Foreground{PID: 20, Name: "zsh", Argv: []string{"-zsh"}}
	srv.Add(shell)

	// Started as sh -c "codex ...", has not reported, sits at a trust question.
	trust := liveTerminal("t4", "sh", "-c", "codex --no-daemon")
	trust.Foreground = &hc.Foreground{PID: 41, Name: "codex", Argv: []string{"/usr/bin/codex", "--no-daemon"}}
	srv.Add(trust)
	srv.SetScreen("t4", trustScreen)

	srv.Add(reported("m/t1", "claude-code", "idle"))

	odd := liveTerminal("t6", "custom")
	odd.Reported = true
	odd.Status = &hc.Status{State: "weird"}
	srv.Add(odd)

	cleared := liveTerminal("t7", "custom")
	cleared.Reported = true
	srv.Add(cleared)

	srv.Add(liveTerminal("t8", "vim"))

	quiet := liveTerminal("t9", "codex")
	srv.Add(quiet)
	srv.SetScreen("t9", "OpenAI Codex\n")

	gone := reported("t10", "claude-code", "idle")
	gone.State = "finished"
	srv.Add(gone)
}

func TestPing(t *testing.T) {
	b, _ := newBackend(t)
	pong, err := b.Ping(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if pong.Version != "0.1.15" || pong.Protocol != 1 {
		t.Fatalf("pong = %+v", pong)
	}
}

func TestListAgentsMapping(t *testing.T) {
	b, srv := newBackend(t)
	seedFleet(srv)

	agents, err := b.ListAgents(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]herdr.AgentInfo{}
	for _, a := range agents {
		got[a.PaneID] = a
	}

	want := map[string]struct {
		agent string
		state herdr.State
	}{
		"t1": {"claude", herdr.StateWorking},
		"t2": {"codex", herdr.StateDone},
		"t4": {"codex", herdr.StateBlocked},
		"t6": {"custom", herdr.StateUnknown},
		"t7": {"custom", herdr.StateUnknown},
		"t9": {"codex", herdr.StateUnknown},
	}
	if len(got) != len(want) {
		t.Fatalf("agents = %+v, want panes of %v", got, want)
	}
	for id, w := range want {
		a, ok := got[id]
		if !ok {
			t.Errorf("%s missing from agents", id)
			continue
		}
		if a.Agent != w.agent || a.Status != w.state {
			t.Errorf("%s = agent %q status %q, want %q %q", id, a.Agent, a.Status, w.agent, w.state)
		}
	}

	t1 := got["t1"]
	if t1.TerminalID != "t1" || t1.Name != "api" || t1.Cwd != "/work/now" ||
		t1.TerminalTitle != "Refactor" || t1.StateChangeSeq != 7 ||
		t1.WorkspaceID != "" || t1.LaunchPending || t1.Session != nil {
		t.Fatalf("t1 = %+v", t1)
	}
}

func TestSnapshot(t *testing.T) {
	b, srv := newBackend(t)
	seedFleet(srv)

	snap, err := b.Snapshot(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Version != "0.1.15" || snap.Protocol != 1 || len(snap.Workspaces) != 0 {
		t.Fatalf("snapshot header = %+v", snap)
	}
	panes := map[string]herdr.PaneInfo{}
	for _, p := range snap.Panes {
		panes[p.PaneID] = p
	}
	if len(panes) != 8 {
		t.Fatalf("panes = %+v, want 8 live local terminals", panes)
	}
	if p := panes["t3"]; p.Agent != "" || p.Status != herdr.StateUnknown {
		t.Fatalf("shell pane = %+v", p)
	}
	if p := panes["t1"]; p.Agent != "claude" || p.Status != herdr.StateWorking || p.Cwd != "/work/now" {
		t.Fatalf("agent pane = %+v", p)
	}
	if len(snap.Agents) != 6 {
		t.Fatalf("agents = %d, want 6", len(snap.Agents))
	}
	if _, ok := panes["m/t1"]; ok {
		t.Fatal("remote terminal listed")
	}
}

// The herdr.space label is the workspace, herdr.tab the tab; a space is
// named after its first terminal's repo, else folder, and shows its most
// urgent agent's state. Unlabeled terminals stay panes in no workspace.
func TestSnapshotSpacesFromLabels(t *testing.T) {
	b, srv := newBackend(t)
	repo := filepath.Join(t.TempDir(), "api")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "notes")
	labels := func(space, tab string) map[string]string {
		return map[string]string{"herdr.space": space, "herdr.tab": tab}
	}

	// s1: a shell made first, in a subfolder of a repo, and two agents.
	shell := liveTerminal("t1", "zsh")
	shell.Cwd = filepath.Join(repo, "cmd")
	shell.CreatedMS = 100
	shell.Labels = labels("s1", "a")
	srv.Add(shell)
	idle := reported("t2", "claude-code", "idle")
	idle.CreatedMS = 200
	idle.Labels = labels("s1", "b")
	srv.Add(idle)
	blocked := reported("t3", "codex", "blocked")
	blocked.CreatedMS = 300
	blocked.Labels = labels("s1", "c")
	srv.Add(blocked)
	// s2: one agent in a plain folder.
	working := reported("t4", "claude-code", "working")
	working.Cwd = folder
	working.CreatedMS = 400
	working.Labels = labels("s2", "d")
	srv.Add(working)
	// No labels: a pane, in no space.
	srv.Add(reported("t5", "claude-code", "idle"))
	// Another app's label does not make a space.
	other := liveTerminal("t6", "zsh")
	other.Labels = map[string]string{"sheepdog": "spawn"}
	srv.Add(other)

	snap, err := b.Snapshot(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []herdr.WorkspaceInfo{
		{WorkspaceID: "s1", Number: 1, Label: "api", PaneCount: 3, Status: herdr.StateBlocked},
		{WorkspaceID: "s2", Number: 2, Label: "notes", PaneCount: 1, Status: herdr.StateWorking},
	}
	if !slices.Equal(snap.Workspaces, want) {
		t.Fatalf("workspaces = %+v, want %+v", snap.Workspaces, want)
	}
	panes := map[string]herdr.PaneInfo{}
	for _, p := range snap.Panes {
		panes[p.PaneID] = p
	}
	if len(panes) != 6 {
		t.Fatalf("panes = %+v, want all 6 live local terminals", panes)
	}
	for id, w := range map[string][2]string{"t1": {"s1", "a"}, "t3": {"s1", "c"}, "t4": {"s2", "d"}, "t5": {"", ""}, "t6": {"", ""}} {
		if p := panes[id]; p.WorkspaceID != w[0] || p.TabID != w[1] {
			t.Errorf("%s = space %q tab %q, want %q %q", id, p.WorkspaceID, p.TabID, w[0], w[1])
		}
	}
	for _, a := range snap.Agents {
		switch a.PaneID {
		case "t3":
			if a.WorkspaceID != "s1" || a.TabID != "c" {
				t.Errorf("agent t3 = %+v", a)
			}
		case "t5":
			if a.WorkspaceID != "" {
				t.Errorf("unlabeled agent t5 = %+v", a)
			}
		}
	}
}

func TestSpaceNameAtHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	b, srv := newBackend(t)
	at := liveTerminal("t1", "zsh")
	at.Cwd = home
	at.Labels = map[string]string{"herdr.space": "s1", "herdr.tab": "a"}
	srv.Add(at)
	snap, err := b.Snapshot(testContext(t))
	if err != nil || len(snap.Workspaces) != 1 || snap.Workspaces[0].Label != "~" {
		t.Fatalf("workspaces = %+v, %v", snap.Workspaces, err)
	}
}

func TestGetAgent(t *testing.T) {
	b, srv := newBackend(t)
	seedFleet(srv)
	ctx := testContext(t)

	a, err := b.GetAgent(ctx, "t4")
	if err != nil || a.Status != herdr.StateBlocked || a.Agent != "codex" {
		t.Fatalf("GetAgent(t4) = %+v, %v", a, err)
	}
	for _, target := range []string{"t99", "t3", "t10"} {
		if _, err := b.GetAgent(ctx, target); !errors.Is(err, herdr.ErrAgentNotFound) {
			t.Errorf("GetAgent(%s) error = %v, want ErrAgentNotFound", target, err)
		}
	}
	if _, err := b.GetAgent(ctx, "t99"); !errors.Is(err, hc.ErrNotFound) {
		t.Errorf("hc error lost from the chain: %v", err)
	}
}

func TestPrompt(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(reported("t1", "claude-code", "idle"))
	srv.Add(reported("t2", "claude-code", "blocked"))
	ctx := testContext(t)

	a, err := b.Prompt(ctx, "t1", "run the tests")
	if err != nil || a.PaneID != "t1" || a.Status != herdr.StateIdle {
		t.Fatalf("Prompt = %+v, %v", a, err)
	}
	sent := requestParams[struct {
		Terminal string `json:"terminal"`
		Text     string `json:"text"`
		Force    bool   `json:"force"`
	}](t, srv, "terminal.send")
	if len(sent) != 1 || sent[0].Terminal != "t1" || sent[0].Text != "run the tests" || sent[0].Force {
		t.Fatalf("send params = %+v", sent)
	}

	_, err = b.Prompt(ctx, "t2", "go on")
	if !errors.Is(err, herdr.ErrAgentBlocked) || !errors.Is(err, hc.ErrBlocked) {
		t.Fatalf("Prompt on blocked = %v, want ErrAgentBlocked", err)
	}
	_, err = b.Prompt(ctx, "t99", "hi")
	if !errors.Is(err, herdr.ErrPaneNotFound) {
		t.Fatalf("Prompt on missing = %v, want ErrPaneNotFound", err)
	}
}

func TestReadSources(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(reported("t1", "claude-code", "idle"))
	srv.SetScreen("t1", "a\nb\nc\n")
	ctx := testContext(t)

	cases := []struct {
		source    herdr.ReadSource
		lines     int
		wantLines int
		wantText  string
	}{
		{herdr.SourceVisible, 50, 0, "a\nb\nc\n"},
		{herdr.SourceRecent, 2, 2, "b\nc"},
		{herdr.SourceRecentUnwrapped, 0, 0, "a\nb\nc\n"},
	}
	for _, c := range cases {
		res, err := b.Read(ctx, "t1", c.source, c.lines)
		if err != nil {
			t.Fatal(err)
		}
		if res.PaneID != "t1" || res.Source != c.source || res.Text != c.wantText || res.Truncated {
			t.Errorf("Read(%s, %d) = %+v", c.source, c.lines, res)
		}
	}
	reads := requestParams[struct {
		Lines int `json:"lines"`
	}](t, srv, "terminal.read")
	for i, c := range cases {
		if reads[i].Lines != c.wantLines {
			t.Errorf("read %d sent lines %d, want %d", i, reads[i].Lines, c.wantLines)
		}
	}
	if _, err := b.Read(ctx, "t99", herdr.SourceVisible, 0); !errors.Is(err, herdr.ErrPaneNotFound) {
		t.Errorf("Read on missing = %v", err)
	}
}

func TestSendKeysAndInterrupt(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(reported("t1", "claude-code", "blocked"))
	ctx := testContext(t)

	if err := b.SendKeys(ctx, "t1", "down", "enter"); err != nil {
		t.Fatal(err)
	}
	keys := requestParams[struct {
		Terminal string   `json:"terminal"`
		Keys     []string `json:"keys"`
	}](t, srv, "terminal.keys")
	if len(keys) != 1 || keys[0].Terminal != "t1" || !slices.Equal(keys[0].Keys, []string{"down", "enter"}) {
		t.Fatalf("keys params = %+v", keys)
	}

	if err := b.Interrupt(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	stops := requestParams[struct {
		Terminal string `json:"terminal"`
	}](t, srv, "terminal.interrupt")
	if len(stops) != 1 || stops[0].Terminal != "t1" {
		t.Fatalf("interrupt params = %+v", stops)
	}
	if err := b.Interrupt(ctx, "t99"); !errors.Is(err, herdr.ErrPaneNotFound) {
		t.Fatalf("Interrupt on missing = %v", err)
	}
}

func TestClosePane(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(reported("t1", "claude-code", "idle"))
	ctx := testContext(t)

	if err := b.ClosePane(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if n := countRequests(srv, "terminal.kill"); n != 1 {
		t.Fatalf("terminal.kill requests = %d", n)
	}
	agents, err := b.ListAgents(ctx)
	if err != nil || len(agents) != 0 {
		t.Fatalf("agents after close = %+v, %v", agents, err)
	}
}
