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
func Sweep(ctx context.Context, client *hc.Client, repo, worktreeRoot string, s flow.State) []flow.SweepProbe {
	lbProbe := flow.SweepProbe{Probe: "local_base"}
	if ref, _, err := remoteBaseCommit(ctx, repo, false); err != nil {
		lbProbe.Unknown = err.Error()
	} else {
		rb := strings.TrimPrefix(ref, "refs/remotes/")
		_, base, _ := strings.Cut(rb, "/")
		sha, e1 := gitCmd(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+base+"^{commit}")
		out, e2 := gitCmd(ctx, repo, "rev-list", "--count", ref+"..refs/heads/"+base)
		if n, e3 := strconv.Atoi(strings.TrimSpace(out)); cmp.Or(e1, e2, e3) != nil {
			lbProbe.Unknown = cmp.Or(e1, e2, e3).Error()
		} else if n > 0 {
			lbProbe.Items = []flow.SweepItem{{Kind: "local_base", Key: sha, Summary: fmt.Sprintf("the local %s is %d %s ahead of %s", base, n, map[bool]string{true: "commit", false: "commits"}[n == 1], rb)}}
		}
	}
	if s.Project.ID == "" {
		return []flow.SweepProbe{{Probe: "terminals", Unknown: "no project state"}, {Probe: "worktrees", Unknown: "no project state"}, {Probe: "branches", Unknown: "no project state"}, lbProbe}
	}
	ownedWT, ownedBr, doneWT := map[string]bool{}, map[string]bool{}, map[string]string{}
	for _, t := range s.Tasks {
		cleaned := flow.Count(t.Acts, flow.AskCleanup) > 0
		for _, a := range t.Attempts {
			ownedWT[realPath(a.Worktree)], ownedBr[a.Branch], cleaned = true, true, cleaned || flow.Count(a.Acts, flow.AskCleanup) > 0
		}
		for _, a := range t.Attempts {
			if t.Done != nil && cleaned {
				doneWT[realPath(a.Worktree)] = t.ID
			}
		}
	}
	wtProbe, brProbe, wtBr := flow.SweepProbe{Probe: "worktrees"}, flow.SweepProbe{Probe: "branches"}, map[string]bool{}
	if out, err := gitCmd(ctx, repo, "worktree", "list", "--porcelain"); err != nil {
		wtProbe.Unknown = err.Error()
	} else {
		mainRepo, wtRoot := realPath(repo), realPath(worktreeRoot)
		for stanza := range strings.SplitSeq(out, "\n\n") {
			var wt, br string
			for ln := range strings.Lines(stanza) {
				if raw, ok := strings.CutPrefix(strings.TrimSpace(ln), "worktree "); ok {
					wt = realPath(raw)
				} else if ref, ok := strings.CutPrefix(strings.TrimSpace(ln), "branch refs/heads/"); ok {
					br = ref
				}
			}
			if wt == "" || wt == mainRepo {
				continue
			}
			note := map[bool]string{true: " (branch " + br + ")"}[br != ""]
			if tid, ok := doneWT[wt]; ok {
				wtProbe.Items, wtBr[br] = append(wtProbe.Items, flow.SweepItem{Kind: "worktree", Key: wt, Summary: fmt.Sprintf("a leftover worktree %s of done task %s%s", wt, tid, note)}), true
			} else if rel, rerr := filepath.Rel(wtRoot, wt); !ownedWT[wt] && wtRoot != "" && rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				wtProbe.Items, wtBr[br] = append(wtProbe.Items, flow.SweepItem{Kind: "worktree", Key: wt, Summary: fmt.Sprintf("a leftover worktree %s owned by no attempt%s", wt, note)}), true
			}
		}
	}
	if out, err := gitCmd(ctx, repo, "branch", "--list", "--format=%(refname:short)", "task/*"); err != nil {
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
