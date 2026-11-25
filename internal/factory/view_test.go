package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
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
	if tv := byRef["T2"]; tv.Layer != 1 || tv.Stage != "working" || tv.WhyWaiting != "In review round 2" ||
		tv.Rounds != 1 || tv.LastRule != "N29" || tv.LastDecision != "started reviewer rev-demo-t2-1-2" ||
		tv.Model != "sonnet" || tv.Terminal != "rev-demo-t2-1-2" || !slices.Equal(tv.Dependents, []string{"T4", "T5"}) {
		t.Errorf("T2 = %+v", tv)
	}
	if tv := byRef["T3"]; tv.Layer != 1 || tv.Stage != "queued" || tv.WhyWaiting != "Queued to start" || tv.Rounds != 0 || tv.Terminal != "" {
		t.Errorf("T3 = %+v", tv)
	}
	if tv := byRef["T4"]; tv.Layer != 2 || tv.Stage != "blocked_or_failed" || tv.WhyWaiting != "At review recap, waiting on the coordinator" ||
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
			{ID: "fix_no_slot", LastRow: "N22", HoldWhy: "no slot is free", Attempts: []flow.Attempt{{
				BaseCommit: "0", Ready: []flow.Ready{{Head: "h1"}},
				Run:    &flow.RunRecord{Role: flow.RoleWorker, State: flow.PhaseEnded, Result: flow.EndExited},
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
		"done":        {"done", ""},
		"closed":      {"done", ""},
		"dropped":     {"done", ""},
		"split":       {"done", ""},
		"parked":      {"blocked_or_failed", "Parked, waiting on the coordinator"},
		"ntc":         {"blocked_or_failed", "Worker reported nothing to change, waiting on the coordinator"},
		"working":     {"working", "Worker is working"},
		"fix":         {"working", "Waiting on the worker"},
		"fix_no_slot": {"working", "no slot is free"},
		"ci":          {"ci", "Waiting on CI and merge"},
		"cycA":        {"waiting_on_dep", "Waiting on cycB"},
		"cycB":        {"waiting_on_dep", "Waiting on cycA"},
	}
	for ref, w := range want {
		tv := byRef[ref]
		if tv.Stage != w.stage || tv.WhyWaiting != w.why {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", ref, tv.Stage, tv.WhyWaiting, w.stage, w.why)
		}
	}
	// Non-row log lines (ready, pr_snapshot) do not populate LastRule/LastDecision
	// when no task row has been logged (plan.md 9.1).
	if byRef["ci"].LastRule != "" || byRef["ci"].LastDecision != "" || byRef["parked"].LastRule != "N3" || byRef["parked"].LastDecision != "stopped runs" {
		t.Errorf("last_rule/last_decision unexpected: ci=%+v parked=%+v", byRef["ci"], byRef["parked"])
	}

	// Finding 7 (trial seq 65, design.md 4.11): N22 with worker gone and no slot
	// free records HoldWhy "no slot is free" so the why line shows it instead of
	// "Waiting on the worker".
	t.Run("n22_worker_gone_no_slot", func(t *testing.T) {
		ctx := context.Background()
		srv := hctest.New(t)
		client := hc.NewClient(srv.Path)
		paths := testPaths(t)
		repo, base := initGitRepoWithRemote(t)
		githubOrigin(t, repo)
		gh := bindRepoFakeGH(t, repo)

		now := time.Date(2026, 11, 22, 20, 34, 0, 0, time.UTC)
		endedAt := now.Add(-5 * time.Minute)
		eng := newTestEngine(t, paths, client)
		eng.Now = func() time.Time { return now }
		if err := eng.Start(repo, "Finding 7 no slot why", 1); err != nil {
			t.Fatal(err)
		}
		id := eng.projectDirs()[0].Name()
		srv.Add(hc.Terminal{
			Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
			Labels: testCoordLabels(id, "run-elsewhere"),
			Status: &hc.Status{App: "claude-code", State: "working"},
		})

		wt1, br1, _ := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
		wt5, br5, c5 := makeSlotWorktree(t, ctx, paths, repo, id, "T5", "t5.go")
		writeOpenPRView(t, gh, 5, c5, "[]")

		w1, err := startRun(ctx, client, world.RunSpec{Name: "worker-t1-live", Cwd: wt1, Argv: []string{"sh"}})
		if err != nil {
			t.Fatal(err)
		}
		setTerminalStatusAt(t, srv, client, w1, "working", now)

		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		st.Project.RunID = "coord-elsewhere"
		st.Tasks = []flow.Task{
			{
				ID: "T1", Title: "feat: T1 holding the slot", EstimateLines: 100,
				Attempts: []flow.Attempt{{
					SizeCap: 200, BaseCommit: base, Branch: br1, Worktree: wt1, RunID: w1,
					Run: &flow.RunRecord{
						Role: flow.RoleWorker, CLI: "claude", Address: w1,
						State: flow.PhaseWorking, Status: "working", StatusSince: now, StatusReported: true,
					},
					Ready: []flow.Ready{}, Rounds: []flow.Round{}, Accepts: []flow.Accept{},
					Acts: []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-time.Minute)}},
				}},
			},
			{
				ID: "T5", Title: "feat: T5 at N22 with no slot", EstimateLines: 200,
				Attempts: []flow.Attempt{{
					SizeCap: 400, BaseCommit: base, Branch: br5, Worktree: wt5, PR: 5,
					Run: &flow.RunRecord{
						Role: flow.RoleWorker, CLI: "claude", Address: "worker-t5-gone",
						SessionID: "sess-t5", Resumable: true,
						State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: endedAt,
					},
					Ready:   []flow.Ready{{Head: c5}},
					Rounds:  []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c5, Verdict: flow.VerdictChanges, Majors: 1, ReportPath: "/r/t5-r1.md"}},
					Accepts: []flow.Accept{},
					Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: endedAt.Add(-time.Minute)}},
				}},
			},
		}
		if err := SaveState(paths.State(id), st); err != nil {
			t.Fatal(err)
		}

		// Pass 1 holds round 1 findings on T5; Pass 2 waits at N22 with no slot free.
		eng.passProject(ctx, id, false)
		eng.passProject(ctx, id, false)

		g, err := eng.Graph(id)
		if err != nil {
			t.Fatal(err)
		}
		if tv := g.Tasks[1]; tv.Ref != "T5" || tv.Stage != "working" || tv.WhyWaiting != "no slot is free" {
			t.Errorf("GraphViewOf[T5] = (ref=%q, stage=%q, why=%q), want (T5, working, %q)", tv.Ref, tv.Stage, tv.WhyWaiting, "no slot is free")
		}
		s, logs, err := eng.loadProjectCopy(id)
		if err != nil {
			t.Fatal(err)
		}
		if sv := StatusViewOf(s, logs); sv[1].ID != "T5" || sv[1].Why != "no slot is free" {
			t.Errorf("StatusViewOf[T5] = (id=%q, why=%q), want (T5, %q)", sv[1].ID, sv[1].Why, "no slot is free")
		}
	})
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

	eng := newGitTestEngine(t, paths, nil)
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

