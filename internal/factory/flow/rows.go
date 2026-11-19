package flow

import (
	"fmt"
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
	AskPushPR       = "push_and_open_pr"
	AskAccept       = "record_accept"
	AskMerge        = "merge"
	AskMarkDone     = "mark_done"
	AskCleanup      = "cleanup"
	AskWake         = "wake_coordinator"
	AskStopTask     = "stop_task"
)

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

func ended(in In) bool       { return in.Task.Done != nil || in.Task.Closed != nil }
func prMerged(in In) bool    { return in.Facts.PR.State == PRMerged }
func noAttempt(in In) bool   { return in.attempt() == nil }
func hasAttempt(in In) bool  { return in.attempt() != nil }
func workerGone(in In) bool  { return in.Facts.Worker.State == RunGone }
func reviewGone(in In) bool  { return in.Facts.Reviewer.State == RunGone }
func noReady(in In) bool     { return len(in.attempt().Ready) == 0 }
func tipAhead(in In) bool    { return in.Facts.TipRel == RelAhead } // slice 1: ahead is always changes
func noPR(in In) bool        { return in.Facts.PR.State == PRNone }
func prOpen(in In) bool      { return in.Facts.PR.State == PROpen }
func prNotClosed(in In) bool { return in.Facts.PR.State != PRClosed }
func tipSame(in In) bool     { return in.Facts.TipRel == RelSame } // slice 1: diverged falls to N43 (N7 comes later)
func prBehindH(in In) bool   { return in.Facts.PR.HeadRel == RelBehind }
func prHeadIsH(in In) bool   { return in.Facts.PR.Head == headH(in) }
func answered(in In) bool    { return in.Facts.PR.Answered }
func unanswered(in In) bool  { return !answered(in) }
func notBehind(in In) bool   { return !in.Facts.PR.Behind }
func checksGreen(in In) bool { return in.Facts.PR.Checks == ChecksGreen }
func checksPend(in In) bool  { return in.Facts.PR.Checks == ChecksPending }
func mergeClean(in In) bool  { return in.Facts.PR.Mergeable == MergeableClean }
func mergeUnkn(in In) bool   { return in.Facts.PR.Mergeable == MergeableUnknown }
func working(in In) bool     { return in.Facts.Worker.State == RunWorking }
func notWorking(in In) bool  { return !working(in) }
func mergeAsked(in In) bool  { return Count(in.acts(), key(AskMerge, headH(in))) > 0 }

// checksAbsentInGrace: no checks yet, and the head is younger than
// checks_grace_minutes (N36).
func checksAbsentInGrace(in In) bool {
	grace := time.Duration(in.Settings.ChecksGraceMinutes) * time.Minute
	return in.Facts.PR.Checks == ChecksNone && in.Now.Sub(in.Facts.PR.HeadSince) < grace
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
	{ID: "S1", WaitsOn: "the owner", When: all(hasAttempt, anyOf(workerGone, all(ladderSays(Review), reviewGone))), Then: func(in In) Decision {
		who := "worker"
		if !workerGone(in) {
			who = "reviewer"
		}
		return say("the "+who+" run ended: the task is stopped", once(in.acts(), AskStopTask))(in)
	}},
	{ID: "N11", WaitsOn: "the engine", When: noAttempt, Then: func(in In) Decision {
		return say("no attempt: start one", once(in.acts(), AskNewAttempt))(in)
	}},
	{ID: "N22", WaitsOn: "the worker", When: all(ladderSays(Fix), prNotClosed), Then: func(in In) Decision {
		r := newestCovering(*in.attempt(), headH(in))
		return say("the ladder says fix", ensureRun(in, in.Facts.Worker, AskStartWorker),
			once(in.acts(), key(AskTellFindings, r.N)))(in)
	}},
	{ID: "N23", WaitsOn: "the worker", When: all(hasAttempt, anyOf(noReady, tipAhead)), Then: func(in In) Decision {
		return say("the worker is working", ensureRun(in, in.Facts.Worker, AskStartWorker))(in)
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
	{ID: "N36", WaitsOn: "CI", When: all(ladderSays(Passed), prOpen, anyOf(checksPend, unanswered, checksAbsentInGrace, mergeUnkn)),
		Then: say("checks pending or unreadable, or mergeable unknown")},
	{ID: "N39", WaitsOn: "the worker", When: all(ladderSays(Passed), prOpen, working), Then: say("the worker is working")},
	{ID: "N41", WaitsOn: "GitHub", When: all(ladderSays(Passed), mergeAsked, prOpen, notBehind),
		Then: say("a merge was asked for H")},
	{ID: "N42", WaitsOn: "GitHub",
		When: all(ladderSays(Passed), prOpen, prHeadIsH, tipSame, answered, checksGreen, mergeClean, notBehind, notWorking),
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
