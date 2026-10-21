package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// spacesWorld is a spawn world with the bench project open as space w3
// (a git checkout on main, a Claude in w3:p1 and a shell in w3:p2).
func spacesWorld(t *testing.T) *spawnWorld {
	t.Helper()
	w := newSpawnWorld(t)
	if err := os.MkdirAll(filepath.Join(w.bench, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.bench, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w3", Label: "bench"})
	w.srv.SetPane(herdr.PaneInfo{PaneID: "w3:p1", WorkspaceID: "w3", Cwd: w.bench, Agent: "claude"})
	w.srv.SetPane(herdr.PaneInfo{PaneID: "w3:p2", WorkspaceID: "w3", Cwd: w.bench})
	w.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w3:p1", To: herdr.StateIdle, At: w.clock.Now(), Agent: "claude", WorkspaceID: "w3", WorkspaceLabel: "bench", Cwd: w.bench})
	for _, m := range []string{"workspace.rename", "workspace.close", "worktree.remove"} {
		w.srv.Handle(m, func(json.RawMessage) (any, error) { return map[string]any{"type": "ok"}, nil })
	}
	return w
}

func TestSpacesListsSpacesFoldersAndKinds(t *testing.T) {
	w := spacesWorld(t)
	v, err := w.sup.Spaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Spaces) != 1 {
		t.Fatalf("spaces = %+v", v.Spaces)
	}
	sp := v.Spaces[0]
	if sp.ID != "w3" || sp.Name != "bench" || sp.Path != w.bench || !sp.Git || sp.Branch != "main" || sp.Repo != "bench" || sp.Worktree {
		t.Errorf("space = %+v", sp)
	}
	if len(sp.Agents) != 1 || sp.Agents[0].PaneID != "w3:p1" || sp.Agents[0].Name != "Claude" || sp.Agents[0].Kind != "claude" {
		t.Errorf("agents = %+v", sp.Agents)
	}
	if len(sp.Shells) != 1 || sp.Shells[0] != "w3:p2" {
		t.Errorf("shells = %v", sp.Shells)
	}
	if len(v.Folders) != 1 || v.Folders[0].Name != "app" || v.Folders[0].Git {
		t.Errorf("folders = %+v", v.Folders)
	}
	if strings.Join(v.Kinds, " ") != "claude codex agy" || !v.Worktrees {
		t.Errorf("kinds = %v, worktrees = %v", v.Kinds, v.Worktrees)
	}
}

// A new agent starts in an open space (split from its first pane) or in
// a new space, even in a folder a space already works in.
func TestUserNewAgentInASpaceOrANewOne(t *testing.T) {
	w := spacesWorld(t)
	if _, err := w.sup.UserNewAgent(context.Background(), NewAgentRequest{Kind: "codex", Space: "w3"}); err != nil {
		t.Fatal(err)
	}
	if split := w.params("pane.split"); len(split) != 1 || split[0]["target_pane_id"] != "w3:p1" || split[0]["cwd"] != w.bench {
		t.Fatalf("pane.split = %v", split)
	}
	if _, err := w.sup.UserNewAgent(context.Background(), NewAgentRequest{Kind: "codex", Dir: "bench"}); err != nil {
		t.Fatal(err)
	}
	if created := w.params("workspace.create"); len(created) != 1 || created[0]["cwd"] != w.bench {
		t.Fatalf("workspace.create = %v", created)
	}
	for name, req := range map[string]NewAgentRequest{
		"no space":           {Kind: "codex", Space: "w9"},
		"both":               {Kind: "codex", Space: "w3", Dir: "bench"},
		"neither":            {Kind: "codex"},
		"worktree in space":  {Kind: "codex", Space: "w3", Worktree: "fix"},
		"outside the roots":  {Kind: "codex", Dir: "/etc"},
		"kind not installed": {Kind: "gemini", Space: "w3"},
	} {
		if _, err := w.sup.UserNewAgent(context.Background(), req); err == nil {
			t.Errorf("%s: started", name)
		}
	}
	if _, err := w.sup.UserNewAgent(context.Background(), NewAgentRequest{Kind: "codex", Space: "w9"}); !errors.Is(err, ErrNoSuchSpace) {
		t.Errorf("unknown space = %v", err)
	}
}

func TestRenameAndCloseSpace(t *testing.T) {
	w := spacesWorld(t)
	if _, err := w.sup.RenameSpace(context.Background(), "w3", " "); !errors.Is(err, ErrUserAction) {
		t.Errorf("empty name = %v", err)
	}
	if _, err := w.sup.RenameSpace(context.Background(), "w3", "bench  tests"); err != nil {
		t.Fatal(err)
	}
	if r := w.params("workspace.rename"); len(r) != 1 || r[0]["workspace_id"] != "w3" || r[0]["label"] != "bench tests" {
		t.Errorf("workspace.rename = %v", r)
	}

	// Refused while its agent works or waits for an answer.
	for _, st := range []herdr.State{herdr.StateWorking, herdr.StateBlocked} {
		w.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w3:p1", To: st, At: w.clock.Now(), Agent: "claude", WorkspaceID: "w3", WorkspaceLabel: "bench"})
		if _, err := w.sup.CloseSpace(context.Background(), "w3"); !errors.Is(err, ErrSpaceBusy) {
			t.Errorf("close while %s = %v", st, err)
		}
	}
	w.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w3:p1", To: herdr.StateDone, At: w.clock.Now(), Agent: "claude", WorkspaceID: "w3", WorkspaceLabel: "bench"})
	w.sup.cfg.OwnPane = "w3:p2"
	if _, err := w.sup.CloseSpace(context.Background(), "w3"); !errors.Is(err, brain.ErrNotPermitted) {
		t.Errorf("close of sheepdog's own space = %v", err)
	}
	w.sup.cfg.OwnPane = ""
	if len(w.params("workspace.close")) != 0 {
		t.Fatal("a refused close reached herdr")
	}
	if _, err := w.sup.CloseSpace(context.Background(), "w3"); err != nil {
		t.Fatal(err)
	}
	if c := w.params("workspace.close"); len(c) != 1 || c[0]["workspace_id"] != "w3" {
		t.Errorf("workspace.close = %v", c)
	}
	if _, err := w.sup.CloseSpace(context.Background(), "w9"); !errors.Is(err, ErrNoSuchSpace) {
		t.Errorf("unknown space = %v", err)
	}
}

// Only a linked worktree is removed, and not with uncommitted changes
// unless forced.
func TestRemoveWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	w := spacesWorld(t)
	if _, err := w.sup.RemoveWorktree(context.Background(), "w3", true); !errors.Is(err, ErrUserAction) {
		t.Errorf("removing the main checkout = %v", err)
	}
	wt := filepath.Join(w.root, "bench-fix")
	if err := os.Mkdir(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", wt}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "fix")
	w.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w4", Label: "bench-fix", Worktree: &herdr.WorkspaceWorktree{RepoName: "bench", RepoRoot: w.bench, CheckoutPath: wt, Linked: true}})
	w.srv.SetPane(herdr.PaneInfo{PaneID: "w4:p1", WorkspaceID: "w4", Cwd: wt})
	if err := os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.sup.RemoveWorktree(context.Background(), "w4", false); !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("dirty remove = %v", err)
	}
	if len(w.params("worktree.remove")) != 0 {
		t.Fatal("a refused removal reached herdr")
	}
	if _, err := w.sup.RemoveWorktree(context.Background(), "w4", true); err != nil {
		t.Fatal(err)
	}
	if r := w.params("worktree.remove"); len(r) != 1 || r[0]["workspace_id"] != "w4" || r[0]["force"] != true {
		t.Errorf("worktree.remove = %v", r)
	}
}

