package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

var gitVersionRe = regexp.MustCompile(`git version (\d+)\.(\d+)`)

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
	empty              bool
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
	run, gitRun := o.RunCommand, o.RunCommand
	inHC := opts.HCSocket != ""
	if inHC {
		gitRun = hcExecRunner(o.HCSocket)
	}
	var gitFailure []onboard.Result
	o.RunCommand = func(ctx context.Context, dir string, env []string, argv ...string) CommandOutput {
		if argv[0] != "git" {
			return run(ctx, dir, env, argv...)
		}
		if gitFailure != nil {
			return CommandOutput{Exit: -1}
		}
		env = append(slices.Clone(env), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
		c := gitRun(ctx, dir, env, argv...)
		args := argv[1:]
		if args[0] == "-C" {
			args = args[2:]
		}
		verb := args[0]
		missing := c.Err == nil && c.Exit == 1 && (verb == "config" || verb == "symbolic-ref" || verb == "rev-parse" && slices.Contains(args, "--quiet"))
		if inHC && !cmdOK(c) && !missing && (verb != "fetch" || c.Err != nil || c.TimedOut) {
			detail, fix := "git failed", "run git "+strings.Join(args, " ")+" in the project folder and resolve the error"
			var gitErr *world.GitError
			var rpcErr *hc.Error
			if errors.As(c.Err, &gitErr) && gitErr.RPC != nil && (!errors.As(gitErr.RPC, &rpcErr) || rpcErr.Kind == "unavailable") {
				detail, fix = "herdr-core unavailable", "run: hc server start (or check the configured hc socket)"
			}
			gitFailure = []onboard.Result{resFail("start git", detail+": "+cleanReason(firstNonEmpty(c.Stderr, errText(c.Err))), fix)}
		}
		return c
	}
	top := gitLine(ctx, o, o.Folder, "rev-parse", "--show-toplevel")
	r := startRepo{root: firstNonEmpty(top, o.Folder)}
	r.remote = world.PushRemote(strings.Fields(gitOut(ctx, o, r.root, "remote")))
	r.slug = resolveRemoteSlug(ctx, o, r.root, r.remote)
	out := []onboard.Result{checkGitVersion(ctx, o)}
	if r.remote != "" { // like the engine
		fetch, cancel := context.WithTimeout(ctx, world.GitNetworkTimeout)
		defer cancel()
		if !cmdOK(o.RunCommand(fetch, o.TempDir, nil, "git", "-C", r.root, "fetch", "--prune", r.remote)) {
			out = append(out, resWarn("start fetch", "git failed to fetch "+r.remote+", so the refs below may be stale", "run: git fetch "+r.remote+" (sign in to git if it asks)"))
		}
	}
	if gitFailure != nil {
		return gitFailure
	}
	gh := checkStartGH(ctx, o, &r)
	r.empty = r.view != nil && r.view.DefaultBranchRef.Name == ""
	out = append(append(out, checkStartCLIs(ctx, o, r)...), gh, checkStartRemote(ctx, o, r), checkPRWorkflow(ctx, o, r))
	if gitFailure != nil {
		return gitFailure
	}
	return append(out, checkStartProtection(ctx, o, r)...)
}

func checkGitVersion(ctx context.Context, o DoctorOptions) onboard.Result {
	const name, fix = "git version", "install or upgrade git to 2.38 or later (needed by git merge-tree --write-tree)"
	c := o.run(ctx, o.TempDir, "git", "--version")
	m := gitVersionRe.FindStringSubmatch(c.Stdout)
	if !cmdOK(c) || m == nil {
		return resFail(name, "could not read git version: "+cleanReason(firstNonEmpty(c.Stderr, c.Stdout, "no answer")), fix)
	}
	maj, _ := strconv.Atoi(m[1])
	if minV, _ := strconv.Atoi(m[2]); maj < 2 || (maj == 2 && minV < 38) {
		msg := fmt.Sprintf("git %d.%d is older than 2.38 (needed by git merge-tree --write-tree)", maj, minV)
		return resFail(name, msg, fix)
	}
	return resOK(name, firstLine(c.Stdout))
}

