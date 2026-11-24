package factory

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
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
		"git --version":                                             out("git version 2.45.0\n"),
		"rev-parse --show-toplevel":                                 out(root),
		"rev-parse --verify HEAD":                                   out("abc1234\n"),
		"remote get-url --all origin":                               out("https://github.com/o/r.git"),
		"remote get-url --push --all origin":                        out("https://github.com/o/r.git"),
		"refs/remotes/origin/HEAD":                                  out("origin/main"),
		"rev-parse --verify --quiet origin/main":                    out("abc1234\n"),
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
		{"remote none", goodWorkflow, startFake{"remote get-url --all origin": {Exit: 2}}, "github remote", onboard.Fail},
		{"base differs from default", goodWorkflow, startFake{"refs/remotes/origin/HEAD": out("origin/dev")}, "github remote", onboard.Fail},
		{"origin head unset", goodWorkflow, startFake{"refs/remotes/origin/HEAD": {Exit: 1}}, "github remote", onboard.Warn},
		{"workflow ok", goodWorkflow, nil, "pr workflow", onboard.OK},
		{"workflow string", "on: pull_request\njobs: {}\n", nil, "pr workflow", onboard.OK},
		{"workflow quoted target", "on: \"pull_request_target\"\njobs: {}\n", nil, "pr workflow", onboard.OK},
		{"workflow flow list", "on: [push, pull_request]\njobs: {}\n", nil, "pr workflow", onboard.OK},
		{"workflow multiline flow list", "on: [\n  push,\n  pull_request\n]\njobs: {}\n", nil, "pr workflow", onboard.OK},
		{"workflow block list", "on:\n  - push\n  - pull_request\njobs: {}\n", nil, "pr workflow", onboard.OK},
		{"workflow quoted on indent 0 list", "'on':\n- pull_request\njobs: {}\n", nil, "pr workflow", onboard.OK},
		{"workflow flow map", "on: { push: {}, pull_request: {} }\njobs: {}\n", nil, "pr workflow", onboard.OK},
		{"workflow utf8 bom", "\uFEFFon: pull_request\njobs: {}\n", nil, "pr workflow", onboard.OK},
		{"workflow flow map null values", "on: {pull_request, push}\njobs: {}\n", nil, "pr workflow", onboard.OK},
		{"workflow anchor block map", "on: &triggers\n  pull_request:\njobs: {}\n", nil, "pr workflow", onboard.OK},
		{"workflow anchor on pull_request respects branches", "on:\n  pull_request: &pr\n    branches: [other]\njobs: {}\n", nil, "pr workflow", onboard.Fail},
		{"workflow next-line flow list under branches", "on:\n  pull_request:\n    branches:\n      [main]\njobs: {}\n", nil, "pr workflow", onboard.OK},
		{"workflow literal true key is not a trigger", "true:\n  pull_request:\njobs: {}\n", nil, "pr workflow", onboard.Fail},
		{"workflow push only", "on: push\njobs: {}\n", nil, "pr workflow", onboard.Fail},
		{"workflow pull_request only in if", "on: push\njobs:\n  ci:\n    if: github.event_name == 'pull_request'\n    runs-on: ubuntu-latest\n", nil, "pr workflow", onboard.Fail},
		{"workflow pull_request only in string", "name: pull_request check\non: push\njobs:\n  ci:\n    steps:\n      - run: echo pull_request\n", nil, "pr workflow", onboard.Fail},
		{"workflow trigger commented out", "on: push\n# pull_request:\n", nil, "pr workflow", onboard.Fail},
		{"workflow none", "", nil, "pr workflow", onboard.Fail},
		{"workflow paths-ignore warns", "on:\n  pull_request:\n    paths-ignore:\n      - 'docs/**'\njobs: {}\n", nil, "pr workflow", onboard.Warn},
		{"workflow paths warns", "on:\n  pull_request:\n    paths: ['src/**']\njobs: {}\n", nil, "pr workflow", onboard.Warn},
		{"workflow pull_request_target paths warns", "on:\n  pull_request_target:\n    paths: ['src/**']\njobs: {}\n", nil, "pr workflow", onboard.Warn},
		{"workflow .yaml filtered warns", "on:\n  pull_request:\n    paths-ignore: ['docs/**']\njobs: {}\n", startFake{
			"ls-tree --name-only origin/main .github/workflows/": out(".github/workflows/ci.yaml"),
			"show origin/main:.github/workflows/ci.yaml":         out("on:\n  pull_request:\n    paths-ignore: ['docs/**']\njobs: {}\n"),
		}, "pr workflow", onboard.Warn},
		{"workflow flow map paths warns", "on: { pull_request: { paths: [src/**] } }\njobs: {}\n", nil, "pr workflow", onboard.Warn},
		{"workflow push paths-ignore does not warn", "on:\n  push:\n    paths-ignore: ['docs/**']\n  pull_request:\n    branches: [main]\njobs: {}\n", nil, "pr workflow", onboard.OK},
		{"git version 2.38 ok", goodWorkflow, startFake{"git --version": out("git version 2.38.0\n")}, "git version", onboard.OK},
		{"git version 2.38 rc ok", goodWorkflow, startFake{"git --version": out("git version 2.38.0-rc1\n")}, "git version", onboard.OK},
		{"git version 2.38 apple ok", goodWorkflow, startFake{"git --version": out("git version 2.38.0 (Apple Git-143)\n")}, "git version", onboard.OK},
		{"git version 3.0 ok", goodWorkflow, startFake{"git --version": out("git version 3.0.0\n")}, "git version", onboard.OK},
		{"git version 2.37 fails", goodWorkflow, startFake{"git --version": out("git version 2.37.1 (Apple Git-137.1)\n")}, "git version", onboard.Fail},
		{"git version 2.37 rc fails", goodWorkflow, startFake{"git --version": out("git version 2.37.9-rc0\n")}, "git version", onboard.Fail},
		{"git version odd output fails", goodWorkflow, startFake{"git --version": out("hub version x\n")}, "git version", onboard.Fail},
		{"git version missing fails", goodWorkflow, startFake{"git --version": {Exit: 127, Stderr: "not found"}}, "git version", onboard.Fail},
		{"empty remote repo fails", goodWorkflow, startFake{"gh repo view o/r --json defaultBranchRef,viewerPermission": out(`{"defaultBranchRef":null,"viewerPermission":"ADMIN"}`)}, "github remote", onboard.Fail},
		{"unborn local head with gh failure is not empty", goodWorkflow, startFake{
			"rev-parse --verify HEAD":                                   {Exit: 128},
			"refs/remotes/origin/HEAD":                                  {Exit: 1},
			"gh repo view o/r --json defaultBranchRef,viewerPermission": {Exit: 1, Stderr: "not logged in"},
		}, "github remote", onboard.Skip},
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
		{"no origin, two remotes", goodWorkflow, startFake{"remote get-url --all origin": {Exit: 2}}, "github remote", onboard.Fail},
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
	if !onboard.Failed([]onboard.Result{res["pr workflow"], res["start claude"]}) || len(res) != 7 {
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

func TestStartChecksUnfetchedBaseRefFails(t *testing.T) {
	res := runStart(t, goodWorkflow, startFake{"rev-parse --verify --quiet origin/main": {Exit: 1}})
	r := res["pr workflow"]
	if r.Status != onboard.Fail || r.Fix != "run: git fetch origin" {
		t.Fatalf("pr workflow = %+v, want Fail with fix %q", r, "run: git fetch origin")
	}
}

// TestStartChecksEscapeBranchName: a default branch with a slash is escaped in API paths.
func TestStartChecksEscapeBranchName(t *testing.T) {
	const releaseWorkflow = "name: ci\non:\n  pull_request:\n    branches: [release/1.0]\njobs: {}\n"
	res := runStart(t, releaseWorkflow, startFake{
		"gh repo view o/r --json defaultBranchRef,viewerPermission": out(`{"defaultBranchRef":{"name":"release/1.0"},"viewerPermission":"WRITE"}`),
		"refs/remotes/origin/HEAD":                                  out("origin/release/1.0"),
		"rev-parse --verify --quiet origin/release/1.0":             out("abc1234\n"),
		"ls-tree --name-only origin/release/1.0 .github/workflows/": out(".github/workflows/ci.yml"),
		"show origin/release/1.0:.github/workflows/ci.yml":          out(releaseWorkflow),
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
	f := startFake{
		"git --version":                          out("git version 2.45.0\n"),
		"rev-parse --show-toplevel":              out(root),
		"rev-parse --verify HEAD":                out("abc1234\n"),
		"remote get-url --all origin":            out("https://github.com/o/r.git"),
		"remote get-url --push --all origin":     out("https://github.com/o/r.git"),
		"rev-parse --verify --quiet origin/HEAD": out("abc1234\n"),
	}
	for _, r := range StartChecks(context.Background(), DoctorOptions{Folder: root, RunCommand: f.run}) {
		if r.Name == "pr workflow" && r.Status != onboard.Fail {
			t.Fatalf("pr workflow = %+v", r)
		}
	}
}

func TestStartChecksMultipleWorkflowsPathFilter(t *testing.T) {
	filtered := "on:\n  pull_request:\n    paths-ignore: ['docs/**']\njobs: {}\n"
	res := runStart(t, filtered, startFake{
		"ls-tree --name-only origin/main .github/workflows/": out(".github/workflows/a.yml\n.github/workflows/b.yml\n"),
		"show origin/main:.github/workflows/a.yml":           out(filtered),
		"show origin/main:.github/workflows/b.yml":           out(goodWorkflow),
	})
	if r := res["pr workflow"]; r.Status != onboard.OK {
		t.Fatalf("one unfiltered workflow should pass: %+v", r)
	}
	resAllFiltered := runStart(t, filtered, startFake{
		"ls-tree --name-only origin/main .github/workflows/": out(".github/workflows/a.yml\n.github/workflows/b.yml\n"),
		"show origin/main:.github/workflows/a.yml":           out(filtered),
		"show origin/main:.github/workflows/b.yml":           out("on:\n  pull_request:\n    paths: ['go.mod']\n"),
	})
	if r := resAllFiltered["pr workflow"]; r.Status != onboard.Warn || !strings.Contains(r.Detail, "N35") {
		t.Fatalf("all filtered workflows should warn with N35: %+v", r)
	}
}

func TestStartChecksSecondReviewerCodex(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	st := flow.State{
		Project:  flow.Project{ID: "p1", Folder: root},
		Settings: flow.DefaultSettings(),
	}
	st.Settings.SecondReviewer = "codex"
	if err := SaveState(Paths{DataDir: dataDir}.State("p1"), st); err != nil {
		t.Fatal(err)
	}

	makeFake := func(codex CommandOutput) startFake {
		return startFake{
			"git --version":                                             out("git version 2.45.0\n"),
			"rev-parse --show-toplevel":                                 out(root),
			"rev-parse --verify HEAD":                                   out("abc1234\n"),
			"remote get-url --all origin":                               out("https://github.com/o/r.git"),
			"remote get-url --push --all origin":                        out("https://github.com/o/r.git"),
			"refs/remotes/origin/HEAD":                                  out("origin/main"),
			"rev-parse --verify --quiet origin/main":                    out("abc1234\n"),
			"claude auth status --json":                                 out(`{"loggedIn":true}`),
			"codex login status":                                        codex,
			"ls-tree --name-only origin/main .github/workflows/":        out(".github/workflows/ci.yml"),
			"show origin/main:.github/workflows/ci.yml":                 out(goodWorkflow),
			"gh api repos/o/r/branches/main":                            out(`{"protected":false}`),
			"gh repo view o/r --json defaultBranchRef,viewerPermission": out(repoView),
			"gh api repos/o/r/rules/branches/main":                      out("[]"),
			"fetch --prune origin":                                      out(""),
		}
	}

	for _, tc := range []struct {
		name  string
		codex CommandOutput
		want  onboard.Status
	}{
		{"codex signed in", out("Logged in using ChatGPT\n"), onboard.OK},
		{"codex signed out", CommandOutput{Exit: 1, Stdout: "Not logged in\n"}, onboard.Warn},
		{"codex exit 0 not logged in", CommandOutput{Exit: 0, Stderr: "Not logged in\n"}, onboard.Warn},
		{"codex missing", CommandOutput{Exit: 127}, onboard.Warn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := makeFake(tc.codex)
			rs := StartChecks(context.Background(), DoctorOptions{Folder: root, DataDir: dataDir, RunCommand: f.run})
			r := findResult(t, rs, "start codex")
			if r.Status != tc.want {
				t.Fatalf("start codex = %+v, want %v", r, tc.want)
			}
			if r.Status == onboard.Warn && !strings.Contains(r.Fix, "codex login") {
				t.Errorf("start codex fix = %q", r.Fix)
			}
		})
	}

	// Default project (second_reviewer=opus) does not check codex.
	other := t.TempDir()
	f := makeFake(CommandOutput{Exit: 127})
	f["rev-parse --show-toplevel"] = out(other)
	for _, r := range StartChecks(context.Background(), DoctorOptions{Folder: other, DataDir: dataDir, RunCommand: f.run}) {
		if r.Name == "start codex" {
			t.Fatalf("unexpected start codex check for default settings: %+v", r)
		}
	}

	// Empty DataDir means no project state (check claude only), even when XDG_DATA_HOME has state.
	t.Setenv("XDG_DATA_HOME", dataDir)
	f2 := makeFake(CommandOutput{Exit: 127})
	for _, r := range StartChecks(context.Background(), DoctorOptions{Folder: root, RunCommand: f2.run}) {
		if r.Name == "start codex" {
			t.Fatalf("empty DataDir must not read project state: %+v", r)
		}
	}
}

func TestStartCheckNoTestingImport(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "startcheck.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range f.Imports {
		if imp.Path.Value == `"testing"` {
			t.Fatalf("startcheck.go must not import testing")
		}
	}
}

func TestStartChecksTempRepos(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	hybrid := func(ctx context.Context, dir string, env []string, argv ...string) CommandOutput {
		if slices.Contains(argv, "fetch") {
			return out("") // The fixture already has the tracking ref.
		}
		if argv[0] == "git" {
			return SystemRunCommand(ctx, dir, env, argv...)
		}
		if argv[0] == "claude" {
			return out(`{"loggedIn":true}`)
		}
		return CommandOutput{Exit: 1, Stderr: "no gh"}
	}

	t.Run("empty local repo with no remote fails with no GitHub remote", func(t *testing.T) {
		empty := t.TempDir()
		runGit(empty, "init", "-b", "main")
		rs := StartChecks(context.Background(), DoctorOptions{Folder: empty, RunCommand: hybrid})
		r := findResult(t, rs, "github remote")
		if r.Status != onboard.Fail || !strings.Contains(r.Detail, "no GitHub remote") || !strings.Contains(r.Fix, "git remote add origin") {
			t.Fatalf("no-remote empty repo github remote = %+v", r)
		}
		if pr := findResult(t, rs, "pr workflow"); pr.Status != onboard.Skip {
			t.Fatalf("no-remote empty repo pr workflow = %+v, want Skip", pr)
		}
	})

	t.Run("repo with empty bare remote skips pr workflow", func(t *testing.T) {
		bare := t.TempDir()
		runGit(bare, "init", "--bare", "-b", "main")
		work := t.TempDir()
		runGit(work, "init", "-b", "main")
		runGit(work, "remote", "add", "origin", bare)
		rs := StartChecks(context.Background(), DoctorOptions{Folder: work, RunCommand: hybrid})
		if pr := findResult(t, rs, "pr workflow"); pr.Status != onboard.Skip {
			t.Fatalf("empty bare remote (non-GitHub) pr workflow = %+v, want Skip", pr)
		}
		runGit(work, "remote", "set-url", "--push", "origin", "https://github.com/o/r.git")
		rs = StartChecks(context.Background(), DoctorOptions{Folder: work, RunCommand: hybrid})
		if pr := findResult(t, rs, "pr workflow"); pr.Status != onboard.Skip {
			t.Fatalf("empty bare remote (GitHub pushurl) pr workflow = %+v, want Skip", pr)
		}
	})

	t.Run("empty remote repo fails with push a first commit and skips downstream", func(t *testing.T) {
		res := runStart(t, goodWorkflow, startFake{
			"gh repo view o/r --json defaultBranchRef,viewerPermission": out(`{"defaultBranchRef":null,"viewerPermission":"ADMIN"}`),
			"symbolic-ref --quiet --short HEAD":                         out("main"),
		})
		r := res["github remote"]
		if r.Status != onboard.Fail || !strings.Contains(r.Fix, "push a first commit to main") {
			t.Fatalf("empty remote github remote = %+v", r)
		}
		for _, skipped := range []string{"pr workflow", "review rule", "base protection"} {
			if s := res[skipped]; s.Status != onboard.Skip {
				t.Fatalf("empty remote %s = %+v, want Skip", skipped, s)
			}
		}
	})

	t.Run("real repo with pull_request only in if fails pr workflow", func(t *testing.T) {
		bare := t.TempDir()
		runGit(bare, "init", "--bare", "-b", "main")
		work := t.TempDir()
		runGit(work, "init", "-b", "main")
		wfDir := filepath.Join(work, ".github", "workflows")
		if err := os.MkdirAll(wfDir, 0o750); err != nil {
			t.Fatal(err)
		}
		wf := "name: ci\non: push\njobs:\n  test:\n    if: github.event_name == 'pull_request'\n    runs-on: ubuntu-latest\n"
		if err := os.WriteFile(filepath.Join(wfDir, "ci.yml"), []byte(wf), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(work, "add", ".")
		runGit(work, "commit", "-m", "init")
		runGit(work, "remote", "add", "origin", bare)
		runGit(work, "push", "-u", "origin", "main")
		runGit(work, "remote", "set-head", "origin", "main")
		runGit(work, "remote", "set-url", "origin", "https://github.com/o/r.git")

		rs := StartChecks(context.Background(), DoctorOptions{Folder: work, RunCommand: hybrid})
		if r := findResult(t, rs, "pr workflow"); r.Status != onboard.Fail || r.Fix == "" {
			t.Fatalf("pr workflow with if: only = %+v", r)
		}
	})
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

func TestStartChecksReportsGitFailure(t *testing.T) {
	srv := hctest.New(t)
	srv.OnExec = func(hc.ExecParams) (hc.ExecResult, error) {
		code := 128
		return hc.ExecResult{Code: &code, Stderr: []byte("canary git failure")}, nil
	}
	for _, want := range []string{"git failed", "herdr-core unavailable"} {
		opts := DoctorOptions{Folder: t.TempDir(), HCSocket: srv.Path, RunCommand: startFake{}.run}
		rs := StartChecks(context.Background(), opts)
		if len(rs) != 1 || rs[0].Status != onboard.Fail || !strings.Contains(rs[0].Detail, want) ||
			rs[0].Fix == "" || strings.Contains(rs[0].Fix, "git remote add") {
			t.Errorf("want %q with its fix, got %+v", want, rs)
		}
		srv.Close()
	}
}

func TestStartChecksReportsWorkflowGitFailure(t *testing.T) {
	root := t.TempDir()
	srv := hctest.New(t)
	fake := startFake{
		"git --version":                                             out("git version 2.45.0"),
		"rev-parse --show-toplevel":                                 out(root),
		"remote get-url --all origin":                               out("https://github.com/o/r.git"),
		"remote get-url --push --all origin":                        out("https://github.com/o/r.git"),
		"symbolic-ref --quiet --short refs/remotes/origin/HEAD":     out("origin/main"),
		"rev-parse --verify --quiet origin/main":                    out("abc1234"),
		"ls-tree --name-only origin/main .github/workflows/":        out(".github/workflows/ci.yml"),
		"show origin/main:.github/workflows/ci.yml":                 out(goodWorkflow),
		"fetch --prune origin":                                      out(""),
		"gh repo view o/r --json defaultBranchRef,viewerPermission": out(repoView),
	}
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if p.Argv[0] == "gh" && (p.Cwd != root || p.Timeout == nil || *p.Timeout > 30 ||
			p.Env["GH_PROMPT_DISABLED"] != "1" || p.Env["GH_NO_UPDATE_NOTIFIER"] != "1") {
			t.Errorf("unexpected preflight gh request: %+v", p)
		}
		c := fake.run(context.Background(), p.Cwd, nil, p.Argv...)
		return hc.ExecResult{Code: &c.Exit, Stdout: []byte(c.Stdout), Stderr: []byte(c.Stderr), Truncated: p.Argv[1] == "show"}, nil
	}
	results := StartChecks(context.Background(), DoctorOptions{Folder: root, HCSocket: srv.Path, RunCommand: fake.run})
	if len(results) != 1 || results[0].Status != onboard.Fail ||
		!strings.Contains(results[0].Detail, "git failed") || !strings.Contains(results[0].Detail, "truncated") ||
		!strings.Contains(results[0].Fix, "git show ") {
		t.Fatalf("truncated workflow should report the failed git show: %+v", results)
	}
}

func TestStartChecksRefusesDifferentOriginTargets(t *testing.T) {
	for _, name := range []string{"missing", "only remote is not origin", "mismatched", "multiple", "rewritten"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			gitIn(t, root, "init", "-b", "main")
			if name == "only remote is not origin" {
				gitIn(t, root, "remote", "add", "upstream", "https://github.com/acme/demo.git")
			} else if name != "missing" {
				gitIn(t, root, "remote", "add", "origin", "https://github.com/acme/demo.git")
			}
			switch name {
			case "mismatched":
				gitIn(t, root, "remote", "set-url", "--push", "origin", "git@github.com:acme/other.git")
			case "multiple":
				gitIn(t, root, "config", "--add", "remote.origin.pushurl", "git@github.com:acme/demo.git")
				gitIn(t, root, "config", "--add", "remote.origin.pushurl", "git@github.com:acme/other.git")
			case "rewritten":
				gitIn(t, root, "config", "url.https://github.com/acme/other.git.pushInsteadOf", "https://github.com/acme/demo.git")
			}
			srv := hctest.New(t)
			srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
				if p.Argv[0] == "gh" || slices.Contains(p.Argv, "fetch") {
					t.Errorf("invalid origin reached %v", p.Argv)
				}
				return hctest.GitExec(p)
			}
			rs := StartChecks(context.Background(), DoctorOptions{Folder: root, HCSocket: srv.Path,
				RunCommand: startFake{"claude auth status --json": out(`{"loggedIn":true}`)}.run})
			r := findResult(t, rs, "github remote")
			fix := "same GitHub repository"
			if name == "missing" || name == "only remote is not origin" {
				fix = "git remote add origin"
				if gh := findResult(t, rs, "gh push access"); gh.Status != onboard.Skip {
					t.Fatalf("gh push access = %+v, want skip without origin", gh)
				}
			}
			if r.Status != onboard.Fail || !strings.Contains(r.Fix, fix) {
				t.Fatalf("origin preflight = %+v, want failure with %q", r, fix)
			}
		})
	}
}
