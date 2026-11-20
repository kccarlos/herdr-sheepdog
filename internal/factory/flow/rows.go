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
	AskCIRecap      = "open_ci_recap"
	AskTellStop     = "tell_worker_stop"
	AskTellConflict = "tell_worker_conflict"
	AskMergeOnly    = "move_h_merge_only"
	AskPushPR       = "push_and_open_pr"
	AskRerunCI      = "rerun_ci"
	AskTellCI       = "tell_worker_ci"
	AskDispatchCI   = "dispatch_ci"
	AskUpdateBranch = "update_branch"
	AskFailUpdate   = "fail_update_branch"
	AskAccept       = "record_accept"
	AskMerge        = "merge"
	AskMarkDone     = "mark_done"
	AskCleanup      = "cleanup"
	AskWake         = "wake_coordinator"
	AskStopTask     = "stop_task"
	AskStopRuns     = "stop_runs"
)

// commitKeyed are the verbs whose key ends in the commit it is for.
var commitKeyed = []string{AskPushPR, AskStartReview, AskTellConflict, AskRerunCI, AskTellCI, AskDispatchCI, AskUpdateBranch, AskAccept, AskMerge}

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

func ended(in In) bool       { return Ended(in.Task) }
func recapOpen(in In) bool   { return OpenRecap(in.Task) != nil }
func prMerged(in In) bool    { return in.Facts.PR.State == PRMerged }
func noAttempt(in In) bool   { return in.attempt() == nil }
func hasAttempt(in In) bool  { return in.attempt() != nil }
func depsNotDone(in In) bool { return len(in.Facts.DepsNotDone) > 0 }
func depsDropped(in In) bool { return len(in.Facts.DepsDropped) > 0 }
func noSlot(in In) bool      { return in.Facts.NoSlot }
func workerGone(in In) bool  { return in.Facts.Worker.State == RunGone }
func reviewGone(in In) bool  { return in.Facts.Reviewer.State == RunGone }
func noReady(in In) bool     { return len(in.attempt().Ready) == 0 }
func tipAhead(in In) bool    { return in.Facts.TipRel == RelAhead } // merge_only is caught by N20 above
func noPR(in In) bool        { return in.Facts.PR.State == PRNone }
func prOpen(in In) bool      { return in.Facts.PR.State == PROpen }
func prNotClosed(in In) bool { return in.Facts.PR.State != PRClosed }
func tipAtOrBehindH(in In) bool {
	a := in.attempt()
	return in.Facts.TipRel == RelSame || a != nil && in.Facts.TipRel == RelBehind && Covers(*a, in.Facts.Tip, H(*a))
}                          // diverged or uncovered tip falls to N43 until N7
func prBehindH(in In) bool { return in.Facts.PR.HeadRel == RelBehind }
func prHeadIsH(in In) bool { return in.Facts.PR.Head == headH(in) }
func prHeadAheadMergeOnly(in In) bool {
	return prOpen(in) && answered(in) && in.Facts.PR.HeadRel == RelAhead && in.Facts.PR.HeadMergeOnly
}
func answered(in In) bool             { return in.Facts.PR.Answered }
func unanswered(in In) bool           { return !answered(in) }
func githubUnavail(in In) bool        { return in.Facts.GitHubUnavailable }
func githubAvail(in In) bool          { return !githubUnavail(in) }
func prBehindBase(in In) bool         { return in.Facts.PR.Behind && !in.Facts.PR.BehindUnknown }
func prBehindUnknown(in In) bool      { return in.Facts.PR.BehindUnknown }
func notBehind(in In) bool            { return !in.Facts.PR.Behind && !in.Facts.PR.BehindUnknown }
func checksGreen(in In) bool          { return in.Facts.PR.Checks == ChecksGreen }
func checksRed(in In) bool            { return in.Facts.PR.Checks == ChecksRed }
func checksStoppedExt(in In) bool     { return in.Facts.PR.Checks == ChecksStoppedExternal }
func checksStoppedTimeout(in In) bool { return in.Facts.PR.Checks == ChecksStoppedTimeout }
func checksStoppedUnknown(in In) bool { return in.Facts.PR.Checks == ChecksStoppedUnknown }
func checksPend(in In) bool           { return in.Facts.PR.Checks == ChecksPending }
func checksUnreadable(in In) bool     { return in.Facts.PR.Checks == ChecksUnreadable }
func mergeClean(in In) bool           { return in.Facts.PR.Mergeable == MergeableClean }
func mergeConflict(in In) bool        { return in.Facts.PR.Mergeable == MergeableConflict }
func mergeUnkn(in In) bool            { return in.Facts.PR.Mergeable == MergeableUnknown }
func reviewRequired(in In) bool       { return in.Facts.PR.ReviewRequired }
func noReviewRequired(in In) bool     { return !reviewRequired(in) }
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
	// N6 tells the worker of a size or ci recap once to stop (it has no
	// recap-written report yet, so nothing waits for one).
	{ID: "N6", WaitsOn: "the coordinator", When: recapOpen, Then: func(in In) Decision {
		n, tell, trig := len(in.Task.Recaps), []string(nil), OpenRecap(in.Task).Trigger
		if (trig == TriggerSize || trig == TriggerCI) && in.Facts.Worker.State != RunNone {
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
	// N13 has no "no owner override covers H" clause until slice 7.
	{ID: "N13", WaitsOn: "the coordinator", When: func(in In) bool {
		return hasAttempt(in) && !ladderSays(Passed)(in) && in.Settings.CIFailsPerHour > 0 && ciFailCount(in) >= in.Settings.CIFailsPerHour
	},
		Then: func(in In) Decision {
			why := fmt.Sprintf("%d commits failed CI in the last hour (limit %d)", ciFailCount(in), in.Settings.CIFailsPerHour)
			return say(why, once(in.acts(), key(AskCIRecap, len(in.Task.Recaps))))(in)
		}},
	{ID: "N20", WaitsOn: "the engine", When: func(in In) bool {
		return hasAttempt(in) && ((tipAhead(in) && in.Facts.TipMergeOnly) || prHeadAheadMergeOnly(in))
	},
		Then: func(in In) Decision {
			if tipAhead(in) && in.Facts.TipMergeOnly {
				return say("the tip is ahead of H by merges of the base only: H moves to it, no round", once(in.acts(), key(AskMergeOnly, in.Facts.Tip)))(in)
			}
			return say("the PR head is ahead of H by merges of the base only: H moves to it, no round", once(in.acts(), key(AskMergeOnly, in.Facts.PR.Head)))(in)
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
	{ID: "N32", WaitsOn: "the worker", When: ciReady(mergeConflict), Then: func(in In) Decision {
		return say("PR conflicts with the base: tell the worker once to merge the base (never rebase)",
			ensureRun(in, in.Facts.Worker, AskStartWorker), once(in.acts(), key(AskTellConflict, headH(in))))(in)
	}},
	{ID: "N33", WaitsOn: "CI", When: ciReady(checksRed), Then: func(in In) Decision {
		h := headH(in)
		if asks, wait := rerunOrWait(in, h); len(asks) > 0 || wait {
			return say("checks red: re-run failed jobs, or wait for the re-run or for checks still running", asks)(in)
		}
		var tells []string
		for _, run := range ciRuns(in) {
			tells = append(tells, once(in.acts(), key(AskTellCI, run, h))...)
		}
		return say("checks red at H: tell the worker once per failing run", ensureRun(in, in.Facts.Worker, AskStartWorker), tells)(in)
	}},
	{ID: "N34", WaitsOn: "CI", When: ciReady(anyOf(checksStoppedExt, checksStoppedTimeout, checksStoppedUnknown)), Then: func(in In) Decision {
		h := headH(in)
		if !checksStoppedExt(in) { // a time limit, or a cancel with no evidence of who: one wake for both, never a re-run
			return say("a CI job was stopped by its own time limit or for an unread reason ("+string(in.Facts.PR.Checks)+")",
				once(in.acts(), key(AskWake, "checks_stopped", "limit", h)))(in)
		}
		if asks, wait := rerunOrWait(in, h); len(asks) > 0 || wait {
			return say("checks cancelled from outside: re-run, or wait for the re-run or for checks still running", asks)(in)
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
	{ID: "N36", WaitsOn: "CI", When: all(ladderSays(Passed), prOpen, anyOf(checksPend, checksUnreadable, unanswered, githubUnavail, checksAbsentInGrace, mergeUnkn, prBehindUnknown)),
		Then: func(in In) Decision {
			if prBehindUnknown(in) && !anyOf(checksPend, checksUnreadable, unanswered, githubUnavail, checksAbsentInGrace, mergeUnkn)(in) && in.Now.Sub(in.Facts.PR.HeadSince) >= checksGrace(in) {
				return say("cannot tell if PR is behind the base past checks_grace_minutes", once(in.acts(), key(AskWake, "behind_unknown", headH(in))))(in)
			}
			return say("checks pending or unreadable, or mergeable/behind unknown")(in)
		}},
	{ID: "N37", WaitsOn: "a GitHub approval", When: ciReady(reviewRequired), Then: func(in In) Decision {
		return say("GitHub requires an approving review (branch protection / required approving reviews); nobody in the factory can approve",
			once(in.acts(), key(AskWake, "review_required", headH(in))))(in)
	}},
	{ID: "N39", WaitsOn: "the worker", When: all(ladderSays(Passed), prOpen, working), Then: say("the worker is working")},
	{ID: "N40", WaitsOn: "GitHub", When: ciReady(prBehindBase), Then: func(in In) Decision {
		h := headH(in)
		uk := key(AskUpdateBranch, h)
		act := LastAct(in.acts(), uk)
		if act == nil {
			return say("PR is behind the base: update branch with a merge commit", []string{uk})(in)
		}
		if act.Result == "failed" {
			return say("branch update failed for H")(in)
		}
		if in.Now.Sub(act.At) < checksGrace(in) {
			return say("branch update asked; waiting for the new head")(in)
		}
		return say("branch update did not move the PR head within checks_grace_minutes: failed try", []string{key(AskFailUpdate, h)})(in)
	}},
	{ID: "N41", WaitsOn: "GitHub", When: all(ladderSays(Passed), mergeAsked, prOpen, notBehind),
		Then: say("a merge was asked for H")},
	{ID: "N42", WaitsOn: "GitHub",
		When: all(ladderSays(Passed), prOpen, prHeadIsH, tipAtOrBehindH, answered, githubAvail, checksGreen, mergeClean, noReviewRequired, notBehind, notWorking),
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

// RecentCIFails returns the open attempt's distinct commits with a failing
// check run in the last hour and since the last recap decision, in order of
// first observation (design.md 4.3, N13).
func RecentCIFails(t Task, now time.Time) []CIFail {
	a := OpenAttempt(t)
	if a == nil {
		return nil
	}
	cutoff := now.Add(-time.Hour)
	for _, r := range t.Recaps {
		if r.Decided != nil && r.Decided.At.After(cutoff) {
			cutoff = r.Decided.At
		}
	}
	seen := map[string]bool{}
	var out []CIFail
	for _, f := range a.CIFails {
		if f.Head != "" && f.At.After(cutoff) && !seen[f.Head] {
			seen[f.Head] = true
			out = append(out, f)
		}
	}
	return out
}

func ciFailCount(in In) int { return len(RecentCIFails(in.Task, in.Now)) }

// ciRuns are the failing checks' run ids, sorted; "none" for a check
// with no run (a status, or no run in its URL).
func ciRuns(in In) []string {
	var rs []string
	for _, f := range in.Facts.PR.Failed {
		rs = append(rs, cmp.Or(f.Run, "none"))
	}
	slices.Sort(rs)
	return slices.Compact(rs)
}

// rerunOrWait waits while a check runs (a re-run of an unfinished run is
// refused). Else it asks one re-run per failing run with flake_reruns left
// for that run at H (a failed request counts), keyed by the run's red jobs,
// and waits while a done re-run still reads those jobs red, at most
// checks_grace_minutes from the request.
func rerunOrWait(in In, h string) (asks []string, wait bool) {
	if in.Facts.PR.Running || in.Facts.PR.ChecksUnread && in.Now.Sub(in.Facts.PR.HeadSince) < checksGrace(in) {
		return nil, true // H's runs unread: never a re-run; tell or wake after the grace
	}
	for _, run := range ciRuns(in) {
		var jobs []string
		for _, f := range in.Facts.PR.Failed {
			if cmp.Or(f.Run, "none") == run && f.Job != "" {
				jobs = append(jobs, f.Job)
			}
		}
		slices.Sort(jobs)
		k, spent := key(AskRerunCI, run, strings.Join(jobs, ","), h), 0
		for _, a := range in.acts() {
			if strings.HasPrefix(a.Key, key(AskRerunCI, run)+"@") && strings.HasSuffix(a.Key, "@"+h) {
				spent++
			}
		}
		if a := LastAct(in.acts(), k); a != nil {
			wait = wait || a.Result == "done" && in.Now.Sub(a.At) < checksGrace(in)
		} else if run != "none" && spent < in.Settings.FlakeReruns {
			asks = append(asks, k)
		}
	}
	return asks, wait
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
