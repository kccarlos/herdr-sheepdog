package factory

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
)

// holdWake queues a wake_coordinator@<key> ask as a held wake, once per
// task and key, with the task's facts as they are now (design.md 10, J12).
func holdWake(s *flow.State, ti int, row, why, ask string, f flow.Facts, now time.Time) string {
	t := s.Tasks[ti]
	_, k, _ := strings.Cut(ask, "@")
	if slices.ContainsFunc(s.Project.Wakes, func(w flow.Wake) bool { return w.Task == t.ID && w.Key == k }) {
		return "wake already held"
	}
	s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: k, Task: t.ID, Why: row + ": " + why, Facts: taskFacts(t, f), At: now})
	return "wake held for the coordinator"
}

// The facts block's bounds: a title's characters, the wakes listed.
const maxFactsTitle, maxFactsWakes = 120, 20

// taskFacts is a waking task's line of the facts block: title, H, the PR
// number and checks, the last verdict.
func taskFacts(t flow.Task, f flow.Facts) string {
	h, pr, verdict := "none", 0, "none"
	if n := len(t.Attempts); n > 0 {
		a := t.Attempts[n-1]
		h, pr = flow.H(a), a.PR
		if r := len(a.Rounds); r > 0 {
			verdict = fmt.Sprintf("%s (round %d, %d major(s))", a.Rounds[r-1].Verdict, a.Rounds[r-1].N, a.Rounds[r-1].Majors)
		}
	}
	title := []rune(t.Title)
	if len(title) > maxFactsTitle {
		title = append(title[:maxFactsTitle], '…')
	}
	out := fmt.Sprintf("title %q; H %s; PR #%d, checks %s; last verdict %s", string(title), h, cmp.Or(f.PR.Number, pr), cmp.Or(string(f.PR.Checks), "unknown"), verdict)
	if len(f.DepsDropped) > 0 {
		out += "; dropped dependencies: " + strings.Join(f.DepsDropped, ", ")
	}
	if f.PR.ReviewRequired {
		out += "; GitHub requires an approving review (repo branch protection / ruleset required approving reviews): nobody in the factory can approve"
	}
	if rc := flow.OpenRecap(t); rc != nil {
		out += fmt.Sprintf("; recap trigger %s, allowed [%s]", rc.Trigger, strings.Join(rc.Allowed, ", "))
		if a := flow.OpenAttempt(t); a != nil && rc.Trigger == flow.TriggerSize {
			if f.LinesTimeout {
				out += fmt.Sprintf("; changed lines timed out, size cap %d, estimate %d", a.SizeCap, t.EstimateLines)
			} else {
				out += fmt.Sprintf("; changed lines %d, size cap %d, estimate %d", f.Lines, a.SizeCap, t.EstimateLines)
			}
		}
	}
	return out
}

// wakeMessage is the one message a batch of wakes is delivered as. It is
// all facts built in code: each waking task, then every setting that
// differs from its default.
func wakeMessage(s flow.State) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Wake: %d task wake(s) wait on you.\n", len(s.Project.Wakes))
	for i, w := range s.Project.Wakes {
		if i == maxFactsWakes {
			fmt.Fprintf(&b, "- and %d more.\n", len(s.Project.Wakes)-i)
			break
		}
		fmt.Fprintf(&b, "- %s: %s. %s. (wake %s at %s)\n", w.Task, w.Why, w.Facts, w.Key, w.At.Format(time.RFC3339))
	}
	changed := cmp.Or(strings.Join(s.Settings.Changed(), ", "), "none")
	fmt.Fprintf(&b, "Settings that differ from their default: %s.", changed)
	return b.String()
}

// deliverWakesLocked is J12: when the coordinator run is idle, every held
// wake goes to it as one message, then is logged (one line listing the
// keys) and cleared. While it works, or with no run, they stay held; a
// coordinator that died is J7's to restart, and the wakes wait for it.
// Delivery is at-least-once: a send that went through but whose commit
// failed leaves the wakes held, and the next pass sends them again. A
// duplicate wake is cheaper than a lost one.
func (e *Engine) deliverWakesLocked(ctx context.Context, id string, s *flow.State) {
	if len(s.Project.Wakes) == 0 || s.Project.RunID == "" || e.HC == nil {
		return
	}
	st, err := world.RunState(ctx, e.HC, s.Project.RunID)
	if err != nil {
		e.logError("factory.hc_error", id, err)
		return
	}
	if st != flow.RunIdle {
		return
	}
	if err := world.SendRun(ctx, e.HC, s.Project.RunID, wakeMessage(*s)); err != nil {
		e.logError("factory.exec_error", id, err)
		return
	}
	var keys []string
	for _, w := range s.Project.Wakes {
		keys = append(keys, w.Task+":"+w.Key)
	}
	s.Project.Wakes = nil
	line := LogLine{By: "engine", Rule: "J12", Asked: keys, Decision: fmt.Sprintf("delivered %d wake(s) to the coordinator", len(keys)), Why: "wakes held and the coordinator is idle"}
	if err := e.commitLocked(id, s, line); err != nil {
		e.logError("factory.commit_error", id, err)
	}
}
