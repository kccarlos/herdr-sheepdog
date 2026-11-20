// Package flow holds the engine's records, settings and rule tables. It is
// pure: no I/O and no clock; the time comes in as an argument
// (docs/dev/engine-v2/design.md sections 2.1 and 13).
package flow

import (
	"encoding/json"
	"time"
)

// Roles of Slice 1 (design.md 9.1).
const (
	RoleCoordinator = "coordinator"
	RoleWorker      = "worker"
	RoleReviewer    = "reviewer"
)

// Round kinds of slice 2 (design.md 3, 4.6).
const (
	RoundFull     = "full"
	RoundDelta    = "delta"
	RoundSendBack = "send_back" // the coordinator's "changes", with a why (design.md 4.9)
)

// State is one project's state.json. Seq counts the inputs committed to it.
type State struct {
	Seq      int64    `json:"seq"`
	Project  Project  `json:"project"`
	Settings Settings `json:"settings"`
	Tasks    []Task   `json:"tasks"`
}

// Project is the project record of the skeleton.
type Project struct {
	ID           string `json:"id"`
	Folder       string `json:"folder,omitempty"`
	Goal         string `json:"goal,omitempty"`
	Started      bool   `json:"started,omitempty"`
	RunID        string `json:"run_id,omitempty"`           // coordinator terminal identity
	RunTokenHash string `json:"run_token_sha256,omitempty"` // SHA-256 hex of the coordinator's SHEEPDOG_RUN_TOKEN
	Acts         []Act  `json:"acts,omitempty"`
	Wakes        []Wake `json:"wakes,omitempty"` // held until J12 delivers them
}

// Wake is a held wake for the coordinator (design.md 10, J12): queued once
// per task and key, delivered in one batch when the coordinator is idle.
// Facts are the task's facts when it was queued, built in code.
type Wake struct {
	Key   string    `json:"key"`
	Task  string    `json:"task,omitempty"`
	Why   string    `json:"why"`
	Facts string    `json:"facts"`
	At    time.Time `json:"at"`
}

// Outcome is one verifiable result of a task (design.md 3, 11.7; prompts.md 3.1):
// what is true when done and how it will be proved (cmd:, test:, or manual:).
type Outcome struct {
	Text     string `json:"text"`
	ProvedBy string `json:"proved_by"`
}

// UnmarshalJSON accepts both {text, proved_by} and legacy plain-string
// outcomes from stored state written before PR 3.2.
func (o *Outcome) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*o = Outcome{Text: s}
		return nil
	}
	type alias Outcome
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*o = Outcome(a)
	return nil
}

// Task is a planned task. It has no phase field: rows derive where it is.
type Task struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Brief    string    `json:"brief"`
	Outcomes []Outcome `json:"outcomes"`
	After    []string  `json:"after,omitempty"`
	// Scope fields of design.md 3, 11.7 (prompts.md 3.1).
	ForWhom   string `json:"for_whom,omitempty"`
	IfSkipped string `json:"if_skipped,omitempty"`
	Smallest  string `json:"smallest,omitempty"`
	NotDoing  string `json:"not_doing,omitempty"`
	// EstimateLines is the plan's estimate of changed lines (design.md 11.5).
	EstimateLines int       `json:"estimate_lines"`
	SplitFrom     string    `json:"split_from,omitempty"`
	Done          *Merged   `json:"done,omitempty"`
	Closed        *Closed   `json:"closed,omitempty"`
	Dropped       *Dropped  `json:"dropped,omitempty"`
	Split         *Split    `json:"split,omitempty"`
	Parked        bool      `json:"parked,omitempty"`
	Recaps        []Recap   `json:"recaps,omitempty"`
	Acts          []Act     `json:"acts,omitempty"`
	Attempts      []Attempt `json:"attempts"`
}

// Merged is the done mark: the task's PR merged at this commit.
type Merged struct {
	MergedCommit string `json:"merged_commit"`
}

// Closed marks a task closed without a merge.
type Closed struct {
	Why string `json:"why"`
}

// Dropped marks a task dropped by the coordinator (design.md 3, 4.9).
type Dropped struct {
	Why string `json:"why"`
}

// Split marks a task split into child tasks (design.md 3, 4.9).
type Split struct {
	Children []string `json:"children"`
}

// Recap is a non-convergence or limit stop waiting on the coordinator (design.md 3).
type Recap struct {
	Trigger string         `json:"trigger"`
	Allowed []string       `json:"allowed"`
	Decided *RecapDecision `json:"decided,omitempty"`
	Amended bool           `json:"amended,omitempty"` // a plan amend changed the brief or outcomes since it opened
}

// RecapDecision is the coordinator's decision closing a recap.
type RecapDecision struct {
	Choice string `json:"choice"`
	Why    string `json:"why"`
}

// Recap choices and triggers (design.md 4.3, 4.9).
const (
	ChoiceContinue = "continue"
	TriggerReview  = "review"
	TriggerSize    = "size"
)

// SizeRecapAllowed is a size recap's choices: continue only while the task
// has used fewer than continues_per_task (design.md 4.3).
func SizeRecapAllowed(t Task, s Settings) []string {
	used := 0
	for _, r := range t.Recaps {
		if r.Decided != nil && r.Decided.Choice == ChoiceContinue {
			used++
		}
	}
	if used < s.ContinuesPerTask {
		return []string{ChoiceContinue, "split", "new", "drop"}
	}
	return []string{"split", "new", "drop"}
}

