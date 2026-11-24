package herdrcore_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Blocking probe (NeonH §1.3, Carbon §2/§5): herdrcore.Subscribe must stream
// live status events for remote panes whether addressed as "box~t4" or raw
// "box/t4" (isLocal must not reject raw "box/t4").
func TestSubscribeAcceptsBothRemoteForms(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
	}{
		{"pane_id", "box~t4"},
		{"raw_address", "box/t4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, srv := newBackend(t)
			srv.Add(reported("box/t4", "claude-code", "idle"))
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()

			ch := b.Subscribe(ctx, tc.target)
			awaitSubscribers(t, srv, 1)

			srv.SetStatus("box/t4", &hc.Status{State: "working", App: "claude-code"})
			ev := receive(t, ch)
			if ev.State != herdr.StateWorking || ev.Agent != "claude" {
				t.Fatalf("Subscribe(%q) event = %+v, want working claude", tc.target, ev)
			}
		})
	}
}

// Blocking probe (NeonH §1.3, Carbon §2): MarkSeen, MarkUnread and SetPinned
// must accept raw "box/t4" addresses just like "box~t4" (currently blocked by
// dead isLocal checking !strings.Contains(address, "/")).
func TestMarksAcceptRawRemoteAddress(t *testing.T) {
	b, srv := newBackend(t)
	ctx := testContext(t)
	term := reported("box/t4", "claude-code", "done")
	term.Turn = 3
	srv.Add(term)

	if _, err := b.MarkSeen(ctx, "box/t4", 2); err != nil {
		t.Fatalf("MarkSeen(box/t4) error = %v, want nil", err)
	}
	if _, err := b.MarkUnread(ctx, "box/t4"); err != nil {
		t.Fatalf("MarkUnread(box/t4) error = %v, want nil", err)
	}
	if _, err := b.SetPinned(ctx, "box/t4", true); err != nil {
		t.Fatalf("SetPinned(box/t4) error = %v, want nil", err)
	}
	reqs := requestParams[struct {
		Terminal string            `json:"terminal"`
		Labels   map[string]string `json:"labels"`
	}](t, srv, "terminal.label")
	if len(reqs) < 3 {
		t.Fatalf("terminal.label requests = %+v, want 3 writes to box/t4", reqs)
	}
	for i, r := range reqs {
		if r.Terminal != "box/t4" {
			t.Errorf("request %d terminal = %q, want box/t4", i, r.Terminal)
		}
	}
}

