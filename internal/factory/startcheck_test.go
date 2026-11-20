package factory

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

const pullRule = `[{"type":"pull_request","parameters":{"required_approving_review_count":2}}]`

const (
	goodWorkflow = "name: ci\non:\n  pull_request:\n    branches: [main]\njobs: {}\n"
	repoView     = `{"defaultBranchRef":{"name":"main"},"viewerPermission":"WRITE"}`
)

// startFake answers each command by its argv (joined); an unlisted command fails.
// Nothing here reaches the real gh, claude, git, home or herdr-core.
type startFake map[string]CommandOutput

func (f startFake) run(_ context.Context, _ string, _ []string, argv ...string) CommandOutput {
	key := strings.Join(argv, " ")
	for k, v := range f {
		if strings.HasSuffix(key, k) {
			return v
		}
	}
	return CommandOutput{Exit: 1, Stderr: "unexpected: " + key}
}

func out(s string) CommandOutput { return CommandOutput{Stdout: s} }

// runStart serves the workflow given from the remote base (not from disk) and
// runs StartChecks over the fake, whose good defaults the overrides replace. It
// returns each result by name.
func runStart(t *testing.T, workflow string, over startFake) map[string]onboard.Result {
	t.Helper()
	root := t.TempDir()
	f := startFake{
		"rev-parse --show-toplevel":                                 out(root),
		"remote":                                                    out("origin"),
		"config --get remote.origin.url":                            out("https://github.com/o/r.git"),
		"refs/remotes/origin/HEAD":                                  out("origin/main"),
		"claude auth status --json":                                 out(`{"loggedIn":true}`),
		"ls-tree --name-only origin/main .github/workflows/":        out(".github/workflows/ci.yml"),
		"show origin/main:.github/workflows/ci.yml":                 out(workflow),
		"gh api repos/o/r/branches/main":                            out(`{"protected":false}`),
		"gh repo view o/r --json defaultBranchRef,viewerPermission": out(repoView),
		"gh api repos/o/r/branches/main/protection":                 {Exit: 1, Stderr: "HTTP 404: Branch not protected"},
		"gh api repos/o/r/rules/branches/main":                      out("[]"),
		"fetch --prune origin":                                      out(""),
	}
	if workflow == "" {
		delete(f, "ls-tree --name-only origin/main .github/workflows/")
	}
	for k, v := range over {
		for d := range f {
			if strings.HasSuffix(d, k) || strings.HasSuffix(k, d) {
				delete(f, d)
			}
		}
		f[k] = v
	}
	res := map[string]onboard.Result{}
	for _, r := range StartChecks(context.Background(), DoctorOptions{Folder: root, RunCommand: f.run}) {
		res[r.Name] = r
	}
	return res
}

