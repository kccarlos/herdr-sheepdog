package world

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// githubRepoRe takes OWNER/NAME from a github.com remote URL only: https,
// ssh:// or scp form (git@github.com:o/r). Any other host is refused.
var githubRepoRe = regexp.MustCompile(`^(?:(?:https|ssh)://(?:[^/@\s]+@)?github\.com/|(?:[^/@:\s]+@)?github\.com:)([\w.-]+/[\w.-]+?)(?:\.git)?/?$`)

// pushRemote is the remote the engine pushes to and names on every gh call.
const pushRemote = "origin"

var (
	ErrNoOrigin       = errors.New("no GitHub remote named origin")
	ErrOriginMismatch = errors.New("origin fetch and push URLs must all target the same github.com repo")
)

// GitHubOrigin reads effective URLs, including Git URL rewrites and all push targets.
func GitHubOrigin(read func(...string) (string, error)) (string, error) {
	fetch, err := read("remote", "get-url", "--all", pushRemote)
	if err != nil && !gitExit(err, 2) {
		return "", err
	}
	if fetch == "" {
		return "", Fail(flow.ClassSettled, ErrNoOrigin)
	}
	push, err := read("remote", "get-url", "--push", "--all", pushRemote)
	if err != nil {
		return "", err
	}
	var slug string
	for _, target := range strings.Split(fetch+"\n"+push, "\n") {
		m := githubRepoRe.FindStringSubmatch(target)
		if m == nil || slug != "" && !strings.EqualFold(slug, m[1]) {
			return "", Fail(flow.ClassSettled, ErrOriginMismatch)
		}
		slug = cmp.Or(slug, m[1])
	}
	return slug, nil
}

func (g *Git) ghRepo(ctx context.Context, dir string) (string, error) {
	return GitHubOrigin(func(args ...string) (string, error) { return g.gitCmd(ctx, dir, args...) })
}

// RunGH returns the captured stdout, stderr and exit status from herdr-core.
func (g *Git) RunGH(ctx context.Context, dir string, args ...string) (hc.ExecResult, error) {
	r, err := g.run(ctx, dir, "gh", args, map[string]string{
		"GH_PROMPT_DISABLED": "1", "GH_NO_UPDATE_NOTIFIER": "1", "NO_COLOR": "1",
		"GH_HOST": "github.com", "GH_REPO": "",
	})
	if gitExit(err, 4) {
		err = Fail(flow.ClassSettled, err)
	}
	return r, err
}

func (g *Git) ghCmd(ctx context.Context, dir string, args ...string) (string, error) {
	r, err := g.RunGH(ctx, dir, args...)
	return strings.TrimSpace(string(r.Stdout)), err
}

var ghOutage = regexp.MustCompile(`(?i)\bHTTP(?:/\d(?:\.\d)?)? (?:5\d\d|429)\b|rate limit|error connecting|dial (?:tcp|udp)|connection (?:refused|reset)|no such host|network (?:error|failure|is unreachable)|no route to host|i/o timeout|TLS handshake timeout|timeout awaiting response headers|context deadline exceeded|unexpected EOF`)

// GitHubUnavailable identifies a GitHub read failure reported by gh itself.
func GitHubUnavailable(err error) bool {
	if !gitExit(err, 1) {
		return false
	}
	var child *GitError
	return errors.As(err, &child) && child.command == "gh" && (ghOutage.MatchString(ghOutput(child)) || isChecksDenied(err))
}

func ghOutput(child *GitError) string {
	return string(child.Result.Stderr) + "\n" + string(child.Result.Stdout)
}

func snapshotInterrupted(err error) bool {
	var child *GitError
	return errors.As(err, &child) && (child.RPC != nil || child.Result.TimedOut || child.Result.Truncated ||
		child.Result.Signal != nil || child.Result.Code == nil || gitExit(err, 1) && ghOutage.MatchString(ghOutput(child)))
}

// maxPRTitleRunes is GitHub's PR title length limit (cut on a rune boundary).
const maxPRTitleRunes = 256

