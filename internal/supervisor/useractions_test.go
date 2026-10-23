package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// A tap on New agent needs no words of the user's and spends none; the
// directory and kind rails still hold.
func TestUserNewAgentIsTheUsersTap(t *testing.T) {
	w := newSpawnWorld(t)
	res, err := w.sup.UserNewAgent(context.Background(), NewAgentRequest{Kind: "claude", Dir: "bench"})
	if err != nil {
		t.Fatal(err)
	}
	if res.PaneID != "w20:p1" || res.Spoken != "Claude is ready in bench." || res.Blocked {
		t.Errorf("result = %+v", res)
	}
	if starts := w.startParams(); len(starts) != 1 || starts[0]["kind"] != "claude" || starts[0]["pane_id"] != "w20:p1" {
		t.Fatalf("agent.start = %v", starts)
	}
	ev := w.log.ofType("supervisor.spawn")
	if len(ev) != 1 || ev[0].Payload.(map[string]any)["via"] != "user" {
		t.Fatalf("supervisor.spawn = %+v", ev)
	}

	for name, req := range map[string]NewAgentRequest{
		"outside roots": {Kind: "claude", Dir: "/etc"},
		"unknown kind":  {Kind: "bash", Dir: "bench"},
		"not installed": {Kind: "gemini", Dir: "bench"},
		"bad branch":    {Kind: "codex", Dir: "bench", Worktree: "../main"},
		"lock branch":   {Kind: "codex", Dir: "bench", Worktree: "fix.lock"},
	} {
		if _, err := w.sup.UserNewAgent(context.Background(), req); err == nil {
			t.Errorf("%s: started", name)
		} else if name != "outside roots" && !errors.Is(err, ErrUserAction) {
			t.Errorf("%s: %v is not a bad request", name, err)
		} else if name == "outside roots" && !errors.Is(err, brain.ErrNotPermitted) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestUserNewAgentInAFreshWorktree(t *testing.T) {
	w := newSpawnWorld(t)
	w.srv.Handle("worktree.create", func(p json.RawMessage) (any, error) {
		pane := herdr.PaneInfo{PaneID: "w30:p1", WorkspaceID: "w30", Cwd: w.bench + "-fix-login", Status: herdr.StateUnknown}
		w.srv.SetPane(pane)
		w.srv.SetScreen(pane.PaneID, "❯ ")
		return map[string]any{"type": "worktree_created", "root_pane": pane}, nil
	})
	gitRepo(t, w.bench)
	opts, err := w.sup.Spaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Worktrees || kindList(opts.Kinds) != "claude:Claude codex:Codex agy:Antigravity" || len(opts.Folders) != 2 {
		t.Fatalf("options = %+v", opts)
	}
	res, err := w.sup.UserNewAgent(context.Background(), NewAgentRequest{Kind: "codex", Dir: "bench", Worktree: "fix-login", Text: "fix the login test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.PaneID != "w30:p1" || !strings.Contains(res.Spoken, "a new worktree fix-login of bench") {
		t.Errorf("result = %+v", res)
	}
	created := w.params("worktree.create")
	if len(created) != 1 || created[0]["cwd"] != w.bench || created[0]["branch"] != "fix-login" || created[0]["focus"] != false {
		t.Fatalf("worktree.create = %v", created)
	}
	if len(w.params("workspace.create")) != 0 {
		t.Error("a workspace was opened in the project folder too")
	}

	// The app shows its own screens for these.
	for name, c := range map[string]struct {
		req    NewAgentRequest
		reason string
	}{
		"not git":       {NewAgentRequest{Kind: "codex", Dir: "app", Worktree: "fix-app"}, ReasonNotGit},
		"checked out":   {NewAgentRequest{Kind: "codex", Dir: "bench", Worktree: "main"}, ReasonBranchExists},
		"not installed": {NewAgentRequest{Kind: "gemini", Dir: "bench"}, ReasonNotInstalled},
	} {
		_, err := w.sup.UserNewAgent(context.Background(), c.req)
		var re *ReasonError
		if !errors.As(err, &re) || re.Reason != c.reason || !errors.Is(err, ErrUserAction) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if len(w.params("worktree.create")) != 1 {
		t.Error("a refused worktree reached herdr")
	}
	// An existing branch checked out nowhere opens (herdr checks it out).
	if out, err := exec.Command("git", "-C", w.bench, "branch", "spare").CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v: %s", err, out)
	}
	_, err = w.sup.UserNewAgent(context.Background(), NewAgentRequest{Kind: "codex", Dir: "bench", Worktree: "spare"})
	var spareErr *ReasonError
	if errors.As(err, &spareErr) {
		t.Errorf("spare branch refused: %v", err)
	}
	if len(w.params("worktree.create")) != 2 {
		t.Error("the spare branch did not reach herdr")
	}
}

// gitRepo makes dir a git repository whose branch main has one commit.
func gitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "start"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

// The user's name for a new agent replaces the placeholder once the
// launch is done.
func TestUserNewAgentTakesTheUsersName(t *testing.T) {
	w := newSpawnWorld(t)
	res, err := w.sup.UserNewAgent(context.Background(), NewAgentRequest{Kind: "claude", Dir: "bench", Name: "  Login   fix "})
	if err != nil {
		t.Fatal(err)
	}
	w.detect(res.PaneID, "", herdr.StateIdle)
	waitFor(t, func() bool { return len(w.params("agent.rename")) == 1 }, "agent.rename")
	if r := w.params("agent.rename")[0]; r["target"] != "w20:p1" || r["name"] != "Login fix" {
		t.Errorf("agent.rename = %v", r)
	}
	long := NewAgentRequest{Kind: "claude", Dir: "bench", Name: strings.Repeat("x", 61)}
	if _, err := w.sup.UserNewAgent(context.Background(), long); !errors.Is(err, ErrUserAction) {
		t.Errorf("a 61-character name: %v", err)
	}
}

// Any installed kind herdr supports starts by tap, agy included; the
// orchestrator's kinds stay brain.SpawnKinds.
func TestUserNewAgentStartsAgy(t *testing.T) {
	w := newSpawnWorld(t)
	res, err := w.sup.UserNewAgent(context.Background(), NewAgentRequest{Kind: "agy", Dir: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Spoken != "Antigravity is ready in app." {
		t.Errorf("result = %+v", res)
	}
	if starts := w.startParams(); len(starts) != 1 || starts[0]["kind"] != "agy" {
		t.Fatalf("agent.start = %v", starts)
	}
	w.hear("open an antigravity in bench")
	if _, err := w.spawn(brain.SpawnRequest{Dir: "bench", Kind: "agy"}); err == nil {
		t.Error("the orchestrator started agy")
	}
}

// A tap start that herdr detects at its trust question has started; one
// that never shows up fails with its pane, left at the shell prompt.
func TestUserNewAgentBlockedAndFailed(t *testing.T) {
	w := newSpawnWorld(t)
	w.startAs = herdr.StateBlocked
	res, err := w.sup.UserNewAgent(context.Background(), NewAgentRequest{Kind: "claude", Dir: "bench"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked || res.Spoken != "Claude started in bench and asks a question first." {
		t.Errorf("result = %+v", res)
	}

	w.noStart = true
	_, err = w.sup.UserNewAgent(context.Background(), NewAgentRequest{Kind: "codex", Dir: "app"})
	var se *StartError
	if !errors.As(err, &se) || se.PaneID != "w20:p2" || !strings.Contains(err.Error(), "Codex did not start in app: herdr did not see it running") {
		t.Fatalf("err = %v", err)
	}
	if w.log.count("supervisor.spawn_not_ready") != 1 {
		t.Error("no supervisor.spawn_not_ready")
	}
}

func TestUserCloseRenameInterrupt(t *testing.T) {
	h := closeWorld(t, braintest.NewModel())
	// The protected agent closes on the user's tap (the app confirmed it
	// by name); no words were said.
	if _, err := h.sup.UserClose(context.Background(), "w1:p1"); err != nil {
		t.Fatal(err)
	}
	if closed := h.srv.RequestsFor("pane.close"); len(closed) != 1 {
		t.Errorf("pane.close = %v", closed)
	}
	h.sup.cfg.OwnPane = "w2:p1"
	if _, err := h.sup.UserClose(context.Background(), "w2:p1"); !errors.Is(err, brain.ErrNotPermitted) {
		t.Errorf("close of the daemon's own pane = %v", err)
	}
	if _, err := h.sup.UserClose(context.Background(), "w9:p9"); !errors.Is(err, ErrNoSuchAgent) {
		t.Errorf("close of an unknown pane = %v", err)
	}
	if _, err := h.sup.UserRename(context.Background(), "w2:p1", "   "); !errors.Is(err, ErrUserAction) {
		t.Errorf("empty name = %v", err)
	}
	if echo, err := h.sup.UserRename(context.Background(), "w2:p1", "voice  app"); err != nil || !strings.Contains(echo, "voice app") {
		t.Errorf("rename = %q, %v", echo, err)
	}
	if _, err := h.sup.UserInterrupt(context.Background(), "w9:p9"); !errors.Is(err, ErrNoSuchAgent) {
		t.Errorf("interrupt of an unknown pane = %v", err)
	}
}
