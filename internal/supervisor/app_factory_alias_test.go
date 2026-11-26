package supervisor

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// Pins PR 1 fix round 2 (app-factory.md §3.2: a project's alias is unique,
// only the spoken name "the notes factory" is shared): a user-named agent
// "notes" and the coordinators of two projects named notes keep three
// distinct aliases, "notes" still reaches the user's agent, and each
// coordinator still speaks as "the notes factory".
func TestCoordinatorAliasStaysUniqueBesideSameNamedAgent(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)
	const user = "w1:p1"
	if _, _, err := st.Apply(herdr.Transition{PaneID: user, To: herdr.StateIdle, At: at, Agent: "codex", WorkspaceID: "w1", WorkspaceLabel: "notes", Source: "socket"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAlias(user, "notes"); err != nil {
		t.Fatal(err)
	}
	coords := map[string]string{"hc:coord-1": "proj-1a2b3c4d", "hc:coord-2": "proj-9f9f9f9f"}
	for pane, proj := range coords {
		labels := map[string]string{"sheepdog.project": proj, "sheepdog.role": flow.RoleCoordinator}
		if _, _, err := st.ApplyWithProjectName(herdr.Transition{PaneID: pane, From: herdr.StateWorking, To: herdr.StateBlocked, At: at, Agent: "claude",
			Name: "coord-" + proj[len("proj-"):], WorkspaceID: "ws-" + proj, Labels: labels, Source: "socket"}, "notes"); err != nil {
			t.Fatal(err)
		}
	}

	rows := st.List()
	seen := map[string]string{}
	for _, r := range rows {
		if other, dup := seen[r.Alias]; dup {
			t.Errorf("alias %q is on both %s and %s, want distinct aliases", r.Alias, other, r.PaneID)
		}
		seen[r.Alias] = r.PaneID
	}
	if a, _ := st.Get(user); a.Alias != "notes" {
		t.Errorf("user agent alias = %q, want notes (kept)", a.Alias)
	}
	if got, ok := findAlias(rows, "notes"); !ok || got.PaneID != user {
		t.Errorf("findAlias(notes) = (%q, %v), want the user's agent %s", got.PaneID, ok, user)
	}
	for pane := range coords {
		if a, _ := st.Get(pane); state.Say(a) != "the notes factory" {
			t.Errorf("%s speaks as %q, want %q", pane, state.Say(a), "the notes factory")
		}
	}
}
