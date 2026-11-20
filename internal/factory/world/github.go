package world

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// PRSnapshot reads the PR for branch with one gh call (design.md 7.3). No
// PR for the branch is an answer (State none); any other failure is an
// error, and the caller marks the snapshot unanswered. HeadRel and
// HeadSince are left to the caller, which knows H and the last snapshot.
func PRSnapshot(ctx context.Context, repoDir, branch string) (flow.PR, error) {
	repo, err := ghRepo(ctx, repoDir)
	if err != nil {
		return flow.PR{}, err
	}
	out, err := ghCmd(ctx, repoDir, "pr", "view", branch, "--repo", repo, "--json", "number,state,headRefOid,mergeable,statusCheckRollup,baseRefName,mergeCommit")
	if err != nil && strings.Contains(err.Error(), "no pull requests found") {
		return flow.PR{Answered: true, State: flow.PRNone}, nil
	} else if err != nil {
		return flow.PR{}, err
	}
	return parseSnapshot([]byte(out))
}

func parseSnapshot(data []byte) (flow.PR, error) {
	var v struct {
		Number            int
		State             string
		HeadRefOid        string
		Mergeable         string
		StatusCheckRollup []flow.Check
		MergeCommit       struct{ Oid string }
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return flow.PR{}, fmt.Errorf("world: gh pr view: %w", err)
	}
	state := flow.PRState(strings.ToLower(v.State))
	if state != flow.PROpen && state != flow.PRMerged && state != flow.PRClosed {
		return flow.PR{}, errors.New("world: gh pr view: unknown state " + v.State)
	}
	mergeable := map[string]flow.Mergeable{"MERGEABLE": flow.MergeableClean, "CONFLICTING": flow.MergeableConflict}[v.Mergeable]
	return flow.PR{
		Answered: true, Number: v.Number, State: state, Head: v.HeadRefOid, MergedCommit: v.MergeCommit.Oid,
		Mergeable: cmp.Or(mergeable, flow.MergeableUnknown), Checks: flow.Rollup(v.StatusCheckRollup),
	}, nil
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
