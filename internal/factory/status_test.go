package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		{"T4", "queued", "", "no pass yet"},
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
	if !strings.Contains(line4, " - ") || !strings.Contains(line4, "no pass yet") {
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
		if len(disk) != 4 || disk[2].Rule != "N9" || disk[2].Why != "waiting on T2" || disk[3].Why != "no pass yet" {
			t.Fatalf("ProjectStatus(%q) = %+v", folder, disk)
		}
	}

	// Works when log.jsonl does not exist yet: every task reports "no pass yet".
	if err := os.Remove(paths.Log(st.Project.ID)); err != nil {
		t.Fatal(err)
	}
	noLog, err := ProjectStatus(paths, filepath.Clean(st.Project.Folder))
	if err != nil {
		t.Fatalf("ProjectStatus without log: %v", err)
	}
	for _, ts := range noLog {
		if ts.Rule != "" || ts.Why != "no pass yet" {
			t.Errorf("without log, %s = %+v, want empty rule and 'no pass yet'", ts.ID, ts)
		}
	}
}
