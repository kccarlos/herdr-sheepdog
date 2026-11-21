package world

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// fakeGHScript records each call's argv (one line, words joined by spaces)
// in $D/argv and answers from files in $D: "fail" makes every call fail,
// view.json is the branch's PR, listed by pr list (honouring --state open;
// pr create copies pr.json there). gh api serves NAME.json (page N:
// NAME.N.json), and $D/<call>_fail fails one call (annotations, status,
// workflow_runs, compare).
const fakeGHScript = `#!/bin/sh
D=%DIR%
printf '%s\n' "$*" >> "$D/argv"
[ -f "$D/fail" ] && { echo "HTTP 502: Bad Gateway" >&2; exit 1; }
failif() { [ -f "$D/$1_fail" ] && { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }; return 0; }
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
"run rerun") [ -f "$D/rerun_fail" ] && { echo "HTTP 403: rerun failed" >&2; exit 1; }; : ;;
"workflow run") [ -f "$D/dispatch_fail" ] && { echo "HTTP 422: dispatch failed" >&2; exit 1; }; : ;;
api\ *)
  case "$*" in *"/pulls/"*"/update-branch"*) failif update_branch; :; exit 0 ;; esac
  pg=1; case "$2" in *"&page="*) pg=${2##*&page=} ;; esac
  sfx=""; [ "$pg" = 1 ] || sfx=".$pg"
  case "$2" in
  */actions/workflows/*/dispatches) failif dispatch; : ;;
  */check-runs/*/annotations*) failif annotations; cat "$D/annotations$sfx.json" 2>/dev/null || echo '[]' ;;
  */commits/*/check-runs*)
    [ -f "$D/check_runs_denied" ] && { echo "gh: Resource not accessible by personal access token (HTTP 403)" >&2; exit 1; }
    f="$D/check-runs$sfx.json"; case "$2" in *filter=all*) [ -f "$D/check-runs.all$sfx.json" ] && f="$D/check-runs.all$sfx.json" ;; esac
    cat "$f" 2>/dev/null || echo '{"total_count":0,"check_runs":[]}' ;;
  */commits/*/status*) failif status; cat "$D/status$sfx.json" 2>/dev/null || echo '{"state":"pending","total_count":0,"statuses":[]}' ;;
  */git/ref/heads/*) failif base_ref; cat "$D/base-ref.json" 2>/dev/null || echo '{"object":{"sha":"deadbeef1234567890"}}' ;;
  */actions/runs/*/jobs*) failif run_jobs; cat "$D/run-jobs$sfx.json" 2>/dev/null || echo '{"jobs":[]}' ;;
  */actions/runs*branch=*) failif base_runs; cat "$D/base-runs$sfx.json" 2>/dev/null || echo '{"workflow_runs":[]}' ;;
  */actions/runs*) failif workflow_runs; cat "$D/workflow-runs$sfx.json" 2>/dev/null || echo '{"total_count":0,"workflow_runs":[]}' ;;
  */compare/*) failif compare; cat "$D/compare.json" 2>/dev/null || echo '{"behind_by":0}' ;;
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

func TestChecksRollupTable(t *testing.T) {
	run := func(status, conclusion string) flow.Check {
		return flow.Check{Kind: "CheckRun", Status: status, Conclusion: conclusion}
	}
	ctx := func(state string) flow.Check { return flow.Check{Kind: "StatusContext", State: state} }
	ok, skip, neutral := run("COMPLETED", "SUCCESS"), run("COMPLETED", "SKIPPED"), run("COMPLETED", "NEUTRAL")
	for name, c := range map[string]struct {
		checks    []flow.Check
		wfPending bool
		want      flow.Checks
	}{
		"empty":                    {nil, false, flow.ChecksAbsent},
		"only skipped and neutral": {[]flow.Check{skip, neutral}, false, flow.ChecksAbsent},
		"success with skipped":     {[]flow.Check{ok, skip, neutral}, false, flow.ChecksGreen},
		"workflow unfinished":      {[]flow.Check{ok}, true, flow.ChecksPending},
		"queued":                   {[]flow.Check{ok, run("QUEUED", "")}, false, flow.ChecksPending},
		"skipped and in progress":  {[]flow.Check{skip, run("IN_PROGRESS", "")}, false, flow.ChecksPending},
		"failure beats pending":    {[]flow.Check{run("IN_PROGRESS", ""), run("COMPLETED", "FAILURE")}, false, flow.ChecksRed},
		"timed_out":                {[]flow.Check{ok, run("COMPLETED", "TIMED_OUT")}, false, flow.ChecksStoppedTimeout},
		"cancelled by timeout":     {[]flow.Check{ok, {Kind: "CheckRun", Status: "COMPLETED", Conclusion: "CANCELLED", Stop: "timeout"}}, false, flow.ChecksStoppedTimeout},
		"cancelled, cause unread":  {[]flow.Check{ok, {Kind: "CheckRun", Status: "COMPLETED", Conclusion: "CANCELLED", Stop: "unknown"}}, false, flow.ChecksStoppedUnknown},
		"unread beats outside":     {[]flow.Check{{Kind: "CheckRun", Status: "COMPLETED", Conclusion: "CANCELLED", Stop: "external"}, {Kind: "CheckRun", Status: "COMPLETED", Conclusion: "CANCELLED", Stop: "unknown"}}, false, flow.ChecksStoppedUnknown},
		"cancelled from outside":   {[]flow.Check{ok, {Kind: "CheckRun", Status: "COMPLETED", Conclusion: "CANCELLED", Stop: "external"}}, false, flow.ChecksStoppedExternal},
		"cancelled, no evidence":   {[]flow.Check{ok, run("COMPLETED", "CANCELLED")}, false, flow.ChecksStoppedUnknown},
		"cancelled replaced": {[]flow.Check{
			{Kind: "CheckRun", Name: "check", Status: "COMPLETED", Conclusion: "CANCELLED", StartedAt: "2026-11-20T09:00:00Z"},
			{Kind: "CheckRun", Name: "check", Status: "COMPLETED", Conclusion: "SUCCESS", StartedAt: "2026-11-20T09:05:00Z"},
		}, false, flow.ChecksGreen},
		"unknown conclusion":                  {[]flow.Check{run("COMPLETED", "STALE")}, false, flow.ChecksRed},
		"a check run never replaces a status": {[]flow.Check{{Kind: "StatusContext", Context: "ci", State: "PENDING"}, {Kind: "CheckRun", Name: "ci", Status: "COMPLETED", Conclusion: "SUCCESS"}}, false, flow.ChecksPending},
		"status success":                      {[]flow.Check{ctx("SUCCESS")}, false, flow.ChecksGreen},
		"status pending":                      {[]flow.Check{ok, ctx("PENDING")}, false, flow.ChecksPending},
		"status expected":                     {[]flow.Check{ctx("EXPECTED")}, false, flow.ChecksPending},
		"status error":                        {[]flow.Check{ok, ctx("ERROR")}, false, flow.ChecksRed},
	} {
		if got := flow.Rollup(c.checks, c.wfPending); got != c.want {
			t.Errorf("%s: Rollup = %s, want %s", name, got, c.want)
		}
	}
}

// TestNoChecksNeverMerges is KB-MRG-15: a passed task with an open,
// mergeable PR at H and no checks at all is never merged, inside or past
// the checks grace. The same task with green checks is merged.
func TestNoChecksNeverMerges(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	for _, class := range []string{"absent", "green"} {
		data, _ := os.ReadFile(filepath.Join("testdata", "snapshots", class+".json"))
		v, p, err := decodeSnapshot(data)
		if err != nil {
			t.Fatal(err)
		}
		p.Checks = flow.Rollup(v.StatusCheckRollup, false)
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
	if p, _, err := PRSnapshot(ctx, repo, "task/t1-x", 0); err != nil || !p.Answered || p.State != flow.PRNone {
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
	p, _, err := PRSnapshot(ctx, repo, "task/t1-x", 7)
	if err != nil || !reflect.DeepEqual(p, flow.PR{Answered: true, Number: 7, State: flow.PROpen, Head: c1, Mergeable: flow.MergeableUnknown, Checks: flow.ChecksAbsent}) {
		t.Fatalf("snapshot = %+v, %v", p, err)
	}
	if views := ghCalls(t, gh, "pr view"); !strings.Contains(views[len(views)-1], "pr view 7 --repo acme/demo --json "+prViewFields) {
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
	if p, _, err := PRSnapshot(ctx, repo, "task/t1-x", 7); err == nil || p.Answered {
		t.Fatalf("failed gh call gave %+v, %v", p, err)
	}
}

func TestGHRepoIsRequired(t *testing.T) {
	installFakeGH(t)
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "remote", "add", "origin", "/srv/git/demo.git")
	if _, _, err := PRSnapshot(context.Background(), repo, "b", 0); err == nil || !strings.Contains(err.Error(), "not a github.com repo") {
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
		// Before its run exists a PR reads absent; N36's checks grace holds it.
		{"pending_before_run", "pending-before-run.json", "pending-before-run.workflow-runs.json", "", flow.ChecksAbsent, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh := installFakeGH(t)
			copySnap(t, c.view, filepath.Join(gh, "view.json"))
			copySnap(t, c.workflow, filepath.Join(gh, "workflow-runs.json"))
			if c.annotations != "" {
				copySnap(t, c.annotations, filepath.Join(gh, "annotations.json"))
			}
			p, warn, err := PRSnapshot(ctx, repo, "s06/"+c.name, 0)
			if err != nil || warn != nil {
				t.Fatalf("PRSnapshot: %v, %v", err, warn)
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
	p, _, err := PRSnapshot(ctx, repo, "s06/green", 0)
	if err != nil || !p.Answered || p.Checks != flow.ChecksGreen {
		t.Fatalf("fallback snapshot = %+v, %v; want answered green", p, err)
	}
	if len(ghCalls(t, gh, "api repos/acme/demo/commits/")) != 2 {
		t.Fatalf("expected a REST check-runs and a status call, got %v", ghCalls(t, gh, ""))
	}
	// REST check-runs also denied -> unreadable + error.
	if err := os.WriteFile(filepath.Join(gh, "check_runs_denied"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p, _, err = PRSnapshot(ctx, repo, "s06/green", 0)
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
	p, _, err := PRSnapshot(ctx, repo, "s06/behind", 0)
	if err != nil || p.Behind || p.ReviewRequired {
		t.Fatalf("before mover: %+v, %v; want Behind=false ReviewRequired=false", p, err)
	}
	copySnap(t, "behind.json", filepath.Join(gh, "view.json"))
	copySnap(t, "behind.compare.json", filepath.Join(gh, "compare.json"))
	p, _, err = PRSnapshot(ctx, repo, "s06/behind", 0)
	if err != nil || !p.Behind || p.Checks != flow.ChecksGreen {
		t.Fatalf("after mover: %+v, %v; want Behind=true green", p, err)
	}
	// compare is base...head; a slashed base keeps its slash raw, as the
	// S0.6 spike called compare main...s06/behind.
	calls := ghCalls(t, gh, "api repos/acme/demo/compare/")
	if want := "api repos/acme/demo/compare/main..." + p.Head; calls[len(calls)-1] != want {
		t.Fatalf("compare argv = %q, want %q", calls[len(calls)-1], want)
	}
	raw, err := os.ReadFile(filepath.Join(gh, "view.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeGH(t, gh, "view.json", strings.Replace(string(raw), `"baseRefName": "main"`, `"baseRefName": "release/1.0"`, 1))
	// A PR on another base than origin/HEAD is treated as not ours (PRNone).
	if pOther, _, err := PRSnapshot(ctx, repo, "s06/behind", 0); err != nil || !pOther.Answered || pOther.State != flow.PRNone {
		t.Fatalf("other-base PR = %+v, %v; want answered PRNone", pOther, err)
	}
	// When the project base itself is release/1.0, compare keeps its slash raw.
	runGit(t, repo, "update-ref", "refs/remotes/origin/release/1.0", "refs/remotes/origin/main")
	runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/release/1.0")
	if _, _, err := PRSnapshot(ctx, repo, "s06/behind", 0); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	calls = ghCalls(t, gh, "api repos/acme/demo/compare/")
	if want := "api repos/acme/demo/compare/release/1.0..." + p.Head; calls[len(calls)-1] != want {
		t.Fatalf("compare argv = %q, want %q", calls[len(calls)-1], want)
	}
	raw, err = os.ReadFile(filepath.Join("testdata", "snapshots", "green.json"))
	if err != nil {
		t.Fatal(err)
	}
	rev := strings.Replace(string(raw), `"reviewDecision": ""`, `"reviewDecision": "REVIEW_REQUIRED"`, 1)
	if err := os.WriteFile(filepath.Join(gh, "view.json"), []byte(rev), 0o600); err != nil {
		t.Fatal(err)
	}
	p, _, err = PRSnapshot(ctx, repo, "s06/review", 0)
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
	p, _, err := PRSnapshot(ctx, repo, "s06/green", 0)
	if err == nil || p.Checks != flow.ChecksUnreadable || strings.Contains(err.Error(), "not a github.com repo") {
		t.Fatalf("network failure = %+v, %v", p, err)
	}
	if err := os.Remove(failFile); err != nil {
		t.Fatal(err)
	}
	p, _, err = PRSnapshot(ctx, repo, "s06/green", 0)
	if err != nil || !p.Answered || p.Checks != flow.ChecksGreen {
		t.Fatalf("after recovery = %+v, %v; want answered green", p, err)
	}
}

func writeGH(t *testing.T, gh, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(gh, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPRSnapshotNoCheckHidesAnother: same-named checks of two workflows are
// two checks, and a queued re-run (gh prints its startedAt as year 1)
// replaces the run it reruns, in either order.
func TestPRSnapshotNoCheckHidesAnother(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	run := func(wf, status, concl, started string) string {
		return fmt.Sprintf(`{"__typename":"CheckRun","name":"test","workflowName":%q,"status":%q,"conclusion":%q,"startedAt":%q}`, wf, status, concl, started)
	}
	failed, queued := run("ci", "COMPLETED", "FAILURE", "2026-11-20T10:00:05Z"), run("ci", "QUEUED", "", "0001-01-01T00:00:00Z")
	for name, c := range map[string]struct {
		rollup string
		want   flow.Checks
	}{
		"failure in ci, later success in e2e": {failed + "," + run("e2e", "COMPLETED", "SUCCESS", "2026-11-20T10:00:09Z"), flow.ChecksRed},
		"queued re-run after the failure":     {failed + "," + queued, flow.ChecksPending},
		"queued re-run before the failure":    {queued + "," + failed, flow.ChecksPending},
		"queued re-run of a cancelled run":    {run("ci", "COMPLETED", "CANCELLED", "2026-11-20T10:00:05Z") + "," + queued, flow.ChecksPending},
	} {
		gh := installFakeGH(t)
		writeGH(t, gh, "view.json", `{"number":7,"state":"OPEN","headRefOid":"h1","mergeable":"MERGEABLE","baseRefName":"main","statusCheckRollup":[`+c.rollup+`]}`)
		if p, _, err := PRSnapshot(ctx, repo, "b", 0); err != nil || p.Checks != c.want {
			t.Errorf("%s: checks = %s, %v; want %s", name, p.Checks, err, c.want)
		}
	}
}

// TestPRSnapshotFallbackReadsEverything: the REST fallback follows
// check-run pages and reads commit statuses, so a red check on page 2 or a
// status-only gate counts; more pages than the bound is unreadable, never a
// partial green.
func TestPRSnapshotFallbackReadsEverything(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	page := func(from, n int, concl string) string {
		runs := make([]string, n)
		for i := range runs {
			runs[i] = fmt.Sprintf(`{"id":%d,"name":"c%d","status":"completed","conclusion":%q,"check_suite":{"id":1}}`, from+i, from+i, concl)
		}
		return `{"total_count":2000,"check_runs":[` + strings.Join(runs, ",") + `]}`
	}
	full := map[string]string{"check-runs.json": page(100, 100, "success")}
	for i := 2; i <= 20; i++ {
		full[fmt.Sprintf("check-runs.%d.json", i)] = page(i*100, 100, "success")
	}
	for name, c := range map[string]struct {
		files map[string]string
		want  flow.Checks
	}{
		"red on page 2":    {map[string]string{"check-runs.json": page(1, 100, "success"), "check-runs.2.json": page(500, 1, "failure")}, flow.ChecksRed},
		"status-only gate": {map[string]string{"status.json": `{"state":"failure","total_count":1,"statuses":[{"context":"ci/jenkins","state":"failure"}]}`}, flow.ChecksRed},
		"status pending":   {map[string]string{"check-runs.json": page(1, 1, "success"), "status.json": `{"state":"pending","total_count":1,"statuses":[{"context":"ci/jenkins","state":"pending"}]}`}, flow.ChecksPending},
		"too many pages":   {full, flow.ChecksUnreadable},
	} {
		gh := installFakeGH(t)
		copySnap(t, "green.json", filepath.Join(gh, "view.json"))
		writeGH(t, gh, "view_denied", "")
		for f, body := range c.files {
			writeGH(t, gh, f, body)
		}
		if p, _, _ := PRSnapshot(ctx, repo, "s06/green", 0); p.Checks != c.want {
			t.Errorf("%s: checks = %s, want %s", name, p.Checks, c.want)
		}
	}
}

// TestPRSnapshotOptionalCallFails: a failed annotations, workflow-runs or
// compare call never makes the snapshot unreadable. A cancelled run whose
// annotations cannot be read is stopped_unknown (N34 treats it as a time
// limit: the coordinator is told, nothing is re-run); a failed workflow-runs
// read is never green (absent: N36 holds it in the grace, then N43); a
// failed compare leaves Behind false with BehindUnknown set.
func TestPRSnapshotOptionalCallFails(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	for _, c := range []struct {
		fail, view, workflow, extra string
		want                        flow.Checks
	}{
		{"annotations", "stopped_timeout.json", "stopped_timeout.workflow-runs.json", "stopped_timeout.annotations.json", flow.ChecksStoppedUnknown},
		{"workflow_runs", "absent.json", "", "", flow.ChecksAbsent},
		{"workflow_runs", "green.json", "", "", flow.ChecksAbsent},
		{"compare", "behind.json", "green.workflow-runs.json", "behind.compare.json", flow.ChecksGreen},
	} {
		gh := installFakeGH(t)
		copySnap(t, c.view, filepath.Join(gh, "view.json"))
		if c.workflow != "" {
			copySnap(t, c.workflow, filepath.Join(gh, "workflow-runs.json"))
		}
		if c.extra != "" {
			copySnap(t, c.extra, filepath.Join(gh, strings.TrimPrefix(filepath.Ext(strings.TrimSuffix(c.extra, ".json")), ".")+".json"))
		}
		writeGH(t, gh, c.fail+"_fail", "")
		p, warn, err := PRSnapshot(ctx, repo, "b", 0)
		if err != nil || warn == nil || !p.Answered || p.Checks != c.want || p.Behind || p.BehindUnknown != (c.fail == "compare") || p.ChecksUnread != (c.fail == "workflow_runs") {
			t.Errorf("%s fails on %s: %+v, warn %v, err %v; want answered %s, not behind, a warning", c.fail, c.view, p, warn, err, c.want)
		}
	}
}

// TestCheckRunsRoundTwo: a completed check without a start time never hides
// a failure of the same check, and annotations are paged 50 at a time (the
// endpoint's cap), so a timeout line on page 2 is read.
func TestCheckRunsRoundTwo(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)
	writeGH(t, gh, "view.json", `{"number":7,"state":"OPEN","headRefOid":"h1","mergeable":"MERGEABLE","baseRefName":"main","statusCheckRollup":[`+
		`{"__typename":"CheckRun","name":"test","workflowName":"ci","status":"COMPLETED","conclusion":"SUCCESS"},`+
		`{"__typename":"CheckRun","name":"test","workflowName":"ci","status":"COMPLETED","conclusion":"FAILURE","startedAt":"2026-11-20T10:00:05Z"}]}`)
	if p, _, err := PRSnapshot(ctx, repo, "b", 0); err != nil || p.Checks != flow.ChecksRed {
		t.Errorf("completed success without a start beside a failure: %s, %v; want red", p.Checks, err)
	}
	gh = installFakeGH(t)
	copySnap(t, "stopped_timeout.json", filepath.Join(gh, "view.json"))
	copySnap(t, "stopped_timeout.workflow-runs.json", filepath.Join(gh, "workflow-runs.json"))
	writeGH(t, gh, "annotations.json", "["+strings.Repeat(`{"message":"warning"},`, 49)+`{"message":"warning"}]`)
	copySnap(t, "stopped_timeout.annotations.json", filepath.Join(gh, "annotations.2.json"))
	if p, _, err := PRSnapshot(ctx, repo, "b", 0); err != nil || p.Checks != flow.ChecksStoppedTimeout {
		t.Errorf("timeout annotation on page 2: %s, %v; want stopped_timeout", p.Checks, err)
	}
}

func TestRerunFailedJobsAndDispatchWorkflow(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)

	// RerunFailedJobs calls gh run rerun <id> --failed --repo acme/demo.
	if err := RerunFailedJobs(ctx, repo, "101"); err != nil {
		t.Fatal(err)
	}
	wantReruns := []string{"run rerun 101 --failed --repo acme/demo"}
	if got := ghCalls(t, gh, "run rerun"); !slices.Equal(got, wantReruns) {
		t.Fatalf("rerun calls = %q, want %q", got, wantReruns)
	}

	// DispatchWorkflow is opt-in by name: empty never dispatches, a named
	// file missing at head never does, a named file present is dispatched.
	head := runGit(t, repo, "rev-parse", "HEAD")
	for _, name := range []string{"", "ci.yml"} {
		if err := DispatchWorkflow(ctx, repo, "task/t1", head, name); !errors.Is(err, ErrNoDispatchWorkflow) {
			t.Fatalf("name %q before the file err = %v, want ErrNoDispatchWorkflow", name, err)
		}
	}
	wfDir := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "ci.yml"), []byte("on:\n  pull_request:\n  workflow_dispatch:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".github/workflows/ci.yml")
	runGit(t, repo, "commit", "-m", "ci: add dispatch")
	head = runGit(t, repo, "rev-parse", "HEAD")
	if err := DispatchWorkflow(ctx, repo, "task/t1", head, ""); !errors.Is(err, ErrNoDispatchWorkflow) {
		t.Fatalf("empty name with a dispatchable file err = %v, want ErrNoDispatchWorkflow", err)
	}
	if err := DispatchWorkflow(ctx, repo, "task/t1", head, "ci.yml"); err != nil {
		t.Fatal(err)
	}
	if got := ghCalls(t, gh, "api repos/acme/demo/actions/workflows/"); !slices.Equal(got, []string{"api repos/acme/demo/actions/workflows/ci.yml/dispatches -X POST -f ref=task/t1"}) {
		t.Fatalf("dispatch calls = %q, want the file's own dispatches endpoint", got)
	}
}

// TestDispatchWorkflowExactFileOnly: a value gh could read as a workflow id,
// a display name or a flag is never dispatched, even if a file by that name
// exists at head (the guard runs where the command runs).
func TestDispatchWorkflowExactFileOnly(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)
	wfDir := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	names := []string{"12345", "ci", "-x.yml"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(wfDir, n), []byte("on: workflow_dispatch\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", ".github")
	runGit(t, repo, "commit", "-m", "ci")
	head := runGit(t, repo, "rev-parse", "HEAD")
	for _, n := range append(names, "a/b.yml") {
		if err := DispatchWorkflow(ctx, repo, "task/t1", head, n); !errors.Is(err, ErrNoDispatchWorkflow) {
			t.Errorf("DispatchWorkflow(%q) err = %v, want ErrNoDispatchWorkflow", n, err)
		}
	}
	if got := append(ghCalls(t, gh, "workflow run"), ghCalls(t, gh, "api repos/acme/demo/actions/workflows/")...); len(got) != 0 {
		t.Fatalf("dispatched %q", got)
	}
}

// TestPRSnapshotPartialRunListNoTarget: when H's run list fails on a later
// page, the ids read on earlier pages are not used: no re-run target.
func TestPRSnapshotPartialRunListNoTarget(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)
	writeGH(t, gh, "view.json", `{"number":7,"state":"OPEN","headRefOid":"h1","mergeable":"MERGEABLE","baseRefName":"main","statusCheckRollup":[`+
		`{"__typename":"CheckRun","name":"test","workflowName":"ci","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://github.com/acme/demo/actions/runs/5/job/50"}]}`)
	runs := []string{`{"id":5,"status":"completed","conclusion":"failure"}`}
	for i := 1; i < 100; i++ {
		runs = append(runs, fmt.Sprintf(`{"id":%d,"status":"completed","conclusion":"success"}`, 1000+i))
	}
	writeGH(t, gh, "workflow-runs.json", `{"total_count":150,"workflow_runs":[`+strings.Join(runs, ",")+`]}`)
	writeGH(t, gh, "workflow-runs.2.json", `not json`)
	p, warn, err := PRSnapshot(ctx, repo, "b", 0)
	if err != nil || warn == nil || p.Checks != flow.ChecksRed || !p.ChecksUnread || len(p.Failed) != 1 || p.Failed[0].Run != "" {
		t.Fatalf("partial run list: %+v, warn %v, err %v; want red, checks_unread, a failure with no run", p, warn, err)
	}
}

// TestPRSnapshotFallbackSeesEarlierAttempts: the REST fallback asks for
// every attempt (filter=all), so a timed-out earlier attempt of a job that
// was re-run still vetoes re-running its run.
func TestPRSnapshotFallbackSeesEarlierAttempts(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)
	writeGH(t, gh, "view.json", `{"number":7,"state":"OPEN","headRefOid":"h1","mergeable":"MERGEABLE","baseRefName":"main","statusCheckRollup":[]}`)
	writeGH(t, gh, "view_denied", "")
	cr := func(id int, name, concl string) string {
		return fmt.Sprintf(`{"id":%d,"name":%q,"status":"completed","conclusion":%q,"details_url":"https://github.com/acme/demo/actions/runs/90/job/%d","check_suite":{"id":1},"app":{"slug":"github-actions"}}`, id, name, concl, id)
	}
	writeGH(t, gh, "check-runs.json", `{"total_count":2,"check_runs":[`+cr(900, "build", "failure")+","+cr(902, "report", "success")+`]}`)
	writeGH(t, gh, "check-runs.all.json", `{"total_count":3,"check_runs":[`+cr(900, "build", "failure")+","+cr(901, "report", "cancelled")+","+cr(902, "report", "success")+`]}`)
	writeGH(t, gh, "annotations.json", `[{"message":"The job has exceeded the maximum execution time of 1m0s"}]`)
	writeGH(t, gh, "workflow-runs.json", `{"total_count":1,"workflow_runs":[{"id":90,"status":"completed","conclusion":"failure"}]}`)
	p, _, err := PRSnapshot(ctx, repo, "b", 0)
	if err != nil || p.Checks != flow.ChecksRed || len(p.Failed) != 1 || p.Failed[0].Run != "" {
		t.Fatalf("fallback with an earlier timed-out attempt: %+v, %v; want red with no re-run target", p, err)
	}
}

func TestUpdateBranch(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)
	if err := UpdateBranch(ctx, repo, 0, "abc"); err == nil {
		t.Fatal("UpdateBranch(0, abc) succeeded")
	}
	if err := UpdateBranch(ctx, repo, 7, ""); err == nil {
		t.Fatal("UpdateBranch(7, empty) succeeded")
	}
	if err := UpdateBranch(ctx, repo, 7, "abc1234"); err != nil {
		t.Fatalf("UpdateBranch: %v", err)
	}
	got := ghCalls(t, gh, "api -X PUT")
	want := "api -X PUT repos/acme/demo/pulls/7/update-branch -f expected_head_sha=abc1234"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("UpdateBranch calls = %q, want [%q]", got, want)
	}
	writeGH(t, gh, "update_branch_fail", "")
	if err := UpdateBranch(ctx, repo, 7, "abc1234"); err == nil {
		t.Fatal("UpdateBranch succeeded when fake gh failed")
	}
}

func TestBaseStatus(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)

	// Empty runs list (path-ignored push) -> unknown, not red.
	got, err := BaseStatus(ctx, repo)
	if err != nil || got.Known || got.Red || got.Branch != "main" {
		t.Fatalf("empty = %+v, %v", got, err)
	}
	wantCall := "api repos/acme/demo/actions/runs?branch=main&event=push&head_sha=deadbeef1234567890&per_page=100&page=1"
	if calls := ghCalls(t, gh, "api repos/acme/demo/actions/runs"); len(calls) != 1 || calls[0] != wantCall {
		t.Fatalf("calls = %q, want [%q]", calls, wantCall)
	}

	// Failure and timed_out count red; success counts green.
	for _, tc := range []struct {
		name, body string
		red, known bool
		id         int64
	}{
		{"failure", `{"workflow_runs":[{"id":11,"status":"completed","conclusion":"failure"}]}`, true, true, 11},
		{"timed_out", `{"workflow_runs":[{"id":12,"status":"completed","conclusion":"timed_out"}]}`, true, true, 12},
		{"success", `{"workflow_runs":[{"id":13,"status":"completed","conclusion":"success"}]}`, false, true, 13},
		{"skipped and failure", `{"workflow_runs":[{"id":14,"status":"completed","conclusion":"skipped"},{"id":15,"status":"completed","conclusion":"failure"}]}`, true, true, 15},
	} {
		writeGH(t, gh, "base-runs.json", tc.body)
		got, err := BaseStatus(ctx, repo)
		if err != nil || got.Red != tc.red || got.Known != tc.known || got.ID != tc.id {
			t.Errorf("%s: got %+v, %v; want red=%v known=%v id=%d", tc.name, got, err, tc.red, tc.known, tc.id)
		}
	}

	// Cancelled by job time limit counts red; external cancel + success counts green.
	writeGH(t, gh, "base-runs.json", `{"workflow_runs":[{"id":20,"status":"completed","conclusion":"cancelled"},{"id":19,"status":"completed","conclusion":"success"}]}`)
	writeGH(t, gh, "run-jobs.json", `{"jobs":[{"id":501,"conclusion":"cancelled"}]}`)
	writeGH(t, gh, "annotations.json", `[{"message":"The job has exceeded the maximum execution time of 60m0s"}]`)
	if got, err := BaseStatus(ctx, repo); err != nil || !got.Red || !got.Known || got.ID != 20 {
		t.Fatalf("cancelled by limit = %+v, %v; want red id=20", got, err)
	}
	writeGH(t, gh, "annotations.json", `[{"message":"The run was canceled by @someone."}]`)
	if got, err := BaseStatus(ctx, repo); err != nil || got.Red || !got.Known {
		t.Fatalf("cancelled externally = %+v, %v; want green", got, err)
	}

	// Read error returns err (never flips red).
	writeGH(t, gh, "base_runs_fail", "")
	if _, err := BaseStatus(ctx, repo); err == nil {
		t.Fatal("BaseStatus succeeded on base_runs_fail")
	}
}

func TestBaseStatusHeadCommit(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)
	const head = "1111222233334444555566667777888899990000"
	writeGH(t, gh, "base-ref.json", `{"object":{"sha":"`+head+`"}}`)

	// Passing workflow listed first cannot hide a failing workflow on the same head commit.
	writeGH(t, gh, "base-runs.json", `{"workflow_runs":[{"id":202,"head_sha":"`+head+`","status":"completed","conclusion":"success"},{"id":201,"head_sha":"`+head+`","status":"completed","conclusion":"failure"}]}`)
	got, err := BaseStatus(ctx, repo)
	if err != nil || !got.Red || !got.Known || got.HeadSHA != head {
		t.Fatalf("two workflows (pass then fail) = %+v, %v; want red known head=%s", got, err, head)
	}
	wantSub := "actions/runs?branch=main&event=push&head_sha=" + head
	if calls := ghCalls(t, gh, "api repos/acme/demo/actions/runs"); len(calls) == 0 || !strings.Contains(calls[len(calls)-1], wantSub) || strings.Contains(calls[len(calls)-1], "status=completed") {
		t.Fatalf("runs query = %q, want %q without status=completed", calls, wantSub)
	}

	// Re-run in progress (or another workflow still running) with no red run yet -> unknown (keep last value).
	writeGH(t, gh, "base-runs.json", `{"workflow_runs":[{"id":201,"head_sha":"`+head+`","status":"in_progress","conclusion":""},{"id":202,"head_sha":"`+head+`","status":"completed","conclusion":"success"}]}`)
	got, err = BaseStatus(ctx, repo)
	if err != nil || got.Known || got.Red {
		t.Fatalf("in-progress rerun alongside pass = %+v, %v; want unknown (keep last value)", got, err)
	}

	// Paged runs: 100 passing runs on page 1, 1 failing run on page 2 -> red.
	var p1 strings.Builder
	p1.WriteString(`{"workflow_runs":[`)
	for i := range 100 {
		if i > 0 {
			p1.WriteByte(',')
		}
		fmt.Fprintf(&p1, `{"id":%d,"head_sha":"%s","status":"completed","conclusion":"success"}`, 1000+i, head)
	}
	p1.WriteString(`]}`)
	writeGH(t, gh, "base-runs.json", p1.String())
	writeGH(t, gh, "base-runs.2.json", `{"workflow_runs":[{"id":2000,"head_sha":"`+head+`","status":"completed","conclusion":"failure"}]}`)
	got, err = BaseStatus(ctx, repo)
	if err != nil || !got.Red || !got.Known {
		t.Fatalf("paged runs (fail on page 2) = %+v, %v; want red known", got, err)
	}
}

// TestPushAndOpenPRTitleCap verifies that PR titles longer than GitHub's 256
// rune limit are cut on a rune boundary.
func TestPushAndOpenPRTitleCap(t *testing.T) {
	ctx := context.Background()
	gh := installFakeGH(t)
	repo, _ := repoWithGitHubOrigin(t)
	runGit(t, repo, "switch", "-c", "task/t1-cap")
	runGit(t, repo, "commit", "--allow-empty", "-m", "feat: cap")
	c1 := runGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(gh, "pr.json"), []byte(`{"number":7,"state":"OPEN","headRefOid":"`+c1+`","baseRefName":"main","headRefName":"task/t1-cap"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	longTitle := "feat: " + strings.Repeat("é", 294) // 300 runes (>300 bytes)
	if n, err := PushAndOpenPR(ctx, repo, "task/t1-cap", c1, longTitle, "body"); err != nil || n != 7 {
		t.Fatalf("PushAndOpenPR = %d, %v", n, err)
	}
	creates := ghCalls(t, gh, "pr create")
	wantTitle := "feat: " + strings.Repeat("é", 250)
	if len(creates) != 1 || !strings.Contains(creates[0], "--title "+wantTitle+" --body") {
		t.Fatalf("pr create = %q, want title cut to 256 runes (%d runes)", creates, len([]rune(wantTitle)))
	}
}

// TestRollupNewerRunReplacesOlderCancelled verifies design 4.3: an older
// cancelled workflow run is replaced by a newer passing run of the same
// workflow and check name on one head.
func TestRollupNewerRunReplacesOlderCancelled(t *testing.T) {
	checks := []flow.Check{
		{Kind: "CheckRun", Name: "check", WorkflowName: "ci", Status: "COMPLETED", Conclusion: "CANCELLED", Stop: "unknown", StartedAt: "2026-11-20T09:00:00Z", DetailsURL: "https://github.com/acme/demo/actions/runs/100/job/1001"},
		{Kind: "CheckRun", Name: "check", WorkflowName: "ci", Status: "COMPLETED", Conclusion: "SUCCESS", StartedAt: "2026-11-20T09:05:00Z", DetailsURL: "https://github.com/acme/demo/actions/runs/200/job/2001"},
	}
	if got := flow.Rollup(checks, false); got != flow.ChecksGreen {
		t.Fatalf("older cancelled run + newer passing run Rollup = %s, want green", got)
	}
}
