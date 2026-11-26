package web

import (
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

func getRawJSONMap(t *testing.T, h *harness, path string) (m map[string]any) {
	t.Helper()
	if code := h.getJSON(path, &m); code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, code)
	}
	return m
}

// Pins app-factory.md §4: GET /api/state omits "factory" when empty (matching
// captured rest/state.json), and carries exact pinned keys {id, name, goal,
// state, counts, folder, coordinator_pane?, alert?} with alert precedence.
func TestStateFactoryListPinnedKeysAlertPrecedenceAndOmittedWhenEmpty(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(goldenDir, "rest", "state.json"))
	var golden map[string]any
	if err != nil || json.Unmarshal(b, &golden) != nil || golden["factory"] != nil {
		t.Fatalf("captured rest/state.json err=%v factory=%v", err, golden["factory"])
	}
	eEmpty := factory.New(factory.Paths{DataDir: t.TempDir()}, nil)
	h0 := newHarness(t, harnessOpts{factory: eEmpty})
	h0.srv.deps.Factory = eEmpty
	if got := getRawJSONMap(t, h0, "/api/state"); got["factory"] != nil {
		t.Fatalf("0 projects carried factory = %v, want omitted", got["factory"])
	}
	t0, paths := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC), factory.Paths{DataDir: t.TempDir()}
	for _, st := range []flow.State{
		{Seq: 2, Settings: flow.DefaultSettings(), Project: flow.Project{
			ID: "proj-1a2b3c4d", Folder: "/work/notes", Goal: "Notes with search", Started: true,
			RunID: "coord-notes-1", Run: &flow.RunRecord{Role: flow.RoleCoordinator, State: flow.PhaseWorking},
			Alerts: map[string]flow.Alert{
				"ci_stalled@T1":  {Kind: flow.AlertCIStalled, Cause: "CI pending", At: t0},
				"agents_down@j6": {Kind: flow.AlertAgentsDown, Cause: "claude signed out", Fix: "sign in", At: t0},
				"disk":           {Kind: flow.AlertDisk, Cause: "disk full", Fix: "free space", At: t0},
			}}, Tasks: []flow.Task{{ID: "T1", Title: "done", Done: &flow.Merged{MergedCommit: "a"}}, {ID: "T2", Title: "q"}}},
		{Seq: 1, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "proj-2b3c4d5e", Folder: "/work/archive", Goal: "Done", Finished: true}},
	} {
		if err := factory.SaveState(paths.State(st.Project.ID), st); err != nil {
			t.Fatal(err)
		}
	}
	eng := factory.New(paths, nil)
	h := newHarness(t, harnessOpts{factory: eng})
	h.srv.deps.Factory = eng
	list, ok := getRawJSONMap(t, h, "/api/state")["factory"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("GET /api/state factory = %#v, want 2 entries", list)
	}
	e0, e1 := list[0].(map[string]any), list[1].(map[string]any)
	if k := slices.Sorted(maps.Keys(e0)); !slices.Equal(k, []string{"alert", "coordinator_pane", "counts", "folder", "goal", "id", "name", "state"}) {
		t.Errorf("factory[0] keys = %v", k)
	}
	if e0["name"] != "notes" || e0["goal"] != "Notes with search" || e0["state"] != "running" || e0["coordinator_pane"] != "coord-notes-1" {
		t.Errorf("factory[0] = %v", e0)
	}
	if al, _ := e0["alert"].(map[string]any); al["kind"] != flow.AlertDisk || al["cause"] != "disk full" || al["fix"] != "free space" || len(al) != 3 {
		t.Errorf("factory[0].alert = %v", e0["alert"])
	}
	if k := slices.Sorted(maps.Keys(e1)); !slices.Equal(k, []string{"counts", "folder", "goal", "id", "name", "state"}) {
		t.Errorf("factory[1] keys = %v", k)
	}
}

// Pins app-factory.md §3.2 & §4: fask- PendingView carries project name in
// agent and factory {project, task, kind}; ordinary cards omit factory;
// /events?kinds=factory streams factory.changed.
func TestStateFactoryAskCardMetaProjectNameAndEventsKind(t *testing.T) {
	t0, paths := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC), factory.Paths{DataDir: t.TempDir()}
	if err := factory.SaveState(paths.State("proj-1a2b3c4d"), flow.State{
		Seq: 2, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "proj-1a2b3c4d", Folder: "/work/notes", Goal: "Notes", Started: true},
		Asks: []flow.Ask{
			{ID: "ask-gate-1", Project: "proj-1a2b3c4d", Kind: flow.KindGate, Gated: true, Question: "Merge T1?", Options: []string{"Merge", "Hold"}, Recommend: "Merge", Task: "T1", At: t0, Status: flow.AskStatusOpen},
			{ID: "ask-rule-1", Project: "proj-1a2b3c4d", Kind: flow.KindRule, Gated: true, Question: "Save max_running 3?", Options: []string{"Save", "Keep current"}, Recommend: "Save", At: t0.Add(time.Second), Status: flow.AskStatusOpen},
		}}); err != nil {
		t.Fatal(err)
	}
	eng := factory.New(paths, nil)
	h := newHarness(t, harnessOpts{factory: eng})
	h.srv.deps.Factory = eng
	startSupervisorRun(t, h)
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "kind": "codex", "screen": codexScreen(t)}, nil); code != http.StatusOK {
		t.Fatalf("inject blocked = %d %s", code, body)
	}
	var byID map[string]map[string]any
	waitFor(t, time.Second, func() bool {
		byID = map[string]map[string]any{}
		for _, q := range getRawJSONMap(t, h, "/api/state")["questions"].([]any) {
			m := q.(map[string]any)
			byID[m["id"].(string)] = m
		}
		return byID["fask-ask-gate-1"] != nil && byID["fask-ask-rule-1"] != nil && len(byID) >= 3
	}, "factory asks and ordinary question")
	if ag, _ := byID["fask-ask-gate-1"]["agent"].(string); !strings.Contains(strings.ToLower(ag), "notes") || strings.Contains(ag, "proj-1a2b3c4d") {
		t.Errorf("gate card agent = %q, want notes", ag)
	}
	if fm, _ := byID["fask-ask-gate-1"]["factory"].(map[string]any); fm["project"] != "proj-1a2b3c4d" || fm["task"] != "T1" || fm["kind"] != flow.KindGate {
		t.Errorf("gate card factory = %v", byID["fask-ask-gate-1"]["factory"])
	}
	if fm, _ := byID["fask-ask-rule-1"]["factory"].(map[string]any); fm["project"] != "proj-1a2b3c4d" || fm["kind"] != flow.KindRule {
		t.Errorf("rule card factory = %v", byID["fask-ask-rule-1"]["factory"])
	}
	for id, q := range byID {
		if !strings.HasPrefix(id, "fask-") && q["factory"] != nil {
			t.Errorf("ordinary card %s leaked factory = %v", id, q["factory"])
		}
	}
	h.log.Log("factory", "factory.changed", "", map[string]any{"project": "proj-1a2b3c4d"})
	if resp, err := http.Get(h.http.URL + "/events?n=4&kinds=factory"); err != nil {
		t.Fatal(err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /events?kinds=factory status = %d, want 200", resp.StatusCode)
		}
	}
}
