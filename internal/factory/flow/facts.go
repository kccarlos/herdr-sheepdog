package flow

import "time"

// Rel is how one commit stands to another (design.md 3.1).
type Rel string

// The relations of design.md 3.1.
const (
	RelSame     Rel = "same"
	RelAhead    Rel = "ahead"
	RelBehind   Rel = "behind"
	RelDiverged Rel = "diverged"
)

// PRState is the PR's state on GitHub; PRNone means there is no PR.
type PRState string

// The PR states of design.md 7.3.
const (
	PRNone   PRState = ""
	PROpen   PRState = "open"
	PRMerged PRState = "merged"
	PRClosed PRState = "closed"
)

// Checks is the PR checks rollup class (design.md 7.3).
type Checks string

// The checks rollup classes of design.md 7.3.
const (
	ChecksUnreadable      Checks = "unreadable"
	ChecksRed             Checks = "red"
	ChecksStoppedTimeout  Checks = "stopped_timeout"
	ChecksStoppedExternal Checks = "stopped_external"
	ChecksStoppedUnknown  Checks = "stopped_unknown" // cancelled, annotations unread: N34 treats it as a time limit, never re-runs
	ChecksPending         Checks = "pending"
	ChecksAbsent          Checks = "absent"
	ChecksGreen           Checks = "green"
)

// Mergeable is GitHub's answer on conflicts (design.md 7.3).
type Mergeable string

// The mergeable values of design.md 7.3.
const (
	MergeableClean    Mergeable = "clean"
	MergeableConflict Mergeable = "conflict"
	MergeableUnknown  Mergeable = "unknown"
)

// RunState is a run as herdr-core shows it; RunNone means none was started.
type RunState string

// The run states of slice 1 (plan.md section 3, runs row).
const (
	RunNone    RunState = ""
	RunIdle    RunState = "idle"
	RunWorking RunState = "working"
	RunGone    RunState = "gone"
)

// Verdicts a round can carry in slice 1.
const (
	VerdictPass    = "pass"
	VerdictChanges = "changes"
)

// PR is the latest PR snapshot (design.md 7.3). Answered is false when GitHub
// did not answer this pass; the rest is then the last snapshot it gave.
// No merge_queue field: spike S0.5 found no merge queue on the repo.
type PR struct {
	Answered       bool      `json:"answered"`
	Number         int       `json:"number"`
	State          PRState   `json:"state"`
	MergedCommit   string    `json:"merged_commit,omitempty"`
	Head           string    `json:"head"`
	HeadRel        Rel       `json:"head_rel"`   // Head against H
	HeadSince      time.Time `json:"head_since"` // when Head was first seen; the checks grace counts from it
	Mergeable      Mergeable `json:"mergeable"`
	Checks         Checks    `json:"checks"`
	Failed         []Failure `json:"failed,omitempty"`  // the red or outside-cancelled checks (N33, N34)
	Running        bool      `json:"running,omitempty"` // a check has not finished: no re-run or tell yet
	Behind         bool      `json:"behind"`
	BehindUnknown  bool      `json:"behind_unknown,omitempty"` // compare failed: Behind false is not "not behind" (N40)
	ChecksUnread   bool      `json:"checks_unread,omitempty"`  // workflow runs unread: absent is not "no checks", N35 never dispatches (N43)
	ReviewRequired bool      `json:"review_required,omitempty"`
}

// Run is one run's state, and whether a start for it is in flight.
type Run struct {
	State   RunState `json:"state"`
	Pending bool     `json:"pending"`
}

// Facts are one pass's observations of a task. Plain data, no I/O.
type Facts struct {
	Ask    string `json:"-"` // the ask an executor runs for (its key)
	PR     PR     `json:"pr"`
	Tip    string `json:"tip"`
	TipRel Rel    `json:"tip_rel"` // the worktree tip against H
	// TipMergeOnly: the tip is ahead of H by merges of the base only (N20).
	TipMergeOnly bool `json:"tip_merge_only,omitempty"`
	// Lines is the changed lines (added + removed) at the tip against the
	// merge base, read from git every pass (N12).
	Lines int `json:"lines"`
	// LinesTimeout: reading changed lines timed out; N12 counts it as over
	// size_cap (design.md 3, last_tip).
	LinesTimeout bool `json:"lines_timeout,omitempty"`
	Worker       Run  `json:"worker"`
	// Reviewer is the run for the round the ladder asks for at H, not an
	// earlier round's (that one was stopped when its verdict came in).
	Reviewer Run `json:"reviewer"`
	// N9/N10 scheduling facts (design.md 4.2, 4.10).
	DepsNotDone []string `json:"deps_not_done,omitempty"`
	DepsDropped []string `json:"deps_dropped,omitempty"`
	NoSlot      bool     `json:"no_slot,omitempty"`
	// GitHubUnavailable is the project fact raised when GitHub cannot be read
	// (design.md 7.3); it holds merges (N42) and never marks the repo non-GitHub.
	GitHubUnavailable bool `json:"github_unavailable,omitempty"`
}

