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
	AskNudgeReview  = "nudge_reviewer"
	AskStopReview   = "stop_reviewer"
	AskTellFindings = "tell_worker_findings"
	AskOpenRecap    = "open_recap"
	AskSizeRecap    = "open_size_recap"
	AskCIRecap      = "open_ci_recap"
	AskTimeRecap    = "open_time_recap"
	AskTellStop     = "tell_worker_stop"
	AskTellDiverged = "tell_worker_diverged"
	AskTellConflict = "tell_worker_conflict"
	AskMergeOnly    = "move_h_merge_only"
	AskPushPR       = "push_and_open_pr"
	AskRerunCI      = "rerun_ci"
	AskTellCI       = "tell_worker_ci"
	AskTellBaseRed  = "tell_worker_base_red"
	AskDispatchCI   = "dispatch_ci"
	AskUpdateBranch = "update_branch"
	AskFailUpdate   = "fail_update_branch"
	AskAccept       = "record_accept"
	AskMerge        = "merge"
	AskMarkDone     = "mark_done"
	AskCleanup      = "cleanup"
	AskWake         = "wake_coordinator"
	AskStopRuns     = "stop_runs"

	countBase        = 1 // 1-based restart, retry and back-off shift index
	maxBackoffShift  = 10
	maxEnsureBackoff = 30 * time.Minute
)

// key joins a verb and what it is for: "merge@3f9c".
func key(verb string, parts ...any) string {
	s := []string{verb}
	for _, p := range parts {
		s = append(s, fmt.Sprint(p))
	}
	return strings.Join(s, "@")
}

// once asks for k unless acts already hold a done entry for it.
func once(acts []Act, k string) []string {
	if k == "" || Done(acts, k) {
		return nil
	}
	return []string{k}
}

func ensureStep(in In, k string) (due, exhausted bool) {
	last, _, tries, counted, settled := actFailures(in.acts(), k)
	switch base := time.Duration(in.Settings.EnsureBackoffMinutes) * time.Minute; {
	case settled || counted > 0 && counted >= in.Settings.EnsureFailures:
		return false, true
	case last == nil || last.Result == "done":
		return true, false
	case last.Result != "failed" || last.At.IsZero():
		return false, false
	default:
		return in.Now.Sub(last.At) >= min(base<<min(max(0, tries-countBase), maxBackoffShift), max(base, maxEnsureBackoff)), false
	}
}

// ensure asks for k by key and retries failed tries with doubling back-off
// until settled or ensure_failures in a row (design.md section 8).
func ensure(in In, k string) []string {
	if k == "" || Done(doneActs(in.acts(), k), k) {
		return nil
	}
	due, exhausted := ensureStep(in, k)
	if in.probe && exhausted || !in.probe && due {
		return []string{k}
	}
	return nil
}

// EnsureRun is design.md 4.8's first-match table for a role's run (worker or
// reviewer). A live run needs nothing; otherwise how the last run of that
// role, round and commit ended decides the next ask.
func EnsureRun(in In, role string) []string {
	a := in.attempt()
	if a == nil {
		return nil
	}
	r, round, h, last, tries := in.Facts.Worker, nextRound(*a), headH(in), a.Run, 0
	if role == RoleReviewer {
		r, last = in.Facts.Reviewer, nil
		if a.Review != nil && a.Review.Round == round && Covers(*a, a.Review.Head, h) {
			last, tries = a.Review.Run, a.Review.Retries
		}
	}
	// A stopping alert (section 6) is slice 5b.
	if r.State == RunWorking || r.State == RunIdle || r.Pending {
		return nil
	}
	inFlight := last != nil && last.State == PhaseStarting && last.Address == ""
	if last != nil && last.State != PhaseEnded && !inFlight {
		last = nil
	}
	if last == nil && r.State == RunGone {
		last = &RunRecord{State: PhaseEnded, Result: EndExited}
	}
	firstKey := AskStartWorker
	if role == RoleReviewer {
		firstKey = key(AskStartReview, round, h)
	}
	switch {
	case (last == nil || inFlight) && !Done(a.Acts, firstKey):
		if role != RoleReviewer && in.Facts.NoSlot {
			return nil
		}
		return ensure(in, firstKey)
	case inFlight || last != nil && last.Result == EndStopped:
		if role == RoleReviewer {
			return []string{key(AskStartReview, round, ContinueResume, h)}
		} else if in.Facts.NoSlot {
			return nil
		}
		return []string{workerStartAsk(in, last, 0)}
	case last == nil:
		return nil
	case last.Result == EndQuota || last.Result == EndSignedOut:
		// Quota and signed-out rows are slice 5b.
	case role == RoleReviewer && tries < in.Settings.ReviewerRetries:
		return []string{key(AskStartReview, round, tries+countBase, h)}
	case role == RoleWorker && a.Restarts < in.Settings.WorkerRestarts:
		if !last.EndedAt.IsZero() && in.Now.Sub(last.EndedAt) < time.Duration(in.Settings.EnsureBackoffMinutes)*time.Minute<<a.Restarts || in.Facts.NoSlot {
			return nil
		}
		return []string{workerStartAsk(in, last, a.Restarts+countBase)}
	}
	// Otherwise it counts as a failure; N5 with retry is PR 5a.3.
	if role == RoleReviewer {
		return once(SinceRetry(a.Acts), key(AskWake, "reviewer_failed", round, h))
	}
	return once(SinceRetry(a.Acts), key(AskWake, "worker_failed"))
}

