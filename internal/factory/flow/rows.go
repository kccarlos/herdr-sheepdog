package flow

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// What a row asks for. A key is the verb and the commit or round it is
// for, so an act in acts[] marks it asked (design.md 3.2, "done once" keys).
const (
	AskNewAttempt   = "new_attempt"
	AskStartWorker  = "start_worker"
	AskStartReview  = "start_reviewer"
	AskTellFindings = "tell_worker_findings"
	AskOpenRecap    = "open_recap"
	AskSizeRecap    = "open_size_recap"
	AskTellStop     = "tell_worker_stop"
	AskMergeOnly    = "move_h_merge_only"
	AskPushPR       = "push_and_open_pr"
	AskRerunCI      = "rerun_ci"
	AskTellCI       = "tell_worker_ci"
	AskDispatchCI   = "dispatch_ci"
	AskAccept       = "record_accept"
	AskMerge        = "merge"
	AskMarkDone     = "mark_done"
	AskCleanup      = "cleanup"
	AskWake         = "wake_coordinator"
	AskStopTask     = "stop_task"
	AskStopRuns     = "stop_runs"
)

// commitKeyed are the verbs whose key ends in the commit it is for.
var commitKeyed = []string{AskPushPR, AskStartReview, AskRerunCI, AskTellCI, AskDispatchCI, AskAccept, AskMerge}

// key joins a verb and what it is for: "merge@3f9c".
func key(verb string, parts ...any) string {
	s := []string{verb}
	for _, p := range parts {
		s = append(s, fmt.Sprint(p))
	}
	return strings.Join(s, "@")
}

// once asks for k unless acts already hold it, whatever its result. Retries
// and back-off are section 8 (slice 5a).
func once(acts []Act, k string) []string {
	if Count(acts, k) > 0 {
		return nil
	}
	return []string{k}
}

// ensureRun is the first two lines of design.md 4.8: a start in flight
// counts as present; none yet starts fresh. A live run needs nothing.
func ensureRun(in In, r Run, start string) []string {
	if r.Pending || r.State != RunNone {
		return nil
	}
	return once(in.acts(), start)
}

// Clause is one named part of a row's condition (design.md 4: "each clause
// is named").
type Clause = func(In) bool

func all(cs ...Clause) Clause {
	return func(in In) bool {
		for _, c := range cs {
			if !c(in) {
				return false
			}
		}
		return true
	}
}

func anyOf(cs ...Clause) Clause {
	return func(in In) bool {
		for _, c := range cs {
			if c(in) {
				return true
			}
		}
		return false
	}
}

func headH(in In) string {
	if a := in.attempt(); a != nil {
		return H(*a)
	}
	return ""
}