func TestTaskWhyLinesPriorityAndFactoryPageRoundText(t *testing.T) {
	passed := []flow.Attempt{{
		BaseCommit: "000", PR: 1, Ready: []flow.Ready{{Head: "h1"}},
		Rounds: []flow.Round{
			{N: 1, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
			{N: 2, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
		},
	}}
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "p", Started: true},
		Tasks: []flow.Task{
			{ID: "rev1", LastRow: "N29", Attempts: []flow.Attempt{{
				BaseCommit: "000", Ready: []flow.Ready{{Head: "h1"}},
				Review: &flow.ReviewRun{Round: 1, Head: "h1"},
			}}},
			{ID: "rev2", LastRow: "N29", Attempts: []flow.Attempt{{
				BaseCommit: "000", Ready: []flow.Ready{{Head: "h1"}},
				Rounds: []flow.Round{{N: 1, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass}},
				Review: &flow.ReviewRun{Round: 2, Head: "h1"},
			}}},
			{ID: "parked_stale", Parked: true, LastRow: "N23", Attempts: []flow.Attempt{{BaseCommit: "000", RunID: "w-1"}}},
			{ID: "dropped_stale", Dropped: &flow.Dropped{Why: "dup"}, LastRow: "N23", Attempts: []flow.Attempt{{BaseCommit: "000", RunID: "w-1"}}},
			{ID: "recap_size", LastRow: "N6", Recaps: []flow.Recap{{Trigger: flow.TriggerSize}}},
			{ID: "ci_n36", LastRow: "N36", Attempts: passed},
			{ID: "ci_n38", LastRow: "N38", Attempts: passed},
			{ID: "ci_n37", LastRow: "N37", Attempts: passed},
			{ID: "ntc_n8", LastRow: "N8", Attempts: []flow.Attempt{{BaseCommit: "000", NothingToChange: "done"}}},
			{ID: "closed_pr_n4", LastRow: "N4", Attempts: []flow.Attempt{{BaseCommit: "000"}}},
		},
	}
	g := GraphViewOf(st, nil)
	byRef := map[string]TaskView{}
	for _, tv := range g.Tasks {
		byRef[tv.Ref] = tv
	}
	want := map[string]struct {
		stage, why string
		rounds     int
	}{
		"rev1":          {"working", "In review round 1", 0},
		"rev2":          {"working", "In review round 2", 1},
		"parked_stale":  {"blocked_or_failed", "Parked, waiting on the coordinator", 0},
		"dropped_stale": {"done", "", 0},
		"recap_size":    {"blocked_or_failed", "At size recap, waiting on the coordinator", 0},
		"ci_n36":        {"ci", "Waiting on CI and merge", 2},
		"ci_n38":        {"ci", "Waiting on the base branch", 2},
		"ci_n37":        {"ci", "Waiting on a GitHub approval", 2},
		"ntc_n8":        {"blocked_or_failed", "Worker reported nothing to change, waiting on the coordinator", 0},
		"closed_pr_n4":  {"blocked_or_failed", "Waiting on the coordinator", 0},
	}
	for ref, w := range want {
		tv := byRef[ref]
		if tv.Stage != w.stage || tv.WhyWaiting != w.why || tv.Rounds != w.rounds {
			t.Errorf("%s: got (%q, %q, rounds=%d), want (%q, %q, rounds=%d)", ref, tv.Stage, tv.WhyWaiting, tv.Rounds, w.stage, w.why, w.rounds)
		}
	}

	// Stopped project reports "Project is stopped" even when LastRow is set.
	stopped := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "p-stop", Started: false},
		Tasks: []flow.Task{
			{ID: "q", LastRow: "N10"},
			{ID: "w", LastRow: "N23", Attempts: []flow.Attempt{{BaseCommit: "000", RunID: "w-1"}}},
			{ID: "d", Done: &flow.Merged{MergedCommit: "aaa"}, LastRow: "N1"},
		},
	}
	gs := GraphViewOf(stopped, nil)
	if gs.Tasks[0].Stage != "queued" || gs.Tasks[0].WhyWaiting != "Project is stopped" {
		t.Errorf("stopped queued = %+v", gs.Tasks[0])
	}
	if gs.Tasks[1].Stage != "working" || gs.Tasks[1].WhyWaiting != "Project is stopped" {
		t.Errorf("stopped working = %+v", gs.Tasks[1])
	}
	if gs.Tasks[2].Stage != "done" || gs.Tasks[2].WhyWaiting != "" {
		t.Errorf("stopped done = %+v", gs.Tasks[2])
	}
}

