package world

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// fakeGHScript records each call's argv (one line, words joined by spaces)
// in $D/argv and answers from files in $D: "fail" makes every call fail,
// view.json is the branch's PR, listed by pr list (honouring --state open;
// pr create copies pr.json there).
const fakeGHScript = `#!/bin/sh
D=%DIR%
printf '%s\n' "$*" >> "$D/argv"
[ -f "$D/fail" ] && { echo "HTTP 502: Bad Gateway" >&2; exit 1; }
case "$*" in *"--state open"*) grep -q '"state":"OPEN"' "$D/view.json" 2>/dev/null || { echo '[]'; exit 0; } ;; esac
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
api\ *)
  case "$2" in
  */check-runs/*/annotations) cat "$D/annotations.json" 2>/dev/null || echo '[]' ;;
  */commits/*/check-runs)
    [ -f "$D/check_runs_denied" ] && { echo "gh: Resource not accessible by personal access token (HTTP 403)" >&2; exit 1; }
    cat "$D/check-runs.json" 2>/dev/null || echo '{"total_count":0,"check_runs":[]}' ;;
  */actions/runs*) cat "$D/workflow-runs.json" 2>/dev/null || echo '{"total_count":0,"workflow_runs":[]}' ;;
  */compare/*) cat "$D/compare.json" 2>/dev/null || echo '{"behind_by":0}' ;;
  *) echo "unexpected gh api $2" >&2; exit 2 ;;
  esac ;;
*) echo "unexpected gh $*" >&2; exit 2 ;;
esac
`

// installFakeGH puts a fake gh first on PATH and returns its state dir.
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

// repoWithGitHubOrigin is a temporary repo whose origin reads as
// github.com/acme/demo but is rewritten (insteadOf) to a local bare remote.
func repoWithGitHubOrigin(t *testing.T) (repo, bare string) {
	t.Helper()
	tmp := t.TempDir()
	bare, repo = filepath.Join(tmp, "remote.git"), filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "init", "--initial-branch=main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	runGit(t, repo, "remote", "add", "origin", "https://github.com/acme/demo.git")
	runGit(t, repo, "config", "url."+bare+".insteadOf", "https://github.com/acme/demo.git")
	runGit(t, repo, "commit", "--allow-empty", "-m", "chore: init")
	runGit(t, repo, "push", "-u", "origin", "main")
	runGit(t, repo, "fetch", "origin")
	return repo, bare
}

func TestChecksRollupS06Fixtures(t *testing.T) {
	for file, want := range map[string]flow.Checks{
		"prfull-red.json":     flow.ChecksRed,
		"prfull-pending.json": flow.ChecksPending,
		"prfull-green.json":   flow.ChecksGreen,
		"prfull-none.json":    flow.ChecksAbsent,
	} {
		data, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		p, err := parseSnapshot(data)
		if err != nil {
			t.Fatal(err)
		}
		if p.Checks != want || !p.Answered || p.State != flow.PROpen || p.Mergeable != flow.MergeableClean || p.Number < 10 || len(p.Head) != 40 {
			t.Errorf("%s parsed as %+v, want checks=%s", file, p, want)
		}
	}
}

