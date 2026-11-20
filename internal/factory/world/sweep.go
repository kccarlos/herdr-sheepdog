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
	openWT, openBr, doneWT := map[string]bool{}, map[string]bool{}, map[string]string{}
	for _, t := range s.Tasks {
		if a := flow.OpenAttempt(t); !flow.Ended(t) && a != nil {
			openWT[realPath(a.Worktree)], openBr[a.Branch] = true, true
		} else if t.Done != nil {
			for _, a := range t.Attempts {
				doneWT[realPath(a.Worktree)] = t.ID
			}
		}
	}
	wtProbe, brProbe := flow.SweepProbe{Probe: "worktrees"}, flow.SweepProbe{Probe: "branches"}
	if out, err := gitCmd(ctx, repo, "worktree", "list", "--porcelain"); err != nil {
		wtProbe.Unknown = err.Error()
	} else {
		mainRepo, wtRoot := realPath(repo), realPath(worktreeRoot)
		for ln := range strings.Lines(out) {
			raw, ok := strings.CutPrefix(strings.TrimSpace(ln), "worktree ")
			wt := realPath(raw)
			if !ok || wt == "" || wt == mainRepo {
				continue
			}
			if tid, ok := doneWT[wt]; ok {
				wtProbe.Items = append(wtProbe.Items, flow.SweepItem{Kind: "worktree", Key: wt, Summary: fmt.Sprintf("a leftover worktree %s of done task %s", wt, tid)})
			} else if rel, rerr := filepath.Rel(wtRoot, wt); !openWT[wt] && wtRoot != "" && rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				wtProbe.Items = append(wtProbe.Items, flow.SweepItem{Kind: "worktree", Key: wt, Summary: fmt.Sprintf("a leftover worktree %s owned by no open attempt", wt)})
			}
		}
	}
	if out, err := gitCmd(ctx, repo, "branch", "--list", "--format=%(refname:short)", "task/*"); err != nil {
		brProbe.Unknown = err.Error()
	} else {
		for ln := range strings.Lines(out) {
			if br := strings.TrimSpace(ln); br != "" && !openBr[br] {
				brProbe.Items = append(brProbe.Items, flow.SweepItem{Kind: "branch", Key: br, Summary: fmt.Sprintf("a leftover branch %s owned by no open attempt", br)})
			}
		}
	}
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
	return []flow.SweepProbe{SweepTerminals(ctx, client, s), wtProbe, brProbe, lbProbe}
}

// SweepTerminals finds live factory-labelled terminals that no active run owns.
func SweepTerminals(ctx context.Context, client *hc.Client, s flow.State) flow.SweepProbe {
	if client == nil {
		return flow.SweepProbe{Probe: "terminals", Unknown: "no hc client"}
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
		if proj := t.Labels["sheepdog.project"]; proj != "" && (s.Project.ID == "" || proj == s.Project.ID) && !owned[t.Address] && !owned[t.Labels["sheepdog.run"]] {
			p.Items = append(p.Items, flow.SweepItem{Kind: "stray_terminal", Key: t.Address, Summary: fmt.Sprintf("a stray agent terminal %s with factory labels", t.Address)})
		}
	}
	return p
}
