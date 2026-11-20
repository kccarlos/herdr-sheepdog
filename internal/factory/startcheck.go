package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// prTriggerRe finds a pull_request trigger on a line that is not a comment.
var prTriggerRe = regexp.MustCompile(`(?m)^[^#\n]*\bpull_request(_target)?\b`)

// ghRepoView is the part of `gh repo view --json` the start checks read.
type ghRepoView struct {
	DefaultBranchRef struct {
		Name string `json:"name"`
	} `json:"defaultBranchRef"`
	ViewerPermission string `json:"viewerPermission"`
}

// startRepo is what the checks learn about the folder's GitHub repo.
type startRepo struct {
	root, remote, slug string
	view               *ghRepoView
}

func (o DoctorOptions) run(ctx context.Context, dir string, argv ...string) CommandOutput {
	c, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	defer cancel()
	return o.RunCommand(c, dir, nil, argv...)
}

func cmdOK(c CommandOutput) bool { return c.Err == nil && !c.TimedOut && c.Exit == 0 }

// StartChecks are the checks `sheepdog factory start` makes before it starts
// anything (design.md 11): each line is ok, warn or fail with one fix. All
// external calls go through opts.RunCommand, so tests use a fake.
func StartChecks(ctx context.Context, opts DoctorOptions) []onboard.Result {
	o := opts.withDefaults()
	r := startRepo{root: gitLine(ctx, o, o.Folder, "rev-parse", "--show-toplevel")}
	if r.root == "" {
		r.root = o.Folder
	}
	r.remote = resolvePushRemote(ctx, o, r.root, strings.Fields(gitOut(ctx, o, r.root, "remote")))
	r.slug = resolveRemoteSlug(ctx, o, r.root, r.remote)
	gh := checkStartGH(ctx, o, &r)
	out := []onboard.Result{checkStartCLI(ctx, o), gh, checkStartRemote(ctx, o, r), checkPRWorkflow(r.root)}
	return append(out, checkStartProtection(ctx, o, r)...)
}

func checkStartCLI(ctx context.Context, o DoctorOptions) onboard.Result {
	const name = "coding cli"
	c := o.run(ctx, o.TempDir, "claude", "auth", "status", "--json")
	switch {
	case c.Err != nil || c.Exit == 127:
		return resFail(name, "claude (the CLI the factory runs) is not installed", "install Claude Code, then run: claude auth login")
	case !claudeLoggedIn(c):
		return resFail(name, "claude is not signed in", "run: claude auth login")
	}
	return resOK(name, "claude is signed in")
}

func checkStartGH(ctx context.Context, o DoctorOptions, r *startRepo) onboard.Result {
	const name, fix = "gh", "run: gh auth login (an account with push access to the repo)"
	if c := o.run(ctx, r.root, "gh", "auth", "status"); !cmdOK(c) {
		return resFail(name, "gh is not installed or not signed in", "install GitHub CLI (https://cli.github.com), then "+fix)
	}
	if r.slug == "" {
		return resOK(name, "signed in (no GitHub remote to check access to)")
	}
	c := o.run(ctx, r.root, "gh", "repo", "view", r.slug, "--json", "defaultBranchRef,viewerPermission")
	var v ghRepoView
	if !cmdOK(c) || json.Unmarshal([]byte(c.Stdout), &v) != nil {
		return resFail(name, "signed in, but cannot read "+r.slug+": "+cleanReason(firstNonEmpty(c.Stderr, "no answer")), fix)
	}
	if p := v.ViewerPermission; p != "WRITE" && p != "MAINTAIN" && p != "ADMIN" {
		return resFail(name, fmt.Sprintf("signed in, but your access to %s is %q, not push", r.slug, p), fix)
	}
	r.view = &v
	return resOK(name, fmt.Sprintf("signed in; push access to %s", r.slug))
}

