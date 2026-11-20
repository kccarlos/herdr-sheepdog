package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// TestStatusViewAgreesWithGraph checks design.md 4.11: status lists working,
// waiting_on_dep (naming the dependency), done, and a task no pass has reached
// yet; every task's stage matches GraphViewOf from the same single function,
// and non-row log lines (pr_snapshot, ready) do not overwrite the last row.
func TestStatusViewAgreesWithGraph(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	st := flow.State{
		Seq:      5,
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "proj-st", Folder: "/tmp/not-a-git-repo", Goal: "demo", Started: false},
		Tasks: []flow.Task{
			{ID: "T1", Title: "feat: done task", Done: &flow.Merged{MergedCommit: "aaa111"}},
			{ID: "T2", Title: "feat: a very long working task title that should truncate cleanly", Attempts: []flow.Attempt{{BaseCommit: "000", RunID: "w-2"}}},
			{ID: "T3", Title: "feat: waiting on dep", After: []string{"T2"}},
			{ID: "T4", Title: "feat: not reached yet", After: []string{"T1"}},
		},
	}
	logs := []LogLine{
		{Seq: 1, At: t0, By: "engine", Task: "T1", Rule: "N1", Decision: "cleaned up", Why: "ended: stop its runs and clean up"},
		{Seq: 2, At: t0.Add(time.Minute), By: "engine", Task: "T2", Rule: "N23", Decision: "started worker", Why: "the worker is working"},
		{Seq: 3, At: t0.Add(2 * time.Minute), By: "engine", Task: "T2", Rule: "pr_snapshot", Why: "the PR snapshot changed"},
		{Seq: 4, At: t0.Add(3 * time.Minute), By: "engine", Task: "T3", Rule: "N9", Decision: "waiting", Why: "waiting on T2"},
	}

	got := StatusViewOf(st, logs)
	graph := GraphViewOf(st, logs)
	if len(got) != 4 || len(graph.Tasks) != 4 {
		t.Fatalf("len(status)=%d len(graph)=%d, want 4", len(got), len(graph.Tasks))
	}
	for i := range got {
		if got[i].ID != graph.Tasks[i].Ref || got[i].Stage != graph.Tasks[i].Stage {
			t.Errorf("task %d: status=(%s, %s) != graph=(%s, %s)", i, got[i].ID, got[i].Stage, graph.Tasks[i].Ref, graph.Tasks[i].Stage)
		}
	}

	want := []struct {
		id, stage, rule, why string
	}{
		{"T1", "done", "N1", "ended: stop its runs and clean up"},
		{"T2", "working", "N23", "the worker is working"},
		{"T3", "waiting_on_dep", "N9", "waiting on T2"},
		{"T4", "queued", "", "Project is stopped (no logged row)"},
	}
	for i, w := range want {
		if got[i].ID != w.id || got[i].Stage != w.stage || got[i].Rule != w.rule || got[i].Why != w.why {
			t.Errorf("task %d = %+v, want %+v", i, got[i], w)
		}
	}

	// Formatting: one line per task, short title truncated, "-" for unreached rule.
	line2 := FormatStatusLine(got[1])
	if strings.Contains(line2, "\n") || !strings.Contains(line2, "…") || !strings.Contains(line2, "N23") {
		t.Errorf("T2 formatted = %q", line2)
	}
	line4 := FormatStatusLine(got[3])
	if !strings.Contains(line4, " - ") || !strings.Contains(line4, "no logged row") {
		t.Errorf("T4 formatted = %q", line4)
	}

	// ProjectStatus reads stored state + log only (works with Started: false and no git repo at Folder).
	paths := testPaths(t)
	if err := SaveState(paths.State(st.Project.ID), st); err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if err := AppendLog(paths.Log(st.Project.ID), l); err != nil {
			t.Fatal(err)
		}
	}
	for _, folder := range []string{"", st.Project.Folder} {
		disk, err := ProjectStatus(paths, folder)
		if err != nil {
			t.Fatalf("ProjectStatus(%q): %v", folder, err)
		}
		if len(disk) != 4 || disk[2].Rule != "N9" || disk[2].Why != "waiting on T2" || !strings.Contains(disk[3].Why, "no logged row") {
			t.Fatalf("ProjectStatus(%q) = %+v", folder, disk)
		}
	}

	// Works when log.jsonl does not exist yet: every task reports "no logged row".
	if err := os.Remove(paths.Log(st.Project.ID)); err != nil {
		t.Fatal(err)
	}
	noLog, err := ProjectStatus(paths, filepath.Clean(st.Project.Folder))
	if err != nil {
		t.Fatalf("ProjectStatus without log: %v", err)
	}
	for _, ts := range noLog {
		if ts.Rule != "" || !strings.Contains(ts.Why, "no logged row") {
			t.Errorf("without log, %s = %+v, want empty rule and 'no logged row'", ts.ID, ts)
		}
	}
}

