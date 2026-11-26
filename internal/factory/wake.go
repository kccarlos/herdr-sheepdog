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
	facts := taskFacts(t, f, now)
	if rc := flow.OpenRecap(t); rc != nil {
		switch rc.Trigger {
		case flow.TriggerOwnerNo:
			if a := flow.GateNoSinceRecap(s.Asks, t, flow.OpenAttempt(t)); a != nil {
				facts += fmt.Sprintf("; owner answered %q", flow.AskAnswerWords(*a))
			}
		case flow.TriggerReview:
			if words := flow.LatestOwnerAskAnswer(s.Asks, t.ID, flow.AskedByN24); words != "" {
				facts += fmt.Sprintf("; owner answered %q", words)
			}
		}
	}
	s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: k, Task: t.ID, Why: row + ": " + why, Facts: facts, At: now})
	return "wake held for the coordinator"
}

func appendWake(s *flow.State, task, k, why, facts string, now time.Time) {
	if !slices.ContainsFunc(s.Project.Wakes, func(w flow.Wake) bool { return w.Task == task && w.Key == k }) {
		s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: k, Task: task, Why: why, Facts: facts, At: now})
	}
}

func holdProjectWake(s *flow.State, k, why, facts string, now time.Time) {
	appendWake(s, "", k, why, facts, now)
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
				if f.LinesGenerated > 0 {
					out += fmt.Sprintf("; %d generated lines not counted", f.LinesGenerated)
				}
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
		if a := flow.OpenAttempt(t); a != nil {
			if rs := flow.AttemptRetros(*a); len(rs) > 0 {
				r := rs[len(rs)-1]
				for i := len(t.Retros) - 1; i >= 0; i-- {
					if ret := t.Retros[i]; ret.Attempt == len(t.Attempts) && ret.Round == r.N {
						out += fmt.Sprintf("; retro r%d %s (cause %s; coordinator %s: %s; report %s)",
							r.N, cmp.Or(r.Verdict, "none"), ret.Cause, ret.CoordinatorChangeKind, ret.CoordinatorChange, cmp.Or(r.ReportPath, "none"))
						break
					}
				}
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
	fmt.Fprintf(&b, "%s#%d %d task wake(s) wait on you.\n", flow.CoordWakePromptPrefix, s.Seq+1, len(s.Project.Wakes))
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
			st, why, _ := taskStageWhy(t, s.Tasks, s.Asks, s.Settings, s.Project.Started)
			ls = append(ls, fmt.Sprintf("- %s (%s): %s — %s\n", t.ID, st, cmp.Or(t.LastRow, "-"), cmp.Or(why, st)))
		}
	}
	planLine := ""
	if pf := flow.OpenPlanFindings(s.Project); len(pf) > 0 {
		items := make([]string, len(pf))
		for i, f := range pf {
			items[i] = f.Kind + ": " + f.Task + " " + f.Why
		}
		planLine = fmt.Sprintf("Open plan findings (%d): %s.\n", len(pf), strings.Join(items, "; "))
	}
	changed := cmp.Or(strings.Join(s.Settings.Changed(), ", "), "none")
	if heartbeat {
		body := map[bool]string{true: fmt.Sprintf("No plan applied yet. Goal: %s\n", s.Project.Goal), false: fmt.Sprintf("Tasks: %d open, %d ended.\n%s", open, len(s.Tasks)-open, strings.Join(ls, ""))}[len(s.Tasks) == 0]
		return fmt.Sprintf("Wake: heartbeat (no wake for %dm).\n%s%sSettings that differ from their default: %s.", s.Settings.HeartbeatMinutes, body, planLine, changed)
	}
	return fmt.Sprintf("Handoff from the store (replacement coordinator; held wakes follow once idle):\nLive workers: %s.\n%s%sSettings that differ from their default: %s.\n\n%s",
		cmp.Or(strings.Join(ws, ", "), "none"), strings.Join(ls, ""), planLine, changed, coordinatorPrompt(coordinatorTemplate, s.Project, bin))
}

// deliverWakesLocked is J12: when the coordinator run is idle, one bounded batch
// of held wakes is sent (at-least-once: a failed send or commit keeps wakes held).
func (e *Engine) deliverWakesLocked(ctx context.Context, id string, s *flow.State, now time.Time) {
	next := *s
	p := &next.Project
	if len(p.Wakes) == 0 || len(p.CoordWakesInFlight) != 0 || p.RunID == "" || p.Run == nil || e.HC == nil {
		return
	}
	run := *p.Run
	p.Run = &run
	if e.sendCoordLocked(ctx, id, &next, wakeMessage(next), now, false) != nil {
		return
	}
	n := min(len(p.Wakes), maxFactsWakes)
	p.CoordWakesInFlight, p.Wakes = p.Wakes[:n], p.Wakes[n:]
	p.CoordWakeRetried = slices.ContainsFunc(p.CoordWakesInFlight, func(w flow.Wake) bool { return w.Retried })
	p.CoordWakeAt, p.CoordLastWakeAt, p.CoordWakeTurn, p.CoordWakeSeq = now, now, run.Turn, next.Seq+1
	keys := make([]string, n)
	for i, w := range p.CoordWakesInFlight {
		keys[i] = coordWakeKey(w)
	}
	if e.commitLogLocked(id, &next, LogLine{By: "engine", Rule: "J12", Asked: keys, Decision: fmt.Sprintf("delivered %d wake(s) to the coordinator", n), Why: "wakes held and the coordinator is idle"}) {
		*s = next
	}
}

func coordWakeKey(w flow.Wake) string { return cmp.Or(w.Task, "project") + ":" + w.Key }

