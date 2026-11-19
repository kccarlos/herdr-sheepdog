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

// Checks is slice 1's checks rollup (plan.md section 3, git and gh row).
type Checks string

// The four simple classes.
const (
	ChecksNone    Checks = "none"
	ChecksPending Checks = "pending"
	ChecksRed     Checks = "red"
	ChecksGreen   Checks = "green"
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

// PR is the latest PR snapshot. Answered is false when GitHub did not
// answer this pass; the rest is then the last snapshot it gave.
type PR struct {
	Answered     bool      `json:"answered"`
	Number       int       `json:"number"`
	State        PRState   `json:"state"`
	MergedCommit string    `json:"merged_commit,omitempty"`
	Head         string    `json:"head"`
	HeadRel      Rel       `json:"head_rel"`   // Head against H
	HeadSince    time.Time `json:"head_since"` // when Head was first seen; the checks grace counts from it
	Mergeable    Mergeable `json:"mergeable"`
	Checks       Checks    `json:"checks"`
	Behind       bool      `json:"behind"`
}

// Run is one run's state, and whether a start for it is in flight.
type Run struct {
	State   RunState `json:"state"`
	Pending bool     `json:"pending"`
}

// Facts are one pass's observations of a task. Plain data, no I/O.
type Facts struct {
	PR     PR     `json:"pr"`
	Tip    string `json:"tip"`
	TipRel Rel    `json:"tip_rel"` // the worktree tip against H
	Worker Run    `json:"worker"`
	// Reviewer is the run for the round the ladder asks for at H, not an
	// earlier round's (that one was stopped when its verdict came in).
	Reviewer Run `json:"reviewer"`
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
func (in In) attempt() *Attempt {
	if len(in.Task.Attempts) == 0 {
		return nil
	}
	return &in.Task.Attempts[len(in.Task.Attempts)-1]
}

// acts is the current attempt's ledger.
func (in In) acts() []Act {
	if a := in.attempt(); a != nil {
		return a.Acts
	}
	return nil
}

// H is the attempt's head (design.md 3.1): the base commit, then the
// commit of the latest ready report. Slice 1 has no engine branch update
// and no merge-only advance.
func H(a Attempt) string {
	if n := len(a.Ready); n > 0 {
		return a.Ready[n-1].Head
	}
	return a.BaseCommit
}

// Covers reports whether a record made at x still counts for h (design.md
// 3.1). Slice 1 has no merge_only deltas, so only the same commit covers.
func Covers(x, h string) bool { return x == h }

// newestCovering is the newest round that covers h, or nil.
func newestCovering(a Attempt, h string) *Round {
	for i := len(a.Rounds) - 1; i >= 0; i-- {
		if Covers(a.Rounds[i].Head, h) {
			return &a.Rounds[i]
		}
	}
	return nil
}
