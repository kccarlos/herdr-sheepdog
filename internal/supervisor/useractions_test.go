package supervisor

import (
	"context"
	"encoding/json"
	"errors"
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
	if res.PaneID != "w20:p1" || res.Spoken != "Starting a new Claude in bench." {
		t.Errorf("result = %+v", res)
	}
	if inputs := w.params("pane.send_input"); len(inputs) != 1 || inputs[0]["text"] != "claude" {
		t.Fatalf("pane.send_input = %v", inputs)
	}
	ev := w.log.ofType("supervisor.spawn")
	if len(ev) != 1 || ev[0].Payload.(map[string]any)["via"] != "user" {
		t.Fatalf("supervisor.spawn = %+v", ev)
	}

	for name, req := range map[string]NewAgentRequest{
		"outside roots": {Kind: "claude", Dir: "/etc"},
		"unknown kind":  {Kind: "bash", Dir: "bench"},
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
	opts, err := w.sup.NewAgentOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Worktree || len(opts.Kinds) != 2 || len(opts.Projects) != 2 {
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
