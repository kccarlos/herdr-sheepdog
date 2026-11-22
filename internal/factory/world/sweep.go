package world

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// Sweep runs all sweep finders for a project (design.md 11.3): stray terminals,
// orphan or done-task worktrees, orphan task/* branches, and local base ahead.
func (g *Git) Sweep(ctx context.Context, client *hc.Client, repo, worktreeRoot string, s flow.State) []flow.SweepProbe {
	lbProbe := g.sweepLocalBase(ctx, repo)
	if s.Project.ID == "" {
		return []flow.SweepProbe{{Probe: "terminals", Unknown: "no project state"}, {Probe: "worktrees", Unknown: "no project state"}, {Probe: "branches", Unknown: "no project state"}, lbProbe}
	}
	cfg := s.Settings
	cfg.EnsureFailures = cmp.Or(cfg.EnsureFailures, flow.DefaultSettings().EnsureFailures)
	cleanupDone := func(acts []flow.Act) bool {
		return flow.Done(acts, flow.AskCleanup) || flow.Exhausted(acts, flow.AskCleanup, cfg)
	}
	ownedBr, doneWT := map[string]bool{}, map[string]string{}
	for _, t := range s.Tasks {
		cleaned := cleanupDone(t.Acts)
		for _, a := range t.Attempts {
			ownedBr[a.Branch], cleaned = true, cleaned || cleanupDone(a.Acts)
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
	if len(listed) <= 1 {
		return nil, nil, nil
	}
	resolved, err := g.CanonicalPaths(ctx, repo, paths...)
	return resolved, listed, err
}

func (g *Git) sweepLocalBase(ctx context.Context, repo string) flow.SweepProbe {
	p := flow.SweepProbe{Probe: "local_base"}
	ref, err := g.gitCmd(ctx, repo, "symbolic-ref", "--quiet", "refs/remotes/"+pushRemote+"/HEAD")
	if err != nil && !gitExit(err, 1) {
		p.Unknown = err.Error()
		return p
	}
	var rsha, sha, base, localRef string
	readRefs := func(r string) error {
		_, base, _ = strings.Cut(strings.TrimPrefix(r, "refs/remotes/"), "/")
		localRef = "refs/heads/" + base
		out, err := g.gitCmd(ctx, repo, "for-each-ref", "--format=%(refname)%09%(objectname)", r, localRef)
		if err != nil {
			return err
		}
		for ln := range strings.Lines(out) {
			if name, oid, ok := strings.Cut(strings.TrimSpace(ln), "\t"); ok {
				switch name {
				case r:
					rsha = oid
				case localRef:
					sha = oid
				}
			}
		}
		return nil
	}
	if strings.HasPrefix(ref, "refs/remotes/"+pushRemote+"/") {
		if err := readRefs(ref); err != nil {
			p.Unknown = err.Error()
			return p
		}
	}
	if rsha == "" {
		if ref, rsha, err = g.remoteBaseCommit(ctx, repo, false); err != nil {
			p.Unknown = err.Error()
			return p
		}
		if err := readRefs(ref); err != nil {
			p.Unknown = err.Error()
			return p
		}
	}
	if sha == "" {
		p.Unknown = fmt.Sprintf("missing local base branch %s", localRef)
		return p
	}
	if sha == rsha {
		return p
	}
	out, err := g.gitCmd(ctx, repo, "rev-list", "--count", ref+".."+localRef)
	if n, nerr := strconv.Atoi(strings.TrimSpace(out)); cmp.Or(err, nerr) != nil {
		p.Unknown = cmp.Or(err, nerr).Error()
	} else if n > 0 {
		p.Items = []flow.SweepItem{{Kind: "local_base", Key: sha, Summary: fmt.Sprintf("the local %s is %d %s ahead of the last-fetched tracking ref %s (%s)", base, n, map[bool]string{true: "commit", false: "commits"}[n == 1], ref, rsha[:min(len(rsha), 7)])}}
	}
	return p
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
	type runRef struct {
		addr, id  string
		createdMS uint64
	}
	var refs []runRef
	add := func(addr string, r *flow.RunRecord) {
		if r == nil && addr != "" {
			refs = append(refs, runRef{addr: addr})
		} else if r != nil && r.State != flow.PhaseEnded {
			refs = append(refs, runRef{addr: cmp.Or(r.Address, addr), id: r.ID, createdMS: r.CreatedMS})
		}
	}
	add(s.Project.RunID, s.Project.Run)
	for _, t := range s.Tasks {
		for _, a := range t.Attempts {
			add(a.RunID, a.Run)
			if a.Review != nil {
				add(a.Review.RunID, a.Review.Run)
			}
		}
	}
	owned := func(t hc.Terminal) bool {
		for _, ref := range refs {
			switch {
			case ref.id != "" && ref.createdMS != 0:
				if t.Labels["sheepdog.run"] == ref.id && t.CreatedMS == ref.createdMS {
					return true
				}
			case ref.id != "":
				if t.Labels["sheepdog.run"] == ref.id {
					return true
				}
			case ref.createdMS != 0:
				if ref.addr != "" && (t.Address == ref.addr || t.Name == ref.addr) && t.CreatedMS == ref.createdMS {
					return true
				}
			default:
				if ref.addr != "" && (t.Address == ref.addr || t.Name == ref.addr) {
					return true
				}
			}
		}
		return false
	}
	p := flow.SweepProbe{Probe: "terminals"}
	for _, t := range list.Terminals {
		if t.Labels["sheepdog.project"] == s.Project.ID && !owned(t) {
			p.Items = append(p.Items, flow.SweepItem{Kind: "stray_terminal", Key: fmt.Sprintf("%s@%d", t.Address, t.CreatedMS), Summary: fmt.Sprintf("a stray agent terminal %s with factory labels", t.Address)})
		}
	}
	return p
}

// SweepCodexHome reports the location, file count and gathered byte size of the
// shared Codex home without opening files or following symlinks (design.md 11.3).
func SweepCodexHome(dir string) flow.SweepProbe {
	p := flow.SweepProbe{Probe: "codex_home"}
	if dir == "" {
		return p
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return p
	} else if err != nil {
		p.Unknown = err.Error()
		return p
	}
	var files int
	var total int64
	if err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		files++
		total += info.Size()
		return nil
	}); err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			p.Unknown = fmt.Sprintf("%v: %s", pe.Err, pe.Path)
		} else {
			p.Unknown = err.Error()
		}
		return p
	}
	if files == 0 {
		return p
	}
	size := fmt.Sprintf("%d B", total)
	if total >= 1024*1024 {
		size = fmt.Sprintf("%.1f MiB", float64(total)/(1024*1024))
	} else if total >= 1024 {
		size = fmt.Sprintf("%.1f KiB", float64(total)/1024)
	}
	noun := map[bool]string{true: "file", false: "files"}[files == 1]
	p.Items = []flow.SweepItem{{Kind: "codex_home", Key: dir, Summary: fmt.Sprintf("shared codex-home at %s (%d %s, %s)", dir, files, noun, size)}}
	return p
}