func TestStatusControlCharsAndBadTaskID(t *testing.T) {
	line := FormatStatusLine(TaskStatus{
		ID:    "T1\x1b[2J\nX",
		Title: "feat: \x1b]0;pwn\a x\u202ey",
		Stage: "working",
		Rule:  "N23\x1b[31m",
		Why:   "a\x1b[2Jb\rc\u202ed",
	})
	if strings.Contains(line, "\n") {
		t.Errorf("status line contains newline: %q", line)
	}
	for _, r := range line {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Errorf("status line contains control/format rune %U in %q", r, line)
		}
	}

	// plan apply / amend / split children all validate IDs through checkPlanWith.
	for _, badID := range []string{"T1\x1b[2J", "T1\nX", "bad id", "a/b", strings.Repeat("a", 33)} {
		raw := []byte(`{"tasks":[{"id":` + `"` + strings.ReplaceAll(strings.ReplaceAll(badID, "\x1b", `\u001b`), "\n", `\n`) + `"` + `,"title":"feat: x","for_whom":"u","if_skipped":"s","smallest":"m","not_doing":"n","outcomes":[{"text":"ok","proved_by":"cmd: true"}],"estimate_lines":10}]}`)
		_, fails := checkPlan(raw, 400)
		if len(fails) == 0 {
			t.Errorf("checkPlan accepted bad task id %q", badID)
		}
	}
	goodRaw := []byte(`{"tasks":[{"id":"SDF-53_a.1","title":"feat: x","for_whom":"u","if_skipped":"s","smallest":"m","not_doing":"n","outcomes":[{"text":"ok","proved_by":"cmd: true"}],"estimate_lines":10}]}`)
	if _, fails := checkPlan(goodRaw, 400); len(fails) != 0 {
		t.Errorf("checkPlan rejected valid task id: %v", fails)
	}
}

func TestStatusLineAgeStaleAndParkedNoLog(t *testing.T) {
	now := time.Now()
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "proj-age", Folder: "/tmp/age", Started: true},
		Tasks: []flow.Task{
			{ID: "T0", Title: "feat: merged dep", Done: &flow.Merged{MergedCommit: "aaa"}},
			{ID: "T1", Title: "feat: in ci", LastRow: "N36", Attempts: []flow.Attempt{{
				BaseCommit: "000", Ready: []flow.Ready{{Head: "h1"}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
				},
			}}},
			{ID: "T2", Title: "feat: now queued", After: []string{"T0"}, LastRow: "N10"},
			{ID: "T3", Title: "feat: working", Attempts: []flow.Attempt{{BaseCommit: "000", RunID: "w-3"}}},
			{ID: "T4", Title: "feat: parked before start", Parked: true},
		},
	}
	logs := []LogLine{
		{Seq: 1, At: now.Add(-12*time.Minute - 30*time.Second), By: "engine", Task: "T1", Rule: "N29", Why: "the ladder says review"},
		{Seq: 2, At: now.Add(-5*time.Minute - 30*time.Second), By: "engine", Task: "T2", Rule: "N9", Why: "waiting on T0"},
		{Seq: 3, At: now.Add(-3*time.Minute - 30*time.Second), By: "engine", Task: "T3", Rule: "N23", Why: "the worker is working"},
	}
	got := StatusViewOf(st, logs)

	l1 := FormatStatusLine(got[1])
	if !strings.Contains(l1, "ci") || !strings.Contains(l1, "12m ago") || !strings.Contains(l1, "(last logged") {
		t.Errorf("stale ci line = %q, want 'ci', '12m ago', and '(last logged'", l1)
	}
	l2 := FormatStatusLine(got[2])
	if !strings.Contains(l2, "queued") || !strings.Contains(l2, "5m ago") || !strings.Contains(l2, "(last logged") {
		t.Errorf("stale queued line = %q, want 'queued', '5m ago', and '(last logged'", l2)
	}
	l3 := FormatStatusLine(got[3])
	if !strings.Contains(l3, "3m ago") || strings.Contains(l3, "(last logged") {
		t.Errorf("fresh working line = %q, want '3m ago' without '(last logged'", l3)
	}
	l4 := FormatStatusLine(got[4])
	if !strings.Contains(l4, "blocked_or_failed") || !strings.Contains(l4, "no logged row") || strings.Contains(l4, "no pass yet") {
		t.Errorf("parked no-log line = %q, want 'blocked_or_failed' and 'no logged row' (not 'no pass yet')", l4)
	}
}

