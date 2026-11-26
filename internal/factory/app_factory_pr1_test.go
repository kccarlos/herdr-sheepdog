package factory

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func toJSONMap(t *testing.T, v any) (m map[string]any) {
	t.Helper()
	if b, err := json.Marshal(v); err != nil || json.Unmarshal(b, &m) != nil {
		t.Fatalf("json roundtrip: %v", err)
	}
	return m
}

// Pins app-factory.md §3.2 (D2) & §4: ProjectView name (last folder segment,
// fallback to id), coordinator_pane (omitted unless live), and alert
// ({kind, cause, fix}) with precedence StoppingAlert > agents_down > first key.
func TestProjectViewNameCoordinatorPaneAndAlertPrecedence(t *testing.T) {
	t0, def := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC), flow.DefaultSettings()
	views := ProjectsView([]flow.State{
		{Settings: def, Project: flow.Project{ID: "proj-1a2b3c4d", Folder: "/Users/me/code/notes", Goal: "Notes", Started: true,
			RunID: "coord-notes-1", Run: &flow.RunRecord{Role: flow.RoleCoordinator, State: flow.PhaseWorking},
			Alerts: map[string]flow.Alert{
				"ci_stalled@T1":  {Kind: flow.AlertCIStalled, Cause: "CI pending", At: t0},
				"agents_down@j6": {Kind: flow.AlertAgentsDown, Cause: "claude signed out", Fix: "sign in", At: t0},
				"disk":           {Kind: flow.AlertDisk, Cause: "volume full", Fix: "free space", At: t0, Rule: "J3", Scope: "/"},
			}}},
		{Settings: def, Project: flow.Project{ID: "proj-2b3c4d5e", Folder: "/Users/me/code/recipes/", Goal: "Recipes", Started: true,
			RunID: "coord-ended", Run: &flow.RunRecord{Role: flow.RoleCoordinator, State: flow.PhaseEnded},
			Alerts: map[string]flow.Alert{
				"aaa_ci":         {Kind: flow.AlertCIStalled, Cause: "CI pending", At: t0},
				"agents_down@j6": {Kind: flow.AlertAgentsDown, Cause: "claude signed out", Fix: "sign in to claude", At: t0},
			}}},
		{Settings: def, Project: flow.Project{ID: "proj-3c4d5e6f", Goal: "No folder", Alerts: map[string]flow.Alert{
			"main_red":      {Kind: flow.AlertMainRed, Cause: "main red", At: t0},
			"ci_stalled@T1": {Kind: flow.AlertCIStalled, Cause: "T1 pending", Fix: "check CI", At: t0},
		}}},
		{Settings: def, Project: flow.Project{ID: "proj-4d5e6f70", Folder: "/Users/me/code/clean"}},
	})
	v0, v1, v2, v3 := toJSONMap(t, views[0]), toJSONMap(t, views[1]), toJSONMap(t, views[2]), toJSONMap(t, views[3])
	if v0["name"] != "notes" || v0["coordinator_pane"] != "coord-notes-1" {
		t.Errorf("v0 name/coord = (%v, %v), want (notes, coord-notes-1)", v0["name"], v0["coordinator_pane"])
	}
	if a, _ := v0["alert"].(map[string]any); a["kind"] != flow.AlertDisk || a["cause"] != "volume full" || a["fix"] != "free space" || len(a) != 3 {
		t.Errorf("v0 alert = %v, want {kind:disk, cause:volume full, fix:free space}", v0["alert"])
	}
	if v1["name"] != "recipes" || v1["coordinator_pane"] != nil {
		t.Errorf("v1 name/coord = (%v, %v), want (recipes, nil)", v1["name"], v1["coordinator_pane"])
	}
	if a, _ := v1["alert"].(map[string]any); a["kind"] != flow.AlertAgentsDown || a["fix"] != "sign in to claude" {
		t.Errorf("v1 alert = %v, want agents_down@j6 over aaa_ci", v1["alert"])
	}
	if v2["name"] != "proj-3c4d5e6f" || toJSONMap(t, v2["alert"])["kind"] != flow.AlertCIStalled || v3["alert"] != nil {
		t.Errorf("v2/v3 = (%v, %v, %v)", v2["name"], v2["alert"], v3["alert"])
	}
}

