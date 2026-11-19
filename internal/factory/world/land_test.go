package world

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// TestMergePRPinnedSquash: the merge names the repo, squashes, pins the
// head and passes the caller's body; never --admin, --auto or
// --delete-branch. With no PR number or head, gh is not called.
func TestMergePRPinnedSquash(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)
	head := strings.Repeat("c", 40)
	if err := MergePR(ctx, repo, 7, head, "body"); err != nil {
		t.Fatal(err)
	}
	if got := ghCalls(t, gh, "pr merge"); len(got) != 1 || got[0] != "pr merge 7 --repo acme/demo --squash --match-head-commit "+head+" --body body" {
		t.Fatalf("merge argv = %q", got)
	}
	if MergePR(ctx, repo, 0, head, "b") == nil || MergePR(ctx, repo, 7, "", "b") == nil || len(ghCalls(t, gh, "pr merge")) != 1 {
		t.Fatalf("merge without a number or head: calls %q", ghCalls(t, gh, ""))
	}
	if err := os.WriteFile(filepath.Join(gh, "merge_fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MergePR(ctx, repo, 7, head, "b"); err == nil || !strings.Contains(err.Error(), "Head branch was modified") {
		t.Fatalf("refused merge err = %v", err)
	}
}

// TestSnapshotMergeCommit: a merged PR's snapshot carries gh's mergeCommit
// oid; an open one has none.
func TestSnapshotMergeCommit(t *testing.T) {
	p, err := parseSnapshot([]byte(`{"number":7,"state":"MERGED","headRefOid":"h","mergeable":"UNKNOWN","statusCheckRollup":[],"mergeCommit":{"oid":"m1"}}`))
	if err != nil || p.State != flow.PRMerged || p.MergedCommit != "m1" {
		t.Fatalf("merged snapshot = %+v, %v", p, err)
	}
	p, err = parseSnapshot([]byte(`{"number":7,"state":"OPEN","headRefOid":"h","mergeable":"MERGEABLE","statusCheckRollup":[],"mergeCommit":null}`))
	if err != nil || p.MergedCommit != "" {
		t.Fatalf("open snapshot = %+v, %v", p, err)
	}
}

// TestRemoveWorktreeOnlyClean: a dirty or untracked worktree is kept
// (KB-MRG-78); a clean one is removed; a missing one counts as removed.
func TestRemoveWorktreeOnlyClean(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-b", "task/x", wt)
	if err := os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if gone, err := RemoveWorktree(ctx, repo, wt); err != nil || gone {
		t.Fatalf("dirty worktree: gone=%v err=%v", gone, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "wip.txt")); err != nil {
		t.Fatalf("uncommitted work destroyed: %v", err)
	}
	if err := os.Remove(filepath.Join(wt, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if gone, err := RemoveWorktree(ctx, repo, wt); err != nil || !gone {
			t.Fatalf("clean worktree: gone=%v err=%v", gone, err)
		}
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("clean worktree still there: %v", err)
	}
}

// TestDeleteRemoteBranchIdempotent: the branch is deleted on the push
// remote; deleting it again is not an error.
func TestDeleteRemoteBranchIdempotent(t *testing.T) {
	ctx := context.Background()
	repo, bare := repoWithGitHubOrigin(t)
	runGit(t, repo, "push", "origin", "HEAD:refs/heads/task/x")
	for range 2 {
		if err := DeleteRemoteBranch(ctx, repo, "task/x"); err != nil {
			t.Fatal(err)
		}
	}
	if out := runGit(t, bare, "branch", "--list", "task/x"); out != "" {
		t.Fatalf("remote branch still there: %q", out)
	}
	if DeleteRemoteBranch(ctx, repo, "") == nil {
		t.Fatal("empty branch accepted")
	}
}
