// Package flow holds the engine's records, settings and rule tables. It is
// pure: no I/O and no clock; the time comes in as an argument
// (docs/dev/engine-v2/design.md sections 2.1 and 13).
package flow

import "time"

// Roles of Slice 1 (design.md 9.1).
const (
	RoleCoordinator = "coordinator"
	RoleWorker      = "worker"
	RoleReviewer    = "reviewer"
)

// Round kinds of slice 2 (design.md 3, 4.6).
const (
	RoundFull  = "full"
	RoundDelta = "delta"
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

// Task is a planned task. It has no phase field: rows derive where it is.
type Task struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Brief    string    `json:"brief"`
	Outcomes []string  `json:"outcomes"`
	After    []string  `json:"after,omitempty"`
	Done     *Merged   `json:"done,omitempty"`
	Closed   *Closed   `json:"closed,omitempty"`
	Acts     []Act     `json:"acts,omitempty"`
	Attempts []Attempt `json:"attempts"`
}

// Merged is the done mark: the task's PR merged at this commit.
type Merged struct {
	MergedCommit string `json:"merged_commit"`
}

// Closed marks a task closed without a merge.
type Closed struct {
	Why string `json:"why"`
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
}

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