// Pins app-factory.md §4 & §5.3: TaskView run_role, run_cli, run_started_at
// (omitted unless live) and last_actor_decision {by, decision, why, at}
// (last coordinator/owner log line for the task; last_rule/last_decision kept).
func TestTaskViewRunMetadataAndLastActorDecision(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)
	tW, tR, tC, tO := t0.Add(time.Minute), t0.Add(2*time.Minute), t0.Add(3*time.Minute), t0.Add(5*time.Minute)
	st := flow.State{Seq: 7, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "proj-notes", Folder: "/work/notes", Started: true}, Tasks: []flow.Task{
		{ID: "T1", Title: "tags", Attempts: []flow.Attempt{{BaseCommit: "0", RunID: "w-1",
			Run: &flow.RunRecord{Role: flow.RoleWorker, CLI: "codex", StartedAt: tW, State: flow.PhaseWorking}}}},
		{ID: "T2", Title: "search", Attempts: []flow.Attempt{{BaseCommit: "0", RunID: "w-2", Ready: []flow.Ready{{Head: "a"}},
			Review: &flow.ReviewRun{RunID: "rev-1", Round: 1, Kind: flow.RoundFull, Head: "a", Reviewer: flow.Reviewer{CLI: "claude", Model: "sonnet"},
				Run: &flow.RunRecord{Role: flow.RoleReviewer, CLI: "claude", StartedAt: tR, State: flow.PhaseWorking}}}}},
		{ID: "T3", Title: "queued"},
	}}
	logs := []LogLine{
		{Seq: 1, At: t0, Project: "proj-notes", By: "engine", Task: "T1", Rule: "N23", Decision: "started worker", Why: "working"},
		{Seq: 2, At: tC, Project: "proj-notes", By: "coordinator", Task: "T1", Rule: "task send-back", Decision: "sent back", Why: "case-insensitive index"},
		{Seq: 3, At: tC.Add(time.Second), Project: "proj-notes", By: "engine", Task: "T1", Rule: "N29", Decision: "started reviewer", Why: "ladder"},
		{Seq: 4, At: tC.Add(2 * time.Second), Project: "proj-notes", By: "worker", Task: "T1", Rule: "ready", Decision: "ready", Why: "deadbeef"},
		{Seq: 5, At: tC.Add(3 * time.Second), Project: "proj-notes", By: "coordinator", Task: "", Rule: "plan amend", Decision: "plan amended", Why: "project-level"},
		{Seq: 6, At: tC, Project: "proj-notes", By: "coordinator", Task: "T2", Rule: "recap decide", Decision: "continue", Why: "close"},
		{Seq: 7, At: tO, Project: "proj-notes", By: "owner", Task: "T2", Rule: "A3", Decision: "answered ask-1", Why: "Merge"},
	}
	g := GraphViewOf(st, logs)
	m1, m2, m3 := toJSONMap(t, g.Tasks[0]), toJSONMap(t, g.Tasks[1]), toJSONMap(t, g.Tasks[2])
	if m1["run_role"] != flow.RoleWorker || m1["run_cli"] != "codex" || m1["run_started_at"] != tW.Format(time.RFC3339) || m1["last_rule"] != "N29" || m1["last_decision"] != "started reviewer" {
		t.Errorf("T1 = (role=%v cli=%v started=%v rule=%v dec=%v)", m1["run_role"], m1["run_cli"], m1["run_started_at"], m1["last_rule"], m1["last_decision"])
	}
	if d, _ := m1["last_actor_decision"].(map[string]any); d["by"] != "coordinator" || d["decision"] != "sent back" || d["why"] != "case-insensitive index" || d["at"] != tC.Format(time.RFC3339) {
		t.Errorf("T1 last_actor_decision = %v", m1["last_actor_decision"])
	}
	if m2["run_role"] != flow.RoleReviewer || m2["run_cli"] != "claude" || m2["run_started_at"] != tR.Format(time.RFC3339) {
		t.Errorf("T2 run = (%v, %v, %v)", m2["run_role"], m2["run_cli"], m2["run_started_at"])
	}
	if d, _ := m2["last_actor_decision"].(map[string]any); d["by"] != "owner" || d["at"] != tO.Format(time.RFC3339) {
		t.Errorf("T2 last_actor_decision = %v, want owner", m2["last_actor_decision"])
	}
	for _, k := range []string{"run_role", "run_cli", "run_started_at", "last_actor_decision"} {
		if _, has := m3[k]; has {
			t.Errorf("T3 leaked %q = %v", k, m3[k])
		}
	}
}

