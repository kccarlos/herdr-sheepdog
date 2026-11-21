package flow

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// InvariantViolation is one broken invariant (I1–I5) found after a pass (design.md 8).
type InvariantViolation struct{ ID, Why string }

// InvariantSnapshot is the post-pass stored state and observed facts CheckInvariants judges.
type InvariantSnapshot struct {
	State             State
	TaskFacts         map[string]Facts
	CoordPendingStart bool
	Now               time.Time
}

// CheckInvariants checks design.md section 8's invariants I1–I5 purely over
// the post-pass stored state and observed facts.
func CheckInvariants(snap InvariantSnapshot) []InvariantViolation {
	s, p, out, writers := snap.State, snap.State.Project, []InvariantViolation(nil), map[string][]string{}
	add := func(id, format string, args ...any) {
		out = append(out, InvariantViolation{id, fmt.Sprintf(format, args...)})
	}
	checkRun := func(id string, r *RunRecord, label string) bool {
		if id != "" && r != nil && r.State == PhaseEnded {
			add("I1", "%s run_id %s still set after run ended", label, id)
			return false
		}
		return id != "" || r != nil && r.State != PhaseEnded
	}
	pr, pd := FirstMatch(ProjectRows, ProjectIn{Project: p, Tasks: s.Tasks, Settings: s.Settings, Now: snap.Now})
	coordAlive := checkRun(p.RunID, p.Run, "coordinator")
	if coordAlive && !slices.Contains([]string{"J4", "J8", "J9", "J12", "J13", "J14"}, pr.ID) && !slices.Contains(pd.Asked, AskStopCoordinator) {
		add("I1", "live coordinator run %s not wanted by %s", p.RunID, pr.ID)
	}
	if p.Started && (!AllTasksEnded(s.Tasks) || p.NewGoal) && !coordAlive && !snap.CoordPendingStart && pr.ID != "J6" {
		add("I2", "project should have a coordinator (matched %s, no live run, pending start, or J5/J6 reason)", pr.ID)
	}
	if coordAlive && p.Folder != "" {
		writers[filepath.Clean(p.Folder)] = append(writers[filepath.Clean(p.Folder)], "coordinator")
	}
	for _, t := range s.Tasks {
		f, hasFacts := snap.TaskFacts[t.ID]
		openCount := 0
		for i, a := range t.Attempts {
			openCount += map[bool]int{true: 1}[a.Closed == ""]
			for _, act := range a.Acts {
				if head, ok := strings.CutPrefix(act.Key, AskMerge+"@"); ok && (head == "" || !slices.ContainsFunc(a.Accepts, func(ac Accept) bool { return ac.Head == head })) {
					add("I4", "task %s attempt %d asked merge for %q without an accept record", t.ID, i+1, head)
				}
			}
			wLive := checkRun(a.RunID, a.Run, "task "+t.ID+" worker")
			rLive := a.Review != nil && checkRun(a.Review.RunID, a.Review.Run, "task "+t.ID+" reviewer")
			if wLive && a.Worktree != "" {
				writers[filepath.Clean(a.Worktree)] = append(writers[filepath.Clean(a.Worktree)], fmt.Sprintf("%s/a%d/worker", t.ID, i+1))
			}
			closed, pfx := a.Closed != "" || i < len(t.Attempts)-1, ""
			if !closed && hasFacts && a.PR > 0 && f.PR.Number > 0 && f.PR.Number != a.PR {
				add("I5", "task %s attempt %d has PR #%d but GitHub reported PR #%d", t.ID, i+1, a.PR, f.PR.Number)
			}
			switch {
			case closed:
				pfx = fmt.Sprintf("task %s closed attempt %d", t.ID, i+1)
			case !p.Started:
				pfx = "stopped project task " + t.ID
			case Ended(t) && (Done(a.Acts, AskCleanup) || Exhausted(a.Acts, AskCleanup, s.Settings)):
				pfx = "ended task " + t.ID + " still"
			case t.Parked && Exhausted(a.Acts, AskStopRuns, s.Settings):
				pfx = "parked task " + t.ID + " (stop_runs exhausted)"
			case rLive:
				rev, in := a.Review, In{Task: t, Settings: s.Settings, Facts: f, Now: snap.Now}
				switch {
				case OpenRecap(t) != nil:
					add("I1", "task %s has unwanted live reviewer run: recap is open (N6)", t.ID)
				case len(a.Ready) == 0:
					add("I1", "task %s has unwanted live reviewer run: no ready report in attempt", t.ID)
				case rev.Round != len(a.Rounds)+1 || !Covers(a, rev.Head, H(a)):
					add("I1", "task %s has unwanted live reviewer run: round %d/%s no longer matches current round %d/%s", t.ID, rev.Round, rev.Head, len(a.Rounds)+1, H(a))
				case Ladder(in) != Review:
					add("I1", "task %s has unwanted live reviewer run: ladder says %s", t.ID, Ladder(in))
				case hasFacts && (f.PR.State == PRMerged || f.PR.State == PRClosed):
					add("I1", "task %s has unwanted live reviewer run: PR is %s", t.ID, f.PR.State)
				case hasFacts && (f.TipRel == RelDiverged || f.TipRel == RelAhead && !f.TipMergeOnly || f.TipRel == RelBehind && !Covers(a, f.Tip, H(a))):
					add("I1", "task %s has unwanted live reviewer run: worktree tip (%s) moved off reviewed head", t.ID, f.TipRel)
				case hasFacts && PRHeadDivergedOrForeign(in):
					add("I1", "task %s has unwanted live reviewer run: PR head diverged or has foreign commits (N7)", t.ID)
				}
			}
			for j, on := range [2]bool{wLive, rLive} {
				if pfx != "" && on {
					add("I1", "%s has live %s run", pfx, [2]string{RoleWorker, RoleReviewer}[j])
				}
			}
		}
		if openCount > 1 {
			add("I5", "task %s has %d open attempts", t.ID, openCount)
		}
	}
	for _, wt := range slices.Sorted(maps.Keys(writers)) {
		if len(writers[wt]) > 1 {
			add("I3", "worktree %s shared by writable live runs: %s", wt, strings.Join(writers[wt], ", "))
		}
	}
	return out
}