func TestStartChecksStatuses(t *testing.T) {
	protect := func(n string) CommandOutput {
		return out(`{"required_pull_request_reviews":{"required_approving_review_count":` + n + `}}`)
	}
	cases := []struct {
		name, workflow string
		over           startFake
		check          string
		want           onboard.Status
	}{
		{"all good", goodWorkflow, nil, "start claude", onboard.OK},
		{"cli signed out", goodWorkflow, startFake{"claude auth status --json": out(`{"loggedIn":false}`)}, "start claude", onboard.Fail},
		{"cli missing", goodWorkflow, startFake{"claude auth status --json": {Exit: 127}}, "start claude", onboard.Fail},
		{"gh ok", goodWorkflow, nil, "gh push access", onboard.OK},
		{"gh no access", goodWorkflow, startFake{"viewerPermission": out(`{"defaultBranchRef":{"name":"main"},"viewerPermission":"READ"}`)}, "gh push access", onboard.Fail},
		{"gh cannot see repo", goodWorkflow, startFake{"viewerPermission": {Exit: 1, Stderr: "Could not resolve"}}, "gh push access", onboard.Fail},
		{"remote ok", goodWorkflow, nil, "github remote", onboard.OK},
		{"remote none", goodWorkflow, startFake{"config --get remote.origin.url": {Exit: 1}}, "github remote", onboard.Fail},
		{"base differs from default", goodWorkflow, startFake{"refs/remotes/origin/HEAD": out("origin/dev")}, "github remote", onboard.Fail},
		{"origin head unset", goodWorkflow, startFake{"refs/remotes/origin/HEAD": {Exit: 1}}, "github remote", onboard.Warn},
		{"workflow ok", goodWorkflow, nil, "pr workflow", onboard.OK},
		{"workflow push only", "on: push\njobs: {}\n", nil, "pr workflow", onboard.Fail},
		{"workflow trigger commented out", "on: push\n# pull_request:\n", nil, "pr workflow", onboard.Fail},
		{"workflow none", "", nil, "pr workflow", onboard.Fail},
		{"no review required", goodWorkflow, nil, "review rule", onboard.OK},
		{"protection with 0 approvals", goodWorkflow, startFake{"gh api repos/o/r/branches/main": out(`{"protected":true}`), "branches/main/protection": protect("0")}, "review rule", onboard.OK},
		{"protection needs approval", goodWorkflow, startFake{"gh api repos/o/r/branches/main": out(`{"protected":true}`), "branches/main/protection": protect("1")}, "review rule", onboard.Fail},
		{"ruleset needs approval", goodWorkflow, startFake{"rules/branches/main": out(`[{"type":"pull_request","parameters":{"required_approving_review_count":2}}]`)}, "review rule", onboard.Fail},
		{"protected, approvals unreadable (not admin)", goodWorkflow, startFake{"gh api repos/o/r/branches/main": out(`{"protected":true}`), "branches/main/protection": {Exit: 1, Stderr: "HTTP 403"}}, "review rule", onboard.Warn},
		{"protected, not admin, ruleset needs approval", goodWorkflow, startFake{"gh api repos/o/r/branches/main": out(`{"protected":true}`), "branches/main/protection": {Exit: 1, Stderr: "HTTP 403"}, "rules/branches/main": out(pullRule)}, "review rule", onboard.Fail},
		{"not protected, ruleset needs approval", goodWorkflow, startFake{"rules/branches/main": out(pullRule)}, "review rule", onboard.Fail},
		{"free plan has no rulesets", goodWorkflow, startFake{"rules/branches/main": {Exit: 1, Stderr: "HTTP 403: Upgrade to GitHub Pro or make this repository public to enable this feature."}}, "review rule", onboard.OK},
		{"rulesets unreadable", goodWorkflow, startFake{"rules/branches/main": {Exit: 1, Stderr: "HTTP 500"}}, "review rule", onboard.Warn},
		{"branch 404 is not unprotected", goodWorkflow, startFake{"gh api repos/o/r/branches/main": {Exit: 1, Stderr: "HTTP 404"}}, "review rule", onboard.Warn},
		{"fetch fails", goodWorkflow, startFake{"fetch --prune origin": {Exit: 128, Stderr: "could not read Username"}}, "start fetch", onboard.Warn},
		{"ruleset-only protection gives no warn", goodWorkflow, startFake{"gh api repos/o/r/branches/main": out(`{"protected":true}`)}, "review rule", onboard.OK},
		{"classic code-owner review", goodWorkflow, startFake{"gh api repos/o/r/branches/main": out(`{"protected":true}`), "branches/main/protection": out(`{"required_pull_request_reviews":{"required_approving_review_count":0,"require_code_owner_reviews":true}}`)}, "review rule", onboard.Fail},
		{"ruleset code-owner review", goodWorkflow, startFake{"rules/branches/main": out(`[{"type":"pull_request","parameters":{"required_approving_review_count":0,"require_code_owner_review":true}}]`)}, "review rule", onboard.Fail},
		{"workflow only on disk", "", nil, "pr workflow", onboard.Fail},
		{"workflow on another remote branch only", goodWorkflow, startFake{"ls-tree --name-only origin/main .github/workflows/": {Exit: 1}}, "pr workflow", onboard.Fail},
		{"no origin, two remotes", goodWorkflow, startFake{"remote": out("mine\nfork")}, "github remote", onboard.Fail},
		{"only remote is not origin", goodWorkflow, startFake{"remote": out("mine"), "config --get remote.mine.url": out("https://github.com/o/r.git"), "refs/remotes/mine/HEAD": out("mine/main")}, "gh push access", onboard.OK},
		{"no protection is reported, not required", goodWorkflow, nil, "base protection", onboard.Warn},
		{"protection present", goodWorkflow, startFake{"gh api repos/o/r/branches/main": out(`{"protected":true}`), "branches/main/protection": protect("0")}, "base protection", onboard.OK},
		{"ruleset present", goodWorkflow, startFake{"rules/branches/main": out(`[{"type":"required_status_checks"}]`)}, "base protection", onboard.OK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok := runStart(t, c.workflow, c.over)[c.check]
			if !ok || r.Status != c.want {
				t.Fatalf("%s = %+v, want status %v", c.check, r, c.want)
			}
			if r.Status != onboard.OK && r.Status != onboard.Skip && r.Fix == "" {
				t.Errorf("%s has no fix: %+v", c.check, r)
			}
		})
	}
}