func TestCIWhyOnlyWhenWaitingOnCIOrGitHub(t *testing.T) {
	passed := []flow.Attempt{{
		BaseCommit: "000", PR: 1, Ready: []flow.Ready{{Head: "h1"}},
		Rounds: []flow.Round{
			{N: 1, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
			{N: 2, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
		},
	}}
	cases := []struct {
		row, want string
	}{
		{"N33", "Waiting on CI; then the worker"},
		{"N34", "Waiting on CI; then the coordinator"},
		{"N35", "Waiting on CI; then the coordinator"},
		{"N36", "Waiting on CI and merge"},
		{"N37", "Waiting on a GitHub approval"},
		{"N38", "Waiting on the base branch"},
		{"N40", "Waiting on CI and merge"},
		{"N41", "Waiting on CI and merge"},
		{"N42", "Waiting on CI and merge"},
	}
	tasks := make([]flow.Task, len(cases))
	for i, tc := range cases {
		tasks[i] = flow.Task{ID: tc.row, LastRow: tc.row, Attempts: passed}
	}
	g := GraphViewOf(flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "p-ci", Started: true},
		Tasks:    tasks,
	}, nil)
	for i, tc := range cases {
		if got := g.Tasks[i].WhyWaiting; got != tc.want {
			t.Errorf("%s: WhyWaiting = %q, want %q", tc.row, got, tc.want)
		}
	}
}

func TestStoppedProjectDoesNotHideCoordinatorWait(t *testing.T) {
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "p-stop-coord", Started: false},
		Tasks: []flow.Task{
			{ID: "recap", LastRow: "N6", Recaps: []flow.Recap{{Trigger: flow.TriggerReview}}},
			{ID: "ntc", LastRow: "N8", Attempts: []flow.Attempt{{BaseCommit: "000", NothingToChange: "already fixed"}}},
			{ID: "parked", Parked: true, LastRow: "N3"},
			{ID: "queued", LastRow: "N10"},
		},
	}
	g := GraphViewOf(st, nil)
	want := []string{
		"At review recap, waiting on the coordinator",
		"Worker reported nothing to change, waiting on the coordinator",
		"Parked, waiting on the coordinator",
		"Project is stopped",
	}
	for i, w := range want {
		if got := g.Tasks[i].WhyWaiting; got != w {
			t.Errorf("%s: WhyWaiting = %q, want %q", g.Tasks[i].Ref, got, w)
		}
	}
}

func TestNoInReviewRoundAfterLastVerdict(t *testing.T) {
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "p-post-review", Started: true},
		Tasks: []flow.Task{{
			ID:      "T1",
			LastRow: "N29",
			Attempts: []flow.Attempt{{
				BaseCommit: "000", PR: 1, Ready: []flow.Ready{{Head: "h1"}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
				},
			}},
		}},
	}
	logs := []LogLine{{Seq: 1, By: "engine", Task: "T1", Rule: "N29", Why: "the ladder says review"}}
	g := GraphViewOf(st, logs)
	s := StatusViewOf(st, logs)
	want := "Waiting on the reviewer"
	if got := g.Tasks[0].WhyWaiting; got != want {
		t.Errorf("GraphViewOf WhyWaiting = %q, want %q", got, want)
	}
	if got := s[0].Why; got != want {
		t.Errorf("StatusViewOf Why = %q, want %q", got, want)
	}
}