func checkStartRemote(ctx context.Context, o DoctorOptions, r startRepo) onboard.Result {
	const name = "github remote"
	switch {
	case r.slug == "":
		return resFail(name, "no GitHub remote in "+r.root, "add one: git remote add origin https://github.com/<owner>/<repo>.git")
	case r.view == nil:
		return resSkip(name, r.slug+": needs the gh check above to pass")
	}
	base := r.view.DefaultBranchRef.Name
	local := strings.TrimPrefix(gitLine(ctx, o, r.root, "symbolic-ref", "--quiet", "--short", "refs/remotes/"+r.remote+"/HEAD"), r.remote+"/")
	fix := "run: git remote set-head " + r.remote + " -a"
	switch {
	case local == "":
		return resWarn(name, r.slug+" default branch is "+base+", but "+r.remote+"/HEAD is not set here", fix)
	case local != base:
		return resFail(name, fmt.Sprintf("%s/HEAD says %s but the default branch on GitHub is %s", r.remote, local, base), fix+" (or change the default branch on GitHub)")
	}
	return resOK(name, fmt.Sprintf("%s is %s; its default branch %s is the base", r.remote, r.slug, base))
}

func checkPRWorkflow(root string) onboard.Result {
	const name = "pr workflow"
	files, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && prTriggerRe.Match(b) { //nolint:gosec // a workflow file in the repo being checked
			return resOK(name, filepath.Base(f)+" runs on pull requests")
		}
	}
	return resFail(name, "no workflow in .github/workflows runs on pull_request, so no PR would ever get checks",
		"add .github/workflows/ci.yml with `on: pull_request` that runs your tests and commit it to the base branch")
}

// checkStartProtection reads branch protection and rulesets of the base: a
// required approving review fails (the engine merges itself); protection
// itself is only reported.
func checkStartProtection(ctx context.Context, o DoctorOptions, r startRepo) []onboard.Result {
	const review, prot = "review rule", "base protection"
	if r.view == nil {
		return []onboard.Result{resSkip(review, "needs the gh check above to pass"), resSkip(prot, "needs the gh check above to pass")}
	}
	base := r.view.DefaultBranchRef.Name
	pc := o.run(ctx, r.root, "gh", "api", "repos/"+r.slug+"/branches/"+base+"/protection")
	if !cmdOK(pc) && !strings.Contains(pc.Stderr+pc.Stdout, "404") {
		return []onboard.Result{resWarn(review, "could not read branch protection of "+base+": "+cleanReason(firstNonEmpty(pc.Stderr, pc.Stdout, "no answer")),
			"check by hand that "+base+" does not require an approving review, or sign in as a repo admin"), resSkip(prot, "unknown")}
	}
	var p struct {
		Reviews struct {
			Count int `json:"required_approving_review_count"`
		} `json:"required_pull_request_reviews"`
	}
	if cmdOK(pc) {
		_ = json.Unmarshal([]byte(pc.Stdout), &p)
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			Count int `json:"required_approving_review_count"`
		} `json:"parameters"`
	}
	if rc := o.run(ctx, r.root, "gh", "api", "repos/"+r.slug+"/rules/branches/"+base); cmdOK(rc) {
		_ = json.Unmarshal([]byte(rc.Stdout), &rules)
	}
	need := p.Reviews.Count
	for _, x := range rules {
		if x.Type == "pull_request" {
			need = max(need, x.Parameters.Count)
		}
	}
	res := []onboard.Result{resOK(review, base+" requires no approving review")}
	if need > 0 {
		res[0] = resFail(review, fmt.Sprintf("%s requires %d approving review(s) and the engine merges by itself", base, need),
			"in GitHub, set required approvals on "+base+" to 0 in its branch protection rule or ruleset")
	}
	if cmdOK(pc) || len(rules) > 0 {
		return append(res, resOK(prot, base+" has a branch protection rule or ruleset"))
	}
	return append(res, resWarn(prot, base+" has no branch protection or ruleset (not required)", "optional: require the CI check on "+base+" in GitHub settings"))
}