// TestStartChecksNoPRWorkflowGivesEveryFix: a repo with no PR workflow and a signed-out CLI
// reports both failures, each with its own fix, and still checks the rest.
func TestStartChecksNoPRWorkflowGivesEveryFix(t *testing.T) {
	res := runStart(t, "", startFake{"claude auth status --json": out(`{"loggedIn":false}`)})
	if !onboard.Failed([]onboard.Result{res["pr workflow"], res["start claude"]}) || len(res) != 6 {
		t.Fatalf("results = %+v", res)
	}
	for _, n := range []string{"pr workflow", "start claude"} {
		if res[n].Status != onboard.Fail || !strings.Contains(res[n].Line(), "fix: ") {
			t.Errorf("%s line lacks its fix: %q", n, res[n].Line())
		}
	}
	if res["gh push access"].Status != onboard.OK {
		t.Errorf("independent checks must still run: %+v", res["gh push access"])
	}
}

// TestStartChecksEscapeBranchName: a default branch with a slash is escaped in API paths.
func TestStartChecksEscapeBranchName(t *testing.T) {
	res := runStart(t, goodWorkflow, startFake{
		"gh repo view o/r --json defaultBranchRef,viewerPermission": out(`{"defaultBranchRef":{"name":"release/1.0"},"viewerPermission":"WRITE"}`),
		"refs/remotes/origin/HEAD":                                  out("origin/release/1.0"),
		"ls-tree --name-only origin/release/1.0 .github/workflows/": out(".github/workflows/ci.yml"),
		"show origin/release/1.0:.github/workflows/ci.yml":          out(goodWorkflow),
		"gh api repos/o/r/branches/release%2F1.0":                   out(`{"protected":false}`),
		"gh api repos/o/r/rules/branches/release%2F1.0":             out("[]"),
	})
	if r := res["review rule"]; r.Status != onboard.OK {
		t.Fatalf("review rule = %+v", r)
	}
	if r := res["pr workflow"]; r.Status != onboard.OK {
		t.Fatalf("pr workflow = %+v", r)
	}
}

// TestStartChecksWorkflowIgnoresDisk: a workflow file on disk that is not on the remote base does not pass.
func TestStartChecksWorkflowIgnoresDisk(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte(goodWorkflow), 0o600); err != nil {
		t.Fatal(err)
	}
	f := startFake{"rev-parse --show-toplevel": out(root), "remote": out("origin"), "config --get remote.origin.url": out("https://github.com/o/r.git")}
	for _, r := range StartChecks(context.Background(), DoctorOptions{Folder: root, RunCommand: f.run}) {
		if r.Name == "pr workflow" && r.Status != onboard.Fail {
			t.Fatalf("pr workflow = %+v", r)
		}
	}
}

// TestStartChecksGitNeverPrompts: every git call runs with prompts off.
func TestStartChecksGitNeverPrompts(t *testing.T) {
	git := 0
	run := func(_ context.Context, _ string, env []string, argv ...string) CommandOutput {
		if argv[0] == "git" {
			git++
			if !slices.Contains(env, "GIT_TERMINAL_PROMPT=0") || !slices.Contains(env, "GIT_ASKPASS=") {
				t.Errorf("git %v ran with env %v", argv, env)
			}
		}
		return CommandOutput{Stdout: "origin"}
	}
	StartChecks(context.Background(), DoctorOptions{Folder: t.TempDir(), RunCommand: run})
	if git == 0 {
		t.Fatal("no git call seen")
	}
}
