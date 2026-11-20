package factory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

func fixtureViewState() (flow.State, []LogLine) {
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	st := flow.State{
		Seq:      6,
		Settings: flow.DefaultSettings(),
		Project: flow.Project{
			ID:      "proj-demo",
			Folder:  "/tmp/demo",
			Goal:    "ship view api",
			Started: true,
		},
		Tasks: []flow.Task{
			{
				ID:    "T1",
				Title: "feat: done task",
				Done:  &flow.Merged{MergedCommit: "aaa111"},
				Attempts: []flow.Attempt{{
					BaseCommit: "000",
					Rounds: []flow.Round{
						{N: 1, Kind: flow.RoundFull, Head: "aaa111", Verdict: flow.VerdictPass},
						{N: 2, Kind: flow.RoundFull, Head: "aaa111", Verdict: flow.VerdictPass},
					},
				}},
			},
			{
				ID:    "T2",
				Title: "feat: in review",
				After: []string{"T1"},
				Attempts: []flow.Attempt{{
					BaseCommit: "000",
					RunID:      "worker-demo-t2-1",
					Ready:      []flow.Ready{{Head: "bbb222"}},
					Rounds: []flow.Round{
						{N: 1, Kind: flow.RoundFull, Head: "bbb222", Verdict: flow.VerdictPass, Reviewer: flow.Reviewer{CLI: "claude", Model: "opus"}},
					},
					Review: &flow.ReviewRun{
						RunID:    "rev-demo-t2-1-2",
						Round:    2,
						Kind:     flow.RoundFull,
						Head:     "bbb222",
						Reviewer: flow.Reviewer{CLI: "claude", Model: "sonnet"},
					},
				}},
			},
			{
				ID:    "T3",
				Title: "feat: queued task",
				After: []string{"T1"},
			},
			{
				ID:    "T4",
				Title: "feat: at recap",
				After: []string{"T2"},
				Recaps: []flow.Recap{{
					Trigger: flow.TriggerReview,
					Allowed: []string{"split", "new", "drop"},
				}},
				Attempts: []flow.Attempt{{
					BaseCommit: "000",
					RunID:      "worker-demo-t4-1",
					Ready:      []flow.Ready{{Head: "ccc333"}},
					Rounds: []flow.Round{
						{N: 1, Kind: flow.RoundFull, Head: "ccc333", Verdict: flow.VerdictChanges, Majors: 2},
						{N: 2, Kind: flow.RoundSendBack, Head: "ccc333", Verdict: flow.VerdictChanges, Why: "missing test"},
						{N: 3, Kind: flow.RoundDelta, Head: "ccc333", Verdict: flow.VerdictChanges, Majors: 1},
					},
				}},
			},
			{
				ID:    "T5",
				Title: "feat: waiting on dep",
				After: []string{"T2"},
			},
		},
	}
	logs := []LogLine{
		{Seq: 1, At: t0, Project: "proj-demo", By: "engine", Task: "T1", Rule: "N1", Decision: "cleaned up", Why: "ended: stop its runs and clean up"},
		{Seq: 2, At: t0.Add(time.Minute), Project: "proj-demo", By: "engine", Task: "T2", Rule: "N23", Decision: "started worker", Why: "the worker is working"},
		{Seq: 3, At: t0.Add(2 * time.Minute), Project: "proj-demo", By: "engine", Task: "T2", Rule: "N29", Decision: "started reviewer rev-demo-t2-1-2", Why: "the ladder says review"},
		{Seq: 4, At: t0.Add(3 * time.Minute), Project: "proj-demo", By: "engine", Task: "T4", Rule: "N6", Decision: "queued wake", Why: "a recap is open"},
	}
	return st, logs
}