// Slice 5b's Resolve will replace this with the resolved worker and reviewer CLIs.
func checkStartCLIs(ctx context.Context, o DoctorOptions, r startRepo) []onboard.Result {
	out := []onboard.Result{checkStartCLI(ctx, o, "claude", "Claude Code", "run: claude auth login",
		claudeLoggedIn, "claude", "auth", "status", "--json")}
	if p := (Paths{DataDir: o.DataDir}); o.DataDir != "" {
		if id, err := findStoredProject(p, r.root); err == nil {
			if s, err := LoadState(p.State(id)); err == nil && s.Settings.SecondReviewer == "codex" {
				out = append(out, checkStartCLI(ctx, o, "codex", "Codex CLI", "run: codex login",
					codexLoggedIn, "codex", "login", "status"))
			}
		}
	}
	return out
}

func checkStartCLI(ctx context.Context, o DoctorOptions, cli, pkg, loginFix string,
	ok func(CommandOutput) bool, argv ...string) onboard.Result {
	name, c := "start "+cli, o.run(ctx, o.TempDir, argv...)
	if c.Err != nil || c.Exit == 127 {
		return resFail(name, cli+" is not installed", "install "+pkg+", then "+loginFix)
	} else if !ok(c) {
		return resFail(name, cli+" is not signed in", loginFix)
	}
	return resOK(name, cli+" is signed in")
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
	case r.empty:
		base := firstNonEmpty(gitLine(ctx, o, r.root, "symbolic-ref", "--quiet", "--short", "HEAD"), "main")
		return resFail(name, r.slug+" is an empty repository (no commits or default branch)",
			"push a first commit to "+base)
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

// checkPRWorkflow parses the YAML `on:` trigger of workflows on the remote base
// the engine builds on, not the working tree. Known gap: a `branches:` filter
// limited to other branches still passes.
func checkPRWorkflow(ctx context.Context, o DoctorOptions, r startRepo) onboard.Result {
	const name = "pr workflow"
	switch {
	case r.empty:
		return resSkip(name, "repo is empty (no default branch yet)")
	case r.slug == "":
		return resSkip(name, "needs the github remote check above to pass")
	}
	ref := r.remote + "/HEAD"
	if r.view != nil {
		ref = r.remote + "/" + r.view.DefaultBranchRef.Name
	}
	if gitLine(ctx, o, r.root, "rev-parse", "--verify", "--quiet", ref) == "" {
		if r.view != nil {
			return resFail(name, "cannot read "+ref+" locally", "run: git fetch "+r.remote)
		}
		return resSkip(name, ref+" does not exist yet (push the base branch)")
	}
	var filteredFile string
	for _, f := range strings.Split(gitOut(ctx, o, r.root, "ls-tree", "--name-only", ref, ".github/workflows/"), "\n") {
		if ext := filepath.Ext(f); ext != ".yml" && ext != ".yaml" {
			continue
		}
		if hasPR, filtered := parsePRWorkflow(gitOut(ctx, o, r.root, "show", ref+":"+f)); hasPR && !filtered {
			return resOK(name, filepath.Base(f)+" on "+ref+" runs on pull requests")
		} else if hasPR && filteredFile == "" {
			filteredFile = filepath.Base(f)
		}
	}
	if filteredFile != "" {
		return resWarn(name, filteredFile+" on "+ref+" filters on.pull_request with paths/paths-ignore; "+
			"a PR touching only filtered paths gets no checks and waits at N35",
			"remove paths/paths-ignore from on.pull_request or add an unfiltered PR workflow")
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
	if r.empty || r.view == nil {
		msg := "needs the gh check above to pass"
		if r.empty {
			msg = "repo is empty (no default branch yet)"
		}
		return []onboard.Result{resSkip(review, msg), resSkip(prot, msg)}
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

func isPRTrigger(k string) bool { return k == "pull_request" || k == "pull_request_target" }

// parsePRWorkflow inspects a workflow's top-level YAML `on:` trigger (string,
// list or map). It returns hasPR when pull_request or pull_request_target is a
// trigger, and filtered when every such trigger defines paths or paths-ignore.
func parsePRWorkflow(src string) (bool, bool) {
	lines := strings.Split(strings.ReplaceAll(strings.TrimPrefix(src, "\uFEFF"), "\r\n", "\n"), "\n")
	for i, ln := range lines {
		if j := yamlScan(ln, func(j int, c byte) bool {
			return c == '#' && (j == 0 || ln[j-1] == ' ' || ln[j-1] == '\t')
		}); j >= 0 {
			ln = ln[:j]
		}
		lines[i] = strings.TrimRight(ln, " \t")
	}
	collect := func(i int, b string) string {
		for _, d := yamlFlow(b); len(b) > 0 && (b[0] == '[' || b[0] == '{') && d > 0 && i+1 < len(lines); {
			i, b = i+1, b+" "+strings.TrimSpace(lines[i+1])
			_, d = yamlFlow(b)
		}
		return b
	}
	isPaths := func(s string) bool { k, _, ok := yamlKey(s); return ok && (k == "paths" || k == "paths-ignore") }
	trimTrigger := func(s string) bool { return isPRTrigger(strings.Trim(strings.TrimSpace(s), "\"'")) }
	hasPaths := func(v string) bool {
		items, _ := yamlFlow(strings.TrimSuffix(strings.TrimPrefix(v, "{"), "}"))
		return len(v) >= 2 && v[0] == '{' && v[len(v)-1] == '}' && slices.ContainsFunc(items, isPaths)
	}
	for i, ln := range lines {
		if k, rest, ok := yamlKey(ln); len(ln) > 0 && ln[0] != ' ' && ln[0] != '\t' && ok && k == "on" {
			if strings.HasPrefix(rest, "&") {
				_, rest, _ = strings.Cut(strings.ReplaceAll(rest, "\t", " "), " ")
			}
			if rest = strings.TrimSpace(rest); rest != "" {
				v := collect(i, rest)
				if len(v) < 2 || (v[0] != '[' || v[len(v)-1] != ']') && (v[0] != '{' || v[len(v)-1] != '}') {
					return trimTrigger(v), false
				}
				items, _ := yamlFlow(v[1 : len(v)-1])
				if v[0] == '[' {
					return slices.ContainsFunc(items, trimTrigger), false
				}
				hasPR, unfilt := false, false
				for _, it := range items {
					if tk, val, tok := yamlKey(it); tok && isPRTrigger(tk) {
						hasPR, unfilt = true, unfilt || !hasPaths(val)
					} else if !tok && trimTrigger(it) {
						hasPR, unfilt = true, true
					}
				}
				return hasPR, hasPR && !unfilt
			}
			base, child, pr, filt, inPR := -1, -1, 0, 0, false
			for j := i + 1; j < len(lines); j++ {
				tr := strings.TrimSpace(lines[j])
				if tr == "" {
					continue
				}
				ind := len(lines[j]) - len(strings.TrimLeft(lines[j], " "))
				if (ind == 0 && tr[0] != '-') || (base >= 0 && ind < base) {
					break
				}
				if base < 0 {
					base = ind
				}
				if ind > base {
					if inPR && child < 0 {
						child = ind
					}
					if inPR && ind == child && isPaths(tr) {
						filt, inPR = filt+1, false
					}
					continue
				}
				inPR, child = false, -1
				if tr[0] == '-' && (len(tr) == 1 || tr[1] == ' ' || tr[1] == '\t') && trimTrigger(tr[1:]) {
					return true, false
				}
				if tk, val, tok := yamlKey(tr); tok && isPRTrigger(tk) {
					pr, inPR = pr+1, val == ""
					if val != "" && hasPaths(collect(j, val)) {
						filt++
					}
				}
			}
			return pr > 0, pr > 0 && filt == pr
		}
	}
	return false, false
}

func yamlScan(s string, stop func(int, byte) bool) int {
	for i, q := 0, byte(0); i < len(s); i++ {
		if q == '"' && s[i] == '\\' {
			i++
		} else if q != 0 && s[i] == q {
			q = 0
		} else if q == 0 && (s[i] == '\'' || s[i] == '"') {
			q = s[i]
		} else if q == 0 && stop(i, s[i]) {
			return i
		}
	}
	return -1
}

func yamlFlow(s string) ([]string, int) {
	out, depth, start := []string(nil), 0, 0
	yamlScan(s, func(i int, c byte) bool {
		if depth += map[byte]int{'[': 1, '{': 1, ']': -1, '}': -1}[c]; c == ',' && depth == 0 {
			out, start = append(out, strings.TrimSpace(s[start:i])), i+1
		}
		return false
	})
	return append(out, strings.TrimSpace(s[start:])), depth
}

func yamlKey(s string) (string, string, bool) {
	s = strings.TrimSpace(s)
	if i := yamlScan(s, func(i int, c byte) bool {
		return c == ':' && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\t')
	}); s != "" && s[0] != '-' && i >= 0 {
		return strings.Trim(strings.TrimSpace(s[:i]), "\"'"), strings.TrimSpace(s[i+1:]), true
	}
	return "", "", false
}