func TestClosedDepSatisfiedInGraphAndStatus(t *testing.T) {
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "proj-closed", Folder: "/tmp/closed", Started: true},
		Tasks: []flow.Task{
			{ID: "T1", Title: "feat: closed ntc", Closed: &flow.Closed{Why: "already upstream"}},
			{ID: "T2", Title: "feat: after closed", After: []string{"T1"}},
			{ID: "T3", Title: "feat: dropped", Dropped: &flow.Dropped{Why: "dup"}},
			{ID: "T4", Title: "feat: after dropped", After: []string{"T3"}},
		},
	}
	g := GraphViewOf(st, nil)
	s := StatusViewOf(st, nil)
	if g.Tasks[1].Stage != "queued" || s[1].Stage != "queued" {
		t.Errorf("T2 after closed dep: graph=%q status=%q, want queued", g.Tasks[1].Stage, s[1].Stage)
	}
	if g.Tasks[3].Stage != "waiting_on_dep" || s[3].Stage != "waiting_on_dep" {
		t.Errorf("T4 after dropped dep: graph=%q status=%q, want waiting_on_dep", g.Tasks[3].Stage, s[3].Stage)
	}
}

func TestProjectStatusSubfolderAndCorruptState(t *testing.T) {
	paths := testPaths(t)
	repo := filepath.Join(t.TempDir(), "my-repo")
	sub := filepath.Join(repo, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "proj-sub", Folder: repo, Started: false},
		Tasks:    []flow.Task{{ID: "T1", Title: "feat: one"}},
	}
	if err := SaveState(paths.State("proj-sub"), st); err != nil {
		t.Fatal(err)
	}
	got, err := ProjectStatus(paths, sub)
	if err != nil || len(got) != 1 || got[0].ID != "T1" {
		t.Errorf("ProjectStatus(subfolder) = %+v, err=%v; want T1", got, err)
	}

	// Corrupt state file prints the load error instead of "no factory project".
	badPaths := testPaths(t)
	badState := badPaths.State("proj-broken")
	if err := os.MkdirAll(filepath.Dir(badState), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badState, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"", "proj-broken", "/tmp/some-folder"} {
		_, err := ProjectStatus(badPaths, target)
		if err == nil || strings.Contains(err.Error(), "no factory project") {
			t.Errorf("ProjectStatus(corrupt, %q) err = %v, want load error", target, err)
		}
	}
}

func TestStatusEveryTaskRowFreshAndStage(t *testing.T) {
	validStage := map[string]bool{
		"done": true, "blocked_or_failed": true, "waiting_on_dep": true,
		"queued": true, "working": true, "ci": true,
	}
	for _, r := range flow.LadderRows {
		if !validStage[r.Stage] {
			t.Errorf("LadderRows[%s].Stage = %q, want valid 4.11 stage", r.ID, r.Stage)
		}
	}
	passedTask := func(id, title string) flow.Task {
		return flow.Task{
			ID: id, Title: title,
			Attempts: []flow.Attempt{{
				BaseCommit: "000", PR: 42, Ready: []flow.Ready{{Head: "h1"}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: "h1", Verdict: flow.VerdictPass},
				},
			}},
		}
	}
	now := time.Now()
	for _, r := range flow.TaskRows {
		t.Run(r.ID, func(t *testing.T) {
			if !validStage[r.Stage] {
				t.Fatalf("TaskRows[%s].Stage = %q, want valid 4.11 stage", r.ID, r.Stage)
			}
			task := passedTask("T1", "feat: "+r.ID)
			switch r.ID {
			case "N4":
				task = passedTask("T1", "feat: closed PR")
			case "N32":
				task = passedTask("T1", "feat: passed task with conflict")
			}
			st := flow.State{
				Settings: flow.DefaultSettings(),
				Project:  flow.Project{ID: "proj-row", Folder: "/tmp/row", Started: true},
				Tasks:    []flow.Task{task},
			}
			logs := []LogLine{{Seq: 1, At: now, By: "engine", Task: "T1", Rule: r.ID, Why: "matched " + r.ID}}
			got := StatusViewOf(st, logs)
			graph := GraphViewOf(st, logs)
			if got[0].Stage != r.Stage || graph.Tasks[0].Stage != r.Stage {
				t.Errorf("%s: status stage=%q graph stage=%q, want %q", r.ID, got[0].Stage, graph.Tasks[0].Stage, r.Stage)
			}
			if got[0].Stale {
				t.Errorf("%s: Stale=true, want false (stage=%q)", r.ID, got[0].Stage)
			}
			if line := FormatStatusLine(got[0]); strings.Contains(line, "(last logged") || !strings.Contains(line, r.ID) {
				t.Errorf("%s formatted = %q, want live %s line", r.ID, line, r.ID)
			}
		})
	}
}
