package world

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// githubRepoRe takes OWNER/NAME from a github.com remote URL only: https,
// ssh:// or scp form (git@github.com:o/r). Any other host is refused.
var githubRepoRe = regexp.MustCompile(`^(?:(?:https|ssh)://(?:[^/@\s]+@)?github\.com/|(?:[^/@:\s]+@)?github\.com:)([\w.-]+/[\w.-]+?)(?:\.git)?/?$`)

// pushRemote is the remote the engine pushes to and names on every gh call.
const pushRemote = "origin"

// ghRepo is OWNER/NAME of the push remote, read from its configured URL
// (before any insteadOf rewrite). A gh call with no repo is refused
// (design.md 9.4).
func ghRepo(ctx context.Context, dir string) (string, error) {
	url, _ := gitCmd(ctx, dir, "config", "--get", "remote."+pushRemote+".url")
	m := githubRepoRe.FindStringSubmatch(url)
	if m == nil {
		return "", fmt.Errorf("world: remote %s of %s is not a github.com repo (%q)", pushRemote, dir, url)
	}
	return m[1], nil
}

// ghCmd runs gh non-interactively in dir. Under go test, gh must be a fake
// in the temp dir, so no test reaches GitHub.
func ghCmd(ctx context.Context, dir string, args ...string) (string, error) {
	bin, err := exec.LookPath("gh")
	if err != nil {
		return "", err
	}
	if testing.Testing() {
		if rel, rerr := filepath.Rel(os.TempDir(), bin); rerr != nil || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("world: gh %s is outside the temp dir under go test", bin)
		}
	}
	gctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(gctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// PushAndOpenPR pushes head to branch on the push remote (never forced: a
// branch that moved elsewhere refuses it) and makes sure one PR is open for
// branch against the remote base (design.md 9.4). An open PR is adopted
// only if it is in this repo (not a fork), on the base and at head. Any
// other PR for branch, open, closed or merged, is an error: the engine
// never reopens a PR or opens a second one (N4). No PR at all: one is
// created. It returns the PR number.
func PushAndOpenPR(ctx context.Context, worktree, branch, head, title, body string) (int, error) {
	repo, err := ghRepo(ctx, worktree)
	if err != nil {
		return 0, err
	}
	if _, err := gitCmd(ctx, worktree, "push", pushRemote, head+":refs/heads/"+branch); err != nil {
		return 0, err
	}
	ref, _, err := remoteBaseCommit(ctx, worktree, false)
	if err != nil {
		return 0, err
	}
	base := strings.TrimPrefix(ref, "refs/remotes/"+pushRemote+"/")
	out, err := ghCmd(ctx, worktree, "pr", "list", "--repo", repo, "--head", branch, "--state", "all", "--json", "number,state,baseRefName,headRefOid,isCrossRepository")
	if err != nil {
		return 0, err
	}
	var prs []struct {
		Number                         int
		State, BaseRefName, HeadRefOid string
		IsCrossRepository              bool
	}
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return 0, fmt.Errorf("world: gh pr list: %w", err)
	}
	for _, p := range prs {
		// We just pushed H to this branch, so its open same-repo PR is ours
		// even if GitHub still lists the previous head (it lags a re-push).
		if p.State == "OPEN" && !p.IsCrossRepository && p.BaseRefName == base {
			return p.Number, nil
		}
	}
	if len(prs) > 0 {
		p := prs[0]
		return 0, fmt.Errorf("world: %s already has PR #%d (%s, base %s, head %s, fork %t), want open on %s at %s: not adopted, no second PR",
			branch, p.Number, p.State, p.BaseRefName, p.HeadRefOid, p.IsCrossRepository, base, head)
	}
	out, err = ghCmd(ctx, worktree, "pr", "create", "--repo", repo, "--head", branch, "--base", base, "--title", title, "--body", body)
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

// PRSnapshot reads the PR for branch (design.md 7.3). No PR for the branch
// is an answer (State none). When gh pr view is denied on statusCheckRollup,
// it falls back to REST check-runs; if that also fails, or the network fails,
// Checks is unreadable and an error is returned so the caller raises
// github_unavailable. A failure is never remembered as "not a GitHub repo"
// (KB-GIT-20). HeadRel and HeadSince are left to the caller.
func PRSnapshot(ctx context.Context, repoDir, branch string) (flow.PR, error) {
	repo, err := ghRepo(ctx, repoDir)
	if err != nil {
		return flow.PR{}, err
	}
	checksDenied := false
	raw, err := ghCmd(ctx, repoDir, "pr", "view", branch, "--repo", repo, "--json", prViewFields)
	if err != nil && strings.Contains(err.Error(), "no pull requests found") {
		return flow.PR{Answered: true, State: flow.PRNone}, nil
	} else if err != nil && isChecksDenied(err) {
		checksDenied = true
		raw, err = ghCmd(ctx, repoDir, "pr", "view", branch, "--repo", repo, "--json", strings.Replace(prViewFields, "statusCheckRollup,", "", 1))
	}
	if err != nil {
		return flow.PR{Checks: flow.ChecksUnreadable}, err
	}
	v, p, err := decodeSnapshot([]byte(raw))
	if err != nil || p.State != flow.PROpen {
		p.Checks = flow.Rollup(flow.RollupInput{Checks: v.StatusCheckRollup})
		return p, err
	}
	if checksDenied && p.Head != "" {
		if v.StatusCheckRollup, err = fetchCheckRuns(ctx, repoDir, repo, p.Head); err != nil {
			p.Answered, p.Checks = false, flow.ChecksUnreadable
			return p, err
		}
	}
	checks := flow.LatestChecks(v.StatusCheckRollup)
	for i, c := range checks {
		if id := checkRunID(c); id > 0 && strings.EqualFold(c.Status, "COMPLETED") && strings.EqualFold(c.Conclusion, "CANCELLED") {
			if checks[i].TimedOut, err = checkRunTimedOut(ctx, repoDir, repo, id); err != nil {
				return flow.PR{Checks: flow.ChecksUnreadable}, err
			}
		}
	}
	wfPending := false
	if p.Head != "" {
		if base := flow.Rollup(flow.RollupInput{Checks: checks}); base == flow.ChecksGreen || base == flow.ChecksAbsent {
			if wfPending, err = hasPendingWorkflow(ctx, repoDir, repo, p.Head); err != nil {
				return flow.PR{Checks: flow.ChecksUnreadable}, err
			}
		}
	}
	p.Checks = flow.Rollup(flow.RollupInput{Checks: checks, WorkflowPending: wfPending})
	if v.BaseRefName != "" && p.Head != "" {
		var cmpResp struct {
			BehindBy int `json:"behind_by"`
		}
		if err = ghAPI(ctx, repoDir, "repos/"+repo+"/compare/"+url.PathEscape(v.BaseRefName)+"..."+p.Head, &cmpResp); err != nil {
			return flow.PR{Checks: flow.ChecksUnreadable}, err
		}
		p.Behind = cmpResp.BehindBy > 0
	}
	return p, nil
}

func isChecksDenied(err error) bool {
	s := err.Error()
	return strings.Contains(s, "Resource not accessible") || strings.Contains(s, "HTTP 403") || strings.Contains(s, "statusCheckRollup")
}

func ghAPI(ctx context.Context, dir, endpoint string, dst any) error {
	out, err := ghCmd(ctx, dir, "api", endpoint)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(out), dst); err != nil {
		return fmt.Errorf("world: gh api %s: %w", endpoint, err)
	}
	return nil
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

func parseSnapshot(data []byte) (flow.PR, error) {
	v, p, err := decodeSnapshot(data)
	if err != nil {
		return flow.PR{}, err
	}
	p.Checks = flow.Rollup(flow.RollupInput{Checks: v.StatusCheckRollup})
	return p, nil
}

func fetchCheckRuns(ctx context.Context, dir, repo, head string) ([]flow.Check, error) {
	var resp struct {
		CheckRuns []struct {
			ID                       int64
			Name, Status, Conclusion string
			StartedAt                string `json:"started_at"`
			DetailsURL               string `json:"details_url"`
		} `json:"check_runs"`
	}
	if err := ghAPI(ctx, dir, "repos/"+repo+"/commits/"+head+"/check-runs", &resp); err != nil {
		return nil, err
	}
	checks := make([]flow.Check, len(resp.CheckRuns))
	for i, cr := range resp.CheckRuns {
		checks[i] = flow.Check{Kind: "CheckRun", ID: cr.ID, Name: cr.Name, Status: cr.Status, Conclusion: cr.Conclusion, StartedAt: cr.StartedAt, DetailsURL: cr.DetailsURL}
	}
	return checks, nil
}

func checkRunID(c flow.Check) int64 {
	if c.ID > 0 {
		return c.ID
	}
	if _, after, ok := strings.Cut(c.DetailsURL, "/job/"); ok {
		if fs := strings.FieldsFunc(after, func(r rune) bool { return r < '0' || r > '9' }); len(fs) > 0 {
			if id, err := strconv.ParseInt(fs[0], 10, 64); err == nil {
				return id
			}
		}
	}
	return 0
}

func checkRunTimedOut(ctx context.Context, dir, repo string, id int64) (bool, error) {
	var anns []struct{ Message string }
	err := ghAPI(ctx, dir, fmt.Sprintf("repos/%s/check-runs/%d/annotations", repo, id), &anns)
	return slices.ContainsFunc(anns, func(a struct{ Message string }) bool {
		return strings.Contains(a.Message, "exceeded the maximum execution time")
	}), err
}

func hasPendingWorkflow(ctx context.Context, dir, repo, head string) (bool, error) {
	var resp struct {
		WorkflowRuns []struct{ Status string } `json:"workflow_runs"`
	}
	err := ghAPI(ctx, dir, "repos/"+repo+"/actions/runs?head_sha="+head, &resp)
	return slices.ContainsFunc(resp.WorkflowRuns, func(r struct{ Status string }) bool {
		return !strings.EqualFold(r.Status, "completed")
	}), err
}

// MergePR asks GitHub to squash-merge PR number, pinned to head: GitHub
// refuses it if the PR head moved. Never --admin, --auto or --delete-branch
// (design.md 9.4, KB-MRG-10). The squash body is the caller's, built from
// task fields only. Its result is not trusted: only a later snapshot that
// reads merged ends the task (N2, KB-MRG-9).
func MergePR(ctx context.Context, repoDir string, number int, head, body string) error {
	if number <= 0 || head == "" {
		return fmt.Errorf("world: merge needs a PR number and a head (got #%d at %q)", number, head)
	}
	repo, err := ghRepo(ctx, repoDir)
	if err != nil {
		return err
	}
	_, err = ghCmd(ctx, repoDir, "pr", "merge", strconv.Itoa(number), "--repo", repo, "--squash", "--match-head-commit", head, "--body", body)
	return err
}

// ClosePR closes PR number with a short comment and keeps its branch (never
// --delete-branch, never merges; design.md 4.1 N1, 4.9).
func ClosePR(ctx context.Context, repoDir string, number int, note string) error {
	if number <= 0 || strings.TrimSpace(note) == "" {
		return fmt.Errorf("world: close PR needs a PR number and a note (got #%d)", number)
	}
	repo, err := ghRepo(ctx, repoDir)
	if err != nil {
		return err
	}
	_, err = ghCmd(ctx, repoDir, "pr", "close", strconv.Itoa(number), "--repo", repo, "--comment", note)
	return err
}

// DeleteRemoteBranch deletes branch on the push remote. The caller calls it
// only once the PR reads merged, never as part of the merge (design.md
// 9.4). A branch already gone (GitHub may delete it on merge) is not an error.
func DeleteRemoteBranch(ctx context.Context, dir, branch string) error {
	if branch == "" {
		return errors.New("world: no branch to delete")
	}
	out, err := gitCmd(ctx, dir, "ls-remote", "--heads", pushRemote, "refs/heads/"+branch)
	if err != nil || out == "" {
		return err
	}
	_, err = gitCmd(ctx, dir, "push", pushRemote, "--delete", "refs/heads/"+branch)
	return err
}
