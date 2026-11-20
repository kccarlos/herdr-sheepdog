package factory

import (
	"context"
	"errors"
	"fmt"
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
	a.Accepts = append(a.Accepts, flow.Accept{Head: h, By: "bar", Rounds: rounds})
	return fmt.Sprintf("accepted %s by the bar (rounds %v)", h, rounds), false, nil
}

// execMerge is N42's merge of the snapshot's PR, squash, pinned to H. The
// merge@H act makes it once per head; N41 waits while the PR reads open.
// Its result is not trusted: only a snapshot that reads merged ends the
// task (N2).
func (e *Engine) execMerge(ctx context.Context, _ string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, a := s.Tasks[ti], s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	gctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	err := world.MergePR(gctx, s.Project.Folder, f.PR.Number, flow.H(a), prBody(t))
	return fmt.Sprintf("asked GitHub to merge PR #%d at %s", f.PR.Number, flow.H(a)), false, err
}

// execMarkDone is N2: done{merged commit} from the snapshot that reads merged.
func (e *Engine) execMarkDone(_ context.Context, _ string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	if f.PR.State != flow.PRMerged || f.PR.MergedCommit == "" {
		return "", false, errors.New("factory: done needs a snapshot that reads merged, with its merge commit")
	}
	s.Tasks[ti].Done = &flow.Merged{MergedCommit: f.PR.MergedCommit}
	return fmt.Sprintf("done: PR #%d merged as %s", f.PR.Number, f.PR.MergedCommit), false, nil
}

// execCleanup is N1's Cleanup (design.md 4.7), each step safe to repeat:
// stop every run of the task; delete the remote branch only for a done
// task (done is only ever set from a snapshot that reads merged); remove
// the worktree only if it is clean. A dirty one is kept and logged
// (KB-MRG-78).
func (e *Engine) execCleanup(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t := &s.Tasks[ti]
	if err := e.stopRuns(ctx, taskRuns(id, *t)); err != nil {
		return "", false, err
	}
	clearRuns(t)
	steps := []string{"stopped the task's runs"}
	if len(t.Attempts) == 0 {
		return steps[0], false, nil
	}
	a := t.Attempts[len(t.Attempts)-1]
	gctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	if t.Done != nil {
		if err := world.DeleteRemoteBranch(gctx, s.Project.Folder, a.Branch); err != nil {
			return "", false, err
		}
		steps = append(steps, "deleted remote branch "+a.Branch)
	}
	if note := closePRNote(*t); note != "" {
		closed, err := closeOpenPR(gctx, s.Project.Folder, a, note)
		if err != nil {
			return "", false, err
		} else if closed {
			steps = append(steps, fmt.Sprintf("closed PR #%d", a.PR))
		}
	}
	removed, err := world.RemoveWorktree(gctx, s.Project.Folder, a.Worktree)
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
func closeOpenPR(ctx context.Context, folder string, a flow.Attempt, note string) (bool, error) {
	if a.PR <= 0 {
		return false, nil
	}
	p, err := world.PRSnapshot(ctx, folder, a.Branch)
	if err != nil || p.State != flow.PROpen {
		return false, err
	}
	return true, world.ClosePR(ctx, folder, a.PR, note)
}