func ended(in In) bool                { return Ended(in.Task) }
func recapOpen(in In) bool            { return OpenRecap(in.Task) != nil }
func prMerged(in In) bool             { return in.Facts.PR.State == PRMerged }
func noAttempt(in In) bool            { return in.attempt() == nil }
func hasAttempt(in In) bool           { return in.attempt() != nil }
func depsNotDone(in In) bool          { return len(in.Facts.DepsNotDone) > 0 }
func depsDropped(in In) bool          { return len(in.Facts.DepsDropped) > 0 }
func noSlot(in In) bool               { return in.Facts.NoSlot }
func workerGone(in In) bool           { return in.Facts.Worker.State == RunGone }
func reviewGone(in In) bool           { return in.Facts.Reviewer.State == RunGone }
func noReady(in In) bool              { return len(in.attempt().Ready) == 0 }
func tipAhead(in In) bool             { return in.Facts.TipRel == RelAhead } // merge_only is caught by N20 above
func noPR(in In) bool                 { return in.Facts.PR.State == PRNone }
func prOpen(in In) bool               { return in.Facts.PR.State == PROpen }
func prNotClosed(in In) bool          { return in.Facts.PR.State != PRClosed }
func tipSame(in In) bool              { return in.Facts.TipRel == RelSame } // slice 1: diverged falls to N43 (N7 comes later)
func prBehindH(in In) bool            { return in.Facts.PR.HeadRel == RelBehind }
func prHeadIsH(in In) bool            { return in.Facts.PR.Head == headH(in) }
func answered(in In) bool             { return in.Facts.PR.Answered }
func unanswered(in In) bool           { return !answered(in) }
func githubUnavail(in In) bool        { return in.Facts.GitHubUnavailable }
func githubAvail(in In) bool          { return !githubUnavail(in) }
func notBehind(in In) bool            { return !in.Facts.PR.Behind }
func checksGreen(in In) bool          { return in.Facts.PR.Checks == ChecksGreen }
func checksRed(in In) bool            { return in.Facts.PR.Checks == ChecksRed }
func checksStoppedExt(in In) bool     { return in.Facts.PR.Checks == ChecksStoppedExternal }
func checksStoppedTimeout(in In) bool { return in.Facts.PR.Checks == ChecksStoppedTimeout }
func checksStoppedUnknown(in In) bool { return in.Facts.PR.Checks == ChecksStoppedUnknown }
func checksPend(in In) bool           { return in.Facts.PR.Checks == ChecksPending }
func checksUnreadable(in In) bool     { return in.Facts.PR.Checks == ChecksUnreadable }
func mergeClean(in In) bool           { return in.Facts.PR.Mergeable == MergeableClean }
func mergeUnkn(in In) bool            { return in.Facts.PR.Mergeable == MergeableUnknown }
func working(in In) bool              { return in.Facts.Worker.State == RunWorking }
func notWorking(in In) bool           { return !working(in) }
func mergeAsked(in In) bool           { return Count(in.acts(), key(AskMerge, headH(in))) > 0 }

func checksGrace(in In) time.Duration {
	return time.Duration(in.Settings.ChecksGraceMinutes) * time.Minute
}
func checksAbsentInGrace(in In) bool {
	return in.Facts.PR.Checks == ChecksAbsent && in.Now.Sub(in.Facts.PR.HeadSince) < checksGrace(in)
}
func checksAbsentPastGrace(in In) bool {
	return in.Facts.PR.Checks == ChecksAbsent && in.Now.Sub(in.Facts.PR.HeadSince) >= checksGrace(in)
}
func checksSeenNone(in In) bool { return !in.Facts.PR.ChecksUnread } // absent from a failed read is not "no checks"
func ciReady(c Clause) Clause {
	return all(ladderSays(Passed), prOpen, prHeadIsH, answered, githubAvail, c)
}

func ladderSays(answer string) Clause {
	return func(in In) bool { return hasAttempt(in) && Ladder(in) == answer }
}

func say(why string, asked ...[]string) func(In) Decision {
	return func(In) Decision { return Decision{Asked: join(asked), Why: why} }
}

func join(asked [][]string) []string {
	var out []string
	for _, a := range asked {
		out = append(out, a...)
	}
	return out
}

