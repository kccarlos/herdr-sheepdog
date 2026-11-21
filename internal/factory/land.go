package factory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
)

// execRecordAccept is N42's accept by the bar at H. N42 asks it with the
// merge, so both land in one log line and one state write (design.md 3:
// "written at H in the same step as the merge").
func (e *Engine) execRecordAccept(_ context.Context, _ string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	a := &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	h, rounds := flow.H(*a), []int{}
	for _, r := range flow.PassingCovering(*a, h) {
		rounds = append(rounds, r.N)
	}
	if !slices.ContainsFunc(a.Accepts, func(ac flow.Accept) bool { return ac.Head == h }) {
		a.Accepts = append(a.Accepts, flow.Accept{Head: h, By: "bar", Rounds: rounds})
	}
	return fmt.Sprintf("accepted %s by the bar (rounds %v)", h, rounds), false, nil
}

// execMerge is N42's merge of the snapshot's PR, squash, pinned to H. Before
// calling GitHub it reads fresh facts and re-tests N42 (design.md 8).
func (e *Engine) execMerge(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, a := s.Tasks[ti], &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	h, num := flow.H(*a), cmp.Or(a.PR, f.PR.Number)
	gctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	gitCtx, cancelGit := context.WithTimeout(gctx, lockGitTimeout)
	tip, rel, _, err := e.git.InspectWorktree(gitCtx, a.Worktree, h)
	cancelGit()
	if err != nil {
		return "", false, err
	}
	gitCtx, cancelGit = context.WithTimeout(gctx, lockGitTimeout)
	lines, linesTO, tipMO, err := e.readTipLocked(gitCtx, *a, tip, rel)
	cancelGit()
	if err != nil {
		return "", false, err
	}
	worker := f.Worker
	if a.RunID != "" && e.HC != nil {
		if worker, err = e.advanceRunLocked(gctx, id, t.ID, s, a.RunID, flow.RoleWorker, "claude", &a.Run); err != nil {
			return "", false, err
		}
	}
	p := e.readPRLocked(gctx, id, s, ti)
	fresh := flow.Facts{
		PR: p, Tip: tip, TipRel: rel, TipMergeOnly: tipMO, Lines: lines, LinesTimeout: linesTO,
		Worker: worker, GitHubUnavailable: e.ghUnavail[id], BaseRed: f.BaseRed, BaseRedSince: f.BaseRedSince, BaseRedSpell: f.BaseRedSpell,
	}
	if r, _ := flow.FirstMatch(flow.TaskRows, flow.In{Task: s.Tasks[ti], Settings: s.Settings, Facts: fresh, Now: e.now()}); r.ID != "N42" {
		return fmt.Sprintf("merge called off on fresh check (row %s)", r.ID), true, nil
	}
	num = cmp.Or(num, p.Number)
	return fmt.Sprintf("asked GitHub to merge PR #%d at %s", num, h), false, e.git.MergePR(gctx, s.Project.Folder, num, h, prBody(t))
}

// execMarkDone is N2: done{merged commit} from the snapshot that reads merged.
func (e *Engine) execMarkDone(_ context.Context, _ string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	if f.PR.State != flow.PRMerged || f.PR.MergedCommit == "" {
		return "", false, errors.New("factory: done needs a snapshot that reads merged, with its merge commit")
	}
	s.Tasks[ti].Done = &flow.Merged{MergedCommit: f.PR.MergedCommit}
	return fmt.Sprintf("done: PR #%d merged as %s", f.PR.Number, f.PR.MergedCommit), false, nil
}

// execCleanup is N1's Cleanup (design.md 4.7, 8): checks N1 once under e.mu
// and reads fresh PR, hc terminal and worktree facts before acting.
func (e *Engine) execCleanup(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t := &s.Tasks[ti]
	if r, _ := flow.FirstMatch(flow.TaskRows, flow.In{Task: *t, Settings: s.Settings, Now: e.now()}); r.ID != "N1" {
		return "", false, world.Fail(flow.ClassSettled, errors.New("factory: cleanup requires N1"))
	}
	if err := e.stopRuns(ctx, taskRuns(id, *t)); err != nil {
		return "", false, err
	}
	stopTaskRuns(t, s.Settings, e.now())
	steps := []string{"stopped the task's runs"}
	if len(t.Attempts) == 0 {
		return steps[0], false, nil
	}
	a := t.Attempts[len(t.Attempts)-1]
	gctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	if t.Done != nil {
		if err := e.git.DeleteRemoteBranch(gctx, s.Project.Folder, a.Branch); err != nil {
			return "", false, err
		}
		steps = append(steps, "deleted remote branch "+a.Branch)
	}
	if note := closePRNote(*t); note != "" {
		closed, err := e.closeOpenPR(gctx, s.Project.Folder, a, note)
		if err != nil {
			return "", false, err
		} else if closed {
			steps = append(steps, fmt.Sprintf("closed PR #%d", a.PR))
		}
	}
	if e.HC != nil && a.Worktree != "" {
		list, err := e.HC.List(gctx, true)
		if err != nil {
			return "", false, world.Fail(flow.ClassTransient, err)
		}
		var live []string
		for _, tm := range list.Terminals {
			if tm.State == "live" && tm.Cwd != "" {
				live = append(live, tm.Cwd)
			}
		}
		keep := slices.ContainsFunc(live, func(dir string) bool { return underFolder(a.Worktree, dir) })
		if !keep && len(live) > 0 {
			dirs, err := e.git.CanonicalPaths(gctx, s.Project.Folder, append(live, a.Worktree)...)
			if err != nil {
				return "", false, err
			}
			keep = slices.ContainsFunc(live, func(dir string) bool { return underFolder(dirs[a.Worktree], dirs[dir]) })
		}
		if keep {
			e.logEvent("factory.worktree_kept", map[string]string{"project": id, "task": t.ID, "worktree": a.Worktree, "why": "live terminal inside"})
			return strings.Join(append(steps, "kept worktree "+a.Worktree+" (live terminal inside)"), "; "), false, nil
		}
	}
	removed, err := e.git.RemoveWorktree(gctx, s.Project.Folder, a.Worktree)
	switch {
	case err != nil:
		return "", false, err
	case removed:
		steps = append(steps, "removed worktree "+a.Worktree)
	default:
		e.logEvent("factory.worktree_kept", map[string]string{"project": id, "task": t.ID, "worktree": a.Worktree, "why": "uncommitted work"})
		steps = append(steps, "kept worktree "+a.Worktree+" (uncommitted work)")
	}
	return strings.Join(steps, "; "), false, nil
}

func closePRNote(t flow.Task) string {
	switch {
	case t.Split != nil:
		return fmt.Sprintf("Closed by sheepdog factory: task %s was split into %s.", t.ID, strings.Join(t.Split.Children, ", "))
	case t.Dropped != nil:
		return fmt.Sprintf("Closed by sheepdog factory: task %s was dropped (%s).", t.ID, t.Dropped.Why)
	default:
		return ""
	}
}

// closeOpenPR closes a's PR with note when GitHub still reads it open.
func (e *Engine) closeOpenPR(ctx context.Context, folder string, a flow.Attempt, note string) (bool, error) {
	if a.PR <= 0 {
		return false, nil
	}
	p, _, err := e.git.PRSnapshot(ctx, folder, a.Branch, a.PR)
	if err != nil || p.State != flow.PROpen {
		return false, err
	}
	return true, e.git.ClosePR(ctx, folder, a.PR, note)
}
