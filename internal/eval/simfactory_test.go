package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// Pins app-factory.md §3.2, §4, §6.1, §6.5, §9 & §10 PR 3-6: factory-only
// scenarios load with DisallowUnknownFields using real ProjectHead/TaskView/Ask
// shapes, validate, and wire inject.Factory into simWorld/RunSim (bridged asks,
// quiet vs blocked coordinator naming, Say, project Investigate, factory.changed,
// and judge Facts).
func TestSimFactoryLoadValidateAndRun(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)
	sc := SimScenario{
		ID: "factory-sim-check", Goal: "check notes factory", Script: []string{"how is notes?"},
		Caps: SimCaps{MaxTurns: 2, MaxSeconds: 20, SilenceSeconds: 2},
		Factory: []SimProject{
			{
				ProjectHead:     factory.ProjectHead{ID: "proj-1a2b3c4d", Folder: "/work/notes", Goal: "Notes", State: "running", CoordinatorPane: "coord-notes-1"},
				CoordinatorKind: "claude", CoordinatorState: "blocked", CoordinatorScreen: "codex-approval-prompt.screen.txt",
				Tasks:  []factory.TaskView{{Ref: "T1", Title: "tags", Stage: "working", RunRole: flow.RoleWorker, RunCLI: "codex", RunStartedAt: t0, LastActorDecision: &factory.LogEntry{At: t0, By: "coordinator", Decision: "sent back", Why: "case-insensitive"}, Steps: []factory.StepView{{Name: "planned", State: "done"}, {Name: "implementing", State: "now"}}}},
				Log:    []factory.LogEntry{{At: t0, By: "coordinator", Decision: "plan amended", Why: "added T2"}},
				Asks:   []flow.Ask{{ID: "ask-1", Project: "proj-1a2b3c4d", Task: "T1", Kind: flow.KindDelete, Gated: true, Question: "Delete old notes table?", Options: []string{"Delete", "Keep"}, Recommend: "Keep", At: t0, Status: flow.AskStatusOpen}},
				Answer: "T1 was split to stay under 400 lines.",
			},
			{ProjectHead: factory.ProjectHead{ID: "proj-2b3c4d5e", Folder: "/work/recipes", Goal: "Recipes", State: "running", CoordinatorPane: "coord-recipes-1"}, CoordinatorKind: "claude", CoordinatorState: "working", CoordinatorScreen: "codex-working.screen.txt"},
		},
	}
	p := filepath.Join(t.TempDir(), "factory.json")
	b, _ := json.Marshal(SimFile{Scenarios: []SimScenario{sc}})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadSimFile(p); err != nil || len(got) != 1 || len(got[0].Factory) != 2 || len(got[0].Factory[0].Tasks[0].Steps) != 2 {
		t.Fatalf("LoadSimFile = (%+v, %v)", got, err)
	}
	for _, bad := range []SimScenario{
		{ID: "empty", Goal: "g"},
		{ID: "noid", Goal: "g", Factory: []SimProject{{ProjectHead: factory.ProjectHead{Folder: "/work/notes"}}}},
		{ID: "dup", Goal: "g", Factory: []SimProject{{ProjectHead: factory.ProjectHead{ID: "p1"}}, {ProjectHead: factory.ProjectHead{ID: "p1"}}}},
		{ID: "badstate", Goal: "g", Factory: []SimProject{{ProjectHead: factory.ProjectHead{ID: "p1", State: "paused"}}}},
	} {
		if bad.validate() == nil {
			t.Errorf("%s validated unexpectedly", bad.ID)
		}
	}
	fake := braintest.NewModel()
	fake.Default, fake.EventDefault = &braintest.Step{Text: braintest.Reply("Offline.", "")}, braintest.NothingToAdd()
	w, err := newSimWorld(context.Background(), sc, simWorldOptions{brainCfg: brain.Config{MaxSteps: 2, UtteranceTimeout: 2 * time.Second}, model: fake, fixtureDir: "../../testdata/herdr"})
	if err != nil {
		t.Fatalf("newSimWorld: %v", err)
	}
	defer w.close()
	if w.factory == nil || len(w.factory.ProjectHeads()) != 2 || w.factory.ProjectName("proj-1a2b3c4d") != "notes" {
		t.Fatalf("world factory = %+v", w.factory)
	}
	for end := time.Now().Add(time.Second); len(w.sup.FactoryQuestions()) == 0 && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
	}
	if qs := w.sup.FactoryQuestions(); len(qs) != 1 || qs[0].ID != "fask-ask-1" || qs[0].Factory == nil || qs[0].Factory.ProjectName != "notes" || qs[0].Factory.Task != "T1" {
		t.Fatalf("FactoryQuestions = %+v", qs)
	}
	if spoken := strings.ToLower(strings.Join(w.sup.SpokenNames(), ",")); !strings.Contains(spoken, "notes") || strings.Contains(spoken, "coord-") || strings.Contains(spoken, "recipes") {
		t.Errorf("SpokenNames = %q", spoken)
	}
	if res, err := (&simQuerier{w: w}).Investigate(context.Background(), supervisor.InvestigationQuery{Workspace: "/work/notes", Question: "why?"}); err != nil || !strings.Contains(res.Answer, "400 lines") {
		t.Fatalf("project Investigate = (%+v, %v)", res, err)
	}
	if err := w.factory.Say("proj-1a2b3c4d", "skip export"); err != nil {
		t.Fatalf("Say = %v", err)
	}
	evDir := t.TempDir()
	run := RunSim(context.Background(), sc, SimOptions{Offline: true, FixtureDir: "../../testdata/herdr", EventsDir: evDir, Settle: 200 * time.Millisecond}, 1)
	if run.Status != RunPass || !strings.Contains(renderSim(run.Transcript), "notes, T1: Delete old notes table?") || !strings.Contains(run.Facts, "notes") || !strings.Contains(run.Facts, "T1") {
		t.Fatalf("RunSim = %s failures=%+v\ntranscript:\n%s\nfacts:\n%s", run.Status, run.Failures, renderSim(run.Transcript), run.Facts)
	}
	if raw, err := os.ReadFile(filepath.Join(evDir, sc.ID+".r1.jsonl")); err != nil || !strings.Contains(string(raw), `"type":"factory.changed"`) {
		t.Errorf("events JSONL missing factory.changed: err=%v", err)
	}
}