// Ended reports whether t carries a terminal mark (design.md 4.1, N1).
func Ended(t Task) bool {
	return t.Done != nil || t.Closed != nil || t.Dropped != nil || t.Split != nil
}

// OpenRecap returns t's undecided recap, or nil when none is open.
func OpenRecap(t Task) *Recap {
	for i := len(t.Recaps) - 1; i >= 0; i-- {
		if t.Recaps[i].Decided == nil {
			return &t.Recaps[i]
		}
	}
	return nil
}

// Attempt is one try at a task, from its base commit.
type Attempt struct {
	BaseCommit   string     `json:"base_commit"`
	Branch       string     `json:"branch"`
	Worktree     string     `json:"worktree"`
	PR           int        `json:"pr"`
	RunID        string     `json:"run_id"`                     // the run's terminal identity
	RunTokenHash string     `json:"run_token_sha256,omitempty"` // SHA-256 hex of the run's SHEEPDOG_RUN_TOKEN
	Review       *ReviewRun `json:"review,omitempty"`
	Ready        []Ready    `json:"ready"`
	Rejects      []Reject   `json:"rejects,omitempty"`
	Rounds       []Round    `json:"rounds"`
	Accepts      []Accept   `json:"accepts"`
	Acts         []Act      `json:"acts"`
	// NothingToChange is the worker's reason for "nothing to change"; a later
	// ready or send-back clears it (design.md 3.2).
	NothingToChange string `json:"nothing_to_change,omitempty"`
	// Boundary is how many rounds were recorded when a plan amend last
	// changed the brief or outcomes: those rounds no longer cover (design.md 3.1).
	Boundary int    `json:"boundary,omitempty"`
	Closed   string `json:"closed,omitempty"` // the note a recap's "new" closed this attempt with
	// SizeCap is N12's limit on changed lines at the tip (design.md 3, 11.5).
	SizeCap int `json:"size_cap"`
	// MergeOnly are H's moves to a tip that was ahead of it by merges of the
	// base only (N20, design.md 3.1), in order.
	MergeOnly []Advance `json:"merge_only,omitempty"`
}

// Advance is one merge_only move of H, observed with git when it was made.
type Advance struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// OpenAttempt is t's current attempt, or nil before the first or once a
// recap's "new" closed it.
func OpenAttempt(t Task) *Attempt {
	if n := len(t.Attempts); n > 0 && t.Attempts[n-1].Closed == "" {
		return &t.Attempts[n-1]
	}
	return nil
}

// Counted is a's rounds after its boundary: the only ones that can cover.
func Counted(a Attempt) []Round { return a.Rounds[min(a.Boundary, len(a.Rounds)):] }

// ReviewRun is the active reviewer run started for the attempt's next round.
type ReviewRun struct {
	RunID        string   `json:"run_id,omitempty"`
	RunTokenHash string   `json:"run_token_sha256,omitempty"`
	Round        int      `json:"round"`
	Kind         string   `json:"kind,omitempty"`
	Head         string   `json:"head"`
	Code         string   `json:"code"`
	Reviewer     Reviewer `json:"reviewer"`
	// IdleSince is when a pass first read the run idle with no valid tag:
	// the stuck row's clock (S2). The record goes when the round ends.
	IdleSince *time.Time `json:"idle_since,omitempty"`
}

// Ready is the worker's "done at this commit".
type Ready struct {
	Head string `json:"head"`
}

// Reject is the worker's rejection of every finding of a reviewer's round at
// Head with no new commit (design.md 3: "A reject is not a round").
type Reject struct {
	Head  string `json:"head"`
	Round int    `json:"round"`
	Why   string `json:"why"`
}

// Reviewer names who reviewed a round.
type Reviewer struct {
	CLI    string `json:"cli"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// Round is one review round (kind: full, delta, retro or send_back).
type Round struct {
	N          int      `json:"n"`
	Kind       string   `json:"kind"`
	Head       string   `json:"head"`
	Reviewer   Reviewer `json:"reviewer"`
	Verdict    string   `json:"verdict"`
	Majors     int      `json:"majors"`
	ReportPath string   `json:"report_path"`
	Why        string   `json:"why,omitempty"` // a send-back's reason
}

// Accept records that the bar (or an override) accepted a commit.
type Accept struct {
	Head   string `json:"head"`
	By     string `json:"by"` // bar or override
	Rounds []int  `json:"rounds"`
}

// Act is one entry of the engine's own ledger: a thing it did or tried.
type Act struct {
	Key    string    `json:"key"`
	Result string    `json:"result"`
	At     time.Time `json:"at"`
}

// Done reports whether an act with key succeeded ("done").
func Done(acts []Act, key string) bool {
	for _, a := range acts {
		if a.Key == key && a.Result == "done" {
			return true
		}
	}
	return false
}

// Count is how many times key was tried, whatever the result.
func Count(acts []Act, key string) int {
	n := 0
	for _, a := range acts {
		if a.Key == key {
			n++
		}
	}
	return n
}
