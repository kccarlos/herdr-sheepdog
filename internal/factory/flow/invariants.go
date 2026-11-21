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
type InvariantViolation struct{ ID, Subject, Why string }

// InvariantSnapshot is the post-pass stored state CheckInvariants judges.
type InvariantSnapshot struct {
	State   State
	Skipped map[string]bool
	Now     time.Time
}

// CheckInvariants checks design.md section 8's invariants I1–I5 purely over
// the post-pass stored state.
func CheckInvariants(snap InvariantSnapshot) []InvariantViolation {
	s, p, out, writers := snap.State, snap.State.Project, []InvariantViolation(nil), map[string][]string{}
	add := func(id, subj, format string, args ...any) {
		out = append(out, InvariantViolation{id, subj, fmt.Sprintf(format, args...)})
	}
	runLive := func(id string, r *RunRecord) bool {
		return r != nil && r.State != PhaseEnded || r == nil && id != ""
	}
	pr, _ := FirstMatch(ProjectRows, ProjectIn{Project: p, Tasks: s.Tasks, Settings: s.Settings, Now: snap.Now})
	coordAlive := runLive(p.RunID, p.Run)
	// I2 cannot fire under the current rule table (J6/J7/J10 cover it); it guards future rule changes.
	if p.Started && (!AllTasksEnded(s.Tasks) || p.NewGoal) && !coordAlive && pr.ID != "J6" && pr.ID != "J7" {
		add("I2", "project", "project should have a coordinator (matched %s, no live run or J6/J7 reason)", pr.ID)
	}
	if coordAlive && p.Folder != "" {
		writers[filepath.Clean(p.Folder)] = append(writers[filepath.Clean(p.Folder)], "coordinator")
	}
	for _, t := range s.Tasks {
		openCount := 0
		for i, a := range t.Attempts {
			openCount += map[bool]int{true: 1}[a.Closed == ""]
			for _, act := range a.Acts {
				if head, ok := strings.CutPrefix(act.Key, AskMerge+"@"); ok && (head == "" || !slices.ContainsFunc(a.Accepts, func(ac Accept) bool { return ac.Head == head })) {
					add("I4", t.ID, "task %s attempt %d asked merge for %q without an accept record", t.ID, i+1, head)
				}
			}
			wLive := runLive(a.RunID, a.Run)
			rLive := a.Review != nil && runLive(a.Review.RunID, a.Review.Run)
			if wLive && a.Worktree != "" {
				writers[filepath.Clean(a.Worktree)] = append(writers[filepath.Clean(a.Worktree)], fmt.Sprintf("%s/a%d/worker", t.ID, i+1))
			}
			closed, pfx := a.Closed != "" || i < len(t.Attempts)-1, ""
			switch {
			case closed:
				pfx = fmt.Sprintf("task %s closed attempt %d", t.ID, i+1)
			case Ended(t):
				if Done(a.Acts, AskCleanup) || Exhausted(a.Acts, AskCleanup, s.Settings) {
					pfx = "ended task " + t.ID + " still"
				}
			case t.Parked:
				if Exhausted(a.Acts, AskStopRuns, s.Settings) {
					pfx = "parked task " + t.ID + " (stop_runs exhausted)"
				}
			case rLive && !snap.Skipped[t.ID]:
				rev, in := a.Review, In{Task: t, Settings: s.Settings, Now: snap.Now}
				switch {
				case OpenRecap(t) != nil:
					add("I1", t.ID, "task %s has unwanted live reviewer run: recap is open (N6)", t.ID)
				case len(a.Ready) == 0:
					add("I1", t.ID, "task %s has unwanted live reviewer run: no ready report in attempt", t.ID)
				case rev.Round != len(a.Rounds)+1 || !Covers(a, rev.Head, H(a)):
					add("I1", t.ID, "task %s has unwanted live reviewer run: round %d/%s no longer matches current round %d/%s", t.ID, rev.Round, rev.Head, len(a.Rounds)+1, H(a))
				case Ladder(in) != Review:
					add("I1", t.ID, "task %s has unwanted live reviewer run: ladder says %s", t.ID, Ladder(in))
				}
			}
			for j, on := range [2]bool{wLive, rLive} {
				if pfx != "" && on {
					add("I1", t.ID, "%s has live %s run", pfx, [2]string{RoleWorker, RoleReviewer}[j])
				}
			}
		}
		if openCount > 1 {
			add("I5", t.ID, "task %s has %d open attempts", t.ID, openCount)
		}
	}
	for _, wt := range slices.Sorted(maps.Keys(writers)) {
		if len(writers[wt]) > 1 {
			add("I3", wt, "worktree %s shared by writable live runs: %s", wt, strings.Join(writers[wt], ", "))
		}
	}
	return out
}
