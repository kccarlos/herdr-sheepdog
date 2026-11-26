package web

import (
	"bufio"
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

var (
	_ Factory      = (*inject.Factory)(nil)
	_ factoryAsker = (*inject.Factory)(nil)
)

type fnLogger func(source, typ, pane string, payload any)

func (f fnLogger) Log(s, typ, p string, pl any) { f(s, typ, p, pl) }

// Pins app-factory.md §3.2, §4, §5.3 & PR 1 shapes: inject.Factory wired into
// web.Server serves /api/state factory list (omitted when empty), fask- cards
// with project name + factory meta, /graph TaskView steps, and factory.changed.
func TestFakeFactoryWebStateGraphAskCardAndEvents(t *testing.T) {
	empty := inject.NewFactory(nil)
	h0 := newHarness(t, harnessOpts{factory: empty})
	h0.srv.deps.Factory = empty
	if got := getState(t, h0); got["factory"] != nil {
		t.Fatalf("empty fake factory leaked factory = %v", got["factory"])
	}
	var logTo logging.Logger
	fk := inject.NewFactory(fnLogger(func(s, typ, p string, pl any) {
		if logTo != nil {
			logTo.Log(s, typ, p, pl)
		}
	}))
	t0 := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)
	fk.SetProject(inject.FactoryProject{
		ProjectHead: factory.ProjectHead{ID: "proj-1a2b3c4d", Folder: "/work/notes", Goal: "Notes", State: "running", CoordinatorPane: "coord-notes-1", Alert: &factory.ProjectAlert{Kind: flow.AlertAgentsDown, Cause: "signed out", Fix: "sign in"}},
		Tasks:       []factory.TaskView{{Ref: "T1", Title: "tags", Stage: "working", RunRole: flow.RoleWorker, RunCLI: "codex", RunStartedAt: t0, LastActorDecision: &factory.LogEntry{At: t0, By: "coordinator", Decision: "sent back", Why: "case-insensitive"}, Steps: []factory.StepView{{Name: "planned", State: "done"}, {Name: "implementing", State: "now"}}}},
		Asks:        []flow.Ask{{ID: "ask-1", Project: "proj-1a2b3c4d", Task: "T1", Kind: flow.KindDelete, Gated: true, Question: "Delete old notes table?", Options: []string{"Delete", "Keep"}, Recommend: "Keep", At: t0, Status: flow.AskStatusOpen}},
	})
	h := newHarness(t, harnessOpts{factory: fk})
	logTo, h.srv.deps.Factory = h.log, fk
	startSupervisorRun(t, h)
	var st, card map[string]any
	waitFor(t, time.Second, func() bool {
		st = getState(t, h)
		for _, raw := range st["questions"].([]any) {
			if m := raw.(map[string]any); m["id"] == "fask-ask-1" {
				card = m
				return true
			}
		}
		return false
	}, "fask-ask-1 card")
	list, _ := st["factory"].([]any)
	if len(list) != 1 {
		t.Fatalf("/api/state factory = %#v", st["factory"])
	}
	e0, _ := list[0].(map[string]any)
	ag, _ := card["agent"].(string)
	fm, _ := card["factory"].(map[string]any)
	if k := slices.Sorted(maps.Keys(e0)); !slices.Equal(k, []string{"alert", "coordinator_pane", "counts", "folder", "goal", "id", "name", "state"}) || e0["name"] != "notes" || e0["coordinator_pane"] != "coord-notes-1" {
		t.Errorf("factory[0] = %v (keys %v)", e0, k)
	}
	if !strings.Contains(strings.ToLower(ag), "notes") || strings.Contains(ag, "proj-1a2b3c4d") || fm["project"] != "proj-1a2b3c4d" || fm["task"] != "T1" || fm["kind"] != flow.KindDelete {
		t.Errorf("fask card agent=%q factory=%v", ag, fm)
	}
	var graph factory.GraphView
	if code := h.getJSON("/api/factory/projects/proj-1a2b3c4d/graph", &graph); code != http.StatusOK || len(graph.Tasks) != 1 || len(graph.Tasks[0].Steps) != 2 || graph.Tasks[0].RunCLI != "codex" || graph.Tasks[0].LastActorDecision == nil {
		t.Fatalf("GET graph = (%d, %+v)", code, graph)
	}
	if err := fk.Say("proj-1a2b3c4d", "skip export"); err != nil {
		t.Fatalf("Say = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.http.URL+"/events?n=10&kinds=factory", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	for sc := bufio.NewScanner(resp.Body); sc.Scan(); {
		if l := sc.Text(); strings.Contains(l, `"factory.changed"`) && strings.Contains(l, `"proj-1a2b3c4d"`) {
			return
		}
	}
	t.Fatal("/events?kinds=factory missing factory.changed")
}