func TestPureViewsOverFixtureState(t *testing.T) {
	st, logs := fixtureViewState()

	projs := ProjectsView([]flow.State{st})
	if len(projs) != 1 {
		t.Fatalf("ProjectsView len = %d, want 1", len(projs))
	}
	p := projs[0]
	if p.ID != "proj-demo" || p.Folder != "/tmp/demo" || p.Goal != "ship view api" || !p.Running {
		t.Fatalf("unexpected project view: %+v", p)
	}
	wantCounts := map[string]int{"done": 1, "working": 1, "queued": 1, "blocked_or_failed": 1, "waiting_on_dep": 1}
	for k, want := range wantCounts {
		if p.Counts[k] != want {
			t.Errorf("Counts[%q] = %d, want %d (all=%v)", k, p.Counts[k], want, p.Counts)
		}
	}

	g := GraphViewOf(st, logs)
	if g.Protocol != 1 || g.Project != "proj-demo" || len(g.Tasks) != 5 {
		t.Fatalf("unexpected graph header/tasks: protocol=%d project=%s tasks=%d", g.Protocol, g.Project, len(g.Tasks))
	}
	byRef := map[string]TaskView{}
	for _, tv := range g.Tasks {
		byRef[tv.Ref] = tv
	}
	if tv := byRef["T1"]; tv.Layer != 0 || tv.Stage != "done" || tv.WhyWaiting != "" || tv.Rounds != 2 ||
		tv.LastRule != "N1" || tv.LastDecision != "cleaned up" || tv.Terminal != "" ||
		!slices.Equal(tv.Dependents, []string{"T2", "T3"}) {
		t.Errorf("T1 = %+v", tv)
	}
	if tv := byRef["T2"]; tv.Layer != 1 || tv.Stage != "working" || tv.WhyWaiting != "Waiting on the reviewer" ||
		tv.Rounds != 1 || tv.LastRule != "N29" || tv.LastDecision != "started reviewer rev-demo-t2-1-2" ||
		tv.Model != "sonnet" || tv.Terminal != "rev-demo-t2-1-2" || !slices.Equal(tv.Dependents, []string{"T4", "T5"}) {
		t.Errorf("T2 = %+v", tv)
	}
	if tv := byRef["T3"]; tv.Layer != 1 || tv.Stage != "queued" || tv.WhyWaiting != "Queued to start" || tv.Rounds != 0 || tv.Terminal != "" {
		t.Errorf("T3 = %+v", tv)
	}
	if tv := byRef["T4"]; tv.Layer != 2 || tv.Stage != "blocked_or_failed" || tv.WhyWaiting != "Waiting on the coordinator" ||
		tv.Rounds != 2 || tv.LastRule != "N6" || tv.LastDecision != "queued wake" || tv.Terminal != "worker-demo-t4-1" {
		t.Errorf("T4 = %+v", tv)
	}
	if tv := byRef["T5"]; tv.Layer != 2 || tv.Stage != "waiting_on_dep" || tv.WhyWaiting != "Waiting on T2" {
		t.Errorf("T5 = %+v", tv)
	}

	// Verify JSON uses contract shape: flat tasks[], ref (string), no string id field.
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, hasLayers := doc["layers"]; hasLayers {
		t.Fatalf("graph JSON should use flat tasks[], not layers: %s", raw)
	}
	firstTask := doc["tasks"].([]any)[0].(map[string]any)
	if _, hasID := firstTask["id"]; hasID {
		t.Fatalf("task JSON must use ref (not string id): %s", raw)
	}
	if firstTask["ref"] != "T1" {
		t.Fatalf("first task ref = %v, want T1", firstTask["ref"])
	}
}

func TestStageMappingAndWhyWaitingCases(t *testing.T) {
	settings := flow.DefaultSettings()
	st := flow.State{
		Settings: settings,
		Project:  flow.Project{ID: "p", Started: true},
		Tasks: []flow.Task{
			{ID: "done", Done: &flow.Merged{MergedCommit: "a"}},
			{ID: "closed", Closed: &flow.Closed{Why: "no-op"}},
			{ID: "dropped", Dropped: &flow.Dropped{Why: "dup"}},
			{ID: "split", Split: &flow.Split{Children: []string{"c1"}}},
			{ID: "parked", Parked: true},
			{ID: "ntc", Attempts: []flow.Attempt{{BaseCommit: "0", NothingToChange: "already fixed"}}},
			{ID: "working", Attempts: []flow.Attempt{{BaseCommit: "0", RunID: "w-1"}}},
			{ID: "fix", Attempts: []flow.Attempt{{
				BaseCommit: "0", Ready: []flow.Ready{{Head: "h1"}},
				Rounds: []flow.Round{{N: 1, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictChanges, Majors: 1}},
			}}},
			{ID: "ci", Attempts: []flow.Attempt{{
				BaseCommit: "0", Ready: []flow.Ready{{Head: "h1"}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
				},
			}}},
			// Cycle in After must not recurse infinitely.
			{ID: "cycA", After: []string{"cycB"}},
			{ID: "cycB", After: []string{"cycA"}},
		},
	}
	// Trailing ready (SHA in Why) and pr_snapshot log lines must not pollute why_waiting.
	logs := []LogLine{
		{Seq: 1, By: "worker", Task: "ci", Rule: "ready", Decision: "accepted", Why: "deadbeef1234567890"},
		{Seq: 2, By: "engine", Task: "ci", Rule: "pr_snapshot", Decision: "", Why: "the PR snapshot changed"},
		{Seq: 3, By: "engine", Task: "parked", Rule: "N3", Decision: "stopped runs", Why: "parked"},
	}
	g := GraphViewOf(st, logs)
	byRef := map[string]TaskView{}
	for _, tv := range g.Tasks {
		byRef[tv.Ref] = tv
	}
	want := map[string]struct{ stage, why string }{
		"done":    {"done", ""},
		"closed":  {"done", ""},
		"dropped": {"done", ""},
		"split":   {"done", ""},
		"parked":  {"blocked_or_failed", "Waiting on the coordinator"},
		"ntc":     {"blocked_or_failed", "Worker reported nothing to change, waiting on the coordinator"},
		"working": {"working", "Worker is working"},
		"fix":     {"working", "Waiting on the worker"},
		"ci":      {"ci", "Waiting on the engine"},
		"cycA":    {"waiting_on_dep", "Waiting on cycB"},
		"cycB":    {"waiting_on_dep", "Waiting on cycA"},
	}
	for ref, w := range want {
		tv := byRef[ref]
		if tv.Stage != w.stage || tv.WhyWaiting != w.why {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", ref, tv.Stage, tv.WhyWaiting, w.stage, w.why)
		}
	}
	if byRef["ci"].LastRule != "pr_snapshot" || byRef["parked"].LastRule != "N3" || byRef["parked"].LastDecision != "stopped runs" {
		t.Errorf("last_rule/last_decision unexpected: ci=%+v parked=%+v", byRef["ci"], byRef["parked"])
	}
}