// TaskRows is the slice-1 task table, in design.md section 4's order. S1
// stands in for N5 and the rest of 4.8 until slice 5a (plan.md section 3).
// Landing rows (4.7) carry the ladder's "passed" as a clause, since slice 1
// has no ladder row for retro or recap to catch the rest.
var TaskRows = []Row[In]{
	// S2 is the stuck stand-in until slice 5a's section 4.8 (plan.md PR
	// 2.1). It sits above N1, since a failed cleanup act belongs to an ended
	// task. It holds only for the pass that queues its wake, so the rows
	// below go on afterwards. (A retry-closed attempt's worktree is cleaned
	// by N1 only once the task ends; slices 1 and 2 keep it.)
	{ID: "S2", WaitsOn: "the coordinator", When: func(in In) bool { _, k := stuckWake(in); return k != nil },
		Then: func(in In) Decision {
			what, k := stuckWake(in)
			return say(what+" never answered for stuck_minutes", k)(in)
		}},
	{ID: "N1", WaitsOn: "nobody", When: ended, Then: func(in In) Decision {
		return say("ended: stop its runs and clean up", once(in.acts(), AskCleanup))(in)
	}},
	{ID: "N2", WaitsOn: "nobody", When: prMerged, Then: func(in In) Decision {
		asked := once(in.acts(), AskMarkDone)
		if !hasAccept(in, in.Facts.PR.Head) {
			asked = append(asked, once(in.acts(), key(AskWake, "merged_outside_bar"))...)
		}
		return Decision{Asked: asked, Why: "the PR is merged"}
	}},
	// N3 asks while a run is recorded, not once: the stop clears the
	// records, and a failed stop is tried again next pass.
	{ID: "N3", WaitsOn: "the coordinator", When: func(in In) bool { return in.Task.Parked }, Then: func(in In) Decision {
		if a := in.attempt(); a != nil && (a.RunID != "" || a.Review != nil) {
			return say("parked: stop its runs", []string{AskStopRuns})(in)
		}
		return say("parked")(in)
	}},
	{ID: "S1", WaitsOn: "the owner", When: all(hasAttempt, anyOf(workerGone, all(ladderSays(Review), reviewGone))), Then: func(in In) Decision {
		who := "worker"
		if !workerGone(in) {
			who = "reviewer"
		}
		return say("the "+who+" run ended: the task is stopped", once(in.acts(), AskStopTask))(in)
	}},
	// N6 tells the worker of a size recap once to stop (it has no
	// recap-written report yet, so nothing waits for one).
	{ID: "N6", WaitsOn: "the coordinator", When: recapOpen, Then: func(in In) Decision {
		n, tell := len(in.Task.Recaps), []string(nil)
		if OpenRecap(in.Task).Trigger == TriggerSize && in.Facts.Worker.State != RunNone {
			tell = once(in.acts(), key(AskTellStop, n))
		}
		return say("a recap is open", once(in.acts(), key(AskWake, "recap", n)), tell)(in)
	}},
	{ID: "N8", WaitsOn: "the coordinator", When: func(in In) bool { return hasAttempt(in) && in.attempt().NothingToChange != "" }, Then: func(in In) Decision {
		a := in.attempt()
		return say("the worker says nothing to change: "+a.NothingToChange, once(in.acts(), key(AskWake, "nothing_to_change", len(a.Rounds))))(in)
	}},
	{ID: "N9", WaitsOn: "dependencies", When: all(noAttempt, anyOf(depsNotDone, depsDropped)), Then: func(in In) Decision {
		var asked []string
		for _, dep := range in.Facts.DepsDropped {
			asked = append(asked, once(in.acts(), key(AskWake, "dep_dropped", dep))...)
		}
		return say(n9Why(in), asked)(in)
	}},
	// N10: milestone and quota (no worker candidate resolves) come in later slices (design.md 4.2).
	{ID: "N10", WaitsOn: "a slot", When: all(noAttempt, noSlot), Then: say("no slot is free")},
	{ID: "N11", WaitsOn: "the engine", When: noAttempt, Then: func(in In) Decision {
		return say("no attempt: start one", once(in.acts(), AskNewAttempt))(in)
	}},
	// N12 has no "no owner override covers H" clause until slice 7.
	{ID: "N12", WaitsOn: "the coordinator", When: func(in In) bool {
		return hasAttempt(in) && (in.Facts.LinesTimeout || in.Facts.Lines > in.attempt().SizeCap)
	},
		Then: func(in In) Decision {
			why := fmt.Sprintf("%d changed lines, over the size cap %d", in.Facts.Lines, in.attempt().SizeCap)
			if in.Facts.LinesTimeout {
				why = fmt.Sprintf("changed-lines read timed out (counts as over the size cap %d)", in.attempt().SizeCap)
			}
			return say(why, once(in.acts(), key(AskSizeRecap, len(in.Task.Recaps))))(in)
		}},
	{ID: "N20", WaitsOn: "the engine", When: func(in In) bool { return hasAttempt(in) && tipAhead(in) && in.Facts.TipMergeOnly },
		Then: func(in In) Decision {
			return say("the tip is ahead of H by merges of the base only: H moves to it, no round", once(in.acts(), key(AskMergeOnly, in.Facts.Tip)))(in)
		}},
	{ID: "N22", WaitsOn: "the worker", When: all(ladderSays(Fix), prNotClosed), Then: func(in In) Decision {
		r := newestCovering(*in.attempt(), headH(in))
		return say("the ladder says fix", ensureRun(in, in.Facts.Worker, AskStartWorker),
			once(in.acts(), key(AskTellFindings, r.N)))(in)
	}},
	{ID: "N23", WaitsOn: "the worker", When: all(hasAttempt, anyOf(noReady, tipAhead)), Then: func(in In) Decision {
		return say("the worker is working", ensureRun(in, in.Facts.Worker, AskStartWorker))(in)
	}},
	{ID: "N25", WaitsOn: "the coordinator", When: all(ladderSays(RecapAnswer), prNotClosed), Then: func(in In) Decision {
		return say("the ladder says recap", once(in.acts(), key(AskOpenRecap, "review")))(in)
	}},
	{ID: "N26", WaitsOn: "the engine", When: all(hasAttempt, anyOf(noPR, all(prOpen, prBehindH))), Then: func(in In) Decision {
		if unanswered(in) { // "no PR" from a failed read is a guess: wait, so the once-only push is not spent
			return say("GitHub did not answer: wait for a snapshot")(in)
		}
		return say("GitHub does not have H", once(in.acts(), key(AskPushPR, headH(in))))(in)
	}},
	{ID: "N29", WaitsOn: "the reviewer", When: all(ladderSays(Review), prNotClosed), Then: func(in In) Decision {
		start := key(AskStartReview, nextRound(*in.attempt()), headH(in))
		return say("the ladder says review", ensureRun(in, in.Facts.Reviewer, start))(in)
	}},
	{ID: "N33", WaitsOn: "CI", When: ciReady(checksRed), Then: func(in In) Decision {
		h := headH(in)
		if ask, wait := maybeRerun(in, h); wait {
			return say("re-run requested; waiting for CI")(in)
		} else if ask != nil {
			return say("checks red: re-run failed jobs", ask)(in)
		}
		return say("checks red at H: tell the worker",
			ensureRun(in, in.Facts.Worker, AskStartWorker),
			once(in.acts(), key(AskTellCI, cmp.Or(in.Facts.PR.FailedRun, "none"), h)))(in)
	}},
	{ID: "N34", WaitsOn: "CI", When: ciReady(anyOf(checksStoppedExt, checksStoppedTimeout, checksStoppedUnknown)), Then: func(in In) Decision {
		h := headH(in)
		if checksStoppedTimeout(in) {
			return say("a CI job hit its own time limit", once(in.acts(), key(AskWake, "checks_stopped", "timeout", h)))(in)
		}
		if checksStoppedUnknown(in) { // why it was cancelled is unread: a time limit, never re-run
			return say("a CI job was cancelled and why is unread: treated as a time limit", once(in.acts(), key(AskWake, "checks_stopped", "unknown", h)))(in)
		}
		if ask, wait := maybeRerun(in, h); wait {
			return say("re-run requested; waiting for CI")(in)
		} else if ask != nil {
			return say("checks cancelled from outside: re-run failed jobs", ask)(in)
		}
		return say("checks cancelled from outside with no re-runs left", once(in.acts(), key(AskWake, "checks_stopped", "external", h)))(in)
	}},
	{ID: "N35", WaitsOn: "CI", When: ciReady(all(checksAbsentPastGrace, checksSeenNone)), Then: func(in In) Decision {
		h := headH(in)
		dk := key(AskDispatchCI, h)
		act := LastAct(in.acts(), dk)
		if act == nil {
			return say("no checks past checks_grace_minutes: dispatch the workflow", []string{dk})(in)
		}
		if act.Result == "done" && in.Now.Sub(act.At) < checksGrace(in) {
			return say("workflow dispatched; waiting for checks")(in)
		}
		return say("no checks past checks_grace_minutes: push a change that triggers a workflow, ask for an owner override, or drop the task",
			once(in.acts(), key(AskWake, "no_checks", h)))(in)
	}},
	{ID: "N36", WaitsOn: "CI", When: all(ladderSays(Passed), prOpen, anyOf(checksPend, checksUnreadable, unanswered, githubUnavail, checksAbsentInGrace, mergeUnkn)),
		Then: say("checks pending or unreadable, or mergeable unknown")},
	{ID: "N39", WaitsOn: "the worker", When: all(ladderSays(Passed), prOpen, working), Then: say("the worker is working")},
	{ID: "N41", WaitsOn: "GitHub", When: all(ladderSays(Passed), mergeAsked, prOpen, notBehind),
		Then: say("a merge was asked for H")},
	{ID: "N42", WaitsOn: "GitHub",
		When: all(ladderSays(Passed), prOpen, prHeadIsH, tipSame, answered, githubAvail, checksGreen, mergeClean, notBehind, notWorking),
		Then: func(in In) Decision {
			h := headH(in)
			return say("passed, green and mergeable at H", once(in.acts(), key(AskAccept, h)), once(in.acts(), key(AskMerge, h)))(in)
		}},
	{ID: "N43", WaitsOn: "the coordinator", Then: func(in In) Decision {
		// Once per stall: H and the facts that left it unmatched.
		p := in.Facts.PR
		return say("no rule matched", once(in.acts(), key(AskWake, "no_rule_matched", headH(in), p.State, p.Checks, p.Mergeable, in.Facts.TipRel)))(in)
	}},
}