// Blocking probe (NeonH §3.6, Carbon §3.6): spaceName in herdrcore/spaces.go
// must never probe local herdr.GitInfo or local os.UserHomeDir using a remote
// terminal's Cwd, even when that path is a real local directory with files and
// a .git repository.
func TestSnapshotRemoteSpaceDoesNotProbeLocalGitOrHome(t *testing.T) {
	base := t.TempDir()
	localRepo := filepath.Join(base, "local-secret-repo")
	sub := filepath.Join(localRepo, "sub")
	if err := os.MkdirAll(filepath.Join(localRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localRepo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "local-only.txt"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	localHome := filepath.Join(base, "myhome")
	if err := os.MkdirAll(localHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localHome, "home-secret.txt"), []byte("home\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", localHome)

	b, srv := newBackend(t)

	rGit := reported("box/t4", "claude-code", "idle")
	rGit.Cwd = sub
	rGit.Labels = map[string]string{"herdr.space": "s-remote-git", "herdr.tab": "a"}
	srv.Add(rGit)

	rHome := reported("box/t5", "claude-code", "idle")
	rHome.Cwd = localHome
	rHome.Labels = map[string]string{"herdr.space": "s-remote-home", "herdr.tab": "b"}
	srv.Add(rHome)

	snap, err := b.Snapshot(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	for _, w := range snap.Workspaces {
		labels[w.WorkspaceID] = w.Label
	}
	if labels["s-remote-git"] == "local-secret-repo" {
		t.Errorf("remote space label = %q: probed local .git repo via herdr.GitInfo", labels["s-remote-git"])
	}
	if labels["s-remote-home"] == "~" {
		t.Errorf("remote space label = %q: matched remote cwd against local os.UserHomeDir", labels["s-remote-home"])
	}
}

// Blocking probe (NeonH §3.4, Carbon §3.3): ShellWorkspace must never join a
// local phone shell into a remote pane's space just because the remote pane's
// Cwd string matches a real local directory.
func TestShellWorkspaceIgnoresRemotePaneWithLocalCwd(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, srv := newBackend(t)
	remote := reported("box/t4", "claude-code", "idle")
	remote.Cwd = localDir
	remote.Labels = map[string]string{"herdr.space": "s-remote", "herdr.tab": "a"}
	srv.Add(remote)

	snap, err := b.Snapshot(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if ws := b.ShellWorkspace(snap, localDir); ws != "" {
		t.Fatalf("ShellWorkspace(snap, %q) = %q, want empty (must not join remote space)", localDir, ws)
	}
}

// Blocking probe (NeonH §3.4, §3.8): SplitPane must not open a local shell
// inside a remote pane's space, and StartAgent must not type local OS priority
// wrappers (like macOS /usr/sbin/taskpolicy) into a remote pane.
func TestSplitPaneAndStartAgentRefuseRemotePane(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, srv := newBackend(t)
	b.GOOS = "darwin"
	b.LookPath = func(file string) (string, error) {
		switch file {
		case "nice":
			return "/usr/bin/nice", nil
		case "taskpolicy", "/usr/sbin/taskpolicy":
			return "/usr/sbin/taskpolicy", nil
		case "claude":
			return "/opt/homebrew/bin/claude", nil
		}
		return "", exec.ErrNotFound
	}
	remote := liveTerminal("box/t4", "zsh")
	remote.Cwd = localDir
	remote.Labels = map[string]string{"herdr.space": "s-remote", "herdr.tab": "a"}
	srv.Add(remote)
	ctx := testContext(t)

	if pane, err := b.SplitPane(ctx, "box~t4", localDir); err == nil {
		t.Errorf("SplitPane(box~t4) succeeded (%+v), want refusal for remote anchor", pane)
	}
	if n := countRequests(srv, "terminal.run"); n != 0 {
		t.Errorf("SplitPane(box~t4) issued %d terminal.run requests, want 0", n)
	}

	if err := b.StartAgent(ctx, "box~t4", "", "claude", nil, 0); err == nil {
		t.Errorf("StartAgent(box~t4) succeeded, want refusal for remote pane")
	}
	if n := countRequests(srv, "terminal.send"); n != 0 {
		t.Errorf("StartAgent(box~t4) issued %d terminal.send requests, want 0", n)
	}
}

// Untested guard from PR #623 (NeonH §5, Carbon §5): ProcessInfo must never
// return remote PIDs that could match unrelated local processes.
func TestProcessInfoRemoteReturnsNoLocalPIDs(t *testing.T) {
	b, srv := newBackend(t)
	ctx := testContext(t)
	localPID := os.Getpid()
	remote := reported("box/t4", "claude-code", "working")
	remote.PID = &localPID
	remote.Foreground = &hc.Foreground{PID: localPID, Name: "claude", Argv: []string{"claude"}}
	srv.Add(remote)

	for _, id := range []string{"box~t4", "box/t4"} {
		info, err := b.ProcessInfo(ctx, id)
		if err != nil {
			t.Fatalf("ProcessInfo(%q) error = %v", id, err)
		}
		if info.ShellPID != 0 || info.ForegroundProcessGroup != 0 || len(info.ForegroundProcesses) != 0 {
			t.Fatalf("ProcessInfo(%q) leaked remote PIDs: %+v", id, info)
		}
	}
	if n := countRequests(srv, "terminal.get"); n != 0 {
		t.Fatalf("ProcessInfo on remote panes made %d terminal.get requests, want 0", n)
	}
}

// Regression test (category 3): existing remote behaviour (status, send, read,
// fleet events) keeps working end-to-end through herdrcore.Backend.
func TestRemoteBehaviourStatusSendReadEvents(t *testing.T) {
	b, srv := newBackend(t)
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()

	srv.Add(reported("box/t4", "claude-code", "idle"))
	srv.SetScreen("box/t4", "remote status screen\n")
	srv.Add(liveTerminal("box/t5", "zsh"))

	// Status & listing.
	agents, err := b.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].PaneID != "box~t4" || agents[0].Status != herdr.StateIdle || agents[0].Agent != "claude" {
		t.Fatalf("ListAgents = %+v, want [box~t4 idle claude]", agents)
	}
	got, err := b.GetAgent(ctx, "box~t4")
	if err != nil || got.PaneID != "box~t4" || got.Status != herdr.StateIdle {
		t.Fatalf("GetAgent(box~t4) = (%+v, %v)", got, err)
	}

	// Read.
	rd, err := b.Read(ctx, "box~t4", herdr.SourceVisible, 0)
	if err != nil || rd.Text != "remote status screen\n" {
		t.Fatalf("Read(box~t4) = (%+v, %v)", rd, err)
	}

	// Send prompt, keys, interrupt.
	if _, err := b.Prompt(ctx, "box~t4", "check logs"); err != nil {
		t.Fatalf("Prompt(box~t4): %v", err)
	}
	sends := requestParams[struct {
		Terminal string `json:"terminal"`
		Text     string `json:"text"`
	}](t, srv, "terminal.send")
	if len(sends) != 1 || sends[0].Terminal != "box/t4" || sends[0].Text != "check logs" {
		t.Fatalf("terminal.send = %+v", sends)
	}
	if err := b.SendKeys(ctx, "box~t4", "down", "enter"); err != nil {
		t.Fatalf("SendKeys(box~t4): %v", err)
	}
	keys := requestParams[struct {
		Terminal string   `json:"terminal"`
		Keys     []string `json:"keys"`
	}](t, srv, "terminal.keys")
	if len(keys) != 1 || keys[0].Terminal != "box/t4" || !slices.Equal(keys[0].Keys, []string{"down", "enter"}) {
		t.Fatalf("terminal.keys = %+v", keys)
	}
	if err := b.Interrupt(ctx, "box~t4"); err != nil {
		t.Fatalf("Interrupt(box~t4): %v", err)
	}

	// Fleet events: remote agent detection on status + exit.
	fleet := b.SubscribeFleet(ctx)
	awaitSubscribers(t, srv, 1)

	srv.SetStatus("box/t5", &hc.Status{State: "working", App: "claude-code"})
	ev := receive(t, fleet)
	if ev.Kind != herdr.FleetAgentDetected || ev.PaneID != "box~t5" || ev.Agent != "claude" {
		t.Fatalf("remote detect event = %+v, want FleetAgentDetected for box~t5", ev)
	}

	srv.Finish("box/t5", "exited")
	ev = receive(t, fleet)
	if ev.Kind != herdr.FleetPaneExited || ev.PaneID != "box~t5" {
		t.Fatalf("remote exit event = %+v, want FleetPaneExited for box~t5", ev)
	}
}
