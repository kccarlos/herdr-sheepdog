package world

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// Sweep runs all sweep finders for a project (design.md 11.3): stray terminals,
// orphan or done-task worktrees, orphan task/* branches, and local base ahead.
func (g *Git) Sweep(ctx context.Context, client *hc.Client, repo, worktreeRoot string, s flow.State) []flow.SweepProbe {
	lbProbe := flow.SweepProbe{Probe: "local_base"}
	if ref, _, err := g.remoteBaseCommit(ctx, repo, false); err != nil {
		lbProbe.Unknown = err.Error()
	} else {
		rb := strings.TrimPrefix(ref, "refs/remotes/")
		_, base, _ := strings.Cut(rb, "/")
		sha, e1 := g.gitCmd(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+base+"^{commit}")
		out, e2 := g.gitCmd(ctx, repo, "rev-list", "--count", ref+"..refs/heads/"+base)
		if n, e3 := strconv.Atoi(strings.TrimSpace(out)); cmp.Or(e1, e2, e3) != nil {
			lbProbe.Unknown = cmp.Or(e1, e2, e3).Error()
		} else if n > 0 {
			lbProbe.Items = []flow.SweepItem{{Kind: "local_base", Key: sha, Summary: fmt.Sprintf("the local %s is %d %s ahead of %s", base, n, map[bool]string{true: "commit", false: "commits"}[n == 1], rb)}}
		}
	}
	if s.Project.ID == "" {
		return []flow.SweepProbe{{Probe: "terminals", Unknown: "no project state"}, {Probe: "worktrees", Unknown: "no project state"}, {Probe: "branches", Unknown: "no project state"}, lbProbe}
	}
	ownedBr, doneWT := map[string]bool{}, map[string]string{}
	for _, t := range s.Tasks {
		cleaned := flow.Count(t.Acts, flow.AskCleanup) > 0
		for _, a := range t.Attempts {
			ownedBr[a.Branch], cleaned = true, cleaned || flow.Count(a.Acts, flow.AskCleanup) > 0
		}
		for _, a := range t.Attempts {
			doneWT[a.Worktree] = ""
			if t.Done != nil && cleaned {
				doneWT[a.Worktree] = t.ID
			}
		}
	}
	wtProbe, brProbe, wtBr := flow.SweepProbe{Probe: "worktrees"}, flow.SweepProbe{Probe: "branches"}, map[string]bool{}
	if roots, branches, err := g.sweepWorktreePaths(ctx, repo, worktreeRoot, doneWT); err != nil {
		wtProbe.Unknown = err.Error()
	} else {
		mainRepo, wtRoot, owned := roots[repo], roots[worktreeRoot], map[string]string{}
		for path, task := range doneWT {
			owned[roots[path]] = cmp.Or(task, owned[roots[path]])
		}
		for _, entry := range branches {
			wt, br := roots[entry.path], entry.branch
			if wt == "" || wt == mainRepo {
				continue
			}
			note := map[bool]string{true: " (branch " + br + ")"}[br != ""]
			if tid, ok := owned[wt]; tid != "" {
				wtProbe.Items, wtBr[br] = append(wtProbe.Items, flow.SweepItem{Kind: "worktree", Key: wt, Summary: fmt.Sprintf("a leftover worktree %s of done task %s%s", wt, tid, note)}), true
			} else if rel, rerr := filepath.Rel(wtRoot, wt); !ok && wtRoot != "" && rerr == nil && rel != "." && filepath.IsLocal(rel) {
				wtProbe.Items, wtBr[br] = append(wtProbe.Items, flow.SweepItem{Kind: "worktree", Key: wt, Summary: fmt.Sprintf("a leftover worktree %s owned by no attempt%s", wt, note)}), true
			}
		}
	}
	if wtProbe.Unknown != "" {
		brProbe.Unknown = wtProbe.Unknown
	} else if out, err := g.gitCmd(ctx, repo, "branch", "--list", "--format=%(refname:short)", "task/*"); err != nil {
		brProbe.Unknown = err.Error()
	} else {
		for ln := range strings.Lines(out) {
			if br := strings.TrimSpace(ln); br != "" && !ownedBr[br] && !wtBr[br] {
				brProbe.Items = append(brProbe.Items, flow.SweepItem{Kind: "branch", Key: br, Summary: fmt.Sprintf("a leftover branch %s owned by no attempt", br)})
			}
		}
	}
	return []flow.SweepProbe{SweepTerminals(ctx, client, s), wtProbe, brProbe, lbProbe}
}

type sweepWorktree struct{ path, branch string }

func (g *Git) sweepWorktreePaths(ctx context.Context, repo, root string, owned map[string]string) (map[string]string, []sweepWorktree, error) {
	r, err := g.Run(ctx, repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, nil, err
	}
	paths := []string{repo, root}
	for path := range owned {
		paths = append(paths, path)
	}
	var listed []sweepWorktree
	for stanza := range strings.SplitSeq(string(r.Stdout), "\x00\x00") {
		var item sweepWorktree
		for field := range strings.SplitSeq(stanza, "\x00") {
			if path, ok := strings.CutPrefix(field, "worktree "); ok {
				item.path = path
			} else if branch, ok := strings.CutPrefix(field, "branch refs/heads/"); ok {
				item.branch = branch
			}
		}
		if item.path != "" {
			listed, paths = append(listed, item), append(paths, item.path)
		}
	}
	resolved, err := g.CanonicalPaths(ctx, repo, paths...)
	return resolved, listed, err
}

// SweepTerminals finds live factory-labelled terminals that no active run owns.
func SweepTerminals(ctx context.Context, client *hc.Client, s flow.State) flow.SweepProbe {
	if s.Project.ID == "" || client == nil {
		return flow.SweepProbe{Probe: "terminals", Unknown: map[bool]string{true: "no project state", false: "no hc client"}[s.Project.ID == ""]}
	}
	list, err := client.List(ctx, false)
	if err != nil {
		return flow.SweepProbe{Probe: "terminals", Unknown: err.Error()}
	}
	owned := map[string]bool{}
	mark := func(addr string, r *flow.RunRecord) {
		if r == nil {
			owned[addr] = true
		} else if r.State != flow.PhaseEnded {
			owned[addr], owned[r.Address], owned[r.ID] = true, true, true
		}
	}
	mark(s.Project.RunID, s.Project.Run)
	for _, t := range s.Tasks {
		for _, a := range t.Attempts {
			mark(a.RunID, a.Run)
			if a.Review != nil {
				mark(a.Review.RunID, a.Review.Run)
			}
		}
	}
	delete(owned, "")
	p := flow.SweepProbe{Probe: "terminals"}
	for _, t := range list.Terminals {
		if t.Labels["sheepdog.project"] == s.Project.ID && !owned[t.Address] && !owned[t.Name] && !owned[t.Labels["sheepdog.run"]] {
			p.Items = append(p.Items, flow.SweepItem{Kind: "stray_terminal", Key: t.Address, Summary: fmt.Sprintf("a stray agent terminal %s with factory labels", t.Address)})
		}
	}
	return p
}