// In is what a row sees: the task's records, the settings, the facts and
// the time (design.md 2.1).
type In struct {
	Task     Task
	Settings Settings
	Facts    Facts
	Now      time.Time
}

// attempt is the task's current attempt, or nil before the first.
func (in In) attempt() *Attempt { return OpenAttempt(in.Task) }

// acts is the current attempt's ledger (or the task's before the first attempt).
func (in In) acts() []Act {
	if a := in.attempt(); a != nil {
		return a.Acts
	}
	return in.Task.Acts
}

// H is the attempt's head (design.md 3.1): the base commit, then the
// commit of the latest ready report, then each merge_only move from it.
func H(a Attempt) string {
	h := a.BaseCommit
	if n := len(a.Ready); n > 0 {
		h = a.Ready[n-1].Head
	}
	for _, m := range a.MergeOnly {
		if m.From == h {
			h = m.To
		}
	}
	return h
}

// Covers reports whether a record made at x still counts for h (design.md
// 3.1): x is h, or h was reached from x by merge_only moves only. Every
// record is made at some H, so the attempt's moves are the whole relation.
func Covers(a Attempt, x, h string) bool {
	for i := len(a.MergeOnly) - 1; i >= 0 && x != h; i-- {
		if a.MergeOnly[i].To == h {
			h = a.MergeOnly[i].From
		}
	}
	return x == h
}

// newestCovering is the newest round that covers h, or nil.
func newestCovering(a Attempt, h string) *Round {
	rs := Counted(a)
	for i := len(rs) - 1; i >= 0; i-- {
		if Covers(a, rs[i].Head, h) {
			return &rs[i]
		}
	}
	return nil
}

func isReviewerRound(r Round) bool { return r.Kind == RoundFull || r.Kind == RoundDelta }

// LastReviewed is the newest reviewer round after the boundary, or nil: a
// send-back reviewed nothing, so a delta never starts from it.
func LastReviewed(a Attempt) *Round {
	rs := Counted(a)
	for i := len(rs) - 1; i >= 0; i-- {
		if isReviewerRound(rs[i]) {
			return &rs[i]
		}
	}
	return nil
}

// RejectableRound returns the newest round covering h when it is an unrejected
// reviewer changes round, or nil otherwise (design.md 7.2).
func RejectableRound(a Attempt, h string) *Round {
	r := newestCovering(a, h)
	if r == nil || !isReviewerRound(*r) || r.Verdict != VerdictChanges || rejectFor(a, h, r.N) != nil {
		return nil
	}
	return r
}

// RejectedRound returns the newest round covering h and its reject when that
// round is a rejected reviewer changes round (design.md 4.6, L10).
func RejectedRound(a Attempt, h string) (*Round, *Reject) {
	r := newestCovering(a, h)
	if r == nil || !isReviewerRound(*r) || r.Verdict != VerdictChanges {
		return nil, nil
	}
	if rej := rejectFor(a, h, r.N); rej != nil {
		return r, rej
	}
	return nil, nil
}

func rejectFor(a Attempt, h string, round int) *Reject {
	for i := len(a.Rejects) - 1; i >= 0; i-- {
		if Covers(a, a.Rejects[i].Head, h) && a.Rejects[i].Round == round {
			return &a.Rejects[i]
		}
	}
	return nil
}

// RoundKind is delta from the last round's commit when h is ahead of it and
// not covered by it, and full otherwise (design.md 4.6, 9.4).
// A rejected delta round stays at the same head, so it re-reviews as full (a superset for a fresh reviewer).
func RoundKind(a Attempt, h string, sinceRel Rel) (kind, since string) {
	if r := LastReviewed(a); r != nil && sinceRel == RelAhead && !Covers(a, r.Head, h) {
		return RoundDelta, r.Head
	}
	return RoundFull, ""
}
