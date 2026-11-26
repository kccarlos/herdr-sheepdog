package eval

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Pins app-factory.md §5.1, §10 PR 4 & docs/dev/eval/README.md (scale-ten bar):
// projects beside workday-scale-ten's 10 ordinary agents keep ordinary lines
// verbatim/ordered, add 1 compact row per running/stopped project (no finished
// projects or factory terminals), and bound roster growth.
func TestScaleTenRosterWithProjectsStaysWithinBar(t *testing.T) {
	scenarios, err := LoadSimFile("../../testdata/eval/sim/workday.json")
	if err != nil {
		t.Fatal(err)
	}
	baseSc := scenarios[slices.IndexFunc(scenarios, func(s SimScenario) bool { return s.ID == "workday-scale-ten" })]
	start := func(sc SimScenario) (*simWorld, string) {
		fake := braintest.NewModel()
		fake.Default, fake.EventDefault = &braintest.Step{Text: braintest.Reply("OK.", "")}, braintest.NothingToAdd()
		w, err := newSimWorld(context.Background(), sc, simWorldOptions{brainCfg: brain.Config{MaxSteps: 2, UtteranceTimeout: 2 * time.Second}, model: fake, fixtureDir: "../../testdata/herdr"})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range w.brain.PromptSections() {
			if s.Name == "roster" {
				return w, s.Text
			}
		}
		return w, ""
	}
	w0, baseRoster := start(baseSc)
	w0.close()
	t0, withFac := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC), baseSc
	withFac.Factory = []SimProject{
		{ProjectHead: factory.ProjectHead{ID: "proj-1a2b3c4d", Folder: "/work/notes", Goal: "Notes", State: "running", CoordinatorPane: "coord-notes-1", Counts: map[string]int{"working": 3, "owner_answer": 1, "done": 9}}, CoordinatorKind: "claude", CoordinatorState: "working", CoordinatorScreen: "codex-working.screen.txt", Asks: []flow.Ask{{ID: "ask-1", Project: "proj-1a2b3c4d", Task: "T10", Kind: flow.KindDelete, Question: "Delete old notes table?", Options: []string{"Delete", "Keep"}, Recommend: "Keep", At: t0}}},
		{ProjectHead: factory.ProjectHead{ID: "proj-2b3c4d5e", Folder: "/work/recipes", Goal: "Recipes", State: "running", CoordinatorPane: "coord-recipes-1"}, CoordinatorKind: "claude", CoordinatorState: "working", CoordinatorScreen: "codex-working.screen.txt"},
		{ProjectHead: factory.ProjectHead{ID: "proj-3c4d5e6f", Folder: "/work/docs-site", Goal: "Docs", State: "stopped"}},
		{ProjectHead: factory.ProjectHead{ID: "proj-4d5e6f70", Folder: "/work/old-archive", Goal: "Old", State: "finished"}},
	}
	w1, _ := start(withFac)
	defer w1.close()
	w1.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "hc:w9", To: herdr.StateWorking, At: t0, Agent: "codex", Name: "worker-t8", Labels: map[string]string{"sheepdog.project": "proj-1a2b3c4d", "sheepdog.role": flow.RoleWorker}})
	w1.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "hc:r9", To: herdr.StateWorking, At: t0, Agent: "claude", Name: "rev-t7", Labels: map[string]string{"sheepdog.project": "proj-1a2b3c4d", "sheepdog.role": flow.RoleReviewer}})
	facRoster := ""
	for _, s := range w1.brain.PromptSections() {
		if s.Name == "roster" {
			facRoster = s.Text
		}
	}
	prev := -1
	for _, line := range strings.Split(baseRoster, "\n") {
		if head, _, ok := strings.Cut(line, " for "); ok && strings.HasPrefix(line, "- ") {
			if pos := strings.Index(facRoster, head); pos < 0 || pos <= prev {
				t.Errorf("scale-ten agent line %q missing or reordered", head)
			} else {
				prev = pos
			}
		}
	}
	lower := strings.ToLower(facRoster)
	for _, want := range []string{"notes", "recipes", "docs-site", "running", "stopped"} {
		if !strings.Contains(lower, want) {
			t.Errorf("scale-ten+factory roster missing %q", want)
		}
	}
	if !strings.Contains(lower, "1 waiting") && !strings.Contains(lower, "waiting on you") {
		t.Errorf("scale-ten+factory roster missing waiting count")
	}
	for _, bad := range []string{"old-archive", "coord-notes", "coord-recipes", "worker-t8", "rev-t7"} {
		if strings.Contains(lower, bad) {
			t.Errorf("scale-ten+factory roster leaked %q", bad)
		}
	}
	if extra := len(facRoster) - len(baseRoster); extra > 3*350 {
		t.Errorf("scale-ten+factory roster grew by %d chars (want <= %d)", extra, 3*350)
	}
}
