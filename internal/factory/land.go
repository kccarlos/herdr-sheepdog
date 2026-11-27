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
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// execRecordAccept is N42's accept at H; N19 records Accept{By: "override"}
// inside execMerge only after MergePR succeeds so call-offs and restarts
// before MergePR leave no accept record (design.md 3, 4.1 N2, 4.3b).
func (e *Engine) execRecordAccept(_ context.Context, _ string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	a := &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	return recordAccept(s.Tasks[ti], a, s.Settings, f, flow.H(*a), false), false, nil
}

func recordAccept(t flow.Task, a *flow.Attempt, settings flow.Settings, f flow.Facts, h string, byOverride bool) string {
	rounds := []int{}
	for _, r := range flow.PassingCovering(*a, h) {
		rounds = append(rounds, r.N)
	}
	retroShip := false
	if r := flow.NewestCovering(*a, h); r != nil && r.Kind == flow.RoundRetro && r.Verdict == flow.VerdictShip {
		rounds, retroShip = append(rounds, r.N), true
	}
	ac := flow.Accept{Head: h, By: "bar", Rounds: rounds}
	msg := fmt.Sprintf("accepted %s by the bar (rounds %v)", h, rounds)
	if byOverride {
		ov := flow.CoveringOverride(*a, h)
		if ov == nil {
			return ""
		}
		ac.By, ac.Waived, ac.ProofID = "override", overrideWaived(t, *a, settings, f, !retroShip && len(rounds) < settings.ReviewRounds), ov.ProofID
		msg = fmt.Sprintf("accepted %s by override (waived %v, proof %s)", h, ac.Waived, ov.ProofID)
	}
	if !slices.ContainsFunc(a.Accepts, func(x flow.Accept) bool { return x.Head == h }) {
		a.Accepts = append(a.Accepts, ac)
	}
	return msg
}

func overrideWaived(t flow.Task, a flow.Attempt, s flow.Settings, f flow.Facts, waiveReview bool) []string {
	var w []string
	for _, c := range []struct {
		on   bool
		name string
	}{
		{waiveReview, "review"}, {f.PR.Checks != flow.ChecksGreen, "checks"},
		{f.PR.Behind || f.PR.BehindUnknown, "behind"}, {f.BaseRed, "base_red"},
		{f.PR.ReviewRequired, "review_required"}, {f.Worker.State == flow.RunWorking, "worker_working"},
		{f.TipRel == flow.RelAhead, "unreported_tip"}, {f.LinesTimeout || f.Lines > a.SizeCap, "size_cap"},
		{t.Gate == flow.GateOwner || t.Kind == flow.TaskKindDesign && s.DesignGate == flow.GateOwner, "owner_gate"},
	} {
		if c.on {
			w = append(w, c.name)
		}
	}
	if len(w) == 0 {
		w = append(w, "bar")
	}
	return w
}

func otherMergeInFlight(tasks []flow.Task, self int) bool {
	for i, t := range tasks {
		if i == self || flow.Ended(t) || t.Parked || (t.LastRow != "N19" && t.LastRow != "N41" && t.LastRow != "N42") {
			continue
		}
		if a := flow.OpenAttempt(t); a != nil {
			if last := flow.LastAct(flow.SinceRetry(a.Acts), flow.AskMerge+"@"+flow.H(*a)); last != nil && (last.Result == "done" || last.Result == "asked") {
				return true
			}
		}
	}
	return false
}

func spendCoveringOverrides(a *flow.Attempt, head string) {
	if a == nil {
		return
	}
	for i := range a.Overrides {
		if !a.Overrides[i].Spent && flow.Covers(*a, a.Overrides[i].Head, head) {
			a.Overrides[i].Spent = true
		}
	}
}