func capTitle(s string) string {
	if utf8.RuneCountInString(s) <= maxPRTitleRunes {
		return s
	}
	return string([]rune(s)[:maxPRTitleRunes])
}

// PushAndOpenPR pushes head to branch on the push remote (never forced: a
// branch that moved elsewhere refuses it) and makes sure one PR is open for
// branch against the remote base, decided by flow.PRAdoptRows (design.md 9.4,
// KB-GIT-2). It returns the PR number.
func (g *Git) PushAndOpenPR(ctx context.Context, worktree, branch, head, title, body string) (int, error) {
	repo, err := g.ghRepo(ctx, worktree)
	if err != nil {
		return 0, err
	}
	if _, err := g.gitCmd(ctx, worktree, "push", pushRemote, head+":refs/heads/"+branch); err != nil {
		return 0, err
	}
	ref, _, err := g.remoteBaseCommit(ctx, worktree, false)
	if err != nil {
		return 0, err
	}
	base := strings.TrimPrefix(ref, "refs/remotes/"+pushRemote+"/")
	// No headRefOid check: GitHub can lag on updating a PR's head SHA after a re-push.
	out, err := g.ghCmd(ctx, worktree, "pr", "list", "--repo", repo, "--head", branch, "--state", "all", "--json", "number,state,baseRefName,isCrossRepository")
	if err != nil {
		return 0, err
	}
	var prs []struct {
		Number             int
		State, BaseRefName string
		IsCrossRepository  bool
	}
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return 0, fmt.Errorf("world: gh pr list: %w", err)
	}
	cands := make([]flow.PRCandidate, len(prs))
	for i, p := range prs {
		cands[i] = flow.PRCandidate{
			Number: p.Number, State: flow.PRState(strings.ToLower(p.State)),
			Base: p.BaseRefName, IsCrossRepository: p.IsCrossRepository,
		}
	}
	switch action, n, why := flow.AdoptPR(flow.PRAdoptIn{Base: base, PRs: cands}); action {
	case flow.PRAdopt:
		return n, nil
	case flow.PRRefuse:
		return 0, Fail(flow.ClassSettled, fmt.Errorf("world: %s: %s", branch, why))
	}
	out, err = g.ghCmd(ctx, worktree, "pr", "create", "--repo", repo, "--head", branch, "--base", base, "--title", capTitle(title), "--body", body)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(out[strings.LastIndex(out, "/")+1:])
	if err != nil {
		return 0, fmt.Errorf("world: gh pr create printed %q", out)
	}
	return n, nil
}

const prViewFields = "number,state,headRefOid,mergeable,statusCheckRollup,baseRefName,mergeCommit,reviewDecision"

type rawPRView struct {
	Number                                                    int
	State, HeadRefOid, BaseRefName, Mergeable, ReviewDecision string
	StatusCheckRollup                                         []flow.Check
	MergeCommit                                               struct{ Oid string }
}

