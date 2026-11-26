package web

import (
	"context"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/termproto"
)

// Pins review-pr2-neon finding 1 on /term: ended runs in another project must
// never block /term from opening a second project's live current coordinator
// or an ordinary tracked agent that reuses the same terminal address.
func TestTermOpensLiveCoordinatorAndOrdinaryAgentDespiteEndedRunCollision(t *testing.T) {
	paths := factory.Paths{DataDir: t.TempDir()}
	save := func(st flow.State) {
		st.Seq, st.Settings = 2, flow.DefaultSettings()
		if err := factory.SaveState(paths.State(st.Project.ID), st); err != nil {
			t.Fatal(err)
		}
	}
	// Finished project with ended workers at "t1" and "t2" (RunID cleared, Run.Address kept).
	save(flow.State{
		Project: flow.Project{ID: "proj-old", Folder: "/work/old", Goal: "Old", Started: false, Finished: true},
		Tasks: []flow.Task{
			{ID: "T1", Title: "done1", Attempts: []flow.Attempt{{BaseCommit: "0", Run: &flow.RunRecord{Role: flow.RoleWorker, CLI: "codex", Address: "t1", State: flow.PhaseEnded, Result: flow.EndStopped}}}},
			{ID: "T2", Title: "done2", Attempts: []flow.Attempt{{BaseCommit: "0", Run: &flow.RunRecord{Role: flow.RoleWorker, CLI: "codex", Address: "t2", State: flow.PhaseEnded, Result: flow.EndStopped}}}},
		},
	})
	// Active project whose live current coordinator reused address "t1".
	save(flow.State{
		Project: flow.Project{ID: "proj-live", Folder: "/work/live", Goal: "Live", Started: true, RunID: "t1",
			Run: &flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude", Address: "t1", State: flow.PhaseIdle}},
	})

	eng, ft := factory.New(paths, nil), &fakeTerm{version: "0.9.5"}
	h := newHarness(t, harnessOpts{term: ft.deps(), factory: eng})
	h.srv.deps.Factory = eng
	// Ordinary interactive agent (no sheepdog.project label) reused address "t2".
	h.herdr.SetScreen("t2", "claude", "ordinary screen", herdr.StateIdle)
	h.sup.HandleTransition(context.Background(), herdr.Transition{
		PaneID: "t2", To: herdr.StateIdle, At: time.Now(), Agent: "claude", Name: "ordinary", WorkspaceID: "ws-1", WorkspaceLabel: "main",
	})

	// Repeated availability checks catch Go's randomized map iteration across projects.
	missed := 0
	for range 100 {
		if !h.srv.termPaneAvailable("t1") {
			missed++
		}
	}
	if missed > 0 {
		t.Errorf("termPaneAvailable(live coordinator \"t1\") = false in %d/100 checks (collided with ended worker in proj-old)", missed)
	}
	c := dialTerm(t, h, true)
	c.send(openMsg(1, "t1", termproto.ModeControl))
	if m, _ := c.next(); m.Type != termproto.TypeOpened || m.PaneID != "t1" {
		t.Errorf("term-open(live coordinator \"t1\") = %+v, want term-opened", m)
	} else {
		c.expectFrame()
	}
	c.send(openMsg(2, "t2", termproto.ModeControl))
	if m, _ := c.next(); m.Type != termproto.TypeOpened || m.PaneID != "t2" {
		t.Errorf("term-open(ordinary agent \"t2\") = %+v, want term-opened", m)
	}
}
