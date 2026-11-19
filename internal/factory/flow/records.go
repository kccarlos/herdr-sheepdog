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

// State is one project's state.json. Seq counts the inputs committed to it.
type State struct {
	Seq      int64    `json:"seq"`
	Project  Project  `json:"project"`
	Settings Settings `json:"settings"`
	Tasks    []Task   `json:"tasks"`
}

// Project is the project record of the skeleton.
type Project struct {
	ID      string `json:"id"`
	Folder  string `json:"folder,omitempty"`
	Goal    string `json:"goal,omitempty"`
	Started bool   `json:"started,omitempty"`
	RunID   string `json:"run_id,omitempty"` // coordinator terminal identity
}

// Task is a planned task. It has no phase field: rows derive where it is.
type Task struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Brief    string    `json:"brief"`
	Outcomes []string  `json:"outcomes"`
	Done     *Merged   `json:"done,omitempty"`
	Closed   *Closed   `json:"closed,omitempty"`
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
	BaseCommit string   `json:"base_commit"`
	Branch     string   `json:"branch"`
	Worktree   string   `json:"worktree"`
	PR         int      `json:"pr"`
	RunID      string   `json:"run_id"` // the run's terminal identity
	Ready      []Ready  `json:"ready"`
	Rounds     []Round  `json:"rounds"`
	Accepts    []Accept `json:"accepts"`
	Acts       []Act    `json:"acts"`
}

// Ready is the worker's "done at this commit".
type Ready struct {
	Head string `json:"head"`
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
