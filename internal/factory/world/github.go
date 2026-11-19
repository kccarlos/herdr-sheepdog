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

var githubRepoRe = regexp.MustCompile(`github\.com[:/]([^/]+/[^/]+?)(?:\.git)?/?$`)

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
// branch against the remote base: the open one is adopted, else one is
// created (design.md 9.4). It returns the PR number.
func PushAndOpenPR(ctx context.Context, worktree, branch, head, title, body string) (int, error) {
	repo, err := ghRepo(ctx, worktree)
	if err != nil {
		return 0, err
	}
	if _, err := gitCmd(ctx, worktree, "push", pushRemote, head+":refs/heads/"+branch); err != nil {
		return 0, err
	}
	out, err := ghCmd(ctx, worktree, "pr", "list", "--repo", repo, "--head", branch, "--state", "open", "--json", "number")
	if err != nil {
		return 0, err
	}
	var open []struct{ Number int }
	if err := json.Unmarshal([]byte(out), &open); err != nil {
		return 0, fmt.Errorf("world: gh pr list: %w", err)
	}
	if len(open) > 0 {
		return open[0].Number, nil
	}
	ref, _, err := remoteBaseCommit(ctx, worktree, false)
	if err != nil {
		return 0, err
	}
	base := strings.TrimPrefix(ref, "refs/remotes/"+pushRemote+"/")
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
	out, err := ghCmd(ctx, repoDir, "pr", "view", branch, "--repo", repo, "--json", "number,state,headRefOid,mergeable,statusCheckRollup,baseRefName")
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
		Answered: true, Number: v.Number, State: state, Head: v.HeadRefOid,
		Mergeable: cmp.Or(mergeable, flow.MergeableUnknown), Checks: flow.Rollup(v.StatusCheckRollup),
	}, nil
}
