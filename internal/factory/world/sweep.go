package world

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// Sweep runs all sweep finders for a project (design.md 11.3): stray terminals,
// orphan or done-task worktrees, orphan task/* branches, and local base ahead.
func (g *Git) Sweep(ctx context.Context, client *hc.Client, repo, worktreeRoot string, s flow.State) []flow.SweepProbe {
	lbProbe := flow.SweepProbe{Probe: "local_base"}
	if ref, rsha, err := g.remoteBaseCommit(ctx, repo, false); err != nil {
		lbProbe.Unknown = err.Error()
	} else {
		_, base, _ := strings.Cut(strings.TrimPrefix(ref, "refs/remotes/"), "/")
		localRef := "refs/heads/" + base
		if sha, e1 := g.gitCmd(ctx, repo, "rev-parse", "--verify", "--quiet", localRef+"^{commit}"); gitExit(e1, 1) {
			lbProbe.Unknown = "missing local base branch " + localRef
		} else if e1 != nil {
			lbProbe.Unknown = e1.Error()
		} else if sha != rsha {
			out, e2 := g.gitCmd(ctx, repo, "rev-list", "--count", rsha+".."+sha)
			if n, e3 := strconv.Atoi(strings.TrimSpace(out)); cmp.Or(e2, e3) != nil {
				lbProbe.Unknown = cmp.Or(e2, e3).Error()
			} else if n > 0 {
				lbProbe.Items = []flow.SweepItem{{Kind: "local_base", Key: sha, Summary: fmt.Sprintf("the local %s is %d %s ahead of the last-fetched tracking ref %s (%s)", base, n, map[bool]string{true: "commit", false: "commits"}[n == 1], ref, rsha[:min(len(rsha), 7)])}}
			}
		}
	}
	if s.Project.ID == "" {
		return []flow.SweepProbe{{Probe: "terminals", Unknown: "no project state"}, {Probe: "worktrees", Unknown: "no project state"}, {Probe: "branches", Unknown: "no project state"}, lbProbe}
	}
	cfg := flow.Settings{EnsureFailures: cmp.Or(s.Settings.EnsureFailures, flow.DefaultSettings().EnsureFailures)}
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
		var prunable bool
		for field := range strings.SplitSeq(stanza, "\x00") {
			if path, ok := strings.CutPrefix(field, "worktree "); ok {
				item.path = path
			} else if branch, ok := strings.CutPrefix(field, "branch refs/heads/"); ok {
				item.branch = branch
			} else if field == "prunable" || strings.HasPrefix(field, "prunable ") {
				prunable = true
			}
		}
		if item.path == "" {
			continue
		}
		if prunable {
			if exists, perr := g.PathExists(ctx, repo, item.path); perr != nil {
				return nil, nil, perr
			} else if !exists {
				continue
			}
		}
		listed, paths = append(listed, item), append(paths, item.path)
	}
	if len(listed) <= 1 {
		return nil, nil, nil
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
	var refs []flow.RunRecord
	add := func(addr string, r *flow.RunRecord) {
		if r == nil && addr != "" {
			refs = append(refs, flow.RunRecord{Address: addr})
		} else if r != nil && r.State != flow.PhaseEnded {
			refs = append(refs, flow.RunRecord{Address: cmp.Or(r.Address, addr), ID: r.ID, CreatedMS: r.CreatedMS})
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
		return slices.ContainsFunc(refs, func(ref flow.RunRecord) bool {
			return matchesRunTerminal(t, ref.ID, ref.CreatedMS) &&
				(ref.ID != "" || ref.Address != "" && (t.Address == ref.Address || t.Name == ref.Address))
		})
	}
	p := flow.SweepProbe{Probe: "terminals"}
	for _, t := range list.Terminals {
		if t.Labels["sheepdog.project"] == s.Project.ID && !owned(t) {
			p.Items = append(p.Items, flow.SweepItem{Kind: "stray_terminal", Key: fmt.Sprintf("%s@%d", t.Address, t.CreatedMS), Summary: fmt.Sprintf("a stray agent terminal %s with factory labels", t.Address)})
		}
	}
	return p
}

const codexHomeScript = `LC_ALL=C
if err=$(stat "$1" 2>&1 >/dev/null); then [ ! -L "$1" ] && [ -d "$1" ] || { printf 'not a directory: %s\n' "$1" >&2; exit 2; }; else case "$err" in *": No such file or directory"|*": Not a directory") exit 3;; esac; printf '%s\n' "$err" >&2; exit 2; fi
if stat -f %z "$1" >/dev/null 2>&1; then f=-f; m=%z; else f=-c; m=%s; fi
cd "$1" 2>/dev/null || { err=$(cd "$1" 2>&1); printf '%s: %s\n' "${err##*: }" "$1" >&2; exit 2; }
out=$(find -P . -type f -exec stat "$f" "$m" {} +) || exit 2
printf '%s\n' "$out" | awk 'NF {n++; b+=$1} END {printf "%d %d\n", n+0, b+0}'`

// SweepCodexHome reports the shared Codex home via herdr-core without opening files or following symlinks (design.md 11.3).
func (g *Git) SweepCodexHome(ctx context.Context, dir string) flow.SweepProbe {
	p := flow.SweepProbe{Probe: "codex_home"}
	if dir == "" {
		return p
	}
	abs, err := absolutePath(dir)
	if rel, rerr := filepath.Rel(os.TempDir(), abs); err == nil && testing.Testing() && (rerr != nil || strings.HasPrefix(rel, "..")) {
		err = fmt.Errorf("world: %s is outside the temp dir under go test", dir)
	}
	if err != nil {
		p.Unknown = err.Error()
		return p
	}
	r, err := g.pathCommand(GitRepository(ctx, abs), os.TempDir(), codexHomeScript, abs)
	switch {
	case gitExit(err, 3):
	case GitTimedOut(err):
		p.Unknown = "codex-home probe timed out"
	case gitExit(err, 2) && strings.TrimSpace(string(r.Stderr)) != "":
		p.Unknown = strings.TrimSpace(string(r.Stderr))
	case err != nil:
		p.Unknown = err.Error()
	default:
		var files, total int64
		if n, serr := fmt.Sscanf(string(r.Stdout), "%d %d", &files, &total); serr != nil || n != 2 {
			p.Unknown = "unexpected codex-home inventory response"
		} else {
			p.Items = []flow.SweepItem{{Kind: "codex_home", Key: dir, Summary: fmt.Sprintf("shared codex-home at %s (%d %s, %d B)", dir, files, map[bool]string{true: "file", false: "files"}[files == 1], total)}}
		}
	}
	return p
}