// TestTaskWhyHoldPrecedesOverrideRowIDs checks plan.md 9.1 (KB-TSK-19):
// taskWhy returns t.HoldWhy before row-id cases, so an N15 hold (e.g. GitHub
// unavailable) and an N16 launch wait (e.g. quota hold) show the recorded hold
// instead of the fixed override prose.
func TestTaskWhyHoldPrecedesOverrideRowIDs(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 13, 0, 0, 0, time.UTC)
	n15Hold := "GitHub did not answer"
	n16Wait := "waiting on claude quota until 3:00 PM"
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "p-hold", Started: true},
		Tasks: []flow.Task{
			{
				ID:      "T-n15",
				Title:   "feat: override push held",
				LastRow: "N15",
				HoldWhy: n15Hold,
				Attempts: []flow.Attempt{{
					BaseCommit: "b0",
					Ready:      []flow.Ready{{Head: "c1"}},
					Overrides:  []flow.Override{{Head: "c1", ChecksShown: flow.ChecksAbsent}},
				}},
			},
			{
				ID:      "T-n16",
				Title:   "feat: override conflict launch wait",
				LastRow: "N16",
				HoldWhy: n16Wait,
				Attempts: []flow.Attempt{{
					BaseCommit: "b0",
					Ready:      []flow.Ready{{Head: "c2"}},
					Overrides:  []flow.Override{{Head: "c2", ChecksShown: flow.ChecksGreen}},
				}},
			},
		},
	}
	logs := []LogLine{
		{Seq: 1, At: t0, By: "engine", Task: "T-n15", Rule: "N15", Decision: "hold", Why: n15Hold},
		{Seq: 2, At: t0, By: "engine", Task: "T-n16", Rule: "N16", Decision: "hold", Why: n16Wait},
	}
	g := GraphViewOf(st, logs)
	s := StatusViewOf(st, logs)
	want := []struct {
		id, why string
	}{
		{"T-n15", n15Hold},
		{"T-n16", n16Wait},
	}
	for i, w := range want {
		if g.Tasks[i].Ref != w.id || g.Tasks[i].WhyWaiting != w.why {
			t.Errorf("GraphViewOf[%s].WhyWaiting = %q, want %q", w.id, g.Tasks[i].WhyWaiting, w.why)
		}
		if s[i].ID != w.id || s[i].Why != w.why {
			t.Errorf("StatusViewOf[%s].Why = %q, want %q", w.id, s[i].Why, w.why)
		}
	}
}

// TestTrailingPRSnapshotDoesNotBlankTaskDetail checks plan.md 9.1 (KB-TSK-19):
// GraphViewOf and StatusViewOf use the last task-row log line (lastRow), so a
// trailing pr_snapshot line does not overwrite LastRule/LastDecision on
// #<project>/<task>, and a task with no logged task row stays empty with no
// t.LastRow fallback.
func TestTrailingPRSnapshotDoesNotBlankTaskDetail(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 13, 0, 0, 0, time.UTC)
	passed := []flow.Attempt{{
		BaseCommit: "000", PR: 1, Ready: []flow.Ready{{Head: "h1"}},
		Rounds: []flow.Round{
			{N: 1, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
			{N: 2, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
		},
	}}
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "p-snap", Started: true},
		Tasks: []flow.Task{
			{ID: "T1", Title: "feat: logged row then snapshot", LastRow: "N36", Attempts: passed},
			{ID: "T2", Title: "feat: snapshot only, no logged row", LastRow: "N36", Attempts: passed},
		},
	}
	logs := []LogLine{
		{Seq: 1, At: t0, By: "engine", Task: "T1", Rule: "N36", Decision: "waiting on checks", Why: "checks are pending"},
		{Seq: 2, At: t0.Add(time.Second), By: "engine", Task: "T1", Rule: "pr_snapshot", Decision: "", Why: "the PR snapshot changed"},
		{Seq: 3, At: t0.Add(2 * time.Second), By: "engine", Task: "T2", Rule: "pr_snapshot", Decision: "", Why: "the PR snapshot changed"},
	}

	g := GraphViewOf(st, logs)
	s := StatusViewOf(st, logs)

	if g.Tasks[0].LastRule != "N36" || g.Tasks[0].LastDecision != "waiting on checks" {
		t.Errorf("GraphViewOf[T1] = (rule=%q, decision=%q), want (N36, %q)", g.Tasks[0].LastRule, g.Tasks[0].LastDecision, "waiting on checks")
	}
	if s[0].Rule != "N36" || s[0].Stale {
		t.Errorf("StatusViewOf[T1] = (rule=%q, stale=%v), want (N36, false)", s[0].Rule, s[0].Stale)
	}
	if g.Tasks[1].LastRule != "" || g.Tasks[1].LastDecision != "" {
		t.Errorf("GraphViewOf[T2] = (rule=%q, decision=%q), want empty (no logged row, no t.LastRow fallback)", g.Tasks[1].LastRule, g.Tasks[1].LastDecision)
	}
	if s[1].Rule != "" || s[1].Why != "Waiting on CI and merge (no logged row)" {
		t.Errorf("StatusViewOf[T2] = (rule=%q, why=%q), want empty rule and '(no logged row)'", s[1].Rule, s[1].Why)
	}
}

func TestGraphViewTaskBrief(t *testing.T) {
	wantBrief := "## Scope\n\n- Render task brief on `#project/task`\n- Keep board rows compact"
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "p-brief", Started: true},
		Tasks: []flow.Task{
			{ID: "T1", Title: "feat: has brief", Brief: wantBrief},
			{ID: "T2", Title: "feat: empty brief"},
		},
	}

	g := GraphViewOf(st, nil)
	if len(g.Tasks) != 2 {
		t.Fatalf("len(g.Tasks) = %d, want 2", len(g.Tasks))
	}
	if g.Tasks[0].Brief != wantBrief {
		t.Errorf("GraphViewOf[T1].Brief = %q, want %q", g.Tasks[0].Brief, wantBrief)
	}
	if g.Tasks[1].Brief != "" {
		t.Errorf("GraphViewOf[T2].Brief = %q, want empty", g.Tasks[1].Brief)
	}

	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	tasks, ok := doc["tasks"].([]any)
	if !ok || len(tasks) != 2 {
		t.Fatalf("unexpected tasks JSON: %s", raw)
	}
	t1 := tasks[0].(map[string]any)
	if got, ok := t1["brief"]; !ok || got != wantBrief {
		t.Errorf("T1 JSON brief = (%v, ok=%v), want (%q, true); raw=%s", got, ok, wantBrief, raw)
	}
	t2 := tasks[1].(map[string]any)
	if got, hasBrief := t2["brief"]; hasBrief {
		t.Errorf("T2 JSON unexpectedly marshaled brief = %v (want omitted when empty); raw=%s", got, raw)
	}
}

