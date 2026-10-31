package herdrcore_test

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func TestPhoneShellOpensInTheFoldersExistingSpace(t *testing.T) {
	b, srv := newBackend(t)
	dir := t.TempDir()
	app := liveTerminal("t1", "zsh")
	app.Cwd = dir
	app.Labels = map[string]string{"herdr.space": "s-existing", "herdr.tab": "k-1"}
	srv.Add(app)
	ctx := testContext(t)

	snap, err := b.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	space := b.ShellWorkspace(snap, dir)
	if space != "s-existing" {
		t.Fatalf("ShellWorkspace = %q, want s-existing", space)
	}
	pane, err := b.OpenShell(ctx, space, dir)
	if err != nil {
		t.Fatal(err)
	}
	if pane.WorkspaceID != space || pane.TabID == "" || pane.TabID == "k-1" || !pane.PhoneShell {
		t.Fatalf("phone shell pane = %+v", pane)
	}

	runs := requestParams[hc.RunParams](t, srv, "terminal.run")
	if len(runs) != 1 {
		t.Fatalf("terminal.run requests = %d", len(runs))
	}
	run := runs[0]
	if run.Cwd != dir || len(run.Argv) != 0 || run.Line != "" || !run.NoParent || !run.Keep ||
		!maps.Equal(run.Env, map[string]string{"DISABLE_AUTO_UPDATE": "true"}) ||
		run.Labels["sheepdog"] != "shell" || run.Labels["herdr.space"] != space || run.Labels["herdr.tab"] != pane.TabID ||
		len(run.Labels) != 3 {
		t.Fatalf("terminal.run params = %+v", run)
	}
}

func TestPhoneShellOpensANewSpaceForAFolder(t *testing.T) {
	b, srv := newBackend(t)
	dir := t.TempDir()
	ctx := testContext(t)

	snap, err := b.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if space := b.ShellWorkspace(snap, dir); space != "" {
		t.Fatalf("ShellWorkspace for a new folder = %q", space)
	}
	pane, err := b.OpenShell(ctx, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if pane.WorkspaceID == "" || pane.TabID == "" || !pane.PhoneShell {
		t.Fatalf("phone shell pane = %+v", pane)
	}
	run := requestParams[hc.RunParams](t, srv, "terminal.run")[0]
	if run.Labels["sheepdog"] != "shell" || run.Labels["herdr.space"] != pane.WorkspaceID || run.Labels["herdr.tab"] != pane.TabID {
		t.Fatalf("terminal.run params = %+v", run)
	}
}

func TestPhoneShellFolderMatchFollowsSymlinks(t *testing.T) {
	b, srv := newBackend(t)
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	app := liveTerminal("t1", "zsh")
	app.Cwd = dir
	app.Labels = map[string]string{"herdr.space": "s-existing"}
	srv.Add(app)

	snap, err := b.Snapshot(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if space := b.ShellWorkspace(snap, link); space != "s-existing" {
		t.Fatalf("ShellWorkspace through a symlink = %q", space)
	}
}

func TestPhoneShellIsNeverAnAgent(t *testing.T) {
	b, srv := newBackend(t)
	dir := t.TempDir()
	shell, err := b.OpenShell(testContext(t), "", dir)
	if err != nil {
		t.Fatal(err)
	}
	srv.Add(hc.Terminal{
		Address: shell.PaneID,
		State:   "live",
		Argv:    []string{"codex"},
		Cwd:     dir,
		Labels:  map[string]string{"sheepdog": "shell", "herdr.space": shell.WorkspaceID, "herdr.tab": shell.TabID},
	})

	agents, err := b.ListAgents(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 0 {
		t.Fatalf("phone shell listed as an agent: %+v", agents)
	}
}

func TestCloseShellRefusesANonShellTerminal(t *testing.T) {
	b, srv := newBackend(t)
	agent := liveTerminal("t1", "codex")
	agent.Labels = map[string]string{"sheepdog": "spawn"}
	srv.Add(agent)

	if err := b.CloseShell(testContext(t), "t1"); err == nil {
		t.Fatal("CloseShell killed a non-shell terminal")
	}
	if n := countRequests(srv, "terminal.kill"); n != 0 {
		t.Fatalf("terminal.kill requests = %d", n)
	}
}

func TestCloseShellKillsAShell(t *testing.T) {
	b, srv := newBackend(t)
	dir := t.TempDir()
	ctx := testContext(t)
	pane, err := b.OpenShell(ctx, "", dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := b.CloseShell(ctx, pane.PaneID); err != nil {
		t.Fatal(err)
	}
	kills := requestParams[struct {
		Terminal string `json:"terminal"`
	}](t, srv, "terminal.kill")
	if len(kills) != 1 || kills[0].Terminal != pane.PaneID {
		t.Fatalf("terminal.kill params = %+v", kills)
	}
	agents, err := b.ListAgents(ctx)
	if err != nil || len(agents) != 0 {
		t.Fatalf("agents after close = %+v, %v", agents, err)
	}
}