func clearCoordBatch(p *flow.Project) {
	p.CoordWakesInFlight, p.CoordWakeSeq, p.CoordWakeRetried, p.CoordWakeAt = nil, 0, false, time.Time{}
}

// returnCoordWakes gives newer held duplicates priority and leaves their retry bits intact.
func returnCoordWakes(p *flow.Project, retry bool) (retried, exhausted []string) {
	var returned []flow.Wake
	for _, w := range p.CoordWakesInFlight {
		if slices.ContainsFunc(p.Wakes, func(held flow.Wake) bool { return held.Task == w.Task && held.Key == w.Key }) {
			continue
		}
		if retry {
			if w.Retried {
				exhausted = append(exhausted, coordWakeKey(w))
				continue
			}
			w.Retried = true
			retried = append(retried, coordWakeKey(w))
		}
		returned = append(returned, w)
	}
	if len(returned) > 0 {
		p.Wakes = append(returned, p.Wakes...)
	}
	clearCoordBatch(p)
	return retried, exhausted
}

func recoverEndedCoordWakes(p *flow.Project) bool {
	if p.Run == nil || p.Run.State != flow.PhaseEnded || len(p.CoordWakesInFlight) == 0 {
		return false
	}
	returnCoordWakes(p, false)
	return true
}

// reconcileCoordWakesLocked handles a later idle tick and persists its outcome.
func (e *Engine) reconcileCoordWakesLocked(id string, s *flow.State, tick flow.RunEvent, now time.Time) bool {
	p, r := &s.Project, s.Project.Run
	if tick.Kind != flow.EvTick || r.State != flow.PhaseIdle || r.Turn <= p.CoordWakeTurn || len(p.CoordWakesInFlight) == 0 {
		return false
	}
	p.CoordWakeAt, p.CoordSilentStops = time.Time{}, 0
	reason := tick.WakeReason
	switch {
	case r.Status == "error":
		reason = "coordinator turn reported an error"
	case reason == flow.WakeEvidenceUnavailable:
		if !r.LastTurnEnd.IsZero() && now.Before(r.LastTurnEnd.Add(time.Duration(s.Settings.IdleGraceSeconds)*time.Second)) {
			e.saveLocked(id, *s)
			return true
		}
	case p.CoordWakeSeq != 0 && tick.WakeSeq == p.CoordWakeSeq && tick.WakeConfirmed:
		clearCoordBatch(p)
		e.saveLocked(id, *s)
		return true
	case p.CoordWakeSeq == 0:
		reason = "wake delivery has no sequence"
	case tick.WakeSeq != 0 && tick.WakeSeq != p.CoordWakeSeq:
		reason = "screen shows a stale wake delivery"
	}
	next := *s
	retried, exhausted := returnCoordWakes(&next.Project, true)
	keys := append(slices.Clone(retried), exhausted...)
	saved := false
	if len(keys) == 0 {
		saved = e.saveLocked(id, next)
	} else {
		decision := fmt.Sprintf("unconfirmed wake: retried [%s], exhausted [%s]", strings.Join(retried, ", "), strings.Join(exhausted, ", "))
		saved = e.commitLogLocked(id, &next, LogLine{By: "engine", Rule: "J12", Asked: keys, Decision: decision, Why: reason})
	}
	if saved {
		*s = next
	}
	return true
}

func (e *Engine) sweepDueLocked(id string, s flow.State, now time.Time) bool {
	prev, ok := e.lastSweep[id]
	return s.Project.Started && s.Project.Folder != "" && (!ok || now.Sub(prev) >= time.Duration(min(flow.SweepMax, max(flow.SweepMin, s.Settings.SweepMinutes)))*time.Minute)
}

func (e *Engine) probeCodexHome(ctx context.Context, id string) flow.SweepProbe {
	e.mu.Lock()
	s, err := LoadState(e.Paths.State(id))
	due := e.Paths.DataDir != "" && err == nil && e.sweepDueLocked(id, s, e.now())
	e.mu.Unlock()
	if !due {
		return flow.SweepProbe{}
	}
	pctx, cancel := context.WithTimeout(ctx, min(lockGitTimeout, world.GitStageTimeout))
	defer cancel()
	return e.git.SweepCodexHome(pctx, e.Paths.CodexHome())
}

func (e *Engine) sweepProjectLocked(ctx context.Context, id string, s *flow.State, codex flow.SweepProbe) {
	now := e.now()
	if !e.sweepDueLocked(id, *s, now) {
		return
	}
	gctx, cancel := context.WithTimeout(ctx, min(3*lockGitTimeout, world.GitStageTimeout))
	probes := e.git.Sweep(gctx, e.HC, s.Project.Folder, filepath.Join(e.Paths.project(id), "worktrees"), *s)
	cancel()
	if ctx.Err() != nil {
		return
	}
	if codex.Probe != "" {
		probes = append(probes, codex)
	}
	notices, changed := flow.SweepEpisodes(&s.Project.SweepItems, &s.Project.SweepUnknown, &s.Project.Wakes, probes)
	e.lastSweep[id] = now
	var asked, whys []string
	for _, n := range notices {
		asked, whys = append(asked, n.ActKey), append(whys, n.Summary)
		if n.WakeKey != "" {
			holdProjectWake(s, n.WakeKey, "sweep: "+n.Summary, n.Summary, now)
		} else {
			e.logEvent("factory.sweep_unknown", map[string]string{"project": id, "error": n.Summary})
		}
	}
	if len(notices) > 0 {
		why := strings.Join(whys, "; ")
		e.commitLogLocked(id, s, LogLine{By: "engine", Rule: "sweep", Asked: asked, Decision: "sweep: " + why, Why: why})
	} else if changed {
		e.saveLocked(id, *s)
	}
}