type logEntryWire struct {
	At       time.Time `json:"at"`
	By       string    `json:"by"`
	Task     string    `json:"task"`
	Decision string    `json:"decision"`
	Why      string    `json:"why"`
	Kind     string    `json:"kind"`
}

func decodeLogEntries(t *testing.T, entries []LogEntry) ([]logEntryWire, []map[string]any) {
	t.Helper()
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal LogEntry slice: %v", err)
	}
	var wire []logEntryWire
	var objs []map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal logEntryWire: %v", err)
	}
	if err := json.Unmarshal(raw, &objs); err != nil {
		t.Fatalf("unmarshal raw objects: %v", err)
	}
	return wire, objs
}

// callLogView invokes LogView regardless of whether PR 14.7 keeps the 2-arg
// signature or adds observations (bool or ...bool) / task parameters.
func callLogView(t *testing.T, lines []LogLine, n int, observations bool) ([]LogEntry, bool) {
	t.Helper()
	fv := reflect.ValueOf(LogView)
	ft := fv.Type()
	switch {
	case ft.NumIn() == 2 && !ft.IsVariadic():
		out := fv.Call([]reflect.Value{reflect.ValueOf(lines), reflect.ValueOf(n)})
		return out[0].Interface().([]LogEntry), !observations
	case ft.NumIn() == 3:
		out := fv.Call([]reflect.Value{reflect.ValueOf(lines), reflect.ValueOf(n), reflect.ValueOf(observations)})
		return out[0].Interface().([]LogEntry), true
	case ft.NumIn() == 4:
		out := fv.Call([]reflect.Value{reflect.ValueOf(lines), reflect.ValueOf(n), reflect.ValueOf(""), reflect.ValueOf(observations)})
		return out[0].Interface().([]LogEntry), true
	default:
		t.Fatalf("unexpected LogView signature: %s", ft)
		return nil, false
	}
}

