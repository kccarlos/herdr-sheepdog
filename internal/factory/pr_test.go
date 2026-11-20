package factory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// fakeGHScript is world's fake gh (world/github_test.go): it records argv in
// $D/argv, fails when $D/fail exists, and serves $D/view.json as the PR
// (pr list lists it; pr create copies $D/pr.json there).
const fakeGHScript = `#!/bin/sh
D=%DIR%
printf '%s\n' "$*" >> "$D/argv"
[ -f "$D/fail" ] && { echo "HTTP 502: Bad Gateway" >&2; exit 1; }
failif() { [ -f "$D/$1_fail" ] && { echo "gh: Resource not accessible by integration (HTTP 403)" >&2; exit 1; }; return 0; }
case "$1 $2" in
"pr list") if [ -f "$D/view.json" ]; then printf "[%s]" "$(cat "$D/view.json")"; else echo '[]'; fi ;;
"pr create") cp "$D/pr.json" "$D/view.json" && echo "https://github.com/acme/demo/pull/7" ;;
"pr view")
  if [ -f "$D/view_denied" ]; then
    case "$*" in *statusCheckRollup*)
      echo "GraphQL: Resource not accessible by personal access token (repository.pullRequest.statusCheckRollup.nodes.0.commit.statusCheckRollup.contexts.nodes.0)" >&2
      exit 1 ;;
    esac
  fi
  cat "$D/view.json" 2>/dev/null || { echo 'no pull requests found for branch "x"' >&2; exit 1; } ;;
"pr merge") [ -f "$D/merge_fail" ] && { echo "GraphQL: Head branch was modified" >&2; exit 1; }; : ;;
"pr close") [ -f "$D/close_fail" ] && { echo "GraphQL: close failed" >&2; exit 1; }; : ;;
"run rerun") [ -f "$D/rerun_fail" ] && { echo "HTTP 403: rerun failed" >&2; exit 1; }; : ;;
"workflow run") [ -f "$D/dispatch_fail" ] && { echo "HTTP 422: dispatch failed" >&2; exit 1; }; : ;;
api\ *)
  pg=1; case "$2" in *page=*) pg=${2##*page=} ;; esac
  sfx=""; [ "$pg" = 1 ] || sfx=".$pg"
  case "$2" in
  */check-runs/*/annotations*) failif annotations; cat "$D/annotations$sfx.json" 2>/dev/null || echo '[]' ;;
  */commits/*/check-runs*)
    [ -f "$D/check_runs_denied" ] && { echo "gh: Resource not accessible by personal access token (HTTP 403)" >&2; exit 1; }
    cat "$D/check-runs$sfx.json" 2>/dev/null || echo '{"total_count":0,"check_runs":[]}' ;;
  */commits/*/status*) failif status; cat "$D/status$sfx.json" 2>/dev/null || echo '{"state":"pending","total_count":0,"statuses":[]}' ;;
  */actions/runs*) failif workflow_runs; cat "$D/workflow-runs$sfx.json" 2>/dev/null || echo '{"total_count":0,"workflow_runs":[]}' ;;
  */compare/*) failif compare; cat "$D/compare.json" 2>/dev/null || echo '{"behind_by":0}' ;;
  *) echo "unexpected gh api $2" >&2; exit 2 ;;
  esac ;;
*) echo "unexpected gh $*" >&2; exit 2 ;;
esac
`