// Wrap-up commands are kept per space folder in the data dir, listed
// with the space, and run in a new tab of it.
func TestWrapUpCommands(t *testing.T) {
	w := spacesWorld(t)
	ctx := context.Background()
	if _, err := w.sup.SaveCommand(ctx, "w3", "", "Run tests", "go test ./...\nrm -rf /"); !errors.Is(err, ErrUserAction) {
		t.Errorf("two lines = %v", err)
	}
	c, err := w.sup.SaveCommand(ctx, "w3", "", "Run tests", "go test ./...")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "wrapups.json")); err != nil {
		t.Errorf("not stored in the data dir: %v", err)
	}
	if _, err := w.sup.SaveCommand(ctx, "w3", c.ID, "Tests", "go test -race ./..."); err != nil {
		t.Fatal(err)
	}
	v, _ := w.sup.Spaces(ctx)
	if cmds := v.Spaces[0].Commands; len(cmds) != 1 || cmds[0].Name != "Tests" || cmds[0].Command != "go test -race ./..." {
		t.Fatalf("commands = %+v", cmds)
	}
	w.srv.Handle("tab.create", func(p json.RawMessage) (any, error) {
		pane := herdr.PaneInfo{PaneID: "w3:p7", WorkspaceID: "w3", Cwd: w.bench}
		w.srv.SetPane(pane)
		w.srv.SetScreen(pane.PaneID, "bench ❯ ")
		return map[string]any{"type": "tab_created", "root_pane": pane}, nil
	})
	w.srv.Handle("pane.send_input", func(json.RawMessage) (any, error) { return map[string]any{"type": "ok"}, nil })
	pane, err := w.sup.RunCommand(ctx, "w3", c.ID)
	if err != nil || pane != "w3:p7" {
		t.Fatalf("run = %q, %v", pane, err)
	}
	tabs := w.params("tab.create")
	if len(tabs) != 1 || tabs[0]["workspace_id"] != "w3" || tabs[0]["cwd"] != w.bench || tabs[0]["label"] != "Tests" {
		t.Errorf("tab.create = %v", tabs)
	}
	in := w.params("pane.send_input")
	if len(in) != 1 || in[0]["pane_id"] != "w3:p7" || in[0]["text"] != "go test -race ./..." {
		t.Errorf("pane.send_input = %v", in)
	}
	if err := w.sup.DeleteCommand(ctx, "w3", c.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.sup.DeleteCommand(ctx, "w3", c.ID); !errors.Is(err, ErrNoSuchCommand) {
		t.Errorf("second delete = %v", err)
	}
	if list, _ := w.sup.SpaceCommands(ctx, "w3"); len(list) != 0 {
		t.Errorf("after delete = %+v", list)
	}
}
