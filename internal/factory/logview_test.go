package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// TestFactoryLogGolden: `sheepdog factory log` prints one line per step
// with time, task, rule, asks, decision and why; commit ids are cut to 7;
// a torn last line is skipped; n keeps the last n lines.
func TestFactoryLogGolden(t *testing.T) {
	paths := testPaths(t)
	id := "proj-0001"
	if err := SaveState(paths.State(id), flow.State{Seq: 6, Project: flow.Project{ID: id, Folder: "/tmp/demo", Started: true}}); err != nil {
		t.Fatal(err)
	}
	h := strings.Repeat("3f9c", 10)
	at := func(m int) time.Time { return time.Date(2026, 11, 19, 10, m, 0, 0, time.UTC) }
	for _, l := range []LogLine{
		{Seq: 1, At: at(0), By: "owner", Rule: "start", Decision: "started", Why: "Add Greet."},
		{Seq: 2, At: at(1), By: "engine", Rule: "J7", Asked: []string{"coordinator:t-1"}, Decision: "started coordinator", Why: "project started with no coordinator run"},
		{Seq: 3, At: at(30), By: "engine", Task: "T1", Rule: "pr_snapshot", Why: "the PR snapshot changed"},
		{Seq: 4, At: at(31), By: "engine", Task: "T1", Rule: "N42", Asked: []string{"record_accept@" + h, "merge@" + h},
			Decision: "accepted " + h + " by the bar (rounds [1]); asked GitHub to merge PR #7 at " + h, Why: "passed, green and mergeable at H"},
		{Seq: 5, At: at(32), By: "engine", Task: "T1", Rule: "N2", Asked: []string{"mark_done"}, Decision: "done: PR #7 merged as " + strings.Repeat("ab", 20), Why: "the PR is merged"},
		{Seq: 6, At: at(33), By: "engine", Task: "T1", Rule: "N1", Asked: []string{"cleanup"}, Decision: "stopped the task's runs; deleted remote branch task/T1-x", Why: "ended: stop its runs and clean up"},
	} {
		if err := AppendLog(paths.Log(id), l); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(paths.Log(id), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"seq":7,"rule":"N4`)
	_ = f.Close()

	format := func(n int) string {
		lines, summary, err := ProjectLog(context.Background(), paths, "", n)
		if err != nil {
			t.Fatal(err)
		}
		if summary != "" {
			t.Fatalf("summary with no reviewed tasks = %q, want empty", summary)
		}
		var b strings.Builder
		for _, l := range lines {
			b.WriteString(FormatLogLine(l) + "\n")
		}
		return b.String()
	}
	want, err := os.ReadFile(filepath.Join("testdata", "factory_log.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got := format(0); got != string(want) {
		t.Fatalf("factory log =\n%s\nwant\n%s", got, want)
	}
	if got := format(2); strings.Count(got, "\n") != 2 || !strings.Contains(got, " N1 ") {
		t.Fatalf("factory log -n 2 =\n%s", got)
	}
}

// TestFirstLookPassRateAndLogSummary checks design.md 11.2 / prompts.md 1.2:
// first-look pass rate counts tasks whose first reviewed round passed over
// tasks with at least one reviewed round, ignoring send-backs and unreviewed tasks.
func TestFirstLookPassRateAndLogSummary(t *testing.T) {
	paths := testPaths(t)
	id := "proj-rate"
	tasks := []flow.Task{
		{ID: "T1", Attempts: []flow.Attempt{{Rounds: []flow.Round{
			{N: 1, Kind: flow.RoundSendBack, Verdict: flow.VerdictChanges},
			{N: 2, Kind: flow.RoundFull, Verdict: flow.VerdictPass},
			{N: 3, Kind: flow.RoundFull, Verdict: flow.VerdictPass},
		}}}},
		{ID: "T2", Attempts: []flow.Attempt{{Rounds: []flow.Round{
			{N: 1, Kind: flow.RoundFull, Verdict: flow.VerdictChanges, Majors: 1},
			{N: 2, Kind: flow.RoundDelta, Verdict: flow.VerdictPass},
		}}}},
		{ID: "T3", Attempts: []flow.Attempt{
			{Closed: "new", Rounds: []flow.Round{{N: 1, Kind: flow.RoundFull, Verdict: flow.VerdictChanges, Majors: 1}}},
			{Rounds: []flow.Round{{N: 1, Kind: flow.RoundFull, Verdict: flow.VerdictPass}}},
		}},
		{ID: "T4", Attempts: []flow.Attempt{{Rounds: []flow.Round{{N: 1, Kind: flow.RoundSendBack, Verdict: flow.VerdictChanges}}}}},
		{ID: "T5"},
	}
	if p, r := flow.FirstLookPassRate(tasks); p != 1 || r != 3 {
		t.Fatalf("FirstLookPassRate = %d/%d, want 1/3", p, r)
	}
	if err := SaveState(paths.State(id), flow.State{Seq: 1, Project: flow.Project{ID: id, Folder: "/tmp/rate", Started: true}, Tasks: tasks}); err != nil {
		t.Fatal(err)
	}
	if err := AppendLog(paths.Log(id), LogLine{
		Seq: 1, At: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC), By: flow.RoleReviewer, Task: "T2",
		Rule: "verdict", Decision: "changes", Why: "round 1 (full, opus) at abcdef1: changes (1 major(s), 2 self-check miss(es))",
	}); err != nil {
		t.Fatal(err)
	}
	lines, summary, err := ProjectLog(context.Background(), paths, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "first-look pass rate: 1/3 (33%)" {
		t.Fatalf("summary = %q, want 1/3 (33%%)", summary)
	}
	if len(lines) != 1 || !strings.Contains(FormatLogLine(lines[0]), "2 self-check miss(es)") {
		t.Fatalf("formatted line = %q", FormatLogLine(lines[0]))
	}
}
