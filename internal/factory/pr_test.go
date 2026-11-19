package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// fakeGHScript is world's fake gh (world/github_test.go): it records argv in
// $D/argv, fails when $D/fail exists, and serves $D/view.json as the PR
// (pr create copies $D/pr.json there).
const fakeGHScript = `#!/bin/sh
D=%DIR%
printf '%s\n' "$*" >> "$D/argv"
[ -f "$D/fail" ] && { echo "HTTP 502: Bad Gateway" >&2; exit 1; }
case "$1 $2" in
"pr list") if [ -f "$D/view.json" ]; then echo '[{"number":7}]'; else echo '[]'; fi ;;
"pr create") cp "$D/pr.json" "$D/view.json" && echo "https://github.com/acme/demo/pull/7" ;;
"pr view") cat "$D/view.json" 2>/dev/null || { echo 'no pull requests found for branch "x"' >&2; exit 1; } ;;
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
	if err := eng.Start(repo, "Goal"); err != nil {
		t.Fatal(err)
	}
	id, s, err := eng.findProjectLocked(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "commit", "--allow-empty", "-m", "feat: x")
	c1 := gitIn(t, repo, "rev-parse", "HEAD")
	s.Tasks = []flow.Task{{ID: "t1", Title: "feat: x", Attempts: []flow.Attempt{{BaseCommit: base, Branch: "task/t1", Worktree: repo, Ready: []flow.Ready{{Head: c1}}}}}}

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
	if p2 := read(); p2.Checks != flow.ChecksGreen || !p2.HeadSince.Equal(p1.HeadSince) {
		t.Fatalf("green snapshot = %+v (head_since must stay %s)", p2, p1.HeadSince)
	}
	if err := os.WriteFile(filepath.Join(gh, "fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if p3 := read(); p3.Answered || p3.State != flow.PROpen || p3.Checks != flow.ChecksGreen {
		t.Fatalf("failed snapshot = %+v, want answered=false and the last snapshot", p3)
	}
	logRaw, _ := os.ReadFile(paths.Log(id))
	if n := strings.Count(string(logRaw), `"rule":"pr_snapshot"`); n != 3 ||
		!strings.Contains(string(logRaw), `"checks":"pending"`) || !strings.Contains(string(logRaw), `"checks":"green"`) || !strings.Contains(string(logRaw), `"answered":false`) {
		t.Fatalf("log has %d snapshot lines:\n%s", n, logRaw)
	}
}
