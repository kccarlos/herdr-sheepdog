package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

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

// runStart makes a repo with the workflows given and runs StartChecks over the fake,
// whose good defaults the overrides replace. It returns each result by name.
func runStart(t *testing.T, workflow string, over startFake) map[string]onboard.Result {
	t.Helper()
	root := t.TempDir()
	if workflow != "" {
		dir := filepath.Join(root, ".github", "workflows")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte(workflow), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := startFake{
		"rev-parse --show-toplevel":      out(root),
		"remote":                         out("origin"),
		"config --get remote.origin.url": out("https://github.com/o/r.git"),
		"refs/remotes/origin/HEAD":       out("origin/main"),
		"claude auth status --json":      out(`{"loggedIn":true}`),
		"gh auth status":                 out("ok"),
		"gh repo view o/r --json defaultBranchRef,viewerPermission": out(repoView),
		"gh api repos/o/r/branches/main/protection":                 {Exit: 1, Stderr: "HTTP 404: Branch not protected"},
		"gh api repos/o/r/rules/branches/main":                      out("[]"),
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
		{"all good", goodWorkflow, nil, "coding cli", onboard.OK},
		{"cli signed out", goodWorkflow, startFake{"claude auth status --json": out(`{"loggedIn":false}`)}, "coding cli", onboard.Fail},
		{"cli missing", goodWorkflow, startFake{"claude auth status --json": {Exit: 127}}, "coding cli", onboard.Fail},
		{"gh ok", goodWorkflow, nil, "gh", onboard.OK},
		{"gh signed out", goodWorkflow, startFake{"gh auth status": {Exit: 1}}, "gh", onboard.Fail},
		{"gh no access", goodWorkflow, startFake{"viewerPermission": out(`{"defaultBranchRef":{"name":"main"},"viewerPermission":"READ"}`)}, "gh", onboard.Fail},
		{"gh cannot see repo", goodWorkflow, startFake{"viewerPermission": {Exit: 1, Stderr: "Could not resolve"}}, "gh", onboard.Fail},
		{"remote ok", goodWorkflow, nil, "github remote", onboard.OK},
		{"remote none", goodWorkflow, startFake{"config --get remote.origin.url": {Exit: 1}}, "github remote", onboard.Fail},
		{"base differs from default", goodWorkflow, startFake{"refs/remotes/origin/HEAD": out("origin/dev")}, "github remote", onboard.Fail},
		{"origin head unset", goodWorkflow, startFake{"refs/remotes/origin/HEAD": {Exit: 1}}, "github remote", onboard.Warn},
		{"workflow ok", goodWorkflow, nil, "pr workflow", onboard.OK},
		{"workflow push only", "on: push\njobs: {}\n", nil, "pr workflow", onboard.Fail},
		{"workflow trigger commented out", "on: push\n# pull_request:\n", nil, "pr workflow", onboard.Fail},
		{"workflow none", "", nil, "pr workflow", onboard.Fail},
		{"no review required", goodWorkflow, nil, "review rule", onboard.OK},
		{"protection with 0 approvals", goodWorkflow, startFake{"branches/main/protection": protect("0")}, "review rule", onboard.OK},
		{"protection needs approval", goodWorkflow, startFake{"branches/main/protection": protect("1")}, "review rule", onboard.Fail},
		{"ruleset needs approval", goodWorkflow, startFake{"rules/branches/main": out(`[{"type":"pull_request","parameters":{"required_approving_review_count":2}}]`)}, "review rule", onboard.Fail},
		{"protection unreadable", goodWorkflow, startFake{"branches/main/protection": {Exit: 1, Stderr: "HTTP 403"}}, "review rule", onboard.Warn},
		{"no protection is reported, not required", goodWorkflow, nil, "base protection", onboard.Warn},
		{"protection present", goodWorkflow, startFake{"branches/main/protection": protect("0")}, "base protection", onboard.OK},
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
	if !onboard.Failed([]onboard.Result{res["pr workflow"], res["coding cli"]}) || len(res) != 6 {
		t.Fatalf("results = %+v", res)
	}
	for _, n := range []string{"pr workflow", "coding cli"} {
		if res[n].Status != onboard.Fail || !strings.Contains(res[n].Line(), "fix: ") {
			t.Errorf("%s line lacks its fix: %q", n, res[n].Line())
		}
	}
	if res["gh"].Status != onboard.OK {
		t.Errorf("independent checks must still run: %+v", res["gh"])
	}
}
