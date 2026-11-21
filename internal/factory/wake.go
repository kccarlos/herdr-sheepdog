package factory

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
)

// holdWake queues a wake_coordinator@<key> ask as a held wake, once per
// task and key, with the task's facts as they are now (design.md 10, J12).
func holdWake(s *flow.State, ti int, row, why, ask string, f flow.Facts, bin string, now time.Time) string {
	t := s.Tasks[ti]
	_, k, _ := strings.Cut(ask, "@")
	if slices.ContainsFunc(s.Project.Wakes, func(w flow.Wake) bool { return w.Task == t.ID && w.Key == k }) {
		return "wake already held"
	}
	bin = cmp.Or(bin, "sheepdog")
	why = strings.ReplaceAll(why, "sheepdog factory ", bin+" factory ")
	if k == "worker_failed" || strings.HasPrefix(k, "reviewer_failed@") {
		why += fmt.Sprintf("; run `%s factory task retry %s --why \"<why>\"`", bin, t.ID)
	}
	s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: k, Task: t.ID, Why: row + ": " + why, Facts: taskFacts(t, f, now), At: now})
	return "wake held for the coordinator"
}

func holdProjectWake(s *flow.State, k, why, facts string, now time.Time) {
	if !slices.ContainsFunc(s.Project.Wakes, func(w flow.Wake) bool { return w.Task == "" && w.Key == k }) {
		s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: k, Why: why, Facts: facts, At: now})
	}
}

// The facts block's bounds: a title's characters, the wakes listed.
const maxFactsTitle, maxFactsWakes = 120, 20

// taskFacts is a waking task's line of the facts block: title, H, the PR
// number and checks, the last verdict.
func taskFacts(t flow.Task, f flow.Facts, now time.Time) string {
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
		if rc.Trigger == flow.TriggerCI {
			fails := flow.RecentCIFails(t, now)
			heads := make([]string, len(fails))
			for i, cf := range fails {
				heads[i] = cf.Head[:min(len(cf.Head), 7)]
			}
			if len(heads) > 0 {
				out += fmt.Sprintf("; %d failing CI commit(s): %s", len(fails), strings.Join(heads, ", "))
			} else {
				out += fmt.Sprintf("; %d failing CI commit(s)", len(fails))
			}
		}
		if rc.Trigger == flow.TriggerTime {
			out += fmt.Sprintf("; working time %s", flow.WorkingTime(t).Truncate(time.Minute))
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
		fmt.Fprintf(&b, "- %s: %s. %s. (wake %s at %s)\n", cmp.Or(w.Task, "project"), w.Why, w.Facts, w.Key, w.At.Format(time.RFC3339))
	}
	changed := cmp.Or(strings.Join(s.Settings.Changed(), ", "), "none")
	fmt.Fprintf(&b, "Settings that differ from their default: %s.", changed)
	return b.String()
}

// coordSummary builds J13's heartbeat wake (heartbeat=true) or J7's restart handoff (KB-COO-4, design.md 6).
func coordSummary(s flow.State, bin string, heartbeat bool) string {
	open, ws, ls := 0, []string{}, []string{}
	for _, t := range s.Tasks {
		if a := flow.OpenAttempt(t); a != nil && a.RunID != "" && (a.Run == nil || a.Run.State != flow.PhaseEnded) {
			ws = append(ws, fmt.Sprintf("%s (%s %s)", a.RunID, t.ID, t.Title))
		}
		if !flow.Ended(t) {
			open++
		} else if heartbeat {
			continue
		}
		if len(ls) < maxFactsWakes {
			st, why := taskStageWhy(t, s.Tasks, s.Settings, s.Project.Started)
			ls = append(ls, fmt.Sprintf("- %s (%s): %s — %s\n", t.ID, st, cmp.Or(t.LastRow, "-"), cmp.Or(why, st)))
		}
	}
	changed := cmp.Or(strings.Join(s.Settings.Changed(), ", "), "none")
	if heartbeat {
		body := map[bool]string{true: fmt.Sprintf("No plan applied yet. Goal: %s\n", s.Project.Goal), false: fmt.Sprintf("Tasks: %d open, %d ended.\n%s", open, len(s.Tasks)-open, strings.Join(ls, ""))}[len(s.Tasks) == 0]
		return fmt.Sprintf("Wake: heartbeat (no wake for %dm).\n%sSettings that differ from their default: %s.", s.Settings.HeartbeatMinutes, body, changed)
	}
	return fmt.Sprintf("Handoff from the store (replacement coordinator; held wakes follow once idle):\nLive workers: %s.\n%sSettings that differ from their default: %s.\n\n%s",
		cmp.Or(strings.Join(ws, ", "), "none"), strings.Join(ls, ""), changed, coordinatorPrompt(coordinatorTemplate, s.Project, bin))
}

// deliverWakesLocked is J12: when the coordinator run is idle, one bounded batch
// of held wakes is sent (at-least-once: a failed send or commit keeps wakes held).
func (e *Engine) deliverWakesLocked(ctx context.Context, id string, s *flow.State, now time.Time) {
	if len(s.Project.Wakes) == 0 || s.Project.RunID == "" || e.HC == nil || e.sendCoordLocked(ctx, id, s, wakeMessage(*s), now, true) != nil {
		return
	}
	n := min(len(s.Project.Wakes), maxFactsWakes)
	keys := make([]string, n)
	for i, w := range s.Project.Wakes[:n] {
		keys[i] = cmp.Or(w.Task, "project") + ":" + w.Key
	}
	s.Project.Wakes, s.Project.CoordLastWakeAt = s.Project.Wakes[n:], now
	e.commitLogLocked(id, s, LogLine{By: "engine", Rule: "J12", Asked: keys, Decision: fmt.Sprintf("delivered %d wake(s) to the coordinator", len(keys)), Why: "wakes held and the coordinator is idle"})
}

func (e *Engine) sweepProjectLocked(ctx context.Context, id string, s *flow.State) {
	if s.Project.Folder == "" {
		return
	}
	now := e.now()
	if last := flow.LastAct(s.Project.Acts, "sweep"); last != nil && now.Sub(last.At) < 10*time.Minute {
		return
	}
	gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
	probes := world.Sweep(gctx, e.HC, s.Project.Folder, filepath.Join(e.Paths.project(id), "worktrees"), *s)
	cancel()
	for _, p := range probes {
		e.logOnce("factory.sweep_unknown", id, p.Probe, map[bool]error{true: fmt.Errorf("%s", p.Unknown)}[p.Unknown != ""])
	}
	s.Project.Acts = append(slices.DeleteFunc(s.Project.Acts, func(a flow.Act) bool { return a.Key == "sweep" }), flow.Act{Key: "sweep", Result: "done", At: now})
	var asked, whys []string
	for _, w := range flow.SweepWakes(s.Project.Acts, probes) {
		s.Project.Acts = append(s.Project.Acts, flow.Act{Key: w.ActKey, Result: "done", At: now})
		holdProjectWake(s, w.WakeKey, "sweep: "+w.Summary, w.Summary, now)
		asked, whys = append(asked, w.ActKey), append(whys, w.Summary)
	}
	if len(asked) > 0 {
		e.commitLogLocked(id, s, LogLine{By: "engine", Rule: "sweep", Asked: asked, Decision: fmt.Sprintf("sweep found %d leftover(s): %s", len(asked), strings.Join(whys, "; ")), Why: strings.Join(whys, "; ")})
	} else {
		e.saveLocked(id, *s)
	}
}