// PRSnapshot reads by recorded number if available, otherwise by branch
// (design.md 7.3). An unreadable recorded PR stays unanswered. If pr view
// cannot read the rollup, the snapshot tries REST check runs and statuses.
// Deadlines and interrupted requests invalidate the snapshot. Optional API
// refusals are returned in warn and keep the affected facts unknown.
func (g *Git) PRSnapshot(ctx context.Context, repoDir, branch string, number int) (p flow.PR, warn, err error) {
	ctx, cancel := context.WithTimeout(ctx, prSnapshotLimit)
	defer cancel()
	defer func() {
		if ctx.Err() != nil && err == nil {
			err = ctx.Err()
		}
		if err != nil {
			p.Answered, p.Checks = false, flow.ChecksUnreadable
		}
	}()
	repo, err := g.ghRepo(ctx, repoDir)
	if err != nil {
		return flow.PR{}, nil, err
	}
	baseRef, _, err := g.remoteBaseCommit(ctx, repoDir, false)
	if err != nil {
		return flow.PR{Checks: flow.ChecksUnreadable}, nil, err
	}
	base := strings.TrimPrefix(baseRef, "refs/remotes/"+pushRemote+"/")
	target := branch
	if number > 0 {
		target = strconv.Itoa(number)
	}
	checksDenied := false
	raw, err := g.ghCmd(ctx, repoDir, "pr", "view", target, "--repo", repo, "--json", prViewFields)
	if gitExit(err, 1) && !snapshotInterrupted(err) {
		if number <= 0 && (strings.Contains(err.Error(), "no pull requests found") || strings.Contains(err.Error(), "Could not resolve to a PullRequest")) {
			return flow.PR{Answered: true, State: flow.PRNone}, nil, nil
		}
		if checksDenied = isChecksDenied(err); checksDenied {
			raw, err = g.ghCmd(ctx, repoDir, "pr", "view", target, "--repo", repo, "--json", strings.Replace(prViewFields, "statusCheckRollup,", "", 1))
		}
	}
	if err != nil {
		return flow.PR{Checks: flow.ChecksUnreadable}, nil, err
	}
	v, p, err := decodeSnapshot([]byte(raw))
	if err != nil {
		return p, nil, err
	}
	if v.BaseRefName != base {
		if number > 0 {
			return flow.PR{Answered: true, Number: v.Number, State: flow.PRNone, OtherBase: cmp.Or(v.BaseRefName, "unknown")}, nil, nil
		}
		return flow.PR{Answered: true, State: flow.PRNone}, nil, nil
	}
	if p.State != flow.PROpen {
		p.Checks = flow.Rollup(v.StatusCheckRollup, false)
		return p, nil, nil
	}
	if checksDenied && p.Head != "" {
		if v.StatusCheckRollup, err = g.fetchCheckRuns(ctx, repoDir, repo, p.Head); err != nil {
			return p, nil, err
		}
	}
	var warns []error
	all := v.StatusCheckRollup // every check of H, before the dedupe: re-run eligibility reads them all
	for i, c := range all {
		all[i].Actions = c.Actions || !checksDenied && c.WorkflowName != "" // the rollup names only an Actions run's workflow; REST reads the app
		if id := flow.CheckRunID(c); id > 0 && strings.EqualFold(c.Status, "COMPLETED") && strings.EqualFold(c.Conclusion, "CANCELLED") {
			all[i].Stop, err = g.checkRunStop(ctx, repoDir, repo, id)
			if snapshotInterrupted(err) {
				return p, nil, err
			}
			warns = append(warns, err)
		}
	}
	checks := flow.LatestChecks(all)
	wfPending, failedRuns := false, map[string]bool{}
	if p.Head != "" { // H's own workflow runs: an unfinished one waits, and only a failed one is re-run
		if wfPending, failedRuns, err = g.headRuns(ctx, repoDir, repo, p.Head); err != nil {
			if snapshotInterrupted(err) {
				return p, nil, err
			}
			p.ChecksUnread, failedRuns = true, nil // N33/N34 never re-run on it, nor on the pages read before the error
			if base := flow.Rollup(checks, false); base == flow.ChecksGreen || base == flow.ChecksAbsent {
				checks = nil // never green unread: absent, N36 holds it in the grace, then N43
			}
		}
		warns = append(warns, err)
	}
	p.Checks = flow.Rollup(checks, wfPending)
	p.Failed, p.Running = flow.FailingRuns(all, p.Checks, repo, failedRuns)
	p.Running = p.Running || wfPending
	if v.BaseRefName != "" && p.Head != "" {
		var cmpResp struct {
			BehindBy int `json:"behind_by"`
		}
		err = g.ghAPI(ctx, repoDir, "repos/"+repo+"/compare/"+escapeRef(v.BaseRefName)+"..."+p.Head, &cmpResp)
		if snapshotInterrupted(err) {
			return p, nil, err
		}
		p.Behind, p.BehindUnknown = err == nil && cmpResp.BehindBy > 0, err != nil
		warns = append(warns, err)
	}
	return p, errors.Join(warns...), nil
}