// TestLogEntryKindTableAndPrecedence pins plan.md PR 14.7's typed-field kind
// table (first match wins, @-suffix stripped, no text guessing, no rule leak),
// using real log line shapes from factory_log.golden and PR 14.1 settings logs.
func TestLogEntryKindTableAndPrecedence(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	h := strings.Repeat("3f9c", 10)
	cases := []struct {
		name string
		line LogLine
		want string
	}{
		// Row 1: observation (empty decision) — wins over by=owner and any asked.
		{"observation_pr_snapshot", LogLine{Seq: 1, At: t0, By: "engine", Task: "T1", Rule: "pr_snapshot", Why: "the PR snapshot changed"}, "observation"},
		{"observation_precedes_owner_and_asked", LogLine{Seq: 2, At: t0, By: "owner", Task: "T1", Rule: "N2", Asked: []string{flow.AskMarkDone}, Decision: "", Why: "empty decision wins"}, "observation"},

		// Row 2: yours (by == owner) — wins over asked.
		{"yours_start", LogLine{Seq: 3, At: t0, By: "owner", Rule: "start", Decision: "started", Why: "Add Greet."}, "yours"},
		{"yours_setting_save", LogLine{Seq: 4, At: t0, By: "owner", Rule: "A3", Decision: "saved max_running 2 (was 1)", Why: "answered Save"}, "yours"},
		{"yours_precedes_asked", LogLine{Seq: 5, At: t0, By: "owner", Task: "T1", Rule: "A3", Asked: []string{flow.AskNotifyOwner, flow.AskMarkDone}, Decision: "answered Save", Why: "owner wins"}, "yours"},

		// Row 3: merged (asked holds mark_done, with or without @suffix) — wins over asked_you/needs_decision.
		{"merged_n2_golden", LogLine{Seq: 6, At: t0, By: "engine", Task: "T1", Rule: "N2", Asked: []string{flow.AskMarkDone}, Decision: "done: PR #7 merged as " + h, Why: "the PR is merged"}, "merged"},
		{"merged_with_extra_ask_and_suffix", LogLine{Seq: 7, At: t0, By: "engine", Task: "T1", Rule: "N2", Asked: []string{flow.AskWake + "@merged_outside_bar", flow.AskMarkDone + "@" + h}, Decision: "done", Why: "merged"}, "merged"},
		{"merged_precedes_asked_you", LogLine{Seq: 8, At: t0, By: "engine", Task: "T1", Rule: "N2", Asked: []string{flow.AskNotifyOwner, flow.AskMarkDone}, Decision: "done", Why: "precedence"}, "merged"},

		// Row 4: asked_you (notify_owner, open_gate_ask, open_n24_ask, open_owner_no_recap).
		{"asked_you_setting_proposal", LogLine{Seq: 9, At: t0, By: "engine", Rule: "A2", Asked: []string{flow.AskNotifyOwner}, Decision: "opened ask", Why: "This project: max running 2 (now 1). Save?"}, "asked_you"},
		{"asked_you_gate", LogLine{Seq: 10, At: t0, By: "engine", Task: "T1", Rule: "N31", Asked: []string{flow.AskOpenGate + "@" + h}, Decision: "opened gate ask", Why: "design gate"}, "asked_you"},
		{"asked_you_n24", LogLine{Seq: 11, At: t0, By: "engine", Task: "T1", Rule: "N24", Asked: []string{flow.AskOpenN24 + "@1"}, Decision: "opened owner ask", Why: "review stalled"}, "asked_you"},
		{"asked_you_owner_no_recap", LogLine{Seq: 12, At: t0, By: "engine", Task: "T1", Rule: "N31", Asked: []string{flow.AskOwnerNoRecap + "@" + h}, Decision: "opened recap", Why: "owner said no"}, "asked_you"},
		{"asked_you_precedes_needs_decision", LogLine{Seq: 13, At: t0, By: "engine", Task: "T1", Rule: "N31", Asked: []string{flow.AskOpenRecap + "@review", flow.AskOpenGate + "@" + h}, Decision: "opened ask", Why: "precedence"}, "asked_you"},

		// Row 5: needs_decision (open_recap, open_size_recap, open_ci_recap, open_time_recap, fail_update_branch).
		{"needs_decision_open_recap", LogLine{Seq: 14, At: t0, By: "engine", Task: "T1", Rule: "N28", Asked: []string{flow.AskOpenRecap + "@review"}, Decision: "opened recap", Why: "max rounds"}, "needs_decision"},
		{"needs_decision_size_recap", LogLine{Seq: 15, At: t0, By: "engine", Task: "T1", Rule: "N12", Asked: []string{flow.AskSizeRecap + "@" + h}, Decision: "opened size recap", Why: "212 lines, cap 160"}, "needs_decision"},
		{"needs_decision_ci_recap", LogLine{Seq: 16, At: t0, By: "engine", Task: "T1", Rule: "N13", Asked: []string{flow.AskCIRecap + "@" + h}, Decision: "opened ci recap", Why: "ci stalled"}, "needs_decision"},
		{"needs_decision_time_recap", LogLine{Seq: 17, At: t0, By: "engine", Task: "T1", Rule: "N14", Asked: []string{flow.AskTimeRecap + "@1"}, Decision: "opened time recap", Why: "build hours"}, "needs_decision"},
		{"needs_decision_fail_update", LogLine{Seq: 18, At: t0, By: "engine", Task: "T1", Rule: "N40", Asked: []string{flow.AskFailUpdate + "@" + h}, Decision: "could not bring the branch up to date", Why: "merge conflict"}, "needs_decision"},

		// Plain lines (no kind; never guessed from text).
		{"plain_j7_golden", LogLine{Seq: 19, At: t0, By: "engine", Rule: "J7", Asked: []string{"coordinator:t-1"}, Decision: "started coordinator", Why: "project started with no coordinator run"}, ""},
		{"plain_n42_golden", LogLine{Seq: 20, At: t0, By: "engine", Task: "T1", Rule: "N42", Asked: []string{flow.AskAccept + "@" + h, flow.AskMerge + "@" + h}, Decision: "accepted " + h + "; asked GitHub to merge PR #7", Why: "passed, green and mergeable"}, ""},
		{"plain_n1_golden", LogLine{Seq: 21, At: t0, By: "engine", Task: "T1", Rule: "N1", Asked: []string{flow.AskCleanup}, Decision: "stopped runs; deleted branch", Why: "ended"}, ""},
		{"plain_no_text_guess_merged", LogLine{Seq: 22, At: t0, By: "engine", Task: "T1", Rule: "N33", Asked: []string{flow.AskRerunCI + "@" + h}, Decision: "merged PR #7 failed and asked you", Why: "the PR snapshot changed"}, ""},
		{"plain_coordinator_action", LogLine{Seq: 23, At: t0, By: flow.RoleCoordinator, Task: "T1", Rule: "plan", Decision: "planned 4 tasks", Why: "in 2 layers"}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := tc.want == "observation"
			entries, supported := callLogView(t, []LogLine{tc.line}, 10, obs)
			if !supported {
				t.Fatalf("LogView does not accept observations=true yet (plan.md 14.7)")
			}
			if len(entries) != 1 {
				t.Fatalf("LogView returned %d entries, want 1", len(entries))
			}
			wire, objs := decodeLogEntries(t, entries)
			if wire[0].Kind != tc.want {
				t.Errorf("Kind = %q, want %q (raw=%v)", wire[0].Kind, tc.want, objs[0])
			}
			for _, forbidden := range []string{"rule", "asked", "input", "facts", "seq", "project"} {
				if _, ok := objs[0][forbidden]; ok {
					t.Errorf("LogEntry JSON leaked forbidden field %q: %v", forbidden, objs[0])
				}
			}
		})
	}
}

