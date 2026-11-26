package inject

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

var _ supervisor.Factory = (*Factory)(nil)

func changedProjects(log *recLog) []string {
	log.mu.Lock()
	evs := slices.Clone(log.events)
	log.mu.Unlock()
	var out []string
	for _, ev := range evs {
		var m map[string]string
		if ev.Source == "factory" && ev.Type == "factory.changed" {
			if b, _ := json.Marshal(ev.Payload); json.Unmarshal(b, &m) == nil {
				out = append(out, m["project"])
			}
		}
	}
	return out
}

// Pins app-factory.md §3.1, §3.2, §4, §5.3, §6.1, §6.3, §8 & §9: inject.Factory
// reuses real factory/flow types for projects, names/ties, coordinator_pane,
// tasks with steps, asks with proofs, Say, Start/Stop, and factory.changed.
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
	f.SetProject(notes) // identical head+log re-save must stay quiet
	f.SetProject(FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-2b3c4d5e", Folder: "/work/b/notes", Goal: "Second", State: "stopped", CoordinatorPane: "stale-s"}})
	f.SetProject(FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-3c4d5e6f", Goal: "Archived", State: "finished", CoordinatorPane: "stale-f"}})
	heads := f.ProjectHeads()
	if len(heads) != 3 || len(f.Projects()) != 3 || heads[0].Name != "notes" || heads[1].Name != "notes" || heads[2].Name != "proj-3c4d5e6f" ||
		heads[0].CoordinatorPane != "coord-notes-1" || heads[0].Counts["working"] != 1 || heads[0].Alert == nil || heads[1].CoordinatorPane != "" || heads[2].CoordinatorPane != "" ||
		f.ProjectName("proj-1a2b3c4d") != "notes" || f.ProjectName("proj-3c4d5e6f") != "proj-3c4d5e6f" {
		t.Fatalf("ProjectHeads = %+v", heads)
	}
	if got := changedProjects(log); !slices.Equal(got, []string{"proj-1a2b3c4d", "proj-2b3c4d5e", "proj-3c4d5e6f"}) {
		t.Errorf("factory.changed = %v", got)
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
	if err := f.Stop("proj-1a2b3c4d"); err != nil || f.ProjectHeads()[0].State != "stopped" || f.ProjectHeads()[0].CoordinatorPane != "" || f.Start("/work/a/notes", "Notes", 0) != nil || f.ProjectHeads()[0].State != "running" {
		t.Errorf("Stop/Start: err=%v head=%+v", err, f.ProjectHeads()[0])
	}
}

// Pins PR 3 review: SetProject must not race with Stop/Start on ProjectHead
// reads under -race.
func TestFactorySetProjectRace(t *testing.T) {
	f := NewFactory(&recLog{})
	f.SetProject(FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-race", Folder: "/work/race", Goal: "Race", State: "running", CoordinatorPane: "coord-1"}})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 500 {
			f.SetProject(FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-race", Folder: "/work/race", Goal: "Race", State: "running", CoordinatorPane: "coord-1"}})
		}
	}()
	go func() {
		defer wg.Done()
		for range 500 {
			_ = f.Stop("proj-race")
			_ = f.Start("/work/race", "Race", 0)
		}
	}()
	wg.Wait()
}

// Pins Codex item 4 & Neon item 1: SetProject and ProjectHeads defensively copy
// Counts and Alert so caller/reader mutations cannot alias stored state.
func TestFactoryHeadDefensiveCopy(t *testing.T) {
	f := NewFactory(&recLog{})
	counts := map[string]int{"working": 1}
	alert := &factory.ProjectAlert{Kind: flow.AlertAgentsDown, Cause: "signed out", Fix: "sign in"}
	f.SetProject(FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-copy", Folder: "/work/copy", Goal: "Copy", State: "running", Counts: counts, Alert: alert}})
	counts["working"] = 99
	alert.Cause = "caller-mutated"
	heads := f.ProjectHeads()
	if heads[0].Counts["working"] != 1 || heads[0].Alert.Cause != "signed out" {
		t.Errorf("SetProject aliased caller head: counts=%v alert=%+v", heads[0].Counts, heads[0].Alert)
	}
	heads[0].Alert.Cause = "reader-mutated"
	if again := f.ProjectHeads(); again[0].Alert.Cause != "signed out" {
		t.Errorf("ProjectHeads aliased Alert: %+v", again[0].Alert)
	}
}

// Pins Codex item 4: appending a log line with an identical head emits factory.changed.
func TestFactoryChangedOnLogAppendWithSameHead(t *testing.T) {
	t0, log := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC), &recLog{}
	f := NewFactory(log)
	p := FactoryProject{
		ProjectHead: factory.ProjectHead{ID: "proj-log", Folder: "/work/notes", Goal: "Notes", State: "running"},
		Log:         []factory.LogEntry{{At: t0, By: "coordinator", Decision: "plan amended", Why: "added T1"}},
	}
	f.SetProject(p)
	f.SetProject(p)
	p.Log = append(p.Log, factory.LogEntry{At: t0.Add(time.Minute), By: "coordinator", Task: "T1", Decision: "sent back", Why: "needs test"})
	f.SetProject(p)
	if got := changedProjects(log); !slices.Equal(got, []string{"proj-log", "proj-log"}) {
		t.Fatalf("factory.changed = %v, want 2 events (initial + log append)", got)
	}
}

// Pins Codex item 2 & Neon items 2-3: asks derive Gated from Kind and default
// Status to open; Answer enforces invalid_option, proof_required, and ungated asks.
func TestFactoryAskNormalizationAndEngineProofRules(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)
	f := NewFactory(nil)
	f.SetProject(FactoryProject{
		ProjectHead: factory.ProjectHead{ID: "proj-1", Folder: "/work/notes", Goal: "Notes", State: "running"},
		Asks:        []flow.Ask{{ID: "ask-seed", Project: "proj-1", Task: "T1", Kind: flow.KindRelease, Question: "Tag?", Options: []string{"Tag", "Wait"}, Recommend: "Wait", At: t0}},
	})
	f.OpenAsk(flow.Ask{ID: "ask-del", Project: "proj-1", Task: "T2", Kind: flow.KindDelete, Question: "Delete?", Options: []string{"Delete", "Keep"}, Recommend: "Keep", At: t0.Add(time.Second)})
	f.OpenAsk(flow.Ask{ID: "ask-dec", Project: "proj-1", Task: "T3", Kind: flow.KindDecision, Question: "Which?", Options: []string{"A", "B"}, Recommend: "A", At: t0.Add(2 * time.Second)})

	open := f.Asks(true)
	if len(open) != 3 {
		t.Errorf("open asks = %d (%+v), want 3 normalized asks", len(open), open)
	} else {
		if !open[0].Gated || open[0].Status != flow.AskStatusOpen || open[0].Folder != "/work/notes" {
			t.Errorf("seeded release ask = %+v, want Gated=true Status=open Folder=/work/notes", open[0])
		}
		if !open[1].Gated || open[1].Status != flow.AskStatusOpen {
			t.Errorf("opened delete ask = %+v, want Gated=true Status=open", open[1])
		}
		if open[2].Gated || open[2].Status != flow.AskStatusOpen {
			t.Errorf("opened decision ask = %+v, want Gated=false Status=open", open[2])
		}
	}

	var ref *factory.Refusal
	if err := f.Answer("ask-del", "Keep", factory.AnswerProof{}); !errors.As(err, &ref) || ref.Code != "proof_required" {
		t.Errorf("empty proof err = %v (ref=%+v), want code proof_required", err, ref)
	}
	badProof, err := f.MintProof("ask-del", "Bogus", factory.ProofInputTap, "dev-1")
	if err != nil {
		t.Fatalf("MintProof(Bogus) = %v", err)
	}
	if err := f.Answer("ask-del", "Bogus", badProof); !errors.As(err, &ref) || ref.Code != "invalid_option" {
		t.Errorf("invalid option err = %v (ref=%+v), want code invalid_option", err, ref)
	}
	if err := f.Answer("ask-dec", "A", factory.AnswerProof{}); err != nil {
		t.Errorf("ungated decision answer without proof = %v, want nil", err)
	}
	if g, err := f.Graph("proj-1"); err != nil || g.Tasks == nil {
		t.Errorf("Graph(empty tasks) = (%+v, %v), want non-nil slice", g, err)
	}
}

// Pins Start/Stop bounds: no folderless match, no empty-goal Start, finished
// Stop is a no-op, and finished Start with the same goal refuses closed.
func TestFactoryStartStopBounds(t *testing.T) {
	f := NewFactory(nil)
	f.SetProject(FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-stop", Folder: "/work/stop", Goal: "Ship", State: "stopped"}})
	f.SetProject(FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-fin", Folder: "/work/fin", Goal: "Done", State: "finished"}})
	f.SetProject(FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-nofolder", Goal: "Bare", State: "stopped"}})

	if err := f.Start("", "Bare", 0); err == nil {
		t.Errorf("Start with empty folder matched folderless project")
	}
	if err := f.Start("/work/stop", "", 0); err == nil {
		t.Errorf("Start with empty goal succeeded, want error")
	}
	if err := f.Stop("proj-fin"); err != nil {
		t.Errorf("Stop(finished) = %v, want nil no-op", err)
	}
	for _, h := range f.ProjectHeads() {
		if h.ID == "proj-fin" && h.State != "finished" {
			t.Errorf("Stop(finished) changed state to %q", h.State)
		}
	}
	var ref *factory.Refusal
	if err := f.Start("/work/fin", "Done", 0); !errors.As(err, &ref) || ref.Code != flow.RefuseClosed {
		t.Errorf("Start(finished, same goal) = %v (ref=%+v), want code %s", err, ref, flow.RefuseClosed)
	}
}