// escapeRef escapes each segment of a branch name and keeps its slashes
// raw, as in the S0.6 spike's compare main...s06/behind (a slash on the
// head side; a slashed base is not verified live).
func escapeRef(ref string) string {
	segs := strings.Split(ref, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func isChecksDenied(err error) bool {
	s := err.Error()
	return strings.Contains(s, "Resource not accessible") || strings.Contains(s, "HTTP 403") || strings.Contains(s, "statusCheckRollup")
}

func (g *Git) ghAPI(ctx context.Context, dir, endpoint string, dst any) error {
	out, err := g.ghCmd(ctx, dir, "api", endpoint)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(out), dst); err != nil {
		return fmt.Errorf("world: gh api %s: %w", endpoint, err)
	}
	return nil
}

const maxPages = 10 // bounds a paged REST read

// ghPages reads endpoint per items a page (the endpoint's cap); page returns
// how many items one held. Over maxPages full pages is an error, never partial.
func (g *Git) ghPages(ctx context.Context, dir, endpoint string, per int, page func([]byte) (int, error)) error {
	sep := map[bool]string{false: "?", true: "&"}[strings.Contains(endpoint, "?")]
	for n := 1; n <= maxPages; n++ {
		out, err := g.ghCmd(ctx, dir, "api", fmt.Sprintf("%s%sper_page=%d&page=%d", endpoint, sep, per, n))
		if err != nil {
			return err
		}
		if got, err := page([]byte(out)); err != nil {
			return fmt.Errorf("world: gh api %s: %w", endpoint, err)
		} else if got < per {
			return nil
		}
	}
	return fmt.Errorf("world: gh api %s: more than %d pages", endpoint, maxPages)
}

func decodeSnapshot(data []byte) (rawPRView, flow.PR, error) {
	var v rawPRView
	if err := json.Unmarshal(data, &v); err != nil {
		return v, flow.PR{}, fmt.Errorf("world: gh pr view: %w", err)
	}
	state := flow.PRState(strings.ToLower(v.State))
	if state != flow.PROpen && state != flow.PRMerged && state != flow.PRClosed {
		return v, flow.PR{}, errors.New("world: gh pr view: unknown state " + v.State)
	}
	mergeable := map[string]flow.Mergeable{"MERGEABLE": flow.MergeableClean, "CONFLICTING": flow.MergeableConflict}[v.Mergeable]
	return v, flow.PR{
		Answered: true, Number: v.Number, State: state, Head: v.HeadRefOid, MergedCommit: v.MergeCommit.Oid,
		Mergeable: cmp.Or(mergeable, flow.MergeableUnknown), ReviewRequired: v.ReviewDecision == "REVIEW_REQUIRED",
	}, nil
}

// fetchCheckRuns is the REST fallback: every check run of head (keyed by
// its check suite) and every commit status.
func (g *Git) fetchCheckRuns(ctx context.Context, dir, repo, head string) ([]flow.Check, error) {
	var checks []flow.Check
	// filter=all: every attempt, so an earlier timed-out one still vetoes its run.
	err := g.ghPages(ctx, dir, "repos/"+repo+"/commits/"+head+"/check-runs?filter=all", 100, func(raw []byte) (int, error) {
		var resp struct {
			CheckRuns []struct {
				ID                       int64
				Name, Status, Conclusion string
				StartedAt                string             `json:"started_at"`
				DetailsURL               string             `json:"details_url"`
				CheckSuite               struct{ ID int64 } `json:"check_suite"`
				App                      struct{ Slug string }
			} `json:"check_runs"`
		}
		err := json.Unmarshal(raw, &resp)
		for _, cr := range resp.CheckRuns {
			checks = append(checks, flow.Check{Kind: "CheckRun", ID: cr.ID, Name: cr.Name, Status: cr.Status, Conclusion: cr.Conclusion,
				StartedAt: cr.StartedAt, DetailsURL: cr.DetailsURL, WorkflowName: fmt.Sprint("suite ", cr.CheckSuite.ID), Actions: cr.App.Slug == "github-actions"})
		}
		return len(resp.CheckRuns), err
	})
	if err != nil {
		return nil, err
	}
	err = g.ghPages(ctx, dir, "repos/"+repo+"/commits/"+head+"/status", 100, func(raw []byte) (int, error) {
		var resp struct {
			Statuses []struct{ Context, State string }
		}
		err := json.Unmarshal(raw, &resp)
		for _, st := range resp.Statuses {
			checks = append(checks, flow.Check{Kind: "StatusContext", Context: st.Context, State: st.State})
		}
		return len(resp.Statuses), err
	})
	return checks, err
}

// checkRunStop is why a run was cancelled, from its annotations (S0.6):
// "timeout" (the job's own time limit), "external" (a canceled-by line),
// else "unknown": no evidence either way, or unread (never re-run).
func (g *Git) checkRunStop(ctx context.Context, dir, repo string, id int64) (string, error) {
	stop := "unknown"
	err := g.ghPages(ctx, dir, fmt.Sprintf("repos/%s/check-runs/%d/annotations", repo, id), 50, func(raw []byte) (int, error) {
		var anns []struct{ Message string }
		err := json.Unmarshal(raw, &anns)
		for _, a := range anns {
			if strings.Contains(a.Message, "exceeded the maximum execution time") {
				stop = "timeout"
			} else if stop == "unknown" && strings.Contains(a.Message, "was canceled by") {
				stop = "external"
			}
		}
		return len(anns), err
	})
	if err != nil && stop != "timeout" {
		stop = "unknown"
	}
	return stop, err
}

// headRuns reads head's workflow runs: whether one is unfinished (seen
// even if a later page fails) and the ids of those completed with a
// failure or a cancel, the only runs N33/N34 may re-run.
func (g *Git) headRuns(ctx context.Context, dir, repo, head string) (bool, map[string]bool, error) {
	pending, failed := false, map[string]bool{}
	err := g.ghPages(ctx, dir, "repos/"+repo+"/actions/runs?head_sha="+head, 100, func(raw []byte) (int, error) {
		var resp struct {
			WorkflowRuns []struct {
				ID                 int64
				Status, Conclusion string
			} `json:"workflow_runs"`
		}
		err := json.Unmarshal(raw, &resp)
		for _, r := range resp.WorkflowRuns {
			done := strings.EqualFold(r.Status, "completed")
			pending = pending || !done
			failed[fmt.Sprint(r.ID)] = done && slices.Contains([]string{"failure", "cancelled", "timed_out"}, strings.ToLower(r.Conclusion))
		}
		return len(resp.WorkflowRuns), err
	})
	return pending, failed, err
}

// BaseRun is the push workflow runs result on the base branch's head commit.
type BaseRun struct {
	ID                          int64
	Branch, HeadSHA, Conclusion string
	Red, Known                  bool
}

// BaseStatus reads the base branch's head commit and its push workflow runs
// (design.md 7.3, N38, KB-MRG-47). Red if any completed run failed (failure,
// timed_out, or cancelled by job time limit); green when every run completed
// and none is red; unknown while any run is still in progress or when no run
// exists for that commit (path-ignored push). Read errors are returned so
// callers keep the last known state and never flip red.
func (g *Git) BaseStatus(ctx context.Context, repoDir string) (BaseRun, error) {
	repo, err := g.ghRepo(ctx, repoDir)
	if err != nil {
		return BaseRun{}, err
	}
	ref, _, err := g.remoteBaseCommit(ctx, repoDir, false)
	if err != nil {
		return BaseRun{}, err
	}
	base := strings.TrimPrefix(ref, "refs/remotes/"+pushRemote+"/")
	var headRef struct{ Object struct{ SHA string } }
	if err := g.ghAPI(ctx, repoDir, "repos/"+repo+"/git/ref/heads/"+escapeRef(base), &headRef); err != nil || headRef.Object.SHA == "" {
		return BaseRun{}, cmp.Or(err, fmt.Errorf("world: empty head sha for base %s", base))
	}
	head := headRef.Object.SHA
	red, firstID, firstConc, total, pending := BaseRun{}, int64(0), "", 0, false
	endpoint := "repos/" + repo + "/actions/runs?branch=" + url.QueryEscape(base) + "&event=push&head_sha=" + url.QueryEscape(head)
	err = g.ghPages(ctx, repoDir, endpoint, 100, func(raw []byte) (int, error) {
		var resp struct {
			WorkflowRuns []struct {
				ID                 int64
				Status, Conclusion string
			} `json:"workflow_runs"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return 0, err
		}
		for _, r := range resp.WorkflowRuns {
			total++
			if firstID == 0 {
				firstID, firstConc = r.ID, strings.ToLower(r.Conclusion)
			}
			if !strings.EqualFold(r.Status, "completed") {
				pending = true
				continue
			}
			if red.Red {
				continue
			}
			switch c := strings.ToLower(r.Conclusion); c {
			case "failure", "timed_out":
				red = BaseRun{ID: r.ID, Branch: base, HeadSHA: head, Conclusion: c, Red: true, Known: true}
			case "cancelled":
				if lim, err := g.runCancelledByLimit(ctx, repoDir, repo, r.ID); err != nil {
					return 0, err
				} else if lim {
					red = BaseRun{ID: r.ID, Branch: base, HeadSHA: head, Conclusion: "cancelled_timeout", Red: true, Known: true}
				}
			}
		}
		return len(resp.WorkflowRuns), nil
	})
	if err != nil {
		return BaseRun{}, err
	} else if red.Red {
		return red, nil
	} else if total == 0 || pending {
		return BaseRun{Branch: base, HeadSHA: head}, nil
	}
	return BaseRun{ID: firstID, Branch: base, HeadSHA: head, Conclusion: firstConc, Known: true}, nil
}

func (g *Git) runCancelledByLimit(ctx context.Context, dir, repo string, runID int64) (bool, error) {
	limited := false
	err := g.ghPages(ctx, dir, fmt.Sprintf("repos/%s/actions/runs/%d/jobs", repo, runID), 100, func(raw []byte) (int, error) {
		var resp struct {
			Jobs []struct {
				ID         int64
				Conclusion string
			}
		}
		err := json.Unmarshal(raw, &resp)
		for _, j := range resp.Jobs {
			if err == nil && strings.EqualFold(j.Conclusion, "cancelled") {
				var stop string
				stop, err = g.checkRunStop(ctx, dir, repo, j.ID)
				limited = limited || stop == "timeout"
			}
		}
		return len(resp.Jobs), err
	})
	return limited, err
}

// MergePR asks GitHub to squash-merge PR number, pinned to head: GitHub
// refuses it if the PR head moved. Never --admin, --auto or --delete-branch
// (design.md 9.4, KB-MRG-10). The squash body is the caller's, built from
// task fields only. Its result is not trusted: only a later snapshot that
// reads merged ends the task (N2, KB-MRG-9).
func (g *Git) MergePR(ctx context.Context, repoDir string, number int, head, body string) error {
	if number <= 0 || head == "" {
		return Fail(flow.ClassSettled, fmt.Errorf("world: merge needs a PR number and a head (got #%d at %q)", number, head))
	}
	repo, err := g.ghRepo(ctx, repoDir)
	if err != nil {
		return err
	}
	_, err = g.ghCmd(ctx, repoDir, "pr", "merge", strconv.Itoa(number), "--repo", repo, "--squash", "--match-head-commit", head, "--body", body)
	return err
}

// ClosePR closes PR number with a short comment and keeps its branch (never
// --delete-branch, never merges; design.md 4.1 N1, 4.9).
func (g *Git) ClosePR(ctx context.Context, repoDir string, number int, note string) error {
	if number <= 0 || strings.TrimSpace(note) == "" {
		return Fail(flow.ClassSettled, fmt.Errorf("world: close PR needs a PR number and a note (got #%d)", number))
	}
	repo, err := g.ghRepo(ctx, repoDir)
	if err != nil {
		return err
	}
	_, err = g.ghCmd(ctx, repoDir, "pr", "close", strconv.Itoa(number), "--repo", repo, "--comment", note)
	return err
}

// DeleteRemoteBranch deletes branch on the push remote. The caller calls it
// only once the PR reads merged, never as part of the merge (design.md
// 9.4). A branch already gone (GitHub may delete it on merge) is not an error.
func (g *Git) DeleteRemoteBranch(ctx context.Context, dir, branch string) error {
	if branch == "" {
		return Fail(flow.ClassSettled, errors.New("world: no branch to delete"))
	}
	if _, err := g.ghRepo(ctx, dir); err != nil {
		return err
	}
	out, err := g.gitCmd(ctx, dir, "ls-remote", "--heads", pushRemote, "refs/heads/"+branch)
	if err != nil || out == "" {
		return err
	}
	_, err = g.gitCmd(ctx, dir, "push", pushRemote, "--delete", "refs/heads/"+branch)
	return err
}

// ErrNoDispatchWorkflow is returned by DispatchWorkflow when no
// dispatch_workflow is set or its file is missing at head (N35 skips to
// waking the coordinator).
var ErrNoDispatchWorkflow = errors.New("world: no dispatch_workflow set, or its file is missing")

// RerunFailedJobs asks GitHub Actions to re-run the failed (or cancelled)
// jobs of one workflow run (design.md 9.4, N33, N34).
func (g *Git) RerunFailedJobs(ctx context.Context, repoDir, runID string) error {
	repo, err := g.ghRepo(ctx, repoDir)
	if err != nil {
		return err
	}
	_, err = g.ghCmd(ctx, repoDir, "run", "rerun", runID, "--failed", "--repo", repo)
	return err
}

// UpdateBranch asks GitHub to bring PR number up to date with its base using a
// merge commit, pinned to expectedHead (design.md 9.4, N40, KB-MRG-43).
func (g *Git) UpdateBranch(ctx context.Context, repoDir string, number int, expectedHead string) error {
	if number <= 0 || expectedHead == "" {
		return Fail(flow.ClassSettled, fmt.Errorf("world: update-branch needs a PR number and an expected head (got #%d at %q)", number, expectedHead))
	}
	repo, err := g.ghRepo(ctx, repoDir)
	if err != nil {
		return err
	}
	_, err = g.ghCmd(ctx, repoDir, "api", "-X", "PUT", "repos/"+repo+"/pulls/"+strconv.Itoa(number)+"/update-branch", "-f", "expected_head_sha="+expectedHead)
	return err
}

// DispatchWorkflow dispatches the owner-named workflow on branch, only if
// .github/workflows/<name> exists at head. Dispatch is opt-in by name: a
// dispatchable workflow can deploy or release (design.md 9.4, N35).
func (g *Git) DispatchWorkflow(ctx context.Context, worktree, branch, head, name string) error {
	if !flow.WorkflowFile.MatchString(name) { // also here: settings are reloaded from disk each pass
		return ErrNoDispatchWorkflow
	}
	repo, err := g.ghRepo(ctx, worktree)
	if err != nil {
		return err
	}
	if _, err := g.gitCmd(ctx, worktree, "cat-file", "-e", head+":.github/workflows/"+name); err != nil {
		if gitExit(err, 128) {
			return ErrNoDispatchWorkflow
		}
		return err
	}
	// The file's own dispatches endpoint: gh workflow run would resolve the
	// value as an id or a display name too.
	_, err = g.ghCmd(ctx, worktree, "api", "repos/"+repo+"/actions/workflows/"+name+"/dispatches", "-X", "POST", "-f", "ref="+branch)
	return err
}