// TestEveryFlowAskConstantClassifiedForLogKind checks plan.md PR 14.7: every
// Ask… action constant in internal/factory/flow must be either mapped to a
// non-empty LogEntry.Kind or listed here as plain on purpose, so a new row
// cannot be silently unmarked.
func TestEveryFlowAskConstantClassifiedForLogKind(t *testing.T) {
	t.Parallel()
	plainOnPurpose := map[string]bool{
		flow.AskDeliverAnswer:     true,
		flow.AskOverrideResult:    true,
		flow.AskStartCoordinator:  true,
		flow.AskStopCoordinator:   true,
		flow.AskFinalJournal:      true,
		flow.AskCoordGoOn:         true,
		flow.AskCoordDialogStuck:  true,
		flow.AskDeliverWakes:      true,
		flow.AskHeartbeat:         true,
		flow.AskStartPlanReviewer: true,
		flow.AskStopPlanReviewer:  true,
		flow.AskAcceptPlan:        true,
		flow.AskRetry:             true,
		flow.AskNewAttempt:        true,
		flow.AskStartWorker:       true,
		flow.AskStartReview:       true,
		flow.AskNudgeReview:       true,
		flow.AskStopReview:        true,
		flow.AskStartRetro:        true,
		flow.AskNudgeRetro:        true,
		flow.AskStopRetro:         true,
		flow.AskTellFindings:      true,
		flow.AskTellStop:          true,
		flow.AskTellDiverged:      true,
		flow.AskTellConflict:      true,
		flow.AskMergeOnly:         true,
		flow.AskPushPR:            true,
		flow.AskRerunCI:           true,
		flow.AskTellCI:            true,
		flow.AskTellBaseRed:       true,
		flow.AskDispatchCI:        true,
		flow.AskUpdateBranch:      true,
		flow.AskAccept:            true,
		flow.AskMerge:             true,
		flow.AskSpendOverride:     true,
		flow.AskCleanup:           true,
		flow.AskWake:              true,
		flow.AskStopRuns:          true,
	}

	files, err := filepath.Glob(filepath.Join("flow", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	discovered := map[string]string{} // const name -> string value
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, ident := range vs.Names {
					name := ident.Name
					if !strings.HasPrefix(name, "Ask") || strings.HasPrefix(name, "AskStatus") || strings.HasPrefix(name, "AskedBy") {
						continue
					}
					if i >= len(vs.Values) {
						t.Fatalf("%s: const %s has no literal value", file, name)
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Fatalf("%s: const %s is not a string literal", file, name)
					}
					val, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("%s: unquote %s: %v", file, name, err)
					}
					discovered[name] = val
				}
			}
		}
	}
	if len(discovered) < 40 {
		t.Fatalf("discovered only %d Ask* constants in flow, expected >= 40", len(discovered))
	}

	t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	seenVal := map[string]bool{}
	for name, val := range discovered {
		seenVal[val] = true
		entries, _ := callLogView(t, []LogLine{{Seq: 1, At: t0, By: "engine", Task: "T1", Decision: "step", Asked: []string{val}}}, 10, false)
		if len(entries) != 1 {
			t.Fatalf("flow.%s (%q): LogView returned %d entries, want 1", name, val, len(entries))
		}
		wire, _ := decodeLogEntries(t, entries)
		inPlain := plainOnPurpose[val]
		if wire[0].Kind == "" && !inPlain {
			t.Errorf("flow.%s (%q) is in neither the log kind map nor plainOnPurpose", name, val)
		}
		if wire[0].Kind != "" && inPlain {
			t.Errorf("flow.%s (%q) mapped to kind %q but is also in plainOnPurpose", name, val, wire[0].Kind)
		}
	}
	for val := range plainOnPurpose {
		if !seenVal[val] {
			t.Errorf("plainOnPurpose entry %q does not match any Ask* constant in flow", val)
		}
	}
}