// execMerge is N42/N19's merge of the snapshot's PR, squash, pinned to H. Before
// calling GitHub it reads fresh facts and re-tests N42/N19 (design.md 8, 4.3b).
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
	lines, generated, linesTO, tipMO, err := e.readTipLocked(gitCtx, *a, tip, rel)
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
	notDone, dropped := flow.UnmetDeps(s.Tasks[ti], s.Tasks)
	fresh := flow.Facts{
		PR: p, Tip: tip, TipRel: rel, TipMergeOnly: tipMO, Lines: lines, LinesGenerated: generated, LinesTimeout: linesTO,
		Worker: worker, DepsNotDone: notDone, DepsDropped: dropped, OtherMergeInFlight: otherMergeInFlight(s.Tasks, ti),
		GitHubUnavailable: e.ghUnavail[id] || s.Project.HasAlert(flow.AlertGitHubUnavailable), BaseRed: f.BaseRed, BaseRedSince: f.BaseRedSince, BaseRedSpell: f.BaseRedSpell,
	}
	freshRow, _ := flow.FirstMatch(flow.TaskRows, flow.In{Task: s.Tasks[ti], Settings: s.Settings, Facts: fresh, Asks: s.Asks, Now: e.now()})
	if freshRow.ID != "N42" && freshRow.ID != "N19" {
		return fmt.Sprintf("merge called off on fresh check (row %s)", freshRow.ID), true, nil
	}
	num = cmp.Or(num, p.Number)
	if err := e.git.MergePR(gctx, s.Project.Folder, num, h, prBody(t)); err != nil {
		return "", false, err
	}
	dec := fmt.Sprintf("asked GitHub to merge PR #%d at %s", num, h)
	if freshRow.ID == "N19" {
		if adec := recordAccept(t, a, s.Settings, fresh, h, true); adec != "" {
			dec = adec + "; " + dec
		}
	}
	return dec, false, nil
}

// execSpendOverride is N19b: mark covering unspent overrides spent so the next pass returns to normal rules.
func (e *Engine) execSpendOverride(_ context.Context, _ string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	a := flow.OpenAttempt(s.Tasks[ti])
	if a == nil {
		return "no open attempt", false, nil
	}
	h := flow.H(*a)
	spendCoveringOverrides(a, h)
	return fmt.Sprintf("spent override for %s (checks got worse)", h), false, nil
}

// execMarkDone is N2: done{merged commit} from the snapshot that reads merged.
func (e *Engine) execMarkDone(_ context.Context, _ string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	if f.PR.State != flow.PRMerged || f.PR.MergedCommit == "" {
		return "", false, errors.New("factory: done needs a snapshot that reads merged, with its merge commit")
	}
	s.Tasks[ti].Done = &flow.Merged{MergedCommit: f.PR.MergedCommit}
	if a := flow.OpenAttempt(s.Tasks[ti]); a != nil {
		spendCoveringOverrides(a, cmp.Or(f.PR.Head, flow.H(*a)))
	}
	return fmt.Sprintf("done: PR #%d merged as %s", f.PR.Number, f.PR.MergedCommit), false, nil
}

// execCleanup is N1's Cleanup (design.md 4.7, 8): checks N1 once under e.mu
// and reads fresh PR, hc terminal and worktree facts before acting.
func (e *Engine) execCleanup(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t := &s.Tasks[ti]
	if r, _ := flow.FirstMatch(flow.TaskRows, flow.In{Task: *t, Settings: s.Settings, Asks: s.Asks, Now: e.now()}); r.ID != "N1" {
		return "", false, world.Fail(flow.ClassSettled, errors.New("factory: cleanup requires N1"))
	}
	if err := e.stopRuns(ctx, taskRuns(id, *t)); err != nil {
		return "", false, err
	}
	now := e.now()
	stopTaskRuns(t, s.Settings, now)
	steps := []string{"stopped the task's runs"}
	if len(t.Attempts) == 0 {
		return strings.Join(steps, "; "), false, nil
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
			if cwd := hc.LocalCwd(tm.Address, tm.Cwd); tm.State == "live" && cwd != "" {
				live = append(live, cwd)
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
	if err != nil || p.State != flow.PROpen && p.OtherBase == "" {
		return false, err
	}
	return true, e.git.ClosePR(ctx, folder, a.PR, note)
}