func n9Why(in In) string {
	var parts []string
	if len(in.Facts.DepsNotDone) > 0 {
		parts = append(parts, "waiting on "+strings.Join(in.Facts.DepsNotDone, ", "))
	}
	if len(in.Facts.DepsDropped) > 0 {
		parts = append(parts, "depends on dropped "+strings.Join(in.Facts.DepsDropped, ", "))
	}
	return strings.Join(parts, "; ")
}

func rerunsFor(acts []Act, h string) int {
	n := 0
	for _, a := range acts {
		if strings.HasPrefix(a.Key, AskRerunCI+"@") && strings.HasSuffix(a.Key, "@"+h) {
			n++
		}
	}
	return n
}

func maybeRerun(in In, h string) (ask []string, wait bool) {
	rk := key(AskRerunCI, cmp.Or(in.Facts.PR.FailedRun, "none"), h)
	if in.Facts.PR.FailedJob != "" {
		rk = key(AskRerunCI, cmp.Or(in.Facts.PR.FailedRun, "none"), in.Facts.PR.FailedJob, h)
		if a := LastAct(in.acts(), rk); a != nil && a.Result == "done" {
			return nil, true
		}
	}
	if rerunsFor(in.acts(), h) < in.Settings.FlakeReruns && LastAct(in.acts(), rk) == nil {
		return []string{rk}, false
	}
	return nil, false
}