// TestLogViewAndEngineLogLinesObservationsAndTaskFilter checks plan.md PR 14.7:
// newest n lines without observations (n counts lines the owner sees), with
// observations, observation-only logs, and Engine.LogLines(project, n, task, observations)
// returning an older task's lines past 100 newer lines of another task.
func TestLogViewAndEngineLogLinesObservationsAndTaskFilter(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	var lines []LogLine
	var seq int64
	add := func(by, task, rule string, asked []string, dec, why string) {
		seq++
		lines = append(lines, LogLine{
			Seq: seq, At: t0.Add(time.Duration(seq) * time.Second), Project: "proj-log",
			By: by, Task: task, Rule: rule, Asked: asked, Decision: dec, Why: why,
		})
	}
	add("owner", "", "start", nil, "started", "build log")
	add("engine", "T1", "N23", []string{flow.AskStartWorker}, "started worker", "claude sonnet")
	add("engine", "T1", "pr_snapshot", nil, "", "the PR snapshot changed")
	add("engine", "T1", "N12", []string{flow.AskSizeRecap + "@c1"}, "opened size recap", "212 lines, cap 160")
	add("engine", "T1", "N2", []string{flow.AskMarkDone}, "done: PR #7 merged", "the PR is merged")
	add("engine", "T1", "pr_snapshot", nil, "", "the PR snapshot changed")
	for i := 1; i <= 55; i++ {
		add("engine", "T2", "N23", []string{flow.AskStartWorker}, fmt.Sprintf("t2-step-%d", i), "working")
		add("engine", "T2", "pr_snapshot", nil, "", "the PR snapshot changed")
	}
	// Two trailing observations so default n=2 must still return 2 visible decisions.
	add("engine", "T2", "pr_snapshot", nil, "", "the PR snapshot changed")

	// 1. LogView without observations: n=2 counts visible lines, not trailing observations.
	noObs, _ := callLogView(t, lines, 2, false)
	wireNoObs, _ := decodeLogEntries(t, noObs)
	if len(wireNoObs) != 2 || wireNoObs[0].Decision != "t2-step-54" || wireNoObs[1].Decision != "t2-step-55" {
		t.Errorf("LogView(n=2, obs=false) = %+v, want [t2-step-54, t2-step-55]", wireNoObs)
	}
	// 2. LogView with observations=true: n=2 returns the two trailing observation lines.
	withObs, obsSupported := callLogView(t, lines, 2, true)
	if !obsSupported {
		t.Errorf("LogView does not accept observations=true yet (plan.md 14.7)")
	} else {
		wireObs, _ := decodeLogEntries(t, withObs)
		if len(wireObs) != 2 || wireObs[0].Kind != "observation" || wireObs[1].Kind != "observation" {
			t.Errorf("LogView(n=2, obs=true) = %+v, want 2 observation lines", wireObs)
		}
	}
	// 3. Observation-only slice returns empty when observations=false.
	onlyObs := []LogLine{
		{Seq: 1, At: t0, By: "engine", Task: "T1", Rule: "pr_snapshot", Why: "the PR snapshot changed"},
		{Seq: 2, At: t0.Add(time.Second), By: "engine", Task: "T1", Rule: "pr_snapshot", Why: "the checks rollup changed"},
	}
	if got, _ := callLogView(t, onlyObs, 50, false); len(got) != 0 {
		t.Errorf("LogView(onlyObs, obs=false) len = %d, want 0", len(got))
	}

	// 4. Engine.LogLines(project, n, task, observations).
	paths := Paths{DataDir: t.TempDir()}
	if err := SaveState(paths.State("proj-log"), flow.State{
		Seq: seq, Settings: flow.DefaultSettings(),
		Project: flow.Project{ID: "proj-log", Folder: "/tmp/log", Goal: "build log", Started: true},
	}); err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if err := AppendLog(paths.Log("proj-log"), l); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveState(paths.State("proj-obs"), flow.State{
		Seq: 2, Settings: flow.DefaultSettings(),
		Project: flow.Project{ID: "proj-obs", Folder: "/tmp/obs", Goal: "only obs", Started: true},
	}); err != nil {
		t.Fatal(err)
	}
	for _, l := range onlyObs {
		if err := AppendLog(paths.Log("proj-obs"), l); err != nil {
			t.Fatal(err)
		}
	}

	eng := newGitTestEngine(t, paths, nil)
	type logLines4 interface {
		LogLines(project string, n int, task string, observations bool) ([]LogEntry, error)
	}
	ll, ok := any(eng).(logLines4)
	if !ok {
		t.Fatalf("Engine.LogLines(project, n, task, observations) method signature is missing (plan.md 14.7)")
	}

	// Task filter recovers T1's lines even though >100 T2 lines follow them.
	t1Vis, err := ll.LogLines("proj-log", 100, "T1", false)
	if err != nil {
		t.Fatalf("LogLines(T1, obs=false): %v", err)
	}
	wT1Vis, _ := decodeLogEntries(t, t1Vis)
	if len(wT1Vis) != 3 || wT1Vis[0].Decision != "started worker" || wT1Vis[1].Kind != "needs_decision" || wT1Vis[2].Kind != "merged" {
		t.Errorf("LogLines(T1, obs=false) = %+v, want 3 visible T1 lines [plain, needs_decision, merged]", wT1Vis)
	}
	for _, e := range wT1Vis {
		if e.Task != "T1" || e.Kind == "observation" {
			t.Errorf("LogLines(T1, obs=false) leaked entry %+v", e)
		}
	}

	// Task filter with observations=true includes T1's 2 pr_snapshot lines too.
	t1All, err := ll.LogLines("proj-log", 100, "T1", true)
	if err != nil {
		t.Fatalf("LogLines(T1, obs=true): %v", err)
	}
	wT1All, _ := decodeLogEntries(t, t1All)
	if len(wT1All) != 5 || wT1All[1].Kind != "observation" || wT1All[4].Kind != "observation" {
		t.Errorf("LogLines(T1, obs=true) = %+v, want 5 T1 lines with 2 observations", wT1All)
	}

	// Task filter honours n.
	t1One, err := ll.LogLines("proj-log", 1, "T1", false)
	if err != nil {
		t.Fatalf("LogLines(T1, n=1, obs=false): %v", err)
	}
	wT1One, _ := decodeLogEntries(t, t1One)
	if len(wT1One) != 1 || wT1One[0].Kind != "merged" {
		t.Errorf("LogLines(T1, n=1, obs=false) = %+v, want newest 1 visible T1 line (merged)", wT1One)
	}

	// Observation-only project: empty without observations, full with observations=true.
	if got, err := ll.LogLines("proj-obs", 50, "", false); err != nil || len(got) != 0 {
		t.Errorf("LogLines(proj-obs, obs=false) = (%d, %v), want (0, nil)", len(got), err)
	}
	if got, err := ll.LogLines("proj-obs", 50, "", true); err != nil || len(got) != 2 {
		t.Errorf("LogLines(proj-obs, obs=true) = (%d, %v), want (2, nil)", len(got), err)
	}
	if _, err := ll.LogLines("missing", 10, "T1", false); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("LogLines(missing) err = %v, want ErrProjectNotFound", err)
	}
}