func TestChecksRollupTable(t *testing.T) {
	run := func(status, conclusion string) flow.Check {
		return flow.Check{Kind: "CheckRun", Status: status, Conclusion: conclusion}
	}
	ctx := func(state string) flow.Check { return flow.Check{Kind: "StatusContext", State: state} }
	ok, skip, neutral := run("COMPLETED", "SUCCESS"), run("COMPLETED", "SKIPPED"), run("COMPLETED", "NEUTRAL")
	grace := 5 * time.Minute
	for name, c := range map[string]struct {
		in   flow.RollupInput
		want flow.Checks
	}{
		"unreadable beats all":     {flow.RollupInput{Unreadable: true, Checks: []flow.Check{ok}}, flow.ChecksUnreadable},
		"empty past grace":         {flow.RollupInput{HeadAge: grace, Grace: grace}, flow.ChecksAbsent},
		"empty in grace":           {flow.RollupInput{HeadAge: time.Minute, Grace: grace}, flow.ChecksPending},
		"only skipped and neutral": {flow.RollupInput{Checks: []flow.Check{skip, neutral}, HeadAge: time.Minute, Grace: grace}, flow.ChecksAbsent},
		"success with skipped":     {flow.RollupInput{Checks: []flow.Check{ok, skip, neutral}}, flow.ChecksGreen},
		"workflow unfinished":      {flow.RollupInput{Checks: []flow.Check{ok}, WorkflowPending: true}, flow.ChecksPending},
		"queued":                   {flow.RollupInput{Checks: []flow.Check{ok, run("QUEUED", "")}}, flow.ChecksPending},
		"skipped and in progress":  {flow.RollupInput{Checks: []flow.Check{skip, run("IN_PROGRESS", "")}}, flow.ChecksPending},
		"failure beats pending":    {flow.RollupInput{Checks: []flow.Check{run("IN_PROGRESS", ""), run("COMPLETED", "FAILURE")}}, flow.ChecksRed},
		"timed_out":                {flow.RollupInput{Checks: []flow.Check{ok, run("COMPLETED", "TIMED_OUT")}}, flow.ChecksStoppedTimeout},
		"cancelled by timeout":     {flow.RollupInput{Checks: []flow.Check{ok, {Kind: "CheckRun", Status: "COMPLETED", Conclusion: "CANCELLED", TimedOut: true}}}, flow.ChecksStoppedTimeout},
		"cancelled from outside":   {flow.RollupInput{Checks: []flow.Check{ok, run("COMPLETED", "CANCELLED")}}, flow.ChecksStoppedExternal},
		"cancelled replaced": {flow.RollupInput{Checks: []flow.Check{
			{Kind: "CheckRun", Name: "check", Status: "COMPLETED", Conclusion: "CANCELLED", StartedAt: "2026-11-20T09:00:00Z"},
			{Kind: "CheckRun", Name: "check", Status: "COMPLETED", Conclusion: "SUCCESS", StartedAt: "2026-11-20T09:05:00Z"},
		}}, flow.ChecksGreen},
		"unknown conclusion": {flow.RollupInput{Checks: []flow.Check{run("COMPLETED", "STALE")}}, flow.ChecksRed},
		"status success":     {flow.RollupInput{Checks: []flow.Check{ctx("SUCCESS")}}, flow.ChecksGreen},
		"status pending":     {flow.RollupInput{Checks: []flow.Check{ok, ctx("PENDING")}}, flow.ChecksPending},
		"status expected":    {flow.RollupInput{Checks: []flow.Check{ctx("EXPECTED")}}, flow.ChecksPending},
		"status error":       {flow.RollupInput{Checks: []flow.Check{ok, ctx("ERROR")}}, flow.ChecksRed},
	} {
		if got := flow.Rollup(c.in); got != c.want {
			t.Errorf("%s: Rollup = %s, want %s", name, got, c.want)
		}
	}
}

// TestNoChecksNeverMerges is KB-MRG-15: a passed task with an open,
// mergeable PR at H and no checks at all is never merged, inside or past
// the checks grace. The same task with green checks is merged.
func TestNoChecksNeverMerges(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	for _, class := range []string{"none", "green"} {
		data, _ := os.ReadFile(filepath.Join("testdata", "prfull-"+class+".json"))
		p, err := parseSnapshot(data)
		if err != nil {
			t.Fatal(err)
		}
		p.HeadRel = flow.RelSame
		task := flow.Task{ID: "t1", Attempts: []flow.Attempt{{BaseCommit: "b0", Ready: []flow.Ready{{Head: p.Head}},
			Rounds: []flow.Round{
				{N: 1, Head: p.Head, Verdict: flow.VerdictPass},
				{N: 2, Head: p.Head, Verdict: flow.VerdictPass},
			}}}}
		for _, age := range []time.Duration{time.Minute, time.Hour} {
			p.HeadSince = now.Add(-age)
			in := flow.In{Task: task, Settings: flow.DefaultSettings(), Now: now,
				Facts: flow.Facts{PR: p, Tip: p.Head, TipRel: flow.RelSame, Worker: flow.Run{State: flow.RunIdle}}}
			r, d := flow.FirstMatch(flow.TaskRows, in)
			merges := slices.Contains(d.Asked, flow.AskMerge+"@"+p.Head)
			if merges != (class == "green") {
				t.Errorf("checks %s, head age %s: row %s asked %v", class, age, r.ID, d.Asked)
			}
		}
	}
}

