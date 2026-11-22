package herdr_test

import (
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/herdrtest"
)

func TestFactoryQuietRule(t *testing.T) {
	allStates := []herdr.State{
		herdr.StateIdle,
		herdr.StateWorking,
		herdr.StateBlocked,
		herdr.StateDone,
		herdr.StateUnknown,
	}

	// Factory workers, reviewers, checks and helpers are fully quiet for every state.
	for _, role := range []string{"worker", "reviewer", "checks", "helper"} {
		labels := map[string]string{"sheepdog.project": "demo", "sheepdog.role": role}
		for _, st := range allStates {
			if !herdr.IsFactoryQuiet(labels, st) {
				t.Errorf("IsFactoryQuiet(role=%s, state=%s) = false, want true", role, st)
			}
		}
	}

	// Factory coordinator speaks only when blocked; every other state is quiet.
	coordLabels := map[string]string{"sheepdog.project": "demo", "sheepdog.role": "coordinator"}
	for _, st := range allStates {
		wantQuiet := st != herdr.StateBlocked
		if got := herdr.IsFactoryQuiet(coordLabels, st); got != wantQuiet {
			t.Errorf("IsFactoryQuiet(role=coordinator, state=%s) = %v, want %v", st, got, wantQuiet)
		}
	}

	// Non-factory terminals are never silenced by the quiet rule.
	for _, labels := range []map[string]string{nil, {}, {"team": "core"}} {
		for _, st := range allStates {
			if herdr.IsFactoryQuiet(labels, st) {
				t.Errorf("IsFactoryQuiet(labels=%v, state=%s) = true, want false", labels, st)
			}
		}
	}
}

func TestFactoryWatcherPropagatesLabels(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w5", Label: "demo"})
	a := codexAgent()
	a.Labels = map[string]string{"sheepdog.project": "demo", "sheepdog.role": "worker"}
	srv.SetAgent(a)

	w, _, _ := startWatcher(t, srv, time.Hour, 1)
	ch := w.Transitions()

	tr := expectTransition(t, ch, "", herdr.StateIdle)
	if tr.Labels["sheepdog.project"] != "demo" || tr.Labels["sheepdog.role"] != "worker" {
		t.Fatalf("bootstrap transition labels = %v, want sheepdog.project=demo sheepdog.role=worker", tr.Labels)
	}

	srv.EmitStatus(a.PaneID, herdr.StateWorking)
	tr = expectTransition(t, ch, herdr.StateIdle, herdr.StateWorking)
	if tr.Labels["sheepdog.project"] != "demo" || tr.Labels["sheepdog.role"] != "worker" {
		t.Fatalf("working transition labels = %v, want sheepdog.project=demo sheepdog.role=worker", tr.Labels)
	}
}
