package factory

import (
	"errors"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

func fixtureViewState() (flow.State, []LogLine) {
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	st := flow.State{
		Seq:      5,
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
				Title: "feat: planned task",
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
		{Seq: 3, At: t0.Add(2 * time.Minute), Project: "proj-demo", By: "engine", Task: "T2", Rule: "N29", Decision: "started reviewer rev-demo-t2-1-2", Why: "the ladder says review", Model: "sonnet"},
		{Seq: 4, At: t0.Add(3 * time.Minute), Project: "proj-demo", By: "engine", Wake: "recap", Task: "T4", Rule: "N6", Decision: "queued wake", Why: "a recap is open", Model: "opus"},
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
	wantCounts := map[string]int{"done": 1, "review": 1, "planned": 1, "recap": 1, "waiting_on_dep": 1}
	for k, want := range wantCounts {
		if p.Counts[k] != want {
			t.Errorf("Counts[%q] = %d, want %d (all=%v)", k, p.Counts[k], want, p.Counts)
		}
	}

	g := GraphViewOf(st, logs)
	if g.Protocol != 1 || g.Project != "proj-demo" || len(g.Layers) != 3 {
		t.Fatalf("unexpected graph header/layers: protocol=%d project=%s layers=%d", g.Protocol, g.Project, len(g.Layers))
	}
	byID := map[string]TaskView{}
	for _, layer := range g.Layers {
		for _, tv := range layer {
			byID[tv.ID] = tv
		}
	}
	if tv := byID["T1"]; tv.Layer != 0 || tv.Stage != "done" || tv.Rounds != 2 || tv.Rule != "N1" || tv.Terminal != "" {
		t.Errorf("T1 = %+v", tv)
	}
	if tv := byID["T2"]; tv.Layer != 1 || tv.Stage != "review" || tv.Rounds != 1 || tv.Rule != "N29" ||
		tv.Decision != "started reviewer rev-demo-t2-1-2" || tv.WhyWaiting != "the ladder says review" ||
		tv.Model != "sonnet" || tv.Terminal != "rev-demo-t2-1-2" {
		t.Errorf("T2 = %+v", tv)
	}
	if tv := byID["T3"]; tv.Layer != 1 || tv.Stage != "planned" || tv.Rounds != 0 || tv.Terminal != "" {
		t.Errorf("T3 = %+v", tv)
	}
	if tv := byID["T4"]; tv.Layer != 2 || tv.Stage != "recap" || tv.Rounds != 2 || tv.Rule != "N6" ||
		tv.Decision != "queued wake" || tv.WhyWaiting != "a recap is open" || tv.Terminal != "worker-demo-t4-1" {
		t.Errorf("T4 = %+v", tv)
	}
	if tv := byID["T5"]; tv.Layer != 2 || tv.Stage != "waiting_on_dep" {
		t.Errorf("T5 = %+v", tv)
	}

	tail := LogView(logs, 2)
	if len(tail) != 2 || tail[0].Seq != 3 || tail[1].Seq != 4 || tail[1].Wake != "recap" || tail[1].Model != "opus" {
		t.Fatalf("LogView(2) = %+v", tail)
	}
	if all := LogView(logs, 0); len(all) != 4 {
		t.Fatalf("LogView(0) len = %d, want 4", len(all))
	}
}

func TestEngineViewMethodsReadStoredStateOnly(t *testing.T) {
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
	// Nil HC ensures no hc/git/gh calls can happen.
	eng := New(paths, nil)

	projs := eng.Projects()
	if len(projs) != 1 || projs[0].ID != "proj-demo" {
		t.Fatalf("eng.Projects() = %+v", projs)
	}
	g, err := eng.Graph("proj-demo")
	if err != nil || len(g.Layers) != 3 {
		t.Fatalf("eng.Graph = %+v, err=%v", g, err)
	}
	lines, err := eng.LogLines("proj-demo", 2)
	if err != nil || len(lines) != 2 || lines[1].Wake != "recap" || lines[1].Model != "opus" {
		t.Fatalf("eng.LogLines = %+v, err=%v", lines, err)
	}

	for _, bad := range []string{"missing", "../proj-demo", "a/b", ""} {
		if _, err := eng.Graph(bad); !errors.Is(err, ErrProjectNotFound) {
			t.Errorf("Graph(%q) err = %v, want ErrProjectNotFound", bad, err)
		}
		if _, err := eng.LogLines(bad, 10); !errors.Is(err, ErrProjectNotFound) {
			t.Errorf("LogLines(%q) err = %v, want ErrProjectNotFound", bad, err)
		}
	}
}