func TestPushAndOpenPROnceThenAdopts(t *testing.T) {
	ctx := context.Background()
	gh := installFakeGH(t)
	repo, bare := repoWithGitHubOrigin(t)
	runGit(t, repo, "switch", "-c", "task/t1-x")
	runGit(t, repo, "commit", "--allow-empty", "-m", "feat: x")
	c1 := runGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(gh, "pr.json"), []byte(`{"number":7,"state":"OPEN","headRefOid":"`+c1+`","mergeable":"UNKNOWN","statusCheckRollup":[],"baseRefName":"main"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Before: no PR is an answer, not a failure.
	if p, err := PRSnapshot(ctx, repo, "task/t1-x"); err != nil || !p.Answered || p.State != flow.PRNone {
		t.Fatalf("snapshot before PR = %+v, %v", p, err)
	}
	for i := range 2 {
		n, err := PushAndOpenPR(ctx, repo, "task/t1-x", c1, "feat: x", "Brief.\n\nOpened by the sheepdog factory.")
		if err != nil || n != 7 {
			t.Fatalf("call %d: PushAndOpenPR = %d, %v", i+1, n, err)
		}
	}
	if got := runGit(t, bare, "rev-parse", "refs/heads/task/t1-x"); got != c1 {
		t.Fatalf("remote branch = %s, want %s", got, c1)
	}
	creates := ghCalls(t, gh, "pr create")
	if len(creates) != 1 || len(ghCalls(t, gh, "pr list")) != 2 || !strings.Contains(creates[0], "--repo acme/demo --head task/t1-x --base main --title feat: x --body Brief.") {
		t.Fatalf("gh calls:\n%s", strings.Join(ghCalls(t, gh, ""), "\n"))
	}
	p, err := PRSnapshot(ctx, repo, "task/t1-x")
	if err != nil || p != (flow.PR{Answered: true, Number: 7, State: flow.PROpen, Head: c1, Mergeable: flow.MergeableUnknown, Checks: flow.ChecksAbsent}) {
		t.Fatalf("snapshot = %+v, %v", p, err)
	}
	if views := ghCalls(t, gh, "pr view"); !strings.Contains(views[len(views)-1], "pr view task/t1-x --repo acme/demo --json "+prViewFields) {
		t.Fatalf("pr view argv = %q", views)
	}
	if rel, err := Relation(ctx, repo, c1, runGit(t, repo, "rev-parse", "main")); err != nil || rel != flow.RelBehind {
		t.Fatalf("Relation(main vs c1) = %s, %v", rel, err)
	}

	// Never forced: a branch that moved on the remote refuses the push and stays.
	runGit(t, repo, "switch", "-c", "task/t2", "main")
	runGit(t, repo, "commit", "--allow-empty", "-m", "feat: mine")
	mine := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "commit", "--allow-empty", "--amend", "-m", "feat: theirs")
	runGit(t, repo, "push", "origin", "HEAD:refs/heads/task/t2") // someone else's commit
	theirs := runGit(t, repo, "rev-parse", "HEAD")
	if _, err := PushAndOpenPR(ctx, repo, "task/t2", mine, "feat: t2", "b"); err == nil {
		t.Fatal("push over a diverged remote branch succeeded")
	}
	if got := runGit(t, bare, "rev-parse", "refs/heads/task/t2"); got != theirs {
		t.Fatalf("remote task/t2 = %s, want untouched %s", got, theirs)
	}
	if len(ghCalls(t, gh, "pr create")) != 1 {
		t.Fatal("a refused push still opened a PR")
	}

	// A failed gh call is an error (the engine marks it unanswered).
	if err := os.WriteFile(filepath.Join(gh, "fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := PRSnapshot(ctx, repo, "task/t1-x"); err == nil || p.Answered {
		t.Fatalf("failed gh call gave %+v, %v", p, err)
	}
}

func TestGHRepoIsRequired(t *testing.T) {
	installFakeGH(t)
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "remote", "add", "origin", "/srv/git/demo.git")
	if _, err := PRSnapshot(context.Background(), repo, "b"); err == nil || !strings.Contains(err.Error(), "not a github.com repo") {
		t.Fatalf("PRSnapshot without a GitHub remote: %v", err)
	}
}

// TestGHRepoHosts: OWNER/NAME only from a github.com host; lookalikes and
// github.com elsewhere in the URL are refused.
func TestGHRepoHosts(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/acme/demo.git":           "acme/demo",
		"https://github.com/acme/demo":               "acme/demo",
		"https://user@github.com/acme/demo.git":      "acme/demo",
		"ssh://git@github.com/acme/demo.git":         "acme/demo",
		"git@github.com:acme/demo.git":               "acme/demo",
		"git@github.com:acme/demo":                   "acme/demo",
		"https://github.com.evil.example/acme/demo":  "",
		"git@github.com.evil.example:acme/demo.git":  "",
		"https://notgithub.com/acme/demo.git":        "",
		"git@notgithub.com:acme/demo.git":            "",
		"https://gitlab.com/github.com/acme/demo":    "",
		"https://x.example/github.com/acme/demo.git": "",
		"/srv/git/github.com/acme/demo.git":          "",
		"https://github.com/acme/demo/extra":         "",
	} {
		repo := t.TempDir()
		runGit(t, repo, "init")
		runGit(t, repo, "remote", "add", "origin", url)
		got, err := ghRepo(context.Background(), repo)
		if got != want || (err == nil) != (want != "") {
			t.Errorf("ghRepo(%s) = %q, %v; want %q", url, got, err, want)
		}
	}
}

// TestPushAndOpenPRRefusesOtherPRs: a branch whose PR is closed, merged,
// on another base, from a fork or at another head gets no second PR and
// nothing is adopted (design.md 9.4, N4).
func TestPushAndOpenPRRefusesOtherPRs(t *testing.T) {
	ctx := context.Background()
	gh := installFakeGH(t)
	repo, _ := repoWithGitHubOrigin(t)
	runGit(t, repo, "switch", "-c", "task/t1-x")
	runGit(t, repo, "commit", "--allow-empty", "-m", "feat: x")
	c1 := runGit(t, repo, "rev-parse", "HEAD")
	for name, pr := range map[string]string{
		"closed":     `"state":"CLOSED","baseRefName":"main","headRefOid":"` + c1 + `"`,
		"merged":     `"state":"MERGED","baseRefName":"main","headRefOid":"` + c1 + `"`,
		"wrong base": `"state":"OPEN","baseRefName":"release","headRefOid":"` + c1 + `"`,
		"fork":       `"state":"OPEN","baseRefName":"main","headRefOid":"` + c1 + `","isCrossRepository":true`,
	} {
		if err := os.WriteFile(filepath.Join(gh, "view.json"), []byte(`{"number":7,`+pr+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if n, err := PushAndOpenPR(ctx, repo, "task/t1-x", c1, "feat: x", "b"); err == nil || !strings.Contains(err.Error(), "no second PR") {
			t.Errorf("%s: PushAndOpenPR = %d, %v; want refused", name, n, err)
		}
	}
	if creates := ghCalls(t, gh, "pr create"); len(creates) != 0 {
		t.Fatalf("a branch with a PR got another: %q", creates)
	}
	// After our push, GitHub may still list the previous head (it lags a
	// re-push in a later round): our own open same-repo same-base PR is
	// adopted, not refused.
	lag := `"state":"OPEN","baseRefName":"main","headRefOid":"0000000000000000000000000000000000000000"`
	if err := os.WriteFile(filepath.Join(gh, "view.json"), []byte(`{"number":7,`+lag+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := PushAndOpenPR(ctx, repo, "task/t1-x", c1, "feat: x", "b"); err != nil || n != 7 {
		t.Fatalf("lagging head: PushAndOpenPR = %d, %v; want PR #7 adopted", n, err)
	}
	if creates := ghCalls(t, gh, "pr create"); len(creates) != 0 {
		t.Fatalf("lagging head opened a second PR: %q", creates)
	}
}

func copySnap(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "snapshots", src))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPRSnapshotS06Classes reads every checks class from its recorded real
// S0.6 snapshot (testdata/snapshots/).
func TestPRSnapshotS06Classes(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	cases := []struct {
		name, view, workflow, annotations string
		want                              flow.Checks
		wantAnn                           bool
	}{
		{"green", "green.json", "green.workflow-runs.json", "", flow.ChecksGreen, false},
		{"red", "red.json", "red.workflow-runs.json", "", flow.ChecksRed, false},
		{"stopped_timeout", "stopped_timeout.json", "stopped_timeout.workflow-runs.json", "stopped_timeout.annotations.json", flow.ChecksStoppedTimeout, true},
		{"stopped_external", "stopped_external.json", "stopped_external.workflow-runs.json", "stopped_external.annotations.json", flow.ChecksStoppedExternal, true},
		{"pending", "pending.json", "pending.workflow-runs.json", "", flow.ChecksPending, false},
		{"workflow_unfinished", "green.json", "pending.workflow-runs.json", "", flow.ChecksPending, false},
		{"absent", "absent.json", "absent.workflow-runs.json", "", flow.ChecksAbsent, false},
		{"absent_skipped", "absent-skipped.json", "absent-skipped.workflow-runs.json", "", flow.ChecksAbsent, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh := installFakeGH(t)
			copySnap(t, c.view, filepath.Join(gh, "view.json"))
			copySnap(t, c.workflow, filepath.Join(gh, "workflow-runs.json"))
			if c.annotations != "" {
				copySnap(t, c.annotations, filepath.Join(gh, "annotations.json"))
			}
			p, err := PRSnapshot(ctx, repo, "s06/"+c.name)
			if err != nil {
				t.Fatalf("PRSnapshot: %v", err)
			}
			if p.Checks != c.want || !p.Answered {
				t.Fatalf("PRSnapshot checks=%s answered=%t, want %s true", p.Checks, p.Answered, c.want)
			}
			annCalled := len(ghCalls(t, gh, "api repos/acme/demo/check-runs/")) > 0
			if annCalled != c.wantAnn {
				t.Fatalf("annotations called=%t, want %t", annCalled, c.wantAnn)
			}
		})
	}
	// pending-before-run vs absent vs absent-skipped with checks_grace_minutes.
	grace := time.Duration(flow.DefaultSettings().ChecksGraceMinutes) * time.Minute
	for _, c := range []struct {
		file string
		age  time.Duration
		want flow.Checks
	}{
		{"pending-before-run.json", 10 * time.Second, flow.ChecksPending},
		{"absent.json", grace, flow.ChecksAbsent},
		{"absent-skipped.json", 10 * time.Second, flow.ChecksAbsent},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "snapshots", c.file))
		if err != nil {
			t.Fatal(err)
		}
		v, _, err := decodeSnapshot(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := flow.Rollup(flow.RollupInput{Checks: v.StatusCheckRollup, HeadAge: c.age, Grace: grace}); got != c.want {
			t.Errorf("%s (age %v): Rollup = %s, want %s", c.file, c.age, got, c.want)
		}
	}
}

// TestPRSnapshotDeniedFallbackAndUnreadable verifies denied pr view -> REST
// check-runs fallback, and both denied -> ChecksUnreadable with error.
func TestPRSnapshotDeniedFallbackAndUnreadable(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)
	copySnap(t, "green.json", filepath.Join(gh, "view.json"))
	copySnap(t, "green.check-runs.json", filepath.Join(gh, "check-runs.json"))
	copySnap(t, "green.workflow-runs.json", filepath.Join(gh, "workflow-runs.json"))
	if err := os.WriteFile(filepath.Join(gh, "view_denied"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Fallback to REST check-runs succeeds.
	p, err := PRSnapshot(ctx, repo, "s06/green")
	if err != nil || !p.Answered || p.Checks != flow.ChecksGreen {
		t.Fatalf("fallback snapshot = %+v, %v; want answered green", p, err)
	}
	if len(ghCalls(t, gh, "api repos/acme/demo/commits/")) != 1 {
		t.Fatalf("expected 1 REST check-runs call, got %v", ghCalls(t, gh, ""))
	}
	// REST check-runs also denied -> unreadable + error.
	if err := os.WriteFile(filepath.Join(gh, "check_runs_denied"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err = PRSnapshot(ctx, repo, "s06/green")
	if err == nil || p.Answered || p.Checks != flow.ChecksUnreadable {
		t.Fatalf("both denied = %+v, %v; want unreadable + err", p, err)
	}
}

// TestPRSnapshotBehindAndReviewRequired reads Behind from the real compare
// capture (not mergeStateStatus) and ReviewRequired from reviewDecision.
func TestPRSnapshotBehindAndReviewRequired(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)
	copySnap(t, "behind-before.json", filepath.Join(gh, "view.json"))
	copySnap(t, "behind-before.compare.json", filepath.Join(gh, "compare.json"))
	p, err := PRSnapshot(ctx, repo, "s06/behind")
	if err != nil || p.Behind || p.ReviewRequired {
		t.Fatalf("before mover: %+v, %v; want Behind=false ReviewRequired=false", p, err)
	}
	copySnap(t, "behind.json", filepath.Join(gh, "view.json"))
	copySnap(t, "behind.compare.json", filepath.Join(gh, "compare.json"))
	p, err = PRSnapshot(ctx, repo, "s06/behind")
	if err != nil || !p.Behind || p.Checks != flow.ChecksGreen {
		t.Fatalf("after mover: %+v, %v; want Behind=true green", p, err)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "snapshots", "green.json"))
	if err != nil {
		t.Fatal(err)
	}
	rev := strings.Replace(string(raw), `"reviewDecision": ""`, `"reviewDecision": "REVIEW_REQUIRED"`, 1)
	if err := os.WriteFile(filepath.Join(gh, "view.json"), []byte(rev), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err = PRSnapshot(ctx, repo, "s06/review")
	if err != nil || !p.ReviewRequired {
		t.Fatalf("review_required: %+v, %v; want ReviewRequired=true", p, err)
	}
}

// TestPRSnapshotNetworkFailureNotNonGitHub is KB-GIT-20: a network outage
// returns unreadable and is never remembered as "not a GitHub repo".
func TestPRSnapshotNetworkFailureNotNonGitHub(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)
	copySnap(t, "green.json", filepath.Join(gh, "view.json"))
	copySnap(t, "green.workflow-runs.json", filepath.Join(gh, "workflow-runs.json"))
	failFile := filepath.Join(gh, "fail")
	if err := os.WriteFile(failFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := PRSnapshot(ctx, repo, "s06/green")
	if err == nil || p.Checks != flow.ChecksUnreadable || strings.Contains(err.Error(), "not a github.com repo") {
		t.Fatalf("network failure = %+v, %v", p, err)
	}
	if err := os.Remove(failFile); err != nil {
		t.Fatal(err)
	}
	p, err = PRSnapshot(ctx, repo, "s06/green")
	if err != nil || !p.Answered || p.Checks != flow.ChecksGreen {
		t.Fatalf("after recovery = %+v, %v; want answered green", p, err)
	}
}
