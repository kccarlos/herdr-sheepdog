package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
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
	run := o.RunCommand
	o.RunCommand = func(ctx context.Context, dir string, env []string, argv ...string) CommandOutput {
		if argv[0] == "git" { // a credential prompt must fail fast, not wait
			env = append(slices.Clone(env), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
		}
		return run(ctx, dir, env, argv...)
	}
	r := startRepo{root: gitLine(ctx, o, o.Folder, "rev-parse", "--show-toplevel")}
	if r.root == "" {
		r.root = o.Folder
	}
	r.remote = world.PushRemote(strings.Fields(gitOut(ctx, o, r.root, "remote")))
	r.slug = resolveRemoteSlug(ctx, o, r.root, r.remote)
	var out []onboard.Result
	if r.remote != "" { // like the engine
		if !cmdOK(o.run(ctx, o.TempDir, "git", "-C", r.root, "fetch", "--prune", r.remote)) {
			out = append(out, resWarn("start fetch", "could not fetch "+r.remote+", so the refs below may be stale", "run: git fetch "+r.remote+" (sign in to git if it asks)"))
		}
	}
	gh := checkStartGH(ctx, o, &r)
	out = append(out, checkStartCLI(ctx, o), gh, checkStartRemote(ctx, o, r), checkPRWorkflow(ctx, o, r))
	return append(out, checkStartProtection(ctx, o, r)...)
}

func checkStartCLI(ctx context.Context, o DoctorOptions) onboard.Result {
	const name = "start claude"
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
	const name, fix = "gh push access", "run: gh auth login (an account with push access to the repo)"
	if r.slug == "" {
		return resSkip(name, "no GitHub remote to check access to")
	}
	c := o.run(ctx, o.TempDir, "gh", "repo", "view", r.slug, "--json", "defaultBranchRef,viewerPermission")
	var v ghRepoView
	if !cmdOK(c) || json.Unmarshal([]byte(c.Stdout), &v) != nil {
		return resFail(name, "cannot read "+r.slug+" with gh (not installed, not signed in, or no access): "+cleanReason(firstNonEmpty(c.Stderr, "no answer")),
			"install GitHub CLI (https://cli.github.com) if missing, then "+fix)
	}
	if p := v.ViewerPermission; p != "WRITE" && p != "MAINTAIN" && p != "ADMIN" {
		return resFail(name, fmt.Sprintf("your access to %s is %q, not push", r.slug, p), fix)
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

// checkPRWorkflow reads the workflows on the remote base the engine builds
// on, not the working tree. Known gap: the match is a text search, so a
// pull_request inside an if: expression, or a branches: filter that excludes
// the base, still passes.
func checkPRWorkflow(ctx context.Context, o DoctorOptions, r startRepo) onboard.Result {
	const name = "pr workflow"
	ref := r.remote + "/HEAD"
	if r.view != nil {
		ref = r.remote + "/" + r.view.DefaultBranchRef.Name
	}
	for _, f := range strings.Split(gitOut(ctx, o, r.root, "ls-tree", "--name-only", ref, ".github/workflows/"), "\n") {
		if ext := filepath.Ext(f); ext != ".yml" && ext != ".yaml" {
			continue
		}
		if prTriggerRe.MatchString(gitOut(ctx, o, r.root, "show", ref+":"+f)) {
			return resOK(name, filepath.Base(f)+" on "+ref+" runs on pull requests")
		}
	}
	return resFail(name, "no workflow in .github/workflows on "+ref+" runs on pull_request, so no PR would ever get checks",
		"add .github/workflows/ci.yml with `on: pull_request` that runs your tests, commit it to the base branch and push")
}

// checkStartProtection reads protection and rulesets of the base: a required
// approving review fails (the engine merges itself); protection itself is only
// reported. Any reader can see `protected` and the rulesets; the classic
// approval count needs admin, so a refused read warns.
func checkStartProtection(ctx context.Context, o DoctorOptions, r startRepo) []onboard.Result {
	const review, prot = "review rule", "base protection"
	if r.view == nil {
		return []onboard.Result{resSkip(review, "needs the gh check above to pass"), resSkip(prot, "needs the gh check above to pass")}
	}
	base := r.view.DefaultBranchRef.Name
	path := "repos/" + r.slug + "/branches/" + url.PathEscape(base)
	byHand := "check by hand that " + base + " does not require an approving review"
	unread := func(what string, c CommandOutput) []onboard.Result {
		return []onboard.Result{resWarn(review, "could not read "+what+" of "+base+": "+cleanReason(firstNonEmpty(c.Stderr, c.Stdout, "no answer")), byHand), resSkip(prot, "unknown")}
	}
	bc := o.run(ctx, r.root, "gh", "api", path)
	var b struct {
		Protected bool `json:"protected"`
	}
	if !cmdOK(bc) || json.Unmarshal([]byte(bc.Stdout), &b) != nil {
		return unread("the branch", bc)
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			Count int  `json:"required_approving_review_count"`
			Owner bool `json:"require_code_owner_review"`
		} `json:"parameters"`
	}
	rc := o.run(ctx, r.root, "gh", "api", "repos/"+r.slug+"/rules/branches/"+url.PathEscape(base))
	noRulesets := strings.Contains(rc.Stderr+rc.Stdout, "403") && strings.Contains(strings.ToLower(rc.Stderr+rc.Stdout), "upgrade to github pro")
	switch {
	case cmdOK(rc):
		_ = json.Unmarshal([]byte(rc.Stdout), &rules)
	case !noRulesets:
		return unread("rulesets", rc)
	}
	need, owner := 0, false
	for _, x := range rules {
		if x.Type == "pull_request" {
			need = max(need, x.Parameters.Count)
			owner = owner || x.Parameters.Owner
		}
	}
	var warn *CommandOutput
	if b.Protected {
		pc := o.run(ctx, r.root, "gh", "api", path+"/protection")
		var p struct {
			Reviews struct {
				Count int  `json:"required_approving_review_count"`
				Owner bool `json:"require_code_owner_reviews"`
			} `json:"required_pull_request_reviews"`
		}
		all := pc.Stderr + pc.Stdout
		notClassic := strings.Contains(all, "404") && strings.Contains(strings.ToLower(all), "not protected") // protected by a ruleset only
		if !notClassic && (!cmdOK(pc) || json.Unmarshal([]byte(pc.Stdout), &p) != nil) {
			warn = &pc
		}
		need, owner = max(need, p.Reviews.Count), owner || p.Reviews.Owner
	}
	res := []onboard.Result{resOK(review, base+" requires no approving review")}
	switch {
	case need > 0 || owner:
		what := fmt.Sprintf("%d approving review(s)", need)
		if need == 0 {
			what = "a code-owner review"
		} else if owner {
			what += " and a code-owner review"
		}
		res[0] = resFail(review, fmt.Sprintf("%s requires %s and the engine merges by itself", base, what),
			"in GitHub, set required approvals on "+base+" to 0 and turn off code-owner review in its branch protection rule or ruleset")
	case warn != nil:
		res[0] = unread("the approval count (protected; needs admin)", *warn)[0]
	}
	if b.Protected || len(rules) > 0 {
		return append(res, resOK(prot, base+" has a branch protection rule or ruleset"))
	}
	return append(res, resWarn(prot, base+" has no branch protection or ruleset (not required)", "optional: require the CI check on "+base+" in GitHub settings"))
}