func hasAccept(in In, head string) bool {
	if a := in.attempt(); a != nil {
		for _, ac := range a.Accepts {
			if ac.Head == head {
				return true
			}
		}
	}
	return false
}

// stuckWake is S2's: the first stall of the open attempt that has waited
// stuck_minutes and has not woken the coordinator yet, and its wake key
// stuck@<what>@<H> (so a new H or another stall wakes again). A run that
// never answered is exactly: an act whose result is failed (a failed
// start, merge or cleanup, or a voided round; its clock is the act's At)
// for H or for no commit (a failed act for an older H, such as a round
// voided before H moved, is spent), or the round's reviewer read idle with
// no valid tag (review.idle_since).
func stuckWake(in In) (string, []string) {
	limit := time.Duration(in.Settings.StuckMinutes) * time.Minute
	try := func(what string, at time.Time) []string {
		if in.Now.Sub(at) < limit {
			return nil
		}
		return once(in.acts(), key(AskWake, "stuck", what, headH(in)))
	}
	for _, a := range in.acts() {
		verb, _, _ := strings.Cut(a.Key, "@")
		if a.Result != "failed" || slices.Contains(commitKeyed, verb) && !strings.HasSuffix(a.Key, "@"+headH(in)) {
			continue
		}
		if k := try(a.Key, a.At); k != nil {
			return a.Key, k
		}
	}
	if a := in.attempt(); a != nil && a.Review != nil && a.Review.IdleSince != nil {
		what := key("reviewer_idle", a.Review.Round)
		return what, try(what, *a.Review.IdleSince)
	}
	return "", nil
}