func TestRoundsUsesFlowCounted(t *testing.T) {
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "p", Started: true},
		Tasks: []flow.Task{{
			ID: "T1",
			Attempts: []flow.Attempt{{
				BaseCommit: "000",
				Boundary:   2,
				Ready:      []flow.Ready{{Head: "h2"}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictChanges},
					{N: 2, Kind: flow.RoundDelta, Head: "h1", Verdict: flow.VerdictPass},
					{N: 3, Kind: flow.RoundSendBack, Head: "h2", Verdict: flow.VerdictChanges},
					{N: 4, Kind: flow.RoundFull, Head: "h2", Verdict: flow.VerdictPass},
				},
			}},
		}},
	}
	g := GraphViewOf(st, nil)
	if g.Tasks[0].Rounds != 1 {
		t.Fatalf("Rounds = %d, want 1 (only non-send_back rounds after Boundary=2)", g.Tasks[0].Rounds)
	}
}

func TestLogViewProjectionAndLimits(t *testing.T) {
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	var lines []LogLine
	for i := 1; i <= 600; i++ {
		lines = append(lines, LogLine{
			Seq: int64(i), At: t0.Add(time.Duration(i) * time.Second), Project: "p",
			By: "engine", Task: "T1", Input: map[string]string{"secret": "no"},
			Facts: map[string]any{"pr": i}, Rule: "N23", Asked: []string{"start"},
			Decision: fmt.Sprintf("d%d", i), Why: fmt.Sprintf("w%d", i),
		})
	}
	if def := LogView(lines, 0); len(def) != DefaultLogLimit || def[0].Decision != "d551" {
		t.Fatalf("LogView(0) len=%d first=%+v, want %d d551", len(def), def[0], DefaultLogLimit)
	}
	if capped := LogView(lines, 999); len(capped) != MaxLogLimit || capped[0].Decision != "d101" {
		t.Fatalf("LogView(999) len=%d first=%+v, want %d d101", len(capped), capped[0], MaxLogLimit)
	}
	raw, err := json.Marshal(LogView(lines, 1)[0])
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"input", "facts", "seq", "project", "rule", "asked"} {
		if _, ok := obj[forbidden]; ok {
			t.Errorf("LogEntry JSON leaked forbidden field %q: %s", forbidden, raw)
		}
	}
}

func TestEngineViewsLockFreeCorruptStateAndBadLogLine(t *testing.T) {
	paths := Paths{DataDir: t.TempDir()}
	st, logs := fixtureViewState()
	if err := SaveState(paths.State(st.Project.ID), st); err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if err := AppendLog(paths.Log(st.Project.ID), l); err != nil {
			t.Fatal(err)
		}
	}
	// Append a torn/unparseable JSONL line to verify it is skipped.
	f, err := os.OpenFile(paths.Log(st.Project.ID), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{torn json line\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Create a second project whose state.json cannot be parsed.
	badState := paths.State("proj-broken")
	if err := os.MkdirAll(filepath.Dir(badState), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badState, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	eng := New(paths, nil)
	// Hold the engine lock (simulating a pass in flight) and verify reads still return.
	eng.mu.Lock()
	defer eng.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		projs := eng.Projects()
		if len(projs) != 2 || projs[0].ID != "proj-broken" || projs[0].Error == "" || projs[1].ID != "proj-demo" || projs[1].Error != "" {
			t.Errorf("eng.Projects() = %+v, want broken(error) + demo", projs)
		}
		g, err := eng.Graph("proj-demo")
		if err != nil || len(g.Tasks) != 5 {
			t.Errorf("eng.Graph = %+v, err=%v", g, err)
		}
		entries, err := eng.LogLines("proj-demo", 2)
		if err != nil || len(entries) != 2 || entries[1].Decision != "queued wake" {
			t.Errorf("eng.LogLines = %+v, err=%v", entries, err)
		}
		for _, bad := range []string{"missing", "../proj-demo", "a/b", ""} {
			if _, err := eng.Graph(bad); !errors.Is(err, ErrProjectNotFound) {
				t.Errorf("Graph(%q) err = %v, want ErrProjectNotFound", bad, err)
			}
			if _, err := eng.LogLines(bad, 10); !errors.Is(err, ErrProjectNotFound) {
				t.Errorf("LogLines(%q) err = %v, want ErrProjectNotFound", bad, err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("view reads blocked while Engine.mu was held")
	}
}
