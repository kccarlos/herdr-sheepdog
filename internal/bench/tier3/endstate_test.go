package tier3

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// cloneRepo makes a source repo with one commit and clones it, as tier 3
// clones the bench repo per workspace.
func cloneRepo(t *testing.T) string {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	src := filepath.Join(t.TempDir(), "src")
	_ = os.MkdirAll(src, 0o755)
	git(t, src, "init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(src, "README.md"), []byte("# bench\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "ok.sh"), []byte("exit 0\n"), 0o644)
	git(t, src, "add", ".")
	git(t, src, "commit", "-q", "-m", "init")
	dst := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dst), "clone", "-q", src, dst)
	return dst
}

func TestEndStateChecks(t *testing.T) {
	repo := cloneRepo(t)
	_ = os.WriteFile(filepath.Join(repo, "CONTRIBUTING.md"), []byte("Run go test ./...\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("# bench\nmore\n"), 0o644)
	ew := endWorld{
		repo:   func(string) string { return repo },
		alias:  func(w string) string { return w },
		roster: []AgentRow{{PaneID: "w1:p1", Alias: "bench-claude", Kind: "claude"}, {PaneID: "w1:p2", Alias: "bench-claude-2", Kind: "claude"}},
		ours:   map[string]bool{"w1:p1": true, "w2:p1": true},
	}
	checks := []EndStateCheck{
		{Kind: EndFileExists, Worker: "bench-claude", Path: "CONTRIBUTING.md"},
		{Kind: EndFileContains, Worker: "bench-claude", Path: "CONTRIBUTING.md", Matches: `go test`},
		{Kind: EndFileChanged, Worker: "bench-claude", Path: "README.md"},
		{Kind: EndFileAbsent, Worker: "bench-claude", Path: "secrets.txt"},
		{Kind: EndBranch, Worker: "bench-claude", Matches: `^main$`},
		{Kind: EndTestsPass, Worker: "bench-claude", Argv: []string{"sh", "ok.sh"}},
		{Kind: EndAgentPresent, Worker: "bench-claude"},
		{Kind: EndAgentClosed, Worker: "docs-claude"},
		{Kind: EndAgentSpawned, Matches: "claude"},
		// These fail.
		{Kind: EndCommitMade, Worker: "bench-claude"},
		{Kind: EndFileChanged, Worker: "bench-claude", Path: "ok.sh"},
	}
	res, fails := checkEndState(context.Background(), checks, ew)
	for i, r := range res {
		want := i < 9
		if r.Pass != want {
			t.Errorf("%s: pass %v (%s), want %v", r.Name, r.Pass, r.Detail, want)
		}
	}
	if len(fails) != 2 || !strings.HasPrefix(fails[0].Detail, "end state: commit_made") {
		t.Errorf("fails = %+v", fails)
	}
	// A commit makes commit_made and a committed file_changed pass.
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "docs")
	res, _ = checkEndState(context.Background(), []EndStateCheck{{Kind: EndCommitMade, Worker: "bench-claude"}, {Kind: EndFileChanged, Worker: "bench-claude", Path: "README.md"}}, ew)
	if !res[0].Pass || !res[1].Pass {
		t.Errorf("after commit: %+v", res)
	}
}

func TestEndStateValidation(t *testing.T) {
	workers := map[string]Worker{"bench-claude": {Alias: "bench-claude"}}
	bad := []EndStateCheck{
		{Kind: "tests_green", Worker: "bench-claude"},
		{Kind: EndFileExists, Worker: "nobody", Path: "x"},
		{Kind: EndFileExists, Worker: "bench-claude"},
		{Kind: EndFileExists, Worker: "bench-claude", Path: "../etc/passwd"},
		{Kind: EndBranch, Worker: "bench-claude"},
		{Kind: EndFileContains, Worker: "bench-claude", Path: "x", Matches: "("},
	}
	for _, c := range bad {
		if err := c.validate(workers); err == nil {
			t.Errorf("%+v: want an error", c)
		}
	}
	for _, c := range []EndStateCheck{{Kind: EndAgentSpawned}, {Kind: EndTestsPass, Worker: "bench-claude"}} {
		if err := c.validate(workers); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
}
