package world

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
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
	_, p, err := decodeSnapshot([]byte(`{"number":7,"state":"MERGED","headRefOid":"h","mergeable":"UNKNOWN","statusCheckRollup":[],"mergeCommit":{"oid":"m1"}}`))
	if err != nil || p.State != flow.PRMerged || p.MergedCommit != "m1" {
		t.Fatalf("merged snapshot = %+v, %v", p, err)
	}
	_, p, err = decodeSnapshot([]byte(`{"number":7,"state":"OPEN","headRefOid":"h","mergeable":"MERGEABLE","statusCheckRollup":[],"mergeCommit":null}`))
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

func TestClosePR(t *testing.T) {
	ctx := context.Background()
	repo, _ := repoWithGitHubOrigin(t)
	gh := installFakeGH(t)
	if err := ClosePR(ctx, repo, 7, "split into T1a, T1b"); err != nil {
		t.Fatal(err)
	}
	got := ghCalls(t, gh, "pr close")
	if len(got) != 1 || got[0] != "pr close 7 --repo acme/demo --comment split into T1a, T1b" {
		t.Fatalf("close argv = %q", got)
	}
	if strings.Contains(got[0], "--delete-branch") {
		t.Fatalf("close passed --delete-branch: %q", got[0])
	}
	if ClosePR(ctx, repo, 0, "note") == nil || ClosePR(ctx, repo, 7, "  ") == nil || len(ghCalls(t, gh, "pr close")) != 1 {
		t.Fatalf("close without number or note called gh: %q", ghCalls(t, gh, ""))
	}
}

func TestFailureClass(t *testing.T) {
	exitErr := func(code string) error {
		return exec.Command("sh", "-c", "exit "+code).Run()
	}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"deadline", context.DeadlineExceeded, flow.ClassTimeout},
		{"explicit settled", Fail(flow.ClassSettled, os.ErrPermission), flow.ClassSettled},
		{"plain os error", os.ErrNotExist, flow.ClassTransient},
		{"exit 4 (gh auth)", exitErr("4"), flow.ClassSettled},
		{"exit 127 (missing cmd)", exitErr("127"), flow.ClassSettled},
		{"exit 1 (generic)", exitErr("1"), flow.ClassTransient},
		{"hc invalid params (-32602)", hcFail(&hc.Error{Code: -32602, Message: "invalid params"}), flow.ClassSettled},
		{"hc HTTP 403", hcFail(&hc.Error{Code: 403, Message: "forbidden"}), flow.ClassTransient},
		{"hc server error (-32000)", hcFail(&hc.Error{Code: -32000, Kind: "unavailable", Message: "busy"}), flow.ClassTransient},
		{"gh HTTP 500", errString("HTTP 500: Internal Server Error"), flow.ClassTransient},
		{"gh HTTP 502", errString("HTTP 502: Bad Gateway"), flow.ClassTransient},
		{"gh HTTP 429", errString("HTTP 429: Too Many Requests"), flow.ClassTransient},
		{"gh HTTP 401", errString("HTTP 401: Bad credentials"), flow.ClassSettled},
		{"gh HTTP 403", errString("gh: Resource not accessible by integration (HTTP 403)"), flow.ClassTransient},
		{"gh HTTP 403 rate limit", errString("HTTP 403: API rate limit exceeded"), flow.ClassTransient},
		{"gh HTTP 404", errString("gh: Not Found (HTTP 404)"), flow.ClassSettled},
		{"gh HTTP 422", errString("HTTP 422: Validation Failed"), flow.ClassSettled},
		{"gh HTTP 405 (unlisted -> transient)", errString("HTTP 405: Method Not Allowed"), flow.ClassTransient},
		{"prose without status (head modified)", errString("GraphQL: Head branch was modified"), flow.ClassTransient},
		{"prose without status (policy)", errString("the base branch policy prohibits the merge"), flow.ClassTransient},
		{"prose without status (non-ff)", errString("! [rejected] main -> main (non-fast-forward)"), flow.ClassTransient},
		{"prose without status (not found)", errString("branch not found"), flow.ClassTransient},
		{"prose without status (already exists)", errString("worktree already exists"), flow.ClassTransient},
	} {
		if got := FailureClass(tc.err); got != tc.want {
			t.Errorf("%s: FailureClass(%v) = %q, want %q", tc.name, tc.err, got, tc.want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