func workerStartAsk(in In, last *RunRecord, n int) string {
	mode := ContinueHandoff
	if res, _ := Resolve(RoleWorker, in.Task, in.Settings, UnprobedFacts); (last.Resumable || last.State == PhaseStarting && last.Resumed) && last.SessionID != "" && last.CLI == cmp.Or(res.CLI, "claude") {
		mode = ContinueResume
	}
	if n == 0 {
		return key(AskStartWorker, mode)
	}
	return key(AskStartWorker, mode, n)
}

// WantsWorkerSlot reports whether an open attempt's ended worker run is ready
// to ask for a start if granted a free slot (design.md 4.8, N10).
func WantsWorkerSlot(t Task, s Settings, now time.Time) bool {
	a := OpenAttempt(t)
	if Ended(t) || t.Parked || OpenRecap(t) != nil || a == nil || a.NothingToChange != "" ||
		a.Run == nil || a.Run.State != PhaseEnded || a.Review != nil && a.Review.RunID != "" {
		return false
	}
	asks := EnsureRun(In{Task: t, Settings: s, Facts: Facts{Worker: Run{State: RunGone}}, Now: now}, RoleWorker)
	return len(asks) > 0 && strings.HasPrefix(asks[0], AskStartWorker)
}

// WantsNewAttempt reports whether a task with no open attempt is ready to ask
// for AskNewAttempt if granted a free slot (design.md 4.2, 8).
func WantsNewAttempt(t Task, s Settings, now time.Time) bool {
	return len(ensure(In{Task: t, Settings: s, Now: now}, AskNewAttempt)) > 0
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
func noReady(in In) bool     { return len(in.attempt().Ready) == 0 }
func tipAhead(in In) bool    { return in.Facts.TipRel == RelAhead } // merge_only is caught by N20 above
func noPR(in In) bool        { return in.Facts.PR.State == PRNone }
func prOpen(in In) bool      { return in.Facts.PR.State == PROpen }
func prClosed(in In) bool    { return in.Facts.PR.State == PRClosed }
func tipAtOrBehindH(in In) bool {
	a := in.attempt()
	return in.Facts.TipRel == RelSame || a != nil && in.Facts.TipRel == RelBehind && Covers(*a, in.Facts.Tip, H(*a))
}
func tipDivergedOrUncovered(in In) bool {
	a := in.attempt()
	return a != nil && (in.Facts.TipRel == RelDiverged || in.Facts.TipRel == RelBehind && !Covers(*a, in.Facts.Tip, headH(in)))
}

// PRHeadDivergedOrForeign reports whether an open, answered PR head diverged
// from H or has non-merge_only commits ahead of H.
func PRHeadDivergedOrForeign(in In) bool {
	return prOpen(in) && answered(in) && (in.Facts.PR.HeadRel == RelDiverged || in.Facts.PR.HeadRel == RelAhead && !in.Facts.PR.HeadMergeOnly && !in.Facts.PR.HeadMergeOnlyUnknown)
}
func prHeadMergeOnlyUnknown(in In) bool {
	return prOpen(in) && answered(in) && in.Facts.PR.HeadRel == RelAhead && in.Facts.PR.HeadMergeOnlyUnknown
}
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
func baseRed(in In) bool              { return in.Facts.BaseRed }
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
func mergeAsked(in In) bool           { return Done(SinceRetry(in.acts()), key(AskMerge, headH(in))) }

func checksGrace(in In) time.Duration {
	return time.Duration(in.Settings.ChecksGraceMinutes) * time.Minute
}
func ciStalled(in In) time.Duration {
	return time.Duration(in.Settings.CIStalledMinutes) * time.Minute
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

var rowsBelowN5 []Row[In]

func init() {
	i := slices.IndexFunc(TaskRows, func(r Row[In]) bool { return r.ID == "N5" })
	rowsBelowN5 = TaskRows[i+countBase:]
}

func n5Key(in In) string {
	in.probe = true
	if r, d := FirstMatch(rowsBelowN5, in); r.ID != "N43" {
		for _, k := range d.Asked {
			if !strings.HasPrefix(k, AskWake+"@") && Exhausted(in.acts(), k, in.Settings) {
				return k
			}
		}
	}
	return ""
}

func n5Wake(acts []Act, fk string) []string {
	if r := Count(acts, AskRetry); r > 0 {
		return once(acts, key(AskWake, "failed", r, fk))
	}
	return once(acts, key(AskWake, "failed", fk))
}

func n5Why(taskID string, acts []Act, fk string) string {
	_, cause, _, _, _ := actFailures(acts, fk)
	cls, words := ClassTransient, ""
	if cause != nil {
		cls, words = cmp.Or(cause.Class, ClassTransient), CleanErr(cause.Err)
	}
	cmd := fmt.Sprintf("task retry %s --why \"<why>\"", taskID)
	if words != "" {
		return fmt.Sprintf("%s failed (%s): %s; run `sheepdog factory %s`", fk, cls, words, cmd)
	}
	return fmt.Sprintf("%s failed (%s); run `sheepdog factory %s`", fk, cls, cmd)
}

// TaskRows is the task table in design.md section 4's order, using EnsureRun
// (4.8) wherever a row needs a worker or reviewer run.
var TaskRows = []Row[In]{
	{ID: "N1", WaitsOn: "nobody", Stage: "done", When: ended, Then: func(in In) Decision {
		asked := ensure(in, AskCleanup)
		if Exhausted(in.acts(), AskCleanup, in.Settings) {
			asked = append(asked, n5Wake(in.acts(), AskCleanup)...)
		}
		return say("ended: stop its runs and clean up", asked)(in)
	}},
	{ID: "N2", WaitsOn: "nobody", Stage: "done", When: prMerged, Then: func(in In) Decision {
		asked := ensure(in, AskMarkDone)
		if !hasAccept(in, in.Facts.PR.Head) {
			asked = append(asked, once(in.acts(), key(AskWake, "merged_outside_bar"))...)
		}
		return Decision{Asked: asked, Why: "the PR is merged"}
	}},
	// N3 asks while a live run is recorded, not once: the stop ends the run
	// records as stopped, and a failed stop backs off and wakes once per section 8.
	{ID: "N3", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: func(in In) bool { return in.Task.Parked }, Then: func(in In) Decision {
		if a := in.attempt(); a != nil && (a.RunID != "" || a.Run != nil && a.Run.State != PhaseEnded ||
			a.Review != nil && (a.Review.RunID != "" || a.Review.Run != nil && a.Review.Run.State != PhaseEnded)) {
			if due, exhausted := ensureStep(in, AskStopRuns); due {
				return say("parked: stop its runs", []string{AskStopRuns})(in)
			} else if exhausted {
				return say(n5Why(in.Task.ID, in.acts(), AskStopRuns), n5Wake(in.acts(), AskStopRuns))(in)
			}
		}
		return say("parked")(in)
	}},
	{ID: "N4", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: prClosed, Then: func(in In) Decision {
		return say(fmt.Sprintf("the PR was closed without merging (never reopened; run `sheepdog factory task retry %s --why \"<why>\"` to start a new attempt)", in.Task.ID), once(in.acts(), key(AskWake, "pr_closed")))(in)
	}},
	{ID: "N5", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: func(in In) bool { return n5Key(in) != "" }, Then: func(in In) Decision {
		fk := n5Key(in)
		return say(n5Why(in.Task.ID, in.acts(), fk), n5Wake(in.acts(), fk))(in)
	}},
	// N6 tells a live worker of a size, ci or time recap once to stop (it has no
	// recap-written report yet, so nothing waits for one).
	{ID: "N6", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: recapOpen, Then: func(in In) Decision {
		n, tell, trig := len(in.Task.Recaps), []string(nil), OpenRecap(in.Task).Trigger
		if (trig == TriggerSize || trig == TriggerCI || trig == TriggerTime) && (in.Facts.Worker.State == RunWorking || in.Facts.Worker.State == RunIdle) {
			tell = ensure(in, key(AskTellStop, n))
		}
		return say("a recap is open", once(in.acts(), key(AskWake, "recap", n)), tell)(in)
	}},
	{ID: "N7", WaitsOn: "the worker; then the coordinator", Stage: "blocked_or_failed", When: all(hasAttempt, anyOf(tipDivergedOrUncovered, PRHeadDivergedOrForeign, prHeadMergeOnlyUnknown)), Then: func(in In) Decision {
		h, acts := headH(in), SinceRetry(in.acts())
		if !tipDivergedOrUncovered(in) && !PRHeadDivergedOrForeign(in) {
			if in.Now.Sub(in.Facts.PR.HeadSince) >= checksGrace(in) {
				return say("cannot tell if PR head is merge_only past checks_grace_minutes", once(acts, key(AskWake, "merge_only_unknown", h)))(in)
			}
			return say("cannot tell if PR head is merge_only: wait")(in)
		}
		kind := n7Kind(in)
		tk := key(AskTellDiverged, kind, h)
		tell := LastAct(acts, tk)
		if tell == nil || tell.Result != "done" {
			return say(N7Why(in, false), EnsureRun(in, RoleWorker), ensure(in, tk))(in)
		}
		if working(in) || (!in.Facts.Worker.LastTurnEnd.After(tell.At) && in.Now.Sub(tell.At) < checksGrace(in)) {
			return say(N7Why(in, false), EnsureRun(in, RoleWorker))(in)
		}
		return say(N7Why(in, true), once(acts, key(AskWake, "diverged", kind, h)))(in)
	}},
	{ID: "N8", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: func(in In) bool { return hasAttempt(in) && in.attempt().NothingToChange != "" }, Then: func(in In) Decision {
		a := in.attempt()
		return say("the worker says nothing to change: "+a.NothingToChange, once(in.acts(), key(AskWake, "nothing_to_change", len(a.Rounds))))(in)
	}},
	{ID: "N9", WaitsOn: "dependencies", Stage: "waiting_on_dep", When: all(noAttempt, anyOf(depsNotDone, depsDropped)), Then: func(in In) Decision {
		var asked []string
		for _, dep := range in.Facts.DepsDropped {
			asked = append(asked, once(in.acts(), key(AskWake, "dep_dropped", dep))...)
		}
		return say(n9Why(in), asked)(in)
	}},
	// N10: milestone and quota (no worker candidate resolves) come in later slices (design.md 4.2).
	{ID: "N10", WaitsOn: "a slot", Stage: "queued", When: all(noAttempt, noSlot), Then: say("no slot is free")},
	{ID: "N11", WaitsOn: "the engine", Stage: "queued", When: noAttempt, Then: func(in In) Decision {
		return say("no attempt: start one", ensure(in, AskNewAttempt))(in)
	}},
	// N12 has no "no owner override covers H" clause until slice 7.
	{ID: "N12", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: func(in In) bool {
		return hasAttempt(in) && (in.Facts.LinesTimeout || in.Facts.Lines > in.attempt().SizeCap)
	},
		Then: func(in In) Decision {
			why := fmt.Sprintf("%d changed lines, over the size cap %d", in.Facts.Lines, in.attempt().SizeCap)
			if in.Facts.LinesTimeout {
				why = fmt.Sprintf("changed-lines read timed out (counts as over the size cap %d)", in.attempt().SizeCap)
			}
			return say(why, ensure(in, key(AskSizeRecap, len(in.Task.Recaps))))(in)
		}},
	// N13 has no "no owner override covers H" clause until slice 7.
	{ID: "N13", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: func(in In) bool {
		return hasAttempt(in) && !ladderSays(Passed)(in) && in.Settings.CIFailsPerHour > 0 && ciFailCount(in) >= in.Settings.CIFailsPerHour
	},
		Then: func(in In) Decision {
			why := fmt.Sprintf("%d commits failed CI in the last hour (limit %d)", ciFailCount(in), in.Settings.CIFailsPerHour)
			return say(why, ensure(in, key(AskCIRecap, len(in.Task.Recaps))))(in)
		}},
	// N14 has no "no owner override covers H" clause until slice 7.
	{ID: "N14", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: func(in In) bool {
		return hasAttempt(in) && !ladderSays(Passed)(in) && WorkingTime(in.Task) >= time.Duration(in.Settings.BuildHours)*time.Hour
	},
		Then: func(in In) Decision {
			why := fmt.Sprintf("worker spent %s working (limit %dh)", WorkingTime(in.Task).Truncate(time.Minute), in.Settings.BuildHours)
			return say(why, ensure(in, key(AskTimeRecap, len(in.Task.Recaps))))(in)
		}},
	{ID: "N20", WaitsOn: "the engine", Stage: "working", When: func(in In) bool {
		return hasAttempt(in) && ((tipAhead(in) && in.Facts.TipMergeOnly) || prHeadAheadMergeOnly(in))
	},
		Then: func(in In) Decision {
			if tipAhead(in) && in.Facts.TipMergeOnly {
				return say("the tip is ahead of H by merges of the base only: H moves to it, no round", ensure(in, key(AskMergeOnly, in.Facts.Tip)))(in)
			}
			return say("the PR head is ahead of H by merges of the base only: H moves to it, no round", ensure(in, key(AskMergeOnly, in.Facts.PR.Head)))(in)
		}},
	{ID: "N22", WaitsOn: "the worker", Stage: "working", When: ladderSays(Fix), Then: func(in In) Decision {
		r := newestCovering(*in.attempt(), headH(in))
		return say("the ladder says fix", EnsureRun(in, RoleWorker),
			ensure(in, key(AskTellFindings, r.N)))(in)
	}},
	{ID: "N23", WaitsOn: "the worker", Stage: "working", When: all(hasAttempt, anyOf(noReady, tipAhead)), Then: func(in In) Decision {
		return say("the worker is working", EnsureRun(in, RoleWorker))(in)
	}},
	{ID: "N25", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: ladderSays(RecapAnswer), Then: func(in In) Decision {
		return say("the ladder says recap", ensure(in, key(AskOpenRecap, "review")))(in)
	}},
	{ID: "N26", WaitsOn: "the engine", Stage: "working", When: all(hasAttempt, anyOf(noPR, all(prOpen, prBehindH))), Then: func(in In) Decision {
		if unanswered(in) && !in.probe { // "no PR" from a failed read is a guess: wait, so push is not spent blindly
			return say("GitHub did not answer: wait for a snapshot")(in)
		}
		if in.Facts.PR.OtherBase != "" {
			return say(fmt.Sprintf("PR #%d targets base %q instead of the project base: waiting (run `sheepdog factory task retry %s --why \"<why>\"` to start a new attempt)",
				in.Facts.PR.Number, in.Facts.PR.OtherBase, in.Task.ID), once(SinceRetry(in.acts()), key(AskWake, "pr_other_base", in.Facts.PR.Number, in.Facts.PR.OtherBase)))(in)
		}
		return say("GitHub does not have H", ensure(in, key(AskPushPR, headH(in))))(in)
	}},
	{ID: "N29", WaitsOn: "the reviewer", Stage: "working", When: ladderSays(Review), Then: func(in In) Decision {
		return say("the ladder says review", n29Asks(in))(in)
	}},
	{ID: "N32", WaitsOn: "the worker", Stage: "working", When: ciReady(mergeConflict), Then: func(in In) Decision {
		return say("PR conflicts with the base: tell the worker once to merge the base (never rebase)",
			EnsureRun(in, RoleWorker), ensure(in, key(AskTellConflict, headH(in))))(in)
	}},
	{ID: "N33", WaitsOn: "CI; then the worker", Stage: "ci", When: ciReady(checksRed), Then: func(in In) Decision {
		h := headH(in)
		if asks, wait := rerunOrWait(in, h); len(asks) > 0 || wait {
			return say("checks red: re-run failed jobs, or wait for the re-run or for checks still running", asks)(in)
		}
		var tells []string
		for _, run := range ciRuns(in) {
			tells = append(tells, ensure(in, key(AskTellCI, run, h))...)
		}
		return say("checks red at H: tell the worker once per failing run", EnsureRun(in, RoleWorker), tells)(in)
	}},
	{ID: "N34", WaitsOn: "CI; then the coordinator", Stage: "ci", When: ciReady(anyOf(checksStoppedExt, checksStoppedTimeout, checksStoppedUnknown)), Then: func(in In) Decision {
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
	{ID: "N35", WaitsOn: "CI; then the coordinator", Stage: "ci", When: ciReady(all(checksAbsentPastGrace, checksSeenNone)), Then: func(in In) Decision {
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
	{ID: "N38", WaitsOn: "the base branch", Stage: "ci", When: ciReady(all(baseRed, anyOf(prBehindBase, prBehindUnknown))), Then: func(in In) Decision {
		if in.Facts.BaseRedSpell == "" || in.Now.Sub(in.Facts.BaseRedSince) < time.Duration(in.Settings.MainRedMinutes)*time.Minute {
			return say("the base branch is red: holding branch updates and merges")(in)
		}
		spell := in.Facts.BaseRedSpell
		var wake []string
		if !in.Facts.BaseRedWoken {
			wake = once(in.acts(), key(AskWake, "base_red_held", spell))
		}
		return say("the base branch has been red past main_red_minutes: tell the oldest held task's worker to merge the base and say ready (or land a fix PR directly)",
			EnsureRun(in, RoleWorker), ensure(in, key(AskTellBaseRed, spell)), wake)(in)
	}},
	{ID: "N36", WaitsOn: "CI", Stage: "ci", Why: WhyCI, When: all(ladderSays(Passed), prOpen, anyOf(checksPend, checksUnreadable, unanswered, githubUnavail, checksAbsentInGrace, mergeUnkn, prBehindUnknown)),
		Then: func(in In) Decision {
			// Stands in for the ci_stalled alert until slice 5b builds the alerts table.
			if checksPend(in) && prHeadIsH(in) && answered(in) && githubAvail(in) && !in.Facts.PR.HeadSince.IsZero() && in.Now.Sub(in.Facts.PR.HeadSince) >= ciStalled(in) {
				return say("checks pending past ci_stalled_minutes", once(in.acts(), key(AskWake, "ci_stalled", headH(in))))(in)
			}
			if prBehindUnknown(in) && !anyOf(checksPend, checksUnreadable, unanswered, githubUnavail, checksAbsentInGrace, mergeUnkn)(in) && in.Now.Sub(in.Facts.PR.HeadSince) >= checksGrace(in) {
				return say("cannot tell if PR is behind the base past checks_grace_minutes", once(in.acts(), key(AskWake, "behind_unknown", headH(in))))(in)
			}
			return say("checks pending or unreadable, or mergeable/behind unknown")(in)
		}},
	{ID: "N37", WaitsOn: "a GitHub approval", Stage: "ci", When: ciReady(reviewRequired), Then: func(in In) Decision {
		return say("GitHub requires an approving review (branch protection / required approving reviews); nobody in the factory can approve",
			once(in.acts(), key(AskWake, "review_required", headH(in))))(in)
	}},
	{ID: "N39", WaitsOn: "the worker", Stage: "working", When: all(ladderSays(Passed), prOpen, working), Then: say("the worker is working")},
	{ID: "N40", WaitsOn: "GitHub", Stage: "ci", Why: WhyCI, When: ciReady(prBehindBase), Then: func(in In) Decision {
		h := headH(in)
		uk := key(AskUpdateBranch, h)
		act := LastAct(SinceRetry(in.acts()), uk)
		if act == nil || act.Result == "failed" {
			return say("PR is behind the base: update branch with a merge commit", ensure(in, uk))(in)
		}
		if in.Now.Sub(act.At) < checksGrace(in) {
			return say("branch update asked; waiting for the new head")(in)
		}
		return say("branch update did not move the PR head within checks_grace_minutes: failed try", []string{key(AskFailUpdate, h)})(in)
	}},
	{ID: "N41", WaitsOn: "GitHub", Stage: "ci", Why: WhyCI, When: all(ladderSays(Passed), mergeAsked, prOpen, notBehind),
		Then: say("a merge was asked for H")},
	{ID: "N42", WaitsOn: "GitHub", Stage: "ci", Why: WhyCI,
		When: all(ladderSays(Passed), prOpen, prHeadIsH, tipAtOrBehindH, answered, githubAvail, checksGreen, mergeClean, noReviewRequired, notBehind, notWorking),
		Then: func(in In) Decision {
			h := headH(in)
			return say("passed, green and mergeable at H", ensure(in, key(AskAccept, h)), ensure(in, key(AskMerge, h)))(in)
		}},
	{ID: "N43", WaitsOn: "the coordinator", Stage: "blocked_or_failed", Then: func(in In) Decision {
		// Once per stall: H and the facts that left it unmatched.
		p := in.Facts.PR
		return say("no rule matched", once(in.acts(), key(AskWake, "no_rule_matched", headH(in), p.State, p.Checks, p.Mergeable, in.Facts.TipRel)))(in)
	}},
}

func n7Kind(in In) string {
	if PRHeadDivergedOrForeign(in) {
		if in.Facts.PR.HeadRel == RelDiverged {
			return "pr_diverged"
		}
		return "pr_foreign"
	}
	return "tip"
}

// N7Why describes the N7 stall for in, shared by the row and executor so they
// cannot drift.
func N7Why(in In, wake bool) string {
	h, branch := headH(in), ""
	if a := in.attempt(); a != nil {
		branch = a.Branch
	}
	var msg string
	switch {
	case PRHeadDivergedOrForeign(in):
		msg = fmt.Sprintf("PR #%d head %s diverged from H %s or has changes the engine did not push — restore branch %s (never force-push)", in.Facts.PR.Number, in.Facts.PR.Head, h, branch)
	case in.Facts.TipRel == RelBehind:
		msg = fmt.Sprintf("worktree tip %s is behind H %s — pull (`git pull --ff-only origin %s`)", in.Facts.Tip, h, branch)
	case in.Facts.TipDivergedByMergeOnly:
		msg = fmt.Sprintf("worktree tip %s diverged from H %s after an engine branch update — pull (`git fetch origin && git merge --no-edit origin/%s`, never rebase)", in.Facts.Tip, h, branch)
	default:
		msg = fmt.Sprintf("worktree tip %s diverged from H %s — restore branch %s (never rebase or force-push)", in.Facts.Tip, h, branch)
	}
	if wake {
		return fmt.Sprintf("%s and still holds after the worker's turn: run `sheepdog factory task say %s --why \"<message>\"` then `sheepdog factory task retry %s --why \"<why>\"`", msg, in.Task.ID, in.Task.ID)
	}
	return msg
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

// WorkingTime returns how long the open attempt's worker run has spent in
// PhaseWorking since the last recap decision (design.md 4.3, N14).
func WorkingTime(t Task) time.Duration {
	if a := OpenAttempt(t); a != nil {
		return a.Working
	}
	return 0
}

// n29Asks enforces N29's reviewer limits: stop a run whose accumulated live
// age reached review_minutes; an idle reviewer with no report gets one nudge,
// then is stopped so 4.8 retries (background-job hold awaits herdr-core).
func n29Asks(in In) []string {
	r, a, h := in.Facts.Reviewer, in.attempt(), headH(in)
	if r.State != RunWorking && r.State != RunIdle {
		if (unanswered(in) || in.Facts.PR.HeadRel == "") && !in.probe { // PR state or head relation unknown: hold until known
			return nil
		}
		return EnsureRun(in, RoleReviewer)
	}
	round, tries, age := nextRound(*a), 0, time.Duration(0)
	if a.Review != nil && a.Review.Round == round && Covers(*a, a.Review.Head, h) {
		tries, age = a.Review.Retries, a.Review.Age
	}
	stopKey := key(AskStopReview, round, tries, h)
	if age >= time.Duration(in.Settings.ReviewMinutes)*time.Minute {
		return ensure(in, stopKey)
	}
	if r.State == RunIdle && !r.Refused {
		nudgeKey := key(AskNudgeReview, round, tries, h)
		if !Done(SinceRetry(in.acts()), nudgeKey) {
			return ensure(in, nudgeKey)
		}
		return ensure(in, stopKey)
	}
	return nil
}

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