type eventRec struct {
	mu      sync.Mutex
	changed []string
}

func (r *eventRec) Log(_, typ, _ string, pl any) {
	if typ != "factory.changed" {
		return
	}
	b, _ := json.Marshal(pl)
	var m map[string]string
	_ = json.Unmarshal(b, &m)
	r.mu.Lock()
	r.changed = append(r.changed, m["project"])
	r.mu.Unlock()
}

func (r *eventRec) projects() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.changed)
}

// Pins app-factory.md §4 (review update): factory.changed {project} fires via
// Engine.Log on a project log line or a changed head entry with no 2s rate
// limit, does not fire on a state save that changes no head entry, and is never
// sent on Engine.Changes().
func TestFactoryChangedEventOnLogOrHeadChangeAndQuietSave(t *testing.T) {
	paths, srv := testPaths(t), hctest.New(t)
	srv.OnExec = hctest.GitExec
	now, rec := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC), &eventRec{}
	eng := newTestEngine(t, paths, hc.NewClient(srv.Path))
	eng.Log, eng.Now = rec, func() time.Time { return now }
	ch := eng.Changes()
	repo := filepath.Join(t.TempDir(), "notes")
	if b, err := exec.Command("git", "init", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, b)
	}
	if err := eng.Start(repo, "goal 1", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.Projects()[0].ID
	if err := eng.Stop(id); err != nil { // immediate back-to-back: no 2s rate limit
		t.Fatal(err)
	}
	if got := rec.projects(); !slices.Equal(got, []string{id, id}) {
		t.Fatalf("after Start+Stop: factory.changed = %v, want [%s %s]", got, id, id)
	}
	eng.mu.Lock()
	st, err := LoadState(paths.State(id))
	if err == nil {
		st.Project.WaitKey = "internal-only" // non-head save must not fire
		eng.saveLocked(id, st)
	}
	eng.mu.Unlock()
	if err != nil || len(rec.projects()) != 2 {
		t.Fatalf("non-head save: err=%v factory.changed=%v, want 2", err, rec.projects())
	}
	eng.mu.Lock()
	st.Project.RunID, st.Project.Run = "coord-notes-1", &flow.RunRecord{Role: flow.RoleCoordinator, State: flow.PhaseWorking}
	eng.saveLocked(id, st)                                                                                // head change fires
	_ = eng.commitLocked(id, &st, LogLine{By: "coordinator", Rule: "note", Decision: "noted", Why: "ok"}) // log line fires
	eng.mu.Unlock()
	if got := rec.projects(); !slices.Equal(got, []string{id, id, id, id}) {
		t.Fatalf("after head save + log line: factory.changed = %v, want 4", got)
	}
	select {
	case c := <-ch:
		t.Errorf("factory.changed sent on Engine.Changes(): %+v", c)
	default:
	}
}
