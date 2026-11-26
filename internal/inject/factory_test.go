package inject

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

var _ supervisor.Factory = (*Factory)(nil)

// Pins app-factory.md §3.1, §3.2, §4, §5.3, §6.1, §6.3, §8 & §9: inject.Factory
// reuses real factory/flow types for projects, names/ties, coordinator_pane,
// tasks with steps, logs, asks with proofs, Say, Start/Stop, and factory.changed.
func TestFactoryProjectsTasksAsksSayAndChanged(t *testing.T) {
	t0, log := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC), &recLog{}
	f := NewFactory(log)
	ch := f.Changes()
	if got := f.ProjectHeads(); len(got) != 0 {
		t.Fatalf("empty ProjectHeads = %+v", got)
	}
	notes := FactoryProject{
		ProjectHead: factory.ProjectHead{ID: "proj-1a2b3c4d", Folder: "/work/a/notes", Goal: "Notes", State: "running", CoordinatorPane: "coord-notes-1", Alert: &factory.ProjectAlert{Kind: flow.AlertAgentsDown, Cause: "signed out", Fix: "sign in"}},
		Tasks:       []factory.TaskView{{Ref: "T1", Title: "tags", Stage: "working", RunRole: flow.RoleWorker, RunCLI: "codex", RunStartedAt: t0, LastActorDecision: &factory.LogEntry{At: t0, By: "coordinator", Decision: "sent back", Why: "case-insensitive"}, Steps: []factory.StepView{{Name: "planned", State: "done"}, {Name: "implementing", State: "now"}}}},
		Log:         []factory.LogEntry{{At: t0, By: "coordinator", Decision: "plan amended", Why: "added T2"}, {At: t0.Add(time.Minute), By: "coordinator", Task: "T1", Decision: "sent back", Why: "case-insensitive"}},
	}
	f.SetProject(notes)
	f.SetProject(notes) // identical head re-save must stay quiet
	f.SetProject(FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-2b3c4d5e", Folder: "/work/b/notes", Goal: "Second", State: "stopped", CoordinatorPane: "stale-s"}})
	f.SetProject(FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-3c4d5e6f", Goal: "Archived", State: "finished", CoordinatorPane: "stale-f"}})
	heads := f.ProjectHeads()
	if len(heads) != 3 || heads[0].Name != "notes" || heads[1].Name != "notes" || heads[2].Name != "proj-3c4d5e6f" ||
		heads[0].CoordinatorPane != "coord-notes-1" || heads[0].Counts["working"] != 1 || heads[0].Alert == nil || heads[1].CoordinatorPane != "" || heads[2].CoordinatorPane != "" ||
		f.ProjectName("proj-1a2b3c4d") != "notes" || f.ProjectName("proj-3c4d5e6f") != "proj-3c4d5e6f" {
		t.Fatalf("ProjectHeads = %+v", heads)
	}
	var changed []string
	log.mu.Lock()
	evs := slices.Clone(log.events)
	log.mu.Unlock()
	for _, ev := range evs {
		var m map[string]string
		if ev.Source == "factory" && ev.Type == "factory.changed" {
			if b, _ := json.Marshal(ev.Payload); json.Unmarshal(b, &m) == nil {
				changed = append(changed, m["project"])
			}
		}
	}
	if !slices.Equal(changed, []string{"proj-1a2b3c4d", "proj-2b3c4d5e", "proj-3c4d5e6f"}) {
		t.Errorf("factory.changed = %v", changed)
	}
	select {
	case c := <-ch:
		t.Fatalf("unexpected Change on SetProject: %+v", c)
	default:
	}
	if g, err := f.Graph("proj-1a2b3c4d"); err != nil || g.Protocol != 1 || len(g.Tasks) != 1 || len(g.Tasks[0].Steps) != 2 || g.Tasks[0].RunCLI != "codex" || g.Tasks[0].LastActorDecision == nil {
		t.Fatalf("Graph = (%+v, %v)", g, err)
	}
	if _, err := f.Graph("proj-missing"); !errors.Is(err, factory.ErrProjectNotFound) {
		t.Errorf("missing Graph err = %v", err)
	}
	if lines, err := f.LogLines("proj-1a2b3c4d", 10, "T1", false); err != nil || len(lines) != 1 || lines[0].Why != "case-insensitive" {
		t.Fatalf("LogLines(T1) = (%+v, %v)", lines, err)
	}
	f.OpenAsk(flow.Ask{ID: "ask-1", Project: "proj-1a2b3c4d", Task: "T1", Kind: flow.KindDelete, Question: "Delete old notes?", Options: []string{"Delete", "Keep"}, Recommend: "Keep", At: t0})
	if c := <-ch; c.Kind != flow.EvAskOpen || c.AskID != "ask-1" || f.Answer("ask-1", "Keep", factory.AnswerProof{}) == nil {
		t.Fatalf("OpenAsk/proof guard failed: %+v", c)
	}
	if _, err := f.MintProof("ask-1", "Keep", factory.ProofInputTap, "local"); err == nil {
		t.Fatal("MintProof accepted local device")
	}
	proof, err := f.MintProof("ask-1", "Keep", factory.ProofInputTap, "dev-1")
	var ref *factory.Refusal
	if err != nil || f.Answer("ask-1", "Keep", proof) != nil || (<-ch).Kind != "ask_answer" || len(f.Asks(true)) != 0 || !errors.As(f.Answer("ask-1", "Keep", proof), &ref) || ref.Code != flow.RefuseClosed {
		t.Fatalf("MintProof/Answer failed: err=%v open=%d ref=%+v", err, len(f.Asks(true)), ref)
	}
	if err := f.Say("proj-1a2b3c4d", "skip export"); err != nil || (<-ch).Kind != "say" || f.Say("proj-2b3c4d5e", "hold") != nil || !errors.As(f.Say("proj-3c4d5e6f", "late"), &ref) || ref.Code != flow.RefuseClosed {
		t.Fatalf("Say failed: err=%v ref=%+v", err, ref)
	}
	if err := f.Stop("proj-1a2b3c4d"); err != nil || f.ProjectHeads()[0].State != "stopped" || f.ProjectHeads()[0].CoordinatorPane != "" || f.Start("/work/a/notes", "", 0) != nil || f.ProjectHeads()[0].State != "running" {
		t.Errorf("Stop/Start: err=%v head=%+v", err, f.ProjectHeads()[0])
	}
}