func installFakeGH(t *testing.T) string {
	t.Helper()
	dir, bin := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(strings.ReplaceAll(fakeGHScript, "%DIR%", dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func ghCalls(t *testing.T, dir, prefix string) []string {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(dir, "argv"))
	var out []string
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(ln, prefix) {
			out = append(out, ln)
		}
	}
	return out
}

// githubOrigin makes repo's origin read as github.com/acme/demo while git
// still reaches the local bare remote (insteadOf).
func githubOrigin(t *testing.T, repo string) {
	t.Helper()
	bare := gitIn(t, repo, "config", "--get", "remote.origin.url")
	gitIn(t, repo, "remote", "set-url", "origin", "https://github.com/acme/demo.git")
	gitIn(t, repo, "config", "url."+bare+".insteadOf", "https://github.com/acme/demo.git")
}

// writePRJSON writes PR #7 at head with rollup into the fake gh's file:
// pr.json (what pr create opens) or view.json (the open PR).
func writePRJSON(t *testing.T, dir, file, head, rollup string) {
	t.Helper()
	body := `{"number":7,"state":"OPEN","headRefOid":"` + head + `","mergeable":"MERGEABLE","statusCheckRollup":` + rollup + `,"baseRefName":"main"}`
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPRSnapshotFactsAndFailure: the snapshot feeds the facts with head_rel
// and head_since, each change is one log line (checks pending -> green),
// and a failed gh call is answered=false with the last snapshot kept.
func TestPRSnapshotFactsAndFailure(t *testing.T) {
	ctx := context.Background()
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := installFakeGH(t)
	paths := testPaths(t)
	eng := New(paths, nil)
	if err := eng.Start(repo, "Goal", 0); err != nil {
		t.Fatal(err)
	}
	id, s, err := eng.findProjectLocked(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "commit", "--allow-empty", "-m", "feat: x")
	c1 := gitIn(t, repo, "rev-parse", "HEAD")
	s.Tasks = []flow.Task{{ID: "t1", Title: "feat: x", EstimateLines: 50, Attempts: []flow.Attempt{{SizeCap: 100, BaseCommit: base, Branch: "task/t1", Worktree: repo, Ready: []flow.Ready{{Head: c1}}}}}}

	read := func() flow.PR { eng.mu.Lock(); defer eng.mu.Unlock(); return eng.readPRLocked(ctx, id, &s, 0) }
	writePRJSON(t, gh, "view.json", c1, `[{"__typename":"CheckRun","name":"check","status":"IN_PROGRESS","conclusion":""}]`)
	p1 := read()
	if !p1.Answered || p1.Number != 7 || p1.Head != c1 || p1.HeadRel != flow.RelSame || p1.Checks != flow.ChecksPending || p1.HeadSince.IsZero() {
		t.Fatalf("pending snapshot = %+v", p1)
	}
	if read() != p1 {
		t.Fatal("an unchanged snapshot changed")
	}
	writePRJSON(t, gh, "view.json", c1, `[{"__typename":"CheckRun","name":"check","status":"COMPLETED","conclusion":"SUCCESS"}]`)
	p2 := read()
	if p2.Checks != flow.ChecksGreen || !p2.HeadSince.Equal(p1.HeadSince) {
		t.Fatalf("green snapshot = %+v (head_since must stay %s)", p2, p1.HeadSince)
	}
	// A new PR head restarts head_since (the checks grace counts from it).
	gitIn(t, repo, "commit", "--allow-empty", "-m", "feat: y")
	c2 := gitIn(t, repo, "rev-parse", "HEAD")
	s.Tasks[0].Attempts[0].Ready = append(s.Tasks[0].Attempts[0].Ready, flow.Ready{Head: c2})
	writePRJSON(t, gh, "view.json", c2, `[{"__typename":"CheckRun","name":"check","status":"COMPLETED","conclusion":"SUCCESS"}]`)
	if p := read(); p.Head != c2 || p.HeadRel != flow.RelSame || !p.HeadSince.After(p2.HeadSince) {
		t.Fatalf("new head snapshot = %+v, want head_since after %s", p, p2.HeadSince)
	}
	var daemonLog bytes.Buffer
	eng.Log = logging.NewJSONLLogger(&daemonLog)
	if err := os.WriteFile(filepath.Join(gh, "fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if p3 := read(); p3.Answered || p3.State != flow.PROpen || p3.Head != c2 || p3.Checks != flow.ChecksUnreadable {
		t.Fatalf("failed snapshot = %+v, want answered=false, the last PR, checks unreadable (never the last green)", p3)
	}
	read()
	read()
	if n := strings.Count(daemonLog.String(), "factory.gh_error"); n != 1 {
		t.Fatalf("the same gh error was logged %d times, want once:\n%s", n, daemonLog.String())
	}
	logRaw, _ := os.ReadFile(paths.Log(id))
	if n := strings.Count(string(logRaw), `"rule":"pr_snapshot"`); n != 4 ||
		!strings.Contains(string(logRaw), `"checks":"pending"`) || !strings.Contains(string(logRaw), `"checks":"green"`) || !strings.Contains(string(logRaw), `"answered":false`) {
		t.Fatalf("log has %d snapshot lines:\n%s", n, logRaw)
	}
}

// TestGitHubUnavailableHoldsMerge verifies denied -> fallback -> unreadable ->
// github_unavailable holds N42, and a network failure is never remembered as
// "not a GitHub repo" (KB-GIT-20): once GitHub answers again, N42 merges.
func TestGitHubUnavailableHoldsMerge(t *testing.T) {
	ctx := context.Background()
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := installFakeGH(t)
	eng := New(testPaths(t), nil)
	if err := eng.Start(repo, "Goal", 0); err != nil {
		t.Fatal(err)
	}
	id, s, err := eng.findProjectLocked(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "commit", "--allow-empty", "-m", "feat: x")
	c1 := gitIn(t, repo, "rev-parse", "HEAD")
	s.Tasks = []flow.Task{{ID: "t1", Title: "feat: x", EstimateLines: 50, Attempts: []flow.Attempt{{
		SizeCap: 100, BaseCommit: base, Branch: "task/t1", Worktree: repo,
		Ready: []flow.Ready{{Head: c1}},
		Rounds: []flow.Round{
			{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
			{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
		},
	}}}}
	writePRJSON(t, gh, "view.json", c1, `[{"__typename":"CheckRun","name":"check","status":"COMPLETED","conclusion":"SUCCESS"}]`)

	eval := func() (flow.PR, bool, string, []string) {
		eng.mu.Lock()
		p := eng.readPRLocked(ctx, id, &s, 0)
		unavail := eng.ghUnavail[id]
		eng.mu.Unlock()
		r, d := flow.FirstMatch(flow.TaskRows, flow.In{
			Task: s.Tasks[0], Settings: s.Settings,
			Facts: flow.Facts{PR: p, Tip: c1, TipRel: flow.RelSame, Worker: flow.Run{State: flow.RunIdle}, GitHubUnavailable: unavail},
		})
		return p, unavail, r.ID, d.Asked
	}

	// Denied pr view + denied REST check-runs -> unreadable + github_unavailable holds merge.
	for _, f := range []string{"view_denied", "check_runs_denied"} {
		if err := os.WriteFile(filepath.Join(gh, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if p, unavail, row, asked := eval(); p.Checks != flow.ChecksUnreadable || !unavail || row != "N36" || len(asked) != 0 {
		t.Fatalf("both denied: checks=%s unavail=%t row=%s asked=%v", p.Checks, unavail, row, asked)
	}
	// Network failure also sets github_unavailable and holds merge (never marks non-GitHub).
	for _, f := range []string{"view_denied", "check_runs_denied"} {
		_ = os.Remove(filepath.Join(gh, f))
	}
	if err := os.WriteFile(filepath.Join(gh, "fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, unavail, row, asked := eval(); !unavail || row != "N36" || len(asked) != 0 {
		t.Fatalf("network failure: unavail=%t row=%s asked=%v", unavail, row, asked)
	}
	// Recovery clears github_unavailable and N42 merges.
	_ = os.Remove(filepath.Join(gh, "fail"))
	if p, unavail, row, asked := eval(); !p.Answered || p.Checks != flow.ChecksGreen || unavail || row != "N42" || len(asked) == 0 {
		t.Fatalf("after recovery: checks=%s answered=%t unavail=%t row=%s asked=%v", p.Checks, p.Answered, unavail, row, asked)
	}
	// A failing optional call (compare) is logged once and holds nothing.
	var daemonLog bytes.Buffer
	eng.Log = logging.NewJSONLLogger(&daemonLog)
	if err := os.WriteFile(filepath.Join(gh, "compare_fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if p, unavail, row, _ := eval(); !p.Answered || p.Checks != flow.ChecksGreen || unavail || row != "N42" {
			t.Fatalf("compare failing: checks=%s answered=%t unavail=%t row=%s", p.Checks, p.Answered, unavail, row)
		}
	}
	if n := strings.Count(daemonLog.String(), "factory.gh_partial"); n != 1 {
		t.Fatalf("a failing compare was logged %d times, want once:\n%s", n, daemonLog.String())
	}
}

// TestWorkflowRunsDeniedHoldsMerge: a persistent 403 on actions/runs never
// lets a green PR merge: it reads absent, N36 holds it in the checks grace
// and N43 wakes the coordinator after it, never N42.
func TestWorkflowRunsDeniedHoldsMerge(t *testing.T) {
	ctx := context.Background()
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := installFakeGH(t)
	eng := New(testPaths(t), nil)
	if err := eng.Start(repo, "Goal", 0); err != nil {
		t.Fatal(err)
	}
	id, s, err := eng.findProjectLocked(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "commit", "--allow-empty", "-m", "feat: x")
	c1 := gitIn(t, repo, "rev-parse", "HEAD")
	s.Tasks = []flow.Task{{ID: "t1", Title: "feat: x", EstimateLines: 50, Attempts: []flow.Attempt{{
		SizeCap: 100, BaseCommit: base, Branch: "task/t1", Worktree: repo, Ready: []flow.Ready{{Head: c1}},
		Rounds: []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass}, {N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass}},
	}}}}
	writePRJSON(t, gh, "view.json", c1, `[{"__typename":"CheckRun","name":"check","workflowName":"ci","status":"COMPLETED","conclusion":"SUCCESS","startedAt":"2026-11-20T10:00:00Z"}]`)
	if err := os.WriteFile(filepath.Join(gh, "workflow_runs_fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, age := range []time.Duration{time.Minute, time.Hour} {
		eng.mu.Lock()
		p := eng.readPRLocked(ctx, id, &s, 0)
		unavail := eng.ghUnavail[id]
		eng.mu.Unlock()
		r, d := flow.FirstMatch(flow.TaskRows, flow.In{Task: s.Tasks[0], Settings: s.Settings, Now: p.HeadSince.Add(age),
			Facts: flow.Facts{PR: p, Tip: c1, TipRel: flow.RelSame, Worker: flow.Run{State: flow.RunIdle}, GitHubUnavailable: unavail}})
		want := map[bool]string{true: "N36", false: "N43"}[age < time.Duration(s.Settings.ChecksGraceMinutes)*time.Minute]
		if p.Checks == flow.ChecksGreen || unavail || r.ID != want || slices.Contains(d.Asked, flow.AskMerge+"@"+c1) {
			t.Fatalf("head age %s: checks=%s unavail=%t row=%s asked=%v; want %s and no merge", age, p.Checks, unavail, r.ID, d.Asked, want)
		}
	}
}
